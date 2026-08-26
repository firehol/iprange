//go:build windows

package recovery

import (
	"os"

	"golang.org/x/sys/windows"
)

// openScratchTestFile opens one scratch artifact with the full share
// set of the production machine (FILE_SHARE_READ|WRITE|DELETE). Go's
// os package opens without FILE_SHARE_DELETE, so a retained
// creator-only handle (opened with FILE_ALL_ACCESS, which includes
// DELETE) denies the open with a sharing violation on Windows.
func openScratchTestFile(path string, writable bool) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if writable {
		access |= windows.GENERIC_WRITE
	}
	handle, err := windows.CreateFile(ptr, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// readScratchTestFile reads one scratch artifact through the
// full-share test handle (a second os-package open would still be
// denied by the retained creator-only handle).
func readScratchTestFile(path string) ([]byte, error) {
	file, err := openScratchTestFile(path, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, int(info.Size()))
	n, err := file.ReadAt(buffer, 0)
	if err != nil && n != len(buffer) {
		return nil, err
	}
	return buffer, nil
}
