//go:build linux && amd64

// Client drive seam tests (Rust worker/client.rs and client_tests.rs):
// the handshake failure classes, the drive complete/fault/failed and
// cancellation transitions, the per-mode event hook, the callback and
// poll acknowledgements, the unreadable-page ledger, and the retained
// cleanup guard. Every worker side is the in-process double of
// client_double_test.go, spawned through the real SpawnWorker path.

package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// spawnDouble starts the in-process worker double for one mode through
// the real spawn path: the candidate list points at the test binary
// and the double mode travels in the environment, so the child
// dispatches into workerDoubleMain instead of the test suite.
func spawnDouble(t *testing.T, mode string) (*Process, *Control) {
	t.Helper()
	t.Setenv(workerDoubleEnv, mode)
	workerCandidatesHook = func() ([]string, error) { return []string{os.Args[0]}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(control.Close)
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn double:", err)
	}
	t.Cleanup(child.Close)
	return child, control
}

// wantWorkerError fails the test unless err is exactly the intermediate
// worker-operation pair with the given code (Rust Error::WorkerOperation
// over the wire; readWorkerError folds only the registered constants).
func wantWorkerError(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	var we *WireError
	if !errors.As(err, &we) || we.Code != code {
		t.Fatalf("expected worker-operation code %d, got %v", code, err)
	}
}

// wantConflictDetail fails the test unless err is a Conflict error with
// the exact Rust detail string.
func wantConflictDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeConflict {
		t.Fatalf("expected Conflict error, got %v", err)
	}
	if e.Detail != detail {
		t.Fatalf("detail = %q, want %q", e.Detail, detail)
	}
}

func TestWorkerCandidatesDefault(t *testing.T) {
	workerCandidatesHook = nil
	t.Cleanup(func() { workerCandidatesHook = nil })
	candidates, err := workerCandidates()
	if err != nil {
		t.Fatal("candidates:", err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal("executable:", err)
	}
	want := filepath.Join(filepath.Dir(current), "iprange-v4-worker")
	if len(candidates) != 1 || candidates[0] != want {
		t.Fatalf("candidates = %v, want [%s]", candidates, want)
	}
}

func TestSpawnWorkerUnavailableWithoutCandidates(t *testing.T) {
	t.Cleanup(func() { workerCandidatesHook = nil })
	workerCandidatesHook = func() ([]string, error) { return nil, nil }
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()
	_, err = SpawnWorker(control)
	wantOSUnsupportedDetail(t, err, "SDK validation/recovery worker is unavailable")
}

func TestSpawnWorkerNotFoundFallback(t *testing.T) {
	t.Setenv(workerDoubleEnv, "hang")
	workerCandidatesHook = func() ([]string, error) {
		return []string{filepath.Join(t.TempDir(), "missing-iprange-v4-worker"), os.Args[0]}, nil
	}
	t.Cleanup(func() { workerCandidatesHook = nil })
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(control.Close)
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn:", err)
	}
	t.Cleanup(child.Close)
	if !child.Active() {
		t.Fatal("spawn did not fall through to the second candidate")
	}
}

func TestStartWorkerHandshakeSuccess(t *testing.T) {
	child, control := spawnDouble(t, "ready")
	control.SetOpcode(OpcodeValidate)
	control.SetExternalPoll(false)
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	if control.state() != stateRunning {
		t.Fatalf("state = %d, want running", control.state())
	}
	if control.path != "" {
		t.Fatal("control path was not unlinked after the handshake")
	}
}

func TestHandshakeIdentityConflict(t *testing.T) {
	child, control := spawnDouble(t, "wrongpid")
	err := Handshake(child, control)
	wantConflictDetail(t, err, "SDK worker identity does not match")
	if child.Active() {
		t.Fatal("identity conflict must abort the child")
	}
}

func TestHandshakeExitedBeforeReadySuccess(t *testing.T) {
	child, control := spawnDouble(t, "exit0")
	err := Handshake(child, control)
	wantConflictDetail(t, err, "SDK worker exited before its version handshake")
	if child.Active() {
		t.Fatal("an exited child must be consumed by the handshake")
	}
}

func TestHandshakeExitedBeforeReadyFailure(t *testing.T) {
	child, control := spawnDouble(t, "exit1")
	err := Handshake(child, control)
	wantConflictDetail(t, err, "SDK worker version or protocol does not match")
	if child.Active() {
		t.Fatal("an exited child must be consumed by the handshake")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	previous := startLimit
	startLimit = 100 * time.Millisecond
	defer func() { startLimit = previous }()
	child, control := spawnDouble(t, "hang")
	err := Handshake(child, control)
	wantConflictDetail(t, err, "SDK worker version handshake timed out")
	if child.Active() {
		t.Fatal("a timed-out handshake must abort the child")
	}
}

func TestDriveComplete(t *testing.T) {
	child, control := spawnDouble(t, "complete")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	drive, err := DriveWorker(child, control, nil)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !drive.Complete || drive.GuardPending {
		t.Fatalf("drive = %+v, want Complete without guard", drive)
	}
	if child.Active() {
		t.Fatal("a completed worker must be reaped")
	}
}

func TestDriveCompleteGuardPending(t *testing.T) {
	child, control := spawnDouble(t, "complete_guard")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	drive, err := DriveWorker(child, control, nil)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !drive.Complete || !drive.GuardPending {
		t.Fatalf("drive = %+v, want Complete with guard pending", drive)
	}
	if !child.Active() {
		t.Fatal("a guard-pending completion must not wait for the child")
	}
}

func TestDriveCompleteInvalidStatus(t *testing.T) {
	child, control := spawnDouble(t, "complete_bad")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantConflictDetail(t, err, "SDK worker completion status is invalid")
}

func TestDriveFault(t *testing.T) {
	child, control := spawnDouble(t, "fault")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	drive, err := DriveWorker(child, control, nil)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if drive.Complete {
		t.Fatalf("drive = %+v, want the Fault arm", drive)
	}
	if drive.Fault.Role != RoleSource || drive.Fault.Relative != 0x100 || drive.Fault.MappingLen != 0x2000 {
		t.Fatalf("fault record = %+v, want source 0x100/0x2000", drive.Fault)
	}
	if child.Active() {
		t.Fatal("a faulted worker must be reaped")
	}
}

func TestDriveFaultUntrustedExit(t *testing.T) {
	child, control := spawnDouble(t, "fault_bad")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantConflictDetail(t, err, "SDK worker fault record is untrusted")
}

func TestDriveFailed(t *testing.T) {
	child, control := spawnDouble(t, "failed")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantWorkerError(t, err, format.CodeConflict)
}

func TestDriveFailedInvalidStatus(t *testing.T) {
	child, control := spawnDouble(t, "failed_bad")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantConflictDetail(t, err, "SDK worker failure record has an invalid completion status")
}

func TestDriveExitedWithoutTerminalRecord(t *testing.T) {
	child, control := spawnDouble(t, "vanish")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantConflictDetail(t, err, "SDK worker exited without a terminal record")
}

func TestDriveUnexpectedEvent(t *testing.T) {
	child, control := spawnDouble(t, "finding")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	_, err := DriveWorker(child, control, nil)
	wantConflictDetail(t, err, "SDK worker emitted an unexpected event")
	if child.Active() {
		t.Fatal("an unexpected event must abort the child")
	}
}

func TestDriveLoopEventHookConsumesFinding(t *testing.T) {
	child, control := spawnDouble(t, "finding_then_complete")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	var observed bool
	event := func(state uint32, child *Process, control *Control) (bool, error) {
		if state == stateFinding {
			observed = true
			return true, nil
		}
		return false, nil
	}
	drive, err := DriveLoop(child, control, nil, "SDK worker emitted an unexpected event", event)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !observed {
		t.Fatal("the event hook never saw the Finding state")
	}
	if !drive.Complete {
		t.Fatalf("drive = %+v, want Complete after the hook consumed Finding", drive)
	}
}

func TestDriveCancellation(t *testing.T) {
	child, control := spawnDouble(t, "cancel")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	check := func() error { return &format.Error{Code: format.CodeCancelled} }
	drive, err := DriveWorker(child, control, check)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !drive.Complete {
		t.Fatalf("drive = %+v, want Complete after cancellation", drive)
	}
	if !control.Cancelled() {
		t.Fatal("the drive never raised the control cancellation flag")
	}
}

func TestDriveCancelPollAcknowledge(t *testing.T) {
	child, control := spawnDouble(t, "cancelpoll")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	drive, err := DriveWorker(child, control, nil)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !drive.Complete {
		t.Fatalf("drive = %+v, want Complete after the poll acknowledgement", drive)
	}
	if control.Response() != 0 {
		t.Fatalf("response = %d, want 0 for an idle checkpoint", control.Response())
	}
	if control.Cancelled() {
		t.Fatal("an idle checkpoint must not raise cancellation")
	}
}

func TestDriveCancelPollCancelled(t *testing.T) {
	child, control := spawnDouble(t, "cancelpoll")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	check := func() error { return &format.Error{Code: format.CodeCancelled} }
	drive, err := DriveWorker(child, control, check)
	if err != nil {
		t.Fatal("drive:", err)
	}
	if !drive.Complete {
		t.Fatalf("drive = %+v, want Complete after the poll acknowledgement", drive)
	}
	if control.Response() != 1 {
		t.Fatalf("response = %d, want 1 for a fired checkpoint", control.Response())
	}
	if !control.Cancelled() {
		t.Fatal("a fired checkpoint must raise the control cancellation flag")
	}
}

func TestRequiresExternalPoll(t *testing.T) {
	if RequiresExternalPoll(nil) {
		t.Fatal("a nil checkpoint must not require external polling")
	}
	if !RequiresExternalPoll(func() error { return nil }) {
		t.Fatal("a checkpoint must require external polling")
	}
}

func TestAcknowledgeCallbackResponses(t *testing.T) {
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()

	decision, err := AcknowledgeCallback(control, false, nil)
	if err != nil || decision != nil {
		t.Fatalf("continue = (%v, %v), want (nil, nil)", decision, err)
	}
	if control.Response() != 0 || control.state() != stateRunning {
		t.Fatalf("continue wrote response %d state %d", control.Response(), control.state())
	}

	decision, err = AcknowledgeCallback(control, true, nil)
	if err != nil {
		t.Fatal("stop:", err)
	}
	var e *format.Error
	if !errors.As(decision.IntoError(), &e) || e.Code != format.CodeStoppedBySink {
		t.Fatalf("stop decision folds to %v, want StoppedBySink", decision.IntoError())
	}
	if control.Response() != 1 || control.state() != stateRunning {
		t.Fatalf("stop wrote response %d state %d", control.Response(), control.state())
	}

	cause := &format.Error{Code: format.CodeConflict, Detail: "double sink failure"}
	decision, err = AcknowledgeCallback(control, false, cause)
	if err != nil {
		t.Fatal("failure:", err)
	}
	if !errors.As(decision.IntoError(), &e) || e.Code != format.CodeSinkFailed {
		t.Fatalf("failure decision folds to %v, want SinkFailed", decision.IntoError())
	}
	// SinkFailed carries the full formatted cause (Rust
	// SinkFailed(Box<Error>) Display), not a bare detail.
	if e.Detail != cause.Error() {
		t.Fatalf("failure detail = %q, want %q", e.Detail, cause.Error())
	}
	if control.Response() != 2 || control.state() != stateRunning {
		t.Fatalf("failure wrote response %d state %d", control.Response(), control.state())
	}
	// The wire carries only the code pair; the detail survives only in
	// the local decision (Rust encode_error drops the detail).
	written, err := ReadWorkerError(control)
	if err != nil {
		t.Fatal("read worker error:", err)
	}
	wantWorkerError(t, written, format.CodeConflict)
}

func TestAdvanceSequence(t *testing.T) {
	child, _ := spawnDouble(t, "hang")
	var delivered uint64
	if err := AdvanceSequence(child, &delivered, 1, "worker validation finding sequence is invalid"); err != nil {
		t.Fatal("first sequence:", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	if !child.Active() {
		t.Fatal("a valid sequence must not abort the child")
	}
	err := AdvanceSequence(child, &delivered, 3, "worker validation finding sequence is invalid")
	wantConflictDetail(t, err, "worker validation finding sequence is invalid")
	if child.Active() {
		t.Fatal("an invalid sequence must abort the child")
	}
}

func TestRecordUnreadablePage(t *testing.T) {
	var pages []uint32
	for _, page := range []uint32{3, 1, 2, 5, 4} {
		if err := RecordUnreadablePage(&pages, page, 1024, "candidate inspection fault did not advance"); err != nil {
			t.Fatal("record:", err)
		}
	}
	if len(pages) != 5 || pages[0] != 1 || pages[1] != 2 || pages[2] != 3 || pages[3] != 4 || pages[4] != 5 {
		t.Fatalf("pages = %v, want [1 2 3 4 5]", pages)
	}
	err := RecordUnreadablePage(&pages, 3, 1024, "candidate inspection fault did not advance")
	wantConflictDetail(t, err, "candidate inspection fault did not advance")
	if len(pages) != 5 {
		t.Fatalf("a duplicate must not grow the list, got %v", pages)
	}
	err = RecordUnreadablePage(&pages, 6, 8, "candidate inspection fault did not advance")
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeInsufficientResourceBudget {
		t.Fatalf("expected the budget class, got %v", err)
	}
	if e.Detail != "unreadable source-page list" {
		t.Fatalf("detail = %q, want the Rust ledger detail", e.Detail)
	}
}

func TestWorkerCleanupReleaseComplete(t *testing.T) {
	child, control := spawnDouble(t, "cleanup_ok")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	cleanup := NewWorkerCleanup(child, control, &WireProblem{Code: format.CodeCleanupConflict, Detail: "seed"})
	defer cleanup.Close()
	if err := cleanup.Release(); err != nil {
		t.Fatal("release:", err)
	}
	if child.Active() {
		t.Fatal("release must reap the child")
	}
}

func TestWorkerCleanupReleaseInvalidStatus(t *testing.T) {
	child, control := spawnDouble(t, "cleanup_bad")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	cleanup := NewWorkerCleanup(child, control, &WireProblem{})
	defer cleanup.Close()
	err := cleanup.Release()
	wantConflictDetail(t, err, "SDK cleanup worker completion status is invalid")
}

func TestWorkerCleanupReleaseRetainedProblem(t *testing.T) {
	child, control := spawnDouble(t, "cleanup_problem")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	cleanup := NewWorkerCleanup(child, control, &WireProblem{})
	defer cleanup.Close()
	err := cleanup.Release()
	wantWorkerError(t, err, format.CodeCleanupConflict)
	if problem := cleanup.LastProblem(); problem == nil || problem.Detail != "double cleanup problem" {
		t.Fatalf("last problem = %+v, want the double's cleanup problem", problem)
	}
}

func TestWorkerCleanupReleaseOmittedProblem(t *testing.T) {
	child, control := spawnDouble(t, "cleanup_noproblem")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	cleanup := NewWorkerCleanup(child, control, &WireProblem{})
	defer cleanup.Close()
	err := cleanup.Release()
	wantConflictDetail(t, err, "SDK cleanup worker omitted its cleanup problem")
}

func TestWorkerCleanupReleaseUnexpectedExit(t *testing.T) {
	child, control := spawnDouble(t, "vanish")
	if err := StartWorker(child, control); err != nil {
		t.Fatal("start:", err)
	}
	cleanup := NewWorkerCleanup(child, control, &WireProblem{})
	defer cleanup.Close()
	err := cleanup.Release()
	wantWorkerError(t, err, format.CodeConflict)
	if problem := cleanup.LastProblem(); problem == nil || problem.Detail != "isolated cleanup worker exited unexpectedly" {
		t.Fatalf("last problem = %+v, want the unexpected-exit conflict", problem)
	}
}

// wantOSUnsupportedDetail fails the test unless err is the exact
// unsupported-class message of the Rust authority.
func wantOSUnsupportedDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeOSUnsupported {
		t.Fatalf("expected the unsupported class, got %v", err)
	}
	if e.Detail != detail {
		t.Fatalf("detail = %q, want %q", e.Detail, detail)
	}
}
