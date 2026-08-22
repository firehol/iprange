// Ordered range traversal through a mutable mapped-page store (Rust
// range_store_cursor.rs): a forward cursor over one base generation read
// through the draft mapping. The source selection pins the base meta
// transaction and page count (Rust SelectedStore), so an ordered merge
// compares the incoming scan against the committed destination exactly,
// never against the draft's own pages. History never consumes the base
// tree (recorded chunk-3b-4 decision), so this cursor only reads, and
// next hands back one record by value so the merge loop allocates
// nothing per record (Rust Cursor::next returns DirectRange by value).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// selectedStore is one base generation pinned for reads through the
// draft mapping (Rust SelectedStore): every page view must name a page
// below the base page count, and pages parse against the base
// transaction, so a draft-grown file never leaks draft pages into a
// committed-generation scan.
type selectedStore struct {
	store *DraftStore
	meta  format.Meta
}

// TargetTxn returns the pinned base transaction (Rust
// PageSource::selected_txn).
func (s selectedStore) TargetTxn() uint64 { return s.meta.TxnID }

// PageLimit returns the pinned base page count (Rust
// PageSource::selected_page_limit).
func (s selectedStore) PageLimit() uint64 { return s.meta.PageCount }

// Inspect validates the page against the base bounds, then views it
// through the draft mapping (Rust SelectedStore::view_page).
func (s selectedStore) Inspect(pageNumber uint32, fn func(page []byte) error) error {
	if pageNumber < 2 || uint64(pageNumber) >= s.meta.PageCount {
		return corrupt("stored range page is outside its source")
	}
	return s.store.InspectBase(pageNumber, fn)
}

// DiscardPrivate is unreachable on a selected base source (the cursor is
// never consuming here); it fails closed if a future caller misuses it.
func (s selectedStore) DiscardPrivate(pageNumber uint32) error {
	return corrupt("selected base source cannot discard pages")
}

// rangeCursor is one forward cursor over a base generation range tree
// through the draft mapping (Rust Cursor<K> with SelectedStore).
type rangeCursor struct {
	cursor *tree.ForwardCursor
	family uint8
}

// newRangeCursor opens the forward cursor over the base range tree
// (Rust Cursor::new; release_private is always false for the history
// projection, which never consumes the base tree).
func newRangeCursor(store *DraftStore, base format.Meta) (*rangeCursor, error) {
	if base.AddressFamily != format.AddressFamilyIPv4 && base.AddressFamily != format.AddressFamilyIPv6 {
		return nil, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "stored range cursor has the wrong address family"}
	}
	source := selectedStore{store: store, meta: base}
	var codec tree.Codec
	if base.AddressFamily == format.AddressFamilyIPv4 {
		codec = rangeCodec4{}
	} else {
		codec = rangeCodec6{}
	}
	cursor, err := tree.NewForwardCursor(codec, source, base.RangeRoot, false)
	if err != nil {
		return nil, err
	}
	return &rangeCursor{cursor: cursor, family: base.AddressFamily}, nil
}

// next returns the next range record in ascending key order, or ok=false
// at the end (Rust Cursor::next + RangeItem; one record by value).
func (c *rangeCursor) next() (record rangeRecord, ok bool, err error) {
	found := false
	err = c.cursor.Next(func(cell []byte, header *tree.Header, pageNumber uint32, index int) error {
		var decoded rangeRecord
		var err error
		if c.family == format.AddressFamilyIPv4 {
			var r format.RangeRecordV4
			r, err = format.DecodeRangeRecordV4(cell)
			decoded = rangeRecord{from: tree.Key{Hi: uint64(r.From)}, to: tree.Key{Hi: uint64(r.To)}, value: r.Value}
		} else {
			var r format.RangeRecordV6
			r, err = format.DecodeRangeRecordV6(cell)
			decoded = rangeRecord{from: tree.Key{Hi: r.FromHi, Lo: r.FromLo}, to: tree.Key{Hi: r.ToHi, Lo: r.ToLo}, value: r.Value}
		}
		if err != nil {
			return corrupt("range leaf is invalid")
		}
		record = decoded
		found = true
		return nil
	})
	if err != nil {
		return rangeRecord{}, false, err
	}
	if !found {
		return rangeRecord{}, false, nil
	}
	work.RangeConsumed(1)
	return record, true, nil
}

// InspectBase views one mapped page without the base-bound validation
// (the DraftStore-bound readers use Inspect; the selected base source
// adds its own bound on top).
func (s *DraftStore) InspectBase(pageNumber uint32, fn func(page []byte) error) error {
	if uint64(pageNumber) >= s.draft.meta.PageCount {
		return corrupt("draft page is out of bounds")
	}
	page, err := s.mapping.Page(pageNumber)
	if err != nil {
		return err
	}
	return fn(page)
}
