package iprangedb

// ChangeEvent represents a change detected during migration.
type ChangeEvent[K ipKey[K]] struct {
	Kind       ChangeKind
	From       K
	To         K
	ScopeID    uint32
	OldScopeID uint32 // valid for Changed (old scope)
	HasOld     bool   // true if OldScopeID is set
}

type ChangeKind int

const (
	ChangeAdded     ChangeKind = 0
	ChangeRemoved   ChangeKind = 1
	ChangeUnchanged ChangeKind = 2
	ChangeChanged   ChangeKind = 3
)

// MigrateCounters holds migration statistics.
type MigrateCounters struct {
	OldScanned     uint64
	DesiredScanned uint64
	Added          uint64
	Removed        uint64
	Changed        uint64
	Unchanged      uint64
}

// MigrateOptions configures the migration.
type MigrateOptions[K ipKey[K]] struct {
	EmitUnchanged bool
	OnChange      func(*ChangeEvent[K])
}

// DesiredRecord is one record from the desired stream.
type DesiredRecord[K ipKey[K]] struct {
	From    K
	To      K
	ScopeID uint32
}

// DesiredStream is a sorted, disjoint stream of desired records.
type DesiredStream[K ipKey[K]] interface {
	Peek() *DesiredRecord[K]
	Next() *DesiredRecord[K]
}

// Migrate updates the writer's pending tree to match the desired stream.
//
// The old committed tree is snapshotted once (O(DB_size), bounded — this is a
// batch operation, not the per-record hot path). The snapshot is a private copy
// because COW allocation during the merge may move the store backing array
// (vec store) or remap the mmap, invalidating live references. The merge then
// streams the old snapshot and the desired input simultaneously — O(1)
// additional memory during the merge loop (fixed-size path stack, no per-record
// heap allocation).
func Migrate[K ipKey[K]](w *Writer[K], desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	if opts == nil {
		opts = &MigrateOptions[K]{}
	}
	counters := &MigrateCounters{}

	committed := append([]byte(nil), w.store.committedBytes()...)
	walker := newTreeWalker[K](committed, w.committedRoot, w.committedHeight)
	oldFrom, oldTo, oldScope, hasOld := walker.peek()

	for {
		des := desired.Peek()
		hasDes := des != nil

		if !hasOld && !hasDes {
			break
		}

		if hasOld && !hasDes {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oldFrom, To: oldTo, OldScopeID: oldScope, HasOld: true})
			if _, err := w.Delete(oldFrom, oldTo); err != nil {
				return nil, err
			}
			counters.Removed++
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			continue
		}

		if !hasOld && hasDes {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: des.From, To: des.To, ScopeID: des.ScopeID})
			if err := w.Set(des.From, des.To, des.ScopeID); err != nil {
				return nil, err
			}
			desired.Next()
			counters.DesiredScanned++
			counters.Added++
			continue
		}

		// Both present.
		if oldTo.cmp(des.From) < 0 {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oldFrom, To: oldTo, OldScopeID: oldScope, HasOld: true})
			if _, err := w.Delete(oldFrom, oldTo); err != nil {
				return nil, err
			}
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			counters.Removed++
			counters.OldScanned++
		} else if des.To.cmp(oldFrom) < 0 {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: des.From, To: des.To, ScopeID: des.ScopeID})
			if err := w.Set(des.From, des.To, des.ScopeID); err != nil {
				return nil, err
			}
			desired.Next()
			counters.DesiredScanned++
			counters.Added++
		} else {
			if oldFrom.cmp(des.From) == 0 && oldTo.cmp(des.To) == 0 && oldScope == des.ScopeID {
				if opts.EmitUnchanged {
					emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeUnchanged, From: oldFrom, To: oldTo, ScopeID: oldScope})
				}
				counters.Unchanged++
			} else {
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeChanged, From: des.From, To: des.To, ScopeID: des.ScopeID, OldScopeID: oldScope, HasOld: true})
				if err := w.Set(des.From, des.To, des.ScopeID); err != nil {
					return nil, err
				}
				counters.Changed++
			}
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			desired.Next()
			counters.OldScanned++
			counters.DesiredScanned++
		}
	}

	return counters, nil
}

func emitChangeGo[K ipKey[K]](opts *MigrateOptions[K], ev *ChangeEvent[K]) {
	if opts.OnChange != nil {
		opts.OnChange(ev)
	}
}

// --- streaming in-order B+tree walker ---
//
// Walks the committed tree in key order using a fixed-size path stack — zero
// heap allocation per record. Mirrors the Rust TreeWalker in migrate.rs. Depth
// convention: root is at depth 1, leaves at depth == height.

type pathEntry struct {
	pgno uint32
	idx  int
}

type treeWalker[K ipKey[K]] struct {
	bytes    []byte
	root     uint32
	height   uint32
	kw       int
	path     [TreeHeightMax]pathEntry
	pathLen  int
	curFrom  K
	curTo    K
	curScope uint32
	curOk    bool
}

func newTreeWalker[K ipKey[K]](bytes []byte, root uint32, height uint32) *treeWalker[K] {
	var zero K
	w := &treeWalker[K]{
		bytes:  bytes,
		root:   root,
		height: height,
		kw:     zero.width(),
	}
	if root != 0 {
		w.descendFirst(root, 1)
	}
	return w
}

func (w *treeWalker[K]) page(pgno uint32) []byte {
	off := int(pgno) * PageSize
	return w.bytes[off : off+PageSize]
}

func (w *treeWalker[K]) peek() (K, K, uint32, bool) {
	return w.curFrom, w.curTo, w.curScope, w.curOk
}

func (w *treeWalker[K]) descendFirst(pgno uint32, depth uint32) {
	var zero K
	page := w.page(pgno)
	h := decodeHeader(page)
	if depth >= w.height {
		count := int(h.entryCount)
		if count > 0 {
			lv := newLeafView(page, count, w.kw)
			w.curFrom = zero.readLE(lv.recordFrom(0))
			w.curTo = zero.readLE(lv.recordTo(0))
			w.curScope = lv.recordScopeID(0)
			w.curOk = true
			w.path[w.pathLen] = pathEntry{pgno: pgno, idx: 0}
			w.pathLen++
		}
		return
	}
	bv := newBranchView(page, int(h.entryCount), w.kw)
	child := bv.child(0)
	w.path[w.pathLen] = pathEntry{pgno: pgno, idx: 0}
	w.pathLen++
	w.descendFirst(child, depth+1)
}

func (w *treeWalker[K]) advance() (K, K, uint32, bool) {
	if !w.curOk {
		var zero K
		return zero, zero, 0, false
	}
	if w.pathLen > 0 {
		w.pathLen--
		e := w.path[w.pathLen]
		w.tryLeafNext(e.pgno, e.idx+1)
	} else {
		w.curOk = false
	}
	return w.curFrom, w.curTo, w.curScope, w.curOk
}

func (w *treeWalker[K]) tryLeafNext(pgno uint32, idx int) {
	var zero K
	page := w.page(pgno)
	h := decodeHeader(page)
	if h.pageType == PageTypeLeaf {
		count := int(h.entryCount)
		if idx < count {
			lv := newLeafView(page, count, w.kw)
			w.curFrom = zero.readLE(lv.recordFrom(idx))
			w.curTo = zero.readLE(lv.recordTo(idx))
			w.curScope = lv.recordScopeID(idx)
			w.curOk = true
			w.path[w.pathLen] = pathEntry{pgno: pgno, idx: idx}
			w.pathLen++
			return
		}
	}
	w.walkUp()
}

func (w *treeWalker[K]) walkUp() {
	for {
		if w.pathLen == 0 {
			w.curOk = false
			return
		}
		w.pathLen--
		e := w.path[w.pathLen]
		// Extract the branch child info into locals BEFORE writing back to the
		// path array. This mirrors the Rust borrow-scope pattern and keeps the
		// read of the old entry isolated from the path mutation below.
		var nextIdx int
		var childPgno uint32
		hasChild := false
		{
			page := w.page(e.pgno)
			h := decodeHeader(page)
			if h.pageType == PageTypeBranch {
				bv := newBranchView(page, int(h.entryCount), w.kw)
				ni := e.idx + 1
				if ni < bv.childCount() {
					nextIdx = ni
					childPgno = bv.child(ni)
					hasChild = true
				}
			}
		}
		if hasChild {
			w.path[w.pathLen] = pathEntry{pgno: e.pgno, idx: nextIdx}
			w.pathLen++
			w.descendFirst(childPgno, uint32(w.pathLen)+1)
			return
		}
	}
}
