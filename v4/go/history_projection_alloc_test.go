package iprangedb

import (
	"testing"
)

// TestProjectHistoryAllocCeiling pins the warm ProjectHistory allocation
// count. The projection API is inherently object-bearing: one report
// vector per window plus per-projection plan, merge, draft-store handle,
// cursors, and the two fstat sequences of the abort path (require
// unchanged base + trim + verify, exactly the Rust discard_unpublished
// call pattern). None of the counted allocations scale with the input
// record count; the writer-owned encode scratches keep every tree insert
// allocation-free. Baseline before the fixes: 5,169 per run. The ceiling
// gives headroom for Go release differences while still failing on any
// new per-record or per-insert allocation.
func TestProjectHistoryAllocCeiling(t *testing.T) {
	sourcePath := histCreateSource4(t, ranges1000())
	destinationPath := histCreateMembership(t)
	w, err := OpenWriter(destinationPath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()
	windows := windows3()

	for range 8 {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("warm:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal(err)
		}
	}
	allocs := testing.AllocsPerRun(1, func() {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("project:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("ProjectHistory allocations per run: %.0f", allocs)
	const ceiling = 50
	if allocs > ceiling {
		t.Fatalf("ProjectHistory allocates %.0f objects per warm run, ceiling is %d: a new per-record or per-insert allocation was introduced", allocs, ceiling)
	}
}
