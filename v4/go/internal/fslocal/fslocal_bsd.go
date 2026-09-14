//go:build darwin || freebsd

// The BSD family answers the durability proof from the mount flags
// (Rust require_local_filesystem's MNT_LOCAL arms for darwin and
// freebsd).

package fslocal

import (
	"os"

	"golang.org/x/sys/unix"
)

// RequireLocal reports ErrNotLocal unless f sits on a locally mounted
// filesystem. The statfs failure itself is reported verbatim.
func RequireLocal(f *os.File) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return err
	}
	if uint64(st.Flags)&uint64(unix.MNT_LOCAL) != 0 {
		return nil
	}
	return ErrNotLocal
}
