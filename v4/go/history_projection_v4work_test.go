//go:build v4work

// SOW-0025 chunk 3b-4 slice C necessary-work pins (Rust
// live_writer/history_projection/tests.rs parity): one projection over
// the 1000-range multi-window vector performs exactly one input source
// pass, three source passes (one per window), one consume and one emit
// per source range, one output pass, and 3000 per-window tests; an
// empty source with 64 unused window prefixes interns nothing. Any
// hot-path regression becomes visible in test builds; production builds
// compile the counters out.

package iprangedb

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestHistoryProjectionWorkCounters(t *testing.T) {
	requireLiveCreation(t)
	sourcePath := histCreateSource4(t, ranges1000())
	destinationPath := histCreateMembership(t)
	w, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	work.Reset()
	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows3(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 1 {
		t.Fatalf("input source passes = %d, want 1", s.InputSourcePasses)
	}
	if s.SourcePasses != 3 {
		t.Fatalf("source passes = %d, want 3", s.SourcePasses)
	}
	if s.RangesConsumed != 1000 {
		t.Fatalf("ranges consumed = %d, want 1000", s.RangesConsumed)
	}
	if s.RangesEmitted != 1000 {
		t.Fatalf("ranges emitted = %d, want 1000", s.RangesEmitted)
	}
	if s.OutputPasses != 1 {
		t.Fatalf("output passes = %d, want 1", s.OutputPasses)
	}
	if s.HistoryWindowTests != 3000 {
		t.Fatalf("history window tests = %d, want 3000", s.HistoryWindowTests)
	}
	if !handle.IsChanged() {
		t.Fatal("projection of new feeds is not changed")
	}
	if err := handle.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := w.coreOf().Healthy(); err != nil {
		t.Fatalf("writer unhealthy after abort: %v", err)
	}
}

func TestHistoryProjectionUnusedPrefixesNotInterned(t *testing.T) {
	requireLiveCreation(t)
	// An empty last_seen database: CreateLive alone leaves the empty
	// txn-1 generation (no transaction is ever committed).
	sourcePath := filepath.Join(t.TempDir(), "empty-source.iprdb")
	if _, err := CreateLive(sourcePath, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen(), 4, nil); err != nil {
		t.Fatal(err)
	}
	destinationPath := histCreateMembership(t)
	w, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := histSource(t, sourcePath)
	defer source.Close()

	windows := make([]HistoryWindow, 64)
	for index := range windows {
		windows[index] = HistoryWindow{FeedName: "history-" + string(rune('0'+index/10)) + string(rune('0'+index%10)), Cutoff: uint32(index)}
	}
	work.Reset()
	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceLive, Live: source}, windows, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.MembershipInterns != 0 {
		t.Fatalf("membership interns = %d, want 0", s.MembershipInterns)
	}
	if !handle.IsChanged() {
		t.Fatal("64 new feeds on an empty destination did not change the catalog")
	}
	if err := handle.Abort(); err != nil {
		t.Fatal(err)
	}
}
