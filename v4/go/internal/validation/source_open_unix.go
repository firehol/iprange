//go:build !windows

package validation

import (
	"os"
)

// openReadOnlyNoFollow opens the database main read-only without
// following a final symlink (Rust database_file::open_read_only unix
// arm: O_NOFOLLOW; Go sets close-on-exec on every open).
func openReadOnlyNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unixO_NOFOLLOW, 0)
}
