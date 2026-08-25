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
// Rust-parity stance (worker/client/recovery.rs recover_once): the
// parent creates and secures the private output attempt at the
// destination before the request is written, the request carries the
// exact attempt facts, the worker session resumes the owned artifact
// and builds into it, and every interrupted or failed terminal
// discards the attempt through an isolated cleanup session with the
// exact facts (worker/cleanup.rs discard). A guard-pending terminal
// retains the child through WorkerCleanup and the arm returns that
// retained cleanup alongside the outcome; the routing package builds
// the recovery.RecoverySourceCleanupGuard from it (source_cleanup.go
// FromWorkerCleanup). The in-process (non-worker) recovery machines
// keep their own create position and never use this arm.
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
	// output is the parent-owned attempt facts of the session (Rust
	// recover_once facts; nil only on the early arms that never
	// created the attempt).
	output *publication.PrivateOutputAttempt
	// scratch is the session scratch checkpoint in its wire shape,
	// exactly like the Rust RecoveryAttempt arms (the checkpoint
	// travels into the cleanup request; the domain scratch of the
	// failure folds from the discard result).
	scratch *ScratchCheckpoint
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
// recovery source-cleanup guard. The parent creates and secures the
// private output attempt before each request (Rust recover_once
// create + secure arms), so the attempt facts travel the wire and
// every failed terminal discards the owned artifact.
func RecoverWithWorker(sourcePath, destinationPath string, candidate *recovery.RecoveryCandidate, mode WorkerMode, budget *recovery.RecoveryBudget, check Checkpoint, sink recovery.RecoverySink) (*RecoveryOutcome, *WorkerCleanup) {
	// Rust recover(): validate_worker_budget is the first statement,
	// before the retry loop and before any control, spawn, or attempt
	// artifact exists; a refusal reports the early failure with zero
	// destination side effects.
	if failure := recovery.ValidateWorkerBudget(budget, mode == WorkerModeLive); failure != nil {
		return &RecoveryOutcome{Failure: failure}, nil
	}
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
			// Rust recover() Interrupted arm: the publication
			// checkpoint already owned the attempt (the machine ran
			// its failing terminal inside the session), so every
			// other interruption discards the parent-owned attempt and
			// the scratch checkpoint through an isolated cleanup
			// session; a dirty discard is the terminal even for source
			// faults.
			discarded, scratch := discardRecoveryAttemptComposed(destinationPath, attempt.output, scratchDirectoryOf(budget), attempt.scratch)
			if attempt.fault.Role != RoleSource || !discarded.Clean() || !scratchClean(scratch) {
				return &RecoveryOutcome{Failure: discardedRecoveryFailureOf(faultProblem(attempt.fault.Role), recovery.RecoveryReport{}, discarded, scratch)}, nil
			}
			page, pageErr := faultPageOf(&attempt.fault)
			if pageErr != nil {
				return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(pageErr)}, nil
			}
			if err := RecordUnreadablePage(&unreadablePages, page, budget.MaxHeapBytes, "recovery source fault did not advance"); err != nil {
				return &RecoveryOutcome{Failure: earlyRecoveryFailureOf(err)}, nil
			}
		case attemptFailed:
			// Rust recover() Failed arm: the attempt and the scratch
			// checkpoint are discarded before the terminal fold.
			discarded, scratch := discardRecoveryAttemptComposed(destinationPath, attempt.output, scratchDirectoryOf(budget), attempt.scratch)
			return &RecoveryOutcome{Failure: discardedRecoveryFailureOf(problemOf(attempt.cause), recovery.RecoveryReport{}, discarded, scratch)}, nil
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
	// Rust recover_once create + secure arms: the parent creates and
	// secures the private output attempt at the destination before the
	// request is written, so the worker machine resumes the owned
	// artifact and every interrupted or failed terminal discards it
	// with the exact facts.
	created, createFailure := publication.CreatePublishAttempt(destinationPath, publication.PolicyFailIfExists)
	if createFailure != nil {
		child.Abort()
		// Rust recover_once create/secure arms: the folded publication
		// failure carries the exact ledger (no cleanup session exists
		// yet; the source never opened, so no source guard).
		return recoveryAttempt{kind: attemptComplete, outcome: &RecoveryOutcome{Failure: recovery.FromAttemptFailure(createFailure)}}
	}
	facts := created.Facts()
	if err := WriteRecoveryRequest(control, sourcePath, destinationPath, candidate, mode, budget, &facts, unreadablePages, *deliveredUnknowns); err != nil {
		child.Abort()
		// Rust recover_once write_request arm: the created attempt is
		// discarded in-process (no cleanup session exists yet) and the
		// cause folds through source_guard::problem with the fixed
		// detail (recovery/source_guard.rs:306-311).
		discardFacts, artifact := created.DiscardFacts()
		discarded := &EarlyDiscard{Output: discardFacts, Artifact: artifact}
		wire := WireProblemOf(err)
		return recoveryAttempt{kind: attemptComplete, outcome: &RecoveryOutcome{Failure: discardedRecoveryFailureOf(&format.Error{Code: wire.Code, Detail: "recovery source operation failed"}, recovery.RecoveryReport{}, discarded, nil)}}
	}
	// The parent's descriptors are released once the request is sealed
	// (Rust drop(file); drop(attempt)); the worker resumes the
	// artifact from the facts.
	created.Close()
	control.SetState(stateRunning)
	var callback *CallbackDecision
	driven, driveErr := driveRecoveryWorker(child, control, check, sink, deliveredUnknowns, &callback)
	switch {
	case driveErr != nil:
		child.Abort()
		if callback != nil {
			return recoveryCallbackFailureWorker(control, callback, destinationPath, &facts, budget)
		}
		// Rust drive-error arm: a corrupt scratch checkpoint becomes
		// the cause with a nil scratch (client/recovery.rs:263-272);
		// a valid checkpoint keeps the drive cause.
		scratch, scratchErr := scratchCheckpointStrict(control)
		if scratchErr != nil {
			return recoveryAttempt{kind: attemptFailed, cause: scratchErr, output: &facts, scratch: nil}
		}
		return recoveryAttempt{kind: attemptFailed, cause: driveErr, output: &facts, scratch: scratch}
	case driven.Complete:
		outcome, retainedProblem, readErr := ReadRecoveryOutcome(control)
		if readErr != nil {
			child.Abort()
			return recoveryAttempt{kind: attemptFailed, cause: readErr, output: &facts, scratch: scratchCheckpointOf(control)}
		}
		if driven.GuardPending {
			if retainedProblem == nil {
				child.Abort()
				return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker omitted its retained cleanup problem"), output: &facts, scratch: scratchCheckpointOf(control)}
			}
			if outcome.Failure == nil {
				child.Abort()
				return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker retained cleanup after success"), output: &facts, scratch: scratchCheckpointOf(control)}
			}
			outcome.Failure.CoordinationCleanup = publication.CoordinationCleanupCleanupGuard
			retained = true
			return recoveryAttempt{kind: attemptComplete, outcome: outcome, cleanup: NewWorkerCleanup(child, control, retainedProblem)}
		}
		if retainedProblem != nil {
			child.Abort()
			return recoveryAttempt{kind: attemptFailed, cause: conflict("SDK recovery worker reported cleanup without retaining authority"), output: &facts, scratch: scratchCheckpointOf(control)}
		}
		return recoveryAttempt{kind: attemptComplete, outcome: outcome}
	case driven.Fault.Role == RoleSource && callback == nil:
		checkpoint, checkpointErr := recoveryCheckpointOf(control)
		if checkpointErr != nil {
			return recoveryAttempt{kind: attemptFailed, cause: checkpointErr, output: &facts, scratch: nil}
		}
		// Rust recover_once Fault arm: a scratch-checkpoint decode
		// error is a Failed terminal with a nil scratch, never a
		// retried interruption (client/recovery.rs:114-117).
		scratch, scratchErr := scratchCheckpointStrict(control)
		if scratchErr != nil {
			return recoveryAttempt{kind: attemptFailed, cause: scratchErr, output: &facts, scratch: nil}
		}
		return recoveryAttempt{kind: attemptInterrupted, fault: driven.Fault, output: &facts, checkpoint: checkpoint, scratch: scratch}
	default:
		if callback != nil {
			return recoveryCallbackFailureWorker(control, callback, destinationPath, &facts, budget)
		}
		return recoveryAttempt{kind: attemptFailed, cause: mappedWorkerFault(), output: &facts, scratch: scratchCheckpointOf(control)}
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
func recoveryCallbackFailureWorker(control *Control, callback *CallbackDecision, destinationPath string, output *publication.PrivateOutputAttempt, budget *recovery.RecoveryBudget) recoveryAttempt {
	report, err := recoveryCallbackReportOf(control)
	if err != nil {
		return recoveryAttempt{kind: attemptFailed, cause: err, output: output, scratch: scratchCheckpointOf(control)}
	}
	// Rust recovery_callback_failure: a scratch-checkpoint decode
	// error is a Failed terminal with a nil scratch
	// (client/recovery.rs:347-352).
	scratch, scratchErr := scratchCheckpointStrict(control)
	if scratchErr != nil {
		return recoveryAttempt{kind: attemptFailed, cause: scratchErr, output: output, scratch: nil}
	}
	// Rust recovery_callback_failure: the parent-owned attempt is
	// discarded before the terminal fold, exactly like the loop arms.
	discarded, cleanup := discardRecoveryAttemptComposed(destinationPath, output, scratchDirectoryOf(budget), scratch)
	return recoveryAttempt{kind: attemptComplete, outcome: &RecoveryOutcome{Failure: discardedRecoveryFailureOf(problemOf(callback.IntoError()), report, discarded, cleanup)}}
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

// scratchCheckpointOf reads the control scratch checkpoint in its
// wire shape, tolerating an absent or corrupt checkpoint (Rust
// control.scratch_checkpoint().ok().flatten on the recovery arms).
func scratchCheckpointOf(control *Control) *ScratchCheckpoint {
	checkpoint, err := control.ScratchCheckpoint()
	if err != nil || checkpoint == nil {
		return nil
	}
	return checkpoint
}

// scratchCheckpointStrict reads the control scratch checkpoint with
// the decode error surfaced (Rust control.scratch_checkpoint(): the
// recovery Fault and callback-failure arms turn a corrupt checkpoint
// into a Failed terminal instead of tolerating it).
func scratchCheckpointStrict(control *Control) (*ScratchCheckpoint, error) {
	return control.ScratchCheckpoint()
}

// scratchDirectoryOf folds the recovery budget's scratch directory
// into the optional-path shape of the cleanup request (Rust
// budget.scratch_directory.as_deref(); the Go budget stores the empty
// string for the absent directory).
func scratchDirectoryOf(budget *recovery.RecoveryBudget) *string {
	if budget == nil || budget.ScratchDirectory == "" {
		return nil
	}
	value := budget.ScratchDirectory
	return &value
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
	// The role set is closed (Rust fault_problem exhaustive match);
	// the switch keeps the map-free detail selection on the hot path.
	var detail string
	switch role {
	case RoleSource:
		detail = "recovery source mapping faulted"
	case RoleScratch:
		detail = "recovery scratch mapping faulted"
	case RoleOutput:
		detail = "recovery output mapping faulted"
	case RoleCoordination:
		detail = "recovery coordination mapping faulted"
	}
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
	return discardedRecoveryFailureOf(problemOf(cause), recovery.RecoveryReport{}, nil, nil)
}

// discardedRecoveryFailureOf builds one recovery preparation failure
// over the exact discard ledger of an interrupted or failed session
// (Rust RecoveryPreparationFailure::discarded: the early-discard
// facts ride the Output and Cleanup/Housekeeping slots, the
// discard-session scratch cleanup absorbs into the ledger, and no
// source guard exists at these arms).
func discardedRecoveryFailureOf(cause error, report recovery.RecoveryReport, discarded *EarlyDiscard, scratch *ScratchCleanup) *recovery.RecoveryPreparationFailure {
	failure := &recovery.RecoveryPreparationFailure{
		Cause:               cause,
		Report:              report,
		Cleanup:             publication.NewCleanupArtifacts(),
		CoordinationCleanup: publication.CoordinationCleanupNone,
		Housekeeping:        publication.HousekeepingNone,
		SourceCleanup:       nil,
	}
	if discarded != nil {
		failure.Output = &discarded.Output
		if discarded.Artifact != nil {
			failure.Cleanup.Push(*discarded.Artifact)
		}
		failure.Housekeeping = discarded.Housekeeping
		failure.VisibleHousekeeping = discarded.VisibleHousekeeping
	}
	// Rust terminal.rs absorb_scratch over the discard-session scratch
	// cleanup: each residue becomes an AuthorizedScratch artifact, the
	// cleanup's attempt facts ride the failure Scratch slot, and the
	// housekeeping merges into the discarded facts.
	if scratch != nil {
		failure.Scratch = &recovery.RecoveryScratchAttempt{
			AttemptID:         scratch.AttemptID,
			DirectoryIdentity: scratch.DirectoryIdentity,
			CreationSecurity: publication.CreationSecurity{
				Kind:       scratch.CreationSecurityKind,
				Commitment: scratch.CreationSecurityCommitment,
			},
		}
		failure.Housekeeping = failure.Housekeeping.Merge(scratch.Housekeeping)
		failure.VisibleHousekeeping = append(failure.VisibleHousekeeping, scratch.VisibleHousekeeping...)
		for _, residue := range scratch.Residues {
			identity := residue.Identity
			failure.Cleanup.Push(publication.CleanupArtifact{
				Kind:              publication.ArtifactAuthorizedScratch,
				DirectoryRole:     publication.DirectoryRoleScratchDirectory,
				DirectoryIdentity: residue.DirectoryIdentity,
				BasenameEncoding:  1,
				Basename:          append([]byte(nil), residue.Basename...),
				Identity:          &identity,
				CreationSecurity: &publication.CreationSecurity{
					Kind:       residue.CreationSecurityKind,
					Commitment: residue.CreationSecurityCommitment,
				},
				Error: &format.Error{Code: residue.Problem.Code, Detail: residue.Problem.Detail},
			})
		}
	}
	return failure
}
