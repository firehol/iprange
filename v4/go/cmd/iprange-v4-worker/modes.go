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
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// pollInterval is the worker spin step of the Rust 1 ms sleep
// (worker.rs serve_cleanup:361-367 and the proxy loops worker.rs:142,
// worker.rs:399): the cmd binary repeats the value because the
// control surface exposes no sleep step.
const pollInterval = time.Millisecond

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

// setUnreadableSourcePages records one request's unreadable source
// pages into the worker session state before the domain machine runs
// (Rust worker.rs:316-333 set_unreadable_source_pages into the
// UNREADABLE_SOURCE_PAGES thread-local; the sorted, duplicate-free
// list lives in the internal/mapping leaf, reached through the
// internal/worker fault-memory seam, and this driver is the only
// writer). A duplicate is refused with the verbatim Rust
// InvalidArgument class.
func setUnreadableSourcePages(pages []uint32) error {
	return worker.SetSourceUnreadablePages(pages)
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
		problem := worker.WireProblemOf(guard.LastProblem())
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
		problem := worker.WireProblemOf(guard.LastProblem())
		retained = &problem
	}
	outcome := &worker.RecoveryOutcome{Result: result, Failure: failure}
	if err := worker.WriteRecoveryOutcome(control, outcome, retained); err != nil {
		return nil, err
	}
	return guard, nil
}

// runCleanup serves one CleanupRecoveryAttempt session (Rust
// worker/cleanup.rs run_worker:14-27): the cleanup request is decoded
// through the 4-11A codec, and the secured-attempt discard runs through
// the exported publication seam over the exact Rust three arms (present
// -> discard_attempt, proven absent -> confirmed_absent, resume failure
// -> failed_attempt with the Problem::output fold). Rust cleanup does
// not call set_unreadable_source_pages (worker.rs:416 installs the
// fault memory only for inspect/validate/recover), so no fault memory
// is armed here. The scratch cleanup stays the recorded 4-10 deferral
// (CleanupCheckpoint), then the result is written and the session
// returns without a cleanup guard (Rust returns Ok(None)).
func runCleanup(control *worker.Control) error {
	request, err := worker.ReadCleanupRequest(control)
	if err != nil {
		return err
	}
	discarded := publication.DiscardSecuredAttempt(request.DestinationPath, &request.Output)
	facts := worker.WireEarlyDiscardOf(discarded)
	scratch := worker.CleanupCheckpoint(request.Scratch)
	return worker.WriteCleanupResult(control, &facts, scratch)
}

// serveCleanup drives the retained source cleanup guard (Rust worker.rs
// serve_cleanup): it waits for each CleanupRequest, retries the guard,
// writes the cleanup result, and repeats after a failed retry. A
// completed cleanup clears guard pending and returns; a dead parent
// ends the loop gracefully (Rust parity).
func serveCleanup(control *worker.Control, guard *recovery.RecoverySourceCleanupGuard) error {
	for {
		// Rust worker.rs serve_cleanup:361-367 spins with only the
		// parent-liveness bound (no deadline): a retained guard may be
		// released at any later time and the worker must still serve
		// it. The control WaitFor 30 s limit is NOT used here.
		for control.State() != worker.StateCleanupRequest {
			if !control.ParentAlive() {
				return nil
			}
			time.Sleep(pollInterval)
		}
		complete, problem := guard.RetryCleanup()
		if complete {
			if err := worker.WriteValidationCleanupResult(control, true, nil); err != nil {
				return err
			}
			control.SetGuardPending(false)
			control.SetState(worker.StateCleanupResult)
			return nil
		}
		wire := worker.WireProblemOf(problem)
		if err := worker.WriteValidationCleanupResult(control, false, &wire); err != nil {
			return err
		}
		control.SetState(worker.StateCleanupResult)
	}
}

// waitAcknowledgement spins until the parent acknowledges one streamed
// callback by returning the control to Running (Rust proxy loops: the
// callback state is left, then the response word decides). Parent death
// maps to Cancelled exactly like the Rust proxy arms; the spin has no
// deadline (a parent sink may take arbitrarily long on one finding).
func waitAcknowledgement(control *worker.Control) error {
	// Rust worker.rs ValidationProxy:399-404 / RecoveryProxy:142-147
	// spin on the callback state with only the parent-liveness bound
	// (no deadline): a parent sink may take arbitrarily long on one
	// finding and the worker must wait. The control WaitFor 30 s limit
	// is NOT used here; a dead parent maps to Cancelled exactly like
	// the Rust proxy arms.
	for control.State() != worker.StateRunning {
		if !control.ParentAlive() {
			return &format.Error{Code: format.CodeCancelled, Detail: "SDK worker parent exited"}
		}
		time.Sleep(pollInterval)
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
	p.control.SetState(worker.StateFinding)
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
	p.control.SetState(worker.StateUnknown)
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
