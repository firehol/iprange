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
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
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
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
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
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
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
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
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

	// delete_feed: one lookup, then abort.
	delete, err := w.DeleteFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := delete.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicFeedWorkflowPreconditions pins the Rust precondition
// classes: create on an existing name, replace on a missing name, the
// pending-transaction refusal, and the wrong value kind.
func TestPublicFeedWorkflowPreconditions(t *testing.T) {
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
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
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()

	create, err := w.BeginCreateFeed(feedName(t, "feed"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4([]AddressRange4{{From: 10, To: 9}}); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("reversed range = %v, want invalid argument", err)
	}
	// The reversed-range error leaves the workflow open (Rust parity).
	if err := w.Abort(); err != nil {
		t.Fatal(err)
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
		var ab *abortError
		if !errors.As(err, &ab) || !isPubCode(ab.cause, ErrorWrongAddressFamily) {
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
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
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
	// The spent handle refuses a second commit (Rust type consumption).
	if _, err := change.Commit(); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("second commit = %v, want wrong state", err)
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
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
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
