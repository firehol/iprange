//! Exact completion or rollback of an interrupted `CreateLive`.

use std::path::Path;

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::database;
use crate::error::{Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, State, MAIN_LIFETIME_LOCK};
use crate::live_writer::{
    empty_meta, write_empty_main, CreateResult, CreationState, LocalBasename,
};
use crate::publication::{ArtifactKind, DirectoryRole};
use crate::validation::LocalFileIdentity;

use super::LiveTransitionResolutionMode;

enum Main {
    Absent,
    Exact {
        file: std::fs::File,
        identity: Identity,
    },
    Malformed {
        file: std::fs::File,
        identity: Identity,
    },
}

enum Coordination {
    Absent,
    Exact {
        sidecar: Sidecar,
        state: State,
    },
    Malformed {
        file: std::fs::File,
        identity: Identity,
    },
}

/// Resolve only the exact creation attempt identified by `supplied`.
pub fn resolve_create_live(
    path: impl AsRef<Path>,
    supplied: &CreateResult,
    mode: LiveTransitionResolutionMode,
    cancellation: &CancellationToken,
) -> Result<CreateResult> {
    let path = path.as_ref();
    require_supplied(path, supplied)?;
    cancellation.check()?;
    let main = observe_main(path, supplied, cancellation)?;
    let sidecar_path = crate::path::canonical_sidecar(path)?;
    let coordination = observe_coordination(&sidecar_path, supplied)?;
    if let Some(terminal) = definitive(supplied, &main, &coordination)? {
        return Ok(terminal);
    }

    match mode {
        LiveTransitionResolutionMode::Complete => complete(path, supplied, main, coordination),
        LiveTransitionResolutionMode::Rollback => rollback(path, supplied, main, coordination),
    }
}

fn definitive(
    supplied: &CreateResult,
    main: &Main,
    coordination: &Coordination,
) -> Result<Option<CreateResult>> {
    if matches!(
        (main, coordination),
        (
            Main::Exact { .. },
            Coordination::Exact {
                state: State::Ready,
                ..
            }
        )
    ) {
        return Ok(Some(created(
            supplied,
            main_identity(main).expect("exact main identity"),
            coordination_identity(coordination).expect("exact sidecar identity"),
        )));
    }
    match supplied.state {
        CreationState::Created => Err(Error::Conflict(
            "a completed creation result no longer names a ready pair",
        )),
        CreationState::NotCreated if !supplied.residue_possible => {
            if matches!((main, coordination), (Main::Absent, Coordination::Absent)) {
                Ok(Some(not_created(
                    supplied,
                    supplied.main_identity,
                    supplied.sidecar_identity,
                )))
            } else {
                Err(Error::Conflict(
                    "a clean not-created result has unexpected artifacts",
                ))
            }
        }
        CreationState::NotCreated | CreationState::OutcomeUnknown => Ok(None),
    }
}

fn complete(
    path: &Path,
    supplied: &CreateResult,
    main: Main,
    coordination: Coordination,
) -> Result<CreateResult> {
    if matches!(main, Main::Malformed { .. }) {
        return Err(Error::Unresolvable(
            "creation main exists but is not the exact empty generation",
        ));
    }
    if matches!(coordination, Coordination::Malformed { .. }) {
        return Err(Error::Unresolvable(
            "creation sidecar exists but its header is malformed",
        ));
    }

    let (sidecar, state) = match coordination {
        Coordination::Exact { sidecar, state } => (sidecar, state),
        Coordination::Absent => {
            let sidecar = match Sidecar::reserve(
                path,
                supplied.database_id,
                supplied.sidecar_id,
                supplied.reader_capacity,
            ) {
                Ok(sidecar) => sidecar,
                Err(failure) => {
                    let sidecar_identity = failure.identity.map(live_sidecar::public_identity);
                    return Ok(unknown_after_private_failure(
                        supplied,
                        main_identity(&main),
                        sidecar_identity,
                        failure,
                    ));
                }
            };
            let identity = live_sidecar::public_identity(sidecar.local_identity());
            if let Err(cause) = sidecar
                .initialize_creating()
                .and_then(|()| live_sidecar::sync_parent(&sidecar.path))
            {
                return Ok(unknown(
                    supplied,
                    main_identity(&main),
                    Some(identity),
                    cause,
                ));
            }
            (sidecar, State::Creating)
        }
        Coordination::Malformed { .. } => unreachable!("checked above"),
    };
    let sidecar_identity = live_sidecar::public_identity(sidecar.local_identity());

    let (main_file, main_identity) = match main {
        Main::Exact { file, identity } => (file, live_sidecar::public_identity(identity)),
        Main::Absent => {
            let created = match live_sidecar::create_private(
                path,
                CleanupAuthority {
                    attempt_id: supplied.database_id,
                    ordinal: 0,
                    kind: ArtifactKind::OwnedMain,
                    directory_role: DirectoryRole::MainFile,
                },
            ) {
                Ok(created) => created,
                Err(failure) => {
                    let main_identity = failure.identity.map(live_sidecar::public_identity);
                    return Ok(unknown_after_private_failure(
                        supplied,
                        main_identity,
                        Some(sidecar_identity),
                        failure,
                    ));
                }
            };
            let public = live_sidecar::public_identity(created.identity);
            let meta = expected_meta(supplied);
            if let Err(cause) =
                write_empty_main(&created.file, meta).and_then(|()| live_sidecar::sync_parent(path))
            {
                return Ok(unknown(
                    supplied,
                    Some(public),
                    Some(sidecar_identity),
                    cause,
                ));
            }
            (created.file, public)
        }
        Main::Malformed { .. } => unreachable!("checked above"),
    };

    let finished = main_file
        .sync_all()
        .map_err(Error::from)
        .and_then(|()| live_sidecar::sync_parent(path))
        .and_then(|()| {
            if state == State::Creating {
                sidecar.publish_ready()
            } else {
                Ok(())
            }
        })
        .and_then(|()| live_sidecar::sync_parent(&sidecar.path))
        .and_then(|()| verify_created(path, &main_file, &sidecar, supplied));
    match finished {
        Ok(()) => Ok(created(supplied, main_identity, sidecar_identity)),
        Err(cause) => Ok(unknown(
            supplied,
            Some(main_identity),
            Some(sidecar_identity),
            cause,
        )),
    }
}

fn rollback(
    path: &Path,
    supplied: &CreateResult,
    main: Main,
    coordination: Coordination,
) -> Result<CreateResult> {
    let main_identity = main_identity(&main);
    let sidecar_identity = coordination_identity(&coordination);
    let mut cleanup = live_cleanup::Outcome::clean();
    if let Some((file, identity)) = raw_main(&main) {
        cleanup.absorb(live_cleanup::remove(
            path,
            file,
            identity,
            CleanupAuthority {
                attempt_id: supplied.database_id,
                ordinal: 0,
                kind: ArtifactKind::OwnedMain,
                directory_role: DirectoryRole::MainFile,
            },
        ));
        if !cleanup.is_clean() {
            return Ok(cleanup_result(
                supplied,
                main_identity,
                sidecar_identity,
                cleanup,
            ));
        }
    }
    if let Some((file, identity)) = raw_coordination(&coordination) {
        let sidecar_path = crate::path::canonical_sidecar(path)?;
        cleanup.absorb(live_cleanup::remove(
            &sidecar_path,
            file,
            identity,
            CleanupAuthority {
                attempt_id: supplied.sidecar_id,
                ordinal: 1,
                kind: ArtifactKind::OwnedCoordination,
                directory_role: DirectoryRole::MainFile,
            },
        ));
    }
    Ok(cleanup_result(
        supplied,
        main_identity,
        sidecar_identity,
        cleanup,
    ))
}

fn observe_main(
    path: &Path,
    supplied: &CreateResult,
    cancellation: &CancellationToken,
) -> Result<Main> {
    if live_sidecar::path_identity(path)?.is_none() {
        return Ok(Main::Absent);
    }
    let file = live_sidecar::open_rw(path)?;
    let identity = live_sidecar::identity(&file)?;
    live_cleanup::require_main_available(path, identity, supplied.database_id)?;
    let public = live_sidecar::public_identity(identity);
    if supplied
        .main_identity
        .is_some_and(|expected| expected != public)
    {
        return Err(Error::Conflict("creation main identity changed"));
    }
    live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Exclusive, cancellation)?;
    live_sidecar::verify_path(path, identity)?;
    match database::bootstrap_file(&file, OpenMode::Writer) {
        Ok(opened)
            if opened.physical_bytes == opened.committed_bytes
                && opened.meta == expected_meta(supplied) =>
        {
            Ok(Main::Exact { file, identity })
        }
        Ok(_) => Err(Error::Conflict(
            "creation path contains another valid database",
        )),
        Err(Error::Format(_) | Error::Corrupt(_)) if supplied.main_identity == Some(public) => {
            Ok(Main::Malformed { file, identity })
        }
        Err(Error::Format(_) | Error::Corrupt(_)) => Err(Error::Conflict(
            "malformed main cannot be attributed to this creation",
        )),
        Err(cause) => Err(cause),
    }
}

fn observe_coordination(path: &Path, supplied: &CreateResult) -> Result<Coordination> {
    let Some(identity) = super::transition::existing_identity(path)? else {
        return Ok(Coordination::Absent);
    };
    let public = live_sidecar::public_identity(identity);
    live_cleanup::require_available(
        path,
        identity,
        CleanupAuthority {
            attempt_id: supplied.sidecar_id,
            ordinal: 1,
            kind: ArtifactKind::OwnedCoordination,
            directory_role: DirectoryRole::MainFile,
        },
    )?;
    if supplied
        .sidecar_identity
        .is_some_and(|expected| expected != public)
    {
        return Err(Error::Conflict("creation sidecar identity changed"));
    }
    match Sidecar::open_at(path.to_path_buf(), supplied.database_id) {
        Ok((sidecar, state))
            if sidecar.header.sidecar_id == supplied.sidecar_id
                && sidecar.header.capacity == supplied.reader_capacity =>
        {
            Ok(Coordination::Exact { sidecar, state })
        }
        Ok(_) => Err(Error::Conflict(
            "canonical sidecar belongs to another creation",
        )),
        Err(Error::Format(_) | Error::Corrupt(_) | Error::WrongState(_))
            if supplied.sidecar_identity == Some(public) =>
        {
            let file = live_sidecar::open_rw(path)?;
            let reopened = live_sidecar::identity(&file)?;
            if reopened != identity {
                return Err(Error::Conflict(
                    "creation sidecar changed while it was reopened",
                ));
            }
            live_sidecar::verify_path(path, identity)?;
            Ok(Coordination::Malformed { file, identity })
        }
        Err(Error::Format(_) | Error::Corrupt(_) | Error::WrongState(_)) => Err(Error::Conflict(
            "malformed sidecar cannot be attributed to this creation",
        )),
        Err(cause) => Err(cause),
    }
}

fn verify_created(
    path: &Path,
    main: &std::fs::File,
    sidecar: &Sidecar,
    supplied: &CreateResult,
) -> Result<()> {
    let main_identity = live_sidecar::identity(main)?;
    live_sidecar::verify_path(path, main_identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()?;
    let opened = database::bootstrap_file(main, OpenMode::Writer)?;
    if opened.physical_bytes != opened.committed_bytes || opened.meta != expected_meta(supplied) {
        return Err(Error::Conflict("created main changed during resolution"));
    }
    Ok(())
}

fn require_supplied(path: &Path, supplied: &CreateResult) -> Result<()> {
    if supplied.database_id == [0; 16]
        || supplied.commit_nonce == [0; 16]
        || supplied.sidecar_id == [0; 16]
        || supplied.reader_capacity == 0
    {
        return Err(Error::InvalidArgument("creation result is incomplete"));
    }
    if LocalBasename::from_path(path)? != supplied.main_basename {
        return Err(Error::Conflict("creation destination name changed"));
    }
    let expected_directory = supplied.directory_identity.ok_or(Error::Unresolvable(
        "creation never proved its parent directory identity",
    ))?;
    if live_sidecar::parent_identity(path)? != expected_directory {
        return Err(Error::DirectoryIdentityMismatch);
    }
    Ok(())
}

fn expected_meta(supplied: &CreateResult) -> crate::contract::MetaV4 {
    empty_meta(
        supplied.address_family,
        supplied.value_kind,
        supplied.value_tag,
        supplied.database_id,
        supplied.commit_nonce,
    )
}

fn raw_main_identity(main: &Main) -> Option<Identity> {
    match main {
        Main::Absent => None,
        Main::Exact { identity, .. } | Main::Malformed { identity, .. } => Some(*identity),
    }
}

fn raw_main(main: &Main) -> Option<(&std::fs::File, Identity)> {
    match main {
        Main::Absent => None,
        Main::Exact { file, identity } | Main::Malformed { file, identity } => {
            Some((file, *identity))
        }
    }
}

fn main_identity(main: &Main) -> Option<LocalFileIdentity> {
    raw_main_identity(main).map(live_sidecar::public_identity)
}

fn raw_coordination_identity(coordination: &Coordination) -> Option<Identity> {
    match coordination {
        Coordination::Absent => None,
        Coordination::Exact { sidecar, .. } => Some(sidecar.local_identity()),
        Coordination::Malformed { identity, .. } => Some(*identity),
    }
}

fn raw_coordination(coordination: &Coordination) -> Option<(&std::fs::File, Identity)> {
    match coordination {
        Coordination::Absent => None,
        Coordination::Exact { sidecar, .. } => Some((&sidecar.file, sidecar.local_identity())),
        Coordination::Malformed { file, identity } => Some((file, *identity)),
    }
}

fn coordination_identity(coordination: &Coordination) -> Option<LocalFileIdentity> {
    raw_coordination_identity(coordination).map(live_sidecar::public_identity)
}

fn created(
    supplied: &CreateResult,
    main_identity: LocalFileIdentity,
    sidecar_identity: LocalFileIdentity,
) -> CreateResult {
    result(
        supplied,
        CreationState::Created,
        Some(main_identity),
        Some(sidecar_identity),
        false,
        None,
    )
}

fn not_created(
    supplied: &CreateResult,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
) -> CreateResult {
    result(
        supplied,
        CreationState::NotCreated,
        main_identity,
        sidecar_identity,
        false,
        None,
    )
}

fn unknown(
    supplied: &CreateResult,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    cause: Error,
) -> CreateResult {
    result(
        supplied,
        CreationState::OutcomeUnknown,
        main_identity,
        sidecar_identity,
        true,
        Some(cause),
    )
}

fn unknown_after_private_failure(
    supplied: &CreateResult,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    failure: live_sidecar::PrivateCreationFailure,
) -> CreateResult {
    let housekeeping = merge_housekeeping(supplied.housekeeping, failure.cleanup.housekeeping);
    let mut visible = supplied.visible_housekeeping.clone().into_vec();
    visible.extend(failure.cleanup.visible);
    let cause = match failure.cleanup.cause {
        None => failure.cause,
        Some(cleanup) => Error::CleanupIncomplete {
            cause: Box::new(failure.cause),
            cleanup: Box::new(cleanup),
        },
    };
    result_with_housekeeping(
        supplied,
        CreationState::OutcomeUnknown,
        main_identity,
        sidecar_identity,
        true,
        housekeeping,
        visible.into_boxed_slice(),
        Some(cause),
    )
}

fn cleanup_result(
    supplied: &CreateResult,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    cleanup: live_cleanup::Outcome,
) -> CreateResult {
    let state = if cleanup.is_clean() {
        CreationState::NotCreated
    } else {
        CreationState::OutcomeUnknown
    };
    let residue_possible = !cleanup.is_clean();
    let housekeeping = merge_housekeeping(supplied.housekeeping, cleanup.housekeeping);
    let mut visible = supplied.visible_housekeeping.clone().into_vec();
    visible.extend(cleanup.visible);
    result_with_housekeeping(
        supplied,
        state,
        main_identity,
        sidecar_identity,
        residue_possible,
        housekeeping,
        visible.into_boxed_slice(),
        cleanup.cause,
    )
}

fn result(
    supplied: &CreateResult,
    state: CreationState,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    residue_possible: bool,
    cause: Option<Error>,
) -> CreateResult {
    result_with_housekeeping(
        supplied,
        state,
        main_identity,
        sidecar_identity,
        residue_possible,
        supplied.housekeeping,
        supplied.visible_housekeeping.clone(),
        cause,
    )
}

#[allow(clippy::too_many_arguments)]
fn result_with_housekeeping(
    supplied: &CreateResult,
    state: CreationState,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    residue_possible: bool,
    housekeeping: crate::publication::Housekeeping,
    visible_housekeeping: Box<[crate::publication::HousekeepingArtifact]>,
    cause: Option<Error>,
) -> CreateResult {
    CreateResult {
        address_family: supplied.address_family,
        value_kind: supplied.value_kind,
        value_tag: supplied.value_tag,
        database_id: supplied.database_id,
        commit_nonce: supplied.commit_nonce,
        sidecar_id: supplied.sidecar_id,
        directory_identity: supplied.directory_identity,
        main_basename: supplied.main_basename,
        main_identity,
        sidecar_identity,
        reader_capacity: supplied.reader_capacity,
        state,
        residue_possible,
        housekeeping,
        visible_housekeeping,
        cause,
    }
}

const fn merge_housekeeping(
    left: crate::publication::Housekeeping,
    right: crate::publication::Housekeeping,
) -> crate::publication::Housekeeping {
    use crate::publication::Housekeeping;

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

#[cfg(test)]
#[path = "create_resolution_tests.rs"]
mod tests;
