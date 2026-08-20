//go:build windows

package mapping

import "github.com/firehol/iprange/v4/go/internal/format"

// The Windows mapping owner is an honest stub (see mapping_windows.go);
// the publication namespace primitives refuse with the same contract
// code. Every consumer fails closed on Windows.

// exchangeAvailable reports whether the target has an atomic name
// exchange.
func exchangeAvailable() bool { return false }

// RenameNoReplace refuses on Windows.
func RenameNoReplace(oldpath, newpath string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_noreplace is not available on windows"}
}

// RenameExchange refuses on Windows.
func RenameExchange(oldpath, newpath string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_exchange is not available on windows"}
}

// RenamePlain refuses on Windows.
func RenamePlain(oldpath, newpath string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename is not available on windows"}
}

// Unlink refuses on Windows.
func Unlink(path string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "unlink is not available on windows"}
}

// SyncDirectory refuses on Windows.
func SyncDirectory(dir string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "synchronize publication directory is not available on windows"}
}

// StatIdentity refuses on Windows.
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "stat publication identity is not available on windows"}
}
