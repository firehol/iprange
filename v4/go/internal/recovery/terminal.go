package recovery

// Recovery terminal (Rust recovery/terminal.rs): the public completed
// facts of one recovery (report, scratch attempt, publication), and
// the failing preparation facts with the exact cleanup ledger, the
// source cleanup guard, and the fixed problem. The scratch attempt
// stays nil in the heap-only arm; the authorized-scratch absorb is
// the recorded chunk-4-10 follow-up.

import "github.com/firehol/iprange/v4/go/internal/publication"

// RecoveryScratchAttempt identifies one authorized scratch attempt of
// a recovery build (Rust RecoveryScratchAttempt). The heap-only arm
// never creates one; the type keeps the public terminal shape.
type RecoveryScratchAttempt struct {
	AttemptID         [16]byte
	DirectoryIdentity publication.LocalFileIdentity
	CreationSecurity  publication.CreationSecurity
}

// RecoveryResult is the completed factual recovery outcome (Rust
// RecoveryResult).
type RecoveryResult struct {
	Report      RecoveryReport
	Scratch     *RecoveryScratchAttempt
	Publication publication.PublicationResult
}

// CleanupState reports the combined cleanup state of the result (Rust
// RecoveryResult::cleanup_state).
func (r *RecoveryResult) CleanupState() publication.CleanupState {
	return r.Publication.CleanupState()
}

// RecoveryPreparationFailure is the failing recovery preparation with
// its partial report, the exact output and cleanup facts, the source
// cleanup guard, and the fixed problem (Rust
// RecoveryPreparationFailure).
type RecoveryPreparationFailure struct {
	Report              RecoveryReport
	Scratch             *RecoveryScratchAttempt
	Output              *publication.PrivateOutputAttempt
	Cleanup             publication.CleanupArtifacts
	CoordinationCleanup publication.CoordinationCleanup
	Housekeeping        publication.Housekeeping
	VisibleHousekeeping []publication.HousekeepingArtifact
	SourceCleanup       *RecoverySourceCleanupGuard
	Cause               error
}

// CleanupState reports the combined cleanup state of the failure (Rust
// RecoveryPreparationFailure::cleanup_state).
func (f *RecoveryPreparationFailure) CleanupState() publication.CleanupState {
	if f.Cleanup.Empty() && f.CoordinationCleanup == publication.CoordinationCleanupNone {
		return publication.CleanupStateClean
	}
	return publication.CleanupStateResiduePossible
}

// earlyRecoveryFailure builds the fixed early recovery failure (Rust
// RecoveryPreparationFailure::early: the fixed problem of the cause
// and the empty facts).
func earlyRecoveryFailure(cause error) *RecoveryPreparationFailure {
	return newRecoveryPreparationFailure(problem(cause), RecoveryReport{}, nil, nil, nil)
}

// newRecoveryPreparationFailure builds one complete recovery
// preparation failure (Rust RecoveryPreparationFailure::new: the
// output artifact and the scratch absorb fold into the ledger, and a
// retained source guard carries the coordination cleanup class).
func newRecoveryPreparationFailure(cause error, report RecoveryReport, output *publication.PrivateOutputAttempt, outputArtifact *publication.CleanupArtifact, sourceCleanup *RecoverySourceCleanupGuard) *RecoveryPreparationFailure {
	cleanup := publication.NewCleanupArtifacts()
	if outputArtifact != nil {
		cleanup.Push(*outputArtifact)
	}
	absorbed := absorbScratch(nil, &cleanup)
	coordination := publication.CoordinationCleanupNone
	if sourceCleanup != nil {
		coordination = publication.CoordinationCleanupCleanupGuard
	}
	return &RecoveryPreparationFailure{
		Report:              report,
		Scratch:             absorbed.attempt,
		Output:              output,
		Cleanup:             cleanup,
		CoordinationCleanup: coordination,
		Housekeeping:        absorbed.housekeeping,
		VisibleHousekeeping: absorbed.visible,
		SourceCleanup:       sourceCleanup,
		Cause:               cause,
	}
}

// fromPublicationFailure folds one publication preparation failure
// into the recovery failure (Rust
// RecoveryPreparationFailure::from_publication).
func fromPublicationFailure(failure *publication.PublicationPreparationFailure, report RecoveryReport) *RecoveryPreparationFailure {
	output := &publication.PrivateOutputAttempt{
		PublicationAttemptID: failure.PublicationAttemptID,
		DirectoryIdentity:    failure.DirectoryIdentity,
		BasenameEncoding:     failure.PrivateOutputBasenameEncoding,
		Basename:             failure.PrivateOutputBasename,
		Identity:             failure.OutputIdentity,
		IdentityPresent:      true,
		CreationSecurity:     failure.CreationSecurity,
	}
	absorbed := absorbScratch(nil, &failure.Cleanup)
	housekeeping := failure.Housekeeping.Merge(absorbed.housekeeping)
	visible := failure.VisibleHousekeeping
	visible = append(visible, absorbed.visible...)
	return &RecoveryPreparationFailure{
		Report:              report,
		Scratch:             absorbed.attempt,
		Output:              output,
		Cleanup:             failure.Cleanup,
		CoordinationCleanup: failure.CoordinationCleanup,
		Housekeeping:        housekeeping,
		VisibleHousekeeping: visible,
		SourceCleanup:       nil,
		Cause:               failure.Cause,
	}
}

// completedRecovery builds the completed recovery outcome (Rust
// terminal::completed): the publication result absorbs the scratch
// facts (none in the heap-only arm).
func completedRecovery(report RecoveryReport, publication publication.PublicationResult) RecoveryResult {
	absorbed := absorbScratch(nil, &publication.Cleanup)
	publication.Housekeeping = publication.Housekeeping.Merge(absorbed.housekeeping)
	publication.VisibleHousekeeping = append(publication.VisibleHousekeeping, absorbed.visible...)
	return RecoveryResult{
		Report:      report,
		Scratch:     absorbed.attempt,
		Publication: publication,
	}
}

// absorbedScratch is the folded scratch facts of one terminal (Rust
// AbsorbedScratch; the heap-only arm always absorbs nothing).
type absorbedScratch struct {
	attempt      *RecoveryScratchAttempt
	housekeeping publication.Housekeeping
	visible      []publication.HousekeepingArtifact
}

// absorbScratch folds one scratch cleanup into the artifact ledger
// (Rust absorb_scratch non-posix arm: the heap-only arm carries no
// scratch cleanup, so nothing is absorbed; the authorized-scratch
// follow-up fills the residue arm).
func absorbScratch(scratch any, artifacts *publication.CleanupArtifacts) absorbedScratch {
	_ = scratch
	_ = artifacts
	return absorbedScratch{housekeeping: publication.HousekeepingNone}
}
