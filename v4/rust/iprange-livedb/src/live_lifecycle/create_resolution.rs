//! Exact completion or rollback of an interrupted `CreateLive`.

use std::path::Path;

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::database;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, State, MAIN_LIFETIME_LOCK};
use crate::live_writer::{
    empty_meta, write_empty_main, CreateResult, CreationState, LocalBasename,
};
use crate::validation::LocalFileIdentity;

use super::LiveTransitionResolutionMode;

enum Main {
    Absent,
    Exact {
        file: std::fs::File,
        identity: Identity,
    },
    Malformed {
        identity: Identity,
    },
}

enum Coordination {
    Absent,
    Exact { sidecar: Sidecar, state: State },
    Malformed { identity: Identity },
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
                Err(cause) => return Ok(unknown(supplied, main_identity(&main), None, cause)),
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
            let file = match live_sidecar::create_private(path) {
                Ok(file) => file,
                Err(cause) => return Ok(unknown(supplied, None, Some(sidecar_identity), cause)),
            };
            let identity = match live_sidecar::identity(&file) {
                Ok(identity) => identity,
                Err(cause) => return Ok(unknown(supplied, None, Some(sidecar_identity), cause)),
            };
            let public = live_sidecar::public_identity(identity);
            let meta = expected_meta(supplied);
            if let Err(cause) =
                write_empty_main(&file, meta).and_then(|()| live_sidecar::sync_parent(path))
            {
                return Ok(unknown(
                    supplied,
                    Some(public),
                    Some(sidecar_identity),
                    cause,
                ));
            }
            (file, public)
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

    if let Some(identity) = raw_main_identity(&main) {
        if let Err(cause) = remove_exact(path, identity) {
            return Ok(unknown(supplied, main_identity, sidecar_identity, cause));
        }
    }
    if let Some(identity) = raw_coordination_identity(&coordination) {
        let sidecar_path = crate::path::canonical_sidecar(path)?;
        if let Err(cause) = remove_exact(&sidecar_path, identity) {
            return Ok(unknown(supplied, main_identity, sidecar_identity, cause));
        }
    }
    Ok(not_created(supplied, main_identity, sidecar_identity))
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
            Ok(Main::Malformed { identity })
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
            Ok(Coordination::Malformed { identity })
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

fn remove_exact(path: &Path, identity: Identity) -> Result<()> {
    live_sidecar::remove_exact(path, identity)
}

fn raw_main_identity(main: &Main) -> Option<Identity> {
    match main {
        Main::Absent => None,
        Main::Exact { identity, .. } | Main::Malformed { identity } => Some(*identity),
    }
}

fn main_identity(main: &Main) -> Option<LocalFileIdentity> {
    raw_main_identity(main).map(live_sidecar::public_identity)
}

fn raw_coordination_identity(coordination: &Coordination) -> Option<Identity> {
    match coordination {
        Coordination::Absent => None,
        Coordination::Exact { sidecar, .. } => Some(sidecar.local_identity()),
        Coordination::Malformed { identity } => Some(*identity),
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

fn result(
    supplied: &CreateResult,
    state: CreationState,
    main_identity: Option<LocalFileIdentity>,
    sidecar_identity: Option<LocalFileIdentity>,
    residue_possible: bool,
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
        cause,
    }
}

#[cfg(test)]
#[path = "create_resolution_tests.rs"]
mod tests;
