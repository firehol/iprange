//go:build !windows

package live

import "path/filepath"

// requireAvailable is a POSIX no-op: Windows GC custody verification
// only (Rust live_cleanup::require_available non-windows arm).
func requireAvailable(path string, expected FileIdentity, authority cleanupAuthority) error {
	_ = filepath.Clean(path)
	_ = expected
	_ = authority
	return nil
}
