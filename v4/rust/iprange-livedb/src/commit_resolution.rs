//! Exact two-meta commit-outcome resolution.

use std::path::Path;

use crate::bootstrap::{self, Bootstrap, CommitAttemptResolution, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::database;
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, MAIN_LIFETIME_LOCK};
use crate::live_writer::{CommitCleanupArtifacts, CommitResult};
use crate::publication::{CleanupState, CoordinationCleanup};
use crate::validation::LocalFileIdentity;

/// Coordination mode used while proving one commit attempt.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommitResolutionMode {
    Live,
    Immutable,
}

/// Relation between the attempted and inspected local files.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LocalFileRelation {
    SameLocalFile,
    DifferentLocalFile,
}

/// Exact durability classification for one attempted transaction and nonce.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommitResolution {
    Committed,
    NotCommitted,
    SupersededUnknown,
    Unresolvable,
}

/// Factual identities and classification returned by commit resolution.
#[derive(Debug)]
pub struct CommitResolutionResult {
    pub attempted_database_id: [u8; 16],
    pub attempted_transaction_id: u64,
    pub attempted_commit_nonce: [u8; 16],
    pub actual_directory_identity: LocalFileIdentity,
    pub actual_main_identity: LocalFileIdentity,
    pub local_file_relation: LocalFileRelation,
    pub resolution: CommitResolution,
    pub cleanup: CommitCleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

impl CommitResolutionResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

struct Opened {
    file: std::fs::File,
    identity: live_sidecar::Identity,
    directory_identity: LocalFileIdentity,
    main_identity: LocalFileIdentity,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Classification {
    resolution: CommitResolution,
    selected_transaction_id: u64,
    selected_commit_nonce: [u8; 16],
}

/// Resolve one exact commit attempt without validating either page graph.
pub fn resolve_commit(
    path: impl AsRef<Path>,
    attempt: &CommitResult,
    mode: CommitResolutionMode,
    cancellation: &CancellationToken,
) -> Result<CommitResolutionResult> {
    validate_attempt(attempt)?;
    cancellation.check()?;
    let path = path.as_ref();
    let opened = open(path, mode)?;
    let relation = relation(attempt, &opened);

    match mode {
        CommitResolutionMode::Immutable => {
            let sidecar = crate::path::canonical_sidecar(path)?;
            database::require_sidecar_absent(&sidecar)?;
            let result = resolve_locked(path, attempt, &opened, None, cancellation, relation);
            let unlocked = live_lock::unlock(&opened.file, MAIN_LIFETIME_LOCK);
            result.and_then(|result| unlocked.map(|()| result))
        }
        CommitResolutionMode::Live => {
            let sidecar = live_sidecar::Sidecar::open(path, attempt.attempted_database_id)?;
            sidecar.lock_gate(Mode::Exclusive)?;
            if let Err(cause) = sidecar.claim_writer() {
                let _ = sidecar.unlock_gate();
                return Err(cause);
            }
            let result = resolve_locked(
                path,
                attempt,
                &opened,
                Some(&sidecar),
                cancellation,
                relation,
            );
            let released = sidecar.release_writer();
            let gate = sidecar.unlock_gate();
            let lifetime = live_lock::unlock(&opened.file, MAIN_LIFETIME_LOCK);
            result.and_then(|result| {
                released?;
                gate?;
                lifetime?;
                Ok(result)
            })
        }
    }
}

fn validate_attempt(attempt: &CommitResult) -> Result<()> {
    if attempt.attempted_database_id == [0; 16]
        || attempt.attempted_transaction_id == 0
        || attempt.attempted_commit_nonce == [0; 16]
    {
        return Err(Error::InvalidArgument("commit result is incomplete"));
    }
    Ok(())
}

fn open(path: &Path, mode: CommitResolutionMode) -> Result<Opened> {
    let file = match mode {
        CommitResolutionMode::Live => live_sidecar::open_rw(path)?,
        CommitResolutionMode::Immutable => database::open_read_only(path)?,
    };
    let identity = match mode {
        CommitResolutionMode::Live => live_sidecar::identity(&file)?,
        CommitResolutionMode::Immutable => live_sidecar::identity_any_link(&file)?,
    };
    live_lock::lock(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
    verify_main(path, identity, mode)?;
    Ok(Opened {
        directory_identity: live_sidecar::parent_identity(path)?,
        main_identity: live_sidecar::public_identity(identity),
        file,
        identity,
    })
}

fn resolve_locked(
    path: &Path,
    attempt: &CommitResult,
    opened: &Opened,
    sidecar: Option<&live_sidecar::Sidecar>,
    cancellation: &CancellationToken,
    relation: LocalFileRelation,
) -> Result<CommitResolutionResult> {
    cancellation.check()?;
    let first = match classify(&opened.file, attempt) {
        Ok(first) => first,
        Err(cause) => return Ok(unresolvable(attempt, opened, relation, cause)),
    };
    if let Some(sidecar) = sidecar {
        sidecar.scan_at_most(first.selected_transaction_id)?;
    }
    cancellation.check()?;
    if let Err(cause) = opened.file.sync_all() {
        return Ok(unresolvable(attempt, opened, relation, cause.into()));
    }
    cancellation.check()?;
    let second = match classify(&opened.file, attempt) {
        Ok(second) => second,
        Err(cause) => return Ok(unresolvable(attempt, opened, relation, cause)),
    };
    if first != second {
        return Ok(unresolvable(
            attempt,
            opened,
            relation,
            Error::WrongState("selected generation changed during resolution"),
        ));
    }

    verify_main(path, opened.identity, sidecar_mode(sidecar))?;
    if live_sidecar::parent_identity(path)? != opened.directory_identity {
        return Ok(unresolvable(
            attempt,
            opened,
            relation,
            Error::DirectoryIdentityMismatch,
        ));
    }
    match sidecar {
        Some(sidecar) => {
            sidecar.verify_path()?;
            sidecar.verify_header()?;
        }
        None => database::require_sidecar_absent(&crate::path::canonical_sidecar(path)?)?,
    }
    Ok(resolved(attempt, opened, relation, first.resolution))
}

fn classify(file: &std::fs::File, attempt: &CommitResult) -> Result<Classification> {
    let physical_bytes = file.metadata()?.len();
    let mut pages = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut pages, 0)?;
    let page0 = (&pages[..PAGE_SIZE]).try_into().unwrap();
    let page1 = (&pages[PAGE_SIZE..]).try_into().unwrap();
    let selected = bootstrap::open_meta_pages(page0, page1, physical_bytes, OpenMode::Writer)?;
    let resolution = match bootstrap::resolve_commit_attempt(
        page0,
        page1,
        physical_bytes,
        attempt.attempted_database_id,
        attempt.attempted_transaction_id,
        attempt.attempted_commit_nonce,
    )? {
        CommitAttemptResolution::Committed => CommitResolution::Committed,
        CommitAttemptResolution::NotCommitted => CommitResolution::NotCommitted,
        CommitAttemptResolution::SupersededUnknown => CommitResolution::SupersededUnknown,
    };
    Ok(classification(resolution, selected))
}

fn classification(resolution: CommitResolution, selected: Bootstrap) -> Classification {
    Classification {
        resolution,
        selected_transaction_id: selected.meta.txn_id,
        selected_commit_nonce: selected.meta.commit_nonce,
    }
}

fn relation(attempt: &CommitResult, opened: &Opened) -> LocalFileRelation {
    if attempt.directory_identity == opened.directory_identity
        && attempt.main_identity == opened.main_identity
    {
        LocalFileRelation::SameLocalFile
    } else {
        LocalFileRelation::DifferentLocalFile
    }
}

fn resolved(
    attempt: &CommitResult,
    opened: &Opened,
    relation: LocalFileRelation,
    resolution: CommitResolution,
) -> CommitResolutionResult {
    CommitResolutionResult {
        attempted_database_id: attempt.attempted_database_id,
        attempted_transaction_id: attempt.attempted_transaction_id,
        attempted_commit_nonce: attempt.attempted_commit_nonce,
        actual_directory_identity: opened.directory_identity,
        actual_main_identity: opened.main_identity,
        local_file_relation: relation,
        resolution,
        cleanup: CommitCleanupArtifacts::clean(),
        coordination_cleanup: CoordinationCleanup::None,
        cause: None,
    }
}

fn unresolvable(
    attempt: &CommitResult,
    opened: &Opened,
    relation: LocalFileRelation,
    cause: Error,
) -> CommitResolutionResult {
    let mut result = resolved(attempt, opened, relation, CommitResolution::Unresolvable);
    result.cause = Some(cause);
    result
}

fn verify_main(
    path: &Path,
    identity: live_sidecar::Identity,
    mode: CommitResolutionMode,
) -> Result<()> {
    match mode {
        CommitResolutionMode::Live => live_sidecar::verify_path(path, identity),
        CommitResolutionMode::Immutable => live_sidecar::verify_path_any_link(path, identity),
    }
}

fn sidecar_mode(sidecar: Option<&live_sidecar::Sidecar>) -> CommitResolutionMode {
    if sidecar.is_some() {
        CommitResolutionMode::Live
    } else {
        CommitResolutionMode::Immutable
    }
}
