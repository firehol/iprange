package random

import "testing"

// TestNonzero128 pins the nonzero_128 contract: the draw succeeds and
// never returns the zero identity (Rust random.rs rejects an all-zero
// fill as Corrupt; the draw path itself is unreachable in a test).
func TestNonzero128(t *testing.T) {
	a, err := Nonzero128()
	if err != nil {
		t.Fatalf("nonzero draw failed: %v", err)
	}
	if a == [16]byte{} {
		t.Fatalf("nonzero draw returned the zero identity")
	}
	b, err := Nonzero128()
	if err != nil {
		t.Fatalf("second nonzero draw failed: %v", err)
	}
	if a == b {
		t.Fatalf("two draws returned the same identity")
	}
}
