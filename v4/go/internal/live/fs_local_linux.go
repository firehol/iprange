//go:build linux

// Local-filesystem durability proof and name_max (Rust
// namespace/unix.rs require_local_filesystem + fpathconf). Linux
// whitelists the filesystems whose durability semantics the live
// lifecycle requires; glibc fpathconf(_PC_NAME_MAX) resolves to the
// statfs f_namelen field, so the statfs Namelen is the exact value.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// localFilesystemWhitelist is the Linux f_type whitelist (Rust
// require_local_filesystem EXT/XFS/BTRFS/F2FS/ZFS/BCACHEFS).
const (
	fsExt    = 0x0000_ef53
	fsXFS    = 0x5846_5342
	fsBtrfs  = 0x9123_683e
	fsF2fs   = 0xf2f5_2010
	fsZFS    = 0x2fc1_2fc1
	fsBcache = 0xca45_1a4e
)

// requireLocalFilesystem refuses filesystems outside the durability
// whitelist (Rust require_local_filesystem Unsupported).
func requireLocalFilesystem(f *os.File) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return nsIoError("inspect publication filesystem", err)
	}
	switch uint32(st.Type) {
	case fsExt, fsXFS, fsBtrfs, fsF2fs, fsZFS, fsBcache:
		return nil
	}
	return nsUnsupportedError()
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
