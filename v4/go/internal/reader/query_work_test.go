//go:build v4work

package reader

// Necessary-work pins for the milestone-3 chunk-1 logical read SDK
// (ordered cursors, catalog feed cursor, named-feed projections,
// membership queries) on the committed membership-ipv4 fixture. The
// fixture is frozen content, so the exact counter deltas below are
// stable; a change that adds or removes hot-path work is visible.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestWorkFeedCursorCatalog(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "work-feedcursor.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	c, err := r.NewFeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		_, ok, err := c.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		n++
	}
	if n != 70 {
		t.Fatalf("catalog emitted %d feeds, want 70", n)
	}
	want := work.Snapshot{PagesVisited: 71, PagesParsed: 71, LeafValidations: 70}
	if got := work.Read(); got != want {
		t.Fatalf("feed cursor counters = %+v, want %+v", got, want)
	}
}

func TestWorkDirectCursorScanAndSeek(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "work-dircursor.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	c, err := r.NewDirectCursor4(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok, err := c.Next(); err != nil {
			t.Fatal(err)
		} else if !ok {
			break
		}
	}
	want := work.Snapshot{PagesVisited: 4, PagesParsed: 4, LeafValidations: 3, RangesConsumed: 3}
	if got := work.Read(); got != want {
		t.Fatalf("direct cursor counters = %+v, want %+v", got, want)
	}

	work.Reset()
	c2, err := r.NewDirectCursor4(RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Seek(0x0a000080); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c2.Next(); err != nil || !ok {
		t.Fatalf("seek next: ok=%v err=%v", ok, err)
	}
	want = work.Snapshot{PagesVisited: 3, PagesParsed: 3, LeafValidations: 2, RangesConsumed: 1}
	if got := work.Read(); got != want {
		t.Fatalf("seek counters = %+v, want %+v", got, want)
	}
}

func TestWorkMatchingFeeds(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "work-matching.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	n, err := r.MatchingFeeds4(0x0a000005, func([]byte) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("matching count = %d, want 5", n)
	}
	want := work.Snapshot{
		TreeLookups: 7, PagesVisited: 7, PagesParsed: 7, KeyProbes: 4,
		LeafValidations: 7, WordReads: 2, MembershipDecodes: 1,
	}
	if got := work.Read(); got != want {
		t.Fatalf("matching counters = %+v, want %+v", got, want)
	}

	work.Reset()
	n, err = r.MatchingFeeds4(0x0a000200, func([]byte) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("absent matching count = %d, want 0", n)
	}
	want = work.Snapshot{TreeLookups: 1, PagesVisited: 1, PagesParsed: 1, KeyProbes: 2, LeafValidations: 1}
	if got := work.Read(); got != want {
		t.Fatalf("absent matching counters = %+v, want %+v", got, want)
	}
}

func TestWorkFeedProjection(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "work-projection.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	fp, err := r.NewFeedRangeProjection4(0, RangeForward)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		if _, ok, err := fp.Next(); err != nil {
			t.Fatal(err)
		} else if !ok {
			break
		}
		n++
	}
	if n != 1 {
		t.Fatalf("projection emitted %d intervals, want 1", n)
	}
	want := work.Snapshot{
		TreeLookups: 3, PagesVisited: 7, PagesParsed: 7, KeyProbes: 6,
		LeafValidations: 6, WordReads: 3, RangesConsumed: 3,
	}
	if got := work.Read(); got != want {
		t.Fatalf("projection counters = %+v, want %+v", got, want)
	}
}
