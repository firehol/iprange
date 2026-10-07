//go:build !windows

package recovery

import "golang.org/x/sys/unix"

// setUmask sets the process umask and returns the previous one; the
// windows twin is a no-op so callers can state the intent inline (the
// root package's creator-only tests use the same pair).
func setUmask(mask int) int {
	return unix.Umask(mask)
}
