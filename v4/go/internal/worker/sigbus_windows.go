//go:build windows && (amd64 || arm64)

// Windows mapped-fault containment machine (Rust worker/windows.rs
// parity): the isolated worker registers one first-priority vectored
// exception handler that claims only EXCEPTION_IN_PAGE_ERROR with the
// documented accessed-address parameter inside the armed mapping and
// all ownership checks agreeing, writes the exact fault facts to the
// mapped control record, and terminates the worker with the owned-fault
// exit code. Every other exception returns EXCEPTION_CONTINUE_SEARCH.
// The handler body is deliberately allocation-free: plain mapped
// accesses plus the release/acquire primitives and the two terminal
// kernel32 syscalls (GetCurrentProcess, TerminateProcess) through
// pre-resolved raw addresses.

package worker

import (
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Windows exception constants and the EXCEPTION_POINTERS /
// EXCEPTION_RECORD layouts (winnt.h): the pointers structure holds
// the record pointer at offset 0 and the context pointer at offset 8;
// the record holds Code@0, Flags@4, Record@8, Address@16,
// NumberParameters@24, and ExceptionInformation@32 (array of 15
// uintptr-sized parameters). Parameter 1 is the accessed address and
// parameter 2 the NTSTATUS of EXCEPTION_IN_PAGE_ERROR.
const (
	exceptionInPageError    = 0xC0000006
	exceptionContinueSearch = 0
)

// exceptionPointers mirrors EXCEPTION_POINTERS (64-bit layout).
type exceptionPointers struct {
	Record  unsafe.Pointer // PEXCEPTION_RECORD
	Context unsafe.Pointer
}

// exceptionRecord mirrors EXCEPTION_RECORD (64-bit layout).
type exceptionRecord struct {
	Code      uint32
	Flags     uint32
	Record    uintptr
	Address   uintptr
	NumParams uint32
	_         uint32
	Info      [15]uintptr
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procAddVectoredHandler   = kernel32.NewProc("AddVectoredExceptionHandler")
	procRemoveVectoredHandle = kernel32.NewProc("RemoveVectoredExceptionHandler")

	// getCurrentProcessAddr and terminateProcessAddr are the raw
	// GetCurrentProcess / TerminateProcess addresses, resolved by
	// InstallHandler in ordinary context. The exception callback calls
	// them through SyscallN only: LazyProc.Call can still take the
	// per-proc mutex, allocate, and call GetProcAddress on its first
	// use, which must never happen inside a vectored handler (Rust
	// windows.rs uses import-table addresses compiled in).
	getCurrentProcessAddr uintptr
	terminateProcessAddr  uintptr
)

// resolveHandlerProcs resolves the two kernel32 addresses the
// exception callback needs, in ordinary (non-fault) context.
func resolveHandlerProcs() error {
	k32, err := windows.LoadDLL("kernel32.dll")
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "LoadDLL(kernel32): " + err.Error()}
	}
	getCurrentProcess, err := k32.FindProc("GetCurrentProcess")
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "GetCurrentProcess: " + err.Error()}
	}
	terminateProcess, err := k32.FindProc("TerminateProcess")
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "TerminateProcess: " + err.Error()}
	}
	getCurrentProcessAddr = getCurrentProcess.Addr()
	terminateProcessAddr = terminateProcess.Addr()
	return nil
}

// activeData is the byte view of the armed control mapping published
// next to activeControl (the raw-base uintptr the session probe
// reads); the handler derives its base from this slice so every
// pointer derivation stays vet-clean and allocation-free.
var activeData []byte

// Handler is the installed EXCEPTION_IN_PAGE_ERROR worker handler
// (Rust windows.rs Handler). The callback and the registration handle
// stay referenced for the whole session.
type Handler struct {
	control  *Control
	callback uintptr
	handle   uintptr
}

// InstallHandler installs the vectored fault handler for one control
// (Rust windows.rs Handler::install): a first-priority registration is
// taken, ACTIVE_CONTROL is published, and the ownership is verified.
// Every failure path removes the registration it took.
func (c *Control) InstallHandler() (*Handler, error) {
	if err := resolveHandlerProcs(); err != nil {
		return nil, err
	}
	if atomic.LoadUintptr(&activeControl) != 0 {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "mapped-fault worker handler is already installed"}
	}
	callback := windows.NewCallback(exceptionHandler)
	handle, _, err := procAddVectoredHandler.Call(1, callback)
	if handle == 0 {
		return nil, &format.Error{Code: format.CodeIO, Detail: "AddVectoredExceptionHandler: " + err.Error()}
	}
	if !atomic.CompareAndSwapUintptr(&activeControl, 0, c.base()) {
		procRemoveVectoredHandle.Call(handle)
		return nil, &format.Error{Code: format.CodeConflict, Detail: "mapped-fault worker handler is already installed"}
	}
	h := &Handler{control: c, callback: callback, handle: handle}
	activeData = c.data
	if err := h.verifyOwned(); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// VerifyOwned proves the registration and ACTIVE_CONTROL still
// describe our handler (Rust windows.rs verify_owned).
func (h *Handler) VerifyOwned() error { return h.verifyOwned() }

func (h *Handler) verifyOwned() error {
	if atomic.LoadUintptr(&activeControl) != h.control.base() {
		return &format.Error{Code: format.CodeConflict, Detail: "mapped-fault worker handler ownership was lost"}
	}
	return nil
}

// Close disarms and removes the registration (Rust windows.rs Drop).
func (h *Handler) Close() {
	h.control.Disarm()
	activeData = nil
	if atomic.CompareAndSwapUintptr(&activeControl, h.control.base(), 0) {
		procRemoveVectoredHandle.Call(h.handle)
	}
}

// exceptionHandler is the vectored callback (Rust windows.rs
// exception_handler): it claims an in-page error whose accessed
// address lies in exactly one armed mapping with a nonzero generation
// and valid role, writes the fault record, publishes Fault, and
// terminates only this worker. The callback parameter is an
// unsafe.Pointer (the runtime callback marshal supports pointer
// arguments), so every access is a plain pointer conversion and
// dereference; the body never allocates and never touches Go runtime
// state.
func exceptionHandler(pointers unsafe.Pointer) uintptr {
	data := activeData
	if len(data) == 0 || pointers == nil {
		return exceptionContinueSearch
	}
	control := baseOf(data)

	ep := (*exceptionPointers)(pointers)
	rec := (*exceptionRecord)(ep.Record)
	if rec == nil || rec.Code != exceptionInPageError {
		return exceptionContinueSearch
	}
	if rec.NumParams < 3 {
		return exceptionContinueSearch
	}
	address := uint64(rec.Info[1])
	ntstatus := int32(rec.Info[2])

	if mapAtomicLoad32(control, offArmed) != 1 {
		return exceptionContinueSearch
	}
	generation := format.U64(data[offGeneration : offGeneration+8])
	role := format.U32(data[offRole : offRole+4])
	if generation == 0 || role < 1 || role > 4 {
		return exceptionContinueSearch
	}
	base := format.U64(data[offBase : offBase+8])
	length := format.U64(data[offLen : offLen+8])
	if length == 0 || address < base || address-base >= length {
		return exceptionContinueSearch
	}
	relative := address - base

	if mapAtomicCas32(control, offHandling, 0, 1) != 1 {
		return exceptionContinueSearch
	}
	format.PutU64(data[offFaultGen:offFaultGen+8], generation)
	format.PutU32(data[offFaultRole:offFaultRole+4], role)
	format.PutU32(data[offFaultCode:offFaultCode+4], uint32(ntstatus))
	format.PutU64(data[offFaultRelative:offFaultRelative+8], relative)
	format.PutU64(data[offFaultAddress:offFaultAddress+8], address)
	format.PutU32(data[offFaultMarker:offFaultMarker+4], faultMarker)
	mapAtomicStore32(control, offState, stateFault)

	current, _, _ := syscall.SyscallN(getCurrentProcessAddr)
	syscall.SyscallN(terminateProcessAddr, current, ownedFaultExit)
	return exceptionContinueSearch // unreachable
}
