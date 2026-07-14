package iprangedb

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Issue 5: an end-to-end timing comparison between the v4 binary engine and
// the legacy C `iprange` binary over a realistic 100k-IP ipset.
//
// What is compared (indicative, NOT like-for-like):
//   - C path:  `iprange <file>` reads a text ipset, merges/overlaps adjacent
//     ranges, and writes minimized text output.
//   - v4 path: build a mode-0 (scalar) Writer, append 100k single-IP ranges,
//     commit, then scan the whole tree (the "queryable structure" outcome).
//
// The two do different work (text minimize vs. binary B+tree build), so this is
// a rough sanity check that v4 is in the right ballpark — not a strict benchmark.
// It is a printing test (t.Logf), not a reporting benchmark, per the task.
func TestIssue5_V4VsCBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark comparison skipped in -short mode")
	}
	const cBin = "/home/costa/src/firehol/iprange/iprange"
	if _, err := os.Stat(cBin); err != nil {
		t.Skipf("C iprange binary not found at %s: %v", cBin, err)
	}
	const n = 100_000

	// Deterministic random IPs written to a temp text file (one IP per line).
	dir := t.TempDir()
	ipFile := filepath.Join(dir, "ips.txt")
	f, err := os.Create(ipFile)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(42))
	var wbuf bytes.Buffer
	for i := 0; i < n; i++ {
		ip := rng.Uint32()
		fmt.Fprintf(&wbuf, "%d.%d.%d.%d\n", byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
	}
	if _, err := f.Write(wbuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// ── C iprange: load + minimize + output to /dev/null ──
	cBest := time.Duration(0)
	for trial := 0; trial < 3; trial++ {
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(cBin, ipFile)
		cmd.Stdout = devnull
		cmd.Stderr = &bytes.Buffer{}
		start := time.Now()
		if err := cmd.Run(); err != nil {
			devnull.Close()
			t.Fatalf("C iprange failed: %v", err)
		}
		elapsed := time.Since(start)
		devnull.Close()
		if cBest == 0 || elapsed < cBest {
			cBest = elapsed
		}
	}

	// ── v4: build writer + append n + commit + scan ──
	v4Best := time.Duration(0)
	for trial := 0; trial < 3; trial++ {
		w, err := Create[Ipv4Key](ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		// Re-derive the same IPs from the seed so the workload matches the
		// text file (avoids re-reading it).
		rng2 := rand.New(rand.NewSource(42))
		for i := 0; i < n; i++ {
			ip := rng2.Uint32()
			if err := w.Append(Ipv4Key(ip), Ipv4Key(ip), 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Commit(1, ^uint64(0)); err != nil {
			t.Fatal(err)
		}
		count := uint64(0)
		if err := w.Scan(func(_, _ Ipv4Key, _ uint32) { count++ }); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		if v4Best == 0 || elapsed < v4Best {
			v4Best = elapsed
		}
		if count == 0 {
			t.Fatal("scan returned 0 records")
		}
	}

	t.Logf("=== Issue 5: v4 vs C iprange (100k IPs, best-of-3) ===")
	t.Logf("  C iprange (text load+minimize+output): %v", cBest)
	t.Logf("  v4 engine  (build+commit+scan):         %v", v4Best)
	if cBest > 0 {
		t.Logf("  ratio v4/C: %.2fx", float64(v4Best)/float64(cBest))
	}
}
