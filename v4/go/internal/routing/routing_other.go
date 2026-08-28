//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

// Facade routing on every platform without a worker build (the worker
// binary cross-builds on linux, darwin, freebsd, and windows for
// amd64 and arm64; the worker-routed surface lives in routing_
// worker.go for exactly those combinations). Every other platform
// runs the in-process machines directly, the same machines the worker
// routes to; no worker binary exists there to spawn.

package routing

import (
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// Validate runs one explicit validation through the in-process
// machine (Rust validation::validate_local).
func Validate(path string, mode validation.ValidationMode, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	return validation.Validate(path, mode, budget, check, sink)
}

// ValidateOfflineCandidate validates one retained recovery candidate
// through the in-process machine (Rust validate_offline).
func ValidateOfflineCandidate(path string, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	return recovery.ValidateOfflineCandidate(path, candidate, budget, check, sink)
}

// InspectRecoveryCandidates runs one in-process candidate inspection
// (Rust inspect_recovery_candidates_local).
func InspectRecoveryCandidates(path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, check func() error) (*recovery.RecoveryCandidateInspection, error) {
	return recovery.InspectRecoveryCandidates(path, mode, budget, check)
}

// RecoverImmutable runs one in-process immutable recovery (Rust
// recover_immutable over the local machine).
func RecoverImmutable(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recovery.RecoverImmutable(sourcePath, candidate, destinationPath, budget, check, sink)
}

// RecoverOffline runs one in-process quiescent recovery (Rust
// recover_offline over the local machine).
func RecoverOffline(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recovery.RecoverOffline(sourcePath, candidate, destinationPath, budget, check, sink)
}

// RecoverLive runs one in-process live recovery (Rust recover_live
// over the local machine; the live-support refusal runs first).
func RecoverLive(sourcePath string, candidate *recovery.RecoveryCandidate, destinationPath string, budget *recovery.RecoveryBudget, check func() error, sink recovery.RecoverySink) (*recovery.RecoveryResult, *recovery.RecoveryPreparationFailure) {
	return recovery.RecoverLive(sourcePath, candidate, destinationPath, budget, check, sink)
}
