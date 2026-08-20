// Milestone 3 chunk 3a half of the complete-page ownership evidence: the
// algebra count and compare sweeps must not allocate any Go heap object
// of 4096 bytes or larger while walking the pre-built scopes.
// Complete-page copies would allocate exactly such objects; the algebra
// core models its retained heap with the Rust size_of accounting and
// never transfers mapped bytes into owned buffers.

package iprangedb

import (
	"runtime"
	"testing"
)

func TestNoPageSizedHeapAllocationsAlgebra(t *testing.T) {
	alg, closeFn := algebraV4(t)
	defer closeFn()

	// Warm every path outside the measured window.
	for range 16 {
		if _, err := alg.Count(AlgebraFeedSelectionAll(), nil); err != nil {
			t.Fatal("warm count:", err)
		}
		if _, err := alg.Count(AlgebraFeedSelectionNamed([]string{"feed-000", "feed-001"}), nil); err != nil {
			t.Fatal("warm named count:", err)
		}
		if _, err := alg.Compare(AlgebraFeedSelectionAll(), AlgebraFeedSelectionNamed([]string{"feed-001"}), nil); err != nil {
			t.Fatal("warm compare:", err)
		}
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for range 256 {
		if _, err := alg.Count(AlgebraFeedSelectionAll(), nil); err != nil {
			t.Fatal("count:", err)
		}
		if _, err := alg.Count(AlgebraFeedSelectionNamed([]string{"feed-000", "feed-001"}), nil); err != nil {
			t.Fatal("named count:", err)
		}
		if _, err := alg.Compare(AlgebraFeedSelectionAll(), AlgebraFeedSelectionNamed([]string{"feed-001"}), nil); err != nil {
			t.Fatal("compare:", err)
		}
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	beforeBySize := map[uint32]uint64{}
	for _, c := range before.BySize {
		beforeBySize[c.Size] = c.Mallocs
	}
	for _, c := range after.BySize {
		if c.Size < 4096 {
			continue
		}
		if c.Mallocs > beforeBySize[c.Size] {
			t.Fatalf("heap allocation of %d bytes during algebra count/compare (mallocs %d -> %d): a complete mapped page was copied into owned memory", c.Size, beforeBySize[c.Size], c.Mallocs)
		}
	}
}
