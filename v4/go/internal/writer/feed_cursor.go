// Forward catalog enumeration over one pinned generation (Rust
// feed_catalog::FeedCursor): entries arrive in ascending feed-index
// order with the Rust structural guarantees - the declared index limit,
// strict ordering, and the declared active count. The Go writer has no
// reader table until Milestone 4, so the cursor carries no owner
// identity (Rust require_owner guards the sidecar-era reader table).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// FeedCursor is one forward cursor over the catalog index tree of a
// pinned generation.
type FeedCursor struct {
	cursor      *tree.ForwardCursor[format.CatalogNameRecord]
	meta        format.Meta
	emitted     uint64
	previous    uint32
	hasPrevious bool
	finished    bool
}

// NewFeedCursor opens the forward cursor over the generation's catalog
// index tree (Rust FeedCursor::new; an absent index root is the empty
// catalog).
func NewFeedCursor(store *DraftStore, meta format.Meta) (*FeedCursor, error) {
	if meta.CatalogIndexRoot == 0 {
		return &FeedCursor{meta: meta, finished: true}, nil
	}
	cursor, err := tree.NewForwardCursor[format.CatalogNameRecord](indexCodec{}, selectedStore{store: store, meta: meta}, meta.CatalogIndexRoot, false)
	if err != nil {
		return nil, err
	}
	return &FeedCursor{cursor: cursor, meta: meta}, nil
}

// next returns the next catalog entry in ascending feed-index order, or
// ok=false at the end (Rust FeedCursor::next_feed; the exact corrupt
// classes of next_inner, with the finished flag set exactly where Rust
// sets it).
func (c *FeedCursor) Next() (FeedEntry, bool, error) {
	if c.finished {
		return FeedEntry{}, false, nil
	}
	record, ok, err := c.cursor.Next()
	if err != nil {
		c.finished = true
		return FeedEntry{}, false, err
	}
	if !ok {
		c.finished = true
		if c.emitted != c.meta.ActiveFeedCount {
			return FeedEntry{}, false, corrupt("feed catalog count is incomplete")
		}
		return FeedEntry{}, false, nil
	}
	if uint64(record.FeedIndex) >= c.meta.FeedIndexLimit {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed index is outside the declared limit")
	}
	if c.hasPrevious && c.previous >= record.FeedIndex {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed indexes are not strictly increasing")
	}
	c.previous = record.FeedIndex
	c.hasPrevious = true
	next := c.emitted + 1
	if next < c.emitted {
		return FeedEntry{}, false, overflow("feed cursor count")
	}
	c.emitted = next
	if c.emitted > c.meta.ActiveFeedCount {
		c.finished = true
		return FeedEntry{}, false, corrupt("feed catalog exceeds its declared count")
	}
	return FeedEntry{Name: string(record.Name), Index: record.FeedIndex}, true, nil
}
