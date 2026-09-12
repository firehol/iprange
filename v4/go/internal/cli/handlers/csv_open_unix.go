//go:build !windows

package handlers

import (
	"os"

	"golang.org/x/sys/unix"
)

// openDirectCsvNoBlock opens a direct CSV input without ever blocking
// on a FIFO: O_NONBLOCK makes a FIFO swapped in between the caller's
// pre-stat and this open return immediately (regular files ignore
// O_NONBLOCK, so direct-replace behavior is unchanged; the CSV header
// walk then refuses any non-regular content).
func openDirectCsvNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
