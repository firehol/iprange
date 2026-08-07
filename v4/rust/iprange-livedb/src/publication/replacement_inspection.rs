//! Stable two-inode inspection for replacement resolution.

use std::fs::File;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::error::ErrorCode;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;
use crate::publication;

use super::namespace::{Destination, Identity, Name, Regular};
use super::output;
use super::problem::Problem;
use super::reservation::Header;
use super::result::AccessPolicy;
use super::{ArtifactKind, DirectoryRole};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Content {
    Desired,
    Previous,
    Other,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Location {
    Main,
    Private,
}

#[derive(Debug)]
pub(super) struct Inspected {
    pub(super) name: Name,
    pub(super) file: File,
    pub(super) mapping: Option<Mapping>,
    pub(super) identity: Identity,
    pub(super) meta: Option<MetaV4>,
    pub(super) byte_length: u64,
    pub(super) sha512: [u8; 64],
    pub(super) content: Content,
    pub(super) location: Location,
    pub(super) access: AccessPolicy,
    attempt_id: [u8; 16],
    locked: bool,
}

#[derive(Debug)]
pub(super) struct Pair {
    pub(super) main: Option<Inspected>,
    pub(super) private: Option<Inspected>,
}

pub(super) fn inspect(
    destination: &Destination,
    header: Header,
    cancellation: &CancellationToken,
) -> Result<Pair, Problem> {
    let private_name = destination
        .output_name(header.attempt_id)
        .map_err(|error| Problem::namespace(&error))?;
    let mut main = open(
        destination,
        destination.main().clone(),
        Location::Main,
        header.attempt_id,
    )?;
    let mut private = open(
        destination,
        private_name,
        Location::Private,
        header.attempt_id,
    )?;
    lock_role(
        &mut main,
        &mut private,
        header.output_identity,
        cancellation,
    )?;
    let previous = header
        .previous
        .expect("replacement inspection requires previous evidence");
    lock_role(&mut main, &mut private, previous.identity, cancellation)?;
    lock_remaining(&mut main, cancellation)?;
    lock_remaining(&mut private, cancellation)?;
    let main = finish(destination, header, main, cancellation)?;
    let private = finish(destination, header, private, cancellation)?;
    Ok(Pair { main, private })
}

fn open(
    destination: &Destination,
    name: Name,
    location: Location,
    attempt_id: [u8; 16],
) -> Result<Option<Inspected>, Problem> {
    let Some(regular) = destination
        .directory()
        .open_regular(&name, true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    let entry = opened(name, location, attempt_id, regular);
    require_available(destination, &entry)?;
    Ok(Some(entry))
}

fn opened(name: Name, location: Location, attempt_id: [u8; 16], regular: Regular) -> Inspected {
    Inspected {
        name,
        file: regular.file,
        mapping: None,
        identity: regular.identity,
        meta: None,
        byte_length: 0,
        sha512: [0; 64],
        content: Content::Other,
        location,
        access: AccessPolicy::Unclassified,
        attempt_id,
        locked: false,
    }
}

fn lock_role(
    main: &mut Option<Inspected>,
    private: &mut Option<Inspected>,
    identity: [u8; 32],
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    if let Some(entry) = entry_with_identity(main, private, identity) {
        lock(entry, cancellation)?;
    }
    Ok(())
}

fn entry_with_identity<'a>(
    main: &'a mut Option<Inspected>,
    private: &'a mut Option<Inspected>,
    identity: [u8; 32],
) -> Option<&'a mut Inspected> {
    if main
        .as_ref()
        .is_some_and(|entry| entry.identity.encode() == identity)
    {
        main.as_mut()
    } else if private
        .as_ref()
        .is_some_and(|entry| entry.identity.encode() == identity)
    {
        private.as_mut()
    } else {
        None
    }
}

fn lock_remaining(
    entry: &mut Option<Inspected>,
    cancellation: &CancellationToken,
) -> Result<(), Problem> {
    if let Some(entry) = entry.as_mut().filter(|entry| !entry.locked) {
        lock(entry, cancellation)?;
    }
    Ok(())
}

fn lock(entry: &mut Inspected, cancellation: &CancellationToken) -> Result<(), Problem> {
    live_lock::lock_file_cancellable(
        &entry.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(|error| Problem::sdk(&error))?;
    entry.locked = true;
    Ok(())
}

fn finish(
    destination: &Destination,
    header: Header,
    entry: Option<Inspected>,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    entry
        .map(|entry| inspect_one(destination, header, entry, cancellation))
        .transpose()
}

fn inspect_one(
    destination: &Destination,
    header: Header,
    mut entry: Inspected,
    cancellation: &CancellationToken,
) -> Result<Inspected, Problem> {
    require_available(destination, &entry)?;
    destination
        .directory()
        .verify_name(&entry.name, entry.identity)
        .map_err(|error| Problem::namespace(&error))?;
    entry.byte_length = entry
        .file
        .metadata()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    let mapping = Mapping::read_only_view(&entry.file, entry.byte_length)
        .map_err(|error| Problem::sdk(&error))?;
    entry.sha512 = output::digest_cancellable(&mapping, entry.byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    entry.meta = desired_meta(&mapping, entry.byte_length, entry.sha512, header)?;
    entry.mapping = Some(mapping);
    entry.content = classify(&entry, header);
    entry.access = access(&entry, header);
    verify_stable(destination, &entry)?;
    Ok(entry)
}

fn desired_meta(
    mapping: &Mapping,
    byte_length: u64,
    sha512: [u8; 64],
    header: Header,
) -> Result<Option<MetaV4>, Problem> {
    if byte_length != header.output_byte_length || sha512 != header.output_sha512 {
        return Ok(None);
    }
    if byte_length < (2 * PAGE_SIZE) as u64 || byte_length % PAGE_SIZE as u64 != 0 {
        return Ok(None);
    }
    let left = mapping.page(0, 2).map_err(|error| Problem::sdk(&error))?;
    let right = mapping.page(1, 2).map_err(|error| Problem::sdk(&error))?;
    let opened =
        match bootstrap::open_meta_pages(left, right, byte_length, OpenMode::ImmutableReader) {
            Ok(opened) => opened,
            Err(_) => return Ok(None),
        };
    let meta = opened.meta;
    if meta.database_id == header.database_id
        && meta.txn_id == header.transaction_id
        && meta.commit_nonce == header.commit_nonce
    {
        Ok(Some(meta))
    } else {
        Ok(None)
    }
}

fn classify(entry: &Inspected, header: Header) -> Content {
    if entry.meta.is_some() {
        return Content::Desired;
    }
    let previous = header
        .previous
        .expect("replacement inspection requires previous evidence");
    if entry.identity.encode() == previous.identity
        && entry.byte_length == previous.byte_length
        && entry.sha512 == previous.sha512
    {
        Content::Previous
    } else {
        Content::Other
    }
}

fn access(entry: &Inspected, header: Header) -> AccessPolicy {
    match publication::security::creator_only_commitment(&entry.file) {
        Ok(commitment) if commitment == header.security_commitment => AccessPolicy::CreatorOnly,
        _ => AccessPolicy::ChangedOrUnproven,
    }
}

fn verify_stable(destination: &Destination, entry: &Inspected) -> Result<(), Problem> {
    require_available(destination, entry)?;
    destination
        .directory()
        .verify()
        .and_then(|()| {
            destination
                .directory()
                .verify_name(&entry.name, entry.identity)
        })
        .map_err(|error| Problem::namespace(&error))?;
    if entry
        .file
        .metadata()
        .map_err(crate::error::Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len()
        != entry.byte_length
    {
        return Err(conflict("replacement content changed while hashing"));
    }
    Ok(())
}

fn require_available(destination: &Destination, entry: &Inspected) -> Result<(), Problem> {
    if entry.location == Location::Private {
        super::gc_barrier::require_source_available(
            destination.directory(),
            entry.attempt_id,
            0,
            ArtifactKind::PrivateOutput,
            DirectoryRole::Destination,
            &entry.name,
            entry.identity,
        )?;
    }
    Ok(())
}

impl Inspected {
    pub(super) fn verify(
        &self,
        destination: &Destination,
        cancellation: &CancellationToken,
    ) -> Result<(), Problem> {
        verify_stable(destination, self)?;
        let digest = output::digest_cancellable(
            self.mapping
                .as_ref()
                .expect("finished replacement inspection retains its mapping"),
            self.byte_length,
            cancellation,
        )
        .map_err(|error| Problem::output(&error))?;
        verify_stable(destination, self)?;
        if digest != self.sha512 {
            return Err(conflict("replacement content changed after inspection"));
        }
        Ok(())
    }
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}
