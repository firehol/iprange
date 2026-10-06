//go:build windows

package live

import (
	"os"

	"golang.org/x/sys/windows"
)

// openForCreatorProof opens the main file with the right the Windows
// DACL proof reads. os.Open does not request READ_CONTROL, so a
// protected file looks unprotected to the proof.
func openForCreatorProof(path string) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
