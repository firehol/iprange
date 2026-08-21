//go:build v4work

package reader

// Necessary-work pins for the structured range cursor (slice A of chunk
// 3b-3) on the frozen structured-ipv4 fixture: one range leaf (page 11)
// and one level-0 structure record page (page 9). The cursor counts one
// leaf validation and one consumed range per record like the direct
// cursor (Rust CursorState::next), and each structure resolution counts
// one tree lookup plus one decode through the shared lookupStructureID
// path (Rust by_id -> table::inspect).

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestWorkNetworkEnrichmentV1Cursor4(t *testing.T) {
	path := copyFixture(t, "structured-ipv4.iprdb", "work-structured-cursor.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	c, err := r.NewNetworkEnrichmentV1Cursor4()
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
	want := work.Snapshot{
		TreeLookups:      4, // one structure-table descent per record
		PagesVisited:     6, // leaf descent + first openLeaf + 4 structure pages
		PagesParsed:      6,
		LeafValidations:  4,
		StructureDecodes: 4,
		RangesConsumed:   4,
	}
	if got := work.Read(); got != want {
		t.Fatalf("structured cursor counters = %+v, want %+v", got, want)
	}

	// A second cursor walks the same tree again: the leaf is re-read on
	// its first Next like the direct cursor.
	work.Reset()
	c2, err := r.NewNetworkEnrichmentV1Cursor4()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c2.Next(); err != nil || !ok {
		t.Fatalf("first next: ok=%v err=%v", ok, err)
	}
	want = work.Snapshot{
		TreeLookups:      1,
		PagesVisited:     3, // descent + openLeaf re-read + structure page
		PagesParsed:      3,
		LeafValidations:  1,
		StructureDecodes: 1,
		RangesConsumed:   1,
	}
	if got := work.Read(); got != want {
		t.Fatalf("single record counters = %+v, want %+v", got, want)
	}
}

func TestWorkNetworkEnrichmentV1Cursor6(t *testing.T) {
	r, err := OpenImmutable(buildStructuredV6CursorDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	work.Reset()
	c, err := r.NewNetworkEnrichmentV1Cursor6()
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
	want := work.Snapshot{
		TreeLookups:      2, // one structure-table descent per record
		PagesVisited:     4, // leaf descent + first openLeaf + 2 structure pages
		PagesParsed:      4,
		LeafValidations:  2,
		StructureDecodes: 2,
		RangesConsumed:   2,
	}
	if got := work.Read(); got != want {
		t.Fatalf("ipv6 cursor counters = %+v, want %+v", got, want)
	}
}
