package iprangedb

import (
	"errors"
	"math"
	"testing"
)

type round4DesiredStream[K ipKey[K]] struct {
	records []DesiredRecord[K]
	pos     int
	err     error
}

func (s *round4DesiredStream[K]) Peek() *DesiredRecord[K] {
	if s.pos >= len(s.records) {
		return nil
	}
	return &s.records[s.pos]
}

func (s *round4DesiredStream[K]) Next() *DesiredRecord[K] {
	if s.pos >= len(s.records) {
		return nil
	}
	r := s.records[s.pos]
	s.pos++
	return &r
}

func (s *round4DesiredStream[K]) Err() error {
	if s.pos >= len(s.records) {
		return s.err
	}
	return nil
}

func TestNormalizeOverlappingPreservesFamilyMaximum(t *testing.T) {
	t.Run("IPv4", func(t *testing.T) {
		max := ^Ipv4Key(0)
		got := NormalizeOverlapping([]IntervalRecord[Ipv4Key]{
			{From: 10, To: max, Scope: 7},
		})
		if len(got) != 1 || got[0].From != 10 || got[0].To != max ||
			len(got[0].Scopes) != 1 || got[0].Scopes[0] != 7 {
			t.Fatalf("normalization = %#v, want [10..max] scope 7", got)
		}
	})

	t.Run("IPv6", func(t *testing.T) {
		from := Ipv6Key{Hi: 1, Lo: 10}
		max := Ipv6Key{Hi: math.MaxUint64, Lo: math.MaxUint64}
		got := NormalizeOverlapping([]IntervalRecord[Ipv6Key]{
			{From: from, To: max, Scope: 7},
		})
		if len(got) != 1 || got[0].From != from || got[0].To != max ||
			len(got[0].Scopes) != 1 || got[0].Scopes[0] != 7 {
			t.Fatalf("normalization = %#v, want [from..max] scope 7", got)
		}
	})
}

func TestMigrateSourceErrorPoisonsTransaction(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	stream := &round4DesiredStream[Ipv4Key]{
		records: []DesiredRecord[Ipv4Key]{{From: 10, To: 20, ScopeID: 7}},
		err:     errors.New("late source read failure"),
	}
	if _, err := Migrate(w, stream, nil); err == nil {
		t.Fatal("Migrate accepted a source that failed after yielding data")
	}
	if err := w.Commit(1, math.MaxUint64); err == nil {
		t.Fatal("Commit published partial migration state after Migrate returned an error")
	}
}

func TestMigrateRejectsMalformedDesiredStreamAndPoisonsTransaction(t *testing.T) {
	tests := []struct {
		name    string
		records []DesiredRecord[Ipv4Key]
	}{
		{
			name: "unsorted",
			records: []DesiredRecord[Ipv4Key]{
				{From: 100, To: 110, ScopeID: 1},
				{From: 10, To: 20, ScopeID: 2},
			},
		},
		{
			name: "overlapping",
			records: []DesiredRecord[Ipv4Key]{
				{From: 10, To: 20, ScopeID: 1},
				{From: 20, To: 30, ScopeID: 2},
			},
		},
		{
			name: "reversed",
			records: []DesiredRecord[Ipv4Key]{
				{From: 20, To: 10, ScopeID: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := Create[Ipv4Key](ScopeModeScalar, 0)
			if err != nil {
				t.Fatal(err)
			}
			stream := &round4DesiredStream[Ipv4Key]{records: tt.records}
			if _, err := Migrate(w, stream, nil); err == nil {
				t.Fatalf("Migrate accepted %s desired input", tt.name)
			}
			if err := w.Commit(1, math.MaxUint64); err == nil {
				t.Fatalf("Commit accepted transaction after %s migration failure", tt.name)
			}
		})
	}
}

func TestForeignVsAllMalformedInputNeverReturnsWrongCounts(t *testing.T) {
	t.Run("overlapping", func(t *testing.T) {
		w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Set(0, 30, 1); err != nil {
			t.Fatal(err)
		}
		foreign := []ForeignRange[Ipv4Key]{{From: 10, To: 20}, {From: 15, To: 25}}
		var total uint64
		err = ForeignVsAllFromSlice(w, foreign, func(_, _ uint32, n uint64) { total += n })
		if err == nil && total != 16 {
			t.Fatalf("overlapping input returned %d addresses, want union count 16 or an error", total)
		}
	})

	t.Run("unsorted", func(t *testing.T) {
		w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Set(0, 9, 1); err != nil {
			t.Fatal(err)
		}
		if err := w.Set(20, 29, 1); err != nil {
			t.Fatal(err)
		}
		foreign := []ForeignRange[Ipv4Key]{{From: 20, To: 25}, {From: 0, To: 5}}
		var total uint64
		err = ForeignVsAllFromSlice(w, foreign, func(_, _ uint32, n uint64) { total += n })
		if err == nil && total != 12 {
			t.Fatalf("unsorted input returned %d addresses, want exact count 12 or an error", total)
		}
	})

	t.Run("reversed", func(t *testing.T) {
		w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Set(0, 30, 1); err != nil {
			t.Fatal(err)
		}
		foreign := []ForeignRange[Ipv4Key]{{From: 20, To: 10}}
		if err := ForeignVsAllFromSlice(w, foreign, func(_, _ uint32, _ uint64) {}); err == nil {
			t.Fatal("ForeignVsAll accepted a range with from > to")
		}
	})
}

func TestAllToAllReportsOneTotalPerFeedPair(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(0, 9, 0b11); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(20, 29, 0b11); err != nil {
		t.Fatal(err)
	}
	var got []FeedOverlap
	if err := AllToAllOverlap(w, func(overlap FeedOverlap) { got = append(got, overlap) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (FeedOverlap{FeedA: 0, FeedB: 1, IPCount: 20}) {
		t.Fatalf("callbacks = %#v, want one total {0 1 20}", got)
	}
}

func TestOverlapRejectsScalarScopeMode(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 10, 123); err != nil {
		t.Fatal(err)
	}
	if err := AllToAllOverlap(w, func(FeedOverlap) {}); err == nil {
		t.Fatal("AllToAllOverlap silently returned empty data for scalar mode")
	}
	foreign := []ForeignRange[Ipv4Key]{{From: 1, To: 10}}
	if err := ForeignVsAllFromSlice(w, foreign, func(_, _ uint32, _ uint64) {}); err == nil {
		t.Fatal("ForeignVsAll silently returned empty data for scalar mode")
	}
}

func TestIPv6OverlapCountReportsOverflow(t *testing.T) {
	w, err := Create[Ipv6Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	// This inclusive span contains exactly 2^64 addresses. Zero can never be a
	// correct result, even before the API grows an exact wide-count type.
	if err := w.Set(Ipv6Key{}, Ipv6Key{Lo: math.MaxUint64}, 0b11); err != nil {
		t.Fatal(err)
	}
	if err := AllToAllOverlap(w, func(FeedOverlap) {}); err == nil {
		t.Fatal("2^64-address IPv6 overlap did not report uint64 overflow")
	}
}

func TestIndirectScopeFeedBitMustRoundTripWithoutTruncation(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	high, err := w.ScopeBitmapSetFeed(id, MaxBitmapWidth*8)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 1, high); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	storedID, ok := r.LookupV4(1)
	if !ok {
		t.Fatal("stored range is missing")
	}
	bitmap := r.ScopeResolve(storedID)
	if len(bitmap) <= MaxBitmapWidth || bitmap[MaxBitmapWidth]&1 == 0 {
		t.Fatalf("feed bit %d was silently lost; persisted bitmap length=%d", MaxBitmapWidth*8, len(bitmap))
	}
}

func TestIndirectScopeBitmapIsCanonical(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	original, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := w.ScopeBitmapSetFeed(original, 8)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := w.ScopeBitmapClearFeed(expanded, 8)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != original {
		t.Fatalf("identical membership minted another scope: original=%d cleared=%d bitmap=%v", original, cleared, w.ScopeResolve(cleared))
	}
}

func TestIndirectScopeMutationRejectsUnknownScope(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := w.ScopeBitmapSetFeed(999, 1); err == nil {
		t.Fatalf("setting an unknown scope silently returned scope %d", id)
	}
	if id, err := w.ScopeBitmapClearFeed(999, 1); err == nil {
		t.Fatalf("clearing an unknown scope silently returned scope %d", id)
	}
}

func TestIndirectRecordsRejectDanglingScopeIDs(t *testing.T) {
	t.Run("Set", func(t *testing.T) {
		w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Set(1, 10, 999); err == nil {
			t.Fatal("Set accepted a dangling scope ID")
		}
	})

	t.Run("Append", func(t *testing.T) {
		w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Append(1, 10, 999); err == nil {
			t.Fatal("Append accepted a dangling scope ID")
		}
	})
}

func TestMultiLevelScopeLookupRoutesAtEveryBoundary(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	const count = 8000
	for i := uint32(0); i < count; i++ {
		bitmap := []byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24)}
		id, err := w.ScopeIntern(bitmap)
		if err != nil || id != i+1 {
			t.Fatalf("ScopeIntern(%d): id=%d err=%v", i, id, err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1, 2, 7634, 7635, 7636, 7999, 8000} {
		want := id - 1
		got := w.ScopeResolve(id)
		if len(got) != 4 || got[0] != byte(want) || got[1] != byte(want>>8) ||
			got[2] != byte(want>>16) || got[3] != byte(want>>24) {
			t.Fatalf("scope %d resolved to %v, want little-endian %d", id, got, want)
		}
	}
}

func TestFeedAddAtFamilyMaximumDoesNotDuplicateTail(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	max := ^Ipv4Key(0)
	if err := w.FeedAddRange(10, max, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.FeedAddRange(10, max, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("FeedAddRange produced an invalid tree: %v", err)
	}
	var got []DesiredRecord[Ipv4Key]
	if err := r.ScanV4(func(from, to Ipv4Key, scope uint32) {
		got = append(got, DesiredRecord[Ipv4Key]{From: from, To: to, ScopeID: scope})
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (DesiredRecord[Ipv4Key]{From: 10, To: max, ScopeID: 3}) {
		t.Fatalf("result = %#v, want one [10..max] scope 3 record", got)
	}
}
