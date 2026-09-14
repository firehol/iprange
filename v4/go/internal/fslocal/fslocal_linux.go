//go:build linux

// Linux answers the durability proof from statfs f_type: only the
// filesystems whose durability semantics the live contract has
// qualified are accepted (Rust require_local_filesystem's
// EXT/XFS/BTRFS/F2FS/ZFS/BCACHEFS whitelist). Everything else —
// including procfs, sysfs, tmpfs, and network mounts — is ErrNotLocal.

package fslocal

import (
	"os"

	"golang.org/x/sys/unix"
)

const (
	fsExt    = 0x0000_ef53
	fsXFS    = 0x5846_5342
	fsBtrfs  = 0x9123_683e
	fsF2fs   = 0xf2f5_2010
	fsZFS    = 0x2fc1_2fc1
	fsBcache = 0xca45_1a4e
)

// RequireLocal reports ErrNotLocal unless f names a directory on one of
// the whitelisted filesystems. The statfs failure itself is reported
// verbatim so callers keep the io class of a failed probe.
func RequireLocal(f *os.File) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return err
	}
	switch uint32(st.Type) {
	case fsExt, fsXFS, fsBtrfs, fsF2fs, fsZFS, fsBcache:
		return nil
	}
	return ErrNotLocal
}
