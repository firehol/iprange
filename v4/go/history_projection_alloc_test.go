package iprangedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestProjectHistoryAllocCeiling pins the warm ProjectHistory abort
// path. The projection API is inherently object-bearing: one report
// vector per window plus per-projection plan, merge, draft-store handle,
// cursors, and the two fstat sequences of the abort path (require
// unchanged base + trim + verify, exactly the Rust discard_unpublished
// call pattern). None of the counted allocations scale with the input
// record count; the writer-owned encode scratches keep every tree insert
// allocation-free. Baseline before the fixes: 5,169 per run. The ceiling
// gives headroom for Go release differences while still failing on any
// new per-record or per-insert allocation.
func TestProjectHistoryAllocCeiling(t *testing.T) {
	requireFileCreation(t)
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
	allocs := testing.AllocsPerRun(4, func() {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("project:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("ProjectHistory abort allocations per run: %.0f", allocs)
	const ceiling = 50
	if allocs > ceiling {
		t.Fatalf("ProjectHistory allocates %.0f objects per warm run, ceiling is %d: a new per-record or per-insert allocation was introduced", allocs, ceiling)
	}
}

// TestProjectHistoryCommitAllocCeiling pins the full terminal path of
// one projection: fresh destination create, writer open, project, commit,
// close. The commit terminal adds the publication path (base re-read,
// meta stamping, sync, sidecar-independent publish) on top of the abort
// path; like the abort path its cost is per projection, never per
// record. Measured floor at 1,000 source records: 84 per run.
func TestProjectHistoryCommitAllocCeiling(t *testing.T) {
	requireFileCreation(t)
	sourcePath := histCreateSource4(t, ranges1000())
	source := histSource(t, sourcePath)
	defer source.Close()
	windows := windows3()
	dir := t.TempDir()
	var next int
	run := func() {
		index := next
		next++
		path := filepath.Join(dir, fmt.Sprintf("dest-%d.iprdb", index))
		tag, err := NewValueTag([]byte("feeds"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
			t.Fatal(err)
		}
		w, err := OpenWriter(path, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, windows, nil)
		if err != nil {
			t.Fatal("project:", err)
		}
		if _, err := handle.Commit(); err != nil {
			t.Fatal("commit:", err)
		}
	}
	for range 4 {
		run()
	}
	allocs := testing.AllocsPerRun(4, func() {
		run()
	})
	t.Logf("create+project+commit allocations per run: %.0f", allocs)
	const ceiling = 120
	if allocs > ceiling {
		t.Fatalf("create+project+commit allocates %.0f objects per run, ceiling is %d: a new per-record or per-insert allocation was introduced", allocs, ceiling)
	}
}
