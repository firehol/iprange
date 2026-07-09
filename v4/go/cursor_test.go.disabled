package iprangedb

import (
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Mirrors the Rust cursor tests (iprange-livedb/src/cursor.rs) plus a behavioral cross-read
// over the shared Rust-written goldens in ../conformance (the Go cursor/helpers must read
// Rust-written files identically — §12).

func cursorCollectV4(t *testing.T, r *Reader) [][3]int {
	t.Helper()
	c, err := r.CursorV4()
	if err != nil {
		t.Fatal(err)
	}
	var out [][3]int
	c.First()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		out = append(out, [3]int{int(f), int(to), int(s[0])})
		c.Next()
	}
	return out
}

func TestCursorIterateForwardAndBackward(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}, {30, 40, []byte{2}}, {50, 60, []byte{1}}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := cursorCollectV4(t, r)
	want := [][3]int{{10, 20, 1}, {30, 40, 2}, {50, 60, 1}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("forward = %v", got)
	}

	c, _ := r.CursorV4()
	if !c.Last() {
		t.Fatal("last")
	}
	var back []int
	for {
		f, _, _, ok := c.Current()
		if !ok {
			break
		}
		back = append(back, int(f))
		c.Prev()
	}
	if len(back) != 3 || back[0] != 50 || back[1] != 30 || back[2] != 10 {
		t.Fatalf("backward = %v", back)
	}
}

func TestCursorSeekSemantics(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}, {30, 40, []byte{2}}, {50, 60, []byte{1}}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()

	curFrom := func() int { f, _, _, _ := c.Current(); return int(f) }
	if !c.Seek(5) || curFrom() != 10 {
		t.Fatal("seek before-all -> first")
	}
	if !c.Seek(10) || curFrom() != 10 {
		t.Fatal("seek exact from")
	}
	if !c.Seek(25) || curFrom() != 30 {
		t.Fatal("seek gap -> successor")
	}
	if !c.Seek(30) || curFrom() != 30 {
		t.Fatal("seek exact")
	}
	if c.Seek(61) {
		t.Fatal("seek past-all should be false")
	}
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("AfterLast has no current")
	}
	if !c.Prev() || curFrom() != 50 {
		t.Fatal("prev from AfterLast -> last record")
	}
}

func TestCursorEmptyTree(t *testing.T) {
	r, err := Open(buildEmptyFile(V4, 1))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	if c.First() || c.Last() || c.Next() || c.Prev() || c.Seek(5) {
		t.Fatal("empty cursor ops must all be false")
	}
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("empty current")
	}
}

func TestCursorBeforeAfterTransitions(t *testing.T) {
	r, err := Open(buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("starts BeforeFirst (no current)")
	}
	if c.Prev() {
		t.Fatal("prev stays BeforeFirst")
	}
	if !c.Next() {
		t.Fatal("next -> first")
	}
	if f, _, _, _ := c.Current(); f != 10 {
		t.Fatal("first record")
	}
	if c.Next() {
		t.Fatal("next -> AfterLast (false)")
	}
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("AfterLast no current")
	}
	if c.Next() {
		t.Fatal("next stays AfterLast")
	}
	if !c.Prev() {
		t.Fatal("prev -> last record")
	}
	if f, _, _, _ := c.Current(); f != 10 {
		t.Fatal("back to record")
	}
}

func TestCursorTwoLevelIterateAndSeek(t *testing.T) {
	left := []v4rec{{10, 20, []byte{1}}, {50, 60, []byte{2}}}
	right := []v4rec{{100, 110, []byte{3}}, {200, 210, []byte{4}}}
	r, err := Open(buildTwoLevel(V4, 1, 100, left, right))
	if err != nil {
		t.Fatal(err)
	}
	got := cursorCollectV4(t, r)
	order := []int{}
	for _, g := range got {
		order = append(order, g[0])
	}
	if len(order) != 4 || order[0] != 10 || order[1] != 50 || order[2] != 100 || order[3] != 200 {
		t.Fatalf("two-level order = %v", order)
	}
	c, _ := r.CursorV4()
	if !c.Seek(70) { // crosses left leaf -> right leaf
		t.Fatal("seek 70")
	}
	if f, _, _, _ := c.Current(); f != 100 {
		t.Fatal("seek 70 -> 100")
	}
	if !c.Prev() { // back across the leaf boundary
		t.Fatal("prev across boundary")
	}
	if f, _, _, _ := c.Current(); f != 50 {
		t.Fatal("prev -> 50")
	}
	if !c.Next() || func() int { f, _, _, _ := c.Current(); return int(f) }() != 100 {
		t.Fatal("next -> 100")
	}
}

func TestCursorQueryRangesMergedAndSelect(t *testing.T) {
	// [21,30] is contiguous with [10,20] (20+1 == 21).
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}, {21, 30, []byte{1}}, {40, 50, []byte{2}}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()

	var runs [][2]int
	if err := c.QueryRangesMerged(0, ^Ipv4Key(0), func([]byte) bool { return true }, func(f, to Ipv4Key) error {
		runs = append(runs, [2]int{int(f), int(to)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0] != [2]int{10, 30} || runs[1] != [2]int{40, 50} {
		t.Fatalf("merged runs = %v", runs)
	}

	var only2 [][2]int
	c2, _ := r.CursorV4()
	if err := c2.QueryRangesMerged(0, ^Ipv4Key(0), func(s []byte) bool { return len(s) == 1 && s[0] == 2 }, func(f, to Ipv4Key) error {
		only2 = append(only2, [2]int{int(f), int(to)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(only2) != 1 || only2[0] != [2]int{40, 50} {
		t.Fatalf("select scope 2 = %v", only2)
	}
}

func TestCursorCountIPs(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 19, []byte{1}}, {30, 39, []byte{2}}}) // 10 + 10
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func([]byte) bool { return true }
	if got := c.CountIPs(0, ^Ipv4Key(0), all); got != (Uint128{Lo: 20}) {
		t.Fatalf("count all = %v", got)
	}
	if got := c.CountIPs(0, ^Ipv4Key(0), func(s []byte) bool { return s[0] == 1 }); got != (Uint128{Lo: 10}) {
		t.Fatalf("count scope1 = %v", got)
	}
	if got := c.CountIPs(15, 34, all); got != (Uint128{Lo: 10}) { // 15..19 + 30..34
		t.Fatalf("count window = %v", got)
	}
}

func TestCursorQueryCIDRsAndCount(t *testing.T) {
	// 0.0.0.0 .. 0.0.0.255 is exactly one /24.
	r, err := Open(buildSingleLeaf(V4, 1, []v4rec{{0, 255, []byte{1}}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	var cidrs [][2]int
	if err := c.QueryCIDRs(0, ^Ipv4Key(0), func([]byte) bool { return true }, func(a Ipv4Key, p uint8, _ []byte) error {
		cidrs = append(cidrs, [2]int{int(a), int(p)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(cidrs) != 1 || cidrs[0] != [2]int{0, 24} {
		t.Fatalf("cidrs = %v", cidrs)
	}
	if n := c.CountCIDRs(0, ^Ipv4Key(0), func([]byte) bool { return true }); n != 1 {
		t.Fatalf("count cidrs = %d", n)
	}
}

func TestCIDRDecompositionUnits(t *testing.T) {
	cover := func(a, b uint32) [][2]int {
		var v [][2]int
		_ = emitCIDRs[Ipv4Key](Ipv4Key(a), Ipv4Key(b), func(addr Ipv4Key, p uint8) error {
			v = append(v, [2]int{int(addr), int(p)})
			return nil
		})
		return v
	}
	eq := func(name string, got [][2]int, want [][2]int) {
		if len(got) != len(want) {
			t.Fatalf("%s: %v != %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v != %v", name, got, want)
			}
		}
	}
	eq("/24", cover(0, 255), [][2]int{{0, 24}})
	eq("10..20", cover(10, 20), [][2]int{{10, 31}, {12, 30}, {16, 30}, {20, 32}})
	eq("whole v4", cover(0, ^uint32(0)), [][2]int{{0, 0}})
	eq("single host", cover(42, 42), [][2]int{{42, 32}})

	// IPv6 whole space -> ::/0 (no overflow).
	var v6 [][2]any
	_ = emitCIDRs[Ipv6Key](Ipv6Key{}.minKey(), Ipv6Key{}.maxKey(), func(a Ipv6Key, p uint8) error {
		v6 = append(v6, [2]any{a, p})
		return nil
	})
	if len(v6) != 1 || v6[0][1].(uint8) != 0 {
		t.Fatalf("whole v6 = %v", v6)
	}
}

func TestCursorVisitorStop(t *testing.T) {
	file := buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}, {40, 50, []byte{2}}, {70, 80, []byte{3}}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	var seen []int
	if err := c.QueryRanges(0, ^Ipv4Key(0), func([]byte) bool { return true }, func(f, _ Ipv4Key, _ []byte) error {
		seen = append(seen, int(f))
		if len(seen) == 2 {
			return Stop
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 10 || seen[1] != 40 {
		t.Fatalf("stop not honored: %v", seen)
	}
}

func TestCursorFamilyMismatch(t *testing.T) {
	r, err := Open(buildSingleLeaf(V4, 1, []v4rec{{10, 20, []byte{1}}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CursorV6(); errorClass(err) != "InvalidInput" {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// Cross-read: the Go cursor over each Rust-written golden must iterate (forward and
// backward) to exactly expect_scan, proving behavioral cross-read of the cursor (§12).
func TestCursorCrossReadGoldens(t *testing.T) {
	for _, c := range loadCases(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			golden, err := readGolden(t, c.Name)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			r, err := Open(golden)
			if err != nil {
				t.Fatalf("Go reader rejected Rust golden %s: %v", c.Name, err)
			}
			want := wantScan(t, c.ExpectScan)
			switch c.Family {
			case "v4":
				assertScan(t, cursorScanFwdV4(t, r), want, "cursor fwd cross-read")
				assertScan(t, cursorScanBackV4(t, r), want, "cursor back cross-read")
				// CountIPs over the whole space == sum of record sizes from expect_scan.
				cur, _ := r.CursorV4()
				if got := cur.CountIPs(0, ^Ipv4Key(0), func([]byte) bool { return true }); got != expectedTotalIPs(want) {
					t.Fatalf("count_ips cross-read = %v, want %v", got, expectedTotalIPs(want))
				}
			case "v6":
				assertScan(t, cursorScanFwdV6(t, r), want, "cursor fwd cross-read")
				assertScan(t, cursorScanBackV6(t, r), want, "cursor back cross-read")
			default:
				t.Fatalf("unknown family %q", c.Family)
			}
		})
	}
}

func readGolden(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(corpusPath(filepath.Join("files", name+".iprdb")))
}

func cursorScanFwdV4(t *testing.T, r *Reader) []scanTriple {
	c, err := r.CursorV4()
	if err != nil {
		t.Fatal(err)
	}
	var got []scanTriple
	c.First()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		got = append(got, scanTriple{from: strconv.FormatUint(uint64(f), 10), to: strconv.FormatUint(uint64(to), 10), scope: append([]byte(nil), s...)})
		c.Next()
	}
	return got
}

func cursorScanBackV4(t *testing.T, r *Reader) []scanTriple {
	c, err := r.CursorV4()
	if err != nil {
		t.Fatal(err)
	}
	var rev []scanTriple
	c.Last()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		rev = append(rev, scanTriple{from: strconv.FormatUint(uint64(f), 10), to: strconv.FormatUint(uint64(to), 10), scope: append([]byte(nil), s...)})
		c.Prev()
	}
	return reverseTriples(rev)
}

func cursorScanFwdV6(t *testing.T, r *Reader) []scanTriple {
	c, err := r.CursorV6()
	if err != nil {
		t.Fatal(err)
	}
	var got []scanTriple
	c.First()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		got = append(got, scanTriple{from: v6ToDecimal(f), to: v6ToDecimal(to), scope: append([]byte(nil), s...)})
		c.Next()
	}
	return got
}

func cursorScanBackV6(t *testing.T, r *Reader) []scanTriple {
	c, err := r.CursorV6()
	if err != nil {
		t.Fatal(err)
	}
	var rev []scanTriple
	c.Last()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		rev = append(rev, scanTriple{from: v6ToDecimal(f), to: v6ToDecimal(to), scope: append([]byte(nil), s...)})
		c.Prev()
	}
	return reverseTriples(rev)
}

func reverseTriples(s []scanTriple) []scanTriple {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}

// expectedTotalIPs sums (to - from + 1) over the expected scan as a Uint128 (v4 totals fit
// easily, but the helper is width-agnostic).
func expectedTotalIPs(want []scanTriple) Uint128 {
	total := new(big.Int)
	one := big.NewInt(1)
	for _, e := range want {
		from, _ := new(big.Int).SetString(e.from, 10)
		to, _ := new(big.Int).SetString(e.to, 10)
		span := new(big.Int).Sub(to, from)
		span.Add(span, one)
		total.Add(total, span)
	}
	lo := new(big.Int).And(total, new(big.Int).SetUint64(maxUint64)).Uint64()
	hi := new(big.Int).Rsh(total, 64).Uint64()
	return Uint128{Hi: hi, Lo: lo}
}
