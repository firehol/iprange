package fileio

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// openedCallDeadline bounds one call into an input owner that decides
// regularity on the descriptor it opened.
const openedCallDeadline = 5 * time.Second

// runOpenedCall runs one call into an input owner and fails the test if
// it does not answer. The owners are required to open their source with
// O_NONBLOCK, because a blocking read-only open of a FIFO waits for a
// writer that never comes. A dropped flag therefore has to surface as a
// failed test inside the deadline instead of leaving the suite waiting
// for the go test timeout. work writes its results into captured locals,
// which the caller asserts on after the call has answered.
func runOpenedCall(t *testing.T, label string, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		work()
	}()
	select {
	case <-done:
	case <-time.After(openedCallDeadline):
		t.Fatalf("%s did not answer within %s: the owner blocked, which means the no-follow open lost O_NONBLOCK",
			label, openedCallDeadline)
	}
}

// Platform-neutral class mapping of the caller-path input opens. A
// directory is a non-regular node on every OS, so the feed-input
// refusal classes are pinned without mkfifo and without any POSIX-only
// call; the FIFO arms of the same owners, including the @file-list arm
// (which needs a node that is neither a regular file nor a directory),
// are in input_opened_regular_test.go.

// The feed-input arm refuses a non-regular node with its own class and
// message, whether the node was already there (path check) or appeared
// at the open instant (descriptor check).
func TestOpenInputNonRegularClassMappingWithoutFifo(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/subdir"
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	fromPathCheck, ierr := openInput(path)
	if fromPathCheck != nil {
		_ = fromPathCheck.Close()
	}
	if ierr == nil || ierr.kind != inputErrorInvalidPath {
		t.Fatalf("openInput(directory) = %+v, want invalid_path", ierr)
	}
	if !strings.Contains(ierr.message, "input is not a regular file: "+path) {
		t.Fatalf("openInput refusal = %q, want the arm's non-regular message", ierr.message)
	}
	// The same node judged on the opened descriptor maps to the arm's
	// refusal, which is what the callers of the no-block open surface.
	var err error
	runOpenedCall(t, "openInputNoBlock(directory)", func() {
		var file *os.File
		file, err = openInputNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if err == nil || !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openInputNoBlock(directory) = %v, want %v", err, errOpenedNotRegular)
	}
}
