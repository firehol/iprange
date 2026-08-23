//go:build freebsd

package live

import "golang.org/x/sys/unix"

// atNofollow is the fstatat AT_SYMLINK_NOFOLLOW flag (freebsd value).
const atNofollow = unix.AT_SYMLINK_NOFOLLOW
