//go:build !windows

package recovery

import "os"

// readScratchTestFile reads one scratch artifact through the plain
// os open (no sharing constraints on POSIX).
func readScratchTestFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
