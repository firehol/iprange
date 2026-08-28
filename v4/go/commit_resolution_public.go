// Public commit-outcome resolution surface (Rust commit_resolution.rs
// re-exports): ResolveCommit proves one exact attempted transaction
// and nonce against the two meta pages of a database file without
// validating either page graph, in Live or Immutable coordination
// mode, and trims only the unpublished tail of the selected
// generation. Live mode requires the attempted database's ready
// reader table and claims the writer lease for the proof; Immutable
// mode requires the canonical sidecar to be absent and accepts any
// local link count on the main file.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// CommitResolutionMode is the coordination mode used while proving
// one commit attempt (Rust CommitResolutionMode): Live coordinates
// through the attempted database's reader table and writer lease,
// Immutable inspects a sidecar-free local file (exactly one local
// copy, any link count).
type CommitResolutionMode uint8

const (
	CommitResolutionModeLive      CommitResolutionMode = iota
	CommitResolutionModeImmutable CommitResolutionMode = iota
)

// LocalFileRelation is the relation between the attempted and
// inspected local files (Rust LocalFileRelation): the attempted
// directory and main identities are compared with the inspected
// file's retained identities.
type LocalFileRelation uint8

const (
	LocalFileRelationSameLocalFile      LocalFileRelation = iota
	LocalFileRelationDifferentLocalFile LocalFileRelation = iota
)

// CommitResolution is the exact durability classification of one
// attempted transaction and nonce (Rust CommitResolution):
// Committed when either meta page carries the exact transaction and
// nonce, NotCommitted when the selected generation proves the attempt
// never landed, SupersededUnknown when the database advanced past the
// attempt without proving its nonce, and Unresolvable when the file
// could not prove a stable classification.
type CommitResolution uint8

const (
	CommitResolutionCommitted         CommitResolution = iota
	CommitResolutionNotCommitted      CommitResolution = iota
	CommitResolutionSupersededUnknown CommitResolution = iota
	CommitResolutionUnresolvable      CommitResolution = iota
)

// CommitResolutionResult is the factual identities and classification
// returned by commit resolution (Rust CommitResolutionResult): the
// attempted transaction facts, the actual retained identities of the
// inspected file, the local-file relation, the exact outcome, and the
// cleanup evidence when the unpublished tail could not be removed.
type CommitResolutionResult struct {
	AttemptedDatabaseID     [16]byte
	AttemptedTransactionID  uint64
	AttemptedCommitNonce    [16]byte
	ActualDirectoryIdentity FileIdentity
	ActualMainIdentity      FileIdentity
	LocalFileRelation       LocalFileRelation
	Resolution              CommitResolution
	Cleanup                 LiveCommitCleanupArtifacts
	CoordinationCleanup     CoordinationCleanup
	Cause                   error
}

// CleanupState reports whether the resolution left an unresolved
// unpublished tail or coordination residue (Rust
// CommitResolutionResult::cleanup_state).
func (r CommitResolutionResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// ResolveCommit resolves one exact commit attempt without validating
// either page graph (Rust resolve_commit): the main file is opened
// read-write under the shared lifetime lock, the attempt is classified
// twice around a file sync, the selected generation must be stable,
// and in Live mode the ready reader table of the attempted database is
// gated, the writer lease is claimed, and the reader slots are scanned
// against the selected generation. attempt is any CommitResult value
// (live direct, membership, feed, structured, or history projection
// commit), so an interrupted advanced commit can be resolved exactly
// like a live-direct one. An Unresolvable outcome is a successful
// return carrying the classification, cause, and cleanup facts; only
// coordination failures return an error. cancellation, when non-nil,
// is checked between every bounded step.
func ResolveCommit(path string, attempt CommitResult, mode CommitResolutionMode, cancellation *CancellationToken) (*CommitResolutionResult, error) {
	internalAttempt := live.LiveCommitResult{
		AttemptedDatabaseID:    attempt.AttemptedDatabaseID,
		DirectoryIdentity:      internalIdentityOrZero(attempt.DirectoryIdentity),
		MainIdentity:           internalIdentityOrZero(attempt.MainIdentity),
		AttemptedTransactionID: attempt.AttemptedTransactionID,
		AttemptedCommitNonce:   attempt.AttemptedCommitNonce,
	}
	result, err := live.ResolveCommit(path, &internalAttempt, live.CommitResolutionMode(mode), cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	directoryIdentity := publicIdentity(&result.ActualDirectoryIdentity)
	mainIdentity := publicIdentity(&result.ActualMainIdentity)
	return &CommitResolutionResult{
		AttemptedDatabaseID:     result.AttemptedDatabaseID,
		AttemptedTransactionID:  result.AttemptedTransactionID,
		AttemptedCommitNonce:    result.AttemptedCommitNonce,
		ActualDirectoryIdentity: *directoryIdentity,
		ActualMainIdentity:      *mainIdentity,
		LocalFileRelation:       LocalFileRelation(result.LocalFileRelation),
		Resolution:              CommitResolution(result.Resolution),
		Cleanup:                 publicCleanupArtifacts(result.Cleanup),
		CoordinationCleanup:     publicCoordinationCleanup(result.CoordinationCleanup),
		Cause:                   publicError(result.Cause),
	}, nil
}

// internalIdentityOrZero maps one public portable identity back to the
// internal retained identity, with a nil public identity as the zero
// internal identity (the Rust CommitResult identities are mandatory;
// a zero identity reports DifferentLocalFile, never an error).
func internalIdentityOrZero(id *FileIdentity) live.FileIdentity {
	if id == nil {
		return live.FileIdentity{}
	}
	return *internalIdentity(id)
}
