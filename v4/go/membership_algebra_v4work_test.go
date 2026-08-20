//go:build v4work

// Milestone 3 chunk 3a necessary-work pins (Rust work.rs parity): the
// algebra sweep performs exactly one input-source pass per source, one
// Start/End event per selected range per source (the fixture's third
// range ends at the family maximum, so it emits no End event), and one
// bitmap decode per physical range per source. Any hot-path regression
// becomes visible in test builds; production builds compile the
// counters out.

package iprangedb

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestAlgebraWorkCounters(t *testing.T) {
	alg, closeFn := algebraV4(t)
	defer closeFn()

	// All feeds, two identical sources: each of the three fixture rows
	// produces one Start and one End event per source (the fixture rows
	// do not touch the family maximum), so the sweep processes 12
	// events in total.
	work.Reset()
	report, err := alg.Count(AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 2 {
		t.Fatalf("input source passes = %d, want 2", s.InputSourcePasses)
	}
	if s.JoinAdvances != 12 {
		t.Fatalf("join advances = %d, want 12 (6 events per source)", s.JoinAdvances)
	}
	if s.MembershipDecodes != 6 || s.MembershipDecodeCacheHits != 0 {
		t.Fatalf("decodes = %d hits = %d, want 6/0", s.MembershipDecodes, s.MembershipDecodeCacheHits)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != report.SourceRangeCount {
		t.Fatalf("decodes+hits = %d, want source range count %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, report.SourceRangeCount)
	}
	if report.SourceRangeCount != 6 || report.JoinedSegmentCount != 3 {
		t.Fatalf("report = %+v", report)
	}

	work.Reset()
	cmp, err := alg.Compare(AlgebraFeedSelectionAll(), AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s = work.Read()
	if s.InputSourcePasses != 2 || s.JoinAdvances != 12 {
		t.Fatalf("compare passes=%d join=%d, want 2/12", s.InputSourcePasses, s.JoinAdvances)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != cmp.SourceRangeCount {
		t.Fatalf("compare decodes+hits = %d, want %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, cmp.SourceRangeCount)
	}
}

func TestAlgebraNamedWorkCounters(t *testing.T) {
	// One named scope per source: feed-001 selects the middle and last
	// fixture rows, which are adjacent and carry identical selected sets
	// for the one-feed scope, so the selected-range merger folds them
	// into one run per source: 2 events per source (start + end).
	dbA := mustOpen(t, "rust/membership-ipv4.iprdb")
	dbB := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer dbA.Close()
	defer dbB.Close()
	qa, err := dbA.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	qb, err := dbB.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scopeA, err := qa.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scopeB, err := qb.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	alg, err := NewMembershipAlgebra([]*MembershipScope{scopeA, scopeB}, MembershipAlgebraBudget{
		MaxHeapBytes: 1 << 20,
		MaxSources:   8,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	work.Reset()
	report, err := alg.Count(AlgebraFeedSelectionNamed([]string{"feed-001"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 2 {
		t.Fatalf("input source passes = %d, want 2", s.InputSourcePasses)
	}
	if s.JoinAdvances != 4 {
		t.Fatalf("join advances = %d, want 4 (2 merged events per source)", s.JoinAdvances)
	}
	// The first fixture row is read and discarded (it does not select
	// feed-001), so each source still decodes all three physical ranges.
	if s.MembershipDecodes != 6 || s.MembershipDecodeCacheHits != 0 {
		t.Fatalf("decodes = %d hits = %d, want 6/0", s.MembershipDecodes, s.MembershipDecodeCacheHits)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != report.SourceRangeCount {
		t.Fatalf("decodes+hits = %d, want source range count %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, report.SourceRangeCount)
	}
	if report.SourceRangeCount != 6 {
		t.Fatalf("source range count = %d, want 6", report.SourceRangeCount)
	}
	// feed-001 covers 10.0.1.0 - 10.0.1.255 (256 addresses).
	if report.Addresses.String() != "256" {
		t.Fatalf("named union = %s, want 256", report.Addresses.String())
	}
}
