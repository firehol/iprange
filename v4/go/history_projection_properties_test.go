// Randomized scalar-model property test of the history projection over
// the live surface (Rust tests/history_projection_properties.rs
// parity): one deterministic xorshift generator drives 40 rounds of
// direct last_seen source replacement over a 128-address domain; the
// independent scalar model derives the exact expected per-window and
// aggregate report after every round plus the committed destination
// membership. Every fixture is a live pair: the projection source is
// the committed generation of the source path bound by a live reader
// (HistoryProjectionSourceLive after the source writer closes), and
// the destination writer closes after each committed round before the
// live reader asserts the database, unlike the Rust test that keeps
// both live writers open across rounds.

package iprangedb

import (
	"path/filepath"
	"testing"
)

// historyPropertyWindows is the Rust WINDOW_COUNT.
const historyPropertyWindows = 4

// histPropertyCreateSource writes one fresh empty last_seen direct IPv4
// database; every round replaces its whole content through the direct
// replacement workflow (Rust create_live ValueTag::LAST_SEEN).
func histPropertyCreateSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history-property-source.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen(), 4, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// histPropertyChanges classifies one window's address transitions
// between the before and after boolean states (Rust changes).
func histPropertyChanges(before, after *[workflowDomain]bool) (unchanged, added, removed uint64) {
	for address := 0; address < workflowDomain; address++ {
		switch {
		case before[address] && after[address]:
			unchanged++
		case !before[address] && after[address]:
			added++
		case before[address] && !after[address]:
			removed++
		}
	}
	return unchanged, added, removed
}

// histPropertyUnion is the per-address OR across every window state
// (Rust union).
func histPropertyUnion(states *[historyPropertyWindows][workflowDomain]bool) [workflowDomain]bool {
	var result [workflowDomain]bool
	for address := 0; address < workflowDomain; address++ {
		for window := 0; window < historyPropertyWindows; window++ {
			if states[window][address] {
				result[address] = true
				break
			}
		}
	}
	return result
}

// TestRandomizedProjectionMatchesIndependentScalarModel mirrors Rust
// randomized_projection_matches_independent_scalar_model: 40 rounds of
// direct last_seen replacement projected into four window feeds plus
// one pre-existing unrelated feed, with the exact report and committed
// membership asserted against the scalar model every round.
func TestRandomizedProjectionMatchesIndependentScalarModel(t *testing.T) {
	requireLiveCreation(t)
	sourcePath := histPropertyCreateSource(t)
	destinationPath := histCreateMembership(t)

	// The unrelated destination feed covers every 5th address as single
	// point ranges and must survive every projection round (Rust
	// begin_create_feed before the rounds).
	destination, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedName, err := NewFeedName("unrelated")
	if err != nil {
		t.Fatal(err)
	}
	unrelatedRanges := make([]AddressRange4, 0, (workflowDomain+4)/5)
	for address := 0; address < workflowDomain; address += 5 {
		unrelatedRanges = append(unrelatedRanges, AddressRange4{From: IPv4(address), To: IPv4(address)})
	}
	create, err := destination.BeginCreateFeed(unrelatedName, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4(unrelatedRanges); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if finished.IsChanged() {
		result, err := finished.Commit()
		if err != nil || result.Status != CommitCommitted {
			t.Fatalf("unrelated feed commit = %+v err %v, want committed", result, err)
		}
	}
	if _, err := destination.Close(); err != nil {
		t.Fatal(err)
	}

	names := [historyPropertyWindows]string{"history-a", "history-b", "history-c", "history-d"}
	var before [historyPropertyWindows][workflowDomain]bool
	var random workflowRandom
	random.state = 0x0ef4ba8249817c61

	for round := 0; round < 40; round++ {
		inputCount := int(random.below(32))
		input := make([]DirectRangeV4, 0, inputCount)
		var sourceModel workflowModel
		for index := 0; index < inputCount; index++ {
			from, to := random.span()
			value := random.below(16)
			input = append(input, DirectRangeV4{From: uint32(from), To: uint32(to), Value: value})
			sourceModel.set(from, to, value)
		}

		// Replace the whole source database with the round input in
		// batches of 5 (Rust begin_direct_replacement + finish_workflow
		// + writer close so the live projection source binds the
		// committed generation).
		sourceWriter, err := OpenLiveWriter(sourcePath, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		replacement, err := sourceWriter.BeginDirectReplacement(NewCancellationToken())
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(input); start += 5 {
			end := start + 5
			if end > len(input) {
				end = len(input)
			}
			if err := replacement.AddRangesV4(input[start:end]); err != nil {
				t.Fatal(err)
			}
		}
		sourceFinished, err := replacement.FinishInput()
		if err != nil {
			t.Fatal(err)
		}
		if sourceFinished.IsChanged() {
			result, err := sourceFinished.Commit()
			if err != nil || result.Status != CommitCommitted {
				t.Fatalf("source commit = %+v err %v, want committed", result, err)
			}
		}
		if _, err := sourceWriter.Close(); err != nil {
			t.Fatal(err)
		}

		var cutoffs [historyPropertyWindows]uint32
		for window := 0; window < historyPropertyWindows; window++ {
			cutoffs[window] = random.below(18)
		}
		windows := make([]HistoryWindow, historyPropertyWindows)
		var after [historyPropertyWindows][workflowDomain]bool
		for window := 0; window < historyPropertyWindows; window++ {
			windows[window] = HistoryWindow{FeedName: names[window], Cutoff: cutoffs[window]}
			for address := 0; address < workflowDomain; address++ {
				value, present := sourceModel.value(address)
				after[window][address] = present && value > cutoffs[window]
			}
		}
		created := round == 0
		expected := LogicalNoChange
		if created || before != after {
			expected = LogicalChanged
		}

		// Project the committed source generation into the destination
		// (Rust project_history over the live source).
		source := histSource(t, sourcePath)
		destinationWriter, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := destinationWriter.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows, nil)
		if err != nil {
			t.Fatal(err)
		}
		report := handle.Report()
		if report.LogicalChange != expected {
			t.Fatalf("round %d: logical change = %v, want %v", round, report.LogicalChange, expected)
		}
		createdCount := uint64(0)
		if created {
			createdCount = uint64(historyPropertyWindows)
		}
		if report.CreatedFeedCount != createdCount {
			t.Fatalf("round %d: created feed count = %d, want %d", round, report.CreatedFeedCount, createdCount)
		}
		if report.SourceRangeCount != workflowValueRuns(&sourceModel) {
			t.Fatalf("round %d: source range count = %d, want %d", round, report.SourceRangeCount, workflowValueRuns(&sourceModel))
		}
		if histCount(t, report.SourceAddresses) != workflowCoverage(&sourceModel) {
			t.Fatalf("round %d: source addresses = %d, want %d", round, histCount(t, report.SourceAddresses), workflowCoverage(&sourceModel))
		}
		if len(report.Windows) != historyPropertyWindows {
			t.Fatalf("round %d: window reports = %d, want %d", round, len(report.Windows), historyPropertyWindows)
		}
		for window := 0; window < historyPropertyWindows; window++ {
			windowReport := report.Windows[window]
			if windowReport.FeedName != names[window] || windowReport.Cutoff != cutoffs[window] || windowReport.Created != created {
				t.Fatalf("round %d: window %d = %q cutoff %d created %v, want %q/%d/%v", round, window, windowReport.FeedName, windowReport.Cutoff, windowReport.Created, names[window], cutoffs[window], created)
			}
			if windowReport.BeforeIntervalCount != workflowBooleanRuns(&before[window]) {
				t.Fatalf("round %d: window %s before intervals = %d, want %d", round, names[window], windowReport.BeforeIntervalCount, workflowBooleanRuns(&before[window]))
			}
			if windowReport.AfterIntervalCount != workflowBooleanRuns(&after[window]) {
				t.Fatalf("round %d: window %s after intervals = %d, want %d", round, names[window], windowReport.AfterIntervalCount, workflowBooleanRuns(&after[window]))
			}
			if histCount(t, windowReport.BeforeAddresses) != workflowBooleanCount(&before[window]) {
				t.Fatalf("round %d: window %s before addresses = %d, want %d", round, names[window], histCount(t, windowReport.BeforeAddresses), workflowBooleanCount(&before[window]))
			}
			if histCount(t, windowReport.AfterAddresses) != workflowBooleanCount(&after[window]) {
				t.Fatalf("round %d: window %s after addresses = %d, want %d", round, names[window], histCount(t, windowReport.AfterAddresses), workflowBooleanCount(&after[window]))
			}
			unchanged, added, removed := histPropertyChanges(&before[window], &after[window])
			if histCount(t, windowReport.UnchangedAddresses) != unchanged {
				t.Fatalf("round %d: window %s unchanged = %d, want %d", round, names[window], histCount(t, windowReport.UnchangedAddresses), unchanged)
			}
			if histCount(t, windowReport.AddedAddresses) != added {
				t.Fatalf("round %d: window %s added = %d, want %d", round, names[window], histCount(t, windowReport.AddedAddresses), added)
			}
			if histCount(t, windowReport.RemovedAddresses) != removed {
				t.Fatalf("round %d: window %s removed = %d, want %d", round, names[window], histCount(t, windowReport.RemovedAddresses), removed)
			}
		}
		beforeUnion := histPropertyUnion(&before)
		afterUnion := histPropertyUnion(&after)
		if report.BeforeIntervalCount != workflowBooleanRuns(&beforeUnion) {
			t.Fatalf("round %d: before intervals = %d, want %d", round, report.BeforeIntervalCount, workflowBooleanRuns(&beforeUnion))
		}
		if report.AfterIntervalCount != workflowBooleanRuns(&afterUnion) {
			t.Fatalf("round %d: after intervals = %d, want %d", round, report.AfterIntervalCount, workflowBooleanRuns(&afterUnion))
		}
		if histCount(t, report.BeforeAddresses) != workflowBooleanCount(&beforeUnion) {
			t.Fatalf("round %d: before addresses = %d, want %d", round, histCount(t, report.BeforeAddresses), workflowBooleanCount(&beforeUnion))
		}
		if histCount(t, report.AfterAddresses) != workflowBooleanCount(&afterUnion) {
			t.Fatalf("round %d: after addresses = %d, want %d", round, histCount(t, report.AfterAddresses), workflowBooleanCount(&afterUnion))
		}
		unchanged, added, removed := histPropertyChanges(&beforeUnion, &afterUnion)
		if histCount(t, report.UnchangedAddresses) != unchanged {
			t.Fatalf("round %d: unchanged = %d, want %d", round, histCount(t, report.UnchangedAddresses), unchanged)
		}
		if histCount(t, report.AddedAddresses) != added {
			t.Fatalf("round %d: added = %d, want %d", round, histCount(t, report.AddedAddresses), added)
		}
		if histCount(t, report.RemovedAddresses) != removed {
			t.Fatalf("round %d: removed = %d, want %d", round, histCount(t, report.RemovedAddresses), removed)
		}

		// Commit the changed projection; a no-change result is already
		// clean. The destination writer must close before the live
		// reader opens (exclusive lifetime lock).
		if handle.IsChanged() {
			result, err := handle.Commit()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != CommitCommitted {
				t.Fatalf("round %d: projection commit status = %v (%v), want committed", round, result.Status, result.Err)
			}
		}
		if _, err := destinationWriter.Close(); err != nil {
			t.Fatal(err)
		}

		// The committed destination carries every window feed plus the
		// unrelated feed; the membership of every address must match the
		// scalar model for all four windows and the unrelated feed.
		reader, err := OpenLiveReader(destinationPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		var indexes [historyPropertyWindows]uint32
		for window := 0; window < historyPropertyWindows; window++ {
			entry, found, err := reader.LookupFeed(names[window])
			if err != nil || !found {
				t.Fatalf("round %d: feed %q found %v err %v", round, names[window], found, err)
			}
			indexes[window] = entry.Index
		}
		unrelatedEntry, found, err := reader.LookupFeed("unrelated")
		if err != nil || !found {
			t.Fatalf("round %d: unrelated feed found %v err %v", round, found, err)
		}
		unrelatedIndex := unrelatedEntry.Index
		pin, err := reader.Pin()
		if err != nil {
			t.Fatal(err)
		}
		for address := 0; address < workflowDomain; address++ {
			view, viewFound, err := pin.LookupMembershipV4(IPv4(address))
			if err != nil {
				t.Fatal(err)
			}
			for window := 0; window < historyPropertyWindows; window++ {
				actual := false
				if viewFound {
					actual, err = view.ContainsIndex(indexes[window])
					if err != nil {
						t.Fatal(err)
					}
				}
				if actual != after[window][address] {
					t.Fatalf("round %d: address %d window %d = %v, want %v", round, address, window, actual, after[window][address])
				}
			}
			actualUnrelated := false
			if viewFound {
				actualUnrelated, err = view.ContainsIndex(unrelatedIndex)
				if err != nil {
					t.Fatal(err)
				}
			}
			if actualUnrelated != (address%5 == 0) {
				t.Fatalf("round %d: address %d unrelated = %v, want %v", round, address, actualUnrelated, address%5 == 0)
			}
		}
		if err := pin.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Close(); err != nil {
			t.Fatal(err)
		}
		before = after
	}
}
