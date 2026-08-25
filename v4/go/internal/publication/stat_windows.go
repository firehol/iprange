//go:build windows

package publication

import (
	"os"

	"golang.org/x/sys/windows"
)

// fstatSize reports one retained file's size from its handle
// information (Rust File::metadata().len()); no wrapper allocation on
// the machine verify paths.
func fstatSize(file *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow), nil
}
