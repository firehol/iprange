// Coverage-union input allocation pins (SOW-0025 slice A): the ordered
// prefix path must not allocate per record, and the general fallback
// must stay within the generic gap-protocol baseline. The writer-owned
// encode scratch (rangeCtx.encodeRecord) and the untracked accounting
// flag (rangeCtx.markUntracked) keep the ordered path allocation-free;
// the general path retains the generic tree gap machinery (~2 per
// record: rejection, probePredecessor, privatePathSelect), tracked as
// the P3-B optionalCell follow-up of the milestone close gate.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// pushOrderedAllocs measures one fresh draft plus count ascending
// non-adjacent untracked pushes over it (the ordered-prefix builder
// path). The AllocsPerRun warmup builds a second fresh draft, so the
// setup cost cancels on subtraction; the recorded ceilings still fail
// on any per-record allocation.
func pushOrderedAllocs(t *testing.T, count int) float64 {
	t.Helper()
	return testing.AllocsPerRun(1, func() {
		path := t.TempDir() + "/db.iprdb"
		if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindMembership, 0, [16]byte{3}, nil); err != nil {
			t.Fatal(err)
		}
		_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
		input := newUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
		family, err := store.rangeFamily()
		if err != nil {
			t.Fatal(err)
		}
		root := store.draft.workflowRangeRoot
		countv := store.draft.workflowRangeCount
		ctx := &rangeCtx{family: family, store: store, root: &root, count: &countv}
		for i := 0; i < count; i++ {
			from := uint64(i) * 3
			if _, err := pushPrivateUntracked(ctx, tree.Key{Hi: from}, tree.Key{Hi: from + 1}, 1, &input); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// pushGeneralAllocs is the same measurement over the general assignment
// fallback (Rust UnionInput::start_general), which exercises the
// closed-gap rejection machinery of the generic tree.
func pushGeneralAllocs(t *testing.T, count int) float64 {
	t.Helper()
	return testing.AllocsPerRun(1, func() {
		path := t.TempDir() + "/db.iprdb"
		if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindMembership, 0, [16]byte{3}, nil); err != nil {
			t.Fatal(err)
		}
		_, store, _ := openDraftStore(t, path, historyBudget(), [16]byte{3})
		input := newUnionInput(format.AddressFamilyIPv4, format.ValueKindMembership, 256*1024)
		input.startGeneral()
		family, err := store.rangeFamily()
		if err != nil {
			t.Fatal(err)
		}
		root := store.draft.workflowRangeRoot
		countv := store.draft.workflowRangeCount
		ctx := &rangeCtx{family: family, store: store, root: &root, count: &countv}
		for i := 0; i < count; i++ {
			from := uint64(i) * 3
			if _, err := pushPrivateUntracked(ctx, tree.Key{Hi: from}, tree.Key{Hi: from + 1}, 1, &input); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestSliceAOrderedUnionAllocCeiling pins the ordered coverage-input
// path. Baseline after the slice A fixes: ~54 allocations for the fresh
// draft plus zero per record (512 records). The ceiling of 128 fails on
// any per-record allocation (54 + 512 = 566) while leaving headroom for
// setup variance across Go releases.
func TestSliceAOrderedUnionAllocCeiling(t *testing.T) {
	total := pushOrderedAllocs(t, 512)
	t.Logf("ordered coverage input allocations (fresh draft + 512 records): %.0f", total)
	const ceiling = 128
	if total > ceiling {
		t.Fatalf("ordered coverage input allocates %.0f objects for 512 records, ceiling is %d: a per-record allocation returned", total, ceiling)
	}
}

// TestSliceAGeneralUnionAllocCeiling pins the general fallback. The
// generic gap rejection machinery allocates ~2 objects per record at
// this tree size (rejection, probePredecessor, privatePathSelect; the
// optionalCell follow-up removes them); the allocation slope grows
// with tree depth, so the pin uses the absolute total of one fixed
// measurement. Baseline: ~57 for the fresh draft plus 1.98 per record
// = ~565 for 256 records. Ceiling 800 permits up to 2.9 per record
// and fails on any new per-record allocation on top of the baseline
// (a +1 regression lands at 57 + 2.98 x 256 = 820).
func TestSliceAGeneralUnionAllocCeiling(t *testing.T) {
	total := pushGeneralAllocs(t, 256)
	t.Logf("general coverage input allocations (fresh draft + 256 records): %.0f", total)
	const ceiling = 800
	if total > ceiling {
		t.Fatalf("general coverage input allocates %.0f objects for 256 records, ceiling is %d: a new per-record allocation was introduced", total, ceiling)
	}
}
