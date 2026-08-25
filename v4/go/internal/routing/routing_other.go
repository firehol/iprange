//go:build !linux || !amd64

// Facade routing on every platform without the worker binary (the
// worker is built only for linux/amd64, the recorded stance): each
// public entry runs the in-process machine directly, exactly as the
// pre-4-12 facade did. The six signatures are the same shapes the
// in-process machines return on every platform.

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
