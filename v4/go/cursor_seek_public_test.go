package iprangedb

// Public cursor seek tests (Rust db.rs / feed_catalog.rs seek surface
// parity): FeedCursor.SeekByIndex at-or-after repositioning with the
// full-sweep count health check disabled after a seek, and the
// named-feed projection Seek used by the CLI paging hot path. The
// page-equivalence tests re-implement the CLI checkpoint loop (one
// cursor per page seeked to the retained checkpoint) and require the
// paged stream to reproduce an unbounded sweep exactly.

import (
	"fmt"
	"testing"
)

// TestFeedCursorSeekByIndex pins the public at-or-after semantics on a
// real membership fixture (70 feeds, feed-000..feed-069): mid-catalog
// continuation, exhaustion past the last index without the
// incomplete-count corruption (Rust seeked flag), and a full-sweep
// restart via seek(0).
func TestFeedCursorSeekByIndex(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	defer db.Close()

	cur, err := db.FeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := cur.NextFeed()
	if err != nil || !ok || first.Index != 0 {
		t.Fatalf("first feed = (%+v, %v, %v), want index 0", first, ok, err)
	}

	if err := cur.SeekByIndex(30); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := cur.NextFeed()
	if err != nil || !ok || entry.Index != 30 {
		t.Fatalf("seek(30) = (%+v, %v, %v), want index 30", entry, ok, err)
	}
	next, ok, err := cur.NextFeed()
	if err != nil || !ok || next.Index != 31 {
		t.Fatalf("seek(30) next = (%+v, %v, %v), want index 31", next, ok, err)
	}

	// Seek past the last index finishes the cursor; the emitted count
	// was reset, so the full-sweep count health check does not apply.
	if err := cur.SeekByIndex(70); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cur.NextFeed(); err != nil || ok {
		t.Fatalf("seek(70) next: ok=%v err=%v", ok, err)
	}

	// Ending a sweep one entry after a mid-catalog seek is clean too.
	if err := cur.SeekByIndex(69); err != nil {
		t.Fatal(err)
	}
	if entry, ok, err := cur.NextFeed(); err != nil || !ok || entry.Index != 69 {
		t.Fatalf("seek(69) = (%+v, %v, %v), want index 69", entry, ok, err)
	}
	if _, ok, err := cur.NextFeed(); err != nil || ok {
		t.Fatalf("exhaustion after seek(69): ok=%v err=%v", ok, err)
	}

	// Seek to 0 restarts a complete sweep of all 70 feeds.
	if err := cur.SeekByIndex(0); err != nil {
		t.Fatal(err)
	}
	for index := uint32(0); index < 70; index++ {
		entry, ok, err := cur.NextFeed()
		if err != nil || !ok {
			t.Fatalf("restart sweep %d: ok=%v err=%v", index, ok, err)
		}
		if entry.Index != index {
			t.Fatalf("restart sweep %d = index %d", index, entry.Index)
		}
	}
	if _, ok, err := cur.NextFeed(); err != nil || ok {
		t.Fatalf("restart sweep tail: ok=%v err=%v", ok, err)
	}
}

// TestFeedCursorSeekByIndexClosedReader pins the handle-open guard:
// every call re-validates the reader state, so a seek on a cursor whose
// reader closed reports WrongState (Rust require_owner parity).
func TestFeedCursorSeekByIndexClosedReader(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	cur, err := db.FeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cur.SeekByIndex(10); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("seek after close = %v, want WrongState", err)
	}
}

// TestFeedRangeCursorV4SeekPageEquivalence re-implements the CLI
// ranges.next checkpoint loop for a feed view (one projection cursor
// per page, seeked once to the retained checkpoint) and requires the
// paged stream to equal an unbounded sweep exactly (Rust
// feed_range_cursor seek + cursors.rs open_feed_page parity). The
// immutable-feed fixture carries three disjoint feed-000 intervals, so
// every page boundary crosses a real gap.
func TestFeedRangeCursorV4SeekPageEquivalence(t *testing.T) {
	db := mustOpen(t, "go/immutable-feed-ipv4.iprdb")
	defer db.Close()

	for _, backward := range []bool{false, true} {
		for _, batch := range []int{1, 2, 3} {
			direction := RangeDirectionForward
			if backward {
				direction = RangeDirectionBackward
			}
			t.Run(seekPageName(backward, batch), func(t *testing.T) {
				// Unbounded reference sweep.
				ref, err := db.FeedRangeCursorV4("feed-000", direction)
				if err != nil {
					t.Fatal(err)
				}
				var want []AddressRange4
				for {
					rng, ok, err := ref.NextRange()
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						break
					}
					want = append(want, rng)
				}
				if len(want) != 3 {
					t.Fatalf("reference sweep emitted %d intervals, want 3", len(want))
				}

				// Paged sweep with retained checkpoints (CLI
				// nextV4Point semantics: to+1 forward, from-1
				// backward, nil at the family edge).
				var paged []AddressRange4
				var last *IPv4
				for {
					cur, err := db.FeedRangeCursorV4("feed-000", direction)
					if err != nil {
						t.Fatal(err)
					}
					if last != nil {
						if err := cur.Seek(*last); err != nil {
							t.Fatal(err)
						}
					}
					count := 0
					for count < batch {
						rng, ok, err := cur.NextRange()
						if err != nil {
							t.Fatal(err)
						}
						if !ok {
							break
						}
						paged = append(paged, rng)
						count++
						if backward {
							if rng.From == 0 {
								last = nil
							} else {
								point := rng.From - 1
								last = &point
							}
						} else {
							if rng.To == ^IPv4(0) {
								last = nil
							} else {
								point := rng.To + 1
								last = &point
							}
						}
					}
					if count < batch || last == nil {
						break
					}
				}
				if len(paged) != len(want) {
					t.Fatalf("paged sweep emitted %d intervals, want %d", len(paged), len(want))
				}
				for i := range want {
					if paged[i] != want[i] {
						t.Fatalf("paged interval %d = %v-%v, want %v-%v", i, paged[i].From, paged[i].To, want[i].From, want[i].To)
					}
				}
			})
		}
	}
}

func seekPageName(backward bool, batch int) string {
	name := "forward"
	if backward {
		name = "backward"
	}
	return fmt.Sprintf("%s-batch-%d", name, batch)
}

// TestFeedRangeCursorV4SeekStructured pins projection Seek on the
// structured value kind: each seek repositions the projection without
// resplitting the coalesced intervals or losing the membership
// resolution (Rust ProjectionState::seek parity).
func TestFeedRangeCursorV4SeekStructured(t *testing.T) {
	db := mustOpen(t, "rust/structured-ipv4.iprdb")
	defer db.Close()

	cur, err := db.FeedRangeCursorV4("botnet", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	// Seek inside the first interval: the containing interval is
	// emitted, then the second, disjoint interval.
	if err := cur.Seek(parseV4("10.1.0.50")); err != nil {
		t.Fatal(err)
	}
	first, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek inside: ok=%v err=%v", ok, err)
	}
	if first.From != parseV4("10.1.0.0") || first.To != parseV4("10.1.0.63") {
		t.Fatalf("seek inside = %v-%v, want 10.1.0.0-10.1.0.63", first.From, first.To)
	}
	second, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek continuation: ok=%v err=%v", ok, err)
	}
	if second.From != parseV4("10.1.0.128") || second.To != parseV4("10.1.0.255") {
		t.Fatalf("seek continuation = %v-%v, want 10.1.0.128-10.1.0.255", second.From, second.To)
	}
	if _, ok, err := cur.NextRange(); err != nil || ok {
		t.Fatalf("seek tail: ok=%v err=%v", ok, err)
	}

	// Seek into the gap between the two intervals: the next interval
	// in the cursor's direction is emitted.
	if err := cur.Seek(parseV4("10.1.0.100")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek gap: ok=%v err=%v", ok, err)
	}
	if got.From != parseV4("10.1.0.128") || got.To != parseV4("10.1.0.255") {
		t.Fatalf("seek gap = %v-%v, want 10.1.0.128-10.1.0.255", got.From, got.To)
	}
}

// TestFeedRangeCursorV6Seek pins the public IPv6 projection seek on
// the membership fixture: at-or-after repositioning inside the
// coalesced global interval and the closed-reader guard.
func TestFeedRangeCursorV6Seek(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer db.Close()

	cur, err := db.FeedRangeCursorV6("global", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	target := IPv6FromHalves(0x2001, 0x0db8000000000000)
	if err := cur.Seek(target); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cur.NextRange()
	if err != nil || !ok {
		t.Fatalf("seek inside: ok=%v err=%v", ok, err)
	}
	if got.FromHi != 0 || got.FromLo != 0 || got.ToHi != ^uint64(0) || got.ToLo != ^uint64(0) {
		t.Fatalf("seek inside = %x:%x-%x:%x, want ::-ffff:...", got.FromHi, got.FromLo, got.ToHi, got.ToLo)
	}
	if _, ok, err := cur.NextRange(); err != nil || ok {
		t.Fatalf("seek tail: ok=%v err=%v", ok, err)
	}

	// Repeatable on an exhausted cursor.
	if err := cur.Seek(IPv6FromHalves(0, 1)); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := cur.NextRange(); err != nil || !ok || got.FromLo != 0 {
		t.Fatalf("restart seek = (%+v, %v, %v)", got, ok, err)
	}

	// Closed-reader guard.
	db6 := mustOpen(t, "rust/membership-ipv6.iprdb")
	cur6, err := db6.FeedRangeCursorV6("global", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	if err := db6.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cur6.Seek(IPv6FromHalves(1, 2)); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("v6 seek after close = %v, want WrongState", err)
	}
}
