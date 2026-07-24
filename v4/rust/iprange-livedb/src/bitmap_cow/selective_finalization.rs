//! Exact-scope, two-pass finalization for a bound bitmap reservation.

use super::*;
use crate::private_page_pool::{
    private_page_selective_scratch_requirements, PrivatePageSelectiveError,
    PrivatePageSelectiveOverlayNode, PrivatePageSelectivePathEntry, PrivatePageSelectiveScratch,
};
use crate::range_root_proof::{
    RangeRootRetirementStage, RangeRootRetirementStageStateError, RangeRootTransactionProof,
};
use crate::range_staging::RangeTreeMaterializedResult;
use crate::retirement_writer::{ProducedBitmapRootProvenance, RetirementTreeEditResult};
use core::cell::{Cell, RefCell};

static FINALIZATION_NONCE: AtomicU64 = AtomicU64::new(0);

// Both preview and terminal finalization must construct the identical detached
// bitmap state. Keeping this as one expansion prevents their COW rules from
// drifting while preserving the stack-only lifetime of the stage pool.
macro_rules! build_finalization_shadow {
    (
        $bound:ident,
        $live_scope:ident,
        $source:expr,
        $pool:ident,
        $scope:ident,
        $shadow:ident
    ) => {
        $bound.stage.arena_bindings[..$bound.private_pages].fill(BitmapCowArenaBinding::empty());
        $bound.stage.replacements.fill(0);
        $bound.stage.candidates.fill(0);
        $bound.stage.index_nodes.fill(BitmapCowIndexNode::empty());
        $bound.stage.available_slots.fill(0);
        for page in $bound.stage.verified_pages.iter_mut() {
            *page = VerifiedBitmapPage::empty();
        }
        $bound.stage.replacements[..$bound.cow.replacements.len()]
            .copy_from_slice($bound.cow.replacements);
        $bound.stage.candidates[..$bound.cow.candidates.len()]
            .copy_from_slice($bound.cow.candidates);
        let $pool = PrivatePagePool::new_vacant(
            &mut $bound.stage.arena[..$bound.private_pages],
            $bound.cow.committed_page_count,
            $bound.cow.pending_page_count,
            $bound.cow.pending_txn,
        )
        .map_err(FreeBitmapCowError::PrivatePool)?;
        let $scope = $pool
            .reserve_scope($bound.private_pages)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let ledger = ScopedFreeBitmapCowLedger::new(
            &mut $bound.stage.arena_bindings[..$bound.private_pages],
            &mut $bound.stage.replacements[..$bound.cow.replacements.len()],
            $bound.cow.replacement_len,
            &mut $bound.stage.candidates[..$bound.cow.candidates.len()],
            0,
            &mut $bound.stage.index_nodes[..$bound.cow.index_nodes.len()],
            &mut $bound.stage.available_slots[..$bound.private_pages],
            &mut $bound.stage.verified_pages[..0],
            $bound.cow.planned_candidate_len,
            $bound.cow.reservation_planned,
            $bound.cow.payload_page_budget,
            $bound.cow.planned_required_private_pages,
        );
        let mut $shadow = FreeBitmapCow::from_scoped_pool_with_pending_txn(
            $source,
            $bound.cow.selected_txn,
            $bound.cow.pending_txn,
            $bound.cow.pending_page_count,
            $bound.cow.root,
            &$pool,
            &$scope,
            ledger,
        )?;
        let mut checkpoint = Some(
            $pool
                .begin_checkpoint()
                .map_err(FreeBitmapCowError::PrivatePool)?,
        );
        for binding in &$bound.cow.arena_bindings[..$bound.private_pages] {
            let info = $bound.cow.scoped_slot_info(binding.pool_slot)?;
            let authorization = info
                .authorization
                .ok_or(FreeBitmapCowError::ArenaPageConflict(info.pgno))?;
            let active_checkpoint = checkpoint
                .as_ref()
                .ok_or(FreeBitmapCowError::ArenaPageConflict(info.pgno))?;
            if let Err(error) =
                $pool.bind_page(active_checkpoint, &$scope, info.pgno, authorization)
            {
                let rollback = checkpoint
                    .take()
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(info.pgno))?;
                match $pool.rollback_checkpoint(rollback) {
                    Ok(()) => Err(FreeBitmapCowError::PrivatePool(error))?,
                    Err((_checkpoint, rollback_error)) => {
                        Err(FreeBitmapCowError::PrivatePool(rollback_error))?
                    }
                }
            }
        }
        $pool
            .commit_checkpoint(
                checkpoint
                    .take()
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?,
            )
            .map_err(|(_checkpoint, error)| FreeBitmapCowError::PrivatePool(error))?;
        for index in 0..$bound.private_pages {
            let live_slot = $bound.cow.arena_bindings[index].pool_slot;
            let desired = $bound
                .cow
                .pool()
                .finalized_slot(&$live_scope, live_slot)
                .map_err(finalization_error)?;
            let stage_slot = $shadow.arena_bindings[index].pool_slot;
            $pool
                .install_finalized_slot_in_shadow(&$scope, stage_slot, &desired)
                .map_err(finalization_error)?;
        }
        let selected_target = $bound.cow.selected_candidate_target();
        $shadow.select_planned_candidate_prefix(selected_target)?;
        $shadow.synchronize_scoped_bindings_for_candidate_prefix(&$scope, selected_target)?;
        $shadow.candidate_len = $bound.cow.candidate_len;
        $shadow.validate_scoped_bindings()?;
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct FreeBitmapFinalizationCachedPage {
    pgno: u32,
    bytes: [u8; PAGE_SIZE],
    occupied: bool,
}

impl FreeBitmapFinalizationCachedPage {
    pub(crate) const fn empty() -> Self {
        Self {
            pgno: 0,
            bytes: [0; PAGE_SIZE],
            occupied: false,
        }
    }
}

pub(crate) struct FreeBitmapFinalizationScratch<'scratch> {
    pub(crate) release_pages: &'scratch mut [u32],
    pub(crate) insert_pages: &'scratch mut [FreeBitmapInsertPage],
    pub(crate) cached_pages: &'scratch mut [FreeBitmapFinalizationCachedPage],
    pub(crate) index_stack: &'scratch mut [usize],
    pub(crate) cleanup_nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    pub(crate) cleanup_path: &'scratch mut [PrivatePageSelectivePathEntry],
    pub(crate) cleanup_targets: &'scratch mut [usize],
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct FreeBitmapFinalizationScratchRequirements {
    pub(crate) release_pages: usize,
    pub(crate) insert_pages: usize,
    pub(crate) cached_pages: usize,
    pub(crate) index_stack: usize,
    pub(crate) cleanup_nodes: usize,
    pub(crate) cleanup_path: usize,
    pub(crate) cleanup_targets: usize,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum FreeBitmapFinalizationPreviewError<E> {
    Bitmap(FreeBitmapCowError),
    Stage(E),
}

impl<E> From<FreeBitmapCowError> for FreeBitmapFinalizationPreviewError<E> {
    fn from(error: FreeBitmapCowError) -> Self {
        Self::Bitmap(error)
    }
}

pub(crate) struct FreeBitmapFinalizationResult<
    'scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    pub(crate) output: SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>,
    pub(crate) successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
    pub(crate) released: UnusedReservationRelease,
    pub(crate) reinserted_reclaimed: usize,
}

pub(crate) struct SealedFreeBitmapOutput<
    'scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    cow: FreeBitmapCow<'a, 'slots, 'scope, S>,
    selected_bitmap_root: u32,
    scope: PrivatePageReservationScope<'slots>,
    nonce: u64,
    bitmap_terminal_page_count: usize,
    range_terminal_page_count: usize,
    retirement_terminal_page_count: usize,
    terminal_page_count: usize,
    cleanup_nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    cleanup_path: &'scratch mut [PrivatePageSelectivePathEntry],
    cleanup_targets: &'scratch mut [usize],
}

/// Producer-bound bitmap journal. The page bytes cannot be supplied directly:
/// this authority is created only by consuming the real bitmap finalizer
/// output together with its exact successor seed.
pub(crate) struct PreparedFreeBitmapTerminalExport<
    'pages,
    'scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    output: SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>,
    successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
    pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
}

/// Paired terminal journals taken from one sealed bitmap-finalization scope.
/// The range journal cannot be supplied independently of the exact scope that
/// retained its pages through finalization.
pub(crate) struct PreparedFreeBitmapRangeTerminalExport<
    'bitmap_pages,
    'range_pages,
    'scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    output: SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>,
    successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
    materialized: RangeTreeMaterializedResult,
    bitmap_pages: &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
    range_pages: &'range_pages mut [PrivatePageCoordinatorTerminalPage],
}

/// Three typed journals taken from one sealed scope after a proof-bound
/// retirement stage has completed. It is private transaction authority, not a
/// metadata or publication result.
pub(crate) struct PreparedFreeBitmapRangeRetirementTerminalExport<
    'bitmap_pages,
    'range_pages,
    'retirement_pages,
    'scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    output: SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>,
    successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
    materialized: RangeTreeMaterializedResult,
    retirement: RetirementTreeEditResult,
    bitmap_pages: &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
    range_pages: &'range_pages mut [PrivatePageCoordinatorTerminalPage],
    retirement_pages: &'retirement_pages mut [PrivatePageCoordinatorTerminalPage],
}

pub(crate) struct FreeBitmapFinalizationSuccessorSeed<'a, 'slots> {
    pool: &'a PrivatePagePool<'slots>,
    scope: PrivatePageReservationScope<'slots>,
    nonce: u64,
}

fn range_root_retirement_stage_error(
    error: RangeRootRetirementStageStateError,
) -> FreeBitmapCowError {
    match error {
        RangeRootRetirementStageStateError::Bitmap(error) => error,
        RangeRootRetirementStageStateError::Stale
        | RangeRootRetirementStageStateError::Proof(_)
        | RangeRootRetirementStageStateError::Retirement(_) => {
            FreeBitmapCowError::StaleReservationPredecessor
        }
    }
}

impl core::fmt::Debug for FreeBitmapFinalizationSuccessorSeed<'_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("FreeBitmapFinalizationSuccessorSeed")
            .field("nonce", &self.nonce)
            .finish_non_exhaustive()
    }
}

pub(crate) struct FreeBitmapFinalizationPredecessor<'a, 'slots> {
    pool: &'a PrivatePagePool<'slots>,
    scope: PrivatePageReservationScope<'slots>,
    nonce: u64,
    commitment: PrivatePagePoolCommitment,
}

/// Generic-free ownership retained by the transaction coordinator after one
/// bitmap work unit seals. The committed source is deliberately dropped; later
/// draft reads consult this exact private-page index and otherwise fall back to
/// the transaction's selected committed source.
pub(crate) struct SealedFreeBitmapCoordinatorRecord<'scratch, 'arena, 'pool, 'slots> {
    pub(crate) record_index: usize,
    pub(crate) work_unit: u64,
    pool: &'pool PrivatePagePool<'slots>,
    scope: PrivatePageReservationScope<'slots>,
    nonce: u64,
    cleanup: FreeBitmapFinalizationPredecessor<'pool, 'slots>,
    root: u32,
    pending_page_count: u64,
    arena_bindings: &'arena mut [BitmapCowArenaBinding],
    replacements: &'arena mut [u32],
    replacement_len: usize,
    index_nodes: &'arena mut [BitmapCowIndexNode],
    index_root: usize,
    returned: &'arena [Cell<bool>],
    cleanup_nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    cleanup_path: &'scratch mut [PrivatePageSelectivePathEntry],
    cleanup_targets: &'scratch mut [usize],
}

/// Fully constructed live-pool record image whose only remaining step is to
/// attach the already-proved live pool reference after future-state replay.
///
/// Despite the historical bitmap name, this canonical record owns every page
/// in its sealed coordinator scope. Bitmap and retirement pages remain
/// inseparable until output and scope cleanup complete.
pub(crate) struct PreparedFreeBitmapCoordinatorRecord<'arena, 'cleanup> {
    record_index: usize,
    work_unit: u64,
    scope: PrivatePageReservationScopeSeed,
    nonce: u64,
    commitment: PrivatePagePoolCommitment,
    root: u32,
    pending_page_count: u64,
    arena_bindings: &'arena mut [BitmapCowArenaBinding],
    replacements: &'arena mut [u32],
    index_nodes: &'arena mut [BitmapCowIndexNode],
    index_root: usize,
    returned: &'arena [Cell<bool>],
    cleanup_nodes: &'cleanup mut [PrivatePageSelectiveOverlayNode],
    cleanup_path: &'cleanup mut [PrivatePageSelectivePathEntry],
    cleanup_targets: &'cleanup mut [usize],
}

pub(crate) struct SealedFreeBitmapCoordinatorScratch<'arena, 'scratch> {
    pub(crate) arena_bindings: &'arena mut [BitmapCowArenaBinding],
    pub(crate) replacements: &'arena mut [u32],
    pub(crate) index_nodes: &'arena mut [BitmapCowIndexNode],
    pub(crate) returned: &'arena [Cell<bool>],
    pub(crate) cleanup_nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    pub(crate) cleanup_path: &'scratch mut [PrivatePageSelectivePathEntry],
    pub(crate) cleanup_targets: &'scratch mut [usize],
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum FreeBitmapCoordinatorOutputError<E> {
    Sink(E),
    Record(FreeBitmapCowError),
}

impl SealedFreeBitmapCoordinatorScratch<'_, '_> {
    pub(crate) fn is_canonical_for(&self, page_count: usize) -> bool {
        self.arena_bindings.len() >= page_count
            && self.index_nodes.len() >= page_count
            && self.returned.len() >= page_count
            && self.cleanup_targets.len() >= page_count
            && self
                .arena_bindings
                .iter()
                .all(|binding| *binding == BitmapCowArenaBinding::empty())
            && self.replacements.iter().all(|&page| page == 0)
            && self
                .index_nodes
                .iter()
                .all(|node| *node == BitmapCowIndexNode::empty())
            && self.returned.iter().all(|returned| !returned.get())
            && self
                .cleanup_nodes
                .iter()
                .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            && self
                .cleanup_path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            && self
                .cleanup_targets
                .iter()
                .all(|&target| target == usize::MAX)
    }

    fn clear(&mut self) {
        self.arena_bindings.fill(BitmapCowArenaBinding::empty());
        self.replacements.fill(0);
        self.index_nodes.fill(BitmapCowIndexNode::empty());
        for returned in self.returned.iter() {
            returned.set(false);
        }
        self.cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        self.cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        self.cleanup_targets.fill(NO_INDEX);
    }
}

impl core::fmt::Debug for SealedFreeBitmapCoordinatorRecord<'_, '_, '_, '_> {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter
            .debug_struct("SealedFreeBitmapCoordinatorRecord")
            .field("record_index", &self.record_index)
            .field("work_unit", &self.work_unit)
            .field("root", &self.root)
            .field("pending_page_count", &self.pending_page_count)
            .field("replacement_len", &self.replacement_len)
            .finish_non_exhaustive()
    }
}

impl<'scratch, 'arena, 'pool, 'slots>
    SealedFreeBitmapCoordinatorRecord<'scratch, 'arena, 'pool, 'slots>
{
    fn binding_returned(&self, binding_index: usize) -> bool {
        self.returned.get(binding_index).is_some_and(Cell::get)
    }

    pub(crate) fn returned_cell(&self, binding_index: usize) -> Option<&'arena Cell<bool>> {
        self.returned.get(binding_index)
    }

    pub(crate) fn cancel_inactive_into_scratch(
        self,
    ) -> SealedFreeBitmapCoordinatorScratch<'arena, 'scratch> {
        let Self {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
            ..
        } = self;
        arena_bindings.fill(BitmapCowArenaBinding::empty());
        replacements.fill(0);
        index_nodes.fill(BitmapCowIndexNode::empty());
        for returned in returned {
            returned.set(false);
        }
        cleanup_nodes.fill(PrivatePageSelectiveOverlayNode::empty());
        cleanup_path.fill(PrivatePageSelectivePathEntry::empty());
        cleanup_targets.fill(NO_INDEX);
        SealedFreeBitmapCoordinatorScratch {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
        }
    }

    pub(crate) const fn root(&self) -> u32 {
        self.root
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }

    pub(crate) fn replacements(&self) -> &[u32] {
        &self.replacements[..self.replacement_len]
    }

    pub(crate) fn map_bound_slots(
        &self,
        record_index: usize,
        slot_to_record: &mut [usize],
    ) -> Result<usize, PrivatePagePoolError> {
        let mut mapped = 0usize;
        for (binding_index, binding) in self.arena_bindings.iter().enumerate() {
            if !binding.bound || self.binding_returned(binding_index) {
                continue;
            }
            let destination = slot_to_record
                .get(binding.pool_slot)
                .ok_or(PrivatePagePoolError::SlotOutOfBounds(binding.pool_slot))?;
            if *destination != usize::MAX {
                return Err(PrivatePagePoolError::InvalidState(binding.pool_slot));
            }
            mapped += 1;
        }
        for (binding_index, binding) in self.arena_bindings.iter().enumerate() {
            if !binding.bound || self.binding_returned(binding_index) {
                continue;
            }
            slot_to_record[binding.pool_slot] = record_index;
        }
        Ok(mapped)
    }

    pub(crate) fn visit_private_pages(
        &self,
        mut visitor: impl FnMut(
            crate::writer_fixed_point::DraftPrivatePageLocation,
        ) -> Result<(), crate::writer_fixed_point::FixedPointError>,
    ) -> Result<usize, crate::writer_fixed_point::FixedPointError> {
        let mut visited = 0usize;
        for (binding_index, binding) in self.arena_bindings.iter().enumerate() {
            if !binding.bound || self.binding_returned(binding_index) {
                continue;
            }
            let page =
                match self
                    .pool
                    .sealed_page_provenance(&self.scope, self.nonce, binding.pool_slot)
                {
                    Ok(page) => page,
                    Err(PrivatePagePoolError::InvalidState(_)) => continue,
                    Err(_) => {
                        return Err(crate::writer_fixed_point::FixedPointError::StalePredecessor);
                    }
                };
            visitor(crate::writer_fixed_point::DraftPrivatePageLocation {
                provenance: crate::writer_fixed_point::DraftPageProvenance::Private {
                    work_unit: self.work_unit,
                    page,
                },
                nonce: self.nonce,
                record_index: self.record_index,
                binding_index,
            })?;
            visited += 1;
        }
        Ok(visited)
    }

    pub(crate) fn private_provenance(
        &self,
        pgno: u32,
    ) -> Result<Option<crate::writer_fixed_point::DraftPageProvenance>, PrivatePagePoolError> {
        let Some(IndexedPage::Arena(slot)) =
            page_index_find(self.index_nodes, self.index_root, pgno)
        else {
            return Ok(None);
        };
        let node = page_index_find_node(self.index_nodes, self.index_root, pgno)
            .ok_or(PrivatePagePoolError::InvalidState(slot))?;
        if self.binding_returned(node) {
            return Ok(None);
        }
        let page = self
            .pool
            .sealed_page_provenance(&self.scope, self.nonce, slot)?;
        Ok(Some(
            crate::writer_fixed_point::DraftPageProvenance::Private {
                work_unit: self.work_unit,
                page,
            },
        ))
    }

    pub(crate) fn read_private(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<bool, PrivatePagePoolError> {
        let Some(IndexedPage::Arena(slot)) =
            page_index_find(self.index_nodes, self.index_root, pgno)
        else {
            return Ok(false);
        };
        let node = page_index_find_node(self.index_nodes, self.index_root, pgno)
            .ok_or(PrivatePagePoolError::InvalidState(slot))?;
        if self.binding_returned(node) {
            return Ok(false);
        }
        let page = self
            .pool
            .borrow_sealed_page(&self.scope, self.nonce, slot)?;
        destination.copy_from_slice(&page[..]);
        Ok(true)
    }

    pub(crate) fn validate_private_return(
        &self,
        location: crate::writer_fixed_point::DraftPrivatePageLocation,
    ) -> Result<PrivatePageCoordinatorPriorReturn, FreeBitmapCowError> {
        let provenance = location.provenance;
        let crate::writer_fixed_point::DraftPageProvenance::Private { work_unit, page } =
            provenance
        else {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        };
        if work_unit != self.work_unit
            || location.nonce != self.nonce
            || location.record_index != self.record_index
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let node = page_index_find_node(self.index_nodes, self.index_root, page.pgno)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if self.index_nodes[node].page != IndexedPage::Arena(page.slot) {
            return Err(FreeBitmapCowError::ArenaPageConflict(page.pgno));
        }
        let binding = self
            .arena_bindings
            .get(location.binding_index)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if !binding.bound
            || self.binding_returned(location.binding_index)
            || binding.pool_slot != page.slot
            || binding.page_number != page.pgno
            || binding.active_node != node
            || binding.pool_epoch != page.binding_epoch
            || self
                .private_provenance(page.pgno)
                .map_err(FreeBitmapCowError::PrivatePool)?
                != Some(provenance)
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        Ok(PrivatePageCoordinatorPriorReturn {
            page,
            nonce: location.nonce,
        })
    }

    pub(crate) fn apply_private_return_terminal_prepared(
        &mut self,
        location: crate::writer_fixed_point::DraftPrivatePageLocation,
    ) {
        let crate::writer_fixed_point::DraftPageProvenance::Private { page, .. } =
            location.provenance
        else {
            unreachable!("prepared prior return retains private provenance");
        };
        let node = page_index_find_node(self.index_nodes, self.index_root, page.pgno)
            .expect("prepared prior return retains the exact record index node");
        let (root, removed) = page_index_delete(self.index_nodes, self.index_root, page.pgno);
        debug_assert_eq!(removed, node);
        self.index_root = root;
        self.index_nodes[removed] = BitmapCowIndexNode::empty();
        let binding = &mut self.arena_bindings[location.binding_index];
        binding.pool_epoch += 1;
        binding.page_number = 0;
        binding.active_node = NO_INDEX;
        binding.bound = false;
        self.cleanup.commitment = self
            .pool
            .exact_sealed_commitment_terminal_prepared(&self.scope, self.nonce);
    }

    #[cfg(test)]
    pub(crate) fn return_private_page(
        &mut self,
        location: crate::writer_fixed_point::DraftPrivatePageLocation,
    ) -> Result<(), FreeBitmapCowError> {
        self.return_private_page_inner(location)
    }

    fn return_private_page_inner(
        &mut self,
        location: crate::writer_fixed_point::DraftPrivatePageLocation,
    ) -> Result<(), FreeBitmapCowError> {
        let provenance = location.provenance;
        let crate::writer_fixed_point::DraftPageProvenance::Private { work_unit, page } =
            provenance
        else {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        };
        if work_unit != self.work_unit
            || location.nonce != self.nonce
            || location.record_index != self.record_index
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let node = page_index_find_node(self.index_nodes, self.index_root, page.pgno)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if self.index_nodes[node].page != IndexedPage::Arena(page.slot) {
            return Err(FreeBitmapCowError::ArenaPageConflict(page.pgno));
        }
        let binding_index = location.binding_index;
        let binding = self
            .arena_bindings
            .get(binding_index)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if !binding.bound
            || binding.pool_slot != page.slot
            || binding.page_number != page.pgno
            || binding.active_node != node
            || binding.pool_epoch != page.binding_epoch
            || self
                .private_provenance(page.pgno)
                .map_err(FreeBitmapCowError::PrivatePool)?
                != Some(provenance)
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        if !self
            .cleanup_nodes
            .iter()
            .all(|entry| *entry == PrivatePageSelectiveOverlayNode::empty())
            || !self
                .cleanup_path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            || !self
                .cleanup_targets
                .iter()
                .all(|target| *target == usize::MAX)
            || self.cleanup_targets.is_empty()
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(page.pgno));
        }
        self.cleanup
            .refresh_commitment()
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let status = self
            .pool
            .validate_sealed_scope(&self.scope, self.nonce)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if !status.successor_consumed || status.bound == 0 {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let next_binding_epoch =
            page.binding_epoch
                .checked_add(1)
                .ok_or(FreeBitmapCowError::PrivatePool(
                    PrivatePagePoolError::EpochExhausted,
                ))?;
        self.cleanup_targets[0] = page.slot;
        let prepared = {
            let scratch = PrivatePageSelectiveScratch::new(
                &mut *self.cleanup_nodes,
                &mut *self.cleanup_path,
                &mut *self.cleanup_targets,
            );
            (|| {
                let mut deletes = self
                    .pool
                    .prepare_selective_deletes(&self.scope, scratch, 1, 0)
                    .map_err(finalization_error)?;
                self.pool
                    .normalize_selective_deletes(&self.scope, &mut deletes)
                    .map_err(finalization_error)?;
                self.pool
                    .validate_selective_checkpoint_touches(&self.scope, &deletes)
                    .map_err(finalization_error)?;
                self.pool
                    .preflight_selective_sealed_return_epochs(&self.scope, self.nonce, &deletes)
                    .map_err(finalization_error)?;
                let checkpoint_steps = status
                    .capacity
                    .checked_add(3)
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                let checkpoint = self
                    .pool
                    .preflight_checkpoint_steps(checkpoint_steps)
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                Ok::<_, FreeBitmapCowError>((deletes, checkpoint))
            })()
        };
        let (deletes, checkpoint) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.cleanup_nodes
                    .fill(PrivatePageSelectiveOverlayNode::empty());
                self.cleanup_path
                    .fill(PrivatePageSelectivePathEntry::empty());
                self.cleanup_targets.fill(usize::MAX);
                return Err(error);
            }
        };
        if let Err(error) = self.cleanup.validate_commitment() {
            let mut scratch = deletes.into_scratch();
            scratch.clear();
            return Err(FreeBitmapCowError::PrivatePool(error));
        }
        let current_page =
            match self
                .pool
                .sealed_page_provenance(&self.scope, self.nonce, page.slot)
            {
                Ok(current_page) => current_page,
                Err(error) => {
                    let mut scratch = deletes.into_scratch();
                    scratch.clear();
                    return Err(FreeBitmapCowError::PrivatePool(error));
                }
            };
        if current_page != page {
            let mut scratch = deletes.into_scratch();
            scratch.clear();
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let begin = self.pool.begin_checkpoint_prepared(&checkpoint);
        if let Err(error) = begin {
            let mut scratch = deletes.into_scratch();
            scratch.clear();
            return Err(FreeBitmapCowError::PrivatePool(error));
        }
        self.pool.apply_selective_delete_trees_terminal_prepared(
            &checkpoint,
            &self.scope,
            &deletes,
        );
        self.pool.unbind_selective_target_terminal_prepared(
            &checkpoint,
            &self.scope,
            page.slot,
            false,
        );
        self.pool
            .commit_selective_checkpoint_in_scope_terminal_prepared(checkpoint, &self.scope);

        let (root, removed) = page_index_delete(self.index_nodes, self.index_root, page.pgno);
        debug_assert_eq!(removed, node);
        self.index_root = root;
        self.index_nodes[removed] = BitmapCowIndexNode::empty();
        let binding = &mut self.arena_bindings[binding_index];
        binding.pool_epoch = next_binding_epoch;
        binding.page_number = 0;
        binding.active_node = NO_INDEX;
        binding.bound = false;
        self.cleanup.commitment = self.pool.exact_commitment_terminal_prepared(&self.scope);
        let mut scratch = deletes.into_scratch();
        scratch.clear();
        Ok(())
    }

    #[allow(clippy::result_large_err)] // Failure must return the move-only cleanup authority.
    pub(crate) fn cleanup(
        mut self,
    ) -> Result<SealedFreeBitmapCoordinatorScratch<'arena, 'scratch>, (Self, FreeBitmapCowError)>
    {
        let coordinator_cleanup = match self
            .pool
            .begin_sealed_coordinator_cleanup(&self.scope, self.nonce)
        {
            Ok(cleanup) => cleanup,
            Err(error) => return Err((self, FreeBitmapCowError::PrivatePool(error))),
        };
        if let Err(error) = self.cleanup.refresh_commitment() {
            return Err((self, FreeBitmapCowError::PrivatePool(error)));
        }
        let status = match self.pool.validate_sealed_scope(&self.scope, self.nonce) {
            Ok(status) if status.successor_consumed => status,
            _ => {
                return Err((self, FreeBitmapCowError::StaleReservationPredecessor));
            }
        };
        if !self
            .cleanup_nodes
            .iter()
            .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            || !self
                .cleanup_path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            || !self
                .cleanup_targets
                .iter()
                .all(|target| *target == usize::MAX)
        {
            return Err((self, FreeBitmapCowError::ArenaPageConflict(0)));
        }
        let mut target_len = 0usize;
        for (binding_index, binding) in self.arena_bindings.iter().enumerate() {
            if !binding.bound || self.binding_returned(binding_index) {
                continue;
            }
            let Some(target) = self.cleanup_targets.get_mut(target_len) else {
                self.cleanup_targets.fill(usize::MAX);
                return Err((
                    self,
                    FreeBitmapCowError::InsufficientResourceBudget {
                        resource: ReservationResource::AvailableSlots,
                        required: target_len + 1,
                        available: target_len,
                    },
                ));
            };
            *target = binding.pool_slot;
            target_len += 1;
        }
        if target_len != status.bound {
            self.cleanup_targets.fill(usize::MAX);
            return Err((self, FreeBitmapCowError::ArenaPageConflict(0)));
        }
        let prepared = {
            let scratch = PrivatePageSelectiveScratch::new(
                &mut *self.cleanup_nodes,
                &mut *self.cleanup_path,
                &mut *self.cleanup_targets,
            );
            (|| {
                let mut deletes = self
                    .pool
                    .prepare_selective_deletes(&self.scope, scratch, target_len, 0)
                    .map_err(finalization_error)?;
                self.pool
                    .normalize_selective_deletes(&self.scope, &mut deletes)
                    .map_err(finalization_error)?;
                self.pool
                    .validate_selective_checkpoint_touches(&self.scope, &deletes)
                    .map_err(finalization_error)?;
                self.pool
                    .preflight_selective_cleanup_epochs(&self.scope, &deletes)
                    .map_err(finalization_error)?;
                let checkpoint_steps = status
                    .capacity
                    .checked_add(target_len)
                    .and_then(|value| value.checked_add(2))
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                let checkpoint = self
                    .pool
                    .preflight_checkpoint_steps(checkpoint_steps)
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                Ok::<_, FreeBitmapCowError>((deletes, checkpoint))
            })()
        };
        let (deletes, checkpoint) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.cleanup_nodes
                    .fill(PrivatePageSelectiveOverlayNode::empty());
                self.cleanup_path
                    .fill(PrivatePageSelectivePathEntry::empty());
                self.cleanup_targets.fill(usize::MAX);
                return Err((self, error));
            }
        };
        if let Err(error) = self.cleanup.validate_commitment() {
            let mut scratch = deletes.into_scratch();
            scratch.clear();
            return Err((self, FreeBitmapCowError::PrivatePool(error)));
        }
        if let Err(error) = self.pool.begin_checkpoint_prepared(&checkpoint) {
            let mut scratch = deletes.into_scratch();
            scratch.clear();
            return Err((self, FreeBitmapCowError::PrivatePool(error)));
        }
        self.pool.apply_selective_delete_trees_terminal_prepared(
            &checkpoint,
            &self.scope,
            &deletes,
        );
        for index in 0..deletes.target_len() {
            self.pool.unbind_selective_target_terminal_prepared(
                &checkpoint,
                &self.scope,
                deletes.target(index),
                false,
            );
        }
        self.pool
            .commit_selective_checkpoint_in_scope_terminal_prepared(checkpoint, &self.scope);
        self.pool
            .close_sealed_scope_terminal_prepared(&self.scope, self.nonce);
        if let Some(cleanup) = coordinator_cleanup {
            if let Err(error) = cleanup.finish() {
                return Err((self, FreeBitmapCowError::PrivatePool(error)));
            }
        }
        let Self {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
            ..
        } = self;
        arena_bindings.fill(BitmapCowArenaBinding::empty());
        replacements.fill(0);
        index_nodes.fill(BitmapCowIndexNode::empty());
        cleanup_nodes.fill(PrivatePageSelectiveOverlayNode::empty());
        cleanup_path.fill(PrivatePageSelectivePathEntry::empty());
        cleanup_targets.fill(NO_INDEX);
        for returned in returned {
            returned.set(false);
        }
        Ok(SealedFreeBitmapCoordinatorScratch {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
        })
    }
}

impl<'scratch, 'arena, 'pool, 'slots>
    SealedFreeBitmapCoordinatorRecord<'scratch, 'arena, 'pool, 'slots>
{
    #[allow(clippy::too_many_arguments, clippy::result_large_err)]
    pub(crate) fn from_coordinator_terminal(
        pool: &'pool PrivatePagePool<'slots>,
        scope: PrivatePageReservationScope<'slots>,
        nonce: u64,
        work_unit: u64,
        record_index: usize,
        root: u32,
        pending_page_count: u64,
        pages: &[PrivatePageCoordinatorTerminalPage],
        scratch: SealedFreeBitmapCoordinatorScratch<'arena, 'scratch>,
    ) -> Result<Self, FreeBitmapCowError> {
        let terminal_pages = pages.len();
        if nonce == 0
            || work_unit == 0
            || terminal_pages == 0
            || !scratch.is_canonical_for(terminal_pages)
            || (root != 0
                && !pages
                    .iter()
                    .any(|page| page.pgno == root && page.owner == PrivatePageOwner::Bitmap))
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let status = pool
            .validate_sealed_scope(&scope, nonce)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if !status.successor_consumed || status.bound != pages.len() {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let mut index_root = NO_INDEX;
        for (index, page) in pages.iter().enumerate() {
            let provenance = pool
                .sealed_page_provenance(&scope, nonce, page.pool_slot)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            if provenance.pgno != page.pgno
                || provenance.owner != page.owner
                || provenance.owner_generation != page.owner_generation
                || provenance.tag != page.tag
            {
                return Err(FreeBitmapCowError::StaleReservationPredecessor);
            }
            scratch.arena_bindings[index] = BitmapCowArenaBinding {
                pool_slot: page.pool_slot,
                pool_epoch: provenance.binding_epoch,
                page_number: page.pgno,
                storage_node: index,
                active_node: index,
                bound: true,
            };
            page_index_insert_existing_prechecked(
                scratch.index_nodes,
                &mut index_root,
                index,
                page.pgno,
                IndexedPage::Arena(page.pool_slot),
            );
        }
        let commitment = pool
            .exact_commitment(&scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        Ok(Self {
            record_index,
            work_unit,
            pool,
            scope: scope.share(),
            nonce,
            cleanup: FreeBitmapFinalizationPredecessor {
                pool,
                scope,
                nonce,
                commitment,
            },
            root,
            pending_page_count,
            arena_bindings: scratch.arena_bindings,
            replacements: scratch.replacements,
            replacement_len: 0,
            index_nodes: scratch.index_nodes,
            index_root,
            returned: scratch.returned,
            cleanup_nodes: scratch.cleanup_nodes,
            cleanup_path: scratch.cleanup_path,
            cleanup_targets: scratch.cleanup_targets,
        })
    }
}

impl<'arena, 'cleanup> PreparedFreeBitmapCoordinatorRecord<'arena, 'cleanup> {
    fn binding_returned(&self, binding_index: usize) -> bool {
        self.returned.get(binding_index).is_some_and(Cell::get)
    }

    pub(crate) fn returned_cell(&self, binding_index: usize) -> Option<&'arena Cell<bool>> {
        self.returned.get(binding_index)
    }

    fn private_provenance(
        &self,
        pool: &PrivatePagePool<'_>,
        pgno: u32,
    ) -> Result<Option<crate::writer_fixed_point::DraftPageProvenance>, PrivatePagePoolError> {
        let Some(IndexedPage::Arena(slot)) =
            page_index_find(self.index_nodes, self.index_root, pgno)
        else {
            return Ok(None);
        };
        let node = page_index_find_node(self.index_nodes, self.index_root, pgno)
            .ok_or(PrivatePagePoolError::InvalidState(slot))?;
        if self.binding_returned(node) {
            return Ok(None);
        }
        let scope = self.scope.materialize(pool);
        let page = pool.sealed_page_provenance(&scope, self.nonce, slot)?;
        Ok(Some(
            crate::writer_fixed_point::DraftPageProvenance::Private {
                work_unit: self.work_unit,
                page,
            },
        ))
    }

    pub(crate) fn validate_private_return(
        &self,
        pool: &PrivatePagePool<'_>,
        location: crate::writer_fixed_point::DraftPrivatePageLocation,
    ) -> Result<PrivatePageCoordinatorPriorReturn, FreeBitmapCowError> {
        let provenance = location.provenance;
        let crate::writer_fixed_point::DraftPageProvenance::Private { work_unit, page } =
            provenance
        else {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        };
        if work_unit != self.work_unit
            || location.nonce != self.nonce
            || location.record_index != self.record_index
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let node = page_index_find_node(self.index_nodes, self.index_root, page.pgno)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if self.index_nodes[node].page != IndexedPage::Arena(page.slot) {
            return Err(FreeBitmapCowError::ArenaPageConflict(page.pgno));
        }
        let binding = self
            .arena_bindings
            .get(location.binding_index)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(page.pgno))?;
        if !binding.bound
            || self.binding_returned(location.binding_index)
            || binding.pool_slot != page.slot
            || binding.page_number != page.pgno
            || binding.active_node != node
            || binding.pool_epoch != page.binding_epoch
            || self
                .private_provenance(pool, page.pgno)
                .map_err(FreeBitmapCowError::PrivatePool)?
                != Some(provenance)
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        Ok(PrivatePageCoordinatorPriorReturn {
            page,
            nonce: location.nonce,
        })
    }

    /// Confirms that the immutable record image matches the sealed scope
    /// aggregate that the coordinator is about to retain. This is the last
    /// fallible check before the coordinator releases active-work registration.
    pub(crate) fn validate_sealed_handoff(
        &self,
        pool: &PrivatePagePool<'_>,
        scope: &PrivatePageReservationScope<'_>,
        work_unit: u64,
        record_index: usize,
        root: u32,
        pending_page_count: u64,
    ) -> Result<(), FreeBitmapCowError> {
        if self.record_index != record_index
            || self.work_unit != work_unit
            || self.root != root
            || self.pending_page_count != pending_page_count
            || self.scope != scope.seed()
        {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        let status = pool
            .validate_sealed_scope(scope, self.nonce)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        if !status.successor_consumed {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        pool.validate_coordinator_scope_commitment(scope, &self.commitment)
            .map_err(FreeBitmapCowError::PrivatePool)?;

        let mut terminal_bindings = 0usize;
        let mut root_found = root == 0;
        for binding in self.arena_bindings.iter() {
            if !binding.bound {
                continue;
            }
            let page = pool
                .sealed_page_provenance(scope, self.nonce, binding.pool_slot)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            if page.binding_epoch != binding.pool_epoch || page.pgno != binding.page_number {
                return Err(FreeBitmapCowError::StaleReservationPredecessor);
            }
            root_found |= page.pgno == root && page.owner == PrivatePageOwner::Bitmap;
            terminal_bindings = terminal_bindings
                .checked_add(1)
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
        }
        if !root_found || terminal_bindings != status.bound {
            return Err(FreeBitmapCowError::StaleReservationPredecessor);
        }
        Ok(())
    }

    /// Streams only pages still owned by this sealed record. The pool retains
    /// the bytes until a later successful cleanup consumes the record.
    pub(crate) fn write_private_pages<E>(
        &self,
        pool: &PrivatePagePool<'_>,
        write: &mut impl FnMut(u32, &[u8]) -> Result<(), E>,
    ) -> Result<usize, FreeBitmapCoordinatorOutputError<E>> {
        let scope = self.scope.materialize(pool);
        let status = pool
            .validate_sealed_scope(&scope, self.nonce)
            .map_err(|error| {
                FreeBitmapCoordinatorOutputError::Record(FreeBitmapCowError::PrivatePool(error))
            })?;
        if !status.successor_consumed {
            return Err(FreeBitmapCoordinatorOutputError::Record(
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        let mut written = 0usize;
        for (binding_index, binding) in self.arena_bindings.iter().enumerate() {
            if !binding.bound || self.binding_returned(binding_index) {
                continue;
            }
            let page = pool
                .sealed_page_provenance(&scope, self.nonce, binding.pool_slot)
                .map_err(|error| {
                    FreeBitmapCoordinatorOutputError::Record(FreeBitmapCowError::PrivatePool(error))
                })?;
            if page.binding_epoch != binding.pool_epoch || page.pgno != binding.page_number {
                return Err(FreeBitmapCoordinatorOutputError::Record(
                    FreeBitmapCowError::StaleReservationPredecessor,
                ));
            }
            let bytes = pool
                .borrow_sealed_page(&scope, self.nonce, binding.pool_slot)
                .map_err(|error| {
                    FreeBitmapCoordinatorOutputError::Record(FreeBitmapCowError::PrivatePool(error))
                })?;
            write(page.pgno, &bytes[..]).map_err(FreeBitmapCoordinatorOutputError::Sink)?;
            written = written
                .checked_add(1)
                .ok_or(FreeBitmapCoordinatorOutputError::Record(
                    FreeBitmapCowError::CoverageOverflow,
                ))?;
        }
        if written != status.bound {
            return Err(FreeBitmapCoordinatorOutputError::Record(
                FreeBitmapCowError::ArenaPageConflict(0),
            ));
        }
        Ok(written)
    }

    #[allow(clippy::too_many_arguments, clippy::result_large_err)]
    pub(crate) fn prepare_from_simulated_terminal(
        replay: &PrivatePagePreparedSparseReplay<'_, '_, '_>,
        nonce: u64,
        work_unit: u64,
        record_index: usize,
        root: u32,
        pending_page_count: u64,
        pages: &[PrivatePageCoordinatorTerminalPage],
        mut scratch: SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>,
    ) -> Result<
        Self,
        (
            SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup>,
            FreeBitmapCowError,
        ),
    > {
        let terminal_pages = pages.len();
        if nonce == 0
            || work_unit == 0
            || pages.is_empty()
            || !scratch.is_canonical_for(terminal_pages)
            || (root != 0
                && !pages
                    .iter()
                    .any(|page| page.pgno == root && page.owner == PrivatePageOwner::Bitmap))
        {
            scratch.clear();
            return Err((scratch, FreeBitmapCowError::StaleReservationPredecessor));
        }
        let mut index_root = NO_INDEX;
        for (binding_index, page) in pages.iter().enumerate() {
            let provenance = match replay.future_sealed_page_provenance(page.pool_slot) {
                Ok(provenance) => provenance,
                Err(error) => {
                    scratch.clear();
                    return Err((scratch, FreeBitmapCowError::PrivatePool(error)));
                }
            };
            if provenance.pgno != page.pgno
                || provenance.owner != page.owner
                || provenance.owner_generation != page.owner_generation
                || provenance.tag != page.tag
            {
                scratch.clear();
                return Err((scratch, FreeBitmapCowError::StaleReservationPredecessor));
            }
            scratch.arena_bindings[binding_index] = BitmapCowArenaBinding {
                pool_slot: page.pool_slot,
                pool_epoch: provenance.binding_epoch,
                page_number: page.pgno,
                storage_node: binding_index,
                active_node: binding_index,
                bound: true,
            };
            page_index_insert_existing_prechecked(
                scratch.index_nodes,
                &mut index_root,
                binding_index,
                page.pgno,
                IndexedPage::Arena(page.pool_slot),
            );
        }
        let commitment = match replay.future_commitment() {
            Ok(commitment) => commitment,
            Err(error) => {
                scratch.clear();
                return Err((scratch, FreeBitmapCowError::PrivatePool(error)));
            }
        };
        Ok(Self {
            record_index,
            work_unit,
            scope: replay.scope_seed(),
            nonce,
            commitment,
            root,
            pending_page_count,
            arena_bindings: scratch.arena_bindings,
            replacements: scratch.replacements,
            index_nodes: scratch.index_nodes,
            index_root,
            returned: scratch.returned,
            cleanup_nodes: scratch.cleanup_nodes,
            cleanup_path: scratch.cleanup_path,
            cleanup_targets: scratch.cleanup_targets,
        })
    }

    pub(crate) fn cancel(self) {
        self.arena_bindings.fill(BitmapCowArenaBinding::empty());
        self.replacements.fill(0);
        self.index_nodes.fill(BitmapCowIndexNode::empty());
        for returned in self.returned.iter() {
            returned.set(false);
        }
        self.cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        self.cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        self.cleanup_targets.fill(NO_INDEX);
    }

    pub(crate) fn cancel_into_scratch(
        self,
    ) -> SealedFreeBitmapCoordinatorScratch<'arena, 'cleanup> {
        let Self {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
            ..
        } = self;
        arena_bindings.fill(BitmapCowArenaBinding::empty());
        replacements.fill(0);
        index_nodes.fill(BitmapCowIndexNode::empty());
        for returned in returned {
            returned.set(false);
        }
        cleanup_nodes.fill(PrivatePageSelectiveOverlayNode::empty());
        cleanup_path.fill(PrivatePageSelectivePathEntry::empty());
        cleanup_targets.fill(NO_INDEX);
        SealedFreeBitmapCoordinatorScratch {
            arena_bindings,
            replacements,
            index_nodes,
            returned,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
        }
    }

    pub(crate) fn materialize<'pool, 'slots>(
        self,
        pool: &'pool PrivatePagePool<'slots>,
    ) -> SealedFreeBitmapCoordinatorRecord<'cleanup, 'arena, 'pool, 'slots> {
        let scope = self.scope.materialize(pool);
        SealedFreeBitmapCoordinatorRecord {
            record_index: self.record_index,
            work_unit: self.work_unit,
            pool,
            scope: scope.share(),
            nonce: self.nonce,
            cleanup: FreeBitmapFinalizationPredecessor {
                pool,
                scope,
                nonce: self.nonce,
                commitment: self.commitment,
            },
            root: self.root,
            pending_page_count: self.pending_page_count,
            arena_bindings: self.arena_bindings,
            replacements: self.replacements,
            replacement_len: 0,
            index_nodes: self.index_nodes,
            index_root: self.index_root,
            returned: self.returned,
            cleanup_nodes: self.cleanup_nodes,
            cleanup_path: self.cleanup_path,
            cleanup_targets: self.cleanup_targets,
        }
    }
}

struct PreparedFreeBitmapFinalization<'scratch, 'slots> {
    scope: PrivatePageReservationScope<'slots>,
    nonce: u64,
    bitmap_terminal_page_count: usize,
    range_terminal_page_count: usize,
    retirement_terminal_page_count: usize,
    terminal_page_count: usize,
    cleanup: PrivatePageSelectiveScratch<'scratch>,
    released: UnusedReservationRelease,
    reclaimed: usize,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct FinalizedBindingPartition {
    retained: usize,
    released: usize,
}

fn stable_partition_finalized_bindings(
    length: usize,
    mut retained: impl FnMut(usize) -> Result<bool, FreeBitmapCowError>,
    retained_indices: &mut [usize],
    released_indices: &mut [usize],
) -> Result<FinalizedBindingPartition, FreeBitmapCowError> {
    if retained_indices.len() < length || released_indices.len() < length {
        return Err(FreeBitmapCowError::InsufficientResourceBudget {
            resource: ReservationResource::AvailableSlots,
            required: length,
            available: retained_indices.len().min(released_indices.len()),
        });
    }
    retained_indices.fill(usize::MAX);
    released_indices.fill(usize::MAX);
    let mut partition = FinalizedBindingPartition::default();
    for index in 0..length {
        if retained(index)? {
            retained_indices[partition.retained] = index;
            partition.retained += 1;
        } else {
            released_indices[partition.released] = index;
            partition.released += 1;
        }
    }
    Ok(partition)
}

fn compact_finalized_index(
    nodes: &mut [BitmapCowIndexNode],
    root: &mut usize,
    length: &mut usize,
    arena_slot_map: &[usize],
    remap: &mut [usize],
) -> Result<(), FreeBitmapCowError> {
    if remap.len() < nodes.len() {
        return Err(FreeBitmapCowError::InsufficientResourceBudget {
            resource: ReservationResource::IndexNodes,
            required: nodes.len(),
            available: remap.len(),
        });
    }
    remap.fill(usize::MAX);
    let empty = BitmapCowIndexNode::empty();
    let mut compacted = 0usize;
    for (index, node) in nodes.iter().enumerate() {
        if *node != empty {
            remap[index] = compacted;
            compacted += 1;
        }
    }
    let old_root = *root;
    for source in 0..nodes.len() {
        let destination = remap[source];
        if destination == usize::MAX {
            continue;
        }
        let mut node = nodes[source];
        for child in [&mut node.left, &mut node.right] {
            if *child != NO_INDEX {
                *child = *remap
                    .get(*child)
                    .filter(|&&mapped| mapped != usize::MAX)
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(node.pgno))?;
            }
        }
        if let IndexedPage::Arena(slot) = node.page {
            node.page = IndexedPage::Arena(
                *arena_slot_map
                    .get(slot)
                    .filter(|&&mapped| mapped != usize::MAX)
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(node.pgno))?,
            );
        }
        nodes[destination] = node;
    }
    nodes[compacted..].fill(empty);
    *root = if old_root == NO_INDEX {
        NO_INDEX
    } else {
        *remap
            .get(old_root)
            .filter(|&&mapped| mapped != usize::MAX)
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?
    };
    *length = compacted;
    Ok(())
}

struct FinalizationCachedSource<'cache, S: CommittedPageSource + ?Sized> {
    base: &'cache S,
    pages: RefCell<&'cache mut [FreeBitmapFinalizationCachedPage]>,
    length: Cell<usize>,
    replay: bool,
}

impl<S: CommittedPageSource + ?Sized> CommittedPageSource for FinalizationCachedSource<'_, S> {
    fn read_page(&self, pgno: u32, dst: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
        let mut pages = self
            .pages
            .try_borrow_mut()
            .map_err(|_| PageSourceError::ForkedHandle)?;
        if let Some(page) = pages[..self.length.get()]
            .iter()
            .find(|page| page.occupied && page.pgno == pgno)
        {
            *dst = page.bytes;
            return Ok(());
        }
        if self.replay {
            return Err(PageSourceError::PageOutOfBounds(pgno));
        }
        if self.length.get() == pages.len() {
            return Err(PageSourceError::ForkedHandle);
        }
        self.base.read_page(pgno, dst)?;
        let index = self.length.get();
        pages[index] = FreeBitmapFinalizationCachedPage {
            pgno,
            bytes: *dst,
            occupied: true,
        };
        self.length.set(index + 1);
        Ok(())
    }

    fn check_access(&self) -> Result<(), PageSourceError> {
        if self.replay {
            Ok(())
        } else {
            self.base.check_access()
        }
    }
}

fn finalization_error(error: PrivatePageSelectiveError) -> FreeBitmapCowError {
    match error {
        PrivatePageSelectiveError::Pool(error) => FreeBitmapCowError::PrivatePool(error),
        PrivatePageSelectiveError::Corrupt(pgno) => FreeBitmapCowError::ArenaPageConflict(pgno),
        PrivatePageSelectiveError::Scratch { required, actual } => {
            FreeBitmapCowError::InsufficientResourceBudget {
                resource: ReservationResource::IndexNodes,
                required,
                available: actual,
            }
        }
        PrivatePageSelectiveError::Overflow => FreeBitmapCowError::CoverageOverflow,
    }
}

fn successor_consumption_error(error: PrivatePagePoolError) -> FreeBitmapCowError {
    match error {
        PrivatePagePoolError::PoolMismatch | PrivatePagePoolError::StaleScope => {
            FreeBitmapCowError::StaleReservationPredecessor
        }
        error => FreeBitmapCowError::PrivatePool(error),
    }
}

fn mint_nonce() -> Result<u64, FreeBitmapCowError> {
    let mut current = FINALIZATION_NONCE.load(Ordering::Relaxed);
    loop {
        let next = current
            .checked_add(1)
            .ok_or(FreeBitmapCowError::MutationEpochExhausted)?;
        match FINALIZATION_NONCE.compare_exchange_weak(
            current,
            next,
            Ordering::AcqRel,
            Ordering::Relaxed,
        ) {
            Ok(_) => return Ok(next),
            Err(actual) => current = actual,
        }
    }
}

fn promote_finalization_empty_root<S: CommittedPageSource + ?Sized>(
    shadow: &mut FreeBitmapCow<'_, '_, '_, S>,
    pgno: u32,
) -> Result<(), FreeBitmapCowError> {
    if shadow.root != 0 {
        return Err(FreeBitmapCowError::StaleInsertionPlan);
    }
    let Some(IndexedPage::Arena(slot)) = shadow.indexed_page(pgno) else {
        return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
    };
    let info = shadow.scoped_slot_info(slot)?;
    if info.state != PrivatePagePoolState::Available || info.pgno != pgno {
        return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
    }
    let mut bytes = [0u8; PAGE_SIZE];
    write_bitmap_header(
        &mut bytes,
        PageType::BitmapLeaf,
        shadow.pending_txn,
        0,
        0,
        LEAF_LOWER,
    );
    let scope = shadow
        .scoped()
        .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?
        .share();
    let checkpoint = shadow
        .pool()
        .begin_checkpoint()
        .map_err(FreeBitmapCowError::PrivatePool)?;
    let authority = match shadow.pool().claim_page_in_scope(
        &checkpoint,
        &scope,
        pgno,
        PrivatePageOwner::Bitmap,
        shadow.pending_txn,
        0,
    ) {
        Ok(authority) => authority,
        Err(error) => {
            return match shadow.pool().rollback_checkpoint(checkpoint) {
                Ok(()) => Err(FreeBitmapCowError::PrivatePool(error)),
                Err((_checkpoint, rollback_error)) => {
                    Err(FreeBitmapCowError::PrivatePool(rollback_error))
                }
            };
        }
    };
    let write_result = match shadow.pool().borrow_page_mut_in_scope(&scope, &authority) {
        Ok(mut page) => {
            page.copy_from_slice(&bytes);
            Ok(())
        }
        Err(error) => Err(error),
    };
    if let Err(error) = write_result {
        return match shadow.pool().rollback_checkpoint(checkpoint) {
            Ok(()) => Err(FreeBitmapCowError::PrivatePool(error)),
            Err((_checkpoint, rollback_error)) => {
                Err(FreeBitmapCowError::PrivatePool(rollback_error))
            }
        };
    }
    shadow
        .pool()
        .commit_checkpoint(checkpoint)
        .map_err(|(_checkpoint, error)| FreeBitmapCowError::PrivatePool(error))?;
    let binding_epoch = shadow
        .pool()
        .scoped_slot_info(&scope, slot)
        .map_err(FreeBitmapCowError::PrivatePool)?
        .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?
        .binding_epoch;
    shadow.refresh_arena_binding_epoch(slot, binding_epoch)?;
    shadow.rebuild_available_slots();
    shadow.root = pgno;
    Ok(())
}

#[allow(clippy::drop_non_drop)] // Releases the plan's borrow before root promotion.
fn run_fixed_point<S: CommittedPageSource + ?Sized>(
    shadow: &mut FreeBitmapCow<'_, '_, '_, S>,
    release_pages: &mut [u32],
    insert_pages: &mut [FreeBitmapInsertPage],
    index_stack: &mut [usize],
) -> Result<(UnusedReservationRelease, usize), FreeBitmapCowError> {
    let limit = shadow
        .scope_capacity
        .checked_mul(2)
        .and_then(|value| value.checked_add(FREE_PATH_CAPACITY + 1))
        .ok_or(FreeBitmapCowError::CoverageOverflow)?;
    let mut result = UnusedReservationRelease::default();
    let mut reclaimed = 0usize;
    for _ in 0..limit {
        let old_tail = shadow.pending_page_count;
        let mut new_tail = old_tail;
        while new_tail > shadow.committed_page_count {
            let pgno =
                u32::try_from(new_tail - 1).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
            let Some(IndexedPage::Arena(slot)) = shadow.indexed_page(pgno) else {
                break;
            };
            let info = shadow.scoped_slot_info(slot)?;
            if info.authorization != Some(PrivatePageAuthorization::Appended)
                || info.state != PrivatePagePoolState::Available
            {
                break;
            }
            new_tail -= 1;
        }
        let mut release_len = 0usize;
        let mut top = 0usize;
        let mut visited = 0usize;
        let mut node = shadow.index_root;
        let mut previous = None;
        while node != NO_INDEX || top != 0 {
            while node != NO_INDEX {
                if node >= shadow.index_nodes.len() {
                    return Err(FreeBitmapCowError::ArenaPageConflict(0));
                }
                if top == index_stack.len() {
                    return Err(FreeBitmapCowError::InsufficientResourceBudget {
                        resource: ReservationResource::IndexNodes,
                        required: top + 1,
                        available: index_stack.len(),
                    });
                }
                index_stack[top] = node;
                top += 1;
                node = shadow.index_nodes[node].left;
            }
            top -= 1;
            node = index_stack[top];
            visited += 1;
            if visited > shadow.index_nodes.len() {
                return Err(FreeBitmapCowError::ArenaPageConflict(0));
            }
            let indexed = shadow.index_nodes[node];
            if previous.is_some_and(|prior| indexed.pgno <= prior) {
                return Err(FreeBitmapCowError::ArenaPageConflict(indexed.pgno));
            }
            previous = Some(indexed.pgno);
            if let IndexedPage::Arena(slot) = indexed.page {
                let info = shadow.scoped_slot_info(slot)?;
                if info.state == PrivatePagePoolState::Available && u64::from(info.pgno) < new_tail
                {
                    if release_len == release_pages.len() {
                        return Err(FreeBitmapCowError::InsufficientResourceBudget {
                            resource: ReservationResource::CandidatePages,
                            required: release_len + 1,
                            available: release_pages.len(),
                        });
                    }
                    release_pages[release_len] = info.pgno;
                    release_len += 1;
                }
            }
            node = indexed.right;
        }
        index_stack.fill(usize::MAX);
        if release_len == 0 && new_tail == old_tail {
            for binding in &mut shadow.arena_bindings[..shadow.scope_capacity] {
                if !binding.bound {
                    continue;
                }
                let active = binding.active_node;
                if !shadow.index_nodes[active].candidate_mapped {
                    continue;
                }
                let pgno = binding.page_number;
                let (root, removed) =
                    page_index_delete(shadow.index_nodes, shadow.index_root, pgno);
                if removed != active || binding.storage_node >= shadow.index_nodes.len() {
                    return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                }
                shadow.index_root = root;
                shadow.index_nodes[removed] = BitmapCowIndexNode::empty();
                page_index_insert_existing_prechecked(
                    shadow.index_nodes,
                    &mut shadow.index_root,
                    binding.storage_node,
                    pgno,
                    IndexedPage::Arena(binding.pool_slot),
                );
                binding.active_node = binding.storage_node;
            }
            shadow.candidate_len = 0;
            shadow.planned_candidate_len = 0;
            shadow.selected_candidate_len = 0;
            shadow.candidate_selection_set = false;
            shadow.reservation_planned = false;
            shadow.payload_page_budget = 0;
            shadow.planned_required_private_pages = 0;
            shadow.available_len = 0;
            shadow.available_slots.fill(0);
            shadow.validate_scoped_bindings()?;
            result.pending_page_count = shadow.pending_page_count;
            return Ok((result, reclaimed));
        }
        for page in insert_pages.iter_mut() {
            *page = FreeBitmapInsertPage::empty();
        }
        let mut start = 0usize;
        let mut length = release_len;
        let root_was_empty = shadow.root == 0;
        let (plan, committed, reclaimed_now, appended) = loop {
            let mut committed = 0usize;
            let mut reclaimed_now = 0usize;
            let mut appended = 0usize;
            for &pgno in &release_pages[start..start + length] {
                let Some(IndexedPage::Arena(slot)) = shadow.indexed_page(pgno) else {
                    return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                };
                match shadow.scoped_slot_info(slot)?.authorization {
                    Some(PrivatePageAuthorization::CommittedFree) => committed += 1,
                    Some(PrivatePageAuthorization::SafelyReclaimed) => reclaimed_now += 1,
                    Some(PrivatePageAuthorization::Appended) => appended += 1,
                    None => return Err(FreeBitmapCowError::ArenaPageConflict(pgno)),
                }
            }
            match shadow.plan_insert_free_for_page_count(
                &release_pages[start..start + length],
                new_tail,
                insert_pages,
            ) {
                Ok(plan) => break (plan, committed, reclaimed_now, appended),
                Err(FreeBitmapCowError::InsufficientResourceBudget {
                    resource: ReservationResource::ArenaPages,
                    required,
                    available,
                }) if length != 0 => {
                    let deficit = required.saturating_sub(available).max(1).min(length);
                    start += deficit;
                    length -= deficit;
                    for page in insert_pages.iter_mut() {
                        *page = FreeBitmapInsertPage::empty();
                    }
                }
                Err(error) => return Err(error),
            }
        };
        if length == 0 && release_len != 0 && root_was_empty {
            drop(plan);
            promote_finalization_empty_root(shadow, release_pages[0])?;
            continue;
        }
        let applied = apply_shadow_insert(plan)?;
        if new_tail != old_tail {
            for page in (new_tail..old_tail).rev() {
                let pgno = u32::try_from(page).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                let Some(IndexedPage::Arena(slot)) = shadow.indexed_page(pgno) else {
                    return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                };
                let binding_index = shadow
                    .arena_binding_index(slot)
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
                let (root, removed) =
                    page_index_delete(shadow.index_nodes, shadow.index_root, pgno);
                if removed != shadow.arena_bindings[binding_index].active_node {
                    return Err(FreeBitmapCowError::ArenaPageConflict(pgno));
                }
                shadow.index_root = root;
                shadow.index_nodes[removed] = BitmapCowIndexNode::empty();
            }
            let checkpoint = shadow
                .pool()
                .begin_checkpoint()
                .map_err(FreeBitmapCowError::PrivatePool)?;
            for page in (new_tail..old_tail).rev() {
                if let Err(error) = shadow.pool().unbind_page(
                    &checkpoint,
                    shadow.scoped().expect("shadow is scoped"),
                    u32::try_from(page).map_err(|_| FreeBitmapCowError::CoverageOverflow)?,
                ) {
                    return match shadow.pool().rollback_checkpoint(checkpoint) {
                        Ok(()) => Err(FreeBitmapCowError::PrivatePool(error)),
                        Err((_checkpoint, rollback_error)) => {
                            Err(FreeBitmapCowError::PrivatePool(rollback_error))
                        }
                    };
                }
            }
            shadow
                .pool()
                .commit_checkpoint(checkpoint)
                .map_err(|(_checkpoint, error)| FreeBitmapCowError::PrivatePool(error))?;
            for page in (new_tail..old_tail).rev() {
                let pgno = u32::try_from(page).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                let binding_index = shadow
                    .arena_bindings
                    .iter()
                    .position(|binding| binding.bound && binding.page_number == pgno)
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
                let slot = shadow.arena_bindings[binding_index].pool_slot;
                let scope = shadow.scoped().expect("shadow is scoped");
                let info = shadow
                    .pool()
                    .scoped_slot_info(scope, slot)
                    .map_err(FreeBitmapCowError::PrivatePool)?
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
                shadow.arena_bindings[binding_index].pool_epoch = info.binding_epoch;
                shadow.arena_bindings[binding_index].page_number = 0;
                shadow.arena_bindings[binding_index].active_node = NO_INDEX;
                shadow.arena_bindings[binding_index].bound = false;
            }
        }
        result.reinserted_candidates += committed + applied.recycled_private_pages;
        result.reinserted_appended += appended;
        result.truncated_appended += usize::try_from(old_tail - new_tail)
            .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
        reclaimed += reclaimed_now;
    }
    Err(FreeBitmapCowError::StaleInsertionPlan)
}

fn cache_fingerprint(pages: &[FreeBitmapFinalizationCachedPage], length: usize) -> u64 {
    let mut hash = 1469598103934665603u64;
    for page in &pages[..length] {
        hash = reservation_hash_u64(hash, u64::from(page.pgno));
        for chunk in page.bytes.chunks(8) {
            let mut word = [0u8; 8];
            word[..chunk.len()].copy_from_slice(chunk);
            hash = reservation_hash_u64(hash, u64::from_le_bytes(word));
        }
    }
    hash
}

fn apply_shadow_insert<S: CommittedPageSource + ?Sized>(
    plan: PlannedFreeBitmapInsertion<'_, '_, '_, '_, '_, S>,
) -> Result<FreeBitmapInsertResult, FreeBitmapCowError> {
    let cow = plan.cow;
    let prepared = plan.prepared;
    let scope = cow
        .scoped()
        .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?
        .share();
    for &slot in &prepared.demoted_slots[..prepared.demoted_len] {
        let epoch = cow
            .pool()
            .shadow_return(
                &scope,
                slot,
                PrivatePageOwner::Bitmap,
                cow.pending_txn,
                PrivatePageReturn::Available,
            )
            .map_err(FreeBitmapCowError::PrivatePool)?;
        cow.refresh_arena_binding_epoch(slot, epoch)?;
    }
    for node in &prepared.scratch[..prepared.scratch_len] {
        if !node.changed {
            continue;
        }
        match node.origin {
            InsertPageOrigin::Private(slot) => cow
                .pool()
                .shadow_write(
                    &scope,
                    slot,
                    PrivatePageOwner::Bitmap,
                    cow.pending_txn,
                    &node.bytes,
                )
                .map_err(FreeBitmapCowError::PrivatePool)?,
            InsertPageOrigin::Committed | InsertPageOrigin::Verified(_) => {
                let slot = node.destination_slot;
                let epoch = cow
                    .pool()
                    .shadow_claim_and_write(
                        &scope,
                        slot,
                        PrivatePageOwner::Bitmap,
                        cow.pending_txn,
                        u64::from(node.source_pgno),
                        &node.bytes,
                    )
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                cow.refresh_arena_binding_epoch(slot, epoch)?;
                cow.replacements[cow.replacement_len] = node.source_pgno;
                cow.replacement_len += 1;
                match node.origin {
                    InsertPageOrigin::Committed => page_index_insert_prechecked(
                        cow.index_nodes,
                        &mut cow.index_root,
                        &mut cow.index_len,
                        node.source_pgno,
                        IndexedPage::Replacement,
                    ),
                    InsertPageOrigin::Verified(verified) => {
                        if page_index_replace(
                            cow.index_nodes,
                            cow.index_root,
                            node.source_pgno,
                            IndexedPage::Replacement,
                        ) != Some(IndexedPage::Verified(verified))
                        {
                            return Err(FreeBitmapCowError::ArenaPageConflict(node.source_pgno));
                        }
                    }
                    _ => unreachable!(),
                }
            }
            InsertPageOrigin::New => {
                let slot = node.destination_slot;
                let epoch = cow
                    .pool()
                    .shadow_claim_and_write(
                        &scope,
                        slot,
                        PrivatePageOwner::Bitmap,
                        cow.pending_txn,
                        0,
                        &node.bytes,
                    )
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                cow.refresh_arena_binding_epoch(slot, epoch)?;
            }
            InsertPageOrigin::None => unreachable!(),
        }
    }
    for &pgno in prepared
        .pages
        .iter()
        .chain(prepared.auto_release_pages[..prepared.auto_release_len].iter())
    {
        if let Some(IndexedPage::Arena(slot)) = cow.indexed_page(pgno) {
            if cow.slot_state(slot) == BitmapPrivatePageState::Available {
                let claimed_epoch = cow
                    .pool()
                    .shadow_claim_and_write(
                        &scope,
                        slot,
                        PrivatePageOwner::Bitmap,
                        cow.pending_txn,
                        0,
                        &[0; PAGE_SIZE],
                    )
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                cow.refresh_arena_binding_epoch(slot, claimed_epoch)?;
                let returned_epoch = cow
                    .pool()
                    .shadow_return(
                        &scope,
                        slot,
                        PrivatePageOwner::Bitmap,
                        cow.pending_txn,
                        PrivatePageReturn::Free,
                    )
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                cow.refresh_arena_binding_epoch(slot, returned_epoch)?;
            }
        }
    }
    cow.root = prepared.root;
    cow.pending_page_count = prepared.governing_page_count;
    cow.rebuild_available_slots();
    Ok(FreeBitmapInsertResult {
        inserted: prepared.inserted,
        already_free: prepared.already_free,
        committed_replacements: prepared.committed_replacements,
        new_bitmap_pages: prepared.new_bitmap_pages,
        recycled_private_pages: prepared.demoted_len,
    })
}

impl<'a, 'slots, 'scope, 'barrier, 'pages, S: CommittedPageSource + ?Sized>
    BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, 'pages, S>
{
    fn projected_tail(&self) -> Result<u64, FreeBitmapCowError> {
        let mut tail = self.cow.pool().pending_page_count();
        while tail > self.cow.committed_page_count {
            let pgno = u32::try_from(tail - 1).map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
            let Some(IndexedPage::Arena(slot)) = self.cow.indexed_page(pgno) else {
                break;
            };
            let info = self.cow.scoped_slot_info(slot)?;
            if info.authorization != Some(PrivatePageAuthorization::Appended)
                || info.state != PrivatePagePoolState::Available
            {
                break;
            }
            tail -= 1;
        }
        Ok(tail)
    }

    pub(crate) fn finalization_scratch_requirements(
        &self,
    ) -> Result<FreeBitmapFinalizationScratchRequirements, FreeBitmapCowError> {
        self.cow.validate_scoped_bindings()?;
        let (cleanup_nodes, cleanup_path) = private_page_selective_scratch_requirements(
            self.cow.pool().len(),
            self.private_pages,
            self.private_pages,
        )
        .map_err(finalization_error)?;
        let tail = self.projected_tail()?;
        let mut releases = 0usize;
        for binding in &self.cow.arena_bindings[..self.private_pages] {
            let info = self.cow.scoped_slot_info(binding.pool_slot)?;
            if info.state == PrivatePagePoolState::Available && u64::from(info.pgno) < tail {
                releases = releases
                    .checked_add(1)
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            }
        }
        let mut result = FreeBitmapFinalizationScratchRequirements {
            release_pages: self.private_pages,
            insert_pages: 0,
            cached_pages: 1,
            index_stack: self.cow.index_nodes.len(),
            cleanup_nodes,
            cleanup_path,
            cleanup_targets: self.private_pages,
        };
        if releases != 0 || tail != self.cow.pending_page_count {
            result.insert_pages = self
                .private_pages
                .checked_mul(FREE_PATH_CAPACITY)
                .and_then(|value| value.checked_add(FREE_PATH_CAPACITY))
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            result.cached_pages = self
                .private_pages
                .checked_mul(FREE_PATH_CAPACITY)
                .ok_or(FreeBitmapCowError::CoverageOverflow)?
                .max(1);
        }
        Ok(result)
    }

    /// Checks caller-owned finalization scratch before another stage mutates
    /// the shared shadow scope.
    pub(crate) fn preflight_terminal_finalization_scratch(
        &self,
        scratch: &FreeBitmapFinalizationScratch<'_>,
    ) -> Result<(), FreeBitmapCowError> {
        self.validate_finalization_scratch(scratch)
    }

    fn validate_finalization_scratch(
        &self,
        scratch: &FreeBitmapFinalizationScratch<'_>,
    ) -> Result<(), FreeBitmapCowError> {
        let required = self.finalization_scratch_requirements()?;
        for (resource, needed, actual) in [
            (
                ReservationResource::CandidatePages,
                required.release_pages,
                scratch.release_pages.len(),
            ),
            (
                ReservationResource::ArenaPages,
                required.insert_pages,
                scratch.insert_pages.len(),
            ),
            (
                ReservationResource::VerifiedPages,
                required.cached_pages,
                scratch.cached_pages.len(),
            ),
            (
                ReservationResource::IndexNodes,
                required.index_stack,
                scratch.index_stack.len(),
            ),
            (
                ReservationResource::IndexNodes,
                required.cleanup_nodes,
                scratch.cleanup_nodes.len(),
            ),
            (
                ReservationResource::AvailableSlots,
                required.cleanup_path,
                scratch.cleanup_path.len(),
            ),
            (
                ReservationResource::AvailableSlots,
                required.cleanup_targets,
                scratch.cleanup_targets.len(),
            ),
        ] {
            if actual < needed {
                return Err(FreeBitmapCowError::InsufficientResourceBudget {
                    resource,
                    required: needed,
                    available: actual,
                });
            }
        }
        if !scratch
            .cleanup_nodes
            .iter()
            .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            || !scratch
                .cleanup_path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            || !scratch
                .cleanup_targets
                .iter()
                .all(|target| *target == usize::MAX)
            || scratch.cached_pages.iter().any(|page| page.occupied)
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        if reservation_slices_overlap(scratch.release_pages, self.cow.candidates)
            || reservation_slices_overlap(scratch.release_pages, self.cow.replacements)
            || reservation_slices_overlap(scratch.release_pages, self.pool_validation)
            || reservation_slices_overlap(scratch.index_stack, self.cow.available_slots)
            || reservation_slices_overlap(scratch.cleanup_path, self.cow.available_slots)
            || reservation_slices_overlap(scratch.cleanup_targets, self.cow.available_slots)
            || reservation_slices_overlap(scratch.cleanup_path, scratch.cleanup_targets)
            || reservation_slices_overlap(scratch.index_stack, scratch.cleanup_path)
            || reservation_slices_overlap(scratch.index_stack, scratch.cleanup_targets)
        {
            return Err(FreeBitmapCowError::ArenaPageConflict(0));
        }
        Ok(())
    }

    /// Gives one caller-owned probe a detached copy of the exact bound scope.
    ///
    /// This is intentionally separate from the two-pass terminal preview: a
    /// selected-reclaim probe needs the same reader-safe bitmap state before it
    /// knows the retirement blob it will stage into those later passes. The
    /// detached pool and its scope cannot escape the higher-ranked callback.
    pub(crate) fn with_detached_reclamation_stage<T, E, F>(
        &mut self,
        scratch: FreeBitmapFinalizationScratch<'_>,
        stage: F,
    ) -> Result<T, FreeBitmapFinalizationPreviewError<E>>
    where
        F: for<'stage> FnOnce(
            &RetirementReclamationAuthority<'pages>,
            &'stage PrivatePagePool<'stage>,
            &'stage PrivatePageReservationScope<'stage>,
        ) -> Result<T, E>,
    {
        self.validate_finalization_scratch(&scratch)?;
        let result: Result<T, FreeBitmapFinalizationPreviewError<E>> = (|| {
            let live_scope = self
                .cow
                .scoped()
                .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?
                .share();
            let commitment = self
                .cow
                .pool()
                .exact_commitment(&live_scope)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            let cache = FinalizationCachedSource {
                base: self.cow.committed,
                pages: RefCell::new(scratch.cached_pages),
                length: Cell::new(0),
                replay: false,
            };
            build_finalization_shadow!(self, live_scope, &cache, stage_pool, stage_scope, shadow);
            let result = stage(&self.reclamation, &stage_pool, &stage_scope)
                .map_err(FreeBitmapFinalizationPreviewError::Stage)?;
            let cached_len = cache.length.get();
            let cache_seal = cache_fingerprint(&cache.pages.borrow(), cached_len);
            self.cow.committed.check_access().map_err(|error| {
                FreeBitmapFinalizationPreviewError::Bitmap(FreeBitmapCowError::Source(error))
            })?;
            self.cow
                .pool()
                .validate_exact_commitment(&live_scope, &commitment)
                .map_err(|_| {
                    FreeBitmapFinalizationPreviewError::Bitmap(
                        FreeBitmapCowError::StaleInsertionPlan,
                    )
                })?;
            if cache_fingerprint(&cache.pages.borrow(), cached_len) != cache_seal {
                return Err(FreeBitmapFinalizationPreviewError::Bitmap(
                    FreeBitmapCowError::StaleInsertionPlan,
                ));
            }
            Ok(result)
        })();
        scratch
            .cached_pages
            .fill(FreeBitmapFinalizationCachedPage::empty());
        result
    }

    /// Predicts the complete committed bitmap replacement list after terminal
    /// finalization without changing the live scope or bitmap COW.
    pub(crate) fn preview_terminal_replacements(
        &mut self,
        scratch: FreeBitmapFinalizationScratch<'_>,
        output: &mut [u32],
    ) -> Result<usize, FreeBitmapCowError> {
        match self.preview_terminal_replacements_with_stage(scratch, output, |_, _, _| {
            Ok::<_, core::convert::Infallible>(())
        }) {
            Ok(length) => Ok(length),
            Err(FreeBitmapFinalizationPreviewError::Bitmap(error)) => Err(error),
            Err(FreeBitmapFinalizationPreviewError::Stage(never)) => match never {},
        }
    }

    /// Runs caller-owned prospective work in the detached finalization scope
    /// before each bitmap discovery/replay pass. The callback's equality
    /// witness prevents a one-pass or unstable stage from becoming a preview.
    pub(crate) fn preview_terminal_replacements_with_stage<T, E, F>(
        &mut self,
        scratch: FreeBitmapFinalizationScratch<'_>,
        output: &mut [u32],
        mut stage: F,
    ) -> Result<usize, FreeBitmapFinalizationPreviewError<E>>
    where
        T: PartialEq,
        F: for<'stage> FnMut(
            &RetirementReclamationAuthority<'pages>,
            &'stage PrivatePagePool<'stage>,
            &'stage PrivatePageReservationScope<'stage>,
        ) -> Result<T, E>,
    {
        self.validate_finalization_scratch(&scratch)?;
        let output_required = self.cow.replacements.len();
        if output.len() < output_required {
            return Err(FreeBitmapFinalizationPreviewError::Bitmap(
                FreeBitmapCowError::InsufficientResourceBudget {
                    resource: ReservationResource::ReplacementPages,
                    required: output_required,
                    available: output.len(),
                },
            ));
        }

        let result: Result<usize, FreeBitmapFinalizationPreviewError<E>> = (|| {
            let live_scope = self
                .cow
                .scoped()
                .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?
                .share();
            let commitment = self
                .cow
                .pool()
                .exact_commitment(&live_scope)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            let (
                expected_release,
                expected_reclaimed,
                expected_root,
                expected_tail,
                cached_len,
                expected_stage,
            ) = {
                let discovery_cache = FinalizationCachedSource {
                    base: self.cow.committed,
                    pages: RefCell::new(scratch.cached_pages),
                    length: Cell::new(0),
                    replay: false,
                };
                build_finalization_shadow!(
                    self,
                    live_scope,
                    &discovery_cache,
                    stage_pool,
                    stage_scope,
                    shadow
                );
                let expected_stage = stage(&self.reclamation, &stage_pool, &stage_scope)
                    .map_err(FreeBitmapFinalizationPreviewError::Stage)?;
                let (released, reclaimed) = run_fixed_point(
                    &mut shadow,
                    scratch.release_pages,
                    scratch.insert_pages,
                    scratch.index_stack,
                )?;
                (
                    released,
                    reclaimed,
                    shadow.root,
                    shadow.pending_page_count,
                    discovery_cache.length.get(),
                    expected_stage,
                )
            };
            let cache_seal = cache_fingerprint(scratch.cached_pages, cached_len);

            let final_access = self.cow.committed.check_access();
            let live_commitment = self
                .cow
                .pool()
                .validate_exact_commitment(&live_scope, &commitment);
            if live_commitment.is_err() {
                return Err(FreeBitmapCowError::StaleInsertionPlan.into());
            }
            if cache_fingerprint(scratch.cached_pages, cached_len) != cache_seal {
                return Err(FreeBitmapCowError::StaleInsertionPlan.into());
            }
            final_access.map_err(FreeBitmapCowError::Source)?;

            let output_len = {
                let replay_cache = FinalizationCachedSource {
                    base: self.cow.committed,
                    pages: RefCell::new(scratch.cached_pages),
                    length: Cell::new(cached_len),
                    replay: true,
                };
                build_finalization_shadow!(
                    self,
                    live_scope,
                    &replay_cache,
                    stage_pool,
                    stage_scope,
                    shadow
                );
                let replay_stage = stage(&self.reclamation, &stage_pool, &stage_scope)
                    .map_err(FreeBitmapFinalizationPreviewError::Stage)?;
                let (released, reclaimed) = run_fixed_point(
                    &mut shadow,
                    scratch.release_pages,
                    scratch.insert_pages,
                    scratch.index_stack,
                )?;
                if released != expected_release
                    || reclaimed != expected_reclaimed
                    || shadow.root != expected_root
                    || shadow.pending_page_count != expected_tail
                    || replay_cache.length.get() != cached_len
                    || cache_fingerprint(&replay_cache.pages.borrow(), cached_len) != cache_seal
                    || replay_stage != expected_stage
                {
                    return Err(FreeBitmapCowError::StaleInsertionPlan.into());
                }
                output[..shadow.replacement_len]
                    .copy_from_slice(&shadow.replacements[..shadow.replacement_len]);
                shadow.replacement_len
            };

            Ok(output_len)
        })();
        scratch
            .cached_pages
            .fill(FreeBitmapFinalizationCachedPage::empty());
        result
    }

    fn prepare_terminal_finalization<'scratch>(
        &mut self,
        scratch: FreeBitmapFinalizationScratch<'scratch>,
    ) -> Result<PreparedFreeBitmapFinalization<'scratch, 'slots>, FreeBitmapCowError> {
        self.validate_finalization_scratch(&scratch)?;
        let live_scope = self
            .cow
            .scoped()
            .ok_or(FreeBitmapCowError::ArenaPageConflict(0))?
            .share();
        let commitment = self
            .cow
            .pool()
            .exact_commitment(&live_scope)
            .map_err(FreeBitmapCowError::PrivatePool)?;
        let private_pages = self.private_pages;
        let live_pending = self.cow.pending_page_count;

        let (expected_release, expected_reclaimed, expected_root, expected_tail, cached_len) = {
            let discovery_cache = FinalizationCachedSource {
                base: self.cow.committed,
                pages: RefCell::new(scratch.cached_pages),
                length: Cell::new(0),
                replay: false,
            };
            build_finalization_shadow!(
                self,
                live_scope,
                &discovery_cache,
                stage_pool,
                stage_scope,
                shadow
            );
            let (released, reclaimed) = run_fixed_point(
                &mut shadow,
                scratch.release_pages,
                scratch.insert_pages,
                scratch.index_stack,
            )?;
            (
                released,
                reclaimed,
                shadow.root,
                shadow.pending_page_count,
                discovery_cache.length.get(),
            )
        };
        let cache_seal = cache_fingerprint(scratch.cached_pages, cached_len);

        let final_access = self.cow.committed.check_access();
        let live_commitment = self
            .cow
            .pool()
            .validate_exact_commitment(&live_scope, &commitment);
        if live_commitment.is_err() {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        if cache_fingerprint(scratch.cached_pages, cached_len) != cache_seal {
            return Err(FreeBitmapCowError::StaleInsertionPlan);
        }
        final_access.map_err(FreeBitmapCowError::Source)?;

        let (
            sealed_scope,
            nonce,
            bitmap_terminal_page_count,
            range_terminal_page_count,
            retirement_terminal_page_count,
            terminal_page_count,
            released,
            reclaimed,
            mut cleanup,
        ) = {
            let replay_cache = FinalizationCachedSource {
                base: self.cow.committed,
                pages: RefCell::new(scratch.cached_pages),
                length: Cell::new(cached_len),
                replay: true,
            };
            build_finalization_shadow!(
                self,
                live_scope,
                &replay_cache,
                stage_pool,
                stage_scope,
                shadow
            );
            let (released, reclaimed) = run_fixed_point(
                &mut shadow,
                scratch.release_pages,
                scratch.insert_pages,
                scratch.index_stack,
            )?;
            if released != expected_release
                || reclaimed != expected_reclaimed
                || shadow.root != expected_root
                || shadow.pending_page_count != expected_tail
                || replay_cache.length.get() != cached_len
                || cache_fingerprint(&replay_cache.pages.borrow(), cached_len) != cache_seal
            {
                return Err(FreeBitmapCowError::StaleInsertionPlan);
            }

            let mut bitmap_terminal_page_count = 0usize;
            let mut range_terminal_page_count = 0usize;
            let mut retirement_terminal_page_count = 0usize;
            for (index, binding) in shadow.arena_bindings[..private_pages].iter().enumerate() {
                let (release, bitmap_owned, range_owned, retirement_owned) = if !binding.bound {
                    (1, false, false, false)
                } else {
                    let info = stage_pool
                        .scoped_slot_info(&stage_scope, binding.pool_slot)
                        .map_err(FreeBitmapCowError::PrivatePool)?
                        .ok_or(FreeBitmapCowError::ArenaPageConflict(binding.page_number))?;
                    match info.state {
                        PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Bitmap,
                            owner_generation,
                            ..
                        } if owner_generation == self.cow.pending_txn => (0, true, false, false),
                        PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Range,
                            owner_generation,
                            tag: 4 | 6,
                        } if owner_generation == self.cow.pending_txn => (0, false, true, false),
                        PrivatePagePoolState::InUse {
                            owner: PrivatePageOwner::Retirement,
                            owner_generation,
                            tag: 1 | 2,
                        } if owner_generation != 0 => (0, false, false, true),
                        PrivatePagePoolState::ReturnedFree => (1, false, false, false),
                        _ => {
                            return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                        }
                    }
                };
                scratch.release_pages[index] = release;
                bitmap_terminal_page_count = bitmap_terminal_page_count
                    .checked_add(usize::from(bitmap_owned))
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                range_terminal_page_count = range_terminal_page_count
                    .checked_add(usize::from(range_owned))
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                retirement_terminal_page_count = retirement_terminal_page_count
                    .checked_add(usize::from(retirement_owned))
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            }
            let partition = stable_partition_finalized_bindings(
                private_pages,
                |index| Ok(scratch.release_pages[index] == 0),
                self.cow.available_slots,
                scratch.cleanup_targets,
            )?;
            for destination in 0..partition.retained {
                self.pool_validation[destination].pool_slot = self.cow.available_slots[destination];
            }
            for ordinal in 0..partition.released {
                let binding_index = scratch.cleanup_targets[ordinal];
                let binding = shadow.arena_bindings[binding_index];
                if !binding.bound {
                    continue;
                }
                let (root, removed) =
                    page_index_delete(shadow.index_nodes, shadow.index_root, binding.page_number);
                if removed != binding.active_node {
                    return Err(FreeBitmapCowError::ArenaPageConflict(binding.page_number));
                }
                shadow.index_root = root;
                shadow.index_nodes[removed] = BitmapCowIndexNode::empty();
            }

            self.cow.available_slots.fill(usize::MAX);
            for destination in 0..partition.retained {
                let source = self.pool_validation[destination].pool_slot;
                let real_slot = self.cow.arena_bindings[source].pool_slot;
                self.cow.available_slots[source] = real_slot;
            }
            compact_finalized_index(
                shadow.index_nodes,
                &mut shadow.index_root,
                &mut shadow.index_len,
                self.cow.available_slots,
                scratch.index_stack,
            )?;

            let mut target_len = 0usize;
            for ordinal in 0..partition.released {
                let binding_index = scratch.cleanup_targets[ordinal];
                let binding = self.cow.arena_bindings[binding_index];
                if u64::from(binding.page_number) >= shadow.pending_page_count {
                    continue;
                }
                scratch.cleanup_targets[target_len] = binding.pool_slot;
                scratch.release_pages[target_len] = binding.page_number;
                target_len += 1;
            }
            let tail_count = usize::try_from(live_pending - shadow.pending_page_count)
                .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
            for offset in 0..tail_count {
                let pgno = u32::try_from(live_pending - 1 - offset as u64)
                    .map_err(|_| FreeBitmapCowError::CoverageOverflow)?;
                let slot = self
                    .cow
                    .pool()
                    .find_in_scope(&live_scope, pgno)
                    .map_err(FreeBitmapCowError::PrivatePool)?
                    .ok_or(FreeBitmapCowError::ArenaPageConflict(pgno))?;
                scratch.cleanup_targets[target_len] = slot;
                scratch.release_pages[target_len] = pgno;
                target_len += 1;
            }
            if target_len != partition.released {
                return Err(FreeBitmapCowError::ArenaPageConflict(0));
            }
            let cleanup = PrivatePageSelectiveScratch::new(
                scratch.cleanup_nodes,
                scratch.cleanup_path,
                scratch.cleanup_targets,
            );
            let mut deletes = self
                .cow
                .pool()
                .prepare_selective_deletes(&live_scope, cleanup, target_len, partition.retained)
                .map_err(finalization_error)?;
            for destination in 0..partition.retained {
                let source = self.pool_validation[destination].pool_slot;
                let live_slot = self.cow.arena_bindings[source].pool_slot;
                let desired_slot = shadow.arena_bindings[source].pool_slot;
                let desired = stage_pool
                    .finalized_slot(&stage_scope, desired_slot)
                    .map_err(finalization_error)?;
                self.cow
                    .pool()
                    .prepare_retained_refreshes(
                        &live_scope,
                        &mut deletes,
                        core::slice::from_ref(&live_slot),
                        core::slice::from_ref(&desired),
                    )
                    .map_err(finalization_error)?;
            }
            self.cow
                .pool()
                .normalize_selective_deletes(&live_scope, &mut deletes)
                .map_err(finalization_error)?;
            self.cow
                .pool()
                .validate_selective_checkpoint_touches(&live_scope, &deletes)
                .map_err(finalization_error)?;
            self.cow
                .pool()
                .preflight_selective_finalization_epochs(&live_scope, &deletes, private_pages)
                .map_err(finalization_error)?;
            let checkpoint_steps = private_pages
                .checked_add(3)
                .ok_or(FreeBitmapCowError::CoverageOverflow)?;
            let checkpoint = self
                .cow
                .pool()
                .preflight_checkpoint_steps(checkpoint_steps)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            let nonce = mint_nonce()?;

            self.cow
                .pool()
                .validate_exact_commitment(&live_scope, &commitment)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            self.cow
                .pool()
                .begin_checkpoint_prepared(&checkpoint)
                .map_err(FreeBitmapCowError::PrivatePool)?;
            self.cow
                .pool()
                .apply_selective_delete_trees_terminal_prepared(&checkpoint, &live_scope, &deletes);
            for index in 0..deletes.target_len() {
                self.cow.pool().unbind_selective_target_terminal_prepared(
                    &checkpoint,
                    &live_scope,
                    deletes.target(index),
                    u64::from(scratch.release_pages[index]) >= shadow.pending_page_count,
                );
            }
            for destination in 0..partition.retained {
                let source = self.pool_validation[destination].pool_slot;
                self.cow.pool().copy_finalized_from_pool_terminal_prepared(
                    &checkpoint,
                    self.cow.arena_bindings[source].pool_slot,
                    &stage_pool,
                    shadow.arena_bindings[source].pool_slot,
                );
            }
            let sealed_scope = self
                .cow
                .pool()
                .seal_scope_terminal_prepared(&live_scope, nonce);
            self.cow
                .pool()
                .commit_selective_checkpoint_in_scope_terminal_prepared(checkpoint, &live_scope);

            for destination in 0..partition.retained {
                let source = self.pool_validation[destination].pool_slot;
                let real_slot = self.cow.arena_bindings[source].pool_slot;
                let old_epoch = self.cow.arena_bindings[source].pool_epoch;
                let mut binding = shadow.arena_bindings[source];
                binding.storage_node = scratch.index_stack[binding.storage_node];
                binding.active_node = scratch.index_stack[binding.active_node];
                binding.pool_slot = real_slot;
                binding.pool_epoch = old_epoch + 1;
                self.cow.arena_bindings[destination] = binding;
            }
            self.cow.arena_bindings[partition.retained..private_pages]
                .fill(BitmapCowArenaBinding::empty());
            self.cow.replacements.copy_from_slice(shadow.replacements);
            self.cow.index_nodes.copy_from_slice(shadow.index_nodes);
            self.cow.available_slots.fill(0);
            self.cow.replacement_len = shadow.replacement_len;
            self.cow.candidate_len = 0;
            self.cow.index_root = shadow.index_root;
            self.cow.index_len = shadow.index_len;
            self.cow.available_len = 0;
            self.cow.planned_candidate_len = 0;
            self.cow.selected_candidate_len = 0;
            self.cow.candidate_selection_set = false;
            self.cow.reservation_planned = false;
            self.cow.payload_page_budget = 0;
            self.cow.planned_required_private_pages = 0;
            self.cow.pending_page_count = shadow.pending_page_count;
            self.cow.root = shadow.root;

            let cleanup = deletes.into_scratch();
            (
                sealed_scope,
                nonce,
                bitmap_terminal_page_count,
                range_terminal_page_count,
                retirement_terminal_page_count,
                partition.retained,
                released,
                reclaimed,
                cleanup,
            )
        };
        cleanup.clear();
        Ok(PreparedFreeBitmapFinalization {
            scope: sealed_scope,
            nonce,
            bitmap_terminal_page_count,
            range_terminal_page_count,
            retirement_terminal_page_count,
            terminal_page_count,
            released,
            reclaimed,
            cleanup,
        })
    }

    // Returning the move-only reservation is what makes every pre-terminal
    // failure retryable without a heap allocation.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finalize<'scratch>(
        mut self,
        scratch: FreeBitmapFinalizationScratch<'scratch>,
    ) -> Result<
        FreeBitmapFinalizationResult<'scratch, 'a, 'slots, 'scope, S>,
        (Self, FreeBitmapCowError),
    > {
        let prepared = match self.prepare_terminal_finalization(scratch) {
            Ok(prepared) => prepared,
            Err(error) => return Err((self, error)),
        };
        let successor = FreeBitmapFinalizationSuccessorSeed {
            pool: self.cow.persistent_shared_pool(),
            scope: prepared.scope.share(),
            nonce: prepared.nonce,
        };
        let output = SealedFreeBitmapOutput {
            cow: self.cow,
            selected_bitmap_root: self.selected_bitmap_root,
            scope: prepared.scope,
            nonce: prepared.nonce,
            bitmap_terminal_page_count: prepared.bitmap_terminal_page_count,
            range_terminal_page_count: prepared.range_terminal_page_count,
            retirement_terminal_page_count: prepared.retirement_terminal_page_count,
            terminal_page_count: prepared.terminal_page_count,
            cleanup_nodes: prepared.cleanup.nodes,
            cleanup_path: prepared.cleanup.path,
            cleanup_targets: prepared.cleanup.targets,
        };
        Ok(FreeBitmapFinalizationResult {
            output,
            successor,
            released: prepared.released,
            reinserted_reclaimed: prepared.reclaimed,
        })
    }

    /// The only proof-bound entry to three-owner terminal composition. The
    /// stage is checked while this reservation still owns its mutable scope;
    /// the later sealed exporter rechecks the resulting immutable scope.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finalize_range_root_retirement<'proof, 'workspace, 'storage, 'scratch>(
        self,
        stage: &RangeRootRetirementStage,
        proof: &mut RangeRootTransactionProof<'proof, 'workspace, 'storage>,
        scratch: FreeBitmapFinalizationScratch<'scratch>,
    ) -> Result<
        FreeBitmapFinalizationResult<'scratch, 'a, 'slots, 'scope, S>,
        (Self, FreeBitmapCowError),
    > {
        let scope = match self.cow.scoped() {
            Some(scope) => scope,
            None => return Err((self, FreeBitmapCowError::StaleReservationPredecessor)),
        };
        if let Err(error) = stage.verify(&self, self.cow.pool(), scope, proof) {
            return Err((self, range_root_retirement_stage_error(error)));
        }
        let retirement = stage.retirement_result();
        // A normal append COW-replaces the selected retirement-tree path. The
        // proof-bound editor has already listed those committed pages in the
        // new retirement batch. Prior-private replacements, however, would
        // mean a different draft state leaked into this one-shot operation.
        if retirement.prior_private_replacements != 0 {
            return Err((self, FreeBitmapCowError::StaleReservationPredecessor));
        }
        self.finalize(scratch)
    }
}

impl<'scratch, 'a, 'slots, 'scope, S: CommittedPageSource + ?Sized>
    SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>
{
    pub(crate) fn replacements(&self) -> &[u32] {
        self.cow.replacements()
    }

    pub(crate) const fn root(&self) -> u32 {
        self.cow.root
    }

    /// A bitmap root is either a newly retained bitmap page or the exact
    /// selected root. Reclaimed range pages can leave the bitmap unchanged.
    fn produced_bitmap_root_provenance(
        &self,
        pages: &[PrivatePageCoordinatorTerminalPage],
    ) -> Option<ProducedBitmapRootProvenance> {
        let has_bitmap_page = pages
            .iter()
            .any(|page| page.owner == PrivatePageOwner::Bitmap);
        if self.root() == 0 {
            return (!has_bitmap_page).then_some(ProducedBitmapRootProvenance::Empty);
        }
        if pages
            .iter()
            .any(|page| page.pgno == self.root() && page.owner == PrivatePageOwner::Bitmap)
        {
            return Some(ProducedBitmapRootProvenance::Terminal(self.root()));
        }
        (!has_bitmap_page && self.root() == self.selected_bitmap_root)
            .then_some(ProducedBitmapRootProvenance::SelectedUnchanged(self.root()))
    }

    fn bitmap_root_has_terminal_or_selected_provenance(
        &self,
        pages: &[PrivatePageCoordinatorTerminalPage],
    ) -> bool {
        self.produced_bitmap_root_provenance(pages).is_some()
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.cow.pending_page_count
    }

    /// Exact number of bitmap-owned terminal pages in this sealed scope.
    ///
    /// Retirement pages may share the scope, so the reservation capacity and
    /// total in-use count cannot size the bitmap export journal.
    pub(crate) const fn bitmap_terminal_page_count(&self) -> usize {
        self.bitmap_terminal_page_count
    }

    /// Exact number of range-owned terminal pages retained by this sealed
    /// scope. This stays separate from the bitmap count so later composition
    /// cannot infer range ownership from capacity or total bound pages.
    pub(crate) const fn range_terminal_page_count(&self) -> usize {
        self.range_terminal_page_count
    }

    /// Exact number of retirement-owned terminal pages retained by this sealed
    /// scope. Only the proof-bound three-owner exporter may consume them.
    pub(crate) const fn retirement_terminal_page_count(&self) -> usize {
        self.retirement_terminal_page_count
    }

    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn prepare_range_bitmap_retirement_terminal_export<
        'proof,
        'workspace,
        'storage,
        'bitmap_pages,
        'range_pages,
        'retirement_pages,
    >(
        self,
        successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
        stage: &RangeRootRetirementStage,
        proof: &mut RangeRootTransactionProof<'proof, 'workspace, 'storage>,
        bitmap_pages: &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
        range_pages: &'range_pages mut [PrivatePageCoordinatorTerminalPage],
        retirement_pages: &'retirement_pages mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedFreeBitmapRangeRetirementTerminalExport<
            'bitmap_pages,
            'range_pages,
            'retirement_pages,
            'scratch,
            'a,
            'slots,
            'scope,
            S,
        >,
        (
            Self,
            FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
            &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
            &'range_pages mut [PrivatePageCoordinatorTerminalPage],
            &'retirement_pages mut [PrivatePageCoordinatorTerminalPage],
            FreeBitmapCowError,
        ),
    > {
        let all_terminal_pages_accounted = self
            .bitmap_terminal_page_count
            .checked_add(self.range_terminal_page_count)
            .and_then(|count| count.checked_add(self.retirement_terminal_page_count))
            == Some(self.terminal_page_count);
        if !core::ptr::eq(successor.pool, self.cow.pool())
            || successor.nonce != self.nonce
            || self
                .cow
                .pool()
                .validate_sealed_scope(&self.scope, self.nonce)
                .is_err()
            || bitmap_pages.len() != self.bitmap_terminal_page_count
            || range_pages.len() != self.range_terminal_page_count
            || retirement_pages.len() != self.retirement_terminal_page_count
            || retirement_pages.len() != stage.terminal_page_count()
            || !all_terminal_pages_accounted
            || bitmap_pages
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
            || range_pages
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
            || retirement_pages
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
        {
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        if let Err(error) =
            stage.validate_sealed_terminal(self.cow.pool(), &self.scope, self.nonce, proof)
        {
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                range_root_retirement_stage_error(error),
            ));
        }

        if let Err(error) = self
            .cow
            .pool()
            .export_range_scope_terminal_pages(&self.scope, range_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            retirement_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .export_bitmap_scope_terminal_pages(&self.scope, bitmap_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            retirement_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .export_retirement_scope_terminal_pages(&self.scope, retirement_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            retirement_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }

        let materialized = proof.materialized_result();
        let proof_range_pages = proof.range_pages();
        let retirement = stage.retirement_result();
        let range_matches = materialized.page_count == range_pages.len()
            && range_pages.iter().eq(proof_range_pages.iter())
            && range_pages
                .iter()
                .all(|page| u64::from(page.pgno) < self.pending_page_count())
            && ((materialized.root_pgno == 0
                && materialized.page_count == 0
                && materialized.record_count == 0)
                || (materialized.root_pgno >= 2
                    && range_pages.iter().any(|page| {
                        page.pgno == materialized.root_pgno && page.owner == PrivatePageOwner::Range
                    })));
        let retirement_root_found = retirement.root != 0
            && retirement_pages.iter().any(|page| {
                page.pgno == retirement.root && page.owner == PrivatePageOwner::Retirement
            });
        let retirement_matches = retirement_pages.iter().all(|page| {
            page.owner == PrivatePageOwner::Retirement
                && u64::from(page.pgno) < self.pending_page_count()
        }) && if stage.terminal_page_count() == 0 {
            retirement_pages.is_empty() && retirement.private_pages == 0
        } else {
            retirement.root >= 2
                && retirement.batch_count != 0
                && retirement.private_pages != 0
                && retirement_pages.len() >= retirement.private_pages
                && retirement_root_found
        };
        if !range_matches
            || !retirement_matches
            || !self.bitmap_root_has_terminal_or_selected_provenance(bitmap_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            retirement_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                retirement_pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        Ok(PreparedFreeBitmapRangeRetirementTerminalExport {
            output: self,
            successor,
            materialized,
            retirement,
            bitmap_pages,
            range_pages,
            retirement_pages,
        })
    }

    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn prepare_range_and_bitmap_terminal_export<'bitmap_pages, 'range_pages>(
        self,
        successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
        materialized: RangeTreeMaterializedResult,
        bitmap_pages: &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
        range_pages: &'range_pages mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedFreeBitmapRangeTerminalExport<
            'bitmap_pages,
            'range_pages,
            'scratch,
            'a,
            'slots,
            'scope,
            S,
        >,
        (
            Self,
            FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
            &'bitmap_pages mut [PrivatePageCoordinatorTerminalPage],
            &'range_pages mut [PrivatePageCoordinatorTerminalPage],
            FreeBitmapCowError,
        ),
    > {
        let all_terminal_pages_accounted = self
            .bitmap_terminal_page_count
            .checked_add(self.range_terminal_page_count)
            == Some(self.terminal_page_count);
        if !core::ptr::eq(successor.pool, self.cow.pool())
            || successor.nonce != self.nonce
            || self
                .cow
                .pool()
                .validate_sealed_scope(&self.scope, self.nonce)
                .is_err()
            || bitmap_pages.len() != self.bitmap_terminal_page_count
            || range_pages.len() != self.range_terminal_page_count
            || !all_terminal_pages_accounted
            || bitmap_pages
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
            || range_pages
                .iter()
                .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
        {
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .export_bitmap_scope_terminal_pages(&self.scope, bitmap_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .export_range_scope_terminal_pages(&self.scope, range_pages)
        {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
        let range_matches = materialized.page_count == range_pages.len()
            && range_pages
                .iter()
                .all(|page| u64::from(page.pgno) < self.pending_page_count())
            && ((materialized.root_pgno == 0
                && materialized.page_count == 0
                && materialized.record_count == 0)
                || (materialized.root_pgno >= 2
                    && range_pages.iter().any(|page| {
                        page.pgno == materialized.root_pgno && page.owner == PrivatePageOwner::Range
                    })));
        if !range_matches || !self.bitmap_root_has_terminal_or_selected_provenance(bitmap_pages) {
            bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                bitmap_pages,
                range_pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        Ok(PreparedFreeBitmapRangeTerminalExport {
            output: self,
            successor,
            materialized,
            bitmap_pages,
            range_pages,
        })
    }

    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn prepare_terminal_export<'pages>(
        self,
        successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
        pages: &'pages mut [PrivatePageCoordinatorTerminalPage],
    ) -> Result<
        PreparedFreeBitmapTerminalExport<'pages, 'scratch, 'a, 'slots, 'scope, S>,
        (
            Self,
            FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
            &'pages mut [PrivatePageCoordinatorTerminalPage],
            FreeBitmapCowError,
        ),
    > {
        if !core::ptr::eq(successor.pool, self.cow.pool())
            || successor.nonce != self.nonce
            || self
                .cow
                .pool()
                .validate_sealed_scope(&self.scope, self.nonce)
                .is_err()
        {
            return Err((
                self,
                successor,
                pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .export_bitmap_scope_terminal_pages(&self.scope, pages)
        {
            pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                pages,
                FreeBitmapCowError::PrivatePool(error),
            ));
        }
        if !self.bitmap_root_has_terminal_or_selected_provenance(pages) {
            pages.fill(PrivatePageCoordinatorTerminalPage::empty());
            return Err((
                self,
                successor,
                pages,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        Ok(PreparedFreeBitmapTerminalExport {
            output: self,
            successor,
            pages,
        })
    }

    #[allow(clippy::result_large_err, clippy::type_complexity)] // Failure returns both authorities.
    pub(crate) fn into_coordinator_record(
        self,
        successor: FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
        work_unit: u64,
        record_index: usize,
    ) -> Result<
        SealedFreeBitmapCoordinatorRecord<'scratch, 'a, 'a, 'slots>,
        (
            Self,
            FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
            FreeBitmapCowError,
        ),
    > {
        if work_unit == 0
            || !core::ptr::eq(successor.pool, self.cow.pool())
            || successor.nonce != self.nonce
        {
            return Err((
                self,
                successor,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        if let Err(error) = self
            .cow
            .pool()
            .validate_sealed_scope(&self.scope, self.nonce)
        {
            return Err((self, successor, successor_consumption_error(error)));
        }
        let cleanup = match successor.consume() {
            Ok(cleanup) => cleanup,
            Err((successor, error)) => return Err((self, successor, error)),
        };
        let SealedFreeBitmapOutput {
            cow,
            selected_bitmap_root: _,
            scope,
            nonce,
            bitmap_terminal_page_count: _,
            range_terminal_page_count: _,
            retirement_terminal_page_count: _,
            terminal_page_count: _,
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
        } = self;
        let FreeBitmapCow {
            pending_page_count,
            root,
            arena_bindings,
            replacements,
            replacement_len,
            index_nodes,
            index_root,
            ..
        } = cow;
        Ok(SealedFreeBitmapCoordinatorRecord {
            record_index,
            work_unit,
            pool: cleanup.pool,
            scope,
            nonce,
            cleanup,
            root,
            pending_page_count,
            arena_bindings,
            replacements,
            replacement_len,
            index_nodes,
            index_root,
            returned: &[],
            cleanup_nodes,
            cleanup_path,
            cleanup_targets,
        })
    }

    #[cfg(test)]
    pub(crate) fn test_exact_commitment(
        &self,
    ) -> Result<PrivatePagePoolCommitment, PrivatePagePoolError> {
        self.cow.pool().exact_commitment(&self.scope)
    }

    #[cfg(test)]
    pub(crate) fn test_poison_cleanup_scratch(&mut self) {
        self.cleanup_targets[0] = 0;
    }

    #[cfg(test)]
    pub(crate) fn test_repair_cleanup_scratch(&mut self) {
        self.cleanup_nodes
            .fill(PrivatePageSelectiveOverlayNode::empty());
        self.cleanup_path
            .fill(PrivatePageSelectivePathEntry::empty());
        self.cleanup_targets.fill(usize::MAX);
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_cleanup_vacant_payload(
        &self,
        corruption: crate::private_page_pool::PrivatePageVacantPayloadCorruption,
    ) -> (usize, PrivatePagePoolSlot) {
        self.cow
            .pool()
            .test_corrupt_scoped_vacant_payload(&self.scope, corruption)
    }

    #[cfg(test)]
    pub(crate) fn test_corrupt_cleanup_bound_validation_marker(
        &self,
    ) -> (usize, PrivatePagePoolSlot) {
        self.cow
            .pool()
            .test_corrupt_bound_validation_marker(&self.scope)
    }

    #[cfg(test)]
    pub(crate) fn test_restore_cleanup_slot(&self, slot: usize, original: PrivatePagePoolSlot) {
        self.cow.pool().test_restore_slot(slot, original);
    }

    #[cfg(test)]
    pub(crate) fn test_pool_mutation_snapshot(
        &self,
    ) -> crate::private_page_pool::PrivatePagePoolTestSnapshot {
        self.cow.pool().test_mutation_snapshot()
    }

    // Returning both move-only authorities keeps every pre-checkpoint failure
    // retryable without allocating.
    #[allow(clippy::result_large_err)]
    #[allow(clippy::result_large_err)] // Failure must return the move-only cleanup authority.
    pub(crate) fn cleanup(
        self,
        predecessor: FreeBitmapFinalizationPredecessor<'a, 'slots>,
    ) -> Result<
        (),
        (
            Self,
            FreeBitmapFinalizationPredecessor<'a, 'slots>,
            FreeBitmapCowError,
        ),
    > {
        if !core::ptr::eq(predecessor.pool, self.cow.pool()) || predecessor.nonce != self.nonce {
            return Err((
                self,
                predecessor,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        let status = match self
            .cow
            .pool()
            .validate_sealed_scope(&predecessor.scope, predecessor.nonce)
        {
            Ok(status) => status,
            Err(_) => {
                return Err((
                    self,
                    predecessor,
                    FreeBitmapCowError::StaleReservationPredecessor,
                ));
            }
        };
        if !status.successor_consumed || status.bound > self.cow.arena_bindings.len() {
            return Err((
                self,
                predecessor,
                FreeBitmapCowError::StaleReservationPredecessor,
            ));
        }
        if !self
            .cleanup_nodes
            .iter()
            .all(|node| *node == PrivatePageSelectiveOverlayNode::empty())
            || !self
                .cleanup_path
                .iter()
                .all(|entry| *entry == PrivatePageSelectivePathEntry::empty())
            || !self
                .cleanup_targets
                .iter()
                .all(|target| *target == usize::MAX)
        {
            return Err((self, predecessor, FreeBitmapCowError::ArenaPageConflict(0)));
        }
        let mut target_len = 0usize;
        for binding in self.cow.arena_bindings.iter() {
            if !binding.bound {
                continue;
            }
            if target_len == self.cleanup_targets.len() {
                let available = self.cleanup_targets.len();
                self.cleanup_targets.fill(usize::MAX);
                return Err((
                    self,
                    predecessor,
                    FreeBitmapCowError::InsufficientResourceBudget {
                        resource: ReservationResource::AvailableSlots,
                        required: target_len + 1,
                        available,
                    },
                ));
            }
            self.cleanup_targets[target_len] = binding.pool_slot;
            target_len += 1;
        }
        if target_len != status.bound {
            self.cleanup_targets.fill(usize::MAX);
            return Err((self, predecessor, FreeBitmapCowError::ArenaPageConflict(0)));
        }
        let prepared = {
            let scratch = PrivatePageSelectiveScratch::new(
                &mut *self.cleanup_nodes,
                &mut *self.cleanup_path,
                &mut *self.cleanup_targets,
            );
            (|| {
                let mut deletes = self
                    .cow
                    .pool()
                    .prepare_selective_deletes(&predecessor.scope, scratch, target_len, 0)
                    .map_err(finalization_error)?;
                self.cow
                    .pool()
                    .normalize_selective_deletes(&predecessor.scope, &mut deletes)
                    .map_err(finalization_error)?;
                self.cow
                    .pool()
                    .validate_selective_checkpoint_touches(&predecessor.scope, &deletes)
                    .map_err(finalization_error)?;
                self.cow
                    .pool()
                    .preflight_selective_cleanup_epochs(&predecessor.scope, &deletes)
                    .map_err(finalization_error)?;
                let checkpoint_steps = status
                    .capacity
                    .checked_add(target_len)
                    .and_then(|value| value.checked_add(2))
                    .ok_or(FreeBitmapCowError::CoverageOverflow)?;
                let checkpoint = self
                    .cow
                    .pool()
                    .preflight_checkpoint_steps(checkpoint_steps)
                    .map_err(FreeBitmapCowError::PrivatePool)?;
                Ok::<_, FreeBitmapCowError>((deletes, checkpoint))
            })()
        };
        let (deletes, checkpoint) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.cleanup_nodes
                    .fill(PrivatePageSelectiveOverlayNode::empty());
                self.cleanup_path
                    .fill(PrivatePageSelectivePathEntry::empty());
                self.cleanup_targets.fill(usize::MAX);
                return Err((self, predecessor, error));
            }
        };
        if let Err(error) = predecessor.validate_commitment() {
            self.cleanup_nodes
                .fill(PrivatePageSelectiveOverlayNode::empty());
            self.cleanup_path
                .fill(PrivatePageSelectivePathEntry::empty());
            self.cleanup_targets.fill(usize::MAX);
            return Err((self, predecessor, FreeBitmapCowError::PrivatePool(error)));
        }
        if let Err(error) = self.cow.pool().begin_checkpoint_prepared(&checkpoint) {
            self.cleanup_nodes
                .fill(PrivatePageSelectiveOverlayNode::empty());
            self.cleanup_path
                .fill(PrivatePageSelectivePathEntry::empty());
            self.cleanup_targets.fill(usize::MAX);
            return Err((self, predecessor, FreeBitmapCowError::PrivatePool(error)));
        }
        self.cow
            .pool()
            .apply_selective_delete_trees_terminal_prepared(
                &checkpoint,
                &predecessor.scope,
                &deletes,
            );
        for index in 0..deletes.target_len() {
            self.cow.pool().unbind_selective_target_terminal_prepared(
                &checkpoint,
                &predecessor.scope,
                deletes.target(index),
                false,
            );
        }
        self.cow
            .pool()
            .commit_selective_checkpoint_in_scope_terminal_prepared(checkpoint, &predecessor.scope);
        self.cow
            .pool()
            .close_sealed_scope_terminal_prepared(&predecessor.scope, predecessor.nonce);
        Ok(())
    }
}

impl<'pages, 'scratch, 'a, 'slots, 'scope, S: CommittedPageSource + ?Sized>
    PreparedFreeBitmapTerminalExport<'pages, 'scratch, 'a, 'slots, 'scope, S>
{
    pub(crate) const fn root(&self) -> u32 {
        self.output.root()
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.output.pending_page_count()
    }

    pub(crate) fn pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.pages
    }

    pub(crate) fn produced_bitmap_root_provenance(&self) -> Option<ProducedBitmapRootProvenance> {
        self.output.produced_bitmap_root_provenance(self.pages)
    }

    pub(crate) fn replacements(&self) -> &[u32] {
        self.output.replacements()
    }

    pub(super) fn into_parts(
        self,
    ) -> (
        SealedFreeBitmapOutput<'scratch, 'a, 'slots, 'scope, S>,
        FreeBitmapFinalizationSuccessorSeed<'a, 'slots>,
        &'pages mut [PrivatePageCoordinatorTerminalPage],
    ) {
        (self.output, self.successor, self.pages)
    }
}

impl<
        'bitmap_pages,
        'range_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S: CommittedPageSource + ?Sized,
    >
    PreparedFreeBitmapRangeTerminalExport<
        'bitmap_pages,
        'range_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S,
    >
{
    pub(crate) const fn root(&self) -> u32 {
        self.output.root()
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.output.pending_page_count()
    }

    pub(crate) const fn materialized(&self) -> RangeTreeMaterializedResult {
        self.materialized
    }

    pub(crate) fn bitmap_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.bitmap_pages
    }

    pub(crate) fn produced_bitmap_root_provenance(&self) -> Option<ProducedBitmapRootProvenance> {
        self.output
            .produced_bitmap_root_provenance(self.bitmap_pages)
    }

    pub(crate) fn range_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.range_pages
    }
}

impl<
        'bitmap_pages,
        'range_pages,
        'retirement_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S: CommittedPageSource + ?Sized,
    >
    PreparedFreeBitmapRangeRetirementTerminalExport<
        'bitmap_pages,
        'range_pages,
        'retirement_pages,
        'scratch,
        'a,
        'slots,
        'scope,
        S,
    >
{
    pub(crate) const fn root(&self) -> u32 {
        self.output.root()
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.output.pending_page_count()
    }

    pub(crate) const fn materialized(&self) -> RangeTreeMaterializedResult {
        self.materialized
    }

    pub(crate) const fn retirement(&self) -> RetirementTreeEditResult {
        self.retirement
    }

    pub(crate) fn bitmap_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.bitmap_pages
    }

    pub(crate) fn produced_bitmap_root_provenance(&self) -> Option<ProducedBitmapRootProvenance> {
        self.output
            .produced_bitmap_root_provenance(self.bitmap_pages)
    }

    pub(crate) fn range_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.range_pages
    }

    pub(crate) fn retirement_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.retirement_pages
    }

    /// Clears the exported journals after the enclosing private transaction
    /// has already become abort-only. The sealed scope itself is abandoned by
    /// that outer abort; this method only prevents caller-owned journals from
    /// looking reusable.
    pub(crate) fn discard_after_abort(self) {
        let Self {
            bitmap_pages,
            range_pages,
            retirement_pages,
            ..
        } = self;
        bitmap_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
        range_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
        retirement_pages.fill(PrivatePageCoordinatorTerminalPage::empty());
    }
}

impl<S: CommittedPageSource + ?Sized> CommittedPageSource
    for SealedFreeBitmapOutput<'_, '_, '_, '_, S>
{
    fn read_page(&self, pgno: u32, dst: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
        self.cow
            .pool()
            .validate_sealed_scope(&self.scope, self.nonce)
            .map_err(|_| PageSourceError::ForkedHandle)?;
        if let Some(IndexedPage::Arena(slot)) = self.cow.indexed_page(pgno) {
            let bytes = self
                .cow
                .pool()
                .borrow_sealed_page(&self.scope, self.nonce, slot)
                .map_err(|_| PageSourceError::ForkedHandle)?;
            *dst = *bytes;
            return Ok(());
        }
        self.cow.committed.read_page(pgno, dst)
    }

    fn check_access(&self) -> Result<(), PageSourceError> {
        self.cow
            .pool()
            .validate_sealed_scope(&self.scope, self.nonce)
            .map_err(|_| PageSourceError::ForkedHandle)?;
        self.cow.committed.check_access()
    }
}

impl<'a, 'slots> FreeBitmapFinalizationSuccessorSeed<'a, 'slots> {
    #[cfg(test)]
    pub(crate) fn test_duplicate(&self) -> Self {
        Self {
            pool: self.pool,
            scope: self.scope.share(),
            nonce: self.nonce,
        }
    }

    pub(crate) fn consume(
        self,
    ) -> Result<FreeBitmapFinalizationPredecessor<'a, 'slots>, (Self, FreeBitmapCowError)> {
        if let Err(error) = self.pool.validate_sealed_scope(&self.scope, self.nonce) {
            return Err((self, successor_consumption_error(error)));
        }
        let commitment = match self
            .pool
            .consume_sealed_scope_successor_with_commitment(&self.scope, self.nonce)
        {
            Ok(commitment) => commitment,
            Err(error) => return Err((self, successor_consumption_error(error))),
        };
        Ok(FreeBitmapFinalizationPredecessor {
            pool: self.pool,
            scope: self.scope,
            nonce: self.nonce,
            commitment,
        })
    }
}

impl FreeBitmapFinalizationPredecessor<'_, '_> {
    fn validate_commitment(&self) -> Result<(), PrivatePagePoolError> {
        self.pool
            .validate_exact_commitment(&self.scope, &self.commitment)
    }

    fn refresh_commitment(&mut self) -> Result<(), PrivatePagePoolError> {
        self.pool.validate_sealed_scope(&self.scope, self.nonce)?;
        self.commitment = self.pool.exact_commitment(&self.scope)?;
        Ok(())
    }
}

#[cfg(test)]
impl<'a, 'slots> FreeBitmapFinalizationPredecessor<'a, 'slots> {
    pub(crate) fn test_wrong_nonce(&self) -> Self {
        Self {
            pool: self.pool,
            scope: self.scope.share(),
            nonce: self.nonce.checked_add(1).unwrap_or(1),
            commitment: self.commitment.clone(),
        }
    }
}

#[cfg(test)]
mod operation_seal_tests {
    use super::*;

    #[test]
    fn selective_predecessor_rejects_operation_registration_before_terminal_apply() {
        let mut storage = [PrivatePagePoolSlot::empty(), PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut storage, 20, 20, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let foreign = pool.reserve_scope(1).unwrap();
        let predecessor = FreeBitmapFinalizationPredecessor {
            pool: &pool,
            commitment: pool.exact_commitment(&scope).unwrap(),
            scope,
            nonce: 1,
        };
        let mut plan = [];
        let operation = pool
            .preflight_operation_in_scope(&foreign, 0, &mut plan)
            .unwrap();
        assert_eq!(
            predecessor.validate_commitment(),
            Err(PrivatePagePoolError::StaleAuthority)
        );
        pool.finish_operation_in_scope(operation).unwrap();
        assert_eq!(
            predecessor.validate_commitment(),
            Err(PrivatePagePoolError::StaleAuthority)
        );
    }
}

#[cfg(test)]
mod returned_free_compaction_tests {
    use super::*;
    use std::vec::Vec;

    #[test]
    fn returned_free_low_middle_and_high_bindings_compact_stably() {
        for released in [
            [true, false, false],
            [false, true, false],
            [false, false, true],
        ] {
            let mut retained_indices = [usize::MAX; 3];
            let mut released_indices = [usize::MAX; 3];
            let partition = stable_partition_finalized_bindings(
                released.len(),
                |index| Ok(!released[index]),
                &mut retained_indices,
                &mut released_indices,
            )
            .unwrap();
            let expected_retained: Vec<_> = (0..released.len())
                .filter(|&index| !released[index])
                .collect();
            let expected_released: Vec<_> = (0..released.len())
                .filter(|&index| released[index])
                .collect();
            assert_eq!(
                &retained_indices[..partition.retained],
                expected_retained.as_slice()
            );
            assert_eq!(
                &released_indices[..partition.released],
                expected_released.as_slice()
            );
            assert!(retained_indices[partition.retained..]
                .iter()
                .all(|&index| index == usize::MAX));
            assert!(released_indices[partition.released..]
                .iter()
                .all(|&index| index == usize::MAX));
        }
    }
}
