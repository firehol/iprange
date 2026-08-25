// Public recovery surface (Rust recovery.rs recover_immutable /
// recover_offline / recover_live, inspect_recovery_candidates,
// validate_offline, and the recovery re-exports): one bounded recovery
// construction of an exact retained candidate into a fresh published
// output. The facade mirrors iprange-livedb/src/recovery/api.rs plus
// the worker client create position: the budget preflight, the
// fail-if-exists destination attempt, the source open per mode with
// the attempt discard on failure, the source-identity proof, the
// kind-split build, the final source check, and the publication
// terminal; on linux/amd64 the operations route through the isolated
// worker client after the preflight (internal/routing), exactly like
// the Rust public entries. The offline certification, the live
// support refusal, and the live newest-only candidate rule mirror the
// Rust api arms exactly.

package iprangedb

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/routing"
)

// OfflineQuiescenceCertification certifies exclusive quiescence of the
// source for one offline recovery (Rust
// OfflineQuiescenceCertification; consumed by the caller boundary
// exactly like the Rust `let _ = certification` arm).
type OfflineQuiescenceCertification uint8

const (
	// CallerCertified certifies that the caller holds exclusive
	// quiescence of the source for the complete operation.
	CallerCertified OfflineQuiescenceCertification = iota
)

// RecoveryCandidateLabel is the exact role of one recoverable retained
// meta page (Rust RecoveryCandidateLabel).
type RecoveryCandidateLabel = recovery.RecoveryCandidateLabel

const (
	// RecoveryCandidateNewest is the proven current generation of a
	// live or retired pair.
	RecoveryCandidateNewest = recovery.CandidateNewest
	// RecoveryCandidatePrevious is the proven previous generation of a
	// live pair.
	RecoveryCandidatePrevious = recovery.CandidatePrevious
	// RecoveryCandidateUnorderedMeta0 is meta page 0 of a pair whose
	// order cannot be proven.
	RecoveryCandidateUnorderedMeta0 = recovery.CandidateUnorderedMeta0
	// RecoveryCandidateUnorderedMeta1 is meta page 1 of an unordered
	// pair.
	RecoveryCandidateUnorderedMeta1 = recovery.CandidateUnorderedMeta1
)

// RecoveryCandidate is the exact opaque recovery token returned by
// candidate inspection and consumed by the recovery entry points (Rust
// RecoveryCandidate).
type RecoveryCandidate = recovery.RecoveryCandidate

// RecoveryCandidateInspection is the bounded recovery-candidate
// inspection result (Rust RecoveryCandidateInspection).
type RecoveryCandidateInspection = recovery.RecoveryCandidateInspection

// RecoveryInspectionMode selects the coordination binding of one
// recovery candidate inspection (Rust RecoveryInspectionMode).
type RecoveryInspectionMode = recovery.RecoveryInspectionMode

const (
	RecoveryInspectionImmutable = recovery.RecoveryInspectionImmutable
	RecoveryInspectionLive      = recovery.RecoveryInspectionLive
	RecoveryInspectionOffline   = recovery.RecoveryInspectionOffline
)

// RecoveryBudget bounds one recovery operation (Rust RecoveryBudget).
type RecoveryBudget = recovery.RecoveryBudget

// RecoveryHeapOnly builds a recovery budget which forbids external
// scratch files (Rust RecoveryBudget::heap_only).
func RecoveryHeapOnly(maxHeapBytes uint64, maxOutputPages uint64, maxOpenFiles uint32) *RecoveryBudget {
	return recovery.HeapOnly(maxHeapBytes, maxOutputPages, maxOpenFiles)
}

// RecoveryPageCounts is the physical-page facts of one recovery read
// (Rust RecoveryPageCounts).
type RecoveryPageCounts = recovery.RecoveryPageCounts

// RecoveryLogicalCounts is the logical-object facts of one recovery
// read (Rust RecoveryLogicalCounts).
type RecoveryLogicalCounts = recovery.RecoveryLogicalCounts

// RecoveryReport is the truthful completed or partial recovery facts
// (Rust RecoveryReport).
type RecoveryReport = recovery.RecoveryReport

// RecoveryUnknownEnvelope is one independently established
// recovery-damage envelope (Rust RecoveryUnknownEnvelope).
type RecoveryUnknownEnvelope = recovery.RecoveryUnknownEnvelope

// RecoverySinkControl is the sink response for one borrowed damage
// envelope (Rust RecoverySinkControl).
type RecoverySinkControl = recovery.RecoverySinkControl

const (
	RecoverySinkContinue = recovery.RecoverySinkContinue
	RecoverySinkStop     = recovery.RecoverySinkStop
)

// RecoverySink consumes one borrowed recovery-damage envelope and
// decides whether the read continues (Rust RecoverySink; a nil sink
// behaves like Continue for every envelope).
type RecoverySink = recovery.RecoverySink

// RecoverySinkFunc adapts a plain function to the recovery sink
// interface (Rust impl RecoverySink for F).
type RecoverySinkFunc = recovery.RecoverySinkFunc

// RecoveryScratchAttempt identifies one authorized scratch attempt of
// a recovery build (Rust RecoveryScratchAttempt; the heap-only arm
// never creates one).
type RecoveryScratchAttempt = recovery.RecoveryScratchAttempt

// RecoveryResult is the completed factual recovery outcome (Rust
// RecoveryResult).
type RecoveryResult = recovery.RecoveryResult

// RecoveryPreparationFailure is the failing recovery preparation with
// its partial report, the exact output and cleanup facts, the source
// cleanup guard, and the fixed problem (Rust
// RecoveryPreparationFailure).
type RecoveryPreparationFailure = recovery.RecoveryPreparationFailure

// RecoverySourceCleanupGuard is the public retryable cleanup authority
// of one failed recovery source release (Rust
// RecoverySourceCleanupGuard). LastProblem reports the internal typed
// failure; ErrorCode class comparisons apply to it unchanged.
type RecoverySourceCleanupGuard = recovery.RecoverySourceCleanupGuard

// PrivateOutputAttempt is the identity of one private output artifact
// of a recovery terminal (Rust PrivateOutputAttempt).
type PrivateOutputAttempt = publication.PrivateOutputAttempt

// CleanupArtifacts is the fixed cleanup ledger of one publication
// attempt (Rust CleanupArtifacts).
type CleanupArtifacts = publication.CleanupArtifacts

// CreationSecurity is the creator-only proof of one private attempt
// (Rust CreationSecurity).
type CreationSecurity = publication.CreationSecurity

// InspectRecoveryCandidates classifies the retained recovery
// candidates of one database path under the selected mode (Rust
// inspect_recovery_candidates; on linux/amd64 the preflight runs and
// then the inspection routes through the isolated worker client).
// cancellation, when non-nil, is checked between bounded steps; the
// inspection never scans a page graph.
func InspectRecoveryCandidates(path string, mode RecoveryInspectionMode, budget *ValidationBudget, cancellation *CancellationToken) (*RecoveryCandidateInspection, error) {
	result, err := routing.InspectRecoveryCandidates(path, mode, budget, cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	return result, nil
}

// ValidateOfflineCandidate validates one retained recovery candidate
// of a quiescent database path (Rust validation::validate over the
// OfflineCandidate arm; the Go mode enum cannot carry the candidate
// payload, so the arm is entered here): the exclusive-lifetime-locked
// source is opened, the token identity and the selected meta are
// re-proved, the candidate state is swept through the validation
// machine, and the terminal re-verifies the source and the token
// before the result is reported. A nil budget is refused before any
// path access; cancellation, when non-nil, is checked between bounded
// steps. Exactly one of the result and the failure is non-nil.
func ValidateOfflineCandidate(path string, candidate *RecoveryCandidate, budget *ValidationBudget, cancellation *CancellationToken, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	result, failure := routing.ValidateOfflineCandidate(path, candidate, budget, cancellation.check, sink)
	if failure != nil {
		converted := *failure
		converted.Cause = publicError(failure.Cause)
		return nil, &converted
	}
	return result, nil
}

// RecoverImmutable runs one bounded recovery of the exact candidate
// into a fresh published output (Rust recover_immutable): the source
// opens under a shared lifetime lock with the sidecar refused, the
// build streams damage envelopes into sink, and the destination is a
// fresh fail-if-exists publication. A nil budget is refused with
// ErrorInvalidArgument before any path access.
func RecoverImmutable(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, sink RecoverySink, cancellation *CancellationToken) (*RecoveryResult, *RecoveryPreparationFailure) {
	if budget == nil {
		return nil, recoveryBudgetFailure()
	}
	result, failure := routing.RecoverImmutable(sourcePath, candidate, destinationPath, budget, internalCheck(cancellation), sink)
	return result, publicRecoveryFailure(failure)
}

// RecoverOffline runs one bounded recovery of the exact candidate
// under caller-certified exclusive quiescence (Rust recover_offline):
// the source opens read-write under an exclusive lifetime lock. The
// certification is accepted at the boundary like the Rust arm.
func RecoverOffline(sourcePath string, candidate *RecoveryCandidate, destinationPath string, certification OfflineQuiescenceCertification, budget *RecoveryBudget, sink RecoverySink, cancellation *CancellationToken) (*RecoveryResult, *RecoveryPreparationFailure) {
	_ = certification
	if budget == nil {
		return nil, recoveryBudgetFailure()
	}
	result, failure := routing.RecoverOffline(sourcePath, candidate, destinationPath, budget, internalCheck(cancellation), sink)
	return result, publicRecoveryFailure(failure)
}

// RecoverLive runs one bounded recovery of the newest candidate of a
// live database into a fresh published output (Rust recover_live): the
// source claims one reader slot through the sidecar coordination, and
// only the newest candidate is accepted. With a valid budget the
// platform support refusal runs before any path access with
// ErrorLiveCoordinationUnsupported, exactly like the Rust api arm; a
// nil budget is refused first at the Go boundary.
func RecoverLive(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, sink RecoverySink, cancellation *CancellationToken) (*RecoveryResult, *RecoveryPreparationFailure) {
	if budget == nil {
		return nil, recoveryBudgetFailure()
	}
	result, failure := routing.RecoverLive(sourcePath, candidate, destinationPath, budget, internalCheck(cancellation), sink)
	return result, publicRecoveryFailure(failure)
}

// internalCheck exposes the public token checkpoint on the internal
// error class (Rust carries one error type through the boundary): the
// recovery problem folds classify the public cancellation refusal
// exactly like the internal one.
func internalCheck(token *CancellationToken) func() error {
	if token == nil {
		return nil
	}
	return func() error {
		if err := token.check(); err != nil {
			var public *Error
			if errors.As(err, &public) {
				return &format.Error{Code: format.ErrorCode(public.Code), Detail: public.Detail}
			}
			return err
		}
		return nil
	}
}

// recoveryBudgetFailure builds the fixed nil-budget refusal (the
// snapshot nil-budget precedent).
func recoveryBudgetFailure() *RecoveryPreparationFailure {
	return &RecoveryPreparationFailure{Cause: &Error{Code: ErrorInvalidArgument, Detail: "recovery budget is required"}}
}

// publicRecoveryFailure converts the internal typed causes onto the
// public error class, keeping every factual field (Rust
// Box<RecoveryPreparationFailure> surfaces unchanged).
func publicRecoveryFailure(failure *RecoveryPreparationFailure) *RecoveryPreparationFailure {
	if failure == nil {
		return nil
	}
	out := *failure
	out.Cause = publicError(failure.Cause)
	return &out
}
