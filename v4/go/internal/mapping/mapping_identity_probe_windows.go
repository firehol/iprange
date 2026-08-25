//go:build windows

package mapping

import (
	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// StatIdentity returns the volume serial and file index of the file at
// path (Rust validation::LocalFileIdentity windows projection through
// BY_HANDLE_FILE_INFORMATION). Any failure carries the IO class with
// the "publication filesystem operation failed" detail of the POSIX
// probe.
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
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}
