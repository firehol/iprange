//! Scope table for mode 2 (indirect bitmap interning).
//!
//! Maps scope_id → bitmap. Used when scope_mode == 2 (unlimited feeds).
//! The scope table is a B+tree (page types 4/5) stored in the file.
//!
//! Operations:
//! - intern(bitmap) → scope_id: find-or-create
//! - resolve(scope_id) → bitmap: read back
//!
//! These are metadata operations (feed updates), NOT the per-IP hot path.

use alloc::vec::Vec;
use rustc_hash::FxHashMap;

use crate::error::{Error, Result};
use crate::spec;
use crate::wire::{self, PageHeader};

/// Canonical form of a scope bitmap: trailing zero bytes removed, so two
/// bitmaps with the same set bits map to the same scope_id.
fn canonicalize(bitmap: &[u8]) -> Vec<u8> {
    let mut end = bitmap.len();
    while end > 0 && bitmap[end - 1] == 0 {
        end -= 1;
    }
    bitmap[..end].to_vec()
}

/// Maximum bitmap width in bytes (2048 feeds).
pub const MAX_BITMAP_WIDTH: usize = 256;
/// Scope table leaf entry: scope_id(u32) + bitmap_len(u16) + bitmap(u8[256]) = 262.
pub const SCOPE_ENTRY_SIZE: usize = 4 + 2 + MAX_BITMAP_WIDTH;

/// In-memory scope entry.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ScopeEntry {
    pub scope_id: u32,
    pub bitmap: Vec<u8>,
}

/// In-memory scope registry. Does NOT materialize the committed scope table
/// into a heap HashMap (issue-1/issue-2/issue-7 fix).
///
/// Design:
///   - The committed scope table stays on disk (a B+tree keyed by scope_id).
///     `resolve()` descends it via `find_scope` → O(log S), zero heap.
///   - Only THIS transaction's newly-interned entries live in RAM.
///   - `intern()` dedups against the new set (O(1)); against the committed set
///     it streams the on-disk scope tree via `find_scope_by_bitmap`
///     (O(scope_pages) time, O(1) heap). It NEVER materializes the whole
///     committed table into a HashMap — the old eager load allocated O(S) on
///     the first intern after reopen (issue-7).
///   - `committed_index` is retained ONLY as an in-memory facility for the
///     `from_entries` test helper and the incremental fold performed by
///     `promote` (this-txn new entries, O(new)). It is never populated from
///     disk.
///
/// `committed_bytes` is passed into resolve/intern by the caller (the Writer,
/// which owns the page store). The registry does not hold a reference to it.
#[allow(missing_debug_implementations)]
pub struct ScopeRegistry {
    new_entries: Vec<ScopeEntry>,
    new_bitmap_index: FxHashMap<alloc::vec::Vec<u8>, u32>,
    committed_root: u32,
    /// In-memory bitmap → scope_id index. NEVER loaded from disk (issue-7).
    /// Populated only by `from_entries` (tests) and incrementally by `promote`
    /// (this-session new entries). Production dedup of committed bitmaps goes
    /// through `find_scope_by_bitmap` instead.
    committed_index: Option<FxHashMap<alloc::vec::Vec<u8>, u32>>,
    next_id: u32,
}

impl Default for ScopeRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl ScopeRegistry {
    pub fn new() -> Self {
        ScopeRegistry {
            new_entries: Vec::new(),
            new_bitmap_index: FxHashMap::default(),
            committed_root: 0,
            committed_index: None,
            next_id: 1,
        }
    }

    /// Open a registry over an existing committed scope table WITHOUT loading
    /// it. `next_id` must be max committed scope_id + 1 (caller computes via
    /// `read_max_scope_id`).
    pub fn open(committed_root: u32, next_id: u32) -> Self {
        ScopeRegistry {
            new_entries: Vec::new(),
            new_bitmap_index: FxHashMap::default(),
            committed_root,
            committed_index: None,
            next_id,
        }
    }

    /// Build a registry with a pre-populated committed index (tests). No
    /// on-disk root is referenced, so resolve/intern never touch bytes.
    pub fn from_entries(entries: Vec<ScopeEntry>) -> Self {
        let next_id = entries.iter().map(|e| e.scope_id).max().unwrap_or(0) + 1;
        let mut idx: FxHashMap<alloc::vec::Vec<u8>, u32> = FxHashMap::default();
        for e in &entries {
            idx.insert(e.bitmap.clone(), e.scope_id);
        }
        ScopeRegistry {
            new_entries: Vec::new(),
            new_bitmap_index: FxHashMap::default(),
            committed_root: 0,
            committed_index: Some(idx),
            next_id,
        }
    }

    /// Find or create a scope_id for `bitmap`. Returns (scope_id, was_new).
    ///
    /// Dedup order:
    ///   1. this-txn new entries (O(1) via `new_bitmap_index`)
    ///   2. in-memory `committed_index` if present (O(1); from_entries/promote)
    ///   3. committed on-disk scope tree via `find_scope_by_bitmap`
    ///      (O(scope_pages) time, O(1) heap — issue-7: no eager O(S) load)
    ///   4. miss → mint a new id.
    pub fn intern(&mut self, bitmap: &[u8], committed_bytes: &[u8]) -> Result<(u32, bool)> {
        if let Some(&id) = self.new_bitmap_index.get(bitmap) {
            return Ok((id, false));
        }
        if let Some(ref idx) = self.committed_index {
            if let Some(&id) = idx.get(bitmap) {
                return Ok((id, false));
            }
        }
        if self.committed_root != 0 {
            if let Some(id) = crate::scope_table::find_scope_by_bitmap(
                committed_bytes,
                self.committed_root,
                bitmap,
            ) {
                return Ok((id, false));
            }
        }
        // scope_id 0 is reserved (FILE_SCOPE_ID); valid ids are 1..=u32::MAX.
        // next_id wraps to 0 only after u32::MAX has been minted, or when the
        // opened table already maxed out the id space — either way the id
        // space is exhausted and a new id cannot be minted without
        // wrapping/colliding.
        if self.next_id == 0 {
            return Err(Error::State("scope_id space exhausted"));
        }
        let id = self.next_id;
        self.next_id += 1;
        // to_vec (not to_vec on empty → empty non-nil Vec) keeps an empty
        // bitmap distinguishable from None on resolve.
        let bm = bitmap.to_vec();
        self.new_bitmap_index.insert(bm.clone(), id);
        self.new_entries.push(ScopeEntry {
            scope_id: id,
            bitmap: bm,
        });
        Ok((id, true))
    }

    /// Resolve a scope_id to its bitmap. O(log S) via find_scope for committed
    /// scopes; linear over this-txn new entries (small). Returns an owned Vec
    /// because find_scope decodes from the page image.
    pub fn resolve(&self, scope_id: u32, committed_bytes: &[u8]) -> Option<Vec<u8>> {
        for e in &self.new_entries {
            if e.scope_id == scope_id {
                return Some(e.bitmap.clone());
            }
        }
        if self.committed_root != 0 {
            return find_scope(committed_bytes, self.committed_root, scope_id)
                .ok()
                .flatten();
        }
        None
    }

    /// Zero-copy resolve: returns a slice borrowing the committed page image
    /// (issue-6). For new (this-txn) entries, returns a slice into the entry's
    /// owned bitmap. Avoids the per-call Vec allocation that `resolve` pays —
    /// used by the all-to-all overlap scan which resolves one bitmap per record.
    pub fn resolve_ref<'a>(&'a self, scope_id: u32, committed_bytes: &'a [u8]) -> Option<&'a [u8]> {
        for e in &self.new_entries {
            if e.scope_id == scope_id {
                return Some(&e.bitmap);
            }
        }
        if self.committed_root != 0 {
            return find_scope_ref(committed_bytes, self.committed_root, scope_id)
                .ok()
                .flatten();
        }
        None
    }

    /// Set a feed bit in a bitmap. Returns `None` if the input `scope_id` does
    /// not resolve to a known scope; otherwise `(new_scope_id, created)` where
    /// `created` is true iff a new scope_id had to be minted (the only case
    /// that dirties the scope table).
    pub fn bitmap_set_feed(
        &mut self,
        scope_id: u32,
        feed_bit: u32,
        committed_bytes: &[u8],
    ) -> Result<Option<(u32, bool)>> {
        let bitmap = match self.resolve(scope_id, committed_bytes) {
            Some(b) => b,
            None => return Ok(None),
        };
        let mut new_bitmap = bitmap;
        let byte_idx = (feed_bit / 8) as usize;
        let bit_idx = (feed_bit % 8) as u8;
        if byte_idx >= new_bitmap.len() {
            new_bitmap.resize(byte_idx + 1, 0);
        }
        new_bitmap[byte_idx] |= 1 << bit_idx;
        let (id, created) = self.intern(&new_bitmap, committed_bytes)?;
        Ok(Some((id, created)))
    }

    /// Clear a feed bit from a bitmap. Returns `None` if the input `scope_id`
    /// does not resolve to a known scope; otherwise `(new_scope_id, created)`
    /// (scope_id 0 if the bitmap becomes empty). `created` is true iff a new
    /// scope_id had to be minted. Trailing zero bytes are trimmed so that
    /// clearing the highest set bit returns to the canonical (original) scope.
    pub fn bitmap_clear_feed(
        &mut self,
        scope_id: u32,
        feed_bit: u32,
        committed_bytes: &[u8],
    ) -> Result<Option<(u32, bool)>> {
        let bitmap = match self.resolve(scope_id, committed_bytes) {
            Some(b) => b,
            None => return Ok(None),
        };
        let mut new_bitmap = bitmap;
        let byte_idx = (feed_bit / 8) as usize;
        let bit_idx = (feed_bit % 8) as u8;
        if byte_idx < new_bitmap.len() {
            new_bitmap[byte_idx] &= !(1 << bit_idx);
        }
        let trimmed = canonicalize(&new_bitmap);
        if trimmed.iter().all(|&b| b == 0) {
            return Ok(Some((0, false)));
        }
        let (id, created) = self.intern(&trimmed, committed_bytes)?;
        Ok(Some((id, created)))
    }

    /// Full entry list (committed ∪ new) for the bulk rebuild at commit.
    /// Reads the committed table from disk (no index warming — issue-7) and
    /// appends this-txn new entries.
    pub fn entries_for_commit(&mut self, committed_bytes: &[u8]) -> Vec<ScopeEntry> {
        let mut all: Vec<ScopeEntry> = Vec::new();
        if self.committed_root != 0 {
            if let Ok(committed) = read_all(committed_bytes, self.committed_root) {
                all = committed;
            }
        }
        all.extend(self.new_entries.iter().cloned());
        all
    }

    /// Advance the registry to the newly-committed root: fold this-txn new
    /// entries into the (warm) committed index, clear the new set.
    pub fn promote(&mut self, new_root: u32) {
        if self.committed_index.is_some() {
            if let Some(ref mut idx) = self.committed_index {
                for e in &self.new_entries {
                    idx.insert(e.bitmap.clone(), e.scope_id);
                }
            }
        } else if !self.new_entries.is_empty() {
            let mut idx: FxHashMap<alloc::vec::Vec<u8>, u32> = FxHashMap::default();
            for e in &self.new_entries {
                idx.insert(e.bitmap.clone(), e.scope_id);
            }
            self.committed_index = Some(idx);
        }
        self.new_entries.clear();
        self.new_bitmap_index.clear();
        self.committed_root = new_root;
    }

    pub fn committed_root(&self) -> u32 {
        self.committed_root
    }

    pub fn len(&self) -> usize {
        self.new_entries.len()
    }

    pub fn is_empty(&self) -> bool {
        if !self.new_entries.is_empty() {
            return false;
        }
        self.committed_root == 0
    }
}

/// Sentinel stored in the inline `bitmap_len` field when a scope entry's bitmap
/// is too large for the inline slot (more than `MAX_BITMAP_WIDTH` bytes). The
/// true bitmap then lives in a chain of PAGE_TYPE_OVERFLOW pages; the inline
/// record carries the true length and the first overflow page number.
pub const SCOPE_BITMAP_OVERFLOW: u16 = 0xFFFF;

/// Offset of the next-page pointer within an OVERFLOW page (right after the
/// 16-byte page header).
const OVERFLOW_NEXT_OFF: usize = spec::PAGE_HEADER_SIZE;
/// Offset of the payload within an OVERFLOW page.
const OVERFLOW_PAYLOAD_OFF: usize = spec::PAGE_HEADER_SIZE + 4;

/// Read the bitmap for the entry located at `rec_off` within `page`, following
/// the overflow chain when the inline `bitmap_len` is the overflow sentinel.
/// `bytes` is the full page image (needed to follow overflow pages).
fn read_entry_bitmap(bytes: &[u8], page: &[u8], rec_off: usize) -> Vec<u8> {
    let bitmap_len = wire::u16_le(page, rec_off + 4);
    if bitmap_len == SCOPE_BITMAP_OVERFLOW {
        let true_len = wire::u32_le(page, rec_off + 6) as usize;
        let payload_cap = spec::PAGE_SIZE - OVERFLOW_PAYLOAD_OFF;
        let mut out = Vec::with_capacity(true_len);
        let mut pgno = wire::u32_le(page, rec_off + 10);
        // The chain length is bounded by the declared bitmap length: at most
        // ceil(true_len/payload_cap) pages. Stop after that many so a corrupt
        // or cyclic chain cannot drive an unbounded read here. Validation
        // rejects over-/under-length chains before any normal read reaches them.
        let max_pages = if true_len == 0 {
            0
        } else {
            true_len.div_ceil(payload_cap)
        };
        let mut pages_read = 0usize;
        while pgno != 0 && out.len() < true_len {
            if pages_read >= max_pages {
                break;
            }
            pages_read += 1;
            let base = pgno as usize * spec::PAGE_SIZE;
            if base + spec::PAGE_SIZE > bytes.len() {
                break;
            }
            let opage = &bytes[base..base + spec::PAGE_SIZE];
            let next = wire::u32_le(opage, OVERFLOW_NEXT_OFF);
            let need = (true_len - out.len()).min(payload_cap);
            out.extend_from_slice(&opage[OVERFLOW_PAYLOAD_OFF..OVERFLOW_PAYLOAD_OFF + need]);
            pgno = next;
        }
        out
    } else {
        let n = (bitmap_len as usize).min(MAX_BITMAP_WIDTH);
        page[rec_off + 6..rec_off + 6 + n].to_vec()
    }
}

/// Write a bitmap that exceeds the inline slot to a fresh chain of OVERFLOW
/// pages. Returns the first page number of the chain. The allocated page
/// numbers are recorded in `allocated` (so the writer tracks them as private
/// pages) and removed from `free_pool`.
fn write_overflow_chain(
    store: &mut dyn crate::page_store::PageStore,
    bitmap: &[u8],
    allocated: &mut Vec<u32>,
    free_pool: &mut Vec<u32>,
) -> Result<u32> {
    let payload_cap = spec::PAGE_SIZE - OVERFLOW_PAYLOAD_OFF;
    let n_pages = bitmap.len().div_ceil(payload_cap).max(1);
    let mut pages: Vec<u32> = Vec::with_capacity(n_pages);
    for _ in 0..n_pages {
        let pgno = if let Some(p) = free_pool.pop() {
            p
        } else {
            store.alloc_page()?
        };
        allocated.push(pgno);
        pages.push(pgno);
    }
    for (i, &pgno) in pages.iter().enumerate() {
        let page = store.page_mut(pgno);
        page.fill(0);
        let next = if i + 1 < pages.len() { pages[i + 1] } else { 0 };
        PageHeader::write(page, spec::PAGE_TYPE_OVERFLOW, 0, pgno);
        wire::put_u32(page, OVERFLOW_NEXT_OFF, next);
        let start = i * payload_cap;
        let end = (start + payload_cap).min(bitmap.len());
        page[OVERFLOW_PAYLOAD_OFF..OVERFLOW_PAYLOAD_OFF + (end - start)]
            .copy_from_slice(&bitmap[start..end]);
    }
    Ok(pages[0])
}

/// Encode an inline scope entry into a fixed-size record (SCOPE_ENTRY_SIZE
/// bytes). Only valid for bitmaps that fit inline (len <= MAX_BITMAP_WIDTH);
/// larger bitmaps are handled by [`write_overflow_chain`] at build time.
pub fn encode_entry(out: &mut [u8], entry: &ScopeEntry) {
    debug_assert_eq!(out.len(), SCOPE_ENTRY_SIZE);
    out.fill(0);
    wire::put_u32(out, 0, entry.scope_id);
    let n = entry.bitmap.len().min(MAX_BITMAP_WIDTH);
    wire::put_u16(out, 4, n as u16);
    out[6..6 + n].copy_from_slice(&entry.bitmap[..n]);
}

/// Decode an inline scope entry from a fixed-size record. Overflow entries
/// (sentinel bitmap_len) cannot be decoded from the 262-byte record alone —
/// use [`read_entry_bitmap`] with the full page image.
pub fn decode_entry(rec: &[u8]) -> ScopeEntry {
    debug_assert_eq!(rec.len(), SCOPE_ENTRY_SIZE);
    let scope_id = wire::u32_le(rec, 0);
    let bitmap_len = wire::u16_le(rec, 4);
    let bitmap = if bitmap_len == SCOPE_BITMAP_OVERFLOW {
        // Overflow entries need the full image; from a bare record return empty.
        Vec::new()
    } else {
        let n = (bitmap_len as usize).min(MAX_BITMAP_WIDTH);
        rec[6..6 + n].to_vec()
    };
    ScopeEntry { scope_id, bitmap }
}

/// Build the scope table B+tree into page buffers. Returns the root pgno.
/// Uses simple bulk-loading: sort entries by scope_id, pack into leaf pages,
/// build branch pages on top.
pub fn build_scope_tree(
    store: &mut dyn crate::page_store::PageStore,
    entries: &[ScopeEntry],
    allocated: &mut Vec<u32>,
    free_pool: &mut Vec<u32>,
) -> Result<u32> {
    if entries.is_empty() {
        return Ok(0);
    }

    // Sort by scope_id.
    let mut sorted: Vec<&ScopeEntry> = entries.iter().collect();
    sorted.sort_by_key(|e| e.scope_id);

    let leaf_max = (spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE) / SCOPE_ENTRY_SIZE;

    // Build leaf pages.
    let mut leaf_pgnos: Vec<u32> = Vec::new();

    for chunk in sorted.chunks(leaf_max) {
        let pgno = if let Some(p) = free_pool.pop() {
            p
        } else {
            store.alloc_page()?
        };
        allocated.push(pgno);
        // Spill any oversized bitmaps to OVERFLOW chains BEFORE borrowing the
        // leaf page (the chain writer needs the store). Record each overflow
        // entry's first page by index.
        let mut overflow_first: Vec<(usize, u32)> = Vec::new();
        for (i, entry) in chunk.iter().enumerate() {
            if entry.bitmap.len() > MAX_BITMAP_WIDTH {
                let first = write_overflow_chain(store, &entry.bitmap, allocated, free_pool)?;
                overflow_first.push((i, first));
            }
        }
        let page = store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_SCOPE_LEAF, chunk.len() as u16, pgno);
        for (i, entry) in chunk.iter().enumerate() {
            let off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
            let rec = &mut page[off..off + SCOPE_ENTRY_SIZE];
            if entry.bitmap.len() > MAX_BITMAP_WIDTH {
                rec.fill(0);
                wire::put_u32(rec, 0, entry.scope_id);
                wire::put_u16(rec, 4, SCOPE_BITMAP_OVERFLOW);
                wire::put_u32(rec, 6, entry.bitmap.len() as u32);
                let first = overflow_first
                    .iter()
                    .find(|(idx, _)| *idx == i)
                    .map(|(_, p)| *p)
                    .unwrap();
                wire::put_u32(rec, 10, first);
            } else {
                encode_entry(rec, entry);
            }
        }
        leaf_pgnos.push(pgno);
    }

    if leaf_pgnos.len() == 1 {
        return Ok(leaf_pgnos[0]);
    }

    // Build branch levels bottom-up. Each level is a list of (pgno, min_scope_id)
    // pairs. The separator between two sibling subtrees is the MIN scope_id of
    // the right subtree (= its first child's min, carried up from the level
    // below). This is the only correct way to key a multi-level B+tree: a
    // branch's leftmost separator at the parent level must be the min of that
    // branch's whole subtree, not the min of its second child.
    let sep_width = spec::SCOPE_KEY_WIDTH; // 4
    let branch_max = (spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE - 4) / (sep_width + 4);

    let mut level: Vec<(u32, u32)> = leaf_pgnos
        .iter()
        .map(|&pgno| {
            let page = store.page(pgno);
            // The first entry's scope_id is this leaf's minimum.
            (pgno, wire::u32_le(page, spec::PAGE_HEADER_SIZE))
        })
        .collect();

    while level.len() > 1 {
        let mut next: Vec<(u32, u32)> = Vec::new();
        let mut child_idx = 0;
        while child_idx < level.len() {
            let remaining = level.len() - child_idx;
            let count = remaining.min(branch_max);
            let pgno = if let Some(p) = free_pool.pop() {
                p
            } else {
                store.alloc_page()?
            };
            allocated.push(pgno);
            let page = store.page_mut(pgno);
            page.fill(0);
            // child[0]
            wire::put_u32(page, spec::PAGE_HEADER_SIZE, level[child_idx].0);
            for i in 0..count - 1 {
                let off = spec::PAGE_HEADER_SIZE + 4 + i * (sep_width + 4);
                // Separator between child[i] and child[i+1] = min of child[i+1].
                wire::put_u32(page, off, level[child_idx + i + 1].1);
                wire::put_u32(page, off + sep_width, level[child_idx + i + 1].0);
            }
            PageHeader::write(page, spec::PAGE_TYPE_SCOPE_BRANCH, (count - 1) as u16, pgno);
            // This branch's min is its first child's min.
            next.push((pgno, level[child_idx].1));
            child_idx += count;
        }
        level = next;
    }

    Ok(level[0].0)
}

/// Read all scope entries from a committed scope table. Used at open time.
pub fn read_all(bytes: &[u8], root_pgno: u32) -> Result<Vec<ScopeEntry>> {
    let mut entries = Vec::new();
    if root_pgno == 0 {
        return Ok(entries);
    }
    read_node(bytes, root_pgno, 0, &mut entries)?;
    Ok(entries)
}

/// Read all scope entries, verifying the per-page CRC32C of every scope page.
/// Used at open time to reject a corrupt scope table instead of silently
/// loading garbage data. `total` bounds the page-number space.
pub fn read_all_checked(bytes: &[u8], root_pgno: u32, total: u32) -> Result<Vec<ScopeEntry>> {
    let mut entries = Vec::new();
    if root_pgno == 0 {
        return Ok(entries);
    }
    read_node_checked(bytes, root_pgno, 0, total, &mut entries)?;
    Ok(entries)
}

fn read_node_checked(
    bytes: &[u8],
    pgno: u32,
    depth: u32,
    total: u32,
    out: &mut Vec<ScopeEntry>,
) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("scope table too deep"));
    }
    if pgno as u64 >= total as u64 {
        return Err(Error::Structural("scope table page out of bounds"));
    }
    let off = pgno as usize * spec::PAGE_SIZE;
    if off + spec::PAGE_SIZE > bytes.len() {
        return Err(Error::Structural("scope table page out of bounds"));
    }
    let page = &bytes[off..off + spec::PAGE_SIZE];
    if !crate::crc32c::verify_page(page) {
        return Err(Error::ChecksumFailed("scope table page fails CRC"));
    }
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            let count = h.entry_count as usize;
            for i in 0..count {
                let rec_off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
                let scope_id = wire::u32_le(page, rec_off);
                let bitmap = read_entry_bitmap(bytes, page, rec_off);
                out.push(ScopeEntry { scope_id, bitmap });
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let branch =
                crate::node::BranchView::<crate::key::Ipv4Key>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                read_node_checked(bytes, branch.child(j), depth + 1, total, out)?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("unexpected page type in scope table")),
    }
}

fn read_node(bytes: &[u8], pgno: u32, depth: u32, out: &mut Vec<ScopeEntry>) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("scope table too deep"));
    }
    let off = pgno as usize * spec::PAGE_SIZE;
    if off + spec::PAGE_SIZE > bytes.len() {
        return Err(Error::Structural("scope table page out of bounds"));
    }
    let page = &bytes[off..off + spec::PAGE_SIZE];
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            let count = h.entry_count as usize;
            for i in 0..count {
                let rec_off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
                let scope_id = wire::u32_le(page, rec_off);
                let bitmap = read_entry_bitmap(bytes, page, rec_off);
                out.push(ScopeEntry { scope_id, bitmap });
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let branch =
                crate::node::BranchView::<crate::key::Ipv4Key>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                read_node(bytes, branch.child(j), depth + 1, out)?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("unexpected page type in scope table")),
    }
}

/// Check whether `target_id` is a defined scope, WITHOUT materializing its
/// bitmap. O(log S) time, O(1) heap — used by the open-time data-record scope
/// validation so it does not allocate per record.
pub fn scope_id_exists(bytes: &[u8], root_pgno: u32, target_id: u32) -> Result<bool> {
    Ok(match find_scope_ref(bytes, root_pgno, target_id)? {
        // find_scope_ref returns None for overflow entries (no single slice);
        // those still exist, so fall back to a descent that only checks id
        // presence via a leaf binary search.
        Some(_) => true,
        None => scope_id_leaf_contains(bytes, root_pgno, target_id),
    })
}

/// Descend the scope tree and report whether any leaf holds `target_id`, without
/// allocating the bitmap. Covers overflow entries (which find_scope_ref skips).
fn scope_id_leaf_contains(bytes: &[u8], root_pgno: u32, target_id: u32) -> bool {
    let mut pgno = root_pgno;
    if pgno == 0 {
        return false;
    }
    for _ in 0..spec::TREE_HEIGHT_MAX {
        let off = pgno as usize * spec::PAGE_SIZE;
        if off + spec::PAGE_SIZE > bytes.len() {
            return false;
        }
        let page = &bytes[off..off + spec::PAGE_SIZE];
        match PageHeader::decode(page).page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                let count = page_header_entry_count(page);
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    let rec_off = spec::PAGE_HEADER_SIZE + mid * SCOPE_ENTRY_SIZE;
                    let id = wire::u32_le(page, rec_off);
                    if id < target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                if lo < count {
                    let rec_off = spec::PAGE_HEADER_SIZE + lo * SCOPE_ENTRY_SIZE;
                    return wire::u32_le(page, rec_off) == target_id;
                }
                return false;
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                    page,
                    page_header_entry_count(page),
                );
                let (mut lo, mut hi) = (0usize, branch.sep_count());
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    if branch.sep(mid).0 <= target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                pgno = branch.child(lo);
            }
            _ => return false,
        }
    }
    false
}

/// Read a scope page's entry_count, clamped to a safe capacity for the page
/// type, so a corrupt (CRC-valid) value cannot drive an out-of-range slice.
fn page_header_entry_count(page: &[u8]) -> usize {
    let raw = PageHeader::decode(page).entry_count as usize;
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            raw.min((spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE) / SCOPE_ENTRY_SIZE)
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => raw.min((spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE - 4) / 8),
        _ => raw,
    }
}

/// Find a single scope entry by scope_id via B+tree descent.
/// O(log S) — reads ~3-4 pages for thousands of entries.
pub fn find_scope(bytes: &[u8], root_pgno: u32, target_id: u32) -> Result<Option<Vec<u8>>> {
    if root_pgno == 0 {
        return Ok(None);
    }
    let mut pgno = root_pgno;
    for _ in 0..spec::TREE_HEIGHT_MAX {
        if pgno as usize * spec::PAGE_SIZE + spec::PAGE_SIZE > bytes.len() {
            return Err(Error::Structural("scope table page out of bounds"));
        }
        let off = pgno as usize * spec::PAGE_SIZE;
        let page = &bytes[off..off + spec::PAGE_SIZE];
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                let count = h.entry_count as usize;
                // Binary search the sorted entries by scope_id.
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    let rec_off = spec::PAGE_HEADER_SIZE + mid * SCOPE_ENTRY_SIZE;
                    let id = wire::u32_le(page, rec_off);
                    if id < target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                if lo < count {
                    let rec_off = spec::PAGE_HEADER_SIZE + lo * SCOPE_ENTRY_SIZE;
                    let id = wire::u32_le(page, rec_off);
                    if id == target_id {
                        let bitmap = read_entry_bitmap(bytes, page, rec_off);
                        return Ok(Some(bitmap));
                    }
                }
                return Ok(None);
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                    page,
                    h.entry_count as usize,
                );
                // Binary search for the child index.
                let (mut lo, mut hi) = (0usize, branch.sep_count());
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    let sep = branch.sep(mid).0;
                    if sep <= target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                pgno = branch.child(lo);
            }
            _ => return Err(Error::Structural("unexpected page type in scope table")),
        }
    }
    Err(Error::Invariant(
        "scope table descent exceeded TREE_HEIGHT_MAX",
    ))
}

/// Zero-copy variant of [`find_scope`]: returns a slice borrowing `bytes`
/// (the page image) instead of an owned `Vec<u8>` (issue-6). Used by the
/// all-to-all overlap scan which only needs to iterate set bits, not own the
/// bitmap — avoids one allocation per resolved record.
pub fn find_scope_ref(bytes: &[u8], root_pgno: u32, target_id: u32) -> Result<Option<&[u8]>> {
    if root_pgno == 0 {
        return Ok(None);
    }
    let mut pgno = root_pgno;
    for _ in 0..spec::TREE_HEIGHT_MAX {
        if pgno as usize * spec::PAGE_SIZE + spec::PAGE_SIZE > bytes.len() {
            return Err(Error::Structural("scope table page out of bounds"));
        }
        let off = pgno as usize * spec::PAGE_SIZE;
        let page = &bytes[off..off + spec::PAGE_SIZE];
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                let count = h.entry_count as usize;
                let (mut lo, mut hi) = (0usize, count);
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    let rec_off = spec::PAGE_HEADER_SIZE + mid * SCOPE_ENTRY_SIZE;
                    let id = wire::u32_le(page, rec_off);
                    if id < target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                if lo < count {
                    let rec_off = spec::PAGE_HEADER_SIZE + lo * SCOPE_ENTRY_SIZE;
                    let id = wire::u32_le(page, rec_off);
                    if id == target_id {
                        let bm_len = wire::u16_le(page, rec_off + 4);
                        // Overflow entries span pages and cannot be returned as
                        // a single borrowed slice; the overlap scans that use
                        // this zero-copy path only encounter inline bitmaps.
                        if bm_len == SCOPE_BITMAP_OVERFLOW {
                            return Ok(None);
                        }
                        let n = (bm_len as usize).min(MAX_BITMAP_WIDTH);
                        return Ok(Some(&page[rec_off + 6..rec_off + 6 + n]));
                    }
                }
                return Ok(None);
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                    page,
                    h.entry_count as usize,
                );
                let (mut lo, mut hi) = (0usize, branch.sep_count());
                while lo < hi {
                    let mid = lo + (hi - lo) / 2;
                    let sep = branch.sep(mid).0;
                    if sep <= target_id {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
                pgno = branch.child(lo);
            }
            _ => return Err(Error::Structural("unexpected page type in scope table")),
        }
    }
    Err(Error::Invariant(
        "scope table descent exceeded TREE_HEIGHT_MAX",
    ))
}

/// Find the scope_id of an existing entry whose bitmap equals `target` by
/// streaming the committed scope tree (issue-7). O(scope_pages) time, O(1)
/// heap — replaces the old eager O(S) HashMap materialization. The scope tree
/// is keyed by scope_id (not bitmap), so this is a linear leaf scan; it is
/// only reached on a `new_bitmap_index` miss, which is rare (new feed
/// combinations). Returns `None` if no committed entry matches.
pub fn find_scope_by_bitmap(bytes: &[u8], root_pgno: u32, target: &[u8]) -> Option<u32> {
    if root_pgno == 0 {
        return None;
    }
    find_scope_by_bitmap_node(bytes, root_pgno, 0, target)
}

fn find_scope_by_bitmap_node(bytes: &[u8], pgno: u32, depth: u32, target: &[u8]) -> Option<u32> {
    if depth > spec::TREE_HEIGHT_MAX {
        return None;
    }
    let off = pgno as usize * spec::PAGE_SIZE;
    if off + spec::PAGE_SIZE > bytes.len() {
        return None;
    }
    let page = &bytes[off..off + spec::PAGE_SIZE];
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            let count = h.entry_count as usize;
            for i in 0..count {
                let rec_off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
                let raw_len = wire::u16_le(page, rec_off + 4);
                // Compare the full bitmap (inline or overflow) against target.
                let bm = if raw_len == SCOPE_BITMAP_OVERFLOW {
                    read_entry_bitmap(bytes, page, rec_off)
                } else {
                    let n = (raw_len as usize).min(MAX_BITMAP_WIDTH);
                    page[rec_off + 6..rec_off + 6 + n].to_vec()
                };
                if bm == target {
                    return Some(wire::u32_le(page, rec_off));
                }
            }
            None
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let branch =
                crate::node::BranchView::<crate::key::Ipv4Key>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                if let Some(id) =
                    find_scope_by_bitmap_node(bytes, branch.child(j), depth + 1, target)
                {
                    return Some(id);
                }
            }
            None
        }
        _ => None,
    }
}

/// Return the highest scope_id in the committed scope table by descending to
/// the rightmost leaf (O(log S)). Used at open to compute next_id = max + 1
/// without loading the table.
pub fn read_max_scope_id(bytes: &[u8], root_pgno: u32) -> Option<u32> {
    if root_pgno == 0 {
        return None;
    }
    let mut pgno = root_pgno;
    for _ in 0..spec::TREE_HEIGHT_MAX {
        let off = pgno as usize * spec::PAGE_SIZE;
        if off + spec::PAGE_SIZE > bytes.len() {
            return None;
        }
        let page = &bytes[off..off + spec::PAGE_SIZE];
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                let count = h.entry_count as usize;
                if count == 0 {
                    return None;
                }
                let rec_off = spec::PAGE_HEADER_SIZE + (count - 1) * SCOPE_ENTRY_SIZE;
                return Some(wire::u32_le(page, rec_off));
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                    page,
                    h.entry_count as usize,
                );
                let cc = branch.child_count();
                if cc == 0 {
                    return None;
                }
                pgno = branch.child(cc - 1);
            }
            _ => return None,
        }
    }
    None
}

/// Walk every page of the committed scope tree and verify its per-page CRC32C
/// AND structural integrity WITHOUT materializing entries. O(S pages) time,
/// O(log S) heap (descent stack). Preserves the open-time corruption guard the
/// old eager `read_all_checked` provided, while fixing the heap load.
///
/// Structural checks (entry_count within page capacity, page type matches a
/// scope node, child page numbers in range, reserved byte / self-pgno) are
/// essential because a corrupt entry_count that still verifies against a
/// recomputed CRC would otherwise pass the CRC guard and later cause a
/// slice-bounds panic when the leaf is read. Overflow scope bitmaps (bitmaps
/// larger than the inline MAX_BITMAP_WIDTH slot) are validated chain-by-chain
/// (CRC, type, declared length vs. page count, unused tail, no shared/aliased
/// pages). Branch separators must equal their right child's minimum scope_id.
pub fn validate_scope_crc(bytes: &[u8], root_pgno: u32) -> Result<()> {
    if root_pgno == 0 {
        return Ok(());
    }
    let total_pages = (bytes.len() / spec::PAGE_SIZE) as u32;
    let mut prev_max: Option<u32> = None;
    let mut seen_overflow: alloc::collections::BTreeSet<u32> = alloc::collections::BTreeSet::new();
    validate_scope_crc_node(
        bytes,
        root_pgno,
        0,
        total_pages,
        &mut prev_max,
        &mut seen_overflow,
    )
}

/// Validates the overflow chain for the scope-leaf entry at `rec_off`. Verifies
/// each page's CRC, type, self-pgno, reserved/entry_count fields, that the page
/// count matches the declared bitmap length, that the unused payload tail of the
/// final page is zero, and (via `seen_overflow`) that no overflow page is shared
/// between two scopes. A chain that points at a non-overflow page (e.g. live
/// data/scope-tree data) is rejected by the type check, covering the "overflow
/// page aliases live data" case.
fn validate_overflow_chain(
    bytes: &[u8],
    page: &[u8],
    rec_off: usize,
    total_pages: u32,
    seen_overflow: &mut alloc::collections::BTreeSet<u32>,
) -> Result<()> {
    let true_len = wire::u32_le(page, rec_off + 6);
    // Overflow is only canonical when the bitmap exceeds the inline slot.
    if (true_len as u64) <= MAX_BITMAP_WIDTH as u64 {
        return Err(Error::Structural("noncanonical scope overflow length"));
    }
    let first = wire::u32_le(page, rec_off + 10);
    if first == 0 {
        return Err(Error::Structural(
            "scope overflow chain has zero first page",
        ));
    }
    let payload_cap = (spec::PAGE_SIZE - OVERFLOW_PAYLOAD_OFF) as u64;
    let expected_pages = (true_len as u64).div_ceil(payload_cap);
    let mut pgno = first;
    let mut count: u64 = 0;
    while pgno != 0 {
        count += 1;
        if count > expected_pages {
            return Err(Error::Structural(
                "scope overflow chain longer than declared length",
            ));
        }
        if pgno < 2 || pgno >= total_pages {
            return Err(Error::Structural("scope overflow page out of bounds"));
        }
        if !seen_overflow.insert(pgno) {
            return Err(Error::Structural(
                "scope overflow page owned by multiple scopes",
            ));
        }
        let base = pgno as usize * spec::PAGE_SIZE;
        if base + spec::PAGE_SIZE > bytes.len() {
            return Err(Error::Structural("scope overflow page out of bounds"));
        }
        let opage = &bytes[base..base + spec::PAGE_SIZE];
        if !crate::crc32c::verify_page(opage) {
            return Err(Error::ChecksumFailed("scope overflow page fails CRC"));
        }
        let h = PageHeader::decode(opage);
        if h.page_type != spec::PAGE_TYPE_OVERFLOW {
            return Err(Error::Structural("scope overflow page wrong type"));
        }
        if h.reserved != 0 {
            return Err(Error::NonZeroReserved("scope overflow page reserved byte"));
        }
        if h.entry_count != 0 {
            return Err(Error::Invariant("scope overflow page entry_count nonzero"));
        }
        if h.pgno != pgno {
            return Err(Error::Structural("scope overflow page self-pgno mismatch"));
        }
        // The final page's unused payload tail (beyond the declared length)
        // must be zero so a later reader does not interpret stale bytes.
        if count == expected_pages {
            let used = (true_len as u64) - (expected_pages - 1) * payload_cap;
            let tail_start = OVERFLOW_PAYLOAD_OFF + used as usize;
            if opage[tail_start..].iter().any(|&b| b != 0) {
                return Err(Error::NonZeroReserved("scope overflow nonzero unused tail"));
            }
        }
        pgno = wire::u32_le(opage, OVERFLOW_NEXT_OFF);
    }
    if count != expected_pages {
        return Err(Error::Structural(
            "scope overflow chain shorter than declared length",
        ));
    }
    Ok(())
}

/// Returns the minimum scope_id of the subtree rooted at `pgno` by descending
/// the leftmost child at each level. Returns `None` on a structural error.
fn scope_subtree_min(bytes: &[u8], mut pgno: u32, total_pages: u32) -> Option<u32> {
    for _ in 0..spec::TREE_HEIGHT_MAX {
        if pgno < 2 || pgno >= total_pages {
            return None;
        }
        let off = pgno as usize * spec::PAGE_SIZE;
        if off + spec::PAGE_SIZE > bytes.len() {
            return None;
        }
        let page = &bytes[off..off + spec::PAGE_SIZE];
        let h = PageHeader::decode(page);
        match h.page_type {
            spec::PAGE_TYPE_SCOPE_LEAF => {
                if h.entry_count < 1 {
                    return None;
                }
                return Some(wire::u32_le(page, spec::PAGE_HEADER_SIZE));
            }
            spec::PAGE_TYPE_SCOPE_BRANCH => {
                let bv = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                    page,
                    h.entry_count as usize,
                );
                if bv.child_count() == 0 {
                    return None;
                }
                pgno = bv.child(0);
            }
            _ => return None,
        }
    }
    None
}

fn validate_scope_crc_node(
    bytes: &[u8],
    pgno: u32,
    depth: u32,
    total_pages: u32,
    prev_max: &mut Option<u32>,
    seen_overflow: &mut alloc::collections::BTreeSet<u32>,
) -> Result<()> {
    if depth > spec::TREE_HEIGHT_MAX {
        return Err(Error::Invariant("scope table too deep"));
    }
    if pgno as u64 >= total_pages as u64 {
        return Err(Error::Structural("scope page out of bounds"));
    }
    let off = pgno as usize * spec::PAGE_SIZE;
    if off + spec::PAGE_SIZE > bytes.len() {
        return Err(Error::Structural("scope page out of bounds"));
    }
    let page = &bytes[off..off + spec::PAGE_SIZE];
    if !crate::crc32c::verify_page(page) {
        return Err(Error::ChecksumFailed("scope table page fails CRC"));
    }
    let h = PageHeader::decode(page);
    if h.reserved != 0 {
        return Err(Error::NonZeroReserved("scope page header reserved"));
    }
    if h.pgno != pgno {
        return Err(Error::Structural("scope page self-pgno mismatch"));
    }
    let entry_count = h.entry_count as usize;
    match h.page_type {
        spec::PAGE_TYPE_SCOPE_LEAF => {
            // entry_count MUST be within the page capacity, or a later read
            // computes an offset past the page and panics.
            let max_entries = (spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE) / SCOPE_ENTRY_SIZE;
            if entry_count < 1 || entry_count > max_entries {
                return Err(Error::Invariant("scope leaf entry_count out of range"));
            }
            for i in 0..entry_count {
                let rec_off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
                let id = wire::u32_le(page, rec_off);
                let bitmap_len = wire::u16_le(page, rec_off + 4);
                // An inline bitmap_len beyond the on-disk slot would read past
                // the entry's payload. The overflow sentinel is the one
                // legitimate value > MAX_BITMAP_WIDTH.
                if bitmap_len != SCOPE_BITMAP_OVERFLOW && (bitmap_len as usize) > MAX_BITMAP_WIDTH {
                    return Err(Error::Invariant("scope bitmap_len exceeds payload"));
                }
                if bitmap_len == SCOPE_BITMAP_OVERFLOW {
                    validate_overflow_chain(bytes, page, rec_off, total_pages, seen_overflow)?;
                }
                // Strictly increasing across leaves (prev_max threads the
                // largest scope_id seen so far in the in-order walk).
                if let Some(pm) = *prev_max {
                    if id <= pm {
                        return Err(Error::Invariant(
                            "scope_ids not strictly increasing across leaves",
                        ));
                    }
                }
                *prev_max = Some(id);
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let sep_width = spec::SCOPE_KEY_WIDTH;
            let max_seps = (spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE - 4) / (sep_width + 4);
            if entry_count < 1 || entry_count > max_seps {
                return Err(Error::Invariant(
                    "scope branch separator count out of range",
                ));
            }
            // child_count = sep_count + 1. Each child MUST be a valid page
            // number in [2, total_pages); otherwise descent reads garbage.
            let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(page, entry_count);
            for j in 0..branch.child_count() {
                let child = branch.child(j);
                if child < 2 || child as u64 >= total_pages as u64 {
                    return Err(Error::Structural("scope child pgno out of range"));
                }
            }
            // Each separator MUST equal the minimum scope_id of its right
            // subtree: the scope tree is keyed by scope_id, and a separator that
            // does not match the right child's minimum would mis-route lookups.
            for j in 0..entry_count {
                let sep_off = spec::PAGE_HEADER_SIZE + 4 + j * (sep_width + 4);
                let sep = wire::u32_le(page, sep_off);
                let right_child = branch.child(j + 1);
                match scope_subtree_min(bytes, right_child, total_pages) {
                    Some(min) if min == sep => {}
                    _ => {
                        return Err(Error::Invariant(
                            "scope branch separator does not equal right child minimum",
                        ));
                    }
                }
            }
            for j in 0..branch.child_count() {
                validate_scope_crc_node(
                    bytes,
                    branch.child(j),
                    depth + 1,
                    total_pages,
                    prev_max,
                    seen_overflow,
                )?;
            }
            Ok(())
        }
        _ => Err(Error::Structural("unexpected page type in scope table")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn intern_and_resolve() {
        let mut reg = ScopeRegistry::new();
        let (id1, _) = reg.intern(&[0b00000001], &[]).unwrap(); // feed 0
        let (id2, _) = reg.intern(&[0b00000010], &[]).unwrap(); // feed 1
        let (id1b, _) = reg.intern(&[0b00000001], &[]).unwrap(); // same as id1

        assert_eq!(id1, 1);
        assert_eq!(id2, 2);
        assert_eq!(id1b, 1); // reuse
        assert_eq!(reg.resolve(id1, &[]), Some(vec![0b00000001]));
        assert_eq!(reg.resolve(id2, &[]), Some(vec![0b00000010]));
    }

    #[test]
    fn bitmap_set_clear_feed() {
        let mut reg = ScopeRegistry::new();
        let (empty, _) = reg.intern(&[], &[]).unwrap(); // empty bitmap (presence only)
        let (with_feed0, _) = reg.bitmap_set_feed(empty, 0, &[]).unwrap().unwrap();
        assert_ne!(with_feed0, empty);
        let resolved = reg.resolve(with_feed0, &[]).unwrap();
        assert_eq!(resolved[0] & 1, 1); // bit 0 set

        let (without_feed0, _) = reg.bitmap_clear_feed(with_feed0, 0, &[]).unwrap().unwrap();
        assert_eq!(without_feed0, 0); // empty again
    }

    #[test]
    fn encode_decode_roundtrip() {
        let entry = ScopeEntry {
            scope_id: 42,
            bitmap: vec![0xAB, 0xCD, 0xEF],
        };
        let mut buf = vec![0u8; SCOPE_ENTRY_SIZE];
        encode_entry(&mut buf, &entry);
        let decoded = decode_entry(&buf);
        assert_eq!(decoded.scope_id, 42);
        assert_eq!(decoded.bitmap, vec![0xAB, 0xCD, 0xEF]);
    }

    #[test]
    fn many_entries() {
        let mut reg = ScopeRegistry::new();
        for i in 0..100u32 {
            let mut bitmap = vec![0u8; (i / 8 + 1) as usize];
            bitmap[i as usize / 8] = 1 << (i % 8);
            let _ = reg.intern(&bitmap, &[]).unwrap();
        }
        assert_eq!(reg.len(), 100);
        // Each unique bitmap gets its own scope_id
        for i in 0..100u32 {
            let id = i + 1;
            let bitmap = reg.resolve(id, &[]).unwrap();
            assert!(bitmap[i as usize / 8] & (1 << (i % 8)) != 0);
        }
    }
}
