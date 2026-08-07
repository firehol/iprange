//! Retained-descriptor implementation of publication residue handling.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::{Error, ErrorCode};
use crate::live_lock::{self, Mode};
use crate::live_sidecar;
use crate::mapping::Mapping;
use crate::validation::LocalFileIdentity;

use crate::publication::namespace::IDENTITY_KIND;
use crate::publication::namespace::{Destination, Identity, Regular};
use crate::publication::problem::Problem;
use crate::publication::reservation::{self, Header, State as ReservationState};
use crate::publication::reservation_inspection;
use crate::publication::result::{FinalState, Seed};
use crate::publication::types::{
    AccessPolicy, CleanupArtifacts, CoordinationCleanup, DestinationContent, PublicationStatus,
};
use crate::publication::{ArtifactKind, DirectoryRole, Housekeeping, HousekeepingArtifact};

use super::{
    PublicationResidueCoordination, PublicationResidueHandle, PublicationResidueInspection,
    PublicationResidueRemoval,
};

const OPERATION_LOCK: u64 = 0;
const RESERVATION_SIZE: usize = 2 * PAGE_SIZE;

#[derive(Debug)]
pub(super) struct Handle {
    destination: Destination,
    coordination: File,
    coordination_identity: Identity,
    retired: Option<Retired>,
}

#[derive(Debug)]
struct Retired {
    main: Option<super::main::Guard>,
    housekeeping: Housekeeping,
    visible: Vec<HousekeepingArtifact>,
    retirement_pending: bool,
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
                retired: None,
            },
        }),
    })
}

pub(super) fn remove(
    mut handle: Handle,
    cancellation: &CancellationToken,
) -> Result<PublicationResidueRemoval, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    if handle.retired.is_some() {
        return Ok(finish_retired(handle));
    }
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
    let main = super::main::inspect(&handle.destination, cancellation)?;
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    let retired = super::retirement::retire(
        &handle.destination,
        &handle.coordination,
        handle.coordination_identity,
    )?;
    let cause = retired.cause;
    handle.retired = Some(Retired {
        main,
        housekeeping: retired.housekeeping,
        visible: retired.visible,
        retirement_pending: cause.is_some(),
    });
    if let Some(cause) = cause {
        return Ok(incomplete(handle, cause));
    }
    Ok(finish_retired(handle))
}

fn finish_retired(mut handle: Handle) -> PublicationResidueRemoval {
    if let Some(cause) = retry_retirement(&mut handle) {
        return incomplete(handle, cause);
    }
    let main = handle
        .retired
        .as_ref()
        .expect("retired cleanup has retained state")
        .main
        .as_ref();
    let later = match finish_removal(&handle, main) {
        Ok(later) => later,
        Err(problem) => return incomplete(handle, problem),
    };
    let directory_identity = local(handle.destination.directory().identity());
    let coordination_identity = local(handle.coordination_identity);
    let Retired {
        main,
        housekeeping,
        visible,
        ..
    } = handle
        .retired
        .take()
        .expect("retired cleanup has retained state");
    PublicationResidueRemoval {
        directory_identity,
        coordination_identity,
        main: main.map(|main| main.evidence),
        later_coordination: later.kind,
        coordination_access_policy: later.access,
        cleanup: CleanupArtifacts::new(),
        coordination_cleanup: CoordinationCleanup::None,
        housekeeping,
        visible_housekeeping: visible.into_boxed_slice(),
        handle: None,
        cause: None,
    }
}

fn retry_retirement(handle: &mut Handle) -> Option<Problem> {
    let Retired {
        housekeeping,
        visible,
        retirement_pending,
        ..
    } = handle
        .retired
        .as_mut()
        .expect("retirement retry has retained state");
    let retried = super::retirement::retry(
        &handle.destination,
        &handle.coordination,
        handle.coordination_identity,
        *retirement_pending,
    );
    *housekeeping = merge_housekeeping(*housekeeping, retried.housekeeping);
    visible.extend(retried.visible);
    *retirement_pending = retried.cause.is_some();
    retried.cause
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
        require_coordination_available(
            destination,
            header.attempt_id,
            DirectoryRole::Destination,
            regular.identity,
        )?;
        let access = reservation_access(regular, header);
        return Ok((
            PublicationResidueCoordination::PublicationReservation,
            Some(reconstruct(destination, header, access)?),
        ));
    }
    if let Ok((_, header)) = live_sidecar::read_header(&regular.file) {
        require_coordination_available(
            destination,
            header.sidecar_id,
            DirectoryRole::MainFile,
            regular.identity,
        )?;
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
    let Some(mapping) = reservation_mapping(&regular.file)? else {
        return Ok(None);
    };
    let bytes = mapping
        .bytes(0, RESERVATION_SIZE)
        .map_err(|error| Problem::sdk(&error))?;
    let Ok(selected) = reservation::select(bytes) else {
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
    let publication = if header.state == ReservationState::Prepared {
        PublicationStatus::NotPublished
    } else {
        PublicationStatus::OutcomeUnknown
    };
    Ok(seed.result(
        FinalState {
            reservation_identity: Identity::decode(header.reservation_identity)
                .expect("selected reservation identity is valid"),
            main_namespace_may_have_been_attempted: header.state
                == ReservationState::MainMayHaveBeenAttempted,
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
    if reservation_mapping(file)?.is_some_and(|mapping| {
        mapping
            .bytes(0, RESERVATION_SIZE)
            .ok()
            .is_some_and(reservation::contains_selectable_header)
    }) || live_sidecar::has_selectable_header(file).map_err(|error| Problem::sdk(&error))?
    {
        return Err(selection_conflict(
            "selectable coordination requires its operation-specific resolver",
        ));
    }
    Ok(())
}

fn reservation_mapping(file: &File) -> Result<Option<Mapping>, Problem> {
    if file
        .metadata()
        .map_err(Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len()
        != RESERVATION_SIZE as u64
    {
        return Ok(None);
    }
    Mapping::read_only_view(file, RESERVATION_SIZE as u64)
        .map(Some)
        .map_err(|error| Problem::sdk(&error))
}

fn finish_removal(
    handle: &Handle,
    main: Option<&super::main::Guard>,
) -> Result<FinalCoordination, Problem> {
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

fn incomplete(handle: Handle, cause: Problem) -> PublicationResidueRemoval {
    let Retired {
        main,
        housekeeping,
        visible,
        ..
    } = handle
        .retired
        .as_ref()
        .expect("incomplete removal has retained state");
    let main = main.as_ref().map(|main| main.evidence);
    let housekeeping = *housekeeping;
    let visible_housekeeping = visible.clone().into_boxed_slice();
    PublicationResidueRemoval {
        directory_identity: local(handle.destination.directory().identity()),
        coordination_identity: local(handle.coordination_identity),
        main,
        later_coordination: PublicationResidueCoordination::Unselectable,
        coordination_access_policy: AccessPolicy::Unclassified,
        cleanup: CleanupArtifacts::new(),
        coordination_cleanup: CoordinationCleanup::CleanupGuard,
        housekeeping,
        visible_housekeeping,
        handle: Some(PublicationResidueHandle { inner: handle }),
        cause: Some(cause),
    }
}

const fn merge_housekeeping(left: Housekeeping, right: Housekeeping) -> Housekeeping {
    if matches!(left, Housekeeping::Visible) || matches!(right, Housekeeping::Visible) {
        Housekeeping::Visible
    } else if matches!(left, Housekeeping::CrashReappearancePossible)
        || matches!(right, Housekeeping::CrashReappearancePossible)
    {
        Housekeeping::CrashReappearancePossible
    } else {
        Housekeeping::None
    }
}

#[derive(Clone, Copy)]
struct FinalCoordination {
    kind: PublicationResidueCoordination,
    access: AccessPolicy,
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: identity.encode(),
    }
}

fn require_coordination_available(
    destination: &Destination,
    attempt_id: [u8; 16],
    role: DirectoryRole,
    identity: Identity,
) -> Result<(), Problem> {
    crate::publication::gc_barrier::require_source_available(
        destination.directory(),
        attempt_id,
        1,
        ArtifactKind::OwnedCoordination,
        role,
        destination.coordination(),
        identity,
    )
}

const fn selection_conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}

const fn cleanup_conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::CleanupConflict, None, detail)
}

#[cfg(all(test, target_os = "linux"))]
#[path = "linux_tests.rs"]
mod retry_tests;
