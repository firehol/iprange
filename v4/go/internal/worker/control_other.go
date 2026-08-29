//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

// Package worker has no implementation on this platform. The
// mapped-fault control surface, the SIGBUS/VEH handlers, and the
// client arms exist only on the worker cross-build matrix (linux,
// darwin, freebsd, and windows for amd64 and arm64; the mapped-control
// atomics are hand-written assembly for exactly those combinations).
// Nothing in the SDK routes here: the routing facade refuses faultable
// operations with the typed worker-unavailable class before any source
// scan or destination mutation (binary-format-v4.md section 19). This
// file exists only so module-wide builds and test sweeps do not fail
// on "build constraints exclude all Go files".
package worker
