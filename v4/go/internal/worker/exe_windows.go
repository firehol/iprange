//go:build windows

package worker

// workerExecutableName is the SDK worker binary name (Rust
// EXE_SUFFIX ".exe": the spec names the helper with the platform's
// normal executable suffix).
func workerExecutableName() string { return "iprange-v4-worker.exe" }
