//! Reader registration companion file (LMDB model).
//!
//! A small mmap'd file (`<db>.readers`) where each reader process registers
//! the `txn_id` it's using. The writer scans this to find the oldest active
//! reader generation, and only reclaims freed pages from generations older
//! than that.

use std::fs::OpenOptions;
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

/// Each reader slot: 32 bytes.
pub const SLOT_SIZE: usize = 32;
/// Maximum slots in the default 4096-byte file.
pub const MAX_SLOTS: usize = 4096 / SLOT_SIZE;

const SLOT_PID_OFFSET: usize = 0;
const SLOT_TXN_ID_OFFSET: usize = 4;

/// A reader registration table. Holds a writable mmap of the companion file.
/// The mmap is shared memory — writes go directly to the file.
pub struct ReaderTable {
    mmap: memmap2::MmapMut,
    my_slot: Option<usize>,
    path: PathBuf,
}

/// A registered reader guard. Dropping it deregisters the slot.
pub struct ReaderGuard {
    slot: usize,
    path: PathBuf,
}

impl Drop for ReaderGuard {
    fn drop(&mut self) {
        if let Ok(file) = OpenOptions::new().read(true).write(true).open(&self.path) {
            if let Ok(mmap) = unsafe { memmap2::MmapOptions::new().map_mut(&file) } {
                let off = self.slot * SLOT_SIZE;
                if off + 4 <= mmap.len() {
                    let ptr = mmap.as_ptr().wrapping_add(off) as *mut u8;
                    unsafe { core::ptr::write_bytes(ptr, 0, 4); }
                }
            }
        }
    }
}

impl ReaderTable {
    pub fn open(db_path: &Path) -> Result<ReaderTable> {
        let readers_path = db_path.with_extension("iprdb.readers");
        if !readers_path.exists() {
            let file = OpenOptions::new()
                .read(true).write(true).create(true).truncate(true)
                .open(&readers_path)
                .map_err(Error::Io)?;
            file.set_len(4096).map_err(Error::Io)?;
        }

        let file = OpenOptions::new()
            .read(true).write(true)
            .open(&readers_path)
            .map_err(Error::Io)?;

        let mmap = unsafe {
            memmap2::MmapOptions::new()
                .map_mut(&file)
                .map_err(Error::Io)?
        };

        Ok(ReaderTable {
            mmap,
            my_slot: None,
            path: readers_path,
        })
    }

    /// Register this process as a reader at `txn_id`.
    pub fn register(&mut self, txn_id: u64) -> Result<ReaderGuard> {
        let pid = std::process::id();
        let slot = self.find_or_claim_slot(pid)?;
        self.write_slot(slot, pid, txn_id);
        self.my_slot = Some(slot);
        Ok(ReaderGuard { slot, path: self.path.clone() })
    }

    /// Find the oldest active reader's `txn_id`.
    pub fn oldest_reader_txn_id(&self) -> u64 {
        let mut oldest = u64::MAX;
        for i in 0..MAX_SLOTS {
            let pid = self.slot_pid(i);
            if pid != 0 && is_process_alive(pid) {
                let txn_id = self.slot_txn_id(i);
                if txn_id < oldest {
                    oldest = txn_id;
                }
            }
        }
        oldest
    }

    /// Clear stale slots (readers that crashed without deregistering).
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

    fn find_or_claim_slot(&self, pid: u32) -> Result<usize> {
        let mut free_slot = None;
        for i in 0..MAX_SLOTS {
            let slot_pid = self.slot_pid(i);
            if slot_pid == pid {
                return Ok(i);
            }
            if slot_pid == 0 || !is_process_alive(slot_pid) {
                if free_slot.is_none() {
                    free_slot = Some(i);
                }
            }
        }
        free_slot.ok_or(Error::State("reader table full"))
    }

    fn write_slot(&mut self, slot: usize, pid: u32, txn_id: u64) {
        let off = slot * SLOT_SIZE;
        let bytes = &mut self.mmap[off..off + SLOT_SIZE];
        bytes[SLOT_PID_OFFSET..SLOT_PID_OFFSET + 4].copy_from_slice(&pid.to_le_bytes());
        bytes[SLOT_TXN_ID_OFFSET..SLOT_TXN_ID_OFFSET + 8].copy_from_slice(&txn_id.to_le_bytes());
    }

    fn clear_slot(&mut self, slot: usize) {
        let off = slot * SLOT_SIZE;
        let bytes = &mut self.mmap[off..off + 4];
        bytes.copy_from_slice(&0u32.to_le_bytes());
    }

    #[inline]
    fn slot_pid(&self, slot: usize) -> u32 {
        let off = slot * SLOT_SIZE + SLOT_PID_OFFSET;
        u32::from_le_bytes([
            self.mmap[off], self.mmap[off + 1],
            self.mmap[off + 2], self.mmap[off + 3],
        ])
    }

    #[inline]
    fn slot_txn_id(&self, slot: usize) -> u64 {
        let off = slot * SLOT_SIZE + SLOT_TXN_ID_OFFSET;
        u64::from_le_bytes([
            self.mmap[off], self.mmap[off + 1], self.mmap[off + 2], self.mmap[off + 3],
            self.mmap[off + 4], self.mmap[off + 5], self.mmap[off + 6], self.mmap[off + 7],
        ])
    }
}

impl Drop for ReaderTable {
    fn drop(&mut self) {
        if let Some(slot) = self.my_slot {
            self.clear_slot(slot);
        }
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
    fn multiple_registers_same_pid() {
        let path = test_path("rdr2");
        let mut t1 = ReaderTable::open(&path).unwrap();
        // Same process → same slot. Registering 10 then 20 overwrites.
        let _r1 = t1.register(10).unwrap();
        assert_eq!(t1.oldest_reader_txn_id(), 10);

        // Re-register at a different txn_id (same process = same slot)
        let _r2 = t1.register(20).unwrap();
        assert_eq!(t1.oldest_reader_txn_id(), 20);
    }

    #[test]
    fn reap_stale() {
        let path = test_path("rdr3");
        let mut table = ReaderTable::open(&path).unwrap();
        // Manually write a slot with a dead PID
        table.write_slot(5, 999999, 1); // PID 999999 is almost certainly dead
        assert!(table.reap_stale() >= 1);
    }
}
