package validation

// Reverse-index table of one validation sweep (Rust
// validation/membership_table.rs): a bounded open-addressing probe
// table over the membership (or structure) ids, charged against the
// validation heap budget. The plan hash is the Rust u32 mix; probe
// chains checkpoint at the same 64-step cadence.

import (
	"unsafe"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Slot is one occupied table entry (Rust Slot).
type Slot struct {
	ID           uint32
	RangeCount   uint64
	StoredRefcnt uint64
	WordCount    uint32
	Digest       [32]byte
	Defined      bool
	ReverseSeen  bool
}

// InsertResult reports the outcome of one define (Rust InsertResult).
type InsertResult uint8

const (
	InsertInserted InsertResult = iota
	InsertExisting
	InsertFull
)

// CountResult reports the outcome of one range count (Rust
// CountResult).
type CountResult uint8

const (
	CountInserted CountResult = iota
	CountExisting
	CountFull
	CountCancelled
	CountUnavailable
)

// probeResult is the internal probe outcome (Rust ProbeResult).
type probeResult uint8

const (
	probeIndex probeResult = iota
	probeMissing
	probeCancelled
)

// Table is one bounded reverse-index probe table (Rust Table).
type Table struct {
	slots []Slot
	mask  uint
}

var emptySlot = Slot{}

// newTable builds the probe table with next-power-of-two capacity
// (Rust Table::new; ArithmeticOverflow and BudgetExceeded classes as
// exact).
func newTable(entryCount uint64, availableBytes uint64) (*Table, error) {
	if entryCount == 0 {
		return &Table{}, nil
	}
	wanted := entryCount * 2
	if wanted < entryCount {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation membership table capacity"}
	}
	// next power of two
	next := uint64(1)
	for next < wanted {
		next <<= 1
	}
	if next < wanted {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation membership table capacity"}
	}
	capacity := next
	if uint64(uint(capacity)) != capacity {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "validation membership table"}
	}
	bytes := capacity * uint64(slotSize)
	if capacity != 0 && bytes/capacity != uint64(slotSize) {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation membership table bytes"}
	}
	if bytes > availableBytes {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "validation membership table"}
	}
	return &Table{slots: make([]Slot, capacity), mask: uint(capacity - 1)}, nil
}

// slotSize is the Go size of one Slot (Rust mem::size_of::<Slot>; the
// Go layout pads to the same 64 bytes as the Rust repr over the
// u32/u64 pair, the 32-byte digest, and the two flags).
var slotSize = int(unsafe.Sizeof(Slot{}))

// retainedBytes is the heap retained by the table (Rust
// Table::retained_bytes).
func (t *Table) retainedBytes() uint64 { return uint64(len(t.slots)) * uint64(slotSize) }

// countRange counts one range for id (Rust Table::count_range; the
// bounded probe can only report Full when the table is saturated).
func (t *Table) countRange(id uint32, check func() error) CountResult {
	index, outcome := t.probe(id, true, check)
	switch outcome {
	case probeIndex:
		if t.slots[index].ID == 0 {
			t.slots[index].ID = id
			t.slots[index].RangeCount++
			return CountInserted
		}
		t.slots[index].RangeCount++
		return CountExisting
	case probeMissing:
		return CountFull
	default:
		return CountCancelled
	}
}

// define records the stored facts of one dictionary entry (Rust
// Table::define).
func (t *Table) define(id uint32, storedRefcount uint64, wordCount uint32, digest [32]byte, check func() error) (InsertResult, error) {
	index, outcome := t.probeResult(id, true, check)
	if outcome == probeMissing {
		return InsertFull, nil
	}
	if outcome == probeCancelled {
		return 0, &format.Error{Code: format.CodeCancelled, Detail: "validation cancelled"}
	}
	if t.slots[index].ID == 0 {
		t.slots[index].ID = id
	} else if t.slots[index].Defined {
		return InsertExisting, nil
	}
	t.slots[index].StoredRefcnt = storedRefcount
	t.slots[index].WordCount = wordCount
	t.slots[index].Digest = digest
	t.slots[index].Defined = true
	t.slots[index].ReverseSeen = false
	return InsertInserted, nil
}

// markReverse records one reverse-index observation (Rust
// Table::mark_reverse).
func (t *Table) markReverse(id uint32, wordCount uint32, digest [32]byte, check func() error) (bool, error) {
	index, outcome := t.probeResult(id, false, check)
	if outcome != probeIndex {
		return false, nil
	}
	slot := &t.slots[index]
	if !slot.Defined || slot.WordCount != wordCount || slot.Digest != digest || slot.ReverseSeen {
		return false, nil
	}
	slot.ReverseSeen = true
	return true, nil
}

// markReverseDigest records one reverse-index observation without the
// word-count comparison (Rust Table::mark_reverse_digest).
func (t *Table) markReverseDigest(id uint32, digest [32]byte, check func() error) (bool, error) {
	index, outcome := t.probeResult(id, false, check)
	if outcome != probeIndex {
		return false, nil
	}
	slot := &t.slots[index]
	if !slot.Defined || slot.Digest != digest || slot.ReverseSeen {
		return false, nil
	}
	slot.ReverseSeen = true
	return true, nil
}

// len reports the capacity (Rust Table::len).
func (t *Table) len() int { return len(t.slots) }

// slot returns one occupied entry (Rust Table::slot).
func (t *Table) slot(index int) (Slot, bool) {
	if index < 0 || index >= len(t.slots) {
		return Slot{}, false
	}
	slot := t.slots[index]
	if slot.ID == 0 {
		return Slot{}, false
	}
	return slot, true
}

// probeResult maps the probe outcome to (index, ok) semantics (Rust
// find/find_or_empty).
func (t *Table) probeResult(id uint32, acceptEmpty bool, check func() error) (int, probeResult) {
	index, outcome := t.probe(id, acceptEmpty, check)
	if outcome == probeCancelled {
		return 0, probeCancelled
	}
	if outcome == probeMissing {
		return 0, probeMissing
	}
	return index, probeIndex
}

// probe is the bounded open-addressing probe (Rust Table::probe; an
// empty table or the zero id is always Missing; the probe-chain
// cancellation checkpoints every 64 steps).
func (t *Table) probe(id uint32, acceptEmpty bool, check func() error) (int, probeResult) {
	length := len(t.slots)
	if length == 0 || id == 0 {
		return 0, probeMissing
	}
	index := int(hash(id)) & int(t.mask)
	for step := 0; step < length; step++ {
		if step != 0 && step&63 == 0 && check != nil {
			if err := check(); err != nil {
				return 0, probeCancelled
			}
		}
		found := t.slots[index].ID
		if found == id || (acceptEmpty && found == 0) {
			return index, probeIndex
		}
		if found == 0 {
			return 0, probeMissing
		}
		index = (index + 1) & int(t.mask)
	}
	return 0, probeMissing
}

// hash is the plan u32 mixer (Rust hash: id * 0x9e3779b1).
func hash(id uint32) uint {
	return uint(id * 0x9e37_79b1)
}
