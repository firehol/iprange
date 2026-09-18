//go:build windows

package legacy

import (
	"testing"

	"golang.org/x/sys/windows"
)

// denyOpeningDirectory holds the directory open with dwShareMode == 0 for the
// length of the test, so every later open of it fails with
// ERROR_SHARING_VIOLATION. It returns "" on success.
//
// A sharing conflict is the refusal this platform cannot talk around: opening
// a directory normally fails with ERROR_ACCESS_DENIED for a *parameter* reason
// (CreateFile wants FILE_FLAG_BACKUP_SEMANTICS for a directory), which the
// loader resolves the way the platform requires, so a denial that backup
// intent can also bypass would pin the platform's parameter rule rather than
// the caller's rights. A sharing violation is refused whatever flags the later
// open asks for, which is the shape decision 3A of SOW-0028 asks for.
func denyOpeningDirectory(t *testing.T, path string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "the fixture path is not representable: " + err.Error()
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "holding the directory exclusively: " + err.Error()
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })
	return ""
}
