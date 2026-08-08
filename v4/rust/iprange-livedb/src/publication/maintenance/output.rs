//! Portable private publication-output discovery and exact removal.

use std::fs::File;
use std::path::Path;

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;
#[cfg(windows)]
use crate::publication::gc_codec::Payload;
use crate::publication::namespace::{local_identity as local, Directory};
use crate::publication::output;
use crate::publication::ArtifactKind;
use crate::validation::LocalFileIdentity;

use super::common::Artifact;
use super::{
    AbandonedArtifactRemoval, AbandonedPublicationTempEntry, AbandonedPublicationTempList,
    AbandonedPublicationTempSink, AbandonedPublicationTempSinkControl, PublicationDigest,
    PublicationTuple,
};

const ARTIFACT: Artifact = Artifact::new(
    b".iprange-publish-",
    "invalid publication temp name",
    "unsupported publication identity kind",
    "invalid publication identity",
    "publication temp identity or link count changed",
    "publication temp ownership changed",
    "publication temp lost its exact name",
    "publication temp remained linked after removal",
);

pub(super) fn list<S: AbandonedPublicationTempSink>(
    path: &Path,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedPublicationTempList> {
    let (directory_identity, entries) = ARTIFACT.scan(
        path,
        cancellation,
        "publication temp entries",
        |directory, directory_identity, bytes, attempt| {
            let Some(entry) = inspect(directory, directory_identity, bytes, attempt, cancellation)?
            else {
                return Ok(false);
            };
            deliver(sink, &entry)?;
            Ok(true)
        },
    )?;
    Ok(AbandonedPublicationTempList {
        directory_identity,
        entries,
    })
}

pub(super) fn remove(
    path: &Path,
    expected_directory: LocalFileIdentity,
    attempt: [u8; 16],
    expected_artifact: LocalFileIdentity,
    expected_tuple: Option<PublicationTuple>,
    expected_digest: Option<PublicationDigest>,
    cancellation: &CancellationToken,
) -> Result<AbandonedArtifactRemoval> {
    cancellation.check()?;
    if expected_tuple.is_some() != expected_digest.is_some() {
        return Err(Error::InvalidArgument(
            "publication tuple and digest evidence must both be present or absent",
        ));
    }
    let expected = expected_tuple.zip(expected_digest);
    #[cfg(windows)]
    let payload = expected.map(|(tuple, digest)| Payload {
        byte_length: digest.byte_length,
        sha512: digest.sha512,
        database_id: tuple.database_id,
        transaction_id: tuple.transaction_id,
        commit_nonce: tuple.commit_nonce,
    });
    #[cfg(not(windows))]
    let payload = ();
    ARTIFACT.remove(
        path,
        expected_directory,
        attempt,
        expected_artifact,
        MAIN_LIFETIME_LOCK,
        cancellation,
        0,
        ArtifactKind::PrivateOutput,
        payload,
        |file, _| {
            if content_evidence(file, cancellation)? != expected {
                return Err(Error::CleanupConflict(
                    "publication temp content evidence changed",
                ));
            }
            Ok(())
        },
    )
}

fn inspect(
    directory: &Directory,
    directory_identity: LocalFileIdentity,
    bytes: &[u8],
    attempt: [u8; 16],
    cancellation: &CancellationToken,
) -> Result<Option<AbandonedPublicationTempEntry>> {
    let Some((identity, evidence)) = ARTIFACT.inspect_stable(directory, bytes, |file, _| {
        content_evidence(file, cancellation)
    })?
    else {
        return Ok(None);
    };
    let (tuple, digest) = evidence.unzip();
    Ok(Some(AbandonedPublicationTempEntry {
        directory_identity,
        artifact_identity: local(identity),
        publication_attempt_id: attempt,
        tuple,
        digest,
    }))
}

fn content_evidence(
    file: &File,
    cancellation: &CancellationToken,
) -> Result<Option<(PublicationTuple, PublicationDigest)>> {
    cancellation.check()?;
    let byte_length = file.metadata()?.len();
    if byte_length < (2 * PAGE_SIZE) as u64 || byte_length % PAGE_SIZE as u64 != 0 {
        return Ok(None);
    }
    let mapping = Mapping::read_only_view(file, byte_length)?;
    let bootstrap = match crate::database::bootstrap_mapping(
        &mapping,
        byte_length,
        OpenMode::ImmutableReader,
    ) {
        Ok(bootstrap) => bootstrap,
        Err(Error::Format(_) | Error::Corrupt(_)) => return Ok(None),
        Err(error) => return Err(error),
    };
    let sha512 =
        output::digest_cancellable(&mapping, byte_length, cancellation).map_err(output_error)?;
    let meta = bootstrap.meta;
    Ok(Some((
        PublicationTuple {
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
        },
        PublicationDigest {
            byte_length,
            sha512,
        },
    )))
}

fn output_error(error: output::Error) -> Error {
    match error {
        output::Error::Sdk(error) => error,
        _ => Error::CleanupConflict("publication temp changed while hashing"),
    }
}

fn deliver<S: AbandonedPublicationTempSink>(
    sink: &mut S,
    entry: &AbandonedPublicationTempEntry,
) -> Result<()> {
    match sink.entry(entry) {
        Ok(AbandonedPublicationTempSinkControl::Continue) => Ok(()),
        Ok(AbandonedPublicationTempSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(error) => Err(Error::SinkFailed(Box::new(error))),
    }
}
