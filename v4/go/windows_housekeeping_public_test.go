// Public Windows housekeeping surface tests (Rust maintenance.rs
// list_windows_housekeeping / remove_windows_housekeeping parity):
// the leading cancellation refusal shared by every maintenance
// entry, and the public entry/removal mapping from the internal scan
// shapes (the identity, attempt, ordinal, and artifact facts keep
// their alias types; the internal problem/cause classes map to the
// exported error type).

package iprangedb

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// TestPublicWindowsHousekeepingCancellationFirst pins the leading
// cancellation refusal shared by every maintenance entry point: a
// cancelled token stops before the platform arm, exactly like the
// abandoned-temp maintenance surface.
func TestPublicWindowsHousekeepingCancellationFirst(t *testing.T) {
	cancelled := NewCancellationToken()
	cancelled.Cancel()
	if _, err := ListWindowsHousekeeping(t.TempDir(), cancelled, nil); !hasPublicCode(err, ErrorCancelled) {
		t.Fatalf("list error = %v, want Cancelled", err)
	}
	if _, err := RemoveWindowsHousekeeping(t.TempDir(), FileIdentity{}, [16]byte{}, 0, FileIdentity{}, nil, cancelled); !hasPublicCode(err, ErrorCancelled) {
		t.Fatalf("remove error = %v, want Cancelled", err)
	}
}

// TestPublicWindowsHousekeepingEntryMapping pins the sink-entry fold
// (Rust WindowsHousekeepingEntry): the identity, attempt, ordinal, and
// artifact facts map field-for-field from the internal alias shapes,
// and the internal problem class maps to the exported error type.
func TestPublicWindowsHousekeepingEntryMapping(t *testing.T) {
	identity := live.LocalFileIdentity{Kind: 1, Bytes: [32]byte{4}}
	attempt := [16]byte{9}
	ordinal := uint32(11)
	artifact := &live.HousekeepingArtifact{
		Kind:           live.ArtifactOwnedMain,
		SourcePresence: live.ArtifactPresent,
	}
	problem := &format.Error{Code: format.CodeIO, Detail: "scan evidence"}
	internal := &publication.WindowsHousekeepingEntry{
		DirectoryIdentity: identity,
		CandidateKind:     publication.WindowsHousekeepingCandidateInertPayload,
		BasenameEncoding:  2,
		Basename:          []byte("iprdb-main.gc-payload-0000000000000009.3"),
		Identity:          &identity,
		AttemptID:         &attempt,
		Ordinal:           &ordinal,
		Artifact:          artifact,
		Problem:           problem,
	}

	entry := publicWindowsHousekeepingEntry(internal)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.DirectoryIdentity != FileIdentity(identity) {
		t.Fatalf("directory identity = %v, want %v", entry.DirectoryIdentity, identity)
	}
	if entry.CandidateKind != WindowsHousekeepingCandidateInertPayload {
		t.Fatalf("candidate kind = %v, want inert payload", entry.CandidateKind)
	}
	if entry.BasenameEncoding != 2 || string(entry.Basename) != "iprdb-main.gc-payload-0000000000000009.3" {
		t.Fatalf("basename = (%d, %q)", entry.BasenameEncoding, entry.Basename)
	}
	if entry.Identity == nil || *entry.Identity != FileIdentity(identity) {
		t.Fatalf("identity = %v, want %v", entry.Identity, identity)
	}
	if entry.AttemptID == nil || *entry.AttemptID != attempt {
		t.Fatalf("attempt = %v, want %v", entry.AttemptID, attempt)
	}
	if entry.Ordinal == nil || *entry.Ordinal != ordinal {
		t.Fatalf("ordinal = %v, want %v", entry.Ordinal, ordinal)
	}
	if entry.Artifact == nil || entry.Artifact.Kind != live.ArtifactOwnedMain {
		t.Fatalf("artifact = %v, want owned-main kind", entry.Artifact)
	}
	if !hasPublicCode(entry.Problem, ErrorIO) {
		t.Fatalf("problem = %v, want exported IO class", entry.Problem)
	}
}

// TestPublicWindowsHousekeepingRemovalMapping pins the removal fold
// (Rust WindowsHousekeepingRemoval): the housekeeping and visible
// artifact facts map field-for-field, and the internal cause class
// maps to the exported error type; a clean terminal keeps a nil cause.
func TestPublicWindowsHousekeepingRemovalMapping(t *testing.T) {
	visible := []live.HousekeepingArtifact{
		{Kind: live.ArtifactOwnedMain, SourcePresence: live.ArtifactPresent},
	}
	failed := publicWindowsHousekeepingRemoval(publication.WindowsHousekeepingRemoval{
		Housekeeping: live.HousekeepingVisible,
		Visible:      visible,
		Cause:        &format.Error{Code: format.CodeConflict, Detail: "unresolvable pair"},
	})
	if failed.Housekeeping != HousekeepingVisible {
		t.Fatalf("housekeeping = %v, want visible", failed.Housekeeping)
	}
	if len(failed.VisibleHousekeeping) != 1 || failed.VisibleHousekeeping[0].Kind != live.ArtifactOwnedMain {
		t.Fatalf("visible = %v, want one owned-main artifact", failed.VisibleHousekeeping)
	}
	if !hasPublicCode(failed.Cause, ErrorConflict) {
		t.Fatalf("cause = %v, want exported Conflict class", failed.Cause)
	}

	clean := publicWindowsHousekeepingRemoval(publication.WindowsHousekeepingRemoval{})
	if clean.Cause != nil {
		t.Fatalf("clean cause = %v, want nil", clean.Cause)
	}
}

// hasPublicCode reports whether one error carries the exported error
// type with the exact public code class, or no error for nil.
func hasPublicCode(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}
	var public *Error
	return errors.As(err, &public) && public.Code == code
}
