//go:build windows

package handlers

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readFileShareDelete reads a whole file through a share-delete
// mapping view.  Two Windows constraints force this shape: Go's
// os.ReadFile does not share FILE_SHARE_DELETE (so it cannot open a
// file that a live reader's sidecar gate retains with DELETE access),
// and the live reader table's exclusive per-slot byte-range lock
// blocks plain ReadFile through any other handle, while memory-mapped
// reads are not byte-range-locked (the product itself reads the
// sidecar through its mapping).  The absent-name identity and slot
// ownership stays covered by the lock; this helper only asserts the
// displaced table bytes.
func readFileShareDelete(path string) ([]byte, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	section, err := windows.CreateFileMapping(h, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(section)
	view, err := windows.MapViewOfFile(section, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	defer windows.UnmapViewOfFile(view)
	n := int(info.Size())
	if n < 0 {
		n = 0
	}
	return unsafe.Slice((*byte)(uintptrToPointer(view)), n), nil
}

// uintptrToPointer reinterprets one kernel-returned address as a
// pointer without a direct conversion (vet's unsafeptr check only
// accepts uintptr-to-pointer conversions in syscall argument lists;
// same pattern as mapping/platform_windows.go).
func uintptrToPointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}
