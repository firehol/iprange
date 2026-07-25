//! Final durability and namespace checks for restart resolution.

use crate::cancellation::CancellationToken;
use crate::error::ErrorCode;

use super::{
    cleanup, file_inspection, reservation_inspection, Destination, DestinationContent, Header,
    Inspected, Location, Problem,
};

pub(super) fn verify_destination(
    destination: &Destination,
    content: DestinationContent,
    main: Option<&file_inspection::Inspected>,
    summary: &cleanup::Summary,
) -> Result<(), Problem> {
    match (content, main) {
        (DestinationContent::Absent, None) if summary.main_absent => Ok(()),
        (DestinationContent::Other, Some(main)) => main.verify(destination),
        _ => Err(Problem::new(
            ErrorCode::CleanupConflict,
            None,
            "destination changed during publication cleanup",
        )),
    }
}

pub(super) fn verify_no_later(
    destination: &Destination,
    reservation: Option<&Inspected>,
    summary: &cleanup::Summary,
) -> Result<(), Problem> {
    if summary.coordination_absent {
        return Ok(());
    }
    match reservation {
        Some(reservation) if reservation.location == Location::Canonical => {
            reservation.verify(destination)
        }
        _ => Err(conflict(
            "another coordination owner appeared during publication cleanup",
        )),
    }
}

pub(super) fn final_later(
    destination: &Destination,
    header: Header,
    reservation: Option<&Inspected>,
    later: Option<Inspected>,
    summary: &cleanup::Summary,
) -> Result<Option<Inspected>, Problem> {
    if later.is_some() || summary.coordination_absent {
        return Ok(later);
    }
    if reservation.is_some_and(|reservation| {
        reservation.location == Location::Canonical && reservation.verify(destination).is_ok()
    }) {
        return Ok(None);
    }
    let current = reservation_inspection::canonical(destination, &CancellationToken::new())?;
    match current {
        Some(current) if current.header.attempt_id == header.attempt_id => Err(conflict(
            "publication coordination identity changed for the same attempt",
        )),
        current => Ok(current),
    }
}

pub(super) fn synchronize(
    destination: &Destination,
    main: &file_inspection::Inspected,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    check_cancellation(cancellation)?;
    super::super::namespace::sync_file(&main.file)
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?;
    main.verify(destination)
}

pub(super) fn check_cancellation(cancellation: &CancellationToken) -> Result<(), Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}
