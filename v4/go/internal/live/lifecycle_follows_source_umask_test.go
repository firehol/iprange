//go:build linux

package live

import (
	"syscall"
	"testing"
)

// umaskWindow fixes the process umask for one creation and restores
// it afterward (the test owns its goroutine's thread). Linux only:
// the SYS_UMASK constant (and the mode-shaping detectors that use
// the window) exist there.
func umaskWindow(t *testing.T, mask uint32) {
	t.Helper()
	ret, _, _ := syscall.Syscall(syscall.SYS_UMASK, uintptr(mask), 0, 0)
	t.Cleanup(func() {
		syscall.Syscall(syscall.SYS_UMASK, ret, 0, 0)
	})
}
