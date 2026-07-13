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
	// Combine is an optional scope combiner for overlapping records with
	// differing scopes. Called with (oldScopeID, desiredScopeID) → the scopeID
	// to keep. If nil, the desired scopeID wins (overwrite — legacy behavior).
	// For Mode 0 retention, set this to keep the older timestamp
	// (e.g. func(old, new uint32) uint32 { if old < new { return old }; return new }).
	Combine func(oldScopeID, desiredScopeID uint32) uint32
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
// Uses a proper sweep-line merge that splits at every interval boundary,
// handling ALL overlap cases: identical, partial, one-to-many, many-to-one,
// complete separation. The old committed tree is traversed one record at a
// time via treeWalker (fixed-size path stack, no per-record heap allocation).
// The merge applies set/delete only for changed segments.
//
// Fixes blocker #3 (correct partial-overlap merge with boundary splitting).
//
// NOTE on the committed snapshot: the walker reads from a private copy of the
// committed bytes. With the v4.3 bitset COW model, committed tree pages are
// never modified in-place during a transaction (cowPage always allocates a new
// private page), so reading from the live store would also be safe. The
// snapshot is retained as a defensive measure.
func Migrate[K ipKey[K]](w *Writer[K], desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	if opts == nil {
		opts = &MigrateOptions[K]{}
	}
	counters := &MigrateCounters{}

	// Migration mode: the treeWalker reads committed pages directly from the
	// store. Same-txn recycling would zero COW victims the walker still needs
	// to traverse, so disable it for the duration of the migration (mirrors the
	// Rust migrate.rs "migration mode" guard against the COW-reuse hazard).
	prevRecycle := w.canRecycle
	w.canRecycle = false
	defer func() { w.canRecycle = prevRecycle }()

	var zero K
	kw := zero.width()
	walker := newTreeWalker[K](w.store, kw, w.committedRoot, w.committedHeight)

	// The merge uses a "trim" approach: when old and desired partially overlap,
	// we track trimmed starts for the current record on each side.
	oldFrom, oldTo, oldScope, hasOld := walker.peek()
	desRec := desired.Peek()
	hasDes := desRec != nil

	// Trimmed starts (for partial overlap handling).
	var oldTrim K
	oldTrimOk := false
	if hasOld {
		oldTrim = oldFrom
		oldTrimOk = true
	}
	var desTrim K
	desTrimOk := false
	if hasDes {
		desTrim = desRec.From
		desTrimOk = true
	}

	for {
		// Compute effective current records (with trimmed starts).
		var oEffFrom, oEffTo K
		var oEffScope uint32
		oEff := false
		if hasOld && oldTrimOk {
			oEffFrom = oldTrim
			oEffTo = oldTo
			oEffScope = oldScope
			oEff = true
		}

		var dEffFrom, dEffTo K
		var dEffScope uint32
		dEff := false
		if hasDes && desTrimOk {
			dEffFrom = desTrim
			dEffTo = desRec.To
			dEffScope = desRec.ScopeID
			dEff = true
		}

		if !oEff && !dEff {
			break
		}

		if oEff && !dEff {
			// Only old remains → remove.
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oEffFrom, To: oEffTo, OldScopeID: oEffScope, HasOld: true})
			if _, err := w.Delete(oEffFrom, oEffTo); err != nil {
				return nil, err
			}
			counters.Removed++
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if !oEff && dEff {
			// Only desired remains → add.
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: dEffFrom, To: dEffTo, ScopeID: dEffScope})
			if err := w.Set(dEffFrom, dEffTo, dEffScope); err != nil {
				return nil, err
			}
			counters.Added++
			counters.DesiredScanned++
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Both present.
		if oEffTo.cmp(dEffFrom) < 0 {
			// Old entirely before desired → remove old.
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oEffFrom, To: oEffTo, OldScopeID: oEffScope, HasOld: true})
			if _, err := w.Delete(oEffFrom, oEffTo); err != nil {
				return nil, err
			}
			counters.Removed++
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if dEffTo.cmp(oEffFrom) < 0 {
			// Desired entirely before old → add desired.
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: dEffFrom, To: dEffTo, ScopeID: dEffScope})
			if err := w.Set(dEffFrom, dEffTo, dEffScope); err != nil {
				return nil, err
			}
			counters.Added++
			counters.DesiredScanned++
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Overlap! Split at boundaries.
		// Step 1: Emit any old-only prefix [oEffFrom, dEffFrom-1].
		if oEffFrom.cmp(dEffFrom) < 0 {
			prefixEnd, ok := dEffFrom.checkedDec()
			if !ok {
				prefixEnd = dEffFrom
			}
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oEffFrom, To: prefixEnd, OldScopeID: oEffScope, HasOld: true})
			if _, err := w.Delete(oEffFrom, prefixEnd); err != nil {
				return nil, err
			}
			counters.Removed++
		}

		// Step 2: Emit any desired-only prefix [dEffFrom, oEffFrom-1].
		if dEffFrom.cmp(oEffFrom) < 0 {
			prefixEnd, ok := oEffFrom.checkedDec()
			if !ok {
				prefixEnd = oEffFrom
			}
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: dEffFrom, To: prefixEnd, ScopeID: dEffScope})
			if err := w.Set(dEffFrom, prefixEnd, dEffScope); err != nil {
				return nil, err
			}
			counters.Added++
		}

		// Step 3: Now both start at overlap_start.
		var overlapStart K
		if oEffFrom.cmp(dEffFrom) < 0 {
			overlapStart = dEffFrom
		} else {
			overlapStart = oEffFrom
		}

		cmpEnd := oEffTo.cmp(dEffTo)
		if cmpEnd == 0 {
			// Same end → compare scopes, advance both.
			if oEffScope == dEffScope {
				if opts.EmitUnchanged {
					emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeUnchanged, From: overlapStart, To: oEffTo, ScopeID: oEffScope})
				}
				counters.Unchanged++
			} else {
				keepScope := dEffScope
				if opts.Combine != nil {
					keepScope = opts.Combine(oEffScope, dEffScope)
				}
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeChanged, From: overlapStart, To: oEffTo, ScopeID: keepScope, OldScopeID: oEffScope, HasOld: true})
				if keepScope != oEffScope {
					if err := w.Set(overlapStart, oEffTo, keepScope); err != nil {
						return nil, err
					}
				}
				counters.Changed++
			}
			counters.OldScanned++
			counters.DesiredScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
		} else if cmpEnd < 0 {
			// Old ends first → overlap [overlap_start, oEffTo], then desired continues.
			if oEffScope == dEffScope {
				if opts.EmitUnchanged {
					emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeUnchanged, From: overlapStart, To: oEffTo, ScopeID: oEffScope})
				}
				counters.Unchanged++
			} else {
				keepScope := dEffScope
				if opts.Combine != nil {
					keepScope = opts.Combine(oEffScope, dEffScope)
				}
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeChanged, From: overlapStart, To: oEffTo, ScopeID: keepScope, OldScopeID: oEffScope, HasOld: true})
				if keepScope != oEffScope {
					if err := w.Set(overlapStart, oEffTo, keepScope); err != nil {
						return nil, err
					}
				}
				counters.Changed++
			}
			counters.OldScanned++
			// Advance old, trim desired's start.
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			// Trim desired start to oEffTo+1.
			trimNext, ok := oEffTo.checkedInc()
			if !ok {
				// Desired fully consumed.
				desired.Next()
				desRec = desired.Peek()
				hasDes = desRec != nil
				if hasDes {
					desTrim = desRec.From
					desTrimOk = true
				} else {
					desTrimOk = false
				}
			} else {
				desTrim = trimNext
				desTrimOk = true
				if desTrim.cmp(dEffTo) > 0 {
					// Trimmed past desired end → advance.
					desired.Next()
					desRec = desired.Peek()
					hasDes = desRec != nil
					if hasDes {
						desTrim = desRec.From
						desTrimOk = true
					} else {
						desTrimOk = false
					}
				}
			}
		} else {
			// Desired ends first → overlap [overlap_start, dEffTo], then old continues.
			if oEffScope == dEffScope {
				if opts.EmitUnchanged {
					emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeUnchanged, From: overlapStart, To: dEffTo, ScopeID: oEffScope})
				}
				counters.Unchanged++
			} else {
				keepScope := dEffScope
				if opts.Combine != nil {
					keepScope = opts.Combine(oEffScope, dEffScope)
				}
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeChanged, From: overlapStart, To: dEffTo, ScopeID: keepScope, OldScopeID: oEffScope, HasOld: true})
				if keepScope != oEffScope {
					if err := w.Set(overlapStart, dEffTo, keepScope); err != nil {
						return nil, err
					}
				}
				counters.Changed++
			}
			counters.DesiredScanned++
			// Advance desired, trim old's start.
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			// Trim old start to dEffTo+1.
			trimNext, ok := dEffTo.checkedInc()
			if !ok {
				// Old fully consumed.
				oldFrom, oldTo, oldScope, hasOld = walker.advance()
				if hasOld {
					oldTrim = oldFrom
					oldTrimOk = true
				} else {
					oldTrimOk = false
				}
			} else {
				oldTrim = trimNext
				oldTrimOk = true
				if oldTrim.cmp(oEffTo) > 0 {
					// Trimmed past old end → advance.
					oldFrom, oldTo, oldScope, hasOld = walker.advance()
					if hasOld {
						oldTrim = oldFrom
						oldTrimOk = true
					} else {
						oldTrimOk = false
					}
				}
			}
		}
	}

	return counters, nil
}

func emitChangeGo[K ipKey[K]](opts *MigrateOptions[K], ev *ChangeEvent[K]) {
	if opts.OnChange != nil {
		opts.OnChange(ev)
	}
}

// MigrateRetention is like Migrate but preserves the older scopeID on a scope
// mismatch instead of overwriting it.
//
// For Mode 0 (retention/timestamp) databases, scopeID is a unix timestamp. The
// correct merge semantics is "keep min(old, new)" — the older timestamp wins,
// so a record is only rewritten when the desired stream carries an older
// timestamp than what is already stored.
func MigrateRetention[K ipKey[K]](w *Writer[K], desired DesiredStream[K]) (*MigrateCounters, error) {
	opts := &MigrateOptions[K]{
		Combine: func(oldScopeID, desiredScopeID uint32) uint32 {
			if oldScopeID < desiredScopeID {
				return oldScopeID
			}
			return desiredScopeID
		},
	}
	return Migrate[K](w, desired, opts)
}

// --- streaming in-order B+tree walker ---
//
// Walks the committed tree in key order using a fixed-size path stack — zero
// heap allocation per record. Reads committed pages directly from the store
// (safe because the bitset-based COW model never modifies committed pages
// in-place). No full-DB heap copy (fixes #9).

type pathEntry struct {
	pgno uint32
	idx  int
}

type treeWalker[K ipKey[K]] struct {
	store   pageStore
	root    uint32
	height  uint32
	kw      int
	path    [TreeHeightMax]pathEntry
	pathLen int
	curFrom  K
	curTo    K
	curScope uint32
	curOk    bool
}

func newTreeWalker[K ipKey[K]](store pageStore, kw int, root uint32, height uint32) *treeWalker[K] {
	w := &treeWalker[K]{
		store:  store,
		root:   root,
		height: height,
		kw:     kw,
	}
	if root != 0 {
		w.descendFirst(root, 1)
	}
	return w
}

func (w *treeWalker[K]) page(pgno uint32) []byte {
	return w.store.page(pgno)
}

func (w *treeWalker[K]) peek() (K, K, uint32, bool) {
	return w.curFrom, w.curTo, w.curScope, w.curOk
}

func (w *treeWalker[K]) descendFirst(pgno uint32, depth uint32) {
	var zero K
	page := w.page(pgno)
	h := decodeHeader(page)
	if h.pageType == PageTypeLeaf {
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
