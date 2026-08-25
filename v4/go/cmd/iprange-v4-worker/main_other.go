//go:build !linux || !amd64

// Platform-refusal entry (internal/worker control_other.go parity): the SIGBUS isolation proof is
// linux/amd64-only, so the worker refuses to start anywhere else with
// the honest recorded stance. The refusal exits with the Rust protocol
// code 65, the same class the parent maps to "worker version or
// protocol does not match".

package main

import "os"

func main() {
	os.Stderr.WriteString("iprange v4 worker: worker SIGBUS isolation is not implemented on this platform\n")
	os.Exit(65)
}
