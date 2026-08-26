// Package worker owns the isolated mapped-fault worker coordination: the
// 1 MiB control mapping (Rust worker/control.rs) and the SIGBUS isolation
// handler (Rust worker/posix.rs). The control file is a mapped
// coordination artifact exactly like the live sidecar: no read or write
// syscalls, only mapping views. The future cmd/iprange-v4-worker binary
// and the per-platform worker proofs consume this package.
package worker

import (
	"time"

	"github.com/firehol/iprange/v4/go/internal/publication"
)

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
	// waitLimit bounds every state spin of the control surface and the
	// version handshake (Rust WAIT_LIMIT); pollInterval is the 1 ms
	// sleep between spins (Rust request_external_poll / wait_for).
	waitLimit    = 30 * time.Second
	pollInterval = time.Millisecond
)

// Control states (Rust worker/control.rs State). The values are the wire
// constants of the control page.
const (
	stateRequest        = 1
	stateWorkerReady    = 2
	stateRunning        = 3
	stateCancelPoll     = 4
	stateFinding        = 5
	stateUnknown        = 6
	stateComplete       = 7
	stateFault          = 8
	stateFailed         = 9
	stateCleanupRequest = 10
	stateCleanupResult  = 11
)

// StateRequest..StateCleanupResult are the exported session state
// words of the worker control (Rust control.rs State). The cmd worker
// binary drives the session states through SetState and State; the
// worker-internal code keeps using the short private names.
const (
	StateRequest        = stateRequest
	StateWorkerReady    = stateWorkerReady
	StateRunning        = stateRunning
	StateCancelPoll     = stateCancelPoll
	StateFinding        = stateFinding
	StateUnknown        = stateUnknown
	StateComplete       = stateComplete
	StateFault          = stateFault
	StateFailed         = stateFailed
	StateCleanupRequest = stateCleanupRequest
	StateCleanupResult  = stateCleanupResult
)

// faultMarker seals a complete fault record (Rust FAULT_MARKER).
const faultMarker = 0x42555346

// Opcode selects the operation of one worker session (Rust
// worker/control.rs Opcode).
type Opcode uint32

const (
	OpcodeInspectRecoveryCandidates Opcode = 1
	OpcodeValidate                  Opcode = 2
	OpcodeRecover                   Opcode = 3
	OpcodeCleanupRecoveryAttempt    Opcode = 4
)

// CallbackCheckpoint identifies the sealed callback payload of one
// interrupted session (Rust worker/control.rs CallbackCheckpoint).
type CallbackCheckpoint uint32

const (
	CallbackRecoveryReport     CallbackCheckpoint = 1
	CallbackValidationProgress CallbackCheckpoint = 2
)

// callbackCheckpointFromWire maps one callback-checkpoint wire value
// (Rust CallbackCheckpoint::from_wire).
func callbackCheckpointFromWire(value uint32) (CallbackCheckpoint, bool) {
	switch CallbackCheckpoint(value) {
	case CallbackRecoveryReport, CallbackValidationProgress:
		return CallbackCheckpoint(value), true
	}
	return 0, false
}

// Control-page offsets (Rust worker/control.rs; the fault subset plus
// the wire-era control surface).
const (
	offMagic           = 0
	offProtocol        = 8
	offState           = 12
	offBuildID         = 16
	offNonce           = 80
	offParentPID       = 96
	offWorkerPID       = 100
	offGeneration      = 104
	offRole            = 112
	offArmed           = 116
	offHandling        = 120
	offBase            = 128
	offLen             = 136
	offFaultGen        = 144
	offFaultRole       = 152
	offFaultCode       = 156
	offFaultRelative   = 160
	offFaultAddress    = 168
	offFaultMarker     = 176
	offOpcode          = 180
	offPayloadLen      = 184
	offResponse        = 188
	offCancelled       = 192
	offExternalPoll    = 196
	offGuardPending    = 200
	offScratchActive   = 204
	offScratchCount    = 208
	offRecoveryCheck   = 212
	offScratchAttempt  = 216
	offScratchDirKind  = 232
	offScratchDirID    = 240
	offScratchSecKind  = 272
	offScratchSec      = 280
	offScratchEntry    = 320
	offCallbackCheck   = 400
	offCallbackPayLen  = 404
	offCallbackPayload = 512
	offPayload         = 4096
)

// Fixed control-page sizes (Rust worker/control.rs).
const (
	buildLen             = 64
	nonceLen             = 16
	scratchEntryLen      = 40
	scratchEntryCapacity = 2
	// callbackPayCapacity is the callback payload region of every
	// session (Rust CALLBACK_PAYLOAD_CAPACITY = PAYLOAD_AT -
	// CALLBACK_PAYLOAD_AT).
	callbackPayCapacity = offPayload - offCallbackPayload
	// payloadCapacity is the session payload region (Rust
	// PAYLOAD_CAPACITY = CONTROL_LEN - ALT_STACK_LEN - PAYLOAD_AT).
	payloadCapacity = controlLen - altStackLen - offPayload
)

// controlMagic is the control-page magic (Rust MAGIC "IPR4WRK\x00").
var controlMagic = [8]byte{'I', 'P', 'R', '4', 'W', 'R', 'K', 0}

// buildID is the worker build identity written at offBuildID and
// compared by VerifyRequest (Rust env!("IPRANGE_V4_BUILD_ID"), constant
// per build). The worker binary resolves its identity through
// SetBuildID before its first control access: the environment value
// when IPRANGE_V4_BUILD_ID is set, otherwise the fixed buildIDDefault;
// a pinned Go build can replace buildIDDefault with -ldflags -X. A
// test pins the exact 64-byte length (Rust verify_request rejects any
// other).
var buildID = buildIDDefault

const buildIDDefault = "iprange-v4-go-worker-0000000000000000000000000000000000000000000"

// BuildIDDefault is the fixed 64-byte build identity of this tree
// (Rust build.rs digest analog): the value used when
// IPRANGE_V4_BUILD_ID is unset. The cmd binary resolves its identity
// from BuildIDDefault or the environment and pins it with SetBuildID.
const BuildIDDefault = buildIDDefault

// identityKind and creationSecurityKind are the unix namespace kinds of
// the retained-identity and creator-only commitment checks (Rust
// publication/namespace/unix.rs IDENTITY_KIND and CREATION_SECURITY_KIND;
// the Go publication package keeps both private, so the worker boundary
// repeats the unix values). The worker control surface is unix-only,
// so no Windows variant exists here.
const (
	identityKind         uint16 = 1
	creationSecurityKind uint16 = 1
)

// scratch artifact-name grammar (Rust artifact_name.rs scratch_name):
// ".iprange-scratch-" + 32 hex attempt chars + "-" + 8 hex ordinal
// chars + ".tmp" = 62 bytes total (SCRATCH_NAME_SIZE). checkpointBasename
// builds the exact bytes of one entry name.
const (
	scratchPrefix     = ".iprange-scratch-"
	scratchSuffix     = ".tmp"
	scratchNameLength = 62
)

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

// MappingRegistration is the complete armed-probe registration (Rust
// worker/control.rs MappingRegistration).
type MappingRegistration struct {
	Generation uint64
	Role       MappingRole
	Base       uintptr
	Len        uint64
}

// ScratchCheckpointEntry is one exact scratch artifact of an active
// scratch checkpoint (Rust worker/control.rs ScratchCheckpointEntry).
type ScratchCheckpointEntry struct {
	Ordinal  uint32
	Identity publication.LocalFileIdentity
}

// ScratchCheckpoint is the active authorized-scratch checkpoint (Rust
// worker/control.rs ScratchCheckpoint).
type ScratchCheckpoint struct {
	AttemptID         [16]byte
	DirectoryIdentity publication.LocalFileIdentity
	CreationSecurity  publication.CreationSecurity
	Entries           []ScratchCheckpointEntry
}

// checkpointBasename builds the exact 62-byte scratch artifact name of
// one checkpoint entry (Rust recovery::checkpoint_basename over
// artifact_name::scratch_name): the ".iprange-scratch-" prefix, the
// lowercase hex attempt, "-", 8 lowercase hex ordinal digits, ".tmp".
func checkpointBasename(attemptID [16]byte, ordinal uint32) []byte {
	hexDigits := "0123456789abcdef"
	out := make([]byte, 0, scratchNameLength)
	out = append(out, scratchPrefix...)
	for _, b := range attemptID {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	out = append(out, '-')
	for shift := 28; shift >= 0; shift -= 4 {
		out = append(out, hexDigits[(ordinal>>shift)&0x0f])
	}
	out = append(out, scratchSuffix...)
	return out
}

// scratchIdentityValid reports whether one portable identity passes the
// retained-directory check of the control and cleanup codecs (Rust
// namespace::identity_from_local: the unix kind and a decodable
// device+inode pair). The publication package owns the exact decode; the
// worker boundary composes its exported check.
func scratchIdentityValid(identity publication.LocalFileIdentity) bool {
	_, _, ok := identity.DeviceInode()
	return ok
}

// scratchSecurityValid reports whether one creator-only commitment is a
// plausible unix commitment (Rust wire_cleanup.rs::valid_security: the
// unix kind and a nonzero commitment).
func scratchSecurityValid(kind uint16, commitment [32]byte) bool {
	return kind == creationSecurityKind && commitment != [32]byte{}
}
