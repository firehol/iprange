package iprangedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestProjectHistoryAllocCeiling pins the warm ProjectHistory abort
// path on the live surface. The projection API is inherently
// object-bearing: one report vector per window plus per-projection
// plan, merge, draft-store handle, cursors, the live writer's abort
// machine (pair proof, unchanged base, trim, verify - the Rust
// discard_unpublished call pattern, with the sidecar coordination and
// cleanup evidence the immutable writer lacked). None of the counted
// allocations scale with the input record count; the writer-owned
// encode scratches keep every tree insert allocation-free. Measured
// live floor: 59 per run. The ceiling gives headroom for Go release
// differences while still failing on any new per-record or
// per-insert allocation.
func TestProjectHistoryAllocCeiling(t *testing.T) {
	requireLiveCreation(t)
	sourcePath := histCreateSource4(t, ranges1000())
	destinationPath := histCreateMembership(t)
	w, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()
	windows := windows3()

	for range 8 {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows, nil)
		if err != nil {
			t.Fatal("warm:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal(err)
		}
	}
	allocs := testing.AllocsPerRun(4, func() {
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows, nil)
		if err != nil {
			t.Fatal("project:", err)
		}
		if err := handle.Abort(); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("ProjectHistory abort allocations per run: %.0f", allocs)
	const ceiling = 100
	if allocs > ceiling {
		t.Fatalf("ProjectHistory allocates %.0f objects per warm run, ceiling is %d: a new per-record or per-insert allocation was introduced", allocs, ceiling)
	}
}

// TestProjectHistoryCommitAllocCeiling pins the full terminal path of
// one projection: fresh live destination create, live writer open,
// project, commit, close. The commit terminal adds the live publication
// path (reader-table gate, base re-read, meta stamping, sync, sidecar
// coordination and cleanup evidence) on top of the abort path; like the
// abort path its cost is per projection, never per record. Measured
// floor at 1,000 source records: 233 per run on linux/amd64 (375 under
// race+checkptr instrumentation) and 407 per run on windows/amd64 with
// Go 1.26.5 (the Windows profile differs in GC metadata, file identity
// capture, and security-probe allocations, none of which scale with
// the record count). The ceiling gives headroom for Go release and
// instrumentation differences while still failing on any new
// per-record or per-insert allocation (a per-record allocation would
// add at least 1,000 objects at this input size).
func TestProjectHistoryCommitAllocCeiling(t *testing.T) {
	requireLiveCreation(t)
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
		if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag, 4, nil); err != nil {
			t.Fatal(err)
		}
		w, err := OpenLiveWriter(path, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows, nil)
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
	const ceiling = 450
	if allocs > ceiling {
		t.Fatalf("create+project+commit allocates %.0f objects per run, ceiling is %d: a new per-record or per-insert allocation was introduced", allocs, ceiling)
	}
}
