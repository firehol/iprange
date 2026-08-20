//go:build v4work

// Milestone 3 chunk 2 necessary-work pins (Rust work.rs parity): the
// one-scan aggregation and the two ordered joins must perform exactly one
// input-source pass per side, decode each recurring membership bitmap once
// per source (cache hits cover the repeats), and fold exactly one
// contribution per result record. These pins make any hot-path regression
// visible in test builds; production builds compile the counters out.

package iprangedb

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func memberScopes(t *testing.T) (*MembershipScope, *MembershipScope, *ImmutableReader) {
	t.Helper()
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	direct := mustOpen(t, "rust/direct-ipv4.iprdb")
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := q.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { direct.Close() })
	t.Cleanup(func() { db.Close() })
	return scope, right, direct
}

func TestAggregationWorkCounters(t *testing.T) {
	scope, _, _ := memberScopes(t)
	work.Reset()
	report, err := scope.Aggregate(MembershipAggregationAllPairs(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 1 || s.JoinAdvances != 0 {
		t.Fatalf("passes=%d joinAdv=%d, want 1/0", s.InputSourcePasses, s.JoinAdvances)
	}
	if s.MembershipDecodes != 3 || s.MembershipDecodeCacheHits != 0 {
		t.Fatalf("decodes=%d hits=%d, want 3/0", s.MembershipDecodes, s.MembershipDecodeCacheHits)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != report.ScannedRangeCount {
		t.Fatalf("decodes+hits = %d, want scanned ranges %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, report.ScannedRangeCount)
	}
	if s.MembershipWordReads != 6 {
		t.Fatalf("word reads = %d, want 6 (3 bitmaps x 2 words)", s.MembershipWordReads)
	}
	if s.AggregationContributions != 46 {
		t.Fatalf("contributions = %d, want 46 (14 feed totals + 32 pair folds)", s.AggregationContributions)
	}
	if s.AggregationResults != report.FeedResultCount+report.PairResultCount {
		t.Fatalf("results = %d, want %d", s.AggregationResults, report.FeedResultCount+report.PairResultCount)
	}
	if s.AggregationResults != 2485 {
		t.Fatalf("results = %d, want 2485 (70 feeds + 2415 pairs)", s.AggregationResults)
	}
}

func TestJoinDirectWorkCounters(t *testing.T) {
	scope, _, direct := memberScopes(t)
	work.Reset()
	report, err := scope.JoinDirect(direct, DirectJoinBudget{MaxResultCells: 4096}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 2 {
		t.Fatalf("passes = %d, want 2 (membership + direct)", s.InputSourcePasses)
	}
	if s.MembershipDecodes != 3 || s.MembershipDecodeCacheHits != 0 {
		t.Fatalf("decodes=%d hits=%d, want 3/0", s.MembershipDecodes, s.MembershipDecodeCacheHits)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != report.MembershipRangeCount {
		t.Fatalf("decodes+hits = %d, want membership ranges %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, report.MembershipRangeCount)
	}
	if s.MembershipWordReads != 6 {
		t.Fatalf("word reads = %d, want 6", s.MembershipWordReads)
	}
	if s.JoinAdvances != 9 {
		t.Fatalf("join advances = %d, want 9 sweep segments", s.JoinAdvances)
	}
	if s.AggregationResults != report.ResultCellCount {
		t.Fatalf("results = %d, want result cells %d", s.AggregationResults, report.ResultCellCount)
	}
	if s.AggregationResults != 22 || s.AggregationContributions != 44 {
		t.Fatalf("results=%d contributions=%d, want 22/44", s.AggregationResults, s.AggregationContributions)
	}
}

func TestJoinMembershipWorkCounters(t *testing.T) {
	scope, right, _ := memberScopes(t)
	work.Reset()
	report, err := scope.JoinMembership(right, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := work.Read()
	if s.InputSourcePasses != 2 {
		t.Fatalf("passes = %d, want 2 (left + right)", s.InputSourcePasses)
	}
	if s.MembershipDecodes != 6 || s.MembershipDecodeCacheHits != 0 {
		t.Fatalf("decodes=%d hits=%d, want 6/0", s.MembershipDecodes, s.MembershipDecodeCacheHits)
	}
	if s.MembershipDecodes+s.MembershipDecodeCacheHits != report.LeftRangeCount+report.RightRangeCount {
		t.Fatalf("decodes+hits = %d, want left+right ranges %d", s.MembershipDecodes+s.MembershipDecodeCacheHits, report.LeftRangeCount+report.RightRangeCount)
	}
	if s.MembershipWordReads != 12 {
		t.Fatalf("word reads = %d, want 12", s.MembershipWordReads)
	}
	if s.JoinAdvances != 3 {
		t.Fatalf("join advances = %d, want 3 sweep segments", s.JoinAdvances)
	}
	if s.AggregationResults != report.CrossResultCount+report.UncoveredResultCount {
		t.Fatalf("results = %d, want %d", s.AggregationResults, report.CrossResultCount+report.UncoveredResultCount)
	}
	if s.AggregationResults != 141 {
		t.Fatalf("results = %d, want 141 (70 cross + 71 uncovered)", s.AggregationResults)
	}
}
