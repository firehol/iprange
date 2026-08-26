//go:build windows

package mapping

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// StatIdentity returns the 64-bit volume serial and the low half of
// the 128-bit FILE_ID_INFO identifier of the file at path (Rust
// validation::LocalFileIdentity windows projection through
// namespace/windows.rs file_identity; NTFS zeroes the high half). Any
// failure carries the IO class with the "publication filesystem
// operation failed" detail of the POSIX probe.
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	handle, err := windows.CreateFile(ptr, 0, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	defer windows.CloseHandle(handle)
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return info.VolumeSerialNumber, binary.LittleEndian.Uint64(info.FileId[0:8]), nil
}
