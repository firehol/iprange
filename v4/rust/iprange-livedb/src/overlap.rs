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
use crate::writer::Writer;
use crate::node::{BranchView, LeafView};
use crate::spec;
use crate::wire::PageHeader;

/// A pairwise overlap result: feeds A and B share `ip_count` addresses.
#[derive(Clone, Copy, Debug)]
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

                // Get the feeds covering this record.
                let feeds = get_feeds_for_scope(writer, r.scope_id());

                // For every pair of feeds, accumulate the overlap.
                for a in 0..feeds.len() {
                    for b in (a + 1)..feeds.len() {
                        on_overlap(FeedOverlap {
                            feed_a: feeds[a],
                            feed_b: feeds[b],
                            ip_count,
                        });
                    }
                }
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

/// Resolve a scope_id to the list of feed bits it represents.
fn get_feeds_for_scope<K: IpKey>(writer: &Writer<K>, scope_id: u32) -> Vec<u32> {
    match writer.scope_mode {
        spec::SCOPE_MODE_BITMAP => {
            // scope_id IS the bitmap. Extract set bits.
            let mut feeds = Vec::new();
            let mut bits = scope_id;
            let mut bit = 0u32;
            while bits != 0 {
                if bits & 1 != 0 {
                    feeds.push(bit);
                }
                bits >>= 1;
                bit += 1;
            }
            feeds
        }
        spec::SCOPE_MODE_INDIRECT => {
            // Resolve via the scope registry (writer-side).
            if let Some(bitmap) = writer.scope_resolve(scope_id) {
                let mut feeds = Vec::new();
                for (byte_idx, &byte) in bitmap.iter().enumerate() {
                    let mut bits = byte;
                    let mut bit_in_byte = 0u32;
                    while bits != 0 {
                        if bits & 1 != 0 {
                            feeds.push((byte_idx as u32) * 8 + bit_in_byte);
                        }
                        bits >>= 1;
                        bit_in_byte += 1;
                    }
                }
                feeds
            } else {
                Vec::new()
            }
        }
        _ => Vec::new(),
    }
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
/// For each IP range in the foreign feed, find all records in the multi-feed
/// file that overlap, and report which feeds cover the overlap region.
///
/// This is the ASN/Geo/critical-infra use case: compare one external feed
/// against all stored feeds in a single pass.
pub fn foreign_vs_all<K: IpKey, F: FnMut(u32, u32, u64)>(
    writer: &Writer<K>,
    foreign: &[(K, K)], // sorted, disjoint ranges from the foreign feed
    on_overlap: &mut F,
) -> Result<()> {
    // For each foreign range, find overlapping records via tree descent.
    for &(from, to) in foreign {
        let mut overlaps: Vec<(K, K, u32)> = Vec::new();
        collect_overlapping_writer(writer, writer.pending_root, from, to, &mut overlaps)?;

        for (rec_from, rec_to, scope_id) in &overlaps {
            let overlap_from = if from >= *rec_from { from } else { *rec_from };
            let overlap_to = if to <= *rec_to { to } else { *rec_to };
            let ip_count = ip_range_count(overlap_from, overlap_to);

            let feeds = get_feeds_for_scope(writer, *scope_id);
            for feed in feeds {
                on_overlap(feed, 0, ip_count); // feed_bit=0 means "foreign feed"
            }
        }
    }
    Ok(())
}

fn collect_overlapping_writer<K: IpKey>(
    writer: &Writer<K>,
    pgno: u32,
    from: K,
    to: K,
    out: &mut Vec<(K, K, u32)>,
) -> Result<()> {
    let page = writer.store.as_ref().page(pgno);
    let h = PageHeader::decode(page);
    match h.page_type {
        spec::PAGE_TYPE_LEAF => {
            let leaf = LeafView::<K>::new(page, h.entry_count as usize);
            for i in 0..leaf.len() {
                let r = leaf.record(i);
                if r.from() > to { break; }
                if r.to() >= from {
                    out.push((r.from(), r.to(), r.scope_id()));
                }
            }
            Ok(())
        }
        spec::PAGE_TYPE_BRANCH => {
            let branch = BranchView::<K>::new(page, h.entry_count as usize);
            let start = branch_find_child(&branch, from);
            for j in start..branch.child_count() {
                if j > 0 && branch.sep(j - 1) > to { break; }
                collect_overlapping_writer(writer, branch.child(j), from, to, out)?;
            }
            Ok(())
        }
        _ => Ok(()),
    }
}

fn branch_find_child<K: IpKey>(branch: &BranchView<'_, K>, key: K) -> usize {
    let (mut lo, mut hi) = (0usize, branch.sep_count());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if branch.sep(mid) <= key {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    lo
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
        let r0 = overlaps.iter().find(|o| o.feed_a == 0 && o.feed_b == 1).unwrap();
        assert_eq!(r0.ip_count, 11); // 10..=20 = 11 IPs
        let r1 = overlaps.iter().find(|o| o.feed_a == 1 && o.feed_b == 2).unwrap();
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
}
