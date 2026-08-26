// Public live-writer facade: create, open, direct transactions, metadata,
// commit, abort, close. The facade composes the internal writer owner; it
// never touches bytes or pages itself (SOW-0025 chunk-6 design record D1
// extends the module-root boundary to internal/writer so the public SDK
// stays the single `iprangedb` package, mirroring the Rust lib). The
// sidecar, cancellation, and coordination surfaces are milestone-4 gaps
// recorded in the SOW (D3).

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// PageBudget declares the draft resource limits of one opened writer
// (Rust live_writer::TransactionBudget): MaxHeapBytes bounds owned
// scratch (metadata compression), MaxPrivatePages bounds the COW draft
// extent, MaxGrowthPages bounds the file growth one transaction may
// claim, and MaxOpenFiles bounds the descriptors one operation may hold
// (the live writer validates it at open, Rust TransactionBudget::
// validate).
type PageBudget struct {
	MaxHeapBytes    uint64
	MaxPrivatePages uint64
	MaxGrowthPages  uint64
	MaxOpenFiles    uint32
}

// DefaultBudget returns the budget proven by the committed corpus
// generation (the Rust conformance transaction_budget values for the
// writer work the fixtures exercise).
func DefaultBudget() PageBudget {
	return PageBudget{MaxHeapBytes: 32 << 20, MaxPrivatePages: 200_000, MaxGrowthPages: 200_000, MaxOpenFiles: 2}
}

func (b PageBudget) internal() writer.PageBudget {
	return writer.PageBudget{MaxHeapBytes: b.MaxHeapBytes, MaxPrivatePages: b.MaxPrivatePages, MaxGrowthPages: b.MaxGrowthPages, MaxOpenFiles: b.MaxOpenFiles}
}

// writerNamespaceCheck is the module-root namespace hook: the SDK's
// namespace surface is a milestone-4 gap, so the hook is a package-level
// no-op implementing the writer owner's callback formal (Rust's namespace
// resolver no-op default).
func writerNamespaceCheck(clean string) error { return nil }

// noopCheckpoint is the module-root durability checkpoint hook: the
// coordination surface is a milestone-4 gap, so the hook is a
// package-level no-op implementing the checkpoint formal.
func noopCheckpoint() error { return nil }

// Create writes a brand-new empty transaction-1 database at path (Rust
// create_live minus the sidecar, SOW-0025 chunk-6 design record D2): an
// existing destination is refused (ErrorNameExists), the value-kind and
// structure-kind combination is validated (ErrorWrongStructureKind), the
// database id and commit nonce are drawn, the identical txn-1 meta is
// written to both meta pages, flushed and synced. The file is left
// committed and readable by both readers; open it with OpenWriter to
// mutate it. The returned CreateResult reports the truth of the
// immutable-only path: State Created with no sidecar, no identities,
// no basename, and zero reader capacity (the full live-pair surface is
// reported by CreateLive).
func Create(path string, family AddressFamily, kind ValueKind, structure StructureKind, tag ValueTag) (CreateResult, error) {
	created, err := writer.Create(path, uint8(family), uint8(kind), uint8(structure), tag.Wire(), writerNamespaceCheck)
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		Family:        family,
		ValueKind:     kind,
		StructureKind: structure,
		ValueTag:      tag,
		DatabaseID:    created.DatabaseID,
		CommitNonce:   created.CommitNonce,
		State:         CreationStateCreated,
	}, nil
}

// Writer is one opened live writer: the exclusive-lifetime-locked
// read-write mapping of the committed generation (Rust LiveWriter). At
// most one direct transaction is open at a time.
//
// The Writer surface is the recorded off-contract shape (SOW-0027): the
// normative SDK exposes the sidecar-bound LiveWriter instead. The parity
// ledger tracks every Writer symbol as remove-planned; this type
// disappears when the live-writer consolidation lands.
type Writer struct {
	core *writer.Core
}

// coreOf is the mutationHost accessor for the transaction/workflow
// facades (the parity-ledger migration holder).
func (w *Writer) coreOf() *writer.Core { return w.core }

// healthy proves the writer is open and usable (the mutationHost state
// probe on the off-contract consolidation holder): a closed writer
// reports WrongState.
func (w *Writer) healthy() error {
	if w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return w.core.Healthy()
}

// OpenWriter opens path as the single live writer (Rust LiveWriter::open
// minus the sidecar coordination, an accepted divergence (SOW-0025
// chunk-6 design record); the mapping owner's exclusive lifetime lock
// is the writer claim). Readers block on the writer's lock until
// Close. budget declares the draft resource limits; use DefaultBudget
// for the proven values.
func OpenWriter(path string, budget PageBudget) (*Writer, error) {
	core, err := writer.Open(path, budget.internal(), writerNamespaceCheck)
	if err != nil {
		return nil, err
	}
	return &Writer{core: core}, nil
}

// Info reports the selected committed generation (Rust
// WriterCore::base_info mapped to the public DatabaseInfo; after a
// successful open the selection is always ProvenCurrent).
func (w *Writer) Info() (DatabaseInfo, error) {
	if w.core == nil {
		return DatabaseInfo{}, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	wi := w.core.BaseInfo()
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

// BeginDirect opens one ordered direct transaction on a clean writer
// (Rust LiveWriter::begin_direct_transaction): a direct database is
// required, the commit nonce is drawn inside the writer core, and the
// transaction owns every later mutation until Commit or Abort.
func (w *Writer) BeginDirect() (*DirectTransaction, error) {
	if w.core == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if w.core.BaseInfo().ValueKind != format.ValueKindDirect {
		return nil, &format.Error{Code: format.CodeWrongValueKind, Detail: "direct transaction requires a direct database"}
	}
	if err := w.core.BeginDraft(); err != nil {
		return nil, err
	}
	return &DirectTransaction{w: w, active: true}, nil
}

// Close finishes the writer (Rust LiveWriter::close): any open draft is
// discarded with its unpublished tail, the committed generation is
// re-selected and trimmed, and the exclusive lifetime lock is released.
// A second Close is idempotent success exactly like Rust close() on
// State::Closed; every later Writer call reports ErrorWrongState.
func (w *Writer) Close() error {
	if w.core == nil {
		return nil
	}
	plan, err := w.core.PrepareClose()
	if err == nil {
		err = w.core.FinishClose(plan)
	}
	closeErr := w.core.Close()
	w.core = nil
	if err != nil {
		return err
	}
	return closeErr
}

// DirectTransaction is one ordered advanced direct transaction (Rust
// DirectTransaction). Every mutation applies in exact call order; the
// draft is discarded by Commit and Abort and by Writer.Close.
type DirectTransaction struct {
	w      *Writer
	active bool
}

// requireActive mirrors Rust DirectState::require_transaction (the
// operation-nonce gate): every mutation op refuses a spent transaction
// (Rust WrongState("direct transaction is no longer active") when the
// nonce left with the discarded draft). The terminal Commit and Abort do
// not use this gate: Rust commit_attempt and abort have no nonce check
// and report NoPendingTransaction on a draft-less core.
func (t *DirectTransaction) requireActive() error {
	if !t.active || t.w == nil || t.w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	if t.w.core.Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	return nil
}

// requireMutation mirrors Rust LiveWriter::require_direct: no staged
// metadata yet, ordered range, direct value kind, matching family.
func (t *DirectTransaction) requireMutation(family uint8, ordered bool) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if t.w.core.Draft().MetadataStaged() {
		return &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	if !ordered {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"}
	}
	if t.w.core.BaseInfo().ValueKind != format.ValueKindDirect {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "direct mutation requires a direct database"}
	}
	if t.w.core.BaseInfo().AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "direct mutation does not match the database family"}
	}
	return nil
}

// AssignV4 assigns one inclusive IPv4 interval in exact call order (Rust
// DirectTransaction::assign_v4).
func (t *DirectTransaction) AssignV4(from, to IPv4, value uint32) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, err
	}
	changed, err := t.w.core.AssignV4(uint32(from), uint32(to), value)
	if err != nil {
		// Rust LiveWriter::mutate -> abort_after: a failed store op
		// discards the draft, spends the transaction, and reports the
		// TransactionAborted class wrapping the cause.
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// AssignV6 assigns one inclusive IPv6 interval in exact call order (Rust
// DirectTransaction::assign_v6).
func (t *DirectTransaction) AssignV6(from, to IPv6, value uint32) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, err
	}
	changed, err := t.w.core.AssignV6(from.Hi, from.Lo, to.Hi, to.Lo, value)
	if err != nil {
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// ClearV4 clears one inclusive IPv4 interval (Rust
// DirectTransaction::clear_v4).
func (t *DirectTransaction) ClearV4(from, to IPv4) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, err
	}
	changed, err := t.w.core.ClearV4(uint32(from), uint32(to))
	if err != nil {
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// ClearV6 clears one inclusive IPv6 interval (Rust
// DirectTransaction::clear_v6).
func (t *DirectTransaction) ClearV6(from, to IPv6) (bool, error) {
	if err := t.requireMutation(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, err
	}
	changed, err := t.w.core.ClearV6(from.Hi, from.Lo, to.Hi, to.Lo)
	if err != nil {
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// SetMetadataJSON stages one exact metadata replacement in this
// transaction (Rust DirectTransaction::set_metadata_json): the payload is
// bounded by the 20 MiB cap, compressed, and stored as the exact metadata
// chain. At most one metadata stage is allowed per transaction. An
// oversized payload refuses with ErrorInvalidArgument before the store
// (Rust stage_metadata_json position) and the draft survives; a failure
// inside the store aborts the draft like every other mutation error.
func (t *DirectTransaction) SetMetadataJSON(input []byte) (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	if t.w.core.Draft().MetadataStaged() {
		return false, &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	if uint64(len(input)) > format.MaxMetadataUncompressed {
		return false, &format.Error{Code: format.CodeInvalidArgument, Detail: "metadata exceeds 20 MiB"}
	}
	changed, err := t.w.core.SetMetadata(input)
	if err != nil {
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in this transaction (Rust
// DirectTransaction::clear_metadata_json); an already-absent database
// reports false.
func (t *DirectTransaction) ClearMetadataJSON() (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	if t.w.core.Draft().MetadataStaged() {
		return false, &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	changed, err := t.w.core.ClearMetadata()
	if err != nil {
		t.active = false
		return false, t.w.abortAfter(err)
	}
	return changed, nil
}

// CommitStatus classifies one commit outcome (Rust CommitDurability).
type CommitStatus uint8

const (
	CommitNotCommitted   CommitStatus = iota // the commit never reached the file
	CommitCommitted                          // the commit landed durably
	CommitOutcomeUnknown                     // the file may have advanced past this commit
)

// CommitResult is the factual outcome of one commit attempt (Rust
// CommitResult): the durability status, the pinned attempt identity, the
// cause, and - on the live-writer paths - the retained cleanup and
// coordination evidence. The off-contract writer paths leave the cleanup
// fields zero (no sidecar exists there).
type CommitResult struct {
	Status              CommitStatus
	DatabaseID          [16]byte
	TransactionID       uint64
	CommitNonce         [16]byte
	Err                 error
	Cleanup             LiveCommitCleanupArtifacts
	CoordinationCleanup CoordinationCleanup
}

// CleanupState reports whether the commit left coordination residue
// (Rust CommitResult::cleanup_state).
func (r CommitResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// Commit publishes this transaction (Rust DirectTransaction::commit). An
// unchanged transaction is discarded and reports ErrorNoPendingTransaction
// (Rust commit_attempt parity); a transaction whose draft a failed
// operation already aborted also reports ErrorNoPendingTransaction (Rust
// commit_attempt on a draft-less core). A preparation failure returns a
// CommitNotCommitted result carrying the cause with the draft discarded; a
// publication failure reports CommitNotCommitted before the meta write or
// CommitOutcomeUnknown after it. A spent transaction reports
// ErrorNoPendingTransaction.
func (t *DirectTransaction) Commit() (CommitResult, error) {
	if !t.active {
		// Rust commit_attempt reports NoPendingTransaction for a spent
		// transaction (the draft was discarded by Abort, an op failure,
		// or a cancellation).
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if t.w == nil || t.w.core == nil {
		return CommitResult{}, &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	draft := t.w.core.Draft()
	if draft == nil {
		// Rust commit_attempt on a draft-less core reports
		// NoPendingTransaction whatever discarded the draft.
		t.active = false
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if !draft.Changed() {
		if err := t.w.core.DiscardUnpublished(); err != nil {
			t.active = false
			return CommitResult{}, err
		}
		t.active = false
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	attempt, err := t.w.core.CommitAttempt()
	if err != nil {
		t.active = false
		return CommitResult{}, err
	}
	if err := t.w.core.Prepare(noopCheckpoint); err != nil {
		// Rust commit_with: a preparation failure aborts the draft and
		// reports the NotCommitted result carrying the cause wrapped in
		// the TransactionAborted class (code 22); a failed discard
		// nests the CleanupInProgress class (code 64, Rust
		// Error::CleanupIncomplete) exactly like Rust
		// abort_after_source.
		return t.commitAbortAfter(attempt, err), nil
	}
	// Rust commit_locked runs the prepublication checks immediately
	// before publish: the sidecar pair (noop here), the unchanged base,
	// and the locked file covering the draft length. A failure is a
	// BeforePublication abort, same class as a preparation failure, and
	// the cause is wrapped through abort_after with the draft discarded
	// (Rust finish_commit_locked_with).
	if err := t.w.core.RequireUnchangedBase(); err != nil {
		return t.commitAbortAfter(attempt, err), nil
	}
	if err := t.w.core.RequireDraftLength(); err != nil {
		return t.commitAbortAfter(attempt, err), nil
	}
	res := t.w.core.Publish(noopCheckpoint)
	t.active = false
	result := CommitResult{DatabaseID: attempt.DatabaseID, TransactionID: attempt.TransactionID, CommitNonce: attempt.CommitNonce, Err: res.Err}
	switch res.Status {
	case writer.PublishCommitted:
		result.Status = CommitCommitted
	case writer.PublishBeforePublication:
		result.Status = CommitNotCommitted
		result.Err = t.w.abortAfter(res.Err)
	default:
		result.Status = CommitOutcomeUnknown
	}
	return result, nil
}

// abortAfter reports an aborted commit the Rust way
// (abort_after/abort_after_source): the result error class is
// TransactionAborted (code 22); when the abandonment discard also fails,
// the chain nests the CleanupInProgress class (code 64, Rust
// CleanupIncomplete) around the original cause. Both classes stay
// reachable through errors.As on the unwrapped chain.
func (t *DirectTransaction) commitAbortAfter(attempt writer.CommitAttempt, cause error) CommitResult {
	discardErr := t.w.core.DiscardUnpublished()
	t.active = false
	inner := cause
	if discardErr != nil {
		// Rust abort_after_source nests Error::CleanupIncomplete (code
		// CleanupInProgress, 64) around the original cause, brands the
		// writer unusable, and keeps the outer TransactionAborted class
		// as the As target.
		t.w.core.MarkUnresolved(discardErr)
		inner = &abortError{
			class: &format.Error{Code: format.CodeCleanupInProgress, Detail: "commit discard failed"},
			cause: cause,
		}
	}
	return CommitResult{
		Status:        CommitNotCommitted,
		DatabaseID:    attempt.DatabaseID,
		TransactionID: attempt.TransactionID,
		CommitNonce:   attempt.CommitNonce,
		Err: &abortError{
			class: &format.Error{Code: format.CodeTransactionAborted, Detail: "commit aborted after a preparation failure"},
			cause: inner,
		},
	}
}

// abortError carries one declared error class while keeping the wrapped
// cause chain inspectable: errors.As sees the class as a *format.Error,
// and Unwrap exposes the nested cause (Rust
// TransactionAborted(Box<cause>) / CleanupIncomplete{cause}).
type abortError struct {
	class *format.Error
	cause error
}

func (e *abortError) Error() string {
	if e.cause == nil {
		return e.class.Error()
	}
	return e.class.Error() + ": " + e.cause.Error()
}

func (e *abortError) Unwrap() error { return e.cause }

func (e *abortError) As(target any) bool {
	fe, ok := target.(**format.Error)
	if !ok {
		return false
	}
	*fe = e.class
	return true
}

// Abort discards this transaction and its unpublished tail (Rust
// DirectTransaction::abort); the writer stays open and healthy. A
// transaction whose draft a failed operation already aborted reports
// ErrorNoPendingTransaction (Rust LiveWriter::abort has_draft gate). A
// spent transaction reports ErrorNoPendingTransaction.
func (t *DirectTransaction) Abort() error {
	if !t.active {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if t.w == nil || t.w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "direct transaction is no longer active"}
	}
	t.active = false
	if t.w.core.Draft() == nil {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return t.w.core.DiscardUnpublished()
}
