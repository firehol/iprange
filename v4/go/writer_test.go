package iprangedb

import (
	"math"
	"testing"
)

// NOTE: wk() and must() are defined in oracle_test.go — reused here.

func TestCreateEmptyRoundTrips(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 0 {
		t.Fatal("not empty")
	}
	if _, ok := r.LookupV4(wk(5)); ok {
		t.Fatal("empty lookup must miss")
	}
}

func TestSingleLeafInsertRoundTrips(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Append(wk(10), wk(20), 1))
	must(t, w.Append(wk(30), wk(40), 2))
	must(t, w.Append(wk(5), wk(8), 3))
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 3 {
		t.Fatal("count")
	}
	expect := map[uint32]uint32{15: 1, 35: 2, 6: 3}
	for ip, sc := range expect {
		s, ok := r.LookupV4(wk(ip))
		if !ok || s != sc {
			t.Fatalf("lookup %d = %d ok=%v want %d", ip, s, ok, sc)
		}
	}
	if _, ok := r.LookupV4(wk(25)); ok {
		t.Fatal("gap should miss")
	}
	var order []uint32
	r.ScanV4(func(f, _ Ipv4Key, _ uint32) { order = append(order, uint32(f)) })
	if len(order) != 3 || order[0] != 5 || order[1] != 10 || order[2] != 30 {
		t.Fatalf("order = %v", order)
	}
}

func TestManyInsertsForceSplitsAndScan(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	const n = 5000
	for i := uint32(0); i < n; i++ {
		base := i * 10
		must(t, w.Set(wk(base), wk(base+4), i))
	}
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != n {
		t.Fatalf("count = %d", r.RecordCount())
	}
	for _, i := range []uint32{0, 1, 123, 2500, 4999} {
		base := i * 10
		s, ok := r.LookupV4(wk(base + 2))
		if !ok || s != i {
			t.Fatalf("i=%d lookup got %d,%v", i, s, ok)
		}
		if _, ok := r.LookupV4(wk(base + 5)); ok {
			t.Fatalf("gap after i=%d", i)
		}
	}
	count := 0
	var prev uint32
	have := false
	r.ScanV4(func(f, _ Ipv4Key, _ uint32) {
		if have && uint32(f) <= prev {
			t.Fatalf("scan not increasing at %d", f)
		}
		prev = uint32(f)
		have = true
		count++
	})
	if count != n {
		t.Fatalf("scan count = %d", count)
	}
}

func TestMultipleCommitsReuseFreedPages(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 200; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+1), i))
	}
	must(t, w.Commit(1, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	pagesAfterFirst := len(img) / PageSize
	for i := uint32(200); i < 260; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+1), i))
	}
	must(t, w.Commit(2, math.MaxUint64))
	img, ok = w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 260 {
		t.Fatalf("count = %d", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(2550)); !ok || s != 255 {
		t.Fatal("lookup 2550")
	}
	if pagesAfterFirst < 2 {
		t.Fatal("file did not grow past the metas")
	}
}

// TestRepeatedWritesSameLeafCOWOncePerTxn verifies COW discipline: writing
// to the same leaf path multiple times in one transaction COWs each page
// exactly once. The private-pages bitset must contain exactly `height` entries.
func TestRepeatedWritesSameLeafCOWOncePerTxn(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 700; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+1), i%251))
	}
	must(t, w.Commit(1, math.MaxUint64))
	if w.committedHeight <= 1 {
		t.Fatal("test must exercise a branch + leaf path")
	}

	pagesBefore := w.store.totalPages()
	expectedPrivatePath := uint32(w.committedHeight)
	for i := uint32(0); i < 8; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+1), 250))
		if got, want := w.store.totalPages(), pagesBefore+expectedPrivatePath; got != want {
			t.Fatalf("write %d COW'd a page already private in this transaction: pages=%d want %d", i, got, want)
		}
		if got, want := w.privatePages.count(), int(expectedPrivatePath); got != want {
			t.Fatalf("write %d duplicated dirty pages for the same transaction-private path: dirty=%d want %d", i, got, want)
		}
	}

	must(t, w.Commit(2, math.MaxUint64))
	// privatePages is cleared after commit.
	if !w.privatePages.isEmpty() {
		t.Fatal("privatePages not cleared after commit")
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 700 {
		t.Fatalf("count = %d, want 700", r.RecordCount())
	}
	for i := uint32(0); i < 8; i++ {
		s, ok := r.LookupV4(wk(i * 10))
		if !ok || s != 250 {
			t.Fatalf("lookup %d = %d ok=%v, want scope 250", i*10, s, ok)
		}
	}
}

func TestByteLevelSingleLeafInsertDeleteBoundaries(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Append(wk(20), wk(21), 2))
	must(t, w.Append(wk(0), wk(1), 0))
	must(t, w.Append(wk(40), wk(41), 4))
	must(t, w.Append(wk(10), wk(11), 1))

	var order []uint32
	w.Scan(func(from, _ Ipv4Key, scopeID uint32) {
		order = append(order, uint32(from), scopeID)
	})
	wantOrder := []uint32{0, 0, 10, 1, 20, 2, 40, 4}
	if len(order) != len(wantOrder) {
		t.Fatalf("order len = %d, want %d: %v", len(order), len(wantOrder), order)
	}
	for i := range order {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}

	if _, err := w.Delete(wk(0), wk(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Delete(wk(40), wk(41)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Delete(wk(20), wk(21)); err != nil {
		t.Fatal(err)
	}
	var remaining []uint32
	w.Scan(func(from, _ Ipv4Key, scopeID uint32) {
		remaining = append(remaining, uint32(from), scopeID)
	})
	wantRemaining := []uint32{10, 1}
	if len(remaining) != len(wantRemaining) {
		t.Fatalf("remaining len = %d, want %d: %v", len(remaining), len(wantRemaining), remaining)
	}
	for i := range remaining {
		if remaining[i] != wantRemaining[i] {
			t.Fatalf("remaining = %v, want %v", remaining, wantRemaining)
		}
	}

	must(t, w.Commit(1, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("count = %d, want 1", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(10)); !ok || s != 1 {
		t.Fatalf("lookup 10 = %d ok=%v, want scope 1", s, ok)
	}
	if _, ok := r.LookupV4(wk(20)); ok {
		t.Fatal("lookup 20 should miss")
	}
}

func TestReopenMutatesAndRecommits(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 500; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+3), 1))
	}
	must(t, w.Commit(1, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}

	w2, err := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), img...)))
	if err != nil {
		t.Fatal(err)
	}
	if w2.RecordCount() != 500 {
		t.Fatalf("reopen count = %d", w2.RecordCount())
	}
	for i := uint32(0); i < 250; i++ {
		_, err := w2.Delete(wk(i*10), wk(i*10+3))
		must(t, err)
	}
	must(t, w2.Set(wk(99999), wk(100000), 7))
	must(t, w2.Commit(2, math.MaxUint64))
	img2, ok := w2.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}

	r, err := Open(img2)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 251 {
		t.Fatalf("final count = %d, want 251", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(2500)); !ok || s != 1 {
		t.Fatal("i=250 should survive")
	}
	if _, ok := r.LookupV4(wk(5)); ok {
		t.Fatal("i=0 should be deleted")
	}
	if s, ok := r.LookupV4(wk(99999)); !ok || s != 7 {
		t.Fatal("new record")
	}
}

// --- crash recovery (uncommitted state is invisible) ---

func TestCrashBeforeMetaFlipKeepsOldTree(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(1), wk(1), 1))
	must(t, w.Commit(1, math.MaxUint64))
	must(t, w.Set(wk(2), wk(2), 2))
	// IntoImage gives the raw page bytes. The meta hasn't been flipped yet
	// (Commit hasn't been called for the second Set), so Open sees T1.
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("count = %d, want 1", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(1)); !ok || s != 1 {
		t.Fatal("T1 record missing")
	}
	if _, ok := r.LookupV4(wk(2)); ok {
		t.Fatal("uncommitted set must be invisible")
	}
}

func TestCommittedNewMetaYieldsNewTree(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(1), wk(1), 1))
	must(t, w.Commit(1, math.MaxUint64))
	must(t, w.Set(wk(2), wk(2), 2))
	must(t, w.Commit(2, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("count = %d, want 2", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(2)); !ok || s != 2 {
		t.Fatal("new tree record missing")
	}
}

// TestPoisonedWriterRefusesOps verifies that once the writer is poisoned, every
// mutating op and Commit refuses with an error and the on-disk image is
// untouched — still the last committed state.
func TestPoisonedWriterRefusesOps(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(wk(10), wk(20), 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	committed := append([]byte(nil), img...)
	w.poisoned = true

	check := func(name string, err error) {
		if err == nil {
			t.Fatalf("%s: poisoned writer must refuse", name)
		}
	}
	check("Set", w.Set(wk(30), wk(40), 2))
	_, derr := w.Delete(wk(10), wk(15))
	check("Delete", derr)
	check("Commit", w.Commit(0, math.MaxUint64))

	img2, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	if string(img2) != string(committed) {
		t.Fatal("on-disk image changed after poisoned ops")
	}
	r, err := Open(committed)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("committed record lost: %d", r.RecordCount())
	}
}
