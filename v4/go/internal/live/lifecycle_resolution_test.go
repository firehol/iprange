// Exact-identity transition resolution tests (Rust
// live_lifecycle/resolution_tests.rs): ResolveLiveTransition completes
// or rolls back one interrupted initialize or reset only when the
// observed canonical/private sidecars match the supplied attempt
// facts, and the main never changes.

package live

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// resolutionFiles is one live test pair plus one fresh attempt id
// (Rust resolution_tests::Files).
type resolutionFiles struct {
	main      string
	attemptID [16]byte
}

func (f *resolutionFiles) sidecar() string { return f.main + ".readers" }
func (f *resolutionFiles) private() string { return f.main + ".readers.reset" }

func (f *resolutionFiles) create(t *testing.T) {
	t.Helper()
	if _, err := CreateLive(f.main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck); err != nil {
		t.Fatal(err)
	}
}

// sidecarFacts is one prepared sidecar's supplied facts (Rust
// resolution_tests::SidecarFacts).
type sidecarFacts struct {
	id       [16]byte
	capacity uint32
	previous *FileIdentity
	identity FileIdentity
	location LiveCoordinationLocation
}

// suppliedResult assembles one LiveTransitionResult exactly like the
// Rust test helper (Rust resolution_tests::supplied).
func suppliedResult(operation LiveTransitionOperation, main *lockedMain, resetPolicy *LiveResetPolicy, sidecar sidecarFacts) *LiveTransitionResult {
	return &LiveTransitionResult{
		Operation:               operation,
		ResetPolicy:             resetPolicy,
		Status:                  LiveTransitionStatusOutcomeUnknown,
		DatabaseID:              main.bootstrap.Meta.DatabaseID,
		TransactionID:           main.bootstrap.Meta.TxnID,
		CommitNonce:             main.bootstrap.Meta.CommitNonce,
		DirectoryIdentity:       &main.directoryIdentity,
		MainIdentity:            &main.identity,
		MainBasename:            main.basename,
		ReaderCapacity:          sidecar.capacity,
		SidecarID:               sidecar.id,
		PreviousSidecarIdentity: sidecar.previous,
		NewSidecarIdentity:      &sidecar.identity,
		NewSidecarLocation:      sidecar.location,
		ResiduePossible:         true,
	}
}

// prepareInitialize creates the pair, removes the canonical sidecar,
// and prepares one fresh creating sidecar at the canonical name (Rust
// resolution_tests::prepare_initialize).
func prepareInitialize(t *testing.T, files *resolutionFiles) *LiveTransitionResult {
	t.Helper()
	files.create(t)
	if err := os.Remove(files.sidecar()); err != nil {
		t.Fatal(err)
	}
	main, err := openLockedMain(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	defer main.file.Close()
	sidecar, failure := reserve(files.main, main.bootstrap.Meta.DatabaseID, files.attemptID, 2)
	if failure != nil {
		t.Fatal(failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		sidecar.close()
		t.Fatal(err)
	}
	if err := syncParent(sidecar.path); err != nil {
		sidecar.close()
		t.Fatal(err)
	}
	result := suppliedResult(LiveTransitionInitialize, main, nil, sidecarFacts{
		id:       files.attemptID,
		capacity: 2,
		identity: sidecar.localIdentity(),
		location: LiveCoordinationLocationCanonical,
	})
	sidecar.close()
	return result
}

func TestResolutionInitializeCanBeCompletedExactly(t *testing.T) {
	files := resolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	id, err := uniqueAttemptID(files.main, 1)
	if err != nil {
		t.Fatal(err)
	}
	files.attemptID = id
	supplied := prepareInitialize(t, &files)

	resolved, err := ResolveLiveTransition(files.main, supplied, LiveTransitionResolutionComplete, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", resolved.Status)
	}
	reader, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResolutionInitializeCanBeRolledBackExactly(t *testing.T) {
	files := resolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	id, err := uniqueAttemptID(files.main, 1)
	if err != nil {
		t.Fatal(err)
	}
	files.attemptID = id
	supplied := prepareInitialize(t, &files)

	resolved, err := ResolveLiveTransition(files.main, supplied, LiveTransitionResolutionRollback, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusUnchanged {
		t.Fatalf("status = %v, want Unchanged", resolved.Status)
	}
	if _, err := os.Lstat(files.sidecar()); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after rollback: %v", err)
	}
	immutable, err := reader.OpenImmutable(files.main)
	if err != nil {
		t.Fatal(err)
	}
	if immutable.Meta().TxnID != 1 {
		immutable.Close()
		t.Fatalf("txn = %d, want 1", immutable.Meta().TxnID)
	}
	immutable.Close()
}

func TestResolutionResetOverCorruptCoordinationCanBeCompleted(t *testing.T) {
	files := resolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	id, err := uniqueAttemptID(files.main, 1)
	if err != nil {
		t.Fatal(err)
	}
	files.attemptID = id
	files.create(t)
	if err := os.WriteFile(files.sidecar(), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	main, err := openLockedMain(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := existingIdentity(files.sidecar())
	if err != nil {
		main.file.Close()
		t.Fatal(err)
	}
	if previous == nil {
		main.file.Close()
		t.Fatal("corrupt sidecar identity missing")
	}
	sidecar, failure := reserveAt(files.private(), main.bootstrap.Meta.DatabaseID, files.attemptID, 2)
	if failure != nil {
		main.file.Close()
		t.Fatal(failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	if err := sidecar.publishReady(); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	if err := syncParent(sidecar.path); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	discard := LiveResetDiscardPrevious
	supplied := suppliedResult(LiveTransitionReset, main, &discard, sidecarFacts{
		id:       files.attemptID,
		capacity: 2,
		previous: previous,
		identity: sidecar.localIdentity(),
		location: LiveCoordinationLocationPrivate,
	})
	sidecar.close()
	main.file.Close()

	resolved, err := ResolveLiveTransition(files.main, supplied, LiveTransitionResolutionComplete, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", resolved.Status)
	}
	first, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	first.Close()
	second.Close()
}

func TestResolutionExchangedResetCleansTheExactPreviousSidecar(t *testing.T) {
	if !mapping.ExchangeAvailable() {
		t.Skip("atomic name exchange is linux/apple only")
	}
	files := resolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	id, err := uniqueAttemptID(files.main, 1)
	if err != nil {
		t.Fatal(err)
	}
	files.attemptID = id
	files.create(t)
	if err := os.WriteFile(files.sidecar(), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	main, err := openLockedMain(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := existingIdentity(files.sidecar())
	if err != nil {
		main.file.Close()
		t.Fatal(err)
	}
	if previous == nil {
		main.file.Close()
		t.Fatal("corrupt sidecar identity missing")
	}
	sidecar, failure := reserveAt(files.private(), main.bootstrap.Meta.DatabaseID, files.attemptID, 2)
	if failure != nil {
		main.file.Close()
		t.Fatal(failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	if err := sidecar.publishReady(); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	rollbackSafe := LiveResetRollbackSafe
	supplied := suppliedResult(LiveTransitionReset, main, &rollbackSafe, sidecarFacts{
		id:       files.attemptID,
		capacity: 2,
		previous: previous,
		identity: sidecar.localIdentity(),
		location: LiveCoordinationLocationCanonical,
	})
	if err := install(files.private(), files.sidecar(), sidecar.file, sidecar.localIdentity(), previous, LiveResetRollbackSafe); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	if err := syncParent(files.sidecar()); err != nil {
		sidecar.close()
		main.file.Close()
		t.Fatal(err)
	}
	sidecar.close()
	main.file.Close()

	resolved, err := ResolveLiveTransition(files.main, supplied, LiveTransitionResolutionComplete, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != LiveTransitionStatusInitialized {
		t.Fatalf("status = %v, want Initialized", resolved.Status)
	}
	if _, err := os.Lstat(files.private()); !os.IsNotExist(err) {
		t.Fatalf("private sidecar survived the exchanged cleanup: %v", err)
	}
	lr, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	lr.Close()
}
