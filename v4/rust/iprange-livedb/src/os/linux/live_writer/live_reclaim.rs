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
    FreeBitmapReservationStageBuffers, VerifiedBitmapPage,
};
use crate::private_page_pool::{
    PrivatePageCompositeBind, PrivatePageCoordinatorTerminalPage, PrivatePagePool,
    PrivatePagePoolSlot, PrivatePagePreparedScopeSlot, PrivatePageSelectiveOverlayNode,
    PrivatePageSelectivePathEntry,
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
use crate::retirement_writer::{CommittedPageReplacement, PageRoleIndexSlot, RetirementPathFrame};
use crate::writer_fixed_point::{FixedPointCoordinatorWorkspace, FixedPointPreparedWorkSlot};
use crate::writer_transaction_contract::{PrivateCleanupEntry, PrivateWriterResourceBudget};

/// Bounded work limits for one internal clean-writer Reclaim attempt.
///
/// This remains crate-private while the SDK-owned workspace is being wired.
/// No caller outside the crate can construct a public Reclaim operation from
/// these raw implementation limits.
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

/// All bounded backing needed by one crate-private clean-writer Reclaim.
///
/// The operation borrows this as one unit and never lets it escape the held
/// Linux operation barrier.  This is deliberately an internal staging type:
/// the later SDK workspace will own the same logical partitions instead of
/// allowing an SDK caller to manage these individual arrays.
pub(crate) struct LinuxLiveWriterReclaimScratch<'a> {
    /// Exact live transaction pages, used only after selected terminal output
    /// establishes their required count.
    pub(crate) live_slots: &'a mut [PrivatePagePoolSlot],
    pub(crate) cleanup_entries:
        &'a mut [Option<PrivateCleanupEntry<(), (), LinuxLiveWriterPageSinkError>>],
    pub(crate) work_slot: &'a mut FixedPointPreparedWorkSlot,
    pub(crate) scope_slot: &'a mut PrivatePagePreparedScopeSlot,
    pub(crate) preparation_scratch: &'a mut [u8],

    // Lock-held bitmap selection and reservation planning.
    pub(crate) planner_arena: &'a mut [PrivatePagePoolSlot],
    pub(crate) planner_pool_validation: &'a mut [PrivatePageCompositeBind],
    pub(crate) planner_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) planner_candidates: &'a mut [u32],
    pub(crate) planner_verified: &'a mut [VerifiedBitmapPage],
    pub(crate) planner_replacements: &'a mut [u32],
    pub(crate) planner_index: &'a mut [BitmapCowIndexNode],
    pub(crate) planner_available: &'a mut [usize],
    pub(crate) planner_source_nodes: &'a mut [FreeBitmapReservationSourceNode],
    pub(crate) reclamation_ticket: &'a FreeBitmapReclamationTicket,
    pub(crate) stage_arena: &'a mut [PrivatePagePoolSlot],
    pub(crate) stage_bindings: &'a mut [BitmapCowArenaBinding],
    pub(crate) stage_candidates: &'a mut [u32],
    pub(crate) stage_verified: &'a mut [VerifiedBitmapPage],
    pub(crate) stage_replacements: &'a mut [u32],
    pub(crate) stage_index: &'a mut [BitmapCowIndexNode],
    pub(crate) stage_available: &'a mut [usize],
    pub(crate) verified_batches: &'a mut [RetirementBatch],
    pub(crate) verified_pages: &'a mut [u32],

    // An isolated pool rebuilt only after selection has supplied exact size.
    pub(crate) shadow_slots: &'a mut [PrivatePagePoolSlot],

    // Read-only selected-reclaim fixed-point preview.
    pub(crate) probe_delete_path: &'a mut [RetirementPathFrame],
    pub(crate) probe_upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) probe_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) probe_releases: &'a mut [u32],
    pub(crate) probe_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) protected_pages: &'a mut [u32],
    pub(crate) next_protected_pages: &'a mut [u32],
    pub(crate) preview_bitmap_replacements: &'a mut [u32],
    pub(crate) preview_blob_pages: &'a mut [u32],
    pub(crate) preview_delete_path: &'a mut [RetirementPathFrame],
    pub(crate) preview_upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) preview_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) preview_releases: &'a mut [u32],
    pub(crate) preview_roles: &'a mut [PageRoleIndexSlot],

    // Final bitmap and retirement terminal construction.
    pub(crate) final_release_pages: &'a mut [u32],
    pub(crate) final_insert_pages: &'a mut [FreeBitmapInsertPage],
    pub(crate) final_cached_pages: &'a mut [FreeBitmapFinalizationCachedPage],
    pub(crate) final_index_stack: &'a mut [usize],
    pub(crate) final_cleanup_nodes: &'a mut [PrivatePageSelectiveOverlayNode],
    pub(crate) final_cleanup_path: &'a mut [PrivatePageSelectivePathEntry],
    pub(crate) final_cleanup_targets: &'a mut [usize],
    pub(crate) terminal_blob_pages: &'a mut [u32],
    pub(crate) terminal_delete_path: &'a mut [RetirementPathFrame],
    pub(crate) terminal_upsert_path: &'a mut [RetirementPathFrame],
    pub(crate) terminal_replacements: &'a mut [CommittedPageReplacement],
    pub(crate) terminal_releases: &'a mut [u32],
    pub(crate) terminal_roles: &'a mut [PageRoleIndexSlot],
    pub(crate) retirement_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) bitmap_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
    pub(crate) combined_terminal_pages: &'a mut [PrivatePageCoordinatorTerminalPage],
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
    /// Runs one self-contained, clean-writer Reclaim using only preallocated
    /// crate-private backing.
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
    pub(crate) fn reclaim_with_private_scratch<'slots, 'backing, 'arena, 'record_cleanup>(
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
