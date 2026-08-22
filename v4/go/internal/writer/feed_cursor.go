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

// feedCursor is one forward cursor over the catalog index tree of a
// pinned generation.
type feedCursor struct {
	cursor      *tree.ForwardCursor[format.CatalogNameRecord]
	meta        format.Meta
	emitted     uint64
	previous    uint32
	hasPrevious bool
	finished    bool
}

// newFeedCursor opens the forward cursor over the generation's catalog
// index tree (Rust FeedCursor::new; an absent index root is the empty
// catalog).
func newFeedCursor(store *DraftStore, meta format.Meta) (*feedCursor, error) {
	if meta.CatalogIndexRoot == 0 {
		return &feedCursor{meta: meta, finished: true}, nil
	}
	cursor, err := tree.NewForwardCursor[format.CatalogNameRecord](indexCodec{}, selectedStore{store: store, meta: meta}, meta.CatalogIndexRoot, false)
	if err != nil {
		return nil, err
	}
	return &feedCursor{cursor: cursor, meta: meta}, nil
}

// next returns the next catalog entry in ascending feed-index order, or
// ok=false at the end (Rust FeedCursor::next_feed; the exact corrupt
// classes of next_inner, with the finished flag set exactly where Rust
// sets it).
func (c *feedCursor) next() (feedEntry, bool, error) {
	if c.finished {
		return feedEntry{}, false, nil
	}
	record, ok, err := c.cursor.Next()
	if err != nil {
		c.finished = true
		return feedEntry{}, false, err
	}
	if !ok {
		c.finished = true
		if c.emitted != c.meta.ActiveFeedCount {
			return feedEntry{}, false, corrupt("feed catalog count is incomplete")
		}
		return feedEntry{}, false, nil
	}
	if uint64(record.FeedIndex) >= c.meta.FeedIndexLimit {
		c.finished = true
		return feedEntry{}, false, corrupt("feed index is outside the declared limit")
	}
	if c.hasPrevious && c.previous >= record.FeedIndex {
		c.finished = true
		return feedEntry{}, false, corrupt("feed indexes are not strictly increasing")
	}
	c.previous = record.FeedIndex
	c.hasPrevious = true
	next := c.emitted + 1
	if next < c.emitted {
		return feedEntry{}, false, overflow("feed cursor count")
	}
	c.emitted = next
	if c.emitted > c.meta.ActiveFeedCount {
		c.finished = true
		return feedEntry{}, false, corrupt("feed catalog exceeds its declared count")
	}
	return feedEntry{name: string(record.Name), index: record.FeedIndex}, true, nil
}
