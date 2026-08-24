//go:build linux && amd64

// Per-mode worker drivers (Rust worker.rs run_validation / run_recovery
// / inspect / cleanup and the callback proxies): each driver reads its
// request through the 4-11A wire codecs, runs the Go domain machine
// that exists in the tree after milestone 2, and writes the result
// through the same codecs. The machine check hook is the recorded
// 4-10/4-11 domain seam: it runs the control's external cancellation
// poll at every bounded-work checkpoint. The 4-11A callback accessors
// (BeginCallbackCheckpoint / WriteCallbackPayload /
// SealCallbackCheckpoint) publish the sealed progress payloads once the
// domain machines expose mid-run progress; until then the progress
// checkpoints stay recorded with the 4-11 client slices.

package main

import (
	"errors"
	"sort"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// runMode dispatches one session opcode to its mode driver (Rust
// worker.rs opcode match). Every driver returns the retained source
// cleanup guard of its terminal, if any; a mode failure propagates to
// run, which writes it as the worker error.
func runMode(control *worker.Control, opcode worker.Opcode) (*recovery.RecoverySourceCleanupGuard, error) {
	switch opcode {
	case worker.OpcodeInspectRecoveryCandidates:
		return nil, runInspect(control)
	case worker.OpcodeValidate:
		return runValidation(control)
	case worker.OpcodeRecover:
		return runRecovery(control)
	case worker.OpcodeCleanupRecoveryAttempt:
		return nil, runCleanup(control)
	default:
		// The Opcode accessor already bounds the wire value; the arm
		// keeps the closed-set invariant explicit (Rust exhaustive
		// match).
		return nil, &format.Error{Code: format.CodeInvalidEnum, Detail: "worker opcode is invalid"}
	}
}

// newWorkerCheckpoint builds the domain check hook of one worker
// session (Rust CancellationToken::from_poll over
// control.request_external_poll): at every bounded-work checkpoint the
// control runs its cancellation poll, and a cancelled session reports
// the Cancelled class exactly like the public token checkpoint.
func newWorkerCheckpoint(control *worker.Control) func() error {
	return func() error {
		if control.RequestExternalPoll() {
			return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
		}
		return nil
	}
}

// unreadableSourcePages is the sorted, duplicate-free source-page list
// of one session (Rust worker.rs thread-local UNREADABLE_SOURCE_PAGES).
// The 4-11D fault-memory slice consumes the list for mapped-page
// retries; every request path already validates the facts exactly like
// Rust set_unreadable_source_pages.
var unreadableSourcePages []uint32

// setUnreadableSourcePages records one request's unreadable source
// pages after sorting and duplicate rejection (Rust
// set_unreadable_source_pages: duplicates are InvalidArgument).
func setUnreadableSourcePages(pages []uint32) error {
	sorted := append([]uint32(nil), pages...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for index := 1; index < len(sorted); index++ {
		if sorted[index] == sorted[index-1] {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "unreadable source pages contain duplicates"}
		}
	}
	unreadableSourcePages = sorted
	return nil
}

// runInspect serves one InspectRecoveryCandidates session (Rust
// worker.rs inspect): the inspection failure travels inside the result
// envelope (tag 1), never as the session error.
func runInspect(control *worker.Control) error {
	request, err := worker.ReadInspectionRequest(control)
	if err != nil {
		return err
	}
	if err := setUnreadableSourcePages(request.UnreadablePages); err != nil {
		return err
	}
	inspection, err := recovery.InspectRecoveryCandidates(request.Path, request.Mode, &request.Budget, newWorkerCheckpoint(control))
	if err != nil {
		return worker.WriteInspectionResult(control, nil, err)
	}
	wire := worker.WireInspectionOf(inspection)
	return worker.WriteInspectionResult(control, &wire, nil)
}

// runValidation serves one Validate session (Rust worker.rs
// run_validation): the request mode selects the sweep entry (the
// offline-candidate arm composes the recovery-owned terminal), the
// streamed findings run through the callback proxy, and the retained
// publication problem rides on the result envelope.
func runValidation(control *worker.Control) (*recovery.RecoverySourceCleanupGuard, error) {
	request, err := worker.ReadValidationRequest(control)
	if err != nil {
		return nil, err
	}
	if err := setUnreadableSourcePages(request.UnreadablePages); err != nil {
		return nil, err
	}
	sink := &validationProxy{control: control, suppressThrough: request.DeliveredFindings}
	var result *validation.ValidationResult
	var failure *validation.ValidationFailure
	switch request.Mode {
	case validation.ValidationModeImmutableCurrent, validation.ValidationModeLiveCurrent:
		result, failure = validation.Validate(request.Path, request.Mode, &request.Budget, newWorkerCheckpoint(control), sink)
	case validation.ValidationModeOfflineCandidate:
		result, failure = recovery.ValidateOfflineCandidate(request.Path, request.Candidate, &request.Budget, newWorkerCheckpoint(control), sink)
	default:
		// The request codec bounds the mode tag; the arm keeps the
		// closed-set invariant explicit.
		return nil, &format.Error{Code: format.CodeInvalidEnum, Detail: "worker validation mode is invalid"}
	}
	var guard *recovery.RecoverySourceCleanupGuard
	if failure != nil {
		// The Go validation machine does not retain a source cleanup
		// guard yet (its failure field stays nil; recorded with the
		// 4-11 completion). Accept the guard when a later slice does.
		if source, ok := failure.SourceCleanup.(*recovery.RecoverySourceCleanupGuard); ok {
			guard = source
			failure.SourceCleanup = nil
		}
	}
	var retained *worker.WireProblem
	if guard != nil {
		problem := problemWireOf(guard.LastProblem())
		retained = &problem
	}
	if err := worker.WriteValidationResult(control, result, failure, retained); err != nil {
		return nil, err
	}
	return guard, nil
}

// runRecovery serves one Recover session (Rust worker.rs run_recovery):
// the worker mode selects the built machine, the streamed
// unknown-damage envelopes run through the callback proxy, and a
// retained source release failure becomes the cleanup guard of the
// terminal. The Go recovery machine creates its own secured output at
// the request destination (recovery api.go "worker client create
// position"); the resume of a parent-created attempt stays recorded
// with the 4-11 completion.
func runRecovery(control *worker.Control) (*recovery.RecoverySourceCleanupGuard, error) {
	request, err := worker.ReadRecoveryRequest(control)
	if err != nil {
		return nil, err
	}
	if err := setUnreadableSourcePages(request.UnreadablePages); err != nil {
		return nil, err
	}
	sink := &recoveryProxy{control: control, suppressThrough: request.DeliveredUnknowns}
	var result *recovery.RecoveryResult
	var failure *recovery.RecoveryPreparationFailure
	switch request.Mode {
	case worker.WorkerModeImmutable:
		result, failure = recovery.RecoverImmutable(request.SourcePath, &request.Candidate, request.DestinationPath, &request.Budget, newWorkerCheckpoint(control), sink)
	case worker.WorkerModeOffline:
		result, failure = recovery.RecoverOffline(request.SourcePath, &request.Candidate, request.DestinationPath, &request.Budget, newWorkerCheckpoint(control), sink)
	case worker.WorkerModeLive:
		result, failure = recovery.RecoverLive(request.SourcePath, &request.Candidate, request.DestinationPath, &request.Budget, newWorkerCheckpoint(control), sink)
	default:
		// The request codec bounds the mode tag; the arm keeps the
		// closed-set invariant explicit.
		return nil, &format.Error{Code: format.CodeInvalidEnum, Detail: "worker recovery mode is invalid"}
	}
	var guard *recovery.RecoverySourceCleanupGuard
	if failure != nil && failure.SourceCleanup != nil {
		guard = failure.SourceCleanup
		failure.SourceCleanup = nil
	}
	var retained *worker.WireProblem
	if guard != nil {
		problem := problemWireOf(guard.LastProblem())
		retained = &problem
	}
	outcome := &worker.RecoveryOutcome{Result: result, Failure: failure}
	if err := worker.WriteRecoveryOutcome(control, outcome, retained); err != nil {
		return nil, err
	}
	return guard, nil
}

// runCleanup serves one CleanupRecoveryAttempt session (Rust
// worker/cleanup.rs run_worker): the request envelope flows through the
// 4-11A codec. The discard machine arms of the Go tree
// (internal/publication cleanup.go discard_attempt / confirmed_absent
// / failed_attempt and output_resume.go resume_secured_output_for_
// cleanup) are package-private; the SOW plan records cleanup +
// publication wire arms for slice 4-11E. Until that slice exports the
// machine, this driver reports the missing arm as a typed Conflict
// instead of fabricating discard facts.
func runCleanup(control *worker.Control) error {
	if _, err := worker.ReadCleanupRequest(control); err != nil {
		return err
	}
	return &format.Error{Code: format.CodeConflict, Detail: "worker cleanup machine is not wired in this build"}
}

// serveCleanup drives the retained source cleanup guard (Rust worker.rs
// serve_cleanup): it waits for each CleanupRequest, retries the guard,
// writes the cleanup result, and repeats after a failed retry. A
// completed cleanup clears guard pending and returns; a dead parent
// ends the loop gracefully (Rust parity).
func serveCleanup(control *worker.Control, guard *recovery.RecoverySourceCleanupGuard) error {
	for {
		if err := control.WaitFor(stateCleanupRequest); err != nil {
			if !control.ParentAlive() {
				return nil
			}
			return err
		}
		complete, problem := guard.RetryCleanup()
		if complete {
			if err := worker.WriteValidationCleanupResult(control, true, nil); err != nil {
				return err
			}
			control.SetGuardPending(false)
			control.SetState(stateCleanupResult)
			return nil
		}
		wire := problemWireOf(problem)
		if err := worker.WriteValidationCleanupResult(control, false, &wire); err != nil {
			return err
		}
		control.SetState(stateCleanupResult)
	}
}

// waitAcknowledgement spins until the parent acknowledges one streamed
// callback by returning the control to Running (Rust proxy loops: the
// callback state is left, then the response word decides). Parent death
// maps to Cancelled exactly like the Rust proxy arms; the control
// surface bounds the spin by its 30 s wait limit.
func waitAcknowledgement(control *worker.Control) error {
	if err := control.WaitFor(stateRunning); err != nil {
		if !control.ParentAlive() {
			return &format.Error{Code: format.CodeCancelled, Detail: "SDK worker parent exited"}
		}
		return err
	}
	return nil
}

// validationProxy streams one worker validation's findings to the
// parent with suppression of the already-delivered prefix (Rust
// worker.rs ValidationProxy).
type validationProxy struct {
	control         *worker.Control
	suppressThrough uint64
}

// Finding implements validation.ValidationSink.
func (p *validationProxy) Finding(finding *validation.ValidationFinding) (validation.ValidationSinkControl, error) {
	if finding.Sequence <= p.suppressThrough {
		return validation.SinkContinue, nil
	}
	if err := worker.WriteValidationFinding(p.control, finding); err != nil {
		return 0, err
	}
	p.control.SetResponse(0)
	p.control.SetState(stateFinding)
	if err := waitAcknowledgement(p.control); err != nil {
		return 0, err
	}
	switch p.control.Response() {
	case 0:
		return validation.SinkContinue, nil
	case 1:
		return validation.SinkStop, nil
	case 2:
		cause, err := worker.ReadWorkerError(p.control)
		if err != nil {
			return 0, err
		}
		return 0, cause
	default:
		return 0, &format.Error{Code: format.CodeConflict, Detail: "worker validation callback response is invalid"}
	}
}

// recoveryProxy streams one worker recovery's unknown-damage envelopes
// to the parent with suppression of the already-delivered prefix (Rust
// worker.rs RecoveryProxy).
type recoveryProxy struct {
	control         *worker.Control
	suppressThrough uint64
}

// Unknown implements recovery.RecoverySink.
func (p *recoveryProxy) Unknown(envelope *recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
	if envelope.Sequence <= p.suppressThrough {
		return recovery.RecoverySinkContinue, nil
	}
	if err := worker.WriteRecoveryUnknown(p.control, envelope); err != nil {
		return 0, err
	}
	p.control.SetResponse(0)
	p.control.SetState(stateUnknown)
	if err := waitAcknowledgement(p.control); err != nil {
		return 0, err
	}
	switch p.control.Response() {
	case 0:
		return recovery.RecoverySinkContinue, nil
	case 1:
		return recovery.RecoverySinkStop, nil
	case 2:
		cause, err := worker.ReadWorkerError(p.control)
		if err != nil {
			return 0, err
		}
		return 0, cause
	default:
		return 0, &format.Error{Code: format.CodeConflict, Detail: "worker recovery callback response is invalid"}
	}
}

// problemWireOf folds one boundary error into the wire problem shape;
// the worker package keeps its wireProblemOf helper private, so the
// boundary repeats the stable mapping: a format.Error keeps its class
// and detail, an errno chain reports the IO class with the raw errno,
// and any other error is the Conflict class of an unknown failure.
func problemWireOf(err error) worker.WireProblem {
	var formatted *format.Error
	if errors.As(err, &formatted) {
		return worker.WireProblem{Code: formatted.Code, Detail: formatted.Detail}
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		code := int32(errno)
		return worker.WireProblem{Code: format.CodeIO, OSCode: &code, Detail: err.Error()}
	}
	return worker.WireProblem{Code: format.CodeConflict, Detail: err.Error()}
}
