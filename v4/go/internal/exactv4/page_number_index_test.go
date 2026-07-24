package exactv4

import (
	"slices"
	"testing"
)

func collectPageNumberIndex(t *testing.T, index *pageNumberIndex) []uint32 {
	t.Helper()
	values := make([]uint32, 0, index.len())
	if err := index.visitAscending(func(value uint32) error {
		values = append(values, value)
		return nil
	}); err != nil {
		t.Fatalf("visit page-number index: %v", err)
	}
	return values
}

func TestPageNumberIndexOrdersAndDeduplicates(t *testing.T) {
	pages := make([]pageNumberIndexPage, 8)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}

	values := []uint32{429, 2, 55, 4_000_000_000, 55, 3, 2, 1_024, 0, ^uint32(0)}
	wantInserted := []bool{true, true, true, true, false, true, false, true, true, true}
	for position, value := range values {
		inserted, insertErr := index.insert(value)
		if insertErr != nil {
			t.Fatalf("insert %d: %v", value, insertErr)
		}
		if inserted != wantInserted[position] {
			t.Fatalf("unexpected inserted result for %d: %v", value, inserted)
		}
	}

	want := []uint32{0, 2, 3, 55, 429, 1_024, 4_000_000_000, ^uint32(0)}
	if got := collectPageNumberIndex(t, &index); !slices.Equal(got, want) {
		t.Fatalf("ordered values = %v, want %v", got, want)
	}
	if index.len() != uint64(len(want)) || index.logicalPageCount() != 1 {
		t.Fatalf("index counts = values:%d pages:%d", index.len(), index.logicalPageCount())
	}
}

func TestPageNumberIndexSplitsDenseAndReverseInput(t *testing.T) {
	pages := make([]pageNumberIndexPage, 16)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}

	const count = 4_096
	for value := count - 1; value >= 0; value-- {
		inserted, insertErr := index.insert(uint32(value))
		if insertErr != nil || !inserted {
			t.Fatalf("reverse insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
	}
	if index.logicalPageCount() <= 2 {
		t.Fatalf("dense reverse input did not build branch pages: %d logical pages", index.logicalPageCount())
	}
	if index.len() != count {
		t.Fatalf("value count = %d, want %d", index.len(), count)
	}

	position := 0
	if err = index.visitAscending(func(value uint32) error {
		if value != uint32(position) {
			t.Fatalf("value at %d = %d", position, value)
		}
		position++
		return nil
	}); err != nil {
		t.Fatalf("visit dense index: %v", err)
	}
	if position != count {
		t.Fatalf("visited %d values, want %d", position, count)
	}
}

func TestPageNumberIndexSplitsFullBranchRoot(t *testing.T) {
	const count = 270_000
	pages := make([]pageNumberIndexPage, 540)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}
	for value := 0; value < count; value++ {
		inserted, insertErr := index.insert(uint32(value))
		if insertErr != nil || !inserted {
			t.Fatalf("insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
	}
	root := &pages[int(index.root)]
	if root.kind != pageNumberIndexPageBranch ||
		pages[int(pageNumberIndexBranchEntryAt(root, 0).child)].kind != pageNumberIndexPageBranch {
		t.Fatal("full branch did not grow a second branch level")
	}
	position := 0
	if err = index.visitAscending(func(value uint32) error {
		if value != uint32(position) {
			t.Fatalf("value at %d = %d", position, value)
		}
		position++
		return nil
	}); err != nil {
		t.Fatalf("visit branch-split index: %v", err)
	}
	if position != count || index.len() != count {
		t.Fatalf("counts = visited:%d stored:%d want:%d", position, index.len(), count)
	}
}

func TestPageNumberIndexCapacityFailureIsPreMutation(t *testing.T) {
	pages := make([]pageNumberIndexPage, 1)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}
	for value := 0; value < pageNumberIndexLeafCapacity; value++ {
		inserted, insertErr := index.insert(uint32(value))
		if insertErr != nil || !inserted {
			t.Fatalf("fill insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
	}
	before := pages
	beforeLen, beforePages := index.len(), index.logicalPageCount()
	inserted, insertErr := index.insert(pageNumberIndexLeafCapacity)
	problem, ok := insertErr.(*pageNumberIndexError)
	if inserted || !ok || problem.code != pageNumberIndexErrPageBudget {
		t.Fatalf("capacity result inserted=%v error=%#v", inserted, insertErr)
	}
	if !slices.Equal(pages, before) || index.len() != beforeLen || index.logicalPageCount() != beforePages {
		t.Fatal("capacity rejection changed the index")
	}
	if inserted, insertErr = index.insert(17); insertErr != nil || inserted {
		t.Fatalf("duplicate after capacity rejection: inserted=%v error=%v", inserted, insertErr)
	}
}

func TestPageNumberIndexRejectsStaleWorkspaceAndScrubsOnAbort(t *testing.T) {
	pages := make([]pageNumberIndexPage, 2)
	pages[0].kind = pageNumberIndexPageLeaf
	workspace := newPageNumberIndexWorkspace(pages)
	if _, err := newPageNumberIndex(&workspace); err == nil {
		t.Fatal("accepted occupied workspace")
	}
	workspace.reset()
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new after reset: %v", err)
	}
	if inserted, insertErr := index.insert(99); insertErr != nil || !inserted {
		t.Fatalf("insert before discard: inserted=%v error=%v", inserted, insertErr)
	}
	index.discardAfterAbort()
	if !workspace.clean() || index.len() != 0 || index.logicalPageCount() != 0 {
		t.Fatal("discard did not scrub the private index")
	}
	if _, err = newPageNumberIndex(&workspace); err != nil {
		t.Fatalf("workspace was not reusable after discard: %v", err)
	}
}

func fillPageNumberIndexNoAlloc(index *pageNumberIndex) bool {
	index.discardAfterAbort()
	for value := 0; value < 2_048; value++ {
		inserted, err := index.insert(uint32(value))
		if err != nil || !inserted {
			return false
		}
	}
	return index.len() == 2_048
}

func TestPageNumberIndexUsesNoHeapAfterWorkspaceSetup(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	pages := make([]pageNumberIndexPage, 8)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}
	if !fillPageNumberIndexNoAlloc(&index) {
		t.Fatal("warmup failed")
	}
	allocations := testing.AllocsPerRun(20, func() {
		if !fillPageNumberIndexNoAlloc(&index) {
			t.Fatal("page-number index fill failed")
		}
	})
	if allocations != 0 {
		t.Fatalf("page-number index allocations = %v, want 0", allocations)
	}
}
