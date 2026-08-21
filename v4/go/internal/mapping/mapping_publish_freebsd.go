//go:build freebsd

package mapping

import "path/filepath"

// RenameNoReplace publishes oldpath as newpath without replacing any
// existing name (Rust Directory::rename_noreplace on FreeBSD: the
// crash-safe linkat machine of namespace_mutation.rs). expectedDevice/
// expectedInode is the attempt identity captured at creation; the
// machine proves every probed name still names that inode before,
// during, and after the link transition.
func RenameNoReplace(oldpath, newpath string, expectedDevice, expectedInode uint64) error {
	if err := linkNoReplace(filepath.Dir(newpath), oldpath, newpath, expectedDevice, expectedInode); err != nil {
		return err
	}
	return nil
}
