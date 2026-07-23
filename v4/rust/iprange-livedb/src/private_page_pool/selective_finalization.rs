//! Target-local exact-scope finalization for the private page pool.

use super::{
    PrivatePageAuthorization, PrivatePageOwner, PrivatePagePool, PrivatePagePoolCommitment,
    PrivatePagePoolError, PrivatePagePoolSlot, PrivatePageReservationScope, PrivatePageReturn,
    PrivatePageState, NO_SLOT,
};
use core::cell::Ref;
use core::marker::PhantomData;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivatePageSelectiveError {
    Pool(PrivatePagePoolError),
    Corrupt(u32),
    Scratch { required: usize, actual: usize },
    Overflow,
}

impl From<PrivatePagePoolError> for PrivatePageSelectiveError {
    fn from(value: PrivatePagePoolError) -> Self {
        Self::Pool(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SelectiveTree {
    Global,
    Scope,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
enum SelectiveReference {
    #[default]
    None,
    Slot(usize),
    Overlay(usize),
}

impl SelectiveReference {
    const fn empty() -> Self {
        Self::None
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSelectivePathEntry {
    reference: SelectiveReference,
}

impl PrivatePageSelectivePathEntry {
    pub(crate) const fn empty() -> Self {
        Self {
            reference: SelectiveReference::None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSelectiveOverlayNode {
    slot: usize,
    tree: Option<SelectiveTree>,
    left: SelectiveReference,
    right: SelectiveReference,
    final_left: usize,
    final_right: usize,
    height: u8,
    available: usize,
    in_use: usize,
    unscoped_available: usize,
    self_available: usize,
    self_in_use: usize,
    self_unscoped_available: usize,
    dirty: bool,
    path_ordinal: usize,
    successor: bool,
}

impl PrivatePageSelectiveOverlayNode {
    pub(crate) const fn empty() -> Self {
        Self {
            slot: NO_SLOT,
            tree: None,
            left: SelectiveReference::None,
            right: SelectiveReference::None,
            final_left: NO_SLOT,
            final_right: NO_SLOT,
            height: 0,
            available: 0,
            in_use: 0,
            unscoped_available: 0,
            self_available: 0,
            self_in_use: 0,
            self_unscoped_available: 0,
            dirty: false,
            path_ordinal: 0,
            successor: false,
        }
    }
}

pub(crate) struct PrivatePageSelectiveScratch<'scratch> {
    pub(crate) nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    pub(crate) path: &'scratch mut [PrivatePageSelectivePathEntry],
    pub(crate) targets: &'scratch mut [usize],
}

impl<'scratch> PrivatePageSelectiveScratch<'scratch> {
    pub(crate) fn new(
        nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
        path: &'scratch mut [PrivatePageSelectivePathEntry],
        targets: &'scratch mut [usize],
    ) -> Self {
        Self {
            nodes,
            path,
            targets,
        }
    }

    pub(crate) fn clear(&mut self) {
        self.nodes.fill(PrivatePageSelectiveOverlayNode::empty());
        self.path.fill(PrivatePageSelectivePathEntry::empty());
        self.targets.fill(NO_SLOT);
    }

    pub(crate) fn is_canonical(&self) -> bool {
        self.nodes
            .iter()
            .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            && self
                .path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            && self.targets.iter().all(|target| *target == NO_SLOT)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct PrivatePageFinalizedSlot {
    pgno: u32,
    authorization: PrivatePageAuthorization,
    state: PrivatePageState,
    bytes: [u8; super::PAGE_SIZE],
    adapter_owner: Option<PrivatePageOwner>,
    adapter_tag: u64,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct SelectiveNodeState {
    left: SelectiveReference,
    right: SelectiveReference,
    height: usize,
    available: usize,
    in_use: usize,
    unscoped_available: usize,
}

pub(crate) struct PreparedPrivatePageSelectiveDeletes<'scratch> {
    scratch: PrivatePageSelectiveScratch<'scratch>,
    node_len: usize,
    target_len: usize,
    index_root: SelectiveReference,
    scope_root: SelectiveReference,
    final_index_root: usize,
    final_scope_root: usize,
    work: u64,
    work_limit: u64,
    rotations: u64,
    successors: u64,
}

impl<'scratch> PreparedPrivatePageSelectiveDeletes<'scratch> {
    pub(crate) fn target_len(&self) -> usize {
        self.target_len
    }

    pub(crate) fn target(&self, index: usize) -> usize {
        self.scratch.targets[index]
    }

    #[cfg(test)]
    pub(crate) const fn test_work(&self) -> u64 {
        self.work
    }

    #[cfg(test)]
    pub(crate) const fn test_work_limit(&self) -> u64 {
        self.work_limit
    }

    #[cfg(test)]
    pub(crate) const fn test_node_len(&self) -> usize {
        self.node_len
    }

    pub(crate) fn into_scratch(self) -> PrivatePageSelectiveScratch<'scratch> {
        self.scratch
    }

    pub(crate) fn work(&self) -> u64 {
        self.work
    }

    pub(crate) fn work_limit(&self) -> u64 {
        self.work_limit
    }

    pub(crate) fn rotations(&self) -> u64 {
        self.rotations
    }

    pub(crate) fn successors(&self) -> u64 {
        self.successors
    }
}

fn maximum_avl_height(nodes: usize) -> usize {
    if nodes == 0 {
        return 0;
    }
    let (mut lower, mut current, mut height) = (0usize, 1usize, 1usize);
    loop {
        let Some(next) = current
            .checked_add(lower)
            .and_then(|value| value.checked_add(1))
        else {
            return height;
        };
        if next > nodes {
            return height;
        }
        lower = current;
        current = next;
        height += 1;
    }
}

pub(crate) fn private_page_selective_scratch_requirements(
    pool_slots: usize,
    targets: usize,
    refresh_pages: usize,
) -> Result<(usize, usize), PrivatePageSelectiveError> {
    if targets > pool_slots || refresh_pages > pool_slots {
        return Err(PrivatePageSelectiveError::Overflow);
    }
    if targets == 0 && refresh_pages == 0 {
        return Ok((0, 0));
    }
    let height = maximum_avl_height(pool_slots);
    let nodes = targets
        .checked_mul(6)
        .and_then(|value| value.checked_mul(height))
        .and_then(|value| {
            refresh_pages
                .checked_mul(2)
                .and_then(|refresh| refresh.checked_mul(height))
                .and_then(|refresh| value.checked_add(refresh))
        })
        .ok_or(PrivatePageSelectiveError::Overflow)?;
    Ok((nodes.min(pool_slots.saturating_mul(2)), height))
}

fn selective_work_limit(
    pool_slots: usize,
    targets: usize,
    refresh_pages: usize,
) -> Result<u64, PrivatePageSelectiveError> {
    if targets > pool_slots || refresh_pages > pool_slots {
        return Err(PrivatePageSelectiveError::Overflow);
    }
    let height = u64::try_from(maximum_avl_height(pool_slots))
        .map_err(|_| PrivatePageSelectiveError::Overflow)?;
    let targets = u64::try_from(targets).map_err(|_| PrivatePageSelectiveError::Overflow)?;
    let refresh = u64::try_from(refresh_pages).map_err(|_| PrivatePageSelectiveError::Overflow)?;
    6u64.checked_add(
        targets
            .checked_mul(192)
            .and_then(|value| value.checked_mul(height))
            .ok_or(PrivatePageSelectiveError::Overflow)?,
    )
    .and_then(|value| {
        refresh
            .checked_mul(16)
            .and_then(|refresh| refresh.checked_mul(height))
            .and_then(|refresh| value.checked_add(refresh))
    })
    .ok_or(PrivatePageSelectiveError::Overflow)
}

fn state_counts(
    slot: &PrivatePagePoolSlot,
) -> Result<(usize, usize, usize), PrivatePageSelectiveError> {
    match slot.state {
        PrivatePageState::Available => Ok((1, 0, usize::from(slot.scope_id == 0))),
        PrivatePageState::InUse { .. } => Ok((0, 1, 0)),
        PrivatePageState::PendingReturn { .. }
        | PrivatePageState::ReturnedFree
        | PrivatePageState::ReturnedTail => Ok((0, 0, 0)),
        PrivatePageState::Vacant => Err(PrivatePageSelectiveError::Corrupt(slot.pgno)),
    }
}

fn live_state(slot: &PrivatePagePoolSlot, tree: SelectiveTree) -> SelectiveNodeState {
    match tree {
        SelectiveTree::Global => SelectiveNodeState {
            left: if slot.index_left == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(slot.index_left)
            },
            right: if slot.index_right == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(slot.index_right)
            },
            height: usize::from(slot.index_height),
            available: slot.index_available,
            in_use: slot.index_in_use,
            unscoped_available: slot.index_unscoped_available,
        },
        SelectiveTree::Scope => SelectiveNodeState {
            left: if slot.scope_left == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(slot.scope_left)
            },
            right: if slot.scope_right == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(slot.scope_right)
            },
            height: usize::from(slot.scope_height),
            available: slot.scope_available,
            in_use: slot.scope_in_use,
            unscoped_available: 0,
        },
    }
}

struct SelectiveOverlay<'slots, 'scratch> {
    slots: &'slots [PrivatePagePoolSlot],
    scope_id: u64,
    scope_anchor: usize,
    nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    path: &'scratch mut [PrivatePageSelectivePathEntry],
    targets: &'scratch mut [usize],
    node_len: usize,
    index_root: SelectiveReference,
    scope_root: SelectiveReference,
    work: u64,
    work_limit: u64,
    rotations: u64,
    successors: u64,
    delete_ordinal: usize,
}

impl<'slots, 'scratch> SelectiveOverlay<'slots, 'scratch> {
    fn corrupt(&self, slot: usize) -> PrivatePageSelectiveError {
        PrivatePageSelectiveError::Corrupt(self.slots.get(slot).map_or(0, |entry| entry.pgno))
    }

    fn charge(&mut self) -> Result<(), PrivatePageSelectiveError> {
        if self.work >= self.work_limit {
            return Err(PrivatePageSelectiveError::Overflow);
        }
        self.work += 1;
        Ok(())
    }

    fn resolve(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
    ) -> Result<(usize, Option<usize>), PrivatePageSelectiveError> {
        self.charge()?;
        match reference {
            SelectiveReference::None => Ok((NO_SLOT, None)),
            SelectiveReference::Slot(slot) if slot < self.slots.len() => Ok((slot, None)),
            SelectiveReference::Overlay(index) if index < self.node_len => {
                let node = &self.nodes[index];
                if node.tree == Some(tree) && node.slot < self.slots.len() {
                    Ok((node.slot, Some(index)))
                } else {
                    Err(self.corrupt(node.slot))
                }
            }
            SelectiveReference::Slot(slot) => Err(self.corrupt(slot)),
            SelectiveReference::Overlay(_) => Err(PrivatePageSelectiveError::Corrupt(0)),
        }
    }

    fn materialize(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
    ) -> Result<(SelectiveReference, usize), PrivatePageSelectiveError> {
        let (slot, existing) = self.resolve(tree, reference)?;
        if slot == NO_SLOT {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        if let Some(index) = existing {
            return Ok((reference, index));
        }
        if self.node_len >= self.nodes.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: self.node_len + 1,
                actual: self.nodes.len(),
            });
        }
        let live = &self.slots[slot];
        let (self_available, self_in_use, self_unscoped_available) = state_counts(live)?;
        let (left, right, height, available, in_use, unscoped_available) = match tree {
            SelectiveTree::Global => (
                live.index_left,
                live.index_right,
                live.index_height,
                live.index_available,
                live.index_in_use,
                live.index_unscoped_available,
            ),
            SelectiveTree::Scope => (
                live.scope_left,
                live.scope_right,
                live.scope_height,
                live.scope_available,
                live.scope_in_use,
                0,
            ),
        };
        let index = self.node_len;
        self.node_len += 1;
        self.nodes[index] = PrivatePageSelectiveOverlayNode {
            slot,
            tree: Some(tree),
            left: if left == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(left)
            },
            right: if right == NO_SLOT {
                SelectiveReference::None
            } else {
                SelectiveReference::Slot(right)
            },
            final_left: NO_SLOT,
            final_right: NO_SLOT,
            height,
            available,
            in_use,
            unscoped_available,
            self_available,
            self_in_use,
            self_unscoped_available: match tree {
                SelectiveTree::Global => self_unscoped_available,
                SelectiveTree::Scope => 0,
            },
            dirty: false,
            path_ordinal: 0,
            successor: false,
        };
        Ok((SelectiveReference::Overlay(index), index))
    }

    fn state(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
    ) -> Result<SelectiveNodeState, PrivatePageSelectiveError> {
        let (slot, node) = self.resolve(tree, reference)?;
        if slot == NO_SLOT {
            return Ok(SelectiveNodeState::default());
        }
        if let Some(index) = node {
            let node = self.nodes[index];
            return Ok(SelectiveNodeState {
                left: node.left,
                right: node.right,
                height: usize::from(node.height),
                available: node.available,
                in_use: node.in_use,
                unscoped_available: node.unscoped_available,
            });
        }
        let live = &self.slots[slot];
        Ok(live_state(live, tree))
    }

    fn set_state(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
        state: SelectiveNodeState,
    ) -> Result<(), PrivatePageSelectiveError> {
        if state.height == 0 || state.height > maximum_avl_height(self.slots.len()) {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let (_, node) = self.resolve(tree, reference)?;
        let index = node.ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let node = &mut self.nodes[index];
        node.left = state.left;
        node.right = state.right;
        node.height =
            u8::try_from(state.height).map_err(|_| PrivatePageSelectiveError::Overflow)?;
        node.available = state.available;
        node.in_use = state.in_use;
        node.unscoped_available = state.unscoped_available;
        node.dirty = true;
        Ok(())
    }

    fn mark_path(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
        successor: bool,
    ) -> Result<(), PrivatePageSelectiveError> {
        let (_, node) = self.resolve(tree, reference)?;
        let index = node.ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let node = &mut self.nodes[index];
        if node.path_ordinal == 0 {
            node.path_ordinal = self.delete_ordinal + 1;
        }
        node.successor |= successor;
        Ok(())
    }

    fn validate_slot_role(
        &self,
        tree: SelectiveTree,
        slot_index: usize,
        lower: u64,
        upper: u64,
    ) -> Result<(), PrivatePageSelectiveError> {
        let slot = self
            .slots
            .get(slot_index)
            .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let page = u64::from(slot.pgno);
        if slot.authorization.is_none()
            || page < 2
            || page <= lower
            || page >= upper
            || (tree == SelectiveTree::Scope
                && (slot.scope_id != self.scope_id
                    || slot.scope_anchor_index != self.scope_anchor
                    || slot.scope_anchor != (slot_index == self.scope_anchor)))
        {
            return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
        }
        Ok(())
    }

    fn validate_local(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
        lower: u64,
        upper: u64,
    ) -> Result<SelectiveNodeState, PrivatePageSelectiveError> {
        let (slot, _) = self.resolve(tree, reference)?;
        if slot == NO_SLOT {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        self.validate_slot_role(tree, slot, lower, upper)?;
        let state = self.state(tree, reference)?;
        if state.height == 0 {
            return Err(self.corrupt(slot));
        }
        let page = u64::from(self.slots[slot].pgno);
        let mut children = [SelectiveNodeState::default(); 2];
        for (index, (child, child_lower, child_upper)) in
            [(state.left, lower, page), (state.right, page, upper)]
                .into_iter()
                .enumerate()
        {
            let (child_slot, _) = self.resolve(tree, child)?;
            if child_slot == NO_SLOT {
                continue;
            }
            if child_slot == slot {
                return Err(self.corrupt(slot));
            }
            self.validate_slot_role(tree, child_slot, child_lower, child_upper)?;
            children[index] = self.state(tree, child)?;
            if children[index].height == 0 {
                return Err(self.corrupt(child_slot));
            }
        }
        let left_height = children[0].height;
        let right_height = children[1].height;
        if left_height.abs_diff(right_height) > 1 {
            return Err(self.corrupt(slot));
        }
        let height = left_height.max(right_height) + 1;
        let (self_available, self_in_use, self_unscoped) = state_counts(&self.slots[slot])?;
        let available = children[0]
            .available
            .checked_add(children[1].available)
            .and_then(|value| value.checked_add(self_available))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let in_use = children[0]
            .in_use
            .checked_add(children[1].in_use)
            .and_then(|value| value.checked_add(self_in_use))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let unscoped = children[0]
            .unscoped_available
            .checked_add(children[1].unscoped_available)
            .and_then(|value| {
                value.checked_add(if tree == SelectiveTree::Global {
                    self_unscoped
                } else {
                    0
                })
            })
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        if state.height != height
            || state.available != available
            || state.in_use != in_use
            || state.unscoped_available != unscoped
            || available > self.slots.len()
            || in_use > self.slots.len() - available
            || unscoped > available
        {
            return Err(self.corrupt(slot));
        }
        Ok(state)
    }

    fn refresh(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
    ) -> Result<(), PrivatePageSelectiveError> {
        let mut state = self.state(tree, reference)?;
        let left = self.state(tree, state.left)?;
        let right = self.state(tree, state.right)?;
        let (_, node) = self.resolve(tree, reference)?;
        let node_index = node.ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let node = self.nodes[node_index];
        state.height = left.height.max(right.height) + 1;
        state.available = left
            .available
            .checked_add(right.available)
            .and_then(|value| value.checked_add(node.self_available))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        state.in_use = left
            .in_use
            .checked_add(right.in_use)
            .and_then(|value| value.checked_add(node.self_in_use))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        state.unscoped_available = left
            .unscoped_available
            .checked_add(right.unscoped_available)
            .and_then(|value| value.checked_add(node.self_unscoped_available))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        if left.height.abs_diff(right.height) > 2
            || state.available > self.slots.len()
            || state.in_use > self.slots.len() - state.available
            || state.unscoped_available > state.available
        {
            return Err(self.corrupt(node.slot));
        }
        self.set_state(tree, reference, state)
    }

    fn rotate_right(
        &mut self,
        tree: SelectiveTree,
        root: SelectiveReference,
    ) -> Result<SelectiveReference, PrivatePageSelectiveError> {
        self.rotations = self
            .rotations
            .checked_add(1)
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let (root, root_index) = self.materialize(tree, root)?;
        let mut root_state = self.state(tree, root)?;
        if root_state.left == SelectiveReference::None {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let (left, _) = self.materialize(tree, root_state.left)?;
        let mut left_state = self.state(tree, left)?;
        root_state.left = left_state.right;
        left_state.right = root;
        self.set_state(tree, root, root_state)?;
        self.set_state(tree, left, left_state)?;
        self.refresh(tree, root)?;
        self.refresh(tree, left)?;
        debug_assert_eq!(
            root_index,
            match root {
                SelectiveReference::Overlay(i) => i,
                _ => root_index,
            }
        );
        Ok(left)
    }

    fn rotate_left(
        &mut self,
        tree: SelectiveTree,
        root: SelectiveReference,
    ) -> Result<SelectiveReference, PrivatePageSelectiveError> {
        self.rotations = self
            .rotations
            .checked_add(1)
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let (root, _) = self.materialize(tree, root)?;
        let mut root_state = self.state(tree, root)?;
        if root_state.right == SelectiveReference::None {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let (right, _) = self.materialize(tree, root_state.right)?;
        let mut right_state = self.state(tree, right)?;
        root_state.right = right_state.left;
        right_state.left = root;
        self.set_state(tree, root, root_state)?;
        self.set_state(tree, right, right_state)?;
        self.refresh(tree, root)?;
        self.refresh(tree, right)?;
        Ok(right)
    }

    fn rebalance(
        &mut self,
        tree: SelectiveTree,
        root: SelectiveReference,
        lower: u64,
        upper: u64,
    ) -> Result<SelectiveReference, PrivatePageSelectiveError> {
        self.refresh(tree, root)?;
        let mut state = self.state(tree, root)?;
        let left = self.state(tree, state.left)?;
        let right = self.state(tree, state.right)?;
        let (root_slot, _) = self.resolve(tree, root)?;
        let page = u64::from(self.slots[root_slot].pgno);
        if left.height > right.height + 1 {
            let left_state = self.validate_local(tree, state.left, lower, page)?;
            let left_left = self.state(tree, left_state.left)?;
            let left_right = self.state(tree, left_state.right)?;
            if left_right.height > left_left.height {
                state.left = self.rotate_left(tree, state.left)?;
                self.set_state(tree, root, state)?;
            }
            return self.rotate_right(tree, root);
        }
        if right.height > left.height + 1 {
            let right_state = self.validate_local(tree, state.right, page, upper)?;
            let right_left = self.state(tree, right_state.left)?;
            let right_right = self.state(tree, right_state.right)?;
            if right_left.height > right_right.height {
                state.right = self.rotate_right(tree, state.right)?;
                self.set_state(tree, root, state)?;
            }
            return self.rotate_left(tree, root);
        }
        Ok(root)
    }

    fn detach_minimum(
        &mut self,
        tree: SelectiveTree,
        root: SelectiveReference,
        lower: u64,
        upper: u64,
        depth: usize,
    ) -> Result<(SelectiveReference, SelectiveReference), PrivatePageSelectiveError> {
        if depth >= self.path.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: depth + 1,
                actual: self.path.len(),
            });
        }
        let (root, _) = self.materialize(tree, root)?;
        self.path[depth].reference = root;
        self.mark_path(tree, root, true)?;
        let mut state = self.validate_local(tree, root, lower, upper)?;
        if state.left == SelectiveReference::None {
            return Ok((state.right, root));
        }
        let (root_slot, _) = self.resolve(tree, root)?;
        let page = u64::from(self.slots[root_slot].pgno);
        let (new_left, minimum) = self.detach_minimum(tree, state.left, lower, page, depth + 1)?;
        state.left = new_left;
        self.set_state(tree, root, state)?;
        Ok((self.rebalance(tree, root, lower, upper)?, minimum))
    }

    fn delete(
        &mut self,
        tree: SelectiveTree,
        root: SelectiveReference,
        target: SelectiveReference,
        lower: u64,
        upper: u64,
        depth: usize,
    ) -> Result<SelectiveReference, PrivatePageSelectiveError> {
        if depth >= self.path.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: depth + 1,
                actual: self.path.len(),
            });
        }
        let (root, _) = self.materialize(tree, root)?;
        self.path[depth].reference = root;
        self.mark_path(tree, root, false)?;
        let mut state = self.validate_local(tree, root, lower, upper)?;
        let (target_slot, _) = self.resolve(tree, target)?;
        let (root_slot, _) = self.resolve(tree, root)?;
        if target_slot == NO_SLOT || root_slot == NO_SLOT {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let target_page = self.slots[target_slot].pgno;
        let root_page = self.slots[root_slot].pgno;
        if target_page < root_page {
            state.left = self.delete(
                tree,
                state.left,
                target,
                lower,
                u64::from(root_page),
                depth + 1,
            )?;
            self.set_state(tree, root, state)?;
            return self.rebalance(tree, root, lower, upper);
        }
        if target_page > root_page {
            state.right = self.delete(
                tree,
                state.right,
                target,
                u64::from(root_page),
                upper,
                depth + 1,
            )?;
            self.set_state(tree, root, state)?;
            return self.rebalance(tree, root, lower, upper);
        }
        if root_slot != target_slot {
            return Err(self.corrupt(target_slot));
        }
        if state.left == SelectiveReference::None {
            return Ok(state.right);
        }
        if state.right == SelectiveReference::None {
            return Ok(state.left);
        }
        let (right, successor) =
            self.detach_minimum(tree, state.right, u64::from(root_page), upper, depth + 1)?;
        self.successors = self
            .successors
            .checked_add(1)
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let mut successor_state = self.state(tree, successor)?;
        successor_state.left = state.left;
        successor_state.right = right;
        self.set_state(tree, successor, successor_state)?;
        self.rebalance(tree, successor, lower, upper)
    }

    fn resolved_state(
        &mut self,
        tree: SelectiveTree,
        slot: usize,
        node: Option<usize>,
    ) -> Result<SelectiveNodeState, PrivatePageSelectiveError> {
        if slot == NO_SLOT {
            return Ok(SelectiveNodeState::default());
        }
        self.state(
            tree,
            node.map_or(SelectiveReference::Slot(slot), SelectiveReference::Overlay),
        )
    }

    fn prepare_refresh_local(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
        lower: u64,
        upper: u64,
    ) -> Result<(SelectiveReference, usize, SelectiveNodeState), PrivatePageSelectiveError> {
        let (reference, node_index) = self.materialize(tree, reference)?;
        let slot_index = self.nodes[node_index].slot;
        self.validate_slot_role(tree, slot_index, lower, upper)?;
        let state = self.state(tree, reference)?;
        let page = u64::from(self.slots[slot_index].pgno);
        let mut child_states = [SelectiveNodeState::default(); 2];
        for (child_index, (child, child_lower, child_upper)) in
            [(state.left, lower, page), (state.right, page, upper)]
                .into_iter()
                .enumerate()
        {
            let (child_slot, child_node) = self.resolve(tree, child)?;
            if child_slot == NO_SLOT {
                continue;
            }
            if child_slot == slot_index {
                return Err(self.corrupt(slot_index));
            }
            self.validate_slot_role(tree, child_slot, child_lower, child_upper)?;
            child_states[child_index] = self.resolved_state(tree, child_slot, child_node)?;
            if child_states[child_index].height == 0
                || child_states[child_index].available > self.slots.len()
                || child_states[child_index].in_use
                    > self.slots.len() - child_states[child_index].available
                || child_states[child_index].unscoped_available
                    > child_states[child_index].available
            {
                return Err(self.corrupt(child_slot));
            }
        }
        let node = self.nodes[node_index];
        let height = child_states[0].height.max(child_states[1].height) + 1;
        let available = child_states[0]
            .available
            .checked_add(child_states[1].available)
            .and_then(|value| value.checked_add(node.self_available))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let in_use = child_states[0]
            .in_use
            .checked_add(child_states[1].in_use)
            .and_then(|value| value.checked_add(node.self_in_use))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        let unscoped = child_states[0]
            .unscoped_available
            .checked_add(child_states[1].unscoped_available)
            .and_then(|value| value.checked_add(node.self_unscoped_available))
            .ok_or(PrivatePageSelectiveError::Overflow)?;
        if child_states[0].height.abs_diff(child_states[1].height) > 1
            || state.height != height
            || state.available != available
            || state.in_use != in_use
            || state.unscoped_available != unscoped
        {
            return Err(self.corrupt(slot_index));
        }
        Ok((reference, node_index, state))
    }

    fn prepare_refresh_node(
        &mut self,
        tree: SelectiveTree,
        reference: SelectiveReference,
    ) -> Result<(), PrivatePageSelectiveError> {
        let (_, node_index) = self.resolve(tree, reference)?;
        let node_index = node_index.ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let node = self.nodes[node_index];
        let left = self.state(tree, node.left)?;
        let right = self.state(tree, node.right)?;
        if left.height.abs_diff(right.height) > 1 {
            return Err(self.corrupt(node.slot));
        }
        let state = SelectiveNodeState {
            left: node.left,
            right: node.right,
            height: left.height.max(right.height) + 1,
            available: left
                .available
                .checked_add(right.available)
                .and_then(|value| value.checked_add(node.self_available))
                .ok_or(PrivatePageSelectiveError::Overflow)?,
            in_use: left
                .in_use
                .checked_add(right.in_use)
                .and_then(|value| value.checked_add(node.self_in_use))
                .ok_or(PrivatePageSelectiveError::Overflow)?,
            unscoped_available: left
                .unscoped_available
                .checked_add(right.unscoped_available)
                .and_then(|value| value.checked_add(node.self_unscoped_available))
                .ok_or(PrivatePageSelectiveError::Overflow)?,
        };
        if state.available > self.slots.len()
            || state.in_use > self.slots.len() - state.available
            || state.unscoped_available > state.available
        {
            return Err(self.corrupt(node.slot));
        }
        self.set_state(tree, reference, state)
    }

    fn prepare_refresh_path(
        &mut self,
        tree: SelectiveTree,
        mut root: SelectiveReference,
        target_slot: usize,
        desired: &PrivatePageFinalizedSlot,
    ) -> Result<SelectiveReference, PrivatePageSelectiveError> {
        let target = self
            .slots
            .get(target_slot)
            .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        if desired.pgno != target.pgno
            || target.authorization != Some(desired.authorization)
            || matches!(desired.state, PrivatePageState::PendingReturn { .. })
        {
            return Err(self.corrupt(target_slot));
        }
        let (desired_available, desired_in_use, desired_unscoped) = match desired.state {
            PrivatePageState::Available => (1, 0, usize::from(target.scope_id == 0)),
            PrivatePageState::InUse { .. } => (0, 1, 0),
            PrivatePageState::PendingReturn { .. }
            | PrivatePageState::ReturnedFree
            | PrivatePageState::ReturnedTail => (0, 0, 0),
            PrivatePageState::Vacant => return Err(self.corrupt(target_slot)),
        };
        let target_page = target.pgno;
        let mut reference = root;
        let (mut lower, mut upper) = (0u64, 1u64 << 32);
        let mut parent: Option<(usize, bool)> = None;
        for path_index in 0..maximum_avl_height(self.slots.len()) {
            if path_index >= self.path.len() {
                return Err(PrivatePageSelectiveError::Scratch {
                    required: path_index + 1,
                    actual: self.path.len(),
                });
            }
            let (prepared_reference, node_index, state) =
                self.prepare_refresh_local(tree, reference, lower, upper)?;
            if let Some((parent_index, left)) = parent {
                if left {
                    self.nodes[parent_index].left = prepared_reference;
                } else {
                    self.nodes[parent_index].right = prepared_reference;
                }
            } else {
                root = prepared_reference;
            }
            self.path[path_index].reference = prepared_reference;
            let path_len = path_index + 1;
            let slot_index = self.nodes[node_index].slot;
            let page = self.slots[slot_index].pgno;
            if target_page < page {
                parent = Some((node_index, true));
                reference = state.left;
                upper = u64::from(page);
            } else if target_page > page {
                parent = Some((node_index, false));
                reference = state.right;
                lower = u64::from(page);
            } else {
                if slot_index != target_slot {
                    return Err(self.corrupt(target_slot));
                }
                let node = &mut self.nodes[node_index];
                node.self_available = desired_available;
                node.self_in_use = desired_in_use;
                node.self_unscoped_available = if tree == SelectiveTree::Global {
                    desired_unscoped
                } else {
                    0
                };
                for index in (0..path_len).rev() {
                    self.prepare_refresh_node(tree, self.path[index].reference)?;
                }
                self.path[..path_len].fill(PrivatePageSelectivePathEntry::empty());
                return Ok(root);
            }
        }
        Err(self.corrupt(target_slot))
    }
}

impl<'slots> PrivatePagePool<'slots> {
    pub(crate) fn mutation_snapshot_in_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
    ) -> Result<super::PrivatePagePoolSnapshot<'slots>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        drop(slots);
        Ok(super::PrivatePagePoolSnapshot {
            pool_identity: self.identity,
            epoch: self.epoch.get(),
            operation_sequence: self.operation_sequence.get(),
            active_operation_id: self.active_operation_id.get(),
            operation_start_epoch: self.operation_start_epoch.get(),
            abort_required: self.abort_required.get(),
            _slots: PhantomData,
        })
    }

    pub(crate) fn shadow_claim_and_write(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        tag: u64,
        bytes: &[u8; super::PAGE_SIZE],
    ) -> Result<u64, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get_mut(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id
            || page.scope_anchor_index != scope.anchor
            || page.state != PrivatePageState::Available
        {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        let next_binding_epoch = page
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        page.state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        page.allocation_generation = self.generation.get();
        page.bytes = *bytes;
        page.binding_epoch = next_binding_epoch;
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        self.sync_aggregate_views(&slots);
        Ok(next_binding_epoch)
    }

    pub(crate) fn shadow_write(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        bytes: &[u8; super::PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get_mut(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        let PrivatePageState::InUse {
            owner: actual,
            owner_generation: actual_generation,
            tag,
            ..
        } = page.state
        else {
            return Err(PrivatePagePoolError::PageUnavailable(page.pgno));
        };
        if actual != owner || actual_generation != owner_generation {
            return Err(PrivatePagePoolError::OwnerMismatch {
                pgno: page.pgno,
                expected: owner,
                actual,
            });
        }
        page.state = PrivatePageState::InUse {
            owner,
            owner_generation,
            tag,
            authority_epoch: next_epoch,
        };
        page.bytes = *bytes;
        self.epoch.set(next_epoch);
        Ok(())
    }

    pub(crate) fn shadow_return(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        owner: PrivatePageOwner,
        owner_generation: u64,
        disposition: PrivatePageReturn,
    ) -> Result<u64, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let page = slots
            .get_mut(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != scope.anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        let PrivatePageState::InUse {
            owner: actual,
            owner_generation: actual_generation,
            ..
        } = page.state
        else {
            return Err(PrivatePagePoolError::PageUnavailable(page.pgno));
        };
        if actual != owner || actual_generation != owner_generation {
            return Err(PrivatePagePoolError::OwnerMismatch {
                pgno: page.pgno,
                expected: owner,
                actual,
            });
        }
        let next_binding_epoch = page
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        page.state = match disposition {
            PrivatePageReturn::Available => PrivatePageState::Available,
            PrivatePageReturn::Free => PrivatePageState::ReturnedFree,
            PrivatePageReturn::Tail => PrivatePageState::ReturnedTail,
        };
        page.allocation_generation = 0;
        page.binding_epoch = next_binding_epoch;
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        self.sync_aggregate_views(&slots);
        Ok(next_binding_epoch)
    }

    pub(crate) fn finalized_slot(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
    ) -> Result<PrivatePageFinalizedSlot, PrivatePageSelectiveError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let live = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if live.scope_id != scope.id
            || live.scope_anchor_index != scope.anchor
            || live.authorization.is_none()
            || live.state == PrivatePageState::Vacant
        {
            return Err(PrivatePageSelectiveError::Corrupt(live.pgno));
        }
        Ok(PrivatePageFinalizedSlot {
            pgno: live.pgno,
            authorization: live.authorization.expect("checked above"),
            state: live.state,
            bytes: live.bytes,
            adapter_owner: live.adapter_owner,
            adapter_tag: live.adapter_tag,
        })
    }

    pub(crate) fn install_finalized_slot_in_shadow(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        slot: usize,
        desired: &PrivatePageFinalizedSlot,
    ) -> Result<(), PrivatePageSelectiveError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive.into());
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_scope(&slots, scope)?;
        let live = slots
            .get_mut(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if live.scope_id != scope.id
            || live.scope_anchor_index != scope.anchor
            || live.pgno != desired.pgno
            || live.authorization != Some(desired.authorization)
        {
            return Err(PrivatePageSelectiveError::Corrupt(live.pgno));
        }
        let next_binding_epoch = live
            .binding_epoch
            .checked_add(1)
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        live.state = desired.state;
        live.allocation_generation =
            usize::from(matches!(desired.state, PrivatePageState::InUse { .. })) as u64;
        live.adapter_owner = desired.adapter_owner;
        live.adapter_tag = desired.adapter_tag;
        live.bytes = desired.bytes;
        live.binding_epoch = next_binding_epoch;
        self.epoch.set(next_epoch);
        self.refresh_slot_counts(&mut slots, slot);
        self.sync_aggregate_views(&slots);
        Ok(())
    }

    pub(crate) fn prepare_selective_deletes<'scratch>(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        scratch: PrivatePageSelectiveScratch<'scratch>,
        target_len: usize,
        refresh_pages: usize,
    ) -> Result<PreparedPrivatePageSelectiveDeletes<'scratch>, PrivatePageSelectiveError> {
        let work_limit = selective_work_limit(self.slot_count, target_len, refresh_pages)?;
        let (required_nodes, required_path) = private_page_selective_scratch_requirements(
            self.slot_count,
            target_len,
            refresh_pages,
        )?;
        if target_len > scratch.targets.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: target_len,
                actual: scratch.targets.len(),
            });
        }
        if required_nodes > scratch.nodes.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: required_nodes,
                actual: scratch.nodes.len(),
            });
        }
        if required_path > scratch.path.len() {
            return Err(PrivatePageSelectiveError::Scratch {
                required: required_path,
                actual: scratch.path.len(),
            });
        }
        if !scratch
            .nodes
            .iter()
            .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            || !scratch
                .path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
        {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let index_root = self.index_root.get();
        let scope_root = slots[anchor].scope_root;
        if target_len == 0 {
            return Ok(PreparedPrivatePageSelectiveDeletes {
                scratch,
                node_len: 0,
                target_len: 0,
                index_root: if index_root == NO_SLOT {
                    SelectiveReference::None
                } else {
                    SelectiveReference::Slot(index_root)
                },
                scope_root: if scope_root == NO_SLOT {
                    SelectiveReference::None
                } else {
                    SelectiveReference::Slot(scope_root)
                },
                final_index_root: NO_SLOT,
                final_scope_root: NO_SLOT,
                work: 0,
                work_limit,
                rotations: 0,
                successors: 0,
            });
        }
        let (node_len, work, rotations, successors, index_root, scope_root) = {
            let mut overlay = SelectiveOverlay {
                slots: &slots,
                scope_id: scope.id,
                scope_anchor: anchor,
                nodes: scratch.nodes,
                path: scratch.path,
                targets: scratch.targets,
                node_len: 0,
                index_root: if index_root == NO_SLOT {
                    SelectiveReference::None
                } else {
                    SelectiveReference::Slot(index_root)
                },
                scope_root: if scope_root == NO_SLOT {
                    SelectiveReference::None
                } else {
                    SelectiveReference::Slot(scope_root)
                },
                work: 0,
                work_limit,
                rotations: 0,
                successors: 0,
                delete_ordinal: 0,
            };
            for target_index in 0..target_len {
                let target = overlay.targets[target_index];
                if target >= overlay.slots.len() {
                    return Err(overlay.corrupt(target));
                }
                let live = &overlay.slots[target];
                if live.scope_id != scope.id
                    || live.scope_anchor_index != anchor
                    || live.authorization.is_none()
                {
                    return Err(overlay.corrupt(target));
                }
                overlay.delete_ordinal = target_index;
                overlay.index_root = overlay.delete(
                    SelectiveTree::Global,
                    overlay.index_root,
                    SelectiveReference::Slot(target),
                    0,
                    1u64 << 32,
                    0,
                )?;
                overlay.scope_root = overlay.delete(
                    SelectiveTree::Scope,
                    overlay.scope_root,
                    SelectiveReference::Slot(target),
                    0,
                    1u64 << 32,
                    0,
                )?;
            }
            (
                overlay.node_len,
                overlay.work,
                overlay.rotations,
                overlay.successors,
                overlay.index_root,
                overlay.scope_root,
            )
        };
        drop(slots);
        Ok(PreparedPrivatePageSelectiveDeletes {
            scratch,
            node_len,
            target_len,
            index_root,
            scope_root,
            final_index_root: NO_SLOT,
            final_scope_root: NO_SLOT,
            work,
            work_limit,
            rotations,
            successors,
        })
    }

    pub(crate) fn prepare_retained_refreshes(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &mut PreparedPrivatePageSelectiveDeletes<'_>,
        retained: &[usize],
        desired: &[PrivatePageFinalizedSlot],
    ) -> Result<(), PrivatePageSelectiveError> {
        if retained.len() != desired.len() {
            return Err(PrivatePageSelectiveError::Corrupt(0));
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let mut overlay = SelectiveOverlay {
            slots: &slots,
            scope_id: scope.id,
            scope_anchor: anchor,
            nodes: prepared.scratch.nodes,
            path: prepared.scratch.path,
            targets: prepared.scratch.targets,
            node_len: prepared.node_len,
            index_root: prepared.index_root,
            scope_root: prepared.scope_root,
            work: prepared.work,
            work_limit: prepared.work_limit,
            rotations: prepared.rotations,
            successors: prepared.successors,
            delete_ordinal: prepared.target_len,
        };
        for (&slot, desired) in retained.iter().zip(desired) {
            overlay.index_root = overlay.prepare_refresh_path(
                SelectiveTree::Global,
                overlay.index_root,
                slot,
                desired,
            )?;
            overlay.scope_root = overlay.prepare_refresh_path(
                SelectiveTree::Scope,
                overlay.scope_root,
                slot,
                desired,
            )?;
        }
        prepared.node_len = overlay.node_len;
        prepared.work = overlay.work;
        prepared.rotations = overlay.rotations;
        prepared.successors = overlay.successors;
        prepared.index_root = overlay.index_root;
        prepared.scope_root = overlay.scope_root;
        Ok(())
    }

    pub(crate) fn normalize_selective_deletes(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &mut PreparedPrivatePageSelectiveDeletes<'_>,
    ) -> Result<(), PrivatePageSelectiveError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        for index in 0..prepared.node_len {
            let tree = prepared.scratch.nodes[index]
                .tree
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
            let left = normalize_reference(
                slots.len(),
                prepared.scratch.nodes,
                prepared.node_len,
                tree,
                prepared.scratch.nodes[index].left,
                &mut prepared.work,
                prepared.work_limit,
            )?;
            let right = normalize_reference(
                slots.len(),
                prepared.scratch.nodes,
                prepared.node_len,
                tree,
                prepared.scratch.nodes[index].right,
                &mut prepared.work,
                prepared.work_limit,
            )?;
            prepared.scratch.nodes[index].final_left = left;
            prepared.scratch.nodes[index].final_right = right;
        }
        prepared.final_index_root = normalize_reference(
            slots.len(),
            prepared.scratch.nodes,
            prepared.node_len,
            SelectiveTree::Global,
            prepared.index_root,
            &mut prepared.work,
            prepared.work_limit,
        )?;
        prepared.final_scope_root = normalize_reference(
            slots.len(),
            prepared.scratch.nodes,
            prepared.node_len,
            SelectiveTree::Scope,
            prepared.scope_root,
            &mut prepared.work,
            prepared.work_limit,
        )?;
        let final_bound = slots[anchor]
            .scope_bound
            .checked_sub(prepared.target_len)
            .ok_or(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno))?;
        if (final_bound == 0) != (prepared.final_scope_root == NO_SLOT) {
            return Err(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno));
        }
        if prepared.node_len == 0 && prepared.target_len == 0 {
            validate_preserved_root(
                &slots,
                scope,
                SelectiveTree::Global,
                prepared.final_index_root,
                &mut prepared.work,
                prepared.work_limit,
            )?;
            validate_preserved_root(
                &slots,
                scope,
                SelectiveTree::Scope,
                prepared.final_scope_root,
                &mut prepared.work,
                prepared.work_limit,
            )?;
        }
        Ok(())
    }

    pub(crate) fn validate_selective_checkpoint_touches(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &PreparedPrivatePageSelectiveDeletes<'_>,
    ) -> Result<(), PrivatePageSelectiveError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive.into());
        }
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        if !checkpoint_tags_canonical(&slots[anchor]) {
            return Err(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno));
        }
        for &target in &prepared.scratch.targets[..prepared.target_len] {
            let slot = slots
                .get(target)
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
            if !checkpoint_tags_canonical(slot) {
                return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
            }
        }
        for node in &prepared.scratch.nodes[..prepared.node_len] {
            if node.dirty {
                let slot = slots
                    .get(node.slot)
                    .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
                if !checkpoint_tags_canonical(slot) {
                    return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
                }
            }
        }
        Ok(())
    }

    pub(crate) fn preflight_selective_finalization_epochs(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &PreparedPrivatePageSelectiveDeletes<'_>,
        expected_bound: usize,
    ) -> Result<(), PrivatePageSelectiveError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        slots[anchor]
            .scope_generation
            .checked_add(1)
            .ok_or(PrivatePagePoolError::GenerationExhausted)?;
        if slots[anchor].scope_sealed || slots[anchor].scope_bound != expected_bound {
            return Err(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno));
        }
        self.authorized_len
            .get()
            .checked_sub(prepared.target_len)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        self.pending_page_count
            .get()
            .checked_sub(
                u64::try_from(prepared.target_len)
                    .map_err(|_| PrivatePageSelectiveError::Overflow)?,
            )
            .ok_or(PrivatePagePoolError::StaleScope)?;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        let mut bound = 0usize;
        for ordinal in 0..capacity {
            let slot = slots
                .get(member)
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.scope_member_ordinal != ordinal
            {
                return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
            }
            if slot.authorization.is_some() {
                bound = bound
                    .checked_add(1)
                    .ok_or(PrivatePageSelectiveError::Overflow)?;
                slot.binding_epoch
                    .checked_add(1)
                    .ok_or(PrivatePagePoolError::EpochExhausted)?;
            }
            member = slot.scope_member_next;
        }
        if member != NO_SLOT || bound != expected_bound {
            return Err(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno));
        }
        Ok(())
    }

    pub(crate) fn preflight_selective_cleanup_epochs(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &PreparedPrivatePageSelectiveDeletes<'_>,
    ) -> Result<(), PrivatePageSelectiveError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let capacity = slots[anchor].scope_capacity;
        self.active_scopes
            .get()
            .checked_sub(1)
            .ok_or(PrivatePagePoolError::ActiveScopeUnderflow)?;
        self.validate_unscoped_vacancy_boundary(&slots)?;
        self.unscoped_vacant_count
            .get()
            .checked_add(capacity)
            .filter(|&count| count <= self.slot_count)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        self.authorized_len
            .get()
            .checked_sub(prepared.target_len)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        let mut member = slots[anchor].scope_member_head;
        let mut bound = 0usize;
        for ordinal in 0..capacity {
            let slot = slots
                .get(member)
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.scope_member_ordinal != ordinal
            {
                return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
            }
            if slot.authorization.is_some() {
                bound = bound
                    .checked_add(1)
                    .ok_or(PrivatePageSelectiveError::Overflow)?;
                if slot.scope_validation_marker != 0 {
                    return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
                }
            } else if !Self::is_canonical_vacant_payload(slot) {
                return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
            }
            slot.binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
            member = slot.scope_member_next;
        }
        if member != NO_SLOT || bound != prepared.target_len {
            return Err(PrivatePageSelectiveError::Corrupt(slots[anchor].pgno));
        }
        for &index in &prepared.scratch.targets[..prepared.target_len] {
            slots
                .get(index)
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?
                .binding_epoch
                .checked_add(2)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
        }
        Ok(())
    }

    pub(crate) fn preflight_selective_sealed_return_epochs(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
        prepared: &PreparedPrivatePageSelectiveDeletes<'_>,
    ) -> Result<(), PrivatePageSelectiveError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_scope(&slots, scope)?;
        let header = &slots[anchor];
        if !header.scope_sealed
            || header.scope_successor != nonce
            || !header.successor_consumed
            || prepared.target_len == 0
            || prepared.target_len > header.scope_bound
        {
            return Err(PrivatePageSelectiveError::Corrupt(header.pgno));
        }
        self.authorized_len
            .get()
            .checked_sub(prepared.target_len)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        for &target in &prepared.scratch.targets[..prepared.target_len] {
            let slot = slots
                .get(target)
                .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
            if slot.scope_id != scope.id
                || slot.scope_anchor_index != anchor
                || slot.authorization.is_none()
            {
                return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
            }
            slot.binding_epoch
                .checked_add(1)
                .ok_or(PrivatePagePoolError::EpochExhausted)?;
        }
        Ok(())
    }

    pub(crate) fn apply_selective_delete_trees_terminal_prepared(
        &self,
        checkpoint: &super::PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        prepared: &PreparedPrivatePageSelectiveDeletes<'_>,
    ) {
        if prepared.node_len == 0 && prepared.target_len == 0 {
            return;
        }
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared selective apply owns the pool mutation suffix");
        for node in &prepared.scratch.nodes[..prepared.node_len] {
            if !node.dirty {
                continue;
            }
            self.remember_index_in_journal(&mut slots, node.slot, checkpoint.generation);
            let slot = &mut slots[node.slot];
            match node.tree.expect("normalized node has a tree") {
                SelectiveTree::Global => {
                    slot.index_left = node.final_left;
                    slot.index_right = node.final_right;
                    slot.index_height = node.height;
                    slot.index_available = node.available;
                    slot.index_in_use = node.in_use;
                    slot.index_unscoped_available = node.unscoped_available;
                }
                SelectiveTree::Scope => {
                    slot.scope_left = node.final_left;
                    slot.scope_right = node.final_right;
                    slot.scope_height = node.height;
                    slot.scope_available = node.available;
                    slot.scope_in_use = node.in_use;
                }
            }
        }
        self.index_root.set(prepared.final_index_root);
        Self::remember_scope_header(&mut slots, scope.anchor, checkpoint.generation);
        slots[scope.anchor].scope_root = prepared.final_scope_root;
        self.available_count
            .set(if prepared.final_index_root == NO_SLOT {
                0
            } else {
                slots[prepared.final_index_root].index_available
            });
    }

    pub(crate) fn unbind_selective_target_terminal_prepared(
        &self,
        checkpoint: &super::PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
        index: usize,
        shrink_tail: bool,
    ) {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared selective unbind owns the pool mutation suffix");
        self.save_for_checkpoint(&mut slots[index]);
        self.remember_index_in_journal(&mut slots, index, checkpoint.generation);
        Self::remember_scope_header(&mut slots, scope.anchor, checkpoint.generation);
        slots[scope.anchor].scope_bound -= 1;
        let vacant_head = slots[scope.anchor].scope_vacant_head;
        let slot = &mut slots[index];
        slot.pgno = 0;
        slot.authorization = None;
        slot.state = PrivatePageState::Vacant;
        slot.allocation_generation = 0;
        slot.adapter_owner = None;
        slot.adapter_tag = 0;
        slot.bytes.fill(0);
        slot.scope_vacant_next = vacant_head;
        slot.index_left = NO_SLOT;
        slot.index_right = NO_SLOT;
        slot.index_height = 0;
        slot.index_available = 0;
        slot.index_in_use = 0;
        slot.index_unscoped_available = 0;
        slot.scope_left = NO_SLOT;
        slot.scope_right = NO_SLOT;
        slot.scope_height = 0;
        slot.scope_available = 0;
        slot.scope_in_use = 0;
        slot.binding_epoch += 1;
        slots[scope.anchor].scope_vacant_head = index;
        if shrink_tail {
            self.pending_page_count
                .set(self.pending_page_count.get() - 1);
        }
        self.authorized_len.set(self.authorized_len.get() - 1);
        self.advance_epoch_prepared();
    }

    pub(crate) fn copy_finalized_slot_terminal_prepared(
        &self,
        checkpoint: &super::PrivatePagePoolCheckpoint<'_>,
        index: usize,
        desired: &PrivatePageFinalizedSlot,
    ) {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared finalized copy owns the pool mutation suffix");
        self.save_for_checkpoint(&mut slots[index]);
        let next_authority_epoch = slots[index].binding_epoch + 1;
        let state = match desired.state {
            PrivatePageState::InUse { owner, tag, .. } => PrivatePageState::InUse {
                owner,
                owner_generation: self.pending_txn,
                tag,
                authority_epoch: next_authority_epoch,
            },
            PrivatePageState::ReturnedFree => PrivatePageState::ReturnedFree,
            _ => desired.state,
        };
        let slot = &mut slots[index];
        debug_assert_eq!(slot.pgno, desired.pgno);
        debug_assert_eq!(slot.authorization, Some(desired.authorization));
        slot.state = state;
        slot.allocation_generation = if matches!(state, PrivatePageState::InUse { .. }) {
            checkpoint.generation
        } else {
            0
        };
        slot.adapter_owner = desired.adapter_owner;
        slot.adapter_tag = desired.adapter_tag;
        slot.bytes = desired.bytes;
        slot.binding_epoch += 1;
        self.advance_epoch_prepared();
    }

    pub(crate) fn copy_finalized_from_pool_terminal_prepared(
        &self,
        checkpoint: &super::PrivatePagePoolCheckpoint<'_>,
        index: usize,
        desired_pool: &PrivatePagePool<'_>,
        desired_index: usize,
    ) {
        let desired_slots = desired_pool.slots.borrow();
        let desired = &desired_slots[desired_index];
        let snapshot = PrivatePageFinalizedSlot {
            pgno: desired.pgno,
            authorization: desired
                .authorization
                .expect("prepared desired slot is bound"),
            state: desired.state,
            bytes: desired.bytes,
            adapter_owner: desired.adapter_owner,
            adapter_tag: desired.adapter_tag,
        };
        self.copy_finalized_slot_terminal_prepared(checkpoint, index, &snapshot);
    }

    pub(crate) fn commit_selective_checkpoint_in_scope_terminal_prepared(
        &self,
        checkpoint: super::PrivatePagePoolCheckpoint<'_>,
        scope: &PrivatePageReservationScope<'_>,
    ) {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared selective commit owns the pool mutation suffix");
        let anchor = scope.anchor;
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            debug_assert!(!matches!(
                slots[member].state,
                PrivatePageState::PendingReturn { .. }
            ));
            if slots[member].checkpoint_generation == checkpoint.generation {
                slots[member].checkpoint_generation = 0;
                slots[member].saved_state = super::SavedState::None;
                slots[member].saved_binding = super::SavedBinding::None;
            }
            member = next;
        }
        if slots[anchor].saved_scope_generation == checkpoint.generation {
            slots[anchor].saved_scope_generation = 0;
        }
        self.clear_checkpoint_index_journal_prepared(&mut slots, checkpoint.generation);
        self.active_checkpoint.set(0);
        self.checkpoint_cleanup_slots.set(0);
        debug_assert!(self.epoch.get() < checkpoint.reserved_end_epoch);
        self.advance_epoch_prepared();
    }
}

fn normalize_reference(
    slot_count: usize,
    nodes: &[PrivatePageSelectiveOverlayNode],
    node_len: usize,
    tree: SelectiveTree,
    reference: SelectiveReference,
    work: &mut u64,
    work_limit: u64,
) -> Result<usize, PrivatePageSelectiveError> {
    if *work >= work_limit {
        return Err(PrivatePageSelectiveError::Overflow);
    }
    *work += 1;
    match reference {
        SelectiveReference::None => Ok(NO_SLOT),
        SelectiveReference::Slot(slot) if slot < slot_count => Ok(slot),
        SelectiveReference::Overlay(index)
            if index < node_len
                && nodes[index].tree == Some(tree)
                && nodes[index].slot < slot_count =>
        {
            Ok(nodes[index].slot)
        }
        _ => Err(PrivatePageSelectiveError::Corrupt(0)),
    }
}

fn validate_preserved_root(
    slots: &[PrivatePagePoolSlot],
    scope: &PrivatePageReservationScope<'_>,
    tree: SelectiveTree,
    root: usize,
    work: &mut u64,
    work_limit: u64,
) -> Result<(), PrivatePageSelectiveError> {
    if root == NO_SLOT {
        return Ok(());
    }
    let slot = slots
        .get(root)
        .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
    let page = u64::from(slot.pgno);
    if slot.authorization.is_none()
        || page < 2
        || (tree == SelectiveTree::Scope
            && (slot.scope_id != scope.id
                || slot.scope_anchor_index != scope.anchor
                || slot.scope_anchor != (root == scope.anchor)))
    {
        return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
    }
    let state = live_state(slot, tree);
    let (self_available, self_in_use, self_unscoped) = state_counts(slot)?;
    let mut children = [SelectiveNodeState::default(); 2];
    for (index, (reference, lower, upper)) in
        [(state.left, 0, page), (state.right, page, 1u64 << 32)]
            .into_iter()
            .enumerate()
    {
        let child = normalize_reference(slots.len(), &[], 0, tree, reference, work, work_limit)?;
        if child == NO_SLOT {
            continue;
        }
        let child_slot = slots
            .get(child)
            .ok_or(PrivatePageSelectiveError::Corrupt(0))?;
        let child_page = u64::from(child_slot.pgno);
        if child == root
            || child_slot.authorization.is_none()
            || child_page < 2
            || child_page <= lower
            || child_page >= upper
            || (tree == SelectiveTree::Scope
                && (child_slot.scope_id != scope.id
                    || child_slot.scope_anchor_index != scope.anchor
                    || child_slot.scope_anchor != (child == scope.anchor)))
        {
            return Err(PrivatePageSelectiveError::Corrupt(child_slot.pgno));
        }
        children[index] = live_state(child_slot, tree);
        if children[index].height == 0
            || children[index].available > slots.len()
            || children[index].in_use > slots.len() - children[index].available
            || children[index].unscoped_available > children[index].available
        {
            return Err(PrivatePageSelectiveError::Corrupt(child_slot.pgno));
        }
    }
    let height = children[0].height.max(children[1].height) + 1;
    let available = children[0]
        .available
        .checked_add(children[1].available)
        .and_then(|value| value.checked_add(self_available))
        .ok_or(PrivatePageSelectiveError::Overflow)?;
    let in_use = children[0]
        .in_use
        .checked_add(children[1].in_use)
        .and_then(|value| value.checked_add(self_in_use))
        .ok_or(PrivatePageSelectiveError::Overflow)?;
    let unscoped = children[0]
        .unscoped_available
        .checked_add(children[1].unscoped_available)
        .and_then(|value| {
            value.checked_add(if tree == SelectiveTree::Global {
                self_unscoped
            } else {
                0
            })
        })
        .ok_or(PrivatePageSelectiveError::Overflow)?;
    if children[0].height.abs_diff(children[1].height) > 1
        || state.height != height
        || state.available != available
        || state.in_use != in_use
        || state.unscoped_available != unscoped
    {
        return Err(PrivatePageSelectiveError::Corrupt(slot.pgno));
    }
    Ok(())
}

fn checkpoint_tags_canonical(slot: &PrivatePagePoolSlot) -> bool {
    slot.checkpoint_generation == 0
        && slot.saved_state == super::SavedState::None
        && slot.saved_binding == super::SavedBinding::None
        && slot.saved_index_generation == 0
        && slot.saved_index_next == NO_SLOT
        && slot.saved_scope_generation == 0
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivatePageSealedScopeStatus {
    pub(crate) capacity: usize,
    pub(crate) bound: usize,
    pub(crate) successor_consumed: bool,
}

impl<'slots> PrivatePagePool<'slots> {
    fn validate_sealed_scope_anchor(
        &self,
        slots: &[super::PrivatePagePoolSlot],
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<usize, PrivatePagePoolError> {
        if scope.pool_identity != self.identity
            || scope.pool_epoch != self.identity_epoch
            || scope.pending_txn != self.pending_txn
            || scope.anchor >= slots.len()
        {
            return Err(PrivatePagePoolError::PoolMismatch);
        }
        let anchor = &slots[scope.anchor];
        if scope.id == 0
            || nonce == 0
            || anchor.scope_id != scope.id
            || !anchor.scope_anchor
            || anchor.scope_anchor_index != scope.anchor
            || anchor.scope_generation != scope.generation
            || !anchor.scope_sealed
            || anchor.scope_successor != nonce
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        Ok(scope.anchor)
    }

    pub(crate) fn validate_sealed_scope(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<PrivatePageSealedScopeStatus, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_sealed_scope_anchor(&slots, scope, nonce)?;
        Ok(PrivatePageSealedScopeStatus {
            capacity: slots[anchor].scope_capacity,
            bound: slots[anchor].scope_bound,
            successor_consumed: slots[anchor].successor_consumed,
        })
    }

    pub(crate) fn exact_sealed_commitment_terminal_prepared(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> PrivatePagePoolCommitment {
        let slots = self
            .slots
            .try_borrow()
            .expect("prepared terminal commitment owns the pool suffix");
        let anchor = self
            .validate_sealed_scope_anchor(&slots, scope, nonce)
            .expect("prepared prior return retains the exact sealed scope");
        self.commitment_with_validated_anchor_at_epoch(&slots, scope, anchor, self.epoch.get())
            .expect("prepared sealed scope remains exact")
    }

    pub(crate) fn borrow_sealed_page(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
        slot: usize,
    ) -> Result<Ref<'_, [u8; super::PAGE_SIZE]>, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        self.validate_sealed_scope_anchor(&slots, scope, nonce)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id
            || page.scope_anchor_index != scope.anchor
            || !matches!(page.state, PrivatePageState::InUse { .. })
        {
            return Err(PrivatePagePoolError::InvalidState(slot));
        }
        Ok(Ref::map(slots, |slots| &slots[slot].bytes))
    }

    pub(crate) fn sealed_page_provenance(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
        slot: usize,
    ) -> Result<super::PrivatePageSealedProvenance, PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_sealed_scope_anchor(&slots, scope, nonce)?;
        let page = slots
            .get(slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(slot))?;
        if page.scope_id != scope.id || page.scope_anchor_index != anchor {
            return Err(PrivatePagePoolError::ScopeMismatch(page.pgno));
        }
        let (owner, owner_generation, tag) = match page.state {
            PrivatePageState::InUse {
                owner,
                owner_generation,
                tag,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                tag,
                ..
            } => (owner, owner_generation, tag),
            _ => return Err(PrivatePagePoolError::InvalidState(slot)),
        };
        Ok(super::PrivatePageSealedProvenance {
            scope_id: scope.id,
            scope_anchor: anchor,
            scope_generation: scope.generation,
            slot,
            pgno: page.pgno,
            binding_epoch: page.binding_epoch,
            owner,
            owner_generation,
            tag,
        })
    }

    pub(crate) fn copy_sealed_page_by_provenance(
        &self,
        provenance: &super::PrivatePageSealedProvenance,
        nonce: u64,
        destination: &mut [u8; super::PAGE_SIZE],
    ) -> Result<(), PrivatePagePoolError> {
        let slots = self
            .slots
            .try_borrow()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = slots
            .get(provenance.scope_anchor)
            .ok_or(PrivatePagePoolError::StaleScope)?;
        if provenance.scope_id == 0
            || nonce == 0
            || !anchor.scope_anchor
            || anchor.scope_id != provenance.scope_id
            || anchor.scope_anchor_index != provenance.scope_anchor
            || anchor.scope_generation != provenance.scope_generation
            || !anchor.scope_sealed
            || anchor.scope_successor != nonce
        {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let page = slots
            .get(provenance.slot)
            .ok_or(PrivatePagePoolError::SlotOutOfBounds(provenance.slot))?;
        let (owner, owner_generation) = match page.state {
            PrivatePageState::InUse {
                owner,
                owner_generation,
                ..
            }
            | PrivatePageState::PendingReturn {
                owner,
                owner_generation,
                ..
            } => (owner, owner_generation),
            _ => return Err(PrivatePagePoolError::InvalidState(provenance.slot)),
        };
        if page.scope_id != provenance.scope_id
            || page.scope_anchor_index != provenance.scope_anchor
            || page.pgno != provenance.pgno
            || page.binding_epoch != provenance.binding_epoch
            || owner != provenance.owner
            || owner_generation != provenance.owner_generation
        {
            return Err(PrivatePagePoolError::StaleAuthority);
        }
        destination.copy_from_slice(&page.bytes);
        Ok(())
    }

    pub(crate) fn consume_sealed_scope_successor(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<(), PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_sealed_scope_anchor(&slots, scope, nonce)?;
        if slots[anchor].successor_consumed {
            return Err(PrivatePagePoolError::StaleScope);
        }
        slots[anchor].successor_consumed = true;
        self.epoch.set(next_epoch);
        Ok(())
    }

    pub(crate) fn consume_sealed_scope_successor_with_commitment(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        if self.active_checkpoint.get() != 0 {
            return Err(PrivatePagePoolError::CheckpointActive);
        }
        let next_epoch = self.next_epoch()?;
        let mut slots = self
            .slots
            .try_borrow_mut()
            .map_err(|_| PrivatePagePoolError::BorrowConflict)?;
        let anchor = self.validate_sealed_scope_anchor(&slots, scope, nonce)?;
        if slots[anchor].successor_consumed {
            return Err(PrivatePagePoolError::StaleScope);
        }
        let commitment = self.commitment_with_slots_at_epoch(&slots, scope, next_epoch)?;
        slots[anchor].successor_consumed = true;
        self.epoch.set(next_epoch);
        Ok(commitment)
    }

    pub(crate) fn seal_scope_terminal_prepared(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) -> PrivatePageReservationScope<'slots> {
        debug_assert_ne!(nonce, 0);
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared finalization owns the pool mutation suffix");
        let anchor = scope.anchor;
        debug_assert_eq!(slots[anchor].scope_id, scope.id);
        debug_assert_eq!(slots[anchor].scope_generation, scope.generation);
        debug_assert!(!slots[anchor].scope_sealed);
        slots[anchor].scope_generation += 1;
        slots[anchor].scope_sealed = true;
        slots[anchor].scope_successor = nonce;
        slots[anchor].successor_consumed = false;
        self.advance_epoch_prepared();
        PrivatePageReservationScope {
            pool_identity: self.identity,
            pool_epoch: self.identity_epoch,
            id: scope.id,
            pending_txn: self.pending_txn,
            anchor,
            generation: slots[anchor].scope_generation,
            _pool: PhantomData,
        }
    }

    pub(crate) fn close_sealed_scope_terminal_prepared(
        &self,
        scope: &PrivatePageReservationScope<'_>,
        nonce: u64,
    ) {
        let mut slots = self
            .slots
            .try_borrow_mut()
            .expect("prepared sealed cleanup owns the pool mutation suffix");
        let anchor = scope.anchor;
        debug_assert_eq!(slots[anchor].scope_id, scope.id);
        debug_assert_eq!(slots[anchor].scope_generation, scope.generation);
        debug_assert!(slots[anchor].scope_sealed);
        debug_assert_eq!(slots[anchor].scope_successor, nonce);
        debug_assert!(slots[anchor].successor_consumed);
        debug_assert_eq!(slots[anchor].scope_bound, 0);
        debug_assert_eq!(slots[anchor].scope_root, NO_SLOT);
        let capacity = slots[anchor].scope_capacity;
        let mut member = slots[anchor].scope_member_head;
        for _ in 0..capacity {
            let next = slots[member].scope_member_next;
            let slot = &mut slots[member];
            debug_assert!(Self::is_canonical_vacant_payload(slot));
            slot.scope_id = 0;
            slot.scope_anchor = false;
            slot.scope_anchor_index = NO_SLOT;
            slot.scope_member_next = NO_SLOT;
            slot.scope_member_head = NO_SLOT;
            slot.scope_member_ordinal = NO_SLOT;
            slot.scope_validation_marker = 0;
            slot.scope_vacant_next = NO_SLOT;
            slot.scope_root = NO_SLOT;
            slot.scope_vacant_head = NO_SLOT;
            slot.scope_capacity = 0;
            slot.scope_bound = 0;
            slot.scope_generation = 0;
            slot.scope_sealed = false;
            slot.scope_successor = 0;
            slot.successor_consumed = false;
            slot.binding_epoch += 1;
            self.append_unscoped_vacancy_prepared(&mut slots, member);
            self.advance_epoch_prepared();
            member = next;
        }
        debug_assert_eq!(member, NO_SLOT);
        self.active_scopes.set(self.active_scopes.get() - 1);
    }
}
