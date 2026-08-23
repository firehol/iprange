// Canonical live-coordination replacement while quiescent (Rust
// live_lifecycle/transition.rs reset_live_coordination): the main file
// is locked and proven exactly like initialize, the canonical sidecar
// identity is captured, the new sidecar is prepared at the private
// .readers.reset name, the previous coordination is re-verified, and
// the prepared sidecar is installed at the canonical name with the
// selected policy (rollback-safe atomic exchange or discarding
// replacement). Every step is crash- and cancellation-checkpointed at
// the exact Rust points; any failure removes the created sidecar
// identity-guarded and reports the factual state.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// ResetLiveCoordination replaces missing, corrupt, or obsolete live
// coordination while the main is quiescent (Rust
// live_lifecycle::reset_live_coordination / transition.rs): the main
// file is opened read-write and locked for its lifetime, its committed
// generation is proven with the exact committed length, and a fresh
// sidecar is prepared privately and installed at the canonical .readers
// name with the selected guarantee. RollbackSafe requires the atomic
// name exchange when existing coordination is replaced (Rust
// require_exchange_available; the exchange is linux/apple only).
// check, when non-nil, is the cancellation checkpoint. Capacity-zero
// and every open/proof failure are hard errors; later failures return
// a LiveTransitionResult with the factual state.
func ResetLiveCoordination(path string, readerCapacity uint32, policy LiveResetPolicy, check func() error) (*LiveTransitionResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := requireCapacity(readerCapacity); err != nil {
		return nil, err
	}
	main, err := openLockedMain(path, check)
	if err != nil {
		return nil, err
	}
	defer main.file.Close()
	canonical, err := canonicalSidecarPath(main.path)
	if err != nil {
		return nil, err
	}
	private, err := liveTransitionTemp(main.path)
	if err != nil {
		return nil, err
	}
	previous, err := existingIdentity(canonical)
	if err != nil {
		return nil, err
	}
	if previous != nil && policy == LiveResetRollbackSafe && !mapping.ExchangeAvailable() {
		return nil, &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "rollback-safe live reset requires atomic name exchange"}
	}
	attempt, err := newTransitionAttempt(LiveTransitionReset, &policy, main, private, readerCapacity, previous)
	if err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	sidecar, failure := reserveAt(private, attempt.databaseID, attempt.sidecarID, readerCapacity)
	if failure != nil {
		return attempt.reservationFailure(*failure), nil
	}
	// The prepared sidecar descriptor is owned by this transition (Rust
	// drops the Sidecar when reset_live_coordination returns); every
	// return closes it after the install and path-level cleanup.
	defer sidecar.Close()
	identity := sidecar.localIdentity()
	if err := checkpoint(check); err != nil {
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationPrivate), nil
	}
	if err := prepareResetSidecar(main, sidecar, check); err != nil {
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationPrivate), nil
	}
	if err := verifyPrevious(canonical, previous); err != nil {
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationPrivate), nil
	}

	fault.Crash("live_reset.before_replace")
	if err := install(sidecar.path, canonical, sidecar.file, identity, previous, policy); err != nil {
		if residuePossible(err) {
			return attempt.unknown(&identity, LiveCoordinationLocationUnclassified, err), nil
		}
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationPrivate), nil
	}
	fault.Crash("live_reset.after_replace")
	switch cause, err := finishReset(main, sidecar, canonical, identity, previous, policy); {
	case err != nil:
		return attempt.unknown(&identity, LiveCoordinationLocationCanonical, err), nil
	case cause != nil:
		return attempt.initializedWithResidue(&identity, cause), nil
	default:
		return attempt.initialized(&identity), nil
	}
}

// unknown reports a transition whose outcome cannot be proven (Rust
// Attempt::unknown): the sidecar identity stays retained at the given
// location with residue facts.
func (a liveTransitionAttempt) unknown(identity *FileIdentity, location LiveCoordinationLocation, cause error) *LiveTransitionResult {
	return a.result(LiveTransitionStatusOutcomeUnknown, identity, location, residueFacts(cause))
}

// initializedWithResidue reports an installed reset whose final
// cleanup failed (Rust Attempt::initialized_with_residue): the
// transition is Initialized at the canonical location with residue
// facts.
func (a liveTransitionAttempt) initializedWithResidue(identity *FileIdentity, cause error) *LiveTransitionResult {
	return a.result(LiveTransitionStatusInitialized, identity, LiveCoordinationLocationCanonical, residueFacts(cause))
}

// prepareResetSidecar runs the reset sidecar steps and crash points
// between each durability step (Rust transition::
// prepare_reset_sidecar: creating, ready, private parent sync, main
// verify).
func prepareResetSidecar(main *lockedMain, sidecar *Sidecar, check func() error) error {
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := sidecar.initializeCreating(); err != nil {
		return err
	}
	fault.Crash("live_reset.after_creating_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := sidecar.publishReady(); err != nil {
		return err
	}
	fault.Crash("live_reset.after_ready_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := syncParent(sidecar.path); err != nil {
		return err
	}
	fault.Crash("live_reset.after_private_parent_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	return main.verify()
}

// finishReset proves the installed canonical sidecar and retires the
// previous one per policy (Rust transition::finish_reset): the parent
// directory sync, main verify, canonical identity re-check, header
// re-check, then RollbackSafe removes the exact previous inode from the
// private name (returning the failure as residue) or DiscardPrevious
// requires the private name absent (a leftover is CleanupConflict).
func finishReset(main *lockedMain, sidecar *Sidecar, canonical string, identity FileIdentity, previous *FileIdentity, policy LiveResetPolicy) (error, error) {
	if err := syncParent(canonical); err != nil {
		return nil, err
	}
	fault.Crash("live_reset.after_directory_sync")
	if err := main.verify(); err != nil {
		return nil, err
	}
	if err := verifyPath(canonical, identity); err != nil {
		return nil, err
	}
	if err := sidecar.verifyHeader(); err != nil {
		return nil, err
	}
	if previous != nil && policy == LiveResetRollbackSafe {
		if err := removeExactResult(sidecar.path, *previous); err != nil {
			return err, nil
		}
		return nil, nil
	}
	present, err := pathIdentity(sidecar.path)
	if err != nil {
		return nil, err
	}
	if present != nil {
		return nil, &format.Error{Code: format.CodeCleanupConflict, Detail: "discarding reset retained an unexpected private sidecar"}
	}
	return nil, nil
}

// existingIdentity reports the identity of the canonical sidecar when
// it is one regular single-link file (Rust transition::
// existing_identity = live_namespace::path_identity).
func existingIdentity(path string) (*FileIdentity, error) {
	return pathIdentity(path)
}

// verifyPrevious re-proves the previous canonical coordination before
// installation (Rust transition::verify_previous): an expected identity
// must still name the canonical path; an absent expectation requires
// the canonical name still absent (a fresh sidecar is CleanupConflict).
func verifyPrevious(path string, previous *FileIdentity) error {
	if previous != nil {
		return verifyPath(path, *previous)
	}
	present, err := pathIdentity(path)
	if err != nil {
		return err
	}
	if present != nil {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical sidecar appeared during reset"}
	}
	return nil
}

// removeExactResult removes the path only when it still names the
// retained identity and folds the cleanup outcome into one error (Rust
// transition::remove_exact over live_namespace::remove_exact).
func removeExactResult(path string, expected FileIdentity) error {
	outcome := removeExact(path, expected)
	if outcome.cause != nil {
		return outcome.cause
	}
	return nil
}

// residuePossible reports whether one error class implies possible
// residue (Rust Error::residue_possible: only the cleanup-incomplete
// class survives the operation).
func residuePossible(err error) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == format.CodeCleanupInProgress
}
