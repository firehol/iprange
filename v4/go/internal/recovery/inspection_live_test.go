//go:build linux || darwin

package recovery

// Live-candidate inspection tests ported from the Rust
// recovery/inspection_tests.rs: the unprovable-current and
// unreadable-proven-current refusals, the healthy newest-only
// projection, and the offline validation of a committed live
// generation. The writer and the source coordination require the
// proven live platforms, so the file carries the same linux || darwin
// tag as the live writer suite.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// liveRecoveryTestBudget is the live writer budget shared by the
// recovery live tests (the proven live validation budget shape).
func liveRecoveryTestBudget() writer.PageBudget {
	return writer.PageBudget{MaxHeapBytes: 1 << 20, MaxPrivatePages: 4096, MaxGrowthPages: 4096, MaxOpenFiles: 2}
}

// createLiveRecoveryPair creates one live IPv4 direct pair and
// returns the main path.
func createLiveRecoveryPair(t *testing.T) string {
	t.Helper()
	main := filepath.Join(t.TempDir(), "db.iprdb")
	if _, err := live.CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, nil); err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	return main
}

func TestLiveInspectionReportsUnprovableCurrentOrder(t *testing.T) {
	main := createLiveRecoveryPair(t)
	rewriteMeta(t, main, 0, func(meta *format.Meta) {
		meta.CommitNonce = [16]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	})
	_, err := inspect(t, main, RecoveryInspectionLive)
	if err == nil {
		t.Fatal("unprovable order accepted")
	}
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeLiveRecoveryCurrentGenerationUnprovable {
		t.Fatalf("cause %v, want LiveRecoveryCurrentGenerationUnprovable", err)
	}
}

func TestLiveInspectionReportsUnreadableProvenCurrent(t *testing.T) {
	main := createLiveRecoveryPair(t)
	w, err := live.OpenLiveWriter(main, liveRecoveryTestBudget(), nil, nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(10, 20, 7); err != nil || !changed {
		t.Fatalf("AssignV4: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Durability != live.CommitCommitted {
		t.Fatalf("durability = %v, want committed (cause %v)", result.Durability, result.Cause)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The committed current page (page 0, txn 2) loses its
	// recovery-valid surface: records without a root become
	// impossible, so the proven current is unreadable.
	rewriteMeta(t, main, 0, func(meta *format.Meta) {
		meta.RangeRecordCount = 0
	})
	_, err = inspect(t, main, RecoveryInspectionLive)
	if err == nil {
		t.Fatal("unreadable proven current accepted")
	}
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeLiveRecoveryCurrentGenerationUnreadable {
		t.Fatalf("cause %v, want LiveRecoveryCurrentGenerationUnreadable", err)
	}
}

func TestLiveInspectionReportsOnlyTheNewestCandidate(t *testing.T) {
	main := createLiveRecoveryPair(t)
	result, err := inspect(t, main, RecoveryInspectionLive)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.CandidateCount() != 1 {
		t.Fatalf("candidate count %d, want 1", result.CandidateCount())
	}
	candidate := result.Candidate(0)
	if candidate.Label != CandidateNewest || candidate.MetaPage != 1 || candidate.TransactionID != 1 {
		t.Fatalf("candidate %+v, want newest page 1 txn 1", candidate)
	}
	if result.Progress.FindingCount != 0 {
		t.Fatalf("progress %+v, want clean", result.Progress)
	}
}

func TestValidateOfflineCandidateCommittedLiveGeneration(t *testing.T) {
	main := createLiveRecoveryPair(t)
	w, err := live.OpenLiveWriter(main, liveRecoveryTestBudget(), nil, nil)
	if err != nil {
		t.Fatalf("OpenLiveWriter: %v", err)
	}
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(100, 200, 9); err != nil || !changed {
		t.Fatalf("AssignV4: changed=%v err=%v", changed, err)
	}
	if _, err := w.Commit(nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	result, err := inspect(t, main, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	candidate := result.Candidate(0)
	if candidate.Label != CandidateNewest || candidate.MetaPage != 0 || candidate.TransactionID != 2 {
		t.Fatalf("candidate %+v, want newest page 0 txn 2", candidate)
	}
	validated, failure := ValidateOfflineCandidate(main, candidate, validation.HeapOnly(1<<20, 2), nil, nil)
	if failure != nil {
		t.Fatalf("validate: %v", failure.Cause)
	}
	if !validated.Valid {
		t.Fatalf("valid = false, progress %+v", validated.Progress)
	}
	if validated.Generation == nil || validated.Generation.TransactionID != 2 || validated.Generation.Roots[0] == 0 {
		t.Fatalf("generation %+v, want committed txn 2 with a range root", validated.Generation)
	}
}
