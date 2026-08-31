//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

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
// the naked SIGBUS handler (internal/worker sigbus_* asm) or the
// Windows vectored exception handler, without unwinding through Go. The worker build identity is pinned by
// SetBuildID before the header verify: IPRANGE_V4_BUILD_ID when set
// (64 bytes), otherwise the fixed BuildIDDefault; a mismatched worker
// exits 65 before WorkerReady.

package main

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/worker"
)

// Worker exit codes (Rust worker.rs EXIT_USAGE / EXIT_PROTOCOL and
// control.rs OWNED_FAULT_EXIT; the fault exit is raised by the naked
// SIGBUS handler, never by this code).
const (
	exitUsage    = 64
	exitProtocol = 65
)

// workerBuildID resolves the worker build identity (Rust
// env!("IPRANGE_V4_BUILD_ID"), a build-time environment variable): the
// runtime environment value when set, otherwise the fixed
// BuildIDDefault. run() then pins the resolved value with
// worker.SetBuildID before any control access, so the env seam is
// authoritative for the header verify.
func workerBuildID() string {
	value := worker.BuildIDDefault
	if env := os.Getenv("IPRANGE_V4_BUILD_ID"); env != "" {
		value = env
	}
	return value
}

func main() {
	// startWorkerProfile is the v4work-only bench hook (see
	// phases_v4work.go); production builds carry no profiling
	// machinery.
	stop := startWorkerProfile()
	code := run(os.Args[1:])
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// run executes the worker main flow (Rust worker.rs run): usage
// refusal outside the exact --control argv, then the version handshake
// (open worker, verify_request, worker pid, WorkerReady, wait Running),
// the SIGBUS handler install at the Rust position, the opcode dispatch,
// and the terminal (Complete with an optional retained cleanup guard,
// or Failed after write_worker_error). Protocol-class failures exit 65;
// a transmitted mode failure still exits 0 because the protocol
// completed (Rust parity).
// workerPhases reports the worker-side phase timings of one
// validation/recovery run (bench-only tooling; SOW-0027 direction item
// 3). Set IPRANGE_WORKER_PHASES to a file path to append
// "<name> <nanoseconds-since-process-start>" rows for the control
// handshake (verify, ready, running), the fault-handler install, and
// the mode dispatch while the worker runs. The parent spawns the worker
// with null stdio, so the rows go to the file, never stdout/stderr.
func run(args []string) int {
	marker := newPhaseMarker() // v4work-only bench observability
	if len(args) != 2 || args[0] != "--control" {
		return exitUsage
	}
	if err := worker.SetBuildID(workerBuildID()); err != nil {
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
	marker.mark("verify")
	control.SetWorkerPID(uint32(os.Getpid()))
	control.SetState(worker.StateWorkerReady)
	marker.mark("ready")
	if err := control.WaitFor(worker.StateRunning); err != nil {
		return exitProtocol
	}
	marker.mark("running")
	handler, err := installFaultHandler(control)
	if err != nil {
		return exitProtocol
	}
	defer handler.Close()
	marker.mark("handler")
	// Rust worker.rs Context::enter (worker.rs:155-183): the worker
	// session is active for the mode run. EnterSession publishes the
	// control's ProbeRegion as the mapping session probe hook, so
	// every domain-machine probe (validation sweeps, recovery source
	// guards, output writes, sidecar ops) arms its region on this
	// control; a real mapped fault inside an armed region becomes an
	// owned fault record instead of chaining. The session context
	// drops when the mode run returns, exactly like the Rust Context.
	if err := worker.EnterSession(control); err != nil {
		return exitProtocol
	}
	defer worker.LeaveSession()
	opcode, ok := control.Opcode()
	if !ok {
		return exitProtocol
	}
	marker.mark("dispatch")
	guard, err := runMode(control, opcode)
	marker.mark("mode")
	if err != nil {
		if writeErr := worker.WriteWorkerError(control, err); writeErr != nil {
			return exitProtocol
		}
		control.SetState(worker.StateFailed)
		return 0
	}
	control.SetGuardPending(guard != nil)
	control.SetState(worker.StateComplete)
	if guard != nil {
		if err := serveCleanup(control, guard); err != nil {
			return exitProtocol
		}
	}
	marker.mark("total")
	return 0
}
