//go:build !linux

package live

import "testing"

// umaskWindow is a no-op off Linux: the mode-shaping detectors that
// need the window skip themselves there, and creation without it is
// fine for the openability detectors.
func umaskWindow(t *testing.T, mask uint32) {
	t.Helper()
	_ = mask
}
