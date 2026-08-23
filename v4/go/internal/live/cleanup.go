// Exact cleanup facts for live-lifecycle artifacts (Rust
// live_cleanup.rs POSIX subset). Windows retires artifacts through the
// publication gc machinery, which is out of the M4 sidecar scope; the
// Go POSIX path removes the exact inode and reports residue facts.

package live

import (
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// cleanupOutcome is the fact record of one cleanup attempt (Rust
// live_cleanup Outcome): clean when nothing failed, otherwise the
// failure cause. Housekeeping and the visible artifact ledger are
// Windows-only in the Rust authority and land with the publication
// resolver slice.
type cleanupOutcome struct {
	cause error
}

func cleanupOutcomeFailed(cause error) cleanupOutcome {
	return cleanupOutcome{cause: cause}
}

func (o cleanupOutcome) isClean() bool { return o.cause == nil }

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
		Detail: cause.Error() + "; cleanup failed: " + cleanup.Error(),
	}
}

// requireAvailable is a POSIX no-op: Windows GC custody verification
// only (Rust live_cleanup::require_available).
func requireAvailable(path string, expected fileIdentity, authority cleanupAuthority) error {
	_ = filepath.Clean(path)
	_ = expected
	_ = authority
	return nil
}
