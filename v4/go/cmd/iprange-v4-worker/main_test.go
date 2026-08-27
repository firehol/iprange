//go:build linux || darwin || freebsd || windows

// Main-flow subprocess tests of the worker binary (Rust worker.rs run
// + the 4-11A wire-era control): usage and protocol refusals, the
// worker-ready handshake, and the per-mode dispatch with a fake parent
// control. The fixture pattern mirrors internal/worker
// sigbus_linux_amd64_test.go: the test binary re-runs itself as the
// worker child (os.Executable + -test.run + env marker). The fake
// parent writes the control header bytes itself because the worker
// package keeps its control path private and exposes no state reader;
// the fixture observes the state word through its own read-only
// mapping, the same way the Rust parent drive loop reads control.state.

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

const (
	// workerSpawned gates the subprocess entry; the child argv travels
	// in indexed env entries (the same spawn pattern as
	// internal/worker sigbus_linux_amd64_test.go).
	workerSpawned = "IPRANGE_V4_WORKER_MAIN_SPAWNED"
	// workerMainCount and workerMainArg carry the child argv in env
	// entries (NUL-joined values are rejected by os/exec).
	workerMainCount = "IPRANGE_V4_WORKER_MAIN_ARG_COUNT"
	workerMainArg   = "IPRANGE_V4_WORKER_MAIN_ARG"
	workerTimeout   = 30 * time.Second
)

// Control-header wire constants repeated by the fixture (Rust
// worker/control.rs MAGIC / PROTOCOL / STATE_AT / BUILD_AT /
// PARENT_PID_AT / CONTROL_LEN; the worker package keeps them private).
const (
	fixtureControlLen   = 1024 * 1024
	fixtureProtocol     = uint32(1)
	fixtureMagicOffset  = 0
	fixtureProtocolOff  = 8
	fixtureStateOffset  = 12
	fixtureBuildOffset  = 16
	fixtureParentPIDOff = 96
)

var fixtureMagic = [8]byte{'I', 'P', 'R', '4', 'W', 'R', 'K', 0}

// TestWorkerMainChild is the subprocess entry point: with the spawn
// marker it runs the real worker main (run) with the caller's argv and
// exits with its code. Without the marker it is skipped.
func TestWorkerMainChild(t *testing.T) {
	if os.Getenv(workerSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	count, err := strconv.Atoi(os.Getenv(workerMainCount))
	if err != nil {
		t.Fatalf("worker main argv count: %v", err)
	}
	args := make([]string, count)
	for index := 0; index < count; index++ {
		args[index] = os.Getenv(workerMainArg + "_" + strconv.Itoa(index))
	}
	os.Exit(run(args))
}

// fakeParent is the parent side of one worker session over a control
// file the fixture created at an exact path: the worker-package control
// handle (accessors + wire codecs) and a private read-only mapping used
// to observe the state word (Rust Control::state parity).
type fakeParent struct {
	control *worker.Control
	observe *mapping.Mapping
	state   []byte
	path    string
}

func (p *fakeParent) close() {
	p.control.Close()
	_ = p.observe.Close()
}

// newFakeParent creates the 1 MiB control file at an exact path with
// the create_parent header bytes (magic, protocol, build id, Request
// state, parent pid) written through a mapped view only, then attaches
// the parent control handle and the state observer.
func newFakeParent(t *testing.T) *fakeParent {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.ctl")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("fixture control create:", err)
	}
	if err := f.Truncate(fixtureControlLen); err != nil {
		t.Fatal("fixture control truncate:", err)
	}
	m, err := mapping.MapFile(f, fixtureControlLen, true)
	if err != nil {
		t.Fatal("fixture control map:", err)
	}
	data, err := m.View(0, fixtureControlLen)
	if err != nil {
		t.Fatal("fixture control view:", err)
	}
	clear(data)
	copy(data[0:8], fixtureMagic[:])
	format.PutU32(data[8:12], fixtureProtocol)
	format.PutU32(data[12:16], worker.StateRequest)
	copy(data[fixtureBuildOffset:fixtureBuildOffset+buildIDLen], worker.BuildIDDefault)
	format.PutU32(data[fixtureParentPIDOff:fixtureParentPIDOff+4], uint32(os.Getpid()))
	if err := m.Close(); err != nil {
		t.Fatal("fixture control close:", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal("fixture control close file:", err)
	}
	control, err := worker.OpenWorker(path)
	if err != nil {
		t.Fatal("fixture open control:", err)
	}
	observed, err := os.Open(path)
	if err != nil {
		t.Fatal("fixture observation open:", err)
	}
	om, err := mapping.MapFile(observed, fixtureControlLen, false)
	if err != nil {
		t.Fatal("fixture observation map:", err)
	}
	view, err := om.View(0, fixtureControlLen)
	if err != nil {
		t.Fatal("fixture observation view:", err)
	}
	return &fakeParent{control: control, observe: om, state: view, path: path}
}

// waitState spins until the control state word reaches wanted, exactly
// like the Rust parent drive loop reads the state each millisecond.
func (p *fakeParent) waitState(t *testing.T, wanted uint32) {
	t.Helper()
	deadline := time.Now().Add(workerTimeout)
	for time.Now().Before(deadline) {
		if format.U32(p.state[fixtureStateOffset:fixtureStateOffset+4]) == wanted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("control state did not reach %d (word = %d)", wanted, format.U32(p.state[fixtureStateOffset:fixtureStateOffset+4]))
}

// spawnWorkerMain starts the worker child against one control path
// (Rust Command::new(current_exe).arg("--control").arg(path)).
func spawnWorkerMain(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWorkerMainChild$")
	childEnv := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_WORKER_MAIN_") {
			continue
		}
		childEnv = append(childEnv, kv)
	}
	childEnv = append(childEnv, workerSpawned+"=1", workerMainCount+"="+strconv.Itoa(len(args)))
	for index, arg := range args {
		childEnv = append(childEnv, workerMainArg+"_"+strconv.Itoa(index)+"="+arg)
	}
	cmd.Env = childEnv
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd
}

// runWorkerMain spawns the worker child, waits for it, and returns its
// exit code.
func runWorkerMain(t *testing.T, args ...string) int {
	t.Helper()
	cmd := spawnWorkerMain(t, args...)
	if err := cmd.Run(); err == nil {
		return 0
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("spawn worker main: %v", err)
		}
		return exitErr.ExitCode()
	}
}

// handshake completes the parent side of the worker-ready protocol:
// wait WorkerReady, prove the recorded pid matches the child, then
// release Running (Rust client.rs handshake + start).
func (p *fakeParent) handshake(t *testing.T, child *exec.Cmd) {
	t.Helper()
	p.waitState(t, worker.StateWorkerReady)
	if pid := p.control.WorkerPID(); pid != uint32(child.Process.Pid) {
		t.Fatalf("worker pid = %d, want %d", pid, child.Process.Pid)
	}
	p.control.SetState(worker.StateRunning)
}

// driveDispatch runs one complete worker session to the given terminal
// state: spawn, handshake, wait for the terminal, reap, return the
// exit code.
func (p *fakeParent) driveDispatch(t *testing.T, terminal uint32) int {
	t.Helper()
	cmd := spawnWorkerMain(t, "--control", p.path)
	if err := cmd.Start(); err != nil {
		t.Fatal("start worker:", err)
	}
	p.handshake(t, cmd)
	p.waitState(t, terminal)
	if err := cmd.Wait(); err == nil {
		return 0
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait worker: %v", err)
		}
		return exitErr.ExitCode()
	}
}

// wantCode fails the test unless err carries the given typed code
// (both the reconstructed WireError pair and the plain format.Error
// class are accepted).
func wantCode(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %d, got nil", code)
	}
	var wire *worker.WireError
	if errors.As(err, &wire) {
		if wire.Code == code {
			return
		}
		t.Fatalf("expected error code %d, got %d (%v)", code, wire.Code, err)
	}
	var formatted *format.Error
	if !errors.As(err, &formatted) || formatted.Code != code {
		t.Fatalf("expected error code %d, got %v", code, err)
	}
}

func TestUsageRefusal(t *testing.T) {
	cases := [][]string{
		nil,
		{"--control"},
		{"--control", "a", "b"},
		{"--flag", "x"},
	}
	for _, args := range cases {
		if code := run(args); code != exitUsage {
			t.Errorf("run(%q) = %d, want %d", args, code, exitUsage)
		}
	}
	if code := runWorkerMain(t, "--control"); code != exitUsage {
		t.Errorf("subprocess usage = %d, want %d", code, exitUsage)
	}
}

func TestProtocolRefusalBadControlPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.ctl")
	if code := run([]string{"--control", path}); code != exitProtocol {
		t.Errorf("run = %d, want %d", code, exitProtocol)
	}
	if code := runWorkerMain(t, "--control", path); code != exitProtocol {
		t.Errorf("subprocess protocol = %d, want %d", code, exitProtocol)
	}
}

// buildIDLen is the fixed control build-identity width (Rust
// control.rs BUILD_LEN 64); the worker package keeps its buildLen
// private, so the test repeats the value.
const buildIDLen = 64

func TestBuildIDSeam(t *testing.T) {
	if got := len(worker.BuildIDDefault); got != buildIDLen {
		t.Fatalf("BuildIDDefault length = %d, want %d", got, buildIDLen)
	}
	t.Setenv("IPRANGE_V4_BUILD_ID", "")
	if id := workerBuildID(); id != worker.BuildIDDefault {
		t.Fatalf("workerBuildID() = %q; want the default", id)
	}
	custom := strings.Repeat("a", buildIDLen)
	t.Setenv("IPRANGE_V4_BUILD_ID", custom)
	if id := workerBuildID(); id != custom {
		t.Fatalf("env build id = %q; want %q", id, custom)
	}
	t.Setenv("IPRANGE_V4_BUILD_ID", "short")
	if err := worker.SetBuildID(workerBuildID()); err == nil {
		t.Fatal("short env build id accepted")
	}
	if err := worker.SetBuildID(worker.BuildIDDefault); err != nil {
		t.Fatalf("default build id rejected: %v", err)
	}
}

func TestWorkerValidateDispatch(t *testing.T) {
	parent := newFakeParent(t)
	defer parent.close()
	parent.control.SetOpcode(worker.OpcodeValidate)
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	missing := filepath.Join(t.TempDir(), "missing.v4")
	if err := worker.WriteValidationRequest(parent.control, missing, validation.ValidationModeImmutableCurrent, nil, budget, nil, 0); err != nil {
		t.Fatal("write request:", err)
	}
	if code := parent.driveDispatch(t, worker.StateComplete); code != 0 {
		t.Fatalf("worker exit = %d, want 0", code)
	}
	if parent.control.GuardPending() {
		t.Fatal("validation reported a pending guard")
	}
	result, failure, retained := worker.ReadValidationResult(parent.control)
	if result != nil || failure == nil || retained != nil {
		t.Fatalf("result = %v, failure = %v, retained = %v", result, failure, retained)
	}
	wantCode(t, failure.Cause, format.CodeIO)
}

func TestWorkerInspectDispatch(t *testing.T) {
	parent := newFakeParent(t)
	defer parent.close()
	parent.control.SetOpcode(worker.OpcodeInspectRecoveryCandidates)
	budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 30, MaxOpenFiles: 4}
	missing := filepath.Join(t.TempDir(), "missing.v4")
	if err := worker.WriteInspectionRequest(parent.control, missing, recovery.RecoveryInspectionImmutable, budget, nil); err != nil {
		t.Fatal("write request:", err)
	}
	if code := parent.driveDispatch(t, worker.StateComplete); code != 0 {
		t.Fatalf("worker exit = %d, want 0", code)
	}
	if parent.control.GuardPending() {
		t.Fatal("inspection reported a pending guard")
	}
	inspection, err := worker.ReadInspectionResult(parent.control)
	if inspection != nil || err == nil {
		t.Fatalf("inspection = %v, err = %v; want the error arm", inspection, err)
	}
	wantCode(t, err, format.CodeIO)
}

func TestWorkerRecoverDispatch(t *testing.T) {
	parent := newFakeParent(t)
	defer parent.close()
	parent.control.SetOpcode(worker.OpcodeRecover)
	source := filepath.Join(t.TempDir(), "missing.v4")
	destination := filepath.Join(t.TempDir(), "out.v4")
	budget := &recovery.RecoveryBudget{MaxHeapBytes: 1 << 30, MaxOutputPages: 1 << 16, MaxOpenFiles: 8}
	candidate := &recovery.RecoveryCandidate{
		Label:          recovery.CandidateNewest,
		SourceIdentity: publication.LocalFileIdentity{},
		DatabaseID:     [16]byte{1},
		CommitNonce:    [16]byte{2},
	}
	// The request carries the real parent-created attempt facts (Rust
	// recover_once: the parent creates and secures the attempt before
	// the request; the worker machine resumes it, and the open-source
	// failure discards it in-session).
	created, createFailure := publication.CreatePublishAttempt(destination, publication.PolicyFailIfExists)
	if createFailure != nil {
		t.Fatalf("create attempt: %v", createFailure.Cause)
	}
	facts := created.Facts()
	if err := worker.WriteRecoveryRequest(parent.control, source, destination, candidate, worker.WorkerModeImmutable, budget, &facts, nil, 0); err != nil {
		t.Fatal("write request:", err)
	}
	created.Close()
	if code := parent.driveDispatch(t, worker.StateComplete); code != 0 {
		t.Fatalf("worker exit = %d, want 0", code)
	}
	if parent.control.GuardPending() {
		t.Fatal("recovery reported a pending guard")
	}
	outcome, retained, err := worker.ReadRecoveryOutcome(parent.control)
	if err != nil {
		t.Fatal("read outcome:", err)
	}
	if outcome == nil || outcome.Result != nil || outcome.Failure == nil || retained != nil {
		t.Fatalf("outcome = %+v, retained = %v", outcome, retained)
	}
	wantCode(t, outcome.Failure.Cause, format.CodeIO)
	// The machine discarded the resumed attempt on the source-open
	// failure; the destination directory holds no private attempt.
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal("read destination directory:", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".iprange-publish-") {
			t.Fatalf("private attempt %s survived the source-open failure", entry.Name())
		}
	}
}

func TestWorkerCleanupDispatch(t *testing.T) {
	parent := newFakeParent(t)
	defer parent.close()
	parent.control.SetOpcode(worker.OpcodeCleanupRecoveryAttempt)
	output := &publication.PrivateOutputAttempt{}
	if err := worker.WriteCleanupRequest(parent.control, filepath.Join(t.TempDir(), "out.v4"), output, nil, nil); err != nil {
		t.Fatal("write request:", err)
	}
	if code := parent.driveDispatch(t, worker.StateComplete); code != 0 {
		t.Fatalf("worker exit = %d, want 0", code)
	}
	if parent.control.GuardPending() {
		t.Fatal("cleanup reported a pending guard")
	}
	discarded, scratch, err := worker.ReadCleanupResult(parent.control)
	if err != nil {
		t.Fatal("read cleanup result:", err)
	}
	if discarded == nil || discarded.Artifact == nil {
		t.Fatalf("discarded = %+v, want the failed-attempt artifact for zero facts", discarded)
	}
	if scratch != nil {
		t.Fatalf("scratch = %+v, want nil", scratch)
	}
	wantCode(t, discarded.Artifact.Error, format.CodeInvalidArgument)
}

// patchControl overwrites one header field of one control file before
// any worker spawns (test-only; the Rust worker-side refusals are
// verify_request control.rs:214-223, and every other header field
// stays intact). The write lands in the file's page cache through a
// fresh writable mapping, so every existing mapping of the control
// observes it.
func patchControl(t *testing.T, path string, offset int, value []byte) {
	t.Helper()
	if len(value) == 0 || offset < 0 || offset+len(value) > fixtureControlLen {
		t.Fatalf("patch control range %d+%d out of bounds", offset, len(value))
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal("patch control open:", err)
	}
	m, err := mapping.MapFile(f, fixtureControlLen, true)
	if err != nil {
		t.Fatal("patch control map:", err)
	}
	data, err := m.View(0, fixtureControlLen)
	if err != nil {
		t.Fatal("patch control view:", err)
	}
	copy(data[offset:offset+len(value)], value)
	if err := m.Close(); err != nil {
		t.Fatal("patch control close map:", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal("patch control close:", err)
	}
}

// TestProtocolRefusalBuildIDMismatch proves the worker refuses a
// control whose 64 build-id bytes differ with the exact Conflict class
// and exit code 65, before WorkerReady (Rust control.rs
// verify_request:214-223 and worker.rs EXIT_PROTOCOL parity).
func TestProtocolRefusalBuildIDMismatch(t *testing.T) {
	parent := newFakeParent(t)
	defer parent.close()
	patchControl(t, parent.path, fixtureBuildOffset, []byte(strings.Repeat("x", buildIDLen)))
	if code := run([]string{"--control", parent.path}); code != exitProtocol {
		t.Errorf("run = %d, want %d", code, exitProtocol)
	}
	if code := runWorkerMain(t, "--control", parent.path); code != exitProtocol {
		t.Errorf("subprocess protocol = %d, want %d", code, exitProtocol)
	}
	control, err := worker.OpenWorker(parent.path)
	if err != nil {
		t.Fatal("open patched control:", err)
	}
	defer control.Close()
	err = control.VerifyRequest()
	if err == nil {
		t.Fatal("patched control verified")
	}
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeConflict || fe.Detail != "worker protocol does not match the SDK" {
		t.Fatalf("verify error = %v, want the exact protocol Conflict", err)
	}
}

// TestProtocolRefusalHeaderMismatches extends the build-id refusal to
// every remaining header field verify_request checks (Rust
// control.rs:214-223): a patched magic, protocol, state (away from
// Request), or zeroed parent pid makes the worker exit 65 before
// WorkerReady, and the parent VerifyRequest reports the exact protocol
// Conflict on the same control.
func TestProtocolRefusalHeaderMismatches(t *testing.T) {
	variants := []struct {
		name   string
		offset int
		value  []byte
	}{
		{"magic", fixtureMagicOffset, []byte("XXXXXXXX")},
		{"protocol", fixtureProtocolOff, []byte{2, 0, 0, 0}},
		{"state", fixtureStateOffset, []byte{2, 0, 0, 0}},
		{"parent-pid", fixtureParentPIDOff, []byte{0, 0, 0, 0}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			parent := newFakeParent(t)
			defer parent.close()
			patchControl(t, parent.path, variant.offset, variant.value)
			if code := run([]string{"--control", parent.path}); code != exitProtocol {
				t.Errorf("run = %d, want %d", code, exitProtocol)
			}
			if code := runWorkerMain(t, "--control", parent.path); code != exitProtocol {
				t.Errorf("subprocess protocol = %d, want %d", code, exitProtocol)
			}
			control, err := worker.OpenWorker(parent.path)
			if err != nil {
				t.Fatal("open patched control:", err)
			}
			defer control.Close()
			err = control.VerifyRequest()
			if err == nil {
				t.Fatal("patched control verified")
			}
			var fe *format.Error
			if !errors.As(err, &fe) || fe.Code != format.CodeConflict || fe.Detail != "worker protocol does not match the SDK" {
				t.Fatalf("verify error = %v, want the exact protocol Conflict", err)
			}
		})
	}
}
