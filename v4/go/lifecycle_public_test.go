// Public CreateLive/InitializeLive round trip (Rust
// tests/live_transitions.rs + tests/live_roundtrip.rs create/init
// cases through the public facade).

package iprangedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// lifecycleCode extracts the public error code from either the public
// Error type (cancellation checkpoints) or the internal format error
// (lifecycle internals); 0 for other errors or nil.
func lifecycleCode(err error) ErrorCode {
	var fe *format.Error
	if errors.As(err, &fe) {
		return ErrorCode(fe.Code)
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return 0
}

func TestPublicCreateLiveAndInitializeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}

	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", created.State)
	}
	if created.ResiduePossible || created.Cause != nil {
		t.Fatalf("unexpected residue facts: %v %v", created.ResiduePossible, created.Cause)
	}
	if created.Family != AddressFamilyIPv4 || created.ValueKind != ValueKindDirect || created.StructureKind != StructureKindNone {
		t.Fatalf("created kinds = %v/%v/%v, want ipv4/direct/none", created.Family, created.ValueKind, created.StructureKind)
	}
	if created.ValueTag.Wire() != tag.Wire() {
		t.Fatal("created tag mismatch")
	}
	if created.DatabaseID == [16]byte{} || created.CommitNonce == [16]byte{} || created.SidecarID == [16]byte{} {
		t.Fatal("create left a zero identity draw")
	}
	if created.DirectoryIdentity == nil || created.MainIdentity == nil || created.SidecarIdentity == nil {
		t.Fatal("identity pointers missing on a completed create")
	}
	if created.DirectoryIdentity.Kind != 1 || created.MainIdentity.Kind != 1 || created.SidecarIdentity.Kind != 1 {
		t.Fatalf("identity kinds = %d/%d/%d, want 1", created.DirectoryIdentity.Kind, created.MainIdentity.Kind, created.SidecarIdentity.Kind)
	}
	if created.DirectoryIdentity.Bytes == [32]byte{} || created.MainIdentity.Bytes == [32]byte{} || created.SidecarIdentity.Bytes == [32]byte{} {
		t.Fatal("identity bytes empty")
	}
	if got := created.MainBasename.Bytes(); string(got) != "db.iprdb" {
		t.Fatalf("basename = %q, want db.iprdb", got)
	}
	if created.MainBasename.Encoding() != 1 {
		t.Fatalf("basename encoding = %d, want 1", created.MainBasename.Encoding())
	}
	if created.ReaderCapacity != 2 {
		t.Fatalf("capacity = %d, want 2", created.ReaderCapacity)
	}
	if created.Housekeeping != HousekeepingNone || len(created.VisibleHousekeeping) != 0 {
		t.Fatalf("housekeeping = %v %v, want none", created.Housekeeping, created.VisibleHousekeeping)
	}

	// The immutable reader refuses a live pair (Rust live_roundtrip:
	// ImmutableReader::open fails after create); the live pair refuses
	// initialization while its sidecar exists.
	if _, err := OpenImmutable(main); err == nil {
		t.Fatal("immutable open succeeded on a live pair")
	} else if lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("immutable open on live pair: %v, want WrongState", err)
	}
	if _, err := InitializeLive(main, 3, nil); err == nil {
		t.Fatal("initialize succeeded with an existing sidecar")
	} else if lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("initialize with existing sidecar: %v, want WrongState", err)
	}

	// Remove the sidecar and initialize the quiescent main.
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal(err)
	}
	transitioned, err := InitializeLive(main, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transitioned.Operation != LiveTransitionInitialize {
		t.Fatalf("operation = %v, want Initialize", transitioned.Operation)
	}
	if transitioned.ResetPolicy != nil {
		t.Fatalf("reset policy = %v, want nil", *transitioned.ResetPolicy)
	}
	if transitioned.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", transitioned.Status)
	}
	if transitioned.NewSidecarLocation != LiveCoordinationLocationCanonical {
		t.Fatalf("location = %v, want Canonical", transitioned.NewSidecarLocation)
	}
	if transitioned.TransactionID != 1 || transitioned.ReaderCapacity != 3 {
		t.Fatalf("transition = txn %d capacity %d, want 1/3", transitioned.TransactionID, transitioned.ReaderCapacity)
	}
	if transitioned.DatabaseID != created.DatabaseID || transitioned.CommitNonce != created.CommitNonce {
		t.Fatal("transition lost the main identity")
	}
	if transitioned.PreviousSidecarIdentity != nil || transitioned.NewSidecarIdentity == nil {
		t.Fatal("sidecar identity facts wrong for initialize")
	}
	if transitioned.MainIdentity == nil || transitioned.DirectoryIdentity == nil {
		t.Fatal("main or directory identity missing")
	}
	if transitioned.MainBasename.Bytes() == nil || string(transitioned.MainBasename.Bytes()) != "db.iprdb" {
		t.Fatalf("main basename = %q, want db.iprdb", transitioned.MainBasename.Bytes())
	}
	if transitioned.ResiduePossible || transitioned.Cause != nil {
		t.Fatalf("unexpected transition residue: %v %v", transitioned.ResiduePossible, transitioned.Cause)
	}
}

func TestPublicLifecycleCancellation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	cancelled := NewCancellationToken()
	cancelled.Cancel()

	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}, 1, cancelled); err == nil {
		t.Fatal("cancelled create succeeded")
	} else if lifecycleCode(err) != ErrorCancelled {
		t.Fatalf("cancelled create: %v, want Cancelled", err)
	}
	if _, err := os.Lstat(main); !os.IsNotExist(err) {
		t.Fatalf("cancelled create left a main: %v", err)
	}

	// A quiescent main must remain untouched by a cancelled initialize.
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeLive(main, 2, cancelled); err == nil {
		t.Fatal("cancelled initialize succeeded")
	} else if lifecycleCode(err) != ErrorCancelled {
		t.Fatalf("cancelled initialize: %v, want Cancelled", err)
	}
	if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("cancelled initialize left a sidecar: %v", err)
	}
}
