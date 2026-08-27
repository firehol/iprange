//go:build v4work

package format

// Necessary-work pins for the slotted mutation authority: replace does not
// scan the slot array when the cell size is unchanged, a shrinking replace
// scans every slot except the target, and a failed truncate leaves the page
// byte-identical (mirroring the Rust slotted_page work pins).

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func expectFmtCounters(t *testing.T, want work.Snapshot) {
	t.Helper()
	got := work.Read()
	if got != want {
		t.Fatalf("work counters = %+v, want %+v", got, want)
	}
}

// TestReplaceSameSizeDoesNotScanSlots pins the local-edit property: a
// same-size replacement performs zero slot-array scans.
func TestReplaceSameSizeDoesNotScanSlots(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aa"), []byte("bb")})
	work.Reset()
	ok, err := SlottedReplace(page, &header, 0, 2, []byte("cc"))
	if err != nil || !ok {
		t.Fatalf("replace: %v %v", ok, err)
	}
	// record_start reads the target slot once; the slot array is not
	// scanned (Rust asserts slot_scan_steps == 0 for this edit).
	expectFmtCounters(t, work.Snapshot{SlotReads: 1})
}

// TestTruncateFailureLeavesPageUntouched pins that truncate validates the
// physical offsets before any write.
func TestTruncateFailureLeavesPageUntouched(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aa"), []byte("bb")})
	duplicate := int(U16(page[SlottedHeaderSize:]))
	PutU16(page[SlottedHeaderSize+2:], uint16(duplicate))
	before := append([]byte(nil), page...)
	if _, err := SlottedTruncate(page, &header, 1); err == nil {
		t.Fatal("truncate accepted duplicate offsets")
	}
	for at := range before {
		if page[at] != before[at] {
			t.Fatalf("page modified at %d on failed truncate", at)
		}
	}
}

// TestShrinkReplaceScansEverySlotExceptTarget pins the adjust scan cost of
// a shrinking replacement on a 3-record page (2 scans).
func TestShrinkReplaceScansEverySlotExceptTarget(t *testing.T) {
	page, header := buildCells(t, [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cccc")})
	work.Reset()
	ok, err := SlottedReplace(page, &header, 1, 4, []byte("b"))
	if err != nil || !ok {
		t.Fatalf("replace: %v %v", ok, err)
	}
	// adjust_slots_before steps every slot (including the target, which is
	// skipped after the step) but reads slot values without slot_read
	// accounting; record_start reads the target slot once.
	expectFmtCounters(t, work.Snapshot{SlotScanSteps: 3, SlotReads: 1})
}

// TestFixedPositionsScanCost pins one slot_scan_step plus one slot_read per
// logical record during the fixed-physical mapping.
func TestFixedPositionsScanCost(t *testing.T) {
	page, header := fixedUnorderedPage(t)
	work.Reset()
	var scratch [SlotItemsPerPage]int16
	positions, err := fixedPositions(page, &header, 4, scratch[:])
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 10 {
		t.Fatalf("positions = %d", len(positions))
	}
	expectFmtCounters(t, work.Snapshot{SlotScanSteps: 10, SlotReads: 10})
}
