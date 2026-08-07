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

pub(crate) enum PublicationCheckpoint<'a> {
    Preparation(&'a PreparationFailure),
    Result(&'a PublicationResult),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Point {
    ReservationCreated,
    State1Selected,
    ReservationAcquired,
    State2Selected,
    DesiredProven,
    CleanupOutput,
    CleanupReservation,
    #[cfg(unix)]
    CleanupDirectorySync,
}

pub(crate) fn fail_if_exists_cancellable(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result {
    debug_assert!(output.previous.is_none());
    debug_assert_eq!(output.policy, super::reservation::Policy::FailIfExists);
    publish_with_observer(
        output,
        Some(cancellation),
        |point| {
            if cleanup_ignores_cancellation(point) {
                return Ok(());
            }
            cancellation.check().map_err(|error| Problem::sdk(&error))
        },
        false,
        |_| Ok(()),
    )
}

pub(crate) fn fail_if_exists_cancellable_observed(
    output: PreparedOutput,
    cancellation: &CancellationToken,
    observer: impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> Result {
    debug_assert!(output.previous.is_none());
    debug_assert_eq!(output.policy, super::reservation::Policy::FailIfExists);
    publish_with_observer(
        output,
        Some(cancellation),
        |point| {
            if cleanup_ignores_cancellation(point) {
                return Ok(());
            }
            cancellation.check().map_err(|error| Problem::sdk(&error))
        },
        true,
        observer,
    )
}

pub(crate) fn replace_existing_cancellable(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result {
    debug_assert!(output.previous.is_some());
    debug_assert!(output.policy.is_replacement());
    publish_with_observer(
        output,
        Some(cancellation),
        |point| {
            if cleanup_ignores_cancellation(point) {
                return Ok(());
            }
            cancellation.check().map_err(|error| Problem::sdk(&error))
        },
        false,
        |_| Ok(()),
    )
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

#[cfg(all(test, unix))]
fn fail_if_exists_with(
    output: PreparedOutput,
    checkpoint: impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    publish_with(output, None, checkpoint)
}

#[cfg(all(test, unix))]
fn publish_with(
    output: PreparedOutput,
    cancellation: Option<&CancellationToken>,
    checkpoint: impl FnMut(Point) -> std::result::Result<(), Problem>,
) -> Result {
    publish_with_observer(output, cancellation, checkpoint, false, |_| Ok(()))
}

fn publish_with_observer(
    output: PreparedOutput,
    cancellation: Option<&CancellationToken>,
    mut checkpoint: impl FnMut(Point) -> std::result::Result<(), Problem>,
    observe: bool,
    mut observer: impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
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
    if let Err(cause) = observe_preparation(
        &seed,
        &output,
        draft.identity,
        NameSlot::PrivateReservation,
        observe,
        &mut observer,
    )
    .and_then(|()| checkpoint(Point::ReservationCreated))
    {
        return preparation(
            seed,
            output,
            Some(draft_owner(&draft)),
            cause,
            &mut checkpoint,
        );
    }

    let reservation = match draft.initialize_observed(&output, |identity| {
        observe_not_published(
            &seed,
            output.attempt.identity(),
            identity,
            NameSlot::PrivateReservation,
            AccessPolicy::Unclassified,
            observe,
            &mut observer,
        )?;
        checkpoint(Point::State1Selected)
    }) {
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
    from_private(
        seed,
        output,
        reservation,
        cancellation,
        &mut checkpoint,
        observe,
        &mut observer,
    )
}

fn from_private(
    seed: Seed,
    output: PreparedOutput,
    reservation: PrivateReservation,
    cancellation: Option<&CancellationToken>,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
    observe: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> Result {
    let reservation = match reservation.acquire_observed(&output, |identity| {
        observe_not_published(
            &seed,
            output.attempt.identity(),
            identity,
            NameSlot::Coordination,
            AccessPolicy::CreatorOnly,
            observe,
            observer,
        )
    }) {
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
    from_canonical(
        seed,
        output,
        reservation,
        cancellation,
        checkpoint,
        observe,
        observer,
    )
}

fn from_canonical(
    seed: Seed,
    output: PreparedOutput,
    reservation: CanonicalReservation,
    cancellation: Option<&CancellationToken>,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
    observe: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
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

    let reservation = match reservation.arm_observed(&output, |identity| {
        observe_outcome_unknown(&seed, identity, observe, observer)?;
        checkpoint(Point::State2Selected)
    }) {
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
    from_armed(seed, output, reservation, checkpoint, observe, observer)
}

fn from_armed(
    seed: Seed,
    output: PreparedOutput,
    reservation: ArmedReservation,
    checkpoint: &mut impl FnMut(Point) -> std::result::Result<(), Problem>,
    observe: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> Result {
    let reservation_identity = reservation.identity;
    match main_file::publish_observed(output, reservation, |point| {
        if point == main_file::Point::DesiredProven {
            observe_published(&seed, reservation_identity, observe, observer)
                .map_err(main_file::Error::Checkpoint)?;
            checkpoint(Point::DesiredProven).map_err(main_file::Error::Checkpoint)?;
        }
        Ok(())
    }) {
        Ok(published) => Ok(finish_published_observed(
            seed, published, None, observe, observer,
        )),
        Err(failure) => {
            let cause = Problem::main(&failure.cause);
            if failure.owner.desired_proven {
                Ok(finish_published_observed(
                    seed,
                    PublishedMain {
                        output: failure.owner.output,
                        reservation: failure.owner.reservation,
                    },
                    Some(cause),
                    observe,
                    observer,
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

fn observe_preparation(
    seed: &Seed,
    output: &PreparedOutput,
    reservation_identity: Option<super::namespace::Identity>,
    reservation_slot: NameSlot,
    enabled: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> std::result::Result<(), Problem> {
    if !enabled {
        return Ok(());
    }
    let problem = interrupted_problem();
    let mut checkpoint_seed = seed.clone();
    let mut cleanup = CleanupArtifacts::new();
    cleanup.push(checkpoint_seed.artifact(
        ArtifactKind::PrivateOutput,
        NameSlot::PrivateOutput,
        Some(output.attempt.identity()),
        problem.clone(),
    ));
    cleanup.push(checkpoint_seed.artifact(
        ArtifactKind::PrivateReservation,
        reservation_slot,
        reservation_identity,
        problem.clone(),
    ));
    let failure = checkpoint_seed.preparation_with_housekeeping(
        cleanup,
        super::Housekeeping::None,
        Vec::new(),
        problem,
    );
    observer(PublicationCheckpoint::Preparation(&failure))
}

fn observe_not_published(
    seed: &Seed,
    output_identity: super::namespace::Identity,
    reservation_identity: super::namespace::Identity,
    reservation_slot: NameSlot,
    coordination_access_policy: AccessPolicy,
    enabled: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> std::result::Result<(), Problem> {
    if !enabled {
        return Ok(());
    }
    let problem = interrupted_problem();
    let mut checkpoint_seed = seed.clone();
    let mut cleanup = CleanupArtifacts::new();
    cleanup.push(checkpoint_seed.artifact(
        ArtifactKind::PrivateOutput,
        NameSlot::PrivateOutput,
        Some(output_identity),
        problem.clone(),
    ));
    cleanup.push(checkpoint_seed.artifact(
        ArtifactKind::PrivateReservation,
        reservation_slot,
        Some(reservation_identity),
        problem.clone(),
    ));
    let result = checkpoint_seed.result(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: false,
            publication: PublicationStatus::NotPublished,
            destination_content: DestinationContent::Unclassified,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy,
        },
        cleanup,
        Some(problem),
    );
    observer(PublicationCheckpoint::Result(&result))
}

fn observe_outcome_unknown(
    seed: &Seed,
    reservation_identity: super::namespace::Identity,
    enabled: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> std::result::Result<(), Problem> {
    if !enabled {
        return Ok(());
    }
    let result = outcome_unknown(seed.clone(), reservation_identity, interrupted_problem());
    observer(PublicationCheckpoint::Result(&result))
}

fn observe_published(
    seed: &Seed,
    reservation_identity: super::namespace::Identity,
    enabled: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> std::result::Result<(), Problem> {
    if !enabled {
        return Ok(());
    }
    let problem = interrupted_problem();
    let mut checkpoint_seed = seed.clone();
    let mut cleanup = CleanupArtifacts::new();
    cleanup.push(checkpoint_seed.artifact(
        ArtifactKind::PrivateReservation,
        NameSlot::Coordination,
        Some(reservation_identity),
        problem.clone(),
    ));
    let result = checkpoint_seed.result(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: true,
            publication: PublicationStatus::Published,
            destination_content: DestinationContent::Desired,
            main_access_policy: AccessPolicy::CreatorOnly,
            coordination_access_policy: AccessPolicy::ChangedOrUnproven,
        },
        cleanup,
        Some(problem),
    );
    observer(PublicationCheckpoint::Result(&result))
}

fn interrupted_problem() -> Problem {
    Problem::new(
        crate::ErrorCode::Io,
        None,
        "mapped output fault interrupted publication",
    )
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
    Err(Box::new(seed.preparation_with_housekeeping(
        cleanup.artifacts,
        cleanup.housekeeping,
        cleanup.visible_housekeeping,
        cause,
    )))
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
    seed.result_with_housekeeping(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: false,
            publication: PublicationStatus::NotPublished,
            destination_content: content,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy: coordination,
        },
        cleanup.artifacts,
        cleanup.housekeeping,
        cleanup.visible_housekeeping,
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
    seed: Seed,
    published: PublishedMain,
    cause: Option<Problem>,
) -> PublicationResult {
    finish_published_observed(seed, published, cause, false, &mut |_| Ok(()))
}

fn finish_published_observed(
    mut seed: Seed,
    published: PublishedMain,
    cause: Option<Problem>,
    observe: bool,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> PublicationResult {
    let reservation_identity = published.reservation.identity;
    let retirement = if observe {
        published.retire_observed(|artifact| {
            observe_published_housekeeping(&seed, reservation_identity, artifact, observer)
        })
    } else {
        published.retire()
    };
    match retirement {
        Ok(completed) => seed.result_with_housekeeping(
            FinalState {
                reservation_identity: completed.reservation_identity,
                main_namespace_may_have_been_attempted: true,
                publication: PublicationStatus::Published,
                destination_content: DestinationContent::Desired,
                main_access_policy: AccessPolicy::CreatorOnly,
                coordination_access_policy: AccessPolicy::Absent,
            },
            CleanupArtifacts::new(),
            completed.housekeeping,
            completed.visible_housekeeping,
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
                        retirement.clone(),
                    ));
                }
            }
            if !failure.owner.reservation_retired_proven {
                cleanup.push(seed.artifact(
                    ArtifactKind::PrivateReservation,
                    NameSlot::Coordination,
                    Some(reservation_identity),
                    retirement.clone(),
                ));
            }
            let housekeeping = failure.owner.housekeeping;
            let visible_housekeeping = failure.owner.visible_housekeeping;
            seed.result_with_housekeeping(
                FinalState {
                    reservation_identity,
                    main_namespace_may_have_been_attempted: true,
                    publication: PublicationStatus::Published,
                    destination_content: DestinationContent::Desired,
                    main_access_policy: main_access,
                    coordination_access_policy: coordination,
                },
                cleanup,
                housekeeping,
                visible_housekeeping,
                cause.or(Some(retirement)),
            )
        }
    }
}

fn observe_published_housekeeping(
    seed: &Seed,
    reservation_identity: super::namespace::Identity,
    artifact: &super::HousekeepingArtifact,
    observer: &mut impl FnMut(PublicationCheckpoint<'_>) -> std::result::Result<(), Problem>,
) -> std::result::Result<(), Problem> {
    let problem = interrupted_problem();
    let mut checkpoint_seed = seed.clone();
    let mut cleanup = CleanupArtifacts::new();
    cleanup.push(super::CleanupArtifact {
        kind: artifact.kind,
        directory_role: artifact.directory_role,
        directory_identity: artifact.directory_identity,
        basename_encoding: artifact.basename_encoding,
        basename: artifact.source_basename.clone(),
        identity: artifact.source_identity,
        creation_security: Some(artifact.creation_security.clone()),
        unpublished_tail: None,
        error: problem.clone(),
    });
    if artifact.kind == ArtifactKind::PrivateOutput {
        cleanup.push(checkpoint_seed.artifact(
            ArtifactKind::PrivateReservation,
            NameSlot::Coordination,
            Some(reservation_identity),
            problem.clone(),
        ));
    }
    let result = checkpoint_seed.result_with_housekeeping(
        FinalState {
            reservation_identity,
            main_namespace_may_have_been_attempted: true,
            publication: PublicationStatus::Published,
            destination_content: DestinationContent::Desired,
            main_access_policy: AccessPolicy::CreatorOnly,
            coordination_access_policy: AccessPolicy::ChangedOrUnproven,
        },
        cleanup,
        super::Housekeeping::Visible,
        vec![artifact.clone()],
        Some(problem),
    );
    observer(PublicationCheckpoint::Result(&result))
}

fn draft_owner(draft: &ReservationDraft) -> ReservationOwner<'_> {
    ReservationOwner {
        file: &draft.file,
        identity: draft.identity,
        private_name: &draft.name,
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
        #[cfg(unix)]
        cleanup::Point::DirectorySync => Point::CleanupDirectorySync,
    }
}

const fn cleanup_ignores_cancellation(point: Point) -> bool {
    match point {
        Point::CleanupOutput | Point::CleanupReservation => true,
        #[cfg(unix)]
        Point::CleanupDirectorySync => true,
        _ => false,
    }
}

#[cfg(all(test, unix))]
#[path = "attempt_tests.rs"]
mod tests;
