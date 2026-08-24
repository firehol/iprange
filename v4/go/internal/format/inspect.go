package format

// Explicit-validation page inspection (Rust page_header.rs raw checks and
// slotted_page.rs inspect_tree_header / inspect_layout). The reader hot
// paths stay on DecodePageHeader/OpenSlotted; these helpers exist for
// explicit validation and recovery, which must classify every header
// problem exactly like the Rust validator (common, born, kind, level,
// shape) and prove one complete cell extent layout of a page.

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// PageCommonValid mirrors page_header::common_valid: full page length,
// page magic, zero flags, and the fixed 32-byte header size.
func PageCommonValid(page []byte) bool {
	return len(page) == PageSize &&
		magic4(page, PageMagic[:]) &&
		page[HeaderFlags] == 0 &&
		U16(page[HeaderSizePos:HeaderSizePos+2]) == SlottedHeaderSize
}

// PageBornValid mirrors page_header::born_valid: a nonzero born
// transaction at or below the selected transaction.
func PageBornValid(page []byte, selectedTxn uint64) bool {
	born := U64(page[HeaderBorn : HeaderBorn+8])
	return born != 0 && born <= selectedTxn
}

// PageKindValid mirrors page_header::kind_valid: the page-type byte and
// the aux discriminator.
func PageKindValid(page []byte, pageType byte, aux uint32) bool {
	return len(page) >= HeaderAux+4 &&
		page[HeaderType] == pageType &&
		U32(page[HeaderAux:HeaderAux+4]) == aux
}

// TreeHeaderProblem is the classified result of one tree-page header
// inspection (Rust slotted_page::HeaderProblem; the Go peer adds the
// explicit None success value).
type TreeHeaderProblem uint8

const (
	TreeHeaderProblemNone TreeHeaderProblem = iota
	TreeHeaderProblemHeader
	TreeHeaderProblemBorn
	TreeHeaderProblemType
	TreeHeaderProblemLevel
	TreeHeaderProblemShape
)

// InspectTreeHeader mirrors slotted_page::inspect_tree_header: the exact
// problem order (common, born, kind-at-level, level, shape) and the raw
// shape rule. The header is valid exactly when the problem is None.
func InspectTreeHeader(page []byte, selectedTxn uint64, branchType, leafType byte, aux uint32, expectedLevel *uint16) (PageHeader, TreeHeaderProblem) {
	var header PageHeader
	if !PageCommonValid(page) {
		return header, TreeHeaderProblemHeader
	}
	header = decodeRawPageHeader(page)
	if !PageBornValid(page, selectedTxn) {
		return header, TreeHeaderProblemBorn
	}
	expectedType := leafType
	if header.Level != 0 {
		expectedType = branchType
	}
	if !PageKindValid(page, expectedType, aux) {
		return header, TreeHeaderProblemType
	}
	if header.Level > MaxTreeLevel || (expectedLevel != nil && *expectedLevel != header.Level) {
		return header, TreeHeaderProblemLevel
	}
	if !SlottedShapeValid(&header) {
		return header, TreeHeaderProblemShape
	}
	return header, TreeHeaderProblemNone
}

// decodeRawPageHeader reads one raw page header after the common-valid
// proof (shared by the tree and structure-table inspections).
func decodeRawPageHeader(page []byte) PageHeader {
	return PageHeader{
		PageType:   PageType(page[HeaderType]),
		PageFlags:  page[HeaderFlags],
		HeaderSize: U16(page[HeaderSizePos : HeaderSizePos+2]),
		BornTxn:    U64(page[HeaderBorn : HeaderBorn+8]),
		ItemCount:  U16(page[HeaderCount : HeaderCount+2]),
		Level:      U16(page[HeaderLevel : HeaderLevel+2]),
		Lower:      U16(page[HeaderLower : HeaderLower+2]),
		Upper:      U16(page[HeaderUpper : HeaderUpper+2]),
		Aux:        U32(page[HeaderAux : HeaderAux+4]),
		PageCRC32C: U32(page[HeaderCRC : HeaderCRC+4]),
	}
}

// StructureTableMaxLevel is the maximum height of the dense structure-ID
// table (Rust structured_value/table.rs MAX_LEVEL): one record level plus
// up to three directory levels cover the complete u32 ID space.
const StructureTableMaxLevel = 3

// InspectStructureTableHeader mirrors structured_value/table.rs
// inspect_header: the dense-table page classification with the exact
// problem order (common, born, level, kind, shape). The dense table has
// fixed record/directory arrays instead of slotted cells, so its shape
// rule is the fixed lower bound plus a full-page upper bound, and the
// item count must fit the fixed array of the level.
func InspectStructureTableHeader(page []byte, selectedTxn uint64, aux uint32, expectedLevel *uint16) (PageHeader, TreeHeaderProblem) {
	var header PageHeader
	if !PageCommonValid(page) {
		return header, TreeHeaderProblemHeader
	}
	header = decodeRawPageHeader(page)
	if !PageBornValid(page, selectedTxn) {
		return header, TreeHeaderProblemBorn
	}
	if header.Level > StructureTableMaxLevel || (expectedLevel != nil && *expectedLevel != header.Level) {
		return header, TreeHeaderProblemLevel
	}
	expectedType := byte(PageTypeStructureIDRecord)
	if header.Level != 0 {
		expectedType = byte(PageTypeStructureIDDirectory)
	}
	if !PageKindValid(page, expectedType, aux) {
		return header, TreeHeaderProblemType
	}
	lower := uint16(StructureLeafEnd)
	maximum := StructureRecordSlots
	if header.Level != 0 {
		lower = uint16(StructureBranchEnd)
		maximum = StructureDirectoryChildCount
	}
	if header.Lower != lower || header.Upper != PageSize {
		return header, TreeHeaderProblemShape
	}
	if header.ItemCount == 0 || int(header.ItemCount) > maximum {
		return header, TreeHeaderProblemShape
	}
	return header, TreeHeaderProblemNone
}

// CellLayout mirrors slotted_page::CellLayout: one fixed cell size or a
// variable record length range.
type CellLayout struct {
	fixedLen int
	minimum  int
	maximum  int
}

// FixedLayout builds the fixed-cell layout of one tree level.
func FixedLayout(length int) CellLayout { return CellLayout{fixedLen: length} }

// VariableLayout builds the variable-record layout of one tree level.
func VariableLayout(minimum, maximum int) CellLayout {
	return CellLayout{minimum: minimum, maximum: maximum}
}

// LayoutInspection is the proved cell-extent layout of one slotted page
// (Rust slotted_page::LayoutInspection): the reserved-region scan result
// plus the inspected geometry for the cell walk.
type LayoutInspection struct {
	ReservedNonzero bool
	page            []byte
	header          PageHeader
	layout          CellLayout
}

// InspectLayout mirrors slotted_page::inspect_layout: the complete extent
// proof (fixed or variable cells, in the record area, non-overlapping, and
// the minimum record start equal to upper) and the reserved-region plus
// unmarked-region nonzero scan. A nil result means the layout is invalid
// (PageHeaderInvalid class for the validators).
func InspectLayout(page []byte, header *PageHeader, layout CellLayout) *LayoutInspection {
	if len(page) != PageSize || !SlottedShapeValid(header) {
		return nil
	}
	var used [PageSize / 64]uint64
	minimum, ok := inspectExtents(page, header, layout, &used)
	if !ok || minimum != int(header.Upper) {
		return nil
	}
	reservedNonzero := !AllZero(page[header.Lower:header.Upper])
	if !reservedNonzero {
		reservedNonzero = unmarkedNonzero(page, &used, int(header.Upper))
	}
	return &LayoutInspection{ReservedNonzero: reservedNonzero, page: page, header: *header, layout: layout}
}

// Cells returns the cell iterator of one inspection (Rust
// LayoutInspection::cells; every step counts the cell probe and the slot
// read exactly like the Rust iterator).
func (l *LayoutInspection) Cells() *LayoutCells {
	return &LayoutCells{inspection: l}
}

// LayoutCells iterates the inspected cells of one page (Rust
// LayoutCells). The inspection proved every cell extent, so the returned
// slices are directly usable by the walkers.
type LayoutCells struct {
	inspection *LayoutInspection
	index      int
}

// Next returns the next cell; ok is false after the last cell.
func (c *LayoutCells) Next() (cell []byte, ok bool) {
	if c.index == int(c.inspection.header.ItemCount) {
		return nil, false
	}
	work.CellProbe(1)
	work.SlotRead(1)
	slot := SlottedHeaderSize + c.index*2
	c.index++
	start := int(U16(c.inspection.page[slot : slot+2]))
	length := c.inspection.layout.fixedLen
	if length == 0 {
		length = int(U16(c.inspection.page[start : start+2]))
	}
	return c.inspection.page[start : start+length], true
}

func inspectExtents(page []byte, header *PageHeader, layout CellLayout, used *[PageSize / 64]uint64) (int, bool) {
	if layout.fixedLen != 0 {
		return inspectFixedExtents(page, header, layout.fixedLen, used)
	}
	return inspectVariableExtents(page, header, layout.minimum, layout.maximum, used)
}

func inspectFixedExtents(page []byte, header *PageHeader, length int, used *[PageSize / 64]uint64) (int, bool) {
	if length > PageSize {
		return 0, false
	}
	maximumStart := PageSize - length
	minimum := PageSize
	for index := 0; index < int(header.ItemCount); index++ {
		work.CellProbe(1)
		work.SlotRead(1)
		start := int(U16(page[SlottedHeaderSize+index*2 : SlottedHeaderSize+index*2+2]))
		if start < int(header.Upper) || start > maximumStart {
			return 0, false
		}
		if !markExtent(used, start, start+length) {
			return 0, false
		}
		if start < minimum {
			minimum = start
		}
	}
	return minimum, true
}

func inspectVariableExtents(page []byte, header *PageHeader, minimumLen, maximumLen int, used *[PageSize / 64]uint64) (int, bool) {
	minimum := PageSize
	for index := 0; index < int(header.ItemCount); index++ {
		// The slot value is read once uncounted and once through the
		// record helper: the inspection probe counts exactly like the
		// Rust record() (one cell probe + one slot read per record).
		start := int(U16(page[SlottedHeaderSize+index*2 : SlottedHeaderSize+index*2+2]))
		record, err := SlottedRecord(page, header, index, minimumLen, maximumLen)
		if err != nil {
			return 0, false
		}
		if !markExtent(used, start, start+len(record)) {
			return 0, false
		}
		if start < minimum {
			minimum = start
		}
	}
	return minimum, true
}

func markExtent(bits *[PageSize / 64]uint64, start, end int) bool {
	if start >= end || end > PageSize {
		return false
	}
	first := start / 64
	last := (end - 1) / 64
	if first == last {
		mask := (^uint64(0) << (start % 64)) & endMask(end%64)
		return markWord(&bits[first], mask)
	}
	if !markWord(&bits[first], ^uint64(0)<<(start%64)) {
		return false
	}
	for word := first + 1; word < last; word++ {
		if !markWord(&bits[word], ^uint64(0)) {
			return false
		}
	}
	return markWord(&bits[last], endMask(end%64))
}

func endMask(bit int) uint64 {
	if bit == 0 {
		return ^uint64(0)
	}
	return (uint64(1) << bit) - 1
}

func markWord(word *uint64, mask uint64) bool {
	if *word&mask != 0 {
		return false
	}
	*word |= mask
	return true
}

func unmarkedNonzero(page []byte, used *[PageSize / 64]uint64, start int) bool {
	for wordIndex := start / 64; wordIndex < len(used); wordIndex++ {
		base := wordIndex * 64
		inRange := ^uint64(0)
		if base < start {
			inRange = ^uint64(0) << (start - base)
		}
		unmarked := ^used[wordIndex] & inRange
		for unmarked != 0 {
			bit := bits.TrailingZeros64(unmarked)
			length := bits.TrailingZeros64(^(unmarked >> bit))
			if !AllZero(page[base+bit : base+bit+length]) {
				return true
			}
			mask := ^uint64(0)
			if length < 64 {
				mask = ((uint64(1) << length) - 1) << bit
			}
			unmarked &^= mask
		}
	}
	return false
}

// AllZero reports whether every byte of b is zero (Rust ByteSource
// all_zero; the layout and metadata reserved-region scans).
func AllZero(page []byte) bool {
	for _, b := range page {
		if b != 0 {
			return false
		}
	}
	return true
}
