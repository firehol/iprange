//go:build linux || darwin

package validation

// LiveCurrent validation tests (Rust tests/validation.rs live arms):
// the selected sweep over a registered live source, the bootstrap
// report over a live pair whose committed generation cannot be
// selected, and the terminal failure shapes of the live release
// machine. The writer and the source coordination require the proven
// live platforms, so the file carries the same linux || darwin tag as
// the live writer suite.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// liveValidationTestBudget is the live writer budget shared by the
// LiveCurrent validation tests (the proven 4-2 budget shape).
func liveValidationTestBudget() writer.PageBudget {
	return writer.PageBudget{MaxHeapBytes: 1 << 20, MaxPrivatePages: 4096, MaxGrowthPages: 4096, MaxOpenFiles: 2}
}

// createLiveValidationPair creates one live IPv4 direct pair and
// returns the main path.
func createLiveValidationPair(t *testing.T, capacity uint32) string {
	t.Helper()
	main := filepath.Join(t.TempDir(), "db.iprdb")
	if _, err := live.CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, capacity, nil); err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	return main
}

// openLiveValidationWriter opens the live writer for the validation
// tests; the caller must Close it.
func openLiveValidationWriter(t *testing.T, main string) *live.LiveWriter {
	t.Helper()
	w, err := live.OpenLiveWriter(main, liveValidationTestBudget(), nil, nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	return w
}

// findLiveValidationFindings runs one LiveCurrent validation into a
// slice and returns the result, the failure, and the findings.
func findLiveValidationFindings(t *testing.T, path string) (*ValidationResult, *ValidationFailure, []ValidationFinding) {
	t.Helper()
	var findings []ValidationFinding
	result, failure := Validate(path, ValidationModeLiveCurrent, HeapOnly(1<<20, 2), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	return result, failure, findings
}

// TestValidateLiveCurrentCleanSweep mirrors Rust
// live_current_validation_pins_and_releases_its_reader_slot: one
// committed direct generation validates cleanly while the writer is
// still open, and the released slot is immediately claimable by a
// fresh live reader.
func TestValidateLiveCurrentCleanSweep(t *testing.T) {
	liveGate(t)
	main := createLiveValidationPair(t, 1)
	w := openLiveValidationWriter(t, main)
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(100, 200, 9); err != nil || !changed {
		t.Fatalf("AssignV4: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Durability != live.CommitCommitted {
		t.Fatalf("durability = %v, want committed (%v)", result.Durability, result.Cause)
	}

	validated, failure, findings := findLiveValidationFindings(t, main)
	if failure != nil {
		t.Fatalf("live validation failed: %v", failure.Cause)
	}
	if !validated.Valid {
		t.Fatal("committed live generation reported invalid")
	}
	if len(findings) != 0 {
		t.Fatalf("findings %d, want none", len(findings))
	}
	if validated.Generation == nil || validated.Generation.TransactionID != 2 {
		t.Fatalf("generation %+v, want txn 2", validated.Generation)
	}
	if validated.Progress.FindingCount != 0 {
		t.Fatalf("progress %+v, want clean", validated.Progress)
	}

	// The validation claimed and released the only reader slot: a
	// fresh live reader opens and sees the committed range.
	r, err := live.OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("OpenLiveReader after validation: %v", err)
	}
	core, err := r.Core()
	if err != nil {
		t.Fatalf("live reader core: %v", err)
	}
	value, ok, err := core.LookupDirect4(150)
	if err != nil {
		t.Fatalf("LookupDirect4: %v", err)
	}
	if !ok || value != 9 {
		t.Fatalf("LookupDirect4(150) = %v,%v, want 9,true", value, ok)
	}
	if _, err := r.Close(); err != nil {
		t.Fatalf("live reader close: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
}

// TestValidateLiveCurrentEmptySweep validates a freshly created live
// pair with no commit (Rust empty_immutable_database_validates_
// explicitly): the empty sweep proves the two meta pages and the
// empty allocation partition through every validator, so the report
// is valid with no findings.
func TestValidateLiveCurrentEmptySweep(t *testing.T) {
	liveGate(t)
	main := createLiveValidationPair(t, 2)
	w := openLiveValidationWriter(t, main)
	if _, err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	validated, failure, findings := findLiveValidationFindings(t, main)
	if failure != nil {
		t.Fatalf("live validation failed: %v", failure.Cause)
	}
	if !validated.Valid {
		t.Fatal("empty sweep reported invalid")
	}
	if len(findings) != 0 {
		t.Fatalf("findings %d, want none", len(findings))
	}
	if validated.Generation == nil || validated.Generation.TransactionID != 1 {
		t.Fatalf("generation %+v, want txn 1", validated.Generation)
	}
}

// TestValidateLiveBootstrapReport mirrors Rust
// unselectable_live_generation_is_a_completed_invalid_report: one
// identity-readable meta page with a broken checksum makes the
// committed-generation selection unprovable while the raw pair still
// binds the sidecar database id, so the open falls to the bootstrap
// registration and the sweep reports the MetaInvalid finding with the
// untraversable mark, then releases the gate-held registration.
func TestValidateLiveBootstrapReport(t *testing.T) {
	liveGate(t)
	main := createLiveValidationPair(t, 1)
	w := openLiveValidationWriter(t, main)
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if _, err := w.AssignV4(1, 2, 7); err != nil {
		t.Fatalf("AssignV4: %v", err)
	}
	if result, err := w.Commit(nil); err != nil || result.Durability != live.CommitCommitted {
		t.Fatalf("Commit: %v (result %+v)", err, result)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Corrupt the page-0 checksum only: ParseIdentity still reads the
	// database id from page 0, but the checksum proof fails, so the
	// live-mode selection has one valid page and reports the
	// CurrentGenerationUnprovable problem (bootstrap.Open). The binding
	// arm (DatabaseIDFromPages) keeps working on the same pair.
	f, err := os.OpenFile(main, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, format.PageSize)
	if _, err := f.ReadAt(page, 0); err != nil {
		t.Fatal(err)
	}
	format.PutU32(page[252:256], 0) // broken CRC
	if _, err := f.WriteAt(page, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	validated, failure, findings := findLiveValidationFindings(t, main)
	if failure != nil {
		t.Fatalf("live bootstrap validation failed: %v", failure.Cause)
	}
	if validated.Valid {
		t.Fatal("unselectable generation reported valid")
	}
	if validated.Generation != nil {
		t.Fatal("bootstrap report carries a generation")
	}
	if len(findings) != 1 {
		t.Fatalf("findings %d, want 1", len(findings))
	}
	if findings[0].Reason != ReasonMetaInvalid || findings[0].Object != ObjectMeta {
		t.Fatalf("finding %+v", findings[0])
	}
	if validated.Progress.FindingCount != 1 || validated.Progress.UntraversableSubgraphs != 1 || !validated.Progress.HasUnboundedUnknown {
		t.Fatalf("progress %+v", validated.Progress)
	}
}

// TestValidateLiveCurrentCancellationPolls mirrors Rust
// live_validation_checks_cancellation_across_reader_capacity: the
// live open scans every reader slot through the checkpoint hook, so
// an uncancelled counter observes at least one poll per slot and the
// sweep still validates cleanly.
func TestValidateLiveCurrentCancellationPolls(t *testing.T) {
	liveGate(t)
	main := createLiveValidationPair(t, 64)
	var polls int
	result, failure := Validate(main, ValidationModeLiveCurrent, HeapOnly(1<<20, 2), func() error {
		polls++
		return nil
	}, nil)
	if failure != nil {
		t.Fatalf("live validation failed: %v", failure.Cause)
	}
	if !result.Valid {
		t.Fatal("capacity-64 live pair reported invalid")
	}
	if polls < 64 {
		t.Fatalf("checkpoint polls %d, want >= 64", polls)
	}
}

// TestValidateLiveFinalCheckFailure mirrors Rust
// failed_live_validation_keeps_the_final_check_cause: moving the
// sidecar away between the open and the terminal makes the final
// check fail with the coordination class; the release stays clean, so
// the failure carries no cleanup guard.
func TestValidateLiveFinalCheckFailure(t *testing.T) {
	liveGate(t)
	// A realistic final-check failure needs the source open to stay
	// alive across the mutation, which the Validate entry cannot
	// express: the open and the terminal are one call. The unit below
	// pins the terminal shape through the package helpers: a final
	// check failure folds into the LiveSourceEnd cause, and a clean
	// release keeps the residue flag clear. The residue-carrying
	// release fold is pinned by the live package unit test
	// TestLiveTerminalResultResidue.
	main := createLiveValidationPair(t, 2)
	w := openLiveValidationWriter(t, main)
	if _, err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	source, err := live.OpenLiveSourceCurrent(main, nil)
	if err != nil {
		t.Fatalf("OpenLiveSourceCurrent: %v", err)
	}
	sidecar, err := live.CanonicalSidecarPath(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sidecar, sidecar+".saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(sidecar+".saved", sidecar)
	end := source.FinishCurrent(nil)
	if end.Cause == nil {
		t.Fatal("final check accepted the moved sidecar")
	}
	var fe *format.Error
	if !errors.As(end.Cause, &fe) || fe.Code != format.CodeLiveRecoveryCoordinationUnavailable {
		t.Fatalf("final check class %v", end.Cause)
	}
	if end.Residue {
		t.Fatal("clean release reported residue")
	}
}

// TestValidateLiveOpenFailureNoSidecar reports the coordination class
// for a live-mode open of an immutable-only file (Rust open_sidecar_
// locked maps the sidecar open to live_coordination); the open
// failure carries no residue because nothing was claimed.
func TestValidateLiveOpenFailureNoSidecar(t *testing.T) {
	path := metaDB(t, 4)
	_, failure, _ := findLiveValidationFindings(t, path)
	if failure == nil {
		t.Fatal("live validation accepted a file without a reader table")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeLiveRecoveryCoordinationUnavailable {
		t.Fatalf("open failure class %v", failure.Cause)
	}
	if failure.CoordinationCleanup != publication.CoordinationCleanupNone {
		t.Fatalf("unclaimed open failure reported cleanup %v", failure.CoordinationCleanup)
	}
}

// TestValidateLiveBootstrapVerifyFailure mirrors Rust verify failure
// on the bootstrap arm: a moved sidecar fails the terminal verify and
// the release folds the coordination class with the gate-held
// registration (the verify runs before the gate unlock, so the
// failure surfaces from Finish).
func TestValidateLiveBootstrapVerifyFailure(t *testing.T) {
	liveGate(t)
	main := createLiveValidationPair(t, 1)
	w := openLiveValidationWriter(t, main)
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if _, err := w.AssignV4(1, 2, 7); err != nil {
		t.Fatalf("AssignV4: %v", err)
	}
	if result, err := w.Commit(nil); err != nil || result.Durability != live.CommitCommitted {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The bootstrap arm opens only when the committed generation is
	// unselectable but the raw pair still binds the sidecar id:
	// corrupt the page-0 checksum (page 1 keeps the identity), then
	// move the sidecar so the terminal verify fails.
	f, err := os.OpenFile(main, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, format.PageSize)
	if _, err := f.ReadAt(page, 0); err != nil {
		t.Fatal(err)
	}
	format.PutU32(page[252:256], 0)
	if _, err := f.WriteAt(page, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sidecar, err := live.CanonicalSidecarPath(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sidecar, sidecar+".saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(sidecar+".saved", sidecar)

	_, failure, _ := findLiveValidationFindings(t, main)
	if failure == nil {
		t.Fatal("bootstrap validation accepted the moved sidecar")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeLiveRecoveryCoordinationUnavailable {
		t.Fatalf("verify failure class %v", failure.Cause)
	}
}
