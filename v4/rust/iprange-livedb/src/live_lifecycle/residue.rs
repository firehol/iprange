//! Restart recovery for live transitions whose in-memory result was lost.

use std::path::{Path, PathBuf};

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::live_lock::Mode;
use crate::live_sidecar::{self, Identity, Sidecar, State};
use crate::publication::{ArtifactKind, DirectoryRole, Housekeeping, HousekeepingArtifact};
use crate::validation::LocalFileIdentity;

use super::transition::LockedMain;
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
    OutcomeUnknown,
}

/// Facts recovered directly from the retained main and sidecar.
#[derive(Debug)]
pub struct LiveResidueResult {
    pub status: LiveResidueStatus,
    pub kind: Option<LiveResidueKind>,
    pub database_id: Option<[u8; 16]>,
    pub sidecar_id: Option<[u8; 16]>,
    pub reader_capacity: Option<u32>,
    pub main_identity: Option<LocalFileIdentity>,
    pub sidecar_identity: Option<LocalFileIdentity>,
    pub residue_possible: bool,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<Error>,
}

enum Observed {
    Absent,
    Valid(Sidecar, State),
    Malformed {
        file: std::fs::File,
        identity: Identity,
    },
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
    let mut cleanup = retire_observed(&residue_path, &residue);
    match live_sidecar::parent_identity(path) {
        Ok(found) if found == directory_identity => {}
        Ok(_) => cleanup.absorb(live_cleanup::Outcome::failed(
            Error::DirectoryIdentityMismatch,
        )),
        Err(cause) => cleanup.absorb(live_cleanup::Outcome::failed(cause)),
    }
    Ok(after_removal(facts, cleanup))
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
            let cleanup = remove_private_residue(&main, &private_path, &private, cancellation)?;
            Ok(with_cleanup(
                completed_result(&main, LiveResidueKind::PrivateReset, &canonical),
                cleanup,
                true,
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
        (canonical, residue @ Observed::Malformed { .. })
            if mode == LiveTransitionResolutionMode::Rollback =>
        {
            let _ = canonical;
            cancellation.check()?;
            main.verify()?;
            let facts = facts(
                LiveResidueKind::PrivateReset,
                Some(main.public_identity),
                &residue,
            );
            let mut cleanup = retire_observed(&private_path, &residue);
            if let Err(cause) = main.verify() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            Ok(after_removal(facts, cleanup))
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
    let residue = Observed::Valid(sidecar, state);
    let Observed::Valid(sidecar, _) = &residue else {
        unreachable!("constructed valid residue")
    };
    sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
    let prepared = (|| {
        cancellation.check()?;
        main.verify()?;
        require_state(sidecar, state)
    })();
    prepared?;
    let facts = facts(
        LiveResidueKind::PrivateReset,
        Some(main.public_identity),
        &residue,
    );
    let mut cleanup = retire_observed(path, &residue);
    if let Err(cause) = main.verify() {
        cleanup.absorb(live_cleanup::Outcome::failed(cause));
    }
    let unlocked = sidecar.unlock_gate();
    if let Err(cause) = unlocked {
        cleanup.absorb(live_cleanup::Outcome::failed(cause));
    }
    Ok(after_removal(facts, cleanup))
}

fn remove_private_residue(
    main: &LockedMain,
    path: &Path,
    residue: &Observed,
    cancellation: &CancellationToken,
) -> Result<live_cleanup::Outcome> {
    match residue {
        Observed::Absent => Ok(live_cleanup::Outcome::clean()),
        Observed::Valid(sidecar, state) => {
            sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
            let prepared = (|| {
                cancellation.check()?;
                main.verify()?;
                require_state(sidecar, *state)
            })();
            prepared?;
            let mut cleanup = retire_observed(path, residue);
            if let Err(cause) = main.verify() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            if let Err(cause) = sidecar.unlock_gate() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            Ok(cleanup)
        }
        Observed::Malformed { .. } => {
            cancellation.check()?;
            main.verify()?;
            let mut cleanup = retire_observed(path, residue);
            if let Err(cause) = main.verify() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            Ok(cleanup)
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
            let identity = live_sidecar::identity(&file)?;
            Ok(Observed::Malformed { file, identity })
        }
        Err(cause) => Err(cause),
    }
}

fn retire_observed(path: &Path, residue: &Observed) -> live_cleanup::Outcome {
    let (file, identity, attempt_id) = match residue {
        Observed::Absent => return live_cleanup::Outcome::clean(),
        Observed::Valid(sidecar, _) => (
            &sidecar.file,
            sidecar.local_identity(),
            Ok(sidecar.header.sidecar_id),
        ),
        Observed::Malformed { file, identity } => (
            file,
            *identity,
            live_cleanup::fresh_cleanup_attempt(
                path,
                *identity,
                1,
                ArtifactKind::OwnedCoordination,
                DirectoryRole::MainFile,
            ),
        ),
    };
    match attempt_id {
        Ok(attempt_id) => live_cleanup::remove(
            path,
            file,
            identity,
            CleanupAuthority {
                attempt_id,
                ordinal: 1,
                kind: ArtifactKind::OwnedCoordination,
                directory_role: DirectoryRole::MainFile,
            },
        ),
        Err(cause) => live_cleanup::Outcome::failed(cause),
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
        residue_possible: false,
        housekeeping: Housekeeping::None,
        visible_housekeeping: Box::default(),
        cause: None,
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
        residue_possible: false,
        housekeeping: Housekeeping::None,
        visible_housekeeping: Box::default(),
        cause: None,
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
        residue_possible: false,
        housekeeping: Housekeeping::None,
        visible_housekeeping: Box::default(),
        cause: None,
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
        Observed::Malformed { identity, .. } => (
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
        residue_possible: false,
        housekeeping: Housekeeping::None,
        visible_housekeeping: Box::default(),
        cause: None,
    }
}

fn after_removal(result: LiveResidueResult, cleanup: live_cleanup::Outcome) -> LiveResidueResult {
    with_cleanup(result, cleanup, false)
}

fn with_cleanup(
    mut result: LiveResidueResult,
    cleanup: live_cleanup::Outcome,
    preserve_status: bool,
) -> LiveResidueResult {
    let clean = cleanup.is_clean();
    if clean {
        if !preserve_status {
            result.status = LiveResidueStatus::Removed;
        }
    } else {
        result.residue_possible = true;
        if !preserve_status {
            result.status = LiveResidueStatus::OutcomeUnknown;
        }
    }
    result.housekeeping = merge_housekeeping(result.housekeeping, cleanup.housekeeping);
    let mut visible = result.visible_housekeeping.into_vec();
    visible.extend(cleanup.visible);
    result.visible_housekeeping = visible.into_boxed_slice();
    result.cause = cleanup.cause;
    result
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
