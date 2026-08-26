// Public transaction-bound typed structure surface (Rust
// live_writer/structured.rs parity): one advanced structured transaction
// over a clean writer. The transaction reuses the membership state
// machinery (feeds, memberships, reference epoch), adds typed
// network_enrichment_v1 interning with optional threat membership, and
// assigns/clears structures over inclusive ranges. StructureRef values
// pin the creating database id, operation nonce, and reference epoch so
// a reference from another transaction or a later epoch is refused
// (ErrorForeignReference, ErrorStaleReference). Commit and Abort spend
// the transaction and its references.

package iprangedb

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// StructureRef is one SDK-owned structure valid only in its creating
// transaction (Rust StructureRef): the database id, operation nonce, and
// reference epoch pin the transaction, and the handle is the internal
// structure dictionary id.
type StructureRef struct {
	databaseID     [16]byte
	operationNonce [16]byte
	handle         writer.StructureHandle
	catalogEpoch   uint64
}

// StructuredTransaction is one advanced typed structure operation over a
// clean live writer (Rust StructuredTransaction): the transaction owns
// the draft until Commit, Abort, or Writer.Close, and every reference
// produced by it is refused by any other transaction. The transaction
// also serves the membership catalog operations (feeds, memberships)
// that structured values link to.
type StructuredTransaction struct {
	w               mutationHost
	edit            *writer.WriterEdit
	databaseID      [16]byte
	operationNonce  [16]byte
	membershipEpoch uint64
	cancellation    *CancellationToken
	spent           bool
	inputV4         writer.AssignmentInput
	inputV6         writer.AssignmentInput
}

// BeginStructuredTransaction begins one advanced structured transaction
// on a clean writer (Rust LiveWriter::begin_structured_transaction): a
// network_enrichment_v1 structured database is required and the
// operation nonce pins every reference produced by the transaction. The
// guard order is the Rust exact sequence: the closed-writer probe (a
// state Rust cannot express because it consumes the writer), then the
// structure-kind outer guard, then cancellation, healthy, and the
// value-kind inner guard. The draft installed by BeginTransaction is
// bound once so every operation reuses one edit (Rust writer.mutate
// borrows the draft for the transaction lifetime); each input locator
// is built for its own literal family like the Rust typed assignment
// inputs, so an IPv4 database carries no dead IPv6 locator.
func (w *Writer) BeginStructuredTransaction(cancellation *CancellationToken) (*StructuredTransaction, error) {
	return beginStructuredTransaction(w, cancellation)
}

// BeginStructuredTransaction begins one advanced structured transaction
// on a clean live writer (Rust LiveWriter::begin_structured_transaction):
// the Go-only closed-writer probe precedes the structure-kind gate (Rust
// checks the kind before any writer state), then cancellation, then the
// live writer open/healthy probe, then the value-kind gate; the operation
// nonce pins every reference produced by the transaction.
func (w *LiveWriter) BeginStructuredTransaction(cancellation *CancellationToken) (*StructuredTransaction, error) {
	return beginStructuredTransaction(w, cancellation)
}

// beginStructuredTransaction is the shared host-based begin (Rust
// begin_structured_transaction): the closed-writer probe (a state Rust
// cannot express because it consumes the writer), then the
// structure-kind outer guard, then cancellation, then the open/healthy
// probe, and the value-kind inner guard. The draft installed by
// BeginTransaction is bound once so every operation reuses one edit
// (Rust writer.mutate borrows the draft for the transaction lifetime);
// each input locator is built for its own literal family like the Rust
// typed assignment inputs, so an IPv4 database carries no dead IPv6
// locator.
func beginStructuredTransaction(h mutationHost, cancellation *CancellationToken) (*StructuredTransaction, error) {
	if h.coreOf() == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if h.coreOf().BaseInfo().StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return nil, &format.Error{Code: format.CodeWrongStructureKind, Detail: "no typed transaction exists for this structure kind"}
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if err := h.healthy(); err != nil {
		return nil, err
	}
	if h.coreOf().BaseInfo().ValueKind != format.ValueKindStructured {
		return nil, &format.Error{Code: format.CodeWrongValueKind, Detail: "structured transaction requires a structured database"}
	}
	nonce, err := h.coreOf().BeginTransaction()
	if err != nil {
		return nil, err
	}
	edit, err := h.coreOf().BindEdit()
	if err != nil {
		return nil, err
	}
	return &StructuredTransaction{
		w:              h,
		edit:           edit,
		databaseID:     h.coreOf().BaseInfo().DatabaseID,
		operationNonce: nonce,
		cancellation:   cancellation,
		inputV4:        writer.NewAssignmentInput(format.AddressFamilyIPv4, h.coreOf().Budget().MaxHeapBytes),
		inputV6:        writer.NewAssignmentInput(format.AddressFamilyIPv6, h.coreOf().Budget().MaxHeapBytes),
	}, nil
}

// FeedCursor enumerates the current private catalog by ascending feed
// index (Rust StructuredTransaction::feed_cursor).
func (t *StructuredTransaction) FeedCursor() (*TransactionFeedCursor, error) {
	if err := t.requireActive(); err != nil {
		return nil, err
	}
	if err := t.checkOrAbort(); err != nil {
		return nil, err
	}
	cursor, err := t.w.coreOf().CurrentFeedCursor()
	if err != nil {
		return nil, err
	}
	return &TransactionFeedCursor{cursor: cursor, databaseID: t.databaseID, operationNonce: t.operationNonce}, nil
}

// LookupFeed returns an exact existing feed without creating it (Rust
// StructuredTransaction::lookup_feed).
func (t *StructuredTransaction) LookupFeed(name FeedName) (FeedRef, bool, error) {
	if !format.FeedNameValidString(string(name)) {
		return FeedRef{}, false, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireActive(); err != nil {
		return FeedRef{}, false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, false, err
	}
	entry, found, err := t.edit.LookupFeed(string(name))
	if err != nil {
		return FeedRef{}, false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, false, err
	}
	if !found {
		return FeedRef{}, false, nil
	}
	return t.reference(entry), true, nil
}

// EnsureFeed returns the exact feed, creating it at the lowest free
// index if absent (Rust StructuredTransaction::ensure_feed).
func (t *StructuredTransaction) EnsureFeed(name FeedName) (FeedRef, error) {
	if !format.FeedNameValidString(string(name)) {
		return FeedRef{}, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireActive(); err != nil {
		return FeedRef{}, err
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, err
	}
	entry, _, err := t.edit.EnsureFeed(string(name))
	if err != nil {
		return FeedRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, err
	}
	return t.reference(entry), nil
}

// RenameFeed renames one referenced feed while preserving its
// membership (Rust StructuredTransaction::rename_feed).
func (t *StructuredTransaction) RenameFeed(feed FeedRef, newName FeedName) (FeedRef, error) {
	if !format.FeedNameValidString(string(newName)) {
		return FeedRef{}, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireCurrentFeed(feed); err != nil {
		return FeedRef{}, err
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, err
	}
	entry, err := t.edit.RenameCurrentFeed(feed.entry, string(newName))
	if err != nil {
		return FeedRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, err
	}
	return t.reference(entry), nil
}

// DeleteFeed deletes one feed and removes it from every stored structure
// payload (Rust StructuredTransaction::delete_feed): every membership
// and structure reference produced before this call becomes stale.
func (t *StructuredTransaction) DeleteFeed(feed FeedRef) error {
	if err := t.requireCurrentFeed(feed); err != nil {
		return err
	}
	if err := t.checkOrAbort(); err != nil {
		return err
	}
	if err := t.edit.DeleteCurrentStructuredFeed(feed.entry, t.cancellation.check); err != nil {
		return t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return err
	}
	nextEpoch := t.membershipEpoch + 1
	if nextEpoch == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership reference epoch"}
	}
	t.membershipEpoch = nextEpoch
	return nil
}

// EmptyMembership constructs the empty threat membership without
// allocating an internal id (Rust StructuredTransaction::empty_membership).
func (t *StructuredTransaction) EmptyMembership() (MembershipRef, error) {
	if err := t.requireActive(); err != nil {
		return MembershipRef{}, err
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, err
	}
	return t.membershipReference(writer.EmptyMembershipHandle()), nil
}

// AddFeed adds one feed to a transaction-owned membership (Rust
// StructuredTransaction::add_feed).
func (t *StructuredTransaction) AddFeed(membership MembershipRef, feed FeedRef) (MembershipRef, error) {
	if err := t.requireCurrentMembership(membership); err != nil {
		return MembershipRef{}, err
	}
	if err := t.requireCurrentFeed(feed); err != nil {
		return MembershipRef{}, err
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, err
	}
	handle, err := t.edit.AddFeedToMembership(membership.handle, feed.entry)
	if err != nil {
		return MembershipRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, err
	}
	return t.membershipReference(handle), nil
}

// InternNetworkEnrichmentV1 interns one typed enrichment value with the
// optional threat membership and returns its transaction-bound reference
// (Rust StructuredTransaction::intern_network_enrichment_v1): equal
// payloads deduplicate to the same reference.
func (t *StructuredTransaction) InternNetworkEnrichmentV1(value NetworkEnrichmentV1, membership MembershipRef) (StructureRef, error) {
	if err := t.requireActive(); err != nil {
		return StructureRef{}, err
	}
	// Rust: Some(membership) validates the reference and supplies its
	// handle; None interns with the empty handle. The Go zero
	// MembershipRef is the None case. Every other value is a reference
	// some transaction produced (including EmptyMembership, whose handle
	// is zero but whose database id and nonce are pinned) and validates
	// exactly like Rust's Some, so a stale or foreign reference is
	// refused even when it carries the empty handle.
	if membership != (MembershipRef{}) {
		if err := t.requireCurrentMembership(membership); err != nil {
			return StructureRef{}, err
		}
	}
	structure, err := t.edit.InternNetworkEnrichmentV1(value.internal(), membership.handle)
	if err != nil {
		return StructureRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return StructureRef{}, err
	}
	return t.structureReference(structure), nil
}

// AssignV4 applies one structure to an inclusive IPv4 interval (Rust
// StructuredTransaction::assign_v4): the range must be ordered, the
// structure reference current, and the database family must match.
func (t *StructuredTransaction) AssignV4(from, to IPv4, structure StructureRef) (bool, error) {
	if err := t.requireStructureFamily(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, err
	}
	if err := t.requireCurrentStructure(structure); err != nil {
		return false, err
	}
	changed, err := t.edit.AssignStructureInputV4(uint32(from), uint32(to), structure.handle, &t.inputV4)
	if err != nil {
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// AssignV6 applies one structure to an inclusive IPv6 interval (Rust
// StructuredTransaction::assign_v6).
func (t *StructuredTransaction) AssignV6(from, to IPv6, structure StructureRef) (bool, error) {
	if err := t.requireStructureFamily(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, err
	}
	if err := t.requireCurrentStructure(structure); err != nil {
		return false, err
	}
	changed, err := t.edit.AssignStructureInputV6(from.Hi, from.Lo, to.Hi, to.Lo, structure.handle, &t.inputV6)
	if err != nil {
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// ClearV4 clears one inclusive IPv4 interval (Rust
// StructuredTransaction::clear_v4).
func (t *StructuredTransaction) ClearV4(from, to IPv4) (bool, error) {
	empty := t.structureReference(writer.EmptyStructureHandle())
	return t.AssignV4(from, to, empty)
}

// ClearV6 clears one inclusive IPv6 interval (Rust
// StructuredTransaction::clear_v6).
func (t *StructuredTransaction) ClearV6(from, to IPv6) (bool, error) {
	empty := t.structureReference(writer.EmptyStructureHandle())
	return t.AssignV6(from, to, empty)
}

// SetMetadataJSON stages one exact opaque metadata replacement in this
// transaction (Rust StructuredTransaction::set_metadata_json).
func (t *StructuredTransaction) SetMetadataJSON(input []byte) (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.edit.SetMetadata(input)
	if err != nil {
		// Rust stage_metadata_json raises the already-staged WrongState
		// and the 20 MiB cap before mutate; those stay raw. Errors from
		// inside the edit abort the transaction.
		if metadataStagePreCheck(err) {
			return false, err
		}
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in this transaction (Rust
// StructuredTransaction::clear_metadata_json); an already-absent
// database reports false.
func (t *StructuredTransaction) ClearMetadataJSON() (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, err
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	changed, err := t.edit.ClearMetadata()
	if err != nil {
		// Rust stage_clear_metadata_json raises the already-staged
		// WrongState before mutate; that stays raw. Errors from inside
		// the edit abort the transaction.
		if metadataStagePreCheck(err) {
			return false, err
		}
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, err
	}
	return changed, nil
}

// Commit publishes this transaction through the alternate metadata page
// (Rust StructuredTransaction::commit). Commit on a spent transaction
// (aborted by Abort, a failed operation, or a fired cancellation)
// reports ErrorNoPendingTransaction (Rust commit_attempt parity).
func (t *StructuredTransaction) Commit() (CommitResult, error) {
	if t.spent {
		// Rust commit_attempt reports NoPendingTransaction for a spent
		// transaction (the draft was discarded by an abort, an op
		// failure, or a cancellation).
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := t.requireActive(); err != nil {
		return CommitResult{}, err
	}
	return t.w.commitPrepared(t.cancellation, func() { t.spent = true }, "structured transaction")
}

// Abort discards this transaction and invalidates all of its references
// (Rust StructuredTransaction::abort); the writer stays open and
// healthy. A committed transaction reports ErrorNoPendingTransaction.
func (t *StructuredTransaction) Abort() error {
	t.spent = true
	return t.w.Abort()
}

// reference builds one transaction-pinned feed reference (Rust
// MembershipState::reference).
func (t *StructuredTransaction) reference(entry writer.FeedEntry) FeedRef {
	return FeedRef{databaseID: t.databaseID, operationNonce: t.operationNonce, entry: entry}
}

// membershipReference builds one transaction-pinned membership reference
// at the current epoch (Rust MembershipState::membership_reference).
func (t *StructuredTransaction) membershipReference(handle writer.MembershipHandle) MembershipRef {
	return MembershipRef{
		databaseID:     t.databaseID,
		operationNonce: t.operationNonce,
		handle:         handle,
		catalogEpoch:   t.membershipEpoch,
	}
}

// structureReference builds one transaction-pinned structure reference at
// the current reference epoch (Rust MembershipState::structure_reference).
func (t *StructuredTransaction) structureReference(handle writer.StructureHandle) StructureRef {
	return StructureRef{
		databaseID:     t.databaseID,
		operationNonce: t.operationNonce,
		handle:         handle,
		catalogEpoch:   t.membershipEpoch,
	}
}

// requireActive mirrors Rust MembershipState::require_active: the
// transaction must still own the open operation and the writer must be
// healthy. The detail string is the Rust membership INACTIVE constant:
// the structured transaction reuses the membership state machinery and
// therefore reports the membership wording (Rust exact).
func (t *StructuredTransaction) requireActive() error {
	if t.spent {
		return &format.Error{Code: format.CodeWrongState, Detail: "membership transaction is no longer active"}
	}
	// Rust require_transaction reports the stale transaction before the
	// closed writer; the host probe only guards the nonce check.
	if t.w.coreOf() != nil && !t.w.coreOf().OperationIs(t.operationNonce) {
		return &format.Error{Code: format.CodeWrongState, Detail: "membership transaction is no longer active"}
	}
	return t.w.healthy()
}

// checkOrAbort mirrors Rust MembershipState::check_or_abort: the
// transaction must be active and the captured cancellation must not have
// fired; a fired cancellation aborts the workflow through the writer.
func (t *StructuredTransaction) checkOrAbort() error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if err := t.cancellation.check(); err != nil {
		t.spent = true
		return t.w.abortAfter(err)
	}
	return nil
}

// metadataStagePreCheck reports whether an edit error is one of the
// metadata stage checks Rust raises before mutate (Rust
// LiveWriter::stage_metadata_json and stage_clear_metadata_json): the
// already-staged WrongState and the 20 MiB InvalidArgument cap stay raw
// and the transaction survives. Every error raised inside the edit
// aborts the transaction instead.
func metadataStagePreCheck(err error) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == format.CodeWrongState || fe.Code == format.CodeInvalidArgument
}

// abortEdit mirrors Rust LiveWriter::mutate: an error raised inside the
// edit (after the pre-mutate require checks) discards the draft, spends
// the transaction, and reports TransactionAborted wrapping the cause
// (Rust live_writer.rs mutate -> abort_after; Io/Format causes
// additionally brand the writer unusable). The bound edit is stale
// after the discard; spent blocks every further use.
func (t *StructuredTransaction) abortEdit(err error) error {
	t.spent = true
	return t.w.abortAfter(err)
}

// requireCurrentFeed mirrors Rust MembershipState::require_current_feed.
func (t *StructuredTransaction) requireCurrentFeed(feed FeedRef) error {
	if err := t.requireReference(feed); err != nil {
		return err
	}
	current, found, err := t.w.coreOf().LookupCurrentFeed(feed.entry.Name)
	if err != nil {
		return err
	}
	if !found || current != feed.entry {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireCurrentMembership mirrors Rust
// MembershipState::require_current_membership.
func (t *StructuredTransaction) requireCurrentMembership(membership MembershipRef) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if membership.databaseID != t.databaseID {
		return &format.Error{Code: format.CodeForeignReference, Detail: "operation reference belongs to another transaction"}
	}
	if membership.operationNonce != t.operationNonce {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	if membership.catalogEpoch != t.membershipEpoch {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireCurrentStructure mirrors Rust
// MembershipState::require_current_structure: the structure reference
// must belong to this transaction and the current reference epoch.
func (t *StructuredTransaction) requireCurrentStructure(structure StructureRef) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if structure.databaseID != t.databaseID {
		return &format.Error{Code: format.CodeForeignReference, Detail: "operation reference belongs to another transaction"}
	}
	if structure.operationNonce != t.operationNonce {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	if structure.catalogEpoch != t.membershipEpoch {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireReference mirrors Rust MembershipState::require_reference: the
// feed reference must belong to this transaction's database and
// operation.
func (t *StructuredTransaction) requireReference(feed FeedRef) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if feed.databaseID != t.databaseID {
		return &format.Error{Code: format.CodeForeignReference, Detail: "operation reference belongs to another transaction"}
	}
	if feed.operationNonce != t.operationNonce {
		return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireStructureFamily mirrors Rust
// MembershipState::require_structure_family: the active transaction, the
// ordered range, and the database address family.
func (t *StructuredTransaction) requireStructureFamily(family uint8, ordered bool) error {
	if err := t.requireActive(); err != nil {
		return err
	}
	if !ordered {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"}
	}
	if t.w.coreOf().BaseInfo().AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "structured mutation does not match the database family"}
	}
	return nil
}

// internal converts the public value to the canonical payload struct
// (the reader's optional location mirrors Rust's Option as the
// HasLocation flag, decision 5A).
func (v NetworkEnrichmentV1) internal() format.NetworkEnrichmentV1 {
	flags := uint32(0)
	if v.HasLocation {
		flags = format.NetworkEnrichmentV1HasLocation
	}
	return format.NetworkEnrichmentV1{
		ASN:                   v.ASN,
		CountryID:             v.CountryID,
		StateID:               v.StateID,
		CityID:                v.CityID,
		LatitudeMicrodegrees:  v.Location.LatitudeMicrodegrees,
		LongitudeMicrodegrees: v.Location.LongitudeMicrodegrees,
		Flags:                 flags,
	}
}
