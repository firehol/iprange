//! Shared authentication, result, and namespace helpers.

use std::fs::File;

use crate::error::{Error, Result};
use crate::file_io;
use crate::publication::namespace::{Directory, Identity, Name, NamespaceError, IDENTITY_KIND};
use crate::publication::{
    AbandonedArtifactRemoval, CleanupState, Housekeeping, PublicationProblem,
};
use crate::recovery::scratch::format::{decode_header, DecodedHeader, HEADER_SIZE};
use crate::validation::LocalFileIdentity;

use super::{
    AbandonedScratchAuthentication, AbandonedScratchEntry, AbandonedScratchSink,
    AbandonedScratchSinkControl, ScratchOwnerKind,
};

pub(super) fn authenticate(
    file: &File,
    parsed: ([u8; 16], u32),
) -> Result<AbandonedScratchAuthentication> {
    let mut bytes = [0; HEADER_SIZE as usize];
    let header = match file_io::read_exact_at(file, &mut bytes, 0) {
        Ok(()) => decode_header(&bytes),
        Err(Error::Corrupt(_)) => None,
        Err(cause) => return Err(cause),
    };
    let Some(header) = header else {
        return Ok(AbandonedScratchAuthentication::Unauthenticated);
    };
    if !matches_name(header, parsed) {
        return Ok(AbandonedScratchAuthentication::Unauthenticated);
    }
    Ok(AbandonedScratchAuthentication::Authenticated(owner(
        header.owner_kind,
    )?))
}

pub(super) fn require_header(
    file: &File,
    attempt: [u8; 16],
    ordinal: u32,
) -> Result<DecodedHeader> {
    let mut bytes = [0; HEADER_SIZE as usize];
    file_io::read_exact_at(file, &mut bytes, 0)?;
    let header = decode_header(&bytes).ok_or(Error::CleanupConflict(
        "abandoned scratch header is unauthenticated",
    ))?;
    if !matches_name(header, (attempt, ordinal)) {
        return Err(Error::CleanupConflict(
            "abandoned scratch header is unauthenticated",
        ));
    }
    Ok(header)
}

pub(super) fn entry(
    directory_identity: LocalFileIdentity,
    artifact_identity: LocalFileIdentity,
    parsed: ([u8; 16], u32),
    authentication: AbandonedScratchAuthentication,
) -> AbandonedScratchEntry {
    AbandonedScratchEntry {
        directory_identity,
        artifact_identity,
        attempt_id: parsed.0,
        ordinal: parsed.1,
        authentication,
    }
}

pub(super) fn deliver<S: AbandonedScratchSink>(
    sink: &mut S,
    entry: &AbandonedScratchEntry,
) -> Result<()> {
    match sink.entry(entry) {
        Ok(AbandonedScratchSinkControl::Continue) => Ok(()),
        Ok(AbandonedScratchSinkControl::Stop) => Err(Error::StoppedBySink),
        Err(cause) => Err(Error::SinkFailed(Box::new(cause))),
    }
}

pub(super) fn durable_absence(directory: &Directory, name: &Name) -> Result<()> {
    directory.sync().map_err(namespace_error)?;
    directory.verify().map_err(namespace_error)?;
    directory.require_absent(name).map_err(cleanup_error)
}

pub(super) fn require_directory(directory: &Directory, expected: Identity) -> Result<()> {
    if directory.identity() != expected {
        return Err(Error::DirectoryIdentityMismatch);
    }
    Ok(())
}

pub(super) fn require_owned(
    regular: bool,
    links: u64,
    found: Identity,
    expected: Identity,
) -> Result<()> {
    if !regular || links != 1 || found != expected {
        return Err(Error::CleanupConflict(
            "abandoned scratch identity or link count changed",
        ));
    }
    Ok(())
}

pub(super) fn removal(
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

pub(super) fn identity(identity: LocalFileIdentity) -> Result<Identity> {
    if identity.kind != IDENTITY_KIND {
        return Err(Error::InvalidArgument(
            "unsupported abandoned scratch identity kind",
        ));
    }
    Identity::decode(identity.bytes)
        .ok_or(Error::InvalidArgument("invalid abandoned scratch identity"))
}

pub(super) fn cleanup_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        _ => Error::CleanupConflict("abandoned scratch ownership changed"),
    }
}

pub(super) fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::InvalidArgument("invalid abandoned scratch name"),
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
            Error::Unsupported("scratch directory lacks required local operations")
        }
        _ => Error::CleanupConflict("scratch directory entry changed"),
    }
}

fn matches_name(header: DecodedHeader, parsed: ([u8; 16], u32)) -> bool {
    header.attempt_id == parsed.0 && header.ordinal == parsed.1
}

fn owner(value: u16) -> Result<ScratchOwnerKind> {
    match value {
        1 => Ok(ScratchOwnerKind::Validation),
        2 => Ok(ScratchOwnerKind::Recovery),
        _ => Err(Error::Corrupt("scratch owner kind is invalid")),
    }
}
