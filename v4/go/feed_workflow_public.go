// Public named-feed create/replace/rename/delete workflows (Rust
// live_writer/{feed_workflow,feed_lifecycle}.rs parity): the exact
// one-feed workflows over a clean membership writer. Each workflow
// consumes one ordered input, finishes into a changed or no-change
// terminal, and the changed terminal is one prepared handle
// (DirectTransaction precedent) that owns the draft until Commit,
// Abort, or Writer.Close. The PreparedFeedChange handles own the
// rename/delete drafts the same way.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// CreateFeed is one in-progress feed creation (Rust
// LiveWriter::begin_create_feed): the named feed is prepared on the
// draft, input ranges are streamed through AddRangesV4/AddRangesV6, and
// FinishInput produces the terminal handle. The handle borrows the
// writer; the pending draft survives the handle (Rust owns the draft on
// the writer), so a dropped input is discarded with Writer.Abort.
type CreateFeed struct {
	state *exactFeedWorkflow
}

// ReplaceFeed is one in-progress feed replacement (Rust
// LiveWriter::begin_replace_feed): the existing named feed is selected
// on the draft, input ranges are streamed, and FinishInput produces the
// terminal handle.
type ReplaceFeed struct {
	state *exactFeedWorkflow
}

// exactFeedWorkflow is the shared input state of one exact feed
// workflow (Rust ExactFeedState): the prepared membership handle, the
// coverage input, and the creation flags. The input record counter
// mirrors the Rust drain path exactly.
type exactFeedWorkflow struct {
	w              mutationHost
	cancellation   *CancellationToken
	workflow       WorkflowKind
	create         bool
	emptyMapCreate bool
	member         writer.MembershipHandle
	coverage       writer.UnionInput
	// edit is the draft-lifetime edit binding (Rust writer_core edit
	// core): the feed slice path streams every batch through this one
	// binding, so the hot path allocates nothing per batch (a fresh
	// Mutate binding per call would allocate the store, the edit, and
	// the operation closures).
	edit         *writer.WriterEdit
	inputRecords uint64
}

// BeginCreateFeed begins creation of one absent named feed on a clean
// membership writer (Rust LiveWriter::begin_create_feed): the writer
// must be healthy, hold a membership database, and have no pending
// transaction; the name must not exist (ErrorNameExists).
func (w *Writer) BeginCreateFeed(name FeedName, cancellation *CancellationToken) (*CreateFeed, error) {
	state, err := beginExactFeed(w, name, true, cancellation)
	if err != nil {
		return nil, err
	}
	return &CreateFeed{state: state}, nil
}

// BeginCreateFeed begins creation of one absent named feed on a clean
// live writer (Rust LiveWriter::begin_create_feed): the live writer must
// be open and healthy, hold a membership database, and have no pending
// transaction; the name must not exist (ErrorNameExists).
func (w *LiveWriter) BeginCreateFeed(name FeedName, cancellation *CancellationToken) (*CreateFeed, error) {
	state, err := beginExactFeed(w, name, true, cancellation)
	if err != nil {
		return nil, err
	}
	return &CreateFeed{state: state}, nil
}

// BeginReplaceFeed begins complete replacement of one existing named feed
// (Rust LiveWriter::begin_replace_feed): the writer preconditions are
// the same and the name must exist (ErrorNameNotFound).
func (w *Writer) BeginReplaceFeed(name FeedName, cancellation *CancellationToken) (*ReplaceFeed, error) {
	state, err := beginExactFeed(w, name, false, cancellation)
	if err != nil {
		return nil, err
	}
	return &ReplaceFeed{state: state}, nil
}

// BeginReplaceFeed begins complete replacement of one existing named
// feed on a clean live writer (Rust LiveWriter::begin_replace_feed): the
// live preconditions are the same and the name must exist
// (ErrorNameNotFound).
func (w *LiveWriter) BeginReplaceFeed(name FeedName, cancellation *CancellationToken) (*ReplaceFeed, error) {
	state, err := beginExactFeed(w, name, false, cancellation)
	if err != nil {
		return nil, err
	}
	return &ReplaceFeed{state: state}, nil
}

// requireFeedWorkflowReady mirrors Rust require_feed_workflow_ready:
// the writer must be healthy and hold a membership database with no
// pending transaction.
func requireFeedWorkflowReady(h mutationHost) error {
	if err := h.healthy(); err != nil {
		return err
	}
	if h.coreOf().BaseInfo().ValueKind != format.ValueKindMembership {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "named-feed workflow requires a membership database"}
	}
	if h.coreOf().HasDraft() {
		return &format.Error{Code: format.CodeWrongState, Detail: "a writer transaction is already pending"}
	}
	return nil
}

// beginExactFeed is the shared Rust begin_exact_feed_state: the workflow
// preconditions, the base feed lookup, the create/replace precondition,
// the membership workflow draft, and the prepared member on it.
func beginExactFeed(h mutationHost, name FeedName, create bool, cancellation *CancellationToken) (*exactFeedWorkflow, error) {
	if err := h.healthy(); err != nil {
		return nil, err
	}
	if !format.FeedNameValidString(string(name)) {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := requireFeedWorkflowReady(h); err != nil {
		return nil, err
	}
	existing, found, err := h.coreOf().LookupBaseFeed(string(name))
	if err != nil {
		return nil, err
	}
	if err := requireFeedPrecondition(found, create); err != nil {
		return nil, err
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	emptyMapCreate := create && h.coreOf().BaseInfo().RangeRecordCount == 0
	if err := h.coreOf().BeginMembershipWorkflow(); err != nil {
		return nil, err
	}
	var member writer.MembershipHandle
	err = h.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		// setup_feed (Rust feed_workflow.rs): check, select the feed,
		// and intern the single-member bitmap on the empty handle.
		if err := cancellation.check(); err != nil {
			return err
		}
		var feed writer.FeedEntry
		if create {
			feed, err = edit.InsertFeed(string(name))
		} else {
			if !found {
				return corruptError("replacement feed disappeared")
			}
			feed = existing
		}
		if err != nil {
			return err
		}
		member, err = edit.AddFeedToMembership(writer.EmptyMembershipHandle(), feed)
		if err != nil {
			return err
		}
		if err := cancellation.check(); err != nil {
			return err
		}
		if emptyMapCreate {
			return edit.BeginEmptyMapFeed()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	edit, err := h.coreOf().BindEdit()
	if err != nil {
		return nil, err
	}
	return &exactFeedWorkflow{
		w:              h,
		cancellation:   cancellation,
		workflow:       workflowKindFor(create),
		create:         create,
		emptyMapCreate: emptyMapCreate,
		member:         member,
		coverage:       writer.NewUnionInput(h.coreOf().BaseInfo().AddressFamily, format.ValueKindMembership, h.coreOf().Budget().MaxHeapBytes),
		edit:           edit,
	}, nil
}

func workflowKindFor(create bool) WorkflowKind {
	if create {
		return WorkflowCreateFeed
	}
	return WorkflowReplaceFeed
}

// requireFeedPrecondition is the Rust require_feed_precondition: a
// creation refuses an existing name, a replacement refuses a missing
// one.
func requireFeedPrecondition(found, create bool) error {
	switch {
	case found && create:
		return &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	case !found && !create:
		return &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	return nil
}

// AddRangesV4 streams one batch of inclusive IPv4 input ranges into this
// creation (Rust CreateFeed::add_ranges_v4_slice): every range must be
// ordered and match the database family. Both error classes abort the
// workflow: the caller observes ErrorTransactionAborted wrapping
// ErrorInvalidArgument (reversed range) or ErrorWrongAddressFamily
// (family mismatch), and the creation must not be reused.
func (f *CreateFeed) AddRangesV4(ranges []AddressRange4) error {
	return f.state.addRanges4(ranges)
}

// AddRangesV6 streams one batch of inclusive IPv6 input ranges into this
// creation (Rust CreateFeed::add_ranges_v6_slice). Error semantics are
// identical to AddRangesV4: reversed ranges and family mismatches abort
// the workflow, reported as ErrorTransactionAborted wrapping the cause.
func (f *CreateFeed) AddRangesV6(ranges []AddressRange6) error {
	return f.state.addRanges6(ranges)
}

// FinishInput finishes the input and returns the terminal workflow
// handle (Rust CreateFeed::finish_input).
func (f *CreateFeed) FinishInput() (*FinishedWorkflow, error) {
	return f.state.finishInput()
}

// AddRangesV4 streams one batch of inclusive IPv4 input ranges into this
// replacement (Rust ReplaceFeed::add_ranges_v4_slice). Error semantics
// are identical to CreateFeed.AddRangesV4: reversed ranges and family
// mismatches abort the workflow, reported as ErrorTransactionAborted
// wrapping the cause.
func (f *ReplaceFeed) AddRangesV4(ranges []AddressRange4) error {
	return f.state.addRanges4(ranges)
}

// AddRangesV6 streams one batch of inclusive IPv6 input ranges into this
// replacement (Rust ReplaceFeed::add_ranges_v6_slice). Error semantics
// are identical to CreateFeed.AddRangesV4: reversed ranges and family
// mismatches abort the workflow, reported as ErrorTransactionAborted
// wrapping the cause.
func (f *ReplaceFeed) AddRangesV6(ranges []AddressRange6) error {
	return f.state.addRanges6(ranges)
}

// FinishInput finishes the input and returns the terminal workflow
// handle (Rust ReplaceFeed::finish_input).
func (f *ReplaceFeed) FinishInput() (*FinishedWorkflow, error) {
	return f.state.finishInput()
}

// addRanges4 streams one IPv4 batch through the draft-lifetime edit
// binding (Rust CreateFeed::add_ranges_v4_slice over the writer_core
// edit core): one source pass, one input-source pass, one cancellation
// checkpoint before the batch, between 4096-record chunks, and after
// the final batch (Rust drain_source loop-top check before
// end-of-stream), and one consumed-range charge plus record count per
// record. The body is a plain loop, not a closure, so the measured
// slice path allocates nothing (Rust allocate_nothing_per_record
// parity).
func (in *exactFeedWorkflow) addRanges4(ranges []AddressRange4) error {
	if err := in.requireInputFamily(format.AddressFamilyIPv4); err != nil {
		return err
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	edit := in.edit
	// Rust drain_source runs every cancellation checkpoint inside
	// writer.mutate, so each failure aborts the workflow and wraps the
	// cause in TransactionAborted.
	if err := in.cancellation.check(); err != nil {
		return in.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := in.cancellation.check(); err != nil {
				return in.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := in.nextInputRecord()
			if err != nil {
				return in.w.abortAfter(err)
			}
			if r.From > r.To {
				return in.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			if in.emptyMapCreate {
				if err := edit.AddEmptyMapFeedRange(tree.Key{Hi: uint64(r.From)}, tree.Key{Hi: uint64(r.To)}, in.member, &in.coverage); err != nil {
					return in.w.abortAfter(err)
				}
			} else if err := edit.AddFeedCoverage(tree.Key{Hi: uint64(r.From)}, tree.Key{Hi: uint64(r.To)}, &in.coverage); err != nil {
				return in.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			in.inputRecords = next
		}
	}
	// Rust drain_source runs its loop-top checkpoint once more after the
	// final batch before the source reports end-of-stream; an empty
	// source never reaches that iteration.
	if len(ranges) != 0 {
		if err := in.cancellation.check(); err != nil {
			return in.w.abortAfter(err)
		}
	}
	return nil
}

// addRanges6 streams one IPv6 batch through the draft-lifetime edit
// binding with the same per-batch accounting and trailing checkpoint
// as addRanges4.
func (in *exactFeedWorkflow) addRanges6(ranges []AddressRange6) error {
	if err := in.requireInputFamily(format.AddressFamilyIPv6); err != nil {
		return err
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	edit := in.edit
	// Rust drain_source runs every cancellation checkpoint inside
	// writer.mutate, so each failure aborts the workflow and wraps the
	// cause in TransactionAborted.
	if err := in.cancellation.check(); err != nil {
		return in.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := in.cancellation.check(); err != nil {
				return in.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := in.nextInputRecord()
			if err != nil {
				return in.w.abortAfter(err)
			}
			if r.FromHi > r.ToHi || (r.FromHi == r.ToHi && r.FromLo > r.ToLo) {
				return in.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			from := tree.Key{Hi: r.FromHi, Lo: r.FromLo}
			to := tree.Key{Hi: r.ToHi, Lo: r.ToLo}
			if in.emptyMapCreate {
				if err := edit.AddEmptyMapFeedRange(from, to, in.member, &in.coverage); err != nil {
					return in.w.abortAfter(err)
				}
			} else if err := edit.AddFeedCoverage(from, to, &in.coverage); err != nil {
				return in.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			in.inputRecords = next
		}
	}
	// Rust drain_source runs its loop-top checkpoint once more after the
	// final batch before the source reports end-of-stream; an empty
	// source never reaches that iteration.
	if len(ranges) != 0 {
		if err := in.cancellation.check(); err != nil {
			return in.w.abortAfter(err)
		}
	}
	return nil
}

// nextInputRecord reserves the next input record counter before the
// record is applied and the caller charges RangeConsumed and stores the
// counter after the apply, mirroring Rust drain_source per-record order.
func (in *exactFeedWorkflow) nextInputRecord() (uint64, error) {
	next := in.inputRecords + 1
	if next == 0 {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "workflow input record count"}
	}
	return next, nil
}

// requireInputFamily mirrors Rust require_input_family: the input must
// be active and the database family must match; a mismatch aborts the
// workflow through the writer.
func (in *exactFeedWorkflow) requireInputFamily(family uint8) error {
	if err := in.w.healthy(); err != nil {
		return err
	}
	if !in.w.coreOf().WorkflowInputOpen() {
		return &format.Error{Code: format.CodeWrongState, Detail: "workflow input is not active"}
	}
	if in.w.coreOf().BaseInfo().AddressFamily != family {
		return in.w.abortAfter(&format.Error{Code: format.CodeWrongAddressFamily, Detail: "range family does not match the database"})
	}
	return nil
}

// FinishedWorkflow is the terminal of one exact feed workflow (Rust
// FinishedWorkflow collapsed to one Go handle, the DirectTransaction
// precedent). Every operation works on both variants except Commit,
// SetMetadataJSON, and ClearMetadataJSON, which require the changed
// variant; Abort on a no-change result reports ErrorNoPendingTransaction
// (Rust FinishedWorkflow::abort parity). The changed handle owns the
// draft until Commit, Abort, or Writer.Close.
type FinishedWorkflow struct {
	w            mutationHost
	report       WorkflowReport
	changed      bool
	spent        bool
	cancellation *CancellationToken
}

// IsChanged reports whether the workflow produced a logical change
// (Rust FinishedWorkflow::Changed vs NoChange).
func (f *FinishedWorkflow) IsChanged() bool {
	return f.changed
}

// Report returns the exact workflow report for both variants (Rust
// FinishedWorkflow::report).
func (f *FinishedWorkflow) Report() WorkflowReport {
	return f.report
}

// requireChangedActive gates the changed-variant-only operations: the
// handle must be the changed variant, not spent, and the writer must
// still own the draft.
func (f *FinishedWorkflow) requireChangedActive() error {
	if !f.changed {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow did not change"}
	}
	if f.spent {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow is no longer active"}
	}
	if err := f.w.healthy(); err != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow is no longer active"}
	}
	if f.w.coreOf().Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow is no longer active"}
	}
	return nil
}

// SetMetadataJSON stages one exact metadata replacement in the changed
// workflow (Rust PreparedWorkflow::set_metadata_json): the same 20 MiB
// cap and single-stage rule as a direct transaction, with the captured
// cancellation checked before and after the stage.
func (f *FinishedWorkflow) SetMetadataJSON(input []byte) (bool, error) {
	if err := f.requireChangedActive(); err != nil {
		return false, err
	}
	if err := f.cancellation.check(); err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	changed, err := f.w.coreOf().SetMetadata(input)
	if err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	if err := f.cancellation.check(); err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in the changed workflow
// (Rust PreparedWorkflow::clear_metadata_json); an already-absent
// database reports false.
func (f *FinishedWorkflow) ClearMetadataJSON() (bool, error) {
	if err := f.requireChangedActive(); err != nil {
		return false, err
	}
	if err := f.cancellation.check(); err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	changed, err := f.w.coreOf().ClearMetadata()
	if err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	if err := f.cancellation.check(); err != nil {
		f.spent = true
		return false, f.w.abortAfter(err)
	}
	return changed, nil
}

// Commit publishes the changed workflow (Rust PreparedWorkflow::commit):
// the DirectTransaction commit sequence with the captured cancellation
// checked at prepare and during publication (Rust commit_with). An
// unchanged draft is discarded and reports ErrorNoPendingTransaction.
func (f *FinishedWorkflow) Commit() (CommitResult, error) {
	if f.spent {
		// Rust commit_attempt reports NoPendingTransaction for a spent
		// workflow (the draft was discarded by Abort, an op failure, or
		// a cancellation).
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := f.requireChangedActive(); err != nil {
		return CommitResult{}, err
	}
	return f.w.commitPrepared(f.cancellation, func() { f.spent = true }, "feed workflow")
}

// Abort discards the changed workflow draft; the writer stays open and
// healthy (Rust PreparedWorkflow::abort). A no-change result is already
// clean and reports ErrorNoPendingTransaction (Rust
// FinishedWorkflow::abort parity).
func (f *FinishedWorkflow) Abort() error {
	if f.spent {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	f.spent = true
	if !f.changed {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := f.w.healthy(); err != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow is no longer active"}
	}
	if f.w.coreOf().Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed workflow is no longer active"}
	}
	return f.w.coreOf().DiscardUnpublished()
}

// RenameFeed renames one existing feed without changing its index or
// membership (Rust LiveWriter::rename_feed): the new name must not
// exist (ErrorNameExists), and the prepared change owns the draft until
// Commit, Abort, or Writer.Close.
func (w *Writer) RenameFeed(old, new FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	return beginRenameFeed(w, old, new, cancellation)
}

// RenameFeed renames one existing feed without changing its index or
// membership (Rust LiveWriter::rename_feed): the live writer must be
// open and healthy, the new name must not exist (ErrorNameExists), and
// the prepared change owns the draft until Commit, Abort, or Close.
func (w *LiveWriter) RenameFeed(old, new FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	return beginRenameFeed(w, old, new, cancellation)
}

// beginRenameFeed is the shared host-based rename (Rust
// feed_lifecycle.rs rename_feed): the workflow preconditions on the
// host, the existing-feed lookup, the new-name probe, the membership
// workflow draft, and the prepared change handle.
func beginRenameFeed(h mutationHost, old, new FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	if err := h.healthy(); err != nil {
		return nil, err
	}
	if !format.FeedNameValidString(string(old)) || !format.FeedNameValidString(string(new)) {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	// require_existing_feed (Rust feed_lifecycle.rs): workflow ready,
	// base lookup, missing name refused, cancellation checkpoint.
	if err := requireFeedWorkflowReady(h); err != nil {
		return nil, err
	}
	feed, found, err := h.coreOf().LookupBaseFeed(string(old))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	// require_existing_feed (Rust feed_lifecycle.rs): the cancellation
	// checkpoint runs before the new-name lookup, so a fired token
	// classifies as Cancelled even when the new name exists.
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if _, found, err := h.coreOf().LookupBaseFeed(string(new)); err != nil {
		return nil, err
	} else if found {
		return nil, &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if err := h.coreOf().BeginMembershipWorkflow(); err != nil {
		return nil, err
	}
	err = h.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		if err := cancellation.check(); err != nil {
			return err
		}
		if _, err := edit.RenameCurrentFeedKnownAvailable(feed, string(new)); err != nil {
			return err
		}
		return edit.FinishMembershipWorkflow(cancellation.check)
	})
	if err != nil {
		return nil, err
	}
	return &PreparedFeedChange{w: h, cancellation: cancellation}, nil
}

// DeleteFeed deletes one existing feed while preserving every other feed
// (Rust LiveWriter::delete_feed): the name must exist
// (ErrorNameNotFound), and the prepared change owns the draft until
// Commit, Abort, or Writer.Close.
func (w *Writer) DeleteFeed(name FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	return beginDeleteFeed(w, name, cancellation)
}

// DeleteFeed deletes one existing feed while preserving every other feed
// (Rust LiveWriter::delete_feed): the live writer must be open and
// healthy, the name must exist (ErrorNameNotFound), and the prepared
// change owns the draft until Commit, Abort, or Close.
func (w *LiveWriter) DeleteFeed(name FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	return beginDeleteFeed(w, name, cancellation)
}

// beginDeleteFeed is the shared host-based delete (Rust feed_lifecycle.rs
// delete_feed): the workflow preconditions on the host, the
// existing-feed lookup, the membership workflow draft, and the prepared
// change handle.
func beginDeleteFeed(h mutationHost, name FeedName, cancellation *CancellationToken) (*PreparedFeedChange, error) {
	if err := h.healthy(); err != nil {
		return nil, err
	}
	if !format.FeedNameValidString(string(name)) {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	}
	if err := requireFeedWorkflowReady(h); err != nil {
		return nil, err
	}
	feed, found, err := h.coreOf().LookupBaseFeed(string(name))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if err := h.coreOf().BeginMembershipWorkflow(); err != nil {
		return nil, err
	}
	err = h.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		if err := edit.DeleteCurrentFeedMembership(feed, cancellation.check); err != nil {
			return err
		}
		return edit.FinishMembershipWorkflow(cancellation.check)
	})
	if err != nil {
		return nil, err
	}
	return &PreparedFeedChange{w: h, cancellation: cancellation}, nil
}

// PreparedFeedChange is one prepared feed rename or delete awaiting
// optional metadata and publication (Rust PreparedFeedChange): the
// single handle owns the draft until Commit, Abort, or Writer.Close.
type PreparedFeedChange struct {
	w            mutationHost
	cancellation *CancellationToken
	spent        bool
}

// requireActive gates every prepared-change operation: the handle must
// not be spent and the writer must still own the draft.
func (p *PreparedFeedChange) requireActive() error {
	if p.spent {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed change is no longer active"}
	}
	if err := p.w.healthy(); err != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed change is no longer active"}
	}
	if p.w.coreOf().Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed change is no longer active"}
	}
	return nil
}

// SetMetadataJSON stages one exact metadata replacement in this prepared
// change (Rust PreparedFeedChange::set_metadata_json).
func (p *PreparedFeedChange) SetMetadataJSON(input []byte) (bool, error) {
	if err := p.requireActive(); err != nil {
		return false, err
	}
	if err := p.cancellation.check(); err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	changed, err := p.w.coreOf().SetMetadata(input)
	if err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	if err := p.cancellation.check(); err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in this prepared change
// (Rust PreparedFeedChange::clear_metadata_json).
func (p *PreparedFeedChange) ClearMetadataJSON() (bool, error) {
	if err := p.requireActive(); err != nil {
		return false, err
	}
	if err := p.cancellation.check(); err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	changed, err := p.w.coreOf().ClearMetadata()
	if err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	if err := p.cancellation.check(); err != nil {
		p.spent = true
		return false, p.w.abortAfter(err)
	}
	return changed, nil
}

// Commit publishes this prepared change (Rust
// PreparedFeedChange::commit).
func (p *PreparedFeedChange) Commit() (CommitResult, error) {
	if p.spent {
		// Rust commit_attempt reports NoPendingTransaction for a spent
		// prepared change (the draft was discarded by Abort, an op
		// failure, or a cancellation).
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	if err := p.requireActive(); err != nil {
		return CommitResult{}, err
	}
	return p.w.commitPrepared(p.cancellation, func() { p.spent = true }, "feed change")
}

// Abort discards this prepared change draft; the writer stays open and
// healthy (Rust PreparedFeedChange::abort).
func (p *PreparedFeedChange) Abort() error {
	if p.spent {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	p.spent = true
	if err := p.w.healthy(); err != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed change is no longer active"}
	}
	if p.w.coreOf().Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "feed change is no longer active"}
	}
	return p.w.coreOf().DiscardUnpublished()
}

// Abort discards any open draft without publishing it (Rust
// LiveWriter::abort): a clean writer reports ErrorNoPendingTransaction.
// The writer stays open and healthy.
func (w *Writer) Abort() error {
	if w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := w.core.Healthy(); err != nil {
		return err
	}
	if !w.core.HasDraft() {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return w.core.DiscardUnpublished()
}

// finishState mirrors Rust ExactFeedState::finish_state: the empty-map
// creation path when the base was empty, otherwise the coverage merge
// over the prepared member, both ending in the shared workflow
// completion that turns a no-change report into an already-clean
// terminal.
func (in *exactFeedWorkflow) finishState() (finishedWorkflow, error) {
	if err := in.requireActive(); err != nil {
		return finishedWorkflow{}, err
	}
	if in.emptyMapCreate {
		return in.finishEmptyMapCreate()
	}
	if err := in.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinishFeedCoverage(&in.coverage)
	}); err != nil {
		return finishedWorkflow{}, err
	}
	var merged writer.FeedMerge
	err := in.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		merged, err = edit.MergeFeed(in.member, in.create, in.cancellation.check)
		return err
	})
	if err != nil {
		return finishedWorkflow{}, err
	}
	if err := in.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinalizeMembershipWorkflow(in.cancellation.check)
	}); err != nil {
		return finishedWorkflow{}, err
	}
	report := in.prepareReport(merged)
	return completeFeedWorkflow(in.w, report, in.cancellation)
}

// requireActive mirrors Rust ExactFeedState::require_active (the shared
// require_input_active): healthy writer with an open workflow input.
func (in *exactFeedWorkflow) requireActive() error {
	if err := in.w.healthy(); err != nil {
		return err
	}
	if !in.w.coreOf().WorkflowInputOpen() {
		return &format.Error{Code: format.CodeWrongState, Detail: "workflow input is not active"}
	}
	return nil
}

// finishEmptyMapCreate mirrors Rust ExactFeedState::finish_empty_map_create:
// the value-free base creation seals the constant ranges, finalizes the
// membership deltas, and builds the comparison from the ordered-prefix
// address count or a full map comparison.
func (in *exactFeedWorkflow) finishEmptyMapCreate() (finishedWorkflow, error) {
	var addresses format.Cardinality129
	var hasOrdered bool
	err := in.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		var err error
		addresses, hasOrdered, err = edit.FinishEmptyMapFeedRanges(in.member, &in.coverage)
		return err
	})
	if err != nil {
		return finishedWorkflow{}, err
	}
	if err := in.w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinalizeMembershipWorkflow(in.cancellation.check)
	}); err != nil {
		return finishedWorkflow{}, err
	}
	before := in.w.coreOf().BaseInfo()
	after := in.w.coreOf().Draft().Meta()
	var comparison writer.Comparison
	if hasOrdered {
		comparison = writer.Comparison{After: addresses, Added: addresses}
	} else {
		comparison, err = in.w.coreOf().CompareMaps(in.cancellation.check)
		if err != nil {
			return finishedWorkflow{}, in.w.abortAfter(err)
		}
	}
	report := &WorkflowReport{
		Workflow:                     WorkflowCreateFeed,
		LogicalChange:                LogicalChanged,
		InputRecordCount:             in.inputRecords,
		InputNormalizedIntervalCount: after.RangeRecordCount,
		BeforeRangeRecordCount:       before.RangeRecordCount,
		AfterRangeRecordCount:        after.RangeRecordCount,
		InputAddresses:               comparison.After,
		BeforeAddresses:              comparison.Before,
		AfterAddresses:               comparison.After,
		UnchangedValueAddresses:      comparison.Unchanged,
		ChangedValueAddresses:        comparison.Changed,
		AddedAddresses:               comparison.Added,
		RemovedAddresses:             comparison.Removed,
	}
	return completeFeedWorkflow(in.w, report, in.cancellation)
}

// prepareReport builds the replacement report of one coverage merge
// (Rust ExactFeedState::prepare_report): a creation is always changed,
// a replacement is classified by its exact comparison.
func (in *exactFeedWorkflow) prepareReport(merged writer.FeedMerge) *WorkflowReport {
	logical := LogicalChanged
	if !in.create {
		logical = classifyComparison(merged.Comparison.Comparison)
	}
	comparison := merged.Comparison.Comparison
	return &WorkflowReport{
		Workflow:                     in.workflow,
		LogicalChange:                logical,
		InputRecordCount:             in.inputRecords,
		InputNormalizedIntervalCount: merged.InputIntervals,
		BeforeRangeRecordCount:       merged.Comparison.BeforeIntervals,
		AfterRangeRecordCount:        merged.Comparison.AfterIntervals,
		InputAddresses:               merged.InputAddresses,
		BeforeAddresses:              comparison.Before,
		AfterAddresses:               comparison.After,
		UnchangedValueAddresses:      comparison.Unchanged,
		ChangedValueAddresses:        comparison.Changed,
		AddedAddresses:               comparison.Added,
		RemovedAddresses:             comparison.Removed,
	}
}

// classifyComparison is the Rust workflow::classify: any added, removed,
// or changed address makes the outcome a logical change.
func classifyComparison(comparison writer.Comparison) LogicalChange {
	if comparison.Changed.Compare(format.CardinalityZero()) == 0 &&
		comparison.Added.Compare(format.CardinalityZero()) == 0 &&
		comparison.Removed.Compare(format.CardinalityZero()) == 0 {
		return LogicalNoChange
	}
	return LogicalChanged
}

// completeFeedWorkflow mirrors Rust complete_workflow: a no-change
// report discards the draft and returns the clean terminal; a changed
// report finishes the membership workflow and returns the prepared
// terminal with the captured cancellation.
func completeFeedWorkflow(h mutationHost, report *WorkflowReport, cancellation *CancellationToken) (finishedWorkflow, error) {
	if report.LogicalChange == LogicalNoChange {
		if err := h.coreOf().DiscardUnpublished(); err != nil {
			h.coreOf().MarkUnresolved(err)
			return finishedWorkflow{}, err
		}
		return finishedWorkflow{report: report}, nil
	}
	if err := h.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinishMembershipWorkflow(cancellation.check)
	}); err != nil {
		return finishedWorkflow{}, err
	}
	return finishedWorkflow{report: report, changed: true, cancellation: cancellation}, nil
}

// finishedWorkflow is the bindable outcome of one finished exact
// workflow (Rust FinishedState).
type finishedWorkflow struct {
	report       *WorkflowReport
	changed      bool
	cancellation *CancellationToken
}

// bind wraps the outcome into the public terminal handle (Rust
// FinishedState::bind).
func (out finishedWorkflow) bind(h mutationHost) *FinishedWorkflow {
	report := WorkflowReport{}
	if out.report != nil {
		report = *out.report
	}
	return &FinishedWorkflow{
		w:            h,
		report:       report,
		changed:      out.changed,
		cancellation: out.cancellation,
	}
}

// finishInput is the shared Rust ExactFeedState::finish: the state
// transitions and the public terminal handle.
func (in *exactFeedWorkflow) finishInput() (*FinishedWorkflow, error) {
	out, err := in.finishState()
	if err != nil {
		return nil, err
	}
	return out.bind(in.w), nil
}

// corruptError is the writer-package corrupt class used for the
// impossible-but-guarded disappeared-replacement branch (Rust
// Error::Corrupt("replacement feed disappeared")).
func corruptError(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

// commitPrepared publishes one prepared draft through the shared
// commit_with sequence (Rust LiveWriter::commit_operation): the
// changed-draft check, the commit attempt, the prepare-and-lock steps
// with the captured cancellation, the prepublication checks, and the
// classified outcome. markSpent records the handle's terminal state on
// every consuming path.
func (w *Writer) commitPrepared(cancellation *CancellationToken, markSpent func(), context string) (CommitResult, error) {
	draft := w.core.Draft()
	if !draft.Changed() {
		if err := w.core.DiscardUnpublished(); err != nil {
			markSpent()
			return CommitResult{}, err
		}
		markSpent()
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	attempt, err := w.core.CommitAttempt()
	if err != nil {
		markSpent()
		return CommitResult{}, err
	}
	// Rust commit_with prepare_and_lock: check, prepare, check, then
	// the sidecar lock (Go noop).
	if err := cancellation.check(); err != nil {
		return w.commitAbortAfter(attempt, err, markSpent, context), nil
	}
	if err := w.core.Prepare(cancellation.check); err != nil {
		return w.commitAbortAfter(attempt, err, markSpent, context), nil
	}
	if err := cancellation.check(); err != nil {
		return w.commitAbortAfter(attempt, err, markSpent, context), nil
	}
	// Rust prepublication_checks: unchanged base, then the sidecar scan
	// (Go noop), then the locked file covering the draft length.
	if err := w.core.RequireUnchangedBase(); err != nil {
		return w.commitAbortAfter(attempt, err, markSpent, context), nil
	}
	if err := w.core.RequireDraftLength(); err != nil {
		return w.commitAbortAfter(attempt, err, markSpent, context), nil
	}
	res := w.core.Publish(cancellation.check)
	markSpent()
	result := CommitResult{DatabaseID: attempt.DatabaseID, TransactionID: attempt.TransactionID, CommitNonce: attempt.CommitNonce, Err: res.Err}
	switch res.Status {
	case writer.PublishCommitted:
		result.Status = CommitCommitted
	case writer.PublishBeforePublication:
		// Rust finish_commit_locked_with wraps a BeforePublication
		// cause through abort_after (TransactionAborted class, draft
		// discarded) before building the NotCommitted result.
		result.Status = CommitNotCommitted
		result.Err = w.abortAfter(res.Err)
	default:
		result.Status = CommitOutcomeUnknown
	}
	return result, nil
}

// commitAbortAfter reports an aborted prepared commit exactly like the
// direct transaction commit abort (Rust commit_with abort_after): the
// result error class is TransactionAborted, and a failed abandonment
// discard nests the CleanupInProgress class.
func (w *Writer) commitAbortAfter(attempt writer.CommitAttempt, cause error, markSpent func(), context string) CommitResult {
	discardErr := w.core.DiscardUnpublished()
	markSpent()
	inner := cause
	if discardErr != nil {
		w.core.MarkUnresolved(discardErr)
		inner = &abortError{
			class: &format.Error{Code: format.CodeCleanupInProgress, Detail: context + " commit discard failed"},
			cause: cause,
		}
	}
	return CommitResult{
		Status:        CommitNotCommitted,
		DatabaseID:    attempt.DatabaseID,
		TransactionID: attempt.TransactionID,
		CommitNonce:   attempt.CommitNonce,
		Err: &abortError{
			class: &format.Error{Code: format.CodeTransactionAborted, Detail: context + " commit aborted after a preparation failure"},
			cause: inner,
		},
	}
}
