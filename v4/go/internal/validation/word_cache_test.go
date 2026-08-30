package validation

// Bitmap word-cache regression tests (Rust validation/bitmap/
// word_cache.rs): the cache key must pin the probed leaf region even on
// an absent-child miss, so a later probe of a previously loaded region
// re-descends instead of reusing the cached empty answer.

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestBitmapWordCacheAbsentRegionCacheKey proves the absent-child branch
// stores the target region as the cache key (word_cache.rs leaf_base =
// Some(leaf_base)): after probing an absent region, a probe of the
// previously loaded region must re-descend and return the real word, and
// a repeated probe of the absent region must stay cached as empty.
func TestBitmapWordCacheAbsentRegionCacheKey(t *testing.T) {
	// Synthetic sparse feed-used bitmap: one branch (page 2) with a
	// leaf child at region 0 (page 3) and an absent child at region 1.
	branch := make([]byte, format.PageSize)
	bitmap.Initialize(branch, 1, 1, bitmap.KindFeed)
	format.PutU16(branch[format.HeaderCount:], 2)
	if err := bitmap.SetBranchChild(branch, 0, 3); err != nil {
		t.Fatal(err)
	}
	leaf := make([]byte, format.PageSize)
	bitmap.Initialize(leaf, 1, 0, bitmap.KindFeed)
	format.PutU16(leaf[format.HeaderCount:], 1)
	if err := bitmap.SetLeafWord(leaf, 0, 0x2); err != nil {
		t.Fatal(err)
	}
	meta := metaPage(1, 4)
	path := filepath.Join(t.TempDir(), "bitmap.iprdb")
	if err := writePages(path, meta, branch, leaf); err != nil {
		t.Fatal(err)
	}
	ctx := fixturePathContext(t, path, 1<<20)
	limit := bitmap.LeafBits * 2 // region 0 and absent region 1
	cache := newBitmapWordCache(2, limit, bitmap.KindFeed)

	got, err := cache.word(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x2 {
		t.Fatalf("word 0 (region 0) = %#x, want 0x2", got)
	}
	got, err = cache.word(ctx, 600) // bit 38400: absent region 32000
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("word 600 (absent region) = %#x, want 0", got)
	}
	got, err = cache.word(ctx, 0) // must re-descend, not reuse the miss
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x2 {
		t.Fatalf("word 0 after absent-region miss = %#x, want 0x2 (stale empty cache reused)", got)
	}
	got, err = cache.word(ctx, 600) // absent region must stay cached
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("word 600 re-probe = %#x, want 0", got)
	}
}
