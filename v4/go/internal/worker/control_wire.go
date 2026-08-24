//go:build linux && amd64

// Full worker control-page surface (Rust worker/control.rs, wire-era
// methods): the header identity, the session fields (pid, opcode, poll
// and cancellation, guard, response), the payload and callback buffers,
// the scratch and recovery checkpoints, the armed-probe registration,
// and the state spins. The fault-subset accessors stay in control.go;
// everything here is the coordination contract of the worker process
// boundary. Plain fields use the mapped-slice byte codecs (x86-64 TSO
// orders them like the Rust volatile accesses), and the cross-process
// flags use the asm map atomic primitives exactly like the fault
// subset.

package worker

import (
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"golang.org/x/sys/unix"
)

// VerifyRequest proves the header of a freshly opened worker control
// (Rust Control::verify_request): magic, protocol, the exact 64-byte
// build identity, the Request state, and a parent pid.
func (c *Control) VerifyRequest() error {
	if string(c.data[offMagic:offMagic+8]) != string(controlMagic[:]) ||
		format.U32(c.data[offProtocol:offProtocol+4]) != protocol ||
		len(buildID) != buildLen ||
		string(c.data[offBuildID:offBuildID+buildLen]) != buildID ||
		c.state() != stateRequest ||
		format.U32(c.data[offParentPID:offParentPID+4]) == 0 {
		return &format.Error{Code: format.CodeConflict, Detail: "worker protocol does not match the SDK"}
	}
	return nil
}

// SetState publishes the control state word (Rust Control::set_state,
// Release).
func (c *Control) SetState(state uint32) {
	mapAtomicStore32(baseOf(c.data), offState, state)
}

// SetWorkerPID records the worker process id (Rust
// Control::set_worker_pid).
func (c *Control) SetWorkerPID(pid uint32) {
	format.PutU32(c.data[offWorkerPID:offWorkerPID+4], pid)
}

// WorkerPID returns the recorded worker process id (Rust
// Control::worker_pid).
func (c *Control) WorkerPID() uint32 {
	return format.U32(c.data[offWorkerPID : offWorkerPID+4])
}

// ParentPID returns the recorded parent process id (Rust
// Control::parent_pid).
func (c *Control) ParentPID() uint32 {
	return format.U32(c.data[offParentPID : offParentPID+4])
}

// ParentAlive reports whether the recorded parent is still this
// process's parent (Rust Control::parent_alive on unix: the recorded
// pid is nonzero and equals getppid, proving the worker was not
// reparented by a crash of its launcher).
func (c *Control) ParentAlive() bool {
	expected := c.ParentPID()
	return expected != 0 && uint32(unix.Getppid()) == expected
}

// SetOpcode records the session opcode (Rust Control::set_opcode).
func (c *Control) SetOpcode(opcode Opcode) {
	format.PutU32(c.data[offOpcode:offOpcode+4], uint32(opcode))
}

// Opcode reads the session opcode (Rust Control::opcode).
func (c *Control) Opcode() (Opcode, bool) {
	value := Opcode(format.U32(c.data[offOpcode : offOpcode+4]))
	switch value {
	case OpcodeInspectRecoveryCandidates, OpcodeValidate, OpcodeRecover, OpcodeCleanupRecoveryAttempt:
		return value, true
	}
	return 0, false
}

// SetExternalPoll records the external-poll mode (Rust
// Control::set_external_poll).
func (c *Control) SetExternalPoll(enabled bool) {
	value := uint32(0)
	if enabled {
		value = 1
	}
	format.PutU32(c.data[offExternalPoll:offExternalPoll+4], value)
}

// ExternalPoll reads the external-poll mode (Rust
// Control::external_poll).
func (c *Control) ExternalPoll() bool {
	return format.U32(c.data[offExternalPoll:offExternalPoll+4]) != 0
}

// RequestCancel raises the cancellation flag (Rust
// Control::request_cancel, Release).
func (c *Control) RequestCancel() {
	mapAtomicStore32(baseOf(c.data), offCancelled, 1)
}

// Cancelled reads the cancellation flag (Rust Control::cancelled,
// Acquire).
func (c *Control) Cancelled() bool {
	return mapAtomicLoad32(baseOf(c.data), offCancelled) != 0
}

// SetResponse records the callback or poll response (Rust
// Control::set_response).
func (c *Control) SetResponse(value uint32) {
	format.PutU32(c.data[offResponse:offResponse+4], value)
}

// Response reads the callback or poll response (Rust
// Control::response).
func (c *Control) Response() uint32 {
	return format.U32(c.data[offResponse : offResponse+4])
}

// SetGuardPending records whether a cleanup guard is still pending
// (Rust Control::set_guard_pending).
func (c *Control) SetGuardPending(pending bool) {
	value := uint32(0)
	if pending {
		value = 1
	}
	format.PutU32(c.data[offGuardPending:offGuardPending+4], value)
}

// GuardPending reads the pending-cleanup-guard flag (Rust
// Control::guard_pending).
func (c *Control) GuardPending() bool {
	return format.U32(c.data[offGuardPending:offGuardPending+4]) != 0
}

// StartScratchCheckpoint opens a fresh scratch checkpoint (Rust
// Control::start_scratch_checkpoint): the previous entries are dropped,
// the attempt identity and the directory/security facts are recorded,
// and only then is the checkpoint marked active, so a reader never
// observes a half-written checkpoint.
func (c *Control) StartScratchCheckpoint(attemptID [16]byte, directoryIdentity publication.LocalFileIdentity, creationSecurity *publication.CreationSecurity) error {
	if attemptID == [16]byte{} {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch attempt ID is zero"}
	}
	if creationSecurity == nil {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch creation security is missing"}
	}
	mapAtomicStore32(baseOf(c.data), offScratchActive, 0)
	mapAtomicStore32(baseOf(c.data), offScratchCount, 0)
	copy(c.data[offScratchAttempt:offScratchAttempt+16], attemptID[:])
	format.PutU16(c.data[offScratchDirKind:offScratchDirKind+2], directoryIdentity.Kind)
	copy(c.data[offScratchDirID:offScratchDirID+32], directoryIdentity.Bytes[:])
	format.PutU16(c.data[offScratchSecKind:offScratchSecKind+2], creationSecurity.Kind)
	copy(c.data[offScratchSec:offScratchSec+32], creationSecurity.Commitment[:])
	mapAtomicStore32(baseOf(c.data), offScratchActive, 1)
	return nil
}

// AddScratchCheckpoint appends one exact scratch artifact to the active
// checkpoint (Rust Control::add_scratch_checkpoint). The writer
// publishes the entry before the count store, so a reader that sees the
// new count also sees the complete entry.
func (c *Control) AddScratchCheckpoint(ordinal uint32, identity publication.LocalFileIdentity) error {
	if mapAtomicLoad32(baseOf(c.data), offScratchActive) == 0 {
		return &format.Error{Code: format.CodeConflict, Detail: "scratch checkpoint is not active"}
	}
	count := int(mapAtomicLoad32(baseOf(c.data), offScratchCount))
	if count >= scratchEntryCapacity {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "scratch checkpoint entries"}
	}
	at := offScratchEntry + count*scratchEntryLen
	format.PutU32(c.data[at:at+4], ordinal)
	format.PutU16(c.data[at+4:at+6], identity.Kind)
	copy(c.data[at+8:at+40], identity.Bytes[:])
	mapAtomicStore32(baseOf(c.data), offScratchCount, uint32(count+1))
	return nil
}

// ScratchCheckpoint reads and validates the active scratch checkpoint
// (Rust Control::scratch_checkpoint). A nil checkpoint with a nil error
// means no checkpoint is active; every cross-check of the Rust
// authority is mirrored, including the identity/security kinds and the
// duplicate-authority rejection.
func (c *Control) ScratchCheckpoint() (*ScratchCheckpoint, error) {
	if mapAtomicLoad32(baseOf(c.data), offScratchActive) == 0 {
		return nil, nil
	}
	count := int(mapAtomicLoad32(baseOf(c.data), offScratchCount))
	if count > scratchEntryCapacity {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch checkpoint is invalid"}
	}
	var attemptID [16]byte
	copy(attemptID[:], c.data[offScratchAttempt:offScratchAttempt+16])
	if attemptID == [16]byte{} {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch checkpoint is invalid"}
	}
	directory := publication.LocalFileIdentity{
		Kind:  format.U16(c.data[offScratchDirKind : offScratchDirKind+2]),
		Bytes: [32]byte(c.data[offScratchDirID : offScratchDirID+32]),
	}
	security := publication.CreationSecurity{
		Kind:       format.U16(c.data[offScratchSecKind : offScratchSecKind+2]),
		Commitment: [32]byte(c.data[offScratchSec : offScratchSec+32]),
	}
	if !scratchIdentityValid(directory) {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch directory checkpoint is invalid"}
	}
	if !scratchSecurityValid(security.Kind, security.Commitment) {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch security checkpoint is invalid"}
	}
	checkpoint := &ScratchCheckpoint{
		AttemptID:         attemptID,
		DirectoryIdentity: directory,
		CreationSecurity:  security,
	}
	for index := 0; index < count; index++ {
		at := offScratchEntry + index*scratchEntryLen
		entry := ScratchCheckpointEntry{
			Ordinal: format.U32(c.data[at : at+4]),
			Identity: publication.LocalFileIdentity{
				Kind:  format.U16(c.data[at+4 : at+6]),
				Bytes: [32]byte(c.data[at+8 : at+40]),
			},
		}
		if !scratchIdentityValid(entry.Identity) {
			return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch artifact checkpoint is invalid"}
		}
		for _, prior := range checkpoint.Entries {
			if prior.Ordinal == entry.Ordinal || prior.Identity == entry.Identity {
				return nil, &format.Error{Code: format.CodeConflict, Detail: "worker scratch checkpoint contains duplicate authority"}
			}
		}
		checkpoint.Entries = append(checkpoint.Entries, entry)
	}
	return checkpoint, nil
}

// BeginRecoveryCheckpoint opens the recovery checkpoint window (Rust
// Control::begin_recovery_checkpoint): the parent observes the unsealed
// marker until the worker seals it.
func (c *Control) BeginRecoveryCheckpoint() {
	mapAtomicStore32(baseOf(c.data), offRecoveryCheck, 0)
}

// SealRecoveryCheckpoint seals the recovery checkpoint (Rust
// Control::seal_recovery_checkpoint).
func (c *Control) SealRecoveryCheckpoint() {
	mapAtomicStore32(baseOf(c.data), offRecoveryCheck, 1)
}

// RecoveryCheckpointSealed reports whether the recovery checkpoint is
// sealed (Rust Control::recovery_checkpoint_is_sealed).
func (c *Control) RecoveryCheckpointSealed() bool {
	return mapAtomicLoad32(baseOf(c.data), offRecoveryCheck) == 1
}

// BeginCallbackCheckpoint opens the callback checkpoint window (Rust
// Control::begin_callback_checkpoint): the kind word is zero until the
// worker seals it with the concrete kind.
func (c *Control) BeginCallbackCheckpoint() {
	mapAtomicStore32(baseOf(c.data), offCallbackCheck, 0)
}

// SealCallbackCheckpoint seals the callback checkpoint with its kind
// (Rust Control::seal_callback_checkpoint).
func (c *Control) SealCallbackCheckpoint(kind CallbackCheckpoint) {
	mapAtomicStore32(baseOf(c.data), offCallbackCheck, uint32(kind))
}

// CallbackCheckpoint reads the sealed callback-checkpoint kind (Rust
// Control::callback_checkpoint).
func (c *Control) CallbackCheckpoint() (CallbackCheckpoint, bool) {
	return callbackCheckpointFromWire(mapAtomicLoad32(baseOf(c.data), offCallbackCheck))
}

// RequestExternalPoll runs the external cancellation poll (Rust
// Control::request_external_poll): when external polling is disabled the
// recorded cancellation flag is the answer; otherwise the control enters
// CancelPoll and spins until the worker acknowledges the poll or the
// parent dies. The worker moves the state back to Running after it
// acknowledges any pending requests.
func (c *Control) RequestExternalPoll() bool {
	if !c.ExternalPoll() {
		return c.Cancelled()
	}
	c.SetState(stateCancelPoll)
	for c.state() == stateCancelPoll {
		if !c.ParentAlive() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return c.Response() != 0 || c.Cancelled()
}

// PayloadLen reads the sealed session payload length (Rust
// Control::payload_len).
func (c *Control) PayloadLen() (int, error) {
	length := int(format.U32(c.data[offPayloadLen : offPayloadLen+4]))
	if length > payloadCapacity {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker payload length is invalid"}
	}
	return length, nil
}

// SetPayloadLen seals the session payload length (Rust
// Control::set_payload_len).
func (c *Control) SetPayloadLen(length int) error {
	if length > payloadCapacity || length > int(^uint32(0)) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker control payload"}
	}
	format.PutU32(c.data[offPayloadLen:offPayloadLen+4], uint32(length))
	return nil
}

// PayloadByte returns one byte of the session payload (Rust
// Control::payload_byte): only bytes below the sealed payload length are
// readable.
func (c *Control) PayloadByte(at int) (byte, bool) {
	length, err := c.PayloadLen()
	if err != nil || at < 0 || at >= length {
		return 0, false
	}
	return c.data[offPayload+at], true
}

// WritePayload writes bytes into the session payload (Rust
// Control::write_payload: the checked destination stays inside the
// mapped capacity).
func (c *Control) WritePayload(at int, bytes []byte) error {
	if at < 0 || len(bytes) > payloadCapacity-at {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker control payload"}
	}
	copy(c.data[offPayload+at:offPayload+at+len(bytes)], bytes)
	return nil
}

// CallbackPayloadLen reads the sealed callback payload length (Rust
// Control::callback_payload_len).
func (c *Control) CallbackPayloadLen() (int, error) {
	length := int(format.U32(c.data[offCallbackPayLen : offCallbackPayLen+4]))
	if length > callbackPayCapacity {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker callback checkpoint length is invalid"}
	}
	return length, nil
}

// SetCallbackPayloadLen seals the callback payload length (Rust
// Control::set_callback_payload_len).
func (c *Control) SetCallbackPayloadLen(length int) error {
	if length > callbackPayCapacity || length > int(^uint32(0)) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker callback checkpoint"}
	}
	format.PutU32(c.data[offCallbackPayLen:offCallbackPayLen+4], uint32(length))
	return nil
}

// CallbackPayloadByte returns one byte of the callback payload (Rust
// Control::callback_payload_byte): only bytes below the sealed length
// are readable.
func (c *Control) CallbackPayloadByte(at int) (byte, bool) {
	length, err := c.CallbackPayloadLen()
	if err != nil || at < 0 || at >= length {
		return 0, false
	}
	return c.data[offCallbackPayload+at], true
}

// WriteCallbackPayload writes bytes into the callback payload (Rust
// Control::write_callback_payload: the checked destination stays inside
// the mapped capacity).
func (c *Control) WriteCallbackPayload(at int, bytes []byte) error {
	if at < 0 || len(bytes) > callbackPayCapacity-at {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker callback checkpoint"}
	}
	copy(c.data[offCallbackPayload+at:offCallbackPayload+at+len(bytes)], bytes)
	return nil
}

// Registration reads the armed-probe registration (Rust
// Control::registration): None (nil with no error) when the probe is
// disarmed, otherwise the validated generation, role, base, and length.
func (c *Control) Registration() (*MappingRegistration, error) {
	if mapAtomicLoad32(baseOf(c.data), offArmed) == 0 {
		return nil, nil
	}
	role, ok := roleFromWire(format.U32(c.data[offRole : offRole+4]))
	if !ok {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker mapping role is invalid"}
	}
	base := format.U64(c.data[offBase : offBase+8])
	length := format.U64(c.data[offLen : offLen+8])
	generation := format.U64(c.data[offGeneration : offGeneration+8])
	if generation == 0 || base == 0 || length == 0 || length > uint64(^uintptr(0)) {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker mapping registration is invalid"}
	}
	return &MappingRegistration{
		Generation: generation,
		Role:       role,
		Base:       uintptr(base),
		Len:        length,
	}, nil
}

// WaitFor spins until the control reaches the wanted state, the parent
// exits, or the 30 s limit expires (Rust Control::wait_for: the parent
// liveness probe runs on the worker side and turns a dead launcher into
// the Conflict class instead of a timeout).
func (c *Control) WaitFor(wanted uint32) error {
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if c.state() == wanted {
			return nil
		}
		if !c.ParentAlive() {
			return &format.Error{Code: format.CodeConflict, Detail: "SDK worker parent exited"}
		}
		time.Sleep(pollInterval)
	}
	return &format.Error{Code: format.CodeConflict, Detail: "worker protocol timed out"}
}
