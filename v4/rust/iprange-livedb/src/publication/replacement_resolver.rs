//! Restart resolution for one exact replacement publication.

use crate::cancellation::CancellationToken;
use crate::error::ErrorCode;

use super::result_builder::{coordination_access, first_problem, record_cancellation, LaterResult};
use super::{
    arm, conflict, require_no_later, reservation_identity, ArmFailure, BaseResolution, Mode,
};
use crate::publication::attempt;
use crate::publication::cleanup::{self, OutputOwner};
use crate::publication::namespace::Destination;
use crate::publication::output::{PreparedOutput, ResumedOutput};
use crate::publication::problem::Problem;
use crate::publication::replacement::PreviousMain;
use crate::publication::replacement_inspection::{self, Content, Inspected, Location, Pair};
use crate::publication::reservation::{Header, Policy, State};
use crate::publication::reservation_inspection;
use crate::publication::result::{
    AccessPolicy, ArtifactKind, DestinationContent, FinalState, NameSlot, PublicationResult,
    PublicationStatus,
};

pub(super) fn dispatch(
    base: BaseResolution,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    unlock(&base.exact)?;
    unlock(&base.later)?;
    let pair = replacement_inspection::inspect(&base.destination, base.header, cancellation)?;
    relock(&base.exact, &base.destination, cancellation)?;
    relock(&base.later, &base.destination, cancellation)?;
    match pair.main.as_ref().map(|main| main.content) {
        Some(Content::Previous) => {
            require_no_later(&base.later)?;
            resolve_previous(base, pair, mode, cancellation)
        }
        Some(Content::Desired) => resolve_desired(base, pair, mode, cancellation),
        Some(Content::Other) => {
            require_no_later(&base.later)?;
            resolve_other(base, pair, cancellation)
        }
        None => Err(conflict(
            "replacement cannot legitimately leave the destination absent",
        )),
    }
}

fn resolve_previous(
    base: BaseResolution,
    pair: Pair,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    match mode {
        Mode::Complete => complete_previous(base, pair, cancellation),
        Mode::Remove => remove_previous(base, pair, cancellation),
    }
}

fn complete_previous(
    mut base: BaseResolution,
    mut pair: Pair,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let reservation = base
        .exact
        .take()
        .ok_or_else(|| unresolvable("replacement completion requires its exact reservation"))?;
    let main = pair
        .main
        .take()
        .ok_or_else(|| conflict("replacement previous destination disappeared"))?;
    let private = pair
        .private
        .take()
        .ok_or_else(|| unresolvable("replacement completion requires its prepared output"))?;
    require_previous(&main, base.header)?;
    require_output(&private, base.header)?;
    let previous = PreviousMain {
        file: main.file,
        mapping: main
            .mapping
            .expect("finished replacement inspection retains its mapping"),
        identity: main.identity,
        byte_length: main.byte_length,
        sha512: main.sha512,
    };
    let output = PreparedOutput::resume_replacement(
        base.destination,
        base.header.attempt_id,
        ResumedOutput {
            file: private.file,
            mapping: private
                .mapping
                .expect("desired replacement output retains its mapping"),
            identity: private.identity,
            meta: private
                .meta
                .expect("desired replacement output has selected metadata"),
            byte_length: private.byte_length,
            sha512: private.sha512,
        },
        previous,
        base.header.policy,
    )
    .map_err(|error| Problem::output(&error))?;
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    let reservation_identity = reservation.identity;
    let result = match arm(reservation, &output) {
        Ok(armed) => attempt::resume_armed(base.seed, output, armed),
        Err(ArmFailure::Problem(problem)) => return Err(problem),
        Err(ArmFailure::Unknown(problem)) => {
            attempt::outcome_unknown(base.seed, reservation_identity, problem)
        }
    };
    Ok(record_cancellation(result, cancellation))
}

fn remove_previous(
    base: BaseResolution,
    pair: Pair,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let main = pair.main.as_ref().expect("dispatch selected previous main");
    require_previous(main, base.header)?;
    resolve_not_desired(base, pair, DestinationContent::Previous, cancellation)
}

fn resolve_desired(
    mut base: BaseResolution,
    pair: Pair,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    if base.header.policy == Policy::ReplaceExistingNoRollback && mode == Mode::Remove {
        return Err(unresolvable(
            "no-rollback replacement cannot restore a discarded destination",
        ));
    }
    let main = pair.main.as_ref().expect("dispatch selected desired main");
    synchronize(main, &base.destination, cancellation)?;
    let (output, foreign_private) = desired_cleanup(pair.private.as_ref(), base.header);
    let reservation = base.exact.as_ref().map(super::reservation_owner);
    let mut summary =
        cleanup::discard_recovered(&mut base.seed, &base.destination, output, reservation);
    if let Some(private) = foreign_private {
        summary.artifacts.push(base.seed.artifact(
            ArtifactKind::PrivateOutput,
            NameSlot::PrivateOutput,
            Some(private.identity),
            conflict("private replacement artifact does not match recorded ownership"),
        ));
    }
    let verified = main
        .verify(&base.destination, &CancellationToken::new())
        .and_then(|()| super::verify_later(base.later.as_ref(), &base.destination));
    let cause = verified
        .as_ref()
        .err()
        .cloned()
        .or_else(|| first_problem(&summary.artifacts));
    let publication = if verified.is_ok() {
        PublicationStatus::Published
    } else {
        PublicationStatus::OutcomeUnknown
    };
    let result = base
        .seed
        .result_with_housekeeping(
            FinalState {
                reservation_identity: reservation_identity(base.header),
                main_namespace_may_have_been_attempted: attempted(base.header.state),
                publication,
                destination_content: if publication == PublicationStatus::Published {
                    DestinationContent::Desired
                } else {
                    DestinationContent::Unclassified
                },
                main_access_policy: if verified.is_ok() {
                    main.access
                } else {
                    AccessPolicy::Unclassified
                },
                coordination_access_policy: if verified.is_ok() {
                    coordination_access(&summary, base.exact.as_ref(), base.later.as_ref())
                } else {
                    AccessPolicy::Unclassified
                },
            },
            summary.artifacts,
            summary.housekeeping,
            summary.visible_housekeeping,
            cause,
        )
        .with_later(base.later.as_ref());
    Ok(record_cancellation(result, cancellation))
}

fn resolve_other(
    base: BaseResolution,
    pair: Pair,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    resolve_not_desired(base, pair, DestinationContent::Other, cancellation)
}

fn resolve_not_desired(
    mut base: BaseResolution,
    pair: Pair,
    content: DestinationContent,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let main = pair
        .main
        .as_ref()
        .expect("dispatch selected a non-desired main");
    synchronize(main, &base.destination, cancellation)?;
    let output = removable_output(pair.private.as_ref(), base.header)?;
    let reservation = base.exact.as_ref().map(super::reservation_owner);
    let summary =
        cleanup::discard_recovered(&mut base.seed, &base.destination, output, reservation);
    let verified = main.verify(&base.destination, &CancellationToken::new());
    let cause = verified
        .as_ref()
        .err()
        .cloned()
        .or_else(|| first_problem(&summary.artifacts));
    let publication = if verified.is_ok() {
        PublicationStatus::NotPublished
    } else {
        PublicationStatus::OutcomeUnknown
    };
    let result = base.seed.result_with_housekeeping(
        FinalState {
            reservation_identity: reservation_identity(base.header),
            main_namespace_may_have_been_attempted: attempted(base.header.state),
            publication,
            destination_content: if verified.is_ok() {
                content
            } else {
                DestinationContent::Unclassified
            },
            main_access_policy: if verified.is_ok() {
                main.access
            } else {
                AccessPolicy::Unclassified
            },
            coordination_access_policy: if verified.is_ok() {
                coordination_access(&summary, base.exact.as_ref(), None)
            } else {
                AccessPolicy::Unclassified
            },
        },
        summary.artifacts,
        summary.housekeeping,
        summary.visible_housekeeping,
        cause,
    );
    Ok(record_cancellation(result, cancellation))
}

fn removable_output(
    private: Option<&Inspected>,
    header: Header,
) -> Result<Option<OutputOwner<'_>>, Problem> {
    match private {
        None => Ok(None),
        Some(private)
            if private.content == Content::Desired
                && private.identity.encode() == header.output_identity =>
        {
            Ok(Some(owner(private)))
        }
        Some(_) => Err(conflict(
            "private replacement artifact does not match the prepared output",
        )),
    }
}

fn desired_cleanup(
    private: Option<&Inspected>,
    header: Header,
) -> (Option<OutputOwner<'_>>, Option<&Inspected>) {
    match private {
        None => (None, None),
        Some(private)
            if (private.content == Content::Desired
                && private.identity.encode() == header.output_identity)
                || (private.content == Content::Previous
                    && header.previous.is_some_and(|previous| {
                        private.identity.encode() == previous.identity
                    })) =>
        {
            (Some(owner(private)), None)
        }
        Some(private) => (None, Some(private)),
    }
}

fn owner(entry: &Inspected) -> OutputOwner<'_> {
    OutputOwner {
        file: &entry.file,
        identity: entry.identity,
        name: &entry.name,
    }
}

fn require_output(output: &Inspected, header: Header) -> Result<(), Problem> {
    if output.location != Location::Private
        || output.content != Content::Desired
        || output.identity.encode() != header.output_identity
        || output.access != AccessPolicy::CreatorOnly
    {
        return Err(unresolvable(
            "replacement prepared output does not match its reservation",
        ));
    }
    Ok(())
}

fn require_previous(previous: &Inspected, header: Header) -> Result<(), Problem> {
    let expected = header
        .previous
        .expect("replacement header contains previous evidence");
    if previous.content != Content::Previous || previous.identity.encode() != expected.identity {
        return Err(conflict(
            "replacement destination no longer matches previous evidence",
        ));
    }
    Ok(())
}

fn synchronize(
    main: &Inspected,
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    main.file
        .sync_all()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?;
    main.verify(destination, cancellation)
}

fn unlock(reservation: &Option<reservation_inspection::Inspected>) -> Result<(), Problem> {
    if let Some(reservation) = reservation {
        reservation.unlock_operation()?;
    }
    Ok(())
}

fn relock(
    reservation: &Option<reservation_inspection::Inspected>,
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    if let Some(reservation) = reservation {
        reservation.relock_operation(destination, cancellation)?;
    }
    Ok(())
}

const fn attempted(state: State) -> bool {
    matches!(state, State::MainMayHaveBeenAttempted)
}

const fn unresolvable(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Unresolvable, None, detail)
}
