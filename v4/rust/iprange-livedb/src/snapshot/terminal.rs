//! Terminal snapshot facts and pre-publication cleanup state.

use crate::error::Error;
use crate::publication::{
    CleanupArtifact, CleanupArtifacts, CleanupState, CoordinationCleanup, Housekeeping,
    HousekeepingArtifact, PrivateOutputAttempt, PublicationPreparationFailure, PublicationProblem,
    PublicationResult,
};
use crate::recovery::RecoverySourceCleanupGuard;

use crate::publication::cleanup::EarlyDiscard;
use crate::recovery::source_guard::problem;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SnapshotResult {
    pub publication: PublicationResult,
}

impl SnapshotResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        self.publication.cleanup_state()
    }
}

#[derive(Debug)]
pub struct SnapshotPreparationFailure {
    pub output: Option<PrivateOutputAttempt>,
    pub cleanup: CleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub source_cleanup: Option<RecoverySourceCleanupGuard>,
    pub cause: PublicationProblem,
}

impl SnapshotPreparationFailure {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }

    pub(crate) fn early(cause: Error) -> Self {
        Self::new(problem(&cause), None, None, None)
    }

    pub(crate) fn new(
        cause: PublicationProblem,
        output: Option<PrivateOutputAttempt>,
        output_artifact: Option<CleanupArtifact>,
        source_cleanup: Option<RecoverySourceCleanupGuard>,
    ) -> Self {
        let mut cleanup = CleanupArtifacts::new();
        if let Some(artifact) = output_artifact {
            cleanup.push(artifact);
        }
        let coordination_cleanup = if source_cleanup.is_some() {
            CoordinationCleanup::CleanupGuard
        } else {
            CoordinationCleanup::None
        };
        Self {
            output,
            cleanup,
            coordination_cleanup,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
            source_cleanup,
            cause,
        }
    }

    pub(crate) fn discarded(
        cause: PublicationProblem,
        discarded: EarlyDiscard,
        source_cleanup: Option<RecoverySourceCleanupGuard>,
    ) -> Self {
        let mut cleanup = CleanupArtifacts::new();
        if let Some(artifact) = discarded.artifact {
            cleanup.push(artifact);
        }
        let coordination_cleanup = if source_cleanup.is_some() {
            CoordinationCleanup::CleanupGuard
        } else {
            CoordinationCleanup::None
        };
        Self {
            output: Some(discarded.output),
            cleanup,
            coordination_cleanup,
            housekeeping: discarded.housekeeping,
            visible_housekeeping: discarded.visible_housekeeping,
            source_cleanup,
            cause,
        }
    }

    pub(crate) fn from_publication(failure: PublicationPreparationFailure) -> Self {
        let PublicationPreparationFailure {
            publication_attempt_id,
            directory_identity,
            private_output_basename_encoding,
            private_output_basename,
            output_identity,
            creation_security,
            cleanup,
            coordination_cleanup,
            housekeeping,
            visible_housekeeping,
            cause,
        } = failure;
        Self {
            output: Some(PrivateOutputAttempt {
                publication_attempt_id,
                directory_identity,
                basename_encoding: private_output_basename_encoding,
                basename: private_output_basename,
                identity: Some(output_identity),
                creation_security,
            }),
            cleanup,
            coordination_cleanup,
            housekeeping,
            visible_housekeeping,
            source_cleanup: None,
            cause,
        }
    }
}
