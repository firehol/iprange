//go:build windows

package worker

import (
	"os"

	"golang.org/x/sys/windows"
)

// openControlFile opens an existing control file read-write (Rust
// Control::open_worker). The creator-only control file is created with
// a protected FILE_ALL_ACCESS DACL and a FILE_SHARE_DELETE handle
// (security.create_private), and Windows requires every later handle
// to keep advertising DELETE sharing while such a handle exists; the
// stdlib os.OpenFile share mode (read+write only) cannot reopen it, so
// the raw create arm is mirrored here (same access, share, and
// no-inheritance shape as mapping/openNoFollow and the live source
// open arm).
func openControlFile(path string) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
