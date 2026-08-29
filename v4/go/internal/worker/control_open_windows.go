//go:build windows && (amd64 || arm64)

package worker

import (
	"os"

	"golang.org/x/sys/windows"
)

// openControlFile opens an existing control file read-write. This is
// the Go side of Rust Control::open_worker with one deliberate
// platform divergence: Go unlinks the worker control on every platform
// (Rust's remove_path is #[cfg(unix)] and leaves the file on Windows),
// and Windows DeleteFileW requires every open handle to advertise
// FILE_SHARE_DELETE while the parent still holds the creator handle.
// The stdlib os.OpenFile share mode (read+write only) would make the
// parent's unlink fail with a sharing violation, so the raw create arm
// is mirrored here (same access, share, and no-inheritance shape as
// mapping/openNoFollow and the live source open arm). The creator-only
// control file is created with a protected FILE_ALL_ACCESS DACL and a
// FILE_SHARE_DELETE handle (security.create_private).
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
