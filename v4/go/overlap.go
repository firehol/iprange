package iprangedb

import (
	"fmt"
	"math/bits"
)

// All-to-all feed overlap accumulation.
//
// Scans a multi-feed file (mode 1 or mode 2) in a single pass, computing the
// pairwise overlap matrix between all feeds. For each record [from, to,
// scope_id], the scope is resolved to feed bits and every (feed_a, feed_b) pair
// is emitted via the callback.
//
// Mirrors the Rust overlap.rs.

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
//
// Single pass: O(records x avg_feeds_per_record^2).
func AllToAllOverlap[K ipKey[K]](w *Writer[K], onOverlap func(FeedOverlap)) error {
	if w.pendingRoot == 0 {
		return nil
	}
	return scanOverlapNode[K](w, w.pendingRoot, onOverlap)
}

func scanOverlapNode[K ipKey[K]](w *Writer[K], pgno uint32, onOverlap func(FeedOverlap)) error {
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
			ipCount := ipRangeCount[K](rf, rt)
			// Iterate feed pairs directly from the scope bitmap — no per-record
			// slice allocation. Emits every (a, b) pair with a < b.
			forEachFeedPair[K](w, lv.recordScopeID(i), func(a, b uint32) {
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
			if err := scanOverlapNode[K](w, bv.child(j), onOverlap); err != nil {
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
func ForeignVsAll[K ipKey[K]](w *Writer[K], nextForeign func() (K, K, bool), onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
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
	for {
		from, to, ok := nextForeign()
		if !ok {
			return nil
		}
		// Callback-driven descent: no intermediate slice of overlapping records.
		if err := descendOverlapping[K](w, w.pendingRoot, from, to, func(recFrom, recTo K, scopeID uint32) {
			overlapFrom := from
			if recFrom.cmp(from) > 0 {
				overlapFrom = recFrom
			}
			overlapTo := to
			if recTo.cmp(to) < 0 {
				overlapTo = recTo
			}
			ipCount := ipRangeCount[K](overlapFrom, overlapTo)
			forEachFeed[K](w, scopeID, func(feed uint32) {
				onOverlap(feed, 0, ipCount)
			})
		}); err != nil {
			return err
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

// descendOverlapping descends the pending tree, invoking onRec for every leaf
// record that overlaps [from, to]. Binary-search pruned branch descent.
func descendOverlapping[K ipKey[K]](w *Writer[K], pgno uint32, from, to K, onRec func(recFrom, recTo K, scopeID uint32)) error {
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
				onRec(rf, rt, lv.recordScopeID(i))
			}
		}
		return nil
	case PageTypeBranch:
		bv := newBranchView(page, int(h.entryCount), kw)
		start := branchFindChild[K](&bv, from)
		for j := start; j < bv.childCount(); j++ {
			if j > 0 {
				sep := zero.readLE(bv.sep(j - 1))
				if sep.cmp(to) > 0 {
					return nil
				}
			}
			if err := descendOverlapping[K](w, bv.child(j), from, to, onRec); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

// forEachFeedPair iterates every ordered feed pair (a, b) with a < b covered by
// scopeID, invoking onPair(a, b) for each. Avoids materializing a feed slice.
//
// Bitmap mode: scopeID IS the bitmap; walk set bits with x & (x-1).
// Indirect mode: resolve to the bitmap byte slice and scan it directly.
func forEachFeedPair[K ipKey[K]](w *Writer[K], scopeID uint32, onPair func(a, b uint32)) {
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
		bitmap := w.ScopeResolve(scopeID)
		if bitmap != nil {
			forEachSetBitPair(bitmap, onPair)
		}
	}
}

// forEachFeed iterates every set feed bit in scopeID, invoking onFeed(bit).
func forEachFeed[K ipKey[K]](w *Writer[K], scopeID uint32, onFeed func(bit uint32)) {
	switch w.scopeMode {
	case ScopeModeBitmap:
		remaining := scopeID
		for remaining != 0 {
			bit := uint32(bits.TrailingZeros32(remaining))
			remaining &= remaining - 1
			onFeed(bit)
		}
	case ScopeModeIndirect:
		bitmap := w.ScopeResolve(scopeID)
		if bitmap != nil {
			forEachSetBit(bitmap, onFeed)
		}
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
// The result is truncated to u64 (matching the Rust ip_range_count).
func ipRangeCount[K ipKey[K]](from, to K) uint64 {
	fv := from.toU128()
	tv := to.toU128()
	// Guard: to < from → 0.
	if tv.Hi < fv.Hi {
		return 0
	}
	if tv.Hi == fv.Hi && tv.Lo < fv.Lo {
		return 0
	}
	// Low 64 bits of (to - from + 1).
	lo, _ := bits.Sub64(tv.Lo, fv.Lo, 0)
	result, _ := bits.Add64(lo, 1, 0)
	return result
}
