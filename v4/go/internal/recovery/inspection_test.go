package recovery

// Inspection and offline-candidate validation tests: the mode and
// budget preflights, the immutable and offline candidate projections
// over raw meta pairs, the classification progress over absent and
// invalid pages, and the offline validation terminal (identity,
// selection, sweep, verification). The live arms live in
// inspection_live_test.go on the proven-live platforms.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// metaDBFile writes one database main with the given page count: page
// 0 (and page 1 when pages > 1) carries the valid transaction-1 meta
// image, every remaining page is zero.
func metaDBFile(t *testing.T, pages uint64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	page := directMetaPage(1, nil)
	for i := uint64(0); i < pages; i++ {
		buf := make([]byte, format.PageSize)
		if i < 2 {
			copy(buf, page)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// inspect runs one inspection with the shared test budget.
func inspect(t *testing.T, path string, mode RecoveryInspectionMode) (*RecoveryCandidateInspection, error) {
	t.Helper()
	return InspectRecoveryCandidates(path, mode, validation.HeapOnly(1<<20, 2), nil)
}

func TestInspectionPreflights(t *testing.T) {
	path := metaDBFile(t, 2)
	if _, err := InspectRecoveryCandidates(path, RecoveryInspectionImmutable, nil, nil); err == nil {
		t.Fatal("nil budget accepted")
	}
	if _, err := InspectRecoveryCandidates(path, RecoveryInspectionImmutable, validation.HeapOnly(1<<20, 0), nil); err == nil {
		t.Fatal("zero open files accepted")
	}
	if _, err := InspectRecoveryCandidates(path, RecoveryInspectionLive, validation.HeapOnly(1<<20, 1), nil); err == nil {
		t.Fatal("live inspection with one open file accepted")
	}
	if _, err := InspectRecoveryCandidates(path, RecoveryInspectionMode(99), validation.HeapOnly(1<<20, 2), nil); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestImmutableInspectionReportsTheNewestCandidate(t *testing.T) {
	path := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.SourceIdentity == (publication.LocalFileIdentity{}) {
		t.Fatal("zero source identity")
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

func TestOfflineInspectionReportsTheNewestCandidate(t *testing.T) {
	path := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.CandidateCount() != 1 || result.Candidate(0).Label != CandidateNewest {
		t.Fatalf("candidates %+v, want only newest", result.Candidates())
	}
}

func TestImmutableInspectionShortFileKeepsTheOrderUnproven(t *testing.T) {
	path := metaDBFile(t, 1)
	result, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.CandidateCount() != 1 {
		t.Fatalf("candidate count %d, want 1", result.CandidateCount())
	}
	if candidate := result.Candidate(0); candidate.Label != CandidateUnorderedMeta0 || candidate.MetaPage != 0 {
		t.Fatalf("candidate %+v, want unordered page 0", candidate)
	}
	if found := result.Progress.FindingsFor(validation.ReasonIoError); found != 1 {
		t.Fatalf("IoError findings %d, want 1 (the unmapped page 1)", found)
	}
	if result.Progress.UntraversableSubgraphs != 1 {
		t.Fatalf("untraversable subgraphs %d, want 1", result.Progress.UntraversableSubgraphs)
	}
}

func TestInspectionEmptyFileReportsBothPagesAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.iprdb")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.CandidateCount() != 0 {
		t.Fatalf("candidate count %d, want 0", result.CandidateCount())
	}
	if found := result.Progress.FindingsFor(validation.ReasonIoError); found != 2 {
		t.Fatalf("IoError findings %d, want 2", found)
	}
	if result.Progress.UntraversableSubgraphs != 2 {
		t.Fatalf("untraversable subgraphs %d, want 2", result.Progress.UntraversableSubgraphs)
	}
}

func TestInspectionMissingFileFailsTheOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.iprdb")
	if _, err := inspect(t, path, RecoveryInspectionImmutable); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestValidateOfflineCandidateCleanSweep(t *testing.T) {
	path := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	candidate := result.Candidate(0)
	validated, failure := ValidateOfflineCandidate(path, candidate, validation.HeapOnly(1<<20, 2), nil, nil)
	if failure != nil {
		t.Fatalf("validate: %v", failure.Cause)
	}
	if !validated.Valid {
		t.Fatalf("valid = false, progress %+v", validated.Progress)
	}
	if validated.FileIdentity != result.SourceIdentity {
		t.Fatal("validated identity differs from the inspected identity")
	}
	if validated.Generation == nil || validated.Generation.TransactionID != candidate.TransactionID {
		t.Fatalf("generation %+v", validated.Generation)
	}
}

func TestValidateOfflineCandidateRejectsForeignIdentity(t *testing.T) {
	path := metaDBFile(t, 2)
	other := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	otherResult, err := inspect(t, other, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect other: %v", err)
	}
	if result.SourceIdentity == otherResult.SourceIdentity {
		t.Fatal("test files share one identity")
	}
	_, failure := ValidateOfflineCandidate(other, result.Candidate(0), validation.HeapOnly(1<<20, 2), nil, nil)
	if failure == nil || failure.Cause.Error() == "" {
		t.Fatal("foreign identity accepted")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause %v, want RecoveryCandidateChanged", failure.Cause)
	}
}

func TestValidateOfflineCandidateStaleToken(t *testing.T) {
	path := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// Rewrite page 1 with a different commit nonce: the equal-pair
	// order becomes unproven and the newest token no longer selects.
	rewriteMeta(t, path, 1, func(meta *format.Meta) {
		meta.CommitNonce = [16]byte{0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99, 0x99}
	})
	_, failure := ValidateOfflineCandidate(path, result.Candidate(0), validation.HeapOnly(1<<20, 2), nil, nil)
	if failure == nil {
		t.Fatal("stale token accepted")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause %v, want RecoveryCandidateChanged", failure.Cause)
	}
}

func TestValidateOfflineCandidateMissingFile(t *testing.T) {
	path := metaDBFile(t, 2)
	result, err := inspect(t, path, RecoveryInspectionOffline)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.iprdb")
	_, failure := ValidateOfflineCandidate(missing, result.Candidate(0), validation.HeapOnly(1<<20, 2), nil, nil)
	if failure == nil {
		t.Fatal("missing file accepted")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeIO {
		t.Fatalf("cause %v, want CodeIO", failure.Cause)
	}
}

// rewriteMeta rewrites one complete meta page of a test file (the
// test counterpart of the Rust rewrite_meta helper: read the page,
// decode, mutate, re-encode, re-seal, write, and synchronize).
func rewriteMeta(t *testing.T, path string, pageIndex uint64, change func(*format.Meta)) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw := make([]byte, format.PageSize)
	if _, err := f.ReadAt(raw, int64(pageIndex*format.PageSize)); err != nil {
		t.Fatal(err)
	}
	meta, ok := format.ParseIdentity(raw)
	if !ok {
		t.Fatalf("page %d not identity-readable", pageIndex)
	}
	change(&meta)
	if err := meta.EncodeMapped(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(raw, int64(pageIndex*format.PageSize)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
}
