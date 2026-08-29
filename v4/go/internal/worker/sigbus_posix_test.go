//go:build (linux || darwin || freebsd) && (amd64 || arm64)

// Subprocess proofs of the SIGBUS isolation handler over a real mapped
// fault (Rust worker/posix.rs tests, run natively on linux, darwin, and
// freebsd): the owned-fault record survives the worker exit, and an
// unarmed fault chains into the Go runtime's own SIGBUS handler without
// a hang and without exiting 197. The 15-case previous-disposition
// matrix lives in sigbus_matrix_v4work_test.go (linux/amd64 v4work) and
// needs the v4work naked symbols.

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

const (
	// sigbusSpawned gates the subprocess entry points: a normal suite run
	// skips them regardless of ambient variables (the same pattern as
	// internal/writer/crash_v4work_test.go).
	sigbusSpawned     = "IPRANGE_V4_SIGBUS_SPAWNED"
	sigbusCaseEnv     = "IPRANGE_V4_SIGBUS_CASE"
	sigbusControlEnv  = "IPRANGE_V4_SIGBUS_CONTROL"
	sigbusChildOwned  = "^TestSigbusOwnedChild$"
	sigbusChildChain  = "^TestSigbusChainChild$"
	sigbusChildMatrix = "^TestSigbusChild$"
	sigbusTimeout     = 30 * time.Second
)

// sigbusStatus is one child outcome: either a numeric exit code, a death
// by signal, or a timeout (spawn failure is a test failure, not a status).
type sigbusStatus struct {
	code     int
	signaled bool
	signal   syscall.Signal
	timedOut bool
}

// spawnSigbusChild runs this test binary as the named entry point with a
// stripped environment plus the spawn marker and the caller's variables
// (Rust Command::new(current_exe) + CASE_ENV/CONTROL_ENV).
func spawnSigbusChild(t *testing.T, run string, env ...string) sigbusStatus {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sigbusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+run)
	childEnv := make([]string, 0, len(os.Environ())+len(env)+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_SIGBUS_") {
			continue
		}
		childEnv = append(childEnv, kv)
	}
	childEnv = append(childEnv, sigbusSpawned+"=1")
	childEnv = append(childEnv, env...)
	cmd.Env = childEnv
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return sigbusStatus{timedOut: true}
	}
	if err == nil {
		return sigbusStatus{code: 0}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return sigbusStatus{signaled: true, signal: ws.Signal()}
		}
		return sigbusStatus{code: exitErr.ExitCode()}
	}
	t.Fatalf("spawn %s: %v", run, err)
	return sigbusStatus{}
}

// signalMapping creates a private two-page file, maps it read-only, and
// (when truncate is set) truncates the file to the first native page so
// the second page faults on access (Rust tests::mapping).
func signalMapping(t *testing.T, label string, truncate bool) (*mapping.Mapping, uintptr, uint64) {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf(".iprange-v4-signal-%s-%d", label, os.Getpid()))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("signal mapping create:", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal("signal mapping unlink:", err)
	}
	native := uint64(os.Getpagesize())
	if err := f.Truncate(int64(2 * native)); err != nil {
		t.Fatal("signal mapping truncate:", err)
	}
	m, err := mapping.MapFile(f, 2*native, false)
	if err != nil {
		t.Fatal("signal mapping map:", err)
	}
	if truncate {
		if err := f.Truncate(int64(native)); err != nil {
			t.Fatal("signal mapping fault truncate:", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal("signal mapping close:", err)
	}
	view, err := m.View(0, native)
	if err != nil {
		t.Fatal("signal mapping view:", err)
	}
	return m, baseOf(view), native
}

// signalFault reads the first byte of the truncated second page; the load
// raises SIGBUS on the locked thread (Rust tests::fault). The byte must
// feed a branch the compiler cannot fold: a dead load would be
// eliminated and the fault would never fire. If the read survives, the
// child exits 86/87 to prove the fault did not fire.
func signalFault(t *testing.T, m *mapping.Mapping, native uint64) {
	t.Helper()
	view, err := m.View(native, native)
	if err != nil {
		t.Fatal("fault view:", err)
	}
	if view[0] != 0 {
		os.Exit(86)
	}
	os.Exit(87)
}

// TestSigbusOwnedFaultRecordSurvivesWorkerExit mirrors Rust
// owned_fault_record_survives_worker_exit: the parent creates the
// control, the child opens it by path, installs the handler, arms a
// two-page probe as RoleScratch, and faults the truncated second page.
// The parent reads the exact record after the child exits 197.
func TestSigbusOwnedFaultRecordSurvivesWorkerExit(t *testing.T) {
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create control:", err)
	}
	defer control.Close()
	status := spawnSigbusChild(t, sigbusChildOwned, sigbusControlEnv+"="+control.path)
	if status.timedOut {
		t.Fatal("owned-fault child timed out")
	}
	if status.signaled {
		t.Fatalf("owned-fault child died by signal %v, want exit %d", status.signal, ownedFaultExit)
	}
	if status.code != ownedFaultExit {
		t.Fatalf("owned-fault child exited %d, want %d", status.code, ownedFaultExit)
	}
	record, err := control.FaultRecord()
	if err != nil {
		t.Fatal("fault record:", err)
	}
	native := uint64(os.Getpagesize())
	if record.Role != RoleScratch {
		t.Fatalf("record role = %d, want %d", record.Role, RoleScratch)
	}
	if record.Relative != native {
		t.Fatalf("record relative = %d, want %d", record.Relative, native)
	}
	if record.MappingLen != 2*native {
		t.Fatalf("record mapping_len = %d, want %d", record.MappingLen, 2*native)
	}
}

// TestSigbusOwnedChild is the subprocess entry of the owned-fault record
// proof (Rust owned_fault_record_child).
func TestSigbusOwnedChild(t *testing.T) {
	if os.Getenv(sigbusSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(sigbusTimeout, func() { os.Exit(1) })
	path := os.Getenv(sigbusControlEnv)
	if path == "" {
		t.Fatal("missing " + sigbusControlEnv)
	}
	runtime.LockOSThread()
	control, err := OpenWorker(path)
	if err != nil {
		t.Fatal("open control:", err)
	}
	handler, err := control.InstallHandler()
	if err != nil {
		t.Fatal("install handler:", err)
	}
	defer handler.Close()
	mapping, base, native := signalMapping(t, "record", true)
	if err := control.Arm(41, RoleScratch, base, 2*native); err != nil {
		t.Fatal("arm:", err)
	}
	signalFault(t, mapping, native)
}

// TestSigbusChainsIntoGoRuntime proves the previous-disposition chain is
// safe with the Go runtime's own SIGBUS handler: an unarmed fault must
// restore that exact action and tail-jump into it with the kernel frame
// intact. The runtime then fatals on the unexpected fault address; the
// assertions are no hang, no exit 197, and no plain success.
func TestSigbusChainsIntoGoRuntime(t *testing.T) {
	status := spawnSigbusChild(t, sigbusChildChain)
	if status.timedOut {
		t.Fatal("chain child timed out: the tail-jump into the Go runtime handler hung")
	}
	if status.signaled {
		t.Fatalf("chain child died by signal %v: the chained runtime handler must own the fault", status.signal)
	}
	if status.code == 0 || status.code == 87 || status.code == ownedFaultExit {
		t.Fatalf("chain child exited %d: the unarmed fault must chain into the runtime fatal", status.code)
	}
}

// TestSigbusChainChild is the subprocess entry of the Go-runtime chaining
// proof: install the handler on the locked thread, then fault an unarmed
// mapping.
func TestSigbusChainChild(t *testing.T) {
	if os.Getenv(sigbusSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(sigbusTimeout, func() { os.Exit(1) })
	runtime.LockOSThread()
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create control:", err)
	}
	defer control.Close()
	handler, err := control.InstallHandler()
	if err != nil {
		t.Fatal("install handler:", err)
	}
	defer handler.Close()
	mapping, _, native := signalMapping(t, "chain", true)
	signalFault(t, mapping, native)
}
