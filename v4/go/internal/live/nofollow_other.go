//go:build !windows && !freebsd

package live

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isNofollowSymlink reports whether an openat failure is the
// no-follow final-symlink class (Rust namespace::is_nofollow_symlink).
func isNofollowSymlink(err error) bool {
	return errors.Is(err, unix.ELOOP)
}
