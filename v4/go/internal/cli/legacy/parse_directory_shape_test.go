// Direct shape test for the empty-set admission rule (isDirectoryReadError).
//
// The end-to-end denied-open test covers the platform path where the open
// itself fails; it cannot distinguish the two halves of the rule on a host
// where the refused open arrives with a different errno than the read
// failure. This test pins the rule itself: an error carrying the platform's
// directory-read errno but Op "open" is NOT the empty-set case, because a
// "read" op is what proves the open succeeded. A mutation that drops the
// Op check must turn this test red on every platform.
package legacy

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDirectoryReadAdmissionRequiresReadOpNotJustErrno(t *testing.T) {
	dir := t.TempDir()

	// The read-shaped error: the platform's directory-read errno under an
	// Op "read" on a real directory. This is the one admitted shape.
	readShaped := &os.PathError{Op: "read", Path: dir, Err: directoryReadErrno}
	if !isDirectoryReadError(dir, readShaped) {
		t.Fatalf("the admitted shape (Op read + %v + existing directory) was rejected",
			directoryReadErrno)
	}

	// The same errno under Op "open" on the SAME directory: a refused open
	// wearing the directory code must never be made empty.
	openShaped := &os.PathError{Op: "open", Path: dir, Err: directoryReadErrno}
	if isDirectoryReadError(dir, openShaped) {
		t.Fatal("an Op \"open\" failure was admitted as the directory read case; " +
			"a refused open must keep failing, not become the empty set")
	}

	// Not a PathError at all: rejected without consulting the path.
	if isDirectoryReadError(dir, errors.New("bare")) {
		t.Fatal("a bare error was admitted")
	}

	// The right shape pointing at something that is not a directory: the
	// attribute half must refuse it (narrow-only rule).
	regular := filepath.Join(dir, "f")
	if err := os.WriteFile(regular, []byte("192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isDirectoryReadError(regular, &os.PathError{Op: "read", Path: regular, Err: directoryReadErrno}) {
		t.Fatal("a read failure on a regular file was admitted as the directory case")
	}

	// Right shape, vanished path: the attribute check cannot confirm a
	// directory, so the error stays an error.
	gone := filepath.Join(dir, "gone")
	if isDirectoryReadError(gone, &os.PathError{Op: "read", Path: gone, Err: directoryReadErrno}) {
		t.Fatal("a read failure naming a missing path was admitted as the directory case")
	}

	// A wrong errno under the right op on a directory: only the platform's
	// own directory-read code is admitted. ESRCH is chosen because it is
	// neither the unix constant (EISDIR) nor the Windows one
	// (ERROR_INVALID_FUNCTION = 1).
	if directoryReadErrno == syscall.ESRCH {
		t.Fatal("the platform directory-read code is ESRCH; pick another wrong code here")
	}
	if isDirectoryReadError(dir, &os.PathError{Op: "read", Path: dir, Err: syscall.ESRCH}) {
		t.Fatal("ESRCH under Op read was admitted as the directory case")
	}
}
