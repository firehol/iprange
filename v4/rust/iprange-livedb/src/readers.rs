//! Reader registration companion file (LMDB model).
//!
//! Each reader registers (pid, reader_id, txn_id) in a mmap'd companion file.
//! Slot claiming uses atomic CAS (compare-and-swap on the PID field) to
//! prevent cross-process TOCTOU races. Each Reader instance gets a unique
//! reader_id from a process-local atomic counter — no thread_id dependency.

use std::fs::OpenOptions;
use std::path::{Path, PathBuf};
use std::os::unix::io::AsRawFd;
use std::sync::atomic::{AtomicU64, Ordering};

use crate::error::{Error, Result};

pub const SLOT_SIZE: usize = 32;
pub const MAX_SLOTS: usize = 4096 / SLOT_SIZE;

const SLOT_PID_OFF: usize = 0;
const SLOT_RID_OFF: usize = 4;
const SLOT_TXN_OFF: usize = 8;
#[allow(dead_code)]
const SLOT_ROOT_OFF: usize = 16;
#[allow(dead_code)]
const SLOT_HEIGHT_OFF: usize = 20;

/// Process-local counter for unique reader IDs.
static READER_ID_COUNTER: AtomicU64 = AtomicU64::new(1);

pub fn next_reader_id() -> u32 {
    READER_ID_COUNTER.fetch_add(1, Ordering::SeqCst) as u32
}

#[allow(missing_debug_implementations)]
pub struct ReaderTable {
    mmap: memmap2::MmapMut,
    my_slot: Option<usize>,
    path: PathBuf,
}

#[allow(missing_debug_implementations)]
pub struct ReaderGuard {
    pub slot: usize,
    pub pid: u32,
    pub reader_id: u32,
    path: PathBuf,
}

impl Drop for ReaderGuard {
    fn drop(&mut self) {
        if let Ok(file) = OpenOptions::new().read(true).write(true).open(&self.path) {
            if let Ok(mmap) = unsafe { memmap2::MmapOptions::new().map_mut(&file) } {
                let off = self.slot * SLOT_SIZE;
                if off + SLOT_SIZE <= mmap.len() {
                    // Only clear if our pid+reader_id still match.
                    let stored_pid = u32::from_le_bytes([
                        mmap[off], mmap[off+1], mmap[off+2], mmap[off+3],
                    ]);
                    let stored_rid = u32::from_le_bytes([
                        mmap[off+4], mmap[off+5], mmap[off+6], mmap[off+7],
                    ]);
                    if stored_pid == self.pid && stored_rid == self.reader_id {
                        let ptr = mmap.as_ptr().wrapping_add(off) as *mut u8;
                        unsafe { core::ptr::write_bytes(ptr, 0, 4); }
                    }
                }
            }
        }
    }
}

impl ReaderTable {
    pub fn open(db_path: &Path) -> Result<ReaderTable> {
        let readers_path = db_path.with_extension("iprdb.readers");
        // Atomic creation: use O_CREATE|O_EXCL. If it fails with EEXIST,
        // the file was already created by another process.
        match OpenOptions::new()
            .read(true).write(true).create_new(true)
            .open(&readers_path) {
            Ok(file) => { file.set_len(4096).map_err(Error::Io)?; }
            Err(ref e) if e.kind() == std::io::ErrorKind::AlreadyExists => { /* OK */ }
            Err(e) => return Err(Error::Io(e)),
        }
        let file = OpenOptions::new()
            .read(true).write(true)
            .open(&readers_path).map_err(Error::Io)?;
        let mmap = unsafe { memmap2::MmapOptions::new().map_mut(&file).map_err(Error::Io)? };
        Ok(ReaderTable { mmap, my_slot: None, path: readers_path })
    }

    /// Register using flock for cross-process atomicity.
    /// Brief LOCK_EX on the companion file prevents TOCTOU race.
    pub fn register(&mut self, txn_id: u64) -> Result<ReaderGuard> {
        let pid = std::process::id();
        let reader_id = next_reader_id();

        // Acquire brief exclusive lock on the companion file for atomic slot claiming.
        // This serializes register() across processes (not during normal reads).
        let file = OpenOptions::new()
            .read(true).write(true)
            .open(&self.path).map_err(Error::Io)?;
        let fd = file.as_raw_fd();
        if unsafe { libc::flock(fd, libc::LOCK_EX) } != 0 {
            return Err(Error::Io(std::io::Error::last_os_error()));
        }

        // Find a free slot under the lock.
        let mut claimed = None;
        for slot in 0..MAX_SLOTS {
            let current = self.slot_pid(slot);
            if current == 0 || !is_process_alive(current) {
                self.write_slot(slot, pid, reader_id, txn_id);
                claimed = Some(slot);
                break;
            }
        }

        // Release the lock.
        let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };

        match claimed {
            Some(slot) => {
                self.my_slot = Some(slot);
                Ok(ReaderGuard { slot, pid, reader_id, path: self.path.clone() })
            }
            None => Err(Error::State("reader table full")),
        }
    }

    /// Update the txn_id in a reader's slot. Used after the reader has
    /// determined its pinned transaction: register with u64::MAX first
    /// (preventing any reclamation), then update to the real txn_id.
    pub fn update_txn_id(&mut self, slot: usize, pid: u32, reader_id: u32, txn_id: u64) {
        let file = OpenOptions::new()
            .read(true).write(true)
            .open(&self.path).map_err(Error::Io);
        let file = match file {
            Ok(f) => f,
            Err(_) => return,
        };
        let fd = file.as_raw_fd();
        if unsafe { libc::flock(fd, libc::LOCK_EX) } != 0 { return; }
        // Only update if our pid+reader_id still match.
        let stored_pid = self.slot_pid(slot);
        let stored_rid = self.slot_reader_id(slot);
        if stored_pid == pid && stored_rid == reader_id {
            self.write_slot(slot, pid, reader_id, txn_id);
        }
        let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
    }

    pub fn oldest_reader_txn_id(&self) -> u64 {
        let mut oldest = u64::MAX;
        for i in 0..MAX_SLOTS {
            let pid = self.slot_pid(i);
            if pid != 0 && is_process_alive(pid) {
                let txn_id = self.slot_txn_id(i);
                if txn_id < oldest { oldest = txn_id; }
            }
        }
        oldest
    }

    /// Acquire LOCK_SH on the reader companion file for the duration of a
    /// writer commit. Returns an open File whose Drop releases the lock. This
    /// blocks reader register/update_txn_id (LOCK_EX) during the commit,
    /// closing the MVCC race where a reader registers after the writer's
    /// oldest-txn query but before the meta flip.
    pub fn lock_for_commit(&self) -> Result<std::fs::File> {
        let file = OpenOptions::new()
            .read(true).write(true)
            .open(&self.path).map_err(Error::Io)?;
        let fd = file.as_raw_fd();
        if unsafe { libc::flock(fd, libc::LOCK_SH) } != 0 {
            return Err(Error::Io(std::io::Error::last_os_error()));
        }
        Ok(file)
    }


    pub fn reap_stale(&mut self) -> usize {
        let mut cleared = 0;
        for i in 0..MAX_SLOTS {
            let pid = self.slot_pid(i);
            if pid != 0 && !is_process_alive(pid) {
                self.clear_slot(i);
                cleared += 1;
            }
        }
        cleared
    }

    fn write_slot(&mut self, slot: usize, pid: u32, reader_id: u32, txn_id: u64) {
        let off = slot * SLOT_SIZE;
        let b = &mut self.mmap[off..off + SLOT_SIZE];
        // Write fields in reverse publication order. PID is the "publish" marker —
        // a scanning writer only considers a slot valid when PID != 0, by which
        // point all other fields are already written.
        b[SLOT_TXN_OFF..SLOT_TXN_OFF+8].copy_from_slice(&txn_id.to_le_bytes());
        b[SLOT_RID_OFF..SLOT_RID_OFF+4].copy_from_slice(&reader_id.to_le_bytes());
        b[SLOT_PID_OFF..SLOT_PID_OFF+4].copy_from_slice(&pid.to_le_bytes()); // publish last
    }

    fn clear_slot(&mut self, slot: usize) {
        let off = slot * SLOT_SIZE;
        self.mmap[off..off+4].copy_from_slice(&0u32.to_le_bytes());
    }



    #[inline]
    fn slot_pid(&self, slot: usize) -> u32 {
        let off = slot * SLOT_SIZE + SLOT_PID_OFF;
        u32::from_le_bytes([self.mmap[off], self.mmap[off+1], self.mmap[off+2], self.mmap[off+3]])
    }

    #[inline]
    fn slot_reader_id(&self, slot: usize) -> u32 {
        let off = slot * SLOT_SIZE + SLOT_RID_OFF;
        u32::from_le_bytes([self.mmap[off], self.mmap[off+1], self.mmap[off+2], self.mmap[off+3]])
    }

    #[inline]
    fn slot_txn_id(&self, slot: usize) -> u64 {
        let off = slot * SLOT_SIZE + SLOT_TXN_OFF;
        u64::from_le_bytes([
            self.mmap[off], self.mmap[off+1], self.mmap[off+2], self.mmap[off+3],
            self.mmap[off+4], self.mmap[off+5], self.mmap[off+6], self.mmap[off+7],
        ])
    }
}

impl Drop for ReaderTable {
    fn drop(&mut self) {
        // F5 fix: do NOT clear the slot here. ReaderGuard::drop already
        // clears it. If both clear, a reader that reused the slot between
        // the two drops would be incorrectly deregistered.
    }
}

fn is_process_alive(pid: u32) -> bool {
    unsafe { libc::kill(pid as i32, 0) == 0 }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env::temp_dir;

    fn test_path(name: &str) -> PathBuf {
        let p = temp_dir().join(format!("iprange_test_{}.iprdb", name));
        let _ = std::fs::remove_file(p.with_extension("iprdb.readers"));
        p
    }

    #[test]
    fn register_and_query() {
        let path = test_path("rdr1");
        let mut table = ReaderTable::open(&path).unwrap();
        let reg = table.register(42).unwrap();
        assert_eq!(table.oldest_reader_txn_id(), 42);
        drop(reg);
        assert_eq!(table.oldest_reader_txn_id(), u64::MAX);
    }

    #[test]
    fn unique_reader_ids() {
        let id1 = next_reader_id();
        let id2 = next_reader_id();
        assert_ne!(id1, id2);
    }

    #[test]
    fn reap_stale() {
        let path = test_path("rdr3");
        let mut table = ReaderTable::open(&path).unwrap();
        table.write_slot(5, 999999, 1, 1);
        assert!(table.reap_stale() >= 1);
    }
}
