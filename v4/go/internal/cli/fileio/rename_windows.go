//go:build windows

package fileio

import "os"

// RenameReplace publishes one completed temporary over its destination
// (Rust fs::rename windows arm: MoveFileExW with MOVEFILE_REPLACE_EXISTING,
// which os.Rename performs).
func RenameReplace(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
