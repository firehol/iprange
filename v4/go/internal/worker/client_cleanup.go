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
// client/recovery.rs fault_problem:525). The production recovery arms
// compose the parent-owned attempt facts through
// discardRecoveryAttemptComposed (Rust cleanup.rs discard: the
// isolated session is total - every session failure folds into the
// failed-attempt facts and the checkpoint residue ledger). A
// guard-pending recovery terminal retains the child through
// WorkerCleanup and the routing package builds the recovery
// source-cleanup guard from it (source_cleanup.go FromWorkerCleanup).

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
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

// discardRecoveryAttemptComposed runs the composed recovery-attempt
// discard of the client loop arms (Rust worker/cleanup.rs discard: the
// isolated cleanup session is total - every session failure folds into
// the failed-attempt facts and the checkpoint residue ledger instead
// of surfacing an error, exactly like Rust failed:117-129). The
// scratch checkpoint of an interrupted session is carried through, so
// the cleanup session reports its removal evidence (the Go machine
// never creates authorized scratch, so in practice the checkpoint and
// the cleanup stay nil; the fold keeps the Rust shape for the wire).
func discardRecoveryAttemptComposed(destinationPath string, output *publication.PrivateOutputAttempt, scratchDirectory *string, scratch *ScratchCheckpoint) (*EarlyDiscard, *ScratchCleanup) {
	discarded, cleanup, err := discardRecoveryAttempt(destinationPath, output, scratchDirectory, scratch)
	if err == nil {
		return discarded, cleanup
	}
	problem := problemOf(err)
	facts := WireEarlyDiscardOf(publication.FailedAttemptFacts(output, problem))
	// problemOf always returns a *format.Error (WireProblem.Err), so the
	// assertion is total on this fold.
	return &facts, failedScratchCleanup(scratch, problem.(*format.Error))
}

// failedScratchCleanup builds the scratch-cleanup evidence of a failed
// discard session (Rust worker/cleanup.rs failed: one residue per
// checkpoint entry, each carrying the exact checkpoint basename and
// the fixed problem of the failed session; a nil checkpoint stays
// nil).
func failedScratchCleanup(checkpoint *ScratchCheckpoint, problem error) *ScratchCleanup {
	if checkpoint == nil {
		return nil
	}
	cleanup := &ScratchCleanup{
		AttemptID:                  checkpoint.AttemptID,
		DirectoryIdentity:          checkpoint.DirectoryIdentity,
		CreationSecurityKind:       checkpoint.CreationSecurity.Kind,
		CreationSecurityCommitment: checkpoint.CreationSecurity.Commitment,
	}
	wire := WireProblemOf(problem)
	for _, entry := range checkpoint.Entries {
		cleanup.Residues = append(cleanup.Residues, ScratchResidue{
			Ordinal:                    entry.Ordinal,
			DirectoryIdentity:          checkpoint.DirectoryIdentity,
			Basename:                   checkpointBasename(checkpoint.AttemptID, entry.Ordinal),
			Identity:                   entry.Identity,
			CreationSecurityKind:       checkpoint.CreationSecurity.Kind,
			CreationSecurityCommitment: checkpoint.CreationSecurity.Commitment,
			Problem:                    ScratchProblem{Code: wire.Code, Detail: wire.Detail},
		})
	}
	return cleanup
}

// scratchClean reports whether a discard-session scratch cleanup left
// no residue at all (Rust client scratch_clean: a nil cleanup is
// clean, a present cleanup must prove every artifact absent).
func scratchClean(cleanup *ScratchCleanup) bool {
	return cleanup == nil || cleanup.Clean()
}
