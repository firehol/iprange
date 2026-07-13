package iprangedb

import (
	"errors"
	"math"
	"testing"
)

// Cursor tests for the v4.3 ordered-cursor + helpers (cursor.go), mirroring the
// Rust cursor suite. scope_id is a fixed u32 (scope_mode=0/scalar), so every
// selector takes a uint32 predicate and Current returns the scope_id directly.

// cursorCollectFwd walks a cursor forward from First and returns (from, to,
// scopeID) triples.
func cursorCollectFwd(t *testing.T, r *Reader) [][3]uint32 {
	t.Helper()
	c, err := r.CursorV4()
	if err != nil {
		t.Fatal(err)
	}
	var out [][3]uint32
	c.First()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		out = append(out, [3]uint32{uint32(f), uint32(to), s})
		c.Next()
	}
	return out
}

func TestCursorIterateForwardAndBackward(t *testing.T) {
	file := buildSingleLeaf([]v4rec{{10, 20, 1}, {30, 40, 2}, {50, 60, 1}})
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := cursorCollectFwd(t, r)
	want := [][3]uint32{{10, 20, 1}, {30, 40, 2}, {50, 60, 1}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("forward = %v want %v", got, want)
	}

	c, _ := r.CursorV4()
	if !c.Last() {
		t.Fatal("last")
	}
	var back []uint32
	for {
		f, _, _, ok := c.Current()
		if !ok {
			break
		}
		back = append(back, uint32(f))
		c.Prev()
	}
	if len(back) != 3 || back[0] != 50 || back[1] != 30 || back[2] != 10 {
		t.Fatalf("backward = %v", back)
	}
}

func TestCursorSeekSemantics(t *testing.T) {
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}, {30, 40, 2}, {50, 60, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()

	curFrom := func() Ipv4Key { f, _, _, _ := c.Current(); return f }
	if !c.Seek(Ipv4Key(5)) || curFrom() != Ipv4Key(10) {
		t.Fatal("seek before-all -> first")
	}
	if !c.Seek(Ipv4Key(10)) || curFrom() != Ipv4Key(10) {
		t.Fatal("seek exact from")
	}
	if !c.Seek(Ipv4Key(25)) || curFrom() != Ipv4Key(30) {
		t.Fatal("seek gap -> successor")
	}
	if !c.Seek(Ipv4Key(30)) || curFrom() != Ipv4Key(30) {
		t.Fatal("seek exact")
	}
	if c.Seek(Ipv4Key(61)) {
		t.Fatal("seek past-all should be false")
	}
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("AfterLast has no current")
	}
	if !c.Prev() || curFrom() != Ipv4Key(50) {
		t.Fatal("prev from AfterLast -> last record")
	}
}

func TestCursorEmptyTree(t *testing.T) {
	r, err := Open(buildEmptyFile())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	if c.First() || c.Last() || c.Next() || c.Prev() || c.Seek(Ipv4Key(5)) {
		t.Fatal("empty cursor ops must all be false")
	}
	if _, _, _, ok := c.Current(); ok {
		t.Fatal("empty current")
	}
}

func TestCursorBeforeAfterTransitions(t *testing.T) {
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}}))
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
	left := []v4rec{{10, 20, 1}, {50, 60, 2}}
	right := []v4rec{{100, 110, 3}, {200, 210, 4}}
	r, err := Open(buildTwoLevel(Ipv4Key(100), left, right))
	if err != nil {
		t.Fatal(err)
	}
	got := cursorCollectFwd(t, r)
	order := make([]uint32, len(got))
	for i, g := range got {
		order[i] = g[0]
	}
	if len(order) != 4 || order[0] != 10 || order[1] != 50 || order[2] != 100 || order[3] != 200 {
		t.Fatalf("two-level order = %v", order)
	}
	c, _ := r.CursorV4()
	if !c.Seek(Ipv4Key(70)) { // crosses left leaf -> right leaf
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
	if !c.Next() {
		t.Fatal("next after prev")
	}
	if f, _, _, _ := c.Current(); f != 100 {
		t.Fatal("next -> 100")
	}
}

func TestCursorQueryRangesClampsAndCovers(t *testing.T) {
	// [10,20] covers the query start 15: forEachOverlap must back up and emit the
	// clamped slice [15,18], not skip it.
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}, {30, 40, 2}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }
	var got [][3]uint32
	if err := c.QueryRanges(Ipv4Key(15), Ipv4Key(35), all, func(f, to Ipv4Key, s uint32) error {
		got = append(got, [3]uint32{uint32(f), uint32(to), s})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := [][3]uint32{{15, 20, 1}, {30, 35, 2}}
	if len(got) != len(want) {
		t.Fatalf("ranges = %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ranges = %v want %v", got, want)
		}
	}
}

func TestCursorQueryRangesMergedAndSelect(t *testing.T) {
	// [21,30] is contiguous with [10,20] (20+1 == 21), same scope.
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}, {21, 30, 1}, {40, 50, 2}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }

	var runs [][2]uint32
	if err := c.QueryRangesMerged(Ipv4Key(0), ^Ipv4Key(0), all, func(f, to Ipv4Key) error {
		runs = append(runs, [2]uint32{uint32(f), uint32(to)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0] != [2]uint32{10, 30} || runs[1] != [2]uint32{40, 50} {
		t.Fatalf("merged runs = %v", runs)
	}

	var only2 [][2]uint32
	c2, _ := r.CursorV4()
	scope2 := func(s uint32) bool { return s == 2 }
	if err := c2.QueryRangesMerged(Ipv4Key(0), ^Ipv4Key(0), scope2, func(f, to Ipv4Key) error {
		only2 = append(only2, [2]uint32{uint32(f), uint32(to)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(only2) != 1 || only2[0] != [2]uint32{40, 50} {
		t.Fatalf("select scope 2 = %v", only2)
	}
}

func TestCursorCountIPs(t *testing.T) {
	r, err := Open(buildSingleLeaf([]v4rec{{10, 19, 1}, {30, 39, 2}})) // 10 + 10
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }
	if got := c.CountIPs(Ipv4Key(0), ^Ipv4Key(0), all); got != (Uint128{Lo: 20}) {
		t.Fatalf("count all = %v want 20", got)
	}
	c2, _ := r.CursorV4()
	scope1 := func(s uint32) bool { return s == 1 }
	if got := c2.CountIPs(Ipv4Key(0), ^Ipv4Key(0), scope1); got != (Uint128{Lo: 10}) {
		t.Fatalf("count scope1 = %v want 10", got)
	}
	c3, _ := r.CursorV4()
	if got := c3.CountIPs(Ipv4Key(15), Ipv4Key(34), all); got != (Uint128{Lo: 10}) { // 15..19 + 30..34
		t.Fatalf("count window = %v want 10", got)
	}
}

func TestCursorQueryCIDRsAndCount(t *testing.T) {
	// 0.0.0.0 .. 0.0.0.255 is exactly one /24.
	r, err := Open(buildSingleLeaf([]v4rec{{0, 255, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }
	var cidrs [][2]uint32
	if err := c.QueryCIDRs(Ipv4Key(0), ^Ipv4Key(0), all, func(a Ipv4Key, p uint8, _ uint32) error {
		cidrs = append(cidrs, [2]uint32{uint32(a), uint32(p)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(cidrs) != 1 || cidrs[0] != [2]uint32{0, 24} {
		t.Fatalf("cidrs = %v", cidrs)
	}
	c2, _ := r.CursorV4()
	if n := c2.CountCIDRs(Ipv4Key(0), ^Ipv4Key(0), all); n != 1 {
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
	eq := func(name string, got, want [][2]int) {
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
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}, {40, 50, 2}, {70, 80, 3}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }
	var seen []uint32
	if err := c.QueryRanges(Ipv4Key(0), ^Ipv4Key(0), all, func(f, _ Ipv4Key, _ uint32) error {
		seen = append(seen, uint32(f))
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

func TestCursorVisitorErrorPropagates(t *testing.T) {
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}, {40, 50, 2}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CursorV4()
	all := func(uint32) bool { return true }
	sentinel := errors.New("test: bail")
	err = c.QueryRanges(Ipv4Key(0), ^Ipv4Key(0), all, func(Ipv4Key, Ipv4Key, uint32) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected visitor error to propagate, got %v", err)
	}
}

func TestCursorFamilyMismatch(t *testing.T) {
	r, err := Open(buildSingleLeaf([]v4rec{{10, 20, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CursorV6(); err == nil {
		t.Fatal("CursorV6 on an IPv4 file must error")
	}
}

// TestCursorV6Navigate covers CursorV6 navigation over a Writer-built IPv6 image.
func TestCursorV6Navigate(t *testing.T) {
	w, err := Create[Ipv6Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(Ipv6Key{Lo: 10}, Ipv6Key{Lo: 20}, 1))
	must(t, w.Set(Ipv6Key{Lo: 30}, Ipv6Key{Lo: 40}, 2))
	must(t, w.Commit(0, math.MaxUint64))
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CursorV6()
	if err != nil {
		t.Fatal(err)
	}
	// Wrong family from the other direction.
	if _, err := r.CursorV4(); err == nil {
		t.Fatal("CursorV4 on an IPv6 file must error")
	}

	type rec struct{ from, to, s uint64 }
	var fwd []rec
	c.First()
	for {
		f, to, s, ok := c.Current()
		if !ok {
			break
		}
		fwd = append(fwd, rec{f.Lo, to.Lo, uint64(s)})
		c.Next()
	}
	if len(fwd) != 2 || fwd[0] != (rec{10, 20, 1}) || fwd[1] != (rec{30, 40, 2}) {
		t.Fatalf("v6 forward = %v", fwd)
	}

	// Backward.
	var back []uint64
	c.Last()
	for {
		f, _, _, ok := c.Current()
		if !ok {
			break
		}
		back = append(back, f.Lo)
		c.Prev()
	}
	if len(back) != 2 || back[0] != 30 || back[1] != 10 {
		t.Fatalf("v6 backward = %v", back)
	}

	// Seek into the gap.
	if !c.Seek(Ipv6Key{Lo: 25}) {
		t.Fatal("seek 25")
	}
	f, _, _, _ := c.Current()
	if f.Lo != 30 {
		t.Fatalf("seek 25 -> %d", f.Lo)
	}

	// CountIPs over the whole space.
	all := func(uint32) bool { return true }
	if got := c.CountIPs(Ipv6Key{}, Ipv6Key{Hi: math.MaxUint64, Lo: math.MaxUint64}, all); got != (Uint128{Lo: 22}) {
		t.Fatalf("v6 count = %v want 22", got)
	}
}
