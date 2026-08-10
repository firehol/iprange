//! Shared construction and publication of one private immutable output.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::immutable_output::Finished;

use super::cleanup::{self, EarlyDiscard};
use super::output::{CreatedOutput, OutputAttempt};
use super::problem::Problem;
use super::{
    PublicationPolicy, PublicationPreparationFailure, PublicationProblem, PublicationResult,
};

pub(crate) struct EarlyFailure {
    pub(crate) cause: PublicationProblem,
    pub(crate) discarded: Option<EarlyDiscard>,
}

#[allow(clippy::large_enum_variant)] // Publication setup is once per output; keep errors allocation-free.
pub(crate) enum Failure {
    Early(EarlyFailure),
    Publication(Box<PublicationPreparationFailure>),
}

#[allow(clippy::result_large_err)] // See Failure: this is outside the range hot path.
pub(crate) fn create(
    path: &Path,
    policy: PublicationPolicy,
) -> Result<(OutputAttempt, File), EarlyFailure> {
    if policy == PublicationPolicy::ReplaceExisting {
        super::namespace::require_exchange_available().map_err(|_| EarlyFailure {
            cause: Problem::sdk(&crate::error::Error::DurabilityUnsupported(
                "rollback-safe replacement requires atomic name exchange",
            )),
            discarded: None,
        })?;
    }
    let created = match policy {
        PublicationPolicy::FailIfExists => CreatedOutput::create_absent(path),
        PublicationPolicy::ReplaceExisting | PublicationPolicy::ReplaceExistingNoRollback => {
            CreatedOutput::create(path)
        }
    }
    .map_err(|cause| EarlyFailure {
        cause: Problem::output(&cause),
        discarded: None,
    })?;
    let secured = created.secure().map_err(|failure| EarlyFailure {
        cause: Problem::output(&failure.cause),
        discarded: Some(cleanup::discard_created(&failure.owner)),
    })?;
    Ok(secured.into_parts())
}

#[allow(clippy::result_large_err)] // See Failure: this is outside the range hot path.
pub(crate) fn publish(
    attempt: OutputAttempt,
    finished: Finished,
    policy: PublicationPolicy,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Failure> {
    let prepared = attempt
        .prepare_cancellable(finished, cancellation)
        .map_err(|failure| {
            Failure::Early(EarlyFailure {
                cause: Problem::output(&failure.cause),
                discarded: Some(cleanup::discard_attempt(
                    &failure.owner.attempt,
                    &failure.owner.finished.file,
                )),
            })
        })?;
    let prepared = match policy {
        PublicationPolicy::FailIfExists => prepared,
        PublicationPolicy::ReplaceExisting => super::replacement::bind(prepared, cancellation)
            .map_err(|failure| {
                Failure::Early(EarlyFailure {
                    cause: Problem::replacement(&failure.cause),
                    discarded: Some(cleanup::discard_attempt(
                        &failure.output.attempt,
                        &failure.output.file,
                    )),
                })
            })?,
        PublicationPolicy::ReplaceExistingNoRollback => {
            super::replacement::bind_no_rollback(prepared, cancellation).map_err(|failure| {
                Failure::Early(EarlyFailure {
                    cause: Problem::replacement(&failure.cause),
                    discarded: Some(cleanup::discard_attempt(
                        &failure.output.attempt,
                        &failure.output.file,
                    )),
                })
            })?
        }
    };
    let result = match policy {
        PublicationPolicy::FailIfExists => {
            super::attempt::fail_if_exists_cancellable(prepared, cancellation)
        }
        PublicationPolicy::ReplaceExisting | PublicationPolicy::ReplaceExistingNoRollback => {
            super::attempt::replace_existing_cancellable(prepared, cancellation)
        }
    };
    result.map_err(Failure::Publication)
}
