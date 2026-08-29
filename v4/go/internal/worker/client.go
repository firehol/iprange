//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

// Worker client drive seam (Rust worker/client.rs): spawning the
// isolated worker process, the version handshake, the drive loop with
// its per-mode event hook, the callback and poll acknowledgements, the
// fault cross-checks, and the retained-cleanup guard. The per-mode
// arms (the client/validation.rs and client/recovery.rs ports, slice
// 4-11D) compose this seam with the 4-11A wire codecs; nothing in this
// file knows a validation or recovery record, so the arms can be added
// without editing client.go. The check-hook positions map to the
// worker callback checkpoints exactly where the Rust authority crosses
// them: the per-mode drive hook (DriveEvent) consumes one callback
// payload per Finding/Unknown state, and the result mailbox is read by
// the arms after DriveLoop returns. Every detail string is verbatim
// Rust.
package worker

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// startLimit bounds the version handshake and the cleanup release
// (Rust worker/client.rs START_LIMIT). A package variable so the
// handshake-timeout test can shrink the deadline; production keeps
// 30 s.
var startLimit = 30 * time.Second

// conflict builds one Conflict error with the exact Rust detail.
func conflict(detail string) error {
	return &format.Error{Code: format.CodeConflict, Detail: detail}
}

// Checkpoint is one bounded-operation cancellation hook (Rust
// CancellationToken::check; nil never cancels). The drive polls it at
// every idle spin and CancelPoll acknowledgement and raises the
// control cancellation flag when it fires and external polling is off
// (Rust client.rs drive_loop lines 165-167 and acknowledge_poll lines
// 233-240). The per-mode arms record whether the caller needs external
// polling through RequiresExternalPoll (Rust
// CancellationToken::requires_external_poll; validation.rs lines
// 148-149 and recovery.rs lines 152-153).
type Checkpoint func() error

// RequiresExternalPoll reports whether the checkpoint must be polled
// from the worker side (Rust CancellationToken::requires_external_poll:
// a token with a poll hook enables external polling).
func RequiresExternalPoll(check Checkpoint) bool { return check != nil }

// checkpointCancelled reports whether the checkpoint fired (Rust
// CancellationToken::is_cancelled: the atomic flag or the poll hook).
func checkpointCancelled(check Checkpoint) bool {
	return check != nil && check() != nil
}

// Drive is one terminal outcome of a driven worker session (Rust
// worker/client.rs Drive: Complete{guard_pending} | Fault(FaultRecord);
// the Failed transition surfaces as a returned error, exactly like the
// Rust Result<Drive>). Complete is true for the Complete arm and
// GuardPending carries its retained-cleanup flag; otherwise Fault
// holds the validated fault record of the Fault arm.
type Drive struct {
	Complete     bool
	GuardPending bool
	Fault        FaultRecord
}

// DriveEvent is one per-mode drive hook (Rust drive_loop's
// `event: impl FnMut(State, &mut Process, &Control) -> Result<bool>`):
// it observes one control state before the generic idle and terminal
// handling and reports whether it consumed the state. The validation
// arm handles Finding (client/validation.rs drive_validation) and the
// recovery arm handles Unknown (client/recovery.rs drive_recovery);
// every other state must return false.
type DriveEvent func(state uint32, child *Process, control *Control) (bool, error)

// DriveLoop polls the control through one worker session (Rust
// worker/client.rs drive_loop). The transition order is exact: the
// CancelPoll acknowledgement, the Complete/Fault/Failed terminals, the
// per-mode event hook for every remaining known state, then the
// synchronous-cancellation arm for Running/WorkerReady/Request/None,
// and an abort with the unexpected-event Conflict for any other known
// state.
func DriveLoop(child *Process, control *Control, check Checkpoint, unexpected string, event DriveEvent) (*Drive, error) {
	for {
		state := control.state()
		switch state {
		case stateCancelPoll:
			AcknowledgePoll(control, check)
		case stateComplete:
			guardPending := control.GuardPending()
			if guardPending {
				return &Drive{Complete: true, GuardPending: true}, nil
			}
			status, err := child.wait()
			if err != nil {
				return nil, err
			}
			if !status.success() {
				return nil, conflict("SDK worker completion status is invalid")
			}
			return &Drive{Complete: true}, nil
		case stateFault:
			status, err := child.wait()
			if err != nil {
				return nil, err
			}
			if code, exited := status.exitCode(); !exited || code != ownedFaultExit {
				return nil, conflict("SDK worker fault record is untrusted")
			}
			fault, err := control.FaultRecord()
			if err != nil {
				return nil, err
			}
			return &Drive{Fault: fault}, nil
		case stateFailed:
			return nil, WorkerFailure(child, control)
		default:
			handled, err := event(state, child, control)
			if err != nil {
				return nil, err
			}
			if handled {
				continue
			}
			if err := idlePoll(child, control, check, state, unexpected); err != nil {
				return nil, err
			}
		}
	}
}

// idlePoll runs the synchronous continuation arm of one drive
// iteration (Rust drive_loop: the fused Running|WorkerReady|Request|None
// match arm and the Some(_) refusal). A raw state that decodes to Rust
// None joins the Running/WorkerReady/Request arm; any other known
// state is an unexpected event that aborts the child. The poll arm
// requests cancellation through the control when the checkpoint fired
// and external polling is off, detects a child exit without a terminal
// record, and sleeps one poll interval.
func idlePoll(child *Process, control *Control, check Checkpoint, state uint32, unexpected string) error {
	switch state {
	case stateRunning, stateWorkerReady, stateRequest:
		// The fused continuation arm below.
	default:
		if stateKnown(state) {
			child.Abort()
			return conflict(unexpected)
		}
	}
	if !control.ExternalPoll() && checkpointCancelled(check) {
		control.RequestCancel()
	}
	_, exited, err := child.tryWait()
	if err != nil {
		return err
	}
	if exited {
		return conflict("SDK worker exited without a terminal record")
	}
	time.Sleep(pollInterval)
	return nil
}

// DriveWorker drives one session with the default event hook (Rust
// worker/client.rs drive): no per-mode events, so any Finding, Unknown,
// or cleanup state is the generic unexpected-event Conflict.
func DriveWorker(child *Process, control *Control, check Checkpoint) (*Drive, error) {
	return DriveLoop(child, control, check, "SDK worker emitted an unexpected event",
		func(uint32, *Process, *Control) (bool, error) { return false, nil })
}

// AdvanceSequence accepts the next strictly ordered per-item sequence
// (Rust worker/client.rs advance_sequence): a repeat or a gap aborts
// the child and refuses with the caller-selected invalid detail; a
// saturated delivered counter never matches (Rust
// delivered.saturating_add(1)).
func AdvanceSequence(child *Process, delivered *uint64, sequence uint64, invalid string) error {
	next := *delivered
	if next != math.MaxUint64 {
		next++
	}
	if sequence != next {
		child.Abort()
		return conflict(invalid)
	}
	*delivered = sequence
	return nil
}

// CallbackDecision records how one per-mode callback acknowledgement
// terminated the session (Rust worker/client.rs CallbackDecision):
// Stop when the sink requested a stop, Error(cause) when the sink
// failed. AcknowledgeCallback produces the decision; IntoError folds
// it back into the SDK error surface (Rust
// CallbackDecision::into_error: StoppedBySink and SinkFailed).
type CallbackDecision struct {
	stop  bool
	cause error
}

// IntoError folds the decision into the SDK error surface (Rust
// CallbackDecision::into_error: Stop carries StoppedBySink, Error
// carries SinkFailed around the recorded cause).
func (d *CallbackDecision) IntoError() error {
	if d.stop {
		return &format.Error{Code: format.CodeStoppedBySink, Detail: "worker sink requested stop"}
	}
	return &format.Error{Code: format.CodeSinkFailed, Detail: d.cause.Error()}
}

// AcknowledgeCallback writes one per-mode callback response (Rust
// worker/client.rs acknowledge_callback): response 0 continues, 1
// records the Stop decision, and 2 writes the sink failure into the
// session payload and records the Error decision; every arm then
// returns the control to Running. A failure writing the error payload
// surfaces before the response and state stores, exactly like the Rust
// `written?` short-circuit.
func AcknowledgeCallback(control *Control, result bool, cause error) (*CallbackDecision, error) {
	if cause == nil {
		var decision *CallbackDecision
		response := uint32(0)
		if result {
			decision = &CallbackDecision{stop: true}
			response = 1
		}
		control.SetResponse(response)
		control.SetState(stateRunning)
		return decision, nil
	}
	decision := &CallbackDecision{cause: cause}
	if err := WriteWorkerError(control, cause); err != nil {
		return decision, err
	}
	control.SetResponse(2)
	control.SetState(stateRunning)
	return decision, nil
}

// AcknowledgePoll answers one worker-initiated external cancellation
// poll (Rust worker/client.rs acknowledge_poll): the response carries
// the checkpoint state, a fired checkpoint also raises the control
// cancellation flag, and the control returns to Running.
func AcknowledgePoll(control *Control, check Checkpoint) {
	cancelled := checkpointCancelled(check)
	response := uint32(0)
	if cancelled {
		response = 1
	}
	control.SetResponse(response)
	if cancelled {
		control.RequestCancel()
	}
	control.SetState(stateRunning)
}

// WorkerFailure consumes the Failed terminal (Rust worker/client.rs
// worker_failure): the child must exit successfully and the session
// payload must carry the worker error, which is returned unchanged.
func WorkerFailure(child *Process, control *Control) error {
	status, err := child.wait()
	if err != nil {
		return err
	}
	if !status.success() {
		return conflict("SDK worker failure record has an invalid completion status")
	}
	failure, err := ReadWorkerError(control)
	if err != nil {
		return err
	}
	return failure
}

// SpawnWorker starts one isolated worker process (Rust
// worker/client.rs spawn): the first candidate of workerCandidates that
// executes with `--control <path>` and null stdio wins; a NotFound
// failure falls through to the next candidate; the last NotFound
// surfaces as the Io class and an empty candidate list as the
// unsupported class.
func SpawnWorker(control *Control) (*Process, error) {
	candidates, err := workerCandidates()
	if err != nil {
		return nil, err
	}
	var lastNotFound error
	for _, executable := range candidates {
		cmd := exec.Command(executable, "--control", control.path)
		// nil stdin/stdout/stderr connect to the null device
		// (os/exec), exactly the Rust Stdio::null() triple.
		if err := cmd.Start(); err != nil {
			// Candidate fallthrough on a missing executable (Rust
			// spawn ErrorKind::NotFound): os/exec reports the missing
			// path as os.ErrNotExist on unix and as exec.ErrNotFound on
			// Windows (CreateProcess ERROR_FILE_NOT_FOUND), so both
			// sentinels must fall through to the next candidate.
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
				lastNotFound = err
				continue
			}
			return nil, &format.Error{Code: format.CodeIO, Detail: "worker spawn: " + err.Error()}
		}
		return newProcess(cmd), nil
	}
	if lastNotFound != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "worker spawn: " + lastNotFound.Error()}
	}
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "SDK validation/recovery worker is unavailable"}
}

// StartWorker runs the version handshake and marks the session Running
// (Rust worker/client.rs start).
func StartWorker(child *Process, control *Control) error {
	if err := Handshake(child, control); err != nil {
		return err
	}
	control.SetState(stateRunning)
	return nil
}

// Handshake waits for the worker's version handshake (Rust
// worker/client.rs handshake): the control must reach WorkerReady with
// the child's own pid within startLimit, the control path is unlinked,
// and an early exit or a deadline aborts the child with the exact
// Conflict class and detail.
func Handshake(child *Process, control *Control) error {
	deadline := time.Now().Add(startLimit)
	for {
		if control.state() == stateWorkerReady {
			if control.WorkerPID() != child.ID() {
				child.Abort()
				return conflict("SDK worker identity does not match")
			}
			return control.RemovePath()
		}
		status, exited, err := child.tryWait()
		if err != nil {
			return err
		}
		if exited {
			if status.success() {
				return conflict("SDK worker exited before its version handshake")
			}
			return conflict("SDK worker version or protocol does not match")
		}
		if !time.Now().Before(deadline) {
			child.Abort()
			return conflict("SDK worker version handshake timed out")
		}
		time.Sleep(pollInterval)
	}
}

// workerCandidatesHook is a test-only candidate source: the worker
// tests point spawn at the in-process double (the test binary), which
// the default executable-relative rule cannot find because `go test`
// build-cache directories are never named "deps". Nil in production;
// the Rust authority has no such seam.
var workerCandidatesHook func() ([]string, error)

// SetWorkerCandidatesForTest installs a test-only spawn candidate
// source; nil restores the production executable-relative rule. The
// Rust authority has no such seam: production never calls this, and
// the root-package facade tests install the real worker binary
// through it (the unexported hook is unreachable from package
// iprangedb).
func SetWorkerCandidatesForTest(source func() ([]string, error)) {
	workerCandidatesHook = source
}

// workerCandidates returns the spawn candidate list (Rust
// worker/client.rs worker_candidates): the executable's own directory
// for "iprange-v4-worker", plus the deps-parent rule (Cargo places
// integration-test binaries in target/*/deps and package binaries in
// its parent; the version handshake rejects every unrelated
// executable). Duplicate candidates are dropped.
func workerCandidates() ([]string, error) {
	if workerCandidatesHook != nil {
		return workerCandidatesHook()
	}
	name := workerExecutableName() // Rust EXE_SUFFIX (empty on unix, .exe on windows)
	current, err := os.Executable()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "worker executable: " + err.Error()}
	}
	directory := filepath.Dir(current)
	candidates := []string{filepath.Join(directory, name)}
	if filepath.Base(directory) == "deps" {
		candidates = append(candidates, filepath.Join(filepath.Dir(directory), name))
	}
	if len(candidates) == 2 && candidates[1] == candidates[0] {
		candidates = candidates[:1]
	}
	return candidates, nil
}

// Process wraps one spawned worker child (Rust worker/client.rs
// Process). The wrapper owns reaping through one per-child reaper
// goroutine: cmd.Wait runs exactly once and delivers the status to a
// buffered channel; wait blocks on it and tryWait polls it
// non-blockingly, so both may complete on every platform (the POSIX
// wait4(WNOHANG) and Windows GetExitCodeProcess arms are equivalent
// to this single portable form). After the reap the os.Process
// handle is released by cmd.Wait and never touched again. Abort and
// Close kill only this child and then reap it (targeted pid, never a
// process group or a name match).
type Process struct {
	cmd    *exec.Cmd
	done   chan exitStatus
	status *exitStatus
}

// exitStatus is the reaped child outcome the drive needs (the Rust
// ExitStatus subset): the exit code and whether it exited normally.
type exitStatus struct {
	code   int
	exited bool
}

// success mirrors Rust ExitStatus::success: a normal exit with code 0.
func (s *exitStatus) success() bool { return s.exited && s.code == 0 }

// exitCode mirrors Rust ExitStatus::code: the exit code, present only
// for a normal exit.
func (s *exitStatus) exitCode() (int, bool) { return s.code, s.exited }

// newProcess wraps the successfully started child and starts its
// one-shot reaper (Rust Child spawn + the Go non-blocking-wait
// equivalent): cmd.Wait completes exactly once, the status is
// delivered to the buffered channel, and every later wait/tryWait
// path consumes the recorded status without another syscall.
func newProcess(cmd *exec.Cmd) *Process {
	p := &Process{cmd: cmd, done: make(chan exitStatus, 1)}
	go func() {
		_ = cmd.Wait()
		ps := cmd.ProcessState
		var status exitStatus
		if ps != nil {
			status = exitStatus{code: ps.ExitCode(), exited: ps.Exited()}
		}
		p.done <- status
	}()
	return p
}

// ID returns the child pid (Rust Process::id; 0 once the child was
// reaped, exactly like the Rust Option<Child>).
func (p *Process) ID() uint32 {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return uint32(p.cmd.Process.Pid)
}

// Active reports whether the wrapper still owns the running child
// (Rust Process::active: the child is Some).
func (p *Process) Active() bool { return p.cmd != nil && p.status == nil }

// wait reaps the child and returns its status (Rust Process::wait);
// the wrapper consumes the child, and a status recorded by a previous
// tryWait is returned without another syscall.
func (p *Process) wait() (*exitStatus, error) {
	if p.status != nil {
		return p.status, nil
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker process is not active"}
	}
	status := <-p.done
	p.cmd = nil
	p.status = &status
	return p.status, nil
}

// Abort kills and reaps the child (Rust Process::abort): only the
// child this Process spawned is signaled, and a reaped wrapper is a
// no-op.
func (p *Process) Abort() {
	if p.cmd == nil || p.cmd.Process == nil || p.status != nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.wait()
}

// Close aborts the child and releases the wrapper (Rust Process's
// Drop, which aborts; the Go analog for deferred cleanup).
func (p *Process) Close() { p.Abort() }

// RecordUnreadablePage inserts one source page into the ordered
// unreadable-page list of a fault-restartable operation (Rust
// worker/client.rs record_unreadable_page): a duplicate is the
// repeated Conflict, and the list footprint is charged against the
// operation's heap budget with the exact overflow and budget classes.
// Go append cannot surface allocator failure the way Rust
// try_reserve_exact can (an OOM is fatal), so the byte-budget charge
// is the equivalent guard; the list stays bounded by the session
// budget in every reachable flow.
func RecordUnreadablePage(pages *[]uint32, page uint32, maxHeapBytes uint64, repeated string) error {
	insertion := sort.Search(len(*pages), func(i int) bool { return (*pages)[i] >= page })
	if insertion < len(*pages) && (*pages)[insertion] == page {
		return conflict(repeated)
	}
	count := uint64(len(*pages))
	if count == math.MaxUint64 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "unreadable source-page list"}
	}
	bytes := (count + 1) * 4
	if bytes/4 != count+1 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "unreadable source-page list"}
	}
	if bytes > maxHeapBytes {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "unreadable source-page list"}
	}
	*pages = append(*pages, 0)
	copy((*pages)[insertion+1:], (*pages)[insertion:])
	(*pages)[insertion] = page
	return nil
}

// WorkerCleanup owns the retained cleanup guard of a completed worker
// session (Rust worker/client.rs WorkerCleanup): when a worker
// completes with guard_pending, the cleanup runs in the same isolated
// worker with a CleanupRequest/CleanupResult exchange, and the last
// problem is retained for the guard's final report.
type WorkerCleanup struct {
	child       *Process
	control     *Control
	lastProblem *WireProblem
}

// NewWorkerCleanup retains the child, the control, and the last
// problem of a guard-pending completion (Rust WorkerCleanup::new; the
// wrapper takes ownership of the child and the control).
func NewWorkerCleanup(child *Process, control *Control, lastProblem *WireProblem) *WorkerCleanup {
	return &WorkerCleanup{child: child, control: control, lastProblem: lastProblem}
}

// Release requests the isolated cleanup and waits for its result
// (Rust WorkerCleanup::release): a complete cleanup requires a
// successful child exit, an incomplete cleanup folds the retained
// problem into the worker-operation error class, and a child exit or a
// deadline without a result records the exact Conflict problem. A
// reaped wrapper releases immediately.
func (w *WorkerCleanup) Release() error {
	if !w.child.Active() {
		return nil
	}
	w.control.SetState(stateCleanupRequest)
	deadline := time.Now().Add(startLimit)
	for {
		if w.control.state() == stateCleanupResult {
			complete, problem, err := ReadValidationCleanupResult(w.control)
			if err != nil {
				return err
			}
			if complete {
				status, err := w.child.wait()
				if err != nil {
					return err
				}
				if status.success() {
					return nil
				}
				return conflict("SDK cleanup worker completion status is invalid")
			}
			if problem == nil {
				return conflict("SDK cleanup worker omitted its cleanup problem")
			}
			w.lastProblem = problem
			return w.operationError()
		}
		_, exited, err := w.child.tryWait()
		if err != nil {
			return err
		}
		if exited {
			w.lastProblem = &WireProblem{Code: format.CodeConflict, Detail: "isolated cleanup worker exited unexpectedly"}
			return w.operationError()
		}
		if !time.Now().Before(deadline) {
			w.lastProblem = &WireProblem{Code: format.CodeConflict, Detail: "isolated cleanup worker timed out"}
			return w.operationError()
		}
		time.Sleep(pollInterval)
	}
}

// LastProblem returns the retained publication problem (Rust
// WorkerCleanup::last_problem; the caller shares the wrapper's
// storage).
func (w *WorkerCleanup) LastProblem() *WireProblem { return w.lastProblem }

// LastProblemError reports the retained publication problem on the Go
// error surface (WireProblem::Err parity). The recovery package
// source-cleanup guard needs the error form but cannot import this
// package, so the conversion lives here.
func (w *WorkerCleanup) LastProblemError() error {
	if w.lastProblem == nil {
		return nil
	}
	return w.lastProblem.Err()
}

// Close aborts the retained child when it is still active (Rust
// WorkerCleanup drops the retained Process, which aborts).
func (w *WorkerCleanup) Close() {
	if w.child != nil {
		w.child.Close()
	}
}

// operationError folds the last problem into the worker-operation
// error surface (Rust WorkerCleanup::operation_error: only the code
// and errno survive).
func (w *WorkerCleanup) operationError() error {
	return &WireError{Code: w.lastProblem.Code, OSCode: w.lastProblem.OSCode}
}

// stateKnown reports whether one raw control-state word decodes to a
// known Rust State value (Rust control.rs State: 1..11; every other
// word reads as None).
func stateKnown(value uint32) bool {
	return value >= stateRequest && value <= stateCleanupResult
}
