// Exact cleanup facts for live-lifecycle artifacts (Rust
// live_cleanup.rs POSIX subset). Windows retires artifacts through the
// publication gc machinery, which is out of the M4 sidecar scope; the
// Go POSIX path removes the exact inode and reports residue facts.

package live

import (
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// cleanupOutcome is the fact record of one cleanup attempt (Rust
// live_cleanup Outcome): clean when nothing failed, otherwise the
// failure cause, plus the housekeeping class and the visible artifact
// ledger (Windows-only in the Rust authority; POSIX keeps both empty).
type cleanupOutcome struct {
	cause        error
	housekeeping housekeeping
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
	o.housekeeping = o.housekeeping.merge(other.housekeeping)
	o.visible = append(o.visible, other.visible...)
}

// cleanupAuthority identifies one created coordination inode for
// identity-guarded cleanup (Rust live_cleanup Authority). On POSIX only
// the removal is observable; kind, ordinal, and directory role feed the
// Windows retirement machinery.
type cleanupAuthority struct {
	attemptID     [16]byte
	ordinal       uint32
	kind          cleanupKind
	directoryRole cleanupDirectoryRole
}

type cleanupKind uint8

const (
	cleanupKindOwnedCoordination cleanupKind = iota
	cleanupKindOwnedMain
)

type cleanupDirectoryRole uint8

const (
	cleanupRoleMainFile cleanupDirectoryRole = iota
	cleanupRoleScratchDirectory
)

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

// requireAvailable is a POSIX no-op: Windows GC custody verification
// only (Rust live_cleanup::require_available).
func requireAvailable(path string, expected FileIdentity, authority cleanupAuthority) error {
	_ = filepath.Clean(path)
	_ = expected
	_ = authority
	return nil
}

// terminalFacts are the exact facts retained for the caller of one
// lifecycle attempt (Rust live_cleanup::TerminalFacts): whether residue
// is possible, the housekeeping class, and the failure cause with any
// cleanup failure absorbed.
type terminalFacts struct {
	residuePossible  bool
	housekeeping     housekeeping
	visibleHousekeep []HousekeepingArtifact
	cause            error
}

// HousekeepingArtifact is one ledger entry of the Windows retirement
// machinery (Rust publication::HousekeepingArtifact). POSIX cleanup
// never produces entries; the ledger and its full field surface land
// with the publication resolver slice (4-8). The empty slice type keeps
// the terminal facts shape exact.
type HousekeepingArtifact struct{}

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

// uniqueAttemptID draws one nonzero 128-bit cleanup attempt identity
// (Rust live_cleanup::unique_attempt_id POSIX arm: a plain nonzero
// draw; the Windows envelope-collision loop is out of this surface).
func uniqueAttemptID(_ string, _ uint32) ([16]byte, error) {
	return random.Nonzero128()
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
