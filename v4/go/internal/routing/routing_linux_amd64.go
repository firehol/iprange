//go:build linux && amd64

// Facade routing on the worker-supported platform (Rust validation.rs
// validate -> worker::validate and recovery/api.rs recover_* ->
// worker::recover): every public entry runs the Rust preflight and
// then routes through the isolated worker client arms; the in-process
// machines stay the worker-side engines and the non-linux path
// (routing_other.go). The worker arms return the 4-11A wire shapes,
// so this package converts them back to the domain types the
// in-process machines return; the exported additions to the
// validation and recovery packages are the worker-entry seams
// (Preflight, ValidateWorkerBudget, PreflightInspection,
// FromWorkerCleanup) and the wire-to-domain conversion constructors.
// There is no silent in-process fallback here: an exhausted or empty
// worker candidate list surfaces the Rust Io or Unsupported class
// through the arms.

package routing

import (
	"errors"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// Validate runs one explicit validation through the worker client arm
// (Rust validation::validate: the preflight runs first, then
// worker::validate). The preflight failure keeps the exact in-process
// shape (zero progress and the clean ledger).
func Validate(path string, mode validation.ValidationMode, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	if err := validation.Preflight(mode, budget, check); err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(err, &progress)
	}
	result, failure := worker.ValidateWithWorker(path, mode, nil, budget, check, sink)
	return validationResultOf(result), validationFailureOf(failure)
}

// ValidateOfflineCandidate validates one retained recovery candidate
// through the worker client arm (Rust validate_offline over the
// OfflineCandidate mode; the Go mode enum cannot carry the candidate
// payload, so the arm is entered here). The offline preflight and the
// nil-candidate refusal mirror the in-process machine.
func ValidateOfflineCandidate(path string, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	if err := validation.Preflight(validation.ValidationModeOfflineCandidate, budget, check); err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(err, &progress)
	}
	if candidate == nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(&format.Error{Code: format.CodeInvalidArgument, Detail: "offline-candidate validation requires a candidate token"}, &progress)
	}
	result, failure := worker.ValidateWithWorker(path, validation.ValidationModeOfflineCandidate, candidate, budget, check, sink)
	return validationResultOf(result), validationFailureOf(failure)
}

// InspectRecoveryCandidates runs one worker recovery-candidate
// inspection (Rust inspect_recovery_candidates: the preflight runs
// first, then the worker client).
func InspectRecoveryCandidates(path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, check func() error) (*recovery.RecoveryCandidateInspection, error) {
	if err := recovery.PreflightInspection(mode, budget, check); err != nil {
		return nil, err
	}
	wire, err := worker.InspectRecoveryCandidatesWithWorker(path, mode, budget, check)
	if err != nil {
		return nil, wireCauseToDomain(err)
	}
	return inspectionOf(wire), nil
}

// RecoverImmutable runs one bounded worker recovery of the exact
// candidate (Rust recover_immutable -> worker::recover under the
// immutable machine).
func RecoverImmutable(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recoverRouted(sourcePath, candidate, destinationPath, worker.WorkerModeImmutable, budget, check, sink)
}

// RecoverOffline runs one quiescent worker recovery (Rust
// recover_offline -> worker::recover under the offline machine).
func RecoverOffline(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recoverRouted(sourcePath, candidate, destinationPath, worker.WorkerModeOffline, budget, check, sink)
}

// RecoverLive runs one live worker recovery (Rust recover_live ->
// worker::recover under the live machine). The live-support refusal
// runs worker-side before any path access (the worker machine's
// RecoverLive entry); on this build the parent-side
// require_live_supported would be the same always-nil check the
// worker performs.
func RecoverLive(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recoverRouted(sourcePath, candidate, destinationPath, worker.WorkerModeLive, budget, check, sink)
}

// recoverRouted drives one worker recovery session and folds the
// retained guard-pending cleanup into the domain failure (Rust
// client/recovery.rs guard_pending arm: the source cleanup guard is
// built from the retained worker cleanup; the arm already set the
// coordination class).
func recoverRouted(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, mode worker.WorkerMode, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	outcome, cleanup := worker.RecoverWithWorker(sourcePath, destinationPath, candidate, mode, budget, check, sink)
	if outcome.Result != nil {
		return outcome.Result, nil
	}
	// Exactly one of Result and Failure is set by the wire contract;
	// the guard makes the invariant explicit so a future codec drift
	// fails with the typed Conflict instead of a nil dereference.
	if outcome.Failure == nil {
		// The zero-valued ledger is the clean empty-facts shape (the
		// none classes are the zero values).
		return nil, &recovery.RecoveryPreparationFailure{Cause: &format.Error{Code: format.CodeConflict, Detail: "SDK worker recovery returned neither result nor failure"}}
	}
	if cleanup != nil {
		outcome.Failure.SourceCleanup = recovery.FromWorkerCleanup(cleanup, cleanup.LastProblem().Err())
	}
	return nil, outcome.Failure
}

// validationResultOf converts one wire validation result back to the
// domain result (the wire shape is the worker boundary's; the facade
// surfaces only the domain type).
func validationResultOf(value *worker.ValidationResultWire) *validation.ValidationResult {
	if value == nil {
		return nil
	}
	return &validation.ValidationResult{
		Valid:        value.Valid,
		FileIdentity: value.FileIdentity,
		Generation:   value.Generation,
		Progress:     domainProgressOf(value.Progress),
	}
}

// validationFailureOf converts one wire validation failure back to the
// domain failure, keeping the cleanup authorities (a guard-pending
// terminal rides the retained WorkerCleanup in the source-cleanup
// slot, exactly like the in-process machine's terminal).
func validationFailureOf(value *worker.ValidationFailureWire) *validation.ValidationFailure {
	if value == nil {
		return nil
	}
	progress := domainProgressOf(value.Progress)
	return &validation.ValidationFailure{
		Cause:               wireCauseToDomain(value.Cause),
		Progress:            &progress,
		Cleanup:             value.Cleanup,
		CoordinationCleanup: value.CoordinationCleanup,
		SourceCleanup:       value.SourceCleanup,
	}
}

// wireCauseToDomain folds one worker-boundary decoded cause back to
// the internal error surface (Rust Error::WorkerOperation and the raw
// errno read arms of wire.go): the wire transmits the code pair
// without the detail, and the public facade converts internal causes
// onto the public class, so the wire shape must not leak; the errno
// is dropped like every Go arm.
func wireCauseToDomain(cause error) error {
	var workerError *worker.WireError
	if errors.As(cause, &workerError) {
		return &format.Error{Code: workerError.Code}
	}
	var errno syscall.Errno
	if errors.As(cause, &errno) {
		return &format.Error{Code: format.CodeIO}
	}
	return cause
}

// inspectionOf converts one wire inspection back to the domain
// inspection (the recovery package keeps its candidate slots private).
func inspectionOf(value *worker.InspectionWire) *recovery.RecoveryCandidateInspection {
	return recovery.InspectionOf(value.SourceIdentity, domainProgressOf(value.Progress), value.Candidates)
}

// domainProgressOf converts one wire progress back to the domain
// progress (the validation package keeps its per-reason and per-object
// counter arrays private; the reconstruction goes through the
// exported constructor).
func domainProgressOf(value worker.ProgressWire) validation.ValidationProgress {
	return *validation.ProgressFromCounters(value.CheckedUniquePages, value.FindingCount, value.UntraversableSubgraphs, value.HasUnboundedUnknown, value.BoundedPossibleSpanAddresses, value.ReasonCounts, value.ObjectCounts)
}
