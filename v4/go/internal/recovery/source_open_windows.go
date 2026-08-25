//go:build windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/windows"
)

// openSourceFilePlatform opens the database main without following a
// final reparse point (Rust open_file windows arm: the full share
// modes and FILE_FLAG_OPEN_REPARSE_POINT mirror database_file::
// open_read_only and live_namespace::open_rw). The attribute refusal
// carries the exact Rust WrongMode detail of the arm that opened: the
// read-only arm refuses reparse points, the read-write namespace arm
// refuses directories and reparse points.
func openSourceFilePlatform(path string, flags int) (*os.File, error) {
	writable := flags&os.O_RDWR != 0
	access := uint32(windows.GENERIC_READ)
	if writable {
		access |= windows.GENERIC_WRITE
	}
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		access,
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
	attributes := info.FileAttributes
	refused := attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(writable && attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)
	if refused {
		file.Close()
		detail := "database path is a Windows reparse point"
		if writable {
			detail = "live file ownership changed"
		}
		return nil, &format.Error{Code: format.CodeWrongState, Detail: detail}
	}
	return file, nil
}
