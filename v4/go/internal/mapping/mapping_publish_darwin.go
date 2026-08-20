//go:build darwin

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Darwin implements the publication namespace through renameatx_np
// (RENAME_EXCL / RENAME_SWAP), the atomic no-replace and exchange
// primitives of the Apple namespace.

// exchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func exchangeAvailable() bool { return true }

// RenameNoReplace atomically renames oldpath to newpath only when
// newpath does not exist (renameatx_np RENAME_EXCL).
func RenameNoReplace(oldpath, newpath string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCL)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "rename_noreplace: " + err.Error()}
	}
	return nil
}

// RenameExchange atomically exchanges oldpath and newpath (renameatx_np
// RENAME_SWAP): the rollback-safe replacement path.
func RenameExchange(oldpath, newpath string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_SWAP)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "rename_exchange: " + err.Error()}
	}
	return nil
}

// SyncDirectory forces the directory-entry changes into stable storage
// (fsync on the directory descriptor; F_FULLFSYNC is a regular-file
// primitive). EINVAL maps to the unsupported contract code.
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

// StatIdentity returns the device+inode identity of path (Rust
// Identity{device, inode}); the Apple device number is a 32-bit value.
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "stat publication identity: " + err.Error()}
	}
	return uint64(st.Dev), st.Ino, nil
}
