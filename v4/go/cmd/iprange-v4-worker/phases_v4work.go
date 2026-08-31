//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64) && v4work

// Bench-only worker observability (SOW-0027 direction item 3). These
// helpers compile only under the v4work tag, so the production worker
// binary contains no profiling or phase-recording machinery.

package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"time"
)

// phaseMarker records the worker-side phase timings of one validation or
// recovery run. Set IPRANGE_WORKER_PHASES to a file path to append
// "<name> <nanoseconds-since-process-start>" rows while the worker
// runs; the parent spawns the worker with null stdio, so the rows go to
// the file, never stdout or stderr.
type phaseMarker struct {
	started time.Time
	path    string
}

// newPhaseMarker starts a phase marker against the process start time.
func newPhaseMarker() phaseMarker {
	return phaseMarker{started: time.Now(), path: os.Getenv("IPRANGE_WORKER_PHASES")}
}

// mark appends one phase row to the IPRANGE_WORKER_PHASES file.
func (p phaseMarker) mark(name string) {
	if p.path == "" {
		return
	}
	file, err := os.OpenFile(p.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "%s %d\n", name, time.Since(p.started).Nanoseconds())
	_ = file.Close()
}

// startWorkerProfile writes one pprof CPU profile of the worker process
// when IPRANGE_CPU_PROFILE is set (the worker runs one wire mode per
// process, so one process is one operation).
func startWorkerProfile() func() {
	path := os.Getenv("IPRANGE_CPU_PROFILE")
	if path == "" {
		return nil
	}
	file, err := os.Create(path + "." + strconv.Itoa(os.Getpid()))
	if err != nil {
		os.Exit(exitUsage)
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		os.Exit(exitUsage)
	}
	return func() {
		pprof.StopCPUProfile()
		_ = file.Close()
	}
}
