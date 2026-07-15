//! All-to-all feed overlap accumulation.
//!
//! Scans a multi-feed file (mode 1 or mode 2) in a single pass, computing
//! the pairwise overlap matrix between all feeds. This replaces the
//! O(feeds² × per-feed-scan) approach in update-ipsets with a single
//! O(records) scan.
//!
//! For each record [from, to, scope_id]:
//! - mode 1 (bitmap): scope_id IS the bitmap → each set bit is a feed
//! - mode 2 (indirect): resolve scope_id → bitmap → each set bit is a feed
//!
//! The overlap matrix is accumulated via a callback that receives
//! (feed_a, feed_b, overlap_ip_count) for every non-zero pair.
//!
//! Heap discipline: both overlap scans run with heap that is FLAT in the
//! stored record / leaf count. Leaf iteration uses an O(tree-height) cursor
//! (never a materialized `Vec` of leaf page numbers), and overflow scope
//! bitmaps are resolved once and cached per operation (never per record).

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::page_store::PageStore;
use crate::spec;
use crate::wire::PageHeader;
use crate::writer::Writer;
use alloc::vec::Vec;

/// A pairwise overlap result: feeds A and B share `ip_count` addresses.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedOverlap {
    pub feed_a: u32,
    pub feed_b: u32,
    pub ip_count: u64,
}

/// Scan all records and compute the pairwise feed overlap matrix.
///
/// Calls `on_overlap` ONCE per `(feed_a, feed_b)` pair (feed_a < feed_b) they
/// share at least one IP address, with `ip_count` being the TOTAL number of
/// shared addresses summed across every covering record. Aggregation is
/// overflow-checked: an IPv6 span larger than `u64::MAX` addresses is reported
/// as an error rather than silently wrapping.
///
/// For mode 1 (bitmap): each record's scope_id bits are the feeds.
/// For mode 2 (indirect): each record's scope_id is resolved to a bitmap via
/// the scope table. Overflow (multi-page) scopes are resolved once and cached
/// for the duration of the scan.
///
/// **Single pass: O(records × avg_feeds_per_record²).** For a typical record
/// covered by 3 feeds, that's 3 choose 2 = 3 comparisons per record.
pub fn all_to_all_overlap<K: IpKey>(
    writer: &Writer<K>,
    on_overlap: &mut dyn FnMut(FeedOverlap),
) -> Result<()> {
    if writer.scope_mode == spec::SCOPE_MODE_SCALAR {
        return Err(Error::InvalidInput(
            "overlap requires bitmap or indirect scope mode",
        ));
    }
    if writer.pending_root == 0 {
        return Ok(());
    }
    // Aggregate the per-record contribution of each (a, b) feed pair into a
    // single total, emitted once per pair. Use a small flat list since the
    // number of distinct pairs is bounded by the feed count.
    let mut totals: Vec<(u32, u32, u64)> = Vec::new();
    let mut cache = ScopeCache::new();
    let mut overflow = false;
    scan_overlap_node(
        writer,
        writer.pending_root,
        &mut cache,
        &mut |a, b, ip_count| {
            if let Some(t) = totals.iter_mut().find(|(fa, fb, _)| *fa == a && *fb == b) {
                match t.2.checked_add(ip_count) {
                    Some(v) => t.2 = v,
                    // Two individually representable spans whose summed IP count
                    // overflows u64 must be reported, not silently truncated.
                    None => overflow = true,
                };
            } else {
                totals.push((a, b, ip_count));
            }
        },
    )?;
    if overflow {
        return Err(Error::Overflow(
            "accumulated overlap pair count exceeds u64::MAX",
        ));
    }
    totals.sort_by_key(|(a, b, _)| (*a, *b));
    for (a, b, n) in totals {
        on_overlap(FeedOverlap {
            feed_a: a,
            feed_b: b,
            ip_count: n,
        });
    }
    Ok(())
}

fn scan_overlap_node<K: IpKey>(
    writer: &Writer<K>,
    pgno: u32,
    cache: &mut ScopeCache,
    on_pair_count: &mut dyn FnMut(u32, u32, u64),
) -> Result<()> {
    let page = writer.store.as_ref().page(pgno);
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_LEAF => {
            let count = h.entry_count as usize;
            let leaf = LeafView::<K>::new(page, count);
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                let ip_count = ip_range_count(r.from(), r.to())
                    .ok_or(Error::Overflow("ip range count exceeds u64::MAX"))?;

                // Iterate feed pairs directly from the scope bitmap — no
                // per-record Vec allocation. Emits every (a, b) with a < b.
                for_each_feed_pair(writer, r.scope_id(), cache, &mut |a, b| {
                    on_pair_count(a, b, ip_count);
                });
            }
            Ok(())
        }
        spec::PAGE_TYPE_BRANCH => {
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                scan_overlap_node(writer, branch.child(j), cache, on_pair_count)?;
            }
            Ok(())
        }
        _ => Ok(()),
    }
}

/// Iterate every ordered feed pair (a, b) with a < b covered by `scope_id`,
/// calling `on_pair(a, b)` for each. Avoids materializing a feed `Vec`.
///
/// - bitmap mode: `scope_id` IS the bitmap; walk set bits with `x & (x - 1)`.
/// - indirect mode: resolve to the bitmap byte slice (zero-copy ref for inline
///   scopes, cached owned slice for overflow scopes), then scan it directly —
///   no per-record Vec allocation.
fn for_each_feed_pair<K: IpKey>(
    writer: &Writer<K>,
    scope_id: u32,
    cache: &mut ScopeCache,
    on_pair: &mut dyn FnMut(u32, u32),
) {
    match writer.scope_mode {
        spec::SCOPE_MODE_BITMAP => {
            // `outer` always holds the bits strictly greater than the current `a`;
            // after clearing `a`, the remaining bits form the inner iteration set,
            // so every emitted pair satisfies a < b.
            let mut outer = scope_id;
            while outer != 0 {
                let a = outer.trailing_zeros();
                outer &= outer - 1;
                let mut inner = outer;
                while inner != 0 {
                    let b = inner.trailing_zeros();
                    inner &= inner - 1;
                    on_pair(a, b);
                }
            }
        }
        spec::SCOPE_MODE_INDIRECT => {
            // Inline scopes resolve zero-copy. Overflow scopes span pages and
            // cannot be borrowed as one slice, so they are decoded once and
            // cached for the rest of this overlap operation — a scope's bitmap
            // is immutable within a transaction, so caching is safe.
            if let Some(b) = writer.scope_resolve_ref(scope_id) {
                for_each_set_bit_pair(b, on_pair);
            } else {
                let b = cache.overflow_bitmap(writer, scope_id);
                for_each_set_bit_pair(b, on_pair);
            }
        }
        _ => {}
    }
}

/// Iterate every set feed bit in `scope_id`, calling `on_feed(bit)` for each.
fn for_each_feed<K: IpKey>(
    writer: &Writer<K>,
    scope_id: u32,
    cache: &mut ScopeCache,
    on_feed: &mut dyn FnMut(u32),
) {
    match writer.scope_mode {
        spec::SCOPE_MODE_BITMAP => {
            let mut bits = scope_id;
            while bits != 0 {
                let bit = bits.trailing_zeros();
                bits &= bits - 1;
                on_feed(bit);
            }
        }
        spec::SCOPE_MODE_INDIRECT => {
            // Zero-copy ref for inline scopes; cached decode for overflow scopes.
            if let Some(b) = writer.scope_resolve_ref(scope_id) {
                for_each_set_bit(b, on_feed);
            } else {
                let b = cache.overflow_bitmap(writer, scope_id);
                for_each_set_bit(b, on_feed);
            }
        }
        _ => {}
    }
}

/// Resolve-once cache for overflow (multi-page) scope bitmaps within a single
/// overlap operation.
///
/// `scope_resolve_ref` returns `None` for overflow scopes (they cannot be
/// borrowed as one slice). Without a cache, every record referencing such a
/// scope would pay a fresh `Vec` decode — O(records × bitmap_size) heap that
/// grows with the record count. Since a scope's bitmap is immutable within a
/// transaction, each distinct overflow scope need only be decoded once.
///
/// Heap is bounded by the number of DISTINCT overflow scopes (typically 1–5),
/// which is independent of the record count — flat.
#[derive(Debug)]
struct ScopeCache {
    entries: Vec<(u32, Vec<u8>)>,
}

impl ScopeCache {
    fn new() -> Self {
        ScopeCache {
            entries: Vec::new(),
        }
    }

    /// Return the overflow bitmap for `scope_id`, decoding it on first access
    /// and reusing the cached copy thereafter. Only call this after
    /// `scope_resolve_ref` has already returned `None` (i.e. this is an
    /// overflow scope). Returns an empty slice for an unknown id.
    fn overflow_bitmap<K: IpKey>(&mut self, writer: &Writer<K>, scope_id: u32) -> &[u8] {
        if let Some(idx) = self.entries.iter().position(|(id, _)| *id == scope_id) {
            return self.entries[idx].1.as_slice();
        }
        let bitmap = writer.scope_resolve(scope_id).unwrap_or_default();
        self.entries.push((scope_id, bitmap));
        self.entries.last().expect("just pushed").1.as_slice()
    }
}

/// In-order leaf traversal of the pending B+tree using O(tree height) state.
///
/// Replaces the previous `pending_leaf_pages()` materialization, which pushed
/// every leaf page number into a `Vec<u32>` — O(leaf count) heap that grows
/// with the database. This cursor keeps a stack of `(branch_pgno, child_index)`
/// frames (at most `TREE_HEIGHT_MAX`), so its heap footprint is a fixed-size
/// constant regardless of how many leaves the tree holds.
///
/// Invariant: when `valid`, `record_index` is a valid slot within
/// `current_leaf_pgno` OR the cursor is positioned at the first slot of a leaf
/// that may still need to be advanced over (handled by `current_record`/`advance`).
#[derive(Clone, Copy, Debug)]
struct LeafCursor {
    /// Path from the root: each frame is the branch page and the child index
    /// currently being visited. Capped at the maximum legal tree height.
    stack: [(u32, u16); STACK_CAP],
    /// Number of valid frames in `stack`.
    depth: usize,
    current_leaf_pgno: u32,
    record_index: u16,
    valid: bool,
}

/// Maximum cursor stack depth. The tree height is bounded by `TREE_HEIGHT_MAX`
/// (32); a height-H tree has H-1 branch levels, so 32 frames always suffice.
const STACK_CAP: usize = spec::TREE_HEIGHT_MAX as usize;

impl LeafCursor {
    /// Descend from `root` to its leftmost leaf. `root` MUST be non-zero and
    /// point at a well-formed tree (caller checks `pending_root != 0`).
    fn new<K: IpKey>(root: u32, store: &dyn PageStore) -> Self {
        let mut stack = [(0u32, 0u16); STACK_CAP];
        let mut depth = 0usize;
        let mut pgno = root;
        loop {
            let page = store.page(pgno);
            let h = PageHeader::decode(page);
            if h.page_type == spec::PAGE_TYPE_LEAF {
                break;
            }
            // Branch: record this frame and descend into the leftmost child.
            if depth >= STACK_CAP {
                break;
            }
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            stack[depth] = (pgno, 0);
            depth += 1;
            pgno = branch.child(0);
        }
        LeafCursor {
            stack,
            depth,
            current_leaf_pgno: pgno,
            record_index: 0,
            valid: true,
        }
    }

    /// The record at the current cursor position, or `None` past the last leaf.
    /// Returns owned key/scope values so no store borrow escapes the call.
    #[inline]
    fn current_record<K: IpKey>(&self, store: &dyn PageStore) -> Option<(K, K, u32)> {
        if !self.valid {
            return None;
        }
        let page = store.page(self.current_leaf_pgno);
        let h = PageHeader::decode(page);
        let count = h.entry_count as usize;
        if self.record_index as usize >= count {
            return None;
        }
        let leaf = LeafView::<K>::new(page, count);
        let r = leaf.record(self.record_index as usize);
        Some((r.from(), r.to(), r.scope_id()))
    }

    /// Advance by one record, crossing leaf boundaries. O(height) amortized
    /// over a full scan: each page is pushed/popped a constant number of times.
    #[inline]
    fn advance<K: IpKey>(&mut self, store: &dyn PageStore) {
        if !self.valid {
            return;
        }
        self.record_index += 1;
        let page = store.page(self.current_leaf_pgno);
        let h = PageHeader::decode(page);
        if (self.record_index as usize) < h.entry_count as usize {
            return;
        }
        self.descend_to_next_leaf::<K>(store);
    }

    /// Move from the end of the current leaf to the first record of the next
    /// leaf in key order, popping the ancestor stack until a branch has an
    /// unvisited child. Sets `valid = false` when no more leaves remain.
    fn descend_to_next_leaf<K: IpKey>(&mut self, store: &dyn PageStore) {
        while self.depth > 0 {
            self.depth -= 1;
            let (pgno, child_idx) = self.stack[self.depth];
            let next_child = child_idx + 1;
            let page = store.page(pgno);
            let h = PageHeader::decode(page);
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            if (next_child as usize) < branch.child_count() {
                self.stack[self.depth] = (pgno, next_child);
                self.depth += 1;
                let mut pgno = branch.child(next_child as usize);
                loop {
                    let dpage = store.page(pgno);
                    let dh = PageHeader::decode(dpage);
                    if dh.page_type == spec::PAGE_TYPE_LEAF {
                        self.current_leaf_pgno = pgno;
                        self.record_index = 0;
                        return;
                    }
                    if self.depth >= STACK_CAP {
                        self.valid = false;
                        return;
                    }
                    let dbranch = BranchView::<K>::new(dpage, dh.entry_count as usize);
                    self.stack[self.depth] = (pgno, 0);
                    self.depth += 1;
                    pgno = dbranch.child(0);
                }
            }
        }
        self.valid = false;
    }
}

/// Walk set bits of a byte slice, calling `on_feed(absolute_bit)` for each.
fn for_each_set_bit(bitmap: &[u8], on_feed: &mut dyn FnMut(u32)) {
    for (byte_idx, &byte) in bitmap.iter().enumerate() {
        let mut bits = byte;
        while bits != 0 {
            let bit_in_byte = bits.trailing_zeros();
            bits &= bits - 1;
            on_feed((byte_idx as u32) * 8 + bit_in_byte);
        }
    }
}

/// Walk every ordered pair (a, b) with a < b over the set bits of `bitmap`.
/// Two-cursor scan over the byte slice — zero allocation, works for any bitmap
/// width (indirect mode supports unlimited feeds).
fn for_each_set_bit_pair(bitmap: &[u8], on_pair: &mut dyn FnMut(u32, u32)) {
    let mut a_pos: usize = 0;
    while let Some(a) = next_set_bit_from(bitmap, a_pos) {
        let mut b_pos = a as usize + 1;
        while let Some(b) = next_set_bit_from(bitmap, b_pos) {
            on_pair(a, b);
            b_pos = b as usize + 1;
        }
        a_pos = a as usize + 1;
    }
}

/// Return the absolute position of the first set bit at or after `start`,
/// or `None` if no such bit exists.
fn next_set_bit_from(bitmap: &[u8], start: usize) -> Option<u32> {
    let mut byte_idx = start >> 3;
    if byte_idx >= bitmap.len() {
        return None;
    }
    let bit_in_byte = (start & 7) as u32;
    // First byte: ignore bits below the requested offset.
    let first = bitmap[byte_idx] & (0xFFu8).wrapping_shl(bit_in_byte);
    if first != 0 {
        return Some((byte_idx as u32) * 8 + first.trailing_zeros());
    }
    byte_idx += 1;
    while byte_idx < bitmap.len() {
        let b = bitmap[byte_idx];
        if b != 0 {
            return Some((byte_idx as u32) * 8 + b.trailing_zeros());
        }
        byte_idx += 1;
    }
    None
}

/// Count the number of IP addresses in [from, to] (inclusive). Returns `None`
/// when the span exceeds `u64::MAX` (only possible for IPv6), so callers can
/// report an overflow rather than silently truncating to a wrong count.
fn ip_range_count<K: IpKey>(from: K, to: K) -> Option<u64> {
    let from_u128 = from.to_u128();
    let to_u128 = to.to_u128();
    if to_u128 < from_u128 {
        return Some(0);
    }
    let count = to_u128 - from_u128 + 1;
    if count > u64::MAX as u128 {
        None
    } else {
        Some(count as u64)
    }
}

/// Foreign-vs-all comparison: scan a foreign feed against the multi-feed file.
///
/// The foreign ranges are streamed via `next_foreign`: each call returns the
/// next `(from, to)` and `Some`, or `None` when exhausted. This lets a caller
/// feed ranges from a file/iterator without materializing a `Vec` (issue-4
/// fix). For each foreign range, every stored feed covering each overlap region
/// is reported via `on_overlap(feed, 0, ip_count)`.
///
/// **Precondition:** the foreign ranges MUST be yielded sorted ascending by
/// `from`. Both inputs (foreign feed + stored tree records) are then sorted, so
/// a single-pass linear merge replaces the per-range B+tree descent (issue-5).
/// The old implementation walked the tree once per foreign range — O(foreign ×
/// tree_height); this is O(tree_pages + foreign) + overlap output. Unsorted
/// foreign input would silently under-count overlaps.
///
/// Tree records are streamed leaf-by-leaf via an O(tree-height) [`LeafCursor`]
/// — no materialized leaf-page list — so heap is flat in the leaf count.
///
/// This is the ASN/Geo/critical-infra use case: compare one external feed
/// against all stored feeds in a single pass.
pub fn foreign_vs_all<K: IpKey>(
    writer: &Writer<K>,
    mut next_foreign: impl FnMut() -> Option<(K, K)>,
    on_overlap: &mut dyn FnMut(u32, u32, u64),
) -> Result<()> {
    if writer.scope_mode == spec::SCOPE_MODE_SCALAR {
        // Drain the stream so the caller's iterator state is fully consumed.
        while next_foreign().is_some() {}
        return Err(Error::InvalidInput(
            "overlap requires bitmap or indirect scope mode",
        ));
    }
    if writer.pending_root == 0 {
        // Drain the stream so the caller's iterator state is fully consumed
        // even when there is nothing to compare against.
        while next_foreign().is_some() {}
        return Ok(());
    }

    let mut cursor = LeafCursor::new::<K>(writer.pending_root, writer.store.as_ref());
    let mut cache = ScopeCache::new();

    // The single-pass linear merge REQUIRES the foreign ranges to be sorted
    // ascending by `from` and pairwise disjoint (from <= to, and each range
    // starts strictly after the previous one ends). Unsorted or overlapping
    // input would silently under-count; reject it instead.
    let mut prev_to: Option<K> = None;

    while let Some((f_from, f_to)) = next_foreign() {
        if f_from > f_to {
            return Err(Error::InvalidInput("foreign range has from > to"));
        }
        if let Some(pt) = prev_to {
            if f_from <= pt {
                return Err(Error::InvalidInput(
                    "foreign ranges must be sorted and disjoint",
                ));
            }
        }
        prev_to = Some(f_to);
        // Phase 1 — permanently skip records ending strictly before f_from.
        // Records that end before the current foreign range's `from` can never
        // overlap it or any later (sorted) range.
        while let Some((_, rec_to, _)) = cursor.current_record::<K>(writer.store.as_ref()) {
            if rec_to >= f_from {
                break;
            }
            cursor.advance::<K>(writer.store.as_ref());
        }
        // Phase 2 — scan forward from a COPY of the permanent cursor, emitting
        // overlaps, until a record starts after f_to. The scan cursor is a copy
        // because records overlapping this foreign range may also overlap the
        // next, so the permanent cursor is not advanced past them.
        let mut scan = cursor;
        while let Some((rec_from, rec_to, scope_id)) =
            scan.current_record::<K>(writer.store.as_ref())
        {
            if rec_from > f_to {
                break;
            }
            // Overlap is guaranteed: rec_to >= f_from (phase 1) and rec_from <= f_to.
            let overlap_from = if f_from >= rec_from { f_from } else { rec_from };
            let overlap_to = if f_to <= rec_to { f_to } else { rec_to };
            let ip_count = ip_range_count::<K>(overlap_from, overlap_to)
                .ok_or(Error::Overflow("ip range count exceeds u64::MAX"))?;
            for_each_feed(writer, scope_id, &mut cache, &mut |feed| {
                on_overlap(feed, 0, ip_count); // feed_bit=0 means "foreign feed"
            });
            scan.advance::<K>(writer.store.as_ref());
        }
    }
    Ok(())
}

/// Slice-based convenience wrapper around `foreign_vs_all`.
pub fn foreign_vs_all_slice<K: IpKey>(
    writer: &Writer<K>,
    foreign: &[(K, K)],
    on_overlap: &mut dyn FnMut(u32, u32, u64),
) -> Result<()> {
    let mut i = 0;
    foreign_vs_all(
        writer,
        || {
            if i < foreign.len() {
                let r = foreign[i];
                i += 1;
                Some(r)
            } else {
                None
            }
        },
        on_overlap,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;

    #[test]
    fn overlap_basic() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        // [10-20] feeds 0+1, [30-40] feeds 1+2
        w.set(Ipv4Key(10), Ipv4Key(20), 0b011).unwrap(); // feeds 0,1
        w.set(Ipv4Key(30), Ipv4Key(40), 0b110).unwrap(); // feeds 1,2

        let mut overlaps = Vec::new();
        all_to_all_overlap(&w, &mut |o| overlaps.push(o)).unwrap();

        // Should find: (0,1, 11 IPs) from [10-20], (1,2, 11 IPs) from [30-40]
        assert_eq!(overlaps.len(), 2);
        let r0 = overlaps
            .iter()
            .find(|o| o.feed_a == 0 && o.feed_b == 1)
            .unwrap();
        assert_eq!(r0.ip_count, 11); // 10..=20 = 11 IPs
        let r1 = overlaps
            .iter()
            .find(|o| o.feed_a == 1 && o.feed_b == 2)
            .unwrap();
        assert_eq!(r1.ip_count, 11);
    }

    #[test]
    fn overlap_triple() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        w.set(Ipv4Key(0), Ipv4Key(99), 0b111).unwrap(); // feeds 0,1,2 over 100 IPs

        let mut overlaps = Vec::new();
        all_to_all_overlap(&w, &mut |o| overlaps.push(o)).unwrap();

        // 3 choose 2 = 3 pairs
        assert_eq!(overlaps.len(), 3);
        for o in &overlaps {
            assert_eq!(o.ip_count, 100);
        }
    }

    // Issue 5: foreign_vs_all must produce identical overlap counts to the
    // per-range descent, across multiple foreign ranges that each overlap
    // multiple records, including ranges that fall in gaps (no overlap) and
    // ranges that span the whole tree. Exercises the linear-merge cursor.
    #[test]
    fn foreign_vs_all_merge_correctness() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        // feeds 0+1 over [10-20], feeds 1+2 over [30-40], feed 0 over [50-60]
        w.set(Ipv4Key(10), Ipv4Key(20), 0b011).unwrap();
        w.set(Ipv4Key(30), Ipv4Key(40), 0b110).unwrap();
        w.set(Ipv4Key(50), Ipv4Key(60), 0b001).unwrap();

        // Foreign ranges sorted by `from` (merge precondition):
        //   [5-8]   → gap before first record (no overlap)
        //   [15-35] → overlaps [10-20] (feed 0,1) and [30-40] (feed 1,2)
        //   [45-45] → gap between records (no overlap)
        //   [55-70] → overlaps [50-60] (feed 0)
        let foreign: Vec<(Ipv4Key, Ipv4Key)> = vec![
            (Ipv4Key(5), Ipv4Key(8)),
            (Ipv4Key(15), Ipv4Key(35)),
            (Ipv4Key(45), Ipv4Key(45)),
            (Ipv4Key(55), Ipv4Key(70)),
        ];

        let mut got: Vec<(u32, u64)> = Vec::new();
        foreign_vs_all_slice(&w, &foreign, &mut |feed, _fid, cnt| {
            got.push((feed, cnt));
        })
        .unwrap();

        // Expected (feed, ip_count), unsorted then sorted for compare:
        //   [15-20] → feed 0 (6), feed 1 (6)
        //   [30-35] → feed 1 (6), feed 2 (6)
        //   [55-60] → feed 0 (6)
        let mut want: Vec<(u32, u64)> = vec![(0, 6), (1, 6), (1, 6), (2, 6), (0, 6)];
        got.sort_unstable();
        want.sort_unstable();
        assert_eq!(got, want, "merge produced wrong overlaps");
    }

    // Issue 5: a foreign range fully spanning one record, plus a foreign range
    // equal to a record, plus adjacent foreign ranges sharing a record. Guards
    // the cursor-advance logic (records overlapped by one range must remain
    // visible to the next).
    #[test]
    fn foreign_vs_all_merge_adjacent_ranges() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        w.set(Ipv4Key(100), Ipv4Key(200), 0b001).unwrap(); // feed 0 over 101 IPs

        // Two adjacent foreign ranges that BOTH overlap the same record.
        let foreign: Vec<(Ipv4Key, Ipv4Key)> = vec![
            (Ipv4Key(110), Ipv4Key(120)), // 11 IPs of feed 0
            (Ipv4Key(150), Ipv4Key(160)), // 11 IPs of feed 0
        ];
        let mut total = 0u64;
        foreign_vs_all_slice(&w, &foreign, &mut |_feed, _fid, cnt| {
            total += cnt;
        })
        .unwrap();
        assert_eq!(total, 22, "adjacent ranges should each report their slice");
    }

    // Issue 5: scale — many records × many foreign ranges. Asserts the merge
    // visits every overlap exactly once (no double-count, no skip) for a
    // non-trivial interleaving. Also acts as a smoke test that the linear merge
    // handles O(N) inputs (the old per-range descent was O(N log N) here).
    #[test]
    fn foreign_vs_all_merge_scaled() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        // 1000 disjoint single-IP records, each feed 0.
        for i in 0u32..1000 {
            w.set(Ipv4Key(10_000 + i), Ipv4Key(10_000 + i), 0b001)
                .unwrap();
        }
        // Foreign ranges each covering exactly one record, plus gaps.
        let mut foreign: Vec<(Ipv4Key, Ipv4Key)> = Vec::new();
        for i in 0u32..1000 {
            foreign.push((Ipv4Key(10_000 + i), Ipv4Key(10_000 + i)));
        }
        let mut hits = 0u64;
        foreign_vs_all_slice(&w, &foreign, &mut |_f, _fid, _c| {
            hits += 1;
        })
        .unwrap();
        assert_eq!(
            hits, 1000,
            "every foreign range must hit its record exactly once"
        );
    }

    // Multi-leaf tree: assert the LeafCursor crosses leaf boundaries correctly.
    // Insert enough records to force several leaf pages, then verify a spanning
    // foreign range reports every record exactly once.
    #[test]
    fn foreign_vs_all_multi_leaf_cursor() {
        let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap();
        // IPv4 leaf holds ~340 records; insert 2000 disjoint single-IP records
        // (feed 0) → at least 5 leaf pages, exercising cursor leaf crossings.
        const N: u32 = 2000;
        for i in 0u32..N {
            w.set(Ipv4Key(10_000 + i), Ipv4Key(10_000 + i), 0b001)
                .unwrap();
        }
        // One foreign range spanning the whole key range.
        let mut yielded = false;
        let mut hits = 0u64;
        foreign_vs_all(
            &w,
            || {
                if yielded {
                    None
                } else {
                    yielded = true;
                    Some((Ipv4Key(10_000), Ipv4Key(10_000 + N - 1)))
                }
            },
            &mut |_f, _fid, _c| hits += 1,
        )
        .unwrap();
        assert_eq!(hits, N as u64, "spanning range must hit every record once");
    }
}

// Heap-scaling proof for both fixes. Lives in a separate module because it
// installs a `#[global_allocator]` (valid only once per test binary); the lib
// unit-test binary has no other global allocator, so this is unambiguous.
#[cfg(test)]
mod memory_tests {
    use super::*;
    use crate::key::Ipv4Key;
    use crate::spec;
    use std::alloc::{GlobalAlloc, Layout, System};
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    struct CountingAlloc;

    unsafe impl GlobalAlloc for CountingAlloc {
        unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
            if COUNTING.load(Ordering::Relaxed) {
                ALLOCATED_BYTES.fetch_add(layout.size(), Ordering::Relaxed);
            }
            unsafe { System.alloc(layout) }
        }

        unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
            unsafe { System.dealloc(ptr, layout) }
        }

        unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
            if COUNTING.load(Ordering::Relaxed) {
                ALLOCATED_BYTES.fetch_add(layout.size(), Ordering::Relaxed);
            }
            unsafe { System.alloc_zeroed(layout) }
        }

        unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
            if COUNTING.load(Ordering::Relaxed) {
                ALLOCATED_BYTES.fetch_add(new_size, Ordering::Relaxed);
            }
            unsafe { System.realloc(ptr, layout, new_size) }
        }
    }

    #[global_allocator]
    static ALLOCATOR: CountingAlloc = CountingAlloc;
    static COUNTING: AtomicBool = AtomicBool::new(false);
    static ALLOCATED_BYTES: AtomicUsize = AtomicUsize::new(0);

    // Build a tree of `records` single-IP records, ALL referencing the same
    // committed overflow scope (bitmap > MAX_BITMAP_WIDTH = 256 bytes). This
    // exercises BOTH fixes at once:
    //   - Fix 1: many leaves (≈records/340) → LeafCursor must not materialize them.
    //   - Fix 2: one overflow scope repeated per record → must be cached, not
    //            decoded fresh each time.
    fn overflow_scope_writer(records: u32) -> Writer<Ipv4Key> {
        let mut w = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
        // 300-byte bitmap (300 > 256) with a high bit set so canonicalization
        // keeps the full width → stored as an overflow chain.
        let mut bitmap = vec![0u8; 300];
        bitmap[0] = 0x01; // feed 0
        bitmap[299] = 0x80; // force length 300 → overflow
        let scope_id = w.scope_intern(&bitmap).unwrap();
        // Commit so the scope leaves `new_entries` and is resolved from the
        // committed scope tree — where overflow entries return `None` from
        // `scope_resolve_ref` (triggering the cached decode path).
        w.commit(1, u64::MAX).unwrap();
        for i in 0..records {
            let ip = Ipv4Key(i * 2);
            w.append(ip, ip, scope_id).unwrap();
        }
        w.commit(2, u64::MAX).unwrap();
        w
    }

    fn overflow_overlap_allocated_bytes(records: u32) -> usize {
        let writer = overflow_scope_writer(records);
        let mut yielded = false;
        ALLOCATED_BYTES.store(0, Ordering::Relaxed);
        COUNTING.store(true, Ordering::Relaxed);
        foreign_vs_all(
            &writer,
            || {
                if yielded {
                    None
                } else {
                    yielded = true;
                    Some((Ipv4Key(0), Ipv4Key((records - 1) * 2)))
                }
            },
            &mut |_, _, _| {},
        )
        .unwrap();
        COUNTING.store(false, Ordering::Relaxed);
        ALLOCATED_BYTES.load(Ordering::Relaxed)
    }

    // Heap must stay flat as the stored leaf/record count grows: the overflow
    // scope is decoded once (cache) and leaves are streamed (cursor), so the
    // 500k-record case allocates essentially the same bytes as the 1k case.
    #[test]
    fn foreign_vs_all_overflow_scope_heap_flat() {
        let small = overflow_overlap_allocated_bytes(1_000);
        let large = overflow_overlap_allocated_bytes(500_000);
        // Tolerance stays below the un-fixed costs so the assertion catches a
        // regression of EITHER fix: a materialized leaf-page Vec (~5.9 KB for
        // 500k IPv4 records) or a per-record overflow decode (~150 MB). With
        // both fixes the measured delta is 0 (one cached bitmap, O(height)
        // cursor on the stack).
        const TOLERANCE: usize = 4 << 10;
        assert!(
            large <= small + TOLERANCE,
            "overlap heap scaled with records/leaves: small={small} large={large}"
        );
    }
}
