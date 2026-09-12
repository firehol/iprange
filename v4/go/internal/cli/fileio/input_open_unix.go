//go:build !windows

package fileio

import (
	"os"

	"golang.org/x/sys/unix"
)

// openInputNoBlock opens one input file without ever blocking on a
// FIFO: O_NONBLOCK makes a FIFO swapped in between the caller's
// pre-stat and this open return immediately (regular files ignore
// O_NONBLOCK, so input behavior is unchanged). The caller verifies
// the opened descriptor's identity after the open.
func openInputNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
