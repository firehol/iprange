// v4 benchmark harness (SOW-0013). Mirrors the Rust criterion benches.
// Run: go test -bench=. -benchmem -benchtime=2s
//
// Scenarios match the Rust benches (1-9). All use IPv4, scope_width=1.

package iprangedb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// benchLcg is the shared deterministic generator (identical constants to Rust).
type benchLcg uint64

func newBenchLcg(seed uint64) benchLcg { return benchLcg(seed ^ 0x9e3779b97f4a7c15) }

func (l *benchLcg) next() uint32 {
	*l = benchLcg(uint64(*l)*6364136223846793005 + 1442695040888963407)
	return uint32(uint64(*l) >> 33)
}

func genOrderedBench(n int) [][2]uint32 {
	rng := newBenchLcg(1)
	out := make([][2]uint32, 0, n)
	var cursor uint32
	for i := 0; i < n; i++ {
		gap := 1 + rng.next()%16
		width := rng.next() % 8
		start := cursor + gap
		end := start + width
		if start < cursor || end < start { // overflow
			break
		}
		out = append(out, [2]uint32{start, end})
		cursor = end
	}
	return out
}

func genRandomBench(n int) [][2]uint32 {
	rng := newBenchLcg(2)
	span := uint32(n * 10)
	if span < 1000 {
		span = 1000
	}
	out := make([][2]uint32, 0, n)
	for i := 0; i < n; i++ {
		a := rng.next() % span
		b := rng.next() % span
		if a <= b {
			out = append(out, [2]uint32{a, b})
		} else {
			out = append(out, [2]uint32{b, a})
		}
	}
	return out
}

func buildDBAppendBench(ranges [][2]uint32) []byte {
	w := CreateV4(1, 0)
	for _, r := range ranges {
		_ = w.Append(Ipv4Key(r[0]), Ipv4Key(r[1]), []byte{1})
	}
	_ = w.Commit(0)
	return w.Image()
}

var benchSizes = []int{10_000, 100_000, 1_000_000}

// --- Scenario 1: ordered read (scan) ---

func BenchmarkScan(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			img := buildDBAppendBench(ranges)
			r, err := Open(img)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				count := 0
				_ = r.ScanV4(func(_, _ Ipv4Key, _ []byte) { count++ })
			}
		})
	}
}

// --- Scenario 2: ordered write (append) ---

func BenchmarkAppend(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := CreateV4(1, 0)
				for _, r := range ranges {
					_ = w.Append(Ipv4Key(r[0]), Ipv4Key(r[1]), []byte{1})
				}
				_ = w.Commit(0)
			}
		})
	}
}

// --- Scenario 3: unordered write (set random) ---

func BenchmarkSetRandom(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genRandomBench(n)
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := CreateV4(1, 0)
				for _, r := range ranges {
					_ = w.Set(Ipv4Key(r[0]), Ipv4Key(r[1]), []byte{1})
				}
				_ = w.Commit(0)
			}
		})
	}
}

// --- Scenario 5: lookup hit ---

func BenchmarkLookupHit(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			img := buildDBAppendBench(ranges)
			r, err := Open(img)
			if err != nil {
				b.Fatal(err)
			}
			keys := make([]Ipv4Key, len(ranges))
			for i, rg := range ranges {
				keys[i] = Ipv4Key(rg[0] + (rg[1]-rg[0])/2)
			}
			b.SetBytes(int64(len(keys)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				found := 0
				for _, k := range keys {
					if _, ok, _ := r.LookupV4(k); ok {
						found++
					}
				}
				_ = found
			}
		})
	}
}

// --- Scenario 6: lookup miss ---

func BenchmarkLookupMiss(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			img := buildDBAppendBench(ranges)
			r, err := Open(img)
			if err != nil {
				b.Fatal(err)
			}
			var keys []Ipv4Key
			for i := 0; i+1 < len(ranges); i++ {
				gapStart := ranges[i][1] + 1
				gapEnd := ranges[i+1][0]
				if gapEnd > gapStart {
					keys = append(keys, Ipv4Key(gapStart+(gapEnd-gapStart)/2))
				}
			}
			b.SetBytes(int64(len(keys)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				found := 0
				for _, k := range keys {
					if _, ok, _ := r.LookupV4(k); ok {
						found++
					}
				}
				_ = found
			}
		})
	}
}

// --- Scenario 7: open read (trusted) ---

func BenchmarkOpenRead(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			img := buildDBAppendBench(ranges)
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := Open(img)
				if err != nil {
					b.Fatal(err)
				}
				_ = r
			}
		})
	}
}

// --- Scenario 7b: open + validate (full §9 walk) ---

func BenchmarkOpenValidate(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			ranges := genOrderedBench(n)
			img := buildDBAppendBench(ranges)
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := Open(img)
				if err != nil {
					b.Fatal(err)
				}
				if err := r.Validate(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// --- Scenario 7f: open read (file-backed MmapReader) ---

func BenchmarkOpenReadFile(b *testing.B) {
	for _, n := range benchSizes {
		path := filepath.Join(os.TempDir(), fmt.Sprintf("iprange-v4-bench-rd-%d-%d.iprdb", n, os.Getpid()))
		// Build the file once.
		ranges := genOrderedBench(n)
		fw, err := CreateFileWriterV4(path, 1, 0, 30*time.Second)
		if err != nil {
			b.Fatal(err)
		}
		for _, r := range ranges {
			_ = fw.Set(Ipv4Key(r[0]), Ipv4Key(r[1]), []byte{1})
		}
		_ = fw.Commit(0)
		_ = fw.Close()

		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mr, err := OpenMmap(path)
				if err != nil {
					b.Fatal(err)
				}
				_, err = mr.Reader()
				if err != nil {
					b.Fatal(err)
				}
				_ = mr.Close()
			}
		})
		_ = os.Remove(path)
	}
}

// --- Scenario 9: create file (full create + commit + close) ---

func BenchmarkCreateFile(b *testing.B) {
	for _, n := range benchSizes {
		ranges := genOrderedBench(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				path := filepath.Join(os.TempDir(), fmt.Sprintf("iprange-v4-bench-cr-%d-%d-%d.iprdb", n, os.Getpid(), i))
				fw, err := CreateFileWriterV4(path, 1, 0, 30*time.Second)
				if err != nil {
					b.Fatal(err)
				}
				for _, r := range ranges {
					_ = fw.Set(Ipv4Key(r[0]), Ipv4Key(r[1]), []byte{1})
				}
				_ = fw.Commit(0)
				_ = fw.Close()
				_ = os.Remove(path)
			}
		})
	}
}
