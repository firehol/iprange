// Reference-batch heap charging shared by the immutable output machines
// (Rust immutable_output/reference_batch.rs ReferenceBatch::new): the
// snapshot copy and the immutable-feed build both size and charge their
// membership/structured reference batches against the operation heap
// exactly like Rust. This file is the single authority for the charge.

package writer

// referenceBatchSlotSize and referenceBatchEntryLimit are the Rust
// immutable reference-batch shape constants
// (immutable_output/reference_batch.rs: Slot{id: u32, count: i64} is 16
// bytes; ENTRY_LIMIT is 1024).
const (
	referenceBatchSlotSize   = 16
	referenceBatchEntryLimit = 1024
)

// ChargeReferenceBatch sizes and charges one reference batch against the
// operation heap exactly like Rust ReferenceBatch::new: the entry
// capacity is the floor power of two of the affordable slot pairs (two
// 16-byte slots per entry), capped at 1024; a heap that cannot fit one
// entry disables the batch with no charge. The charged bytes are
// deducted from the remaining heap.
func ChargeReferenceBatch(heap *uint64) int {
	affordable := *heap / (2 * referenceBatchSlotSize)
	if affordable > referenceBatchEntryLimit {
		affordable = referenceBatchEntryLimit
	}
	entries := floorPowerOfTwo(int(affordable))
	if entries == 0 {
		return 0
	}
	*heap -= uint64(entries) * 2 * referenceBatchSlotSize
	return entries
}

// floorPowerOfTwo returns the largest power of two at or below value,
// or 0 for a non-positive value (Rust floor_power_of_two with its
// explicit zero guard: a heap that cannot fit one entry disables the
// batch with no charge).
func floorPowerOfTwo(value int) int {
	if value <= 0 {
		return 0
	}
	power := 1
	for power <= value>>1 {
		power <<= 1
	}
	return power
}
