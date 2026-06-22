//! Unix file layer for v4: an `mmap` reader and a `pread`/`pwrite` writer, with
//! `flock(2)` (§11) and the §10 open hardening (`O_NOFOLLOW` / `O_CLOEXEC`, `fstat` the
//! fd, `SEEK_HOLE`, re-`fstat`, last-byte probe).
//!
//! The shareable artifact is the v3 snapshot; this live store is a **local** file (NFS
//! unsupported, §11). A corrupt / truncated / hostile file is rejected — never a
//! `SIGBUS`, loop, or out-of-bounds read.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{FileExt, MetadataExt, OpenOptionsExt};
use std::path::Path;
use std::time::{Duration, Instant};

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::reader::Reader;
use crate::spec::PAGE_SIZE;
use crate::writer::Writer;

/// Default bound on how long a writer waits for `LOCK_EX` before a typed timeout (§11);
/// a deployment knob, not part of the format.
pub const DEFAULT_LOCK_WAIT: Duration = Duration::from_secs(30);

fn flock_shared(fd: i32) -> Result<()> {
    // SAFETY: `fd` is a live descriptor owned by the caller's `File`.
    if unsafe { libc::flock(fd, libc::LOCK_SH) } != 0 {
        return Err(Error::Io(io::Error::last_os_error()));
    }
    Ok(())
}

fn flock_exclusive(fd: i32, wait: Duration) -> Result<()> {
    // Bounded-wait `LOCK_EX` via `LOCK_NB` retry (a stalled-reader defense, §11).
    let deadline = Instant::now() + wait;
    loop {
        // SAFETY: `fd` is a live descriptor owned by the caller's `File`.
        if unsafe { libc::flock(fd, libc::LOCK_EX | libc::LOCK_NB) } == 0 {
            return Ok(());
        }
        let e = io::Error::last_os_error();
        if e.raw_os_error() != Some(libc::EWOULDBLOCK) {
            return Err(Error::Io(e));
        }
        if Instant::now() >= deadline {
            return Err(Error::Io(io::Error::new(
                io::ErrorKind::WouldBlock,
                "flock(LOCK_EX) timed out",
            )));
        }
        std::thread::sleep(Duration::from_millis(25));
    }
}

/// A read-only `mmap` of a v4 file, holding `LOCK_SH` for its lifetime (§11). Call
/// [`reader`](Self::reader) once and reuse the returned [`Reader`] for many queries.
#[derive(Debug)]
pub struct MmapReader {
    _file: File, // keeps the fd (and the shared lock) alive
    map: memmap2::Mmap,
}

impl MmapReader {
    /// Open and map `path` read-only with the §10 hardening + `LOCK_SH`. Errors (never
    /// `SIGBUS`/loops) on a symlink final component, sparse hole, truncation, TOCTOU
    /// replacement, or a filesystem without hole detection.
    pub fn open(path: &Path) -> Result<MmapReader> {
        let file = File::options()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)?;
        let fd = file.as_raw_fd();
        flock_shared(fd)?;
        let meta = file.metadata()?;
        if !meta.file_type().is_file() {
            return Err(Error::Structural("not a regular file"));
        }
        let len = meta.len();
        if len < (2 * PAGE_SIZE) as u64 {
            return Err(Error::FileTooShort {
                need: (2 * PAGE_SIZE) as u64,
                have: len,
            });
        }
        // SEEK_HOLE: a hole inside the mapped range would SIGBUS; refuse it (§10).
        // SAFETY: `fd` is live for the lifetime of `file`.
        let hole = unsafe { libc::lseek(fd, 0, libc::SEEK_HOLE) };
        if hole < 0 {
            return Err(Error::Structural(
                "hole detection unavailable — read into a Vec and use Reader::open",
            ));
        }
        if hole as u64 != len {
            return Err(Error::Structural("sparse file (hole) — refusing to mmap"));
        }
        // SAFETY: read-only map of a file treated as immutable under the shared lock.
        let map = unsafe { memmap2::Mmap::map(&file)? };
        let meta2 = file.metadata()?;
        if meta2.len() != len || meta2.ino() != meta.ino() || meta2.dev() != meta.dev() {
            return Err(Error::Structural("file changed during mmap (TOCTOU)"));
        }
        let mut probe = [0u8; 1];
        if !matches!(file.read_at(&mut probe, len - 1), Ok(1)) {
            return Err(Error::Structural("file truncated after fstat (probe failed)"));
        }
        Ok(MmapReader { _file: file, map })
    }

    /// A **validated** reader over the mapped bytes (§9 full validation). Call once and
    /// reuse the returned reader.
    pub fn reader(&self) -> Result<Reader<'_>> {
        Reader::open(&self.map)
    }

    /// The mapped bytes (validate via [`reader`](Self::reader) before trusting them).
    pub fn bytes(&self) -> &[u8] {
        &self.map
    }
}

/// A read/write handle holding `LOCK_EX` (§11): mutate via `set`/`delete`, then
/// `commit` (the two-fsync double-meta protocol, §6.3). Loads the file image into
/// memory (fine for files that fit in RAM; a `pread`-on-demand variant for larger files
/// is future work).
#[derive(Debug)]
pub struct FileWriter<K: IpKey> {
    file: File,
    w: Writer<K>,
}

impl<K: IpKey> FileWriter<K> {
    /// Create a **new** file (must not exist — `O_EXCL`) and write the initial empty DB
    /// durably. Holds `LOCK_EX`.
    pub fn create(
        path: &Path,
        scope_width: u8,
        created_unixtime: u64,
        wait: Duration,
    ) -> Result<Self> {
        let file = File::options()
            .read(true)
            .write(true)
            .create_new(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)?;
        flock_exclusive(file.as_raw_fd(), wait)?;
        let w = Writer::<K>::create(scope_width, created_unixtime);
        let img = w.image();
        file.set_len(img.len() as u64)?;
        file.write_all_at(img, 0)?;
        file.sync_all()?;
        Ok(FileWriter { file, w })
    }

    /// Open an **existing** file for mutation: `LOCK_EX`, read the image, validate +
    /// derive the free set (§6.2 / §7). Rejects a non-regular file.
    pub fn open(path: &Path, wait: Duration) -> Result<Self> {
        let file = File::options()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)?;
        flock_exclusive(file.as_raw_fd(), wait)?;
        let meta = file.metadata()?;
        if !meta.file_type().is_file() {
            return Err(Error::Structural("not a regular file"));
        }
        let mut buf = vec![0u8; meta.len() as usize];
        file.read_exact_at(&mut buf, 0)?;
        let w = Writer::<K>::open_image(buf)?;
        Ok(FileWriter { file, w })
    }

    /// `set([from,to]) = scope` (§8). Not durable until `commit`.
    pub fn set(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
        self.w.set(from, to, scope)
    }

    /// `delete([from,to])` (§8). Not durable until `commit`.
    pub fn delete(&mut self, from: K, to: K) -> Result<()> {
        self.w.delete(from, to)
    }

    /// Commit durably (§6.3): `pwrite` the new data pages, **fsync** (Barrier 1),
    /// `pwrite` the new meta, **fsync** (Barrier 2). On any error the txn is abandoned
    /// with no acknowledged commit; recovery is automatic on the next open.
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        let dirty = self.w.take_dirty();
        self.file.set_len(self.w.image().len() as u64)?; // grow / reclaim trailing
        for &p in &dirty {
            let off = p as usize * PAGE_SIZE;
            self.file
                .write_all_at(&self.w.image()[off..off + PAGE_SIZE], off as u64)?;
        }
        self.file.sync_all()?; // Barrier 1: data durable before the meta references it
        let inactive = self.w.commit_meta(updated_unixtime)?;
        let off = inactive as usize * PAGE_SIZE;
        self.file
            .write_all_at(&self.w.image()[off..off + PAGE_SIZE], off as u64)?;
        self.file.sync_all()?; // Barrier 2: the commit point
        Ok(())
    }

    /// Records in the (pending) tree.
    pub fn record_count(&self) -> u64 {
        self.w.record_count()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;
    use std::sync::atomic::{AtomicU64, Ordering};

    fn temp_path(tag: &str) -> std::path::PathBuf {
        static N: AtomicU64 = AtomicU64::new(0);
        let n = N.fetch_add(1, Ordering::Relaxed);
        std::env::temp_dir().join(format!(
            "iprange-v4-{tag}-{}-{n}.iprdb",
            std::process::id()
        ))
    }

    fn k(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    #[test]
    fn create_commit_mmap_read() {
        let path = temp_path("ccr");
        {
            let mut fw =
                FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            for i in 0..1000u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8]).unwrap();
            }
            fw.commit(0).unwrap();
        } // drop releases LOCK_EX

        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.record_count(), 1000);
        assert_eq!(r.lookup_v4(k(5001)).unwrap(), Some(&[244u8][..])); // i=500 -> 5000..5003
        assert_eq!(r.lookup_v4(k(5005)).unwrap(), None); // gap
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn reopen_mutate_recommit() {
        let path = temp_path("rmr");
        {
            let mut fw =
                FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            // Non-adjacent ranges (gaps) so same-scope records don't coalesce.
            for i in 0..400u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[1]).unwrap();
            }
            fw.commit(1).unwrap();
        }
        {
            let mut fw = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
            assert_eq!(fw.record_count(), 400);
            fw.delete(k(0), k(1999)).unwrap(); // removes i = 0..=199 (200 records)
            fw.set(k(100_000), k(100_000), &[9]).unwrap();
            fw.commit(2).unwrap();
        }
        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.record_count(), 201); // 200 survivors + 1 new
        assert_eq!(r.lookup_v4(k(0)).unwrap(), None); // i=0 deleted
        assert_eq!(r.lookup_v4(k(2000)).unwrap(), Some(&[1u8][..])); // i=200 survives
        assert_eq!(r.lookup_v4(k(100_000)).unwrap(), Some(&[9u8][..]));
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn exclusive_lock_is_mutually_exclusive() {
        let path = temp_path("lock");
        let fw1 = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        // A second writer cannot acquire LOCK_EX while the first holds it.
        let r = FileWriter::<Ipv4Key>::open(&path, Duration::from_millis(150));
        assert!(r.is_err(), "second writer must not acquire the exclusive lock");
        drop(fw1); // release
        let _ = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap(); // now succeeds
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_rejects_symlink_final_component() {
        let target = temp_path("sym-target");
        {
            let _ = FileWriter::<Ipv4Key>::create(&target, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        }
        let link = temp_path("sym-link");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        // O_NOFOLLOW refuses the symlink at the final component.
        assert!(MmapReader::open(&link).is_err());
        let _ = std::fs::remove_file(&link);
        let _ = std::fs::remove_file(&target);
    }

    #[test]
    fn mmap_rejects_too_short() {
        let path = temp_path("short");
        std::fs::write(&path, b"not a v4 file").unwrap();
        assert!(matches!(
            MmapReader::open(&path),
            Err(Error::FileTooShort { .. })
        ));
        let _ = std::fs::remove_file(&path);
    }
}
