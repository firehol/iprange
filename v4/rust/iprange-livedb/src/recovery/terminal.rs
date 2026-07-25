//! Public terminal recovery facts and bounded cleanup composition.

use crate::error::Error;
#[cfg(target_os = "linux")]
use crate::publication::{ArtifactKind, DirectoryRole};
use crate::publication::{
    CleanupArtifact, CleanupArtifacts, CleanupState, CoordinationCleanup, CreationSecurity,
    Housekeeping, HousekeepingArtifact, PrivateOutputAttempt, PublicationPreparationFailure,
    PublicationProblem, PublicationResult,
};
use crate::validation::LocalFileIdentity;

use super::source_guard::{problem, RecoverySourceCleanupGuard};
use super::{RecoveryReport, ScratchCleanup};

#[cfg(target_os = "linux")]
const POSIX_KIND: u16 = 1;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RecoveryScratchAttempt {
    pub attempt_id: [u8; 16],
    pub directory_identity: LocalFileIdentity,
    pub creation_security: CreationSecurity,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RecoveryResult {
    pub report: RecoveryReport,
    pub scratch: Option<RecoveryScratchAttempt>,
    pub publication: PublicationResult,
}

impl RecoveryResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        self.publication.cleanup_state()
    }
}

#[derive(Debug)]
pub struct RecoveryPreparationFailure {
    pub report: RecoveryReport,
    pub scratch: Option<RecoveryScratchAttempt>,
    pub output: Option<PrivateOutputAttempt>,
    pub cleanup: CleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub source_cleanup: Option<RecoverySourceCleanupGuard>,
    pub cause: PublicationProblem,
}

impl RecoveryPreparationFailure {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }

    pub(crate) fn early(cause: Error) -> Self {
        Self::new(
            problem(&cause),
            RecoveryReport::default(),
            None,
            None,
            None,
            None,
        )
    }

    pub(crate) fn new(
        cause: PublicationProblem,
        report: RecoveryReport,
        output: Option<PrivateOutputAttempt>,
        output_artifact: Option<CleanupArtifact>,
        scratch: Option<ScratchCleanup>,
        source_cleanup: Option<RecoverySourceCleanupGuard>,
    ) -> Self {
        let mut cleanup = CleanupArtifacts::new();
        if let Some(artifact) = output_artifact {
            cleanup.push(artifact);
        }
        let scratch = absorb_scratch(scratch, &mut cleanup);
        let coordination_cleanup = if source_cleanup.is_some() {
            CoordinationCleanup::CleanupGuard
        } else {
            CoordinationCleanup::None
        };
        Self {
            report,
            scratch,
            output,
            cleanup,
            coordination_cleanup,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
            source_cleanup,
            cause,
        }
    }

    pub(crate) fn from_publication(
        failure: PublicationPreparationFailure,
        report: RecoveryReport,
        scratch: Option<ScratchCleanup>,
    ) -> Self {
        let PublicationPreparationFailure {
            publication_attempt_id,
            directory_identity,
            private_output_basename_encoding,
            private_output_basename,
            output_identity,
            creation_security,
            mut cleanup,
            coordination_cleanup,
            housekeeping,
            visible_housekeeping,
            cause,
        } = failure;
        let output = Some(PrivateOutputAttempt {
            publication_attempt_id,
            directory_identity,
            basename_encoding: private_output_basename_encoding,
            basename: private_output_basename,
            identity: Some(output_identity),
            creation_security,
        });
        let scratch = absorb_scratch(scratch, &mut cleanup);
        Self {
            report,
            scratch,
            output,
            cleanup,
            coordination_cleanup,
            housekeeping,
            visible_housekeeping,
            source_cleanup: None,
            cause,
        }
    }
}

pub(crate) fn completed(
    report: RecoveryReport,
    scratch: Option<ScratchCleanup>,
    mut publication: PublicationResult,
) -> RecoveryResult {
    let scratch = absorb_scratch(scratch, &mut publication.cleanup);
    RecoveryResult {
        report,
        scratch,
        publication,
    }
}

#[cfg(target_os = "linux")]
fn absorb_scratch(
    cleanup: Option<ScratchCleanup>,
    artifacts: &mut CleanupArtifacts,
) -> Option<RecoveryScratchAttempt> {
    let cleanup = cleanup?;
    for residue in cleanup.residues {
        artifacts.push(CleanupArtifact {
            kind: ArtifactKind::AuthorizedScratch,
            directory_role: DirectoryRole::ScratchDirectory,
            directory_identity: residue.directory_identity,
            basename_encoding: POSIX_KIND,
            basename: residue.basename,
            identity: Some(residue.identity),
            creation_security: Some(CreationSecurity {
                kind: residue.creation_security_kind,
                commitment: residue.creation_security_commitment,
            }),
            unpublished_tail: None,
            error: PublicationProblem {
                code: residue.problem.code,
                os_code: residue.problem.os_code,
                detail: residue.problem.detail,
            },
        });
    }
    Some(RecoveryScratchAttempt {
        attempt_id: cleanup.attempt_id,
        directory_identity: cleanup.directory_identity,
        creation_security: CreationSecurity {
            kind: cleanup.creation_security_kind,
            commitment: cleanup.creation_security_commitment,
        },
    })
}

#[cfg(not(target_os = "linux"))]
fn absorb_scratch(
    cleanup: Option<ScratchCleanup>,
    _artifacts: &mut CleanupArtifacts,
) -> Option<RecoveryScratchAttempt> {
    debug_assert!(cleanup.is_none());
    None
}
