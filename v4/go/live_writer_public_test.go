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
	requireLiveCreation(t)
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

	tx, err := w.BeginDirect(nil)
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
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 1)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := w.BeginDirect(nil)
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

	tx, err = w.BeginDirect(nil)
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
	requireLiveCreation(t)
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
	requireFileCreation(t)
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
	requireLiveCreation(t)
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
	requireLiveCreation(t)
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
	requireLiveCreation(t)
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

	tx, err := w.BeginDirect(nil)
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
	tx2, err := w.BeginDirect(nil)
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
	tx3, err := w.BeginDirect(nil)
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
	requireLiveCreation(t)
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

	tx, err := w.BeginDirect(nil)
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
	tx2, err := w.BeginDirect(nil)
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

// TestPublicLiveDirectCommitObservesCapturedCancellation pins the Rust
// DirectTransaction::commit contract (SOW-0027 slice 2a): the captured
// cancellation token checkpoints the commit attempt, the
// prepare-and-lock sequence, and the publication loop. A fired token
// before Commit publishes nothing, reports CommitNotCommitted with the
// TransactionAborted class, and leaves the live writer healthy for a
// fresh transaction.
func TestPublicLiveDirectCommitObservesCapturedCancellation(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	token := NewCancellationToken()
	tx, err := w.BeginDirect(token)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(20), 1); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	token.Cancel()
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitNotCommitted {
		t.Fatalf("commit status = %v, want NotCommitted (result %+v)", result.Status, result)
	}
	if code := lifecycleCode(result.Cause); code != ErrorTransactionAborted {
		t.Fatalf("commit cause = %v, want TransactionAborted", result.Cause)
	}
	// The writer stayed healthy and clean: a fresh transaction begins
	// and commits normally.
	fresh, err := w.BeginDirect(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fresh.AssignV4(IPv4(1), IPv4(5), 2); err != nil || !changed {
		t.Fatalf("fresh assign: changed=%v err=%v", changed, err)
	}
	if result, err := fresh.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("fresh commit: %v (result %+v)", err, result)
	}
}

// TestPublicLiveDirectMetadataObservesCapturedCancellation pins the
// missing Rust run_transaction checkpoints on the live direct metadata
// stages (SOW-0027 slice 2a): a fired token before a metadata stage
// aborts the transaction through the writer machine and spends the
// handle, exactly like the range operations.
func TestPublicLiveDirectMetadataObservesCapturedCancellation(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	token := NewCancellationToken()
	tx, err := w.BeginDirect(token)
	if err != nil {
		t.Fatal(err)
	}
	token.Cancel()
	if _, err := tx.SetMetadataJSON([]byte(`{"a":1}`)); err == nil {
		t.Fatal("metadata stage after cancellation succeeded")
	} else if code := lifecycleCode(err); code != ErrorTransactionAborted {
		t.Fatalf("metadata stage = %v, want TransactionAborted", err)
	}
	// The handle is spent by the aborted stage.
	if _, err := tx.SetMetadataJSON([]byte(`{"b":2}`)); err == nil {
		t.Fatal("metadata stage on a spent handle succeeded")
	}
	// A fresh transaction stages and commits metadata normally.
	fresh, err := w.BeginDirect(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fresh.SetMetadataJSON([]byte(`{"c":3}`)); err != nil || !changed {
		t.Fatalf("fresh stage: changed=%v err=%v", changed, err)
	}
	if result, err := fresh.Commit(); err != nil || result.Status != CommitCommitted {
		t.Fatalf("fresh commit: %v (result %+v)", err, result)
	}
}

// TestPublicLiveWriterMetadataReadYourWrites pins the clean-writer
// metadata read surface (Rust LiveWriter::metadata_json_len /
// read_metadata_json / metadata_json): the current-generation read
// observes the staged draft metadata before commit and the committed
// metadata after, absence is reported exactly, and an undersized caller
// buffer is refused with the buffer-too-small class.
func TestPublicLiveWriterMetadataReadYourWrites(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, present, err := w.MetadataJSONLen(); err != nil || present {
		t.Fatalf("initial metadata len: present=%v err=%v, want absent", present, err)
	}
	if _, present, err := w.MetadataJSON(); err != nil || present {
		t.Fatalf("initial metadata: present=%v err=%v, want absent", present, err)
	}

	// Stage metadata inside a direct transaction and read it back
	// before commit (Rust current_meta over the staged draft).
	tx, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.SetMetadataJSON([]byte(`{"staged":true}`)); err != nil || !changed {
		t.Fatalf("stage: changed=%v err=%v", changed, err)
	}
	length, present, err := w.MetadataJSONLen()
	if err != nil || !present || length != uint64(len(`{"staged":true}`)) {
		t.Fatalf("staged length = (%d, %v, %v), want the staged payload length", length, present, err)
	}
	value, present, err := w.MetadataJSON()
	if err != nil || !present || string(value) != `{"staged":true}` {
		t.Fatalf("staged metadata = %q (present %v, err %v), want the staged payload", value, present, err)
	}
	var small [4]byte
	if _, _, err := w.ReadMetadataJSON(small[:]); !isPubCode(err, ErrorBufferTooSmall) {
		t.Fatalf("small-buffer read = %v, want buffer too small", err)
	}
	output := make([]byte, 64)
	n, present, err := w.ReadMetadataJSON(output)
	if err != nil || !present || n != len(`{"staged":true}`) || string(output[:n]) != `{"staged":true}` {
		t.Fatalf("caller-buffer read = (%d, %v, %v), want the exact staged payload", n, present, err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	value, present, err = w.MetadataJSON()
	if err != nil || !present || string(value) != `{"staged":true}` {
		t.Fatalf("committed metadata = %q (present %v, err %v), want the staged payload", value, present, err)
	}

	// Clear metadata in a fresh transaction and read the absence.
	fresh, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := fresh.ClearMetadataJSON(); err != nil || !changed {
		t.Fatalf("clear: changed=%v err=%v", changed, err)
	}
	if _, present, err := w.MetadataJSONLen(); err != nil || present {
		t.Fatalf("staged-clear length: present=%v err=%v, want absent", present, err)
	}
	if _, err := fresh.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, present, err := w.MetadataJSON(); err != nil || present {
		t.Fatalf("post-clear metadata: present=%v err=%v, want absent", present, err)
	}
}

// TestPublicLiveReclaimWaitsForReadersThenAutoPublishes ports the Rust
// reclamation_waits_for_old_readers_then_auto_publishes vector: a
// pinned live reader blocks reclamation of the retirement its
// generation needs, closing it releases the safe frontier, and the
// reclamation then publishes as its own committed transaction while the
// visible values stay correct.
func TestPublicLiveReclaimWaitsForReadersThenAutoPublishes(t *testing.T) {
	requireLiveCreation(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := first.AssignV4(IPv4(10), IPv4(20), 1); err != nil || !changed {
		t.Fatalf("first assign: changed=%v err=%v", changed, err)
	}
	if _, err := first.Commit(); err != nil {
		t.Fatal(err)
	}

	pinned, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := pinned.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.TransactionID != 2 {
		t.Fatalf("pinned generation = %d, want 2", info.TransactionID)
	}
	second, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := second.AssignV4(IPv4(12), IPv4(18), 2); err != nil || !changed {
		t.Fatalf("second assign: changed=%v err=%v", changed, err)
	}
	if _, err := second.Commit(); err != nil {
		t.Fatal(err)
	}

	blocked, err := w.Reclaim(10, 10_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Outcome != ReclaimOutcomeNoChange {
		t.Fatalf("reclaim with a pinned old reader = %+v, want NoChange", blocked)
	}
	if _, err := pinned.Close(); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := w.Reclaim(10, 10_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Outcome != ReclaimOutcomeCommitted {
		t.Fatalf("reclaim after the reader closed = %+v, want Commit", reclaimed)
	}
	if reclaimed.TransactionCount != 1 {
		t.Fatalf("transaction count = %d, want 1", reclaimed.TransactionCount)
	}
	if reclaimed.PageCount == 0 {
		t.Fatal("page count = 0, want retired pages reclaimed")
	}
	if reclaimed.Commit.AttemptedTransactionID != 4 {
		t.Fatalf("reclamation transaction = %d, want 4", reclaimed.Commit.AttemptedTransactionID)
	}
	if reclaimed.Commit.Status != CommitCommitted {
		t.Fatalf("reclamation status = %v, want committed (cause %v)", reclaimed.Commit.Status, reclaimed.Commit.Cause)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if value, found, err := reader.LookupDirectV4(IPv4(11)); err != nil || !found || value != 1 {
		t.Fatalf("value at 11 = (%d, %v, %v), want (1, true, nil)", value, found, err)
	}
	if value, found, err := reader.LookupDirectV4(IPv4(15)); err != nil || !found || value != 2 {
		t.Fatalf("value at 15 = (%d, %v, %v), want (2, true, nil)", value, found, err)
	}
}
