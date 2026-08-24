//go:build !windows

package recovery

import "golang.org/x/sys/unix"

// unixO_NOFOLLOW is the no-follow open flag of the quiescent source
// open (Rust live_namespace::open_rw O_NOFOLLOW).
const unixO_NOFOLLOW = unix.O_NOFOLLOW
