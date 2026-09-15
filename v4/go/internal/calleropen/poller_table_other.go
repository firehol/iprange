//go:build !unix

package calleropen

// FreeDescriptors is the non-unix arm of the table read. This file must not
// carry a GOOS name in its own name: a `poller_table_windows.go` filename
// would restrict this `!unix` arm to GOOS=windows by filename alone, and the
// other !unix targets (plan9, js, wasip1) would then have no FreeDescriptors
// at all. The goos matrix gate type-checks Windows, not those targets, so the
// gap would be silent; the naming rule for this package's arms is the build
// expression alone.
//
// FreeDescriptors reports no poller-descriptor demand on Windows: there
// is no RLIMIT_NOFILE there and the I/O completion port allocates
// neither a descriptor nor an eventfd, so the resolver is never gated by
// descriptor pressure on this platform.
func FreeDescriptors() (free int, ok bool) { return 1 << 20, true }
