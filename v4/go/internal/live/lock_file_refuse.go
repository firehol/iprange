//go:build !linux && !darwin && !freebsd && !windows

package live

import (
	"os"
)

// Every platform without an artifact-level lock machine refuses here
// with the same typed refusal as the byte-range surface (Rust live_lock
// non-unix arm). Windows implements the real byte-range machine in
// lock_windows.go (Rust lock_file uses LockFileEx there); the remaining
// platforms refuse before any path access.

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
