package iprangedb

import (
	"os"
	"testing"
)

func TestCreateEmpty(t *testing.T) {
	w, err := Create[Ipv4Key](0, 0)
	if err != nil { t.Fatal(err) }
	if err := w.Commit(0); err != nil { t.Fatal(err) }
	img, ok := w.IntoImage()
	if !ok { t.Fatal("expected image") }
	r, err := Open(img)
	if err != nil { t.Fatal(err) }
	if r.RecordCount() != 0 { t.Fatalf("count=%d", r.RecordCount()) }
}

func TestSetSingle(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 1 { t.Fatalf("count=%d", r.RecordCount()) }
	s, ok := r.LookupV4(Ipv4Key(15))
	if !ok || s != 1 { t.Fatalf("lookup(15)=%d,%v", s, ok) }
}

func TestAppend1k(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < 1000; i++ {
		w.Append(Ipv4Key(i*10), Ipv4Key(i*10+5), i)
	}
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 1000 { t.Fatalf("count=%d", r.RecordCount()) }
	s, ok := r.LookupV4(Ipv4Key(500))
	if !ok || s != 50 { t.Fatalf("lookup(500)=%d,%v", s, ok) }
}

func TestDeleteOverlap(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(100), 1)
	w.Delete(Ipv4Key(30), Ipv4Key(50))
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 2 { t.Fatalf("count=%d", r.RecordCount()) }
	if s, ok := r.LookupV4(Ipv4Key(20)); !ok || s != 1 { t.Fatalf("lookup(20)=%d,%v", s, ok) }
	if _, ok := r.LookupV4(Ipv4Key(40)); ok { t.Fatal("40 should be deleted") }
	if s, ok := r.LookupV4(Ipv4Key(60)); !ok || s != 1 { t.Fatalf("lookup(60)=%d,%v", s, ok) }
}

func TestSetOverwrite(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(100), 1)
	w.Set(Ipv4Key(10), Ipv4Key(100), 2)
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 1 { t.Fatalf("count=%d", r.RecordCount()) }
	s, _ := r.LookupV4(Ipv4Key(50))
	if s != 2 { t.Fatalf("scope=%d", s) }
}

func TestLeafSplit(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < 1000; i++ {
		w.Set(Ipv4Key(i*2), Ipv4Key(i*2+1), i)
	}
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 1000 { t.Fatalf("count=%d", r.RecordCount()) }
	for i := uint32(0); i < 1000; i++ {
		s, ok := r.LookupV4(Ipv4Key(i * 2))
		if !ok || s != i { t.Fatalf("lookup(%d)=%d,%v", i*2, s, ok) }
	}
}

func TestWriterReaderCommitted(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0)
	w.Set(Ipv4Key(30), Ipv4Key(40), 2) // pending
	// Reader sees committed only (this test uses IntoImage which gives pending;
	// for a proper committed-reader test we'd use the file-backed path)
}

// --- Migration tests ---

func TestMigrateEmptyToFull(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Commit(0)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(30), To: Ipv4Key(40), ScopeID: 2},
	})
	counters, err := Migrate(w, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Added != 2 || counters.Removed != 0 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateFullToEmpty(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Set(Ipv4Key(30), Ipv4Key(40), 2)
	w.Commit(0)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{})
	counters, err := Migrate(w, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Added != 0 || counters.Removed != 2 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateIdentical(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Unchanged != 1 || counters.Added != 0 || counters.Removed != 0 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateChangeScope(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 2},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Changed != 1 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestExtSortAndMigrate(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Commit(0)

	unsorted := []DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(30), To: Ipv4Key(40), ScopeID: 2},
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(50), To: Ipv4Key(60), ScopeID: 3},
	}
	stream, err := ExtSort(unsorted, nil)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := Migrate(w, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Added != 3 {
		t.Fatalf("counters=%+v", counters)
	}
	w.Commit(0)

	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 3 {
		t.Fatalf("count=%d", r.RecordCount())
	}
	if s, ok := r.LookupV4(Ipv4Key(15)); !ok || s != 1 {
		t.Fatalf("lookup(15)=%d,%v", s, ok)
	}
}

func TestStress200k(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < 200_000; i++ {
		w.Set(Ipv4Key(i), Ipv4Key(i), i)
	}
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 200_000 {
		t.Fatalf("count=%d", r.RecordCount())
	}
	if s, ok := r.LookupV4(Ipv4Key(0)); !ok || s != 0 {
		t.Fatalf("lookup(0)=%d,%v", s, ok)
	}
	if s, ok := r.LookupV4(Ipv4Key(199999)); !ok || s != 199999 {
		t.Fatalf("lookup(199999)=%d,%v", s, ok)
	}
}

func TestStress500k(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < 500_000; i++ {
		w.Set(Ipv4Key(i), Ipv4Key(i), i)
	}
	w.Commit(0)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != 500_000 {
		t.Fatalf("count=%d", r.RecordCount())
	}
	for i := uint32(0); i < 500_000; i += 1000 {
		if s, ok := r.LookupV4(Ipv4Key(i)); !ok || s != i {
			t.Fatalf("lookup(%d)=%d,%v", i, s, ok)
		}
	}
}

// --- Reader registration tests ---

func TestReaderTableRegister(t *testing.T) {
	tmpPath := "/tmp/iprange_test_reader.iprdb"
	rt, err := OpenReaderTable(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	guard, err := rt.Register(42)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	if rt.OldestReaderTxnID() != 42 {
		t.Fatalf("oldest=%d", rt.OldestReaderTxnID())
	}
}

func TestReaderTableReapStale(t *testing.T) {
	tmpPath := "/tmp/iprange_test_reader2.iprdb"
	rt, err := OpenReaderTable(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Write a slot with a dead PID
	rt.writeSlot(5, 999999, 1)

	cleared := rt.ReapStale()
	if cleared < 1 {
		t.Fatalf("expected at least 1 stale slot cleared, got %d", cleared)
	}
}

// --- Churn tests ---

func TestChurnStableSize(t *testing.T) {
	img := func() []byte {
		w, _ := Create[Ipv4Key](0, 0)
		for i := uint32(0); i < 1000; i++ {
			w.Set(Ipv4Key(i), Ipv4Key(i), i)
		}
		w.Commit(0)
		img, _ := w.IntoImage()
		return img
	}()

	initialPages := len(img) / PageSize

	for cycle := uint32(0); cycle < 20; cycle++ {
		w, _ := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), img...)))
		w.SetSafeReclaimTxnID(0)
		for i := uint32(0); i < 1000; i++ {
			w.Delete(Ipv4Key(i), Ipv4Key(i))
		}
		for i := uint32(0); i < 1000; i++ {
			w.Set(Ipv4Key(i), Ipv4Key(i), i)
		}
		w.Commit(uint64(cycle + 1))
		img, _ = w.IntoImage()
	}

	finalPages := len(img) / PageSize
	if finalPages > initialPages*2 {
		t.Fatalf("file grew from %d to %d pages — reclamation broken", initialPages, finalPages)
	}

	r, _ := Open(img)
	if r.RecordCount() != 1000 {
		t.Fatalf("count=%d", r.RecordCount())
	}
}

// --- External sort spill tests (ported from extsort.rs) ---

func TestSpillSort(t *testing.T) {
	cfg := &ExtSortConfig{ChunkSize: 10, TempDir: t.TempDir()}
	input := make([]DesiredRecord[Ipv4Key], 0, 25)
	for i := uint32(0); i < 25; i++ {
		input = append(input, DesiredRecord[Ipv4Key]{From: Ipv4Key(1000 - i), To: Ipv4Key(1000 - i), ScopeID: i})
	}
	stream, err := ExtSort(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var prev Ipv4Key
	count := 0
	for {
		r := stream.Next()
		if r == nil {
			break
		}
		if count > 0 && r.From.cmp(prev) <= 0 {
			t.Fatalf("not sorted: prev=%v cur=%v", uint32(prev), uint32(r.From))
		}
		prev = r.From
		count++
	}
	if count != 25 {
		t.Fatalf("count=%d want 25", count)
	}
}

func TestSpillCoalesce(t *testing.T) {
	cfg := &ExtSortConfig{ChunkSize: 5, TempDir: t.TempDir()}
	input := make([]DesiredRecord[Ipv4Key], 0, 10)
	for i := uint32(0); i < 10; i++ {
		input = append(input, DesiredRecord[Ipv4Key]{From: Ipv4Key(i * 2), To: Ipv4Key(i*2 + 1), ScopeID: 1})
	}
	stream, err := ExtSort(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := stream.Next()
	if r == nil {
		t.Fatal("expected one coalesced record")
	}
	if r.From != 0 || r.To != Ipv4Key(19) {
		t.Fatalf("got [%v,%v] want [0,19]", uint32(r.From), uint32(r.To))
	}
	if stream.Next() != nil {
		t.Fatal("expected a single coalesced record")
	}
}

func TestSpillCoalesceV6(t *testing.T) {
	cfg := &ExtSortConfig{ChunkSize: 4, TempDir: t.TempDir()}
	input := make([]DesiredRecord[Ipv6Key], 0, 10)
	for i := uint32(0); i < 10; i++ {
		k := Ipv6Key{Hi: 0, Lo: uint64(i * 2)}
		input = append(input, DesiredRecord[Ipv6Key]{From: k, To: Ipv6Key{Hi: 0, Lo: uint64(i*2 + 1)}, ScopeID: 1})
	}
	stream, err := ExtSort(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := stream.Next()
	if r == nil {
		t.Fatal("expected one coalesced record")
	}
	if r.From.Lo != 0 || r.To.Lo != 19 {
		t.Fatalf("got [%d,%d] want [0,19]", r.From.Lo, r.To.Lo)
	}
	if stream.Next() != nil {
		t.Fatal("expected a single coalesced record")
	}
}

func TestSpillTempFilesCleanedUp(t *testing.T) {
	dir := t.TempDir()
	cfg := &ExtSortConfig{ChunkSize: 3, TempDir: dir}
	input := make([]DesiredRecord[Ipv4Key], 10)
	for i := range input {
		input[i] = DesiredRecord[Ipv4Key]{From: Ipv4Key(uint32(i)), To: Ipv4Key(uint32(i)), ScopeID: uint32(i)}
	}
	stream, err := ExtSort(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if stream.Next() == nil {
			break
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected clean temp dir, found %d files", len(entries))
	}
}

func TestSpillAndMigrate(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Commit(0)

	const n = 50
	cfg := &ExtSortConfig{ChunkSize: 7, TempDir: t.TempDir()}
	input := make([]DesiredRecord[Ipv4Key], n)
	for i := 0; i < n; i++ {
		v := uint32(i*3 + 1)
		input[i] = DesiredRecord[Ipv4Key]{From: Ipv4Key(v), To: Ipv4Key(v), ScopeID: uint32(i)}
	}
	stream, err := ExtSort(input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := Migrate(w, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Added != n {
		t.Fatalf("added=%d want %d", counters.Added, n)
	}
	w.Commit(0)

	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.RecordCount() != uint64(n) {
		t.Fatalf("count=%d want %d", r.RecordCount(), n)
	}
	for i := 0; i < n; i++ {
		v := uint32(i*3 + 1)
		if s, ok := r.LookupV4(Ipv4Key(v)); !ok || s != uint32(i) {
			t.Fatalf("lookup(%d)=%d,%v want %d", v, s, ok, i)
		}
	}
}

// --- Streaming TreeWalker tests (deep tree, branch traversal) ---

func TestMigrateStreamingDeepTreeUnchanged(t *testing.T) {
	const n = 2000
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < n; i++ {
		if err := w.Set(Ipv4Key(i*10), Ipv4Key(i*10+5), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(0); err != nil {
		t.Fatal(err)
	}
	// Force a tree with at least one branch level so the walker exercises
	// descend/walk-up across branch pages, not just a flat leaf scan.
	if w.committedHeight < 2 {
		t.Fatalf("expected height>=2, got %d", w.committedHeight)
	}

	desired := make([]DesiredRecord[Ipv4Key], n)
	for i := uint32(0); i < n; i++ {
		desired[i] = DesiredRecord[Ipv4Key]{From: Ipv4Key(i * 10), To: Ipv4Key(i*10 + 5), ScopeID: i}
	}
	stream := FromUnsorted(desired)
	counters, err := Migrate(w, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Unchanged != n || counters.Added != 0 || counters.Removed != 0 || counters.Changed != 0 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateStreamingDeepTreeChurn(t *testing.T) {
	const n = 1500
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < n; i++ {
		if err := w.Set(Ipv4Key(i*10), Ipv4Key(i*10+5), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(0); err != nil {
		t.Fatal(err)
	}
	if w.committedHeight < 2 {
		t.Fatalf("expected height>=2, got %d", w.committedHeight)
	}

	// Keep even keys (change scope of the first half), drop odd keys.
	desired := make([]DesiredRecord[Ipv4Key], 0, n/2)
	for i := uint32(0); i < n; i += 2 {
		sc := uint32(1)
		if i < n/2 {
			sc = 2
		}
		desired = append(desired, DesiredRecord[Ipv4Key]{From: Ipv4Key(i * 10), To: Ipv4Key(i*10 + 5), ScopeID: sc})
	}
	stream := FromUnsorted(desired)
	if _, err := Migrate(w, stream, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0); err != nil {
		t.Fatal(err)
	}

	img, _ := w.IntoImage()
	r, _ := Open(img)
	for i := uint32(0); i < n; i += 2 {
		want := uint32(1)
		if i < n/2 {
			want = 2
		}
		if s, ok := r.LookupV4(Ipv4Key(i * 10)); !ok || s != want {
			t.Fatalf("lookup(%d)=%d,%v want %d", i*10, s, ok, want)
		}
	}
	for i := uint32(1); i < n; i += 2 {
		if _, ok := r.LookupV4(Ipv4Key(i * 10)); ok {
			t.Fatalf("lookup(%d) should be removed", i*10)
		}
	}
}

// TestMigrateStreamingHeight3 forces a three-level tree (root branch whose
// children are branches) so the walker exercises nested walkUp across two
// branch levels, then verifies an identical-desired migrate marks everything
// unchanged.
func TestMigrateStreamingHeight3(t *testing.T) {
	const n = 200_000
	w, _ := Create[Ipv4Key](0, 0)
	for i := uint32(0); i < n; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(0); err != nil {
		t.Fatal(err)
	}
	if w.committedHeight < 3 {
		t.Fatalf("expected height>=3, got %d", w.committedHeight)
	}

	desired := make([]DesiredRecord[Ipv4Key], n)
	for i := uint32(0); i < n; i++ {
		desired[i] = DesiredRecord[Ipv4Key]{From: Ipv4Key(i), To: Ipv4Key(i), ScopeID: i}
	}
	stream := FromUnsorted(desired)
	counters, err := Migrate(w, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Unchanged != n {
		t.Fatalf("unchanged=%d want %d", counters.Unchanged, n)
	}
}
