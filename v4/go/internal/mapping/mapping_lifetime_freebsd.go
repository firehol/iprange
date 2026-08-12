//go:build freebsd

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD has no proven OFD byte-range primitive, so live coordination is
// unsupported there (every live constructor rejects before path access).
// Immutable readers remain supported and use the canonical whole-file shared
// flock lifetime lock (binary-format-v4.md platform table; Rust live_lock.rs
// freebsd_file_lock), exactly like the Rust open_immutable path.

// lockLifetimeShared takes the shared whole-file flock lifetime lock on fd.
func lockLifetimeShared(fd int) error {
	for {
		if err := unix.Flock(fd, unix.LOCK_SH); err == nil {
			return nil
		} else if err != unix.EINTR {
			return &format.Error{Code: format.CodeIO, Detail: "lifetime lock: " + err.Error()}
		}
	}
}

// unlockLifetime releases the shared whole-file flock lifetime lock on fd.
func unlockLifetime(fd int) error {
	for {
		if err := unix.Flock(fd, unix.LOCK_UN); err == nil {
			return nil
		} else if err != unix.EINTR {
			return &format.Error{Code: format.CodeIO, Detail: "lifetime unlock: " + err.Error()}
		}
	}
}
