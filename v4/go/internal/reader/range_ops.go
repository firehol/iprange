package reader

// Family-specific range primitives for the membership aggregation and
// join cores, folded onto one ordered address-key primitive (Rust
// IpKey parity). IPv4 bounds live in Lo with Hi zero; IPv6 bounds use
// both limbs. The sweeps compare with addrKey.Less/Equal (direct method
// calls, no dispatch); the ops adapter supplies only the operations
// that genuinely differ per family: boundary arithmetic and inclusive
// cardinality. The concrete iterators wrap the shared treeCursor the
// same way the public range cursors do, with a family switch only on
// record decode.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// addrKey is the single ordered key space of the membership machinery
// (Rust IpKey parity): hi/lo limbs with IPv4 keys in lo only.
type addrKey struct {
	hi uint64
	lo uint64
}

// Less reports strict ascending key order.
func (k addrKey) Less(other addrKey) bool {
	if k.hi != other.hi {
		return k.hi < other.hi
	}
	return k.lo < other.lo
}

// Equal reports exact key equality.
func (k addrKey) Equal(other addrKey) bool {
	return k.hi == other.hi && k.lo == other.lo
}

// rangeOps carries the family-specific key operations (Rust
// IpKey::checked_next / checked_previous / inclusive_cardinality). The
// function fields are initialized from the sealed family tables below,
// so every call bottoms out in scanned module code.
type rangeOps struct {
	next      func(k addrKey) (addrKey, error)
	previous  func(k addrKey) (addrKey, error)
	inclusive func(from, to addrKey) (format.Cardinality129, error)
}

var (
	ops4 = rangeOps{
		next:      next4,
		previous:  previous4,
		inclusive: inclusive4,
	}
	ops6 = rangeOps{
		next:      next6,
		previous:  previous6,
		inclusive: inclusive6,
	}
)

// key4 packs one IPv4 bound into the ordered key space.
func key4(v uint32) addrKey { return addrKey{lo: uint64(v)} }

// key6 packs one IPv6 bound into the ordered key space.
func key6(hi, lo uint64) addrKey { return addrKey{hi: hi, lo: lo} }

// next4 advances one IPv4 bound; the top address has no successor (Rust
// Ipv4Key::checked_next).
func next4(k addrKey) (addrKey, error) {
	if k.hi != 0 || k.lo == uint64(^uint32(0)) {
		return addrKey{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv4 boundary"}
	}
	return addrKey{lo: k.lo + 1}, nil
}

// previous4 retreats one IPv4 bound; zero has no predecessor.
func previous4(k addrKey) (addrKey, error) {
	if k.hi != 0 || k.lo == 0 {
		return addrKey{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv4 boundary"}
	}
	return addrKey{lo: k.lo - 1}, nil
}

// next6 advances one IPv6 bound; the top address has no successor.
func next6(k addrKey) (addrKey, error) {
	if k.lo == ^uint64(0) {
		if k.hi == ^uint64(0) {
			return addrKey{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv6 boundary"}
		}
		return addrKey{hi: k.hi + 1}, nil
	}
	return addrKey{hi: k.hi, lo: k.lo + 1}, nil
}

// previous6 retreats one IPv6 bound; zero has no predecessor.
func previous6(k addrKey) (addrKey, error) {
	if k.lo == 0 {
		if k.hi == 0 {
			return addrKey{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv6 boundary"}
		}
		return addrKey{hi: k.hi - 1, lo: ^uint64(0)}, nil
	}
	return addrKey{hi: k.hi, lo: k.lo - 1}, nil
}

func inclusive4(from, to addrKey) (format.Cardinality129, error) {
	return format.IPv4Inclusive(uint32(from.lo), uint32(to.lo))
}

func inclusive6(from, to addrKey) (format.Cardinality129, error) {
	return format.IPv6Inclusive(from.hi, from.lo, to.hi, to.lo)
}

// membershipRange is one physical membership interval in ordered-key
// space (Rust MembershipRange).
type membershipRange struct {
	from, to     addrKey
	membershipID uint32
}

// membershipIterator is the ordered membership range stream over one
// concrete tree cursor (Rust MembershipRangeCursor parity). Unlike the
// direct cursor, visiting a record does not count RangeConsumed: the
// aggregation and join scans count physical ranges themselves (Rust
// does the same).
type membershipIterator struct {
	cursor *treeCursor
	family uint8
}

// next returns the next membership interval, or ok=false when the
// cursor is exhausted.
func (it *membershipIterator) next() (membershipRange, bool, error) {
	if it.cursor.finished {
		return membershipRange{}, false, nil
	}
	sl, _, err := it.cursor.openLeaf()
	if err != nil {
		return membershipRange{}, false, err
	}
	if it.family == format.AddressFamilyIPv4 {
		rec, err := rangeRecordAt4(sl, it.cursor.index)
		if err != nil {
			return membershipRange{}, false, err
		}
		if _, _, err := it.cursor.advance(); err != nil {
			return membershipRange{}, false, err
		}
		return membershipRange{from: key4(rec.From), to: key4(rec.To), membershipID: rec.Value}, true, nil
	}
	rec, err := rangeRecordAt6(sl, it.cursor.index)
	if err != nil {
		return membershipRange{}, false, err
	}
	if _, _, err := it.cursor.advance(); err != nil {
		return membershipRange{}, false, err
	}
	return membershipRange{from: key6(rec.FromHi, rec.FromLo), to: key6(rec.ToHi, rec.ToLo), membershipID: rec.Value}, true, nil
}

// directRangeFrame is one ordered direct-provider interval.
type directRangeFrame struct {
	from, to addrKey
	value    uint32
}

// directIterator is the ordered direct range stream over one concrete
// tree cursor (Rust direct_cursor parity, forward only). Each visited
// record counts RangeConsumed exactly like the public direct cursors.
type directIterator struct {
	cursor *treeCursor
	family uint8
}

// next returns the next direct interval, or ok=false when the cursor is
// exhausted.
func (it *directIterator) next() (directRangeFrame, bool, error) {
	if it.cursor.finished {
		return directRangeFrame{}, false, nil
	}
	sl, _, err := it.cursor.openLeaf()
	if err != nil {
		return directRangeFrame{}, false, err
	}
	if it.family == format.AddressFamilyIPv4 {
		rec, err := rangeRecordAt4(sl, it.cursor.index)
		if err != nil {
			return directRangeFrame{}, false, err
		}
		work.RangeConsumed(1)
		if _, _, err := it.cursor.advance(); err != nil {
			return directRangeFrame{}, false, err
		}
		return directRangeFrame{from: key4(rec.From), to: key4(rec.To), value: rec.Value}, true, nil
	}
	rec, err := rangeRecordAt6(sl, it.cursor.index)
	if err != nil {
		return directRangeFrame{}, false, err
	}
	work.RangeConsumed(1)
	if _, _, err := it.cursor.advance(); err != nil {
		return directRangeFrame{}, false, err
	}
	return directRangeFrame{from: key6(rec.FromHi, rec.FromLo), to: key6(rec.ToHi, rec.ToLo), value: rec.Value}, true, nil
}

func addCard(left, right format.Cardinality129) (format.Cardinality129, error) {
	return left.Add(right)
}

func increment64(value uint64, detail string) (uint64, error) {
	if value == ^uint64(0) {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: detail}
	}
	return value + 1, nil
}
