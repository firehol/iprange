// Bounded heap budget tests (Rust heap.rs): exact charge accounting,
// the insufficient-resource class on underflow, and the overflow guard
// on the modeled element multiplication.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestHeapBudgetChargesExactBytes pins the Rust Heap arithmetic: the
// used/remaining pair moves only by the charged bytes and the last byte
// of the budget is spendable.
func TestHeapBudgetChargesExactBytes(t *testing.T) {
	heap := newHeapBudget(100)
	if heap.usedBytes() != 0 || heap.remainingBytes() != 100 {
		t.Fatalf("fresh heap = used %d remaining %d", heap.usedBytes(), heap.remainingBytes())
	}
	if err := heap.reserveBytes(40, "history projection heap"); err != nil {
		t.Fatal(err)
	}
	if err := heap.filled(5, 8, "history projection heap"); err != nil {
		t.Fatal(err)
	}
	if err := heap.vector(4, 4, "history projection heap"); err != nil {
		t.Fatal(err)
	}
	if heap.usedBytes() != 96 || heap.remainingBytes() != 4 {
		t.Fatalf("charged heap = used %d remaining %d", heap.usedBytes(), heap.remainingBytes())
	}
	if err := heap.reserveBytes(4, "history projection heap"); err != nil {
		t.Fatal("the last budget byte is spendable")
	}
	if heap.remainingBytes() != 0 {
		t.Fatalf("remaining = %d, want 0", heap.remainingBytes())
	}
}

// TestHeapBudgetExceededPinsTheClass mirrors the Rust BudgetExceeded
// class: every overcharge fails with the insufficient-resource code and
// the operation label, and the budget is left untouched.
func TestHeapBudgetExceededPinsTheClass(t *testing.T) {
	heap := newHeapBudget(16)
	if err := heap.reserveBytes(17, "history projection heap"); err == nil {
		t.Fatal("overcharge accepted")
	} else if code := errCode(err); code != format.CodeInsufficientResourceBudget {
		t.Fatalf("code = %d, want InsufficientResourceBudget", code)
	} else if detail := err.(*format.Error).Detail; detail != "history projection heap" {
		t.Fatalf("detail = %q, want the operation label", detail)
	}
	if heap.remainingBytes() != 16 {
		t.Fatalf("failed charge moved the budget: remaining %d", heap.remainingBytes())
	}
	// An element-count product beyond u64 fails the same class instead
	// of wrapping (Rust checked_mul).
	if err := heap.vector(1<<63, 4, "history projection heap"); err == nil {
		t.Fatal("overflowing vector charge accepted")
	} else if code := errCode(err); code != format.CodeInsufficientResourceBudget {
		t.Fatalf("overflow code = %d, want InsufficientResourceBudget", code)
	}
}

// TestHeapBudgetZeroBudgetRefusesEverything pins the zero-budget shape:
// any charge, even an empty one, fails under a zero ceiling (Rust
// Heap::new(0)).
func TestHeapBudgetZeroBudgetRefusesEverything(t *testing.T) {
	heap := newHeapBudget(0)
	if err := heap.filled(1, 1, "history projection heap"); err == nil {
		t.Fatal("zero-budget charge accepted")
	}
}
