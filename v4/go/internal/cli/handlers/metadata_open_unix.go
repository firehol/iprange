//go:build !windows

package handlers

import (
	"os"

	"golang.org/x/sys/unix"
)

// openMetadataSourceNoBlock opens one metadata source and returns it
// only when the descriptor it opened is a regular file.
//
// O_NONBLOCK makes a FIFO swapped in after the caller's path check
// return immediately instead of waiting for a writer (regular files
// ignore O_NONBLOCK, so metadata behavior is unchanged); the fstat of
// that same descriptor is the regular-file authority, so a swapped-in
// FIFO is refused with the arm's invalid_path class exactly as a
// pre-placed node is (Rust lifecycle::read_bounded over the opened
// file).
func openMetadataSourceNoBlock(path string) (*os.File, error) {
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
