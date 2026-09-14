package handlers

import (
	"testing"
	"time"
)

// openedCallDeadline bounds one call into an input owner that decides
// regularity on the descriptor it opened.
const openedCallDeadline = 5 * time.Second

// runOpenedCall runs one call into an input owner and fails the test if
// it does not answer. The owners are required to open their source with
// O_NONBLOCK (Rust database_file::open_read_only, callers), because a
// blocking read-only open of a FIFO waits for a writer that never
// comes. A dropped flag therefore has to surface as a failed test inside
// the deadline; without this guard the suite waits for the go test
// timeout instead and reports nothing about the owner that lost the
// flag. work writes its results into captured locals, which the caller
// asserts on after the call has answered.
//
// The goroutine outlives a failed assertion, which is the point: the
// blocked open belongs to the production owner under test and the test
// binary exits regardless.
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
