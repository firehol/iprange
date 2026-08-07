//! Windows correctness cleanup through one authenticated inert name.

use std::fs::File;

use crate::mapping::Mapping;

use super::gc_codec::{self, Header, Payload};
use super::gc_name;
use super::namespace::{
    regular_identity, sync_file, Directory, Identity, Name, NamespaceError, ScanError,
    BASENAME_ENCODING_KIND, CREATION_SECURITY_KIND, IDENTITY_KIND,
};
use super::problem::Problem;
use super::security::{self, Profile};
use super::{ArtifactKind, CreationSecurity, DirectoryRole, Housekeeping, HousekeepingArtifact};

#[path = "gc/resolver.rs"]
mod resolver;
#[path = "gc/source.rs"]
mod source;
pub(super) use resolver::{artifact, observe_pair, resolve_existing};
use resolver::{failed, resolve};

pub(crate) struct Authority<'a> {
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
    pub(crate) kind: ArtifactKind,
    pub(crate) directory_role: DirectoryRole,
    pub(crate) source_name: &'a Name,
    pub(crate) source_file: &'a File,
    pub(crate) identity: Identity,
    pub(crate) creation_security: CreationSecurity,
    pub(crate) payload: Option<Payload>,
}

pub(crate) struct Retirement {
    pub(crate) problem: Option<Problem>,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible: Option<HousekeepingArtifact>,
}

pub(crate) struct ResumeAuthority<'a> {
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
    pub(crate) kind: ArtifactKind,
    pub(crate) directory_role: DirectoryRole,
    pub(crate) source_name: &'a Name,
    pub(crate) identity: Identity,
    pub(crate) payload: Option<Payload>,
}

pub(crate) fn retire(directory: &Directory, authority: Authority<'_>) -> Retirement {
    retire_with(directory, authority, false, |_| Ok(()))
}

pub(crate) fn retire_observed(
    directory: &Directory,
    authority: Authority<'_>,
    observer: impl FnMut(&HousekeepingArtifact) -> Result<(), Problem>,
) -> Retirement {
    retire_with(directory, authority, true, observer)
}

fn retire_with(
    directory: &Directory,
    authority: Authority<'_>,
    observe: bool,
    mut observer: impl FnMut(&HousekeepingArtifact) -> Result<(), Problem>,
) -> Retirement {
    let envelope_name = match gc_name::envelope(authority.attempt_id, authority.ordinal) {
        Ok(name) => name,
        Err(error) => {
            return failed(
                directory,
                &authority,
                None,
                None,
                Problem::namespace(&error),
            )
        }
    };
    let inert_name = match gc_name::inert(authority.attempt_id, authority.ordinal) {
        Ok(name) => name,
        Err(error) => {
            return failed(
                directory,
                &authority,
                Some(&envelope_name),
                None,
                Problem::namespace(&error),
            )
        }
    };
    let envelope = match load_or_create(
        directory,
        &authority,
        envelope_name,
        inert_name,
        observe,
        &mut observer,
    ) {
        Ok(envelope) => envelope,
        Err(failure) => {
            return failed(
                directory,
                &authority,
                Some(&failure.envelope_name),
                Some(&failure.inert_name),
                failure.problem,
            )
        }
    };
    resolve(directory, &authority, envelope)
}

pub(crate) fn resume(
    directory: &Directory,
    expected: ResumeAuthority<'_>,
) -> Result<Option<Retirement>, Problem> {
    let envelope_name = gc_name::envelope(expected.attempt_id, expected.ordinal)
        .map_err(|error| Problem::namespace(&error))?;
    let Some(envelope) = open_as(directory, envelope_name, true, expected.kind)? else {
        return Ok(None);
    };
    let header = &envelope.header;
    if header.attempt_id != expected.attempt_id
        || header.ordinal != expected.ordinal
        || header.kind != expected.kind
        || header.directory_role != expected.directory_role
        || envelope.source_name != *expected.source_name
        || header.artifact_identity != expected.identity.encode()
        || header.payload != expected.payload
    {
        return Err(Problem::cleanup_conflict(
            "GC authority does not match the abandoned artifact",
        ));
    }
    Ok(Some(resolve_existing(directory, envelope)))
}

pub(crate) fn require_source_available(
    directory: &Directory,
    attempt_id: [u8; 16],
    ordinal: u32,
    kind: ArtifactKind,
    directory_role: DirectoryRole,
    source_name: &Name,
    identity: Identity,
) -> Result<(), Problem> {
    let envelope_name =
        gc_name::envelope(attempt_id, ordinal).map_err(|error| Problem::namespace(&error))?;
    let Some(envelope) = open_as(directory, envelope_name, false, kind)? else {
        return Ok(());
    };
    let header = &envelope.header;
    if header.attempt_id != attempt_id
        || header.ordinal != ordinal
        || header.kind != kind
        || header.directory_role != directory_role
        || envelope.source_name != *source_name
        || header.artifact_identity != identity.encode()
    {
        return Err(Problem::cleanup_conflict(
            "GC authority conflicts with the retained source",
        ));
    }
    Err(Problem::cleanup_in_progress(
        "retained source is owned by Windows housekeeping",
    ))
}

pub(crate) fn fresh_attempt(
    directory: &Directory,
    source_name: &Name,
    identity: Identity,
    ordinal: u32,
    kind: ArtifactKind,
    directory_role: DirectoryRole,
) -> Result<[u8; 16], Problem> {
    require_unclaimed_source(directory, source_name, identity, kind, directory_role)?;
    loop {
        let attempt = crate::random::nonzero_128().map_err(|error| Problem::sdk(&error))?;
        let envelope =
            gc_name::envelope(attempt, ordinal).map_err(|error| Problem::namespace(&error))?;
        let inert = gc_name::inert(attempt, ordinal).map_err(|error| Problem::namespace(&error))?;
        match (
            directory.require_absent(&envelope),
            directory.require_absent(&inert),
        ) {
            (Ok(()), Ok(())) => return Ok(attempt),
            (Err(NamespaceError::Exists), _) | (_, Err(NamespaceError::Exists)) => continue,
            (Err(error), _) | (_, Err(error)) => return Err(Problem::namespace(&error)),
        }
    }
}

fn require_unclaimed_source(
    directory: &Directory,
    source_name: &Name,
    identity: Identity,
    kind: ArtifactKind,
    directory_role: DirectoryRole,
) -> Result<(), Problem> {
    let mut exact_claims = 0u8;
    let scan = directory.scan(|bytes| {
        let Some(gc_name::Candidate::Envelope(Some((attempt, ordinal)))) =
            gc_name::candidate(bytes)
        else {
            return Ok(());
        };
        let name =
            gc_name::envelope(attempt, ordinal).map_err(|error| Problem::namespace(&error))?;
        let Some(envelope) = open_as(directory, name, false, kind)? else {
            return Ok(());
        };
        if envelope.source_name != *source_name {
            return Ok(());
        }
        if envelope.header.artifact_identity != identity.encode()
            || envelope.header.kind != kind
            || envelope.header.directory_role != directory_role
        {
            return Err(Problem::cleanup_conflict(
                "GC authority conflicts with the retained source",
            ));
        }
        exact_claims = exact_claims
            .checked_add(1)
            .ok_or_else(|| Problem::cleanup_conflict("duplicate GC source authority"))?;
        Ok(())
    });
    match scan {
        Ok(()) if exact_claims == 0 => Ok(()),
        Ok(()) if exact_claims == 1 => Err(Problem::cleanup_in_progress(
            "retained source is owned by Windows housekeeping",
        )),
        Ok(()) => Err(Problem::cleanup_conflict("duplicate GC source authority")),
        Err(ScanError::Namespace(error)) => Err(Problem::namespace(&error)),
        Err(ScanError::Visitor(problem)) => Err(problem),
    }
}

pub(super) struct Envelope {
    pub(super) name: Name,
    pub(super) source_name: Name,
    pub(super) inert_name: Name,
    pub(super) identity: Identity,
    pub(super) header: Header,
}

struct EnvelopeFailure {
    envelope_name: Name,
    inert_name: Name,
    problem: Problem,
}

fn load_or_create(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope_name: Name,
    inert_name: Name,
    observe: bool,
    observer: &mut impl FnMut(&HousekeepingArtifact) -> Result<(), Problem>,
) -> Result<Envelope, EnvelopeFailure> {
    let opened = directory
        .open_regular(&envelope_name, true)
        .map_err(|error| Problem::namespace(&error));
    let result = match opened {
        Ok(Some(regular)) => checkpoint_envelope(
            directory,
            authority,
            &envelope_name,
            regular.identity,
            &inert_name,
            observe,
            observer,
        )
        .and_then(|()| {
            load(
                directory,
                envelope_name.clone(),
                regular.file,
                regular.identity,
                Some(authority.kind),
            )
        })
        .and_then(|envelope| verify_authority(directory, authority, &envelope).map(|()| envelope)),
        Ok(None) => create(
            directory,
            authority,
            envelope_name.clone(),
            inert_name.clone(),
            observe,
            observer,
        ),
        Err(problem) => Err(problem),
    };
    result.map_err(|problem| EnvelopeFailure {
        envelope_name,
        inert_name,
        problem,
    })
}

fn create(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope_name: Name,
    inert_name: Name,
    observe: bool,
    observer: &mut impl FnMut(&HousekeepingArtifact) -> Result<(), Problem>,
) -> Result<Envelope, Problem> {
    verify_source(directory, authority)?;
    directory
        .require_absent(&inert_name)
        .map_err(|error| Problem::namespace(&error))?;
    let profile = Profile::capture().map_err(|error| Problem::namespace(&error))?;
    if authority.creation_security.kind != CREATION_SECURITY_KIND
        || authority.creation_security.commitment != profile.commitment()
    {
        return Err(Problem::cleanup_conflict(
            "GC source access policy no longer matches the effective user",
        ));
    }
    let file = directory
        .create(&envelope_name, &profile)
        .map_err(|error| Problem::namespace(&error))?;
    let identity = regular_identity(&file, directory.identity())
        .map_err(|error| Problem::namespace(&error))?;
    security::secure_creator_only(&file, &profile).map_err(|error| Problem::namespace(&error))?;
    checkpoint_envelope(
        directory,
        authority,
        &envelope_name,
        identity,
        &inert_name,
        observe,
        observer,
    )?;
    let header = header(directory, authority, &inert_name);
    file.set_len(gc_codec::FILE_SIZE as u64)
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?;
    let mut mapping = Mapping::read_write_view(&file, gc_codec::FILE_SIZE as u64)
        .map_err(|error| Problem::sdk(&error))?;
    let _probe = crate::worker::enter_artifact(&mapping, authority.kind)
        .map_err(|error| Problem::sdk(&error))?;
    header
        .encode(
            &mut mapping
                .page_mut(0, 2)
                .map_err(|error| Problem::sdk(&error))?,
        )
        .map_err(|error| Problem::sdk(&error))?;
    header
        .encode(
            &mut mapping
                .page_mut(1, 2)
                .map_err(|error| Problem::sdk(&error))?,
        )
        .map_err(|error| Problem::sdk(&error))?;
    mapping
        .flush_range(0, gc_codec::FILE_SIZE as u64)
        .map_err(|error| Problem::sdk(&error))?;
    sync_file(&file).map_err(|error| Problem::sdk(&error.into()))?;
    directory
        .sync()
        .and_then(|()| directory.verify())
        .map_err(|error| Problem::namespace(&error))?;
    let envelope = load(
        directory,
        envelope_name,
        file,
        identity,
        Some(authority.kind),
    )?;
    verify_authority(directory, authority, &envelope)?;
    if envelope.inert_name != inert_name {
        return Err(Problem::cleanup_conflict(
            "GC inert name changed during envelope creation",
        ));
    }
    Ok(envelope)
}

fn checkpoint_envelope(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope_name: &Name,
    envelope_identity: Identity,
    inert_name: &Name,
    enabled: bool,
    observer: &mut impl FnMut(&HousekeepingArtifact) -> Result<(), Problem>,
) -> Result<(), Problem> {
    if !enabled {
        return Ok(());
    }
    let artifact = resolver::pending_artifact(
        directory,
        authority,
        envelope_name,
        envelope_identity,
        inert_name,
    );
    observer(&artifact)
}

pub(super) fn open(
    directory: &Directory,
    envelope_name: Name,
    writable: bool,
) -> Result<Option<Envelope>, Problem> {
    let Some(regular) = directory
        .open_regular(&envelope_name, writable)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    load(
        directory,
        envelope_name,
        regular.file,
        regular.identity,
        None,
    )
    .map(Some)
}

fn open_as(
    directory: &Directory,
    envelope_name: Name,
    writable: bool,
    kind: ArtifactKind,
) -> Result<Option<Envelope>, Problem> {
    let Some(regular) = directory
        .open_regular(&envelope_name, writable)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    load(
        directory,
        envelope_name,
        regular.file,
        regular.identity,
        Some(kind),
    )
    .map(Some)
}

fn load(
    directory: &Directory,
    envelope_name: Name,
    file: File,
    identity: Identity,
    kind: Option<ArtifactKind>,
) -> Result<Envelope, Problem> {
    directory
        .verify_name(&envelope_name, identity)
        .map_err(|error| Problem::namespace(&error))?;
    let length = file
        .metadata()
        .map_err(|error| Problem::sdk(&error.into()))?
        .len();
    if length != gc_codec::FILE_SIZE as u64 {
        return Err(Problem::cleanup_conflict(
            "GC authority envelope has the wrong length",
        ));
    }
    let mapping = Mapping::read_only_view(&file, gc_codec::FILE_SIZE as u64)
        .map_err(|error| Problem::sdk(&error))?;
    let _probe = kind
        .map(|kind| crate::worker::enter_artifact(&mapping, kind))
        .transpose()
        .map_err(|error| Problem::sdk(&error))?;
    let bytes = mapping
        .bytes(0, gc_codec::FILE_SIZE)
        .map_err(|error| Problem::sdk(&error))?;
    let header = gc_codec::select(bytes)
        .map_err(|_| Problem::cleanup_conflict("GC authority envelope is not selectable"))?;
    let (attempt_id, ordinal) = gc_name::decode_envelope(envelope_name.bytes())
        .ok_or_else(|| Problem::cleanup_conflict("GC envelope name is not canonical"))?;
    let inert_name =
        gc_name::inert(attempt_id, ordinal).map_err(|error| Problem::namespace(&error))?;
    let source_name =
        Name::from_encoded(&header.source_basename).map_err(|error| Problem::namespace(&error))?;
    verify_record(
        directory,
        &file,
        attempt_id,
        ordinal,
        &source_name,
        &inert_name,
        &header,
    )?;
    Ok(Envelope {
        name: envelope_name,
        source_name,
        inert_name,
        identity,
        header,
    })
}

fn header(directory: &Directory, authority: &Authority<'_>, inert: &Name) -> Header {
    Header {
        kind: authority.kind,
        basename_encoding: BASENAME_ENCODING_KIND,
        attempt_id: authority.attempt_id,
        ordinal: authority.ordinal,
        directory_identity_kind: IDENTITY_KIND,
        artifact_identity_kind: IDENTITY_KIND,
        directory_identity: directory.identity().encode(),
        source_commitment: gc_codec::source_commitment(
            BASENAME_ENCODING_KIND,
            authority.source_name.bytes(),
        ),
        inert_commitment: gc_codec::inert_commitment(BASENAME_ENCODING_KIND, inert.bytes()),
        artifact_identity: authority.identity.encode(),
        payload: authority.payload,
        creation_security_kind: authority.creation_security.kind,
        directory_role: authority.directory_role,
        creation_security_commitment: authority.creation_security.commitment,
        source_basename: authority.source_name.bytes().into(),
        sequence: 1,
    }
}

fn verify_authority(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope: &Envelope,
) -> Result<(), Problem> {
    let header = &envelope.header;
    if header.kind != authority.kind
        || header.basename_encoding != BASENAME_ENCODING_KIND
        || header.attempt_id != authority.attempt_id
        || header.ordinal != authority.ordinal
        || header.directory_identity_kind != IDENTITY_KIND
        || header.artifact_identity_kind != IDENTITY_KIND
        || header.directory_identity != directory.identity().encode()
        || header.artifact_identity != authority.identity.encode()
        || envelope.source_name != *authority.source_name
        || header.source_commitment
            != gc_codec::source_commitment(BASENAME_ENCODING_KIND, authority.source_name.bytes())
        || header.inert_commitment
            != gc_codec::inert_commitment(BASENAME_ENCODING_KIND, envelope.inert_name.bytes())
        || header.payload != authority.payload
        || header.creation_security_kind != authority.creation_security.kind
        || header.directory_role != authority.directory_role
        || header.creation_security_commitment != authority.creation_security.commitment
    {
        return Err(Problem::cleanup_conflict(
            "GC authority envelope does not match the retained artifact",
        ));
    }
    Ok(())
}

fn verify_record(
    directory: &Directory,
    envelope_file: &File,
    attempt_id: [u8; 16],
    ordinal: u32,
    source_name: &Name,
    inert_name: &Name,
    header: &Header,
) -> Result<(), Problem> {
    if header.basename_encoding != BASENAME_ENCODING_KIND
        || header.attempt_id != attempt_id
        || header.ordinal != ordinal
        || header.directory_identity_kind != IDENTITY_KIND
        || header.artifact_identity_kind != IDENTITY_KIND
        || header.directory_identity != directory.identity().encode()
        || Identity::decode(header.artifact_identity).is_none()
        || header.source_commitment
            != gc_codec::source_commitment(BASENAME_ENCODING_KIND, source_name.bytes())
        || header.inert_commitment
            != gc_codec::inert_commitment(BASENAME_ENCODING_KIND, inert_name.bytes())
        || header.creation_security_kind != CREATION_SECURITY_KIND
        || !source::role_matches(header.kind, header.directory_role)
        || !source::name_matches(header.kind, attempt_id, ordinal, source_name)
    {
        return Err(Problem::cleanup_conflict(
            "GC authority envelope does not match its names or directory",
        ));
    }
    let commitment = security::creator_only_commitment(envelope_file)
        .map_err(|error| Problem::namespace(&error))?;
    if commitment != header.creation_security_commitment {
        return Err(Problem::cleanup_conflict(
            "GC authority envelope access policy changed",
        ));
    }
    Ok(())
}

fn verify_source(directory: &Directory, authority: &Authority<'_>) -> Result<(), Problem> {
    let identity = regular_identity(authority.source_file, directory.identity())
        .map_err(|error| Problem::namespace(&error))?;
    if identity != authority.identity {
        return Err(Problem::cleanup_conflict(
            "GC source handle identity changed",
        ));
    }
    directory
        .verify_name(authority.source_name, authority.identity)
        .map_err(|error| Problem::namespace(&error))
}
