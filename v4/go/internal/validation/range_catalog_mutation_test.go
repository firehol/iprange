package validation

// Slice-C mutation tests: targeted corruptions of the conformance
// corpus and synthetic multi-level range trees produce the exact Rust
// finding classes, in walk order, on the page that owns the defect.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// mappingMapFile maps one database file read-only.
func mappingMapFile(file *os.File, size uint64) (*mapping.Mapping, error) {
	return mapping.MapFile(file, size, false)
}

// bootstrapOpen opens the meta pair of one immutable database.
func bootstrapOpen(p0, p1 []byte, size uint64) (*bootstrap.Result, error) {
	return bootstrap.Open(p0, p1, size, bootstrap.ModeImmutableReader)
}

// mutationHeader parses one page header from the raw fields without the
// transaction check (the corpus fixtures commit at different
// generations; the mutation view only needs the geometry).
func mutationHeader(page []byte) format.PageHeader {
	return format.PageHeader{
		PageType:  format.PageType(page[4]),
		BornTxn:   format.U64(page[8:16]),
		ItemCount: uint16(format.U16(page[16:18])),
		Level:     uint16(format.U16(page[18:20])),
		Lower:     uint16(format.U16(page[20:22])),
		Upper:     uint16(format.U16(page[22:24])),
		Aux:       format.U32(page[24:28]),
	}
}

// itemCountOf reads the item count of one page header.
func itemCountOf(page []byte) int {
	return int(format.U16(page[format.HeaderCount:]))
}

// boundedRecord views one slotted record strictly inside its own
// extent. The slot table descends (the writer fills the record area
// from the page top down), so every extent starts at its slot and runs
// fixedLen bytes (fixed cells) or its own length word (variable
// records); the slotted Record view itself runs to the page end and
// must never be written past the record.
func boundedRecord(sl format.SlottedPage, index int, fixedLen int) []byte {
	raw, err := sl.Record(index)
	if err != nil {
		panic(err)
	}
	length := fixedLen
	if length == 0 {
		if len(raw) < 2 {
			panic("record is too short for its length word")
		}
		length = int(format.U16(raw[0:2]))
	}
	if length <= 0 || length > len(raw) {
		panic("record extent is invalid")
	}
	return raw[:length]
}

// slotStart reads one slot offset of a slotted page.
func slotStart(sl format.SlottedPage, index int) int {
	return int(format.U16(sl.Page[format.SlottedHeaderSize+index*2 : format.SlottedHeaderSize+index*2+2]))
}

// rightmostIndexLeaf descends the catalog index tree to its rightmost
// leaf and returns the leaf bytes and its page number.
func rightmostIndexLeaf(raw []byte, pages int) ([]byte, int) {
	pageNumber := int(format.U32(raw[152:156])) // meta CatalogIndexRoot
	for {
		page := raw[pageNumber*format.PageSize : (pageNumber+1)*format.PageSize]
		header := mutationHeader(page)
		if header.Level == 0 {
			return page, pageNumber
		}
		sl, err := format.OpenSlottedHeader(page, header, format.PageTypeCatalogIndexBranch, 0, format.SlotItemsPerPage)
		if err != nil {
			panic(err)
		}
		record, err := sl.Record(int(header.ItemCount) - 1)
		if err != nil {
			panic(err)
		}
		_, child, err := format.DecodeCatalogIndexBranchFields(record)
		if err != nil {
			panic(err)
		}
		pageNumber = int(child)
		if pageNumber < 2 || pageNumber >= pages {
			panic("index child outside the database")
		}
	}
}

// corpusCopy copies one fixture into a fresh file, applies the mutate
// hook to the whole raw file, and re-seals any page touched by the hook
// (the hook reports which page indices changed).
func corpusCopy(t *testing.T, fixture string, mutate func(raw []byte, pages int) []int) string {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	touched := mutate(raw, len(raw)/format.PageSize)
	for _, page := range touched {
		if page >= 2 {
			if err := format.SealPageChecksum(raw[page*format.PageSize : (page+1)*format.PageSize]); err != nil {
				t.Fatal(err)
			}
		} else {
			// Meta pages carry the meta CRC in their tail.
			meta := raw[page*format.PageSize : (page+1)*format.PageSize]
			format.PutU32(meta[252:256], format.MetaCRC32C(meta))
		}
	}
	path := filepath.Join(t.TempDir(), "database.iprdb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fixturePathContext is fixtureContext over an arbitrary database path.
func fixturePathContext(t *testing.T, path string, maxHeap uint64) *context {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mappingMapFile(file, uint64(info.Size()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p0, err := m.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := bootstrapOpen(p0, p1, uint64(info.Size()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := newContext(m, res.Meta, HeapOnly(maxHeap, 1), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// rangeLeafPage opens one range leaf page of a copied fixture for the
// mutation helpers.
func rangeLeafPage(page []byte) format.SlottedPage {
	header := mutationHeader(page)
	if header.PageType != format.PageTypeRangeLeaf {
		panic("range root is not a leaf page")
	}
	sl, err := format.OpenSlottedHeader(page, header, format.PageTypeRangeLeaf, header.Aux, format.SlotItemsPerPage)
	if err != nil {
		panic(err)
	}
	return sl
}

func TestValidateRangeReversedFinding(t *testing.T) {
	// The last IPv4 record becomes reversed: exactly one
	// RangeReversed finding on the leaf page (the neighbor state resets,
	// so nothing follows).
	path := corpusCopy(t, "direct-ipv4.iprdb", func(raw []byte, pages int) []int {
		page := raw[2*format.PageSize : 3*format.PageSize]
		sl := rangeLeafPage(page)
		record, err := sl.Record(itemCountOf(page) - 1)
		if err != nil {
			panic(err)
		}
		from := format.U32(record[0:4])
		format.PutU32(record[4:8], from-1)
		return []int{2}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonRangeReversed ||
		findings[0].Object != ObjectRangeTree || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeOverlapFinding(t *testing.T) {
	// The first record's end reaches the next record's start: exactly
	// one RangeOverlap finding on the following record.
	path := corpusCopy(t, "direct-ipv4.iprdb", func(raw []byte, pages int) []int {
		page := raw[2*format.PageSize : 3*format.PageSize]
		sl := rangeLeafPage(page)
		first := boundedRecord(sl, 0, format.RangeRecordV4Size)
		second := boundedRecord(sl, 1, format.RangeRecordV4Size)
		format.PutU32(first[4:8], format.U32(second[0:4]))
		return []int{2}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonRangeOverlap || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeNotCoalescedFinding(t *testing.T) {
	// The second record starts exactly where the first ends and the
	// values match: the pair reports the RangeNotCoalesced class. The
	// other pairs keep their canonical separation and values, so this
	// is the only finding.
	path := corpusCopy(t, "direct-ipv4.iprdb", func(raw []byte, pages int) []int {
		page := raw[2*format.PageSize : 3*format.PageSize]
		sl := rangeLeafPage(page)
		first := boundedRecord(sl, 0, format.RangeRecordV4Size)
		second := boundedRecord(sl, 1, format.RangeRecordV4Size)
		secondFrom := format.U32(second[0:4])
		format.PutU32(first[4:8], secondFrom-1) // close the gap
		format.PutU32(second[8:12], format.U32(first[8:12]))
		return []int{2}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) == 0 || findings[0].Reason != ReasonRangeNotCoalesced || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
	for _, finding := range findings {
		if finding.Reason != ReasonRangeNotCoalesced {
			t.Fatalf("findings %+v", findings)
		}
	}
}

func TestValidateRangeCountMismatch(t *testing.T) {
	// A declared record count one too large is the RootCountInvalid
	// finding without a page.
	path := corpusCopy(t, "direct-ipv4.iprdb", func(raw []byte, pages int) []int {
		for _, meta := range []int{0, 1} {
			page := raw[meta*format.PageSize : (meta+1)*format.PageSize]
			format.PutU64(page[80:88], format.U64(page[80:88])+1)
		}
		return []int{0, 1}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonRootCountInvalid || findings[0].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeMembershipZeroValue(t *testing.T) {
	// A zero membership value in a range record is the
	// MembershipBitmapInvalid class (Rust KIND 1 arm).
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		root := 8
		page := raw[root*format.PageSize : (root+1)*format.PageSize]
		sl := rangeLeafPage(page)
		record := boundedRecord(sl, 0, format.RangeRecordV4Size)
		format.PutU32(record[8:12], 0)
		return []int{root}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonMembershipBitmapInvalid || *findings[0].PageNumber != 8 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeStructuredZeroValue(t *testing.T) {
	// A zero structured value in a range record is the StructureMissing
	// class (Rust KIND 2 arm).
	path := corpusCopy(t, "structured-ipv4.iprdb", func(raw []byte, pages int) []int {
		root := 11
		page := raw[root*format.PageSize : (root+1)*format.PageSize]
		sl := rangeLeafPage(page)
		record := boundedRecord(sl, 0, format.RangeRecordV4Size)
		format.PutU32(record[8:12], 0)
		return []int{root}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonStructureMissing || *findings[0].PageNumber != 11 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateCatalogIndexLimitFinding(t *testing.T) {
	// The last index-tree entry moves to the declared limit: the walk
	// reports the record bijection on its leaf page first, then the
	// cross-check cursor folds the same defect into a no-page bijection
	// finding.
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		leaf, page := rightmostIndexLeaf(raw, pages)
		limit := int(format.U64(raw[96:104]))
		sl, err := format.OpenSlottedHeader(leaf, mutationHeader(leaf), format.PageTypeCatalogIndexLeaf, 0, format.SlotItemsPerPage)
		if err != nil {
			panic(err)
		}
		record := boundedRecord(sl, itemCountOf(leaf)-1, 0)
		format.PutU32(record[4:8], uint32(limit))
		return []int{page}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateCatalog)
	if len(findings) < 2 || findings[0].Reason != ReasonCatalogBijectionInvalid ||
		findings[0].Object != ObjectCatalogIndexTree {
		t.Fatalf("findings %+v", findings)
	}
	for _, finding := range findings[1:] {
		if finding.Reason != ReasonCatalogBijectionInvalid {
			t.Fatalf("findings %+v", findings)
		}
	}
}

func TestValidateCatalogNameGrammarFinding(t *testing.T) {
	// One name byte becomes invalid grammar (uppercase): the layout
	// proof still passes and the leaf decode reports the
	// CatalogNameInvalid class on the name leaf page.
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		page := raw[2*format.PageSize : 3*format.PageSize]
		sl, err := format.OpenSlottedHeader(page, mutationHeader(page), format.PageTypeCatalogNameLeaf, 0, format.SlotItemsPerPage)
		if err != nil {
			panic(err)
		}
		first := boundedRecord(sl, 0, 0)
		nameLen := int(first[8])
		first[12+nameLen-1] = 'A' // 'a' -> 'A' keeps the length and the extent
		return []int{2}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateCatalog)
	want := []ValidationReason{ReasonCatalogNameInvalid, ReasonRootCountInvalid, ReasonCatalogBijectionInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v, want %v", i, findings[i], reason)
		}
	}
	if findings[0].Object != ObjectCatalogNameTree || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateCatalogUsedBitmapCountFinding(t *testing.T) {
	// Clearing one set bit of the feed used bitmap: the set-bit count
	// no longer matches (CatalogBitmapInvalid without a page) and the
	// cross-check reports the missing feed as a bijection finding.
	path := corpusCopy(t, "membership-ipv4.iprdb", func(raw []byte, pages int) []int {
		page := raw[4*format.PageSize : 5*format.PageSize]
		// 70 feeds fit into one bitmap leaf word window; clear the
		// first set word bit (index 0 is the first candidate for
		// feeds).
		wordOff := format.SlottedHeaderSize
		for index := 0; index < format.BitmapLeafWords; index++ {
			word := format.U64(page[wordOff+index*8 : wordOff+index*8+8])
			if word != 0 {
				bit := uint64(0)
				for word&(1<<bit) == 0 {
					bit++
				}
				format.PutU64(page[wordOff+index*8:wordOff+index*8+8], word & ^(1<<bit))
				break
			}
		}
		return []int{4}
	})
	ctx := fixturePathContext(t, path, 1<<30)
	findings := collectContextFindings(t, ctx, validateCatalog)
	if len(findings) < 2 || findings[0].Reason != ReasonCatalogBitmapInvalid ||
		findings[0].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
	for _, finding := range findings[1:] {
		if finding.Reason != ReasonCatalogBijectionInvalid {
			t.Fatalf("findings %+v", findings)
		}
	}
}
