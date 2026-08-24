//go:build v4work

package validation

// Slice-B work-counter pins: the retirement walk counts exactly one cell
// probe and one slot read per cell in the layout inspection and again in
// the walk iteration (Rust slotted_page inspect_layout + LayoutCells
// parity).

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestValidateRetirementWorkCounters(t *testing.T) {
	leaf := retirementLeaf(t,
		retirementCell(2, 3, 2),
		retirementCell(2, 6, 1),
	)
	path := dbWithMeta(t, cleanRetirementMeta(t, 2), 7, leaf)
	work.Reset()
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
	snapshot := work.Read()
	if snapshot.CellProbes != 4 || snapshot.SlotReads != 4 {
		t.Fatalf("work counters: probes %d reads %d, want 4/4", snapshot.CellProbes, snapshot.SlotReads)
	}
	if snapshot.PagesParsed != 0 {
		t.Fatalf("validation must not count page parses (reader-only counter): %d", snapshot.PagesParsed)
	}
}
