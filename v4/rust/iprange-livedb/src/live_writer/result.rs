//! Allocation-free live-writer terminal facts.

use std::path::Path;

#[cfg(unix)]
use std::os::unix::ffi::OsStrExt;

use crate::error::{Error, ErrorCode, Result};
use crate::publication::{CleanupState, CoordinationCleanup};
use crate::validation::LocalFileIdentity;

const MAX_BASENAME_BYTES: usize = 512;

/// One platform basename copied without allocation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LocalBasename {
    encoding: u16,
    length: u16,
    bytes: [u8; MAX_BASENAME_BYTES],
}

impl LocalBasename {
    pub(crate) fn from_path(path: &Path) -> Result<Self> {
        let name = path
            .file_name()
            .ok_or(Error::InvalidArgument("database path has no file name"))?;
        #[cfg(unix)]
        let (encoding, source) = (1, name.as_bytes());
        #[cfg(windows)]
        let (encoding, owned) = {
            use std::os::windows::ffi::OsStrExt;
            let units: Vec<u16> = name.encode_wide().collect();
            let mut bytes = Vec::with_capacity(units.len() * 2);
            for unit in units {
                bytes.extend_from_slice(&unit.to_le_bytes());
            }
            (2, bytes)
        };
        #[cfg(windows)]
        let source = owned.as_slice();
        #[cfg(not(any(unix, windows)))]
        let (encoding, source) = (0, &[][..]);

        if source.is_empty() || source.len() > MAX_BASENAME_BYTES {
            return Err(Error::InvalidArgument(
                "database basename exceeds the portable result bound",
            ));
        }
        let mut bytes = [0; MAX_BASENAME_BYTES];
        bytes[..source.len()].copy_from_slice(source);
        Ok(Self {
            encoding,
            length: source.len() as u16,
            bytes,
        })
    }

    pub const fn encoding(&self) -> u16 {
        self.encoding
    }

    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes[..usize::from(self.length)]
    }
}

/// Factual publication state of one attempted commit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommitDurability {
    NotCommitted,
    Committed,
    OutcomeUnknown,
}

/// One exact unresolved unpublished main tail.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CommitCleanupArtifact {
    pub directory_identity: LocalFileIdentity,
    pub main_basename: LocalBasename,
    pub main_identity: LocalFileIdentity,
    pub expected_database_id: [u8; 16],
    pub target_transaction_id: u64,
    pub target_commit_nonce: [u8; 16],
    pub committed_target_length: u64,
    pub observed_tail_end_exclusive: Option<u64>,
    pub cleanup_error: ErrorCode,
}

/// Fixed commit cleanup ledger; commits can own only their main tail.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct CommitCleanupArtifacts {
    entry: Option<CommitCleanupArtifact>,
}

impl CommitCleanupArtifacts {
    pub(crate) const fn clean() -> Self {
        Self { entry: None }
    }

    pub(crate) const fn tail(entry: CommitCleanupArtifact) -> Self {
        Self { entry: Some(entry) }
    }

    pub const fn len(&self) -> usize {
        if self.entry.is_some() {
            1
        } else {
            0
        }
    }

    pub const fn is_empty(&self) -> bool {
        self.entry.is_none()
    }

    pub const fn get(&self, index: usize) -> Option<&CommitCleanupArtifact> {
        if index == 0 {
            self.entry.as_ref()
        } else {
            None
        }
    }

    pub fn iter(&self) -> impl Iterator<Item = &CommitCleanupArtifact> {
        self.entry.iter()
    }
}

/// Exact identity, durability, and cleanup facts for one commit attempt.
#[derive(Debug)]
pub struct CommitResult {
    pub attempted_database_id: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub main_identity: LocalFileIdentity,
    pub attempted_transaction_id: u64,
    pub attempted_commit_nonce: [u8; 16],
    pub durability: CommitDurability,
    pub cleanup: CommitCleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

impl CommitResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbortOutcome {
    Aborted,
    AbortIncomplete,
}

/// Factual abort result; cleanup failure retains a close-only writer.
#[derive(Debug)]
pub struct AbortResult {
    pub outcome: AbortOutcome,
    pub cleanup: CommitCleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CloseOutcome {
    Closed,
    CloseIncomplete,
}

/// Factual writer-close result. An incomplete close is retryable.
#[derive(Debug)]
pub struct CloseResult {
    pub outcome: CloseOutcome,
    pub abort_outcome: Option<AbortOutcome>,
    pub cleanup: CommitCleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

impl CloseResult {
    pub(super) const fn closed(abort_outcome: Option<AbortOutcome>) -> Self {
        Self {
            outcome: CloseOutcome::Closed,
            abort_outcome,
            cleanup: CommitCleanupArtifacts::clean(),
            coordination_cleanup: CoordinationCleanup::None,
            cause: None,
        }
    }

    pub(super) fn incomplete(
        abort_outcome: Option<AbortOutcome>,
        cleanup: CommitCleanupArtifacts,
        cause: Error,
    ) -> Self {
        Self {
            outcome: CloseOutcome::CloseIncomplete,
            abort_outcome,
            cleanup,
            coordination_cleanup: CoordinationCleanup::RetainedWriterCloseRequired,
            cause: Some(cause),
        }
    }

    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

impl AbortResult {
    pub(super) const fn aborted() -> Self {
        Self {
            outcome: AbortOutcome::Aborted,
            cleanup: CommitCleanupArtifacts::clean(),
            coordination_cleanup: CoordinationCleanup::None,
            cause: None,
        }
    }

    pub(super) fn incomplete(artifact: CommitCleanupArtifact, cause: Error) -> Self {
        Self {
            outcome: AbortOutcome::AbortIncomplete,
            cleanup: CommitCleanupArtifacts::tail(artifact),
            coordination_cleanup: CoordinationCleanup::RetainedWriterCloseRequired,
            cause: Some(cause),
        }
    }

    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}
