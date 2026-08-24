package format

// Unit tests for the explicit-validation page inspection authorities:
// the tree-header problem classification order and the fixed/variable
// cell layout proofs (Rust slotted_page.rs inspect_tree_header and
// inspect_layout).

import "testing"

func inspectPage(t *testing.T, pageType byte, level uint16, slots []int, mutate func([]byte)) []byte {
	t.Helper()
	page := make([]byte, PageSize)
	copy(page[:4], PageMagic[:])
	page[4] = pageType
	PutU16(page[6:8], 32)
	PutU64(page[8:16], 1)
	PutU16(page[16:18], uint16(len(slots)))
	PutU16(page[18:20], level)
	PutU16(page[20:22], uint16(32+2*len(slots)))
	lower := uint16(PageSize)
	for i, start := range slots {
		PutU16(page[32+2*i:], uint16(start))
		if start < int(lower) {
			lower = uint16(start)
		}
	}
	PutU16(page[22:24], lower)
	if mutate != nil {
		mutate(page)
	}
	return page
}

func TestInspectTreeHeaderProblems(t *testing.T) {
	level := uint16(0)
	expect := func(name string, mutate func([]byte), want TreeHeaderProblem) {
		t.Helper()
		page := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4080}, mutate)
		_, problem := InspectTreeHeader(page, 1, byte(PageTypeRetirementBranch), byte(PageTypeRetirementLeaf), 0, &level)
		if problem != want {
			t.Fatalf("%s: problem %v, want %v", name, problem, want)
		}
	}
	expect("valid", nil, TreeHeaderProblemNone)
	expect("bad magic", func(p []byte) { p[0] = 'X' }, TreeHeaderProblemHeader)
	expect("flags", func(p []byte) { p[5] = 1 }, TreeHeaderProblemHeader)
	expect("header size", func(p []byte) { PutU16(p[6:8], 31) }, TreeHeaderProblemHeader)
	expect("born zero", func(p []byte) { PutU64(p[8:16], 0) }, TreeHeaderProblemBorn)
	expect("born future", func(p []byte) { PutU64(p[8:16], 2) }, TreeHeaderProblemBorn)
	expect("type", func(p []byte) { p[4] = byte(PageTypeRetirementBranch) }, TreeHeaderProblemType)
	expect("aux", func(p []byte) { PutU32(p[24:28], 7) }, TreeHeaderProblemType)
	// A leaf-typed page at level 2 fails the kind-at-level check first
	// (Type); the Level class needs an identity-valid branch level with a
	// mismatched expected level.
	expect("kind at level", func(p []byte) { PutU16(p[18:20], 2) }, TreeHeaderProblemType)
	levelPage := inspectPage(t, byte(PageTypeRetirementBranch), 2, []int{4080}, nil)
	if _, problem := InspectTreeHeader(levelPage, 1, byte(PageTypeRetirementBranch), byte(PageTypeRetirementLeaf), 0, &level); problem != TreeHeaderProblemLevel {
		t.Fatalf("branch level mismatch: %v", problem)
	}
	expect("level expected", func(p []byte) { PutU16(p[18:20], 0) }, TreeHeaderProblemNone)
	expect("empty", func(p []byte) { PutU16(p[16:18], 0); PutU16(p[20:22], 32) }, TreeHeaderProblemShape)
	expect("lower", func(p []byte) { PutU16(p[20:22], 40) }, TreeHeaderProblemShape)
	expect("upper page size", func(p []byte) { PutU16(p[22:24], PageSize) }, TreeHeaderProblemShape)
}

func TestInspectTreeHeaderBranchLevel(t *testing.T) {
	// A branch-typed page at level 1 is valid with the branch identity
	// and expected level 1; the same identity fails at level 0 (Type)
	// and at expected level 2 (Level).
	level := uint16(1)
	page := inspectPage(t, byte(PageTypeRetirementBranch), 1, []int{4080}, nil)
	if _, problem := InspectTreeHeader(page, 1, byte(PageTypeRetirementBranch), byte(PageTypeRetirementLeaf), 0, &level); problem != TreeHeaderProblemNone {
		t.Fatalf("branch level 1: %v", problem)
	}
	lev0 := uint16(0)
	if _, problem := InspectTreeHeader(page, 1, byte(PageTypeRetirementBranch), byte(PageTypeRetirementLeaf), 0, &lev0); problem != TreeHeaderProblemLevel {
		t.Fatalf("branch mismatch: %v", problem)
	}
	PutU16(page[18:20], 0)
	if _, problem := InspectTreeHeader(page, 1, byte(PageTypeRetirementBranch), byte(PageTypeRetirementLeaf), 0, &level); problem != TreeHeaderProblemType {
		t.Fatalf("leaf identity at level 0: %v", problem)
	}
}

func TestInspectLayoutFixed(t *testing.T) {
	fixed := FixedLayout(16)
	valid := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4064, 4048}, nil)
	// Packed cells: minimum start equals upper.
	if inspection := InspectLayout(valid, mustHeader(t, valid), fixed); inspection == nil || inspection.ReservedNonzero {
		t.Fatalf("valid layout refused: %+v", inspection)
	}
	// Overlapping cells fail the extent proof.
	overlap := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4064, 4064}, nil)
	if inspection := InspectLayout(overlap, mustHeader(t, overlap), fixed); inspection != nil {
		t.Fatal("overlap accepted")
	}
	// A cell below upper fails.
	below := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4080}, func(p []byte) {
		PutU16(p[22:24], 4090)
	})
	if inspection := InspectLayout(below, mustHeader(t, below), fixed); inspection != nil {
		t.Fatal("cell below upper accepted")
	}
	// A cell overrunning the page fails.
	overrun := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4088}, nil)
	if inspection := InspectLayout(overrun, mustHeader(t, overrun), fixed); inspection != nil {
		t.Fatal("overrunning cell accepted")
	}
	// A nonzero reserved byte is reported, not refused.
	reserved := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4064, 4048}, func(p []byte) {
		p[100] = 1
	})
	if inspection := InspectLayout(reserved, mustHeader(t, reserved), fixed); inspection == nil || !inspection.ReservedNonzero {
		t.Fatalf("reserved byte missed: %+v", inspection)
	}
	// A nonzero byte in an unmarked gap is reported.
	gap := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4080, 4000}, func(p []byte) {
		p[4048] = 1
	})
	if inspection := InspectLayout(gap, mustHeader(t, gap), fixed); inspection == nil || !inspection.ReservedNonzero {
		t.Fatalf("unmarked byte missed: %+v", inspection)
	}
	// The same gap with a zero byte is clean.
	cleanGap := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4080, 4000}, nil)
	if inspection := InspectLayout(cleanGap, mustHeader(t, cleanGap), fixed); inspection == nil || inspection.ReservedNonzero {
		t.Fatalf("zero gap reported: %+v", inspection)
	}
}

func TestInspectLayoutVariable(t *testing.T) {
	variable := VariableLayout(2, 40)
	// One record of 6 bytes at 4088; minimum start equals upper.
	page := inspectPage(t, byte(PageTypeCatalogNameLeaf), 0, []int{4088}, func(p []byte) {
		PutU16(p[4088:4090], 6)
		copy(p[4090:4094], "name")
	})
	if inspection := InspectLayout(page, mustHeader(t, page), variable); inspection == nil || inspection.ReservedNonzero {
		t.Fatalf("valid variable layout refused: %+v", inspection)
	}
	// Two records: the first record spans [4060,4064) (2-byte length
	// prefix plus 2 payload bytes), the second [4088,4094), leaving an
	// all-zero gap between them.
	two := inspectPage(t, byte(PageTypeCatalogNameLeaf), 0, []int{4088, 4060}, func(p []byte) {
		PutU16(p[4088:4090], 6)
		copy(p[4090:4094], "name")
		PutU16(p[4060:4062], 4)
		copy(p[4062:4064], "fe")
	})
	if inspection := InspectLayout(two, mustHeader(t, two), variable); inspection == nil || inspection.ReservedNonzero {
		t.Fatalf("two-record layout refused: %+v", inspection)
	}
	// Overlapping records fail (record at 4084 spans [4084,4092) and
	// overlaps the record at 4088).
	overlap := inspectPage(t, byte(PageTypeCatalogNameLeaf), 0, []int{4088, 4084}, func(p []byte) {
		PutU16(p[4088:4090], 6)
		copy(p[4090:4094], "name")
		PutU16(p[4084:4086], 8)
		copy(p[4086:4090], "xyz")
	})
	if inspection := InspectLayout(overlap, mustHeader(t, overlap), variable); inspection != nil {
		t.Fatal("overlapping records accepted")
	}
	// A record length outside the bounds fails.
	long := inspectPage(t, byte(PageTypeCatalogNameLeaf), 0, []int{4080}, func(p []byte) {
		PutU16(p[4080:4082], 41)
	})
	if inspection := InspectLayout(long, mustHeader(t, long), variable); inspection != nil {
		t.Fatal("over-long record accepted")
	}
	// A record length that overruns the page fails.
	short := inspectPage(t, byte(PageTypeCatalogNameLeaf), 0, []int{4090}, func(p []byte) {
		PutU16(p[4090:4092], 8)
	})
	if inspection := InspectLayout(short, mustHeader(t, short), variable); inspection != nil {
		t.Fatal("overrunning record accepted")
	}
}

func TestInspectLayoutCells(t *testing.T) {
	fixed := FixedLayout(16)
	page := inspectPage(t, byte(PageTypeRetirementLeaf), 0, []int{4064, 4048}, func(p []byte) {
		copy(p[4048:4064], "abcdefghijklmnop")
		copy(p[4064:4080], "0123456789abcdef")
	})
	inspection := InspectLayout(page, mustHeader(t, page), fixed)
	if inspection == nil {
		t.Fatal("layout refused")
	}
	cells := inspection.Cells()
	first, ok := cells.Next()
	if !ok || string(first[:4]) != "0123" {
		t.Fatalf("first cell %q ok %v", first, ok)
	}
	second, ok := cells.Next()
	if !ok || string(second[:4]) != "abcd" {
		t.Fatalf("second cell %q ok %v", second, ok)
	}
	if _, ok := cells.Next(); ok {
		t.Fatal("extra cell")
	}
}

func mustHeader(t *testing.T, page []byte) *PageHeader {
	t.Helper()
	h, err := DecodePageHeader(page, 1)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &h
}

func TestMetadataChunkFields(t *testing.T) {
	page := make([]byte, PageSize)
	copy(page[:4], PageMagic[:])
	PutU32(page[32:36], 0) // next
	PutU16(page[36:38], 4) // length
	PutU64(page[40:48], 0) // offset
	PutU16(page[16:18], 1) // item count
	PutU16(page[20:22], 48+4)
	PutU16(page[22:24], PageSize)
	copy(page[48:52], "data")
	chunk, ok := MetadataChunkFields(page, 3, 8, 0, 4)
	if !ok || chunk.ChunkLen != 4 || chunk.Next != 0 || string(chunk.Data) != "data" {
		t.Fatalf("valid chunk %+v ok %v", chunk, ok)
	}
	if ok := MetadataChunkTailZero(page, 4); !ok {
		t.Fatal("zero tail reported nonzero")
	}
	page[60] = 1
	if ok := MetadataChunkTailZero(page, 4); ok {
		t.Fatal("nonzero tail reported zero")
	}
	page[60] = 0
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 5); ok {
		t.Fatal("length mismatch accepted")
	}
	PutU64(page[40:48], 1)
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 4); ok {
		t.Fatal("offset mismatch accepted")
	}
	PutU64(page[40:48], 0)
	PutU16(page[38:40], 1)
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 4); ok {
		t.Fatal("reserved word accepted")
	}
	PutU16(page[38:40], 0)
	PutU32(page[32:36], 3) // self link
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 4); ok {
		t.Fatal("self link accepted")
	}
	PutU32(page[32:36], 1) // meta page link
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 4); ok {
		t.Fatal("meta-page link accepted")
	}
	PutU32(page[32:36], 2) // nonfinal needs a nonzero next
	if _, ok := MetadataChunkFields(page, 3, 8, 0, 4); ok {
		t.Fatal("truncated chain accepted")
	}
}
