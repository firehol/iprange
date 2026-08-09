//! Portable public facts returned by namespace publication.

use std::borrow::Cow;

use crate::error::ErrorCode;
use crate::validation::LocalFileIdentity;

const CLEANUP_CAPACITY: usize = 4;

/// Namespace policy selected for one immutable publication.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PublicationPolicy {
    FailIfExists,
    ReplaceExisting,
    ReplaceExistingNoRollback,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PublicationStatus {
    NotPublished,
    Published,
    OutcomeUnknown,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DestinationContent {
    Desired,
    Previous,
    Absent,
    Other,
    Unclassified,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LaterCanonical {
    None,
    ReservationOrTransition,
    ReadyLiveSidecar,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LiveLineage {
    SameGenerationExactBytes,
    SameGenerationPhysicalBytesChanged,
    AdvancedGeneration,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AccessPolicy {
    Absent,
    CreatorOnly,
    ChangedOrUnproven,
    Unclassified,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CleanupState {
    Clean,
    ResiduePossible,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ArtifactKind {
    PrivateOutput,
    PrivateReservation,
    OwnedCoordination,
    AuthorizedScratch,
    OwnedMain,
    UnpublishedMainTail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DirectoryRole {
    Destination,
    ScratchDirectory,
    MainFile,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CoordinationCleanup {
    None,
    CleanupGuard,
    RetainedReaderCloseRequired,
    RetainedWriterCloseRequired,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Housekeeping {
    None,
    CrashReappearancePossible,
    Visible,
}

impl Housekeeping {
    pub(crate) const fn merge(self, other: Self) -> Self {
        if matches!(self, Self::Visible) || matches!(other, Self::Visible) {
            Self::Visible
        } else if matches!(self, Self::CrashReappearancePossible)
            || matches!(other, Self::CrashReappearancePossible)
        {
            Self::CrashReappearancePossible
        } else {
            Self::None
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum HousekeepingState {
    MovePending,
    MoveAmbiguous,
    Inert,
    Conflict,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ArtifactPresence {
    Absent,
    Present,
    Unclassified,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CreationSecurity {
    pub kind: u16,
    pub commitment: [u8; 32],
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PublicationProblem {
    pub code: ErrorCode,
    pub os_code: Option<i32>,
    pub detail: Cow<'static, str>,
}

impl PublicationProblem {
    pub(crate) const fn new(code: ErrorCode, os_code: Option<i32>, detail: &'static str) -> Self {
        Self {
            code,
            os_code,
            detail: Cow::Borrowed(detail),
        }
    }

    pub(crate) fn with_owned_detail(code: ErrorCode, os_code: Option<i32>, detail: String) -> Self {
        Self {
            code,
            os_code,
            detail: Cow::Owned(detail),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UnpublishedTailFacts {
    pub expected_database_id: [u8; 16],
    pub committed_target_transaction_id: u64,
    pub committed_target_nonce: [u8; 16],
    pub committed_target_length: u64,
    pub observed_tail_end_exclusive: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CleanupArtifact {
    pub kind: ArtifactKind,
    pub directory_role: DirectoryRole,
    pub directory_identity: LocalFileIdentity,
    pub basename_encoding: u16,
    pub basename: Box<[u8]>,
    pub identity: Option<LocalFileIdentity>,
    pub creation_security: Option<CreationSecurity>,
    pub unpublished_tail: Option<UnpublishedTailFacts>,
    pub error: PublicationProblem,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HousekeepingArtifact {
    pub state: HousekeepingState,
    pub directory_role: DirectoryRole,
    pub directory_identity: LocalFileIdentity,
    pub basename_encoding: u16,
    pub attempt_id: [u8; 16],
    pub ordinal: u32,
    pub envelope_basename: Box<[u8]>,
    pub envelope_identity: LocalFileIdentity,
    pub source_basename: Box<[u8]>,
    pub inert_basename: Box<[u8]>,
    pub source_presence: ArtifactPresence,
    pub source_identity: Option<LocalFileIdentity>,
    pub inert_presence: ArtifactPresence,
    pub inert_identity: Option<LocalFileIdentity>,
    pub kind: ArtifactKind,
    pub creation_security: CreationSecurity,
    pub selected_envelope_sequence: u64,
}

/// Factual outcome of one exact abandoned-artifact removal.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AbandonedArtifactRemoval {
    pub source_present: bool,
    pub cleanup_state: CleanupState,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<PublicationProblem>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CleanupArtifacts {
    entries: [Option<CleanupArtifact>; CLEANUP_CAPACITY],
    len: usize,
}

impl CleanupArtifacts {
    pub(crate) const fn new() -> Self {
        Self {
            entries: [None, None, None, None],
            len: 0,
        }
    }

    pub(crate) fn push(&mut self, artifact: CleanupArtifact) {
        assert!(self.len < CLEANUP_CAPACITY, "fixed cleanup ledger overflow");
        self.entries[self.len] = Some(artifact);
        self.len += 1;
    }

    pub const fn len(&self) -> usize {
        self.len
    }

    pub const fn is_empty(&self) -> bool {
        self.len == 0
    }

    pub const fn state(&self) -> CleanupState {
        if self.is_empty() {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }

    pub fn get(&self, index: usize) -> Option<&CleanupArtifact> {
        self.entries.get(index)?.as_ref()
    }

    pub fn iter(&self) -> impl Iterator<Item = &CleanupArtifact> {
        self.entries[..self.len].iter().flatten()
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PreviousDestination {
    pub identity: LocalFileIdentity,
    pub byte_length: u64,
    pub sha512: [u8; 64],
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PublicationAttempt {
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub publication_attempt_id: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub destination_basename_encoding: u16,
    pub destination_basename: Box<[u8]>,
    pub output_identity: LocalFileIdentity,
    pub output_byte_length: u64,
    pub output_sha512: [u8; 64],
    pub publication_policy: PublicationPolicy,
    pub previous_destination: Option<PreviousDestination>,
    pub reservation_identity: LocalFileIdentity,
    pub creation_security: CreationSecurity,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PrivateOutputAttempt {
    pub publication_attempt_id: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub basename_encoding: u16,
    pub basename: Box<[u8]>,
    pub identity: Option<LocalFileIdentity>,
    pub creation_security: CreationSecurity,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PublicationResult {
    pub attempt: PublicationAttempt,
    pub main_namespace_may_have_been_attempted: bool,
    pub publication: PublicationStatus,
    pub destination_content: DestinationContent,
    pub later_canonical: LaterCanonical,
    pub live_lineage: Option<LiveLineage>,
    pub later_attempt_or_sidecar_id: Option<[u8; 16]>,
    pub later_selected_transaction_id: Option<u64>,
    pub later_selected_commit_nonce: Option<[u8; 16]>,
    pub main_access_policy: AccessPolicy,
    pub coordination_access_policy: AccessPolicy,
    pub cleanup: CleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<PublicationProblem>,
}

impl PublicationResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PublicationPreparationFailure {
    pub publication_attempt_id: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub private_output_basename_encoding: u16,
    pub private_output_basename: Box<[u8]>,
    pub output_identity: LocalFileIdentity,
    pub creation_security: CreationSecurity,
    pub cleanup: CleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: PublicationProblem,
}

impl PublicationPreparationFailure {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}
