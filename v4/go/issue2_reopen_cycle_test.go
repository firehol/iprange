//go:build unix

package iprangedb

import (
	"os"
	"testing"
)

// ── Issue 2: file-backed reopen cycles must not grow without bound. ────────────
//
// Root cause: when Commit allocates a free-list chain page in the growth region
// with no trailing free pages to reclaim (trailing == 0), the committed total
// did not include the chain page. After close+reopen, committed_pages < chain
// page position, so loadFreeList silently dropped the free-list head and the
// freed pages were never reclaimed — the file grew by ~1 page per cycle.
//
// Fix: (1) Commit must include the highest chain page in committed_pages even
// when trailing == 0. (2) Close must truncate the file to exactly
// committed_pages * PageSize so no stale chain pages linger in the growth
// region across reopen.

func TestReopenCycleDoesNotGrowUnbounded(t *testing.T) {
	path := tempPath(t, "issue2-reopen")
	defer os.Remove(path)
	defer os.Remove(path + ".readers")

	// Initial create: 1 record, commit, close.
	{
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		must(t, fw.Set(wk(0), wk(3), 1))
		must(t, fw.Commit(1))
		must(t, fw.Close())
	}

	// 201 reopen cycles: each sets [0,3] with a new scope, commits, closes.
	for i := uint32(1); i <= 201; i++ {
		fw, err := OpenFile[Ipv4Key](path)
		if err != nil {
			t.Fatalf("cycle %d open: %v", i, err)
		}
		must(t, fw.Set(wk(0), wk(3), i+1))
		must(t, fw.Commit(uint64(i+1)))
		must(t, fw.Close())
	}

	// The file MUST stay bounded: a 1-record DB is a handful of live pages plus
	// a small free-list chain (compacted once it reaches 20 chain pages). 80
	// pages is a generous upper bound that still catches unbounded growth.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	pages := uint32(info.Size() / PageSize)
	if pages > 80 {
		t.Fatalf("file grew to %d pages after 201 reopen cycles (bound 80) — free-list chain was lost across reopen", pages)
	}

	// Data correctness: the last scope written must be readable.
	fw, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.LookupV4(wk(1)); !ok || got != 202 {
		t.Fatalf("lookup after reopen cycles = %d ok=%v, want 202", got, ok)
	}
}
