//go:build windows

// Subprocess proofs of the Windows vectored EXCEPTION_IN_PAGE_ERROR
// containment handler (Rust worker/windows.rs tests): the owned-fault
// record survives the worker termination, and an unarmed fault falls
// through to the Go runtime's own handler without a hang and without
// exiting 197. The child harness is the exact twin of
// sigbus_posix_test.go; the handler body differs (VEH instead of
// SIGBUS), the record semantics do not.

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
	"unsafe"

	"github.com/firehol/iprange/v4/go/internal/mapping"
	"golang.org/x/sys/windows"
)

const (
	sigbusSpawned    = "IPRANGE_V4_SIGBUS_SPAWNED"
	sigbusControlEnv = "IPRANGE_V4_SIGBUS_CONTROL"
	sigbusChildOwned = "^TestSigbusOwnedChild$"
	sigbusChildChain = "^TestSigbusChainChild$"
	sigbusTimeout    = 30 * time.Second
)

type sigbusStatus struct {
	code     int
	signaled bool
	signal   syscall.Signal
	timedOut bool
}

// Synthetic in-page error delivery (kernel32 RaiseException): all
// three parameters are the documented EXCEPTION_IN_PAGE_ERROR record
// fields (access flags, the accessed virtual address, and the
// NTSTATUS), and the address selects the second page of the armed
// probe so the handler records relative == native exactly like the
// POSIX arm.
var (
	kernel32Test       = windows.NewLazySystemDLL("kernel32.dll")
	procRaiseException = kernel32Test.NewProc("RaiseException")
)

const ntStatusEndOfFile = 0xC0000011

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

// createSignalMappingFile creates the signal-mapping file with a
// DELETE-sharing handle: the mapping is unlinked while the handle stays
// open (the windows anonymous-file pattern), and Windows refuses the
// unlink unless every open handle advertises FILE_SHARE_DELETE (the
// stdlib os.OpenFile share mode lacks it).
func createSignalMappingFile(path string) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// signalMapping creates a private two-page section over a two-page
// file and maps it read-only. A hardware EXCEPTION_IN_PAGE_ERROR
// cannot be armed deterministically on Windows: CreateFileMapping
// refuses a maximum size above the file extent with
// ERROR_COMMITMENT_LIMIT, MapViewOfFile cannot exceed the section,
// and SetEndOfFile refuses while a section is open (ERROR_USER_MAPPED_
// FILE). The subprocess proofs therefore deliver the in-page error
// synthetically (signalFault) with the real vectored dispatch, record
// layout, and accessed-address parameters; POSIX arms the real SIGBUS.
func signalMapping(t *testing.T, label string) (*mapping.Mapping, uintptr, uint64) {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf(".iprange-v4-signal-%s-%d", label, os.Getpid()))
	f, err := createSignalMappingFile(path)
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
	if err := f.Close(); err != nil {
		t.Fatal("signal mapping close:", err)
	}
	view, err := m.View(0, native)
	if err != nil {
		t.Fatal("signal mapping view:", err)
	}
	return m, baseOf(view), native
}

// signalFault raises one synthetically delivered in-page error on the
// second page of the armed probe (the Windows twin of the POSIX SIGBUS
// fault): kernel32 RaiseException with the EXCEPTION_IN_PAGE_ERROR code
// and the three documented parameters (access type, the accessed
// page-2 address inside the armed region, and the NTSTATUS). The
// vectored handler receives the real EXCEPTION_POINTERS record and
// claims it exactly like a hardware delivery. If the raise returns, the
// fault was not claimed and the child exits 87 to prove it.
func signalFault(t *testing.T, m *mapping.Mapping, native uint64) {
	t.Helper()
	view, err := m.View(0, native)
	if err != nil {
		t.Fatal("fault view:", err)
	}
	args := [3]uintptr{0, baseOf(view) + uintptr(native), ntStatusEndOfFile}
	procRaiseException.Call(exceptionInPageError, 0, 3, uintptr(unsafe.Pointer(&args[0])))
	os.Exit(87)
}

// TestSigbusOwnedFaultRecordSurvivesWorkerExit mirrors Rust
// owned_fault_record_survives_worker_exit: the parent creates the
// control, the child opens it by path, installs the vectored handler,
// arms a two-page probe as RoleScratch, and faults the truncated second
// page. The parent reads the exact record after the child exits 197.
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
	mapping, base, native := signalMapping(t, "record")
	if err := control.Arm(41, RoleScratch, base, 2*native); err != nil {
		t.Fatal("arm:", err)
	}
	signalFault(t, mapping, native)
}

// TestSigbusChainsIntoGoRuntime proves the fall-through path is safe
// with the Go runtime's own exception handling: an unarmed fault must
// return EXCEPTION_CONTINUE_SEARCH and reach the runtime's fatal path.
// The assertions are no hang, no exit 197, and no plain success.
func TestSigbusChainsIntoGoRuntime(t *testing.T) {
	status := spawnSigbusChild(t, sigbusChildChain)
	if status.timedOut {
		t.Fatal("chain child timed out: the unclaimed exception never reached the runtime")
	}
	if status.code == 0 || status.code == 87 || status.code == ownedFaultExit {
		t.Fatalf("chain child exited %d: the unarmed fault must chain into the runtime fatal", status.code)
	}
}

// TestSigbusChainChild is the subprocess entry of the runtime
// fall-through proof: install the handler, then fault an unarmed
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
	mapping, _, native := signalMapping(t, "chain")
	signalFault(t, mapping, native)
}
