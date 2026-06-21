package iprangeformat

import (
	"testing"
	"time"
)

// lcg is the same deterministic generator used in the Rust robustness/bench harness,
// so both languages benchmark the identical workload.
type lcg struct{ s uint64 }

func newLcg(seed uint64) *lcg { return &lcg{s: seed ^ 0x9e37_79b9_7f4a_7c15} }
func (l *lcg) next() uint32 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return uint32(l.s >> 33)
}

func sampleMeta() FeedMeta {
	return FeedMeta{Name: "firehol_level1", Category: "attacks", License: "GPL-2.0"}
}

func buildSampleV4(t *testing.T) []byte {
	t.Helper()
	w := NewWriterV4(sampleMeta(), licenseFlagDontRedist, 1700)
	if err := w.AddRange(0x0a00_0000, 0x0a00_00ff, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.AddRange(0x0b00_0000, 0x0b00_000f, &Value{TypeID: 2, Bytes: []byte{7}}); err != nil {
		t.Fatal(err)
	}
	b, err := w.Build()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTripAndLookups(t *testing.T) {
	b := buildSampleV4(t)
	r, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.IsIPv6() {
		t.Fatal("expected IPv4")
	}
	if r.RecordCount() != 2 {
		t.Fatalf("record count = %d", r.RecordCount())
	}
	fm, err := r.FeedMeta()
	if err != nil {
		t.Fatal(err)
	}
	if fm.Name != "firehol_level1" || fm.License != "GPL-2.0" {
		t.Fatalf("feed-meta = %+v", fm)
	}
	// inside first range -> present, no value.
	if h, found, _ := r.LookupV4(0x0a00_0080); !found || h.ValueID != valueIDNone {
		t.Fatalf("lookup 1: found=%v hit=%+v", found, h)
	}
	// inside second range -> value present.
	h, found, _ := r.LookupV4(0x0b00_0005)
	if !found || h.ValueID == valueIDNone {
		t.Fatalf("lookup 2: found=%v hit=%+v", found, h)
	}
	v, ok := r.Value(h.ValueID)
	if !ok || v.TypeID != 2 || len(v.Bytes) != 1 || v.Bytes[0] != 7 {
		t.Fatalf("value = %+v ok=%v", v, ok)
	}
	// gaps / boundaries.
	if _, found, _ := r.LookupV4(0x0c00_0000); found {
		t.Fatal("gap should miss")
	}
	if _, found, _ := r.LookupV4(0x0a00_0100); found {
		t.Fatal("just past range should miss")
	}
}

func TestOwnedMutableIdempotent(t *testing.T) {
	b := buildSampleV4(t)
	r, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.ToWriterV4()
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := w.Build()
	if err != nil {
		t.Fatal(err)
	}
	if string(rebuilt) != string(b) {
		t.Fatal("reload+rebuild must be byte-idempotent")
	}
}

func TestPanicSafetyMutations(t *testing.T) {
	base := buildSampleV4(t)
	for i := range base {
		for _, xor := range []byte{0x01, 0x80, 0xff} {
			b := append([]byte(nil), base...)
			b[i] ^= xor
			_, _ = Open(b) // must return, never panic.
		}
	}
	rng := newLcg(0xC0FFEE)
	for _, n := range []int{0, 1, 8, 71, 72, 73, 200, 1024} {
		for k := 0; k < 64; k++ {
			buf := make([]byte, n)
			for j := range buf {
				buf[j] = byte(rng.next())
			}
			_, _ = Open(buf)
			_, _ = OpenMetadataOnly(buf)
		}
	}
}

// genWorkload builds N ascending disjoint v4 ranges using the shared LCG.
func genWorkload(n int) [][3]uint32 {
	rng := newLcg(1)
	spec := make([][3]uint32, 0, n)
	var cursor uint32
	for i := 0; i < n; i++ {
		gap := 1 + rng.next()%16
		width := rng.next() % 8
		start := cursor + gap
		if start < cursor {
			break
		}
		end := start + width
		if end < start {
			break
		}
		spec = append(spec, [3]uint32{start, end, 0})
		cursor = end
	}
	return spec
}

// TestSpeedReport is the early Rust-vs-Go speed check (run with -v). It is not a
// pass/fail gate; it prints wall-clock for build + lookup over the shared workload.
func TestSpeedReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping speed report in -short")
	}
	const n = 200_000
	const lookups = 1_000_000
	spec := genWorkload(n)

	t0 := time.Now()
	w := NewWriterV4(sampleMeta(), 0, 1700)
	for _, s := range spec {
		if err := w.AddRange(Ipv4Key(s[0]), Ipv4Key(s[1]), nil); err != nil {
			t.Fatal(err)
		}
	}
	bytes, err := w.Build()
	if err != nil {
		t.Fatal(err)
	}
	buildDur := time.Since(t0)

	r, err := Open(bytes)
	if err != nil {
		t.Fatal(err)
	}

	rng := newLcg(99)
	var hits int
	t1 := time.Now()
	for i := 0; i < lookups; i++ {
		key := Ipv4Key(rng.next())
		if _, found, _ := r.LookupV4(key); found {
			hits++
		}
	}
	lookupDur := time.Since(t1)

	t.Logf("GO  build: %d ranges -> %d bytes in %v (%.1f ns/range)",
		len(spec), len(bytes), buildDur, float64(buildDur.Nanoseconds())/float64(len(spec)))
	t.Logf("GO  lookup: %d lookups in %v (%.1f ns/op), hit rate %.1f%%",
		lookups, lookupDur, float64(lookupDur.Nanoseconds())/float64(lookups), 100*float64(hits)/float64(lookups))
}
