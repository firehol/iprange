//go:build !linux && !darwin && !windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD and every other platform without a proven sidecar lock
// machine refuse here: FreeBSD lacks OFD byte-range locks (spec
// section 15 platform table) and has no equivalent primitive; Windows
// implements the real LockFileEx machine in lock_windows.go. Every
// live constructor refuses before path access on the remaining
// platforms; these primitives stay typed refusals for defense in
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
// table). Windows implements the machine and returns nil from
// lock_windows.go; the remaining platforms refuse here exactly like
// the mapping owner refuses their opens.
func requireLiveSupported() error {
	return liveRefusal()
}
