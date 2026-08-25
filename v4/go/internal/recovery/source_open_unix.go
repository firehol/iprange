//go:build !windows

package recovery

import (
	"os"
)

// openSourceFilePlatform opens the database main without following a
// final symlink (Rust open_file unix arm: database_file::
// open_read_only for the immutable arm, live_namespace::open_rw for
// the quiescent arm; Go sets close-on-exec on every open).
func openSourceFilePlatform(path string, flags int) (*os.File, error) {
	return os.OpenFile(path, flags|unixO_NOFOLLOW, 0)
}
