package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Reader benchmarks on the committed conformance fixtures plus the
// synthetic multi-level range and blob databases, so the deep trees are
// measured, not only the single-leaf corpus pages. Every benchmark opens
// its database once and measures the steady-state hot path.

func benchOpen(tb testing.TB, name string) *ImmutableReader {
	tb.Helper()
	path := copyFixture(tb, name, "bench-"+name)
	r, err := OpenImmutable(path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { r.Close() })
	return r
}

func BenchmarkLookupDirect4MultiLevel(b *testing.B) {
	path := buildMultiLevelDatabase(b)
	r, err := OpenImmutable(path)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := r.LookupDirect4(581000); err != nil || !found {
			b.Fatalf("lookup: %v %v", found, err)
		}
	}
}

func BenchmarkLookupDirect4MissMultiLevel(b *testing.B) {
	path := buildMultiLevelDatabase(b)
	r, err := OpenImmutable(path)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := r.LookupDirect4(1); err != nil || found {
			b.Fatalf("lookup: %v %v", found, err)
		}
	}
}

func BenchmarkLookupDirect6(b *testing.B) {
	r := benchOpen(b, "first-seen-ipv6.iprdb")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := r.LookupDirect6(0x20010db8, 1); err != nil || !found {
			b.Fatalf("lookup: %v %v", found, err)
		}
	}
}

func BenchmarkLookupFeed(b *testing.B) {
	r := benchOpen(b, "membership-ipv4.iprdb")
	feeds := []string{"feed-000", "feed-001", "feed-010", "feed-069"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := feeds[i%len(feeds)]
		if _, found, err := r.LookupFeed(name); err != nil || !found {
			b.Fatalf("lookup %s: %v %v", name, found, err)
		}
	}
}

func BenchmarkMembershipLookupWord(b *testing.B) {
	path := buildBlobDatabase(b)
	r, err := OpenImmutable(path)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	view, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		b.Fatalf("lookup: %v %v", found, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, err := view.Word(uint32(i) % 600); err != nil || !ok {
			b.Fatalf("word: %v %v", ok, err)
		}
	}
}

func BenchmarkStructuredLookup(b *testing.B) {
	r := benchOpen(b, "structured-ipv4.iprdb")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := r.LookupNetworkEnrichmentV14(0x0a010001); err != nil || !found {
			b.Fatalf("lookup: %v %v", found, err)
		}
	}
}

func BenchmarkScanDirect4(b *testing.B) {
	r := benchOpen(b, "direct-ipv4.iprdb")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		if err := r.ScanDirect4(func(RangeVisit4) error { n++; return nil }); err != nil {
			b.Fatal(err)
		}
		if n != 4 {
			b.Fatalf("scanned %d ranges", n)
		}
	}
}

func BenchmarkCardinality(b *testing.B) {
	r := benchOpen(b, "direct-ipv4.iprdb")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Cardinality(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormatDecodeRangeRecord is the raw codec cost used by range
// lookups; the profile attributes the remaining lookup cost to the search
// and page traversal.
func BenchmarkFormatDecodeRangeRecord(b *testing.B) {
	raw := make([]byte, format.RangeRecordV4Size)
	raw[0] = 1 // from = 1 (little-endian)
	raw[4] = 2 // to = 2
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := format.DecodeRangeRecordV4(raw); err != nil {
			b.Fatal(err)
		}
	}
}
