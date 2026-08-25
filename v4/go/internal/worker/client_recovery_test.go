//go:build linux && amd64

// Recovery-mode client arm tests (Rust worker/client/recovery.rs
// client_tests.rs): the completed and streaming sessions against the
// in-process double, the fault-record read-back arm, the unreadable
// fault retry, the guard-pending retained cleanup, the callback
// failure class, and the real-worker session (the machine creates the
// destination attempt and discards it on the source failure).

package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// recoveryRequest builds one immutable recovery request shape shared
// by the arm tests (the same candidate the worker dispatch tests use).
func recoveryRequest(t *testing.T, destination string) (source string, candidate *recovery.RecoveryCandidate, budget *recovery.RecoveryBudget) {
	t.Helper()
	source = filepath.Join(t.TempDir(), "missing-source.v4")
	candidate = &recovery.RecoveryCandidate{
		Label:          recovery.CandidateNewest,
		SourceIdentity: publication.LocalFileIdentity{},
		DatabaseID:     [16]byte{1},
		CommitNonce:    [16]byte{2},
	}
	budget = &recovery.RecoveryBudget{MaxHeapBytes: 1 << 30, MaxOutputPages: 1 << 16, MaxOpenFiles: 8}
	return source, candidate, budget
}

func TestRecoverWithWorkerComplete(t *testing.T) {
	armDouble(t, "recovery_complete")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	outcome, cleanup := RecoverWithWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, nil)
	if outcome == nil || outcome.Result == nil || outcome.Failure != nil {
		t.Fatalf("outcome = %+v, want the completed result arm", outcome)
	}
	if cleanup != nil {
		t.Fatal("completed recovery retained a cleanup guard")
	}
}

func TestRecoverWithWorkerUnknownStream(t *testing.T) {
	armDouble(t, "recovery_unknown_then_complete")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	seen := 0
	sink := recovery.RecoverySinkFunc(func(envelope *recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
		seen++
		if envelope.Sequence != 1 || envelope.Reason != validation.ReasonIoError || envelope.PageNumber == nil || *envelope.PageNumber != 4 {
			t.Fatalf("envelope = %+v, want sequence 1 IoError page 4", envelope)
		}
		return recovery.RecoverySinkContinue, nil
	})
	outcome, cleanup := RecoverWithWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, sink)
	if outcome == nil || outcome.Result == nil {
		t.Fatalf("outcome = %+v, want the completed result arm", outcome)
	}
	if cleanup != nil {
		t.Fatal("completed recovery retained a cleanup guard")
	}
	if seen != 1 {
		t.Fatalf("sink saw %d envelopes, want 1", seen)
	}
}

func TestRecoverOnceFaultRecordReadBack(t *testing.T) {
	armDouble(t, "fault")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	attempt := recoverOnceWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, nil, nil, new(uint64))
	if attempt.kind != attemptInterrupted {
		t.Fatalf("attempt kind = %d, want interrupted (%+v)", attempt.kind, attempt)
	}
	if attempt.fault.Role != RoleSource || attempt.fault.Relative != 0x100 || attempt.fault.MappingLen != 0x2000 {
		t.Fatalf("fault record = %+v, want source relative 0x100 mapping-len 0x2000", attempt.fault)
	}
	if attempt.checkpoint != nil {
		t.Fatal("source fault carried a publication checkpoint")
	}
}

func TestRecoverWithWorkerFaultRetryRecordsUnreadablePage(t *testing.T) {
	armDouble(t, "fault")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	outcome, cleanup := RecoverWithWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, nil)
	if outcome == nil || outcome.Failure == nil || outcome.Result != nil {
		t.Fatalf("outcome = %+v, want the failure arm", outcome)
	}
	if cleanup != nil {
		t.Fatal("fault retry retained a cleanup guard")
	}
	// The first source fault records page 0 and restarts; the repeated
	// source fault on the retry is the recorded Conflict.
	var e *format.Error
	if !errors.As(outcome.Failure.Cause, &e) || e.Code != format.CodeConflict {
		t.Fatalf("cause = %v, want the Conflict class", outcome.Failure.Cause)
	}
	if e.Detail != "recovery source fault did not advance" {
		t.Fatalf("detail = %q, want the recorded retry refusal", e.Detail)
	}
}

func TestRecoverWithWorkerGuardPending(t *testing.T) {
	armDouble(t, "recovery_guard_pending")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	outcome, cleanup := RecoverWithWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, nil)
	if outcome == nil || outcome.Failure == nil || outcome.Result != nil {
		t.Fatalf("outcome = %+v, want the failure arm", outcome)
	}
	if outcome.Failure.CoordinationCleanup != publication.CoordinationCleanupCleanupGuard {
		t.Fatalf("coordination cleanup = %v, want the cleanup-guard class", outcome.Failure.CoordinationCleanup)
	}
	if cleanup == nil {
		t.Fatal("guard-pending completion did not retain a cleanup guard")
	}
	if !cleanup.child.Active() {
		t.Fatal("guard-pending completion did not retain the child")
	}
	if err := cleanup.Release(); err != nil {
		t.Fatalf("cleanup release: %v", err)
	}
}

func TestRecoverWithWorkerCallbackFailure(t *testing.T) {
	armDouble(t, "recovery_unknown_then_exit3")
	source, candidate, budget := recoveryRequest(t, filepath.Join(t.TempDir(), "out.v4"))
	sink := recovery.RecoverySinkFunc(func(*recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
		return 0, errors.New("sink exploded")
	})
	outcome, cleanup := RecoverWithWorker(source, filepath.Join(t.TempDir(), "out.v4"), candidate, WorkerModeImmutable, budget, nil, sink)
	if outcome == nil || outcome.Failure == nil || outcome.Result != nil {
		t.Fatalf("outcome = %+v, want the failure arm", outcome)
	}
	if cleanup != nil {
		t.Fatal("callback failure retained a cleanup guard")
	}
	var e *format.Error
	if !errors.As(outcome.Failure.Cause, &e) || e.Code != format.CodeConflict {
		t.Fatalf("cause = %v, want the Conflict class", outcome.Failure.Cause)
	}
	if e.Detail != "worker recovery callback checkpoint is missing" {
		t.Fatalf("detail = %q, want the missing checkpoint refusal", e.Detail)
	}
}

func TestRecoverWithWorkerRealBinary(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	directory := t.TempDir()
	destination := filepath.Join(directory, "out.v4")
	source, candidate, budget := recoveryRequest(t, destination)
	outcome, cleanup := RecoverWithWorker(source, destination, candidate, WorkerModeImmutable, budget, nil, nil)
	if outcome == nil || outcome.Failure == nil || outcome.Result != nil {
		t.Fatalf("outcome = %+v, want the failure arm", outcome)
	}
	if cleanup != nil {
		t.Fatal("real recovery retained a cleanup guard")
	}
	wantCode(t, outcome.Failure.Cause, format.CodeIO)
	// The worker machine created and discarded its own destination
	// attempt; the destination path must be absent again.
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination %s exists after the failed recovery", destination)
	}
}

// realRecoveryFixture builds one real direct recovery source with two
// committed ranges (the Rust client_tests.rs fault_fixture source
// shape; the Go writer coalesces the pair into one root leaf, so the
// committed data page is the range root) and returns its path and the
// range-root page number.
func realRecoveryFixture(t *testing.T) (string, uint32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.v4")
	var tag [16]byte
	copy(tag[:], "first-seen")
	builder, err := writer.NewOutputBuilder(path, writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       tag,
		DatabaseID:     [16]byte{1},
		TxnID:          7,
		CommitNonce:    [16]byte{2},
		FeedIndexLimit: 0,
	}, writer.OutputBudget{MaxOutputPages: 20_000}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for _, record := range [][3]uint32{{10, 20, 1}, {100, 110, 2}} {
		if err := builder.PushDirectV4(record[0], record[1], record[2]); err != nil {
			t.Fatalf("PushDirectV4: %v", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if meta.RangeRoot == 0 {
		t.Fatalf("fixture meta %+v: want a range root", meta)
	}
	return path, meta.RangeRoot
}

// realFixtureCandidate inspects a real source in-process and returns
// its newest recovery candidate (the parent-side token of the real
// worker session).
func realFixtureCandidate(t *testing.T, path string) *recovery.RecoveryCandidate {
	t.Helper()
	inspection, err := recovery.InspectRecoveryCandidates(path, recovery.RecoveryInspectionImmutable, validation.HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("InspectRecoveryCandidates: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no candidate projected")
	}
	return candidate
}

// TestRecoverWithWorkerRealBinaryDeclaredPageRefuses mirrors the Rust
// client_tests.rs fault_fixture second run (after marking the exact
// page unreadable the session completes with report.pages.io_unreadable
// == 1): a declared real source page travels in the request, the real
// worker applies the session list to its source-guard mapping
// (source_guard.rs map_available parity), the checked-page authority
// refuses the page deterministically - no second SIGBUS - and the
// machine completes with exactly one I/O-unreadable page reported
// through the wire outcome.
//
// The session-1 SIGBUS half of the Rust fixture (a real mapped fault
// with exit 197 and a fault record) additionally requires the Rust
// probe_source arm of the domain-machine mappings, which the Go worker
// does not port yet: only tests arm probes, unarmed faults chain into
// the Go runtime fatal and produce no record (sigbus_linux_amd64_test
// chain proof). The client-side fault-retry loop over recorded pages
// is covered by TestRecoverWithWorkerFaultRetryRecordsUnreadablePage;
// this test pins the worker-side refusal that the retry lands on.
func TestRecoverWithWorkerRealBinaryDeclaredPageRefuses(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	sourcePath, declared := realRecoveryFixture(t)
	candidate := realFixtureCandidate(t, sourcePath)
	budget := &recovery.RecoveryBudget{MaxHeapBytes: 1 << 30, MaxOutputPages: 1 << 16, MaxOpenFiles: 8}
	destination := filepath.Join(t.TempDir(), "out.v4")
	var envelopes []*recovery.RecoveryUnknownEnvelope
	attempt := recoverOnceWorker(sourcePath, destination, candidate, WorkerModeImmutable, budget, nil, recovery.RecoverySinkFunc(func(envelope *recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
		envelopes = append(envelopes, envelope)
		return recovery.RecoverySinkContinue, nil
	}), []uint32{declared}, new(uint64))
	if attempt.kind != attemptComplete {
		t.Fatalf("attempt kind = %d (%+v), want the completed terminal", attempt.kind, attempt)
	}
	if attempt.outcome == nil || attempt.outcome.Result == nil || attempt.outcome.Failure != nil {
		t.Fatalf("outcome = %+v, want the completed result arm", attempt.outcome)
	}
	if attempt.outcome.Result.Report.Pages.IOUnreadable != 1 {
		t.Fatalf("io_unreadable = %d, want 1", attempt.outcome.Result.Report.Pages.IOUnreadable)
	}
	count := 0
	for _, envelope := range envelopes {
		if envelope.Reason == validation.ReasonIoError && envelope.PageNumber != nil && *envelope.PageNumber == declared {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("envelopes = %+v, want one IoError envelope at page %d", envelopes, declared)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("published output missing after the refused page: %v", err)
	}
}
