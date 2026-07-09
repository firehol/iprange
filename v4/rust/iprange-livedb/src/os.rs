//! The v4.3 Unix file layer: writable mmap reader + writer.
//!
//! Concurrency model (Rule 2): readers take no blocking lock. The writer uses a
//! writable MAP_SHARED mmap. Cross-process coordination via a reader-registration
//! companion file is Phase 3 of SOW-0014 (not yet implemented); until then, the
//! writer takes a brief advisory flock only at open to serialize writer-open.

use alloc::boxed::Box;
use std::fs::File;
use std::path::Path;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::page_store::{MmapStore};
use crate::reader::Reader;
use crate::spec::{self, PAGE_SIZE};
use crate::wire::{read_magic, read_version_major, Meta};
use crate::writer::{Changed, Writer};

/// A read-only mmap of a v4 file. Does NOT hold a blocking lock.
pub struct MmapReader {
    _file: File,
    mmap: memmap2::Mmap,
}

impl MmapReader {
    pub fn open(path: &Path) -> Result<MmapReader> {
        use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
            .map_err(Error::Io)?;

        let meta = file.metadata().map_err(Error::Io)?;
        let len = meta.len() as usize;
        if len < 2 * PAGE_SIZE {
            return Err(Error::Structural("file too small for two meta pages"));
        }
        if (meta.blocks() * 512) < len as u64 {
            return Err(Error::Structural("sparse file (hole)"));
        }

        let mmap = unsafe { memmap2::Mmap::map(&file).map_err(Error::Io)? };

        let page0 = &mmap[..PAGE_SIZE];
        if read_magic(page0) != spec::MAGIC {
            return Err(Error::Structural("bad magic"));
        }
        if read_version_major(page0) != spec::VERSION_MAJOR {
            return Err(Error::Structural("unsupported version_major"));
        }

        Ok(MmapReader { _file: file, mmap })
    }

    pub fn reader(&self) -> Result<Reader<'_>> {
        Reader::open(&self.mmap[..])
    }

    pub fn bytes(&self) -> &[u8] {
        &self.mmap[..]
    }
}

/// A file-backed writer using a writable MAP_SHARED mmap.
pub struct FileWriter<K: IpKey> {
    writer: Writer<K>,
}

impl<K: IpKey> FileWriter<K> {
    pub fn create(path: &Path, scope_mode: u8, created_unixtime: u64) -> Result<FileWriter<K>> {
        use std::io::Write;
        use std::os::unix::fs::OpenOptionsExt;

        let file = std::fs::OpenOptions::new()
            .read(true).write(true).create(true).truncate(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path).map_err(Error::Io)?;

        let w = Writer::<K>::create(scope_mode, created_unixtime)?;
        let image = w.into_image().ok_or(Error::State("expected VecPageStore"))?;
        file.set_len(image.len() as u64).map_err(Error::Io)?;
        (&file).write_all(&image).map_err(Error::Io)?;

        Self::open_with_file(file)
    }

    pub fn open(path: &Path) -> Result<FileWriter<K>> {
        use std::os::unix::fs::OpenOptionsExt;
        use std::os::unix::io::AsRawFd;
        use std::os::unix::fs::FileExt;

        let file = std::fs::OpenOptions::new()
            .read(true).write(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path).map_err(Error::Io)?;

        // Brief LOCK_EX|LOCK_NB to serialize writer-open.
        let fd = file.as_raw_fd();
        if unsafe { libc::flock(fd, libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            return Err(Error::Locked("another writer has the file open"));
        }

        let len = file.metadata().map_err(Error::Io)?.len() as usize;
        if len < 2 * PAGE_SIZE {
            let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
            return Err(Error::Structural("file too small"));
        }

        let mut buf = vec![0u8; 2 * PAGE_SIZE];
        file.read_at(&mut buf, 0).map_err(Error::Io)?;
        if read_magic(&buf[..PAGE_SIZE]) != spec::MAGIC {
            let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };
            return Err(Error::Structural("bad magic"));
        }

        let meta_a = Meta::decode(&buf[..PAGE_SIZE]);
        let meta_b = Meta::decode(&buf[PAGE_SIZE..2 * PAGE_SIZE]);
        let committed_pages = if meta_a.txn_id >= meta_b.txn_id {
            meta_a.total_pages
        } else {
            meta_b.total_pages
        } as u32;

        // Release the open-time lock.
        let _ = unsafe { libc::flock(fd, libc::LOCK_UN) };

        let store = MmapStore::open(file, committed_pages)?;
        let writer = Writer::<K>::open(Box::new(store))?;
        Ok(FileWriter { writer })
    }

    fn open_with_file(file: File) -> Result<FileWriter<K>> {
        use std::os::unix::fs::MetadataExt;
        let len = file.metadata().map_err(Error::Io)?.len() as usize;
        let committed_pages = (len / PAGE_SIZE) as u32;
        let store = MmapStore::open(file, committed_pages)?;
        let writer = Writer::<K>::open(Box::new(store))?;
        Ok(FileWriter { writer })
    }

    // ── delegated hot-path API ────────────────────────────────────────────

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
        self.writer.commit(updated_unixtime)
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

    pub fn close(&mut self) -> Result<()> {
        Ok(()) // drop handles cleanup
    }
}
