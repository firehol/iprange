// Restart recovery for live transitions whose in-memory result was
// lost (Rust live_lifecycle/residue.rs resolve_interrupted_live_transition):
// the retained main and coordination names are observed directly, the
// canonical/private matrix is classified, and the residue is
// completed, retired, or reported ready with the factual status. This
// operation performs only bootstrap and coordination checks; it never
// walks or validates the database page graph.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// LiveResidueKind is the location of an interrupted live-coordination
// artifact (Rust live_lifecycle::LiveResidueKind).
type LiveResidueKind uint8

const (
	// LiveResidueKindCanonical: the canonical .readers sidecar.
	LiveResidueKindCanonical LiveResidueKind = iota
	// LiveResidueKindPrivateReset: the private .readers.reset sidecar.
	LiveResidueKindPrivateReset
)

// LiveResidueStatus is the factual terminal state of resultless
// transition recovery (Rust live_lifecycle::LiveResidueStatus).
type LiveResidueStatus uint8

const (
	LiveResidueStatusAbsent LiveResidueStatus = iota
	LiveResidueStatusReady
	LiveResidueStatusCompleted
	LiveResidueStatusRemoved
	LiveResidueStatusOutcomeUnknown
)

// LiveResidueResult is the facts recovered directly from the retained
// main and sidecar (Rust live_lifecycle::LiveResidueResult). Identity
// and header facts are nil when the corresponding artifact does not
// exist or its header cannot be read.
type LiveResidueResult struct {
	Status              LiveResidueStatus
	Kind                *LiveResidueKind
	DatabaseID          *[16]byte
	SidecarID           *[16]byte
	ReaderCapacity      *uint32
	MainIdentity        *FileIdentity
	SidecarIdentity     *FileIdentity
	ResiduePossible     bool
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

type observedResidueKind uint8

const (
	residueAbsent observedResidueKind = iota
	residueValid
	residueMalformed
)

// observedResidue is one observed coordination artifact (Rust
// residue::Observed): absent, a valid open sidecar with its state, or
// a malformed artifact with its retained descriptor.
type observedResidue struct {
	kind     observedResidueKind
	sidecar  *Sidecar
	state    sidecarState
	file     *os.File
	identity FileIdentity
}

// residues is the observed canonical and private coordination pair of
// one interrupted transition (Rust residue::Residues).
type residues struct {
	canonicalPath string
	canonical     observedResidue
	privatePath   string
	private       observedResidue
}

// ResolveInterruptedLiveTransition resolves one interrupted canonical
// create/initialize or private reset without the lost in-memory result
// (Rust resolve_interrupted_live_transition): the retained main is
// opened under the lifetime lock when present, the canonical and
// private coordination names are observed, and the artifact matrix is
// completed, retired, or reported ready per mode. check, when non-nil,
// is the cancellation checkpoint.
func ResolveInterruptedLiveTransition(path string, mode LiveTransitionResolutionMode, check func() error) (*LiveResidueResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	directoryIdentity, err := parentIdentity(path)
	if err != nil {
		return nil, err
	}
	main, err := openMain(path, check)
	if err != nil {
		return nil, err
	}
	// The locked main descriptor is owned by this resolution (Rust
	// drops the LockedMain when resolve_interrupted_live_transition
	// returns); close it on every path, including the failure paths of
	// the later observations.
	if main != nil {
		defer main.file.Close()
	}
	canonicalPath, err := canonicalSidecarPath(path)
	if err != nil {
		return nil, err
	}
	privatePath, err := liveTransitionTemp(path)
	if err != nil {
		return nil, err
	}
	canonical, err := observeResidue(canonicalPath)
	if err != nil {
		return nil, err
	}
	// The observed canonical residue descriptor is owned by this
	// resolution (Rust drops the observed residue when
	// resolve_interrupted_live_transition returns); close it on every
	// path, including the failure paths of the later observations.
	defer func() {
		if canonical.sidecar != nil {
			canonical.sidecar.Close()
		}
		if canonical.file != nil {
			canonical.file.Close()
		}
	}()
	private, err := observeResidue(privatePath)
	if err != nil {
		return nil, err
	}
	// The observed private residue descriptor is owned by this
	// resolution; close it after the resolution work on every path.
	defer func() {
		if private.sidecar != nil {
			private.sidecar.Close()
		}
		if private.file != nil {
			private.file.Close()
		}
	}()
	residue := residues{
		canonicalPath: canonicalPath,
		canonical:     canonical,
		privatePath:   privatePath,
		private:       private,
	}
	if main == nil {
		return resolveWithoutMain(path, directoryIdentity, residue, mode, check)
	}
	return resolveWithMain(main, residue, mode, check)
}

// resolveWithoutMain retires the sole coordination residue when no main
// exists (Rust residue::resolve_without_main): both names present is a
// conflict, completion without a main is unresolvable, and the rollback
// removes the artifact identity-guarded with the parent identity
// re-proven.
func resolveWithoutMain(path string, directoryIdentity FileIdentity, residue residues, mode LiveTransitionResolutionMode, check func() error) (*LiveResidueResult, error) {
	if residue.canonical.kind != residueAbsent && residue.private.kind != residueAbsent {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "canonical and private live residues both exist without a main"}
	}
	var kind LiveResidueKind
	var residuePath string
	var observed observedResidue
	switch {
	case residue.canonical.kind == residueAbsent && residue.private.kind == residueAbsent:
		return absentResult(), nil
	case residue.private.kind == residueAbsent:
		kind = LiveResidueKindCanonical
		residuePath = residue.canonicalPath
		observed = residue.canonical
	default:
		kind = LiveResidueKindPrivateReset
		residuePath = residue.privatePath
		observed = residue.private
	}
	if mode == LiveTransitionResolutionComplete {
		return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "a live coordination residue has no main to complete"}
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	current, err := parentIdentity(path)
	if err != nil {
		return nil, err
	}
	if current != directoryIdentity {
		return nil, &format.Error{Code: format.CodeDirectoryIdentityMismatch}
	}
	facts := artifactFacts(kind, nil, &observed)
	cleanup := retireObserved(residuePath, &observed)
	switch found, err := parentIdentity(path); {
	case err != nil:
		cleanup.absorb(cleanupOutcomeFailed(err))
	case found != directoryIdentity:
		cleanup.absorb(cleanupOutcomeFailed(&format.Error{Code: format.CodeDirectoryIdentityMismatch}))
	}
	return afterRemoval(facts, cleanup), nil
}

// resolveWithMain classifies the canonical/private matrix against the
// locked main (Rust residue::resolve_with_main): ready canonical
// coordination reports Ready, a creating canonical sidecar is completed
// (or refused under rollback), a ready private reset is installed or
// removed per mode, and malformed artifacts are unresolvable or retired
// per mode.
func resolveWithMain(main *lockedMain, residue residues, mode LiveTransitionResolutionMode, check func() error) (*LiveResidueResult, error) {
	canonical, private := residue.canonical, residue.private
	switch {
	case canonical.kind == residueAbsent && private.kind == residueAbsent:
		return absentWithMain(main), nil

	case canonical.kind == residueValid && canonical.state == stateReady && private.kind == residueAbsent:
		if err := requireDatabase(main, canonical.sidecar); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		return readyResult(main, LiveResidueKindCanonical, canonical.sidecar), nil

	case canonical.kind == residueValid && canonical.state == stateReady:
		if err := requireDatabase(main, canonical.sidecar); err != nil {
			return nil, err
		}
		cleanup, err := removePrivateResidue(main, residue.privatePath, &private, check)
		if err != nil {
			return nil, err
		}
		return withCleanup(completedResult(main, LiveResidueKindPrivateReset, canonical.sidecar), cleanup, true), nil

	case canonical.kind == residueValid && canonical.state == stateCreating && private.kind == residueAbsent:
		if err := requireDatabase(main, canonical.sidecar); err != nil {
			return nil, err
		}
		if mode == LiveTransitionResolutionRollback {
			return nil, &format.Error{Code: format.CodeConflict, Detail: "resultless rollback cannot prove ownership of the valid main"}
		}
		return completeCanonical(main, canonical.sidecar, check)

	case canonical.kind == residueAbsent && private.kind == residueValid:
		if err := requireDatabase(main, private.sidecar); err != nil {
			return nil, err
		}
		switch {
		case mode == LiveTransitionResolutionComplete && private.state == stateReady:
			return completePrivateReset(main, residue.canonicalPath, residue.privatePath, private.sidecar, check)
		case mode == LiveTransitionResolutionComplete:
			return nil, &format.Error{Code: format.CodeConflict, Detail: "private reset sidecar is not ready"}
		default:
			return removeValidPrivate(main, residue.privatePath, private.sidecar, private.state, check)
		}

	case private.kind == residueValid && mode == LiveTransitionResolutionRollback:
		if err := requireDatabase(main, private.sidecar); err != nil {
			return nil, err
		}
		return removeValidPrivate(main, residue.privatePath, private.sidecar, private.state, check)

	case private.kind == residueMalformed && mode == LiveTransitionResolutionRollback:
		if err := checkpoint(check); err != nil {
			return nil, err
		}
		if err := main.verify(); err != nil {
			return nil, err
		}
		facts := artifactFacts(LiveResidueKindPrivateReset, &main.identity, &private)
		cleanup := retireObserved(residue.privatePath, &private)
		if err := main.verify(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		return afterRemoval(facts, cleanup), nil

	case private.kind == residueValid && private.state == stateCreating:
		return nil, &format.Error{Code: format.CodeConflict, Detail: "private reset sidecar is not ready"}

	case private.kind == residueMalformed:
		return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "private reset sidecar is malformed"}

	case canonical.kind == residueMalformed && private.kind == residueAbsent:
		return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "canonical live coordination is malformed; explicit reset is required"}

	default:
		return nil, &format.Error{Code: format.CodeConflict, Detail: "live transition residue conflicts with canonical coordination"}
	}
}

// completeCanonical finishes a creating canonical sidecar under the
// exclusive gate (Rust residue::complete_canonical: verify, sync, state
// proof, publish ready, parent syncs, re-verify; the gate unlock folds
// through finishWithCleanup).
func completeCanonical(main *lockedMain, sidecar *Sidecar, check func() error) (*LiveResidueResult, error) {
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return nil, err
	}
	completed := func() error {
		if err := checkpoint(check); err != nil {
			return err
		}
		if err := main.verify(); err != nil {
			return err
		}
		if err := requireState(sidecar, stateCreating); err != nil {
			return err
		}
		if err := main.file.Sync(); err != nil {
			return &format.Error{Code: format.CodeIO, Detail: "sync: " + err.Error()}
		}
		if err := syncParent(main.path); err != nil {
			return err
		}
		if err := sidecar.publishReady(); err != nil {
			return err
		}
		if err := syncParent(sidecar.path); err != nil {
			return err
		}
		if err := main.verify(); err != nil {
			return err
		}
		if err := sidecar.verifyPath(); err != nil {
			return err
		}
		return sidecar.verifyHeader()
	}
	if err := finishWithCleanup(completed(), sidecar.unlockGate()); err != nil {
		return nil, err
	}
	return completedResult(main, LiveResidueKindCanonical, sidecar), nil
}

// completePrivateReset installs a ready private reset sidecar at the
// canonical name (Rust residue::complete_private_reset: canonical
// absence, state proof, no-replace install, parent sync, verify, header
// check under the exclusive gate).
func completePrivateReset(main *lockedMain, canonicalPath, privatePath string, sidecar *Sidecar, check func() error) (*LiveResidueResult, error) {
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return nil, err
	}
	completed := func() error {
		if err := checkpoint(check); err != nil {
			return err
		}
		if err := main.verify(); err != nil {
			return err
		}
		if err := requireAbsentResidue(canonicalPath); err != nil {
			return err
		}
		if err := requireState(sidecar, stateReady); err != nil {
			return err
		}
		if err := install(privatePath, canonicalPath, sidecar.file, sidecar.localIdentity(), nil, LiveResetRollbackSafe); err != nil {
			return err
		}
		if err := syncParent(canonicalPath); err != nil {
			return err
		}
		if err := main.verify(); err != nil {
			return err
		}
		if err := verifyPath(canonicalPath, sidecar.localIdentity()); err != nil {
			return err
		}
		return sidecar.verifyHeader()
	}
	if err := finishWithCleanup(completed(), sidecar.unlockGate()); err != nil {
		return nil, err
	}
	return completedResult(main, LiveResidueKindPrivateReset, sidecar), nil
}

// removeValidPrivate removes a private reset sidecar under the
// exclusive gate (Rust residue::remove_valid_private: state proof,
// identity-guarded removal, main re-verify, gate unlock; every failure
// absorbs into the cleanup outcome).
func removeValidPrivate(main *lockedMain, path string, sidecar *Sidecar, state sidecarState, check func() error) (*LiveResidueResult, error) {
	residue := observedResidue{kind: residueValid, sidecar: sidecar, state: state}
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return nil, err
	}
	prepared := func() error {
		if err := checkpoint(check); err != nil {
			return err
		}
		if err := main.verify(); err != nil {
			return err
		}
		return requireState(sidecar, state)
	}
	if err := prepared(); err != nil {
		return nil, combineErrors(err, sidecar.unlockGate())
	}
	facts := artifactFacts(LiveResidueKindPrivateReset, &main.identity, &residue)
	cleanup := retireObserved(path, &residue)
	if err := main.verify(); err != nil {
		cleanup.absorb(cleanupOutcomeFailed(err))
	}
	if err := sidecar.unlockGate(); err != nil {
		cleanup.absorb(cleanupOutcomeFailed(err))
	}
	return afterRemoval(facts, cleanup), nil
}

// removePrivateResidue removes the private residue beside a ready
// canonical sidecar (Rust residue::remove_private_residue: valid
// sidecars under the exclusive gate, malformed artifacts retired
// directly).
func removePrivateResidue(main *lockedMain, path string, residue *observedResidue, check func() error) (cleanupOutcome, error) {
	switch residue.kind {
	case residueAbsent:
		return cleanupOutcome{}, nil
	case residueValid:
		sidecar := residue.sidecar
		if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
			return cleanupOutcome{}, err
		}
		prepared := func() error {
			if err := checkpoint(check); err != nil {
				return err
			}
			if err := main.verify(); err != nil {
				return err
			}
			return requireState(sidecar, residue.state)
		}
		if err := prepared(); err != nil {
			return cleanupOutcome{}, combineErrors(err, sidecar.unlockGate())
		}
		cleanup := retireObserved(path, residue)
		if err := main.verify(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		if err := sidecar.unlockGate(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		return cleanup, nil
	default:
		if err := checkpoint(check); err != nil {
			return cleanupOutcome{}, err
		}
		if err := main.verify(); err != nil {
			return cleanupOutcome{}, err
		}
		cleanup := retireObserved(path, residue)
		if err := main.verify(); err != nil {
			cleanup.absorb(cleanupOutcomeFailed(err))
		}
		return cleanup, nil
	}
}

// openMain opens the retained main under the lifetime lock when it
// exists (Rust residue::open_main: path_identity absence short-circuits).
func openMain(path string, check func() error) (*lockedMain, error) {
	identity, err := pathIdentity(path)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, nil
	}
	return openLockedMain(path, check)
}

// observeResidue opens one coordination path without requiring a
// database identity (Rust residue::observe: open_any classifies the
// Format/Corrupt/WrongState family as malformed with the retained
// descriptor).
func observeResidue(path string) (observedResidue, error) {
	identity, err := pathIdentity(path)
	if err != nil {
		return observedResidue{}, err
	}
	if identity == nil {
		return observedResidue{kind: residueAbsent}, nil
	}
	sidecar, state, err := openAny(path)
	if err == nil {
		return observedResidue{kind: residueValid, sidecar: sidecar, state: state}, nil
	}
	if isMalformedClass(err) {
		file, id, openErr := openRw(path)
		if openErr != nil {
			return observedResidue{}, openErr
		}
		return observedResidue{kind: residueMalformed, file: file, identity: id}, nil
	}
	return observedResidue{}, err
}

// retireObserved removes one observed coordination artifact
// identity-guarded (Rust residue::retire_observed: the valid sidecar
// uses its header sidecar id, the malformed artifact draws a fresh
// cleanup attempt id and runs the authenticated GC transition on
// windows).
func retireObserved(path string, residue *observedResidue) cleanupOutcome {
	switch residue.kind {
	case residueAbsent:
		return cleanupOutcome{}
	case residueValid:
		return removeCoordinated(path, residue.sidecar.file, residue.sidecar.localIdentity(), cleanupAuthority{
			attemptID:     residue.sidecar.header.sidecarID,
			ordinal:       1,
			kind:          ArtifactOwnedCoordination,
			directoryRole: DirectoryRoleMainFile,
		})
	default:
		attemptID, err := freshCleanupAttempt(path, residue.identity, 1, ArtifactOwnedCoordination, DirectoryRoleMainFile)
		if err != nil {
			return cleanupOutcomeFailed(err)
		}
		return removeCoordinated(path, residue.file, residue.identity, cleanupAuthority{
			attemptID:     attemptID,
			ordinal:       1,
			kind:          ArtifactOwnedCoordination,
			directoryRole: DirectoryRoleMainFile,
		})
	}
}

// requireDatabase proves the sidecar belongs to the locked main's
// database (Rust residue::require_database).
func requireDatabase(main *lockedMain, sidecar *Sidecar) error {
	if sidecar.header.databaseID != main.bootstrap.Meta.DatabaseID {
		return &format.Error{Code: format.CodeConflict, Detail: "live residue belongs to a different database"}
	}
	return nil
}

// requireState proves the sidecar state and header are unchanged (Rust
// residue::require_state: current_header equality plus the path
// re-verification).
func requireState(sidecar *Sidecar, state sidecarState) error {
	current, header, err := sidecar.currentHeader()
	if err != nil {
		return err
	}
	if current != state || header != sidecar.header {
		return &format.Error{Code: format.CodeConflict, Detail: "live residue changed during resolution"}
	}
	return sidecar.verifyPath()
}

// requireAbsentResidue requires the canonical coordination name absent
// (Rust residue::require_absent).
func requireAbsentResidue(path string) error {
	identity, err := pathIdentity(path)
	if err != nil {
		return err
	}
	if identity != nil {
		return &format.Error{Code: format.CodeConflict, Detail: "canonical coordination appeared during resolution"}
	}
	return nil
}

// absentResult reports no artifact at all (Rust residue::absent).
func absentResult() *LiveResidueResult {
	return &LiveResidueResult{Status: LiveResidueStatusAbsent}
}

// absentWithMain reports no coordination beside the locked main (Rust
// residue::absent_with_main: the database id and main identity facts).
func absentWithMain(main *lockedMain) *LiveResidueResult {
	result := absentResult()
	result.DatabaseID = &main.bootstrap.Meta.DatabaseID
	result.MainIdentity = &main.identity
	return result
}

// readyResult reports a ready canonical sidecar (Rust residue::ready).
func readyResult(main *lockedMain, kind LiveResidueKind, sidecar *Sidecar) *LiveResidueResult {
	return sidecarResult(main, kind, sidecar, LiveResidueStatusReady)
}

// completedResult reports a completed sidecar (Rust
// residue::completed_result).
func completedResult(main *lockedMain, kind LiveResidueKind, sidecar *Sidecar) *LiveResidueResult {
	return sidecarResult(main, kind, sidecar, LiveResidueStatusCompleted)
}

// sidecarResult assembles the full sidecar facts of one result (Rust
// residue::sidecar_result).
func sidecarResult(main *lockedMain, kind LiveResidueKind, sidecar *Sidecar, status LiveResidueStatus) *LiveResidueResult {
	identity := sidecar.localIdentity()
	return &LiveResidueResult{
		Status:          status,
		Kind:            &kind,
		DatabaseID:      &sidecar.header.databaseID,
		SidecarID:       &sidecar.header.sidecarID,
		ReaderCapacity:  &sidecar.header.capacity,
		MainIdentity:    &main.identity,
		SidecarIdentity: &identity,
	}
}

// artifactFacts assembles the recoverable facts of one observed
// artifact (Rust residue::facts: the header facts when valid, the
// identity only when malformed).
func artifactFacts(kind LiveResidueKind, mainIdentity *FileIdentity, residue *observedResidue) *LiveResidueResult {
	result := &LiveResidueResult{
		Status: LiveResidueStatusAbsent,
		Kind:   &kind,
	}
	switch residue.kind {
	case residueAbsent:
	case residueValid:
		identity := residue.sidecar.localIdentity()
		result.DatabaseID = &residue.sidecar.header.databaseID
		result.SidecarID = &residue.sidecar.header.sidecarID
		result.ReaderCapacity = &residue.sidecar.header.capacity
		result.SidecarIdentity = &identity
	default:
		result.SidecarIdentity = &residue.identity
	}
	result.MainIdentity = mainIdentity
	return result
}

// afterRemoval folds one cleanup outcome into the removal facts (Rust
// residue::after_removal: a clean removal reports Removed, a failed one
// OutcomeUnknown with residue possible).
func afterRemoval(result *LiveResidueResult, cleanup cleanupOutcome) *LiveResidueResult {
	return withCleanup(result, cleanup, false)
}

// withCleanup folds one cleanup outcome into the result facts (Rust
// residue::with_cleanup: preserve_status keeps Ready/Completed on a
// clean outcome).
func withCleanup(result *LiveResidueResult, cleanup cleanupOutcome, preserveStatus bool) *LiveResidueResult {
	if cleanup.isClean() {
		if !preserveStatus {
			result.Status = LiveResidueStatusRemoved
		}
	} else {
		result.ResiduePossible = true
		if !preserveStatus {
			result.Status = LiveResidueStatusOutcomeUnknown
		}
	}
	result.Housekeeping = result.Housekeeping.merge(cleanup.housekeeping)
	result.VisibleHousekeeping = append(result.VisibleHousekeeping, cleanup.visible...)
	result.Cause = cleanup.cause
	return result
}
