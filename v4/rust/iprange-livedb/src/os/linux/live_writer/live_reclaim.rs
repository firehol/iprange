//! Internal clean-writer Reclaim operation scaffolding.
//!
//! Reclaim is unlike a normal pending writer transaction: it must select a
//! safe retirement prefix while holding the live operation barrier, and a
//! no-change result must leave no draft or generation behind.  The operation
//! owner therefore keeps all pre-draft planning and shadow finalization local
//! to the held barrier.

use super::*;

use crate::bitmap_cow::{
    BitmapCowArenaBinding, BitmapCowIndexNode, FreeBitmapCowError,
    FreeBitmapFinalizationCachedPage, FreeBitmapFinalizationScratch, FreeBitmapInsertPage,
    FreeBitmapReclamationTicket, FreeBitmapReservationBuffers, FreeBitmapReservationSourceNode,
    FreeBitmapReservationStageBuffers, SealedFreeBitmapCoordinatorScratch, VerifiedBitmapPage,
};
use crate::contract::MAX_TREE_LEVEL;
use crate::private_page_pool::{
    PrivatePageCompositeBind, PrivatePageCoordinatorPriorReturn,
    PrivatePageCoordinatorTerminalPage, PrivatePagePool, PrivatePagePoolSlot,
    PrivatePagePreparedScopeSlot, PrivatePageSelectiveOverlayNode, PrivatePageSelectivePathEntry,
    PrivatePageSparseReplayIndex, PrivatePageSparseReplaySlot,
};
use crate::reclamation_finalizer::{
    finalize_selected_reclamation_terminal_export, plan_locked_reclamation_bitmap_reservation,
    preview_selected_reclamation_protected_pages, LockedReclamationBitmapPlanOutcome,
    LockedReclamationFinalizerError, LockedReclamationFinalizerLimits,
    LockedReclamationFinalizerScratch, ReclamationProtectedPagesError,
    ReclamationProtectedPagesScratch, SelectedReclamationRetirementScratch,
    SelectedReclamationTerminalCompositionError, SelectedReclamationTerminalCompositionFailure,
    SelectedReclamationTerminalScratch,
};
use crate::retirement_page::RetirementBatch;
use crate::retirement_writer::{
    CommittedPageOrigin, CommittedPageReplacement, PageRoleIndexSlot, RetirementPathFrame,
};
use crate::writer_fixed_point::{
    DraftPrivatePageEntry, DraftPrivatePageLocation, FixedPointCoordinatorJournals,
    FixedPointCoordinatorWorkspace, FixedPointError, FixedPointMapJournalWrite,
    FixedPointPreparedWorkSlot, FixedPointSourceJournalWrite, FixedPointTombstoneJournalWrite,
    FixedPointWorkspaceRecordSlot,
};
use crate::writer_transaction_contract::{PrivateCleanupEntry, PrivateWriterResourceBudget};
use core::cell::Cell;
use core::mem;

/// Bounded work limits for one internal clean-writer Reclaim attempt.
///
/// This remains crate-private. The opaque SDK-owned workspace validates these
/// limits before it takes a writer barrier; no caller supplies raw backing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LinuxLiveWriterReclaimLimits {
    pub(crate) max_batches: u64,
    pub(crate) max_pages: u64,
    pub(crate) bitmap_payload_pages: usize,
}

impl From<LinuxLiveWriterReclaimLimits> for LockedReclamationFinalizerLimits {
    fn from(value: LinuxLiveWriterReclaimLimits) -> Self {
        Self {
            max_batches: value.max_batches,
            max_pages: value.max_pages,
            bitmap_payload_pages: value.bitmap_payload_pages,
        }
    }
}

/// Explicit retained capacity for the private Reclaim workspace.
///
/// These are semantic limits, not caller-provided backing arrays. The workspace
/// owns every resulting allocation and checks the complete logical size before
/// it allocates any of them.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LinuxLiveWriterReclaimWorkspaceCapacity {
    /// Maximum complete terminal output and live transaction scope.
    pub(crate) max_live_pages: usize,
    /// Maximum exact shadow scope used while binding the selected bitmap plan.
    pub(crate) max_shadow_pages: usize,
    /// Maximum retirement batches that one attempt may verify.
    pub(crate) max_reclamation_batches: usize,
    /// Maximum reclaimed page numbers that one attempt may retain.
    pub(crate) max_reclaimed_pages: usize,
    /// Largest bitmap payload-page limit accepted by this workspace.
    pub(crate) max_bitmap_payload_pages: usize,
    /// Common bounded planner/finalizer working-set capacity.
    pub(crate) scratch_slots: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterReclaimWorkspaceResource {
    Batches,
    Pages,
    BitmapPayloadPages,
}

/// Workspace construction or preflight failure before a Reclaim barrier is
/// acquired. No draft or file mutation exists for these failures.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterReclaimWorkspaceError {
    InvalidCapacity,
    CapacityOverflow,
    Allocation,
    LimitExceedsCapacity {
        resource: LinuxLiveWriterReclaimWorkspaceResource,
        required: u64,
        actual: usize,
    },
    Coordinator(FixedPointError),
}

const EMPTY_REPLACEMENT: CommittedPageReplacement = CommittedPageReplacement {
    pgno: 0,
    origin: CommittedPageOrigin::RetirementTree,
};

const EMPTY_RETIREMENT_BATCH: RetirementBatch = RetirementBatch {
    retired_by_txn: 0,
    page_count: 0,
    page_list_blob_root: 0,
};

#[derive(Clone, Copy)]
struct LinuxLiveWriterReclaimWorkspaceLayout {
    live_pages: usize,
    shadow_pages: usize,
    batches: usize,
    reclaimed_pages: usize,
    scratch: usize,
    double_scratch: usize,
    triple_scratch: usize,
    quadruple_scratch: usize,
    octuple_scratch: usize,
    double_live_pages: usize,
    retirement_path: usize,
    retained_bytes: u64,
}

impl LinuxLiveWriterReclaimWorkspaceLayout {
    fn checked_multiple(
        value: usize,
        multiplier: usize,
    ) -> Result<usize, LinuxLiveWriterReclaimWorkspaceError> {
        value
            .checked_mul(multiplier)
            .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)
    }

    fn add_bytes<T>(
        total: &mut u64,
        count: usize,
    ) -> Result<(), LinuxLiveWriterReclaimWorkspaceError> {
        let bytes = mem::size_of::<T>()
            .checked_mul(count)
            .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
        let bytes = u64::try_from(bytes)
            .map_err(|_| LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
        *total = total
            .checked_add(bytes)
            .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
        Ok(())
    }

    fn new(
        capacity: LinuxLiveWriterReclaimWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterReclaimWorkspaceError> {
        if capacity.max_live_pages == 0
            || capacity.max_shadow_pages == 0
            || capacity.max_reclamation_batches == 0
            || capacity.max_reclaimed_pages == 0
            || capacity.max_bitmap_payload_pages == 0
            || capacity.scratch_slots == 0
        {
            return Err(LinuxLiveWriterReclaimWorkspaceError::InvalidCapacity);
        }
        let double_scratch = Self::checked_multiple(capacity.scratch_slots, 2)?;
        let triple_scratch = Self::checked_multiple(capacity.scratch_slots, 3)?;
        let quadruple_scratch = Self::checked_multiple(capacity.scratch_slots, 4)?;
        let octuple_scratch = Self::checked_multiple(capacity.scratch_slots, 8)?;
        let double_live_pages = Self::checked_multiple(capacity.max_live_pages, 2)?;
        let retirement_path = usize::from(MAX_TREE_LEVEL)
            .checked_add(1)
            .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;

        let mut retained_bytes =
            u64::try_from(mem::size_of::<LinuxLiveWriterReclaimWorkspace>())
                .map_err(|_| LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
        // The temporary borrowing view and its one record slot are fixed
        // control state charged with the owning workspace, even though they
        // live on the operation stack rather than in an additional heap block.
        Self::add_bytes::<FixedPointCoordinatorWorkspace<'static, 'static, 'static>>(
            &mut retained_bytes,
            1,
        )?;
        Self::add_bytes::<FixedPointWorkspaceRecordSlot<'static, 'static>>(&mut retained_bytes, 1)?;

        for (element_size, count) in [
            (
                mem::size_of::<PrivatePagePoolSlot>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<Option<PrivateCleanupEntry<(), (), LinuxLiveWriterPageSinkError>>>(),
                0,
            ),
            (mem::size_of::<u8>(), 0),
            (
                mem::size_of::<BitmapCowArenaBinding>(),
                capacity.max_live_pages,
            ),
            (mem::size_of::<u32>(), 0),
            (
                mem::size_of::<BitmapCowIndexNode>(),
                capacity.max_live_pages,
            ),
            (mem::size_of::<Cell<bool>>(), capacity.max_live_pages),
            (
                mem::size_of::<PrivatePageSelectiveOverlayNode>(),
                octuple_scratch,
            ),
            (
                mem::size_of::<PrivatePageSelectivePathEntry>(),
                octuple_scratch,
            ),
            (mem::size_of::<usize>(), capacity.max_live_pages),
            (
                mem::size_of::<Cell<Option<DraftPrivatePageEntry>>>(),
                capacity.max_live_pages,
            ),
            (mem::size_of::<Cell<usize>>(), capacity.max_live_pages),
            (mem::size_of::<Cell<usize>>(), capacity.max_live_pages),
            (
                mem::size_of::<Cell<FixedPointSourceJournalWrite>>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<Cell<FixedPointMapJournalWrite>>(),
                double_live_pages,
            ),
            (
                mem::size_of::<Cell<FixedPointTombstoneJournalWrite>>(),
                capacity.max_live_pages,
            ),
            (mem::size_of::<DraftPrivatePageLocation>(), 0),
            (mem::size_of::<PrivatePageCoordinatorPriorReturn>(), 0),
            (
                mem::size_of::<DraftPrivatePageLocation>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<PrivatePageSparseReplaySlot>(),
                octuple_scratch,
            ),
            (
                mem::size_of::<PrivatePageSparseReplayIndex>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<PrivatePagePoolSlot>(),
                capacity.scratch_slots,
            ),
            (
                mem::size_of::<PrivatePageCompositeBind>(),
                capacity.scratch_slots,
            ),
            (
                mem::size_of::<BitmapCowArenaBinding>(),
                capacity.scratch_slots,
            ),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<VerifiedBitmapPage>(), capacity.scratch_slots),
            (mem::size_of::<u32>(), quadruple_scratch),
            (mem::size_of::<BitmapCowIndexNode>(), octuple_scratch),
            (mem::size_of::<usize>(), capacity.scratch_slots),
            (
                mem::size_of::<FreeBitmapReservationSourceNode>(),
                double_scratch,
            ),
            (
                mem::size_of::<PrivatePagePoolSlot>(),
                capacity.scratch_slots,
            ),
            (
                mem::size_of::<BitmapCowArenaBinding>(),
                capacity.scratch_slots,
            ),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<VerifiedBitmapPage>(), capacity.scratch_slots),
            (mem::size_of::<u32>(), quadruple_scratch),
            (mem::size_of::<BitmapCowIndexNode>(), octuple_scratch),
            (mem::size_of::<usize>(), capacity.scratch_slots),
            (
                mem::size_of::<RetirementBatch>(),
                capacity.max_reclamation_batches,
            ),
            (mem::size_of::<u32>(), capacity.max_reclaimed_pages),
            (
                mem::size_of::<PrivatePagePoolSlot>(),
                capacity.max_shadow_pages,
            ),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (
                mem::size_of::<CommittedPageReplacement>(),
                capacity.scratch_slots,
            ),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<PageRoleIndexSlot>(), quadruple_scratch),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<u32>(), quadruple_scratch),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (
                mem::size_of::<CommittedPageReplacement>(),
                capacity.scratch_slots,
            ),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<PageRoleIndexSlot>(), quadruple_scratch),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<FreeBitmapInsertPage>(), octuple_scratch),
            (
                mem::size_of::<FreeBitmapFinalizationCachedPage>(),
                triple_scratch,
            ),
            (mem::size_of::<usize>(), octuple_scratch),
            (
                mem::size_of::<PrivatePageSelectiveOverlayNode>(),
                octuple_scratch,
            ),
            (
                mem::size_of::<PrivatePageSelectivePathEntry>(),
                octuple_scratch,
            ),
            (mem::size_of::<usize>(), capacity.scratch_slots),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (mem::size_of::<RetirementPathFrame>(), retirement_path),
            (
                mem::size_of::<CommittedPageReplacement>(),
                capacity.scratch_slots,
            ),
            (mem::size_of::<u32>(), capacity.scratch_slots),
            (mem::size_of::<PageRoleIndexSlot>(), quadruple_scratch),
            (
                mem::size_of::<PrivatePageCoordinatorTerminalPage>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<PrivatePageCoordinatorTerminalPage>(),
                capacity.max_live_pages,
            ),
            (
                mem::size_of::<PrivatePageCoordinatorTerminalPage>(),
                capacity.max_live_pages,
            ),
        ] {
            let bytes = element_size
                .checked_mul(count)
                .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
            let bytes = u64::try_from(bytes)
                .map_err(|_| LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
            retained_bytes = retained_bytes
                .checked_add(bytes)
                .ok_or(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)?;
        }

        Ok(Self {
            live_pages: capacity.max_live_pages,
            shadow_pages: capacity.max_shadow_pages,
            batches: capacity.max_reclamation_batches,
            reclaimed_pages: capacity.max_reclaimed_pages,
            scratch: capacity.scratch_slots,
            double_scratch,
            triple_scratch,
            quadruple_scratch,
            octuple_scratch,
            double_live_pages,
            retirement_path,
            retained_bytes,
        })
    }
}

/// Coordinator state retained by the opaque Reclaim workspace.
///
/// Keeping these partitions together makes the temporary fixed-point view
/// borrow-only: no caller can retain a reference into the workspace or manage
/// one of its internal value combinations.
struct LinuxLiveWriterReclaimCoordinatorStorage {
    live_slots: Vec<PrivatePagePoolSlot>,
    cleanup_entries: Vec<Option<PrivateCleanupEntry<(), (), LinuxLiveWriterPageSinkError>>>,
    work_slot: FixedPointPreparedWorkSlot,
    scope_slot: PrivatePagePreparedScopeSlot,
    preparation_scratch: Vec<u8>,

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

struct LinuxLiveWriterReclaimPlannerStorage {
    planner_arena: Vec<PrivatePagePoolSlot>,
    planner_pool_validation: Vec<PrivatePageCompositeBind>,
    planner_bindings: Vec<BitmapCowArenaBinding>,
    planner_candidates: Vec<u32>,
    planner_verified: Vec<VerifiedBitmapPage>,
    planner_replacements: Vec<u32>,
    planner_index: Vec<BitmapCowIndexNode>,
    planner_available: Vec<usize>,
    planner_source_nodes: Vec<FreeBitmapReservationSourceNode>,
    reclamation_ticket: FreeBitmapReclamationTicket,

    stage_arena: Vec<PrivatePagePoolSlot>,
    stage_bindings: Vec<BitmapCowArenaBinding>,
    stage_candidates: Vec<u32>,
    stage_verified: Vec<VerifiedBitmapPage>,
    stage_replacements: Vec<u32>,
    stage_index: Vec<BitmapCowIndexNode>,
    stage_available: Vec<usize>,
    verified_batches: Vec<RetirementBatch>,
    verified_pages: Vec<u32>,
    shadow_slots: Vec<PrivatePagePoolSlot>,
}

struct LinuxLiveWriterReclaimProtectedStorage {
    probe_delete_path: Vec<RetirementPathFrame>,
    probe_upsert_path: Vec<RetirementPathFrame>,
    probe_replacements: Vec<CommittedPageReplacement>,
    probe_releases: Vec<u32>,
    probe_roles: Vec<PageRoleIndexSlot>,
    protected_pages: Vec<u32>,
    next_protected_pages: Vec<u32>,
    preview_bitmap_replacements: Vec<u32>,
    preview_blob_pages: Vec<u32>,
    preview_delete_path: Vec<RetirementPathFrame>,
    preview_upsert_path: Vec<RetirementPathFrame>,
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

struct LinuxLiveWriterReclaimTerminalStorage {
    terminal_blob_pages: Vec<u32>,
    terminal_delete_path: Vec<RetirementPathFrame>,
    terminal_upsert_path: Vec<RetirementPathFrame>,
    terminal_replacements: Vec<CommittedPageReplacement>,
    terminal_releases: Vec<u32>,
    terminal_roles: Vec<PageRoleIndexSlot>,
    retirement_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    bitmap_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
    combined_terminal_pages: Vec<PrivatePageCoordinatorTerminalPage>,
}

/// Opaque SDK-owned memory for a bounded clean-writer Reclaim operation.
///
/// Construction allocates all partitions after checking their complete logical
/// capacity. Every operation thereafter only borrows and resets this storage;
/// it never allocates or lets a caller supply internal bitmaps or journals.
pub(crate) struct LinuxLiveWriterReclaimWorkspace {
    capacity: LinuxLiveWriterReclaimWorkspaceCapacity,
    retained_bytes: u64,
    dirty: bool,
    coordinator: LinuxLiveWriterReclaimCoordinatorStorage,
    planner: LinuxLiveWriterReclaimPlannerStorage,
    protected: LinuxLiveWriterReclaimProtectedStorage,
    terminal: LinuxLiveWriterReclaimTerminalStorage,
}

fn allocate_reclaim_workspace_vec<T>(
    len: usize,
    mut make: impl FnMut() -> T,
) -> Result<Vec<T>, LinuxLiveWriterReclaimWorkspaceError> {
    let mut values = Vec::new();
    values
        .try_reserve_exact(len)
        .map_err(|_| LinuxLiveWriterReclaimWorkspaceError::Allocation)?;
    for _ in 0..len {
        values.push(make());
    }
    Ok(values)
}

impl LinuxLiveWriterReclaimWorkspace {
    /// Creates one reusable bounded Reclaim workspace. This is the only point
    /// where this operation obtains heap storage.
    #[allow(clippy::too_many_lines)]
    pub(crate) fn new(
        capacity: LinuxLiveWriterReclaimWorkspaceCapacity,
    ) -> Result<Self, LinuxLiveWriterReclaimWorkspaceError> {
        let layout = LinuxLiveWriterReclaimWorkspaceLayout::new(capacity)?;
        let coordinator = LinuxLiveWriterReclaimCoordinatorStorage {
            live_slots: allocate_reclaim_workspace_vec(
                layout.live_pages,
                PrivatePagePoolSlot::empty,
            )?,
            cleanup_entries: allocate_reclaim_workspace_vec(0, || None)?,
            work_slot: FixedPointPreparedWorkSlot::empty(),
            scope_slot: PrivatePagePreparedScopeSlot::empty(),
            preparation_scratch: allocate_reclaim_workspace_vec(0, || 0)?,
            record_bindings: allocate_reclaim_workspace_vec(
                layout.live_pages,
                BitmapCowArenaBinding::empty,
            )?,
            record_replacements: allocate_reclaim_workspace_vec(0, || 0)?,
            record_index_nodes: allocate_reclaim_workspace_vec(
                layout.live_pages,
                BitmapCowIndexNode::empty,
            )?,
            record_returned: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::new(false)
            })?,
            record_cleanup_nodes: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                PrivatePageSelectiveOverlayNode::empty,
            )?,
            record_cleanup_path: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                PrivatePageSelectivePathEntry::empty,
            )?,
            record_cleanup_targets: allocate_reclaim_workspace_vec(layout.live_pages, || {
                usize::MAX
            })?,
            workspace_entries: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::<Option<DraftPrivatePageEntry>>::new(None)
            })?,
            workspace_source_map: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::new(usize::MAX)
            })?,
            workspace_record_map: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::new(usize::MAX)
            })?,
            source_journal: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::new(FixedPointSourceJournalWrite::EMPTY)
            })?,
            map_journal: allocate_reclaim_workspace_vec(layout.double_live_pages, || {
                Cell::new(FixedPointMapJournalWrite::EMPTY)
            })?,
            tombstone_journal: allocate_reclaim_workspace_vec(layout.live_pages, || {
                Cell::new(FixedPointTombstoneJournalWrite::EMPTY)
            })?,
            ordered_prior_locations: allocate_reclaim_workspace_vec(0, || {
                DraftPrivatePageLocation::EMPTY
            })?,
            pool_returns: allocate_reclaim_workspace_vec(
                0,
                PrivatePageCoordinatorPriorReturn::empty,
            )?,
            new_locations: allocate_reclaim_workspace_vec(layout.live_pages, || {
                DraftPrivatePageLocation::EMPTY
            })?,
            replay_slots: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                PrivatePageSparseReplaySlot::empty,
            )?,
            replay_index: allocate_reclaim_workspace_vec(
                layout.live_pages,
                PrivatePageSparseReplayIndex::empty,
            )?,
        };
        let planner = LinuxLiveWriterReclaimPlannerStorage {
            planner_arena: allocate_reclaim_workspace_vec(
                layout.scratch,
                PrivatePagePoolSlot::empty,
            )?,
            planner_pool_validation: allocate_reclaim_workspace_vec(
                layout.scratch,
                PrivatePageCompositeBind::empty,
            )?,
            planner_bindings: allocate_reclaim_workspace_vec(
                layout.scratch,
                BitmapCowArenaBinding::empty,
            )?,
            planner_candidates: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            planner_verified: allocate_reclaim_workspace_vec(
                layout.scratch,
                VerifiedBitmapPage::empty,
            )?,
            planner_replacements: allocate_reclaim_workspace_vec(layout.quadruple_scratch, || 0)?,
            planner_index: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                BitmapCowIndexNode::empty,
            )?,
            planner_available: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            planner_source_nodes: allocate_reclaim_workspace_vec(
                layout.double_scratch,
                FreeBitmapReservationSourceNode::empty,
            )?,
            reclamation_ticket: FreeBitmapReclamationTicket::new(),
            stage_arena: allocate_reclaim_workspace_vec(
                layout.scratch,
                PrivatePagePoolSlot::empty,
            )?,
            stage_bindings: allocate_reclaim_workspace_vec(
                layout.scratch,
                BitmapCowArenaBinding::empty,
            )?,
            stage_candidates: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            stage_verified: allocate_reclaim_workspace_vec(
                layout.scratch,
                VerifiedBitmapPage::empty,
            )?,
            stage_replacements: allocate_reclaim_workspace_vec(layout.quadruple_scratch, || 0)?,
            stage_index: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                BitmapCowIndexNode::empty,
            )?,
            stage_available: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            verified_batches: allocate_reclaim_workspace_vec(layout.batches, || {
                EMPTY_RETIREMENT_BATCH
            })?,
            verified_pages: allocate_reclaim_workspace_vec(layout.reclaimed_pages, || 0)?,
            shadow_slots: allocate_reclaim_workspace_vec(
                layout.shadow_pages,
                PrivatePagePoolSlot::empty,
            )?,
        };
        let protected = LinuxLiveWriterReclaimProtectedStorage {
            probe_delete_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            probe_upsert_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            probe_replacements: allocate_reclaim_workspace_vec(layout.scratch, || {
                EMPTY_REPLACEMENT
            })?,
            probe_releases: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            probe_roles: allocate_reclaim_workspace_vec(
                layout.quadruple_scratch,
                PageRoleIndexSlot::new,
            )?,
            protected_pages: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            next_protected_pages: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            preview_bitmap_replacements: allocate_reclaim_workspace_vec(
                layout.quadruple_scratch,
                || 0,
            )?,
            preview_blob_pages: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            preview_delete_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            preview_upsert_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            preview_replacements: allocate_reclaim_workspace_vec(layout.scratch, || {
                EMPTY_REPLACEMENT
            })?,
            preview_releases: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            preview_roles: allocate_reclaim_workspace_vec(
                layout.quadruple_scratch,
                PageRoleIndexSlot::new,
            )?,
            final_release_pages: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            final_insert_pages: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                FreeBitmapInsertPage::empty,
            )?,
            final_cached_pages: allocate_reclaim_workspace_vec(
                layout.triple_scratch,
                FreeBitmapFinalizationCachedPage::empty,
            )?,
            final_index_stack: allocate_reclaim_workspace_vec(layout.octuple_scratch, || {
                usize::MAX
            })?,
            final_cleanup_nodes: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                PrivatePageSelectiveOverlayNode::empty,
            )?,
            final_cleanup_path: allocate_reclaim_workspace_vec(
                layout.octuple_scratch,
                PrivatePageSelectivePathEntry::empty,
            )?,
            final_cleanup_targets: allocate_reclaim_workspace_vec(layout.scratch, || usize::MAX)?,
        };
        let terminal = LinuxLiveWriterReclaimTerminalStorage {
            terminal_blob_pages: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            terminal_delete_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            terminal_upsert_path: allocate_reclaim_workspace_vec(
                layout.retirement_path,
                RetirementPathFrame::new,
            )?,
            terminal_replacements: allocate_reclaim_workspace_vec(layout.scratch, || {
                EMPTY_REPLACEMENT
            })?,
            terminal_releases: allocate_reclaim_workspace_vec(layout.scratch, || 0)?,
            terminal_roles: allocate_reclaim_workspace_vec(
                layout.quadruple_scratch,
                PageRoleIndexSlot::new,
            )?,
            retirement_terminal_pages: allocate_reclaim_workspace_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            bitmap_terminal_pages: allocate_reclaim_workspace_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
            combined_terminal_pages: allocate_reclaim_workspace_vec(
                layout.live_pages,
                PrivatePageCoordinatorTerminalPage::empty,
            )?,
        };
        Ok(Self {
            capacity,
            retained_bytes: layout.retained_bytes,
            dirty: false,
            coordinator,
            planner,
            protected,
            terminal,
        })
    }

    fn bounded_capacity(value: usize) -> u64 {
        u64::try_from(value).unwrap_or(u64::MAX)
    }

    fn preflight_limits(
        &self,
        limits: LinuxLiveWriterReclaimLimits,
    ) -> Result<(), LinuxLiveWriterReclaimWorkspaceError> {
        let checks = [
            (
                LinuxLiveWriterReclaimWorkspaceResource::Batches,
                limits.max_batches,
                self.capacity.max_reclamation_batches,
            ),
            (
                LinuxLiveWriterReclaimWorkspaceResource::Pages,
                limits.max_pages,
                self.capacity.max_reclaimed_pages,
            ),
            (
                LinuxLiveWriterReclaimWorkspaceResource::BitmapPayloadPages,
                Self::bounded_capacity(limits.bitmap_payload_pages),
                self.capacity.max_bitmap_payload_pages,
            ),
        ];
        for (resource, required, actual) in checks {
            if required > Self::bounded_capacity(actual) {
                return Err(LinuxLiveWriterReclaimWorkspaceError::LimitExceedsCapacity {
                    resource,
                    required,
                    actual,
                });
            }
        }
        Ok(())
    }

    /// Clears every owned partition once between attempts. The first attempt
    /// uses construction-time canonical storage; later attempts pay one
    /// deterministic reset before taking the writer barrier.
    #[allow(clippy::too_many_lines)]
    fn reset_for_next_attempt(&mut self) {
        if !self.dirty {
            return;
        }

        for slot in &mut self.coordinator.live_slots {
            *slot = PrivatePagePoolSlot::empty();
        }
        for entry in &mut self.coordinator.cleanup_entries {
            *entry = None;
        }
        self.coordinator.work_slot = FixedPointPreparedWorkSlot::empty();
        self.coordinator.scope_slot = PrivatePagePreparedScopeSlot::empty();
        self.coordinator.preparation_scratch.fill(0);
        self.coordinator
            .record_bindings
            .fill(BitmapCowArenaBinding::empty());
        self.coordinator.record_replacements.fill(0);
        self.coordinator
            .record_index_nodes
            .fill(BitmapCowIndexNode::empty());
        for returned in &self.coordinator.record_returned {
            returned.set(false);
        }
        self.coordinator
            .record_cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        self.coordinator
            .record_cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        self.coordinator.record_cleanup_targets.fill(usize::MAX);
        for entry in &self.coordinator.workspace_entries {
            entry.set(None);
        }
        for entry in &self.coordinator.workspace_source_map {
            entry.set(usize::MAX);
        }
        for entry in &self.coordinator.workspace_record_map {
            entry.set(usize::MAX);
        }
        for entry in &self.coordinator.source_journal {
            entry.set(FixedPointSourceJournalWrite::EMPTY);
        }
        for entry in &self.coordinator.map_journal {
            entry.set(FixedPointMapJournalWrite::EMPTY);
        }
        for entry in &self.coordinator.tombstone_journal {
            entry.set(FixedPointTombstoneJournalWrite::EMPTY);
        }
        self.coordinator
            .ordered_prior_locations
            .fill(DraftPrivatePageLocation::EMPTY);
        self.coordinator
            .pool_returns
            .fill(PrivatePageCoordinatorPriorReturn::empty());
        self.coordinator
            .new_locations
            .fill(DraftPrivatePageLocation::EMPTY);
        self.coordinator
            .replay_slots
            .fill(PrivatePageSparseReplaySlot::empty());
        self.coordinator
            .replay_index
            .fill(PrivatePageSparseReplayIndex::empty());

        for slot in &mut self.planner.planner_arena {
            *slot = PrivatePagePoolSlot::empty();
        }
        self.planner
            .planner_pool_validation
            .fill(PrivatePageCompositeBind::empty());
        self.planner
            .planner_bindings
            .fill(BitmapCowArenaBinding::empty());
        self.planner.planner_candidates.fill(0);
        self.planner
            .planner_verified
            .fill(VerifiedBitmapPage::empty());
        self.planner.planner_replacements.fill(0);
        self.planner.planner_index.fill(BitmapCowIndexNode::empty());
        self.planner.planner_available.fill(0);
        self.planner
            .planner_source_nodes
            .fill(FreeBitmapReservationSourceNode::empty());
        self.planner.reclamation_ticket = FreeBitmapReclamationTicket::new();
        for slot in &mut self.planner.stage_arena {
            *slot = PrivatePagePoolSlot::empty();
        }
        self.planner
            .stage_bindings
            .fill(BitmapCowArenaBinding::empty());
        self.planner.stage_candidates.fill(0);
        self.planner
            .stage_verified
            .fill(VerifiedBitmapPage::empty());
        self.planner.stage_replacements.fill(0);
        self.planner.stage_index.fill(BitmapCowIndexNode::empty());
        self.planner.stage_available.fill(0);
        self.planner.verified_batches.fill(EMPTY_RETIREMENT_BATCH);
        self.planner.verified_pages.fill(0);
        for slot in &mut self.planner.shadow_slots {
            *slot = PrivatePagePoolSlot::empty();
        }

        self.protected
            .probe_delete_path
            .fill(RetirementPathFrame::new());
        self.protected
            .probe_upsert_path
            .fill(RetirementPathFrame::new());
        self.protected.probe_replacements.fill(EMPTY_REPLACEMENT);
        self.protected.probe_releases.fill(0);
        self.protected.probe_roles.fill(PageRoleIndexSlot::new());
        self.protected.protected_pages.fill(0);
        self.protected.next_protected_pages.fill(0);
        self.protected.preview_bitmap_replacements.fill(0);
        self.protected.preview_blob_pages.fill(0);
        self.protected
            .preview_delete_path
            .fill(RetirementPathFrame::new());
        self.protected
            .preview_upsert_path
            .fill(RetirementPathFrame::new());
        self.protected.preview_replacements.fill(EMPTY_REPLACEMENT);
        self.protected.preview_releases.fill(0);
        self.protected.preview_roles.fill(PageRoleIndexSlot::new());
        self.protected.final_release_pages.fill(0);
        for page in &mut self.protected.final_insert_pages {
            *page = FreeBitmapInsertPage::empty();
        }
        self.protected
            .final_cached_pages
            .fill(FreeBitmapFinalizationCachedPage::empty());
        self.protected.final_index_stack.fill(usize::MAX);
        self.protected
            .final_cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        self.protected
            .final_cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        self.protected.final_cleanup_targets.fill(usize::MAX);

        self.terminal.terminal_blob_pages.fill(0);
        self.terminal
            .terminal_delete_path
            .fill(RetirementPathFrame::new());
        self.terminal
            .terminal_upsert_path
            .fill(RetirementPathFrame::new());
        self.terminal.terminal_replacements.fill(EMPTY_REPLACEMENT);
        self.terminal.terminal_releases.fill(0);
        self.terminal.terminal_roles.fill(PageRoleIndexSlot::new());
        self.terminal
            .retirement_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        self.terminal
            .bitmap_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        self.terminal
            .combined_terminal_pages
            .fill(PrivatePageCoordinatorTerminalPage::empty());
        self.dirty = false;
    }

    #[allow(
        clippy::result_large_err,
        clippy::too_many_lines,
        clippy::type_complexity
    )]
    fn reclaim(
        &mut self,
        writer: &LinuxLiveWriter,
        limits: LinuxLiveWriterReclaimLimits,
        mut cancelled: impl FnMut() -> bool,
    ) -> Result<LinuxLiveWriterReclaimOutcome, LinuxLiveWriterReclaimError> {
        self.preflight_limits(limits)
            .map_err(LinuxLiveWriterReclaimError::Workspace)?;
        self.reset_for_next_attempt();
        // Any result may leave private planner state changed. The next attempt
        // therefore performs one canonical reset before it observes that state.
        self.dirty = true;

        let retained_bytes = self.retained_bytes;
        let coordinator = &mut self.coordinator;
        let planner = &mut self.planner;
        let protected = &mut self.protected;
        let terminal = &mut self.terminal;
        let workspace_records = [FixedPointWorkspaceRecordSlot::new(
            SealedFreeBitmapCoordinatorScratch {
                arena_bindings: &mut coordinator.record_bindings,
                replacements: &mut coordinator.record_replacements,
                index_nodes: &mut coordinator.record_index_nodes,
                returned: &coordinator.record_returned,
                cleanup_nodes: &mut coordinator.record_cleanup_nodes,
                cleanup_path: &mut coordinator.record_cleanup_path,
                cleanup_targets: &mut coordinator.record_cleanup_targets,
            },
        )];
        let journals = FixedPointCoordinatorJournals::new(
            &coordinator.source_journal,
            &coordinator.map_journal,
            &coordinator.tombstone_journal,
        );
        let mut fixed_point = FixedPointCoordinatorWorkspace::new(
            &workspace_records,
            &coordinator.workspace_entries,
            &coordinator.workspace_source_map,
            &coordinator.workspace_record_map,
            journals,
            &mut coordinator.ordered_prior_locations,
            &mut coordinator.pool_returns,
            &mut coordinator.new_locations,
            &mut coordinator.replay_slots,
            &mut coordinator.replay_index,
            coordinator.live_slots.len(),
        )
        .map_err(LinuxLiveWriterReclaimWorkspaceError::Coordinator)
        .map_err(LinuxLiveWriterReclaimError::Workspace)?;
        fixed_point
            .set_transaction_retained_bytes(retained_bytes)
            .map_err(LinuxLiveWriterReclaimWorkspaceError::Coordinator)
            .map_err(LinuxLiveWriterReclaimError::Workspace)?;
        debug_assert_eq!(fixed_point.retained_bytes(), retained_bytes);

        writer.reclaim_with_private_scratch(
            &mut fixed_point,
            limits,
            LinuxLiveWriterReclaimScratch {
                live_slots: &mut coordinator.live_slots,
                cleanup_entries: &mut coordinator.cleanup_entries,
                work_slot: &mut coordinator.work_slot,
                scope_slot: &mut coordinator.scope_slot,
                preparation_scratch: &mut coordinator.preparation_scratch,
                planner_arena: &mut planner.planner_arena,
                planner_pool_validation: &mut planner.planner_pool_validation,
                planner_bindings: &mut planner.planner_bindings,
                planner_candidates: &mut planner.planner_candidates,
                planner_verified: &mut planner.planner_verified,
                planner_replacements: &mut planner.planner_replacements,
                planner_index: &mut planner.planner_index,
                planner_available: &mut planner.planner_available,
                planner_source_nodes: &mut planner.planner_source_nodes,
                reclamation_ticket: &planner.reclamation_ticket,
                stage_arena: &mut planner.stage_arena,
                stage_bindings: &mut planner.stage_bindings,
                stage_candidates: &mut planner.stage_candidates,
                stage_verified: &mut planner.stage_verified,
                stage_replacements: &mut planner.stage_replacements,
                stage_index: &mut planner.stage_index,
                stage_available: &mut planner.stage_available,
                verified_batches: &mut planner.verified_batches,
                verified_pages: &mut planner.verified_pages,
                shadow_slots: &mut planner.shadow_slots,
                probe_delete_path: &mut protected.probe_delete_path,
                probe_upsert_path: &mut protected.probe_upsert_path,
                probe_replacements: &mut protected.probe_replacements,
                probe_releases: &mut protected.probe_releases,
                probe_roles: &mut protected.probe_roles,
                protected_pages: &mut protected.protected_pages,
                next_protected_pages: &mut protected.next_protected_pages,
                preview_bitmap_replacements: &mut protected.preview_bitmap_replacements,
                preview_blob_pages: &mut protected.preview_blob_pages,
                preview_delete_path: &mut protected.preview_delete_path,
                preview_upsert_path: &mut protected.preview_upsert_path,
                preview_replacements: &mut protected.preview_replacements,
                preview_releases: &mut protected.preview_releases,
                preview_roles: &mut protected.preview_roles,
                final_release_pages: &mut protected.final_release_pages,
                final_insert_pages: &mut protected.final_insert_pages,
                final_cached_pages: &mut protected.final_cached_pages,
                final_index_stack: &mut protected.final_index_stack,
                final_cleanup_nodes: &mut protected.final_cleanup_nodes,
                final_cleanup_path: &mut protected.final_cleanup_path,
                final_cleanup_targets: &mut protected.final_cleanup_targets,
                terminal_blob_pages: &mut terminal.terminal_blob_pages,
                terminal_delete_path: &mut terminal.terminal_delete_path,
                terminal_upsert_path: &mut terminal.terminal_upsert_path,
                terminal_replacements: &mut terminal.terminal_replacements,
                terminal_releases: &mut terminal.terminal_releases,
                terminal_roles: &mut terminal.terminal_roles,
                retirement_terminal_pages: &mut terminal.retirement_terminal_pages,
                bitmap_terminal_pages: &mut terminal.bitmap_terminal_pages,
                combined_terminal_pages: &mut terminal.combined_terminal_pages,
            },
            &mut cancelled,
        )
    }
}

/// Borrowed view over one opaque Reclaim workspace.
///
/// The operation borrows this as one unit and never lets it escape the held
/// Linux operation barrier. It is deliberately private: the owning workspace
/// is the only code that can assemble these internal partitions.
struct LinuxLiveWriterReclaimScratch<'a> {
    /// Exact live transaction pages, used only after selected terminal output
    /// establishes their required count.
    live_slots: &'a mut [PrivatePagePoolSlot],
    cleanup_entries: &'a mut [Option<PrivateCleanupEntry<(), (), LinuxLiveWriterPageSinkError>>],
    work_slot: &'a mut FixedPointPreparedWorkSlot,
    scope_slot: &'a mut PrivatePagePreparedScopeSlot,
    preparation_scratch: &'a mut [u8],

    // Lock-held bitmap selection and reservation planning.
    planner_arena: &'a mut [PrivatePagePoolSlot],
    planner_pool_validation: &'a mut [PrivatePageCompositeBind],
    planner_bindings: &'a mut [BitmapCowArenaBinding],
    planner_candidates: &'a mut [u32],
    planner_verified: &'a mut [VerifiedBitmapPage],
    planner_replacements: &'a mut [u32],
    planner_index: &'a mut [BitmapCowIndexNode],
    planner_available: &'a mut [usize],
    planner_source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    reclamation_ticket: &'a FreeBitmapReclamationTicket,
    stage_arena: &'a mut [PrivatePagePoolSlot],
    stage_bindings: &'a mut [BitmapCowArenaBinding],
    stage_candidates: &'a mut [u32],
    stage_verified: &'a mut [VerifiedBitmapPage],
    stage_replacements: &'a mut [u32],
    stage_index: &'a mut [BitmapCowIndexNode],
    stage_available: &'a mut [usize],
    verified_batches: &'a mut [RetirementBatch],
    verified_pages: &'a mut [u32],

    // An isolated pool rebuilt only after selection has supplied exact size.
    shadow_slots: &'a mut [PrivatePagePoolSlot],

    // Read-only selected-reclaim fixed-point preview.
    probe_delete_path: &'a mut [RetirementPathFrame],
    probe_upsert_path: &'a mut [RetirementPathFrame],
    probe_replacements: &'a mut [CommittedPageReplacement],
    probe_releases: &'a mut [u32],
    probe_roles: &'a mut [PageRoleIndexSlot],
    protected_pages: &'a mut [u32],
    next_protected_pages: &'a mut [u32],
    preview_bitmap_replacements: &'a mut [u32],
    preview_blob_pages: &'a mut [u32],
    preview_delete_path: &'a mut [RetirementPathFrame],
    preview_upsert_path: &'a mut [RetirementPathFrame],
    preview_replacements: &'a mut [CommittedPageReplacement],
    preview_releases: &'a mut [u32],
    preview_roles: &'a mut [PageRoleIndexSlot],

    // Final bitmap and retirement terminal construction.
    final_release_pages: &'a mut [u32],
    final_insert_pages: &'a mut [FreeBitmapInsertPage],
    final_cached_pages: &'a mut [FreeBitmapFinalizationCachedPage],
    final_index_stack: &'a mut [usize],
    final_cleanup_nodes: &'a mut [PrivatePageSelectiveOverlayNode],
    final_cleanup_path: &'a mut [PrivatePageSelectivePathEntry],
    final_cleanup_targets: &'a mut [usize],
    terminal_blob_pages: &'a mut [u32],
    terminal_delete_path: &'a mut [RetirementPathFrame],
    terminal_upsert_path: &'a mut [RetirementPathFrame],
    terminal_replacements: &'a mut [CommittedPageReplacement],
    terminal_releases: &'a mut [u32],
    terminal_roles: &'a mut [PageRoleIndexSlot],
    retirement_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    bitmap_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    combined_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
}

/// Observable result of the internal clean-writer Reclaim path.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LinuxLiveWriterReclaimOutcome {
    /// No safe complete retirement batch was selected.  No draft was started.
    NoChange,
    /// One selected prefix was finalized and its target metadata was made durable.
    Reclaimed(Bootstrap),
}

/// Failure before a physical Reclaim publication attempt is recorded.
///
/// Every `Core` error has already attempted the mandatory workspace
/// cancellation and whole-draft abort while the operation barrier was still
/// held. `cleanup_complete` makes any failed cleanup explicit instead of
/// silently reusing caller-owned backing.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterReclaimFailure {
    /// Cancellation was observed before physical publication. When a private
    /// draft had already begun, `cleanup_complete` reports the whole-draft
    /// abort result separately from the cancellation fact.
    Cancelled {
        cleanup_complete: bool,
    },
    /// Selection found no work, but releasing its non-publication barrier
    /// failed. This is not a cancellation and must not be reported as one.
    NoChangeRelease,
    FinalizationContext(LinuxLiveWriterFinalizationContextError),
    Planner(LockedReclamationFinalizerError),
    Bitmap(FreeBitmapCowError),
    ProtectedPages(ReclamationProtectedPagesError),
    Terminal(SelectedReclamationTerminalCompositionError),
    ShadowCapacity {
        required: usize,
        actual: usize,
    },
    ShadowPool(crate::private_page_pool::PrivatePagePoolError),
    LiveCapacity {
        required: usize,
        actual: usize,
    },
    EmptyTerminal,
    FinalizationScratchCapacity,
    CommitNonce(LinuxOsError),
    PublicationPreflight {
        cause: LinuxWriterLeaseError,
        cleanup_complete: bool,
    },
    Core {
        cause: PrivateWriterTransactionError<LinuxLiveWriterPageSinkError>,
        cleanup_complete: bool,
    },
}

/// Failure of the self-contained internal Reclaim operation.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterReclaimError {
    /// The opaque operation workspace cannot satisfy this attempt before the
    /// writer barrier is acquired. The committed file is untouched.
    Workspace(LinuxLiveWriterReclaimWorkspaceError),
    Barrier(LinuxLiveWriterBarrierCause),
    Failed {
        cause: LinuxLiveWriterReclaimFailure,
        release: Option<LinuxLiveWriterBarrierReleaseError>,
    },
    Publication {
        source: LinuxLiveWriterCoreCommitError<LinuxLiveWriterPageSinkError>,
        cleanup_complete: bool,
    },
}

type LinuxLiveWriterReclaimCore<'slots, 'cleanup> =
    PrivateWriterTransactionCore<'slots, 'cleanup, (), (), LinuxLiveWriterPageSinkError>;

struct LinuxLiveWriterPreparedReclaim<'slots, 'cleanup> {
    core: LinuxLiveWriterReclaimCore<'slots, 'cleanup>,
    handle: PrivateWriterTransactionHandle,
}

enum LinuxLiveWriterReclaimBuild<'slots, 'cleanup> {
    NoChange,
    Prepared(LinuxLiveWriterPreparedReclaim<'slots, 'cleanup>),
}

enum LinuxLiveWriterReclaimCoreStageFailure {
    Cancelled {
        pre_cleanup_complete: bool,
    },
    Core {
        cause: PrivateWriterTransactionError<LinuxLiveWriterPageSinkError>,
        pre_cleanup_complete: bool,
    },
}

fn fail_after_nonpublication(
    barrier: LinuxLiveWriterOperationBarrier<'_>,
    cause: LinuxLiveWriterReclaimFailure,
) -> LinuxLiveWriterReclaimError {
    LinuxLiveWriterReclaimError::Failed {
        cause,
        release: barrier.release_after_nonpublication().err(),
    }
}

/// Releases every private transaction proof that can still be abandoned before
/// physical publication. The boolean is deliberately separate from the
/// original cause: a broken cleanup is a different operational fact.
fn abort_pending_reclaim<'slots, 'cleanup, 'backing, 'arena, 'record_cleanup>(
    core: &mut LinuxLiveWriterReclaimCore<'slots, 'cleanup>,
    handle: &PrivateWriterTransactionHandle,
    workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
    workspace_registered: bool,
) -> bool {
    let workspace_cancelled =
        !workspace_registered || core.cancel_fixed_point_workspace(handle, workspace).is_ok();
    let aborted = core.abort().is_ok();
    workspace_cancelled && aborted
}

fn clear_ephemeral_workspace(workspace: &mut FixedPointCoordinatorWorkspace<'_, '_, '_>) -> bool {
    workspace.cancel().is_ok() && workspace.is_idle()
}

fn coordinator_nonce(commit_nonce: [u8; 16]) -> u64 {
    let lower = u64::from_le_bytes(commit_nonce[..8].try_into().unwrap());
    let upper = u64::from_le_bytes(commit_nonce[8..].try_into().unwrap());
    // This nonce is private to one fresh coordinator pool. It must be nonzero,
    // but it has no caller-visible identity or persistence role.
    (lower ^ upper) | 1
}

impl LinuxLiveWriter {
    /// Runs Reclaim through its SDK-owned bounded workspace.
    #[allow(clippy::result_large_err)]
    pub(crate) fn reclaim_with_workspace(
        &self,
        workspace: &mut LinuxLiveWriterReclaimWorkspace,
        limits: LinuxLiveWriterReclaimLimits,
        cancelled: impl FnMut() -> bool,
    ) -> Result<LinuxLiveWriterReclaimOutcome, LinuxLiveWriterReclaimError> {
        workspace.reclaim(self, limits, cancelled)
    }

    /// Runs one self-contained, clean-writer Reclaim using only preallocated
    /// workspace-owned backing.
    ///
    /// The selected source, reader fence, shadow allocation, private draft,
    /// output drain, target-meta publication, and writer-lease transition all
    /// remain under one Linux operation barrier. A no-change result releases
    /// that barrier without ever constructing a transaction core.
    #[allow(
        clippy::result_large_err,
        clippy::too_many_lines,
        clippy::type_complexity
    )]
    fn reclaim_with_private_scratch<'slots, 'backing, 'arena, 'record_cleanup>(
        &self,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        limits: LinuxLiveWriterReclaimLimits,
        scratch: LinuxLiveWriterReclaimScratch<'slots>,
        mut cancelled: impl FnMut() -> bool,
    ) -> Result<LinuxLiveWriterReclaimOutcome, LinuxLiveWriterReclaimError>
    where
        'arena: 'backing,
    {
        let barrier = match self.acquire_operation_barrier_with_cancel(&mut cancelled) {
            Ok(barrier) => barrier,
            Err(LinuxLiveWriterBarrierError::Failed(cause)) => {
                return Err(LinuxLiveWriterReclaimError::Barrier(cause));
            }
            Err(LinuxLiveWriterBarrierError::Locked { cause, mut barrier }) => {
                barrier.force_close_only_after_publication();
                return Err(LinuxLiveWriterReclaimError::Barrier(cause));
            }
        };

        let mut scratch = Some(scratch);
        let built = match barrier.with_finalization_context(|context| {
            let (
                live_slots,
                cleanup_entries,
                work_slot,
                scope_slot,
                preparation_scratch,
                planner_arena,
                planner_pool_validation,
                planner_bindings,
                planner_candidates,
                planner_verified,
                planner_replacements,
                planner_index,
                planner_available,
                planner_source_nodes,
                reclamation_ticket,
                stage_arena,
                stage_bindings,
                stage_candidates,
                stage_verified,
                stage_replacements,
                stage_index,
                stage_available,
                verified_batches,
                verified_pages,
                shadow_slots,
                probe_delete_path,
                probe_upsert_path,
                probe_replacements,
                probe_releases,
                probe_roles,
                protected_pages,
                next_protected_pages,
                preview_bitmap_replacements,
                preview_blob_pages,
                preview_delete_path,
                preview_upsert_path,
                preview_replacements,
                preview_releases,
                preview_roles,
                final_release_pages,
                final_insert_pages,
                final_cached_pages,
                final_index_stack,
                final_cleanup_nodes,
                final_cleanup_path,
                final_cleanup_targets,
                terminal_blob_pages,
                terminal_delete_path,
                terminal_upsert_path,
                terminal_replacements,
                terminal_releases,
                terminal_roles,
                retirement_terminal_pages,
                bitmap_terminal_pages,
                combined_terminal_pages,
            ) = {
                let LinuxLiveWriterReclaimScratch {
                    live_slots,
                    cleanup_entries,
                    work_slot,
                    scope_slot,
                    preparation_scratch,
                    planner_arena,
                    planner_pool_validation,
                    planner_bindings,
                    planner_candidates,
                    planner_verified,
                    planner_replacements,
                    planner_index,
                    planner_available,
                    planner_source_nodes,
                    reclamation_ticket,
                    stage_arena,
                    stage_bindings,
                    stage_candidates,
                    stage_verified,
                    stage_replacements,
                    stage_index,
                    stage_available,
                    verified_batches,
                    verified_pages,
                    shadow_slots,
                    probe_delete_path,
                    probe_upsert_path,
                    probe_replacements,
                    probe_releases,
                    probe_roles,
                    protected_pages,
                    next_protected_pages,
                    preview_bitmap_replacements,
                    preview_blob_pages,
                    preview_delete_path,
                    preview_upsert_path,
                    preview_replacements,
                    preview_releases,
                    preview_roles,
                    final_release_pages,
                    final_insert_pages,
                    final_cached_pages,
                    final_index_stack,
                    final_cleanup_nodes,
                    final_cleanup_path,
                    final_cleanup_targets,
                    terminal_blob_pages,
                    terminal_delete_path,
                    terminal_upsert_path,
                    terminal_replacements,
                    terminal_releases,
                    terminal_roles,
                    retirement_terminal_pages,
                    bitmap_terminal_pages,
                    combined_terminal_pages,
                } = scratch
                    .take()
                    .expect("one Reclaim attempt consumes its private scratch exactly once");
                (
                    live_slots,
                    cleanup_entries,
                    work_slot,
                    scope_slot,
                    preparation_scratch,
                    planner_arena,
                    planner_pool_validation,
                    planner_bindings,
                    planner_candidates,
                    planner_verified,
                    planner_replacements,
                    planner_index,
                    planner_available,
                    planner_source_nodes,
                    reclamation_ticket,
                    stage_arena,
                    stage_bindings,
                    stage_candidates,
                    stage_verified,
                    stage_replacements,
                    stage_index,
                    stage_available,
                    verified_batches,
                    verified_pages,
                    shadow_slots,
                    probe_delete_path,
                    probe_upsert_path,
                    probe_replacements,
                    probe_releases,
                    probe_roles,
                    protected_pages,
                    next_protected_pages,
                    preview_bitmap_replacements,
                    preview_blob_pages,
                    preview_delete_path,
                    preview_upsert_path,
                    preview_replacements,
                    preview_releases,
                    preview_roles,
                    final_release_pages,
                    final_insert_pages,
                    final_cached_pages,
                    final_index_stack,
                    final_cleanup_nodes,
                    final_cleanup_path,
                    final_cleanup_targets,
                    terminal_blob_pages,
                    terminal_delete_path,
                    terminal_upsert_path,
                    terminal_replacements,
                    terminal_releases,
                    terminal_roles,
                    retirement_terminal_pages,
                    bitmap_terminal_pages,
                    combined_terminal_pages,
                )
            };
            let (selected, pages, reclaim_fence) = context.into_parts();
            if cancelled() {
                return Err(LinuxLiveWriterReclaimFailure::Cancelled {
                    cleanup_complete: true,
                });
            }

            let plan = plan_locked_reclamation_bitmap_reservation(
                selected.meta,
                &pages,
                reclaim_fence,
                limits.into(),
                LockedReclamationFinalizerScratch {
                    bitmap: FreeBitmapReservationBuffers {
                        arena: planner_arena,
                        pool_validation: planner_pool_validation,
                        arena_bindings: planner_bindings,
                        candidates: planner_candidates,
                        verified_pages: planner_verified,
                        replacements: planner_replacements,
                        index_nodes: planner_index,
                        available_slots: planner_available,
                        source_nodes: planner_source_nodes,
                        reclamation: reclamation_ticket,
                        stage: FreeBitmapReservationStageBuffers {
                            arena: stage_arena,
                            arena_bindings: stage_bindings,
                            candidates: stage_candidates,
                            verified_pages: stage_verified,
                            replacements: stage_replacements,
                            index_nodes: stage_index,
                            available_slots: stage_available,
                        },
                    },
                    verified_batches,
                    verified_pages,
                },
            )
            .map_err(LinuxLiveWriterReclaimFailure::Planner)?;

            let plan = match plan {
                LockedReclamationBitmapPlanOutcome::NoChange => {
                    return Ok(LinuxLiveWriterReclaimBuild::NoChange);
                }
                LockedReclamationBitmapPlanOutcome::Selected(plan) => plan,
            };
            if cancelled() {
                return Err(LinuxLiveWriterReclaimFailure::Cancelled {
                    cleanup_complete: true,
                });
            }
            let shadow_capacity = plan.required_private_pages();
            if shadow_capacity > shadow_slots.len() {
                return Err(LinuxLiveWriterReclaimFailure::ShadowCapacity {
                    required: shadow_capacity,
                    actual: shadow_slots.len(),
                });
            }
            let pending_txn = selected
                .meta
                .txn_id
                .checked_add(1)
                .ok_or(LinuxLiveWriterReclaimFailure::EmptyTerminal)?;
            let shadow_pool = PrivatePagePool::new_vacant(
                &mut shadow_slots[..shadow_capacity],
                selected.meta.page_count,
                selected.meta.page_count,
                pending_txn,
            )
            .map_err(LinuxLiveWriterReclaimFailure::ShadowPool)?;
            let shadow_scope = shadow_pool
                .reserve_scope(shadow_capacity)
                .map_err(LinuxLiveWriterReclaimFailure::ShadowPool)?;
            let mut reservation = plan
                .bind(&shadow_pool, &shadow_scope)
                .map_err(LinuxLiveWriterReclaimFailure::Planner)?;
            if cancelled() {
                return Err(LinuxLiveWriterReclaimFailure::Cancelled {
                    cleanup_complete: true,
                });
            }

            let protected = preview_selected_reclamation_protected_pages(
                &mut reservation.bound,
                ReclamationProtectedPagesScratch {
                    probe_delete_path,
                    probe_upsert_path,
                    probe_replacements,
                    probe_releases,
                    probe_roles,
                    protected_pages,
                    next_protected_pages,
                    preview_bitmap_replacements,
                    preview_blob_pages,
                    preview_delete_path,
                    preview_upsert_path,
                    preview_replacements,
                    preview_releases,
                    preview_roles,
                    final_release_pages: &mut *final_release_pages,
                    final_insert_pages: &mut *final_insert_pages,
                    final_cached_pages: &mut *final_cached_pages,
                    final_index_stack: &mut *final_index_stack,
                    final_cleanup_nodes: &mut *final_cleanup_nodes,
                    final_cleanup_path: &mut *final_cleanup_path,
                    final_cleanup_targets: &mut *final_cleanup_targets,
                },
            )
            .map_err(LinuxLiveWriterReclaimFailure::ProtectedPages)?;
            let requirements = reservation
                .bound
                .finalization_scratch_requirements()
                .map_err(LinuxLiveWriterReclaimFailure::Bitmap)?;
            if requirements.release_pages > final_release_pages.len()
                || requirements.insert_pages > final_insert_pages.len()
                || requirements.cached_pages > final_cached_pages.len()
                || requirements.index_stack > final_index_stack.len()
                || requirements.cleanup_nodes > final_cleanup_nodes.len()
                || requirements.cleanup_path > final_cleanup_path.len()
                || requirements.cleanup_targets > final_cleanup_targets.len()
            {
                return Err(LinuxLiveWriterReclaimFailure::FinalizationScratchCapacity);
            }
            if cancelled() {
                return Err(LinuxLiveWriterReclaimFailure::Cancelled {
                    cleanup_complete: true,
                });
            }

            let terminal = match finalize_selected_reclamation_terminal_export(
                reservation,
                &shadow_pool,
                &shadow_scope,
                protected,
                SelectedReclamationTerminalScratch {
                    retirement: SelectedReclamationRetirementScratch {
                        blob_pages: terminal_blob_pages,
                        delete_path: terminal_delete_path,
                        upsert_path: terminal_upsert_path,
                        replacements: terminal_replacements,
                        releases: terminal_releases,
                        roles: terminal_roles,
                        terminal_pages: retirement_terminal_pages,
                    },
                    bitmap_finalization: FreeBitmapFinalizationScratch {
                        release_pages: &mut final_release_pages[..requirements.release_pages],
                        insert_pages: &mut final_insert_pages[..requirements.insert_pages],
                        cached_pages: &mut final_cached_pages[..requirements.cached_pages],
                        index_stack: &mut final_index_stack[..requirements.index_stack],
                        cleanup_nodes: &mut final_cleanup_nodes[..requirements.cleanup_nodes],
                        cleanup_path: &mut final_cleanup_path[..requirements.cleanup_path],
                        cleanup_targets: &mut final_cleanup_targets[..requirements.cleanup_targets],
                    },
                    bitmap_terminal_pages,
                    combined_terminal_pages,
                },
            ) {
                Ok(terminal) => terminal,
                Err(SelectedReclamationTerminalCompositionFailure::Retry { error, .. })
                | Err(SelectedReclamationTerminalCompositionFailure::Discard { error }) => {
                    return Err(LinuxLiveWriterReclaimFailure::Terminal(error));
                }
            };
            let terminal_count = terminal.terminal_pages().len();
            if terminal_count == 0 {
                return Err(LinuxLiveWriterReclaimFailure::EmptyTerminal);
            }
            if terminal_count > live_slots.len() {
                return Err(LinuxLiveWriterReclaimFailure::LiveCapacity {
                    required: terminal_count,
                    actual: live_slots.len(),
                });
            }
            if cancelled() {
                return Err(LinuxLiveWriterReclaimFailure::Cancelled {
                    cleanup_complete: true,
                });
            }
            let commit_nonce =
                random_nonzero_128().map_err(LinuxLiveWriterReclaimFailure::CommitNonce)?;
            let terminal_nonce = coordinator_nonce(commit_nonce);
            let terminal_pages = u64::try_from(terminal_count).map_err(|_| {
                LinuxLiveWriterReclaimFailure::LiveCapacity {
                    required: usize::MAX,
                    actual: live_slots.len(),
                }
            })?;
            let mut core = LinuxLiveWriterReclaimCore::new(
                selected.meta,
                PrivateWriterResourceBudget::new(
                    workspace.retained_bytes(),
                    terminal_pages,
                    terminal_pages,
                    2,
                ),
                &mut live_slots[..terminal_count],
                cleanup_entries,
            )
            .map_err(|cause| LinuxLiveWriterReclaimFailure::Core {
                cause,
                cleanup_complete: true,
            })?;
            let handle =
                core.begin(commit_nonce)
                    .map_err(|cause| LinuxLiveWriterReclaimFailure::Core {
                        cause,
                        cleanup_complete: true,
                    })?;
            if cancelled() {
                let cleanup_complete = abort_pending_reclaim(&mut core, &handle, workspace, false);
                return Err(LinuxLiveWriterReclaimFailure::Cancelled { cleanup_complete });
            }
            if let Err(cause) = core.reserve_fixed_point_workspace(&handle, workspace) {
                let cleanup_complete = abort_pending_reclaim(&mut core, &handle, workspace, false);
                return Err(LinuxLiveWriterReclaimFailure::Core {
                    cause,
                    cleanup_complete,
                });
            }
            let stage = (|| -> Result<(), LinuxLiveWriterReclaimCoreStageFailure> {
                let predecessor = core
                    .fixed_point(&handle)
                    .and_then(|coordinator| {
                        coordinator
                            .predecessor()
                            .map_err(PrivateWriterTransactionError::FixedPoint)
                    })
                    .map_err(|cause| LinuxLiveWriterReclaimCoreStageFailure::Core {
                        cause,
                        pre_cleanup_complete: true,
                    })?;
                let reserved = match (core.fixed_point(&handle), core.draft(&handle)) {
                    (Ok(coordinator), Ok(live_pool)) => coordinator
                        .prepare_reserved_work(
                            &predecessor,
                            live_pool,
                            1,
                            terminal_count,
                            work_slot,
                            scope_slot,
                            preparation_scratch,
                        )
                        .map_err(PrivateWriterTransactionError::FixedPoint)
                        .map_err(|cause| LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete: true,
                        })?,
                    (Err(cause), _) | (_, Err(cause)) => {
                        return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete: true,
                        });
                    }
                };
                if cancelled() {
                    let pre_cleanup_complete = core
                        .draft(&handle)
                        .ok()
                        .and_then(|pool| reserved.cancel(pool).ok())
                        .is_some();
                    return Err(LinuxLiveWriterReclaimCoreStageFailure::Cancelled {
                        pre_cleanup_complete,
                    });
                }
                let produced = match core.draft(&handle) {
                    Ok(live_pool) => {
                        match terminal.bind_to_reserved_work(reserved, live_pool, terminal_nonce) {
                            Ok(produced) => produced,
                            Err((reserved, _terminal, error)) => {
                                let pre_cleanup_complete = core
                                    .draft(&handle)
                                    .ok()
                                    .and_then(|pool| reserved.cancel(pool).ok())
                                    .is_some();
                                return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                                    cause: PrivateWriterTransactionError::FixedPoint(error),
                                    pre_cleanup_complete,
                                });
                            }
                        }
                    }
                    Err(cause) => {
                        let pre_cleanup_complete = core
                            .draft(&handle)
                            .ok()
                            .and_then(|pool| reserved.cancel(pool).ok())
                            .is_some();
                        return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete,
                        });
                    }
                };
                if cancelled() {
                    let pre_cleanup_complete = core
                        .draft(&handle)
                        .ok()
                        .and_then(|pool| produced.cancel(pool).ok())
                        .is_some();
                    return Err(LinuxLiveWriterReclaimCoreStageFailure::Cancelled {
                        pre_cleanup_complete,
                    });
                }
                let aggregate = match (core.fixed_point(&handle), core.draft(&handle)) {
                    (Ok(coordinator), Ok(live_pool)) => match workspace.prepare_aggregate(
                        produced,
                        coordinator,
                        &predecessor,
                        live_pool,
                        &pages,
                        &[],
                    ) {
                        Ok(aggregate) => aggregate,
                        Err((produced, error)) => {
                            let pre_cleanup_complete = produced.cancel(live_pool).is_ok();
                            return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                                cause: PrivateWriterTransactionError::FixedPoint(error),
                                pre_cleanup_complete,
                            });
                        }
                    },
                    (Err(cause), Ok(live_pool)) => {
                        let pre_cleanup_complete = produced.cancel(live_pool).is_ok();
                        return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete,
                        });
                    }
                    (Ok(_), Err(cause)) | (Err(cause), Err(_)) => {
                        return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete: false,
                        });
                    }
                };
                if cancelled() {
                    let pre_cleanup_complete = core
                        .draft(&handle)
                        .ok()
                        .and_then(|pool| aggregate.cancel(pool).ok())
                        .is_some();
                    return Err(LinuxLiveWriterReclaimCoreStageFailure::Cancelled {
                        pre_cleanup_complete,
                    });
                }
                let sealed =
                    match core.execute_fixed_point_aggregate(&handle, predecessor, aggregate) {
                        Ok(sealed) => sealed,
                        Err((aggregate, _predecessor, error)) => {
                            let pre_cleanup_complete = core
                                .draft(&handle)
                                .ok()
                                .and_then(|pool| aggregate.cancel(pool).ok())
                                .is_some();
                            return Err(LinuxLiveWriterReclaimCoreStageFailure::Core {
                                cause: PrivateWriterTransactionError::FixedPoint(error),
                                pre_cleanup_complete,
                            });
                        }
                    };
                let successor = core
                    .complete_fixed_point_aggregate(&handle, workspace, sealed)
                    .map_err(|cause| LinuxLiveWriterReclaimCoreStageFailure::Core {
                        cause,
                        pre_cleanup_complete: true,
                    })?;
                core.finish_fixed_point_input(&handle, workspace, successor)
                    .map_err(|(_successor, cause)| {
                        LinuxLiveWriterReclaimCoreStageFailure::Core {
                            cause,
                            pre_cleanup_complete: true,
                        }
                    })?;
                Ok(())
            })();
            if let Err(stage) = stage {
                match stage {
                    LinuxLiveWriterReclaimCoreStageFailure::Cancelled {
                        pre_cleanup_complete,
                    } => {
                        let cleanup_complete = pre_cleanup_complete
                            && abort_pending_reclaim(&mut core, &handle, workspace, true);
                        return Err(LinuxLiveWriterReclaimFailure::Cancelled { cleanup_complete });
                    }
                    LinuxLiveWriterReclaimCoreStageFailure::Core {
                        cause,
                        pre_cleanup_complete,
                    } => {
                        let cleanup_complete = pre_cleanup_complete
                            && abort_pending_reclaim(&mut core, &handle, workspace, true);
                        return Err(LinuxLiveWriterReclaimFailure::Core {
                            cause,
                            cleanup_complete,
                        });
                    }
                }
            }
            Ok(LinuxLiveWriterReclaimBuild::Prepared(
                LinuxLiveWriterPreparedReclaim { core, handle },
            ))
        }) {
            Ok(built) => built,
            Err(context) => {
                return Err(fail_after_nonpublication(
                    barrier,
                    LinuxLiveWriterReclaimFailure::FinalizationContext(context),
                ));
            }
        };

        match built {
            Err(cause) => Err(fail_after_nonpublication(barrier, cause)),
            Ok(LinuxLiveWriterReclaimBuild::NoChange) => {
                match barrier.release_after_nonpublication() {
                    Ok(()) => Ok(LinuxLiveWriterReclaimOutcome::NoChange),
                    Err(release) => Err(LinuxLiveWriterReclaimError::Failed {
                        cause: LinuxLiveWriterReclaimFailure::NoChangeRelease,
                        release: Some(release),
                    }),
                }
            }
            Ok(LinuxLiveWriterReclaimBuild::Prepared(prepared)) => {
                self.publish_reclaim_prepared(barrier, workspace, prepared, &mut cancelled)
            }
        }
    }
}

impl LinuxLiveWriter {
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    fn publish_reclaim_prepared<'slots, 'cleanup, 'backing, 'arena, 'record_cleanup>(
        &self,
        barrier: LinuxLiveWriterOperationBarrier<'_>,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        prepared: LinuxLiveWriterPreparedReclaim<'slots, 'cleanup>,
        cancelled: &mut impl FnMut() -> bool,
    ) -> Result<LinuxLiveWriterReclaimOutcome, LinuxLiveWriterReclaimError>
    where
        'arena: 'backing,
    {
        let LinuxLiveWriterPreparedReclaim { mut core, handle } = prepared;
        if cancelled() {
            let cleanup_complete = abort_pending_reclaim(&mut core, &handle, workspace, true);
            return Err(fail_after_nonpublication(
                barrier,
                LinuxLiveWriterReclaimFailure::Cancelled { cleanup_complete },
            ));
        }
        let preparation = match core.prepare_fixed_point_private_output(&handle, workspace) {
            Ok(preparation) => preparation,
            Err(cause) => {
                let cleanup_complete = abort_pending_reclaim(&mut core, &handle, workspace, true);
                return Err(fail_after_nonpublication(
                    barrier,
                    LinuxLiveWriterReclaimFailure::Core {
                        cause,
                        cleanup_complete,
                    },
                ));
            }
        };
        if cancelled() {
            let cleanup_complete = abort_pending_reclaim(&mut core, &handle, workspace, true);
            return Err(fail_after_nonpublication(
                barrier,
                LinuxLiveWriterReclaimFailure::Cancelled { cleanup_complete },
            ));
        }

        let target = preparation.target();
        let mut authorization = None::<PrivateWriterMetaPublication>;
        let mut workspace_registered = true;
        let publication = barrier.publish_private_pages(target, |sink| {
            let mut write = |pgno: u32, bytes: &[u8]| sink.write_page(pgno, bytes);
            core.drain_fixed_point_private_pages(&handle, &preparation, workspace, &mut write)?;
            let completed =
                core.finish_fixed_point_private_output(&handle, preparation, workspace)?;
            workspace_registered = false;
            authorization = Some(completed);
            Ok(())
        });

        match publication {
            Ok(publication) => {
                let Some(authorization) = authorization else {
                    publication.force_close_only();
                    let cleanup_complete = clear_ephemeral_workspace(workspace);
                    return Err(LinuxLiveWriterReclaimError::Publication {
                        source: LinuxLiveWriterCoreCommitError::MissingCorePublicationAuthority {
                            phase_five: None,
                        },
                        cleanup_complete,
                    });
                };
                let target = publication.target();
                if let Err(core_error) = core.confirm_durable_publication(&handle, authorization) {
                    publication.force_close_only();
                    let cleanup_complete = clear_ephemeral_workspace(workspace);
                    return Err(LinuxLiveWriterReclaimError::Publication {
                        source: LinuxLiveWriterCoreCommitError::CoreAfterDurablePublication {
                            phase_five: None,
                            core: core_error,
                        },
                        cleanup_complete,
                    });
                }
                match publication.release() {
                    Ok(()) => Ok(LinuxLiveWriterReclaimOutcome::Reclaimed(target)),
                    Err((publication, release)) => {
                        drop(publication);
                        Err(LinuxLiveWriterReclaimError::Publication {
                            source: LinuxLiveWriterCoreCommitError::ReleaseAfterDurablePublication(
                                release,
                            ),
                            cleanup_complete: true,
                        })
                    }
                }
            }
            Err((barrier, LinuxLiveWriterPublicationError::Preflight(publication))) => {
                let cleanup_complete =
                    abort_pending_reclaim(&mut core, &handle, workspace, workspace_registered);
                let cause = LinuxLiveWriterReclaimFailure::PublicationPreflight {
                    cause: publication,
                    cleanup_complete,
                };
                Err(fail_after_nonpublication(barrier, cause))
            }
            Err((barrier, LinuxLiveWriterPublicationError::NotCommitted(cause))) => {
                let cleanup_complete =
                    abort_pending_reclaim(&mut core, &handle, workspace, workspace_registered);
                drop(barrier);
                Err(LinuxLiveWriterReclaimError::Publication {
                    source: LinuxLiveWriterCoreCommitError::Publication(
                        LinuxLiveWriterPublicationError::NotCommitted(cause),
                    ),
                    cleanup_complete,
                })
            }
            Err((barrier, LinuxLiveWriterPublicationError::OutcomeUnknown(cause))) => {
                let completion = match authorization.as_ref() {
                    Some(authorization) => {
                        core.mark_publication_outcome_unknown(&handle, authorization)
                    }
                    None => {
                        core.force_publication_outcome_unknown();
                        Err(PrivateWriterTransactionError::StaleHandle)
                    }
                };
                let cleanup_complete =
                    !workspace_registered || clear_ephemeral_workspace(workspace);
                drop(barrier);
                let source = match completion {
                    Ok(()) => LinuxLiveWriterCoreCommitError::Publication(
                        LinuxLiveWriterPublicationError::OutcomeUnknown(cause),
                    ),
                    Err(core) => LinuxLiveWriterCoreCommitError::CoreAfterOutcomeUnknown {
                        publication: cause,
                        core,
                    },
                };
                Err(LinuxLiveWriterReclaimError::Publication {
                    source,
                    cleanup_complete,
                })
            }
            Err((barrier, LinuxLiveWriterPublicationError::Committed(cause))) => {
                let Some(authorization) = authorization else {
                    drop(barrier);
                    let cleanup_complete =
                        !workspace_registered || clear_ephemeral_workspace(workspace);
                    return Err(LinuxLiveWriterReclaimError::Publication {
                        source: LinuxLiveWriterCoreCommitError::MissingCorePublicationAuthority {
                            phase_five: Some(cause),
                        },
                        cleanup_complete,
                    });
                };
                let completion = core.confirm_durable_publication(&handle, authorization);
                let cleanup_complete =
                    !workspace_registered || clear_ephemeral_workspace(workspace);
                drop(barrier);
                let source = match completion {
                    Ok(()) => LinuxLiveWriterCoreCommitError::Publication(
                        LinuxLiveWriterPublicationError::Committed(cause),
                    ),
                    Err(core) => LinuxLiveWriterCoreCommitError::CoreAfterDurablePublication {
                        phase_five: Some(cause),
                        core,
                    },
                };
                Err(LinuxLiveWriterReclaimError::Publication {
                    source,
                    cleanup_complete,
                })
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const TEST_CAPACITY: LinuxLiveWriterReclaimWorkspaceCapacity =
        LinuxLiveWriterReclaimWorkspaceCapacity {
            max_live_pages: 3,
            max_shadow_pages: 4,
            max_reclamation_batches: 1,
            max_reclaimed_pages: 2,
            max_bitmap_payload_pages: 2,
            scratch_slots: 4,
        };

    #[test]
    fn opaque_reclaim_workspace_rejects_invalid_and_overflowing_capacity_before_allocating() {
        let mut invalid = TEST_CAPACITY;
        invalid.max_live_pages = 0;
        assert!(matches!(
            LinuxLiveWriterReclaimWorkspace::new(invalid),
            Err(LinuxLiveWriterReclaimWorkspaceError::InvalidCapacity)
        ));

        let mut overflowing = TEST_CAPACITY;
        overflowing.scratch_slots = usize::MAX;
        assert!(matches!(
            LinuxLiveWriterReclaimWorkspace::new(overflowing),
            Err(LinuxLiveWriterReclaimWorkspaceError::CapacityOverflow)
        ));
    }

    #[test]
    fn opaque_reclaim_workspace_rejects_out_of_capacity_limits_before_an_attempt() {
        let workspace = LinuxLiveWriterReclaimWorkspace::new(TEST_CAPACITY).unwrap();
        assert!(!workspace.dirty);
        assert!(workspace.retained_bytes > 0);
        assert_eq!(
            workspace.preflight_limits(LinuxLiveWriterReclaimLimits {
                max_batches: 1,
                max_pages: 3,
                bitmap_payload_pages: 2,
            }),
            Err(LinuxLiveWriterReclaimWorkspaceError::LimitExceedsCapacity {
                resource: LinuxLiveWriterReclaimWorkspaceResource::Pages,
                required: 3,
                actual: 2,
            })
        );
        assert!(
            !workspace.dirty,
            "limit rejection must occur before the workspace starts an attempt"
        );
    }
}
