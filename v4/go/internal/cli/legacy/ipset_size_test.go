// Unit tests for the unique-address bookkeeping of the legacy interval
// set (ipset.go). The Rust reference for these cases is
// v4/rust/iprange-cli/src/legacy/range.rs, and the C oracle is
// ipset_added_entry()/ipset6_added_entry() in src/ipset.h and
// src/ipset6.h.

package legacy

import "testing"

// setWith builds one unoptimized set by the C load path: every range
// goes through AddRange, which is the arm that books `unique` per add.
func setWith(t *testing.T, fam Family, rs ...Range) *IpSet {
	t.Helper()
	s := NewIpSet(fam)
	for _, r := range rs {
		s.AddRange(r)
	}
	return s
}

// The whole IPv6 universe holds 2^128 addresses, one more than a u128
// pair can carry. Range.Size saturates at the family maximum, which is
// what the released C tool reports for ::/0 (src/ipset6.h
// ipset6_added_entry). A bare hi-lo+1 wraps that count to zero, and
// saturating only the accumulation cannot recover it: the addend itself
// is already wrong.
func TestRangeSizeOfTheFullIPv6UniverseSaturates(t *testing.T) {
	hi, lo := Range{Lo: ip128(0, 0), Hi: v6Max}.Size()
	if hi != v6Max.Hi || lo != v6Max.Lo {
		t.Fatalf("Size(::/0) = (%d,%d), want the family maximum (%d,%d)",
			hi, lo, v6Max.Hi, v6Max.Lo)
	}
}

// Saturating the one unrepresentable count must not disturb the counts
// that fit: a half universe (2^127), a single address, two addresses,
// and the whole IPv4 universe at 2^32 all stay exact.
func TestRangeSizeOfRepresentableRangesStaysExact(t *testing.T) {
	const half = uint64(0x8000_0000_0000_0000) // 2^127 as the high limb
	cases := []struct {
		name   string
		r      Range
		hi, lo uint64
	}{
		{"::/1 holds 2^127", Range{Lo: ip128(0, 0), Hi: ip128(0x7FFF_FFFF_FFFF_FFFF, 0xFFFF_FFFF_FFFF_FFFF)}, half, 0},
		{"top address alone", Range{Lo: v6Max, Hi: v6Max}, 0, 1},
		{"two addresses", Range{Lo: ip128(0, 0), Hi: ip128(0, 1)}, 0, 2},
		{"whole v4 universe", Range{Lo: ip128(0, 0), Hi: ip128(0, 0xFFFF_FFFF)}, 0, 4_294_967_296},
	}
	for _, c := range cases {
		hi, lo := c.r.Size()
		if hi != c.hi || lo != c.lo {
			t.Errorf("%s: Size() = (%d,%d), want (%d,%d)", c.name, hi, lo, c.hi, c.lo)
		}
	}
}

// The per-add counter and the optimize sweep must agree at the
// saturation point: which one a consumer reports depends on whether
// the set was optimized before the count was taken, so both arms owe
// the same answer.
func TestUniqueCounterSaturatesOnAddAndOnOptimize(t *testing.T) {
	full := Range{Lo: ip128(0, 0), Hi: v6Max}

	added := setWith(t, V6, full)
	if added.Entries != 1 || added.Unique != v6Max {
		t.Fatalf("after AddRange: entries=%d unique=%v, want 1 and %v",
			added.Entries, added.Unique, v6Max)
	}

	// The universe split into two adjacent halves cannot be summed in
	// u128; the sweep merges them into one entry and reports the family
	// maximum, the count `iprange -6 -C` prints for that input.
	split := setWith(t, V6,
		Range{Lo: ip128(0, 0), Hi: ip128(0xFFFF_FFFF_FFFF_FFFF, 0xFFFF_FFFF_FFFF_FFFE)},
		Range{Lo: v6Max, Hi: v6Max})
	split.Optimize()
	if split.Entries != 1 || split.Unique != v6Max {
		t.Fatalf("split universe after Optimize: entries=%d unique=%v, want 1 and %v",
			split.Entries, split.Unique, v6Max)
	}

	// The same full range twice: the duplicate is contained and must not
	// move either count, before or after the sweep.
	dup := setWith(t, V6, full, full)
	if dup.Unique != v6Max {
		t.Fatalf("duplicated ::/0 before Optimize: unique=%v, want %v", dup.Unique, v6Max)
	}
	dup.Optimize()
	if dup.Entries != 1 || dup.Unique != v6Max {
		t.Fatalf("duplicated ::/0 after Optimize: entries=%d unique=%v, want 1 and %v",
			dup.Entries, dup.Unique, v6Max)
	}
}

// The carry out of the low limb is part of the saturation test, not an
// afterthought: adding one address to a counter already at the family
// maximum overflows only through that carry, and a test written on the
// high limb alone reads "no overflow" and returns zero. Rust reaches this
// by using u128::saturating_add, which has no such seam.
func TestAddSatSaturatesOnTheLowLimbCarry(t *testing.T) {
	one := addSat(v6Max, IP128{Lo: 1})
	if one != v6Max {
		t.Fatalf("maximum + 1 = %v, want %v", one, v6Max)
	}
	two := addSat(v6Max, v6Max)
	if two != v6Max {
		t.Fatalf("maximum + maximum = %v, want %v", two, v6Max)
	}
	exact := addSat(IP128{Hi: 0, Lo: 10}, IP128{Hi: 0, Lo: 5})
	if exact != (IP128{Hi: 0, Lo: 15}) {
		t.Fatalf("10 + 5 = %v, want 15", exact)
	}
	carry := addSat(IP128{Hi: 0, Lo: ^uint64(0)}, IP128{Hi: 0, Lo: 1})
	if carry != (IP128{Hi: 1, Lo: 0}) {
		t.Fatalf("low-limb carry into the high limb = %v, want {Hi:1}", carry)
	}
}

// Saturating the 128-bit arm must not disturb the 32-bit arm, which the
// C bookkeeping keeps as a plain accumulation: no IPv4 count can reach
// 2^32 addresses plus one through a valid range pair, so the absence of
// saturation there is observable rather than merely unused.
func TestUniqueCounterKeepsExactIPv4Accounting(t *testing.T) {
	s := setWith(t, V4,
		Range{Lo: ip128(0, 0), Hi: ip128(0, 9)},
		Range{Lo: ip128(0, 11), Hi: ip128(0, 11)})
	if s.Unique != (IP128{Lo: 11}) {
		t.Fatalf("v4 unique = %v, want {Lo:11}", s.Unique)
	}
	s.Optimize()
	if s.Entries != 2 || s.Unique != (IP128{Lo: 11}) {
		t.Fatalf("v4 after Optimize: entries=%d unique=%v, want 2 and {Lo:11}",
			s.Entries, s.Unique)
	}
}
