package format

import (
	"github.com/firehol/iprange/v4/go/internal/work"
)

// put.go is the write-side page authority, mirroring Rust page_header.rs
// initialize and the slotted_page.rs mutation surface. Every helper writes
// directly into a caller-supplied complete page view (a mapped page in
// production; tests may supply owned pages). There is no owned-page
// persistence anywhere in production code.

// Common page-header field offsets (binary-format-v4.md section 5).
const (
	HeaderMagic   = 0
	HeaderType    = 4
	HeaderFlags   = 5
	HeaderSizePos = 6 // 16-bit header size (always 32)
	HeaderBorn    = 8
	HeaderCount   = 16
	HeaderLevel   = 18
	HeaderLower   = 20
	HeaderUpper   = 22
	HeaderAux     = 24
	HeaderCRC     = 28 // PageChecksumOffset; dirty-chain tag slot during a draft
)

// SlottedHeaderSize is the fixed slotted-page head: the 32-byte page header
// followed by the 16-bit slot array (binary-format-v4.md section 7).
const SlottedHeaderSize = 32

// MaxSlotCount is the maximum number of 16-bit slots that fit after the
// fixed head (Rust slotted_page MAX_SLOT_COUNT).
const MaxSlotCount = (PageSize - SlottedHeaderSize) / 2

// InitializePageHeader fills one complete page with the canonical common
// header (Rust page_header::initialize): zero body, magic, type, zero
// flags, fixed header size, born transaction, item count, level, bounds and
// aux. The checksum field is left zero; the caller stamps it when the page
// is sealed.
func InitializePageHeader(page []byte, pageType PageType, bornTxn uint64, itemCount, level, lower, upper uint16, aux uint32) {
	clear(page)
	copy(page[HeaderMagic:], PageMagic[:])
	page[HeaderType] = byte(pageType)
	page[HeaderFlags] = 0
	PutU16(page[HeaderSizePos:], PageHeaderSize)
	PutU64(page[HeaderBorn:], bornTxn)
	PutU16(page[HeaderCount:], itemCount)
	PutU16(page[HeaderLevel:], level)
	PutU16(page[HeaderLower:], lower)
	PutU16(page[HeaderUpper:], upper)
	PutU32(page[HeaderAux:], aux)
}

// PageHeaderSize is the fixed common header length.
const PageHeaderSize = 32

// SlottedShape is the (item count, bounds) result of one slotted-page
// structural mutation (Rust slotted_page::Header).
type SlottedShape struct {
	ItemCount int
	Lower     uint16
	Upper     uint16
}

// slottedLayout is a copy of one slotted page's geometry: item count and
// free-space bounds.
type slottedLayout struct {
	count int
	lower uint16
	upper uint16
}

func layoutFrom(header *PageHeader) slottedLayout {
	return slottedLayout{count: int(header.ItemCount), lower: header.Lower, upper: header.Upper}
}

// SlottedInsertFits reports whether one record fits in the free area below
// the current upper bound, mirroring slotted_page.rs insert_fits.
func SlottedInsertFits(header *PageHeader, cellLen int) bool {
	return cellLen != 0 && int(header.Lower)+2+cellLen <= int(header.Upper)
}

// SlottedReplaceFits reports whether one replacement fits in the free area
// plus the space the old record occupies, mirroring slotted_page.rs
// replace_fits.
func SlottedReplaceFits(header *PageHeader, oldLen, newLen int) bool {
	return oldLen != 0 && newLen != 0 && newLen <= oldLen+int(header.Upper-header.Lower)
}

// SlottedInsert inserts one record at logical index, mirroring
// slotted_page.rs insert. It reports false (without touching the page) when
// the record does not fit.
func SlottedInsert(page []byte, header *PageHeader, index int, cell []byte) (bool, error) {
	l := layoutFrom(header)
	if index > l.count {
		return false, headerErr("slotted-page insertion index is invalid")
	}
	if len(cell) == 0 {
		return false, &Error{Code: CodeInvalidArgument, Detail: "slotted-page record is empty"}
	}
	if !SlottedInsertFits(header, len(cell)) {
		return false, nil
	}
	upper := int(l.upper) - len(cell)
	lower := int(l.lower) + 2
	slot := SlottedHeaderSize + index*2
	if slot != int(l.lower) {
		copy(page[slot+2:], page[slot:int(l.lower)])
	}
	copy(page[upper:], cell)
	PutU16(page[slot:], uint16(upper))
	PutU16(page[HeaderCount:], uint16(l.count+1))
	PutU16(page[HeaderLower:], uint16(lower))
	PutU16(page[HeaderUpper:], uint16(upper))
	return true, nil
}

// SlottedReplace replaces one record in place, mirroring slotted_page.rs
// replace. It reports false (without touching the page) when the new record
// does not fit.
func SlottedReplace(page []byte, header *PageHeader, index, oldLen int, cell []byte) (bool, error) {
	l := layoutFrom(header)
	if index >= l.count || oldLen == 0 || len(cell) == 0 {
		return false, headerErr("slotted-page replacement is invalid")
	}
	start, ok := recordStart(page, header, index, oldLen)
	if !ok {
		return false, headerErr("slotted-page record start is invalid")
	}
	if len(cell) > oldLen {
		growth := len(cell) - oldLen
		if int(l.lower) > int(l.upper)-growth {
			return false, nil
		}
		copy(page[int(l.upper)-growth:], page[int(l.upper):start])
		if err := adjustSlotsBefore(page, header, index, start, false, growth); err != nil {
			return false, err
		}
		newStart := start - growth
		copy(page[newStart:], cell)
		PutU16(page[SlottedHeaderSize+index*2:], uint16(newStart))
		PutU16(page[HeaderUpper:], uint16(int(l.upper)-growth))
	} else {
		shrink := oldLen - len(cell)
		if shrink != 0 {
			copy(page[int(l.upper)+shrink:], page[int(l.upper):start])
			clear(page[int(l.upper) : int(l.upper)+shrink])
			if err := adjustSlotsBefore(page, header, index, start, true, shrink); err != nil {
				return false, err
			}
		}
		newStart := start + shrink
		copy(page[newStart:], cell)
		PutU16(page[SlottedHeaderSize+index*2:], uint16(newStart))
		PutU16(page[HeaderUpper:], uint16(int(l.upper)+shrink))
	}
	return true, nil
}

// SlottedRemove removes one record, mirroring slotted_page.rs remove. The
// page must retain at least one record.
func SlottedRemove(page []byte, header *PageHeader, index, oldLen int) error {
	l := layoutFrom(header)
	if index >= l.count || l.count <= 1 || oldLen == 0 {
		return headerErr("slotted-page removal is invalid")
	}
	start, ok := recordStart(page, header, index, oldLen)
	if !ok {
		return headerErr("slotted-page record start is invalid")
	}
	copy(page[int(l.upper)+oldLen:], page[int(l.upper):start])
	clear(page[int(l.upper) : int(l.upper)+oldLen])
	if err := adjustSlotsBefore(page, header, index, start, true, oldLen); err != nil {
		return err
	}
	slot := SlottedHeaderSize + index*2
	copy(page[slot:], page[slot+2:int(l.lower)])
	PutU16(page[int(l.lower)-2:], 0)
	PutU16(page[HeaderCount:], uint16(l.count-1))
	PutU16(page[HeaderLower:], uint16(int(l.lower)-2))
	PutU16(page[HeaderUpper:], uint16(int(l.upper)+oldLen))
	return nil
}

// SlottedRemoveFixedRange removes count fixed-size records starting at
// start, mirroring slotted_page.rs remove_fixed_range. It returns the
// resulting page shape.
func SlottedRemoveFixedRange(page []byte, header *PageHeader, start, count, cellLen int) (SlottedShape, error) {
	end := start + count
	if count == 0 || end > int(header.ItemCount) || count >= int(header.ItemCount) || cellLen == 0 {
		return SlottedShape{}, headerErr("slotted-page removal range is invalid")
	}
	positions, err := fixedPositions(page, header, cellLen)
	if err != nil {
		return SlottedShape{}, err
	}
	remaining := int(header.ItemCount) - count
	destination := PageSize
	for physical := len(positions) - 1; physical >= 0; physical-- {
		logical := positions[physical]
		if logical < start || logical >= end {
			output := logical
			if logical >= start {
				output = logical - count
			}
			source := int(header.Upper) + physical*cellLen
			destination -= cellLen
			copy(page[destination:], page[source:source+cellLen])
			PutU16(page[SlottedHeaderSize+output*2:], uint16(destination))
		}
	}
	lower := SlottedHeaderSize + remaining*2
	clear(page[lower:int(header.Lower)])
	clear(page[int(header.Upper):destination])
	PutU16(page[HeaderCount:], uint16(remaining))
	PutU16(page[HeaderLower:], uint16(lower))
	PutU16(page[HeaderUpper:], uint16(destination))
	return SlottedShape{ItemCount: remaining, Lower: uint16(lower), Upper: uint16(destination)}, nil
}

// SlottedTruncate keeps the first keep logical records of a variable-size
// slotted page, mirroring slotted_page.rs truncate. Records are re-packed by
// physical offset, so logical and physical order may differ.
func SlottedTruncate(page []byte, header *PageHeader, keep int) (SlottedShape, error) {
	if keep == 0 || keep > int(header.ItemCount) {
		return SlottedShape{}, headerErr("slotted-page truncation is invalid")
	}
	if keep == int(header.ItemCount) {
		return SlottedShape{ItemCount: int(header.ItemCount), Lower: header.Lower, Upper: header.Upper}, nil
	}
	records := make([]physicalRecord, header.ItemCount)
	for index := range records {
		work.SlotScanStep(1)
		start, ok := slotStart(page, header, index)
		if !ok || start < int(header.Upper) || start >= PageSize {
			return SlottedShape{}, headerErr("slotted-page record offset is invalid")
		}
		records[index] = physicalRecord{start: start, index: index}
	}
	sortPhysical(records)
	if records[0].start != int(header.Upper) {
		return SlottedShape{}, headerErr("slotted-page record offsets are invalid")
	}
	for at := 1; at < len(records); at++ {
		if records[at].start == records[at-1].start {
			return SlottedShape{}, headerErr("slotted-page record offsets are invalid")
		}
	}
	destination := PageSize
	for physical := len(records) - 1; physical >= 0; physical-- {
		record := records[physical]
		end := PageSize
		if physical+1 < len(records) {
			end = records[physical+1].start
		}
		if record.index < keep {
			length := end - record.start
			newStart := destination - length
			copy(page[newStart:], page[record.start:end])
			PutU16(page[SlottedHeaderSize+record.index*2:], uint16(newStart))
			destination = newStart
		}
	}
	lower := SlottedHeaderSize + keep*2
	clear(page[lower:int(header.Lower)])
	clear(page[int(header.Upper):destination])
	PutU16(page[HeaderCount:], uint16(keep))
	PutU16(page[HeaderLower:], uint16(lower))
	PutU16(page[HeaderUpper:], uint16(destination))
	return SlottedShape{ItemCount: keep, Lower: uint16(lower), Upper: uint16(destination)}, nil
}

// SlottedTruncateFixed keeps the first keep fixed-size logical records,
// mirroring slotted_page.rs truncate_fixed.
func SlottedTruncateFixed(page []byte, header *PageHeader, keep, cellLen int) (SlottedShape, error) {
	if keep == 0 || keep > int(header.ItemCount) || cellLen == 0 {
		return SlottedShape{}, headerErr("fixed slotted-page truncation is invalid")
	}
	if keep == int(header.ItemCount) {
		return SlottedShape{ItemCount: int(header.ItemCount), Lower: header.Lower, Upper: header.Upper}, nil
	}
	positions, err := fixedPositions(page, header, cellLen)
	if err != nil {
		return SlottedShape{}, err
	}
	destination := PageSize
	for physical := len(positions) - 1; physical >= 0; physical-- {
		logical := positions[physical]
		if logical < keep {
			source := int(header.Upper) + physical*cellLen
			destination -= cellLen
			copy(page[destination:], page[source:source+cellLen])
			PutU16(page[SlottedHeaderSize+logical*2:], uint16(destination))
		}
	}
	lower := SlottedHeaderSize + keep*2
	clear(page[lower:int(header.Lower)])
	clear(page[int(header.Upper):destination])
	PutU16(page[HeaderCount:], uint16(keep))
	PutU16(page[HeaderLower:], uint16(lower))
	PutU16(page[HeaderUpper:], uint16(destination))
	return SlottedShape{ItemCount: keep, Lower: uint16(lower), Upper: uint16(destination)}, nil
}

// SlottedBuilder appends records at the end of one slotted page, mirroring
// slotted_page.rs Appender/Builder (used to build fresh pages from source
// records).
type SlottedBuilder struct {
	count int
	upper int
}

// NewSlottedBuilder initializes one fresh slotted page with the common
// header and empty geometry.
func NewSlottedBuilder(page []byte, pageType PageType, bornTxn uint64, level uint16, aux uint32) SlottedBuilder {
	InitializePageHeader(page, pageType, bornTxn, 0, level, SlottedHeaderSize, PageSize, aux)
	return SlottedBuilder{count: 0, upper: PageSize}
}

// Push appends one record to the page under construction.
func (b *SlottedBuilder) Push(page []byte, cell []byte) error {
	if len(cell) == 0 {
		return headerErr("slotted-page record is empty")
	}
	lower := SlottedHeaderSize + (b.count+1)*2
	upper := b.upper - len(cell)
	if lower > upper {
		return headerErr("slotted page is full")
	}
	copy(page[upper:], cell)
	PutU16(page[SlottedHeaderSize+b.count*2:], uint16(upper))
	b.count++
	b.upper = upper
	return nil
}

// Finish stamps the final count and bounds of the page under construction.
func (b *SlottedBuilder) Finish(page []byte) error {
	if b.count == 0 {
		return headerErr("reachable slotted page cannot be empty")
	}
	PutU16(page[HeaderCount:], uint16(b.count))
	PutU16(page[HeaderLower:], uint16(SlottedHeaderSize+b.count*2))
	PutU16(page[HeaderUpper:], uint16(b.upper))
	return nil
}

// maxInt is the largest int value; the appender slot-array overflow
// check uses it to mirror Rust checked_add on the page geometry (dead on
// 64-bit builds).
const maxInt = int(^uint(0) >> 1)

// SlottedAppender mirrors slotted_page.rs Appender: a fresh page under
// construction whose records are appended one by one with tryPush,
// reporting whether the pushed record fits without failing the page
// (Rust try_push). The range bulk builder uses it to roll a full page
// upward without re-encoding.
type SlottedAppender struct {
	count int
	upper int
}

// NewSlottedAppender initializes one fresh slotted page with the common
// header and empty geometry (Rust Appender::new).
func NewSlottedAppender(page []byte, pageType PageType, bornTxn uint64, level uint16, aux uint32) SlottedAppender {
	InitializePageHeader(page, pageType, bornTxn, 0, level, SlottedHeaderSize, PageSize, aux)
	return SlottedAppender{count: 0, upper: PageSize}
}

// TryPush appends one record when it fits and reports whether it was
// appended (Rust Appender::try_push).
func (a *SlottedAppender) TryPush(page []byte, cell []byte) (bool, error) {
	if len(cell) == 0 {
		return false, &Error{Code: CodeInvalidArgument, Detail: "slotted-page record is empty"}
	}
	if a.count+1 > (maxInt-SlottedHeaderSize)/2 {
		return false, headerErr("slotted-page slot array overflows")
	}
	lower := SlottedHeaderSize + (a.count+1)*2
	if len(cell) > a.upper {
		return false, headerErr("slotted-page record area overflows")
	}
	upper := a.upper - len(cell)
	if lower > upper {
		return false, nil
	}
	copy(page[upper:], cell)
	PutU16(page[SlottedHeaderSize+a.count*2:], uint16(upper))
	a.count++
	a.upper = upper
	return true, nil
}

// Finish stamps the final count and bounds of the page under construction
// (Rust Appender::finish).
func (a *SlottedAppender) Finish(page []byte) error {
	if a.count == 0 {
		return &Error{Code: CodeInvalidArgument, Detail: "reachable slotted page cannot be empty"}
	}
	PutU16(page[HeaderCount:], uint16(a.count))
	PutU16(page[HeaderLower:], uint16(SlottedHeaderSize+a.count*2))
	PutU16(page[HeaderUpper:], uint16(a.upper))
	return nil
}

// physicalRecord is one (start, logical index) pair used to re-pack records
// in physical order (Rust slotted_page::PhysicalRecord).
type physicalRecord struct {
	start int
	index int
}

func sortPhysical(records []physicalRecord) {
	// Insertion sort: mutation pages are tiny (a few hundred records) and
	// the records are usually already nearly sorted. Keeps the helper
	// allocation-free on the hot path.
	for at := 1; at < len(records); at++ {
		for at > 0 && records[at].start < records[at-1].start {
			records[at], records[at-1] = records[at-1], records[at]
			at--
		}
	}
}

// recordStart returns the record offset for logical index with a known
// length, verifying the slot and bounds (slotted_page.rs record_start).
func recordStart(page []byte, header *PageHeader, index, cellLen int) (int, bool) {
	if index >= int(header.ItemCount) {
		return 0, false
	}
	slot, ok := slotStart(page, header, index)
	if !ok {
		return 0, false
	}
	if slot < int(header.Upper) || slot+cellLen > PageSize {
		return 0, false
	}
	return slot, true
}

// slotStart reads the persistent slot value for logical index (slotted_page.rs
// slot_start).
func slotStart(page []byte, header *PageHeader, index int) (int, bool) {
	work.SlotRead(1)
	if index >= int(header.ItemCount) || SlottedHeaderSize+index*2+2 > len(page) {
		return 0, false
	}
	return int(U16(page[SlottedHeaderSize+index*2:])), true
}

// adjustSlotsBefore shifts the slot of every record physically below
// `before` by amount, skipping the target record, mirroring slotted_page.rs
// adjust_slots_before. `add` selects the direction: add on shrink, subtract
// on growth (the moved area between upper and the target record shifts that
// way).
func adjustSlotsBefore(page []byte, header *PageHeader, target, before int, add bool, amount int) error {
	for index := 0; index < int(header.ItemCount); index++ {
		work.SlotScanStep(1)
		if index == target {
			continue
		}
		at := SlottedHeaderSize + index*2
		old := int(U16(page[at:]))
		if old >= before {
			continue
		}
		adjusted := old + amount
		if !add {
			adjusted = old - amount
		}
		if adjusted < 0 || adjusted >= PageSize {
			return headerErr("slotted-page slot adjustment is invalid")
		}
		PutU16(page[at:], uint16(adjusted))
	}
	return nil
}

// fixedPositions returns the physical-order logical indexes of every
// fixed-size record on the page, mirroring slotted_page.rs fixed_positions:
// the payload must be packed (upper + item_count*cell_len == page size) and
// every slot must land on a distinct aligned cell.
func fixedPositions(page []byte, header *PageHeader, cellLen int) ([]int, error) {
	count := int(header.ItemCount)
	payload := int(header.Upper) + count*cellLen
	if cellLen == 0 || payload != PageSize {
		return nil, headerErr("fixed slotted-page payload is not packed")
	}
	positions := make([]int, count)
	for at := range positions {
		positions[at] = -1
	}
	for logical := 0; logical < count; logical++ {
		work.SlotScanStep(1)
		start, ok := slotStart(page, header, logical)
		if !ok {
			return nil, headerErr("slotted-page slot index is invalid")
		}
		if start < int(header.Upper) {
			return nil, headerErr("fixed slotted-page record is outside payload")
		}
		offset := start - int(header.Upper)
		if offset%cellLen != 0 {
			return nil, headerErr("fixed slotted-page record is misaligned")
		}
		physical := offset / cellLen
		if physical >= count {
			return nil, headerErr("fixed slotted-page record is outside payload")
		}
		if positions[physical] != -1 {
			return nil, headerErr("fixed slotted-page records overlap")
		}
		positions[physical] = logical
	}
	for _, logical := range positions {
		if logical == -1 {
			return nil, headerErr("fixed slotted-page payload has a gap")
		}
	}
	return positions, nil
}

// SlottedCell reads one fixed-size cell at logical index, mirroring
// slotted_page.rs cell: the persistent slot value is checked against the
// record area on every probe. The caller has already validated the page
// shape.
func SlottedCell(page []byte, header *PageHeader, index, cellLen int) ([]byte, error) {
	work.CellProbe(1)
	start, ok := slotStart(page, header, index)
	if !ok {
		return nil, headerErr("slotted-page cell start is invalid")
	}
	if start < int(header.Upper) || start+cellLen > PageSize {
		return nil, headerErr("slotted-page cell is outside the record area")
	}
	return page[start : start+cellLen], nil
}

// SlottedShapeValid reports the canonical slotted geometry (Rust
// slotted_page shape_valid): nonzero item count, exact lower bound, and an
// upper bound inside the page.
func SlottedShapeValid(header *PageHeader) bool {
	return header.ItemCount != 0 &&
		int(header.Lower) == SlottedHeaderSize+int(header.ItemCount)*2 &&
		header.Lower <= header.Upper &&
		int(header.Upper) < PageSize
}

// SlottedCellOffset resolves the physical offset of one logical record
// slot (the slot table start value). Both fixed cells and variable
// records are returned by the read helpers as page[start:end] slices, so
// this offset is the cell's position inside the page.
func SlottedCellOffset(page []byte, header *PageHeader, index int) (int, bool) {
	return slotStart(page, header, index)
}

// SlottedRecord reads one variable-length record at logical index,
// mirroring slotted_page.rs record: the persisted record length is checked
// against [minimumLen, maximumLen] on every probe. The caller has already
// validated the page shape.
func SlottedRecord(page []byte, header *PageHeader, index, minimumLen, maximumLen int) ([]byte, error) {
	work.CellProbe(1)
	start, ok := slotStart(page, header, index)
	if !ok {
		return nil, headerErr("slotted-page record is outside the record area")
	}
	if start < int(header.Upper) || start+2 > PageSize {
		return nil, headerErr("slotted-page record is outside the record area")
	}
	recordLen := int(U16(page[start:]))
	if recordLen < minimumLen || recordLen > maximumLen {
		return nil, headerErr("slotted-page record length is invalid")
	}
	end := start + recordLen
	if end > PageSize {
		return nil, headerErr("slotted-page record is outside the record area")
	}
	return page[start:end], nil
}
