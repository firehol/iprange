//go:build !windows

package recovery

import "os"

// openScratchTestFile opens one scratch artifact for a test. On
// Windows the open must offer the full share set (see the windows
// arm); on POSIX the plain open has no sharing constraints.
func openScratchTestFile(path string, writable bool) (*os.File, error) {
	if writable {
		return os.OpenFile(path, os.O_RDWR, 0)
	}
	return os.Open(path)
}
