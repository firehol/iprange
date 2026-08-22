// Draft is one unpublished COW transaction over the opened committed
// generation (Rust draft_store.rs Draft). It owns the evolving meta and the
// private-page bookkeeping: the private-page stack, the dirty-page chain
// head and charge, the allocator-retired backlog, and the growth charge for
// one transaction, plus the range-tree privacy flag. The membership and
// structure delta state machines of the Rust Draft arrive with their edit
// cores.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// Draft is one unpublished COW transaction over the committed generation.
type Draft struct {
	base format.Meta
	meta format.Meta

	// privateHead is the LIFO of claimed pages discarded by the edit
	// core for reuse (Rust private_head).
	privateHead uint32
	// dirtyHead is the head of the dirty-page chain: every privately
	// claimed page is linked through its checksum slot until prepare
	// seals the data pages (Rust dirty_head).
	dirtyHead uint32
	// allocatorRetired is the backlog of bitmap COW victims that must be
	// retired after the current bitmap edit (Rust allocator_retired).
	allocatorRetired tree.RetiredPages
	// privatePages is the number of dirty-chain entries (the private
	// page budget charge; Rust private_pages).
	privatePages uint64
	// growthPages is the number of file-tail pages claimed this
	// transaction (Rust growth_pages).
	growthPages uint64
	// changed reports whether the draft mutated persistent content
	// (Rust Draft::changed; set by the edit workflows and prepare).
	changed bool
	// metadataStaged reports one metadata stage was already applied to
	// this draft (Rust Draft::metadata_staged; set by the metadata edit
	// core and enforced by requireMetadataAvailable).
	metadataStaged bool
	// rangeTreePrivate reports the range tree is draft-private (Rust
	// Draft::range_tree_private; true when the committed base has no
	// range tree). Public range edits on a private tree take the gap
	// path; edits over a committed tree COW it.
	rangeTreePrivate bool
	// baseRangeTreeRetired reports the committed base range tree was
	// already retired by a merge (Rust base_range_tree_retired): the
	// workflow finish must not retire it a second time.
	baseRangeTreeRetired bool
	// membershipDeltaRoot is the private refcount delta tree of the
	// open workflow (Rust membership_delta_root; flushed and drained by
	// finishMembershipDeltasWithCheckpoint).
	membershipDeltaRoot uint32
	// membershipDeltaPending is the two-slot delta buffer in front of
	// the delta tree (Rust membership_delta_pending).
	membershipDeltaPending deltaPending
	// workflowRangeRoot is the private coverage tree of an exact feed
	// workflow that has not started merging yet (Rust
	// workflow_range_root): each add_feed_coverage call builds it
	// untracked, and the merge consumes it.
	workflowRangeRoot uint32
	// workflowRangeCount is the record count of workflowRangeRoot (Rust
	// workflow_range_count).
	workflowRangeCount uint64
	// workflow is the exact-workflow state of the draft (Rust
	// WorkflowState: None, Input, Prepared).
	workflow workflowState
	// operationAbandonedFlag reports the prepared operation was
	// abandoned by its Drop-style cleanup (Rust operation_abandoned;
	// Go has no Drop hook, so the public workflows set it when the
	// prepared handle is closed without commit or abort).
	operationAbandonedFlag bool
}

// workflowState is one exact workflow state (Rust WorkflowState).
type workflowState uint8

const (
	workflowNone     workflowState = iota
	workflowInput                  // the workflow accepts input records
	workflowPrepared               // the draft is prepared for publication
)

// beginRangeWorkflow starts one exact range workflow: the range tree is
// detached to a private empty tree (Rust Draft::begin_range_workflow).
func (d *Draft) beginRangeWorkflow() error {
	if err := d.beginWorkflow(); err != nil {
		return err
	}
	d.meta.RangeRoot = 0
	d.meta.RangeRecordCount = 0
	d.rangeTreePrivate = true
	return nil
}

// beginMembershipWorkflow starts one exact membership workflow (Rust
// Draft::begin_membership_workflow).
func (d *Draft) beginMembershipWorkflow() error {
	return d.beginWorkflow()
}

func (d *Draft) beginWorkflow() error {
	if d.workflow != workflowNone {
		return &format.Error{Code: format.CodeWrongState, Detail: "another exact workflow is active"}
	}
	d.workflow = workflowInput
	return nil
}

// workflowInputOpen reports the draft accepts workflow input records
// (Rust Draft::workflow_input_open).
func (d *Draft) workflowInputOpen() bool { return d.workflow == workflowInput }

// workflowActive reports any exact workflow state (Rust
// Draft::workflow_active).
func (d *Draft) workflowActive() bool { return d.workflow != workflowNone }

// operationAbandoned reports the prepared operation was abandoned (Rust
// Draft::operation_abandoned).
func (d *Draft) operationAbandoned() bool { return d.operationAbandonedFlag }

// abandonOperation brands the draft abandoned (Rust
// Draft::abandon_operation).
func (d *Draft) abandonOperation() { d.operationAbandonedFlag = true }

// finishWorkflow seals the input state and marks the draft changed (Rust
// Draft::finish_workflow).
func (d *Draft) finishWorkflow() {
	d.workflow = workflowPrepared
	d.changed = true
}

// NewDraft starts one draft over base with the next transaction ID and the
// caller's commit nonce (Rust Draft::new). The transaction ID is checked
// for exhaustion exactly like the Rust checked_add.
func NewDraft(base format.Meta, nonce [16]byte) (*Draft, error) {
	meta := base
	if meta.TxnID == ^uint64(0) {
		return nil, overflow("transaction ID")
	}
	meta.TxnID++
	meta.CommitNonce = nonce
	return &Draft{
		base: base, meta: meta,
		rangeTreePrivate:       base.RangeRoot == 0,
		membershipDeltaPending: newDeltaPending(),
	}, nil
}

// Base returns the committed base generation the draft edits.
func (d *Draft) Base() format.Meta { return d.base }

// Meta returns the evolving draft meta; the caller publishes it when the
// draft commits.
func (d *Draft) Meta() format.Meta { return d.meta }

// Changed reports whether the draft mutated persistent content.
func (d *Draft) Changed() bool { return d.changed }

// MetadataStaged reports one metadata stage was already applied (Rust
// Draft::metadata_staged).
func (d *Draft) MetadataStaged() bool { return d.metadataStaged }
