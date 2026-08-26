// Public live writer surface (Rust live_writer.rs LiveWriter +
// result.rs): OpenLiveWriter opens the single live writer lease with
// the full sidecar coordination (shared main lifetime lock, reader-table
// gate, committed selection, slot scan, writer claim, tail trim), the
// direct transaction commits through the gate-around-Publish barrier,
// and Close releases the writer lease, the gate, and the lifetime lock
// in the Rust order. The immutable-mode Writer/OpenWriter paths are
// unchanged.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// LiveWriter is the exclusive writer of one live database (Rust
// LiveWriter). It holds the shared main lifetime lock and the exclusive
// sidecar writer lease from OpenLiveWriter until Close; readers keep
// registering and committing through the reader table while it is open.
type LiveWriter struct {
	lw *live.LiveWriter
}

// OpenLiveWriter opens path as the single live writer (Rust
// LiveWriter::open): the main file is mapped read-write under the shared
// lifetime lock, the ready reader table of the same database is gated,
// the pair is proven, the committed generation is selected, every reader
// slot is scanned against it, the writer lease is claimed, and any
// unpublished tail is trimmed. The database must be a live pair (created
// by CreateLive or converted by InitializeLive). cancellation, when
// non-nil, is checked between every bounded step.
func OpenLiveWriter(path string, budget PageBudget, cancellation *CancellationToken) (*LiveWriter, error) {
	lw, err := live.OpenLiveWriter(path, budget.internal(), writerNamespaceCheck, cancellation.check)
	if err != nil {
		return nil, err
	}
	return &LiveWriter{lw: lw}, nil
}

// Info reports the selected committed generation (Rust
// WriterCore::base_info mapped to the public DatabaseInfo; after a
// successful open the selection is always ProvenCurrent).
func (w *LiveWriter) Info() (DatabaseInfo, error) {
	if w.lw == nil {
		return DatabaseInfo{}, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	wi, err := w.lw.BaseInfo()
	if err != nil {
		return DatabaseInfo{}, err
	}
	return DatabaseInfo{
		Family:           AddressFamily(wi.AddressFamily),
		ValueKind:        ValueKind(wi.ValueKind),
		StructureKind:    StructureKind(wi.StructureKind),
		ValueTag:         ValueTag{wire: wi.ValueTag},
		DatabaseID:       wi.DatabaseID,
		TransactionID:    wi.TransactionID,
		CommitNonce:      wi.CommitNonce,
		PageCount:        wi.PageCount,
		RangeRecordCount: wi.RangeRecordCount,
		ActiveFeedCount:  wi.ActiveFeedCount,
		MetaSelection:    MetaSelectionProvenCurrent,
	}, nil
}

// mutationHost is the writer-facing surface the advanced transaction
// and workflow facades compose (Rust LiveWriter): the mapped core owner
// plus the abort, discard, and commit terminals. Both hosts satisfy it
// today: the sidecar-bound LiveWriter and the off-contract Writer (the
// latter recorded remove-planned in the parity ledger).
type mutationHost interface {
	coreOf() *writer.Core
	healthy() error
	abortAfter(cause error) error
	abortAfterSource(cause error) error
	discardDraft() error
	Abort() error
	commitPrepared(cancellation *CancellationToken, markSpent func(), context string) (CommitResult, error)
}

// ReclaimOutcome classifies one reclamation attempt (Rust
// ReclaimResult).
type ReclaimOutcome uint8

const (
	// ReclaimOutcomeNoChange reports no complete retirement
	// transaction was safe and within both limits (Rust
	// ReclaimResult::NoChange).
	ReclaimOutcomeNoChange ReclaimOutcome = iota
	// ReclaimOutcomeCommitted reports one selected prefix reached the
	// normal commit path (Rust ReclaimResult::Commit).
	ReclaimOutcomeCommitted
)

// ReclaimResult is the factual outcome of one reclamation (Rust
// ReclaimResult): the outcome, the selected transaction/page counts,
// and the full live commit result of the published reclamation.
type ReclaimResult struct {
	Outcome          ReclaimOutcome
	TransactionCount uint64
	PageCount        uint64
	Commit           LiveCommitResult
}

// Reclaim reclaims the oldest safe retirement transactions and
// auto-publishes (Rust LiveWriter::reclaim): the cancellation is
// checked first, the live writer must be open, healthy, and clean, both
// work limits must be nonzero, and the selected prefix publishes
// through the normal commit terminal under the exclusive reader-table
// gate. A pinned reader whose generation a retirement would invalidate
// blocks that reclamation (NoChange) until it closes.
func (w *LiveWriter) Reclaim(maxTransactions, maxPages uint64, cancellation *CancellationToken) (ReclaimResult, error) {
	if w.lw == nil {
		return ReclaimResult{}, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	result, err := w.lw.Reclaim(maxTransactions, maxPages, cancellation.check)
	if err != nil {
		return ReclaimResult{}, err
	}
	return ReclaimResult{
		Outcome:          ReclaimOutcome(result.Outcome),
		TransactionCount: result.TransactionCount,
		PageCount:        result.PageCount,
		Commit:           publicCommitResult(result.Commit),
	}, nil
}

// MetadataJSONLen returns the exact decompressed metadata length of the
// current generation (Rust LiveWriter::metadata_json_len): the staged
// draft metadata when a transaction staged metadata, else the committed
// base metadata. present is false for absent metadata.
func (w *LiveWriter) MetadataJSONLen() (uint64, bool, error) {
	if w.lw == nil {
		return 0, false, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return w.lw.MetadataJSONLen()
}

// ReadMetadataJSON fills caller storage with the exact decompressed
// metadata bytes of the current generation (Rust
// LiveWriter::read_metadata_json): absent metadata reports present
// false; an undersized caller buffer is refused before any bytes are
// read.
func (w *LiveWriter) ReadMetadataJSON(output []byte) (int, bool, error) {
	if w.lw == nil {
		return 0, false, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return w.lw.ReadMetadataJSON(output)
}

// MetadataJSON returns the complete decompressed metadata bytes of the
// current generation (Rust LiveWriter::metadata_json): the staged draft
// metadata when a transaction staged metadata, else the committed base
// metadata. present is false for absent metadata; an empty non-nil
// slice with present true is the exact empty-payload state.
func (w *LiveWriter) MetadataJSON() ([]byte, bool, error) {
	if w.lw == nil {
		return nil, false, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return w.lw.MetadataJSON()
}

// coreOf exposes the mapped writer core to the transaction facades
// (SOW-0027 consolidation host accessor).
func (w *LiveWriter) coreOf() *writer.Core {
	if w.lw == nil {
		return nil
	}
	return w.lw.Core()
}

// healthy proves the live writer is open and usable (Rust
// LiveWriter::require_healthy; the mutationHost state probe): the
// closed state reports WrongState.
func (w *LiveWriter) healthy() error {
	if w.lw == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return w.lw.Healthy()
}

// abortAfter aborts the draft after a failed mutation or commit
// preparation (Rust LiveWriter::abort_after; the mutationHost terminal):
// the fatal classes (Io, Format, Corrupt) make the live writer
// unusable even when the discard succeeds.
func (w *LiveWriter) abortAfter(cause error) error { return w.lw.AbortAfter(cause) }

// abortAfterSource aborts the draft after a source-driven workflow
// failure (Rust LiveWriter::abort_after_source; the mutationHost
// terminal): no fatal branding, and a failed discard makes the live
// writer unusable.
func (w *LiveWriter) abortAfterSource(cause error) error { return w.lw.AbortAfterSource(cause) }

// discardDraft aborts the draft under a pair proof (Rust
// LiveWriter::discard_draft; the mutationHost no-change teardown
// terminal): the unpublished tail is removed and a failed discard makes
// the live writer unusable.
func (w *LiveWriter) discardDraft() error { return w.lw.DiscardDraft() }

// Abort discards any open draft without publishing it (Rust
// LiveWriter::abort): a clean writer reports ErrorNoPendingTransaction.
// The writer stays open and healthy. An incomplete discard keeps the
// writer close-only and reports the factual cause.
func (w *LiveWriter) Abort() error {
	if w.lw == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	result, err := w.lw.Abort()
	if err != nil {
		return err
	}
	if result.Outcome == live.AbortOutcomeAbortIncomplete {
		return result.Cause
	}
	return nil
}

// commitPrepared publishes the open draft through the live commit
// barrier (Rust LiveWriter::commit_with; the mutationHost terminal used
// by the advanced transaction and workflow surfaces): the attempt is
// named, the draft prepared, the reader-table gate taken exclusive, the
// pair, the unchanged base, the reader slots, and the draft length are
// proven, and the alternate meta page is published, with the retained
// cleanup and coordination evidence mapped into the public CommitResult.
// An unchanged draft is discarded and reports ErrorNoPendingTransaction,
// exactly like the Rust commit_attempt.
func (w *LiveWriter) commitPrepared(cancellation *CancellationToken, markSpent func(), context string) (CommitResult, error) {
	result, err := w.lw.Commit(cancellation.check)
	if err != nil {
		markSpent()
		return CommitResult{}, err
	}
	markSpent()
	return CommitResult{
		Status:              CommitStatus(result.Durability),
		DatabaseID:          result.AttemptedDatabaseID,
		TransactionID:       result.AttemptedTransactionID,
		CommitNonce:         result.AttemptedCommitNonce,
		Err:                 result.Cause,
		Cleanup:             publicCleanupArtifacts(result.Cleanup),
		CoordinationCleanup: publicCoordinationCleanup(result.CoordinationCleanup),
	}, nil
}

// BeginDirect opens one ordered direct transaction on a clean live
// writer (Rust LiveWriter::begin_direct_transaction): cancellation is
// checked first (a fired token classifies as Cancelled even on a closed
// writer), a direct database is required, the commit nonce is drawn
// inside the writer core, and the transaction owns every later mutation
// until Commit or Abort. The captured token checkpoints every mutation
// and the commit.
func (w *LiveWriter) BeginDirect(cancellation *CancellationToken) (*LiveDirectTransaction, error) {
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if w.lw == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := w.lw.BeginDirect(); err != nil {
		return nil, err
	}
	return &LiveDirectTransaction{w: w, active: true, cancellation: cancellation}, nil
}

// Close finishes the live writer (Rust LiveWriter::close): any open
// draft is discarded with its unpublished tail under a pair proof, the
// committed generation is re-selected and trimmed, and the writer lease,
// the gate, and the shared lifetime lock are released in the Rust order.
// A second Close on the closed state is idempotent success; an
// incomplete close is retryable and keeps the writer usable for another
// Close (Rust CloseResult parity).
func (w *LiveWriter) Close() (LiveCloseResult, error) {
	if w.lw == nil {
		return closedResult(nil), nil
	}
	result, err := w.lw.Close()
	if err != nil {
		return LiveCloseResult{}, err
	}
	if result.Outcome == live.CloseOutcomeClosed {
		w.lw = nil
	}
	return publicCloseResult(result), nil
}

// LiveDirectTransaction is one ordered advanced direct transaction on a
// live writer (Rust DirectTransaction). Every mutation applies in exact
// call order; the draft is discarded by Commit and Abort and by
// LiveWriter.Close.
type LiveDirectTransaction struct {
	w            *LiveWriter
	active       bool
	cancellation *CancellationToken
}

// checkOrAbort mirrors the Rust transaction state checkpoint: the
// transaction must be active and the captured cancellation must not have
// fired; a fired cancellation aborts the workflow through the live
// writer and spends the handle.
func (t *LiveDirectTransaction) checkOrAbort() error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if err := t.cancellation.check(); err != nil {
		t.active = false
		return t.w.abortAfter(err)
	}
	return nil
}

func (t *LiveDirectTransaction) requireActive() error {
	if !t.active || t.w == nil || t.w.lw == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	if t.w.lw.Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	return nil
}

func (t *LiveDirectTransaction) requireMutation(family uint8, ordered bool) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if t.w.lw.Draft().MetadataStaged() {
		return &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	if !ordered {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"}
	}
	info, err := t.w.lw.BaseInfo()
	if err != nil {
		return err
	}
	if info.ValueKind != format.ValueKindDirect {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "direct mutation requires a direct database"}
	}
	if info.AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "direct mutation does not match the database family"}
	}
	return nil
}

// AssignV4 assigns one inclusive IPv4 interval in exact call order (Rust
// DirectTransaction::assign_v4).
func (t *LiveDirectTransaction) AssignV4(from, to IPv4, value uint32) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.w.lw.AssignV4(uint32(from), uint32(to), value)
	if err != nil {
		// Rust mutate -> abort_after discards the draft on a store
		// failure, and the operation nonce lives in the draft: once the
		// draft is gone the handle is stale and must refuse every later
		// op (Rust require_transaction WrongState), even after a newer
		// transaction began. Spend it whenever the failure left no
		// draft; pre-mutate refusals (the 20 MiB cap) keep the draft
		// and the handle.
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint: a token that fired during
	// the mutation aborts the draft and reports the cancellation.
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// AssignV6 assigns one inclusive IPv6 interval in exact call order (Rust
// DirectTransaction::assign_v6).
func (t *LiveDirectTransaction) AssignV6(from, to IPv6, value uint32) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.w.lw.AssignV6(from.Hi, from.Lo, to.Hi, to.Lo, value)
	if err != nil {
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint (see AssignV4).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// ClearV4 clears one inclusive IPv4 interval (Rust
// DirectTransaction::clear_v4).
func (t *LiveDirectTransaction) ClearV4(from, to IPv4) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.w.lw.ClearV4(uint32(from), uint32(to))
	if err != nil {
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint (see AssignV4).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// ClearV6 clears one inclusive IPv6 interval (Rust
// DirectTransaction::clear_v6).
func (t *LiveDirectTransaction) ClearV6(from, to IPv6) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.w.lw.ClearV6(from.Hi, from.Lo, to.Hi, to.Lo)
	if err != nil {
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint (see AssignV4).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// SetMetadataJSON stages one exact metadata replacement in this
// transaction (Rust DirectTransaction::set_metadata_json).
func (t *LiveDirectTransaction) SetMetadataJSON(input []byte) (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	// Rust run_transaction pre-checkpoint: a fired token aborts the
	// draft before the stage refusal.
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	if t.w.lw.Draft().MetadataStaged() {
		return false, &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	changed, err := t.w.lw.SetMetadata(input)
	if err != nil {
		// The 20 MiB cap is a pre-mutate refusal (InvalidArgument, the
		// draft survives); a store failure aborted the draft and spends
		// the handle (Rust mutate -> abort_after).
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint (see AssignV4).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in this transaction (Rust
// DirectTransaction::clear_metadata_json).
func (t *LiveDirectTransaction) ClearMetadataJSON() (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	// Rust run_transaction pre-checkpoint (see SetMetadataJSON).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	if t.w.lw.Draft().MetadataStaged() {
		return false, &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	changed, err := t.w.lw.ClearMetadata()
	if err != nil {
		if t.w.lw.Draft() == nil {
			t.active = false
		}
		return false, err
	}
	// Rust run_transaction post-checkpoint (see AssignV4).
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// Commit publishes this transaction through the commit barrier (Rust
// DirectTransaction::commit + LiveWriter::commit_with): the draft is
// prepared, the reader-table gate is taken exclusive, the pair, the
// unchanged base, the reader slots, and the draft length are proven, and
// the alternate meta page is published. An unchanged transaction is
// discarded and reports ErrorNoPendingTransaction; a transaction whose
// draft a failed operation already aborted also reports
// ErrorNoPendingTransaction (Rust commit_attempt on a draft-less core).
// A failure before publication aborts the draft and reports
// CommitNotCommitted carrying the cause; a failure after the meta write
// reports CommitOutcomeUnknown and the writer fails closed until Close.
// A spent transaction (this Commit or Abort already ran) reports
// ErrorNoPendingTransaction.
func (t *LiveDirectTransaction) Commit() (LiveCommitResult, error) {
	if !t.active {
		return LiveCommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if t.w == nil || t.w.lw == nil {
		return LiveCommitResult{}, &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	// Rust DirectTransaction::commit -> commit_operation with the
	// captured cancellation: the token checkpoints the attempt, the
	// prepare-and-lock sequence, and the publication loop.
	result, err := t.w.lw.Commit(t.cancellation.check)
	t.active = false
	if err != nil {
		return LiveCommitResult{}, err
	}
	return publicCommitResult(result), nil
}

// Abort discards this transaction and its unpublished tail (Rust
// DirectTransaction::abort); the live writer stays open and healthy. A
// transaction whose draft a failed operation already aborted reports
// ErrorNoPendingTransaction (Rust LiveWriter::abort has_draft gate). A
// spent transaction (this Commit or Abort already ran) reports
// ErrorNoPendingTransaction.
func (t *LiveDirectTransaction) Abort() (LiveAbortResult, error) {
	if !t.active {
		return LiveAbortResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if t.w == nil || t.w.lw == nil {
		return LiveAbortResult{}, &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	result, err := t.w.lw.Abort()
	t.active = false
	if err != nil {
		return LiveAbortResult{}, err
	}
	return publicAbortResult(result), nil
}

// AbortOutcome is the factual outcome of one live abort (Rust
// live_writer::AbortOutcome): whether the draft and its unpublished
// tail were provably removed.
type AbortOutcome uint8

const (
	AbortOutcomeAborted AbortOutcome = iota
	AbortOutcomeAbortIncomplete
)

// CloseOutcome is the factual outcome of one live writer close (Rust
// live_writer::CloseOutcome): an incomplete close is retryable.
type CloseOutcome uint8

const (
	CloseOutcomeClosed CloseOutcome = iota
	CloseOutcomeCloseIncomplete
)

// CoordinationCleanup is the coordination residue class of one failed
// live operation (Rust publication::CoordinationCleanup): which lock or
// guard the caller must still release. The type is the publication
// machine enum so the recovery terminal carries the same class.
type CoordinationCleanup = publication.CoordinationCleanup

const (
	CoordinationCleanupNone                        = publication.CoordinationCleanupNone
	CoordinationCleanupCleanupGuard                = publication.CoordinationCleanupCleanupGuard
	CoordinationCleanupRetainedReaderCloseRequired = publication.CoordinationCleanupRetainedReaderCloseRequired
	CoordinationCleanupRetainedWriterCloseRequired = publication.CoordinationCleanupRetainedWriterCloseRequired
)

// LiveCommitCleanupArtifact is one exact unresolved unpublished main
// tail (Rust live_writer::CommitCleanupArtifact).
type LiveCommitCleanupArtifact struct {
	DirectoryIdentity        *FileIdentity
	MainBasename             LocalBasename
	MainIdentity             *FileIdentity
	ExpectedDatabaseID       [16]byte
	TargetTransactionID      uint64
	TargetCommitNonce        [16]byte
	CommittedTargetLength    uint64
	ObservedTailEndExclusive *uint64
	CleanupError             ErrorCode
}

// LiveCommitCleanupArtifacts is the fixed commit cleanup ledger; live
// commits can own only their main tail (Rust
// live_writer::CommitCleanupArtifacts).
type LiveCommitCleanupArtifacts struct {
	entry *LiveCommitCleanupArtifact
}

// Empty reports whether the ledger carries no entry (Rust is_empty).
func (c LiveCommitCleanupArtifacts) Empty() bool { return c.entry == nil }

// Entry returns the single tail artifact, or nil (Rust get(0)).
func (c LiveCommitCleanupArtifacts) Entry() *LiveCommitCleanupArtifact { return c.entry }

// CleanupState reports whether an abandoned attempt artifact was
// provably removed (Rust CommitResult::cleanup_state).
func (c LiveCommitCleanupArtifacts) CleanupState() CleanupState {
	if c.Empty() {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// LiveCommitResult is the exact identity, durability, and cleanup facts
// of one live commit attempt (Rust live_writer::CommitResult). The
// immutable-mode CommitResult carries the pre-sidecar shape of the
// accepted coordination divergence; the live result carries the full
// sidecar and coordination surface.
type LiveCommitResult struct {
	AttemptedDatabaseID    [16]byte
	DirectoryIdentity      *FileIdentity
	MainIdentity           *FileIdentity
	AttemptedTransactionID uint64
	AttemptedCommitNonce   [16]byte
	Status                 CommitStatus
	Cleanup                LiveCommitCleanupArtifacts
	CoordinationCleanup    CoordinationCleanup
	Cause                  error
}

// CleanupState reports whether the commit left coordination residue
// (Rust CommitResult::cleanup_state).
func (r LiveCommitResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// LiveAbortResult is the factual live abort result; a cleanup failure
// retains a close-only writer (Rust live_writer::AbortResult).
type LiveAbortResult struct {
	Outcome             AbortOutcome
	Cleanup             LiveCommitCleanupArtifacts
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// CleanupState reports whether the abort left coordination residue
// (Rust AbortResult::cleanup_state).
func (r LiveAbortResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// LiveCloseResult is the factual live writer close result; an
// incomplete close is retryable (Rust live_writer::CloseResult).
type LiveCloseResult struct {
	Outcome             CloseOutcome
	AbortOutcome        *AbortOutcome
	Cleanup             LiveCommitCleanupArtifacts
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// CleanupState reports whether the close left coordination residue
// (Rust CloseResult::cleanup_state).
func (r LiveCloseResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// publicCommitResult maps one internal live commit result to the public
// SDK surface (Rust live_writer::CommitResult).
func publicCommitResult(result live.LiveCommitResult) LiveCommitResult {
	return LiveCommitResult{
		AttemptedDatabaseID:    result.AttemptedDatabaseID,
		DirectoryIdentity:      publicIdentity(&result.DirectoryIdentity),
		MainIdentity:           publicIdentity(&result.MainIdentity),
		AttemptedTransactionID: result.AttemptedTransactionID,
		AttemptedCommitNonce:   result.AttemptedCommitNonce,
		Status:                 CommitStatus(result.Durability),
		Cleanup:                publicCleanupArtifacts(result.Cleanup),
		CoordinationCleanup:    publicCoordinationCleanup(result.CoordinationCleanup),
		Cause:                  result.Cause,
	}
}

// publicAbortResult maps one internal live abort result to the public
// SDK surface (Rust live_writer::AbortResult).
func publicAbortResult(result live.LiveAbortResult) LiveAbortResult {
	return LiveAbortResult{
		Outcome:             publicAbortOutcome(result.Outcome),
		Cleanup:             publicCleanupArtifacts(result.Cleanup),
		CoordinationCleanup: publicCoordinationCleanup(result.CoordinationCleanup),
		Cause:               result.Cause,
	}
}

// publicCloseResult maps one internal live close result to the public
// SDK surface (Rust live_writer::CloseResult).
func publicCloseResult(result live.LiveCloseResult) LiveCloseResult {
	out := LiveCloseResult{
		Outcome:             publicCloseOutcome(result.Outcome),
		Cleanup:             publicCleanupArtifacts(result.Cleanup),
		CoordinationCleanup: publicCoordinationCleanup(result.CoordinationCleanup),
		Cause:               result.Cause,
	}
	if result.AbortOutcome != nil {
		value := publicAbortOutcome(*result.AbortOutcome)
		out.AbortOutcome = &value
	}
	return out
}

// closedResult builds the public success close result of an already
// closed writer (Rust CloseResult::closed).
func closedResult(abort *live.AbortOutcome) LiveCloseResult {
	out := LiveCloseResult{Outcome: CloseOutcomeClosed}
	if abort != nil {
		value := publicAbortOutcome(*abort)
		out.AbortOutcome = &value
	}
	return out
}

func publicCleanupArtifacts(c live.CommitCleanupArtifacts) LiveCommitCleanupArtifacts {
	entry := c.Entry()
	if entry == nil {
		return LiveCommitCleanupArtifacts{}
	}
	return LiveCommitCleanupArtifacts{entry: &LiveCommitCleanupArtifact{
		DirectoryIdentity:        publicIdentity(&entry.DirectoryIdentity),
		MainBasename:             publicBasename(entry.MainBasename),
		MainIdentity:             publicIdentity(&entry.MainIdentity),
		ExpectedDatabaseID:       entry.ExpectedDatabaseID,
		TargetTransactionID:      entry.TargetTransactionID,
		TargetCommitNonce:        entry.TargetCommitNonce,
		CommittedTargetLength:    entry.CommittedTargetLength,
		ObservedTailEndExclusive: entry.ObservedTailEndExclusive,
		CleanupError:             ErrorCode(entry.CleanupError),
	}}
}

func publicAbortOutcome(outcome live.AbortOutcome) AbortOutcome { return AbortOutcome(outcome) }

func publicCloseOutcome(outcome live.CloseOutcome) CloseOutcome { return CloseOutcome(outcome) }

func publicCoordinationCleanup(cleanup live.CoordinationCleanup) CoordinationCleanup {
	return CoordinationCleanup(cleanup)
}
