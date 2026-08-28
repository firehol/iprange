package iprangedb

import (
	"testing"
)

// TestStreamingFacadesDoNotAllocatePerDeliveredRecord pins the
// batch-lifetime contract of the public streaming facades (SOW-0027
// milestone-4 external review): delivery must not allocate one heap
// object per delivered record. The complete-page gate test measures
// with nil sinks, which never exercise the facade conversions; this
// test measures the aggregate with real sinks and compares against the
// nil-sink baseline. The aggregate scope delivers thousands of records
// per run, so the per-record class dominates the measured delta by an
// order of magnitude; the join facades are pinned at unit scale
// (internal/reader join-direct emit test) because their conformance
// fixture output is too small for an alloc-count gate.
func TestStreamingFacadesDoNotAllocatePerDeliveredRecord(t *testing.T) {
	member := openPublic(t, "membership-ipv4.iprdb")
	defer member.Close()
	q, err := member.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}

	var records, names int
	seen := map[string]bool{}
	warmRecords := func(batch []FeedCardinality) error {
		records += len(batch)
		for i := range batch {
			seen[batch[i].Feed] = true
		}
		return nil
	}
	warmPairs := func(batch []FeedOverlap) error {
		records += len(batch)
		for i := range batch {
			seen[batch[i].Left] = true
			seen[batch[i].Right] = true
		}
		return nil
	}
	for range 8 {
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(), warmRecords, warmPairs, nil); err != nil {
			t.Fatal("warm aggregate:", err)
		}
	}
	records /= 8
	names = len(seen)

	// The sinks must consume the delivered strings: a sink that ignores
	// its batch lets the compiler eliminate the conversions, hiding the
	// per-record allocation class (Go escape analysis of dead stores).
	var consumed int
	countFeed := func(batch []FeedCardinality) error {
		for i := range batch {
			consumed += len(batch[i].Feed)
		}
		return nil
	}
	countOverlap := func(batch []FeedOverlap) error {
		for i := range batch {
			consumed += len(batch[i].Left) + len(batch[i].Right)
		}
		return nil
	}
	baseline := testing.AllocsPerRun(24, func() {
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(), nil, nil, nil); err != nil {
			t.Fatal("aggregate:", err)
		}
	})
	consumed = 0
	got := testing.AllocsPerRun(24, func() {
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(), countFeed, countOverlap, nil); err != nil {
			t.Fatal("aggregate:", err)
		}
	})
	if consumed == 0 {
		t.Fatal("sink consumed no data: the measurement window is dead")
	}
	// Allowed: one owned string per distinct delivered name (names), the
	// per-batch output slices and the sink-side batch machinery, plus a
	// deterministic-measurement floor of 96 objects. A per-record
	// allocation (or a toolchain escape regression of the short-lived
	// conversions) adds one object per delivered record, which exceeds
	// the bound by an order of magnitude.
	bound := float64(names) + 96
	if got-baseline > bound {
		t.Fatalf("aggregate: facade allocated %.1f allocs/run over the nil-sink baseline for %d records / %d names (bound %.1f): per-record allocation", got-baseline, records, names, bound)
	}
}
