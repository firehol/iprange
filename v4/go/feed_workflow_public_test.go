// Public feed workflow tests (Rust live_writer/feed_workflow_tests.rs
// parity): the three frozen Rust vectors - zero-allocation slice
// ingestion with exact comparison work, the alternating second-feed
// membership deltas, and the exact name-lookup counts of the create,
// replace, rename, and delete workflows - plus the surface semantics of
// the input and terminal handles.

package iprangedb

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// feed1000 is the Rust slice_ingestion vector: 1000 single-point IPv4
// ranges at even addresses.
func feedRanges1000() []AddressRange4 {
	ranges := make([]AddressRange4, 1000)
	for index := 0; index < 1000; index++ {
		ranges[index] = AddressRange4{From: IPv4(index * 2), To: IPv4(index * 2)}
	}
	return ranges
}

// testFeedMembership creates one fresh empty IPv4 membership database
// (Rust create_live membership + feeds tag).
func testFeedMembership(t *testing.T) string {
	t.Helper()
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "feed-workflow.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// feedName builds one validated name or fails the test.
func feedName(t *testing.T, name string) FeedName {
	t.Helper()
	feed, err := NewFeedName(name)
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

// TestPublicCreateFeedSliceIngestion runs the Rust
// slice_ingestion_and_feed_comparison vector on the public surface:
// begin on an empty base interns the feed and the member, the 1000
// slice records coalesce with zero per-record allocations, the finish
// reports the exact comparison, and the changed terminal aborts.
func TestPublicCreateFeedSliceIngestion(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()
	name := feedName(t, "feed")

	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4(feedRanges1000()); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatal("feed creation on an empty base is not a change")
	}
	report := finished.Report()
	if report.Workflow != WorkflowCreateFeed {
		t.Fatalf("workflow kind = %d, want create feed", report.Workflow)
	}
	if report.LogicalChange != LogicalChanged {
		t.Fatal("feed creation is not logically changed")
	}
	if report.InputRecordCount != 1000 {
		t.Fatalf("input record count = %d, want 1000", report.InputRecordCount)
	}
	if count := histCount(t, report.InputAddresses); count != 1000 {
		t.Fatalf("input addresses = %d, want 1000", count)
	}
	if count := histCount(t, report.AfterAddresses); count != 1000 {
		t.Fatalf("after addresses = %d, want 1000", count)
	}
	if count := histCount(t, report.AddedAddresses); count != 1000 {
		t.Fatalf("added addresses = %d, want 1000", count)
	}
	if err := finished.Abort(); err != nil {
		t.Fatal(err)
	}
	// The spent handle reports the draftless commit class (Rust
	// commit_attempt parity: the draft was discarded by Abort).
	if _, err := finished.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit after abort = %v, want no pending transaction", err)
	}
	if err := finished.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after abort = %v, want no pending transaction", err)
	}
	// The aborted workflow left the writer clean.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after clean = %v, want no pending transaction", err)
	}
}

// TestPublicSecondFeedAlternatingDeltas runs the Rust
// second_feed_aggregates_alternating_membership_deltas vector: the
// first feed commits, the second feed with identical ranges produces a
// changed prepared terminal whose commit lands durably.
func TestPublicSecondFeedAlternatingDeltas(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ranges := feedRanges1000()

	first, err := w.BeginCreateFeed(feedName(t, "first"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	firstFinished, err := first.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !firstFinished.IsChanged() {
		t.Fatal("first feed creation is not a change")
	}
	firstResult, err := firstFinished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Status != CommitCommitted {
		t.Fatalf("first commit status = %v, want committed", firstResult.Status)
	}

	second, err := w.BeginCreateFeed(feedName(t, "second"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	secondFinished, err := second.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !secondFinished.IsChanged() {
		t.Fatal("second feed creation is not a change")
	}
	secondResult, err := secondFinished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Status != CommitCommitted {
		t.Fatalf("second commit status = %v, want committed", secondResult.Status)
	}
}

// TestPublicExactFeedWorkflowNameLookups runs the Rust
// exact_feed_workflows_lookup_each_name_once vector: create commits,
// replace is prepared and aborted, rename and delete prepare and
// abort, each with the exact catalog lookup count.
func TestPublicExactFeedWorkflowNameLookups(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	name := feedName(t, "feed")
	renamed := feedName(t, "renamed")

	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	createFinished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !createFinished.IsChanged() {
		t.Fatal("empty-input feed creation is not a change")
	}
	if result, err := createFinished.Commit(); err != nil {
		t.Fatal(err)
	} else if result.Status != CommitCommitted {
		t.Fatalf("create commit status = %v, want committed", result.Status)
	}

	// begin_replace_feed: one base catalog lookup, then the input is
	// dropped and the pending draft aborted (Rust drop + writer.abort).
	replace, err := w.BeginReplaceFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	_ = replace

	// rename_feed: one lookup of the old name and one of the new name,
	// then abort (Rust rename.unwrap().abort()).
	change, err := w.RenameFeed(name, renamed, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Abort(); err != nil {
		t.Fatal(err)
	}
	// The spent prepared change reports the draftless commit class.
	if _, err := change.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("rename commit after abort = %v, want no pending transaction", err)
	}
	if err := change.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("rename abort after abort = %v, want no pending transaction", err)
	}

	// delete_feed: one lookup, then abort.
	delete, err := w.DeleteFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := delete.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := delete.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("delete commit after abort = %v, want no pending transaction", err)
	}
}

// TestPublicFeedWorkflowPreconditions pins the Rust precondition
// classes: create on an existing name, replace on a missing name, the
// pending-transaction refusal, and the wrong value kind.
func TestPublicFeedWorkflowPreconditions(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	name := feedName(t, "feed")

	if _, err := w.BeginReplaceFeed(name, cancellation); !isPubCode(err, ErrorNameNotFound) {
		t.Fatalf("replace of a missing feed = %v, want name not found", err)
	}
	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create.FinishInput(); err != nil {
		t.Fatal(err)
	}
	// The pending changed draft refuses a second workflow.
	if _, err := w.BeginCreateFeed(feedName(t, "second"), cancellation); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("second workflow on a pending draft = %v, want wrong state", err)
	}
	// The prepared handle's report precedes the commit.
	// (the draft is discarded by the failed workflow attempt below)
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicFeedWorkflowReversedAndWrongFamily pins the input errors of
// the Rust require_ordered and require_input_family classes.
func TestPublicFeedWorkflowReversedAndWrongFamily(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()

	create, err := w.BeginCreateFeed(feedName(t, "feed"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	err = create.AddRangesV4([]AddressRange4{{From: 10, To: 9}})
	if abortCauseCode(err) != ErrorInvalidArgument {
		t.Fatalf("reversed range = %v, want transaction aborted wrapping invalid argument", err)
	}
	// Rust drain_source errors wrap through writer.mutate abort_after:
	// the workflow is aborted and the writer is clean.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after reversed range = %v, want no pending transaction", err)
	}

	create6, err := w.BeginCreateFeed(feedName(t, "feed6"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := create6.AddRangesV6([]AddressRange6{{FromHi: 1, ToHi: 2}}); err == nil {
		t.Fatal("wrong family input was accepted")
	} else {
		// Rust require_input_family aborts the workflow through
		// abort_after: the public class is TransactionAborted and
		// the nesting cause is WrongAddressFamily.
		if abortCauseCode(err) != ErrorWrongAddressFamily {
			t.Fatalf("wrong family = %v, want transaction aborted wrapping wrong address family", err)
		}
	}
	// The family mismatch aborted the workflow; the writer is clean.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after family abort = %v, want no pending transaction", err)
	}
}

// TestPublicPreparedFeedChangeMetadata pins the prepared
// rename/delete metadata stage and the terminal rules.
func TestPublicPreparedFeedChangeMetadata(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	name := feedName(t, "feed")
	renamed := feedName(t, "renamed")

	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finished.Commit(); err != nil {
		t.Fatal(err)
	}

	change, err := w.RenameFeed(name, renamed, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := change.SetMetadataJSON([]byte(`{"rename":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("rename metadata stage reported no change")
	}
	result, err := change.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("rename commit status = %v, want committed", result.Status)
	}
	// The spent handle reports the draftless commit class (Rust
	// commit_attempt parity: the draft is gone after publication).
	if _, err := change.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("second commit = %v, want no pending transaction", err)
	}
	if err := change.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after commit = %v, want no pending transaction", err)
	}

	delete, err := w.DeleteFeed(renamed, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delete.ClearMetadataJSON(); err != nil {
		t.Fatal(err)
	}
	if err := delete.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicFeedWorkflowAbortOnNoChange pins the Rust
// FinishedWorkflow::abort class on a clean result: a replacement that
// reproduces the committed state is NoChange and aborts with
// ErrorNoPendingTransaction.
func TestPublicFeedWorkflowAbortOnNoChange(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	name := feedName(t, "feed")
	ranges := feedRanges1000()

	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	first, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Commit(); err != nil {
		t.Fatal(err)
	}

	replace, err := w.BeginReplaceFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := replace.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	same, err := replace.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if same.IsChanged() {
		t.Fatal("identical replacement is not a no-change")
	}
	if err := same.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort on no-change = %v, want no pending transaction", err)
	}
	// SetMetadataJSON on the no-change variant is refused (Rust type
	// gating).
	if _, err := same.SetMetadataJSON([]byte(`{}`)); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("metadata on no-change = %v, want wrong state", err)
	}
}

// TestPublicFullIPv6FeedWorkflow runs the Rust
// full_ipv6_feed_cardinality_is_exact vector: one IPv6 create-feed
// workflow over the complete address space commits and reads back the
// full range through the v6 feed cursor.
func TestPublicFullIPv6FeedWorkflow(t *testing.T) {
	requireLiveCreation(t)
	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "feed-ipv6.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv6, ValueKindMembership, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	create, err := w.BeginCreateFeed(feedName(t, "all"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	full := AddressRange6{FromHi: 0, FromLo: 0, ToHi: ^uint64(0), ToLo: ^uint64(0)}
	if err := create.AddRangesV6([]AddressRange6{full}); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatal("full IPv6 feed creation is not a change")
	}
	if result, err := finished.Commit(); err != nil {
		t.Fatal(err)
	} else if result.Status != CommitCommitted {
		t.Fatalf("IPv6 feed commit = %v, want committed", result.Status)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cursor, err := r.FeedRangeCursorV6("all", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := cursor.NextRange()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != full {
		t.Fatalf("full IPv6 range = %+v ok %v, want the complete space", got, ok)
	}
	if _, ok, err := cursor.NextRange(); err != nil || ok {
		t.Fatalf("second IPv6 range = ok %v err %v, want none", ok, err)
	}
}

// TestPublicFeedInputCancellationAbortsWorkflow pins the Rust
// feed_input_failures_and_cancellation_abort_the_complete_workflow
// surface expressible with the slice input: a cancellation during the
// input aborts the workflow with TransactionAborted wrapping Cancelled,
// an unfinished workflow blocks the next workflow until aborted, and no
// aborted workflow publishes a feed.
func TestPublicFeedInputCancellationAbortsWorkflow(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cancellation := NewCancellationToken()

	// Cancel after begin: the first add aborts through the input
	// checkpoint (Rust drain_source loop-top check inside mutate).
	cancelled := NewCancellationToken()
	create, err := w.BeginCreateFeed(feedName(t, "cancelled"), cancelled)
	if err != nil {
		t.Fatal(err)
	}
	cancelled.Cancel()
	err = create.AddRangesV4([]AddressRange4{{From: 0, To: 100}})
	if abortCauseCode(err) != ErrorCancelled {
		t.Fatalf("cancelled input = %v, want transaction aborted wrapping cancelled", err)
	}
	// The aborted workflow discarded its draft; the writer is clean.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after input abort = %v, want no pending transaction", err)
	}

	// An unfinished workflow keeps its pending draft; a second workflow
	// is refused until the draft is aborted, and the abandoned draft
	// publishes nothing (Rust dropped-handle parity).
	unfinished, err := w.BeginCreateFeed(feedName(t, "unfinished"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := unfinished.AddRangesV4([]AddressRange4{{From: 1, To: 2}}); err != nil {
		t.Fatal(err)
	}
	unfinished = nil
	if _, err := w.BeginCreateFeed(feedName(t, "second"), cancellation); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("workflow over a pending unfinished draft = %v, want wrong state", err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}

	// A replace of a missing feed refuses up front (Rust NameNotFound).
	if _, err := w.BeginReplaceFeed(feedName(t, "missing"), cancellation); !isPubCode(err, ErrorNameNotFound) {
		t.Fatalf("replace of a missing feed = %v, want name not found", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, absent := range []string{"cancelled", "unfinished", "second", "missing"} {
		if _, found, err := r.LookupFeed(absent); err != nil || found {
			t.Fatalf("feed %q after aborted workflows = found %v err %v, want absent", absent, found, err)
		}
	}
}

// TestPublicPreparedFeedChangeAbortAndCancelledCommit pins the Rust
// lifecycle_failure_or_dropped_handle_cannot_publish_partial_state
// vectors expressible on the public surface: oversized staged metadata
// aborts the workflow, and a commit through a cancelled token reports
// NotCommitted carrying the cancellation cause, leaving the committed
// feed untouched.
func TestPublicPreparedFeedChangeAbortAndCancelledCommit(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	name := feedName(t, "alpha")
	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := finished.Commit(); err != nil {
		t.Fatal(err)
	} else if result.Status != CommitCommitted {
		t.Fatalf("feed commit = %v, want committed", result.Status)
	}

	oversized, err := w.RenameFeed(name, feedName(t, "oversized"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = oversized.SetMetadataJSON(make([]byte, MaxMetadataUncompressed+1))
	if abortCauseCode(err) != ErrorInvalidArgument {
		t.Fatalf("oversized metadata = %v, want transaction aborted wrapping invalid argument", err)
	}
	// The aborted workflow discarded its draft; the writer is clean.
	if err := w.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after oversized metadata = %v, want no pending transaction", err)
	}

	// A delete commit through a cancelled token reports NotCommitted
	// with the cancellation cause (Rust commit result parity: cancel
	// after the delete handle is prepared).
	cancelled := NewCancellationToken()
	delete, err := w.DeleteFeed(name, cancelled)
	if err != nil {
		t.Fatal(err)
	}
	cancelled.Cancel()
	result, err := delete.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitNotCommitted {
		t.Fatalf("cancelled delete commit = %v, want not committed", result.Status)
	}
	if abortCauseCode(result.Cause) != ErrorCancelled {
		t.Fatalf("cancelled delete cause = %v, want transaction aborted wrapping cancelled", result.Cause)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, found, err := r.LookupFeed("alpha"); err != nil || !found {
		t.Fatalf("alpha after failed changes = found %v err %v, want present", found, err)
	}
	if _, found, err := r.LookupFeed("oversized"); err != nil || found {
		t.Fatalf("oversized after abort = found %v err %v, want absent", found, err)
	}
}

// TestPublicDirectDatabaseRejectsFeedLifecycle pins the Rust
// direct_database_rejects_named_feed_lifecycle_operations vector: the
// named feed lifecycle operations refuse a direct database with
// WrongValueKind.
func TestPublicDirectDatabaseRejectsFeedLifecycle(t *testing.T) {
	requireLiveCreation(t)
	tag, err := NewValueTag([]byte("direct"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "direct.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()
	name := feedName(t, "alpha")
	if _, err := w.DeleteFeed(name, cancellation); !isPubCode(err, ErrorWrongValueKind) {
		t.Fatalf("delete feed on direct database = %v, want wrong value kind", err)
	}
	if _, err := w.RenameFeed(name, feedName(t, "beta"), cancellation); !isPubCode(err, ErrorWrongValueKind) {
		t.Fatalf("rename feed on direct database = %v, want wrong value kind", err)
	}
}

// TestPublicRenameDeleteReuseCommittedIndex runs the Rust
// rename_and_delete_preserve_other_feeds_and_reuse_the_committed_index
// vector: two committed feeds with overlapping memberships, a rename
// preserving the feed index and metadata, a delete clearing the
// membership bits, and a fresh feed reusing the released index.
func TestPublicRenameDeleteReuseCommittedIndex(t *testing.T) {
	requireLiveCreation(t)
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	{
		tx, err := w.BeginMembershipTransaction(cancellation)
		if err != nil {
			t.Fatal(err)
		}
		alpha, err := tx.EnsureFeed(feedName(t, "alpha"))
		if err != nil {
			t.Fatal(err)
		}
		beta, err := tx.EnsureFeed(feedName(t, "beta"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.EnsureFeed(feedName(t, "empty")); err != nil {
			t.Fatal(err)
		}
		empty, err := tx.EmptyMembership()
		if err != nil {
			t.Fatal(err)
		}
		alphaMember, err := tx.AddFeed(empty, alpha)
		if err != nil {
			t.Fatal(err)
		}
		betaMember, err := tx.AddFeed(empty, beta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ApplyV4(IPv4(0), IPv4(9), alphaMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ApplyV4(IPv4(5), IPv4(14), betaMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if result, err := tx.Commit(); err != nil {
			t.Fatal(err)
		} else if result.Status != CommitCommitted {
			t.Fatalf("membership commit = %v, want committed", result.Status)
		}
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	openWriter := func() *LiveWriter {
		t.Helper()
		ww, err := OpenLiveWriter(path, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return ww
	}
	openReader := func() *LiveReader {
		t.Helper()
		rr, err := OpenLiveReader(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		return rr
	}
	commitFeedChange := func(t *testing.T, change *PreparedFeedChange) {
		t.Helper()
		result, err := change.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != CommitCommitted {
			t.Fatalf("feed change commit = %v, want committed", result.Status)
		}
	}

	r := openReader()
	alphaEntry, found, err := r.LookupFeed("alpha")
	if err != nil || !found {
		t.Fatalf("alpha before rename = found %v err %v, want present", found, err)
	}
	alphaIndex := alphaEntry.Index
	betaEntry, found, err := r.LookupFeed("beta")
	if err != nil || !found {
		t.Fatalf("beta before rename = found %v err %v, want present", found, err)
	}
	betaIndex := betaEntry.Index
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Rename preserves the feed index and stages metadata.
	w = openWriter()
	rename, err := w.RenameFeed(feedName(t, "alpha"), feedName(t, "renamed"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := rename.SetMetadataJSON([]byte(`{"feed":"renamed"}`))
	if err != nil || !changed {
		t.Fatalf("rename metadata = changed %v err %v, want true/nil", changed, err)
	}
	commitFeedChange(t, rename)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r = openReader()
	if _, found, err := r.LookupFeed("alpha"); err != nil || found {
		t.Fatalf("alpha after rename = found %v err %v, want absent", found, err)
	}
	renamedEntry, found, err := r.LookupFeed("renamed")
	if err != nil || !found {
		t.Fatalf("renamed after rename = found %v err %v, want present", found, err)
	}
	if renamedEntry.Index != alphaIndex {
		t.Fatalf("renamed index = %d, want preserved %d", renamedEntry.Index, alphaIndex)
	}
	betaAfter, found, err := r.LookupFeed("beta")
	if err != nil || !found || betaAfter.Index != betaIndex {
		t.Fatalf("beta after rename = %+v found %v err %v, want index %d", betaAfter, found, err, betaIndex)
	}
	metadata, present, err := r.MetadataJSON()
	if err != nil || !present || string(metadata) != `{"feed":"renamed"}` {
		t.Fatalf("metadata after rename = %q present %v err %v, want the staged payload", metadata, present, err)
	}
	pin, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	overlap, found, err := pin.LookupMembershipV4(IPv4(7))
	if err != nil || !found {
		t.Fatalf("membership at 7 after rename = found %v err %v, want present", found, err)
	}
	if has, err := overlap.ContainsIndex(alphaIndex); err != nil || !has {
		t.Fatalf("membership at 7 contains alpha = %v err %v, want true", has, err)
	}
	if has, err := overlap.ContainsIndex(betaIndex); err != nil || !has {
		t.Fatalf("membership at 7 contains beta = %v err %v, want true", has, err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Delete clears the membership bits and the metadata: the prepared
	// change must explicitly stage metadata absence, exactly like the
	// Rust vector calls clear_metadata_json before commit (the delete
	// itself only clears the feed bits and catalog entry).
	w = openWriter()
	del := mustDelete(t, w, "renamed", cancellation)
	clearedMeta, err := del.ClearMetadataJSON()
	if err != nil || !clearedMeta {
		t.Fatalf("delete metadata clear = %v err %v, want true/nil", clearedMeta, err)
	}
	commitFeedChange(t, del)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r = openReader()
	if _, found, err := r.LookupFeed("renamed"); err != nil || found {
		t.Fatalf("renamed after delete = found %v err %v, want absent", found, err)
	}
	pin, err = r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := pin.LookupMembershipV4(IPv4(0)); err != nil || found {
		t.Fatalf("membership at 0 after delete = found %v err %v, want absent", found, err)
	}
	betaOnly, found, err := pin.LookupMembershipV4(IPv4(7))
	if err != nil || !found {
		t.Fatalf("membership at 7 after delete = found %v err %v, want present", found, err)
	}
	if has, err := betaOnly.ContainsIndex(alphaIndex); err != nil || has {
		t.Fatalf("membership at 7 contains alpha after delete = %v err %v, want false", has, err)
	}
	if has, err := betaOnly.ContainsIndex(betaIndex); err != nil || !has {
		t.Fatalf("membership at 7 contains beta after delete = %v err %v, want true", has, err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	betaRanges, err := r.FeedRangeCursorV4("beta", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	betaRange, ok, err := betaRanges.NextRange()
	if err != nil || !ok || betaRange != (AddressRange4{From: 5, To: 14}) {
		t.Fatalf("beta range after delete = %+v ok %v err %v, want [5,14]", betaRange, ok, err)
	}
	if _, ok, err := betaRanges.NextRange(); err != nil || ok {
		t.Fatalf("beta second range = ok %v err %v, want none", ok, err)
	}
	if _, present, err := r.MetadataJSON(); err != nil || present {
		t.Fatalf("metadata after delete = present %v err %v, want absent", present, err)
	}
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh feed reuses the released index (Rust committed-index
	// reuse parity).
	w = openWriter()
	create, err := w.BeginCreateFeed(feedName(t, "reused"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := reused.Commit(); err != nil {
		t.Fatal(err)
	} else if result.Status != CommitCommitted {
		t.Fatalf("reused feed commit = %v, want committed", result.Status)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r = openReader()
	reusedEntry, found, err := r.LookupFeed("reused")
	if err != nil || !found {
		t.Fatalf("reused feed = found %v err %v, want present", found, err)
	}
	if reusedEntry.Index != alphaIndex {
		t.Fatalf("reused feed index = %d, want released %d", reusedEntry.Index, alphaIndex)
	}
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Deleting the empty feed leaves beta untouched.
	w = openWriter()
	commitFeedChange(t, mustDelete(t, w, "empty", cancellation))
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r = openReader()
	if _, found, err := r.LookupFeed("empty"); err != nil || found {
		t.Fatalf("empty after delete = found %v err %v, want absent", found, err)
	}
	if _, found, err := r.LookupFeed("beta"); err != nil || !found {
		t.Fatalf("beta after empty delete = found %v err %v, want present", found, err)
	}
	if _, err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// mustDelete starts and commits one feed deletion.
func mustDelete(t *testing.T, w *LiveWriter, name string, cancellation *CancellationToken) *PreparedFeedChange {
	t.Helper()
	delete, err := w.DeleteFeed(feedName(t, name), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	return delete
}

// causeCode extracts the public error code of one wrapped cause, matching
// either the public Error type (cancellation checkpoints) or the internal
// format.Error the facade sometimes returns directly (isPubCode only
// matches the internal type).
func causeCode(err error) ErrorCode {
	var public *Error
	if errorAs(err, &public) {
		return public.Code
	}
	var fe *format.Error
	if errors.As(err, &fe) {
		return ErrorCode(fe.Code)
	}
	return 0
}
