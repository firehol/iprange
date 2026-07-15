package iprangedb

import (
	"fmt"
	"math/bits"
	"sort"
)

// All-to-all feed overlap accumulation.
//
// Scans a multi-feed file (mode 1 or mode 2) in a single pass, computing the
// pairwise overlap matrix between all feeds. For each record [from, to,
// scope_id], the scope is resolved to feed bits and every (feed_a, feed_b) pair
// is emitted via the callback.
//
// Heap discipline: both overlap scans run with heap that is FLAT in the stored
// record / leaf count. Leaf iteration uses an O(tree-height) cursor (never a
// materialized slice of leaf page numbers), and overflow scope bitmaps are
// resolved once and cached per operation (never per record). Mirrors the Rust
// overlap.rs.

// FeedOverlap is a pairwise overlap result: feeds A and B share IPCount addresses.
type FeedOverlap struct {
	FeedA   uint32
	FeedB   uint32
	IPCount uint64
}

// ForeignRange is a single IP range from a foreign (external) feed.
type ForeignRange[K ipKey[K]] struct {
	From K
	To   K
}

// AllToAllOverlap scans all records and computes the pairwise feed overlap
// matrix. onOverlap is called for every (feed_a, feed_b) pair (feed_a < feed_b)
// that shares at least one IP address. IPCount is the total addresses shared.
//
// Mode 1 (bitmap): each record's scope_id bits are the feeds.
// Mode 2 (indirect): each scope_id is resolved to a bitmap via the scope registry.
// Overflow (multi-page) scopes are resolved once and cached for the scan.
//
// Single pass: O(records x avg_feeds_per_record^2).
func AllToAllOverlap[K ipKey[K]](w *Writer[K], onOverlap func(FeedOverlap)) error {
	if w.scopeMode == ScopeModeScalar {
		return fmt.Errorf("overlap requires bitmap or indirect scope mode")
	}
	if w.pendingRoot == 0 {
		return nil
	}
	// Aggregate each (a, b) feed pair's contribution across all covering records
	// into one total, emitted once per pair (overflow-checked).
	totals := make(map[uint64]uint64)
	var order []uint64
	overflow := false
	emit := func(a, b, ipCount uint64) {
		key := a<<32 | b
		if _, ok := totals[key]; !ok {
			order = append(order, key)
		}
		var carry uint64
		totals[key], carry = bits.Add64(totals[key], ipCount, 0)
		if carry != 0 {
			overflow = true
		}
	}
	// One cache shared across the whole scan: each distinct overflow scope is
	// decoded exactly once regardless of how many records reference it.
	cache := &scopeCache{}
	if err := scanOverlapNode[K](w, w.pendingRoot, cache, func(o FeedOverlap) {
		emit(uint64(o.FeedA), uint64(o.FeedB), o.IPCount)
	}); err != nil {
		return err
	}
	if overflow {
		return fmt.Errorf("accumulated overlap pair count exceeds uint64")
	}
	sort.Slice(order, func(i, j int) bool {
		return order[i] < order[j]
	})
	for _, key := range order {
		onOverlap(FeedOverlap{
			FeedA:   uint32(key >> 32),
			FeedB:   uint32(key),
			IPCount: totals[key],
		})
	}
	return nil
}

func scanOverlapNode[K ipKey[K]](w *Writer[K], pgno uint32, cache *scopeCache, onOverlap func(FeedOverlap)) error {
	var zero K
	kw := zero.width()
	page := w.store.page(pgno)
	h := decodeHeader(page)
	switch h.pageType {
	case PageTypeLeaf:
		lv := newLeafView(page, int(h.entryCount), kw)
		for i := 0; i < lv.len(); i++ {
			rf := zero.readLE(lv.recordFrom(i))
			rt := zero.readLE(lv.recordTo(i))
			ipCount, ok := ipRangeCount[K](rf, rt)
			if !ok {
				return fmt.Errorf("ip range count exceeds uint64")
			}
			// Iterate feed pairs directly from the scope bitmap — no per-record
			// slice allocation. Overflow scopes are resolved via the shared
			// cache (decode-once). Emits every (a, b) pair with a < b.
			forEachFeedPair[K](w, lv.recordScopeID(i), cache, func(a, b uint32) {
				onOverlap(FeedOverlap{
					FeedA:   a,
					FeedB:   b,
					IPCount: ipCount,
				})
			})
		}
		return nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		for j := 0; j < bv.childCount(); j++ {
			if err := scanOverlapNode[K](w, bv.child(j), cache, onOverlap); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

// ForeignVsAll compares a foreign feed against all stored feeds. The foreign
// ranges are streamed via nextForeign: each call returns the next (from, to) and
// true, or a zero pair and false when exhausted. This lets a caller feed ranges
// from a file/iterator without materializing a slice (issue-4 fix).
//
// onOverlap receives (feed, foreignID, ipCount) for every overlapping feed.
// foreignID is always 0 (the foreign feed marker).
//
// Precondition: the foreign ranges MUST be yielded sorted ascending by `from`.
// Both inputs (foreign feed + stored tree records) are then sorted, so a
// single-pass linear merge replaces the per-range B+tree descent (issue-5).
// The old implementation walked the tree once per foreign range —
// O(foreign × tree_height); this is O(tree_pages + foreign) + overlap output.
// Unsorted foreign input would silently under-count overlaps.
//
// Tree records are streamed leaf-by-leaf via an O(tree-height) leafCursor — no
// materialized leaf-page list — so heap is flat in the leaf count. Overflow
// scopes are resolved once via a per-operation cache (flat in record count).
func ForeignVsAll[K ipKey[K]](w *Writer[K], nextForeign func() (K, K, bool), onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
	if w.scopeMode == ScopeModeScalar {
		for {
			_, _, ok := nextForeign()
			if !ok {
				break
			}
		}
		return fmt.Errorf("overlap requires bitmap or indirect scope mode")
	}
	if w.pendingRoot == 0 {
		// Drain the stream so the caller's iterator state is fully consumed
		// even when there is nothing to compare against.
		for {
			_, _, ok := nextForeign()
			if !ok {
				break
			}
		}
		return nil
	}

	// Permanent cursor over tree records — O(tree height) state, never a
	// materialized leaf-page slice. Only advances forward across foreign
	// ranges: records ending before the current foreign range's `from` can
	// never overlap it or any later (sorted) range.
	cursor := newLeafCursor[K](w.pendingRoot, w.store)
	cache := &scopeCache{}

	// The single-pass linear merge REQUIRES the foreign ranges to be sorted
	// ascending by `from` and pairwise disjoint. Track the previous range's end
	// and reject unsorted/overlapping/reversed input.
	var prevTo K
	prevOk := false

	for {
		from, to, ok := nextForeign()
		if !ok {
			return nil
		}
		if from.cmp(to) > 0 {
			return fmt.Errorf("foreign range has from > to")
		}
		if prevOk && from.cmp(prevTo) <= 0 {
			return fmt.Errorf("foreign ranges must be sorted and disjoint")
		}
		prevTo = to
		prevOk = true
		// Phase 1 — permanently skip records ending strictly before `from`.
		for {
			_, recTo, _, rok := cursor.currentRecord(w.store)
			if !rok {
				break
			}
			if recTo.cmp(from) >= 0 {
				break
			}
			cursor.advance(w.store)
		}
		// Phase 2 — scan forward from a COPY of the permanent cursor, emitting
		// overlaps, until a record starts after `to`. The scan cursor is a copy:
		// records that overlap this foreign range may also overlap the next, so
		// the permanent cursor is not advanced past them.
		scan := cursor
		for {
			recFrom, recTo, scopeID, rok := scan.currentRecord(w.store)
			if !rok {
				break
			}
			if recFrom.cmp(to) > 0 {
				break
			}
			// Overlap is guaranteed: recTo >= from (phase 1) and recFrom <= to.
			overlapFrom := from
			if recFrom.cmp(from) > 0 {
				overlapFrom = recFrom
			}
			overlapTo := to
			if recTo.cmp(to) < 0 {
				overlapTo = recTo
			}
			ipCount, countOk := ipRangeCount[K](overlapFrom, overlapTo)
			if !countOk {
				return fmt.Errorf("ip range count exceeds uint64")
			}
			forEachFeed[K](w, scopeID, cache, func(feed uint32) {
				onOverlap(feed, 0, ipCount)
			})
			scan.advance(w.store)
		}
	}
}

// ForeignVsAllFromSlice is a convenience wrapper around ForeignVsAll that takes
// a materialized slice of foreign ranges (backward-compatible shape).
func ForeignVsAllFromSlice[K ipKey[K]](w *Writer[K], foreign []ForeignRange[K], onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
	i := 0
	return ForeignVsAll[K](w, func() (K, K, bool) {
		if i >= len(foreign) {
			var zero K
			return zero, zero, false
		}
		r := foreign[i]
		i++
		return r.From, r.To, true
	}, onOverlap)
}

// leafFrame is one stack frame: the branch page number and the child index
// currently being visited within it.
type leafFrame struct {
	pgno     uint32
	childIdx uint16
}

// leafStackCap bounds the cursor stack depth. The tree height is bounded by
// TreeHeightMax (32); a height-H tree has H-1 branch levels, so 32 frames
// always suffice.
const leafStackCap = TreeHeightMax

// leafCursor is an in-order leaf traversal of the pending B+tree using O(tree
// height) state. It replaces the previous pendingLeafPages() materialization,
// which pushed every leaf page number into a []uint32 — O(leaf count) heap that
// grows with the database. The cursor keeps a stack of (branch_pgno,
// child_index) frames (at most leafStackCap), so its heap footprint is a
// fixed-size constant regardless of how many leaves the tree holds.
//
// Mirrors Rust LeafCursor (overlap.rs:263). Stored by value: the scan phase
// copies it (scan := cursor) the way Rust relies on Copy.
type leafCursor[K ipKey[K]] struct {
	// Path from the root: each frame is the branch page and the child index
	// currently being visited.
	stack [leafStackCap]leafFrame
	// depth is the number of valid frames in stack.
	depth   int
	curLeaf uint32
	recIdx  uint16
	valid   bool
}

// newLeafCursor descends from root to its leftmost leaf. root MUST be non-zero
// and point at a well-formed tree (caller checks pendingRoot != 0).
func newLeafCursor[K ipKey[K]](root uint32, store pageStore) leafCursor[K] {
	var zero K
	kw := zero.width()
	var c leafCursor[K]
	pgno := root
	for {
		page := store.page(pgno)
		h := decodeHeader(page)
		if h.pageType == PageTypeLeaf {
			break
		}
		// Branch: record this frame and descend into the leftmost child.
		if c.depth >= leafStackCap {
			break
		}
		bv := newBranchView(page, int(h.entryCount), kw)
		c.stack[c.depth] = leafFrame{pgno: pgno, childIdx: 0}
		c.depth++
		pgno = bv.child(0)
	}
	c.curLeaf = pgno
	c.recIdx = 0
	c.valid = true
	return c
}

// currentRecord returns the record at the current cursor position as owned key
// values (no store borrow escapes the call), or the zero value and false past
// the last leaf.
func (c *leafCursor[K]) currentRecord(store pageStore) (K, K, uint32, bool) {
	var zero K
	if !c.valid {
		return zero, zero, 0, false
	}
	kw := zero.width()
	page := store.page(c.curLeaf)
	h := decodeHeader(page)
	count := int(h.entryCount)
	if int(c.recIdx) >= count {
		return zero, zero, 0, false
	}
	lv := newLeafView(page, count, kw)
	rf := zero.readLE(lv.recordFrom(int(c.recIdx)))
	rt := zero.readLE(lv.recordTo(int(c.recIdx)))
	return rf, rt, lv.recordScopeID(int(c.recIdx)), true
}

// advance moves forward by one record, crossing leaf boundaries. O(height)
// amortized over a full scan: each page is pushed/popped a constant number of
// times.
func (c *leafCursor[K]) advance(store pageStore) {
	if !c.valid {
		return
	}
	c.recIdx++
	page := store.page(c.curLeaf)
	h := decodeHeader(page)
	if int(c.recIdx) < int(h.entryCount) {
		return
	}
	c.descendToNextLeaf(store)
}

// descendToNextLeaf moves from the end of the current leaf to the first record
// of the next leaf in key order, popping the ancestor stack until a branch has
// an unvisited child. Sets valid = false when no more leaves remain.
func (c *leafCursor[K]) descendToNextLeaf(store pageStore) {
	var zero K
	kw := zero.width()
	for c.depth > 0 {
		c.depth--
		frame := c.stack[c.depth]
		nextChild := frame.childIdx + 1
		page := store.page(frame.pgno)
		h := decodeHeader(page)
		bv := newBranchView(page, int(h.entryCount), kw)
		if int(nextChild) < bv.childCount() {
			c.stack[c.depth] = leafFrame{pgno: frame.pgno, childIdx: nextChild}
			c.depth++
			pgno := bv.child(int(nextChild))
			for {
				dpage := store.page(pgno)
				dh := decodeHeader(dpage)
				if dh.pageType == PageTypeLeaf {
					c.curLeaf = pgno
					c.recIdx = 0
					return
				}
				if c.depth >= leafStackCap {
					c.valid = false
					return
				}
				dbv := newBranchView(dpage, int(dh.entryCount), kw)
				c.stack[c.depth] = leafFrame{pgno: pgno, childIdx: 0}
				c.depth++
				pgno = dbv.child(0)
			}
		}
	}
	c.valid = false
}

// scopeCacheEntry is one cached overflow-scope bitmap.
type scopeCacheEntry struct {
	id     uint32
	bitmap []byte
}

// scopeCache is a resolve-once cache for overflow (multi-page) scope bitmaps
// within a single overlap operation.
//
// ScopeResolveRef returns nil for overflow scopes (they span pages and cannot
// be returned as one borrowed slice). Without a cache, every record referencing
// such a scope would pay a fresh []byte decode — O(records × bitmap_size) heap
// that grows with the record count. Since a scope's bitmap is immutable within
// a transaction, each distinct overflow scope need only be decoded once.
//
// Heap is bounded by the number of DISTINCT overflow scopes (typically 1–5),
// which is independent of the record count — flat. Mirrors Rust ScopeCache
// (overlap.rs:226).
type scopeCache struct {
	entries []scopeCacheEntry
}

// scopeCacheOverflowBitmap returns the overflow bitmap for scopeID, decoding it
// on first access and reusing the cached copy thereafter. Only call this after
// ScopeResolveRef has already returned nil (i.e. this is an overflow scope).
// Returns nil for an unknown id (treated as an empty bitmap by the callers).
func scopeCacheOverflowBitmap[K ipKey[K]](c *scopeCache, w *Writer[K], scopeID uint32) []byte {
	for i := range c.entries {
		if c.entries[i].id == scopeID {
			return c.entries[i].bitmap
		}
	}
	bitmap := w.ScopeResolve(scopeID)
	c.entries = append(c.entries, scopeCacheEntry{id: scopeID, bitmap: bitmap})
	return c.entries[len(c.entries)-1].bitmap
}

// forEachFeedPair iterates every ordered feed pair (a, b) with a < b covered by
// scopeID, invoking onPair(a, b) for each. Avoids materializing a feed slice.
//
// Bitmap mode: scopeID IS the bitmap; walk set bits with x & (x-1).
// Indirect mode: resolve to the bitmap byte slice — zero-copy ref for inline
// scopes (issue-6), decode-once-via-cache for overflow scopes — and scan it
// directly. No per-record slice allocation.
func forEachFeedPair[K ipKey[K]](w *Writer[K], scopeID uint32, cache *scopeCache, onPair func(a, b uint32)) {
	switch w.scopeMode {
	case ScopeModeBitmap:
		// outer always holds the bits strictly greater than the current a;
		// after clearing a, the remaining bits form the inner iteration set,
		// so every emitted pair satisfies a < b.
		outer := scopeID
		for outer != 0 {
			a := uint32(bits.TrailingZeros32(outer))
			outer &= outer - 1
			inner := outer
			for inner != 0 {
				b := uint32(bits.TrailingZeros32(inner))
				inner &= inner - 1
				onPair(a, b)
			}
		}
	case ScopeModeIndirect:
		bitmap := w.ScopeResolveRef(scopeID)
		if bitmap == nil {
			// Overflow scope: resolve once via the per-operation cache so the
			// same multi-page bitmap is never re-decoded per record.
			bitmap = scopeCacheOverflowBitmap[K](cache, w, scopeID)
		}
		forEachSetBitPair(bitmap, onPair)
	}
}

// forEachFeed iterates every set feed bit in scopeID, invoking onFeed(bit).
func forEachFeed[K ipKey[K]](w *Writer[K], scopeID uint32, cache *scopeCache, onFeed func(bit uint32)) {
	switch w.scopeMode {
	case ScopeModeBitmap:
		remaining := scopeID
		for remaining != 0 {
			bit := uint32(bits.TrailingZeros32(remaining))
			remaining &= remaining - 1
			onFeed(bit)
		}
	case ScopeModeIndirect:
		// Zero-copy ref (issue-6) for inline scopes; decode-once-via-cache for
		// overflow scopes.
		bitmap := w.ScopeResolveRef(scopeID)
		if bitmap == nil {
			bitmap = scopeCacheOverflowBitmap[K](cache, w, scopeID)
		}
		forEachSetBit(bitmap, onFeed)
	}
}

// forEachSetBit walks set bits of a byte slice, invoking onFeed(absouluteBit).
func forEachSetBit(bitmap []byte, onFeed func(bit uint32)) {
	for byteIdx, by := range bitmap {
		b := by
		for b != 0 {
			bitInByte := uint32(bits.TrailingZeros8(b))
			b &= b - 1
			onFeed(uint32(byteIdx)*8 + bitInByte)
		}
	}
}

// forEachSetBitPair walks every ordered pair (a, b) with a < b over the set bits
// of bitmap. Two-cursor scan over the byte slice — zero allocation, works for
// any bitmap width (indirect mode supports unlimited feeds).
func forEachSetBitPair(bitmap []byte, onPair func(a, b uint32)) {
	aPos := 0
	for {
		a, ok := nextSetBitFrom(bitmap, aPos)
		if !ok {
			return
		}
		bPos := int(a) + 1
		for {
			b, ok := nextSetBitFrom(bitmap, bPos)
			if !ok {
				break
			}
			onPair(a, b)
			bPos = int(b) + 1
		}
		aPos = int(a) + 1
	}
}

// nextSetBitFrom returns the absolute position of the first set bit at or after
// start, or (0, false) if no such bit exists.
func nextSetBitFrom(bitmap []byte, start int) (uint32, bool) {
	byteIdx := start >> 3
	if byteIdx >= len(bitmap) {
		return 0, false
	}
	bitInByte := uint(start & 7)
	// First byte: ignore bits below the requested offset.
	first := bitmap[byteIdx] & byte(0xFF<<bitInByte)
	if first != 0 {
		return uint32(byteIdx)*8 + uint32(bits.TrailingZeros8(first)), true
	}
	byteIdx++
	for byteIdx < len(bitmap) {
		b := bitmap[byteIdx]
		if b != 0 {
			return uint32(byteIdx)*8 + uint32(bits.TrailingZeros8(b)), true
		}
		byteIdx++
	}
	return 0, false
}

// ipRangeCount returns the number of IP addresses in [from, to] (inclusive).
// The second value is false when the span exceeds uint64 (only possible for
// IPv6), so callers can report an overflow instead of silently truncating.
func ipRangeCount[K ipKey[K]](from, to K) (uint64, bool) {
	fv := from.toU128()
	tv := to.toU128()
	// Guard: to < from → 0.
	if tv.Hi < fv.Hi {
		return 0, true
	}
	if tv.Hi == fv.Hi && tv.Lo < fv.Lo {
		return 0, true
	}
	// (to - from) as u128, then +1.
	lo, borrow := bits.Sub64(tv.Lo, fv.Lo, 0)
	hi := tv.Hi - fv.Hi - borrow
	lo2, carry := bits.Add64(lo, 1, 0)
	hi2 := hi + carry
	if hi2 != 0 {
		return 0, false // exceeds uint64
	}
	return lo2, true
}
