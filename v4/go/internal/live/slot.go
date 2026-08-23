// Exact reader-table slot codec (Rust live_sidecar/slot.rs, spec
// section 15.1): 16 bytes at offset 4096 + slot*16 holding the selected
// transaction and its bitwise complement. An all-zero slot is inactive;
// a locked active slot has a nonzero transaction and an exact
// complement. No PID, claim nonce, transition state, or slot checksum
// exists: slot ownership is the lifetime byte-range lock, not the stale
// bytes.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

const slotSize = 16

const (
	slotTxnOff        = 0
	slotComplementOff = 8
)

// writeSlot encodes one active slot (Rust slot::write).
func writeSlot(slot []byte, transaction uint64) error {
	if len(slot) != slotSize {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot is not 16 bytes"}
	}
	format.PutU64(slot[slotTxnOff:], transaction)
	format.PutU64(slot[slotComplementOff:], ^transaction)
	return nil
}

// activeTransaction decodes a locked active slot and requires the exact
// complement (Rust slot::active_transaction).
func activeTransaction(slot []byte) (uint64, error) {
	transaction := format.U64(slot[slotTxnOff:])
	if transaction == 0 || format.U64(slot[slotComplementOff:]) != ^transaction {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "active reader slot is malformed"}
	}
	return transaction, nil
}

// slotIsClear reports whether the slot is entirely zero (Rust
// slot::is_clear).
func slotIsClear(slot []byte) bool {
	return allZero(slot, 0, slotSize)
}
