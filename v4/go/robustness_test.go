package iprangedb

// Hostile-input robustness (§9 / §10): Open and OpenImageV4 must return only a value or a
// typed error — never panic, loop, or read out of bounds — on truncations, bit-flips, and
// arbitrary buffers. (OpenImageV4 runs the same validation, so this also guards the
// writer's open path.) This is the Go port of v4/rust/iprange-livedb/tests/robustness.rs,
// using the shared LCG (oracle_test.go) so the two suites explore comparable inputs. In Go
// a panic fails the test, so no recover() is needed — none must occur.

import (
	"strconv"
	"testing"
)

// validRobustnessFile builds a multi-level valid file with some freed (unreachable) pages,
// to exercise both the reachable-page reject path and the unreachable-page ignore path.
func validRobustnessFile(t *testing.T) []byte {
	t.Helper()
	w := CreateV4(1, 0)
	for i := uint32(0); i < 2000; i++ {
		must(t, w.Set(wk(i*7), wk(i*7+2), []byte{byte(i & 0xff)}))
	}
	for i := uint32(0); i < 2000; i += 5 {
		must(t, w.Delete(wk(i*7), wk(i*7+2))) // frees pages
	}
	must(t, w.Commit(0))
	return append([]byte(nil), w.Image()...) // copy: Image aliases the writer buffer
}

func TestTruncationsNeverPanic(t *testing.T) {
	f := validRobustnessFile(t)
	two := 2 * pageSize
	// Every byte through the meta region (where bootstrap is most fragile), strided
	// beyond — opening any prefix must never panic.
	for length := 0; length < len(f); length++ {
		if length < two || length%37 == 0 {
			_, _ = Open(f[:length])
		}
	}
	_, _ = Open(f)
}

func TestSingleBitFlipsNeverPanic(t *testing.T) {
	f := validRobustnessFile(t)
	rng := lcg(0x9e3779b97f4a7c15)
	for i := 0; i < 5000; i++ {
		pos := int(rng()) % len(f)
		bit := rng() & 7
		g := append([]byte(nil), f...)
		g[pos] ^= 1 << bit
		_, _ = Open(g)
		_, _ = OpenImageV4(append([]byte(nil), g...))
	}
}

func TestArbitraryBuffersNeverPanic(t *testing.T) {
	rng := lcg(0x1234567890abcdef)
	sizes := []int{0, 1, 16, 100, 4095, 4096, 4097, 8191, 8192, 8193, 12288, 20000}
	for _, size := range sizes {
		for i := 0; i < 40; i++ {
			buf := make([]byte, size)
			for j := range buf {
				buf[j] = byte(rng())
			}
			_, _ = Open(buf)
		}
	}
}

func TestTreeRegionFlipNeverSilentlyAccepted(t *testing.T) {
	// A bit flip in the data region (pages >= 2) is either detected (a reachable page's
	// checksum fails => reject) or ignored (an unreachable/free page => same data). It is
	// never accepted as a *different* reachable tree. (Meta-region flips are not tested
	// here: tearing the active meta legitimately recovers the previous committed state,
	// §6.3 — covered by the writer's crash-recovery tests.)
	f := validRobustnessFile(t)
	r0, err := Open(f)
	if err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	base := robustScan(r0)

	two := 2 * pageSize
	rng := lcg(0xdeadbeefcafebabe)
	for i := 0; i < 4000; i++ {
		pos := two + int(rng())%(len(f)-two)
		bit := rng() & 7
		g := append([]byte(nil), f...)
		g[pos] ^= 1 << bit
		r, err := Open(g)
		if err != nil {
			continue // rejected: acceptable
		}
		assertScan(t, robustScan(r), base, "accepted a corrupted reachable tree")
	}
}

// robustScan collects an in-order scan as scanTriples for equality comparison.
func robustScan(r *Reader) []scanTriple {
	var out []scanTriple
	r.ScanV4(func(a, b Ipv4Key, sc []byte) {
		out = append(out, scanTriple{
			from:  strconv.FormatUint(uint64(a), 10),
			to:    strconv.FormatUint(uint64(b), 10),
			scope: append([]byte(nil), sc...),
		})
	})
	return out
}
