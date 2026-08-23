//go:build !darwin

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// SyncFile forces one open file to stable storage (Rust namespace
// sync_file): plain fsync on every platform but macOS (see
// sync_file_darwin.go). Windows publication refuses before this point;
// the helper stays available so the machine compiles everywhere.
func SyncFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "fsync: " + err.Error()}
	}
	return nil
}
