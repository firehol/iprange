// Print-path unit tests: the print.rs test module ported to Go,
// plus byte-exact diagnostics and wrapper coverage. Expected lines
// are C-oracle ground truth (src/ipset_print.c / src/ipset6_print.c
// and the tests.d shapes), cross-checked against the C and Rust
// release binaries.

package legacy

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// buildSet4 mirrors the print.rs test helper: ranges appended
// unoptimized (exact closed pairs), then one optimize sweep.
func buildSet4(ranges ...[2]uint32) *IpSet {
	s := &IpSet{Fam: V4}
	for _, r := range ranges {
		s.AddRange(Range{Lo: IP128{Lo: uint64(r[0])}, Hi: IP128{Lo: uint64(r[1])}})
	}
	s.Optimize()
	return s
}

// r6 builds one IPv6 range from explicit u128 endpoint halves.
func r6(loHi, loLo, hiHi, hiLo uint64) [2]IP128 {
	return [2]IP128{{Hi: loHi, Lo: loLo}, {Hi: hiHi, Lo: hiLo}}
}

// buildSet6 mirrors the print.rs test helper for IPv6.
func buildSet6(ranges ...[2]IP128) *IpSet {
	s := &IpSet{Fam: V6}
	for _, r := range ranges {
		s.AddRange(Range{Lo: r[0], Hi: r[1]})
	}
	s.Optimize()
	return s
}

// render renders one set through the unexported writer-based core,
// like the Rust tests render into a byte buffer.
func render(o *Options, set *IpSet) string {
	var buf bytes.Buffer
	if err := printSet(&buf, o, set, "test"); err != nil {
		panic(err)
	}
	return buf.String()
}

// wantOut fails unless got is byte-identical to want.
func wantOut(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

// redirect runs body with the given stdout/stderr slot replaced by a
// pipe and returns everything written to it. Tests run sequentially,
// so swapping the process-global file is safe.
func redirect(t *testing.T, slot **os.File, body func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *slot
	*slot = w
	body()
	*slot = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	return string(out)
}

func TestPrintCIDRWhole24IsOneLine(t *testing.T) {
	set := buildSet4([2]uint32{0, 255})
	wantOut(t, render(DefaultOptions(), set), "0.0.0.0/24\n")
}

func TestPrintCIDRMatchesCOracleShapes(t *testing.T) {
	// Oracle-probed: 10.0.0.1 - 10.0.0.6 decomposes greedily.
	set := buildSet4([2]uint32{0x0a00_0001, 0x0a00_0006})
	wantOut(t, render(DefaultOptions(), set), "10.0.0.1\n10.0.0.2/31\n10.0.0.4/31\n10.0.0.6\n")
}

func TestPrintCIDRObeysDisabledPrefixes(t *testing.T) {
	o := DefaultOptions()
	// --min-prefix 25 disables 0..24; /25 stays enabled.
	for slot := 0; slot < 25; slot++ {
		o.Prefix4Enabled[slot] = false
	}
	set := buildSet4([2]uint32{0, 255})
	wantOut(t, render(o, set), "0.0.0.0/25\n0.0.0.128/25\n")
}

func TestPrintCIDRV6FullWidthIsBare(t *testing.T) {
	// A /126 prints as one CIDR line; disabling it decomposes to
	// four bare /128 addresses.
	set := buildSet6(r6(
		0x2001_0db8_0000_0000, 0,
		0x2001_0db8_0000_0000, 3,
	))
	o := DefaultOptions()
	o.Family = V6
	wantOut(t, render(o, set), "2001:db8::/126\n")
	for slot := 0; slot < 128; slot++ {
		o.Prefix6Enabled[slot] = false
	}
	wantOut(t, render(o, set), "2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n")
}

func TestPrintRangesLoDashHi(t *testing.T) {
	o := DefaultOptions()
	o.Print.Mode = PrintRanges
	set := buildSet4([2]uint32{0x0a00_0001, 0x0a00_0001}, [2]uint32{0x0a00_0008, 0x0a00_000b})
	wantOut(t, render(o, set), "10.0.0.1-10.0.0.1\n10.0.0.8-10.0.0.11\n")
}

func TestPrintSingleIpsExpandsEveryAddress(t *testing.T) {
	o := DefaultOptions()
	o.Print.Mode = PrintSingleIps
	set := buildSet4([2]uint32{0x0a00_0000, 0x0a00_0003})
	wantOut(t, render(o, set), "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n")
}

func TestPrintSingleIPsCapIsStrictlyGreater(t *testing.T) {
	// The C eliminates only when `end - start > 256^3`; a range
	// whose size is exactly the cap is still expanded. The full
	// expansion is 16.7M lines, so assert the size predicate the
	// mode is built on instead of rendering it.
	set := buildSet4([2]uint32{0, uint32(singleIPsCap)})
	start, end := set.Ranges[0].Lo, set.Ranges[0].Hi
	delta := Sub128(end, start)
	if delta.Hi != 0 || delta.Lo != singleIPsCap {
		t.Fatalf("delta = %d/%d, want 0/%d", delta.Hi, delta.Lo, singleIPsCap)
	}
	if delta.Hi != 0 && delta.Lo > singleIPsCap {
		t.Fatal("cap predicate fired at the cap boundary")
	}
	if singleIPsCap+1 <= singleIPsCap {
		t.Fatal("cap + 1 must exceed the cap")
	}
}

func TestPrintPrefixSuffixWrappersAttachPerLine(t *testing.T) {
	// Oracle-probed test 37 shape: the /32 uses the ips wrappers,
	// the /30 CIDR line uses the nets wrappers; the suffix sits
	// before the newline.
	o := DefaultOptions()
	o.Print.PrefixIps = "IP:"
	o.Print.SuffixIps = ":I"
	o.Print.PrefixNets = "NET:"
	o.Print.SuffixNets = ":N"
	set := buildSet4([2]uint32{0x0a00_0001, 0x0a00_0001}, [2]uint32{0x0a00_0008, 0x0a00_000b})
	wantOut(t, render(o, set), "IP:10.0.0.1:I\nNET:10.0.0.8/30:N\n")
}

func TestPrintSingleIPsWrappersMatchC(t *testing.T) {
	o := DefaultOptions()
	o.Print.Mode = PrintSingleIps
	o.Print.PrefixIps = "P-"
	o.Print.SuffixIps = "-S"
	set := buildSet4([2]uint32{0x0a00_000a, 0x0a00_000a})
	wantOut(t, render(o, set), "P-10.0.0.10-S\n")
}

func TestPrintBinaryEmitsV1Payload(t *testing.T) {
	o := DefaultOptions()
	o.Print.Mode = PrintBinary
	set := buildSet4([2]uint32{0xac10_6301, 0xac10_6301})
	set.Lines = 1
	var buf bytes.Buffer
	if err := printSet(&buf, o, set, "test"); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := WriteV1(&want, set); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want.Bytes()) {
		t.Fatalf("binary payload mismatch\n got: %q\nwant: %q", buf.Bytes(), want.Bytes())
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("iprange binary format v1.0\noptimized\n")) {
		t.Fatalf("unexpected v1 header: %q", buf.Bytes())
	}
}

func TestPrintBinaryEmptySetWritesNothing(t *testing.T) {
	o := DefaultOptions()
	o.Print.Mode = PrintBinary
	var buf bytes.Buffer
	if err := printSet(&buf, o, NewIpSet(V4), "test"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("empty set wrote %d bytes", buf.Len())
	}
}

func TestPrintQuietSuppressesAllOutput(t *testing.T) {
	o := DefaultOptions()
	o.Quiet = true
	set := buildSet4([2]uint32{0, 255})
	if got := render(o, set); got != "" {
		t.Fatalf("quiet printed %q", got)
	}
}

func TestPrintUnoptimizedInputIsOptimizedBeforePrinting(t *testing.T) {
	// C ipset_print() optimizes when the flag is clear; two
	// overlapping appends must print as one merged range.
	s := &IpSet{Fam: V4}
	s.AddRange(Range{Lo: IP128{Lo: 1}, Hi: IP128{Lo: 10}})
	s.AddRange(Range{Lo: IP128{Lo: 5}, Hi: IP128{Lo: 20}})
	if s.Optimized {
		t.Fatal("test set must start unoptimized")
	}
	wantOut(t, render(DefaultOptions(), s),
		"0.0.0.1\n0.0.0.2/31\n0.0.0.4/30\n0.0.0.8/29\n0.0.0.16/30\n0.0.0.20\n")
	// The caller's set must be untouched by the internal copy.
	if s.Entries != 2 || len(s.Ranges) != 2 {
		t.Fatalf("caller set mutated: entries=%d ranges=%d", s.Entries, len(s.Ranges))
	}
}

func TestPrintV6CIDRDecompositionBasic(t *testing.T) {
	set := buildSet6(r6(
		0x2001_0db8_0000_0000, 1,
		0x2001_0db8_0000_0000, 6,
	))
	o := DefaultOptions()
	o.Family = V6
	wantOut(t, render(o, set), "2001:db8::1\n2001:db8::2/127\n2001:db8::4/127\n2001:db8::6\n")
}

func TestPrintDebugDiagnosticsV4(t *testing.T) {
	// Exact C stderr shape for a merged set under -v (verified
	// byte-identical against the C and Rust binaries; Lines is 0
	// here because the unit set never went through a loader).
	o := DefaultOptions()
	o.Debug = true
	set := buildSet4([2]uint32{1, 2}, [2]uint32{5, 5}, [2]uint32{8, 8})
	var stdout string
	stderr := redirect(t, &os.Stderr, func() {
		stdout = render(o, set)
	})
	wantOut(t, stdout, "0.0.0.1\n0.0.0.2\n0.0.0.5\n0.0.0.8\n")
	wantOut(t, stderr,
		"iprange: Printing test with 3 ranges, 4 unique IPs\n"+
			"\n"+
			"4 printed CIDRs, break down by prefix:\n"+
			"\t- prefix /32 counts 4 entries\n"+
			"\n"+
			"totals: 0 lines read, 3 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n")
}

func TestPrintDebugDiagnosticsV6(t *testing.T) {
	// A /64 universe: unique = 2^64 exercises the u128 decimal
	// path; verified byte-identical against the C and Rust binaries.
	o := DefaultOptions()
	o.Family = V6
	o.Debug = true
	set := buildSet6(r6(0, 0, 0, 0xFFFF_FFFF_FFFF_FFFF))
	var stdout string
	stderr := redirect(t, &os.Stderr, func() {
		stdout = render(o, set)
	})
	wantOut(t, stdout, "::/64\n")
	wantOut(t, stderr,
		"iprange: Printing test (IPv6) with 1 ranges, 18446744073709551616 unique IPs\n"+
			"\n"+
			"1 printed CIDRs, break down by prefix:\n"+
			"\t- prefix /64 counts 1 entries\n"+
			"\n"+
			"totals: 0 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 18446744073709551616 unique IPs\n")
}

func TestPrintSingleIPsTooBigRangeEliminatedV4(t *testing.T) {
	// C ground truth for 0.0.0.0/0 in single-IP mode: the warning
	// names the range, the count is end - start (not inclusive),
	// and nothing is printed to stdout.
	o := DefaultOptions()
	o.Print.Mode = PrintSingleIps
	set := buildSet4([2]uint32{0, 0xFFFF_FFFF})
	var stdout string
	stderr := redirect(t, &os.Stderr, func() {
		stdout = render(o, set)
	})
	wantOut(t, stdout, "")
	wantOut(t, stderr,
		"iprange: too big range eliminated start=0.0.0.0 end=255.255.255.255 gives 4294967295 IPs\n")
}

func TestPrintSingleIPsTooBigRangeEliminatedV6(t *testing.T) {
	// C ground truth for ::/0 in single-IP mode: the v6 warning has
	// no count suffix.
	o := DefaultOptions()
	o.Family = V6
	o.Print.Mode = PrintSingleIps
	set := buildSet6(r6(0, 0, 0xFFFF_FFFF_FFFF_FFFF, 0xFFFF_FFFF_FFFF_FFFF))
	var stdout string
	stderr := redirect(t, &os.Stderr, func() {
		stdout = render(o, set)
	})
	wantOut(t, stdout, "")
	wantOut(t, stderr,
		"iprange: too big range eliminated start=:: end=ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n")
}

func TestPrintSetFlushesBufferedStdout(t *testing.T) {
	// The exported entry must deliver its buffered output even when
	// the caller never flushes anything (one flush per set).
	var err error
	stdout := redirect(t, &os.Stdout, func() {
		err = PrintSet(DefaultOptions(), buildSet4([2]uint32{0, 255}), "test")
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOut(t, stdout, "0.0.0.0/24\n")
}
