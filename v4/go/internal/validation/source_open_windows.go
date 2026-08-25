//go:build windows

package validation

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/windows"
)

// openReadOnlyNoFollow opens the database main read-only without
// following a final reparse point (Rust database_file::open_read_only
// windows arm): the full share modes and FILE_FLAG_OPEN_REPARSE_POINT
// mirror the Rust open, and a reparse-point main refuses with the
// WrongState class of Error::WrongMode.
func openReadOnlyNoFollow(path string) (*os.File, error) {
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		file.Close()
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		file.Close()
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "database path is a Windows reparse point"}
	}
	return file, nil
}
