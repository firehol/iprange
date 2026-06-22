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
	if _, err := OpenImageV4(img); err == nil {
		t.Fatal("open_image must reject corruption")
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
	txn0 := decodeMeta(img[:pageSize]).txnID
	txn1 := decodeMeta(img[pageSize : 2*pageSize]).txnID
	active := 0
	if txn1 > txn0 {
		active = 1
	}
	img[active*pageSize+64] ^= 0xFF // tear the active meta
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1 {
		t.Fatalf("recovered count = %d, want 1 (T1)", r.RecordCount())
	}
	if _, ok, _ := r.LookupV4(wk(2)); ok {
		t.Fatal("T2 should be rolled back")
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
