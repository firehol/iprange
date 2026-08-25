//go:build linux && amd64

// In-process worker double for the client drive tests. The Rust
// client_tests.rs drives the real worker binary, which the Go slice
// cannot assume exists, so the test binary re-executes itself as a
// fake iprange-v4-worker: TestMain dispatches on workerDoubleEnv before
// the test suite runs, and every spawn in this suite carries that
// environment variable, so the child never recurses into the suite.
// Each mode walks the same control protocol as the real worker (open,
// verify, own pid, WorkerReady, wait for the session to start) and
// then records one scripted outcome.

package worker

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// workerDoubleEnv selects the double mode of the re-executed test
// binary.
const workerDoubleEnv = "IPRANGE_V4_WORKER_DOUBLE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(workerDoubleEnv); mode != "" {
		os.Exit(workerDoubleMain(mode))
	}
	// A double spawned without its mode would re-enter the suite and
	// recurse; refuse loudly instead.
	if doubleControlArg() {
		fmt.Fprintln(os.Stderr, "worker double spawned without a mode")
		os.Exit(127)
	}
	os.Exit(m.Run())
}

// doubleControlArg reports whether the process was spawned with the
// worker `--control` argument.
func doubleControlArg() bool {
	args := os.Args
	for i := 0; i < len(args); i++ {
		if args[i] == "--control" {
			return true
		}
	}
	return false
}

// doubleControlPath extracts the control path of the spawned double
// (the worker binary arg contract: `--control <path>`).
func doubleControlPath() string {
	args := os.Args
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--control" {
			return args[i+1]
		}
	}
	return ""
}

// workerDoubleMain runs one scripted double mode and returns the exit
// code of the fake worker.
func workerDoubleMain(mode string) int {
	switch mode {
	case "exit0":
		return 0
	case "exit1":
		return 1
	case "hang":
		time.Sleep(time.Hour)
		return 0
	}
	control, err := OpenWorker(doubleControlPath())
	if err != nil {
		return 126
	}
	defer control.Close()
	if err := control.VerifyRequest(); err != nil {
		return 125
	}
	control.SetWorkerPID(uint32(os.Getpid()))
	if mode == "wrongpid" {
		// Publish a pid that can never match the spawned child.
		control.SetWorkerPID(control.WorkerPID() + 1)
	}
	control.SetState(stateWorkerReady)
	if mode == "wrongpid" {
		// The parent aborts on the identity mismatch; stay alive
		// until the kill lands.
		time.Sleep(time.Hour)
		return 0
	}
	// Wait for the session to start (the parent publishes Running after
	// the handshake and, in the cleanup tests, immediately moves on to
	// CleanupRequest; the real worker tolerates any post-Ready state, so
	// the double waits for the control to leave Request/WorkerReady
	// instead of pinning one fleeting state).
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		switch control.state() {
		case stateRequest, stateWorkerReady:
			if !control.ParentAlive() {
				return 124
			}
			time.Sleep(time.Millisecond)
		default:
			goto running
		}
	}
	return 124
running:
	switch mode {
	case "ready":
		return 0
	case "vanish":
		// Exit with no terminal record.
		return 0
	case "complete":
		control.SetState(stateComplete)
		return 0
	case "complete_guard":
		control.SetGuardPending(true)
		control.SetState(stateComplete)
		time.Sleep(200 * time.Millisecond)
		return 0
	case "complete_bad":
		control.SetState(stateComplete)
		return 3
	case "fault":
		doubleWriteFaultRecord(control)
		return ownedFaultExit
	case "fault_bad":
		control.SetState(stateFault)
		return 0
	case "failed":
		if err := WriteWorkerError(control, &format.Error{Code: format.CodeConflict, Detail: "double failure"}); err != nil {
			return 123
		}
		control.SetState(stateFailed)
		return 0
	case "failed_bad":
		control.SetState(stateFailed)
		return 2
	case "cancel":
		if !doubleWaitFor(control.Cancelled, 30*time.Second) {
			return 122
		}
		control.SetState(stateComplete)
		return 0
	case "cancelpoll":
		control.SetState(stateCancelPoll)
		if err := control.WaitFor(stateRunning); err != nil {
			return 124
		}
		control.SetState(stateComplete)
		return 0
	case "finding":
		// A known state the default drive hook refuses; the parent
		// aborts.
		control.SetState(stateFinding)
		time.Sleep(time.Hour)
		return 0
	case "finding_then_complete":
		// A known state a custom drive hook may consume; without a
		// hook the parent aborts before Complete matters.
		control.SetState(stateFinding)
		time.Sleep(100 * time.Millisecond)
		control.SetState(stateComplete)
		return 0
	case "cleanup_ok":
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, true, nil); err != nil {
			return 123
		}
		return 0
	case "cleanup_bad":
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, true, nil); err != nil {
			return 123
		}
		return 5
	case "cleanup_problem":
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, false, &WireProblem{Code: format.CodeCleanupConflict, Detail: "double cleanup problem"}); err != nil {
			return 123
		}
		return 0
	case "cleanup_noproblem":
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, false, nil); err != nil {
			return 123
		}
		return 0
	case "inspection_complete":
		inspection := &InspectionWire{SourceIdentity: publication.LocalFileIdentity{}, Progress: ProgressWire{}}
		if err := WriteInspectionResult(control, inspection, nil); err != nil {
			return 123
		}
		control.SetState(stateComplete)
		return 0
	case "validation_complete":
		if err := doubleWriteValidationResult(control); err != nil {
			return 123
		}
		control.SetState(stateComplete)
		return 0
	case "validation_finding_then_complete":
		if !doubleStreamFinding(control) {
			return 122
		}
		if err := doubleWriteValidationResult(control); err != nil {
			return 123
		}
		control.SetState(stateComplete)
		return 0
	case "validation_finding_then_exit3":
		if !doubleStreamFinding(control) {
			return 122
		}
		return 3
	case "validation_guard_pending":
		if err := doubleWriteValidationFailureGuard(control); err != nil {
			return 123
		}
		control.SetGuardPending(true)
		control.SetState(stateComplete)
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, true, nil); err != nil {
			return 123
		}
		return 0
	case "recovery_complete":
		if err := doubleWriteRecoveryResult(control); err != nil {
			return 123
		}
		control.SetState(stateComplete)
		return 0
	case "recovery_unknown_then_complete":
		if !doubleStreamUnknown(control) {
			return 122
		}
		if err := doubleWriteRecoveryResult(control); err != nil {
			return 123
		}
		control.SetState(stateComplete)
		return 0
	case "recovery_unknown_then_exit3":
		if !doubleStreamUnknown(control) {
			return 122
		}
		return 3
	case "recovery_fault":
		// The recovery fault-retry client arm discards the
		// parent-owned attempt through an isolated cleanup session
		// after the interrupted fault; the double serves that cleanup
		// session with the real discard machine (like the real worker)
		// and reserves this mode's fault script for the Recover
		// opcode.
		if doubleOpcodeIs(control, OpcodeCleanupRecoveryAttempt) {
			return doubleRunCleanupMachine(control)
		}
		doubleWriteFaultRecord(control)
		return ownedFaultExit
	case "recovery_callback_fail":
		// The callback-failure client arm discards the parent-owned
		// attempt after the missing report refusal; the double serves
		// the cleanup session with the real discard machine (like the
		// real worker) and reserves the stream-then-exit script for
		// the Recover opcode.
		if doubleOpcodeIs(control, OpcodeCleanupRecoveryAttempt) {
			return doubleRunCleanupMachine(control)
		}
		if !doubleStreamUnknown(control) {
			return 122
		}
		return 3
	case "recovery_guard_pending":
		if err := doubleWriteRecoveryFailureGuard(control); err != nil {
			return 123
		}
		control.SetGuardPending(true)
		control.SetState(stateComplete)
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			return 124
		}
		if err := doubleWriteCleanupResult(control, true, nil); err != nil {
			return 123
		}
		return 0
	}
	return 121
}

// doubleWriteCleanupResult writes one cleanup-result payload and then
// seals it with the CleanupResult state, exactly like the real cleanup
// worker (the wire codec writes only the payload; worker/cleanup.rs
// publishes the terminal state).
func doubleWriteCleanupResult(control *Control, complete bool, problem *WireProblem) error {
	if err := WriteValidationCleanupResult(control, complete, problem); err != nil {
		return err
	}
	control.SetState(stateCleanupResult)
	return nil
}

// doubleWriteFaultRecord stamps a complete owned-fault record into the
// control (the Rust fault handler field set; the parent side validates
// every cross-check in Control.FaultRecord).
func doubleWriteFaultRecord(control *Control) {
	data := control.data
	base := baseOf(data)
	format.PutU64(data[offGeneration:offGeneration+8], 1)
	format.PutU32(data[offRole:offRole+4], uint32(RoleSource))
	format.PutU64(data[offBase:offBase+8], 0x1000)
	format.PutU64(data[offLen:offLen+8], 0x2000)
	format.PutU64(data[offFaultGen:offFaultGen+8], 1)
	format.PutU32(data[offFaultRole:offFaultRole+4], uint32(RoleSource))
	format.PutU32(data[offFaultCode:offFaultCode+4], 7)
	format.PutU64(data[offFaultRelative:offFaultRelative+8], 0x100)
	format.PutU64(data[offFaultAddress:offFaultAddress+8], 0x1100)
	mapAtomicStore32(base, offHandling, 1)
	mapAtomicStore32(base, offFaultMarker, faultMarker)
	control.SetState(stateFault)
}

// doubleWaitFor polls one double-side condition until it holds or the
// limit expires.
func doubleWaitFor(condition func() bool, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// doubleWriteValidationResult writes one completed validation result
// envelope with zero facts (the arm reads the mailbox; the fixture
// values only need to round-trip).
func doubleWriteValidationResult(control *Control) error {
	progress := validation.NewProgress()
	result := &validation.ValidationResult{
		Valid:        false,
		FileIdentity: publication.LocalFileIdentity{},
		Progress:     progress,
	}
	return WriteValidationResult(control, result, nil, nil)
}

// doubleWriteValidationFailureGuard writes one operational validation
// failure envelope with a retained publication problem (the
// guard-pending arm cross-check pair).
func doubleWriteValidationFailureGuard(control *Control) error {
	progress := validation.NewProgress()
	failure := &validation.ValidationFailure{
		Cause:    &format.Error{Code: format.CodeConflict, Detail: "double validation failure"},
		Progress: &progress,
	}
	retained := &WireProblem{Code: format.CodeCleanupConflict, Detail: "double retained problem"}
	return WriteValidationResult(control, nil, failure, retained)
}

// doubleWriteRecoveryResult writes one completed recovery outcome
// envelope with zero facts.
func doubleWriteRecoveryResult(control *Control) error {
	outcome := &RecoveryOutcome{
		Result: &recovery.RecoveryResult{Report: recovery.RecoveryReport{}, Publication: publication.PublicationResult{}},
	}
	return WriteRecoveryOutcome(control, outcome, nil)
}

// doubleWriteRecoveryFailureGuard writes one recovery preparation
// failure envelope with a retained publication problem.
func doubleWriteRecoveryFailureGuard(control *Control) error {
	outcome := &RecoveryOutcome{
		Failure: &recovery.RecoveryPreparationFailure{
			Report: recovery.RecoveryReport{},
			Cause:  &format.Error{Code: format.CodeConflict, Detail: "double recovery failure"},
		},
	}
	retained := &WireProblem{Code: format.CodeCleanupConflict, Detail: "double retained problem"}
	return WriteRecoveryOutcome(control, outcome, retained)
}

// doubleStreamFinding writes one finding envelope, publishes the
// Finding state, and waits for the parent acknowledgement (the parent
// callback acknowledge seam returns the control to Running).
func doubleStreamFinding(control *Control) bool {
	page := uint32(4)
	finding := &validation.ValidationFinding{
		Sequence:   1,
		Reason:     validation.ReasonIoError,
		Object:     validation.ObjectMeta,
		PageNumber: &page,
	}
	if err := WriteValidationFinding(control, finding); err != nil {
		return false
	}
	control.SetResponse(0)
	control.SetState(stateFinding)
	return doubleWaitFor(func() bool { return control.state() == stateRunning }, 10*time.Second)
}

// doubleStreamUnknown writes one unknown-damage envelope, publishes the
// Unknown state, and waits for the parent acknowledgement.
func doubleStreamUnknown(control *Control) bool {
	page := uint32(4)
	envelope := &recovery.RecoveryUnknownEnvelope{
		Sequence:   1,
		Reason:     validation.ReasonIoError,
		Object:     validation.ObjectMeta,
		PageNumber: &page,
	}
	if err := WriteRecoveryUnknown(control, envelope); err != nil {
		return false
	}
	control.SetResponse(0)
	control.SetState(stateUnknown)
	return doubleWaitFor(func() bool { return control.state() == stateRunning }, 10*time.Second)
}

// doubleOpcodeIs reports whether the current session opcode matches
// one value (the double reads the opcode the parent recorded before
// spawning, exactly like the real worker dispatch).
func doubleOpcodeIs(control *Control, want Opcode) bool {
	got, ok := control.Opcode()
	return ok && got == want
}

// doubleRunCleanupMachine serves one cleanup session with the real
// discard machine (cmd modes.go runCleanup parity): the request is
// decoded through the 4-11A codec, the secured attempt is discarded
// through the exported publication seam, the result is written, and
// the session completes. The recovery double modes need this arm so
// the client-side discard composition in the retry and callback
// failure loops observes a genuine clean removal, exactly like a real
// worker binary would provide.
func doubleRunCleanupMachine(control *Control) int {
	request, err := ReadCleanupRequest(control)
	if err != nil {
		return 123
	}
	discarded := publication.DiscardSecuredAttempt(request.DestinationPath, &request.Output)
	facts := WireEarlyDiscardOf(discarded)
	scratch := CleanupCheckpoint(request.Scratch)
	if err := WriteCleanupResult(control, &facts, scratch); err != nil {
		return 123
	}
	control.SetState(stateComplete)
	return 0
}
