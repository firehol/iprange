//! Stable, bounded inspection of publication output bytes.

use std::fs::File;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::error::ErrorCode;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;

use super::namespace::{Destination, Identity, Name, Regular};
use super::output;
use super::problem::Problem;
use super::reservation::Header;
use super::result::AccessPolicy;
use super::{ArtifactKind, DirectoryRole};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Content {
    Desired,
    Other,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Location {
    Main,
    Private,
}

#[derive(Debug)]
pub(super) struct Inspected {
    pub(super) name: Name,
    pub(super) file: File,
    pub(super) mapping: Mapping,
    pub(super) identity: Identity,
    pub(super) meta: MetaV4,
    pub(super) byte_length: u64,
    pub(super) sha512: [u8; 64],
    pub(super) content: Content,
    pub(super) access: AccessPolicy,
    location: Location,
    attempt_id: [u8; 16],
}

impl Inspected {
    pub(super) fn verify(&self, destination: &Destination) -> Result<(), Problem> {
        require_available(
            destination,
            self.location,
            self.attempt_id,
            &self.name,
            self.identity,
        )?;
        let (meta, byte_length) = read_bootstrap(&self.file, &self.mapping)?;
        if (meta, byte_length) != (self.meta, self.byte_length) {
            return Err(conflict("publication output changed after inspection"));
        }
        destination
            .directory()
            .verify()
            .and_then(|()| {
                destination
                    .directory()
                    .verify_name(&self.name, self.identity)
            })
            .map_err(|error| Problem::namespace(&error))
    }
}

pub(super) fn main(
    destination: &Destination,
    header: Header,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    #[cfg(target_os = "freebsd")]
    let Some(regular) = destination
        .directory()
        .open_regular_any_link(destination.main(), false)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    #[cfg(not(target_os = "freebsd"))]
    let Some(regular) = destination
        .directory()
        .open_regular(destination.main(), false)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    #[cfg(target_os = "freebsd")]
    if regular.identity.encode() == header.output_identity {
        let private = destination
            .output_name(header.attempt_id)
            .map_err(|error| Problem::namespace(&error))?;
        destination
            .directory()
            .finish_noreplace_transition(&private, destination.main(), regular.identity)
            .map_err(|error| Problem::namespace(&error))?;
    }
    inspect(
        destination,
        destination.main().clone(),
        regular,
        header,
        Location::Main,
        Mode::Shared,
        cancellation,
    )
    .map(Some)
}

pub(super) fn private(
    destination: &Destination,
    header: Header,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    let Some(inspected) = private_owned(destination, header, cancellation)? else {
        return Ok(None);
    };
    if inspected.access != AccessPolicy::CreatorOnly {
        return Err(conflict(
            "private publication output access no longer matches its reservation",
        ));
    }
    Ok(Some(inspected))
}

pub(super) fn private_owned(
    destination: &Destination,
    header: Header,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    let name = destination
        .output_name(header.attempt_id)
        .map_err(|error| Problem::namespace(&error))?;
    let Some(regular) = destination
        .directory()
        .open_regular(&name, true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    let inspected = inspect(
        destination,
        name,
        regular,
        header,
        Location::Private,
        Mode::Exclusive,
        cancellation,
    )?;
    require_exact_private(&inspected, header)?;
    Ok(Some(inspected))
}

fn inspect(
    destination: &Destination,
    name: Name,
    regular: Regular,
    header: Header,
    location: Location,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<Inspected, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    require_available(
        destination,
        location,
        header.attempt_id,
        &name,
        regular.identity,
    )?;
    lock_output(&regular, mode, cancellation)?;
    destination
        .directory()
        .verify_name(&name, regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let (mapping, meta, byte_length) = map_bootstrap(&regular.file)?;
    let sha512 = output::digest_cancellable(&mapping, byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    let (final_meta, final_length) = read_bootstrap(&regular.file, &mapping)?;
    if (final_meta, final_length) != (meta, byte_length) {
        return Err(conflict("publication output changed while hashing"));
    }
    destination
        .directory()
        .verify()
        .and_then(|()| destination.directory().verify_name(&name, regular.identity))
        .map_err(|error| Problem::namespace(&error))?;
    let content = classify(meta, byte_length, sha512, header);
    let access = classify_access(&regular, header);
    Ok(Inspected {
        name,
        file: regular.file,
        mapping,
        identity: regular.identity,
        meta,
        byte_length,
        sha512,
        content,
        access,
        location,
        attempt_id: header.attempt_id,
    })
}

fn require_available(
    destination: &Destination,
    location: Location,
    attempt_id: [u8; 16],
    name: &Name,
    identity: Identity,
) -> Result<(), Problem> {
    if location == Location::Private {
        super::gc_barrier::require_source_available(
            destination.directory(),
            attempt_id,
            0,
            ArtifactKind::PrivateOutput,
            DirectoryRole::Destination,
            name,
            identity,
        )?;
    }
    Ok(())
}

fn lock_output(
    regular: &Regular,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    live_lock::lock_file_cancellable(&regular.file, MAIN_LIFETIME_LOCK, mode, cancellation)
        .map_err(|error| Problem::sdk(&error))?;
    cancellation.check().map_err(|error| Problem::sdk(&error))
}

fn map_bootstrap(file: &File) -> Result<(Mapping, MetaV4, u64), Problem> {
    let byte_length = file
        .metadata()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    if !bootstrap::geometry_valid(byte_length) {
        return Err(conflict(
            "publication destination has invalid v4 file geometry",
        ));
    }
    let mapping =
        Mapping::read_only_view(file, byte_length).map_err(|error| Problem::sdk(&error))?;
    let (meta, _) = read_bootstrap(file, &mapping)?;
    Ok((mapping, meta, byte_length))
}

fn read_bootstrap(file: &File, mapping: &Mapping) -> Result<(MetaV4, u64), Problem> {
    let byte_length = file
        .metadata()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    if byte_length != mapping.len() {
        return Err(conflict(
            "publication destination changed while reading metadata",
        ));
    }
    let page0 = mapping.page(0, 2).map_err(|error| Problem::sdk(&error))?;
    let page1 = mapping.page(1, 2).map_err(|error| Problem::sdk(&error))?;
    let opened = bootstrap::open_meta_pages(page0, page1, byte_length, OpenMode::ImmutableReader)
        .map_err(|_| conflict("publication destination is not a complete v4 file"))?;
    Ok((opened.meta, byte_length))
}

fn classify(meta: MetaV4, byte_length: u64, sha512: [u8; 64], header: Header) -> Content {
    if meta.database_id == header.database_id
        && meta.txn_id == header.transaction_id
        && meta.commit_nonce == header.commit_nonce
        && byte_length == header.output_byte_length
        && sha512 == header.output_sha512
    {
        Content::Desired
    } else {
        Content::Other
    }
}

fn classify_access(regular: &Regular, header: Header) -> AccessPolicy {
    match regular.creator_only_commitment() {
        Ok(commitment) if commitment == header.security_commitment => AccessPolicy::CreatorOnly,
        _ => AccessPolicy::ChangedOrUnproven,
    }
}

fn require_exact_private(inspected: &Inspected, header: Header) -> Result<(), Problem> {
    if inspected.identity.encode() != header.output_identity
        || inspected.content != Content::Desired
    {
        return Err(conflict(
            "private publication output does not match its reservation",
        ));
    }
    Ok(())
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}
