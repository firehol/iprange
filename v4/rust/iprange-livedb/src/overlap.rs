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

use crate::error::Result;
use crate::key::IpKey;
use crate::node::{BranchView, LeafView};
use crate::spec;
use crate::wire::PageHeader;
use crate::writer::Writer;

/// A pairwise overlap result: feeds A and B share `ip_count` addresses.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedOverlap {
    pub feed_a: u32,
    pub feed_b: u32,
    pub ip_count: u64,
}

/// Scan all records and compute the pairwise feed overlap matrix.
///
/// Calls `on_overlap` for every (feed_a, feed_b) pair where feed_a < feed_b
/// and they share at least one IP address. The `ip_count` is the total number
/// of IP addresses shared by both feeds.
///
/// For mode 1 (bitmap): each record's scope_id bits are the feeds.
/// For mode 2 (indirect): each record's scope_id is resolved to a bitmap via
/// the scope table.
///
/// **Single pass: O(records × avg_feeds_per_record²).** For a typical record
/// covered by 3 feeds, that's 3 choose 2 = 3 comparisons per record.
pub fn all_to_all_overlap<K: IpKey>(
    writer: &Writer<K>,
    on_overlap: &mut dyn FnMut(FeedOverlap),
) -> Result<()> {
    if writer.pending_root == 0 {
        return Ok(());
    }
    scan_overlap_node(writer, writer.pending_root, on_overlap)
}

fn scan_overlap_node<K: IpKey>(
    writer: &Writer<K>,
    pgno: u32,
    on_overlap: &mut dyn FnMut(FeedOverlap),
) -> Result<()> {
    let page = writer.store.as_ref().page(pgno);
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_LEAF => {
            let count = h.entry_count as usize;
            let leaf = LeafView::<K>::new(page, count);
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                let ip_count = ip_range_count(r.from(), r.to());

                // Iterate feed pairs directly from the scope bitmap — no
                // per-record Vec allocation. Emits every (a, b) with a < b.
                for_each_feed_pair(writer, r.scope_id(), &mut |a, b| {
                    on_overlap(FeedOverlap {
                        feed_a: a,
                        feed_b: b,
                        ip_count,
                    });
                });
            }
            Ok(())
        }
        spec::PAGE_TYPE_BRANCH => {
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            for j in 0..branch.child_count() {
                scan_overlap_node(writer, branch.child(j), on_overlap)?;
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
/// - indirect mode: resolve to the bitmap byte slice (zero-copy ref, issue-6)
///   and scan it directly — no per-record Vec allocation.
fn for_each_feed_pair<K: IpKey>(
    writer: &Writer<K>,
    scope_id: u32,
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
            if let Some(bitmap) = writer.scope_resolve_ref(scope_id) {
                for_each_set_bit_pair(bitmap, on_pair);
            }
        }
        _ => {}
    }
}

/// Iterate every set feed bit in `scope_id`, calling `on_feed(bit)` for each.
fn for_each_feed<K: IpKey>(writer: &Writer<K>, scope_id: u32, on_feed: &mut dyn FnMut(u32)) {
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
            // Zero-copy ref (issue-6): borrows the committed page image.
            if let Some(bitmap) = writer.scope_resolve_ref(scope_id) {
                for_each_set_bit(bitmap, on_feed);
            }
        }
        _ => {}
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

/// Count the number of IP addresses in [from, to] (inclusive).
fn ip_range_count<K: IpKey>(from: K, to: K) -> u64 {
    let from_u128 = from.to_u128();
    let to_u128 = to.to_u128();
    if to_u128 >= from_u128 {
        (to_u128 - from_u128 + 1) as u64
    } else {
        0
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
/// This is the ASN/Geo/critical-infra use case: compare one external feed
/// against all stored feeds in a single pass.
pub fn foreign_vs_all<K: IpKey>(
    writer: &Writer<K>,
    mut next_foreign: impl FnMut() -> Option<(K, K)>,
    on_overlap: &mut dyn FnMut(u32, u32, u64),
) -> Result<()> {
    if writer.pending_root == 0 {
        // Drain the stream so the caller's iterator state is fully consumed
        // even when there is nothing to compare against.
        while next_foreign().is_some() {}
        return Ok(());
    }
    // Collect leaf page numbers in tree (key) order once — page numbers only,
    // no record materialization. The records inside are globally sorted.
    let leaf_pages = writer.pending_leaf_pages()?;

    // Permanent cursor over tree records (leaf idx, record idx). Only advances
    // forward across foreign ranges — records that end before the current
    // foreign range's `from` can never overlap it or any later (sorted) range.
    let mut r_li = 0usize;
    let mut r_ri = 0usize;

    while let Some((f_from, f_to)) = next_foreign() {
        // Phase 1 — permanently skip records ending strictly before f_from.
        while let Some((_, rec_to, _)) = read_leaf_rec::<K>(writer, &leaf_pages, r_li, r_ri) {
            if rec_to >= f_from {
                break;
            }
            step_leaf_rec::<K>(writer, &leaf_pages, &mut r_li, &mut r_ri);
        }
        // Phase 2 — scan forward from the permanent cursor, emitting overlaps,
        // until a record starts after f_to. The scan cursor is a COPY of the
        // permanent one: records that overlap this foreign range may also
        // overlap the next, so the permanent cursor is not advanced past them.
        let mut s_li = r_li;
        let mut s_ri = r_ri;
        while let Some((rec_from, rec_to, scope_id)) =
            read_leaf_rec::<K>(writer, &leaf_pages, s_li, s_ri)
        {
            if rec_from > f_to {
                break;
            }
            // Overlap is guaranteed: rec_to >= f_from (phase 1) and rec_from <= f_to.
            let overlap_from = if f_from >= rec_from { f_from } else { rec_from };
            let overlap_to = if f_to <= rec_to { f_to } else { rec_to };
            let ip_count = ip_range_count::<K>(overlap_from, overlap_to);
            for_each_feed(writer, scope_id, &mut |feed| {
                on_overlap(feed, 0, ip_count); // feed_bit=0 means "foreign feed"
            });
            step_leaf_rec::<K>(writer, &leaf_pages, &mut s_li, &mut s_ri);
        }
    }
    Ok(())
}

/// Read the record at leaf-pages cursor `(li, ri)`, or `None` past the end.
#[inline]
fn read_leaf_rec<K: IpKey>(
    writer: &Writer<K>,
    leaf_pages: &[u32],
    li: usize,
    ri: usize,
) -> Option<(K, K, u32)> {
    if li >= leaf_pages.len() {
        return None;
    }
    let page = writer.store.as_ref().page(leaf_pages[li]);
    let h = PageHeader::decode(page);
    let count = h.entry_count as usize;
    if ri >= count {
        return None;
    }
    let leaf = LeafView::<K>::new(page, count);
    let r = leaf.record(ri);
    Some((r.from(), r.to(), r.scope_id()))
}

/// Advance the leaf-pages cursor by one record, crossing leaf boundaries.
#[inline]
fn step_leaf_rec<K: IpKey>(writer: &Writer<K>, leaf_pages: &[u32], li: &mut usize, ri: &mut usize) {
    *ri += 1;
    while *li < leaf_pages.len() {
        let page = writer.store.as_ref().page(leaf_pages[*li]);
        let h = PageHeader::decode(page);
        let count = h.entry_count as usize;
        if *ri < count {
            return;
        }
        *li += 1;
        *ri = 0;
    }
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
}
