//! Lock-bound input construction for private bitmap/retirement finalization.
//!
//! This module deliberately stops before coordinator execution. It makes the
//! required live sequence non-optional: use the selected source and held
//! reader fence to verify a reclaimable prefix, bind bitmap pages in a
//! caller-owned shadow scope, construct the exact combined terminal output,
//! then hand that move-only output to the existing coordinator boundary.

use crate::bitmap_cow::{
    BoundFreeBitmapReservation, FreeBitmapCowError, FreeBitmapFinalizationCachedPage,
    FreeBitmapFinalizationPreviewError, FreeBitmapFinalizationScratch, FreeBitmapInsertPage,
    FreeBitmapReservationBuffers, FreeBitmapReservationPlanner, LockedFreeBitmapReservationPlan,
    PreparedFreeBitmapTerminalExport, ReservationResource,
};
use crate::contract::MetaV4;
use crate::page_source::CommittedPageSource;
use crate::private_page_pool::{
    PrivatePageCoordinatorTerminalPage, PrivatePagePool, PrivatePageReservationScope,
    PrivatePageSelectiveOverlayNode, PrivatePageSelectivePathEntry,
};
use crate::retirement_page::RetirementBatch;
use crate::retirement_reader::{
    RetirementIdentity, RetirementReadError, RetirementReclaimFence, RetirementReclamation,
    RetirementReclamationExecutionError, RetirementTree,
};
use crate::retirement_writer::{
    BlobBuildScratch, CommittedPageReplacement, CommittedReplacementLedger, PageRoleIndex,
    PageRoleIndexSlot, PreparedProducedTerminalExport, PreparedRetirementTerminalExport,
    PrivatePageArena, PrivateReleaseBuffer, RetirementBlobBuilder, RetirementPathFrame,
    RetirementTreeEditResult, RetirementTreeEditor, RetirementTreeState, RetirementWriteError,
};
use crate::writer_fixed_point::{
    FixedPointError, FixedPointPreparedOutput, FixedPointPreparedProducedTerminalWork,
    FixedPointReservedWork,
};
use core::cell::Cell;

/// Bounded work limits and bitmap payload capacity for one lock-held attempt.
///
/// All backing storage remains caller-owned in
/// [`LockedReclamationFinalizerScratch`]. The private helper rejects zero
/// limits before it reads the selected source or changes the shadow scope.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct LockedReclamationFinalizerLimits {
    pub(crate) max_batches: u64,
    pub(crate) max_pages: u64,
    pub(crate) bitmap_payload_pages: usize,
}

/// Caller-owned scratch retained for the complete selection and bitmap-binding
/// phase of one lock-bound reclamation finalizer.
#[derive(Debug)]
pub(crate) struct LockedReclamationFinalizerScratch<'a> {
    pub(crate) bitmap: FreeBitmapReservationBuffers<'a>,
    pub(crate) verified_batches: &'a mut [RetirementBatch],
    pub(crate) verified_pages: &'a mut [u32],
}

/// Typed failure before a finalizer can construct terminal output or publish.
#[derive(Debug)]
pub(crate) enum LockedReclamationFinalizerError {
    InvalidLimits,
    Retirement(RetirementReadError),
    Bitmap(FreeBitmapCowError),
}

/// A verified and physically bound bitmap reservation that retains the
/// operation-barrier guard through later finalization.
#[derive(Debug)]
pub(crate) struct LockedReclamationBitmapReservation<
    'a,
    'slots,
    'scope,
    'barrier,
    S: CommittedPageSource + ?Sized,
> {
    pub(crate) pass: crate::retirement_reader::RetirementPassResult,
    pub(crate) bound: BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, 'a, S>,
}

/// A selected bitmap plan that has exact physical-page capacity but has not
/// bound a shadow scope or mutated it yet.
#[derive(Debug)]
pub(crate) struct LockedReclamationBitmapPlan<'a, 'barrier, S: CommittedPageSource + ?Sized> {
    pass: crate::retirement_reader::RetirementPassResult,
    plan: LockedFreeBitmapReservationPlan<'a, 'barrier, 'a, S>,
}

impl<'a, 'barrier, S: CommittedPageSource + ?Sized> LockedReclamationBitmapPlan<'a, 'barrier, S> {
    pub(crate) const fn pass(&self) -> crate::retirement_reader::RetirementPassResult {
        self.pass
    }

    pub(crate) const fn required_private_pages(&self) -> usize {
        self.plan.required_private_pages()
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn bind<'slots, 'scope>(
        self,
        shadow_pool: &'a PrivatePagePool<'slots>,
        shadow_scope: &'a PrivatePageReservationScope<'scope>,
    ) -> Result<
        LockedReclamationBitmapReservation<'a, 'slots, 'scope, 'barrier, S>,
        LockedReclamationFinalizerError,
    > {
        let Self { pass, plan } = self;
        let mut bound = plan
            .bind(shadow_pool, shadow_scope)
            .map_err(LockedReclamationFinalizerError::Bitmap)?;
        bound
            .cow
            .apply_planned_reservation()
            .map_err(LockedReclamationFinalizerError::Bitmap)?;
        Ok(LockedReclamationBitmapReservation { pass, bound })
    }
}

/// Selection and capacity planning outcome before any private shadow scope is
/// bound. `NoChange` deliberately owns no allocator state.
#[derive(Debug)]
#[allow(clippy::large_enum_variant)] // The selected plan is move-only and must not allocate.
pub(crate) enum LockedReclamationBitmapPlanOutcome<'a, 'barrier, S: CommittedPageSource + ?Sized> {
    NoChange,
    Selected(LockedReclamationBitmapPlan<'a, 'barrier, S>),
}

/// Caller-owned buffers for the selected-reclaim protected-page fixed point.
///
/// The probe and each detached preview use independent path/ledger storage;
/// sharing either would make a replay appear stable only because it overwrote
/// the first pass's evidence.
#[derive(Debug)]
pub(crate) struct ReclamationProtectedPagesScratch<'protected, 'scratch> {
    pub(crate) probe_delete_path: &'scratch mut [RetirementPathFrame],
    pub(crate) probe_upsert_path: &'scratch mut [RetirementPathFrame],
    pub(crate) probe_replacements: &'scratch mut [CommittedPageReplacement],
    pub(crate) probe_releases: &'scratch mut [u32],
    pub(crate) probe_roles: &'scratch mut [PageRoleIndexSlot],
    pub(crate) protected_pages: &'protected mut [u32],
    pub(crate) next_protected_pages: &'scratch mut [u32],
    pub(crate) preview_bitmap_replacements: &'scratch mut [u32],
    pub(crate) preview_blob_pages: &'scratch mut [u32],
    pub(crate) preview_delete_path: &'scratch mut [RetirementPathFrame],
    pub(crate) preview_upsert_path: &'scratch mut [RetirementPathFrame],
    pub(crate) preview_replacements: &'scratch mut [CommittedPageReplacement],
    pub(crate) preview_releases: &'scratch mut [u32],
    pub(crate) preview_roles: &'scratch mut [PageRoleIndexSlot],
    pub(crate) final_release_pages: &'scratch mut [u32],
    pub(crate) final_insert_pages: &'scratch mut [FreeBitmapInsertPage],
    pub(crate) final_cached_pages: &'scratch mut [FreeBitmapFinalizationCachedPage],
    pub(crate) final_index_stack: &'scratch mut [usize],
    pub(crate) final_cleanup_nodes: &'scratch mut [PrivatePageSelectiveOverlayNode],
    pub(crate) final_cleanup_path: &'scratch mut [PrivatePageSelectivePathEntry],
    pub(crate) final_cleanup_targets: &'scratch mut [usize],
}

/// A converged selected-reclaim page list and the exact bounded retirement
/// capacity facts needed to materialize its next batch.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct SelectedReclamationProtectedPages<'a> {
    pages: &'a [u32],
    blob_private_pages: usize,
    tree_private_page_budget: usize,
    retirement_private_page_budget: usize,
}

impl SelectedReclamationProtectedPages<'_> {
    pub(crate) fn pages(&self) -> &[u32] {
        self.pages
    }

    pub(crate) const fn blob_private_pages(&self) -> usize {
        self.blob_private_pages
    }

    pub(crate) const fn tree_private_page_budget(&self) -> usize {
        self.tree_private_page_budget
    }

    pub(crate) const fn retirement_private_page_budget(&self) -> usize {
        self.retirement_private_page_budget
    }
}

/// Caller-owned scratch for one actual selected-reclaim retirement stage.
#[derive(Debug)]
pub(crate) struct SelectedReclamationRetirementScratch<'terminal, 'scratch> {
    pub(crate) blob_pages: &'scratch mut [u32],
    pub(crate) delete_path: &'scratch mut [RetirementPathFrame],
    pub(crate) upsert_path: &'scratch mut [RetirementPathFrame],
    pub(crate) replacements: &'scratch mut [CommittedPageReplacement],
    pub(crate) releases: &'scratch mut [u32],
    pub(crate) roles: &'scratch mut [PageRoleIndexSlot],
    pub(crate) terminal_pages: &'terminal mut [PrivatePageCoordinatorTerminalPage],
}

/// Failure while staging selected-reclaim retirement output in an isolated
/// shadow attempt. Post-mutation failures require the caller to discard that
/// attempt before retrying or aborting its still-pending transaction.
#[derive(Debug)]
pub(crate) enum SelectedReclamationRetirementStageError {
    MissingSelectedIdentity,
    BlobScratchTooSmall { required: usize, actual: usize },
    TerminalPagesTooSmall { required: usize, actual: usize },
    TerminalPagesNotEmpty,
    PreMutationBitmap(FreeBitmapCowError),
    PreMutationRetirement(RetirementWriteError),
    PostMutationRetirement(RetirementWriteError),
    PostMutationBitmap(FreeBitmapCowError),
    PostMutationTerminalPageCountOverflow,
    PostMutationCapacityMismatch { actual: usize, budget: usize },
}

impl SelectedReclamationRetirementStageError {
    const fn is_pre_mutation(&self) -> bool {
        matches!(
            self,
            Self::MissingSelectedIdentity
                | Self::BlobScratchTooSmall { .. }
                | Self::TerminalPagesTooSmall { .. }
                | Self::TerminalPagesNotEmpty
                | Self::PreMutationBitmap(_)
                | Self::PreMutationRetirement(_)
        )
    }
}

/// Caller-owned storage for the terminal half of one selected-reclaim
/// attempt. The maximum journals are sized to the already-reserved shadow
/// scope; the helper returns only their exact used prefixes.
pub(crate) struct SelectedReclamationTerminalScratch<
    'retirement_terminal,
    'retirement_scratch,
    'bitmap_terminal,
    'bitmap_scratch,
    'combined,
> {
    pub(crate) retirement:
        SelectedReclamationRetirementScratch<'retirement_terminal, 'retirement_scratch>,
    pub(crate) bitmap_finalization: FreeBitmapFinalizationScratch<'bitmap_scratch>,
    pub(crate) bitmap_terminal_pages: &'bitmap_terminal mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) combined_terminal_pages: &'combined mut [PrivatePageCoordinatorTerminalPage],
}

/// A real selected-reclaim terminal export ready for the existing coordinator
/// bind step, but not yet bound or published.
pub(crate) struct PreparedSelectedReclamationTerminalExport<
    'combined,
    'bitmap_terminal,
    'bitmap_scratch,
    'a,
    'slots,
    'scope,
    S: CommittedPageSource + ?Sized,
> {
    pass: crate::retirement_reader::RetirementPassResult,
    bitmap_root: u32,
    pending_page_count: u64,
    retirement: RetirementTreeEditResult,
    export: PreparedProducedTerminalExport<
        'combined,
        PreparedFreeBitmapTerminalExport<'bitmap_terminal, 'bitmap_scratch, 'a, 'slots, 'scope, S>,
    >,
}

impl<'combined, 'bitmap_terminal, 'bitmap_scratch, 'a, 'slots, 'scope, S>
    PreparedSelectedReclamationTerminalExport<
        'combined,
        'bitmap_terminal,
        'bitmap_scratch,
        'a,
        'slots,
        'scope,
        S,
    >
where
    S: CommittedPageSource + ?Sized,
{
    pub(crate) const fn pass(&self) -> crate::retirement_reader::RetirementPassResult {
        self.pass
    }

    pub(crate) const fn bitmap_root(&self) -> u32 {
        self.bitmap_root
    }

    pub(crate) const fn pending_page_count(&self) -> u64 {
        self.pending_page_count
    }

    pub(crate) const fn retirement_result(&self) -> RetirementTreeEditResult {
        self.retirement
    }

    pub(crate) fn bitmap_replacements(&self) -> &[u32] {
        self.export.bitmap().replacements()
    }

    pub(crate) fn terminal_pages(&self) -> &[PrivatePageCoordinatorTerminalPage] {
        self.export.pages()
    }

    /// Binds this complete selected-reclaim output to its pre-reserved
    /// coordinator scope. A failed preflight returns both move-only inputs, so
    /// the caller can retry without reconstructing the shadow output.
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn bind_to_reserved_work<'slot, 'scope_slot, 'scratch, 'carried>(
        self,
        reserved: FixedPointReservedWork<'slot, 'scope_slot, 'scratch, 'carried>,
        pool: &PrivatePagePool<'_>,
        nonce: u64,
    ) -> Result<
        FixedPointPreparedProducedTerminalWork<
            'slot,
            'scope_slot,
            'scratch,
            'carried,
            'combined,
            PreparedFreeBitmapTerminalExport<
                'bitmap_terminal,
                'bitmap_scratch,
                'a,
                'slots,
                'scope,
                S,
            >,
        >,
        (
            FixedPointReservedWork<'slot, 'scope_slot, 'scratch, 'carried>,
            Self,
            FixedPointError,
        ),
    > {
        let Self {
            pass,
            bitmap_root,
            pending_page_count,
            retirement,
            export,
        } = self;
        match reserved.with_finalized_produced_terminal_export(
            pool,
            FixedPointPreparedOutput {
                root: bitmap_root,
                pending_page_count,
            },
            export,
            nonce,
        ) {
            Ok(work) => Ok(work),
            Err((reserved, export, error)) => Err((
                reserved,
                Self {
                    pass,
                    bitmap_root,
                    pending_page_count,
                    retirement,
                    export,
                },
                error,
            )),
        }
    }

    pub(crate) fn into_export(
        self,
    ) -> PreparedProducedTerminalExport<
        'combined,
        PreparedFreeBitmapTerminalExport<'bitmap_terminal, 'bitmap_scratch, 'a, 'slots, 'scope, S>,
    > {
        self.export
    }
}

/// The non-output reason a selected-reclaim terminal composition stopped.
#[derive(Debug)]
pub(crate) enum SelectedReclamationTerminalCompositionError {
    PreMutationBitmap(FreeBitmapCowError),
    BitmapTerminalPagesTooSmall { required: usize, actual: usize },
    BitmapTerminalPagesNotEmpty,
    CombinedTerminalPagesTooSmall { required: usize, actual: usize },
    CombinedTerminalPagesNotEmpty,
    RetirementStage(SelectedReclamationRetirementStageError),
    PostMutationBitmapFinalization(FreeBitmapCowError),
    PostMutationBitmapTerminalPageCountExceedsScope { actual: usize, capacity: usize },
    PostMutationBitmapExport(FreeBitmapCowError),
    PostMutationCombinedTerminalPageCountOverflow,
    PostMutationCombinedTerminalPageCountExceedsScope { actual: usize, capacity: usize },
    PostMutationMerge(RetirementWriteError),
}

/// Terminal composition either returns its untouched reservation for retry or
/// consumes the shadow attempt after a real retirement mutation. The latter
/// has no retry authority by design: the caller must discard the attempt and
/// use the outer transaction's existing abort/restart path.
#[derive(Debug)]
#[allow(clippy::large_enum_variant)]
pub(crate) enum SelectedReclamationTerminalCompositionFailure<
    'a,
    'slots,
    'scope,
    'barrier,
    S: CommittedPageSource + ?Sized,
> {
    Retry {
        reservation: LockedReclamationBitmapReservation<'a, 'slots, 'scope, 'barrier, S>,
        error: SelectedReclamationTerminalCompositionError,
    },
    Discard {
        error: SelectedReclamationTerminalCompositionError,
    },
}

/// Typed read-only fixed-point failure before terminal finalization begins.
#[derive(Debug)]
pub(crate) enum ReclamationProtectedPagesError {
    NoSelectedBatches,
    SelectedIdentityUnavailable,
    Bitmap(FreeBitmapCowError),
    Retirement(RetirementWriteError),
    ProbeChanged,
    ReplacementPageOutOfBounds(u32),
    FixedPointDidNotConverge { limit: usize },
}

/// Runs lock-held selection, verification, and bitmap capacity planning before
/// any shadow scope is bound.
///
/// This is the Reclaim owner boundary: a no-change result does not invoke the
/// bitmap planner or create private allocator state, while a selected result
/// retains the reader fence until the caller binds its exact required scope.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn plan_locked_reclamation_bitmap_reservation<
    'a,
    'barrier,
    S: CommittedPageSource + ?Sized,
>(
    selected: MetaV4,
    pages: &'a S,
    reclaim_fence: RetirementReclaimFence<'barrier>,
    limits: LockedReclamationFinalizerLimits,
    scratch: LockedReclamationFinalizerScratch<'a>,
) -> Result<LockedReclamationBitmapPlanOutcome<'a, 'barrier, S>, LockedReclamationFinalizerError> {
    if limits.max_batches == 0 || limits.max_pages == 0 || limits.bitmap_payload_pages == 0 {
        return Err(LockedReclamationFinalizerError::InvalidLimits);
    }

    let identity = RetirementIdentity {
        database_id: selected.database_id,
        txn_id: selected.txn_id,
        commit_nonce: selected.commit_nonce,
        page_count: selected.page_count,
        root: selected.retirement_root,
        batch_count: selected.retirement_batch_count,
    };
    let tree = RetirementTree::from_source(pages, identity)
        .map_err(LockedReclamationFinalizerError::Retirement)?;
    let LockedReclamationFinalizerScratch {
        bitmap,
        verified_batches,
        verified_pages,
    } = scratch;
    let reclamation = tree
        .prepare_reclamation(
            reclaim_fence,
            limits.max_batches,
            limits.max_pages,
            verified_batches,
            verified_pages,
        )
        .map_err(LockedReclamationFinalizerError::Retirement)?;
    let pass = reclamation.pass_result();
    let reclamation = match reclamation {
        RetirementReclamation::NoChange(_) => {
            return Ok(LockedReclamationBitmapPlanOutcome::NoChange)
        }
        reclamation @ RetirementReclamation::Reclaimed(_) => reclamation,
    };

    let planner = FreeBitmapReservationPlanner::new(
        pages,
        selected.txn_id,
        selected.page_count,
        selected.free_bitmap_root,
        limits.bitmap_payload_pages,
        bitmap,
    )
    .map_err(LockedReclamationFinalizerError::Bitmap)?;
    let plan = planner
        .plan_under_reclamation(reclamation)
        .map_err(LockedReclamationFinalizerError::Bitmap)?;
    Ok(LockedReclamationBitmapPlanOutcome::Selected(
        LockedReclamationBitmapPlan { pass, plan },
    ))
}

/// Runs the mandatory lock-held prefix of a bitmap/retirement finalizer.
///
/// The returned reservation retains the live reader-barrier authority.
/// Consequently, no caller can bind pages from an arbitrary slice, skip
/// verification, or continue finalization after the operation barrier has
/// been released. Later finalizers consume that move-only reservation directly
/// and return it to their caller on a pre-terminal error.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn prepare_locked_reclamation_bitmap_reservation<
    'a,
    'slots,
    'scope,
    'barrier,
    S: CommittedPageSource + ?Sized,
>(
    selected: MetaV4,
    pages: &'a S,
    reclaim_fence: RetirementReclaimFence<'barrier>,
    limits: LockedReclamationFinalizerLimits,
    scratch: LockedReclamationFinalizerScratch<'a>,
    shadow_pool: &'a PrivatePagePool<'slots>,
    shadow_scope: &'a PrivatePageReservationScope<'scope>,
) -> Result<
    LockedReclamationBitmapReservation<'a, 'slots, 'scope, 'barrier, S>,
    LockedReclamationFinalizerError,
> {
    if limits.max_batches == 0 || limits.max_pages == 0 || limits.bitmap_payload_pages == 0 {
        return Err(LockedReclamationFinalizerError::InvalidLimits);
    }

    let identity = RetirementIdentity {
        database_id: selected.database_id,
        txn_id: selected.txn_id,
        commit_nonce: selected.commit_nonce,
        page_count: selected.page_count,
        root: selected.retirement_root,
        batch_count: selected.retirement_batch_count,
    };
    let tree = RetirementTree::from_source(pages, identity)
        .map_err(LockedReclamationFinalizerError::Retirement)?;
    let LockedReclamationFinalizerScratch {
        bitmap,
        verified_batches,
        verified_pages,
    } = scratch;

    match tree.with_reclamation(
        reclaim_fence,
        limits.max_batches,
        limits.max_pages,
        verified_batches,
        verified_pages,
        |pass, reclamation| {
            let planner = FreeBitmapReservationPlanner::new(
                pages,
                selected.txn_id,
                selected.page_count,
                selected.free_bitmap_root,
                limits.bitmap_payload_pages,
                bitmap,
            )?;
            let locked = planner.plan_under_reclamation(reclamation)?;
            let mut bound = locked.bind(shadow_pool, shadow_scope)?;
            bound.cow.apply_planned_reservation()?;
            Ok::<_, FreeBitmapCowError>(LockedReclamationBitmapReservation { pass, bound })
        },
    ) {
        Ok(result) => Ok(result),
        Err(RetirementReclamationExecutionError::Read(error)) => {
            Err(LockedReclamationFinalizerError::Retirement(error))
        }
        Err(RetirementReclamationExecutionError::Consumer(error)) => {
            Err(LockedReclamationFinalizerError::Bitmap(error))
        }
    }
}

fn merge_reclamation_protected_pages(
    bitmap: &[u32],
    retirement: &[CommittedPageReplacement],
    page_count: u64,
    output: &mut [u32],
) -> Result<usize, ReclamationProtectedPagesError> {
    let raw_len = bitmap.len().checked_add(retirement.len()).ok_or(
        ReclamationProtectedPagesError::Retirement(RetirementWriteError::ArithmeticOverflow),
    )?;
    if raw_len > output.len() {
        return Err(ReclamationProtectedPagesError::Retirement(
            RetirementWriteError::ReplacementLedgerTooSmall {
                required: raw_len,
                actual: output.len(),
            },
        ));
    }
    output[..bitmap.len()].copy_from_slice(bitmap);
    for (destination, replacement) in output[bitmap.len()..raw_len].iter_mut().zip(retirement) {
        *destination = replacement.pgno;
    }
    output[..raw_len].sort_unstable();
    let mut unique = 0usize;
    for index in 0..raw_len {
        let pgno = output[index];
        if pgno < 2 || u64::from(pgno) >= page_count {
            return Err(ReclamationProtectedPagesError::ReplacementPageOutOfBounds(
                pgno,
            ));
        }
        if unique == 0 || output[unique - 1] != pgno {
            output[unique] = pgno;
            unique += 1;
        }
    }
    Ok(unique)
}

fn require_finalization_capacity(
    resource: ReservationResource,
    required: usize,
    actual: usize,
) -> Result<(), ReclamationProtectedPagesError> {
    if actual < required {
        return Err(ReclamationProtectedPagesError::Bitmap(
            FreeBitmapCowError::InsufficientResourceBudget {
                resource,
                required,
                available: actual,
            },
        ));
    }
    Ok(())
}

fn replacement_fingerprint(entries: &[CommittedPageReplacement]) -> u64 {
    let mut fingerprint = 0u64;
    for replacement in entries {
        fingerprint = fingerprint.wrapping_mul(0x100_0000_01b3)
            ^ u64::from(replacement.pgno)
            ^ u64::from(matches!(
                replacement.origin,
                crate::retirement_writer::CommittedPageOrigin::RetirementBlob
            ));
    }
    fingerprint
}

fn selected_reclamation_retirement_private_page_budget(
    protected_page_count: usize,
    tree_private_page_budget: usize,
) -> Result<(usize, usize), ReclamationProtectedPagesError> {
    let protected_page_count = u64::try_from(protected_page_count).map_err(|_| {
        ReclamationProtectedPagesError::Retirement(RetirementWriteError::ArithmeticOverflow)
    })?;
    let blob_private_pages = RetirementBlobBuilder::required_private_pages(protected_page_count)
        .map_err(ReclamationProtectedPagesError::Retirement)?;
    let retirement_private_page_budget = blob_private_pages
        .checked_add(tree_private_page_budget)
        .ok_or(ReclamationProtectedPagesError::Retirement(
        RetirementWriteError::ArithmeticOverflow,
    ))?;
    Ok((blob_private_pages, retirement_private_page_budget))
}

fn require_selected_reclamation_retirement_capacity(
    required: usize,
    available: usize,
) -> Result<(), ReclamationProtectedPagesError> {
    if required > available {
        return Err(ReclamationProtectedPagesError::Retirement(
            RetirementWriteError::PrivatePageBudgetTooSmall {
                required,
                actual: available,
            },
        ));
    }
    Ok(())
}

/// Computes the stable page list that the next selected-reclaim retirement
/// batch must protect, without terminally finalizing the live bitmap scope.
///
/// It first probes the old retirement prefix, then runs the prospective
/// retirement blob/tree edit inside both detached bitmap preview passes. The
/// protected list is accepted only when the replacement union stops changing.
/// Both the source and retirement identity come from the bound reservation, so
/// callers cannot combine proof from one selected generation with pages from
/// another.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn preview_selected_reclamation_protected_pages<
    'protected,
    'scratch,
    'a,
    'slots,
    'scope,
    'barrier,
    'pages,
    S: CommittedPageSource + ?Sized,
>(
    bound: &mut BoundFreeBitmapReservation<'a, 'slots, 'scope, 'barrier, 'pages, S>,
    scratch: ReclamationProtectedPagesScratch<'protected, 'scratch>,
) -> Result<SelectedReclamationProtectedPages<'protected>, ReclamationProtectedPagesError> {
    if bound.reclamation_authority().batch_count() == 0 {
        return Err(ReclamationProtectedPagesError::NoSelectedBatches);
    }
    let identity = bound
        .reclamation_authority()
        .identity()
        .ok_or(ReclamationProtectedPagesError::SelectedIdentityUnavailable)?;
    let pages = bound.reclamation_source();
    let pending_txn =
        identity
            .txn_id
            .checked_add(1)
            .ok_or(ReclamationProtectedPagesError::Retirement(
                RetirementWriteError::SelectedTransactionOverflow(identity.txn_id),
            ))?;
    let state = RetirementTreeState {
        selected_txn: identity.txn_id,
        page_count: identity.page_count,
        root: identity.root,
        batch_count: identity.batch_count,
    };
    let requirements = bound
        .finalization_scratch_requirements()
        .map_err(ReclamationProtectedPagesError::Bitmap)?;
    let ReclamationProtectedPagesScratch {
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
    } = scratch;
    require_finalization_capacity(
        ReservationResource::CandidatePages,
        requirements.release_pages,
        final_release_pages.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::ArenaPages,
        requirements.insert_pages,
        final_insert_pages.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::VerifiedPages,
        requirements.cached_pages,
        final_cached_pages.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::IndexNodes,
        requirements.index_stack,
        final_index_stack.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::IndexNodes,
        requirements.cleanup_nodes,
        final_cleanup_nodes.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::AvailableSlots,
        requirements.cleanup_path,
        final_cleanup_path.len(),
    )?;
    require_finalization_capacity(
        ReservationResource::AvailableSlots,
        requirements.cleanup_targets,
        final_cleanup_targets.len(),
    )?;

    let (probe_replacement_len, tree_private_page_budget) = bound
        .with_detached_reclamation_stage(
            FreeBitmapFinalizationScratch {
                release_pages: &mut final_release_pages[..requirements.release_pages],
                insert_pages: &mut final_insert_pages[..requirements.insert_pages],
                cached_pages: &mut final_cached_pages[..requirements.cached_pages],
                index_stack: &mut final_index_stack[..requirements.index_stack],
                cleanup_nodes: &mut final_cleanup_nodes[..requirements.cleanup_nodes],
                cleanup_path: &mut final_cleanup_path[..requirements.cleanup_path],
                cleanup_targets: &mut final_cleanup_targets[..requirements.cleanup_targets],
            },
            |reclamation, shadow_pool, shadow_scope| {
                let mut arena =
                    PrivatePageArena::from_scoped_pool(shadow_pool, shadow_scope, pending_txn)
                        .map_err(ReclamationProtectedPagesError::Retirement)?;
                let mut replacements = CommittedReplacementLedger::new(probe_replacements);
                let mut releases = PrivateReleaseBuffer::new(probe_releases);
                let mut roles = PageRoleIndex::new(probe_roles);
                let probe = RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest(
                    pages,
                    state,
                    identity,
                    reclamation,
                    &mut arena,
                    probe_delete_path,
                    probe_upsert_path,
                    &mut replacements,
                    &mut releases,
                    &mut roles,
                )
                .map_err(ReclamationProtectedPagesError::Retirement)?;
                if probe.replacement_count != replacements.entries().len()
                    || !releases.entries_from(0).is_empty()
                    || arena
                        .in_use_count()
                        .map_err(ReclamationProtectedPagesError::Retirement)?
                        != 0
                {
                    return Err(ReclamationProtectedPagesError::ProbeChanged);
                }
                Ok::<_, ReclamationProtectedPagesError>((
                    probe.replacement_count,
                    probe.tree_private_page_budget,
                ))
            },
        )
        .map_err(|error| match error {
            FreeBitmapFinalizationPreviewError::Bitmap(error) => {
                ReclamationProtectedPagesError::Bitmap(error)
            }
            FreeBitmapFinalizationPreviewError::Stage(error) => error,
        })?;
    let probe_entries = &probe_replacements[..probe_replacement_len];
    let mut protected_len = merge_reclamation_protected_pages(
        bound.cow.replacements(),
        probe_entries,
        identity.page_count,
        protected_pages,
    )?;

    for _ in 0..=protected_pages.len() {
        let candidate = &protected_pages[..protected_len];
        let preview_replacement_len = Cell::new(0usize);
        let preview_len = bound
            .preview_terminal_replacements_with_stage(
                FreeBitmapFinalizationScratch {
                    release_pages: &mut final_release_pages[..requirements.release_pages],
                    insert_pages: &mut final_insert_pages[..requirements.insert_pages],
                    cached_pages: &mut final_cached_pages[..requirements.cached_pages],
                    index_stack: &mut final_index_stack[..requirements.index_stack],
                    cleanup_nodes: &mut final_cleanup_nodes[..requirements.cleanup_nodes],
                    cleanup_path: &mut final_cleanup_path[..requirements.cleanup_path],
                    cleanup_targets: &mut final_cleanup_targets[..requirements.cleanup_targets],
                },
                preview_bitmap_replacements,
                |reclamation, stage_pool, stage_scope| {
                    let mut arena =
                        PrivatePageArena::from_scoped_pool(stage_pool, stage_scope, pending_txn)?;
                    let blob = RetirementBlobBuilder::build(
                        candidate,
                        &mut arena,
                        &mut BlobBuildScratch::new(preview_blob_pages),
                    )?;
                    let mut replacements = CommittedReplacementLedger::new(preview_replacements);
                    let mut releases = PrivateReleaseBuffer::new(preview_releases);
                    let mut roles = PageRoleIndex::new(preview_roles);
                    let result = RetirementTreeEditor::delete_reclaimed_oldest_and_upsert_newest(
                        pages,
                        state,
                        identity,
                        reclamation,
                        blob,
                        preview_delete_path,
                        preview_upsert_path,
                        &mut replacements,
                        &mut releases,
                        &mut roles,
                    )?;
                    preview_replacement_len.set(replacements.entries().len());
                    Ok::<_, RetirementWriteError>((
                        result,
                        replacement_fingerprint(replacements.entries()),
                        arena.in_use_count()?,
                    ))
                },
            )
            .map_err(|error| match error {
                FreeBitmapFinalizationPreviewError::Bitmap(error) => {
                    ReclamationProtectedPagesError::Bitmap(error)
                }
                FreeBitmapFinalizationPreviewError::Stage(error) => {
                    ReclamationProtectedPagesError::Retirement(error)
                }
            })?;
        if preview_replacement_len.get() != probe_replacement_len
            || preview_replacements[..probe_replacement_len] != *probe_entries
        {
            return Err(ReclamationProtectedPagesError::ProbeChanged);
        }
        let next_len = merge_reclamation_protected_pages(
            &preview_bitmap_replacements[..preview_len],
            probe_entries,
            identity.page_count,
            next_protected_pages,
        )?;
        if next_protected_pages[..next_len] == candidate[..] {
            let (blob_private_pages, retirement_private_page_budget) =
                selected_reclamation_retirement_private_page_budget(
                    protected_len,
                    tree_private_page_budget,
                )?;
            let available_private_pages = bound.cow.available_private_pages();
            require_selected_reclamation_retirement_capacity(
                retirement_private_page_budget,
                available_private_pages,
            )?;
            return Ok(SelectedReclamationProtectedPages {
                pages: &protected_pages[..protected_len],
                blob_private_pages,
                tree_private_page_budget,
                retirement_private_page_budget,
            });
        }
        if next_len > protected_pages.len() {
            return Err(ReclamationProtectedPagesError::Retirement(
                RetirementWriteError::ReplacementLedgerTooSmall {
                    required: next_len,
                    actual: protected_pages.len(),
                },
            ));
        }
        protected_pages[..next_len].copy_from_slice(&next_protected_pages[..next_len]);
        protected_len = next_len;
    }
    Err(ReclamationProtectedPagesError::FixedPointDidNotConverge {
        limit: protected_pages.len().saturating_add(1),
    })
}

/// Materializes the selected reclaim's retirement blob/tree output inside the
/// exact shadow scope already bound to `bound`.
///
/// The scope and source are not caller-selectable: the scope is checked against
/// the bound bitmap reservation and the source/identity come from its verified
/// reclamation authority. Errors before the edit is applied leave the shadow
/// attempt reusable; later errors explicitly report that the caller must
/// discard this isolated attempt before retrying the outer transaction.
#[allow(clippy::result_large_err, clippy::too_many_arguments)]
pub(crate) fn stage_selected_reclamation_retirement<
    'terminal,
    'scratch,
    'bound,
    'pool,
    'barrier,
    'pages,
    S: CommittedPageSource + ?Sized,
>(
    bound: &mut BoundFreeBitmapReservation<'bound, 'pool, 'pool, 'barrier, 'pages, S>,
    shadow_pool: &'pool PrivatePagePool<'pool>,
    shadow_scope: &PrivatePageReservationScope<'pool>,
    protected: SelectedReclamationProtectedPages<'_>,
    scratch: SelectedReclamationRetirementScratch<'terminal, 'scratch>,
) -> Result<PreparedRetirementTerminalExport<'terminal>, SelectedReclamationRetirementStageError> {
    let SelectedReclamationRetirementScratch {
        blob_pages,
        delete_path,
        upsert_path,
        replacements,
        releases,
        roles,
        terminal_pages,
    } = scratch;
    if blob_pages.len() < protected.blob_private_pages() {
        return Err(
            SelectedReclamationRetirementStageError::BlobScratchTooSmall {
                required: protected.blob_private_pages(),
                actual: blob_pages.len(),
            },
        );
    }
    if terminal_pages.len() < protected.retirement_private_page_budget() {
        return Err(
            SelectedReclamationRetirementStageError::TerminalPagesTooSmall {
                required: protected.retirement_private_page_budget(),
                actual: terminal_pages.len(),
            },
        );
    }
    if terminal_pages[..protected.retirement_private_page_budget()]
        .iter()
        .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
    {
        return Err(SelectedReclamationRetirementStageError::TerminalPagesNotEmpty);
    }
    bound
        .validate_reclamation_scope(shadow_scope)
        .map_err(SelectedReclamationRetirementStageError::PreMutationBitmap)?;
    let identity = bound
        .reclamation_authority()
        .identity()
        .ok_or(SelectedReclamationRetirementStageError::MissingSelectedIdentity)?;
    let pending_txn = identity.txn_id.checked_add(1).ok_or(
        SelectedReclamationRetirementStageError::PreMutationRetirement(
            RetirementWriteError::SelectedTransactionOverflow(identity.txn_id),
        ),
    )?;
    let state = RetirementTreeState {
        selected_txn: identity.txn_id,
        page_count: identity.page_count,
        root: identity.root,
        batch_count: identity.batch_count,
    };
    let source = bound.reclamation_source();
    let mut arena = PrivatePageArena::from_scoped_pool(shadow_pool, shadow_scope, pending_txn)
        .map_err(SelectedReclamationRetirementStageError::PreMutationRetirement)?;
    let blob = RetirementBlobBuilder::build(
        protected.pages(),
        &mut arena,
        &mut BlobBuildScratch::new(blob_pages),
    )
    .map_err(SelectedReclamationRetirementStageError::PreMutationRetirement)?;
    let mut replacements = CommittedReplacementLedger::new(replacements);
    let mut releases = PrivateReleaseBuffer::new(releases);
    let mut roles = PageRoleIndex::new(roles);
    let result = RetirementTreeEditor::delete_reclaimed_oldest_and_upsert_newest(
        source,
        state,
        identity,
        bound.reclamation_authority(),
        blob,
        delete_path,
        upsert_path,
        &mut replacements,
        &mut releases,
        &mut roles,
    )
    .map_err(SelectedReclamationRetirementStageError::PreMutationRetirement)?;
    let terminal_page_count = protected
        .blob_private_pages()
        .checked_add(result.private_pages)
        .ok_or(SelectedReclamationRetirementStageError::PostMutationTerminalPageCountOverflow)?;
    if terminal_page_count > protected.retirement_private_page_budget() {
        return Err(
            SelectedReclamationRetirementStageError::PostMutationCapacityMismatch {
                actual: terminal_page_count,
                budget: protected.retirement_private_page_budget(),
            },
        );
    }
    let export = arena
        .prepare_terminal_export(result, &mut terminal_pages[..terminal_page_count])
        .map_err(SelectedReclamationRetirementStageError::PostMutationRetirement)?;
    bound
        .synchronize_reclamation_scope(shadow_scope)
        .map_err(SelectedReclamationRetirementStageError::PostMutationBitmap)?;
    Ok(export)
}

/// Completes the mutable selected-reclaim terminal sequence in one isolated
/// shadow attempt. Retryable failures happen before the retirement edit; every
/// later failure consumes the attempt because the shared shadow scope changed.
#[allow(
    clippy::result_large_err,
    clippy::too_many_arguments,
    clippy::type_complexity
)]
pub(crate) fn finalize_selected_reclamation_terminal_export<
    'retirement_terminal,
    'retirement_scratch,
    'bitmap_terminal,
    'bitmap_scratch,
    'combined,
    'a,
    'slots,
    'barrier,
    S: CommittedPageSource + ?Sized,
>(
    reservation: LockedReclamationBitmapReservation<'a, 'slots, 'slots, 'barrier, S>,
    shadow_pool: &'slots PrivatePagePool<'slots>,
    shadow_scope: &PrivatePageReservationScope<'slots>,
    protected: SelectedReclamationProtectedPages<'_>,
    scratch: SelectedReclamationTerminalScratch<
        'retirement_terminal,
        'retirement_scratch,
        'bitmap_terminal,
        'bitmap_scratch,
        'combined,
    >,
) -> Result<
    PreparedSelectedReclamationTerminalExport<
        'combined,
        'bitmap_terminal,
        'bitmap_scratch,
        'a,
        'slots,
        'slots,
        S,
    >,
    SelectedReclamationTerminalCompositionFailure<'a, 'slots, 'slots, 'barrier, S>,
> {
    if let Err(error) = reservation.bound.validate_reclamation_scope(shadow_scope) {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::PreMutationBitmap(error),
        });
    }
    let scope_capacity = match shadow_pool.scope_status(shadow_scope) {
        Ok(status) => status.capacity,
        Err(error) => {
            return Err(SelectedReclamationTerminalCompositionFailure::Retry {
                reservation,
                error: SelectedReclamationTerminalCompositionError::PreMutationBitmap(
                    FreeBitmapCowError::PrivatePool(error),
                ),
            });
        }
    };
    if scratch.bitmap_terminal_pages.len() < scope_capacity {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::BitmapTerminalPagesTooSmall {
                required: scope_capacity,
                actual: scratch.bitmap_terminal_pages.len(),
            },
        });
    }
    if scratch.combined_terminal_pages.len() < scope_capacity {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::CombinedTerminalPagesTooSmall {
                required: scope_capacity,
                actual: scratch.combined_terminal_pages.len(),
            },
        });
    }
    if scratch.bitmap_terminal_pages[..scope_capacity]
        .iter()
        .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
    {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::BitmapTerminalPagesNotEmpty,
        });
    }
    if scratch.combined_terminal_pages[..scope_capacity]
        .iter()
        .any(|page| *page != PrivatePageCoordinatorTerminalPage::empty())
    {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::CombinedTerminalPagesNotEmpty,
        });
    }
    if let Err(error) = reservation
        .bound
        .preflight_terminal_finalization_scratch(&scratch.bitmap_finalization)
    {
        return Err(SelectedReclamationTerminalCompositionFailure::Retry {
            reservation,
            error: SelectedReclamationTerminalCompositionError::PreMutationBitmap(error),
        });
    }

    let LockedReclamationBitmapReservation { pass, mut bound } = reservation;
    let SelectedReclamationTerminalScratch {
        retirement,
        bitmap_finalization,
        bitmap_terminal_pages,
        combined_terminal_pages,
    } = scratch;
    let retirement = match stage_selected_reclamation_retirement(
        &mut bound,
        shadow_pool,
        shadow_scope,
        protected,
        retirement,
    ) {
        Ok(export) => export,
        Err(error) if error.is_pre_mutation() => {
            return Err(SelectedReclamationTerminalCompositionFailure::Retry {
                reservation: LockedReclamationBitmapReservation { pass, bound },
                error: SelectedReclamationTerminalCompositionError::RetirementStage(error),
            });
        }
        Err(error) => {
            return Err(SelectedReclamationTerminalCompositionFailure::Discard {
                error: SelectedReclamationTerminalCompositionError::RetirementStage(error),
            });
        }
    };
    let finalized = match bound.finalize(bitmap_finalization) {
        Ok(finalized) => finalized,
        Err((_bound, error)) => {
            return Err(SelectedReclamationTerminalCompositionFailure::Discard {
                error: SelectedReclamationTerminalCompositionError::PostMutationBitmapFinalization(
                    error,
                ),
            });
        }
    };
    let bitmap_root = finalized.output.root();
    let pending_page_count = finalized.output.pending_page_count();
    let bitmap_terminal_page_count = finalized.output.bitmap_terminal_page_count();
    if bitmap_terminal_page_count > scope_capacity {
        return Err(SelectedReclamationTerminalCompositionFailure::Discard {
            error: SelectedReclamationTerminalCompositionError::PostMutationBitmapTerminalPageCountExceedsScope {
                actual: bitmap_terminal_page_count,
                capacity: scope_capacity,
            },
        });
    }
    let bitmap_export = match finalized.output.prepare_terminal_export(
        finalized.successor,
        &mut bitmap_terminal_pages[..bitmap_terminal_page_count],
    ) {
        Ok(export) => export,
        Err((_output, _successor, _pages, error)) => {
            return Err(SelectedReclamationTerminalCompositionFailure::Discard {
                error: SelectedReclamationTerminalCompositionError::PostMutationBitmapExport(error),
            });
        }
    };
    let combined_terminal_page_count = match retirement
        .pages()
        .len()
        .checked_add(bitmap_export.pages().len())
    {
        Some(count) => count,
        None => {
            return Err(SelectedReclamationTerminalCompositionFailure::Discard {
                error: SelectedReclamationTerminalCompositionError::PostMutationCombinedTerminalPageCountOverflow,
            });
        }
    };
    if combined_terminal_page_count > scope_capacity {
        return Err(SelectedReclamationTerminalCompositionFailure::Discard {
            error: SelectedReclamationTerminalCompositionError::PostMutationCombinedTerminalPageCountExceedsScope {
                actual: combined_terminal_page_count,
                capacity: scope_capacity,
            },
        });
    }
    let retirement_result = retirement.result();
    let export = match retirement.merge_with_bitmap_export(
        bitmap_export,
        &mut combined_terminal_pages[..combined_terminal_page_count],
    ) {
        Ok(export) => export,
        Err((_retirement, _bitmap, _pages, error)) => {
            return Err(SelectedReclamationTerminalCompositionFailure::Discard {
                error: SelectedReclamationTerminalCompositionError::PostMutationMerge(error),
            });
        }
    };
    Ok(PreparedSelectedReclamationTerminalExport {
        pass,
        bitmap_root,
        pending_page_count,
        retirement: retirement_result,
        export,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bitmap_cow::{FreeBitmapReclamationTicket, FreeBitmapReservationStageBuffers};
    use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
    use crate::page_source::PageSourceError;
    use crate::private_page_pool::{PrivatePagePoolSlot, PrivatePagePoolState};
    use crate::retirement_reader::RetirementReclaimBarrier;
    use crate::test_alloc::count_thread_allocations;
    use core::cell::Cell;

    #[derive(Debug)]
    struct TestBarrier;

    impl RetirementReclaimBarrier for TestBarrier {}

    static TEST_BARRIER: TestBarrier = TestBarrier;

    #[derive(Debug)]
    struct RejectingSource {
        calls: Cell<usize>,
    }

    impl CommittedPageSource for RejectingSource {
        fn check_access(&self) -> Result<(), PageSourceError> {
            self.calls.set(self.calls.get() + 1);
            Err(PageSourceError::ForkedHandle)
        }

        fn read_page(&self, _: u32, _: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
            self.calls.set(self.calls.get() + 1);
            Err(PageSourceError::ForkedHandle)
        }
    }

    fn selected_meta() -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            txn_id: 1,
            commit_nonce: [2; 16],
            page_count: 2,
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
            free_bitmap_root: 0,
            retirement_root: 0,
        }
    }

    #[test]
    fn invalid_limits_fail_before_source_access_or_shadow_mutation() {
        let source = RejectingSource {
            calls: Cell::new(0),
        };
        let mut slots = [PrivatePagePoolSlot::empty()];
        let pool = PrivatePagePool::new_vacant(&mut slots, 2, 2, 2).unwrap();
        let scope = pool.reserve_scope(1).unwrap();
        let before = pool.exact_commitment(&scope).unwrap();

        let mut arena = [];
        let mut pool_validation = [];
        let mut arena_bindings = [];
        let mut candidates = [];
        let mut verified_bitmap_pages = [];
        let mut replacements = [];
        let mut index_nodes = [];
        let mut available_slots = [];
        let mut source_nodes = [];
        let reclamation = FreeBitmapReclamationTicket::new();
        let mut stage_arena = [];
        let mut stage_bindings = [];
        let mut stage_candidates = [];
        let mut stage_verified = [];
        let mut stage_replacements = [];
        let mut stage_index = [];
        let mut stage_available = [];
        let mut verified_batches = [];
        let mut verified_pages = [];

        let result = prepare_locked_reclamation_bitmap_reservation(
            selected_meta(),
            &source,
            RetirementReclaimFence::from_stable_reader_table(&TEST_BARRIER, 0, None),
            LockedReclamationFinalizerLimits {
                max_batches: 0,
                max_pages: 1,
                bitmap_payload_pages: 1,
            },
            LockedReclamationFinalizerScratch {
                bitmap: FreeBitmapReservationBuffers {
                    arena: &mut arena,
                    pool_validation: &mut pool_validation,
                    arena_bindings: &mut arena_bindings,
                    candidates: &mut candidates,
                    verified_pages: &mut verified_bitmap_pages,
                    replacements: &mut replacements,
                    index_nodes: &mut index_nodes,
                    available_slots: &mut available_slots,
                    source_nodes: &mut source_nodes,
                    reclamation: &reclamation,
                    stage: FreeBitmapReservationStageBuffers {
                        arena: &mut stage_arena,
                        arena_bindings: &mut stage_bindings,
                        candidates: &mut stage_candidates,
                        verified_pages: &mut stage_verified,
                        replacements: &mut stage_replacements,
                        index_nodes: &mut stage_index,
                        available_slots: &mut stage_available,
                    },
                },
                verified_batches: &mut verified_batches,
                verified_pages: &mut verified_pages,
            },
            &pool,
            &scope,
        );

        assert!(matches!(
            result,
            Err(LockedReclamationFinalizerError::InvalidLimits)
        ));
        assert_eq!(source.calls.get(), 0);
        assert!(pool.validate_exact_commitment(&scope, &before).is_ok());
        assert_eq!(
            pool.scoped_slot_info(&scope, 0).unwrap().unwrap().state,
            PrivatePagePoolState::Vacant
        );
    }

    #[test]
    fn no_change_stops_before_bitmap_planning_or_shadow_binding() {
        let source = RejectingSource {
            calls: Cell::new(0),
        };
        let mut arena = [];
        let mut pool_validation = [];
        let mut arena_bindings = [];
        let mut candidates = [];
        let mut verified_bitmap_pages = [];
        let mut replacements = [];
        let mut index_nodes = [];
        let mut available_slots = [];
        let mut source_nodes = [];
        let reclamation = FreeBitmapReclamationTicket::new();
        let mut stage_arena = [];
        let mut stage_bindings = [];
        let mut stage_candidates = [];
        let mut stage_verified = [];
        let mut stage_replacements = [];
        let mut stage_index = [];
        let mut stage_available = [];
        let mut verified_batches = [];
        let mut verified_pages = [];

        let (result, allocations) = count_thread_allocations(|| {
            plan_locked_reclamation_bitmap_reservation(
                selected_meta(),
                &source,
                RetirementReclaimFence::from_stable_reader_table(&TEST_BARRIER, 0, None),
                LockedReclamationFinalizerLimits {
                    max_batches: 1,
                    max_pages: 1,
                    bitmap_payload_pages: 1,
                },
                LockedReclamationFinalizerScratch {
                    bitmap: FreeBitmapReservationBuffers {
                        arena: &mut arena,
                        pool_validation: &mut pool_validation,
                        arena_bindings: &mut arena_bindings,
                        candidates: &mut candidates,
                        verified_pages: &mut verified_bitmap_pages,
                        replacements: &mut replacements,
                        index_nodes: &mut index_nodes,
                        available_slots: &mut available_slots,
                        source_nodes: &mut source_nodes,
                        reclamation: &reclamation,
                        stage: FreeBitmapReservationStageBuffers {
                            arena: &mut stage_arena,
                            arena_bindings: &mut stage_bindings,
                            candidates: &mut stage_candidates,
                            verified_pages: &mut stage_verified,
                            replacements: &mut stage_replacements,
                            index_nodes: &mut stage_index,
                            available_slots: &mut stage_available,
                        },
                    },
                    verified_batches: &mut verified_batches,
                    verified_pages: &mut verified_pages,
                },
            )
        });

        assert!(matches!(
            result.unwrap(),
            LockedReclamationBitmapPlanOutcome::NoChange
        ));
        assert_eq!(allocations, 0);
        assert_eq!(source.calls.get(), 0);
    }

    fn replacement(pgno: u32) -> CommittedPageReplacement {
        CommittedPageReplacement {
            pgno,
            origin: crate::retirement_writer::CommittedPageOrigin::RetirementTree,
        }
    }

    #[test]
    fn protected_page_merge_is_sorted_unique_and_bounded() {
        let mut output = [0u32; 4];
        let length = merge_reclamation_protected_pages(
            &[9, 4],
            &[replacement(4), replacement(8)],
            10,
            &mut output,
        )
        .unwrap();

        assert_eq!(&output[..length], &[4, 8, 9]);
    }

    #[test]
    fn protected_page_merge_rejects_short_or_invalid_input_without_panicking() {
        let mut short = [0u32; 1];
        assert!(matches!(
            merge_reclamation_protected_pages(&[4], &[replacement(8)], 10, &mut short),
            Err(ReclamationProtectedPagesError::Retirement(
                RetirementWriteError::ReplacementLedgerTooSmall {
                    required: 2,
                    actual: 1,
                }
            ))
        ));

        let mut output = [0u32; 2];
        assert!(matches!(
            merge_reclamation_protected_pages(&[1], &[replacement(8)], 10, &mut output),
            Err(ReclamationProtectedPagesError::ReplacementPageOutOfBounds(
                1
            ))
        ));
        assert!(matches!(
            merge_reclamation_protected_pages(&[4], &[replacement(10)], 10, &mut output),
            Err(ReclamationProtectedPagesError::ReplacementPageOutOfBounds(
                10
            ))
        ));
    }

    #[test]
    fn selected_reclamation_budget_combines_exact_blob_and_tree_capacity() {
        assert_eq!(
            selected_reclamation_retirement_private_page_budget(3, 1).unwrap(),
            (1, 2)
        );
    }

    #[test]
    fn selected_reclamation_budget_rejects_a_short_bound_scope_before_mutation() {
        assert!(matches!(
            require_selected_reclamation_retirement_capacity(3, 2),
            Err(ReclamationProtectedPagesError::Retirement(
                RetirementWriteError::PrivatePageBudgetTooSmall {
                    required: 3,
                    actual: 2,
                }
            ))
        ));
    }
}
