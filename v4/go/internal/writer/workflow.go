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
func (e *WriterEdit) PrepareHistoryFrom(windows []historyWindow, check func() error) (*historyPlan, error) {
	return prepareHistoryPlan(e.store, windows, check)
}

// BeginHistory starts the projection merge over the committed
// destination (Rust WriterEdit::begin_history).
func (e *WriterEdit) BeginHistory(plan *historyPlan, check func() error) (*historyMerge, error) {
	return plan.begin(e.store, e.base, check)
}

// PushHistory feeds one source range into the projection merge (Rust
// WriterEdit::push_history).
func (e *WriterEdit) PushHistory(merge *historyMerge, from, to tree.Key, lastSeen uint32, check func() error) error {
	return merge.push(e.store, from, to, lastSeen, check)
}

// FinishHistory ends the projection merge and assembles the projection
// report (Rust WriterEdit::finish_history).
func (e *WriterEdit) FinishHistory(merge *historyMerge, sourceRangeCount uint64, sourceAddresses format.Cardinality129, check func() error) (*historyProjectionReport, error) {
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

// finishDirectWorkflow completes a direct (range) workflow (Rust
// draft_store/workflow.rs finish_direct_workflow): the committed base
// range tree is retired unless the merge already retired it, then the
// draft seals its input state.
func (s *DraftStore) finishDirectWorkflow(base format.Meta, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	if !s.draft.baseRangeTreeRetired {
		var codec rangeFamily
		if base.AddressFamily == format.AddressFamilyIPv4 {
			codec = rangeCodec4{}
		} else {
			codec = rangeCodec6{}
		}
		if err := tree.RetireTree(codec, s, base.RangeRoot, check); err != nil {
			return err
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
