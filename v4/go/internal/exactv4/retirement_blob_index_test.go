package exactv4

import (
	"encoding/binary"
	"testing"
)

func newRetirementBlobIndex(t *testing.T, values []uint32) (*pageNumberIndex, *pageNumberIndexWorkspace) {
	t.Helper()
	pages := make([]pageNumberIndexPage, len(values)/512+8)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new page-number index: %v", err)
	}
	for valueIndex := len(values) - 1; valueIndex >= 0; valueIndex-- {
		if _, err = index.insert(values[valueIndex]); err != nil {
			t.Fatalf("index insert %d: %v", values[valueIndex], err)
		}
	}
	return &index, &workspace
}

func requireRetirementIndexLeaf(t *testing.T, arena *privatePageArena, page uint32, wantOffset uint64, want []uint32) {
	t.Helper()
	slot := arena.testSlot(page)
	if slot == nil {
		t.Fatalf("missing private blob page %d", page)
	}
	leaf, status := openBlobLeafStatus(slot.bytes[:], arena.bornTxn, blobKindRetirementPageList)
	if status.failed() {
		t.Fatalf("open blob leaf %d: %+v", page, status)
	}
	if status = leaf.verifyLocalStatus(); status.failed() {
		t.Fatalf("verify blob leaf %d: %+v", page, status)
	}
	if leaf.logicalOffset != wantOffset || int(leaf.dataLength)/4 != len(want) {
		t.Fatalf("leaf %d geometry = offset:%d values:%d, want offset:%d values:%d", page, leaf.logicalOffset, int(leaf.dataLength)/4, wantOffset, len(want))
	}
	for index, value := range want {
		if got := binary.LittleEndian.Uint32(leaf.data()[index*4:]); got != value {
			t.Fatalf("leaf %d value %d = %d, want %d", page, index, got, value)
		}
	}
}

func TestRetirementBlobBuilderStreamsPageNumberIndex(t *testing.T) {
	values := make([]uint32, retirementValuesPerBlobLeaf+1)
	for index := range values {
		values[index] = uint32(index + 2)
	}
	index, _ := newRetirementBlobIndex(t, values)
	slots := writerSlots(5000, 3)
	arena, problem := newPrivatePageArena(slots, 5000, 5003, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := make([]uint32, 3)
	token, problem := buildRetirementBlobFromIndex(index, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	if token.pageCount != uint64(len(values)) || token.byteLength != uint64(len(values))*4 || token.privatePages != 3 || arena.inUseCount() != 3 {
		t.Fatalf("token=%+v in-use=%d", token, arena.inUseCount())
	}
	root := arena.testSlot(token.root)
	if root == nil {
		t.Fatalf("missing root %d", token.root)
	}
	branch, status := openBlobBranchStatus(root.bytes[:], arena.bornTxn, blobKindRetirementPageList, arena.pendingPageCount)
	if status.failed() || branch.level != 1 || branch.len() != 2 {
		t.Fatalf("root branch=%+v status=%+v", branch, status)
	}
	if status = branch.verifyLocalStatus(); status.failed() {
		t.Fatalf("verify root branch: %+v", status)
	}
	first, status := branch.entryStatus(0)
	if status.failed() {
		t.Fatal(status)
	}
	second, status := branch.entryStatus(1)
	if status.failed() {
		t.Fatal(status)
	}
	if first.logicalOffset != 0 || second.logicalOffset != uint64(retirementValuesPerBlobLeaf*4) {
		t.Fatalf("branch offsets = %d/%d", first.logicalOffset, second.logicalOffset)
	}
	requireRetirementIndexLeaf(t, &arena, first.childPage, 0, values[:retirementValuesPerBlobLeaf])
	requireRetirementIndexLeaf(t, &arena, second.childPage, uint64(retirementValuesPerBlobLeaf*4), values[retirementValuesPerBlobLeaf:])
	if cleanup := token.discard(); cleanup.failed() {
		t.Fatalf("discard: %v", cleanup)
	}
	if arena.inUseCount() != 0 {
		t.Fatalf("discard left %d private pages", arena.inUseCount())
	}
}

func TestRetirementBlobIndexPreflightIsAtomic(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  []uint32
		mutate  func(*pageNumberIndex)
		scratch int
		want    retirementWriteErrorCode
	}{
		{name: "out of bounds", values: []uint32{1}, scratch: 1, want: retirementWriteErrRetirementStreamPageOutOfBounds},
		{name: "declared count mismatch", values: []uint32{2}, mutate: func(index *pageNumberIndex) { index.values++ }, scratch: 1, want: retirementWriteErrRetirementStreamCountMismatch},
		{name: "failed index", values: []uint32{2}, mutate: func(index *pageNumberIndex) { index.failed = true }, scratch: 1, want: retirementWriteErrPageNumberIndex},
		{name: "short output scratch", values: func() []uint32 {
			values := make([]uint32, retirementValuesPerBlobLeaf+1)
			for index := range values {
				values[index] = uint32(index + 2)
			}
			return values
		}(), scratch: 2, want: retirementWriteErrBlobBuildScratchTooSmall},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, _ := newRetirementBlobIndex(t, test.values)
			if test.mutate != nil {
				test.mutate(index)
			}
			slots := writerSlots(5000, 3)
			arena, problem := newPrivatePageArena(slots, 5000, 5003, 2)
			if problem.failed() {
				t.Fatal(problem)
			}
			order := make([]uint32, test.scratch)
			for position := range order {
				order[position] = uint32(position + 99)
			}
			beforeOrder := append([]uint32(nil), order...)
			before := snapshotRetirementPool(&arena)
			_, problem = buildRetirementBlobFromIndex(index, &arena, &blobBuildScratch{pageNumbers: order})
			requireRetirementWriteCode(t, problem, test.want)
			requireRetirementPoolSnapshot(t, &arena, before)
			for position := range order {
				if order[position] != beforeOrder[position] {
					t.Fatalf("output scratch %d changed", position)
				}
			}
		})
	}
}

func TestRetirementBlobIndexBuildAllocatesNothingAfterSetup(t *testing.T) {
	values := make([]uint32, retirementValuesPerBlobLeaf+1)
	for index := range values {
		values[index] = uint32(index + 2)
	}
	index, _ := newRetirementBlobIndex(t, values)
	slots := writerSlots(5000, 3)
	arena, problem := newPrivatePageArena(slots, 5000, 5003, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := make([]uint32, 3)
	scratch := blobBuildScratch{pageNumbers: order}
	allocations := testing.AllocsPerRun(20, func() {
		token, buildProblem := buildRetirementBlobFromIndex(index, &arena, &scratch)
		if buildProblem.failed() {
			panic(buildProblem)
		}
		if cleanup := token.discard(); cleanup.failed() {
			panic(cleanup)
		}
	})
	if allocations != 0 {
		t.Fatalf("index blob build allocations = %v, want 0", allocations)
	}
	if arena.inUseCount() != 0 {
		t.Fatalf("allocation test left %d private pages", arena.inUseCount())
	}
}
