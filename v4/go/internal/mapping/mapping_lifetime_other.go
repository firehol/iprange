//go:build !linux && !darwin && !freebsd && !windows

package mapping

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Other unix platforms mirror the Rust platform table: live coordination
// (the OFD byte-range lifetime lock) is not implemented there, so every
// open is refused with the typed Unsupported error. No content is mapped.

// requireLiveWriter refuses live writer opens, mirroring the Rust platform
// cfg (require_live_supported).
func requireLiveWriter() error {
	return &format.Error{Code: format.CodeLiveCoordinationUnsupported, Detail: "live coordination is not implemented on this platform"}
}

// lockLifetimeShared refuses the open, mirroring the Rust platform cfg.
func lockLifetimeShared(fd int) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "live coordination is not implemented on this platform"}
}

// lockLifetimeExclusive refuses the open, mirroring the Rust platform cfg.
func lockLifetimeExclusive(fd int) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "live coordination is not implemented on this platform"}
}

// unlockLifetime is unreachable: no lock is ever held.
func unlockLifetime(fd int) error { return nil }
