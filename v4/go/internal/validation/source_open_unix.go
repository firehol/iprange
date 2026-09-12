//go:build !windows

package validation

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// openReadOnlyNoFollow opens the database main read-only without
// following a final symlink (Rust database_file::open_read_only unix
// arm: O_NOFOLLOW | O_NONBLOCK with the authoritative-fd regular
// check; Go sets close-on-exec on every open). O_NONBLOCK makes a
// FIFO open return immediately instead of blocking until a writer
// appears, and the fd regular check then refuses the fifo; regular
// files ignore O_NONBLOCK, so database behavior is unchanged. The
// opened fd is the identity: nothing stats the path directly, so a
// file replaced between open and check is judged on the bytes
// actually opened.
func openReadOnlyNoFollow(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unixO_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "database path is not a regular file"}
	}
	return file, nil
}
