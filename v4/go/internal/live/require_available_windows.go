//go:build windows

package live

import "path/filepath"

// requireAvailable proves one retained source is not owned by Windows
// housekeeping (Rust live_cleanup::require_available windows arm over
// publication::gc::require_source_available): the exact envelope name
// of the authority is checked without scanning the directory, and a
// matching selected envelope means cleanup owns the inode.
func requireAvailable(path string, expected FileIdentity, authority cleanupAuthority) error {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return gcNamespaceProblem(err)
	}
	defer dir.Close()
	return gcRequireSourceAvailable(dir, authority.attemptID, authority.ordinal, authority.kind, authority.directoryRole, name, expected)
}
