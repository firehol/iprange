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
	// recyclePos is the cursor into freedThisTxn for same-transaction recycling.
	recyclePos int
	// canRecycle controls whether COW victims may be recycled in-place. Safe
	// only when no readers are active (oldestReaderTxnID == MaxUint64).
	canRecycle bool

	scopeRegistry      *ScopeRegistry
	scopeTableRootCache uint32
	scopeDirty         bool
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
		canRecycle:          true,
		scopeRegistry:       scopeRegForMode(scopeMode),
		scopeTableRootCache: 0, scopeDirty: false,
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
		canRecycle:          true,
		scopeRegistry:       nil,
		scopeTableRootCache: active.scopeTableRoot, scopeDirty: false,
	}
	// Load scope table for mode 2.
	if active.scopeMode == ScopeModeIndirect {
		entries, _ := readAllScopes(w.store.committedBytes(), active.scopeTableRoot)
		if entries != nil {
			w.scopeRegistry = ScopeRegistryFromEntries(entries)
		} else {
			w.scopeRegistry = NewScopeRegistry()
		}
	}
	w.LoadFreeList(^uint64(0))
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

	// Rebuild scope table (mode 2) only if the registry changed.
	if w.scopeDirty {
		// Collect old scope tree pages.
		var oldScopePages []uint32
		if w.scopeTableRootCache != 0 {
			w.collectScopePageNumbers(w.scopeTableRootCache, 0, &oldScopePages)
		}
		// When readers are active, old scope pages are committed-region pages
		// that readers may reference via MAP_SHARED. Push them to freedThisTxn
		// for the free-list chain and let buildScopeTree allocate fresh.
		var freePool []uint32
		if w.canRecycle {
			freePool = oldScopePages
		} else {
			w.freedThisTxn = append(w.freedThisTxn, oldScopePages...)
		}
		w.scopeTableRootCache = 0
		if w.scopeRegistry != nil && !w.scopeRegistry.IsEmpty() {
			var allocated []uint32
			root, err := buildScopeTree(w.store, w.scopeRegistry.Entries(), &allocated, &freePool)
			if err != nil {
				return err
			}
			// Register scope pages in privatePages for CRC finalization.
			for _, pgno := range allocated {
				w.privatePages.insert(pgno)
			}
			w.scopeTableRootCache = root
		}
		w.scopeDirty = false
	}

	// Finalize CRCs on all private pages.
	for _, pgno := range w.privatePages.iter() {
		if pgno >= 2 {
			finalizeChecksum(w.store.pageMut(pgno))
		}
	}

	// Build and write the persistent free-list.
	newTxnIDVal := w.committedTxnID

	// Collect entries to persist:
	// - Old entries not consumed this transaction
	// - Pages freed this transaction (COW victims + old chain pages + old scope pages)
	var oldEntries []FreeEntry
	if w.freeListHead != 0 {
		oldEntries, _ = ReadChain(w.store, w.freeListHead)
	}
	oldChainPages := ReadChainPageNumbers(w.store, w.freeListHead)

	// Determine consumed pages (popped from freePages during the transaction).
	consumed := make(map[uint32]bool)
	upper := w.freePos
	if upper > len(w.freePages) {
		upper = len(w.freePages)
	}
	for i := 0; i < upper; i++ {
		consumed[w.freePages[i]] = true
	}

	var entriesToWrite []FreeEntry
	for _, e := range oldEntries {
		if !consumed[e.Pgno] {
			entriesToWrite = append(entriesToWrite, e)
		}
	}
	// Old chain pages are being replaced.
	w.freedThisTxn = append(w.freedThisTxn, oldChainPages...)
	// Add pages freed this transaction (excluding recycled pages, which are
	// now live tree pages and must NOT appear in the free-list).
	for _, pgno := range w.freedThisTxn[w.recyclePos:] {
		entriesToWrite = append(entriesToWrite, FreeEntry{Pgno: pgno, FreedTxnID: newTxnIDVal})
	}

	// Rule 5: compute trailing free pages that can be truncated. This must
	// happen BEFORE writing the chain so truncated pages don't appear as stale
	// entries in the chain.
	preTruncateTotal := w.store.totalPages()
	var trailing uint32
	if oldestReaderTxnID == ^uint64(0) {
		freePgnos := make([]uint32, len(entriesToWrite))
		for i, e := range entriesToWrite {
			freePgnos[i] = e.Pgno
		}
		trailing = TrailingFreeCount(freePgnos, preTruncateTotal)
	}
	newTotal := preTruncateTotal - trailing

	// Remove trailing pages from entriesToWrite so the chain doesn't reference
	// pages that will be truncated.
	if trailing > 0 {
		kept := entriesToWrite[:0]
		for _, e := range entriesToWrite {
			if e.Pgno < newTotal {
				kept = append(kept, e)
			}
		}
		entriesToWrite = kept
	}

	// Allocate chain pages: prefer reusing a freed page as the first chain page
	// (Rule 5: no unbounded file growth). Only safe when no readers are active.
	var selfSupplyPage uint32
	hasSelfSupply := false
	if w.canRecycle && len(entriesToWrite) >= 2 {
		maxIdx := 0
		for i, e := range entriesToWrite {
			if e.Pgno > entriesToWrite[maxIdx].Pgno {
				maxIdx = i
			}
		}
		selfSupplyPage = entriesToWrite[maxIdx].Pgno
		entriesToWrite[maxIdx] = entriesToWrite[len(entriesToWrite)-1]
		entriesToWrite = entriesToWrite[:len(entriesToWrite)-1]
		hasSelfSupply = true
	}

	sort.Slice(entriesToWrite, func(i, j int) bool {
		return entriesToWrite[i].FreedTxnID < entriesToWrite[j].FreedTxnID
	})
	needed := ChainPageCount(entriesToWrite)

	chainPages := make([]uint32, 0, needed)
	if hasSelfSupply {
		chainPages = append(chainPages, selfSupplyPage)
	}
	for len(chainPages) < needed {
		pgno, err := w.store.allocPage()
		if err != nil {
			return err
		}
		chainPages = append(chainPages, pgno)
	}

	head, err := WriteChain(w.store, entriesToWrite, chainPages)
	if err != nil {
		return err
	}
	w.freeListHead = head

	// Truncate trailing free pages now that the chain no longer references them.
	var total uint32
	if trailing > 0 {
		if err := w.store.truncate(newTotal); err != nil {
			return err
		}
		total = newTotal
	} else {
		total = w.store.totalPages()
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
	w.LoadFreeList(oldestReaderTxnID)
	return nil
}

func (w *Writer[K]) RecordCount() uint64 {
	return w.pendingRecordCount
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

// allocPage pops a page from the free pool, recycles a same-txn COW victim, or
// extends the file. Every allocated page is marked private.
func (w *Writer[K]) allocPage() (uint32, error) {
	if w.freePos < len(w.freePages) {
		pgno := w.freePages[w.freePos]
		w.freePos++
		w.privatePages.insert(pgno)
		return pgno, nil
	}
	if w.canRecycle && w.recyclePos < len(w.freedThisTxn) {
		pgno := w.freedThisTxn[w.recyclePos]
		w.recyclePos++
		w.privatePages.insert(pgno)
		// Clear stale data: recycled pages may contain old branch headers with
		// child pointers that create cycles during tree traversal.
		page := w.store.pageMut(pgno)
		for i := range page {
			page[i] = 0
		}
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
// oldestReaderTxnID: pages freed in txn < this are reclaimable.
// ^uint64(0) means no readers (all pages reclaimable).
func (w *Writer[K]) LoadFreeList(oldestReaderTxnID uint64) {
	w.canRecycle = oldestReaderTxnID == ^uint64(0)
	if w.freeListHead == 0 {
		w.freePages = w.freePages[:0]
		w.freePos = 0
		return
	}
	entries, _ := ReadChain(w.store, w.freeListHead)
	w.freePages = Reclaimable(entries, oldestReaderTxnID)
	sort.Slice(w.freePages, func(i, j int) bool { return w.freePages[i] < w.freePages[j] })
	w.freePos = 0
}

// resetTxn clears the private-pages bitset for the next transaction.
// registerScopePages walks the scope tree and adds all pages to privatePages.
func (w *Writer[K]) registerScopePages(root uint32) {
	var pages []uint32
	w.collectScopePageNumbers(root, 0, &pages)
	for _, pgno := range pages {
		w.privatePages.insert(pgno)
	}
}

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
	w.recyclePos = 0
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
		overlap, err := w.scanFirstOverlap(from, to)
		if err != nil {
			return err
		}
		if overlap == nil {
			return nil
		}
		o := *overlap
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
}

type overlapInfo[K ipKey[K]] struct {
	recFrom  K
	recTo    K
	recScope uint32
	leafPgno uint32
	recIdx   int
}

func (w *Writer[K]) scanFirstOverlap(from, to K) (*overlapInfo[K], error) {
	if w.pendingRoot == 0 {
		return nil, nil
	}
	return w.scanOverlapNode(w.pendingRoot, from, to)
}

func (w *Writer[K]) scanOverlapNode(pgno uint32, from, to K) (*overlapInfo[K], error) {
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
				return nil, nil
			}
			rt := zero.readLE(lv.recordTo(i))
			if rt.cmp(from) >= 0 {
				return &overlapInfo[K]{
					recFrom: rf, recTo: rt, recScope: lv.recordScopeID(i),
					leafPgno: pgno, recIdx: i,
				}, nil
			}
		}
		return nil, nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		start := branchFindChild[K](&bv, from)
		for j := start; j < bv.childCount(); j++ {
			r, err := w.scanOverlapNode(bv.child(j), from, to)
			if err != nil {
				return nil, err
			}
			if r != nil {
				return r, nil
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected page type %d", h.pageType)
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
		scopeTableRoot: w.scopeTableRootCache,
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
	id, wasNew := w.scopeRegistry.Intern(bitmap)
	if wasNew {
		w.scopeDirty = true
	}
	return id, nil
}

func (w *Writer[K]) ScopeResolve(scopeID uint32) []byte {
	if w.scopeRegistry == nil {
		return nil
	}
	return w.scopeRegistry.Resolve(scopeID)
}

func (w *Writer[K]) ScopeBitmapSetFeed(scopeID uint32, feedBit uint32) (uint32, error) {
	if w.scopeRegistry == nil {
		return 0, fmt.Errorf("requires scope_mode == 2")
	}
	return w.scopeRegistry.BitmapSetFeed(scopeID, feedBit), nil
}

func (w *Writer[K]) ScopeBitmapClearFeed(scopeID uint32, feedBit uint32) (uint32, error) {
	if w.scopeRegistry == nil {
		return 0, fmt.Errorf("requires scope_mode == 2")
	}
	return w.scopeRegistry.BitmapClearFeed(scopeID, feedBit), nil
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
		for j := 0; j < bv.childCount(); j++ {
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
		return 1 << feedBit, nil
	case ScopeModeIndirect:
		if w.scopeRegistry == nil {
			return 0, fmt.Errorf("requires scope_mode == 2")
		}
		bm := make([]byte, feedBit/8+1)
		bm[feedBit/8] |= 1 << (feedBit % 8)
		id, wasNew := w.scopeRegistry.Intern(bm)
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
		return w.scopeRegistry.BitmapSetFeed(scopeID, feedBit), nil
	default:
		return 0, fmt.Errorf("feed operations require scope_mode 1 or 2")
	}
}

// clearFeedBit clears a feed bit from a scope_id, returning the new scope_id
// (0 if the bitmap becomes empty).
func (w *Writer[K]) clearFeedBit(scopeID, feedBit uint32) (uint32, error) {
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
		return w.scopeRegistry.BitmapClearFeed(scopeID, feedBit), nil
	default:
		return 0, fmt.Errorf("feed operations require scope_mode 1 or 2")
	}
}
