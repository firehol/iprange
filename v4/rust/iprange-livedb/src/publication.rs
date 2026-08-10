//! Crash-resolvable publication of complete immutable outputs.

use std::path::Path;

use crate::cancellation::CancellationToken;

pub(crate) mod attempt;
pub(crate) mod cleanup;
mod file_inspection;
#[cfg(windows)]
pub(crate) mod gc;
pub(crate) mod gc_barrier;
#[cfg(any(windows, test))]
mod gc_codec;
#[cfg(windows)]
mod gc_maintenance;
#[cfg(any(windows, test))]
pub(crate) mod gc_name;
mod main_file;
mod maintenance;
pub(crate) mod namespace;
pub(crate) mod output;
pub(crate) mod problem;
pub(crate) mod replacement;
mod replacement_inspection;
mod reservation;
mod reservation_file;
mod reservation_inspection;
mod residue;
mod resolver;
pub(crate) mod result;
pub(crate) mod security;
mod types;
pub(crate) mod workflow;

pub use maintenance::{
    list_abandoned_publication_temps, list_abandoned_reservation_artifacts,
    list_windows_housekeeping, remove_abandoned_publication_temp,
    remove_abandoned_reservation_artifact, remove_windows_housekeeping,
    AbandonedPublicationTempEntry, AbandonedPublicationTempList, AbandonedPublicationTempSink,
    AbandonedPublicationTempSinkControl, AbandonedReservationEntry, AbandonedReservationEvidence,
    AbandonedReservationList, AbandonedReservationPhase, AbandonedReservationPolicy,
    AbandonedReservationSink, AbandonedReservationSinkControl, HousekeepingPayloadIdentity,
    PublicationDigest, PublicationOutputEvidence, PublicationTuple,
    WindowsHousekeepingCandidateKind, WindowsHousekeepingEntry, WindowsHousekeepingList,
    WindowsHousekeepingRemoval, WindowsHousekeepingSink, WindowsHousekeepingSinkControl,
};
pub use residue::{
    inspect_publication_residue, remove_publication_residue, PublicationResidueCoordination,
    PublicationResidueHandle, PublicationResidueInspection, PublicationResidueMain,
    PublicationResidueMainContent, PublicationResidueRemoval,
};
pub use types::{
    AbandonedArtifactRemoval, AccessPolicy, ArtifactKind, ArtifactPresence, CleanupArtifact,
    CleanupArtifacts, CleanupState, CoordinationCleanup, CreationSecurity, DestinationContent,
    DirectoryRole, Housekeeping, HousekeepingArtifact, HousekeepingState, LaterCanonical,
    LiveLineage, PreviousDestination, PrivateOutputAttempt, PublicationAttempt, PublicationPolicy,
    PublicationPreparationFailure, PublicationProblem, PublicationResult, PublicationStatus,
    UnpublishedTailFacts,
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
    let mode = match mode {
        PublicationResolutionMode::Complete => resolver::Mode::Complete,
        PublicationResolutionMode::Remove => resolver::Mode::Remove,
    };
    resolver::resolve(path.as_ref(), supplied, mode, cancellation)
}

#[cfg(all(test, unix))]
#[path = "publication/crash_tests.rs"]
mod crash_tests;
