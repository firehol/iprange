package live

// Recovery-candidate live source registration (Rust
// recovery/source_guard/live.rs LiveSource::open over
// open_candidate_locked): the same registration machinery as
// OpenLiveSourceCurrent with the selection bound to one exact newest
// recovery candidate token instead of the committed-current bind. The
// path identity must equal the token identity, the candidate is
// re-selected under the exclusive gate, and the final check re-proves
// the selected candidate transaction. The candidate-bound source
// retains no reader core: recovery reads pages through the mapping
// owner, and the mapping close is the lifetime release.

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// OpenLiveSourceCandidate opens and registers the live recovery source
// for one exact newest candidate (Rust LiveSource::open; the caller
// proves the candidate label before any path access). check, when
// non-nil, runs between every bounded step.
func OpenLiveSourceCandidate(path string, token bootstrap.RecoveryCandidateToken, check func() error) (*LiveSource, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	// open_file: the mapping open takes the shared lifetime lock (the
	// API-layer cancellation check runs before the open like Rust's
	// lock_file_cancellable refusal position).
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
	// bind_candidate: verify the path, select the exact candidate
	// (identity, pair classification, token match), and verify the
	// path again (Rust bind_candidate: verify_path, select,
	// require_main_available as the recorded POSIX no-op,
	// verify_path).
	meta, err := bindCandidateLive(m, path, identity, token, check)
	if err != nil {
		return fail(err)
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
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return fail(liveCoordination(err))
	}
	unlockGate := func(err error) (*LiveSource, error) {
		return fail(combineErrors(err, sidecar.unlockGate()))
	}
	// prepare_claim: prove the pair, re-run the candidate bind under
	// the gate, prove the reader table belongs to the selected
	// generation, and scan the slots against it (Rust prepare_claim
	// candidate arm).
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
	reselected, err := bindCandidateLive(m, path, identity, token, check)
	if err != nil {
		return unlockGate(err)
	}
	if reselected != meta || reselected.DatabaseID != sidecar.header.databaseID {
		return unlockGate(candidateChangedError())
	}
	if err := sidecar.scanAtMostCancellable(reselected.TxnID, check); err != nil {
		return unlockGate(liveCoordination(err))
	}
	// claim_prepared: resize the mapping to the selected committed
	// bytes, claim one reader slot under the held gate, then prove the
	// pair and the slot before releasing the gate (Rust
	// claim_prepared over the candidate meta, then verify_live_claim).
	if err := m.Remap(reselected.PageCount * format.PageSize); err != nil {
		return unlockGate(err)
	}
	slot, err := sidecar.claimReaderCancellable(reselected.TxnID, check)
	if err != nil {
		return unlockGate(liveCoordination(err))
	}
	source := &LiveSource{
		mapping:      m,
		path:         path,
		identity:     identity,
		sidecar:      sidecar,
		slot:         slot,
		meta:         reselected,
		candidateTxn: token.TransactionID,
		hasCandidate: true,
		gateLocked:   true,
		registration: liveRegistrationActive,
		ownerPID:     currentPID,
	}
	// verify_live_claim: the pair, the header, and the claimed slot
	// are re-proven through live_coordination while the gate is still
	// held; the claim-unwind release runs through the Rust Claimed
	// arm.
	if err := verifyPath(path, identity); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyPath(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyHeader(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.verifyReader(slot, reselected.TxnID); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	if err := sidecar.unlockGate(); err != nil {
		return source.releaseUnclaimed(liveCoordination(err))
	}
	source.gateLocked = false
	return source, nil
}

// bindCandidateLive runs the Rust bind_candidate over the open reader
// mapping: the path identity proofs map to the candidate-changed
// class, the pair classification propagates its own errors, and a
// token miss is the candidate-changed class.
func bindCandidateLive(m *mapping.Mapping, path string, identity FileIdentity, token bootstrap.RecoveryCandidateToken, check func() error) (format.Meta, error) {
	if err := verifyPath(path, identity); err != nil {
		return format.Meta{}, candidateChangedError()
	}
	meta, ok, err := selectCandidateLive(m, identity, token, check)
	if err != nil {
		return format.Meta{}, err
	}
	if !ok {
		return format.Meta{}, candidateChangedError()
	}
	// require_main_available is the recorded POSIX no-op.
	if err := verifyPath(path, identity); err != nil {
		return format.Meta{}, candidateChangedError()
	}
	return meta, nil
}

// selectCandidateLive classifies the meta pair and selects the exact
// token (Rust select over read_classified: the source-identity and
// token mismatches are the candidate-changed class, the
// classification propagates its errors).
func selectCandidateLive(m *mapping.Mapping, identity FileIdentity, token bootstrap.RecoveryCandidateToken, check func() error) (format.Meta, bool, error) {
	if token.Device != identity.device || token.Inode != identity.inode {
		return format.Meta{}, false, nil
	}
	pair, err := classifyPairLive(m, check)
	if err != nil {
		return format.Meta{}, false, err
	}
	meta, ok := pair.SelectedMeta(token)
	return meta, ok, nil
}

// classifyPairLive classifies the two meta pages of the open reader
// mapping (Rust classify::read_classified over the opened file; the
// bootstrap mapping initially covers exactly the meta pair).
func classifyPairLive(m *mapping.Mapping, check func() error) (bootstrap.RecoveryMetaPair, error) {
	if err := checkpoint(check); err != nil {
		return bootstrap.RecoveryMetaPair{}, err
	}
	var states [2]bootstrap.RecoveryMetaState
	var has [2]bool
	for index := uint64(0); index < 2; index++ {
		if err := checkpoint(check); err != nil {
			return bootstrap.RecoveryMetaPair{}, err
		}
		if (index+1)*format.PageSize > m.Size() {
			continue
		}
		page, err := m.Page(uint32(index))
		if err != nil {
			return bootstrap.RecoveryMetaPair{}, err
		}
		states[index] = bootstrap.ClassifyRecoveryMeta(page)
		has[index] = true
	}
	return bootstrap.ClassifyRecoveryMetaPair(states, has), nil
}
