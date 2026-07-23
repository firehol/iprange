package iprangedb

import "github.com/firehol/iprange/v4/go/internal/exactv4"

// AddressFamily selects one static address family for a database.
type AddressFamily = exactv4.AddressFamily

const (
	AddressFamilyIPv4 = exactv4.AddressFamilyIPv4
	AddressFamilyIPv6 = exactv4.AddressFamilyIPv6
)

// ValueKind selects the semantic meaning of each range's u32 value.
type ValueKind = exactv4.ValueKind

const (
	ValueKindDirect     = exactv4.ValueKindDirect
	ValueKindMembership = exactv4.ValueKindMembership
)

// IPv4 and IPv6 are numeric address values. IPv6 keeps high/low halves in
// numeric order; the SDK owns their exact wire encoding.
type IPv4 = exactv4.IPv4
type IPv6 = exactv4.IPv6

func IPv6FromHalves(hi, lo uint64) IPv6 { return exactv4.IPv6FromHalves(hi, lo) }

// ValueTag is an opaque canonical tag of at most 15 non-NUL bytes.
type ValueTag = exactv4.ValueTag

var ErrInvalidValueTag = exactv4.ErrInvalidValueTag

func NewValueTag(value []byte) (ValueTag, error) { return exactv4.NewValueTag(value) }
func RetentionTag() ValueTag                     { return exactv4.RetentionTag() }

// Cardinality129 is an exact unsigned value in 0..=2^129-1.
type Cardinality129 = exactv4.Cardinality129

var ErrCardinalityOverflow = exactv4.ErrCardinalityOverflow

func CardinalityZero() Cardinality129 { return exactv4.CardinalityZero() }
func FullIPv6Space() Cardinality129   { return exactv4.FullIPv6Space() }
func NewCardinality129(top uint8, hi, lo uint64) (Cardinality129, error) {
	return exactv4.NewCardinality129(top, hi, lo)
}
func CardinalityFromUint64(value uint64) Cardinality129 {
	return exactv4.CardinalityFromUint64(value)
}
func CardinalityFromUint128(hi, lo uint64) Cardinality129 {
	return exactv4.CardinalityFromUint128(hi, lo)
}
func IPv4Inclusive(from, to uint32) (Cardinality129, error) {
	return exactv4.IPv4Inclusive(from, to)
}
func IPv6Inclusive(fromHi, fromLo, toHi, toLo uint64) (Cardinality129, error) {
	return exactv4.IPv6Inclusive(fromHi, fromLo, toHi, toLo)
}
