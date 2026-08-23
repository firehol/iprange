//go:build linux || darwin

// Functional tests for the live writer open, the gate-around-Publish
// commit barrier, and close (Rust tests/live_roundtrip.rs writer cases
// through the internal live writer). The crash-point matrix lives in
// lifecycle_crash_test.go under the v4work tag.

package live

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

func liveWriterTestBudget() writer.PageBudget {
	return writer.PageBudget{MaxHeapBytes: 1 << 20, MaxPrivatePages: 4096, MaxGrowthPages: 4096, MaxOpenFiles: 2}
}

// copyMainForImmutableRead copies the main file to a sibling name with
// no sidecar so the immutable reader (which refuses live pairs by
// design, Rust open_immutable sidecar-absent rule) can prove the
// committed payload of a closed live writer.
func copyMainForImmutableRead(t *testing.T, main string) string {
	t.Helper()
	copy := main + ".copy"
	raw, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(copy, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return copy
}

// createLiveV4Pair creates one live IPv4 direct pair with the given
// reader capacity and returns the main path.
func createLiveV4Pair(t *testing.T, capacity uint32) string {
	t.Helper()
	main := filepath.Join(t.TempDir(), "db.iprdb")
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, capacity, nil); err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	return main
}

// openTestLiveWriter opens the live writer with the test budget and no
// cancellation.
func openTestLiveWriter(t *testing.T, main string) *LiveWriter {
	t.Helper()
	w, err := OpenLiveWriter(main, liveWriterTestBudget(), nil, nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	return w
}

func TestLiveWriterOpenCloseRoundTrip(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	w := openTestLiveWriter(t, main)
	info, err := w.BaseInfo()
	if err != nil {
		t.Fatalf("BaseInfo: %v", err)
	}
	if got := info.TransactionID; got != 1 {
		t.Fatalf("txn = %d, want 1", got)
	}

	result, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if result.Outcome != CloseOutcomeClosed {
		t.Fatalf("close outcome = %v, want closed", result.Outcome)
	}
	if result.AbortOutcome != nil {
		t.Fatalf("abort outcome = %v, want none", *result.AbortOutcome)
	}
	if result.Cleanup != (CommitCleanupArtifacts{}) || result.CoordinationCleanup != CoordinationCleanupNone || result.Cause != nil {
		t.Fatalf("close facts = %+v, want clean", result)
	}

	// A second close is idempotent success (Rust close on State::Closed).
	result, err = w.Close()
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if result.Outcome != CloseOutcomeClosed || result.AbortOutcome != nil {
		t.Fatalf("second close = %+v, want idempotent closed", result)
	}
	if _, err := w.BaseInfo(); err == nil {
		t.Fatal("BaseInfo succeeded after close")
	} else {
		expectCode(t, err, format.CodeWrongState)
	}

	// The lease and locks are released: a fresh writer opens again.
	w2 := openTestLiveWriter(t, main)
	w2.Close()
}

func TestLiveWriterSecondOpenIsWriterBusy(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	w := openTestLiveWriter(t, main)
	defer w.Close()

	if _, err := OpenLiveWriter(main, liveWriterTestBudget(), nil, nil); err == nil {
		t.Fatal("second open succeeded while the lease is held")
	} else {
		expectCode(t, err, format.CodeWriterBusy)
	}
}

func TestLiveWriterDirectCommitAdvancesGeneration(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	w := openTestLiveWriter(t, main)
	defer w.Close()

	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	for _, op := range []struct {
		apply func() (bool, error)
	}{
		{func() (bool, error) { return w.AssignV4(10, 30, 1) }},
		{func() (bool, error) { return w.AssignV4(20, 25, 2) }},
		{func() (bool, error) { return w.AssignV4(22, 23, 3) }},
	} {
		if changed, err := op.apply(); err != nil || !changed {
			t.Fatalf("assign: changed=%v err=%v", changed, err)
		}
	}

	attempt, err := w.core.CommitAttempt()
	if err != nil {
		t.Fatalf("CommitAttempt: %v", err)
	}
	result, commitErr := w.Commit(nil)
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}
	if result.Durability != CommitCommitted {
		t.Fatalf("durability = %v, want committed (cause %v)", result.Durability, result.Cause)
	}
	if result.AttemptedTransactionID != 2 {
		t.Fatalf("txn = %d, want 2", result.AttemptedTransactionID)
	}
	if result.AttemptedDatabaseID != attempt.DatabaseID || result.AttemptedCommitNonce != attempt.CommitNonce {
		t.Fatal("commit result does not carry the attempt identity")
	}
	if result.DirectoryIdentity != w.directoryIdentity || result.MainIdentity != w.mainIdentity {
		t.Fatal("commit result identity mismatch")
	}
	if !result.Cleanup.Empty() || result.CoordinationCleanup != CoordinationCleanupNone || result.Cause != nil {
		t.Fatalf("commit facts = %+v, want clean", result)
	}

	// Close discards nothing (the draft was consumed) and reports the
	// closed outcome without an abort.
	closeResult, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeResult.Outcome != CloseOutcomeClosed || closeResult.AbortOutcome != nil {
		t.Fatalf("close = %+v, want closed without abort", closeResult)
	}

	// The immutable reader sees the new generation and the three ranges
	// with the last-write-wins value at the overlap (Rust
	// live_roundtrip::live_generations_are_atomic_and_old_readers_stay_pinned).
	r, err := reader.OpenImmutable(copyMainForImmutableRead(t, main))
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	if r.Meta().TxnID != 2 {
		t.Fatalf("txn = %d, want 2", r.Meta().TxnID)
	}
	for _, want := range []struct {
		addr uint32
		val  uint32
		ok   bool
	}{
		{19, 1, true},
		{21, 2, true},
		{22, 3, true},
		{9, 0, false},
		{31, 0, false},
	} {
		got, ok, err := r.LookupDirect4(want.addr)
		if err != nil {
			t.Fatalf("LookupDirect4(%d): %v", want.addr, err)
		}
		if ok != want.ok || got != want.val {
			t.Fatalf("LookupDirect4(%d) = (%d,%v), want (%d,%v)", want.addr, got, ok, want.val, want.ok)
		}
	}
}

func TestLiveWriterNoopCommitIsNoPendingTransaction(t *testing.T) {
	main := createLiveV4Pair(t, 1)
	w := openTestLiveWriter(t, main)
	defer w.Close()

	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.ClearV4(1, 2); err != nil || changed {
		t.Fatalf("noop clear: changed=%v err=%v", changed, err)
	}
	if _, err := w.Commit(nil); err == nil {
		t.Fatal("noop commit succeeded")
	} else {
		expectCode(t, err, format.CodeNoPendingTransaction)
	}
	// The writer stays healthy after the noop discard.
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect after noop: %v", err)
	}
	if changed, err := w.AssignV4(1, 2, 7); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Durability != CommitCommitted {
		t.Fatalf("durability = %v, want committed", result.Durability)
	}
}

func TestLiveWriterAbortDiscardsDraft(t *testing.T) {
	main := createLiveV4Pair(t, 1)
	w := openTestLiveWriter(t, main)

	// Abort without a draft is NoPendingTransaction (Rust abort).
	if _, err := w.Abort(); err == nil {
		t.Fatal("abort without a draft succeeded")
	} else {
		expectCode(t, err, format.CodeNoPendingTransaction)
	}

	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(1, 2, 7); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	result, err := w.Abort()
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if result.Outcome != AbortOutcomeAborted {
		t.Fatalf("abort outcome = %v, want aborted", result.Outcome)
	}
	if !result.Cleanup.Empty() || result.CoordinationCleanup != CoordinationCleanupNone || result.Cause != nil {
		t.Fatalf("abort facts = %+v, want clean", result)
	}

	// The spent transaction cannot commit (Rust commit_attempt
	// NoPendingTransaction for a discarded draft).
	if _, err := w.Commit(nil); err == nil {
		t.Fatal("commit after abort succeeded")
	} else {
		expectCode(t, err, format.CodeNoPendingTransaction)
	}

	// The abort consumed the draft, so close has no pending draft and
	// reports the closed outcome without an abort (Rust had_pending =
	// core.has_draft()).
	closeResult, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeResult.Outcome != CloseOutcomeClosed || closeResult.AbortOutcome != nil {
		t.Fatalf("close = %+v, want closed without abort", closeResult)
	}

	// A close over an open draft reports the had-pending abort outcome
	// and discards it (Rust close_failure/CloseResult::closed with the
	// Aborted payload).
	w2 := openTestLiveWriter(t, main)
	if err := w2.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w2.AssignV4(1, 2, 7); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	closeResult, err = w2.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeResult.Outcome != CloseOutcomeClosed || closeResult.AbortOutcome == nil || *closeResult.AbortOutcome != AbortOutcomeAborted {
		t.Fatalf("close with pending = %+v, want closed with aborted", closeResult)
	}

	// Nothing was published by the abort or the pending close.
	r, err := reader.OpenImmutable(copyMainForImmutableRead(t, main))
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	if r.Meta().TxnID != 1 {
		t.Fatalf("txn = %d, want 1", r.Meta().TxnID)
	}
	if got, ok, err := r.LookupDirect4(1); err != nil || ok || got != 0 {
		t.Fatalf("LookupDirect4(1) = (%d,%v,%v), want none", got, ok, err)
	}
}

func TestLiveWriterCommitBarrierRejectsNewerReader(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	w := openTestLiveWriter(t, main)

	// A reader claims a transaction newer than the committed generation
	// (Rust live_roundtrip pinned-reader scenario) through its own
	// sidecar descriptor, so the slot lock is a separate open-file
	// description exactly like a real reader: the prepublication slot
	// scan must refuse the commit.
	readerSidecar, err := open(main, w.sidecar.header.databaseID)
	if err != nil {
		t.Fatalf("reader sidecar open: %v", err)
	}
	defer readerSidecar.close()
	slot, err := readerSidecar.claimReaderCancellable(99, nil)
	if err != nil {
		t.Fatalf("claimReader: %v", err)
	}
	releaseReader := func() {
		if err := readerSidecar.clearReader(slot); err != nil {
			t.Fatalf("clearReader: %v", err)
		}
		if err := readerSidecar.unlockReader(slot); err != nil {
			t.Fatalf("unlockReader: %v", err)
		}
	}

	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(10, 20, 1); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	result, commitErr := w.Commit(nil)
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}
	if result.Durability != CommitNotCommitted {
		t.Fatalf("durability = %v, want not committed", result.Durability)
	}
	// The cause is the TransactionAborted class wrapping the slot-scan
	// FormatInvalid failure (Rust abort_after_source), and the
	// FormatInvalid class is fatal: the writer fails closed (Rust
	// abort_after: Io | Format | Corrupt).
	var fe *format.Error
	if !errors.As(result.Cause, &fe) || fe.Code != format.CodeTransactionAborted {
		t.Fatalf("cause = %v, want the TransactionAborted class", result.Cause)
	}
	inner, ok := result.Cause.(*classedError)
	if !ok {
		t.Fatalf("cause type = %T, want classedError", result.Cause)
	}
	expectCode(t, inner.cause, format.CodeFormatInvalid)
	if result.CoordinationCleanup != CoordinationCleanupRetainedWriterCloseRequired {
		t.Fatalf("coordination = %v, want retained writer close", result.CoordinationCleanup)
	}
	if _, err := w.BaseInfo(); err == nil {
		t.Fatal("writer stayed healthy after a fatal commit failure")
	}

	// Close retries from the full path; after the newer reader releases
	// its slot the close completes (Rust close_locked scan_at_most).
	releaseReader()
	closeResult, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeResult.Outcome != CloseOutcomeClosed {
		t.Fatalf("close = %+v, want closed (cause %v)", closeResult, closeResult.Cause)
	}
}

func TestLiveWriterOpenCancellationReleasesLocks(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	if _, err := OpenLiveWriter(main, liveWriterTestBudget(), nil, cancelledCheck); err == nil {
		t.Fatal("open with a pre-cancelled check succeeded")
	} else {
		expectCode(t, err, format.CodeCancelled)
	}
	// No lease or lifetime lock residue: a clean open succeeds.
	w := openTestLiveWriter(t, main)
	w.Close()
}

func TestLiveWriterCommitCancellationAbortsDraft(t *testing.T) {
	main := createLiveV4Pair(t, 1)
	w := openTestLiveWriter(t, main)
	defer w.Close()

	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(1, 5, 9); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	// The prepare checkpoint fires after the draft is prepared; the
	// failure aborts the draft and reports NotCommitted with the
	// TransactionAborted class (Rust prepare_and_lock cancellation).
	result, commitErr := w.Commit(failsAfter(1))
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}
	if result.Durability != CommitNotCommitted {
		t.Fatalf("durability = %v, want not committed", result.Durability)
	}
	var fe *format.Error
	if !errors.As(result.Cause, &fe) || fe.Code != format.CodeTransactionAborted {
		t.Fatalf("cause = %v, want TransactionAborted class", result.Cause)
	}
	// Cancellation is not fatal: the writer stays healthy and a retry
	// commits (Rust live_roundtrip abort_and_noop_never_publish).
	if _, err := w.BaseInfo(); err != nil {
		t.Fatalf("writer not healthy after cancellation: %v", err)
	}
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect after cancellation: %v", err)
	}
	if changed, err := w.AssignV4(1, 5, 9); err != nil || !changed {
		t.Fatalf("assign retry: changed=%v err=%v", changed, err)
	}
	if result, err := w.Commit(nil); err != nil || result.Durability != CommitCommitted {
		t.Fatalf("retry durability = %v (err %v), want committed (cause %v)", result.Durability, err, result.Cause)
	}
}

// TestLiveDirectOpFailureAbortsDraft pins the Rust mutate abort_after
// contract on the direct transaction path: a failed mutation must
// discard the draft, so a later Commit can never publish the partial
// mutation the failed op left behind (the defect class already fixed
// for the structured/membership transactions; Rust DirectState ops
// route every store error through LiveWriter::mutate).
func TestLiveDirectOpFailureAbortsDraft(t *testing.T) {
	main := createLiveV4Pair(t, 2)
	// Zero heap budget: the range assign works (the IPv4 pending
	// locator is not heap-bounded), and the metadata compression heap
	// bound fails inside the store (Rust metadata.rs compress), so the
	// op fails mid-edit exactly like the Rust direct budget tests.
	budget := writer.PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100, MaxOpenFiles: 2}
	w, err := OpenLiveWriter(main, budget, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	if err := w.BeginDirect(); err != nil {
		t.Fatal(err)
	}
	if changed, err := w.AssignV4(10, 20, 5); err != nil || !changed {
		t.Fatalf("first assign: changed=%v err=%v", changed, err)
	}
	// The metadata compression heap charge fails mid-edit: the op must
	// abort the draft and report the TransactionAborted class (Rust
	// mutate Err(cause) => abort_after(cause)).
	if _, err := w.SetMetadata([]byte("x")); err == nil {
		t.Fatal("metadata stage succeeded under a zero heap budget")
	} else {
		expectCode(t, err, format.CodeTransactionAborted)
	}
	// The draft is gone: a commit finds nothing to publish.
	if _, err := w.Commit(nil); err == nil {
		t.Fatal("commit succeeded after the aborted op")
	} else {
		expectCode(t, err, format.CodeNoPendingTransaction)
	}
	// The failure class is not fatal (budget exhaustion): the writer
	// stays healthy and a fresh transaction commits normally.
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect after aborted op: %v", err)
	}
	if changed, err := w.AssignV4(30, 40, 7); err != nil || !changed {
		t.Fatalf("post-abort assign: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Durability != CommitCommitted {
		t.Fatalf("post-abort durability = %v, want committed", result.Durability)
	}
	// The failed op's partial mutation never published: the immutable
	// copy sees txn 2 with only the post-abort value, and the first
	// assign's value is absent because the whole draft was discarded.
	r, err := reader.OpenImmutable(copyMainForImmutableRead(t, main))
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	if r.Meta().TxnID != 2 {
		t.Fatalf("txn = %d, want 2", r.Meta().TxnID)
	}
	if got, ok, err := r.LookupDirect4(10); err != nil || ok || got != 0 {
		t.Fatalf("LookupDirect4(10) = (%d,%v,%v), want none after the aborted draft", got, ok, err)
	}
	if got, ok, err := r.LookupDirect4(35); err != nil || !ok || got != 7 {
		t.Fatalf("LookupDirect4(35) = (%d,%v,%v), want (7,true)", got, ok, err)
	}
}
