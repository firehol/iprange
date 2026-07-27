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
    platform::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        platform::Mode::Immutable,
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
    platform::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        platform::Mode::Offline,
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
    platform::recover(
        source_path.as_ref(),
        candidate,
        destination_path.as_ref(),
        platform::Mode::Live,
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
    use crate::publication::output::{CreatedOutput, OutputAttempt};
    use crate::publication::problem::Problem;
    use crate::publication::{self, PublicationProblem};
    use crate::random;

    use super::*;
    use crate::recovery::direct_build;
    use crate::recovery::membership_build;
    use crate::recovery::source_guard::{problem, Source, SourceMode};
    use crate::recovery::terminal;
    use crate::recovery::{RecoveryReport, ScratchCleanup};

    #[derive(Clone, Copy)]
    pub(super) enum Mode {
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

    pub(super) fn recover<S: RecoverySink>(
        source_path: &Path,
        candidate: RecoveryCandidate,
        destination_path: &Path,
        mode: Mode,
        budget: &RecoveryBudget,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> RecoveryOutcome {
        let effective = validate_budget(budget, mode)?;
        let source = open_source(source_path, candidate, mode, cancellation)?;
        let created = match CreatedOutput::create_absent(destination_path) {
            Ok(created) => created,
            Err(cause) => {
                return Err(fail_source(
                    source,
                    Problem::output(&cause),
                    RecoveryReport::default(),
                    None,
                    None,
                ))
            }
        };
        let secured = match created.secure() {
            Ok(secured) => secured,
            Err(failure) => {
                return Err(fail_source(
                    source,
                    Problem::output(&failure.cause),
                    RecoveryReport::default(),
                    Some(cleanup::discard_created(&failure.owner)),
                    None,
                ));
            }
        };
        let (attempt, file) = secured.into_parts();
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

    fn validate_budget(
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
                max_heap_bytes: budget.max_heap_bytes,
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
        let built = match build(source.file(), meta, builder, budget, cancellation, sink) {
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
        file: &std::fs::File,
        meta: MetaV4,
        builder: Builder,
        budget: &RecoveryBudget,
        cancellation: &CancellationToken,
        sink: &mut S,
    ) -> std::result::Result<Built, Box<BuildFailure>> {
        match meta.value_kind {
            ValueKind::Direct => {
                direct_build::construct(file, meta, builder, budget, cancellation, sink)
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
                membership_build::construct(file, meta, builder, budget, cancellation, sink)
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
        let end = source.finish(source_meta, cancellation);
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
        match publication::attempt::fail_if_exists_cancellable(prepared, cancellation) {
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
    pub(super) enum Mode {
        Immutable,
        Offline,
        Live,
    }

    pub(super) fn recover<S: RecoverySink>(
        _source_path: &Path,
        _candidate: RecoveryCandidate,
        _destination_path: &Path,
        _mode: Mode,
        _budget: &RecoveryBudget,
        _sink: &mut S,
        _cancellation: &CancellationToken,
    ) -> RecoveryOutcome {
        Err(Box::new(RecoveryPreparationFailure::early(
            Error::Unsupported("recovery publication is not implemented on this platform"),
        )))
    }
}
