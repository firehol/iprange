// Core-level exact-workflow state, the edit binding, and the workflow
// finish helpers (Rust writer_core.rs, writer_core/edit.rs,
// draft_store/workflow.rs): the public workflows compose these over the
// installed draft exactly like the Rust live writer, and the ordered
// merges finish their workflow here.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// HasDraft reports whether one COW draft is installed (Rust
// WriterCore::has_draft).
func (c *Core) HasDraft() bool { return c.draft != nil }

// DraftChanged reports whether the installed draft mutated persistent
// content (Rust WriterCore::draft_changed).
func (c *Core) DraftChanged() bool { return c.draft != nil && c.draft.changed }

// WorkflowInputOpen reports the draft accepts workflow input records
// (Rust WriterCore::workflow_input_open).
func (c *Core) WorkflowInputOpen() bool { return c.draft != nil && c.draft.workflowInputOpen() }

// WorkflowActive reports any exact workflow state (Rust
// WriterCore::workflow_active).
func (c *Core) WorkflowActive() bool { return c.draft != nil && c.draft.workflowActive() }

// MetadataStaged reports one metadata stage was already applied (Rust
// WriterCore::metadata_staged).
func (c *Core) MetadataStaged() bool { return c.draft != nil && c.draft.metadataStaged }

// OperationAbandoned reports the prepared operation was abandoned (Rust
// WriterCore::operation_abandoned).
func (c *Core) OperationAbandoned() bool { return c.draft != nil && c.draft.operationAbandoned() }

// OperationIs reports whether the open draft is the operation that drew
// nonce (Rust WriterCore::operation_is).
func (c *Core) OperationIs(nonce [16]byte) bool {
	return c.draft != nil && c.draft.meta.CommitNonce == nonce
}

// AbandonOperation brands the open draft abandoned (Rust
// WriterCore::abandon_operation).
func (c *Core) AbandonOperation() {
	if c.draft != nil {
		c.draft.abandonOperation()
	}
}

// BeginRangeWorkflow starts one exact range workflow: a new transaction
// whose range tree is detached to a private empty tree (Rust
// WriterCore::begin_range_workflow; the new transaction is undone when
// the workflow start fails).
func (c *Core) BeginRangeWorkflow() error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if err := c.BeginDraft(); err != nil {
		return err
	}
	if err := c.draft.beginRangeWorkflow(); err != nil {
		c.draft = nil
		return err
	}
	return nil
}

// BeginMembershipWorkflow starts one exact membership workflow (Rust
// WriterCore::begin_membership_workflow).
func (c *Core) BeginMembershipWorkflow() error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if err := c.BeginDraft(); err != nil {
		return err
	}
	if err := c.draft.beginMembershipWorkflow(); err != nil {
		c.draft = nil
		return err
	}
	return nil
}

// Mutate runs one operation over a fresh WriterEdit binding, starting a
// plain transaction when none is open (Rust WriterCore::edit). The store
// is a stateless view over (mapping, committed page count, budget,
// draft), so binding it per operation is semantically identical to the
// Rust edit core that holds one store for the draft lifetime.
func (c *Core) Mutate(operation func(edit *WriterEdit) error) error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if c.draft == nil {
		if err := c.BeginDraft(); err != nil {
			return err
		}
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	return operation(newWriterEdit(store, c.base.Meta))
}

// BindEdit returns one edit binding over the installed draft (Rust
// writer_core/edit.rs WriterCore::edit core that holds one store for
// the draft lifetime). Per-operation Mutate bindings allocate; a
// workflow that streams many batches (feed slice ingestion) binds once
// and reuses the edit, so the hot slice path allocates nothing per
// batch. The draft must already be installed (workflow preconditions
// guarantee it); the committed base and page count are stable for the
// draft lifetime, exactly like Mutate's per-call binding.
func (c *Core) BindEdit() (*WriterEdit, error) {
	if err := c.requireHealthy(); err != nil {
		return nil, err
	}
	if c.draft == nil {
		return nil, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return newWriterEdit(NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft), c.base.Meta), nil
}

// WriterEdit is one edit binding over the open draft (Rust
// writer_core/edit.rs WriterEdit: DraftStore plus the committed base).
type WriterEdit struct {
	store *DraftStore
	base  format.Meta
}

func newWriterEdit(store *DraftStore, base format.Meta) *WriterEdit {
	return &WriterEdit{store: store, base: base}
}

// PrepareHistoryFrom validates the history windows, interns every
// destination feed, and prepares the projection plan (Rust
// WriterEdit::prepare_history_from).
func (e *WriterEdit) PrepareHistoryFrom(windows []HistoryWindow, check func() error) (*historyPlan, error) {
	return prepareHistoryPlan(e.store, windows, check)
}

// BeginHistory4 starts the IPv4 projection merge over the committed
// destination (Rust WriterEdit::begin_history over Ipv4Key).
func (e *WriterEdit) BeginHistory4(plan *historyPlan, check func() error) (*historyMerge[key4], error) {
	merge4, _, err := plan.begin(e.store, e.base, check)
	return merge4, err
}

// BeginHistory6 starts the IPv6 projection merge over the committed
// destination (Rust WriterEdit::begin_history over Ipv6Key).
func (e *WriterEdit) BeginHistory6(plan *historyPlan, check func() error) (*historyMerge[key6], error) {
	_, merge6, err := plan.begin(e.store, e.base, check)
	return merge6, err
}

// PushHistory4 feeds one inclusive IPv4 source range into the
// projection merge (Rust WriterEdit::push_history over Ipv4Key).
func (e *WriterEdit) PushHistory4(merge *historyMerge[key4], from, to uint32, lastSeen uint32, check func() error) error {
	return merge.push(e.store, key4(from), key4(to), lastSeen, check)
}

// PushHistory6 feeds one inclusive IPv6 source range into the
// projection merge (Rust WriterEdit::push_history over Ipv6Key).
func (e *WriterEdit) PushHistory6(merge *historyMerge[key6], fromHi, fromLo, toHi, toLo uint64, lastSeen uint32, check func() error) error {
	return merge.push(e.store, key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, lastSeen, check)
}

// FinishHistory4 ends the IPv4 projection merge and assembles the
// projection report (Rust WriterEdit::finish_history over Ipv4Key).
func (e *WriterEdit) FinishHistory4(merge *historyMerge[key4], sourceRangeCount uint64, sourceAddresses format.Cardinality129, check func() error) (*HistoryProjectionReport, error) {
	return merge.finish(e.store, check, sourceRangeCount, sourceAddresses)
}

// FinishHistory6 ends the IPv6 projection merge and assembles the
// projection report (Rust WriterEdit::finish_history over Ipv6Key).
func (e *WriterEdit) FinishHistory6(merge *historyMerge[key6], sourceRangeCount uint64, sourceAddresses format.Cardinality129, check func() error) (*HistoryProjectionReport, error) {
	return merge.finish(e.store, check, sourceRangeCount, sourceAddresses)
}

// FinalizeMembershipWorkflow applies the buffered membership refcount
// deltas without sealing the workflow (Rust
// WriterEdit::finalize_membership_workflow).
func (e *WriterEdit) FinalizeMembershipWorkflow(check func() error) error {
	return e.store.finalizeMembershipWorkflow(check)
}

// FinishMembershipWorkflow finalizes the membership deltas and finishes
// the workflow (Rust WriterEdit::finish_membership_workflow).
func (e *WriterEdit) FinishMembershipWorkflow(check func() error) error {
	return e.store.finishMembershipWorkflow(check)
}

// FinishDirectWorkflow retires the base range tree when the merge did
// not retire it already, then finishes the workflow (Rust
// WriterEdit::finish_direct_workflow).
func (e *WriterEdit) FinishDirectWorkflow(check func() error) error {
	return e.store.finishDirectWorkflow(e.base, check)
}

// InternNetworkEnrichmentV1 interns one typed enrichment value with the
// optional threat membership (Rust
// WriterEdit::intern_network_enrichment_v1).
func (e *WriterEdit) InternNetworkEnrichmentV1(value format.NetworkEnrichmentV1, membership MembershipHandle) (StructureHandle, error) {
	return e.store.internNetworkEnrichmentV1(value, membership)
}

// AssignStructureInputV4 assigns one structured IPv4 range through the
// transaction assignment input (Rust
// WriterEdit::assign_structure_input_v4).
func (e *WriterEdit) AssignStructureInputV4(from, to uint32, structure StructureHandle, input *AssignmentInput) (bool, error) {
	return e.store.assignStructureInputV4(from, to, structure, &input.v4)
}

// AssignStructureInputV6 assigns one structured IPv6 range through the
// transaction assignment input (Rust
// WriterEdit::assign_structure_input_v6).
func (e *WriterEdit) AssignStructureInputV6(fromHi, fromLo, toHi, toLo uint64, structure StructureHandle, input *AssignmentInput) (bool, error) {
	return e.store.assignStructureInputV6(fromHi, fromLo, toHi, toLo, structure, &input.v6)
}

// DeleteCurrentStructuredFeed deletes one feed and removes it from every
// stored structure payload (Rust
// WriterEdit::delete_current_structured_feed).
func (e *WriterEdit) DeleteCurrentStructuredFeed(feed FeedEntry, check func() error) error {
	return e.store.deleteCurrentStructuredFeed(feed, check)
}

// finishDirectWorkflow completes a direct (range) workflow (Rust
// draft_store/workflow.rs finish_direct_workflow): the committed base
// range tree is retired unless the merge already retired it, then the
// draft seals its input state.
func (s *DraftStore) finishDirectWorkflow(base format.Meta, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	if !s.draft.baseRangeTreeRetired {
		if base.AddressFamily == format.AddressFamilyIPv4 {
			if err := tree.RetireTree(rangeCodec4{}, s, base.RangeRoot, check); err != nil {
				return err
			}
		} else {
			if err := tree.RetireTree(rangeCodec6{}, s, base.RangeRoot, check); err != nil {
				return err
			}
		}
	}
	s.draft.finishWorkflow()
	return nil
}

// finalizeMembershipWorkflow drains and applies the buffered membership
// refcount deltas, checkpointing between records (Rust
// draft_store/workflow.rs finalize_membership_workflow).
func (s *DraftStore) finalizeMembershipWorkflow(check func() error) error {
	if err := check(); err != nil {
		return err
	}
	if err := s.finishMembershipDeltasWithCheckpoint(check); err != nil {
		return err
	}
	return check()
}

// finishMembershipWorkflow finalizes the membership deltas and finishes
// the workflow (Rust draft_store/workflow.rs finish_membership_workflow).
func (s *DraftStore) finishMembershipWorkflow(check func() error) error {
	if err := s.finalizeMembershipWorkflow(check); err != nil {
		return err
	}
	s.draft.finishWorkflow()
	return nil
}

// LookupBaseFeed resolves one exact feed name in the committed base
// catalog (Rust WriterCore::lookup_base_feed): read-only, no draft
// required, page-bounded to the committed generation.
func (c *Core) LookupBaseFeed(name string) (FeedEntry, bool, error) {
	if err := c.requireHealthy(); err != nil {
		return FeedEntry{}, false, err
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	return lookupCatalogFeed(store, c.base.Meta, name)
}

// LookupCurrentFeed resolves one exact feed name in the current catalog
// generation (Rust WriterCore::lookup_current_feed): the draft meta when
// a draft is open, otherwise the committed base.
func (c *Core) LookupCurrentFeed(name string) (FeedEntry, bool, error) {
	if err := c.requireHealthy(); err != nil {
		return FeedEntry{}, false, err
	}
	meta := c.base.Meta
	if c.draft != nil {
		meta = c.draft.meta
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	return lookupCatalogFeed(store, meta, name)
}

// CurrentFeedCursor opens the forward catalog cursor over the current
// generation (Rust WriterCore::current_feed_cursor: the writer has no
// reader table until Milestone 4, so the cursor carries no owner
// identity).
func (c *Core) CurrentFeedCursor() (*FeedCursor, error) {
	if err := c.requireHealthy(); err != nil {
		return nil, err
	}
	meta := c.base.Meta
	if c.draft != nil {
		meta = c.draft.meta
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	return NewFeedCursor(store, meta)
}

// CompareMaps sweeps the committed base and the current draft range maps
// and returns the exact six-way address classification (Rust
// WriterCore::compare_maps over workflow/compare.rs maps).
func (c *Core) CompareMaps(check func() error) (Comparison, error) {
	if err := c.requireHealthy(); err != nil {
		return Comparison{}, err
	}
	if c.draft == nil {
		return Comparison{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if c.base.Meta.AddressFamily == format.AddressFamilyIPv4 {
		return compareMaps4(store, c.base.Meta, check)
	}
	return compareMaps6(store, c.base.Meta, check)
}

// LookupFeed resolves one exact feed name in the draft catalog (Rust
// WriterEdit::lookup_feed).
func (e *WriterEdit) LookupFeed(name string) (FeedEntry, bool, error) {
	return e.store.lookupFeed(name)
}

// EnsureFeed returns the existing entry or creates the feed (Rust
// WriterEdit::ensure_feed; the created flag distinguishes the two).
func (e *WriterEdit) EnsureFeed(name string) (FeedEntry, bool, error) {
	return e.store.ensureFeed(name)
}

// InsertFeed allocates a feed index and inserts the dual catalog
// records (Rust WriterEdit::insert_feed).
func (e *WriterEdit) InsertFeed(name string) (FeedEntry, error) {
	return e.store.insertFeed(name)
}

// RenameCurrentFeed renames one current feed entry, refusing a name
// that already exists (Rust WriterEdit::rename_current_feed).
func (e *WriterEdit) RenameCurrentFeed(entry FeedEntry, newName string) (FeedEntry, error) {
	return e.store.renameCurrentFeed(entry, newName)
}

// RenameCurrentFeedKnownAvailable renames one current feed entry when
// the new name was already proven available (Rust
// WriterEdit::rename_current_feed_known_available).
func (e *WriterEdit) RenameCurrentFeedKnownAvailable(entry FeedEntry, newName string) (FeedEntry, error) {
	return e.store.renameCurrentFeedKnownAvailable(entry, newName)
}

// AddFeedToMembership interns the member bitmap of one feed over a base
// bitmap (Rust WriterEdit::add_feed_to_membership).
func (e *WriterEdit) AddFeedToMembership(base MembershipHandle, feed FeedEntry) (MembershipHandle, error) {
	return e.store.addFeedToMembership(base, feed)
}

// ApplyMembershipV4 applies one membership operation over an inclusive
// IPv4 interval (Rust WriterEdit::apply_membership_v4).
func (e *WriterEdit) ApplyMembershipV4(from, to uint32, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	return e.store.applyMembershipV4(from, to, member, operation, check)
}

// ApplyMembershipV6 applies one membership operation over an inclusive
// IPv6 interval (Rust WriterEdit::apply_membership_v6).
func (e *WriterEdit) ApplyMembershipV6(fromHi, fromLo, toHi, toLo uint64, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	return e.store.applyMembershipV6(fromHi, fromLo, toHi, toLo, member, operation, check)
}

// DeleteCurrentFeedMembership deletes one feed and clears its bit from
// every stored membership (Rust
// WriterEdit::delete_current_feed_membership).
func (e *WriterEdit) DeleteCurrentFeedMembership(feed FeedEntry, check func() error) error {
	return e.store.deleteCurrentFeedMembership(feed, check)
}

// BeginEmptyMapFeed starts the empty-map workflow of a value-free base
// (Rust WriterEdit::begin_empty_map_feed).
func (e *WriterEdit) BeginEmptyMapFeed() error {
	return e.store.beginEmptyMapFeed()
}

// AddEmptyMapFeedRange4 pushes one constant member-valued IPv4 range
// into the private draft tree (Rust
// WriterEdit::add_empty_map_feed_range over Ipv4Key).
func (e *WriterEdit) AddEmptyMapFeedRange4(from, to uint32, member MembershipHandle, input *UnionInput) error {
	return e.store.addEmptyMapFeedRange4(key4(from), key4(to), member, &input.v4)
}

// AddEmptyMapFeedRange6 is the IPv6 form of AddEmptyMapFeedRange4.
func (e *WriterEdit) AddEmptyMapFeedRange6(fromHi, fromLo, toHi, toLo uint64, member MembershipHandle, input *UnionInput) error {
	return e.store.addEmptyMapFeedRange6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, member, &input.v6)
}

// FinishEmptyMapFeedRanges4 seals the constant IPv4 ranges and accounts
// the member refcount (Rust
// WriterEdit::finish_empty_map_feed_ranges over Ipv4Key).
func (e *WriterEdit) FinishEmptyMapFeedRanges4(member MembershipHandle, input *UnionInput) (format.Cardinality129, bool, error) {
	return e.store.finishEmptyMapFeedRanges4(member, &input.v4)
}

// FinishEmptyMapFeedRanges6 is the IPv6 form of
// FinishEmptyMapFeedRanges4.
func (e *WriterEdit) FinishEmptyMapFeedRanges6(member MembershipHandle, input *UnionInput) (format.Cardinality129, bool, error) {
	return e.store.finishEmptyMapFeedRanges6(member, &input.v6)
}

// AddFeedCoverage4 pushes one value-1 IPv4 range into the private
// workflow coverage tree (Rust WriterEdit::add_feed_coverage over
// Ipv4Key).
func (e *WriterEdit) AddFeedCoverage4(from, to uint32, input *UnionInput) error {
	return e.store.addFeedCoverage4(key4(from), key4(to), &input.v4)
}

// AddFeedCoverage6 is the IPv6 form of AddFeedCoverage4.
func (e *WriterEdit) AddFeedCoverage6(fromHi, fromLo, toHi, toLo uint64, input *UnionInput) error {
	return e.store.addFeedCoverage6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, &input.v6)
}

// FinishFeedCoverage4 seals the pending IPv4 coverage input (Rust
// WriterEdit::finish_feed_coverage over Ipv4Key).
func (e *WriterEdit) FinishFeedCoverage4(input *UnionInput) error {
	return e.store.finishFeedCoverage4(&input.v4)
}

// FinishFeedCoverage6 is the IPv6 form of FinishFeedCoverage4.
func (e *WriterEdit) FinishFeedCoverage6(input *UnionInput) error {
	return e.store.finishFeedCoverage6(&input.v6)
}

// MergeFeed merges the workflow coverage tree over the committed base
// generation (Rust WriterEdit::merge_feed).
func (e *WriterEdit) MergeFeed(member MembershipHandle, create bool, check func() error) (FeedMerge, error) {
	return e.store.mergeFeed(e.base, member, create, check)
}

// AssignInputV4 assigns one IPv4 range through the direct replacement
// assignment input (Rust WriterEdit::assign_input_v4).
func (e *WriterEdit) AssignInputV4(from, to uint32, value uint32, input *AssignmentInput) (bool, error) {
	return e.store.assignInput4(key4(from), key4(to), value, &input.v4)
}

// AssignInputV6 assigns one IPv6 range through the direct replacement
// assignment input (Rust WriterEdit::assign_input_v6).
func (e *WriterEdit) AssignInputV6(fromHi, fromLo, toHi, toLo uint64, value uint32, input *AssignmentInput) (bool, error) {
	return e.store.assignInput6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, value, &input.v6)
}

// AddPrivateConstantRangeV4 pushes one untracked constant IPv4 range
// into the draft range tree (Rust WriterEdit::add_private_constant_range
// for IPv4; timestamp refresh inputs).
func (e *WriterEdit) AddPrivateConstantRangeV4(from, to uint32, value uint32, input *UnionInput) error {
	return e.store.addPrivateConstantRange4(key4(from), key4(to), value, &input.v4)
}

// AddPrivateConstantRangeV6 pushes one untracked constant IPv6 range
// into the draft range tree (Rust WriterEdit::add_private_constant_range
// for IPv6; timestamp refresh inputs).
func (e *WriterEdit) AddPrivateConstantRangeV6(fromHi, fromLo, toHi, toLo uint64, value uint32, input *UnionInput) error {
	return e.store.addPrivateConstantRange6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, value, &input.v6)
}

// FinishPrivateConstantRanges seals one untracked constant-range input
// (Rust WriterEdit::finish_private_constant_ranges; the facade routes
// to the family side selected at input construction).
func (e *WriterEdit) FinishPrivateConstantRanges(input *UnionInput) error {
	if input.family == format.AddressFamilyIPv4 {
		return e.store.finishPrivateConstantRanges4(&input.v4)
	}
	return e.store.finishPrivateConstantRanges6(&input.v6)
}

// MergeFirstSeen merges the timestamp coverage tree over the committed
// base with the first-seen policy (Rust
// WriterEdit::merge_first_seen).
func (e *WriterEdit) MergeFirstSeen(refreshValue uint32, check func() error) (TimestampMerge, error) {
	return e.store.mergeFirstSeen(e.base, refreshValue, check)
}

// MergeLastSeen merges the timestamp coverage tree over the committed
// base with the last-seen policy (Rust WriterEdit::merge_last_seen).
func (e *WriterEdit) MergeLastSeen(refreshValue, cutoff uint32, check func() error) (TimestampMerge, error) {
	return e.store.mergeLastSeen(e.base, refreshValue, cutoff, check)
}

// MergeFirstSeenWithRemovals4 merges with the first-seen policy and
// streams every removed IPv4 interval through the sink in bounded
// batches (Rust WriterEdit::merge_first_seen_v4_with_removals).
func (e *WriterEdit) MergeFirstSeenWithRemovals4(refreshValue uint32, sink FirstSeenRemoval4Sink, check func() error) (TimestampMerge, error) {
	return e.store.mergeFirstSeenWithRemovals4(e.base, refreshValue, sink, check)
}

// MergeFirstSeenWithRemovals6 merges with the first-seen policy and
// streams every removed IPv6 interval through the sink in bounded
// batches (Rust WriterEdit::merge_first_seen_v6_with_removals).
func (e *WriterEdit) MergeFirstSeenWithRemovals6(refreshValue uint32, sink FirstSeenRemoval6Sink, check func() error) (TimestampMerge, error) {
	return e.store.mergeFirstSeenWithRemovals6(e.base, refreshValue, sink, check)
}

// AssignV4 assigns one inclusive IPv4 interval on the open draft
// through this edit binding (Rust WriterEdit::assign_v4). The general
// timestamp-input path uses the bound edit so the hot path never binds
// a fresh DraftStore per record.
func (e *WriterEdit) AssignV4(from, to uint32, value uint32) (bool, error) {
	return e.store.AssignV4(from, to, value)
}

// AssignV6 assigns one inclusive IPv6 interval on the open draft
// through this edit binding (Rust WriterEdit::assign_v6).
func (e *WriterEdit) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	return e.store.AssignV6(fromHi, fromLo, toHi, toLo, value)
}
