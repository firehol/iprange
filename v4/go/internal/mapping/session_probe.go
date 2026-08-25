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
//
// The armed region is a value, not a closure (Rust Probe is a stack
// value): ProbeRelease carries one interface word over the
// already-heap worker control plus the previous registration state,
// so entering, arming, and releasing a probe never allocate, in
// library mode or inside a session.

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
	arm func(role ProbeRole, base uintptr, length uint64) (ProbeRelease, error)
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
// role and the mapping region and returns the release that restores
// the previous registration or disarms the probe, mirroring Rust
// enter_region + Probe::drop). A nil arm clears the session state.
func SetSessionProbe(arm func(role ProbeRole, base uintptr, length uint64) (ProbeRelease, error)) {
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
// installed (Rust CURRENT_CONTROL.is_null() parity).
func SessionProbeActive() bool {
	return sessionProbeSlot.Load() != nil
}

// ProbeRegistration is the previous armed-registration state captured
// before one probe (the projection of the worker control's
// MappingRegistration onto this leaf package; the release re-arms
// exactly this state). The value has no heap identity.
type ProbeRegistration struct {
	Generation uint64
	Role       uint32
	Base       uintptr
	Length     uint64
}

// ProbeOwner restores the armed registration captured before one
// probe (Rust Probe::drop arm/disarm); the worker control implements
// it. The interface word is the only indirection: the owner is
// already heap, so the interface value never allocates.
type ProbeOwner interface {
	RestoreProbe(previous ProbeRegistration, armed bool)
}

// ProbeRelease releases one armed probe region (Rust Probe: a stack
// value whose drop restores the previous registration or disarms).
// The zero value is the inert no-session release (a nil owner), so
// machine steps can defer Release unconditionally. Building and
// releasing a probe allocate nothing: the owner is one interface word
// over the already-heap control and the previous registration is a
// plain value.
type ProbeRelease struct {
	Owner    ProbeOwner
	Previous ProbeRegistration
	Armed    bool
}

// Release restores the armed region (Rust Probe::drop): the owner
// re-arms the previous registration or disarms when the probe was
// armed, and a nil owner is the no-session no-op.
func (r ProbeRelease) Release() {
	if r.Owner == nil {
		return
	}
	r.Owner.RestoreProbe(r.Previous, r.Armed)
}

// ProbeGuard is the armed region of one machine step (Rust
// enter_output guard): the release restores the previous registration
// or disarms the probe, and the zero guard is the no-session no-op,
// so machine callers can defer Exit unconditionally.
type ProbeGuard struct {
	release ProbeRelease
}

// Exit releases the armed region (Rust Probe::drop): a zero guard is
// the no-session no-op.
func (g ProbeGuard) Exit() {
	g.release.Release()
}

// EnterProbe arms one mapped region for a machine step (Rust
// enter_output: the region is resolved and armed before the machine
// runs, and the caller releases the guard when the step finishes).
// The region resolves before the session check (Rust probe_mapping
// order), so an unmapped mapping refuses even in library mode; with a
// hook installed, arming failures surface before the machine runs
// (Rust enter_region error propagation). The guard is a value: Go's
// escape analysis would promote caller stack values captured by a
// probe closure, so the closure-free, allocation-free shape keeps the
// pinned library and session-active publish paths at zero.
func (m *Mapping) EnterProbe(role ProbeRole) (ProbeGuard, error) {
	base, length, err := m.Region()
	if err != nil {
		return ProbeGuard{}, err
	}
	slot := sessionProbeSlot.Load()
	if slot == nil {
		return ProbeGuard{}, nil
	}
	release, err := slot.arm(role, base, length)
	if err != nil {
		return ProbeGuard{}, err
	}
	return ProbeGuard{release: release}, nil
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
