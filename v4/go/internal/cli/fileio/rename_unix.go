//go:build !windows

package fileio

import "golang.org/x/sys/unix"

// RenameReplace publishes one completed temporary over its destination
// with the raw rename(2) so the failure errno is the classification
// input, exactly like Rust fs::rename.
//
// os.Rename must not be used for a published destination: it stats the
// destination first and turns "the destination name is a directory"
// into os.ErrExist, which the export family would report as
// name_exists — a claim that a comparable artifact already occupies the
// name. The raw syscall keeps EISDIR (and ENOTDIR) distinct, so a
// destination directory is refused with io as Rust refuses it.
// os.ErrExist still matches the genuine name-exists errnos EEXIST and
// ENOTEMPTY, so the fail-if-exists hard-link arm is unchanged.
func RenameReplace(oldPath, newPath string) error {
	return unix.Rename(oldPath, newPath)
}
