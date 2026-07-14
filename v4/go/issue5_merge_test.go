package iprangedb

import (
	"sort"
	"testing"
)

// Issue 5: ForeignVsAll must produce identical overlap counts to the per-range
// descent, across multiple foreign ranges that each overlap multiple records,
// including ranges that fall in gaps (no overlap) and ranges that span the
// whole tree. Exercises the linear-merge cursor.
func TestIssue5_ForeignVsAllMergeCorrectness(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	// feeds 0+1 over [10-20], feeds 1+2 over [30-40], feed 0 over [50-60]
	if err := w.Set(Ipv4Key(10), Ipv4Key(20), 0b011); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(30), Ipv4Key(40), 0b110); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(50), Ipv4Key(60), 0b001); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	// Foreign ranges sorted by `from` (merge precondition):
	//   [5-8]   → gap before first record (no overlap)
	//   [15-35] → overlaps [10-20] (feed 0,1) and [30-40] (feed 1,2)
	//   [45-45] → gap between records (no overlap)
	//   [55-70] → overlaps [50-60] (feed 0)
	foreign := []ForeignRange[Ipv4Key]{
		{From: Ipv4Key(5), To: Ipv4Key(8)},
		{From: Ipv4Key(15), To: Ipv4Key(35)},
		{From: Ipv4Key(45), To: Ipv4Key(45)},
		{From: Ipv4Key(55), To: Ipv4Key(70)},
	}

	var got []FeedOverlap
	if err := ForeignVsAllFromSlice(w, foreign, func(feed, foreignID uint32, ipCount uint64) {
		got = append(got, FeedOverlap{FeedA: feed, FeedB: foreignID, IPCount: ipCount})
	}); err != nil {
		t.Fatal(err)
	}

	// Expected (feed, ip_count):
	//   [15-20] → feed 0 (6), feed 1 (6)
	//   [30-35] → feed 1 (6), feed 2 (6)
	//   [55-60] → feed 0 (6)
	want := []FeedOverlap{
		{FeedA: 0, IPCount: 6},
		{FeedA: 1, IPCount: 6},
		{FeedA: 1, IPCount: 6},
		{FeedA: 2, IPCount: 6},
		{FeedA: 0, IPCount: 6},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].FeedA < got[j].FeedA })
	sort.Slice(want, func(i, j int) bool { return want[i].FeedA < want[j].FeedA })
	if !feedOverlapsEqual(got, want) {
		t.Fatalf("merge produced wrong overlaps:\n got  %v\n want %v", got, want)
	}
}

// Issue 5: two adjacent foreign ranges that BOTH overlap the same record.
// Guards the cursor-advance logic (records overlapped by one range must remain
// visible to the next).
func TestIssue5_ForeignVsAllMergeAdjacentRanges(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(100), Ipv4Key(200), 0b001); err != nil { // feed 0, 101 IPs
		t.Fatal(err)
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	foreign := []ForeignRange[Ipv4Key]{
		{From: Ipv4Key(110), To: Ipv4Key(120)}, // 11 IPs of feed 0
		{From: Ipv4Key(150), To: Ipv4Key(160)}, // 11 IPs of feed 0
	}
	var total uint64
	if err := ForeignVsAllFromSlice(w, foreign, func(feed, foreignID uint32, ipCount uint64) {
		total += ipCount
	}); err != nil {
		t.Fatal(err)
	}
	if total != 22 {
		t.Fatalf("adjacent ranges should each report their slice: got %d want 22", total)
	}
}

// Issue 5: scale — 1000 records x 1000 foreign ranges. Asserts the merge
// visits every overlap exactly once (no double-count, no skip).
func TestIssue5_ForeignVsAllMergeScaled(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(10000+i), Ipv4Key(10000+i), 0b001); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	foreign := make([]ForeignRange[Ipv4Key], 1000)
	for i := uint32(0); i < 1000; i++ {
		foreign[i] = ForeignRange[Ipv4Key]{From: Ipv4Key(10000 + i), To: Ipv4Key(10000 + i)}
	}
	var hits uint64
	if err := ForeignVsAllFromSlice(w, foreign, func(feed, foreignID uint32, ipCount uint64) {
		hits++
	}); err != nil {
		t.Fatal(err)
	}
	if hits != 1000 {
		t.Fatalf("every foreign range must hit its record exactly once: got %d want 1000", hits)
	}
}

// Issue 5: foreign ranges that each span MULTIPLE records, ensuring the merge
// correctly continues scanning within a foreign range across leaf boundaries.
func TestIssue5_ForeignVsAllMergeMultiRecordRange(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Use indirect mode to exercise ScopeResolveRef (issue-6) on the hot path.
	id, err := w.ScopeIntern([]byte{0b001}) // feed 0
	if err != nil {
		t.Fatal(err)
	}
	// Records [0-9], [20-29], [40-49] each feed 0.
	for _, start := range []uint32{0, 20, 40} {
		if err := w.Set(Ipv4Key(start), Ipv4Key(start+9), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	// One foreign range spanning all three records: [0-49] = 50 IPs of feed 0.
	foreign := []ForeignRange[Ipv4Key]{
		{From: Ipv4Key(0), To: Ipv4Key(49)},
	}
	var total uint64
	if err := ForeignVsAllFromSlice(w, foreign, func(feed, foreignID uint32, ipCount uint64) {
		total += ipCount
	}); err != nil {
		t.Fatal(err)
	}
	if total != 30 { // 3 records x 10 IPs = 30
		t.Fatalf("multi-record span wrong: got %d want 30", total)
	}
}
