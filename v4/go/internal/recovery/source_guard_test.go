package recovery

// Source-guard tests: the immutable and quiescent candidate opens,
// the current-generation open, the final proof over the retained
// selection, the open refusals, and the retryable cleanup guard. The
// registered live arm arrives with the recover_live machine.

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// guardTestDB writes a two-page valid meta pair and returns its path.
func guardTestDB(t *testing.T) string {
	t.Helper()
	return metaDBFile(t, 2)
}

func TestBasicSourceCandidateOpenAndFinish(t *testing.T) {
	path := guardTestDB(t)
	inspection, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	candidate := inspection.Candidate(0)
	source, failure := openRecoverySource(path, candidate, sourceModeImmutable, nil)
	if failure != nil {
		t.Fatalf("open: %v", failure.cause)
	}
	if source.meta().TxnID != candidate.TransactionID || source.meta().TxnID != 1 {
		t.Fatalf("meta %+v", source.meta())
	}
	if source.identity() != inspection.SourceIdentity {
		t.Fatal("source identity differs from the inspected identity")
	}
	// The final proof passes for the retained generation and the
	// release completes cleanly.
	end := source.finish(source.meta(), nil)
	if end.cause != nil || end.guard != nil {
		t.Fatalf("finish end %+v %+v", end.cause, end.guard)
	}
}

func TestBasicSourceFinalCheckRejectsChangedGeneration(t *testing.T) {
	path := guardTestDB(t)
	inspection, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	source, failure := openRecoverySource(path, inspection.Candidate(0), sourceModeImmutable, nil)
	if failure != nil {
		t.Fatalf("open: %v", failure.cause)
	}
	// A used generation different from the retained one is the
	// candidate-changed class.
	used := source.meta()
	used.TxnID++
	end := source.finish(used, nil)
	if end.cause == nil || end.guard != nil {
		t.Fatalf("finish end %+v", end)
	}
	var fe *format.Error
	if !errors.As(end.cause, &fe) || fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause %v, want RecoveryCandidateChanged", end.cause)
	}
}

func TestOfflineSourceCandidateOpen(t *testing.T) {
	path := guardTestDB(t)
	// The quiescent arm opens the same pair read-write under the
	// exclusive lifetime lock.
	inspection, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	source, failure := openRecoverySource(path, inspection.Candidate(0), sourceModeOffline, nil)
	if failure != nil {
		t.Fatalf("open: %v", failure.cause)
	}
	end := source.finishCurrent(nil)
	if end.cause != nil || end.guard != nil {
		t.Fatalf("finish end %+v %+v", end.cause, end.guard)
	}
}

func TestBasicSourceCurrentOpen(t *testing.T) {
	path := guardTestDB(t)
	source, failure := openRecoverySourceCurrent(path, currentSourceModeImmutable, nil)
	if failure != nil {
		t.Fatalf("open current: %v", failure.cause)
	}
	if source.meta().TxnID != 1 {
		t.Fatalf("meta %+v, want txn 1", source.meta())
	}
	end := source.finishCurrent(nil)
	if end.cause != nil || end.guard != nil {
		t.Fatalf("finish end %+v %+v", end.cause, end.guard)
	}
}

func TestBasicSourceCurrentGeometryRefusal(t *testing.T) {
	// A one-page main is refused before any mapping exists (Rust
	// require_geometry inside bootstrap_file).
	path := metaDBFile(t, 1)
	_, failure := openRecoverySourceCurrent(path, currentSourceModeImmutable, nil)
	if failure == nil {
		t.Fatal("short main accepted")
	}
	var fe *format.Error
	if !errors.As(failure.cause, &fe) || fe.Code != format.CodeFormatInvalid {
		t.Fatalf("cause %v, want FormatInvalid", failure.cause)
	}
}

func TestSourceOpenRefusals(t *testing.T) {
	path := guardTestDB(t)
	// The live arm refuses honestly until the recover_live machine.
	if _, failure := openRecoverySource(path, &RecoveryCandidate{}, sourceModeLive, nil); failure == nil {
		t.Fatal("live source accepted before the recover_live machine")
	}
	// A bogus candidate on a valid pair refuses as the changed class.
	_, failure := openRecoverySource(path, &RecoveryCandidate{MetaPage: 0, Label: CandidateUnorderedMeta0}, sourceModeImmutable, nil)
	if failure == nil {
		t.Fatal("bogus candidate accepted")
	}
	var fe *format.Error
	if !errors.As(failure.cause, &fe) || fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause %v, want RecoveryCandidateChanged", failure.cause)
	}
	// Cancellation preflights before any path access.
	cancelled := &format.Error{Code: format.CodeCancelled, Detail: "cancelled"}
	if _, failure := openRecoverySource(path, &RecoveryCandidate{}, sourceModeImmutable, func() error { return cancelled }); failure == nil {
		t.Fatal("cancelled open accepted")
	}
}

func TestRecoverySourceCleanupGuardRetry(t *testing.T) {
	path := guardTestDB(t)
	inspection, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	source, failure := openRecoverySource(path, inspection.Candidate(0), sourceModeImmutable, nil)
	if failure != nil {
		t.Fatalf("open: %v", failure.cause)
	}
	// The guard releases the retained source on the first retry.
	guard := &RecoverySourceCleanupGuard{source: &guardSource{kind: guardSourceRecovery, recovery: source}}
	if !guard.CleanupPending() {
		t.Fatal("cleanup not pending")
	}
	done, err := guard.RetryCleanup()
	if err != nil || !done {
		t.Fatalf("retry: done=%v err=%v", done, err)
	}
	if guard.CleanupPending() {
		t.Fatal("cleanup still pending")
	}
	// A second retry reports nothing left.
	done, err = guard.RetryCleanup()
	if err != nil || done {
		t.Fatalf("second retry: done=%v err=%v", done, err)
	}
}

func TestBasicSourceSequentialLockModes(t *testing.T) {
	path := guardTestDB(t)
	inspection, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// The exclusive quiescent open and the shared immutable open both
	// succeed when used sequentially, pinning the lifetime lock mode
	// selection of each arm.
	first, failure := openRecoverySource(path, inspection.Candidate(0), sourceModeOffline, nil)
	if failure != nil {
		t.Fatalf("first open: %v", failure.cause)
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	// The release only unlocks; the retained source closes exactly
	// where the machine terminal would (Rust drop), so the mapped
	// source file stays deletable on Windows.
	first.close()
	second, failure := openRecoverySource(path, inspection.Candidate(0), sourceModeImmutable, nil)
	if failure != nil {
		t.Fatalf("second open: %v", failure.cause)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
	second.close()
}
