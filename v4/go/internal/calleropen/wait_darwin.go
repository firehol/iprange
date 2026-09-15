//go:build darwin

package calleropen

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// Sleep blocks for at least d without arming a runtime timer. macOS has
// no nanosleep binding in x/sys/unix; select(2) with no file descriptors
// sleeps for the requested timeout, costs no descriptor the poller would
// need, and never enters net. EINTR resumes against the same deadline so
// the guarantee matches time.Sleep.
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
		timespec := unix.NsecToTimeval(left.Nanoseconds())
		_, err := unix.Select(0, nil, nil, nil, &timespec)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return
		}
	}
}
