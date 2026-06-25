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
use crate::writer::{Changed, MetaEntry, Writer};

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
///
/// **Lock contract (§11):** the mmap'd bytes are only valid while mapped under the lock,
/// so an `MmapReader` necessarily holds `LOCK_SH` across the caller's queries. This *is*
/// the read session's locked window — follow the **open → read → drop** model and keep it
/// short-lived; a writer's `LOCK_EX` is blocked while any reader holds `LOCK_SH`, so do
/// not retain an idle `MmapReader`.
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
            return Err(Error::Structural(
                "file truncated after fstat (probe failed)",
            ));
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

    // --- v4.1 scope registry + per-scope metadata (§C) ---
    //
    // Thin delegations to the inner writer so the daemon can mutate scope/metadata on the
    // live DB (scope-api spec: "writer = the daemon"). Buffered like range edits; the new
    // scope-table/KV pages are built into the image at `commit` and made durable together
    // with the data at Barrier 1 (see `commit` / `rebuild_commit_state`).

    /// Define a new scope, returning its `scope_id` (§C.2). Not durable until `commit`.
    pub fn scope_define(&mut self, name: &[u8]) -> Result<u32> {
        self.w.scope_define(name)
    }

    /// Drop a defined scope from the registry (§C.2). Not durable until `commit`.
    pub fn scope_drop(&mut self, scope_id: u32) -> Result<bool> {
        self.w.scope_drop(scope_id)
    }

    /// A defined scope's name, or `None` (§C.2).
    pub fn scope_name(&self, scope_id: u32) -> Option<Vec<u8>> {
        self.w.scope_name(scope_id)
    }

    /// All defined scopes as `(scope_id, name)`, ascending by id (§C.2).
    pub fn scope_list(&self) -> Vec<(u32, Vec<u8>)> {
        self.w.scope_list()
    }

    /// A scope's version counter, or `None` (§C.2).
    pub fn scope_version(&self, scope_id: u32) -> Option<u64> {
        self.w.scope_version(scope_id)
    }

    /// Set a scope's version counter (§C.2). Not durable until `commit`.
    pub fn scope_set_version(&mut self, scope_id: u32, version: u64) -> Result<bool> {
        self.w.scope_set_version(scope_id, version)
    }

    /// Increment a scope's version counter (§C.2). Not durable until `commit`.
    pub fn scope_bump_version(&mut self, scope_id: u32) -> Result<bool> {
        self.w.scope_bump_version(scope_id)
    }

    /// A scope's opaque `type` byte, or `None` (§C.2).
    pub fn scope_type(&self, scope_id: u32) -> Option<u8> {
        self.w.scope_type(scope_id)
    }

    /// Set a scope's opaque `type` byte (§C.2). Not durable until `commit`.
    pub fn scope_set_type(&mut self, scope_id: u32, type_: u8) -> Result<bool> {
        self.w.scope_set_type(scope_id, type_)
    }

    /// Set `key = (type, value)` on `target` (`0` = FILE, §C.4). Not durable until `commit`.
    pub fn meta_set(&mut self, target: u32, key: &[u8], type_: u32, value: &[u8]) -> Result<()> {
        self.w.meta_set(target, key, type_, value)
    }

    /// Get `key` on `target` as `(type, value)`, or `None` (§C.4).
    pub fn meta_get(&self, target: u32, key: &[u8]) -> Result<Option<(u32, Vec<u8>)>> {
        self.w.meta_get(target, key)
    }

    /// Delete `key` on `target` (§C.7). Not durable until `commit`.
    pub fn meta_delete(&mut self, target: u32, key: &[u8]) -> Result<Changed> {
        self.w.meta_delete(target, key)
    }

    /// List `(key, type, value)` on `target`, ordered by key (§C.4).
    pub fn meta_list(&self, target: u32) -> Result<Vec<MetaEntry>> {
        self.w.meta_list(target)
    }

    /// Commit durably (§6.3): `pwrite` the new data pages, **fsync** (Barrier 1),
    /// `pwrite` the new meta, **fsync** (Barrier 2). On any error the txn is abandoned
    /// with no acknowledged commit; recovery is automatic on the next open.
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        // Build the scope-table / KV metadata pages into the image BEFORE collecting the dirty
        // set, so they are pwritten and made durable at Barrier 1 alongside every other data
        // page (§6.3). Doing this after `take_dirty` (the previous order) stranded them: the
        // flipped meta referenced pages that were never written → a corrupt file on reopen.
        self.w.rebuild_commit_state()?;
        // From here the in-memory writer is partially advanced; poison it on any failure so a
        // half-applied txn can never be observed or reused (the on-disk file stays the last
        // committed valid state, recovered automatically on the next open).
        match self.commit_durable(updated_unixtime) {
            Ok(()) => Ok(()),
            Err(e) => {
                self.w.poison();
                Err(e)
            }
        }
    }

    /// The on-disk half of [`commit`](Self::commit), run after the metadata rebuild: pwrite
    /// the data pages, fsync (Barrier 1), finalize + pwrite the meta page, fsync (Barrier 2).
    fn commit_durable(&mut self, updated_unixtime: u64) -> Result<()> {
        let dirty = self.w.take_dirty();
        self.file.set_len(self.w.image().len() as u64)?; // grow / reclaim trailing
        for &p in &dirty {
            let off = p as usize * PAGE_SIZE;
            self.file
                .write_all_at(&self.w.image()[off..off + PAGE_SIZE], off as u64)?;
        }
        self.file.sync_all()?; // Barrier 1: data durable before the meta references it
        let inactive = self.w.finish_commit_meta(updated_unixtime);
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
        std::env::temp_dir().join(format!("iprange-v4-{tag}-{}-{n}.iprdb", std::process::id()))
    }

    fn k(n: u32) -> Ipv4Key {
        Ipv4Key(n)
    }

    // `scope_define` assigns ids monotonically from 1 (0 = FILE), so the first two defined
    // scopes get ids 1 and 2 (asserted at definition in the tests below).
    fn a_id() -> u32 {
        1
    }
    fn b_id() -> u32 {
        2
    }

    #[test]
    fn create_commit_mmap_read() {
        let path = temp_path("ccr");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            for i in 0..1000u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                    .unwrap();
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
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
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
    fn create_commit_metadata_reopen() {
        // The file-backed commit must persist the scope-table + KV pages that are BUILT at
        // commit time. Regression (codex Finding 1): the metadata rebuild ran AFTER the dirty
        // set was drained, so those pages were never pwritten and the reopened file referenced
        // unwritten pages → corruption. This covers a defined-scope KV (text + overflow-
        // spanning binary) and a FILE-target KV, alongside real IP data.
        let path = temp_path("meta");
        let big = vec![0xABu8; 9000]; // multi-page overflow value
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            for i in 0..50u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[7]).unwrap();
            }
            let a = fw.scope_define(b"scope-a").unwrap();
            let b = fw.scope_define(b"scope-b").unwrap();
            assert_eq!((a, b), (a_id(), b_id())); // ids assigned 1, 2
            fw.scope_set_type(a, 5).unwrap();
            fw.scope_bump_version(b).unwrap();
            fw.meta_set(a, b"license", 0, b"MIT").unwrap();
            fw.meta_set(a, b"blob", 9, &big).unwrap(); // overflow-spanning binary value
            fw.meta_set(crate::spec::FILE_SCOPE_ID, b"dataset", 0, b"firehol")
                .unwrap();
            fw.commit(1).unwrap();
        } // drop releases LOCK_EX

        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.record_count(), 50);
        assert_eq!(r.scope_list().len(), 2);
        assert_eq!(r.scope_name(a_id()).as_deref(), Some(&b"scope-a"[..]));
        assert_eq!(r.scope_type(a_id()), Some(5));
        assert_eq!(r.scope_version(b_id()), Some(1));
        assert_eq!(
            r.meta_get(a_id(), b"license").unwrap(),
            Some((0, b"MIT".to_vec()))
        );
        assert_eq!(r.meta_get(a_id(), b"blob").unwrap(), Some((9, big.clone())));
        assert_eq!(
            r.meta_get(crate::spec::FILE_SCOPE_ID, b"dataset").unwrap(),
            Some((0, b"firehol".to_vec()))
        );
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn reopen_mutate_metadata_recommit() {
        // Incremental metadata mutation on an EXISTING file (open → meta_set → commit) must
        // rebuild and persist the changed pages across the two-fsync commit.
        let path = temp_path("meta2");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            let a = fw.scope_define(b"a").unwrap();
            assert_eq!(a, a_id()); // id assigned 1
            fw.meta_set(a, b"k", 0, b"v1").unwrap();
            fw.commit(1).unwrap();
        }
        {
            let mut fw = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
            assert_eq!(
                fw.meta_get(a_id(), b"k").unwrap(),
                Some((0, b"v1".to_vec()))
            );
            fw.meta_set(a_id(), b"k", 0, b"v2").unwrap();
            fw.meta_set(a_id(), b"k2", 0, b"x").unwrap();
            fw.commit(2).unwrap();
        }
        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.meta_get(a_id(), b"k").unwrap(), Some((0, b"v2".to_vec())));
        assert_eq!(r.meta_get(a_id(), b"k2").unwrap(), Some((0, b"x".to_vec())));
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn exclusive_lock_is_mutually_exclusive() {
        let path = temp_path("lock");
        let fw1 = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        // A second writer cannot acquire LOCK_EX while the first holds it.
        let r = FileWriter::<Ipv4Key>::open(&path, Duration::from_millis(150));
        assert!(
            r.is_err(),
            "second writer must not acquire the exclusive lock"
        );
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
