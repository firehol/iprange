//! Stable two-inode inspection for replacement resolution.

use std::fs::File;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::error::ErrorCode;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::{file_io, publication};

use super::namespace::{Destination, Identity, Name, Regular};
use super::output;
use super::problem::Problem;
use super::reservation::Header;
use super::result::AccessPolicy;

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
    pub(super) identity: Identity,
    pub(super) meta: Option<MetaV4>,
    pub(super) byte_length: u64,
    pub(super) sha512: [u8; 64],
    pub(super) content: Content,
    pub(super) location: Location,
    pub(super) access: AccessPolicy,
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
    let mut main = open(destination, destination.main().clone(), Location::Main)?;
    let mut private = open(destination, private_name, Location::Private)?;
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
) -> Result<Option<Inspected>, Problem> {
    let regular = destination
        .directory()
        .open_regular(&name, true)
        .map_err(|error| Problem::namespace(&error))?;
    Ok(regular.map(|regular| opened(name, location, regular)))
}

fn opened(name: Name, location: Location, regular: Regular) -> Inspected {
    Inspected {
        name,
        file: regular.file,
        identity: regular.identity,
        meta: None,
        byte_length: 0,
        sha512: [0; 64],
        content: Content::Other,
        location,
        access: AccessPolicy::Unclassified,
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
    live_lock::lock_cancellable(
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
    entry.sha512 = output::digest_cancellable(&entry.file, entry.byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    entry.meta = desired_meta(&entry.file, entry.byte_length, entry.sha512, header)?;
    entry.content = classify(&entry, header);
    entry.access = access(&entry, header);
    verify_stable(destination, &entry)?;
    Ok(entry)
}

fn desired_meta(
    file: &File,
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
    let mut pages = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut pages, 0).map_err(|error| Problem::sdk(&error))?;
    let left = (&pages[..PAGE_SIZE]).try_into().expect("fixed meta page");
    let right = (&pages[PAGE_SIZE..]).try_into().expect("fixed meta page");
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

impl Inspected {
    pub(super) fn verify(
        &self,
        destination: &Destination,
        cancellation: &CancellationToken,
    ) -> Result<(), Problem> {
        verify_stable(destination, self)?;
        let digest = output::digest_cancellable(&self.file, self.byte_length, cancellation)
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
