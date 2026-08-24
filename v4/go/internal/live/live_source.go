// Live source-guard registration for one pinned live generation (Rust
// recovery/source_guard/live.rs LiveSource::open_current): the main
// file is mapped read-only under the shared lifetime lock, the
// committed generation is proven, the ready reader table is opened, the
// gate is held exclusive while the pair is re-proven and the slots are
// scanned, one reader slot is claimed (the register-like pin), and the
// pair is proven again before the gate is released. FinishCurrent
// re-locks the gate exclusive, re-proves the paths and the claimed
// slot, and releases in the Rust order (slot, gate, lifetime); any
// release failure folds to the retryable residue state. The snapshot
// builder consumes this source; the recovery-candidate variant is
// chunk 4-10 scope.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// liveRegistrationState is the physical slot-release progress of one
// live source (Rust source_guard/live.rs RegistrationState): the source
// moves one step per successful release action, and every failure keeps
// the exact state so a retried release continues from the failed step.
type liveRegistrationState uint8

const (
	liveRegistrationActive liveRegistrationState = iota
	liveRegistrationClearing
	liveRegistrationCleared
	liveRegistrationReleased
)

// LiveSourceEnd is the factual terminal of one live source (Rust
// SourceEnd): the folded cause and whether the release left
// coordination residue the caller must still release (Rust
// RecoverySourceCleanupGuard present). A nil Cause with Residue false
// is the clean terminal.
type LiveSourceEnd struct {
	Cause   error
	Residue bool
}

// OpenFailure is the failing terminal of one live source open (Rust
// SourceOpenFailure): the primary cause and whether the claimed-open
// unwind left coordination residue (Rust RecoverySourceCleanupGuard
// present, folded from the abandon release). The residue travels on the
// open error so the snapshot machine classifies the cleanup state
// exactly like the Rust api.rs guard field.
type OpenFailure struct {
	Cause   error
	Residue bool
}

func (e *OpenFailure) Error() string { return e.Cause.Error() }

func (e *OpenFailure) Unwrap() error { return e.Cause }

// LiveSource is one registered read-only source against one committed
// generation of a live database (Rust recovery/source_guard/live.rs
// LiveSource): the main mapping under the shared lifetime lock, the
// opened reader table, and one claimed slot. It is owned by one
// goroutine; methods must not run concurrently with each other or with
// the terminal release, exactly like the reader and the mapping owner.
type LiveSource struct {
	mapping  *mapping.Mapping
	core     *reader.ImmutableReader
	path     string
	identity FileIdentity
	sidecar  *Sidecar
	slot     uint32
	meta     format.Meta
	// candidateTxn and hasCandidate retain the recovery-candidate
	// binding of the candidate-open arm (Rust Option<RecoveryCandidate>);
	// the current-generation arm leaves them unset.
	candidateTxn uint64
	hasCandidate bool
	gateLocked   bool
	registration liveRegistrationState
	ownerPID     int
}

// OpenLiveSourceCurrent opens and registers the live snapshot source
// (Rust LiveSource::open_current through open_file, bind_current,
// open_sidecar_locked, prepare_claim, claim_prepared): the read-only
// open under the shared lifetime lock, the path identity, the committed
// generation bootstrap, the ready reader table, the exclusive gate, the
// pair and generation re-proof under the gate, the slot scan, one
// claimed reader slot, and the final pair proof. The gate is released
// before the source returns; the slot is held until FinishCurrent or
// ReleaseOnly. check, when non-nil, runs between every bounded step.
func OpenLiveSourceCurrent(path string, check func() error) (*LiveSource, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	// open_file: the mapping open takes the shared lifetime lock (Rust
	// lock_file_cancellable; the lock is not cancellation-checked in
	// Go, the same precedent as OpenLiveReader). The API-layer
	// cancellation check runs before the open like Rust's
	// lock_file_cancellable refusal position.
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	m, err := mapping.OpenLiveReader(path, nil)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*LiveSource, error) {
		m.Close()
		return nil, err
	}
	device, inode, err := m.FileIdentity()
	if err != nil {
		return fail(err)
	}
	identity := FileIdentity{device: device, inode: inode}
	// bind_current: verify the path, bootstrap the committed generation
	// in live-reader mode over a freshly sampled extent, and verify the
	// path again (Rust bind_current; bootstrap_file re-stats the
	// descriptor; require_main_available is a POSIX no-op, omitted like
	// every other Go live open; both path proofs map through
	// live_coordination).
	if err := verifyPath(path, identity); err != nil {
		return fail(liveCoordination(err))
	}
	core, err := reader.OpenLiveMapped(m)
	if err != nil {
		return fail(err)
	}
	meta := core.Meta()
	if err := verifyPath(path, identity); err != nil {
		return fail(liveCoordination(err))
	}
	// open_sidecar_locked: the sidecar open and gate lock failures are
	// coordination classes (Rust open_sidecar_locked maps both).
	sidecar, err := open(path, meta.DatabaseID)
	if err != nil {
		return fail(liveCoordination(err))
	}
	fail = func(err error) (*LiveSource, error) {
		sidecar.Close()
		m.Close()
		return nil, err
	}
	// open_sidecar_locked: the exclusive reader-table gate for the
	// claim window (Rust lock_gate_cancellable, coordination-mapped).
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return fail(liveCoordination(err))
	}
	unlockGate := func(err error) (*LiveSource, error) {
		return fail(combineErrors(err, sidecar.unlockGate()))
	}
	// prepare_claim: prove the pair, re-run the committed-generation
	// bind under the gate, prove the reader table belongs to the
	// generation, and scan the slots against it (Rust prepare_claim;
	// the live pair proofs and the slot scan map through
	// live_coordination, the database-identity mismatch stays the raw
	// changed-candidate class).
	if err := verifyPath(path, identity); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := sidecar.verifyPath(); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := sidecar.verifyHeader(); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := verifyPath(path, identity); err != nil {
		return unlockGate(liveCoordination(err))
	}
	// bind_current's bootstrap_file: re-select the committed generation
	// under the gate with a freshly sampled physical extent in the live
	// mode (Rust database_file::bootstrap_file(file, OpenMode::
	// LiveReader) re-stats the fd; the writer may have committed a
	// generation that grew or shrank after the snapshot open, and an
	// unpublished draft tail is legal). A failed selection propagates
	// unwrapped like every Go live open.
	physical, err := m.FileSize()
	if err != nil {
		return unlockGate(err)
	}
	if err := core.SelectRegisteredGeneration(physical); err != nil {
		return unlockGate(err)
	}
	if core.Meta().DatabaseID != sidecar.header.databaseID {
		return unlockGate(&format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "the selected recovery candidate changed"})
	}
	if err := verifyPath(path, identity); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := sidecar.scanAtMostCancellable(core.Meta().TxnID, check); err != nil {
		return unlockGate(liveCoordination(err))
	}
	// claim_prepared: resize the mapping to the selected committed
	// bytes, claim one reader slot under the held gate, then prove the
	// pair and the slot before releasing the gate (Rust claim_prepared
	// maps read_only_view over the selected meta page count, then
	// verify_live_claim).
	if err := m.Remap(core.Meta().PageCount * format.PageSize); err != nil {
		return unlockGate(err)
	}
	slot, err := sidecar.claimReaderCancellable(core.Meta().TxnID, check)
	if err != nil {
		return unlockGate(liveCoordination(err))
	}
	source := &LiveSource{
		mapping:      m,
		core:         core,
		path:         path,
		identity:     identity,
		sidecar:      sidecar,
		slot:         slot,
		meta:         core.Meta(),
		gateLocked:   true,
		registration: liveRegistrationActive,
		ownerPID:     currentPID,
	}
	// verify_live_claim: the pair, the header, and the claimed slot are
	// re-proven through live_coordination while the gate is still held;
	// the claim-unwind release runs through the Rust Claimed arm.
	if err := verifyPath(path, identity); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyPath(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyHeader(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyReader(slot, core.Meta().TxnID); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.unlockGate(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	source.gateLocked = false
	return source, nil
}

// releaseUnclaimed runs the Rust Claimed unwind: the claimed source is
// abandoned with the failure, folding the release result into the
// terminal. A failed release retains the residue state and surfaces it
// through the typed open failure (Rust SourceOpenFailure.guard); the
// gate is still held on every call, and the unlock failure nests
// through combineErrors.
func (s *LiveSource) releaseUnclaimed(cause error) (*LiveSource, error) {
	end := s.abandon(cause)
	return nil, &OpenFailure{Cause: end.Cause, Residue: end.Residue}
}

// Mapping returns the raw page mapping of the pinned generation for
// the validation sweep (the validation context reads pages through the
// mapping owner). The mapping is borrowed: it lives until the source
// release and must never be closed by the caller.
func (s *LiveSource) Mapping() *mapping.Mapping { return s.mapping }

// Core returns the logical reader core of the pinned generation (Rust
// source::reader over source.mapping + source.meta). The snapshot copy
// consumes the same cursor surface as the immutable source.
func (s *LiveSource) Core() *reader.ImmutableReader { return s.core }

// Meta returns the claimed committed generation (Rust Source::meta).
func (s *LiveSource) Meta() format.Meta { return s.meta }

// FileIdentity returns the retained device and inode of the mapped
// main file (Rust Source::identity; captured at open, and an open
// descriptor's identity is immutable, so the snapshot compare pays no
// re-stat).
func (s *LiveSource) FileIdentity() (device uint64, inode uint64, err error) {
	return s.identity.device, s.identity.inode, nil
}

// FinishCurrent proves the pinned generation survived the build and
// releases the source in the Rust order (Rust finish_current:
// final_check then release): the owner proof, the cancellation check,
// the exclusive gate re-lock, the meta equality, the path and header
// proofs, and the slot proof, then slot, gate, and lifetime release. A
// release failure folds to the residue terminal exactly like Rust
// terminal().
func (s *LiveSource) FinishCurrent(check func() error) LiveSourceEnd {
	checked := s.finalCheck(s.meta, check)
	released := s.release()
	return s.terminal(checked, released)
}

// FinishCandidate proves the pinned recovery candidate survived the
// build and releases the source (Rust Source::finish over the
// candidate-bound live arm: finalCheck with the used meta, then the
// slot-gate-lifetime release, folded to the terminal).
func (s *LiveSource) FinishCandidate(used format.Meta, check func() error) LiveSourceEnd {
	checked := s.finalCheck(used, check)
	released := s.release()
	return s.terminal(checked, released)
}

// Release runs the release steps without any final check (Rust
// LiveSource::release; the recovery cleanup guard retries a failed
// live release through this seam).
func (s *LiveSource) Release() error { return s.release() }

// ReleaseOnly releases the source without the final check (Rust
// release_only; the snapshot fail_source path).
func (s *LiveSource) ReleaseOnly() LiveSourceEnd {
	released := s.release()
	return s.terminal(nil, released)
}

// finalCheck re-proves the pinned generation before publication (Rust
// LiveSource::final_check): owner, cancellation, exclusive gate,
// unchanged claimed meta (and the retained recovery candidate
// transaction), main path, sidecar path and header, and the claimed
// slot still naming the generation.
func (s *LiveSource) finalCheck(used format.Meta, check func() error) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	if err := checkpoint(check); err != nil {
		return err
	}
	if s.meta != used || s.hasCandidate && s.candidateTxn != used.TxnID {
		return candidateChangedError()
	}
	if !s.gateLocked {
		if err := s.sidecar.lockGateCancellable(LockExclusive, check); err != nil {
			return liveCoordination(err)
		}
		s.gateLocked = true
	}
	// The snapshot passes the claimed meta, so the structural equality
	// is exact (Rust final_check: self.meta != used). The pair proofs
	// and the slot proof map through live_coordination (Rust
	// verify_live_paths + verify_reader.map_err(live_coordination)).
	if err := verifyPath(s.path, s.identity); err != nil {
		return liveCoordination(err)
	}
	if err := s.sidecar.verifyPath(); err != nil {
		return liveCoordination(err)
	}
	if err := s.sidecar.verifyHeader(); err != nil {
		return liveCoordination(err)
	}
	if err := s.sidecar.verifyReader(s.slot, s.meta.TxnID); err != nil {
		return liveCoordination(err)
	}
	return nil
}

// release releases the registration in the Rust order (source_guard/
// live.rs LiveSource::release): the owner proof, the slot clearing and
// unlocking under the exclusive gate, the gate release, and the
// lifetime lock and descriptor release through the mapping close. Each
// step keeps its state so a retried release continues from the failed
// step.
func (s *LiveSource) release() error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	// release_slot: the gate is already held by the final check; a
	// ReleaseOnly caller takes it now (Rust ensure_gate). Every slot
	// and gate coordination failure maps through live_coordination,
	// exactly like the Rust release steps.
	if s.registration != liveRegistrationReleased {
		if !s.gateLocked {
			if err := s.sidecar.lockGate(LockExclusive); err != nil {
				return liveCoordination(err)
			}
			s.gateLocked = true
		}
		if s.registration == liveRegistrationActive {
			s.registration = liveRegistrationClearing
		}
		if s.registration == liveRegistrationClearing {
			if err := s.sidecar.clearReader(s.slot); err != nil {
				return liveCoordination(err)
			}
			s.registration = liveRegistrationCleared
		}
		if s.registration == liveRegistrationCleared {
			if err := s.sidecar.unlockReader(s.slot); err != nil {
				return liveCoordination(err)
			}
			s.registration = liveRegistrationReleased
		}
	}
	// release_gate.
	if s.gateLocked {
		if err := s.sidecar.unlockGate(); err != nil {
			return liveCoordination(err)
		}
		s.gateLocked = false
	}
	// release_lifetime: the mapping close unmaps, releases the shared
	// lifetime lock, and closes the descriptor (Rust unlock_file then
	// drops; the candidate-bound source retains no reader core, so the
	// mapping close is the same lifetime release).
	if s.core != nil {
		if err := s.core.Close(); err != nil {
			return err
		}
	} else if err := s.mapping.Close(); err != nil {
		return err
	}
	s.sidecar.Close()
	return nil
}

// liveCoordination maps one path or sidecar proof failure to the Rust
// live_coordination class (recovery/source_guard.rs): Cancelled,
// ForkedHandle, and an already-coordination class keep their class;
// every other cause surfaces as LiveRecoveryCoordinationUnavailable.
func liveCoordination(cause error) error {
	if cause == nil {
		return nil
	}
	var fe *format.Error
	if errors.As(cause, &fe) &&
		(fe.Code == format.CodeCancelled || fe.Code == format.CodeForkedHandle || fe.Code == format.CodeLiveRecoveryCoordinationUnavailable) {
		return cause
	}
	return &format.Error{Code: format.CodeLiveRecoveryCoordinationUnavailable, Detail: cause.Error()}
}

// requireOwner proves the source is used by the process that opened it
// (Rust require_owner, Error::ForkedHandle). Go cannot fork, so the
// check is structural parity and can never fire.
func (s *LiveSource) requireOwner() error {
	if currentPID != s.ownerPID {
		return &format.Error{Code: format.CodeForkedHandle, Detail: "live source was opened by a different process"}
	}
	return nil
}

// terminal folds the final-check and release results exactly like Rust
// terminal(): a clean release keeps only the check failure; a failed
// release keeps the check failure (or the cleanup class when the check
// was clean) and reports residue possible. The fold is the shared
// terminalResult used by the validation bootstrap source too.
func (s *LiveSource) terminal(checked, released error) LiveSourceEnd {
	return terminalResult(checked, released)
}

// abandon releases the source without the final check (Rust
// Source::abandon; the claimed-open unwind).
func (s *LiveSource) abandon(cause error) LiveSourceEnd {
	released := s.release()
	return s.terminal(cause, released)
}

// CleanupForCause maps one release failure to the reported cause when
// no check failure exists (Rust cleanup_for_cause: ForkedHandle keeps
// its class, every other release failure is CleanupConflict "source
// recovery protection was not released"). The snapshot machine uses
// the same fold for the immutable mapping close.
func CleanupForCause(release error) error {
	var fe *format.Error
	if errors.As(release, &fe) && fe.Code == format.CodeForkedHandle {
		return release
	}
	return &format.Error{Code: format.CodeCleanupConflict, Detail: "source recovery protection was not released"}
}

// candidateChangedError is the fixed recovery-candidate-changed class
// (Rust candidate_changed; the live candidate bind uses the same
// mapping as the basic source).
func candidateChangedError() error {
	return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "recovery candidate changed"}
}

// Abandon releases the source without the final check (Rust
// Source::abandon; the claimed-open unwind and the failing recovery
// terminal).
func (s *LiveSource) Abandon(cause error) LiveSourceEnd {
	return s.abandon(cause)
}
