//! Fixed pre-lock logical storage for one ordinary range replacement.
//!
//! This is deliberately not a public writer operation. It owns only the
//! arrival-order normalizer and its sealed logical range pages so the later
//! lock-held allocator can consume them without a temporary file or a
//! caller-managed intermediate value.

use crate::bitmap_cow::{
    BitmapCowArenaBinding, BitmapCowIndexNode, FreeBitmapFinalizationCachedPage,
    FreeBitmapInsertPage, FreeBitmapReclamationTicket, FreeBitmapReservationSourceNode,
    ReservedBitmapPage, VerifiedBitmapPage,
};
use crate::contract::{AddressFamily, MetaV4, MAX_TREE_LEVEL};
use crate::key::IpKey;
use crate::page_number_index::PageNumberIndexPage;
use crate::private_page_pool::{
    PrivatePageCoordinatorPriorReturn, PrivatePageCoordinatorTerminalPage, PrivatePagePoolSlot,
    PrivatePageCompositeBind, PrivatePagePreparedScopeSlot, PrivatePageSelectiveOverlayNode,
    PrivatePageSelectivePathEntry, PrivatePageSparseReplayIndex, PrivatePageSparseReplaySlot,
};
use crate::range_builder::RangeTreeBuildWorkspace;
use crate::range_staging::{
    RangeTreePayloadReservationSlot, RangeTreePhysicalAssignment,
};
use crate::range_staging::{
    RangeTreeStagedResult, RangeTreeStaging, RangeTreeStagingError, RangeTreeStagingPage,
};
use crate::retirement_writer::{
    CommittedPageOrigin, CommittedPageReplacement, PageRoleIndexSlot, RetirementPathFrame,
};
use crate::sequential_assignment::{
    SequentialAssignmentEngine, SequentialAssignmentError, SequentialAssignmentFinalizeError,
    SequentialAssignmentPage, SequentialAssignmentWorkspace,
};
use crate::writer_fixed_point::{
    DraftPrivatePageEntry, DraftPrivatePageLocation, FixedPointMapJournalWrite,
    FixedPointPreparedWorkSlot, FixedPointSourceJournalWrite, FixedPointTombstoneJournalWrite,
};
use core::cell::Cell;
use core::mem;
use std::vec::Vec;

/// Explicit fixed limits for one private pre-lock range-normalization attempt.
///
/// Zero logical-page capacity is valid for an empty replacement. Any input
/// that needs a node or output page then fails before a live transaction is
/// opened.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LinuxLiveWriterNormalRangeWorkspaceCapacity {
    pub(crate) normalizer_pages: usize,
    pub(crate) staged_range_pages: usize,
    pub(crate) max_assignments: u64,
    pub(crate) max_work: u64,
    pub(crate) max_mutations: usize,
}

/// Construction or preparation failure for the private logical workspace.
///
/// No variant authorizes a physical page allocation, writer barrier, or file
/// mutation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterNormalRangeWorkspaceError {
    Allocation,
    CapacityOverflow,
    WorkspaceBusy,
    NoPreparedOutput,
    SelectedGenerationZero,
    TransactionExhausted,
    AddressFamily {
        selected: AddressFamily,
        requested: AddressFamily,
    },
    Input(SequentialAssignmentError),
    Staging(RangeTreeStagingError),
    Finalize(SequentialAssignmentFinalizeError),
}

#[derive(Clone, Copy, Debug)]
struct LinuxLiveWriterPreparedNormalRange {
    selected: MetaV4,
    staged: RangeTreeStagedResult,
}

/// Opaque owner for bounded normalizer and logical-range staging storage.
///
/// Construction allocates the two variable-size partitions exactly once. The
/// embedded range-tree builder has fixed size. After construction every
/// attempt only clears and borrows this storage; it does not allocate, sort,
/// open a temporary file, or choose a physical v4 page number.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriterNormalRangeWorkspace<K: IpKey> {
    capacity: LinuxLiveWriterNormalRangeWorkspaceCapacity,
    retained_bytes: u64,
    normalizer_pages: Vec<SequentialAssignmentPage>,
    staging_pages: Vec<RangeTreeStagingPage>,
    tree_workspace: RangeTreeBuildWorkspace<K>,
    prepared: Option<LinuxLiveWriterPreparedNormalRange>,
}

fn allocate_normal_range_vec<T>(
    len: usize,
    mut make: impl FnMut() -> T,
) -> Result<Vec<T>, LinuxLiveWriterNormalRangeWorkspaceError> {
    let mut values = Vec::new();
    values
        .try_reserve_exact(len)
        .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::Allocation)?;
    for _ in 0..len {
        values.push(make());
    }
    Ok(values)
}

fn add_retained_bytes<T>(
    total: &mut u64,
    count: usize,
) -> Result<(), LinuxLiveWriterNormalRangeWorkspaceError> {
    let bytes = mem::size_of::<T>()
        .checked_mul(count)
        .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    let bytes = u64::try_from(bytes)
        .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    *total = total
        .checked_add(bytes)
        .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
    Ok(())
}

impl<K: IpKey> LinuxLiveWriterNormalRangeWorkspace<K> {
    /// Creates a reusable fixed logical workspace. This is the only point at
    /// which this owner obtains heap storage.
    pub(crate) fn new(
        capacity: LinuxLiveWriterNormalRangeWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterNormalRangeWorkspaceError> {
        let normalizer_pages =
            allocate_normal_range_vec(capacity.normalizer_pages, SequentialAssignmentPage::empty)?;
        let staging_pages =
            allocate_normal_range_vec(capacity.staged_range_pages, RangeTreeStagingPage::empty)?;

        let mut retained_bytes = u64::try_from(mem::size_of::<Self>())
            .map_err(|_| LinuxLiveWriterNormalRangeWorkspaceError::CapacityOverflow)?;
        add_retained_bytes::<SequentialAssignmentPage>(
            &mut retained_bytes,
            normalizer_pages.capacity(),
        )?;
        add_retained_bytes::<RangeTreeStagingPage>(&mut retained_bytes, staging_pages.capacity())?;

        Ok(Self {
            capacity,
            retained_bytes,
            normalizer_pages,
            staging_pages,
            tree_workspace: RangeTreeBuildWorkspace::new(),
            prepared: None,
        })
    }

    /// Total storage retained by this owner, including its fixed builder.
    pub(crate) const fn retained_bytes(&self) -> u64 {
        self.retained_bytes
    }

    #[allow(clippy::too_many_lines)]
    fn reset_for_next_attempt(&mut self) {
        if !self.dirty {
            return;
        }

        let coordinator = &mut self.coordinator;
        coordinator.live_slots.fill(PrivatePagePoolSlot::empty());
        coordinator.work_slot = FixedPointPreparedWorkSlot::empty();
        coordinator.scope_slot = PrivatePagePreparedScopeSlot::empty();
        coordinator
            .record_bindings
            .fill(BitmapCowArenaBinding::empty());
        coordinator.record_replacements.fill(0);
        coordinator
            .record_index_nodes
            .fill(BitmapCowIndexNode::empty());
        for returned in &coordinator.record_returned {
            returned.set(false);
        }
        coordinator
            .record_cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        coordinator
            .record_cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        coordinator.record_cleanup_targets.fill(usize::MAX);
        for entry in &coordinator.workspace_entries {
            entry.set(None);
        }
        for entry in &coordinator.workspace_source_map {
            entry.set(usize::MAX);
        }
        for entry in &coordinator.workspace_record_map {
            entry.set(usize::MAX);
        }
        for entry in &coordinator.source_journal {
            entry.set(FixedPointSourceJournalWrite::EMPTY);
        }
        for entry in &coordinator.map_journal {
            entry.set(FixedPointMapJournalWrite::EMPTY);
        }
        for entry in &coordinator.tombstone_journal {
            entry.set(FixedPointTombstoneJournalWrite::EMPTY);
        }
        coordinator
            .ordered_prior_locations
            .fill(DraftPrivatePageLocation::EMPTY);
        coordinator
            .pool_returns
            .fill(PrivatePageCoordinatorPriorReturn::empty());
        coordinator
            .new_locations
            .fill(DraftPrivatePageLocation::EMPTY);
        coordinator
            .replay_slots
            .fill(PrivatePageSparseReplaySlot::empty());
        coordinator
            .replay_index
            .fill(PrivatePageSparseReplayIndex::empty());

        let planner = &mut self.planner;
        planner.arena.fill(ReservedBitmapPage::empty());
        planner
            .pool_validation
            .fill(PrivatePageCompositeBind::empty());
        planner
            .arena_bindings
            .fill(BitmapCowArenaBinding::empty());
        planner.candidates.fill(0);
        planner.verified.fill(VerifiedBitmapPage::empty());
        planner.replacements.fill(0);
        planner.index.fill(BitmapCowIndexNode::empty());
        planner.available.fill(0);
        planner
            .source_nodes
            .fill(FreeBitmapReservationSourceNode::empty());
        planner.ticket = FreeBitmapReclamationTicket::new();
        planner.stage_arena.fill(ReservedBitmapPage::empty());
        planner
            .stage_bindings
            .fill(BitmapCowArenaBinding::empty());
        planner.stage_candidates.fill(0);
        planner
            .stage_verified
            .fill(VerifiedBitmapPage::empty());
        planner.stage_replacements.fill(0);
        planner.stage_index.fill(BitmapCowIndexNode::empty());
        planner.stage_available.fill(0);
        planner.shadow_slots.fill(PrivatePagePoolSlot::empty());

        let proof = &mut self.proof;
        proof.assignments.fill(RangeTreePhysicalAssignment::empty());
        proof
            .payload_slots
            .fill(RangeTreePayloadReservationSlot::empty());
        proof
            .range_input_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        proof.seed_pages.fill(PageNumberIndexPage::empty());
        proof.first_pages.fill(PageNumberIndexPage::empty());
        proof.second_pages.fill(PageNumberIndexPage::empty());
        proof.initial_path.fill(RetirementPathFrame::new());
        proof
            .initial_replacements
            .fill(EMPTY_NORMAL_RANGE_REPLACEMENT);
        proof.initial_releases.fill(0);
        proof.initial_roles.fill(PageRoleIndexSlot::new());
        proof.preview_bitmap_replacements.fill(0);
        proof.preview_blob_pages.fill(0);
        proof.preview_path.fill(RetirementPathFrame::new());
        proof
            .preview_replacements
            .fill(EMPTY_NORMAL_RANGE_REPLACEMENT);
        proof.preview_releases.fill(0);
        proof.preview_roles.fill(PageRoleIndexSlot::new());
        proof.final_release_pages.fill(0);
        proof
            .final_insert_pages
            .fill(FreeBitmapInsertPage::empty());
        proof
            .final_cached_pages
            .fill(FreeBitmapFinalizationCachedPage::empty());
        proof.final_index_stack.fill(usize::MAX);
        proof
            .final_cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        proof
            .final_cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        proof.final_cleanup_targets.fill(usize::MAX);

        let terminal = &mut self.terminal;
        terminal.terminal_blob_pages.fill(0);
        terminal.terminal_path.fill(RetirementPathFrame::new());
        terminal
            .terminal_replacements
            .fill(EMPTY_NORMAL_RANGE_REPLACEMENT);
        terminal.terminal_releases.fill(0);
        terminal.terminal_roles.fill(PageRoleIndexSlot::new());
        terminal
            .bitmap_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        terminal
            .range_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        terminal
            .retirement_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        terminal
            .combined_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        self.dirty = false;
    }

    #[allow(clippy::too_many_lines)]
    pub(crate) fn scratch(
        &mut self,
    ) -> LinuxLiveWriterNormalRangeFinalizationScratch<'_> {
        self.reset_for_next_attempt();
        self.dirty = true;

        let coordinator = &mut self.coordinator;
        let planner = &mut self.planner;
        let proof = &mut self.proof;
        let terminal = &mut self.terminal;
        LinuxLiveWriterNormalRangeFinalizationScratch {
            live_slots: &mut coordinator.live_slots,
            work_slot: &mut coordinator.work_slot,
            scope_slot: &mut coordinator.scope_slot,
            record_bindings: &mut coordinator.record_bindings,
            record_replacements: &mut coordinator.record_replacements,
            record_index_nodes: &mut coordinator.record_index_nodes,
            record_returned: &coordinator.record_returned,
            record_cleanup_nodes: &mut coordinator.record_cleanup_nodes,
            record_cleanup_path: &mut coordinator.record_cleanup_path,
            record_cleanup_targets: &mut coordinator.record_cleanup_targets,
            workspace_entries: &coordinator.workspace_entries,
            workspace_source_map: &coordinator.workspace_source_map,
            workspace_record_map: &coordinator.workspace_record_map,
            source_journal: &coordinator.source_journal,
            map_journal: &coordinator.map_journal,
            tombstone_journal: &coordinator.tombstone_journal,
            ordered_prior_locations: &mut coordinator.ordered_prior_locations,
            pool_returns: &mut coordinator.pool_returns,
            new_locations: &mut coordinator.new_locations,
            replay_slots: &mut coordinator.replay_slots,
            replay_index: &mut coordinator.replay_index,
            arena: &mut planner.arena,
            pool_validation: &mut planner.pool_validation,
            arena_bindings: &mut planner.arena_bindings,
            candidates: &mut planner.candidates,
            verified: &mut planner.verified,
            replacements: &mut planner.replacements,
            index: &mut planner.index,
            available: &mut planner.available,
            source_nodes: &mut planner.source_nodes,
            ticket: &planner.ticket,
            stage_arena: &mut planner.stage_arena,
            stage_bindings: &mut planner.stage_bindings,
            stage_candidates: &mut planner.stage_candidates,
            stage_verified: &mut planner.stage_verified,
            stage_replacements: &mut planner.stage_replacements,
            stage_index: &mut planner.stage_index,
            stage_available: &mut planner.stage_available,
            shadow_slots: &mut planner.shadow_slots,
            assignments: &mut proof.assignments,
            payload_slots: &mut proof.payload_slots,
            range_input_pages: &mut proof.range_input_pages,
            seed_pages: &mut proof.seed_pages,
            first_pages: &mut proof.first_pages,
            second_pages: &mut proof.second_pages,
            initial_path: &mut proof.initial_path,
            initial_replacements: &mut proof.initial_replacements,
            initial_releases: &mut proof.initial_releases,
            initial_roles: &mut proof.initial_roles,
            preview_bitmap_replacements: &mut proof.preview_bitmap_replacements,
            preview_blob_pages: &mut proof.preview_blob_pages,
            preview_path: &mut proof.preview_path,
            preview_replacements: &mut proof.preview_replacements,
            preview_releases: &mut proof.preview_releases,
            preview_roles: &mut proof.preview_roles,
            final_release_pages: &mut proof.final_release_pages,
            final_insert_pages: &mut proof.final_insert_pages,
            final_cached_pages: &mut proof.final_cached_pages,
            final_index_stack: &mut proof.final_index_stack,
            final_cleanup_nodes: &mut proof.final_cleanup_nodes,
            final_cleanup_path: &mut proof.final_cleanup_path,
            final_cleanup_targets: &mut proof.final_cleanup_targets,
            terminal_blob_pages: &mut terminal.terminal_blob_pages,
            terminal_path: &mut terminal.terminal_path,
            terminal_replacements: &mut terminal.terminal_replacements,
            terminal_releases: &mut terminal.terminal_releases,
            terminal_roles: &mut terminal.terminal_roles,
            bitmap_terminal_pages: &mut terminal.bitmap_terminal_pages,
            range_terminal_pages: &mut terminal.range_terminal_pages,
            retirement_terminal_pages: &mut terminal.retirement_terminal_pages,
            combined_terminal_pages: &mut terminal.combined_terminal_pages,
        }
    }
}

    fn reset_logical_storage(&mut self) {
        self.normalizer_pages
            .fill(SequentialAssignmentPage::empty());
        self.staging_pages.fill(RangeTreeStagingPage::empty());
    }

    /// Applies one arrival-ordered input stream and retains only its sealed
    /// logical range tree. The closure has no live page allocator or file
    /// access; it can only issue assignment/clear operations to the bounded
    /// sequential engine.
    pub(crate) fn prepare(
        &mut self,
        selected: MetaV4,
        apply: impl FnOnce(&mut SequentialAssignmentEngine<K>) -> Result<(), SequentialAssignmentError>,
    ) -> Result<(), LinuxLiveWriterNormalRangeWorkspaceError> {
        if self.prepared.is_some() {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::WorkspaceBusy);
        }
        if selected.address_family != K::FAMILY {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::AddressFamily {
                selected: selected.address_family,
                requested: K::FAMILY,
            });
        }
        if selected.txn_id == 0 {
            return Err(LinuxLiveWriterNormalRangeWorkspaceError::SelectedGenerationZero);
        }
        let born_txn = selected
            .txn_id
            .checked_add(1)
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::TransactionExhausted)?;

        self.reset_logical_storage();
        let result = (|| {
            let mut normalizer = SequentialAssignmentWorkspace::new(&mut self.normalizer_pages);
            let mut staging =
                RangeTreeStaging::new(&mut self.staging_pages, born_txn, selected.value_kind)
                    .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Staging)?;
            let mut engine = SequentialAssignmentEngine::new(
                &mut normalizer,
                born_txn,
                selected.value_kind,
                self.capacity.max_assignments,
                self.capacity.max_work,
                self.capacity.max_mutations,
            )
            .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Input)?;
            apply(&mut engine).map_err(LinuxLiveWriterNormalRangeWorkspaceError::Input)?;
            engine
                .build_staged_tree(&mut self.tree_workspace, &mut staging)
                .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Finalize)
        })();

        match result {
            Ok(staged) => {
                self.prepared = Some(LinuxLiveWriterPreparedNormalRange { selected, staged });
                Ok(())
            }
            Err(error) => {
                self.reset_logical_storage();
                Err(error)
            }
        }
    }

    /// Reopens the workspace-owned sealed logical output for the later
    /// lock-held allocator. The returned staging view borrows this workspace,
    /// so it cannot outlive the operation or be retained independently. The
    /// prepared marker is consumed first; a reattachment failure is terminal
    /// for this attempt and must follow the enclosing abort/reset path.
    pub(crate) fn reopen_prepared_staging(
        &mut self,
    ) -> Result<
        (MetaV4, RangeTreeStagedResult, RangeTreeStaging<'_, K>),
        LinuxLiveWriterNormalRangeWorkspaceError,
    > {
        let prepared = self
            .prepared
            .take()
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput)?;
        let born_txn = prepared
            .selected
            .txn_id
            .checked_add(1)
            .ok_or(LinuxLiveWriterNormalRangeWorkspaceError::TransactionExhausted)?;
        let staging = RangeTreeStaging::reopen_sealed(
            &mut self.staging_pages,
            born_txn,
            prepared.selected.value_kind,
            prepared.staged,
        )
        .map_err(LinuxLiveWriterNormalRangeWorkspaceError::Staging)?;
        Ok((prepared.selected, prepared.staged, staging))
    }

    /// Erases unpublished logical input/output after the enclosing live draft
    /// has been abandoned. It does not cancel a core or release a writer lock;
    /// those remain the enclosing operation's explicit responsibilities.
    pub(crate) fn discard_after_abort(&mut self) {
        self.reset_logical_storage();
        self.prepared = None;
    }

    /// Erases transient logical storage after the enclosing transaction has
    /// durably completed and released its publication authority.
    pub(crate) fn finish_after_publication(&mut self) {
        self.reset_logical_storage();
        self.prepared = None;
    }

    #[cfg(test)]
    fn is_idle(&self) -> bool {
        self.prepared.is_none()
            && self
                .normalizer_pages
                .iter()
                .all(|page| *page == SequentialAssignmentPage::empty())
            && self
                .staging_pages
                .iter()
                .all(|page| *page == RangeTreeStagingPage::empty())
    }
}

/// Explicit backing limits for the lock-held portion of one normal range
/// replacement. These are internal memory bounds, not caller-visible page or
/// bitmap identifiers.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LinuxLiveWriterNormalRangeFinalizationWorkspaceCapacity {
    pub(crate) max_live_pages: usize,
    pub(crate) bitmap_replacement_capacity: usize,
    pub(crate) range_page_capacity: usize,
    pub(crate) page_index_pages: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterNormalRangeFinalizationWorkspaceError {
    InvalidCapacity,
    CapacityOverflow,
    Allocation,
}

const EMPTY_NORMAL_RANGE_REPLACEMENT: CommittedPageReplacement = CommittedPageReplacement {
    pgno: 0,
    origin: CommittedPageOrigin::RetirementTree,
};

#[derive(Clone, Copy)]
struct LinuxLiveWriterNormalRangeFinalizationWorkspaceLayout {
    live_pages: usize,
    bitmap_replacements: usize,
    range_pages: usize,
    index_pages: usize,
    retirement_path: usize,
    double_live_pages: usize,
    triple_bitmap_replacements: usize,
    quadruple_live_pages: usize,
    final_insert_pages: usize,
}

impl LinuxLiveWriterNormalRangeFinalizationWorkspaceLayout {
    fn checked_multiple(
        value: usize,
        multiplier: usize,
    ) -> Result<usize, LinuxLiveWriterNormalRangeFinalizationWorkspaceError> {
        value
            .checked_mul(multiplier)
            .ok_or(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)
    }

    fn new(
        capacity: LinuxLiveWriterNormalRangeFinalizationWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterNormalRangeFinalizationWorkspaceError> {
        if capacity.max_live_pages == 0
            || capacity.bitmap_replacement_capacity == 0
            || capacity.page_index_pages == 0
        {
            return Err(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::InvalidCapacity);
        }
        let double_live_pages = Self::checked_multiple(capacity.max_live_pages, 2)?;
        let triple_bitmap_replacements =
            Self::checked_multiple(capacity.bitmap_replacement_capacity, 3)?;
        let quadruple_live_pages = Self::checked_multiple(capacity.max_live_pages, 4)?;
        let final_insert_pages = quadruple_live_pages
            .checked_add(4)
            .ok_or(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
        let retirement_path = usize::from(MAX_TREE_LEVEL)
            .checked_add(1)
            .ok_or(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
        Ok(Self {
            live_pages: capacity.max_live_pages,
            bitmap_replacements: capacity.bitmap_replacement_capacity,
            range_pages: capacity.range_page_capacity,
            index_pages: capacity.page_index_pages,
            retirement_path,
            double_live_pages,
            triple_bitmap_replacements,
            quadruple_live_pages,
            final_insert_pages,
        })
    }
}

struct LinuxLiveWriterNormalRangeCoordinatorStorage {
    live_slots: Vec<PrivatePagePoolSlot>,
    work_slot: FixedPointPreparedWorkSlot,
    scope_slot: PrivatePagePreparedScopeSlot,
    record_bindings: Vec<BitmapCowArenaBinding>,
    record_replacements: Vec<u32>,
    record_index_nodes: Vec<BitmapCowIndexNode>,
    record_returned: Vec<Cell<bool>>,
    record_cleanup_nodes: Vec<PrivatePageSelectiveOverlayNode>,
    record_cleanup_path: Vec<PrivatePageSelectivePathEntry>,
    record_cleanup_targets: Vec<usize>,
    workspace_entries: Vec<Cell<Option<DraftPrivatePageEntry>>>,
    workspace_source_map: Vec<Cell<usize>>,
    workspace_record_map: Vec<Cell<usize>>,
    source_journal: Vec<Cell<FixedPointSourceJournalWrite>>,
    map_journal: Vec<Cell<FixedPointMapJournalWrite>>,
    tombstone_journal: Vec<Cell<FixedPointTombstoneJournalWrite>>,
    ordered_prior_locations: Vec<DraftPrivatePageLocation>,
    pool_returns: Vec<PrivatePageCoordinatorPriorReturn>,
    new_locations: Vec<DraftPrivatePageLocation>,
    replay_slots: Vec<PrivatePageSparseReplaySlot>,
    replay_index: Vec<PrivatePageSparseReplayIndex>,
}

struct LinuxLiveWriterNormalRangePlannerStorage {
    arena: Vec<ReservedBitmapPage>,
    pool_validation: Vec<PrivatePageCompositeBind>,
    arena_bindings: Vec<BitmapCowArenaBinding>,
    candidates: Vec<u32>,
    verified: Vec<VerifiedBitmapPage>,
    replacements: Vec<u32>,
    index: Vec<BitmapCowIndexNode>,
    available: Vec<usize>,
    source_nodes: Vec<FreeBitmapReservationSourceNode>,
    ticket: FreeBitmapReclamationTicket,
    stage_arena: Vec<ReservedBitmapPage>,
    stage_bindings: Vec<BitmapCowArenaBinding>,
    stage_candidates: Vec<u32>,
    stage_verified: Vec<VerifiedBitmapPage>,
    stage_replacements: Vec<u32>,
    stage_index: Vec<BitmapCowIndexNode>,
    stage_available: Vec<usize>,
    shadow_slots: Vec<PrivatePagePoolSlot>,
}

struct LinuxLiveWriterNormalRangeProofStorage {
    assignments: Vec<RangeTreePhysicalAssignment>,
    payload_slots: Vec<RangeTreePayloadReservationSlot>,
    range_input_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    seed_pages: Vec<PageNumberIndexPage>,
    first_pages: Vec<PageNumberIndexPage>,
    second_pages: Vec<PageNumberIndexPage>,
    initial_path: Vec<RetirementPathFrame>,
    initial_replacements: Vec<CommittedPageReplacement>,
    initial_releases: Vec<u32>,
    initial_roles: Vec<PageRoleIndexSlot>,
    preview_bitmap_replacements: Vec<u32>,
    preview_blob_pages: Vec<u32>,
    preview_path: Vec<RetirementPathFrame>,
    preview_replacements: Vec<CommittedPageReplacement>,
    preview_releases: Vec<u32>,
    preview_roles: Vec<PageRoleIndexSlot>,
    final_release_pages: Vec<u32>,
    final_insert_pages: Vec<FreeBitmapInsertPage>,
    final_cached_pages: Vec<FreeBitmapFinalizationCachedPage>,
    final_index_stack: Vec<usize>,
    final_cleanup_nodes: Vec<PrivatePageSelectiveOverlayNode>,
    final_cleanup_path: Vec<PrivatePageSelectivePathEntry>,
    final_cleanup_targets: Vec<usize>,
}

struct LinuxLiveWriterNormalRangeTerminalStorage {
    terminal_blob_pages: Vec<u32>,
    terminal_path: Vec<RetirementPathFrame>,
    terminal_replacements: Vec<CommittedPageReplacement>,
    terminal_releases: Vec<u32>,
    terminal_roles: Vec<PageRoleIndexSlot>,
    bitmap_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    range_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    retirement_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    combined_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
}

/// Borrowed internal slices for one lock-held normal-range finalization.
///
/// This type is crate-private and exists only to move the old fixture backing
/// behind one owner before the operation orchestration itself moves there.
#[allow(clippy::struct_excessive_bools)]
pub(crate) struct LinuxLiveWriterNormalRangeFinalizationScratch<'a> {
    pub(crate) live_slots: &'a mut [PrivatePagePoolSlot],
    pub(crate) work_slot: &'a mut FixedPointPreparedWorkSlot,
    pub(crate) scope_slot: &'a mut PrivatePagePreparedScopeSlot,
    pub(crate) record_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) record_replacements: &'a mut [u32],
    pub(crate) record_index_nodes: &'a mut [BitmapCowIndexNode],
    pub(crate) record_returned: &'a [Cell<bool>],
    pub(crate) record_cleanup_nodes: &'a mut [PrivatePageSelectiveOverlayNode],
    pub(crate) record_cleanup_path: &'a mut [PrivatePageSelectivePathEntry],
    pub(crate) record_cleanup_targets: &'a mut [usize],
    pub(crate) workspace_entries: &'a [Cell<Option<DraftPrivatePageEntry>>],
    pub(crate) workspace_source_map: &'a [Cell<usize>],
    pub(crate) workspace_record_map: &'a [Cell<usize>],
    pub(crate) source_journal: &'a [Cell<FixedPointSourceJournalWrite>],
    pub(crate) map_journal: &'a [Cell<FixedPointMapJournalWrite>],
    pub(crate) tombstone_journal: &'a [Cell<FixedPointTombstoneJournalWrite>],
    pub(crate) ordered_prior_locations: &'a mut [DraftPrivatePageLocation],
    pub(crate) pool_returns: &'a mut [PrivatePageCoordinatorPriorReturn],
    pub(crate) new_locations: &'a mut [DraftPrivatePageLocation],
    pub(crate) replay_slots: &'a mut [PrivatePageSparseReplaySlot],
    pub(crate) replay_index: &'a mut [PrivatePageSparseReplayIndex],
    pub(crate) arena: &'a mut [ReservedBitmapPage],
    pub(crate) pool_validation: &'a mut [PrivatePageCompositeBind],
    pub(crate) arena_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) candidates: &'a mut [u32],
    pub(crate) verified: &'a mut [VerifiedBitmapPage],
    pub(crate) replacements: &'a mut [u32],
    pub(crate) index: &'a mut [BitmapCowIndexNode],
    pub(crate) available: &'a mut [usize],
    pub(crate) source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    pub(crate) ticket: &'a FreeBitmapReclamationTicket,
    pub(crate) stage_arena: &'a mut [ReservedBitmapPage],
    pub(crate) stage_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) stage_candidates: &'a mut [u32],
    pub(crate) stage_verified: &'a mut [VerifiedBitmapPage],
    pub(crate) stage_replacements: &'a mut [u32],
    pub(crate) stage_index: &'a mut [BitmapCowIndexNode],
    pub(crate) stage_available: &'a mut [usize],
    pub(crate) shadow_slots: &'a mut [PrivatePagePoolSlot],
    pub(crate) assignments: &'a mut [RangeTreePhysicalAssignment],
    pub(crate) payload_slots: &'a mut [RangeTreePayloadReservationSlot],
    pub(crate) range_input_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) seed_pages: &'a mut [PageNumberIndexPage],
    pub(crate) first_pages: &'a mut [PageNumberIndexPage],
    pub(crate) second_pages: &'a mut [PageNumberIndexPage],
    pub(crate) initial_path: &'a mut [RetirementPathFrame],
    pub(crate) initial_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) initial_releases: &'a mut [u32],
    pub(crate) initial_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) preview_bitmap_replacements: &'a mut [u32],
    pub(crate) preview_blob_pages: &'a mut [u32],
    pub(crate) preview_path: &'a mut [RetirementPathFrame],
    pub(crate) preview_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) preview_releases: &'a mut [u32],
    pub(crate) preview_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) final_release_pages: &'a mut [u32],
    pub(crate) final_insert_pages: &'a mut [FreeBitmapInsertPage],
    pub(crate) final_cached_pages: &'a mut [FreeBitmapFinalizationCachedPage],
    pub(crate) final_index_stack: &'a mut [usize],
    pub(crate) final_cleanup_nodes: &'a mut [PrivatePageSelectiveOverlayNode],
    pub(crate) final_cleanup_path: &'a mut [PrivatePageSelectivePathEntry],
    pub(crate) final_cleanup_targets: &'a mut [usize],
    pub(crate) terminal_blob_pages: &'a mut [u32],
    pub(crate) terminal_path: &'a mut [RetirementPathFrame],
    pub(crate) terminal_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) terminal_releases: &'a mut [u32],
    pub(crate) terminal_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) bitmap_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) range_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) retirement_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) combined_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
}

/// Opaque owner for every variable-sized lock-held normal-range partition.
///
/// It does not yet run a writer operation by itself. The owner exists so the
/// existing finalizer can be migrated without retaining caller-managed slices
/// or undercounting its memory in the transaction resource budget.
pub(crate) struct LinuxLiveWriterNormalRangeFinalizationWorkspace {
    retained_bytes: u64,
    dirty: bool,
    coordinator: LinuxLiveWriterNormalRangeCoordinatorStorage,
    planner: LinuxLiveWriterNormalRangePlannerStorage,
    proof: LinuxLiveWriterNormalRangeProofStorage,
    terminal: LinuxLiveWriterNormalRangeTerminalStorage,
}

fn allocate_normal_range_finalization_vec<T>(
    len: usize,
    mut make: impl FnMut() -> T,
) -> Result<Vec<T>, LinuxLiveWriterNormalRangeFinalizationWorkspaceError> {
    let mut values = Vec::new();
    values
        .try_reserve_exact(len)
        .map_err(|_| LinuxLiveWriterNormalRangeFinalizationWorkspaceError::Allocation)?;
    for _ in 0..len {
        values.push(make());
    }
    Ok(values)
}

fn add_normal_range_finalization_bytes<T>(
    total: &mut u64,
    count: usize,
) -> Result<(), LinuxLiveWriterNormalRangeFinalizationWorkspaceError> {
    let bytes = mem::size_of::<T>()
        .checked_mul(count)
        .ok_or(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
    let bytes = u64::try_from(bytes)
        .map_err(|_| LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
    *total = total
        .checked_add(bytes)
        .ok_or(LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
    Ok(())
}

impl LinuxLiveWriterNormalRangeFinalizationWorkspace {
    /// Allocates every variable-sized finalization partition before an
    /// operation can acquire its Linux barrier.
    #[allow(clippy::too_many_lines)]
    pub(crate) fn new(
        capacity: LinuxLiveWriterNormalRangeFinalizationWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterNormalRangeFinalizationWorkspaceError> {
        let layout = LinuxLiveWriterNormalRangeFinalizationWorkspaceLayout::new(capacity)?;
        let coordinator = LinuxLiveWriterNormalRangeCoordinatorStorage {
            live_slots: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePagePoolSlot::empty,
            )?,
            work_slot: FixedPointPreparedWorkSlot::empty(),
            scope_slot: PrivatePagePreparedScopeSlot::empty(),
            record_bindings: allocate_normal_range_finalization_vec(
                layout.live_pages,
                BitmapCowArenaBinding::empty,
            )?,
            record_replacements: allocate_normal_range_finalization_vec(
                layout.bitmap_replacements,
                || 0,
            )?,
            record_index_nodes: allocate_normal_range_finalization_vec(
                layout.triple_bitmap_replacements,
                BitmapCowIndexNode::empty,
            )?,
            record_returned: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::new(false)
            })?,
            record_cleanup_nodes: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                PrivatePageSelectiveOverlayNode::empty,
            )?,
            record_cleanup_path: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                PrivatePageSelectivePathEntry::empty,
            )?,
            record_cleanup_targets: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || usize::MAX,
            )?,
            workspace_entries: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::<Option<DraftPrivatePageEntry>>::new(None)
            })?,
            workspace_source_map: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::new(usize::MAX)
            })?,
            workspace_record_map: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::new(usize::MAX)
            })?,
            source_journal: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::new(FixedPointSourceJournalWrite::EMPTY)
            })?,
            map_journal: allocate_normal_range_finalization_vec(layout.double_live_pages, || {
                Cell::new(FixedPointMapJournalWrite::EMPTY)
            })?,
            tombstone_journal: allocate_normal_range_finalization_vec(layout.live_pages, || {
                Cell::new(FixedPointTombstoneJournalWrite::EMPTY)
            })?,
            ordered_prior_locations: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || DraftPrivatePageLocation::EMPTY,
            )?,
            pool_returns: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCoordinatorPriorReturn::empty,
            )?,
            new_locations: allocate_normal_range_finalization_vec(layout.live_pages, || {
                DraftPrivatePageLocation::EMPTY
            })?,
            replay_slots: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                PrivatePageSparseReplaySlot::empty,
            )?,
            replay_index: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageSparseReplayIndex::empty,
            )?,
        };
        let planner = LinuxLiveWriterNormalRangePlannerStorage {
            arena: allocate_normal_range_finalization_vec(
                layout.live_pages,
                ReservedBitmapPage::empty,
            )?,
            pool_validation: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCompositeBind::empty,
            )?,
            arena_bindings: allocate_normal_range_finalization_vec(
                layout.live_pages,
                BitmapCowArenaBinding::empty,
            )?,
            candidates: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            verified: allocate_normal_range_finalization_vec(
                layout.live_pages,
                VerifiedBitmapPage::empty,
            )?,
            replacements: allocate_normal_range_finalization_vec(
                layout.bitmap_replacements,
                || 0,
            )?,
            index: allocate_normal_range_finalization_vec(
                layout.triple_bitmap_replacements,
                BitmapCowIndexNode::empty,
            )?,
            available: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            source_nodes: allocate_normal_range_finalization_vec(
                layout.double_live_pages,
                FreeBitmapReservationSourceNode::empty,
            )?,
            ticket: FreeBitmapReclamationTicket::new(),
            stage_arena: allocate_normal_range_finalization_vec(
                layout.live_pages,
                ReservedBitmapPage::empty,
            )?,
            stage_bindings: allocate_normal_range_finalization_vec(
                layout.live_pages,
                BitmapCowArenaBinding::empty,
            )?,
            stage_candidates: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            stage_verified: allocate_normal_range_finalization_vec(
                layout.live_pages,
                VerifiedBitmapPage::empty,
            )?,
            stage_replacements: allocate_normal_range_finalization_vec(
                layout.bitmap_replacements,
                || 0,
            )?,
            stage_index: allocate_normal_range_finalization_vec(
                layout.triple_bitmap_replacements,
                BitmapCowIndexNode::empty,
            )?,
            stage_available: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            shadow_slots: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePagePoolSlot::empty,
            )?,
        };
        let proof = LinuxLiveWriterNormalRangeProofStorage {
            assignments: allocate_normal_range_finalization_vec(
                layout.range_pages,
                RangeTreePhysicalAssignment::empty,
            )?,
            payload_slots: allocate_normal_range_finalization_vec(
                layout.range_pages,
                RangeTreePayloadReservationSlot::empty,
            )?,
            range_input_pages: allocate_normal_range_finalization_vec(
                layout.range_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            seed_pages: allocate_normal_range_finalization_vec(
                layout.index_pages,
                PageNumberIndexPage::empty,
            )?,
            first_pages: allocate_normal_range_finalization_vec(
                layout.index_pages,
                PageNumberIndexPage::empty,
            )?,
            second_pages: allocate_normal_range_finalization_vec(
                layout.index_pages,
                PageNumberIndexPage::empty,
            )?,
            initial_path: allocate_normal_range_finalization_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            initial_replacements: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || EMPTY_NORMAL_RANGE_REPLACEMENT,
            )?,
            initial_releases: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            initial_roles: allocate_normal_range_finalization_vec(
                layout.double_live_pages,
                PageRoleIndexSlot::new,
            )?,
            preview_bitmap_replacements: allocate_normal_range_finalization_vec(
                layout.bitmap_replacements,
                || 0,
            )?,
            preview_blob_pages: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            preview_path: allocate_normal_range_finalization_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            preview_replacements: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || EMPTY_NORMAL_RANGE_REPLACEMENT,
            )?,
            preview_releases: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            preview_roles: allocate_normal_range_finalization_vec(
                layout.double_live_pages,
                PageRoleIndexSlot::new,
            )?,
            final_release_pages: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            final_insert_pages: allocate_normal_range_finalization_vec(
                layout.final_insert_pages,
                FreeBitmapInsertPage::empty,
            )?,
            final_cached_pages: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                FreeBitmapFinalizationCachedPage::empty,
            )?,
            final_index_stack: allocate_normal_range_finalization_vec(
                layout.triple_bitmap_replacements,
                || usize::MAX,
            )?,
            final_cleanup_nodes: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                PrivatePageSelectiveOverlayNode::empty,
            )?,
            final_cleanup_path: allocate_normal_range_finalization_vec(
                layout.quadruple_live_pages,
                PrivatePageSelectivePathEntry::empty,
            )?,
            final_cleanup_targets: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || usize::MAX,
            )?,
        };
        let terminal = LinuxLiveWriterNormalRangeTerminalStorage {
            terminal_blob_pages: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            terminal_path: allocate_normal_range_finalization_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            terminal_replacements: allocate_normal_range_finalization_vec(
                layout.live_pages,
                || EMPTY_NORMAL_RANGE_REPLACEMENT,
            )?,
            terminal_releases: allocate_normal_range_finalization_vec(layout.live_pages, || 0)?,
            terminal_roles: allocate_normal_range_finalization_vec(
                layout.double_live_pages,
                PageRoleIndexSlot::new,
            )?,
            bitmap_terminal_pages: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            range_terminal_pages: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            retirement_terminal_pages: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            combined_terminal_pages: allocate_normal_range_finalization_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
        };

        let mut retained_bytes = u64::try_from(mem::size_of::<Self>())
            .map_err(|_| LinuxLiveWriterNormalRangeFinalizationWorkspaceError::CapacityOverflow)?;
        macro_rules! add_vector {
            ($vector:expr) => {
                add_normal_range_finalization_bytes(&mut retained_bytes, $vector.capacity())?
            };
        }
        add_vector!(coordinator.live_slots);
        add_vector!(coordinator.record_bindings);
        add_vector!(coordinator.record_replacements);
        add_vector!(coordinator.record_index_nodes);
        add_vector!(coordinator.record_returned);
        add_vector!(coordinator.record_cleanup_nodes);
        add_vector!(coordinator.record_cleanup_path);
        add_vector!(coordinator.record_cleanup_targets);
        add_vector!(coordinator.workspace_entries);
        add_vector!(coordinator.workspace_source_map);
        add_vector!(coordinator.workspace_record_map);
        add_vector!(coordinator.source_journal);
        add_vector!(coordinator.map_journal);
        add_vector!(coordinator.tombstone_journal);
        add_vector!(coordinator.ordered_prior_locations);
        add_vector!(coordinator.pool_returns);
        add_vector!(coordinator.new_locations);
        add_vector!(coordinator.replay_slots);
        add_vector!(coordinator.replay_index);
        add_vector!(planner.arena);
        add_vector!(planner.pool_validation);
        add_vector!(planner.arena_bindings);
        add_vector!(planner.candidates);
        add_vector!(planner.verified);
        add_vector!(planner.replacements);
        add_vector!(planner.index);
        add_vector!(planner.available);
        add_vector!(planner.source_nodes);
        add_vector!(planner.stage_arena);
        add_vector!(planner.stage_bindings);
        add_vector!(planner.stage_candidates);
        add_vector!(planner.stage_verified);
        add_vector!(planner.stage_replacements);
        add_vector!(planner.stage_index);
        add_vector!(planner.stage_available);
        add_vector!(planner.shadow_slots);
        add_vector!(proof.assignments);
        add_vector!(proof.payload_slots);
        add_vector!(proof.range_input_pages);
        add_vector!(proof.seed_pages);
        add_vector!(proof.first_pages);
        add_vector!(proof.second_pages);
        add_vector!(proof.initial_path);
        add_vector!(proof.initial_replacements);
        add_vector!(proof.initial_releases);
        add_vector!(proof.initial_roles);
        add_vector!(proof.preview_bitmap_replacements);
        add_vector!(proof.preview_blob_pages);
        add_vector!(proof.preview_path);
        add_vector!(proof.preview_replacements);
        add_vector!(proof.preview_releases);
        add_vector!(proof.preview_roles);
        add_vector!(proof.final_release_pages);
        add_vector!(proof.final_insert_pages);
        add_vector!(proof.final_cached_pages);
        add_vector!(proof.final_index_stack);
        add_vector!(proof.final_cleanup_nodes);
        add_vector!(proof.final_cleanup_path);
        add_vector!(proof.final_cleanup_targets);
        add_vector!(terminal.terminal_blob_pages);
        add_vector!(terminal.terminal_path);
        add_vector!(terminal.terminal_replacements);
        add_vector!(terminal.terminal_releases);
        add_vector!(terminal.terminal_roles);
        add_vector!(terminal.bitmap_terminal_pages);
        add_vector!(terminal.range_terminal_pages);
        add_vector!(terminal.retirement_terminal_pages);
        add_vector!(terminal.combined_terminal_pages);

        Ok(Self {
            retained_bytes,
            dirty: false,
            coordinator,
            planner,
            proof,
            terminal,
        })
    }

    pub(crate) const fn retained_bytes(&self) -> u64 {
        self.retained_bytes
    }

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{ValueKind, ValueTag};
    use crate::key::Ipv4Key;
    use crate::private_page_pool::{PrivatePageAuthorization, PrivatePageCoordinatorTerminalPage};
    use crate::range_page::RangeLeaf;
    use crate::range_staging::RangeTreePhysicalAssignment;
    use crate::test_alloc::count_thread_allocations;

    fn selected() -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 7,
            commit_nonce: [2; 16],
            page_count: 64,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count: 0,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 2,
            retirement_root: 0,
        }
    }

    fn capacity() -> LinuxLiveWriterNormalRangeWorkspaceCapacity {
        LinuxLiveWriterNormalRangeWorkspaceCapacity {
            normalizer_pages: 2,
            staged_range_pages: 1,
            max_assignments: 8,
            max_work: 10_000,
            max_mutations: 10_000,
        }
    }

    #[test]
    fn prepared_logical_range_survives_reopen_without_post_setup_allocation() {
        let mut workspace =
            LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(capacity()).unwrap();
        assert!(workspace.retained_bytes() > 0);

        let (_, allocations) = count_thread_allocations(|| {
            workspace
                .prepare(selected(), |engine| {
                    engine.assign(Ipv4Key(10), Ipv4Key(20), 1)?;
                    engine.assign(Ipv4Key(15), Ipv4Key(17), 2)?;
                    engine.assign(Ipv4Key(21), Ipv4Key(30), 1)
                })
                .unwrap();

            {
                let (source, staged, staging) = workspace.reopen_prepared_staging().unwrap();
                assert_eq!(source.txn_id, 7);
                assert_eq!(staged.page_count(), 1);
                let assignments = [RangeTreePhysicalAssignment {
                    pgno: 12,
                    authorization: PrivatePageAuthorization::Appended,
                }];
                let mut output = [PrivatePageCoordinatorTerminalPage::empty(); 1];
                let materialized = staging
                    .materialize(staged, 64, &assignments, &mut output)
                    .unwrap();
                assert_eq!(materialized.root_pgno, 12);
                assert_eq!(materialized.record_count, 3);
                let leaf = RangeLeaf::<Ipv4Key>::open(
                    &output[0].bytes,
                    8,
                    AddressFamily::Ipv4,
                    ValueKind::Direct,
                )
                .unwrap();
                assert_eq!(leaf.len(), 3);
                assert_eq!(leaf.record(0).unwrap().from, Ipv4Key(10));
                assert_eq!(leaf.record(0).unwrap().to, Ipv4Key(14));
                assert_eq!(leaf.record(0).unwrap().value, 1);
                assert_eq!(leaf.record(1).unwrap().from, Ipv4Key(15));
                assert_eq!(leaf.record(1).unwrap().to, Ipv4Key(17));
                assert_eq!(leaf.record(1).unwrap().value, 2);
                assert_eq!(leaf.record(2).unwrap().from, Ipv4Key(18));
                assert_eq!(leaf.record(2).unwrap().to, Ipv4Key(30));
                assert_eq!(leaf.record(2).unwrap().value, 1);
            }
            assert_eq!(
                workspace.reopen_prepared_staging().unwrap_err(),
                LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput
            );
            workspace.discard_after_abort();
        });

        assert_eq!(allocations, 0);
        assert!(workspace.is_idle());
    }

    #[test]
    fn failed_input_scrubs_owned_logical_storage() {
        let mut workspace =
            LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(capacity()).unwrap();
        let error = workspace
            .prepare(selected(), |engine| {
                engine.assign(Ipv4Key(10), Ipv4Key(20), 1)?;
                engine.assign(Ipv4Key(20), Ipv4Key(10), 1)
            })
            .unwrap_err();
        assert_eq!(
            error,
            LinuxLiveWriterNormalRangeWorkspaceError::Input(
                SequentialAssignmentError::RangeReversed
            )
        );
        assert!(workspace.is_idle());
        assert_eq!(
            workspace.reopen_prepared_staging().unwrap_err(),
            LinuxLiveWriterNormalRangeWorkspaceError::NoPreparedOutput
        );
    }

    #[test]
    fn empty_replacement_needs_no_logical_page() {
        let mut workspace = LinuxLiveWriterNormalRangeWorkspace::<Ipv4Key>::new(
            LinuxLiveWriterNormalRangeWorkspaceCapacity {
                normalizer_pages: 0,
                staged_range_pages: 0,
                max_assignments: 0,
                max_work: 0,
                max_mutations: 0,
            },
        )
        .unwrap();
        workspace.prepare(selected(), |_| Ok(())).unwrap();
        {
            let (_source, staged, staging) = workspace.reopen_prepared_staging().unwrap();
            assert_eq!(staged.page_count(), 0);
            let assignments: [RangeTreePhysicalAssignment; 0] = [];
            let mut output: [PrivatePageCoordinatorTerminalPage; 0] = [];
            let materialized = staging
                .materialize(staged, 64, &assignments, &mut output)
                .unwrap();
            assert_eq!(materialized.root_pgno, 0);
            assert_eq!(materialized.record_count, 0);
        }
        workspace.finish_after_publication();
        assert!(workspace.is_idle());
    }
}
