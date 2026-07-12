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
/// Returns a Vec of (pgno, freed_txn_id) sorted by pgno ascending.
/// Cost: O(chain_page_count) = O(free_count / 1016).
pub fn read_chain(store: &dyn PageStore, head: u32) -> Result<Vec<FreeEntry>> {
    let mut entries = Vec::new();
    let mut pgno = head;
    let mut seen_pages = 0u32;

    while pgno != 0 {
        seen_pages += 1;
        if seen_pages > 10_000_000 {
            return Err(Error::Structural("free-list chain exceeds 10M pages — corrupt"));
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
            entries.push(FreeEntry { pgno: p, freed_txn_id: freed_txn });
        }
        pgno = next;
    }
    // Sort ascending by pgno (Rule 5: prefer low-numbered pages).
    entries.sort_by_key(|e| e.pgno);
    Ok(entries)
}

/// Read the page numbers of the chain pages themselves (for freeing at commit).
pub fn read_chain_page_numbers(store: &dyn PageStore, head: u32) -> Vec<u32> {
    let mut pages = Vec::new();
    let mut pgno = head;
    let mut guard = 0u32;
    while pgno != 0 && guard < 10_000_000 {
        guard += 1;
        if pgno as u64 >= store.total_pages() as u64 { break; }
        let page = store.page(pgno);
        if PageHeader::decode(page).page_type != spec::PAGE_TYPE_TXN_FREE { break; }
        pages.push(pgno);
        pgno = wire::u32_le(page, spec::TXN_FREE_NEXT);
    }
    pages
}

/// Filter free-list entries by MVCC safety.
/// Only pages freed in txn_id <= oldest_reader_txn_id are reclaimable.
/// If oldest_reader_txn_id == u64::MAX, all entries are reclaimable.
pub fn reclaimable(entries: &[FreeEntry], oldest_reader_txn_id: u64) -> Vec<u32> {
    entries.iter()
        .filter(|e| oldest_reader_txn_id == u64::MAX || e.freed_txn_id <= oldest_reader_txn_id)
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
                count += (group_size + spec::TXN_FREE_CAPACITY - 1) / spec::TXN_FREE_CAPACITY;
            }
            last_txn = e.freed_txn_id;
            group_size = 0;
        }
        group_size += 1;
    }
    if group_size > 0 {
        count += (group_size + spec::TXN_FREE_CAPACITY - 1) / spec::TXN_FREE_CAPACITY;
    }
    count
}

/// Build the new free-list chain using pre-allocated chain pages.
///
/// entries: ALL entries to persist (unconsumed old + new freed + old chain pages).
/// chain_pages: pre-allocated page numbers (from Writer.alloc_page — reuses freed pages).
/// Entries are grouped by freed_txn_id and sorted by pgno within each group.
/// Returns the head page number of the chain (0 if entries is empty).
pub fn write_chain(
    store: &mut dyn PageStore,
    entries: &[FreeEntry],
    chain_pages: &[u32],
) -> Result<u32> {
    if entries.is_empty() {
        return Ok(0);
    }

    // Group by freed_txn_id, sort each group by pgno ascending (Rule 5).
    let mut grouped: Vec<(u64, Vec<u32>)> = Vec::new();
    for e in entries {
        if let Some(last) = grouped.last_mut() {
            if last.0 == e.freed_txn_id {
                last.1.push(e.pgno);
                continue;
            }
        }
        grouped.push((e.freed_txn_id, vec![e.pgno]));
    }
    for (_, pgnos) in grouped.iter_mut() {
        pgnos.sort();
    }

    // Write chain pages using the pre-allocated pages.
    let mut page_iter = chain_pages.iter();
    let mut head: u32 = 0;
    for (ftxn, pgnos) in &grouped {
        for chunk in pgnos.chunks(spec::TXN_FREE_CAPACITY) {
            let pgno = *page_iter.next().ok_or(Error::Structural(
                "not enough pre-allocated chain pages for free-list",
            ))?;
            write_free_page(store, pgno, head, *ftxn, chunk)?;
            head = pgno;
        }
    }
    Ok(head)
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
        if pgno == 2 { break; }
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
            FreeEntry { pgno: 5, freed_txn_id: 1 },
            FreeEntry { pgno: 3, freed_txn_id: 1 },
            FreeEntry { pgno: 8, freed_txn_id: 2 },
        ];
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        assert!(head != 0);

        let read = read_chain(&store as &dyn PageStore, head).unwrap();
        assert_eq!(read.len(), 3);
        // Sorted by pgno
        assert_eq!(read[0].pgno, 3);
        assert_eq!(read[1].pgno, 5);
        assert_eq!(read[2].pgno, 8);
    }

    #[test]
    fn reclaimable_filter() {
        let entries = vec![
            FreeEntry { pgno: 1, freed_txn_id: 5 },
            FreeEntry { pgno: 2, freed_txn_id: 7 },
            FreeEntry { pgno: 3, freed_txn_id: 3 },
        ];
        // Reader at txn 5: reclaim entries with freed_txn <= 5.
        let free = reclaimable(&entries, 5);
        assert_eq!(free, vec![1, 3]); // pgno 1 (txn 5) and pgno 3 (txn 3)
        // No readers: reclaim all.
        let all = reclaimable(&entries, u64::MAX);
        assert_eq!(all.len(), 3);
    }

    #[test]
    fn chain_page_numbers() {
        let mut store = make_store(100);
        let entries = vec![
            FreeEntry { pgno: 5, freed_txn_id: 1 },
            FreeEntry { pgno: 10, freed_txn_id: 1 },
            FreeEntry { pgno: 20, freed_txn_id: 2 },
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
            .map(|i| FreeEntry { pgno: i + 10, freed_txn_id: 1 })
            .collect();
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        let read = read_chain(&store as &dyn PageStore, head).unwrap();
        assert_eq!(read.len(), 3000);
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
            FreeEntry { pgno: 5, freed_txn_id: 3 },
            FreeEntry { pgno: 10, freed_txn_id: 7 },
        ];
        let chain_pages = alloc_chain_pages(&mut store as &mut dyn PageStore, &entries);
        let head = write_chain(&mut store as &mut dyn PageStore, &entries, &chain_pages).unwrap();
        let read = read_chain(&store as &dyn PageStore, head).unwrap();
        assert_eq!(read[0], FreeEntry { pgno: 5, freed_txn_id: 3 });
        assert_eq!(read[1], FreeEntry { pgno: 10, freed_txn_id: 7 });
    }
}
