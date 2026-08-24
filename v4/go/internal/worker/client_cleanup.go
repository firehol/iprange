//go:build linux && amd64

// Cleanup-mode client arm (Rust worker/cleanup.rs discard:31-82): the
// parent side of one isolated recovery-cleanup session, composed over
// the internal/worker client seam and the 4-11A cleanup codecs. The
// operation order mirrors the Rust authority exactly: the control is
// created, the CleanupRecoveryAttempt opcode recorded, the request
// written, the child spawned and started (Rust discard_inner:48-61),
// and the session driven with no per-mode events; the terminal folds
// the exact classes (discardRecoveryAttempt: pipe Complete with
// guard_pending=false into the decoded result, refuse a guard-pending
// completion with the verbatim Conflict, and fold a mapped fault
// through fault_problem exactly like Rust
// client/recovery.rs fault_problem:525). The Go recovery machine
// creates its own secured output at the request destination, so the
// production recovery arms do not call this arm yet (the from_worker
// constructor stays reserved); the arm is delivered for the Rust
// surface and the wire tests.

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// discardRecoveryAttempt runs one isolated cleanup session and returns
// the discarded-attempt facts and the scratch cleanup (Rust
// worker/cleanup.rs discard): the fallible surface composes the two
// Rust-explicit classes — a guard-pending completion is the verbatim
// Conflict "isolated recovery cleanup retained unexpected authority"
// (cleanup.rs:73-76), and a mapped fault folds through faultProblem
// with the role detail (cleanup.rs:78-79 over recovery.rs:525-534).
// Every earlier failure returns its raw cause; the child is aborted
// whenever a session fails before the complete terminal.
func discardRecoveryAttempt(destinationPath string, output *publication.PrivateOutputAttempt, scratchDirectory *string, scratch *ScratchCheckpoint) (*EarlyDiscard, *ScratchCleanup, error) {
	control, err := CreateParent()
	if err != nil {
		return nil, nil, err
	}
	defer control.Close()
	control.SetOpcode(OpcodeCleanupRecoveryAttempt)
	if err := WriteCleanupRequest(control, destinationPath, output, scratchDirectory, scratch); err != nil {
		return nil, nil, err
	}
	child, err := SpawnWorker(control)
	if err != nil {
		return nil, nil, err
	}
	if err := Handshake(child, control); err != nil {
		child.Abort()
		return nil, nil, err
	}
	control.SetState(stateRunning)
	driven, driveErr := DriveLoop(child, control, nil, "SDK worker emitted an unexpected event",
		func(uint32, *Process, *Control) (bool, error) { return false, nil })
	switch {
	case driveErr != nil:
		child.Abort()
		return nil, nil, driveErr
	case driven.Complete:
		if driven.GuardPending {
			child.Abort()
			return nil, nil, conflict("isolated recovery cleanup retained unexpected authority")
		}
		discarded, cleanup, readErr := ReadCleanupResult(control)
		if readErr != nil {
			return nil, nil, readErr
		}
		return discarded, cleanup, nil
	default:
		// The Drive-loop Fault arm already reaped the child; the fixed
		// Io problem with the role detail is the Rust class.
		return nil, nil, faultProblem(driven.Fault.Role)
	}
}
