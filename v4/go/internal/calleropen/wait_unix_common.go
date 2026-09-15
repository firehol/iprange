//go:build unix

package calleropen

import "time"

// waitPollStep is the poll granularity of WaitUntil. One millisecond
// matches the worker-control pollInterval already used across the
// product, so a bounded wait observes its predicate with the same
// latency the runtime-timer version had while arming no timer.
const waitPollStep = time.Millisecond

// WaitUntil polls ready every poll step up to d and reports whether
// ready became true first. It replaces
// `select { case <-event: case <-time.After(d): }` on paths that must
// stay free of runtime timers; ready must be a non-blocking observation
// (a receive from a closed or buffered channel in a select with a
// default).
func WaitUntil(d time.Duration, ready func() bool) bool {
	if d <= 0 {
		return ready()
	}
	deadline := time.Now().Add(d)
	for {
		if ready() {
			return true
		}
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		if left > waitPollStep {
			left = waitPollStep
		}
		Sleep(left)
	}
}
