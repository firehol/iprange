//! Restart completion or removal of one exact fail-if-exists publication.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::ErrorCode;

use super::attempt;
use super::cleanup::{self, OutputOwner, ReservationLocation, ReservationOwner};
use super::file_inspection::{self, Content};
use super::namespace::{Destination, Identity};
use super::output::PreparedOutput;
use super::problem::Problem;
use super::reservation::{Header, State};
use super::reservation_file::{ArmedReservation, CanonicalReservation, PrivateReservation};
use super::reservation_inspection::{self, Inspected, Location};
use super::result::{
    AccessPolicy, ArtifactKind, DestinationContent, FinalState, NameSlot, PublicationResult,
    PublicationStatus, Seed,
};

#[path = "resolver_result.rs"]
mod result_builder;
use result_builder::{
    coordination_access, desired_problem, desired_result, first_problem, published_output_result,
    record_cancellation, DesiredContext,
};
#[path = "resolver_verification.rs"]
mod verification;
use verification::{
    check_cancellation, final_later, synchronize, verify_destination, verify_no_later,
};
#[path = "resolver_authority.rs"]
mod authority;
#[path = "replacement_resolver.rs"]
mod replacement;
use authority::BaseResolution;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Mode {
    Complete,
    Remove,
}

pub(super) fn resolve(
    path: &Path,
    supplied: Option<&PublicationResult>,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let base = authority::inspect(path, supplied, cancellation)?;
    if base.header.policy == super::reservation::Policy::ReplaceExisting {
        return replacement::dispatch(base, mode, cancellation);
    }
    let main = file_inspection::main(&base.destination, base.header, cancellation)?;
    dispatch(
        Resolution {
            destination: base.destination,
            header: base.header,
            seed: base.seed,
            exact: base.exact,
            later: base.later,
            main,
        },
        mode,
        cancellation,
    )
}

fn dispatch(
    resolution: Resolution,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let Resolution {
        destination,
        header,
        seed,
        exact,
        later,
        main,
    } = resolution;
    match main {
        Some(main) if main.content == Content::Desired => {
            resolve_desired(destination, header, seed, exact, later, main, cancellation)
        }
        Some(main) => resolve_other(destination, header, seed, exact, later, main, cancellation),
        None => resolve_absent(destination, header, seed, exact, later, mode, cancellation),
    }
}

fn resolve_other(
    destination: Destination,
    header: Header,
    seed: Seed,
    exact: Option<Inspected>,
    later: Option<Inspected>,
    main: file_inspection::Inspected,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    require_no_later(&later)?;
    if let Err(problem) = synchronize(&destination, &main, cancellation) {
        return if problem.code == ErrorCode::Cancelled {
            Err(problem)
        } else {
            Ok(attempt::outcome_unknown(
                seed,
                reservation_identity(header),
                problem,
            ))
        };
    }
    abandon(
        &destination,
        header,
        seed,
        exact,
        Some(main),
        DestinationContent::Other,
        cancellation,
    )
}

fn resolve_absent(
    destination: Destination,
    header: Header,
    seed: Seed,
    exact: Option<Inspected>,
    later: Option<Inspected>,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    require_no_later(&later)?;
    match mode {
        Mode::Complete => complete_absent(destination, header, seed, exact, cancellation),
        Mode::Remove => abandon(
            &destination,
            header,
            seed,
            exact,
            None,
            DestinationContent::Absent,
            cancellation,
        ),
    }
}

fn complete_absent(
    destination: Destination,
    header: Header,
    seed: Seed,
    reservation: Option<Inspected>,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let reservation = reservation
        .ok_or_else(|| unresolvable("completion requires the exact recorded reservation inode"))?;
    let inspected = file_inspection::private(&destination, header, cancellation)?
        .ok_or_else(|| unresolvable("completion requires the exact prepared output inode"))?;
    let output = PreparedOutput::resume(destination, header.attempt_id, inspected)
        .map_err(|error| Problem::output(&error))?;
    check_cancellation(cancellation)?;
    let reservation_identity = reservation.identity;
    let result = match arm(reservation, &output) {
        Ok(armed) => attempt::resume_armed(seed, output, armed),
        Err(ArmFailure::Problem(problem)) => return Err(problem),
        Err(ArmFailure::Unknown(problem)) => {
            attempt::outcome_unknown(seed, reservation_identity, problem)
        }
    };
    Ok(record_cancellation(result, cancellation))
}

fn arm(inspected: Inspected, output: &PreparedOutput) -> Result<ArmedReservation, ArmFailure> {
    let canonical = match inspected.location {
        Location::Private => {
            let private = PrivateReservation {
                name: inspected.name,
                file: inspected.file,
                identity: inspected.identity,
                header: inspected.header,
            };
            match private.acquire(output) {
                Ok(canonical) => canonical,
                Err(failure) => {
                    let problem = Problem::reservation(&failure.cause);
                    if matches!(
                        failure.cause,
                        super::reservation_file::Error::Namespace(
                            super::namespace::NamespaceError::Exists
                        )
                    ) {
                        return Err(ArmFailure::Problem(conflict(
                            "another coordination inode won publication",
                        )));
                    }
                    return Err(if failure.owner.namespace_call_started {
                        ArmFailure::Unknown(problem)
                    } else {
                        ArmFailure::Problem(problem)
                    });
                }
            }
        }
        Location::Canonical => CanonicalReservation {
            name: inspected.name,
            file: inspected.file,
            identity: inspected.identity,
            header: inspected.header,
        },
    };
    match canonical.header.state {
        State::Prepared => canonical.arm(output).map_err(|failure| {
            let problem = Problem::reservation(&failure.cause);
            if failure.owner.state2_selected {
                ArmFailure::Unknown(problem)
            } else {
                ArmFailure::Problem(problem)
            }
        }),
        State::MainMayHaveBeenAttempted => canonical
            .resume_armed(output)
            .map_err(|failure| ArmFailure::Problem(Problem::reservation(&failure.cause))),
    }
}

fn resolve_desired(
    destination: Destination,
    header: Header,
    mut seed: Seed,
    reservation: Option<Inspected>,
    later: Option<Inspected>,
    main: file_inspection::Inspected,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    if let Err(problem) = synchronize(&destination, &main, cancellation) {
        if problem.code == ErrorCode::Cancelled {
            return Err(problem);
        }
        return Ok(attempt::outcome_unknown(
            seed,
            reservation_identity(header),
            problem,
        ));
    }
    let private = match file_inspection::private_owned(&destination, header, cancellation) {
        Ok(private) => private,
        Err(problem) => {
            if problem.code == ErrorCode::Cancelled {
                return Err(problem);
            }
            check_cancellation(cancellation)?;
            let mut summary = cleanup::discard_recovered(
                &mut seed,
                &destination,
                None,
                reservation.as_ref().map(reservation_owner),
            );
            summary.artifacts.push(seed.artifact(
                ArtifactKind::PrivateOutput,
                NameSlot::PrivateOutput,
                Identity::decode(header.output_identity),
                problem,
            ));
            let later =
                match final_later(&destination, header, reservation.as_ref(), later, &summary) {
                    Ok(later) => later,
                    Err(final_problem) => {
                        let result = desired_problem(seed, header, summary, final_problem);
                        return Ok(record_cancellation(result, cancellation));
                    }
                };
            let context = DesiredContext {
                destination: &destination,
                header,
                reservation: reservation.as_ref(),
                later: later.as_ref(),
                main: &main,
            };
            let result = published_output_result(seed, summary, problem, context);
            return Ok(record_cancellation(result, cancellation));
        }
    };
    check_cancellation(cancellation)?;
    let summary = cleanup::discard_recovered(
        &mut seed,
        &destination,
        private.as_ref().map(output_owner),
        reservation.as_ref().map(reservation_owner),
    );
    let later = match final_later(&destination, header, reservation.as_ref(), later, &summary) {
        Ok(later) => later,
        Err(problem) => {
            let result = desired_problem(seed, header, summary, problem);
            return Ok(record_cancellation(result, cancellation));
        }
    };
    let context = DesiredContext {
        destination: &destination,
        header,
        reservation: reservation.as_ref(),
        later: later.as_ref(),
        main: &main,
    };
    let result = desired_result(seed, summary, context);
    Ok(record_cancellation(result, cancellation))
}

fn abandon(
    destination: &Destination,
    header: Header,
    mut seed: Seed,
    reservation: Option<Inspected>,
    main: Option<file_inspection::Inspected>,
    content: DestinationContent,
    cancellation: &CancellationToken,
) -> Result<PublicationResult, Problem> {
    let private = file_inspection::private_owned(destination, header, cancellation)?;
    check_cancellation(cancellation)?;
    let summary = cleanup::discard_recovered(
        &mut seed,
        destination,
        private.as_ref().map(output_owner),
        reservation.as_ref().map(reservation_owner),
    );
    let verification = verify_destination(destination, content, main.as_ref(), &summary);
    let verification =
        verification.and_then(|()| verify_no_later(destination, reservation.as_ref(), &summary));
    let cause = verification
        .as_ref()
        .err()
        .copied()
        .or_else(|| first_problem(&summary.artifacts));
    let publication = if verification.is_ok() {
        PublicationStatus::NotPublished
    } else {
        PublicationStatus::OutcomeUnknown
    };
    let result = seed.result(
        FinalState {
            reservation_identity: reservation_identity(header),
            main_namespace_may_have_been_attempted: header.state == State::MainMayHaveBeenAttempted,
            publication,
            destination_content: if publication == PublicationStatus::NotPublished {
                content
            } else {
                DestinationContent::Unclassified
            },
            main_access_policy: if verification.is_ok() {
                main.as_ref()
                    .map_or(AccessPolicy::Absent, |main| main.access)
            } else {
                AccessPolicy::Unclassified
            },
            coordination_access_policy: if verification.is_ok() {
                coordination_access(&summary, reservation.as_ref(), None)
            } else {
                AccessPolicy::Unclassified
            },
        },
        summary.artifacts,
        cause,
    );
    Ok(record_cancellation(result, cancellation))
}

fn output_owner(output: &file_inspection::Inspected) -> OutputOwner<'_> {
    OutputOwner {
        file: &output.file,
        identity: output.identity,
        name: &output.name,
    }
}

fn reservation_owner(reservation: &Inspected) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &reservation.file,
        identity: Some(reservation.identity),
        private_name: &reservation.name,
        location: match reservation.location {
            Location::Private => ReservationLocation::Private,
            Location::Canonical => ReservationLocation::Canonical,
        },
    }
}

fn verify_later(later: Option<&Inspected>, destination: &Destination) -> Result<(), Problem> {
    match later {
        Some(reservation) => reservation.verify(destination),
        None => Ok(()),
    }
}

fn reservation_identity(header: Header) -> Identity {
    Identity::decode(header.reservation_identity).expect("selected reservation identity is valid")
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}

const fn unresolvable(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Unresolvable, None, detail)
}

pub(super) enum ArmFailure {
    Problem(Problem),
    Unknown(Problem),
}

struct Resolution {
    destination: Destination,
    header: Header,
    seed: Seed,
    exact: Option<Inspected>,
    later: Option<Inspected>,
    main: Option<file_inspection::Inspected>,
}

fn require_no_later(later: &Option<Inspected>) -> Result<(), Problem> {
    if later.is_some() {
        Err(conflict(
            "another publication reservation currently owns the destination",
        ))
    } else {
        Ok(())
    }
}

#[cfg(test)]
#[path = "resolver_tests.rs"]
mod tests;
