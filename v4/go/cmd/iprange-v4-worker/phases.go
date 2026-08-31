//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64) && !v4work

// Production no-ops of the worker observability hooks. Profiling and
// phase-recording machinery exists only in v4work test builds
// (phases_v4work.go); the shipped iprange-v4-worker carries none
// (AGENTS.md test-only observability rule).

package main

// phaseMarker is the production no-op of the worker phase recorder.
type phaseMarker struct{}

// newPhaseMarker returns the production no-op marker.
func newPhaseMarker() phaseMarker { return phaseMarker{} }

// mark is the production no-op; see phases_v4work.go.
func (phaseMarker) mark(string) {}

// startWorkerProfile is the production no-op; see phases_v4work.go.
func startWorkerProfile() func() { return nil }
