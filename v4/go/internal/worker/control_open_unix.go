//go:build !windows && (amd64 || arm64)

package worker

import "os"

// openControlFile opens an existing control file read-write (Rust
// Control::open_worker). The POSIX handle needs no share mode; the
// Windows arm must advertise DELETE sharing because the creator-only
// control handle grants FILE_ALL_ACCESS.
func openControlFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
