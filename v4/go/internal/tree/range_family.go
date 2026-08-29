// Canonical range family types shared by the tree core and the writer
// (Rust range_tree.rs RangeCodec + key.rs IpKey live inside the crate
// for the same reason): the tree-side gap machinery operates on the
// concrete families with static dispatch, exactly like Rust's
// monomorphized RangeCodec<Ipv4Key>/<Ipv6Key>, while the writer's
// generic machinery sees the same types through aliases.

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// RangeKey4 is the IPv4 range key: one numeric address (Rust Ipv4Key).
// Wire cells stay little-endian.
type RangeKey4 uint32

// RangeKey6 is the IPv6 range key: the numeric high and low limbs (Rust
// Ipv6Key, high limb most significant).
type RangeKey6 struct {
	Hi uint64
	Lo uint64
}

// RangeRecord is one decoded range leaf record in the family key space
// (Rust range_tree::Record<K>).
type RangeRecord[K any] struct {
	From  K
	To    K
	Value uint32
}

// RangeFamily is one address-family range contract over its typed key
// (Rust IpKey: width, record size, checked next/previous, Ord).
type RangeFamily[K any] interface {
	Codec[RangeRecord[K]]
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
	KeyOf(key K) Key
	// EncodeRecord writes one range record into output (record size
	// bytes) (Rust RangeCodec::encode; the Go method takes an owned
	// output buffer so no record write can ever target a mapped view).
	EncodeRecord(r RangeRecord[K], output []byte) (int, error)
}

// RangeCodec4 is the IPv4 range tree codec (Rust RangeCodec<Ipv4Key>).
type RangeCodec4 struct{}

func (RangeCodec4) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (RangeCodec4) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (RangeCodec4) Aux() uint32                 { return uint32(format.AddressFamilyIPv4) }
func (RangeCodec4) KeySize() int                { return 4 }
func (RangeCodec4) LeafSize() int               { return format.RangeRecordV4Size }

func (RangeCodec4) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 4 {
		return Key{}, corrupt("range key is truncated")
	}
	return KeyOfU32(format.U32(cell)), nil
}

// ReadKeyLimbs decodes the u32 order key of one fixed-size cell (the
// NumericKeyCodec seam; a 4-byte key zero-extends into the low limb).
func (RangeCodec4) ReadKeyLimbs(cell []byte) (uint64, uint64, error) {
	if len(cell) < 4 {
		return 0, 0, corrupt("range key is truncated")
	}
	return 0, uint64(format.U32(cell)), nil
}

// PrefixKeyProbe opts the codec into the tree core's inline prefix
// probe: fixed cells carry the little-endian u32 key as their prefix.
func (RangeCodec4) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// Ipv4Key Ord; never called on the hot path, which uses the prefix
// probe).
func (RangeCodec4) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 4 {
		return 0, corrupt("range key is truncated")
	}
	return cmpU32(format.U32(cell), target.U32()), nil
}

func (RangeCodec4) ReadLeaf(cell []byte) (RangeRecord[RangeKey4], error) {
	if len(cell) != format.RangeRecordV4Size {
		return RangeRecord[RangeKey4]{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV4(cell)
	if err != nil {
		return RangeRecord[RangeKey4]{}, corrupt("range leaf is invalid")
	}
	return RangeRecord[RangeKey4]{
		From:  RangeKey4(r.From),
		To:    RangeKey4(r.To),
		Value: r.Value,
	}, nil
}

func (RangeCodec4) WriteKey(key Key, output []byte) {
	format.PutU32(output, key.U32())
}

func (RangeCodec4) Less(a, b RangeKey4) bool  { return a < b }
func (RangeCodec4) Equal(a, b RangeKey4) bool { return a == b }

func (RangeCodec4) Next(key RangeKey4) (RangeKey4, bool) {
	if key == RangeKey4(0xFFFFFFFF) {
		return 0, false
	}
	return key + 1, true
}

func (RangeCodec4) Previous(key RangeKey4) (RangeKey4, bool) {
	if key == 0 {
		return 0, false
	}
	return key - 1, true
}

func (RangeCodec4) KeyOf(key RangeKey4) Key { return KeyOfU32(uint32(key)) }

func (RangeCodec4) EncodeRecord(r RangeRecord[RangeKey4], output []byte) (int, error) {
	if err := format.EncodeRangeRecordV4(format.RangeRecordV4{
		From:  uint32(r.From),
		To:    uint32(r.To),
		Value: r.Value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV4Size, nil
}

// RangeCodec6 is the IPv6 range tree codec (Rust RangeCodec<Ipv6Key>).
type RangeCodec6 struct{}

func (RangeCodec6) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (RangeCodec6) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (RangeCodec6) Aux() uint32                 { return uint32(format.AddressFamilyIPv6) }
func (RangeCodec6) KeySize() int                { return 16 }
func (RangeCodec6) LeafSize() int               { return format.RangeRecordV6Size }

func (RangeCodec6) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 16 {
		return Key{}, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	return KeyOfU128(hi, lo), nil
}

// ReadKeyLimbs decodes the u128 order key of one fixed-size cell (the
// NumericKeyCodec seam).
func (RangeCodec6) ReadKeyLimbs(cell []byte) (uint64, uint64, error) {
	if len(cell) < 16 {
		return 0, 0, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	return hi, lo, nil
}

// PrefixKeyProbe opts the codec into the inline prefix probe (u128 LE).
func (RangeCodec6) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// Ipv6Key Ord; never called on the hot path, which uses the prefix
// probe).
func (RangeCodec6) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 16 {
		return 0, corrupt("range key is truncated")
	}
	hi, lo := format.U128(cell)
	thi, tlo := target.U128()
	return cmpU128(hi, lo, thi, tlo), nil
}

func (RangeCodec6) ReadLeaf(cell []byte) (RangeRecord[RangeKey6], error) {
	if len(cell) != format.RangeRecordV6Size {
		return RangeRecord[RangeKey6]{}, corrupt("range leaf has the wrong record size")
	}
	r, err := format.DecodeRangeRecordV6(cell)
	if err != nil {
		return RangeRecord[RangeKey6]{}, corrupt("range leaf is invalid")
	}
	return RangeRecord[RangeKey6]{
		From:  RangeKey6{Hi: r.FromHi, Lo: r.FromLo},
		To:    RangeKey6{Hi: r.ToHi, Lo: r.ToLo},
		Value: r.Value,
	}, nil
}

func (RangeCodec6) WriteKey(key Key, output []byte) {
	hi, lo := key.U128()
	format.PutU128(output, hi, lo)
}

func (RangeCodec6) Less(a, b RangeKey6) bool {
	return a.Hi < b.Hi || (a.Hi == b.Hi && a.Lo < b.Lo)
}
func (RangeCodec6) Equal(a, b RangeKey6) bool { return a == b }

func (RangeCodec6) Next(key RangeKey6) (RangeKey6, bool) {
	lo := key.Lo + 1
	hi := key.Hi
	if lo == 0 {
		hi++
		if hi == 0 {
			return RangeKey6{}, false
		}
	}
	return RangeKey6{Hi: hi, Lo: lo}, true
}

func (RangeCodec6) Previous(key RangeKey6) (RangeKey6, bool) {
	if key.Hi == 0 && key.Lo == 0 {
		return RangeKey6{}, false
	}
	lo := key.Lo - 1
	hi := key.Hi
	if lo == ^uint64(0) {
		hi--
	}
	return RangeKey6{Hi: hi, Lo: lo}, true
}

func (RangeCodec6) KeyOf(key RangeKey6) Key { return KeyOfU128(key.Hi, key.Lo) }

func (RangeCodec6) EncodeRecord(r RangeRecord[RangeKey6], output []byte) (int, error) {
	if err := format.EncodeRangeRecordV6(format.RangeRecordV6{
		FromHi: r.From.Hi, FromLo: r.From.Lo,
		ToHi: r.To.Hi, ToLo: r.To.Lo,
		Value: r.Value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV6Size, nil
}

// RangePrivateGap evaluates the local gap around one candidate range
// (Rust range_mutation::PrivateGap). The probe carries the concrete
// family so the emitted per-family gap layer calls it without any
// interface dispatch.
type RangePrivateGap[K any] struct {
	Family RangeFamily[K]
	R      RangeRecord[K]
}

func (g RangePrivateGap[K]) decode(cell []byte) (RangeRecord[K], error) {
	return g.Family.ReadLeaf(cell)
}

// Previous implements the non-generic LocalGap probe by value (Rust
// LocalGap::previous): the decision needs the decoded record, but the
// interface returns the raw probing cell so the generic tree selector
// can decode it once into the reject value.
func (g RangePrivateGap[K]) Previous(exact bool, cell []byte) (LocalPrevious, []byte, error) {
	if cell == nil {
		return LocalPreviousAccept, nil, nil
	}
	previous, err := g.decode(cell)
	if err != nil {
		return 0, nil, err
	}
	bridges := false
	if next, ok := g.Family.Next(previous.To); ok {
		bridges = g.Family.Equal(next, g.R.From)
	}
	if exact || !g.Family.Less(previous.To, g.R.From) ||
		(previous.Value == g.R.Value && bridges) {
		return LocalPreviousReject, cell, nil
	}
	return LocalPreviousAccept, nil, nil
}

// Next implements the non-generic LocalGap probe by value (Rust
// LocalGap::next).
func (g RangePrivateGap[K]) Next(cell []byte) (LocalNext, []byte, error) {
	if cell == nil {
		return LocalNextAccept, nil, nil
	}
	next, err := g.decode(cell)
	if err != nil {
		return 0, nil, err
	}
	bridges := false
	if boundary, ok := g.Family.Next(g.R.To); ok {
		bridges = g.Family.Equal(boundary, next.From)
	}
	if g.Family.Less(g.R.To, next.From) && (next.Value != g.R.Value || !bridges) {
		return LocalNextAccept, nil, nil
	}
	return LocalNextReject, cell, nil
}
