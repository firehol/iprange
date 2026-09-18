//go:build unix

package legacy

import (
	"os"
	"testing"
)

// denyOpeningDirectory removes every access mode from the directory so the
// open fails with EACCES, and restores the mode when the test ends so the
// scratch tree stays removable. It returns "" on success.
func denyOpeningDirectory(t *testing.T, path string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		return "running as root, which mode 000 does not deny"
	}
	if err := os.Chmod(path, 0o000); err != nil {
		return "chmod 000: " + err.Error()
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
	return ""
}
