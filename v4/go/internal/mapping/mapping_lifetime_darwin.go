//go:build darwin

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// The lifetime lock is the shared OFD byte-range lock at offset 1<<44,
// mirroring the Rust MAIN_LIFETIME_LOCK (live_sidecar.rs, also F_OFD_SETLK
// on Apple platforms).
//
// macOS 10.15+ defines F_OFD_SETLK as 90 (F_OFD_SETLKW 91, F_OFD_GETLK 92);
// x/sys does not publish the constant for darwin, so it is declared here
// against the Apple header value and verified by the platform milestone.
const (
	fOfdSetLK          = 90
	fOfdSetLKW         = 91
	lifetimeLockOffset = 1 << 44
	lifetimeLockLen    = 1
)

// lockLifetimeShared takes the shared OFD byte-range lifetime lock on fd.
// The lock is blocking (F_OFD_SETLKW), mirroring Rust live_lock: an
// immutable open waits for a writer holding the exclusive lock.
func lockLifetimeShared(fd int) error {
	fl := unix.Flock_t{Type: unix.F_RDLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	if err := unix.FcntlFlock(uintptr(fd), fOfdSetLKW, &fl); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime lock: " + err.Error()}
	}
	return nil
}

// unlockLifetime releases the shared OFD byte-range lifetime lock on fd.
func unlockLifetime(fd int) error {
	fl := unix.Flock_t{Type: unix.F_UNLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	for {
		if err := unix.FcntlFlock(uintptr(fd), fOfdSetLK, &fl); err == nil {
			return nil
		} else if !errors.Is(err, unix.EINTR) {
			return &format.Error{Code: format.CodeIO, Detail: "lifetime unlock: " + err.Error()}
		}
	}
}
