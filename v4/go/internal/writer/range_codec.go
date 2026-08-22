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
	return tree.Key{Hi: uint64(format.U32(cell))}, nil
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
		from:  tree.Key{Hi: uint64(r.From)},
		to:    tree.Key{Hi: uint64(r.To)},
		value: r.Value,
	}, nil
}

func (rangeCodec4) WriteKey(key tree.Key, output []byte) {
	format.PutU32(output, uint32(key.Hi))
}

func (rangeCodec4) Next(key tree.Key) (tree.Key, bool) {
	if key.Hi == 0xFFFFFFFF {
		return tree.Key{}, false
	}
	return tree.Key{Hi: key.Hi + 1}, true
}

func (rangeCodec4) Previous(key tree.Key) (tree.Key, bool) {
	if key.Hi == 0 {
		return tree.Key{}, false
	}
	return tree.Key{Hi: key.Hi - 1}, true
}

func (rangeCodec4) EncodeRecord(r rangeRecord, output []byte) (int, error) {
	if err := format.EncodeRangeRecordV4(format.RangeRecordV4{
		From:  uint32(r.from.Hi),
		To:    uint32(r.to.Hi),
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
	return tree.Key{Hi: hi, Lo: lo}, nil
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
		from:  tree.Key{Hi: r.FromHi, Lo: r.FromLo},
		to:    tree.Key{Hi: r.ToHi, Lo: r.ToLo},
		value: r.Value,
	}, nil
}

func (rangeCodec6) WriteKey(key tree.Key, output []byte) {
	format.PutU128(output, key.Hi, key.Lo)
}

func (rangeCodec6) Next(key tree.Key) (tree.Key, bool) {
	hi, lo := key.Hi, key.Lo
	lo++
	if lo == 0 {
		hi++
		if hi == 0 {
			return tree.Key{}, false
		}
	}
	return tree.Key{Hi: hi, Lo: lo}, true
}

func (rangeCodec6) Previous(key tree.Key) (tree.Key, bool) {
	hi, lo := key.Hi, key.Lo
	if hi == 0 && lo == 0 {
		return tree.Key{}, false
	}
	lo--
	if lo == ^uint64(0) {
		hi--
	}
	return tree.Key{Hi: hi, Lo: lo}, true
}

func (rangeCodec6) EncodeRecord(r rangeRecord, output []byte) (int, error) {
	if err := format.EncodeRangeRecordV6(format.RangeRecordV6{
		FromHi: r.from.Hi, FromLo: r.from.Lo,
		ToHi: r.to.Hi, ToLo: r.to.Lo,
		Value: r.value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV6Size, nil
}
