// Bounded reader-safe page reclamation (Rust live_writer/reclaim.rs):
// one clean writer selects the oldest reader-safe retired transactions
// within the work limits, applies them physically in a prepared draft,
// and publishes through the normal commit terminal while holding the
// exclusive reader-table gate.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// LiveReclaimOutcome classifies one reclamation attempt (Rust
// ReclaimResult).
type LiveReclaimOutcome uint8

const (
	// ReclaimOutcomeNoChange reports no complete retirement
	// transaction was safe and within both limits (Rust
	// ReclaimResult::NoChange).
	ReclaimOutcomeNoChange LiveReclaimOutcome = iota
	// ReclaimOutcomeCommitted reports one selected prefix reached the
	// normal commit path (Rust ReclaimResult::Commit).
	ReclaimOutcomeCommitted
)

// LiveReclaimResult is the factual outcome of one reclamation (Rust
// ReclaimResult): the outcome, the selected transaction/page counts,
// and the full commit result of the published reclamation.
type LiveReclaimResult struct {
	Outcome          LiveReclaimOutcome
	TransactionCount uint64
	PageCount        uint64
	Commit           LiveCommitResult
}

// Reclaim reclaims the oldest safe retirement transactions and
// auto-publishes (Rust LiveWriter::reclaim): the cancellation is
// checked first, the writer must be healthy and clean, both work limits
// must be nonzero, the reader-table gate is taken exclusive, the pair
// and the unchanged base are proven, the oldest reader slot pins the
// safe frontier, the bounded selection is prepared and applied in the
// file-backed mapping, and the reclamation draft is published through
// the normal commit terminal. The gate stays exclusive until the
// publish resolves.
func (w *LiveWriter) Reclaim(maxTransactions, maxPages uint64, check func() error) (LiveReclaimResult, error) {
	if err := checkpoint(check); err != nil {
		return LiveReclaimResult{}, err
	}
	if err := w.requireReclaim(maxTransactions, maxPages); err != nil {
		return LiveReclaimResult{}, err
	}
	if err := w.sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return LiveReclaimResult{}, err
	}
	prepared, cause := w.reclaimLocked(maxTransactions, maxPages, check)
	return w.finishReclaim(prepared, cause, check)
}

// requireReclaim is the Rust require_reclaim gate: healthy, no pending
// draft (WrongState is the SDK projection of Rust WrongMode), and
// nonzero work limits.
func (w *LiveWriter) requireReclaim(maxTransactions, maxPages uint64) error {
	if err := w.requireHealthy(); err != nil {
		return err
	}
	if w.core.HasDraft() {
		return &format.Error{Code: format.CodeWrongState, Detail: "reclamation requires a clean writer"}
	}
	if maxTransactions == 0 || maxPages == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "reclamation work limits must be nonzero"}
	}
	return nil
}

// reclaimLocked runs the Rust reclaim_locked sequence under the
// exclusive gate: pair proof, unchanged base, the oldest-reader scan
// (a slot naming an uncommitted transaction is corrupt), a second pair
// proof, and the bounded selection. A nil prepared result with nil
// cause reports no safe work.
func (w *LiveWriter) reclaimLocked(maxTransactions, maxPages uint64, check func() error) (*writer.PreparedReclamation, error) {
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	if err := w.verifyPair(); err != nil {
		return nil, err
	}
	if err := w.core.RequireUnchangedBase(); err != nil {
		return nil, err
	}
	oldest, err := w.oldestReader(check)
	if err != nil {
		return nil, err
	}
	if err := w.verifyPair(); err != nil {
		return nil, err
	}
	return w.core.PrepareReclamation(oldest, maxTransactions, maxPages, check)
}

// oldestReader returns the oldest transaction named by a locked reader
// slot, or nil when no reader is registered (Rust
// Sidecar::oldest_reader_cancellable): any slot naming a transaction
// newer than the committed generation is corrupt.
func (w *LiveWriter) oldestReader(check func() error) (*uint64, error) {
	committed := w.core.BaseInfo().TransactionID
	var oldest uint64
	var found bool
	err := w.sidecar.scanReadersCancellable(check, func(txn uint64) error {
		if txn > committed {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "reader slot names an uncommitted transaction"}
		}
		if !found || txn < oldest {
			oldest = txn
			found = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &oldest, nil
}

// finishReclaim resolves the gated operation exactly like Rust
// finish_reclaim: a failure aborts any draft left behind, the gate is
// always released, and a released-gate failure brands the writer
// unusable and nests the cleanup. A successful select publishes through
// the normal commit terminal and returns the full commit result.
func (w *LiveWriter) finishReclaim(prepared *writer.PreparedReclamation, cause error, check func() error) (LiveReclaimResult, error) {
	if cause != nil {
		if w.core.HasDraft() {
			cause = w.abortAfter(cause)
		}
		if unlockErr := w.sidecar.unlockGate(); unlockErr != nil {
			w.state = LiveWriterUnusable
			return LiveReclaimResult{}, combineErrors(cause, unlockErr)
		}
		return LiveReclaimResult{}, cause
	}
	if prepared == nil {
		if unlockErr := w.sidecar.unlockGate(); unlockErr != nil {
			w.state = LiveWriterUnusable
			return LiveReclaimResult{}, unlockErr
		}
		return LiveReclaimResult{Outcome: ReclaimOutcomeNoChange}, nil
	}
	commit := w.finishCommitLocked(prepared.Attempt, check)
	w.applyCommitUnlock(&commit, w.sidecar.unlockGate())
	return LiveReclaimResult{
		Outcome:          ReclaimOutcomeCommitted,
		TransactionCount: prepared.TransactionCount,
		PageCount:        prepared.PageCount,
		Commit:           commit,
	}, nil
}
