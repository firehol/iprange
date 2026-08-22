// Retirement successor-key parity (Rust retirement::after): first+1
// wraps at the u32 boundary to the first key of the next transaction,
// and an overflowing transaction has no successor at all.

package retire

import (
	"testing"
)

func TestAfterWrapsFirstIntoNextTransaction(t *testing.T) {
	st := newMemoryStore(3)
	root := uint32(0)
	count := uint64(0)
	if _, err := AddPage(st, &root, &count, 2, 5); err != nil {
		t.Fatalf("AddPage: %v", err)
	}
	// The successor of (2, MaxUint32) is the first key of transaction 3:
	// no such extent exists, so After must report none. The wrapped key
	// (2, 0) would instead re-find the stored (2, 5) extent and make the
	// reclamation scan loop or fail on overlap.
	got, hasGot, err := After(st, root, Extent{Key: Key{Txn: 2, First: ^uint32(0)}})
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	if hasGot {
		t.Fatalf("After((2, MaxUint32)) = %+v, want none", got)
	}
}
