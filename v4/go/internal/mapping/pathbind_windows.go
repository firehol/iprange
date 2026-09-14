//go:build windows

package mapping

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// verifyPathAgainstFile is the Windows arm of VerifyPathAgainstFile.
// Windows has no bindable-directory analogue of the POSIX
// O_DIRECTORY | O_NOFOLLOW parent open, so the proof is the handle
// comparison the platform can express: the path must still name the
// opened file as one regular file (Rust verify_path_any_link, whose
// Windows arm is the volume-qualified file identity of
// publication::namespace::windows). The full Windows namespace machine
// (reparse-point refusal, volume-qualified identity) is owned by
// internal/live and is applied by every surface that needs it.
func verifyPathAgainstFile(path string, f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if !st.Mode().IsRegular() {
		return &format.Error{Code: format.CodeWrongState, Detail: "path does not name a regular file"}
	}
	now, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "path does not exist"}
		}
		return &format.Error{Code: format.CodeIO, Detail: "lstat: " + err.Error()}
	}
	if !now.Mode().IsRegular() {
		return &format.Error{Code: format.CodeWrongState, Detail: "path does not name a regular file"}
	}
	if !os.SameFile(now, st) {
		return &format.Error{Code: format.CodeWrongState, Detail: "path no longer names the opened file"}
	}
	return nil
}

// proveLocalNamespace is the Windows arm of the containing-directory
// durability proof. The Windows namespace machine proves local NTFS as
// part of the retained-directory bind in internal/live (see
// directory_windows.go), which every writable arm already passes
// through; there is no second bind to prove here.
func proveLocalNamespace(string) error { return nil }
