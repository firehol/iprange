// Package worker owns the isolated mapped-fault worker coordination: the
// 1 MiB control mapping (Rust worker/control.rs fault subset) and the
// SIGBUS isolation handler (Rust worker/posix.rs). The control file is a
// mapped coordination artifact exactly like the live sidecar: no read or
// write syscalls, only mapping views. The future cmd/iprange-v4-worker
// binary and the per-platform worker proofs consume this package.
package worker

const (
	// controlLen is the complete fixed control-file extent (Rust
	// worker/control.rs CONTROL_LEN).
	controlLen = 1024 * 1024
	// altStackLen is the alternate-signal-stack extent carved from the
	// tail of the control mapping (Rust ALT_STACK_LEN).
	altStackLen = 64 * 1024
	// ownedFaultExit is the worker exit code after an owned fault record
	// was written (Rust OWNED_FAULT_EXIT).
	ownedFaultExit = 197
	// protocol is the control-page protocol version (Rust PROTOCOL).
	protocol = 1
	// stateRequest / stateFault are the control states the fault subset
	// uses (Rust State::Request / State::Fault).
	stateRequest = 1
	stateFault   = 8
	// faultMarker seals a complete fault record (Rust FAULT_MARKER).
	faultMarker = 0x42555346
)

// Control-page offsets (Rust worker/control.rs; fault subset only).
const (
	offMagic         = 0
	offProtocol      = 8
	offState         = 12
	offGeneration    = 104
	offRole          = 112
	offArmed         = 116
	offHandling      = 120
	offBase          = 128
	offLen           = 136
	offFaultGen      = 144
	offFaultRole     = 152
	offFaultCode     = 156
	offFaultRelative = 160
	offFaultAddress  = 168
	offFaultMarker   = 176
)

// controlMagic is the control-page magic (Rust MAGIC "IPR4WRK\x00").
var controlMagic = [8]byte{'I', 'P', 'R', '4', 'W', 'R', 'K', 0}

// MappingRole identifies the mapped region an armed probe protects (Rust
// worker/control.rs MappingRole).
type MappingRole uint32

const (
	RoleSource       MappingRole = 1
	RoleScratch      MappingRole = 2
	RoleOutput       MappingRole = 3
	RoleCoordination MappingRole = 4
)

func roleFromWire(value uint32) (MappingRole, bool) {
	switch MappingRole(value) {
	case RoleSource, RoleScratch, RoleOutput, RoleCoordination:
		return MappingRole(value), true
	}
	return 0, false
}

// FaultRecord is the validated record an owned fault left in the control
// mapping (Rust worker/control.rs FaultRecord).
type FaultRecord struct {
	Role       MappingRole
	Relative   uint64
	MappingLen uint64
}
