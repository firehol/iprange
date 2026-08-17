package format

// Tests for the write-side slotted-page authority, mirroring the vectors in
// Rust slotted_page_tests.rs. Records are treated as caller-known-length
// cells exactly like the Rust tests; the u16 length prefix is only part of
// codec-level variable records, not of the slotted page itself.

import (
	"testing"
)

func testPage() []byte { return make([]byte, PageSize) }

// parseTestHeader decodes the common header of a test page built with
// bornTxn 7, page type 2 and aux 4 (the Rust test vectors).
func parseTestHeader(t *testing.T, page []byte) PageHeader {
	t.Helper()
	h, err := DecodePageHeader(page, 7)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if h.PageType != 2 || h.Aux != 4 {
		t.Fatalf("unexpected page type %d aux %d", h.PageType, h.Aux)
	}
	return h
}

// readFixedCell returns the fixed-size cell at logical index (Rust
// slotted_page::cell).
func readFixedCell(t *testing.T, page []byte, header *PageHeader, index, cellLen int) []byte {
	t.Helper()
	if index >= int(header.ItemCount) {
		t.Fatalf("cell index %d out of range", index)
	}
	start := int(U16(page[SlottedHeaderSize+index*2:]))
	if start < int(header.Upper) || start+cellLen > PageSize {
		t.Fatalf("cell %d outside record area", index)
	}
	return page[start : start+cellLen]
}

// fixedValues decodes every cell of a fixed-size page as u32 values.
func fixedValues(t *testing.T, page []byte, header *PageHeader, cellLen int) []uint32 {
	t.Helper()
	out := make([]uint32, header.ItemCount)
	for index := range out {
		cell := readFixedCell(t, page, header, index, cellLen)
		out[index] = U32(cell[:4])
	}
	return out
}

func buildCells(t *testing.T, cells [][]byte) ([]byte, PageHeader) {
	t.Helper()
	page := testPage()
	b := NewSlottedBuilder(page, 2, 7, 0, 4)
	for _, cell := range cells {
		if err := b.Push(page, cell); err != nil {
			t.Fatalf("push %v: %v", cell, err)
		}
	}
	if err := b.Finish(page); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return page, parseTestHeader(t, page)
}

// freeZero asserts the free area between lower and upper is all zero.
func freeZero(t *testing.T, page []byte, header *PageHeader) {
	t.Helper()
	for at := int(header.Lower); at < int(header.Upper); at++ {
		if page[at] != 0 {
			t.Fatalf("free area nonzero at %d", at)
		}
	}
}

// TestSlottedBuilderMatchesLiteralBytes pins the exact little-endian bytes
// of a two-record fixed page (Rust big_endian_portable_slotted_page_matches_literal_bytes).
func TestSlottedBuilderMatchesLiteralBytes(t *testing.T) {
	page := testPage()
	b := NewSlottedBuilder(page, 2, 7, 0, 4)
	for _, cell := range [][]byte{bytes12(1), bytes12(2)} {
		if err := b.Push(page, cell); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finish(page); err != nil {
		t.Fatal(err)
	}
	want := []byte{'I', 'P', '4', 'P', 2, 0, 32, 0, 7, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 36, 0, 0xe8, 0x0f, 4, 0, 0, 0}
	for at := range want {
		if page[at] != want[at] {
			t.Fatalf("byte %d = %#x, want %#x", at, page[at], want[at])
		}
	}
	if page[32] != 0xf4 || page[33] != 0x0f || page[34] != 0xe8 || page[35] != 0x0f {
		t.Fatalf("slot array = % x", page[32:36])
	}
	for index := 0; index < 12; index++ {
		if page[4096-12+index] != 1 || page[4096-24+index] != 2 {
			t.Fatalf("record bytes wrong at %d", index)
		}
	}
	header := parseTestHeader(t, page)
	if header.ItemCount != 2 || header.Lower != 36 || header.Upper != 0x0fe8 {
		t.Fatalf("layout = %+v", header)
	}
	if got := readFixedCell(t, page, &header, 0, 12); len(got) != 12 || got[0] != 1 {
		t.Fatalf("cell 0 = %v %v", got[0], len(got))
	}
	if got := readFixedCell(t, page, &header, 1, 12); got[0] != 2 {
		t.Fatalf("cell 1 = %v", got[0])
	}
	if U32(page[HeaderCRC:]) != 0 {
		t.Fatalf("checksum field not zero before seal")
	}
	SealPageChecksum(page)
	if !PageChecksumValid(page) {
		t.Fatal("sealed checksum invalid")
	}
}

func bytes12(v byte) []byte {
	out := make([]byte, 12)
	for at := range out {
		out[at] = v
	}
	return out
}

// TestBuilderRejectsOverfullOrEmptyPage mirrors the Rust builder error
// vectors.
func TestBuilderRejectsOverfullOrEmptyPage(t *testing.T) {
	page := testPage()
	b := NewSlottedBuilder(page, 2, 1, 0, 4)
	if err := b.Finish(page); err == nil {
		t.Fatal("empty finish accepted")
	}
	page = testPage()
	b = NewSlottedBuilder(page, 2, 1, 0, 4)
	if err := b.Push(page, make([]byte, PageSize)); err == nil {
		t.Fatal("overfull push accepted")
	}
}

// TestInPlaceInsertionChangesOnlySlotsAndFreeSpace mirrors the Rust
// insertion order vector.
func TestInPlaceInsertionChangesOnlySlotsAndFreeSpace(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aa"), []byte("cc")})
	ok, err := SlottedInsert(page, &header, 1, []byte("bb"))
	if err != nil || !ok {
		t.Fatalf("insert 1: %v %v", ok, err)
	}
	header = parseTestHeader(t, page)
	ok, err = SlottedInsert(page, &header, 0, []byte("00"))
	if err != nil || !ok {
		t.Fatalf("insert 0: %v %v", ok, err)
	}
	header = parseTestHeader(t, page)
	ok, err = SlottedInsert(page, &header, 4, []byte("zz"))
	if err != nil || !ok {
		t.Fatalf("insert 4: %v %v", ok, err)
	}
	header = parseTestHeader(t, page)
	want := []string{"00", "aa", "bb", "cc", "zz"}
	for index, cell := range want {
		if got := string(readFixedCell(t, page, &header, index, 2)); got != cell {
			t.Fatalf("record %d = %q, want %q", index, got, cell)
		}
	}
	freeZero(t, page, &header)
}

// TestEditsPreserveLogicalOrderWithPhysicallyUnorderedRecords runs the full
// insert/replace/remove/truncate sequence on a page whose physical order
// diverges from logical order (Rust vector).
func TestEditsPreserveLogicalOrderWithPhysicallyUnorderedRecords(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aa"), []byte("dd")})
	for _, step := range []struct {
		index int
		cell  string
	}{
		{1, "bb"}, {2, "cc"}, {4, "zz"},
	} {
		ok, err := SlottedInsert(page, &header, step.index, []byte(step.cell))
		if err != nil || !ok {
			t.Fatalf("insert %d: %v %v", step.index, ok, err)
		}
		header = parseTestHeader(t, page)
	}
	ok, err := SlottedReplace(page, &header, 1, 2, []byte("bbb"))
	if err != nil || !ok {
		t.Fatalf("replace: %v %v", ok, err)
	}
	header = parseTestHeader(t, page)
	if err := SlottedRemove(page, &header, 3, 2); err != nil {
		t.Fatalf("remove: %v", err)
	}
	header = parseTestHeader(t, page)
	if _, err := SlottedTruncate(page, &header, 3); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	header = parseTestHeader(t, page)
	want := []string{"aa", "bbb", "cc"}
	for index, cell := range want {
		length := 2
		if index == 1 {
			length = 3
		}
		if got := string(readFixedCell(t, page, &header, index, length)); got != cell {
			t.Fatalf("record %d = %q, want %q", index, got, cell)
		}
	}
	freeZero(t, page, &header)
}

// fixedUnorderedPage builds the Rust fixed_unordered_page: ten 4-byte cells
// whose physical order is [0,1,2,3,4,5,6,7,8,9]-adjacent but whose logical
// order is the natural 0..9 (the cells were inserted at their value
// positions, so physical order is [9..0]-ish; the payload stays packed).
func fixedUnorderedPage(t *testing.T) ([]byte, PageHeader) {
	t.Helper()
	// Build 0,3,6,9 with the builder, then insert 1,2,4,5,7,8 at their
	// value positions (Rust fixed_unordered_page).
	page := testPage()
	var header PageHeader
	b := NewSlottedBuilder(page, 2, 7, 0, 4)
	for _, value := range []uint32{0, 3, 6, 9} {
		cell := make([]byte, 4)
		PutU32(cell, value)
		if err := b.Push(page, cell); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finish(page); err != nil {
		t.Fatal(err)
	}
	header = parseTestHeader(t, page)
	for _, value := range []uint32{1, 2, 4, 5, 7, 8} {
		cell := make([]byte, 4)
		PutU32(cell, value)
		ok, err := SlottedInsert(page, &header, int(value), cell)
		if err != nil || !ok {
			t.Fatalf("insert %d: %v %v", value, ok, err)
		}
		header = parseTestHeader(t, page)
	}
	return page, header
}

// TestFixedRunRemovalCompactsUnorderedPayloadOnce mirrors the Rust fixed
// run-removal vectors for physical-order divergence.
func TestFixedRunRemovalCompactsUnorderedPayloadOnce(t *testing.T) {
	for _, tc := range []struct{ start, count int }{{0, 3}, {3, 4}, {7, 3}} {
		page, header := fixedUnorderedPage(t)
		shape, err := SlottedRemoveFixedRange(page, &header, tc.start, tc.count, 4)
		if err != nil {
			t.Fatalf("[%d,%d): %v", tc.start, tc.count, err)
		}
		var want []uint32
		for value := 0; value < 10; value++ {
			if value < tc.start || value >= tc.start+tc.count {
				want = append(want, uint32(value))
			}
		}
		got := fixedValues(t, page, &PageHeader{ItemCount: uint16(shape.ItemCount), Lower: shape.Lower, Upper: shape.Upper}, 4)
		if len(got) != len(want) {
			t.Fatalf("[%d,%d): got %d records, want %d", tc.start, tc.count, len(got), len(want))
		}
		for at := range want {
			if got[at] != want[at] {
				t.Fatalf("[%d,%d): record %d = %d, want %d", tc.start, tc.count, at, got[at], want[at])
			}
		}
		if int(shape.Upper)+shape.ItemCount*4 != PageSize {
			t.Fatalf("[%d,%d): payload not packed", tc.start, tc.count)
		}
		freeZero(t, page, &PageHeader{ItemCount: uint16(shape.ItemCount), Lower: shape.Lower, Upper: shape.Upper})
	}
}

// TestFixedTruncateKeepsLogicalPrefix verifies truncate_fixed on an
// unordered fixed payload keeps the first keep logical cells.
func TestFixedTruncateKeepsLogicalPrefix(t *testing.T) {
	page, header := fixedUnorderedPage(t)
	shape, err := SlottedTruncateFixed(page, &header, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	got := fixedValues(t, page, &PageHeader{ItemCount: uint16(shape.ItemCount), Lower: shape.Lower, Upper: shape.Upper}, 4)
	want := []uint32{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for at := range want {
		if got[at] != want[at] {
			t.Fatalf("record %d = %d, want %d", at, got[at], want[at])
		}
	}
	if int(shape.Upper)+shape.ItemCount*4 != PageSize {
		t.Fatal("payload not packed")
	}
	freeZero(t, page, &PageHeader{ItemCount: uint16(shape.ItemCount), Lower: shape.Lower, Upper: shape.Upper})
}

// TestInPlaceInsertionDoesNotModifyAFullPage mirrors the Rust full-page
// refusal vector.
func TestInPlaceInsertionDoesNotModifyAFullPage(t *testing.T) {
	page := testPage()
	b := NewSlottedBuilder(page, 2, 7, 0, 4)
	if err := b.Push(page, make([]byte, PageSize-SlottedHeaderSize-2)); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(page); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), page...)
	header := parseTestHeader(t, page)
	ok, err := SlottedInsert(page, &header, 1, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("insert into full page accepted")
	}
	for at := range before {
		if page[at] != before[at] {
			t.Fatalf("full page modified at %d", at)
		}
	}
}

// TestInPlaceEditsClearVacatedRecordBytes mirrors the Rust vacated-bytes
// zeroing vector.
func TestInPlaceEditsClearVacatedRecordBytes(t *testing.T) {
	page, header := buildCells(t, [][]byte{
		[]byte("aaaa"), []byte("bbbb"), []byte("cccc"), []byte("dddd"),
	})
	ok, err := SlottedReplace(page, &header, 1, 4, []byte("b"))
	if err != nil || !ok {
		t.Fatalf("replace: %v %v", ok, err)
	}
	header = parseTestHeader(t, page)
	freeZero(t, page, &header)

	if err := SlottedRemove(page, &header, 2, 4); err != nil {
		t.Fatalf("remove: %v", err)
	}
	header = parseTestHeader(t, page)
	freeZero(t, page, &header)

	if _, err := SlottedTruncate(page, &header, 2); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	header = parseTestHeader(t, page)
	freeZero(t, page, &header)
	if got := string(readFixedCell(t, page, &header, 0, 4)); got != "aaaa" {
		t.Fatalf("record 0 = %q", got)
	}
	if got := string(readFixedCell(t, page, &header, 1, 1)); got != "b" {
		t.Fatalf("record 1 = %q", got)
	}
}

// TestTruncateDetectsDuplicateOffsets mirrors the Rust duplicate-offset
// refusal vector: truncate validates before touching the page.
func TestTruncateDetectsDuplicateOffsets(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aa"), []byte("bb")})
	duplicate := int(U16(page[SlottedHeaderSize:]))
	PutU16(page[SlottedHeaderSize+2:], uint16(duplicate))
	before := append([]byte(nil), page...)
	if _, err := SlottedTruncate(page, &header, 1); err == nil {
		t.Fatal("truncate accepted duplicate offsets")
	}
	for at := range before {
		if page[at] != before[at] {
			t.Fatalf("page modified at %d on failed truncate", at)
		}
	}
}

// TestFixedPositionsRejectsBrokenPayloads verifies the packed-payload
// validation of fixedPositions (misalignment, gap, overlap).
func TestFixedPositionsRejectsBrokenPayloads(t *testing.T) {
	page, header := fixedUnorderedPage(t)

	// Misaligned slot: point slot 0 at upper+1 (not a cell boundary).
	PutU16(page[SlottedHeaderSize:], uint16(int(header.Upper)+1))
	if _, err := fixedPositions(page, &header, 4); err == nil {
		t.Fatal("misaligned slot accepted")
	}
	page, header = fixedUnorderedPage(t)

	// Gap: move slot 1 below its packed position, leaving a hole.
	PutU16(page[SlottedHeaderSize+2:], uint16(int(header.Upper)+2*4+8))
	if _, err := fixedPositions(page, &header, 4); err == nil {
		t.Fatal("gapped payload accepted")
	}
	page, header = fixedUnorderedPage(t)

	// Overlap: point the last logical slot at the first cell.
	PutU16(page[SlottedHeaderSize+9*2:], header.Upper)
	if _, err := fixedPositions(page, &header, 4); err == nil {
		t.Fatal("overlapping slots accepted")
	}
}
