//! Persistent free-list with transaction-ID tagging (D2, Rules 5-8).
//!
//! The free-list is a chain of pages (PAGE_TYPE_FREE_LIST) stored in the file.
//! Each chain page stores a batch of freed page numbers tagged with the
//! transaction ID that freed them. The writer loads the chain at open time,
//! filters by oldest reader txn_id for MVCC safety, and sorts ascending for
//! optimal file density (Rule 5). At commit, new freed pages are appended
//! to the chain. No full tree scans (Rule 7).

use crate::error::{Error, Result};
use crate::page_store::PageStore;
use crate::spec;
use crate::wire::{self, PageHeader};
use alloc::vec::Vec;

/// One free-list entry: a page freed in a specific transaction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FreeEntry {
    pub pgno: u32,
    pub freed_txn_id: u64,
}

/// Read the entire free-list chain from the file.
///
/// Returns entries in **chain order** (the segment at `head` first, then its
/// `next`, and so on). Because new segments are always prepended at the head
/// (see [`append_segment`]), chain order is newest-first. This preserves the
/// chronology that [`crate::writer::Writer::load_free_list`] needs to apply
/// newest-entry-wins semantics. Callers that want a pgno-sorted view must sort
/// the result themselves.
///
/// Cost: O(chain_page_count) = O(free_count / 1016).
pub fn read_chain(store: &dyn PageStore, head: u32) -> Result<Vec<FreeEntry>> {
    let mut entries = Vec::new();
    let mut pgno = head;
    let mut seen_pages = 0u32;

    while pgno != 0 {
        seen_pages += 1;
        if seen_pages > 10_000_000 {
            return Err(Error::Structural(
                "free-list chain exceeds 10M pages — corrupt",
            ));
        }
        if pgno as u64 >= store.total_pages() as u64 {
            break; // chain page beyond file — corrupt or truncated
        }
        let page = store.page(pgno);
        let h = PageHeader::decode(page);
        if h.page_type != spec::PAGE_TYPE_TXN_FREE {
            break; // not a free-list page — stop
        }
        let next = wire::u32_le(page, spec::TXN_FREE_NEXT);
        let freed_txn = wire::u64_le(page, spec::TXN_FREE_FREED_IN);
        let count = wire::u32_le(page, spec::TXN_FREE_COUNT) as usize;
        let max = count.min(spec::TXN_FREE_CAPACITY);
        for i in 0..max {
            let p = wire::u32_le(page, spec::TXN_FREE_ARRAY + i * 4);
            entries.push(FreeEntry {
                pgno: p,
                freed_txn_id: freed_txn,
            });
        }
        pgno = next;
    }
    // NOTE: intentionally NOT sorted — chain order (newest-first) is preserved
    // so that newest-entry-wins dedup in load_free_list is correct.
    Ok(entries)
}

/// Walk the free-list chain and verify the per-page CRC32C of every TXN_FREE
/// chain page. Use at open time (when chain pages are intact) to reject a
/// corrupt chain page. Returns Ok(()) if intact or head == 0.
pub fn validate_chain_crc(store: &dyn PageStore, head: u32) -> Result<()> {
    if head == 0 {
        return Ok(());
    }
    let total_pages = store.total_pages();
    let mut pgno = head;
    let mut seen = 0u32;
    while pgno != 0 {
        seen += 1;
        if seen > 10_000_000 {
            return Err(Error::Structural(
                "free-list chain exceeds 10M pages — corrupt",
            ));
        }
        // Cycle defense: a valid chain visits only distinct pages, so it can
        // never be longer than total_pages. A self-referential or cyclic chain
        // would otherwise loop forever re-reading pages; reject it here before
        // re-reading any page (keeps the traversal within file bounds).
        if seen > total_pages {
            return Err(Error::Structural("free-list chain cycle detected"));
        }
        if pgno as u64 >= total_pages as u64 {
            return Ok(()); // chain page beyond file — stop
        }
        let page = store.page(pgno);
        if PageHeader::decode(page).page_type != spec::PAGE_TYPE_TXN_FREE {
            return Ok(()); // not a chain page — stop
        }
        if !crate::crc32c::verify_page(page) {
            return Err(Error::ChecksumFailed("free-list chain page fails CRC"));
        }
        pgno = wire::u32_le(page, spec::TXN_FREE_NEXT);
    }
    Ok(())
}

/// Validate the CONTENTS of the free-list chain at open time.
///
/// Two complementary checks:
///   1. Self-reference (raw, per chain page): a chain page's freed-pgno array
///      must never contain its OWN page number — a chain page is allocated, not
///      freed, in the transaction that writes it. This catches a corrupt chain
///      that lists its own (or another chain page's) number, e.g. the head page
///      pointing at itself.
///   2. Live-metadata (newest-entry-wins): a page whose authoritative state is
///      "free" (its newest chain entry is a real freed_txn_id, not a tombstone
///      and not a truncated page) must never be a meta page (0/1), the active
///      data root, or the scope-table root. Stale entries for a page that was
///      later reused (tombstoned) or truncated are harmless and skipped.
pub fn validate_free_entries(
    store: &dyn PageStore,
    head: u32,
    data_root: u32,
    scope_root: u32,
) -> Result<()> {
    if head == 0 {
        return Ok(());
    }
    let total = store.total_pages();

    // Pass 1: raw self-reference check + collect entries for newest-wins.
    let mut entries: Vec<FreeEntry> = Vec::new();
    let mut pgno = head;
    let mut seen = 0u32;
    while pgno != 0 {
        seen += 1;
        if seen > total {
            return Err(Error::Structural("free-list chain cycle detected"));
        }
        if pgno as u64 >= total as u64 {
            break; // chain page beyond file — stop
        }
        let page = store.page(pgno);
        if PageHeader::decode(page).page_type != spec::PAGE_TYPE_TXN_FREE {
            break; // not a chain page — stop
        }
        if !crate::crc32c::verify_page(page) {
            return Err(Error::ChecksumFailed("free-list chain page fails CRC"));
        }
        let freed_txn = wire::u64_le(page, spec::TXN_FREE_FREED_IN);
        let count =
            (wire::u32_le(page, spec::TXN_FREE_COUNT) as usize).min(spec::TXN_FREE_CAPACITY);
        for i in 0..count {
            let p = wire::u32_le(page, spec::TXN_FREE_ARRAY + i * 4);
            if p == pgno {
                return Err(Error::Structural(
                    "free-list entry self-references chain page",
                ));
            }
            entries.push(FreeEntry {
                pgno: p,
                freed_txn_id: freed_txn,
            });
        }
        pgno = wire::u32_le(page, spec::TXN_FREE_NEXT);
    }

    // Pass 2: newest-entry-wins (entries are newest-first), then reject any
    // live metadata page whose authoritative state is "free".
    let mut latest: rustc_hash::FxHashMap<u32, u64> = rustc_hash::FxHashMap::default();
    for e in &entries {
        latest.entry(e.pgno).or_insert(e.freed_txn_id);
    }
    for (p, ftxn) in &latest {
        if *ftxn == u64::MAX {
            continue; // tombstoned (reused) — not actually free
        }
        if *p as u64 >= total as u64 {
            continue; // stale entry for a truncated page; load_free_list filters it
        }
        if *p < 2 {
            return Err(Error::Structural("free-list entry points to meta page"));
        }
        if *p == data_root || *p == scope_root {
            return Err(Error::Structural("free-list entry points to live root"));
        }
    }
    Ok(())
}

/// Read the page numbers of the chain pages themselves (for freeing at commit).
pub fn read_chain_page_numbers(store: &dyn PageStore, head: u32) -> Vec<u32> {
    let mut pages = Vec::new();
    let mut pgno = head;
    let mut guard = 0u32;
    while pgno != 0 && guard < 10_000_000 {
        guard += 1;
        if pgno as u64 >= store.total_pages() as u64 {
            break;
        }
        let page = store.page(pgno);
        if PageHeader::decode(page).page_type != spec::PAGE_TYPE_TXN_FREE {
            break;
        }
        pages.push(pgno);
        pgno = wire::u32_le(page, spec::TXN_FREE_NEXT);
    }
    pages
}

/// Filter free-list entries by MVCC safety.
/// Only pages freed in txn_id < oldest_reader_txn_id are reclaimable.
/// A page tagged with txn T was last live in txn T — a reader pinned at
/// txn T still needs it. So reclamation requires strict < .
/// If oldest_reader_txn_id == u64::MAX, all entries are reclaimable.
pub fn reclaimable(entries: &[FreeEntry], oldest_reader_txn_id: u64) -> Vec<u32> {
    entries
        .iter()
        .filter(|e| oldest_reader_txn_id == u64::MAX || e.freed_txn_id < oldest_reader_txn_id)
        .map(|e| e.pgno)
        .collect()
}

/// Count how many chain pages are needed for the given entries.
/// Entries should be sorted by freed_txn_id for optimal packing.
/// Cost: O(entries).
pub fn chain_page_count(entries: &[FreeEntry]) -> usize {
    if entries.is_empty() {
        return 0;
    }
    let mut count = 0usize;
    let mut last_txn = u64::MAX;
    let mut group_size = 0usize;
    for e in entries {
        if e.freed_txn_id != last_txn {
            if group_size > 0 {
                count += group_size.div_ceil(spec::TXN_FREE_CAPACITY);
            }
            last_txn = e.freed_txn_id;
            group_size = 0;
        }
        group_size += 1;
    }
    if group_size > 0 {
        count += group_size.div_ceil(spec::TXN_FREE_CAPACITY);
    }
    count
}

/// Append a new segment of entries to the front of the free-list chain.
///
/// This is the tombstone-based append-only writer. `entries` MUST be sorted by
/// `freed_txn_id` (consecutive equal values form one group); within each group
/// the pgnos are sorted ascending (Rule 5). Each group is packed into
/// `TXN_FREE_CAPACITY`-sized pages. Pages are linked so that the **last** page
/// written becomes the new head, and the first page of the new segment points
/// its `next` at `old_head`. Thus the new segment is the newest in the chain.
///
/// With `old_head == 0` this builds a standalone chain (equivalent to the legacy
/// full rewrite). Returns the new head page number (== `old_head` if empty).
pub fn append_segment(
    store: &mut dyn PageStore,
    entries: &[FreeEntry],
    chain_pages: &[u32],
    old_head: u32,
) -> Result<u32> {
    if entries.is_empty() {
        return Ok(old_head);
    }

    // Group consecutive same-freed_txn_id entries (caller pre-sorts by txn).
    // Sort each group's pgnos ascending (Rule 5: prefer low-numbered pages).
    let mut page_iter = chain_pages.iter();
    let mut head: u32 = old_head;
    let mut i = 0;
    while i < entries.len() {
        let ftxn = entries[i].freed_txn_id;
        let mut group: Vec<u32> = Vec::new();
        while i < entries.len() && entries[i].freed_txn_id == ftxn {
            group.push(entries[i].pgno);
            i += 1;
        }
        group.sort();
        for chunk in group.chunks(spec::TXN_FREE_CAPACITY) {
            let pgno = *page_iter.next().ok_or(Error::Structural(
                "not enough pre-allocated chain pages for free-list",
            ))?;
            write_free_page(store, pgno, head, ftxn, chunk)?;
            head = pgno;
        }
    }
    Ok(head)
}

/// Build a standalone free-list chain using pre-allocated chain pages.
///
/// This is [`append_segment`] with `old_head == 0` (a fresh chain, not appended
/// to an existing one). Kept for standalone/test use; the commit path uses
/// [`append_segment`] to prepend to the existing chain.
pub fn write_chain(
    store: &mut dyn PageStore,
    entries: &[FreeEntry],
    chain_pages: &[u32],
) -> Result<u32> {
    append_segment(store, entries, chain_pages, 0)
}

fn write_free_page(
    store: &mut dyn PageStore,
    pgno: u32,
    next: u32,
    freed_txn_id: u64,
    pages: &[u32],
) -> Result<()> {
    let page = store.page_mut(pgno);
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_TXN_FREE, pages.len() as u16, pgno);
    wire::put_u32(page, spec::TXN_FREE_NEXT, next);
    wire::put_u64(page, spec::TXN_FREE_FREED_IN, freed_txn_id);
    wire::put_u32(page, spec::TXN_FREE_COUNT, pages.len() as u32);
    for (i, &p) in pages.iter().enumerate() {
        wire::put_u32(page, spec::TXN_FREE_ARRAY + i * 4, p);
    }
    Ok(())
}

/// Detect trailing free pages that can be truncated.
/// Returns the number of pages that can be removed from the end of the file.
pub fn trailing_free_count(free_pages: &[u32], total_pages: u32) -> u32 {
    if free_pages.is_empty() {
        return 0;
    }
    let free_set: alloc::collections::BTreeSet<u32> = free_pages.iter().copied().collect();
    let mut shrink = 0u32;
    // Walk backwards from the end of the file.
    let mut pgno = total_pages - 1;
    while pgno >= 2 && free_set.contains(&pgno) {
        shrink += 1;
        if pgno == 2 {
            break;
        }
        pgno -= 1;
    }
    shrink
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::page_store::VecPageStore;
    use crate::spec::PAGE_SIZE;

    fn make_store(pages: usize) -> VecPageStore {
        VecPageStore::new(alloc::vec![0u8; pages * PAGE_SIZE])
    }

    /// Pre-allocate chain pages from the store's growth region (test helper).
    fn alloc_chain_pages(store: &mut dyn PageStore, entries: &[FreeEntry]) -> Vec<u32> {
        let n = chain_page_count(entries);
        (0..n).map(|_| store.alloc_page().unwrap()).collect()
    }

    #[test]
    fn write_and_read_chain() {
        let mut store = make_store(100);
        let entries = vec![
            FreeEntry {
                pgno: 5,
                freed_txn_id: 1,
            },
            FreeEntry {
                pgno: 3,
                freed_txn_id: 1,
            },
            FreeEntry {
                pgno: 8,
                freed_txn_id: 2,
            },
        ];
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        assert!(head != 0);

        let mut read = read_chain(&store as &dyn PageStore, head).unwrap();
        assert_eq!(read.len(), 3);
        // read_chain returns chain order; sort for comparison.
        read.sort_by_key(|e| e.pgno);
        assert_eq!(read[0].pgno, 3);
        assert_eq!(read[1].pgno, 5);
        assert_eq!(read[2].pgno, 8);
    }

    #[test]
    fn reclaimable_filter() {
        let entries = vec![
            FreeEntry {
                pgno: 1,
                freed_txn_id: 5,
            },
            FreeEntry {
                pgno: 2,
                freed_txn_id: 7,
            },
            FreeEntry {
                pgno: 3,
                freed_txn_id: 3,
            },
        ];
        // Reader at txn 5: a page tagged with txn 5 was last live in txn 5 —
        // the reader still needs it. Only txn < 5 is reclaimable.
        let free = reclaimable(&entries, 5);
        assert_eq!(free, vec![3]); // only pgno 3 (txn 3 < 5)
                                   // Reader at txn 6: pages tagged 5 and 3 are reclaimable (both < 6).
        let free6 = reclaimable(&entries, 6);
        assert_eq!(free6, vec![1, 3]);
        // No readers: reclaim all.
        let all = reclaimable(&entries, u64::MAX);
        assert_eq!(all.len(), 3);
    }

    #[test]
    fn chain_page_numbers() {
        let mut store = make_store(100);
        let entries = vec![
            FreeEntry {
                pgno: 5,
                freed_txn_id: 1,
            },
            FreeEntry {
                pgno: 10,
                freed_txn_id: 1,
            },
            FreeEntry {
                pgno: 20,
                freed_txn_id: 2,
            },
        ];
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        let chain_pages = read_chain_page_numbers(&store as &dyn PageStore, head);
        assert!(!chain_pages.is_empty());
        assert!(chain_pages.iter().all(|&p| p >= 90)); // chain pages at end
    }

    #[test]
    fn large_chain() {
        let mut store = make_store(2000);
        let entries: Vec<FreeEntry> = (0..3000u32)
            .map(|i| FreeEntry {
                pgno: i + 10,
                freed_txn_id: 1,
            })
            .collect();
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        let mut read = read_chain(&store as &dyn PageStore, head).unwrap();
        assert_eq!(read.len(), 3000);
        read.sort_by_key(|e| e.pgno);
        assert_eq!(read[0].pgno, 10);
        assert_eq!(read[2999].pgno, 3009);
    }

    #[test]
    fn trailing_free_detection() {
        // total_pages=20. Pages 17,18,19 are free.
        let free = vec![3, 7, 17, 18, 19];
        assert_eq!(trailing_free_count(&free, 20), 3);
    }

    #[test]
    fn trailing_free_none() {
        let free = vec![3, 7, 10];
        assert_eq!(trailing_free_count(&free, 20), 0); // page 19 not free
    }

    #[test]
    fn empty_chain() {
        let store = make_store(10);
        let read = read_chain(&store, 0).unwrap();
        assert!(read.is_empty());
    }

    #[test]
    fn round_trip_preserves_txn_ids() {
        let mut store = make_store(100);
        let entries = vec![
            FreeEntry {
                pgno: 5,
                freed_txn_id: 3,
            },
            FreeEntry {
                pgno: 10,
                freed_txn_id: 7,
            },
        ];
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        let mut read = read_chain(&store as &dyn PageStore, head).unwrap();
        read.sort_by_key(|e| e.pgno);
        assert_eq!(
            read[0],
            FreeEntry {
                pgno: 5,
                freed_txn_id: 3
            }
        );
        assert_eq!(
            read[1],
            FreeEntry {
                pgno: 10,
                freed_txn_id: 7
            }
        );
    }

    /// append_segment prepends a new segment to an existing chain: the new
    /// segment becomes the head, the old chain stays reachable via `next`.
    #[test]
    fn append_segment_prepends() {
        let mut store = make_store(200);
        // Build an initial chain with one entry.
        let old = vec![FreeEntry {
            pgno: 5,
            freed_txn_id: 1,
        }];
        let old_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &old);
        let old_head = write_chain(&mut store as &mut dyn PageStore, &old, &old_pages).unwrap();

        // Append a new segment with a different txn id.
        let new = vec![FreeEntry {
            pgno: 9,
            freed_txn_id: 2,
        }];
        let new_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &new);
        let new_head =
            append_segment(&mut store as &mut dyn PageStore, &new, &new_pages, old_head).unwrap();

        // New head is the freshly written page, distinct from the old head.
        assert!(new_head != 0);
        assert!(new_head != old_head);
        // Reading the whole chain yields BOTH entries (old + new).
        let read = read_chain(&store as &dyn PageStore, new_head).unwrap();
        assert_eq!(
            read.len(),
            2,
            "appended segment must keep old entries reachable"
        );
    }

    /// Tombstone invariant: a page that is freed then tombstoned must resolve
    /// as NOT free under newest-entry-wins. This mirrors the dedup logic in
    /// Writer::load_free_list at the chain level.
    #[test]
    fn tombstone_newest_wins() {
        let mut store = make_store(200);
        // Segment 1: page 7 freed in txn 1.
        let s1 = vec![FreeEntry {
            pgno: 7,
            freed_txn_id: 1,
        }];
        let s1_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &s1);
        let head1 = write_chain(&mut store as &mut dyn PageStore, &s1, &s1_pages).unwrap();

        // Segment 2 (newer): tombstone page 7 (freed_txn_id = MAX), and free page 8.
        let s2 = vec![
            FreeEntry {
                pgno: 8,
                freed_txn_id: 1,
            },
            FreeEntry {
                pgno: 7,
                freed_txn_id: u64::MAX,
            }, // tombstone
        ];
        // Sort by freed_txn_id so the tombstone group (MAX) is written last → newest.
        let mut s2_sorted = s2.clone();
        s2_sorted.sort_by_key(|e| e.freed_txn_id);
        let s2_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &s2_sorted);
        let head2 = append_segment(
            &mut store as &mut dyn PageStore,
            &s2_sorted,
            &s2_pages,
            head1,
        )
        .unwrap();

        // Newest-wins dedup (first occurrence in newest-first chain order).
        let entries = read_chain(&store as &dyn PageStore, head2).unwrap();
        let mut latest: alloc::collections::BTreeMap<u32, u64> =
            alloc::collections::BTreeMap::new();
        for e in &entries {
            latest.entry(e.pgno).or_insert(e.freed_txn_id);
        }
        // Page 7: newest entry is the tombstone (MAX) → NOT free.
        assert_eq!(latest[&7], u64::MAX, "tombstone must win for page 7");
        // Page 8: newest (only) entry is a normal free → free.
        assert_eq!(latest[&8], 1, "page 8 must be free");
    }
}
