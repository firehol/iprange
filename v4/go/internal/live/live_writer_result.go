// Live writer result facts (Rust live_writer/result.rs + publication
// types.rs): the durability, cleanup ledger, coordination class, and
// outcome enums carried by commit, abort, and close. The root public
// facade maps these to the public SDK surface.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// CommitDurability is the factual publication state of one attempted
// commit (Rust live_writer::CommitDurability).
type CommitDurability uint8

const (
	CommitNotCommitted CommitDurability = iota
	CommitCommitted
	CommitOutcomeUnknown
)

// AbortOutcome is the factual outcome of one abort (Rust
// live_writer::AbortOutcome).
type AbortOutcome uint8

const (
	AbortOutcomeAborted AbortOutcome = iota
	AbortOutcomeAbortIncomplete
)

// CloseOutcome is the factual outcome of one close (Rust
// live_writer::CloseOutcome).
type CloseOutcome uint8

const (
	CloseOutcomeClosed CloseOutcome = iota
	CloseOutcomeCloseIncomplete
)

// CoordinationCleanup is the coordination residue class of one failed
// operation (Rust publication::CoordinationCleanup): which lock or
// guard the caller must still release.
type CoordinationCleanup uint8

const (
	CoordinationCleanupNone CoordinationCleanup = iota
	CoordinationCleanupCleanupGuard
	CoordinationCleanupRetainedReaderCloseRequired
	CoordinationCleanupRetainedWriterCloseRequired
)

// CommitCleanupArtifact is one exact unresolved unpublished main tail
// (Rust live_writer::CommitCleanupArtifact).
type CommitCleanupArtifact struct {
	DirectoryIdentity        FileIdentity
	MainBasename             LocalBasename
	MainIdentity             FileIdentity
	ExpectedDatabaseID       [16]byte
	TargetTransactionID      uint64
	TargetCommitNonce        [16]byte
	CommittedTargetLength    uint64
	ObservedTailEndExclusive *uint64
	CleanupError             format.ErrorCode
}

// CommitCleanupArtifacts is the fixed commit cleanup ledger; commits can
// own only their main tail (Rust live_writer::CommitCleanupArtifacts).
type CommitCleanupArtifacts struct {
	entry *CommitCleanupArtifact
}

func cleanArtifacts() CommitCleanupArtifacts { return CommitCleanupArtifacts{} }

func tailArtifacts(entry CommitCleanupArtifact) CommitCleanupArtifacts {
	return CommitCleanupArtifacts{entry: &entry}
}

// Empty reports whether the ledger carries no entry (Rust is_empty).
func (c CommitCleanupArtifacts) Empty() bool { return c.entry == nil }

// Entry returns the single tail artifact, or nil (Rust get(0)).
func (c CommitCleanupArtifacts) Entry() *CommitCleanupArtifact { return c.entry }

// LiveCommitResult is the exact identity, durability, and cleanup facts
// of one commit attempt (Rust live_writer::CommitResult).
type LiveCommitResult struct {
	AttemptedDatabaseID    [16]byte
	DirectoryIdentity      FileIdentity
	MainIdentity           FileIdentity
	AttemptedTransactionID uint64
	AttemptedCommitNonce   [16]byte
	Durability             CommitDurability
	Cleanup                CommitCleanupArtifacts
	CoordinationCleanup    CoordinationCleanup
	Cause                  error
}

// LiveAbortResult is the factual abort result; a cleanup failure retains
// a close-only writer (Rust live_writer::AbortResult).
type LiveAbortResult struct {
	Outcome             AbortOutcome
	Cleanup             CommitCleanupArtifacts
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// LiveCloseResult is the factual writer-close result; an incomplete
// close is retryable (Rust live_writer::CloseResult).
type LiveCloseResult struct {
	Outcome             CloseOutcome
	AbortOutcome        *AbortOutcome
	Cleanup             CommitCleanupArtifacts
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// classedError carries one declared error class while keeping the
// wrapped cause chain inspectable (mirror of the former public abortError
// shape; Rust TransactionAborted(Box<cause>) / CleanupIncomplete).
type classedError struct {
	class *format.Error
	cause error
}

func (e *classedError) Error() string {
	if e.cause == nil {
		return e.class.Error()
	}
	return e.class.Error() + ": " + e.cause.Error()
}

func (e *classedError) Unwrap() error { return e.cause }

func (e *classedError) As(target any) bool {
	fe, ok := target.(**format.Error)
	if !ok {
		return false
	}
	*fe = e.class
	return true
}

// errorCodeOf extracts the public code of one error, falling back to
// WrongState (Rust Error::code()).
func errorCodeOf(err error) format.ErrorCode {
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return format.CodeWrongState
}

// isFatalClass reports the fatal error classes whose discard leaves the
// writer unusable even when the abandonment succeeds (Rust
// abort_after: Io | Format | Corrupt).
func isFatalClass(err error) bool {
	switch errorCodeOf(err) {
	case format.CodeIO, format.CodeFormatInvalid:
		return true
	}
	return false
}
