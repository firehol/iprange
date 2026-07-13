package iprangedb

import (
	"math"
	"path/filepath"
	"testing"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

func collectTreePagesForAudit[K ipKey[K]](w *Writer[K], pgno uint32, height uint32, out map[uint32]bool) {
	if pgno == 0 || out[pgno] {
		return
	}
	out[pgno] = true
	if height <= 1 {
		return
	}
	page := w.store.page(pgno)
	h := decodeHeader(page)
	bv := newBranchView(page, int(h.entryCount), int(w.keyWidth))
	for i := 0; i < bv.childCount(); i++ {
		collectTreePagesForAudit(w, bv.child(i), height-1, out)
	}
}

func assertNoReachablePageIsFree[K ipKey[K]](t *testing.T, w *Writer[K]) {
	t.Helper()
	reachable := make(map[uint32]bool)
	collectTreePagesForAudit(w, w.committedRoot, w.committedHeight, reachable)
	var scopes []uint32
	w.collectScopePageNumbers(w.scopeTableRootCache, 0, &scopes)
	for _, p := range scopes {
		reachable[p] = true
	}
	chainPages := ReadChainPageNumbers(w.store, w.freeListHead)
	for _, p := range chainPages {
		reachable[p] = true
	}
	entries, err := ReadChain(w.store, w.freeListHead)
	if err != nil {
		t.Fatal(err)
	}
	seenFree := make(map[uint32]bool)
	for _, e := range entries {
		if e.Pgno >= w.store.totalPages() {
			t.Fatalf("free page %d outside total %d", e.Pgno, w.store.totalPages())
		}
		if reachable[e.Pgno] {
			t.Fatalf("reachable page %d is also marked free", e.Pgno)
		}
		if seenFree[e.Pgno] {
			t.Fatalf("duplicate free page %d", e.Pgno)
		}
		seenFree[e.Pgno] = true
	}
}

// ── Issue 1: MVCC provisional reader race ────────────────────────────────────
// MaxUint64 is used both as "no readers" and as the provisional sentinel.
// The provisional reader is invisible to reclamation.

func TestReaudit1_ProvisionalReaderBlocksReclamation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.iprdb")
	table, err := OpenReaderTable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer table.Close()
	// Register with 0 (provisional sentinel) — must block all reclamation.
	guard, err := table.Register(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	// A registered provisional reader must make OldestReaderTxnID < MaxUint64.
	// If it returns MaxUint64, the writer thinks no readers exist.
	oldest := table.OldestReaderTxnID()
	if oldest == math.MaxUint64 {
		t.Fatal("provisional reader is invisible — sentinel collision with no-readers")
	}
}

// ── Issue 2: No-op commits leak pages ────────────────────────────────────────

func TestReaudit2_NoopCommitsDoNotLeakPages(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeScalar, 0)
	for i := uint32(0); i < 5000; i++ {
		w.Set(Ipv4Key(i), Ipv4Key(i), i)
	}
	w.Commit(1, math.MaxUint64)
	for i := uint32(0); i < 3000; i++ {
		w.Delete(Ipv4Key(i), Ipv4Key(i))
	}
	w.Commit(2, math.MaxUint64)
	startTotal := w.store.totalPages()
	for txn := uint64(3); txn <= 100; txn++ {
		if err := w.Commit(txn, math.MaxUint64); err != nil {
			t.Fatal(err)
		}
		assertNoReachablePageIsFree(t, w)
	}
	endTotal := w.store.totalPages()
	if endTotal > startTotal+2 {
		t.Fatalf("no-op commits grew file: %d -> %d", startTotal, endTotal)
	}
}

// ── Issue 3: Partial deletion retains peak tree ──────────────────────────────

func TestReaudit3_LargePartialDeleteShrinksNearFinal(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeScalar, 0)
	for i := uint32(0); i < 100_000; i++ {
		w.Append(Ipv4Key(i), Ipv4Key(i), i)
	}
	w.Commit(1, math.MaxUint64)
	w.Delete(Ipv4Key(0), Ipv4Key(89_999))
	w.Commit(2, math.MaxUint64)
	afterFirst := w.store.totalPages()
	t.Logf("partial delete after commit 2: %d pages", afterFirst)
	// Old committed pages freed by the delete are not trailing, so they
	// survive until the next commit. A second no-op commit lets truncation
	// reclaim the trailing COW pages produced by the compact rebuild.
	w.Commit(3, math.MaxUint64)
	afterSecond := w.store.totalPages()
	t.Logf("after commit 3: %d pages", afterSecond)
	// The compact rebuild keeps the file far below the uncompacted ~1127
	// pages. Old committed pages cannot be reclaimed until the next
	// migration, so 700 is a generous threshold that still catches the
	// regression.
	if afterSecond >= 700 {
		t.Fatalf("90%% delete retained peak tree: first=%d second=%d",
			afterFirst, afterSecond)
	}
	if w.RecordCount() != 10000 {
		t.Fatalf("record count = %d, want 10000", w.RecordCount())
	}
}

// ── Issue 4: ExtSort last-wins across spill runs ─────────────────────────────

func TestReaudit4_ExtSortLastWinsAcrossSpillRuns(t *testing.T) {
	s := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: t.TempDir()})
	if err := s.Add(Ipv4Key(10), Ipv4Key(20), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Ipv4Key(0), Ipv4Key(30), 2); err != nil {
		t.Fatal(err)
	}
	stream, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	for rec := stream.Next(); rec != nil; rec = stream.Next() {
		if rec.ScopeID != 2 {
			t.Fatalf("later record lost across spill runs: got scope %d for [%v,%v]",
				rec.ScopeID, rec.From, rec.To)
		}
	}
}

// ── Issue 5: Go public scope mutation persists ───────────────────────────────

func TestReaudit5_PublicScopeMutationPersists(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeIndirect, 0)
	id, _ := w.ScopeIntern([]byte{1})
	w.Set(Ipv4Key(1), Ipv4Key(1), id)
	w.Commit(1, math.MaxUint64)
	newID, err := w.ScopeBitmapSetFeed(id, 3)
	if err != nil {
		t.Fatal(err)
	}
	w.Set(Ipv4Key(1), Ipv4Key(1), newID)
	w.Commit(2, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	got := r.ScopeResolve(newID)
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("public scope mutation not persisted: id=%d bitmap=%v", newID, got)
	}
}

// ── Issue 6: FeedAddRange rejects feedBit=32 in bitmap mode ──────────────────

func TestReaudit6_FeedBit32RejectedInBitmapMode(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err := w.FeedAddRange(Ipv4Key(1), Ipv4Key(1), 32); err == nil {
		t.Fatal("feed bit 32 should be rejected in 32-bit bitmap mode")
	}
}

// ── Issue 7: No-op commits do not lose free entries ──────────────────────────

func TestReaudit7_NoopCommitsPreserveFreeEntries(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeScalar, 0)
	for i := uint32(0); i < 5000; i++ {
		w.Set(Ipv4Key(i), Ipv4Key(i), i)
	}
	w.Commit(1, math.MaxUint64)
	for i := uint32(0); i < 3000; i++ {
		w.Delete(Ipv4Key(i), Ipv4Key(i))
	}
	w.Commit(2, math.MaxUint64)
	startFree, _ := ReadChain(w.store, w.freeListHead)
	for txn := uint64(3); txn <= 10; txn++ {
		w.Commit(txn, math.MaxUint64)
	}
	endFree, _ := ReadChain(w.store, w.freeListHead)
	if len(endFree)+2 < len(startFree) {
		t.Fatalf("no-op commits leaked free pages: %d -> %d", len(startFree), len(endFree))
	}
}
