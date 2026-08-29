//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

// Facade routing on every platform without a worker build. The worker
// binary cross-builds on linux, darwin, freebsd, and windows for amd64
// and arm64 only (the mapped-control atomics of internal/worker have
// assembly implementations for exactly those combinations); on every
// other platform the SDK cannot supply a worker, so the public
// validation, candidate-inspection, and recovery operations refuse
// before any source scan or destination mutation (binary-format-v4.md
// section 19: a missing worker fails before source scanning; Rust
// worker/client.rs returns Io/Unsupported when the executable is
// absent). There is no in-process fallback: the in-process machines
// remain the worker-side engines and are not reachable from this
// package.

package routing

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// workerUnavailable is the exact fail-closed cause (Rust
// Error::Unsupported "SDK validation/recovery worker is unavailable",
// or Error::Io when spawning fails; the same class the worker client
// arms surface on the worker matrix when the binary is absent).
var workerUnavailable = &format.Error{Code: format.CodeOSUnsupported, Detail: "SDK validation/recovery worker is unavailable"}

// Validate runs the preflight and then refuses with the worker
// unavailable class (Rust validation::validate: the preflight runs
// first, then worker::validate; without a worker build the client
// cannot spawn, so the refusal is the client failure).
func Validate(path string, mode validation.ValidationMode, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	if err := validation.Preflight(mode, budget, check); err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(err, &progress)
	}
	progress := validation.NewProgress()
	return nil, validation.Failure(workerUnavailable, &progress)
}

// ValidateOfflineCandidate runs the offline preflight and the
// nil-candidate refusal, then refuses with the worker unavailable
// class (Rust validate_offline over the worker client).
func ValidateOfflineCandidate(path string, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	if err := validation.Preflight(validation.ValidationModeOfflineCandidate, budget, check); err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(err, &progress)
	}
	if candidate == nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(&format.Error{Code: format.CodeInvalidArgument, Detail: "offline-candidate validation requires a candidate token"}, &progress)
	}
	progress := validation.NewProgress()
	return nil, validation.Failure(workerUnavailable, &progress)
}

// InspectRecoveryCandidates runs the inspection preflight, then
// refuses with the worker unavailable class (Rust
// inspect_recovery_candidates over the worker client).
func InspectRecoveryCandidates(path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, check func() error) (*recovery.RecoveryCandidateInspection, error) {
	if err := recovery.PreflightInspection(mode, budget, check); err != nil {
		return nil, err
	}
	return nil, workerUnavailable
}

// RecoverImmutable, RecoverOffline, and RecoverLive validate the
// budget and then refuse with the worker unavailable class (Rust
// recover_*: validate_worker_budget is the first statement, then the
// worker client; without a worker build the client cannot spawn).
func RecoverImmutable(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	if failure := recovery.ValidateWorkerBudget(budget, false); failure != nil {
		return nil, failure
	}
	return nil, &recovery.RecoveryPreparationFailure{Cause: workerUnavailable}
}

// RecoverOffline runs one quiescent recovery refusal (Rust
// recover_offline over the worker client).
func RecoverOffline(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	if failure := recovery.ValidateWorkerBudget(budget, false); failure != nil {
		return nil, failure
	}
	return nil, &recovery.RecoveryPreparationFailure{Cause: workerUnavailable}
}

// RecoverLive runs one live recovery refusal (Rust recover_live over
// the worker client; the live-support refusal runs worker-side).
func RecoverLive(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	if failure := recovery.ValidateWorkerBudget(budget, true); failure != nil {
		return nil, failure
	}
	return nil, &recovery.RecoveryPreparationFailure{Cause: workerUnavailable}
}
