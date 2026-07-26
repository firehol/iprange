//! One fail-if-exists publication composed from explicit ownership states.

use super::cleanup::{self, ReservationLocation, ReservationOwner};
use super::main_file::{self, PublishedMain};
use super::output::PreparedOutput;
use super::problem::Problem;
use super::reservation_file::{
    AcquiringReservation, ArmedReservation, ArmingReservation, CanonicalReservation,
    PrivateReservation, ReservationDraft,
};
use super::result::{
    AccessPolicy, ArtifactKind, CleanupArtifacts, DestinationContent, FinalState, NameSlot,
    PreparationFailure, PublicationResult, PublicationStatus, Seed,
};
use crate::cancellation::CancellationToken;

pub(crate) type Result = std::result::Result<PublicationResult, Box<PreparationFailure>>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Point {
    ReservationCreated,
    State1Selected,
    ReservationAcquired,
    State2Selected,
    DesiredProven,
    CleanupOutput,
    CleanupReservation,
    CleanupDirectorySync,
}

pub(crate) fn fail_if_exists(output: PreparedOutput) -> Result {
    debug_assert!(output.previous.is_none());
    debug_assert_eq!(output.policy, super::reservation::Policy::FailIfExists);
    fail_if_exists_with(output, |_| Ok(()))
}

pub(crate) fn fail_if_exists_cancellable(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result {
    debug_assert!(output.previous.is_none());
    debug_assert_eq!(output.policy, super::reservation::Policy::FailIfExists);
    publish_with(output, Some(cancellation), |point| {
        if matches!(
            point,
            Point::CleanupOutput | Point::CleanupReservation | Point::CleanupDirectorySync
        ) {
            return Ok(());
        }
        cancellation.check().map_err(|error| Problem::sdk(&error))
    })
}

pub(crate) fn replace_existing_cancellable(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result {
    debug_assert!(output.previous.is_some());
    debug_assert!(output.policy.is_replacement());
    publish_with(output, Some(cancellation), |point| {
        if matches!(
            point,
            Point::CleanupOutput | Point::CleanupReservation | Point::CleanupDirectorySync
        ) {
            return Ok(());
        }
        cancellation.check().map_err(|error| Problem::sdk(&error))
    })
}

pub(super) fn resume_armed(
    seed: Seed,
    output: PreparedOutput,
    reservation: ArmedReservation,
) -> PublicationResult {
    match main_file::publish(output, reservation) {
        Ok(published) => finish_published(seed, published, None),
        Err(failure) if failure.owner.desired_proven => finish_published(
            seed,
            PublishedMain {
                output: failure.owner.output,
                reservation: failure.owner.reservation,
            },
            Some(Problem::main(&failure.cause)),
        ),
        Err(failure) => outcome_unknown(
            seed,
            failure.owner.reservation.identity,
            Problem::main(&failure.cause),
        ),
    }
}

fn fail_if_exists_with(
    output: PreparedOutput,
    checkpoint: impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    publish_with(output, None, checkpoint)
}

fn publish_with(
    output: PreparedOutput,
    cancellation: Option<&CancellationToken>,
    mut checkpoint: impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    let seed = Seed::capture(&output);
    let draft = match ReservationDraft::create(&output) {
        Ok(draft) => draft,
        Err(cause) => {
            return preparation(
                seed,
                output,
                None,
                Problem::reservation(&cause),
                &mut checkpoint,
            )
        }
    };
    if let Err(cause) = checkpoint(Point::ReservationCreated) {
        return preparation(
            seed,
            output,
            Some(draft_owner(&draft)),
            cause,
            &mut checkpoint,
        );
    }

    let reservation = match draft.initialize(&output) {
        Ok(reservation) => reservation,
        Err(failure) => {
            let cause = Problem::reservation(&failure.cause);
            if failure.owner.state1_selected {
                return Ok(not_published(
                    seed,
                    output,
                    draft_owner(&failure.owner),
                    failure
                        .owner
                        .identity
                        .expect("selected state 1 has reservation identity"),
                    cause,
                    &mut checkpoint,
                ));
            }
            return preparation(
                seed,
                output,
                Some(draft_owner(&failure.owner)),
                cause,
                &mut checkpoint,
            );
        }
    };
    from_private(seed, output, reservation, cancellation, &mut checkpoint)
}

fn from_private(
    seed: Seed,
    output: PreparedOutput,
    reservation: PrivateReservation,
    cancellation: Option<&CancellationToken>,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    if let Err(cause) = checkpoint(Point::State1Selected) {
        return Ok(not_published(
            seed,
            output,
            private_owner(&reservation),
            reservation.identity,
            cause,
            checkpoint,
        ));
    }

    let reservation = match reservation.acquire(&output) {
        Ok(reservation) => reservation,
        Err(failure) => {
            return Ok(not_published(
                seed,
                output,
                acquiring_owner(&failure.owner),
                failure.owner.reservation.identity,
                Problem::reservation(&failure.cause),
                checkpoint,
            ))
        }
    };
    from_canonical(seed, output, reservation, cancellation, checkpoint)
}

fn from_canonical(
    seed: Seed,
    output: PreparedOutput,
    reservation: CanonicalReservation,
    cancellation: Option<&CancellationToken>,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    if let Err(cause) = checkpoint(Point::ReservationAcquired) {
        return Ok(not_published(
            seed,
            output,
            canonical_owner(&reservation),
            reservation.identity,
            cause,
            checkpoint,
        ));
    }
    if let Some(previous) = &output.previous {
        if let Err(cause) = previous.verify_content(output.attempt.destination(), cancellation) {
            return Ok(not_published(
                seed,
                output,
                canonical_owner(&reservation),
                reservation.identity,
                Problem::replacement(&cause),
                checkpoint,
            ));
        }
    }

    let reservation = match reservation.arm(&output) {
        Ok(reservation) => reservation,
        Err(failure) => {
            let cause = Problem::reservation(&failure.cause);
            if failure.owner.state2_selected {
                return Ok(outcome_unknown(
                    seed,
                    failure.owner.reservation.identity,
                    cause,
                ));
            }
            return Ok(not_published(
                seed,
                output,
                arming_owner(&failure.owner),
                failure.owner.reservation.identity,
                cause,
                checkpoint,
            ));
        }
    };
    from_armed(seed, output, reservation, checkpoint)
}

fn from_armed(
    seed: Seed,
    output: PreparedOutput,
    reservation: ArmedReservation,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    if let Err(cause) = checkpoint(Point::State2Selected) {
        return Ok(outcome_unknown(seed, reservation.identity, cause));
    }

    match main_file::publish(output, reservation) {
        Ok(published) => {
            let cause = checkpoint(Point::DesiredProven).err();
            Ok(finish_published(seed, published, cause))
        }
        Err(failure) => {
            let cause = Problem::main(&failure.cause);
            if failure.owner.desired_proven {
                Ok(finish_published(
                    seed,
                    PublishedMain {
                        output: failure.owner.output,
                        reservation: failure.owner.reservation,
                    },
                    Some(cause),
                ))
            } else {
                Ok(outcome_unknown(
                    seed,
                    failure.owner.reservation.identity,
                    cause,
                ))
            }
        }
    }
}

fn preparation(
    mut seed: Seed,
    output: PreparedOutput,
    reservation: Option<ReservationOwner<'_>>,
    cause: Problem,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    let cleanup = cleanup::discard_with(&mut seed, &output, reservation, |point| {
        checkpoint(cleanup_point(point))
    });
    Err(Box::new(seed.preparation(cleanup.artifacts, cause)))
}

fn not_published(
    mut seed: Seed,
    output: PreparedOutput,
    reservation: ReservationOwner<'_>,
    reservation_identity: super::namespace::Identity,
    cause: Problem,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> PublicationResult {
    let previous_unchanged = output.previous.as_ref().is_some_and(|previous| {
        previous
            .verify_content(output.attempt.destination(), None)
            .is_ok()
    });
    let cleanup = cleanup::discard_with(&mut seed, &output, Some(reservation), |point| {
        checkpoint(cleanup_point(point))
    });
    let content = if previous_unchanged {
        DestinationContent::Previous
    } else if cleanup.main_absent {
        DestinationContent::Absent
    } else {
        DestinationContent::Unclassified
    };
    let coordination = if cleanup.coordination_absent {
        AccessPolicy::Absent
    } else {
        AccessPolicy::Unclassified
    };
    seed.result(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: false,
            publication: PublicationStatus::NotPublished,
            destination_content: content,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy: coordination,
        },
        cleanup.artifacts,
        Some(cause),
    )
}

pub(super) fn outcome_unknown(
    seed: Seed,
    reservation_identity: super::namespace::Identity,
    cause: Problem,
) -> PublicationResult {
    seed.result(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: true,
            publication: PublicationStatus::OutcomeUnknown,
            destination_content: DestinationContent::Unclassified,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy: AccessPolicy::ChangedOrUnproven,
        },
        CleanupArtifacts::new(),
        Some(cause),
    )
}

pub(super) fn finish_published(
    mut seed: Seed,
    published: PublishedMain,
    cause: Option<Problem>,
) -> PublicationResult {
    let reservation_identity = published.reservation.identity;
    match published.retire() {
        Ok(completed) => seed.result(
            FinalState {
                reservation_identity: completed.reservation_identity,
                main_namespace_may_have_been_attempted: true,
                publication: PublicationStatus::Published,
                destination_content: DestinationContent::Desired,
                main_access_policy: AccessPolicy::CreatorOnly,
                coordination_access_policy: AccessPolicy::Absent,
            },
            CleanupArtifacts::new(),
            cause,
        ),
        Err(failure) => {
            let retirement = Problem::main(&failure.cause);
            let main_access = if failure.owner.published.output.verify_main().is_ok() {
                AccessPolicy::CreatorOnly
            } else {
                AccessPolicy::ChangedOrUnproven
            };
            let coordination = if failure.owner.reservation_retired_proven {
                AccessPolicy::Absent
            } else if failure
                .owner
                .published
                .reservation
                .verify_after_main(&failure.owner.published.output)
                .is_ok()
            {
                AccessPolicy::CreatorOnly
            } else {
                AccessPolicy::ChangedOrUnproven
            };
            let mut cleanup = CleanupArtifacts::new();
            if !failure.owner.previous_retired_proven {
                if let Some(previous) = &failure.owner.published.output.previous {
                    cleanup.push(seed.artifact(
                        ArtifactKind::PrivateOutput,
                        NameSlot::PrivateOutput,
                        Some(previous.identity),
                        retirement,
                    ));
                }
            }
            if !failure.owner.reservation_retired_proven {
                cleanup.push(seed.artifact(
                    ArtifactKind::PrivateReservation,
                    NameSlot::Coordination,
                    Some(reservation_identity),
                    retirement,
                ));
            }
            seed.result(
                FinalState {
                    reservation_identity,
                    main_namespace_may_have_been_attempted: true,
                    publication: PublicationStatus::Published,
                    destination_content: DestinationContent::Desired,
                    main_access_policy: main_access,
                    coordination_access_policy: coordination,
                },
                cleanup,
                cause.or(Some(retirement)),
            )
        }
    }
}

fn draft_owner(draft: &ReservationDraft) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &draft.file,
        identity: draft.identity,
        private_name: &draft.name,
        location: ReservationLocation::Private,
    }
}

fn private_owner(reservation: &PrivateReservation) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &reservation.file,
        identity: Some(reservation.identity),
        private_name: &reservation.name,
        location: ReservationLocation::Private,
    }
}

fn acquiring_owner(reservation: &AcquiringReservation) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &reservation.reservation.file,
        identity: Some(reservation.reservation.identity),
        private_name: &reservation.reservation.name,
        location: ReservationLocation::Either,
    }
}

fn canonical_owner(reservation: &CanonicalReservation) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &reservation.file,
        identity: Some(reservation.identity),
        private_name: &reservation.name,
        location: ReservationLocation::Canonical,
    }
}

fn arming_owner(reservation: &ArmingReservation) -> ReservationOwner<'_> {
    canonical_owner(&reservation.reservation)
}

const fn cleanup_point(point: cleanup::Point) -> Point {
    match point {
        cleanup::Point::OutputRemoval => Point::CleanupOutput,
        cleanup::Point::ReservationRemoval => Point::CleanupReservation,
        cleanup::Point::DirectorySync => Point::CleanupDirectorySync,
    }
}

#[cfg(test)]
#[path = "attempt_tests.rs"]
mod tests;
