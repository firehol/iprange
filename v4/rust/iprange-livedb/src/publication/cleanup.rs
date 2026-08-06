//! Exact discard of direct-publication artifacts before main publication.

use std::fs::File;

#[cfg(unix)]
use super::namespace::{regular_identity, regular_link_count, Directory, NamespaceError};
use super::namespace::{Identity, Name};
use super::output::{CreatedOutput, OutputAttempt, PreparedOutput};
use super::problem::Problem;
#[cfg(unix)]
use super::result::NameSlot;
use super::result::{ArtifactKind, CleanupArtifacts, Seed};
use super::{
    CleanupArtifact, DirectoryRole, Housekeeping, HousekeepingArtifact, PrivateOutputAttempt,
};

#[cfg(windows)]
#[path = "cleanup/windows.rs"]
mod windows;

#[derive(Clone, Copy)]
pub(super) enum ReservationLocation {
    Private,
    Canonical,
    Either,
}

#[derive(Clone, Copy)]
pub(super) struct ReservationOwner<'a> {
    pub(super) file: &'a File,
    pub(super) identity: Option<Identity>,
    pub(super) private_name: &'a Name,
    pub(super) location: ReservationLocation,
}

#[derive(Clone, Copy)]
pub(super) struct OutputOwner<'a> {
    pub(super) file: &'a File,
    pub(super) identity: Identity,
    pub(super) name: &'a Name,
}

pub(super) struct Summary {
    pub(super) artifacts: CleanupArtifacts,
    pub(super) housekeeping: Housekeeping,
    pub(super) visible_housekeeping: Vec<HousekeepingArtifact>,
    pub(super) main_absent: bool,
    pub(super) coordination_absent: bool,
}

pub(crate) struct EarlyDiscard {
    pub(crate) output: PrivateOutputAttempt,
    pub(crate) artifact: Option<CleanupArtifact>,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible_housekeeping: Box<[HousekeepingArtifact]>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Point {
    OutputRemoval,
    ReservationRemoval,
    #[cfg(unix)]
    DirectorySync,
}

pub(crate) fn discard_created(created: &CreatedOutput) -> EarlyDiscard {
    let facts = created.facts();
    #[cfg(windows)]
    {
        windows::discard_created(created, facts)
    }
    #[cfg(unix)]
    {
        let identity = facts
            .identity
            .and_then(|value| Identity::decode(value.bytes));
        let problem = discard_one(
            created.destination().directory(),
            created.name(),
            created.file(),
            identity,
        );
        let artifact = problem.map(|problem| early_artifact(&facts, problem));
        EarlyDiscard {
            output: facts,
            artifact,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
        }
    }
}

pub(crate) fn discard_attempt(attempt: &OutputAttempt, file: &File) -> EarlyDiscard {
    let facts = attempt.facts();
    #[cfg(windows)]
    {
        windows::discard_attempt(attempt, file, facts)
    }
    #[cfg(unix)]
    {
        let problem = discard_one(
            attempt.destination().directory(),
            attempt.name(),
            file,
            Some(attempt.identity()),
        );
        let artifact = problem.map(|problem| early_artifact(&facts, problem));
        EarlyDiscard {
            output: facts,
            artifact,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
        }
    }
}

pub(super) fn discard_with(
    seed: &mut Seed,
    output: &PreparedOutput,
    reservation: Option<ReservationOwner<'_>>,
    checkpoint: impl FnMut(Point) -> Result<(), Problem>,
) -> Summary {
    let destination = output.attempt.destination();
    discard_owners_with(
        seed,
        destination,
        Some(OutputOwner {
            file: &output.file,
            identity: output.attempt.identity(),
            name: output.attempt.name(),
        }),
        reservation,
        checkpoint,
    )
}

pub(super) fn discard_recovered(
    seed: &mut Seed,
    destination: &super::namespace::Destination,
    output: Option<OutputOwner<'_>>,
    reservation: Option<ReservationOwner<'_>>,
) -> Summary {
    discard_owners_with(seed, destination, output, reservation, |_| Ok(()))
}

fn discard_owners_with(
    seed: &mut Seed,
    destination: &super::namespace::Destination,
    output: Option<OutputOwner<'_>>,
    reservation: Option<ReservationOwner<'_>>,
    mut checkpoint: impl FnMut(Point) -> Result<(), Problem>,
) -> Summary {
    #[cfg(windows)]
    {
        windows::discard_owners(seed, destination, output, reservation, &mut checkpoint)
    }
    #[cfg(unix)]
    {
        let directory = destination.directory();
        let output_removal = output.map(|owner| {
            checkpoint(Point::OutputRemoval)
                .and_then(|()| remove_output(directory, owner))
                .unwrap_or_else(|problem| {
                    Removal::failed(
                        ArtifactKind::PrivateOutput,
                        NameSlot::PrivateOutput,
                        Some(owner.identity),
                        problem,
                    )
                })
        });
        let reservation_removal = reservation.map(|owner| {
            checkpoint(Point::ReservationRemoval)
                .and_then(|()| remove_reservation(directory, destination.coordination(), owner))
                .unwrap_or_else(|problem| {
                    Removal::failed(
                        ArtifactKind::PrivateReservation,
                        default_slot(owner.location),
                        owner.identity,
                        problem,
                    )
                })
        });
        let needs_sync = output_removal.as_ref().is_some_and(Removal::needs_sync)
            || reservation_removal
                .as_ref()
                .is_some_and(Removal::needs_sync);
        let sync = if needs_sync {
            checkpoint(Point::DirectorySync)
                .and_then(|()| directory.sync().map_err(|error| Problem::namespace(&error)))
                .and_then(|()| {
                    directory
                        .verify()
                        .map_err(|error| Problem::namespace(&error))
                })
                .err()
        } else {
            None
        };

        let mut artifacts = CleanupArtifacts::new();
        if let Some(removal) = output_removal {
            finish_removal(seed, removal, sync, &mut artifacts);
        }
        if let Some(removal) = reservation_removal {
            finish_removal(seed, removal, sync, &mut artifacts);
        }
        Summary {
            artifacts,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Vec::new(),
            main_absent: directory.require_absent(destination.main()).is_ok(),
            coordination_absent: directory.require_absent(destination.coordination()).is_ok(),
        }
    }
}

#[cfg(unix)]
fn discard_one(
    directory: &Directory,
    name: &Name,
    file: &File,
    identity: Option<Identity>,
) -> Option<Problem> {
    let identity = match identity {
        Some(identity) => identity,
        None => {
            return Some(Problem::cleanup_conflict(
                "private output identity was not established",
            ))
        }
    };
    let removal = match remove_output(
        directory,
        OutputOwner {
            file,
            identity,
            name,
        },
    ) {
        Ok(removal) => removal,
        Err(problem) => return Some(problem),
    };
    finish_one(directory, removal)
}

#[cfg(unix)]
fn finish_one(directory: &Directory, removal: Removal<'_>) -> Option<Problem> {
    match removal.state {
        RemovalState::Failed(problem) => Some(problem),
        RemovalState::NeedsSync(file) => directory
            .sync()
            .and_then(|()| directory.verify())
            .map_err(|error| Problem::namespace(&error))
            .and_then(|()| match links(file)? {
                0 => Ok(()),
                _ => Err(Problem::cleanup_conflict(
                    "private output removal was not proved",
                )),
            })
            .err(),
    }
}

pub(super) fn early_artifact(facts: &PrivateOutputAttempt, error: Problem) -> CleanupArtifact {
    CleanupArtifact {
        kind: ArtifactKind::PrivateOutput,
        directory_role: DirectoryRole::Destination,
        directory_identity: facts.directory_identity,
        basename_encoding: facts.basename_encoding,
        basename: facts.basename.clone(),
        identity: facts.identity,
        creation_security: Some(facts.creation_security.clone()),
        unpublished_tail: None,
        error,
    }
}

#[cfg(unix)]
fn remove_output<'a>(
    directory: &Directory,
    output: OutputOwner<'a>,
) -> Result<Removal<'a>, Problem> {
    remove(
        directory,
        output.file,
        output.identity,
        ArtifactKind::PrivateOutput,
        [Some((output.name, NameSlot::PrivateOutput)), None],
    )
}

#[cfg(unix)]
fn remove_reservation<'a>(
    directory: &Directory,
    canonical: &'a Name,
    owner: ReservationOwner<'a>,
) -> Result<Removal<'a>, Problem> {
    let identity = match owner.identity {
        Some(identity) => identity,
        None => regular_identity(owner.file, directory.identity())
            .map_err(|error| Problem::namespace(&error))?,
    };
    let names = match owner.location {
        ReservationLocation::Private => [
            Some((owner.private_name, NameSlot::PrivateReservation)),
            None,
        ],
        ReservationLocation::Canonical => [
            Some((canonical, NameSlot::Coordination)),
            Some((owner.private_name, NameSlot::PrivateReservation)),
        ],
        ReservationLocation::Either => [
            Some((owner.private_name, NameSlot::PrivateReservation)),
            Some((canonical, NameSlot::Coordination)),
        ],
    };
    remove(
        directory,
        owner.file,
        identity,
        ArtifactKind::PrivateReservation,
        names,
    )
}

#[cfg(unix)]
fn remove<'a>(
    directory: &Directory,
    file: &'a File,
    identity: Identity,
    kind: ArtifactKind,
    names: [Option<(&'a Name, NameSlot)>; 2],
) -> Result<Removal<'a>, Problem> {
    let default_name = names[0].expect("cleanup requires one exact name").1;
    match links(file)? {
        0 => return Ok(Removal::awaiting_sync(kind, default_name, identity, file)),
        1 => {}
        _ => {
            return Err(Problem::cleanup_conflict(
                "owned publication artifact has unexpected links",
            ))
        }
    }

    match unlink_names(directory, identity, names)? {
        Some(slot) => require_unlinked(kind, slot, identity, file),
        None if links(file)? == 0 => Ok(Removal::awaiting_sync(kind, default_name, identity, file)),
        None => Err(Problem::cleanup_conflict(
            "owned publication artifact has no exact retained name",
        )),
    }
}

#[cfg(unix)]
fn unlink_names(
    directory: &Directory,
    identity: Identity,
    names: [Option<(&Name, NameSlot)>; 2],
) -> Result<Option<NameSlot>, Problem> {
    let mut first_problem = None;
    for (name, slot) in names.into_iter().flatten() {
        match directory.unlink_exact(name, identity) {
            Ok(true) => return Ok(Some(slot)),
            Ok(false)
            | Err(NamespaceError::Missing | NamespaceError::IdentityChanged)
            | Err(NamespaceError::NotRegular) => {}
            Err(error @ NamespaceError::LinkCount(_)) => return Err(Problem::namespace(&error)),
            Err(error) => {
                first_problem.get_or_insert_with(|| Problem::namespace(&error));
            }
        }
    }
    match first_problem {
        Some(problem) => Err(problem),
        None => Ok(None),
    }
}

#[cfg(unix)]
fn require_unlinked<'a>(
    kind: ArtifactKind,
    name: NameSlot,
    identity: Identity,
    file: &'a File,
) -> Result<Removal<'a>, Problem> {
    if links(file)? != 0 {
        return Err(Problem::cleanup_conflict(
            "unlinked publication artifact still has links",
        ));
    }
    Ok(Removal::awaiting_sync(kind, name, identity, file))
}

#[cfg(unix)]
fn links(file: &File) -> Result<u64, Problem> {
    regular_link_count(file).map_err(|error| Problem::namespace(&error))
}

#[cfg(unix)]
fn finish_removal(
    seed: &mut Seed,
    removal: Removal<'_>,
    sync: Option<Problem>,
    artifacts: &mut CleanupArtifacts,
) {
    let problem = match removal.state {
        RemovalState::Failed(problem) => Some(problem),
        RemovalState::NeedsSync(file) => sync.or_else(|| match links(file) {
            Ok(0) => None,
            Ok(_) => Some(Problem::cleanup_conflict(
                "publication artifact removal was not proved",
            )),
            Err(problem) => Some(problem),
        }),
    };
    if let Some(problem) = problem {
        artifacts.push(seed.artifact(removal.kind, removal.name, removal.identity, problem));
    }
}

#[cfg(unix)]
struct Removal<'a> {
    kind: ArtifactKind,
    name: NameSlot,
    identity: Option<Identity>,
    state: RemovalState<'a>,
}

#[cfg(unix)]
impl<'a> Removal<'a> {
    fn awaiting_sync(
        kind: ArtifactKind,
        name: NameSlot,
        identity: Identity,
        file: &'a File,
    ) -> Self {
        Self {
            kind,
            name,
            identity: Some(identity),
            state: RemovalState::NeedsSync(file),
        }
    }

    fn failed(
        kind: ArtifactKind,
        name: NameSlot,
        identity: Option<Identity>,
        problem: Problem,
    ) -> Self {
        Self {
            kind,
            name,
            identity,
            state: RemovalState::Failed(problem),
        }
    }

    fn needs_sync(&self) -> bool {
        matches!(self.state, RemovalState::NeedsSync(_))
    }
}

#[cfg(unix)]
enum RemovalState<'a> {
    NeedsSync(&'a File),
    Failed(Problem),
}

#[cfg(unix)]
const fn default_slot(location: ReservationLocation) -> NameSlot {
    match location {
        ReservationLocation::Private | ReservationLocation::Either => NameSlot::PrivateReservation,
        ReservationLocation::Canonical => NameSlot::Coordination,
    }
}
