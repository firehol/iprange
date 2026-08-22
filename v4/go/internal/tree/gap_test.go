// Gap machinery tests mirroring the Rust fixed_tree_tests.rs gap vectors
// (AcceptGap local insert, cached interior gap) plus the rejection and
// edge entry points.

package tree

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// acceptGap is the Rust AcceptGap: every probe accepts the gap. It is
// intentionally non-generic: the probe interface must never force a
// shape-conversion box on the hot path.
type acceptGap struct{}

func (acceptGap) Previous(bool, []byte) (LocalPrevious, []byte, error) {
	return LocalPreviousAccept, nil, nil
}
func (acceptGap) Next([]byte) (LocalNext, []byte, error) {
	return LocalNextAccept, nil, nil
}

// privateLocalInsertInspectsAndUpdatesWithoutCopyingTheLeaf mirrors the
// Rust private_local_insert_inspects_and_updates_without_copying_the_leaf:
// a local gap insert must descend (inspect) without copying any page and
// apply exactly one update.
func TestPrivateLocalInsertInspectsAndUpdatesWithoutCopyingTheLeaf(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key*2), uint32(key*2)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	m.reads = 0
	m.inspections = 0
	m.writes = 0
	m.updates = 0

	retired := RetiredPages{}
	retired, result, err := InsertIfLocalGap(u32Codec{}, m, &root, u32Record(501, 7), retired, acceptGap{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Inserted {
		t.Fatalf("local gap insert was rejected: %#v", result.Reject)
	}
	if retired.Len() != 0 {
		t.Fatalf("local gap insert retired %v", retired.Slice())
	}
	if m.inspections != 2 {
		t.Fatalf("local gap insert inspections = %d, want 2", m.inspections)
	}
	if m.reads != 0 {
		t.Fatalf("local gap insert reads = %d, want 0", m.reads)
	}
	if m.updates != 1 {
		t.Fatalf("local gap insert updates = %d, want 1", m.updates)
	}
	if m.writes != 0 {
		t.Fatalf("local gap insert writes = %d, want 0", m.writes)
	}
	if value, found, err := lookupU32(m, root, 501); err != nil {
		t.Fatal(err)
	} else if !found || value != 7 {
		t.Fatalf("lookup(501) = %d, %v; want 7, true", value, found)
	}
}

// TestCachedLeafInsertAcceptsOnlyAValidPrivateInteriorGap mirrors the Rust
// cached_leaf_insert test: the cached leaf must be private and the
// candidate key must land strictly inside the leaf.
func TestCachedLeafInsertAcceptsOnlyAValidPrivateInteriorGap(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for _, key := range []uint32{0, 2, 4} {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(key, key), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := InsertIfCachedInteriorGap(u32Codec{}, m, root, u32Record(3, 3), acceptGap{})
	if err != nil {
		t.Fatal(err)
	}
	if result != CachedInsertInserted {
		t.Fatalf("cached interior gap = %v, want inserted", result)
	}
	if value, found, err := lookupU32(m, root, 3); err != nil {
		t.Fatal(err)
	} else if !found || value != 3 {
		t.Fatalf("lookup(3) = %d, %v; want 3, true", value, found)
	}

	if result, err := InsertIfCachedInteriorGap(u32Codec{}, m, root, u32Record(5, 5), acceptGap{}); err != nil {
		t.Fatal(err)
	} else if result != CachedInsertMiss {
		t.Fatalf("cached interior gap at the leaf edge = %v, want miss", result)
	}

	m.targetTxn = 2
	if _, err := InsertIfCachedInteriorGap(u32Codec{}, m, root, u32Record(1, 1), acceptGap{}); err == nil {
		t.Fatal("cached interior gap accepted a committed leaf")
	}
	m.targetTxn = 1

	// Corrupt the first slot of the leaf: the probe must fail closed.
	for i := format.SlottedHeaderSize; i < format.SlottedHeaderSize+2; i++ {
		m.pages[root][i] = 0
	}
	if _, err := InsertIfCachedInteriorGap(u32Codec{}, m, root, u32Record(1, 1), acceptGap{}); err == nil {
		t.Fatal("cached interior gap accepted a corrupted leaf")
	}
}

// TestInsertRejectedGapCompletesALocalInsert covers the reject-completion
// path: a bounded gap probe that returns a rejection completes the
// insertion at the same target.
func TestInsertRejectedGapCompletesALocalInsert(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 100; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key*2), uint32(key*2)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	retired := RetiredPages{}
	// The probe with a full-tree-blocking gap rejects; then the caller
	// proves the external sides and completes the insert.
	var gap blockingGap
	retired, result, err := InsertIfLocalGap(u32Codec{}, m, &root, u32Record(501, 7), retired, &gap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted {
		t.Fatal("blocking gap accepted the local insert")
	}
	if !result.Rejected {
		t.Fatal("blocking gap returned no rejection")
	}
	position, fits, err := InsertRejectedGap(u32Codec{}, m, &root, u32Record(501, 7), result.Reject)
	if err != nil {
		t.Fatal(err)
	}
	if !fits {
		t.Fatal("rejected gap completion did not fit locally")
	}
	_ = position
	if value, found, err := lookupU32(m, root, 501); err != nil {
		t.Fatal(err)
	} else if !found || value != 7 {
		t.Fatalf("lookup(501) after completion = %d, %v; want 7, true", value, found)
	}
}

// blockingGap rejects every probe that names an existing neighbor cell and
// accepts absent sides (the LocalGap contract: an absent neighbor can
// never bridge the gap). The reject returns the raw probing cell; the
// generic selector decodes it into the reject value.
type blockingGap struct{}

func (blockingGap) Previous(_ bool, cell []byte) (LocalPrevious, []byte, error) {
	if cell == nil {
		return LocalPreviousAccept, nil, nil
	}
	if _, err := (u32Codec{}).ReadLeaf(cell); err != nil {
		return 0, nil, err
	}
	return LocalPreviousReject, cell, nil
}
func (blockingGap) Next(cell []byte) (LocalNext, []byte, error) {
	if cell == nil {
		return LocalNextAccept, nil, nil
	}
	if _, err := (u32Codec{}).ReadLeaf(cell); err != nil {
		return 0, nil, err
	}
	return LocalNextReject, cell, nil
}

// TestEdgeGapInsertSplitsAndKeepsTheEdge covers the cached edge path: a
// first-edge insert over a full private leaf splits the leaf and keeps the
// new record on the left edge.
func TestEdgeGapInsertSplitsAndKeepsTheEdge(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	// Fill one leaf with wide 64-byte records: a leaf fits at most
	// (4096-32-2)/66 ≈ 61 records.
	for key := 0; key < 61; key++ {
		if _, _, err := Insert(wideCodec{}, m, &root, wideRecord(uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	cached := RootEdge(root)
	result, err := InsertIfEdgeGap(wideCodec{}, m, &root, wideRecord(200), &cached, EdgeLast, true, acceptGap{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Inserted {
		t.Fatalf("edge gap insert was rejected: %#v", result.Reject)
	}
	if value, found, err := lookupWide(m, root, 200); err != nil {
		t.Fatal(err)
	} else if !found || value != 200 {
		t.Fatalf("lookup(200) = %d, %v; want 200, true", value, found)
	}
	workEdgeCheck(t, m, &root, &cached, EdgeLast)
}

func workEdgeCheck(t *testing.T, m *memoryStore, root *uint32, cached *PrivateEdge, edge Edge) {
	t.Helper()
	result, err := InsertIfEdgeGap(wideCodec{}, m, root, wideRecord(201), cached, edge, true, acceptGap{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Inserted {
		t.Fatalf("reused edge gap insert was rejected: %#v", result.Reject)
	}
}

func lookupWide(m *memoryStore, root uint32, key uint32) (uint64, bool, error) {
	if root == 0 {
		return 0, false, nil
	}
	pageNumber := root
	var expectedLevel uint16
	checkLevel := false
	for {
		var value uint64
		found := false
		child := uint32(0)
		page, err := m.Inspect(pageNumber)
		if err != nil {
			return 0, false, err
		}
		header, err := parse(wideCodec{}, page, m.TargetTxn(), expectedLevel, checkLevel)
		if err != nil {
			return 0, false, err
		}
		if header.Level == 0 {
			index, exists, err := lowerBound(wideCodec{}, page, &header, wideKey(key), true)
			if err != nil {
				return 0, false, err
			}
			if exists {
				cell, err := codecCell(wideCodec{}, page, &header, index)
				if err != nil {
					return 0, false, err
				}
				value = uint64(format.U32(cell[56:]))
				found = true
			}
			return value, found, nil
		}
		index, _, err := lowerBound(wideCodec{}, page, &header, wideKey(key), false)
		if err != nil {
			return 0, false, err
		}
		child, err = branchChild(wideCodec{}, page, &header, index, m.PageLimit())
		if err != nil {
			return 0, false, err
		}
		expectedLevel = header.Level - 1
		checkLevel = true
		pageNumber = child
	}
}
