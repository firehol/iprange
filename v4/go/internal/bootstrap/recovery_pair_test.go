package bootstrap

// Order-proof boundary tests (Rust classify_tests adjacent_order arm):
// the adjacent transaction proof must refuse a wrapping lower
// transaction exactly like the Rust checked_add.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestAdjacentOrderRefusesTransactionWrap pins the checked_add
// boundary (the Rust adjacent_order authority): a lower transaction
// of 2^64-1 can never be the predecessor of a higher transaction.
func TestAdjacentOrderRefusesTransactionWrap(t *testing.T) {
	var states [2]RecoveryMetaState
	states[0] = RecoveryMetaState{Order: format.Meta{TxnID: 0}, OrderValid: true}
	states[1] = RecoveryMetaState{Order: format.Meta{TxnID: ^uint64(0)}, OrderValid: true}
	pair := ClassifyRecoveryMetaPair(states, [2]bool{true, true})
	if pair.Proven() {
		t.Fatalf("order %+v, want unproven (the lower transaction cannot wrap)", pair)
	}
}
