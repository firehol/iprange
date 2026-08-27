//go:build linux || darwin || freebsd || windows

// Validation-mode client arms (Rust worker/client/validation.rs): the
// parent side of one worker validation or recovery-candidate
// inspection session, composed over the internal/worker client seam
// (spawn/handshake/drive) and the 4-11A wire codecs. Every operation
// order, response wiring, and error class mirrors the Rust authority:
// the request is written before the spawn (validation) or after the
// handshake (inspection), streamed findings run through the drive hook
// into the sink with the callback acknowledge seam, the result mailbox
// folds the exact classes and the retained publication problem, and a
// guard-pending terminal retains the child through WorkerCleanup. A
// source mapped fault returns the fault facts to the retry loop, which
// records the unreadable page and restarts; a non-source fault is the
// fixed Io worker-operation class. Results and failures use the 4-11A
// wire shapes because the Go validation package keeps its progress
// counter arrays private (the recorded 4-11A stance).
package worker

import (
	"math"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// validationAttempt is one single-session validation outcome (Rust
// client/validation.rs ValidationAttempt): a completed result or
// failure with the optional terminal callback decision, or the
// validated fault record of a source mapped fault.
type validationAttempt struct {
	complete bool
	result   *ValidationResultWire
	failure  *ValidationFailureWire
	callback *CallbackDecision
	fault    *FaultRecord
}

// ValidateWithWorker runs one explicit worker validation, restarting
// after every source mapped fault with the page recorded unreadable
// (Rust client/validation.rs validate + validate_all). The facade
// routing package composes this arm after the public preflight; the
// returned pair mirrors Result<ValidationResult, ValidationFailure>
// in the 4-11A wire shapes: exactly one of result and failure is
// non-nil, and a drive-level error folds into the zero-progress
// failure exactly
// like the Rust wrapper.
func ValidateWithWorker(path string, mode validation.ValidationMode, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check Checkpoint, sink validation.ValidationSink) (*ValidationResultWire, *ValidationFailureWire) {
	attempt, err := validateAllWorker(path, mode, candidate, budget, check, sink)
	if err != nil {
		return nil, &ValidationFailureWire{
			Cause:               err,
			Progress:            ProgressWire{},
			Cleanup:             publication.NewCleanupArtifacts(),
			CoordinationCleanup: publication.CoordinationCleanupNone,
		}
	}
	return attempt.result, attempt.failure
}

// validateAllWorker runs the fault-restartable validation loop (Rust
// validate_all): every source fault is converted to its page, recorded
// into the unreadable-page list under the heap budget, and the session
// restarts; a repeated page is the recorded Conflict and a non-source
// fault the fixed Io worker-operation class.
func validateAllWorker(path string, mode validation.ValidationMode, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check Checkpoint, sink validation.ValidationSink) (validationAttempt, error) {
	var unreadablePages []uint32
	var deliveredFindings uint64
	for {
		if err := checkpointCall(check); err != nil {
			return validationAttempt{}, err
		}
		attempt, err := validateOnceWorker(path, mode, candidate, budget, check, sink, unreadablePages, &deliveredFindings)
		if err != nil {
			return validationAttempt{}, err
		}
		if attempt.complete {
			if attempt.callback != nil {
				switch {
				case attempt.failure != nil:
					attempt.failure.Cause = attempt.callback.IntoError()
				case attempt.result != nil:
					return validationAttempt{}, conflict("worker ignored a terminal validation callback")
				}
			}
			return attempt, nil
		}
		if attempt.fault == nil {
			return validationAttempt{}, conflict("worker validation attempt lacks a terminal")
		}
		if attempt.fault.Role != RoleSource {
			return validationAttempt{}, mappedWorkerFault()
		}
		page, pageErr := faultPageOf(attempt.fault)
		if pageErr != nil {
			return validationAttempt{}, pageErr
		}
		if err := RecordUnreadablePage(&unreadablePages, page, budget.MaxHeapBytes, "validation fault did not advance"); err != nil {
			return validationAttempt{}, err
		}
	}
}

// validateOnceWorker runs one validation session (Rust
// validate_once): create the control, write the request before the
// spawn, start, drive with the finding hook, and fold the terminal.
// A guard-pending completion retains the child and the control in a
// WorkerCleanup attached to the failure's source-cleanup slot (the Go
// any field of the 4-11A wire shape).
func validateOnceWorker(path string, mode validation.ValidationMode, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check Checkpoint, sink validation.ValidationSink, unreadablePages []uint32, deliveredFindings *uint64) (validationAttempt, error) {
	control, err := CreateParent()
	if err != nil {
		return validationAttempt{}, err
	}
	retained := false
	defer func() {
		if !retained {
			control.Close()
		}
	}()
	control.SetOpcode(OpcodeValidate)
	control.SetExternalPoll(RequiresExternalPoll(check))
	if err := WriteValidationRequest(control, path, mode, candidate, budget, unreadablePages, *deliveredFindings); err != nil {
		return validationAttempt{}, err
	}
	child, err := SpawnWorker(control)
	if err != nil {
		return validationAttempt{}, err
	}
	if err := StartWorker(child, control); err != nil {
		child.Abort()
		return validationAttempt{}, err
	}
	var callback *CallbackDecision
	driven, err := driveValidationWorker(child, control, check, sink, deliveredFindings, &callback)
	if err != nil {
		child.Abort()
		if callback != nil {
			return validationCallbackFailureWorker(control, callback)
		}
		return validationAttempt{}, err
	}
	if driven.Complete {
		result, failure, retainedProblem := ReadValidationResult(control)
		if driven.GuardPending {
			if retainedProblem == nil {
				child.Abort()
				return validationAttempt{}, conflict("SDK worker omitted its retained cleanup problem")
			}
			if failure == nil || failure.SourceCleanup != nil {
				child.Abort()
				return validationAttempt{}, conflict("SDK worker retained cleanup after a successful operation")
			}
			failure.SourceCleanup = NewWorkerCleanup(child, control, retainedProblem)
			failure.CoordinationCleanup = publication.CoordinationCleanupCleanupGuard
			retained = true
			return validationAttempt{complete: true, failure: failure, callback: callback}, nil
		}
		if retainedProblem != nil {
			child.Abort()
			return validationAttempt{}, conflict("SDK worker reported cleanup without retaining authority")
		}
		return validationAttempt{complete: true, result: result, failure: failure, callback: callback}, nil
	}
	if callback != nil {
		return validationCallbackFailureWorker(control, callback)
	}
	if driven.Fault.Role != RoleSource {
		progress, progressErr := ReadValidationProgress(control)
		if progressErr != nil {
			return validationAttempt{}, progressErr
		}
		var wire ProgressWire
		if progress != nil {
			wire = *progress
		}
		return validationAttempt{complete: true, failure: &ValidationFailureWire{
			Cause:               mappedWorkerFault(),
			Progress:            wire,
			Cleanup:             publication.NewCleanupArtifacts(),
			CoordinationCleanup: publication.CoordinationCleanupNone,
		}}, nil
	}
	return validationAttempt{complete: false, fault: &driven.Fault}, nil
}

// driveValidationWorker drives one validation session with the
// Finding state hook (Rust drive_validation): each finding envelope is
// decoded, its sequence advanced, delivered to the sink, and
// acknowledged through the callback seam; the acknowledgement records
// the terminal callback decision.
func driveValidationWorker(child *Process, control *Control, check Checkpoint, sink validation.ValidationSink, deliveredFindings *uint64, callback **CallbackDecision) (*Drive, error) {
	return DriveLoop(child, control, check, "SDK worker emitted an unexpected recovery event",
		func(state uint32, child *Process, control *Control) (bool, error) {
			if state != stateFinding {
				return false, nil
			}
			finding, err := ReadValidationFinding(control)
			if err != nil {
				return false, err
			}
			if err := AdvanceSequence(child, deliveredFindings, finding.Sequence, "worker validation finding sequence is invalid"); err != nil {
				return false, err
			}
			var cause error
			result := false
			if sink != nil {
				value, sinkErr := sink.Finding(finding)
				if sinkErr != nil {
					cause = sinkErr
				} else {
					result = value == validation.SinkStop
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

// validationCallbackFailureWorker folds a terminal callback decision
// into the validation failure of an interrupted session (Rust
// validation_callback_failure): the sealed progress checkpoint is
// required, and the cause is the decision's error surface.
func validationCallbackFailureWorker(control *Control, callback *CallbackDecision) (validationAttempt, error) {
	progress, err := ReadValidationProgress(control)
	if err != nil {
		return validationAttempt{}, err
	}
	if progress == nil {
		return validationAttempt{}, conflict("worker validation callback checkpoint is missing")
	}
	return validationAttempt{complete: true, failure: &ValidationFailureWire{
		Cause:               callback.IntoError(),
		Progress:            *progress,
		Cleanup:             publication.NewCleanupArtifacts(),
		CoordinationCleanup: publication.CoordinationCleanupNone,
	}}, nil
}

// inspectionAttempt is one single-session inspection outcome (Rust
// client/validation.rs InspectionAttempt): the completed inspection
// (facts or an encoded failure) or the validated fault record.
type inspectionAttempt struct {
	complete bool
	value    *InspectionWire
	err      error
	fault    *FaultRecord
}

// InspectRecoveryCandidatesWithWorker runs one worker
// recovery-candidate inspection, restarting after every source mapped
// meta fault with the page recorded unreadable (Rust
// inspect_recovery_candidates; the facade routing package composes
// this arm after the inspection preflight). Inspection maps at most
// the two meta pages, so a fault page at or above 2 is the recorded
// Conflict.
func InspectRecoveryCandidatesWithWorker(path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, check Checkpoint) (*InspectionWire, error) {
	var unreadablePages []uint32
	for {
		if err := checkpointCall(check); err != nil {
			return nil, err
		}
		attempt, err := inspectOnceWorker(path, mode, budget, check, unreadablePages)
		if err != nil {
			return nil, err
		}
		if attempt.complete {
			return attempt.value, attempt.err
		}
		if attempt.fault == nil {
			return nil, conflict("worker inspection attempt lacks a terminal")
		}
		if attempt.fault.Role != RoleSource {
			return nil, mappedWorkerFault()
		}
		page, pageErr := faultPageOf(attempt.fault)
		if pageErr != nil {
			return nil, pageErr
		}
		if page >= 2 {
			return nil, conflict("candidate inspection fault did not advance")
		}
		if err := RecordUnreadablePage(&unreadablePages, page, budget.MaxHeapBytes, "candidate inspection fault did not advance"); err != nil {
			return nil, err
		}
	}
}

// inspectOnceWorker runs one inspection session (Rust inspect_once):
// the request is written before the spawn which the Rust authority
// orders identically for inspection, and a guard-pending terminal is
// the recorded Conflict because inspection retains no cleanup.
func inspectOnceWorker(path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, check Checkpoint, unreadablePages []uint32) (inspectionAttempt, error) {
	control, err := CreateParent()
	if err != nil {
		return inspectionAttempt{}, err
	}
	defer control.Close()
	control.SetOpcode(OpcodeInspectRecoveryCandidates)
	control.SetExternalPoll(RequiresExternalPoll(check))
	if err := WriteInspectionRequest(control, path, mode, budget, unreadablePages); err != nil {
		return inspectionAttempt{}, err
	}
	child, err := SpawnWorker(control)
	if err != nil {
		return inspectionAttempt{}, err
	}
	if err := StartWorker(child, control); err != nil {
		child.Abort()
		return inspectionAttempt{}, err
	}
	driven, err := DriveWorker(child, control, check)
	if err != nil {
		child.Abort()
		return inspectionAttempt{}, err
	}
	if driven.Complete {
		if driven.GuardPending {
			child.Abort()
			return inspectionAttempt{}, conflict("candidate inspection retained unexpected cleanup authority")
		}
		value, readErr := ReadInspectionResult(control)
		return inspectionAttempt{complete: true, value: value, err: readErr}, nil
	}
	return inspectionAttempt{complete: false, fault: &driven.Fault}, nil
}

// mappedWorkerFault is the fixed fault class of every non-source
// mapped fault (Rust mapped_worker_fault: the Io worker operation
// class with no errno).
func mappedWorkerFault() error {
	return &WireError{Code: format.CodeIO}
}

// faultPageOf converts one fault record's relative offset to its page
// (Rust the retry arms: fault.relative / PAGE_SIZE with the overflow
// class).
func faultPageOf(fault *FaultRecord) (uint32, error) {
	page := fault.Relative / uint64(format.PageSize)
	if page > math.MaxUint32 {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "worker fault page"}
	}
	return uint32(page), nil
}

// CheckpointCall runs one bounded-work checkpoint (Rust
// CancellationToken::check; a nil checkpoint never cancels).
func checkpointCall(check Checkpoint) error {
	if check == nil {
		return nil
	}
	return check()
}
