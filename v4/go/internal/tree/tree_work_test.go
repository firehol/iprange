//go:build v4work

// Necessary-work pins for the fixed tree (Rust fixed_tree_tests.rs
// work::measure assertions).

package tree

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestOneShotReadsHaveNoHiddenScans(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key+10)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	header, err := parse(u32Codec{}, m.pages[root][:], m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	pathPages := uint64(header.Level) + 1

	work.Reset()
	if _, _, err := Predecessor(u32Codec{}, m, root, u32Key(501)); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.TreeLookups != 1 {
		t.Fatalf("predecessor tree lookups = %d, want 1", snap.TreeLookups)
	}
	if snap.TreeDescents+1 != pathPages {
		t.Fatalf("predecessor tree descents = %d, want %d", snap.TreeDescents, pathPages-1)
	}

	work.Reset()
	if _, _, err := AtOrAfter(u32Codec{}, m, root, u32Key(501)); err != nil {
		t.Fatal(err)
	}
	snap = work.Read()
	if snap.TreeLookups != 1 {
		t.Fatalf("at_or_after tree lookups = %d, want 1", snap.TreeLookups)
	}
	if snap.TreeDescents+1 != pathPages {
		t.Fatalf("at_or_after tree descents = %d, want %d", snap.TreeDescents, pathPages-1)
	}

	// A seek that must advance into the next leaf follows the sibling
	// subtree. Use an evens-only tree so key 811 lands at the end of the
	// first leaf and must continue into the second.
	evenStore := newMemoryStore()
	evenRoot := uint32(0)
	for key := 0; key < 1000; key += 2 {
		if _, _, err := Insert(u32Codec{}, evenStore, &evenRoot, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	work.Reset()
	value, found, err := AtOrAfter(u32Codec{}, evenStore, evenRoot, u32Key(811))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value.key != 812 {
		t.Fatalf("at_or_after(811) = %#v, want key 812", value)
	}
	snap = work.Read()
	if snap.TreeLookups != 1 {
		t.Fatalf("advancing at_or_after tree lookups = %d, want 1", snap.TreeLookups)
	}
}

func TestLowerBoundReusesItsFinalProbe(t *testing.T) {
	header := &Header{ItemCount: 8}
	calls := 0
	work.Reset()
	result, exact, err := lowerBoundBy(header, u32Key(5), true, func(index int) (Key, error) {
		calls++
		return u32Key(uint32(index)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != 5 || !exact {
		t.Fatalf("lower_bound_by(5) = (%d, %v), want (5, true)", result, exact)
	}
	if calls != 3 {
		t.Fatalf("key probes called %d times, want 3", calls)
	}
	snap := work.Read()
	if snap.KeyProbes != 3 {
		t.Fatalf("key probes = %d, want 3", snap.KeyProbes)
	}
}

func TestFixedReplacementUsesOneCapacityProbeAndNoSlotScan(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	work.Reset()
	_, changed, err := Insert(u32Codec{}, m, &root, u32Record(500, 7), RetiredPages{})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("replacement reported a change")
	}
	snap := work.Read()
	if snap.EditFitProbes != 1 {
		t.Fatalf("edit fit probes = %d, want 1", snap.EditFitProbes)
	}
	if snap.SlotScanSteps != 0 {
		t.Fatalf("slot scan steps = %d, want 0", snap.SlotScanSteps)
	}
	if got, ok, err := lookupU32(m, root, 500); err != nil {
		t.Fatal(err)
	} else if !ok || got != 7 {
		t.Fatalf("key 500 after work-pinned replace: (%d, %v)", got, ok)
	}
}

func TestKnownDeletionUsesOneTreeLookup(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 900; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	work.Reset()
	if _, err := DeleteExisting(u32Codec{}, m, &root, u32Key(451), RetiredPages{}); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.TreeLookups != 1 {
		t.Fatalf("delete tree lookups = %d, want 1", snap.TreeLookups)
	}
	if _, ok, err := lookupU32(m, root, 451); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("key 451 survived deletion")
	}
}

// TestLowerBoundPrefixMatchesReference pins the specialized prefix probe
// against the reference closure search across a dense key sweep: both
// paths return the same position and exact flag, and the single
// final-probe reuse stays (Rust lower_bound reuses the last probe when
// it landed on the answer).
func TestLowerBoundPrefixMatchesReference(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	page := m.pages[root][:]
	header, err := parse(u32Codec{}, page, m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for probe := 0; probe <= 1005; probe++ {
		for _, insertion := range []bool{true, false} {
			wantIndex, wantExact, err := lowerBoundBy(&header, u32Key(uint32(probe)), insertion, func(index int) (Key, error) {
				return keyAt(u32Codec{}, page, &header, index)
			})
			if err != nil {
				t.Fatal(err)
			}
			gotIndex, gotExact, err := lowerBound(u32Codec{}, page, &header, u32Key(uint32(probe)), insertion)
			if err != nil {
				t.Fatalf("lowerBound(%d, insertion=%v): %v", probe, insertion, err)
			}
			if gotIndex != wantIndex || gotExact != wantExact {
				t.Fatalf("lowerBound(%d, insertion=%v) = (%d, %v), want (%d, %v)",
					probe, insertion, gotIndex, gotExact, wantIndex, wantExact)
			}
		}
	}
}

// TestLowerBoundPrefixReusesItsFinalProbe pins the work-counter probe
// count of the specialized prefix loop: a 16-record leaf search must
// charge exactly the binary-search probes plus the reuse of the final
// probe for the exact-match check (Rust lower_bound reuse).
func TestLowerBoundPrefixReusesItsFinalProbe(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 16; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	page := m.pages[root][:]
	header, err := parse(u32Codec{}, page, m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	work.Reset()
	index, exists, err := lowerBound(u32Codec{}, page, &header, u32Key(9), true)
	if err != nil {
		t.Fatal(err)
	}
	if index != 9 || !exists {
		t.Fatalf("lower_bound(9) = (%d, %v), want (9, true)", index, exists)
	}
	snap := work.Read()
	if snap.KeyProbes != 4 {
		t.Fatalf("key probes = %d, want 4 (3 search probes + 1 final probe)", snap.KeyProbes)
	}
}
