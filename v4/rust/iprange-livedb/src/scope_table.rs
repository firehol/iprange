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

/// In-memory scope registry. Maintained during a transaction, bulk-rebuilt
/// into the scope table B+tree at commit time.
pub struct ScopeRegistry {
    entries: Vec<ScopeEntry>,
    /// O(1) bitmap → scope_id lookup (fixes #6: was linear search).
    bitmap_index: FxHashMap<alloc::vec::Vec<u8>, u32>,
    next_id: u32,
}

impl ScopeRegistry {
    pub fn new() -> Self {
        ScopeRegistry {
            entries: Vec::new(),
            bitmap_index: FxHashMap::default(),
            next_id: 1,
        }
    }

    /// Load from a committed scope table (at open time).
    pub fn from_entries(entries: Vec<ScopeEntry>) -> Self {
        let next_id = entries.iter().map(|e| e.scope_id).max().unwrap_or(0) + 1;
        let mut bitmap_index: FxHashMap<alloc::vec::Vec<u8>, u32> = FxHashMap::default();
        for e in &entries {
            bitmap_index.insert(e.bitmap.clone(), e.scope_id);
        }
        ScopeRegistry { entries, bitmap_index, next_id }
    }

    /// Find or create a scope_id for the given bitmap.
    /// Returns the scope_id. If the bitmap already exists, reuses its id.
    pub fn intern(&mut self, bitmap: &[u8]) -> u32 {
        // O(1) lookup via bitmap_index.
        if let Some(&id) = self.bitmap_index.get(bitmap) {
            return id;
        }
        let id = self.next_id;
        self.next_id += 1;
        let bm = bitmap.to_vec();
        self.bitmap_index.insert(bm.clone(), id);
        self.entries.push(ScopeEntry { scope_id: id, bitmap: bm });
        id
    }

    /// Resolve a scope_id to its bitmap. Returns None if not found.
    pub fn resolve(&self, scope_id: u32) -> Option<&[u8]> {
        self.entries.iter()
            .find(|e| e.scope_id == scope_id)
            .map(|e| e.bitmap.as_slice())
    }

    /// Set a feed bit in a bitmap. Returns the new scope_id (may differ if
    /// the resulting bitmap is new).
    pub fn bitmap_set_feed(&mut self, scope_id: u32, feed_bit: u32) -> u32 {
        let bitmap = match self.resolve(scope_id) {
            Some(b) => b.to_vec(),
            None => return 0, // unknown scope_id
        };
        let mut new_bitmap = bitmap;
        let byte_idx = (feed_bit / 8) as usize;
        let bit_idx = (feed_bit % 8) as u8;
        if byte_idx >= new_bitmap.len() {
            new_bitmap.resize(byte_idx + 1, 0);
        }
        new_bitmap[byte_idx] |= 1 << bit_idx;
        self.intern(&new_bitmap)
    }

    /// Clear a feed bit from a bitmap. Returns the new scope_id, or 0 if the
    /// bitmap becomes empty (no feeds left).
    pub fn bitmap_clear_feed(&mut self, scope_id: u32, feed_bit: u32) -> u32 {
        let bitmap = match self.resolve(scope_id) {
            Some(b) => b.to_vec(),
            None => return 0,
        };
        let mut new_bitmap = bitmap;
        let byte_idx = (feed_bit / 8) as usize;
        let bit_idx = (feed_bit % 8) as u8;
        if byte_idx < new_bitmap.len() {
            new_bitmap[byte_idx] &= !(1 << bit_idx);
        }
        // Check if bitmap is all-zero → no feeds left.
        if new_bitmap.iter().all(|&b| b == 0) {
            return 0; // empty
        }
        self.intern(&new_bitmap)
    }

    /// All entries (for commit-time bulk rebuild).
    pub fn entries(&self) -> &[ScopeEntry] {
        &self.entries
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }
}

/// Encode a scope entry into a fixed-size record (SCOPE_ENTRY_SIZE bytes).
pub fn encode_entry(out: &mut [u8], entry: &ScopeEntry) {
    debug_assert_eq!(out.len(), SCOPE_ENTRY_SIZE);
    out.fill(0);
    wire::put_u32(out, 0, entry.scope_id);
    wire::put_u16(out, 4, entry.bitmap.len() as u16);
    let n = entry.bitmap.len().min(MAX_BITMAP_WIDTH);
    out[6..6 + n].copy_from_slice(&entry.bitmap[..n]);
}

/// Decode a scope entry from a fixed-size record.
pub fn decode_entry(rec: &[u8]) -> ScopeEntry {
    debug_assert_eq!(rec.len(), SCOPE_ENTRY_SIZE);
    let scope_id = wire::u32_le(rec, 0);
    let bitmap_len = wire::u16_le(rec, 4) as usize;
    let bitmap_len = bitmap_len.min(MAX_BITMAP_WIDTH);
    let bitmap = rec[6..6 + bitmap_len].to_vec();
    ScopeEntry { scope_id, bitmap }
}

/// Build the scope table B+tree into page buffers. Returns the root pgno.
/// Uses simple bulk-loading: sort entries by scope_id, pack into leaf pages,
/// build branch pages on top.
pub fn build_scope_tree(
    store: &mut dyn crate::page_store::PageStore,
    entries: &[ScopeEntry],
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
    let mut seps: Vec<u32> = Vec::new(); // first scope_id of each leaf after the first

    for chunk in sorted.chunks(leaf_max) {
        let pgno = store.alloc_page()?;
        let page = store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_SCOPE_LEAF, chunk.len() as u16, pgno);
        for (i, entry) in chunk.iter().enumerate() {
            let off = spec::PAGE_HEADER_SIZE + i * SCOPE_ENTRY_SIZE;
            encode_entry(&mut page[off..off + SCOPE_ENTRY_SIZE], entry);
        }
        leaf_pgnos.push(pgno);
        // The separator is the first scope_id of the NEXT leaf (not this one).
    }
    // Separators: for leaf[i] (i >= 1), the separator is leaf[i]'s first scope_id.
    for i in 1..leaf_pgnos.len() {
        let page = store.page(leaf_pgnos[i]);
        let first_id = wire::u32_le(page, spec::PAGE_HEADER_SIZE);
        seps.push(first_id);
    }

    if leaf_pgnos.len() == 1 {
        return Ok(leaf_pgnos[0]);
    }

    // Build branch pages (single level for now; handles up to ~7500 leaves).
    let sep_width = spec::SCOPE_KEY_WIDTH; // 4
    let branch_max = (spec::PAGE_SIZE - spec::PAGE_HEADER_SIZE - 4) / (sep_width + 4);

    let mut branch_pgnos: Vec<u32> = Vec::new();
    let mut child_idx = 0;
    while child_idx < leaf_pgnos.len() {
        let remaining = leaf_pgnos.len() - child_idx;
        let count = remaining.min(branch_max);
        let pgno = store.alloc_page()?;
        let page = store.page_mut(pgno);
        page.fill(0);
        // child[0]
        wire::put_u32(page, spec::PAGE_HEADER_SIZE, leaf_pgnos[child_idx]);
        let mut sep_idx = child_idx; // first separator index for this branch
        for i in 0..count - 1 {
            let off = spec::PAGE_HEADER_SIZE + 4 + i * (sep_width + 4);
            let sep = seps[sep_idx];
            wire::put_u32(page, off, sep);
            wire::put_u32(page, off + sep_width, leaf_pgnos[child_idx + i + 1]);
            sep_idx += 1;
        }
        PageHeader::write(page, spec::PAGE_TYPE_SCOPE_BRANCH, (count - 1) as u16, pgno);
        branch_pgnos.push(pgno);
        child_idx += count;
    }

    build_branch_levels(store, &branch_pgnos, &seps, sep_width, branch_max)
}

/// Recursively build branch levels until a single root remains.
fn build_branch_levels(
    store: &mut dyn crate::page_store::PageStore,
    children: &[u32],
    all_seps: &[u32],
    sep_width: usize,
    branch_max: usize,
) -> Result<u32> {
    if children.len() == 1 {
        return Ok(children[0]);
    }

    // Build one level of branches.
    let mut branch_pgnos: Vec<u32> = Vec::new();
    let mut new_seps: Vec<u32> = Vec::new();
    let mut child_idx = 0;
    let mut sep_idx = 0;

    while child_idx < children.len() {
        let remaining = children.len() - child_idx;
        let count = remaining.min(branch_max);
        let pgno = store.alloc_page()?;
        let page = store.page_mut(pgno);
        page.fill(0);
        PageHeader::write(page, spec::PAGE_TYPE_SCOPE_BRANCH, (count - 1) as u16, pgno);
        wire::put_u32(page, spec::PAGE_HEADER_SIZE, children[child_idx]);
        for i in 0..count - 1 {
            let off = spec::PAGE_HEADER_SIZE + 4 + i * (sep_width + 4);
            if sep_idx < all_seps.len() {
                wire::put_u32(page, off, all_seps[sep_idx]);
            }
            wire::put_u32(page, off + sep_width, children[child_idx + i + 1]);
            sep_idx += 1;
        }
        branch_pgnos.push(pgno);
        child_idx += count;
    }

    // Separators for the next level: first scope_id of each branch after the first.
    for i in 1..branch_pgnos.len() {
        // Read the first child's first separator (which is the first scope_id in that subtree).
        // Actually, the separator is already in all_seps — but we need to track which seps
        // belong to which branch level. For simplicity, read the first entry from each branch's
        // leftmost leaf. But that requires descending. Instead, use the separator that was
        // stored at the branch level.
        // For the multi-level case, the separator between branch[i-1] and branch[i] is
        // the first separator stored in branch[i].
        let page = store.page(branch_pgnos[i]);
        let off = spec::PAGE_HEADER_SIZE + 4; // first sep in this branch
        new_seps.push(wire::u32_le(page, off));
    }

    if branch_pgnos.len() == 1 {
        Ok(branch_pgnos[0])
    } else {
        build_branch_levels(store, &branch_pgnos, &new_seps, sep_width, branch_max)
    }
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
                let entry = decode_entry(&page[rec_off..rec_off + SCOPE_ENTRY_SIZE]);
                out.push(entry);
            }
            Ok(())
        }
        spec::PAGE_TYPE_SCOPE_BRANCH => {
            let branch = crate::node::BranchView::<crate::key::Ipv4Key>::new(
                page, h.entry_count as usize,
            );
            for j in 0..branch.child_count() {
                read_node(bytes, branch.child(j), depth + 1, out)?;
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
        let id1 = reg.intern(&[0b00000001]); // feed 0
        let id2 = reg.intern(&[0b00000010]); // feed 1
        let id1b = reg.intern(&[0b00000001]); // same as id1

        assert_eq!(id1, 1);
        assert_eq!(id2, 2);
        assert_eq!(id1b, 1); // reuse
        assert_eq!(reg.resolve(id1), Some(&[0b00000001][..]));
        assert_eq!(reg.resolve(id2), Some(&[0b00000010][..]));
    }

    #[test]
    fn bitmap_set_clear_feed() {
        let mut reg = ScopeRegistry::new();
        let empty = reg.intern(&[]); // empty bitmap (presence only)
        let with_feed0 = reg.bitmap_set_feed(empty, 0);
        assert_ne!(with_feed0, empty);
        let resolved = reg.resolve(with_feed0).unwrap();
        assert_eq!(resolved[0] & 1, 1); // bit 0 set

        let without_feed0 = reg.bitmap_clear_feed(with_feed0, 0);
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
            reg.intern(&bitmap);
        }
        assert_eq!(reg.len(), 100);
        // Each unique bitmap gets its own scope_id
        for i in 0..100u32 {
            let id = i + 1;
            let bitmap = reg.resolve(id).unwrap();
            assert!(bitmap[i as usize / 8] & (1 << (i % 8)) != 0);
        }
    }
}
