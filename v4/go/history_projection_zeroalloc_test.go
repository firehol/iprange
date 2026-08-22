// Milestone 3 chunk 3b-4 slice C half of the complete-page ownership
// evidence: a history projection round trip (plan, merge, report,
// abort) must not allocate any Go heap object of 4096 bytes or larger.
// Complete-page copies would allocate exactly such objects; the writer
// builds pages only at final offsets in the file mapping, so the whole
// projection path stays free of owned page buffers (Rust
// one_source_pass_and_only_window_proportional_allocations parity).

package iprangedb

import (
	"runtime"
	"testing"
)

func TestNoPageSizedHeapAllocationsHistoryProjection(t *testing.T) {
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

	// Warm every path outside the measured window: projection,
	// report read, and abort on the same writer.
	for range 8 {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("warm project:", err)
		}
		if !handle.IsChanged() {
			t.Fatal("warm projection is not changed")
		}
		_ = handle.Report()
		if err := handle.Abort(); err != nil {
			t.Fatal("warm abort:", err)
		}
	}
	if err := w.core.Healthy(); err != nil {
		t.Fatal("writer unhealthy after warm-up:", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for range 64 {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("project:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal("abort:", err)
		}
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	beforeBySize := map[uint32]uint64{}
	for _, c := range before.BySize {
		beforeBySize[c.Size] = c.Mallocs
	}
	for _, c := range after.BySize {
		if c.Size < 4096 {
			continue
		}
		if c.Mallocs > beforeBySize[c.Size] {
			t.Fatalf("heap allocation of %d bytes during history projection (mallocs %d -> %d): a complete mapped page was copied into owned memory", c.Size, beforeBySize[c.Size], c.Mallocs)
		}
	}
}
