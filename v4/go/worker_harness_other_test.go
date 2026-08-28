//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

package iprangedb

import "testing"

// installWorkerForTestPlatform is the no-op for platforms without a
// worker build (the facade stays in-process there, the recorded
// stance; the worker cross-build matrix is linux/darwin/freebsd/
// windows on amd64 and arm64).
func installWorkerForTestPlatform(t *testing.T) { t.Helper() }
