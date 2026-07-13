package iprangedb

import (
	"fmt"
	"sort"
)

// Changed indicates whether a delete actually removed something.
type Changed int

const (
	Unchanged Changed = 0
	Changed_  Changed = 1
)

// Writer is a single-writer COW B+tree over a page store.
//
// Design (LMDB-inspired):
//   - privatePages: bitset tracking pages COW'd this transaction
//   - cowPage: if pgno is in privatePages → in-place; else COW
//   - freePages: derived at open/commit time by walking the committed tree
//   - allocPage: pop from freePages, or extend the file
//   - Commit: finalize CRCs on private pages, write meta, clear bitset
type Writer[K ipKey[K]] struct {
	store pageStore

	// Format identity
	keyWidth    uint8
	scopeMode   uint8
	createdUnix uint64

	// Active meta page (0 or 1)
	activeMeta uint32

	// Committed state (from active meta at open / last commit)
	committedRoot        uint32
	committedHeight      uint32
	committedPages       uint32
	committedRecordCount uint64
	committedTxnID       uint64

	// Previous committed root/height (for reader-safe reclamation).
	// A reader opened before the last commit reads from prevCommittedRoot;
	// those pages must not be freed until the next generation.
	prevCommittedRoot   uint32
	prevCommittedHeight uint32

	// Pending state (this txn's working copy)
	pendingRoot        uint32
	pendingHeight      uint32
	pendingRecordCount uint64

	poisoned bool

	// Page reclamation: bitset + persistent free-list chain.
	privatePages *pageSet
	freePages    []uint32
	freePos      int

	// freeListHead is the head page of the persistent PAGE_TYPE_TXN_FREE chain
	// stored in the file (0 if no chain). Freed pages are tagged with the txn
	// that freed them and reclaimed subject to MVCC safety (Rules 5-8).
	freeListHead uint32
	// freedThisTxn collects COW victims and old chain/scope pages freed this
	// transaction. Used for same-transaction recycling and appended to the
	// persistent chain at commit.
	freedThisTxn []uint32
	// consumedThisTxn collects pages popped from freePages and reused (made
	// live) this transaction. At commit, each gets a tombstone entry
	// (pgno, MaxUint64) appended to the chain so that newest-entry-wins in
	// LoadFreeList marks them NOT free after close/reopen.
	consumedThisTxn []uint32
	// canRecycle controls whether COW victims may be recycled in-place. Safe
	// only when no readers are active (oldestReaderTxnID == MaxUint64).
	canRecycle bool

	scopeRegistry *ScopeRegistry
	scopeDirty    bool
}

// scopeRoot returns the committed scope-table root (the registry is the single
// source of truth), or 0 when there is no scope table.
func (w *Writer[K]) scopeRoot() uint32 {
	if w.scopeRegistry != nil {
		return w.scopeRegistry.CommittedRoot()
	}
	return 0
}

func scopeRegForMode(scopeMode uint8) *ScopeRegistry {
	if scopeMode == ScopeModeIndirect {
		return NewScopeRegistry()
	}
	return nil
}

// --- construction ---

// Create creates a fresh empty DB.
func Create[K ipKey[K]](scopeMode uint8, createdUnix uint64) (*Writer[K], error) {
	var zero K
	kw := uint8(zero.width())
	store := newVecPageStore(make([]byte, 2*PageSize))
	w := &Writer[K]{
		store:               store,
		keyWidth:            kw,
		scopeMode:           scopeMode,
		createdUnix:         createdUnix,
		activeMeta:          0,
		committedPages:      2,
		committedTxnID:      0,
		prevCommittedRoot:   0,
		prevCommittedHeight: 0,
		privatePages:        newPageSet(2),
		freedThisTxn:        make([]uint32, 0, 4096),
		consumedThisTxn:     make([]uint32, 0, 4096),
		canRecycle:          true,
		scopeRegistry: scopeRegForMode(scopeMode),
		scopeDirty:    false,
	}
	if err := w.writeMetaPage(0, 1, 0, 0, 0, 2, 0); err != nil {
		return nil, err
	}
	if err := w.writeMetaPage(1, 0, 0, 0, 0, 2, 0); err != nil {
		return nil, err
	}
	w.activeMeta = 0
	w.committedTxnID = 1
	return w, nil
}

// openWriter opens from an existing committed page store.
func openWriter[K ipKey[K]](store pageStore) (*Writer[K], error) {
	metaA := decodeMeta(store.page(0))
	metaB := decodeMeta(store.page(1))
	// CRC validation: prefer a meta whose page checksum verifies. If both
	// verify, pick the higher txn_id; if only one verifies, use it; if neither
	// verifies, fall back to the txn_id comparison (best-effort).
	validA := verifyPage(store.page(0))
	validB := verifyPage(store.page(1))
	if !validA && !validB {
		return nil, fmt.Errorf("both meta pages fail CRC")
	}
	var active meta
	var activeNo uint32
	switch {
	case validA && !validB:
		active, activeNo = metaA, 0
	case !validA && validB:
		active, activeNo = metaB, 1
	case metaA.txnID >= metaB.txnID:
		active, activeNo = metaA, 0
	default:
		active, activeNo = metaB, 1
	}
	var zero K
	if active.keyWidth != uint8(zero.width()) {
		return nil, fmt.Errorf("key_width mismatch")
	}
	rs := uint32(recordSizeBytes(zero.width()))
	if active.recordSize != rs {
		return nil, fmt.Errorf("record_size mismatch")
	}
	// Previous committed root/height come from the inactive meta (the one
	// with the lower txn_id). A reader opened before the last commit reads
	// from that generation; its pages must stay reachable until next commit.
	var prevRoot, prevHeight uint32
	if activeNo == 0 {
		prevRoot = metaB.rootPgno
		prevHeight = metaB.treeHeight
	} else {
		prevRoot = metaA.rootPgno
		prevHeight = metaA.treeHeight
	}
	capacity := int(active.totalPages)
	if capacity < 4096 {
		capacity = 4096
	}
	w := &Writer[K]{
		store:               store,
		keyWidth:            active.keyWidth,
		scopeMode:           active.scopeMode,
		createdUnix:         active.createdUnix,
		activeMeta:          activeNo,
		committedRoot:       active.rootPgno,
		committedHeight:     active.treeHeight,
		committedPages:      uint32(active.totalPages),
		committedRecordCount: active.recordCount,
		committedTxnID:      active.txnID,
		prevCommittedRoot:   prevRoot,
		prevCommittedHeight: prevHeight,
		pendingRoot:         active.rootPgno,
		pendingHeight:       active.treeHeight,
		pendingRecordCount:  active.recordCount,
		privatePages:        newPageSet(int(active.totalPages)),
		freeListHead:        active.freeListHead,
		freedThisTxn:        make([]uint32, 0, capacity),
		consumedThisTxn:     make([]uint32, 0, 4096),
		canRecycle:          true,
		scopeRegistry:       nil,
		scopeDirty:          false,
	}
	// Open the scope registry WITHOUT materializing the table (issue-1 fix):
	// CRC-validate every scope page (O(S) time, O(log S) heap) to preserve the
	// corruption guard, then compute nextID via a rightmost-leaf descent.
	if active.scopeMode == ScopeModeIndirect {
		if err := ValidateScopeCRC(w.store.committedBytes(), active.scopeTableRoot); err != nil {
			return nil, fmt.Errorf("corrupt scope table on open: %w", err)
		}
		nextID := uint32(1)
		if maxID, ok := ReadMaxScopeID(w.store.committedBytes(), active.scopeTableRoot); ok {
			nextID = maxID + 1
		}
		w.scopeRegistry = OpenScopeRegistry(active.scopeTableRoot, nextID)
	}
	// Strict CRC validation of the persistent free-list chain. At open time
	// chain pages are intact from the previous commit; a CRC failure here means
	// genuine corruption (not the commit-time dangling-head case).
	if err := ValidateChainCRC(w.store, w.freeListHead); err != nil {
		return nil, fmt.Errorf("corrupt free-list on open: %w", err)
	}
	if err := w.LoadFreeList(^uint64(0)); err != nil {
		return nil, err
	}
	return w, nil
}

// --- public hot-path API ---

func (w *Writer[K]) Set(from, to K, scopeID uint32) error {
	if err := w.check(); err != nil {
		return err
	}
	if from.cmp(to) > 0 {
		return fmt.Errorf("from > to")
	}
	if err := w.deleteRange(from, to); err != nil {
		return err
	}
	if err := w.cowInsert(from, to, scopeID); err != nil {
		return err
	}
	w.pendingRecordCount++
	return nil
}

func (w *Writer[K]) Delete(from, to K) (Changed, error) {
	if err := w.check(); err != nil {
		return Unchanged, err
	}
	if from.cmp(to) > 0 {
		return Unchanged, fmt.Errorf("from > to")
	}
	before := w.pendingRecordCount
	if err := w.deleteRange(from, to); err != nil {
		return Unchanged, err
	}
	if w.pendingRecordCount < before {
		return Changed_, nil
	}
	return Unchanged, nil
}

func (w *Writer[K]) Append(from, to K, scopeID uint32) error {
	if err := w.check(); err != nil {
		return err
	}
	if from.cmp(to) > 0 {
		return fmt.Errorf("from > to")
	}
	if err := w.cowInsert(from, to, scopeID); err != nil {
		return err
	}
	w.pendingRecordCount++
	return nil
}

// Commit commits the pending transaction.
// oldestReaderTxnID must be the minimum txn_id among all active readers, or
// math.MaxUint64 if no readers are active. It is queried fresh at call time
// (not cached) to prevent MVCC violations from stale state.
func (w *Writer[K]) Commit(updatedUnix uint64, oldestReaderTxnID uint64) error {
	if err := w.check(); err != nil {
		return err
	}
	// Refresh MVCC state BEFORE any commit logic uses canRecycle.
	w.canRecycle = oldestReaderTxnID == ^uint64(0)

	// I7 fix: run the sparseness check ONCE per commit (not on every delete).
	// compactIfNeeded walks the tree (O(tree pages)); doing it per-delete was
	// O(n²) for bulk delete. At commit it is one walk — acceptable.
	if err := w.compactIfNeeded(); err != nil {
		return err
	}

	// Rebuild scope table (mode 2) only if the registry changed.
	scopeRebuilt := w.scopeDirty
	if w.scopeDirty {
		// Old scope pages are committed-region pages reachable from the old
		// meta's scopeTableRoot. They MUST NOT be overwritten in-place —
		// a reader pinned at the old txn would see corrupted scope data.
		oldRoot := w.scopeRoot()
		oldScopePageCount := 0
		if oldRoot != 0 {
			before := len(w.freedThisTxn)
			w.collectScopePageNumbers(oldRoot, 0, &w.freedThisTxn)
			oldScopePageCount = len(w.freedThisTxn) - before
		}
		// F2 fix: pre-populate freePool from the Writer's free-list so scope
		// page allocation reuses freed pages instead of always extending the
		// file. Pages freed in PREVIOUS transactions live in freePages; the
		// old scope pages freed above are not reclaimable yet.
		var freePool []uint32
		estimate := oldScopePageCount + 2
		for i := 0; i < estimate; i++ {
			if w.freePos < len(w.freePages) {
				pgno := w.freePages[w.freePos]
				w.freePos++
				w.privatePages.insert(pgno)
				freePool = append(freePool, pgno)
			} else {
				break
			}
		}
		// Remember the speculative pre-pop so we can tombstone the pages
		// actually consumed by the scope rebuild. buildScopeTree pops the
		// pages it needs from freePool; the rest are returned below. Those
		// consumed pages are now live scope data and must NOT reappear as
		// free, so they are recorded for tombstoning at commit.
		scopePoolSnapshot := append([]uint32(nil), freePool...)
		newRoot := uint32(0)
		if w.scopeRegistry != nil && !w.scopeRegistry.IsEmpty() {
			// EntriesForCommit re-reads the committed table (still at oldRoot,
			// since Promote has not run) and merges this-txn new entries.
			all := w.scopeRegistry.EntriesForCommit(w.store.committedBytes())
			var allocated []uint32
			root, err := buildScopeTree(w.store, all, &allocated, &freePool)
			if err != nil {
				return err
			}
			// Register scope pages in privatePages for CRC finalization.
			for _, pgno := range allocated {
				w.privatePages.insert(pgno)
			}
			newRoot = root
		}
		// Advance the registry to the newly-committed root (folds new entries
		// into the warm committed index, clears the new set).
		if w.scopeRegistry != nil {
			w.scopeRegistry.Promote(newRoot)
		}
		// Return unused freePool pages to the Writer's free-list and tombstone
		// the ones buildScopeTree actually consumed.
		returned := make(map[uint32]struct{}, len(freePool))
		for _, pgno := range freePool {
			w.freePos--
			w.freePages[w.freePos] = pgno
			w.privatePages.remove(pgno)
			returned[pgno] = struct{}{}
		}
		for _, pgno := range scopePoolSnapshot {
			if _, ok := returned[pgno]; !ok {
				w.consumedThisTxn = append(w.consumedThisTxn, pgno)
			}
		}
		w.scopeDirty = false
	}

	// Finalize CRCs on all private pages.
	for _, pgno := range w.privatePages.iter() {
		if pgno >= 2 {
			finalizeChecksum(w.store.pageMut(pgno))
		}
	}

	// Tag for freed entries written this commit: the generation being
	// superseded (MVCC reclamation uses strict <).
	newTxnIDVal := w.committedTxnID

	// Fast path: nothing was freed, nothing consumed, and no scope rebuild.
	// The existing append-only chain is still valid, so skip the append.
	nothingFreed := len(w.freedThisTxn) == 0
	nothingConsumed := len(w.consumedThisTxn) == 0
	if nothingFreed && nothingConsumed && !scopeRebuilt {
		total := w.store.totalPages()
		inactive := 1 - w.activeMeta
		newTxnID := w.committedTxnID + 1
		if err := w.writeMetaPage(inactive, newTxnID, w.pendingRoot,
			w.pendingHeight, w.pendingRecordCount, total, updatedUnix); err != nil {
			return err
		}
		if err := w.store.sync(); err != nil {
			return err
		}
		w.activeMeta = inactive
		w.committedTxnID = newTxnID
		w.prevCommittedRoot = w.committedRoot
		w.prevCommittedHeight = w.committedHeight
		w.committedRoot = w.pendingRoot
		w.committedHeight = w.pendingHeight
		w.committedRecordCount = w.pendingRecordCount
		w.committedPages = total
		w.store.setCommittedPages(total)
		w.resetTxn()
		if err := w.LoadFreeList(oldestReaderTxnID); err != nil {
			return err
		}
		return nil
	}

	// ── Tombstone append-only free-list commit ───────────────────────────
	//
	// The chain grows monotonically: we append ONE new segment holding this
	// transaction's freed entries (COW victims + old scope pages) tagged with
	// newTxnIDVal, plus tombstone entries (MaxUint64) for every page that is
	// LIVE this commit but was consumed (reused) from the free-list. LoadFreeList
	// resolves the final free set with newest-entry-wins, so a page that is
	// freed and later reused never reappears as free after close/reopen. Chain
	// pages themselves are excluded from the free set by LoadFreeList (they
	// appear in ReadChainPageNumbers), so they need no tombstone.
	//
	// Tombstone rule: a page popped from the free-list is tombstoned ONLY if it
	// is still live at commit (in privatePages). A page that was consumed and
	// then freed again in the SAME transaction (e.g. a COW copy from the
	// delete-all collapse path) is no longer live, so it is NOT tombstoned —
	// its freed entry makes it correctly free.
	var liveConsumed []uint32
	for _, pgno := range w.consumedThisTxn {
		if w.privatePages.contains(pgno) {
			liveConsumed = append(liveConsumed, pgno)
		}
	}

	// Effective free set for truncation = (previously free ∪ freed this txn)
	// − live-consumed. Live-consumed pages are LIVE and must never be truncated,
	// so they are excluded from the trailing scan.
	preTruncateTotal := w.store.totalPages()
	var trailing uint32
	if oldestReaderTxnID == ^uint64(0) {
		eff := make(map[uint32]struct{}, len(w.freePages)+len(w.freedThisTxn))
		for _, p := range w.freePages {
			eff[p] = struct{}{}
		}
		for _, p := range w.freedThisTxn {
			eff[p] = struct{}{}
		}
		for _, p := range liveConsumed {
			delete(eff, p)
		}
		freePgnos := make([]uint32, 0, len(eff))
		for p := range eff {
			freePgnos = append(freePgnos, p)
		}
		sort.Slice(freePgnos, func(i, j int) bool { return freePgnos[i] < freePgnos[j] })
		trailing = TrailingFreeCount(freePgnos, preTruncateTotal)
	}
	newTotal := preTruncateTotal - trailing

	// Build entries: freed (drop trailing pages that will be truncated) and
	// tombstones for live-consumed pages (which are live, so never trailing).
	entriesToWrite := make([]FreeEntry, 0, len(w.freedThisTxn)+len(liveConsumed))
	for _, pgno := range w.freedThisTxn {
		if pgno < newTotal {
			entriesToWrite = append(entriesToWrite, FreeEntry{Pgno: pgno, FreedTxnID: newTxnIDVal})
		}
	}
	for _, pgno := range liveConsumed {
		// Live-consumed pages are live (tree/scope data) ⇒ all < newTotal.
		// Guard defensively anyway.
		if pgno < newTotal {
			entriesToWrite = append(entriesToWrite, FreeEntry{Pgno: pgno, FreedTxnID: ^uint64(0)})
		}
	}

	// Sort by freed_txn_id: tombstones (MaxUint64) sort after freed entries, so
	// they are written last and become the newest pages in the chain —
	// newest-entry-wins then gives tombstones priority for reused pages.
	sort.Slice(entriesToWrite, func(i, j int) bool {
		return entriesToWrite[i].FreedTxnID < entriesToWrite[j].FreedTxnID
	})

	// A1: when the existing chain is large (≥20 pages), compact it instead
	// of appending. Read old entries, merge with new entries (newest-wins
	// dedup), filter tombstones, and rewrite as a single clean chain. Old
	// chain pages are freed (added as reclaimable entries) so they can be
	// reused by future transactions.
	var oldChainPages []uint32
	if w.freeListHead != 0 {
		oldChainPages = ReadChainPageNumbers(w.store, w.freeListHead)
	}

	var chainPages []uint32

	if len(oldChainPages) >= 20 {
		// Compaction path: read old entries, deduplicate, rewrite.
		var oldEntries []FreeEntry
		if w.freeListHead != 0 {
			var err error
			oldEntries, err = ReadChain(w.store, w.freeListHead)
			if err != nil {
				return err
			}
		}

		// Merge: new entries take priority (they are this txn's state).
		// ReadChain returns newest-first, so first-seen among old entries is
		// the newest old entry — matching Rust's entry().or_insert() order.
		merged := make(map[uint32]uint64, len(oldEntries)+len(entriesToWrite))
		for _, e := range oldEntries {
			if _, ok := merged[e.Pgno]; !ok {
				merged[e.Pgno] = e.FreedTxnID
			}
		}
		for _, e := range entriesToWrite {
			merged[e.Pgno] = e.FreedTxnID
		}

		compactEntries := make([]FreeEntry, 0, len(merged))
		for pgno, ftxn := range merged {
			if ftxn == ^uint64(0) {
				continue
			}
			if pgno >= newTotal {
				continue
			}
			compactEntries = append(compactEntries, FreeEntry{Pgno: pgno, FreedTxnID: ftxn})
		}

		// Old chain pages are being replaced by the compact chain. Add them
		// as freed entries so they appear in the compact chain and can be
		// reused by future transactions.
		for _, pgno := range oldChainPages {
			if pgno < newTotal {
				compactEntries = append(compactEntries, FreeEntry{Pgno: pgno, FreedTxnID: newTxnIDVal})
			}
		}
		sort.Slice(compactEntries, func(i, j int) bool {
			return compactEntries[i].FreedTxnID < compactEntries[j].FreedTxnID
		})

		needed := ChainPageCount(compactEntries)
		chainPages = make([]uint32, 0, needed)
		for len(chainPages) < needed {
			pgno, err := w.allocChainPage()
			if err != nil {
				return err
			}
			chainPages = append(chainPages, pgno)
		}

		// F1 fix: drop entries whose page was just allocated as a chain page
		// (it is now metadata, not free data).
		chainSet := make(map[uint32]struct{}, len(chainPages))
		for _, p := range chainPages {
			chainSet[p] = struct{}{}
		}
		kept := compactEntries[:0]
		for _, e := range compactEntries {
			if _, isChain := chainSet[e.Pgno]; !isChain {
				kept = append(kept, e)
			}
		}
		compactEntries = kept

		if len(compactEntries) == 0 {
			w.freeListHead = 0
		} else {
			head, err := WriteChain(w.store, compactEntries, chainPages)
			if err != nil {
				return err
			}
			w.freeListHead = head
		}
	} else {
		// Append-only path: prepend new segment to existing chain. Old chain
		// pages stay reachable (append-only) — they are never freed here.
		needed := ChainPageCount(entriesToWrite)
		chainPages = make([]uint32, 0, needed)
		for len(chainPages) < needed {
			pgno, err := w.allocChainPage()
			if err != nil {
				return err
			}
			chainPages = append(chainPages, pgno)
		}

		oldHead := w.freeListHead
		head, err := AppendSegment(w.store, entriesToWrite, chainPages, oldHead)
		if err != nil {
			return err
		}
		w.freeListHead = head
	}

	// F9 fix: finalize CRCs on the newly written chain pages.
	for _, pgno := range chainPages {
		finalizeChecksum(w.store.pageMut(pgno))
	}

	// Truncate. Chain pages allocated from the growth region may sit at
	// positions >= newTotal; the effective total must preserve them so they are
	// not truncated away.
	var maxChainPage uint32
	for _, p := range chainPages {
		if p > maxChainPage {
			maxChainPage = p
		}
	}
	var total uint32
	if trailing > 0 {
		effectiveTotal := newTotal
		if maxChainPage+1 > effectiveTotal {
			effectiveTotal = maxChainPage + 1
		}
		if effectiveTotal < preTruncateTotal {
			if err := w.store.truncate(effectiveTotal); err != nil {
				return err
			}
		}
		total = effectiveTotal
	} else {
		total = preTruncateTotal
	}

	inactive := 1 - w.activeMeta
	newTxnID := w.committedTxnID + 1
	if err := w.writeMetaPage(inactive, newTxnID, w.pendingRoot,
		w.pendingHeight, w.pendingRecordCount, total, updatedUnix); err != nil {
		return err
	}
	if err := w.store.sync(); err != nil {
		return err
	}
	w.activeMeta = inactive
	w.committedTxnID = newTxnID
	// Save previous committed root/height BEFORE flipping, so the old
	// generation stays protected from reclamation while readers may use it.
	w.prevCommittedRoot = w.committedRoot
	w.prevCommittedHeight = w.committedHeight
	w.committedRoot = w.pendingRoot
	w.committedHeight = w.pendingHeight
	w.committedRecordCount = w.pendingRecordCount
	w.committedPages = total
	w.store.setCommittedPages(total)
	w.resetTxn()
	if err := w.LoadFreeList(oldestReaderTxnID); err != nil {
		return err
	}
	return nil
}

func (w *Writer[K]) RecordCount() uint64 {
	return w.pendingRecordCount
}

// FreePageCount returns the number of pages currently in the in-memory
// free-list (reclaimable pool). Reflects the result of the last LoadFreeList:
// newest-entry-wins over the append-only chain, with tombstones and chain
// pages excluded. Used by tests/audits to verify the tombstone invariant (a
// consumed page must not reappear here after close/reopen).
func (w *Writer[K]) FreePageCount() int {
	return len(w.freePages)
}

// IntoImage consumes the writer and returns the image (vecPageStore only).
func (w *Writer[K]) IntoImage() ([]byte, bool) {
	vps, ok := w.store.(*vecPageStore)
	if !ok {
		return nil, false
	}
	return vps.image, true
}

// --- COW mechanics ---

func (w *Writer[K]) check() error {
	if w.poisoned {
		return fmt.Errorf("writer poisoned")
	}
	return nil
}

// cowPage returns a private (writable) copy of pgno. If pgno is already private
// (COW'd this transaction), it is returned as-is for in-place modification.
func (w *Writer[K]) cowPage(pgno uint32) (uint32, error) {
	if w.privatePages.contains(pgno) {
		return pgno, nil
	}
	newPgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.store.copyPage(pgno, newPgno)
	w.privatePages.insert(newPgno)
	// The old page is a COW victim — record it for the free-list chain and
	// same-transaction recycling.
	w.freedThisTxn = append(w.freedThisTxn, pgno)
	return newPgno, nil
}

// allocPage pops a page from the free pool or extends the file.
// COW victims (freedThisTxn) are NOT reused mid-transaction — they are still
// reachable from committedRoot until the meta flip.
func (w *Writer[K]) allocPage() (uint32, error) {
	if w.freePos < len(w.freePages) {
		pgno := w.freePages[w.freePos]
		w.freePos++
		w.privatePages.insert(pgno)
		// Track for tombstone at commit: this page was free and is now live.
		w.consumedThisTxn = append(w.consumedThisTxn, pgno)
		return pgno, nil
	}
	pgno, err := w.store.allocPage()
	if err != nil {
		return 0, err
	}
	w.privatePages.ensureCapacity(int(pgno) + 1)
	w.privatePages.insert(pgno)
	return pgno, nil
}

// allocChainPage allocates a page for free-list chain metadata. Like allocPage
// but does NOT record the page in consumedThisTxn: chain pages are excluded
// from the free-list by LoadFreeList (they appear in ReadChainPageNumbers), so
// they need no tombstone entry. This breaks what would otherwise be a circular
// dependency (chain page count depends on entries, which would depend on
// tombstones for the chain pages).
func (w *Writer[K]) allocChainPage() (uint32, error) {
	if w.freePos < len(w.freePages) {
		pgno := w.freePages[w.freePos]
		w.freePos++
		w.privatePages.insert(pgno)
		return pgno, nil
	}
	pgno, err := w.store.allocPage()
	if err != nil {
		return 0, err
	}
	w.privatePages.ensureCapacity(int(pgno) + 1)
	w.privatePages.insert(pgno)
	return pgno, nil
}

// LoadFreeList loads the free pool from the persistent PAGE_TYPE_TXN_FREE chain.
//
// Applies newest-entry-wins semantics over the append-only chain: for each
// pgno, the most recent entry (first in chain order, since ReadChain returns
// newest-first) determines state. A tombstone entry (FreedTxnID == MaxUint64)
// means the page was reused and is NOT free. A normal entry means free, subject
// to MVCC filtering (FreedTxnID < oldestReaderTxnID, or all reclaimable when
// MaxUint64). Chain pages themselves are excluded — they are live metadata.
func (w *Writer[K]) LoadFreeList(oldestReaderTxnID uint64) error {
	w.canRecycle = oldestReaderTxnID == ^uint64(0)
	if w.freeListHead == 0 {
		w.freePages = w.freePages[:0]
		w.freePos = 0
		return nil
	}
	entries, err := ReadChain(w.store, w.freeListHead)
	if err != nil {
		return err
	}
	// Chain pages are live metadata and must never be handed out as free,
	// even if an older segment still lists them as freed. Exclude them.
	chainPages := ReadChainPageNumbers(w.store, w.freeListHead)
	chainSet := make(map[uint32]struct{}, len(chainPages))
	for _, p := range chainPages {
		chainSet[p] = struct{}{}
	}
	// Newest-entry-wins: entries are newest-first, so the first occurrence of
	// each pgno is its most recent state.
	latest := make(map[uint32]uint64, len(entries))
	for _, e := range entries {
		if _, ok := latest[e.Pgno]; !ok {
			latest[e.Pgno] = e.FreedTxnID
		}
	}
	var free []uint32
	for pgno, ftxn := range latest {
		if _, isChain := chainSet[pgno]; isChain {
			continue
		}
		if ftxn == ^uint64(0) {
			continue // tombstone → reused, not free
		}
		if oldestReaderTxnID != ^uint64(0) && ftxn >= oldestReaderTxnID {
			continue // MVCC: reader still needs this page
		}
		free = append(free, pgno)
	}
	// Filter out entries beyond the current store (truncated pages).
	total := w.store.totalPages()
	bounded := free[:0]
	for _, p := range free {
		if p < total {
			bounded = append(bounded, p)
		}
	}
	w.freePages = bounded
	sort.Slice(w.freePages, func(i, j int) bool { return w.freePages[i] < w.freePages[j] })
	w.freePos = 0
	return nil
}

// resetTxn clears the private-pages bitset for the next transaction.

func (w *Writer[K]) collectScopePageNumbers(pgno uint32, depth uint32, out *[]uint32) {
	if depth > TreeHeightMax || uint64(pgno) >= uint64(w.store.totalPages()) {
		return
	}
	*out = append(*out, pgno)
	page := w.store.page(pgno)
	h := decodeHeader(page)
	if h.pageType == PageTypeScopeBranch {
		bv := newBranchView(page, int(h.entryCount), int(ScopeKeyWidth))
		for j := 0; j < bv.childCount(); j++ {
			w.collectScopePageNumbers(bv.child(j), depth+1, out)
		}
	}
}

func (w *Writer[K]) resetTxn() {
	w.privatePages.clear()
	w.freedThisTxn = w.freedThisTxn[:0]
	w.consumedThisTxn = w.consumedThisTxn[:0]
	w.privatePages.ensureCapacity(int(w.store.totalPages()))
}

func (w *Writer[K]) cowRoot() (uint32, error) {
	if w.pendingRoot == 0 {
		return 0, nil
	}
	old := w.pendingRoot
	new, err := w.cowPage(old)
	if err != nil {
		return 0, err
	}
	if new != old {
		w.pendingRoot = new
	}
	return new, nil
}

// --- B+tree insert ---

func (w *Writer[K]) cowInsert(from, to K, scopeID uint32) error {
	if w.pendingRoot == 0 {
		leaf, err := w.allocPage()
		if err != nil {
			return err
		}
		if err := w.writeLeafSingle(leaf, from, to, scopeID); err != nil {
			return err
		}
		w.pendingRoot = leaf
		w.pendingHeight = 1
		return nil
	}
	root, err := w.cowRoot()
	if err != nil {
		return err
	}
	split, err := w.cowInsertDescend(root, 1, from, to, scopeID)
	if err != nil {
		return err
	}
	if split != nil {
		newRoot, err := w.allocPage()
		if err != nil {
			return err
		}
		if err := w.writeBranchNew(newRoot, root, split.sep, split.right); err != nil {
			return err
		}
		w.pendingRoot = newRoot
		w.pendingHeight++
	}
	return nil
}

type branchSplit[K ipKey[K]] struct {
	sep   K
	right uint32
}

func (w *Writer[K]) cowInsertDescend(pgno uint32, depth uint32, from, to K, scopeID uint32) (*branchSplit[K], error) {
	if depth >= w.pendingHeight {
		return w.leafInsert(pgno, from, to, scopeID)
	}
	// Branch: find child, descend
	var childIdx int
	var childPgno uint32
	{
		page := w.store.page(pgno)
		count := int(decodeHeader(page).entryCount)
		bv := newBranchView(page, count, int(w.keyWidth))
		childIdx = branchFindChild[K](&bv, from)
		childPgno = bv.child(childIdx)
	}
	cowChild, err := w.cowPage(childPgno)
	if err != nil {
		return nil, err
	}
	if cowChild != childPgno {
		if err := w.branchUpdateChild(pgno, childIdx, cowChild); err != nil {
			return nil, err
		}
	}
	split, err := w.cowInsertDescend(cowChild, depth+1, from, to, scopeID)
	if err != nil {
		return nil, err
	}
	if split != nil {
		return w.branchAbsorbSplit(pgno, childIdx, cowChild, split.sep, split.right)
	}
	return nil, nil
}

func (w *Writer[K]) leafInsert(pgno uint32, from, to K, scopeID uint32) (*branchSplit[K], error) {
	var zero K
	kw := zero.width()
	rs := recordSizeBytes(kw)
	lmax := leafMax(w.keyWidth)

	page := w.store.page(pgno)
	count := int(decodeHeader(page).entryCount)
	pos := leafFindPos[K](page, count, from, kw)

	if count < lmax {
		pageMut := w.store.pageMut(pgno)
		start := PageHeaderSize + pos*rs
		end := PageHeaderSize + count*rs
		copy(pageMut[start+rs:end+rs], pageMut[start:end])
		from.writeLE(pageMut[start : start+kw])
		to.writeLE(pageMut[start+kw : start+2*kw])
		putU32(pageMut, start+2*kw, scopeID)
		writeHeader(pageMut, PageTypeLeaf, uint16(count+1), pgno)
		return nil, nil
	}

	// Split
	var src [PageSize]byte
	copy(src[:], w.store.page(pgno))
	newCount := count + 1
	mid := newCount / 2
	if err := w.writeLeafSplit(pgno, src[:], count, pos, from, to, scopeID, 0, mid); err != nil {
		return nil, err
	}
	right, err := w.allocPage()
	if err != nil {
		return nil, err
	}
	if err := w.writeLeafSplit(right, src[:], count, pos, from, to, scopeID, mid, newCount-mid); err != nil {
		return nil, err
	}
	// Separator = first `from` in the right page
	rpage := w.store.page(right)
	rv := newLeafView(rpage, mid, kw)
	sepBytes := rv.recordFrom(0)
	var sep K = zero.readLE(sepBytes)
	return &branchSplit[K]{sep: sep, right: right}, nil
}

func leafFindPos[K ipKey[K]](page []byte, count int, from K, kw int) int {
	lv := newLeafView(page, count, kw)
	lo, hi := 0, count
	for lo < hi {
		mid := lo + (hi-lo)/2
		k := from.readLE(lv.recordFrom(mid))
		if k.cmp(from) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (w *Writer[K]) writeLeafSplit(pgno uint32, src []byte, oldCount, insertPos int,
	insFrom, insTo K, insScope uint32, startIdx, count int) error {
	var zero K
	kw := zero.width()
	rs := recordSizeBytes(kw)
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeLeaf, uint16(count), pgno)
	for outI := 0; outI < count; outI++ {
		absI := startIdx + outI
		var f, t K
		var s uint32
		if absI == insertPos {
			f, t, s = insFrom, insTo, insScope
		} else {
			oldI := absI
			if absI > insertPos {
				oldI = absI - 1
			}
			off := PageHeaderSize + oldI*rs
			f = zero.readLE(src[off : off+kw])
			t = zero.readLE(src[off+kw : off+2*kw])
			s = u32le(src, off+2*kw)
		}
		outOff := PageHeaderSize + outI*rs
		f.writeLE(page[outOff : outOff+kw])
		t.writeLE(page[outOff+kw : outOff+2*kw])
		putU32(page, outOff+2*kw, s)
	}
	return nil
}

func (w *Writer[K]) writeLeafSingle(pgno uint32, from, to K, scopeID uint32) error {
	var zero K
	kw := zero.width()
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeLeaf, 1, pgno)
	from.writeLE(page[PageHeaderSize : PageHeaderSize+kw])
	to.writeLE(page[PageHeaderSize+kw : PageHeaderSize+2*kw])
	putU32(page, PageHeaderSize+2*kw, scopeID)
	return nil
}

// --- B+tree delete ---

func (w *Writer[K]) deleteRange(from, to K) error {
	for {
		if w.pendingRoot == 0 {
			return nil
		}
		o, found, err := w.scanFirstOverlap(from, to)
		if err != nil {
			return err
		}
		if !found {
			break
		}
		cowLeaf, err := w.cowToLeaf(o.leafPgno)
		if err != nil {
			return err
		}
		if err := w.leafDeleteAt(cowLeaf, o.recIdx); err != nil {
			return err
		}
		w.pendingRecordCount--
		if o.recFrom.cmp(from) < 0 {
			if trimEnd, ok := from.checkedDec(); ok {
				if err := w.cowInsert(o.recFrom, trimEnd, o.recScope); err != nil {
					return err
				}
				w.pendingRecordCount++
			}
		}
		if o.recTo.cmp(to) > 0 {
			if trimStart, ok := to.checkedInc(); ok {
				if err := w.cowInsert(trimStart, o.recTo, o.recScope); err != nil {
					return err
				}
				w.pendingRecordCount++
			}
		}
	}
	// F7 fix: collapse tree when all records are deleted. Move COW copies
	// to freedThisTxn so they go to the free-list chain at commit.
	if w.pendingRecordCount == 0 && w.pendingRoot != 0 {
		for _, pgno := range w.privatePages.iter() {
			if pgno >= 2 {
				w.freedThisTxn = append(w.freedThisTxn, pgno)
			}
		}
		w.privatePages.clear()
		w.pendingRoot = 0
		w.pendingHeight = 0
	}
	// I7 fix: compaction is deferred to Commit (one walk per commit, not per
	// delete). Calling compactIfNeeded here made every delete O(tree pages).
	return nil
}

// countTreePages walks the pending B+tree rooted at pgno and counts every
// page reachable from it (branches and leaves).
func (w *Writer[K]) countTreePages(pgno uint32, height uint32, count *uint64) {
	*count++
	if height <= 1 {
		return
	}
	page := w.store.page(pgno)
	h := decodeHeader(page)
	if h.pageType == PageTypeBranch {
		bv := newBranchView(page, int(h.entryCount), int(w.keyWidth))
		for j := 0; j < bv.childCount(); j++ {
			w.countTreePages(bv.child(j), height-1, count)
		}
	}
}

// compactIfNeeded rebuilds the pending tree compactly when it is less than
// 25% full (actual tree pages exceed 4x the pages needed for the current
// records). Triggered after a large delete that leaves the tree sparse.
func (w *Writer[K]) compactIfNeeded() error {
	if w.pendingRoot == 0 || w.pendingRecordCount == 0 {
		return nil
	}
	var treePages uint64
	w.countTreePages(w.pendingRoot, w.pendingHeight, &treePages)
	lmax := uint64(leafMax(w.keyWidth))
	neededPages := (w.pendingRecordCount + lmax - 1) / lmax
	if treePages > neededPages*4+4 {
		return w.rebuildCompact()
	}
	return nil
}

// rebuildCompact rebuilds the pending tree compactly, streaming one leaf at a
// time so that only one leaf's worth of records is held in memory.
//
// Leaves are processed in tree (key) order, which yields globally sorted
// records → a densely packed tree. Only one leaf's records are buffered at a
// time.
//
// Only PRIVATE pages (uncommitted COW copies) may be recycled. Pages still
// shared with the committed tree may be referenced by pinned readers, so they
// are read for their records but never freed here — they are reclaimed later
// via MVCC reclamation of the superseded generation.
//
// Safety (cross-leaf overwrite): each private leaf is returned to the free pool
// only AFTER its records have been buffered. When leaf L_k is being read, the
// pool holds only non-leaf pages and the private leaves processed before L_k —
// never L_k itself or any later leaf — so the allocator cannot clobber an
// unread leaf. This holds for any processing order; tree order is chosen for
// dense packing.
//
// Placement: each freed leaf is inserted at its sorted position within the
// unconsumed tail of the free pool, so the allocator keeps consuming the lowest
// page numbers first. The new compact tree then lands in the lowest available
// pages and the trailing gap above it is truncated away at commit.
func (w *Writer[K]) rebuildCompact() error {
	oldRoot := w.pendingRoot
	oldHeight := w.pendingHeight

	// Collect old leaf page numbers in tree (left-to-right = key) order.
	leafPages := make([]uint32, 0)
	if err := w.collectLeafPages(oldRoot, oldHeight, &leafPages); err != nil {
		return err
	}
	leafSet := make(map[uint32]struct{}, len(leafPages))
	for _, p := range leafPages {
		leafSet[p] = struct{}{}
	}

	// New pool = unconsumed tail ∪ old non-leaf private pages (disjoint,
	// sorted). Old leaf pages are returned to the pool one-at-a-time after
	// buffering (private) or not at all (shared).
	newFree := append([]uint32(nil), w.freePages[w.freePos:]...)
	var nonLeaf []uint32
	for _, pgno := range w.privatePages.iter() {
		if pgno >= 2 {
			if _, isLeaf := leafSet[pgno]; !isLeaf {
				nonLeaf = append(nonLeaf, pgno)
			}
		}
	}
	sort.Slice(nonLeaf, func(i, j int) bool { return nonLeaf[i] < nonLeaf[j] })
	for _, pgno := range nonLeaf {
		w.freedThisTxn = append(w.freedThisTxn, pgno)
		newFree = append(newFree, pgno)
	}
	sort.Slice(newFree, func(i, j int) bool { return newFree[i] < newFree[j] })
	w.freePages = newFree
	w.freePos = 0
	// Capture private-leaf set before clearing privatePages.
	privateLeaf := make(map[uint32]struct{}, len(leafPages))
	for _, p := range leafPages {
		if w.privatePages.contains(p) {
			privateLeaf[p] = struct{}{}
		}
	}
	w.privatePages.clear()
	w.pendingRoot = 0
	w.pendingHeight = 0
	w.pendingRecordCount = 0

	// Single pass in tree (key) order: dense insertion. Private leaves are
	// recycled after buffering; shared leaves are read but kept in place.
	var buf []overlapRecord[K]
	var zero K
	kw := zero.width()
	for _, leafPgno := range leafPages {
		buf = buf[:0]
		{
			page := w.store.page(leafPgno)
			h := decodeHeader(page)
			lv := newLeafView(page, int(h.entryCount), kw)
			for i := 0; i < lv.len(); i++ {
				buf = append(buf, overlapRecord[K]{
					from:  zero.readLE(lv.recordFrom(i)),
					to:    zero.readLE(lv.recordTo(i)),
					scope: lv.recordScopeID(i),
				})
			}
		}
		if _, isPrivate := privateLeaf[leafPgno]; isPrivate {
			// Records are owned in buf; safe to recycle this leaf now.
			w.freedThisTxn = append(w.freedThisTxn, leafPgno)
			// Insert into the unconsumed tail at its sorted position so the
			// allocator keeps reusing the lowest page numbers first.
			lo := w.freePos
			pos := lo + sort.Search(len(w.freePages)-lo, func(i int) bool {
				return w.freePages[lo+i] >= leafPgno
			})
			w.freePages = append(w.freePages, 0)
			copy(w.freePages[pos+1:], w.freePages[pos:])
			w.freePages[pos] = leafPgno
		}
		for _, r := range buf {
			if err := w.cowInsert(r.from, r.to, r.scope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}
	}
	return nil
}

// collectLeafPages appends the pending tree's leaf page numbers to *out in
// tree (left-to-right) order. Used by rebuildCompact to stream leaves.
func (w *Writer[K]) collectLeafPages(pgno uint32, height uint32, out *[]uint32) error {
	if height <= 1 {
		*out = append(*out, pgno)
		return nil
	}
	var zero K
	kw := zero.width()
	page := w.store.page(pgno)
	h := decodeHeader(page)
	bv := newBranchView(page, int(h.entryCount), kw)
	for j := 0; j < bv.childCount(); j++ {
		if err := w.collectLeafPages(bv.child(j), height-1, out); err != nil {
			return err
		}
	}
	return nil
}

type overlapInfo[K ipKey[K]] struct {
	recFrom  K
	recTo    K
	recScope uint32
	leafPgno uint32
	recIdx   int
}
func (w *Writer[K]) scanFirstOverlap(from, to K) (overlapInfo[K], bool, error) {
	if w.pendingRoot == 0 {
		var zero overlapInfo[K]
		return zero, false, nil
	}
	return w.scanOverlapNode(w.pendingRoot, from, to)
}

func (w *Writer[K]) scanOverlapNode(pgno uint32, from, to K) (overlapInfo[K], bool, error) {
	var zero overlapInfo[K]
	var z K
	kw := z.width()
	page := w.store.page(pgno)
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		lv := newLeafView(page, int(h.entryCount), kw)
		for i := 0; i < lv.len(); i++ {
			rf := z.readLE(lv.recordFrom(i))
			if rf.cmp(to) > 0 {
				return zero, false, nil
			}
			rt := z.readLE(lv.recordTo(i))
			if rt.cmp(from) >= 0 {
				return overlapInfo[K]{
					recFrom: rf, recTo: rt, recScope: lv.recordScopeID(i),
					leafPgno: pgno, recIdx: i,
				}, true, nil
			}
		}
		return zero, false, nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		start := branchFindChild[K](&bv, from)
		for j := start; j < bv.childCount(); j++ {
			// I7: separator-based early exit. The separator at j-1 is the
			// lower bound of child j's key range. If it exceeds `to`, child j
			// and all later children can't overlap [from, to] — stop scanning.
			if j > 0 {
				sep := readKey[K](bv.sep(j - 1))
				if sep.cmp(to) > 0 {
					return zero, false, nil
				}
			}
			r, found, err := w.scanOverlapNode(bv.child(j), from, to)
			if err != nil {
				return zero, false, err
			}
			if found {
				return r, true, nil
			}
		}
		return zero, false, nil
	default:
		return zero, false, fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

func (w *Writer[K]) cowToLeaf(targetLeaf uint32) (uint32, error) {
	var zero K
	kw := zero.width()
	// Read guide key from target leaf
	var guideKey K
	{
		page := w.store.page(targetLeaf)
		count := int(decodeHeader(page).entryCount)
		if count == 0 {
			guideKey = zero.minKey()
		} else {
			lv := newLeafView(page, count, kw)
			guideKey = zero.readLE(lv.recordFrom(0))
		}
	}
	pgno, err := w.cowRoot()
	if err != nil {
		return 0, err
	}
	for depth := uint32(1); depth < w.pendingHeight; depth++ {
		var childIdx int
		var childPgno uint32
		{
			page := w.store.page(pgno)
			count := int(decodeHeader(page).entryCount)
			bv := newBranchView(page, count, kw)
			childIdx = branchFindChild[K](&bv, guideKey)
			childPgno = bv.child(childIdx)
		}
		cowChild, err := w.cowPage(childPgno)
		if err != nil {
			return 0, err
		}
		if cowChild != childPgno {
			if err := w.branchUpdateChild(pgno, childIdx, cowChild); err != nil {
				return 0, err
			}
		}
		pgno = cowChild
	}
	return pgno, nil
}

func (w *Writer[K]) leafDeleteAt(pgno uint32, pos int) error {
	var zero K
	kw := zero.width()
	rs := recordSizeBytes(kw)
	page := w.store.page(pgno)
	count := int(decodeHeader(page).entryCount)
	newCount := count - 1
	start := PageHeaderSize + pos*rs
	end := PageHeaderSize + count*rs
	copy(page[start:end-rs], page[start+rs:end])
	for i := end - rs; i < end; i++ {
		page[i] = 0
	}
	writeHeader(page, PageTypeLeaf, uint16(newCount), pgno)
	return nil
}

// --- branch ops ---

func branchFindChild[K ipKey[K]](bv *branchView, key K) int {
	lo, hi := 0, bv.sepCount
	for lo < hi {
		mid := lo + (hi-lo)/2
		var zero K
		sep := zero.readLE(bv.sep(mid))
		if sep.cmp(key) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (w *Writer[K]) branchUpdateChild(pgno uint32, childIdx int, newChild uint32) error {
	var zero K
	kw := zero.width()
	var off int
	if childIdx == 0 {
		off = PageHeaderSize
	} else {
		off = PageHeaderSize + 4 + (childIdx-1)*(kw+4) + kw
	}
	putU32(w.store.pageMut(pgno), off, newChild)
	return nil
}

func (w *Writer[K]) branchAbsorbSplit(pgno uint32, childIdx int, left uint32, sep K, right uint32) (*branchSplit[K], error) {
	var zero K
	kw := zero.width()
	page := w.store.page(pgno)
	count := int(decodeHeader(page).entryCount)
	bmax := branchMax(w.keyWidth)
	if count < bmax {
		pageMut := w.store.pageMut(pgno)
		insOff := PageHeaderSize + 4 + childIdx*(kw+4)
		endOff := PageHeaderSize + 4 + count*(kw+4)
		copy(pageMut[insOff+kw+4:endOff+kw+4], pageMut[insOff:endOff])
		sep.writeLE(pageMut[insOff : insOff+kw])
		putU32(pageMut, insOff+kw, right)
		var leftOff int
		if childIdx == 0 {
			leftOff = PageHeaderSize
		} else {
			leftOff = PageHeaderSize + 4 + (childIdx-1)*(kw+4) + kw
		}
		putU32(pageMut, leftOff, left)
		writeHeader(pageMut, PageTypeBranch, uint16(count+1), pgno)
		return nil, nil
	}
	// Branch split: copy to stack buffer, redistribute into two pages.
	var src [PageSize]byte
	copy(src[:], w.store.page(pgno))
	total := count + 1
	mid := total / 2

	if err := w.writeBranchSplit(pgno, src[:], count, childIdx, left, sep, right, 0, mid); err != nil {
		return nil, err
	}
	rightPgno, err := w.allocPage()
	if err != nil {
		return nil, err
	}
	// Right page starts at mid+1: the separator at combined index `mid`
	// is promoted up and must NOT remain in either child page.
	if err := w.writeBranchSplit(rightPgno, src[:], count, childIdx, left, sep, right, mid+1, total-mid-1); err != nil {
		return nil, err
	}

	// Promoted separator = sep at index `mid` in the combined array.
	var promoted K
	if mid == childIdx {
		promoted = sep
	} else {
		oldI := mid
		if mid > childIdx {
			oldI = mid - 1
		}
		off := PageHeaderSize + 4 + oldI*(kw+4)
		promoted = zero.readLE(src[off : off+kw])
	}
	return &branchSplit[K]{sep: promoted, right: rightPgno}, nil
}

// readOldChildSrc reads old_child[i] from a source branch page.
func readOldChildSrc(src []byte, i, kw int) uint32 {
	if i == 0 {
		return u32le(src, PageHeaderSize)
	}
	return u32le(src, PageHeaderSize+4+(i-1)*(kw+4)+kw)
}

func (w *Writer[K]) writeBranchSplit(pgno uint32, src []byte, oldCount, insertIdx int,
	insLeft uint32, insSep K, insRight uint32, startIdx, sepCount int) error {
	var zero K
	kw := zero.width()
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeBranch, uint16(sepCount), pgno)

	var firstChild uint32
	switch {
	case startIdx == 0 && insertIdx == 0:
		firstChild = insLeft
	case startIdx == 0:
		firstChild = u32le(src, PageHeaderSize)
	case startIdx == insertIdx:
		firstChild = insLeft
	case startIdx == insertIdx+1:
		firstChild = insRight
	case startIdx < insertIdx:
		firstChild = readOldChildSrc(src, startIdx, kw)
	default: // startIdx > insertIdx+1: shifted by the insertion
		firstChild = readOldChildSrc(src, startIdx-1, kw)
	}
	putU32(page, PageHeaderSize, firstChild)

	for outI := 0; outI < sepCount; outI++ {
		absI := startIdx + outI
		var s K
		var c uint32
		if absI == insertIdx {
			s, c = insSep, insRight
		} else {
			oldI := absI
			if absI > insertIdx {
				oldI = absI - 1
			}
			off := PageHeaderSize + 4 + oldI*(kw+4)
			s = zero.readLE(src[off : off+kw])
			c = u32le(src, off+kw)
		}
		outOff := PageHeaderSize + 4 + outI*(kw+4)
		s.writeLE(page[outOff : outOff+kw])
		putU32(page, outOff+kw, c)
	}
	return nil
}

func (w *Writer[K]) writeBranchNew(pgno uint32, left uint32, sep K, right uint32) error {
	var zero K
	kw := zero.width()
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeBranch, 1, pgno)
	putU32(page, PageHeaderSize, left)
	sep.writeLE(page[PageHeaderSize+4 : PageHeaderSize+4+kw])
	putU32(page, PageHeaderSize+4+kw, right)
	return nil
}

// --- meta ---

func (w *Writer[K]) writeMetaPage(pgno uint32, txnID uint64, root uint32, height uint32,
	recordCount uint64, totalPages uint32, updatedUnix uint64) error {
	var zero K
	m := meta{
		pgno:           pgno,
		versionMinor:   VersionMinor,
		metaSize:       MetaSize,
		pageSize:       PageSize,
		checksumAlgo:   ChecksumAlgoCRC32C,
		flags:          zero.version().Flag(),
		keyWidth:       uint8(zero.width()),
		scopeMode:      w.scopeMode,
		recordSize:     uint32(recordSizeBytes(zero.width())),
		createdUnix:    w.createdUnix,
		rootPgno:       root,
		treeHeight:     height,
		totalPages:     uint64(totalPages),
		recordCount:    recordCount,
		txnID:          txnID,
		updatedUnix:    updatedUnix,
		scopeTableRoot: w.scopeRoot(),
		freeListHead:   w.freeListHead,
	}
	m.encodeInto(w.store.pageMut(pgno))
	return nil
}

// --- scan ---

func (w *Writer[K]) Scan(f func(from, to K, scopeID uint32)) error {
	if w.pendingRoot == 0 {
		return nil
	}
	return w.scanNode(w.pendingRoot, f)
}

// --- scope table operations (mode 2 only) ---

func (w *Writer[K]) ScopeIntern(bitmap []byte) (uint32, error) {
	if w.scopeRegistry == nil {
		return 0, fmt.Errorf("scope_intern requires scope_mode == 2")
	}
	if len(bitmap) > MaxBitmapWidth {
		return 0, fmt.Errorf("bitmap exceeds %d bytes (2048 feeds)", MaxBitmapWidth)
	}
	id, wasNew := w.scopeRegistry.Intern(bitmap, w.store.committedBytes())
	if wasNew {
		w.scopeDirty = true
	}
	return id, nil
}

func (w *Writer[K]) ScopeResolve(scopeID uint32) []byte {
	if w.scopeRegistry == nil {
		return nil
	}
	return w.scopeRegistry.Resolve(scopeID, w.store.committedBytes())
}

// ScopePageCount returns the number of pages in the current scope-table tree
// (mode 2 only). Returns 0 when there is no scope table. Used by tests/audits
// to verify that scope pages are reused across commits rather than accumulated.
func (w *Writer[K]) ScopePageCount() int {
	root := w.scopeRoot()
	if root == 0 {
		return 0
	}
	var pages []uint32
	w.collectScopePageNumbers(root, 0, &pages)
	return len(pages)
}

func (w *Writer[K]) ScopeBitmapSetFeed(scopeID uint32, feedBit uint32) (uint32, error) {
	w.scopeDirty = true
	if w.scopeRegistry == nil {
		return 0, fmt.Errorf("requires scope_mode == 2")
	}
	return w.scopeRegistry.BitmapSetFeed(scopeID, feedBit, w.store.committedBytes()), nil
}

func (w *Writer[K]) ScopeBitmapClearFeed(scopeID uint32, feedBit uint32) (uint32, error) {
	w.scopeDirty = true
	if w.scopeRegistry == nil {
		return 0, fmt.Errorf("requires scope_mode == 2")
	}
	return w.scopeRegistry.BitmapClearFeed(scopeID, feedBit, w.store.committedBytes()), nil
}

func (w *Writer[K]) scanNode(pgno uint32, f func(K, K, uint32)) error {
	var zero K
	kw := zero.width()
	page := w.store.page(pgno)
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		lv := newLeafView(page, int(h.entryCount), kw)
		for i := 0; i < lv.len(); i++ {
			f(zero.readLE(lv.recordFrom(i)), zero.readLE(lv.recordTo(i)), lv.recordScopeID(i))
		}
		return nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		for j := 0; j < bv.childCount(); j++ {
			if err := w.scanNode(bv.child(j), f); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

// --- feed-bit range API ---

// FeedAddRange adds feed `feedBit` to all IP ranges overlapping [from, to].
// Existing feed bits are preserved. Adjacent same-scope ranges are merged.
func (w *Writer[K]) FeedAddRange(from, to K, feedBit uint32) error {
	if err := w.check(); err != nil {
		return err
	}
	if from.cmp(to) > 0 {
		return fmt.Errorf("from > to")
	}

	overlaps, err := w.collectOverlapping(from, to)
	if err != nil {
		return err
	}
	for _, o := range overlaps {
		if _, err := w.Delete(o.from, o.to); err != nil {
			return err
		}
	}

	var cursor K
	cursor = from

	for _, o := range overlaps {
		if o.from.cmp(cursor) > 0 && cursor.cmp(to) <= 0 {
			var gapTo K
			if o.from.cmp(to) <= 0 {
				gt, ok := o.from.checkedDec()
				if !ok {
					gt = o.from
				}
				gapTo = gt
			} else {
				gapTo = to
			}
			if gapTo.cmp(cursor) >= 0 {
				newScope, err := w.freshFeedScope(feedBit)
				if err != nil {
					return err
				}
				if err := w.cowInsert(cursor, gapTo, newScope); err != nil {
					return err
				}
				w.pendingRecordCount++
			}
		}

		if o.from.cmp(from) < 0 {
			trimEnd, ok := from.checkedDec()
			if !ok {
				trimEnd = from
			}
			if err := w.cowInsert(o.from, trimEnd, o.scope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}

		var innerFrom K
		if o.from.cmp(from) > 0 {
			innerFrom = o.from
		} else {
			innerFrom = from
		}
		var innerTo K
		if o.to.cmp(to) < 0 {
			innerTo = o.to
		} else {
			innerTo = to
		}
		newScope, err := w.applyFeedBit(o.scope, feedBit)
		if err != nil {
			return err
		}
		if err := w.cowInsert(innerFrom, innerTo, newScope); err != nil {
			return err
		}
		w.pendingRecordCount++

		if o.to.cmp(to) > 0 {
			trimStart, ok := to.checkedInc()
			if !ok {
				trimStart = to
			}
			if err := w.cowInsert(trimStart, o.to, o.scope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}

		next, ok := o.to.checkedInc()
		if !ok {
			next = o.to
		}
		cursor = next
	}

	if cursor.cmp(to) <= 0 {
		newScope, err := w.freshFeedScope(feedBit)
		if err != nil {
			return err
		}
		if err := w.cowInsert(cursor, to, newScope); err != nil {
			return err
		}
		w.pendingRecordCount++
	}

	return nil
}

// FeedRemoveRange removes feed `feedBit` from all IP ranges overlapping
// [from, to]. Records whose scope becomes empty are removed.
func (w *Writer[K]) FeedRemoveRange(from, to K, feedBit uint32) error {
	if err := w.check(); err != nil {
		return err
	}
	if from.cmp(to) > 0 {
		return fmt.Errorf("from > to")
	}

	overlaps, err := w.collectOverlapping(from, to)
	if err != nil {
		return err
	}

	for _, o := range overlaps {
		if _, err := w.Delete(o.from, o.to); err != nil {
			return err
		}
	}

	for _, o := range overlaps {
		if o.from.cmp(from) < 0 {
			trimEnd, ok := from.checkedDec()
			if !ok {
				trimEnd = from
			}
			if err := w.cowInsert(o.from, trimEnd, o.scope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}
		if o.to.cmp(to) > 0 {
			trimStart, ok := to.checkedInc()
			if !ok {
				trimStart = to
			}
			if err := w.cowInsert(trimStart, o.to, o.scope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}

		var innerFrom K
		if o.from.cmp(from) > 0 {
			innerFrom = o.from
		} else {
			innerFrom = from
		}
		var innerTo K
		if o.to.cmp(to) < 0 {
			innerTo = o.to
		} else {
			innerTo = to
		}
		newScope, err := w.clearFeedBit(o.scope, feedBit)
		if err != nil {
			return err
		}
		if newScope != 0 {
			if err := w.cowInsert(innerFrom, innerTo, newScope); err != nil {
				return err
			}
			w.pendingRecordCount++
		}
	}

	return nil
}

type overlapRecord[K ipKey[K]] struct {
	from  K
	to    K
	scope uint32
}

// collectOverlapping gathers all pending records overlapping [from, to].
func (w *Writer[K]) collectOverlapping(from, to K) ([]overlapRecord[K], error) {
	if w.pendingRoot == 0 {
		return nil, nil
	}
	var result []overlapRecord[K]
	if err := w.collectOverlappingNode(w.pendingRoot, from, to, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (w *Writer[K]) collectOverlappingNode(pgno uint32, from, to K, out *[]overlapRecord[K]) error {
	var zero K
	kw := zero.width()
	page := w.store.page(pgno)
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		lv := newLeafView(page, int(h.entryCount), kw)
		for i := 0; i < lv.len(); i++ {
			rf := zero.readLE(lv.recordFrom(i))
			if rf.cmp(to) > 0 {
				return nil
			}
			rt := zero.readLE(lv.recordTo(i))
			if rt.cmp(from) >= 0 {
				*out = append(*out, overlapRecord[K]{
					from:  rf,
					to:    rt,
					scope: lv.recordScopeID(i),
				})
			}
		}
		return nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		start := branchFindChild(&bv, from)
		for j := start; j < bv.childCount(); j++ {
			if j > 0 {
				sepVal := zero.readLE(bv.sep(j - 1))
				if sepVal.cmp(to) > 0 {
					break
				}
			}
			if err := w.collectOverlappingNode(bv.child(j), from, to, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

// freshFeedScope creates a scope_id with only the given feed bit set.
func (w *Writer[K]) freshFeedScope(feedBit uint32) (uint32, error) {
	switch w.scopeMode {
	case ScopeModeBitmap:
		if feedBit >= 32 {
			return 0, fmt.Errorf("feed bit %d exceeds 32-bit bitmap mode", feedBit)
		}
		return 1 << feedBit, nil
	case ScopeModeIndirect:
		if w.scopeRegistry == nil {
			return 0, fmt.Errorf("requires scope_mode == 2")
		}
		bm := make([]byte, feedBit/8+1)
		bm[feedBit/8] |= 1 << (feedBit % 8)
		id, wasNew := w.scopeRegistry.Intern(bm, w.store.committedBytes())
		if wasNew {
			w.scopeDirty = true
		}
		return id, nil
	default:
		return 0, fmt.Errorf("feed operations require scope_mode 1 or 2")
	}
}

// applyFeedBit OR's a feed bit into a scope_id, returning the new scope_id.
func (w *Writer[K]) applyFeedBit(scopeID, feedBit uint32) (uint32, error) {
	// F3 fix: indirect mode interns a new bitmap here — mark the registry
	// dirty so the scope table is rebuilt at commit.
	if w.scopeMode == ScopeModeIndirect {
		w.scopeDirty = true
	}
	switch w.scopeMode {
	case ScopeModeBitmap:
		if feedBit >= 32 {
			return 0, fmt.Errorf("feed_bit >= 32 in bitmap mode")
		}
		return scopeID | (1 << feedBit), nil
	case ScopeModeIndirect:
		if w.scopeRegistry == nil {
			return 0, fmt.Errorf("requires scope_mode == 2")
		}
		return w.scopeRegistry.BitmapSetFeed(scopeID, feedBit, w.store.committedBytes()), nil
	default:
		return 0, fmt.Errorf("feed operations require scope_mode 1 or 2")
	}
}

// clearFeedBit clears a feed bit from a scope_id, returning the new scope_id
// (0 if the bitmap becomes empty).
func (w *Writer[K]) clearFeedBit(scopeID, feedBit uint32) (uint32, error) {
	// F3 fix: indirect mode interns a new bitmap here — mark the registry
	// dirty so the scope table is rebuilt at commit.
	if w.scopeMode == ScopeModeIndirect {
		w.scopeDirty = true
	}
	switch w.scopeMode {
	case ScopeModeBitmap:
		if feedBit >= 32 {
			return 0, fmt.Errorf("feed_bit >= 32 in bitmap mode")
		}
		return scopeID & ^(1 << feedBit), nil
	case ScopeModeIndirect:
		if w.scopeRegistry == nil {
			return 0, fmt.Errorf("requires scope_mode == 2")
		}
		return w.scopeRegistry.BitmapClearFeed(scopeID, feedBit, w.store.committedBytes()), nil
	default:
		return 0, fmt.Errorf("feed operations require scope_mode 1 or 2")
	}
}
