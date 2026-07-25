//! Fixed live-reader table bound to one database identity.

use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};

#[cfg(unix)]
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_lock::{self, Mode};
use crate::path;
use crate::slotted_page::{put_u16, put_u32, put_u64};

const MAGIC: [u8; 8] = *b"IPRDRS4\0";
const HEADER_SIZE: u16 = 68;
const SLOT_SIZE: u16 = 16;
const HEADER_CRC: usize = 64;
const STATE_CREATING: u32 = 0;
const STATE_READY: u32 = 1;
const GATE_LOCK: u64 = 0;
const WRITER_LOCK: u64 = 1;
pub(crate) const MAIN_LIFETIME_LOCK: u64 = 1u64 << 44;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Identity {
    device: u64,
    inode: u64,
}

impl Identity {
    pub(crate) fn encode(self) -> [u8; 32] {
        let mut bytes = [0; 32];
        bytes[..8].copy_from_slice(&self.device.to_le_bytes());
        bytes[8..16].copy_from_slice(&self.inode.to_le_bytes());
        bytes
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Header {
    pub(crate) capacity: u32,
    pub(crate) database_id: [u8; 16],
    pub(crate) sidecar_id: [u8; 16],
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
        write_header(&self.file, self.header, STATE_CREATING)?;
        self.file.sync_all()?;
        crate::fault::crash("create.after_sidecar_sync");
        Ok(())
    }

    pub(crate) fn publish_ready(&self) -> Result<()> {
        write_header(&self.file, self.header, STATE_READY)?;
        crate::fault::crash("create.after_ready_write");
        Ok(self.file.sync_all()?)
    }

    pub(crate) fn open(main: &Path, database_id: [u8; 16]) -> Result<Self> {
        let path = path::canonical_sidecar(main)?;
        let file = open_rw(&path)?;
        let identity = identity(&file)?;
        let header = read_header(&file)?;
        if header.database_id != database_id {
            return Err(Error::WrongMode(
                "reader table belongs to a different database",
            ));
        }
        if file.metadata()?.len() != sidecar_length(header.capacity)? {
            return Err(Error::Corrupt("reader table length is invalid"));
        }
        Ok(Self {
            file,
            path,
            header,
            identity,
        })
    }

    pub(crate) fn verify_path(&self) -> Result<()> {
        verify_path(&self.path, self.identity)
    }

    pub(crate) fn verify_header(&self) -> Result<()> {
        if read_header(&self.file)? != self.header {
            return Err(Error::Corrupt("reader table header changed"));
        }
        Ok(())
    }

    pub(crate) fn lock_gate(&self, mode: Mode) -> Result<()> {
        live_lock::lock(&self.file, GATE_LOCK, mode)
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

    pub(crate) fn claim_reader(&self, txn: u64) -> Result<u32> {
        if txn == 0 {
            return Err(Error::Corrupt("reader transaction cannot be zero"));
        }
        for slot in 0..self.header.capacity {
            let offset = slot_offset(slot)?;
            if !live_lock::try_lock(&self.file, offset, Mode::Exclusive)? {
                continue;
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
        let offset = slot_offset_checked(slot, self.header.capacity)?;
        file_io::write_exact_at(&self.file, &[0; SLOT_SIZE as usize], offset)?;
        live_lock::unlock(&self.file, offset)
    }

    pub(crate) fn scan_readers(&self, mut observe: impl FnMut(u64) -> Result<()>) -> Result<()> {
        for slot in 0..self.header.capacity {
            if let Some(txn) = self.scan_slot(slot)? {
                observe(txn)?;
            }
        }
        Ok(())
    }

    pub(crate) fn scan_at_most(&self, committed_txn: u64) -> Result<()> {
        self.oldest_reader(committed_txn).map(|_| ())
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
        let mut oldest = None;
        self.scan_readers(|txn| {
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

pub(crate) fn identity(file: &File) -> Result<Identity> {
    let identity = identity_any_link(file)?;
    #[cfg(unix)]
    if file.metadata()?.nlink() != 1 {
        return Err(Error::WrongMode("live files must have exactly one link"));
    }
    Ok(identity)
}

pub(crate) fn identity_any_link(file: &File) -> Result<Identity> {
    let metadata = file.metadata()?;
    if !metadata.file_type().is_file() {
        return Err(Error::InvalidArgument("live path is not a regular file"));
    }
    #[cfg(unix)]
    {
        Ok(Identity {
            device: metadata.dev(),
            inode: metadata.ino(),
        })
    }
    #[cfg(not(unix))]
    Err(Error::Unsupported(
        "live file identity is not implemented on this platform",
    ))
}

pub(crate) fn verify_path(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, true)
}

pub(crate) fn verify_path_any_link(path: &Path, expected: Identity) -> Result<()> {
    verify_path_inner(path, expected, false)
}

fn verify_path_inner(path: &Path, expected: Identity, require_single_link: bool) -> Result<()> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() || !metadata.file_type().is_file() {
        return Err(Error::WrongMode("live path no longer names a regular file"));
    }
    #[cfg(unix)]
    {
        if (require_single_link && metadata.nlink() != 1)
            || metadata.dev() != expected.device
            || metadata.ino() != expected.inode
        {
            return Err(Error::WrongMode("live path identity changed"));
        }
        Ok(())
    }
    #[cfg(not(unix))]
    Err(Error::Unsupported(
        "live path identity is not implemented on this platform",
    ))
}

pub(crate) fn open_rw(path: &Path) -> Result<File> {
    let mut options = OpenOptions::new();
    options.read(true).write(true);
    #[cfg(unix)]
    options.custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC);
    Ok(options.open(path)?)
}

pub(crate) fn create_private(path: &Path) -> Result<File> {
    let mut options = OpenOptions::new();
    options.read(true).write(true).create_new(true);
    #[cfg(unix)]
    {
        options
            .mode(0o600)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC);
    }
    let file = options.open(path)?;
    #[cfg(unix)]
    {
        use std::os::fd::AsRawFd;
        if unsafe { libc::fchmod(file.as_raw_fd(), 0o600) } != 0 {
            let cause = std::io::Error::last_os_error().into();
            return Err(created_failure(path, &file, cause));
        }
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
    verify_path(path, expected)?;
    fs::remove_file(path)?;
    sync_parent(path)
}

pub(crate) fn sync_parent(path: &Path) -> Result<()> {
    let parent = path.parent().ok_or(Error::InvalidArgument(
        "database path has no parent directory",
    ))?;
    File::open(parent)?.sync_all()?;
    Ok(())
}

fn write_header(file: &File, header: Header, state: u32) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    page[..8].copy_from_slice(&MAGIC);
    put_u16(&mut page, 8, HEADER_SIZE);
    put_u16(&mut page, 10, SLOT_SIZE);
    put_u32(&mut page, 12, state);
    put_u32(&mut page, 16, header.capacity);
    page[32..48].copy_from_slice(&header.database_id);
    page[48..64].copy_from_slice(&header.sidecar_id);
    let checksum = crc32c::crc32c_with_zeroed(&page, HEADER_CRC, 4)
        .ok_or(Error::Corrupt("reader table checksum field is invalid"))?;
    put_u32(&mut page, HEADER_CRC, checksum);
    file_io::write_exact_at(file, &page, 0)
}

fn read_header(file: &File) -> Result<Header> {
    let mut page = [0; PAGE_SIZE];
    file_io::read_exact_at(file, &mut page, 0)?;
    if !header_shape_valid(&page) || !header_checksum_valid(&page) {
        return Err(Error::Corrupt("reader table header is invalid"));
    }
    let mut database_id = [0; 16];
    database_id.copy_from_slice(&page[32..48]);
    let mut sidecar_id = [0; 16];
    sidecar_id.copy_from_slice(&page[48..64]);
    if database_id == [0; 16] || sidecar_id == [0; 16] {
        return Err(Error::Corrupt("reader table identity is invalid"));
    }
    Ok(Header {
        capacity: u32_le(&page, 16),
        database_id,
        sidecar_id,
    })
}

fn header_shape_valid(page: &[u8; PAGE_SIZE]) -> bool {
    let fixed = (
        &page[..8],
        u16_le(page, 8),
        u16_le(page, 10),
        u32_le(page, 12),
    );
    fixed == (&MAGIC, HEADER_SIZE, SLOT_SIZE, STATE_READY)
        && u32_le(page, 16) != 0
        && page[20..32].iter().all(|&byte| byte == 0)
        && page[68..].iter().all(|&byte| byte == 0)
}

fn header_checksum_valid(page: &[u8; PAGE_SIZE]) -> bool {
    crc32c::crc32c_with_zeroed(page, HEADER_CRC, 4) == Some(u32_le(page, HEADER_CRC))
}

fn sidecar_length(capacity: u32) -> Result<u64> {
    u64::from(capacity)
        .checked_mul(u64::from(SLOT_SIZE))
        .and_then(|bytes| bytes.checked_add(PAGE_SIZE as u64))
        .ok_or(Error::InvalidArgument("reader table length overflows"))
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
