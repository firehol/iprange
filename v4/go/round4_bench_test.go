package iprangedb

import (
	"fmt"
	"testing"
)

// BenchmarkNestedNormalization records the scaling curve for the adversarial
// containment pattern that previously made normalization quadratic.
func BenchmarkNestedNormalization(b *testing.B) {
	for _, count := range []int{2000, 4000, 8000, 16000} {
		records := make([]DesiredRecord[Ipv4Key], count)
		for i := range records {
			records[i] = DesiredRecord[Ipv4Key]{
				From:    0,
				To:      Ipv4Key(count - i),
				ScopeID: uint32(i%7 + 1),
			}
		}
		b.Run(fmt.Sprintf("records=%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(count))
			for i := 0; i < b.N; i++ {
				stream := FromUnsorted(records)
				if stream == nil {
					b.Fatal("nil stream")
				}
			}
		})
	}
}
