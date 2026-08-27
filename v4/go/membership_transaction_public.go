// Public transaction-bound membership catalog surface (Rust
// live_writer/membership.rs parity): one advanced membership transaction
// over a clean writer. Every mutation applies in exact call order on the
// transaction draft; FeedRef and MembershipRef values pin the creating
// database id, operation nonce, and membership epoch so a reference from
// another transaction or a later epoch is refused (ErrorForeignReference,
// ErrorStaleReference). Commit and Abort spend the transaction and its
// references.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// FeedRef is one SDK-owned feed reference valid only in its creating
// transaction (Rust FeedRef): the database id and operation nonce pin
// the transaction, and the entry carries the feed name and index.
type FeedRef struct {
	databaseID     [16]byte
	operationNonce [16]byte
	entry          writer.FeedEntry
}

// Name returns the feed's current structural name (Rust FeedRef::name).
func (r FeedRef) Name() FeedName { return FeedName(r.entry.Name) }

// Index returns the feed's current structural index (Rust
// FeedRef::index).
func (r FeedRef) Index() uint32 { return r.entry.Index }

// MembershipRef is one SDK-owned membership valid only in its creating
// transaction at the epoch it was produced (Rust MembershipRef): a
// membership reference produced before a feed deletion is stale on
// every later mutation.
type MembershipRef struct {
	databaseID     [16]byte
	operationNonce [16]byte
	handle         writer.MembershipHandle
	catalogEpoch   uint64
}

// TransactionFeedCursor is one ordered transaction-bound feed
// enumeration (Rust TransactionFeedCursor): entries arrive in ascending
// feed-index order and are pinned to the creating transaction.
type TransactionFeedCursor struct {
	cursor         *writer.FeedCursor
	databaseID     [16]byte
	operationNonce [16]byte
}

// Next returns the next transaction-bound feed in ascending feed-index
// order, or ok=false at the end (Rust TransactionFeedCursor::next_feed).
func (c *TransactionFeedCursor) Next() (FeedRef, bool, error) {
	entry, ok, err := c.cursor.Next()
	if err != nil {
		return FeedRef{}, false, publicError(err)
	}
	if !ok {
		return FeedRef{}, false, nil
	}
	return FeedRef{databaseID: c.databaseID, operationNonce: c.operationNonce, entry: entry}, true, nil
}

// MembershipTransaction is one advanced logical membership operation
// over a clean live writer (Rust MembershipTransaction): the transaction
// owns the draft until Commit, Abort, or LiveWriter.Close, and every
// reference produced by it is refused by any other transaction.
type MembershipTransaction struct {
	w               mutationHost
	databaseID      [16]byte
	operationNonce  [16]byte
	membershipEpoch uint64
	cancellation    *CancellationToken
	spent           bool
}

// beginAdvancedTransaction starts one advanced transaction draft on the
// host core (Rust begin_<kind>_state): the kind gate runs before the
// nonce draw. The caller checks cancellation and the host state first,
// in the Rust order.
func beginAdvancedTransaction(core *writer.Core, kind uint8, detail string) ([16]byte, error) {
	if core.BaseInfo().ValueKind != kind {
		return [16]byte{}, &Error{Code: format.CodeWrongValueKind, Detail: detail}
	}
	return core.BeginTransaction()
}

// BeginMembershipTransaction begins one advanced membership transaction
// on a clean live writer (Rust LiveWriter::begin_membership_transaction):
// cancellation is checked first (a fired token classifies as Cancelled
// even on a closed writer), the live writer must be open and healthy, a
// membership database is required, and the operation nonce pins every
// reference produced by the transaction.
func (w *LiveWriter) BeginMembershipTransaction(cancellation *CancellationToken) (*MembershipTransaction, error) {
	if err := cancellation.check(); err != nil {
		return nil, publicError(err)
	}
	if w.lw == nil {
		return nil, &Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := w.lw.Healthy(); err != nil {
		return nil, publicError(err)
	}
	nonce, err := beginAdvancedTransaction(w.coreOf(), uint8(ValueKindMembership), "membership transaction requires a membership database")
	if err != nil {
		return nil, publicError(err)
	}
	return &MembershipTransaction{
		w:              w,
		databaseID:     w.coreOf().BaseInfo().DatabaseID,
		operationNonce: nonce,
		cancellation:   cancellation,
	}, nil
}

// FeedCursor enumerates the current private catalog by ascending feed
// index (Rust MembershipTransaction::feed_cursor).
func (t *MembershipTransaction) FeedCursor() (*TransactionFeedCursor, error) {
	if err := t.requireActive(); err != nil {
		return nil, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return nil, publicError(err)
	}
	cursor, err := t.w.coreOf().CurrentFeedCursor()
	if err != nil {
		return nil, publicError(err)
	}
	return &TransactionFeedCursor{cursor: cursor, databaseID: t.databaseID, operationNonce: t.operationNonce}, nil
}

// EmptyMembership constructs the empty membership without allocating an
// internal id (Rust MembershipTransaction::empty_membership).
func (t *MembershipTransaction) EmptyMembership() (MembershipRef, error) {
	if err := t.requireActive(); err != nil {
		return MembershipRef{}, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, publicError(err)
	}
	return t.membershipReference(writer.EmptyMembershipHandle()), nil
}

// AddFeed adds one feed to a transaction-owned membership (Rust
// MembershipTransaction::add_feed).
func (t *MembershipTransaction) AddFeed(membership MembershipRef, feed FeedRef) (MembershipRef, error) {
	if err := t.requireCurrentMembership(membership); err != nil {
		return MembershipRef{}, publicError(err)
	}
	if err := t.requireCurrentFeed(feed); err != nil {
		return MembershipRef{}, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, publicError(err)
	}
	var handle writer.MembershipHandle
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		handle, err = edit.AddFeedToMembership(membership.handle, feed.entry)
		return publicError(err)
	})
	if err != nil {
		return MembershipRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return MembershipRef{}, publicError(err)
	}
	return t.membershipReference(handle), nil
}

// ApplyV4 applies one membership operation to an inclusive IPv4
// interval (Rust MembershipTransaction::apply_v4): the range must be
// ordered, the membership reference current, and the database family
// must match. The per-cell transform checkpoint is a no-op, exactly like
// Rust apply_membership_handle; cancellation is checked before and after
// the mutate, never inside the cell transform.
func (t *MembershipTransaction) ApplyV4(from, to IPv4, membership MembershipRef, operation MembershipOperation) (bool, error) {
	if err := t.requireFamily(format.AddressFamilyIPv4, from <= to); err != nil {
		return false, publicError(err)
	}
	if err := t.requireCurrentMembership(membership); err != nil {
		return false, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	var changed bool
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		changed, err = edit.ApplyMembershipV4(uint32(from), uint32(to), membership.handle, writer.MembershipOperation(operation), noopCheckpoint)
		return publicError(err)
	})
	if err != nil {
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	return changed, nil
}

// ApplyV6 applies one membership operation to an inclusive IPv6
// interval (Rust MembershipTransaction::apply_v6).
func (t *MembershipTransaction) ApplyV6(from, to IPv6, membership MembershipRef, operation MembershipOperation) (bool, error) {
	if err := t.requireFamily(format.AddressFamilyIPv6, from.Hi < to.Hi || (from.Hi == to.Hi && from.Lo <= to.Lo)); err != nil {
		return false, publicError(err)
	}
	if err := t.requireCurrentMembership(membership); err != nil {
		return false, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	var changed bool
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		changed, err = edit.ApplyMembershipV6(from.Hi, from.Lo, to.Hi, to.Lo, membership.handle, writer.MembershipOperation(operation), noopCheckpoint)
		return publicError(err)
	})
	if err != nil {
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	return changed, nil
}

// LookupFeed returns an exact existing feed without creating it (Rust
// MembershipTransaction::lookup_feed).
func (t *MembershipTransaction) LookupFeed(name FeedName) (FeedRef, bool, error) {
	if !format.FeedNameValidString(string(name)) {
		return FeedRef{}, false, &Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireActive(); err != nil {
		return FeedRef{}, false, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, false, publicError(err)
	}
	var entry writer.FeedEntry
	var found bool
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		entry, found, err = edit.LookupFeed(string(name))
		return publicError(err)
	})
	if err != nil {
		return FeedRef{}, false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, false, publicError(err)
	}
	if !found {
		return FeedRef{}, false, nil
	}
	return t.reference(entry), true, nil
}

// EnsureFeed returns the exact feed, creating it at the lowest free
// index if absent (Rust MembershipTransaction::ensure_feed).
func (t *MembershipTransaction) EnsureFeed(name FeedName) (FeedRef, error) {
	if !format.FeedNameValidString(string(name)) {
		return FeedRef{}, &Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireActive(); err != nil {
		return FeedRef{}, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, publicError(err)
	}
	var entry writer.FeedEntry
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		entry, _, err = edit.EnsureFeed(string(name))
		return publicError(err)
	})
	if err != nil {
		return FeedRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, publicError(err)
	}
	return t.reference(entry), nil
}

// RenameFeed renames one referenced feed while preserving its
// membership (Rust MembershipTransaction::rename_feed): the new name
// must not exist (ErrorNameExists) and the feed reference must be
// current.
func (t *MembershipTransaction) RenameFeed(feed FeedRef, newName FeedName) (FeedRef, error) {
	if !format.FeedNameValidString(string(newName)) {
		return FeedRef{}, &Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := t.requireCurrentFeed(feed); err != nil {
		return FeedRef{}, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, publicError(err)
	}
	var entry writer.FeedEntry
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		entry, err = edit.RenameCurrentFeed(feed.entry, string(newName))
		return publicError(err)
	})
	if err != nil {
		return FeedRef{}, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return FeedRef{}, publicError(err)
	}
	return t.reference(entry), nil
}

// DeleteFeed deletes one feed and clears its bit from every stored
// membership (Rust MembershipTransaction::delete_feed): every membership
// reference produced before this call becomes stale.
func (t *MembershipTransaction) DeleteFeed(feed FeedRef) error {
	if err := t.requireCurrentFeed(feed); err != nil {
		return publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return publicError(err)
	}
	nextEpoch := t.membershipEpoch + 1
	if nextEpoch == 0 {
		return &Error{Code: format.CodeArithmeticOverflow, Detail: "membership reference epoch"}
	}
	err := t.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.DeleteCurrentFeedMembership(feed.entry, t.cancellation.check)
	})
	if err != nil {
		return t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return publicError(err)
	}
	t.membershipEpoch = nextEpoch
	return nil
}

// SetMetadataJSON stages one exact opaque metadata replacement in this
// transaction (Rust MembershipTransaction::set_metadata_json).
func (t *MembershipTransaction) SetMetadataJSON(input []byte) (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	changed, err := t.w.coreOf().SetMetadata(input)
	if err != nil {
		// Rust stage_metadata_json raises the already-staged WrongState
		// and the 20 MiB cap before mutate; those stay raw. Errors from
		// inside the edit abort the transaction.
		if metadataStagePreCheck(err) {
			return false, publicError(err)
		}
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in this transaction (Rust
// MembershipTransaction::clear_metadata_json); an already-absent
// database reports false.
func (t *MembershipTransaction) ClearMetadataJSON() (bool, error) {
	if err := t.requireActive(); err != nil {
		return false, publicError(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	changed, err := t.w.coreOf().ClearMetadata()
	if err != nil {
		// Rust stage_clear_metadata_json raises the already-staged
		// WrongState before mutate; that stays raw. Errors from inside
		// the edit abort the transaction.
		if metadataStagePreCheck(err) {
			return false, publicError(err)
		}
		return false, t.abortEdit(err)
	}
	if err := t.checkOrAbort(); err != nil {
		return false, publicError(err)
	}
	return changed, nil
}

// Commit publishes this transaction through the alternate metadata page
// (Rust MembershipTransaction::commit). Commit on a spent transaction
// (aborted by Abort, a failed operation, or a fired cancellation)
// reports ErrorNoPendingTransaction (Rust commit_attempt parity).
func (t *MembershipTransaction) Commit() (CommitResult, error) {
	if t.spent {
		// Rust commit_attempt reports NoPendingTransaction for a spent
		// transaction (the draft was discarded by an abort, an op
		// failure, or a cancellation).
		return CommitResult{}, &Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := t.requireActive(); err != nil {
		return CommitResult{}, publicError(err)
	}
	return t.w.commitPrepared(t.cancellation, func() { t.spent = true }, "membership transaction")
}

// Abort discards this transaction and invalidates all of its references
// (Rust MembershipTransaction::abort); the writer stays open and
// healthy. A committed transaction reports ErrorNoPendingTransaction.
func (t *MembershipTransaction) Abort() error {
	t.spent = true
	return t.w.Abort()
}

// reference builds one transaction-pinned feed reference (Rust
// MembershipState::reference).
func (t *MembershipTransaction) reference(entry writer.FeedEntry) FeedRef {
	return FeedRef{databaseID: t.databaseID, operationNonce: t.operationNonce, entry: entry}
}

// membershipReference builds one transaction-pinned membership reference
// at the current epoch (Rust MembershipState::membership_reference).
func (t *MembershipTransaction) membershipReference(handle writer.MembershipHandle) MembershipRef {
	return MembershipRef{
		databaseID:     t.databaseID,
		operationNonce: t.operationNonce,
		handle:         handle,
		catalogEpoch:   t.membershipEpoch,
	}
}

// requireActive mirrors Rust MembershipState::require_active: the
// transaction must still own the open operation and the writer must be
// healthy.
func (t *MembershipTransaction) requireActive() error {
	if t.spent {
		return &Error{Code: format.CodeWrongState, Detail: "membership transaction is no longer active"}
	}
	// Rust require_transaction reports the stale transaction before the
	// closed writer; the core nil check only guards the nonce probe.
	if core := t.w.coreOf(); core != nil && !core.OperationIs(t.operationNonce) {
		return &Error{Code: format.CodeWrongState, Detail: "membership transaction is no longer active"}
	}
	if t.w.coreOf() == nil {
		return &Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	return t.w.coreOf().Healthy()
}

// checkOrAbort mirrors Rust MembershipState::check_or_abort: the
// transaction must be active and the captured cancellation must not have
// fired; a fired cancellation aborts the workflow through the writer.
func (t *MembershipTransaction) checkOrAbort() error {
	if err := t.requireActive(); err != nil {
		return publicError(err)
	}
	if err := t.cancellation.check(); err != nil {
		t.spent = true
		return t.w.abortAfter(err)
	}
	return nil
}

// abortEdit mirrors Rust LiveWriter::mutate: an error raised inside the
// edit (after the pre-mutate require checks) discards the draft, spends
// the transaction, and reports TransactionAborted wrapping the cause
// (Rust live_writer.rs mutate -> abort_after; Io/Format causes
// additionally brand the writer unusable).
func (t *MembershipTransaction) abortEdit(err error) error {
	t.spent = true
	return publicError(t.w.abortAfter(err))
}

// requireCurrentFeed mirrors Rust MembershipState::require_current_feed:
// the reference must belong to this transaction and name the exact feed
// currently in the draft catalog.
func (t *MembershipTransaction) requireCurrentFeed(feed FeedRef) error {
	if err := t.requireReference(feed); err != nil {
		return publicError(err)
	}
	current, found, err := t.w.coreOf().LookupCurrentFeed(feed.entry.Name)
	if err != nil {
		return publicError(err)
	}
	if !found || current != feed.entry {
		return &Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireCurrentMembership mirrors Rust
// MembershipState::require_current_membership: the membership reference
// must belong to this transaction and the current membership epoch.
func (t *MembershipTransaction) requireCurrentMembership(membership MembershipRef) error {
	if err := t.requireActive(); err != nil {
		return publicError(err)
	}
	if membership.databaseID != t.databaseID {
		return &Error{Code: format.CodeForeignReference, Detail: "operation reference belongs to another transaction"}
	}
	if membership.operationNonce != t.operationNonce {
		return &Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	if membership.catalogEpoch != t.membershipEpoch {
		return &Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireReference mirrors Rust MembershipState::require_reference: the
// feed reference must belong to this transaction's database and
// operation.
func (t *MembershipTransaction) requireReference(feed FeedRef) error {
	if err := t.requireActive(); err != nil {
		return publicError(err)
	}
	if feed.databaseID != t.databaseID {
		return &Error{Code: format.CodeForeignReference, Detail: "operation reference belongs to another transaction"}
	}
	if feed.operationNonce != t.operationNonce {
		return &Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
	}
	return nil
}

// requireFamily mirrors Rust MembershipState::require_family: the active
// transaction, the ordered range, and the database address family.
func (t *MembershipTransaction) requireFamily(family uint8, ordered bool) error {
	if err := t.requireActive(); err != nil {
		return publicError(err)
	}
	if !ordered {
		return &Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"}
	}
	if t.w.coreOf().BaseInfo().AddressFamily != family {
		return &Error{Code: format.CodeWrongAddressFamily, Detail: "membership mutation does not match the database family"}
	}
	return nil
}
