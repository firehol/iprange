// Exact-identity creation resolution tests (Rust
// live_lifecycle/create_resolution_tests.rs): ResolveCreateLive
// completes or rolls back an interrupted CreateLive only when the
// observed artifacts match the supplied attempt, and never removes a
// ready pair.

package live

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// createResolutionFiles is one live pair under test (Rust
// create_resolution_tests::Files).
type createResolutionFiles struct {
	main string
}

func (f *createResolutionFiles) sidecar() string { return f.main + ".readers" }

func (f *createResolutionFiles) create(t *testing.T) *CreateResult {
	t.Helper()
	result, err := CreateLive(f.main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// interruptedSidecarOnly removes the pair and re-prepares one creating
// sidecar at the canonical name with the original attempt identity
// (Rust create_resolution_tests::Files::interrupted_sidecar_only).
func (f *createResolutionFiles) interruptedSidecarOnly(t *testing.T, created *CreateResult) *CreateResult {
	t.Helper()
	if err := os.Remove(f.main); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.sidecar()); err != nil {
		t.Fatal(err)
	}
	sidecar, failure := reserve(f.main, created.DatabaseID, created.SidecarID, created.ReaderCapacity)
	if failure != nil {
		t.Fatal(failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		sidecar.Close()
		t.Fatal(err)
	}
	if err := syncParent(sidecar.path); err != nil {
		sidecar.Close()
		t.Fatal(err)
	}
	supplied := *created
	supplied.State = CreationStateOutcomeUnknown
	supplied.ResiduePossible = true
	supplied.MainIdentity = nil
	identity := sidecar.localIdentity()
	supplied.SidecarIdentity = &identity
	sidecar.Close()
	return &supplied
}

func TestCreateResolutionSidecarOnlyCanBeCompleted(t *testing.T) {
	files := createResolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	supplied := files.interruptedSidecarOnly(t, files.create(t))

	resolved, err := ResolveCreateLive(files.main, supplied, LiveTransitionResolutionComplete, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", resolved.State)
	}
	if resolved.MainIdentity == nil {
		t.Fatal("main identity missing after completion")
	}
	lr, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	lr.Close()
}

func TestCreateResolutionSidecarOnlyCanBeRolledBack(t *testing.T) {
	files := createResolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	supplied := files.interruptedSidecarOnly(t, files.create(t))

	resolved, err := ResolveCreateLive(files.main, supplied, LiveTransitionResolutionRollback, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != CreationStateNotCreated {
		t.Fatalf("state = %v, want NotCreated", resolved.State)
	}
	if _, err := os.Lstat(files.main); !os.IsNotExist(err) {
		t.Fatalf("main exists after rollback: %v", err)
	}
	if _, err := os.Lstat(files.sidecar()); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after rollback: %v", err)
	}
}

func TestCreateResolutionReadyPairIsNeverRemoved(t *testing.T) {
	files := createResolutionFiles{main: filepath.Join(t.TempDir(), "db.iprdb")}
	created := files.create(t)

	resolved, err := ResolveCreateLive(files.main, created, LiveTransitionResolutionRollback, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", resolved.State)
	}
	lr, err := OpenLiveReader(files.main, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	lr.Close()
}
