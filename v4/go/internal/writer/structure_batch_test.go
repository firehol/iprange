// Structure reference batch unit tests (Rust
// immutable_output/reference_batch.rs as used by structured.rs): the
// structure batch shares the membership batch slot machinery, so the
// add/full/flush contract is exercised through the alias.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestStructureReferenceBatchAccumulatesAndFlushes mirrors the Rust Add
// outcomes through one capacity-1 batch: recurring ids accumulate a
// count, a distinct id fills the table, the flush drains the slots, and
// the retry after a flush succeeds.
func TestStructureReferenceBatchAccumulatesAndFlushes(t *testing.T) {
	var batch structureReferenceBatch = newMembershipReferenceBatch(1)
	if !batch.enabled {
		t.Fatalf("capacity-1 batch is disabled")
	}
	outcome, err := batch.addReference(5)
	if err != nil || outcome != referenceAdded {
		t.Fatalf("add 5 = %v/%v, want added", outcome, err)
	}
	outcome, err = batch.addReference(5)
	if err != nil || outcome != referenceAdded {
		t.Fatalf("repeat 5 = %v/%v, want added", outcome, err)
	}
	// A distinct id cannot fit the two-slot table: full, exactly like
	// Rust ReferenceBatch::add with entries == entry_limit.
	outcome, err = batch.addReference(7)
	if err != nil || outcome != referenceFull {
		t.Fatalf("add 7 = %v/%v, want full", outcome, err)
	}
	if batch.isEmpty() {
		t.Fatalf("pending batch reports empty")
	}
	// The flush path of a full batch: take every occupied slot (the
	// slot position is the hash-probe address, so scan the table), then
	// clear the entry count.
	found := false
	for index := 0; index < batch.capacity(); index++ {
		id, count, ok := batch.takeReference(index)
		if !ok {
			continue
		}
		if found {
			t.Fatalf("second taken slot %d = %d/%d, want exactly one", index, id, count)
		}
		if id != 5 || count != 2 {
			t.Fatalf("take %d = %d/%d, want 5/2", index, id, count)
		}
		found = true
	}
	if !found {
		t.Fatalf("flush found no pending slot")
	}
	batch.finishFlush()
	if !batch.isEmpty() {
		t.Fatalf("flushed batch reports pending")
	}
	outcome, err = batch.addReference(7)
	if err != nil || outcome != referenceAdded {
		t.Fatalf("add 7 after flush = %v/%v, want added", outcome, err)
	}
}

// TestStructureReferenceBatchDisabledAppliesDirectly locks the Direct
// outcome: a zero-capacity batch (the five-argument NewOutputBuilder
// path for structured outputs) never stores a slot.
func TestStructureReferenceBatchDisabledAppliesDirectly(t *testing.T) {
	var batch structureReferenceBatch = newMembershipReferenceBatch(0)
	if batch.enabled {
		t.Fatalf("zero-capacity batch is enabled")
	}
	outcome, err := batch.addReference(9)
	if err != nil || outcome != referenceDirect {
		t.Fatalf("add = %v/%v, want direct", outcome, err)
	}
	if !batch.isEmpty() {
		t.Fatalf("disabled batch reports pending")
	}
}

// TestStructureReferenceBatchRejectsZeroID locks the guard every add
// shares with the membership batch (Rust add: "dictionary reference ID is
// zero").
func TestStructureReferenceBatchRejectsZeroID(t *testing.T) {
	var batch structureReferenceBatch = newMembershipReferenceBatch(4)
	_, err := batch.addReference(0)
	if !isCorrupt(err, "dictionary reference ID is zero") {
		t.Fatalf("add zero = %v, want corrupt zero-ID", err)
	}
}

func isCorrupt(err error, detail string) bool {
	if err == nil {
		return false
	}
	typed, ok := err.(*format.Error)
	return ok && typed.Code == format.CodeFormatInvalid && typed.Detail == detail
}
