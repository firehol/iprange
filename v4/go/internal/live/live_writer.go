// Single live writer and the commit barrier (Rust live_writer.rs
// LiveWriter + close.rs + commit.rs): the writer composes the writer
// core (internal/writer) and the sidecar reader table. Open takes the
// shared main lifetime lock, gates the reader table, proves the pair,
// selects the committed generation, scans the reader slots, and claims
// the exclusive writer lease; every commit runs the gate-around-Publish
// barrier (pair recheck, unchanged base, slot scan, draft length, then
// the alternate-meta publication); close releases the writer lease, the
// gate, and the lifetime lock in the Rust order.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// currentPID is the process id sampled once at package init: Go cannot
// fork (requireOwner is structural parity with Rust ProcessIdentity),
// so the per-operation os.Getpid syscall is avoided on every commit,
// abort, and close step (Rust uses a syscall-free atomic fork marker).
var currentPID = os.Getpid()

// LiveWriterState is the coordination state machine of one live writer
// (Rust live_writer::State).
type LiveWriterState uint8

const (
	LiveWriterHealthy LiveWriterState = iota
	LiveWriterOutcomeUnknown
	LiveWriterUnusable
	LiveWriterClosingWriter
	LiveWriterClosingGate
	LiveWriterClosingMain
	LiveWriterClosed
)

// LiveWriter is the exclusive writer of one live database (Rust
// LiveWriter): the mapped writer core, the retained pair identities, and
// the sidecar coordination. The writer holds the shared main lifetime
// lock plus the exclusive sidecar writer lease from open until close.
type LiveWriter struct {
	core              *writer.Core
	mainPath          string
	mainIdentity      FileIdentity
	directoryIdentity FileIdentity
	mainBasename      LocalBasename
	sidecar           *Sidecar
	state             LiveWriterState
	ownerPID          int
	// closingHadPending remembers whether close started with an open
	// draft, carried through the Closing states (Rust
	// State::ClosingWriter(bool) payload).
	closingHadPending bool
}

// OpenLiveWriter opens the only live writer lease without validating
// either page graph (Rust LiveWriter::open): the shared main lifetime
// lock is taken inside the mapping open, the sidecar reader table must
// be ready for the same database, the gate is held exclusive while the
// pair is proven, the committed generation is selected, every reader
// slot is scanned against it, the writer lease is claimed, and any
// unpublished tail is trimmed. The gate is released before the writer
// returns; the writer lease is held until Close. namespace, when
// non-nil, is the mapping namespace hook; check, when non-nil, runs
// between every bounded step.
func OpenLiveWriter(path string, budget writer.PageBudget, namespace func(clean string) error, check func() error) (*LiveWriter, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	// Rust TransactionBudget::validate at LiveWriter::open: a live
	// writer owns the main descriptor plus the sidecar descriptor, so
	// an open-files bound below two cannot satisfy the contract and is
	// refused before any path access (ErrorCode
	// InsufficientResourceBudget, Rust BudgetExceeded).
	if budget.MaxOpenFiles < 2 {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "a live writer requires two open files"}
	}
	core, err := writer.OpenWriterLive(path, budget, namespace)
	if err != nil {
		return nil, err
	}
	// The retained identity facts of the opened pair (Rust open_main):
	// the main identity from the held descriptor, the parent identity,
	// and the portable basename. The public identity is identity-
	// preserving in Go (publicIdentity returns the same device+inode),
	// so results carry the main identity directly. require_main_available
	// is a pure POSIX no-op in Rust (live_cleanup.rs require_available,
	// non-windows arm), so Go omits it; the cleanup-attempt nonce draw
	// (unique_attempt_id) belongs to the creation path and is
	// documented there.
	device, inode, err := core.FileIdentity()
	if err != nil {
		core.Close()
		return nil, err
	}
	mainIdentity := FileIdentity{device: device, inode: inode}
	directoryIdentity, err := parentIdentity(path)
	if err != nil {
		core.Close()
		return nil, err
	}
	basename, err := localBasenameFromPath(path)
	if err != nil {
		core.Close()
		return nil, err
	}
	if err := verifyPath(path, mainIdentity); err != nil {
		core.Close()
		return nil, err
	}
	sidecar, err := open(path, core.BaseInfo().DatabaseID)
	if err != nil {
		core.Close()
		return nil, err
	}
	fail := func(err error) (*LiveWriter, error) {
		sidecar.Close()
		core.Close()
		return nil, err
	}
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return fail(err)
	}
	unlockGate := func(err error) (*LiveWriter, error) {
		return fail(combineErrors(err, sidecar.unlockGate()))
	}
	// open_locked: prove the pair, select the committed generation, prove
	// it belongs to the reader table, scan the slots, claim the writer
	// lease, trim any unpublished tail, and prove the pair again (Rust
	// live_writer::open_locked). Every failure releases the gate and the
	// mapping.
	if err := verifyPair(path, mainIdentity, sidecar); err != nil {
		return unlockGate(err)
	}
	if err := core.SelectCommitted(); err != nil {
		return unlockGate(err)
	}
	if core.BaseInfo().DatabaseID != sidecar.header.databaseID {
		return unlockGate(&format.Error{Code: format.CodeWrongState, Detail: "reader table belongs to a different database"})
	}
	if err := sidecar.scanAtMostCancellable(core.BaseInfo().TransactionID, check); err != nil {
		return unlockGate(err)
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := sidecar.claimWriter(); err != nil {
		return unlockGate(err)
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := core.TrimCommittedTail(); err != nil {
		return unlockGate(err)
	}
	if err := checkpoint(check); err != nil {
		return unlockGate(err)
	}
	if err := verifyPair(path, mainIdentity, sidecar); err != nil {
		return unlockGate(err)
	}
	if err := sidecar.unlockGate(); err != nil {
		return fail(err)
	}
	return &LiveWriter{
		core:              core,
		mainPath:          path,
		mainIdentity:      mainIdentity,
		directoryIdentity: directoryIdentity,
		mainBasename:      basename,
		sidecar:           sidecar,
		state:             LiveWriterHealthy,
		ownerPID:          currentPID,
	}, nil
}

// BaseInfo reports the selected committed generation (Rust
// WriterCore::base_info over the live writer).
func (w *LiveWriter) BaseInfo() (writer.WriterInfo, error) {
	if err := w.requireHealthy(); err != nil {
		return writer.WriterInfo{}, err
	}
	return w.core.BaseInfo(), nil
}

// Draft returns the open COW draft, or nil (Rust WriterCore::draft over
// the live writer).
func (w *LiveWriter) Draft() *writer.Draft { return w.core.Draft() }

// Core exposes the mapped writer core to the SDK root facade (SOW-0027
// live-writer consolidation): the advanced transaction and workflow
// surfaces compose the core through this one owner while every abort,
// commit, and close terminal stays in the live writer machine.
func (w *LiveWriter) Core() *writer.Core { return w.core }

// MainIdentity returns the retained main-file identity of the open
// writer (Rust LiveWriter::main_identity; the membership import
// compares it with the source reader identity to refuse importing a
// database onto itself).
func (w *LiveWriter) MainIdentity() FileIdentity { return w.mainIdentity }

// Healthy proves the live writer is open and usable (Rust
// LiveWriter::require_healthy).
func (w *LiveWriter) Healthy() error { return w.requireHealthy() }

// AbortAfter aborts the draft after a failed mutation or commit
// preparation (Rust LiveWriter::abort_after): the fatal classes (Io,
// Format, Corrupt) make the writer unusable even when the discard
// succeeds. Exported for the SDK root facade's transaction surfaces.
func (w *LiveWriter) AbortAfter(cause error) error { return w.abortAfter(cause) }

// AbortAfterSource aborts the draft after a source-driven workflow
// failure (Rust LiveWriter::abort_after_source): no fatal branding, but
// a failed discard still makes the writer unusable. Exported for the
// SDK root facade's workflow surfaces (history projection drive).
func (w *LiveWriter) AbortAfterSource(cause error) error { return w.abortAfterSource(cause) }

// DiscardDraft aborts the draft under a pair proof without branding
// (Rust LiveWriter::discard_draft): any open draft and its unpublished
// tail are removed. On a failed discard the writer becomes unusable.
// Exported as the workflow no-change teardown terminal.
func (w *LiveWriter) DiscardDraft() error { return w.discardDraft() }

// MetadataJSONLen returns the exact decompressed metadata length of the
// current generation (Rust LiveWriter::metadata_json_len): the staged
// draft metadata when a transaction staged metadata, else the committed
// base metadata. present is false for absent metadata. The machine's
// open/healthy state is proven first.
func (w *LiveWriter) MetadataJSONLen() (uint64, bool, error) {
	if err := w.requireHealthy(); err != nil {
		return 0, false, err
	}
	length, present := w.core.MetadataJSONLen()
	return length, present, nil
}

// ReadMetadataJSON fills caller storage with the exact decompressed
// metadata bytes of the current generation (Rust
// LiveWriter::read_metadata_json): absent metadata reports present
// false; an undersized caller buffer is refused before any bytes are
// read.
func (w *LiveWriter) ReadMetadataJSON(output []byte) (int, bool, error) {
	if err := w.requireHealthy(); err != nil {
		return 0, false, err
	}
	return w.core.ReadMetadataJSON(output)
}

// MetadataJSON returns the complete decompressed metadata bytes of the
// current generation (Rust LiveWriter::metadata_json): the staged draft
// metadata when a transaction staged metadata, else the committed base
// metadata.
func (w *LiveWriter) MetadataJSON() ([]byte, bool, error) {
	if err := w.requireHealthy(); err != nil {
		return nil, false, err
	}
	return w.core.MetadataJSON()
}

// BeginDirect draws the commit nonce and starts one COW draft over the
// committed generation (Rust begin_direct_transaction: cancellation
// check, require_healthy, the direct value-kind gate, then
// WriterCore::begin_transaction; Go has no per-transaction cancellation
// token because the public Go surface passes no cancellation to
// BeginDirect, so the check is structurally closed).
func (w *LiveWriter) BeginDirect() error {
	if err := w.requireHealthy(); err != nil {
		return err
	}
	if w.core.BaseInfo().ValueKind != format.ValueKindDirect {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "direct transaction requires a direct database"}
	}
	_, err := w.core.BeginTransaction()
	return err
}

// AssignV4 assigns one inclusive IPv4 interval to the open draft (Rust
// DirectTransaction::assign_v4 through LiveWriter::mutate).
func (w *LiveWriter) AssignV4(from, to uint32, value uint32) (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	changed, err := w.core.AssignV4(from, to, value)
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// AssignV6 assigns one inclusive IPv6 interval to the open draft (Rust
// DirectTransaction::assign_v6 through LiveWriter::mutate).
func (w *LiveWriter) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	changed, err := w.core.AssignV6(fromHi, fromLo, toHi, toLo, value)
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// ClearV4 clears one inclusive IPv4 interval from the open draft (Rust
// DirectTransaction::clear_v4 through LiveWriter::mutate).
func (w *LiveWriter) ClearV4(from, to uint32) (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	changed, err := w.core.ClearV4(from, to)
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// ClearV6 clears one inclusive IPv6 interval from the open draft (Rust
// DirectTransaction::clear_v6 through LiveWriter::mutate).
func (w *LiveWriter) ClearV6(fromHi, fromLo, toHi, toLo uint64) (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	changed, err := w.core.ClearV6(fromHi, fromLo, toHi, toLo)
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// SetMetadata stages one exact metadata replacement in the open draft
// (Rust DirectTransaction::set_metadata_json through LiveWriter::mutate).
// The 20 MiB cap is checked before mutate exactly like Rust
// stage_metadata_json: an oversized input is refused with
// InvalidArgument and the draft survives; failures inside the store
// (compression heap, chain pages) abort the draft like every other
// mutation error.
func (w *LiveWriter) SetMetadata(input []byte) (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	if uint64(len(input)) > format.MaxMetadataUncompressed {
		return false, &format.Error{Code: format.CodeInvalidArgument, Detail: "metadata exceeds 20 MiB"}
	}
	changed, err := w.core.SetMetadata(input)
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// ClearMetadata stages metadata absence in the open draft (Rust
// DirectTransaction::clear_metadata_json through LiveWriter::mutate).
func (w *LiveWriter) ClearMetadata() (bool, error) {
	if err := w.requireHealthy(); err != nil {
		return false, err
	}
	changed, err := w.core.ClearMetadata()
	if err != nil {
		return false, w.abortAfter(err)
	}
	return changed, nil
}

// Commit publishes all pending changes through the gate-around-Publish
// barrier (Rust LiveWriter::commit_with): the attempt is named, the
// draft is prepared, the gate is taken exclusive, the prepublication
// checks prove the pair, the unchanged base, the reader slots, and the
// draft length, the alternate meta page is published, and the gate is
// released. A failure before publication aborts the draft and reports
// NotCommitted; a failure after the meta write reports OutcomeUnknown
// and fails the writer closed until Close.
func (w *LiveWriter) Commit(check func() error) (LiveCommitResult, error) {
	attempt, err := w.commitAttempt()
	if err != nil {
		return LiveCommitResult{}, err
	}
	if err := w.prepareAndLock(check); err != nil {
		cause := w.abortAfter(err)
		return w.failedResult(attempt, CommitNotCommitted, cause), nil
	}
	result := w.finishCommitLocked(attempt, check)
	w.applyCommitUnlock(&result, w.sidecar.unlockGate())
	return result, nil
}

// Abort discards all unpublished changes (Rust LiveWriter::abort): the
// draft and any unpublished tail are removed under a fresh pair proof,
// and the writer stays open and healthy. A failed discard retains a
// close-only writer and reports the factual AbortIncomplete facts.
func (w *LiveWriter) Abort() (LiveAbortResult, error) {
	if err := w.requireHealthy(); err != nil {
		return LiveAbortResult{}, err
	}
	if !w.core.HasDraft() {
		return LiveAbortResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := w.discardDraft(); err != nil {
		return LiveAbortResult{
			Outcome:             AbortOutcomeAbortIncomplete,
			Cleanup:             w.unpublishedTailCleanup(errorCodeOf(err)),
			CoordinationCleanup: CoordinationCleanupRetainedWriterCloseRequired,
			Cause:               err,
		}, nil
	}
	return LiveAbortResult{Outcome: AbortOutcomeAborted}, nil
}

// Close finishes the live writer (Rust LiveWriter::close): any open
// draft is discarded with its unpublished tail under a pair proof, the
// committed generation is re-selected and trimmed, then the writer
// lease, the gate, and the shared main lifetime lock are released in
// the Rust order. A second Close on the closed state is idempotent
// success; an incomplete close is retryable.
func (w *LiveWriter) Close() (LiveCloseResult, error) {
	if err := w.requireOwner(); err != nil {
		return LiveCloseResult{}, err
	}
	if w.state == LiveWriterClosed {
		return closedResult(nil), nil
	}
	if w.state == LiveWriterClosingWriter || w.state == LiveWriterClosingGate || w.state == LiveWriterClosingMain {
		return w.finishClose(), nil
	}
	hadPending := w.core.HasDraft()
	w.closingHadPending = hadPending
	if err := w.sidecar.lockGate(LockExclusive); err != nil {
		return w.closeFailure(hadPending, err), nil
	}
	operation := w.closeLocked()
	if operation != nil {
		return w.closeFailure(hadPending, combineErrors(operation, w.sidecar.unlockGate())), nil
	}
	w.state = LiveWriterClosingWriter
	return w.finishClose(), nil
}

func (w *LiveWriter) commitAttempt() (writer.CommitAttempt, error) {
	if err := w.requireHealthy(); err != nil {
		return writer.CommitAttempt{}, err
	}
	// Rust commit_attempt also requires the operation handle not to be
	// abandoned; Go has no operation handles (workflows are not yet
	// ported), so that gate is structurally closed.
	if w.core.HasDraft() && !w.core.DraftChanged() {
		if err := w.discardDraft(); err != nil {
			return writer.CommitAttempt{}, err
		}
		return writer.CommitAttempt{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return w.core.CommitAttempt()
}

func (w *LiveWriter) prepareAndLock(check func() error) error {
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := w.core.Prepare(func() error { return checkpoint(check) }); err != nil {
		return err
	}
	if err := checkpoint(check); err != nil {
		return err
	}
	return w.sidecar.lockGateCancellable(LockExclusive, check)
}

func (w *LiveWriter) finishCommitLocked(attempt writer.CommitAttempt, check func() error) LiveCommitResult {
	result := w.commitLocked(check)
	switch result.Status {
	case writer.PublishCommitted:
		return LiveCommitResult{
			AttemptedDatabaseID:    attempt.DatabaseID,
			DirectoryIdentity:      w.directoryIdentity,
			MainIdentity:           w.mainIdentity,
			AttemptedTransactionID: attempt.TransactionID,
			AttemptedCommitNonce:   attempt.CommitNonce,
			Durability:             CommitCommitted,
		}
	case writer.PublishBeforePublication:
		return w.failedResult(attempt, CommitNotCommitted, w.abortAfter(result.Err))
	default:
		w.state = LiveWriterOutcomeUnknown
		return w.failedResult(attempt, CommitOutcomeUnknown, result.Err)
	}
}

func (w *LiveWriter) commitLocked(check func() error) writer.PublishResult {
	if err := checkpoint(check); err != nil {
		return writer.PublishResult{Status: writer.PublishBeforePublication, Err: err}
	}
	if err := w.prepublicationChecks(check); err != nil {
		return writer.PublishResult{Status: writer.PublishBeforePublication, Err: err}
	}
	if err := checkpoint(check); err != nil {
		return writer.PublishResult{Status: writer.PublishBeforePublication, Err: err}
	}
	return w.core.Publish(func() error { return checkpoint(check) })
}

// prepublicationChecks are the commit barrier (Rust
// LiveWriter::prepublication_checks): the pair is proven, the base is
// unchanged, every reader slot names a transaction no newer than the
// committed generation, the locked file covers the draft length, and the
// pair is proven again immediately before the meta write.
func (w *LiveWriter) prepublicationChecks(check func() error) error {
	if err := w.verifyPair(); err != nil {
		return err
	}
	if err := w.core.RequireUnchangedBase(); err != nil {
		return err
	}
	if err := w.sidecar.scanAtMostCancellable(w.core.BaseInfo().TransactionID, check); err != nil {
		return err
	}
	if err := w.core.RequireDraftLength(); err != nil {
		return err
	}
	return w.verifyPair()
}

func (w *LiveWriter) applyCommitUnlock(result *LiveCommitResult, unlockErr error) {
	if unlockErr == nil {
		return
	}
	w.state = LiveWriterUnusable
	cause := unlockErr
	if result.Cause != nil {
		cause = combineErrors(result.Cause, unlockErr)
	}
	result.Cause = cause
	result.CoordinationCleanup = CoordinationCleanupRetainedWriterCloseRequired
}

func (w *LiveWriter) failedResult(attempt writer.CommitAttempt, durability CommitDurability, cause error) LiveCommitResult {
	cleanup := cleanArtifacts()
	if w.core.HasDraft() {
		cleanup = w.unpublishedTailCleanup(errorCodeOf(cause))
	}
	coordination := CoordinationCleanupNone
	if durability == CommitOutcomeUnknown || w.state != LiveWriterHealthy || !cleanup.Empty() {
		coordination = CoordinationCleanupRetainedWriterCloseRequired
	}
	return LiveCommitResult{
		AttemptedDatabaseID:    attempt.DatabaseID,
		DirectoryIdentity:      w.directoryIdentity,
		MainIdentity:           w.mainIdentity,
		AttemptedTransactionID: attempt.TransactionID,
		AttemptedCommitNonce:   attempt.CommitNonce,
		Durability:             durability,
		Cleanup:                cleanup,
		CoordinationCleanup:    coordination,
		Cause:                  cause,
	}
}

func (w *LiveWriter) closeLocked() error {
	if err := w.verifyPair(); err != nil {
		return err
	}
	plan, err := w.core.PrepareClose()
	if err != nil {
		return err
	}
	if err := w.sidecar.scanAtMost(plan.TransactionID()); err != nil {
		return err
	}
	if err := w.core.FinishClose(plan); err != nil {
		return err
	}
	return w.verifyPair()
}

func (w *LiveWriter) finishClose() LiveCloseResult {
	if err := w.closingStep(); err != nil {
		return closingFailure(w.closingHadPending, err)
	}
	if w.closingHadPending {
		aborted := AbortOutcomeAborted
		return closedResult(&aborted)
	}
	return closedResult(nil)
}

func (w *LiveWriter) closingStep() error {
	switch w.state {
	case LiveWriterClosingWriter:
		if err := w.sidecar.releaseWriter(); err != nil {
			return err
		}
		w.state = LiveWriterClosingGate
		fallthrough
	case LiveWriterClosingGate:
		if err := w.sidecar.unlockGate(); err != nil {
			return err
		}
		w.state = LiveWriterClosingMain
		fallthrough
	case LiveWriterClosingMain:
		// Rust unmaps the core before releasing the writer lease and
		// unlocks the lifetime lock last; Go's mapping Close bundles the
		// unmap with the lifetime unlock, so the release order of the
		// three coordination locks is preserved and the unmap happens at
		// the same final step.
		if err := w.core.Close(); err != nil {
			return err
		}
		w.sidecar.Close()
		w.state = LiveWriterClosed
		return nil
	default:
		// Rust finish_close refuses a non-closing state with the
		// WrongState class: a retry after an incomplete close starts
		// from the full close path, never from a false Closed claim.
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is not closing"}
	}
}

func (w *LiveWriter) closeFailure(hadPending bool, cause error) LiveCloseResult {
	w.state = LiveWriterUnusable
	cleanup := cleanArtifacts()
	if w.core.HasDraft() {
		cleanup = w.unpublishedTailCleanup(errorCodeOf(cause))
	}
	return LiveCloseResult{
		Outcome:             CloseOutcomeCloseIncomplete,
		AbortOutcome:        abortOutcomeFor(hadPending, w.core.HasDraft()),
		Cleanup:             cleanup,
		CoordinationCleanup: CoordinationCleanupRetainedWriterCloseRequired,
		Cause:               cause,
	}
}

func closingFailure(hadPending bool, cause error) LiveCloseResult {
	return LiveCloseResult{
		Outcome:             CloseOutcomeCloseIncomplete,
		AbortOutcome:        abortOutcomeFor(hadPending, false),
		CoordinationCleanup: CoordinationCleanupRetainedWriterCloseRequired,
		Cause:               cause,
	}
}

// abortOutcomeFor reports the abort payload of a failed close: present
// only when a draft was pending at close start, and AbortIncomplete when
// the failure left the draft behind (Rust close_failure had_pending
// rule; closing_failure reports Aborted because the draft was discarded
// before the closing steps began).
func abortOutcomeFor(hadPending, hasDraft bool) *AbortOutcome {
	if !hadPending {
		return nil
	}
	value := AbortOutcomeAborted
	if hasDraft {
		value = AbortOutcomeAbortIncomplete
	}
	return &value
}

func closedResult(abort *AbortOutcome) LiveCloseResult {
	return LiveCloseResult{Outcome: CloseOutcomeClosed, AbortOutcome: abort}
}

func (w *LiveWriter) verifyPair() error {
	return verifyPair(w.mainPath, w.mainIdentity, w.sidecar)
}

// verifyPair proves the main path still names the retained identity and
// the sidecar is the retained path with a valid header (Rust
// live_writer::verify_pair).
func verifyPair(path string, identity FileIdentity, sidecar *Sidecar) error {
	if err := verifyPath(path, identity); err != nil {
		return err
	}
	if err := sidecar.verifyPath(); err != nil {
		return err
	}
	return sidecar.verifyHeader()
}

// discardDraft aborts the draft under a pair proof and fails the writer
// closed when the discard itself fails (Rust LiveWriter::discard_draft).
func (w *LiveWriter) discardDraft() error {
	if err := w.discardDraftInner(); err != nil {
		w.state = LiveWriterUnusable
		return err
	}
	return nil
}

// discardDraftInner removes the unpublished tail under a pair proof
// before and after the discard (Rust LiveWriter::discard_draft_inner).
func (w *LiveWriter) discardDraftInner() error {
	if err := w.verifyPair(); err != nil {
		return err
	}
	if err := w.core.DiscardUnpublished(); err != nil {
		return err
	}
	return w.verifyPair()
}

// abortAfter aborts the draft after a failed mutation or commit
// preparation (Rust LiveWriter::abort_after, used by mutate and
// prepare_and_lock): the fatal classes (Io, Format, Corrupt) make the
// writer unusable even when the discard succeeds.
func (w *LiveWriter) abortAfter(cause error) error {
	fatal := isFatalClass(cause)
	result := w.abortAfterSource(cause)
	if fatal {
		w.state = LiveWriterUnusable
	}
	return result
}

// abortAfterSource wraps the cause in the TransactionAborted class and
// discards the draft (Rust abort_after_source): the outer class is
// always TransactionAborted, and a failed discard nests the
// CleanupInProgress class (Rust CleanupIncomplete) around both causes
// and makes the writer unusable, exactly like the immutable
// commitAbortAfter chain.
func (w *LiveWriter) abortAfterSource(cause error) error {
	inner := cause
	if err := w.discardDraft(); err != nil {
		w.state = LiveWriterUnusable
		inner = &classedError{
			class: &format.Error{Code: format.CodeCleanupInProgress, Detail: "commit discard failed"},
			cause: combineErrors(cause, err),
		}
	}
	return &classedError{
		class: &format.Error{Code: format.CodeTransactionAborted, Detail: "the pending transaction was aborted"},
		cause: inner,
	}
}

// unpublishedTailCleanup builds the commit cleanup ledger entry from the
// core's observed tail evidence (Rust
// LiveWriter::unpublished_tail_cleanup).
func (w *LiveWriter) unpublishedTailCleanup(cleanupError format.ErrorCode) CommitCleanupArtifacts {
	state := w.core.TailCleanupState()
	if state.ObservedTailEndExclusive == nil {
		return cleanArtifacts()
	}
	return tailArtifacts(CommitCleanupArtifact{
		DirectoryIdentity:        w.directoryIdentity,
		MainBasename:             w.mainBasename,
		MainIdentity:             w.mainIdentity,
		ExpectedDatabaseID:       state.DatabaseID,
		TargetTransactionID:      state.TransactionID,
		TargetCommitNonce:        state.CommitNonce,
		CommittedTargetLength:    state.CommittedLength,
		ObservedTailEndExclusive: state.ObservedTailEndExclusive,
		CleanupError:             cleanupError,
	})
}

func (w *LiveWriter) requireHealthy() error {
	if err := w.requireOwner(); err != nil {
		return err
	}
	switch w.state {
	case LiveWriterHealthy:
		return nil
	case LiveWriterOutcomeUnknown:
		return &format.Error{Code: format.CodeWrongState, Detail: "writer has an unresolved commit outcome"}
	case LiveWriterUnusable:
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is unusable"}
	case LiveWriterClosingWriter, LiveWriterClosingGate, LiveWriterClosingMain:
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closing"}
	default:
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
}

// requireOwner proves the writer is used by the process that opened it
// (Rust require_owner, Error::ForkedHandle). Go cannot fork, so the
// check is structural parity and can never fire; currentPID is sampled
// once at package init, so the check costs no syscall per operation.
func (w *LiveWriter) requireOwner() error {
	if currentPID != w.ownerPID {
		return &format.Error{Code: format.CodeForkedHandle, Detail: "writer was opened by a different process"}
	}
	return nil
}
