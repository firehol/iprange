//! Fixed live-reader table bound to one database identity.

use std::fs::File;
use std::path::{Path, PathBuf};

mod header;

use crate::cancellation::CancellationToken;
use crate::contract::{u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::path;
use crate::publication::namespace::{
    retained_regular_identity, Directory, Name, NamespaceError, IDENTITY_KIND,
};
use crate::publication::security::{self, Profile};
use crate::slotted_page::put_u64;
use crate::validation::LocalFileIdentity;

const SLOT_SIZE: u16 = 16;
const GATE_LOCK: u64 = 0;
const WRITER_LOCK: u64 = 1;
pub(crate) const MAIN_LIFETIME_LOCK: u64 = 1u64 << 44;

pub(crate) use crate::publication::namespace::Identity;
#[cfg(unix)]
pub(crate) use header::has_selectable_header;
pub(crate) use header::{read_header, Header, State};
use header::{sidecar_length, write_header};

pub(crate) fn public_identity(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: identity.encode(),
    }
}

pub(crate) fn parent_identity(path: &Path) -> Result<LocalFileIdentity> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    let directory = Directory::open(parent).map_err(|error| match error {
        NamespaceError::Missing => Error::Io(std::io::Error::from(std::io::ErrorKind::NotFound)),
        other => namespace_error(other),
    })?;
    Ok(LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: directory.identity().encode(),
    })
}

#[derive(Debug)]
pub(crate) struct Sidecar {
    pub(crate) file: File,
    pub(crate) path: PathBuf,
    pub(crate) header: Header,
    identity: Identity,
}

impl Sidecar {
    pub(crate) fn reserve(
        main: &Path,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> Result<Self> {
        if capacity == 0 {
            return Err(Error::InvalidArgument(
                "reader capacity must be greater than zero",
            ));
        }
        let path = path::canonical_sidecar(main)?;
        Self::reserve_at(path, database_id, sidecar_id, capacity)
    }

    pub(crate) fn reserve_at(
        path: PathBuf,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> Result<Self> {
        if capacity == 0 {
            return Err(Error::InvalidArgument(
                "reader capacity must be greater than zero",
            ));
        }
        let file = create_private(&path)?;
        let identity = match identity(&file) {
            Ok(identity) => identity,
            Err(cause) => return Err(created_failure(&path, &file, cause)),
        };
        Ok(Self {
            file,
            path,
            header: Header {
                capacity,
                database_id,
                sidecar_id,
            },
            identity,
        })
    }

    #[cfg(test)]
    pub(crate) fn create(
        main: &Path,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> Result<Self> {
        let sidecar = Self::reserve(main, database_id, sidecar_id, capacity)?;
        sidecar.initialize_creating()?;
        Ok(sidecar)
    }

    pub(crate) fn initialize_creating(&self) -> Result<()> {
        self.file.set_len(sidecar_length(self.header.capacity)?)?;
        write_header(&self.file, self.header, State::Creating)?;
        self.file.sync_all()?;
        crate::fault::crash("create.after_sidecar_sync");
        Ok(())
    }

    pub(crate) fn publish_ready(&self) -> Result<()> {
        write_header(&self.file, self.header, State::Ready)?;
        crate::fault::crash("create.after_ready_write");
        Ok(self.file.sync_all()?)
    }

    pub(crate) fn open(main: &Path, database_id: [u8; 16]) -> Result<Self> {
        let path = path::canonical_sidecar(main)?;
        let (sidecar, state) = Self::open_at(path, database_id)?;
        if state != State::Ready {
            return Err(Error::WrongState("reader table is not ready"));
        }
        Ok(sidecar)
    }

    pub(crate) fn open_at(path: PathBuf, database_id: [u8; 16]) -> Result<(Self, State)> {
        let (sidecar, state) = Self::open_any(path)?;
        if sidecar.header.database_id != database_id {
            return Err(Error::WrongMode(
                "reader table belongs to a different database",
            ));
        }
        Ok((sidecar, state))
    }

    pub(crate) fn open_any(path: PathBuf) -> Result<(Self, State)> {
        let file = open_rw(&path)?;
        let identity = identity(&file)?;
        let (state, header) = read_header(&file)?;
        if file.metadata()?.len() != sidecar_length(header.capacity)? {
            return Err(Error::Corrupt("reader table length is invalid"));
        }
        Ok((
            Self {
                file,
                path,
                header,
                identity,
            },
            state,
        ))
    }

    pub(crate) fn verify_path(&self) -> Result<()> {
        verify_path(&self.path, self.identity)
    }

    pub(crate) const fn local_identity(&self) -> Identity {
        self.identity
    }

    pub(crate) fn verify_header(&self) -> Result<()> {
        if read_header(&self.file)? != (State::Ready, self.header) {
            return Err(Error::Corrupt("reader table header changed"));
        }
        Ok(())
    }

    pub(crate) fn lock_gate(&self, mode: Mode) -> Result<()> {
        live_lock::lock(&self.file, GATE_LOCK, mode)
    }

    pub(crate) fn lock_gate_cancellable(
        &self,
        mode: Mode,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        live_lock::lock_cancellable(&self.file, GATE_LOCK, mode, cancellation)
    }

    pub(crate) fn unlock_gate(&self) -> Result<()> {
        live_lock::unlock(&self.file, GATE_LOCK)
    }

    pub(crate) fn claim_writer(&self) -> Result<()> {
        if live_lock::try_lock(&self.file, WRITER_LOCK, Mode::Exclusive)? {
            Ok(())
        } else {
            Err(Error::WriterBusy)
        }
    }

    pub(crate) fn release_writer(&self) -> Result<()> {
        live_lock::unlock(&self.file, WRITER_LOCK)
    }

    pub(crate) fn claim_reader(&self, txn: u64) -> Result<u32> {
        self.claim_reader_inner(txn, None)
    }

    pub(crate) fn claim_reader_cancellable(
        &self,
        txn: u64,
        cancellation: &CancellationToken,
    ) -> Result<u32> {
        self.claim_reader_inner(txn, Some(cancellation))
    }

    fn claim_reader_inner(
        &self,
        txn: u64,
        cancellation: Option<&CancellationToken>,
    ) -> Result<u32> {
        if txn == 0 {
            return Err(Error::Corrupt("reader transaction cannot be zero"));
        }
        for slot in 0..self.header.capacity {
            check(cancellation)?;
            let offset = slot_offset(slot)?;
            if !live_lock::try_lock(&self.file, offset, Mode::Exclusive)? {
                continue;
            }
            if let Err(cause) = check(cancellation) {
                return Err(combine_cleanup(
                    cause,
                    live_lock::unlock(&self.file, offset),
                ));
            }
            let mut active = [0; SLOT_SIZE as usize];
            put_u64(&mut active, 0, txn);
            put_u64(&mut active, 8, !txn);
            if let Err(error) = file_io::write_exact_at(&self.file, &active, offset) {
                let _ = live_lock::unlock(&self.file, offset);
                return Err(error);
            }
            return Ok(slot);
        }
        Err(Error::ReaderCapacityExhausted)
    }

    pub(crate) fn release_reader(&self, slot: u32) -> Result<()> {
        self.clear_reader(slot)?;
        self.unlock_reader(slot)
    }

    pub(crate) fn clear_reader(&self, slot: u32) -> Result<()> {
        let offset = slot_offset_checked(slot, self.header.capacity)?;
        file_io::write_exact_at(&self.file, &[0; SLOT_SIZE as usize], offset)
    }

    pub(crate) fn unlock_reader(&self, slot: u32) -> Result<()> {
        live_lock::unlock(&self.file, slot_offset_checked(slot, self.header.capacity)?)
    }

    pub(crate) fn verify_reader(&self, slot: u32, transaction: u64) -> Result<()> {
        let offset = slot_offset_checked(slot, self.header.capacity)?;
        if self.read_active_slot(offset)? != Some(transaction) {
            return Err(Error::Corrupt("owned reader slot changed"));
        }
        Ok(())
    }

    pub(crate) fn scan_readers(&self, mut observe: impl FnMut(u64) -> Result<()>) -> Result<()> {
        self.scan_readers_inner(None, &mut observe)
    }

    pub(crate) fn scan_at_most(&self, committed_txn: u64) -> Result<()> {
        self.oldest_reader(committed_txn).map(|_| ())
    }

    pub(crate) fn scan_at_most_cancellable(
        &self,
        committed_txn: u64,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let mut oldest = None;
        self.scan_readers_inner(Some(cancellation), |txn| {
            if txn > committed_txn {
                return Err(Error::Corrupt(
                    "reader slot names an uncommitted transaction",
                ));
            }
            oldest = Some(oldest.map_or(txn, |current: u64| current.min(txn)));
            Ok(())
        })
    }

    pub(crate) fn inspect_at_most(&self, committed_txn: u64) -> Result<()> {
        for slot in 0..self.header.capacity {
            if let Some(txn) = self.inspect_slot(slot)? {
                if txn > committed_txn {
                    return Err(Error::Corrupt(
                        "reader slot names an uncommitted transaction",
                    ));
                }
            }
        }
        Ok(())
    }

    pub(crate) fn oldest_reader(&self, committed_txn: u64) -> Result<Option<u64>> {
        self.oldest_reader_inner(committed_txn, None)
    }

    pub(crate) fn oldest_reader_cancellable(
        &self,
        committed_txn: u64,
        cancellation: &CancellationToken,
    ) -> Result<Option<u64>> {
        self.oldest_reader_inner(committed_txn, Some(cancellation))
    }

    fn oldest_reader_inner(
        &self,
        committed_txn: u64,
        cancellation: Option<&CancellationToken>,
    ) -> Result<Option<u64>> {
        let mut oldest = None;
        self.scan_readers_inner(cancellation, |txn| {
            if txn > committed_txn {
                return Err(Error::Corrupt(
                    "reader slot names an uncommitted transaction",
                ));
            }
            oldest = Some(oldest.map_or(txn, |current: u64| current.min(txn)));
            Ok(())
        })?;
        Ok(oldest)
    }

    fn scan_readers_inner(
        &self,
        cancellation: Option<&CancellationToken>,
        mut observe: impl FnMut(u64) -> Result<()>,
    ) -> Result<()> {
        for slot in 0..self.header.capacity {
            check(cancellation)?;
            if let Some(txn) = self.scan_slot(slot)? {
                observe(txn)?;
            }
        }
        Ok(())
    }

    fn scan_slot(&self, slot: u32) -> Result<Option<u64>> {
        let offset = slot_offset(slot)?;
        if live_lock::try_lock(&self.file, offset, Mode::Exclusive)? {
            self.clear_stale(offset)?;
            return Ok(None);
        }
        self.read_active_slot(offset)
    }

    fn inspect_slot(&self, slot: u32) -> Result<Option<u64>> {
        let offset = slot_offset(slot)?;
        if live_lock::try_lock(&self.file, offset, Mode::Exclusive)? {
            live_lock::unlock(&self.file, offset)?;
            return Ok(None);
        }
        self.read_active_slot(offset)
    }

    fn read_active_slot(&self, offset: u64) -> Result<Option<u64>> {
        let mut bytes = [0; SLOT_SIZE as usize];
        file_io::read_exact_at(&self.file, &mut bytes, offset)?;
        let txn = u64_le(&bytes, 0);
        if txn == 0 || u64_le(&bytes, 8) != !txn {
            return Err(Error::Corrupt("active reader slot is malformed"));
        }
        Ok(Some(txn))
    }

    fn clear_stale(&self, offset: u64) -> Result<()> {
        let cleared: Result<()> = (|| {
            let mut bytes = [0; SLOT_SIZE as usize];
            file_io::read_exact_at(&self.file, &mut bytes, offset)?;
            if bytes.iter().any(|&byte| byte != 0) {
                file_io::write_exact_at(&self.file, &[0; SLOT_SIZE as usize], offset)?;
            }
            Ok(())
        })();
        let unlocked = live_lock::unlock(&self.file, offset);
        cleared?;
        unlocked
    }
}

fn check(cancellation: Option<&CancellationToken>) -> Result<()> {
    match cancellation {
        Some(cancellation) => cancellation.check(),
        None => Ok(()),
    }
}

fn combine_cleanup(cause: Error, cleanup: Result<()>) -> Error {
    match cleanup {
        Ok(()) => cause,
        Err(cleanup) => Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        },
    }
}

pub(crate) fn identity(file: &File) -> Result<Identity> {
    retained_regular_identity(file, true).map_err(namespace_error)
}

pub(crate) fn identity_any_link(file: &File) -> Result<Identity> {
    retained_regular_identity(file, false).map_err(namespace_error)
}

pub(crate) fn verify_path(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, true)
}

pub(crate) fn verify_path_any_link(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, false)
}

pub(crate) fn path_identity(path: &Path) -> Result<Option<Identity>> {
    let (directory, name) = match bind_path(path) {
        Ok(bound) => bound,
        Err(Error::NameNotFound) => return Ok(None),
        Err(error) => return Err(error),
    };
    match directory.entry(&name).map_err(namespace_error)? {
        None => Ok(None),
        Some(entry) if entry.regular && entry.links == 1 => Ok(Some(entry.identity)),
        Some(_) => Err(Error::WrongMode("live path is not one regular file")),
    }
}

fn verify_path_inner(path: &Path, expected: Identity, require_single_link: bool) -> Result<()> {
    let (directory, name) = bind_path(path)?;
    let entry = directory
        .entry(&name)
        .map_err(namespace_error)?
        .ok_or(Error::NameNotFound)?;
    if !entry.regular {
        return Err(Error::WrongMode("live path no longer names a regular file"));
    }
    if (require_single_link && entry.links != 1) || entry.identity != expected {
        return Err(Error::WrongMode("live path identity changed"));
    }
    Ok(())
}

pub(crate) fn open_rw(path: &Path) -> Result<File> {
    let (directory, name) = bind_path(path)?;
    let regular = directory
        .open_regular(&name, true)
        .map_err(namespace_error)?
        .ok_or(Error::NameNotFound)?;
    Ok(regular.file)
}

pub(crate) fn create_private(path: &Path) -> Result<File> {
    let (directory, name) = bind_path(path)?;
    let profile = Profile::capture().map_err(namespace_error)?;
    let file = directory.create(&name, &profile).map_err(namespace_error)?;
    if let Err(error) = security::secure_creator_only(&file, &profile) {
        return Err(created_failure(path, &file, namespace_error(error)));
    }
    Ok(file)
}

fn created_failure(path: &Path, file: &File, cause: Error) -> Error {
    match remove_created(path, file) {
        Ok(()) => cause,
        Err(cleanup) => Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        },
    }
}

fn remove_created(path: &Path, file: &File) -> Result<()> {
    let expected = identity(file)?;
    remove_exact(path, expected)
}

pub(crate) fn remove_exact(path: &Path, expected: Identity) -> Result<()> {
    let (directory, name) = bind_path(path)?;
    directory
        .verify_name(&name, expected)
        .map_err(namespace_error)?;
    if !directory
        .unlink_exact(&name, expected)
        .map_err(namespace_error)?
    {
        return Err(Error::NameNotFound);
    }
    directory.sync().map_err(namespace_error)?;
    directory.require_absent(&name).map_err(namespace_error)
}

pub(crate) fn install_noreplace(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    expected: Identity,
) -> Result<()> {
    let (directory, private_name, canonical_name) = bind_pair(private, canonical)?;
    directory
        .verify_name(&private_name, expected)
        .map_err(namespace_error)?;
    directory
        .rename_noreplace(&private_name, private_file, &canonical_name)
        .map_err(namespace_error)?;
    directory.sync().map_err(namespace_error)?;
    directory
        .require_absent(&private_name)
        .and_then(|()| directory.verify_name(&canonical_name, expected))
        .map_err(namespace_error)
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
pub(crate) fn install_exchange(
    private: &Path,
    private_file: &File,
    canonical: &Path,
    expected_private: Identity,
    expected_canonical: Identity,
) -> Result<()> {
    let (directory, private_name, canonical_name) = bind_pair(private, canonical)?;
    if directory
        .verify_name(&private_name, expected_private)
        .and_then(|()| directory.verify_name(&canonical_name, expected_canonical))
        .is_err()
    {
        return Err(Error::CleanupConflict(
            "canonical coordination changed during reset",
        ));
    }
    directory
        .replace(&private_name, private_file, &canonical_name)
        .map_err(namespace_error)?;
    if directory
        .verify_name(&canonical_name, expected_private)
        .is_ok()
        && directory
            .verify_name(&private_name, expected_canonical)
            .is_ok()
    {
        return Ok(());
    }

    let cause = Error::CleanupConflict("canonical coordination changed during reset");
    match directory.replace(&canonical_name, private_file, &private_name) {
        Ok(()) => Err(cause),
        Err(cleanup) => Err(Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(namespace_error(cleanup)),
        }),
    }
}

pub(crate) fn sync_parent(path: &Path) -> Result<()> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    Directory::open(parent)
        .and_then(|directory| directory.sync())
        .map_err(namespace_error)
}

fn bind_path(path: &Path) -> Result<(Directory, Name)> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    let component = path
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))?;
    let directory = Directory::open(parent).map_err(namespace_error)?;
    let name = Name::from_component(component).map_err(namespace_error)?;
    Ok((directory, name))
}

fn bind_pair(private: &Path, canonical: &Path) -> Result<(Directory, Name, Name)> {
    if private.parent() != canonical.parent() {
        return Err(Error::InvalidArgument(
            "live transition names must share one directory",
        ));
    }
    let (directory, private_name) = bind_path(private)?;
    let canonical_name = canonical
        .file_name()
        .ok_or(Error::InvalidArgument("database path has no file name"))
        .and_then(|component| Name::from_component(component).map_err(namespace_error))?;
    Ok((directory, private_name, canonical_name))
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::NameInvalid,
        NamespaceError::Exists => Error::NameExists,
        NamespaceError::Missing => Error::NameNotFound,
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported | NamespaceError::CrossFilesystem => {
            Error::Unsupported("live file namespace lacks required local operations")
        }
        NamespaceError::NotDirectory
        | NamespaceError::NotRegular
        | NamespaceError::IdentityChanged
        | NamespaceError::LinkCount(_)
        | NamespaceError::AccessPolicy => Error::WrongMode("live file ownership changed"),
    }
}

fn slot_offset(slot: u32) -> Result<u64> {
    u64::from(slot)
        .checked_mul(u64::from(SLOT_SIZE))
        .and_then(|bytes| bytes.checked_add(PAGE_SIZE as u64))
        .ok_or(Error::Corrupt("reader slot offset overflows"))
}

fn slot_offset_checked(slot: u32, capacity: u32) -> Result<u64> {
    if slot >= capacity {
        return Err(Error::Corrupt("reader slot index is invalid"));
    }
    slot_offset(slot)
}

#[cfg(test)]
#[path = "live_sidecar_tests.rs"]
mod tests;
