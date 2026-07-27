//! Portable private publication-output discovery and exact removal.

use std::fs::File;
use std::path::Path;

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
#[cfg(unix)]
use crate::publication::namespace::regular_link_count;
#[cfg(windows)]
use crate::publication::namespace::CREATION_SECURITY_KIND;
use crate::publication::namespace::{
    Directory, Identity, Name, NamespaceError, ScanError, IDENTITY_KIND,
};
use crate::publication::output;
use crate::publication::CleanupState;
#[cfg(windows)]
use crate::publication::CreationSecurity;
#[cfg(windows)]
use crate::publication::{ArtifactKind, DirectoryRole};
use crate::validation::LocalFileIdentity;

use super::{
    AbandonedArtifactRemoval, AbandonedPublicationTempEntry, AbandonedPublicationTempList,
    AbandonedPublicationTempSink, AbandonedPublicationTempSinkControl, Housekeeping,
    PublicationDigest, PublicationProblem, PublicationTuple,
};

const PREFIX: &[u8] = b".iprange-publish-";
const SUFFIX: &[u8] = b".tmp";
const ENCODED_ID_LEN: usize = 32;

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
) -> Result<AbandonedArtifactRemoval> {
    cancellation.check()?;
    if expected_tuple.is_some() != expected_digest.is_some() {
        return Err(Error::InvalidArgument(
            "publication tuple and digest evidence must both be present or absent",
        ));
    }
    let expected_directory = identity(expected_directory)?;
    let expected_artifact = identity(expected_artifact)?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    require_directory(&directory, expected_directory)?;
    let name = encode_name(attempt)?;
    let found = directory.entry(&name).map_err(namespace_error)?;
    #[cfg(windows)]
    if let Some(result) = resume(
        &directory,
        attempt,
        &name,
        expected_artifact,
        expected_tuple.zip(expected_digest),
        found.is_some(),
    ) {
        return Ok(result);
    }
    let Some(found) = found else {
        durable_absence(&directory, &name)?;
        return Ok(removal(false, None, Housekeeping::None, Box::default()));
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
    retire(
        &directory,
        &name,
        regular,
        attempt,
        expected_artifact,
        expected_tuple.zip(expected_digest),
    )
}

#[cfg(unix)]
fn retire(
    directory: &Directory,
    name: &Name,
    regular: crate::publication::namespace::Regular,
    _attempt: [u8; 16],
    expected_artifact: Identity,
    _evidence: Option<(PublicationTuple, PublicationDigest)>,
) -> Result<AbandonedArtifactRemoval> {
    if !directory
        .unlink_exact(name, expected_artifact)
        .map_err(cleanup_error)?
    {
        return Err(Error::CleanupConflict(
            "publication temp lost its exact name",
        ));
    }
    match regular_link_count(&regular.file) {
        Ok(0) => {}
        Ok(_) => {
            return Ok(removal(
                true,
                Some(crate::publication::problem::Problem::cleanup_conflict(
                    "publication temp remained linked after removal",
                )),
                Housekeeping::None,
                Box::default(),
            ))
        }
        Err(error) => {
            return Ok(removal(
                true,
                Some(crate::publication::problem::Problem::namespace(&error)),
                Housekeeping::None,
                Box::default(),
            ))
        }
    }
    if let Err(error) = durable_absence(directory, name) {
        return Ok(removal(
            true,
            Some(crate::publication::problem::Problem::sdk(&error)),
            Housekeeping::None,
            Box::default(),
        ));
    }
    Ok(removal(true, None, Housekeeping::None, Box::default()))
}

#[cfg(windows)]
fn retire(
    directory: &Directory,
    name: &Name,
    regular: crate::publication::namespace::Regular,
    attempt: [u8; 16],
    expected_artifact: Identity,
    evidence: Option<(PublicationTuple, PublicationDigest)>,
) -> Result<AbandonedArtifactRemoval> {
    use crate::publication::gc::{self, Authority};
    use crate::publication::gc_codec::Payload;
    use crate::publication::security;

    let commitment = security::creator_only_commitment(&regular.file).map_err(namespace_error)?;
    let payload = evidence.map(|(tuple, digest)| Payload {
        byte_length: digest.byte_length,
        sha512: digest.sha512,
        database_id: tuple.database_id,
        transaction_id: tuple.transaction_id,
        commit_nonce: tuple.commit_nonce,
    });
    let retired = gc::retire(
        directory,
        Authority {
            attempt_id: attempt,
            ordinal: 0,
            kind: ArtifactKind::PrivateOutput,
            directory_role: DirectoryRole::Destination,
            source_name: name,
            source_file: &regular.file,
            identity: expected_artifact,
            creation_security: CreationSecurity {
                kind: CREATION_SECURITY_KIND,
                commitment,
            },
            payload,
        },
    );
    Ok(removal(
        true,
        retired.problem,
        retired.housekeeping,
        retired
            .visible
            .into_iter()
            .collect::<Vec<_>>()
            .into_boxed_slice(),
    ))
}

#[cfg(windows)]
fn resume(
    directory: &Directory,
    attempt: [u8; 16],
    name: &Name,
    identity: Identity,
    evidence: Option<(PublicationTuple, PublicationDigest)>,
    source_present: bool,
) -> Option<AbandonedArtifactRemoval> {
    use crate::publication::gc::{self, ResumeAuthority};
    use crate::publication::gc_codec::Payload;

    let payload = evidence.map(|(tuple, digest)| Payload {
        byte_length: digest.byte_length,
        sha512: digest.sha512,
        database_id: tuple.database_id,
        transaction_id: tuple.transaction_id,
        commit_nonce: tuple.commit_nonce,
    });
    match gc::resume(
        directory,
        ResumeAuthority {
            attempt_id: attempt,
            ordinal: 0,
            kind: ArtifactKind::PrivateOutput,
            directory_role: DirectoryRole::Destination,
            source_name: name,
            identity,
            payload,
        },
    ) {
        Ok(Some(retired)) => Some(removal(
            source_present,
            retired.problem,
            retired.housekeeping,
            retired
                .visible
                .into_iter()
                .collect::<Vec<_>>()
                .into_boxed_slice(),
        )),
        Ok(None) => None,
        Err(problem) => Some(removal(
            source_present,
            Some(problem),
            Housekeeping::None,
            Box::default(),
        )),
    }
}

fn removal(
    source_present: bool,
    cause: Option<PublicationProblem>,
    housekeeping: Housekeeping,
    visible_housekeeping: Box<[crate::publication::HousekeepingArtifact]>,
) -> AbandonedArtifactRemoval {
    AbandonedArtifactRemoval {
        source_present,
        cleanup_state: if cause.is_some() {
            CleanupState::ResiduePossible
        } else {
            CleanupState::Clean
        },
        housekeeping,
        visible_housekeeping,
        cause,
    }
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
    if identity.kind != IDENTITY_KIND {
        return Err(Error::InvalidArgument(
            "unsupported publication identity kind",
        ));
    }
    Identity::decode(identity.bytes).ok_or(Error::InvalidArgument("invalid publication identity"))
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
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
