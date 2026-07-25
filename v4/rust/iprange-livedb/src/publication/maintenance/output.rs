//! Linux private publication-output discovery and removal.

use std::fs::File;
use std::os::unix::fs::MetadataExt;
use std::path::Path;

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::publication::namespace::{Directory, Identity, Name, NamespaceError, ScanError};
use crate::publication::output;
use crate::validation::LocalFileIdentity;

use super::{
    AbandonedPublicationTempEntry, AbandonedPublicationTempList, AbandonedPublicationTempSink,
    AbandonedPublicationTempSinkControl, PublicationDigest, PublicationTuple,
};

const PREFIX: &[u8] = b".iprange-publish-";
const SUFFIX: &[u8] = b".tmp";
const ENCODED_ID_LEN: usize = 32;
const POSIX_IDENTITY: u16 = 1;

pub(super) fn list<S: AbandonedPublicationTempSink>(
    path: &Path,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedPublicationTempList> {
    cancellation.check()?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    let directory_identity = local(directory.identity());
    let mut entries = 0u64;
    let scan = directory.scan(|bytes| {
        cancellation.check()?;
        let Some(attempt) = decode_name(bytes) else {
            return Ok(());
        };
        let Some(entry) = inspect(&directory, directory_identity, bytes, attempt, cancellation)?
        else {
            return Ok(());
        };
        deliver(sink, &entry)?;
        entries = entries
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("publication temp entries"))?;
        Ok(())
    });
    match scan {
        Ok(()) => Ok(AbandonedPublicationTempList {
            directory_identity,
            entries,
        }),
        Err(ScanError::Namespace(error)) => Err(namespace_error(error)),
        Err(ScanError::Visitor(error)) => Err(error),
    }
}

pub(super) fn remove(
    path: &Path,
    expected_directory: LocalFileIdentity,
    attempt: [u8; 16],
    expected_artifact: LocalFileIdentity,
    expected_tuple: Option<PublicationTuple>,
    expected_digest: Option<PublicationDigest>,
    cancellation: &CancellationToken,
) -> Result<bool> {
    cancellation.check()?;
    let expected_directory = identity(expected_directory)?;
    let expected_artifact = identity(expected_artifact)?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    require_directory(&directory, expected_directory)?;
    let name = encode_name(attempt)?;
    let Some(found) = directory.entry(&name).map_err(namespace_error)? else {
        durable_absence(&directory, &name)?;
        return Ok(false);
    };
    require_owned(
        found.regular,
        found.links,
        found.identity,
        expected_artifact,
    )?;
    let regular = directory
        .open_regular(&name, true)
        .map_err(namespace_error)?
        .ok_or(Error::CleanupConflict(
            "publication temp lost its exact name",
        ))?;
    live_lock::lock_cancellable(
        &regular.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )?;
    directory
        .verify_name(&name, expected_artifact)
        .map_err(cleanup_error)?;
    let evidence = content_evidence(&regular.file, cancellation)?;
    if evidence != expected_tuple.zip(expected_digest) {
        return Err(Error::CleanupConflict(
            "publication temp content evidence changed",
        ));
    }
    cancellation.check()?;
    if !directory
        .unlink_exact(&name, expected_artifact)
        .map_err(cleanup_error)?
    {
        return Err(Error::CleanupConflict(
            "publication temp lost its exact name",
        ));
    }
    if regular.file.metadata()?.nlink() != 0 {
        return Err(Error::CleanupConflict(
            "publication temp remained linked after removal",
        ));
    }
    durable_absence(&directory, &name)?;
    Ok(true)
}

fn inspect(
    directory: &Directory,
    directory_identity: LocalFileIdentity,
    bytes: &[u8],
    attempt: [u8; 16],
    cancellation: &CancellationToken,
) -> Result<Option<AbandonedPublicationTempEntry>> {
    let name = Name::new(bytes).map_err(namespace_error)?;
    let Some(found) = directory.entry(&name).map_err(namespace_error)? else {
        return Ok(None);
    };
    if !found.regular || found.links != 1 {
        return Ok(None);
    }
    let Some(regular) = directory
        .open_regular(&name, false)
        .map_err(namespace_error)?
    else {
        return Ok(None);
    };
    let evidence = content_evidence(&regular.file, cancellation)?;
    let Some(current) = directory.entry(&name).map_err(namespace_error)? else {
        return Ok(None);
    };
    if !current.regular || current.links != 1 || current.identity != regular.identity {
        return Ok(None);
    }
    let (tuple, digest) = evidence.unzip();
    Ok(Some(AbandonedPublicationTempEntry {
        directory_identity,
        artifact_identity: local(regular.identity),
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
    let bootstrap = match crate::database::bootstrap_file(file, OpenMode::ImmutableReader) {
        Ok(bootstrap) => bootstrap,
        Err(Error::Format(_) | Error::Corrupt(_)) => return Ok(None),
        Err(error) => return Err(error),
    };
    let byte_length = file.metadata()?.len();
    let sha512 =
        output::digest_cancellable(file, byte_length, cancellation).map_err(output_error)?;
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

fn encode_name(attempt: [u8; 16]) -> Result<Name> {
    if attempt == [0; 16] {
        return Err(Error::InvalidArgument(
            "publication attempt id must be nonzero",
        ));
    }
    let mut bytes = Vec::with_capacity(PREFIX.len() + ENCODED_ID_LEN + SUFFIX.len());
    bytes.extend_from_slice(PREFIX);
    for byte in attempt {
        bytes.push(hex(byte >> 4));
        bytes.push(hex(byte & 0x0f));
    }
    bytes.extend_from_slice(SUFFIX);
    Name::new(&bytes).map_err(namespace_error)
}

fn decode_name(bytes: &[u8]) -> Option<[u8; 16]> {
    let encoded = bytes.strip_prefix(PREFIX)?.strip_suffix(SUFFIX)?;
    if encoded.len() != ENCODED_ID_LEN {
        return None;
    }
    let mut attempt = [0; 16];
    for (slot, pair) in attempt.iter_mut().zip(encoded.chunks_exact(2)) {
        *slot = unhex(pair[0])?.checked_mul(16)? + unhex(pair[1])?;
    }
    (attempt != [0; 16]).then_some(attempt)
}

fn durable_absence(directory: &Directory, name: &Name) -> Result<()> {
    directory.sync().map_err(namespace_error)?;
    directory.verify().map_err(namespace_error)?;
    directory.require_absent(name).map_err(cleanup_error)
}

fn require_directory(directory: &Directory, expected: Identity) -> Result<()> {
    if directory.identity() != expected {
        return Err(Error::DirectoryIdentityMismatch);
    }
    Ok(())
}

fn require_owned(regular: bool, links: u64, found: Identity, expected: Identity) -> Result<()> {
    if !regular || links != 1 || found != expected {
        return Err(Error::CleanupConflict(
            "publication temp identity or link count changed",
        ));
    }
    Ok(())
}

fn identity(identity: LocalFileIdentity) -> Result<Identity> {
    if identity.kind != POSIX_IDENTITY {
        return Err(Error::InvalidArgument(
            "unsupported publication identity kind",
        ));
    }
    Identity::decode(identity.bytes).ok_or(Error::InvalidArgument("invalid publication identity"))
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: POSIX_IDENTITY,
        bytes: identity.encode(),
    }
}

fn cleanup_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        _ => Error::CleanupConflict("publication temp ownership changed"),
    }
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::InvalidArgument("invalid publication temp name"),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
            Error::Unsupported("publication directory lacks required local operations")
        }
        _ => Error::CleanupConflict("publication directory entry changed"),
    }
}

fn hex(value: u8) -> u8 {
    if value < 10 {
        b'0' + value
    } else {
        b'a' + value - 10
    }
}

fn unhex(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}
