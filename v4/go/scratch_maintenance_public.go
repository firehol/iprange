package iprangedb

// Abandoned-recovery-scratch maintenance surface (Rust
// recovery::list_abandoned_scratch / remove_abandoned_scratch and the
// C ABI maintenance arms): exact-pattern scratch artifacts of
// interrupted recovery operations are listed, authenticated through
// their ownership headers, and removed after caller-certified
// quiescence.

import "github.com/firehol/iprange/v4/go/internal/recovery"

// ScratchOwnerKind is the operation which created one authenticated
// scratch artifact (Rust ScratchOwnerKind).
type ScratchOwnerKind = recovery.ScratchOwnerKind

const (
	ScratchOwnerValidation ScratchOwnerKind = recovery.ScratchOwnerValidation
	ScratchOwnerRecovery   ScratchOwnerKind = recovery.ScratchOwnerRecovery
)

// AbandonedScratchAuthentication classifies one exact-pattern entry
// by its authoritative ownership header (Rust
// AbandonedScratchAuthentication).
type AbandonedScratchAuthentication = recovery.AbandonedScratchAuthentication

// AbandonedScratchEntry is one exact-pattern scratch-directory entry
// (Rust AbandonedScratchEntry).
type AbandonedScratchEntry = recovery.AbandonedScratchEntry

// AbandonedScratchList is one completed constant-memory
// scratch-directory scan (Rust AbandonedScratchList).
type AbandonedScratchList = recovery.AbandonedScratchList

// ListAbandonedScratch lists exact scratch-pattern names without
// following their final component (Rust list_abandoned_scratch): the
// scan runs in constant memory, exact-pattern entries authenticate
// through their 128-byte ownership headers, and the sink receives
// every entry. Returning ErrMaintenanceSinkStop stops the scan.
func ListAbandonedScratch(directory string, cancellation *CancellationToken, sink func(entry *AbandonedScratchEntry) error) (AbandonedScratchList, error) {
	list, err := recovery.ListAbandonedScratch(directory, publicationCheck(cancellation), sink)
	if err != nil {
		return AbandonedScratchList{}, publicError(err)
	}
	return list, nil
}

// RemoveAbandonedScratch removes one authenticated scratch artifact
// after the caller certifies quiescence (Rust
// remove_abandoned_scratch): the exact directory identity, attempt,
// ordinal, and artifact identity must match, and the artifact must
// carry the matching ownership header.
func RemoveAbandonedScratch(directory string, expectedDirectory FileIdentity, attempt [16]byte, ordinal uint32, expectedArtifact FileIdentity, cancellation *CancellationToken) (AbandonedArtifactRemoval, error) {
	removal, err := recovery.RemoveAbandonedScratch(directory, expectedDirectory, attempt, ordinal, expectedArtifact, publicationCheck(cancellation))
	if err != nil {
		return AbandonedArtifactRemoval{}, publicError(err)
	}
	return removal, nil
}
