package reader

import "github.com/firehol/iprange/v4/go/internal/work"

// search.go holds the single authoritative lower-bound search over the
// sorted key slots of one slotted page, mirroring fixed_tree/page.rs
// lower_bound_by for the Go reader.

// greatestLE returns the greatest index i in [0, n) whose key compares
// <= 0 against the target, or -1 when none does. cmp decodes key fields
// only (never payload or child fields) and must be monotone in the slot
// order: cmp(i) <= 0 exactly for i <= best. The caller decodes the selected
// record exactly once after the search; records that the search never
// selects have their payload fields decoded by nobody.
func greatestLE(n int, cmp func(i int) (int, error)) (int, error) {
	lo, hi := 0, n
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		work.KeyProbe(1)
		c, err := cmp(mid)
		if err != nil {
			return -1, err
		}
		if c <= 0 {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return best, nil
}

// cmpU32 compares two unsigned 32-bit keys.
func cmpU32(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpU64 compares two unsigned 64-bit keys.
func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpU128 compares two 128-bit keys ordered by (hi, lo).
func cmpU128(ahi, alo, bhi, blo uint64) int {
	if c := cmpU64(ahi, bhi); c != 0 {
		return c
	}
	return cmpU64(alo, blo)
}
