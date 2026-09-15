//go:build (linux || darwin || freebsd) && (amd64 || arm64)

package worker

import (
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// spawnDescriptorDemand is the number of descriptors the spawn path must
// prove claimable before forking (design section 9.1), sized by the
// measurement in spawn_headroom_test.go rather than by assumption: the
// parent's caller-owned null descriptor is one, and os/exec's fork/exec
// handshake itself needs three more (the socketpair that carries the
// child's exec error, the descriptor duplicated into the child, and the
// pair's second end), so a spawn admitted with fewer free slots fails
// inside Start() with EMFILE after the control page already exists. The
// count comes from the descriptor-table read of the poller-readiness
// owner, never from an open of a caller-reachable path.
const spawnDescriptorDemand = 4

// spawnDescriptorWait bounds the headroom retry (design section 9.3).
// It is a named constant owned by the spawn and is deliberately far
// below startLimit, so the whole start path (headroom wait, spawn, and
// the handshake's own startLimit) stays inside the specification's 30 s
// worker-start bound.
const spawnDescriptorWait = 5 * time.Second

// spawnNullStdio opens the descriptor the worker child receives as its
// standard streams, replacing os/exec's nil-stdio path — which opens the
// null device by name inside the spawn, blocks forever on a planted
// FIFO, and (as an os.OpenFile on Linux) registers the descriptor with
// the runtime network poller.
//
// The open is calleropen (bare openat, O_NONBLOCK promptness, flag
// cleared before wrapping) so a FIFO answers immediately instead of
// waiting for a writer that never comes, and the descriptor identity
// check refuses any node that is not the system null character device
// before it can ever reach the child.
func spawnNullStdio() (*os.File, error) {
	file, err := calleropen.Open(os.DevNull, os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if !nullStdioIsDevice(file) {
		_ = file.Close()
		return nil, &os.PathError{Op: "identity", Path: os.DevNull, Err: os.ErrInvalid}
	}
	return file, nil
}

// nullDeviceIdentity is the expected {major, minor} of the null character
// device per platform (Linux and Darwin 1:3; FreeBSD 3:1). The check
// makes a planted node — FIFO, symlink to a live device, or an
// unexpected character device — refuse the spawn promptly instead of
// feeding the child a stream that never drains.
func nullDeviceIdentity() (major, minor uint32) {
	switch runtime.GOOS {
	case "freebsd":
		return 3, 1
	default: // linux, darwin
		return 1, 3
	}
}
