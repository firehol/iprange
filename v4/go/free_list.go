// Persistent free-list with transaction-ID tagging (D2, Rules 5-8).
//
// The free-list is a chain of PAGE_TYPE_TXN_FREE pages stored in the file.
// Each chain page stores a batch of freed page numbers tagged with the
// transaction ID that freed them. The writer loads the chain at open time,
// filters by oldest reader txn_id for MVCC safety, and sorts ascending for
// optimal file density (Rule 5). At commit, new freed pages are appended
// to the chain. No full tree scans (Rule 7).
package iprangedb

import (
	"fmt"
	"sort"
)

// PageReader is the read-only view of a page store needed by the free-list.
type PageReader interface {
	page(pgno uint32) []byte
	totalPages() uint32
}

// PageWriter is the mutable view of a page store needed to write chain pages.
type PageWriter interface {
	page(pgno uint32) []byte
	pageMut(pgno uint32) []byte
	totalPages() uint32
}

// FreeEntry is one free-list entry: a page freed in a specific transaction.
type FreeEntry struct {
	Pgno       uint32
	FreedTxnID uint64
}

// ReadChain reads the entire free-list chain starting at head.
// Returns entries sorted by pgno ascending.
// Cost: O(chain_page_count) = O(free_count / TxnFreeCapacity).
func ReadChain(store PageReader, head uint32) ([]FreeEntry, error) {
	var entries []FreeEntry
	pgno := head
	seenPages := uint32(0)
	for pgno != 0 {
		seenPages++
		if seenPages > 10_000_000 {
			return nil, fmt.Errorf("free-list chain exceeds 10M pages — corrupt")
		}
		if uint64(pgno) >= uint64(store.totalPages()) {
			break // chain page beyond file — corrupt or truncated
		}
		page := store.page(pgno)
		h := decodeHeader(page)
		if h.pageType != PageTypeTxnFree {
			break // not a free-list page — stop
		}
		next := u32le(page, TxnFreeNext)
		freedTxn := u64le(page, TxnFreeFreedIn)
		count := int(u32le(page, TxnFreeCount))
		maxN := count
		if maxN > TxnFreeCapacity {
			maxN = TxnFreeCapacity
		}
		for i := 0; i < maxN; i++ {
			p := u32le(page, TxnFreeArray+i*4)
			entries = append(entries, FreeEntry{Pgno: p, FreedTxnID: freedTxn})
		}
		pgno = next
	}
	// Sort ascending by pgno (Rule 5: prefer low-numbered pages).
	sort.Slice(entries, func(i, j int) bool { return entries[i].Pgno < entries[j].Pgno })
	return entries, nil
}

// ReadChainPageNumbers returns the page numbers of the chain pages themselves
// (for freeing at commit).
func ReadChainPageNumbers(store PageReader, head uint32) []uint32 {
	var pages []uint32
	pgno := head
	guard := uint32(0)
	for pgno != 0 && guard < 10_000_000 {
		guard++
		if uint64(pgno) >= uint64(store.totalPages()) {
			break
		}
		page := store.page(pgno)
		if decodeHeader(page).pageType != PageTypeTxnFree {
			break
		}
		pages = append(pages, pgno)
		pgno = u32le(page, TxnFreeNext)
	}
	return pages
}

// Reclaimable filters free-list entries by MVCC safety.
// Only pages freed in txn_id < oldestReaderTxnID are reclaimable.
// A page tagged with txn T was last live in txn T — a reader pinned at txn T
// still needs it. So reclamation requires strict <.
// If oldestReaderTxnID == math.MaxUint64, all entries are reclaimable.
func Reclaimable(entries []FreeEntry, oldestReaderTxnID uint64) []uint32 {
	var out []uint32
	for _, e := range entries {
		if oldestReaderTxnID == ^uint64(0) || e.FreedTxnID < oldestReaderTxnID {
			out = append(out, e.Pgno)
		}
	}
	return out
}

// ChainPageCount counts how many chain pages are needed for the given entries.
// Entries must be sorted by freed_txn_id for optimal packing.
// Cost: O(entries).
func ChainPageCount(entries []FreeEntry) int {
	if len(entries) == 0 {
		return 0
	}
	count := 0
	lastTxn := ^uint64(0)
	groupSize := 0
	for _, e := range entries {
		if e.FreedTxnID != lastTxn {
			if groupSize > 0 {
				count += (groupSize + TxnFreeCapacity - 1) / TxnFreeCapacity
			}
			lastTxn = e.FreedTxnID
			groupSize = 0
		}
		groupSize++
	}
	if groupSize > 0 {
		count += (groupSize + TxnFreeCapacity - 1) / TxnFreeCapacity
	}
	return count
}

// WriteChain builds the new free-list chain using pre-allocated chain pages.
//
// entries: ALL entries to persist (unconsumed old + new freed + old chain pages).
// chainPages: pre-allocated page numbers (reuses freed pages).
// Entries are grouped by freed_txn_id and sorted by pgno within each group.
// Returns the head page number of the chain (0 if entries is empty).
func WriteChain(store PageWriter, entries []FreeEntry, chainPages []uint32) (uint32, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	// Group consecutive entries by freed_txn_id, sort each group by pgno ascending.
	type txnGroup struct {
		freedTxn uint64
		pgnos    []uint32
	}
	var grouped []txnGroup
	for _, e := range entries {
		if n := len(grouped); n > 0 && grouped[n-1].freedTxn == e.FreedTxnID {
			grouped[n-1].pgnos = append(grouped[n-1].pgnos, e.Pgno)
			continue
		}
		grouped = append(grouped, txnGroup{freedTxn: e.FreedTxnID, pgnos: []uint32{e.Pgno}})
	}
	for i := range grouped {
		sort.Slice(grouped[i].pgnos, func(a, b int) bool {
			return grouped[i].pgnos[a] < grouped[i].pgnos[b]
		})
	}

	// Write chain pages using the pre-allocated pages.
	pageIdx := 0
	var head uint32
	for _, g := range grouped {
		for start := 0; start < len(g.pgnos); start += TxnFreeCapacity {
			end := start + TxnFreeCapacity
			if end > len(g.pgnos) {
				end = len(g.pgnos)
			}
			if pageIdx >= len(chainPages) {
				return 0, fmt.Errorf("not enough pre-allocated chain pages for free-list")
			}
			pgno := chainPages[pageIdx]
			pageIdx++
			writeFreePage(store, pgno, head, g.freedTxn, g.pgnos[start:end])
			head = pgno
		}
	}
	return head, nil
}

// writeFreePage writes one PAGE_TYPE_TXN_FREE chain page.
func writeFreePage(store PageWriter, pgno uint32, next uint32, freedTxnID uint64, pages []uint32) {
	page := store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeTxnFree, uint16(len(pages)), pgno)
	putU32(page, TxnFreeNext, next)
	putU64(page, TxnFreeFreedIn, freedTxnID)
	putU32(page, TxnFreeCount, uint32(len(pages)))
	for i, p := range pages {
		putU32(page, TxnFreeArray+i*4, p)
	}
}

// TrailingFreeCount detects trailing free pages that can be truncated.
// Returns the number of pages that can be removed from the end of the file.
func TrailingFreeCount(freePages []uint32, totalPages uint32) uint32 {
	if len(freePages) == 0 {
		return 0
	}
	freeSet := make(map[uint32]struct{}, len(freePages))
	for _, p := range freePages {
		freeSet[p] = struct{}{}
	}
	var shrink uint32
	pgno := totalPages - 1
	for pgno >= 2 {
		if _, ok := freeSet[pgno]; !ok {
			break
		}
		shrink++
		if pgno == 2 {
			break
		}
		pgno--
	}
	return shrink
}
