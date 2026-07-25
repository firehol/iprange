//! Crash-resolvable publication of complete immutable outputs.

use std::path::Path;

use crate::cancellation::CancellationToken;

#[cfg(target_os = "linux")]
pub(crate) mod attempt;
#[cfg(target_os = "linux")]
pub(crate) mod cleanup;
#[cfg(target_os = "linux")]
mod file_inspection;
#[cfg(target_os = "linux")]
mod main_file;
mod maintenance;
#[cfg(target_os = "linux")]
pub(crate) mod namespace;
#[cfg(target_os = "linux")]
pub(crate) mod output;
#[cfg(target_os = "linux")]
pub(crate) mod problem;
#[cfg(target_os = "linux")]
pub(crate) mod replacement;
#[cfg(target_os = "linux")]
mod replacement_inspection;
mod reservation;
#[cfg(target_os = "linux")]
mod reservation_file;
#[cfg(target_os = "linux")]
mod reservation_inspection;
mod residue;
#[cfg(target_os = "linux")]
mod resolver;
#[cfg(target_os = "linux")]
pub(crate) mod result;
#[cfg(target_os = "linux")]
pub(crate) mod security;
mod types;

pub use maintenance::{
    list_abandoned_publication_temps, list_abandoned_reservation_artifacts,
    remove_abandoned_publication_temp, remove_abandoned_reservation_artifact,
    AbandonedPublicationTempEntry, AbandonedPublicationTempList, AbandonedPublicationTempSink,
    AbandonedPublicationTempSinkControl, AbandonedReservationEntry, AbandonedReservationEvidence,
    AbandonedReservationList, AbandonedReservationPhase, AbandonedReservationPolicy,
    AbandonedReservationSink, AbandonedReservationSinkControl, PublicationDigest,
    PublicationOutputEvidence, PublicationTuple,
};
pub use residue::{
    inspect_publication_residue, remove_publication_residue, PublicationResidueCoordination,
    PublicationResidueHandle, PublicationResidueInspection, PublicationResidueMain,
    PublicationResidueMainContent, PublicationResidueRemoval,
};
pub use types::{
    AccessPolicy, ArtifactKind, ArtifactPresence, CleanupArtifact, CleanupArtifacts, CleanupState,
    CoordinationCleanup, CreationSecurity, DestinationContent, DirectoryRole, Housekeeping,
    HousekeepingArtifact, HousekeepingState, LaterCanonical, LiveLineage, PreviousDestination,
    PrivateOutputAttempt, PublicationAttempt, PublicationPreparationFailure, PublicationProblem,
    PublicationResult, PublicationStatus, UnpublishedTailFacts,
};

/// Requested terminal action for one exact interrupted publication.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum PublicationResolutionMode {
    Complete,
    Remove,
}

/// Resolve one publication using a supplied result or retained reservation.
pub fn resolve_publication(
    path: impl AsRef<Path>,
    supplied: Option<&PublicationResult>,
    mode: PublicationResolutionMode,
    cancellation: &CancellationToken,
) -> std::result::Result<PublicationResult, PublicationProblem> {
    #[cfg(target_os = "linux")]
    {
        let mode = match mode {
            PublicationResolutionMode::Complete => resolver::Mode::Complete,
            PublicationResolutionMode::Remove => resolver::Mode::Remove,
        };
        resolver::resolve(path.as_ref(), supplied, mode, cancellation)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (path, supplied, mode, cancellation);
        Err(PublicationProblem::new(
            crate::error::ErrorCode::PublicationUnsupported,
            None,
            "publication resolution is not implemented on this platform",
        ))
    }
}

#[cfg(all(test, target_os = "linux"))]
#[path = "publication/crash_tests.rs"]
mod crash_tests;
