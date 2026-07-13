package iprangedb

import (
	"runtime"
	"testing"
	"time"
)

// These tests guard the issue-1 + issue-2 refactor of the scope registry:
//
//   - Issue 1: opening a mode-2 writer must NOT materialize the entire scope
//     table into a heap HashMap. The registry keeps only the committed root
//     pointer plus this-transaction's new entries.
//   - Issue 2: Writer.ScopeResolve must be O(log S) (B+tree descent via
//     findScope), not O(S) linear scan over a loaded slice.
//
// The contract preserved by the refactor: scope_intern still deduplicates
// bitmaps across a close/reopen (so a single-feed bitmap re-interned after
// reopen reuses its existing scope_id instead of minting a duplicate).

// makeUniqueBitmaps builds n unique bitmaps of `width` bytes each. The first
// two bytes encode a counter so every bitmap is distinct.
func makeUniqueBitmaps(n, width int) [][]byte {
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		bm := make([]byte, width)
		bm[0] = byte(i >> 8)
		bm[1] = byte(i)
		// pad with a fixed non-zero pattern so width is meaningful
		for j := 2; j < width; j++ {
			bm[j] = byte(0xA0 + (j % 30))
		}
		out[i] = bm
	}
	return out
}

// buildMode2DBWithScopes creates a fresh mode-2 writer, interns `n` unique
// bitmaps, sets one record per scope, commits, and returns the committed
// image plus the interned scope ids.
func buildMode2DBWithScopes(t *testing.T, n, width int) ([]byte, []uint32) {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bms := makeUniqueBitmaps(n, width)
	ids := make([]uint32, n)
	for i, bm := range bms {
		id, err := w.ScopeIntern(bm)
		if err != nil {
			t.Fatalf("ScopeIntern %d: %v", i, err)
		}
		ids[i] = id
		if err := w.Set(Ipv4Key(uint32(i)), Ipv4Key(uint32(i)), id); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatalf("IntoImage failed")
	}
	return img, ids
}

// openWriterFromImage reopens a committed image as a Writer (mode-2 path).
func openWriterFromImage(t *testing.T, img []byte) *Writer[Ipv4Key] {
	t.Helper()
	store := newVecPageStore(img)
	w, err := openWriter[Ipv4Key](store)
	if err != nil {
		t.Fatalf("openWriter: %v", err)
	}
	return w
}

// TestScopeResolveCorrectAfterReopen — every committed scope resolves to its
// exact bitmap after a close/reopen. Guards the resolve-via-findScope path.
func TestScopeResolveCorrectAfterReopen(t *testing.T) {
	const n = 2000
	const width = 8
	img, ids := buildMode2DBWithScopes(t, n, width)
	bms := makeUniqueBitmaps(n, width)
	w := openWriterFromImage(t, img)
	for i := 0; i < n; i++ {
		got := w.ScopeResolve(ids[i])
		if got == nil {
			t.Fatalf("ScopeResolve(%d) returned nil for scope %d", ids[i], i)
		}
		if !bytesEqual(got, bms[i]) {
			t.Fatalf("ScopeResolve(%d) bitmap mismatch at i=%d", ids[i], i)
		}
	}
}

// TestScopeOpenAllocIsConstant — the headline issue-1 proof. Opening a mode-2
// writer must allocate roughly CONSTANT heap regardless of how many scopes are
// committed, because the table is read on demand via the B+tree (findScope),
// not materialized into a HashMap.
//
// Before the fix this ratio was ~10x (linear in scope count). After the fix it
// must be near 1 (constant). The threshold (2.5x) rejects linear with margin.
func TestScopeOpenAllocIsConstant(t *testing.T) {
	if testing.Short() {
		t.Skip("scope-open alloc test is slow")
	}
	const width = 64
	measure := func(n int) uint64 {
		img, _ := buildMode2DBWithScopes(t, n, width)
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)
		w := openWriterFromImage(t, img)
		// Touch nothing that would lazily load; just measure open cost.
		_ = w.ScopePageCount()
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		return m1.HeapAlloc - m0.HeapAlloc
	}
	small := measure(2000)
	large := measure(20000)
	t.Logf("open HeapAlloc: n=2000 -> %d bytes, n=20000 -> %d bytes", small, large)
	// Linear load would make large >> small (ratio ~10). Constant open keeps
	// the ratio near 1. Allow generous headroom for allocator noise.
	if small > 0 && large > small*2 {
		t.Fatalf("open allocations grow with scope count: small=%d large=%d ratio=%.1f (expected ~constant)",
			small, large, float64(large)/float64(small))
	}
}

// TestScopeResolveIsSublinear — issue-2 proof. Resolving the LAST scope_id
// (worst case for a linear scan) must be about as fast as resolving the FIRST.
// A linear scan would make last >> first; findScope keeps them comparable.
func TestScopeResolveIsSublinear(t *testing.T) {
	const n = 20000
	const width = 8
	img, ids := buildMode2DBWithScopes(t, n, width)
	w := openWriterFromImage(t, img)

	first := ids[0]
	last := ids[n-1]
	// Warm: ensure neither path lazily loads the full table during timing.
	// (findScope does not load the table; this just primes caches.)
	_ = w.ScopeResolve(first)

	const reps = 2000
	tFirst := timeFunc(reps, func() { _ = w.ScopeResolve(first) })
	tLast := timeFunc(reps, func() { _ = w.ScopeResolve(last) })
	t.Logf("resolve first(id=%d)=%v last(id=%d)=%v ratio=%.2f", first, tFirst, last, tLast, float64(tLast)/float64(tFirst))
	// A linear scan over ~n entries would put last at ~n× the cost of first.
	// findScope keeps both O(log S); allow a wide margin for cache effects.
	if tFirst > 0 && tLast > tFirst*4 {
		t.Fatalf("ScopeResolve appears linear: last/first ratio=%.2f (expected ~1)", float64(tLast)/float64(tFirst))
	}
}

func timeFunc(reps int, f func()) time.Duration {
	// One untimed warm-up.
	f()
	start := time.Now()
	for i := 0; i < reps; i++ {
		f()
	}
	return time.Since(start) / time.Duration(reps)
}

// TestScopeInternDedupAcrossReopen — the dedup contract. After close/reopen,
// interning a bitmap that is already committed must return its existing
// scope_id (no duplicate ids for the same bitmap).
func TestScopeInternDedupAcrossReopen(t *testing.T) {
	const width = 4
	img, ids := buildMode2DBWithScopes(t, 3, width)
	bms := makeUniqueBitmaps(3, width)
	w := openWriterFromImage(t, img)
	for i := 0; i < 3; i++ {
		got, err := w.ScopeIntern(bms[i])
		if err != nil {
			t.Fatalf("ScopeIntern after reopen %d: %v", i, err)
		}
		if got != ids[i] {
			t.Fatalf("intern dedup across reopen failed: bitmap %d got id %d want %d", i, got, ids[i])
		}
	}
	// A brand-new bitmap must still mint a new id (max committed + 1).
	fresh := make([]byte, width)
	fresh[0] = 0xFF
	fresh[1] = 0xFF
	got, err := w.ScopeIntern(fresh)
	if err != nil {
		t.Fatalf("ScopeIntern fresh: %v", err)
	}
	if got <= ids[2] {
		t.Fatalf("fresh bitmap did not get a new id > max committed: got %d max=%d", got, ids[2])
	}
}

// TestScopeInternDedupWithinTxnAfterReopen — within one transaction after
// reopen, interning the same new bitmap twice must return the same id.
func TestScopeInternDedupWithinTxnAfterReopen(t *testing.T) {
	img, _ := buildMode2DBWithScopes(t, 5, 4)
	w := openWriterFromImage(t, img)
	bm := []byte{0x7F, 0x7F, 0x7F, 0x7F}
	id1, err := w.ScopeIntern(bm)
	if err != nil {
		t.Fatalf("ScopeIntern: %v", err)
	}
	id2, err := w.ScopeIntern(bm)
	if err != nil {
		t.Fatalf("ScopeIntern 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("within-txn dedup failed: %d != %d", id1, id2)
	}
}

// TestScopeInternAfterReopenAllocIsConstant — issue-7 proof. The first
// intern(s) after reopen used to lazily build a bitmap→scope_id HashMap over
// the WHOLE committed scope table (O(S) heap). The fix streams the scope tree
// via findScopeByBitmap (O(1) heap). HeapAlloc for interning existing committed
// bitmaps must stay roughly constant as the scope count grows, not scale with S.
func TestScopeInternAfterReopenAllocIsConstant(t *testing.T) {
	if testing.Short() {
		t.Skip("scope-intern alloc test is slow")
	}
	const width = 8
	measure := func(n int) uint64 {
		img, ids := buildMode2DBWithScopes(t, n, width)
		bms := makeUniqueBitmaps(n, width)
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)
		w := openWriterFromImage(t, img)
		// First interns of existing committed bitmaps: used to trigger the full
		// committed-index load. Now streams the scope tree (O(1) heap).
		_, _ = w.ScopeIntern(bms[0])
		_, _ = w.ScopeIntern(bms[n-1])
		_ = ids
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		return m1.HeapAlloc - m0.HeapAlloc
	}
	small := measure(2000)
	large := measure(20000)
	t.Logf("intern-after-reopen HeapAlloc: n=2000 -> %d, n=20000 -> %d", small, large)
	// A materializing implementation makes large >> small. The streaming scan
	// keeps it near-constant; allow generous allocator headroom.
	if small > 0 && large > small*3 {
		t.Fatalf("first intern after reopen allocates O(S): small=%d large=%d ratio=%.1f",
			small, large, float64(large)/float64(small))
	}
}
