//! Linux retained-descriptor implementation of publication residue handling.

use std::fs::File;
use std::os::unix::fs::MetadataExt;
use std::path::Path;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::{Error, ErrorCode};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, MAIN_LIFETIME_LOCK};
use crate::validation::LocalFileIdentity;

use crate::publication::namespace::{Destination, Identity, Regular};
use crate::publication::output;
use crate::publication::problem::Problem;
use crate::publication::reservation::{self, Header, State};
use crate::publication::reservation_inspection;
use crate::publication::result::{FinalState, Seed};
use crate::publication::types::{
    AccessPolicy, CleanupArtifacts, CoordinationCleanup, DestinationContent, PublicationStatus,
};
use crate::publication::{PublicationDigest, PublicationTuple};

use super::{
    PublicationResidueCoordination, PublicationResidueHandle, PublicationResidueInspection,
    PublicationResidueMain, PublicationResidueMainContent, PublicationResidueRemoval,
};

const POSIX_IDENTITY: u16 = 1;
const OPERATION_LOCK: u64 = 0;
const RESERVATION_SIZE: usize = 2 * PAGE_SIZE;

#[derive(Debug)]
pub(super) struct Handle {
    destination: Destination,
    coordination: File,
    coordination_identity: Identity,
}

pub(super) fn inspect(
    path: &Path,
    cancellation: &CancellationToken,
) -> Result<PublicationResidueInspection, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    let destination = Destination::bind(path).map_err(|error| Problem::namespace(&error))?;
    let directory_identity = local(destination.directory().identity());
    let Some(regular) = destination
        .directory()
        .open_regular(destination.coordination(), true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        let publication = reservation_inspection::discover(&destination, cancellation)?
            .map(|reservation| reconstruct(&destination, reservation.header, reservation.access))
            .transpose()?;
        return Ok(PublicationResidueInspection {
            directory_identity,
            coordination_identity: None,
            coordination: PublicationResidueCoordination::Absent,
            publication,
            handle: None,
        });
    };
    destination
        .directory()
        .verify_name(destination.coordination(), regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let (coordination, publication) = classify_coordination(&destination, &regular)?;
    destination
        .directory()
        .verify()
        .and_then(|()| {
            destination
                .directory()
                .verify_name(destination.coordination(), regular.identity)
        })
        .map_err(|error| Problem::namespace(&error))?;
    let identity = local(regular.identity);
    Ok(PublicationResidueInspection {
        directory_identity,
        coordination_identity: Some(identity),
        coordination,
        publication,
        handle: Some(PublicationResidueHandle {
            inner: Handle {
                destination,
                coordination: regular.file,
                coordination_identity: regular.identity,
            },
        }),
    })
}

pub(super) fn remove(
    handle: Handle,
    cancellation: &CancellationToken,
) -> Result<PublicationResidueRemoval, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    verify_coordination(&handle)?;
    live_lock::lock_cancellable(
        &handle.coordination,
        OPERATION_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(|error| Problem::sdk(&error))?;
    verify_coordination(&handle)?;
    reject_selectable(&handle.coordination)?;
    let main = inspect_main(&handle.destination, cancellation)?;
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    if !handle
        .destination
        .directory()
        .unlink_exact(
            handle.destination.coordination(),
            handle.coordination_identity,
        )
        .map_err(|_| cleanup_conflict("canonical coordination ownership changed"))?
    {
        return Err(cleanup_conflict(
            "canonical coordination disappeared before removal",
        ));
    }
    if handle
        .coordination
        .metadata()
        .map_err(Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .nlink()
        != 0
    {
        return Ok(incomplete(
            &handle,
            main.as_ref(),
            cleanup_conflict("removed coordination inode remains linked"),
        ));
    }
    let later = match finish_removal(&handle, main.as_ref()) {
        Ok(later) => later,
        Err(problem) => return Ok(incomplete(&handle, main.as_ref(), problem)),
    };
    Ok(PublicationResidueRemoval {
        directory_identity: local(handle.destination.directory().identity()),
        coordination_identity: local(handle.coordination_identity),
        main: main.as_ref().map(|main| main.evidence),
        later_coordination: later.kind,
        coordination_access_policy: later.access,
        cleanup: CleanupArtifacts::new(),
        coordination_cleanup: CoordinationCleanup::None,
        cause: None,
    })
}

fn classify_coordination(
    destination: &Destination,
    regular: &Regular,
) -> Result<
    (
        PublicationResidueCoordination,
        Option<crate::publication::PublicationResult>,
    ),
    Problem,
> {
    if let Some(header) = selected_bound_header(destination, regular)? {
        let access = reservation_access(regular, header);
        return Ok((
            PublicationResidueCoordination::PublicationReservation,
            Some(reconstruct(destination, header, access)?),
        ));
    }
    if live_sidecar::has_selectable_header(&regular.file).map_err(|error| Problem::sdk(&error))? {
        return Ok((PublicationResidueCoordination::LiveSidecar, None));
    }
    Ok((PublicationResidueCoordination::Unselectable, None))
}

fn selected_bound_header(
    destination: &Destination,
    regular: &Regular,
) -> Result<Option<Header>, Problem> {
    let Some(bytes) = reservation_bytes(&regular.file)? else {
        return Ok(None);
    };
    let Ok(selected) = reservation::select(&bytes) else {
        return Ok(None);
    };
    if reservation_inspection::require_bound(destination, selected.header, regular.identity, None)
        .is_err()
    {
        return Ok(None);
    }
    Ok(Some(selected.header))
}

fn reconstruct(
    destination: &Destination,
    header: Header,
    coordination_access: AccessPolicy,
) -> Result<crate::publication::PublicationResult, Problem> {
    let seed =
        Seed::reconstruct(destination, header).map_err(|error| Problem::namespace(&error))?;
    let publication = if header.state == State::Prepared {
        PublicationStatus::NotPublished
    } else {
        PublicationStatus::OutcomeUnknown
    };
    Ok(seed.result(
        FinalState {
            reservation_identity: Identity::decode(header.reservation_identity)
                .expect("selected reservation identity is valid"),
            main_namespace_may_have_been_attempted: header.state == State::MainMayHaveBeenAttempted,
            publication,
            destination_content: DestinationContent::Unclassified,
            main_access_policy: AccessPolicy::Unclassified,
            coordination_access_policy: coordination_access,
        },
        CleanupArtifacts::new(),
        None,
    ))
}

fn reservation_access(regular: &Regular, header: Header) -> AccessPolicy {
    match regular.creator_only_commitment() {
        Ok(commitment) if commitment == header.security_commitment => AccessPolicy::CreatorOnly,
        _ => AccessPolicy::ChangedOrUnproven,
    }
}

fn verify_coordination(handle: &Handle) -> Result<(), Problem> {
    handle
        .destination
        .directory()
        .verify()
        .and_then(|()| {
            handle.destination.directory().verify_name(
                handle.destination.coordination(),
                handle.coordination_identity,
            )
        })
        .map_err(|_| cleanup_conflict("canonical coordination ownership changed"))
}

fn reject_selectable(file: &File) -> Result<(), Problem> {
    if reservation_bytes(file)?
        .as_ref()
        .is_some_and(|bytes| reservation::contains_selectable_header(bytes))
        || live_sidecar::has_selectable_header(file).map_err(|error| Problem::sdk(&error))?
    {
        return Err(selection_conflict(
            "selectable coordination requires its operation-specific resolver",
        ));
    }
    Ok(())
}

fn reservation_bytes(file: &File) -> Result<Option<[u8; RESERVATION_SIZE]>, Problem> {
    if file
        .metadata()
        .map_err(Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len()
        != RESERVATION_SIZE as u64
    {
        return Ok(None);
    }
    let mut bytes = [0; RESERVATION_SIZE];
    file_io::read_exact_at(file, &mut bytes, 0).map_err(|error| Problem::sdk(&error))?;
    Ok(Some(bytes))
}

fn inspect_main(
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<Option<MainGuard>, Problem> {
    let Some(regular) = destination
        .directory()
        .open_regular(destination.main(), true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    live_lock::lock_cancellable(
        &regular.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(|error| Problem::sdk(&error))?;
    destination
        .directory()
        .verify_name(destination.main(), regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let byte_length = regular
        .file
        .metadata()
        .map_err(Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    let sha512 = output::digest_cancellable(&regular.file, byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    let tuple = read_tuple(&regular.file, byte_length)?;
    let access_policy = match crate::publication::security::creator_only_commitment(&regular.file) {
        Ok(_) => AccessPolicy::CreatorOnly,
        Err(_) => AccessPolicy::ChangedOrUnproven,
    };
    Ok(Some(MainGuard {
        file: regular.file,
        identity: regular.identity,
        byte_length,
        evidence: PublicationResidueMain {
            identity: local(regular.identity),
            content: if tuple.is_some() {
                PublicationResidueMainContent::V4
            } else {
                PublicationResidueMainContent::Other
            },
            tuple,
            digest: PublicationDigest {
                byte_length,
                sha512,
            },
            access_policy,
        },
    }))
}

fn read_tuple(file: &File, byte_length: u64) -> Result<Option<PublicationTuple>, Problem> {
    if byte_length < (2 * PAGE_SIZE) as u64 || byte_length % PAGE_SIZE as u64 != 0 {
        return Ok(None);
    }
    let mut bytes = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut bytes, 0).map_err(|error| Problem::sdk(&error))?;
    let left = (&bytes[..PAGE_SIZE]).try_into().expect("fixed meta page");
    let right = (&bytes[PAGE_SIZE..]).try_into().expect("fixed meta page");
    let Ok(opened) =
        bootstrap::open_meta_pages(left, right, byte_length, OpenMode::ImmutableReader)
    else {
        return Ok(None);
    };
    Ok(Some(PublicationTuple {
        database_id: opened.meta.database_id,
        transaction_id: opened.meta.txn_id,
        commit_nonce: opened.meta.commit_nonce,
    }))
}

fn finish_removal(handle: &Handle, main: Option<&MainGuard>) -> Result<FinalCoordination, Problem> {
    handle
        .destination
        .directory()
        .sync()
        .and_then(|()| handle.destination.directory().verify())
        .map_err(|error| Problem::namespace(&error))?;
    match main {
        Some(main) => main.verify(&handle.destination)?,
        None => handle
            .destination
            .directory()
            .require_absent(handle.destination.main())
            .map_err(|_| cleanup_conflict("destination main appeared during removal"))?,
    }
    final_coordination(handle)
}

fn final_coordination(handle: &Handle) -> Result<FinalCoordination, Problem> {
    let Some(regular) = handle
        .destination
        .directory()
        .open_regular(handle.destination.coordination(), true)
        .map_err(|_| cleanup_conflict("coordination reuse cannot be inspected"))?
    else {
        return Ok(FinalCoordination {
            kind: PublicationResidueCoordination::Absent,
            access: AccessPolicy::Absent,
        });
    };
    if regular.identity == handle.coordination_identity {
        return Err(cleanup_conflict(
            "removed coordination inode returned to its canonical name",
        ));
    }
    let (kind, publication) = classify_coordination(&handle.destination, &regular)?;
    let access = match kind {
        PublicationResidueCoordination::PublicationReservation => {
            publication
                .expect("reservation classification reconstructs publication")
                .coordination_access_policy
        }
        PublicationResidueCoordination::LiveSidecar => AccessPolicy::ChangedOrUnproven,
        PublicationResidueCoordination::Absent => unreachable!("opened coordination is present"),
        PublicationResidueCoordination::Unselectable => {
            return Err(cleanup_conflict(
                "coordination name was reused by an unselectable inode",
            ))
        }
    };
    handle
        .destination
        .directory()
        .verify_name(handle.destination.coordination(), regular.identity)
        .map_err(|_| cleanup_conflict("coordination reuse changed during inspection"))?;
    Ok(FinalCoordination { kind, access })
}

fn incomplete(
    handle: &Handle,
    main: Option<&MainGuard>,
    cause: Problem,
) -> PublicationResidueRemoval {
    PublicationResidueRemoval {
        directory_identity: local(handle.destination.directory().identity()),
        coordination_identity: local(handle.coordination_identity),
        main: main.map(|main| main.evidence),
        later_coordination: PublicationResidueCoordination::Unselectable,
        coordination_access_policy: AccessPolicy::Unclassified,
        cleanup: CleanupArtifacts::new(),
        coordination_cleanup: CoordinationCleanup::CleanupGuard,
        cause: Some(cause),
    }
}

#[derive(Debug)]
struct MainGuard {
    file: File,
    identity: Identity,
    byte_length: u64,
    evidence: PublicationResidueMain,
}

#[derive(Clone, Copy)]
struct FinalCoordination {
    kind: PublicationResidueCoordination,
    access: AccessPolicy,
}

impl MainGuard {
    fn verify(&self, destination: &Destination) -> Result<(), Problem> {
        destination
            .directory()
            .verify_name(destination.main(), self.identity)
            .map_err(|error| Problem::namespace(&error))?;
        if self
            .file
            .metadata()
            .map_err(Error::from)
            .map_err(|error| Problem::sdk(&error))?
            .len()
            != self.byte_length
        {
            return Err(cleanup_conflict(
                "destination main length changed during removal",
            ));
        }
        Ok(())
    }
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: POSIX_IDENTITY,
        bytes: identity.encode(),
    }
}

const fn selection_conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}

const fn cleanup_conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::CleanupConflict, None, detail)
}
