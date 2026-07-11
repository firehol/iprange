//go:build !linux

package iprangedb

// getThreadID returns 0 on non-Linux platforms (no portable gettid).
// Same-process readers will share slots via pid alone.
func getThreadID() uint32 {
	return 0
}
