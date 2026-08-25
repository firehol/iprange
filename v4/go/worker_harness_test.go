package iprangedb

// Worker test harness seam: the routed facade tests and the traced
// public live-validation legs install the real worker binary on
// linux/amd64 (the facade routes through the worker client there);
// every other platform stays in-process and the helper is a no-op.

import "testing"

// installWorkerForTest installs the real worker binary as the spawn
// candidate source on linux/amd64; on every other platform the facade
// stays in-process and the helper does nothing. Every root-package
// test that exercises a routing path on linux/amd64 must call it.
func installWorkerForTest(t *testing.T) { installWorkerForTestPlatform(t) }
