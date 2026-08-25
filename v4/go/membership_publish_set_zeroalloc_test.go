// Milestone 3 chunk 3b half of the complete-page ownership evidence: a
// publish_set round trip (materialized output, reopen, algebra count)
// must not allocate any Go heap object of 4096 bytes or larger.
// Complete-page copies would allocate exactly such objects; the writer
// builds pages only at final offsets in the file mapping and the
// reader walks mapped views, so the whole publish path stays free of
// owned page buffers.

package iprangedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNoPageSizedHeapAllocationsPublishSet(t *testing.T) {
	requirePublicationSecurity(t)
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	dir := t.TempDir()

	// Warm every path outside the measured window and create one
	// fully reusable output shape.
	warm := filepath.Join(dir, "warm.iprdb")
	if _, err := publishV4(t, helpers, warm, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget()); err != nil {
		t.Fatal("warm publish:", err)
	}
	warmReader := openPublished(t, warm)
	warmScope := publishSetCountScope(t, warmReader)
	warmAlgebra, err := NewMembershipAlgebra([]*MembershipScope{warmScope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal("warm algebra:", err)
	}
	// The replacement policy requires the destination to exist before
	// the first publish (Rust replacement::bind refuses a missing
	// destination), so seed the target once; every warm-up iteration
	// then exercises the replace path.
	again := filepath.Join(dir, "again.iprdb")
	if err := os.WriteFile(again, []byte("placeholder"), 0o644); err != nil {
		t.Fatal("seed again:", err)
	}
	for range 8 {
		if _, err := publishV4(t, helpers, again, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyReplaceExistingNoRollback, outputBudget()); err != nil {
			t.Fatal("warm publish again:", err)
		}
		if _, err := warmAlgebra.Count(AlgebraFeedSelectionAll(), nil); err != nil {
			t.Fatal("warm count:", err)
		}
	}
	mustClose(t, warmReader)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for index := range 64 {
		destination := filepath.Join(dir, "za-"+itob(index)+".iprdb")
		if _, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget()); err != nil {
			t.Fatal("publish:", err)
		}
		reader := openPublished(t, destination)
		scope := publishSetCountScope(t, reader)
		after, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
		if err != nil {
			t.Fatal("algebra:", err)
		}
		if _, err := after.Count(AlgebraFeedSelectionAll(), nil); err != nil {
			t.Fatal("count:", err)
		}
		mustClose(t, reader)
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
			t.Fatalf("heap allocation of %d bytes during publish/reopen/count (mallocs %d -> %d): a complete mapped page was copied into owned memory", c.Size, beforeBySize[c.Size], c.Mallocs)
		}
	}
}

// publishSetCountScope resolves one full membership scope over a
// published output.
func publishSetCountScope(t *testing.T, reader *ImmutableReader) *MembershipScope {
	t.Helper()
	query, err := reader.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}
	return scope
}

// itob renders one small decimal index without fmt allocation noise.
func itob(index int) string {
	if index == 0 {
		return "0"
	}
	var buffer [32]byte
	position := len(buffer)
	for index > 0 {
		position--
		buffer[position] = byte('0' + index%10)
		index /= 10
	}
	return string(buffer[position:])
}
