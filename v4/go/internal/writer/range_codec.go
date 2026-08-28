// Per-family range codecs over the generic tree core (Rust range_tree.rs
// RangeCodec + key.rs IpKey checked arithmetic).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// rangeRecord is one decoded range leaf record in generic tree-key space
// (Rust range_tree::Record<K>).
type rangeRecord struct {
	from  tree.Key
	to    tree.Key
	value uint32
}

// rangeFamily is one address-family range contract over the generic tree
// key (Rust IpKey: width, record size, checked next/previous).
type rangeFamily interface {
	tree.Codec[rangeRecord]
	// Next returns key+1 in the family address space, when it exists.
	Next(key tree.Key) (tree.Key, bool)
	// Previous returns key-1 in the family address space, when it exists.
	Previous(key tree.Key) (tree.Key, bool)
	// EncodeRecord writes one range record into output (record size bytes)
	// (Rust RangeCodec::encode; the Go method takes an owned output buffer
	// so no record write can ever target a mapped view).
	EncodeRecord(r rangeRecord, output []byte) (int, error)
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

func (rangeCodec4) ReadLeaf(cell []byte) (rangeRecord, error) {
	if len(cell) != format.RangeRecordV4Size {
		return rangeRecord{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV4(cell)
	if err != nil {
		return rangeRecord{}, corrupt("range leaf is invalid")
	}
	return rangeRecord{
		from:  tree.KeyOfU32(r.From),
		to:    tree.KeyOfU32(r.To),
		value: r.Value,
	}, nil
}

func (rangeCodec4) WriteKey(key tree.Key, output []byte) {
	format.PutU32(output, key.U32())
}

func (rangeCodec4) Next(key tree.Key) (tree.Key, bool) {
	if key.U32() == 0xFFFFFFFF {
		return tree.Key{}, false
	}
	return tree.KeyOfU32(key.U32() + 1), true
}

func (rangeCodec4) Previous(key tree.Key) (tree.Key, bool) {
	if key.U32() == 0 {
		return tree.Key{}, false
	}
	return tree.KeyOfU32(key.U32() - 1), true
}

func (rangeCodec4) EncodeRecord(r rangeRecord, output []byte) (int, error) {
	if err := format.EncodeRangeRecordV4(format.RangeRecordV4{
		From:  r.from.U32(),
		To:    r.to.U32(),
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

func (rangeCodec6) ReadLeaf(cell []byte) (rangeRecord, error) {
	if len(cell) != format.RangeRecordV6Size {
		return rangeRecord{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV6(cell)
	if err != nil {
		return rangeRecord{}, corrupt("range leaf is invalid")
	}
	return rangeRecord{
		from:  tree.KeyOfU128(r.FromHi, r.FromLo),
		to:    tree.KeyOfU128(r.ToHi, r.ToLo),
		value: r.Value,
	}, nil
}

func (rangeCodec6) WriteKey(key tree.Key, output []byte) {
	hi, lo := key.U128()
	format.PutU128(output, hi, lo)
}

func (rangeCodec6) Next(key tree.Key) (tree.Key, bool) {
	hi, lo := key.U128()
	lo++
	if lo == 0 {
		hi++
		if hi == 0 {
			return tree.Key{}, false
		}
	}
	return tree.KeyOfU128(hi, lo), true
}

func (rangeCodec6) Previous(key tree.Key) (tree.Key, bool) {
	hi, lo := key.U128()
	if hi == 0 && lo == 0 {
		return tree.Key{}, false
	}
	lo--
	if lo == ^uint64(0) {
		hi--
	}
	return tree.KeyOfU128(hi, lo), true
}

func (rangeCodec6) EncodeRecord(r rangeRecord, output []byte) (int, error) {
	fromHi, fromLo := r.from.U128()
	toHi, toLo := r.to.U128()
	if err := format.EncodeRangeRecordV6(format.RangeRecordV6{
		FromHi: fromHi, FromLo: fromLo,
		ToHi: toHi, ToLo: toLo,
		Value: r.value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV6Size, nil
}
