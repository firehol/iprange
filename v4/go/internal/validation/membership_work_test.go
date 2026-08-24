//go:build v4work

package validation

// Slice-D work-counter pins: the full sweep over the membership-ipv4
// fixture counts one cell probe and one slot read per inspected and
// iterated cell (Rust slotted_page parity), the bitmap walks count one
// probe per word, child, and summary bit, and the reader cross-check
// counts its page parses exactly like the Rust reader composition.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestValidateMembershipWorkCounters(t *testing.T) {
	work.Reset()
	findings := sweepFixture(t, "membership-ipv4.iprdb")
	if len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
	snapshot := work.Read()
	// Range leaf 3 records (6 probes/6 reads), catalog name+index trees,
	// membership id+hash leaves: every cell probe pairs with a slot read.
	if snapshot.CellProbes != 298 || snapshot.SlotReads != 298 {
		t.Fatalf("cell counters: probes %d reads %d, want 298/298", snapshot.CellProbes, snapshot.SlotReads)
	}
	// Membership and feed used bitmap walks (500 words each), the
	// catalog bijection bit queries (70), the membership word-cache
	// reads (6), and the slot used reads (3) = 1079.
	if snapshot.BitmapProbes != 1079 {
		t.Fatalf("bitmap probes %d, want 1079", snapshot.BitmapProbes)
	}
	// The catalog cross-check re-reads the trees through the reader, so
	// the reader page-parsed counter moves exactly like the Rust reader
	// composition (validation itself never counts page parses).
	if snapshot.PagesParsed != 72 {
		t.Fatalf("page parses %d, want 72", snapshot.PagesParsed)
	}
}
