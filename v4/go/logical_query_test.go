// Milestone 3 chunk 1 parity tests: the logical read SDK (ordered
// cursors, catalog feed cursor, named-feed projections, membership
// queries) against the committed conformance corpus. Every expected
// value below comes from cases.json (the Rust-produced authority).

package iprangedb

import (
	"errors"
	"path/filepath"
	"testing"
)

func manifestFixture(t *testing.T, file string) conformanceCase {
	t.Helper()
	m := loadManifest(t)
	for _, fx := range m.Fixtures {
		if fx.File == file {
			return fx
		}
	}
	t.Fatalf("fixture %s not in manifest", file)
	return conformanceCase{}
}

func TestDirectCursorV4Parity(t *testing.T) {
	fx := manifestFixture(t, "rust/direct-ipv4.iprdb")
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()

	// Forward: the cursor must emit the exact manifest ranges in order.
	cur, err := db.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range fx.DirectRanges {
		got, ok, err := cur.NextRange()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("cursor ended early at %v", want)
		}
		if uint32(got.From) != uint32(parseV4(want.From)) || uint32(got.To) != uint32(parseV4(want.To)) || got.Value != want.Value {
			t.Fatalf("range = %+v, want %+v", got, want)
		}
	}
	if _, ok, err := cur.NextRange(); err != nil || ok {
		t.Fatalf("cursor not exhausted: ok=%v err=%v", ok, err)
	}

	// Backward: the exact same ranges in reverse order.
	bcur, err := db.DirectCursorV4(RangeDirectionBackward)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(fx.DirectRanges) - 1; i >= 0; i-- {
		want := fx.DirectRanges[i]
		got, ok, err := bcur.NextRange()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("backward cursor ended early at %v", want)
		}
		if uint32(got.From) != uint32(parseV4(want.From)) || uint32(got.To) != uint32(parseV4(want.To)) || got.Value != want.Value {
			t.Fatalf("backward range = %+v, want %+v", got, want)
		}
	}
	if _, ok, err := bcur.NextRange(); err != nil || ok {
		t.Fatalf("backward cursor not exhausted: ok=%v err=%v", ok, err)
	}
}

func TestDirectCursorV4Seek(t *testing.T) {
	fx := manifestFixture(t, "rust/direct-ipv4.iprdb")
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()

	first := fx.DirectRanges[0]
	second := fx.DirectRanges[1]

	// Seek to the exact first range start: the containing range is first.
	cur, err := db.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.Seek(parseV4(first.From)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek exact start: ok=%v err=%v", ok, err)
	}
	if uint32(got.From) != uint32(parseV4(first.From)) {
		t.Fatalf("seek exact start = %+v, want from %v", got, first.From)
	}

	// Seek inside a range: the containing range is emitted.
	if err := cur.Seek(parseV4(first.To)); err != nil {
		t.Fatal(err)
	}
	got, ok, err = cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek inside: ok=%v err=%v", ok, err)
	}
	if uint32(got.To) != uint32(parseV4(first.To)) {
		t.Fatalf("seek inside = %+v, want to %v", got, first.To)
	}

	// Seek in the gap after the first range: the next range is emitted
	// (the ranges in this fixture are adjacent, so seek exactly at the
	// boundary and verify the second range).
	if err := cur.Seek(parseV4(second.From)); err != nil {
		t.Fatal(err)
	}
	got, ok, err = cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek boundary: ok=%v err=%v", ok, err)
	}
	if uint32(got.From) != uint32(parseV4(second.From)) {
		t.Fatalf("seek boundary = %+v, want from %v", got, second.From)
	}

	// Seek past the end: exhausted.
	last := fx.DirectRanges[len(fx.DirectRanges)-1]
	if err := cur.Seek(parseV4(last.To) + 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cur.NextRange(); err != nil || ok {
		t.Fatalf("seek past end: ok=%v err=%v", ok, err)
	}

	// Re-seek after exhaustion must work (Rust CursorState.seek parity).
	if err := cur.Seek(parseV4(first.From)); err != nil {
		t.Fatal(err)
	}
	got, ok, err = cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("re-seek after exhaustion: ok=%v err=%v", ok, err)
	}
	if uint32(got.From) != uint32(parseV4(first.From)) {
		t.Fatalf("re-seek = %+v, want from %v", got, first.From)
	}

	// Backward seek: exact match returns the containing range, then the
	// cursor continues backward.
	bcur, err := db.DirectCursorV4(RangeDirectionBackward)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcur.Seek(parseV4(second.To)); err != nil {
		t.Fatal(err)
	}
	got, ok, err = bcur.NextRange()
	if err != nil || !ok {
		t.Fatalf("backward seek: ok=%v err=%v", ok, err)
	}
	if uint32(got.To) != uint32(parseV4(second.To)) {
		t.Fatalf("backward seek = %+v, want to %v", got, second.To)
	}
	// Below the first range: exhausted.
	if err := bcur.Seek(parseV4(first.From) - 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bcur.NextRange(); err != nil || ok {
		t.Fatalf("backward seek below start: ok=%v err=%v", ok, err)
	}
}

func TestDirectCursorV6Parity(t *testing.T) {
	fx := manifestFixture(t, "rust/first-seen-ipv6.iprdb")
	db := mustOpen(t, "rust/first-seen-ipv6.iprdb")
	defer db.Close()

	cur, err := db.DirectCursorV6(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.DirectRanges) == 0 {
		t.Fatal("fixture has no ranges")
	}
	want := fx.DirectRanges[0]
	got, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("v6 first range: ok=%v err=%v", ok, err)
	}
	wantFrom := parseV6Full(want.From)
	wantTo := parseV6Full(want.To)
	if got.FromHi != wantFrom.Hi || got.FromLo != wantFrom.Lo || got.ToHi != wantTo.Hi || got.ToLo != wantTo.Lo {
		t.Fatalf("v6 range = %+v, want %v-%v", got, want.From, want.To)
	}
	if _, ok, err := cur.NextRange(); err != nil || ok {
		t.Fatalf("v6 cursor not exhausted: ok=%v err=%v", ok, err)
	}

	// Seek inside the single range: containing range must be emitted.
	if err := cur.Seek(wantTo); err != nil {
		t.Fatal(err)
	}
	got, ok, err = cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("v6 seek: ok=%v err=%v", ok, err)
	}
	if got.ToHi != wantTo.Hi || got.ToLo != wantTo.Lo {
		t.Fatalf("v6 seek range = %+v, want to %v", got, want.To)
	}
}

func TestFeedCursorParity(t *testing.T) {
	for _, file := range []string{"rust/membership-ipv4.iprdb", "rust/membership-ipv6.iprdb", "rust/structured-ipv4.iprdb"} {
		t.Run(file, func(t *testing.T) {
			fx := manifestFixture(t, file)
			db := mustOpen(t, file)
			defer db.Close()

			cur, err := db.FeedCursor()
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			for {
				entry, ok, err := cur.NextFeed()
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if index >= len(fx.Feeds) {
					t.Fatalf("catalog emitted more feeds than the manifest")
				}
				want := fx.Feeds[index]
				if entry.Name != want.Name || entry.Index != want.Index {
					t.Fatalf("feed %d = %+v, want %+v", index, entry, want)
				}
				index++
			}
			if index != len(fx.Feeds) {
				t.Fatalf("catalog emitted %d feeds, manifest has %d", index, len(fx.Feeds))
			}
		})
	}
}

func TestFeedCursorDirectDatabase(t *testing.T) {
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()
	if _, err := db.FeedCursor(); err == nil {
		t.Fatal("FeedCursor on a direct database must fail")
	} else if errorAsCode(err) != ErrorWrongValueKind {
		t.Fatalf("FeedCursor error = %v, want WrongValueKind", err)
	}
}

// coalesce feeds: the projection merges adjacent same-feed intervals per
// direction; compute the expected coalesced intervals from the manifest.
type manifestRange4 struct {
	From  string
	To    string
	Feeds []string
}

func coalescedRanges4(ranges []manifestRange4, feed string, backward bool) [][2]IPv4 {
	var own [][2]IPv4
	for _, r := range ranges {
		has := false
		for _, name := range r.Feeds {
			if name == feed {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		own = append(own, [2]IPv4{parseV4(r.From), parseV4(r.To)})
	}
	// Coalesce adjacent (a.To+1 == b.From); ascending order.
	var merged [][2]IPv4
	for _, r := range own {
		if len(merged) > 0 && merged[len(merged)-1][1]+1 == r[0] {
			merged[len(merged)-1][1] = r[1]
		} else {
			merged = append(merged, r)
		}
	}
	if backward {
		out := make([][2]IPv4, len(merged))
		for i, m := range merged {
			out[len(merged)-1-i] = m
		}
		return out
	}
	return merged
}

func TestFeedRangeCursorV4Parity(t *testing.T) {
	fx := manifestFixture(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()

	for _, feed := range []string{"feed-000", "feed-001", "feed-063", "feed-069"} {
		for _, backward := range []bool{false, true} {
			direction := RangeDirectionForward
			name := "forward"
			if backward {
				direction = RangeDirectionBackward
				name = "backward"
			}
			t.Run(feed+"-"+name, func(t *testing.T) {
				cur, err := db.FeedRangeCursorV4(feed, direction)
				if err != nil {
					t.Fatal(err)
				}
				var manifest []manifestRange4
				for _, r := range fx.MembershipRanges {
					manifest = append(manifest, manifestRange4{From: r.From, To: r.To, Feeds: r.Feeds})
				}
				want := coalescedRanges4(manifest, feed, backward)
				index := 0
				for {
					got, ok, err := cur.NextRange()
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						break
					}
					if index >= len(want) {
						t.Fatalf("projection emitted more intervals than expected")
					}
					w := want[index]
					if got.From != w[0] || got.To != w[1] {
						t.Fatalf("interval %d = %v-%v, want %v-%v", index, got.From, got.To, w[0], w[1])
					}
					index++
				}
				if index != len(want) {
					t.Fatalf("projection emitted %d intervals, want %d", index, len(want))
				}
			})
		}
	}
}

func TestFeedRangeCursorStructuredParity(t *testing.T) {
	fx := manifestFixture(t, "rust/structured-ipv4.iprdb")
	db := mustOpen(t, "rust/structured-ipv4.iprdb")
	defer db.Close()

	for _, feed := range []string{"botnet", "scanner"} {
		t.Run(feed, func(t *testing.T) {
			cur, err := db.FeedRangeCursorV4(feed, RangeDirectionForward)
			if err != nil {
				t.Fatal(err)
			}
			var manifest []manifestRange4
			for _, r := range fx.StructuredRanges {
				manifest = append(manifest, manifestRange4{From: r.From, To: r.To, Feeds: r.Feeds})
			}
			want := coalescedRanges4(manifest, feed, false)
			index := 0
			for {
				got, ok, err := cur.NextRange()
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if index >= len(want) {
					t.Fatalf("projection emitted more intervals than expected")
				}
				w := want[index]
				if got.From != w[0] || got.To != w[1] {
					t.Fatalf("interval %d = %v-%v, want %v-%v", index, got.From, got.To, w[0], w[1])
				}
				index++
			}
			if index != len(want) {
				t.Fatalf("projection emitted %d intervals, want %d", index, len(want))
			}
		})
	}
}

func TestFeedRangeCursorErrors(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()

	if _, err := db.FeedRangeCursorV4("no-such-feed", RangeDirectionForward); err == nil {
		t.Fatal("unknown feed must fail")
	} else if errorAsCode(err) != ErrorNameNotFound {
		t.Fatalf("unknown feed error = %v, want NameNotFound", err)
	}

	direct := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer direct.Close()
	if _, err := direct.FeedRangeCursorV4("feed-000", RangeDirectionForward); err == nil {
		t.Fatal("feed cursor on a direct database must fail")
	} else if errorAsCode(err) != ErrorWrongValueKind {
		t.Fatalf("direct db error = %v, want WrongValueKind", err)
	}
}

func feedNames(ranges []manifestRange4, addr IPv4) []string {
	for _, r := range ranges {
		if parseV4(r.From) <= addr && addr <= parseV4(r.To) {
			return r.Feeds
		}
	}
	return nil
}

func TestMembershipQueryParity(t *testing.T) {
	fx := manifestFixture(t, "rust/membership-ipv4.iprdb")
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()

	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}

	// Point matches: every manifest range start/middle/end and one
	// out-of-range address.
	probe := func(addr IPv4) {
		report, err := q.MatchingFeedsV4(addr, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var manifest []manifestRange4
		for _, r := range fx.MembershipRanges {
			manifest = append(manifest, manifestRange4{From: r.From, To: r.To, Feeds: r.Feeds})
		}
		want := feedNames(manifest, addr)
		if report.MatchingFeedCount != uint64(len(want)) {
			t.Fatalf("match count at %v = %d, want %d", addr, report.MatchingFeedCount, len(want))
		}
		var got []string
		_, err = q.MatchingFeedsV4(addr, func(name string) error {
			got = append(got, name)
			return nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("emitted %d names at %v, want %d", len(got), addr, len(want))
		}
		seen := map[string]bool{}
		for _, name := range got {
			seen[name] = true
		}
		for _, name := range want {
			if !seen[name] {
				t.Fatalf("missing name %q at %v", name, addr)
			}
		}
	}
	for _, r := range fx.MembershipRanges {
		probe(parseV4(r.From))
		probe(parseV4(r.To))
	}
	probe(parseV4("10.0.2.0")) // beyond every range
	probe(IPv4(0))             // below the first range

	// The sink error passes through unchanged.
	sentinel := errors.New("sink stop")
	_, err = q.MatchingFeedsV4(parseV4("10.0.0.5"), func(name string) error {
		return sentinel
	}, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("sink error = %v, want sentinel", err)
	}

	// AllFeeds scope: every manifest feed, ascending index order.
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scope.FeedCount() != len(fx.Feeds) {
		t.Fatalf("scope count = %d, want %d", scope.FeedCount(), len(fx.Feeds))
	}
	entries := scope.Feeds()
	if len(entries) != len(fx.Feeds) {
		t.Fatalf("scope entries = %d, want %d", len(entries), len(fx.Feeds))
	}
	for i, want := range fx.Feeds {
		if entries[i].Name != want.Name || entries[i].Index != want.Index {
			t.Fatalf("scope entry %d = %+v, want %+v", i, entries[i], want)
		}
	}

	// NamedFeeds: caller order is normalized to ascending index order.
	named, err := q.NamedFeeds([]string{"feed-001", "feed-000"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries = named.Feeds()
	if len(entries) != 2 || entries[0].Name != "feed-000" || entries[1].Name != "feed-001" {
		t.Fatalf("named scope = %+v", entries)
	}

	// NamedFeeds errors: empty, unknown, duplicate.
	if _, err := q.NamedFeeds(nil, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil); err == nil {
		t.Fatal("empty scope must fail")
	}
	if _, err := q.NamedFeeds([]string{"missing"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil); errorAsCode(err) != ErrorNameNotFound {
		t.Fatalf("unknown name error = %v, want NameNotFound", err)
	}
	if _, err := q.NamedFeeds([]string{"feed-000", "feed-000"}, MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil); err == nil {
		t.Fatal("duplicate names must fail")
	} else if errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("duplicate names error = %v, want InvalidArgument", err)
	}

	// Budget enforcement.
	if _, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 0}, nil); errorAsCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("zero budget error = %v, want InsufficientResourceBudget", err)
	}

	// Cancellation: a pre-cancelled token fails every bounded op.
	cancel := NewCancellationToken()
	cancel.Cancel()
	if _, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, cancel); errorAsCode(err) != ErrorCancelled {
		t.Fatalf("cancelled scope error = %v, want Cancelled", err)
	}
	if _, err := q.MatchingFeedsV4(parseV4("10.0.0.5"), nil, cancel); errorAsCode(err) != ErrorCancelled {
		t.Fatalf("cancelled match error = %v, want Cancelled", err)
	}

	// Cancellation is observable mid-scan: cancel after the first match.
	steps := 0
	_, err = q.MatchingFeedsV4(parseV4("10.0.0.5"), func(name string) error {
		steps++
		if steps == 1 {
			cancel.Cancel()
		}
		return nil
	}, cancel)
	if errorAsCode(err) != ErrorCancelled {
		t.Fatalf("mid-match cancel error = %v, want Cancelled", err)
	}
}

func TestMembershipQueryWrongKind(t *testing.T) {
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	defer db.Close()
	if _, err := db.MembershipQuery(); errorAsCode(err) != ErrorWrongValueKind {
		t.Fatalf("MembershipQuery on direct db error = %v, want WrongValueKind", err)
	}
}

func TestLogicalCursorsClosedReader(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := db.FeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cur.NextFeed(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("cursor after close error = %v, want WrongState", err)
	}
	if _, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("query after close error = %v, want WrongState", err)
	}
	if _, err := db.FeedCursor(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("new cursor after close error = %v, want WrongState", err)
	}
}

// TestEmptyDatabaseCursors pins the Rust empty-tree contract: a freshly
// created database (all roots zero) opens cursors that iterate zero
// records instead of refusing with corruption (fixed_tree
// cursor:unpositioned finished = root == 0; feed_catalog FeedCursor
// finished = catalog_index_root == 0). The kind gates from
// generation.rs still apply: direct cursors need a direct database,
// feed cursors need a membership database, so each half uses the kind
// it drives.
func TestEmptyDatabaseCursors(t *testing.T) {
	requireFileCreation(t)
	dir := t.TempDir()

	// Direct range cursors in both directions are empty, not corrupt.
	directPath := filepath.Join(dir, "empty-direct.iprdb")
	if _, err := Create(directPath, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal("create direct:", err)
	}
	directDB, err := OpenImmutable(directPath)
	if err != nil {
		t.Fatal("open direct:", err)
	}
	for _, direction := range []RangeDirection{RangeDirectionForward, RangeDirectionBackward} {
		cur, err := directDB.DirectCursorV4(direction)
		if err != nil {
			t.Fatalf("direct cursor %v on an empty database: %v", direction, err)
		}
		if _, ok, err := cur.NextRange(); err != nil || ok {
			t.Fatalf("direct cursor %v on an empty database: ok=%v err=%v", direction, ok, err)
		}
	}
	if err := directDB.Close(); err != nil {
		t.Fatal("close direct:", err)
	}

	// The membership catalog feed cursor is empty, not corrupt.
	path := filepath.Join(dir, "empty-membership.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal("create:", err)
	}
	db, err := OpenImmutable(path)
	if err != nil {
		t.Fatal("open:", err)
	}
	defer db.Close()

	fc, err := db.FeedCursor()
	if err != nil {
		t.Fatal("feed cursor:", err)
	}
	if _, ok, err := fc.NextFeed(); err != nil || ok {
		t.Fatalf("feed cursor on an empty database: ok=%v err=%v", ok, err)
	}

	// The all-feeds scope resolves zero feeds.
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal("membership query:", err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("all feeds:", err)
	}
	if n := scope.FeedCount(); n != 0 {
		t.Fatalf("all feeds on an empty database: %d feeds, want 0", n)
	}
}
