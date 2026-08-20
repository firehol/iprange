// Milestone 3 chunk 3a parity tests: the read-side algebra (Rust
// membership_query/algebra.rs) over the committed conformance corpus.
// Expected values are derived from cases.json (the Rust-produced
// authority) through the same interval model the chunk-2 tests use; the
// two-source compounds run two opens of the same fixture, so sweep
// segments align and every bucket folds per fixture row.

package iprangedb

import (
	"errors"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// algebraRow is one fixture membership range.
type algebraRow struct {
	from, to ipAddr
	feeds    []string
}

func algebraRows(t *testing.T, file string) []algebraRow {
	t.Helper()
	raw := fixtureRanges(t, file)
	rows := make([]algebraRow, len(raw))
	for i, r := range raw {
		rows[i] = algebraRow{from: r.from, to: r.to, feeds: r.feeds}
	}
	return rows
}

// selSpec is one modeled selection: all feeds or an exact name list.
type selSpec struct {
	all   bool
	feeds []string
}

func selAll() selSpec                  { return selSpec{all: true} }
func selNamed(names ...string) selSpec { return selSpec{feeds: names} }

func selIn(spec selSpec, rowFeeds []string) bool {
	if spec.all {
		return len(rowFeeds) != 0
	}
	for _, f := range spec.feeds {
		if contains(rowFeeds, f) {
			return true
		}
	}
	return false
}

// algebraCountModel sums every row whose feeds intersect the selection.
func algebraCountModel(rows []algebraRow, spec selSpec) format.Cardinality129 {
	var total format.Cardinality129
	for _, row := range rows {
		if selIn(spec, row.feeds) {
			total = cardAdd(total, inclusiveLen(row.from, row.to))
		}
	}
	return total
}

// algebraCompareModel folds every row into the four comparison buckets.
func algebraCompareModel(rows []algebraRow, left, right selSpec) (l, r, ov, lo, ro, un format.Cardinality129) {
	for _, row := range rows {
		inLeft := selIn(left, row.feeds)
		inRight := selIn(right, row.feeds)
		if !inLeft && !inRight {
			continue
		}
		n := inclusiveLen(row.from, row.to)
		switch {
		case inLeft && inRight:
			l = cardAdd(l, n)
			r = cardAdd(r, n)
			ov = cardAdd(ov, n)
			un = cardAdd(un, n)
		case inLeft:
			l = cardAdd(l, n)
			lo = cardAdd(lo, n)
			un = cardAdd(un, n)
		case inRight:
			r = cardAdd(r, n)
			ro = cardAdd(ro, n)
			un = cardAdd(un, n)
		}
	}
	return l, r, ov, lo, ro, un
}

// algebraV4 opens twice and builds the two-source algebra over both
// all-catalog scopes.
func algebraV4(t *testing.T) (*MembershipAlgebra, func()) {
	t.Helper()
	dbA := mustOpen(t, "rust/membership-ipv4.iprdb")
	dbB := mustOpen(t, "rust/membership-ipv4.iprdb")
	qa, err := dbA.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	qb, err := dbB.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scopeA, err := qa.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope a:", err)
	}
	scopeB, err := qb.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope b:", err)
	}
	alg, err := NewMembershipAlgebra([]*MembershipScope{scopeA, scopeB}, MembershipAlgebraBudget{
		MaxHeapBytes: 1 << 20,
		MaxSources:   8,
	}, nil)
	if err != nil {
		t.Fatal("algebra:", err)
	}
	return alg, func() {
		dbA.Close()
		dbB.Close()
	}
}

// algebraV6 opens twice over the IPv6 fixture.
func algebraV6(t *testing.T) (*MembershipAlgebra, func()) {
	t.Helper()
	dbA := mustOpen(t, "rust/membership-ipv6.iprdb")
	dbB := mustOpen(t, "rust/membership-ipv6.iprdb")
	qa, err := dbA.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	qb, err := dbB.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scopeA, err := qa.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope a:", err)
	}
	scopeB, err := qb.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope b:", err)
	}
	alg, err := NewMembershipAlgebra([]*MembershipScope{scopeA, scopeB}, MembershipAlgebraBudget{
		MaxHeapBytes: 1 << 20,
		MaxSources:   8,
	}, nil)
	if err != nil {
		t.Fatal("algebra:", err)
	}
	return alg, func() {
		dbA.Close()
		dbB.Close()
	}
}

func TestAlgebraCountV4(t *testing.T) {
	rows := algebraRows(t, "rust/membership-ipv4.iprdb")
	alg, closeFn := algebraV4(t)
	defer closeFn()

	if alg.AddressFamily() != AddressFamilyIPv4 {
		t.Fatalf("family = %d", alg.AddressFamily())
	}
	if alg.FeedCount() != 70 {
		t.Fatalf("feed count = %d, want 70", alg.FeedCount())
	}
	feeds := alg.Feeds()
	if len(feeds) != 70 || feeds[0].Name != "feed-000" || feeds[69].Name != "feed-reused" {
		t.Fatalf("feeds = %d, first %q, last %q", len(feeds), feeds[0].Name, feeds[69].Name)
	}

	// All feeds: the two identical sources join to the single-fixture union.
	report, err := alg.Count(AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "all addresses", report.Addresses, format.CardinalityFromUint64(512))
	if report.SourceCount != 2 || report.SourceRangeCount != 6 || report.JoinedSegmentCount != 3 {
		t.Fatalf("report = %+v", report)
	}

	// Named selections match the interval model over the fixture rows.
	for _, spec := range []struct {
		names []string
		want  uint64
	}{
		{[]string{"feed-000"}, 384},
		{[]string{"feed-001"}, 256},
		{[]string{"feed-065"}, 256},
		{[]string{"feed-reused"}, 384},
		{[]string{"feed-002"}, 0},
		{[]string{"feed-000", "feed-001"}, 512},
		{[]string{"feed-002", "feed-065"}, 256},
	} {
		report, err := alg.Count(AlgebraFeedSelectionNamed(spec.names), nil)
		if err != nil {
			t.Fatalf("count %v: %v", spec.names, err)
		}
		expectCard(t, "union "+cardStr(format.CardinalityFromUint64(spec.want)), report.Addresses, format.CardinalityFromUint64(spec.want))
		model := algebraCountModel(rows, selNamed(spec.names...))
		if report.Addresses.Compare(model) != 0 {
			t.Fatalf("count %v = %s, model %s", spec.names, cardStr(report.Addresses), cardStr(model))
		}
		if report.SourceRangeCount != 6 {
			t.Fatalf("count %v source ranges = %d, want 6", spec.names, report.SourceRangeCount)
		}
	}
}

func TestAlgebraCompareV4(t *testing.T) {
	rows := algebraRows(t, "rust/membership-ipv4.iprdb")
	alg, closeFn := algebraV4(t)
	defer closeFn()

	// Equal selections are exactly equal with zero-only columns.
	report, err := alg.Compare(AlgebraFeedSelectionAll(), AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "left", report.LeftAddresses, format.CardinalityFromUint64(512))
	expectCard(t, "union", report.UnionAddresses, format.CardinalityFromUint64(512))
	expectCard(t, "overlap", report.OverlapAddresses, format.CardinalityFromUint64(512))
	if report.LeftOnlyAddresses.Compare(format.CardinalityZero()) != 0 ||
		report.RightOnlyAddresses.Compare(format.CardinalityZero()) != 0 {
		t.Fatalf("only columns = %s/%s, want zero", cardStr(report.LeftOnlyAddresses), cardStr(report.RightOnlyAddresses))
	}
	if !report.Equal || report.SourceRangeCount != 6 || report.JoinedSegmentCount != 3 {
		t.Fatalf("report = %+v", report)
	}

	// All vs one feed: overlap is exactly that feed's coverage.
	report, err = alg.Compare(AlgebraFeedSelectionAll(), AlgebraFeedSelectionNamed([]string{"feed-001"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "left", report.LeftAddresses, format.CardinalityFromUint64(512))
	expectCard(t, "right", report.RightAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "overlap", report.OverlapAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "left only", report.LeftOnlyAddresses, format.CardinalityFromUint64(256))
	if report.RightOnlyAddresses.Compare(format.CardinalityZero()) != 0 {
		t.Fatalf("right only = %s, want zero", cardStr(report.RightOnlyAddresses))
	}
	expectCard(t, "union", report.UnionAddresses, format.CardinalityFromUint64(512))
	if report.Equal {
		t.Fatal("selections reported equal")
	}

	// One side empty: pure left-only column over feed-000's two rows.
	report, err = alg.Compare(AlgebraFeedSelectionNamed([]string{"feed-000"}), AlgebraFeedSelectionNamed([]string{"feed-002"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "left", report.LeftAddresses, format.CardinalityFromUint64(384))
	expectCard(t, "right", report.RightAddresses, format.CardinalityFromUint64(0))
	expectCard(t, "overlap", report.OverlapAddresses, format.CardinalityFromUint64(0))
	expectCard(t, "left only", report.LeftOnlyAddresses, format.CardinalityFromUint64(384))
	if report.RightOnlyAddresses.Compare(format.CardinalityZero()) != 0 {
		t.Fatalf("right only = %s, want zero", cardStr(report.RightOnlyAddresses))
	}
	expectCard(t, "union", report.UnionAddresses, format.CardinalityFromUint64(384))
	if report.Equal {
		t.Fatal("selections reported equal")
	}

	// Overlapping selections: feed-000 and feed-065 share the middle row
	// (128 addresses) and cover 384 and 256 addresses in total.
	report, err = alg.Compare(AlgebraFeedSelectionNamed([]string{"feed-000"}), AlgebraFeedSelectionNamed([]string{"feed-065"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	expectCard(t, "left", report.LeftAddresses, format.CardinalityFromUint64(384))
	expectCard(t, "right", report.RightAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "overlap", report.OverlapAddresses, format.CardinalityFromUint64(128))
	expectCard(t, "left only", report.LeftOnlyAddresses, format.CardinalityFromUint64(256))
	expectCard(t, "right only", report.RightOnlyAddresses, format.CardinalityFromUint64(128))
	expectCard(t, "union", report.UnionAddresses, format.CardinalityFromUint64(512))

	// Every combination matches the fixture-row model.
	for _, pair := range [][2]selSpec{
		{selAll(), selAll()},
		{selAll(), selNamed("feed-001")},
		{selAll(), selNamed("feed-002")},
		{selNamed("feed-000"), selNamed("feed-065")},
		{selNamed("feed-000", "feed-001"), selNamed("feed-001", "feed-065")},
		{selNamed("feed-002"), selNamed("feed-002")},
		{selNamed("feed-002"), selNamed("feed-069")},
	} {
		left := AlgebraFeedSelectionAll()
		right := AlgebraFeedSelectionAll()
		if !pair[0].all {
			left = AlgebraFeedSelectionNamed(pair[0].feeds)
		}
		if !pair[1].all {
			right = AlgebraFeedSelectionNamed(pair[1].feeds)
		}
		report, err := alg.Compare(left, right, nil)
		if err != nil {
			t.Fatalf("compare %v vs %v: %v", pair[0], pair[1], err)
		}
		wl, wr, wov, wlo, wro, wun := algebraCompareModel(rows, pair[0], pair[1])
		expectCard(t, "model left", report.LeftAddresses, wl)
		expectCard(t, "model right", report.RightAddresses, wr)
		expectCard(t, "model overlap", report.OverlapAddresses, wov)
		expectCard(t, "model left only", report.LeftOnlyAddresses, wlo)
		expectCard(t, "model right only", report.RightOnlyAddresses, wro)
		expectCard(t, "model union", report.UnionAddresses, wun)
	}
}

func TestAlgebraV6(t *testing.T) {
	rows := algebraRows(t, "rust/membership-ipv6.iprdb")
	alg, closeFn := algebraV6(t)
	defer closeFn()

	if alg.AddressFamily() != AddressFamilyIPv6 {
		t.Fatalf("family = %d", alg.AddressFamily())
	}
	if alg.FeedCount() != 2 {
		t.Fatalf("feed count = %d, want 2", alg.FeedCount())
	}
	feeds := alg.Feeds()
	if len(feeds) != 2 || feeds[0].Name != "global" || feeds[1].Name != "special" {
		t.Fatalf("feeds = %v", feeds)
	}

	// The three v6 rows tile the whole 128-bit universe without gaps.
	report, err := alg.Count(AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal(err)
	}
	model := algebraCountModel(rows, selAll())
	if report.Addresses.Compare(model) != 0 {
		t.Fatalf("all = %s, model %s", cardStr(report.Addresses), cardStr(model))
	}
	// Model sanity pin: the rows tile the universe, so the all-feeds
	// union is exactly 2^128; the model comparison above already proves
	// it, and the count of the full v6 universe is a stable fixture fact.
	if report.SourceCount != 2 || report.SourceRangeCount != 6 || report.JoinedSegmentCount != 3 {
		t.Fatalf("report = %+v", report)
	}

	for _, names := range [][]string{
		{"global"},
		{"special"},
		{"global", "special"},
	} {
		report, err := alg.Count(AlgebraFeedSelectionNamed(names), nil)
		if err != nil {
			t.Fatalf("count %v: %v", names, err)
		}
		model := algebraCountModel(rows, selNamed(names...))
		if report.Addresses.Compare(model) != 0 {
			t.Fatalf("count %v = %s, model %s", names, cardStr(report.Addresses), cardStr(model))
		}
	}

	// global (all rows) vs special (one row of 65536).
	cmp, err := alg.Compare(AlgebraFeedSelectionNamed([]string{"global"}), AlgebraFeedSelectionNamed([]string{"special"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	wl, wr, wov, wlo, wro, wun := algebraCompareModel(rows, selNamed("global"), selNamed("special"))
	expectCard(t, "model left", cmp.LeftAddresses, wl)
	expectCard(t, "model right", cmp.RightAddresses, wr)
	expectCard(t, "model overlap", cmp.OverlapAddresses, wov)
	expectCard(t, "model left only", cmp.LeftOnlyAddresses, wlo)
	expectCard(t, "model right only", cmp.RightOnlyAddresses, wro)
	expectCard(t, "model union", cmp.UnionAddresses, wun)
	// The special-only row is exactly 65536 addresses.
	expectCard(t, "right", cmp.RightAddresses, format.CardinalityFromUint64(65536))
	if cmp.Equal {
		t.Fatal("global and special reported equal")
	}
}

func TestAlgebraErrors(t *testing.T) {
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

	// No sources (Rust: InvalidArgument "membership algebra has no sources").
	_, err = NewMembershipAlgebra(nil, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if code := errorCode(err); code != ErrorInvalidArgument {
		t.Fatalf("nil scopes: code %v, want %v (err %v)", code, ErrorInvalidArgument, err)
	}

	// MaxSources admission (Rust: BudgetExceeded "membership algebra sources").
	_, err = NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 0}, nil)
	if code := errorCode(err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("max sources: code %v, want %v (err %v)", code, ErrorInsufficientResourceBudget, err)
	}

	// Source heap admission too small (Rust: BudgetExceeded "membership
	// algebra source heap").
	_, err = NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1, MaxSources: 8}, nil)
	if code := errorCode(err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("tiny heap: code %v, want %v (err %v)", code, ErrorInsufficientResourceBudget, err)
	}
	if err.Error() == "" || !containsString(err.Error(), "membership algebra source heap") {
		t.Fatalf("tiny heap detail = %q", err.Error())
	}

	// Family disagreement (Rust: WrongAddressFamily "membership algebra
	// source families differ").
	db6 := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer db6.Close()
	q6, err := db6.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope6, err := q6.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewMembershipAlgebra([]*MembershipScope{scope, scope6}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if code := errorCode(err); code != ErrorWrongAddressFamily {
		t.Fatalf("family mix: code %v, want %v (err %v)", code, ErrorWrongAddressFamily, err)
	}
	if !containsString(err.Error(), "membership algebra source families differ") {
		t.Fatalf("family mix detail = %q", err.Error())
	}

	alg, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Empty named selection (Rust: InvalidArgument "membership algebra
	// feed selection is empty").
	_, err = alg.Count(AlgebraFeedSelectionNamed(nil), nil)
	if code := errorCode(err); code != ErrorInvalidArgument {
		t.Fatalf("empty selection: code %v (err %v)", code, err)
	}
	if !containsString(err.Error(), "membership algebra feed selection is empty") {
		t.Fatalf("empty selection detail = %q", err.Error())
	}

	// Duplicate names (Rust: "membership algebra feed selection is not
	// unique").
	_, err = alg.Count(AlgebraFeedSelectionNamed([]string{"feed-000", "feed-000"}), nil)
	if code := errorCode(err); code != ErrorInvalidArgument {
		t.Fatalf("duplicate selection: code %v (err %v)", code, err)
	}
	if !containsString(err.Error(), "membership algebra feed selection is not unique") {
		t.Fatalf("duplicate selection detail = %q", err.Error())
	}

	// Unknown name (Rust: NameNotFound).
	_, err = alg.Count(AlgebraFeedSelectionNamed([]string{"no-such-feed"}), nil)
	if code := errorCode(err); code != ErrorNameNotFound {
		t.Fatalf("unknown name: code %v (err %v)", code, err)
	}

	// Invalid feed-name spelling (Rust: FeedName::new -> NameInvalid).
	_, err = alg.Count(AlgebraFeedSelectionNamed([]string{"Bad Name!"}), nil)
	if code := errorCode(err); code != ErrorNameInvalid {
		t.Fatalf("invalid name: code %v (err %v)", code, err)
	}

	// Operation heap too small after construction: a budget that passes
	// the state build (sources 224 + catalog 17920 + inputs 32 + local
	// maps 280 = 18456) but leaves less than one source state (1664) in
	// the operation heap fails the first scan charge with a budget error
	// (Rust runs the identical charge order; the 19000 byte budget holds
	// for any nearby calibration of the modeled sizes).
	algSmall, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 19000, MaxSources: 8}, nil)
	if err != nil {
		t.Fatalf("tight algebra: %v", err)
	}
	_, err = algSmall.Count(AlgebraFeedSelectionAll(), nil)
	if code := errorCode(err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("tight heap count: code %v (err %v)", code, err)
	}

	// Cancellation is observed at the first checkpoint (Rust
	// cancellation.rs).
	token := NewCancellationToken()
	token.Cancel()
	_, err = alg.Count(AlgebraFeedSelectionAll(), token)
	if code := errorCode(err); code != ErrorCancelled {
		t.Fatalf("cancelled count: code %v (err %v)", code, err)
	}
}

func TestAlgebraClosedReader(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	alg, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = alg.Count(AlgebraFeedSelectionAll(), nil)
	if code := errorCode(err); code != ErrorWrongState {
		t.Fatalf("closed reader count: code %v (err %v)", code, err)
	}
	// The catalog stays readable after the source reader closed (the
	// algebra owns only scopes; the closed-state guard is at the
	// operation boundary).
	if alg.FeedCount() != 70 {
		t.Fatalf("feed count after close = %d", alg.FeedCount())
	}
}

func errorCode(err error) ErrorCode {
	var e *Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Code
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
