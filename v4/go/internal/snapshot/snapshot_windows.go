// Windows arms of the live snapshot self-replacement probe (Rust
// publication::namespace open_regular, regular_identity, and
// directory identity over the destination): the destination main is
// opened read-only with the full share modes and
// FILE_FLAG_OPEN_REPARSE_POINT, a reparse-point or directory
// destination refuses as the non-regular class, and the identity pair
// is the volume serial plus the 64-bit file index of
// BY_HANDLE_FILE_INFORMATION like the live namespace machine.

//go:build windows

package snapshot

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/windows"
)

// openDestinationNoFollow opens the destination main name without
// following a final reparse point (Rust Directory::open_regular
// read-only arm: GENERIC_READ|READ_CONTROL with FILE_FLAG_OPEN_
// REPARSE_POINT and the full share set; regular_identity refuses
// directory and reparse attributes with the non-regular class).
func openDestinationNoFollow(path string) (*os.File, error) {
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		file.Close()
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		file.Close()
		return nil, &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	}
	return file, nil
}

// fileIDInfo is the FILE_ID_INFO structure of the Windows SDK (Rust
// namespace/windows.rs file_identity): 64-bit volume serial plus the
// 128-bit file identifier.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileId             [16]byte
}

// fileIdentityOf captures the 64-bit volume serial and the low half
// of the 128-bit FILE_ID_INFO identifier of one open descriptor (Rust
// namespace/windows.rs file_identity; NTFS zeroes the high half).
func fileIdentityOf(f *os.File) (uint64, uint64, error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return info.VolumeSerialNumber, binary.LittleEndian.Uint64(info.FileId[0:8]), nil
}

// directoryIdentityOf captures the 64-bit volume serial and the low
// half of the 128-bit FILE_ID_INFO identifier of the destination
// parent directory (Rust Destination::bind Directory::open identity;
// the reject_live_self same-filesystem rule compares the destination
// file against it; the mutation machine already proved the parent is
// a plain directory through publication.CheckPublicationParent).
func directoryIdentityOf(path string) (device uint64, inode uint64, err error) {
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
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

// fileLinksOf reports the hard-link count of one open descriptor
// (Rust regular_link_count over BY_HANDLE_FILE_INFORMATION; the
// publication destination must have exactly one link).
func fileLinksOf(f *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(info.NumberOfLinks), nil
}
