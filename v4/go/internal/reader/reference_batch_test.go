// Exact heap-budget parity for the immutable output reference batch
// (Rust immutable_output_tests.rs
// membership_reference_batch_obeys_the_exact_heap_budget and
// immutable_output/reference_batch.rs ReferenceBatch::new): the entry
// count is the floor power of two of the affordable 2-slot pairs (a
// Rust Slot is 16 bytes), capped at 1024; a heap that fits nothing
// disables the batch with no charge.

package reader

import "testing"

func TestChargeReferenceBatchExactHeapBudget(t *testing.T) {
	// Zero heap: disabled, no charge.
	zero := newOperationHeap(0)
	p := &AlgebraOutputPrepared{heap: zero}
	entries, err := p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("zero heap charge:", err)
	}
	if entries != 0 || zero.remainingBytes() != 0 {
		t.Fatalf("zero heap: entries %d remaining %d, want 0/0", entries, zero.remainingBytes())
	}

	// The Rust bounded test limit (128 bytes): 128/32 = 4 entries, the
	// charge consumes the whole heap.
	bounded := newOperationHeap(128)
	p = &AlgebraOutputPrepared{heap: bounded}
	entries, err = p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("bounded heap charge:", err)
	}
	if entries != 4 || bounded.remainingBytes() != 0 {
		t.Fatalf("128-byte heap: entries %d remaining %d, want 4/0", entries, bounded.remainingBytes())
	}

	// One affordable pair: 32 bytes remaining -> 1 entry, charge 32.
	one := newOperationHeap(63)
	p = &AlgebraOutputPrepared{heap: one}
	entries, err = p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("one-entry heap charge:", err)
	}
	if entries != 1 || one.remainingBytes() != 31 {
		t.Fatalf("63-byte heap: entries %d remaining %d, want 1/31", entries, one.remainingBytes())
	}

	// Under one pair: disabled with the budget untouched.
	none := newOperationHeap(31)
	p = &AlgebraOutputPrepared{heap: none}
	entries, err = p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("under-pair heap charge:", err)
	}
	if entries != 0 || none.remainingBytes() != 31 {
		t.Fatalf("31-byte heap: entries %d remaining %d, want 0/31", entries, none.remainingBytes())
	}

	// A large heap caps at 1024 entries and charges 32768 bytes.
	large := newOperationHeap(1 << 20)
	p = &AlgebraOutputPrepared{heap: large}
	entries, err = p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("large heap charge:", err)
	}
	if entries != 1024 || large.remainingBytes() != 1<<20-32768 {
		t.Fatalf("1MiB heap: entries %d remaining %d, want 1024/%d", entries, large.remainingBytes(), 1<<20-32768)
	}

	// The entry cap applies BEFORE the floor power of two: 100000 bytes
	// afford 3125 possible entries, capped to 1024, floored to 1024.
	mid := newOperationHeap(100000)
	p = &AlgebraOutputPrepared{heap: mid}
	entries, err = p.ChargeReferenceBatch()
	if err != nil {
		t.Fatal("mid heap charge:", err)
	}
	if entries != 1024 || mid.remainingBytes() != 100000-32768 {
		t.Fatalf("100000-byte heap: entries %d remaining %d, want 1024/%d", entries, mid.remainingBytes(), 100000-32768)
	}
}
