package mapping

// Worker-session mapping probe hook (Rust worker.rs CURRENT_CONTROL
// thread-local and probe_region/enter_region): the hook arms the
// worker's control protection around one domain-machine region before
// the operation runs and restores or disarms after it. The state
// lives in this leaf package because internal/worker imports
// validation and recovery, so those domain machines cannot import
// worker; the hook must still reach every mapping they create. The
// worker session (internal/worker EnterSession) is the only writer,
// and the parity-point readers are the domain machines (validation
// sweeps, recovery source guards, output writes, sidecar ops). In
// normal SDK use no hook is installed and Mapping.Probe runs the
// operation directly: library processes observe zero behavior
// change.

import (
	"sync/atomic"
)

// ProbeRole identifies the mapped region an armed session probe
// protects (Rust worker/control.rs MappingRole; the numeric values
// are the control-page wire constants of the role field).
type ProbeRole uint32

const (
	RoleSource       ProbeRole = 1
	RoleScratch      ProbeRole = 2
	RoleOutput       ProbeRole = 3
	RoleCoordination ProbeRole = 4
)

// probeSlot boxes the installed probe hook for the atomic slot (the
// shipped worker process runs exactly one session, so one slot is
// enough; the atomic makes the read race-free for tests and keeps the
// hot-path probe check a single load).
type probeSlot struct {
	hook func(role ProbeRole, base uintptr, length uint64, operation func() error) error
}

// sessionProbeSlot is the one installed session probe hook (Rust
// CURRENT_CONTROL): nil when no worker session is active. The worker
// binary is the single writer (once, before the domain machine runs);
// domain machines read the slot at every probe site; library
// processes never write it. The invariant is one logical session per
// process; callers must not install two sessions expecting them to
// nest.
var sessionProbeSlot atomic.Pointer[probeSlot]

// SetSessionProbe installs one worker-session probe hook (Rust
// Context::enter publishing CURRENT_CONTROL; the hook receives the
// role, the mapping region, and the operation to run inside the armed
// probe). A nil hook clears the session state.
func SetSessionProbe(hook func(role ProbeRole, base uintptr, length uint64, operation func() error) error) {
	if hook == nil {
		sessionProbeSlot.Store(nil)
		return
	}
	sessionProbeSlot.Store(&probeSlot{hook: hook})
}

// ClearSessionProbe removes the session probe hook (Rust Context::drop
// clearing CURRENT_CONTROL): Mapping.Probe returns to running its
// operation directly.
func ClearSessionProbe() {
	sessionProbeSlot.Store(nil)
}

// SessionProbeActive reports whether a worker-session probe hook is
// installed (the hot-path gate the domain machines use to avoid
// building probe closures when no session is active; the Rust parity
// branch is CURRENT_CONTROL.is_null()).
func SessionProbeActive() bool {
	return sessionProbeSlot.Load() != nil
}

// Probe runs one operation with the region protected by the session
// probe hook (Rust probe_region: the region is resolved and entered
// before the operation runs, and the probe restores the previous
// registration or disarms on release). Without an installed hook the
// operation runs directly; with a hook, arming failures surface
// before the operation runs (Rust enter_region error propagation).
func (m *Mapping) Probe(role ProbeRole, operation func() error) error {
	slot := sessionProbeSlot.Load()
	if slot == nil {
		return operation()
	}
	base, length, err := m.Region()
	if err != nil {
		return err
	}
	return slot.hook(role, base, length, operation)
}
