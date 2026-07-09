//go:build linux

package iprangedb

import (
	"golang.org/x/sys/unix"
)

// growFileAlloc allocates disk space for the growth region using Linux-specific
// fallocate. Falls back to pwrite-zero-fill if fallocate is not supported.
func growFileAlloc(fd int, oldLen, newLen int64) error {
	growOff := oldLen
	growLen := newLen - oldLen
	// Try fallocate with FALLOC_FL_ZERO_RANGE (Linux).
	if err := unix.Fallocate(fd, unix.FALLOC_FL_ZERO_RANGE, growOff, growLen); err == nil {
		return nil
	}
	// Try posix_fallocate.
	if err := unix.Fallocate(fd, 0, growOff, growLen); err == nil {
		return nil
	}
	// Fallback: pwrite-zero-fill every page in the grown region.
	return growFilePwrite(fd, oldLen, newLen)
}
