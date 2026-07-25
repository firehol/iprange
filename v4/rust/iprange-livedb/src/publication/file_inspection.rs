//! Stable, bounded inspection of publication output bytes.

use std::fs::File;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::error::ErrorCode;
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;

use super::namespace::{Destination, Identity, Name, Regular};
use super::output;
use super::problem::Problem;
use super::reservation::Header;
use super::result::AccessPolicy;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Content {
    Desired,
    Other,
}

#[derive(Debug)]
pub(super) struct Inspected {
    pub(super) name: Name,
    pub(super) file: File,
    pub(super) identity: Identity,
    pub(super) meta: MetaV4,
    pub(super) byte_length: u64,
    pub(super) sha512: [u8; 64],
    pub(super) content: Content,
    pub(super) access: AccessPolicy,
}

impl Inspected {
    pub(super) fn verify(&self, destination: &Destination) -> Result<(), Problem> {
        let (meta, byte_length) = read_bootstrap(&self.file)?;
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
    let Some(regular) = destination
        .directory()
        .open_regular(destination.main(), false)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    inspect(
        destination,
        destination.main().clone(),
        regular,
        header,
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
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<Inspected, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    lock_output(&regular, mode, cancellation)?;
    destination
        .directory()
        .verify_name(&name, regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let (meta, byte_length) = read_bootstrap(&regular.file)?;
    let sha512 = output::digest_cancellable(&regular.file, byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    let (final_meta, final_length) = read_bootstrap(&regular.file)?;
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
        identity: regular.identity,
        meta,
        byte_length,
        sha512,
        content,
        access,
    })
}

fn lock_output(
    regular: &Regular,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    live_lock::lock_cancellable(&regular.file, MAIN_LIFETIME_LOCK, mode, cancellation)
        .map_err(|error| Problem::sdk(&error))?;
    cancellation.check().map_err(|error| Problem::sdk(&error))
}

fn read_bootstrap(file: &File) -> Result<(MetaV4, u64), Problem> {
    let byte_length = file
        .metadata()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    if byte_length < (2 * PAGE_SIZE) as u64 || byte_length % PAGE_SIZE as u64 != 0 {
        return Err(conflict(
            "publication destination has invalid v4 file geometry",
        ));
    }
    let mut pages = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut pages, 0).map_err(|error| {
        if error.code() == ErrorCode::Corrupt {
            conflict("publication destination changed while reading metadata")
        } else {
            Problem::sdk(&error)
        }
    })?;
    let page0 = (&pages[..PAGE_SIZE]).try_into().expect("fixed meta page");
    let page1 = (&pages[PAGE_SIZE..]).try_into().expect("fixed meta page");
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
