//go:build windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/windows"
)

// openSourceFilePlatform opens the immutable database main without
// following a final reparse point (Rust database_file::open_read_only
// windows arm: the full share modes and FILE_FLAG_OPEN_REPARSE_POINT).
// The attribute refusal carries the exact Rust detail of that arm.
//
// The quiescent read-write arm is not here: it opens through the
// retained parent directory in live.OpenRetainedReadWrite, exactly like
// Rust live_namespace::open_rw, because the namespace classes of that
// arm (volume locality, reparse point, directory) come from the
// retained directory handle, not from a path-level open.
func openSourceFilePlatform(path string) (*os.File, error) {
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
