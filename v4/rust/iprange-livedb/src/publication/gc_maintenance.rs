//! Explicit discovery and removal of authenticated Windows GC pairs.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::validation::LocalFileIdentity;

use super::gc;
use super::gc_codec::Payload;
use super::gc_name::{self, Candidate};
use super::maintenance::{
    HousekeepingPayloadIdentity, WindowsHousekeepingCandidateKind, WindowsHousekeepingEntry,
    WindowsHousekeepingList, WindowsHousekeepingRemoval, WindowsHousekeepingSink,
    WindowsHousekeepingSinkControl,
};
use super::namespace::{
    identity_from_local, local_identity as local, Directory, Identity, Name, NamespaceError,
    ScanError, BASENAME_ENCODING_KIND, IDENTITY_KIND,
};
use super::problem::Problem;
use super::{Housekeeping, HousekeepingState};

pub(super) fn list<S: WindowsHousekeepingSink>(
    path: &Path,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> Result<WindowsHousekeepingList> {
    cancellation.check()?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    let directory_identity = local(directory.identity());
    let mut entries = 0u64;
    let scan = directory.scan(|bytes| {
        cancellation.check()?;
        let Some(candidate) = gc_name::candidate(bytes) else {
            return Ok(());
        };
        let entry = inspect(&directory, directory_identity, bytes, candidate);
        deliver(sink, &entry)?;
        entries = entries
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("Windows housekeeping entries"))?;
        Ok(())
    });
    match scan {
        Ok(()) => Ok(WindowsHousekeepingList {
            directory_identity,
            entries,
        }),
        Err(ScanError::Namespace(error)) => Err(namespace_error(error)),
        Err(ScanError::Visitor(error)) => Err(error),
    }
}

#[allow(clippy::too_many_arguments)]
pub(super) fn remove(
    path: &Path,
    expected_directory: LocalFileIdentity,
    attempt_id: [u8; 16],
    ordinal: u32,
    expected_envelope: LocalFileIdentity,
    expected_payload: Option<HousekeepingPayloadIdentity>,
    cancellation: &CancellationToken,
) -> Result<WindowsHousekeepingRemoval> {
    cancellation.check()?;
    if attempt_id == [0; 16] {
        return Err(Error::InvalidArgument(
            "housekeeping attempt id must be nonzero",
        ));
    }
    let expected_directory = identity(expected_directory)?;
    let expected_envelope = identity(expected_envelope)?;
    let expected_payload = expected_payload.map(payload).transpose()?;
    let directory = Directory::open(path).map_err(namespace_error)?;
    if directory.identity() != expected_directory {
        return Err(Error::DirectoryIdentityMismatch);
    }
    let envelope_name = gc_name::envelope(attempt_id, ordinal).map_err(namespace_error)?;
    let Some(envelope) =
        gc::open(&directory, envelope_name.clone(), true).map_err(problem_error)?
    else {
        directory.verify().map_err(namespace_error)?;
        directory
            .require_absent(&envelope_name)
            .map_err(cleanup_error)?;
        let inert_name = gc_name::inert(attempt_id, ordinal).map_err(namespace_error)?;
        directory
            .require_absent(&inert_name)
            .map_err(cleanup_error)?;
        return Ok(WindowsHousekeepingRemoval {
            housekeeping: Housekeeping::CrashReappearancePossible,
            visible_housekeeping: Box::new([]),
            cause: None,
        });
    };
    if envelope.identity != expected_envelope {
        return Err(Error::CleanupConflict(
            "GC envelope identity changed before removal",
        ));
    }
    if expected_payload.is_some() && envelope.header.payload != expected_payload {
        return Err(Error::CleanupConflict(
            "GC payload identity changed before removal",
        ));
    }
    cancellation.check()?;
    let retirement = gc::resolve_existing(&directory, envelope);
    Ok(WindowsHousekeepingRemoval {
        housekeeping: retirement.housekeeping,
        visible_housekeeping: retirement.visible.into_iter().collect(),
        cause: retirement.problem,
    })
}

fn inspect(
    directory: &Directory,
    directory_identity: LocalFileIdentity,
    bytes: &[u8],
    candidate: Candidate,
) -> WindowsHousekeepingEntry {
    let basename = encoded_basename(bytes);
    let (candidate_kind, decoded) = match candidate {
        Candidate::Envelope(decoded) => (WindowsHousekeepingCandidateKind::Envelope, decoded),
        Candidate::Inert(decoded) => (WindowsHousekeepingCandidateKind::InertPayload, decoded),
    };
    let identity = Name::new(bytes)
        .ok()
        .and_then(|name| directory.entry(&name).ok().flatten())
        .map(|entry| local(entry.identity));
    let Some((attempt_id, ordinal)) = decoded else {
        return candidate_entry(
            directory_identity,
            candidate_kind,
            &basename,
            identity,
            None,
            None,
            None,
            Some(Problem::cleanup_conflict(
                "Windows housekeeping name is not canonical",
            )),
        );
    };
    let envelope_name = match gc_name::envelope(attempt_id, ordinal) {
        Ok(name) => name,
        Err(error) => {
            return candidate_entry(
                directory_identity,
                candidate_kind,
                &basename,
                identity,
                Some(attempt_id),
                Some(ordinal),
                None,
                Some(Problem::namespace(&error)),
            )
        }
    };
    match gc::open(directory, envelope_name, false) {
        Ok(Some(envelope)) => {
            let observed = gc::observe_pair(directory, &envelope);
            let problem = (observed.state == HousekeepingState::Conflict)
                .then(|| Problem::cleanup_conflict("GC payload names or identities conflict"));
            let artifact = gc::artifact(directory.identity(), &envelope, observed);
            candidate_entry(
                directory_identity,
                candidate_kind,
                &basename,
                identity,
                Some(attempt_id),
                Some(ordinal),
                Some(artifact),
                problem,
            )
        }
        Ok(None) => candidate_entry(
            directory_identity,
            candidate_kind,
            &basename,
            identity,
            Some(attempt_id),
            Some(ordinal),
            None,
            Some(Problem::cleanup_conflict(
                "GC candidate has no authority envelope",
            )),
        ),
        Err(problem) => candidate_entry(
            directory_identity,
            candidate_kind,
            &basename,
            identity,
            Some(attempt_id),
            Some(ordinal),
            None,
            Some(problem),
        ),
    }
}

fn encoded_basename(ascii: &[u8]) -> Box<[u8]> {
    let mut bytes = Vec::with_capacity(ascii.len() * 2);
    for &byte in ascii {
        bytes.extend_from_slice(&u16::from(byte).to_le_bytes());
    }
    bytes.into_boxed_slice()
}

#[allow(clippy::too_many_arguments)]
fn candidate_entry(
    directory_identity: LocalFileIdentity,
    candidate_kind: WindowsHousekeepingCandidateKind,
    basename: &[u8],
    identity: Option<LocalFileIdentity>,
    attempt_id: Option<[u8; 16]>,
    ordinal: Option<u32>,
    artifact: Option<super::HousekeepingArtifact>,
    problem: Option<Problem>,
) -> WindowsHousekeepingEntry {
    WindowsHousekeepingEntry {
        directory_identity,
        candidate_kind,
        basename_encoding: BASENAME_ENCODING_KIND,
        basename: basename.into(),
        identity,
        attempt_id,
        ordinal,
        artifact,
        problem,
    }
}

fn payload(expected: HousekeepingPayloadIdentity) -> Result<Payload> {
    let tuple = expected.tuple;
    if expected.digest.byte_length == 0
        || expected.digest.sha512 == [0; 64]
        || tuple.is_some_and(|tuple| {
            tuple.database_id == [0; 16]
                || tuple.transaction_id == 0
                || tuple.commit_nonce == [0; 16]
        })
    {
        return Err(Error::InvalidArgument(
            "housekeeping payload identity is malformed",
        ));
    }
    Ok(Payload {
        byte_length: expected.digest.byte_length,
        sha512: expected.digest.sha512,
        database_id: tuple.map_or([0; 16], |tuple| tuple.database_id),
        transaction_id: tuple.map_or(0, |tuple| tuple.transaction_id),
        commit_nonce: tuple.map_or([0; 16], |tuple| tuple.commit_nonce),
    })
}

fn deliver<S: WindowsHousekeepingSink>(
    sink: &mut S,
    entry: &WindowsHousekeepingEntry,
) -> Result<()> {
    match sink.entry(entry) {
        Ok(WindowsHousekeepingSinkControl::Continue) => Ok(()),
        Ok(WindowsHousekeepingSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(error) => Err(Error::SinkFailed(Box::new(error))),
    }
}

fn identity(value: LocalFileIdentity) -> Result<Identity> {
    if value.kind != IDENTITY_KIND {
        return Err(Error::InvalidArgument(
            "unsupported Windows housekeeping identity kind",
        ));
    }
    identity_from_local(value).ok_or(Error::InvalidArgument(
        "invalid Windows housekeeping identity",
    ))
}

fn problem_error(problem: Problem) -> Error {
    problem.into_sdk()
}

fn cleanup_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        _ => Error::CleanupConflict("Windows housekeeping ownership changed"),
    }
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::InvalidArgument("invalid Windows housekeeping name"),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
            Error::Unsupported("housekeeping directory lacks required local NTFS operations")
        }
        _ => Error::CleanupConflict("Windows housekeeping directory entry changed"),
    }
}
