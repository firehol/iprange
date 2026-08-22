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
func (s selectedStore) Inspect(pageNumber uint32) ([]byte, error) {
	if pageNumber < 2 || uint64(pageNumber) >= s.meta.PageCount {
		return nil, corrupt("stored range page is outside its source")
	}
	return s.store.InspectBase(pageNumber)
}

// DiscardPrivate is unreachable on a selected base source (the cursor is
// never consuming here); it fails closed if a future caller misuses it.
func (s selectedStore) DiscardPrivate(pageNumber uint32) error {
	return corrupt("selected base source cannot discard pages")
}

// rangeCursor is one forward cursor over a base generation range tree
// through the draft mapping (Rust Cursor<K> with SelectedStore).
type rangeCursor struct {
	cursor *tree.ForwardCursor[rangeRecord]
	family uint8
}

// newRangeCursor opens the forward cursor over the source range tree
// (Rust Cursor::new). A non-consuming cursor reads the base generation
// through the selected store (history projection, ordered merges); a
// consuming cursor reads a draft-private tree directly and proves every
// page was born in the draft transaction (Rust release_private), which
// the coverage merge of the feed workflows uses.
func newRangeCursor(store *DraftStore, base format.Meta, consume bool) (*rangeCursor, error) {
	if base.AddressFamily != format.AddressFamilyIPv4 && base.AddressFamily != format.AddressFamilyIPv6 {
		return nil, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "stored range cursor has the wrong address family"}
	}
	var source tree.ForwardStore
	if consume {
		if base.TxnID != store.draft.meta.TxnID || base.PageCount != store.draft.meta.PageCount {
			return nil, corrupt("consumed range tree is outside the draft generation")
		}
		source = store
	} else {
		source = selectedStore{store: store, meta: base}
	}
	var codec tree.Codec[rangeRecord]
	if base.AddressFamily == format.AddressFamilyIPv4 {
		codec = rangeCodec4{}
	} else {
		codec = rangeCodec6{}
	}
	cursor, err := tree.NewForwardCursor[rangeRecord](codec, source, base.RangeRoot, consume)
	if err != nil {
		return nil, err
	}
	return &rangeCursor{cursor: cursor, family: base.AddressFamily}, nil
}

// next returns the next range record in ascending key order, or ok=false
// at the end (Rust Cursor::next + RangeItem; one record by value).
func (c *rangeCursor) next() (record rangeRecord, ok bool, err error) {
	record, ok, err = c.cursor.Next()
	if err != nil {
		return rangeRecord{}, false, err
	}
	if !ok {
		return rangeRecord{}, false, nil
	}
	work.RangeConsumed(1)
	return record, true, nil
}

// InspectBase views one mapped page without the base-bound validation
// (the DraftStore-bound readers use Inspect; the selected base source
// adds its own bound on top).
func (s *DraftStore) InspectBase(pageNumber uint32) ([]byte, error) {
	if uint64(pageNumber) >= s.draft.meta.PageCount {
		return nil, corrupt("draft page is out of bounds")
	}
	return s.mapping.Page(pageNumber)
}
