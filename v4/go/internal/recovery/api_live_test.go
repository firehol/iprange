//go:build linux || darwin

package recovery

// Live recovery api tests: the happy recover_live over a committed
// live database and the newest-only candidate refusal. The live writer
// and the source coordination require the proven live platforms, so
// the file carries the same linux || darwin tag as the live writer
// suite.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// apiLiveTestBudget is the live recovery budget (the live mode
// reserves one coordination file on top of the source and output).
func apiLiveTestBudget() *RecoveryBudget {
	return HeapOnly(1024*1024, 100, 3)
}

// TestOpenProblemLiveRetainsClaimedUnwindGuard proves the
// claimed-open unwind fold (Rust finish_open Claimed ->
// SourceOpenFailure.guard): a live open error with residue builds the
// retryable cleanup guard over the retained half-released source, and
// the retried cleanup completes the release.
func TestOpenProblemLiveRetainsClaimedUnwindGuard(t *testing.T) {
	liveGate(t)
	main := createLiveRecoveryPair(t)
	inspection, err := inspect(t, main, RecoveryInspectionLive)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil || candidate.Label != CandidateNewest {
		t.Fatalf("candidate %+v, want newest", candidate)
	}
	token, ok := candidateLiveToken(candidate)
	if !ok {
		t.Fatal("candidate token")
	}
	source, err := live.OpenLiveSourceCandidate(main, token, nil)
	if err != nil {
		t.Fatalf("OpenLiveSourceCandidate: %v", err)
	}
	defer func() {
		_ = source.Release()
	}()
	failure := openProblemLive(&live.OpenFailure{
		Cause:    &format.Error{Code: format.CodeLiveRecoveryCoordinationUnavailable, Detail: "synthetic live open failure"},
		Residue:  true,
		Retained: source,
		Released: &format.Error{Code: format.CodeCleanupConflict, Detail: "synthetic release failure"},
	})
	if failure.cause == nil || failure.guard == nil {
		t.Fatalf("failure %+v, want the primary cause with a cleanup guard", failure)
	}
	if !failure.guard.CleanupPending() {
		t.Fatal("guard must retain the source")
	}
	if problemCode(failure.guard.LastProblem()) != format.CodeCleanupConflict {
		t.Fatalf("last problem %v, want the release problem", failure.guard.LastProblem())
	}
	done, retryErr := failure.guard.RetryCleanup()
	if retryErr != nil || !done {
		t.Fatalf("retry cleanup = %v/%v, want done", done, retryErr)
	}
	if failure.guard.CleanupPending() {
		t.Fatal("guard must empty after the completed retry")
	}
}

// TestRecoverLivePublishesTheNewestCandidate constructs one committed
// live pair and recovers the newest candidate into a fresh published
// output with the empty direct ranges preserved.
func TestRecoverLivePublishesTheNewestCandidate(t *testing.T) {
	liveGate(t)
	main := createLiveRecoveryPair(t)
	output := filepath.Join(filepath.Dir(main), "output.v4")
	inspection, err := inspect(t, main, RecoveryInspectionLive)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil || candidate.Label != CandidateNewest {
		t.Fatalf("candidate %+v, want newest", candidate)
	}

	result, failure := RecoverLive(main, candidate, output, apiLiveTestBudget(), nil, nil)
	if failure != nil {
		t.Fatalf("recover live: %v", failure)
	}
	if result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("publication %v, want published (cause %v)", result.Publication.Publication, result.Publication.Cause)
	}
	r, err := reader.OpenImmutable(output)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	if meta := r.Meta(); meta.PageCount != 2 || meta.RangeRecordCount != 0 {
		t.Fatalf("meta %+v, want 2 pages 0 ranges", meta)
	}
}

// TestRecoverLiveRefusesANonNewestCandidate proves the live arm
// refuses every candidate that is not the proven newest before any
// path access, with the attempt removed and no private residue.
func TestRecoverLiveRefusesANonNewestCandidate(t *testing.T) {
	liveGate(t)
	main := createLiveRecoveryPair(t)
	output := filepath.Join(filepath.Dir(main), "output.v4")
	inspection, err := inspect(t, main, RecoveryInspectionLive)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no live candidate")
	}
	candidate.Label = CandidatePrevious

	_, failure := RecoverLive(main, candidate, output, apiLiveTestBudget(), nil, nil)
	if failure == nil {
		t.Fatal("recover live accepted a non-newest candidate")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeInvalidArgument {
		t.Fatalf("cause %v, want InvalidArgument", failure.Cause)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output still present after the refusal: %v", err)
	}
	assertNoPrivateNames(t, filepath.Dir(main))
}
