//go:build v4work

// Necessary-work pins for the public named-feed workflows (SOW-0025
// slice B), mirroring the three Rust feed_workflow_tests.rs vectors with
// crate::work::measure: begin interns the feed and its member exactly
// once; the 1000-record slice ingestion coalesces with zero membership
// work and zero output passes; the ordered empty-map finish rescans the
// completed private ranges once and spills each refcount delta once; the
// second-feed merge performs range-proportional refcount and delta-tree
// work; and replace/rename/delete look the catalog up exactly once per
// proven name. The fixtures are frozen, so the exact counter deltas are
// stable; a change that adds or removes hot-path work is visible.

package iprangedb

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestWorkBeginCreateFeedPins pins the Rust begin_create_feed vector:
// one base lookup, one catalog intern, and one membership intern.
func TestWorkBeginCreateFeedPins(t *testing.T) {
	requireFileCreation(t)
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()

	work.Reset()
	if _, err := w.BeginCreateFeed(feedName(t, "feed"), cancellation); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.CatalogLookups != 1 {
		t.Fatalf("begin catalog lookups = %d, want 1", snap.CatalogLookups)
	}
	if snap.CatalogInterns != 1 {
		t.Fatalf("begin catalog interns = %d, want 1", snap.CatalogInterns)
	}
	if snap.MembershipInterns != 1 {
		t.Fatalf("begin membership interns = %d, want 1", snap.MembershipInterns)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkSliceIngestionPins pins the Rust
// slice_ingestion_and_feed_comparison add vector: one source pass,
// exactly one consumed range per record, the coalesced emission, and
// zero membership and output work during ingestion.
func TestWorkSliceIngestionPins(t *testing.T) {
	requireFileCreation(t)
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
	ranges := feedRanges1000()

	work.Reset()
	if err := create.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.SourcePasses != 1 {
		t.Fatalf("slice source passes = %d, want 1", snap.SourcePasses)
	}
	if snap.RangesConsumed != 1000 {
		t.Fatalf("slice ranges consumed = %d, want 1000", snap.RangesConsumed)
	}
	if snap.RangesEmitted != 999 {
		t.Fatalf("slice ranges emitted = %d, want 999", snap.RangesEmitted)
	}
	if snap.MembershipLookups != 0 {
		t.Fatalf("slice membership lookups = %d, want 0", snap.MembershipLookups)
	}
	if snap.MembershipInterns != 0 {
		t.Fatalf("slice membership interns = %d, want 0", snap.MembershipInterns)
	}
	if snap.OutputPasses != 0 {
		t.Fatalf("slice output passes = %d, want 0", snap.OutputPasses)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkEmptyMapFinishPins pins the Rust finish_input vector of the
// ordered empty-map creation: one refcount-charged tree lookup, one
// ordered rescan source pass, no output pass, the sealed constant ranges
// counted as emitted without being consumed again, and each refcount
// delta spilled exactly once.
func TestWorkEmptyMapFinishPins(t *testing.T) {
	requireFileCreation(t)
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
	if err := create.AddRangesV4(feedRanges1000()); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.TreeLookups != 1 {
		t.Fatalf("empty-map finish tree lookups = %d, want 1", snap.TreeLookups)
	}
	if snap.SourcePasses != 1 {
		t.Fatalf("empty-map finish source passes = %d, want 1 (ordered rescan)", snap.SourcePasses)
	}
	if snap.OutputPasses != 0 {
		t.Fatalf("empty-map finish output passes = %d, want 0", snap.OutputPasses)
	}
	if snap.RangesConsumed != 0 {
		t.Fatalf("empty-map finish ranges consumed = %d, want 0", snap.RangesConsumed)
	}
	if snap.RangesEmitted != 1 {
		t.Fatalf("empty-map finish ranges emitted = %d, want 1", snap.RangesEmitted)
	}
	if snap.MembershipLookups != 1 {
		t.Fatalf("empty-map finish membership lookups = %d, want 1", snap.MembershipLookups)
	}
	if snap.MembershipInterns != 0 {
		t.Fatalf("empty-map finish membership interns = %d, want 0", snap.MembershipInterns)
	}
	if snap.MembershipRefcountBatches != 1 {
		t.Fatalf("empty-map finish refcount batches = %d, want 1", snap.MembershipRefcountBatches)
	}
	if snap.MembershipDeltaSpills != 1 {
		t.Fatalf("empty-map finish delta spills = %d, want 1", snap.MembershipDeltaSpills)
	}
	if err := finished.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkSecondFeedMergePins pins the Rust
// second_feed_aggregates_alternating_membership_deltas finish vector:
// the uniform 1000-record merge submits three range-proportional
// refcount batches and three delta-tree spills in one output pass.
func TestWorkSecondFeedMergePins(t *testing.T) {
	requireFileCreation(t)
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()
	ranges := feedRanges1000()

	first, err := w.BeginCreateFeed(feedName(t, "first"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	done, err := first.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := done.Commit(); err != nil {
		t.Fatal(err)
	} else if result.Status != CommitCommitted {
		t.Fatalf("first feed commit = %v, want committed", result.Status)
	}

	second, err := w.BeginCreateFeed(feedName(t, "second"), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AddRangesV4(ranges); err != nil {
		t.Fatal(err)
	}
	work.Reset()
	finished, err := second.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.OutputPasses != 1 {
		t.Fatalf("second-feed merge output passes = %d, want 1", snap.OutputPasses)
	}
	if snap.MembershipRefcountBatches != 3 {
		t.Fatalf("second-feed merge refcount batches = %d, want 3", snap.MembershipRefcountBatches)
	}
	if snap.MembershipDeltaSpills != 3 {
		t.Fatalf("second-feed merge delta spills = %d, want 3", snap.MembershipDeltaSpills)
	}
	if err := finished.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkFeedLifecycleLookupPins pins the Rust
// exact_feed_workflows_lookup_each_name_once vector: replace proves the
// base name once, rename proves old and new names once each, and delete
// proves the base name once.
func TestWorkFeedLifecycleLookupPins(t *testing.T) {
	requireFileCreation(t)
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cancellation := NewCancellationToken()
	name := feedName(t, "feed")
	renamed := feedName(t, "renamed")

	create, err := w.BeginCreateFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	done, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := done.Commit(); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	replace, err := w.BeginReplaceFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.CatalogLookups != 1 {
		t.Fatalf("replace catalog lookups = %d, want 1", snap.CatalogLookups)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	_ = replace

	work.Reset()
	rename, err := w.RenameFeed(name, renamed, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	snap = work.Read()
	if snap.CatalogLookups != 2 {
		t.Fatalf("rename catalog lookups = %d, want 2", snap.CatalogLookups)
	}
	if err := rename.Abort(); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	remove, err := w.DeleteFeed(name, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	snap = work.Read()
	if snap.CatalogLookups != 1 {
		t.Fatalf("delete catalog lookups = %d, want 1", snap.CatalogLookups)
	}
	if err := remove.Abort(); err != nil {
		t.Fatal(err)
	}
}
