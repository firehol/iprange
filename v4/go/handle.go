package iprangedb

// This file implements the public handle registry. Go has no borrow checker
// and no deterministic destructor, so the runtime analog of the Rust contract
// (a view borrows the reader; the reader cannot close while borrowed) is a
// fixed-capacity handle registry owned by the reader:
//
//   - LookupX registers a handle and returns a view value holding its token.
//   - View operations verify the token is alive (ErrorHandleClosed if the
//     view was released or the reader already closed).
//   - Close returns ErrorHandleBusy while any registered handle is alive.
//   - Release explicitly returns the handle; it is idempotent.
//
// The registry is a fixed 1024-slot bitset allocated in the reader value
// (16 × 64-bit words, no heap state), so lookups stay zero-allocation and no
// map or mutex is needed. Like the Rust contract, the reader is not
// concurrency-safe: one reader is used by one logical owner at a time.

const handleCapacity = 1024

// handleRegistry tracks live public views of one reader.
type handleRegistry struct {
	bits  [handleCapacity / 64]uint64
	count uint16
	next  uint16
}

func (h *handleRegistry) register() (uint32, error) {
	for i := 0; i < handleCapacity/64; i++ {
		word := h.bits[i]
		if word == ^uint64(0) {
			continue
		}
		// Find the first free bit starting at the hinted position.
		for b := uint(h.next % 64); b < 64; b++ {
			if word&(1<<b) == 0 {
				tok := uint32(i*64) + uint32(b)
				h.bits[i] |= 1 << b
				h.count++
				h.next = uint16((tok + 1) % handleCapacity)
				return tok, nil
			}
		}
	}
	return 0, &Error{Code: ErrorHandleBusy, Detail: "handle capacity exhausted"}
}

func (h *handleRegistry) release(tok uint32) {
	if tok >= handleCapacity {
		return
	}
	h.bits[tok/64] &^= 1 << (tok % 64)
	h.count--
}

func (h *handleRegistry) alive(tok uint32) bool {
	if tok >= handleCapacity {
		return false
	}
	return h.bits[tok/64]&(1<<(tok%64)) != 0
}

func (h *handleRegistry) empty() bool { return h.count == 0 }
