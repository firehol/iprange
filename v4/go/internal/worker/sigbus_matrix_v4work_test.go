//go:build linux && amd64 && v4work && (amd64 || arm64)

// The 15-case previous-disposition signal matrix (Rust
// worker/posix.rs::tests::signal_chain_subprocess_matrix), running under
// the v4work configuration: the matrix's naked previous-handler symbols
// live in sigbus_matrix_v4work.s and must not reach production builds.

package worker

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// raiseSigbus delivers SIGBUS to the current thread exactly like libc
// raise() (tgkill against the calling thread's tid; linux-only, the
// matrix never runs elsewhere).
func raiseSigbus() {
	if err := unix.Tgkill(unix.Getpid(), unix.Gettid(), unix.SIGBUS); err != nil {
		os.Exit(85)
	}
}

// installPrevious installs the case's previous SIGBUS disposition with
// the project rt_sigreturn stub wherever a handler may return through the
// kernel frame (Rust install_previous; libc always sets SA_RESTORER, and
// the probe matrix proves a raw install without it dies 139 before the
// handler runs).
func installPrevious(caseName string) {
	action := kernelSigaction{}
	switch caseName {
	case "user-one":
		action.Handler = matrixOneArgumentAddr()
	case "user-mask":
		action.Handler = matrixMaskedSiginfoAddr()
		action.Flags = sigActionSigInfo | sigActionRestore
		action.Restorer = rtSigreturnStubAddr()
		action.Mask = 1 << 9 // SIGUSR1 bit
	case "user-nodefer":
		action.Handler = matrixNodeferSiginfoAddr()
		action.Flags = sigActionSigInfo | sigActionNoDefer | sigActionRestore
		action.Restorer = rtSigreturnStubAddr()
	case "user-reset", "native-reset", "captured-reset":
		action.Handler = matrixResetSiginfoAddr()
		action.Flags = sigActionSigInfo | sigActionReset | sigActionRestore
		action.Restorer = rtSigreturnStubAddr()
	case "default":
		action.Handler = sigDFL
	case "ignore":
		action.Handler = sigIGN
	default:
		action.Handler = matrixSiginfoAddr()
		action.Flags = sigActionSigInfo | sigActionRestore
		action.Restorer = rtSigreturnStubAddr()
	}
	if errno := rtSigactionSet(sigBus, uintptr(unsafe.Pointer(&action)), 0); errno != 0 {
		os.Exit(85)
	}
}

// TestSigbusChainSubprocessMatrix runs the full previous-disposition
// matrix as subprocesses and asserts the exact Rust outcomes for
// linux/amd64 (Rust signal_chain_subprocess_matrix).
func TestSigbusChainSubprocessMatrix(t *testing.T) {
	native := spawnSigbusChild(t, sigbusChildMatrix, sigbusCaseEnv+"=native-reset")
	if native.timedOut {
		t.Fatal("native-reset child timed out")
	}
	if native.code != 90 {
		t.Fatalf("native-reset exited %d, want 90 (SA_RESETHAND reset before delivery)", native.code)
	}
	for _, tc := range []struct {
		name     string
		code     int
		signaled bool
		signal   syscall.Signal
	}{
		{"owned", ownedFaultExit, false, 0},
		{"user-one", 81, false, 0},
		{"user-siginfo", 82, false, 0},
		{"user-mask", 88, false, 0},
		{"user-nodefer", 89, false, 0},
		{"user-reset", 90, false, 0},
		{"captured-reset", 92, false, 0},
		{"unarmed", 83, false, 0},
		{"out-of-region", 83, false, 0},
		{"stale-region", 83, false, 0},
		{"nested", 83, false, 0},
		{"null-info", 86, false, 0},
		{"replacement", 91, false, 0},
		{"default", 0, true, syscall.SIGBUS},
		{"ignore", 84, false, 0},
	} {
		status := spawnSigbusChild(t, sigbusChildMatrix, sigbusCaseEnv+"="+tc.name)
		if status.timedOut {
			t.Fatalf("case %s timed out", tc.name)
		}
		if tc.signaled {
			if !status.signaled {
				t.Fatalf("case %s exited %d, want death by signal %d", tc.name, status.code, tc.signal)
			}
			if status.signal != tc.signal {
				t.Fatalf("case %s died by signal %v, want %v", tc.name, status.signal, tc.signal)
			}
			continue
		}
		if status.signaled {
			t.Fatalf("case %s died by signal %v, want exit %d", tc.name, status.signal, tc.code)
		}
		if status.code != tc.code {
			t.Fatalf("case %s exited %d, want %d", tc.name, status.code, tc.code)
		}
	}
}

// TestSigbusChild is the subprocess entry point of the signal matrix
// (Rust signal_chain_child): it runs only when spawned by
// TestSigbusChainSubprocessMatrix.
func TestSigbusChild(t *testing.T) {
	if os.Getenv(sigbusSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(sigbusTimeout, func() { os.Exit(1) })
	caseName := os.Getenv(sigbusCaseEnv)
	if caseName == "" {
		t.Fatal("missing " + sigbusCaseEnv)
	}
	runtime.LockOSThread()
	installPrevious(caseName)
	if caseName == "native-reset" {
		raiseSigbus()
		os.Exit(84)
	}
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create control:", err)
	}
	if err := control.RemovePath(); err != nil {
		t.Fatal("remove control path:", err)
	}
	handler, err := control.InstallHandler()
	if err != nil {
		t.Fatal("install handler:", err)
	}
	if caseName == "captured-reset" {
		if handler.previousAction.Flags&sigActionReset != 0 {
			os.Exit(92)
		}
		os.Exit(93)
	}
	switch caseName {
	case "owned":
		mapping, base, native := signalMapping(t, "owned", true)
		if err := control.Arm(7, RoleSource, base, 2*native); err != nil {
			t.Fatal("arm:", err)
		}
		signalFault(t, mapping, native)
	case "user-one", "user-siginfo", "user-mask", "user-nodefer", "user-reset", "default", "ignore":
		raiseSigbus()
		os.Exit(84)
	case "unarmed":
		mapping, _, native := signalMapping(t, "unarmed", true)
		signalFault(t, mapping, native)
	case "out-of-region":
		_, activeBase, activeNative := signalMapping(t, "out-active", false)
		if err := control.Arm(11, RoleSource, activeBase, 2*activeNative); err != nil {
			t.Fatal("arm:", err)
		}
		mapping, _, native := signalMapping(t, "out-fault", true)
		signalFault(t, mapping, native)
	case "stale-region":
		stale, staleBase, staleNative := signalMapping(t, "stale-fault", true)
		if err := control.Arm(13, RoleSource, staleBase, 2*staleNative); err != nil {
			t.Fatal("arm:", err)
		}
		control.Disarm()
		_, activeBase, activeNative := signalMapping(t, "stale-active", false)
		if err := control.Arm(14, RoleSource, activeBase, 2*activeNative); err != nil {
			t.Fatal("arm:", err)
		}
		signalFault(t, stale, staleNative)
	case "nested":
		mapping, base, native := signalMapping(t, "nested", true)
		if err := control.Arm(15, RoleSource, base, 2*native); err != nil {
			t.Fatal("arm:", err)
		}
		mapAtomicStore32(baseOf(control.data), offHandling, 1)
		signalFault(t, mapping, native)
	case "null-info":
		matrixCallChainNullInfo()
		os.Exit(85)
	case "replacement":
		replacement := kernelSigaction{
			Handler:  matrixReplacementSiginfoAddr(),
			Flags:    sigActionSigInfo | sigActionRestore,
			Restorer: rtSigreturnStubAddr(),
		}
		if errno := rtSigactionSet(sigBus, uintptr(unsafe.Pointer(&replacement)), 0); errno != 0 {
			os.Exit(85)
		}
		if err := handler.VerifyOwned(); err == nil {
			os.Exit(85)
		}
		handler.Close()
		raiseSigbus()
		os.Exit(86)
	default:
		os.Exit(85)
	}
}

// The matrix's naked handler addresses (sigbus_matrix_v4work.s).
func matrixOneArgumentAddr() uintptr
func matrixSiginfoAddr() uintptr
func matrixMaskedSiginfoAddr() uintptr
func matrixNodeferSiginfoAddr() uintptr
func matrixResetSiginfoAddr() uintptr
func matrixReplacementSiginfoAddr() uintptr
func matrixCallChainNullInfo()
