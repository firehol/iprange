// Package iprangedb implements the exact unsigned Phase-1 iprange v4
// database reader: a portable, mmap-only, zero-allocation immutable reader
// that cross-opens the shared v4 conformance corpus.
//
// The scalar types in this file are the relocated, verified public
// foundation (previously aliased from the obsolete milestone-0 tree):
// numeric addresses, the value-tag, and the exact 129-bit cardinality.
package iprangedb

import (
	"errors"
	"math/bits"
)

// AddressFamily selects one static address family for a database.
type AddressFamily uint8

const (
	AddressFamilyIPv4 AddressFamily = 4
	AddressFamilyIPv6 AddressFamily = 6
)

// ValueKind selects the semantic meaning of each range's u32 value.
type ValueKind uint8

const (
	ValueKindDirect     ValueKind = 1
	ValueKindMembership ValueKind = 2
	ValueKindStructured ValueKind = 3
)

// IPv4 is one IPv4 address in numeric network-address order.
type IPv4 uint32

// Next returns the following IPv4 address, or false at the family maximum.
func (k IPv4) Next() (IPv4, bool) {
	if k == ^IPv4(0) {
		return 0, false
	}
	return k + 1, true
}

// Previous returns the preceding IPv4 address, or false at the family minimum.
func (k IPv4) Previous() (IPv4, bool) {
	if k == 0 {
		return 0, false
	}
	return k - 1, true
}

// IPv6 is one IPv6 address as numeric high and low 64-bit halves.
type IPv6 struct {
	Hi uint64
	Lo uint64
}

// IPv6FromHalves constructs an IPv6 address from its numeric halves.
func IPv6FromHalves(hi, lo uint64) IPv6 { return IPv6{Hi: hi, Lo: lo} }

// Next returns the following IPv6 address, or false at the family maximum.
func (k IPv6) Next() (IPv6, bool) {
	if k == (IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}) {
		return IPv6{}, false
	}
	lo, carry := bits.Add64(k.Lo, 1, 0)
	return IPv6{Hi: k.Hi + carry, Lo: lo}, true
}

// Previous returns the preceding IPv6 address, or false at the family minimum.
func (k IPv6) Previous() (IPv6, bool) {
	if k == (IPv6{}) {
		return IPv6{}, false
	}
	lo, borrow := bits.Sub64(k.Lo, 1, 0)
	return IPv6{Hi: k.Hi - borrow, Lo: lo}, true
}

// ErrInvalidValueTag reports a non-canonical value tag.
var ErrInvalidValueTag = errors.New("iprange v4 value tag is not canonical")

// ValueTag is a canonical 15-byte maximum value followed by a mandatory NUL.
// The wire bytes are private so invalid tags cannot be constructed directly.
type ValueTag struct {
	wire [16]byte
}

// NewValueTag constructs a canonical tag from non-NUL caller bytes.
func NewValueTag(value []byte) (ValueTag, error) {
	if len(value) > 15 {
		return ValueTag{}, ErrInvalidValueTag
	}
	for _, b := range value {
		if b == 0 {
			return ValueTag{}, ErrInvalidValueTag
		}
	}
	var tag ValueTag
	copy(tag.wire[:], value)
	return tag, nil
}

// RetentionTag returns the exact predefined retention tag.
func RetentionTag() ValueTag {
	return ValueTag{wire: [16]byte{'r', 'e', 't', 'e', 'n', 't', 'i', 'o', 'n'}}
}

// Wire returns the exact 16-byte on-disk representation.
func (t ValueTag) Wire() [16]byte { return t.wire }

// Bytes returns the tag content before its mandatory NUL.
func (t ValueTag) Bytes() []byte {
	for i, b := range t.wire {
		if b == 0 {
			return t.wire[:i]
		}
	}
	panic("invalid ValueTag invariant")
}

// ErrCardinalityOverflow reports an exact cardinality outside 0..=2^129-1.
var ErrCardinalityOverflow = errors.New("iprange v4 cardinality overflow")

// Cardinality129 is an exact unsigned value in 0..=2^129-1.
// Its fields are private so bit 128 can only be zero or one.
type Cardinality129 struct {
	bit128 uint8
	hi     uint64
	lo     uint64
}

// CardinalityZero returns exact zero.
func CardinalityZero() Cardinality129 { return Cardinality129{} }

// FullIPv6Space returns 2^128, the exact cardinality of ::/0.
func FullIPv6Space() Cardinality129 { return Cardinality129{bit128: 1} }

// NewCardinality129 constructs a checked fixed-size cardinality.
func NewCardinality129(bit128 uint8, hi, lo uint64) (Cardinality129, error) {
	if bit128 > 1 {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	return Cardinality129{bit128: bit128, hi: hi, lo: lo}, nil
}

// CardinalityFromUint64 widens a u64 exactly.
func CardinalityFromUint64(value uint64) Cardinality129 {
	return Cardinality129{lo: value}
}

// CardinalityFromUint128 widens the two halves of a u128 exactly.
func CardinalityFromUint128(hi, lo uint64) Cardinality129 {
	return Cardinality129{hi: hi, lo: lo}
}

// Bit128 returns the top bit (0 or 1).
func (c Cardinality129) Bit128() uint8 { return c.bit128 }

// Hi returns the high 64 bits.
func (c Cardinality129) Hi() uint64 { return c.hi }

// Lo returns the low 64 bits.
func (c Cardinality129) Lo() uint64 { return c.lo }

// Compare returns -1, 0, or 1 according to exact unsigned ordering.
func (c Cardinality129) Compare(other Cardinality129) int {
	if c.bit128 < other.bit128 {
		return -1
	}
	if c.bit128 > other.bit128 {
		return 1
	}
	if c.hi < other.hi {
		return -1
	}
	if c.hi > other.hi {
		return 1
	}
	if c.lo < other.lo {
		return -1
	}
	if c.lo > other.lo {
		return 1
	}
	return 0
}

// Add returns the exact sum or ErrCardinalityOverflow above 2^129-1.
func (c Cardinality129) Add(other Cardinality129) (Cardinality129, error) {
	lo, carry := bits.Add64(c.lo, other.lo, 0)
	hi, carry := bits.Add64(c.hi, other.hi, carry)
	top := uint16(c.bit128) + uint16(other.bit128) + uint16(carry)
	if top > 1 {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	return Cardinality129{bit128: uint8(top), hi: hi, lo: lo}, nil
}

// Sub returns the exact difference or ErrCardinalityOverflow on underflow.
func (c Cardinality129) Sub(other Cardinality129) (Cardinality129, error) {
	if c.Compare(other) < 0 {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	lo, borrow := bits.Sub64(c.lo, other.lo, 0)
	hi, borrow := bits.Sub64(c.hi, other.hi, borrow)
	top, borrow := bits.Sub64(uint64(c.bit128), uint64(other.bit128), borrow)
	if borrow != 0 || top > 1 {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	return Cardinality129{bit128: uint8(top), hi: hi, lo: lo}, nil
}

// IPv4Inclusive returns the exact inclusive IPv4 interval size.
func IPv4Inclusive(from, to uint32) (Cardinality129, error) {
	if from > to {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	return CardinalityFromUint64(uint64(to) - uint64(from) + 1), nil
}

// IPv6Inclusive returns the exact inclusive IPv6 interval size, including ::/0.
func IPv6Inclusive(fromHi, fromLo, toHi, toLo uint64) (Cardinality129, error) {
	if fromHi > toHi || (fromHi == toHi && fromLo > toLo) {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	lo, borrow := bits.Sub64(toLo, fromLo, 0)
	hi, borrow := bits.Sub64(toHi, fromHi, borrow)
	if borrow != 0 {
		return Cardinality129{}, ErrCardinalityOverflow
	}
	return Cardinality129{hi: hi, lo: lo}.Add(CardinalityFromUint64(1))
}

// Uint64 converts exactly or reports overflow.
func (c Cardinality129) Uint64() (uint64, error) {
	if c.bit128 != 0 || c.hi != 0 {
		return 0, ErrCardinalityOverflow
	}
	return c.lo, nil
}

// Uint128 returns the two exact u128 halves or reports overflow.
func (c Cardinality129) Uint128() (hi, lo uint64, err error) {
	if c.bit128 != 0 {
		return 0, 0, ErrCardinalityOverflow
	}
	return c.hi, c.lo, nil
}

// String renders the exact decimal representation.
func (c Cardinality129) String() string {
	limbs := [3]uint64{c.lo, c.hi, uint64(c.bit128)}
	if limbs == [3]uint64{} {
		return "0"
	}
	var reverse [40]byte
	used := 0
	for limbs != [3]uint64{} {
		var remainder uint64
		for i := len(limbs) - 1; i >= 0; i-- {
			limbs[i], remainder = bits.Div64(remainder, limbs[i], 10)
		}
		reverse[used] = byte('0' + remainder)
		used++
	}
	var output [40]byte
	for i := 0; i < used; i++ {
		output[i] = reverse[used-1-i]
	}
	return string(output[:used])
}
