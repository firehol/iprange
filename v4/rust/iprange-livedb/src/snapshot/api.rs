//! Public compact-snapshot operation.

use std::path::Path;

use crate::cancellation::CancellationToken;

use super::{
    SnapshotBudget, SnapshotOutcome, SnapshotPreparationFailure, SnapshotPublicationPolicy,
    SnapshotResult, SnapshotSourceMode,
};

pub fn snapshot_to(
    source_path: impl AsRef<Path>,
    source_mode: SnapshotSourceMode,
    destination_path: impl AsRef<Path>,
    publication_policy: SnapshotPublicationPolicy,
    budget: &SnapshotBudget,
    cancellation: &CancellationToken,
) -> SnapshotOutcome {
    platform::snapshot(
        source_path.as_ref(),
        source_mode,
        destination_path.as_ref(),
        publication_policy,
        budget,
        cancellation,
    )
}

#[cfg(any(unix, windows))]
mod platform {
    use crate::error::Error;
    use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
    use crate::publication::cleanup;
    use crate::publication::output::{CreatedOutput, OutputAttempt};
    use crate::publication::problem::Problem;
    use crate::publication::{self, PublicationProblem};
    use crate::recovery::source_guard::{problem, CurrentSourceMode, Source};

    use super::*;
    use crate::snapshot::build;

    pub(super) fn snapshot(
        source_path: &Path,
        source_mode: SnapshotSourceMode,
        destination_path: &Path,
        publication_policy: SnapshotPublicationPolicy,
        budget: &SnapshotBudget,
        cancellation: &CancellationToken,
    ) -> SnapshotOutcome {
        budget
            .validate(source_mode, publication_policy)
            .map_err(|cause| Box::new(SnapshotPreparationFailure::early(cause)))?;
        if publication_policy == SnapshotPublicationPolicy::ReplaceExisting {
            publication::namespace::require_exchange_available().map_err(|_| {
                Box::new(SnapshotPreparationFailure::early(
                    Error::DurabilityUnsupported(
                        "rollback-safe replacement requires atomic name exchange",
                    ),
                ))
            })?;
        }
        let source = open_source(source_path, source_mode, cancellation)?;
        if let Err(cause) =
            reject_live_self(&source, source_mode, destination_path, publication_policy)
        {
            return Err(fail_source(source, cause, None));
        }
        let created = match publication_policy {
            SnapshotPublicationPolicy::FailIfExists => {
                CreatedOutput::create_absent(destination_path)
            }
            SnapshotPublicationPolicy::ReplaceExisting
            | SnapshotPublicationPolicy::ReplaceExistingNoRollback => {
                CreatedOutput::create(destination_path)
            }
        };
        let created = match created {
            Ok(created) => created,
            Err(cause) => return Err(fail_source(source, Problem::output(&cause), None)),
        };
        let secured = match created.secure() {
            Ok(secured) => secured,
            Err(failure) => {
                return Err(fail_source(
                    source,
                    Problem::output(&failure.cause),
                    Some(cleanup::discard_created(&failure.owner)),
                ));
            }
        };
        let (attempt, file) = secured.into_parts();
        if source.identity().bytes == attempt.identity().encode() {
            return Err(fail_attempt(
                source,
                attempt,
                file,
                Error::InvalidArgument("source and snapshot output identities match"),
            ));
        }
        construct(
            source,
            attempt,
            file,
            publication_policy,
            budget,
            cancellation,
        )
    }

    fn reject_live_self(
        source: &Source,
        source_mode: SnapshotSourceMode,
        destination_path: &Path,
        policy: SnapshotPublicationPolicy,
    ) -> std::result::Result<(), PublicationProblem> {
        if source_mode != SnapshotSourceMode::Live
            || !matches!(
                policy,
                SnapshotPublicationPolicy::ReplaceExisting
                    | SnapshotPublicationPolicy::ReplaceExistingNoRollback
            )
        {
            return Ok(());
        }
        let destination = publication::namespace::Destination::bind(destination_path)
            .map_err(|error| Problem::namespace(&error))?;
        let current = destination
            .directory()
            .open_regular(destination.main(), false)
            .map_err(|error| Problem::namespace(&error))?;
        if current.is_some_and(|current| current.identity.encode() == source.identity().bytes) {
            return Err(problem(&Error::InvalidArgument(
                "a live snapshot cannot replace its own source path",
            )));
        }
        Ok(())
    }

    fn open_source(
        path: &Path,
        mode: SnapshotSourceMode,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Source, Box<SnapshotPreparationFailure>> {
        let mode = match mode {
            SnapshotSourceMode::Immutable => CurrentSourceMode::Immutable,
            SnapshotSourceMode::Live => CurrentSourceMode::Live,
        };
        Source::open_current(path, mode, cancellation).map_err(|failure| {
            Box::new(SnapshotPreparationFailure::new(
                problem(&failure.cause),
                None,
                None,
                failure.guard,
            ))
        })
    }

    fn construct(
        source: Source,
        attempt: OutputAttempt,
        file: std::fs::File,
        policy: SnapshotPublicationPolicy,
        budget: &SnapshotBudget,
        cancellation: &CancellationToken,
    ) -> SnapshotOutcome {
        let meta = source.meta();
        let builder = match Builder::new_owned(
            file,
            output_spec(meta),
            OutputBudget {
                max_output_pages: budget.max_output_pages,
            },
        ) {
            Ok(builder) => builder,
            Err(failure) => return Err(fail_attempt(source, attempt, failure.file, failure.cause)),
        };
        let finished = match build::copy(source.mapping(), meta, builder, budget, cancellation) {
            Ok(finished) => finished,
            Err(failure) => {
                return Err(fail_attempt(
                    source,
                    attempt,
                    failure.builder.into_file(),
                    failure.cause,
                ))
            }
        };
        finish(source, attempt, meta, finished, policy, cancellation)
    }

    fn output_spec(meta: crate::contract::MetaV4) -> OutputSpec {
        OutputSpec {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
            feed_index_limit: meta.feed_index_limit,
        }
    }

    fn finish(
        source: Source,
        attempt: OutputAttempt,
        source_meta: crate::contract::MetaV4,
        finished: crate::immutable_output::Finished,
        policy: SnapshotPublicationPolicy,
        cancellation: &CancellationToken,
    ) -> SnapshotOutcome {
        let end = source.finish(source_meta, cancellation);
        if let Some(cause) = end.cause {
            let discarded = cleanup::discard_attempt(&attempt, &finished.file);
            return Err(Box::new(SnapshotPreparationFailure::discarded(
                problem(&cause),
                discarded,
                end.guard,
            )));
        }
        debug_assert!(end.guard.is_none());
        let prepared = match attempt.prepare_cancellable(finished, cancellation) {
            Ok(prepared) => prepared,
            Err(failure) => {
                let discarded =
                    cleanup::discard_attempt(&failure.owner.attempt, &failure.owner.finished.file);
                return Err(Box::new(SnapshotPreparationFailure::discarded(
                    Problem::output(&failure.cause),
                    discarded,
                    None,
                )));
            }
        };
        let prepared = match policy {
            SnapshotPublicationPolicy::FailIfExists => prepared,
            SnapshotPublicationPolicy::ReplaceExisting
            | SnapshotPublicationPolicy::ReplaceExistingNoRollback => {
                let bound = match policy {
                    SnapshotPublicationPolicy::ReplaceExisting => {
                        publication::replacement::bind(prepared, cancellation)
                    }
                    SnapshotPublicationPolicy::ReplaceExistingNoRollback => {
                        publication::replacement::bind_no_rollback(prepared, cancellation)
                    }
                    SnapshotPublicationPolicy::FailIfExists => unreachable!(),
                };
                match bound {
                    Ok(prepared) => prepared,
                    Err(failure) => {
                        let discarded =
                            cleanup::discard_attempt(&failure.output.attempt, &failure.output.file);
                        return Err(Box::new(SnapshotPreparationFailure::discarded(
                            Problem::replacement(&failure.cause),
                            discarded,
                            None,
                        )));
                    }
                }
            }
        };
        let published = match policy {
            SnapshotPublicationPolicy::FailIfExists => {
                publication::attempt::fail_if_exists_cancellable(prepared, cancellation)
            }
            SnapshotPublicationPolicy::ReplaceExisting
            | SnapshotPublicationPolicy::ReplaceExistingNoRollback => {
                publication::attempt::replace_existing_cancellable(prepared, cancellation)
            }
        };
        match published {
            Ok(publication) => Ok(SnapshotResult { publication }),
            Err(failure) => Err(Box::new(SnapshotPreparationFailure::from_publication(
                *failure,
            ))),
        }
    }

    fn fail_attempt(
        source: Source,
        attempt: OutputAttempt,
        file: std::fs::File,
        cause: Error,
    ) -> Box<SnapshotPreparationFailure> {
        let discarded = cleanup::discard_attempt(&attempt, &file);
        fail_source(source, problem(&cause), Some(discarded))
    }

    fn fail_source(
        source: Source,
        cause: PublicationProblem,
        discarded: Option<cleanup::EarlyDiscard>,
    ) -> Box<SnapshotPreparationFailure> {
        let end = source.release_only();
        match discarded {
            Some(discarded) => Box::new(SnapshotPreparationFailure::discarded(
                cause, discarded, end.guard,
            )),
            None => Box::new(SnapshotPreparationFailure::new(
                cause, None, None, end.guard,
            )),
        }
    }
}

#[cfg(not(any(unix, windows)))]
mod platform {
    use crate::error::Error;

    use super::*;

    pub(super) fn snapshot(
        _source_path: &Path,
        _source_mode: SnapshotSourceMode,
        _destination_path: &Path,
        _publication_policy: SnapshotPublicationPolicy,
        _budget: &SnapshotBudget,
        _cancellation: &CancellationToken,
    ) -> SnapshotOutcome {
        Err(Box::new(SnapshotPreparationFailure::early(
            Error::Unsupported("snapshot publication is not implemented on this platform"),
        )))
    }
}
