//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

// Wire-era control-page unit tests (no signals, no subprocesses): the
// extended header identity, the session fields, the payload and
// callback buffers, the scratch/recovery/callback checkpoints, the
// armed-probe registration, and the state spins. The Rust authority
// vectors come from worker/control.rs and the wire arms of
// worker/client_tests.rs.

package worker

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// testIdentity builds one portable unix identity from a device+inode
// pair (Rust client_tests identity).
func testIdentity(device, inode uint64) publication.LocalFileIdentity {
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

func TestCreateParentWritesWireHeader(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer c.Close()
	data := c.data
	if string(data[offBuildID:offBuildID+buildLen]) != buildID {
		t.Fatalf("build id = %q, want %q", data[offBuildID:offBuildID+buildLen], buildID)
	}
	nonce := data[offNonce : offNonce+nonceLen]
	if format.U64(nonce[0:8]) == 0 && format.U64(nonce[8:16]) == 0 {
		t.Fatal("nonce is zero")
	}
	if got := format.U32(data[offParentPID : offParentPID+4]); got != uint32(os.Getpid()) {
		t.Fatalf("parent pid = %d, want %d", got, os.Getpid())
	}
	if err := c.VerifyRequest(); err != nil {
		t.Fatalf("fresh control fails verify: %v", err)
	}
}

func TestBuildIDLength(t *testing.T) {
	// Rust Control::verify_request rejects any build identity that is
	// not exactly BUILD_LEN bytes; the fixed default must satisfy the
	// handshake until the cmd binary wires its own value.
	if len(buildID) != buildLen {
		t.Fatalf("buildID length = %d, want %d", len(buildID), buildLen)
	}
}

func TestVerifyRequestRejectsTamperedHeader(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	original := append([]byte(nil), c.data...)
	cases := []struct {
		name   string
		tamper func(data []byte)
	}{
		{"magic", func(d []byte) { d[0] ^= 0xff }},
		{"protocol", func(d []byte) { format.PutU32(d[offProtocol:], protocol+1) }},
		{"build id", func(d []byte) { d[offBuildID] ^= 0xff }},
		{"state", func(d []byte) { format.PutU32(d[offState:], stateRunning) }},
		{"parent pid", func(d []byte) { format.PutU32(d[offParentPID:], 0) }},
	}
	for _, tc := range cases {
		copy(c.data, original)
		tc.tamper(c.data)
		wantCode(t, c.VerifyRequest(), format.CodeConflict)
	}
	// The restored header verifies again, proving the rejections above
	// were caused by the tamper and not by the control state.
	copy(c.data, original)
	if err := c.VerifyRequest(); err != nil {
		t.Fatalf("restored control fails verify: %v", err)
	}
}

func TestSessionFieldsRoundTrip(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetWorkerPID(4242)
	if got := c.WorkerPID(); got != 4242 {
		t.Fatalf("worker pid = %d, want 4242", got)
	}
	if got := c.ParentPID(); got != uint32(os.Getpid()) {
		t.Fatalf("parent pid = %d, want %d", got, os.Getpid())
	}
	for _, opcode := range []Opcode{OpcodeInspectRecoveryCandidates, OpcodeValidate, OpcodeRecover, OpcodeCleanupRecoveryAttempt} {
		c.SetOpcode(opcode)
		if got, ok := c.Opcode(); !ok || got != opcode {
			t.Fatalf("opcode = %v %v, want %v", got, ok, opcode)
		}
	}
	format.PutU32(c.data[offOpcode:], 99)
	if _, ok := c.Opcode(); ok {
		t.Fatal("opcode 99 accepted")
	}
	c.SetExternalPoll(true)
	if !c.ExternalPoll() {
		t.Fatal("external poll not set")
	}
	c.SetExternalPoll(false)
	if c.ExternalPoll() {
		t.Fatal("external poll not cleared")
	}
	if c.Cancelled() {
		t.Fatal("fresh control is cancelled")
	}
	c.RequestCancel()
	if !c.Cancelled() {
		t.Fatal("cancel flag not set")
	}
	c.SetResponse(7)
	if got := c.Response(); got != 7 {
		t.Fatalf("response = %d, want 7", got)
	}
	c.SetGuardPending(true)
	if !c.GuardPending() {
		t.Fatal("guard pending not set")
	}
	c.SetGuardPending(false)
	if c.GuardPending() {
		t.Fatal("guard pending not cleared")
	}
}

func TestStateTransitions(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	states := []uint32{stateWorkerReady, stateRunning, stateCancelPoll, stateFinding, stateUnknown, stateComplete, stateFailed, stateCleanupRequest, stateCleanupResult, stateFault, stateRequest}
	for _, state := range states {
		c.SetState(state)
		if got := c.state(); got != state {
			t.Fatalf("state = %d, want %d", got, state)
		}
	}
}

func TestPayloadBuffer(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if length, err := c.PayloadLen(); err != nil || length != 0 {
		t.Fatalf("fresh payload length = %d %v", length, err)
	}
	message := []byte("session payload")
	if err := c.WritePayload(0, message); err != nil {
		t.Fatal("write payload:", err)
	}
	if err := c.SetPayloadLen(len(message)); err != nil {
		t.Fatal("seal payload:", err)
	}
	length, err := c.PayloadLen()
	if err != nil || length != len(message) {
		t.Fatalf("payload length = %d %v", length, err)
	}
	for index := range message {
		value, ok := c.PayloadByte(index)
		if !ok || value != message[index] {
			t.Fatalf("payload byte %d = %v %v", index, value, ok)
		}
	}
	if _, ok := c.PayloadByte(len(message)); ok {
		t.Fatal("payload byte beyond the seal readable")
	}
	// The sealed bogus length is corruption, not a budget error.
	format.PutU32(c.data[offPayloadLen:], payloadCapacity+1)
	_, err = c.PayloadLen()
	wantCode(t, err, format.CodeFormatInvalid)
	format.PutU32(c.data[offPayloadLen:], 0)
	err = c.SetPayloadLen(payloadCapacity + 1)
	wantCode(t, err, format.CodeInsufficientResourceBudget)
	err = c.WritePayload(payloadCapacity-3, []byte("abcd"))
	wantCode(t, err, format.CodeInsufficientResourceBudget)
	// A zero-length write at the capacity edge is legal.
	if err := c.WritePayload(payloadCapacity, nil); err != nil {
		t.Fatalf("edge write rejected: %v", err)
	}
}

func TestCallbackPayloadBuffer(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	message := []byte("callback payload")
	if err := c.WriteCallbackPayload(2, message); err != nil {
		t.Fatal("write callback payload:", err)
	}
	if err := c.SetCallbackPayloadLen(2 + len(message)); err != nil {
		t.Fatal("seal callback payload:", err)
	}
	for index := range message {
		value, ok := c.CallbackPayloadByte(2 + index)
		if !ok || value != message[index] {
			t.Fatalf("callback byte %d = %v %v", index, value, ok)
		}
	}
	if _, ok := c.CallbackPayloadByte(2 + len(message)); ok {
		t.Fatal("callback byte beyond the seal readable")
	}
	format.PutU32(c.data[offCallbackPayLen:], callbackPayCapacity+1)
	_, err = c.CallbackPayloadLen()
	wantCode(t, err, format.CodeFormatInvalid)
	format.PutU32(c.data[offCallbackPayLen:], 0)
	err = c.SetCallbackPayloadLen(callbackPayCapacity + 1)
	wantCode(t, err, format.CodeInsufficientResourceBudget)
	err = c.WriteCallbackPayload(callbackPayCapacity-1, []byte("xy"))
	wantCode(t, err, format.CodeInsufficientResourceBudget)
}

// TestScratchCheckpointKeepsTwoExactNonoverlappingEntries ports the
// Rust client_tests scratch_checkpoint_keeps_two_exact_nonoverlapping_
// entries vector: two entries with distinct ordinals and identities
// survive the mapped checkpoint exactly.
func TestScratchCheckpointKeepsTwoExactNonoverlappingEntries(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	directory := testIdentity(11, 12)
	first := testIdentity(21, 22)
	second := testIdentity(31, 32)
	security := publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x5a}}
	if err := c.StartScratchCheckpoint([16]byte{0x41}, directory, &security); err != nil {
		t.Fatal("start:", err)
	}
	if err := c.AddScratchCheckpoint(7, first); err != nil {
		t.Fatal("add first:", err)
	}
	if err := c.AddScratchCheckpoint(8, second); err != nil {
		t.Fatal("add second:", err)
	}
	checkpoint, err := c.ScratchCheckpoint()
	if err != nil {
		t.Fatal("read checkpoint:", err)
	}
	if checkpoint == nil {
		t.Fatal("checkpoint is not active")
	}
	if checkpoint.AttemptID != [16]byte{0x41} {
		t.Fatalf("attempt id = %x", checkpoint.AttemptID)
	}
	if checkpoint.DirectoryIdentity != directory {
		t.Fatalf("directory identity = %+v", checkpoint.DirectoryIdentity)
	}
	if checkpoint.CreationSecurity != security {
		t.Fatalf("creation security = %+v", checkpoint.CreationSecurity)
	}
	if len(checkpoint.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(checkpoint.Entries))
	}
	if checkpoint.Entries[0].Ordinal != 7 || checkpoint.Entries[0].Identity != first {
		t.Fatalf("entry 0 = %+v", checkpoint.Entries[0])
	}
	if checkpoint.Entries[1].Ordinal != 8 || checkpoint.Entries[1].Identity != second {
		t.Fatalf("entry 1 = %+v", checkpoint.Entries[1])
	}
	// A second start drops the previous checkpoint and its entries.
	if err := c.StartScratchCheckpoint([16]byte{0x42}, directory, &security); err != nil {
		t.Fatal("restart:", err)
	}
	checkpoint, err = c.ScratchCheckpoint()
	if err != nil {
		t.Fatal("read restarted checkpoint:", err)
	}
	if checkpoint == nil || checkpoint.AttemptID != [16]byte{0x42} || len(checkpoint.Entries) != 0 {
		t.Fatalf("restarted checkpoint = %+v", checkpoint)
	}
}

func TestScratchCheckpointRejections(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	directory := testIdentity(1, 2)
	security := publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x6b}}
	err = c.StartScratchCheckpoint([16]byte{}, directory, &security)
	wantCode(t, err, format.CodeInvalidArgument)
	// Not active: add and read both report the inactive state.
	err = c.AddScratchCheckpoint(1, directory)
	wantCode(t, err, format.CodeConflict)
	if checkpoint, err := c.ScratchCheckpoint(); err != nil || checkpoint != nil {
		t.Fatalf("inactive checkpoint = %v %v", checkpoint, err)
	}
	if err := c.StartScratchCheckpoint([16]byte{1}, directory, &security); err != nil {
		t.Fatal("start:", err)
	}
	// Two entries fit; a third exceeds the capacity.
	if err := c.AddScratchCheckpoint(1, testIdentity(3, 4)); err != nil {
		t.Fatal("add:", err)
	}
	if err := c.AddScratchCheckpoint(2, testIdentity(5, 6)); err != nil {
		t.Fatal("add:", err)
	}
	err = c.AddScratchCheckpoint(3, testIdentity(7, 8))
	wantCode(t, err, format.CodeInsufficientResourceBudget)
	// Duplicate ordinal is rejected at read time.
	replacement, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := replacement.StartScratchCheckpoint([16]byte{2}, directory, &security); err != nil {
		t.Fatal("start:", err)
	}
	if err := replacement.AddScratchCheckpoint(9, testIdentity(3, 4)); err != nil {
		t.Fatal("add:", err)
	}
	if err := replacement.AddScratchCheckpoint(9, testIdentity(5, 6)); err != nil {
		t.Fatal("add:", err)
	}
	_, err = replacement.ScratchCheckpoint()
	wantCode(t, err, format.CodeConflict)
	// An invalid directory identity kind fails the read.
	invalid, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer invalid.Close()
	badDirectory := publication.LocalFileIdentity{Kind: 9, Bytes: [32]byte{1}}
	if err := invalid.StartScratchCheckpoint([16]byte{3}, badDirectory, &security); err != nil {
		t.Fatal("start:", err)
	}
	_, err = invalid.ScratchCheckpoint()
	wantCode(t, err, format.CodeConflict)
}

func TestRecoveryCheckpointSeal(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.RecoveryCheckpointSealed() {
		t.Fatal("fresh recovery checkpoint is sealed")
	}
	c.BeginRecoveryCheckpoint()
	if c.RecoveryCheckpointSealed() {
		t.Fatal("begun recovery checkpoint is sealed")
	}
	c.SealRecoveryCheckpoint()
	if !c.RecoveryCheckpointSealed() {
		t.Fatal("sealed recovery checkpoint not observed")
	}
}

func TestCallbackCheckpointSeal(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.CallbackCheckpoint(); ok {
		t.Fatal("fresh callback checkpoint has a kind")
	}
	c.BeginCallbackCheckpoint()
	if _, ok := c.CallbackCheckpoint(); ok {
		t.Fatal("begun callback checkpoint has a kind")
	}
	c.SealCallbackCheckpoint(CallbackRecoveryReport)
	if kind, ok := c.CallbackCheckpoint(); !ok || kind != CallbackRecoveryReport {
		t.Fatalf("callback checkpoint = %v %v", kind, ok)
	}
	c.BeginCallbackCheckpoint()
	c.SealCallbackCheckpoint(CallbackValidationProgress)
	if kind, ok := c.CallbackCheckpoint(); !ok || kind != CallbackValidationProgress {
		t.Fatalf("callback checkpoint = %v %v", kind, ok)
	}
	format.PutU32(c.data[offCallbackCheck:], 99)
	if _, ok := c.CallbackCheckpoint(); ok {
		t.Fatal("callback checkpoint kind 99 accepted")
	}
}

func TestRegistration(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if registration, err := c.Registration(); err != nil || registration != nil {
		t.Fatalf("disarmed registration = %v %v", registration, err)
	}
	if err := c.Arm(23, RoleScratch, 0x7f0000001000, 8192); err != nil {
		t.Fatal("arm:", err)
	}
	registration, err := c.Registration()
	if err != nil {
		t.Fatal("registration:", err)
	}
	if registration == nil || registration.Generation != 23 || registration.Role != RoleScratch ||
		registration.Base != 0x7f0000001000 || registration.Len != 8192 {
		t.Fatalf("registration = %+v", registration)
	}
	c.Disarm()
	if registration, err := c.Registration(); err != nil || registration != nil {
		t.Fatalf("disarmed registration = %v %v", registration, err)
	}
}

func TestRegistrationRejectsInvalidRole(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mapAtomicStore32(baseOf(c.data), offArmed, 1)
	_, err = c.Registration()
	wantCode(t, err, format.CodeConflict)
}

func TestWaitFor(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Already in the wanted state: immediate success without any spin.
	if err := c.WaitFor(stateRequest); err != nil {
		t.Fatalf("wait for current state: %v", err)
	}
	// A wanted state transition in the same process succeeds.
	c.SetState(stateWorkerReady)
	if err := c.WaitFor(stateWorkerReady); err != nil {
		t.Fatalf("wait for set state: %v", err)
	}
	// The parent-liveness probe runs on the worker side: in-process the
	// recorded parent pid never equals getppid, so any other wait fails
	// with the parent-exited class instead of spinning for 30 s.
	c.SetState(stateRunning)
	err = c.WaitFor(stateComplete)
	wantCode(t, err, format.CodeConflict)
}

func TestRequestExternalPoll(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// External polling disabled: the recorded cancellation flag is the
	// whole answer.
	if c.RequestExternalPoll() {
		t.Fatal("fresh control reports cancellation")
	}
	c.RequestCancel()
	if !c.RequestExternalPoll() {
		t.Fatal("recorded cancellation not reported")
	}
	// External polling enabled: the control enters CancelPoll and the
	// parent-liveness probe answers; in-process the parent is never
	// alive, so the poll reports the cancelled answer immediately.
	c.SetExternalPoll(true)
	if !c.RequestExternalPoll() {
		t.Fatal("cancel-poll did not report the dead parent")
	}
}

func TestParentAlive(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// In-process the recorded parent pid is this process, and getppid
	// names the real OS parent, so the worker-side liveness probe is
	// false without a worker child.
	if c.ParentAlive() {
		t.Fatal("in-process parent liveness probe reports alive")
	}
	// Rust parent_alive also rejects a zero pid.
	format.PutU32(c.data[offParentPID:], 0)
	if c.ParentAlive() {
		t.Fatal("zero parent pid reports alive")
	}
	// The worker side records its real parent; simulate it by recording
	// getppid, which is this process's OS parent.
	format.PutU32(c.data[offParentPID:], uint32(os.Getppid()))
	if !c.ParentAlive() {
		t.Fatal("recorded OS parent not recognized")
	}
}
