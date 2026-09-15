//go:build unix && !linux

package calleropen

// The poller-readiness pins identify the runtime poller by the anon_inode
// link names Linux exposes under /proc/self/fd, which the kqueue platforms do
// not have; design section 13.5 records the non-Linux platforms as unmeasured
// by this wave. The child role is therefore never served here, and the
// dispatcher exists so the unix-tagged TestMain compiles on every platform
// the goos matrix covers.
func maybeServeReadinessChild() (int, bool) { return 0, false }
