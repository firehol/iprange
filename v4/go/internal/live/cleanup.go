// Exact cleanup facts for live-lifecycle artifacts (Rust
// live_cleanup.rs POSIX subset). Windows retires artifacts through the
// publication gc machinery, which is out of the Go sidecar scope; the
// Go POSIX path removes the exact inode and reports residue facts.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// cleanupOutcome is the fact record of one cleanup attempt (Rust
// live_cleanup Outcome): clean when nothing failed, otherwise the
// failure cause, plus the housekeeping class and the visible artifact
// ledger (Windows-only in the Rust authority; POSIX keeps both empty).
type cleanupOutcome struct {
	cause        error
	housekeeping Housekeeping
	visible      []HousekeepingArtifact
}

func cleanupOutcomeFailed(cause error) cleanupOutcome {
	return cleanupOutcome{cause: cause}
}

func (o cleanupOutcome) isClean() bool { return o.cause == nil }

// absorb folds another cleanup outcome into this one (Rust
// Outcome::absorb): the first failure wins, housekeeping merges, and
// the visible ledger appends.
func (o *cleanupOutcome) absorb(other cleanupOutcome) {
	if o.cause == nil {
		o.cause = other.cause
	}
	o.housekeeping = o.housekeeping.Merge(other.housekeeping)
	o.visible = append(o.visible, other.visible...)
}

// cleanupAuthority identifies one created coordination inode for
// identity-guarded cleanup (Rust live_cleanup Authority). On POSIX only
// the removal is observable; kind, ordinal, and directory role feed the
// Windows retirement machinery.
type cleanupAuthority struct {
	attemptID     [16]byte
	ordinal       uint32
	kind          ArtifactKind
	directoryRole DirectoryRole
}

// combineErrors mirrors Rust sdk_error::combine_errors: when the
// operation failed and its cleanup (unlock or exact removal) also
// failed, the observable class is CleanupInProgress with both causes in
// the detail; a clean cleanup preserves the operation's error.
func combineErrors(cause error, cleanup error) error {
	if cleanup == nil {
		return cause
	}
	return &format.Error{
		Code:   format.CodeCleanupInProgress,
		Detail: cause.Error() + "; cleanup also failed: " + cleanup.Error(),
	}
}

// terminalFacts are the exact facts retained for the caller of one
// lifecycle attempt (Rust live_cleanup::TerminalFacts): whether residue
// is possible, the housekeeping class, and the failure cause with any
// cleanup failure absorbed.
type terminalFacts struct {
	residuePossible  bool
	housekeeping     Housekeeping
	visibleHousekeep []HousekeepingArtifact
	cause            error
}

func cleanFacts() terminalFacts {
	return terminalFacts{}
}

func causeFacts(cause error) terminalFacts {
	return terminalFacts{cause: cause}
}

// residueFacts reports a failure that leaves residue possible (Rust
// TerminalFacts::residue: the operation outcome cannot be proven
// clean, so residue is possible and the cause is retained).
func residueFacts(cause error) terminalFacts {
	return terminalFacts{residuePossible: true, cause: cause}
}

// failedFacts folds one cleanup outcome into the primary failure (Rust
// TerminalFacts::failed): a failed cleanup makes residue possible and
// the reported cause becomes CleanupInProgress with both sides.
func failedFacts(cause error, cleanup cleanupOutcome) terminalFacts {
	facts := terminalFacts{
		residuePossible:  cleanup.cause != nil,
		housekeeping:     cleanup.housekeeping,
		visibleHousekeep: cleanup.visible,
		cause:            cause,
	}
	if cleanup.cause != nil {
		facts.cause = combineErrors(cause, cleanup.cause)
	}
	return facts
}

// finishWithCleanup mirrors Rust sdk_error::finish_with_cleanup: the
// operation result survives a clean unlock; a failed unlock folds
// through combineErrors into a CleanupInProgress class.
func finishWithCleanup(operation error, cleanup error) error {
	if operation != nil {
		return combineErrors(operation, cleanup)
	}
	return cleanup
}
