//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

// Platform-refusal entry: the mapped-fault worker handler exists only
// on the worker cross-build matrix (linux, darwin, freebsd, and
// windows for amd64 and arm64); anywhere else the worker refuses to
// start with the honest recorded stance. The refusal exits with the
// protocol code the parent maps to a worker operation failure, and the
// routing facade never spawns the worker off the matrix, so this entry
// is only reachable if the binary is invoked directly.

package main

import "os"

func main() {
	os.Stderr.WriteString("iprange v4 worker: worker fault isolation is not implemented on this platform\n")
	os.Exit(65)
}
