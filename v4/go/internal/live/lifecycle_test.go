// Functional lifecycle tests for CreateLive and InitializeLive (Rust
// tests/live_transitions.rs + tests/live_roundtrip.rs create/init
// cases, minus the resolver and live-reader surfaces that land in
// later chunks). The crash-point state matrix lives in
// lifecycle_crash_test.go under the v4work tag.

package live

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// neverCheck is the uncancellable checkpoint.
func neverCheck() error { return nil }

// cancelledCheck is one pre-cancelled checkpoint.
func cancelledCheck() error {
	return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
}

// failsAfter returns one checkpoint that fails with CodeCancelled on
// and after the nth call (Rust CancellationToken checked between every
// bounded step).
func failsAfter(n int) func() error {
	calls := 0
	return func() error {
		calls++
		if calls > n {
			return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
		}
		return nil
	}
}

// TestCreateLiveMidFlowCancellationCleansTheSidecar exercises the
// late-failure cleanup cascade functionally: a cancellation after the
// sidecar reservation removes the reserved sidecar exactly and reports
// NotCreated with the cancellation cause and no residue (Rust
// create_live failed-path cleanup).
func TestCreateLiveMidFlowCancellationCleansTheSidecar(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	result, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, failsAfter(2))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CreationStateNotCreated {
		t.Fatalf("state = %v, want NotCreated", result.State)
	}
	if result.ResiduePossible {
		t.Fatal("residue possible after clean cancellation cleanup")
	}
	expectCode(t, result.Cause, format.CodeCancelled)
	if _, err := os.Lstat(main); !os.IsNotExist(err) {
		t.Fatalf("main exists after cancelled create: %v", err)
	}
	if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after cancelled create: %v", err)
	}
}

// TestInitializeLiveMidFlowCancellationCleansTheSidecar exercises the
// initialize cleanup cascade functionally: a cancellation after the
// sidecar reservation removes the reserved sidecar, leaves the main
// byte-identical, and reports the cancellation cause (Rust
// initialize_live cleanup_created).
func TestInitializeLiveMidFlowCancellationCleansTheSidecar(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	result, err := InitializeLive(main, 2, failsAfter(3))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LiveTransitionStatusUnchanged {
		t.Fatalf("status = %v, want Unchanged", result.Status)
	}
	if result.ResiduePossible {
		t.Fatal("residue possible after clean cancellation cleanup")
	}
	expectCode(t, result.Cause, format.CodeCancelled)
	after, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled initialize changed the main")
	}
	if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after cancelled initialize: %v", err)
	}
}

// readBootstrap opens the main file read-only and proves its bootstrap.
func readBootstrap(t *testing.T, path string) *bootstrap.Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	result, err := bootstrapOf(f)
	if err != nil {
		t.Fatalf("bootstrap of %s: %v", path, err)
	}
	return result
}

// metaPagesAreIdentical reports whether both meta pages are byte-identical.
func metaPagesAreIdentical(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := mapping.MapFile(f, 2*format.PageSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p0, err := m.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(p0, p1)
}

// sidecarStateOf reports the header state of the canonical sidecar,
// or -1 when it is absent.
func sidecarStateOf(t *testing.T, main string) int {
	t.Helper()
	path, err := canonicalSidecarPath(main)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, state, err := openAny(path)
	if err != nil {
		if os.IsNotExist(err) {
			return -1
		}
		t.Fatalf("openAny(%s): %v", path, err)
	}
	sidecar.Close()
	return int(state)
}

func TestCreateLiveCreatesCompletePair(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	result, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", result.State)
	}
	if result.ResiduePossible {
		t.Fatal("residue possible on a clean create")
	}
	if result.Cause != nil {
		t.Fatalf("cause = %v, want nil", result.Cause)
	}
	if result.ReaderCapacity != 2 {
		t.Fatalf("capacity = %d, want 2", result.ReaderCapacity)
	}
	if result.DatabaseID == [16]byte{} || result.CommitNonce == [16]byte{} || result.SidecarID == [16]byte{} {
		t.Fatal("create left a zero identity draw")
	}
	if result.DirectoryIdentity == nil {
		t.Fatal("directory identity missing")
	}
	if result.MainIdentity == nil || result.SidecarIdentity == nil {
		t.Fatal("main or sidecar identity missing on a completed create")
	}
	if result.Housekeeping != housekeepingNone {
		t.Fatalf("housekeeping = %v, want none", result.Housekeeping)
	}
	if len(result.VisibleHousekeeping) != 0 {
		t.Fatalf("visible housekeeping = %v, want empty", result.VisibleHousekeeping)
	}
	if got := result.MainBasename.bytesValue(); string(got) != "db.iprdb" {
		t.Fatalf("basename = %q, want db.iprdb", got)
	}
	if result.MainBasename.encodingValue() != 1 {
		t.Fatalf("basename encoding = %d, want 1", result.MainBasename.encodingValue())
	}

	// The main is a complete empty txn-1 image: two pages, identical
	// meta pair, transaction 1, zero committed payload.
	boot := readBootstrap(t, main)
	if boot.Meta.TxnID != 1 {
		t.Fatalf("txn = %d, want 1", boot.Meta.TxnID)
	}
	if boot.CommittedBytes != 2*format.PageSize {
		t.Fatalf("committed = %d, want two pages", boot.CommittedBytes)
	}
	if !metaPagesAreIdentical(t, main) {
		t.Fatal("meta pages differ after create")
	}
	if boot.Meta.DatabaseID != result.DatabaseID || boot.Meta.CommitNonce != result.CommitNonce {
		t.Fatal("main meta does not carry the attempt identity")
	}

	// The sidecar is a ready table with the requested capacity and the
	// attempt identity.
	sidecar, state, err := openAny(main + ".readers")
	if err != nil {
		t.Fatal(err)
	}
	defer sidecar.Close()
	if state != stateReady {
		t.Fatalf("sidecar state = %d, want ready", state)
	}
	if sidecar.header.capacity != 2 {
		t.Fatalf("sidecar capacity = %d, want 2", sidecar.header.capacity)
	}
	if sidecar.header.databaseID != result.DatabaseID || sidecar.header.sidecarID != result.SidecarID {
		t.Fatal("sidecar header does not carry the attempt identity")
	}
}

func TestCreateLiveHardErrors(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")

	// Capacity zero is refused before any path access.
	_, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 0, neverCheck)
	expectCode(t, err, format.CodeInvalidArgument)

	// Invalid value/kind combinations (Rust validate_kinds).
	_, err = CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNetworkEnrichmentV1, [16]byte{}, 1, neverCheck)
	expectCode(t, err, format.CodeWrongStructureKind)
	_, err = CreateLive(main, format.AddressFamilyIPv4, format.ValueKindStructured, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	expectCode(t, err, format.CodeWrongStructureKind)

	// A reserved coordination name cannot name a main file.
	_, err = CreateLive(filepath.Join(dir, "db.iprdb.readers"), format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	expectCode(t, err, format.CodeInvalidArgument)

	// A pre-cancelled token aborts before any artifact.
	_, err = CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, cancelledCheck)
	expectCode(t, err, format.CodeCancelled)
	if _, err := os.Lstat(main); !os.IsNotExist(err) {
		t.Fatalf("cancelled create left a main: %v", err)
	}
	if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("cancelled create left a sidecar: %v", err)
	}
}

func TestCreateLiveRefusesExistingArtifacts(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	created, failure := createPrivate(main, cleanupAuthority{attemptID: [16]byte{1}, ordinal: 0, kind: cleanupKindOwnedMain, directoryRole: cleanupRoleMainFile})
	if failure != nil {
		t.Fatal(failure.cause)
	}
	created.file.Close()

	_, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	expectCode(t, err, format.CodeInvalidArgument)

	main2 := filepath.Join(dir, "db2.iprdb")
	created, failure = createPrivate(main2+".readers", cleanupAuthority{attemptID: [16]byte{2}, ordinal: 1, kind: cleanupKindOwnedCoordination, directoryRole: cleanupRoleMainFile})
	if failure != nil {
		t.Fatal(failure.cause)
	}
	created.file.Close()
	_, err = CreateLive(main2, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	expectCode(t, err, format.CodeInvalidArgument)
}

func TestCreateLiveMissingParentIsNotCreatedWithoutResidue(t *testing.T) {
	// Rust creation_failure_before_artifacts_is_reported_without_residue:
	// a missing parent reports the Io(NotFound) class as the attempt
	// cause with no artifacts and no residue.
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "database.iprdb")
	result, err := CreateLive(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CreationStateNotCreated {
		t.Fatalf("state = %v, want NotCreated", result.State)
	}
	if result.ResiduePossible {
		t.Fatal("residue possible without artifacts")
	}
	expectCode(t, result.Cause, format.CodeIO)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path exists after failed create: %v", err)
	}
	if _, err := os.Lstat(path + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after failed create: %v", err)
	}
}

func TestInitializeLiveConvertsQuiescentMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	created, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal(err)
	}

	result, err := InitializeLive(main, 3, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != LiveTransitionInitialize {
		t.Fatalf("operation = %v, want Initialize", result.Operation)
	}
	if result.ResetPolicy != nil {
		t.Fatalf("reset policy = %v, want nil", *result.ResetPolicy)
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
	if result.TransactionID != 1 {
		t.Fatalf("transaction = %d, want 1", result.TransactionID)
	}
	if result.DatabaseID != created.DatabaseID || result.CommitNonce != created.CommitNonce {
		t.Fatal("transition lost the main identity")
	}
	if result.DirectoryIdentity == nil || result.MainIdentity == nil {
		t.Fatal("directory or main identity missing")
	}
	if result.PreviousSidecarIdentity != nil {
		t.Fatal("previous sidecar identity must be nil for initialize")
	}
	if result.NewSidecarIdentity == nil {
		t.Fatal("new sidecar identity missing")
	}
	if result.ResiduePossible || result.Cause != nil {
		t.Fatalf("unexpected residue facts: residue=%v cause=%v", result.ResiduePossible, result.Cause)
	}
	if got := result.MainBasename.bytesValue(); string(got) != "db.iprdb" {
		t.Fatalf("basename = %q, want db.iprdb", got)
	}

	after, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("initialize changed the main file bytes")
	}
	sidecar, state, err := openAny(main + ".readers")
	if err != nil {
		t.Fatal(err)
	}
	defer sidecar.Close()
	if state != stateReady {
		t.Fatalf("sidecar state = %d, want ready", state)
	}
	if sidecar.header.capacity != 3 {
		t.Fatalf("sidecar capacity = %d, want 3", sidecar.header.capacity)
	}
	if sidecar.header.databaseID != result.DatabaseID || sidecar.header.sidecarID != result.SidecarID {
		t.Fatal("sidecar header does not carry the attempt identity")
	}
}

func TestInitializeLiveHardErrors(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")

	// Missing main -> NameNotFound.
	_, err := InitializeLive(main, 1, neverCheck)
	expectCode(t, err, format.CodeNameNotFound)

	// Not a v4 file -> FormatInvalid.
	if err := os.WriteFile(main, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InitializeLive(main, 1, neverCheck)
	expectCode(t, err, format.CodeFormatInvalid)

	// Capacity zero is refused before path access.
	_, err = InitializeLive(main, 0, neverCheck)
	expectCode(t, err, format.CodeInvalidArgument)

	// Pre-cancelled token aborts before any change.
	if _, err := CreateLive(filepath.Join(dir, "db2.iprdb"), format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck); err != nil {
		t.Fatal(err)
	}
	main2 := filepath.Join(dir, "db2.iprdb")
	if err := os.Remove(main2 + ".readers"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(main2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InitializeLive(main2, 2, cancelledCheck)
	expectCode(t, err, format.CodeCancelled)
	after, err := os.ReadFile(main2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled initialize changed the main")
	}
	if _, err := os.Lstat(main2 + ".readers"); !os.IsNotExist(err) {
		t.Fatalf("cancelled initialize left a sidecar: %v", err)
	}
}

func TestInitializeLiveRefusesExistingSidecar(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck); err != nil {
		t.Fatal(err)
	}
	_, err := InitializeLive(main, 2, neverCheck)
	expectCode(t, err, format.CodeWrongState)
}

func TestInitializeLiveRequiresExactCommittedLength(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal(err)
	}
	// An unpublished page-aligned tail: physical length 3 pages while
	// the meta commits 2 pages (Rust WrongState "offline transition
	// requires exact committed length").
	f, err := os.OpenFile(main, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(3 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = InitializeLive(main, 2, neverCheck)
	expectCode(t, err, format.CodeWrongState)
}
