//go:build windows

package handlers

import "os"

// openDirectCsvNoBlock opens one direct CSV input and returns it only
// when the handle it opened is a regular file. Windows has no POSIX
// FIFO open-blocking hazard, so the plain open is the canonical
// behavior; the handle check is identical because the caller judges the
// opened handle, never the path it statted.
func openDirectCsvNoBlock(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := checkOpenedRegular(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
