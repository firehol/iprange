//go:build !windows

package validation

import "golang.org/x/sys/unix"

// unixO_NOFOLLOW is the no-follow open flag of the read-only source
// open (Rust database_file::open_read_only O_NOFOLLOW).
const unixO_NOFOLLOW = unix.O_NOFOLLOW
