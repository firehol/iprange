// Width-specialized probe tests (SOW-0027 regression slice H): the
// emitted per-width search loops must behave exactly like the generic
// fixedLowerBound for the same pages and keys, and the u128 probe must
// decode the wire order of format.U128/PutU128 (low limb at offset 0,
// high limb at offset 8). The v6-shaped regression pins the endpoint
// arithmetic scenario that previously looped the writer's range walk.

package tree

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// u128LE is a fixed 16-byte codec whose cells carry the wire u128 key
// low limb first (format.PutU128), like the range v6 records.
type u128LE struct{}

func (u128LE) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (u128LE) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (u128LE) Aux() uint32                 { return 0 }
func (u128LE) KeySize() int                { return 16 }
func (u128LE) LeafSize() int               { return 24 }
func (u128LE) PrefixKeyProbe()             {}
func (u128LE) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 16 {
		return Key{}, corrupt("u128 key truncated")
	}
	hi, lo := format.U128(cell)
	return KeyOfU128(hi, lo), nil
}
func (u128LE) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 16 {
		return 0, corrupt("u128 key truncated")
	}
	hi, lo := format.U128(cell)
	thi, tlo := target.U128()
	return cmpU128(hi, lo, thi, tlo), nil
}
func (u128LE) ReadLeaf(cell []byte) (u128Key, error) {
	if len(cell) != 24 {
		return u128Key{}, corrupt("u128 leaf size")
	}
	hi, lo := format.U128(cell)
	return u128Key{hi: hi, lo: lo}, nil
}
func (u128LE) WriteKey(key Key, output []byte) {
	hi, lo := key.U128()
	format.PutU128(output, hi, lo)
}

type u128Key struct{ hi, lo uint64 }

func u128Record(hi, lo uint64) []byte {
	cell := make([]byte, 24)
	format.PutU128(cell, hi, lo)
	return cell
}

// u64u32 is a fixed 12-byte codec: a u64 wire word at offset 0 and a
// u32 wire word at offset 8 (the (u64, u32) key family).
type u64u32 struct{}

func (u64u32) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (u64u32) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (u64u32) Aux() uint32                 { return 0 }
func (u64u32) KeySize() int                { return 12 }
func (u64u32) LeafSize() int               { return 16 }
func (u64u32) PrefixKeyProbe()             {}
func (u64u32) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 12 {
		return Key{}, corrupt("u64u32 key truncated")
	}
	return keyFromU64U32(format.U64(cell), format.U32(cell[8:12])), nil
}
func (u64u32) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 12 {
		return 0, corrupt("u64u32 key truncated")
	}
	if compare := cmpU64(format.U64(cell), target.U64()); compare != 0 {
		return compare, nil
	}
	return cmpU32(format.U32(cell[8:12]), beU32(target.data[8:12])), nil
}
func (u64u32) ReadLeaf(cell []byte) (u64u32Key, error) {
	if len(cell) != 16 {
		return u64u32Key{}, corrupt("u64u32 leaf size")
	}
	return u64u32Key{hi: format.U64(cell), lo: format.U32(cell[8:12])}, nil
}
func (u64u32) WriteKey(key Key, output []byte) {
	format.PutU64(output, key.U64())
	format.PutU32(output[8:12], beU32(key.data[8:12]))
}

type u64u32Key struct {
	hi uint64
	lo uint32
}

func keyFromU64U32(hi uint64, lo uint32) Key {
	var canonical [12]byte
	bePutU64(canonical[:8], hi)
	canonical[8] = byte(lo >> 24)
	canonical[9] = byte(lo >> 16)
	canonical[10] = byte(lo >> 8)
	canonical[11] = byte(lo)
	return KeyOfFixed(canonical[:])
}

func u64u32Record(hi uint64, lo uint32) []byte {
	cell := make([]byte, 16)
	format.PutU64(cell, hi)
	format.PutU32(cell[8:12], lo)
	return cell
}

// TestWidthProbesMatchGenericSearch builds one asymmetric leaf per
// emitted width and checks the specialized search agrees with the
// generic fixedLowerBound for every probe key and both insertion
// modes. Asymmetric keys (hi != lo) exercise the wire-order decode.
func TestWidthProbesMatchGenericSearch(t *testing.T) {
	type widthCase struct {
		codec   Codec[u128Key]
		records [][]byte
		keys    []Key
	}
	m := newMemoryStore()
	root := uint32(0)
	records := [][]byte{
		u128Record(0, 0),
		u128Record(0, 1),
		u128Record(0, 0xffff_ffff_0000_0000), // asymmetric: hi 0, lo large
		u128Record(7, 0),
		u128Record(^uint64(0), ^uint64(0)),
	}
	for _, r := range records {
		if _, _, err := Insert(u128LE{}, m, &root, r, RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	header, err := parse(u128LE{}, m.pages[root][:], m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	probes := []uint64{0, 1, 2, 0xffff_ffff_0000_0000, 5, 6, 7, 8, ^uint64(0)}
	for _, lo := range probes {
		key := KeyOfU128(uint64(0), lo)
		checkProbe(t, u128LE{}, m.pages[root][:], &header, key)
		key = KeyOfU128(uint64(7), lo)
		checkProbe(t, u128LE{}, m.pages[root][:], &header, key)
	}
	m2 := newMemoryStore()
	root2 := uint32(0)
	records2 := [][]byte{
		u64u32Record(0, 0),
		u64u32Record(0, 1),
		u64u32Record(0, 0xFFFF0000),
		u64u32Record(9, 0),
		u64u32Record(^uint64(0), ^uint32(0)),
	}
	for _, r := range records2 {
		if _, _, err := Insert(u64u32{}, m2, &root2, r, RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	header2, err := parse(u64u32{}, m2.pages[root2][:], m2.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, hi := range []uint64{0, 9} {
		for _, lo := range []uint32{0, 1, 2, 0xFFFF0000, 0xFFFF0001, ^uint32(0)} {
			checkProbe(t, u64u32{}, m2.pages[root2][:], &header2, keyFromU64U32(hi, lo))
		}
	}
}

// checkProbe asserts that the dispatched search (which selects the
// emitted width loop) returns exactly what the generic fixedLowerBound
// returns for the same page, key, and insertion mode.
func checkProbe[T any](t *testing.T, codec Codec[T], page []byte, header *Header, key Key) {
	t.Helper()
	cellLen, fixed := FixedCellSize(codec, header.Level)
	if !fixed {
		t.Fatal("test codec is not fixed-size")
	}
	for _, insertion := range []bool{false, true} {
		wantIdx, wantExact, wantErr := fixedLowerBound(page, header, cellLen, codec.KeySize(), key, insertion, nil)
		gotIdx, gotExact, gotErr := lowerBound(codec, page, header, key, insertion)
		if (wantErr == nil) != (gotErr == nil) ||
			(gotErr == nil && (gotIdx != wantIdx || gotExact != wantExact)) {
			t.Fatalf("key %v insertion=%v: lowerBound = (%d,%v,%v), generic fixedLowerBound = (%d,%v,%v)",
				key.FixedBytes(), insertion, gotIdx, gotExact, gotErr, wantIdx, wantExact, wantErr)
		}
	}
}

// TestV6EndpointWalkTerminates pins the range-mutation endpoint
// scenario whose previous u128 probe order inverted asymmetric limbs:
// the walk from (0,0) over records (0,0), (0,1)..(MAX,MAX-1),
// (MAX,MAX) must visit exactly the three records and stop.
func TestV6EndpointWalkTerminates(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for _, r := range [][]byte{
		u128Record(0, 0),
		u128Record(0, 1),
		u128Record(^uint64(0), ^uint64(0)),
	} {
		if _, _, err := Insert(u128LE{}, m, &root, r, RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	var got []u128Key
	key := KeyOfU128(uint64(0), uint64(0))
	for step := 0; step < 8; step++ {
		v, ok, err := AtOrAfter(u128LE{}, m, root, key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, v)
		hi, lo := v.hi, v.lo
		lo++
		if lo == 0 {
			hi++
			if hi == 0 {
				break
			}
		}
		key = KeyOfU128(hi, lo)
	}
	if len(got) != 3 {
		t.Fatalf("walk visited %d records, want 3 (got %+v)", len(got), got)
	}
	want := []u128Key{{0, 0}, {0, 1}, {^uint64(0), ^uint64(0)}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("walk record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
