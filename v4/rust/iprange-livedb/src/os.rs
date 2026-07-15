//! The v4.3 Unix file layer.
//!
//! Concurrency model:
//! - Readers: no lock, just mmap read-only. Register txn_id in companion file.
//! - Writer: holds LOCK_EX for its entire lifetime (serializes against other writers).
//!   Readers are never blocked (they don't take any lock).

use alloc::boxed::Box;
use std::fs::{File, OpenOptions};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::page_store::MmapStore;
use crate::reader::Reader;
use crate::readers::{ReaderGuard, ReaderTable};
use crate::spec::{self, PAGE_SIZE};
use crate::wire::{read_magic, read_version_major, Meta};
use crate::writer::{Changed, Writer};

/// Validate the pinned meta's geometry against the actual file size. This
/// catches a checksum-valid but structurally impossible meta (invalid
/// scope_mode, root/height mismatch, root beyond total_pages, or total_pages
/// exceeding the file) BEFORE any store is constructed or the file is touched.
fn validate_meta_geometry(meta: &Meta, file_len: usize) -> Result<()> {
    if meta.scope_mode > spec::SCOPE_MODE_INDIRECT {
        return Err(Error::Structural("invalid scope_mode"));
    }
    if meta.total_pages < 2 {
        return Err(Error::Structural("total_pages out of range"));
    }
    if (meta.total_pages as usize) > file_len / PAGE_SIZE {
        return Err(Error::Structural("total_pages exceeds file size"));
    }
    if meta.tree_height > spec::TREE_HEIGHT_MAX {
        return Err(Error::Structural("tree_height > 32"));
    }
    if (meta.tree_height == 0) != (meta.root_pgno == 0) {
        return Err(Error::Structural("tree_height/root_pgno inconsistent"));
    }
    if meta.root_pgno != 0 && (meta.root_pgno < 2 || meta.root_pgno as u64 >= meta.total_pages) {
        return Err(Error::Structural("root_pgno out of range"));
    }
    Ok(())
}

/// A read-only mmap of a v4 file. Registers in the reader table on open.
/// Pins the meta at open time for MVCC — subsequent reader() calls always
/// see the transaction snapshot from open time, not the writer's latest commit.
#[allow(missing_debug_implementations)]
pub struct MmapReader {
    _file: File,
    mmap: memmap2::Mmap,
    pinned_meta: Meta,
    _guard: ReaderGuard,
    _table: ReaderTable,
}

impl MmapReader {
    pub fn open(path: &Path) -> Result<MmapReader> {
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(Error::Io)?;

        let fmeta = file.metadata().map_err(Error::Io)?;
        let len = fmeta.len() as usize;
        if len < 2 * PAGE_SIZE {
            return Err(Error::Structural("file too small"));
        }

        // F4 fix: register in the reader table BEFORE reading meta pages.
        // Use txn_id=0 as the provisional sentinel: it blocks ALL reclamation
        // (freed_txn_id < 0 is never true for unsigned). After reading meta,
        // update to the real txn_id. This is distinct from u64::MAX which
        // means "no readers."
        let mut table = ReaderTable::open(path)?;
        let guard = table.register(0)?;

        let mmap = unsafe { memmap2::Mmap::map(&file).map_err(Error::Io)? };
        let page0 = &mmap[..PAGE_SIZE];
        if read_magic(page0) != spec::MAGIC {
            return Err(Error::Structural("bad magic"));
        }
        if read_version_major(page0) != spec::VERSION_MAJOR {
            return Err(Error::Structural("unsupported version_major"));
        }

        let meta_a = Meta::decode(&mmap[..PAGE_SIZE]);
        let meta_b = Meta::decode(&mmap[PAGE_SIZE..2 * PAGE_SIZE]);

        // CRC validation: only trust a meta whose checksum verifies.
        let crc_a_ok = crate::crc32c::verify_page(&mmap[..PAGE_SIZE]);
        let crc_b_ok = crate::crc32c::verify_page(&mmap[PAGE_SIZE..2 * PAGE_SIZE]);
        let pinned_meta = match (crc_a_ok, crc_b_ok) {
            (true, true) => {
                if meta_a.txn_id >= meta_b.txn_id {
                    meta_a
                } else {
                    meta_b
                }
            }
            (true, false) => meta_a,
            (false, true) => meta_b,
            (false, false) => {
                return Err(Error::Structural("both meta pages fail CRC — corrupt file"))
            }
        };

        // Sparse-file hardening.
        let committed_bytes = pinned_meta.total_pages as u64 * PAGE_SIZE as u64;
        if (fmeta.blocks() * 512) < committed_bytes {
            return Err(Error::Structural("committed region is sparse (hole)"));
        }

        // Validate pinned metadata: a checksum-valid meta can still hold an
        // impossible scope_mode or root/height geometry. Reject it before
        // handing the snapshot to callers.
        validate_meta_geometry(&pinned_meta, len)?;

        // Update the reader slot with the real txn_id now that we've pinned.
        table.update_txn_id(guard.slot, guard.pid, guard.reader_id, pinned_meta.txn_id);

        Ok(MmapReader {
            _file: file,
            mmap,
            pinned_meta,
            _guard: guard,
            _table: table,
        })
    }

    pub fn reader(&self) -> Result<Reader<'_>> {
        Reader::from_meta(&self.mmap[..], self.pinned_meta)
    }
    pub fn bytes(&self) -> &[u8] {
        &self.mmap[..]
    }
}

/// A file-backed writer. Holds LOCK_EX for its entire lifetime.
#[allow(missing_debug_implementations)]
pub struct FileWriter<K: IpKey> {
    writer: Writer<K>,
    _file: File,
    reader_table: ReaderTable,
}

impl<K: IpKey> FileWriter<K> {
    pub fn create(path: &Path, scope_mode: u8, created_unixtime: u64) -> Result<FileWriter<K>> {
        use std::io::Write;
        // Validate the scope_mode BEFORE touching the file: create() truncates
        // an existing file, so an invalid mode must be rejected without
        // destroying the caller's data.
        if scope_mode > spec::SCOPE_MODE_INDIRECT {
            return Err(Error::InvalidInput("invalid scope_mode"));
        }
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(Error::Io)?;
        let w = Writer::<K>::create(scope_mode, created_unixtime)?;
        let image = w
            .into_image()
            .ok_or(Error::State("expected VecPageStore"))?;
        file.set_len(image.len() as u64).map_err(Error::Io)?;
        (&file).write_all(&image).map_err(Error::Io)?;
        drop(file);
        Self::open(path)
    }

    pub fn open(path: &Path) -> Result<FileWriter<K>> {
        // Open with LOCK_EX (held for entire lifetime — serializes writers).
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(Error::Io)?;

        let fd = file.as_raw_fd();
        if unsafe { libc::flock(fd, libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            return Err(Error::Locked("another writer has the file open"));
        }

        // Read meta to determine committed_pages.
        let len = file.metadata().map_err(Error::Io)?.len() as usize;
        if len < 2 * PAGE_SIZE {
            let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
            return Err(Error::Structural("file too small"));
        }

        let mut buf = vec![0u8; 2 * PAGE_SIZE];
        use std::os::unix::fs::FileExt;
        file.read_at(&mut buf, 0).map_err(Error::Io)?;
        if read_magic(&buf[..PAGE_SIZE]) != spec::MAGIC {
            let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
            return Err(Error::Structural("bad magic"));
        }

        let meta_a = Meta::decode(&buf[..PAGE_SIZE]);
        let meta_b = Meta::decode(&buf[PAGE_SIZE..2 * PAGE_SIZE]);

        // CRC validation: detect torn meta writes. Only trust a meta whose
        // checksum verifies. If both verify, pick the higher txn_id.
        // If neither verifies, the file is corrupt.
        let crc_a_ok = crate::crc32c::verify_page(&buf[..PAGE_SIZE]);
        let crc_b_ok = crate::crc32c::verify_page(&buf[PAGE_SIZE..2 * PAGE_SIZE]);
        let active = match (crc_a_ok, crc_b_ok) {
            (true, true) => {
                if meta_a.txn_id >= meta_b.txn_id {
                    meta_a
                } else {
                    meta_b
                }
            }
            (true, false) => meta_a,
            (false, true) => meta_b,
            (false, false) => {
                let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
                return Err(Error::Structural("both meta pages fail CRC — corrupt file"));
            }
        };
        // Validate geometry against the ACTUAL file size before constructing the
        // store: MmapStore::open extends the file, and Writer::open does not
        // re-check root/height against total_pages, so an impossible meta (e.g.
        // total_pages=2 with a live root far beyond it) must be rejected here
        // without modifying the file.
        validate_meta_geometry(&active, len)?;
        let committed_pages = active.total_pages as u32;

        // Pre-validate the committed image BEFORE MmapStore::open, which
        // extends the file by a growth chunk. Writer::open runs the full
        // corruption guard (data tree CRC/structure, scope table including
        // overflow chains, free-list); a corrupt file must be rejected WITHOUT
        // being grown or modified.
        {
            use std::os::unix::fs::FileExt;
            let committed_bytes = committed_pages as usize * PAGE_SIZE;
            let mut pre_image = vec![0u8; committed_bytes];
            file.read_at(&mut pre_image, 0).map_err(Error::Io)?;
            let pre_store = crate::page_store::VecPageStore::new(pre_image);
            if let Err(e) = Writer::<K>::open(Box::new(pre_store)) {
                let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
                return Err(e);
            }
        }

        let store = MmapStore::open(file.try_clone().map_err(Error::Io)?, committed_pages)?;
        let mut writer = Writer::<K>::open(Box::new(store))?;

        // Open reader table and query reader roots for MVCC-safe free derivation.
        let reader_table = ReaderTable::open(path)?;
        writer.load_free_list(reader_table.oldest_reader_txn_id());

        Ok(FileWriter {
            writer,
            _file: file, // keeps LOCK_EX alive
            reader_table,
        })
    }

    // Delegated API (core operations)
    pub fn set(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.writer.set(from, to, scope_id)
    }
    pub fn delete(&mut self, from: K, to: K) -> Result<Changed> {
        self.writer.delete(from, to)
    }
    pub fn append(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.writer.append(from, to, scope_id)
    }
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        // I1 fix: hold LOCK_SH on the reader companion file for the entire
        // commit. This blocks reader register (LOCK_EX) during the
        // query→meta-flip window, so a reader cannot register after the
        // oldest-txn snapshot. The guard is dropped (releasing the lock) when
        // commit returns.
        let _commit_lock = self.reader_table.lock_for_commit()?;
        let oldest = self.reader_table.oldest_reader_txn_id();
        self.writer.commit(updated_unixtime, oldest)?;
        Ok(())
    }
    pub fn reader(&self) -> Result<Reader<'_>> {
        self.writer.reader()
    }
    pub fn scan(&self, f: impl FnMut(K, K, u32)) -> Result<()> {
        self.writer.scan(f)
    }
    pub fn record_count(&self) -> u64 {
        self.writer.record_count()
    }

    // Delegated API (feed operations)
    pub fn feed_add_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.writer.feed_add_range(from, to, feed_bit)
    }
    pub fn feed_remove_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.writer.feed_remove_range(from, to, feed_bit)
    }

    // Delegated API (scope operations — mode 2)
    pub fn scope_intern(&mut self, bitmap: &[u8]) -> Result<u32> {
        self.writer.scope_intern(bitmap)
    }
    pub fn scope_resolve(&self, scope_id: u32) -> Option<Vec<u8>> {
        self.writer.scope_resolve(scope_id)
    }

    // Delegated API (migration)
    pub fn migrate(
        &mut self,
        desired: &mut dyn crate::migrate::DesiredStream<K>,
        opts: &crate::migrate::MigrateOptions<K>,
    ) -> Result<crate::migrate::MigrateCounters> {
        crate::migrate::migrate(&mut self.writer, desired, opts)
    }

    pub fn migrate_feed(
        &mut self,
        feed_bit: u32,
        desired: &mut dyn crate::migrate::DesiredStream<K>,
        opts: &crate::migrate::MigrateOptions<K>,
    ) -> Result<crate::migrate::MigrateCounters> {
        crate::feed_migrate::migrate_feed(&mut self.writer, feed_bit, desired, opts)
    }

    pub fn all_to_all_overlap(
        &self,
        on_overlap: &mut dyn FnMut(crate::overlap::FeedOverlap),
    ) -> Result<()> {
        crate::overlap::all_to_all_overlap(&self.writer, on_overlap)
    }

    pub fn foreign_vs_all(
        &self,
        next_foreign: &mut dyn FnMut() -> Option<(K, K)>,
        on_overlap: &mut dyn FnMut(u32, u32, u64),
    ) -> Result<()> {
        crate::overlap::foreign_vs_all(&self.writer, next_foreign, on_overlap)
    }

    pub fn foreign_vs_all_slice(
        &self,
        foreign: &[(K, K)],
        on_overlap: &mut dyn FnMut(u32, u32, u64),
    ) -> Result<()> {
        crate::overlap::foreign_vs_all_slice(&self.writer, foreign, on_overlap)
    }

    pub fn close(self) {
        // Capture committed_pages BEFORE dropping the writer (it owns the value).
        let committed_pages = self.writer.committed_pages();
        let FileWriter {
            writer,
            _file: file,
            reader_table,
        } = self;
        drop(writer);
        drop(reader_table);
        // Issue 2 fix: truncate the file to exactly committed_pages * PAGE_SIZE.
        // The mmap store over-allocates a growth region (committed + growth_chunk);
        // without truncating on close, chain pages allocated in that region linger
        // on disk past the committed boundary. On reopen, committed_pages (from the
        // meta) is smaller than the lingering chain page, so the free-list head
        // looks out-of-bounds and load_free_list silently drops it. Truncating to
        // the committed boundary guarantees the on-disk file matches the meta.
        if committed_pages > 0 {
            let _ = file.set_len(committed_pages as u64 * PAGE_SIZE as u64);
        }
        let _ = file.sync_all();
    }
}

use std::os::unix::io::AsRawFd;
