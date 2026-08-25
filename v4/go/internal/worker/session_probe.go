//go:build linux && amd64

package worker

// Worker-side mapping probe arm (Rust worker.rs enter_region, Probe,
// NEXT_MAPPING_GENERATION, and Context): Control.ProbeRegion resolves
// the previous armed registration, arms one region with a fresh
// monotonic nonzero generation, runs the operation, and restores the
// previous registration or disarms on release. EnterSession publishes
// the control's ProbeRegion as the mapping session probe hook (Rust
// Context::enter); the cmd worker binary is the only production
// caller, so library processes never install the hook and
// Mapping.Probe keeps running operations directly.

import (
	"sync"
	"sync/atomic"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// Session state mirrors the Rust thread-locals (worker.rs:64): the
// mapping probe hook of one process-wide session. The shipped worker
// runs exactly one session per process, so one slot is enough; the
// mutex makes the state race-free for tests.
var (
	sessionMu      sync.Mutex
	sessionControl *Control
)

// mappingGeneration is the process-wide mapping-probe generation
// counter (Rust NEXT_MAPPING_GENERATION thread-local: generations
// 1,2,3,... and never 0; a checked add wraps to 1 on overflow so the
// invariant survives u64 exhaustion). The fault record cross-check
// requires the armed generation to equal the fault generation, so the
// counter is shared by every probe of one session.
var (
	mappingGenerationMu sync.Mutex
	mappingGeneration   uint64 = 1
)

// nextMappingGeneration returns the next nonzero generation (Rust
// NEXT_MAPPING_GENERATION get-then-checked-add: the returned value is
// the current counter and the counter advances to value+1, wrapping
// to 1 on overflow).
func nextMappingGeneration() uint64 {
	mappingGenerationMu.Lock()
	defer mappingGenerationMu.Unlock()
	generation := mappingGeneration
	if mappingGeneration == ^uint64(0) {
		mappingGeneration = 1
	} else {
		mappingGeneration++
	}
	return generation
}

// registration reads the armed mapping registration of the control
// (Rust Control::registration): the unarmed state returns false, and
// an armed state with an invalid generation, role, base, or length is
// the Conflict class with the verbatim Rust detail.
func (c *Control) registration() (MappingRegistration, bool, error) {
	base := baseOf(c.data)
	fail := func(detail string) (MappingRegistration, bool, error) {
		return MappingRegistration{}, false, &format.Error{Code: format.CodeConflict, Detail: detail}
	}
	if mapAtomicLoad32(base, offArmed) == 0 {
		return MappingRegistration{}, false, nil
	}
	generation := format.U64(c.data[offGeneration : offGeneration+8])
	role, roleOK := roleFromWire(format.U32(c.data[offRole : offRole+4]))
	mappingBase := format.U64(c.data[offBase : offBase+8])
	length := format.U64(c.data[offLen : offLen+8])
	// Rust checks len > usize::MAX as u64, which is never true on this
	// amd64 target; the remaining checks mirror registration exactly.
	if generation == 0 || !roleOK || mappingBase == 0 || length == 0 {
		return fail("worker mapping registration is invalid")
	}
	return MappingRegistration{Generation: generation, Role: role, Base: uintptr(mappingBase), Len: length}, true, nil
}

// ProbeRegion runs one operation with the region armed on this control
// (Rust enter_region + Probe drop): the handler ownership is verified
// first, the previous registration is captured, the region is armed
// with a fresh nonzero generation, the operation runs, and the
// previous registration is restored (or the probe is disarmed) on
// release. Restore failures are swallowed exactly like the Rust
// `let _ = control.arm(...)` in Probe::drop, and the restore still
// runs after a failed operation.
func (c *Control) ProbeRegion(role mapping.ProbeRole, base uintptr, length uint64, operation func() error) error {
	// Rust posix.rs verify_owned gate: only an owned handler may arm a
	// probe; the Go seam is the process-global the naked handler
	// reads (sigbus_linux_amd64.go activeControl).
	if atomic.LoadUintptr(&activeControl) != c.base() {
		return &format.Error{Code: format.CodeConflict, Detail: "SIGBUS worker handler ownership was lost"}
	}
	previous, armed, err := c.registration()
	if err != nil {
		return err
	}
	generation := nextMappingGeneration()
	if err := c.Arm(generation, MappingRole(role), base, length); err != nil {
		return err
	}
	operationErr := operation()
	if armed {
		// Mirror Rust Probe::drop exactly: a failed operation still
		// restores the previous registration, and a restore failure is
		// swallowed like the Rust `let _` arm.
		_ = c.Arm(previous.Generation, previous.Role, previous.Base, previous.Len)
	} else {
		c.Disarm()
	}
	return operationErr
}

// EnterSession activates the worker mapping-probe session for one
// control (Rust worker.rs Context::enter: extends the handler-install
// lifetime across the mode run): the control's ProbeRegion becomes the
// mapping session probe hook, so every domain-machine probe arms its
// region on this control. Only one session may be active; a second
// enter is the Conflict class with the verbatim Rust detail.
func EnterSession(control *Control) error {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if sessionControl != nil {
		return &format.Error{Code: format.CodeConflict, Detail: "worker context is already active"}
	}
	sessionControl = control
	mapping.SetSessionProbe(func(role mapping.ProbeRole, base uintptr, length uint64, operation func() error) error {
		return control.ProbeRegion(role, base, length, operation)
	})
	return nil
}

// LeaveSession clears the session probe hook (Rust Context::drop clears
// CURRENT_CONTROL): Mapping.Probe returns to running its operation
// directly.
func LeaveSession() {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	sessionControl = nil
	mapping.ClearSessionProbe()
}
