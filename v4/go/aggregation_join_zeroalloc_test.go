package iprangedb

import (
	"runtime"
	"testing"
)

// TestNoPageSizedHeapAllocationsJoins is the chunk-2 half of the
// complete-page ownership evidence: the aggregation and join operations
// must not allocate any Go heap object of 4096 bytes or larger while
// walking pre-built scopes. Complete-page copies would allocate exactly
// such objects. Scopes are built and warmed outside the measured window;
// all per-operation buffers (32-record batches, decode scratch, modeled
// tables) stay under the page-size classes.
func TestNoPageSizedHeapAllocationsJoins(t *testing.T) {
	member := openPublic(t, "membership-ipv4.iprdb")
	defer member.Close()
	direct := openPublic(t, "direct-ipv4.iprdb")
	defer direct.Close()
	q, err := member.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}
	right, err := q.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("right scope:", err)
	}

	// Warm every path before the measured window.
	for range 16 {
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(), nil, nil, nil); err != nil {
			t.Fatal("warm aggregate:", err)
		}
		if _, err := scope.JoinDirect(direct, DirectJoinBudget{MaxResultCells: 4096}, nil, nil); err != nil {
			t.Fatal("warm direct join:", err)
		}
		if _, err := scope.JoinMembership(right, nil, nil, nil); err != nil {
			t.Fatal("warm membership join:", err)
		}
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for range 256 {
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(), nil, nil, nil); err != nil {
			t.Fatal("aggregate:", err)
		}
		if _, err := scope.JoinDirect(direct, DirectJoinBudget{MaxResultCells: 4096}, nil, nil); err != nil {
			t.Fatal("direct join:", err)
		}
		if _, err := scope.JoinMembership(right, nil, nil, nil); err != nil {
			t.Fatal("membership join:", err)
		}
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	beforeBySize := map[uint32]uint64{}
	for _, c := range before.BySize {
		beforeBySize[c.Size] = c.Mallocs
	}
	for _, c := range after.BySize {
		if c.Size < 4096 {
			continue
		}
		if c.Mallocs > beforeBySize[c.Size] {
			t.Fatalf("heap allocation of %d bytes during aggregation/joins (mallocs %d -> %d): a complete mapped page was copied into owned memory", c.Size, beforeBySize[c.Size], c.Mallocs)
		}
	}
}
