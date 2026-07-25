//! Crash-resolvable publication of complete immutable outputs.

#[cfg(target_os = "linux")]
pub(crate) mod attempt;
#[cfg(target_os = "linux")]
pub(crate) mod cleanup;
#[cfg(target_os = "linux")]
mod file_inspection;
#[cfg(target_os = "linux")]
mod main_file;
#[cfg(target_os = "linux")]
pub(crate) mod namespace;
#[cfg(target_os = "linux")]
pub(crate) mod output;
#[cfg(target_os = "linux")]
pub(crate) mod problem;
mod reservation;
#[cfg(target_os = "linux")]
mod reservation_file;
#[cfg(target_os = "linux")]
mod reservation_inspection;
#[cfg(target_os = "linux")]
mod resolver;
#[cfg(target_os = "linux")]
pub(crate) mod result;
#[cfg(target_os = "linux")]
pub(crate) mod security;
mod types;

pub use types::{
    AccessPolicy, ArtifactKind, ArtifactPresence, CleanupArtifact, CleanupArtifacts, CleanupState,
    CoordinationCleanup, CreationSecurity, DestinationContent, DirectoryRole, Housekeeping,
    HousekeepingArtifact, HousekeepingState, LaterCanonical, LiveLineage, PreviousDestination,
    PrivateOutputAttempt, PublicationAttempt, PublicationPreparationFailure, PublicationProblem,
    PublicationResult, PublicationStatus, UnpublishedTailFacts,
};

#[cfg(all(test, target_os = "linux"))]
#[path = "publication/crash_tests.rs"]
mod crash_tests;
