//go:build linux

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Publication namespace primitives (Rust publication/namespace.rs +
// namespace_mutation.rs): the atomic name-exchange operations, the
// retained-directory sync, and the device+inode identity probe. They
// live in the mapping owner because the gate grants the syscall surface
// to exactly one package; the writer's staging layer composes them.

// exchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func exchangeAvailable() bool { return true }

// RenameNoReplace atomically renames oldpath to newpath only when
// newpath does not exist (renameat2 RENAME_NOREPLACE).
func RenameNoReplace(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "rename_noreplace: " + err.Error()}
	}
	return nil
}

// RenameExchange atomically exchanges oldpath and newpath (renameat2
// RENAME_EXCHANGE): the rollback-safe replacement path.
func RenameExchange(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCHANGE)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "rename_exchange: " + err.Error()}
	}
	return nil
}

// SyncDirectory forces the directory-entry changes into stable storage
// (fsync on the directory descriptor), mirroring Rust sync_all on the
// retained directory; EINVAL maps to the unsupported contract code.
func SyncDirectory(dir string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "open publication directory: " + err.Error()}
	}
	defer unix.Close(fd)
	if err := unix.Fsync(fd); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return &format.Error{Code: format.CodeOSUnsupported, Detail: "synchronize publication directory: " + err.Error()}
		}
		return &format.Error{Code: format.CodeIO, Detail: "synchronize publication directory: " + err.Error()}
	}
	return nil
}

// RenamePlain renames oldpath to newpath, replacing an existing
// destination (rename(2)): the no-rollback replacement path (Rust
// bind_no_rollback).
func RenamePlain(oldpath, newpath string) error {
	err := unix.Rename(oldpath, newpath)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "rename: " + err.Error()}
	}
	return nil
}

// Unlink removes one attempt-file name (unlink(2)); failures leave the
// attempt artifact behind, which the cleanup-state contract reports as
// residue.
func Unlink(path string) error {
	err := unix.Unlink(path)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "unlink publication attempt: " + err.Error()}
	}
	return nil
}

// StatIdentity returns the device+inode identity of path (Rust
// Identity{device, inode}), binding an attempt file to its destination
// namespace.
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "stat publication identity: " + err.Error()}
	}
	return st.Dev, st.Ino, nil
}
