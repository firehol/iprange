//go:build !darwin && !windows

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// syncFile forces the file's dirty pages to stable storage (fsync), mirroring
// Rust mapping.rs sync_file on non-macOS platforms.
func syncFile(fd int) error {
	if err := unix.Fsync(fd); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "fsync: " + err.Error()}
	}
	return nil
}
