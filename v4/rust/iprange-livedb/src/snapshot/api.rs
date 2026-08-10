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
    if source_mode == SnapshotSourceMode::Live {
        if let Err(cause) = crate::live_lock::require_live_supported() {
            return Err(Box::new(SnapshotPreparationFailure::early(cause)));
        }
    }
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
    use crate::publication::cleanup;
    use crate::publication::output::OutputAttempt;
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
        let source = open_source(source_path, source_mode, cancellation)?;
        if let Err(cause) =
            reject_live_self(&source, source_mode, destination_path, publication_policy)
        {
            return Err(fail_source(source, cause, None));
        }
        let (attempt, file) =
            match publication::workflow::create(destination_path, publication_policy) {
                Ok(output) => output,
                Err(failure) => {
                    return Err(fail_source(source, failure.cause, failure.discarded));
                }
            };
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
        let finished = match build::copy(&source, file, budget, cancellation) {
            Ok(finished) => finished,
            Err(failure) => return Err(fail_attempt(source, attempt, failure.file, failure.cause)),
        };
        finish(source, attempt, finished, policy, cancellation)
    }

    fn finish(
        source: Source,
        attempt: OutputAttempt,
        finished: crate::immutable_output::Finished,
        policy: SnapshotPublicationPolicy,
        cancellation: &CancellationToken,
    ) -> SnapshotOutcome {
        let end = source.finish_current(cancellation);
        if let Some(cause) = end.cause {
            let discarded = cleanup::discard_attempt(&attempt, &finished.file);
            return Err(Box::new(SnapshotPreparationFailure::discarded(
                problem(&cause),
                discarded,
                end.guard,
            )));
        }
        debug_assert!(end.guard.is_none());
        match publication::workflow::publish(attempt, finished, policy, cancellation) {
            Ok(publication) => Ok(SnapshotResult { publication }),
            Err(publication::workflow::Failure::Early(failure)) => match failure.discarded {
                Some(discarded) => Err(Box::new(SnapshotPreparationFailure::discarded(
                    failure.cause,
                    discarded,
                    None,
                ))),
                None => Err(Box::new(SnapshotPreparationFailure::new(
                    failure.cause,
                    None,
                    None,
                    None,
                ))),
            },
            Err(publication::workflow::Failure::Publication(failure)) => Err(Box::new(
                SnapshotPreparationFailure::from_publication(*failure),
            )),
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
