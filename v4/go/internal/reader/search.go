package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

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

// greatestFixedV4 is the v4 family-typed greatest-LE search over one
// fixed-cell page (the closure-free form of greatestLE; Rust
// fixed_tree/page.rs lower_bound_by with the RangeCodec read_key
// inlined). The page shape was validated once by the caller's fixed
// search view; every probe reads the persistent slot and the 4-byte
// from key only.
func greatestFixedV4(search *format.FixedSearch, n int, addr uint32) (int, error) {
	lo, hi := 0, n
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		work.KeyProbe(1)
		midKey, ok := search.U32(mid)
		if !ok {
			return -1, format.FixedCellOutside()
		}
		if midKey <= addr {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return best, nil
}

// greatestFixedV6 is the v6 form of greatestFixedV4 (16-byte from key).
func greatestFixedV6(search *format.FixedSearch, n int, addrHi, addrLo uint64) (int, error) {
	lo, hi := 0, n
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		work.KeyProbe(1)
		keyHi, keyLo, ok := search.U128(mid)
		if !ok {
			return -1, format.FixedCellOutside()
		}
		if keyHi < addrHi || (keyHi == addrHi && keyLo <= addrLo) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return best, nil
}
