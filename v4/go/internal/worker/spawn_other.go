//go:build !(linux || darwin || freebsd) || !(amd64 || arm64)

package worker

import (
	"os"
	"time"
)

// spawnDescriptorDemand is the spawn headroom count on platforms without
// the POSIX fd-table hazard: the Windows I/O completion port allocates no
// descriptor and no eventfd, so the check is vacuous and kept for shape
// parity with the unix owner.
const spawnDescriptorDemand = 0

// spawnDescriptorWait bounds the headroom retry (design section 9.3) on
// the same shape as the unix owner; with demand 0 it never waits.
const spawnDescriptorWait = 5 * time.Second

// spawnNullStdio keeps the platform's standard null handling: os/exec
// opens NUL through the Windows handle namespace, which neither consumes
// a descriptor from RLIMIT_NOFILE nor reaches the POSIX poller. Returning
// nil leaves exec.Cmd's nil-stdio path in charge, exactly as before.
func spawnNullStdio() (*os.File, error) { return nil, nil }
