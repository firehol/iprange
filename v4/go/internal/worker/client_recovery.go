//go:build linux && amd64

// Recovery-mode client arm (Rust worker/client/recovery.rs): the
// parent side of one worker recovery session, composed over the
// internal/worker client seam and the 4-11A recovery codecs. Every
// operation order, response wiring, and error class mirrors the Rust
// authority: the control is created and the child spawned and
// handshaked before the request is written (Rust recover_once order),
// the running state is published only after the request bytes are
// sealed, unknown-damage envelopes stream through the drive hook into
// the sink with the callback acknowledge seam, and the outcome mailbox
// folds the exact classes and the retained publication problem.
//
// Recorded Go stances: the client does not create the destination
// output attempt (the Go recovery machine creates its own secured
// output at the request destination; the request carries the zero
// attempt facts), so a guard-pending terminal retains the child
// through WorkerCleanup and the arm returns that retained cleanup
// alongside the outcome; the routing package builds the
// recovery.RecoverySourceCleanupGuard from it (source_cleanup.go
// FromWorkerCleanup). The parent-side discardRecoveryAttempt cleanup
// arm exists (client_cleanup.go) but the production recovery arms do
// not compose it: the parent owns no attempt facts (the Go machine
// creates its own output).
package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
)

// recoveryAttempt is one single-session recovery outcome (Rust
// client/recovery.rs RecoveryAttempt).
type recoveryAttempt struct {
	kind       recoveryAttemptKind
	outcome    *RecoveryOutcome
	cleanup    *WorkerCleanup
	cause      error
	fault      FaultRecord
	checkpoint *RecoveryOutcome
	scratch    *recovery.RecoveryScratchAttempt
}

// recoveryAttemptKind selects the terminal of one recovery session
// (Rust RecoveryAttempt variants).
type recoveryAttemptKind uint8

const (
	attemptComplete recoveryAttemptKind = iota
	attemptEarly
	attemptInterrupted
	attemptFailed
)

// RecoverWithWorker runs one worker recovery, restarting after every
// source mapped fault with the page recorded unreadable (Rust
// client/recovery.rs recover; the facade routing package composes
// this arm after the public nil-budget refusal). The outcome mirrors
// the Rust RecoveryOutcome; a guard-pending completion attaches the
// retained WorkerCleanup to the failure's coordination class and
// returns it as the second value so the routing package can build the
// recovery source-cleanup guard. The Go machine creates its own
// output attempt, so the parent owns no attempt facts.
func RecoverWithWorker(sourcePath, destinationPath string, candidate *recovery.RecoveryCandidate, mode WorkerMode, budget *recovery.RecoveryBudget, check Checkpoint, sink recovery.RecoverySink) (*RecoveryOutcome, *WorkerCleanup) {
	var unreadablePages []uint32
	var deliveredUnknowns uint64
	for {
		if err := checkpointCall(check); err != nil {
			return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(err)}, nil
		}
		attempt := recoverOnceWorker(sourcePath, destinationPath, candidate, mode, budget, check, sink, unreadablePages, &deliveredUnknowns)
		switch attempt.kind {
		case attemptComplete:
			return attempt.outcome, attempt.cleanup
		case attemptEarly:
			return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(attempt.cause)}, nil
		case attemptInterrupted:
			if attempt.checkpoint != nil {
				if attempt.fault.Role != RoleOutput {
					return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(conflict("recovery publication checkpoint accompanied a non-output fault"))}, nil
				}
				applyFaultToCheckpoint(attempt.checkpoint, attempt.fault)
				return attempt.checkpoint, nil
			}
			// The parent owns no output attempt (recorded Go stance),
			// so the Rust discard arm is trivially clean; the
			// publication discard arms compose discardRecoveryAttempt
			// once a parent-owned attempt exists.
			if attempt.fault.Role != RoleSource {
				return &RecoveryOutcome{Failure: discardedRecoveryFailureOf(faultProblem(attempt.fault.Role), recovery.RecoveryReport{}, attempt.scratch)}, nil
			}
			page, pageErr := faultPageOf(&attempt.fault)
			if pageErr != nil {
				return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(pageErr)}, nil
			}
			if err := RecordUnreadablePage(&unreadablePages, page, budget.MaxHeapBytes, "recovery source fault did not advance"); err != nil {
				return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(err)}, nil
			}
		case attemptFailed:
			return &RecoveryOutcome{Failure: discardedRecoveryFailureOf(problemOf(attempt.cause), recovery.RecoveryReport{}, attempt.scratch)}, nil
		}
	}
}

// recoverOnceWorker runs one recovery session (Rust recover_once): the
// child is spawned and handshaked before the request is written, the
// running state is published after the sealed request, and the
// terminal folds the outcome mailbox with the retained-cleanup cross
// checks.
func recoverOnceWorker(sourcePath, destinationPath string, candidate *recovery.RecoveryCandidate, mode WorkerMode, budget *recovery.RecoveryBudget, check Checkpoint, sink recovery.RecoverySink, unreadablePages []uint32, deliveredUnknowns *uint64) recoveryAttempt {
	control, err := CreateParent()
	if err != nil {
		return recoveryAttempt{kind: attemptEarly, cause: err}
	}
	retained := false
	defer func() {
		if !retained {
			control.Close()
		}
	}()
	control.SetOpcode(OpcodeRecover)
	control.SetExternalPoll(RequiresExternalPoll(check))
	child, err := SpawnWorker(control)
	if err != nil {
		return recoveryAttempt{kind: attemptEarly, cause: err}
	}
	if err := Handshake(child, control); err != nil {
		child.Abort()
		return recoveryAttempt{kind: attemptEarly, cause: err}
	}
	// The request carries the zero attempt facts; the worker machine
	// creates its own secured output at the destination (recorded Go
	// stance, Rust recover_once create + secure arms).
	if err := WriteRecoveryRequest(control, sourcePath, destinationPath, candidate, mode, budget, &publication.PrivateOutputAttempt{}, unreadablePages, *deliveredUnknowns); err != nil {
		child.Abort()
		return recoveryAttempt{kind: attemptComplete, outcome: &RecoveryOutcome{Failure: discardedRecoveryFailureOf(problemOf(err), recovery.RecoveryReport{}, nil)}}
	}
	control.SetState(stateRunning)
	var callback *CallbackDecision
	driven, driveErr := driveRecoveryWorker(child, control, check, sink, deliveredUnknowns, &callback)
	switch {
	case driveErr != nil:
		child.Abort()
		if callback != nil {
			return recoveryCallbackFailureWorker(control, callback)
		}
		return recoveryAttempt{kind: attemptFailed, cause: driveErr, scratch: scratchCheckpointOf(control)}
	case driven.Complete:
		outcome, retainedProblem, readErr := ReadRecoveryOutcome(control)
		if readErr != nil {
			child.Abort()
			return recoveryAttempt{kind: attemptFailed, cause: readErr, scratch: scratchCheckpointOf(control)}
		}
		if driven.GuardPending {
			if retainedProblem == nil {
				child.Abort()
				return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker omitted its retained cleanup problem"), scratch: scratchCheckpointOf(control)}
			}
			if outcome.Failure == nil {
				child.Abort()
				return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker retained cleanup after success"), scratch: scratchCheckpointOf(control)}
			}
			outcome.Failure.CoordinationCleanup = publication.CoordinationCleanupCleanupGuard
			retained = true
			return recoveryAttempt{kind: attemptComplete, outcome: outcome, cleanup: NewWorkerCleanup(child, control, retainedProblem)}
		}
		if retainedProblem != nil {
			child.Abort()
			return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker reported cleanup without retaining authority"), scratch: scratchCheckpointOf(control)}
		}
		return recoveryAttempt{kind: attemptComplete, outcome: outcome}
	case driven.Fault.Role == RoleSource && callback == nil:
		checkpoint, checkpointErr := recoveryCheckpointOf(control)
		if checkpointErr != nil {
			return recoveryAttempt{kind: attemptFailed, cause: checkpointErr, scratch: nil}
		}
		// Rust recover_once Fault arm: a scratch-checkpoint decode
		// error is a Failed terminal with a nil scratch, never a
		// retried interruption (client/recovery.rs:114-117).
		scratch, scratchErr := scratchCheckpointStrict(control)
		if scratchErr != nil {
			return recoveryAttempt{kind: attemptFailed, cause: scratchErr, scratch: nil}
		}
		return recoveryAttempt{kind: attemptInterrupted, fault: driven.Fault, checkpoint: checkpoint, scratch: scratch}
	default:
		if callback != nil {
			return recoveryCallbackFailureWorker(control, callback)
		}
		return recoveryAttempt{kind: attemptFailed, cause: mappedWorkerFault(), scratch: scratchCheckpointOf(control)}
	}
}

// driveRecoveryWorker drives one recovery session with the Unknown
// state hook (Rust drive_recovery): each unknown-damage envelope is
// decoded, its sequence advanced, delivered to the sink, and
// acknowledged through the callback seam.
func driveRecoveryWorker(child *Process, control *Control, check Checkpoint, sink recovery.RecoverySink, deliveredUnknowns *uint64, callback **CallbackDecision) (*Drive, error) {
	return DriveLoop(child, control, check, "SDK worker emitted an unexpected event",
		func(state uint32, child *Process, control *Control) (bool, error) {
			if state != stateUnknown {
				return false, nil
			}
			unknown, err := ReadRecoveryUnknown(control)
			if err != nil {
				return false, err
			}
			if err := AdvanceSequence(child, deliveredUnknowns, unknown.Sequence, "worker recovery envelope sequence is invalid"); err != nil {
				return false, err
			}
			var cause error
			result := false
			if sink != nil {
				value, sinkErr := sink.Unknown(unknown)
				if sinkErr != nil {
					cause = sinkErr
				} else {
					result = value == recovery.RecoverySinkStop
				}
			}
			decision, err := AcknowledgeCallback(control, result, cause)
			if err != nil {
				return false, err
			}
			if decision != nil {
				*callback = decision
			}
			return true, nil
		})
}

// recoveryCallbackFailureWorker folds a terminal callback decision
// into the recovery failure of an interrupted session (Rust
// recovery_callback_failure): the sealed recovery-report checkpoint is
// required, and the cause is the decision's error surface.
func recoveryCallbackFailureWorker(control *Control, callback *CallbackDecision) recoveryAttempt {
	report, err := recoveryCallbackReportOf(control)
	if err != nil {
		return recoveryAttempt{kind: attemptFailed, cause: err, scratch: scratchCheckpointOf(control)}
	}
	// Rust recovery_callback_failure: a scratch-checkpoint decode
	// error is a Failed terminal with a nil scratch
	// (client/recovery.rs:347-352).
	scratch, scratchErr := scratchCheckpointStrict(control)
	if scratchErr != nil {
		return recoveryAttempt{kind: attemptFailed, cause: scratchErr, scratch: nil}
	}
	return recoveryAttempt{kind: attemptComplete, outcome: &RecoveryOutcome{Failure: discardedRecoveryFailureOf(problemOf(callback.IntoError()), report, scratch)}}
}

// recoveryCheckpointOf reads the sealed recovery publication checkpoint
// (Rust read_recovery_checkpoint): nil with no error when no checkpoint
// was sealed; a retained problem in a sealed checkpoint is the recorded
// Conflict.
func recoveryCheckpointOf(control *Control) (*RecoveryOutcome, error) {
	if !control.RecoveryCheckpointSealed() {
		return nil, nil
	}
	outcome, retained, err := ReadRecoveryOutcome(control)
	if err != nil {
		return nil, err
	}
	if retained != nil {
		return nil, conflict("recovery publication checkpoint retained unexpected cleanup authority")
	}
	return outcome, nil
}

// recoveryCallbackReportOf reads the sealed recovery-report callback
// payload (Rust read_recovery_callback_report).
func recoveryCallbackReportOf(control *Control) (recovery.RecoveryReport, error) {
	kind, ok := control.CallbackCheckpoint()
	if !ok || kind != CallbackRecoveryReport {
		return recovery.RecoveryReport{}, conflict("worker recovery callback checkpoint is missing")
	}
	r, err := NewWireCallbackReader(control)
	if err != nil {
		return recovery.RecoveryReport{}, err
	}
	report, err := readRecoveryReport(r)
	if err != nil {
		return recovery.RecoveryReport{}, err
	}
	if err := r.Finish(); err != nil {
		return recovery.RecoveryReport{}, err
	}
	return report, nil
}

// scratchCheckpointOf reads the control scratch checkpoint, tolerating
// an absent checkpoint (Rust control.scratch_checkpoint().ok().flatten
// on the recovery arm).
func scratchCheckpointOf(control *Control) *recovery.RecoveryScratchAttempt {
	checkpoint, err := control.ScratchCheckpoint()
	if err != nil || checkpoint == nil {
		return nil
	}
	return &recovery.RecoveryScratchAttempt{
		AttemptID:         checkpoint.AttemptID,
		DirectoryIdentity: checkpoint.DirectoryIdentity,
		CreationSecurity:  checkpoint.CreationSecurity,
	}
}

// scratchCheckpointStrict reads the control scratch checkpoint with
// the decode error surfaced (Rust control.scratch_checkpoint(): the
// recovery Fault and callback-failure arms turn a corrupt checkpoint
// into a Failed terminal instead of tolerating it).
func scratchCheckpointStrict(control *Control) (*recovery.RecoveryScratchAttempt, error) {
	checkpoint, err := control.ScratchCheckpoint()
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, nil
	}
	return &recovery.RecoveryScratchAttempt{
		AttemptID:         checkpoint.AttemptID,
		DirectoryIdentity: checkpoint.DirectoryIdentity,
		CreationSecurity:  checkpoint.CreationSecurity,
	}, nil
}

// applyFaultToCheckpoint folds the fault problem into a publication
// checkpoint outcome (Rust apply_fault_to_checkpoint).
func applyFaultToCheckpoint(outcome *RecoveryOutcome, fault FaultRecord) {
	problem := faultProblem(fault.Role)
	if outcome.Result != nil {
		outcome.Result.Publication.Cause = problem
	} else if outcome.Failure != nil {
		outcome.Failure.Cause = problem
	}
}

// faultProblem is the fixed Io publication problem of one mapped fault
// role (Rust client/recovery.rs fault_problem with the verbatim role
// details).
func faultProblem(role MappingRole) error {
	detail := map[MappingRole]string{
		RoleSource:       "recovery source mapping faulted",
		RoleScratch:      "recovery scratch mapping faulted",
		RoleOutput:       "recovery output mapping faulted",
		RoleCoordination: "recovery coordination mapping faulted",
	}[role]
	return &format.Error{Code: format.CodeIO, Detail: detail}
}

// problemOf folds one cause into its fixed problem (Rust
// source_guard::problem; the worker boundary form keeps the code and
// detail and drops the errno like every Go arm).
func problemOf(cause error) error {
	return WireProblemOf(cause).Err()
}

// earlyRecoveryFailureOf builds the fixed early recovery failure (Rust
// RecoveryPreparationFailure::early: the fixed problem and the empty
// facts).
func earlyRecoveryFailureOf(cause error) *recovery.RecoveryPreparationFailure {
	return discardedRecoveryFailureOf(problemOf(cause), recovery.RecoveryReport{}, nil)
}

// discardedRecoveryFailureOf builds one recovery preparation failure
// with the given report and scratch and the clean discard ledger (Rust
// RecoveryPreparationFailure::discarded over the worker discard arms).
func discardedRecoveryFailureOf(cause error, report recovery.RecoveryReport, scratch *recovery.RecoveryScratchAttempt) *recovery.RecoveryPreparationFailure {
	return &recovery.RecoveryPreparationFailure{
		Cause:               cause,
		Report:              report,
		Scratch:             scratch,
		Cleanup:             publication.NewCleanupArtifacts(),
		CoordinationCleanup: publication.CoordinationCleanupNone,
		Housekeeping:        publication.HousekeepingNone,
		VisibleHousekeeping: nil,
		Output:              nil,
		SourceCleanup:       nil,
	}
}
