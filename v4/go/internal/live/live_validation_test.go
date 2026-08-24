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
