//go:build freebsd

package mapping

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD mirrors the Rust platform table: live coordination (the OFD
// byte-range lifetime lock) is not implemented there, so every open is
// refused with the typed Unsupported error. No content is mapped.

// lockLifetimeShared refuses the open, mirroring the Rust platform cfg.
func lockLifetimeShared(fd int) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "live coordination is not implemented on this platform"}
}

// unlockLifetime is unreachable: no lock is ever held.
func unlockLifetime(fd int) error { return nil }
