package recovery

// Page-set and budget tests ported from the Rust recovery
// page_set_tests heap arms plus the claim surface: the sparse sizing,
// the heap load cap, the claim reason classes, the reset, and the
// budget refusals.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

func TestSparseMaximumPageDoesNotSizeTheHeapTable(t *testing.T) {
	set, err := newPageSet(1024, format.MaxPageCount)
	if err != nil {
		t.Fatalf("newPageSet: %v", err)
	}
	if len(set.slots) > 128 {
		t.Fatalf("slot count %d, want at most 128", len(set.slots))
	}
	newClaim, err := set.insert(^uint32(0))
	if err != nil || !newClaim {
		t.Fatalf("insert max page: new=%v err=%v", newClaim, err)
	}
	newClaim, err = set.insert(^uint32(0))
	if err != nil || newClaim {
		t.Fatalf("re-insert max page: new=%v err=%v", newClaim, err)
	}
}

func TestFullHeapTableFailsBeforeAllocationOrLooping(t *testing.T) {
	set, err := newPageSet(64, 100)
	if err != nil {
		t.Fatalf("newPageSet: %v", err)
	}
	if len(set.slots) != 8 {
		t.Fatalf("slot count %d, want 8", len(set.slots))
	}
	for page := uint32(0); page < 6; page++ {
		newClaim, err := set.insert(page)
		if err != nil || !newClaim {
			t.Fatalf("insert %d: new=%v err=%v", page, newClaim, err)
		}
	}
	if _, err := set.insert(7); err == nil {
		t.Fatal("heap load cap accepted")
	}
}

func TestPageSetClaimReasonClasses(t *testing.T) {
	set, err := newPageSet(4096, 100)
	if err != nil {
		t.Fatalf("newPageSet: %v", err)
	}
	var path [format.MaxTreeLevel + 1]uint32
	// The meta pages and the out-of-range pages refuse with their
	// deterministic classes.
	claimed, reason, err := set.claim(0, 16, path[:], 0)
	if err != nil || claimed || reason != validation.ReasonPageOutOfBounds {
		t.Fatalf("claim meta page: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	claimed, reason, err = set.claim(16, 16, path[:], 0)
	if err != nil || claimed || reason != validation.ReasonPageOutOfBounds {
		t.Fatalf("claim past end: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	claimed, reason, err = set.claim(99, 16, path[:], 0)
	if err != nil || claimed || reason != validation.ReasonPageOutOfBounds {
		t.Fatalf("claim past end: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	// A depth beyond the path refuses with the level class.
	claimed, reason, err = set.claim(2, 16, path[:2], 2)
	if err != nil || claimed || reason != validation.ReasonTreeLevelInvalid {
		t.Fatalf("claim past depth: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	// A claim records the page on the path; the second claim through a
	// path that already carries it is the cycle class; an alien second
	// claim is the alias class.
	claimed, reason, err = set.claim(2, 16, path[:], 0)
	if err != nil || !claimed || reason != 0 {
		t.Fatalf("first claim: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	claimed, reason, err = set.claim(3, 16, path[:2], 1)
	if err != nil || !claimed || reason != 0 {
		t.Fatalf("second claim: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	claimed, reason, err = set.claim(2, 16, path[:3], 2)
	if err != nil || claimed || reason != validation.ReasonTreeCycle {
		t.Fatalf("cycle claim: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	claimed, reason, err = set.claim(3, 16, path[:3], 1)
	if err != nil || claimed || reason != validation.ReasonPageAlias {
		t.Fatalf("alias claim: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	// Reset clears every claim, and the retained bytes are the exact
	// heap table size.
	if set.retainedBytes() != uint64(len(set.slots))*slotBytes {
		t.Fatalf("retained %d", set.retainedBytes())
	}
	if err := set.reset(); err != nil {
		t.Fatal(err)
	}
	claimed, reason, err = set.claim(2, 16, path[:], 0)
	if err != nil || !claimed || reason != 0 {
		t.Fatalf("claim after reset: claimed=%v reason=%v err=%v", claimed, reason, err)
	}
	// The finish terminal carries the cause and no scratch cleanup.
	failure := set.finish(&format.Error{Code: format.CodeIO, Detail: "boom"})
	if failure.cause == nil || failure.cleanup != nil {
		t.Fatalf("fail finish %+v", failure)
	}
}

func TestRecoveryBudgetRefusals(t *testing.T) {
	budget := HeapOnly(1<<20, 100, 1)
	wantBudgetRefusal(t, budget.validate(), format.CodeInsufficientResourceBudget, "recovery requires source and output files")
	budget = HeapOnly(1<<20, 1, 2)
	wantBudgetRefusal(t, budget.validate(), format.CodeInsufficientResourceBudget, "recovery output pages")
	scratch := &RecoveryBudget{MaxHeapBytes: 1 << 20, MaxOutputPages: 100, MaxOpenFiles: 2, MaxScratchBytes: 1}
	wantBudgetRefusal(t, scratch.validate(), format.CodeInvalidArgument, "recovery scratch path and limits must be supplied together")
	limitsWithoutDir := &RecoveryBudget{MaxHeapBytes: 1 << 20, MaxOutputPages: 100, MaxOpenFiles: 2, MaxScratchBytes: 1, MaxScratchFiles: 1}
	wantBudgetRefusal(t, limitsWithoutDir.validate(), format.CodeInvalidArgument, "recovery scratch path and limits must be supplied together")
	filesWithoutBytes := &RecoveryBudget{MaxHeapBytes: 1 << 20, MaxOutputPages: 100, MaxOpenFiles: 2, MaxScratchFiles: 1, ScratchDirectory: "/tmp"}
	wantBudgetRefusal(t, filesWithoutBytes.validate(), format.CodeInvalidArgument, "recovery scratch path and limits must be supplied together")
	budget = HeapOnly(1<<20, 100, 2)
	if err := budget.validate(); err != nil {
		t.Fatalf("valid heap-only budget refused: %v", err)
	}
	// The same refusal runs through the machine before any path access
	// (Rust api.rs validate_budget before the destination attempt and
	// the source open): the missing source and destination paths are
	// never touched. The machine folds the cause through the fixed
	// problem() detail exactly like Rust terminal.rs early over
	// source_guard.rs problem (the budget code survives).
	result, failure := RecoverImmutable(filepath.Join(t.TempDir(), "missing.v4"), &RecoveryCandidate{Label: CandidateNewest}, filepath.Join(t.TempDir(), "out.v4"), HeapOnly(1<<20, 100, 1), nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("machine = (%v, %v), want the early failure arm", result, failure)
	}
	var e *format.Error
	if !errors.As(failure.Cause, &e) || e.Code != format.CodeInsufficientResourceBudget || e.Detail != "recovery source operation failed" {
		t.Fatalf("machine cause = %v, want the budget class with the fixed problem fold", failure.Cause)
	}
	if !failure.Cleanup.Empty() {
		t.Fatalf("machine cleanup = %+v, want empty (nothing was created)", failure.Cleanup)
	}
}

// wantBudgetRefusal fails unless err is exactly the verbatim Rust
// budget class and detail (budget.rs validate; the Go format codes
// mirror the Rust error classes).
func wantBudgetRefusal(t *testing.T, err error, code format.ErrorCode, detail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %q, got nil", detail)
	}
	var e *format.Error
	if !errors.As(err, &e) || e.Code != code || e.Detail != detail {
		t.Fatalf("budget error = %v, want code %d detail %q", err, code, detail)
	}
}
