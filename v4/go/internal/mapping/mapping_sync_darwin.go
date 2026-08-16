//go:build darwin

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// syncFile forces stable storage on macOS with fcntl(F_FULLFSYNC), mirroring
// Rust mapping.rs sync_file: plain fsync on macOS returns after the kernel
// buffer flush and can leave data in the drive's volatile write cache.
func syncFile(fd int) error {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_FULLFSYNC, 0); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "fcntl(F_FULLFSYNC): " + err.Error()}
	}
	return nil
}
