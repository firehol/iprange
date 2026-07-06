package iprangedb

import (
	"bytes"
	"unicode/utf8"
)

// ScopeEntry is one entry returned by Writer.ScopeList: a scope's id and its name (a copy).
type ScopeEntry struct {
	ID   uint32
	Name []byte
}

// MetaEntry is one KV entry returned by Writer.MetaList: (key, type, value). value is the
// whole reassembled value (inline or overflow-spanning); type == 0 is validated text. All
// three slices are owned copies.
type MetaEntry struct {
	Key   []byte
	Type  uint32
	Value []byte
}

// The v4 writer: copy-on-write B+tree mutation with a double-meta atomic commit
// (§6, §7, §8).
//
// This is the in-memory writer core: it owns the whole file image as a growable buffer
// and emulates the pread/pwrite model the OS layer (os.go) uses against a real file. The
// first write to a committed page in a transaction copies it to a transaction-private
// page; later writes to that private page mutate it in place. Commit writes the new state
// into the inactive meta and flips it (§6.3). A crash leaves the file as old-or-new,
// never torn (the active meta only points at durable pages).
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
	store           pageStore
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
	scopeTableRoot  uint32     // v4.1 metadata (§C.1); 0 = no metadata (file stays v4.0)
	scopes          []scopeRec // in-memory registry (sorted by id), bulk-rebuilt at commit
	nextScopeID     uint32     // monotonic; never reuses a dropped id (§C.2)
	scopeDirty      bool       // registry changed since the last commit → rebuild needed
	// kvDirty holds each dirty target's full entry set (sorted by key), loaded lazily from
	// its committed kv_root on first mutation and bulk-rebuilt at commit (§C.4). target =
	// scope_id (0 = FILE). Absent = clean (read straight from disk).
	kvDirty        map[uint32][]kvEntry
	free           []uint32            // pages reusable by the current txn
	freedThisTxn   []uint32            // pages freed since the last commit (reusable next txn, D7)
	dirty          []uint32            // data pages written this txn (for the OS layer's pwrite set)
	privatePages   map[uint32]struct{} // txn-private data pages safe to mutate in place
	committedPages int                 // on-disk page count as of the last commit (grown-region boundary)
	// poisoned is set if the commit rebuild phase fails: page alloc/free is irreversible, so
	// the in-memory allocator/registry is then indeterminate. The on-disk meta is unwritten
	// (the file is the last committed valid state), so the writer must be discarded and
	// reopened. Every mutating op + Commit refuses once poisoned.
	poisoned bool
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
		store:           newVecPageStore(make([]byte, 2*pageSize)),
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
		scopeTableRoot:  0,
		nextScopeID:     1, // 0 is reserved for the FILE target (§C.2)
		kvDirty:         make(map[uint32][]kvEntry),
		privatePages:    make(map[uint32]struct{}),
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
	// The writer implements up to versionMinor 1 (the v4.1 metadata system). It MUST refuse
	// to mutate a file of a newer minor it does not fully implement (§5.1/§C.6): committing
	// would drop the newer minor's trailing fields. (The reader still accepts such files
	// read-only — forward-compat.) A v4.0 file (minor 0) is opened as-is and stays v4.0 until
	// the first metadata write upgrades it (§C.6).
	if m.versionMinor > versionMinorMetadata {
		return nil, errInvalidInput("writer cannot mutate a newer version_minor file")
	}
	// Reclaim trailing pages beyond total_pages (a crashed growth, §6.4): the committed
	// total_pages is authoritative and reachable pages are all below it.
	image = image[:int(m.totalPages)*pageSize]
	store := newVecPageStore(image)
	// Load the committed scope registry into memory (validated by Open above).
	scopes, err := loadAllScopes(store.committedBytes(), m.scopeTableRoot)
	if err != nil {
		return nil, err
	}
	// Next id = max existing id + 1 (saturating), or 1 when empty. Monotonic, never reused.
	nextScopeID := uint32(1)
	if n := len(scopes); n > 0 {
		maxID := scopes[n-1].id // scopes are sorted ascending by id
		if maxID == maxUint32 {
			nextScopeID = maxUint32
		} else {
			nextScopeID = maxID + 1
		}
	}
	recSize := recordSize(uint8(zero.width()), m.scopeWidth)
	w := &Writer[K]{
		store:           store,
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
		scopeTableRoot:  m.scopeTableRoot,
		scopes:          scopes,
		nextScopeID:     nextScopeID,
		kvDirty:         make(map[uint32][]kvEntry),
		privatePages:    make(map[uint32]struct{}),
		committedPages:  int(m.totalPages), // committed on-disk page count
	}
	w.free = w.deriveFreeSet()
	return w, nil
}

// openWithStore opens an existing image from a page store. The store must already contain
// the committed state (e.g., from an mmap). Validates the tree and derives the free set.
func openWithStore[K ipKey[K]](store pageStore) (*Writer[K], error) {
	bytes := store.committedBytes()
	r, err := Open(bytes)
	if err != nil {
		return nil, err
	}
	var zero K
	if zero.version() != r.version {
		return nil, errInvalidInput("writer family mismatch")
	}
	m := r.meta
	if m.versionMinor > versionMinorMetadata {
		return nil, errInvalidInput("writer cannot mutate a newer version_minor file")
	}
	scopes, err := loadAllScopes(bytes, m.scopeTableRoot)
	if err != nil {
		return nil, err
	}
	nextScopeID := uint32(1)
	if n := len(scopes); n > 0 {
		maxID := scopes[n-1].id
		if maxID == maxUint32 {
			nextScopeID = maxUint32
		} else {
			nextScopeID = maxID + 1
		}
	}
	recSize := recordSize(uint8(zero.width()), m.scopeWidth)
	w := &Writer[K]{
		store:           store,
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
		scopeTableRoot:  m.scopeTableRoot,
		scopes:          scopes,
		nextScopeID:     nextScopeID,
		kvDirty:         make(map[uint32][]kvEntry),
		privatePages:    make(map[uint32]struct{}),
		committedPages:  int(m.totalPages),
	}
	w.free = w.deriveFreeSet()
	return w, nil
}

// Set makes every address in [from,to] equal scope, unconditionally (§8, D11): clears the
// range, then inserts [from,to,scope] coalescing with byte-equal-scope adjacent
// neighbours. O(k log n) (k = records overlapping the range).
func (w *Writer[K]) Set(from, to K, scope []byte) error {
	if err := w.ensureUsable(); err != nil {
		return err
	}
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
	if err := w.ensureUsable(); err != nil {
		return err
	}
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
// NOTE: Commit is only valid for vecPageStore-backed writers. mmapPageStore-backed writers
// must use FileWriter.Commit (which handles the two-fsync protocol).
func (w *Writer[K]) Commit(updatedUnixtime uint64) error {
	if _, ok := w.store.(*vecPageStore); !ok {
		return errInvalidInput("Writer.Commit is only valid for vecPageStore; use FileWriter.Commit for mmap-backed writers")
	}
	if _, err := w.commitMeta(updatedUnixtime); err != nil {
		return err
	}
	w.dirty = w.dirty[:0]
	return nil
}

// Image returns the current image bytes (a valid v4 file after a Commit). For vecPageStore:
// returns the slice directly (zero-cost). For mmapPageStore: returns a copy of committedBytes()
// (the mmap is PROT_READ — returning it directly would SIGSEGV on modification).
func (w *Writer[K]) Image() []byte {
	if img, ok := w.store.(*vecPageStore); ok {
		return img.image
	}
	// mmapPageStore: return a copy to avoid SIGSEGV (the mmap is PROT_READ).
	bytes := w.store.committedBytes()
	out := make([]byte, len(bytes))
	copy(out, bytes)
	return out
}

// RecordCount returns the number of records in the (pending) tree.
func (w *Writer[K]) RecordCount() uint64 { return w.recordCount }

// --- v4.1 scope registry (§C.2) ---

// ScopeDefine defines a new scope, returning its scope_id (>= 1; 0 is reserved for FILE,
// never returned). version starts at 0, type at 0, no KV. scope_ids are monotonic and never
// reused after a drop (§C.2). name is UTF-8, <= 256 bytes (need not be unique). The first
// metadata write upgrades the file to v4.1 at commit.
func (w *Writer[K]) ScopeDefine(name []byte) (uint32, error) {
	if err := w.ensureUsable(); err != nil {
		return 0, err
	}
	if len(name) > scopeNameMax {
		return 0, errInvalidInput("scope name > 256 bytes")
	}
	if !utf8.Valid(name) {
		return 0, errInvalidInput("scope name not valid UTF-8")
	}
	if w.nextScopeID == fileScopeID {
		return 0, errInvalidInput("scope_id space exhausted")
	}
	id := w.nextScopeID
	if id == maxUint32 {
		// Reserve the exhausted state: the next define wraps to 0 (FILE) and is rejected.
		w.nextScopeID = fileScopeID
	} else {
		w.nextScopeID = id + 1
	}
	// Monotonic ids ⇒ appending at the end keeps scopes sorted by id.
	w.scopes = append(w.scopes, scopeRec{
		id:      id,
		version: 0,
		typ:     0,
		name:    cloneBytes(name),
		kvRoot:  0,
	})
	w.scopeDirty = true
	return id, nil
}

// ScopeDrop drops a scope: it removes the scope's metadata (header + KV) only — IP records
// carrying it are NOT touched (caller policy, §C.2). ScopeDrop(0) (FILE) is rejected. It
// returns whether the scope existed.
func (w *Writer[K]) ScopeDrop(scopeID uint32) (bool, error) {
	if err := w.ensureUsable(); err != nil {
		return false, err
	}
	if scopeID == fileScopeID {
		return false, errInvalidInput("cannot drop the FILE scope (0)")
	}
	i, ok := w.scopePos(scopeID)
	if !ok {
		return false, nil
	}
	// Free the dropped scope's KV tree + overflow pages (§C.2 / §C.5). Its committed kv_root
	// is authoritative; any buffered KV for this target is discarded too (the scope is gone).
	w.freeKVTree(w.scopes[i].kvRoot)
	delete(w.kvDirty, scopeID)
	w.scopes = append(w.scopes[:i], w.scopes[i+1:]...)
	w.scopeDirty = true
	return true, nil
}

// ScopeName returns the scope's name (UTF-8 bytes) and whether it exists. A missing scope
// returns (nil, false) — mirroring Rust's Ok(None), per Go convention (like LookupV4).
func (w *Writer[K]) ScopeName(scopeID uint32) ([]byte, bool) {
	if i, ok := w.scopePos(scopeID); ok {
		return cloneBytes(w.scopes[i].name), true
	}
	return nil, false
}

// ScopeList returns all defined scopes as (id, name), ascending by scope_id. The FILE target
// (scope_id 0) is a dataset-metadata target, not a defined scope, so it is excluded (§C.2).
func (w *Writer[K]) ScopeList() []ScopeEntry {
	out := make([]ScopeEntry, 0, len(w.scopes))
	for i := range w.scopes {
		if w.scopes[i].id == fileScopeID {
			continue
		}
		out = append(out, ScopeEntry{ID: w.scopes[i].id, Name: cloneBytes(w.scopes[i].name)})
	}
	return out
}

// ScopeVersion returns the scope's version and whether it exists.
func (w *Writer[K]) ScopeVersion(scopeID uint32) (uint64, bool) {
	if i, ok := w.scopePos(scopeID); ok {
		return w.scopes[i].version, true
	}
	return 0, false
}

// ScopeSetVersion sets the scope's version (caller-bumped, §C.3). It returns whether the
// scope existed.
func (w *Writer[K]) ScopeSetVersion(scopeID uint32, version uint64) (bool, error) {
	if err := w.ensureUsable(); err != nil {
		return false, err
	}
	i, ok := w.scopePos(scopeID)
	if !ok {
		return false, nil
	}
	w.scopes[i].version = version
	w.scopeDirty = true
	return true, nil
}

// ScopeBumpVersion increments the scope's version (saturating). It returns whether the scope
// existed.
func (w *Writer[K]) ScopeBumpVersion(scopeID uint32) (bool, error) {
	if err := w.ensureUsable(); err != nil {
		return false, err
	}
	i, ok := w.scopePos(scopeID)
	if !ok {
		return false, nil
	}
	if w.scopes[i].version != maxUint64 {
		w.scopes[i].version++
	}
	w.scopeDirty = true
	return true, nil
}

// ScopeType returns the scope's opaque type byte and whether it exists.
func (w *Writer[K]) ScopeType(scopeID uint32) (uint8, bool) {
	if i, ok := w.scopePos(scopeID); ok {
		return w.scopes[i].typ, true
	}
	return 0, false
}

// ScopeSetType sets the scope's opaque type byte (the engine does not interpret it, §C.2).
// It returns whether the scope existed.
func (w *Writer[K]) ScopeSetType(scopeID uint32, typ uint8) (bool, error) {
	if err := w.ensureUsable(); err != nil {
		return false, err
	}
	i, ok := w.scopePos(scopeID)
	if !ok {
		return false, nil
	}
	w.scopes[i].typ = typ
	w.scopeDirty = true
	return true, nil
}

// scopePos returns the index of a defined scope in the registry (binary search) and whether
// it was found. The FILE target (scope_id 0) is never a defined scope, so registry
// getters/setters miss it (§C.2).
func (w *Writer[K]) scopePos(scopeID uint32) (int, bool) {
	if scopeID == fileScopeID {
		return 0, false
	}
	return w.scopeIdxAny(scopeID)
}

// scopeIdxAny returns the index of any scope record by id, INCLUDING FILE (scope_id 0) — used
// by the KV layer, which targets FILE as well as defined scopes.
func (w *Writer[K]) scopeIdxAny(scopeID uint32) (int, bool) {
	lo, hi := 0, len(w.scopes)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if w.scopes[mid].id < scopeID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(w.scopes) && w.scopes[lo].id == scopeID {
		return lo, true
	}
	return 0, false
}

// rebuildScopeTable rebuilds the scope table from the in-memory registry at commit (§C.2):
// free the old tree's pages (reclaimed next txn) and bulk-build a fresh one. It sets
// scopeTableRoot (0 when empty → the file stays/returns to byte-compatible v4.0, §C.6).
func (w *Writer[K]) rebuildScopeTable() error {
	var old []uint32
	collectScopePages(w.store.committedBytes(), w.scopeTableRoot, &old)
	for _, p := range old {
		w.freePage(p)
	}
	root, err := w.buildScopeTree(w.scopes)
	if err != nil {
		return err
	}
	w.scopeTableRoot = root
	return nil
}

// buildScopeTree bulk-builds a scope-table B+tree from scopes (sorted by id) into freshly
// allocated pages; it returns the new root pgno (0 if empty). Leaves fill to scopeLeafMax;
// branches use each child subtree's first id as a separator (shape is impl-defined).
func (w *Writer[K]) buildScopeTree(scopes []scopeRec) (uint32, error) {
	if len(scopes) == 0 {
		return 0, nil
	}
	leafMaxN := scopeLeafMax()
	// level holds (pgno, firstID) for each node at the current level.
	type node struct {
		pgno    uint32
		firstID uint32
	}
	var level []node
	for start := 0; start < len(scopes); start += leafMaxN {
		end := start + leafMaxN
		if end > len(scopes) {
			end = len(scopes)
		}
		chunk := scopes[start:end]
		pgno, err := w.allocPage()
		if err != nil {
			return 0, err
		}
		page := w.store.writePageMut(pgno)
		writeScopeLeaf(page, pgno, chunk)
		w.markDirty(pgno)
		level = append(level, node{pgno: pgno, firstID: chunk[0].id})
	}
	height := uint32(1)
	fanout := scopeBranchMax() + 1 // max children per branch (separators + 1)
	for len(level) > 1 {
		if height >= treeHeightMax {
			return 0, errInvalidInput("scope table would exceed TREE_HEIGHT_MAX")
		}
		var next []node
		for _, chunk := range packChildren(level, fanout) {
			pgno, err := w.allocPage()
			if err != nil {
				return 0, err
			}
			children := make([]uint32, len(chunk))
			seps := make([]uint32, 0, len(chunk)-1)
			for i, c := range chunk {
				children[i] = c.pgno
				if i > 0 {
					seps = append(seps, c.firstID)
				}
			}
			page := w.store.writePageMut(pgno)
			writeScopeBranch(page, pgno, seps, children)
			w.markDirty(pgno)
			next = append(next, node{pgno: pgno, firstID: chunk[0].firstID})
		}
		level = next
		height++
	}
	return level[0].pgno, nil
}

// packChildren splits children into branch-node groups of <= fanout each, rebalancing so the
// FINAL group never has a single child (F1): a lone-last-child branch has 0 separators, which
// the reader's validator rejects (count < 1). fanout (max children) is >= 4 for both metadata
// trees, so the combined last two groups (stride+1 children) split into two groups of >= 2.
// The caller guarantees len(children) >= 2 (the branch level only runs while > 1 node remains).
func packChildren[T any](children []T, fanout int) [][]T {
	n := len(children)
	var groups [][]T
	for start := 0; start < n; start += fanout {
		end := start + fanout
		if end > n {
			end = n
		}
		groups = append(groups, children[start:end])
	}
	// If the last group has exactly one child, steal one from the previous group (which keeps
	// >= fanout-1 >= 2). len(groups) >= 2 here because n >= 2 and a single full group cannot
	// leave a remainder.
	if last := len(groups) - 1; last >= 1 && len(groups[last]) == 1 {
		prev := groups[last-1]
		groups[last-1] = prev[:len(prev)-1]
		groups[last] = children[len(children)-2:]
	}
	return groups
}

// --- v4.1 per-scope KV (§C.4) ---

// Changed reports what MetaDelete did, mirroring Rust's Changed enum (§C.7). false = no
// change (the key was absent — a no-op success).
type Changed bool

const (
	// Unchanged: no key was removed (a no-op success, §C.7).
	Unchanged Changed = false
	// ChangedYes: a key was removed.
	ChangedYes Changed = true
)

// MetaSet sets key = (type, value) on target (target = scope_id, 0 = FILE). It buffers the
// change in memory; the target's KV tree is bulk-rebuilt at the next Commit. Many MetaSet on
// one target in one txn ⇒ ONE rebuild (§C.4). It validates key (UTF-8, 1..=1024, no NUL) and,
// for type == 0, the whole value (UTF-8, no NUL) → InvalidInput on violation (§C.7). A
// non-existent non-FILE target → InvalidInput.
func (w *Writer[K]) MetaSet(target uint32, key []byte, typ uint32, value []byte) error {
	if err := w.ensureUsable(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if err := checkTextValue(typ, value); err != nil {
		return err
	}
	if err := w.requireTarget(target); err != nil {
		return err
	}
	entries, err := w.kvLoadDirty(target)
	if err != nil {
		return err
	}
	pos := partitionIdx(len(entries), func(i int) bool { return bytes.Compare(entries[i].key, key) < 0 })
	newEntry := kvEntry{key: cloneBytes(key), typ: typ, value: cloneBytes(value)}
	if pos < len(entries) && bytes.Equal(entries[pos].key, key) {
		entries[pos] = newEntry // replace in place (key already present)
	} else {
		entries = insertKVEntry(entries, pos, newEntry)
	}
	w.kvDirty[target] = entries
	return nil
}

// MetaGet gets key on target as (type, value) (the whole reassembled value) and found=true,
// or found=false if absent (§C.7). It reads the buffered set if the target is dirty this txn,
// else descends the committed KV tree. A non-existent non-FILE target → found=false.
func (w *Writer[K]) MetaGet(target uint32, key []byte) (typ uint32, value []byte, found bool, err error) {
	if err := checkKey(key); err != nil {
		return 0, nil, false, err
	}
	if entries, ok := w.kvDirty[target]; ok {
		pos := partitionIdx(len(entries), func(i int) bool { return bytes.Compare(entries[i].key, key) < 0 })
		if pos < len(entries) && bytes.Equal(entries[pos].key, key) {
			e := entries[pos]
			return e.typ, cloneBytes(e.value), true, nil
		}
		return 0, nil, false, nil
	}
	root, ok := w.targetKVRoot(target)
	if !ok {
		return 0, nil, false, nil
	}
	return kvGet(w.store.committedBytes(), root, key, w.totalPages())
}

// MetaDelete deletes key on target. It returns ChangedYes if a key was removed, Unchanged if
// it was absent (a no-op success, §C.7). It buffers the change; rebuilt at commit.
func (w *Writer[K]) MetaDelete(target uint32, key []byte) (Changed, error) {
	if err := w.ensureUsable(); err != nil {
		return Unchanged, err
	}
	if err := checkKey(key); err != nil {
		return Unchanged, err
	}
	// Deleting on a non-existent target is a no-op (nothing to delete).
	if _, dirty := w.kvDirty[target]; !dirty {
		if _, ok := w.scopeIdxAny(target); !ok {
			return Unchanged, nil
		}
	}
	entries, err := w.kvLoadDirty(target)
	if err != nil {
		return Unchanged, err
	}
	pos := partitionIdx(len(entries), func(i int) bool { return bytes.Compare(entries[i].key, key) < 0 })
	if pos < len(entries) && bytes.Equal(entries[pos].key, key) {
		entries = removeKVEntry(entries, pos)
		w.kvDirty[target] = entries
		return ChangedYes, nil
	}
	w.kvDirty[target] = entries
	return Unchanged, nil
}

// MetaList lists every (key, type, value) on target, ordered by key (§C.4). It reads the
// buffered set if dirty this txn, else the committed KV tree. A non-existent target → an empty
// list.
func (w *Writer[K]) MetaList(target uint32) ([]MetaEntry, error) {
	if entries, ok := w.kvDirty[target]; ok {
		out := make([]MetaEntry, 0, len(entries))
		for i := range entries {
			out = append(out, MetaEntry{Key: cloneBytes(entries[i].key), Type: entries[i].typ, Value: cloneBytes(entries[i].value)})
		}
		return out, nil
	}
	var out []MetaEntry
	if root, ok := w.targetKVRoot(target); ok {
		var entries []kvEntry
		if err := kvList(w.store.committedBytes(), root, w.totalPages(), &entries); err != nil {
			return nil, err
		}
		out = make([]MetaEntry, 0, len(entries))
		for i := range entries {
			out = append(out, MetaEntry{Key: entries[i].key, Type: entries[i].typ, Value: entries[i].value})
		}
	}
	return out, nil
}

// targetKVRoot returns the committed kv_root of target and whether the target has a record.
// FILE (scope_id 0) is looked up like any scope record.
func (w *Writer[K]) targetKVRoot(target uint32) (uint32, bool) {
	if i, ok := w.scopeIdxAny(target); ok {
		return w.scopes[i].kvRoot, true
	}
	return 0, false
}

// requireTarget validates that a KV mutation target exists. FILE (scope_id 0) is always valid
// (it is created on demand). A non-existent defined scope → InvalidInput (§C.7 — a write to a
// scope that was never defined is caller error).
func (w *Writer[K]) requireTarget(target uint32) error {
	if target == fileScopeID {
		return nil
	}
	if _, ok := w.scopePos(target); ok {
		return nil
	}
	return errInvalidInput("meta_set on undefined scope")
}

// kvLoadDirty returns the target's buffered entry set, loading it from the committed KV tree
// on first touch this txn (O(n_kv) once). Subsequent mutations operate in memory.
func (w *Writer[K]) kvLoadDirty(target uint32) ([]kvEntry, error) {
	if entries, ok := w.kvDirty[target]; ok {
		return entries, nil
	}
	var entries []kvEntry
	if root, ok := w.targetKVRoot(target); ok {
		if err := kvList(w.store.committedBytes(), root, w.totalPages(), &entries); err != nil {
			return nil, err
		}
	}
	w.kvDirty[target] = entries
	return entries, nil
}

// rebuildDirtyKV rebuilds every dirty target's KV tree at commit (§C.4): for each, free the
// old KV + overflow pages, bulk-build a fresh balanced tree from the sorted buffered entries,
// and switch the target's kv_root. It creates a FILE (scope_id 0) record on demand the first
// time FILE gets KV; drops a record's KV (kv_root = 0) when it becomes empty. It marks the
// registry dirty so the scope table picks up the new roots.
func (w *Writer[K]) rebuildDirtyKV() error {
	if len(w.kvDirty) == 0 {
		return nil
	}
	// Process targets in id order for determinism (page-allocation order). The set is small.
	targets := make([]uint32, 0, len(w.kvDirty))
	for t := range w.kvDirty {
		targets = append(targets, t)
	}
	sortU32(targets)
	for _, target := range targets {
		entries := w.kvDirty[target]
		oldRoot, _ := w.targetKVRoot(target)
		w.freeKVTree(oldRoot)
		newRoot, err := w.buildKVTree(entries)
		if err != nil {
			return err
		}
		w.setTargetKVRoot(target, newRoot)
	}
	w.kvDirty = make(map[uint32][]kvEntry)
	return nil
}

// setTargetKVRoot sets a target's kv_root, creating the FILE record on demand and removing a
// record that becomes empty metadata. It marks the registry dirty (the scope table must
// rebuild).
func (w *Writer[K]) setTargetKVRoot(target, newRoot uint32) {
	if i, ok := w.scopeIdxAny(target); ok {
		if target == fileScopeID && newRoot == 0 {
			// FILE carries only KV; with none it has no metadata → drop its record so an
			// all-empty file returns to byte-compatible v4.0 (§C.6).
			w.scopes = append(w.scopes[:i], w.scopes[i+1:]...)
		} else {
			w.scopes[i].kvRoot = newRoot
		}
	} else if newRoot != 0 && target == fileScopeID {
		// Only FILE is auto-created (a defined scope always has a record already). The
		// target == fileScopeID guard mirrors Rust's debug_assert: a non-FILE target without a
		// record must never reach here (meta_set rejects undefined scopes), so refuse to insert
		// a bogus record rather than trust the invariant silently.
		// FILE record: no name/type/version, just the KV root. Insert sorted (id 0 sorts first).
		pos := partitionIdx(len(w.scopes), func(i int) bool { return w.scopes[i].id < target })
		w.scopes = insertScopeRec(w.scopes, pos, scopeRec{id: target, kvRoot: newRoot})
	}
	w.scopeDirty = true
}

// freeKVTree frees a KV tree's pages (the tree + all overflow chains) into this txn's freed
// set (reclaimed next txn, D7). root == 0 is a no-op.
func (w *Writer[K]) freeKVTree(root uint32) {
	if root == 0 {
		return
	}
	var pages []uint32
	collectKVPages(w.store.committedBytes(), root, w.totalPages(), &pages)
	for _, p := range pages {
		w.freePage(p)
	}
}

// buildKVTree bulk-builds a balanced KV B+tree from entries (sorted, key-unique) into freshly
// allocated pages; it returns the new root pgno (0 if empty). Large values are written to
// overflow chains first; leaves pack entries greedily by encoded size, branches by separator
// size — the shape is implementation-defined (§C.4/§D).
func (w *Writer[K]) buildKVTree(entries []kvEntry) (uint32, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	// 1) Turn each entry into a leaf slot, writing overflow chains for large values.
	slots := make([]leafSlot, 0, len(entries))
	for i := range entries {
		e := entries[i]
		if len(e.value) <= kvInlineMax {
			slots = append(slots, leafSlot{key: e.key, typ: e.typ, value: e.value})
		} else {
			first, err := w.writeOverflowChain(e.value)
			if err != nil {
				return 0, err
			}
			slots = append(slots, leafSlot{key: e.key, typ: e.typ, overflow: true, firstPgno: first, totalLen: uint64(len(e.value))})
		}
	}
	// 2) Pack leaves greedily (each leaf keeps >= 1 slot; the spec geometry guarantees the
	//    largest single inline entry + its slot fits a fresh leaf body).
	type node struct {
		pgno     uint32
		firstKey []byte
	}
	var level []node
	for i := 0; i < len(slots); {
		used := 0
		j := i
		for j < len(slots) {
			add := slots[j].footprint()
			if j > i && used+add > kvPageBody {
				break
			}
			used += add
			j++
		}
		pgno, err := w.allocPage()
		if err != nil {
			return 0, err
		}
		page := w.store.writePageMut(pgno)
		writeKVLeaf(page, pgno, slots[i:j])
		w.markDirty(pgno)
		level = append(level, node{pgno: pgno, firstKey: cloneBytes(slots[i].slotKey())})
		i = j
	}
	// 3) Build branch levels until a single root remains.
	height := uint32(1)
	for len(level) > 1 {
		if height >= treeHeightMax {
			return 0, errInvalidInput("kv tree would exceed TREE_HEIGHT_MAX")
		}
		// Greedily compute node boundaries: each branch is a leftmost child (a fixed u32, no
		// separator) + as many (sep, child) slot entries as fit the body. bounds holds the
		// start index of each node into level; node k spans level[bounds[k] : bounds[k+1]].
		bounds := []int{0}
		for i := 0; i < len(level); {
			body := kvPageBody - 4 // leftmost-child u32 consumes 4 body bytes
			used := 0
			j := i + 1
			for j < len(level) {
				add := kvBranchSepSize(len(level[j].firstKey)) + kvSlotSize
				if used+add > body {
					break
				}
				used += add
				j++
			}
			bounds = append(bounds, j)
			i = j
		}
		// F1: a final branch with a single child (leftmost only, 0 separators) is rejected by
		// the reader's validator (count < 1). When the last node would be lone, move one child
		// from the previous node into it (the previous keeps >= 2: greedy fits >= 2, and a
		// next-to-last node holds >= 2 children whenever a remainder exists). Recurs naturally
		// up the tree because every level re-runs this loop.
		if k := len(bounds) - 1; k >= 2 && bounds[k]-bounds[k-1] == 1 {
			bounds[k-1] = bounds[k] - 2
		}
		next := make([]node, 0, len(bounds)-1)
		for k := 0; k+1 < len(bounds); k++ {
			lo, hi := bounds[k], bounds[k+1]
			leftmost := level[lo].pgno
			seps := make([]branchSep, 0, hi-lo-1)
			for j := lo + 1; j < hi; j++ {
				seps = append(seps, branchSep{sep: level[j].firstKey, child: level[j].pgno})
			}
			pgno, err := w.allocPage()
			if err != nil {
				return 0, err
			}
			page := w.store.writePageMut(pgno)
			writeKVBranch(page, pgno, leftmost, seps)
			w.markDirty(pgno)
			next = append(next, node{pgno: pgno, firstKey: level[lo].firstKey})
		}
		level = next
		height++
	}
	return level[0].pgno, nil
}

// writeOverflowChain writes value to a fresh overflow page chain; it returns the first page's
// pgno. It splits into ceil(len/overflow_payload) pages, each pointing to the next (last → 0),
// §D.
func (w *Writer[K]) writeOverflowChain(value []byte) (uint32, error) {
	if len(value) == 0 {
		// Release-mode guard (mirrors Rust): the indexing below would panic on
		// an empty chain. Callers guard on kvInlineMax, but a future caller must
		// not trip a silent panic.
		return 0, errInvalidInput("write_overflow_chain: empty value")
	}
	payload := overflowPayload
	n := (len(value) + payload - 1) / payload
	// Allocate all pages up front so each can reference the next.
	pgnos := make([]uint32, n)
	for k := 0; k < n; k++ {
		p, err := w.allocPage()
		if err != nil {
			return 0, err
		}
		pgnos[k] = p
	}
	for k := 0; k < n; k++ {
		start := k * payload
		end := start + payload
		if end > len(value) {
			end = len(value)
		}
		var next uint32
		if k+1 < n {
			next = pgnos[k+1]
		}
		page := w.store.writePageMut(pgnos[k])
		writeOverflow(page, pgnos[k], next, value[start:end])
		w.markDirty(pgnos[k])
	}
	return pgnos[0], nil
}

// totalPages returns the current logical page count of the in-memory image (for KV bounds
// checks).
func (w *Writer[K]) totalPages() uint64 {
	return w.store.totalPages()
}

// ensureUsable refuses to operate on a writer poisoned by a failed commit (see poisoned):
// the in-memory allocator state is indeterminate, so the caller must discard and reopen.
func (w *Writer[K]) ensureUsable() error {
	if w.poisoned {
		return errState("writer poisoned by a failed commit; discard and reopen")
	}
	return nil
}

// rebuildCommitState runs the commit's metadata rebuild phase (§C.2/§C.4): bulk-rebuild each
// dirty target's KV tree (switching its kv_root in the registry, so the scope-table rebuild
// carries the new roots; frees old KV + overflow pages), then the scope table if it changed.
// This allocates and writes the new scope-table/KV pages into the image (marking them dirty)
// and frees the old ones. The OS layer MUST call this BEFORE takeDirty so those metadata pages
// are pwritten and made durable at Barrier 1 like every other data page (§6.3); the in-memory
// Commit path reaches it via commitMeta. Performs irreversible page alloc/free, so a mid-phase
// failure leaves the allocator/registry indeterminate: the writer is poisoned (the on-disk file
// stays the last committed valid state) and the caller must discard and reopen. Refuses if
// txn_id is exhausted (§6.3; unreachable in practice).
func (w *Writer[K]) rebuildCommitState() error {
	if err := w.ensureUsable(); err != nil {
		return err
	}
	if w.txnID == maxUint64 {
		return errInvalidInput("txn_id exhausted")
	}
	if err := w.rebuildCommitStateInner(); err != nil {
		w.poisoned = true
		return err
	}
	return nil
}

func (w *Writer[K]) rebuildCommitStateInner() error {
	if err := w.rebuildDirtyKV(); err != nil {
		return err
	}
	if w.scopeDirty {
		if err := w.rebuildScopeTable(); err != nil {
			return err
		}
		w.scopeDirty = false
	}
	return nil
}

// finishCommitMeta finalizes the commit: write the new meta into the inactive page and flip it
// (the commit point, §6.3); it returns the page number (0 or 1) just written so the OS layer can
// pwrite exactly that page as Barrier 2. Reclaims this txn's freed pages (D7). MUST be called
// after rebuildCommitState and, on the OS path, after the data-page Barrier 1, so the meta never
// references an unwritten page.
func (w *Writer[K]) finishCommitMeta(updatedUnixtime uint64) uint32 {
	inactive := 1 - w.activeMeta
	w.txnID++
	w.writeMeta(inactive, w.txnID, updatedUnixtime)
	w.activeMeta = inactive
	w.free = append(w.free, w.freedThisTxn...)
	w.freedThisTxn = w.freedThisTxn[:0]
	clear(w.privatePages)
	// The file is now this long on disk after the OS layer's truncate; the next txn's
	// grown region starts here.
	w.committedPages = int(w.store.totalPages())
	return inactive
}

// commitMeta is the in-memory commit half: rebuild the metadata pages then finalize the meta in
// one step (no separate page barrier — the whole image is the unit, §6.3). The OS layer instead
// interleaves rebuildCommitState / takeDirty / finishCommitMeta around its two fsync barriers.
func (w *Writer[K]) commitMeta(updatedUnixtime uint64) (uint32, error) {
	if err := w.rebuildCommitState(); err != nil {
		return 0, err
	}
	return w.finishCommitMeta(updatedUnixtime), nil
}

// poison marks the writer unusable after a failed durable commit (the OS layer calls this when
// I/O fails between the metadata rebuild and the commit point): the in-memory state is partially
// advanced and must not be reused. The on-disk file remains the last committed valid state.
func (w *Writer[K]) poison() {
	w.poisoned = true
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
	// Build a set for O(1) lookup. Clone — do NOT drain! finishCommitMeta reads
	// freedThisTxn to move freed pages into the free list.
	freed := make(map[uint32]struct{}, len(w.freedThisTxn))
	for _, p := range w.freedThisTxn {
		freed[p] = struct{}{}
	}
	boundary := uint32(w.committedPages)
	out := d[:0]
	for _, p := range d {
		if p >= boundary {
			out = append(out, p)
		} else if _, ok := freed[p]; !ok {
			out = append(out, p)
		}
	}
	return out
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
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		leaf := newLeafView[K](page, count, w.recordSize)
		pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(rec.from) < 0 })
		return w.leafInsertAt(pgno, pos, rec)
	}
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	branch := newBranchView[K](page, count)
	i := partitionIdx(count, func(j int) bool { return branch.sep(j).cmp(rec.from) <= 0 })
	child := branch.child(i)
	nc, csep, cright, csplit, cerr := w.cowInsert(child, depth+1, rec)
	if cerr != nil {
		return 0, sep, 0, false, cerr
	}
	if csplit {
		return w.branchAbsorbChildSplit(pgno, i, nc, csep, cright)
	}
	p, err := w.branchUpdateChildAt(pgno, i, nc)
	return p, sep, 0, false, err
}

func (w *Writer[K]) leafInsertAt(pgno uint32, pos int, rec ownedRecord[K]) (newPgno uint32, sep K, right uint32, split bool, err error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	if count < w.leafMax {
		p, e := w.ensurePrivatePage(pgno)
		if e != nil {
			return 0, sep, 0, false, e
		}
		page := w.store.writePageMut(p)
		start := pageHeaderSize + pos*w.recordSize
		end := pageHeaderSize + count*w.recordSize
		copy(page[start+w.recordSize:], page[start:end])
		recordWrite[K](page[start:start+w.recordSize], rec.from, rec.to, rec.scope)
		writePageHeader(page, pageTypeLeaf, uint16(count+1), p)
		finalizeChecksum(page)
		return p, sep, 0, false, nil
	}

	var src [pageSize]byte
	copy(src[:], w.page(pgno))
	newCount := count + 1
	mid := newCount / 2
	sep = w.leafInsertCombinedFrom(src[:], pos, rec, mid)
	left, e := w.allocPrivateReplacing(pgno)
	if e != nil {
		return 0, sep, 0, false, e
	}
	rightPgno, e := w.allocPage()
	if e != nil {
		return 0, sep, 0, false, e
	}
	w.writeLeafFromInsertCombined(left, src[:], count, pos, rec, 0, mid)
	w.writeLeafFromInsertCombined(rightPgno, src[:], count, pos, rec, mid, newCount-mid)
	return left, sep, rightPgno, true, nil
}

func (w *Writer[K]) leafDeleteAt(pgno uint32, pos int) (uint32, bool, error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	newCount := count - 1
	p, err := w.ensurePrivatePage(pgno)
	if err != nil {
		return 0, false, err
	}
	page := w.store.writePageMut(p)
	start := pageHeaderSize + pos*w.recordSize
	next := start + w.recordSize
	end := pageHeaderSize + count*w.recordSize
	copy(page[start:], page[next:end])
	tail := pageHeaderSize + newCount*w.recordSize
	clear(page[tail:end])
	writePageHeader(page, pageTypeLeaf, uint16(newCount), p)
	finalizeChecksum(page)
	return p, newCount == 0, nil
}

func (w *Writer[K]) leafInsertCombinedFrom(src []byte, insertPos int, rec ownedRecord[K], idx int) K {
	if idx == insertPos {
		return rec.from
	}
	oldIdx := idx
	if idx > insertPos {
		oldIdx--
	}
	off := pageHeaderSize + oldIdx*w.recordSize
	var zero K
	return zero.readLE(src[off : off+zero.width()])
}

func (w *Writer[K]) writeLeafFromInsertCombined(pgno uint32, src []byte, oldCount, insertPos int, rec ownedRecord[K], startIdx, n int) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
	clear(page)
	writePageHeader(page, pageTypeLeaf, uint16(n), pgno)
	for outIdx := 0; outIdx < n; outIdx++ {
		combinedIdx := startIdx + outIdx
		dst := pageHeaderSize + outIdx*w.recordSize
		if combinedIdx == insertPos {
			recordWrite[K](page[dst:dst+w.recordSize], rec.from, rec.to, rec.scope)
			continue
		}
		oldIdx := combinedIdx
		if combinedIdx > insertPos {
			oldIdx--
		}
		if oldIdx >= oldCount {
			panic("writeLeafFromInsertCombined: old index out of range")
		}
		srcOff := pageHeaderSize + oldIdx*w.recordSize
		copy(page[dst:dst+w.recordSize], src[srcOff:srcOff+w.recordSize])
	}
	finalizeChecksum(page)
}

func (w *Writer[K]) leafPairCombinedFrom(left []byte, leftCount int, right []byte, idx int) K {
	var zero K
	width := zero.width()
	if idx < leftCount {
		off := pageHeaderSize + idx*w.recordSize
		return zero.readLE(left[off : off+width])
	}
	off := pageHeaderSize + (idx-leftCount)*w.recordSize
	return zero.readLE(right[off : off+width])
}

func (w *Writer[K]) writeLeafFromPairCombined(pgno uint32, left []byte, leftCount int, right []byte, rightCount int, startIdx, n int) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
	clear(page)
	writePageHeader(page, pageTypeLeaf, uint16(n), pgno)
	for outIdx := 0; outIdx < n; outIdx++ {
		combinedIdx := startIdx + outIdx
		dst := pageHeaderSize + outIdx*w.recordSize
		if combinedIdx < leftCount {
			src := pageHeaderSize + combinedIdx*w.recordSize
			copy(page[dst:dst+w.recordSize], left[src:src+w.recordSize])
		} else {
			rightIdx := combinedIdx - leftCount
			if rightIdx >= rightCount {
				panic("writeLeafFromPairCombined: right index out of range")
			}
			src := pageHeaderSize + rightIdx*w.recordSize
			copy(page[dst:dst+w.recordSize], right[src:src+w.recordSize])
		}
	}
	finalizeChecksum(page)
}

func (w *Writer[K]) rebalanceLeafChildren(leftPgno, rightPgno uint32) (uint32, K, uint32, bool, error) {
	var sep K
	var leftSrc, rightSrc [pageSize]byte
	copy(leftSrc[:], w.page(leftPgno))
	copy(rightSrc[:], w.page(rightPgno))
	leftCount := int(decodePageHeader(leftSrc[:]).entryCount)
	rightCount := int(decodePageHeader(rightSrc[:]).entryCount)
	combined := leftCount + rightCount
	left, err := w.allocPrivateReplacing(leftPgno)
	if err != nil {
		return 0, sep, 0, false, err
	}
	if combined <= w.leafMax {
		w.writeLeafFromPairCombined(left, leftSrc[:], leftCount, rightSrc[:], rightCount, 0, combined)
		w.freePage(rightPgno)
		return left, sep, 0, false, nil
	}
	mid := combined / 2
	sep = w.leafPairCombinedFrom(leftSrc[:], leftCount, rightSrc[:], mid)
	right, err := w.allocPage()
	if err != nil {
		return 0, sep, 0, false, err
	}
	w.writeLeafFromPairCombined(left, leftSrc[:], leftCount, rightSrc[:], rightCount, 0, mid)
	w.writeLeafFromPairCombined(right, leftSrc[:], leftCount, rightSrc[:], rightCount, mid, combined-mid)
	w.freePage(rightPgno)
	return left, sep, right, true, nil
}

// --- page I/O over the in-memory image ---

func (w *Writer[K]) markDirty(pgno uint32) {
	if _, ok := w.privatePages[pgno]; !ok {
		w.privatePages[pgno] = struct{}{}
		w.dirty = append(w.dirty, pgno)
	}
}

func (w *Writer[K]) ensurePrivatePage(pgno uint32) (uint32, error) {
	if _, ok := w.privatePages[pgno]; ok {
		return pgno, nil
	}

	privatePgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	dst := w.store.writePageMut(privatePgno)
	copy(dst, w.page(pgno))
	w.markDirty(privatePgno)
	w.freePage(pgno)
	return privatePgno, nil
}

// allocPrivateReplacing is like ensurePrivatePage but does NOT copy the old
// page's contents. Used on split/rebalance paths where the caller overwrites
// the entire page (via write*From*Combined, which starts with clear(page)).
// Avoids a wasted 4 KB memcpy per split/rebalance. If pgno is already private
// (touched earlier this txn), it is reused without copy.
func (w *Writer[K]) allocPrivateReplacing(pgno uint32) (uint32, error) {
	if _, ok := w.privatePages[pgno]; ok {
		return pgno, nil
	}
	privatePgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.markDirty(privatePgno)
	w.freePage(pgno)
	return privatePgno, nil
}

func (w *Writer[K]) writeLeaf(records []ownedRecord[K]) (uint32, error) {
	pgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.writeLeafInto(pgno, records)
	return pgno, nil
}

func (w *Writer[K]) writeLeafInto(pgno uint32, records []ownedRecord[K]) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
	for i := range page {
		page[i] = 0
	}
	writePageHeader(page, pageTypeLeaf, uint16(len(records)), pgno)
	for i, r := range records {
		off := pageHeaderSize + i*w.recordSize
		recordWrite[K](page[off:off+w.recordSize], r.from, r.to, r.scope)
	}
	finalizeChecksum(page)
}

func (w *Writer[K]) writeBranch(seps []K, children []uint32) (uint32, error) {
	pgno, err := w.allocPage()
	if err != nil {
		return 0, err
	}
	w.writeBranchInto(pgno, seps, children)
	return pgno, nil
}

func (w *Writer[K]) writeBranchInto(pgno uint32, seps []K, children []uint32) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
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
}

func (w *Writer[K]) branchPairOff(i int) int {
	var zero K
	return pageHeaderSize + 4 + i*(zero.width()+4)
}

func (w *Writer[K]) branchChildOff(j int) int {
	if j == 0 {
		return pageHeaderSize
	}
	var zero K
	return w.branchPairOff(j-1) + zero.width()
}

func (w *Writer[K]) branchChildIn(page []byte, j int) uint32 {
	return le.Uint32(page[w.branchChildOff(j):])
}

func (w *Writer[K]) branchSepIn(page []byte, i int) K {
	var zero K
	width := zero.width()
	off := w.branchPairOff(i)
	return zero.readLE(page[off : off+width])
}

func (w *Writer[K]) branchPutChild(page []byte, j int, child uint32) {
	le.PutUint32(page[w.branchChildOff(j):], child)
}

func (w *Writer[K]) branchUpdateChildAt(pgno uint32, childIdx int, child uint32) (uint32, error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	p, err := w.ensurePrivatePage(pgno)
	if err != nil {
		return 0, err
	}
	page := w.store.writePageMut(p)
	w.branchPutChild(page, childIdx, child)
	writePageHeader(page, pageTypeBranch, uint16(count), p)
	finalizeChecksum(page)
	return p, nil
}

func (w *Writer[K]) branchAbsorbChildSplit(pgno uint32, childIdx int, newChild uint32, sep K, right uint32) (newPgno uint32, promoted K, promotedRight uint32, split bool, err error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	if count < w.branchMax {
		p, e := w.ensurePrivatePage(pgno)
		if e != nil {
			return 0, promoted, 0, false, e
		}
		var zero K
		width := zero.width()
		slot := width + 4
		page := w.store.writePageMut(p)
		start := w.branchPairOff(childIdx)
		end := w.branchPairOff(count)
		copy(page[start+slot:], page[start:end])
		w.branchPutChild(page, childIdx, newChild)
		sep.writeLE(page[start : start+width])
		le.PutUint32(page[start+width:], right)
		writePageHeader(page, pageTypeBranch, uint16(count+1), p)
		finalizeChecksum(page)
		return p, promoted, 0, false, nil
	}

	var src [pageSize]byte
	copy(src[:], w.page(pgno))
	newCount := count + 1
	mid := newCount / 2
	promoted = w.branchInsertCombinedSep(src[:], childIdx, sep, mid)
	left, e := w.allocPrivateReplacing(pgno)
	if e != nil {
		return 0, promoted, 0, false, e
	}
	newRight, e := w.allocPage()
	if e != nil {
		return 0, promoted, 0, false, e
	}
	w.writeBranchFromInsertCombined(left, src[:], count, childIdx, newChild, sep, right, 0, mid)
	w.writeBranchFromInsertCombined(newRight, src[:], count, childIdx, newChild, sep, right, mid+1, newCount-mid-1)
	return left, promoted, newRight, true, nil
}

func (w *Writer[K]) branchInsertCombinedSep(src []byte, insertIdx int, sep K, idx int) K {
	if idx < insertIdx {
		return w.branchSepIn(src, idx)
	}
	if idx == insertIdx {
		return sep
	}
	return w.branchSepIn(src, idx-1)
}

func (w *Writer[K]) branchInsertCombinedChild(src []byte, insertIdx int, newChild uint32, right uint32, idx int) uint32 {
	if idx <= insertIdx {
		if idx == insertIdx {
			return newChild
		}
		return w.branchChildIn(src, idx)
	}
	if idx == insertIdx+1 {
		return right
	}
	return w.branchChildIn(src, idx-1)
}

func (w *Writer[K]) writeBranchFromInsertCombined(pgno uint32, src []byte, oldCount int, insertIdx int, newChild uint32, sep K, right uint32, sepStart, sepLen int) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
	clear(page)
	writePageHeader(page, pageTypeBranch, uint16(sepLen), pgno)
	w.branchPutChild(page, 0, w.branchInsertCombinedChild(src, insertIdx, newChild, right, sepStart))
	var zero K
	width := zero.width()
	for outIdx := 0; outIdx < sepLen; outIdx++ {
		combinedSepIdx := sepStart + outIdx
		dst := w.branchPairOff(outIdx)
		s := w.branchInsertCombinedSep(src, insertIdx, sep, combinedSepIdx)
		c := w.branchInsertCombinedChild(src, insertIdx, newChild, right, combinedSepIdx+1)
		s.writeLE(page[dst : dst+width])
		le.PutUint32(page[dst+width:], c)
	}
	_ = oldCount
	finalizeChecksum(page)
}

func (w *Writer[K]) branchPairCombinedSep(left []byte, leftCount int, parentSep K, right []byte, idx int) K {
	if idx < leftCount {
		return w.branchSepIn(left, idx)
	}
	if idx == leftCount {
		return parentSep
	}
	return w.branchSepIn(right, idx-leftCount-1)
}

func (w *Writer[K]) branchPairCombinedChild(left []byte, leftCount int, right []byte, idx int) uint32 {
	if idx <= leftCount {
		return w.branchChildIn(left, idx)
	}
	return w.branchChildIn(right, idx-leftCount-1)
}

func (w *Writer[K]) writeBranchFromPairCombined(pgno uint32, left []byte, leftCount int, parentSep K, right []byte, rightCount int, sepStart, sepLen int) {
	w.markDirty(pgno)
	page := w.store.writePageMut(pgno)
	clear(page)
	writePageHeader(page, pageTypeBranch, uint16(sepLen), pgno)
	w.branchPutChild(page, 0, w.branchPairCombinedChild(left, leftCount, right, sepStart))
	var zero K
	width := zero.width()
	for outIdx := 0; outIdx < sepLen; outIdx++ {
		combinedSepIdx := sepStart + outIdx
		dst := w.branchPairOff(outIdx)
		sep := w.branchPairCombinedSep(left, leftCount, parentSep, right, combinedSepIdx)
		child := w.branchPairCombinedChild(left, leftCount, right, combinedSepIdx+1)
		sep.writeLE(page[dst : dst+width])
		le.PutUint32(page[dst+width:], child)
	}
	_ = rightCount
	finalizeChecksum(page)
}

func (w *Writer[K]) rebalanceBranchChildren(leftPgno uint32, parentSep K, rightPgno uint32) (uint32, K, uint32, bool, error) {
	var promoted K
	var leftSrc, rightSrc [pageSize]byte
	copy(leftSrc[:], w.page(leftPgno))
	copy(rightSrc[:], w.page(rightPgno))
	leftCount := int(decodePageHeader(leftSrc[:]).entryCount)
	rightCount := int(decodePageHeader(rightSrc[:]).entryCount)
	combined := leftCount + 1 + rightCount
	left, err := w.allocPrivateReplacing(leftPgno)
	if err != nil {
		return 0, promoted, 0, false, err
	}
	if combined <= w.branchMax {
		w.writeBranchFromPairCombined(left, leftSrc[:], leftCount, parentSep, rightSrc[:], rightCount, 0, combined)
		w.freePage(rightPgno)
		return left, promoted, 0, false, nil
	}
	mid := combined / 2
	promoted = w.branchPairCombinedSep(leftSrc[:], leftCount, parentSep, rightSrc[:], mid)
	right, err := w.allocPage()
	if err != nil {
		return 0, promoted, 0, false, err
	}
	w.writeBranchFromPairCombined(left, leftSrc[:], leftCount, parentSep, rightSrc[:], rightCount, 0, mid)
	w.writeBranchFromPairCombined(right, leftSrc[:], leftCount, parentSep, rightSrc[:], rightCount, mid+1, combined-mid-1)
	w.freePage(rightPgno)
	return left, promoted, right, true, nil
}

func (w *Writer[K]) branchRemoveSepChild(pgno uint32, sepIdx int, leftChild uint32) (uint32, bool, error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	p, err := w.ensurePrivatePage(pgno)
	if err != nil {
		return 0, false, err
	}
	var zero K
	slot := zero.width() + 4
	page := w.store.writePageMut(p)
	w.branchPutChild(page, sepIdx, leftChild)
	start := w.branchPairOff(sepIdx)
	next := start + slot
	end := w.branchPairOff(count)
	copy(page[start:], page[next:end])
	newCount := count - 1
	tail := w.branchPairOff(newCount)
	clear(page[tail:end])
	writePageHeader(page, pageTypeBranch, uint16(newCount), p)
	finalizeChecksum(page)
	return p, newCount == 0, nil
}

func (w *Writer[K]) branchUpdateSepChildren(pgno uint32, sepIdx int, leftChild uint32, sep K, rightChild uint32) (uint32, error) {
	count := int(decodePageHeader(w.page(pgno)).entryCount)
	p, err := w.ensurePrivatePage(pgno)
	if err != nil {
		return 0, err
	}
	var zero K
	width := zero.width()
	page := w.store.writePageMut(p)
	w.branchPutChild(page, sepIdx, leftChild)
	sepOff := w.branchPairOff(sepIdx)
	sep.writeLE(page[sepOff : sepOff+width])
	le.PutUint32(page[sepOff+width:], rightChild)
	writePageHeader(page, pageTypeBranch, uint16(count), p)
	finalizeChecksum(page)
	return p, nil
}

func (w *Writer[K]) writeMeta(pgno uint32, txnID, updatedUnixtime uint64) {
	var zero K
	// A file with metadata is v4.1 (minor 1, meta_size 94); with none it stays
	// byte-compatible v4.0 (minor 0, meta_size 90) — the upgrade-on-first-write and
	// stays-v4.0-when-empty rule (§C.6).
	verMinor, mSize := versionMinor, metaSize
	if w.scopeTableRoot != 0 {
		verMinor, mSize = versionMinorMetadata, metaSizeV41
	}
	m := meta{
		pgno:            pgno,
		versionMinor:    verMinor,
		metaSize:        mSize,
		pageSize:        pageSize,
		checksumAlgo:    checksumAlgoCRC32C,
		flags:           zero.version().flag(),
		keyWidth:        uint8(zero.width()),
		scopeWidth:      uint8(w.scopeWidth),
		recordSize:      uint32(w.recordSize),
		createdUnixtime: w.createdUnixtime,
		rootPgno:        w.rootPgno,
		treeHeight:      w.treeHeight,
		totalPages:      w.store.totalPages(),
		recordCount:     w.recordCount,
		txnID:           txnID,
		updatedUnixtime: updatedUnixtime,
		scopeTableRoot:  w.scopeTableRoot,
	}
	page := w.store.writePageMut(pgno)
	m.encodeInto(page)
}

// page returns the bytes for pgno. The returned slice MUST NOT be written to — for
// mmapPageStore the mmap is PROT_READ and writing causes SIGSEGV. Use writePageMut for
// mutable access.
func (w *Writer[K]) page(pgno uint32) []byte {
	return w.store.page(pgno)
}

// allocPage allocates a page: reuse a freed page (from a prior txn) or grow the store. It
// refuses to grow past the 2^32-page limit (§6.4) rather than wrap the u32 pgno.
func (w *Writer[K]) allocPage() (uint32, error) {
	if n := len(w.free); n > 0 {
		p := w.free[n-1]
		w.free = w.free[:n-1]
		return p, nil
	}
	if w.store.totalPages() >= (uint64(1)<<32)-1 {
		return 0, errInvalidInput("file would exceed the 2^32-page limit")
	}
	return w.store.allocPage(), nil
}

// freePage marks a page freed by the current txn (reusable only after this txn commits,
// D7).
func (w *Writer[K]) freePage(pgno uint32) {
	w.freedThisTxn = append(w.freedThisTxn, pgno)
}

// deriveFreeSet derives the free set (§7): pages in [2, total_pages) not reachable from
// the root. The image is already validated, so the walk is bounded and safe.
func (w *Writer[K]) deriveFreeSet() []uint32 {
	bytes := w.store.committedBytes()
	total := len(bytes) / pageSize
	used := make([]bool, total)
	used[0] = true // META-A
	used[1] = true // META-B
	if w.rootPgno != 0 {
		w.markReachable(w.rootPgno, 1, used)
	}
	// The v4.1 scope table is also reachable (§C.5): its pages must not be reallocated.
	if w.scopeTableRoot != 0 {
		var sp []uint32
		collectScopePages(bytes, w.scopeTableRoot, &sp)
		for _, p := range sp {
			used[p] = true
		}
	}
	// Every scope's KV tree + its overflow chains are reachable too (§C.5): walk each
	// committed kv_root (incl. FILE's, if persisted) into the used set.
	var kp []uint32
	for i := range w.scopes {
		collectKVPages(bytes, w.scopes[i].kvRoot, uint64(total), &kp)
	}
	for _, p := range kp {
		used[p] = true
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
		page := w.page(w.rootPgno)
		count := int(decodePageHeader(page).entryCount)
		leaf := newLeafView[K](page, count, w.recordSize)
		pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(key) < 0 })
		if count == 1 {
			w.freePage(w.rootPgno)
			w.rootPgno = 0
			w.treeHeight = 0
		} else {
			p, _, err := w.leafDeleteAt(w.rootPgno, pos)
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
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		leaf := newLeafView[K](page, count, w.recordSize)
		pos := partitionIdx(count, func(i int) bool { return leaf.record(i).from().cmp(key) < 0 })
		if pos < count && leaf.record(pos).from().cmp(key) == 0 {
			return w.leafDeleteAt(pgno, pos)
		}
		return pgno, count == 0, nil
	}
	page := w.page(pgno)
	count := int(decodePageHeader(page).entryCount)
	branch := newBranchView[K](page, count)
	i := partitionIdx(count, func(j int) bool { return branch.sep(j).cmp(key) <= 0 })
	child := branch.child(i)
	nc, childUF, err := w.cowDelete(child, depth+1, key)
	if err != nil {
		return 0, false, err
	}
	if childUF {
		return w.rebalanceAt(pgno, i, depth+1, nc)
	}
	p, err := w.branchUpdateChildAt(pgno, i, nc)
	if err != nil {
		return 0, false, err
	}
	return p, false, nil
}

// rebalance merges an underflowed children[i] with an adjacent sibling and re-emits (1 or
// 2 nodes), patching seps/children. Balance-preserving.
func (w *Writer[K]) rebalanceAt(parentPgno uint32, i int, childDepth uint32, newChild uint32) (uint32, bool, error) {
	var parentSrc [pageSize]byte
	copy(parentSrc[:], w.page(parentPgno))
	parentCount := int(decodePageHeader(parentSrc[:]).entryCount)
	var l, r, sepIdx int
	if i > 0 {
		l, r, sepIdx = i-1, i, i-1
	} else {
		l, r, sepIdx = i, i+1, i
	}
	leftPgno := w.branchChildIn(parentSrc[:], l)
	if l == i {
		leftPgno = newChild
	}
	rightPgno := w.branchChildIn(parentSrc[:], r)
	if r == i {
		rightPgno = newChild
	}
	parentSep := w.branchSepIn(parentSrc[:], sepIdx)
	var p, p2 uint32
	var newSep K
	var split bool
	var err error
	if childDepth == w.treeHeight {
		p, newSep, p2, split, err = w.rebalanceLeafChildren(leftPgno, rightPgno)
	} else {
		p, newSep, p2, split, err = w.rebalanceBranchChildren(leftPgno, parentSep, rightPgno)
	}
	if err != nil {
		return 0, false, err
	}
	if split {
		parent, err := w.branchUpdateSepChildren(parentPgno, sepIdx, p, newSep, p2)
		return parent, false, err
	}
	_ = parentCount
	return w.branchRemoveSepChild(parentPgno, sepIdx, p)
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
	// Fixed stack (mirrors Cursor): avoids per-call heap allocation on the
	// set/delete hot path. treeHeightMax (32) is the format's hard cap.
	type frame struct {
		pgno uint32
		ci   int
	}
	var stack [treeHeightMax]frame
	stackLen := 0
	pgno := w.rootPgno
	depth := uint32(1)
	for depth < w.treeHeight {
		page := w.page(pgno)
		count := int(decodePageHeader(page).entryCount)
		b := newBranchView[K](page, count)
		i := partitionIdx(count, func(j int) bool { return b.sep(j).cmp(key) <= 0 })
		stack[stackLen] = frame{pgno: pgno, ci: i}
		stackLen++
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
	for stackLen > 0 {
		stackLen--
		fr := stack[stackLen]
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

func insertKVEntry(s []kvEntry, i int, v kvEntry) []kvEntry {
	s = append(s, kvEntry{})
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeKVEntry(s []kvEntry, i int) []kvEntry {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}

func insertScopeRec(s []scopeRec, i int, v scopeRec) []scopeRec {
	s = append(s, scopeRec{})
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// sortU32 sorts a small u32 slice ascending (insertion sort; the KV-dirty target set is tiny).
func sortU32(s []uint32) {
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j] > v {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}
