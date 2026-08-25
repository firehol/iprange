//go:build !linux || !amd64

package iprangedb

import "testing"

// installWorkerForTestPlatform is the non-worker no-op (the worker
// binary is linux/amd64-only and the non-linux facade stays
// in-process, the recorded stance).
func installWorkerForTestPlatform(t *testing.T) { t.Helper() }
