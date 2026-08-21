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
// live in the mapping owner because the syscall surface is confined to
// exactly one package; the writer's staging layer composes them.
// Errno classification and detail strings mirror Rust exactly
// (rename_result + problem.rs).

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func ExchangeAvailable() bool { return true }

// renameErr classifies one rename errno like Rust rename_result plus
// the problem mapping: the conflict errno maps to the name-exists or
// name-missing contract code, the no-primitive family maps to
// DurabilityUnsupported, and every other failure is the operation's
// CodeIo detail (Rust IoAt detail = the operation string).
func renameErr(err error, existsErr, missingErr error, operation string) error {
	switch {
	case existsErr != nil && errors.Is(err, existsErr):
		return &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
	case missingErr != nil && errors.Is(err, missingErr):
		return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
	case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
	default:
		return &format.Error{Code: format.CodeIO, Detail: operation}
	}
}

// RenameNoReplace atomically renames oldpath to newpath only when
// newpath does not exist (renameat2 RENAME_NOREPLACE; Rust
// Directory::rename_noreplace, operation "publish name without
// replacement").
func RenameNoReplace(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if err != nil {
		return renameErr(err, unix.EEXIST, nil, "publish name without replacement")
	}
	return nil
}

// RenameExchange atomically exchanges oldpath and newpath (renameat2
// RENAME_EXCHANGE): the rollback-safe replacement path (Rust
// Directory::exchange, operation "atomically exchange publication
// names").
func RenameExchange(oldpath, newpath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCHANGE)
	if err != nil {
		return renameErr(err, nil, unix.ENOENT, "atomically exchange publication names")
	}
	return nil
}

// RenamePlain renames oldpath to newpath, replacing an existing
// destination (rename(2)): the no-rollback replacement path (Rust
// replace_discarding_destination, operation "atomically replace and
// discard publication destination").
func RenamePlain(oldpath, newpath string) error {
	err := unix.Rename(oldpath, newpath)
	if err != nil {
		return renameErr(err, nil, unix.ENOENT, "atomically replace and discard publication destination")
	}
	return nil
}

// Unlink removes one attempt-file name (unlink(2); Rust unlink_exact,
// operation "unlink exact file"). Failures leave the attempt artifact
// behind, which the cleanup-state contract reports as residue.
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
// Identity{device, inode}), binding an attempt file to its destination
// namespace (Rust NamespaceError::Io detail).
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return st.Dev, st.Ino, nil
}
