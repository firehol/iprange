//! Bounded, page-backed arrival-order range assignment.
//!
//! This is private transaction machinery.  It stores only sparse binary-prefix
//! nodes in the transaction page pool, then emits one canonical range stream
//! directly into the normal range-tree builder.  It never sorts input or
//! retains an input/output-sized heap collection.

use crate::contract::{ValueKind, PAGE_SIZE};
use crate::key::IpKey;
use crate::private_page_pool::{
    PrivatePageOwner, PrivatePagePool, PrivatePagePoolCheckpoint, PrivatePagePoolError,
    PrivatePageReturn,
};
use crate::range_builder::{
    RangeTreeBuildError, RangeTreeBuildResult, RangeTreeBuildStartError, RangeTreeBuildWorkspace,
    RangeTreeBuilder, RangeTreePageSink,
};
use crate::range_page::RangeRecord;

const NODE_BYTES: usize = 32;
const NODES_PER_PAGE: usize = PAGE_SIZE / NODE_BYTES;
const NODE_NONE: u64 = u64::MAX;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum AssignmentMode {
    None,
    Value,
    Clear,
}

impl AssignmentMode {
    const fn wire(self) -> u8 {
        match self {
            Self::None => 0,
            Self::Value => 1,
            Self::Clear => 2,
        }
    }

    const fn from_wire(value: u8) -> Option<Self> {
        match value {
            0 => Some(Self::None),
            1 => Some(Self::Value),
            2 => Some(Self::Clear),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct AssignmentTag {
    ordinal: u64,
    mode: AssignmentMode,
    value: u32,
}

impl AssignmentTag {
    const NONE: Self = Self {
        ordinal: 0,
        mode: AssignmentMode::None,
        value: 0,
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct AssignmentNode {
    tag: AssignmentTag,
    left: u64,
    right: u64,
}

impl AssignmentNode {
    const EMPTY: Self = Self {
        tag: AssignmentTag::NONE,
        left: NODE_NONE,
        right: NODE_NONE,
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct NodeRef(u64);

impl NodeRef {
    const NONE: Self = Self(NODE_NONE);

    const fn is_none(self) -> bool {
        self.0 == NODE_NONE
    }

    fn new(page: usize, node: usize) -> Result<Self, SequentialAssignmentError> {
        if page > u32::MAX as usize || node >= NODES_PER_PAGE {
            return Err(SequentialAssignmentError::WorkspacePageLimit);
        }
        Ok(Self(((page as u64) << 32) | node as u64))
    }

    fn parts(self) -> Option<(usize, usize)> {
        if self.is_none() {
            return None;
        }
        let page = (self.0 >> 32) as usize;
        let node = self.0 as u32 as usize;
        (node < NODES_PER_PAGE).then_some((page, node))
    }
}

/// One caller-owned normalizer page slot. The page bytes themselves are owned
/// by the transaction-private pool; this remembers only its physical page and
/// how many fixed node records were initialized in it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct SequentialAssignmentPage {
    pgno: Option<u32>,
    used: u16,
}

impl SequentialAssignmentPage {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: None,
            used: 0,
        }
    }
}

/// Fixed caller-owned control storage for one private assignment engine.
///
/// It deliberately contains no node bytes: each node is packed into a page
/// claimed from the associated transaction pool.
#[derive(Debug)]
pub(crate) struct SequentialAssignmentWorkspace<'storage> {
    pages: &'storage mut [SequentialAssignmentPage],
}

impl<'storage> SequentialAssignmentWorkspace<'storage> {
    pub(crate) fn new(pages: &'storage mut [SequentialAssignmentPage]) -> Self {
        Self { pages }
    }

    fn is_clean(&self) -> bool {
        self.pages
            .iter()
            .all(|page| page.pgno.is_none() && page.used == 0)
    }

    fn reset(&mut self) {
        for page in &mut *self.pages {
            *page = SequentialAssignmentPage::empty();
        }
    }

    /// Clears stale private page references after the enclosing checkpoint was
    /// successfully rolled back. Calling this before rollback would lose the
    /// caller's only page inventory, so it remains crate-private.
    pub(crate) fn discard_after_rollback(&mut self) {
        self.reset();
    }

    pub(crate) const fn page_capacity(&self) -> usize {
        self.pages.len()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SequentialAssignmentError {
    PendingTransactionMismatch { requested: u64, pool_pending: u64 },
    WorkspaceBusy,
    WorkspacePageLimit,
    AssignmentBudget { required: u64, actual: u64 },
    WorkBudget { required: u64, actual: u64 },
    MutationBudget { required: usize, actual: usize },
    OrdinalExhausted,
    RangeReversed,
    MembershipValueZero,
    InvalidNodeReference,
    InvalidNodeEncoding,
    Failed,
    Pool(PrivatePagePoolError),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum SequentialAssignmentFinalizeError<E> {
    Engine(SequentialAssignmentError),
    BuildStart(RangeTreeBuildStartError),
    Build(RangeTreeBuildError<E>),
}

/// An active sparse prefix assignment map for one pool checkpoint.
///
/// `max_assignments`, `max_work`, and `max_mutations` are supplied by the
/// owning transaction resource ledger. They make every input-dependent cost
/// explicit before a public workflow exists.
#[derive(Debug)]
pub(crate) struct SequentialAssignmentEngine<'pool, 'slots, 'workspace, 'storage, K: IpKey> {
    pool: &'pool PrivatePagePool<'slots>,
    checkpoint: &'pool PrivatePagePoolCheckpoint<'slots>,
    workspace: &'workspace mut SequentialAssignmentWorkspace<'storage>,
    born_txn: u64,
    value_kind: ValueKind,
    max_assignments: u64,
    max_work: u64,
    max_mutations: usize,
    assignments: u64,
    work: u64,
    mutations: usize,
    page_count: usize,
    ordinal: u64,
    root: NodeRef,
    finished: bool,
    failed: bool,
    _key: core::marker::PhantomData<K>,
}

impl<'pool, 'slots, 'workspace, 'storage, K: IpKey>
    SequentialAssignmentEngine<'pool, 'slots, 'workspace, 'storage, K>
{
    /// Reserves checkpoint epoch headroom for this bounded private engine.
    ///
    /// A node read reissues one private authority; a node write reissues an
    /// authority and performs one mutable borrow. The formula deliberately
    /// over-reserves every pool slot for output and rollback, then adds the
    /// caller's bounded node-work allowance. It reserves only a counter, never
    /// memory or a file.
    pub(crate) fn preflight_checkpoint(
        pool: &PrivatePagePool<'slots>,
        workspace_pages: usize,
        max_work: u64,
    ) -> Result<PrivatePagePoolCheckpoint<'slots>, PrivatePagePoolError> {
        let pool_slots = pool.len();
        let work = usize::try_from(max_work).map_err(|_| PrivatePagePoolError::EpochExhausted)?;
        let steps = pool_slots
            .checked_mul(3)
            .and_then(|value| value.checked_add(work.checked_mul(2)?))
            .and_then(|value| value.checked_add(workspace_pages.checked_mul(3)?))
            .and_then(|value| value.checked_add(2))
            .ok_or(PrivatePagePoolError::EpochExhausted)?;
        pool.preflight_checkpoint_steps(steps)
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        pool: &'pool PrivatePagePool<'slots>,
        checkpoint: &'pool PrivatePagePoolCheckpoint<'slots>,
        workspace: &'workspace mut SequentialAssignmentWorkspace<'storage>,
        born_txn: u64,
        value_kind: ValueKind,
        max_assignments: u64,
        max_work: u64,
        max_mutations: usize,
    ) -> Result<Self, SequentialAssignmentError> {
        let pool_pending = pool.pending_txn();
        if born_txn != pool_pending {
            return Err(SequentialAssignmentError::PendingTransactionMismatch {
                requested: born_txn,
                pool_pending,
            });
        }
        if workspace.pages.len() > u32::MAX as usize {
            return Err(SequentialAssignmentError::WorkspacePageLimit);
        }
        if !workspace.is_clean() {
            return Err(SequentialAssignmentError::WorkspaceBusy);
        }
        pool.validate_checkpoint_handle(checkpoint)
            .map_err(SequentialAssignmentError::Pool)?;
        Ok(Self {
            pool,
            checkpoint,
            workspace,
            born_txn,
            value_kind,
            max_assignments,
            max_work,
            max_mutations,
            assignments: 0,
            work: 0,
            mutations: 0,
            page_count: 0,
            ordinal: 0,
            root: NodeRef::NONE,
            finished: false,
            failed: false,
            _key: core::marker::PhantomData,
        })
    }

    pub(crate) const fn work(&self) -> u64 {
        self.work
    }

    pub(crate) const fn assignments(&self) -> u64 {
        self.assignments
    }

    pub(crate) fn assign(
        &mut self,
        from: K,
        to: K,
        value: u32,
    ) -> Result<(), SequentialAssignmentError> {
        if self.finished {
            return Err(SequentialAssignmentError::WorkspaceBusy);
        }
        if self.failed {
            return Err(SequentialAssignmentError::Failed);
        }
        if self.value_kind == ValueKind::Membership && value == 0 {
            return Err(self.fail(SequentialAssignmentError::MembershipValueZero));
        }
        self.apply(from, to, AssignmentMode::Value, value)
    }

    pub(crate) fn clear(&mut self, from: K, to: K) -> Result<(), SequentialAssignmentError> {
        self.apply(from, to, AssignmentMode::Clear, 0)
    }

    fn apply(
        &mut self,
        from: K,
        to: K,
        mode: AssignmentMode,
        value: u32,
    ) -> Result<(), SequentialAssignmentError> {
        if self.finished {
            return Err(SequentialAssignmentError::WorkspaceBusy);
        }
        if self.failed {
            return Err(SequentialAssignmentError::Failed);
        }
        if from > to {
            return Err(self.fail(SequentialAssignmentError::RangeReversed));
        }
        if self.assignments == self.max_assignments {
            return Err(self.fail(SequentialAssignmentError::AssignmentBudget {
                required: self.assignments.saturating_add(1),
                actual: self.max_assignments,
            }));
        }
        let ordinal = match self.ordinal.checked_add(1) {
            Some(ordinal) => ordinal,
            None => return Err(self.fail(SequentialAssignmentError::OrdinalExhausted)),
        };
        let tag = AssignmentTag {
            ordinal,
            mode,
            value,
        };
        let root = match self.apply_node(self.root, 0, 0, from.to_u128(), to.to_u128(), tag) {
            Ok(root) => root,
            Err(error) => return Err(self.fail(error)),
        };
        self.root = root;
        self.ordinal = ordinal;
        self.assignments += 1;
        Ok(())
    }

    fn fail(&mut self, error: SequentialAssignmentError) -> SequentialAssignmentError {
        self.failed = true;
        error
    }

    fn apply_node(
        &mut self,
        reference: NodeRef,
        depth: u8,
        prefix: u128,
        from: u128,
        to: u128,
        tag: AssignmentTag,
    ) -> Result<NodeRef, SequentialAssignmentError> {
        let bits = key_bits::<K>();
        let (lower, upper) = region_bounds(bits, depth, prefix);
        if to < lower || upper < from {
            return Ok(reference);
        }

        let reference = if reference.is_none() {
            self.allocate_node()?
        } else {
            reference
        };
        let mut node = self.read_node(reference)?;
        if from <= lower && upper <= to {
            node.tag = tag;
            self.write_node(reference, node)?;
            return Ok(reference);
        }
        if depth == bits {
            return Err(SequentialAssignmentError::InvalidNodeEncoding);
        }

        let next_depth = depth + 1;
        let right_prefix = lower | (1u128 << (bits - next_depth));
        let (_, left_upper) = region_bounds(bits, next_depth, lower);
        let (right_lower, _) = region_bounds(bits, next_depth, right_prefix);
        let mut changed = false;
        if from <= left_upper && lower <= to {
            let child = self.apply_node(node_ref(node.left), next_depth, lower, from, to, tag)?;
            if child != node_ref(node.left) {
                node.left = child.0;
                changed = true;
            }
        }
        if from <= upper && right_lower <= to {
            let child = self.apply_node(
                node_ref(node.right),
                next_depth,
                right_prefix,
                from,
                to,
                tag,
            )?;
            if child != node_ref(node.right) {
                node.right = child.0;
                changed = true;
            }
        }
        if changed {
            self.write_node(reference, node)?;
        }
        Ok(reference)
    }

    fn allocate_node(&mut self) -> Result<NodeRef, SequentialAssignmentError> {
        self.reserve_mutation()?;
        if self.page_count == 0
            || usize::from(self.workspace.pages[self.page_count - 1].used) == NODES_PER_PAGE
        {
            if self.page_count == self.workspace.pages.len() {
                return Err(SequentialAssignmentError::WorkspacePageLimit);
            }
            self.pool
                .validate_checkpoint_handle(self.checkpoint)
                .map_err(SequentialAssignmentError::Pool)?;
            let authority = self
                .pool
                .claim_lowest(
                    PrivatePageOwner::Normalization,
                    self.born_txn,
                    K::FAMILY as u64,
                )
                .map_err(SequentialAssignmentError::Pool)?;
            self.workspace.pages[self.page_count] = SequentialAssignmentPage {
                pgno: Some(authority.page_number()),
                used: 0,
            };
            self.page_count += 1;
        }
        let page = self.page_count - 1;
        let node = usize::from(self.workspace.pages[page].used);
        let reference = NodeRef::new(page, node)?;
        self.workspace.pages[page].used = self.workspace.pages[page]
            .used
            .checked_add(1)
            .ok_or(SequentialAssignmentError::WorkspacePageLimit)?;
        self.write_node_reserved(reference, AssignmentNode::EMPTY)?;
        Ok(reference)
    }

    fn charge_work(&mut self) -> Result<(), SequentialAssignmentError> {
        if self.work == self.max_work {
            return Err(SequentialAssignmentError::WorkBudget {
                required: self.work.saturating_add(1),
                actual: self.max_work,
            });
        }
        self.work += 1;
        Ok(())
    }

    fn reserve_mutation(&mut self) -> Result<(), SequentialAssignmentError> {
        self.charge_work()?;
        if self.mutations == self.max_mutations {
            return Err(SequentialAssignmentError::MutationBudget {
                required: self.mutations.saturating_add(1),
                actual: self.max_mutations,
            });
        }
        self.mutations += 1;
        Ok(())
    }

    fn node_page(&self, reference: NodeRef) -> Result<(u32, usize), SequentialAssignmentError> {
        let (page_index, node_index) = reference
            .parts()
            .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
        let page = self
            .workspace
            .pages
            .get(page_index)
            .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
        if node_index >= usize::from(page.used) {
            return Err(SequentialAssignmentError::InvalidNodeReference);
        }
        let pgno = page
            .pgno
            .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
        Ok((pgno, node_index * NODE_BYTES))
    }

    fn read_node(
        &mut self,
        reference: NodeRef,
    ) -> Result<AssignmentNode, SequentialAssignmentError> {
        self.charge_work()?;
        let (pgno, offset) = self.node_page(reference)?;
        self.pool
            .validate_checkpoint_handle(self.checkpoint)
            .map_err(SequentialAssignmentError::Pool)?;
        let authority = self
            .pool
            .authority(pgno, PrivatePageOwner::Normalization, self.born_txn)
            .map_err(SequentialAssignmentError::Pool)?;
        let bytes = self
            .pool
            .borrow_page(&authority)
            .map_err(SequentialAssignmentError::Pool)?;
        decode_node(&bytes[offset..offset + NODE_BYTES])
    }

    fn write_node(
        &mut self,
        reference: NodeRef,
        node: AssignmentNode,
    ) -> Result<(), SequentialAssignmentError> {
        self.reserve_mutation()?;
        self.write_node_reserved(reference, node)
    }

    fn write_node_reserved(
        &mut self,
        reference: NodeRef,
        node: AssignmentNode,
    ) -> Result<(), SequentialAssignmentError> {
        let (pgno, offset) = self.node_page(reference)?;
        self.pool
            .validate_checkpoint_handle(self.checkpoint)
            .map_err(SequentialAssignmentError::Pool)?;
        let authority = self
            .pool
            .authority(pgno, PrivatePageOwner::Normalization, self.born_txn)
            .map_err(SequentialAssignmentError::Pool)?;
        let mut bytes = self
            .pool
            .borrow_page_mut(&authority)
            .map_err(SequentialAssignmentError::Pool)?;
        encode_node(&mut bytes[offset..offset + NODE_BYTES], node);
        Ok(())
    }

    pub(crate) fn build_final_tree<S: RangeTreePageSink>(
        &mut self,
        tree_workspace: &mut RangeTreeBuildWorkspace<K>,
        sink: &mut S,
    ) -> Result<RangeTreeBuildResult, SequentialAssignmentFinalizeError<S::Error>> {
        if self.finished {
            return Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::WorkspaceBusy,
            ));
        }
        if self.failed {
            return Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed,
            ));
        }
        let mut builder = match tree_workspace.begin(
            self.born_txn,
            self.value_kind,
            self.pool.pending_page_count(),
        ) {
            Ok(builder) => builder,
            Err(error) => {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::BuildStart(error));
            }
        };
        let mut pending = None;
        if let Err(error) = self.emit_node(
            self.root,
            0,
            0,
            AssignmentTag::NONE,
            &mut builder,
            sink,
            &mut pending,
        ) {
            self.failed = true;
            return Err(error.into());
        }
        if let Some(record) = pending {
            if let Err(error) = builder.push(sink, record) {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::Build(error));
            }
        }
        let result = match builder.finish(sink) {
            Ok(result) => result,
            Err(error) => {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::Build(error));
            }
        };
        if let Err(error) = self.return_workspace_pages() {
            self.failed = true;
            return Err(SequentialAssignmentFinalizeError::Engine(error));
        }
        self.finished = true;
        Ok(result)
    }

    // The recursive state is fixed-depth and deliberately passed explicitly.
    #[allow(clippy::too_many_arguments)]
    fn emit_node<S: RangeTreePageSink>(
        &mut self,
        reference: NodeRef,
        depth: u8,
        prefix: u128,
        inherited: AssignmentTag,
        builder: &mut RangeTreeBuilder<'_, K>,
        sink: &mut S,
        pending: &mut Option<RangeRecord<K>>,
    ) -> Result<(), EmitError<S::Error>> {
        let bits = key_bits::<K>();
        if reference.is_none() {
            return self.emit_region(depth, prefix, inherited, builder, sink, pending);
        }
        let node = self.read_node(reference).map_err(EmitError::Engine)?;
        let effective =
            if node.tag.mode != AssignmentMode::None && node.tag.ordinal > inherited.ordinal {
                node.tag
            } else {
                inherited
            };
        if depth == bits || (node.left == NODE_NONE && node.right == NODE_NONE) {
            return self.emit_region(depth, prefix, effective, builder, sink, pending);
        }
        let (lower, _) = region_bounds(bits, depth, prefix);
        let next_depth = depth + 1;
        self.emit_node(
            node_ref(node.left),
            next_depth,
            lower,
            effective,
            builder,
            sink,
            pending,
        )?;
        let right_prefix = lower | (1u128 << (bits - next_depth));
        self.emit_node(
            node_ref(node.right),
            next_depth,
            right_prefix,
            effective,
            builder,
            sink,
            pending,
        )
    }

    fn emit_region<S: RangeTreePageSink>(
        &mut self,
        depth: u8,
        prefix: u128,
        tag: AssignmentTag,
        builder: &mut RangeTreeBuilder<'_, K>,
        sink: &mut S,
        pending: &mut Option<RangeRecord<K>>,
    ) -> Result<(), EmitError<S::Error>> {
        if tag.mode != AssignmentMode::Value {
            return Ok(());
        }
        let (from, to) = region_bounds(key_bits::<K>(), depth, prefix);
        let record = RangeRecord {
            from: key_from_u128::<K>(from),
            to: key_from_u128::<K>(to),
            value: tag.value,
        };
        if let Some(mut prior) = *pending {
            if prior.value == record.value && prior.to.checked_inc() == Some(record.from) {
                prior.to = record.to;
                *pending = Some(prior);
                return Ok(());
            }
            builder.push(sink, prior).map_err(EmitError::Build)?;
        }
        *pending = Some(record);
        Ok(())
    }

    fn return_workspace_pages(&mut self) -> Result<(), SequentialAssignmentError> {
        for page in self.workspace.pages.iter_mut().take(self.page_count) {
            let pgno = page
                .pgno
                .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
            self.pool
                .validate_checkpoint_handle(self.checkpoint)
                .map_err(SequentialAssignmentError::Pool)?;
            let authority = self
                .pool
                .authority(pgno, PrivatePageOwner::Normalization, self.born_txn)
                .map_err(SequentialAssignmentError::Pool)?;
            self.pool
                .return_page(authority, PrivatePageReturn::Available)
                .map_err(|(_, error)| SequentialAssignmentError::Pool(error))?;
            *page = SequentialAssignmentPage::empty();
        }
        self.page_count = 0;
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum EmitError<E> {
    Engine(SequentialAssignmentError),
    Build(RangeTreeBuildError<E>),
}

impl<E> From<EmitError<E>> for SequentialAssignmentFinalizeError<E> {
    fn from(value: EmitError<E>) -> Self {
        match value {
            EmitError::Engine(error) => Self::Engine(error),
            EmitError::Build(error) => Self::Build(error),
        }
    }
}

fn node_ref(raw: u64) -> NodeRef {
    NodeRef(raw)
}

fn encode_node(bytes: &mut [u8], node: AssignmentNode) {
    debug_assert_eq!(bytes.len(), NODE_BYTES);
    bytes.fill(0);
    bytes[0..8].copy_from_slice(&node.tag.ordinal.to_le_bytes());
    bytes[8..16].copy_from_slice(&node.left.to_le_bytes());
    bytes[16..24].copy_from_slice(&node.right.to_le_bytes());
    bytes[24..28].copy_from_slice(&node.tag.value.to_le_bytes());
    bytes[28] = node.tag.mode.wire();
}

fn decode_node(bytes: &[u8]) -> Result<AssignmentNode, SequentialAssignmentError> {
    if bytes.len() != NODE_BYTES || bytes[29..].iter().any(|&value| value != 0) {
        return Err(SequentialAssignmentError::InvalidNodeEncoding);
    }
    let mode = AssignmentMode::from_wire(bytes[28])
        .ok_or(SequentialAssignmentError::InvalidNodeEncoding)?;
    let ordinal = u64::from_le_bytes(bytes[0..8].try_into().unwrap());
    if mode == AssignmentMode::None && ordinal != 0 {
        return Err(SequentialAssignmentError::InvalidNodeEncoding);
    }
    if mode != AssignmentMode::None && ordinal == 0 {
        return Err(SequentialAssignmentError::InvalidNodeEncoding);
    }
    Ok(AssignmentNode {
        tag: AssignmentTag {
            ordinal,
            mode,
            value: u32::from_le_bytes(bytes[24..28].try_into().unwrap()),
        },
        left: u64::from_le_bytes(bytes[8..16].try_into().unwrap()),
        right: u64::from_le_bytes(bytes[16..24].try_into().unwrap()),
    })
}

fn key_bits<K: IpKey>() -> u8 {
    (K::WIDTH * 8) as u8
}

fn key_from_u128<K: IpKey>(value: u128) -> K {
    K::read_le(&value.to_le_bytes())
}

fn region_bounds(bits: u8, depth: u8, prefix: u128) -> (u128, u128) {
    debug_assert!(depth <= bits);
    let full = if bits == 128 {
        u128::MAX
    } else {
        (1u128 << bits) - 1
    };
    let suffix = if depth == bits { 0 } else { full >> depth };
    let lower = prefix & (full ^ suffix);
    (lower, lower | suffix)
}

#[cfg(test)]
// Explicit drops end workspace/checkpoint borrows before terminal assertions.
#[allow(clippy::drop_non_drop)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::AddressFamily;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::page::{PageHeader, PageType};
    use crate::private_page_pool::{
        PrivatePageAuthorization, PrivatePagePoolSlot, PrivatePagePoolState,
    };
    use crate::range_page::RangeLeaf;
    use crate::range_pool_sink::RangeTreePoolSink;
    use crate::range_reader::RangeTree;
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    #[derive(Debug)]
    struct TestSink {
        next_pgno: u32,
        pages: Vec<(u32, [u8; PAGE_SIZE])>,
    }

    impl TestSink {
        fn new() -> Self {
            Self {
                next_pgno: 2,
                pages: Vec::new(),
            }
        }
    }

    impl RangeTreePageSink for TestSink {
        type Error = ();

        fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
            let pgno = self.next_pgno;
            self.next_pgno += 1;
            self.pages.push((pgno, *page));
            Ok(pgno)
        }
    }

    #[derive(Debug)]
    struct FixedSink {
        next_pgno: u32,
        page: [u8; PAGE_SIZE],
        writes: usize,
    }

    impl FixedSink {
        fn new() -> Self {
            Self {
                next_pgno: 2,
                page: [0; PAGE_SIZE],
                writes: 0,
            }
        }

        fn reset(&mut self) {
            self.next_pgno = 2;
            self.writes = 0;
        }
    }

    impl RangeTreePageSink for FixedSink {
        type Error = ();

        fn write_range_page(&mut self, page: &[u8; PAGE_SIZE]) -> Result<u32, Self::Error> {
            if self.writes != 0 {
                return Err(());
            }
            self.page = *page;
            self.writes = 1;
            let pgno = self.next_pgno;
            self.next_pgno += 1;
            Ok(pgno)
        }
    }

    fn direct_v4_records(sink: &TestSink) -> Vec<RangeRecord<Ipv4Key>> {
        assert_eq!(sink.pages.len(), 1);
        let leaf =
            RangeLeaf::<Ipv4Key>::open(&sink.pages[0].1, 2, AddressFamily::Ipv4, ValueKind::Direct)
                .unwrap();
        (0..leaf.len())
            .map(|index| leaf.record(index).unwrap())
            .collect()
    }

    #[test]
    fn arrival_order_preserves_the_uncovered_sides_of_an_older_assignment() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            10_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            8,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(30), 1).unwrap();
        engine.assign(Ipv4Key(15), Ipv4Key(20), 2).unwrap();

        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let result = engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        assert_eq!(result.root_pgno, 2);
        assert_eq!(result.record_count, 3);
        assert_eq!(
            direct_v4_records(&sink),
            vec![
                RangeRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(14),
                    value: 1,
                },
                RangeRecord {
                    from: Ipv4Key(15),
                    to: Ipv4Key(20),
                    value: 2,
                },
                RangeRecord {
                    from: Ipv4Key(21),
                    to: Ipv4Key(30),
                    value: 1,
                },
            ]
        );

        drop(engine);
        assert!(normalizer.is_clean());
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn empty_input_needs_no_normalizer_or_output_page() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages: [SequentialAssignmentPage; 0] = [];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            1_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            0,
            1_000,
            1_000,
        )
        .unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        let result = engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        assert_eq!(result.root_pgno, 0);
        assert_eq!(result.record_count, 0);
        assert!(sink.pages.is_empty());
        assert_eq!(pool.available().unwrap(), 2);

        drop(engine);
        assert!(normalizer.is_clean());
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn final_tree_pages_are_claimed_from_the_actual_private_pool() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            10_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            4,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(20), 7).unwrap();

        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let result = {
            let mut sink =
                RangeTreePoolSink::new(&pool, &checkpoint, 2, AddressFamily::Ipv4).unwrap();
            engine
                .build_final_tree(&mut tree_workspace, &mut sink)
                .unwrap()
        };
        drop(engine);
        assert!(normalizer.is_clean());
        let range_slot = (0..3)
            .find(|&slot| {
                matches!(
                    pool.state(slot),
                    Ok(PrivatePagePoolState::InUse {
                        owner: PrivatePageOwner::Range,
                        ..
                    })
                )
            })
            .unwrap();
        assert_eq!(
            pool.state(range_slot).unwrap(),
            PrivatePagePoolState::InUse {
                owner: PrivatePageOwner::Range,
                owner_generation: 2,
                tag: 4,
            }
        );
        let page = pool.test_bytes(range_slot).unwrap();
        let header = PageHeader::decode(&page, 2).unwrap();
        assert_eq!(header.page_type, PageType::RangeLeaf);
        assert_eq!(header.aux, 4);
        assert_ne!(result.root_pgno, 0);
        let mut meta = empty_direct_meta(2);
        meta.address_family = AddressFamily::Ipv4;
        meta.value_kind = ValueKind::Direct;
        meta.page_count = 20;
        meta.range_root = result.root_pgno;
        meta.range_record_count = result.record_count;
        let mut image = vec![0; 20 * PAGE_SIZE];
        meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        let start = result.root_pgno as usize * PAGE_SIZE;
        image[start..start + PAGE_SIZE].copy_from_slice(&page);
        let tree = RangeTree::<Ipv4Key>::open_immutable(&image).unwrap();
        assert_eq!(
            tree.lookup(Ipv4Key(15)).unwrap(),
            Some(RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 7,
            })
        );

        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn direct_zero_is_valid_but_membership_zero_fails_before_claiming_a_page() {
        let mut direct_slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
        ];
        let direct_pool = PrivatePagePool::new(&mut direct_slots, 20, 20, 2).unwrap();
        let mut direct_pages = [SequentialAssignmentPage::empty(); 1];
        let mut direct_normalizer = SequentialAssignmentWorkspace::new(&mut direct_pages);
        let direct_checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &direct_pool,
            direct_normalizer.page_capacity(),
            1_000,
        )
        .unwrap();
        direct_pool
            .begin_checkpoint_prepared(&direct_checkpoint)
            .unwrap();
        let mut direct = SequentialAssignmentEngine::new(
            &direct_pool,
            &direct_checkpoint,
            &mut direct_normalizer,
            2,
            ValueKind::Direct,
            2,
            1_000,
            1_000,
        )
        .unwrap();
        direct.assign(Ipv4Key(4), Ipv4Key(5), 0).unwrap();
        let mut direct_tree = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut direct_sink = TestSink::new();
        direct
            .build_final_tree(&mut direct_tree, &mut direct_sink)
            .unwrap();
        assert_eq!(
            direct_v4_records(&direct_sink),
            vec![RangeRecord {
                from: Ipv4Key(4),
                to: Ipv4Key(5),
                value: 0,
            }]
        );
        drop(direct);
        direct_pool.rollback_checkpoint(direct_checkpoint).unwrap();

        let mut membership_slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
        ];
        let membership_pool = PrivatePagePool::new(&mut membership_slots, 20, 20, 2).unwrap();
        let mut membership_pages = [SequentialAssignmentPage::empty(); 1];
        let mut membership_normalizer = SequentialAssignmentWorkspace::new(&mut membership_pages);
        let membership_checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &membership_pool,
            membership_normalizer.page_capacity(),
            1_000,
        )
        .unwrap();
        membership_pool
            .begin_checkpoint_prepared(&membership_checkpoint)
            .unwrap();
        let mut membership = SequentialAssignmentEngine::new(
            &membership_pool,
            &membership_checkpoint,
            &mut membership_normalizer,
            2,
            ValueKind::Membership,
            2,
            1_000,
            1_000,
        )
        .unwrap();
        assert_eq!(
            membership.assign(Ipv4Key(4), Ipv4Key(5), 0),
            Err(SequentialAssignmentError::MembershipValueZero)
        );
        assert_eq!(membership_pool.available().unwrap(), 2);
        let mut membership_tree = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut membership_sink = TestSink::new();
        assert_eq!(
            membership.build_final_tree(&mut membership_tree, &mut membership_sink),
            Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed
            ))
        );
        drop(membership);
        assert!(membership_normalizer.is_clean());
        membership_pool
            .rollback_checkpoint(membership_checkpoint)
            .unwrap();
    }

    #[test]
    fn clear_removes_only_its_arrival_interval() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            10_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            4,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(30), 1).unwrap();
        engine.clear(Ipv4Key(15), Ipv4Key(20)).unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        assert_eq!(
            direct_v4_records(&sink),
            vec![
                RangeRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(14),
                    value: 1,
                },
                RangeRecord {
                    from: Ipv4Key(21),
                    to: Ipv4Key(30),
                    value: 1,
                },
            ]
        );

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn adjacent_equal_final_values_are_coalesced() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            10_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            4,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
        engine.assign(Ipv4Key(21), Ipv4Key(30), 7).unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        assert_eq!(
            direct_v4_records(&sink),
            vec![RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
                value: 7,
            }]
        );

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn small_per_address_oracle_matches_arrival_order_assignments_and_clears() {
        let mut slots: [PrivatePagePoolSlot; 64] = core::array::from_fn(|index| {
            PrivatePagePoolSlot::authorized(
                index as u32 * 2 + 3,
                PrivatePageAuthorization::CommittedFree,
            )
        });
        let pool = PrivatePagePool::new(&mut slots, 400, 400, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 32];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            1_000_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            128,
            1_000_000,
            1_000_000,
        )
        .unwrap();
        let mut want = [None; 256];
        let mut state = 0x9e37_79b9u32;
        for _ in 0..128 {
            state ^= state << 13;
            state ^= state >> 17;
            state ^= state << 5;
            let (from, to) = {
                let left = state & 0xff;
                let right = (state >> 8) & 0xff;
                if left <= right {
                    (left, right)
                } else {
                    (right, left)
                }
            };
            if state & 0x1_0000 != 0 {
                engine.clear(Ipv4Key(from), Ipv4Key(to)).unwrap();
                for address in from..=to {
                    want[address as usize] = None;
                }
            } else {
                let value = (state >> 17) & 3;
                engine.assign(Ipv4Key(from), Ipv4Key(to), value).unwrap();
                for address in from..=to {
                    want[address as usize] = Some(value);
                }
            }
        }
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        let mut got = [None; 256];
        for record in direct_v4_records(&sink) {
            assert!(
                record.to.0 <= 255,
                "unexpected range outside oracle: {record:?}"
            );
            for address in record.from.0..=record.to.0 {
                got[address as usize] = Some(record.value);
            }
        }
        assert_eq!(got, want);

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn full_ipv6_space_does_not_wrap_at_the_upper_boundary() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 3];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv6Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            100_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            4,
            100_000,
            100_000,
        )
        .unwrap();
        engine.assign(Ipv6Key::MIN, Ipv6Key::MAX, 1).unwrap();
        engine.assign(Ipv6Key::MAX, Ipv6Key::MAX, 2).unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut sink = TestSink::new();
        engine
            .build_final_tree(&mut tree_workspace, &mut sink)
            .unwrap();
        assert_eq!(sink.pages.len(), 1);
        let leaf =
            RangeLeaf::<Ipv6Key>::open(&sink.pages[0].1, 2, AddressFamily::Ipv6, ValueKind::Direct)
                .unwrap();
        assert_eq!(leaf.len(), 2);
        assert_eq!(
            leaf.record(0).unwrap(),
            RangeRecord {
                from: Ipv6Key::MIN,
                to: Ipv6Key {
                    hi: u64::MAX,
                    lo: u64::MAX - 1,
                },
                value: 1,
            }
        );
        assert_eq!(
            leaf.record(1).unwrap(),
            RangeRecord {
                from: Ipv6Key::MAX,
                to: Ipv6Key::MAX,
                value: 2,
            }
        );

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
    }

    #[test]
    fn workspace_exhaustion_blocks_finalization_until_the_draft_is_rolled_back() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv6Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            100_000,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            2,
            100_000,
            100_000,
        )
        .unwrap();
        assert_eq!(
            engine.assign(Ipv6Key { hi: 0, lo: 1 }, Ipv6Key { hi: 0, lo: 1 }, 1,),
            Err(SequentialAssignmentError::WorkspacePageLimit)
        );
        assert!(pool.available().unwrap() < 3);
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut sink = TestSink::new();
        assert_eq!(
            engine.build_final_tree(&mut tree_workspace, &mut sink),
            Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed
            ))
        );

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
        normalizer.discard_after_rollback();
        assert!(normalizer.is_clean());
        assert_eq!(pool.available().unwrap(), 3);
    }

    #[test]
    fn work_budget_exhaustion_blocks_finalization_until_the_draft_is_rolled_back() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
            &pool,
            normalizer.page_capacity(),
            1,
        )
        .unwrap();
        pool.begin_checkpoint_prepared(&checkpoint).unwrap();
        let mut engine = SequentialAssignmentEngine::new(
            &pool,
            &checkpoint,
            &mut normalizer,
            2,
            ValueKind::Direct,
            2,
            1,
            1_000,
        )
        .unwrap();
        assert_eq!(
            engine.assign(Ipv4Key(10), Ipv4Key(20), 1),
            Err(SequentialAssignmentError::WorkBudget {
                required: 2,
                actual: 1,
            })
        );
        assert!(pool.available().unwrap() < 4);
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = TestSink::new();
        assert_eq!(
            engine.build_final_tree(&mut tree_workspace, &mut sink),
            Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed
            ))
        );

        drop(engine);
        pool.rollback_checkpoint(checkpoint).unwrap();
        normalizer.discard_after_rollback();
        assert_eq!(pool.available().unwrap(), 4);
        assert!(normalizer.is_clean());
    }

    #[test]
    fn nested_arrival_order_work_grows_linearly() {
        fn run(count: u32) -> u64 {
            let mut slots = [
                PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(9, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(11, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(13, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(15, PrivatePageAuthorization::CommittedFree),
                PrivatePagePoolSlot::authorized(17, PrivatePageAuthorization::CommittedFree),
            ];
            let pool = PrivatePagePool::new(&mut slots, 40, 40, 2).unwrap();
            let mut normalizer_pages = [SequentialAssignmentPage::empty(); 8];
            let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
            let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
                &pool,
                normalizer.page_capacity(),
                1_000_000,
            )
            .unwrap();
            pool.begin_checkpoint_prepared(&checkpoint).unwrap();
            let mut engine = SequentialAssignmentEngine::new(
                &pool,
                &checkpoint,
                &mut normalizer,
                2,
                ValueKind::Direct,
                u64::from(count),
                1_000_000,
                1_000_000,
            )
            .unwrap();
            for value in 0..count {
                engine
                    .assign(Ipv4Key(value), Ipv4Key(u32::MAX - value), value & 1)
                    .unwrap();
            }
            let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
            let mut sink = TestSink::new();
            engine
                .build_final_tree(&mut tree_workspace, &mut sink)
                .unwrap();
            let work = engine.work();
            drop(engine);
            pool.rollback_checkpoint(checkpoint).unwrap();
            work
        }

        let small = run(32);
        let large = run(64);
        assert!(
            large <= small * 3,
            "nested work grew superlinearly: 32={small} 64={large}"
        );
    }

    #[test]
    fn hot_path_allocates_nothing_after_fixed_setup() {
        let mut slots = [
            PrivatePagePoolSlot::authorized(3, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(5, PrivatePageAuthorization::CommittedFree),
            PrivatePagePoolSlot::authorized(7, PrivatePageAuthorization::CommittedFree),
        ];
        let pool = PrivatePagePool::new(&mut slots, 20, 20, 2).unwrap();
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut sink = FixedSink::new();

        let (_, allocations) = count_thread_allocations(|| {
            let checkpoint = SequentialAssignmentEngine::<Ipv4Key>::preflight_checkpoint(
                &pool,
                normalizer.page_capacity(),
                10_000,
            )
            .unwrap();
            pool.begin_checkpoint_prepared(&checkpoint).unwrap();
            let mut engine = SequentialAssignmentEngine::new(
                &pool,
                &checkpoint,
                &mut normalizer,
                2,
                ValueKind::Direct,
                2,
                10_000,
                10_000,
            )
            .unwrap();
            engine.assign(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
            sink.reset();
            engine
                .build_final_tree(&mut tree_workspace, &mut sink)
                .unwrap();
            drop(engine);
            pool.rollback_checkpoint(checkpoint).unwrap();
        });
        assert_eq!(allocations, 0);
        assert_eq!(pool.available().unwrap(), 3);
        assert!(normalizer.is_clean());
    }
}
