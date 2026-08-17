//go:build v4work

// Necessary-work pins for the free bitmap (Rust free_bitmap_tests.rs
// bit map probe assertions).

package bitmap

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

func TestSamePathSetFreeUsesExactlyTwoBitmapProbes(t *testing.T) {
	m := newBitmapMemoryStore(2)
	root := uint32(0)
	if err := SetFree(m, &root, 100_000, 40_000, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	if err := m.sealCurrent(); err != nil {
		t.Fatal(err)
	}
	m.txn = 3
	if err := SetFree(m, &root, 100_000, 40_001, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}

	work.Reset()
	if err := SetFree(m, &root, 100_000, 40_002, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	snap := work.Read()
	if snap.BitmapProbes != 2 {
		t.Fatalf("bitmap probes = %d, want 2", snap.BitmapProbes)
	}
	if snap.PagesCopied != 0 || snap.PagesCreated != 0 {
		t.Fatalf("same-path set_free copied/created pages: %d/%d", snap.PagesCopied, snap.PagesCreated)
	}
}
