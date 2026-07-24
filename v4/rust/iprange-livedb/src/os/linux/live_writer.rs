//! Private Linux live-writer lifecycle over retained descriptors.

// Cleanup authority stays inline so an error after lease publication never
// needs a fallible heap allocation merely to return the retained descriptors.
#![allow(clippy::large_enum_variant, clippy::result_large_err)]

use super::live_cleanup::{
    requires_cleanup, retry_any_cleanup, LinuxLiveCleanupError, LinuxLiveCleanupGuard,
    OwnedLiveClaim,
};
use super::*;
use crate::retirement_reader::{RetirementReclaimBarrier, RetirementReclaimFence};
use crate::writer_fixed_point::FixedPointCoordinatorWorkspace;
use crate::writer_transaction_core::{
    PrivateWriterMetaPublication, PrivateWriterTransactionCore, PrivateWriterTransactionError,
    PrivateWriterTransactionHandle,
};
use std::sync::{Mutex, MutexGuard};

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterOpenCause {
    Pair(LinuxLivePairError),
    Lease(LinuxWriterLeaseError),
    Cancelled,
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterOpenError {
    Failed {
        cause: LinuxLiveWriterOpenCause,
        cleanup_outcome: Option<LiveClaimCleanupOutcome>,
    },
    CleanupRequired {
        cause: LinuxLiveWriterOpenCause,
        cleanup: LinuxLiveCleanupError,
        guard: LinuxLiveCleanupGuard,
    },
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterCloseError {
    ForkedHandle,
    OperationBarrierHeld,
    Cleanup(LinuxWriterLeaseError),
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterBarrierCause {
    ForkedHandle,
    CleanupOnly,
    Closed,
    AlreadyHeld,
    Cancelled,
    Os(LinuxOsError),
    Pair(LinuxLivePairError),
    Lease(LinuxWriterLeaseError),
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterBarrierError<'a> {
    Failed(LinuxLiveWriterBarrierCause),
    Locked {
        cause: LinuxLiveWriterBarrierCause,
        barrier: LinuxLiveWriterOperationBarrier<'a>,
    },
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterBarrierReleaseError {
    ForkedHandle,
    NotHeld,
    Os(LinuxOsError),
}

/// Bounded page-output errors inside one already-recorded publication attempt.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterPageSinkError {
    PageOutOfBounds { pgno: u32, page_count: u64 },
    PageLength { actual: usize },
    OffsetOverflow,
    Os(LinuxOsError),
}

/// The immediate cause of a physical-publication failure.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterPublicationCause<E> {
    Sink(E),
    Os(LinuxOsError),
    Lease(LinuxWriterLeaseError),
}

/// Phase-classified physical-publication failure.
///
/// Only `Preflight` leaves the established writer reusable. Every other
/// variant owns a recorded attempt or a potentially durable meta page. The
/// publisher transitions that writer to explicit close-only cleanup before it
/// returns the error.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterPublicationError<E> {
    Preflight(LinuxWriterLeaseError),
    NotCommitted(LinuxLiveWriterPublicationCause<E>),
    OutcomeUnknown(LinuxLiveWriterPublicationCause<E>),
    Committed(LinuxLiveWriterPublicationCause<E>),
}

/// Exact outcome of connecting a fixed-point transaction core to one Linux
/// physical publication attempt.
///
/// `CoreAfterDurablePublication` and
/// `MissingCorePublicationAuthority` are always committed outcomes. Their
/// optional phase-five cause is present only when the target meta was durable
/// but the writer-lease update failed afterward.
#[derive(Debug)]
pub(crate) enum LinuxLiveWriterCoreCommitError<E> {
    Barrier(LinuxLiveWriterBarrierCause),
    Core(PrivateWriterTransactionError<E>),
    CoreRelease {
        core: PrivateWriterTransactionError<E>,
        release: LinuxLiveWriterBarrierReleaseError,
    },
    Publication(LinuxLiveWriterPublicationError<PrivateWriterTransactionError<E>>),
    PublicationRelease {
        publication: LinuxLiveWriterPublicationError<PrivateWriterTransactionError<E>>,
        release: LinuxLiveWriterBarrierReleaseError,
    },
    CoreAfterDurablePublication {
        phase_five: Option<LinuxLiveWriterPublicationCause<PrivateWriterTransactionError<E>>>,
        core: PrivateWriterTransactionError<E>,
    },
    CoreAfterOutcomeUnknown {
        publication: LinuxLiveWriterPublicationCause<PrivateWriterTransactionError<E>>,
        core: PrivateWriterTransactionError<E>,
    },
    MissingCorePublicationAuthority {
        phase_five: Option<LinuxLiveWriterPublicationCause<PrivateWriterTransactionError<E>>>,
    },
    ReleaseAfterDurablePublication(LinuxLiveWriterBarrierReleaseError),
}

impl<E> LinuxLiveWriterPublicationError<E> {
    pub(crate) const fn requires_close_only(&self) -> bool {
        !matches!(self, Self::Preflight(_))
    }
}

/// Restricted writer for the non-meta pages of one target generation.
///
/// It deliberately cannot write either meta page and cannot address a page
/// outside the target's exact committed length.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriterPageSink<'a> {
    main: &'a RetainedRegular,
    target_page_count: u64,
}

impl LinuxLiveWriterPageSink<'_> {
    pub(crate) fn write_page(
        &mut self,
        pgno: u32,
        bytes: &[u8],
    ) -> Result<(), LinuxLiveWriterPageSinkError> {
        if bytes.len() != PAGE_SIZE {
            return Err(LinuxLiveWriterPageSinkError::PageLength {
                actual: bytes.len(),
            });
        }
        if pgno < 2 || u64::from(pgno) >= self.target_page_count {
            return Err(LinuxLiveWriterPageSinkError::PageOutOfBounds {
                pgno,
                page_count: self.target_page_count,
            });
        }
        let offset = u64::from(pgno)
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(LinuxLiveWriterPageSinkError::OffsetOverflow)?;
        self.main
            .write_all_at(bytes, offset)
            .map_err(LinuxLiveWriterPageSinkError::Os)
    }
}

/// A phase-4-confirmed target that still owns the operation barrier.
///
/// The caller must either release it normally after the transaction core has
/// accepted success, or make it close-only if later in-memory completion fails.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriterPublication<'a> {
    barrier: LinuxLiveWriterOperationBarrier<'a>,
    target: Bootstrap,
}

impl LinuxLiveWriterPublication<'_> {
    pub(crate) const fn target(&self) -> Bootstrap {
        self.target
    }

    pub(crate) fn release(self) -> Result<(), (Self, LinuxLiveWriterBarrierReleaseError)> {
        self.release_with(RetainedRegular::release_lock)
    }

    fn release_with(
        mut self,
        release: impl FnOnce(&mut RetainedRegular) -> Result<(), LinuxOsError>,
    ) -> Result<(), (Self, LinuxLiveWriterBarrierReleaseError)> {
        match self.barrier.release_with(release) {
            Ok(()) => Ok(()),
            Err(error) => {
                self.barrier.force_close_only_after_publication();
                Err((self, error))
            }
        }
    }

    pub(crate) fn into_close_only(self) -> Result<(), (Self, LinuxLiveWriterBarrierReleaseError)> {
        let Self { barrier, target } = self;
        match barrier.into_close_only() {
            Ok(()) => Ok(()),
            Err((barrier, error)) => Err((Self { barrier, target }, error)),
        }
    }

    fn force_close_only(mut self) {
        self.barrier.force_close_only_after_publication();
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct LinuxLiveWriterReaderFacts {
    registering_readers: u32,
    oldest_reader_txn: Option<u64>,
}

#[derive(Debug)]
pub(crate) enum LinuxLiveWriterFinalizationContextError {
    ReaderFactsUnavailable,
    Source(PageSourceError),
}

/// Exact selected-generation state usable only while one operation barrier is
/// held. The higher-ranked callback on the barrier prevents this context, its
/// page source, and its reclaim fence from escaping into later unlocked work.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriterFinalizationContext<'a> {
    selected: Bootstrap,
    pages: PinnedPageSource<'a, RetainedRegular>,
    reclaim_fence: RetirementReclaimFence<'a>,
}

impl<'a> LinuxLiveWriterFinalizationContext<'a> {
    pub(crate) fn into_parts(
        self,
    ) -> (
        Bootstrap,
        PinnedPageSource<'a, RetainedRegular>,
        RetirementReclaimFence<'a>,
    ) {
        (self.selected, self.pages, self.reclaim_fence)
    }
}

#[derive(Debug)]
pub(crate) struct LinuxLiveWriterOperationBarrier<'a> {
    inner: MutexGuard<'a, LinuxLiveWriterInner>,
    creator_pid: u32,
    protection: Option<LinuxLiveWriterReaderFacts>,
}

impl RetirementReclaimBarrier for LinuxLiveWriterOperationBarrier<'_> {}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WriterState {
    Open,
    OperationLocked,
    CleanupOnly,
    Closed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum OpenStage {
    DeadWriterFound,
    ScanComplete,
    ClaimPublished,
    BeforeTailCleanup,
}

/// Established private live writer. Transaction mutation and commit are
/// deliberately outside this lifecycle slice.
#[derive(Debug)]
pub(crate) struct LinuxLiveWriter {
    inner: Mutex<LinuxLiveWriterInner>,
    creator_pid: u32,
}

#[derive(Debug)]
struct LinuxLiveWriterInner {
    files: Option<RetainedLiveFiles>,
    owned: Option<OwnedWriterLease>,
    bootstrap: Bootstrap,
    state: WriterState,
}

impl LinuxLiveWriter {
    pub(crate) fn open(path: &Path) -> Result<Self, LinuxLiveWriterOpenError> {
        Self::open_with_cancel(path, || false)
    }

    pub(crate) fn open_with_cancel(
        path: &Path,
        cancelled: impl FnMut() -> bool,
    ) -> Result<Self, LinuxLiveWriterOpenError> {
        Self::open_with_hook_and_io(
            path,
            cancelled,
            |_, _, _| Ok(()),
            |file, length| file.set_len(length),
            |file| file.sync_all(),
        )
    }

    fn open_with_hook(
        path: &Path,
        cancelled: impl FnMut() -> bool,
        hook: impl FnMut(
            OpenStage,
            &mut RetainedLiveFiles,
            Option<&OwnedWriterLease>,
        ) -> Result<(), LinuxLiveWriterOpenCause>,
    ) -> Result<Self, LinuxLiveWriterOpenError> {
        Self::open_with_hook_and_io(
            path,
            cancelled,
            hook,
            |file, length| file.set_len(length),
            |file| file.sync_all(),
        )
    }

    fn open_with_hook_and_io(
        path: &Path,
        mut cancelled: impl FnMut() -> bool,
        mut hook: impl FnMut(
            OpenStage,
            &mut RetainedLiveFiles,
            Option<&OwnedWriterLease>,
        ) -> Result<(), LinuxLiveWriterOpenCause>,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<Self, LinuxLiveWriterOpenError> {
        let mut files =
            RetainedLiveFiles::open_locked_with_cancel(path, &mut cancelled).map_err(|cause| {
                LinuxLiveWriterOpenError::Failed {
                    cause: pair_open_cause(cause),
                    cleanup_outcome: None,
                }
            })?;

        loop {
            match files.scan_and_reap_with_cancel(observe_posix_process, &mut cancelled) {
                Ok(_) => break,
                Err(cause @ LinuxLivePairError::Scan(LinuxSidecarScanError::DeadWriter { .. })) => {
                    if let Err(hook_cause) = hook(OpenStage::DeadWriterFound, &mut files, None) {
                        return failed_with_possible_cleanup(files, None, hook_cause, None);
                    }
                    if cancelled() {
                        return failed_with_possible_cleanup(
                            files,
                            None,
                            LinuxLiveWriterOpenCause::Cancelled,
                            None,
                        );
                    }
                    match files.retry_dead_writer_cleanup() {
                        Ok(()) => continue,
                        Err(cleanup) => {
                            return failed_with_possible_cleanup(
                                files,
                                None,
                                LinuxLiveWriterOpenCause::Pair(cause),
                                Some(LinuxLiveCleanupError::Pair(cleanup)),
                            );
                        }
                    }
                }
                Err(cause) => {
                    return failed_with_possible_cleanup(files, None, pair_open_cause(cause), None);
                }
            }
        }

        if cancelled() {
            return Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                cleanup_outcome: None,
            });
        }
        if let Err(cause) = hook(OpenStage::ScanComplete, &mut files, None) {
            return Err(LinuxLiveWriterOpenError::Failed {
                cause,
                cleanup_outcome: None,
            });
        }

        let owned = match files.claim_writer_lease() {
            Ok(owned) => owned,
            Err(cause) => {
                return failed_with_possible_cleanup(
                    files,
                    None,
                    LinuxLiveWriterOpenCause::Lease(cause),
                    None,
                );
            }
        };
        if let Err(cause) = hook(OpenStage::ClaimPublished, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }
        if cancelled() {
            return failed_with_possible_cleanup(
                files,
                Some(owned),
                LinuxLiveWriterOpenCause::Cancelled,
                None,
            );
        }
        if let Err(cause) = hook(OpenStage::BeforeTailCleanup, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }

        let bootstrap = match files.prepare_writer_for_exposure_with(&owned, truncate, synchronize)
        {
            Ok(bootstrap) => bootstrap,
            Err(cause) => {
                return failed_with_possible_cleanup(
                    files,
                    Some(owned),
                    LinuxLiveWriterOpenCause::Lease(cause),
                    None,
                );
            }
        };
        Ok(Self {
            inner: Mutex::new(LinuxLiveWriterInner {
                files: Some(files),
                owned: Some(owned),
                bootstrap,
                state: WriterState::Open,
            }),
            creator_pid: std::process::id(),
        })
    }

    pub(crate) fn acquire_operation_barrier_with_cancel(
        &self,
        cancelled: impl FnMut() -> bool,
    ) -> Result<LinuxLiveWriterOperationBarrier<'_>, LinuxLiveWriterBarrierError<'_>> {
        self.acquire_operation_barrier_with(cancelled, observe_posix_process)
    }

    fn acquire_operation_barrier_with(
        &self,
        mut cancelled: impl FnMut() -> bool,
        mut observe: impl FnMut(ActiveSlot) -> PosixProcessObservation,
    ) -> Result<LinuxLiveWriterOperationBarrier<'_>, LinuxLiveWriterBarrierError<'_>> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveWriterBarrierError::Failed(
                LinuxLiveWriterBarrierCause::ForkedHandle,
            ));
        }
        let mut inner = self.lock_inner();
        match inner.state {
            WriterState::Open => {}
            WriterState::OperationLocked => {
                return Err(LinuxLiveWriterBarrierError::Locked {
                    cause: LinuxLiveWriterBarrierCause::AlreadyHeld,
                    barrier: LinuxLiveWriterOperationBarrier::new(inner, self.creator_pid, None),
                });
            }
            WriterState::CleanupOnly => {
                return Err(LinuxLiveWriterBarrierError::Failed(
                    LinuxLiveWriterBarrierCause::CleanupOnly,
                ));
            }
            WriterState::Closed => {
                return Err(LinuxLiveWriterBarrierError::Failed(
                    LinuxLiveWriterBarrierCause::Closed,
                ));
            }
        }

        if let Err(cause) = inner
            .files
            .as_mut()
            .expect("open writer retains files")
            .sidecar
            .acquire_lock_interruptible(LockMode::Exclusive, &mut cancelled)
        {
            let cause = if matches!(cause, LinuxOsError::Cancelled) {
                LinuxLiveWriterBarrierCause::Cancelled
            } else {
                LinuxLiveWriterBarrierCause::Os(cause)
            };
            return Err(LinuxLiveWriterBarrierError::Failed(cause));
        }

        // From here every outcome retains explicit lock authority. Validation
        // errors and cancellation must be followed by release/abort so unlock
        // failures remain visible and retryable.
        inner.state = WriterState::OperationLocked;
        let LinuxLiveWriterInner {
            files,
            owned,
            bootstrap,
            ..
        } = &mut *inner;
        let selected = *bootstrap;
        let owned = owned.as_ref().expect("open writer retains exact lease");
        let files = files.as_mut().expect("open writer retains files");
        let owned_active = owned.active;
        let validation = (|| {
            files
                .verify_owned_writer_operation(owned, selected)
                .map_err(LinuxLiveWriterBarrierCause::Lease)?;
            let inspection = files
                .scan_and_reap_with_cancel(
                    |active| {
                        if active == owned_active {
                            PosixProcessObservation::Exists {
                                current_start: Some(active.process_start),
                            }
                        } else {
                            observe(active)
                        }
                    },
                    &mut cancelled,
                )
                .map_err(operation_barrier_pair_cause)?;
            if cancelled() {
                return Err(LinuxLiveWriterBarrierCause::Cancelled);
            }
            files
                .verify_owned_writer_operation(owned, selected)
                .map_err(LinuxLiveWriterBarrierCause::Lease)?;
            if inspection.writer != Some(owned_active) {
                return Err(LinuxLiveWriterBarrierCause::Lease(
                    LinuxWriterLeaseError::OwnerMismatch,
                ));
            }
            Ok(LinuxLiveWriterReaderFacts {
                registering_readers: inspection.registering_readers,
                oldest_reader_txn: inspection.oldest_reader_txn,
            })
        })();
        match validation {
            Ok(protection) => Ok(LinuxLiveWriterOperationBarrier::new(
                inner,
                self.creator_pid,
                Some(protection),
            )),
            Err(cause) => Err(LinuxLiveWriterBarrierError::Locked {
                cause,
                barrier: LinuxLiveWriterOperationBarrier::new(inner, self.creator_pid, None),
            }),
        }
    }

    /// Holds one Linux operation barrier from core preparation through durable
    /// metadata publication, phase-five lease handling, and core completion.
    ///
    /// The caller retains the transaction core after every error so it can run
    /// the appropriate explicit abort or committed-cleanup route. This method
    /// never leaves an undisposed barrier behind: a failed unlock becomes
    /// close-only and is recovered by `close`.
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn publish_fixed_point_private_output<
        'slots,
        'cleanup,
        'backing,
        'arena,
        'record_cleanup,
        I,
        O,
        E,
    >(
        &self,
        core: &mut PrivateWriterTransactionCore<'slots, 'cleanup, I, O, E>,
        handle: &PrivateWriterTransactionHandle,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
    ) -> Result<Bootstrap, LinuxLiveWriterCoreCommitError<E>>
    where
        E: From<LinuxLiveWriterPageSinkError>,
    {
        self.publish_fixed_point_private_output_with(
            core,
            handle,
            workspace,
            |file| file.sync_all(),
            write_target_meta_page,
            |files, owned| files.update_writer_lease_after_meta(owned),
        )
    }

    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn publish_fixed_point_private_output_with<
        'slots,
        'cleanup,
        'backing,
        'arena,
        'record_cleanup,
        I,
        O,
        E,
        S,
        M,
        U,
    >(
        &self,
        core: &mut PrivateWriterTransactionCore<'slots, 'cleanup, I, O, E>,
        handle: &PrivateWriterTransactionHandle,
        workspace: &mut FixedPointCoordinatorWorkspace<'backing, 'arena, 'record_cleanup>,
        synchronize: S,
        write_meta: M,
        update_lease: U,
    ) -> Result<Bootstrap, LinuxLiveWriterCoreCommitError<E>>
    where
        E: From<LinuxLiveWriterPageSinkError>,
        S: FnMut(&File) -> io::Result<()>,
        M: FnOnce(&RetainedRegular, MetaV4) -> Result<(), LinuxOsError>,
        U: FnOnce(
            &mut RetainedLiveFiles,
            &mut OwnedWriterLease,
        ) -> Result<(), LinuxWriterLeaseError>,
    {
        let barrier = match self.acquire_operation_barrier_with_cancel(|| false) {
            Ok(barrier) => barrier,
            Err(LinuxLiveWriterBarrierError::Failed(cause)) => {
                return Err(LinuxLiveWriterCoreCommitError::Barrier(cause));
            }
            Err(LinuxLiveWriterBarrierError::Locked { cause, mut barrier }) => {
                barrier.force_close_only_after_publication();
                return Err(LinuxLiveWriterCoreCommitError::Barrier(cause));
            }
        };

        if core.selected() != barrier.selected_meta() {
            return match barrier.release_after_nonpublication() {
                Ok(()) => Err(LinuxLiveWriterCoreCommitError::Core(
                    PrivateWriterTransactionError::SelectedGenerationMismatch,
                )),
                Err(release) => Err(LinuxLiveWriterCoreCommitError::CoreRelease {
                    core: PrivateWriterTransactionError::SelectedGenerationMismatch,
                    release,
                }),
            };
        }

        let preparation = match core.prepare_fixed_point_private_output(handle, workspace) {
            Ok(preparation) => preparation,
            Err(core_error) => {
                return match barrier.release_after_nonpublication() {
                    Ok(()) => Err(LinuxLiveWriterCoreCommitError::Core(core_error)),
                    Err(release) => Err(LinuxLiveWriterCoreCommitError::CoreRelease {
                        core: core_error,
                        release,
                    }),
                };
            }
        };
        let target = preparation.target();
        let mut authorization = None::<PrivateWriterMetaPublication>;
        let publication = barrier.publish_private_pages_with(
            target,
            |sink| {
                let mut write = |pgno: u32, bytes: &[u8]| -> Result<(), E> {
                    sink.write_page(pgno, bytes).map_err(E::from)
                };
                core.drain_fixed_point_private_pages(handle, &preparation, workspace, &mut write)?;
                let completed =
                    core.finish_fixed_point_private_output(handle, preparation, workspace)?;
                debug_assert_eq!(completed.target(), target);
                authorization = Some(completed);
                Ok(())
            },
            synchronize,
            write_meta,
            update_lease,
        );

        match publication {
            Ok(publication) => {
                let Some(authorization) = authorization else {
                    publication.force_close_only();
                    return Err(
                        LinuxLiveWriterCoreCommitError::MissingCorePublicationAuthority {
                            phase_five: None,
                        },
                    );
                };
                let target = publication.target();
                if let Err(core_error) = core.confirm_durable_publication(handle, authorization) {
                    publication.force_close_only();
                    return Err(
                        LinuxLiveWriterCoreCommitError::CoreAfterDurablePublication {
                            phase_five: None,
                            core: core_error,
                        },
                    );
                }
                match publication.release() {
                    Ok(()) => Ok(target),
                    Err((publication, release)) => {
                        drop(publication);
                        Err(LinuxLiveWriterCoreCommitError::ReleaseAfterDurablePublication(release))
                    }
                }
            }
            Err((barrier, LinuxLiveWriterPublicationError::Preflight(publication))) => {
                let publication = LinuxLiveWriterPublicationError::Preflight(publication);
                match barrier.release_after_nonpublication() {
                    Ok(()) => Err(LinuxLiveWriterCoreCommitError::Publication(publication)),
                    Err(release) => Err(LinuxLiveWriterCoreCommitError::PublicationRelease {
                        publication,
                        release,
                    }),
                }
            }
            Err((barrier, LinuxLiveWriterPublicationError::Committed(phase_five))) => {
                let Some(authorization) = authorization else {
                    drop(barrier);
                    return Err(
                        LinuxLiveWriterCoreCommitError::MissingCorePublicationAuthority {
                            phase_five: Some(phase_five),
                        },
                    );
                };
                let completion = core.confirm_durable_publication(handle, authorization);
                drop(barrier);
                match completion {
                    Ok(()) => Err(LinuxLiveWriterCoreCommitError::Publication(
                        LinuxLiveWriterPublicationError::Committed(phase_five),
                    )),
                    Err(core_error) => Err(
                        LinuxLiveWriterCoreCommitError::CoreAfterDurablePublication {
                            phase_five: Some(phase_five),
                            core: core_error,
                        },
                    ),
                }
            }
            Err((barrier, LinuxLiveWriterPublicationError::OutcomeUnknown(publication))) => {
                let completion = match authorization.as_ref() {
                    Some(authorization) => {
                        core.mark_publication_outcome_unknown(handle, authorization)
                    }
                    None => {
                        core.force_publication_outcome_unknown();
                        Err(PrivateWriterTransactionError::StaleHandle)
                    }
                };
                drop(barrier);
                match completion {
                    Ok(()) => Err(LinuxLiveWriterCoreCommitError::Publication(
                        LinuxLiveWriterPublicationError::OutcomeUnknown(publication),
                    )),
                    Err(core) => Err(LinuxLiveWriterCoreCommitError::CoreAfterOutcomeUnknown {
                        publication,
                        core,
                    }),
                }
            }
            Err((barrier, publication)) => {
                debug_assert!(publication.requires_close_only());
                drop(barrier);
                Err(LinuxLiveWriterCoreCommitError::Publication(publication))
            }
        }
    }

    pub(crate) fn close(
        &self,
    ) -> Result<Option<LiveClaimCleanupOutcome>, LinuxLiveWriterCloseError> {
        self.close_with_io(|file, length| file.set_len(length), |file| file.sync_all())
    }

    fn close_with_io(
        &self,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<Option<LiveClaimCleanupOutcome>, LinuxLiveWriterCloseError> {
        self.check_creator_for_close()?;
        let mut inner = self.lock_inner();
        if inner.state == WriterState::Closed {
            return Ok(None);
        }
        if inner.state == WriterState::OperationLocked {
            return Err(LinuxLiveWriterCloseError::OperationBarrierHeld);
        }
        inner.state = WriterState::CleanupOnly;
        let LinuxLiveWriterInner { files, owned, .. } = &mut *inner;
        let outcome = files
            .as_mut()
            .expect("non-closed writer retains files")
            .retry_writer_lease_cleanup_with(owned.as_ref(), truncate, synchronize)
            .map_err(LinuxLiveWriterCloseError::Cleanup)?;
        *owned = None;
        *files = None;
        inner.state = WriterState::Closed;
        Ok(Some(outcome))
    }

    fn check_creator_for_close(&self) -> Result<(), LinuxLiveWriterCloseError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveWriterCloseError::ForkedHandle);
        }
        Ok(())
    }

    fn lock_inner(&self) -> MutexGuard<'_, LinuxLiveWriterInner> {
        self.inner
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }
}

impl<'a> LinuxLiveWriterOperationBarrier<'a> {
    fn new(
        inner: MutexGuard<'a, LinuxLiveWriterInner>,
        creator_pid: u32,
        protection: Option<LinuxLiveWriterReaderFacts>,
    ) -> Self {
        LinuxLiveWriterOperationBarrier {
            inner,
            creator_pid,
            protection,
        }
    }

    fn selected_meta(&self) -> MetaV4 {
        self.inner.bootstrap.meta
    }

    /// Runs allocator/retirement finalization against the exact selected file
    /// while this same operation barrier remains held.
    ///
    /// The closure is higher-ranked: values that borrow this context cannot
    /// escape it. A caller may then consume the still-held barrier directly for
    /// physical publication.
    pub(crate) fn with_finalization_context<R>(
        &self,
        finalizer: impl for<'context> FnOnce(LinuxLiveWriterFinalizationContext<'context>) -> R,
    ) -> Result<R, LinuxLiveWriterFinalizationContextError> {
        let protection = self
            .protection
            .ok_or(LinuxLiveWriterFinalizationContextError::ReaderFactsUnavailable)?;
        let selected = self.inner.bootstrap;
        let main = &self
            .inner
            .files
            .as_ref()
            .expect("operation barrier retains live files")
            .main;
        let pages = main
            .pinned_page_source(selected)
            .map_err(LinuxLiveWriterFinalizationContextError::Source)?;
        let reclaim_fence = RetirementReclaimFence::from_stable_reader_table(
            self,
            protection.registering_readers,
            protection.oldest_reader_txn,
        );
        Ok(finalizer(LinuxLiveWriterFinalizationContext {
            selected,
            pages,
            reclaim_fence,
        }))
    }

    fn force_close_only_after_publication(&mut self) {
        if self.inner.state == WriterState::OperationLocked {
            self.protection = None;
            self.inner.state = WriterState::CleanupOnly;
        }
    }

    fn publication_failure<E>(
        mut self,
        error: LinuxLiveWriterPublicationError<E>,
    ) -> (Self, LinuxLiveWriterPublicationError<E>) {
        debug_assert!(error.requires_close_only());
        self.force_close_only_after_publication();
        (self, error)
    }

    /// Releases a barrier after no publication attempt was recorded. If the
    /// sidecar unlock itself fails, retain close-only cleanup instead of leaving
    /// an inaccessible locked writer state behind.
    fn release_after_nonpublication(mut self) -> Result<(), LinuxLiveWriterBarrierReleaseError> {
        match self.release() {
            Ok(()) => Ok(()),
            Err(error) => {
                self.force_close_only_after_publication();
                Err(error)
            }
        }
    }

    /// Runs phases 2-5 for pages already finalized under this operation lock.
    ///
    /// The callback receives only the target's non-meta pages. It may stream
    /// the transaction core's bounded private output without allocating or
    /// retaining another file handle. On every post-attempt error this method
    /// marks the writer close-only before returning the retained barrier; the
    /// caller drops that barrier, then uses `LinuxLiveWriter::close` for
    /// recovery.
    #[allow(clippy::result_large_err)]
    pub(crate) fn publish_private_pages<E>(
        self,
        target: MetaV4,
        write: impl FnOnce(&mut LinuxLiveWriterPageSink<'_>) -> Result<(), E>,
    ) -> Result<LinuxLiveWriterPublication<'a>, (Self, LinuxLiveWriterPublicationError<E>)> {
        self.publish_private_pages_with(
            target,
            write,
            |file| file.sync_all(),
            write_target_meta_page,
            |files, owned| files.update_writer_lease_after_meta(owned),
        )
    }

    #[allow(clippy::result_large_err)]
    fn publish_private_pages_with<E, W, S, M, U>(
        mut self,
        target: MetaV4,
        write: W,
        mut synchronize: S,
        write_meta: M,
        update_lease: U,
    ) -> Result<LinuxLiveWriterPublication<'a>, (Self, LinuxLiveWriterPublicationError<E>)>
    where
        W: FnOnce(&mut LinuxLiveWriterPageSink<'_>) -> Result<(), E>,
        S: FnMut(&std::fs::File) -> std::io::Result<()>,
        M: FnOnce(&RetainedRegular, MetaV4) -> Result<(), LinuxOsError>,
        U: FnOnce(
            &mut RetainedLiveFiles,
            &mut OwnedWriterLease,
        ) -> Result<(), LinuxWriterLeaseError>,
    {
        if std::process::id() != self.creator_pid {
            return Err((
                self,
                LinuxLiveWriterPublicationError::Preflight(LinuxWriterLeaseError::Os(
                    LinuxOsError::ForkedHandle,
                )),
            ));
        }
        if self.protection.is_none() {
            return Err((
                self,
                LinuxLiveWriterPublicationError::Preflight(LinuxWriterLeaseError::ScanRequired),
            ));
        }

        {
            let LinuxLiveWriterInner { files, owned, .. } = &mut *self.inner;
            let files = files.as_mut().expect("locked writer retains files");
            let owned = owned.as_ref().expect("locked writer retains exact lease");
            if let Err(error) = files.begin_writer_commit_attempt(owned, target) {
                return Err((self, LinuxLiveWriterPublicationError::Preflight(error)));
            }
        }

        {
            let files = self
                .inner
                .files
                .as_ref()
                .expect("locked writer retains files");
            let mut sink = LinuxLiveWriterPageSink {
                main: &files.main,
                target_page_count: target.page_count,
            };
            if let Err(error) = write(&mut sink) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::NotCommitted(
                        LinuxLiveWriterPublicationCause::Sink(error),
                    ),
                ));
            }
        }

        let target_bytes = match target.page_count.checked_mul(PAGE_SIZE as u64) {
            Some(bytes) => bytes,
            None => {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::NotCommitted(
                        LinuxLiveWriterPublicationCause::Lease(
                            LinuxWriterLeaseError::CommitAttemptMismatch,
                        ),
                    ),
                ));
            }
        };
        {
            let files = self
                .inner
                .files
                .as_mut()
                .expect("locked writer retains files");
            if let Err(error) = files.main.set_len(target_bytes) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::NotCommitted(
                        LinuxLiveWriterPublicationCause::Os(error),
                    ),
                ));
            }
            if let Err(error) = synchronize_retained_main(&files.main, &mut synchronize) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::NotCommitted(
                        LinuxLiveWriterPublicationCause::Os(error),
                    ),
                ));
            }
        }

        {
            let LinuxLiveWriterInner { files, owned, .. } = &mut *self.inner;
            let files = files.as_mut().expect("locked writer retains files");
            let owned = owned.as_ref().expect("locked writer retains exact lease");
            if let Err(error) = files.begin_writer_meta_write(owned, target) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::NotCommitted(
                        LinuxLiveWriterPublicationCause::Lease(error),
                    ),
                ));
            }
        }

        {
            let files = self
                .inner
                .files
                .as_ref()
                .expect("locked writer retains files");
            if let Err(error) = write_meta(&files.main, target) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::OutcomeUnknown(
                        LinuxLiveWriterPublicationCause::Os(error),
                    ),
                ));
            }
            if let Err(error) = synchronize_retained_main(&files.main, &mut synchronize) {
                return Err(self.publication_failure(
                    LinuxLiveWriterPublicationError::OutcomeUnknown(
                        LinuxLiveWriterPublicationCause::Os(error),
                    ),
                ));
            }
        }

        let confirmed = {
            let LinuxLiveWriterInner { files, owned, .. } = &mut *self.inner;
            let files = files.as_mut().expect("locked writer retains files");
            let owned = owned.as_ref().expect("locked writer retains exact lease");
            match files.confirm_writer_meta_sync(owned, target) {
                Ok(confirmed) => confirmed,
                Err(error) => {
                    return Err(self.publication_failure(
                        LinuxLiveWriterPublicationError::OutcomeUnknown(
                            LinuxLiveWriterPublicationCause::Lease(error),
                        ),
                    ));
                }
            }
        };

        let update_result = {
            let LinuxLiveWriterInner { files, owned, .. } = &mut *self.inner;
            let files = files.as_mut().expect("locked writer retains files");
            let owned = owned.as_mut().expect("locked writer retains exact lease");
            update_lease(files, owned)
        };
        if let Err(error) = update_result {
            return Err(
                self.publication_failure(LinuxLiveWriterPublicationError::Committed(
                    LinuxLiveWriterPublicationCause::Lease(error),
                )),
            );
        }

        {
            let LinuxLiveWriterInner { bootstrap, .. } = &mut *self.inner;
            *bootstrap = confirmed;
        }

        Ok(LinuxLiveWriterPublication {
            barrier: self,
            target: confirmed,
        })
    }

    /// Leaves the retained sidecar lock in place for `Close` to clean up and
    /// drops only the in-process operation guard. This is required after any
    /// recorded publication attempt, including a phase-5 interrupted update.
    #[allow(clippy::result_large_err)]
    pub(crate) fn into_close_only(
        mut self,
    ) -> Result<(), (Self, LinuxLiveWriterBarrierReleaseError)> {
        if std::process::id() != self.creator_pid {
            return Err((self, LinuxLiveWriterBarrierReleaseError::ForkedHandle));
        }
        if self.inner.state == WriterState::CleanupOnly {
            return Ok(());
        }
        if self.inner.state != WriterState::OperationLocked {
            return Err((self, LinuxLiveWriterBarrierReleaseError::NotHeld));
        }
        self.force_close_only_after_publication();
        Ok(())
    }

    pub(crate) fn release(&mut self) -> Result<(), LinuxLiveWriterBarrierReleaseError> {
        self.release_with(RetainedRegular::release_lock)
    }

    pub(crate) fn abort(&mut self) -> Result<(), LinuxLiveWriterBarrierReleaseError> {
        self.release()
    }

    fn release_with(
        &mut self,
        release: impl FnOnce(&mut RetainedRegular) -> Result<(), LinuxOsError>,
    ) -> Result<(), LinuxLiveWriterBarrierReleaseError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveWriterBarrierReleaseError::ForkedHandle);
        }
        if self.inner.state != WriterState::OperationLocked {
            return Err(LinuxLiveWriterBarrierReleaseError::NotHeld);
        }
        release(
            &mut self
                .inner
                .files
                .as_mut()
                .expect("locked writer retains files")
                .sidecar,
        )
        .map_err(LinuxLiveWriterBarrierReleaseError::Os)?;
        self.protection = None;
        self.inner.state = WriterState::Open;
        Ok(())
    }
}

fn synchronize_retained_main(
    main: &RetainedRegular,
    synchronize: &mut impl FnMut(&File) -> io::Result<()>,
) -> Result<(), LinuxOsError> {
    main.check_creator()?;
    synchronize(&main.file).map_err(|source| LinuxOsError::Io {
        operation: "synchronize retained main file",
        source,
    })
}

fn write_target_meta_page(main: &RetainedRegular, target: MetaV4) -> Result<(), LinuxOsError> {
    let mut page = [0u8; PAGE_SIZE];
    target.encode_into(&mut page);
    let offset = (target.txn_id & 1)
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(LinuxOsError::OffsetOverflow)?;
    main.write_all_at(&page, offset)
}

fn operation_barrier_pair_cause(cause: LinuxLivePairError) -> LinuxLiveWriterBarrierCause {
    if matches!(
        cause,
        LinuxLivePairError::Os(LinuxOsError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Os(LinuxOsError::Cancelled))
    ) {
        LinuxLiveWriterBarrierCause::Cancelled
    } else {
        LinuxLiveWriterBarrierCause::Pair(cause)
    }
}

fn failed_with_possible_cleanup(
    mut files: RetainedLiveFiles,
    owned: Option<OwnedWriterLease>,
    cause: LinuxLiveWriterOpenCause,
    known_cleanup_error: Option<LinuxLiveCleanupError>,
) -> Result<LinuxLiveWriter, LinuxLiveWriterOpenError> {
    let owned = owned.map(OwnedLiveClaim::Writer);
    if !requires_cleanup(&files, owned.as_ref()) {
        let cleanup_outcome = known_cleanup_error
            .as_ref()
            .map(|_| files.live_cleanup_paths());
        return Err(LinuxLiveWriterOpenError::Failed {
            cause,
            cleanup_outcome,
        });
    }

    let cleanup = match known_cleanup_error {
        Some(cleanup) => Err(cleanup),
        None => retry_any_cleanup(&mut files, owned.as_ref()),
    };
    match cleanup {
        Ok(cleanup_outcome) => Err(LinuxLiveWriterOpenError::Failed {
            cause,
            cleanup_outcome: Some(cleanup_outcome),
        }),
        Err(cleanup) => Err(LinuxLiveWriterOpenError::CleanupRequired {
            cause,
            cleanup,
            guard: LinuxLiveCleanupGuard::new(files, owned),
        }),
    }
}

fn pair_open_cause(error: LinuxLivePairError) -> LinuxLiveWriterOpenCause {
    if matches!(
        &error,
        LinuxLivePairError::Os(LinuxOsError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Os(LinuxOsError::Cancelled))
    ) {
        LinuxLiveWriterOpenCause::Cancelled
    } else {
        LinuxLiveWriterOpenCause::Pair(error)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::MetaV4;
    use crate::retirement_reader::{RetirementIdentity, RetirementSelectionResult, RetirementTree};
    use crate::sidecar::{encode_active_slot, SidecarOrigin};
    use crate::test_alloc::count_thread_allocations;
    use std::cell::Cell;
    use std::os::unix::ffi::OsStrExt;
    use std::os::unix::fs::FileExt;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{mpsc, Arc};

    static NEXT_DIRECTORY: AtomicU64 = AtomicU64::new(1);

    #[derive(Debug)]
    struct TestDatabase {
        directory: PathBuf,
        main: PathBuf,
        sidecar: PathBuf,
        header: SidecarHeader,
        meta: MetaV4,
    }

    impl Drop for TestDatabase {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.directory);
        }
    }

    impl TestDatabase {
        fn new(
            transaction: u64,
            committed_pages: usize,
            physical_pages: usize,
            capacity: u32,
        ) -> Self {
            assert!(committed_pages >= 2);
            assert!(physical_pages >= committed_pages);
            assert!(capacity > 0);
            let ordinal = NEXT_DIRECTORY.fetch_add(1, Ordering::Relaxed);
            let directory = std::env::temp_dir().join(format!(
                "iprange-v4-live-writer-{}-{ordinal}",
                std::process::id()
            ));
            std::fs::create_dir(&directory).unwrap();
            let main = directory.join("main.iprdb");
            let sidecar = directory.join("main.iprdb.readers");
            let mut meta = empty_direct_meta(transaction);
            meta.page_count = committed_pages as u64;
            let mut bytes = vec![0u8; physical_pages * PAGE_SIZE];
            meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
            meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
            std::fs::write(&main, bytes).unwrap();

            let (parent, main_component) = RetainedDirectory::open_parent(&main).unwrap();
            let sidecar_component = parent.sidecar_component(&main_component).unwrap();
            let created = File::create(&sidecar).unwrap();
            created
                .set_len(2 * PAGE_SIZE as u64 + (u64::from(capacity) + 1) * u64::from(SLOT_SIZE))
                .unwrap();
            drop(created);
            let retained_main = parent.open_regular(&main_component, true).unwrap();
            let retained_sidecar = parent.open_regular(&sidecar_component, true).unwrap();
            let header = SidecarHeader {
                identity_kind: LocalIdentityKind::Posix,
                capacity,
                state: SidecarState::Ready,
                database_id: meta.database_id,
                main_identity: retained_main.identity().encode(),
                sidecar_identity: retained_sidecar.identity().encode(),
                sidecar_id: [7; 16],
                origin: SidecarOrigin::CreateLive,
                attempted_txn_id: meta.txn_id,
                attempted_commit_nonce: meta.commit_nonce,
                attempted_main_bytes: meta.page_count * PAGE_SIZE as u64,
                attempted_main_sha512: [8; 64],
                process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
                process_domain_token: linux_process_domain_token().unwrap(),
                basename_encoding: BasenameEncoding::PosixBytes as u16,
                basename_len: main_component.as_bytes().len() as u32,
                basename_commitment: basename_commitment(
                    BasenameEncoding::PosixBytes,
                    main_component.as_bytes(),
                )
                .unwrap(),
                creation_security_kind: 1,
                creation_security_commitment: [9; 32],
                header_seq: 1,
            };
            let mut block = [0u8; PAGE_SIZE];
            header.encode_into(&mut block);
            retained_sidecar.write_all_at(&block, 0).unwrap();
            retained_sidecar
                .write_all_at(&block, PAGE_SIZE as u64)
                .unwrap();
            drop(retained_sidecar);
            drop(retained_main);
            drop(parent);
            Self {
                directory,
                main,
                sidecar,
                header,
                meta,
            }
        }

        fn slot(&self, index: u32) -> [u8; SLOT_SIZE as usize] {
            slot_at(&self.sidecar, self.header, index)
        }

        fn put_active(&self, index: u32, active: ActiveSlot) {
            self.put_raw(index, &encode_active_slot(active));
        }

        fn put_raw(&self, index: u32, bytes: &[u8; SLOT_SIZE as usize]) {
            let (parent, main_component) = RetainedDirectory::open_parent(&self.main).unwrap();
            let sidecar_component = parent.sidecar_component(&main_component).unwrap();
            let retained = parent.open_regular(&sidecar_component, true).unwrap();
            retained
                .write_all_at(bytes, sidecar_slot_offset(self.header, index).unwrap())
                .unwrap();
        }

        fn retained_sidecar(&self) -> RetainedRegular {
            let (parent, main_component) = RetainedDirectory::open_parent(&self.main).unwrap();
            let sidecar_component = parent.sidecar_component(&main_component).unwrap();
            parent.open_regular(&sidecar_component, true).unwrap()
        }

        fn replace_meta_pair(&self, meta: MetaV4) {
            let file = File::options().write(true).open(&self.main).unwrap();
            let mut page = [0u8; PAGE_SIZE];
            meta.encode_into(&mut page);
            file.write_all_at(&page, 0).unwrap();
            file.write_all_at(&page, PAGE_SIZE as u64).unwrap();
        }

        fn replace_meta_page(&self, index: u64, meta: Option<MetaV4>) {
            let file = File::options().write(true).open(&self.main).unwrap();
            let mut page = [0u8; PAGE_SIZE];
            if let Some(meta) = meta {
                meta.encode_into(&mut page);
            }
            file.write_all_at(&page, index * PAGE_SIZE as u64).unwrap();
        }
    }

    fn slot_at(path: &Path, header: SidecarHeader, index: u32) -> [u8; SLOT_SIZE as usize] {
        let bytes = std::fs::read(path).unwrap();
        let offset = usize::try_from(sidecar_slot_offset(header, index).unwrap()).unwrap();
        bytes[offset..offset + SLOT_SIZE as usize]
            .try_into()
            .unwrap()
    }

    fn injected_failure() -> LinuxLiveWriterOpenCause {
        LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged)
    }

    fn tail_for(writer: &LinuxLiveWriter, observed_pages: u64) -> UnpublishedMainTail {
        let inner = writer.lock_inner();
        let files = inner.files.as_ref().unwrap();
        UnpublishedMainTail {
            main_identity: files.main.identity,
            database_id: inner.bootstrap.meta.database_id,
            transaction_id: inner.bootstrap.meta.txn_id,
            commit_nonce: inner.bootstrap.meta.commit_nonce,
            committed_length: inner.bootstrap.committed_bytes,
            observed_end_exclusive: observed_pages * PAGE_SIZE as u64,
        }
    }

    fn next_publication_target(writer: &LinuxLiveWriter, page_count: u64, nonce: u8) -> MetaV4 {
        let source = writer.lock_inner().bootstrap;
        let mut target = source.meta;
        target.txn_id += 1;
        target.commit_nonce = [nonce; 16];
        target.page_count = page_count;
        target
    }

    fn selected_bootstrap(database: &TestDatabase) -> Bootstrap {
        crate::bootstrap::open(&std::fs::read(&database.main).unwrap(), OpenMode::Writer).unwrap()
    }

    #[test]
    fn private_page_publication_commits_target_and_releases_after_success() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xa1);
        let page = [0xa2; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let publication = barrier
            .publish_private_pages(target, |sink| sink.write_page(2, &page))
            .unwrap();
        assert_eq!(publication.target().meta, target);
        let StableSlot::Active(active) = decode_stable_slot(
            &database.slot(0),
            SlotRole::Writer,
            linux_slot_host_limits(),
        )
        .unwrap() else {
            panic!("phase five did not retain the target writer lease");
        };
        assert_eq!(active.txn_id, target.txn_id);

        publication.release().unwrap();
        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, target);
        assert_eq!(
            std::fs::read(&database.main).unwrap()[2 * PAGE_SIZE..],
            page
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_publication_preflight_failure_keeps_writer_reusable() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let mut target = next_publication_target(&writer, 3, 0xa3);
        target.txn_id += 1;

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (mut barrier, error) = barrier
            .publish_private_pages(target, |_| Ok::<(), ()>(()))
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::Preflight(
                LinuxWriterLeaseError::CommitAttemptMismatch
            )
        ));
        assert!(!error.requires_close_only());
        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        barrier.release().unwrap();
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, database.meta);
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_sink_failure_is_not_committed_and_forces_close_only() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xa4);
        let page = [0xa5; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (mut barrier, error) = barrier
            .publish_private_pages(target, |sink| {
                sink.write_page(2, &page).unwrap();
                Err::<(), ()>(())
            })
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::NotCommitted(
                LinuxLiveWriterPublicationCause::Sink(())
            )
        ));
        assert!(error.requires_close_only());
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        assert!(matches!(
            barrier.release(),
            Err(LinuxLiveWriterBarrierReleaseError::NotHeld)
        ));
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, database.meta);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_phase_two_sync_failure_is_not_committed_and_cleans_old_tail() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xa6);
        let page = [0xa7; PAGE_SIZE];
        let synchronizations = Cell::new(0u8);

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (barrier, error) = barrier
            .publish_private_pages_with(
                target,
                |sink| {
                    sink.write_page(2, &page).unwrap();
                    Ok::<(), ()>(())
                },
                |_| {
                    synchronizations.set(synchronizations.get() + 1);
                    Err(std::io::Error::other("injected phase-two sync failure"))
                },
                write_target_meta_page,
                |files, owned| files.update_writer_lease_after_meta(owned),
            )
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::NotCommitted(LinuxLiveWriterPublicationCause::Os(_))
        ));
        assert_eq!(synchronizations.get(), 1);
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, database.meta);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_meta_write_failure_is_outcome_unknown_and_cleans_old_tail() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xa8);
        let page = [0xa9; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (barrier, error) = barrier
            .publish_private_pages_with(
                target,
                |sink| {
                    sink.write_page(2, &page).unwrap();
                    Ok::<(), ()>(())
                },
                |file| file.sync_all(),
                |_, _| Err(LinuxOsError::RandomFailure),
                |files, owned| files.update_writer_lease_after_meta(owned),
            )
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::OutcomeUnknown(LinuxLiveWriterPublicationCause::Os(
                LinuxOsError::RandomFailure
            ))
        ));
        assert!(error.requires_close_only());
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, database.meta);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_phase_four_sync_failure_retains_the_target() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xaa);
        let page = [0xab; PAGE_SIZE];
        let synchronizations = Cell::new(0u8);

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (barrier, error) = barrier
            .publish_private_pages_with(
                target,
                |sink| {
                    sink.write_page(2, &page).unwrap();
                    Ok::<(), ()>(())
                },
                |file| {
                    let next = synchronizations.get() + 1;
                    synchronizations.set(next);
                    if next == 2 {
                        Err(std::io::Error::other("injected phase-four sync failure"))
                    } else {
                        file.sync_all()
                    }
                },
                write_target_meta_page,
                |files, owned| files.update_writer_lease_after_meta(owned),
            )
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::OutcomeUnknown(LinuxLiveWriterPublicationCause::Os(_))
        ));
        assert_eq!(synchronizations.get(), 2);
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, target);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(
            std::fs::read(&database.main).unwrap()[2 * PAGE_SIZE..],
            page
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_phase_four_confirmation_failure_is_outcome_unknown() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let source = writer.lock_inner().bootstrap.meta;
        let target = next_publication_target(&writer, 3, 0xac);
        let page = [0xad; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (barrier, error) = barrier
            .publish_private_pages_with(
                target,
                |sink| {
                    sink.write_page(2, &page).unwrap();
                    Ok::<(), ()>(())
                },
                |file| file.sync_all(),
                |main, target| {
                    write_target_meta_page(main, target)?;
                    let mut source_page = [0u8; PAGE_SIZE];
                    source.encode_into(&mut source_page);
                    main.write_all_at(&source_page, (target.txn_id & 1) * PAGE_SIZE as u64)
                },
                |files, owned| files.update_writer_lease_after_meta(owned),
            )
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::OutcomeUnknown(
                LinuxLiveWriterPublicationCause::Lease(
                    LinuxWriterLeaseError::CommitOutcomeUnresolved
                )
            )
        ));
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, database.meta);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_phase_five_failure_is_committed_and_retains_the_target() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xae);
        let page = [0xaf; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (barrier, error) = barrier
            .publish_private_pages_with(
                target,
                |sink| {
                    sink.write_page(2, &page).unwrap();
                    Ok::<(), ()>(())
                },
                |file| file.sync_all(),
                write_target_meta_page,
                |_, _| Err(LinuxWriterLeaseError::GenerationChanged),
            )
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterPublicationError::Committed(LinuxLiveWriterPublicationCause::Lease(
                LinuxWriterLeaseError::GenerationChanged
            ))
        ));
        assert!(error.requires_close_only());
        assert_eq!(barrier.inner.state, WriterState::CleanupOnly);
        drop(barrier);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, target);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_publication_can_be_made_close_only_after_phase_four() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xb0);
        let page = [0xb1; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let publication = barrier
            .publish_private_pages(target, |sink| sink.write_page(2, &page))
            .unwrap();
        publication.into_close_only().unwrap();

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, target);
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_publication_release_failure_forces_close_only() {
        let database = TestDatabase::new(7, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = next_publication_target(&writer, 3, 0xb2);
        let page = [0xb3; PAGE_SIZE];

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let publication = barrier
            .publish_private_pages(target, |sink| sink.write_page(2, &page))
            .unwrap();
        let (publication, error) = publication
            .release_with(|_| Err(LinuxOsError::RandomFailure))
            .unwrap_err();
        assert!(matches!(
            error,
            LinuxLiveWriterBarrierReleaseError::Os(LinuxOsError::RandomFailure)
        ));
        assert_eq!(publication.barrier.inner.state, WriterState::CleanupOnly);
        drop(publication);

        writer.close().unwrap();
        assert_eq!(selected_bootstrap(&database).meta, target);
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn private_page_publication_is_allocation_free_at_reader_capacity() {
        for capacity in [1, 64, 1024] {
            let database = TestDatabase::new(7, 2, 2, capacity);
            let writer = LinuxLiveWriter::open(&database.main).unwrap();
            let target = next_publication_target(&writer, 3, 0xb4);
            let page = [0xb5; PAGE_SIZE];

            let (result, allocations) = count_thread_allocations(|| {
                let barrier = writer
                    .acquire_operation_barrier_with_cancel(|| false)
                    .unwrap();
                barrier.publish_private_pages(target, |sink| sink.write_page(2, &page))
            });
            assert_eq!(allocations, 0, "reader capacity {capacity}");
            result.unwrap().release().unwrap();
            writer.close().unwrap();
        }
    }

    #[test]
    fn open_claims_exact_selected_transaction_truncates_tail_and_close_is_idempotent() {
        let database = TestDatabase::new(7, 2, 4, 2);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        assert_eq!(writer.lock_inner().bootstrap.meta.txn_id, 7);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        let StableSlot::Active(active) = decode_stable_slot(
            &database.slot(0),
            SlotRole::Writer,
            linux_slot_host_limits(),
        )
        .unwrap() else {
            panic!("writer lease was not active");
        };
        assert_eq!(active.txn_id, 7);
        let inner = writer.lock_inner();
        assert_eq!(active, inner.owned.as_ref().unwrap().active);
        assert!(inner
            .files
            .as_ref()
            .is_some_and(|files| !files.sidecar.lock_held()));
        drop(inner);

        assert!(matches!(
            LinuxLiveWriter::open(&database.main),
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::WriterBusy),
                cleanup_outcome: None,
            })
        ));
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn operation_barrier_finalization_context_reports_exact_reader_facts() {
        let database = TestDatabase::new(7, 2, 2, 4);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        database.put_active(1, current_active_slot(0, [0x81; 16]));
        database.put_active(2, current_active_slot(6, [0x82; 16]));
        database.put_active(3, current_active_slot(2, [0x83; 16]));

        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        barrier
            .with_finalization_context(|context| {
                let (_, pages, protection) = context.into_parts();
                pages.check_access().unwrap();
                assert_eq!(protection.registering_readers(), 1);
                assert_eq!(protection.oldest_reader_txn(), Some(2));
                assert!(!protection.allows_reclaim(1));
            })
            .unwrap();
        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        barrier.release().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn finalization_context_binds_the_selected_retained_source_to_the_held_barrier() {
        let database = TestDatabase::new(7, 3, 3, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();

        barrier
            .with_finalization_context(|context| {
                let (selected, pages, fence) = context.into_parts();
                assert_eq!(pages.bootstrap(), selected);
                let mut page = [0u8; PAGE_SIZE];
                pages.read_page(2, &mut page).unwrap();
                assert_eq!(page, [0; PAGE_SIZE]);

                let meta = selected.meta;
                let tree = RetirementTree::from_source(
                    pages,
                    RetirementIdentity {
                        database_id: meta.database_id,
                        txn_id: meta.txn_id,
                        commit_nonce: meta.commit_nonce,
                        page_count: meta.page_count,
                        root: meta.retirement_root,
                        batch_count: meta.retirement_batch_count,
                    },
                )
                .unwrap();
                assert!(matches!(
                    tree.select_oldest_eligible(fence, 1, 1).unwrap(),
                    RetirementSelectionResult::NoChange(_)
                ));
            })
            .unwrap();

        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_reaps_dead_readers_before_reporting_protection() {
        let database = TestDatabase::new(3, 2, 2, 2);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        database.put_active(
            1,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x84; 16],
            },
        );

        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        barrier
            .with_finalization_context(|context| {
                let (_, _, protection) = context.into_parts();
                assert_eq!(protection.registering_readers(), 0);
                assert_eq!(protection.oldest_reader_txn(), None);
                assert!(protection.allows_reclaim(3));
            })
            .unwrap();
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_never_asks_an_injected_observer_to_kill_its_owned_writer() {
        let database = TestDatabase::new(3, 2, 2, 2);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        database.put_active(
            1,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x87; 16],
            },
        );
        let observations = Cell::new(0usize);
        let mut barrier = writer
            .acquire_operation_barrier_with(
                || false,
                |_| {
                    observations.set(observations.get() + 1);
                    PosixProcessObservation::Missing
                },
            )
            .unwrap();
        assert_eq!(observations.get(), 1);
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
        assert_eq!(
            decode_stable_slot(
                &database.slot(0),
                SlotRole::Writer,
                linux_slot_host_limits()
            ),
            Ok(StableSlot::Active(
                barrier.inner.owned.as_ref().unwrap().active
            ))
        );
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_validates_every_slot_before_reaping() {
        let database = TestDatabase::new(3, 2, 2, 2);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let dead = ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x85; 16],
        };
        database.put_active(1, dead);
        let mut malformed = [0u8; SLOT_SIZE as usize];
        malformed[4] = 1;
        database.put_raw(2, &malformed);
        let observations = Cell::new(0usize);

        let mut barrier = match writer.acquire_operation_barrier_with(
            || false,
            |_| {
                observations.set(observations.get() + 1);
                PosixProcessObservation::Missing
            },
        ) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause:
                    LinuxLiveWriterBarrierCause::Pair(LinuxLivePairError::Scan(
                        LinuxSidecarScanError::Slot {
                            index: 2,
                            problem: SlotProblem::FreeNonzero,
                        },
                    )),
                barrier,
            }) => barrier,
            other => panic!("expected locked malformed-slot failure, got {other:?}"),
        };
        assert_eq!(observations.get(), 0);
        assert_eq!(
            decode_stable_slot(
                &database.slot(1),
                SlotRole::Reader,
                linux_slot_host_limits()
            ),
            Ok(StableSlot::Active(dead))
        );
        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        barrier.abort().unwrap();
        drop(barrier);
        database.put_raw(2, &[0; SLOT_SIZE as usize]);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_lock_wait_is_cancellable_and_lock_is_retained_until_release() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let mut contender = database.retained_sidecar();
        contender.acquire_lock(LockMode::Exclusive, false).unwrap();
        let polls = Cell::new(0usize);
        assert!(matches!(
            writer.acquire_operation_barrier_with_cancel(|| {
                let current = polls.get() + 1;
                polls.set(current);
                current >= 3
            }),
            Err(LinuxLiveWriterBarrierError::Failed(
                LinuxLiveWriterBarrierCause::Cancelled
            ))
        ));
        assert_eq!(writer.lock_inner().state, WriterState::Open);
        contender.release_lock().unwrap();

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        assert!(matches!(
            contender.acquire_lock(LockMode::Exclusive, true),
            Err(LinuxOsError::LockBusy)
        ));
        drop(barrier);
        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::OperationBarrierHeld)
        ));
        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::AlreadyHeld,
                barrier,
            }) => barrier,
            other => panic!("expected recoverable held barrier, got {other:?}"),
        };
        barrier.release().unwrap();
        drop(barrier);
        contender.acquire_lock(LockMode::Exclusive, true).unwrap();
        contender.release_lock().unwrap();
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_rejects_main_generation_and_exact_lease_replacement() {
        let database = TestDatabase::new(4, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let exact_lease = writer.lock_inner().owned.as_ref().unwrap().active;
        let mut changed = empty_direct_meta(5);
        changed.database_id = database.meta.database_id;
        database.replace_meta_pair(changed);
        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                barrier,
            }) => barrier,
            other => panic!("expected locked generation failure, got {other:?}"),
        };
        barrier.abort().unwrap();
        drop(barrier);

        database.replace_meta_pair(database.meta);
        database.put_active(
            0,
            ActiveSlot {
                nonce: [0x86; 16],
                ..exact_lease
            },
        );
        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::Lease(LinuxWriterLeaseError::OwnerMismatch),
                barrier,
            }) => barrier,
            other => panic!("expected locked owner failure, got {other:?}"),
        };
        barrier.abort().unwrap();
        drop(barrier);

        database.put_active(0, exact_lease);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_dropped_locked_error_recovers_exact_held_authority() {
        let database = TestDatabase::new(4, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let mut changed = empty_direct_meta(5);
        changed.database_id = database.meta.database_id;
        let error = {
            database.replace_meta_pair(changed);
            writer
                .acquire_operation_barrier_with_cancel(|| false)
                .unwrap_err()
        };
        assert!(matches!(
            &error,
            LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                ..
            }
        ));
        drop(error);
        database.replace_meta_pair(database.meta);

        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::AlreadyHeld,
                barrier,
            }) => barrier,
            other => panic!("expected recovered dropped authority, got {other:?}"),
        };
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_rejects_canonical_pair_replacement_without_reopening() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let old_main = database.directory.join("barrier-old-main");
        let old_sidecar = database.directory.join("barrier-old-sidecar");
        std::fs::rename(&database.main, &old_main).unwrap();
        std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
        std::fs::write(&database.main, b"replacement").unwrap();
        std::fs::write(&database.sidecar, b"replacement").unwrap();

        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause:
                    LinuxLiveWriterBarrierCause::Lease(LinuxWriterLeaseError::Os(
                        LinuxOsError::PathIdentityMismatch,
                    )),
                barrier,
            }) => barrier,
            other => panic!("expected locked path failure, got {other:?}"),
        };
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        let active = barrier.inner.owned.as_ref().unwrap().active;
        barrier.abort().unwrap();
        drop(barrier);
        assert_eq!(
            slot_at(&old_sidecar, database.header, 0),
            encode_active_slot(active)
        );
    }

    #[test]
    fn operation_barrier_release_failure_retains_retryable_state() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        assert!(barrier.with_finalization_context(|_| ()).is_ok());
        assert!(matches!(
            barrier.release_with(|_| Err(LinuxOsError::RandomFailure)),
            Err(LinuxLiveWriterBarrierReleaseError::Os(
                LinuxOsError::RandomFailure
            ))
        ));
        assert!(barrier.with_finalization_context(|_| ()).is_ok());
        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        barrier.abort().unwrap();
        assert!(matches!(
            barrier.with_finalization_context(|_| ()),
            Err(LinuxLiveWriterFinalizationContextError::ReaderFactsUnavailable)
        ));
        assert_eq!(barrier.inner.state, WriterState::Open);
        assert!(matches!(
            barrier.release(),
            Err(LinuxLiveWriterBarrierReleaseError::NotHeld)
        ));
        assert!(matches!(
            barrier.with_finalization_context(|_| ()),
            Err(LinuxLiveWriterFinalizationContextError::ReaderFactsUnavailable)
        ));
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_pid_checks_precede_cancellation_state_and_unlock() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let mut writer = LinuxLiveWriter::open(&database.main).unwrap();
        let polled = Cell::new(false);
        writer.creator_pid = writer.creator_pid.wrapping_add(1);
        assert!(matches!(
            writer.acquire_operation_barrier_with_cancel(|| {
                polled.set(true);
                true
            }),
            Err(LinuxLiveWriterBarrierError::Failed(
                LinuxLiveWriterBarrierCause::ForkedHandle
            ))
        ));
        assert!(!polled.get());
        assert_eq!(writer.lock_inner().state, WriterState::Open);

        writer.creator_pid = std::process::id();
        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        barrier.creator_pid = barrier.creator_pid.wrapping_add(1);
        assert!(matches!(
            barrier.abort(),
            Err(LinuxLiveWriterBarrierReleaseError::ForkedHandle)
        ));
        assert_eq!(barrier.inner.state, WriterState::OperationLocked);
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        barrier.creator_pid = std::process::id();
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_repeats_without_losing_lock_or_lease_state() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let lease = writer.lock_inner().owned.as_ref().unwrap().active;
        for _ in 0..64 {
            let mut barrier = writer
                .acquire_operation_barrier_with_cancel(|| false)
                .unwrap();
            barrier
                .with_finalization_context(|context| {
                    let (_, _, protection) = context.into_parts();
                    assert_eq!(protection.registering_readers(), 0);
                    assert_eq!(protection.oldest_reader_txn(), None);
                })
                .unwrap();
            barrier.release().unwrap();
            drop(barrier);
            assert_eq!(writer.lock_inner().owned.as_ref().unwrap().active, lease);
        }

        let barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        drop(barrier);
        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::AlreadyHeld,
                barrier,
            }) => barrier,
            other => panic!("expected recoverable already-held barrier, got {other:?}"),
        };
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_mutex_serializes_concurrent_handle_calls() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = Arc::new(LinuxLiveWriter::open(&database.main).unwrap());
        let concurrent = Arc::clone(&writer);
        let mut barrier = writer
            .acquire_operation_barrier_with_cancel(|| false)
            .unwrap();
        let (started_tx, started_rx) = mpsc::channel();
        let (finished_tx, finished_rx) = mpsc::channel();
        let thread = std::thread::spawn(move || {
            started_tx.send(()).unwrap();
            let mut barrier = concurrent
                .acquire_operation_barrier_with_cancel(|| false)
                .unwrap();
            barrier.release().unwrap();
            drop(barrier);
            finished_tx.send(()).unwrap();
        });
        started_rx.recv().unwrap();
        assert!(matches!(
            finished_rx.try_recv(),
            Err(mpsc::TryRecvError::Empty)
        ));

        barrier.release().unwrap();
        drop(barrier);
        finished_rx.recv().unwrap();
        thread.join().unwrap();
        let writer = Arc::try_unwrap(writer).unwrap();
        writer.close().unwrap();
    }

    #[test]
    fn operation_barrier_poisoned_mutex_recovers_exact_held_lock_authority() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = Arc::new(LinuxLiveWriter::open(&database.main).unwrap());
        let poisoner = Arc::clone(&writer);
        assert!(std::thread::spawn(move || {
            let _barrier = poisoner
                .acquire_operation_barrier_with_cancel(|| false)
                .unwrap();
            panic!("poison operation mutex while retaining the flock");
        })
        .join()
        .is_err());
        assert!(writer.inner.is_poisoned());
        let mut writer = Arc::try_unwrap(writer).unwrap();
        writer.creator_pid = writer.creator_pid.wrapping_add(1);
        assert!(matches!(
            writer.acquire_operation_barrier_with_cancel(|| false),
            Err(LinuxLiveWriterBarrierError::Failed(
                LinuxLiveWriterBarrierCause::ForkedHandle
            ))
        ));
        writer.creator_pid = std::process::id();

        let mut barrier = match writer.acquire_operation_barrier_with_cancel(|| false) {
            Err(LinuxLiveWriterBarrierError::Locked {
                cause: LinuxLiveWriterBarrierCause::AlreadyHeld,
                barrier,
            }) => barrier,
            other => panic!("expected poisoned held authority recovery, got {other:?}"),
        };
        assert!(barrier.inner.files.as_ref().unwrap().sidecar.lock_held());
        barrier.abort().unwrap();
        drop(barrier);
        writer.close().unwrap();
        assert!(matches!(
            writer.acquire_operation_barrier_with_cancel(|| false),
            Err(LinuxLiveWriterBarrierError::Failed(
                LinuxLiveWriterBarrierCause::Closed
            ))
        ));
    }

    #[test]
    fn operation_barrier_success_failure_and_capacity_scaling_are_allocation_free() {
        for capacity in [1, 64, 1024] {
            let database = TestDatabase::new(1, 2, 2, capacity);
            let writer = LinuxLiveWriter::open(&database.main).unwrap();
            let (result, allocations) =
                count_thread_allocations(|| writer.acquire_operation_barrier_with_cancel(|| false));
            assert_eq!(allocations, 0, "capacity {capacity}");
            let mut barrier = result.unwrap();
            barrier.release().unwrap();
            drop(barrier);

            let mut malformed = [0u8; SLOT_SIZE as usize];
            malformed[4] = 1;
            database.put_raw(capacity, &malformed);
            let (result, allocations) =
                count_thread_allocations(|| writer.acquire_operation_barrier_with_cancel(|| false));
            let mut barrier = match result {
                Err(LinuxLiveWriterBarrierError::Locked {
                    cause:
                        LinuxLiveWriterBarrierCause::Pair(LinuxLivePairError::Scan(
                            LinuxSidecarScanError::Slot {
                                problem: SlotProblem::FreeNonzero,
                                ..
                            },
                        )),
                    barrier,
                }) => barrier,
                other => panic!("expected locked allocation-free failure, got {other:?}"),
            };
            assert_eq!(allocations, 0, "failure capacity {capacity}");
            barrier.abort().unwrap();
            drop(barrier);
            database.put_raw(capacity, &[0; SLOT_SIZE as usize]);
            writer.close().unwrap();
        }
    }

    #[test]
    fn post_claim_boundaries_preserve_original_cause_and_clear_exact_lease() {
        for failed_stage in [OpenStage::ClaimPublished, OpenStage::BeforeTailCleanup] {
            let database = TestDatabase::new(1, 2, 2, 1);
            let result = LinuxLiveWriter::open_with_hook(
                &database.main,
                || false,
                |stage, _, _| {
                    if stage == failed_stage {
                        Err(injected_failure())
                    } else {
                        Ok(())
                    }
                },
            );
            assert!(matches!(
                result,
                Err(LinuxLiveWriterOpenError::Failed {
                    cause: LinuxLiveWriterOpenCause::Lease(
                        LinuxWriterLeaseError::GenerationChanged
                    ),
                    cleanup_outcome: Some(_),
                })
            ));
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        }
    }

    #[test]
    fn failed_open_guard_retains_zero_slot_length_conflicts() {
        let target = 2 * PAGE_SIZE as u64;
        for actual in [target + 1, target - 1] {
            let database = TestDatabase::new(1, 2, 2, 1);
            let result = LinuxLiveWriter::open_with_hook(
                &database.main,
                || false,
                |stage, files, _| {
                    if stage == OpenStage::ClaimPublished {
                        files.main.file.set_len(actual).unwrap();
                        files
                            .sidecar
                            .write_all_at(
                                &[0; SLOT_SIZE as usize],
                                sidecar_slot_offset(database.header, 0).unwrap(),
                            )
                            .unwrap();
                        return Err(injected_failure());
                    }
                    Ok(())
                },
            );
            let mut guard = match result {
                Err(LinuxLiveWriterOpenError::CleanupRequired {
                    cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                    cleanup:
                        LinuxLiveCleanupError::Writer(LinuxWriterLeaseError::TailLengthConflict {
                            target: error_target,
                            observed_end,
                            actual: error_actual,
                        }),
                    guard,
                }) => {
                    assert_eq!(error_target, target);
                    assert_eq!(observed_end, actual.max(target));
                    assert_eq!(error_actual, actual);
                    guard
                }
                other => panic!("expected retained zero-slot length guard, got {other:?}"),
            };
            assert!(guard.files().unwrap().writer_bootstrap().is_some());
            assert!(guard.owned_writer().is_some());
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(std::fs::metadata(&database.main).unwrap().len(), actual);

            File::options()
                .write(true)
                .open(&database.main)
                .unwrap()
                .set_len(target)
                .unwrap();
            assert!(guard.retry_cleanup().unwrap().is_some());
            assert!(guard.retry_cleanup().unwrap().is_none());
        }
    }

    #[test]
    fn cancellation_before_and_after_claim_never_leaks_unreported_authority() {
        let database = TestDatabase::new(1, 2, 2, 1);
        assert!(matches!(
            LinuxLiveWriter::open_with_cancel(&database.main, || true),
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                cleanup_outcome: None,
            })
        ));
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);

        let cancel = Cell::new(false);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || cancel.get(),
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    cancel.set(true);
                }
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                cleanup_outcome: Some(_),
            })
        ));
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn full_reader_capacity_does_not_block_writer_lease() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let reader = current_active_slot(1, [0x31; 16]);
        database.put_active(1, reader);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        assert_eq!(
            decode_stable_slot(
                &database.slot(1),
                SlotRole::Reader,
                linux_slot_host_limits()
            ),
            Ok(StableSlot::Active(reader))
        );
        writer.close().unwrap();
        assert_eq!(
            decode_stable_slot(
                &database.slot(1),
                SlotRole::Reader,
                linux_slot_host_limits()
            ),
            Ok(StableSlot::Active(reader))
        );
    }

    #[test]
    fn active_writer_is_busy_and_proven_dead_writer_is_cleaned_before_claim() {
        let busy = TestDatabase::new(1, 2, 2, 1);
        let live = current_active_slot(1, [0x41; 16]);
        busy.put_active(0, live);
        assert!(matches!(
            LinuxLiveWriter::open(&busy.main),
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::WriterBusy),
                cleanup_outcome: None,
            })
        ));
        assert_eq!(
            decode_stable_slot(&busy.slot(0), SlotRole::Writer, linux_slot_host_limits()),
            Ok(StableSlot::Active(live))
        );

        let dead = TestDatabase::new(1, 2, 4, 1);
        dead.put_active(
            0,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x42; 16],
            },
        );
        let writer = LinuxLiveWriter::open(&dead.main).unwrap();
        assert_eq!(
            std::fs::metadata(&dead.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_ne!(dead.slot(0), [0; SLOT_SIZE as usize]);
        writer.close().unwrap();
    }

    #[test]
    fn dead_writer_clear_interruptions_retry_through_writer_open_cleanup() {
        for completed_writes in 0..=3 {
            let database = TestDatabase::new(1, 2, 3, 1);
            database.put_active(
                0,
                ActiveSlot {
                    txn_id: 1,
                    process_id: i32::MAX as u64,
                    process_start: 1,
                    task_id: 0,
                    nonce: [0x46; 16],
                },
            );
            let result = LinuxLiveWriter::open_with_hook(
                &database.main,
                || false,
                |stage, files, _| {
                    if stage == OpenStage::DeadWriterFound {
                        files.interrupt_dead_writer_clear_for_test(completed_writes);
                        return Err(injected_failure());
                    }
                    Ok(())
                },
            );
            assert!(matches!(
                result,
                Err(LinuxLiveWriterOpenError::Failed {
                    cause: LinuxLiveWriterOpenCause::Lease(
                        LinuxWriterLeaseError::GenerationChanged
                    ),
                    cleanup_outcome: Some(_),
                })
            ));
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                2 * PAGE_SIZE as u64
            );
        }
    }

    #[test]
    fn dead_writer_origin_guard_retains_exact_generation_across_armed_clear() {
        let database = TestDatabase::new(1, 2, 3, 1);
        database.put_active(
            0,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x47; 16],
            },
        );
        let mut changed = empty_direct_meta(2);
        changed.database_id = database.meta.database_id;
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, files, _| {
                if stage == OpenStage::DeadWriterFound {
                    files.interrupt_dead_writer_clear_for_test(1);
                    database.replace_meta_pair(changed);
                    return Err(injected_failure());
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired {
                cleanup: LinuxLiveCleanupError::Pair(LinuxLivePairError::MainGenerationChanged),
                guard,
                ..
            } => guard,
            other => panic!("expected dead-writer-origin guard, got {other:?}"),
        };
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
        database.replace_meta_pair(database.meta);
        assert!(guard.close().unwrap().is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn cancellation_after_dead_writer_discovery_routes_through_cleanup() {
        let database = TestDatabase::new(1, 2, 3, 1);
        database.put_active(
            0,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x43; 16],
            },
        );
        let cancelled = Cell::new(false);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, _, _| {
                if stage == OpenStage::DeadWriterFound {
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                cleanup_outcome: Some(_),
            })
        ));
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
    }

    #[test]
    fn cancelled_dead_writer_cleanup_failure_retains_retryable_guard() {
        let database = TestDatabase::new(1, 2, 3, 1);
        database.put_active(
            0,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x44; 16],
            },
        );
        let cancelled = Cell::new(false);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, files, _| {
                if stage == OpenStage::DeadWriterFound {
                    files.main.file.set_len(PAGE_SIZE as u64).unwrap();
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                guard,
                ..
            } => guard,
            other => panic!("expected cancelled cleanup guard, got {other:?}"),
        };
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
        guard
            .files()
            .unwrap()
            .main
            .file
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();
        database.replace_meta_pair(database.meta);
        assert!(guard.close().unwrap().is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn cancelled_dead_writer_path_replacement_retains_old_pair_guard() {
        let database = TestDatabase::new(1, 2, 2, 1);
        database.put_active(
            0,
            ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x45; 16],
            },
        );
        let old_main = database.directory.join("cancelled-old-main");
        let old_sidecar = database.directory.join("cancelled-old-sidecar");
        let cancelled = Cell::new(false);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, _, _| {
                if stage == OpenStage::DeadWriterFound {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired {
                cause: LinuxLiveWriterOpenCause::Cancelled,
                cleanup:
                    LinuxLiveCleanupError::Pair(LinuxLivePairError::Os(
                        LinuxOsError::PathIdentityMismatch,
                    )),
                ..
            } => {}
            other => panic!("expected cancelled path-replacement guard, got {other:?}"),
        }
        assert_ne!(
            slot_at(&old_sidecar, database.header, 0),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn entropy_failure_and_zero_nonce_fail_before_publication() {
        for zero in [false, true] {
            let database = TestDatabase::new(1, 2, 3, 1);
            let mut files = RetainedLiveFiles::open_locked(&database.main).unwrap();
            files.scan_and_reap().unwrap();
            let result = files.claim_writer_lease_with(|| {
                if zero {
                    Ok([0; 16])
                } else {
                    Err(LinuxOsError::RandomFailure)
                }
            });
            if zero {
                assert!(matches!(
                    result,
                    Err(LinuxWriterLeaseError::TransitionBeforeArm(_))
                ));
            } else {
                assert!(matches!(
                    result,
                    Err(LinuxWriterLeaseError::Os(LinuxOsError::RandomFailure))
                ));
            }
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert!(files.writer_tail().is_none());
        }
    }

    #[test]
    fn every_interrupted_claim_phase_returns_retryable_guard_with_exact_tail() {
        for completed_writes in 0..=3 {
            let database = TestDatabase::new(1, 2, 3, 1);
            let mut files = RetainedLiveFiles::open_locked(&database.main).unwrap();
            files.scan_and_reap().unwrap();
            let claim = files.claim_writer_lease_with_transition(
                || Ok([0x61; 16]),
                |sidecar, prepared, offset| {
                    let transition = prepared.arm();
                    let state2 = transition.state2_bytes().unwrap();
                    let body = transition.body_bytes().unwrap();
                    let state1 = transition.publish_state_bytes().unwrap();
                    sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
                        transition,
                        dead_writer: None,
                    };
                    if completed_writes >= 1 {
                        sidecar.write_all_at(&state2, offset).unwrap();
                    }
                    if completed_writes >= 2 {
                        sidecar.write_all_at(&body, offset + 4).unwrap();
                    }
                    if completed_writes >= 3 {
                        sidecar.write_all_at(&state1, offset).unwrap();
                    }
                    Err(LockedSlotExecutionError::Interrupted(InterruptedCause::Io(
                        LinuxOsError::RandomFailure,
                    )))
                },
            );
            let original = match claim.unwrap_err() {
                cause @ LinuxWriterLeaseError::Transition(
                    LockedSlotExecutionError::Interrupted(_),
                ) => LinuxLiveWriterOpenCause::Lease(cause),
                other => panic!("expected interrupted claim, got {other:?}"),
            };
            let result = failed_with_possible_cleanup(
                files,
                None,
                original,
                Some(LinuxLiveCleanupError::Writer(
                    LinuxWriterLeaseError::ArmedCleanup(SlotCleanupError::Io(
                        LinuxOsError::RandomFailure,
                    )),
                )),
            );
            let mut guard = match result.unwrap_err() {
                LinuxLiveWriterOpenError::CleanupRequired {
                    cause:
                        LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::Transition(
                            LockedSlotExecutionError::Interrupted(_),
                        )),
                    guard,
                    ..
                } => guard,
                other => panic!("expected interrupted-claim guard, got {other:?}"),
            };
            assert!(guard.files().unwrap().writer_tail().is_some());
            if completed_writes == 0 {
                assert!(matches!(
                    guard.close(),
                    Err(LinuxLiveCleanupError::Writer(
                        LinuxWriterLeaseError::TailLengthConflict {
                            target,
                            observed_end,
                            actual,
                        }
                    )) if target == 2 * PAGE_SIZE as u64
                        && observed_end == 3 * PAGE_SIZE as u64
                        && actual == 3 * PAGE_SIZE as u64
                ));
                File::options()
                    .write(true)
                    .open(&database.main)
                    .unwrap()
                    .set_len(2 * PAGE_SIZE as u64)
                    .unwrap();
            }
            assert!(guard.close().unwrap().is_some());
            assert!(guard.close().unwrap().is_none());
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                2 * PAGE_SIZE as u64
            );
        }
    }

    #[test]
    fn preclaim_generation_change_fails_without_lease_publication() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let mut next = empty_direct_meta(2);
        next.database_id = database.meta.database_id;
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ScanComplete {
                    database.replace_meta_pair(next);
                }
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                cleanup_outcome: None,
            })
        ));
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn replacement_after_claim_clears_retained_lease_and_reports_both_paths() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let old_main = database.directory.join("old-main");
        let old_sidecar = database.directory.join("old-sidecar");
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                    return Err(injected_failure());
                }
                Ok(())
            },
        );
        let outcome = match result.unwrap_err() {
            LinuxLiveWriterOpenError::Failed {
                cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                cleanup_outcome: Some(outcome),
            } => outcome,
            other => panic!("expected cleaned open failure, got {other:?}"),
        };
        assert!(matches!(
            outcome.main_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        assert!(matches!(
            outcome.sidecar_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        assert_eq!(
            slot_at(&old_sidecar, database.header, 0),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn replacement_after_claim_blocks_destructive_tail_cleanup() {
        let database = TestDatabase::new(1, 2, 3, 1);
        let old_main = database.directory.join("old-main-with-tail");
        let old_sidecar = database.directory.join("old-sidecar-with-tail");
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                }
                Ok(())
            },
        );
        match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired {
                cause:
                    LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::Os(
                        LinuxOsError::PathIdentityMismatch,
                    )),
                cleanup:
                    LinuxLiveCleanupError::Writer(LinuxWriterLeaseError::Os(
                        LinuxOsError::PathIdentityMismatch,
                    )),
                ..
            } => {}
            other => panic!("expected retained destructive-tail guard, got {other:?}"),
        }
        assert_eq!(
            std::fs::metadata(&old_main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_ne!(
            slot_at(&old_sidecar, database.header, 0),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn cleanup_guard_retains_writer_conflict_and_retries_non_consumingly() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, files, owned| {
                if stage != OpenStage::ClaimPublished {
                    return Ok(());
                }
                let owned = owned.unwrap();
                files
                    .sidecar
                    .write_all_at(
                        &encode_active_slot(ActiveSlot {
                            nonce: [0x55; 16],
                            ..owned.active
                        }),
                        sidecar_slot_offset(owned.header, 0).unwrap(),
                    )
                    .unwrap();
                Err(injected_failure())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired {
                cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                guard,
                ..
            } => guard,
            other => panic!("expected writer cleanup guard, got {other:?}"),
        };
        assert!(matches!(
            guard.retry_cleanup(),
            Err(LinuxLiveCleanupError::Writer(_))
        ));
        let owned = guard.owned_writer().unwrap();
        guard
            .files()
            .unwrap()
            .sidecar
            .write_all_at(
                &encode_active_slot(owned.active),
                sidecar_slot_offset(owned.header, 0).unwrap(),
            )
            .unwrap();
        assert!(guard.close().unwrap().is_some());
        assert!(guard.close().unwrap().is_none());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn tail_truncate_and_sync_faults_return_original_cause_after_exact_cleanup() {
        for fail_sync in [false, true] {
            let database = TestDatabase::new(1, 2, 3, 1);
            let result = LinuxLiveWriter::open_with_hook_and_io(
                &database.main,
                || false,
                |_, _, _| Ok(()),
                |file, length| {
                    if fail_sync {
                        file.set_len(length)
                    } else {
                        Err(io::Error::other("injected truncate failure"))
                    }
                },
                |file| {
                    if fail_sync {
                        Err(io::Error::other("injected sync failure"))
                    } else {
                        file.sync_all()
                    }
                },
            );
            assert!(matches!(
                result,
                Err(LinuxLiveWriterOpenError::Failed {
                    cause: LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::Os(
                        LinuxOsError::Io { .. }
                    )),
                    cleanup_outcome: Some(_),
                })
            ));
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                2 * PAGE_SIZE as u64
            );
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        }
    }

    #[test]
    fn growth_first_observed_after_claim_is_frozen_and_cleaned_before_exposure() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    File::options()
                        .write(true)
                        .open(&database.main)
                        .unwrap()
                        .set_len(3 * PAGE_SIZE as u64)
                        .unwrap();
                }
                Ok(())
            },
        )
        .unwrap();
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
        writer.close().unwrap();
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn close_discovers_unrecorded_growth_before_clearing_the_lease() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();

        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .writer_tail()
            .is_none());
        assert!(writer.close().unwrap().is_some());
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn cleanup_retry_rejects_growth_beyond_the_first_frozen_bound() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();
        assert!(matches!(
            writer.close_with_io(
                |_, _| Err(io::Error::other("injected truncate failure")),
                |file| file.sync_all(),
            ),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::Os(LinuxOsError::Io { .. })
            ))
        ));
        let inner = writer.lock_inner();
        assert_eq!(inner.state, WriterState::CleanupOnly);
        assert_eq!(
            inner
                .files
                .as_ref()
                .unwrap()
                .writer_tail()
                .unwrap()
                .observed_end_exclusive,
            3 * PAGE_SIZE as u64
        );
        drop(inner);

        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(4 * PAGE_SIZE as u64)
            .unwrap();
        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::TailLengthConflict {
                    observed_end,
                    actual,
                    ..
                }
            )) if observed_end == 3 * PAGE_SIZE as u64
                && actual == 4 * PAGE_SIZE as u64
        ));
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);

        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();
        assert!(writer.close().unwrap().is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            2 * PAGE_SIZE as u64
        );
    }

    #[test]
    fn close_never_clears_a_nonzero_foreign_writer_lease() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let owned_active = writer.lock_inner().owned.as_ref().unwrap().active;
        let foreign = ActiveSlot {
            nonce: [0x54; 16],
            ..owned_active
        };
        database.put_active(0, foreign);

        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::OwnerMismatch
            ))
        ));
        assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
        assert_eq!(
            decode_stable_slot(
                &database.slot(0),
                SlotRole::Writer,
                linux_slot_host_limits()
            ),
            Ok(StableSlot::Active(foreign))
        );

        database.put_active(0, owned_active);
        assert!(writer.close().unwrap().is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn postclaim_generation_authority_mutations_retain_the_exact_lease() {
        #[derive(Clone, Copy)]
        enum Mutation {
            Nonce,
            RootsAndCounts,
            SelectionProof,
            LaterTransaction,
        }

        for mutation in [
            Mutation::Nonce,
            Mutation::RootsAndCounts,
            Mutation::SelectionProof,
            Mutation::LaterTransaction,
        ] {
            let committed_pages = if matches!(mutation, Mutation::RootsAndCounts) {
                3
            } else {
                2
            };
            let database = TestDatabase::new(1, committed_pages, committed_pages, 1);
            let result = LinuxLiveWriter::open_with_hook(
                &database.main,
                || false,
                |stage, _, _| {
                    if stage != OpenStage::ClaimPublished {
                        return Ok(());
                    }
                    match mutation {
                        Mutation::Nonce => {
                            let mut changed = database.meta;
                            changed.commit_nonce = [0x35; 16];
                            database.replace_meta_pair(changed);
                        }
                        Mutation::RootsAndCounts => {
                            let mut changed = database.meta;
                            changed.range_root = 2;
                            changed.range_record_count = 1;
                            database.replace_meta_pair(changed);
                        }
                        Mutation::SelectionProof => {
                            database.replace_meta_page(0, None);
                        }
                        Mutation::LaterTransaction => {
                            let mut changed = empty_direct_meta(2);
                            changed.database_id = database.meta.database_id;
                            changed.page_count = database.meta.page_count;
                            database.replace_meta_pair(changed);
                        }
                    }
                    Ok(())
                },
            );
            let mut guard = match (mutation, result.unwrap_err()) {
                (
                    Mutation::SelectionProof,
                    LinuxLiveWriterOpenError::CleanupRequired {
                        cause:
                            LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::Os(
                                LinuxOsError::Bootstrap(
                                    BootstrapError::CurrentGenerationUnprovable,
                                ),
                            )),
                        cleanup:
                            LinuxLiveCleanupError::Writer(LinuxWriterLeaseError::Os(
                                LinuxOsError::Bootstrap(
                                    BootstrapError::CurrentGenerationUnprovable,
                                ),
                            )),
                        guard,
                    },
                ) => guard,
                (
                    Mutation::Nonce | Mutation::RootsAndCounts | Mutation::LaterTransaction,
                    LinuxLiveWriterOpenError::CleanupRequired {
                        cause:
                            LinuxLiveWriterOpenCause::Lease(LinuxWriterLeaseError::GenerationChanged),
                        cleanup:
                            LinuxLiveCleanupError::Writer(LinuxWriterLeaseError::GenerationChanged),
                        guard,
                    },
                ) => guard,
                (_, other) => panic!("expected exact-authority cleanup guard, got {other:?}"),
            };
            assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert!(guard.files().unwrap().writer_bootstrap().is_some());
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                committed_pages as u64 * PAGE_SIZE as u64
            );

            database.replace_meta_pair(database.meta);
            assert!(guard.close().unwrap().is_some());
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        }
    }

    #[test]
    fn generation_change_and_unexpected_growth_retain_exact_tail_guard() {
        for change_generation in [false, true] {
            let database = TestDatabase::new(1, 2, 3, 1);
            let mut replacement = empty_direct_meta(2);
            replacement.database_id = database.meta.database_id;
            let result = LinuxLiveWriter::open_with_hook(
                &database.main,
                || false,
                |stage, _, _| {
                    if stage == OpenStage::ClaimPublished {
                        if change_generation {
                            database.replace_meta_pair(replacement);
                        } else {
                            File::options()
                                .write(true)
                                .open(&database.main)
                                .unwrap()
                                .set_len(4 * PAGE_SIZE as u64)
                                .unwrap();
                        }
                    }
                    Ok(())
                },
            );
            let mut guard = match result.unwrap_err() {
                LinuxLiveWriterOpenError::CleanupRequired { guard, .. } => guard,
                other => panic!("expected retained tail guard, got {other:?}"),
            };
            assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                if change_generation { 3 } else { 4 } * PAGE_SIZE as u64
            );
            if change_generation {
                database.replace_meta_pair(database.meta);
            } else {
                File::options()
                    .write(true)
                    .open(&database.main)
                    .unwrap()
                    .set_len(3 * PAGE_SIZE as u64)
                    .unwrap();
            }
            assert!(guard.close().unwrap().is_some());
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                2 * PAGE_SIZE as u64
            );
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        }
    }

    #[test]
    fn established_close_resolves_tail_before_clear_and_retries_close_only() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();
        let tail = tail_for(&writer, 3);
        writer
            .lock_inner()
            .files
            .as_mut()
            .unwrap()
            .set_writer_tail_for_test(Some(tail));
        assert!(matches!(
            writer.close_with_io(
                |file, length| file.set_len(length),
                |_| Err(io::Error::other("injected close sync failure")),
            ),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::Os(LinuxOsError::Io { .. })
            ))
        ));
        assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .writer_tail()
            .is_some());
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn first_close_accepts_exact_already_zero_lease_without_tail() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .write_all_at(
                &[0; SLOT_SIZE as usize],
                sidecar_slot_offset(database.header, 0).unwrap(),
            )
            .unwrap();
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
    }

    #[test]
    fn exact_zero_without_tail_ignores_obsolete_main_generation_and_paths() {
        #[derive(Clone, Copy)]
        enum Mutation {
            LaterTransaction,
            SameTransactionNonce,
            PathReplacement,
        }

        for mutation in [
            Mutation::LaterTransaction,
            Mutation::SameTransactionNonce,
            Mutation::PathReplacement,
        ] {
            let database = TestDatabase::new(1, 2, 2, 1);
            let writer = LinuxLiveWriter::open(&database.main).unwrap();
            writer
                .lock_inner()
                .files
                .as_ref()
                .unwrap()
                .sidecar
                .write_all_at(
                    &[0; SLOT_SIZE as usize],
                    sidecar_slot_offset(database.header, 0).unwrap(),
                )
                .unwrap();
            let old_main = database.directory.join("zero-old-main");
            let old_sidecar = database.directory.join("zero-old-sidecar");
            match mutation {
                Mutation::LaterTransaction => {
                    let mut changed = empty_direct_meta(2);
                    changed.database_id = database.meta.database_id;
                    changed.page_count = 2;
                    database.replace_meta_pair(changed);
                }
                Mutation::SameTransactionNonce => {
                    let mut changed = database.meta;
                    changed.commit_nonce = [0x72; 16];
                    database.replace_meta_pair(changed);
                }
                Mutation::PathReplacement => {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                }
            }

            let outcome = writer.close().unwrap().unwrap();
            if matches!(mutation, Mutation::PathReplacement) {
                assert!(matches!(
                    outcome.main_path,
                    Err(LinuxOsError::PathIdentityMismatch)
                ));
                assert!(matches!(
                    outcome.sidecar_path,
                    Err(LinuxOsError::PathIdentityMismatch)
                ));
                assert_eq!(
                    slot_at(&old_sidecar, database.header, 0),
                    [0; SLOT_SIZE as usize]
                );
            } else {
                assert!(outcome.main_path.is_ok());
                assert!(outcome.sidecar_path.is_ok());
                assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            }
            assert!(writer.close().unwrap().is_none());
        }
    }

    #[test]
    fn exact_zero_does_not_bypass_a_recorded_tail_obligation() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(3 * PAGE_SIZE as u64)
            .unwrap();
        let tail = tail_for(&writer, 3);
        {
            let mut inner = writer.lock_inner();
            let files = inner.files.as_mut().unwrap();
            files.set_writer_tail_for_test(Some(tail));
            files
                .sidecar
                .write_all_at(
                    &[0; SLOT_SIZE as usize],
                    sidecar_slot_offset(database.header, 0).unwrap(),
                )
                .unwrap();
        }

        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::TailLengthConflict {
                    target,
                    observed_end,
                    actual,
                }
            )) if target == 2 * PAGE_SIZE as u64
                && observed_end == 3 * PAGE_SIZE as u64
                && actual == 3 * PAGE_SIZE as u64
        ));
        assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .writer_tail()
            .is_some());

        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(2 * PAGE_SIZE as u64)
            .unwrap();
        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::OwnerMismatch
            ))
        ));
        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .writer_tail()
            .is_some());
    }

    #[test]
    fn exact_zero_rejects_one_byte_unpublished_append_and_retains_cleanup() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = 2 * PAGE_SIZE as u64;
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .write_all_at(&[0xa5], target)
            .unwrap();
        {
            let inner = writer.lock_inner();
            let files = inner.files.as_ref().unwrap();
            assert!(files.writer_tail().is_none());
            files
                .sidecar
                .write_all_at(
                    &[0; SLOT_SIZE as usize],
                    sidecar_slot_offset(database.header, 0).unwrap(),
                )
                .unwrap();
        }

        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::TailLengthConflict {
                    target: error_target,
                    observed_end,
                    actual,
                }
            )) if error_target == target && observed_end == target + 1 && actual == target + 1
        ));
        assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
        assert_eq!(std::fs::metadata(&database.main).unwrap().len(), target + 1);
        let inner = writer.lock_inner();
        assert!(inner.files.as_ref().unwrap().writer_tail().is_none());
        assert!(inner.files.as_ref().unwrap().writer_bootstrap().is_some());
        drop(inner);
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);

        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(target)
            .unwrap();
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
    }

    #[test]
    fn exact_zero_rejects_unaligned_short_main_and_retains_cleanup() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        let target = 2 * PAGE_SIZE as u64;
        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(target - 1)
            .unwrap();
        writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .write_all_at(
                &[0; SLOT_SIZE as usize],
                sidecar_slot_offset(database.header, 0).unwrap(),
            )
            .unwrap();

        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::TailLengthConflict {
                    target: error_target,
                    observed_end,
                    actual,
                }
            )) if error_target == target && observed_end == target && actual == target - 1
        ));
        assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
        assert_eq!(std::fs::metadata(&database.main).unwrap().len(), target - 1);
        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .writer_bootstrap()
            .is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);

        File::options()
            .write(true)
            .open(&database.main)
            .unwrap()
            .set_len(target)
            .unwrap();
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
    }

    #[test]
    fn exact_zero_does_not_bypass_armed_writer_clear_provenance() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let writer = LinuxLiveWriter::open(&database.main).unwrap();
        {
            let mut inner = writer.lock_inner();
            let owned = inner.owned.as_ref().unwrap();
            let (header, active) = (owned.header, owned.active);
            let files = inner.files.as_mut().unwrap();
            files
                .sidecar
                .acquire_lock(LockMode::Exclusive, false)
                .unwrap();
            let current = files.sidecar.read_sidecar_slot(header, 0).unwrap();
            let prepared = PreparedSlotTransition::clear_owned(
                header,
                SlotRole::Writer,
                0,
                &current,
                active,
                linux_slot_host_limits(),
            )
            .unwrap();
            files.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
                transition: prepared.arm(),
                dead_writer: None,
            };
            files
                .sidecar
                .write_all_at(
                    &[0; SLOT_SIZE as usize],
                    sidecar_slot_offset(database.header, 0).unwrap(),
                )
                .unwrap();
        }
        let mut changed = empty_direct_meta(2);
        changed.database_id = database.meta.database_id;
        database.replace_meta_pair(changed);

        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::Cleanup(
                LinuxWriterLeaseError::GenerationChanged
            ))
        ));
        assert!(writer
            .lock_inner()
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .has_armed_writer_transition());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);

        database.replace_meta_pair(database.meta);
        assert!(writer.close().unwrap().is_some());
        assert!(writer.close().unwrap().is_none());
    }

    #[test]
    fn armed_zero_slot_rejects_unaligned_main_lengths_and_retains_cleanup() {
        let target = 2 * PAGE_SIZE as u64;
        for actual in [target + 1, target - 1] {
            let database = TestDatabase::new(1, 2, 2, 1);
            let writer = LinuxLiveWriter::open(&database.main).unwrap();
            {
                let mut inner = writer.lock_inner();
                let owned = inner.owned.as_ref().unwrap();
                let (header, active) = (owned.header, owned.active);
                let files = inner.files.as_mut().unwrap();
                files
                    .sidecar
                    .acquire_lock(LockMode::Exclusive, false)
                    .unwrap();
                let current = files.sidecar.read_sidecar_slot(header, 0).unwrap();
                let prepared = PreparedSlotTransition::clear_owned(
                    header,
                    SlotRole::Writer,
                    0,
                    &current,
                    active,
                    linux_slot_host_limits(),
                )
                .unwrap();
                files.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
                    transition: prepared.arm(),
                    dead_writer: None,
                };
                files
                    .sidecar
                    .write_all_at(
                        &[0; SLOT_SIZE as usize],
                        sidecar_slot_offset(database.header, 0).unwrap(),
                    )
                    .unwrap();
                files.main.file.set_len(actual).unwrap();
            }

            assert!(matches!(
                writer.close(),
                Err(LinuxLiveWriterCloseError::Cleanup(
                    LinuxWriterLeaseError::TailLengthConflict {
                        target: error_target,
                        observed_end,
                        actual: error_actual,
                    }
                )) if error_target == target
                    && observed_end == actual.max(target)
                    && error_actual == actual
            ));
            assert_eq!(writer.lock_inner().state, WriterState::CleanupOnly);
            assert!(writer
                .lock_inner()
                .files
                .as_ref()
                .unwrap()
                .sidecar
                .has_armed_writer_transition());
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(std::fs::metadata(&database.main).unwrap().len(), actual);

            File::options()
                .write(true)
                .open(&database.main)
                .unwrap()
                .set_len(target)
                .unwrap();
            assert!(writer.close().unwrap().is_some());
            assert!(writer.close().unwrap().is_none());
        }
    }

    #[test]
    fn creator_check_precedes_cleanup_and_drop_never_mutates_coordination() {
        let database = TestDatabase::new(1, 2, 2, 1);
        let mut writer = LinuxLiveWriter::open(&database.main).unwrap();
        let active = database.slot(0);
        writer.creator_pid = writer.creator_pid.wrapping_add(1);
        assert!(matches!(
            writer.close(),
            Err(LinuxLiveWriterCloseError::ForkedHandle)
        ));
        assert_eq!(writer.lock_inner().state, WriterState::Open);
        assert_eq!(database.slot(0), active);
        writer.creator_pid = std::process::id();
        drop(writer);
        assert_eq!(database.slot(0), active);
    }

    #[test]
    fn cleanup_guard_creator_check_precedes_writer_tail_or_slot_mutation() {
        let database = TestDatabase::new(1, 2, 3, 1);
        let result = LinuxLiveWriter::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    File::options()
                        .write(true)
                        .open(&database.main)
                        .unwrap()
                        .set_len(4 * PAGE_SIZE as u64)
                        .unwrap();
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveWriterOpenError::CleanupRequired { guard, .. } => guard,
            other => panic!("expected retained guard, got {other:?}"),
        };
        let active = database.slot(0);
        guard.make_forked_for_test();
        assert!(matches!(
            guard.retry_cleanup(),
            Err(LinuxLiveCleanupError::ForkedHandle)
        ));
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            4 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), active);
    }
}
