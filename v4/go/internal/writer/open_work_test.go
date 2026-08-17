//go:build v4work

package writer

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Necessary-work pins for the writer open surface: with -tags v4work the
// mapping owner increments the test-only counters at the authoritative
// points (a real remap counts once, a same-size no-op counts zero, a tail
// trim costs exactly one remap plus one file sync). These pins make the
// no-unnecessary-work contract of the open path visible.

func expectCounters(t *testing.T, want work.Snapshot) {
	t.Helper()
	got := work.Read()
	if got != want {
		t.Fatalf("work counters = %+v, want %+v", got, want)
	}
}

// TestOpenTailedFileWork pins the exact work of a tail-trimming open: one
// remap to the committed extent (OpenWriter) plus one shrink remap and one
// stable-storage sync (TrimCommittedTail).
func TestOpenTailedFileWork(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "tailwork.iprdb")
	committed := fileSize(t, path)
	if err := os.Truncate(path, committed+8*format.PageSize); err != nil {
		t.Fatal(err)
	}
	work.Reset()
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	expectCounters(t, work.Snapshot{MappingRemaps: 2, FileSyncs: 1})
}

// TestOpenNoTailWork pins the no-tail open: exactly one remap (bootstrap
// extent -> committed), zero syncs, zero growth.
func TestOpenNoTailWork(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "notailwork.iprdb")
	work.Reset()
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	expectCounters(t, work.Snapshot{MappingRemaps: 1})
}

// TestOpenEmptyDBWork pins the two-page database open: the committed extent
// equals the bootstrap extent, so the whole open performs zero remaps and
// zero syncs.
func TestOpenEmptyDBWork(t *testing.T) {
	path := makeEmptyDB(t)
	work.Reset()
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	expectCounters(t, work.Snapshot{})
}
