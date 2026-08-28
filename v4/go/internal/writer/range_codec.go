// Per-family range codecs over the generic tree core (Rust range_tree.rs
// RangeCodec + key.rs IpKey checked arithmetic). The decoded records are
// family-typed like Rust Record<K>: an IPv4 record is 12 bytes and an
// IPv6 record 36 bytes, so the mutation machinery never materializes the
// general tree key.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// key4 is the IPv4 range key: one numeric address (Rust Ipv4Key). The
// key lives in the value only; wire cells stay little-endian.
type key4 uint32

// key6 is the IPv6 range key: the numeric high and low limbs (Rust
// Ipv6Key, high limb most significant).
type key6 struct {
	hi uint64
	lo uint64
}

// rangeRecord is one decoded range leaf record in the family key space
// (Rust range_tree::Record<K>).
type rangeRecord[K any] struct {
	from  K
	to    K
	value uint32
}

// rangeFamily is one address-family range contract over its typed key
// (Rust IpKey: width, record size, checked next/previous, Ord).
type rangeFamily[K any] interface {
	tree.Codec[rangeRecord[K]]
	// Less reports a < b in the family address space (Rust Ord).
	Less(a, b K) bool
	// Equal reports a == b (Rust PartialEq).
	Equal(a, b K) bool
	// Next returns key+1 in the family address space, when it exists.
	Next(key K) (K, bool)
	// Previous returns key-1 in the family address space, when it exists.
	Previous(key K) (K, bool)
	// KeyOf converts one typed key to the tree comparison primitive
	// (the canonical compare bytes) for the tree core entry points.
	KeyOf(key K) tree.Key
	// EncodeRecord writes one range record into output (record size bytes)
	// (Rust RangeCodec::encode; the Go method takes an owned output buffer
	// so no record write can ever target a mapped view).
	EncodeRecord(r rangeRecord[K], output []byte) (int, error)
}

// rangeCodec4 is the IPv4 range tree codec (Rust RangeCodec<Ipv4Key>).
type rangeCodec4 struct{}

func (rangeCodec4) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (rangeCodec4) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (rangeCodec4) Aux() uint32                 { return uint32(format.AddressFamilyIPv4) }
func (rangeCodec4) KeySize() int                { return 4 }
func (rangeCodec4) LeafSize() int               { return format.RangeRecordV4Size }

func (rangeCodec4) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	if len(cell) < 4 {
		return tree.Key{}, corrupt("range key is truncated")
	}
	return tree.KeyOfU32(format.U32(cell)), nil
}

// ReadKeyLimbs decodes the u32 order key of one fixed-size cell (the
// NumericKeyCodec seam; a 4-byte key zero-extends into the low limb).
func (rangeCodec4) ReadKeyLimbs(cell []byte) (uint64, uint64, error) {
	if len(cell) < 4 {
		return 0, 0, corrupt("range key is truncated")
	}
	return 0, uint64(format.U32(cell)), nil
}

// PrefixKeyProbe opts the codec into the tree core's inline prefix
// probe: fixed cells carry the little-endian u32 key as their prefix.
func (rangeCodec4) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// Ipv4Key Ord; never called on the hot path, which uses the prefix
// probe).
func (rangeCodec4) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	if len(cell) < 4 {
		return 0, corrupt("range key is truncated")
	}
	return cmpU32(format.U32(cell), target.U32()), nil
}

func (rangeCodec4) ReadLeaf(cell []byte) (rangeRecord[key4], error) {
	if len(cell) != format.RangeRecordV4Size {
		return rangeRecord[key4]{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV4(cell)
	if err != nil {
		return rangeRecord[key4]{}, corrupt("range leaf is invalid")
	}
	return rangeRecord[key4]{
		from:  key4(r.From),
		to:    key4(r.To),
		value: r.Value,
	}, nil
}

func (rangeCodec4) WriteKey(key tree.Key, output []byte) {
	format.PutU32(output, key.U32())
}

func (rangeCodec4) Less(a, b key4) bool  { return a < b }
func (rangeCodec4) Equal(a, b key4) bool { return a == b }

func (rangeCodec4) Next(key key4) (key4, bool) {
	if key == key4(0xFFFFFFFF) {
		return 0, false
	}
	return key + 1, true
}

func (rangeCodec4) Previous(key key4) (key4, bool) {
	if key == 0 {
		return 0, false
	}
	return key - 1, true
}

func (rangeCodec4) KeyOf(key key4) tree.Key { return tree.KeyOfU32(uint32(key)) }

func (rangeCodec4) EncodeRecord(r rangeRecord[key4], output []byte) (int, error) {
	if err := format.EncodeRangeRecordV4(format.RangeRecordV4{
		From:  uint32(r.from),
		To:    uint32(r.to),
		Value: r.value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV4Size, nil
}

// rangeCodec6 is the IPv6 range tree codec (Rust RangeCodec<Ipv6Key>).
type rangeCodec6 struct{}

func (rangeCodec6) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (rangeCodec6) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (rangeCodec6) Aux() uint32                 { return uint32(format.AddressFamilyIPv6) }
func (rangeCodec6) KeySize() int                { return 16 }
func (rangeCodec6) LeafSize() int               { return format.RangeRecordV6Size }

func (rangeCodec6) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	if len(cell) < 16 {
		return tree.Key{}, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	return tree.KeyOfU128(hi, lo), nil
}

// ReadKeyLimbs decodes the u128 order key of one fixed-size cell (the
// NumericKeyCodec seam).
func (rangeCodec6) ReadKeyLimbs(cell []byte) (uint64, uint64, error) {
	if len(cell) < 16 {
		return 0, 0, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	return hi, lo, nil
}

// PrefixKeyProbe opts the codec into the inline prefix probe (u128 LE).
func (rangeCodec6) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// Ipv6Key Ord; never called on the hot path, which uses the prefix
// probe).
func (rangeCodec6) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	if len(cell) < 16 {
		return 0, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	thi, tlo := target.U128()
	return cmpU128(hi, lo, thi, tlo), nil
}

func (rangeCodec6) ReadLeaf(cell []byte) (rangeRecord[key6], error) {
	if len(cell) != format.RangeRecordV6Size {
		return rangeRecord[key6]{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV6(cell)
	if err != nil {
		return rangeRecord[key6]{}, corrupt("range leaf is invalid")
	}
	return rangeRecord[key6]{
		from:  key6{hi: r.FromHi, lo: r.FromLo},
		to:    key6{hi: r.ToHi, lo: r.ToLo},
		value: r.Value,
	}, nil
}

func (rangeCodec6) WriteKey(key tree.Key, output []byte) {
	hi, lo := key.U128()
	format.PutU128(output, hi, lo)
}

func (rangeCodec6) Less(a, b key6) bool {
	return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo)
}
func (rangeCodec6) Equal(a, b key6) bool { return a == b }

func (rangeCodec6) Next(key key6) (key6, bool) {
	lo := key.lo + 1
	hi := key.hi
	if lo == 0 {
		hi++
		if hi == 0 {
			return key6{}, false
		}
	}
	return key6{hi: hi, lo: lo}, true
}

func (rangeCodec6) Previous(key key6) (key6, bool) {
	if key.hi == 0 && key.lo == 0 {
		return key6{}, false
	}
	lo := key.lo - 1
	hi := key.hi
	if lo == ^uint64(0) {
		hi--
	}
	return key6{hi: hi, lo: lo}, true
}

func (rangeCodec6) KeyOf(key key6) tree.Key { return tree.KeyOfU128(key.hi, key.lo) }

func (rangeCodec6) EncodeRecord(r rangeRecord[key6], output []byte) (int, error) {
	if err := format.EncodeRangeRecordV6(format.RangeRecordV6{
		FromHi: r.from.hi, FromLo: r.from.lo,
		ToHi: r.to.hi, ToLo: r.to.lo,
		Value: r.value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV6Size, nil
}
