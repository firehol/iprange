//go:build freebsd

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD has no proven OFD byte-range primitive, so live coordination is
// unsupported there: every live constructor rejects before path access
// (requireLiveCoordination), and the exclusive lock is unreachable. Immutable
// readers remain supported and use the canonical whole-file shared flock
// lifetime lock (binary-format-v4.md platform table; Rust live_lock.rs
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

// requireLiveCoordination refuses every live open (writer and reader) on
// FreeBSD: the spec (binary-format-v4.md:2403-2411) and the Rust authority
// (live_lock.rs require_live_supported) return
// LiveCoordinationUnsupported before path access; whole-file flock must
// not be substituted for the absent OFD coordination.
func requireLiveCoordination() error {
	return &format.Error{Code: format.CodeLiveCoordinationUnsupported, Detail: "live coordination is not implemented on this platform"}
}

// lockLifetimeExclusive is unreachable on FreeBSD (requireLiveCoordination
// refuses first); it stays a typed refusal for defense in depth.
func lockLifetimeExclusive(fd int) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "live coordination is not implemented on this platform"}
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
