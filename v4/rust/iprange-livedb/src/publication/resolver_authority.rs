//! Selection and reconstruction of one exact publication authority.

use std::path::Path;

use crate::cancellation::CancellationToken;

use crate::publication::namespace::Destination;
use crate::publication::problem::Problem;
use crate::publication::reservation::Header;
use crate::publication::reservation_inspection::{self, Inspected, Location};
use crate::publication::result::{PublicationResult, Seed};

use super::{conflict, unresolvable};

pub(super) fn inspect(
    path: &Path,
    supplied: Option<&PublicationResult>,
    cancellation: &CancellationToken,
) -> Result<BaseResolution, Problem> {
    let destination = Destination::bind(path).map_err(|error| Problem::namespace(&error))?;
    let supplied_header = supplied
        .map(|result| result.header_for(&destination))
        .transpose()?;
    let authority = inspect_authority(&destination, supplied_header, cancellation)?;
    let header = authority.header;
    let seed =
        Seed::reconstruct(&destination, header).map_err(|error| Problem::namespace(&error))?;
    Ok(BaseResolution {
        destination,
        header,
        seed,
        exact: authority.exact,
        later: authority.later,
    })
}

fn inspect_authority(
    destination: &Destination,
    supplied_header: Option<Header>,
    cancellation: &CancellationToken,
) -> Result<Authority, Problem> {
    let discovered = reservation_inspection::discover(destination, cancellation)?;
    let mut authority = choose_authority(supplied_header, discovered)?;
    if let Some(header) = supplied_header {
        if authority.exact.is_none() {
            authority.exact =
                reservation_inspection::exact_private(destination, header, cancellation)?;
        }
    }
    Ok(authority)
}

fn choose_authority(
    supplied: Option<Header>,
    discovered: Option<Inspected>,
) -> Result<Authority, Problem> {
    match (supplied, discovered) {
        (None, None) => Err(unresolvable(
            "no publication result or bound reservation is available",
        )),
        (Some(header), None) => Ok(Authority {
            header,
            exact: None,
            later: None,
        }),
        (None, Some(reservation)) => Ok(Authority {
            header: reservation.header,
            exact: Some(reservation),
            later: None,
        }),
        (Some(header), Some(reservation)) if header == reservation.header => Ok(Authority {
            header,
            exact: Some(reservation),
            later: None,
        }),
        (Some(header), Some(reservation)) if header.attempt_id == reservation.header.attempt_id => {
            Err(conflict(
                "caller result and reservation disagree for the same attempt",
            ))
        }
        (Some(header), Some(reservation)) if reservation.location == Location::Canonical => {
            Ok(Authority {
                header,
                exact: None,
                later: Some(reservation),
            })
        }
        (Some(_), Some(_)) => Err(conflict(
            "another private publication attempt is bound to the destination",
        )),
    }
}

struct Authority {
    header: Header,
    exact: Option<Inspected>,
    later: Option<Inspected>,
}

pub(super) struct BaseResolution {
    pub(super) destination: Destination,
    pub(super) header: Header,
    pub(super) seed: Seed,
    pub(super) exact: Option<Inspected>,
    pub(super) later: Option<Inspected>,
}
