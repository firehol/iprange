//! Public recovery entry points.

use std::path::Path;

use crate::cancellation::CancellationToken;

use super::{
    RecoveryBudget, RecoveryCandidate, RecoveryPreparationFailure, RecoveryResult, RecoverySink,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OfflineQuiescenceCertification {
    CallerCertified,
}

pub type RecoveryOutcome = std::result::Result<RecoveryResult, Box<RecoveryPreparationFailure>>;

pub fn recover_immutable<S: RecoverySink>(
    source_path: impl AsRef<Path>,
    candidate: RecoveryCandidate,
    destination_path: impl AsRef<Path>,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> RecoveryOutcome {
    crate::worker::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        WorkerMode::Immutable,
        budget,
        sink,
        cancellation,
    )
}

pub fn recover_offline<S: RecoverySink>(
    source_path: impl AsRef<Path>,
    candidate: RecoveryCandidate,
    destination_path: impl AsRef<Path>,
    certification: OfflineQuiescenceCertification,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> RecoveryOutcome {
    let _ = certification;
    crate::worker::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        WorkerMode::Offline,
        budget,
        sink,
        cancellation,
    )
}

pub fn recover_live<S: RecoverySink>(
    source_path: impl AsRef<Path>,
    candidate: RecoveryCandidate,
    destination_path: impl AsRef<Path>,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
) -> RecoveryOutcome {
    if let Err(cause) = crate::live_lock::require_live_supported() {
        return Err(Box::new(RecoveryPreparationFailure::early(cause)));
    }
    crate::worker::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        WorkerMode::Live,
        budget,
        sink,
        cancellation,
    )
}

#[cfg(any(unix, windows))]
mod platform {
    use crate::contract::{MetaV4, ValueKind};
    use crate::error::Error;
    use crate::immutable_output::{Builder, Finished, OutputBudget, OutputSpec};
    use crate::publication::cleanup;
    use crate::publication::output::OutputAttempt;
    use crate::publication::problem::Problem;
    use crate::publication::{self, PublicationProblem};
    use crate::random;

    use super::*;
    use crate::recovery::direct_build;
    use crate::recovery::membership_build;
    use crate::recovery::source_guard::{problem, Source, SourceMode};
    use crate::recovery::terminal;
    use crate::recovery::{RecoveryReport, ScratchCleanup};

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    pub(crate) enum Mode {
        Immutable,
        Offline,
        Live,
    }

    struct Built {
        finished: Finished,
        report: RecoveryReport,
        scratch: Option<ScratchCleanup>,
    }

    struct BuildFailure {
        builder: Builder,
        cause: Error,
        report: RecoveryReport,
        scratch: Option<ScratchCleanup>,
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn recover_precreated<S: RecoverySink>(
        source_path: &Path,
        candidate: RecoveryCandidate,
        mode: Mode,
        budget: &RecoveryBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
        attempt: OutputAttempt,
        file: std::fs::File,
    ) -> RecoveryOutcome {
        let effective = validate_budget(budget, mode)?;
        let source = match open_source(source_path, candidate, mode, cancellation) {
            Ok(source) => source,
            Err(mut failure) => {
                let discarded = cleanup::discard_attempt(&attempt, &file);
                failure.output = Some(discarded.output);
                if let Some(artifact) = discarded.artifact {
                    failure.cleanup.push(artifact);
                }
                failure.housekeeping = discarded.housekeeping;
                failure.visible_housekeeping = discarded.visible_housekeeping;
                return Err(failure);
            }
        };
        if source.identity().bytes == attempt.identity().encode() {
            return Err(fail_attempt(
                source,
                attempt,
                file,
                Error::InvalidArgument("source and recovery output identities match"),
                RecoveryReport::default(),
                None,
            ));
        }
        construct(source, attempt, file, &effective, sink, cancellation)
    }

    pub(crate) fn validate_budget(
        budget: &RecoveryBudget,
        mode: Mode,
    ) -> std::result::Result<RecoveryBudget, Box<RecoveryPreparationFailure>> {
        budget
            .validate()
            .map_err(|cause| Box::new(RecoveryPreparationFailure::early(cause)))?;
        let reserved = u32::from(matches!(mode, Mode::Live));
        let minimum = 2u32.checked_add(reserved).ok_or_else(|| {
            Box::new(RecoveryPreparationFailure::early(
                Error::ArithmeticOverflow("recovery open files"),
            ))
        })?;
        if budget.max_open_files < minimum {
            return Err(Box::new(RecoveryPreparationFailure::early(
                Error::BudgetExceeded("recovery source and output files"),
            )));
        }
        let mut effective = budget.clone();
        effective.max_open_files -= reserved;
        Ok(effective)
    }

    fn open_source(
        path: &Path,
        candidate: RecoveryCandidate,
        mode: Mode,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Source, Box<RecoveryPreparationFailure>> {
        let source_mode = match mode {
            Mode::Immutable => SourceMode::Immutable,
            Mode::Offline => SourceMode::Offline,
            Mode::Live => SourceMode::Live,
        };
        Source::open(path, candidate, source_mode, cancellation).map_err(|failure| {
            Box::new(RecoveryPreparationFailure::new(
                problem(&failure.cause),
                RecoveryReport::default(),
                None,
                None,
                None,
                failure.guard,
            ))
        })
    }

    fn construct<S: RecoverySink>(
        source: Source,
        attempt: OutputAttempt,
        file: std::fs::File,
        budget: &RecoveryBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> RecoveryOutcome {
        let meta = source.meta();
        let spec = match output_spec(meta) {
            Ok(spec) => spec,
            Err(cause) => {
                return Err(fail_attempt(
                    source,
                    attempt,
                    file,
                    cause,
                    RecoveryReport::default(),
                    None,
                ))
            }
        };
        let builder = match Builder::new_owned(
            file,
            spec,
            OutputBudget {
                max_output_pages: budget.max_output_pages,
            },
        ) {
            Ok(builder) => builder,
            Err(failure) => {
                return Err(fail_attempt(
                    source,
                    attempt,
                    failure.file,
                    failure.cause,
                    RecoveryReport::default(),
                    None,
                ))
            }
        };
        let source_probe = match crate::worker::enter_source(source.mapping()) {
            Ok(probe) => probe,
            Err(cause) => {
                return Err(fail_attempt(
                    source,
                    attempt,
                    builder.into_file(),
                    cause,
                    RecoveryReport::default(),
                    None,
                ))
            }
        };
        let built = match build(source.mapping(), meta, builder, budget, cancellation, sink) {
            Ok(built) => built,
            Err(failure) => {
                let failure = *failure;
                let file = failure.builder.into_file();
                return Err(fail_attempt(
                    source,
                    attempt,
                    file,
                    failure.cause,
                    failure.report,
                    failure.scratch,
                ));
            }
        };
        drop(source_probe);
        finish(source, attempt, meta, built, cancellation)
    }

    fn output_spec(meta: MetaV4) -> crate::error::Result<OutputSpec> {
        Ok(OutputSpec {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: random::nonzero_128()?,
            transaction_id: 1,
            commit_nonce: random::nonzero_128()?,
            feed_index_limit: meta.feed_index_limit,
        })
    }

    fn build<S: RecoverySink>(
        mapping: &crate::mapping::Mapping,
        meta: MetaV4,
        builder: Builder,
        budget: &RecoveryBudget,
        cancellation: &CancellationToken,
        sink: &mut S,
    ) -> std::result::Result<Built, Box<BuildFailure>> {
        match meta.value_kind {
            ValueKind::Direct => {
                direct_build::construct(mapping, meta, builder, budget, cancellation, sink)
                    .map(|built| Built {
                        finished: built.finished,
                        report: built.report,
                        scratch: built.scratch,
                    })
                    .map_err(|failure| {
                        Box::new(BuildFailure {
                            builder: failure.builder,
                            cause: failure.cause,
                            report: failure.report,
                            scratch: failure.scratch,
                        })
                    })
            }
            ValueKind::Membership => {
                membership_build::construct(mapping, meta, builder, budget, cancellation, sink)
                    .map(|built| Built {
                        finished: built.finished,
                        report: built.report,
                        scratch: built.scratch,
                    })
                    .map_err(|failure| {
                        Box::new(BuildFailure {
                            builder: failure.builder,
                            cause: failure.cause,
                            report: failure.report,
                            scratch: failure.scratch,
                        })
                    })
            }
        }
    }

    fn finish(
        source: Source,
        attempt: OutputAttempt,
        source_meta: MetaV4,
        built: Built,
        cancellation: &CancellationToken,
    ) -> RecoveryOutcome {
        if let Err(cause) = crate::worker::checkpoint_recovery_progress(&built.report) {
            let discarded = cleanup::discard_attempt(&attempt, &built.finished.file);
            return Err(fail_source(
                source,
                problem(&cause),
                built.report,
                Some(discarded),
                built.scratch,
            ));
        }
        let source_probe = match crate::worker::enter_source(source.mapping()) {
            Ok(probe) => probe,
            Err(cause) => {
                let discarded = cleanup::discard_attempt(&attempt, &built.finished.file);
                return Err(fail_source(
                    source,
                    problem(&cause),
                    built.report,
                    Some(discarded),
                    built.scratch,
                ));
            }
        };
        let end = source.finish(source_meta, cancellation);
        drop(source_probe);
        if let Some(cause) = end.cause {
            let discarded = cleanup::discard_attempt(&attempt, &built.finished.file);
            return Err(Box::new(RecoveryPreparationFailure::discarded(
                problem(&cause),
                built.report,
                discarded,
                built.scratch,
                end.guard,
            )));
        }
        debug_assert!(end.guard.is_none());
        let prepared = match attempt.prepare_cancellable(built.finished, cancellation) {
            Ok(prepared) => prepared,
            Err(failure) => {
                let discarded =
                    cleanup::discard_attempt(&failure.owner.attempt, &failure.owner.finished.file);
                return Err(Box::new(RecoveryPreparationFailure::discarded(
                    Problem::output(&failure.cause),
                    built.report,
                    discarded,
                    built.scratch,
                    None,
                )));
            }
        };
        let checkpoint_report = &built.report;
        let checkpoint_scratch = &built.scratch;
        match publication::attempt::fail_if_exists_cancellable_observed(
            prepared,
            cancellation,
            |checkpoint| {
                let outcome = match checkpoint {
                    publication::attempt::PublicationCheckpoint::Preparation(failure) => {
                        Err(Box::new(RecoveryPreparationFailure::from_publication(
                            failure.clone(),
                            checkpoint_report.clone(),
                            checkpoint_scratch.clone(),
                        )))
                    }
                    publication::attempt::PublicationCheckpoint::Result(result) => {
                        Ok(terminal::completed(
                            checkpoint_report.clone(),
                            checkpoint_scratch.clone(),
                            result.clone(),
                        ))
                    }
                };
                crate::worker::checkpoint_recovery(&outcome).map_err(|cause| Problem::sdk(&cause))
            },
        ) {
            Ok(result) => Ok(terminal::completed(built.report, built.scratch, result)),
            Err(failure) => Err(Box::new(RecoveryPreparationFailure::from_publication(
                *failure,
                built.report,
                built.scratch,
            ))),
        }
    }

    fn fail_attempt(
        source: Source,
        attempt: OutputAttempt,
        file: std::fs::File,
        cause: Error,
        report: RecoveryReport,
        scratch: Option<ScratchCleanup>,
    ) -> Box<RecoveryPreparationFailure> {
        let cause = match crate::worker::checkpoint_recovery_progress(&report) {
            Ok(()) => cause,
            Err(checkpoint) => checkpoint,
        };
        let discarded = cleanup::discard_attempt(&attempt, &file);
        fail_source(source, problem(&cause), report, Some(discarded), scratch)
    }

    fn fail_source(
        source: Source,
        cause: PublicationProblem,
        report: RecoveryReport,
        discarded: Option<cleanup::EarlyDiscard>,
        scratch: Option<ScratchCleanup>,
    ) -> Box<RecoveryPreparationFailure> {
        let end = source.release_only();
        match discarded {
            Some(discarded) => Box::new(RecoveryPreparationFailure::discarded(
                cause, report, discarded, scratch, end.guard,
            )),
            None => Box::new(RecoveryPreparationFailure::new(
                cause, report, None, None, scratch, end.guard,
            )),
        }
    }
}

#[cfg(not(any(unix, windows)))]
mod platform {
    use crate::error::Error;

    use super::*;

    #[derive(Clone, Copy)]
    pub(crate) enum Mode {
        Immutable,
        Offline,
        Live,
    }

    pub(crate) fn recover_precreated<S: RecoverySink>(
        _source_path: &Path,
        _candidate: RecoveryCandidate,
        _mode: Mode,
        _budget: &RecoveryBudget,
        _sink: &mut S,
        _cancellation: &CancellationToken,
        _attempt: crate::publication::output::OutputAttempt,
        _file: std::fs::File,
    ) -> RecoveryOutcome {
        Err(Box::new(RecoveryPreparationFailure::early(
            Error::Unsupported("recovery publication is not implemented on this platform"),
        )))
    }

    pub(crate) fn validate_budget(
        _budget: &RecoveryBudget,
        _mode: Mode,
    ) -> std::result::Result<RecoveryBudget, Box<RecoveryPreparationFailure>> {
        Err(Box::new(RecoveryPreparationFailure::early(
            Error::Unsupported("recovery publication is not implemented on this platform"),
        )))
    }
}

pub(crate) use platform::Mode as WorkerMode;

#[allow(clippy::too_many_arguments)]
pub(crate) fn recover_precreated_local<S: RecoverySink>(
    source_path: &Path,
    candidate: RecoveryCandidate,
    mode: WorkerMode,
    budget: &RecoveryBudget,
    sink: &mut S,
    cancellation: &CancellationToken,
    attempt: crate::publication::output::OutputAttempt,
    file: std::fs::File,
) -> RecoveryOutcome {
    platform::recover_precreated(
        source_path,
        candidate,
        mode,
        budget,
        sink,
        cancellation,
        attempt,
        file,
    )
}

pub(crate) fn validate_worker_budget(
    budget: &RecoveryBudget,
    mode: WorkerMode,
) -> std::result::Result<(), Box<RecoveryPreparationFailure>> {
    platform::validate_budget(budget, mode).map(|_| ())
}
