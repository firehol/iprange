package iprangedb

// Worker test harness seam: the routed facade tests and the traced
// public live-validation legs install the real worker binary on the
// worker-supported platforms (linux, darwin, freebsd, windows on
// amd64 and arm64, where the facade routes through the worker
// client); every platform without a worker build stays in-process and
// the helper is a no-op.

import "testing"

// installWorkerForTest installs the real worker binary as the spawn
// candidate source on the worker-supported platforms; on every other
// platform the facade stays in-process and the helper does nothing.
// Every root-package test that exercises a routing path on a
// worker-supported platform must call it.
func installWorkerForTest(t *testing.T) { installWorkerForTestPlatform(t) }
