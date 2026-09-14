//go:build linux

// Local-filesystem durability proof and name_max (Rust
// namespace/unix.rs require_local_filesystem + fpathconf). Linux
// whitelists the filesystems whose durability semantics the live
// lifecycle requires; glibc fpathconf(_PC_NAME_MAX) resolves to the
// statfs f_namelen field, so the statfs Namelen is the exact value.

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/fslocal"
)

// requireLocalFilesystem refuses filesystems outside the durability
// whitelist (Rust require_local_filesystem Unsupported). The whitelist
// itself is owned by internal/fslocal so every directory bind asks the
// same question.
func requireLocalFilesystem(f *os.File) error {
	if err := fslocal.RequireLocal(f); errors.Is(err, fslocal.ErrNotLocal) {
		return nsUnsupportedError()
	} else if err != nil {
		return nsIoError("inspect publication filesystem", err)
	}
	return nil
}

// directoryNameMax reports the directory name_max (Rust fpathconf
// _PC_NAME_MAX; glibc resolves it from statfs f_namelen).
func directoryNameMax(f *os.File) (int, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	return int(st.Namelen), nil
}

// atNofollow is the fstatat AT_SYMLINK_NOFOLLOW flag (linux value).
const atNofollow = unix.AT_SYMLINK_NOFOLLOW
