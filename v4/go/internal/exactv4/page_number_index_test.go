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

	// Insertion splits leave this source with a second branch level. A dense
	// clone has one branch level, so equality must compare values rather than
	// private page layout.
	densePages := make([]pageNumberIndexPage, 265)
	denseWorkspace := newPageNumberIndexWorkspace(densePages)
	dense, err := newPageNumberIndex(&denseWorkspace)
	if err != nil {
		t.Fatalf("new dense clone index: %v", err)
	}
	if err = clonePageNumberIndexInto(&dense, &index); err != nil {
		t.Fatalf("clone multi-level index: %v", err)
	}
	if dense.logicalPageCount() != len(densePages) {
		t.Fatalf("dense clone pages = %d, want %d", dense.logicalPageCount(), len(densePages))
	}
	if denseRoot := &densePages[int(dense.root)]; denseRoot.kind != pageNumberIndexPageBranch ||
		densePages[int(pageNumberIndexBranchEntryAt(denseRoot, 0).child)].kind != pageNumberIndexPageLeaf {
		t.Fatal("dense clone did not collapse to one branch level")
	}
	equal, equalErr := pageNumberIndexesEqual(&index, &dense)
	if equalErr != nil || !equal {
		t.Fatalf("multi-level source and dense clone equal=%v error=%v", equal, equalErr)
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

func TestPageNumberIndexEqualityDetectsMismatchedValues(t *testing.T) {
	leftPages := make([]pageNumberIndexPage, 8)
	rightPages := make([]pageNumberIndexPage, 8)
	leftWorkspace := newPageNumberIndexWorkspace(leftPages)
	rightWorkspace := newPageNumberIndexWorkspace(rightPages)
	left, err := newPageNumberIndex(&leftWorkspace)
	if err != nil {
		t.Fatalf("new left index: %v", err)
	}
	right, err := newPageNumberIndex(&rightWorkspace)
	if err != nil {
		t.Fatalf("new right index: %v", err)
	}
	for value := 0; value < 2_048; value++ {
		if inserted, insertErr := left.insert(uint32(value)); insertErr != nil || !inserted {
			t.Fatalf("left insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
		other := uint32(value)
		if value == 1_337 {
			other = ^uint32(0) - 1
		}
		if inserted, insertErr := right.insert(other); insertErr != nil || !inserted {
			t.Fatalf("right insert %d: inserted=%v error=%v", other, inserted, insertErr)
		}
	}
	equal, equalErr := pageNumberIndexesEqual(&left, &right)
	if equalErr != nil || equal {
		t.Fatalf("mismatched indexes equal=%v error=%v", equal, equalErr)
	}
}

func TestPageNumberIndexEqualityDoesNotMaskLaterSourceFailure(t *testing.T) {
	leftPages := make([]pageNumberIndexPage, 8)
	rightPages := make([]pageNumberIndexPage, 8)
	leftWorkspace := newPageNumberIndexWorkspace(leftPages)
	rightWorkspace := newPageNumberIndexWorkspace(rightPages)
	left, err := newPageNumberIndex(&leftWorkspace)
	if err != nil {
		t.Fatalf("new left index: %v", err)
	}
	right, err := newPageNumberIndex(&rightWorkspace)
	if err != nil {
		t.Fatalf("new right index: %v", err)
	}
	for value := 0; value < 2_048; value++ {
		if inserted, insertErr := left.insert(uint32(value)); insertErr != nil || !inserted {
			t.Fatalf("left insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
		other := uint32(value)
		if value == 1 {
			other = ^uint32(0) - 1
		}
		if inserted, insertErr := right.insert(other); insertErr != nil || !inserted {
			t.Fatalf("right insert %d: inserted=%v error=%v", other, inserted, insertErr)
		}
	}
	root := &rightPages[int(right.root)]
	if root.kind != pageNumberIndexPageBranch || root.count < 2 {
		t.Fatal("right source setup did not create multiple leaves")
	}
	rightPages[int(pageNumberIndexBranchEntryAt(root, 1).child)].kind = pageNumberIndexPageEmpty

	equal, equalErr := pageNumberIndexesEqual(&left, &right)
	if equal || equalErr == nil {
		t.Fatalf("equality masked later source failure: equal=%v error=%v", equal, equalErr)
	}
}

func TestPageNumberIndexCloneCapacityFailureIsPreMutation(t *testing.T) {
	sourcePages := make([]pageNumberIndexPage, 16)
	sourceWorkspace := newPageNumberIndexWorkspace(sourcePages)
	source, err := newPageNumberIndex(&sourceWorkspace)
	if err != nil {
		t.Fatalf("new source index: %v", err)
	}
	for value := 0; value < 4_096; value++ {
		if inserted, insertErr := source.insert(uint32(value)); insertErr != nil || !inserted {
			t.Fatalf("source insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
	}

	destinationPages := make([]pageNumberIndexPage, 4) // 4 leaves + 1 branch are required.
	destinationWorkspace := newPageNumberIndexWorkspace(destinationPages)
	destination, err := newPageNumberIndex(&destinationWorkspace)
	if err != nil {
		t.Fatalf("new destination index: %v", err)
	}
	before := slices.Clone(destinationPages)
	cloneErr := clonePageNumberIndexInto(&destination, &source)
	problem, ok := cloneErr.(*pageNumberIndexError)
	if !ok || problem.code != pageNumberIndexErrPageBudget || problem.required != 5 || problem.actual != 4 {
		t.Fatalf("clone capacity error = %#v", cloneErr)
	}
	if !slices.Equal(destinationPages, before) || !destinationWorkspace.clean() || destination.len() != 0 || destination.logicalPageCount() != 0 {
		t.Fatal("clone capacity rejection changed the destination")
	}
}

func TestPageNumberIndexCloneScrubsDestinationOnSourceFailure(t *testing.T) {
	sourcePages := make([]pageNumberIndexPage, 8)
	sourceWorkspace := newPageNumberIndexWorkspace(sourcePages)
	source, err := newPageNumberIndex(&sourceWorkspace)
	if err != nil {
		t.Fatalf("new source index: %v", err)
	}
	for value := 0; value < 2_048; value++ {
		if inserted, insertErr := source.insert(uint32(value)); insertErr != nil || !inserted {
			t.Fatalf("source insert %d: inserted=%v error=%v", value, inserted, insertErr)
		}
	}
	root := &sourcePages[int(source.root)]
	if root.kind != pageNumberIndexPageBranch || root.count < 2 {
		t.Fatal("source setup did not create multiple leaves")
	}
	secondLeaf := pageNumberIndexBranchEntryAt(root, 1).child
	sourcePages[int(secondLeaf)].kind = pageNumberIndexPageEmpty

	destinationPages := make([]pageNumberIndexPage, 4)
	destinationWorkspace := newPageNumberIndexWorkspace(destinationPages)
	destination, err := newPageNumberIndex(&destinationWorkspace)
	if err != nil {
		t.Fatalf("new destination index: %v", err)
	}
	if cloneErr := clonePageNumberIndexInto(&destination, &source); cloneErr == nil {
		t.Fatal("clone accepted a malformed source")
	}
	if !destinationWorkspace.clean() || destination.len() != 0 || destination.logicalPageCount() != 0 {
		t.Fatal("source failure did not scrub the clone destination")
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

func TestPageNumberIndexCloneAndEqualityUseNoHeapAfterWorkspaceSetup(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	sourcePages := make([]pageNumberIndexPage, 8)
	destinationPages := make([]pageNumberIndexPage, 8)
	sourceWorkspace := newPageNumberIndexWorkspace(sourcePages)
	destinationWorkspace := newPageNumberIndexWorkspace(destinationPages)
	source, err := newPageNumberIndex(&sourceWorkspace)
	if err != nil {
		t.Fatalf("new source index: %v", err)
	}
	destination, err := newPageNumberIndex(&destinationWorkspace)
	if err != nil {
		t.Fatalf("new destination index: %v", err)
	}
	if !fillPageNumberIndexNoAlloc(&source) {
		t.Fatal("source warmup failed")
	}
	cloneAndCompare := func() bool {
		destination.discardAfterAbort()
		if cloneErr := clonePageNumberIndexInto(&destination, &source); cloneErr != nil {
			return false
		}
		equal, equalErr := pageNumberIndexesEqual(&source, &destination)
		destination.discardAfterAbort()
		return equalErr == nil && equal
	}
	if !cloneAndCompare() {
		t.Fatal("clone warmup failed")
	}
	allocations := testing.AllocsPerRun(20, func() {
		if !cloneAndCompare() {
			t.Fatal("clone or equality failed")
		}
	})
	if allocations != 0 {
		t.Fatalf("clone and equality allocations = %v, want 0", allocations)
	}
}
