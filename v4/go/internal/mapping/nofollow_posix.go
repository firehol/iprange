//go:build !windows && !freebsd

package mapping

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isNofollowSymlink reports whether an open failure is the no-follow
// final-symlink class (Rust publication::namespace::is_nofollow_symlink).
func isNofollowSymlink(err error) bool {
	return errors.Is(err, unix.ELOOP)
}
