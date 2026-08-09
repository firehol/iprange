//! Structured result construction after resolver classification.

use crate::cancellation::CancellationToken;
use crate::error::ErrorCode;

use super::super::result::{
    AccessPolicy, CleanupArtifacts, DestinationContent, FinalState, LaterCanonical,
    PublicationResult, PublicationStatus, Seed,
};
use super::{
    cleanup, file_inspection, reservation_identity, verify_later, Destination, Header, Inspected,
    Location, Problem,
};

pub(super) struct DesiredContext<'a> {
    pub(super) destination: &'a Destination,
    pub(super) header: Header,
    pub(super) reservation: Option<&'a Inspected>,
    pub(super) later: Option<&'a Inspected>,
    pub(super) main: &'a file_inspection::Inspected,
}

pub(super) fn record_cancellation(
    mut result: PublicationResult,
    cancellation: &CancellationToken,
) -> PublicationResult {
    let cleanup_cause = result.cleanup.get(0).map(|artifact| artifact.error.clone());
    if cancellation.is_cancelled() && (result.cause.is_none() || result.cause == cleanup_cause) {
        result.cause = Some(Problem::new(
            ErrorCode::Cancelled,
            None,
            "publication resolution was cancelled after mutation",
        ));
    }
    result
}

pub(super) fn desired_result(
    seed: Seed,
    summary: cleanup::Summary,
    context: DesiredContext<'_>,
) -> PublicationResult {
    let DesiredContext {
        destination,
        header,
        reservation,
        later,
        main,
    } = context;
    let verification = main
        .verify(destination)
        .and_then(|()| verify_later(later, destination));
    let cause = verification
        .as_ref()
        .err()
        .cloned()
        .or_else(|| first_problem(&summary.artifacts));
    seed.result_with_housekeeping(
        desired_state(
            header,
            main,
            &summary,
            reservation,
            later,
            verification.is_ok(),
        ),
        summary.artifacts,
        summary.housekeeping,
        summary.visible_housekeeping,
        cause,
    )
    .with_later(later)
}

pub(super) fn desired_problem(
    seed: Seed,
    header: Header,
    summary: cleanup::Summary,
    problem: Problem,
) -> PublicationResult {
    seed.result_with_housekeeping(
        FinalState {
            reservation_identity: reservation_identity(header),
            main_namespace_may_have_been_attempted: true,
            publication: PublicationStatus::OutcomeUnknown,
            destination_content: DestinationContent::Unclassified,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy: AccessPolicy::Unclassified,
        },
        summary.artifacts,
        summary.housekeeping,
        summary.visible_housekeeping,
        Some(problem),
    )
}

pub(super) fn published_output_result(
    seed: Seed,
    summary: cleanup::Summary,
    problem: Problem,
    context: DesiredContext<'_>,
) -> PublicationResult {
    let DesiredContext {
        destination,
        header,
        reservation,
        later,
        main,
    } = context;
    let verified = main
        .verify(destination)
        .and_then(|()| verify_later(later, destination))
        .is_ok();
    seed.result_with_housekeeping(
        desired_state(header, main, &summary, reservation, later, verified),
        summary.artifacts,
        summary.housekeeping,
        summary.visible_housekeeping,
        Some(problem),
    )
    .with_later(later)
}

fn desired_state(
    header: Header,
    main: &file_inspection::Inspected,
    summary: &cleanup::Summary,
    reservation: Option<&Inspected>,
    later: Option<&Inspected>,
    verified: bool,
) -> FinalState {
    FinalState {
        reservation_identity: reservation_identity(header),
        main_namespace_may_have_been_attempted: true,
        publication: if verified {
            PublicationStatus::Published
        } else {
            PublicationStatus::OutcomeUnknown
        },
        destination_content: if verified {
            DestinationContent::Desired
        } else {
            DestinationContent::Unclassified
        },
        main_access_policy: if verified {
            main.access
        } else {
            AccessPolicy::Unclassified
        },
        coordination_access_policy: if verified {
            coordination_access(summary, reservation, later)
        } else {
            AccessPolicy::Unclassified
        },
    }
}

pub(super) fn coordination_access(
    summary: &cleanup::Summary,
    reservation: Option<&Inspected>,
    later: Option<&Inspected>,
) -> AccessPolicy {
    if summary.coordination_absent {
        AccessPolicy::Absent
    } else {
        later
            .or_else(|| {
                reservation.filter(|reservation| reservation.location == Location::Canonical)
            })
            .map_or(AccessPolicy::Unclassified, |reservation| reservation.access)
    }
}

pub(super) fn first_problem(cleanup: &CleanupArtifacts) -> Option<Problem> {
    cleanup.get(0).map(|artifact| artifact.error.clone())
}

pub(super) trait LaterResult {
    fn with_later(self, later: Option<&Inspected>) -> Self;
}

impl LaterResult for PublicationResult {
    fn with_later(mut self, later: Option<&Inspected>) -> Self {
        if later.is_some_and(|reservation| reservation.location == Location::Canonical) {
            self.later_canonical = LaterCanonical::ReservationOrTransition;
        }
        self
    }
}
