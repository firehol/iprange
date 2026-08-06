//! Exact discovery of one restart-authoritative publication reservation.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error;
use crate::error::ErrorCode;
use crate::live_lock::{self, Mode};
use crate::mapping::Mapping;

use super::namespace::{Destination, Identity, Name, NamespaceError, Regular, ScanError};
use super::problem::Problem;
use super::reservation::{self, Header, Selected};
use super::result::AccessPolicy;
use super::{ArtifactKind, DirectoryRole};

const FILE_SIZE: u64 = (2 * PAGE_SIZE) as u64;
const OPERATION_LOCK: u64 = 0;
const PREFIX: &[u8] = b".iprange-reservation-";
const SUFFIX: &[u8] = b".tmp";
const ENCODED_ATTEMPT_LEN: usize = 32;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Location {
    Private,
    Canonical,
}

#[derive(Debug)]
pub(super) struct Inspected {
    pub(super) name: Name,
    pub(super) file: File,
    pub(super) mapping: Mapping,
    pub(super) identity: Identity,
    pub(super) header: Header,
    pub(super) location: Location,
    pub(super) access: AccessPolicy,
}

impl Inspected {
    pub(super) fn verify(&self, destination: &Destination) -> Result<(), Problem> {
        require_available(
            destination,
            self.location,
            &self.name,
            self.identity,
            self.header,
        )?;
        destination
            .directory()
            .verify()
            .map_err(|error| Problem::namespace(&error))?;
        let name = match self.location {
            Location::Private => &self.name,
            Location::Canonical => destination.coordination(),
        };
        destination
            .directory()
            .verify_name(name, self.identity)
            .map_err(|error| Problem::namespace(&error))?;
        let selected = read_selected(&self.mapping).map_err(strict_record)?;
        if selected.header != self.header {
            return Err(conflict("publication reservation changed after inspection"));
        }
        Ok(())
    }

    pub(super) fn unlock_operation(&self) -> Result<(), Problem> {
        live_lock::unlock(&self.file, OPERATION_LOCK).map_err(|error| Problem::sdk(&error))
    }

    pub(super) fn relock_operation(
        &self,
        destination: &Destination,
        cancellation: &CancellationToken,
    ) -> Result<(), Problem> {
        require_available(
            destination,
            self.location,
            &self.name,
            self.identity,
            self.header,
        )?;
        lock_operation_file(&self.file, cancellation)?;
        self.verify(destination)
    }
}

pub(super) fn discover(
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    if let Some(inspected) = canonical(destination, cancellation)? {
        return Ok(Some(inspected));
    }
    let found = scan_private(destination, cancellation)?;
    destination
        .directory()
        .require_absent(destination.coordination())
        .map_err(|_| conflict("coordination changed during reservation scan"))?;
    Ok(found)
}

pub(super) fn canonical(
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    #[cfg(target_os = "freebsd")]
    let Some(regular) = destination
        .directory()
        .open_regular_any_link(destination.coordination(), true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    #[cfg(not(target_os = "freebsd"))]
    let Some(regular) = destination
        .directory()
        .open_regular(destination.coordination(), true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    inspect_canonical(destination, regular, cancellation).map(Some)
}

pub(super) fn exact_private(
    destination: &Destination,
    expected: Header,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    cancellation.check().map_err(|error| Problem::sdk(&error))?;
    let name = destination
        .reservation_name(expected.attempt_id)
        .map_err(|error| Problem::namespace(&error))?;
    let Some(regular) = destination
        .directory()
        .open_regular(&name, true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    require_available(
        destination,
        Location::Private,
        &name,
        regular.identity,
        expected,
    )?;
    lock_operation(&regular, cancellation)?;
    destination
        .directory()
        .verify_name(&name, regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let mapping = map_reservation(&regular.file).map_err(strict_record)?;
    let selected = read_selected(&mapping).map_err(strict_record)?;
    require_bound(
        destination,
        selected.header,
        regular.identity,
        Some(expected.attempt_id),
    )?;
    if selected.header != expected {
        return Err(conflict("caller result and private reservation disagree"));
    }
    Ok(Some(inspected(
        name,
        regular,
        mapping,
        selected,
        Location::Private,
    )))
}

fn inspect_canonical(
    destination: &Destination,
    regular: Regular,
    cancellation: &CancellationToken,
) -> Result<Inspected, Problem> {
    let mapping = map_reservation(&regular.file).map_err(strict_record)?;
    let selected = read_selected(&mapping).map_err(strict_record)?;
    require_available(
        destination,
        Location::Canonical,
        destination.coordination(),
        regular.identity,
        selected.header,
    )?;
    lock_operation(&regular, cancellation)?;
    if read_selected(&mapping).map_err(strict_record)? != selected {
        return Err(conflict(
            "publication reservation changed while acquiring its lock",
        ));
    }
    require_bound(destination, selected.header, regular.identity, None)?;
    let private_name = destination
        .reservation_name(selected.header.attempt_id)
        .map_err(|error| Problem::namespace(&error))?;
    #[cfg(target_os = "freebsd")]
    destination
        .directory()
        .finish_noreplace_transition(&private_name, destination.coordination(), regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    destination
        .directory()
        .verify_name(destination.coordination(), regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    if read_selected(&mapping).map_err(strict_record)? != selected {
        return Err(conflict(
            "publication reservation changed during inspection",
        ));
    }
    destination
        .directory()
        .verify()
        .map_err(|error| Problem::namespace(&error))?;
    Ok(inspected(
        private_name,
        regular,
        mapping,
        selected,
        Location::Canonical,
    ))
}

fn scan_private(
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    let mut found = None;
    let scanned = destination.directory().scan(|bytes| {
        cancellation.check().map_err(|error| Problem::sdk(&error))?;
        let Some(attempt_id) = parse_private_name(bytes) else {
            return Ok(());
        };
        let name = Name::new(bytes).map_err(|error| Problem::namespace(&error))?;
        let Some(candidate) = inspect_private(destination, name, attempt_id, cancellation)? else {
            return Ok(());
        };
        if found.is_some() {
            return Err(conflict(
                "multiple bound private publication reservations exist",
            ));
        }
        found = Some(candidate);
        Ok(())
    });
    match scanned {
        Ok(()) => Ok(found),
        Err(ScanError::Namespace(error)) => Err(Problem::namespace(&error)),
        Err(ScanError::Visitor(problem)) => Err(problem),
    }
}

fn inspect_private(
    destination: &Destination,
    name: Name,
    attempt_id: [u8; 16],
    cancellation: &CancellationToken,
) -> Result<Option<Inspected>, Problem> {
    let regular = match destination.directory().open_regular(&name, true) {
        Ok(Some(regular)) => regular,
        Ok(None) => return Ok(None),
        Err(error) if invalid_private_entry(&error) => return Ok(None),
        Err(error) => return Err(Problem::namespace(&error)),
    };
    super::gc_barrier::require_source_available(
        destination.directory(),
        attempt_id,
        1,
        ArtifactKind::PrivateReservation,
        DirectoryRole::Destination,
        &name,
        regular.identity,
    )?;
    let mapping = match map_reservation(&regular.file) {
        Ok(mapping) => mapping,
        Err(ReadError::Invalid) => return Ok(None),
        Err(ReadError::Sdk(error)) => return Err(Problem::sdk(&error)),
    };
    let selected = match read_selected(&mapping) {
        Ok(selected) => selected,
        Err(ReadError::Invalid) => return Ok(None),
        Err(ReadError::Sdk(error)) => return Err(Problem::sdk(&error)),
    };
    if require_bound(
        destination,
        selected.header,
        regular.identity,
        Some(attempt_id),
    )
    .is_err()
    {
        return Ok(None);
    }
    lock_operation(&regular, cancellation)?;
    destination
        .directory()
        .verify_name(&name, regular.identity)
        .map_err(|_| conflict("private reservation changed during inspection"))?;
    let rechecked = read_selected(&mapping).map_err(strict_record)?;
    if rechecked != selected {
        return Err(conflict("private reservation changed during inspection"));
    }
    Ok(Some(inspected(
        name,
        regular,
        mapping,
        selected,
        Location::Private,
    )))
}

fn require_available(
    destination: &Destination,
    location: Location,
    private_name: &Name,
    identity: Identity,
    header: Header,
) -> Result<(), Problem> {
    let (name, kind) = match location {
        Location::Private => (private_name, ArtifactKind::PrivateReservation),
        Location::Canonical => (destination.coordination(), ArtifactKind::OwnedCoordination),
    };
    super::gc_barrier::require_source_available(
        destination.directory(),
        header.attempt_id,
        1,
        kind,
        DirectoryRole::Destination,
        name,
        identity,
    )
}

fn inspected(
    name: Name,
    regular: Regular,
    mapping: Mapping,
    selected: Selected,
    location: Location,
) -> Inspected {
    let access = match regular.creator_only_commitment() {
        Ok(commitment) if commitment == selected.header.security_commitment => {
            AccessPolicy::CreatorOnly
        }
        _ => AccessPolicy::ChangedOrUnproven,
    };
    Inspected {
        name,
        file: regular.file,
        mapping,
        identity: regular.identity,
        header: selected.header,
        location,
        access,
    }
}

fn lock_operation(regular: &Regular, cancellation: &CancellationToken) -> Result<(), Problem> {
    lock_operation_file(&regular.file, cancellation)
}

fn lock_operation_file(file: &File, cancellation: &CancellationToken) -> Result<(), Problem> {
    live_lock::lock_cancellable(file, OPERATION_LOCK, Mode::Exclusive, cancellation)
        .map_err(|error| Problem::sdk(&error))?;
    cancellation.check().map_err(|error| Problem::sdk(&error))
}

pub(super) fn require_bound(
    destination: &Destination,
    header: Header,
    identity: Identity,
    filename_attempt: Option<[u8; 16]>,
) -> Result<(), Problem> {
    if header.reservation_identity != identity.encode() {
        return Err(conflict(
            "reservation self identity does not match its inode",
        ));
    }
    if filename_attempt.is_some_and(|attempt| attempt != header.attempt_id) {
        return Err(conflict("private reservation name has another attempt id"));
    }
    let basename_len =
        u32::try_from(destination.main().bytes().len()).map_err(|_| name_mismatch())?;
    if header.basename_len != basename_len
        || header.basename_commitment != destination.basename_commitment()
    {
        return Err(name_mismatch());
    }
    Ok(())
}

fn map_reservation(file: &File) -> Result<Mapping, ReadError> {
    if file
        .metadata()
        .map_err(error::Error::from)
        .map_err(ReadError::Sdk)?
        .len()
        != FILE_SIZE
    {
        return Err(ReadError::Invalid);
    }
    Mapping::read_write_view(file, FILE_SIZE).map_err(ReadError::Sdk)
}

fn read_selected(mapping: &Mapping) -> Result<Selected, ReadError> {
    reservation::select(
        mapping
            .bytes(0, FILE_SIZE as usize)
            .map_err(ReadError::Sdk)?,
    )
    .map_err(|_| ReadError::Invalid)
}

fn parse_private_name(bytes: &[u8]) -> Option<[u8; 16]> {
    let encoded = bytes.strip_prefix(PREFIX)?.strip_suffix(SUFFIX)?;
    if encoded.len() != ENCODED_ATTEMPT_LEN {
        return None;
    }
    let mut attempt = [0; 16];
    for (slot, pair) in attempt.iter_mut().zip(encoded.chunks_exact(2)) {
        *slot = decode_hex(pair[0])?.checked_mul(16)? + decode_hex(pair[1])?;
    }
    (attempt != [0; 16]).then_some(attempt)
}

fn decode_hex(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        _ => None,
    }
}

fn invalid_private_entry(error: &NamespaceError) -> bool {
    match error {
        NamespaceError::NotRegular
        | NamespaceError::LinkCount(_)
        | NamespaceError::CrossFilesystem => true,
        NamespaceError::IoAt { source, .. } => source.raw_os_error() == Some(libc::ELOOP),
        _ => false,
    }
}

fn strict_record(error: ReadError) -> Problem {
    match error {
        ReadError::Invalid => Problem::new(
            ErrorCode::Unresolvable,
            None,
            "publication reservation record is not selectable",
        ),
        ReadError::Sdk(error) => Problem::sdk(&error),
    }
}

const fn conflict(detail: &'static str) -> Problem {
    Problem::new(ErrorCode::Conflict, None, detail)
}

const fn name_mismatch() -> Problem {
    Problem::new(
        ErrorCode::DestinationNameMismatch,
        None,
        "reservation belongs to another destination name",
    )
}

enum ReadError {
    Invalid,
    Sdk(error::Error),
}
