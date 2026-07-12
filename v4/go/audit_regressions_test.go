package iprangedb

import (
	"math"
	"testing"
)

// selfReferentialChainPages scans for free-list chain pages that list
// themselves as a free entry.
func selfReferentialChainPages(store PageReader) []uint32 {
	var bad []uint32
	for pgno := uint32(2); pgno < store.totalPages(); pgno++ {
		page := store.page(pgno)
		if decodeHeader(page).pageType != PageTypeTxnFree {
			continue
		}
		count := u32le(page, TxnFreeCount)
		if count > TxnFreeCapacity {
			count = TxnFreeCapacity
		}
		for i := uint32(0); i < count; i++ {
			if u32le(page, TxnFreeArray+int(i)*4) == pgno {
				bad = append(bad, pgno)
				break
			}
		}
	}
	return bad
}

// F1: Free-list chain page must never list itself as free.
func TestAuditF1NoSelfReferentialFreeList(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 5000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 3000; i++ {
		if _, err := w.Delete(Ipv4Key(i), Ipv4Key(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	// No-op commit
	if err := w.Commit(3, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if bad := selfReferentialChainPages(w.store); len(bad) != 0 {
		t.Fatalf("self-referential free-list pages: %v", bad)
	}
}

// F2: Repeated mode-2 scope updates must not cause unbounded growth.
func TestAuditF2Mode2ScopeStabilizes(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	var pages []uint32
	for i := byte(1); i <= 7; i++ {
		if _, err := w.ScopeIntern([]byte{i}); err != nil {
			t.Fatal(err)
		}
		if err := w.Commit(uint64(i), math.MaxUint64); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, w.store.totalPages())
		t.Logf("commit %d: %d pages", i, w.store.totalPages())
	}
	if growth := int(pages[len(pages)-1]) - int(pages[3]); growth > 3 {
		t.Fatalf("mode-2 pages did not stabilize: %v", pages)
	}
}

// F3: Mode-2 feed update must persist the new scope.
func TestAuditF3Mode2FeedUpdatePersists(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(10), Ipv4Key(20), id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if err := w.FeedAddRange(Ipv4Key(10), Ipv4Key(20), 3); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, _ := w.IntoImage()
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	gotID, ok := r.LookupV4(Ipv4Key(15))
	if !ok {
		t.Fatal("updated range missing")
	}
	bm := r.ScopeResolve(gotID)
	if len(bm) != 1 || bm[0] != 9 {
		t.Fatalf("updated scope was not persisted: id=%d bitmap=%v", gotID, bm)
	}
}

// F7: Delete-all must zero record_count but must NOT collapse pendingRoot
// (the Rust reference reverted that collapse — it breaks COW/CRC tracking).
func TestAuditF7DeleteAllShrinksTree(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 100_000; i++ {
		if err := w.Append(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Delete(Ipv4Key(0), Ipv4Key(99_999)); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if w.pendingRecordCount != 0 {
		t.Fatalf("pendingRecordCount=%d want 0", w.pendingRecordCount)
	}
	img, _ := w.IntoImage()
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	r.ScanV4(func(from, to Ipv4Key, scopeID uint32) { count++ })
	if count != 0 {
		t.Fatalf("reader saw %d records after delete-all, want 0", count)
	}
}

// F8: ExtSort input last-wins.
func TestAuditF8ExtSortLastWins(t *testing.T) {
	s := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(0), To: Ipv4Key(30), ScopeID: 2},
	})
	for rec := s.Next(); rec != nil; rec = s.Next() {
		if rec.ScopeID != 2 {
			t.Fatalf("later covering record lost: got scope %d expected 2 for [%v,%v]",
				rec.ScopeID, rec.From, rec.To)
		}
	}
}

// F9: Free-list chain pages must have valid CRCs.
func TestAuditF9FreeListPagesHaveCRC(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if _, err := w.Delete(Ipv4Key(i), Ipv4Key(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for pgno := uint32(2); pgno < w.store.totalPages(); pgno++ {
		page := w.store.page(pgno)
		if decodeHeader(page).pageType != PageTypeTxnFree {
			continue
		}
		if !verifyPage(page) {
			t.Fatalf("free-list page %d has invalid CRC", pgno)
		}
	}
}
