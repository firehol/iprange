//go:build freebsd || netbsd

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD and NetBSD have no atomic name-exchange primitive in the
// pinned x/sys surface: the exchange policies refuse (Rust
// require_exchange_available), and rename-no-replace is unavailable
// too, so PolicyFailIfExists publications always refuse on these
// targets (failing closed: there is no overwrite race). The plain
// replacement and the retained-directory sync keep working. Builds are
// verified by the cross-compile matrix.

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func ExchangeAvailable() bool { return false }

// RenameNoReplace is unavailable without the atomic primitive; callers
// must use an isolated attempt name so no-replace semantics are
// preserved by construction.
func RenameNoReplace(oldpath, newpath string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_noreplace is not available on this target"}
}

// RenameExchange is unavailable without the atomic primitive (Rust
// require_exchange_available failing closed).
func RenameExchange(oldpath, newpath string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_exchange is not available on this target"}
}

// RenamePlain renames oldpath to newpath, replacing an existing
// destination (rename(2)): the no-rollback replacement path (Rust
// replace_discarding_destination, operation "atomically replace and
// discard publication destination").
func RenamePlain(oldpath, newpath string) error {
	err := unix.Rename(oldpath, newpath)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT):
			return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
			return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
		default:
			return &format.Error{Code: format.CodeIO, Detail: "atomically replace and discard publication destination"}
		}
	}
	return nil
}

// Unlink removes one attempt-file name (unlink(2); Rust unlink_exact,
// operation "unlink exact file").
func Unlink(path string) error {
	err := unix.Unlink(path)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "unlink exact file"}
	}
	return nil
}

// SyncDirectory forces the directory-entry changes into stable storage
// (fsync on the directory descriptor; Rust Directory::sync): EINVAL is
// the no-primitive family, everything else the retained-sync operation
// detail.
func SyncDirectory(dir string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "synchronize retained directory"}
	}
	defer unix.Close(fd)
	if err := unix.Fsync(fd); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
		}
		return &format.Error{Code: format.CodeIO, Detail: "synchronize retained directory"}
	}
	return nil
}

// StatIdentity returns the device+inode identity of path (Rust
// Identity{device, inode}; Rust NamespaceError::Io detail).
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return st.Dev, st.Ino, nil
}
