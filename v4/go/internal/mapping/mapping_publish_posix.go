//go:build freebsd || netbsd

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// FreeBSD and NetBSD have no atomic name-exchange primitive in the
// pinned x/sys surface: the exchange policies refuse (Rust
// require_exchange_available), while the no-replace pre-check and the
// directory sync keep working. Builds are verified by the per-target
// gate scan.

// exchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func exchangeAvailable() bool { return false }

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

// SyncDirectory forces the directory-entry changes into stable storage
// (fsync on the directory descriptor). EINVAL maps to the unsupported
// contract code.
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
// Identity{device, inode}).
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "stat publication identity: " + err.Error()}
	}
	return st.Dev, st.Ino, nil
}
