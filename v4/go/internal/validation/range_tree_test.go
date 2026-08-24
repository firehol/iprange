package validation

// Multi-level range tree tests: the synthetic IPv4 two-level tree pins
// the branch order and fence findings and the degenerate-root class,
// with the exact page attributions and walk order.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// rangeTreeLeaf builds one range leaf page of descending records (the
// writer record layout: records from the top down, slots in key order).
func rangeTreeLeaf(t *testing.T, born uint64, records []format.RangeRecordV4, upper int) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeRangeLeaf)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], born)
	format.PutU16(page[16:18], uint16(len(records)))
	format.PutU16(page[18:20], 0)
	format.PutU16(page[20:22], uint16(format.SlottedHeaderSize+2*len(records)))
	format.PutU16(page[22:24], uint16(upper))
	format.PutU32(page[24:28], 4) // aux = IPv4 family
	cursor := upper
	for i, record := range records {
		if err := format.EncodeRangeRecordV4(record, page[cursor:cursor+format.RangeRecordV4Size]); err != nil {
			t.Fatal(err)
		}
		format.PutU16(page[32+i*2:34+i*2], uint16(cursor))
		cursor += format.RangeRecordV4Size
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// rangeTreeBranch builds one IPv4 range branch page with the given
// (first key, child) entries and returns the page plus its record area
// base for slot rewriting by the mutations.
func rangeTreeBranch(t *testing.T, entries [][2]uint32) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeRangeBranch)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(entries)))
	format.PutU16(page[18:20], 1) // level
	format.PutU16(page[20:22], uint16(format.SlottedHeaderSize+2*len(entries)))
	format.PutU16(page[22:24], uint16(4064))
	format.PutU32(page[24:28], 4) // aux = IPv4 family
	for i, entry := range entries {
		format.PutU32(page[4064+i*8:4068+i*8], entry[0])
		format.PutU32(page[4068+i*8:4072+i*8], entry[1])
		format.PutU16(page[32+i*2:34+i*2], uint16(4064+i*8))
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// rangeTreeMeta builds the meta of a synthetic database with the
// declared record count and page count and the range root at page 2.
func rangeTreeMeta(t *testing.T, recordCount uint64, pageCount uint64) []byte {
	t.Helper()
	meta := metaPage(2, pageCount)
	format.PutU64(meta[80:88], recordCount)
	format.PutU32(meta[144:148], 2) // RangeRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return meta
}

// writeRangeTreeDB writes the synthetic range database (meta page 0,
// zero page 1, branch at page 2, leaves at page 3 onward) and returns
// its path.
func writeRangeTreeDB(t *testing.T, recordCount uint64, branch []byte, leaves ...[]byte) string {
	t.Helper()
	pages := make([][]byte, 0, len(leaves)+1)
	pages = append(pages, branch)
	pages = append(pages, leaves...)
	meta := rangeTreeMeta(t, recordCount, uint64(2+len(pages)))
	path := filepath.Join(t.TempDir(), "database.iprdb")
	if err := writePages(path, meta, pages...); err != nil {
		t.Fatal(err)
	}
	return path
}

// rangeTreeClean builds the clean two-level tree: two leaves of two
// records each under one branch (ranges 0-1999 and 2000-3999, values
// distinct so the coalescing rule never fires).
func rangeTreeClean(t *testing.T, branch []byte) (string, []byte, []byte) {
	t.Helper()
	leaf0 := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 0, To: 999, Value: 1},
		{From: 1000, To: 1999, Value: 2},
	}, 4056)
	leaf1 := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 2000, To: 2999, Value: 3},
		{From: 3000, To: 3999, Value: 4},
	}, 4056)
	return writeRangeTreeDB(t, 4, branch, leaf0, leaf1), leaf0, leaf1
}

// writePages writes one database file: the meta page, a zero second
// meta page, and the given data pages at page 2 onward.
func writePages(path string, meta []byte, pages ...[]byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(meta); err != nil {
		return err
	}
	if _, err := f.Write(make([]byte, format.PageSize)); err != nil {
		return err
	}
	for _, page := range pages {
		if _, err := f.Write(page); err != nil {
			return err
		}
	}
	return f.Close()
}

// readFileBytes reads one database file.
func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFileBytes rewrites one database file.
func writeFileBytes(path string, raw []byte) error {
	return os.WriteFile(path, raw, 0o600)
}

// openSlottedForTest opens one slotted page for the mutation helpers.
func openSlottedForTest(page []byte) (format.SlottedPage, error) {
	header := mutationHeader(page)
	return format.OpenSlottedHeader(page, header, header.PageType, header.Aux, format.SlotItemsPerPage)
}

func TestValidateRangeMultiLevelClean(t *testing.T) {
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}, {2000, 4}})
	path, _, _ := rangeTreeClean(t, branch)
	ctx := fixturePathContext(t, path, 1<<20)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeBranchOrderAndFence(t *testing.T) {
	// Swapping the first keys of the two branch entries: the child
	// fences fire first (each key no longer matches its child's first
	// record), then the on-page order finding, and again the fence of
	// the second entry.
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}, {2000, 4}})
	// Swap the first-key fields.
	first := format.U32(branch[4064:4068])
	format.PutU32(branch[4064:4068], format.U32(branch[4072:4076]))
	format.PutU32(branch[4072:4076], first)
	if err := format.SealPageChecksum(branch); err != nil {
		t.Fatal(err)
	}
	path, _, _ := rangeTreeClean(t, branch)
	ctx := fixturePathContext(t, path, 1<<20)
	findings := collectContextFindings(t, ctx, validateRange)
	want := []ValidationReason{ReasonTreeFenceInvalid, ReasonTreeOrderInvalid, ReasonTreeFenceInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason || findings[i].Object != ObjectRangeTree || *findings[i].PageNumber != 2 {
			t.Fatalf("finding %d: %+v, want %v on page 2", i, findings[i], reason)
		}
	}
}

func TestValidateRangeFenceFinding(t *testing.T) {
	// The first record of the second leaf moves inside its range: the
	// leaf stays ordered (no overlap: the record shifts with its end),
	// and the branch fence reports the drifted first key.
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}, {2000, 4}})
	path, _, leaf1 := rangeTreeClean(t, branch)
	sl, err := openSlottedForTest(leaf1)
	if err != nil {
		t.Fatal(err)
	}
	record := boundedRecord(sl, 0, format.RangeRecordV4Size)
	format.PutU32(record[0:4], 2001)
	format.PutU32(record[4:8], 3000)
	record1 := boundedRecord(sl, 1, format.RangeRecordV4Size)
	format.PutU32(record1[0:4], 3001)
	format.PutU32(record1[4:8], 3999)
	if err := format.SealPageChecksum(leaf1); err != nil {
		t.Fatal(err)
	}
	raw, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	// leaf1 is page 4 (page 2 = branch, page 3 = leaf0, page 4 = leaf1).
	copy(raw[4*format.PageSize:5*format.PageSize], leaf1)
	if err := writeFileBytes(path, raw); err != nil {
		t.Fatal(err)
	}
	ctx := fixturePathContext(t, path, 1<<20)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonTreeFenceInvalid ||
		findings[0].Object != ObjectRangeTree || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRangeDegenerateRoot(t *testing.T) {
	// A level-1 root with a single record is the TreeLevelInvalid
	// class; the walk still descends and stays clean otherwise.
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}})
	leaf0 := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 0, To: 999, Value: 1},
		{From: 1000, To: 1999, Value: 2},
	}, 4056)
	path := writeRangeTreeDB(t, 2, branch, leaf0)
	ctx := fixturePathContext(t, path, 1<<20)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonTreeLevelInvalid ||
		findings[0].Object != ObjectRangeTree || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

// TestValidateRangeRefusedSubtreeResetsNeighborState mirrors Rust
// range.rs validate_node: every refused node (unreadable page, bad
// header, bad layout) clears the ordered-neighbor state, so the first
// record after a refused subtree is never compared against the last
// record before it. A corrupted middle leaf must therefore report
// only the CRC finding, never a spurious NotCoalesced against the
// sibling across the gap.
func TestValidateRangeRefusedSubtreeResetsNeighborState(t *testing.T) {
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}, {1500, 4}, {2000, 5}})
	leaf0 := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 0, To: 999, Value: 1},
		{From: 1000, To: 1999, Value: 2},
	}, 4056)
	refused := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 2000, To: 2999, Value: 5},
	}, 4056)
	format.PutU32(refused[format.PageChecksumOffset:format.PageChecksumOffset+format.PageChecksumLength], 0) // broken CRC: the leaf cannot be read
	leaf2 := rangeTreeLeaf(t, 2, []format.RangeRecordV4{
		{From: 2000, To: 2999, Value: 2},
		{From: 3000, To: 3999, Value: 6},
	}, 4056)
	path := writeRangeTreeDB(t, 4, branch, leaf0, refused, leaf2)
	ctx := fixturePathContext(t, path, 1<<20)
	findings := collectContextFindings(t, ctx, validateRange)
	if len(findings) != 1 || findings[0].Reason != ReasonPageCrcMismatch ||
		findings[0].Object != ObjectRangeTree || *findings[0].PageNumber != 4 {
		t.Fatalf("findings %+v, want the single CRC finding on page 4", findings)
	}
}
