//go:build !linux && !darwin

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD, Windows, and every other platform have no proven Go
// implementation of the sidecar byte-range lock contract: FreeBSD lacks
// OFD locks (spec section 15 platform table), and the Windows live
// surface is a tracked SOW-0026 item. Every live constructor refuses before
// path access; these primitives stay typed refusals for defense in
// depth, exactly like the mapping owner's platform refusals.

func init() {
	lockSet = refuseSet
	lockUnlock = refuseUnlock
}

// liveRefusal is the single typed refusal for the whole live surface on
// platforms without a proven sidecar implementation.
func liveRefusal() error {
	return &format.Error{Code: format.CodeLiveCoordinationUnsupported, Detail: "live coordination is not implemented on this platform"}
}

func refuseSet(_ *os.File, _ uint64, _ LockMode, _ bool) (bool, error) {
	return false, liveRefusal()
}

func refuseUnlock(_ *os.File, _ uint64) error {
	return liveRefusal()
}

// requireLiveSupported refuses live coordination before any path access
// (Rust live_lock::require_live_supported; spec section 15 platform
// table). The Windows live surface is a tracked SOW-0026 item and refuses
// here exactly like the mapping owner refuses Windows opens.
func requireLiveSupported() error {
	return liveRefusal()
}
