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
	root := r.meta.CatalogIndexRoot
	if root == 0 {
		return FeedEntry{}, false, nil
	}
	work.TreeLookup(1)
	cur := root
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return FeedEntry{}, false, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return FeedEntry{}, false, err
		}
		if first {
			level = h.Level
			first = false
		} else if h.Level != level {
			return FeedEntry{}, false, corrupt("catalog level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
		if err != nil {
			return FeedEntry{}, false, err
		}
		switch h.PageType {
		case format.PageTypeCatalogIndexBranch:
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
		case format.PageTypeCatalogIndexLeaf:
			return indexLeafLookup(sl, index, r.meta.FeedIndexLimit)
		default:
			return FeedEntry{}, false, corrupt("unexpected index page type %d", h.PageType)
		}
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
		if c.emitted != c.r.meta.ActiveFeedCount {
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
