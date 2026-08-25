//go:build linux && amd64

package iprangedb

// Routed recovery facade tests (slice 4-12B): on linux/amd64 the
// public inspection and recovery entries route through the isolated
// worker client after the preflight. These tests pin the worker
// equivalence of the candidate inspection, a published recovery
// output readable by the immutable reader, and the guard-pending
// terminal: the retained worker cleanup surfaces as a retryable
// recovery source-cleanup guard on the public failure.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// workerDoubleEnv is the in-process worker double selector of the
// internal worker package tests (client_double_test.go TestMain); the
// guard-pending facade test rebuilds that test binary and re-executes
// it as the fake worker, exactly like the internal arm tests.
const workerDoubleEnv = "IPRANGE_V4_WORKER_DOUBLE"

// routedRecoverySource builds the shared incomplete direct source and
// returns its path plus the projected newest candidate.
func routedRecoverySource(t *testing.T) (string, *RecoveryCandidate) {
	t.Helper()
	sourcePath := filepath.Join(t.TempDir(), "source.v4")
	publicRecoverySource(t, sourcePath)
	inspection, err := recovery.InspectRecoveryCandidates(sourcePath, recovery.RecoveryInspectionImmutable, validation.HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("in-process inspection: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no candidate projected")
	}
	return sourcePath, candidate
}

// TestRoutedInspectRecoveryCandidatesMatchesInProcess proves the
// routed inspection returns the identical candidates, source
// identity, and classification progress as the in-process machine.
func TestRoutedInspectRecoveryCandidatesMatchesInProcess(t *testing.T) {
	installWorkerForTest(t)
	sourcePath := filepath.Join(t.TempDir(), "source.v4")
	publicRecoverySource(t, sourcePath)
	inProcess, err := recovery.InspectRecoveryCandidates(sourcePath, recovery.RecoveryInspectionImmutable, validation.HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("in-process inspection: %v", err)
	}
	routed, err := InspectRecoveryCandidates(sourcePath, RecoveryInspectionImmutable, HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("routed inspection: %v", err)
	}
	if inProcess.SourceIdentity != routed.SourceIdentity {
		t.Errorf("source identity %v, want %v", routed.SourceIdentity, inProcess.SourceIdentity)
	}
	if inProcess.CandidateCount() != routed.CandidateCount() {
		t.Fatalf("candidate count %d, want %d", routed.CandidateCount(), inProcess.CandidateCount())
	}
	for index := 0; index < inProcess.CandidateCount(); index++ {
		if !reflect.DeepEqual(inProcess.Candidate(index), routed.Candidate(index)) {
			t.Errorf("candidate %d %v, want %v", index, routed.Candidate(index), inProcess.Candidate(index))
		}
	}
	assertDomainProgressEqual(t, inProcess.Progress, routed.Progress)
}

// TestRoutedRecoverImmutablePublishesReadableOutput runs the happy
// recover_immutable arm through the worker client: the published
// output is readable by the immutable reader and reports the exact
// rejected-dangling-range facts the in-process machine produces.
func TestRoutedRecoverImmutablePublishesReadableOutput(t *testing.T) {
	installWorkerForTest(t)
	sourcePath, candidate := routedRecoverySource(t)
	outputPath := filepath.Join(t.TempDir(), "output.v4")
	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, RecoveryHeapOnly(1024*1024, 100, 2), nil, nil)
	if failure != nil {
		t.Fatalf("routed recovery: %v", failure.Cause)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("publication %v, want published (cause %v)", result.Publication.Publication, result.Publication.Cause)
	}
	if result.Publication.Cause != nil {
		t.Fatalf("published result carries cause %v", result.Publication.Cause)
	}
	if result.Report.UnknownEnvelopes != 1 {
		t.Fatalf("report %+v, want 1 unknown envelope", result.Report)
	}
	reader, err := OpenImmutable(outputPath)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	info, err := reader.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.PageCount != 2 || info.RangeRecordCount != 0 {
		t.Fatalf("info %+v, want 2 pages 0 ranges", info)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader close: %v", err)
	}
}

// TestRoutedRecoverImmutableGuardPendingRetainsCleanup mirrors the
// internal TestRecoverWithWorkerGuardPending through the facade: a
// guard-pending terminal surfaces the retained worker cleanup as the
// public recovery source-cleanup guard, with the cleanup-guard
// coordination class and a releasable retry. The real worker cannot
// produce a guard-pending terminal from a fixture (the retained guard
// requires a failing source release), so the test drives the internal
// worker package's scripted double exactly like the internal arm
// tests do.
func TestRoutedRecoverImmutableGuardPendingRetainsCleanup(t *testing.T) {
	directory := t.TempDir()
	double := filepath.Join(directory, "worker-tests")
	command := exec.Command("go", "-C", workerModuleRoot(), "test", "-c", "-o", double, "./internal/worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build worker double: %v\n%s", err, output)
	}
	t.Setenv(workerDoubleEnv, "recovery_guard_pending")
	t.Cleanup(func() { worker.SetWorkerCandidatesForTest(nil) })
	worker.SetWorkerCandidatesForTest(func() ([]string, error) { return []string{double}, nil })

	candidate := &RecoveryCandidate{
		Label:          RecoveryCandidateNewest,
		SourceIdentity: publication.LocalFileIdentity{},
		DatabaseID:     [16]byte{1},
		CommitNonce:    [16]byte{2},
	}
	result, failure := RecoverImmutable(filepath.Join(directory, "source.v4"), candidate, filepath.Join(directory, "output.v4"), RecoveryHeapOnly(1024*1024, 100, 2), nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	if failure.CoordinationCleanup != publication.CoordinationCleanupCleanupGuard {
		t.Fatalf("coordination cleanup %v, want the cleanup-guard class", failure.CoordinationCleanup)
	}
	guard := failure.SourceCleanup
	if guard == nil {
		t.Fatal("guard-pending terminal retained no source cleanup")
	}
	if !guard.CleanupPending() {
		t.Fatal("guard-pending terminal did not retain the cleanup")
	}
	var typed *format.Error
	if !errors.As(guard.LastProblem(), &typed) || typed.Code != format.CodeCleanupConflict {
		t.Fatalf("guard last problem %v, want the retained cleanup-conflict class", guard.LastProblem())
	}
	complete, err := guard.RetryCleanup()
	if err != nil || !complete {
		t.Fatalf("cleanup retry = %v, %v; want complete", complete, err)
	}
	if guard.CleanupPending() {
		t.Fatal("completed cleanup still pending")
	}
	// The double wrote its outcome with an empty report; the failure
	// carries the exact wire facts.
	if failure.Report.UnknownEnvelopes != 0 {
		t.Fatalf("report %+v, want the double's empty report", failure.Report)
	}
}

// TestRoutedRecoverLeavesNoResidueRoots pins that the routed
// happy-path recovery leaves no private artifacts behind (the facade
// mirrors assertNoPrivateNames of the internal api tests).
func TestRoutedRecoverLeavesNoResidueRoots(t *testing.T) {
	installWorkerForTest(t)
	sourcePath, candidate := routedRecoverySource(t)
	directory := filepath.Dir(sourcePath)
	outputPath := filepath.Join(directory, "output.v4")
	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, RecoveryHeapOnly(1024*1024, 100, 2), nil, nil)
	if failure != nil {
		t.Fatalf("routed recovery: %v", failure.Cause)
	}
	if result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("publication %v, want published", result.Publication.Publication)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("directory entries %d, want source and output only", len(entries))
	}
}
