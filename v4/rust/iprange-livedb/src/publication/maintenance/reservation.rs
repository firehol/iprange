//! Linux private reservation discovery and exact offline removal.

use std::fs::File;
use std::os::unix::fs::MetadataExt;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::publication::namespace::{Directory, Identity, Name, NamespaceError, ScanError};
use crate::publication::reservation::{self as codec, Header, Policy, State};
use crate::validation::LocalFileIdentity;

use super::{
    AbandonedReservationEntry, AbandonedReservationEvidence, AbandonedReservationList,
    AbandonedReservationPhase, AbandonedReservationPolicy, AbandonedReservationSink,
    AbandonedReservationSinkControl, PublicationDigest, PublicationOutputEvidence,
    PublicationTuple,
};

const PREFIX: &[u8] = b".iprange-reservation-";
const SUFFIX: &[u8] = b".tmp";
const ENCODED_ID_LEN: usize = 32;
const POSIX_IDENTITY: u16 = 1;
const OPERATION_LOCK: u64 = 0;
const FILE_SIZE: usize = 2 * PAGE_SIZE;

pub(super) fn list<S: AbandonedReservationSink>(
    path: &Path,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<AbandonedReservationList> {
    cancellation.check()?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    let directory_identity = local(directory.identity());
    let mut entries = 0u64;
    let scan = directory.scan(|bytes| {
        cancellation.check()?;
        let Some(attempt) = decode_name(bytes) else {
            return Ok(());
        };
        let Some(entry) = inspect(&directory, directory_identity, bytes, attempt)? else {
            return Ok(());
        };
        deliver(sink, &entry)?;
        entries = entries
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("reservation artifact entries"))?;
        Ok(())
    });
    match scan {
        Ok(()) => Ok(AbandonedReservationList {
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
            "reservation artifact lost its exact name",
        ))?;
    live_lock::lock_cancellable(&regular.file, OPERATION_LOCK, Mode::Exclusive, cancellation)?;
    directory
        .verify_name(&name, expected_artifact)
        .map_err(cleanup_error)?;
    require_readable_binding(&regular.file, attempt, expected_artifact)?;
    cancellation.check()?;
    if !directory
        .unlink_exact(&name, expected_artifact)
        .map_err(cleanup_error)?
    {
        return Err(Error::CleanupConflict(
            "reservation artifact lost its exact name",
        ));
    }
    if regular.file.metadata()?.nlink() != 0 {
        return Err(Error::CleanupConflict(
            "reservation artifact remained linked after removal",
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
) -> Result<Option<AbandonedReservationEntry>> {
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
    let evidence = read_header(&regular.file)
        .filter(|header| {
            header.attempt_id == attempt && header.reservation_identity == regular.identity.encode()
        })
        .map(evidence);
    let Some(current) = directory.entry(&name).map_err(namespace_error)? else {
        return Ok(None);
    };
    if !current.regular || current.links != 1 || current.identity != regular.identity {
        return Ok(None);
    }
    Ok(Some(AbandonedReservationEntry {
        directory_identity,
        artifact_identity: local(regular.identity),
        publication_attempt_id: attempt,
        evidence,
    }))
}

fn read_header(file: &File) -> Option<Header> {
    if file.metadata().ok()?.len() != FILE_SIZE as u64 {
        return None;
    }
    let mut bytes = [0; FILE_SIZE];
    file_io::read_exact_at(file, &mut bytes, 0).ok()?;
    codec::select(&bytes).ok().map(|selected| selected.header)
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
            "reservation artifact identity or link count changed",
        ));
    }
    Ok(())
}

fn identity(identity: LocalFileIdentity) -> Result<Identity> {
    if identity.kind != POSIX_IDENTITY {
        return Err(Error::InvalidArgument(
            "unsupported reservation identity kind",
        ));
    }
    Identity::decode(identity.bytes).ok_or(Error::InvalidArgument("invalid reservation identity"))
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
        _ => Error::CleanupConflict("reservation artifact ownership changed"),
    }
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::InvalidArgument("invalid reservation artifact name"),
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
