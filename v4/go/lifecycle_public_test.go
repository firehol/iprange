// Public CreateLive/InitializeLive round trip (Rust
// tests/live_transitions.rs + tests/live_roundtrip.rs create/init
// cases through the public facade).

package iprangedb

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
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

// exchangeAvailable reports whether the host has the atomic name
// exchange required by rollback-safe resets (Rust
// namespace::exchange_available; linux/apple true, other POSIX false).
func exchangeAvailable() bool { return mapping.ExchangeAvailable() }

func TestPublicCreateLiveAndInitializeRoundTrip(t *testing.T) {
	requireLiveCreation(t)
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
	requireLiveCreation(t)
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

// TestPublicImmutableMainIsInitializedExplicitly drives the full
// initialize -> resolve-complete round trip through the public facade
// (Rust tests/live_transitions.rs::immutable_main_is_initialized_explicitly):
// the immutable main reports transaction 1, initialize prepares the
// canonical sidecar, the exact-identity resolver completes it, the
// immutable reader then refuses the live pair, and the live reader
// serves the unchanged generation.
func TestPublicImmutableMainIsInitializedExplicitly(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
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

	immutable, err := OpenImmutable(main)
	if err != nil {
		t.Fatal(err)
	}
	immutableInfo, err := immutable.Info()
	if err != nil {
		immutable.Close()
		t.Fatal(err)
	}
	if immutableInfo.TransactionID != 1 {
		immutable.Close()
		t.Fatalf("immutable txn = %d, want 1", immutableInfo.TransactionID)
	}
	immutable.Close()

	result, err := InitializeLive(main, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != LiveTransitionInitialize {
		t.Fatalf("operation = %v, want Initialize", result.Operation)
	}
	if result.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", result.Status)
	}
	if result.NewSidecarLocation != LiveCoordinationLocationCanonical {
		t.Fatalf("location = %v, want Canonical", result.NewSidecarLocation)
	}
	if result.ReaderCapacity != 3 {
		t.Fatalf("capacity = %d, want 3", result.ReaderCapacity)
	}
	if result.Cause != nil {
		t.Fatalf("cause = %v, want nil", result.Cause)
	}
	resolved, err := ResolveLiveTransition(main, result, LiveTransitionResolutionComplete, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusInitialized {
		t.Fatalf("resolved status = %v, want Initialized", resolved.Status)
	}

	if _, err := OpenImmutable(main); err == nil {
		t.Fatal("immutable open succeeded on the live pair")
	} else if lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("immutable open on live pair: %v, want WrongState", err)
	}
	lr, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := lr.Info()
	if err != nil {
		lr.Close()
		t.Fatal(err)
	}
	if info.TransactionID != 1 {
		lr.Close()
		t.Fatalf("live reader txn = %d, want 1", info.TransactionID)
	}
	lr.Close()
}

// TestPublicInitializationNeverRepairsExistingCoordination proves the
// initialize refusal on a ready pair leaves it untouched (Rust
// tests/live_transitions.rs::initialization_never_repairs_existing_coordination).
func TestPublicInitializationNeverRepairsExistingCoordination(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 1, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := InitializeLive(main, 2, nil); err == nil {
		t.Fatal("initialize succeeded with existing coordination")
	} else if lifecycleCode(err) != ErrorWrongState {
		t.Fatalf("initialize with existing sidecar: %v, want WrongState", err)
	}
	lr, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	lr.Close()
}

// TestPublicResetReplacesCorruptCoordinationWithoutChangingTheMain
// drives a rollback-safe reset over a corrupt sidecar and proves the
// main bytes never change, the exchanged reset resolves, and the fresh
// capacity applies (Rust
// tests/live_transitions.rs::reset_replaces_corrupt_coordination_without_changing_the_main).
// The rollback-safe exchange is linux/apple only.
func TestPublicResetReplacesCorruptCoordinationWithoutChangingTheMain(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("rollback-safe exchange is linux/apple only")
	}
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 1, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main+".readers", []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ResetLiveCoordination(main, 2, LiveResetRollbackSafe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != LiveTransitionReset {
		t.Fatalf("operation = %v, want Reset", result.Operation)
	}
	if result.ResetPolicy == nil || *result.ResetPolicy != LiveResetRollbackSafe {
		t.Fatalf("reset policy = %v, want RollbackSafe", result.ResetPolicy)
	}
	if result.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", result.Status)
	}
	if result.NewSidecarLocation != LiveCoordinationLocationCanonical {
		t.Fatalf("location = %v, want Canonical", result.NewSidecarLocation)
	}
	if result.PreviousSidecarIdentity == nil || result.NewSidecarIdentity == nil {
		t.Fatal("reset identities missing")
	}
	if *result.PreviousSidecarIdentity == *result.NewSidecarIdentity {
		t.Fatal("reset reused the previous sidecar identity")
	}
	after, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("reset changed the main bytes")
	}
	resolved, err := ResolveLiveTransition(main, result, LiveTransitionResolutionComplete, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusInitialized {
		t.Fatalf("resolved status = %v, want Initialized", resolved.Status)
	}

	first, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenLiveReader(main, nil)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if _, err := OpenLiveReader(main, nil); err == nil {
		first.Close()
		second.Close()
		t.Fatal("third live reader opened beyond capacity")
	} else if lifecycleCode(err) != ErrorReaderCapacityExhausted {
		first.Close()
		second.Close()
		t.Fatalf("third live reader: %v, want ReaderCapacityExhausted", err)
	}
	first.Close()
	second.Close()
}

// TestPublicDiscardingResetReportsPolicyAndCannotRollBackAfterInstallation
// proves the discarding reset reports its policy, cannot restore the
// previous sidecar under rollback, and leaves a readable live pair
// (Rust tests/live_transitions.rs::discarding_reset_reports_policy_and_cannot_roll_back_after_installation).
func TestPublicDiscardingResetReportsPolicyAndCannotRollBackAfterInstallation(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 1, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main+".readers", []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ResetLiveCoordination(main, 2, LiveResetDiscardPrevious, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", result.Status)
	}
	if result.ResetPolicy == nil || *result.ResetPolicy != LiveResetDiscardPrevious {
		t.Fatalf("reset policy = %v, want DiscardPrevious", result.ResetPolicy)
	}
	after, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("discarding reset changed the main bytes")
	}
	canonical, err := os.ReadFile(main + ".readers")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveLiveTransition(main, result, LiveTransitionResolutionRollback, nil); err == nil {
		t.Fatal("discarding reset rolled back after installation")
	} else if lifecycleCode(err) != ErrorUnresolvable {
		t.Fatalf("rollback after discarding reset: %v, want Unresolvable", err)
	}
	canonicalAfter, err := os.ReadFile(main + ".readers")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, canonicalAfter) {
		t.Fatal("failed rollback changed the canonical sidecar")
	}

	lr, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	lr.Close()
}

// TestPublicCancelledTransitionLeavesAnImmutableMainUnchanged proves
// a cancelled initialize changes no artifact and the immutable reader
// still serves generation 1 (Rust
// tests/live_transitions.rs::cancelled_transition_leaves_an_immutable_main_unchanged).
func TestPublicCancelledTransitionLeavesAnImmutableMainUnchanged(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
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
	before, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := NewCancellationToken()
	cancelled.Cancel()

	if _, err := InitializeLive(main, 2, cancelled); err == nil {
		t.Fatal("cancelled initialize succeeded")
	} else if lifecycleCode(err) != ErrorCancelled {
		t.Fatalf("cancelled initialize: %v, want Cancelled", err)
	}
	after, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled initialize changed the main bytes")
	}
	if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("cancelled initialize left a sidecar: %v", err)
	}
	immutable, err := OpenImmutable(main)
	if err != nil {
		t.Fatal(err)
	}
	immutableInfo, err := immutable.Info()
	if err != nil {
		immutable.Close()
		t.Fatal(err)
	}
	if immutableInfo.TransactionID != 1 {
		immutable.Close()
		t.Fatalf("immutable txn = %d, want 1", immutableInfo.TransactionID)
	}
	immutable.Close()
}
