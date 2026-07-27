//! Exact two-meta commit-outcome resolution.

use std::path::Path;

use crate::bootstrap::{self, Bootstrap, CommitAttemptResolution, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::database;
use crate::error::{combine_errors, Error, Result};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, MAIN_LIFETIME_LOCK};
use crate::live_writer::{
    CommitCleanupArtifact, CommitCleanupArtifacts, CommitResult, LocalBasename,
};
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
    main_basename: LocalBasename,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Classification {
    resolution: CommitResolution,
    selected_transaction_id: u64,
    selected_commit_nonce: [u8; 16],
    selected_database_id: [u8; 16],
    committed_bytes: u64,
    physical_bytes: u64,
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
    let opened = open(path, mode, cancellation)?;
    let relation = relation(attempt, &opened);

    match mode {
        CommitResolutionMode::Immutable => {
            let sidecar = crate::path::canonical_sidecar(path)?;
            database::require_sidecar_absent(&sidecar)?;
            let mut result = resolve_locked(path, attempt, &opened, None, cancellation, relation)?;
            if let Err(cause) = live_lock::unlock(&opened.file, MAIN_LIFETIME_LOCK) {
                record_postcondition_failure(&mut result, cause);
            }
            Ok(result)
        }
        CommitResolutionMode::Live => {
            let sidecar = live_sidecar::Sidecar::open(path, attempt.attempted_database_id)?;
            sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
            if let Err(cause) = sidecar.claim_writer() {
                return Err(combine_errors(cause, sidecar.unlock_gate()));
            }
            let mut result = resolve_locked(
                path,
                attempt,
                &opened,
                Some(&sidecar),
                cancellation,
                relation,
            )?;
            for release in [
                sidecar.release_writer(),
                sidecar.unlock_gate(),
                live_lock::unlock(&opened.file, MAIN_LIFETIME_LOCK),
            ] {
                if let Err(cause) = release {
                    record_postcondition_failure(&mut result, cause);
                }
            }
            Ok(result)
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

fn open(
    path: &Path,
    mode: CommitResolutionMode,
    cancellation: &CancellationToken,
) -> Result<Opened> {
    let file = match mode {
        CommitResolutionMode::Live => live_sidecar::open_rw(path)?,
        CommitResolutionMode::Immutable => live_sidecar::open_rw(path)?,
    };
    let identity = match mode {
        CommitResolutionMode::Live => live_sidecar::identity(&file)?,
        CommitResolutionMode::Immutable => live_sidecar::identity_any_link(&file)?,
    };
    live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
    verify_main(path, identity, mode)?;
    Ok(Opened {
        directory_identity: live_sidecar::parent_identity(path)?,
        main_identity: live_sidecar::public_identity(identity),
        main_basename: LocalBasename::from_path(path)?,
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
    crate::live_cleanup::require_main_available(path, opened.identity, first.selected_database_id)?;
    if let Some(sidecar) = sidecar {
        sidecar.scan_at_most_cancellable(first.selected_transaction_id, cancellation)?;
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
    Ok(resolve_tail(
        path, attempt, opened, sidecar, relation, first,
    ))
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
        selected_database_id: selected.meta.database_id,
        committed_bytes: selected.committed_bytes,
        physical_bytes: selected.physical_bytes,
    }
}

fn resolve_tail(
    path: &Path,
    attempt: &CommitResult,
    opened: &Opened,
    sidecar: Option<&live_sidecar::Sidecar>,
    relation: LocalFileRelation,
    selected: Classification,
) -> CommitResolutionResult {
    let mut result = resolved(attempt, opened, relation, selected.resolution);
    if selected.physical_bytes == selected.committed_bytes {
        return result;
    }

    let cleanup = trim_tail(path, opened, sidecar, selected);
    if let Err(cause) = cleanup {
        result.cleanup =
            CommitCleanupArtifacts::tail(tail_artifact(opened, selected, cause.code()));
        result.cause = Some(cause);
    }
    result
}

fn trim_tail(
    path: &Path,
    opened: &Opened,
    sidecar: Option<&live_sidecar::Sidecar>,
    selected: Classification,
) -> Result<()> {
    opened.file.set_len(selected.committed_bytes)?;
    opened.file.sync_all()?;
    let current = classify_selected(&opened.file, selected.resolution)?;
    let expected = Classification {
        physical_bytes: selected.committed_bytes,
        ..selected
    };
    if current != expected {
        return Err(Error::Unresolvable(
            "selected generation changed during tail cleanup",
        ));
    }
    verify_main(path, opened.identity, sidecar_mode(sidecar))?;
    if live_sidecar::parent_identity(path)? != opened.directory_identity {
        return Err(Error::DirectoryIdentityMismatch);
    }
    match sidecar {
        Some(sidecar) => {
            sidecar.verify_path()?;
            sidecar.verify_header()
        }
        None => database::require_sidecar_absent(&crate::path::canonical_sidecar(path)?),
    }
}

fn classify_selected(file: &std::fs::File, resolution: CommitResolution) -> Result<Classification> {
    let physical_bytes = file.metadata()?.len();
    let mut pages = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut pages, 0)?;
    let page0 = (&pages[..PAGE_SIZE]).try_into().unwrap();
    let page1 = (&pages[PAGE_SIZE..]).try_into().unwrap();
    let selected = bootstrap::open_meta_pages(page0, page1, physical_bytes, OpenMode::Writer)?;
    Ok(classification(resolution, selected))
}

fn tail_artifact(
    opened: &Opened,
    selected: Classification,
    cleanup_error: crate::error::ErrorCode,
) -> CommitCleanupArtifact {
    CommitCleanupArtifact {
        directory_identity: opened.directory_identity,
        main_basename: opened.main_basename,
        main_identity: opened.main_identity,
        expected_database_id: selected.selected_database_id,
        target_transaction_id: selected.selected_transaction_id,
        target_commit_nonce: selected.selected_commit_nonce,
        committed_target_length: selected.committed_bytes,
        observed_tail_end_exclusive: Some(selected.physical_bytes),
        cleanup_error,
    }
}

fn record_postcondition_failure(result: &mut CommitResolutionResult, cleanup: Error) {
    result.cause = Some(match result.cause.take() {
        Some(cause) => Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        },
        None => cleanup,
    });
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
