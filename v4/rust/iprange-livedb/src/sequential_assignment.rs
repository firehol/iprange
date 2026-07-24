//! Bounded, page-backed arrival-order range assignment.
//!
//! This is private transaction machinery. It stores sparse binary-prefix nodes
//! in fixed caller-owned logical pages, then emits one canonical range stream
//! into logical range-page staging. It never sorts input, touches physical file
//! allocation, or retains an input/output-sized heap collection.

use crate::contract::{ValueKind, PAGE_SIZE};
use crate::key::IpKey;
use crate::range_builder::{
    RangeTreeBuildError, RangeTreeBuildStartError, RangeTreeBuildWorkspace, RangeTreeBuilder,
    RangeTreePageSink,
};
use crate::range_page::RangeRecord;
use crate::range_staging::{RangeTreeStagedResult, RangeTreeStaging, RangeTreeStagingError};

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

/// One caller-owned normalizer logical page. It has no physical v4 page
/// identity and is never handed to the transaction-private allocator.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct SequentialAssignmentPage {
    bytes: [u8; PAGE_SIZE],
    used: u16,
}

impl SequentialAssignmentPage {
    pub(crate) const fn empty() -> Self {
        Self {
            bytes: [0; PAGE_SIZE],
            used: 0,
        }
    }
}

/// Fixed caller-owned logical node storage for one private assignment engine.
#[derive(Debug)]
pub(crate) struct SequentialAssignmentWorkspace<'storage> {
    pages: &'storage mut [SequentialAssignmentPage],
}

impl<'storage> SequentialAssignmentWorkspace<'storage> {
    pub(crate) fn new(pages: &'storage mut [SequentialAssignmentPage]) -> Self {
        Self { pages }
    }

    fn is_clean(&self) -> bool {
        // Every node slot is fully initialized before its first read, so setup
        // checks only occupancy and does not scan caller memory as input data.
        self.pages.iter().all(|page| page.used == 0)
    }

    fn reset(&mut self) {
        for page in &mut *self.pages {
            *page = SequentialAssignmentPage::empty();
        }
    }

    /// Clears unpublished node bytes after the enclosing draft has been
    /// abandoned. The operation must not make a failed draft reusable.
    pub(crate) fn discard_after_abort(&mut self) {
        self.reset();
    }

    pub(crate) const fn page_capacity(&self) -> usize {
        self.pages.len()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SequentialAssignmentError {
    BornTransactionZero,
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
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum SequentialAssignmentFinalizeError {
    Engine(SequentialAssignmentError),
    BuildStart(RangeTreeBuildStartError),
    Build(RangeTreeBuildError<RangeTreeStagingError>),
    Staging(RangeTreeStagingError),
}

/// An active sparse prefix assignment map for one draft generation.
///
/// `max_assignments`, `max_work`, and `max_mutations` are supplied by the
/// owning transaction resource ledger. They make every input-dependent cost
/// explicit before a public workflow exists.
#[derive(Debug)]
pub(crate) struct SequentialAssignmentEngine<'workspace, 'storage, K: IpKey> {
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

impl<'workspace, 'storage, K: IpKey> SequentialAssignmentEngine<'workspace, 'storage, K> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        workspace: &'workspace mut SequentialAssignmentWorkspace<'storage>,
        born_txn: u64,
        value_kind: ValueKind,
        max_assignments: u64,
        max_work: u64,
        max_mutations: usize,
    ) -> Result<Self, SequentialAssignmentError> {
        if born_txn == 0 {
            return Err(SequentialAssignmentError::BornTransactionZero);
        }
        if workspace.pages.len() > u32::MAX as usize {
            return Err(SequentialAssignmentError::WorkspacePageLimit);
        }
        if !workspace.is_clean() {
            return Err(SequentialAssignmentError::WorkspaceBusy);
        }
        Ok(Self {
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
            self.workspace.pages[self.page_count] = SequentialAssignmentPage::empty();
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

    fn node_page(&self, reference: NodeRef) -> Result<(usize, usize), SequentialAssignmentError> {
        let (page_index, node_index) = reference
            .parts()
            .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
        if page_index >= self.page_count {
            return Err(SequentialAssignmentError::InvalidNodeReference);
        }
        let page = self
            .workspace
            .pages
            .get(page_index)
            .ok_or(SequentialAssignmentError::InvalidNodeReference)?;
        if node_index >= usize::from(page.used) {
            return Err(SequentialAssignmentError::InvalidNodeReference);
        }
        Ok((page_index, node_index * NODE_BYTES))
    }

    fn read_node(
        &mut self,
        reference: NodeRef,
    ) -> Result<AssignmentNode, SequentialAssignmentError> {
        self.charge_work()?;
        let (page_index, offset) = self.node_page(reference)?;
        decode_node(&self.workspace.pages[page_index].bytes[offset..offset + NODE_BYTES])
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
        let (page_index, offset) = self.node_page(reference)?;
        encode_node(
            &mut self.workspace.pages[page_index].bytes[offset..offset + NODE_BYTES],
            node,
        );
        Ok(())
    }

    pub(crate) fn build_staged_tree(
        &mut self,
        tree_workspace: &mut RangeTreeBuildWorkspace<K>,
        staging: &mut RangeTreeStaging<'_, K>,
    ) -> Result<RangeTreeStagedResult, SequentialAssignmentFinalizeError> {
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
            staging.logical_page_limit(),
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
            staging,
            &mut pending,
        ) {
            self.failed = true;
            return Err(error.into());
        }
        if let Some(record) = pending {
            if let Err(error) = builder.push(staging, record) {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::Build(error));
            }
        }
        let result = match builder.finish(staging) {
            Ok(result) => result,
            Err(error) => {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::Build(error));
            }
        };
        let staged = match staging.finish(result) {
            Ok(result) => result,
            Err(error) => {
                self.failed = true;
                return Err(SequentialAssignmentFinalizeError::Staging(error));
            }
        };
        self.clear_workspace_nodes();
        self.finished = true;
        Ok(staged)
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

    fn clear_workspace_nodes(&mut self) {
        self.workspace.reset();
        self.page_count = 0;
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum EmitError<E> {
    Engine(SequentialAssignmentError),
    Build(RangeTreeBuildError<E>),
}

impl From<EmitError<RangeTreeStagingError>> for SequentialAssignmentFinalizeError {
    fn from(value: EmitError<RangeTreeStagingError>) -> Self {
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
// Explicit drops end mutable workspace borrows before terminal assertions.
#[allow(clippy::drop_non_drop)]
mod tests {
    use super::*;
    use crate::contract::AddressFamily;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::private_page_pool::{PrivatePageAuthorization, PrivatePageCoordinatorTerminalPage};
    use crate::range_page::{RangeBranch, RangeLeaf};
    use crate::range_staging::{
        RangeTreePhysicalAssignment, RangeTreeStaging, RangeTreeStagingPage,
    };
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    fn assignment(pgno: u32) -> RangeTreePhysicalAssignment {
        RangeTreePhysicalAssignment {
            pgno,
            authorization: PrivatePageAuthorization::CommittedFree,
        }
    }

    fn staged_v4_records(
        staging: &RangeTreeStaging<'_, Ipv4Key>,
        staged: RangeTreeStagedResult,
    ) -> Vec<RangeRecord<Ipv4Key>> {
        assert_eq!(staged.page_count(), 1);
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        staging
            .materialize(staged, 8, &[assignment(3)], &mut terminal)
            .unwrap();
        let leaf = RangeLeaf::<Ipv4Key>::open(
            &terminal[0].bytes,
            2,
            AddressFamily::Ipv4,
            ValueKind::Direct,
        )
        .unwrap();
        (0..leaf.len())
            .map(|index| leaf.record(index).unwrap())
            .collect()
    }

    fn staged_v6_records(
        staging: &RangeTreeStaging<'_, Ipv6Key>,
        staged: RangeTreeStagedResult,
    ) -> Vec<RangeRecord<Ipv6Key>> {
        assert_eq!(staged.page_count(), 1);
        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        staging
            .materialize(staged, 8, &[assignment(3)], &mut terminal)
            .unwrap();
        let leaf = RangeLeaf::<Ipv6Key>::open(
            &terminal[0].bytes,
            2,
            AddressFamily::Ipv6,
            ValueKind::Direct,
        )
        .unwrap();
        (0..leaf.len())
            .map(|index| leaf.record(index).unwrap())
            .collect()
    }

    #[test]
    fn arrival_order_preserves_the_uncovered_sides_of_an_older_assignment() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut staging_pages = [RangeTreeStagingPage::empty(); 2];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(staged.logical_root(), 2);
        assert_eq!(staged.record_count, 3);
        drop(engine);
        assert!(normalizer.is_clean());
        assert_eq!(
            staged_v4_records(&staging, staged),
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
    }

    #[test]
    fn stages_logical_output_before_physical_materialization() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
            &mut normalizer,
            2,
            ValueKind::Direct,
            2,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(20), 7).unwrap();

        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(staged.logical_root(), 2);
        drop(engine);
        assert!(normalizer.is_clean());

        let mut terminal = [PrivatePageCoordinatorTerminalPage::empty(); 1];
        let materialized = staging
            .materialize(staged, 12, &[assignment(7)], &mut terminal)
            .unwrap();
        assert_eq!(materialized.root_pgno, 7);
        assert_eq!(terminal[0].pgno, 7);
        let leaf = RangeLeaf::<Ipv4Key>::open(
            &terminal[0].bytes,
            2,
            AddressFamily::Ipv4,
            ValueKind::Direct,
        )
        .unwrap();
        assert_eq!(
            leaf.record(0).unwrap(),
            RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 7,
            }
        );
    }

    #[test]
    fn empty_input_needs_no_normalizer_or_staged_output_page() {
        let mut normalizer_pages: [SequentialAssignmentPage; 0] = [];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine =
            SequentialAssignmentEngine::new(&mut normalizer, 2, ValueKind::Direct, 0, 1_000, 1_000)
                .unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(staged.logical_root(), 0);
        assert_eq!(staged.record_count, 0);
        assert_eq!(staged.page_count(), 0);
        drop(engine);
        assert!(normalizer.is_clean());
    }

    #[test]
    fn direct_zero_is_valid_but_membership_zero_fails_before_a_node_write() {
        let mut direct_pages = [SequentialAssignmentPage::empty(); 1];
        let mut direct_normalizer = SequentialAssignmentWorkspace::new(&mut direct_pages);
        let mut direct = SequentialAssignmentEngine::new(
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
        let mut direct_staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut direct_staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut direct_staging_pages, 2, ValueKind::Direct)
                .unwrap();
        let direct_staged = direct
            .build_staged_tree(&mut direct_tree, &mut direct_staging)
            .unwrap();
        assert_eq!(
            staged_v4_records(&direct_staging, direct_staged),
            vec![RangeRecord {
                from: Ipv4Key(4),
                to: Ipv4Key(5),
                value: 0,
            }]
        );
        drop(direct);
        assert!(direct_normalizer.is_clean());

        let mut membership_pages = [SequentialAssignmentPage::empty(); 1];
        let mut membership_normalizer = SequentialAssignmentWorkspace::new(&mut membership_pages);
        let mut membership = SequentialAssignmentEngine::new(
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
        drop(membership);
        assert!(membership_normalizer.is_clean());
    }

    #[test]
    fn clear_removes_only_its_arrival_interval() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut staging_pages = [RangeTreeStagingPage::empty(); 2];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(
            staged_v4_records(&staging, staged),
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
    }

    #[test]
    fn adjacent_equal_final_values_are_coalesced() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(
            staged_v4_records(&staging, staged),
            vec![RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(30),
                value: 7,
            }]
        );
    }

    #[test]
    fn small_per_address_oracle_matches_arrival_order_assignments_and_clears() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 32];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut staging_pages = [RangeTreeStagingPage::empty(); 2];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        let mut got = [None; 256];
        for record in staged_v4_records(&staging, staged) {
            assert!(
                record.to.0 <= 255,
                "unexpected range outside oracle: {record:?}"
            );
            for address in record.from.0..=record.to.0 {
                got[address as usize] = Some(record.value);
            }
        }
        assert_eq!(got, want);
    }

    #[test]
    fn full_ipv6_space_does_not_wrap_at_the_upper_boundary() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 3];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(
            staged_v6_records(&staging, staged),
            vec![
                RangeRecord {
                    from: Ipv6Key::MIN,
                    to: Ipv6Key {
                        hi: u64::MAX,
                        lo: u64::MAX - 1,
                    },
                    value: 1,
                },
                RangeRecord {
                    from: Ipv6Key::MAX,
                    to: Ipv6Key::MAX,
                    value: 2,
                },
            ]
        );
    }

    #[test]
    fn stages_and_materializes_a_multilevel_ipv4_tree() {
        let count = crate::range_page::leaf_capacity::<Ipv4Key>() + 1;
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 80];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
            &mut normalizer,
            2,
            ValueKind::Direct,
            count as u64,
            1_000_000,
            1_000_000,
        )
        .unwrap();
        for index in 0..count {
            let address = (index as u32) * 2;
            engine
                .assign(Ipv4Key(address), Ipv4Key(address), 1)
                .unwrap();
        }
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut staging_pages = [RangeTreeStagingPage::empty(); 3];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        let staged = engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .unwrap();
        assert_eq!(staged.page_count(), 3);
        drop(engine);
        assert!(normalizer.is_clean());

        let assignments = [assignment(3), assignment(9), assignment(17)];
        let mut terminal: [PrivatePageCoordinatorTerminalPage; 3] =
            core::array::from_fn(|_| PrivatePageCoordinatorTerminalPage::empty());
        let materialized = staging
            .materialize(staged, 20, &assignments, &mut terminal)
            .unwrap();
        assert_eq!(materialized.root_pgno, 17);
        let branch =
            RangeBranch::<Ipv4Key>::open(&terminal[2].bytes, 2, AddressFamily::Ipv4, 20).unwrap();
        assert_eq!(branch.entry(0).unwrap().child_pgno, 3);
        assert_eq!(branch.entry(1).unwrap().child_pgno, 9);
        assert!(crate::page::verify_crc32c(&terminal[2].bytes));
    }

    #[test]
    fn failure_requires_explicit_abort_before_workspace_reuse() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
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
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv6Key>::new();
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let mut staging =
            RangeTreeStaging::<Ipv6Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        assert_eq!(
            engine.build_staged_tree(&mut tree_workspace, &mut staging),
            Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed
            ))
        );
        drop(engine);
        assert!(!normalizer.is_clean());
        normalizer.discard_after_abort();
        staging.discard_after_abort();
        assert!(normalizer.is_clean());
        assert_eq!(staging_pages, [RangeTreeStagingPage::empty(); 1]);
    }

    #[test]
    fn staging_failure_poison_the_draft_until_abort() {
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 1];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut engine = SequentialAssignmentEngine::new(
            &mut normalizer,
            2,
            ValueKind::Direct,
            1,
            10_000,
            10_000,
        )
        .unwrap();
        engine.assign(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut staging_pages: [RangeTreeStagingPage; 0] = [];
        let mut staging =
            RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
        assert!(engine
            .build_staged_tree(&mut tree_workspace, &mut staging)
            .is_err());
        assert_eq!(
            engine.build_staged_tree(&mut tree_workspace, &mut staging),
            Err(SequentialAssignmentFinalizeError::Engine(
                SequentialAssignmentError::Failed
            ))
        );
        drop(engine);
        assert!(!normalizer.is_clean());
        normalizer.discard_after_abort();
        staging.discard_after_abort();
        assert!(normalizer.is_clean());
    }

    #[test]
    fn nested_arrival_order_work_grows_linearly() {
        fn run(count: u32) -> u64 {
            let mut normalizer_pages = [SequentialAssignmentPage::empty(); 64];
            let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
            let mut engine = SequentialAssignmentEngine::new(
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
            let mut staging_pages = [RangeTreeStagingPage::empty(); 4];
            let mut staging =
                RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
            engine
                .build_staged_tree(&mut tree_workspace, &mut staging)
                .unwrap();
            engine.work()
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
        let mut normalizer_pages = [SequentialAssignmentPage::empty(); 2];
        let mut normalizer = SequentialAssignmentWorkspace::new(&mut normalizer_pages);
        let mut tree_workspace = RangeTreeBuildWorkspace::<Ipv4Key>::new();
        let mut staging_pages = [RangeTreeStagingPage::empty(); 1];
        let (_, allocations) = count_thread_allocations(|| {
            let mut staging =
                RangeTreeStaging::<Ipv4Key>::new(&mut staging_pages, 2, ValueKind::Direct).unwrap();
            let mut engine = SequentialAssignmentEngine::new(
                &mut normalizer,
                2,
                ValueKind::Direct,
                2,
                10_000,
                10_000,
            )
            .unwrap();
            engine.assign(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
            engine
                .build_staged_tree(&mut tree_workspace, &mut staging)
                .unwrap();
            drop(engine);
            staging.discard_after_abort();
        });
        assert_eq!(allocations, 0);
        assert!(normalizer.is_clean());
        assert_eq!(staging_pages, [RangeTreeStagingPage::empty(); 1]);
    }

    #[test]
    fn zero_birth_generation_is_rejected_before_input() {
        let mut pages = [SequentialAssignmentPage::empty(); 1];
        let mut workspace = SequentialAssignmentWorkspace::new(&mut pages);
        assert!(matches!(
            SequentialAssignmentEngine::<Ipv4Key>::new(
                &mut workspace,
                0,
                ValueKind::Direct,
                1,
                1,
                1,
            ),
            Err(SequentialAssignmentError::BornTransactionZero)
        ));
    }

    #[test]
    fn occupied_workspace_is_rejected_before_input() {
        let mut pages = [SequentialAssignmentPage::empty(); 1];
        pages[0].used = 1;
        {
            let mut workspace = SequentialAssignmentWorkspace::new(&mut pages);
            assert!(matches!(
                SequentialAssignmentEngine::<Ipv4Key>::new(
                    &mut workspace,
                    2,
                    ValueKind::Direct,
                    1,
                    1,
                    1,
                ),
                Err(SequentialAssignmentError::WorkspaceBusy)
            ));
        }
        assert_eq!(pages[0].used, 1);
    }
}
