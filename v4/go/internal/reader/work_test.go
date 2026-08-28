//go:build v4work

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// Necessary-work pins: with -tags v4work the reader increments the
// test-only counters at the authoritative points (search.go probes, page
// visits/parses, tree descents, selected-leaf decodes, word reads,
// structure decodes). These tests pin the exact work of deterministic
// lookups on the synthetic multi-level range tree, the synthetic
// blob-backed membership database, and the committed fixtures, so a change
// that adds or removes hot-path work is visible (mirroring the Rust
// work.rs pins).

func expectCounters(t *testing.T, want work.Snapshot) {
	t.Helper()
	got := work.Read()
	if got != want {
		t.Fatalf("work counters = %+v, want %+v", got, want)
	}
}

func TestWorkRangeLookupMultiLevel(t *testing.T) {
	path := buildMultiLevelDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Target on the first record of leaf 5 of 4 (branch has 4 entries).
	// Branch search: 4 slots -> 2 key probes; leaf search: 290 slots -> 9
	// key probes; selected leaf decode once.
	work.Reset()
	v, found, err := r.LookupDirect4(581000)
	if err != nil || !found || v == 0 {
		t.Fatalf("lookup: v=%d found=%v err=%v", v, found, err)
	}
	expectCounters(t, work.Snapshot{
		TreeLookups: 1, TreeDescents: 1,
		PagesVisited: 2, PagesParsed: 2,
		KeyProbes: 2 + 9, LeafValidations: 1,
		// The family-typed fixed probe charges the shared cell and
		// slot-read counters per probe plus the two selected-record
		// cells (Rust FixedSearch accounting).
		CellProbes: 2 + 9 + 2, SlotReads: 2 + 9 + 2,
	})

	// A miss below every key walks only the branch (3 probes, no descent).
	work.Reset()
	if _, found, err := r.LookupDirect4(0); err != nil || found {
		t.Fatalf("miss: found=%v err=%v", found, err)
	}
	expectCounters(t, work.Snapshot{
		TreeLookups:  1,
		PagesVisited: 1, PagesParsed: 1,
		KeyProbes:  3,
		CellProbes: 3, SlotReads: 3,
	})
}

func TestWorkMembershipBlobWords(t *testing.T) {
	path := buildBlobDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Range leaf (1 record, 1 probe) + ID leaf (1 record, 1 probe) + blob
	// branch (2 entries, 1 probe, 1 descent) + blob leaf decode + 1 word:
	// 3 tree lookups, 4 pages visited, 3 key probes, 3 leaf validations,
	// 1 word read.
	work.Reset()
	view, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	word, ok, err := view.Word(0)
	if err != nil || !ok || word == 0 {
		t.Fatalf("word: ok=%v err=%v", ok, err)
	}
	expectCounters(t, work.Snapshot{
		TreeLookups: 3, TreeDescents: 1,
		PagesVisited: 4, PagesParsed: 4,
		KeyProbes: 1 + 1 + 2, LeafValidations: 3,
		WordReads:  1,
		CellProbes: 2, SlotReads: 2,
	})
}

func TestWorkStructureLookup(t *testing.T) {
	path := copyFixture(t, "structured-ipv4.iprdb", "work-structured.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// The direct range lookup is one tree (1 leaf), the structure table is
	// one directory page plus one record page for the fixture's id space.
	// Both decodes are counted.
	work.Reset()
	v, found, err := r.LookupNetworkEnrichmentV14(0x0a010001)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	s := work.Read()
	if s.TreeLookups != 2 {
		t.Fatalf("tree lookups = %d, want 2 (range + structure)", s.TreeLookups)
	}
	if s.StructureDecodes != 1 {
		t.Fatalf("structure decodes = %d, want 1", s.StructureDecodes)
	}
	if s.LeafValidations != 1 {
		t.Fatalf("leaf validations = %d, want 1", s.LeafValidations)
	}
	if s.PagesVisited != s.PagesParsed {
		t.Fatalf("page visits %d != page parses %d", s.PagesVisited, s.PagesParsed)
	}
	if s.TreeDescents == 0 && s.PagesVisited != 2 {
		t.Fatalf("unexpected structure walk: %+v", s)
	}
	_ = v
}
