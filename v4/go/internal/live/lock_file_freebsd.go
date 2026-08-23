//go:build freebsd

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD implements the artifact-level file locks with the
// open-file-table flock that covers the complete file (Rust live_lock
// freebsd arm; the portable sidecar byte-range surface refuses above
// because FreeBSD has no OFD locks). The offset argument is ignored:
// flock has no independent lock ranges, matching the Rust arm.

func init() {
	fileLockSet = setFileFlock
	fileLockUnlock = unlockFileFlock
}

func setFileFlock(f *os.File, _ uint64, mode LockMode, wait bool) (bool, error) {
	operation := int(unix.LOCK_SH)
	if mode == LockExclusive {
		operation = unix.LOCK_EX
	}
	if !wait {
		operation |= unix.LOCK_NB
	}
	for {
		if err := unix.Flock(int(f.Fd()), operation); err == nil {
			return true, nil
		} else if errors.Is(err, unix.EINTR) {
			continue
		} else if !wait && errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		} else {
			return false, &format.Error{Code: format.CodeIO, Detail: "file flock: " + err.Error()}
		}
	}
}

func unlockFileFlock(f *os.File, _ uint64) error {
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err == nil {
			return nil
		} else if errors.Is(err, unix.EINTR) {
			continue
		} else {
			return &format.Error{Code: format.CodeIO, Detail: "file flock unlock: " + err.Error()}
		}
	}
}
