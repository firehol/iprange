//! Exact completion or rollback of one interrupted live transition.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::live_sidecar::{self, Sidecar, State};
use crate::publication::{ArtifactKind, DirectoryRole, Housekeeping};
use crate::validation::LocalFileIdentity;

#[cfg(unix)]
use super::transition::remove_exact;
use super::transition::{existing_identity, LockedMain};
use super::{
    LiveCoordinationLocation, LiveResetPolicy, LiveTransitionOperation, LiveTransitionResult,
    LiveTransitionStatus,
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
    crate::live_lock::require_live_supported()?;
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
            main.verify()?;
            let mut cleanup = cleanup_attempt(&canonical.sidecar, supplied);
            if let Err(cause) = main.verify() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            Ok(resolved_after_cleanup(
                supplied,
                LiveTransitionStatus::Unchanged,
                Some(canonical.identity),
                LiveCoordinationLocation::Absent,
                cleanup,
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
            if supplied.reset_policy == Some(LiveResetPolicy::DiscardPrevious)
                && mode == LiveTransitionResolutionMode::Rollback
            {
                return Err(Error::Unresolvable(
                    "discarding reset cannot restore the previous sidecar",
                ));
            }
            match private {
                Some(ResetPrivate::Previous(identity))
                    if supplied.reset_policy == Some(LiveResetPolicy::RollbackSafe) =>
                {
                    remove_previous(&private_path, identity)?;
                }
                Some(ResetPrivate::Previous(_)) => {
                    return Err(Error::Conflict(
                        "discarding reset retained the previous sidecar",
                    ))
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
            main.verify()?;
            let mut cleanup = cleanup_attempt(&private.sidecar, supplied);
            if let Err(cause) = main.verify() {
                cleanup.absorb(live_cleanup::Outcome::failed(cause));
            }
            Ok(resolved_after_cleanup(
                supplied,
                LiveTransitionStatus::Unchanged,
                Some(private.identity),
                LiveCoordinationLocation::Absent,
                cleanup,
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
                supplied
                    .reset_policy
                    .expect("validated reset result has a reset policy"),
            )?;
            live_sidecar::sync_parent(&canonical_path)?;
            main.verify()?;
            live_sidecar::verify_path(&canonical_path, private.sidecar.local_identity())?;
            private.sidecar.verify_header()?;
            if let (Some(previous), Some(LiveResetPolicy::RollbackSafe)) =
                (previous, supplied.reset_policy)
            {
                remove_previous(&private_path, previous)?;
            } else if existing_identity(&private_path)?.is_some() {
                return Err(Error::Conflict(
                    "discarding reset retained an unexpected private sidecar",
                ));
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
    match (supplied.operation, supplied.reset_policy) {
        (LiveTransitionOperation::Initialize, None) | (LiveTransitionOperation::Reset, Some(_)) => {
        }
        _ => {
            return Err(Error::InvalidArgument(
                "live transition result has an inconsistent reset policy",
            ))
        }
    }
    if cfg!(windows)
        && supplied.operation == LiveTransitionOperation::Reset
        && supplied.reset_policy == Some(LiveResetPolicy::RollbackSafe)
        && supplied.previous_sidecar_identity.is_some()
    {
        return Err(Error::Unresolvable(
            "rollback-safe live reset is unavailable on Windows",
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
        reset_policy: supplied.reset_policy,
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
        housekeeping: supplied.housekeeping,
        visible_housekeeping: supplied.visible_housekeeping.clone(),
        cause: None,
    }
}

fn cleanup_attempt(sidecar: &Sidecar, supplied: &LiveTransitionResult) -> live_cleanup::Outcome {
    live_cleanup::remove(
        &sidecar.path,
        &sidecar.file,
        sidecar.local_identity(),
        CleanupAuthority {
            attempt_id: supplied.sidecar_id,
            ordinal: 1,
            kind: ArtifactKind::OwnedCoordination,
            directory_role: DirectoryRole::MainFile,
        },
    )
}

fn resolved_after_cleanup(
    supplied: &LiveTransitionResult,
    clean_status: LiveTransitionStatus,
    new_sidecar_identity: Option<LocalFileIdentity>,
    clean_location: LiveCoordinationLocation,
    cleanup: live_cleanup::Outcome,
) -> LiveTransitionResult {
    let clean = cleanup.is_clean();
    let mut result = resolved(
        supplied,
        if clean {
            clean_status
        } else {
            LiveTransitionStatus::OutcomeUnknown
        },
        new_sidecar_identity,
        if clean {
            clean_location
        } else {
            supplied.new_sidecar_location
        },
    );
    result.residue_possible = !clean;
    result.housekeeping = merge_housekeeping(result.housekeeping, cleanup.housekeeping);
    let mut visible = result.visible_housekeeping.into_vec();
    visible.extend(cleanup.visible);
    result.visible_housekeeping = visible.into_boxed_slice();
    result.cause = cleanup.cause;
    result
}

fn remove_previous(path: &Path, identity: live_sidecar::Identity) -> Result<()> {
    #[cfg(unix)]
    {
        remove_exact(path, identity)
    }
    #[cfg(not(unix))]
    {
        let _ = (path, identity);
        Err(Error::Unresolvable(
            "rollback-safe previous coordination cleanup is unavailable",
        ))
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

#[cfg(test)]
#[path = "resolution_tests.rs"]
mod tests;
