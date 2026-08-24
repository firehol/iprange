//go:build windows

package publication

// Destination-parent proof, Windows variant (Rust publication/namespace/
// windows.rs Directory::open): CreateFileW with
// FILE_FLAG_OPEN_REPARSE_POINT opens the parent without following
// junctions or directory symlinks; a missing parent
// (ERROR_FILE_NOT_FOUND / ERROR_PATH_NOT_FOUND) is Missing ->
// NameNotFound, an open or attribute failure is the IO class, and any
// entry that is not a real directory (FILE_ATTRIBUTE_DIRECTORY clear) or
// is a reparse point (FILE_ATTRIBUTE_REPARSE_POINT set) is NotDirectory
// -> Conflict "destination parent is not a directory" — the exact arm
// the POSIX path open can never reach (publication/problem.rs).
// Rejecting reparse-point parents intentionally refuses junction- and
// symlink-redirected destination directories like the Rust engine.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/windows"
)

// CheckPublicationParent proves one destination parent is a plain
// directory and classifies every refusal exactly like Rust
// Destination::bind -> Directory::open (publication/namespace/
// windows.rs + problem.rs): missing -> CodeNameNotFound "publication name
// is missing", open/attribute failures -> CodeIO "publication filesystem
// operation failed", and a non-directory or reparse-point parent ->
// CodeConflict "destination parent is not a directory". nil means the
// parent is a plain directory.
func CheckPublicationParent(dir string) error {
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		// Rust open_path refuses an embedded NUL with InvalidName; a
		// NUL can only reach here through a caller that bypassed
		// ValidDestinationName.
		return &format.Error{Code: format.CodeNameInvalid, Detail: "invalid destination parent name"}
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return &format.Error{Code: format.CodeConflict, Detail: "destination parent is not a directory"}
	}
	return nil
}
