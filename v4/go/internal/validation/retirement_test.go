package validation

// Slice-B retirement and tree-walk tests: the extent walk over crafted
// retirement trees with the exact reason classes, the tree walk shape,
// order, fence, level, cycle, and layout findings, and the clean-sweep
// PASS over the composed validators.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// treeFixturePage builds one committed slotted tree page image with the
// given slot starts and cell payloads.
func treeFixturePage(t *testing.T, pageType format.PageType, born uint64, level uint16, slots []int, cells [][]byte, aux uint32, mutate func([]byte)) []byte {
	t.Helper()
	if len(slots) != len(cells) {
		t.Fatal("slot/cell count mismatch")
	}
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(pageType)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], born)
	format.PutU16(page[16:18], uint16(len(slots)))
	format.PutU16(page[18:20], level)
	format.PutU16(page[20:22], uint16(32+2*len(slots)))
	minimum := format.PageSize
	for i, start := range slots {
		if start < minimum {
			minimum = start
		}
		format.PutU16(page[32+2*i:34+2*i], uint16(start))
		copy(page[start:], cells[i])
	}
	format.PutU16(page[22:24], uint16(minimum))
	format.PutU32(page[24:28], aux)
	if mutate != nil {
		mutate(page)
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// retirementCell builds one 16-byte retirement cell: the 12-byte key
// (transaction, first page) plus the count slot, which carries the child
// page in branch cells (Rust retirement.rs decode_branch_child).
func retirementCell(txn uint64, first, count uint32) []byte {
	cell := make([]byte, 16)
	format.PutU64(cell[0:8], txn)
	format.PutU32(cell[8:12], first)
	format.PutU32(cell[12:16], count)
	return cell
}

// retirementPage builds one retirement tree page (branch or leaf) with
// the given cells packed at the record-area tail.
func retirementPage(t *testing.T, born uint64, level uint16, cells [][]byte, mutate func([]byte)) []byte {
	t.Helper()
	slots := make([]int, len(cells))
	for i := range cells {
		slots[i] = format.PageSize - 16*(len(cells)-i)
	}
	pageType := format.PageTypeRetirementLeaf
	if level != 0 {
		pageType = format.PageTypeRetirementBranch
	}
	return treeFixturePage(t, pageType, born, level, slots, cells, 0, mutate)
}

// retirementLeaf builds one retirement leaf page carrying the extents.
func retirementLeaf(t *testing.T, extents ...[]byte) []byte {
	t.Helper()
	return retirementPage(t, 2, 0, extents, nil)
}

// dbWithMeta writes one database file: the given meta at page 0, a zero
// page 1, and the given pages from page 2 on; missing pages up to
// pageCount are written as zero pages so the physical extent matches the
// committed generation exactly (the immutable reader requires it).
func dbWithMeta(t *testing.T, meta []byte, pageCount uint64, pages ...[]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(meta); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, format.PageSize)); err != nil {
		t.Fatal(err)
	}
	for i := uint64(2); i < pageCount; i++ {
		if int(i-2) < len(pages) && pages[i-2] != nil {
			if _, err := f.Write(pages[i-2]); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := f.Write(make([]byte, format.PageSize)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// retirementDB builds one database with a retirement root at page 2 and
// the given retired-extent count; pageCount names the committed
// generation the extents live in.
func retirementDB(t *testing.T, retiredCount, pageCount uint64, pages ...[]byte) string {
	t.Helper()
	meta := metaPage(2, pageCount)
	format.PutU64(meta[136:144], retiredCount)
	format.PutU32(meta[180:184], 2) // RetirementRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return dbWithMeta(t, meta, pageCount, pages...)
}

// collectFindings runs one validation and returns the findings.
func collectFindings(t *testing.T, path string) (*ValidationResult, *ValidationFailure, []ValidationFinding) {
	t.Helper()
	var findings []ValidationFinding
	result, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	return result, failure, findings
}

// cleanRetirementMeta builds the meta of a seven-page generation with
// the retirement root at page 2, the retired-extent count, and page 5
// held by the allocator reserve.
func cleanRetirementMeta(t *testing.T, retiredCount uint64) []byte {
	t.Helper()
	meta := metaPage(2, 7)
	format.PutU64(meta[136:144], retiredCount)
	format.PutU32(meta[180:184], 2) // RetirementRoot
	format.PutU32(meta[184:188], 5) // AllocatorReserve[0] covers the gap
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	return meta
}

// cleanRetirementDBWithLeaf builds a retirement database over the given
// root leaf and returns the validation findings. Extents in the fixtures
// never touch the walk pages and never sit adjacent within one
// transaction (the Rust overlap rule requires coalescing).
func cleanRetirementDBWithLeaf(t *testing.T, retiredCount uint64, leaf []byte) []ValidationFinding {
	t.Helper()
	path := dbWithMeta(t, cleanRetirementMeta(t, retiredCount), 7, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	return findings
}

// cleanRetirementDB builds a clean-generation retirement database whose
// single leaf carries the given extents, and returns the findings.
func cleanRetirementDB(t *testing.T, retiredCount uint64, cells ...[]byte) []ValidationFinding {
	t.Helper()
	return cleanRetirementDBWithLeaf(t, retiredCount, retirementLeaf(t, cells...))
}

func TestValidateRetirementClean(t *testing.T) {
	// Two ordered non-overlapping extents covering pages 3-4 and 6, with
	// page 5 held by the allocator reserve: the root is claimed by the
	// graph walk, every other page by the allocation partition, so the
	// sweep is a clean PASS.
	path := dbWithMeta(t, cleanRetirementMeta(t, 2), 7, retirementLeaf(t,
		retirementCell(2, 3, 2),
		retirementCell(2, 6, 1),
	))
	result, _, findings := collectFindings(t, path)
	if result == nil || !result.Valid || len(findings) != 0 {
		t.Fatalf("clean retirement sweep: valid=%v findings=%+v", result, findings)
	}
	if result.Generation == nil || result.Generation.Roots[9] != 2 {
		t.Fatalf("generation roots %+v", result.Generation)
	}
	if result.Progress.CheckedUniquePages != 1 || result.Progress.ExaminedFor(ObjectRetirementTree) != 1 {
		t.Fatalf("progress %+v", result.Progress)
	}
}

func TestValidateRetirementRootCountMismatch(t *testing.T) {
	// The tree carries two records but the meta declares three.
	findings := cleanRetirementDB(t, 3,
		retirementCell(2, 3, 2),
		retirementCell(2, 6, 1),
	)
	if len(findings) != 1 || findings[0].Reason != ReasonRootCountInvalid || findings[0].Object != ObjectRetirementTree || findings[0].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementOverlap(t *testing.T) {
	// The second extent of the same transaction reaches into the first:
	// the order finding, then the double allocation claim of page 4
	// (the reserve and the remaining extents keep the partition clean).
	findings := cleanRetirementDB(t, 2,
		retirementCell(2, 3, 2),
		retirementCell(2, 4, 1),
	)
	if len(findings) != 3 || findings[0].Reason != ReasonRetirementOrderInvalid ||
		findings[1].Reason != ReasonAllocationPartitionInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementInvalidExtent(t *testing.T) {
	cases := []struct {
		name   string
		extent []byte
	}{
		{"zero count", retirementCell(2, 3, 0)},
		{"meta page", retirementCell(2, 1, 1)},
		{"creation txn", retirementCell(1, 3, 1)},
		{"future txn", retirementCell(3, 3, 1)},
		{"past page count", retirementCell(2, 6, 2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := cleanRetirementDB(t, 1, tc.extent)
			if len(findings) < 1 || findings[0].Reason != ReasonRetirementListInvalid || *findings[0].PageNumber != 2 {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateRetirementFence(t *testing.T) {
	// Root branch with keys (2,6) and (2,11); the first child fence
	// matches, the second child starts at 12 instead of 11:
	// TreeFenceInvalid on the root. The retired pages 6 and 12 sit
	// outside the tree pages, so only the two partition runs remain.
	root := treeFixturePage(t, format.PageTypeRetirementBranch, 2, 1, []int{4064, 4048}, [][]byte{
		retirementCell(2, 6, 3),  // child page 3
		retirementCell(2, 11, 4), // child page 4
	}, 0, nil)
	leafA := retirementLeaf(t, retirementCell(2, 6, 1))
	leafB := retirementLeaf(t, retirementCell(2, 12, 1))
	path := retirementDB(t, 2, 13, root, leafA, leafB)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 3 || findings[0].Reason != ReasonTreeFenceInvalid || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementRootShape(t *testing.T) {
	// A one-record branch root is the TreeLevelInvalid class; the walk
	// continues into the child and the extent claims page 6.
	root := treeFixturePage(t, format.PageTypeRetirementBranch, 2, 1, []int{4080}, [][]byte{
		retirementCell(2, 5, 3),
	}, 0, nil)
	leafA := retirementLeaf(t, retirementCell(2, 5, 1))
	path := retirementDB(t, 1, 6, root, leafA)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 2 || findings[0].Reason != ReasonTreeLevelInvalid || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementCycle(t *testing.T) {
	// A root branch pointing to itself: the root-shape finding, then
	// the graph claim detects the cycle on the second visit, then the
	// stopped walk mismatches the declared count.
	root := treeFixturePage(t, format.PageTypeRetirementBranch, 2, 1, []int{4080}, [][]byte{
		retirementCell(2, 3, 2), // child page 2 = itself
	}, 0, nil)
	path := retirementDB(t, 1, 3, root)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 3 || findings[0].Reason != ReasonTreeLevelInvalid ||
		findings[1].Reason != ReasonTreeCycle || *findings[1].PageNumber != 2 ||
		findings[2].Reason != ReasonRootCountInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementChildLevel(t *testing.T) {
	// A level-2 root expects level-1 children; the leaf children report
	// the TreeLevelInvalid class on their pages.
	root := treeFixturePage(t, format.PageTypeRetirementBranch, 2, 2, []int{4064, 4048}, [][]byte{
		retirementCell(2, 6, 3),
		retirementCell(2, 7, 4),
	}, 0, nil)
	leafA := retirementLeaf(t, retirementCell(2, 6, 1))
	leafB := retirementLeaf(t, retirementCell(2, 7, 1))
	path := retirementDB(t, 2, 8, root, leafA, leafB)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 4 || findings[0].Reason != ReasonTreeLevelInvalid || *findings[0].PageNumber != 3 ||
		findings[1].Reason != ReasonTreeLevelInvalid || *findings[1].PageNumber != 4 ||
		findings[2].Reason != ReasonRootCountInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementBorn(t *testing.T) {
	// A page born after the selected transaction is the Born class; the
	// walk stops, the declared count mismatches, and the unclaimed
	// pages form one partition run.
	leaf := retirementPage(t, 3, 0, [][]byte{
		retirementCell(2, 3, 2),
		retirementCell(2, 5, 1),
	}, nil)
	path := retirementDB(t, 2, 6, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 3 || findings[0].Reason != ReasonPageBornTxnInvalid ||
		findings[1].Reason != ReasonRootCountInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementLayout(t *testing.T) {
	// Overlapping cells fail the layout proof (PageHeaderInvalid) before
	// any cell decodes; the walk stops and the count mismatches.
	leaf := retirementPage(t, 2, 0, [][]byte{
		retirementCell(2, 3, 2),
		retirementCell(2, 5, 1),
	}, func(page []byte) {
		format.PutU16(page[34:36], 4064) // second slot aliases the first
	})
	path := retirementDB(t, 2, 6, leaf)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 3 || findings[0].Reason != ReasonPageHeaderInvalid ||
		findings[1].Reason != ReasonRootCountInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementReserved(t *testing.T) {
	// A nonzero reserved byte is the PageReservedNonzero finding and the
	// walk continues (both extents stay decodable and claim their
	// pages).
	leaf := retirementPage(t, 2, 0, [][]byte{
		retirementCell(2, 3, 2),
		retirementCell(2, 6, 1),
	}, func(page []byte) {
		page[100] = 1
	})
	findings := cleanRetirementDBWithLeaf(t, 2, leaf)
	if len(findings) != 1 || findings[0].Reason != ReasonPageReservedNonzero || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementUnmarkedGap(t *testing.T) {
	// Cells at 4080 and 4000 leave an unmarked gap; a nonzero byte
	// inside it is the PageReservedNonzero class through the unmarked
	// scan.
	leaf := treeFixturePage(t, format.PageTypeRetirementLeaf, 2, 0, []int{4080, 4000}, [][]byte{
		retirementCell(2, 3, 2),
		retirementCell(2, 6, 1),
	}, 0, func(page []byte) {
		page[4048] = 1
	})
	findings := cleanRetirementDBWithLeaf(t, 2, leaf)
	if len(findings) != 1 || findings[0].Reason != ReasonPageReservedNonzero {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementOrder(t *testing.T) {
	// Duplicate leaf keys: the per-page order finding, the overlap
	// finding, and the double allocation claim of page 3.
	findings := cleanRetirementDB(t, 2,
		retirementCell(2, 3, 2),
		retirementCell(2, 3, 1),
	)
	if len(findings) != 4 || findings[0].Reason != ReasonTreeOrderInvalid ||
		findings[1].Reason != ReasonRetirementOrderInvalid {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateRetirementMetaPageChild(t *testing.T) {
	// A branch child naming a meta page is the PageOutOfBounds class
	// and the subtree stops; the second child walks and the declared
	// count mismatches.
	root := treeFixturePage(t, format.PageTypeRetirementBranch, 2, 1, []int{4064, 4048}, [][]byte{
		retirementCell(2, 3, 1), // child page 1 (meta)
		retirementCell(2, 5, 3),
	}, 0, nil)
	leafB := retirementLeaf(t, retirementCell(2, 5, 1))
	path := retirementDB(t, 2, 6, root, leafB)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 3 || findings[0].Reason != ReasonPageOutOfBounds || *findings[0].PageNumber != 1 ||
		findings[1].Reason != ReasonRootCountInvalid {
		t.Fatalf("findings %+v", findings)
	}
}
