// Randomized scalar-model property test of the named-feed replace
// workflows (Rust tests/feed_workflow_properties.rs parity): one
// deterministic xorshift generator (seed 0xa7b11c49d38e52f0) drives
// 100 rounds of complete replacement of the "target" feed over a
// 128-address domain; a second "other" feed built from every 4th
// address must survive every round untouched. The independent scalar
// model derives the exact expected report and membership of both feeds
// after every round. Go has no live-reader sidecar (Milestone 4), so
// the writer closes after each committed round before the immutable
// reader asserts the database, and reopens for the next round, unlike
// the Rust test that keeps the live writer open across rounds.

package iprangedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// feedPropertyPairedCount counts the addresses whose before/after
// presence matches the requested pair (Rust paired_count).
func feedPropertyPairedCount(before, after *[workflowDomain]bool, old, new bool) uint64 {
	var count uint64
	for index := 0; index < workflowDomain; index++ {
		if before[index] == old && after[index] == new {
			count++
		}
	}
	return count
}

// feedPropertyAssertReport checks the exact replacement report against
// the scalar presence model (Rust assert_report): membership has no
// value dimension, so changed-value addresses are always zero and the
// logical change is decided by added/removed addresses alone.
func feedPropertyAssertReport(t *testing.T, report WorkflowReport, before, after *[workflowDomain]bool, inputRecords uint64, context string) {
	t.Helper()
	unchanged := feedPropertyPairedCount(before, after, true, true)
	added := feedPropertyPairedCount(before, after, false, true)
	removed := feedPropertyPairedCount(before, after, true, false)
	logical := LogicalNoChange
	if added != 0 || removed != 0 {
		logical = LogicalChanged
	}
	if report.LogicalChange != logical {
		t.Fatalf("%s: logical change = %v, want %v", context, report.LogicalChange, logical)
	}
	if report.InputRecordCount != inputRecords {
		t.Fatalf("%s: input record count = %d, want %d", context, report.InputRecordCount, inputRecords)
	}
	if report.InputNormalizedIntervalCount != workflowBooleanRuns(after) {
		t.Fatalf("%s: input normalized intervals = %d, want %d", context, report.InputNormalizedIntervalCount, workflowBooleanRuns(after))
	}
	if report.BeforeRangeRecordCount != workflowBooleanRuns(before) {
		t.Fatalf("%s: before range records = %d, want %d", context, report.BeforeRangeRecordCount, workflowBooleanRuns(before))
	}
	if report.AfterRangeRecordCount != workflowBooleanRuns(after) {
		t.Fatalf("%s: after range records = %d, want %d", context, report.AfterRangeRecordCount, workflowBooleanRuns(after))
	}
	if report.InputAddresses.Lo() != workflowBooleanCount(after) {
		t.Fatalf("%s: input addresses = %d, want %d", context, report.InputAddresses.Lo(), workflowBooleanCount(after))
	}
	if report.BeforeAddresses.Lo() != workflowBooleanCount(before) {
		t.Fatalf("%s: before addresses = %d, want %d", context, report.BeforeAddresses.Lo(), workflowBooleanCount(before))
	}
	if report.AfterAddresses.Lo() != workflowBooleanCount(after) {
		t.Fatalf("%s: after addresses = %d, want %d", context, report.AfterAddresses.Lo(), workflowBooleanCount(after))
	}
	if report.UnchangedValueAddresses.Lo() != unchanged {
		t.Fatalf("%s: unchanged addresses = %d, want %d", context, report.UnchangedValueAddresses.Lo(), unchanged)
	}
	if report.ChangedValueAddresses.Compare(CardinalityZero()) != 0 {
		t.Fatalf("%s: changed-value addresses = %v, want zero", context, report.ChangedValueAddresses)
	}
	if report.AddedAddresses.Lo() != added {
		t.Fatalf("%s: added addresses = %d, want %d", context, report.AddedAddresses.Lo(), added)
	}
	if report.RemovedAddresses.Lo() != removed {
		t.Fatalf("%s: removed addresses = %d, want %d", context, report.RemovedAddresses.Lo(), removed)
	}
}

// feedPropertyFinish commits the changed terminal or drops the
// no-change terminal and verifies durable publication (Rust finish).
func feedPropertyFinish(t *testing.T, finished *FinishedWorkflow, context string) {
	t.Helper()
	if !finished.IsChanged() {
		return
	}
	result, err := finished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("%s: commit status = %v (%v), want committed", context, result.Status, result.Err)
	}
}

// feedPropertyAssertDatabase checks the target and other feed
// membership of every address through the immutable reader (Rust
// assert_database).
func feedPropertyAssertDatabase(t *testing.T, path string, target, other *[workflowDomain]bool, iteration int) {
	t.Helper()
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	targetEntry, found, err := r.LookupFeed("target")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("iteration %d: feed target is missing", iteration)
	}
	otherEntry, found, err := r.LookupFeed("other")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("iteration %d: feed other is missing", iteration)
	}
	pin, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	for address := 0; address < workflowDomain; address++ {
		targetMember := false
		otherMember := false
		view, viewFound, err := pin.LookupMembershipV4(IPv4(address))
		if err != nil {
			t.Fatal(err)
		}
		if viewFound {
			if targetMember, err = view.ContainsIndex(targetEntry.Index); err != nil {
				t.Fatal(err)
			}
			if otherMember, err = view.ContainsIndex(otherEntry.Index); err != nil {
				t.Fatal(err)
			}
		}
		if targetMember != target[address] {
			t.Fatalf("iteration %d: target membership at address %d = %v, want %v", iteration, address, targetMember, target[address])
		}
		if otherMember != other[address] {
			t.Fatalf("iteration %d: other membership at address %d = %v, want %v", iteration, address, otherMember, other[address])
		}
	}
}

// TestRandomizedFeedReplacementMatchesScalarSetsAndPreservesOtherFeed
// mirrors Rust
// randomized_feed_replacement_matches_scalar_sets_and_preserves_other_feed:
// 100 rounds of complete replacement of one feed while a second feed
// built from every 4th address stays untouched.
func TestRandomizedFeedReplacementMatchesScalarSetsAndPreservesOtherFeed(t *testing.T) {
	requireFileCreation(t)
	tag, err := NewValueTag([]byte("membership"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "feed-property.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}

	cancellation := NewCancellationToken()
	targetName := FeedName("target")
	otherName := FeedName("other")

	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	create, err := w.BeginCreateFeed(targetName, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	feedPropertyFinish(t, finished, "create target")
	create, err = w.BeginCreateFeed(otherName, cancellation)
	if err != nil {
		t.Fatal(err)
	}
	var otherRanges []AddressRange4
	var otherExpected [workflowDomain]bool
	for index := 0; index < workflowDomain; index += 4 {
		otherRanges = append(otherRanges, AddressRange4{From: IPv4(index), To: IPv4(index + 1)})
		for address := index; address <= index+1; address++ {
			otherExpected[address] = true
		}
	}
	if err := create.AddRangesV4(otherRanges); err != nil {
		t.Fatal(err)
	}
	finished, err = create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	feedPropertyFinish(t, finished, "create other")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var random workflowRandom
	random.state = 0xa7b11c49d38e52f0
	var before [workflowDomain]bool
	for iteration := 0; iteration < 100; iteration++ {
		recordCount := int(random.below(24))
		var after [workflowDomain]bool
		records := make([]AddressRange4, 0, recordCount)
		for index := 0; index < recordCount; index++ {
			from, to := random.span()
			records = append(records, AddressRange4{From: IPv4(from), To: IPv4(to)})
			for address := from; address <= to; address++ {
				after[address] = true
			}
		}
		w, err := OpenWriter(path, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		replacement, err := w.BeginReplaceFeed(targetName, cancellation)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(records); start += 4 {
			end := start + 4
			if end > len(records) {
				end = len(records)
			}
			if err := replacement.AddRangesV4(records[start:end]); err != nil {
				t.Fatal(err)
			}
		}
		finished, err := replacement.FinishInput()
		if err != nil {
			t.Fatal(err)
		}
		context := fmt.Sprintf("feed replacement iteration %d", iteration)
		feedPropertyAssertReport(t, finished.Report(), &before, &after, uint64(recordCount), context)
		feedPropertyFinish(t, finished, context)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		feedPropertyAssertDatabase(t, path, &after, &otherExpected, iteration)
		before = after
	}
}
