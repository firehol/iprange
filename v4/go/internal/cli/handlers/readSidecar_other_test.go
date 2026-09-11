//go:build !windows

package handlers

import "os"

// readFileShareDelete reads a whole file for the same-source guard
// tests.  On POSIX the plain os.ReadFile works against any open
// handle; the Windows arm (readSidecar_windows_test.go) re-opens with
// FILE_SHARE_DELETE because Go's stdlib open does not share delete
// and the live reader's sidecar gate retains a DELETE-access handle.
func readFileShareDelete(path string) ([]byte, error) {
	return os.ReadFile(path)
}
