//! Explicit inspection and offline removal of canonical publication residue.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::validation::LocalFileIdentity;

use super::types::{
    AccessPolicy, CleanupArtifacts, CleanupState, CoordinationCleanup, PublicationProblem,
    PublicationResult,
};
use super::{Housekeeping, HousekeepingArtifact, PublicationDigest, PublicationTuple};

/// Current classification of the canonical coordination name.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PublicationResidueCoordination {
    Absent,
    PublicationReservation,
    LiveSidecar,
    Unselectable,
}

/// Logical classification of a retained destination main.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PublicationResidueMainContent {
    V4,
    Other,
}

/// Stable evidence for a destination main that offline removal never changes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PublicationResidueMain {
    pub identity: LocalFileIdentity,
    pub content: PublicationResidueMainContent,
    pub tuple: Option<PublicationTuple>,
    pub digest: PublicationDigest,
    pub access_policy: AccessPolicy,
}

/// Read-only residue inspection.
#[derive(Debug)]
pub struct PublicationResidueInspection {
    pub directory_identity: LocalFileIdentity,
    pub coordination_identity: Option<LocalFileIdentity>,
    pub coordination: PublicationResidueCoordination,
    pub publication: Option<PublicationResult>,
    pub handle: Option<PublicationResidueHandle>,
}

/// Same-process authority for one exact canonical coordination inode.
#[derive(Debug)]
pub struct PublicationResidueHandle {
    #[cfg(any(unix, windows))]
    inner: platform::Handle,
    #[cfg(not(any(unix, windows)))]
    _unsupported: (),
}

impl PublicationResidueHandle {
    /// Release retained descriptors without transferring a cleanup obligation.
    pub fn close(self) {}
}

/// Factual result after an offline canonical-residue removal attempt.
#[derive(Debug)]
pub struct PublicationResidueRemoval {
    pub directory_identity: LocalFileIdentity,
    pub coordination_identity: LocalFileIdentity,
    pub main: Option<PublicationResidueMain>,
    pub later_coordination: PublicationResidueCoordination,
    pub coordination_access_policy: AccessPolicy,
    pub cleanup: CleanupArtifacts,
    pub coordination_cleanup: CoordinationCleanup,
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub handle: Option<PublicationResidueHandle>,
    pub cause: Option<PublicationProblem>,
}

impl PublicationResidueRemoval {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

/// Inspect publication residue without changing any file or namespace entry.
pub fn inspect_publication_residue(
    path: impl AsRef<Path>,
    cancellation: &CancellationToken,
) -> Result<PublicationResidueInspection, PublicationProblem> {
    #[cfg(any(unix, windows))]
    {
        platform::inspect(path.as_ref(), cancellation)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (path, cancellation);
        Err(PublicationProblem::new(
            crate::error::ErrorCode::PublicationUnsupported,
            None,
            "publication residue inspection is not implemented on this platform",
        ))
    }
}

/// Remove one unselectable canonical coordination inode after certified quiescence.
pub fn remove_publication_residue(
    handle: PublicationResidueHandle,
    cancellation: &CancellationToken,
) -> Result<PublicationResidueRemoval, PublicationProblem> {
    #[cfg(any(unix, windows))]
    {
        platform::remove(handle.inner, cancellation)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (handle, cancellation);
        Err(PublicationProblem::new(
            crate::error::ErrorCode::PublicationUnsupported,
            None,
            "publication residue removal is not implemented on this platform",
        ))
    }
}

#[cfg(any(unix, windows))]
#[path = "residue/linux.rs"]
mod platform;

#[cfg(any(unix, windows))]
#[path = "residue/retirement.rs"]
mod retirement;

#[cfg(any(unix, windows))]
#[path = "residue/main.rs"]
mod main;

#[cfg(all(test, target_os = "linux"))]
#[path = "residue_tests.rs"]
mod tests;
