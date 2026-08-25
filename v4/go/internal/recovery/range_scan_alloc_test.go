//go:build !race

// Per-record heap pin of the recovery range-scan cursor path (the
// Rust authority streams the same work with zero heap): every range
// record travels through by-value cursors, so the allocation count of
// one warmed scan over a single-leaf direct source is fixed per run
// and independent of the record count. The fixed per-run state is one
// LayoutInspection and one returned PageHeader per visited page plus
// one escaped defect-path page pointer per visited node (the events
// unknown envelope carries *uint32 where the Rust authority passes
// Option<u32> by value). Race and checkptr instrumentation allocate
// inside the measured path themselves, so the pin runs only in
// uninstrumented builds (the sibling recovery pins carry the same
// tag for the same reason).

package recovery

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestRangeScanPerRecordPathIsHeapFree(t *testing.T) {
	creationGate(t)
	// The root is one leaf at both sizes, so every measured run
	// visits exactly one page and one node (200 x 12-byte cells fit
	// the leaf without branching).
	allocs := func(rangeCount int) float64 {
		t.Helper()
		path := filepath.Join(t.TempDir(), "source.db")
		builder := directSourceBuilder(t, path)
		ranges := make([][3]uint32, 0, rangeCount)
		for index := 0; index < rangeCount; index++ {
			ranges = append(ranges, [3]uint32{uint32(index), uint32(index), uint32(index % 7)})
		}
		meta := finishRanges(t, builder, ranges)
		source := mapSource(t, path)
		defer source.Close()

		budget := recoveryBudget(1 << 20)
		expected := meta.PageCount
		if physical := uint64(source.Size() / format.PageSize); physical < expected {
			expected = physical
		}
		pages, err := forRecovery(budget.MaxHeapBytes, expected, meta, budget)
		if err != nil {
			t.Fatalf("forRecovery: %v", err)
		}
		codec := rangeV4Codec{}
		events := &analysisEvents{rep: newReporter(nil), codec: codec}
		return testing.AllocsPerRun(20, func() {
			if err := pages.reset(); err != nil {
				t.Fatalf("reset: %v", err)
			}
			if err := scanRanges(codec, source, meta, pages, nil, events); err != nil {
				t.Fatalf("scanRanges: %v", err)
			}
		})
	}

	// Fixed per-run state of one leaf page and one node visit: the
	// LayoutInspection and the returned PageHeader of the page, plus
	// the defect-path page pointers of the node and the leaf. Every
	// per-record cursor stays on the stack, so both sizes must land
	// on exactly this bound.
	const perRunBound = 4
	small := allocs(100)
	large := allocs(200)
	if small > perRunBound || large > perRunBound {
		t.Fatalf("range scan allocated %.2f / %.2f objects per run over 100 / 200 records; want at most %d (the fixed per-page and per-node state)", small, large, perRunBound)
	}
	if small != large {
		t.Fatalf("range scan allocations depend on the record count: %.2f over 100 records vs %.2f over 200 records; every record cursor must stay on the stack", small, large)
	}
	t.Logf("range scan allocations per run over a single leaf: %.2f for 100 records, %.2f for 200 records (per-record path is heap-free)", small, large)
}
