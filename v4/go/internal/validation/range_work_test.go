//go:build v4work

package validation

// Slice-C work-counter pins: the range walk counts one cell probe and
// one slot read per cell in the layout inspection and again in the walk
// iteration (Rust slotted_page inspect_layout + LayoutCells parity).
// The two-level synthetic tree has six cells (two branch entries plus
// two two-record leaves): twelve probes and twelve slot reads in total,
// and validation itself never counts page parses (reader-only counter).

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestValidateRangeWorkCounters(t *testing.T) {
	branch := rangeTreeBranch(t, [][2]uint32{{0, 3}, {2000, 4}})
	path, _, _ := rangeTreeClean(t, branch)
	work.Reset()
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
	snapshot := work.Read()
	if snapshot.CellProbes != 12 || snapshot.SlotReads != 12 {
		t.Fatalf("work counters: probes %d reads %d, want 12/12", snapshot.CellProbes, snapshot.SlotReads)
	}
	if snapshot.PagesParsed != 0 {
		t.Fatalf("validation must not count page parses (reader-only counter): %d", snapshot.PagesParsed)
	}
}
