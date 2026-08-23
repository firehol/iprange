//go:build darwin

package live

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// SyncFile forces one open file to stable storage (Rust namespace
// sync_file): fcntl(F_FULLFSYNC) on macOS, because plain fsync can
// return before the drive's volatile cache is flushed (same decision
// as the mapping owner's syncFile).
func SyncFile(f *os.File) error {
	if _, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "fcntl(F_FULLFSYNC): " + err.Error()}
	}
	return nil
}
