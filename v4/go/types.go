// The scalar types in this file are the relocated, verified public
// foundation (previously aliased from the obsolete milestone-0 tree):
// numeric addresses, the value-tag, and the exact 129-bit cardinality.

package iprangedb

import (
	"errors"
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/format"
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

// firstSeenWire and lastSeenWire are the private canonical wire forms of
// the engine-defined semantic tags. They are package-private and never
// reassigned, so no caller can change the classification authority or race
// DirectSemantic.
var (
	firstSeenWire = [16]byte{'f', 'i', 'r', 's', 't', '_', 's', 'e', 'e', 'n'}
	lastSeenWire  = [16]byte{'l', 'a', 's', 't', '_', 's', 'e', 'e', 'n'}
)

// ValueTagFirstSeen returns the engine-defined first_seen semantic tag
// (binary-format-v4.md section 4; Rust contract ValueTag::FIRST_SEEN).
func ValueTagFirstSeen() ValueTag { return ValueTag{wire: firstSeenWire} }

// ValueTagLastSeen returns the engine-defined last_seen semantic tag.
func ValueTagLastSeen() ValueTag { return ValueTag{wire: lastSeenWire} }

// MaxMetadataUncompressed is the exact 20 MiB uncompressed metadata bound
// (binary-format-v4.md section 2; Rust contract MAX_METADATA_UNCOMPRESSED).
// The single authority is internal/format.
const MaxMetadataUncompressed = format.MaxMetadataUncompressed

// ErrCardinalityOverflow reports an exact cardinality outside 0..=2^129-1.
// The single implementation lives in internal/format.
var ErrCardinalityOverflow = format.ErrCardinalityOverflow

// Cardinality129 is an exact unsigned value in 0..=2^129-1. The single
// arithmetic implementation lives in internal/format; this alias exposes it
// through the public API so the two can never drift.
type Cardinality129 = format.Cardinality129

// CardinalityZero returns exact zero.
func CardinalityZero() Cardinality129 { return format.CardinalityZero() }

// FullIPv6Space returns 2^128, the exact cardinality of ::/0.
func FullIPv6Space() Cardinality129 { return format.FullIPv6Space() }

// NewCardinality129 constructs a checked fixed-size cardinality.
func NewCardinality129(bit128 uint8, hi, lo uint64) (Cardinality129, error) {
	return format.NewCardinality129(bit128, hi, lo)
}

// CardinalityFromUint64 widens a u64 exactly.
func CardinalityFromUint64(value uint64) Cardinality129 {
	return format.CardinalityFromUint64(value)
}

// CardinalityFromUint128 widens the two halves of a u128 exactly.
func CardinalityFromUint128(hi, lo uint64) Cardinality129 {
	return format.CardinalityFromUint128(hi, lo)
}

// IPv4Inclusive returns the exact inclusive IPv4 interval size.
func IPv4Inclusive(from, to uint32) (Cardinality129, error) {
	return format.IPv4Inclusive(from, to)
}

// IPv6Inclusive returns the exact inclusive IPv6 interval size, including ::/0.
func IPv6Inclusive(fromHi, fromLo, toHi, toLo uint64) (Cardinality129, error) {
	return format.IPv6Inclusive(fromHi, fromLo, toHi, toLo)
}
