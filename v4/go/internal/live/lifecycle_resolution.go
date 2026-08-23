// Exact completion or rollback of one interrupted live transition
// (Rust live_lifecycle/resolution.rs resolve_live_transition): the
// supplied transition result identifies the exact attempt, the locked
// main is re-proven unchanged, the canonical and private coordination
// names are observed and classified, and the attempt is completed
// (publishing a creating sidecar or installing a prepared reset) or
// rolled back (identity-guarded removal) with the factual result.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// LiveTransitionResolutionMode is the requested terminal action for an
// exact interrupted transition (Rust
// live_lifecycle::LiveTransitionResolutionMode).
type LiveTransitionResolutionMode uint8

const (
	// LiveTransitionResolutionComplete finishes the interrupted
	// transition (Rust Complete).
	LiveTransitionResolutionComplete LiveTransitionResolutionMode = iota
	// LiveTransitionResolutionRollback removes the attempt artifacts
	// (Rust Rollback).
	LiveTransitionResolutionRollback
)

// observed is one coordination inode opened with the expected database
// identity (Rust resolution::Observed).
type observed struct {
	sidecar  *Sidecar
	state    sidecarState
	identity FileIdentity
}

type resetCanonicalKind uint8

const (
	resetCanonicalPrevious resetCanonicalKind = iota
	resetCanonicalAttempt
)

// resetCanonical is the classified canonical sidecar of one reset
// (Rust resolution::ResetCanonical): the previous inode or the new
// transition attempt.
type resetCanonical struct {
	kind     resetCanonicalKind
	identity FileIdentity // Previous payload
	attempt  *observed    // Attempt payload
}

type resetPrivateKind uint8

const (
	resetPrivatePrevious resetPrivateKind = iota
	resetPrivateAttempt
)

// resetPrivate is the classified private sidecar of one reset (Rust
// resolution::ResetPrivate).
type resetPrivate struct {
	kind     resetPrivateKind
	identity FileIdentity // Previous payload
	attempt  *observed    // Attempt payload
}

// ResolveLiveTransition resolves only the exact transition identified
// by supplied (Rust resolve_live_transition): the locked main must
// still match the supplied facts, the canonical and private
// coordination names are observed, and mode completes or rolls back
// the interrupted attempt. check, when non-nil, is the cancellation
// checkpoint. Validation and open failures are hard errors; the
// resolution outcome is the factual LiveTransitionResult.
func ResolveLiveTransition(path string, supplied *LiveTransitionResult, mode LiveTransitionResolutionMode, check func() error) (*LiveTransitionResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := requireSupplied(supplied); err != nil {
		return nil, err
	}
	main, err := openLockedMain(path, check)
	if err != nil {
		return nil, err
	}
	defer main.file.Close()
	if err := requireMain(main, supplied); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	canonicalPath, err := canonicalSidecarPath(main.path)
	if err != nil {
		return nil, err
	}
	privatePath, err := liveTransitionTemp(main.path)
	if err != nil {
		return nil, err
	}
	switch supplied.Operation {
	case LiveTransitionInitialize:
		canonical, err := observe(canonicalPath, supplied.DatabaseID)
		if err != nil {
			return nil, err
		}
		// The observed sidecar descriptors are owned by this resolution
		// (Rust drops the Observed values when resolve_live_transition
		// returns); close them after the resolution work, including the
		// failure paths of the later observations.
		if canonical != nil {
			defer canonical.sidecar.Close()
		}
		private, err := observe(privatePath, supplied.DatabaseID)
		if err != nil {
			return nil, err
		}
		if private != nil {
			defer private.sidecar.Close()
		}
		return resolveInitialize(main, supplied, canonical, private, mode)
	case LiveTransitionReset:
		canonical, err := observeResetCanonical(canonicalPath, supplied)
		if err != nil {
			return nil, err
		}
		// The observed attempt sidecar descriptors are owned by this
		// resolution (Rust drops them when resolve_live_transition
		// returns); close them after the resolution work, including the
		// failure paths of the later observations.
		defer func() {
			if canonical != nil && canonical.attempt != nil {
				canonical.attempt.sidecar.Close()
			}
		}()
		private, err := observeResetPrivate(privatePath, supplied)
		if err != nil {
			return nil, err
		}
		defer func() {
			if private != nil && private.attempt != nil {
				private.attempt.sidecar.Close()
			}
		}()
		return resolveReset(main, supplied, canonicalPath, canonical, privatePath, private, mode)
	default:
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "live transition result is incomplete"}
	}
}

// resolveInitialize completes or rolls back one interrupted
// initialize (Rust resolution::resolve_initialize): a private sidecar
// is unexpected, an absent canonical sidecar reports Unchanged/Absent,
// a ready canonical sidecar reports Initialized/Canonical, and a
// creating canonical sidecar is published or removed per mode.
func resolveInitialize(main *lockedMain, supplied *LiveTransitionResult, canonical, private *observed, mode LiveTransitionResolutionMode) (*LiveTransitionResult, error) {
	if private != nil {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "initialize transition has an unexpected private sidecar"}
	}
	if canonical == nil {
		return resolved(supplied, LiveTransitionStatusUnchanged, nil, LiveCoordinationLocationAbsent), nil
	}
	if err := requireAttempt(canonical, supplied); err != nil {
		return nil, err
	}
	switch {
	case canonical.state == stateReady:
		return resolved(supplied, LiveTransitionStatusInitialized, &canonical.identity, LiveCoordinationLocationCanonical), nil
	case mode == LiveTransitionResolutionComplete:
		if err := main.verify(); err != nil {
			return nil, err
		}
		if err := main.file.Sync(); err != nil {
			return nil, &format.Error{Code: format.CodeIO, Detail: "sync: " + err.Error()}
		}
		if err := syncParent(canonical.sidecar.path); err != nil {
			return nil, err
		}
		if err := canonical.sidecar.publishReady(); err != nil {
			return nil, err
		}
		if err := syncParent(canonical.sidecar.path); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		return resolved(supplied, LiveTransitionStatusInitialized, &canonical.identity, LiveCoordinationLocationCanonical), nil
	default:
		if err := main.verify(); err != nil {
			return nil, err
		}
		cleanup := cleanupAttempt(canonical.sidecar, supplied)
		if err := main.verify(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		return resolvedAfterCleanup(supplied, LiveTransitionStatusUnchanged, &canonical.identity, LiveCoordinationLocationAbsent, cleanup), nil
	}
}

// resolveReset completes or rolls back one interrupted reset (Rust
// resolution::resolve_reset): the canonical sidecar is classified as
// the previous inode or the new attempt, the private sidecar is
// classified the same way, and the attempt is installed, cleaned up,
// or refused per the policy and mode matrix.
func resolveReset(main *lockedMain, supplied *LiveTransitionResult, canonicalPath string, canonical *resetCanonical, privatePath string, private *resetPrivate, mode LiveTransitionResolutionMode) (*LiveTransitionResult, error) {
	if canonical != nil && canonical.kind == resetCanonicalAttempt {
		c := canonical.attempt
		if err := requireAttempt(c, supplied); err != nil {
			return nil, err
		}
		if c.state != stateReady {
			return nil, &format.Error{Code: format.CodeConflict, Detail: "completed reset sidecar is not ready"}
		}
		if supplied.ResetPolicy != nil && *supplied.ResetPolicy == LiveResetDiscardPrevious && mode == LiveTransitionResolutionRollback {
			return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "discarding reset cannot restore the previous sidecar"}
		}
		switch {
		case private != nil && private.kind == resetPrivatePrevious && supplied.ResetPolicy != nil && *supplied.ResetPolicy == LiveResetRollbackSafe:
			if err := removePrevious(privatePath, private.identity); err != nil {
				return nil, err
			}
		case private != nil && private.kind == resetPrivatePrevious:
			return nil, &format.Error{Code: format.CodeConflict, Detail: "discarding reset retained the previous sidecar"}
		case private != nil && private.kind == resetPrivateAttempt:
			return nil, &format.Error{Code: format.CodeConflict, Detail: "the reset attempt exists at both private and canonical names"}
		}
		if err := syncParent(canonicalPath); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		return resolved(supplied, LiveTransitionStatusInitialized, &c.identity, LiveCoordinationLocationCanonical), nil
	}
	if canonical == nil && supplied.PreviousSidecarIdentity != nil {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "the previous canonical sidecar disappeared"}
	}
	if private == nil {
		return resolved(supplied, LiveTransitionStatusUnchanged, supplied.NewSidecarIdentity, LiveCoordinationLocationAbsent), nil
	}
	if private.kind != resetPrivateAttempt {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "the previous sidecar is private before reset installation"}
	}
	p := private.attempt
	if err := requireAttempt(p, supplied); err != nil {
		return nil, err
	}
	if p.state != stateReady {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "reset private sidecar is not ready"}
	}
	switch mode {
	case LiveTransitionResolutionRollback:
		if err := main.verify(); err != nil {
			return nil, err
		}
		cleanup := cleanupAttempt(p.sidecar, supplied)
		if err := main.verify(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		return resolvedAfterCleanup(supplied, LiveTransitionStatusUnchanged, &p.identity, LiveCoordinationLocationAbsent, cleanup), nil
	default:
		previous, err := existingIdentity(canonicalPath)
		if err != nil {
			return nil, err
		}
		if err := requirePreviousIdentity(previous, supplied); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		if err := install(privatePath, canonicalPath, p.sidecar.file, p.sidecar.localIdentity(), previous, *supplied.ResetPolicy); err != nil {
			return nil, err
		}
		if err := syncParent(canonicalPath); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		if err := verifyPath(canonicalPath, p.sidecar.localIdentity()); err != nil {
			return nil, err
		}
		if err := p.sidecar.verifyHeader(); err != nil {
			return nil, err
		}
		if previous != nil && supplied.ResetPolicy != nil && *supplied.ResetPolicy == LiveResetRollbackSafe {
			if err := removePrevious(privatePath, *previous); err != nil {
				return nil, err
			}
		} else {
			present, err := existingIdentity(privatePath)
			if err != nil {
				return nil, err
			}
			if present != nil {
				return nil, &format.Error{Code: format.CodeConflict, Detail: "discarding reset retained an unexpected private sidecar"}
			}
		}
		return resolved(supplied, LiveTransitionStatusInitialized, &p.identity, LiveCoordinationLocationCanonical), nil
	}
}

// observe opens one coordination path with the expected database
// identity when it exists (Rust resolution::observe).
func observe(path string, databaseID [16]byte) (*observed, error) {
	identity, err := pathIdentity(path)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, nil
	}
	sidecar, state, err := openAt(path, databaseID)
	if err != nil {
		return nil, err
	}
	return &observed{sidecar: sidecar, state: state, identity: sidecar.localIdentity()}, nil
}

// observeResetCanonical classifies the canonical sidecar of one reset
// (Rust resolution::observe_reset_canonical): the previous inode when
// its identity matches the supplied previous identity, otherwise the
// transition attempt when the header matches, otherwise a conflict.
func observeResetCanonical(path string, supplied *LiveTransitionResult) (*resetCanonical, error) {
	identity, err := existingIdentity(path)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, nil
	}
	if supplied.PreviousSidecarIdentity != nil && *supplied.PreviousSidecarIdentity == *identity {
		return &resetCanonical{kind: resetCanonicalPrevious, identity: *identity}, nil
	}
	attempt, err := observe(path, supplied.DatabaseID)
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "canonical sidecar disappeared during transition inspection"}
	}
	if !isAttempt(attempt, supplied) {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "canonical sidecar is neither the old nor new transition inode"}
	}
	return &resetCanonical{kind: resetCanonicalAttempt, attempt: attempt}, nil
}

// observeResetPrivate classifies the private sidecar of one reset
// (Rust resolution::observe_reset_private).
func observeResetPrivate(path string, supplied *LiveTransitionResult) (*resetPrivate, error) {
	identity, err := existingIdentity(path)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, nil
	}
	if supplied.PreviousSidecarIdentity != nil && *supplied.PreviousSidecarIdentity == *identity {
		return &resetPrivate{kind: resetPrivatePrevious, identity: *identity}, nil
	}
	attempt, err := observe(path, supplied.DatabaseID)
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "private sidecar disappeared during transition inspection"}
	}
	if !isAttempt(attempt, supplied) {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "private sidecar belongs to another transition"}
	}
	return &resetPrivate{kind: resetPrivateAttempt, attempt: attempt}, nil
}

// requireSupplied validates the retained transition facts (Rust
// resolution::require_supplied): the nonzero identity draws, the
// operation/reset-policy consistency, and the outcome-unknown rule
// that the new sidecar identity must have been proven. Go models the
// directory and main identities as pointers (Rust carries value
// LocalFileIdentity, so absence is unrepresentable there); a nil draw
// is classified as a mismatch by requireMain, reusing the same error
// classes as a differing value. The Windows rollback-safe refusal is
// unreachable in Go because the live surface refuses before path
// access on Windows (lock_refuse.go), so it stays a parity comment.
func requireSupplied(supplied *LiveTransitionResult) error {
	if supplied.DatabaseID == [16]byte{} ||
		supplied.TransactionID == 0 ||
		supplied.CommitNonce == [16]byte{} ||
		supplied.ReaderCapacity == 0 ||
		supplied.SidecarID == [16]byte{} {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "live transition result is incomplete"}
	}
	switch {
	case supplied.Operation == LiveTransitionInitialize && supplied.ResetPolicy == nil:
	case supplied.Operation == LiveTransitionReset && supplied.ResetPolicy != nil:
	default:
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "live transition result has an inconsistent reset policy"}
	}
	if supplied.NewSidecarIdentity == nil && supplied.Status == LiveTransitionStatusOutcomeUnknown {
		return &format.Error{Code: format.CodeUnresolvable, Detail: "transition never proved its new sidecar identity"}
	}
	return nil
}

// requireMain re-proves the locked main against the supplied facts
// (Rust resolution::require_main: basename, directory identity, main
// identity, and the bootstrap generation).
func requireMain(main *lockedMain, supplied *LiveTransitionResult) error {
	if main.basename != supplied.MainBasename {
		return &format.Error{Code: format.CodeConflict, Detail: "live transition destination name changed"}
	}
	// Go models these identities as pointers (Rust carries value
	// LocalFileIdentity, so absence is unrepresentable there); a nil
	// draw is classified like a mismatch at the same step.
	if supplied.DirectoryIdentity == nil || main.directoryIdentity != *supplied.DirectoryIdentity {
		return &format.Error{Code: format.CodeDirectoryIdentityMismatch}
	}
	if supplied.MainIdentity == nil ||
		main.identity != *supplied.MainIdentity ||
		main.bootstrap.Meta.DatabaseID != supplied.DatabaseID ||
		main.bootstrap.Meta.TxnID != supplied.TransactionID ||
		main.bootstrap.Meta.CommitNonce != supplied.CommitNonce {
		return &format.Error{Code: format.CodeConflict, Detail: "live transition main identity or generation changed"}
	}
	return nil
}

// requireAttempt proves one observed sidecar is the exact transition
// attempt (Rust resolution::require_attempt: the header facts match
// and the proven identity equals the supplied new identity when one
// was proven).
func requireAttempt(attempt *observed, supplied *LiveTransitionResult) error {
	if !isAttempt(attempt, supplied) ||
		(supplied.NewSidecarIdentity != nil && *supplied.NewSidecarIdentity != attempt.identity) {
		return &format.Error{Code: format.CodeConflict, Detail: "coordination inode does not match the transition attempt"}
	}
	return nil
}

// isAttempt reports whether one observed sidecar header matches the
// supplied attempt facts (Rust resolution::is_attempt: database id,
// sidecar id, and reader capacity).
func isAttempt(attempt *observed, supplied *LiveTransitionResult) bool {
	return attempt.sidecar.header.databaseID == supplied.DatabaseID &&
		attempt.sidecar.header.sidecarID == supplied.SidecarID &&
		attempt.sidecar.header.capacity == supplied.ReaderCapacity
}

// requirePreviousIdentity proves the canonical coordination still names
// the supplied previous identity before reset installation (Rust
// resolution::require_previous_identity).
func requirePreviousIdentity(observed *FileIdentity, supplied *LiveTransitionResult) error {
	if observed != nil && supplied.PreviousSidecarIdentity != nil && *observed == *supplied.PreviousSidecarIdentity {
		return nil
	}
	if observed == nil && supplied.PreviousSidecarIdentity == nil {
		return nil
	}
	return &format.Error{Code: format.CodeConflict, Detail: "previous coordination identity changed before reset"}
}

// resolved assembles the factual result of a clean resolution (Rust
// resolution::resolved: the supplied facts, the new status, the new
// sidecar identity, the new location, residue impossible, no cause).
func resolved(supplied *LiveTransitionResult, status LiveTransitionStatus, newSidecarIdentity *FileIdentity, location LiveCoordinationLocation) *LiveTransitionResult {
	return &LiveTransitionResult{
		Operation:               supplied.Operation,
		ResetPolicy:             supplied.ResetPolicy,
		Status:                  status,
		DatabaseID:              supplied.DatabaseID,
		TransactionID:           supplied.TransactionID,
		CommitNonce:             supplied.CommitNonce,
		DirectoryIdentity:       supplied.DirectoryIdentity,
		MainIdentity:            supplied.MainIdentity,
		MainBasename:            supplied.MainBasename,
		ReaderCapacity:          supplied.ReaderCapacity,
		SidecarID:               supplied.SidecarID,
		PreviousSidecarIdentity: supplied.PreviousSidecarIdentity,
		NewSidecarIdentity:      newSidecarIdentity,
		NewSidecarLocation:      location,
		ResiduePossible:         false,
		Housekeeping:            supplied.Housekeeping,
		VisibleHousekeeping:     supplied.VisibleHousekeeping,
		Cause:                   nil,
	}
}

// cleanupAttempt removes one observed attempt sidecar identity-guarded
// (Rust resolution::cleanup_attempt with the supplied sidecar id as
// the cleanup authority).
func cleanupAttempt(sidecar *Sidecar, supplied *LiveTransitionResult) cleanupOutcome {
	return removeExact(sidecar.path, sidecar.localIdentity())
}

// resolvedAfterCleanup folds one cleanup outcome into the resolved
// facts (Rust resolution::resolved_after_cleanup: a clean removal
// reports the clean status at the clean location; a failed removal
// reports OutcomeUnknown at the supplied location with residue
// possible, merged housekeeping, and the cleanup cause).
func resolvedAfterCleanup(supplied *LiveTransitionResult, cleanStatus LiveTransitionStatus, newSidecarIdentity *FileIdentity, cleanLocation LiveCoordinationLocation, cleanup cleanupOutcome) *LiveTransitionResult {
	clean := cleanup.isClean()
	status, location := cleanStatus, cleanLocation
	if !clean {
		status, location = LiveTransitionStatusOutcomeUnknown, supplied.NewSidecarLocation
	}
	result := resolved(supplied, status, newSidecarIdentity, location)
	result.ResiduePossible = !clean
	result.Housekeeping = result.Housekeeping.merge(cleanup.housekeeping)
	result.VisibleHousekeeping = append(result.VisibleHousekeeping, cleanup.visible...)
	result.Cause = cleanup.cause
	return result
}

// removePrevious removes the exact previous sidecar inode from the
// private name (Rust resolution::remove_previous: the rollback-safe
// cleanup of the exchanged previous coordination).
func removePrevious(path string, identity FileIdentity) error {
	return removeExactResult(path, identity)
}
