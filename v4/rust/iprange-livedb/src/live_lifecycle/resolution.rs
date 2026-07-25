//! Exact completion or rollback of one interrupted live transition.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_sidecar::{self, Sidecar, State};
use crate::validation::LocalFileIdentity;

use super::transition::{existing_identity, remove_exact, LockedMain};
use super::{
    LiveCoordinationLocation, LiveTransitionOperation, LiveTransitionResult, LiveTransitionStatus,
};

/// Requested terminal action for an exact interrupted transition.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveTransitionResolutionMode {
    Complete,
    Rollback,
}

struct Observed {
    sidecar: Sidecar,
    state: State,
    identity: LocalFileIdentity,
}

enum ResetCanonical {
    Previous,
    Attempt(Observed),
}

enum ResetPrivate {
    Previous(live_sidecar::Identity),
    Attempt(Observed),
}

/// Resolve only the exact transition identified by `supplied`.
pub fn resolve_live_transition(
    path: impl AsRef<Path>,
    supplied: &LiveTransitionResult,
    mode: LiveTransitionResolutionMode,
    cancellation: &CancellationToken,
) -> Result<LiveTransitionResult> {
    require_supplied(supplied)?;
    let main = LockedMain::open(path.as_ref(), cancellation)?;
    require_main(&main, supplied)?;
    cancellation.check()?;

    let canonical_path = crate::path::canonical_sidecar(&main.path)?;
    let private_path = crate::path::live_transition_temp(&main.path)?;
    match supplied.operation {
        LiveTransitionOperation::Initialize => resolve_initialize(
            main,
            supplied,
            observe(&canonical_path, supplied.database_id)?,
            observe(&private_path, supplied.database_id)?,
            mode,
        ),
        LiveTransitionOperation::Reset => resolve_reset(
            main,
            supplied,
            canonical_path.clone(),
            observe_reset_canonical(&canonical_path, supplied)?,
            private_path.clone(),
            observe_reset_private(&private_path, supplied)?,
            mode,
        ),
    }
}

fn resolve_initialize(
    main: LockedMain,
    supplied: &LiveTransitionResult,
    canonical: Option<Observed>,
    private: Option<Observed>,
    mode: LiveTransitionResolutionMode,
) -> Result<LiveTransitionResult> {
    if private.is_some() {
        return Err(Error::Conflict(
            "initialize transition has an unexpected private sidecar",
        ));
    }
    let Some(canonical) = canonical else {
        return Ok(resolved(
            supplied,
            LiveTransitionStatus::Unchanged,
            None,
            LiveCoordinationLocation::Absent,
        ));
    };
    require_attempt(&canonical, supplied)?;
    match (canonical.state, mode) {
        (State::Ready, _) => Ok(resolved(
            supplied,
            LiveTransitionStatus::Initialized,
            Some(canonical.identity),
            LiveCoordinationLocation::Canonical,
        )),
        (State::Creating, LiveTransitionResolutionMode::Complete) => {
            main.verify()?;
            main.file.sync_all()?;
            live_sidecar::sync_parent(&canonical.sidecar.path)?;
            canonical.sidecar.publish_ready()?;
            live_sidecar::sync_parent(&canonical.sidecar.path)?;
            main.verify()?;
            Ok(resolved(
                supplied,
                LiveTransitionStatus::Initialized,
                Some(canonical.identity),
                LiveCoordinationLocation::Canonical,
            ))
        }
        (State::Creating, LiveTransitionResolutionMode::Rollback) => {
            remove_exact(&canonical.sidecar.path, canonical.sidecar.local_identity())?;
            main.verify()?;
            Ok(resolved(
                supplied,
                LiveTransitionStatus::Unchanged,
                Some(canonical.identity),
                LiveCoordinationLocation::Absent,
            ))
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn resolve_reset(
    main: LockedMain,
    supplied: &LiveTransitionResult,
    canonical_path: std::path::PathBuf,
    canonical: Option<ResetCanonical>,
    private_path: std::path::PathBuf,
    private: Option<ResetPrivate>,
    mode: LiveTransitionResolutionMode,
) -> Result<LiveTransitionResult> {
    if let Some(canonical) = canonical.as_ref() {
        if let ResetCanonical::Attempt(canonical) = canonical {
            require_attempt(canonical, supplied)?;
            if canonical.state != State::Ready {
                return Err(Error::Conflict("completed reset sidecar is not ready"));
            }
            match private {
                Some(ResetPrivate::Previous(identity)) => {
                    remove_exact(&private_path, identity)?;
                }
                Some(ResetPrivate::Attempt(_)) => {
                    return Err(Error::Conflict(
                        "the reset attempt exists at both private and canonical names",
                    ))
                }
                None => {}
            }
            live_sidecar::sync_parent(&canonical_path)?;
            main.verify()?;
            return Ok(resolved(
                supplied,
                LiveTransitionStatus::Initialized,
                Some(canonical.identity),
                LiveCoordinationLocation::Canonical,
            ));
        }
    } else if supplied.previous_sidecar_identity.is_some() {
        return Err(Error::Conflict(
            "the previous canonical sidecar disappeared",
        ));
    }

    let Some(private) = private else {
        return Ok(resolved(
            supplied,
            LiveTransitionStatus::Unchanged,
            supplied.new_sidecar_identity,
            LiveCoordinationLocation::Absent,
        ));
    };
    let ResetPrivate::Attempt(private) = private else {
        return Err(Error::Conflict(
            "the previous sidecar is private before reset installation",
        ));
    };
    require_attempt(&private, supplied)?;
    if private.state != State::Ready {
        return Err(Error::Conflict("reset private sidecar is not ready"));
    }

    match mode {
        LiveTransitionResolutionMode::Rollback => {
            remove_exact(&private_path, private.sidecar.local_identity())?;
            main.verify()?;
            Ok(resolved(
                supplied,
                LiveTransitionStatus::Unchanged,
                Some(private.identity),
                LiveCoordinationLocation::Absent,
            ))
        }
        LiveTransitionResolutionMode::Complete => {
            let previous = existing_identity(&canonical_path)?;
            require_previous_identity(previous, supplied)?;
            main.verify()?;
            super::namespace::install(
                &private_path,
                &private.sidecar.file,
                &canonical_path,
                private.sidecar.local_identity(),
                previous,
            )?;
            live_sidecar::sync_parent(&canonical_path)?;
            main.verify()?;
            live_sidecar::verify_path(&canonical_path, private.sidecar.local_identity())?;
            private.sidecar.verify_header()?;
            if let Some(previous) = previous {
                remove_exact(&private_path, previous)?;
            }
            Ok(resolved(
                supplied,
                LiveTransitionStatus::Initialized,
                Some(private.identity),
                LiveCoordinationLocation::Canonical,
            ))
        }
    }
}

fn observe(path: &Path, database_id: [u8; 16]) -> Result<Option<Observed>> {
    if live_sidecar::path_identity(path)?.is_none() {
        return Ok(None);
    }
    let (sidecar, state) = Sidecar::open_at(path.to_path_buf(), database_id)?;
    let identity = live_sidecar::public_identity(sidecar.local_identity());
    Ok(Some(Observed {
        sidecar,
        state,
        identity,
    }))
}

fn observe_reset_canonical(
    path: &Path,
    supplied: &LiveTransitionResult,
) -> Result<Option<ResetCanonical>> {
    let Some(identity) = existing_identity(path)? else {
        return Ok(None);
    };
    let public_identity = live_sidecar::public_identity(identity);
    if Some(public_identity) == supplied.previous_sidecar_identity {
        return Ok(Some(ResetCanonical::Previous));
    }
    let observed = observe(path, supplied.database_id)?.ok_or(Error::Conflict(
        "canonical sidecar disappeared during transition inspection",
    ))?;
    if !is_attempt(&observed, supplied) {
        return Err(Error::Conflict(
            "canonical sidecar is neither the old nor new transition inode",
        ));
    }
    Ok(Some(ResetCanonical::Attempt(observed)))
}

fn observe_reset_private(
    path: &Path,
    supplied: &LiveTransitionResult,
) -> Result<Option<ResetPrivate>> {
    let Some(identity) = existing_identity(path)? else {
        return Ok(None);
    };
    if Some(live_sidecar::public_identity(identity)) == supplied.previous_sidecar_identity {
        return Ok(Some(ResetPrivate::Previous(identity)));
    }
    let observed = observe(path, supplied.database_id)?.ok_or(Error::Conflict(
        "private sidecar disappeared during transition inspection",
    ))?;
    if !is_attempt(&observed, supplied) {
        return Err(Error::Conflict(
            "private sidecar belongs to another transition",
        ));
    }
    Ok(Some(ResetPrivate::Attempt(observed)))
}

fn require_supplied(supplied: &LiveTransitionResult) -> Result<()> {
    if supplied.database_id == [0; 16]
        || supplied.transaction_id == 0
        || supplied.commit_nonce == [0; 16]
        || supplied.reader_capacity == 0
        || supplied.sidecar_id == [0; 16]
    {
        return Err(Error::InvalidArgument(
            "live transition result is incomplete",
        ));
    }
    if supplied.new_sidecar_identity.is_none()
        && supplied.status == LiveTransitionStatus::OutcomeUnknown
    {
        return Err(Error::Unresolvable(
            "transition never proved its new sidecar identity",
        ));
    }
    Ok(())
}

fn require_main(main: &LockedMain, supplied: &LiveTransitionResult) -> Result<()> {
    if main.basename != supplied.main_basename {
        return Err(Error::Conflict("live transition destination name changed"));
    }
    if main.directory_identity != supplied.directory_identity {
        return Err(Error::DirectoryIdentityMismatch);
    }
    if main.public_identity != supplied.main_identity
        || main.bootstrap.meta.database_id != supplied.database_id
        || main.bootstrap.meta.txn_id != supplied.transaction_id
        || main.bootstrap.meta.commit_nonce != supplied.commit_nonce
    {
        return Err(Error::Conflict(
            "live transition main identity or generation changed",
        ));
    }
    Ok(())
}

fn require_attempt(observed: &Observed, supplied: &LiveTransitionResult) -> Result<()> {
    if !is_attempt(observed, supplied)
        || supplied
            .new_sidecar_identity
            .is_some_and(|identity| identity != observed.identity)
    {
        return Err(Error::Conflict(
            "coordination inode does not match the transition attempt",
        ));
    }
    Ok(())
}

fn is_attempt(observed: &Observed, supplied: &LiveTransitionResult) -> bool {
    observed.sidecar.header.database_id == supplied.database_id
        && observed.sidecar.header.sidecar_id == supplied.sidecar_id
        && observed.sidecar.header.capacity == supplied.reader_capacity
}

fn require_previous_identity(
    observed: Option<live_sidecar::Identity>,
    supplied: &LiveTransitionResult,
) -> Result<()> {
    if observed.map(live_sidecar::public_identity) != supplied.previous_sidecar_identity {
        return Err(Error::Conflict(
            "previous coordination identity changed before reset",
        ));
    }
    Ok(())
}

fn resolved(
    supplied: &LiveTransitionResult,
    status: LiveTransitionStatus,
    new_sidecar_identity: Option<LocalFileIdentity>,
    location: LiveCoordinationLocation,
) -> LiveTransitionResult {
    LiveTransitionResult {
        operation: supplied.operation,
        status,
        database_id: supplied.database_id,
        transaction_id: supplied.transaction_id,
        commit_nonce: supplied.commit_nonce,
        directory_identity: supplied.directory_identity,
        main_identity: supplied.main_identity,
        main_basename: supplied.main_basename,
        reader_capacity: supplied.reader_capacity,
        sidecar_id: supplied.sidecar_id,
        previous_sidecar_identity: supplied.previous_sidecar_identity,
        new_sidecar_identity,
        new_sidecar_location: location,
        residue_possible: false,
        cause: None,
    }
}

#[cfg(test)]
#[path = "resolution_tests.rs"]
mod tests;
