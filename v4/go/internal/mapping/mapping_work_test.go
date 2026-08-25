//go:build v4work

package mapping

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Necessary-work pins for the mapping owner: a real resize counts, a
// same-size remap/shrink is a no-op and counts zero, growth and
// stable-storage syncs count exactly once per operation.

func TestMappingWorkCounters(t *testing.T) {
	if !CoordinationSupported() {
		t.Skip("database file creation is not supported on this platform")
	}
	dir := t.TempDir()
	path := makePagesFile(t, dir, 6)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	work.Reset()
	if err := m.Remap(4 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Remap(4 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Shrink(3 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Shrink(3 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Grow(5 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncFile(); err != nil {
		t.Fatal(err)
	}
	expectWork(t, work.Snapshot{
		MappingRemaps:  2, // Remap(4p) + Shrink(3p); same-size calls are no-ops
		MappingGrowths: 1,
		MappingFlushes: 1,
		FileSyncs:      1,
	})
}

func expectWork(t *testing.T, want work.Snapshot) {
	t.Helper()
	got := work.Read()
	if got != want {
		t.Fatalf("work counters = %+v, want %+v", got, want)
	}
}
