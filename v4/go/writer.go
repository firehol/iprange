package iprangedb

import "bytes"

// The v4 writer: copy-on-write B+tree mutation with a double-meta atomic commit
// (§6, §7, §8).
//
// This is the in-memory writer core: it owns the whole file image as a growable buffer
// and emulates the pread/pwrite model the OS layer (os.go) uses against a real file —
// every mutated node is copied to a freshly allocated page (the old page is
// freed-by-this-txn, D7) up to a new root, and Commit writes the new state into the
// inactive meta and flips it (§6.3). A crash leaves the file as old-or-new, never torn
// (the active meta only points at durable pages).
//
// Implements Create, range Set / Delete (§8) over a COW B+tree (leaf/branch split + root
// growth on insert; sibling-merge + root-collapse on delete), Scan, and Commit. Set /
// Delete compose a disjoint-insert primitive with boundary trimming and same-scope
// coalescing.

// ownedRecord is a record materialized from a leaf for the duration of a COW op.
type ownedRecord[K ipKey[K]] struct {
	from  K
	to    K
	scope []byte
}

// Writer is a single-writer COW B+tree over an in-memory file image. It is generic over
// the key width (fixed per file). Commit makes the accumulated mutations durable+atomic.
type Writer[K ipKey[K]] struct {
	image           []byte
	activeMeta      uint32 // physical meta page (0 or 1) currently active
	rootPgno        uint32
	treeHeight      uint32
	recordCount     uint64
	scopeWidth      int
	recordSize      int
	leafMax         int
	branchMax       int
	createdUnixtime uint64
	txnID           uint64
	free            []uint32 // pages reusable by the current txn
	freedThisTxn    []uint32 // pages freed since the last commit (reusable next txn, D7)
	dirty           []uint32 // data pages written this txn (for the OS layer's pwrite set)
	committedPages  int      // on-disk page count as of the last commit (grown-region boundary)
}

// CreateV4 creates a fresh empty IPv4 DB (see createWriter).
func CreateV4(scopeWidth uint8, createdUnixtime uint64) *Writer[Ipv4Key] {
	return createWriter[Ipv4Key](scopeWidth, createdUnixtime)
}

// CreateV6 creates a fresh empty IPv6 DB (see createWriter).
func CreateV6(scopeWidth uint8, createdUnixtime uint64) *Writer[Ipv6Key] {
	return createWriter[Ipv6Key](scopeWidth, createdUnixtime)
}

// createWriter creates a fresh empty DB: two meta pages (META-A active txn_id=1, META-B
// txn_id=0), an empty tree (root_pgno=0). scope_width is fixed for the file's lifetime
// (§4). Mutations are not durable until Commit.
func createWriter[K ipKey[K]](scopeWidth uint8, createdUnixtime uint64) *Writer[K] {
	var zero K
	recSize := recordSize(uint8(zero.width()), scopeWidth)
	w := &Writer[K]{
		image:           make([]byte, 2*pageSize),
		activeMeta:      0,
		rootPgno:        0,
		treeHeight:      0,
		recordCount:     0,
		scopeWidth:      int(scopeWidth),
		recordSize:      int(recSize),
		leafMax:         leafMax(recSize),
		branchMax:       branchMax(uint8(zero.width())),
		createdUnixtime: createdUnixtime,
		txnID:           1,
		committedPages:  2, // the two metas, both written by create
	}
	// META-A active (txn 1), META-B (txn 0); identical static identity.
	w.writeMeta(0, 1, createdUnixtime)
	w.writeMeta(1, 0, createdUnixtime)
	return w
}

// OpenImageV4 opens an existing committed IPv4 image for mutation (see openImage).
func OpenImageV4(image []byte) (*Writer[Ipv4Key], error) {
	return openImage[Ipv4Key](image)
}

// OpenImageV6 opens an existing committed IPv6 image for mutation (see openImage).
func OpenImageV6(image []byte) (*Writer[Ipv6Key], error) {
	return openImage[Ipv6Key](image)
}

// openImage opens an existing committed image for mutation (§6.2): fully validate it with
// the reader's §9 checks (the writer also reads untrusted bytes), then derive the
// in-memory free set (§7) by walking the reachable tree. The image MUST have no trailing
// pages beyond total_pages (the OS layer truncates those before calling).
func openImage[K ipKey[K]](image []byte) (*Writer[K], error) {
	r, err := Open(image)
	if err != nil {
		return nil, err
	}
	var zero K
	if zero.version() != r.version {
		return nil, errInvalidInput("writer family mismatch")
	}
	m := r.meta
	// The writer implements exactly versionMinor 0. It MUST refuse to mutate a file of a
	// minor it does not fully implement (§5.1): committing would write a minor-0 meta and
	// drop the newer minor's trailing fields. (The reader still accepts such files
	// read-only — forward-compat.)
	if m.versionMinor != versionMinor {
		return nil, errInvalidInput("writer cannot mutate a newer version_minor file")
	}
	// Reclaim trailing pages beyond total_pages (a crashed growth, §6.4): the committed
	// total_pages is authoritative and reachable pages are all below it.
	image = image[:int(m.totalPages)*pageSize]
	recSize := recordSize(uint8(zero.width()), m.scopeWidth)
	w := &Writer[K]{
		image:           image,
		activeMeta:      m.pgno,
		rootPgno:        m.rootPgno,
		treeHeight:      m.treeHeight,
		recordCount:     m.recordCount,
		scopeWidth:      int(m.scopeWidth),
		recordSize:      int(recSize),
		leafMax:         leafMax(recSize),
		branchMax:       branchMax(uint8(zero.width())),
		createdUnixtime: m.createdUnixtime,
		txnID:           m.txnID,
		committedPages:  int(m.totalPages), // committed on-disk page count
	}
	w.free = w.deriveFreeSet()
	return w, nil
}

// Set makes every address in [from,to] equal scope, unconditionally (§8, D11): clears the
// range, then inserts [from,to,scope] coalescing with byte-equal-scope adjacent
// neighbours. O(k log n) (k = records overlapping the range).
func (w *Writer[K]) Set(from, to K, scope []byte) error {
	if to.cmp(from) < 0 {
		return errInvalidInput("set from > to")
	}
	if len(scope) != w.scopeWidth {
		return errInvalidInput("set scope width mismatch")
	}
	if err := w.deleteRange(from, to); err != nil {
		return err
	}
	nf, nt := from, to
	// Coalesce with a same-scope neighbour ending at from-1.
	if fm1, ok := from.checkedDec(); ok {
		if lf, lt, ls, found := w.lookupCovering(fm1); found {
			if lt.cmp(fm1) == 0 && bytes.Equal(ls, scope) {
				if _, err := w.treeDelete(lf); err != nil {
					return err
				}
				nf = lf
			}
		}
	}
	// Coalesce with a same-scope neighbour starting at to+1.
	if tp1, ok := to.checkedInc(); ok {
		if rf, rt, rs, found := w.lookupCovering(tp1); found {
			if rf.cmp(tp1) == 0 && bytes.Equal(rs, scope) {
				if _, err := w.treeDelete(rf); err != nil {
					return err
				}
				nt = rt
			}
		}
	}
	return w.insert(nf, nt, scope)
}

// Delete makes [from,to] absent (§8). It splits a straddling record, trims boundaries,
// removes records fully inside; a wholly-absent range is a no-op. O(k log n).
func (w *Writer[K]) Delete(from, to K) error {
	if to.cmp(from) < 0 {
		return errInvalidInput("delete from > to")
	}
	return w.deleteRange(from, to)
}

// Scan calls f(from, to, scope) per record over the (pending) tree, in key order.
func (w *Writer[K]) Scan(f func(from, to K, scope []byte)) {
	if w.rootPgno != 0 {
		w.scanNode(w.rootPgno, 1, f)
	}
}

// Commit writes the new state into the inactive meta and flips it (§6.3), reclaiming
// pages freed by this txn (D7) and clearing the dirty set. After this the image is a valid
// v4 file whose active meta is the new tree. It errors only if txn_id is exhausted.
func (w *Writer[K]) Commit(updatedUnixtime uint64) error {
	if _, err := w.commitMeta(updatedUnixtime); err != nil {
		return err
	}
	w.dirty = w.dirty[:0]
	return nil
}

// Image returns the current image bytes (a valid v4 file after a Commit). The returned
// slice ALIASES the writer's internal buffer; callers MUST NOT modify it (a later
// mutation/commit will read and overwrite these bytes). Copy it if you need to retain it.
func (w *Writer[K]) Image() []byte { return w.image }

// RecordCount returns the number of records in the (pending) tree.
func (w *Writer[K]) RecordCount() uint64 { return w.recordCount }

// commitMeta writes the new meta into the inactive page and flips it (the in-memory half
// of the commit, §6.3); it returns the page number (0 or 1) just written so the OS layer
// can pwrite exactly that page as Barrier 2. Reclaims this txn's freed pages (D7). It
// refuses to commit if txn_id would reach math.MaxUint64 (§6.3; unreachable in practice).
func (w *Writer[K]) commitMeta(updatedUnixtime uint64) (uint32, error) {
	if w.txnID == maxUint64 {
		return 0, errInvalidInput("txn_id exhausted")
	}
	inactive := 1 - w.activeMeta
	w.txnID++
	w.writeMeta(inactive, w.txnID, updatedUnixtime)
	w.activeMeta = inactive
	w.free = append(w.free, w.freedThisTxn...)
	w.freedThisTxn = w.freedThisTxn[:0]
	// The file is now this long on disk after the OS layer's truncate; the next txn's
	// grown region starts here.
	w.committedPages = len(w.image) / pageSize
	return inactive, nil
}

// takeDirty returns the data pages the OS layer must pwrite at Barrier 1. A page written
// then freed again within the same txn is an orphan the new meta never references, so
// pwriting it is wasted I/O — except if it lies in the file's newly-grown region
// (pgno >= committedPages): the OS layer truncates the file to the new length, so every
// offset up to it MUST be backed by real bytes or the mmap reader rejects a sparse hole
// (§10). We therefore drop a freed page only when it already existed on disk before this
// txn (pgno < committedPages). Within one txn each page is written at most once.
func (w *Writer[K]) takeDirty() []uint32 {
	d := w.dirty
	w.dirty = nil
	if len(w.freedThisTxn) == 0 {
		return d
	}
	boundary := uint32(w.committedPages)
	out := d[:0]
	for _, p := range d {
		if p >= boundary || !containsU32(w.freedThisTxn, p) {
			out = append(out, p)
		}
	}
	return out
}

// containsU32 reports whether s contains v.
func containsU32(s []uint32, v uint32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// insert inserts a disjoint record [from, to] = scope (the caller guarantees it does not
// overlap an existing range and from is unique). The COW building block for Set / Delete.
func (w *Writer[K]) insert(from, to K, scope []byte) error {
	if to.cmp(from) < 0 {
		return errInvalidInput("insert from > to")
	}
	if len(scope) != w.scopeWidth {
		return errInvalidInput("insert scope width mismatch")
	}
	rec := ownedRecord[K]{from: from, to: to, scope: cloneBytes(scope)}
	if w.rootPgno == 0 {
		p, err := w.writeLeaf([]ownedRecord[K]{rec})
		if err != nil {
			return err
		}
		w.rootPgno = p
		w.treeHeight = 1
	} else {
		newRoot, sep, right, split, err := w.cowInsert(w.rootPgno, 1, rec)
		if err != nil {
			return err
		}
		if !split {
			w.rootPgno = newRoot
		} else {
			if w.treeHeight >= treeHeightMax {
				return errInvalidInput("tree would exceed TREE_HEIGHT_MAX")
			}
			root, err := w.writeBranch([]K{sep}, []uint32{newRoot, right})
			if err != nil {
				return err
			}
			w.rootPgno = root
			w.treeHeight++
		}
	}
	w.recordCount++
	return nil
}

// --- COW internals ---

// cowInsert is the recursive COW insert. It returns the new subtree pgno and, on
// overflow, a (separator, right_pgno) split (split=true) for the parent to absorb.
func (w *Writer[K]) cowInsert(pgno, depth uint32, rec ownedRecord[K]) (newPgno uint32, sep K, right uint32, split bool, err error) {
	if depth == w.treeHeight {
		recs := w.readLeaf(pgno)
		w.freePage(pgno)
		pos := partitionIdx(len(recs), func(i int) bool { return recs[i].from.cmp(rec.from) < 0 })
		recs = insertRecord(recs, pos, rec)
		return w.emitLeaf(recs)
	}
	seps, children := w.readBranch(pgno)
	w.freePage(pgno)
	i := partitionIdx(len(seps), func(j int) bool { return seps[j].cmp(rec.from) <= 0 })
	nc, csep, cright, csplit, cerr := w.cowInsert(children[i], depth+1, rec)
	if cerr != nil {
		return 0, sep, 0, false, cerr
	}
	children[i] = nc
	if csplit {
		seps = insertKey(seps, i, csep)
		children = insertU32(children, i+1, cright)
	}
	return w.emitBranch(seps, children)
}

// emitLeaf writes records as one leaf, or splits into two if over leaf_max.
func (w *Writer[K]) emitLeaf(records []ownedRecord[K]) (newPgno uint32, sep K, right uint32, split bool, err error) {
	if len(records) <= w.leafMax {
		p, e := w.writeLeaf(records)
		return p, sep, 0, false, e
	}
	mid := len(records) / 2
	lp, e := w.writeLeaf(records[:mid])
	if e != nil {
		return 0, sep, 0, false, e
	}
	rp, e := w.writeLeaf(records[mid:])
	if e != nil {
		return 0, sep, 0, false, e
	}
	return lp, records[mid].from, rp, true, nil
}

// emitBranch writes a branch, or splits into two (promoting the middle separator) if over
// branch_max.
func (w *Writer[K]) emitBranch(seps []K, children []uint32) (newPgno uint32, sep K, right uint32, split bool, err error) {
	if len(seps) <= w.branchMax {
		p, e := w.writeBranch(seps, children)
		return p, sep, 0, false, e
	}
	mid := len(seps) / 2
	lp, e := w.writeBranch(seps[:mid], children[:mid+1])
	if e != nil {
		return 0, sep, 0, false, e
	}
	rp, e := w.writeBranch(seps[mid+1:], children[mid+1:])
	if e != nil {
		return 0, sep, 0, false, e
	}
	return lp, seps[mid], rp, true, nil
}

// --- page I/O over the in-memory image ---

func (w *Writer[K]) readLeaf(pgno uint32) []ownedRecord[K] {
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	leaf := newLeafView[K](page, count, w.recordSize)
	out := make([]ownedRecord[K], count)
	for i := 0; i < count; i++ {
		rec := leaf.record(i)
		out[i] = ownedRecord[K]{from: rec.from(), to: rec.to(), scope: cloneBytes(rec.scope())}
	}
	return out
}

func (w *Writer[K]) readBranch(pgno uint32) ([]K, []uint32) {
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	b := newBranchView[K](page, count)
	seps := make([]K, count)
	for i := 0; i < count; i++ {
		seps[i] = b.sep(i)
	}
	children := make([]uint32, count+1)
	for j := 0; j <= count; j++ {
		children[j] = b.child(j)
	}
	return seps, children
}

func (w *Writer[K]) writeLeaf(records []ownedRecord[K]) (uint32, error) {
	pgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.dirty = append(w.dirty, pgno)
	base := int(pgno) * pageSize
	page := w.image[base : base+pageSize]
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeLeaf, uint16(len(records)), pgno)
	for i, r := range records {
		off := pageHeaderSize + i*w.recordSize
		recordWrite[K](page[off:off+w.recordSize], r.from, r.to, r.scope)
	}
	finalizeChecksum(page)
	return pgno, nil
}

func (w *Writer[K]) writeBranch(seps []K, children []uint32) (uint32, error) {
	pgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.dirty = append(w.dirty, pgno)
	base := int(pgno) * pageSize
	page := w.image[base : base+pageSize]
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeBranch, uint16(len(seps)), pgno)
	var zero K
	width := zero.width()
	// child[0] at +16, then (sep[i], child[i+1]) pairs.
	le.PutUint32(page[pageHeaderSize:], children[0])
	for i := range seps {
		sepOff := pageHeaderSize + 4 + i*(width+4)
		seps[i].writeLE(page[sepOff : sepOff+width])
		le.PutUint32(page[sepOff+width:], children[i+1])
	}
	finalizeChecksum(page)
	return pgno, nil
}

func (w *Writer[K]) writeMeta(pgno uint32, txnID, updatedUnixtime uint64) {
	var zero K
	m := meta{
		pgno:            pgno,
		versionMinor:    0,
		metaSize:        metaSize,
		pageSize:        pageSize,
		checksumAlgo:    checksumAlgoCRC32C,
		flags:           zero.version().flag(),
		keyWidth:        uint8(zero.width()),
		scopeWidth:      uint8(w.scopeWidth),
		recordSize:      uint32(w.recordSize),
		createdUnixtime: w.createdUnixtime,
		rootPgno:        w.rootPgno,
		treeHeight:      w.treeHeight,
		totalPages:      uint64(len(w.image) / pageSize),
		recordCount:     w.recordCount,
		txnID:           txnID,
		updatedUnixtime: updatedUnixtime,
	}
	base := int(pgno) * pageSize
	m.encodeInto(w.image[base : base+pageSize])
}

func (w *Writer[K]) page(pgno uint32) []byte {
	base := int(pgno) * pageSize
	return w.image[base : base+pageSize]
}

// allocPage allocates a page: reuse a freed page (from a prior txn) or grow the image. It
// refuses to grow past the 2^32-page limit (§6.4) rather than wrap the u32 pgno.
func (w *Writer[K]) allocPage() (uint32, error) {
	if n := len(w.free); n > 0 {
		p := w.free[n-1]
		w.free = w.free[:n-1]
		return p, nil
	}
	// uint64 so the `1 << 32` constant and the comparison are valid on 32-bit targets
	// (where `int` is 32-bit and the constant would overflow it).
	if uint64(len(w.image)/pageSize) >= 1<<32 {
		return 0, errInvalidInput("file would exceed the 2^32-page limit")
	}
	p := uint32(len(w.image) / pageSize)
	w.image = append(w.image, make([]byte, pageSize)...)
	return p, nil
}

// freePage marks a page freed by the current txn (reusable only after this txn commits,
// D7).
func (w *Writer[K]) freePage(pgno uint32) {
	w.freedThisTxn = append(w.freedThisTxn, pgno)
}

// deriveFreeSet derives the free set (§7): pages in [2, total_pages) not reachable from
// the root. The image is already validated, so the walk is bounded and safe.
func (w *Writer[K]) deriveFreeSet() []uint32 {
	total := len(w.image) / pageSize
	used := make([]bool, total)
	used[0] = true // META-A
	used[1] = true // META-B
	if w.rootPgno != 0 {
		w.markReachable(w.rootPgno, 1, used)
	}
	var free []uint32
	for p := 2; p < total; p++ {
		if !used[p] {
			free = append(free, uint32(p))
		}
	}
	return free
}

func (w *Writer[K]) markReachable(pgno, depth uint32, used []bool) {
	used[pgno] = true
	if depth < w.treeHeight {
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		b := newBranchView[K](page, count)
		for j := 0; j < b.childCount(); j++ {
			w.markReachable(b.child(j), depth+1, used)
		}
	}
}

// --- range mutation internals ---

// deleteRange removes everything overlapping [from, to], re-inserting the parts of
// straddling records that fall outside (left [rf, from-1], right [to+1, rt]).
func (w *Writer[K]) deleteRange(from, to K) error {
	for {
		rf, rt, scope, found := w.anyOverlap(from, to)
		if !found {
			return nil
		}
		if _, err := w.treeDelete(rf); err != nil {
			return err
		}
		if rf.cmp(from) < 0 {
			fm1, _ := from.checkedDec() // from > rf >= family_min
			if err := w.insert(rf, fm1, scope); err != nil {
				return err
			}
		}
		if rt.cmp(to) > 0 {
			tp1, _ := to.checkedInc() // to < rt <= family_max
			if err := w.insert(tp1, rt, scope); err != nil {
				return err
			}
		}
	}
}

// anyOverlap returns any record overlapping [from, to]. It is the covering record of
// from (single-leaf), else the successor of from if it starts within the range.
func (w *Writer[K]) anyOverlap(from, to K) (rf, rt K, scope []byte, found bool) {
	if cf, ct, cs, ok := w.lookupCovering(from); ok {
		return cf, ct, cs, true
	}
	if gf, gt, gs, ok := w.lookupGE(from); ok {
		if gf.cmp(to) <= 0 {
			return gf, gt, gs, true
		}
	}
	return rf, rt, nil, false
}

// treeDelete deletes the record whose from == key (rebalancing on underflow; collapsing
// the root). It returns whether a record was removed.
func (w *Writer[K]) treeDelete(key K) (bool, error) {
	if w.rootPgno == 0 || !w.containsFrom(key) {
		return false, nil
	}
	if w.treeHeight == 1 {
		recs := w.readLeaf(w.rootPgno)
		w.freePage(w.rootPgno)
		pos := partitionIdx(len(recs), func(i int) bool { return recs[i].from.cmp(key) < 0 })
		recs = removeRecord(recs, pos)
		if len(recs) == 0 {
			w.rootPgno = 0
			w.treeHeight = 0
		} else {
			p, err := w.writeLeaf(recs)
			if err != nil {
				return false, err
			}
			w.rootPgno = p
		}
	} else {
		newRoot, _, err := w.cowDelete(w.rootPgno, 1, key)
		if err != nil {
			return false, err
		}
		w.rootPgno = newRoot
		// Collapse a root branch that fell to a single child (height shrinks).
		for w.treeHeight > 1 {
			page := w.page(w.rootPgno)
			sepCount := int(decodePageHeader(page).entryCount)
			if sepCount >= 1 {
				break
			}
			only := newBranchView[K](page, 0).child(0)
			w.freePage(w.rootPgno)
			w.rootPgno = only
			w.treeHeight--
		}
	}
	w.recordCount--
	return true, nil
}

// cowDelete is the recursive COW delete. It returns (new_pgno, underflowed) — underflow =
// an empty leaf or a single-child branch, which the parent (or treeDelete at the root)
// repairs.
func (w *Writer[K]) cowDelete(pgno, depth uint32, key K) (uint32, bool, error) {
	if depth == w.treeHeight {
		recs := w.readLeaf(pgno)
		w.freePage(pgno)
		pos := partitionIdx(len(recs), func(i int) bool { return recs[i].from.cmp(key) < 0 })
		if pos < len(recs) && recs[pos].from.cmp(key) == 0 {
			recs = removeRecord(recs, pos)
		}
		p, err := w.writeLeaf(recs)
		if err != nil {
			return 0, false, err
		}
		return p, len(recs) == 0, nil
	}
	seps, children := w.readBranch(pgno)
	w.freePage(pgno)
	i := partitionIdx(len(seps), func(j int) bool { return seps[j].cmp(key) <= 0 })
	nc, childUF, err := w.cowDelete(children[i], depth+1, key)
	if err != nil {
		return 0, false, err
	}
	children[i] = nc
	if childUF {
		seps, children, err = w.rebalance(seps, children, i, depth+1)
		if err != nil {
			return 0, false, err
		}
	}
	p, err := w.writeBranch(seps, children)
	if err != nil {
		return 0, false, err
	}
	return p, len(children) < 2, nil
}

// rebalance merges an underflowed children[i] with an adjacent sibling and re-emits (1 or
// 2 nodes), patching seps/children. Balance-preserving.
func (w *Writer[K]) rebalance(seps []K, children []uint32, i int, childDepth uint32) ([]K, []uint32, error) {
	var l, r, sepIdx int
	if i > 0 {
		l, r, sepIdx = i-1, i, i-1
	} else {
		l, r, sepIdx = i, i+1, i
	}
	var p, p2 uint32
	var newsep K
	var split bool
	var err error
	if childDepth == w.treeHeight {
		recs := w.readLeaf(children[l])
		rr := w.readLeaf(children[r])
		recs = append(recs, rr...)
		w.freePage(children[l])
		w.freePage(children[r])
		p, newsep, p2, split, err = w.emitLeaf(recs)
	} else {
		s1, c1 := w.readBranch(children[l])
		s2, c2 := w.readBranch(children[r])
		w.freePage(children[l])
		w.freePage(children[r])
		s1 = append(s1, seps[sepIdx])
		s1 = append(s1, s2...)
		c1 = append(c1, c2...)
		p, newsep, p2, split, err = w.emitBranch(s1, c1)
	}
	if err != nil {
		return nil, nil, err
	}
	if !split {
		children[l] = p
		children = removeU32(children, r)
		seps = removeKey(seps, sepIdx)
	} else {
		children[l] = p
		children[r] = p2
		seps[sepIdx] = newsep
	}
	return seps, children, nil
}

// --- read-path queries over the pending tree ---

func (w *Writer[K]) scanNode(pgno, depth uint32, f func(from, to K, scope []byte)) {
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	if depth == w.treeHeight {
		leaf := newLeafView[K](page, count, w.recordSize)
		for i := 0; i < count; i++ {
			rec := leaf.record(i)
			f(rec.from(), rec.to(), rec.scope())
		}
		return
	}
	branch := newBranchView[K](page, count)
	for j := 0; j < branch.childCount(); j++ {
		w.scanNode(branch.child(j), depth+1, f)
	}
}

// descendToLeaf returns the leaf page that would contain key (0 if empty).
func (w *Writer[K]) descendToLeaf(key K) uint32 {
	if w.rootPgno == 0 {
		return 0
	}
	pgno := w.rootPgno
	depth := uint32(1)
	for depth < w.treeHeight {
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		b := newBranchView[K](page, count)
		i := partitionIdx(count, func(j int) bool { return b.sep(j).cmp(key) <= 0 })
		pgno = b.child(i)
		depth++
	}
	return pgno
}

// lookupCovering returns the record covering key (from <= key <= to). Single-leaf.
func (w *Writer[K]) lookupCovering(key K) (from, to K, scope []byte, found bool) {
	pgno := w.descendToLeaf(key)
	if pgno == 0 {
		return from, to, nil, false
	}
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	leaf := newLeafView[K](page, count, w.recordSize)
	pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(key) <= 0 })
	if pos == 0 {
		return from, to, nil, false
	}
	rec := leaf.record(pos - 1)
	if key.cmp(rec.to()) <= 0 {
		return rec.from(), rec.to(), cloneBytes(rec.scope()), true
	}
	return from, to, nil, false
}

// containsFrom reports whether a record with exactly from == key exists.
func (w *Writer[K]) containsFrom(key K) bool {
	pgno := w.descendToLeaf(key)
	if pgno == 0 {
		return false
	}
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	leaf := newLeafView[K](page, count, w.recordSize)
	pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(key) < 0 })
	return pos < count && leaf.record(pos).from().cmp(key) == 0
}

// lookupGE returns the record with the smallest from >= key (successor), via a cursor
// that walks to the next leaf when needed (no sibling pointers, D3).
func (w *Writer[K]) lookupGE(key K) (from, to K, scope []byte, found bool) {
	if w.rootPgno == 0 {
		return from, to, nil, false
	}
	type frame struct {
		pgno uint32
		ci   int
	}
	var stack []frame
	pgno := w.rootPgno
	depth := uint32(1)
	for depth < w.treeHeight {
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		b := newBranchView[K](page, count)
		i := partitionIdx(count, func(j int) bool { return b.sep(j).cmp(key) <= 0 })
		stack = append(stack, frame{pgno: pgno, ci: i})
		pgno = b.child(i)
		depth++
	}
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	leaf := newLeafView[K](page, count, w.recordSize)
	pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(key) < 0 })
	if pos < count {
		rec := leaf.record(pos)
		return rec.from(), rec.to(), cloneBytes(rec.scope()), true
	}
	// No record >= key in this leaf; ascend to the nearest next child, descend left.
	for len(stack) > 0 {
		fr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		bpage := w.page(fr.pgno)
		bcount := int(decodePageHeader(bpage).entryCount)
		b := newBranchView[K](bpage, bcount)
		if fr.ci+1 < b.childCount() {
			p := b.child(fr.ci + 1)
			// Leftmost descent, bounded by treeHeightMax so a malformed tree can never
			// loop (the writer only operates on validated trees, so the cap is never
			// reached in practice — it is a defensive termination guarantee).
			for d := uint32(0); d < treeHeightMax; d++ {
				pp := w.page(p)
				if decodePageHeader(pp).pageType == pageTypeLeaf {
					break
				}
				cnt := int(decodePageHeader(pp).entryCount)
				p = newBranchView[K](pp, cnt).child(0)
			}
			lpage := w.page(p)
			lcount := int(decodePageHeader(lpage).entryCount)
			rec := newLeafView[K](lpage, lcount, w.recordSize).record(0)
			return rec.from(), rec.to(), cloneBytes(rec.scope()), true
		}
	}
	return from, to, nil, false
}

// --- slice helpers (mirror the Rust Vec insert/remove) ---

func insertRecord[K ipKey[K]](s []ownedRecord[K], i int, v ownedRecord[K]) []ownedRecord[K] {
	s = append(s, ownedRecord[K]{})
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeRecord[K ipKey[K]](s []ownedRecord[K], i int) []ownedRecord[K] {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}

func insertKey[K ipKey[K]](s []K, i int, v K) []K {
	var zero K
	s = append(s, zero)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeKey[K ipKey[K]](s []K, i int) []K {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}

func insertU32(s []uint32, i int, v uint32) []uint32 {
	s = append(s, 0)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeU32(s []uint32, i int) []uint32 {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}

// partitionIdx returns the number of indices in [0, count) for which pred (monotone
// true-then-false) holds — i.e. the first index where it is false.
func partitionIdx(count int, pred func(int) bool) int {
	lo, hi := 0, count
	for lo < hi {
		mid := lo + (hi-lo)/2
		if pred(mid) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// cloneBytes returns a copy of b (an empty, non-nil slice for a zero-width scope).
func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
