//! Portable private reservation discovery and exact offline removal.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::mapping::Mapping;
use crate::publication::namespace::{local_identity as local, Directory, Identity};
use crate::publication::reservation::{self as codec, Header, Policy, State};
use crate::publication::ArtifactKind;
use crate::validation::LocalFileIdentity;

use super::common::Artifact;
use super::{
    AbandonedArtifactRemoval, AbandonedReservationEntry, AbandonedReservationEvidence,
    AbandonedReservationList, AbandonedReservationPhase, AbandonedReservationPolicy,
    AbandonedReservationSink, AbandonedReservationSinkControl, PublicationDigest,
    PublicationOutputEvidence, PublicationTuple,
};

const ARTIFACT: Artifact = Artifact::new(
    b".iprange-reservation-",
    "invalid reservation artifact name",
    "unsupported reservation identity kind",
    "invalid reservation identity",
    "reservation artifact identity or link count changed",
    "reservation artifact ownership changed",
    "reservation artifact lost its exact name",
    "reservation artifact remained linked after removal",
);
const OPERATION_LOCK: u64 = 0;
const FILE_SIZE: usize = 2 * PAGE_SIZE;

pub(super) fn list<S: AbandonedReservationSink>(
    path: &Path,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedReservationList> {
    let (directory_identity, entries) = ARTIFACT.scan(
        path,
        cancellation,
        "reservation artifact entries",
        |directory, directory_identity, bytes, attempt| {
            let Some(entry) = inspect(directory, directory_identity, bytes, attempt)? else {
                return Ok(false);
            };
            deliver(sink, &entry)?;
            Ok(true)
        },
    )?;
    Ok(AbandonedReservationList {
        directory_identity,
        entries,
    })
}

pub(super) fn remove(
    path: &Path,
    expected_directory: LocalFileIdentity,
    attempt: [u8; 16],
    expected_artifact: LocalFileIdentity,
    cancellation: &CancellationToken,
) -> Result<AbandonedArtifactRemoval> {
    cancellation.check()?;
    #[cfg(windows)]
    let payload = None;
    #[cfg(not(windows))]
    let payload = ();
    ARTIFACT.remove(
        path,
        expected_directory,
        attempt,
        expected_artifact,
        OPERATION_LOCK,
        cancellation,
        1,
        ArtifactKind::PrivateReservation,
        payload,
        |file, identity| require_readable_binding(file, attempt, identity),
    )
}

fn inspect(
    directory: &Directory,
    directory_identity: LocalFileIdentity,
    bytes: &[u8],
    attempt: [u8; 16],
) -> Result<Option<AbandonedReservationEntry>> {
    let Some((identity, evidence)) =
        ARTIFACT.inspect_stable(directory, bytes, |file, identity| {
            Ok(read_header(file)
                .filter(|header| {
                    header.attempt_id == attempt && header.reservation_identity == identity.encode()
                })
                .map(evidence))
        })?
    else {
        return Ok(None);
    };
    Ok(Some(AbandonedReservationEntry {
        directory_identity,
        artifact_identity: local(identity),
        publication_attempt_id: attempt,
        evidence,
    }))
}

fn read_header(file: &File) -> Option<Header> {
    if file.metadata().ok()?.len() != FILE_SIZE as u64 {
        return None;
    }
    let mapping = Mapping::read_only_view(file, FILE_SIZE as u64).ok()?;
    let bytes = mapping.bytes(0, FILE_SIZE).ok()?;
    codec::select(bytes).ok().map(|selected| selected.header)
}

fn require_readable_binding(file: &File, attempt: [u8; 16], identity: Identity) -> Result<()> {
    if let Some(header) = read_header(file) {
        if header.attempt_id != attempt || header.reservation_identity != identity.encode() {
            return Err(Error::CleanupConflict(
                "readable reservation is not bound to its name and inode",
            ));
        }
    }
    Ok(())
}

fn evidence(header: Header) -> AbandonedReservationEvidence {
    let tuple = PublicationTuple {
        database_id: header.database_id,
        transaction_id: header.transaction_id,
        commit_nonce: header.commit_nonce,
    };
    AbandonedReservationEvidence {
        policy: match header.policy {
            Policy::FailIfExists => AbandonedReservationPolicy::FailIfExists,
            Policy::ReplaceExisting => AbandonedReservationPolicy::ReplaceExisting,
            Policy::ReplaceExistingNoRollback => {
                AbandonedReservationPolicy::ReplaceExistingNoRollback
            }
        },
        phase: match header.state {
            State::Prepared => AbandonedReservationPhase::Prepared,
            State::MainMayHaveBeenAttempted => AbandonedReservationPhase::MainMayHaveBeenAttempted,
        },
        output: PublicationOutputEvidence {
            identity: local(
                Identity::decode(header.output_identity)
                    .expect("selected output identity is valid"),
            ),
            tuple,
            digest: PublicationDigest {
                byte_length: header.output_byte_length,
                sha512: header.output_sha512,
            },
        },
        previous: header.previous.map(|previous| {
            (
                local(
                    Identity::decode(previous.identity)
                        .expect("selected previous identity is valid"),
                ),
                PublicationDigest {
                    byte_length: previous.byte_length,
                    sha512: previous.sha512,
                },
            )
        }),
    }
}

fn deliver<S: AbandonedReservationSink>(
    sink: &mut S,
    entry: &AbandonedReservationEntry,
) -> Result<()> {
    match sink.entry(entry) {
        Ok(AbandonedReservationSinkControl::Continue) => Ok(()),
        Ok(AbandonedReservationSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(error) => Err(Error::SinkFailed(Box::new(error))),
    }
}
