//go:build linux || darwin || freebsd

package worker

// workerExecutableName is the SDK worker binary name (Rust
// EXE_SUFFIX: empty on unix; the Windows arm appends .exe).
func workerExecutableName() string { return "iprange-v4-worker" }
