//go:build v4work

// Necessary-work pins for the range edit core (Rust range_mutation_tests.rs
// work::measure assertions).

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestClearSplitCoalesceWorkPins mirrors the Rust clear test counters.
func TestClearSplitCoalesceWorkPins(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	if _, err := rangeAssign(ctx, v4key(0), v4key(100), 7); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	cleared, err := rangeClear(ctx, v4key(40), v4key(60))
	if err != nil || !cleared {
		t.Fatalf("clear(40,60) = %v, %v", cleared, err)
	}
	snap := work.Read()
	if snap.RangesSplit != 1 {
		t.Fatalf("clear ranges_split = %d, want 1", snap.RangesSplit)
	}
	if snap.RangesCoalesced != 0 {
		t.Fatalf("clear ranges_coalesced = %d, want 0", snap.RangesCoalesced)
	}
	if snap.RangesEmitted != 2 {
		t.Fatalf("clear ranges_emitted = %d, want 2", snap.RangesEmitted)
	}

	work.Reset()
	changed, err := rangeAssign(ctx, v4key(40), v4key(60), 7)
	if err != nil || !changed {
		t.Fatalf("reassign = %v, %v", changed, err)
	}
	snap = work.Read()
	if snap.RangesSplit != 0 {
		t.Fatalf("reassign ranges_split = %d, want 0", snap.RangesSplit)
	}
	if snap.RangesCoalesced != 2 {
		t.Fatalf("reassign ranges_coalesced = %d, want 2", snap.RangesCoalesced)
	}
	if snap.RangesEmitted != 1 {
		t.Fatalf("reassign ranges_emitted = %d, want 1", snap.RangesEmitted)
	}
}

// TestNestedAssignmentPageWorkIsNotQuadratic mirrors the Rust test: the
// private gap path costs one tree lookup per record after the first and
// page work grows linearly, not quadratically.
func TestNestedAssignmentPageWorkIsNotQuadratic(t *testing.T) {
	small, _ := nestedAssignmentWork(t, 512)
	large, snap := nestedAssignmentWork(t, 1024)
	if large > small*3 {
		t.Fatalf("doubling input grew deterministic page work from %d to %d", small, large)
	}
	if snap.TreeLookups != 1023 {
		t.Fatalf("nested assignment tree lookups = %d, want 1023", snap.TreeLookups)
	}
}

func nestedAssignmentWork(t *testing.T, count uint32) (uint64, work.Snapshot) {
	t.Helper()
	m := newRangeMemoryStore()
	root := uint32(0)
	records := uint64(0)
	ctx := newV4Ctx(m, &root, &records)
	end := count*4 + 1
	work.Reset()
	for index := uint32(0); index < count; index++ {
		if _, err := rangeAssignPrivate(ctx, v4key(index), v4key(end-index), index%2+1); err != nil {
			t.Fatal(err)
		}
	}
	snap := work.Read()
	return m.reads + m.writes, snap
}

// TestManyDisjointRangesSplitLeaves mirrors the Rust pages_split pin.
func TestManyDisjointRangesSplitLeaves(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	work.Reset()
	for key := int32(1999); key >= 0; key-- {
		if _, err := rangeAssign(ctx, v4key(uint32(key*2)), v4key(uint32(key*2)), uint32(key)); err != nil {
			t.Fatal(err)
		}
	}
	if snap := work.Read(); snap.PagesSplit == 0 {
		t.Fatal("disjoint assignments split no pages")
	}
	if count != 2000 {
		t.Fatalf("record count = %d, want 2000", count)
	}
}
