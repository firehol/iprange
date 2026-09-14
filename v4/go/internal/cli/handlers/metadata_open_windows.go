//go:build windows

package handlers

import "os"

// openMetadataSourceNoBlock opens one metadata source and returns it
// only when the handle it opened is a regular file. Windows has no
// POSIX FIFO open-blocking hazard, so the plain open is the canonical
// behavior; the caller still judges the opened handle.
func openMetadataSourceNoBlock(path string) (*os.File, error) {
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
