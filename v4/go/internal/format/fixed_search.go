// Fixed-cell search view over one slotted page (Rust slotted_page.rs
// FixedSearch, shared by the tree and reader fixed searches).
package format

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// FixedSearch is one fixed-cell page whose shape was checked once; every
// probe reads the persistent slot and validates the complete cell extent
// (Rust FixedSearch). The consuming search bounds index below ItemCount,
// exactly like the Rust lower_bound loops that call cell_at.
//
// The page is carried as a pointer to one fixed-size [PageSize]byte array
// (not a mapping slice): the one explicit length check in NewFixedSearch
// makes every later probe index against the constant 4096 instead of the
// mapping slice length, and the typed accessors slice the array and read
// through the inlined binary.LittleEndian word loaders without the
// extra format.U16/U32 wrapper frames (the same class of loads Rust does
// through its unsafe cell_at, without unsafe).
type FixedSearch struct {
	page    *[PageSize]byte
	header  PageHeader
	cellLen int
	// maxStart is PageSize - cellLen, precomputed once by the shape
	// check so every probe compares one field load (Rust keeps the
	// same expression; the field keeps the probe under the compiler's
	// inlining budget).
	maxStart int
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
	return FixedSearch{page: (*[PageSize]byte)(page), header: header, cellLen: cellLen, maxStart: PageSize - cellLen}, nil
}

// errFixedCellOutside is the one persistent-offset failure of every
// fixed-cell probe, pre-built so the probe hot path never formats or
// allocates an error and stays inlineable. FixedCellOutside exposes it
// to the search loops, which report it verbatim (same type and message
// as the old headerErr path).
var errFixedCellOutside = &HeaderError{Reason: "slotted-page cell is outside the record area"}

// FixedCellOutside reports the fixed-probe persistent-offset failure.
func FixedCellOutside() error { return errFixedCellOutside }

// Cell reads the fixed cell at an index already bounded by the search
// algorithm (Rust FixedSearch::cell_at). The pointer receiver keeps the
// ~64-byte search view on the stack instead of copying it on every
// probe. Callers that only need the key words use the typed accessors
// below, which never materialize the cell slice.
func (f *FixedSearch) Cell(index int) ([]byte, error) {
	work.CellProbe(1)
	work.SlotRead(1)
	p := f.page
	start := int(binary.LittleEndian.Uint16(p[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > f.maxStart {
		return nil, errFixedCellOutside
	}
	return p[start : start+f.cellLen], nil
}

// U32 reads the leading 4 bytes of one fixed cell as a little-endian
// u32 (the u32-key probe: no cell slice, one slot load, one extent
// check). The bool result keeps the accessor inlineable: the search
// loops translate a false probe into their own corruption error.
func (f *FixedSearch) U32(index int) (uint32, bool) {
	if f.cellLen < 4 {
		return 0, false
	}
	work.CellProbe(1)
	work.SlotRead(1)
	p := f.page
	start := int(binary.LittleEndian.Uint16(p[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > f.maxStart {
		return 0, false
	}
	return binary.LittleEndian.Uint32(p[start:]), true
}

// U64 reads the leading 8 bytes of one fixed cell as a little-endian
// u64 (the u64-key probe).
func (f *FixedSearch) U64(index int) (uint64, bool) {
	if f.cellLen < 8 {
		return 0, false
	}
	work.CellProbe(1)
	work.SlotRead(1)
	p := f.page
	start := int(binary.LittleEndian.Uint16(p[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > f.maxStart {
		return 0, false
	}
	return binary.LittleEndian.Uint64(p[start:]), true
}

// U64U32 reads the leading 12 bytes of one fixed cell as a little-endian
// u64 word at offset 0 and a little-endian u32 word at offset 8 (the
// (u64, u32)-key probe).
func (f *FixedSearch) U64U32(index int) (uint64, uint32, bool) {
	if f.cellLen < 12 {
		return 0, 0, false
	}
	work.CellProbe(1)
	work.SlotRead(1)
	p := f.page
	start := int(binary.LittleEndian.Uint16(p[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > f.maxStart {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(p[start:]), binary.LittleEndian.Uint32(p[start+8:]), true
}

// U128 reads the leading 16 bytes of one fixed cell as the little-endian
// u128 low/high limb pair (format.U128/PutU128 wire order: low limb at
// offset 0, high limb at offset 8).
func (f *FixedSearch) U128(index int) (uint64, uint64, bool) {
	work.CellProbe(1)
	work.SlotRead(1)
	p := f.page
	start := int(binary.LittleEndian.Uint16(p[SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > f.maxStart || f.cellLen < 16 {
		return 0, 0, false
	}
	lo := binary.LittleEndian.Uint64(p[start:])
	hi := binary.LittleEndian.Uint64(p[start+8:])
	return hi, lo, true
}
