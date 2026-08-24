package live

// Unit tests of the live validation source machine (Rust
// validation/source.rs terminal folds). The residual-release fold is
// not reachable through one Validate call without a pre-terminal hook,
// so the terminal shape is pinned here directly.

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestLiveTerminalResultResidue pins the terminal fold of the
// bootstrap release machine (Rust validation/source.rs terminal): a
// failed release keeps the check failure (or the cleanup class when
// the check was clean) and reports the residue possible.
func TestLiveTerminalResultResidue(t *testing.T) {
	clean := terminalResult(nil, nil)
	if clean.Cause != nil || clean.Residue {
		t.Fatalf("clean terminal %+v", clean)
	}
	checkErr := errors.New("operation failed")
	checked := terminalResult(checkErr, nil)
	if checked.Cause != checkErr || checked.Residue {
		t.Fatalf("checked terminal %+v", checked)
	}
	released := terminalResult(nil, errors.New("release failed"))
	var fe *format.Error
	if !errors.As(released.Cause, &fe) || fe.Code != format.CodeCleanupConflict {
		t.Fatalf("cleanup terminal cause %v", released.Cause)
	}
	if !released.Residue {
		t.Fatal("failed release must report residue")
	}
	both := terminalResult(checkErr, errors.New("release failed"))
	if both.Cause != checkErr || !both.Residue {
		t.Fatalf("both terminal %+v", both)
	}
}

// TestOpenFailureCarriesClaimedUnwindResidue pins the claimed-open
// unwind terminal (Rust LiveOpenFailure::Claimed -> SourceOpenFailure
// with the abandon guard): when the abandon release fails, the typed
// open failure retains the half-released source and the raw release
// problem so the recovery machine can build its retryable cleanup
// guard.
func TestOpenFailureCarriesClaimedUnwindResidue(t *testing.T) {
	source := &LiveSource{ownerPID: currentPID}
	saved := currentPID
	currentPID = saved + 1
	defer func() { currentPID = saved }()
	primary := errors.New("primary open failure")
	_, err := source.releaseUnclaimed(primary)
	currentPID = saved
	failure, ok := err.(*OpenFailure)
	if !ok {
		t.Fatalf("error %v, want *OpenFailure", err)
	}
	if failure.Cause != primary || !failure.Residue {
		t.Fatalf("failure %+v, want the primary cause with residue", failure)
	}
	if failure.Retained != source {
		t.Fatal("residue must retain the half-released source")
	}
	var fe *format.Error
	if !errors.As(failure.Released, &fe) || fe.Code != format.CodeForkedHandle {
		t.Fatalf("released %v, want the require-owner release failure", failure.Released)
	}
}
