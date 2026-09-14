//go:build !windows

package fileio

import (
	"os"

	"golang.org/x/sys/unix"
)

// openInputNoBlock opens one caller input and returns it only when the
// descriptor it opened is a regular file.
//
// O_NONBLOCK makes a FIFO swapped in after the caller's path check
// return immediately instead of waiting for a writer (regular files
// ignore O_NONBLOCK, so input behavior is unchanged), and the fstat of
// that same descriptor is the regular-file authority: the caller
// never trusts a path stat that may already be stale (Rust
// io::caller_open::open_regular).
func openInputNoBlock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := checkOpenedRegular(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
