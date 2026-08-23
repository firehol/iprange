//go:build linux && amd64

package worker

import (
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Signal and sa_flags constants (kernel x86-64 values; posix.rs uses the
// libc names).
const (
	sigBus           = 7
	sigDFL           = 0
	sigIGN           = 1
	sigActionSigInfo = 0x4
	sigActionNoDefer = 0x40000000
	sigActionReset   = 0x80000000
	sigActionRestore = 0x04000000
	sigActionOnStack = 0x08000000
	sigSSDisable     = 0x2
)

// kernelSigaction mirrors the x86-64 kernel sigaction struct
// (handler@0, flags@8, restorer@16, mask@24; 32 bytes, sigsetsize 8).
type kernelSigaction struct {
	Handler  uintptr
	Flags    uint64
	Restorer uintptr
	Mask     uint64
}

// kernelStack mirrors the x86-64 kernel stack_t struct
// (ss_sp@0, ss_flags@8, ss_size@16; 24 bytes).
type kernelStack struct {
	SP    uintptr
	Flags int32
	Size  uintptr
}

// activeControl publishes the armed control base after the handler is
// installed (posix.rs ACTIVE_CONTROL). The naked handler loads it with a
// plain 8-byte load; on x86-64 TSO that is an acquire-equivalent read of
// the release store performed by install/close.
var activeControl uintptr

// previousAction is the kernel disposition captured before install and
// published before the handler can run (posix.rs PREVIOUS_ACTION). The
// naked handler chains to it.
var previousAction kernelSigaction

// Syscall shims (sigbus_linux_amd64.s). Each returns errno (0 = ok).
func sigaltstackSet(ss, old uintptr) int32
func sigaltstackQuery(old uintptr) int32
func rtSigactionSet(sig int32, act, old uintptr) int32
func rtSigactionQuery(sig int32, old uintptr) int32
func sigbusHandlerAddr() uintptr
func rtSigreturnStubAddr() uintptr

// Mapped-control atomics (sigbus_linux_amd64.s). The naked handler cannot
// call Go code and Go's sync/atomic is not specified for mapped memory, so
// these asm primitives are the single implementation both the Go side and
// the handler use for the fields the handler also touches (armed,
// handling, state). Aligned 32-bit loads/stores are atomic on x86-64; the
// CAS carries the LOCK prefix.
func mapAtomicLoad32(base uintptr, off uint32) uint32
func mapAtomicStore32(base uintptr, off uint32, value uint32)

// Handler is the installed SIGBUS worker handler (posix.rs Handler).
// Install pins nothing by itself: the caller must have locked the current
// OS thread (runtime.LockOSThread) because the alternate signal stack is
// per-thread and Go can migrate goroutines between threads.
type Handler struct {
	control        *Control
	previousAction kernelSigaction
	previousStack  kernelStack
}

// InstallHandler installs the SIGBUS isolation handler for one control
// (posix.rs Handler::install): conflict check, alternate-stack capture and
// install, previous-action capture published before install, our action
// (SA_SIGINFO|SA_ONSTACK|SA_RESTORER with the project rt_sigreturn stub),
// ACTIVE_CONTROL CAS, then verify. Every failure path restores what was
// already changed.
func (c *Control) InstallHandler() (*Handler, error) {
	if atomic.LoadUintptr(&activeControl) != 0 {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "SIGBUS worker handler is already installed"}
	}
	h := &Handler{control: c}

	alt, altLen := c.altStack()
	selectedStack := kernelStack{SP: alt, Size: altLen}
	if errno := sigaltstackSet(uintptr(unsafe.Pointer(&selectedStack)), uintptr(unsafe.Pointer(&h.previousStack))); errno != 0 {
		return nil, &format.Error{Code: format.CodeIO, Detail: "sigaltstack: " + unix.Errno(errno).Error()}
	}
	if errno := rtSigactionQuery(sigBus, uintptr(unsafe.Pointer(&h.previousAction))); errno != 0 {
		restoreStack(&h.previousStack)
		return nil, &format.Error{Code: format.CodeIO, Detail: "sigaction query: " + unix.Errno(errno).Error()}
	}
	// Publish the predecessor before our handler can run; this closes the
	// installation window where a delivery could chain through uninitialized
	// storage (posix.rs install).
	previousAction = h.previousAction

	selectedAction := kernelSigaction{
		Handler:  sigbusHandlerAddr(),
		Flags:    sigActionSigInfo | sigActionOnStack | sigActionRestore,
		Restorer: rtSigreturnStubAddr(),
	}
	if errno := rtSigactionSet(sigBus, uintptr(unsafe.Pointer(&selectedAction)), 0); errno != 0 {
		restoreStack(&h.previousStack)
		return nil, &format.Error{Code: format.CodeIO, Detail: "sigaction install: " + unix.Errno(errno).Error()}
	}
	if !atomic.CompareAndSwapUintptr(&activeControl, 0, c.base()) {
		restoreAction(&h.previousAction)
		restoreStack(&h.previousStack)
		return nil, &format.Error{Code: format.CodeConflict, Detail: "SIGBUS worker handler is already installed"}
	}
	if err := h.verifyOwned(); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// VerifyOwned proves the handler, the alternate stack, and ACTIVE_CONTROL
// still describe our installation (posix.rs verify_owned).
func (h *Handler) VerifyOwned() error {
	return h.verifyOwned()
}

func (h *Handler) verifyOwned() error {
	var current kernelSigaction
	if errno := rtSigactionQuery(sigBus, uintptr(unsafe.Pointer(&current))); errno != 0 {
		return &format.Error{Code: format.CodeIO, Detail: "sigaction query: " + unix.Errno(errno).Error()}
	}
	required := uint64(sigActionSigInfo | sigActionOnStack)
	if current.Handler != sigbusHandlerAddr() ||
		current.Flags&required != required ||
		current.Flags&(sigActionNoDefer|sigActionReset) != 0 {
		return &format.Error{Code: format.CodeConflict, Detail: "SIGBUS worker handler ownership was lost"}
	}
	var currentStack kernelStack
	if errno := sigaltstackQuery(uintptr(unsafe.Pointer(&currentStack))); errno != 0 {
		return &format.Error{Code: format.CodeIO, Detail: "sigaltstack query: " + unix.Errno(errno).Error()}
	}
	alt, altLen := h.control.altStack()
	if currentStack.Flags&sigSSDisable != 0 ||
		currentStack.SP != alt ||
		currentStack.Size != altLen ||
		atomic.LoadUintptr(&activeControl) != h.control.base() {
		return &format.Error{Code: format.CodeConflict, Detail: "SIGBUS worker handler ownership was lost"}
	}
	return nil
}

// Close disarms the probe and tears the installation down (posix.rs
// Handler::drop): ACTIVE_CONTROL CAS, restore the previous action only if
// the current action is still ours, restore the previous alternate stack
// only if the current one is still ours.
func (h *Handler) Close() {
	h.control.Disarm()
	if atomic.CompareAndSwapUintptr(&activeControl, h.control.base(), 0) {
		var current kernelSigaction
		if errno := rtSigactionQuery(sigBus, uintptr(unsafe.Pointer(&current))); errno == 0 &&
			current.Handler == sigbusHandlerAddr() {
			restoreAction(&h.previousAction)
		}
	}
	var currentStack kernelStack
	if errno := sigaltstackQuery(uintptr(unsafe.Pointer(&currentStack))); errno == 0 {
		alt, altLen := h.control.altStack()
		if currentStack.Flags&sigSSDisable == 0 &&
			currentStack.SP == alt &&
			currentStack.Size == altLen {
			restoreStack(&h.previousStack)
		}
	}
}

// restoreAction reinstalls a previously captured disposition (posix.rs
// restore_action; best-effort during teardown).
func restoreAction(previous *kernelSigaction) {
	_ = rtSigactionSet(sigBus, uintptr(unsafe.Pointer(previous)), 0)
}

// restoreStack reinstalls a previously captured alternate stack (posix.rs
// restore_stack; best-effort during teardown).
func restoreStack(previous *kernelStack) {
	_ = sigaltstackSet(uintptr(unsafe.Pointer(previous)), 0)
}
