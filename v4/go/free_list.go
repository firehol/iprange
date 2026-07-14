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
//
// Returns entries in **chain order** (the segment at `head` first, then its
// `next`, and so on). Because new segments are always prepended at the head
// (see AppendSegment), chain order is newest-first. This preserves the
// chronology that Writer.LoadFreeList needs to apply newest-entry-wins
// semantics. Callers that want a pgno-sorted view must sort the result.
//
// Cost: O(chain_page_count) = O(free_count / TxnFreeCapacity).
//
// CRC note: ReadChain is used both at open-time (chain pages intact) and at
// commit-time (where a previous chain head may have been legitimately
// overwritten by COW growth — the dangling-head case). To avoid crashing
// the commit on that known case, ReadChain stops at the first non-TXN_FREE
// page WITHOUT a hard CRC failure. Use ValidateChainCRC for a strict CRC
// check at open time.
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
			break // not a free-list page — stop (handles dangling-head case)
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
	// NOTE: intentionally NOT sorted — chain order (newest-first) is preserved
	// so that newest-entry-wins dedup in LoadFreeList is correct.
	return entries, nil
}

// ValidateChainCRC walks the free-list chain starting at head and verifies the
// per-page CRC32C of every TXN_FREE chain page. Use this at open time (when
// chain pages are intact from the previous commit) to reject a corrupt chain
// page instead of silently loading its garbage freed-page numbers.
//
// Returns nil if the chain is intact or head == 0; an error on the first chain
// page whose CRC fails.
func ValidateChainCRC(store PageReader, head uint32) error {
	if head == 0 {
		return nil
	}
	pgno := head
	seenPages := uint32(0)
	totalPages := store.totalPages()
	for pgno != 0 {
		seenPages++
		if seenPages > 10_000_000 {
			return fmt.Errorf("free-list chain exceeds 10M pages — corrupt")
		}
		// Cycle defense: a valid chain visits only distinct pages, so it can
		// never be longer than totalPages. A self-referential or cyclic chain
		// would otherwise loop forever re-reading pages; reject it here before
		// re-reading any page (keeps the traversal within file bounds).
		if seenPages > totalPages {
			return fmt.Errorf("free-list chain cycle detected")
		}
		if uint64(pgno) >= uint64(totalPages) {
			return nil // chain page beyond file — stop (not a CRC failure)
		}
		page := store.page(pgno)
		if decodeHeader(page).pageType != PageTypeTxnFree {
			return nil // not a chain page — stop
		}
		if !verifyPage(page) {
			return fmt.Errorf("free-list chain page %d fails CRC", pgno)
		}
		pgno = u32le(page, TxnFreeNext)
	}
	return nil
}

// validateFreeEntries validates the CONTENTS of the free-list chain at open
// time. Two complementary checks:
//  1. Self-reference (raw, per chain page): a chain page's freed-pgno array
//     must never contain its OWN page number.
//  2. Live-metadata (newest-entry-wins): a page whose authoritative state is
//     "free" must never be a meta page (0/1), the active data root, or the
//     scope-table root. Stale entries for a reused (tombstoned) or truncated
//     page are harmless and skipped.
func validateFreeEntries(store PageReader, head uint32, dataRoot uint32, scopeRoot uint32) error {
	if head == 0 {
		return nil
	}
	total := store.totalPages()
	// Pass 1: raw self-reference check + collect entries for newest-wins.
	type ent struct {
		pgno uint32
		ftxn uint64
	}
	var entries []ent
	pgno := head
	seen := uint32(0)
	for pgno != 0 {
		seen++
		if seen > total {
			return fmt.Errorf("free-list chain cycle detected")
		}
		if uint64(pgno) >= uint64(total) {
			break
		}
		page := store.page(pgno)
		if decodeHeader(page).pageType != PageTypeTxnFree {
			break
		}
		if !verifyPage(page) {
			return fmt.Errorf("free-list chain page %d fails CRC", pgno)
		}
		freedIn := u64le(page, TxnFreeFreedIn)
		count := int(u32le(page, TxnFreeCount))
		if count > TxnFreeCapacity {
			count = TxnFreeCapacity
		}
		for i := 0; i < count; i++ {
			p := u32le(page, TxnFreeArray+i*4)
			if p == pgno {
				return fmt.Errorf("free-list entry self-references chain page")
			}
			entries = append(entries, ent{pgno: p, ftxn: freedIn})
		}
		pgno = u32le(page, TxnFreeNext)
	}
	// Pass 2: newest-entry-wins (entries are newest-first), then reject live
	// metadata whose authoritative state is "free".
	latest := make(map[uint32]uint64, len(entries))
	for _, e := range entries {
		if _, ok := latest[e.pgno]; !ok {
			latest[e.pgno] = e.ftxn
		}
	}
	for p, ftxn := range latest {
		if ftxn == ^uint64(0) {
			continue // tombstoned (reused)
		}
		if uint64(p) >= uint64(total) {
			continue // truncated; LoadFreeList filters it
		}
		if p < 2 {
			return fmt.Errorf("free-list entry points to meta page")
		}
		if p == dataRoot || p == scopeRoot {
			return fmt.Errorf("free-list entry points to live root")
		}
	}
	return nil
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

// AppendSegment appends a new segment of entries to the front of the free-list
// chain. This is the tombstone-based append-only writer. `entries` MUST be
// sorted by freed_txn_id (consecutive equal values form one group); within each
// group the pgnos are sorted ascending (Rule 5). Each group is packed into
// TxnFreeCapacity-sized pages. Pages are linked so that the LAST page written
// becomes the new head, and the first page of the new segment points its next
// at oldHead. Thus the new segment is the newest in the chain.
//
// With oldHead == 0 this builds a standalone chain (equivalent to the legacy
// full rewrite). Returns the new head page number (== oldHead if empty).
func AppendSegment(store PageWriter, entries []FreeEntry, chainPages []uint32, oldHead uint32) (uint32, error) {
	if len(entries) == 0 {
		return oldHead, nil
	}

	// Group consecutive same-freed-txn entries (caller pre-sorts by txn).
	// Sort each group's pgnos ascending (Rule 5: prefer low-numbered pages).
	pageIdx := 0
	head := oldHead
	i := 0
	for i < len(entries) {
		ftxn := entries[i].FreedTxnID
		var group []uint32
		for i < len(entries) && entries[i].FreedTxnID == ftxn {
			group = append(group, entries[i].Pgno)
			i++
		}
		sort.Slice(group, func(a, b int) bool { return group[a] < group[b] })
		for start := 0; start < len(group); start += TxnFreeCapacity {
			end := start + TxnFreeCapacity
			if end > len(group) {
				end = len(group)
			}
			if pageIdx >= len(chainPages) {
				return 0, fmt.Errorf("not enough pre-allocated chain pages for free-list")
			}
			pgno := chainPages[pageIdx]
			pageIdx++
			writeFreePage(store, pgno, head, ftxn, group[start:end])
			head = pgno
		}
	}
	return head, nil
}

// WriteChain builds a standalone free-list chain using pre-allocated chain
// pages. This is AppendSegment with oldHead == 0 (a fresh chain, not appended
// to an existing one). Kept for standalone/test use; the commit path uses
// AppendSegment to prepend to the existing chain.
func WriteChain(store PageWriter, entries []FreeEntry, chainPages []uint32) (uint32, error) {
	return AppendSegment(store, entries, chainPages, 0)
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
