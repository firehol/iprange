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
	// scanned (Rust asserts slot_scan_steps == 0 for this edit). The
	// same-size replace rewrites the 2-byte cell and stamps the slot and
	// upper headers (2+2+2: Rust replace_cell write_source + put_u16 x2);
	// the free-space gap above upper is never moved on a same-size edit,
	// mirroring Rust's shrink==0 branch which keeps copy_within inside
	// the shrink != 0 guard.
	expectFmtCounters(t, work.Snapshot{SlotReads: 1, BytesMoved: 6})
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
	// accounting; record_start reads the target slot once. The shrink
	// moves 3 bytes of the trailing area, zeros 3, writes the 1-byte
	// cell and the slot/upper stamps (6+1+2+2 moved, 3 zeroed: Rust
	// replace_cell copy_within + zero + write_source + put_u16 x2).
	expectFmtCounters(t, work.Snapshot{SlotScanSteps: 3, SlotReads: 1, BytesMoved: 11, BytesZeroed: 3})
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
