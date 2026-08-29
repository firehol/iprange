//go:build darwin && (amd64 || arm64)

// Darwin mapped-fault containment machine (Rust worker/posix.rs on
// macOS): the isolated worker pins one thread, installs a SIGBUS
// handler with SA_SIGINFO|SA_ONSTACK on the mapped alternate stack, and
// chases the exact posix.rs owned_fault/chain semantics through raw
// syscalls (sigbus_darwin_{amd64,arm64}.s). Darwin has no SA_RESTORER:
// the kernel stores the action's sa_tramp and frames every real-handler
// delivery through it (sendsig sets PC to SIGTRAMP(p, sig)), so the
// worker installs its own sigtramp that calls the catcher and resumes
// the interrupted context with sigreturn. Struct layouts and syscall
// numbers are the macOS LP64 ABI (verified against the XNU headers and
// the M4 kernel probe): sigaction(46) consumes a 24-byte __sigaction
// input (handler@0, tramp@8, mask@16, flags@20) and produces a 16-byte
// sigaction output (handler@0, mask@8, flags@12).

package worker

import (
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Signal and sa_flags constants (macOS values; posix.rs uses the libc
// names).
const (
	sigBus           = 10
	sigDFL           = 0
	sigIGN           = 1
	sigActionSigInfo = 0x40
	sigActionNoDefer = 0x10
	sigActionReset   = 0x4
	sigActionOnStack = 0x1
	sigSSDisable     = 0x4
)

// darwinSigaction mirrors the macOS sigaction struct the kernel writes
// on sigaction(46) (handler@0, mask@8 as a 32-bit sigset, flags@12; 16
// bytes). The kernel never returns the tramp, so queries use this type.
type darwinSigaction struct {
	Handler uintptr
	Mask    uint32
	Flags   int32
}

// darwinSigactionInput mirrors the macOS __sigaction struct sigaction(46)
// consumes (handler@0, tramp@8, mask@16 as a 32-bit sigset, flags@20; 24
// bytes). The kernel stores the tramp with the disposition and jumps
// every real-handler delivery through it; a zero or garbage tramp is
// fatal at delivery, so every install (including restores) must supply a
// valid sigtramp. SA_VALIDATE_SIGRETURN_FROM_SIGTRAMP (0x400) is never
// set: the tramp passes the per-delivery token through and needs no
// kernel-side sigreturn validation.
type darwinSigactionInput struct {
	Handler uintptr
	Tramp   uintptr
	Mask    uint32
	Flags   int32
}

// kernelStack mirrors the macOS stack_t struct (sp@0, size@8, flags@16;
// 24 bytes -- the BSD field order differs from Linux).
type kernelStack struct {
	SP    uintptr
	Size  uintptr
	Flags int32
}

// previousAction is the disposition captured before install and
// published before the handler can run (posix.rs PREVIOUS_ACTION). The
// naked handler chains to it.
var previousAction darwinSigaction

// Syscall shims (sigbus_darwin_amd64.s / sigbus_darwin_arm64.s). Each
// returns errno (0 = ok).
func sigaltstackSet(ss, old uintptr) int32
func sigaltstackQuery(old uintptr) int32
func sigactionSet(sig int32, act, old uintptr) int32
func sigactionQuery(sig int32, old uintptr) int32
func sigbusHandlerAddr() uintptr
func sigtrampAddr() uintptr

// Handler is the installed SIGBUS worker handler (posix.rs Handler).
// Install pins nothing by itself: the caller must have locked the current
// OS thread (runtime.LockOSThread) because the alternate signal stack is
// per-thread and Go can migrate goroutines between threads.
type Handler struct {
	control        *Control
	previousAction darwinSigaction
	previousStack  kernelStack
}

// InstallHandler installs the SIGBUS isolation handler for one control
// (posix.rs Handler::install): conflict check, alternate-stack capture and
// install, previous-action capture published before install, our action
// (SA_SIGINFO|SA_ONSTACK), ACTIVE_CONTROL CAS, then verify. Every failure
// path restores what was already changed.
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
	if errno := sigactionQuery(sigBus, uintptr(unsafe.Pointer(&h.previousAction))); errno != 0 {
		restoreStack(&h.previousStack)
		return nil, &format.Error{Code: format.CodeIO, Detail: "sigaction query: " + unix.Errno(errno).Error()}
	}
	// Publish the predecessor before our handler can run; this closes the
	// installation window where a delivery could chain through uninitialized
	// storage (posix.rs install).
	previousAction = h.previousAction

	selectedAction := darwinSigactionInput{
		Handler: sigbusHandlerAddr(),
		Tramp:   sigtrampAddr(),
		Flags:   sigActionSigInfo | sigActionOnStack,
	}
	if errno := sigactionSet(sigBus, uintptr(unsafe.Pointer(&selectedAction)), 0); errno != 0 {
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
	var current darwinSigaction
	if errno := sigactionQuery(sigBus, uintptr(unsafe.Pointer(&current))); errno != 0 {
		return &format.Error{Code: format.CodeIO, Detail: "sigaction query: " + unix.Errno(errno).Error()}
	}
	required := int32(sigActionSigInfo | sigActionOnStack)
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
		var current darwinSigaction
		if errno := sigactionQuery(sigBus, uintptr(unsafe.Pointer(&current))); errno == 0 &&
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
// restore_action; best-effort during teardown). The kernel input needs
// a tramp; the worker's own sigtramp is used so a restored handler
// still returns through the delivery token path.
func restoreAction(previous *darwinSigaction) {
	action := darwinSigactionInput{
		Handler: previous.Handler,
		Tramp:   sigtrampAddr(),
		Mask:    previous.Mask,
		Flags:   previous.Flags,
	}
	_ = sigactionSet(sigBus, uintptr(unsafe.Pointer(&action)), 0)
}

// restoreStack reinstalls a previously captured alternate stack (posix.rs
// restore_stack; best-effort during teardown).
func restoreStack(previous *kernelStack) {
	_ = sigaltstackSet(uintptr(unsafe.Pointer(previous)), 0)
}
