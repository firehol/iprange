// Fact-shape tests of the publication types package (Rust
// publication/types.rs). Enum discriminants are pinned to the Rust
// ordinals; the cleanup ledger and identity codecs are exercised
// directly.

package publication

import (
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestEnumDiscriminants pins every portable enum ordinal to the Rust
// declaration order (types.rs).
func TestEnumDiscriminants(t *testing.T) {
	tests := []struct {
		got, want any
		name      string
	}{
		{PolicyFailIfExists, PublicationPolicy(0), "publication policy"},
		{PolicyReplaceExisting, PublicationPolicy(1), "publication policy replace"},
		{PolicyReplaceExistingNoRollback, PublicationPolicy(2), "publication policy no-rollback"},
		{PublicationNotPublished, PublicationStatus(0), "publication status"},
		{PublicationPublished, PublicationStatus(1), "publication status published"},
		{PublicationOutcomeUnknown, PublicationStatus(2), "publication status unknown"},
		{DestinationContentDesired, DestinationContent(0), "destination content desired"},
		{DestinationContentPrevious, DestinationContent(1), "destination content previous"},
		{DestinationContentAbsent, DestinationContent(2), "destination content absent"},
		{DestinationContentOther, DestinationContent(3), "destination content other"},
		{DestinationContentUnclassified, DestinationContent(4), "destination content unclassified"},
		{LaterCanonicalNone, LaterCanonical(0), "later canonical none"},
		{LaterCanonicalReservationOrTransition, LaterCanonical(1), "later canonical reservation"},
		{LaterCanonicalReadyLiveSidecar, LaterCanonical(2), "later canonical sidecar"},
		{LiveLineageSameGenerationExactBytes, LiveLineage(0), "live lineage exact"},
		{LiveLineageSameGenerationPhysicalBytesChanged, LiveLineage(1), "live lineage changed"},
		{LiveLineageAdvancedGeneration, LiveLineage(2), "live lineage advanced"},
		{AccessPolicyAbsent, AccessPolicy(0), "access policy absent"},
		{AccessPolicyCreatorOnly, AccessPolicy(1), "access policy creator"},
		{AccessPolicyChangedOrUnproven, AccessPolicy(2), "access policy changed"},
		{AccessPolicyUnclassified, AccessPolicy(3), "access policy unclassified"},
		{CleanupStateClean, CleanupState(0), "cleanup state clean"},
		{CleanupStateResiduePossible, CleanupState(1), "cleanup state residue"},
		{ArtifactPrivateOutput, ArtifactKind(0), "artifact private output"},
		{ArtifactPrivateReservation, ArtifactKind(1), "artifact private reservation"},
		{ArtifactOwnedCoordination, ArtifactKind(2), "artifact coordination"},
		{ArtifactAuthorizedScratch, ArtifactKind(3), "artifact scratch"},
		{ArtifactOwnedMain, ArtifactKind(4), "artifact main"},
		{ArtifactUnpublishedMainTail, ArtifactKind(5), "artifact tail"},
		{DirectoryRoleDestination, DirectoryRole(0), "directory destination"},
		{DirectoryRoleScratchDirectory, DirectoryRole(1), "directory scratch"},
		{DirectoryRoleMainFile, DirectoryRole(2), "directory main"},
		{CoordinationCleanupNone, CoordinationCleanup(0), "coordination none"},
		{CoordinationCleanupCleanupGuard, CoordinationCleanup(1), "coordination guard"},
		{CoordinationCleanupRetainedReaderCloseRequired, CoordinationCleanup(2), "coordination reader"},
		{CoordinationCleanupRetainedWriterCloseRequired, CoordinationCleanup(3), "coordination writer"},
		{HousekeepingNone, Housekeeping(0), "housekeeping none"},
		{HousekeepingCrashReappearancePossible, Housekeeping(1), "housekeeping crash"},
		{HousekeepingVisible, Housekeeping(2), "housekeeping visible"},
		{HousekeepingMovePending, HousekeepingState(0), "housekeeping state pending"},
		{HousekeepingMoveAmbiguous, HousekeepingState(1), "housekeeping state ambiguous"},
		{HousekeepingInert, HousekeepingState(2), "housekeeping state inert"},
		{HousekeepingConflict, HousekeepingState(3), "housekeeping state conflict"},
		{ArtifactAbsent, ArtifactPresence(0), "artifact presence absent"},
		{ArtifactPresent, ArtifactPresence(1), "artifact presence present"},
		{ArtifactUnclassified, ArtifactPresence(2), "artifact presence unclassified"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s ordinal = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

// TestHousekeepingMerge pins the Rust Housekeeping::merge lattice.
func TestHousekeepingMerge(t *testing.T) {
	tests := []struct {
		a, b, want Housekeeping
	}{
		{HousekeepingNone, HousekeepingNone, HousekeepingNone},
		{HousekeepingNone, HousekeepingCrashReappearancePossible, HousekeepingCrashReappearancePossible},
		{HousekeepingCrashReappearancePossible, HousekeepingNone, HousekeepingCrashReappearancePossible},
		{HousekeepingNone, HousekeepingVisible, HousekeepingVisible},
		{HousekeepingVisible, HousekeepingNone, HousekeepingVisible},
		{HousekeepingCrashReappearancePossible, HousekeepingVisible, HousekeepingVisible},
		{HousekeepingVisible, HousekeepingCrashReappearancePossible, HousekeepingVisible},
		{HousekeepingVisible, HousekeepingVisible, HousekeepingVisible},
		{HousekeepingCrashReappearancePossible, HousekeepingCrashReappearancePossible, HousekeepingCrashReappearancePossible},
	}
	for _, tt := range tests {
		if got := tt.a.merge(tt.b); got != tt.want {
			t.Errorf("merge(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestCleanupLedger exercises the fixed ledger exactly like the Rust
// CleanupArtifacts tests: empty state, push order, and the capacity
// overflow contract.
func TestCleanupLedger(t *testing.T) {
	ledger := newCleanupArtifacts()
	if !ledger.Empty() || ledger.Len() != 0 || ledger.State() != CleanupStateClean {
		t.Fatalf("fresh ledger = len %d, state %v; want empty clean", ledger.Len(), ledger.State())
	}
	artifact := func(kind ArtifactKind) CleanupArtifact {
		return CleanupArtifact{Kind: kind, Basename: []byte("residue")}
	}
	ledger.push(artifact(ArtifactPrivateOutput))
	ledger.push(artifact(ArtifactPrivateReservation))
	if ledger.Empty() || ledger.Len() != 2 || ledger.State() != CleanupStateResiduePossible {
		t.Fatalf("ledger after two pushes = len %d, state %v", ledger.Len(), ledger.State())
	}
	if got := ledger.At(1).Kind; got != ArtifactPrivateReservation {
		t.Errorf("At(1).Kind = %v, want %v", got, ArtifactPrivateReservation)
	}
	if got := ledger.At(2); got != nil {
		t.Errorf("At(2) = %v, want nil", got)
	}
	if got := ledger.Slice(); len(got) != 2 {
		t.Errorf("Slice() len = %d, want 2", len(got))
	}
	// Capacity overflow panics (Rust assert!("fixed cleanup ledger
	// overflow")).
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("overflow push did not panic")
			}
		}()
		ledger.push(artifact(ArtifactOwnedCoordination))
		ledger.push(artifact(ArtifactOwnedMain))
		ledger.push(artifact(ArtifactUnpublishedMainTail))
	}()
}

// TestResultCleanupState pins the combined cleanup-state method of the
// result and preparation failure surfaces (Rust cleanup_state).
func TestResultCleanupState(t *testing.T) {
	result := &PublicationResult{}
	if got := result.CleanupState(); got != CleanupStateClean {
		t.Errorf("empty result cleanup = %v, want clean", got)
	}
	result.Cleanup.push(CleanupArtifact{Kind: ArtifactPrivateOutput})
	if got := result.CleanupState(); got != CleanupStateResiduePossible {
		t.Errorf("artifact result cleanup = %v, want residue", got)
	}
	result2 := &PublicationResult{CoordinationCleanup: CoordinationCleanupCleanupGuard}
	if got := result2.CleanupState(); got != CleanupStateResiduePossible {
		t.Errorf("coordination result cleanup = %v, want residue", got)
	}
	failure := &PublicationPreparationFailure{Cause: &format.Error{Code: format.CodeConflict, Detail: "x"}}
	if got := failure.CleanupState(); got != CleanupStateClean {
		t.Errorf("empty failure cleanup = %v, want clean", got)
	}
	if got := failure.Error(); got == "" {
		t.Error("preparation failure Error() is empty")
	}
	if got := failure.Unwrap(); got == nil {
		t.Error("preparation failure Unwrap() is nil")
	}
}

// TestLocalIdentityCodec pins the Identity encode/decode exactly like
// Rust namespace_identity.rs: nonzero device+inode pair, zero tail,
// and the rejection rules.
func TestLocalIdentityCodec(t *testing.T) {
	id := localIdentityFromDeviceInode(0x123456789abcdef0, 0xfeedfacecafebeef)
	if id.Kind != identityKind {
		t.Errorf("kind = %d, want %d", id.Kind, identityKind)
	}
	wantBytes := [32]byte{
		0xf0, 0xde, 0xbc, 0x9a, 0x78, 0x56, 0x34, 0x12,
		0xef, 0xbe, 0xfe, 0xca, 0xce, 0xfa, 0xed, 0xfe,
	}
	if id.Bytes != wantBytes {
		t.Errorf("bytes = %x, want %x", id.Bytes, wantBytes)
	}
	device, inode, ok := id.deviceInode()
	if !ok || device != 0x123456789abcdef0 || inode != 0xfeedfacecafebeef {
		t.Errorf("decode = %x/%x/%v, want pair with ok", device, inode, ok)
	}

	// Rejections (Rust Identity::decode): all-zero payload, nonzero
	// tail beyond the pair, foreign kind.
	zero := LocalFileIdentity{Kind: identityKind}
	if _, _, ok := zero.deviceInode(); ok {
		t.Error("all-zero payload decoded")
	}
	tail := id
	tail.Bytes[16] = 1
	if _, _, ok := tail.deviceInode(); ok {
		t.Error("nonzero tail decoded")
	}
	foreign := id
	foreign.Kind = identityKind + 1
	if _, _, ok := foreign.deviceInode(); ok {
		t.Error("foreign kind decoded")
	}
}

// TestLocalFileIdentityComparable pins the value semantics of the
// portable identity (Rust derives PartialEq, Eq, Hash, Copy).
func TestLocalFileIdentityComparable(t *testing.T) {
	a := localIdentityFromDeviceInode(1, 2)
	b := localIdentityFromDeviceInode(1, 2)
	c := localIdentityFromDeviceInode(1, 3)
	if a != b {
		t.Error("equal identities compare unequal")
	}
	if a == c {
		t.Error("distinct identities compare equal")
	}
	// reflect.DeepEqual covers the [32]byte payload equality used by
	// the public facade.
	if !reflect.DeepEqual(a, b) {
		t.Error("DeepEqual identities differ")
	}
}
