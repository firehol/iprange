// Compact unsigned snapshot surface (Rust snapshot::snapshot_to parity):
// one pinned immutable v4 generation is copied into a fresh published
// output under a caller budget, preserving the source identity, ranges,
// feeds, memberships, structures, and metadata. The machine mirrors
// iprange-livedb/src/snapshot/{api.rs,build.rs,terminal.rs}: the source
// opens before the destination create, the source final check runs
// between the build and the publish rename, and every failure collapses
// to Cause plus CleanupState like AlgebraPreparationFailure.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/snapshot"
)

// SnapshotSourceMode is the source coordination of one snapshot (Rust
// SnapshotSourceMode). The live mode is refused until Milestone 4 with
// ErrorOSUnsupported.
type SnapshotSourceMode uint8

const (
	// SnapshotSourceImmutable snapshots the committed generation of one
	// immutable database path under a shared lifetime lock.
	SnapshotSourceImmutable SnapshotSourceMode = iota
	// SnapshotSourceLive would snapshot one live database generation
	// through the sidecar coordination; the Go SDK refuses it for now.
	SnapshotSourceLive
)

// SnapshotPublicationPolicy is the namespace policy of the snapshot
// destination (Rust PublicationPolicy re-exported as
// SnapshotPublicationPolicy; same values as PublicationPolicy).
type SnapshotPublicationPolicy = PublicationPolicy

// SnapshotBudget bounds one snapshot construction (Rust SnapshotBudget):
// the maximum simultaneous retained heap bytes, the maximum output page
// count, and the maximum simultaneously open files. Validation mirrors
// Rust SnapshotBudget::validate: at least two output pages, and open
// files must cover the source plus the private attempt, with a third file
// for the replace policies (the coordination artifact).
type SnapshotBudget struct {
	MaxHeapBytes   uint64
	MaxOutputPages uint64
	MaxOpenFiles   uint32
}

// SnapshotResult is the successful terminal of one snapshot (Rust
// SnapshotResult): the published output plus the cleanup state.
type SnapshotResult struct {
	Publication PublicationResult
}

// CleanupState reports the artifact state after the publication.
func (r SnapshotResult) CleanupState() CleanupState {
	return r.Publication.Cleanup
}

// SnapshotPreparationFailure is the failing terminal of one snapshot
// (Rust SnapshotPreparationFailure collapsed to the Go-visible fields,
// the AlgebraPreparationFailure precedent): the primary cause and the
// cleanup state of the private attempt artifact.
type SnapshotPreparationFailure struct {
	Cause   error
	Cleanup CleanupState
}

// Error renders the preparation failure.
func (f *SnapshotPreparationFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	return "iprange v4 snapshot preparation: " + f.Cause.Error()
}

// Unwrap exposes the primary cause.
func (f *SnapshotPreparationFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// SnapshotTo runs one compact snapshot (Rust snapshot_to): source path
// and mode, destination path and publication policy, the construction
// budget, and the optional cancellation token. On success the result
// carries the publication; a refused or outcome-unknown publish is a
// result with its own Status/Cause, not an error, exactly like the
// publish_set surface. Every preparation failure returns
// *SnapshotPreparationFailure (the Rust Box<SnapshotPreparationFailure>
// terminal collapses into (SnapshotResult, error) like
// AlgebraPreparationFailure). A nil budget and an invalid source mode
// are Go-boundary guards refused with ErrorInvalidArgument before any
// destination artifact exists.
func SnapshotTo(sourcePath string, sourceMode SnapshotSourceMode, destinationPath string, publicationPolicy SnapshotPublicationPolicy, budget *SnapshotBudget, cancellation *CancellationToken) (SnapshotResult, error) {
	zero := SnapshotResult{}
	if budget == nil {
		return zero, &SnapshotPreparationFailure{
			Cause:   &Error{Code: ErrorInvalidArgument, Detail: "snapshot budget is required"},
			Cleanup: CleanupStateClean,
		}
	}
	internalMode, err := snapshotMode(sourceMode)
	if err != nil {
		return zero, &SnapshotPreparationFailure{Cause: err, Cleanup: CleanupStateClean}
	}
	check := cancellation.check
	result, failure := snapshot.To(sourcePath, internalMode, destinationPath, publicationPolicy, budget.internal(), check)
	if failure != nil {
		return zero, &SnapshotPreparationFailure{Cause: publicError(failure.Cause), Cleanup: failure.Cleanup}
	}
	return SnapshotResult{Publication: *result}, nil
}

// internal converts the public budget onto the machine budget (the
// PageBudget.internal() pattern).
func (b SnapshotBudget) internal() *snapshot.Budget {
	return &snapshot.Budget{
		MaxHeapBytes:   b.MaxHeapBytes,
		MaxOutputPages: b.MaxOutputPages,
		MaxOpenFiles:   b.MaxOpenFiles,
	}
}

// snapshotMode maps the public source mode onto the internal machine
// (Rust api.rs SnapshotSourceMode; the live refusal itself happens inside
// the machine at the same position as Rust's require_live_supported).
func snapshotMode(mode SnapshotSourceMode) (snapshot.SourceMode, error) {
	switch mode {
	case SnapshotSourceImmutable:
		return snapshot.SourceImmutable, nil
	case SnapshotSourceLive:
		return snapshot.SourceLive, nil
	default:
		return 0, &Error{Code: ErrorInvalidArgument, Detail: "snapshot source mode is invalid"}
	}
}
