//! Exact discard of direct-publication artifacts before main publication.

use std::fs::File;
use std::os::unix::fs::MetadataExt;

use super::namespace::{regular_identity, Directory, Identity, Name, NamespaceError};
use super::output::PreparedOutput;
use super::problem::Problem;
use super::result::{ArtifactKind, CleanupArtifacts, NameSlot, Seed};

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

pub(super) struct Summary {
    pub(super) artifacts: CleanupArtifacts,
    pub(super) main_absent: bool,
    pub(super) coordination_absent: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Point {
    OutputRemoval,
    ReservationRemoval,
    DirectorySync,
}

pub(super) fn discard(
    seed: &mut Seed,
    output: &PreparedOutput,
    reservation: Option<ReservationOwner<'_>>,
) -> Summary {
    discard_with(seed, output, reservation, |_| Ok(()))
}

pub(super) fn discard_with(
    seed: &mut Seed,
    output: &PreparedOutput,
    reservation: Option<ReservationOwner<'_>>,
    mut checkpoint: impl FnMut(Point) -> Result<(), Problem>,
) -> Summary {
    let destination = output.attempt.destination();
    let directory = destination.directory();
    let output_removal = checkpoint(Point::OutputRemoval)
        .and_then(|()| remove_output(directory, output))
        .unwrap_or_else(|problem| {
            Removal::failed(
                ArtifactKind::PrivateOutput,
                NameSlot::PrivateOutput,
                Some(output.attempt.identity()),
                problem,
            )
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
    let needs_sync = output_removal.needs_sync()
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
    finish_removal(seed, output_removal, sync, &mut artifacts);
    if let Some(removal) = reservation_removal {
        finish_removal(seed, removal, sync, &mut artifacts);
    }
    Summary {
        artifacts,
        main_absent: directory.require_absent(destination.main()).is_ok(),
        coordination_absent: directory.require_absent(destination.coordination()).is_ok(),
    }
}

fn remove_output<'a>(
    directory: &Directory,
    output: &'a PreparedOutput,
) -> Result<Removal<'a>, Problem> {
    remove(
        directory,
        &output.file,
        output.attempt.identity(),
        ArtifactKind::PrivateOutput,
        [Some((output.attempt.name(), NameSlot::PrivateOutput)), None],
    )
}

fn remove_reservation<'a>(
    directory: &Directory,
    canonical: &'a Name,
    owner: ReservationOwner<'a>,
) -> Result<Removal<'a>, Problem> {
    let identity = match owner.identity {
        Some(identity) => identity,
        None => regular_identity(owner.file, directory.identity().device)
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

fn links(file: &File) -> Result<u64, Problem> {
    file.metadata()
        .map(|metadata| metadata.nlink())
        .map_err(|error| Problem {
            code: crate::error::ErrorCode::Io,
            os_code: error.raw_os_error(),
            detail: "inspect publication artifact during cleanup",
        })
}

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

struct Removal<'a> {
    kind: ArtifactKind,
    name: NameSlot,
    identity: Option<Identity>,
    state: RemovalState<'a>,
}

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

enum RemovalState<'a> {
    NeedsSync(&'a File),
    Failed(Problem),
}

const fn default_slot(location: ReservationLocation) -> NameSlot {
    match location {
        ReservationLocation::Private | ReservationLocation::Either => NameSlot::PrivateReservation,
        ReservationLocation::Canonical => NameSlot::Coordination,
    }
}
