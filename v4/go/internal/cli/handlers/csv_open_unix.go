//go:build !windows

package handlers

import (
	"os"

	"golang.org/x/sys/unix"
)

// openDirectCsvNoBlock opens one direct CSV input and returns it only
// when the descriptor it opened is a regular file.
//
// O_NONBLOCK makes a FIFO swapped in after the caller's path check
// return immediately instead of waiting for a writer (regular files
// ignore O_NONBLOCK, so direct-replace behavior is unchanged); the
// fstat of that same descriptor is the regular-file authority, so the
// caller never consumes a swapped-in FIFO as an empty CSV (Rust
// io::caller_open::open_regular).
func openDirectCsvNoBlock(path string) (*os.File, error) {
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
