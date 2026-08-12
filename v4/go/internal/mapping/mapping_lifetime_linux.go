//go:build linux

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// The lifetime lock is the shared byte-range OFD lock at offset 1<<44,
// mirroring the Rust MAIN_LIFETIME_LOCK (live_sidecar.rs): every reader
// holds it shared from open to close, and every writer must hold it
// exclusively to overwrite the main file, so a mapped reader and a
// truncating writer can never overlap.
const (
	lifetimeLockOffset = 1 << 44
	lifetimeLockLen    = 1
)

// lockLifetimeShared takes the shared OFD byte-range lifetime lock on fd.
func lockLifetimeShared(fd int) error {
	fl := unix.Flock_t{Type: unix.F_RDLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	if err := unix.FcntlFlock(uintptr(fd), unix.F_OFD_SETLK, &fl); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime lock: " + err.Error()}
	}
	return nil
}

// unlockLifetime releases the shared OFD byte-range lifetime lock on fd.
func unlockLifetime(fd int) error {
	fl := unix.Flock_t{Type: unix.F_UNLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	if err := unix.FcntlFlock(uintptr(fd), unix.F_OFD_SETLK, &fl); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime unlock: " + err.Error()}
	}
	return nil
}
