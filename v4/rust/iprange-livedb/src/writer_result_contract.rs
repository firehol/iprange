//! Private wire-neutral writer terminal-result descriptions.
//!
//! This module stores only fixed evidence in caller-provided memory. It owns no
//! filesystem resources, performs no commit or abort work, and performs no
//! cleanup from destructors.

use crate::contract::PAGE_SIZE;
use crate::error::ErrorCode;
use crate::name_binding::{basename_commitment, BasenameBindingError, BasenameEncoding};
use crate::sidecar::{valid_local_identity, LocalIdentityKind};
use crate::writer_transaction_contract::{
    PrivateCleanupState, PrivateCoordinationCleanup, PrivateTerminalCleanup,
    PrivateWriterContractError,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateResultContractError {
    InvalidLocalIdentity,
    NonCanonicalOptionalLocalIdentity,
    InvalidCreationSecurity,
    NonCanonicalOptionalCreationSecurity,
    InvalidUnpublishedTail,
    NonCanonicalOptionalUnpublishedTail,
    Basename(BasenameBindingError),
    BasenameOffsetOverflow,
    BasenameLengthOverflow,
    BasenameArenaTooSmall { required: u64, actual: u64 },
    BasenameReferenceInvalid,
    InvalidArtifactRole,
    InvalidArtifactGroups,
    CleanupArtifactOutOfBounds { index: usize, len: usize },
    MissingCleanupError { index: usize },
    InterruptedCleanup { index: usize },
    ProvenCleanCleanupEntry { index: usize },
    CleanupContract(PrivateWriterContractError),
    InvalidCommitAttempt,
    InvalidCommitResult,
    InvalidAbortResult,
}

impl PrivateResultContractError {
    pub(crate) const fn code(self) -> ErrorCode {
        match self {
            Self::Basename(_) => ErrorCode::NameInvalid,
            Self::BasenameOffsetOverflow | Self::BasenameLengthOverflow => {
                ErrorCode::ArithmeticOverflow
            }
            Self::BasenameArenaTooSmall { .. } => ErrorCode::BufferTooSmall,
            Self::CleanupContract(error) => error.code(),
            Self::InvalidLocalIdentity
            | Self::NonCanonicalOptionalLocalIdentity
            | Self::InvalidCreationSecurity
            | Self::NonCanonicalOptionalCreationSecurity
            | Self::InvalidUnpublishedTail
            | Self::NonCanonicalOptionalUnpublishedTail
            | Self::BasenameReferenceInvalid
            | Self::InvalidArtifactRole
            | Self::InvalidArtifactGroups
            | Self::CleanupArtifactOutOfBounds { .. }
            | Self::MissingCleanupError { .. }
            | Self::InterruptedCleanup { .. }
            | Self::ProvenCleanCleanupEntry { .. }
            | Self::InvalidCommitAttempt
            | Self::InvalidCommitResult
            | Self::InvalidAbortResult => ErrorCode::WrongState,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateLocalIdentity {
    kind: LocalIdentityKind,
    bytes: [u8; 32],
}

impl PrivateLocalIdentity {
    pub(crate) fn new(
        kind: LocalIdentityKind,
        bytes: [u8; 32],
    ) -> Result<Self, PrivateResultContractError> {
        if !valid_local_identity(kind, &bytes) {
            return Err(PrivateResultContractError::InvalidLocalIdentity);
        }
        Ok(Self { kind, bytes })
    }

    pub(crate) const fn kind(self) -> LocalIdentityKind {
        self.kind
    }

    pub(crate) const fn bytes(&self) -> &[u8; 32] {
        &self.bytes
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateOptionalLocalIdentity {
    present: bool,
    kind: u16,
    bytes: [u8; 32],
}

impl PrivateOptionalLocalIdentity {
    pub(crate) const fn absent() -> Self {
        Self {
            present: false,
            kind: 0,
            bytes: [0; 32],
        }
    }

    pub(crate) const fn present(identity: PrivateLocalIdentity) -> Self {
        Self {
            present: true,
            kind: identity.kind as u16,
            bytes: identity.bytes,
        }
    }

    pub(crate) fn from_raw(
        present: bool,
        kind: u16,
        bytes: [u8; 32],
    ) -> Result<Self, PrivateResultContractError> {
        if !present {
            if kind != 0 || bytes != [0; 32] {
                return Err(PrivateResultContractError::NonCanonicalOptionalLocalIdentity);
            }
            return Ok(Self::absent());
        }
        let kind = LocalIdentityKind::from_wire(kind)
            .ok_or(PrivateResultContractError::InvalidLocalIdentity)?;
        Ok(Self::present(PrivateLocalIdentity::new(kind, bytes)?))
    }

    pub(crate) const fn is_present(self) -> bool {
        self.present
    }

    pub(crate) fn value(self) -> Option<PrivateLocalIdentity> {
        if !self.present {
            return None;
        }
        let kind = LocalIdentityKind::from_wire(self.kind)?;
        PrivateLocalIdentity::new(kind, self.bytes).ok()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCreationSecurityKind {
    Posix,
    Windows,
}

impl PrivateCreationSecurityKind {
    const fn from_wire(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::Posix),
            2 => Some(Self::Windows),
            _ => None,
        }
    }

    const fn as_wire(self) -> u16 {
        match self {
            Self::Posix => 1,
            Self::Windows => 2,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateCreationSecurity {
    kind: PrivateCreationSecurityKind,
    commitment: [u8; 32],
}

impl PrivateCreationSecurity {
    pub(crate) const fn new(kind: PrivateCreationSecurityKind, commitment: [u8; 32]) -> Self {
        Self { kind, commitment }
    }

    pub(crate) const fn kind(self) -> PrivateCreationSecurityKind {
        self.kind
    }

    pub(crate) const fn commitment(&self) -> &[u8; 32] {
        &self.commitment
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateOptionalCreationSecurity {
    present: bool,
    kind: u16,
    commitment: [u8; 32],
}

impl PrivateOptionalCreationSecurity {
    pub(crate) const fn absent() -> Self {
        Self {
            present: false,
            kind: 0,
            commitment: [0; 32],
        }
    }

    pub(crate) const fn present(security: PrivateCreationSecurity) -> Self {
        Self {
            present: true,
            kind: security.kind.as_wire(),
            commitment: security.commitment,
        }
    }

    pub(crate) fn from_raw(
        present: bool,
        kind: u16,
        commitment: [u8; 32],
    ) -> Result<Self, PrivateResultContractError> {
        if !present {
            if kind != 0 || commitment != [0; 32] {
                return Err(PrivateResultContractError::NonCanonicalOptionalCreationSecurity);
            }
            return Ok(Self::absent());
        }
        let kind = PrivateCreationSecurityKind::from_wire(kind)
            .ok_or(PrivateResultContractError::InvalidCreationSecurity)?;
        Ok(Self::present(PrivateCreationSecurity::new(
            kind, commitment,
        )))
    }

    pub(crate) const fn is_present(self) -> bool {
        self.present
    }

    pub(crate) fn value(self) -> Option<PrivateCreationSecurity> {
        if !self.present {
            return None;
        }
        Some(PrivateCreationSecurity::new(
            PrivateCreationSecurityKind::from_wire(self.kind)?,
            self.commitment,
        ))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateUnpublishedTailAuthority {
    expected_database_id: [u8; 16],
    committed_target_txn_id: u64,
    committed_target_nonce: [u8; 16],
    committed_target_length: u64,
    observed_tail_end_exclusive: u64,
}

impl PrivateUnpublishedTailAuthority {
    pub(crate) fn new(
        expected_database_id: [u8; 16],
        committed_target_txn_id: u64,
        committed_target_nonce: [u8; 16],
        committed_target_length: u64,
        observed_tail_end_exclusive: u64,
    ) -> Result<Self, PrivateResultContractError> {
        let page_size = PAGE_SIZE as u64;
        if expected_database_id == [0; 16]
            || committed_target_txn_id == 0
            || committed_target_nonce == [0; 16]
            || committed_target_length < 2 * page_size
            || committed_target_length % page_size != 0
            || observed_tail_end_exclusive % page_size != 0
            || observed_tail_end_exclusive <= committed_target_length
        {
            return Err(PrivateResultContractError::InvalidUnpublishedTail);
        }
        Ok(Self {
            expected_database_id,
            committed_target_txn_id,
            committed_target_nonce,
            committed_target_length,
            observed_tail_end_exclusive,
        })
    }

    pub(crate) const fn committed_target_length(self) -> u64 {
        self.committed_target_length
    }

    pub(crate) const fn observed_tail_end_exclusive(self) -> u64 {
        self.observed_tail_end_exclusive
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateOptionalUnpublishedTail {
    present: bool,
    expected_database_id: [u8; 16],
    committed_target_txn_id: u64,
    committed_target_nonce: [u8; 16],
    committed_target_length: u64,
    observed_tail_end_exclusive: u64,
}

impl PrivateOptionalUnpublishedTail {
    pub(crate) const fn absent() -> Self {
        Self {
            present: false,
            expected_database_id: [0; 16],
            committed_target_txn_id: 0,
            committed_target_nonce: [0; 16],
            committed_target_length: 0,
            observed_tail_end_exclusive: 0,
        }
    }

    pub(crate) const fn present(tail: PrivateUnpublishedTailAuthority) -> Self {
        Self {
            present: true,
            expected_database_id: tail.expected_database_id,
            committed_target_txn_id: tail.committed_target_txn_id,
            committed_target_nonce: tail.committed_target_nonce,
            committed_target_length: tail.committed_target_length,
            observed_tail_end_exclusive: tail.observed_tail_end_exclusive,
        }
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn from_raw(
        present: bool,
        expected_database_id: [u8; 16],
        committed_target_txn_id: u64,
        committed_target_nonce: [u8; 16],
        committed_target_length: u64,
        observed_tail_end_exclusive: u64,
    ) -> Result<Self, PrivateResultContractError> {
        if !present {
            if expected_database_id != [0; 16]
                || committed_target_txn_id != 0
                || committed_target_nonce != [0; 16]
                || committed_target_length != 0
                || observed_tail_end_exclusive != 0
            {
                return Err(PrivateResultContractError::NonCanonicalOptionalUnpublishedTail);
            }
            return Ok(Self::absent());
        }
        Ok(Self::present(PrivateUnpublishedTailAuthority::new(
            expected_database_id,
            committed_target_txn_id,
            committed_target_nonce,
            committed_target_length,
            observed_tail_end_exclusive,
        )?))
    }

    pub(crate) const fn is_present(self) -> bool {
        self.present
    }

    pub(crate) fn value(self) -> Option<PrivateUnpublishedTailAuthority> {
        if !self.present {
            return None;
        }
        PrivateUnpublishedTailAuthority::new(
            self.expected_database_id,
            self.committed_target_txn_id,
            self.committed_target_nonce,
            self.committed_target_length,
            self.observed_tail_end_exclusive,
        )
        .ok()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateBasenameRecord {
    encoding: BasenameEncoding,
    offset: u64,
    length: u32,
}

#[derive(Debug)]
pub(crate) struct PrivateBasenameArena<'storage> {
    bytes: &'storage mut [u8],
    used: u64,
}

impl<'storage> PrivateBasenameArena<'storage> {
    pub(crate) fn new(bytes: &'storage mut [u8]) -> Self {
        Self { bytes, used: 0 }
    }

    pub(crate) const fn used(&self) -> u64 {
        self.used
    }

    pub(crate) fn append(
        &mut self,
        encoding: BasenameEncoding,
        basename: &[u8],
    ) -> Result<PrivateBasenameRecord, PrivateResultContractError> {
        basename_commitment(encoding, basename).map_err(PrivateResultContractError::Basename)?;
        let length = u32::try_from(basename.len())
            .map_err(|_| PrivateResultContractError::BasenameLengthOverflow)?;
        let offset = self.used;
        let end = offset
            .checked_add(u64::from(length))
            .ok_or(PrivateResultContractError::BasenameOffsetOverflow)?;
        let capacity = u64::try_from(self.bytes.len())
            .map_err(|_| PrivateResultContractError::BasenameOffsetOverflow)?;
        if end > capacity {
            return Err(PrivateResultContractError::BasenameArenaTooSmall {
                required: end,
                actual: capacity,
            });
        }
        let start = usize::try_from(offset)
            .map_err(|_| PrivateResultContractError::BasenameOffsetOverflow)?;
        let end_index =
            usize::try_from(end).map_err(|_| PrivateResultContractError::BasenameOffsetOverflow)?;
        self.bytes[start..end_index].copy_from_slice(basename);
        self.used = end;
        Ok(PrivateBasenameRecord {
            encoding,
            offset,
            length,
        })
    }

    pub(crate) fn resolve(
        &self,
        record: PrivateBasenameRecord,
    ) -> Result<&[u8], PrivateResultContractError> {
        let end = record
            .offset
            .checked_add(u64::from(record.length))
            .ok_or(PrivateResultContractError::BasenameReferenceInvalid)?;
        if end > self.used {
            return Err(PrivateResultContractError::BasenameReferenceInvalid);
        }
        let start = usize::try_from(record.offset)
            .map_err(|_| PrivateResultContractError::BasenameReferenceInvalid)?;
        let end = usize::try_from(end)
            .map_err(|_| PrivateResultContractError::BasenameReferenceInvalid)?;
        let basename = self
            .bytes
            .get(start..end)
            .ok_or(PrivateResultContractError::BasenameReferenceInvalid)?;
        basename_commitment(record.encoding, basename)
            .map_err(|_| PrivateResultContractError::BasenameReferenceInvalid)?;
        Ok(basename)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCleanupArtifactKind {
    PrivateOutput,
    PrivateReservation,
    OwnedCoordination,
    AuthorizedScratch,
    UnpublishedMainTail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCleanupDirectoryRole {
    Destination,
    ScratchDirectory,
    MainFile,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateCleanupArtifact {
    kind: PrivateCleanupArtifactKind,
    directory_role: PrivateCleanupDirectoryRole,
    directory_identity: PrivateLocalIdentity,
    basename: PrivateBasenameRecord,
    identity: PrivateOptionalLocalIdentity,
    creation_security: PrivateOptionalCreationSecurity,
    unpublished_tail: PrivateOptionalUnpublishedTail,
}

impl PrivateCleanupArtifact {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        kind: PrivateCleanupArtifactKind,
        directory_role: PrivateCleanupDirectoryRole,
        directory_identity: PrivateLocalIdentity,
        basename: PrivateBasenameRecord,
        identity: PrivateOptionalLocalIdentity,
        creation_security: PrivateOptionalCreationSecurity,
        unpublished_tail: PrivateOptionalUnpublishedTail,
    ) -> Result<Self, PrivateResultContractError> {
        let role_valid = match kind {
            PrivateCleanupArtifactKind::PrivateOutput
            | PrivateCleanupArtifactKind::PrivateReservation => {
                directory_role == PrivateCleanupDirectoryRole::Destination
            }
            PrivateCleanupArtifactKind::OwnedCoordination => matches!(
                directory_role,
                PrivateCleanupDirectoryRole::Destination | PrivateCleanupDirectoryRole::MainFile
            ),
            PrivateCleanupArtifactKind::AuthorizedScratch => {
                directory_role == PrivateCleanupDirectoryRole::ScratchDirectory
            }
            PrivateCleanupArtifactKind::UnpublishedMainTail => {
                directory_role == PrivateCleanupDirectoryRole::MainFile
            }
        };
        if !role_valid {
            return Err(PrivateResultContractError::InvalidArtifactRole);
        }

        let tail = kind == PrivateCleanupArtifactKind::UnpublishedMainTail;
        let platform_matches = basename.encoding as u16 == directory_identity.kind as u16
            && identity
                .value()
                .map_or(true, |identity| identity.kind == directory_identity.kind)
            && creation_security.value().map_or(true, |security| {
                matches!(
                    (directory_identity.kind, security.kind),
                    (LocalIdentityKind::Posix, PrivateCreationSecurityKind::Posix)
                        | (
                            LocalIdentityKind::Windows,
                            PrivateCreationSecurityKind::Windows
                        )
                )
            });
        let groups_valid = platform_matches
            && if tail {
                identity.is_present()
                    && !creation_security.is_present()
                    && unpublished_tail.is_present()
            } else {
                creation_security.is_present() && !unpublished_tail.is_present()
            };
        if !groups_valid {
            return Err(PrivateResultContractError::InvalidArtifactGroups);
        }

        Ok(Self {
            kind,
            directory_role,
            directory_identity,
            basename,
            identity,
            creation_security,
            unpublished_tail,
        })
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct PrivateCleanupArtifactView<'result> {
    artifact: &'result PrivateCleanupArtifact,
    basename: &'result [u8],
    cleanup_error: &'result ErrorCode,
}

impl PrivateCleanupArtifactView<'_> {
    pub(crate) const fn artifact(&self) -> &PrivateCleanupArtifact {
        self.artifact
    }

    pub(crate) const fn basename(&self) -> &[u8] {
        self.basename
    }

    pub(crate) const fn cleanup_state(&self) -> PrivateCleanupState {
        PrivateCleanupState::ResiduePossible
    }

    pub(crate) const fn cleanup_error(&self) -> ErrorCode {
        *self.cleanup_error
    }
}

#[derive(Debug)]
pub(crate) struct PrivateTerminalResult<'names, 'entries, O, G> {
    basenames: PrivateBasenameArena<'names>,
    cleanup: PrivateTerminalCleanup<'entries, PrivateCleanupArtifact, O, ErrorCode, G>,
    cause: Option<ErrorCode>,
    guard_taken: bool,
}

impl<'names, 'entries, O, G> PrivateTerminalResult<'names, 'entries, O, G> {
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn new(
        basenames: PrivateBasenameArena<'names>,
        cleanup: PrivateTerminalCleanup<'entries, PrivateCleanupArtifact, O, ErrorCode, G>,
        cause: Option<ErrorCode>,
    ) -> Result<
        Self,
        (
            PrivateBasenameArena<'names>,
            PrivateTerminalCleanup<'entries, PrivateCleanupArtifact, O, ErrorCode, G>,
            PrivateResultContractError,
        ),
    > {
        let ledger = cleanup.ledger();
        match ledger.interrupted_attempt_index() {
            Ok(Some(index)) => {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::InterruptedCleanup { index },
                ));
            }
            Err(error) => {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::CleanupContract(error),
                ));
            }
            Ok(None) => {}
        }
        for index in 0..ledger.len() {
            if ledger.is_proven_clean(index) != Some(false) {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::ProvenCleanCleanupEntry { index },
                ));
            }
            let Some(artifact) = ledger.obligation(index) else {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::CleanupContract(
                        PrivateWriterContractError::CleanupStorageCorrupt { index },
                    ),
                ));
            };
            if ledger.last_error(index).is_none() {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::MissingCleanupError { index },
                ));
            }
            if basenames.resolve(artifact.basename).is_err() {
                return Err((
                    basenames,
                    cleanup,
                    PrivateResultContractError::BasenameReferenceInvalid,
                ));
            }
        }
        Ok(Self {
            basenames,
            cleanup,
            cause,
            guard_taken: false,
        })
    }

    fn cleanup_state(&self) -> PrivateCleanupState {
        self.cleanup.cleanup_state()
    }

    const fn coordination(&self) -> PrivateCoordinationCleanup {
        self.cleanup.coordination()
    }

    const fn cause(&self) -> Option<ErrorCode> {
        self.cause
    }

    fn cleanup_artifact(
        &self,
        index: usize,
    ) -> Result<PrivateCleanupArtifactView<'_>, PrivateResultContractError> {
        let ledger = self.cleanup.ledger();
        let Some(artifact) = ledger.obligation(index) else {
            return Err(PrivateResultContractError::CleanupArtifactOutOfBounds {
                index,
                len: ledger.len(),
            });
        };
        let error = ledger
            .last_error(index)
            .ok_or(PrivateResultContractError::MissingCleanupError { index })?;
        let basename = self.basenames.resolve(artifact.basename)?;
        Ok(PrivateCleanupArtifactView {
            artifact,
            basename,
            cleanup_error: error,
        })
    }

    fn take_cleanup_guard(&mut self) -> Result<G, PrivateWriterContractError> {
        let guard = self.cleanup.take_cleanup_guard()?;
        self.guard_taken = true;
        Ok(guard)
    }

    fn destroy_blocked(&self) -> bool {
        self.coordination() == PrivateCoordinationCleanup::CleanupGuard && !self.guard_taken
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateCommitAttempt {
    attempted_database_id: [u8; 16],
    directory_identity: PrivateLocalIdentity,
    main_identity: PrivateLocalIdentity,
    attempted_txn_id: u64,
    attempted_commit_nonce: [u8; 16],
}

impl PrivateCommitAttempt {
    pub(crate) fn new(
        attempted_database_id: [u8; 16],
        directory_identity: PrivateLocalIdentity,
        main_identity: PrivateLocalIdentity,
        attempted_txn_id: u64,
        attempted_commit_nonce: [u8; 16],
    ) -> Result<Self, PrivateResultContractError> {
        if attempted_database_id == [0; 16]
            || attempted_txn_id == 0
            || attempted_commit_nonce == [0; 16]
            || directory_identity.kind != main_identity.kind
        {
            return Err(PrivateResultContractError::InvalidCommitAttempt);
        }
        Ok(Self {
            attempted_database_id,
            directory_identity,
            main_identity,
            attempted_txn_id,
            attempted_commit_nonce,
        })
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCommitDurability {
    NotCommitted,
    Committed,
    OutcomeUnknown,
}

#[derive(Debug)]
pub(crate) struct PrivateCommitResult<'names, 'entries, O, G> {
    attempt: PrivateCommitAttempt,
    durability: PrivateCommitDurability,
    terminal: PrivateTerminalResult<'names, 'entries, O, G>,
}

impl<'names, 'entries, O, G> PrivateCommitResult<'names, 'entries, O, G> {
    #[allow(clippy::result_large_err)]
    pub(crate) fn new(
        attempt: PrivateCommitAttempt,
        durability: PrivateCommitDurability,
        terminal: PrivateTerminalResult<'names, 'entries, O, G>,
    ) -> Result<
        Self,
        (
            PrivateCommitAttempt,
            PrivateCommitDurability,
            PrivateTerminalResult<'names, 'entries, O, G>,
            PrivateResultContractError,
        ),
    > {
        let coordination = terminal.coordination();
        let valid = match durability {
            PrivateCommitDurability::OutcomeUnknown => {
                coordination == PrivateCoordinationCleanup::RetainedWriterCloseRequired
                    && terminal.cleanup_state() == PrivateCleanupState::ResiduePossible
            }
            PrivateCommitDurability::NotCommitted | PrivateCommitDurability::Committed => {
                coordination != PrivateCoordinationCleanup::RetainedReaderCloseRequired
            }
        };
        if !valid {
            return Err((
                attempt,
                durability,
                terminal,
                PrivateResultContractError::InvalidCommitResult,
            ));
        }
        Ok(Self {
            attempt,
            durability,
            terminal,
        })
    }

    pub(crate) const fn attempt(&self) -> PrivateCommitAttempt {
        self.attempt
    }

    pub(crate) const fn durability(&self) -> PrivateCommitDurability {
        self.durability
    }

    pub(crate) fn cleanup_state(&self) -> PrivateCleanupState {
        self.terminal.cleanup_state()
    }

    pub(crate) const fn coordination(&self) -> PrivateCoordinationCleanup {
        self.terminal.coordination()
    }

    pub(crate) const fn cause(&self) -> Option<ErrorCode> {
        self.terminal.cause()
    }

    pub(crate) fn cleanup_artifact(
        &self,
        index: usize,
    ) -> Result<PrivateCleanupArtifactView<'_>, PrivateResultContractError> {
        self.terminal.cleanup_artifact(index)
    }

    pub(crate) fn take_cleanup_guard(&mut self) -> Result<G, PrivateWriterContractError> {
        self.terminal.take_cleanup_guard()
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn try_destroy(self) -> Result<(), (Self, ErrorCode)> {
        if self.terminal.destroy_blocked() {
            return Err((self, ErrorCode::HandleBusy));
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateAbortOutcome {
    Aborted,
    AbortIncomplete,
}

#[derive(Debug)]
pub(crate) struct PrivateAbortResult<'names, 'entries, O, G> {
    outcome: PrivateAbortOutcome,
    terminal: PrivateTerminalResult<'names, 'entries, O, G>,
}

impl<'names, 'entries, O, G> PrivateAbortResult<'names, 'entries, O, G> {
    #[allow(clippy::result_large_err)]
    pub(crate) fn new(
        outcome: PrivateAbortOutcome,
        terminal: PrivateTerminalResult<'names, 'entries, O, G>,
    ) -> Result<
        Self,
        (
            PrivateAbortOutcome,
            PrivateTerminalResult<'names, 'entries, O, G>,
            PrivateResultContractError,
        ),
    > {
        let coordination = terminal.coordination();
        let cleanup_state = terminal.cleanup_state();
        let valid = match outcome {
            PrivateAbortOutcome::Aborted => {
                (cleanup_state == PrivateCleanupState::Clean
                    && coordination == PrivateCoordinationCleanup::None)
                    || (cleanup_state == PrivateCleanupState::ResiduePossible
                        && coordination == PrivateCoordinationCleanup::RetainedWriterCloseRequired)
            }
            PrivateAbortOutcome::AbortIncomplete => {
                cleanup_state == PrivateCleanupState::ResiduePossible
                    && coordination == PrivateCoordinationCleanup::RetainedWriterCloseRequired
            }
        };
        if !valid {
            return Err((
                outcome,
                terminal,
                PrivateResultContractError::InvalidAbortResult,
            ));
        }
        Ok(Self { outcome, terminal })
    }

    pub(crate) const fn outcome(&self) -> PrivateAbortOutcome {
        self.outcome
    }

    pub(crate) fn cleanup_state(&self) -> PrivateCleanupState {
        self.terminal.cleanup_state()
    }

    pub(crate) const fn coordination(&self) -> PrivateCoordinationCleanup {
        self.terminal.coordination()
    }

    pub(crate) const fn cause(&self) -> Option<ErrorCode> {
        self.terminal.cause()
    }

    pub(crate) fn cleanup_artifact(
        &self,
        index: usize,
    ) -> Result<PrivateCleanupArtifactView<'_>, PrivateResultContractError> {
        self.terminal.cleanup_artifact(index)
    }

    pub(crate) fn take_cleanup_guard(&mut self) -> Result<G, PrivateWriterContractError> {
        self.terminal.take_cleanup_guard()
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn try_destroy(self) -> Result<(), (Self, ErrorCode)> {
        if self.terminal.destroy_blocked() {
            return Err((self, ErrorCode::HandleBusy));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_alloc::count_thread_allocations;
    use crate::writer_transaction_contract::{FixedCleanupLedger, PrivateCleanupEntry};
    use core::cell::Cell;

    fn local_identity(kind: LocalIdentityKind, seed: u8) -> PrivateLocalIdentity {
        let mut bytes = [0; 32];
        bytes[0] = seed;
        PrivateLocalIdentity::new(kind, bytes).unwrap()
    }

    fn creation_security(kind: PrivateCreationSecurityKind) -> PrivateCreationSecurity {
        PrivateCreationSecurity::new(kind, [0x5a; 32])
    }

    fn unpublished_tail() -> PrivateUnpublishedTailAuthority {
        PrivateUnpublishedTailAuthority::new(
            [0x11; 16],
            7,
            [0x22; 16],
            2 * PAGE_SIZE as u64,
            3 * PAGE_SIZE as u64,
        )
        .unwrap()
    }

    fn artifact(
        kind: PrivateCleanupArtifactKind,
        role: PrivateCleanupDirectoryRole,
        basename: PrivateBasenameRecord,
        identity_present: bool,
        security_present: bool,
        tail_present: bool,
    ) -> Result<PrivateCleanupArtifact, PrivateResultContractError> {
        PrivateCleanupArtifact::new(
            kind,
            role,
            local_identity(LocalIdentityKind::Posix, 1),
            basename,
            if identity_present {
                PrivateOptionalLocalIdentity::present(local_identity(LocalIdentityKind::Posix, 2))
            } else {
                PrivateOptionalLocalIdentity::absent()
            },
            if security_present {
                PrivateOptionalCreationSecurity::present(creation_security(
                    PrivateCreationSecurityKind::Posix,
                ))
            } else {
                PrivateOptionalCreationSecurity::absent()
            },
            if tail_present {
                PrivateOptionalUnpublishedTail::present(unpublished_tail())
            } else {
                PrivateOptionalUnpublishedTail::absent()
            },
        )
    }

    fn commit_attempt() -> PrivateCommitAttempt {
        PrivateCommitAttempt::new(
            [0x31; 16],
            local_identity(LocalIdentityKind::Posix, 3),
            local_identity(LocalIdentityKind::Posix, 4),
            9,
            [0x41; 16],
        )
        .unwrap()
    }

    fn retain_failure(
        ledger: &mut FixedCleanupLedger<'_, PrivateCleanupArtifact, u8, ErrorCode>,
        artifact: PrivateCleanupArtifact,
        owner: u8,
        error: ErrorCode,
    ) {
        ledger.append(artifact, owner).unwrap();
        let mut executor = |_: &PrivateCleanupArtifact, _: &mut u8| Err(error);
        let report = ledger.retry_all(Some(&mut executor)).unwrap();
        assert_eq!(report.attempted(), 1);
        assert_eq!(report.cleaned(), 0);
        assert_eq!(report.remaining(), 1);
        assert_eq!(report.first_cause(), Some(&error));
    }

    #[test]
    fn optional_groups_are_explicit_canonical_and_do_not_use_zero_sentinels() {
        assert_eq!(
            PrivateOptionalLocalIdentity::from_raw(false, 0, [0; 32]).unwrap(),
            PrivateOptionalLocalIdentity::absent()
        );
        assert_eq!(
            PrivateOptionalLocalIdentity::from_raw(false, 1, [0; 32]).unwrap_err(),
            PrivateResultContractError::NonCanonicalOptionalLocalIdentity
        );
        let zero_identity =
            PrivateOptionalLocalIdentity::from_raw(true, LocalIdentityKind::Posix as u16, [0; 32])
                .unwrap();
        assert!(zero_identity.is_present());
        assert_eq!(zero_identity.value().unwrap().bytes(), &[0; 32]);
        let mut invalid_padding = [0; 32];
        invalid_padding[31] = 1;
        assert_eq!(
            PrivateOptionalLocalIdentity::from_raw(
                true,
                LocalIdentityKind::Posix as u16,
                invalid_padding,
            )
            .unwrap_err(),
            PrivateResultContractError::InvalidLocalIdentity
        );

        assert_eq!(
            PrivateOptionalCreationSecurity::from_raw(false, 0, [0; 32]).unwrap(),
            PrivateOptionalCreationSecurity::absent()
        );
        assert_eq!(
            PrivateOptionalCreationSecurity::from_raw(false, 1, [0; 32]).unwrap_err(),
            PrivateResultContractError::NonCanonicalOptionalCreationSecurity
        );
        assert_eq!(
            PrivateOptionalCreationSecurity::from_raw(true, 0, [0; 32]).unwrap_err(),
            PrivateResultContractError::InvalidCreationSecurity
        );
        let zero_security = PrivateOptionalCreationSecurity::from_raw(true, 1, [0; 32]).unwrap();
        assert!(zero_security.is_present());
        assert_eq!(zero_security.value().unwrap().commitment(), &[0; 32]);

        assert_eq!(
            PrivateOptionalUnpublishedTail::from_raw(false, [0; 16], 0, [0; 16], 0, 0).unwrap(),
            PrivateOptionalUnpublishedTail::absent()
        );
        assert_eq!(
            PrivateOptionalUnpublishedTail::from_raw(false, [1; 16], 0, [0; 16], 0, 0).unwrap_err(),
            PrivateResultContractError::NonCanonicalOptionalUnpublishedTail
        );
    }

    #[test]
    fn unpublished_tail_requires_exact_nonzero_and_page_geometry_authority() {
        let page = PAGE_SIZE as u64;
        assert_eq!(unpublished_tail().committed_target_length(), 2 * page);
        assert_eq!(unpublished_tail().observed_tail_end_exclusive(), 3 * page);
        for candidate in [
            PrivateUnpublishedTailAuthority::new([0; 16], 1, [1; 16], 2 * page, 3 * page),
            PrivateUnpublishedTailAuthority::new([1; 16], 0, [1; 16], 2 * page, 3 * page),
            PrivateUnpublishedTailAuthority::new([1; 16], 1, [0; 16], 2 * page, 3 * page),
            PrivateUnpublishedTailAuthority::new([1; 16], 1, [1; 16], page, 2 * page),
            PrivateUnpublishedTailAuthority::new([1; 16], 1, [1; 16], 2 * page + 1, 3 * page),
            PrivateUnpublishedTailAuthority::new([1; 16], 1, [1; 16], 2 * page, 3 * page + 1),
            PrivateUnpublishedTailAuthority::new([1; 16], 1, [1; 16], 2 * page, 2 * page),
        ] {
            assert_eq!(
                candidate.unwrap_err(),
                PrivateResultContractError::InvalidUnpublishedTail
            );
        }
        assert!(PrivateOptionalUnpublishedTail::present(unpublished_tail())
            .value()
            .is_some());
    }

    #[test]
    fn basename_arena_is_exact_atomic_and_platform_encoded() {
        let mut storage = [0xa5; 6];
        let mut arena = PrivateBasenameArena::new(&mut storage);
        let raw_posix = [0xff, b'x'];
        let first = arena
            .append(BasenameEncoding::PosixBytes, &raw_posix)
            .unwrap();
        let windows = [b'y', 0, b'z', 0];
        let second = arena
            .append(BasenameEncoding::WindowsUtf16Le, &windows)
            .unwrap();
        assert_eq!(arena.used(), 6);
        assert_eq!(arena.resolve(first).unwrap(), raw_posix);
        assert_eq!(arena.resolve(second).unwrap(), windows);

        assert_eq!(arena.bytes, &[0xff, b'x', b'y', 0, b'z', 0]);
        assert_eq!(
            arena
                .append(BasenameEncoding::PosixBytes, b"q")
                .unwrap_err(),
            PrivateResultContractError::BasenameArenaTooSmall {
                required: 7,
                actual: 6,
            }
        );
        assert_eq!(arena.used(), 6);
        assert_eq!(arena.bytes, &[0xff, b'x', b'y', 0, b'z', 0]);

        let mut invalid_storage = [0xa5; 8];
        let mut invalid_arena = PrivateBasenameArena::new(&mut invalid_storage);
        assert!(matches!(
            invalid_arena
                .append(BasenameEncoding::PosixBytes, b"a/b")
                .unwrap_err(),
            PrivateResultContractError::Basename(BasenameBindingError::InvalidPosixComponent)
        ));
        assert_eq!(invalid_arena.used(), 0);
        assert_eq!(invalid_arena.bytes, &[0xa5; 8]);
        assert!(invalid_arena
            .append(BasenameEncoding::WindowsUtf16Le, &[0x00, 0xd8])
            .is_err());

        invalid_arena.used = u64::MAX;
        assert_eq!(
            invalid_arena
                .append(BasenameEncoding::PosixBytes, b"x")
                .unwrap_err(),
            PrivateResultContractError::BasenameOffsetOverflow
        );
        assert_eq!(invalid_arena.used(), u64::MAX);
    }

    #[cfg(target_pointer_width = "32")]
    #[test]
    fn basename_resolution_rejects_unrepresentable_32_bit_offsets() {
        let mut storage = [0; 1];
        let arena = PrivateBasenameArena {
            bytes: &mut storage,
            used: u64::from(u32::MAX) + 2,
        };
        let record = PrivateBasenameRecord {
            encoding: BasenameEncoding::PosixBytes,
            offset: u64::from(u32::MAX) + 1,
            length: 1,
        };
        assert_eq!(
            arena.resolve(record).unwrap_err(),
            PrivateResultContractError::BasenameReferenceInvalid
        );
    }

    #[test]
    fn artifact_kind_role_and_optional_group_matrix_is_exhaustive() {
        let kinds = [
            PrivateCleanupArtifactKind::PrivateOutput,
            PrivateCleanupArtifactKind::PrivateReservation,
            PrivateCleanupArtifactKind::OwnedCoordination,
            PrivateCleanupArtifactKind::AuthorizedScratch,
            PrivateCleanupArtifactKind::UnpublishedMainTail,
        ];
        let roles = [
            PrivateCleanupDirectoryRole::Destination,
            PrivateCleanupDirectoryRole::ScratchDirectory,
            PrivateCleanupDirectoryRole::MainFile,
        ];
        let record = PrivateBasenameRecord {
            encoding: BasenameEncoding::PosixBytes,
            offset: 0,
            length: 1,
        };
        for kind in kinds {
            for role in roles {
                for identity_present in [false, true] {
                    for security_present in [false, true] {
                        for tail_present in [false, true] {
                            let role_valid = match kind {
                                PrivateCleanupArtifactKind::PrivateOutput
                                | PrivateCleanupArtifactKind::PrivateReservation => {
                                    role == PrivateCleanupDirectoryRole::Destination
                                }
                                PrivateCleanupArtifactKind::OwnedCoordination => matches!(
                                    role,
                                    PrivateCleanupDirectoryRole::Destination
                                        | PrivateCleanupDirectoryRole::MainFile
                                ),
                                PrivateCleanupArtifactKind::AuthorizedScratch => {
                                    role == PrivateCleanupDirectoryRole::ScratchDirectory
                                }
                                PrivateCleanupArtifactKind::UnpublishedMainTail => {
                                    role == PrivateCleanupDirectoryRole::MainFile
                                }
                            };
                            let groups_valid =
                                if kind == PrivateCleanupArtifactKind::UnpublishedMainTail {
                                    identity_present && !security_present && tail_present
                                } else {
                                    security_present && !tail_present
                                };
                            assert_eq!(
                                artifact(
                                    kind,
                                    role,
                                    record,
                                    identity_present,
                                    security_present,
                                    tail_present,
                                )
                                .is_ok(),
                                role_valid && groups_valid,
                                "{kind:?} {role:?} identity={identity_present} security={security_present} tail={tail_present}"
                            );
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn artifact_platform_kinds_must_match_the_directory_identity() {
        let posix_record = PrivateBasenameRecord {
            encoding: BasenameEncoding::PosixBytes,
            offset: 0,
            length: 1,
        };
        let common = (
            PrivateCleanupArtifactKind::PrivateOutput,
            PrivateCleanupDirectoryRole::Destination,
            local_identity(LocalIdentityKind::Posix, 1),
        );
        assert_eq!(
            PrivateCleanupArtifact::new(
                common.0,
                common.1,
                common.2,
                PrivateBasenameRecord {
                    encoding: BasenameEncoding::WindowsUtf16Le,
                    ..posix_record
                },
                PrivateOptionalLocalIdentity::absent(),
                PrivateOptionalCreationSecurity::present(creation_security(
                    PrivateCreationSecurityKind::Posix,
                )),
                PrivateOptionalUnpublishedTail::absent(),
            )
            .unwrap_err(),
            PrivateResultContractError::InvalidArtifactGroups
        );
        assert_eq!(
            PrivateCleanupArtifact::new(
                common.0,
                common.1,
                common.2,
                posix_record,
                PrivateOptionalLocalIdentity::present(local_identity(
                    LocalIdentityKind::Windows,
                    2,
                )),
                PrivateOptionalCreationSecurity::present(creation_security(
                    PrivateCreationSecurityKind::Posix,
                )),
                PrivateOptionalUnpublishedTail::absent(),
            )
            .unwrap_err(),
            PrivateResultContractError::InvalidArtifactGroups
        );
        assert_eq!(
            PrivateCleanupArtifact::new(
                common.0,
                common.1,
                common.2,
                posix_record,
                PrivateOptionalLocalIdentity::absent(),
                PrivateOptionalCreationSecurity::present(creation_security(
                    PrivateCreationSecurityKind::Windows,
                )),
                PrivateOptionalUnpublishedTail::absent(),
            )
            .unwrap_err(),
            PrivateResultContractError::InvalidArtifactGroups
        );
    }

    #[test]
    fn commit_attempt_is_exact_and_does_not_treat_zero_identity_as_absent() {
        let zero = local_identity(LocalIdentityKind::Posix, 0);
        let attempt = PrivateCommitAttempt::new([1; 16], zero, zero, 1, [2; 16]).unwrap();
        assert_eq!(attempt.directory_identity.bytes(), &[0; 32]);
        assert_eq!(attempt.main_identity.bytes(), &[0; 32]);
        assert_eq!(attempt.directory_identity.kind(), LocalIdentityKind::Posix);
        for candidate in [
            PrivateCommitAttempt::new([0; 16], zero, zero, 1, [2; 16]),
            PrivateCommitAttempt::new([1; 16], zero, zero, 0, [2; 16]),
            PrivateCommitAttempt::new([1; 16], zero, zero, 1, [0; 16]),
            PrivateCommitAttempt::new(
                [1; 16],
                zero,
                local_identity(LocalIdentityKind::Windows, 0),
                1,
                [2; 16],
            ),
        ] {
            assert_eq!(
                candidate.unwrap_err(),
                PrivateResultContractError::InvalidCommitAttempt
            );
        }
    }

    #[test]
    fn commit_durability_coordination_artifact_and_cause_matrix_is_exhaustive() {
        let durabilities = [
            PrivateCommitDurability::NotCommitted,
            PrivateCommitDurability::Committed,
            PrivateCommitDurability::OutcomeUnknown,
        ];
        let coordinations = [
            PrivateCoordinationCleanup::None,
            PrivateCoordinationCleanup::CleanupGuard,
            PrivateCoordinationCleanup::RetainedReaderCloseRequired,
            PrivateCoordinationCleanup::RetainedWriterCloseRequired,
        ];
        for durability in durabilities {
            for coordination in coordinations {
                for has_artifact in [false, true] {
                    for cause in [None, Some(ErrorCode::Conflict)] {
                        let mut names = [0; 1];
                        let mut arena = PrivateBasenameArena::new(&mut names);
                        let record = arena.append(BasenameEncoding::PosixBytes, b"x").unwrap();
                        let mut entry_storage: [Option<
                            PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>,
                        >; 1] = [None];
                        let mut ledger = FixedCleanupLedger::new(&mut entry_storage).unwrap();
                        if has_artifact {
                            retain_failure(
                                &mut ledger,
                                artifact(
                                    PrivateCleanupArtifactKind::PrivateOutput,
                                    PrivateCleanupDirectoryRole::Destination,
                                    record,
                                    false,
                                    true,
                                    false,
                                )
                                .unwrap(),
                                1,
                                ErrorCode::Io,
                            );
                        }
                        let cleanup = PrivateTerminalCleanup::new(
                            ledger,
                            coordination,
                            if coordination == PrivateCoordinationCleanup::CleanupGuard {
                                Some(7u8)
                            } else {
                                None
                            },
                        )
                        .unwrap();
                        let terminal = PrivateTerminalResult::new(arena, cleanup, cause).unwrap();
                        let expected = match durability {
                            PrivateCommitDurability::OutcomeUnknown => {
                                coordination
                                    == PrivateCoordinationCleanup::RetainedWriterCloseRequired
                            }
                            PrivateCommitDurability::NotCommitted
                            | PrivateCommitDurability::Committed => {
                                coordination
                                    != PrivateCoordinationCleanup::RetainedReaderCloseRequired
                            }
                        };
                        let result =
                            PrivateCommitResult::new(commit_attempt(), durability, terminal);
                        assert_eq!(
                            result.is_ok(),
                            expected,
                            "{durability:?} {coordination:?} artifact={has_artifact} cause={cause:?}"
                        );
                        if let Ok(result) = result {
                            assert_eq!(result.attempt(), commit_attempt());
                            assert_eq!(result.durability(), durability);
                            assert_eq!(result.coordination(), coordination);
                            assert_eq!(result.cause(), cause);
                            assert_eq!(
                                result.cleanup_state(),
                                if has_artifact || coordination != PrivateCoordinationCleanup::None
                                {
                                    PrivateCleanupState::ResiduePossible
                                } else {
                                    PrivateCleanupState::Clean
                                }
                            );
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn abort_outcome_coordination_artifact_and_cause_matrix_is_exhaustive() {
        let outcomes = [
            PrivateAbortOutcome::Aborted,
            PrivateAbortOutcome::AbortIncomplete,
        ];
        let coordinations = [
            PrivateCoordinationCleanup::None,
            PrivateCoordinationCleanup::CleanupGuard,
            PrivateCoordinationCleanup::RetainedReaderCloseRequired,
            PrivateCoordinationCleanup::RetainedWriterCloseRequired,
        ];
        for outcome in outcomes {
            for coordination in coordinations {
                for has_artifact in [false, true] {
                    for cause in [None, Some(ErrorCode::AbortIncomplete)] {
                        let mut names = [0; 1];
                        let mut arena = PrivateBasenameArena::new(&mut names);
                        let record = arena.append(BasenameEncoding::PosixBytes, b"x").unwrap();
                        let mut entry_storage: [Option<
                            PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>,
                        >; 1] = [None];
                        let mut ledger = FixedCleanupLedger::new(&mut entry_storage).unwrap();
                        if has_artifact {
                            retain_failure(
                                &mut ledger,
                                artifact(
                                    PrivateCleanupArtifactKind::PrivateOutput,
                                    PrivateCleanupDirectoryRole::Destination,
                                    record,
                                    false,
                                    true,
                                    false,
                                )
                                .unwrap(),
                                1,
                                ErrorCode::Io,
                            );
                        }
                        let cleanup = PrivateTerminalCleanup::new(
                            ledger,
                            coordination,
                            if coordination == PrivateCoordinationCleanup::CleanupGuard {
                                Some(7u8)
                            } else {
                                None
                            },
                        )
                        .unwrap();
                        let terminal = PrivateTerminalResult::new(arena, cleanup, cause).unwrap();
                        let state = terminal.cleanup_state();
                        let expected = match outcome {
                            PrivateAbortOutcome::Aborted => (state == PrivateCleanupState::Clean
                                && coordination == PrivateCoordinationCleanup::None)
                                || (state == PrivateCleanupState::ResiduePossible
                                    && coordination
                                        == PrivateCoordinationCleanup::RetainedWriterCloseRequired),
                            PrivateAbortOutcome::AbortIncomplete => {
                                state == PrivateCleanupState::ResiduePossible
                                    && coordination
                                        == PrivateCoordinationCleanup::RetainedWriterCloseRequired
                            }
                        };
                        let result = PrivateAbortResult::new(outcome, terminal);
                        assert_eq!(
                            result.is_ok(),
                            expected,
                            "{outcome:?} {coordination:?} artifact={has_artifact} cause={cause:?}"
                        );
                        if let Ok(result) = result {
                            assert_eq!(result.outcome(), outcome);
                            assert_eq!(result.coordination(), coordination);
                            assert_eq!(result.cause(), cause);
                            assert_eq!(result.cleanup_state(), state);
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn artifact_and_error_association_survives_cleanup_compaction() {
        let mut names = [0; 3];
        let mut arena = PrivateBasenameArena::new(&mut names);
        let records = [
            arena.append(BasenameEncoding::PosixBytes, b"a").unwrap(),
            arena.append(BasenameEncoding::PosixBytes, b"b").unwrap(),
            arena.append(BasenameEncoding::PosixBytes, b"c").unwrap(),
        ];
        let mut entry_storage: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>>;
            3] = [None, None, None];
        let mut ledger = FixedCleanupLedger::new(&mut entry_storage).unwrap();
        for (owner, record) in records.into_iter().enumerate() {
            ledger
                .append(
                    artifact(
                        PrivateCleanupArtifactKind::PrivateOutput,
                        PrivateCleanupDirectoryRole::Destination,
                        record,
                        false,
                        true,
                        false,
                    )
                    .unwrap(),
                    owner as u8,
                )
                .unwrap();
        }
        let mut executor = |_: &PrivateCleanupArtifact, owner: &mut u8| match *owner {
            1 => Ok(()),
            0 => Err(ErrorCode::Conflict),
            _ => Err(ErrorCode::Io),
        };
        let report = ledger.retry_all(Some(&mut executor)).unwrap();
        assert_eq!(report.cleaned(), 1);
        assert_eq!(report.remaining(), 2);
        assert_eq!(ledger.owner(0), Some(&0));
        assert_eq!(ledger.owner(1), Some(&2));
        let cleanup =
            PrivateTerminalCleanup::new(ledger, PrivateCoordinationCleanup::None, None::<()>)
                .unwrap();
        let terminal = PrivateTerminalResult::new(arena, cleanup, None).unwrap();
        let first = terminal.cleanup_artifact(0).unwrap();
        assert_eq!(first.basename(), b"a");
        assert_eq!(first.cleanup_error(), ErrorCode::Conflict);
        assert_eq!(first.cleanup_state(), PrivateCleanupState::ResiduePossible);
        assert_eq!(
            first.artifact().kind,
            PrivateCleanupArtifactKind::PrivateOutput
        );
        let second = terminal.cleanup_artifact(1).unwrap();
        assert_eq!(second.basename(), b"c");
        assert_eq!(second.cleanup_error(), ErrorCode::Io);
        assert_eq!(
            terminal.cleanup_artifact(2).unwrap_err(),
            PrivateResultContractError::CleanupArtifactOutOfBounds { index: 2, len: 2 }
        );
    }

    #[test]
    fn terminal_construction_rejects_missing_and_interrupted_cleanup_evidence_without_loss() {
        let mut names = [0; 1];
        let mut arena = PrivateBasenameArena::new(&mut names);
        let record = arena.append(BasenameEncoding::PosixBytes, b"x").unwrap();
        let mut entry_storage: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>>;
            1] = [None];
        let mut ledger = FixedCleanupLedger::new(&mut entry_storage).unwrap();
        ledger
            .append(
                artifact(
                    PrivateCleanupArtifactKind::PrivateOutput,
                    PrivateCleanupDirectoryRole::Destination,
                    record,
                    false,
                    true,
                    false,
                )
                .unwrap(),
                1,
            )
            .unwrap();
        let cleanup = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::CleanupGuard,
            Some(7u8),
        )
        .unwrap();
        let (arena, mut cleanup, error) =
            PrivateTerminalResult::new(arena, cleanup, None).unwrap_err();
        assert_eq!(
            error,
            PrivateResultContractError::MissingCleanupError { index: 0 }
        );
        assert_eq!(arena.used(), 1);
        assert_eq!(cleanup.ledger().len(), 1);
        assert_eq!(cleanup.take_cleanup_guard().unwrap(), 7);

        let mut names = [0; 1];
        let mut arena = PrivateBasenameArena::new(&mut names);
        let record = arena.append(BasenameEncoding::PosixBytes, b"x").unwrap();
        let mut entry_storage: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>>;
            1] = [None];
        let mut ledger = FixedCleanupLedger::new(&mut entry_storage).unwrap();
        ledger
            .append(
                artifact(
                    PrivateCleanupArtifactKind::PrivateOutput,
                    PrivateCleanupDirectoryRole::Destination,
                    record,
                    false,
                    true,
                    false,
                )
                .unwrap(),
                1,
            )
            .unwrap();
        let panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let mut executor = |_: &PrivateCleanupArtifact, _: &mut u8| -> Result<(), ErrorCode> {
                panic!("simulated interruption")
            };
            let _ = ledger.retry_all(Some(&mut executor));
        }));
        assert!(panic.is_err());
        let cleanup =
            PrivateTerminalCleanup::new(ledger, PrivateCoordinationCleanup::None, None::<()>)
                .unwrap();
        let (_, cleanup, error) = PrivateTerminalResult::new(arena, cleanup, None).unwrap_err();
        assert_eq!(
            error,
            PrivateResultContractError::InterruptedCleanup { index: 0 }
        );
        assert_eq!(
            cleanup.ledger().interrupted_attempt_index().unwrap(),
            Some(0)
        );
    }

    #[derive(Debug)]
    struct TestGuard<'a> {
        cleanup_calls: &'a Cell<u32>,
        drops: &'a Cell<u32>,
    }

    impl TestGuard<'_> {
        fn cleanup(&self) {
            self.cleanup_calls.set(self.cleanup_calls.get() + 1);
        }
    }

    impl Drop for TestGuard<'_> {
        fn drop(&mut self) {
            self.drops.set(self.drops.get() + 1);
        }
    }

    #[test]
    fn results_are_move_only_guard_take_is_once_and_destroy_is_explicit() {
        fn consume<T>(value: T) -> T {
            value
        }

        let cleanup_calls = Cell::new(0);
        let drops = Cell::new(0);
        let mut names = [];
        let mut entries: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, (), ErrorCode>>; 0] =
            [];
        let ledger = FixedCleanupLedger::new(&mut entries).unwrap();
        let cleanup = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::CleanupGuard,
            Some(TestGuard {
                cleanup_calls: &cleanup_calls,
                drops: &drops,
            }),
        )
        .unwrap();
        let terminal =
            PrivateTerminalResult::new(PrivateBasenameArena::new(&mut names), cleanup, None)
                .unwrap();
        let result = PrivateCommitResult::new(
            commit_attempt(),
            PrivateCommitDurability::Committed,
            terminal,
        )
        .unwrap();
        let (mut result, error) = consume(result).try_destroy().unwrap_err();
        assert_eq!(error, ErrorCode::HandleBusy);
        assert_eq!(
            result.coordination(),
            PrivateCoordinationCleanup::CleanupGuard
        );
        let guard = result.take_cleanup_guard().unwrap();
        assert_eq!(
            result.take_cleanup_guard().unwrap_err(),
            PrivateWriterContractError::CleanupGuardUnavailable
        );
        assert_eq!(cleanup_calls.get(), 0);
        guard.cleanup();
        drop(guard);
        assert_eq!(cleanup_calls.get(), 1);
        assert_eq!(drops.get(), 1);
        result.try_destroy().unwrap();

        let cleanup_calls = Cell::new(0);
        let drops = Cell::new(0);
        let mut names = [];
        let mut entries: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, (), ErrorCode>>; 0] =
            [];
        let ledger = FixedCleanupLedger::new(&mut entries).unwrap();
        let cleanup = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::CleanupGuard,
            Some(TestGuard {
                cleanup_calls: &cleanup_calls,
                drops: &drops,
            }),
        )
        .unwrap();
        let terminal =
            PrivateTerminalResult::new(PrivateBasenameArena::new(&mut names), cleanup, None)
                .unwrap();
        let result = PrivateCommitResult::new(
            commit_attempt(),
            PrivateCommitDurability::Committed,
            terminal,
        )
        .unwrap();
        drop(result);
        assert_eq!(cleanup_calls.get(), 0);
        assert_eq!(drops.get(), 1);
    }

    #[test]
    fn result_construction_views_take_and_destroy_allocate_zero_bytes() {
        let (_, allocations) = count_thread_allocations(|| {
            let mut names = [0; 1];
            let mut arena = PrivateBasenameArena::new(&mut names);
            let record = arena.append(BasenameEncoding::PosixBytes, b"x").unwrap();
            let mut entries: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, u8, ErrorCode>>;
                1] = [None];
            let mut ledger = FixedCleanupLedger::new(&mut entries).unwrap();
            retain_failure(
                &mut ledger,
                artifact(
                    PrivateCleanupArtifactKind::PrivateOutput,
                    PrivateCleanupDirectoryRole::Destination,
                    record,
                    false,
                    true,
                    false,
                )
                .unwrap(),
                1,
                ErrorCode::Io,
            );
            let cleanup = PrivateTerminalCleanup::new(
                ledger,
                PrivateCoordinationCleanup::RetainedWriterCloseRequired,
                None::<()>,
            )
            .unwrap();
            let terminal =
                PrivateTerminalResult::new(arena, cleanup, Some(ErrorCode::Conflict)).unwrap();
            let result = PrivateCommitResult::new(
                commit_attempt(),
                PrivateCommitDurability::OutcomeUnknown,
                terminal,
            )
            .unwrap();
            assert_eq!(result.cleanup_artifact(0).unwrap().basename(), b"x");
            result.try_destroy().unwrap();

            let mut names = [];
            let mut entries: [Option<PrivateCleanupEntry<PrivateCleanupArtifact, (), ErrorCode>>;
                0] = [];
            let ledger = FixedCleanupLedger::new(&mut entries).unwrap();
            let cleanup =
                PrivateTerminalCleanup::new(ledger, PrivateCoordinationCleanup::None, None::<()>)
                    .unwrap();
            let terminal =
                PrivateTerminalResult::new(PrivateBasenameArena::new(&mut names), cleanup, None)
                    .unwrap();
            PrivateAbortResult::new(PrivateAbortOutcome::Aborted, terminal)
                .unwrap()
                .try_destroy()
                .unwrap();
        });
        assert_eq!(allocations, 0);
    }

    #[test]
    fn production_contract_has_no_allocation_os_cleanup_or_invented_wire_representations() {
        let source = include_str!("writer_result_contract.rs");
        let production = source.split("#[cfg(test)]").next().unwrap();
        for forbidden in [
            "Vec<",
            "Box<",
            "dyn ",
            "std::",
            "std ::",
            "unsafe ",
            "impl Drop",
            "#[repr",
            "basename: PrivateCommit",
            "abort_attempt",
        ] {
            assert!(
                !production.contains(forbidden),
                "production contract contains forbidden token {forbidden:?}"
            );
        }
    }

    #[test]
    fn contract_errors_map_only_to_stable_error_codes() {
        assert_eq!(
            PrivateResultContractError::Basename(BasenameBindingError::Empty).code(),
            ErrorCode::NameInvalid
        );
        assert_eq!(
            PrivateResultContractError::BasenameOffsetOverflow.code(),
            ErrorCode::ArithmeticOverflow
        );
        assert_eq!(
            PrivateResultContractError::BasenameArenaTooSmall {
                required: 2,
                actual: 1,
            }
            .code(),
            ErrorCode::BufferTooSmall
        );
        assert_eq!(
            PrivateResultContractError::InvalidCommitResult.code(),
            ErrorCode::WrongState
        );
    }
}
