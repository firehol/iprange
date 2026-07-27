//! Explicit offline maintenance for publication-private artifacts.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::Result;
use crate::validation::LocalFileIdentity;

use super::{Housekeeping, HousekeepingArtifact, PublicationProblem};

/// Logical generation recorded in one complete v4 output.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PublicationTuple {
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
}

/// Exact complete-file digest evidence.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PublicationDigest {
    pub byte_length: u64,
    pub sha512: [u8; 64],
}

/// Publication policy recorded in an authenticated private reservation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedReservationPolicy {
    FailIfExists,
    ReplaceExisting,
    ReplaceExistingNoRollback,
}

/// Durable namespace phase recorded in a private reservation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedReservationPhase {
    Prepared,
    MainMayHaveBeenAttempted,
}

/// Exact attempted-output identity and content evidence.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PublicationOutputEvidence {
    pub identity: LocalFileIdentity,
    pub tuple: PublicationTuple,
    pub digest: PublicationDigest,
}

/// Authenticated fields from one selectable private reservation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedReservationEvidence {
    pub policy: AbandonedReservationPolicy,
    pub phase: AbandonedReservationPhase,
    pub output: PublicationOutputEvidence,
    pub previous: Option<(LocalFileIdentity, PublicationDigest)>,
}

/// One stable exact-pattern private reservation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedReservationEntry {
    pub directory_identity: LocalFileIdentity,
    pub artifact_identity: LocalFileIdentity,
    pub publication_attempt_id: [u8; 16],
    pub evidence: Option<AbandonedReservationEvidence>,
}

/// Completed constant-memory private-reservation scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedReservationList {
    pub directory_identity: LocalFileIdentity,
    pub entries: u64,
}

/// Sink response for one borrowed private-reservation entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedReservationSinkControl {
    Continue,
    Stop,
}

/// Synchronous consumer for private publication reservations.
pub trait AbandonedReservationSink {
    fn entry(
        &mut self,
        entry: &AbandonedReservationEntry,
    ) -> Result<AbandonedReservationSinkControl>;
}

impl<F> AbandonedReservationSink for F
where
    F: FnMut(&AbandonedReservationEntry) -> Result<AbandonedReservationSinkControl>,
{
    fn entry(
        &mut self,
        entry: &AbandonedReservationEntry,
    ) -> Result<AbandonedReservationSinkControl> {
        self(entry)
    }
}

/// One stable exact-pattern private publication output.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedPublicationTempEntry {
    pub directory_identity: LocalFileIdentity,
    pub artifact_identity: LocalFileIdentity,
    pub publication_attempt_id: [u8; 16],
    pub tuple: Option<PublicationTuple>,
    pub digest: Option<PublicationDigest>,
}

/// Completed constant-memory private-output scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AbandonedPublicationTempList {
    pub directory_identity: LocalFileIdentity,
    pub entries: u64,
}

/// Sink response for one borrowed private-output entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AbandonedPublicationTempSinkControl {
    Continue,
    Stop,
}

/// Synchronous consumer for private publication outputs.
pub trait AbandonedPublicationTempSink {
    fn entry(
        &mut self,
        entry: &AbandonedPublicationTempEntry,
    ) -> Result<AbandonedPublicationTempSinkControl>;
}

/// Kind of exact Windows housekeeping name found during an offline scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WindowsHousekeepingCandidateKind {
    Envelope,
    InertPayload,
}

/// One exact Windows housekeeping candidate.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WindowsHousekeepingEntry {
    pub directory_identity: LocalFileIdentity,
    pub candidate_kind: WindowsHousekeepingCandidateKind,
    pub basename_encoding: u16,
    pub basename: Box<[u8]>,
    pub identity: Option<LocalFileIdentity>,
    pub attempt_id: Option<[u8; 16]>,
    pub ordinal: Option<u32>,
    pub artifact: Option<HousekeepingArtifact>,
    pub problem: Option<PublicationProblem>,
}

/// Completed constant-memory Windows housekeeping scan.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct WindowsHousekeepingList {
    pub directory_identity: LocalFileIdentity,
    pub entries: u64,
}

/// Sink response for one borrowed Windows housekeeping candidate.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WindowsHousekeepingSinkControl {
    Continue,
    Stop,
}

/// Synchronous consumer for Windows housekeeping candidates.
pub trait WindowsHousekeepingSink {
    fn entry(&mut self, entry: &WindowsHousekeepingEntry)
        -> Result<WindowsHousekeepingSinkControl>;
}

impl<F> WindowsHousekeepingSink for F
where
    F: FnMut(&WindowsHousekeepingEntry) -> Result<WindowsHousekeepingSinkControl>,
{
    fn entry(
        &mut self,
        entry: &WindowsHousekeepingEntry,
    ) -> Result<WindowsHousekeepingSinkControl> {
        self(entry)
    }
}

/// Optional exact content evidence supplied to housekeeping removal.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HousekeepingPayloadIdentity {
    pub tuple: Option<PublicationTuple>,
    pub digest: PublicationDigest,
}

/// Factual terminal state from one Windows housekeeping removal.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WindowsHousekeepingRemoval {
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<PublicationProblem>,
}

impl<F> AbandonedPublicationTempSink for F
where
    F: FnMut(&AbandonedPublicationTempEntry) -> Result<AbandonedPublicationTempSinkControl>,
{
    fn entry(
        &mut self,
        entry: &AbandonedPublicationTempEntry,
    ) -> Result<AbandonedPublicationTempSinkControl> {
        self(entry)
    }
}

/// Stream exact Windows GC candidates without granting deletion authority.
pub fn list_windows_housekeeping<S: WindowsHousekeepingSink>(
    directory: impl AsRef<Path>,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<WindowsHousekeepingList> {
    #[cfg(windows)]
    {
        super::gc_maintenance::list(directory.as_ref(), cancellation, sink)
    }
    #[cfg(not(windows))]
    {
        let _ = (directory, cancellation, sink);
        Err(crate::error::Error::Unsupported(
            "Windows housekeeping is unavailable on this platform",
        ))
    }
}

/// Resolve and best-effort remove one exact authenticated Windows GC pair.
pub fn remove_windows_housekeeping(
    directory: impl AsRef<Path>,
    expected_directory_identity: LocalFileIdentity,
    attempt_id: [u8; 16],
    ordinal: u32,
    expected_envelope_identity: LocalFileIdentity,
    expected_payload_identity: Option<HousekeepingPayloadIdentity>,
    cancellation: &CancellationToken,
) -> Result<WindowsHousekeepingRemoval> {
    #[cfg(windows)]
    {
        super::gc_maintenance::remove(
            directory.as_ref(),
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_envelope_identity,
            expected_payload_identity,
            cancellation,
        )
    }
    #[cfg(not(windows))]
    {
        let _ = (
            directory,
            expected_directory_identity,
            attempt_id,
            ordinal,
            expected_envelope_identity,
            expected_payload_identity,
            cancellation,
        );
        Err(crate::error::Error::Unsupported(
            "Windows housekeeping is unavailable on this platform",
        ))
    }
}

/// List stable no-follow regular private publication outputs.
pub fn list_abandoned_publication_temps<S: AbandonedPublicationTempSink>(
    directory: impl AsRef<Path>,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedPublicationTempList> {
    #[cfg(unix)]
    {
        output::list(directory.as_ref(), cancellation, sink)
    }
    #[cfg(not(unix))]
    {
        let _ = (directory, cancellation, sink);
        Err(crate::error::Error::Unsupported(
            "publication-temp listing is not implemented on this platform",
        ))
    }
}

/// Remove one exact private output after caller-certified quiescence.
///
/// Returns `true` when it was removed and `false` when durable absence was
/// already proved. Readable content requires exact tuple and digest evidence;
/// partial content requires both optional arguments to be absent.
pub fn remove_abandoned_publication_temp(
    directory: impl AsRef<Path>,
    expected_directory_identity: LocalFileIdentity,
    publication_attempt_id: [u8; 16],
    expected_artifact_identity: LocalFileIdentity,
    expected_tuple: Option<PublicationTuple>,
    expected_digest: Option<PublicationDigest>,
    cancellation: &CancellationToken,
) -> Result<bool> {
    #[cfg(unix)]
    {
        output::remove(
            directory.as_ref(),
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            expected_tuple,
            expected_digest,
            cancellation,
        )
    }
    #[cfg(not(unix))]
    {
        let _ = (
            directory,
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            expected_tuple,
            expected_digest,
            cancellation,
        );
        Err(crate::error::Error::Unsupported(
            "publication-temp removal is not implemented on this platform",
        ))
    }
}

/// List stable no-follow regular private publication reservations.
pub fn list_abandoned_reservation_artifacts<S: AbandonedReservationSink>(
    directory: impl AsRef<Path>,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedReservationList> {
    #[cfg(unix)]
    {
        reservation::list(directory.as_ref(), cancellation, sink)
    }
    #[cfg(not(unix))]
    {
        let _ = (directory, cancellation, sink);
        Err(crate::error::Error::Unsupported(
            "reservation listing is not implemented on this platform",
        ))
    }
}

/// Remove one exact private reservation after caller-certified quiescence.
pub fn remove_abandoned_reservation_artifact(
    directory: impl AsRef<Path>,
    expected_directory_identity: LocalFileIdentity,
    publication_attempt_id: [u8; 16],
    expected_artifact_identity: LocalFileIdentity,
    cancellation: &CancellationToken,
) -> Result<bool> {
    #[cfg(unix)]
    {
        reservation::remove(
            directory.as_ref(),
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            cancellation,
        )
    }
    #[cfg(not(unix))]
    {
        let _ = (
            directory,
            expected_directory_identity,
            publication_attempt_id,
            expected_artifact_identity,
            cancellation,
        );
        Err(crate::error::Error::Unsupported(
            "reservation removal is not implemented on this platform",
        ))
    }
}

#[cfg(unix)]
#[path = "maintenance/output.rs"]
mod output;
#[cfg(unix)]
#[path = "maintenance/reservation.rs"]
mod reservation;

#[cfg(all(test, target_os = "linux"))]
#[path = "maintenance_tests.rs"]
mod tests;
