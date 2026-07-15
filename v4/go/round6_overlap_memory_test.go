package iprangedb

import (
	"math"
	"runtime"
	"testing"
)

func round6BitmapOverlapWriter(t *testing.T, records uint32) *Writer[Ipv4Key] {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < records; i++ {
		ip := Ipv4Key(i * 2)
		if err := w.Append(ip, ip, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	return w
}

func round6ForeignOverlapAllocatedBytes(t *testing.T, records uint32) uint64 {
	t.Helper()
	w := round6BitmapOverlapWriter(t, records)
	yielded := false
	callbacks := uint32(0)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	err := ForeignVsAll(w, func() (Ipv4Key, Ipv4Key, bool) {
		if yielded {
			return 0, 0, false
		}
		yielded = true
		return 0, Ipv4Key((records - 1) * 2), true
	}, func(uint32, uint32, uint64) {
		callbacks++
	})
	if err != nil {
		t.Fatal(err)
	}
	if callbacks != records*2 {
		t.Fatalf("overlap callbacks=%d, want %d", callbacks, records*2)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestRound6ForeignVsAllHeapDoesNotScaleWithStoredLeafCount(t *testing.T) {
	small := round6ForeignOverlapAllocatedBytes(t, 1000)
	large := round6ForeignOverlapAllocatedBytes(t, 500000)
	const tolerance = 4 << 10
	if large > small+tolerance {
		t.Fatalf("ForeignVsAll collected every stored leaf page: small=%d large=%d", small, large)
	}
}
