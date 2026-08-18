//go:build !v4work

// Package fault carries test-only process crash points (Rust fault.rs).
// Production builds compile Crash to nothing; v4work builds honor
// IPRANGE_V4_TEST_CRASH_AT and exit with Rust's code 86 when the named
// point is reached, so crash-consistency tests can run the real
// publication path in a child process.

package fault

// Crash no-ops in production builds (Rust fault::crash compiled out).
func Crash(point string) {}
