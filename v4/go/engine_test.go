package iprangedb

import "testing"

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
