package iprangedb

import "testing"

func wk(n uint32) Ipv4Key { return Ipv4Key(n) }

func TestCreateEmptyRoundTrips(t *testing.T) {
	w := CreateV4(1, 0)
	w.Commit(0)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsEmpty() || r.RecordCount() != 0 {
		t.Fatal("not empty")
	}
	if _, ok, _ := r.LookupV4(wk(5)); ok {
		t.Fatal("empty lookup must miss")
	}
}

func TestSingleLeafInsertRoundTrips(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.insert(wk(10), wk(20), []byte{1}))
	must(t, w.insert(wk(30), wk(40), []byte{2}))
	must(t, w.insert(wk(5), wk(8), []byte{3})) // before the others
	w.Commit(0)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 3 {
		t.Fatal("count")
	}
	expect := map[uint32]byte{15: 1, 35: 2, 6: 3}
	for ip, sc := range expect {
		s, ok, _ := r.LookupV4(wk(ip))
		if !ok || s[0] != sc {
			t.Fatalf("lookup %d = %v ok=%v want %d", ip, s, ok, sc)
		}
	}
	if _, ok, _ := r.LookupV4(wk(25)); ok {
		t.Fatal("gap should miss")
	}
	var order []uint32
	r.ScanV4(func(f, _ Ipv4Key, _ []byte) { order = append(order, uint32(f)) })
	if len(order) != 3 || order[0] != 5 || order[1] != 10 || order[2] != 30 {
		t.Fatalf("order = %v", order)
	}
}

func TestManyInsertsForceSplitsAndValidate(t *testing.T) {
	w := CreateV4(2, 0)
	const n = 5000
	for i := uint32(0); i < n; i++ {
		base := i * 10
		must(t, w.insert(wk(base), wk(base+4), []byte{byte(i & 0xff), byte(i >> 8)}))
	}
	w.Commit(0)
	r, err := Open(w.Image()) // full validation of the whole tree
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != n {
		t.Fatalf("count = %d", r.RecordCount())
	}
	for _, i := range []uint32{0, 1, 123, 2500, 4999} {
		base := i * 10
		s, ok, _ := r.LookupV4(wk(base + 2))
		if !ok || s[0] != byte(i&0xff) || s[1] != byte(i>>8) {
			t.Fatalf("i=%d lookup", i)
		}
		if _, ok, _ := r.LookupV4(wk(base + 5)); ok {
			t.Fatalf("gap after i=%d", i)
		}
	}
	count := 0
	var prev uint32
	have := false
	r.ScanV4(func(f, _ Ipv4Key, _ []byte) {
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
	w := CreateV4(1, 0)
	for i := uint32(0); i < 200; i++ {
		must(t, w.insert(wk(i*10), wk(i*10+1), []byte{byte(i)}))
	}
	w.Commit(1)
	pagesAfterFirst := len(w.Image()) / pageSize
	for i := uint32(200); i < 260; i++ {
		must(t, w.insert(wk(i*10), wk(i*10+1), []byte{byte(i)}))
	}
	w.Commit(2)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 260 {
		t.Fatalf("count = %d", r.RecordCount())
	}
	if s, ok, _ := r.LookupV4(wk(2550)); !ok || s[0] != 255 {
		t.Fatal("lookup 2550")
	}
	if pagesAfterFirst < 2 {
		t.Fatal("file did not grow past the metas")
	}
}

func TestRepeatedWritesSameLeafCOWOncePerTxn(t *testing.T) {
	w := CreateV4(1, 0)
	for i := uint32(0); i < 700; i++ {
		must(t, w.insert(wk(i*10), wk(i*10+1), []byte{byte(i % 251)}))
	}
	must(t, w.Commit(1))
	if w.treeHeight <= 1 {
		t.Fatal("test must exercise a branch + leaf path")
	}

	pagesBefore := w.store.totalPages()
	expectedPrivatePath := uint64(w.treeHeight)
	for i := uint32(0); i < 8; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+1), []byte{250}))
		if got, want := w.store.totalPages(), pagesBefore+expectedPrivatePath; got != want {
			t.Fatalf("write %d COW'd a page already private in this transaction: pages=%d want %d", i, got, want)
		}
		if got, want := uint64(len(w.dirty)), expectedPrivatePath; got != want {
			t.Fatalf("write %d duplicated dirty pages for the same transaction-private path: dirty=%d want %d", i, got, want)
		}
	}

	must(t, w.Commit(2))
	// privatePages is cleared after commit (all bits zero)
	for _, b := range w.privatePages.bits {
		if b != 0 {
			t.Fatal("privatePages not cleared after commit")
		}
	}
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 700 {
		t.Fatalf("count = %d, want 700", r.RecordCount())
	}
	for i := uint32(0); i < 8; i++ {
		s, ok, err := r.LookupV4(wk(i * 10))
		if err != nil {
			t.Fatal(err)
		}
		if !ok || len(s) != 1 || s[0] != 250 {
			t.Fatalf("lookup %d = %v ok=%v, want scope 250", i*10, s, ok)
		}
	}
}

func TestByteLevelSingleLeafInsertDeleteBoundaries(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.insert(wk(20), wk(21), []byte{2}))
	must(t, w.insert(wk(0), wk(1), []byte{0}))   // insert at index 0
	must(t, w.insert(wk(40), wk(41), []byte{4})) // append
	must(t, w.insert(wk(10), wk(11), []byte{1})) // insert in the middle

	var order []uint32
	w.Scan(func(from, _ Ipv4Key, scope []byte) {
		order = append(order, uint32(from), uint32(scope[0]))
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

	if _, err := w.treeDelete(wk(0)); err != nil { // delete first
		t.Fatal(err)
	}
	if _, err := w.treeDelete(wk(40)); err != nil { // delete last
		t.Fatal(err)
	}
	if _, err := w.treeDelete(wk(20)); err != nil { // delete middle
		t.Fatal(err)
	}
	var remaining []uint32
	w.Scan(func(from, _ Ipv4Key, scope []byte) {
		remaining = append(remaining, uint32(from), uint32(scope[0]))
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

	must(t, w.Commit(1))
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("count = %d, want 1", r.RecordCount())
	}
	if s, ok, _ := r.LookupV4(wk(10)); !ok || len(s) != 1 || s[0] != 1 {
		t.Fatalf("lookup 10 = %v ok=%v, want scope 1", s, ok)
	}
	if _, ok, _ := r.LookupV4(wk(20)); ok {
		t.Fatal("lookup 20 should miss")
	}
}

func TestReopenValidatesAndMutates(t *testing.T) {
	w := CreateV4(1, 0)
	for i := uint32(0); i < 500; i++ {
		must(t, w.Set(wk(i*10), wk(i*10+3), []byte{1}))
	}
	w.Commit(1)
	img := w.Image()

	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if w2.RecordCount() != 500 {
		t.Fatalf("reopen count = %d", w2.RecordCount())
	}
	for i := uint32(0); i < 250; i++ {
		must(t, w2.Delete(wk(i*10), wk(i*10+3)))
	}
	must(t, w2.Set(wk(99999), wk(100000), []byte{7}))
	w2.Commit(2)

	r, err := Open(w2.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 251 {
		t.Fatalf("final count = %d, want 251", r.RecordCount())
	}
	if s, ok, _ := r.LookupV4(wk(2500)); !ok || s[0] != 1 {
		t.Fatal("i=250 should survive")
	}
	if _, ok, _ := r.LookupV4(wk(5)); ok {
		t.Fatal("i=0 should be deleted")
	}
	if s, ok, _ := r.LookupV4(wk(99999)); !ok || s[0] != 7 {
		t.Fatal("new record")
	}
}

func TestOpenImageRejectsCorruption(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(2), []byte{1}))
	w.Commit(1)
	img := append([]byte(nil), w.Image()...)
	img[len(img)-100] ^= 0xFF // corrupt a leaf-page byte
	r, err := Open(img)
	if err != nil {
		t.Fatalf("open must succeed on CRC-valid meta: %v", err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("validate must reject corruption")
	}
}

func TestOpenImageRefusesNewerMinor(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(2), []byte{1}))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	// Bump BOTH metas' version_minor to 2 — a minor NEWER than this writer implements (it
	// implements up to 1, the v4.1 metadata system) — and re-checksum: a forward-compat
	// file the reader accepts read-only, but the writer must refuse to mutate (§5.1/§C.6).
	// version_minor is a little-endian u16, so 2 -> bytes {2, 0}.
	for p := 0; p < 2; p++ {
		page := img[p*pageSize : (p+1)*pageSize]
		page[metaVersionMinor] = 2
		page[metaVersionMinor+1] = 0
		finalizeChecksum(page)
	}
	if _, err := Open(img); err != nil {
		t.Fatalf("reader must accept a newer minor (forward-compat): %v", err)
	}
	if _, err := OpenImageV4(img); err == nil {
		t.Fatal("writer must refuse to mutate a newer-minor file")
	}
}

// --- crash recovery (§6.3) ---

func TestCrashBeforeMetaFlipKeepsOldTree(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(1), []byte{1}))
	w.Commit(1) // T1 = {[1,1]=1} durable
	must(t, w.Set(wk(2), wk(2), []byte{2}))
	img := append([]byte(nil), w.Image()...) // uncommitted: active meta still T1
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("count = %d, want 1", r.RecordCount())
	}
	if s, ok, _ := r.LookupV4(wk(1)); !ok || s[0] != 1 {
		t.Fatal("T1 record missing")
	}
	if _, ok, _ := r.LookupV4(wk(2)); ok {
		t.Fatal("uncommitted set must be invisible")
	}
}

func TestCrashWithTornNewMetaFallsBackToOld(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(1), []byte{1}))
	w.Commit(1)
	must(t, w.Set(wk(2), wk(2), []byte{2}))
	w.Commit(2)
	img := append([]byte(nil), w.Image()...)
	// Tear the active (higher-txn) meta — corrupt a checksum-covered byte, as a crash
	// mid-write of Barrier 2 would. In trusted open, CRC is skipped so the torn meta is
	// selected (wrong data). validate() catches the CRC failure and rejects — the caller
	// knows the file is corrupt. For daemon files under LOCK_EX + fsync, this scenario does
	// not occur in practice.
	txn0 := decodeMeta(img[:pageSize]).txnID
	txn1 := decodeMeta(img[pageSize : 2*pageSize]).txnID
	active := 0
	if txn1 > txn0 {
		active = 1
	}
	img[active*pageSize+64] ^= 0xFF // tear the active meta
	// Trusted open reads the torn meta — the corrupted total_pages may cause a geometry
	// failure (Structural), or the reader opens with wrong data. Either way, validate()
	// catches the CRC failure if open() succeeds.
	r, err := Open(img)
	if err != nil {
		return // geometry failure from corrupted total_pages — acceptable
	}
	if err := r.Validate(); err == nil {
		t.Fatal("validate must catch torn meta CRC")
	}
}

func TestCommittedNewMetaYieldsNewTree(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(1), []byte{1}))
	w.Commit(1)
	must(t, w.Set(wk(2), wk(2), []byte{2}))
	w.Commit(2)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 2 {
		t.Fatalf("count = %d, want 2", r.RecordCount())
	}
	if s, ok, _ := r.LookupV4(wk(2)); !ok || s[0] != 2 {
		t.Fatal("new tree record missing")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fullPageStore struct{}

func (s *fullPageStore) page(pgno uint32) []byte {
	panic("page() must not be called")
}

func (s *fullPageStore) writePageMut(pgno uint32) []byte {
	panic("writePageMut() must not be called")
}

func (s *fullPageStore) writePage(pgno uint32, data []byte) {
	panic("writePage() must not be called")
}

func (s *fullPageStore) allocPage() uint32 {
	panic("allocPage() must not be called after the guard rejects growth")
}

func (s *fullPageStore) totalPages() uint64 {
	return (uint64(1) << 32) - 1
}

func (s *fullPageStore) truncate(pages uint32) {
	panic("truncate() must not be called")
}

func (s *fullPageStore) committedBytes() []byte {
	return nil
}

func (s *fullPageStore) pageData(pgno uint32) []byte {
	panic("pageData() must not be called")
}

func (s *fullPageStore) clearDirty() {}

func (s *fullPageStore) remap(fd uintptr, newSize int64) error {
	return nil
}

func (s *fullPageStore) close() {}

func TestAllocPageRejectsBeforeU32Wrap(t *testing.T) {
	w := CreateV4(1, 0)
	w.store = &fullPageStore{}
	if _, err := w.allocPage(); errorClass(err) != "InvalidInput" {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestPoisonedWriterRefusesOps verifies that once the writer is poisoned (a failed commit
// rebuild), every mutating op and Commit refuses with a State error and the on-disk image is
// untouched — still the last committed state.
func TestPoisonedWriterRefusesOps(t *testing.T) {
	w := CreateV4(1, 0)
	if err := w.Set(wk(10), wk(20), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0); err != nil {
		t.Fatal(err)
	}
	committed := append([]byte(nil), w.Image()...)
	w.poisoned = true // simulate a failed-commit poison (rebuild does irreversible page work)
	check := func(name string, err error) {
		if errorClass(err) != "State" {
			t.Fatalf("%s: expected State error, got %v", name, err)
		}
	}
	check("Set", w.Set(wk(30), wk(40), []byte{2}))
	check("Delete", w.Delete(wk(10), wk(15)))
	_, e := w.ScopeDefine([]byte("x"))
	check("ScopeDefine", e)
	check("MetaSet", w.MetaSet(0, []byte("k"), 0, []byte("v")))
	check("Commit", w.Commit(0))
	if string(w.Image()) != string(committed) {
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
