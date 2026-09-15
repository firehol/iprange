//go:build !unix

package calleropen

// FreeDescriptors reports no poller-descriptor demand on Windows: there
// is no RLIMIT_NOFILE there and the I/O completion port allocates
// neither a descriptor nor an eventfd, so the resolver is never gated by
// descriptor pressure on this platform.
func FreeDescriptors() (free int, ok bool) { return 1 << 20, true }
