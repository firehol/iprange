package iprangedb

import (
	"sort"
	"testing"
)

// Issue 4: ForeignVsAll must accept a streaming closure so a caller can feed
// foreign ranges from a file/iterator without materializing them into a slice.
// ForeignVsAllFromSlice wraps a slice for backward compatibility and must
// produce identical results to the closure form.
func TestIssue4_ForeignVsAllClosureAndSlice(t *testing.T) {
	// Build a mode-1 (bitmap) writer: [10-20] feed 0, [30-40] feeds 1+2.
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(10), Ipv4Key(20), 0b001); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(30), Ipv4Key(40), 0b110); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}

	// Foreign feed: [15-35] (overlaps both stored ranges).
	foreignRanges := []ForeignRange[Ipv4Key]{
		{From: Ipv4Key(15), To: Ipv4Key(35)},
	}

	// Closure form: stream from an index.
	i := 0
	nextForeign := func() (Ipv4Key, Ipv4Key, bool) {
		if i >= len(foreignRanges) {
			var zero Ipv4Key
			return zero, zero, false
		}
		r := foreignRanges[i]
		i++
		return r.From, r.To, true
	}

	var gotClosure []FeedOverlap
	// Collect (feed, ipCount) pairs (foreignID is always 0).
	if err := ForeignVsAll(w, nextForeign, func(feed, foreignID uint32, ipCount uint64) {
		gotClosure = append(gotClosure, FeedOverlap{FeedA: feed, FeedB: foreignID, IPCount: ipCount})
	}); err != nil {
		t.Fatal(err)
	}

	// Slice form.
	var gotSlice []FeedOverlap
	if err := ForeignVsAllFromSlice(w, foreignRanges, func(feed, foreignID uint32, ipCount uint64) {
		gotSlice = append(gotSlice, FeedOverlap{FeedA: feed, FeedB: foreignID, IPCount: ipCount})
	}); err != nil {
		t.Fatal(err)
	}

	// Both must report feed 0 over [15-20] (6 IPs) and feeds 1,2 over [30-35] (6 IPs).
	normalize := func(v []FeedOverlap) []FeedOverlap {
		sort.Slice(v, func(a, b int) bool { return v[a].FeedA < v[b].FeedA })
		return v
	}
	gotClosure = normalize(gotClosure)
	gotSlice = normalize(gotSlice)

	want := []FeedOverlap{
		{FeedA: 0, IPCount: 6}, // [15-20] overlaps feed 0 → 6 IPs
		{FeedA: 1, IPCount: 6}, // [30-35] overlaps feed 1 → 6 IPs
		{FeedA: 2, IPCount: 6}, // [30-35] overlaps feed 2 → 6 IPs
	}
	if !feedOverlapsEqual(gotClosure, want) {
		t.Fatalf("closure form mismatch:\n got  %v\n want %v", gotClosure, want)
	}
	if !feedOverlapsEqual(gotSlice, want) {
		t.Fatalf("slice form mismatch:\n got  %v\n want %v", gotSlice, want)
	}
}

func feedOverlapsEqual(a, b []FeedOverlap) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
