//go:build !unix

package calleropen

import "time"

// Sleep is the platform fallback for the non-unix family (Windows,
// plan9, js and wasip1). Windows carries no fd-table poller hazard (the
// I/O completion port allocates no descriptor and no eventfd), so the
// runtime timer path is safe there and time.Sleep keeps the sharpest
// wake-up available. Every unix GOOS is served by wait_nanosleep.go or
// wait_darwin.go instead: see the partition note in wait_unix_common.go.
func Sleep(d time.Duration) { time.Sleep(d) }

// WaitUntil is the platform fallback (see Sleep).
func WaitUntil(d time.Duration, ready func() bool) bool {
	if d <= 0 {
		return ready()
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		if ready() {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-time.After(waitPollStepWindows):
		}
	}
}

// waitPollStepWindows keeps the poll granularity identical to the unix
// owner so a wake-up is observed with the same latency on every platform.
const waitPollStepWindows = 1 * time.Millisecond
