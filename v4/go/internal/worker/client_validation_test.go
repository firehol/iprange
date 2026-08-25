//go:build linux && amd64

// Validation-mode client arm tests (Rust worker/client/validation.rs
// client_tests.rs): the completed and streaming sessions against the
// in-process double, the fault-record read-back arm, the unreadable
// fault retry, the guard-pending retained cleanup, the callback
// failure class, and the real-worker sessions with the mode arms'
// unreadable-page field (declared pages travel in the request; a
// duplicate list is refused worker-side before the machine runs).

package worker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// armDouble arms the in-process worker double for one client-arm
// session. The arm (not the test) creates the control and spawns the
// child, so the hook and the environment are all the test provides;
// every child the arm spawns re-executes this test binary as the
// double of the selected mode.
func armDouble(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(workerDoubleEnv, mode)
	workerCandidatesHook = func() ([]string, error) { return []string{os.Args[0]}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })
}

func TestValidateWithWorkerComplete(t *testing.T) {
	armDouble(t, "validation_complete")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, nil)
	if result == nil || failure != nil {
		t.Fatalf("result = %v, failure = %v; want the completed arm", result, failure)
	}
}

func TestValidateWithWorkerFindingStream(t *testing.T) {
	armDouble(t, "validation_finding_then_complete")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	seen := 0
	sink := validation.SinkFunc(func(finding *validation.ValidationFinding) (validation.ValidationSinkControl, error) {
		seen++
		if finding.Sequence != 1 || finding.Reason != validation.ReasonIoError || finding.PageNumber == nil || *finding.PageNumber != 4 {
			t.Fatalf("finding = %+v, want sequence 1 IoError page 4", finding)
		}
		return validation.SinkContinue, nil
	})
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, sink)
	if result == nil || failure != nil {
		t.Fatalf("result = %v, failure = %v; want the completed arm", result, failure)
	}
	if seen != 1 {
		t.Fatalf("sink saw %d findings, want 1", seen)
	}
}

func TestValidateWithWorkerSinkStopWithOkResultConflicts(t *testing.T) {
	armDouble(t, "validation_finding_then_complete")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	sink := validation.SinkFunc(func(*validation.ValidationFinding) (validation.ValidationSinkControl, error) {
		return validation.SinkStop, nil
	})
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, sink)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	wantConflictDetail(t, failure.Cause, "worker ignored a terminal validation callback")
}

func TestValidateOnceFaultRecordReadBack(t *testing.T) {
	armDouble(t, "fault")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	attempt, err := validateOnceWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, nil, nil, new(uint64))
	if err != nil {
		t.Fatal("validate once:", err)
	}
	if attempt.complete || attempt.fault == nil {
		t.Fatalf("attempt = %+v, want the fault arm", attempt)
	}
	// The 4-11A accessor cross-checks generation/role/code/relative/
	// address/marker before the record is returned; the record carries
	// the folded role, relative offset, and mapping length.
	if attempt.fault.Role != RoleSource {
		t.Fatalf("fault role = %d, want source", attempt.fault.Role)
	}
	if attempt.fault.Relative != 0x100 || attempt.fault.MappingLen != 0x2000 {
		t.Fatalf("fault record = %+v, want relative 0x100 mapping-len 0x2000", attempt.fault)
	}
}

func TestValidateWithWorkerFaultRetryRecordsUnreadablePage(t *testing.T) {
	armDouble(t, "fault")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	// The first fault records page 0 (relative 0x100) and restarts;
	// the repeated source fault on the retry is the recorded Conflict.
	wantConflictDetail(t, failure.Cause, "validation fault did not advance")
}

func TestValidateWithWorkerGuardPending(t *testing.T) {
	armDouble(t, "validation_guard_pending")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	if failure.CoordinationCleanup != publication.CoordinationCleanupCleanupGuard {
		t.Fatalf("coordination cleanup = %v, want the cleanup-guard class", failure.CoordinationCleanup)
	}
	cleanup, ok := failure.SourceCleanup.(*WorkerCleanup)
	if !ok || cleanup == nil {
		t.Fatalf("source cleanup = %v, want the retained WorkerCleanup", failure.SourceCleanup)
	}
	if problem := cleanup.LastProblem(); problem == nil || problem.Code != format.CodeCleanupConflict {
		t.Fatalf("retained problem = %v, want the cleanup-conflict class", problem)
	}
	// The retained child stays alive for the CleanupRequest exchange.
	if !cleanup.child.Active() {
		t.Fatal("guard-pending completion did not retain the child")
	}
	if err := cleanup.Release(); err != nil {
		t.Fatalf("cleanup release: %v", err)
	}
}

func TestValidateWithWorkerCallbackFailure(t *testing.T) {
	armDouble(t, "validation_finding_then_exit3")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	sink := validation.SinkFunc(func(*validation.ValidationFinding) (validation.ValidationSinkControl, error) {
		return 0, errors.New("sink exploded")
	})
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, sink)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	wantConflictDetail(t, failure.Cause, "worker validation callback checkpoint is missing")
}

func TestInspectOnceFaultRecordReadBack(t *testing.T) {
	armDouble(t, "fault")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	attempt, err := inspectOnceWorker(t.TempDir()+"/missing.v4", recovery.RecoveryInspectionImmutable, budget, nil, nil)
	if err != nil {
		t.Fatal("inspect once:", err)
	}
	if attempt.complete || attempt.fault == nil {
		t.Fatalf("attempt = %+v, want the fault arm", attempt)
	}
	if attempt.fault.Role != RoleSource || attempt.fault.Relative != 0x100 || attempt.fault.MappingLen != 0x2000 {
		t.Fatalf("fault record = %+v, want source relative 0x100 mapping-len 0x2000", attempt.fault)
	}
}

func TestInspectRecoveryCandidatesWithWorkerFaultRetryConflict(t *testing.T) {
	armDouble(t, "fault")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	value, err := InspectRecoveryCandidatesWithWorker(t.TempDir()+"/missing.v4", recovery.RecoveryInspectionImmutable, budget, nil)
	if value != nil || err == nil {
		t.Fatalf("inspection = %v, err = %v; want the error arm", value, err)
	}
	// Page 0 records on the first fault, the retry faults page 0 again,
	// and the record refuses the duplicate with the exact Conflict.
	wantConflictDetail(t, err, "candidate inspection fault did not advance")
}

func TestInspectRecoveryCandidatesWithWorkerComplete(t *testing.T) {
	armDouble(t, "inspection_complete")
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	value, err := InspectRecoveryCandidatesWithWorker(t.TempDir()+"/missing.v4", recovery.RecoveryInspectionImmutable, budget, nil)
	if err != nil {
		t.Fatal("inspect:", err)
	}
	if value == nil {
		t.Fatal("inspection = nil, want the facts arm")
	}
}

// moduleRootOf returns the v4/go module root from the test working
// directory (go test runs with the package source directory as cwd).
func moduleRootOf(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal("getwd:", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("v4/go module root not found from", dir)
		}
		dir = parent
	}
}

// buildRealWorker builds the real cmd/iprange-v4-worker binary into a
// fresh directory and returns its path (the mode arms need the real
// worker when the mode machine runs in a separate process).
func buildRealWorker(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	destination := filepath.Join(directory, "iprange-v4-worker")
	command := exec.Command("go", "-C", moduleRootOf(t), "build", "-o", destination, "./cmd/iprange-v4-worker")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build real worker: %v\n%s", err, output)
	}
	return destination
}

func TestValidateWithWorkerRealBinary(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	// A declared unreadable page travels in the request, the worker
	// accepts the list into its fault memory, and the machine runs (the
	// missing source surfaces the machine's CodeIO open failure).
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	result, failure := ValidateWithWorker(t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	wantWorkerError(t, failure.Cause, format.CodeIO)
}

func TestWorkerRejectsDuplicateUnreadablePages(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	// Drive one raw validation session whose request carries a
	// duplicate unreadable-page list: the worker's
	// set_unreadable_source_pages refuses the whole session before the
	// machine runs with the verbatim InvalidArgument class (Rust
	// worker.rs:416-425). The client arms never produce duplicates
	// themselves, so the raw session is the proof surface.
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()
	control.SetOpcode(OpcodeValidate)
	control.SetExternalPoll(false)
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	if err := WriteValidationRequest(control, t.TempDir()+"/missing.v4", validation.ValidationModeImmutableCurrent, nil, budget, []uint32{4, 4}, 0); err != nil {
		t.Fatal("write request:", err)
	}
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn:", err)
	}
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err = DriveWorker(child, control, nil)
	if err == nil {
		t.Fatal("duplicate unreadable pages were accepted")
	}
	wantWorkerError(t, err, format.CodeInvalidArgument)
}

func TestInspectRecoveryCandidatesWithWorkerRealBinary(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	value, err := InspectRecoveryCandidatesWithWorker(t.TempDir()+"/missing.v4", recovery.RecoveryInspectionImmutable, budget, nil)
	if value != nil || err == nil {
		t.Fatalf("inspection = %v, err = %v; want the error arm", value, err)
	}
	wantWorkerError(t, err, format.CodeIO)
}
