//go:build linux || darwin || freebsd

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// RegularIdentityAnyLink proves one open file is a regular file on the
// directory filesystem, accepting any link count (Rust
// regular_identity_any_link; the open_regular_any_link caller proves
// no single-link requirement).
func RegularIdentityAnyLink(f *os.File, directoryIdentity FileIdentity) (FileIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return FileIdentity{}, nsPlainIoError("inspect retained file", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return FileIdentity{}, nsNotRegularError()
	}
	if uint64(st.Dev) != directoryIdentity.device {
		return FileIdentity{}, nsCrossFilesystemError()
	}
	return FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)}, nil
}

// RegularIdentity proves one open file is a regular file on the
// directory filesystem with exactly one link (Rust regular_identity;
// the retained-file custody arms require the single-link rule).
func RegularIdentity(f *os.File, directoryIdentity FileIdentity) (FileIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return FileIdentity{}, nsPlainIoError("inspect retained file", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return FileIdentity{}, nsNotRegularError()
	}
	if uint64(st.Dev) != directoryIdentity.device {
		return FileIdentity{}, nsCrossFilesystemError()
	}
	if st.Nlink != 1 {
		return FileIdentity{}, nsLinkCountError(uint64(st.Nlink))
	}
	return FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)}, nil
}

// RegularLinkCount reports the current link count of one open file
// (Rust regular_link_count; the retention proofs use it to decide
// canonical, private, or retired placement).
func RegularLinkCount(f *os.File) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, nsPlainIoError("inspect retained file", err)
	}
	return uint64(st.Nlink), nil
}
