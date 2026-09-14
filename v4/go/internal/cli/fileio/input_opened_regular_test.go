//go:build unix

package fileio

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// POSIX FIFO arms of the caller-path input opens. Every caller-path
// input open must decide regularity on the descriptor it opened, not on
// the caller's earlier path stat: the open is O_NONBLOCK and returns
// errOpenedNotRegular for a node that is not a regular file. Calling the
// helper directly reproduces the swap-race state (the path check already
// passed) without racing, so these pins cover both halves: dropping
// O_NONBLOCK wedges the open on the fifo and trips the watchdog,
// dropping the descriptor check hands the caller a readable FIFO that
// the feed walk then consumes as empty input. The platform-neutral
// refusal-class pins are in opened_guard_test.go.
func TestOpenInputNoBlockRefusesOpenedFifo(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var err error
	runOpenedCall(t, "openInputNoBlock(fifo)", func() {
		var file *os.File
		file, err = openInputNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openInputNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
}

// The feed-input arm refuses a non-regular node with its own class and
// message, whether the node was already there (path check) or appeared
// at the open instant (descriptor check).
func TestOpenInputNonRegularArmClassMatchesOpenedCheck(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var fromPathCheck *os.File
	var ierr *InputError
	runOpenedCall(t, "openInput(standing fifo)", func() {
		fromPathCheck, ierr = openInput(path)
	})
	if fromPathCheck != nil {
		_ = fromPathCheck.Close()
	}
	if ierr == nil || ierr.kind != inputErrorInvalidPath {
		t.Fatalf("openInput(pre-placed fifo) = %+v, want invalid_path", ierr)
	}
	// Same descriptor, judged after the open: the arm refusal is the
	// one the caller maps errOpenedNotRegular to.
	var err error
	runOpenedCall(t, "openInputNoBlock(fifo)", func() {
		var file *os.File
		file, err = openInputNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openInputNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
	if !strings.Contains(ierr.message, "input is not a regular file: "+path) {
		t.Fatalf("openInput refusal = %q, want the arm's non-regular message", ierr.message)
	}
}

// The @file-list arm refuses a non-regular list with the class and
// message its path check uses, so a list swapped in at the open
// instant cannot be read as an empty list.
func TestExpandPathsAtListNonRegularArmClass(t *testing.T) {
	dir := t.TempDir()
	list := dir + "/list.fifo"
	if err := unix.Mkfifo(list, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var err error
	runOpenedCall(t, "expandPaths(@fifo)", func() {
		_, err = expandPaths([]string{"@" + list}, true, 100, 1<<20)
	})
	if err == nil {
		t.Fatal("expandPaths accepted a fifo file list")
	}
	input, ok := err.(*InputError)
	if !ok || input.kind != inputErrorInvalidPath {
		t.Fatalf("expandPaths(@fifo) = %+v, want invalid_path", err)
	}
	if !strings.Contains(input.message, "file list is not a regular file: "+list) {
		t.Fatalf("expandPaths refusal = %q, want the arm's non-regular message", input.message)
	}
}
