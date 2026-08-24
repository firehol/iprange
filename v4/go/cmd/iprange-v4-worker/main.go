//go:build linux && amd64

// Command iprange-v4-worker is the isolated mapped-fault worker process
// of the Go v4 SDK (Rust iprange-livedb/src/bin/iprange-v4-worker.rs +
// worker.rs main flow). The parent creates one 1 MiB control mapping
// (internal/worker Control::create_parent), spawns this binary with
// exactly --control <path>, and the binary verifies the header, reports
// WorkerReady, waits for Running, installs the SIGBUS isolation handler,
// runs one wire-encoded mode (inspect / validate / recover / cleanup)
// over the domain machines, and writes the result back through the
// 4-11A wire codecs. Exit codes are the exact Rust values: 64 usage,
// 65 protocol, 0 clean completion; an owned mapped fault exits 197 from
// the naked SIGBUS handler (internal/worker sigbus_linux_amd64.s)
// without unwinding through Go.

package main

import (
	"os"
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// Worker exit codes (Rust worker.rs EXIT_USAGE / EXIT_PROTOCOL and
// control.rs OWNED_FAULT_EXIT; the fault exit is raised by the naked
// SIGBUS handler, never by this code).
const (
	exitUsage    = 64
	exitProtocol = 65
)

// Control-state wire constants (Rust worker/control.rs State). The
// internal/worker package keeps its State words private and publishes
// only the uint32 surface (SetState / WaitFor), so the worker boundary
// repeats the wire values exactly like the package repeats the
// identity and security kinds.
const (
	stateRequest        = 1
	stateWorkerReady    = 2
	stateRunning        = 3
	stateCancelPoll     = 4
	stateFinding        = 5
	stateUnknown        = 6
	stateComplete       = 7
	stateFault          = 8
	stateFailed         = 9
	stateCleanupRequest = 10
	stateCleanupResult  = 11
)

// buildIDLen is the fixed control build-identity width (Rust control.rs
// BUILD_LEN 64; verify_request rejects any other length).
const buildIDLen = 64

// defaultBuildID is the fixed build identity of this tree: the exact
// 64-byte constant of internal/worker buildIDDefault, repeated here
// because the package keeps its var private (the same repetition rule
// as the state words). VerifyRequest compares the control header
// against the package constant, so both must stay identical until a
// build pins a unique identity through the ldflags seam.
const defaultBuildID = "iprange-v4-go-worker-0000000000000000000000000000000000000000000"

// workerBuildID resolves the worker build identity (Rust
// env!("IPRANGE_V4_BUILD_ID"), a build-time environment variable): the
// runtime environment value when set, otherwise the fixed constant.
// The Go ldflags analog pins internal/worker.buildID via -X; while the
// package has no exported setter the header comparison always uses the
// package constant, so the env value only becomes authoritative once a
// build pins the identical value into the package var. A value that
// could never match the 64-byte header identity is refused here before
// any path access.
func workerBuildID() (string, error) {
	value := defaultBuildID
	if env := os.Getenv("IPRANGE_V4_BUILD_ID"); env != "" {
		value = env
	}
	if len(value) != buildIDLen {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "worker build id must be 64 bytes"}
	}
	return value, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run executes the worker main flow (Rust worker.rs run): usage
// refusal outside the exact --control argv, then the version handshake
// (open worker, verify_request, worker pid, WorkerReady, wait Running),
// the SIGBUS handler install at the Rust position, the opcode dispatch,
// and the terminal (Complete with an optional retained cleanup guard,
// or Failed after write_worker_error). Protocol-class failures exit 65;
// a transmitted mode failure still exits 0 because the protocol
// completed (Rust parity).
func run(args []string) int {
	if len(args) != 2 || args[0] != "--control" {
		return exitUsage
	}
	if _, err := workerBuildID(); err != nil {
		return exitProtocol
	}
	control, err := worker.OpenWorker(args[1])
	if err != nil {
		return exitProtocol
	}
	defer control.Close()
	if err := control.VerifyRequest(); err != nil {
		return exitProtocol
	}
	control.SetWorkerPID(uint32(os.Getpid()))
	control.SetState(stateWorkerReady)
	if err := control.WaitFor(stateRunning); err != nil {
		return exitProtocol
	}
	// The alternate signal stack is per-thread and Go migrates
	// goroutines between threads, so the worker pins one OS thread for
	// the whole session before installing the handler (posix.rs install
	// runs on the process's single thread).
	runtime.LockOSThread()
	handler, err := control.InstallHandler()
	if err != nil {
		return exitProtocol
	}
	defer handler.Close()
	// Rust worker.rs Context::enter: the worker session is active for
	// the mode run. In Go the domain machines reach the worker solely
	// through the check hook and the control handle, so the context is
	// implicit and needs no thread-local.
	opcode, ok := control.Opcode()
	if !ok {
		return exitProtocol
	}
	guard, err := runMode(control, opcode)
	if err != nil {
		if writeErr := worker.WriteWorkerError(control, err); writeErr != nil {
			return exitProtocol
		}
		control.SetState(stateFailed)
		return 0
	}
	control.SetGuardPending(guard != nil)
	control.SetState(stateComplete)
	if guard != nil {
		if err := serveCleanup(control, guard); err != nil {
			return exitProtocol
		}
	}
	return 0
}
