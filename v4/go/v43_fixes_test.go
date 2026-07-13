package iprangedb

import (
	"math"
	"testing"
)

// ─── IntervalDiff tests (ported from interval.rs) ───

func irec(f, t, s uint32) IntervalRecord[Ipv4Key] {
	return IntervalRecord[Ipv4Key]{From: Ipv4Key(f), To: Ipv4Key(t), Scope: s}
}

func segKind[K ipKey[K]](s *DiffSegment[K]) string {
	switch s.Kind() {
	case SegmentAdded:
		return "added"
	case SegmentRemoved:
		return "removed"
	case SegmentChanged:
		return "changed"
	case SegmentUnchanged:
		return "unchanged"
	}
	return "?"
}

func TestIntervalDiffEmptyToFull(t *testing.T) {
	old := []IntervalRecord[Ipv4Key]{}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 1 || segs[0].Kind() != SegmentAdded {
		t.Fatalf("got %d segs, kind=%s", len(segs), segKind(&segs[0]))
	}
}

func TestIntervalDiffFullToEmpty(t *testing.T) {
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{}
	segs := IntervalDiff(old, desired)
	if len(segs) != 1 || segs[0].Kind() != SegmentRemoved {
		t.Fatalf("got %d segs, kind=%s", len(segs), segKind(&segs[0]))
	}
}

func TestIntervalDiffIdentical(t *testing.T) {
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 1 || segs[0].Kind() != SegmentUnchanged {
		t.Fatalf("got %d segs, kind=%s", len(segs), segKind(&segs[0]))
	}
}

func TestIntervalDiffChangeScope(t *testing.T) {
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 20, 2)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 1 || segs[0].Kind() != SegmentChanged {
		t.Fatalf("got %d segs, kind=%s", len(segs), segKind(&segs[0]))
	}
}

func TestIntervalDiffPartialOverlapOldExtends(t *testing.T) {
	// old: [10-20], desired: [10-15] → [10-15] unchanged, [16-20] removed
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 15, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 2 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentUnchanged || segs[0].From != 10 || segs[0].To != 15 {
		t.Fatalf("seg0: kind=%s [%v,%v]", segKind(&segs[0]), uint32(segs[0].From), uint32(segs[0].To))
	}
	if segs[1].Kind() != SegmentRemoved || segs[1].From != 16 || segs[1].To != 20 {
		t.Fatalf("seg1: kind=%s [%v,%v]", segKind(&segs[1]), uint32(segs[1].From), uint32(segs[1].To))
	}
}

func TestIntervalDiffPartialOverlapDesiredExtends(t *testing.T) {
	// old: [10-15], desired: [10-20] → [10-15] unchanged, [16-20] added
	old := []IntervalRecord[Ipv4Key]{irec(10, 15, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 2 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentUnchanged {
		t.Fatalf("seg0: kind=%s", segKind(&segs[0]))
	}
	if segs[1].Kind() != SegmentAdded || segs[1].From != 16 || segs[1].To != 20 {
		t.Fatalf("seg1: kind=%s [%v,%v]", segKind(&segs[1]), uint32(segs[1].From), uint32(segs[1].To))
	}
}

func TestIntervalDiffOneToMany(t *testing.T) {
	// old: [10-30], desired: [10-15],[20-30]
	// → [10-15] unchanged, [16-19] removed, [20-30] unchanged
	old := []IntervalRecord[Ipv4Key]{irec(10, 30, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 15, 1), irec(20, 30, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 3 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentUnchanged {
		t.Fatalf("seg0: kind=%s", segKind(&segs[0]))
	}
	if segs[1].Kind() != SegmentRemoved || segs[1].From != 16 || segs[1].To != 19 {
		t.Fatalf("seg1: kind=%s [%v,%v]", segKind(&segs[1]), uint32(segs[1].From), uint32(segs[1].To))
	}
	if segs[2].Kind() != SegmentUnchanged {
		t.Fatalf("seg2: kind=%s", segKind(&segs[2]))
	}
}

func TestIntervalDiffManyToOne(t *testing.T) {
	// old: [10-15],[20-30], desired: [10-30]
	// → [10-15] unchanged, [16-19] added, [20-30] unchanged
	old := []IntervalRecord[Ipv4Key]{irec(10, 15, 1), irec(20, 30, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(10, 30, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 3 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentUnchanged {
		t.Fatalf("seg0: kind=%s", segKind(&segs[0]))
	}
	if segs[1].Kind() != SegmentAdded || segs[1].From != 16 || segs[1].To != 19 {
		t.Fatalf("seg1: kind=%s [%v,%v]", segKind(&segs[1]), uint32(segs[1].From), uint32(segs[1].To))
	}
	if segs[2].Kind() != SegmentUnchanged {
		t.Fatalf("seg2: kind=%s", segKind(&segs[2]))
	}
}

func TestIntervalDiffDisjoint(t *testing.T) {
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(30, 40, 1)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 2 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentRemoved {
		t.Fatalf("seg0: kind=%s", segKind(&segs[0]))
	}
	if segs[1].Kind() != SegmentAdded {
		t.Fatalf("seg1: kind=%s", segKind(&segs[1]))
	}
}

func TestIntervalDiffOverlappingDifferentScope(t *testing.T) {
	// old: [10-20] scope=1, desired: [15-25] scope=2
	// → [10-14] removed(1), [15-20] changed(1→2), [21-25] added(2)
	old := []IntervalRecord[Ipv4Key]{irec(10, 20, 1)}
	desired := []IntervalRecord[Ipv4Key]{irec(15, 25, 2)}
	segs := IntervalDiff(old, desired)
	if len(segs) != 3 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentRemoved || segs[0].From != 10 || segs[0].To != 14 {
		t.Fatalf("seg0: kind=%s [%v,%v]", segKind(&segs[0]), uint32(segs[0].From), uint32(segs[0].To))
	}
	if segs[1].Kind() != SegmentChanged || segs[1].From != 15 || segs[1].To != 20 {
		t.Fatalf("seg1: kind=%s [%v,%v]", segKind(&segs[1]), uint32(segs[1].From), uint32(segs[1].To))
	}
	if segs[2].Kind() != SegmentAdded || segs[2].From != 21 || segs[2].To != 25 {
		t.Fatalf("seg2: kind=%s [%v,%v]", segKind(&segs[2]), uint32(segs[2].From), uint32(segs[2].To))
	}
}

func TestIntervalDiffIPv6(t *testing.T) {
	old := []IntervalRecord[Ipv6Key]{
		{From: Ipv6Key{Hi: 0, Lo: 10}, To: Ipv6Key{Hi: 0, Lo: 20}, Scope: 1},
	}
	desired := []IntervalRecord[Ipv6Key]{
		{From: Ipv6Key{Hi: 0, Lo: 15}, To: Ipv6Key{Hi: 0, Lo: 25}, Scope: 2},
	}
	segs := IntervalDiff(old, desired)
	if len(segs) != 3 {
		t.Fatalf("got %d segs", len(segs))
	}
	if segs[0].Kind() != SegmentRemoved {
		t.Fatalf("seg0: kind=%s", segKind(&segs[0]))
	}
}

// ─── NormalizeOverlapping tests (ported from interval.rs) ───

func TestNormalizeNoOverlap(t *testing.T) {
	input := []IntervalRecord[Ipv4Key]{irec(10, 20, 1), irec(30, 40, 2)}
	segs := NormalizeOverlapping(input)
	if len(segs) != 2 {
		t.Fatalf("got %d segs", len(segs))
	}
	if len(segs[0].Scopes) != 1 || segs[0].Scopes[0] != 1 {
		t.Fatalf("seg0 scopes=%v", segs[0].Scopes)
	}
	if len(segs[1].Scopes) != 1 || segs[1].Scopes[0] != 2 {
		t.Fatalf("seg1 scopes=%v", segs[1].Scopes)
	}
}

func TestNormalizePartialOverlap(t *testing.T) {
	input := []IntervalRecord[Ipv4Key]{irec(10, 20, 1), irec(15, 25, 2)}
	segs := NormalizeOverlapping(input)
	if len(segs) != 3 {
		t.Fatalf("got %d segs", len(segs))
	}
	// [10-14]=[1], [15-20]=[1,2], [21-25]=[2]
	if segs[0].From != 10 || segs[0].To != 14 || len(segs[0].Scopes) != 1 {
		t.Fatalf("seg0: [%v,%v] scopes=%v", uint32(segs[0].From), uint32(segs[0].To), segs[0].Scopes)
	}
	if segs[1].From != 15 || segs[1].To != 20 || len(segs[1].Scopes) != 2 {
		t.Fatalf("seg1: [%v,%v] scopes=%v", uint32(segs[1].From), uint32(segs[1].To), segs[1].Scopes)
	}
	if segs[2].From != 21 || segs[2].To != 25 || len(segs[2].Scopes) != 1 {
		t.Fatalf("seg2: [%v,%v] scopes=%v", uint32(segs[2].From), uint32(segs[2].To), segs[2].Scopes)
	}
}

func TestNormalizeTripleOverlap(t *testing.T) {
	input := []IntervalRecord[Ipv4Key]{irec(10, 30, 1), irec(15, 25, 2), irec(20, 35, 3)}
	segs := NormalizeOverlapping(input)
	if len(segs) != 5 {
		t.Fatalf("got %d segs", len(segs))
	}
	// [10-14]=1, [15-19]=1+2, [20-25]=1+2+3, [26-30]=1+3, [31-35]=3
	if len(segs[2].Scopes) != 3 {
		t.Fatalf("seg2 scopes=%v want 3", segs[2].Scopes)
	}
}

// ─── ExtSorter streaming API tests (fixes #1) ───

func TestExtSorterSmall(t *testing.T) {
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 100, TempDir: t.TempDir()})
	sorter.Add(Ipv4Key(30), Ipv4Key(40), 1)
	sorter.Add(Ipv4Key(10), Ipv4Key(20), 2)
	sorter.Add(Ipv4Key(5), Ipv4Key(8), 3)
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r1 := stream.Next()
	if r1 == nil || r1.From != 5 {
		t.Fatalf("first=%v", r1)
	}
	r2 := stream.Next()
	if r2 == nil || r2.From != 10 {
		t.Fatalf("second=%v", r2)
	}
	r3 := stream.Next()
	if r3 == nil || r3.From != 30 {
		t.Fatalf("third=%v", r3)
	}
	if stream.Next() != nil {
		t.Fatal("expected EOF")
	}
}

func TestExtSorterSpill(t *testing.T) {
	cfg := &ExtSortConfig{ChunkSize: 10, TempDir: t.TempDir()}
	sorter := NewExtSorter[Ipv4Key](cfg)
	for i := uint32(0); i < 25; i++ {
		if err := sorter.Add(Ipv4Key(1000-i), Ipv4Key(1000-i), i); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := sorter.Finish()
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
			t.Fatalf("not sorted at %d", count)
		}
		prev = r.From
		count++
	}
	if count != 25 {
		t.Fatalf("count=%d want 25", count)
	}
}

func TestExtSorterEmpty(t *testing.T) {
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 10})
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if stream.Next() != nil {
		t.Fatal("expected EOF")
	}
}

func TestExtSorterNormalizeOverlapping(t *testing.T) {
	// Overlapping input with different scopes → last-wins
	cfg := &ExtSortConfig{ChunkSize: 100, TempDir: t.TempDir()}
	sorter := NewExtSorter[Ipv4Key](cfg)
	sorter.Add(Ipv4Key(10), Ipv4Key(20), 1)
	sorter.Add(Ipv4Key(15), Ipv4Key(25), 2)
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r1 := stream.Next()
	if r1 == nil || r1.From != 10 || r1.To != 14 || r1.ScopeID != 1 {
		t.Fatalf("r1=%v", r1)
	}
	r2 := stream.Next()
	if r2 == nil || r2.From != 15 || r2.To != 25 || r2.ScopeID != 2 {
		t.Fatalf("r2=%v", r2)
	}
	if stream.Next() != nil {
		t.Fatal("expected EOF")
	}
}

func TestExtSorterSpillNormalized(t *testing.T) {
	// Overlapping input across spill boundaries should normalize correctly.
	cfg := &ExtSortConfig{ChunkSize: 5, TempDir: t.TempDir()}
	sorter := NewExtSorter[Ipv4Key](cfg)
	for i := uint32(0); i < 10; i++ {
		sorter.Add(Ipv4Key(i*2), Ipv4Key(i*2+1), 1)
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r := stream.Next()
	if r == nil || r.From != 0 || r.To != 19 {
		t.Fatalf("got [%v,%v] want [0,19]", uint32(r.From), uint32(r.To))
	}
	if stream.Next() != nil {
		t.Fatal("expected single coalesced record")
	}
}

// ─── normalizeChunk tests (fixes #4) ───

func TestNormalizeChunkDifferentScope(t *testing.T) {
	// [10-20] scope=1, [15-25] scope=2 → last-wins: [10-14]s1, [15-25]s2
	sorted := []DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(15), To: Ipv4Key(25), ScopeID: 2},
	}
	seqs := []uint64{0, 1}
	out, _ := normalizeChunk(sorted, seqs)
	if len(out) != 2 {
		t.Fatalf("got %d segments", len(out))
	}
	if out[0].From != 10 || out[0].To != 14 || out[0].ScopeID != 1 {
		t.Fatalf("seg0: [%v,%v] s=%d", uint32(out[0].From), uint32(out[0].To), out[0].ScopeID)
	}
	if out[1].From != 15 || out[1].To != 25 || out[1].ScopeID != 2 {
		t.Fatalf("seg1: [%v,%v] s=%d", uint32(out[1].From), uint32(out[1].To), out[1].ScopeID)
	}
}

func TestNormalizeChunkSameScope(t *testing.T) {
	// [10-20] scope=1, [15-25] scope=1 → merge: [10-25]s1
	sorted := []DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(15), To: Ipv4Key(25), ScopeID: 1},
	}
	seqs := []uint64{0, 1}
	out, _ := normalizeChunk(sorted, seqs)
	if len(out) != 1 {
		t.Fatalf("got %d segments", len(out))
	}
	if out[0].From != 10 || out[0].To != 25 || out[0].ScopeID != 1 {
		t.Fatalf("seg0: [%v,%v] s=%d", uint32(out[0].From), uint32(out[0].To), out[0].ScopeID)
	}
}

// ─── sweep-line normalizeChunk edge cases (ported from extsort.rs) ───

func TestNormalizeChunkTailPreserved(t *testing.T) {
	// [56-69]s0, [60-75]s1, [63-72]s0 → [56-59]s0, [60-62]s1, [63-72]s0, [73-75]s1 (tail!)
	sorted := []DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(56), To: Ipv4Key(69), ScopeID: 0},
		{From: Ipv4Key(60), To: Ipv4Key(75), ScopeID: 1},
		{From: Ipv4Key(63), To: Ipv4Key(72), ScopeID: 0},
	}
	seqs := []uint64{0, 1, 2}
	out, _ := normalizeChunk(sorted, seqs)
	if len(out) != 4 {
		t.Fatalf("got %d segments want 4: %+v", len(out), out)
	}
	if out[3].From != 73 || out[3].To != 75 {
		t.Fatalf("tail seg: [%v,%v] want [73,75]", uint32(out[3].From), uint32(out[3].To))
	}
}

func TestNormalizeChunkMaxAddress(t *testing.T) {
	// [MAX-10, MAX]s1 → single record, no end event (checkedInc fails at max)
	sorted := []DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(maxUint32 - 10), To: Ipv4Key(maxUint32), ScopeID: 1},
	}
	seqs := []uint64{0}
	out, _ := normalizeChunk(sorted, seqs)
	if len(out) != 1 {
		t.Fatalf("got %d segments want 1", len(out))
	}
	if out[0].To != Ipv4Key(maxUint32) {
		t.Fatalf("seg0 to=%v want MAX", uint32(out[0].To))
	}
}

func TestFromUnsortedInMemoryCoalesce(t *testing.T) {
	// [30-40]s1, [10-20]s1, [21-29]s1, [50-60]s2 → [10-40]s1, [50-60]s2
	s := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(30), To: Ipv4Key(40), ScopeID: 1},
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
		{From: Ipv4Key(21), To: Ipv4Key(29), ScopeID: 1},
		{From: Ipv4Key(50), To: Ipv4Key(60), ScopeID: 2},
	})
	if len(s.records) != 2 {
		t.Fatalf("got %d records want 2", len(s.records))
	}
	if s.records[0].From != 10 || s.records[0].To != 40 {
		t.Fatalf("seg0: [%v,%v] want [10,40]", uint32(s.records[0].From), uint32(s.records[0].To))
	}
}

func TestCrossRunOverlapSplit(t *testing.T) {
	// chunk_size=1 → each record is its own run. Cross-run overlap with
	// different scopes must be split: [10-14]s1, [15-25]s2.
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: t.TempDir()})
	sorter.Add(Ipv4Key(10), Ipv4Key(20), 1)
	sorter.Add(Ipv4Key(15), Ipv4Key(25), 2)
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r1 := stream.Next()
	if r1 == nil || r1.From != 10 || r1.To != 14 {
		t.Fatalf("r1=%v want [10,14]", r1)
	}
	r2 := stream.Next()
	if r2 == nil || r2.From != 15 || r2.To != 25 {
		t.Fatalf("r2=%v want [15,25]", r2)
	}
	if stream.Next() != nil {
		t.Fatal("expected EOF")
	}
}

func TestSortedStreamClone(t *testing.T) {
	original := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
	})
	original.Next() // advance pos to 1
	clone := original.Clone()
	if clone.pos != 1 {
		t.Fatalf("clone pos=%d want 1", clone.pos)
	}
	r := clone.Next()
	if r != nil {
		t.Fatal("clone should be exhausted")
	}
}

func TestScopeRegistryHashMapIntern(t *testing.T) {
	reg := NewScopeRegistry()
	id1, _ := reg.Intern([]byte{0x01})
	id2, _ := reg.Intern([]byte{0x03})
	id1b, _ := reg.Intern([]byte{0x01})
	if id1 != 1 || id2 != 2 || id1b != 1 {
		t.Fatalf("intern: %d %d %d", id1, id2, id1b)
	}
}

func TestScopeRegistryFromEntriesBuildsIndex(t *testing.T) {
	entries := []ScopeEntry{
		{ScopeID: 1, Bitmap: []byte{0xAB}},
		{ScopeID: 2, Bitmap: []byte{0xCD}},
	}
	reg := ScopeRegistryFromEntries(entries)
	// Interning an existing bitmap should return the existing ID (O(1) via map).
	id, _ := reg.Intern([]byte{0xAB})
	if id != 1 {
		t.Fatalf("expected id=1, got %d", id)
	}
	id, _ = reg.Intern([]byte{0xCD})
	if id != 2 {
		t.Fatalf("expected id=2, got %d", id)
	}
	// New bitmap gets a new ID.
	id, _ = reg.Intern([]byte{0xEF})
	if id != 3 {
		t.Fatalf("expected id=3, got %d", id)
	}
}

// ─── Multi-level scope tree test (fixes #6: was 7635 limit) ───

func TestBuildScopeTreeMultiLevel(t *testing.T) {
	// With > 7635 entries, the old code returned an error. The new code builds
	// multi-level branch pages. We need a vecPageStore to build into.
	w, _ := Create[Ipv4Key](ScopeModeIndirect, 0)
	const n = 8000
	for i := uint32(0); i < n; i++ {
		bm := make([]byte, i/8+1)
		bm[i/8] = 1 << (i % 8)
		w.ScopeIntern(bm)
	}
	// This commit builds the scope tree — previously it would fail for > 7635.
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatalf("commit with %d scopes failed: %v", n, err)
	}

	// Verify all scopes are readable via readAllScopes.
	img, _ := w.IntoImage()
	store := newVecPageStore(img)
	metaA := decodeMeta(store.page(0))
	metaB := decodeMeta(store.page(1))
	metaActive := metaA
	if metaB.txnID > metaA.txnID {
		metaActive = metaB
	}
	if metaActive.scopeTableRoot == 0 {
		t.Fatal("no scope table root")
	}
	entries, err := readAllScopes(store.committedBytes(), metaActive.scopeTableRoot)
	if err != nil {
		t.Fatalf("readAllScopes: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("got %d entries want %d", len(entries), n)
	}
}

// ─── Feed-bit range API tests (fixes #5) ───

func TestFeedAddRangeBitmapMode(t *testing.T) {
	// Mode 1 (bitmap): scope_id IS the bitmap.
	w, _ := Create[Ipv4Key](ScopeModeBitmap, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1) // feed bit 0 set for [10-20]

	// Add feed bit 1 to [15-25].
	if err := w.FeedAddRange(Ipv4Key(15), Ipv4Key(25), 1); err != nil {
		t.Fatal(err)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)

	// [10-14]: only feed 0 → scope=1 (binary 01)
	s, ok := r.LookupV4(Ipv4Key(12))
	if !ok || s != 1 {
		t.Fatalf("lookup(12)=%d,%v want 1", s, ok)
	}
	// [15-20]: feeds 0+1 → scope=3 (binary 11)
	s, ok = r.LookupV4(Ipv4Key(17))
	if !ok || s != 3 {
		t.Fatalf("lookup(17)=%d,%v want 3", s, ok)
	}
	// [21-25]: only feed 1 → scope=2 (binary 10)
	s, ok = r.LookupV4(Ipv4Key(23))
	if !ok || s != 2 {
		t.Fatalf("lookup(23)=%d,%v want 2", s, ok)
	}
}

func TestFeedAddRangeGapFill(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeBitmap, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)

	// Add feed bit 2 to [5-30] — covers existing [10-20] plus gaps.
	w.FeedAddRange(Ipv4Key(5), Ipv4Key(30), 2)
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)

	// [5-9]: only feed 2 → scope=4 (binary 100)
	s, _ := r.LookupV4(Ipv4Key(7))
	if s != 4 {
		t.Fatalf("lookup(7)=%d want 4", s)
	}
	// [10-20]: feeds 0+2 → scope=5 (binary 101)
	s, _ = r.LookupV4(Ipv4Key(15))
	if s != 5 {
		t.Fatalf("lookup(15)=%d want 5", s)
	}
	// [21-30]: only feed 2 → scope=4
	s, _ = r.LookupV4(Ipv4Key(25))
	if s != 4 {
		t.Fatalf("lookup(25)=%d want 4", s)
	}
}

func TestFeedRemoveRangeBitmapMode(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeBitmap, 0)
	w.Set(Ipv4Key(10), Ipv4Key(30), 3) // feeds 0+1

	// Remove feed bit 0 from [15-25].
	w.FeedRemoveRange(Ipv4Key(15), Ipv4Key(25), 0)
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)

	// [10-14]: still feeds 0+1 → scope=3
	s, _ := r.LookupV4(Ipv4Key(12))
	if s != 3 {
		t.Fatalf("lookup(12)=%d want 3", s)
	}
	// [15-25]: only feed 1 → scope=2
	s, _ = r.LookupV4(Ipv4Key(20))
	if s != 2 {
		t.Fatalf("lookup(20)=%d want 2", s)
	}
	// [26-30]: still feeds 0+1 → scope=3
	s, _ = r.LookupV4(Ipv4Key(28))
	if s != 3 {
		t.Fatalf("lookup(28)=%d want 3", s)
	}
}

func TestFeedRemoveRangeClearsRecord(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeBitmap, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1) // only feed 0

	// Remove feed 0 from the entire range → record disappears.
	w.FeedRemoveRange(Ipv4Key(10), Ipv4Key(20), 0)
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if _, ok := r.LookupV4(Ipv4Key(15)); ok {
		t.Fatal("record should be gone")
	}
}

func TestFeedAddRangeIndirectMode(t *testing.T) {
	// Mode 2 (indirect): scope_id → bitmap via registry.
	w, _ := Create[Ipv4Key](ScopeModeIndirect, 0)
	id, _ := w.ScopeIntern([]byte{0x01}) // feed 0
	w.Set(Ipv4Key(10), Ipv4Key(20), id)

	// Add feed bit 3 to [15-25].
	w.FeedAddRange(Ipv4Key(15), Ipv4Key(25), 3)
	w.Commit(0, math.MaxUint64)

	// Verify: [15-20] should have both feed 0 and feed 3.
	img, _ := w.IntoImage()
	r, _ := Open(img)
	s, ok := r.LookupV4(Ipv4Key(17))
	if !ok {
		t.Fatal("lookup(17) failed")
	}
	bm := r.ScopeResolve(s)
	if bm == nil {
		t.Fatal("scope_resolve failed")
	}
	if len(bm) == 0 || bm[0]&1 == 0 || bm[0]&8 == 0 {
		t.Fatalf("bitmap wrong: %v (want bits 0+3)", bm)
	}
}

// ─── Reader.ScopeResolve test (fixes #7) ───

func TestReaderScopeResolve(t *testing.T) {
	w, _ := Create[Ipv4Key](ScopeModeIndirect, 0)
	id1, _ := w.ScopeIntern([]byte{0x01})
	id2, _ := w.ScopeIntern([]byte{0x02, 0x03})
	w.Set(Ipv4Key(10), Ipv4Key(20), id1)
	w.Set(Ipv4Key(30), Ipv4Key(40), id2)
	w.Commit(0, math.MaxUint64)

	img, _ := w.IntoImage()
	r, _ := Open(img)

	bm := r.ScopeResolve(id1)
	if bm == nil || len(bm) != 1 || bm[0] != 0x01 {
		t.Fatalf("scope_resolve(%d)=%v want [0x01]", id1, bm)
	}
	bm = r.ScopeResolve(id2)
	if bm == nil || len(bm) != 2 || bm[0] != 0x02 || bm[1] != 0x03 {
		t.Fatalf("scope_resolve(%d)=%v want [0x02,0x03]", id2, bm)
	}
	// Non-existent scope.
	if r.ScopeResolve(999) != nil {
		t.Fatal("expected nil for unknown scope")
	}
}

func TestReaderScopeResolveWrongMode(t *testing.T) {
	// Mode 0 (scalar): scope_resolve returns nil.
	w, _ := Create[Ipv4Key](ScopeModeScalar, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if r.ScopeResolve(1) != nil {
		t.Fatal("expected nil in scalar mode")
	}
}

// ─── Migrate sweep-line boundary tests (fixes #3) ───
// These test partial-overlap cases that the OLD merge couldn't handle.

func TestMigratePartialOverlapOldExtends(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [10-15] → [10-15] unchanged, [16-20] removed.
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(15), ScopeID: 1},
	})
	counters, err := Migrate(w, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Unchanged != 1 || counters.Removed != 1 {
		t.Fatalf("counters=%+v", counters)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if s, ok := r.LookupV4(Ipv4Key(12)); !ok || s != 1 {
		t.Fatalf("lookup(12)=%d,%v", s, ok)
	}
	if _, ok := r.LookupV4(Ipv4Key(18)); ok {
		t.Fatal("18 should be removed")
	}
}

func TestMigratePartialOverlapDesiredExtends(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(15), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [10-20] → [10-15] unchanged, [16-20] added.
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 1},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Unchanged != 1 || counters.Added != 1 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateOverlappingDifferentScope(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [15-25] scope=2
	// → [10-14] removed(1), [15-20] changed(1→2), [21-25] added(2)
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(15), To: Ipv4Key(25), ScopeID: 2},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Removed != 1 || counters.Changed != 1 || counters.Added != 1 {
		t.Fatalf("counters=%+v", counters)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if _, ok := r.LookupV4(Ipv4Key(12)); ok {
		t.Fatal("12 should be removed")
	}
	if s, ok := r.LookupV4(Ipv4Key(17)); !ok || s != 2 {
		t.Fatalf("lookup(17)=%d,%v want 2", s, ok)
	}
	if s, ok := r.LookupV4(Ipv4Key(23)); !ok || s != 2 {
		t.Fatalf("lookup(23)=%d,%v want 2", s, ok)
	}
}

func TestMigrateOneToMany(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(30), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [10-15],[20-30] → [10-15] unchanged, [16-19] removed, [20-30] unchanged
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(15), ScopeID: 1},
		{From: Ipv4Key(20), To: Ipv4Key(30), ScopeID: 1},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Unchanged != 2 || counters.Removed != 1 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateManyToOne(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(15), 1)
	w.Set(Ipv4Key(20), Ipv4Key(30), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [10-30] → [10-15] unchanged, [16-19] added, [20-30] unchanged
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(30), ScopeID: 1},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Unchanged != 2 || counters.Added != 1 {
		t.Fatalf("counters=%+v", counters)
	}
}

func TestMigrateBoundaryIPs(t *testing.T) {
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(0), Ipv4Key(100), 1)
	w.Commit(0, math.MaxUint64)

	// Desired: [0-50] scope=2 → [0-50] changed, [51-100] removed
	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(0), To: Ipv4Key(50), ScopeID: 2},
	})
	counters, _ := Migrate(w, desired, nil)
	if counters.Changed != 1 || counters.Removed != 1 {
		t.Fatalf("counters=%+v", counters)
	}
}

// Oracle-based random migration test: compare migrate result against a
// brute-force BTreeMap oracle.
func TestMigrateRandomOracle(t *testing.T) {
	rngState := uint64(42)
	nextRand := func() uint32 {
		rngState = rngState*6364136223846793005 + 1442695040888963407
		return uint32(rngState >> 32)
	}

	for trial := 0; trial < 30; trial++ {
		// Build old tree.
		var oldRecords [][3]uint32
		nOld := int(nextRand()%6) + 1
		for i := 0; i < nOld; i++ {
			f := nextRand() % 200
			t := f + nextRand() % 30
			oldRecords = append(oldRecords, [3]uint32{f, t, nextRand() % 3})
		}
		w, _ := Create[Ipv4Key](0, 0)
		for _, r := range oldRecords {
			w.Set(Ipv4Key(r[0]), Ipv4Key(r[1]), r[2])
		}
		w.Commit(0, math.MaxUint64)

		// Build desired.
		var desired []DesiredRecord[Ipv4Key]
		nDes := int(nextRand()%6) + 1
		for i := 0; i < nDes; i++ {
			f := nextRand() % 200
			t := f + nextRand() % 30
			desired = append(desired, DesiredRecord[Ipv4Key]{
				From: Ipv4Key(f), To: Ipv4Key(t), ScopeID: nextRand() % 3,
			})
		}
		stream := FromUnsorted(desired)
		if _, err := Migrate(w, stream, nil); err != nil {
			t.Fatalf("trial %d: migrate error: %v", trial, err)
		}
		w.Commit(0, math.MaxUint64)

		// Build result map.
		img, _ := w.IntoImage()
		r, _ := Open(img)
		result := make(map[uint32]uint32)
		r.ScanV4(func(from, to Ipv4Key, scopeID uint32) {
			for ip := uint32(from); ip <= uint32(to); ip++ {
				result[ip] = scopeID
			}
		})

		// Build expected map (desired last-wins by INPUT order — F8 semantics:
		// a later input record overrides earlier ones regardless of key order).
		expected := make(map[uint32]uint32)
		for _, d := range desired {
			for ip := uint32(d.From); ip <= uint32(d.To); ip++ {
				expected[ip] = d.ScopeID
			}
		}

		// Compare.
		if len(result) != len(expected) {
			t.Fatalf("trial %d: result=%d expected=%d\n  old=%v\n  desired=%v",
				trial, len(result), len(expected), oldRecords, desired)
		}
		for ip, sc := range expected {
			if rs, ok := result[ip]; !ok || rs != sc {
				t.Fatalf("trial %d: ip=%d result=%d,%v expected=%d\n  old=%v\n  desired=%v",
					trial, ip, rs, ok, sc, oldRecords, desired)
			}
		}
	}
}

func sortByFrom(records []DesiredRecord[Ipv4Key]) {
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && records[j].From.cmp(records[j-1].From) < 0; j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
}

// ─── worker-bug fixes (ported from extsort.rs `worker_bugs`) ───

func TestNormalizeChunkMaxAddressOverlap(t *testing.T) {
	// [MAX-10, MAX] scope=1 overlaps [MAX-5, MAX] scope=2.
	// Expected: [MAX-10, MAX-6] scope=1, [MAX-5, MAX] scope=2.
	// Without the synthetic u128-max end event, the second segment is lost.
	s := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(maxUint32 - 10), To: Ipv4Key(maxUint32), ScopeID: 1},
		{From: Ipv4Key(maxUint32 - 5), To: Ipv4Key(maxUint32), ScopeID: 2},
	})
	if len(s.records) != 2 {
		t.Fatalf("got %d segments want 2: %+v", len(s.records), s.records)
	}
	if s.records[0].From != Ipv4Key(maxUint32-10) || s.records[0].To != Ipv4Key(maxUint32-6) || s.records[0].ScopeID != 1 {
		t.Fatalf("seg0: [%v,%v] s=%d want [MAX-10,MAX-6] s=1",
			uint32(s.records[0].From), uint32(s.records[0].To), s.records[0].ScopeID)
	}
	if s.records[1].From != Ipv4Key(maxUint32-5) || s.records[1].To != Ipv4Key(maxUint32) || s.records[1].ScopeID != 2 {
		t.Fatalf("seg1: [%v,%v] s=%d want [MAX-5,MAX] s=2",
			uint32(s.records[1].From), uint32(s.records[1].To), s.records[1].ScopeID)
	}
}

func TestCrossRunContainedTail(t *testing.T) {
	// Run A: [10-30] scope=1 (wide), Run B: [15-25] scope=2 (contained in A).
	// chunk_size=1 → each record is its own run.
	// Expected after merge: [10-14]s1, [15-25]s2, [26-30]s1 (tail preserved).
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: t.TempDir()})
	sorter.Add(Ipv4Key(10), Ipv4Key(30), 1)
	sorter.Add(Ipv4Key(15), Ipv4Key(25), 2)
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	r1 := stream.Next()
	if r1 == nil || r1.From != 10 || r1.To != 14 || r1.ScopeID != 1 {
		t.Fatalf("r1=%v want [10,14]s1", r1)
	}
	r2 := stream.Next()
	if r2 == nil || r2.From != 15 || r2.To != 25 || r2.ScopeID != 2 {
		t.Fatalf("r2=%v want [15,25]s2", r2)
	}
	r3 := stream.Next()
	if r3 == nil || r3.From != 26 || r3.To != 30 || r3.ScopeID != 1 {
		t.Fatalf("r3=%v want [26,30]s1", r3)
	}
	if stream.Next() != nil {
		t.Fatal("expected EOF after 3 segments")
	}
}
