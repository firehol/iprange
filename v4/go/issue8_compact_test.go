package iprangedb

import "testing"

// Issue 8: compactIfNeeded must not walk the whole tree every commit, and
// compaction must still fire when the tree is genuinely sparse.

// Build a dense tree, then delete most records in one txn. Commit must compact
// the now-sparse tree (page count drops sharply) and preserve correctness.
func TestIssue8_CompactFiresAfterSparseDelete(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Dense: 4000 single-IP records (key_width=4 → leafMax=340 → ~12 leaves).
	for i := uint32(0); i < 4000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	densePages := w.TreePageCount()
	if densePages < 12 {
		t.Fatalf("dense tree should span multiple pages, got %d", densePages)
	}

	// Delete ~90% in one txn → tree becomes sparse (same page count, few records).
	for i := uint32(0); i < 3600; i++ {
		if _, err := w.Delete(Ipv4Key(i), Ipv4Key(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(2, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	sparsePages := w.TreePageCount()
	// Compaction must have rebuilt the tree into far fewer pages.
	if sparsePages*4 >= densePages {
		t.Fatalf("compaction did not shrink the sparse tree: dense=%d sparse=%d", densePages, sparsePages)
	}

	// Correctness: surviving records resolve, deleted ones do not.
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("IntoImage failed")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.LookupV4(Ipv4Key(3700)); !ok {
		t.Fatal("surviving record missing")
	}
	if _, ok := r.LookupV4(Ipv4Key(0)); ok {
		t.Fatal("deleted record still present")
	}
}

// Repeated record-only commits on a dense tree must not trigger compaction
// (the tree is already dense). Guards against a false-positive compaction loop.
func TestIssue8_NoCompactionOnDenseAppends(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	pagesBefore := w.TreePageCount()

	// A few small appends + commits — tree stays dense.
	for round := uint32(0); round < 5; round++ {
		if err := w.Set(Ipv4Key(10000+round), Ipv4Key(10000+round), 1); err != nil {
			t.Fatal(err)
		}
		if err := w.Commit(uint64(2+round), ^uint64(0)); err != nil {
			t.Fatal(err)
		}
	}
	pagesAfter := w.TreePageCount()
	// Pages should grow by at most a couple (dense appends), not explode.
	if pagesAfter > pagesBefore+4 {
		t.Fatalf("dense appends caused unexpected page growth: before=%d after=%d", pagesBefore, pagesAfter)
	}
}
