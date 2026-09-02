// Unit tests for the legacy set operations (the ops.rs test module
// ported to Go): merge fold, common/exclude/diff walks, CSV row
// writers, the group-B boundary clamp, and the reduce prefix walk.

package legacy

import (
	"bufio"
	"bytes"
	"slices"
	"testing"
)

// v4r is one closed IPv4 range in test assertions.
type v4r struct{ lo, hi uint32 }

// set4 builds a v4 set from ranges and optimizes it (the Rust test
// helper set4()).
func set4(ranges ...v4r) *IpSet {
	s := NewIpSet(V4)
	for _, r := range ranges {
		s.AddRange(Range{Lo: IP128{Lo: uint64(r.lo)}, Hi: IP128{Lo: uint64(r.hi)}})
	}
	s.Optimize()
	return s
}

// loaded4 builds one loaded v4 set (the Rust test helper loaded4()).
func loaded4(name string, ranges ...v4r) LoadedSet {
	return LoadedSet{Name: name, Set: set4(ranges...)}
}

// rangesOf4 extracts the ranges of a set as v4r pairs (the Rust test
// helper ranges_of()).
func rangesOf4(s *IpSet) []v4r {
	out := make([]v4r, 0, len(s.Ranges))
	for _, r := range s.Ranges {
		out = append(out, v4r{uint32(r.Lo.Lo), uint32(r.Hi.Lo)})
	}
	return out
}

// collect buffers a writer body for byte assertions.
func collect(body func(w *bufio.Writer) error) string {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := body(w); err != nil {
		panic(err)
	}
	if err := w.Flush(); err != nil {
		panic(err)
	}
	return buf.String()
}

func TestOpsMergeFoldsAllSetsInOrder(t *testing.T) {
	opts := DefaultOptions()
	a := []LoadedSet{
		loaded4("a", v4r{1, 3}),
		loaded4("b", v4r{5, 7}),
		loaded4("c", v4r{10, 12}),
	}
	merged := mergeGroup(opts, a, "combined ipset")
	// The C merge leaves the result unoptimized; the printer
	// optimizes. Verify the optimized outcome.
	merged.Optimize()
	if got := rangesOf4(merged); !slices.Equal(got, []v4r{{1, 3}, {5, 7}, {10, 12}}) {
		t.Fatalf("merge ranges = %v, want [(1 3) (5 7) (10 12)]", got)
	}
	if merged.Entries != 3 {
		t.Fatalf("merge entries = %d, want 3", merged.Entries)
	}
}

func TestOpsMergeMergesAdjacentAfterOptimize(t *testing.T) {
	opts := DefaultOptions()
	a := []LoadedSet{loaded4("a", v4r{1, 3}), loaded4("b", v4r{4, 7})}
	merged := mergeGroup(opts, a, "combined ipset")
	merged.Optimize()
	if got := rangesOf4(merged); !slices.Equal(got, []v4r{{1, 7}}) {
		t.Fatalf("merge ranges = %v, want [(1 7)]", got)
	}
	if merged.Entries != 1 {
		t.Fatalf("merge entries = %d, want 1", merged.Entries)
	}
}

func TestOpsCommonIntersectsTwoSets(t *testing.T) {
	x := set4(v4r{1, 10})
	y := set4(v4r{5, 15})
	common := intersectOp(x, y)
	if got := rangesOf4(common); !slices.Equal(got, []v4r{{5, 10}}) {
		t.Fatalf("common ranges = %v, want [(5 10)]", got)
	}
}

func TestOpsCommonWithEmptyOperandIsEmpty(t *testing.T) {
	x := set4()
	y := set4(v4r{5, 15})
	if got := rangesOf4(intersectOp(x, y)); len(got) != 0 {
		t.Fatalf("common(x, y) ranges = %v, want empty", got)
	}
	if got := rangesOf4(intersectOp(y, x)); len(got) != 0 {
		t.Fatalf("common(y, x) ranges = %v, want empty", got)
	}
}

func TestOpsCommonMultiwayFold(t *testing.T) {
	a := set4(v4r{1, 20})
	b := set4(v4r{5, 25})
	c := set4(v4r{10, 30})
	common := intersectOp(a, b)
	common = intersectOp(common, c)
	if got := rangesOf4(common); !slices.Equal(got, []v4r{{10, 20}}) {
		t.Fatalf("common ranges = %v, want [(10 20)]", got)
	}
}

func TestOpsExcludeSubtractsMiddle(t *testing.T) {
	a := set4(v4r{1, 10})
	b := set4(v4r{4, 6})
	out := subtractOp(a, b)
	if got := rangesOf4(out); !slices.Equal(got, []v4r{{1, 3}, {7, 10}}) {
		t.Fatalf("exclude ranges = %v, want [(1 3) (7 10)]", got)
	}
}

func TestOpsExcludeSequentialChainMatchesUnionSubtraction(t *testing.T) {
	// C ipset_exclude is applied per B set: (A \ B1) \ B2.
	a := set4(v4r{1, 100})
	b1 := set4(v4r{4, 10})
	b2 := set4(v4r{20, 30})
	out := subtractOp(a, b1)
	out = subtractOp(out, b2)
	if got := rangesOf4(out); !slices.Equal(got, []v4r{{1, 3}, {11, 19}, {31, 100}}) {
		t.Fatalf("exclude ranges = %v, want [(1 3) (11 19) (31 100)]", got)
	}
}

func TestOpsExcludeWithEmptyBCopiesA(t *testing.T) {
	a := set4(v4r{1, 3}, v4r{7, 9})
	b := set4()
	if got := rangesOf4(subtractOp(a, b)); !slices.Equal(got, []v4r{{1, 3}, {7, 9}}) {
		t.Fatalf("exclude ranges = %v, want [(1 3) (7 9)]", got)
	}
}

func TestOpsExcludeFullCoverIsEmpty(t *testing.T) {
	a := set4(v4r{1, 10})
	b := set4(v4r{0, 20})
	if got := rangesOf4(subtractOp(a, b)); len(got) != 0 {
		t.Fatalf("exclude ranges = %v, want empty", got)
	}
}

func TestOpsDiffEqualSetsIsEmptyAndExitZero(t *testing.T) {
	a := set4(v4r{1, 3}, v4r{7, 9})
	out := diffOp(a, a)
	if got := rangesOf4(out); len(got) != 0 {
		t.Fatalf("diff ranges = %v, want empty", got)
	}
	unique := out.Unique
	if unique.Hi != 0 || unique.Lo != 0 {
		t.Fatalf("diff unique = %v, want 0", unique)
	}
	if unique.Lo != 0 || unique.Hi != 0 {
		t.Fatalf("diff exit = 1, want 0")
	}
}

func TestOpsDiffSymmetricDifference(t *testing.T) {
	a := set4(v4r{1, 5})
	b := set4(v4r{3, 8})
	out := diffOp(a, b)
	if got := rangesOf4(out); !slices.Equal(got, []v4r{{1, 2}, {6, 8}}) {
		t.Fatalf("diff ranges = %v, want [(1 2) (6 8)]", got)
	}
	if out.Unique.Hi != 0 || out.Unique.Lo == 0 {
		t.Fatalf("diff unique = %v, want > 0", out.Unique)
	}
	// The execute exit rule: unique > 0 -> 1.
	rc := 0
	if out.Unique.Hi != 0 || out.Unique.Lo != 0 {
		rc = 1
	}
	if rc != 1 {
		t.Fatalf("diff exit = %d, want 1", rc)
	}
}

func TestOpsDiffWithOneEmptySideCopiesTheOther(t *testing.T) {
	a := set4(v4r{1, 5})
	empty := set4()
	if got := rangesOf4(diffOp(a, empty)); !slices.Equal(got, []v4r{{1, 5}}) {
		t.Fatalf("diff(a, empty) = %v, want [(1 5)]", got)
	}
	if got := rangesOf4(diffOp(empty, a)); !slices.Equal(got, []v4r{{1, 5}}) {
		t.Fatalf("diff(empty, a) = %v, want [(1 5)]", got)
	}
}

func TestOpsCSVFieldQuotingMatchesC(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	for _, f := range []string{"plain", "a,b", `q"uote`, "line\nbreak"} {
		if err := csvField(w, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `plain"a,b""q""uote""line` + "\n" + `break"`
	if got := buf.String(); got != want {
		t.Fatalf("csv fields = %q, want %q", got, want)
	}
}

func TestOpsWriteUintIsDecimal(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := writeUint(w, 0); err != nil {
		t.Fatal(err)
	}
	if err := writeUint(w, 4294967296); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "04294967296"; got != want {
		t.Fatalf("digits = %q, want %q", got, want)
	}
}

func TestOpsWriteUint128IsDecimal(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	// 2^64 (1 << 64): Hi=1, Lo=0.
	if err := writeUint128(w, IP128{Hi: 1, Lo: 0}); err != nil {
		t.Fatal(err)
	}
	// The full v6 maximum (2^128 - 1).
	if err := writeUint128(w, ipMax6); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "18446744073709551616340282366920938463463374607431768211455"; got != want {
		t.Fatalf("digits = %q, want %q", got, want)
	}
}

func TestOpsCompareRowShape(t *testing.T) {
	got := collect(func(w *bufio.Writer) error {
		return writeCompareRow(w, "a", "b", 1, 2, IP128{Lo: 3}, IP128{Lo: 4}, IP128{Lo: 5}, IP128{Lo: 2})
	})
	if want := "a,b,1,2,3,4,5,2\n"; got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
}

func TestOpsCountUniqueAllRowShape(t *testing.T) {
	got := collect(func(w *bufio.Writer) error {
		return writeUniqueRow(w, "x", 7, IP128{Lo: 42})
	})
	if want := "x,7,42\n"; got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
}

func TestOpsGroupBBoundaryIsALoadedSetIndex(t *testing.T) {
	// The parser computes Loaded.GroupB in loaded-set units (an
	// @file source may expand to several sets); ops only clamps it
	// to the loaded length. CountUnique folds only group A and
	// prints a two-column row (no PrintSet), which keeps this test
	// independent of the output worker.
	opts := DefaultOptions()
	opts.Mode = ModeCountUnique
	loaded := &Loaded{
		Sets: []LoadedSet{
			loaded4("a", v4r{1, 2}),
			loaded4("b", v4r{5, 6}),
			loaded4("c", v4r{10, 11}),
			loaded4("d", v4r{20, 21}),
		},
		GroupB: 2,
	}
	if rc := execute(opts, loaded); rc != 0 {
		t.Fatalf("execute = %d, want 0", rc)
	}
	// Clamped when the boundary exceeds the loaded length.
	loaded.GroupB = 99
	if rc := execute(opts, loaded); rc != 0 {
		t.Fatalf("execute = %d, want 0", rc)
	}
}

// enabledAll returns a fresh all-true prefix array of the test size.
func enabledAll(n int) []bool {
	en := make([]bool, n)
	for i := range en {
		en[i] = true
	}
	return en
}

func TestOpsReduceDisablesPrefixesWithoutEntries(t *testing.T) {
	opts := DefaultOptions()
	enabled := enabledAll(33)
	set := set4(v4r{0, 65535}) // exactly one /16
	applyReduce(opts, set, enabled)
	if !enabled[16] {
		t.Error("enabled[16] = false, want true")
	}
	if enabled[15] {
		t.Error("enabled[15] = true, want false")
	}
	if enabled[24] {
		t.Error("enabled[24] = true, want false")
	}
	// The C also disables /32 when no /32 entries were counted.
	if enabled[32] {
		t.Error("enabled[32] = true, want false")
	}
}

func TestOpsReduceMergesIntoClosestLargerPrefix(t *testing.T) {
	opts := DefaultOptions()
	enabled := enabledAll(33)
	// One /24 (0.0.0.0-0.0.0.255) and one /16 (0.1.0.0-0.1.255.255).
	set := set4(v4r{0, 255}, v4r{65536, 131071})
	applyReduce(opts, set, enabled)
	// The /16 is eliminated into the /24 (count*255 increase),
	// matching the C's smallest-increase step.
	if enabled[16] {
		t.Error("enabled[16] = true, want false")
	}
	if !enabled[24] {
		t.Error("enabled[24] = false, want true")
	}
}

func TestOpsReduceRespectsUserDisabledPrefixes(t *testing.T) {
	// --min-prefix 8 disables /1../8; reduce must not enable them.
	opts := DefaultOptions()
	enabled := enabledAll(33)
	for i := 0; i < 8; i++ {
		enabled[i] = false
	}
	set := set4(v4r{0, 255}) // one /24
	applyReduce(opts, set, enabled)
	if enabled[8] {
		t.Error("enabled[8] = true, want false")
	}
	if !enabled[24] {
		t.Error("enabled[24] = false, want true")
	}
}

func TestOpsMergeDebugUsesCombinedIpsetName(t *testing.T) {
	opts := DefaultOptions()
	opts.Debug = true
	a := []LoadedSet{loaded4("first", v4r{1, 3}), loaded4("second", v4r{5, 7})}
	merged := mergeGroup(opts, a, "combined ipset")
	if merged.Entries != 2 {
		t.Fatalf("merge entries = %d, want 2", merged.Entries)
	}
}
