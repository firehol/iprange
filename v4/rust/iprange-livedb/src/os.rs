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
use crate::page_store::{MmapPageStore, PageStore};
use crate::reader::{select_active_meta, Reader};
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
/// `commit` (the two-fsync double-meta protocol, §6.3). Uses an mmap-backed page store
/// to avoid loading the whole file into heap memory.
#[derive(Debug)]
pub struct FileWriter<K: IpKey> {
    file: Option<File>,
    w: Writer<K>,
    /// Current mmap size (0 for VecPageStore, file size at open for MmapPageStore,
    /// updated after each successful remap).
    mmap_len: u64,
    /// Set to true after close(). All operations fail once closed.
    closed: bool,
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
        // Build the initial 2-page file (meta A + meta B) in a small heap buffer,
        // write it to disk, fsync. Then reopen through the mmap-backed path so the
        // writer never holds the full DB image in heap memory.
        let w = Writer::<K>::create(scope_width, created_unixtime);
        let img = w.image();
        file.set_len(img.len() as u64)?;
        file.write_all_at(img, 0)?;
        file.sync_all()?;
        drop(w); // release the Vec-backed writer before reopening

        // Reopen through the mmap-backed path (same as open, but the file is
        // already created and locked).
        Self::open_locked(file)
    }

    /// Open an **existing** file for mutation: `LOCK_EX`, mmap the committed range,
    /// validate + derive the free set (§6.2 / §7). Rejects a non-regular file, sparse
    /// committed range, or corrupt metadata.
    pub fn open(path: &Path, wait: Duration) -> Result<Self> {
        let file = File::options()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)?;
        flock_exclusive(file.as_raw_fd(), wait)?;
        Self::open_locked(file)
    }

    /// Shared open logic for a file that is already opened and locked.
    /// Used by both `open` and `create` (after writing the initial 2-page file).
    fn open_locked(file: File) -> Result<Self> {
        let fd = file.as_raw_fd();
        let meta = file.metadata()?;
        if !meta.file_type().is_file() {
            return Err(Error::Structural("not a regular file"));
        }
        let file_len = meta.len();
        if file_len < (2 * PAGE_SIZE) as u64 {
            return Err(Error::FileTooShort {
                need: (2 * PAGE_SIZE) as u64,
                have: file_len,
            });
        }

        // Read the two meta pages first and validate them using the reader's
        // select_active_meta (CRC32C, magic, page header, class 2 checks).
        let mut meta_buf = [0u8; 2 * PAGE_SIZE];
        file.read_exact_at(&mut meta_buf, 0)?;
        let active_meta = select_active_meta(&meta_buf, true)?;
        let total_pages = active_meta.total_pages;
        // Check for overflow: total_pages * PAGE_SIZE must not wrap u64 (§9).
        let committed_len = match total_pages.checked_mul(PAGE_SIZE as u64) {
            Some(v) => v,
            None => {
                return Err(Error::Overflow("total_pages * PAGE_SIZE exceeds u64 range"));
            }
        };
        // The format requires at least 2 pages (the two metas) and enforces a
        // 2^32-page limit. Reject out-of-range values before any mutation.
        if total_pages < 2 {
            return Err(Error::InvalidInput("total_pages < 2"));
        }
        if total_pages >= (1u64 << 32) {
            return Err(Error::InvalidInput("total_pages >= 2^32 (would wrap u32)"));
        }

        // Verify that the committed range fits within the file.
        if file_len < committed_len {
            return Err(Error::FileTooShort {
                need: committed_len,
                have: file_len,
            });
        }

        // Do NOT truncate trailing pages here — a hostile (but CRC-valid) meta with a
        // small total_pages would destroy data. The committed range is mmap'd below;
        // trailing pages are never accessed through it. At commit time the file is
        // extended properly (no holes). The writer holds LOCK_EX, so no reader can see
        // the trailing pages before the first commit cleans them up.

        // Verify that the committed range is hole-free before mmaping.
        // SAFETY: fd is live for the lifetime of file.
        let hole = unsafe { libc::lseek(fd, 0, libc::SEEK_HOLE) };
        if hole < 0 {
            // ENXIO from offset 0 on a non-empty file indicates a genuinely broken
            // file descriptor or filesystem state. Fail closed.
            return Err(Error::Structural(
                "hole detection unavailable — cannot verify committed range is hole-free",
            ));
        }
        if (hole as u64) < committed_len {
            // A hole exists within the committed range — the file is corrupt.
            // Do NOT fall back to a pread-based heap copy (would violate the
            // "file must fit in RAM" requirement).
            return Err(Error::Structural(
                "sparse committed range — refusing to mmap",
            ));
        }

        // mmap only the committed range.
        let map = unsafe {
            memmap2::MmapOptions::new()
                .len(committed_len as usize)
                .map(&file)?
        };

        // Post-map hardening: re-fstat and last-byte probe (same as MmapReader).
        let meta2 = file.metadata()?;
        if meta2.len() != file_len || meta2.ino() != meta.ino() || meta2.dev() != meta.dev() {
            return Err(Error::Structural("file changed during mmap (TOCTOU)"));
        }
        let mut probe = [0u8; 1];
        if !matches!(file.read_at(&mut probe, committed_len - 1), Ok(1)) {
            return Err(Error::Structural(
                "file truncated after fstat (probe failed)",
            ));
        }

        // Create the MmapPageStore and open the writer through it.
        // total_pages is already validated to be < 2^32, so the u64→u32 cast is safe.
        let store: Box<dyn PageStore> = Box::new(MmapPageStore::new(map, total_pages as u32));
        let w = Writer::<K>::open_with_store(store)?;

        Ok(FileWriter {
            file: Some(file),
            w,
            mmap_len: committed_len,
            closed: false,
        })
    }

    /// Refuse operations after close().
    fn check_open(&self) -> Result<()> {
        if self.closed || self.file.is_none() {
            return Err(Error::State("FileWriter is closed"));
        }
        Ok(())
    }

    /// `set([from,to]) = scope` (§8). Not durable until `commit`.
    pub fn set(&mut self, from: K, to: K, scope: &[u8]) -> Result<()> {
        self.check_open()?;
        self.w.set(from, to, scope)
    }

    /// `delete([from,to])` (§8). Not durable until `commit`.
    pub fn delete(&mut self, from: K, to: K) -> Result<()> {
        self.check_open()?;
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
        self.check_open()?;
        self.w.scope_define(name)
    }

    /// Drop a defined scope from the registry (§C.2). Not durable until `commit`.
    pub fn scope_drop(&mut self, scope_id: u32) -> Result<bool> {
        self.check_open()?;
        self.w.scope_drop(scope_id)
    }

    /// A defined scope's name, or `None` (§C.2).
    pub fn scope_name(&self, scope_id: u32) -> Result<Option<Vec<u8>>> {
        self.check_open()?;
        Ok(self.w.scope_name(scope_id))
    }

    /// All defined scopes as `(scope_id, name)`, ascending by id (§C.2).
    pub fn scope_list(&self) -> Result<Vec<(u32, Vec<u8>)>> {
        self.check_open()?;
        Ok(self.w.scope_list())
    }

    /// A scope's version counter, or `None` (§C.2).
    pub fn scope_version(&self, scope_id: u32) -> Result<Option<u64>> {
        self.check_open()?;
        Ok(self.w.scope_version(scope_id))
    }

    /// Set a scope's version counter (§C.2). Not durable until `commit`.
    pub fn scope_set_version(&mut self, scope_id: u32, version: u64) -> Result<bool> {
        self.check_open()?;
        self.w.scope_set_version(scope_id, version)
    }

    /// Increment a scope's version counter (§C.2). Not durable until `commit`.
    pub fn scope_bump_version(&mut self, scope_id: u32) -> Result<bool> {
        self.check_open()?;
        self.w.scope_bump_version(scope_id)
    }

    /// A scope's opaque `type` byte, or `None` (§C.2).
    pub fn scope_type(&self, scope_id: u32) -> Result<Option<u8>> {
        self.check_open()?;
        Ok(self.w.scope_type(scope_id))
    }

    /// Set a scope's opaque `type` byte (§C.2). Not durable until `commit`.
    pub fn scope_set_type(&mut self, scope_id: u32, type_: u8) -> Result<bool> {
        self.check_open()?;
        self.w.scope_set_type(scope_id, type_)
    }

    /// Set `key = (type, value)` on `target` (`0` = FILE, §C.4). Not durable until `commit`.
    pub fn meta_set(&mut self, target: u32, key: &[u8], type_: u32, value: &[u8]) -> Result<()> {
        self.check_open()?;
        self.w.meta_set(target, key, type_, value)
    }

    /// Get `key` on `target` as `(type, value)`, or `None` (§C.4).
    pub fn meta_get(&self, target: u32, key: &[u8]) -> Result<Option<(u32, Vec<u8>)>> {
        self.check_open()?;
        self.w.meta_get(target, key)
    }

    /// Delete `key` on `target` (§C.7). Not durable until `commit`.
    pub fn meta_delete(&mut self, target: u32, key: &[u8]) -> Result<Changed> {
        self.check_open()?;
        self.w.meta_delete(target, key)
    }

    /// List `(key, type, value)` on `target`, ordered by key (§C.4).
    pub fn meta_list(&self, target: u32) -> Result<Vec<MetaEntry>> {
        self.check_open()?;
        self.w.meta_list(target)
    }

    /// Commit durably (§6.3): `pwrite` the new data pages, **fsync** (Barrier 1),
    /// `pwrite` the new meta, **fsync** (Barrier 2). On any error the txn is abandoned
    /// with no acknowledged commit; recovery is automatic on the next open.
    pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
        self.check_open()?;
        // Build the scope-table / KV metadata pages into the store BEFORE collecting the
        // dirty set, so they are pwritten and made durable at Barrier 1 alongside every
        // other data page (§6.3).
        self.w.rebuild_commit_state()?;
        // From here the in-memory writer is partially advanced; poison it on any failure
        // so a half-applied txn can never be observed or reused (the on-disk file stays
        // the last committed valid state, recovered automatically on the next open).
        match self.commit_durable(updated_unixtime) {
            Ok(()) => Ok(()),
            Err(e) => {
                self.w.poison();
                Err(e)
            }
        }
    }

    /// Extend the file to `new_len`, ensuring no holes via a fallback chain.
    /// Targets only the growth region (offset `old_len`, length `new_len - old_len`).
    fn grow_file(fd: std::os::fd::RawFd, old_len: u64, new_len: u64) -> Result<()> {
        if new_len <= old_len {
            // Shrink is not expected (the writer only grows the file). If it happens,
            // refuse: truncating below the committed tree before the meta flip would
            // destroy data. The caller should handle this case separately.
            return Err(Error::InvalidInput("grow_file called with non-growth size"));
        }
        // Grow: avoid sparse holes that would SIGBUS on mmap access.
        // First extend the file to the new size.
        if unsafe { libc::ftruncate(fd, new_len as libc::off_t) } != 0 {
            return Err(Error::Io(io::Error::last_os_error()));
        }
        // Then allocate space for the growth region only (offset old_len,
        // length new_len - old_len). Using offset 0 would zero-fill the
        // entire file including committed pages — data corruption.
        let grow_off = old_len as libc::off_t;
        let grow_len = (new_len - old_len) as libc::off_t;
        #[cfg(target_os = "linux")]
        {
            if unsafe { libc::fallocate(fd, libc::FALLOC_FL_ZERO_RANGE, grow_off, grow_len) } == 0 {
                return Ok(());
            }
        }
        #[cfg(not(target_os = "linux"))]
        {
            if unsafe { libc::posix_fallocate(fd, grow_off, grow_len) } == 0 {
                return Ok(());
            }
        }
        // Fallback: pwrite-zero-fill every page in the grown region.
        // Hoisted out of the loop: the buffer is never mutated.
        let zeros = [0u8; PAGE_SIZE];
        for off in (old_len..new_len).step_by(PAGE_SIZE) {
            if unsafe {
                libc::pwrite(
                    fd,
                    zeros.as_ptr() as *const _,
                    PAGE_SIZE,
                    off as libc::off_t,
                )
            } != PAGE_SIZE as isize
            {
                return Err(Error::Io(io::Error::last_os_error()));
            }
        }
        Ok(())
    }

    /// The on-disk half of [`commit`](Self::commit), run after the metadata rebuild:
    /// pwrite the data pages, fsync (Barrier 1), finalize + pwrite the meta page,
    /// fsync (Barrier 2).
    fn commit_durable(&mut self, updated_unixtime: u64) -> Result<()> {
        // Build the free-list linked list BEFORE finalize so link pages get CRC.
        self.w.build_free_list();
        self.w.finalize_dirty_checksums();
        let dirty = self.w.take_dirty();
        // take_dirty may keep some freed pages in the grown region (to avoid sparse
        // holes on disk). Those need CRC too — finalize the full pwrite set.
        for &p in &dirty {
            let page = self.w.store.write_page_mut(p);
            crate::wire::finalize_checksum(page);
        }
        let new_len = self.w.store.total_pages() * PAGE_SIZE as u64;
        let file = self
            .file
            .as_ref()
            .ok_or(Error::State("FileWriter is closed"))?;
        let fd = file.as_raw_fd();

        // grow_file is only needed for mmap-backed stores (mmap_len > 0) when
        // the file actually grew. VecPageStore uses pwrite which extends the
        // file naturally.
        if self.mmap_len > 0 && new_len > self.mmap_len {
            Self::grow_file(fd, self.mmap_len, new_len)?;
        }

        for &p in &dirty {
            let off = p as u64 * PAGE_SIZE as u64;
            file.write_all_at(self.w.store.page_data(p), off)?;
        }
        file.sync_all()?; // Barrier 1: data durable before the meta references it

        // Truncate trailing sparse pages (from a crashed growth) that are beyond
        // the new committed range while the old meta is still active. If this fails,
        // no commit has been acknowledged and recovery still selects the old meta.
        if self.mmap_len > 0
            && file.metadata()?.len() > new_len
            && unsafe { libc::ftruncate(fd, new_len as libc::off_t) } != 0
        {
            return Err(Error::Io(io::Error::last_os_error()));
        }

        // Remap before the meta flip. This keeps remap failure on the pre-commit
        // side of the contract: callers never get an ordinary error after the
        // commit point has been durably acknowledged.
        if self.mmap_len > 0 && new_len != self.mmap_len {
            self.w.store.remap(fd, new_len)?;
            self.mmap_len = new_len;
        }

        let inactive = self.w.finish_commit_meta(updated_unixtime);
        // Finalize the meta CRC before pwrite.
        {
            let meta_page = self.w.store.write_page_mut(inactive);
            crate::wire::finalize_checksum(meta_page);
        }
        let off = inactive as u64 * PAGE_SIZE as u64;
        file.write_all_at(self.w.store.page_data(inactive), off)?;
        file.sync_all()?; // Barrier 2: the commit point

        self.w.store.clear_dirty();
        Ok(())
    }

    /// Records in the (pending) tree.
    pub fn record_count(&self) -> Result<u64> {
        self.check_open()?;
        Ok(self.w.record_count())
    }

    /// Close the writer, releasing the exclusive lock and cleaning up the mmap.
    /// Uncommitted mutations are discarded. All further operations fail.
    pub fn close(&mut self) -> Result<()> {
        if self.closed {
            return Ok(());
        }
        self.closed = true;
        self.w.store.close();
        let mut unlock_error = None;
        if let Some(file) = self.file.take() {
            // Release the exclusive lock before closing the file. If unlock fails,
            // still drop the fd: close(2) releases flock locks held by this fd.
            if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_UN) } != 0 {
                unlock_error = Some(Error::Io(io::Error::last_os_error()));
            }
            drop(file);
        }
        match unlock_error {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }
}

impl<K: IpKey> Drop for FileWriter<K> {
    fn drop(&mut self) {
        self.w.store.close();
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
            assert_eq!(fw.record_count().unwrap(), 400);
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

    #[test]
    fn mmap_rejects_non_regular_file() {
        // A directory: O_RDONLY|O_NOFOLLOW opens it (the final component is a real dir, not a
        // symlink), but the fstat `is_file()` guard (§10) rejects it before any mmap — a typed
        // `Structural`, never a panic / SIGBUS. Covers the "not a regular file" reject path.
        let dir = temp_path("nonreg-dir");
        std::fs::create_dir(&dir).unwrap();
        let r = MmapReader::open(&dir);
        let _ = std::fs::remove_dir(&dir);
        assert!(matches!(r, Err(Error::Structural(_))), "got {r:?}");
    }

    #[test]
    fn mmap_writer_no_full_file_heap_copy() {
        // Prove that both FileWriter::create and FileWriter::open use MmapPageStore
        // (not a full-file Vec).
        let path = temp_path("noheap");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            // Verify create is mmap-backed (not VecPageStore).
            assert!(
                fw.w.store.as_bytes().is_none(),
                "create must be mmap-backed, not VecPageStore"
            );
            for i in 0..5000u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                    .unwrap();
            }
            fw.commit(0).unwrap();
        }
        // Open with mmap-backed writer.
        let fw = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
        // Verify the store is NOT a VecPageStore (which would hold the full file in heap).
        let is_mmap = fw.w.store.as_bytes().is_none();
        assert!(is_mmap, "mmap-backed writer must not use VecPageStore");
        assert_eq!(fw.record_count().unwrap(), 5000);
        drop(fw);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_growth_and_remap() {
        // Verify that the mmap-backed writer correctly grows the file and remaps.
        // Both create and open are now mmap-backed, so we can start with create
        // and grow directly.
        let path = temp_path("growth");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        // Verify the store is mmap-backed even after create.
        assert!(
            fw.w.store.as_bytes().is_none(),
            "must be mmap-backed after create"
        );
        let mmap_len_before = fw.mmap_len;
        assert_eq!(
            mmap_len_before,
            (2 * PAGE_SIZE) as u64,
            "initial file is 2 meta pages"
        );

        // First txn: insert enough to grow past the initial 2 pages.
        for i in 0..2000u32 {
            fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                .unwrap();
        }
        fw.commit(0).unwrap();
        let pages_after_first = fw.w.store.total_pages();
        let mmap_len_after_first = fw.mmap_len;
        assert!(
            pages_after_first > 2,
            "file must have grown past meta pages"
        );
        assert!(
            mmap_len_after_first > mmap_len_before,
            "mmap_len must have increased after first commit (remap happened)"
        );

        // Second txn: insert more, forcing further growth and remap.
        for i in 2000..4000u32 {
            fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                .unwrap();
        }
        fw.commit(1).unwrap();
        let pages_after_second = fw.w.store.total_pages();
        let mmap_len_after_second = fw.mmap_len;
        assert!(
            pages_after_second > pages_after_first,
            "file must have grown further"
        );
        assert!(
            mmap_len_after_second > mmap_len_after_first,
            "mmap_len must have increased after second commit (remap happened)"
        );

        // Verify the data is correct through the remapped mmap.
        assert_eq!(fw.record_count().unwrap(), 4000);
        assert_eq!(fw.set(k(50_000), k(50_000), &[99]).unwrap(), ());
        fw.commit(2).unwrap();
        assert_eq!(fw.record_count().unwrap(), 4001);
        drop(fw);

        // Reopen and verify through MmapReader.
        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.record_count(), 4001);
        assert_eq!(r.lookup_v4(k(50_000)).unwrap(), Some(&[99u8][..]));
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_close_releases_lock() {
        // Regression: close() must release LOCK_EX so another writer can open.
        let path = temp_path("close-lock");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        fw.close().unwrap();
        // Now should be able to open (lock was released by close).
        let mut fw2 = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
        fw2.close().unwrap();
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_close_closes_fd() {
        // Regression: close() must release the fd immediately, not wait for Drop.
        let path = temp_path("close-fd");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        let fd = fw.file.as_ref().unwrap().as_raw_fd();
        assert!(unsafe { libc::fcntl(fd, libc::F_GETFD) } >= 0);

        fw.close().unwrap();

        assert_eq!(unsafe { libc::fcntl(fd, libc::F_GETFD) }, -1);
        assert_eq!(
            std::io::Error::last_os_error().raw_os_error(),
            Some(libc::EBADF)
        );
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_grow_file_rejects_shrink() {
        // Regression: grow_file must reject non-growth sizes.
        let path = temp_path("grow-shrink");
        let file = std::fs::File::create(&path).unwrap();
        let fd = file.as_raw_fd();
        let r = FileWriter::<Ipv4Key>::grow_file(fd, 100, 50);
        assert!(r.is_err(), "grow_file must reject shrink");
        drop(file);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_rejects_total_pages_overflow() {
        // Regression: a file with total_pages > 2^32 must be rejected.
        let path = temp_path("overflow");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            fw.set(k(1), k(10), &[1]).unwrap();
            fw.commit(0).unwrap();
        }
        // Patch the active meta's total_pages to exceed 2^32.
        let mut data = std::fs::read(&path).unwrap();
        let meta0 = crate::wire::Meta::decode(&data[..PAGE_SIZE]);
        let meta1 = crate::wire::Meta::decode(&data[PAGE_SIZE..2 * PAGE_SIZE]);
        let active = if meta1.txn_id > meta0.txn_id { 1 } else { 0 };
        let base = active as usize * PAGE_SIZE;
        // total_pages is at offset 48 in the meta (u64 little-endian).
        data[base + crate::spec::META_TOTAL_PAGES..base + crate::spec::META_TOTAL_PAGES + 8].copy_from_slice(&(1u64 << 33).to_le_bytes());
        // Re-checksum the page so select_active_meta accepts it.
        crate::wire::finalize_checksum(&mut data[base..base + PAGE_SIZE]);
        std::fs::write(&path, &data).unwrap();

        let r = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT);
        assert!(r.is_err(), "writer must reject total_pages > 2^32");
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_page_after_close_returns_zero() {
        // Regression: page() after close() must return zero page, not panic.
        // Use the mmap-backed path (open an existing file).
        let path = temp_path("page-after-close");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            fw.set(k(1), k(10), &[1]).unwrap();
            fw.commit(0).unwrap();
        }
        let mut fw = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
        fw.close().unwrap();
        // After close, page() should return zero page (not panic or stale data).
        let page = fw.w.store.page(0);
        assert_eq!(page.len(), PAGE_SIZE);
        assert_eq!(page, &[0u8; PAGE_SIZE]);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_rejects_corrupt_meta() {
        // Verify that writer-open rejects a file with both metas corrupt (torn),
        // rather than silently using a corrupt total_pages. Regression for P1 finding.
        let path = temp_path("corrupt-meta");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            fw.set(k(1), k(10), &[1]).unwrap();
            fw.commit(0).unwrap();
        }
        // Corrupt both meta pages by flipping a byte in each checksum-covered region.
        let mut data = std::fs::read(&path).unwrap();
        data[64] ^= 0xFF; // corrupt META-A
        data[PAGE_SIZE + 64] ^= 0xFF; // corrupt META-B
        std::fs::write(&path, &data).unwrap();

        // Writer-open must reject (select_active_meta finds no valid meta).
        let r = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT);
        assert!(
            r.is_err(),
            "writer must reject file with both metas corrupt"
        );
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_reuse_freed_pages() {
        // Verify that the mmap-backed writer reuses freed pages (file doesn't grow unbounded).
        // D7: freed pages are reclaimed at the FOLLOWING commit, so we need two commits
        // after the delete to see reuse.
        let path = temp_path("reuse");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        // Insert enough to grow the file.
        for i in 0..2000u32 {
            fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                .unwrap();
        }
        fw.commit(0).unwrap();
        // Delete half (frees pages into freed_this_txn, reclaimed at commit 2).
        for i in 0..1000u32 {
            fw.delete(k(i * 10), k(i * 10 + 3)).unwrap();
        }
        fw.commit(1).unwrap();
        let pages_after_delete = fw.w.store.total_pages();

        // Reinsert — should reuse pages freed at commit 1.
        for i in 0..1000u32 {
            fw.set(k(i * 10 + 50_000), k(i * 10 + 3 + 50_000), &[5])
                .unwrap();
        }
        fw.commit(2).unwrap();
        let pages_after_reinsert = fw.w.store.total_pages();
        // The file should not have grown significantly (freed pages are reused).
        assert!(
            pages_after_reinsert <= pages_after_delete + 10,
            "freed pages must be reused: {pages_after_delete} -> {pages_after_reinsert}"
        );
        drop(fw);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_rejects_total_pages_zero() {
        // Regression: writer-open must reject total_pages < 2 before any mutation.
        let path = temp_path("tp-zero");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            fw.set(k(1), k(10), &[1]).unwrap();
            fw.commit(0).unwrap();
        }
        // Patch the active meta's total_pages to 0, re-checksum so select_active_meta accepts it.
        let mut data = std::fs::read(&path).unwrap();
        let meta0 = crate::wire::Meta::decode(&data[..PAGE_SIZE]);
        let meta1 = crate::wire::Meta::decode(&data[PAGE_SIZE..2 * PAGE_SIZE]);
        let active = if meta1.txn_id > meta0.txn_id { 1 } else { 0 };
        let base = active as usize * PAGE_SIZE;
        data[base + crate::spec::META_TOTAL_PAGES..base + crate::spec::META_TOTAL_PAGES + 8].copy_from_slice(&0u64.to_le_bytes());
        crate::wire::finalize_checksum(&mut data[base..base + PAGE_SIZE]);
        std::fs::write(&path, &data).unwrap();
        let file_len = data.len() as u64;

        let r = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT);
        assert!(r.is_err(), "writer must reject total_pages == 0");
        // Verify the file was NOT truncated.
        let after = std::fs::metadata(&path).unwrap().len();
        assert_eq!(after, file_len, "writer must not truncate file on reject");
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_rejects_total_pages_eq_2pow32() {
        // Regression: writer-open must reject total_pages >= 2^32 (wraps u32).
        let path = temp_path("tp-2pow32");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            fw.set(k(1), k(10), &[1]).unwrap();
            fw.commit(0).unwrap();
        }
        let mut data = std::fs::read(&path).unwrap();
        let meta0 = crate::wire::Meta::decode(&data[..PAGE_SIZE]);
        let meta1 = crate::wire::Meta::decode(&data[PAGE_SIZE..2 * PAGE_SIZE]);
        let active = if meta1.txn_id > meta0.txn_id { 1 } else { 0 };
        let base = active as usize * PAGE_SIZE;
        data[base + crate::spec::META_TOTAL_PAGES..base + crate::spec::META_TOTAL_PAGES + 8].copy_from_slice(&(1u64 << 32).to_le_bytes());
        crate::wire::finalize_checksum(&mut data[base..base + PAGE_SIZE]);
        std::fs::write(&path, &data).unwrap();

        let r = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT);
        assert!(r.is_err(), "writer must reject total_pages == 2^32");
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_ops_fail_after_close() {
        // Regression: set/commit after close() must fail (no LOCK_EX).
        let path = temp_path("ops-after-close");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        fw.set(k(1), k(10), &[1]).unwrap();
        fw.commit(0).unwrap();
        fw.close().unwrap();
        // Operations after close must fail.
        assert!(fw.set(k(20), k(30), &[2]).is_err());
        assert!(fw.commit(1).is_err());
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_repairs_trailing_sparse_pages() {
        // Regression: a file with trailing sparse pages (from a crashed growth) must
        // be repaired after commit so readers can open it.
        let path = temp_path("trailing-sparse");
        {
            let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
            for i in 0..100u32 {
                fw.set(k(i * 10), k(i * 10 + 3), &[(i & 0xff) as u8])
                    .unwrap();
            }
            fw.commit(0).unwrap();
            // Extend the file with sparse pages (simulate a crashed growth).
            let fd = fw.file.as_ref().unwrap().as_raw_fd();
            let sparse_len = fw.w.store.total_pages() * PAGE_SIZE as u64 * 2;
            if unsafe { libc::ftruncate(fd, sparse_len as libc::off_t) } != 0 {
                panic!("ftruncate failed");
            }
        } // fw dropped here, lock released

        // A reader must reject the sparse file.
        assert!(
            MmapReader::open(&path).is_err(),
            "reader must reject sparse file"
        );

        // Open with writer (mmap-backed), do a small commit.
        let mut fw = FileWriter::<Ipv4Key>::open(&path, DEFAULT_LOCK_WAIT).unwrap();
        fw.set(k(9999), k(9999), &[99]).unwrap();
        fw.commit(1).unwrap();
        fw.close().unwrap();

        // After commit, the trailing sparse pages must be gone — reader can open.
        let mr = MmapReader::open(&path).unwrap();
        let r = mr.reader().unwrap();
        assert_eq!(r.record_count(), 101);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn mmap_writer_scope_meta_fail_after_close() {
        // Regression: scope/meta mutators must fail after close().
        let path = temp_path("scope-after-close");
        let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, DEFAULT_LOCK_WAIT).unwrap();
        fw.set(k(1), k(10), &[1]).unwrap();
        fw.commit(0).unwrap();
        fw.close().unwrap();

        assert!(fw.scope_define(b"x").is_err());
        assert!(fw.scope_set_version(1, 1).is_err());
        assert!(fw.scope_bump_version(1).is_err());
        assert!(fw.scope_set_type(1, 1).is_err());
        assert!(fw.meta_set(0, b"k", 0, b"v").is_err());
        assert!(fw.meta_delete(0, b"k").is_err());
        // Read methods must also fail after close (mmap is unmapped).
        assert!(fw.meta_get(0, b"k").is_err());
        assert!(fw.meta_list(0).is_err());
        assert!(fw.record_count().is_err());
        // scope_drop on non-existent scope returns Ok(false) normally, but after
        // close it must fail.
        assert!(fw.scope_drop(999).is_err());
        let _ = std::fs::remove_file(&path);
    }

    // The remaining §10 mmap-hardening rejects are environment-dependent and not
    // deterministically unit-testable here, so they are exercised via code review + the
    // structural error returns rather than a unit test:
    //   - sparse hole inside the mapped range (SEEK_HOLE != len): requires the FS to actually
    //     punch/keep a hole, which is filesystem- and allocator-dependent (tmpfs/ext4 differ);
    //   - SEEK_HOLE unavailable (lseek returns < 0): requires a filesystem without hole
    //     detection, which cannot be forced on the test host;
    //   - TOCTOU re-fstat mismatch (len/ino/dev changed between fstat and mmap) and the
    //     last-byte probe (truncation after fstat): require a concurrent racing writer, so any
    //     unit test would be inherently flaky.
}
