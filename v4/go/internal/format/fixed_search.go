// Fixed-cell search view over one slotted page (Rust slotted_page.rs
// FixedSearch, shared by the tree and reader fixed searches).
package format

import "github.com/firehol/iprange/v4/go/internal/work"

// FixedSearch is one fixed-cell page whose shape was checked once; every
// probe reads the persistent slot and validates the complete cell extent
// (Rust FixedSearch). The consuming search bounds index below ItemCount,
// exactly like the Rust lower_bound loops that call cell_at.
type FixedSearch struct {
	page    []byte
	header  PageHeader
	cellLen int
}

// NewFixedSearch validates the page shape once (Rust FixedSearch::new):
// the page must be a full page with the canonical slotted geometry and a
// supported fixed cell length. The canonical geometry (exact slot-array
// lower bound below a record area below the page end) makes every later
// probe's slot-table load in-bounds for index < ItemCount.
func NewFixedSearch(page []byte, header PageHeader, cellLen int) (FixedSearch, error) {
	if len(page) != PageSize || !SlottedShapeValid(&header) ||
		cellLen == 0 || cellLen > PageSize {
		return FixedSearch{}, headerErr("fixed slotted-page search shape is invalid")
	}
	return FixedSearch{page: page, header: header, cellLen: cellLen}, nil
}

// Cell reads the fixed cell at an index already bounded by the search
// algorithm (Rust FixedSearch::cell_at): the slot-table read needs no
// per-probe index re-check; the persistent slot value stays untrusted
// and its complete extent in the record area is validated on every
// probe.
func (f FixedSearch) Cell(index int) ([]byte, error) {
	work.CellProbe(1)
	work.SlotRead(1)
	start := int(U16(f.page[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > PageSize-f.cellLen {
		return nil, headerErr("slotted-page cell is outside the record area")
	}
	return f.page[start : start+f.cellLen], nil
}
