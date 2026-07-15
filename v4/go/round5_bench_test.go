package iprangedb

import (
	"fmt"
	"math"
	"testing"
)

func BenchmarkRound5AllToAllPairScaling(b *testing.B) {
	for _, feeds := range []int{8, 16, 32, 64} {
		b.Run(fmt.Sprintf("feeds=%d", feeds), func(b *testing.B) {
			w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
			if err != nil {
				b.Fatal(err)
			}
			bitmap := make([]byte, (feeds+7)/8)
			for i := range bitmap {
				bitmap[i] = 0xff
			}
			scopeID, err := w.ScopeIntern(bitmap)
			if err != nil {
				b.Fatal(err)
			}
			if err := w.Set(0, 0, scopeID); err != nil {
				b.Fatal(err)
			}
			if err := w.Commit(0, math.MaxUint64); err != nil {
				b.Fatal(err)
			}
			pairCount := feeds * (feeds - 1) / 2
			b.ReportAllocs()
			b.SetBytes(int64(pairCount))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				callbacks := 0
				if err := AllToAllOverlap(w, func(FeedOverlap) { callbacks++ }); err != nil {
					b.Fatal(err)
				}
				if callbacks != pairCount {
					b.Fatalf("callbacks=%d, want %d", callbacks, pairCount)
				}
			}
		})
	}
}
