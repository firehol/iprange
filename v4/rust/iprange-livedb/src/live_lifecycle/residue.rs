//! Restart recovery for live transitions whose in-memory result was lost.

use std::path::{Path, PathBuf};

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_lock::Mode;
use crate::live_sidecar::{self, Identity, Sidecar, State};
use crate::validation::LocalFileIdentity;

use super::transition::{remove_exact, LockedMain};
use super::{LiveResetPolicy, LiveTransitionResolutionMode};

/// Location of an interrupted live-coordination artifact.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveResidueKind {
    Canonical,
    PrivateReset,
}

/// Factual terminal state of resultless transition recovery.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveResidueStatus {
    Absent,
    Ready,
    Completed,
    Removed,
}

/// Facts recovered directly from the retained main and sidecar.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LiveResidueResult {
    pub status: LiveResidueStatus,
    pub kind: Option<LiveResidueKind>,
    pub database_id: Option<[u8; 16]>,
    pub sidecar_id: Option<[u8; 16]>,
    pub reader_capacity: Option<u32>,
    pub main_identity: Option<LocalFileIdentity>,
    pub sidecar_identity: Option<LocalFileIdentity>,
}

enum Observed {
    Absent,
    Valid(Sidecar, State),
    Malformed { identity: Identity },
}

struct Residues {
    canonical_path: PathBuf,
    canonical: Observed,
    private_path: PathBuf,
    private: Observed,
}

/// Resolve one interrupted canonical create/initialize or private reset.
///
/// This operation performs only bootstrap and coordination checks. It never
/// walks or validates the database page graph.
pub fn resolve_interrupted_live_transition(
    path: impl AsRef<Path>,
    mode: LiveTransitionResolutionMode,
    cancellation: &CancellationToken,
) -> Result<LiveResidueResult> {
    let path = path.as_ref();
    cancellation.check()?;
    let directory_identity = live_sidecar::parent_identity(path)?;
    let main = open_main(path, cancellation)?;
    let canonical_path = crate::path::canonical_sidecar(path)?;
    let private_path = crate::path::live_transition_temp(path)?;
    let residues = Residues {
        canonical: observe(&canonical_path)?,
        private: observe(&private_path)?,
        canonical_path,
        private_path,
    };

    match main {
        None => resolve_without_main(path, directory_identity, residues, mode, cancellation),
        Some(main) => resolve_with_main(main, residues, mode, cancellation),
    }
}

fn resolve_without_main(
    path: &Path,
    directory_identity: LocalFileIdentity,
    residues: Residues,
    mode: LiveTransitionResolutionMode,
    cancellation: &CancellationToken,
) -> Result<LiveResidueResult> {
    let Residues {
        canonical_path,
        canonical,
        private_path,
        private,
    } = residues;
    if !matches!(canonical, Observed::Absent) && !matches!(private, Observed::Absent) {
        return Err(Error::Conflict(
            "canonical and private live residues both exist without a main",
        ));
    }
    let (kind, residue_path, residue) = match (canonical, private) {
        (Observed::Absent, Observed::Absent) => return Ok(absent()),
        (residue, Observed::Absent) => (LiveResidueKind::Canonical, canonical_path, residue),
        (Observed::Absent, residue) => (LiveResidueKind::PrivateReset, private_path, residue),
        _ => unreachable!("both-present case rejected"),
    };
    if mode == LiveTransitionResolutionMode::Complete {
        return Err(Error::Unresolvable(
            "a live coordination residue has no main to complete",
        ));
    }

    cancellation.check()?;
    if live_sidecar::parent_identity(path)? != directory_identity {
        return Err(Error::DirectoryIdentityMismatch);
    }
    let facts = facts(kind, None, &residue);
    remove_observed(&residue_path, residue)?;
    if live_sidecar::parent_identity(path)? != directory_identity {
        return Err(Error::DirectoryIdentityMismatch);
    }
    Ok(LiveResidueResult {
        status: LiveResidueStatus::Removed,
        ..facts
    })
}

fn resolve_with_main(
    main: LockedMain,
    residues: Residues,
    mode: LiveTransitionResolutionMode,
    cancellation: &CancellationToken,
) -> Result<LiveResidueResult> {
    let Residues {
        canonical_path,
        canonical,
        private_path,
        private,
    } = residues;
    match (canonical, private) {
        (Observed::Absent, Observed::Absent) => Ok(absent_with_main(&main)),
        (Observed::Valid(sidecar, State::Ready), Observed::Absent) => {
            require_database(&main, &sidecar)?;
            main.verify()?;
            Ok(ready(&main, LiveResidueKind::Canonical, &sidecar))
        }
        (Observed::Valid(canonical, State::Ready), private) => {
            require_database(&main, &canonical)?;
            remove_private_residue(&main, &private_path, private, cancellation)?;
            Ok(completed_result(
                &main,
                LiveResidueKind::PrivateReset,
                &canonical,
            ))
        }
        (Observed::Valid(sidecar, State::Creating), Observed::Absent) => {
            require_database(&main, &sidecar)?;
            if mode == LiveTransitionResolutionMode::Rollback {
                return Err(Error::Conflict(
                    "resultless rollback cannot prove ownership of the valid main",
                ));
            }
            complete_canonical(&main, &sidecar, cancellation)
        }
        (Observed::Absent, Observed::Valid(sidecar, state)) => {
            require_database(&main, &sidecar)?;
            match mode {
                LiveTransitionResolutionMode::Complete if state == State::Ready => {
                    complete_private_reset(&main, &canonical_path, &private_path, sidecar)
                }
                LiveTransitionResolutionMode::Complete => {
                    Err(Error::Conflict("private reset sidecar is not ready"))
                }
                LiveTransitionResolutionMode::Rollback => {
                    remove_valid_private(&main, &private_path, sidecar, state, cancellation)
                }
            }
        }
        (canonical, Observed::Valid(sidecar, state))
            if mode == LiveTransitionResolutionMode::Rollback =>
        {
            require_database(&main, &sidecar)?;
            let _ = canonical;
            remove_valid_private(&main, &private_path, sidecar, state, cancellation)
        }
        (canonical, Observed::Malformed { identity })
            if mode == LiveTransitionResolutionMode::Rollback =>
        {
            let _ = canonical;
            cancellation.check()?;
            main.verify()?;
            remove_exact(&private_path, identity)?;
            main.verify()?;
            Ok(LiveResidueResult {
                status: LiveResidueStatus::Removed,
                kind: Some(LiveResidueKind::PrivateReset),
                database_id: Some(main.bootstrap.meta.database_id),
                sidecar_id: None,
                reader_capacity: None,
                main_identity: Some(main.public_identity),
                sidecar_identity: Some(live_sidecar::public_identity(identity)),
            })
        }
        (_, Observed::Valid(_, State::Creating)) => {
            Err(Error::Conflict("private reset sidecar is not ready"))
        }
        (_, Observed::Malformed { .. }) => {
            Err(Error::Unresolvable("private reset sidecar is malformed"))
        }
        (Observed::Malformed { .. }, Observed::Absent) => Err(Error::Unresolvable(
            "canonical live coordination is malformed; explicit reset is required",
        )),
        (_, _) => Err(Error::Conflict(
            "live transition residue conflicts with canonical coordination",
        )),
    }
}

fn complete_canonical(
    main: &LockedMain,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<LiveResidueResult> {
    sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
    let completed = (|| {
        cancellation.check()?;
        main.verify()?;
        require_state(sidecar, State::Creating)?;
        main.file.sync_all()?;
        live_sidecar::sync_parent(&main.path)?;
        sidecar.publish_ready()?;
        live_sidecar::sync_parent(&sidecar.path)?;
        main.verify()?;
        sidecar.verify_path()?;
        sidecar.verify_header()
    })();
    let unlocked = sidecar.unlock_gate();
    completed?;
    unlocked?;
    Ok(completed_result(main, LiveResidueKind::Canonical, sidecar))
}

fn complete_private_reset(
    main: &LockedMain,
    canonical_path: &Path,
    private_path: &Path,
    sidecar: Sidecar,
) -> Result<LiveResidueResult> {
    sidecar.lock_gate(Mode::Exclusive)?;
    let completed = (|| {
        main.verify()?;
        require_absent(canonical_path)?;
        require_state(&sidecar, State::Ready)?;
        super::namespace::install(
            private_path,
            &sidecar.file,
            canonical_path,
            sidecar.local_identity(),
            None,
            LiveResetPolicy::RollbackSafe,
        )?;
        live_sidecar::sync_parent(canonical_path)?;
        main.verify()?;
        live_sidecar::verify_path(canonical_path, sidecar.local_identity())?;
        sidecar.verify_header()
    })();
    let unlocked = sidecar.unlock_gate();
    completed?;
    unlocked?;
    Ok(completed_result(
        main,
        LiveResidueKind::PrivateReset,
        &sidecar,
    ))
}

fn remove_valid_private(
    main: &LockedMain,
    path: &Path,
    sidecar: Sidecar,
    state: State,
    cancellation: &CancellationToken,
) -> Result<LiveResidueResult> {
    sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
    let removed = (|| {
        cancellation.check()?;
        main.verify()?;
        require_state(&sidecar, state)?;
        remove_exact(path, sidecar.local_identity())?;
        main.verify()
    })();
    let unlocked = sidecar.unlock_gate();
    removed?;
    unlocked?;
    Ok(LiveResidueResult {
        status: LiveResidueStatus::Removed,
        ..facts(
            LiveResidueKind::PrivateReset,
            Some(main.public_identity),
            &Observed::Valid(sidecar, state),
        )
    })
}

fn remove_private_residue(
    main: &LockedMain,
    path: &Path,
    residue: Observed,
    cancellation: &CancellationToken,
) -> Result<()> {
    match residue {
        Observed::Absent => Ok(()),
        Observed::Valid(sidecar, state) => {
            sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
            let removed = (|| {
                cancellation.check()?;
                main.verify()?;
                require_state(&sidecar, state)?;
                remove_exact(path, sidecar.local_identity())?;
                main.verify()
            })();
            let unlocked = sidecar.unlock_gate();
            removed?;
            unlocked
        }
        Observed::Malformed { identity } => {
            cancellation.check()?;
            main.verify()?;
            remove_exact(path, identity)?;
            main.verify()
        }
    }
}

fn open_main(path: &Path, cancellation: &CancellationToken) -> Result<Option<LockedMain>> {
    if live_sidecar::path_identity(path)?.is_none() {
        return Ok(None);
    }
    LockedMain::open(path, cancellation).map(Some)
}

fn observe(path: &Path) -> Result<Observed> {
    if live_sidecar::path_identity(path)?.is_none() {
        return Ok(Observed::Absent);
    }
    match Sidecar::open_any(path.to_path_buf()) {
        Ok((sidecar, state)) => Ok(Observed::Valid(sidecar, state)),
        Err(Error::Format(_) | Error::Corrupt(_) | Error::WrongState(_)) => {
            let file = live_sidecar::open_rw(path)?;
            Ok(Observed::Malformed {
                identity: live_sidecar::identity(&file)?,
            })
        }
        Err(cause) => Err(cause),
    }
}

fn remove_observed(path: &Path, residue: Observed) -> Result<()> {
    match residue {
        Observed::Absent => Ok(()),
        Observed::Valid(sidecar, _) => remove_exact(path, sidecar.local_identity()),
        Observed::Malformed { identity } => remove_exact(path, identity),
    }
}

fn require_database(main: &LockedMain, sidecar: &Sidecar) -> Result<()> {
    if sidecar.header.database_id != main.bootstrap.meta.database_id {
        return Err(Error::Conflict(
            "live residue belongs to a different database",
        ));
    }
    Ok(())
}

fn require_state(sidecar: &Sidecar, state: State) -> Result<()> {
    let (current, header) = live_sidecar::read_header(&sidecar.file)?;
    if current != state || header != sidecar.header {
        return Err(Error::Conflict("live residue changed during resolution"));
    }
    sidecar.verify_path()
}

fn require_absent(path: &Path) -> Result<()> {
    match live_sidecar::path_identity(path)? {
        None => Ok(()),
        Some(_) => Err(Error::Conflict(
            "canonical coordination appeared during resolution",
        )),
    }
}

fn absent() -> LiveResidueResult {
    LiveResidueResult {
        status: LiveResidueStatus::Absent,
        kind: None,
        database_id: None,
        sidecar_id: None,
        reader_capacity: None,
        main_identity: None,
        sidecar_identity: None,
    }
}

fn absent_with_main(main: &LockedMain) -> LiveResidueResult {
    LiveResidueResult {
        database_id: Some(main.bootstrap.meta.database_id),
        main_identity: Some(main.public_identity),
        ..absent()
    }
}

fn ready(main: &LockedMain, kind: LiveResidueKind, sidecar: &Sidecar) -> LiveResidueResult {
    LiveResidueResult {
        status: LiveResidueStatus::Ready,
        kind: Some(kind),
        database_id: Some(sidecar.header.database_id),
        sidecar_id: Some(sidecar.header.sidecar_id),
        reader_capacity: Some(sidecar.header.capacity),
        main_identity: Some(main.public_identity),
        sidecar_identity: Some(live_sidecar::public_identity(sidecar.local_identity())),
    }
}

fn completed_result(
    main: &LockedMain,
    kind: LiveResidueKind,
    sidecar: &Sidecar,
) -> LiveResidueResult {
    LiveResidueResult {
        status: LiveResidueStatus::Completed,
        kind: Some(kind),
        database_id: Some(sidecar.header.database_id),
        sidecar_id: Some(sidecar.header.sidecar_id),
        reader_capacity: Some(sidecar.header.capacity),
        main_identity: Some(main.public_identity),
        sidecar_identity: Some(live_sidecar::public_identity(sidecar.local_identity())),
    }
}

fn facts(
    kind: LiveResidueKind,
    main_identity: Option<LocalFileIdentity>,
    residue: &Observed,
) -> LiveResidueResult {
    let (database_id, sidecar_id, reader_capacity, sidecar_identity) = match residue {
        Observed::Absent => (None, None, None, None),
        Observed::Valid(sidecar, _) => (
            Some(sidecar.header.database_id),
            Some(sidecar.header.sidecar_id),
            Some(sidecar.header.capacity),
            Some(live_sidecar::public_identity(sidecar.local_identity())),
        ),
        Observed::Malformed { identity } => (
            None,
            None,
            None,
            Some(live_sidecar::public_identity(*identity)),
        ),
    };
    LiveResidueResult {
        status: LiveResidueStatus::Absent,
        kind: Some(kind),
        database_id,
        sidecar_id,
        reader_capacity,
        main_identity,
        sidecar_identity,
    }
}
