//! Page-level storage for the v4.3 streaming mmap COW engine.
//!
//! Two implementations:
//! - [`VecPageStore`]: wraps a growable `Vec<u8>` (tests / pure-API path).
//! - [`MmapStore`]: a writable `MAP_SHARED` mmap of the file. All page storage lives
//!   in the mmap — zero heap allocation in the hot path. COW copies go into the file's
//!   growth region `[committed_pages, logical_pages)`. The committed region
//!   `[0, committed_pages)` is never modified in-place (except meta pages 0/1).

use alloc::vec::Vec;

use crate::error::{Error, Result};
use crate::spec::PAGE_SIZE;

/// Page-level storage abstraction. All methods are zero-alloc in the hot path.
pub trait PageStore: Send {
    /// Read a page (immutable). Returns a slice into the backing store.
    fn page(&self, pgno: u32) -> &[u8];

    /// Write into a page (mutable). The caller MUST ensure COW discipline:
    /// never call this on a committed page (pgno < committed_pages) except for
    /// meta pages (pgno 0/1).
    fn page_mut(&mut self, pgno: u32) -> &mut [u8];

    /// Copy `src_pgno`'s full PAGE_SIZE bytes into `dst_pgno`. Both must be valid.
    /// Used by COW to clone a committed page into a growth-region page. Zero-heap.
    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32);

    /// Allocate a new page in the growth region. Returns its pgno.
    /// May grow the backing store (file extend + remap).
    fn alloc_page(&mut self) -> Result<u32>;

    /// Total logical pages (committed + growth region).
    fn total_pages(&self) -> u32;

    /// Number of committed pages (the stable, pre-pending prefix).
    fn committed_pages(&self) -> u32;

    /// Set the committed page count (called at commit time after the meta flip).
    fn set_committed_pages(&mut self, pages: u32);

    /// The committed bytes as a contiguous slice (for Reader construction).
    fn committed_bytes(&self) -> &[u8];

    /// Grow the backing store to at least `min_pages` pages.
    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()>;

    /// Flush dirty pages to disk (msync or fsync).
    fn sync(&self) -> Result<()>;

    /// Truncate the store to exactly `new_total_pages` pages.
    /// Called after commit to shrink the file (Rule 5).
    fn truncate(&mut self, new_total_pages: u32) -> Result<()>;

    /// Grow the file to match logical_pages (for mmap stores, before remap).
    #[cfg(feature = "os")]
    fn file_size(&self) -> Option<u64> {
        None
    }

    /// Consume the boxed store and return its image as a Vec, if backed by
    /// VecPageStore. Returns None for mmap-backed stores.
    fn into_vec(self: Box<Self>) -> Option<alloc::vec::Vec<u8>> {
        None
    }
}

// ── VecPageStore (tests / pure-API) ──────────────────────────────────────────

#[allow(missing_debug_implementations)]
pub struct VecPageStore {
    image: Vec<u8>,
    committed: u32,
}

impl VecPageStore {
    pub fn new(image: Vec<u8>) -> Self {
        let committed = (image.len() / PAGE_SIZE) as u32;
        VecPageStore { image, committed }
    }

    pub fn into_vec(self) -> Vec<u8> {
        self.image
    }
}

impl PageStore for VecPageStore {
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.image[base..base + PAGE_SIZE]
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        let base = pgno as usize * PAGE_SIZE;
        &mut self.image[base..base + PAGE_SIZE]
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        let src = src_pgno as usize * PAGE_SIZE;
        let dst = dst_pgno as usize * PAGE_SIZE;
        self.image.copy_within(src..src + PAGE_SIZE, dst);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        let p = (self.image.len() / PAGE_SIZE) as u32;
        self.image.resize(p as usize * PAGE_SIZE + PAGE_SIZE, 0);
        Ok(p)
    }

    fn total_pages(&self) -> u32 {
        (self.image.len() / PAGE_SIZE) as u32
    }

    fn committed_pages(&self) -> u32 {
        self.committed
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.committed = pages;
    }

    fn committed_bytes(&self) -> &[u8] {
        &self.image[..self.committed as usize * PAGE_SIZE]
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        let needed = min_pages as usize * PAGE_SIZE;
        if self.image.len() < needed {
            self.image.resize(needed, 0);
        }
        Ok(())
    }

    fn sync(&self) -> Result<()> {
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        let new_len = new_total_pages as usize * PAGE_SIZE;
        if new_len < self.image.len() {
            self.image.truncate(new_len);
        }
        Ok(())
    }

    fn into_vec(self: Box<Self>) -> Option<alloc::vec::Vec<u8>> {
        // Safe downcast: consume the Box, extract the VecPageStore's inner Vec.
        // We know the concrete type because only VecPageStore overrides this.
        Some(self.image)
    }
}

// ── MmapStore (writable MAP_SHARED, zero-heap) ──────────────────────────────

#[cfg(feature = "os")]
pub(crate) struct MmapStore {
    mmap: memmap2::MmapMut,
    /// The file handle (for ftruncate/fsync). None after close.
    file: Option<std::fs::File>,
    /// Pages [0, committed) are stable (pre-pending).
    committed: u32,
    /// Pages [0, logical) are allocated (committed + growth).
    logical: u32,
    /// Growth chunk: when extending, over-allocate by this many pages to amortize
    /// remap cost. Starts at 64 pages (256KB) and doubles on each growth.
    growth_chunk: u32,
}

#[cfg(feature = "os")]
impl MmapStore {
    /// Open a writable mmap of `file`. The file must be at least `committed_pages`
    /// pages long. The mapping is PROT_READ|PROT_WRITE, MAP_SHARED.
    pub(crate) fn open(file: std::fs::File, committed_pages: u32) -> Result<Self> {
        let _len = committed_pages as usize * PAGE_SIZE;
        // Grow the mapping ahead to reduce remap frequency.
        let growth_chunk = 64u32;
        let map_len = (committed_pages + growth_chunk) as usize * PAGE_SIZE;

        // Extend the file to the mapping size (fallocate for physical allocation).
        let current_size = { file.metadata().map_err(Error::Io)?.len() as usize };
        if current_size < map_len {
            // ftruncate is sufficient for a writable MAP_SHARED mapping — the
            // kernel allocates physical pages lazily on first write.
            file.set_len(map_len as u64).map_err(Error::Io)?;
        }

        let mmap = unsafe {
            memmap2::MmapOptions::new()
                .map_mut(&file)
                .map_err(Error::Io)?
        };

        // If the mapping is larger than the file content, that's fine — the extra
        // space is zero-filled. We track logical_pages = committed_pages.
        Ok(MmapStore {
            mmap,
            file: Some(file),
            committed: committed_pages,
            logical: committed_pages,
            growth_chunk,
        })
    }

    /// Remap to cover at least `min_pages` pages. Grows the file first (ftruncate),
    /// then creates a new writable mapping.
    fn remap(&mut self, min_pages: u32) -> Result<()> {
        let file = self.file.as_ref().ok_or(Error::State("store closed"))?;
        let new_len = min_pages as usize * PAGE_SIZE;

        // Extend the file.
        file.set_len(new_len as u64).map_err(Error::Io)?;

        // Over-allocate for growth.
        let map_len = (min_pages + self.growth_chunk) as usize * PAGE_SIZE;
        file.set_len(map_len as u64).map_err(Error::Io)?;

        // Create a new writable mapping. `file` is already &File here.
        let new_mmap = unsafe {
            memmap2::MmapOptions::new()
                .map_mut(file)
                .map_err(Error::Io)?
        };
        self.mmap = new_mmap;
        self.growth_chunk = self.growth_chunk.saturating_mul(2).max(self.growth_chunk);
        Ok(())
    }
}

#[cfg(feature = "os")]
impl PageStore for MmapStore {
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * PAGE_SIZE;
        &self.mmap[base..base + PAGE_SIZE]
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        let base = pgno as usize * PAGE_SIZE;
        &mut self.mmap[base..base + PAGE_SIZE]
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        let src = src_pgno as usize * PAGE_SIZE;
        let dst = dst_pgno as usize * PAGE_SIZE;
        self.mmap.copy_within(src..src + PAGE_SIZE, dst);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        let p = self.logical;
        self.logical += 1;
        // Ensure the mapping covers this page.
        let needed = self.logical;
        let mapped_pages = (self.mmap.len() / PAGE_SIZE) as u32;
        if needed > mapped_pages {
            self.remap(needed)?;
        }
        Ok(p)
    }

    fn total_pages(&self) -> u32 {
        self.logical
    }

    fn committed_pages(&self) -> u32 {
        self.committed
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.committed = pages;
    }

    fn committed_bytes(&self) -> &[u8] {
        &self.mmap[..self.committed as usize * PAGE_SIZE]
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        let mapped_pages = (self.mmap.len() / PAGE_SIZE) as u32;
        if min_pages > mapped_pages {
            self.remap(min_pages)?;
        }
        Ok(())
    }

    fn sync(&self) -> Result<()> {
        // msync flushes dirty pages to the page cache.
        self.mmap.flush().map_err(Error::Io)?;
        // fdatasync ensures the page cache is written to stable storage.
        // Without this, a system crash (not just process crash) can lose data.
        if let Some(file) = &self.file {
            use std::os::unix::io::AsRawFd;
            let fd = file.as_raw_fd();
            let rc = unsafe { libc::fdatasync(fd) };
            if rc != 0 {
                return Err(Error::Io(std::io::Error::last_os_error()));
            }
        }
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        let new_len = new_total_pages as usize * PAGE_SIZE;
        // Truncate the backing file, then remap to the smaller size.
        if new_len < self.mmap.len() {
            let file = self.file.as_ref().ok_or(Error::State("store closed"))?;
            file.set_len(new_len as u64).map_err(Error::Io)?;
            self.logical = new_total_pages;
            self.committed = self.committed.min(new_total_pages);
            let new_mmap = unsafe {
                memmap2::MmapOptions::new()
                    .map_mut(file)
                    .map_err(Error::Io)?
            };
            self.mmap = new_mmap;
            self.growth_chunk = 64; // reset growth chunk
        }
        Ok(())
    }

    fn file_size(&self) -> Option<u64> {
        self.file
            .as_ref()
            .map(|f| f.metadata().map(|m| m.len()).unwrap_or(0))
    }
}

#[cfg(feature = "os")]
impl Drop for MmapStore {
    fn drop(&mut self) {
        // The mmap is flushed by the OS on drop; explicit sync is the caller's job.
    }
}
