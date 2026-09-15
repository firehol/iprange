//go:build linux || freebsd

package calleropen

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// Sleep blocks for at least d without arming a runtime timer.
//
// Every runtime timer arm initializes the network poller
// (runtime/time.go: (*timers).addHeap -> netpollGenericInit), and that
// initialization has no failure path under a low RLIMIT_NOFILE (see the
// package comment). nanosleep(2) costs neither a descriptor nor a timer,
// so the session, worker, lock and resolver waits use it instead of
// time.Sleep. Signals that return EINTR resume the remainder, matching
// time.Sleep's guarantee that the call returns only once d has elapsed.
func Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		request := unix.NsecToTimespec(left.Nanoseconds())
		remaining := &unix.Timespec{}
		if err := unix.Nanosleep(&request, remaining); errors.Is(err, unix.EINTR) {
			continue
		}
		// A failed nanosleep with no observable remainder is treated
		// as an elapsed wait: like time.Sleep it must never spin or
		// hang on an error the caller cannot observe.
		return
	}
}
