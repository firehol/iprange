//! Fixed live-reader table bound to one database identity.

use std::fs::File;
use std::ops::{Deref, DerefMut};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};

mod header;

use crate::cancellation::CancellationToken;
use crate::contract::{u64_le, PAGE_SIZE};
use crate::error::{combine_errors, finish_with_cleanup, Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::live_lock::{self, Mode};
use crate::live_namespace::{self, Identity, PrivateCreationFailure};
use crate::mapping::{ByteSource, Mapping};
use crate::path;
use crate::publication::{ArtifactKind, DirectoryRole};

const SLOT_SIZE: u16 = 16;
const GATE_LOCK: u64 = 0;
const WRITER_LOCK: u64 = 1;
pub(crate) const MAIN_LIFETIME_LOCK: u64 = 1u64 << 44;

#[cfg(any(unix, windows))]
pub(crate) use header::has_selectable_header;
pub(crate) use header::{read_header, Header, State};
use header::{read_header_mapping, sidecar_length, write_header_mapping};

#[derive(Debug)]
pub(crate) struct Sidecar {
    pub(crate) file: File,
    pub(crate) path: PathBuf,
    pub(crate) header: Header,
    identity: Identity,
    mapping: Mutex<Option<Mapping>>,
}

struct SidecarMappingGuard<'a> {
    guard: MutexGuard<'a, Option<Mapping>>,
    _probe: crate::worker::Probe<'static>,
}

impl Deref for SidecarMappingGuard<'_> {
    type Target = Option<Mapping>;

    fn deref(&self) -> &Self::Target {
        &self.guard
    }
}

impl DerefMut for SidecarMappingGuard<'_> {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.guard
    }
}

impl Sidecar {
    pub(crate) fn reserve(
        main: &Path,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> core::result::Result<Self, PrivateCreationFailure> {
        if capacity == 0 {
            return Err(PrivateCreationFailure {
                cause: Error::InvalidArgument("reader capacity must be greater than zero"),
                cleanup: live_cleanup::Outcome::clean(),
                identity: None,
            });
        }
        let path = path::canonical_sidecar(main).map_err(|cause| PrivateCreationFailure {
            cause,
            cleanup: live_cleanup::Outcome::clean(),
            identity: None,
        })?;
        Self::reserve_at(path, database_id, sidecar_id, capacity)
    }

    pub(crate) fn reserve_at(
        path: PathBuf,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> core::result::Result<Self, PrivateCreationFailure> {
        if capacity == 0 {
            return Err(PrivateCreationFailure {
                cause: Error::InvalidArgument("reader capacity must be greater than zero"),
                cleanup: live_cleanup::Outcome::clean(),
                identity: None,
            });
        }
        let created = live_namespace::create_private(
            &path,
            CleanupAuthority {
                attempt_id: sidecar_id,
                ordinal: 1,
                kind: ArtifactKind::OwnedCoordination,
                directory_role: DirectoryRole::MainFile,
            },
        )?;
        Ok(Self {
            file: created.file,
            path,
            header: Header {
                capacity,
                database_id,
                sidecar_id,
            },
            identity: created.identity,
            mapping: Mutex::new(None),
        })
    }

    #[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
    pub(crate) fn create(
        main: &Path,
        database_id: [u8; 16],
        sidecar_id: [u8; 16],
        capacity: u32,
    ) -> Result<Self> {
        let sidecar = Self::reserve(main, database_id, sidecar_id, capacity)
            .map_err(PrivateCreationFailure::into_error)?;
        sidecar.initialize_creating()?;
        Ok(sidecar)
    }

    pub(crate) fn initialize_creating(&self) -> Result<()> {
        let length = sidecar_length(self.header.capacity)?;
        self.file.set_len(length)?;
        {
            let mut guard = self.raw_mapping_guard()?;
            *guard = Some(Mapping::read_write_view(&self.file, length)?);
        }
        let mut guard = self.mapping_guard()?;
        let mapping = guard
            .as_mut()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        write_header_mapping(mapping, self.header, State::Creating)?;
        mapping.flush_page(0, 1)?;
        self.file.sync_all()?;
        crate::fault::crash("create.after_sidecar_sync");
        Ok(())
    }

    pub(crate) fn publish_ready(&self) -> Result<()> {
        let mut guard = self.mapping_guard()?;
        let mapping = guard
            .as_mut()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        write_header_mapping(mapping, self.header, State::Ready)?;
        mapping.flush_page(0, 1)?;
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
        let file = live_namespace::open_rw(&path)?;
        let identity = live_namespace::identity(&file)?;
        let (state, header) = read_source_header(&file)?;
        let length = sidecar_length(header.capacity)?;
        if file.metadata()?.len() != length {
            return Err(Error::Corrupt("reader table length is invalid"));
        }
        live_cleanup::require_available(
            &path,
            identity,
            CleanupAuthority {
                attempt_id: header.sidecar_id,
                ordinal: 1,
                kind: ArtifactKind::OwnedCoordination,
                directory_role: DirectoryRole::MainFile,
            },
        )?;
        let mapping = Mapping::read_write_view(&file, length)?;
        Ok((
            Self {
                file,
                path,
                header,
                identity,
                mapping: Mutex::new(Some(mapping)),
            },
            state,
        ))
    }

    pub(crate) fn verify_path(&self) -> Result<()> {
        live_namespace::verify_path(&self.path, self.identity)
    }

    pub(crate) const fn local_identity(&self) -> Identity {
        self.identity
    }

    pub(crate) fn verify_header(&self) -> Result<()> {
        let guard = self.mapping_guard()?;
        let mapping = guard
            .as_ref()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        if read_header_mapping(mapping)? != (State::Ready, self.header) {
            return Err(Error::Corrupt("reader table header changed"));
        }
        Ok(())
    }

    pub(crate) fn current_header(&self) -> Result<(State, Header)> {
        let guard = self.mapping_guard()?;
        let mapping = guard
            .as_ref()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        read_header_mapping(mapping)
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

    #[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
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
                return Err(combine_errors(cause, live_lock::unlock(&self.file, offset)));
            }
            let written = (|| {
                let mut guard = self.mapping_guard()?;
                let mapping = guard
                    .as_mut()
                    .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
                let mut active = mapping.bytes_mut(offset, SLOT_SIZE as usize)?;
                active.put_u64(0, txn)?;
                active.put_u64(8, !txn)
            })();
            if let Err(cause) = written {
                return Err(combine_errors(cause, live_lock::unlock(&self.file, offset)));
            }
            return Ok(slot);
        }
        Err(Error::ReaderCapacityExhausted)
    }

    pub(crate) fn clear_reader(&self, slot: u32) -> Result<()> {
        let offset = slot_offset_checked(slot, self.header.capacity)?;
        let mut guard = self.mapping_guard()?;
        let mapping = guard
            .as_mut()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        mapping.bytes_mut(offset, SLOT_SIZE as usize)?.fill(0);
        Ok(())
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

    #[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
    pub(crate) fn scan_readers(&self, mut observe: impl FnMut(u64) -> Result<()>) -> Result<()> {
        self.scan_readers_inner(None, &mut observe)
    }

    pub(crate) fn scan_readers_cancellable(
        &self,
        cancellation: &CancellationToken,
        mut observe: impl FnMut(u64) -> Result<()>,
    ) -> Result<()> {
        self.scan_readers_inner(Some(cancellation), &mut observe)
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

    pub(crate) fn inspect_at_most_cancellable(
        &self,
        committed_txn: u64,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.inspect_at_most_inner(committed_txn, Some(cancellation))
    }

    fn inspect_at_most_inner(
        &self,
        committed_txn: u64,
        cancellation: Option<&CancellationToken>,
    ) -> Result<()> {
        for slot in 0..self.header.capacity {
            check(cancellation)?;
            if self
                .inspect_slot(slot)?
                .is_some_and(|txn| txn > committed_txn)
            {
                return Err(Error::Corrupt(
                    "reader slot names an uncommitted transaction",
                ));
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
        let guard = self.mapping_guard()?;
        let mapping = guard
            .as_ref()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        let bytes = mapping.bytes(offset, SLOT_SIZE as usize)?;
        let txn = u64_le(bytes, 0);
        if txn == 0 || u64_le(bytes, 8) != !txn {
            return Err(Error::Corrupt("active reader slot is malformed"));
        }
        Ok(Some(txn))
    }

    fn clear_stale(&self, offset: u64) -> Result<()> {
        let cleared: Result<()> = (|| {
            let mut guard = self.mapping_guard()?;
            let mapping = guard
                .as_mut()
                .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
            if !mapping
                .bytes(offset, SLOT_SIZE as usize)?
                .all_zero(0, SLOT_SIZE as usize)
            {
                mapping.bytes_mut(offset, SLOT_SIZE as usize)?.fill(0);
            }
            Ok(())
        })();
        finish_with_cleanup(cleared, live_lock::unlock(&self.file, offset))
    }

    fn mapping_guard(&self) -> Result<SidecarMappingGuard<'_>> {
        let guard = self.raw_mapping_guard()?;
        let mapping = guard
            .as_ref()
            .ok_or(Error::WrongState("reader table mapping is unavailable"))?;
        let probe = crate::worker::enter_coordination(mapping)?;
        Ok(SidecarMappingGuard {
            guard,
            _probe: probe,
        })
    }

    fn raw_mapping_guard(&self) -> Result<MutexGuard<'_, Option<Mapping>>> {
        self.mapping
            .lock()
            .map_err(|_| Error::WrongState("reader table mapping lock is poisoned"))
    }
}

fn read_source_header(file: &File) -> Result<(State, Header)> {
    let mapping = Mapping::read_only_view(file, PAGE_SIZE as u64)?;
    let _probe = crate::worker::enter_coordination(&mapping)?;
    read_header_mapping(&mapping)
}

fn check(cancellation: Option<&CancellationToken>) -> Result<()> {
    match cancellation {
        Some(cancellation) => cancellation.check(),
        None => Ok(()),
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

#[cfg(all(test, any(target_os = "linux", target_vendor = "apple", windows)))]
#[path = "live_sidecar_tests.rs"]
mod tests;
