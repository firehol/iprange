// Bounded heap accounting for writer-side retained planning state (Rust
// heap.rs): one modeled budget per operation, charged with the exact Rust
// vector/filled byte counts under the draft MaxHeapBytes and the
// operation label, failing with the insufficient-resource class on
// overflow. Go allocations are separate and bounded by the same charges
// (the reader operationHeap precedent; Rust charges the exact Vec
// capacity, Go models the same logical capacity).

package writer

// heapBudget is one modeled per-operation heap (Rust Heap).
type heapBudget struct {
	remaining uint64
	used      uint64
}

// newHeapBudget starts one budget under the caller's byte limit (Rust
// Heap::new).
func newHeapBudget(maxHeapBytes uint64) *heapBudget {
	return &heapBudget{remaining: maxHeapBytes}
}

// usedBytes reports the charged bytes (Rust Heap::used).
func (h *heapBudget) usedBytes() uint64 { return h.used }

// remainingBytes reports the uncharged budget (Rust Heap::remaining).
func (h *heapBudget) remainingBytes() uint64 { return h.remaining }

// reserveBytes charges one exact byte reserve (Rust Heap::reserve_bytes).
func (h *heapBudget) reserveBytes(bytes uint64, label string) error {
	if bytes > h.remaining {
		return budgetExceeded(label)
	}
	h.remaining -= bytes
	h.used += bytes
	return nil
}

// vector charges one retained slice of count elements (Rust
// Heap::vector::<T>: count * size_of::<T>).
func (h *heapBudget) vector(count, elementBytes uint64, label string) error {
	return h.charge(count, elementBytes, label)
}

// filled charges one retained filled slice of length elements (Rust
// Heap::filled::<T>).
func (h *heapBudget) filled(length, elementBytes uint64, label string) error {
	return h.charge(length, elementBytes, label)
}

func (h *heapBudget) charge(count, elementBytes uint64, label string) error {
	if count != 0 && elementBytes > ^uint64(0)/count {
		return budgetExceeded(label)
	}
	return h.reserveBytes(count*elementBytes, label)
}
