//go:build !linux && !darwin && !freebsd

package live

import (
	"os"
)

// Windows and every remaining platform refuse the artifact-level file
// locks with the same typed refusal as the byte-range surface (Rust
// live_lock non-unix arm). The Windows publication surface is a tracked
// SOW-0026 item and refuses here before any path access.

func init() {
	fileLockSet = refuseFileSet
	fileLockUnlock = refuseFileUnlock
}

func refuseFileSet(_ *os.File, _ uint64, _ LockMode, _ bool) (bool, error) {
	return false, liveRefusal()
}

func refuseFileUnlock(_ *os.File, _ uint64) error {
	return liveRefusal()
}
