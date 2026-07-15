package iprangedb

import (
	"io"
	"runtime"
	"testing"
)

func round6ExtSorterFinishAllocatedBytes(t *testing.T, records int) uint64 {
	t.Helper()
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{
		ChunkSize: records,
		TempDir:   t.TempDir(),
	})
	for i := 0; i < records; i++ {
		ip := Ipv4Key(i * 2)
		if err := sorter.Add(ip, ip, 1); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(stream)
	if closer, ok := stream.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return after.TotalAlloc - before.TotalAlloc
}

func TestRound6ExternalSorterFinishHeapDoesNotScaleWithSingleRunSize(t *testing.T) {
	small := round6ExtSorterFinishAllocatedBytes(t, 100)
	large := round6ExtSorterFinishAllocatedBytes(t, 100000)
	const tolerance = 128 << 10
	if large > small+tolerance {
		t.Fatalf("external sorter Finish materialized its single spill run: small=%d large=%d", small, large)
	}
}
