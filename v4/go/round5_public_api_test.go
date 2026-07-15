package iprangedb

import (
	"math"
	"path/filepath"
	"testing"
)

func TestRound5QueryCIDRsMergedCombinesSelectedAdjacentScopes(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []DesiredRecord[Ipv4Key]{
		{From: 0, To: 3, ScopeID: 1},
		{From: 4, To: 7, ScopeID: 2},
		{From: 8, To: 15, ScopeID: 1},
		{From: 20, To: 23, ScopeID: 2},
	} {
		if err := w.Set(record.From, record.To, record.ScopeID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := r.CursorV4()
	if err != nil {
		t.Fatal(err)
	}
	type cidr struct {
		network Ipv4Key
		prefix  uint8
	}
	var got []cidr
	if err := cursor.QueryCIDRsMerged(0, 23, func(scopeID uint32) bool { return scopeID != 0 }, func(network Ipv4Key, prefix uint8) error {
		got = append(got, cidr{network: network, prefix: prefix})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []cidr{{network: 0, prefix: 28}, {network: 20, prefix: 30}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("CIDRs=%#v, want %#v", got, want)
	}
}

func TestRound5IPv6MergedCIDRAtFamilyMaximum(t *testing.T) {
	max := Ipv6Key{Hi: math.MaxUint64, Lo: math.MaxUint64}
	from := Ipv6Key{Hi: math.MaxUint64, Lo: math.MaxUint64 - 3}
	w, err := Create[Ipv6Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(from, max, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := r.CursorV6()
	if err != nil {
		t.Fatal(err)
	}
	var networks []Ipv6Key
	var prefixes []uint8
	if err := cursor.QueryCIDRsMerged(from, max, func(scopeID uint32) bool { return scopeID == 1 }, func(network Ipv6Key, prefix uint8) error {
		networks = append(networks, network)
		prefixes = append(prefixes, prefix)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 || networks[0] != from || prefixes[0] != 126 {
		t.Fatalf("family-max CIDRs=%v/%v, want %v/126", networks, prefixes, from)
	}
	if count := cursor.CountIPs(from, max, func(scopeID uint32) bool { return scopeID == 1 }); count != (Uint128{Lo: 4}) {
		t.Fatalf("CountIPs=%#v, want 4", count)
	}
}

func TestRound5FileWriterDelegatesMigrationAndOverlap(t *testing.T) {
	t.Run("migration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "migration.iprdb")
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer fw.Close()
		stream := FromUnsorted([]DesiredRecord[Ipv4Key]{
			{From: 30, To: 39, ScopeID: 2},
			{From: 10, To: 19, ScopeID: 1},
		})
		counters, err := fw.Migrate(stream, nil)
		if err != nil {
			t.Fatal(err)
		}
		if counters.Added != 2 {
			t.Fatalf("migration counters=%+v", counters)
		}
		if err := fw.Commit(1); err != nil {
			t.Fatal(err)
		}
		if fw.RecordCount() != 2 {
			t.Fatalf("record count=%d, want 2", fw.RecordCount())
		}
	})

	t.Run("overlap", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "overlap.iprdb")
		fw, err := CreateFile[Ipv4Key](path, ScopeModeBitmap, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer fw.Close()
		if err := fw.Set(10, 19, 0b11); err != nil {
			t.Fatal(err)
		}
		if err := fw.Commit(1); err != nil {
			t.Fatal(err)
		}
		var pairs []FeedOverlap
		if err := fw.AllToAllOverlap(func(overlap FeedOverlap) { pairs = append(pairs, overlap) }); err != nil {
			t.Fatal(err)
		}
		if len(pairs) != 1 || pairs[0] != (FeedOverlap{FeedA: 0, FeedB: 1, IPCount: 10}) {
			t.Fatalf("pairs=%#v", pairs)
		}
		counts := make(map[uint32]uint64)
		if err := fw.ForeignVsAllFromSlice([]ForeignRange[Ipv4Key]{{From: 12, To: 15}}, func(feed, _ uint32, n uint64) {
			counts[feed] += n
		}); err != nil {
			t.Fatal(err)
		}
		if counts[0] != 4 || counts[1] != 4 {
			t.Fatalf("foreign counts=%v", counts)
		}
	})
}

func TestRound5AllToAllOverlapEmitsOneSortedDeterministicResultPerPair(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []DesiredRecord[Ipv4Key]{
		{From: 0, To: 9, ScopeID: 0b0111},
		{From: 20, To: 24, ScopeID: 0b1010},
		{From: 30, To: 34, ScopeID: 0b0101},
	} {
		if err := w.Set(record.From, record.To, record.ScopeID); err != nil {
			t.Fatal(err)
		}
	}
	want := []FeedOverlap{
		{FeedA: 0, FeedB: 1, IPCount: 10},
		{FeedA: 0, FeedB: 2, IPCount: 15},
		{FeedA: 1, FeedB: 2, IPCount: 10},
		{FeedA: 1, FeedB: 3, IPCount: 5},
	}
	for run := 0; run < 2; run++ {
		var got []FeedOverlap
		if err := AllToAllOverlap(w, func(overlap FeedOverlap) { got = append(got, overlap) }); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("run %d overlaps=%#v, want %#v", run, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run %d overlaps=%#v, want %#v", run, got, want)
			}
		}
	}
}
