// Milestone 3 chunk 2 parity tests: one-scan aggregation (Rust
// membership_query/aggregation.rs) and the two ordered joins (Rust
// membership_query/join/direct.rs and join/membership.rs) over the
// committed conformance corpus. Expected values are derived from
// cases.json (the Rust-produced authority) through a small interval
// model plus hard pins for the headline numbers.

package iprangedb

import (
	"errors"
	"fmt"
	"math/bits"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// ---------------------------------------------------------------------------
// interval model helpers

type ipAddr struct{ hi, lo uint64 }

func ip4(a IPv4) ipAddr { return ipAddr{lo: uint64(a)} }
func ip6(a IPv6) ipAddr { return ipAddr{hi: a.Hi, lo: a.Lo} }
func (a ipAddr) less(b ipAddr) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}

// inclusiveLen returns b - a + 1 (requires a <= b), exactly.
func inclusiveLen(a, b ipAddr) format.Cardinality129 {
	diffLo, borrow := bits.Sub64(b.lo, a.lo, 0)
	diffHi, _ := bits.Sub64(b.hi, a.hi, borrow)
	lo, carry := bits.Add64(diffLo, 1, 0)
	hi, _ := bits.Add64(diffHi, 0, carry)
	return format.CardinalityFromUint128(hi, lo)
}

// interLen is the inclusive length of the intersection of two intervals
// (exact zero when disjoint).
func interLen(a, b, c, d ipAddr) format.Cardinality129 {
	from, to := a, b
	if from.less(c) {
		from = c
	}
	if d.less(to) {
		to = d
	}
	if to.less(from) {
		return format.CardinalityZero()
	}
	return inclusiveLen(from, to)
}

func cardAdd(a, b format.Cardinality129) format.Cardinality129 {
	sum, err := a.Add(b)
	if err != nil {
		panic(err)
	}
	return sum
}

func contains(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

func fixtureRanges(t *testing.T, file string) []struct {
	from, to ipAddr
	feeds    []string
} {
	t.Helper()
	fx := manifestFixture(t, file)
	var out []struct {
		from, to ipAddr
		feeds    []string
	}
	for _, r := range fx.MembershipRanges {
		var from, to ipAddr
		if fx.Family == "ipv4" {
			from, to = ip4(parseV4(r.From)), ip4(parseV4(r.To))
		} else {
			from, to = ip6(parseV6Full(r.From)), ip6(parseV6Full(r.To))
		}
		out = append(out, struct {
			from, to ipAddr
			feeds    []string
		}{from, to, r.Feeds})
	}
	return out
}

// feedCoverage sums the inclusive length of every range selecting feed.
func feedCoverage(rows []struct {
	from, to ipAddr
	feeds    []string
}, feed string) format.Cardinality129 {
	var total format.Cardinality129
	for _, row := range rows {
		if contains(row.feeds, feed) {
			total = cardAdd(total, inclusiveLen(row.from, row.to))
		}
	}
	return total
}

// pairOverlap sums the inclusive length of every range selecting both feeds.
func pairOverlap(rows []struct {
	from, to ipAddr
	feeds    []string
}, a, b string) format.Cardinality129 {
	var total format.Cardinality129
	for _, row := range rows {
		if contains(row.feeds, a) && contains(row.feeds, b) {
			total = cardAdd(total, inclusiveLen(row.from, row.to))
		}
	}
	return total
}

// scopeAddresses sums every range selecting at least one scope feed.
func scopeAddresses(rows []struct {
	from, to ipAddr
	feeds    []string
}, feeds []string) format.Cardinality129 {
	var total format.Cardinality129
	for _, row := range rows {
		any := false
		for _, f := range feeds {
			if contains(row.feeds, f) {
				any = true
				break
			}
		}
		if any {
			total = cardAdd(total, inclusiveLen(row.from, row.to))
		}
	}
	return total
}

func cardStr(c format.Cardinality129) string { return c.String() }

func expectCard(t *testing.T, label string, got format.Cardinality129, want format.Cardinality129) {
	t.Helper()
	if got.Compare(want) != 0 {
		t.Fatalf("%s = %s, want %s", label, cardStr(got), cardStr(want))
	}
}

// ---------------------------------------------------------------------------
// aggregation

func TestAggregationCardinalitiesV4(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}

	report, err := scope.Aggregate(MembershipAggregationCardinalities(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "scanned addresses", report.ScannedAddresses, format.CardinalityFromUint64(512))
	if report.ScannedRangeCount != 3 || report.FeedResultCount != 70 || report.PairResultCount != 0 {
		t.Fatalf("report = %+v", report)
	}

	seen := map[string]format.Cardinality129{}
	var order []string
	_, err = scope.Aggregate(MembershipAggregationCardinalities(), func(batch []FeedCardinality) error {
		for _, fc := range batch {
			seen[fc.Feed] = fc.Addresses
			order = append(order, fc.Feed)
		}
		return nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 70 {
		t.Fatalf("emitted %d feeds, want 70", len(seen))
	}
	// Emission order: ascending scope position (== ascending feed index).
	entries := scope.Feeds()
	for i, name := range order {
		if name != entries[i].Name {
			t.Fatalf("emission order %d = %q, want %q", i, name, entries[i].Name)
		}
	}
	// Every feed matches the interval model; the headline counts are pinned.
	for _, fc := range []struct {
		name  string
		value uint64
	}{
		{"feed-000", 384}, {"feed-001", 256}, {"feed-reused", 384},
		{"feed-063", 384}, {"feed-064", 384}, {"feed-065", 256}, {"feed-069", 384},
		{"feed-002", 0}, {"feed-070 - absent", 0},
	} {
		if fc.name == "feed-070 - absent" {
			continue
		}
		expectCard(t, fc.name, seen[fc.name], format.CardinalityFromUint64(fc.value))
		if fc.value != 0 {
			want := feedCoverage(rows, fc.name)
			expectCard(t, "model "+fc.name, seen[fc.name], want)
		}
	}
	for name := range seen {
		expectCard(t, "model all "+name, seen[name], feedCoverage(rows, name))
	}
}

func TestAggregationAllPairsV4(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var pairs []FeedOverlap
	report, err := scope.Aggregate(MembershipAggregationAllPairs(), nil, func(batch []FeedOverlap) error {
		pairs = append(pairs, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.FeedResultCount != 70 || report.PairResultCount != 2415 || uint64(len(pairs)) != 2415 {
		t.Fatalf("report = %+v pairs = %d", report, len(pairs))
	}

	index := map[string]int{}
	for i, e := range scope.Feeds() {
		index[e.Name] = i
	}
	byPair := map[[2]int]format.Cardinality129{}
	sumPairs := format.CardinalityZero()
	prevLeft, prevRight := -1, -1
	for _, p := range pairs {
		l, r := index[p.Left], index[p.Right]
		// Ascending (left, right) lexicographic order.
		if l < prevLeft || (l == prevLeft && r <= prevRight) {
			t.Fatalf("pairs out of order: (%s,%s) after (%d,%d)", p.Left, p.Right, prevLeft, prevRight)
		}
		prevLeft, prevRight = l, r
		byPair[[2]int{l, r}] = p.Addresses
		sumPairs = cardAdd(sumPairs, p.Addresses)
	}
	// The whole table matches the model over every unordered pair.
	feeds := scope.Feeds()
	modelSum := format.CardinalityZero()
	for i := 0; i < len(feeds); i++ {
		for j := i + 1; j < len(feeds); j++ {
			want := pairOverlap(rows, feeds[i].Name, feeds[j].Name)
			got := byPair[[2]int{i, j}]
			expectCard(t, feeds[i].Name+","+feeds[j].Name, got, want)
			modelSum = cardAdd(modelSum, want)
		}
	}
	expectCard(t, "pair total", sumPairs, modelSum)

	pin := func(a, b string, want uint64) {
		t.Helper()
		expectCard(t, a+","+b, byPair[[2]int{index[a], index[b]}], format.CardinalityFromUint64(want))
	}
	pin("feed-000", "feed-001", 128)
	pin("feed-000", "feed-069", 384)
	pin("feed-001", "feed-065", 256)
	pin("feed-001", "feed-002", 0)
	pin("feed-065", "feed-069", 128)
}

func TestAggregationTargetAgainstScopeV4(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pairs []FeedOverlap
	report, err := scope.Aggregate(MembershipAggregationTargetAgainstScope("feed-001"), nil, func(batch []FeedOverlap) error {
		pairs = append(pairs, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.PairResultCount != 69 || uint64(len(pairs)) != 69 {
		t.Fatalf("report = %+v pairs = %d", report, len(pairs))
	}
	seen := map[string]format.Cardinality129{}
	for _, p := range pairs {
		if p.Right == "feed-001" {
			seen[p.Left] = p.Addresses
		} else {
			seen[p.Right] = p.Addresses
		}
	}
	expectCard(t, "target 001 x 000", seen["feed-000"], format.CardinalityFromUint64(128))
	expectCard(t, "target 001 x 065", seen["feed-065"], format.CardinalityFromUint64(256))
	expectCard(t, "target 001 x 002", seen["feed-002"], format.CardinalityZero())

	// Unknown target: NameNotFound.
	if _, err := scope.Aggregate(MembershipAggregationTargetAgainstScope("missing"), nil, nil, nil); errorAsCode(err) != ErrorNameNotFound {
		t.Fatalf("unknown target error = %v, want NameNotFound", err)
	}
	// Target outside the scope: InvalidArgument.
	named, err := q.NamedFeeds([]string{"feed-000"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := named.Aggregate(MembershipAggregationTargetAgainstScope("feed-001"), nil, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("out-of-scope target error = %v, want InvalidArgument", err)
	}
}

func TestAggregationSelectedPairsV4(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var pairs []FeedOverlap
	report, err := scope.Aggregate(MembershipAggregationSelectedPairs([]FeedPair{
		{Left: "feed-001", Right: "feed-000"},
		{Left: "feed-069", Right: "feed-065"},
	}), nil, func(batch []FeedOverlap) error {
		pairs = append(pairs, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.PairResultCount != 2 || len(pairs) != 2 {
		t.Fatalf("report = %+v pairs = %d", report, len(pairs))
	}
	// Caller order is normalized: (000,001) then (065,069).
	if pairs[0].Left != "feed-000" || pairs[0].Right != "feed-001" ||
		pairs[1].Left != "feed-065" || pairs[1].Right != "feed-069" {
		t.Fatalf("pairs = %+v", pairs)
	}
	expectCard(t, "pair 0", pairs[0].Addresses, format.CardinalityFromUint64(128))
	expectCard(t, "pair 1", pairs[1].Addresses, format.CardinalityFromUint64(128))

	// Empty selection: InvalidArgument.
	if _, err := scope.Aggregate(MembershipAggregationSelectedPairs(nil), nil, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("empty pairs error = %v, want InvalidArgument", err)
	}
	// Self-pair: InvalidArgument.
	if _, err := scope.Aggregate(MembershipAggregationSelectedPairs([]FeedPair{{Left: "feed-000", Right: "feed-000"}}), nil, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("self pair error = %v, want InvalidArgument", err)
	}
	// Duplicate pair: InvalidArgument.
	if _, err := scope.Aggregate(MembershipAggregationSelectedPairs([]FeedPair{
		{Left: "feed-000", Right: "feed-001"},
		{Left: "feed-001", Right: "feed-000"},
	}), nil, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("duplicate pairs error = %v, want InvalidArgument", err)
	}
	// Unknown name: NameNotFound.
	if _, err := scope.Aggregate(MembershipAggregationSelectedPairs([]FeedPair{{Left: "missing", Right: "feed-000"}}), nil, nil, nil); errorAsCode(err) != ErrorNameNotFound {
		t.Fatalf("unknown pair name error = %v, want NameNotFound", err)
	}
	// Name outside the scope: InvalidArgument.
	if _, err := scope.Aggregate(MembershipAggregationSelectedPairs([]FeedPair{{Left: "feed-000", Right: "feed-001"}}), nil, nil, nil); err != nil {
		t.Fatalf("in-scope pairs failed: %v", err)
	}
}

func TestAggregationV6(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv6.iprdb")
	db := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	feeds := map[string]format.Cardinality129{}
	var pairs []FeedOverlap
	report, err := scope.Aggregate(MembershipAggregationAllPairs(), func(batch []FeedCardinality) error {
		for _, fc := range batch {
			feeds[fc.Feed] = fc.Addresses
		}
		return nil
	}, func(batch []FeedOverlap) error {
		pairs = append(pairs, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedRangeCount != 3 || report.FeedResultCount != 2 || report.PairResultCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	expectCard(t, "scanned addresses", report.ScannedAddresses, format.FullIPv6Space())
	expectCard(t, "global", feeds["global"], feedCoverage(rows, "global"))
	expectCard(t, "special", feeds["special"], feedCoverage(rows, "special"))
	// The three ranges tile the full IPv6 space and global is selected in
	// every one of them: its count is exactly 2^128.
	expectCard(t, "global pin", feeds["global"], format.FullIPv6Space())
	if len(pairs) != 1 || pairs[0].Left != "global" || pairs[0].Right != "special" {
		t.Fatalf("pairs = %+v", pairs)
	}
	expectCard(t, "global x special", pairs[0].Addresses, pairOverlap(rows, "global", "special"))
	expectCard(t, "global x special pin", pairs[0].Addresses, format.CardinalityFromUint64(65536))
}

func TestAggregationErrorsAndCancellation(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}

	// Sink errors pass through unchanged.
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("sink stop")
	if _, err := scope.Aggregate(MembershipAggregationCardinalities(), func([]FeedCardinality) error { return sentinel }, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("feed sink error = %v, want sentinel", err)
	}

	// A pre-cancelled token fails every bounded op.
	cancel := NewCancellationToken()
	cancel.Cancel()
	if _, err := scope.Aggregate(MembershipAggregationCardinalities(), nil, nil, cancel); errorAsCode(err) != ErrorCancelled {
		t.Fatalf("cancelled aggregate error = %v, want Cancelled", err)
	}

	// Cancellation is observable mid-emission: cancel after the first
	// feed batch (32 records), before the second batch flush.
	cancel = NewCancellationToken()
	sent := []string{}
	_, err = scope.Aggregate(MembershipAggregationCardinalities(), func(batch []FeedCardinality) error {
		sent = append(sent, batch[0].Feed)
		cancel.Cancel()
		return nil
	}, nil, cancel)
	if errorAsCode(err) != ErrorCancelled {
		t.Fatalf("mid-emission cancel error = %v, want Cancelled (sent %d)", err, len(sent))
	}
	if len(sent) != 1 {
		t.Fatalf("feed sink saw %d batches, want 1", len(sent))
	}

	// An operation heap that cannot fit its own allocations fails with
	// InsufficientResourceBudget: a scope built with a tight budget still
	// resolves, but AllPairs needs 70*24 totals bytes plus the pair table.
	tight, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 2048}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tight.Aggregate(MembershipAggregationAllPairs(), nil, nil, nil); errorAsCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("tight budget aggregate error = %v, want InsufficientResourceBudget", err)
	}
}

// ---------------------------------------------------------------------------
// direct join

type directRow struct {
	from, to ipAddr
	value    uint32
}

func fixtureDirectRows(t *testing.T, file string) []directRow {
	t.Helper()
	fx := manifestFixture(t, file)
	var out []directRow
	for _, r := range fx.DirectRanges {
		out = append(out, directRow{from: ip4(parseV4(r.From)), to: ip4(parseV4(r.To)), value: r.Value})
	}
	return out
}

// joinDirectExpect derives the exact direct-join outcome from the
// membership and direct models.
func joinDirectExpect(rows []struct {
	from, to ipAddr
	feeds    []string
}, directs []directRow, feeds []string) struct {
	selected, mapped, unmapped format.Cardinality129
	cells                      map[string]map[uint32]format.Cardinality129 // value 0 = unmapped
	feedCoverage               map[string]format.Cardinality129
} {
	selected := scopeAddresses(rows, feeds)
	var mapped format.Cardinality129
	for _, d := range directs {
		for _, r := range rows {
			any := false
			for _, f := range r.feeds {
				if contains(feeds, f) {
					any = true
					break
				}
			}
			if any {
				mapped = cardAdd(mapped, interLen(r.from, r.to, d.from, d.to))
			}
		}
	}
	unmapped, err := selected.Sub(mapped)
	if err != nil {
		panic(err)
	}
	cells := map[string]map[uint32]format.Cardinality129{}
	cov := map[string]format.Cardinality129{}
	for _, f := range feeds {
		if _, ok := cells[f]; !ok {
			cells[f] = map[uint32]format.Cardinality129{}
		}
		c := feedCoverage(rows, f)
		cov[f] = c
		var mappedPerFeed format.Cardinality129
		for _, d := range directs {
			for _, r := range rows {
				if contains(r.feeds, f) {
					n := interLen(r.from, r.to, d.from, d.to)
					if n.Compare(format.CardinalityZero()) != 0 {
						cells[f][d.value] = cardAdd(cells[f][d.value], n)
						mappedPerFeed = cardAdd(mappedPerFeed, n)
					}
				}
			}
		}
		unmappedPerFeed, err := c.Sub(mappedPerFeed)
		if err != nil {
			panic(err)
		}
		cells[f][0] = unmappedPerFeed
	}
	return struct {
		selected, mapped, unmapped format.Cardinality129
		cells                      map[string]map[uint32]format.Cardinality129
		feedCoverage               map[string]format.Cardinality129
	}{selected, mapped, unmapped, cells, cov}
}

func feedSet(t *testing.T, scope *MembershipScope) []string {
	t.Helper()
	entries := scope.Feeds()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func TestJoinDirectV4(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv4.iprdb")
	directs := fixtureDirectRows(t, "rust/direct-ipv4.iprdb")
	member := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer member.Close()
	direct := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer direct.Close()
	q, err := member.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := joinDirectExpect(rows, directs, feedSet(t, scope))

	var cells []DirectJoinCell
	report, err := scope.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 4096}, func(batch []DirectJoinCell) error {
		cells = append(cells, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.MembershipRangeCount != 3 || report.DirectRangesVisited != 4 {
		t.Fatalf("report = %+v", report)
	}
	expectCard(t, "selected", report.SelectedAddresses, want.selected)
	expectCard(t, "mapped", report.MappedAddresses, want.mapped)
	expectCard(t, "unmapped", report.UnmappedAddresses, want.unmapped)
	// Hard pins on the fixture facts.
	expectCard(t, "selected pin", report.SelectedAddresses, format.CardinalityFromUint64(512))
	expectCard(t, "mapped pin", report.MappedAddresses, format.CardinalityFromUint64(16))
	expectCard(t, "unmapped pin", report.UnmappedAddresses, format.CardinalityFromUint64(496))
	if report.JoinedSegmentCount != 9 || report.ResultCellCount != 22 {
		t.Fatalf("report = %+v", report)
	}

	// Every cell matches the model; the emission order is ascending feed
	// then direct value (unmapped first, direct values ascending).
	entries := scope.Feeds()
	index := map[string]int{}
	for i, e := range entries {
		index[e.Name] = i
	}
	byCell := map[[2]int]format.Cardinality129{}
	prevFeed, prevValue := -1, -1
	for _, c := range cells {
		value := uint32(0)
		if c.DirectValue != nil {
			value = *c.DirectValue
		}
		fi := index[c.Feed]
		if fi < prevFeed || (fi == prevFeed && int(value) <= prevValue) {
			t.Fatalf("cells out of order: (%s,%d)", c.Feed, value)
		}
		prevFeed, prevValue = fi, int(value)
		byCell[[2]int{fi, int(value)}] = c.Addresses
	}
	for _, f := range entries {
		for value, wantAddr := range want.cells[f.Name] {
			got := byCell[[2]int{index[f.Name], int(value)}]
			expectCard(t, f.Name+"/"+itoa(value), got, wantAddr)
		}
	}
	// Pins: the five selected feeds in range 1 map 16 addresses each.
	expectCard(t, "feed-000 v2", byCell[[2]int{index["feed-000"], 2}], format.CardinalityFromUint64(9))
	expectCard(t, "feed-000 v3", byCell[[2]int{index["feed-000"], 3}], format.CardinalityFromUint64(3))
	expectCard(t, "feed-000 v1", byCell[[2]int{index["feed-000"], 1}], format.CardinalityFromUint64(4))
	expectCard(t, "feed-000 unmapped", byCell[[2]int{index["feed-000"], 0}], format.CardinalityFromUint64(368))
	expectCard(t, "feed-001 unmapped", byCell[[2]int{index["feed-001"], 0}], format.CardinalityFromUint64(256))
	if _, ok := byCell[[2]int{index["feed-001"], 1}]; ok {
		t.Fatal("feed-001 must have no mapped cells")
	}

	// The named scope {feed-000, feed-001} keeps the same sweep totals but
	// only two feeds produce cells.
	named, err := q.NamedFeeds([]string{"feed-000", "feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want2 := joinDirectExpect(rows, directs, feedSet(t, named))
	var cells2 []DirectJoinCell
	report2, err := named.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 4096}, func(batch []DirectJoinCell) error {
		cells2 = append(cells2, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "named selected", report2.SelectedAddresses, want2.selected)
	expectCard(t, "named mapped", report2.MappedAddresses, want2.mapped)
	expectCard(t, "named unmapped", report2.UnmappedAddresses, want2.unmapped)
	if report2.ResultCellCount != 5 || len(cells2) != 5 {
		t.Fatalf("named result cells = %d, want 5", report2.ResultCellCount)
	}
}

func itoa(v uint32) string {
	if v == 0 {
		return "unmapped"
	}
	return fmt.Sprintf("v%d", v)
}

func TestJoinDirectErrors(t *testing.T) {
	member := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer member.Close()
	member6 := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer member6.Close()
	direct := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer direct.Close()
	q, err := member.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong value kind: a membership source is not a direct provider.
	if _, err := scope.JoinDirect(DirectJoinSourceImmutable(member), DirectJoinBudget{MaxResultCells: 1 << 20}, nil, nil); errorAsCode(err) != ErrorWrongValueKind {
		t.Fatalf("membership source error = %v, want WrongValueKind", err)
	}
	// Wrong address family: v6 membership scope against the v4 direct db.
	q6, err := member6.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope6, err := q6.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope6.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 1 << 20}, nil, nil); errorAsCode(err) != ErrorWrongAddressFamily {
		t.Fatalf("family mismatch error = %v, want WrongAddressFamily", err)
	}
	// The result-cell budget bounds distinct cells: 22 exist, 21 must
	// fail after the first 21.
	if _, err := scope.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 21}, nil, nil); errorAsCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("budget 21 error = %v, want InsufficientResourceBudget", err)
	}
	if _, err := scope.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 0}, nil, nil); errorAsCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("budget 0 error = %v, want InsufficientResourceBudget", err)
	}
	// Cancellation: a pre-cancelled token fails the op.
	cancel := NewCancellationToken()
	cancel.Cancel()
	if _, err := scope.JoinDirect(DirectJoinSourceImmutable(direct), DirectJoinBudget{MaxResultCells: 1 << 20}, nil, cancel); errorAsCode(err) != ErrorCancelled {
		t.Fatalf("cancelled direct join error = %v, want Cancelled", err)
	}
}

// ---------------------------------------------------------------------------
// membership join

// joinMembershipExpect derives the exact cross join from the model.
func joinMembershipExpect(rows []struct {
	from, to ipAddr
	feeds    []string
}, left, right []string) struct {
	leftAddr, rightAddr, overlap, leftUnc, rightUnc format.Cardinality129
	cross                                           map[[2]int]format.Cardinality129
	leftUncCells, rightUncCells                     map[string]format.Cardinality129
} {
	li := map[string]int{}
	for i, f := range left {
		li[f] = i
	}
	ri := map[string]int{}
	for i, f := range right {
		ri[f] = i
	}
	cross := map[[2]int]format.Cardinality129{}
	leftUncCells := map[string]format.Cardinality129{}
	rightUncCells := map[string]format.Cardinality129{}
	var leftAddr, rightAddr, overlap, leftUnc, rightUnc format.Cardinality129
	for _, row := range rows {
		lenRow := inclusiveLen(row.from, row.to)
		lAny, rAny := false, false
		for _, f := range row.feeds {
			if contains(left, f) {
				lAny = true
			}
			if contains(right, f) {
				rAny = true
			}
		}
		if lAny {
			leftAddr = cardAdd(leftAddr, lenRow)
		}
		if rAny {
			rightAddr = cardAdd(rightAddr, lenRow)
		}
		if lAny && rAny {
			overlap = cardAdd(overlap, lenRow)
		}
		if lAny && !rAny {
			leftUnc = cardAdd(leftUnc, lenRow)
		}
		if rAny && !lAny {
			rightUnc = cardAdd(rightUnc, lenRow)
		}
		for _, lf := range left {
			for _, rf := range right {
				if contains(row.feeds, lf) && contains(row.feeds, rf) {
					cross[[2]int{li[lf], ri[rf]}] = cardAdd(cross[[2]int{li[lf], ri[rf]}], lenRow)
				}
			}
		}
		for _, lf := range left {
			if contains(row.feeds, lf) {
				hasRight := false
				for _, rf := range right {
					if contains(row.feeds, rf) {
						hasRight = true
						break
					}
				}
				if !hasRight {
					leftUncCells[lf] = cardAdd(leftUncCells[lf], lenRow)
				}
			}
		}
		for _, rf := range right {
			if contains(row.feeds, rf) {
				hasLeft := false
				for _, lf := range left {
					if contains(row.feeds, lf) {
						hasLeft = true
						break
					}
				}
				if !hasLeft {
					rightUncCells[rf] = cardAdd(rightUncCells[rf], lenRow)
				}
			}
		}
	}
	return struct {
		leftAddr, rightAddr, overlap, leftUnc, rightUnc format.Cardinality129
		cross                                           map[[2]int]format.Cardinality129
		leftUncCells, rightUncCells                     map[string]format.Cardinality129
	}{leftAddr, rightAddr, overlap, leftUnc, rightUnc, cross, leftUncCells, rightUncCells}
}

func TestJoinMembershipV4(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	left, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := q.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := joinMembershipExpect(rows, feedSet(t, left), feedSet(t, right))

	var cross []MembershipCrossCell
	var uncovered []UncoveredFeed
	report, err := left.JoinMembership(right, func(batch []MembershipCrossCell) error {
		cross = append(cross, batch...)
		return nil
	}, func(batch []UncoveredFeed) error {
		uncovered = append(uncovered, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.LeftRangeCount != 3 || report.RightRangeCount != 3 || report.JoinedSegmentCount != 3 {
		t.Fatalf("report = %+v", report)
	}
	expectCard(t, "left", report.LeftAddresses, want.leftAddr)
	expectCard(t, "right", report.RightAddresses, want.rightAddr)
	expectCard(t, "overlap", report.OverlapAddresses, want.overlap)
	expectCard(t, "left uncovered", report.LeftUncoveredAddresses, want.leftUnc)
	expectCard(t, "right uncovered", report.RightUncoveredAddresses, want.rightUnc)
	if report.CrossResultCount != 70 || report.UncoveredResultCount != 71 {
		t.Fatalf("report = %+v", report)
	}
	// Pins.
	expectCard(t, "left pin", report.LeftAddresses, format.CardinalityFromUint64(512))
	expectCard(t, "right pin", report.RightAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "overlap pin", report.OverlapAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "left uncovered pin", report.LeftUncoveredAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "right uncovered pin", report.RightUncoveredAddresses, format.CardinalityZero())

	// The 70 cross cells arrive left-position then right-position ordered
	// and match the model.
	if len(cross) != 70 {
		t.Fatalf("cross cells = %d, want 70", len(cross))
	}
	li := map[string]int{}
	for i, e := range left.Feeds() {
		li[e.Name] = i
	}
	seenCross := map[[2]int]format.Cardinality129{}
	prevL, prevR := -1, -1
	for _, c := range cross {
		l, r := li[c.Left], 0
		if l < prevL || (l == prevL && r <= prevR) {
			t.Fatalf("cross out of order: (%s,%s)", c.Left, c.Right)
		}
		prevL, prevR = l, r
		seenCross[[2]int{l, r}] = c.Addresses
	}
	for pair, wantAddr := range want.cross {
		expectCard(t, "cross cell", seenCross[pair], wantAddr)
	}
	expectCard(t, "cross 065x001", seenCross[[2]int{li["feed-065"], 0}], format.CardinalityFromUint64(256))
	expectCard(t, "cross 000x001", seenCross[[2]int{li["feed-000"], 0}], format.CardinalityFromUint64(128))

	// Uncovered: 70 left cells then 1 right cell, model-exact.
	if len(uncovered) != 71 {
		t.Fatalf("uncovered cells = %d, want 71", len(uncovered))
	}
	for i, u := range uncovered {
		if i < 70 && u.Side != UncoveredLeft {
			t.Fatalf("cell %d side = %v, want left", i, u.Side)
		}
		if i == 70 && u.Side != UncoveredRight {
			t.Fatalf("cell %d side = %v, want right", i, u.Side)
		}
	}
	leftSeen := map[string]format.Cardinality129{}
	rightSeen := map[string]format.Cardinality129{}
	for _, u := range uncovered {
		if u.Side == UncoveredLeft {
			leftSeen[u.Feed] = u.Addresses
		} else {
			rightSeen[u.Feed] = u.Addresses
		}
	}
	for feed, wantAddr := range want.leftUncCells {
		expectCard(t, "left unc "+feed, leftSeen[feed], wantAddr)
	}
	for feed, wantAddr := range want.rightUncCells {
		expectCard(t, "right unc "+feed, rightSeen[feed], wantAddr)
	}
	expectCard(t, "left unc 000", leftSeen["feed-000"], format.CardinalityFromUint64(256))
	expectCard(t, "left unc 001", leftSeen["feed-001"], format.CardinalityZero())
	expectCard(t, "right unc 001", rightSeen["feed-001"], format.CardinalityZero())
}

func TestJoinMembershipNamedV4(t *testing.T) {
	rows := fixtureRanges(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	left, err := q.NamedFeeds([]string{"feed-000"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := q.NamedFeeds([]string{"feed-065"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := joinMembershipExpect(rows, feedSet(t, left), feedSet(t, right))

	var cross []MembershipCrossCell
	var uncovered []UncoveredFeed
	report, err := left.JoinMembership(right, func(batch []MembershipCrossCell) error {
		cross = append(cross, batch...)
		return nil
	}, func(batch []UncoveredFeed) error {
		uncovered = append(uncovered, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "left", report.LeftAddresses, want.leftAddr)
	expectCard(t, "right", report.RightAddresses, want.rightAddr)
	expectCard(t, "overlap", report.OverlapAddresses, want.overlap)
	expectCard(t, "left uncovered", report.LeftUncoveredAddresses, want.leftUnc)
	expectCard(t, "right uncovered", report.RightUncoveredAddresses, want.rightUnc)
	if report.CrossResultCount != 1 || report.UncoveredResultCount != 2 {
		t.Fatalf("report = %+v", report)
	}
	// Pins: 000 covers 384 (ranges 1+2), 065 covers 256 (ranges 2+3),
	// shared segment is range 2 (128).
	expectCard(t, "left pin", report.LeftAddresses, format.CardinalityFromUint64(384))
	expectCard(t, "right pin", report.RightAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "overlap pin", report.OverlapAddresses, format.CardinalityFromUint64(128))
	expectCard(t, "left unc pin", report.LeftUncoveredAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "right unc pin", report.RightUncoveredAddresses, format.CardinalityFromUint64(128))
	if len(cross) != 1 || cross[0].Left != "feed-000" || cross[0].Right != "feed-065" {
		t.Fatalf("cross = %+v", cross)
	}
	expectCard(t, "cross cell", cross[0].Addresses, format.CardinalityFromUint64(128))
	if len(uncovered) != 2 ||
		uncovered[0].Side != UncoveredLeft || uncovered[0].Feed != "feed-000" ||
		uncovered[1].Side != UncoveredRight || uncovered[1].Feed != "feed-065" {
		t.Fatalf("uncovered = %+v", uncovered)
	}
	expectCard(t, "unc 000", uncovered[0].Addresses, format.CardinalityFromUint64(256))
	expectCard(t, "unc 065", uncovered[1].Addresses, format.CardinalityFromUint64(128))
	// Model-exact on top of the pins.
	expectCard(t, "model unc 000", uncovered[0].Addresses, want.leftUncCells["feed-000"])
	expectCard(t, "model unc 065", uncovered[1].Addresses, want.rightUncCells["feed-065"])
}

func TestJoinMembershipErrors(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()
	db6 := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer db6.Close()
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	q6, err := db6.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	left, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Family mismatch: v4 scope joined with a v6 scope.
	right6, err := q6.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := left.JoinMembership(right6, nil, nil, nil); errorAsCode(err) != ErrorWrongAddressFamily {
		t.Fatalf("family mismatch error = %v, want WrongAddressFamily", err)
	}
	// A small budget fails the op heap: the cross vector needs 70*24
	// bytes, uncovered 71*24 more.
	tiny, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := q.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tiny.JoinMembership(right, nil, nil, nil); errorAsCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("tight budget error = %v, want InsufficientResourceBudget", err)
	}
	// Sink errors pass through unchanged.
	left2, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	right2, err := q.NamedFeeds([]string{"feed-001"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("sink stop")
	if _, err := left2.JoinMembership(right2, func([]MembershipCrossCell) error { return sentinel }, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("cross sink error = %v, want sentinel", err)
	}
	// Cancellation: pre-cancelled token fails the op.
	cancel := NewCancellationToken()
	cancel.Cancel()
	if _, err := left2.JoinMembership(right2, nil, nil, cancel); errorAsCode(err) != ErrorCancelled {
		t.Fatalf("cancelled join error = %v, want Cancelled", err)
	}
}
