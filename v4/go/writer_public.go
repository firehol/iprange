// Shared types of the live writer facade (Rust live_writer.rs):
// resource budgets, commit outcomes, and the module-root coordination
// hooks. The public surface composes the internal writer owner; it never
// touches bytes or pages itself (SOW-0025 chunk-6 design record D1
// extends the module-root boundary to internal/writer so the public SDK
// stays the single `iprangedb` package, mirroring the Rust lib). The
// sidecar, cancellation, and coordination surfaces are milestone-4 gaps
// recorded in the SOW (D3).

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// PageBudget declares the draft resource limits of one opened writer
// (Rust live_writer::TransactionBudget): MaxHeapBytes bounds owned
// scratch (metadata compression), MaxPrivatePages bounds the COW draft
// extent, MaxGrowthPages bounds the file growth one transaction may
// claim, and MaxOpenFiles bounds the descriptors one operation may hold
// (the live writer validates it at open, Rust TransactionBudget::
// validate).
type PageBudget struct {
	MaxHeapBytes    uint64
	MaxPrivatePages uint64
	MaxGrowthPages  uint64
	MaxOpenFiles    uint32
}

// DefaultBudget returns the budget proven by the committed corpus
// generation (the Rust conformance transaction_budget values for the
// writer work the fixtures exercise).
func DefaultBudget() PageBudget {
	return PageBudget{MaxHeapBytes: 32 << 20, MaxPrivatePages: 200_000, MaxGrowthPages: 200_000, MaxOpenFiles: 2}
}

func (b PageBudget) internal() writer.PageBudget {
	return writer.PageBudget{MaxHeapBytes: b.MaxHeapBytes, MaxPrivatePages: b.MaxPrivatePages, MaxGrowthPages: b.MaxGrowthPages, MaxOpenFiles: b.MaxOpenFiles}
}

// writerNamespaceCheck is the module-root namespace hook: the SDK's
// namespace surface is a milestone-4 gap, so the hook is a package-level
// no-op implementing the writer owner's callback formal (Rust's namespace
// resolver no-op default).
func writerNamespaceCheck(clean string) error { return nil }

// noopCheckpoint is the module-root durability checkpoint hook: the
// coordination surface is a milestone-4 gap, so the hook is a
// package-level no-op implementing the checkpoint formal.
func noopCheckpoint() error { return nil }

// CommitStatus classifies one commit outcome (Rust CommitDurability).
type CommitStatus uint8

const (
	CommitNotCommitted   CommitStatus = iota // the commit never reached the file
	CommitCommitted                          // the commit landed durably
	CommitOutcomeUnknown                     // the file may have advanced past this commit
)

// CommitResult is the factual outcome of one commit attempt (Rust
// live_writer/result.rs CommitResult): the pinned attempt identities
// (database, directory inode, main inode, transaction, nonce), the
// durability status, the cause, and the retained cleanup and
// coordination evidence. Every commit terminal (live direct,
// membership, feed, structured, and history projection) returns this
// shape, and ResolveCommit accepts it, so an interrupted commit can
// always be resolved afterwards. LiveCommitResult is an alias kept for
// source compatibility with the live-direct surface.
type CommitResult struct {
	AttemptedDatabaseID    [16]byte
	DirectoryIdentity      *FileIdentity
	MainIdentity           *FileIdentity
	AttemptedTransactionID uint64
	AttemptedCommitNonce   [16]byte
	Status                 CommitStatus
	Cleanup                LiveCommitCleanupArtifacts
	CoordinationCleanup    CoordinationCleanup
	Cause                  error
}

// CleanupState reports whether the commit left coordination residue
// (Rust CommitResult::cleanup_state).
func (r CommitResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}
