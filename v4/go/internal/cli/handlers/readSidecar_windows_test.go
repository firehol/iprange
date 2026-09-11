//go:build windows

package handlers

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// readFileShareDelete reads a whole file through a share-delete
// handle (windows.CreateFile with FILE_SHARE_READ|WRITE|DELETE).
// Go's os.ReadFile does not share FILE_SHARE_DELETE, so it cannot
// open a file while any live reader retains a DELETE-access handle
// (the reader-coordination sidecar gate); the product's own opens
// all use the share-delete profile, and these test reads must do the
// same to assert the displaced sidecar bytes on Windows.
func readFileShareDelete(path string) ([]byte, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}
