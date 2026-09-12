//go:build windows

package handlers

import "os"

// openDirectCsvNoBlock opens a direct CSV input. Windows has no POSIX
// FIFO open-blocking hazard, so the plain open is canonical.
func openDirectCsvNoBlock(path string) (*os.File, error) {
	return os.Open(path)
}
