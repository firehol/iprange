//go:build darwin || freebsd

// Local-filesystem durability proof and name_max for the BSD-family
// targets (Rust namespace/unix.rs require_local_filesystem apple/freebsd
// arms: MNT_LOCAL, plus fpathconf _PC_NAME_MAX). _PC_NAME_MAX is 4 in
// both sys/unistd.h headers; x/sys exposes Fpathconf on these targets.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// pcNameMax is the POSIX _PC_NAME_MAX option number (darwin and
// freebsd sys/unistd.h both define it as 4).
const pcNameMax = 4

// requireLocalFilesystem refuses filesystems that are not mounted
// local (Rust require_local_filesystem MNT_LOCAL Unsupported).
func requireLocalFilesystem(f *os.File) error {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return nsIoError("inspect publication filesystem", err)
	}
	if uint64(st.Flags)&uint64(unix.MNT_LOCAL) != 0 {
		return nil
	}
	return nsUnsupportedError()
}

// directoryNameMax reports the directory name_max (Rust fpathconf
// _PC_NAME_MAX).
func directoryNameMax(f *os.File) (int, error) {
	value, err := unix.Fpathconf(int(f.Fd()), pcNameMax)
	if err != nil {
		return 0, err
	}
	return int(value), nil
}
