// Legacy interval-set core: closed address ranges with the exact
// ipset merge/optimize semantics of the released C implementation
// (src/ipset{,6}.c ipset_add_ip_range / ipset_optimize). One
// authoritative implementation over the Family-tagged IP128 value,
// like the Rust generic core.

package legacy

import "sort"

// Range is one closed address range [Lo, Hi] with Lo <= Hi always
// true (both endpoints use the run's family width).
type Range struct {
	Lo IP128
	Hi IP128
}

// Size returns the number of addresses covered as a u128 pair
// (Hi - Lo + 1, unsigned 128-bit arithmetic).
func (r Range) Size() (hi, lo uint64) {
	lo = r.Hi.Lo - r.Lo.Lo
	hi = r.Hi.Hi - r.Lo.Hi
	if r.Hi.Lo < r.Lo.Lo {
		hi--
	}
	lo++
	if lo == 0 {
		hi++
	}
	return hi, lo
}

// addSat adds two u128 values, saturating at the full-universe
// maximum (the v6 unique-counter representation limit).
func addSat(a, b IP128) IP128 {
	lo := a.Lo + b.Lo
	hi := a.Hi + b.Hi
	if lo < a.Lo {
		hi++
	}
	if hi < a.Hi {
		return ipMax6
	}
	return IP128{Hi: hi, Lo: lo}
}

// IpSet is an ordered/optimized-flagged collection of closed ranges
// with the line-count and unique-IP bookkeeping of the C ipset.
type IpSet struct {
	Fam       Family
	Ranges    []Range
	Entries   int // C entries: stored ranges
	Lines     int // C lines: input-line bookkeeping (never CSV counts)
	Unique    IP128
	Optimized bool
}

// NewIpSet returns an empty set for the family.
func NewIpSet(fam Family) *IpSet {
	return &IpSet{Fam: fam, Optimized: true}
}

// addUnique adds the range size to the unique counter (v4
// accumulates, v6 saturates at the full-universe limit).
func (s *IpSet) addUnique(r Range) {
	if s.Fam == V4 {
		s.Unique = IP128{Lo: s.Unique.Lo + r.Hi.Lo - r.Lo.Lo + 1}
		return
	}
	hi, lo := r.Size()
	s.Unique = addSat(s.Unique, IP128{Hi: hi, Lo: lo})
}

// AddRange appends one closed range without merging (C
// ipset_add_ip_range): adjacency after the last range merges while
// the set stays optimized; any other disorder appends and clears
// the flag. The unique counter increments first for every added
// range (C order; consumers re-optimize before reporting).
func (s *IpSet) AddRange(r Range) {
	s.addUnique(r)
	if s.Optimized && len(s.Ranges) > 0 {
		last := &s.Ranges[len(s.Ranges)-1]
		if !last.Hi.IsMax(s.Fam) {
			if next, ok := last.Hi.Inc(s.Fam); ok && r.Lo == next {
				last.Hi = r.Hi
				return
			}
			if r.Lo.Compare(last.Hi) > 0 {
				s.Ranges = append(s.Ranges, r)
				s.Entries++
				return
			}
		}
		s.Optimized = false
		s.Ranges = append(s.Ranges, r)
		s.Entries++
		return
	}
	s.Ranges = append(s.Ranges, r)
	s.Entries++
}

// Optimize sorts and merges per the C ipset_optimize sweep: sort by
// lo ascending, hi descending; merge containment, overlap and
// guarded adjacency; recompute unique; set the optimized flag.
// Lines is preserved.
func (s *IpSet) Optimize() {
	// The comparator is the total order of the C qsort (lo asc, hi
	// desc), so the sort's stability is irrelevant.
	sort.Slice(s.Ranges, func(i, j int) bool {
		a, b := s.Ranges[i], s.Ranges[j]
		c := a.Lo.Compare(b.Lo)
		if c != 0 {
			return c < 0
		}
		return a.Hi.Compare(b.Hi) > 0
	})
	out := make([]Range, 0, len(s.Ranges))
	var unique IP128
	flush := func(r Range) {
		if s.Fam == V4 {
			unique = IP128{Lo: unique.Lo + r.Hi.Lo - r.Lo.Lo + 1}
			return
		}
		hi, lo := r.Size()
		unique = addSat(unique, IP128{Hi: hi, Lo: lo})
	}
	for _, r := range s.Ranges {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if r.Hi.Compare(last.Hi) <= 0 {
				continue // contained (including duplicates)
			}
			adjacent := false
			if !last.Hi.IsMax(s.Fam) {
				if next, ok := last.Hi.Inc(s.Fam); ok && r.Lo == next {
					adjacent = true
				}
			}
			if r.Lo.Compare(last.Hi) <= 0 || adjacent {
				last.Hi = r.Hi
				continue
			}
			flush(*last)
			out = append(out, r)
			continue
		}
		out = append(out, r)
	}
	if n := len(out); n > 0 {
		flush(out[n-1])
	}
	s.Ranges = out
	s.Entries = len(out)
	s.Unique = unique
	s.Optimized = true
}

// MergeFrom appends every range of other (C ipset_merge),
// concatenating, adding lines, and clearing the optimized flag. The
// caller runs Optimize afterwards.
func (s *IpSet) MergeFrom(other *IpSet) {
	s.Ranges = append(s.Ranges, other.Ranges...)
	s.Entries += other.Entries
	s.Lines += other.Lines
	if s.Fam == V4 {
		s.Unique = IP128{Lo: s.Unique.Lo + other.Unique.Lo}
	} else {
		s.Unique = addSat(s.Unique, other.Unique)
	}
	s.Optimized = false
}
