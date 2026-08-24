//go:build v4work

package validation

// Slice-E work-counter pin: the full sweep over the structured-ipv4
// fixture counts one cell probe and one slot read per inspected and
// iterated cell (Rust slotted_page parity), one bitmap probe per leaf
// word and bit query of the three used bitmaps plus the slot reads, and
// the reader cross-check page parses exactly like the Rust reader
// composition. The dense structure table is a fixed array, so it never
// counts cell probes; the hash tree does.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestValidateStructureWorkCounters(t *testing.T) {
	work.Reset()
	findings := sweepFixture(t, "structured-ipv4.iprdb")
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
	snapshot := work.Read()
	if snapshot.CellProbes != 28 || snapshot.SlotReads != 28 {
		t.Fatalf("cell counters: probes %d reads %d, want 28/28", snapshot.CellProbes, snapshot.SlotReads)
	}
	// The membership, feed, and structure used bitmap walks (500 words
	// each), the catalog bijection bit queries, the membership
	// word-cache reads, and the slot used reads.
	if snapshot.BitmapProbes != 1508 {
		t.Fatalf("bitmap probes %d, want 1508", snapshot.BitmapProbes)
	}
	// The catalog cross-check re-reads the trees through the reader; the
	// structure equality path is idle on this fixture (no equal digests).
	if snapshot.PagesParsed != 6 {
		t.Fatalf("page parses %d, want 6", snapshot.PagesParsed)
	}
}
