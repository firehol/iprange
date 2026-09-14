//go:build !windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// openSourceFilePlatform opens the immutable database main without
// following a final symlink (Rust database_file::open_read_only: the
// no-follow, non-blocking open plus require_regular_file over the
// authoritative fd). O_NONBLOCK makes a FIFO open return immediately
// instead of blocking until a writer appears, and the fd regular check
// then refuses the fifo; regular files ignore O_NONBLOCK, so database
// behavior is unchanged. Nothing stats the path: the opened fd is the
// identity, so a file replaced between open and check is judged on the
// bytes actually opened. The refusal class mirrors the Rust arm
// (require_regular_file -> InvalidArgument); an open failure keeps the
// IO class, which is what a symlink (ELOOP) or a socket answers.
//
// The quiescent read-write arm is not here: it opens through the
// retained parent directory in live.OpenRetainedReadWrite, exactly like
// Rust live_namespace::open_rw, because the namespace classes of that
// arm (local-filesystem durability, symlink, link count) come from the
// dirfd, not from a path-level errno.
func openSourceFilePlatform(path string) (*os.File, error) {
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
