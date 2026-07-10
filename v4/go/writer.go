package iprangedb

import "fmt"

// Changed indicates whether a delete actually removed something.
type Changed int

const (
	Unchanged Changed = 0
	Changed_  Changed = 1
)

// Writer is a single-writer COW B+tree over a page store. All fields are
// fixed-size — zero heap allocation in the hot path (Rule 1).
type Writer[K ipKey[K]] struct {
	store pageStore

	// Format identity
	keyWidth     uint8
	scopeMode    uint8
	createdUnix  uint64

	// Active meta page (0 or 1)
	activeMeta uint32

	// Committed state (from active meta at open / last commit)
	committedRoot       uint32
	committedHeight     uint32
	committedPages      uint32
	committedRecordCount uint64
	committedTxnID      uint64
	freeListHead        uint32

	// Pending state (this txn's working copy)
	pendingRoot         uint32
	pendingHeight       uint32
	pendingRecordCount  uint64

	poisoned      bool
	// Page reclamation: txn-free tracking (fixed-size, Rule 1: zero heap).
	txnFreeBuf    [64]uint32
	txnFreeCount  int
	txnFreeChain  uint32
	reuseBuf         [64]uint32
	reuseCount       int
	safeReclaimTxnID uint64
}

// --- construction ---

// Create creates a fresh empty DB.
func Create[K ipKey[K]](scopeMode uint8, createdUnix uint64) (*Writer[K], error) {
	var zero K
	kw := uint8(zero.width())
	store := newVecPageStore(make([]byte, 2*PageSize))
	w := &Writer[K]{
		store:              store,
		keyWidth:           kw,
		scopeMode:          scopeMode,
		createdUnix:        createdUnix,
		activeMeta:         0,
		committedPages:     2,
		committedTxnID:     0,
		txnFreeBuf:         [64]uint32{},
		reuseBuf:           [64]uint32{},
		safeReclaimTxnID:   0,
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

// Open opens from an existing committed page store.
func openWriter[K ipKey[K]](store pageStore) (*Writer[K], error) {
	metaA := decodeMeta(store.page(0))
	metaB := decodeMeta(store.page(1))
	var active meta
	var activeNo uint32
	if metaA.txnID >= metaB.txnID {
		active = metaA
		activeNo = 0
	} else {
		active = metaB
		activeNo = 1
	}
	var zero K
	if active.keyWidth != uint8(zero.width()) {
		return nil, fmt.Errorf("key_width mismatch")
	}
	rs := uint32(recordSizeBytes(zero.width()))
	if active.recordSize != rs {
		return nil, fmt.Errorf("record_size mismatch")
	}
	w := &Writer[K]{
		store:              store,
		keyWidth:           active.keyWidth,
		scopeMode:          active.scopeMode,
		createdUnix:        active.createdUnix,
		activeMeta:         activeNo,
		committedRoot:      active.rootPgno,
		committedHeight:    active.treeHeight,
		committedPages:     uint32(active.totalPages),
		committedRecordCount: active.recordCount,
		committedTxnID:     active.txnID,
		freeListHead:       active.freeListHead,
		pendingRoot:        active.rootPgno,
		pendingHeight:      active.treeHeight,
		pendingRecordCount: active.recordCount,
		txnFreeBuf:         [64]uint32{},
		reuseBuf:           [64]uint32{},
		safeReclaimTxnID:   0,
	}
	w.loadFreeList()
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

func (w *Writer[K]) Commit(updatedUnix uint64) error {
	if err := w.check(); err != nil {
		return err
	}
	// Flush remaining freed pages.
	if w.txnFreeCount > 0 {
		w.buildFreeListLinked()
	}
	w.freeListHead = w.txnFreeChain
	total := w.store.totalPages()
	for pgno := w.committedPages; pgno < total; pgno++ {
		finalizeChecksum(w.store.pageMut(pgno))
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
	w.committedRoot = w.pendingRoot
	w.committedHeight = w.pendingHeight
	w.committedRecordCount = w.pendingRecordCount
	w.committedPages = total
	w.store.setCommittedPages(total)
	w.txnFreeCount = 0
	w.txnFreeChain = 0
	w.loadFreeList()
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

// SetSafeReclaimTxnID sets the oldest reader's txn_id for page reclamation.
// 0 = no active readers.
func (w *Writer[K]) SetSafeReclaimTxnID(txnID uint64) {
	w.safeReclaimTxnID = txnID
}

func (w *Writer[K]) cowPage(pgno uint32) (uint32, error) {
	if pgno >= w.committedPages {
		return pgno, nil
	}
	newPgno, err := w.allocOrReuse()
	if err != nil {
		return 0, err
	}
	w.store.copyPage(pgno, newPgno)
	w.trackFreed(pgno)
	return newPgno, nil
}

func (w *Writer[K]) allocOrReuse() (uint32, error) {
	// 1. Intra-transaction reuse.
	if w.txnFreeCount > 0 {
		w.txnFreeCount--
		return w.txnFreeBuf[w.txnFreeCount], nil
	}
	// 2. Cross-transaction reuse.
	if w.reuseCount > 0 {
		w.reuseCount--
		return w.reuseBuf[w.reuseCount], nil
	}
	// 3. Growth.
	return w.store.allocPage()
}

func (w *Writer[K]) trackFreed(pgno uint32) {
	if w.txnFreeCount < len(w.txnFreeBuf) {
		w.txnFreeBuf[w.txnFreeCount] = pgno
		w.txnFreeCount++
	} else {
		w.buildFreeListLinked()
		w.txnFreeBuf[0] = pgno
		w.txnFreeCount = 1
	}
}

func (w *Writer[K]) buildFreeListLinked() {
	if w.txnFreeCount == 0 {
		return
	}
	freedIn := w.committedTxnID + 1
	metaPgno, err := w.allocOrReuse()
	if err != nil {
		return
	}
	page := w.store.pageMut(metaPgno)
	for i := range page {
		page[i] = 0
	}
	putU32(page, TxnFreeNext, w.txnFreeChain)
	putU32(page, TxnFreeCount, uint32(w.txnFreeCount))
	for i := 0; i < w.txnFreeCount; i++ {
		putU32(page, TxnFreeArray+i*4, w.txnFreeBuf[i])
	}
	putU64(page, TxnFreeFreedIn, freedIn)
	writeHeader(page, PageTypeTxnFree, 0, metaPgno)
	w.txnFreeChain = metaPgno
	w.txnFreeCount = 0
}

func (w *Writer[K]) loadFreeList() {
	w.reuseCount = 0
	metaPgno := w.freeListHead
	guard := uint32(0)
	for metaPgno != 0 && guard < TreeHeightMax {
		guard++
		if uint64(metaPgno) >= uint64(w.store.totalPages()) {
			break
		}
		page := w.store.page(metaPgno)
		h := decodeHeader(page)
		if h.pageType != PageTypeTxnFree {
			break
		}
		nextMeta := u32le(page, TxnFreeNext)
		count := int(u32le(page, TxnFreeCount))
		freedIn := u64le(page, TxnFreeFreedIn)
		safe := w.safeReclaimTxnID == 0 || freedIn < w.safeReclaimTxnID
		if safe {
			for i := 0; i < count; i++ {
				if w.reuseCount >= len(w.reuseBuf) {
					w.freeListHead = metaPgno
					return
				}
				freedPgno := u32le(page, TxnFreeArray+i*4)
				w.reuseBuf[w.reuseCount] = freedPgno
				w.reuseCount++
			}
			// Recycle the TXN_FREE metadata page itself.
			if w.reuseCount < len(w.reuseBuf) {
				w.reuseBuf[w.reuseCount] = metaPgno
				w.reuseCount++
			} else {
				w.freeListHead = metaPgno
				return
			}
		} else {
			w.freeListHead = metaPgno
			return
		}
		metaPgno = nextMeta
	}
	w.freeListHead = 0
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
		leaf, err := w.allocOrReuse()
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
		newRoot, err := w.allocOrReuse()
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
	right, err := w.allocOrReuse()
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
	rs := recordSizeBytes(kw)
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeLeaf, 1, pgno)
	from.writeLE(page[PageHeaderSize : PageHeaderSize+kw])
	to.writeLE(page[PageHeaderSize+kw : PageHeaderSize+2*kw])
	putU32(page, PageHeaderSize+2*kw, scopeID)
	_ = rs
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
	recFrom   K
	recTo     K
	recScope  uint32
	leafPgno  uint32
	recIdx    int
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
	rightPgno, err := w.allocOrReuse()
	if err != nil {
		return nil, err
	}
	if err := w.writeBranchSplit(rightPgno, src[:], count, childIdx, left, sep, right, mid, total-mid); err != nil {
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

func (w *Writer[K]) writeBranchSplit(pgno uint32, src []byte, oldCount, insertIdx int,
	insLeft uint32, insSep K, insRight uint32, startIdx, sepCount int) error {
	var zero K
	kw := zero.width()
	page := w.store.pageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeBranch, uint16(sepCount), pgno)

	// Write child[0].
	var firstChild uint32
	if startIdx == 0 {
		if insertIdx == 0 {
			firstChild = insLeft
		} else {
			firstChild = u32le(src, PageHeaderSize)
		}
	} else if insertIdx == startIdx {
		firstChild = insRight
	} else {
		oldI := startIdx - 1
		firstChild = u32le(src, PageHeaderSize+4+oldI*(kw+4)+kw)
	}
	putU32(page, PageHeaderSize, firstChild)

	// Write each separator + following child.
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
		scopeTableRoot: 0,
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
