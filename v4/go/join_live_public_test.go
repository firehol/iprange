// Live direct join source tests (Rust tests/provider_joins.rs live
// parity): a membership scope can join against the pinned generation
// of a live direct provider through DirectJoinSourceLive, the exact
// coverage facts and result cells match the immutable source shape,
// and a closing or closed live reader is refused before any page is
// touched.

package iprangedb

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// liveJoinDirectProvider builds one live direct pair with two ranges:
// 5-14 -> 100 and 20-24 -> 200 (Rust provider_joins provider shape).
func liveJoinDirectProvider(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "join-live-provider.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, mustTag(t, "asn"), 2, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := w.BeginDirectReplacement(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.AddRangesV4([]DirectRangeV4{
		{From: 5, To: 14, Value: 100},
		{From: 20, To: 24, Value: 200},
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	result, err := finished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("direct replacement status = %v, want Committed", result.Status)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// liveJoinMembershipPair builds one live membership pair with the
// feeds x = 5-14 and y = 20-24.
func liveJoinMembershipPair(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "join-live-membership.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, mustTag(t, "providers"), 2, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	addFeed := func(name string, from, to uint32) {
		t.Helper()
		create, err := w.BeginCreateFeed(feedName(t, name), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := create.AddRangesV4([]AddressRange4{{From: IPv4(from), To: IPv4(to)}}); err != nil {
			t.Fatal(err)
		}
		finished, err := create.FinishInput()
		if err != nil {
			t.Fatal(err)
		}
		result, err := finished.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != CommitCommitted {
			t.Fatalf("feed %s status = %v, want Committed", name, result.Status)
		}
	}
	addFeed("x", 5, 14)
	addFeed("y", 20, 24)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestJoinDirectLiveSource runs one live membership scope against one
// live direct provider and pins the exact facts and result cells (Rust
// provider_joins DirectJoinSource::Live parity): feed x is mapped by
// value 100, feed y by value 200, with no unmapped addresses.
func TestJoinDirectLiveSource(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	providerPath := liveJoinDirectProvider(t)
	membershipPath := liveJoinMembershipPair(t)

	membership, err := OpenLiveReader(membershipPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := OpenLiveReader(providerPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	query, err := membership.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var cells []DirectJoinCell
	report, err := scope.JoinDirect(DirectJoinSourceLive(provider), DirectJoinBudget{MaxResultCells: 4096}, func(batch []DirectJoinCell) error {
		cells = append(cells, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.MembershipRangeCount != 2 {
		t.Fatalf("membership range count = %d, want 2", report.MembershipRangeCount)
	}
	if report.DirectRangesVisited != 2 {
		t.Fatalf("direct ranges visited = %d, want 2", report.DirectRangesVisited)
	}
	if report.JoinedSegmentCount != 2 {
		t.Fatalf("joined segment count = %d, want 2", report.JoinedSegmentCount)
	}
	expectCard(t, "selected", report.SelectedAddresses, format.CardinalityFromUint64(15))
	expectCard(t, "mapped", report.MappedAddresses, format.CardinalityFromUint64(15))
	expectCard(t, "unmapped", report.UnmappedAddresses, format.CardinalityFromUint64(0))
	if report.ResultCellCount != 2 {
		t.Fatalf("result cell count = %d, want 2", report.ResultCellCount)
	}

	got := map[string]map[uint64]format.Cardinality129{}
	for _, cell := range cells {
		if got[cell.Feed] == nil {
			got[cell.Feed] = map[uint64]format.Cardinality129{}
		}
		got[cell.Feed][cellKey(cell.DirectValue)] = cell.Addresses
	}
	if got["x"] == nil || len(got["x"]) != 1 {
		t.Fatalf("feed x cells = %v, want one cell", got["x"])
	}
	expectCard(t, "feed x value 100", got["x"][cellKey(valuePointer(100))], format.CardinalityFromUint64(10))
	if got["y"] == nil || len(got["y"]) != 1 {
		t.Fatalf("feed y cells = %v, want one cell", got["y"])
	}
	expectCard(t, "feed y value 200", got["y"][cellKey(valuePointer(200))], format.CardinalityFromUint64(5))

	// A closing or closed live source is refused before any page is
	// touched (Rust LiveReader::core -> require_open), and a
	// membership reader is refused as the direct provider.
	if _, err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.JoinDirect(DirectJoinSourceLive(provider), DirectJoinBudget{MaxResultCells: 4096}, nil, nil); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("closed live source = %v, want WrongState", err)
	}
	if _, err := scope.JoinDirect(DirectJoinSourceLive(membership), DirectJoinBudget{MaxResultCells: 4096}, nil, nil); errorAsCode(err) != ErrorWrongValueKind {
		t.Fatalf("membership live source = %v, want WrongValueKind", err)
	}
	if _, err := scope.JoinDirect(DirectJoinSource{}, DirectJoinBudget{MaxResultCells: 4096}, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("empty source = %v, want InvalidArgument", err)
	}
	if _, err := membership.Close(); err != nil {
		t.Fatal(err)
	}
}

// valuePointer builds one direct-value pointer for cell assertions.
func valuePointer(value uint32) *uint32 { return &value }

// cellKey encodes one optional direct value as a map key: 0 for the
// unmapped cell, otherwise 1<<32 | value (pointer identity is not
// stable across batch boundaries).
func cellKey(value *uint32) uint64 {
	if value == nil {
		return 0
	}
	return 1<<32 | uint64(*value)
}
