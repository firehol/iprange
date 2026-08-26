// Device+inode capture for the live snapshot self-replacement probe
// (Rust publication namespace identity over the open file). This file
// provides the POSIX variant; the windows variant lives in
// snapshot_windows.go and is the real probe over the retained
// destination handle.

//go:build !windows

package snapshot

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// openDestinationNoFollow opens the destination main name without
// following a final symlink (Rust Directory::open_regular O_NOFOLLOW).
func openDestinationNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
}

// fileIdentityOf captures the device+inode of one open descriptor.
func fileIdentityOf(f *os.File) (uint64, uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// directoryIdentityOf captures the device+inode of the destination
// parent directory (Rust Destination::bind Directory::open identity;
// the reject_live_self same-filesystem rule compares the destination
// file against it).
func directoryIdentityOf(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// fileLinksOf reports the link count of one open descriptor (Rust
// regular_identity nlink rule: a publication destination must have
// exactly one link).
func fileLinksOf(f *os.File) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Nlink), nil
}
