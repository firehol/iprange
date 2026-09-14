//go:build darwin || freebsd

// Local-filesystem durability proof and name_max for the BSD-family
// targets (Rust namespace/unix.rs require_local_filesystem apple/freebsd
// arms: MNT_LOCAL, plus fpathconf _PC_NAME_MAX). _PC_NAME_MAX is 4 in
// both sys/unistd.h headers; x/sys exposes Fpathconf on these targets.

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/fslocal"
)

// pcNameMax is the POSIX _PC_NAME_MAX option number (darwin and
// freebsd sys/unistd.h both define it as 4).
const pcNameMax = 4

// requireLocalFilesystem refuses filesystems that are not mounted local
// (Rust require_local_filesystem MNT_LOCAL Unsupported); the predicate
// itself is owned by internal/fslocal so every directory bind answers
// the same question.
func requireLocalFilesystem(f *os.File) error {
	if err := fslocal.RequireLocal(f); errors.Is(err, fslocal.ErrNotLocal) {
		return nsUnsupportedError()
	} else if err != nil {
		return nsIoError("inspect publication filesystem", err)
	}
	return nil
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
