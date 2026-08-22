//go:build v4work

// Necessary-work pins for the gap machinery (Rust work.rs edge_path_check
// and the fixed_tree gap counters).

package tree

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestGapEdgeVerificationCounts mirrors the Rust edge_path_check counter:
// a direction-stamped edge is never re-verified, a fresh edge is verified
// exactly once.
func TestGapEdgeVerificationCounts(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 61; key++ {
		if _, _, err := Insert(wideCodec{}, m, &root, wideRecord(uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	cached := RootEdge(root)
	if _, err := InsertIfEdgeGap(wideCodec{}, m, &root, wideRecord(200), &cached, EdgeLast, true, acceptGap[wideLeaf]{}); err != nil {
		t.Fatal(err)
	}
	work.Reset()
	if _, err := InsertIfEdgeGap(wideCodec{}, m, &root, wideRecord(201), &cached, EdgeLast, true, acceptGap[wideLeaf]{}); err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.EdgePathChecks != 0 {
		t.Fatalf("reused edge verified again: %d edge path checks", snap.EdgePathChecks)
	}
	// A fresh edge over a single-leaf tree verifies its position exactly
	// once (a root edge is only valid while the tree is one leaf).
	fresh := newMemoryStore()
	freshRoot := uint32(0)
	for key := 0; key < 61; key++ {
		if _, _, err := Insert(wideCodec{}, fresh, &freshRoot, wideRecord(uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	newEdge := RootEdge(freshRoot)
	if _, err := InsertIfEdgeGap(wideCodec{}, fresh, &freshRoot, wideRecord(200), &newEdge, EdgeLast, true, acceptGap[wideLeaf]{}); err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.EdgePathChecks != 1 {
		t.Fatalf("fresh edge path checks = %d, want 1", snap.EdgePathChecks)
	}
}
