// Public live writer round trip (Rust tests/live_roundtrip.rs writer
// cases through the public facade): OpenLiveWriter, the direct
// transaction commit barrier, abort, close, and the public result
// surfaces.

package iprangedb

import (
	"path/filepath"
	"testing"
)

// createLivePublicPair creates one live IPv4 direct pair and returns the
// main path plus the created identity facts.
func createLivePublicPair(t *testing.T, capacity uint32) (string, CreateResult) {
	t.Helper()
	main := filepath.Join(t.TempDir(), "db.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, capacity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", created.State)
	}
	return main, created
}

func TestPublicOpenLiveWriterRoundTrip(t *testing.T) {
	main, created := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}

	info, err := w.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.TransactionID != 1 || info.DatabaseID != created.DatabaseID {
		t.Fatalf("info = %+v, want txn 1 of the created pair", info)
	}
	if info.MetaSelection != MetaSelectionProvenCurrent {
		t.Fatalf("meta selection = %v, want ProvenCurrent", info.MetaSelection)
	}

	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(30), 1); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if changed, err := tx.AssignV4(IPv4(22), IPv4(23), 3); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("status = %v, want committed (cause %v)", result.Status, result.Cause)
	}
	if result.AttemptedTransactionID != 2 {
		t.Fatalf("txn = %d, want 2", result.AttemptedTransactionID)
	}
	if result.AttemptedDatabaseID != created.DatabaseID {
		t.Fatal("attempt identity mismatch")
	}
	if result.DirectoryIdentity == nil || result.MainIdentity == nil {
		t.Fatal("identity facts missing on a committed result")
	}
	if result.DirectoryIdentity.Kind != 1 || result.MainIdentity.Kind != 1 {
		t.Fatalf("identity kinds = %d/%d, want 1", result.DirectoryIdentity.Kind, result.MainIdentity.Kind)
	}
	if !result.Cleanup.Empty() || result.Cleanup.CleanupState() != CleanupStateClean {
		t.Fatal("cleanup not clean on a committed result")
	}
	if result.CoordinationCleanup != CoordinationCleanupNone || result.CleanupState() != CleanupStateClean {
		t.Fatal("coordination residue on a committed result")
	}

	// A spent transaction cannot commit again (Rust NoPendingTransaction).
	if _, err := tx.Commit(); err == nil {
		t.Fatal("second commit succeeded on a spent transaction")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("second commit = %v, want NoPendingTransaction", err)
	}

	closeResult, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeResult.Outcome != CloseOutcomeClosed {
		t.Fatalf("close outcome = %v, want closed", closeResult.Outcome)
	}
	if closeResult.AbortOutcome != nil || closeResult.CleanupState() != CleanupStateClean {
		t.Fatalf("close facts = %+v, want clean closed", closeResult)
	}
	closeResult, err = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeResult.Outcome != CloseOutcomeClosed {
		t.Fatalf("second close outcome = %v, want idempotent closed", closeResult.Outcome)
	}
	if _, err := w.Info(); err == nil {
		t.Fatal("Info succeeded after close")
	}
}

func TestPublicLiveWriterNoopAndAbort(t *testing.T) {
	main, _ := createLivePublicPair(t, 1)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.ClearV4(IPv4(1), IPv4(2)); err != nil || changed {
		t.Fatalf("noop clear: changed=%v err=%v", changed, err)
	}
	if _, err := tx.Commit(); err == nil {
		t.Fatal("noop commit succeeded")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("noop commit = %v, want NoPendingTransaction", err)
	}

	tx, err = w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(1), IPv4(2), 7); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	abort, err := tx.Abort()
	if err != nil {
		t.Fatal(err)
	}
	if abort.Outcome != AbortOutcomeAborted {
		t.Fatalf("abort outcome = %v, want aborted", abort.Outcome)
	}
	if abort.CleanupState() != CleanupStateClean || abort.CoordinationCleanup != CoordinationCleanupNone {
		t.Fatalf("abort facts = %+v, want clean", abort)
	}
	if _, err := tx.Commit(); err == nil {
		t.Fatal("commit after abort succeeded")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("commit after abort = %v, want NoPendingTransaction", err)
	}

	closeResult, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeResult.Outcome != CloseOutcomeClosed || closeResult.AbortOutcome != nil {
		t.Fatalf("close = %+v, want closed without abort", closeResult)
	}
}

func TestPublicLiveWriterSecondOpenIsWriterBusy(t *testing.T) {
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := OpenLiveWriter(main, DefaultBudget(), nil); err == nil {
		t.Fatal("second open succeeded while the lease is held")
	} else if code := lifecycleCode(err); code != ErrorWriterBusy {
		t.Fatalf("second open = %v, want WriterBusy", err)
	}
}

func TestPublicOpenLiveWriterRefusesNonLiveMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	if _, err := Create(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLiveWriter(main, DefaultBudget(), nil); err == nil {
		t.Fatal("live writer opened an immutable main")
	}
}

func TestPublicLiveWriterCancelledOpenLeavesNoResidue(t *testing.T) {
	main, _ := createLivePublicPair(t, 2)
	cancelled := NewCancellationToken()
	cancelled.Cancel()
	if _, err := OpenLiveWriter(main, DefaultBudget(), cancelled); err == nil {
		t.Fatal("cancelled open succeeded")
	} else if code := lifecycleCode(err); code != ErrorCancelled {
		t.Fatalf("cancelled open = %v, want Cancelled", err)
	}
	// The locks were released: a clean open succeeds.
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("open after cancelled attempt: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicLiveWriterBudgetValidation pins the Rust
// TransactionBudget::validate gate at LiveWriter::open: a live writer
// owns the main descriptor plus the sidecar descriptor, so an
// open-files bound below two is refused with the budget class before
// any path access (Rust "a live writer requires two open files").
func TestPublicLiveWriterBudgetValidation(t *testing.T) {
	main, _ := createLivePublicPair(t, 2)
	small := DefaultBudget()
	small.MaxOpenFiles = 1
	if _, err := OpenLiveWriter(main, small, nil); err == nil {
		t.Fatal("live writer opened with a one-file budget")
	} else if code := lifecycleCode(err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("budget refusal = %v, want InsufficientResourceBudget", err)
	}
	// The zero default value also refuses (no accidental pass-through).
	zero := DefaultBudget()
	zero.MaxOpenFiles = 0
	if _, err := OpenLiveWriter(main, zero, nil); err == nil {
		t.Fatal("live writer opened with a zero open-files bound")
	}
}

// TestPublicLiveDirectCommitAbortAfterOpFailure pins the Rust
// commit_attempt/abort contracts on the public direct transaction after
// a failed operation: the failed op aborts the draft (TransactionAborted
// class, Rust mutate -> abort_after), so the terminal Commit and Abort
// on that spent transaction report NoPendingTransaction (Rust
// commit_attempt and LiveWriter::abort have no transaction-nonce gate
// and see a draft-less core), never WrongState. The writer stays healthy
// and a fresh transaction commits normally.
func TestPublicLiveDirectCommitAbortAfterOpFailure(t *testing.T) {
	main, _ := createLivePublicPair(t, 2)
	// Zero heap budget: the range assign works (the IPv4 pending
	// locator is not heap-bounded), and the metadata compression heap
	// bound fails inside the store, so the op fails mid-edit exactly
	// like the Rust direct budget tests.
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100, MaxOpenFiles: 2}
	w, err := OpenLiveWriter(main, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(20), 5); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	// The metadata compression heap charge fails mid-edit: the op
	// aborts the draft and reports the TransactionAborted class.
	if _, err := tx.SetMetadataJSON([]byte("x")); err == nil {
		t.Fatal("metadata stage succeeded under a zero heap budget")
	} else if code := lifecycleCode(err); code != ErrorTransactionAborted {
		t.Fatalf("failed op = %v, want TransactionAborted", err)
	}
	// The draft is gone: the terminal calls report the Rust
	// NoPendingTransaction class, not WrongState.
	if _, err := tx.Commit(); err == nil {
		t.Fatal("commit succeeded after the aborted op")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("commit after aborted op = %v, want NoPendingTransaction", err)
	}
	if _, err := tx.Commit(); err == nil {
		t.Fatal("second commit succeeded on the spent transaction")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("spent commit = %v, want NoPendingTransaction", err)
	}

	// The same contract on Abort after a failed op.
	tx2, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx2.AssignV4(IPv4(1), IPv4(2), 3); err != nil || !changed {
		t.Fatalf("assign 2: changed=%v err=%v", changed, err)
	}
	if _, err := tx2.SetMetadataJSON([]byte("y")); err == nil {
		t.Fatal("metadata stage 2 succeeded under a zero heap budget")
	} else if code := lifecycleCode(err); code != ErrorTransactionAborted {
		t.Fatalf("failed op 2 = %v, want TransactionAborted", err)
	}
	if _, err := tx2.Abort(); err == nil {
		t.Fatal("abort succeeded after the aborted op")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("abort after aborted op = %v, want NoPendingTransaction", err)
	}

	// The failure class is not fatal (budget exhaustion): a fresh
	// transaction commits normally and the partial mutation never
	// published.
	tx3, err := w.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect after aborted ops: %v", err)
	}
	if changed, err := tx3.AssignV4(IPv4(30), IPv4(40), 7); err != nil || !changed {
		t.Fatalf("post-abort assign: changed=%v err=%v", changed, err)
	}
	result, err := tx3.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("post-abort status = %v, want committed (cause %v)", result.Status, result.Cause)
	}
	if result.AttemptedTransactionID != 2 {
		t.Fatalf("post-abort txn = %d, want 2", result.AttemptedTransactionID)
	}
}

// TestPublicLiveStaleHandleAfterOpFailure pins the Rust operation-nonce
// gate on the public live direct transaction: a failed op discards the
// draft and spends the handle, so once a newer transaction began the
// stale handle must refuse every mutation with WrongState (Rust
// require_transaction operation_is) and its terminal Commit with
// NoPendingTransaction, and it must never touch the newer draft.
func TestPublicLiveStaleHandleAfterOpFailure(t *testing.T) {
	main, _ := createLivePublicPair(t, 2)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100, MaxOpenFiles: 2}
	w, err := OpenLiveWriter(main, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(20), 5); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if _, err := tx.SetMetadataJSON([]byte("x")); err == nil {
		t.Fatal("metadata stage succeeded under a zero heap budget")
	} else if code := lifecycleCode(err); code != ErrorTransactionAborted {
		t.Fatalf("failed op = %v, want TransactionAborted", err)
	}
	// A newer transaction began with its own draft; the stale handle
	// must not see it (Rust: the nonce lives in the discarded draft).
	tx2, err := w.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect after aborted op: %v", err)
	}
	if _, err := tx.AssignV4(IPv4(1), IPv4(2), 3); err == nil {
		t.Fatal("stale handle assigned into the newer draft")
	} else if code := lifecycleCode(err); code != ErrorWrongState {
		t.Fatalf("stale op = %v, want WrongState", err)
	}
	if _, err := tx.Commit(); err == nil {
		t.Fatal("stale commit published the newer draft")
	} else if code := lifecycleCode(err); code != ErrorNoPendingTransaction {
		t.Fatalf("stale commit = %v, want NoPendingTransaction", err)
	}
	// The newer draft is intact and commits its own value.
	if changed, err := tx2.AssignV4(IPv4(50), IPv4(60), 9); err != nil || !changed {
		t.Fatalf("newer assign: changed=%v err=%v", changed, err)
	}
	result, err := tx2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("newer status = %v, want committed (cause %v)", result.Status, result.Cause)
	}
}
