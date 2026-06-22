package iprangeformat

import "testing"

func feedMetaName(name string) FeedMeta { return FeedMeta{Name: name} }

func r4(a, b uint32) [2]Ipv4Key { return [2]Ipv4Key{Ipv4Key(a), Ipv4Key(b)} }

// idsAt looks up an IPv4 address in a merged reader and returns the ascending feed-ids
// covering it (nil if not found).
func idsAt(t *testing.T, rd *Reader, ip uint32) []uint32 {
	t.Helper()
	hit, found, err := rd.LookupV4(Ipv4Key(ip))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return nil
	}
	vr, ok := rd.Value(hit.ValueID)
	if !ok {
		t.Fatalf("value_id %d not resolvable", hit.ValueID)
	}
	out := make([]uint32, 0, len(vr.Bytes)/4)
	for i := 0; i < len(vr.Bytes); i += 4 {
		out = append(out, le.Uint32(vr.Bytes[i:]))
	}
	return out
}

func TestMergePartialOverlapLookups(t *testing.T) {
	m := NewMergeWriterV4(feedMetaName("merged"), 0, 1700000000)
	// added out of feed_id order — output must not depend on input order.
	if err := m.AddFeed(5, feedMetaName("b"), [][2]Ipv4Key{r4(5, 15)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddFeed(1, feedMetaName("a"), [][2]Ipv4Key{r4(0, 10)}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}
	rd, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := rd.RecordCount(); got != 3 {
		t.Fatalf("record count = %d, want 3 ({1},{1,5},{5})", got)
	}
	check := func(ip uint32, want []uint32) {
		got := idsAt(t, rd, ip)
		if len(got) != len(want) {
			t.Fatalf("ids(%d) = %v, want %v", ip, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ids(%d) = %v, want %v", ip, got, want)
			}
		}
	}
	check(0, []uint32{1})
	check(4, []uint32{1})
	check(5, []uint32{1, 5})
	check(10, []uint32{1, 5})
	check(11, []uint32{5})
	check(15, []uint32{5})
	if idsAt(t, rd, 16) != nil {
		t.Fatal("16 should be in no feed")
	}

	cat, err := rd.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) != 2 || cat[0].FeedID != 1 || cat[0].Meta.Name != "a" || cat[1].FeedID != 5 || cat[1].Meta.Name != "b" {
		t.Fatalf("catalog = %+v", cat)
	}
}

func TestMergeDeterministicByteIdentical(t *testing.T) {
	mk := func() []byte {
		m := NewMergeWriterV4(feedMetaName("merged"), 0, 42)
		_ = m.AddFeed(5, feedMetaName("b"), [][2]Ipv4Key{r4(5, 15)})
		_ = m.AddFeed(1, feedMetaName("a"), [][2]Ipv4Key{r4(0, 10)})
		out, err := m.Build()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := mk(), mk()
	if len(a) != len(b) {
		t.Fatalf("non-deterministic length %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at byte %d", i)
		}
	}
}

func TestMergeEmptyHasCatalog(t *testing.T) {
	m := NewMergeWriterV4(feedMetaName("merged"), 0, 0)
	if err := m.AddFeed(1, feedMetaName("a"), nil); err != nil {
		t.Fatal(err)
	}
	out, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}
	rd, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if rd.RecordCount() != 0 {
		t.Fatalf("empty merged file should have 0 records, got %d", rd.RecordCount())
	}
	cat, err := rd.Catalog()
	if err != nil || len(cat) != 1 {
		t.Fatalf("catalog = %+v err=%v", cat, err)
	}
}

func TestMergeRejections(t *testing.T) {
	// no feeds.
	if _, err := NewMergeWriterV4(feedMetaName("m"), 0, 0).Build(); err == nil {
		t.Fatal("empty merge should be rejected")
	}
	// duplicate feed_id.
	m := NewMergeWriterV4(feedMetaName("m"), 0, 0)
	_ = m.AddFeed(1, feedMetaName("a"), [][2]Ipv4Key{r4(0, 10)})
	_ = m.AddFeed(1, feedMetaName("b"), [][2]Ipv4Key{r4(20, 30)})
	if _, err := m.Build(); err == nil {
		t.Fatal("duplicate feed_id should be rejected")
	}
	// within-feed overlap.
	m2 := NewMergeWriterV4(feedMetaName("m"), 0, 0)
	_ = m2.AddFeed(1, feedMetaName("a"), [][2]Ipv4Key{r4(0, 10), r4(5, 15)})
	if _, err := m2.Build(); err == nil {
		t.Fatal("within-feed overlap should be rejected")
	}
}
