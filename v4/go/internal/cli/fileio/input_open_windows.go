//go:build windows

package fileio

import "os"

// openInputNoBlock opens one input file. Windows has no POSIX FIFO
// open-blocking hazard, so the plain open is the canonical behavior.
func openInputNoBlock(path string) (*os.File, error) {
	return os.Open(path)
}
