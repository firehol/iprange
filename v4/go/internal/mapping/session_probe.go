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

// probeSlot boxes the installed probe arm for the atomic slot (the
// shipped worker process runs exactly one session, so one slot is
// enough; the atomic makes the read race-free for tests and keeps the
// hot-path probe check a single load).
type probeSlot struct {
	arm func(role ProbeRole, base uintptr, length uint64) (func(), error)
}

// sessionProbeSlot is the one installed session probe arm (Rust
// CURRENT_CONTROL): nil when no worker session is active. The worker
// binary is the single writer (once, before the domain machine runs);
// domain machines read the slot at every probe site; library
// processes never write it. The invariant is one logical session per
// process; callers must not install two sessions expecting them to
// nest.
var sessionProbeSlot atomic.Pointer[probeSlot]

// SetSessionProbe installs one worker-session probe arm (Rust
// Context::enter publishing CURRENT_CONTROL; the arm receives the
// role and the mapping region and returns the release function that
// restores the previous registration or disarms the probe, mirroring
// Rust enter_region + Probe::drop). A nil arm clears the session
// state.
func SetSessionProbe(arm func(role ProbeRole, base uintptr, length uint64) (func(), error)) {
	if arm == nil {
		sessionProbeSlot.Store(nil)
		return
	}
	sessionProbeSlot.Store(&probeSlot{arm: arm})
}

// ClearSessionProbe removes the session probe hook (Rust Context::drop
// clearing CURRENT_CONTROL): Mapping.Probe and Mapping.EnterProbe
// return to running directly.
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

// ProbeGuard is the armed region of one machine step (Rust
// enter_output guard): the release function restores the previous
// registration or disarms the probe, and the guard carries no state
// when no session is active, so machine callers can defer Exit
// unconditionally.
type ProbeGuard struct {
	release func()
}

// inertProbeGuard is the shared no-session guard (Rust enter_output
// with a null CURRENT_CONTROL): Exit is a no-op and the machine runs
// directly. One shared instance keeps the library path allocation-free.
var inertProbeGuard = &ProbeGuard{}

// Exit releases the armed region (Rust Probe::drop: restores the
// previous registration or disarms; a nil release is the no-session
// no-op).
func (g *ProbeGuard) Exit() {
	if g != nil && g.release != nil {
		g.release()
	}
}

// EnterProbe arms one mapped region for a machine step (Rust
// enter_output: the region is resolved and armed before the machine
// runs, and the caller releases the guard when the step finishes).
// Without an installed hook the guard is inert and the machine runs
// directly; with a hook, arming failures surface before the machine
// runs (Rust enter_region error propagation). Unlike Probe, the
// machine is not wrapped in a closure: Go's escape analysis promotes
// caller stack values captured by an escaping probe closure, so the
// closure-free guard is the only shape that keeps the pinned library
// publish path allocation-free (the writer/output.go gate has the
// same purpose for per-page Store ops).
func (m *Mapping) EnterProbe(role ProbeRole) (*ProbeGuard, error) {
	slot := sessionProbeSlot.Load()
	if slot == nil {
		return inertProbeGuard, nil
	}
	base, length, err := m.Region()
	if err != nil {
		return nil, err
	}
	release, err := slot.arm(role, base, length)
	if err != nil {
		return nil, err
	}
	return &ProbeGuard{release: release}, nil
}

// Probe runs one operation with the region protected by the session
// probe hook (Rust probe_region: the region is resolved and entered
// before the operation runs, and the probe restores the previous
// registration or disarms on release). Without an installed hook the
// operation runs directly; with a hook, arming failures surface
// before the operation runs (Rust enter_region error propagation).
func (m *Mapping) Probe(role ProbeRole, operation func() error) error {
	guard, err := m.EnterProbe(role)
	if err != nil {
		return err
	}
	defer guard.Exit()
	return operation()
}
