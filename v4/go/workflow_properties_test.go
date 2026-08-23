// Randomized scalar-model property tests of the direct workflows (Rust
// tests/workflow_properties.rs parity): one deterministic xorshift
// generator drives 100 rounds of unordered replacement, first-seen
// refresh, and last-seen refresh inputs over a 128-address domain; the
// independent scalar model derives the exact expected report and
// database after every round. Go has no live-reader sidecar (Milestone
// 4), so the writer closes after each committed round before the
// immutable reader asserts the database, unlike the Rust test that
// keeps the live writer open across rounds.

package iprangedb

import (
	"path/filepath"
	"testing"
)

const workflowDomain = 128

// workflowModel is one per-address direct value state (Rust
// [Option<u32>; DOMAIN]).
type workflowModel [workflowDomain]uint32

// workflowModelSet marks every address of one range present with value.
func (m *workflowModel) set(from, to int, value uint32) {
	for index := from; index <= to; index++ {
		m[index] = value + 1 // zero means absent, so values are stored plus one
	}
}

func (m *workflowModel) value(index int) (uint32, bool) {
	v := m[index]
	if v == 0 {
		return 0, false
	}
	return v - 1, true
}

func (m *workflowModel) clear() {
	for index := range m {
		m[index] = 0
	}
}

// workflowValueRuns counts the canonical value runs (Rust runs).
func workflowValueRuns(m *workflowModel) uint64 {
	var runs uint64
	for index := 0; index < workflowDomain; index++ {
		value, present := m.value(index)
		if !present {
			continue
		}
		if index == 0 {
			runs++
			continue
		}
		previous, previousPresent := m.value(index - 1)
		if !previousPresent || previous != value {
			runs++
		}
	}
	return runs
}

func workflowCoverage(m *workflowModel) uint64 {
	var count uint64
	for index := range m {
		if m[index] != 0 {
			count++
		}
	}
	return count
}

// workflowCompare classifies one address transition (Rust compare).
type workflowCounts struct {
	unchanged, changed, added, removed uint64
}

func workflowCompare(before, after *workflowModel) workflowCounts {
	var counts workflowCounts
	for index := 0; index < workflowDomain; index++ {
		oldValue, oldPresent := before.value(index)
		newValue, newPresent := after.value(index)
		switch {
		case oldPresent && newPresent && oldValue == newValue:
			counts.unchanged++
		case oldPresent && newPresent:
			counts.changed++
		case !oldPresent && newPresent:
			counts.added++
		case oldPresent && !newPresent:
			counts.removed++
		}
	}
	return counts
}

// workflowBooleanRuns counts the runs of present addresses (Rust
// boolean_runs over the desired set).
func workflowBooleanRuns(desired *[workflowDomain]bool) uint64 {
	var runs uint64
	for index, present := range desired {
		if present && (index == 0 || !desired[index-1]) {
			runs++
		}
	}
	return runs
}

func workflowBooleanCount(desired *[workflowDomain]bool) uint64 {
	var count uint64
	for _, present := range desired {
		if present {
			count++
		}
	}
	return count
}

// directWorkflowDB creates one fresh empty direct IPv4 database.
func directWorkflowDB(t *testing.T, tag ValueTag) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow-property.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	return path
}

// workflowRandom is the Rust xorshift generator used by every property
// suite (Random::next in the Rust tests).
type workflowRandom struct {
	state uint64
}

func (r *workflowRandom) next() uint32 {
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	return uint32(r.state)
}

func (r *workflowRandom) below(limit uint32) uint32 { return r.next() % limit }

func (r *workflowRandom) span() (int, int) {
	left := int(r.below(workflowDomain))
	right := int(r.below(workflowDomain))
	if left < right {
		return left, right
	}
	return right, left
}

// assertWorkflowReport checks the exact replacement report against the
// scalar model (Rust assert_report).
func assertWorkflowReport(t *testing.T, report WorkflowReport, before, after *workflowModel, inputRecords uint64, inputIntervals, inputAddresses uint64, context string) {
	t.Helper()
	counts := workflowCompare(before, after)
	logical := LogicalNoChange
	if counts.changed != 0 || counts.added != 0 || counts.removed != 0 {
		logical = LogicalChanged
	}
	if report.LogicalChange != logical {
		t.Fatalf("%s: logical change = %v, want %v", context, report.LogicalChange, logical)
	}
	if report.InputRecordCount != inputRecords {
		t.Fatalf("%s: input record count = %d, want %d", context, report.InputRecordCount, inputRecords)
	}
	if report.InputNormalizedIntervalCount != inputIntervals {
		t.Fatalf("%s: input normalized intervals = %d, want %d", context, report.InputNormalizedIntervalCount, inputIntervals)
	}
	if report.BeforeRangeRecordCount != workflowValueRuns(before) {
		t.Fatalf("%s: before range records = %d, want %d", context, report.BeforeRangeRecordCount, workflowValueRuns(before))
	}
	if report.AfterRangeRecordCount != workflowValueRuns(after) {
		t.Fatalf("%s: after range records = %d, want %d", context, report.AfterRangeRecordCount, workflowValueRuns(after))
	}
	if report.InputAddresses.Lo() != inputAddresses {
		t.Fatalf("%s: input addresses = %d, want %d", context, report.InputAddresses.Lo(), inputAddresses)
	}
	if report.BeforeAddresses.Lo() != workflowCoverage(before) {
		t.Fatalf("%s: before addresses = %d, want %d", context, report.BeforeAddresses.Lo(), workflowCoverage(before))
	}
	if report.AfterAddresses.Lo() != workflowCoverage(after) {
		t.Fatalf("%s: after addresses = %d, want %d", context, report.AfterAddresses.Lo(), workflowCoverage(after))
	}
	if report.UnchangedValueAddresses.Lo() != counts.unchanged {
		t.Fatalf("%s: unchanged addresses = %d, want %d", context, report.UnchangedValueAddresses.Lo(), counts.unchanged)
	}
	if report.ChangedValueAddresses.Lo() != counts.changed {
		t.Fatalf("%s: changed addresses = %d, want %d", context, report.ChangedValueAddresses.Lo(), counts.changed)
	}
	if report.AddedAddresses.Lo() != counts.added {
		t.Fatalf("%s: added addresses = %d, want %d", context, report.AddedAddresses.Lo(), counts.added)
	}
	if report.RemovedAddresses.Lo() != counts.removed {
		t.Fatalf("%s: removed addresses = %d, want %d", context, report.RemovedAddresses.Lo(), counts.removed)
	}
}

// assertWorkflowDatabase checks every address through the immutable
// reader against the scalar model (Rust assert_database).
func assertWorkflowDatabase(t *testing.T, path string, expected *workflowModel, context string) {
	t.Helper()
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for address := 0; address < workflowDomain; address++ {
		value, found, err := r.LookupDirectV4(IPv4(address))
		if err != nil {
			t.Fatal(err)
		}
		wanted, wantPresent := expected.value(address)
		if found != wantPresent || (found && value != wanted) {
			t.Fatalf("%s: address %d = (%d, %v), want (%d, %v)", context, address, value, found, wanted, wantPresent)
		}
	}
}

// finishWorkflowCommit commits the changed terminal or drops the
// no-change terminal (Rust finish).
func finishWorkflowCommit(t *testing.T, finished *FinishedWorkflow, context string) {
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

// TestRandomizedDirectReplacementMatchesScalarStateAndReport mirrors
// Rust randomized_direct_replacement_matches_scalar_state_and_report:
// 100 rounds of unordered direct replacement.
func TestRandomizedDirectReplacementMatchesScalarStateAndReport(t *testing.T) {
	path := directWorkflowDB(t, mustTag(t, "direct"))
	var random workflowRandom
	random.state = 0x8bcf28d1930e44a7
	var before workflowModel
	for round := 0; round < 100; round++ {
		recordCount := 1 + int(random.below(24))
		var after workflowModel
		records := make([]DirectRangeV4, 0, recordCount)
		for index := 0; index < recordCount; index++ {
			from, to := random.span()
			value := random.below(6)
			records = append(records, DirectRangeV4{From: uint32(from), To: uint32(to), Value: value})
			after.set(from, to, value)
		}
		w, err := OpenWriter(path, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		cancellation := NewCancellationToken()
		replacement, err := w.BeginDirectReplacement(cancellation)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(records); start += 3 {
			end := start + 3
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
		context := "direct replacement round " + string(rune('0'+round))
		assertWorkflowReport(t, finished.Report(), &before, &after, uint64(recordCount), workflowValueRuns(&after), workflowCoverage(&after), context)
		finishWorkflowCommit(t, finished, context)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		assertWorkflowDatabase(t, path, &after, context)
		before = after
	}
}

// TestRandomizedFirstSeenRefreshMatchesFullDeltaSemantics mirrors Rust
// randomized_first_seen_refresh_matches_full_delta_semantics: 100
// rounds of first-seen refresh.
func TestRandomizedFirstSeenRefreshMatchesFullDeltaSemantics(t *testing.T) {
	path := directWorkflowDB(t, ValueTagFirstSeen())
	var random workflowRandom
	random.state = 0x57de8a11c442793b
	var before workflowModel
	for round := 1; round <= 100; round++ {
		recordCount := int(random.below(24))
		var desired [workflowDomain]bool
		records := make([]AddressRange4, 0, recordCount)
		for index := 0; index < recordCount; index++ {
			from, to := random.span()
			records = append(records, AddressRange4{From: IPv4(from), To: IPv4(to)})
			for address := from; address <= to; address++ {
				desired[address] = true
			}
		}
		var after workflowModel
		for index := 0; index < workflowDomain; index++ {
			if desired[index] {
				if _, present := before.value(index); present {
					after[index] = before[index]
				} else {
					after[index] = uint32(round) + 1
				}
			}
		}
		w, err := OpenWriter(path, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		cancellation := NewCancellationToken()
		refresh, err := w.BeginFirstSeenRefresh(uint32(round), cancellation)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(records); start += 4 {
			end := start + 4
			if end > len(records) {
				end = len(records)
			}
			if err := refresh.AddRangesV4(records[start:end]); err != nil {
				t.Fatal(err)
			}
		}
		finished, err := refresh.FinishInput()
		if err != nil {
			t.Fatal(err)
		}
		context := "first-seen round " + string(rune('0'+round))
		assertWorkflowReport(t, finished.Report(), &before, &after, uint64(recordCount), workflowBooleanRuns(&desired), workflowBooleanCount(&desired), context)
		finishWorkflowCommit(t, finished, context)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		assertWorkflowDatabase(t, path, &after, context)
		before = after
	}
}

// TestRandomizedLastSeenRefreshMatchesCutoffAndMonotonicTime mirrors
// Rust randomized_last_seen_refresh_matches_cutoff_and_monotonic_time:
// 100 rounds of last-seen refresh with periodic stale cutoffs.
func TestRandomizedLastSeenRefreshMatchesCutoffAndMonotonicTime(t *testing.T) {
	path := directWorkflowDB(t, ValueTagLastSeen())
	var random workflowRandom
	random.state = 0xd39ac6247b105e81
	var before workflowModel
	for round := 1; round <= 100; round++ {
		refresh := uint32(round)
		cutoff := uint32(0)
		if round%7 == 0 {
			refresh = uint32(round - 1)
		}
		if round >= 8 {
			cutoff = uint32(round - 8)
		}
		recordCount := int(random.below(24))
		var desired [workflowDomain]bool
		records := make([]AddressRange4, 0, recordCount)
		for index := 0; index < recordCount; index++ {
			from, to := random.span()
			records = append(records, AddressRange4{From: IPv4(from), To: IPv4(to)})
			for address := from; address <= to; address++ {
				desired[address] = true
			}
		}
		var after workflowModel
		for index := 0; index < workflowDomain; index++ {
			oldValue, oldPresent := before.value(index)
			switch {
			case desired[index]:
				if !oldPresent || oldValue < refresh {
					after[index] = refresh + 1
				} else {
					after[index] = oldValue + 1
				}
			case oldPresent && oldValue > cutoff:
				after[index] = oldValue + 1
			}
		}
		w, err := OpenWriter(path, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		cancellation := NewCancellationToken()
		refreshWorkflow, err := w.BeginLastSeenRefresh(refresh, cutoff, cancellation)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(records); start += 4 {
			end := start + 4
			if end > len(records) {
				end = len(records)
			}
			if err := refreshWorkflow.AddRangesV4(records[start:end]); err != nil {
				t.Fatalf("round %d (refresh=%d cutoff=%d): %v", round, refresh, cutoff, err)
			}
		}
		finished, err := refreshWorkflow.FinishInput()
		if err != nil {
			t.Fatalf("round %d (refresh=%d cutoff=%d): %v", round, refresh, cutoff, err)
		}
		context := "last-seen round " + string(rune('0'+round))
		if finished == nil {
			t.Fatalf("%s: finish returned nil (records=%d refresh=%d cutoff=%d)", context, recordCount, refresh, cutoff)
		}
		assertWorkflowReport(t, finished.Report(), &before, &after, uint64(recordCount), workflowBooleanRuns(&desired), workflowBooleanCount(&desired), context)
		finishWorkflowCommit(t, finished, context)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		assertWorkflowDatabase(t, path, &after, context)
		before = after
	}
}
