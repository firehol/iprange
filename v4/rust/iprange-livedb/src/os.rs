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
use crate::readers::{ReaderTable, ReaderGuard};
use crate::spec::{self, PAGE_SIZE};
use crate::wire::{read_magic, read_version_major, Meta};
use crate::writer::{Changed, Writer};

/// A read-only mmap of a v4 file. Registers in the reader table on open.
pub struct MmapReader {
    _file: File,
    mmap: memmap2::Mmap,
    _guard: ReaderGuard,
    _table: ReaderTable,
}

impl MmapReader {
    pub fn open(path: &Path) -> Result<MmapReader> {
        let file = OpenOptions::new()
            .read(true).custom_flags(libc::O_NOFOLLOW)
            .open(path).map_err(Error::Io)?;

        let meta = file.metadata().map_err(Error::Io)?;
        let len = meta.len() as usize;
        if len < 2 * PAGE_SIZE { return Err(Error::Structural("file too small")); }
        if (meta.blocks() * 512) < len as u64 {
            return Err(Error::Structural("sparse file (hole)"));
        }

        let mmap = unsafe { memmap2::Mmap::map(&file).map_err(Error::Io)? };
        let page0 = &mmap[..PAGE_SIZE];
        if read_magic(page0) != spec::MAGIC { return Err(Error::Structural("bad magic")); }
        if read_version_major(page0) != spec::VERSION_MAJOR {
            return Err(Error::Structural("unsupported version_major"));
        }

        let meta_a = Meta::decode(&mmap[..PAGE_SIZE]);
        let meta_b = Meta::decode(&mmap[PAGE_SIZE..2 * PAGE_SIZE]);
        let active_txn_id = if meta_a.txn_id >= meta_b.txn_id { meta_a.txn_id } else { meta_b.txn_id };

        let mut table = ReaderTable::open(path)?;
        let guard = table.register(active_txn_id)?;

        Ok(MmapReader { _file: file, mmap, _guard: guard, _table: table })
    }

    pub fn reader(&self) -> Result<Reader<'_>> { Reader::open(&self.mmap[..]) }
    pub fn bytes(&self) -> &[u8] { &self.mmap[..] }
}

/// A file-backed writer. Holds LOCK_EX for its entire lifetime.
pub struct FileWriter<K: IpKey> {
    writer: Writer<K>,
    _file: File,
    _reader_table: ReaderTable,
}

impl<K: IpKey> FileWriter<K> {
    pub fn create(path: &Path, scope_mode: u8, created_unixtime: u64) -> Result<FileWriter<K>> {
        use std::io::Write;
        let file = OpenOptions::new()
            .read(true).write(true).create(true).truncate(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path).map_err(Error::Io)?;
        let w = Writer::<K>::create(scope_mode, created_unixtime)?;
        let image = w.into_image().ok_or(Error::State("expected VecPageStore"))?;
        file.set_len(image.len() as u64).map_err(Error::Io)?;
        (&file).write_all(&image).map_err(Error::Io)?;
        drop(file);
        Self::open(path)
    }

    pub fn open(path: &Path) -> Result<FileWriter<K>> {
        // Open with LOCK_EX (held for entire lifetime — serializes writers).
        let file = OpenOptions::new()
            .read(true).write(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path).map_err(Error::Io)?;

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
        let committed_pages = if meta_a.txn_id >= meta_b.txn_id {
            meta_a.total_pages
        } else { meta_b.total_pages } as u32;

        let store = MmapStore::open(file.try_clone().map_err(Error::Io)?, committed_pages)?;
        let writer = Writer::<K>::open(Box::new(store))?;

        // Open reader table (for future oldest_reader queries).
        let reader_table = ReaderTable::open(path)?;

        Ok(FileWriter {
            writer,
            _file: file, // keeps LOCK_EX alive
            _reader_table: reader_table,
        })
    }

    // Delegated API (core operations)
    pub fn set(&mut self, from: K, to: K, scope_id: u32) -> Result<()> { self.writer.set(from, to, scope_id) }
    pub fn delete(&mut self, from: K, to: K) -> Result<Changed> { self.writer.delete(from, to) }
    pub fn append(&mut self, from: K, to: K, scope_id: u32) -> Result<()> { self.writer.append(from, to, scope_id) }
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> { self.writer.commit(updated_unixtime) }
    pub fn reader(&self) -> Result<Reader<'_>> { self.writer.reader() }
    pub fn scan(&self, f: impl FnMut(K, K, u32)) -> Result<()> { self.writer.scan(f) }
    pub fn record_count(&self) -> u64 { self.writer.record_count() }

    // Delegated API (feed operations)
    pub fn feed_add_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.writer.feed_add_range(from, to, feed_bit)
    }
    pub fn feed_remove_range(&mut self, from: K, to: K, feed_bit: u32) -> Result<()> {
        self.writer.feed_remove_range(from, to, feed_bit)
    }

    // Delegated API (scope operations — mode 2)
    pub fn scope_intern(&mut self, bitmap: &[u8]) -> Result<u32> { self.writer.scope_intern(bitmap) }
    pub fn scope_resolve(&self, scope_id: u32) -> Option<&[u8]> { self.writer.scope_resolve(scope_id) }

    // Delegated API (migration)
    pub fn migrate(&mut self, desired: &mut dyn crate::migrate::DesiredStream<K>,
        opts: &crate::migrate::MigrateOptions<K>) -> Result<crate::migrate::MigrateCounters> {
        crate::migrate::migrate(&mut self.writer, desired, opts)
    }

    pub fn migrate_feed(&mut self, feed_bit: u32,
        desired: &mut dyn crate::migrate::DesiredStream<K>,
        opts: &crate::migrate::MigrateOptions<K>) -> Result<crate::migrate::MigrateCounters> {
        crate::feed_migrate::migrate_feed(&mut self.writer, feed_bit, desired, opts)
    }

    pub fn all_to_all_overlap(&self, on_overlap: &mut dyn FnMut(crate::overlap::FeedOverlap)) -> Result<()> {
        crate::overlap::all_to_all_overlap(&self.writer, on_overlap)
    }

    pub fn close(self) {}
}

use std::os::unix::io::AsRawFd;
