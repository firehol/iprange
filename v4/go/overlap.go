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
			feeds := getFeedsForScope[K](w, lv.recordScopeID(i))
			for a := 0; a < len(feeds); a++ {
				for b := a + 1; b < len(feeds); b++ {
					onOverlap(FeedOverlap{
						FeedA:   feeds[a],
						FeedB:   feeds[b],
						IPCount: ipCount,
					})
				}
			}
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

// ForeignVsAll compares a foreign feed against all stored feeds. For each
// foreign range, it descends the tree (binary-search pruned) to find
// overlapping records and reports which feeds cover the overlap region.
//
// onOverlap receives (feed, foreignID, ipCount) for every overlapping feed.
// foreignID is 0 (the foreign feed marker).
func ForeignVsAll[K ipKey[K]](w *Writer[K], foreign []ForeignRange[K], onOverlap func(feed, foreignID uint32, ipCount uint64)) error {
	if w.pendingRoot == 0 {
		return nil
	}
	for _, fr := range foreign {
		var overlaps []overlapRecord[K]
		if err := collectOverlappingWriter[K](w, w.pendingRoot, fr.From, fr.To, &overlaps); err != nil {
			return err
		}
		for _, o := range overlaps {
			overlapFrom := fr.From
			if o.from.cmp(fr.From) > 0 {
				overlapFrom = o.from
			}
			overlapTo := fr.To
			if o.to.cmp(fr.To) < 0 {
				overlapTo = o.to
			}
			ipCount := ipRangeCount[K](overlapFrom, overlapTo)
			feeds := getFeedsForScope[K](w, o.scope)
			for _, feed := range feeds {
				onOverlap(feed, 0, ipCount)
			}
		}
	}
	return nil
}

// collectOverlappingWriter gathers all pending records overlapping [from, to],
// using binary-search pruned branch descent (like the Rust
// collect_overlapping_writer).
func collectOverlappingWriter[K ipKey[K]](w *Writer[K], pgno uint32, from, to K, out *[]overlapRecord[K]) error {
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
		start := branchFindChild[K](&bv, from)
		for j := start; j < bv.childCount(); j++ {
			if j > 0 {
				sep := zero.readLE(bv.sep(j - 1))
				if sep.cmp(to) > 0 {
					return nil
				}
			}
			if err := collectOverlappingWriter[K](w, bv.child(j), from, to, out); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unexpected page type %d", h.pageType)
	}
}

// getFeedsForScope resolves a scope_id to the list of feed bits it represents.
func getFeedsForScope[K ipKey[K]](w *Writer[K], scopeID uint32) []uint32 {
	switch w.scopeMode {
	case ScopeModeBitmap:
		// scope_id IS the bitmap — extract set bits.
		var feeds []uint32
		remaining := scopeID
		for bit := uint32(0); remaining != 0; bit++ {
			if remaining&1 != 0 {
				feeds = append(feeds, bit)
			}
			remaining >>= 1
		}
		return feeds
	case ScopeModeIndirect:
		// Resolve via the scope registry (writer-side).
		bitmap := w.ScopeResolve(scopeID)
		if bitmap == nil {
			return nil
		}
		var feeds []uint32
		for byteIdx, by := range bitmap {
			rb := by
			for bitInByte := uint32(0); rb != 0; bitInByte++ {
				if rb&1 != 0 {
					feeds = append(feeds, uint32(byteIdx)*8+bitInByte)
				}
				rb >>= 1
			}
		}
		return feeds
	default:
		return nil
	}
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
