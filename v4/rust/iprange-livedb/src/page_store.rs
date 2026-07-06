use alloc::boxed::Box;
use alloc::vec::Vec;

#[cfg(feature = "os")]
use rustc_hash::FxHashMap;

use crate::error::Result;
use crate::spec::PAGE_SIZE;

/// Page-level storage abstraction for the v4 writer.
///
/// Two implementations:
/// - [`VecPageStore`]: wraps a `Vec<u8>` (current in-memory behavior). Used by tests
///   and the pure-API path (`Writer::create`, `open_image` from a buffer).
/// - [`MmapPageStore`]: wraps a read-only `Mmap` + dirty-page `HashMap`. Used by the
///   file-backed writer to avoid loading the whole file into heap memory.
///
/// Some methods (`write_page`, `truncate`, `page_data`, `clear_dirty`, `remap`, `close`)
/// are only used by the OS file layer (`os` feature). They are part of the trait contract
/// and will be exercised once the OS layer is updated.
#[allow(dead_code)]
pub(crate) trait PageStore: Send + Sync {
    /// Read a page. Checks the dirty map first (hit → return dirty data),
    /// then falls back to the committed source (mmap or Vec). The Writer
    /// core reads both committed pages (initial COW descent) and dirty pages
    /// (after COW: rebalance, lookup_covering, contains_from, lookup_ge,
    /// descend_to_leaf, scan_node all traverse from root_pgno which may
    /// point at dirty pages after the first COW in a txn).
    fn page(&self, pgno: u32) -> &[u8];

    /// Get a mutable reference to a page's storage. For VecPageStore: returns
    /// a slice into the Vec (zero-copy). For MmapPageStore: returns a slice
    /// into a recycled dirty buffer from the dirty map (Entry API, ~2ns
    /// FxHashMap lookup). Used by write_leaf/write_branch/write_meta to
    /// write directly into the destination, avoiding the stack buffer + memcpy.
    fn write_page_mut(&mut self, pgno: u32) -> &mut [u8];

    /// Extend the store by one page (called only when the Writer's free list
    /// is empty). Returns the new page number. For VecPageStore, also resizes
    /// the underlying Vec by PAGE_SIZE bytes. For MmapPageStore, no memory
    /// allocation is required (logical_pages is just a counter; the mmap is
    /// extended later via remap() at commit time).
    fn alloc_page(&mut self) -> u32;

    /// Total logical pages in the store (committed + allocated this txn).
    /// Returns u64 to match all existing callers (kv::get, kv::list,
    /// kv::collect_pages, Meta::total_pages). The format enforces a
    /// `total_pages < 2^32` limit, so the u64 return is always u32-storable.
    fn total_pages(&self) -> u64;

    /// Return the committed bytes as a contiguous slice.
    /// For VecPageStore: returns the Vec. For MmapPageStore: returns the mmap
    /// (sized to exactly `total_pages * PAGE_SIZE` — no trailing garbage).
    fn committed_bytes(&self) -> &[u8];

    /// Return the bytes for a specific dirty page.
    /// Used by the OS layer at commit time to obtain page data for pwrite.
    /// For VecPageStore: returns the Vec slice directly. For MmapPageStore:
    /// returns from dirty map (or panics if not dirty — the OS layer only
    /// calls this for pages in the dirty set).
    fn page_data(&self, pgno: u32) -> &[u8];

    /// Clear all dirty pages. Called after a successful commit, after the
    /// OS layer has finished reading dirty page data for pwrite. The next
    /// txn starts with an empty dirty map; committed pages are read from
    /// the (remapped) mmap or Vec. Recycled buffers are kept alive for
    /// reuse in the next txn (zero allocation in steady state).
    fn clear_dirty(&mut self);

    /// Extract the inner Vec (VecPageStore only). Returns None for MmapPageStore.
    /// Used by Writer::into_image() to avoid a copy when the writer is Vec-backed.
    fn into_vec(self: Box<Self>) -> Option<Vec<u8>>;

    /// Borrow the inner Vec bytes (VecPageStore only). Returns None for MmapPageStore
    /// (no Vec to borrow — image() falls back to committed_bytes()).
    /// Used by Writer::image() to return the Vec directly when available.
    fn as_bytes(&self) -> Option<&[u8]>;

    /// Remap the mmap to a new size (MmapPageStore only). VecPageStore no-ops.
    /// Called after the file has been extended via ftruncate/fallocate.
    /// On failure, the old mapping is preserved (the mmap syscall does not
    /// destroy the old mapping if the new one fails). The caller must poison
    /// the writer — the on-disk file is valid but the in-memory state cannot
    /// continue safely.
    /// Takes a raw fd because MmapPageStore does not own the file descriptor
    /// (FileWriter does). The fd is needed for the new mmap() call on
    /// platforms without mremap (macOS, FreeBSD).
    /// Uses i32 (same as RawFd on Unix) to avoid std dependency in the trait.
    fn remap(&mut self, fd: i32, new_size: u64) -> Result<()>;

    /// Release resources (mmap munmap for MmapPageStore). VecPageStore no-ops.
    /// Called from FileWriter::close() / Drop to prevent mmap leaks.
    /// Must be idempotent (safe to call multiple times).
    fn close(&mut self);
}

/// In-memory page store backed by a `Vec<u8>`. Zero-copy reads and writes.
pub(crate) struct VecPageStore {
    image: Vec<u8>,
}

impl VecPageStore {
    pub(crate) fn new(image: Vec<u8>) -> Self {
        VecPageStore { image }
    }
}

impl PageStore for VecPageStore {
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.image[base..base + PAGE_SIZE]
    }

    fn write_page_mut(&mut self, pgno: u32) -> &mut [u8] {
        let base = pgno as usize * PAGE_SIZE;
        &mut self.image[base..base + PAGE_SIZE]
    }

    fn alloc_page(&mut self) -> u32 {
        let p = (self.image.len() / PAGE_SIZE) as u32;
        self.image.resize(self.image.len() + PAGE_SIZE, 0);
        p
    }

    fn total_pages(&self) -> u64 {
        (self.image.len() / PAGE_SIZE) as u64
    }

    fn committed_bytes(&self) -> &[u8] {
        &self.image
    }

    fn page_data(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.image[base..base + PAGE_SIZE]
    }

    fn clear_dirty(&mut self) {
        // VecPageStore has no dirty map — no-op.
    }

    fn into_vec(self: Box<Self>) -> Option<Vec<u8>> {
        Some(self.image)
    }

    fn as_bytes(&self) -> Option<&[u8]> {
        Some(&self.image)
    }

    fn remap(&mut self, _fd: i32, _new_size: u64) -> Result<()> {
        Ok(())
    }

    fn close(&mut self) {
        // VecPageStore has no mmap — no-op.
    }
}

/// Mmap-backed page store. Reads committed pages from a read-only mmap;
/// stores dirty/new pages in a private `FxHashMap<u32, Vec<u8>>`.
///
/// Buffer recycling: `clear_dirty()` moves dirty `Vec<u8>` buffers into a
/// recycled pool instead of dropping them. The next txn pops them from the
/// pool — zero heap allocation per dirty page in steady state.
#[cfg(feature = "os")]
#[allow(dead_code)]
pub(crate) struct MmapPageStore {
    mmap: Option<memmap2::Mmap>,
    /// Number of pages in the mmap (committed pages at open time).
    committed_pages: u32,
    /// Logical page count (committed + allocated this txn).
    logical_pages: u32,
    /// Dirty pages: pgno → buffer.
    dirty: FxHashMap<u32, Vec<u8>>,
    /// Recycled buffers from previous txns.
    pool: Vec<Vec<u8>>,
}

#[cfg(feature = "os")]
#[allow(dead_code)]
impl MmapPageStore {
    pub(crate) fn new(mmap: memmap2::Mmap, committed_pages: u32) -> Self {
        MmapPageStore {
            mmap: Some(mmap),
            committed_pages,
            logical_pages: committed_pages,
            dirty: FxHashMap::default(),
            pool: Vec::new(),
        }
    }
}

#[cfg(feature = "os")]
impl PageStore for MmapPageStore {
    fn page(&self, pgno: u32) -> &[u8] {
        // Fast path: no pages dirty this txn → skip the map lookup entirely
        // (common in append-only txns that only read committed pages during descent).
        if !self.dirty.is_empty() {
            if let Some(buf) = self.dirty.get(&pgno) {
                return buf.as_slice();
            }
        }
        // Fall back to mmap for committed pages.
        if pgno < self.committed_pages {
            if let Some(ref mmap) = self.mmap {
                let base = pgno as usize * PAGE_SIZE;
                return &mmap[base..base + PAGE_SIZE];
            }
        }
        // Pages allocated but not yet written this txn, pages beyond the
        // committed file size, or after close() — return a static zero page.
        static ZERO_PAGE: [u8; PAGE_SIZE] = [0u8; PAGE_SIZE];
        &ZERO_PAGE
    }

    fn write_page_mut(&mut self, pgno: u32) -> &mut [u8] {
        use alloc::vec;
        let entry = self.dirty.entry(pgno).or_insert_with(|| {
            // Try to pop a recycled buffer from the pool first.
            self.pool.pop().unwrap_or_else(|| vec![0u8; PAGE_SIZE])
        });
        entry.as_mut_slice()
    }

    fn alloc_page(&mut self) -> u32 {
        let p = self.logical_pages;
        self.logical_pages = self.logical_pages.saturating_add(1);
        p
    }

    fn total_pages(&self) -> u64 {
        self.logical_pages as u64
    }

    fn committed_bytes(&self) -> &[u8] {
        if let Some(ref mmap) = self.mmap {
            let len = self.committed_pages as usize * PAGE_SIZE;
            &mmap[..len]
        } else {
            &[]
        }
    }

    fn page_data(&self, pgno: u32) -> &[u8] {
        // The OS layer guarantees it only calls this for pages in the dirty set.
        &self.dirty[&pgno]
    }

    fn clear_dirty(&mut self) {
        let txn_dirty_count = self.dirty.len();
        // Move all dirty buffers into the recycled pool.
        for (_, buf) in self.dirty.drain() {
            self.pool.push(buf);
        }
        // Trim the pool if it is more than 2x larger than the current txn's
        // dirty page count. Prevents a single anomalous large txn from
        // permanently inflating RSS.
        if self.pool.len() > txn_dirty_count * 2 {
            self.pool.truncate(txn_dirty_count);
        }
    }

    fn into_vec(self: Box<Self>) -> Option<Vec<u8>> {
        None
    }

    fn as_bytes(&self) -> Option<&[u8]> {
        None
    }

    fn remap(&mut self, fd: i32, new_size: u64) -> Result<()> {
        let new_mmap = unsafe { memmap2::MmapOptions::new().len(new_size as usize).map(fd)? };
        self.mmap = Some(new_mmap);
        self.committed_pages = (new_size as usize / PAGE_SIZE) as u32;
        Ok(())
    }

    fn close(&mut self) {
        // Drop the mmap — memmap2::Mmap unmaps on drop.
        self.mmap = None;
    }
}
