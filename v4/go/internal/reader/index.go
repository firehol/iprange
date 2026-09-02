package reader

// Numeric feed-catalog access (Rust feed_catalog.rs parity): exact lookup
// by feed index and the forward index cursor. The name tree remains the
// primary name lookup; the index tree serves point matches and iteration.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// LookupFeedIndex resolves one catalog entry by numeric feed index.
// Probes read the fixed 4-byte first-index key only; the selected record
// is decoded once and validated against the declared namespace limit
// (mirroring feed_catalog.rs lookup_index + decode_leaf).
func (r *ImmutableReader) LookupFeedIndex(index uint32) (FeedEntry, bool, error) {
	work.CatalogLookup(1)
	work.TreeLookup(1) // charged before the root shortcut (Rust fixed_tree::query)
	root := r.meta.CatalogIndexRoot
	if root == 0 {
		return FeedEntry{}, false, nil
	}
	cur := root
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return FeedEntry{}, false, err
		}
		h, err := format.ParseTreeHeader(page, r.meta.TxnID, format.PageTypeCatalogIndexBranch, format.PageTypeCatalogIndexLeaf, 0, level, !first)
		if err != nil {
			return FeedEntry{}, false, err
		}
		if first {
			level = h.Level
			first = false
		}
		sl := format.SlottedPage{Page: page, Header: h}
		if h.Level == 0 {
			return indexLeafLookup(sl, index, r.meta.FeedIndexLimit)
		}
		if level == 0 {
			return FeedEntry{}, false, corrupt("zero-level index branch")
		}
		child, err := indexBranchChild(sl, index, r.meta.PageCount)
		if err != nil {
			return FeedEntry{}, false, err
		}
		if child == 0 {
			return FeedEntry{}, false, nil // no entry qualifies: absent
		}
		work.TreeDescent(1)
		cur, level = child, level-1
	}
}

// indexBranchChild finds the greatest branch entry whose first index is
// <= target. Probes read the fixed 4-byte key only; the selected entry is
// decoded once and its child validated (mirroring nameBranchChild).
func indexBranchChild(sl format.SlottedPage, target uint32, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		if len(b) < format.CatalogIndexBranchSize {
			return 0, corrupt("short index branch record %d", len(b))
		}
		return cmpU32(format.U32(b[0:4]), target), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	rec, err := format.DecodeCatalogIndexBranch(b)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("index child out of range")
	}
	return rec.Child, nil
}

// indexLeafLookup finds the exact index in one index leaf. Probes decode
// the record shape and compare the feed index; only the selected record
// is checked against feedIndexLimit (mirroring decode_leaf).
func indexLeafLookup(sl format.SlottedPage, target uint32, feedIndexLimit uint64) (FeedEntry, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		if len(b) < 12 {
			return 0, corrupt("short catalog record %d", len(b))
		}
		return cmpU32(format.U32(b[4:8]), target), nil
	}
	best, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return FeedEntry{}, false, err
	}
	if !exact || best >= int(sl.Header.ItemCount) {
		return FeedEntry{}, false, nil
	}
	b, err := sl.Record(best)
	if err != nil {
		return FeedEntry{}, false, err
	}
	work.LeafValidation(1)
	rec, err := format.DecodeCatalogNameRecord(b)
	if err != nil {
		return FeedEntry{}, false, err
	}
	if uint64(rec.FeedIndex) >= feedIndexLimit {
		return FeedEntry{}, false, corrupt("catalog feed index %d beyond limit %d", rec.FeedIndex, feedIndexLimit)
	}
	return FeedEntry{FeedIndex: rec.FeedIndex, Name: rec.Name}, true, nil
}

// FeedCursor is the forward cursor over the numeric catalog tree (Rust
// FeedCursor parity: strictly increasing indexes, declared
// count enforcement, namespace limit checks).
type FeedCursor struct {
	r        *ImmutableReader
	state    *treeCursor
	emitted  uint64
	previous uint32
	// seeked records that the cursor was repositioned with SeekByIndex,
	// so the emitted count no longer covers the whole catalog and the
	// full-sweep count health check does not apply (Rust seeked flag).
	seeked   bool
	finished bool
}

// newFeedCursorIndex opens the forward index cursor over the active
// catalog. Work counter parity: catalog_lookup(1) at construction.
func (r *ImmutableReader) NewFeedCursor() (*FeedCursor, error) {
	state, err := r.newTreeCursor(r.meta.CatalogIndexRoot, cursorForward, format.PageTypeCatalogIndexBranch, format.PageTypeCatalogIndexLeaf, 0)
	if err != nil {
		return nil, err
	}
	return &FeedCursor{r: r, state: state, finished: state.finished}, nil
}

func decodeIndexBranch(b []byte) (uint32, error) {
	rec, err := format.DecodeCatalogIndexBranch(b)
	if err != nil {
		return 0, err
	}
	return rec.Child, nil
}

// Next returns the next catalog entry in ascending feed-index order; ok
// reports whether an entry was produced. Mirrors feed_catalog.rs
// next_inner: strictly increasing indexes, count and limit validation,
// and the incomplete-count corruption check at exhaustion.
func (c *FeedCursor) Next() (FeedEntry, bool, error) {
	if c.finished {
		return FeedEntry{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		c.finished = true
		return FeedEntry{}, false, err
	}
	rec, err := indexLeafAt(sl, c.state.index)
	if err != nil {
		c.finished = true
		return FeedEntry{}, false, err
	}
	if uint64(rec.FeedIndex) >= c.r.meta.FeedIndexLimit {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed index is outside the declared limit")
	}
	if c.emitted > 0 && c.previous >= rec.FeedIndex {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed indexes are not strictly increasing")
	}
	c.emitted++
	if c.emitted > c.r.meta.ActiveFeedCount {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed catalog exceeds its declared count")
	}
	c.previous = rec.FeedIndex
	if _, _, err := c.state.advance(); err != nil {
		c.finished = true
		return FeedEntry{}, false, err
	}
	if c.state.finished {
		c.finished = true
		if !c.seeked && c.emitted != c.r.meta.ActiveFeedCount {
			return FeedEntry{}, false, corrupt("feed catalog count is incomplete")
		}
	}
	return rec, true, nil
}

// indexLeafAt decodes one index-leaf record at position.
func indexLeafAt(sl format.SlottedPage, index int) (FeedEntry, error) {
	b, err := sl.Record(index)
	if err != nil {
		return FeedEntry{}, err
	}
	rec, err := format.DecodeCatalogNameRecord(b)
	if err != nil {
		return FeedEntry{}, err
	}
	work.LeafValidation(1)
	return FeedEntry{FeedIndex: rec.FeedIndex, Name: rec.Name}, nil
}

// SeekByIndex repositions the cursor to the first catalog entry whose
// feed index is at least target, discarding already-consumed state
// (Rust feed_catalog.rs FeedCursor::seek_by_index parity). Entries
// before the target are never revisited; subsequent Next calls continue
// from the repositioned entry. Seeking to 0 restarts a complete sweep;
// seeking past the last entry finishes the cursor. Seeks are repeatable
// on an exhausted cursor. The emitted count resets, so the full-sweep
// count health check does not apply after a seek (Rust seeked flag);
// on error the cursor finishes exactly like Rust.
func (c *FeedCursor) SeekByIndex(target uint32) error {
	c.state.seek4 = target
	if err := c.state.seekPosition(); err != nil {
		c.finished = true
		return err
	}
	if c.state.finished {
		c.emitted = 0
		c.previous = 0
		c.seeked = target != 0
		c.finished = true
		return nil
	}
	if c.state.index >= int(c.state.itemCount) {
		// The leaf policy selected a position past the leaf end: cross
		// into the sibling leaf (Rust SeekPosition::NextLeaf); a target
		// beyond the last leaf finishes the cursor.
		c.state.index = int(c.state.itemCount) - 1
		if _, _, err := c.state.advance(); err != nil {
			c.finished = true
			return err
		}
	}
	c.emitted = 0
	c.previous = 0
	c.seeked = target != 0
	c.finished = c.state.finished
	return nil
}

// indexBranchSeek4 selects the catalog index branch child for one seek
// step, mirroring Rust fixed_tree::Cursor::seek_inner with the u32
// first-index key and rangeBranchSeek4's convention: greatest
// first-index <= target; a forward seek below the first key descends
// into the first child and a backward seek below the first key
// finishes. The selected child is validated against the page limit
// (Rust branch_child/require_child).
func indexBranchSeek4(sl format.SlottedPage, target uint32, dir cursorDir, pageLimit uint64) (int, uint32, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		if len(b) < format.CatalogIndexBranchSize {
			return 0, corrupt("short index branch record %d", len(b))
		}
		return cmpU32(format.U32(b[0:4]), target), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, 0, false, err
	}
	index := position
	if !exact {
		index--
	}
	if index < 0 {
		if dir == cursorBackward {
			return 0, 0, true, nil // nothing at or below the target
		}
		index = 0 // forward seek below the first key: first child
	}
	b, err := sl.Record(index)
	if err != nil {
		return 0, 0, false, err
	}
	_, child, err := format.DecodeCatalogIndexBranchFields(b)
	if err != nil {
		return 0, 0, false, err
	}
	if !format.PageNumberValid(child, pageLimit) {
		return 0, 0, false, corrupt("catalog index branch child page %d is invalid", child)
	}
	return index, child, false, nil
}

// indexLeafSeek4 implements the Rust catalog at-or-after leaf policy
// (feed_catalog.rs AtOrAfterIndex over the feed-index keys): forward
// returns the first entry with FeedIndex >= target, or reports a
// position past the leaf end so the seek caller crosses into the
// sibling leaf; a backward seek returns the greatest entry with
// FeedIndex <= target (only reachable by internal misuse: the catalog
// cursor is forward-only in both engines).
func indexLeafSeek4(sl format.SlottedPage, target uint32, dir cursorDir) (int, bool, error) {
	cmp := func(i int) (int, error) {
		key, err := indexLeafKey(sl, i)
		if err != nil {
			return 0, err
		}
		return cmpU32(key, target), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, false, err
	}
	if dir == cursorBackward {
		if exact {
			return position, false, nil
		}
		if position == 0 {
			return 0, true, nil // finished: nothing at or below
		}
		return position - 1, false, nil
	}
	// Forward at-or-after: the first key >= target, or a position past
	// the leaf end when the target lies beyond every cell of this leaf
	// (the seek caller crosses to the next leaf; a target beyond the
	// last leaf finishes the cursor).
	return position, false, nil
}

// indexLeafKey decodes the feed-index key of one index-leaf record
// without validating the name span (probe path; the selected record is
// fully decoded once by indexLeafAt). Mirrors indexLeafLookup's key
// probe.
func indexLeafKey(sl format.SlottedPage, pos int) (uint32, error) {
	b, err := sl.Record(pos)
	if err != nil {
		return 0, err
	}
	if len(b) < 12 {
		return 0, corrupt("short catalog record %d", len(b))
	}
	return format.U32(b[4:8]), nil
}
