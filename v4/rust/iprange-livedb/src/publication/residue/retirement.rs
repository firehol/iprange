//! Platform-specific retirement of one retained coordination inode.

use std::fs::File;

#[cfg(unix)]
use crate::publication::namespace::regular_link_count;
use crate::publication::namespace::{Destination, Identity};
use crate::publication::problem::Problem;
#[cfg(windows)]
use crate::publication::{ArtifactKind, DirectoryRole};
use crate::publication::{Housekeeping, HousekeepingArtifact};

pub(super) struct Outcome {
    pub(super) cause: Option<Problem>,
    pub(super) housekeeping: Housekeeping,
    pub(super) visible: Vec<HousekeepingArtifact>,
}

pub(super) fn retire(
    destination: &Destination,
    file: &File,
    identity: Identity,
) -> Result<Outcome, Problem> {
    #[cfg(unix)]
    {
        retire_unix(destination, file, identity)
    }
    #[cfg(windows)]
    {
        retire_windows(destination, file, identity)
    }
}

#[cfg(unix)]
fn retire_unix(
    destination: &Destination,
    file: &File,
    identity: Identity,
) -> Result<Outcome, Problem> {
    if !destination
        .directory()
        .unlink_exact(destination.coordination(), identity)
        .map_err(|_| cleanup_conflict("canonical coordination ownership changed"))?
    {
        return Err(cleanup_conflict(
            "canonical coordination disappeared before removal",
        ));
    }
    let cause = match regular_link_count(file) {
        Ok(0) => None,
        Ok(_) => Some(cleanup_conflict(
            "removed coordination inode remains linked",
        )),
        Err(error) => Some(Problem::namespace(&error)),
    };
    Ok(Outcome {
        cause,
        housekeeping: Housekeeping::None,
        visible: Vec::new(),
    })
}

#[cfg(windows)]
fn retire_windows(
    destination: &Destination,
    file: &File,
    identity: Identity,
) -> Result<Outcome, Problem> {
    use crate::publication::gc::{self, Authority};
    use crate::publication::namespace::CREATION_SECURITY_KIND;
    use crate::publication::security;
    use crate::publication::CreationSecurity;

    let attempt_id = gc::fresh_attempt(
        destination.directory(),
        destination.coordination(),
        identity,
        1,
        ArtifactKind::OwnedCoordination,
        DirectoryRole::Destination,
    )?;
    let commitment =
        security::creator_only_commitment(file).map_err(|error| Problem::namespace(&error))?;
    let retired = gc::retire(
        destination.directory(),
        Authority {
            attempt_id,
            ordinal: 1,
            kind: ArtifactKind::OwnedCoordination,
            directory_role: DirectoryRole::Destination,
            source_name: destination.coordination(),
            source_file: file,
            identity,
            creation_security: CreationSecurity {
                kind: CREATION_SECURITY_KIND,
                commitment,
            },
            payload: None,
        },
    );
    Ok(Outcome {
        cause: retired.problem,
        housekeeping: retired.housekeeping,
        visible: retired.visible.into_iter().collect(),
    })
}

const fn cleanup_conflict(detail: &'static str) -> Problem {
    Problem::cleanup_conflict(detail)
}
