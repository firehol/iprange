// Range edit tests mirroring the Rust range_mutation_tests.rs base
// vectors (assign/clear/transform semantics against a scalar reference).

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// rangeMemoryStore is the MemoryStore of the Rust range_mutation_tests:
// owned pages, retired and discarded logs, read/write counters.
type rangeMemoryStore struct {
	targetTxn uint64
	pages     [][format.PageSize]byte
	retired   []uint32
	discarded []uint32
	reads     uint64
	writes    uint64
	scratch   [3][format.RangeRecordV6Size]byte
	rangeCtx4 rangeCtx[key4]
	rangeCtx6 rangeCtx[key6]
}

func newRangeMemoryStore() *rangeMemoryStore {
	return &rangeMemoryStore{targetTxn: 1, pages: make([][format.PageSize]byte, 2)}
}

func (m *rangeMemoryStore) TargetTxn() uint64 { return m.targetTxn }
func (m *rangeMemoryStore) PageLimit() uint64 { return uint64(len(m.pages)) }

func (m *rangeMemoryStore) Inspect(pageNumber uint32) ([]byte, error) {
	m.reads++
	if int(pageNumber) >= len(m.pages) {
		return nil, corrupt("test page is out of bounds")
	}
	return m.pages[pageNumber][:], nil
}

func (m *rangeMemoryStore) Allocate() (uint32, error) {
	if len(m.pages) >= 1<<32 {
		return 0, invalid("test page space exhausted")
	}
	pageNumber := uint32(len(m.pages))
	m.pages = append(m.pages, [format.PageSize]byte{})
	return pageNumber, nil
}

func (m *rangeMemoryStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	m.writes++
	if int(pageNumber) >= len(m.pages) {
		return nil, 0, corrupt("test page is out of bounds")
	}
	return m.pages[pageNumber][:], 0, nil
}

// RestoreDirty re-arms one page's dirty marker after a successful
// mutation; the test store keeps no dirty chain, so the re-arm is a
// no-op.
func (m *rangeMemoryStore) RestoreDirty(pageNumber uint32, tag uint32) error {
	return nil
}

func (m *rangeMemoryStore) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	m.reads++
	m.writes++
	if int(source) >= len(m.pages) || int(destination) >= len(m.pages) {
		return nil, nil, 0, corrupt("test page is out of bounds")
	}
	return m.pages[source][:], m.pages[destination][:], 0, nil
}

func (m *rangeMemoryStore) DiscardPrivate(pageNumber uint32) error {
	m.discarded = append(m.discarded, pageNumber)
	return nil
}

func (m *rangeMemoryStore) RetirePages(retired tree.RetiredPages) error {
	m.retired = append(m.retired, retired.Slice()...)
	return nil
}

func (m *rangeMemoryStore) RangeRecordAdded(uint32) error   { return nil }
func (m *rangeMemoryStore) RangeRecordRemoved(uint32) error { return nil }

func rangesV4(m *rangeMemoryStore, root uint32) []format.RangeRecordV4 {
	var result []format.RangeRecordV4
	key := key4(0)
	for {
		value, ok, err := tree.AtOrAfter(rangeCodec4{}, m, root, rangeCodec4{}.KeyOf(key))
		if err != nil {
			panic(err)
		}
		if !ok {
			return result
		}
		result = append(result, format.RangeRecordV4{From: uint32(value.from), To: uint32(value.to), Value: value.value})
		next, ok := rangeCodec4{}.Next(value.from)
		if !ok {
			break
		}
		key = next
	}
	return result
}

func rangesV6(m *rangeMemoryStore, root uint32) []format.RangeRecordV6 {
	var result []format.RangeRecordV6
	key := key6{}
	for {
		value, ok, err := tree.AtOrAfter(rangeCodec6{}, m, root, rangeCodec6{}.KeyOf(key))
		if err != nil {
			panic(err)
		}
		if !ok {
			return result
		}
		result = append(result, format.RangeRecordV6{
			FromHi: value.from.hi, FromLo: value.from.lo,
			ToHi: value.to.hi, ToLo: value.to.lo,
			Value: value.value,
		})
		next, ok := rangeCodec6{}.Next(value.from)
		if !ok {
			break
		}
		key = next
	}
	return result
}

func newV4Ctx(m *rangeMemoryStore, root *uint32, count *uint64) *rangeCtx[key4] {
	ctx := &m.rangeCtx4
	ctx.family = rangeCodec4{}
	ctx.store = m
	ctx.storeView = m
	ctx.root = root
	ctx.count = count
	ctx.untracked = false
	ctx.scratch = &m.scratch
	return ctx
}

func newV6Ctx(m *rangeMemoryStore, root *uint32, count *uint64) *rangeCtx[key6] {
	ctx := &m.rangeCtx6
	ctx.family = rangeCodec6{}
	ctx.store = m
	ctx.storeView = m
	ctx.root = root
	ctx.count = count
	ctx.untracked = false
	ctx.scratch = &m.scratch
	return ctx
}

// TestBigEndianPortableRangeRecordMatchesLiteralBytes mirrors the Rust
// literal vector (little-endian from, to, value).
func TestBigEndianPortableRangeRecordMatchesLiteralBytes(t *testing.T) {
	r := rangeRecord[key4]{
		from:  key4(0x01020304),
		to:    key4(0x05060708),
		value: 0x090a0b0c,
	}
	var scratch [3][format.RangeRecordV6Size]byte
	ctx := &rangeCtx[key4]{family: rangeCodec4{}, scratch: &scratch}
	encoded, err := ctx.encodeRecord(0, r)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{4, 3, 2, 1, 8, 7, 6, 5, 0x0c, 0x0b, 0x0a, 9}
	if len(encoded) != len(want) {
		t.Fatalf("encoded length = %d, want %d", len(encoded), len(want))
	}
	for i := range want {
		if encoded[i] != want[i] {
			t.Fatalf("encoded[%d] = %#x, want %#x", i, encoded[i], want[i])
		}
	}
	decoded, err := rangeCodec4{}.ReadLeaf(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !(rangeCodec4{}).Equal(decoded.from, r.from) || !(rangeCodec4{}).Equal(decoded.to, r.to) || decoded.value != r.value {
		t.Fatalf("round-trip = %#v, want %#v", decoded, r)
	}
}

// TestIpv6RangeRecordMatchesLiteralBytes pins the IPv6 wire vector: the
// 128-bit keys encode little-endian with the low limb first (Rust
// key.rs write_le), followed by the value (Rust range_tree.rs
// encode_record).
func TestIpv6RangeRecordMatchesLiteralBytes(t *testing.T) {
	r := rangeRecord[key6]{
		from:  key6{hi: 0x0102030405060708, lo: 0x090a0b0c0d0e0f10},
		to:    key6{hi: 0x1112131415161718, lo: 0x191a1b1c1d1e1f20},
		value: 0x2a2b2c2d,
	}
	var scratch [3][format.RangeRecordV6Size]byte
	ctx := &rangeCtx[key6]{family: rangeCodec6{}, scratch: &scratch}
	encoded, err := ctx.encodeRecord(0, r)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x10, 0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a, 0x09,
		0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
		0x20, 0x1f, 0x1e, 0x1d, 0x1c, 0x1b, 0x1a, 0x19,
		0x18, 0x17, 0x16, 0x15, 0x14, 0x13, 0x12, 0x11,
		0x2d, 0x2c, 0x2b, 0x2a,
	}
	if len(encoded) != len(want) {
		t.Fatalf("encoded length = %d, want %d", len(encoded), len(want))
	}
	for i := range want {
		if encoded[i] != want[i] {
			t.Fatalf("encoded[%d] = %#x, want %#x", i, encoded[i], want[i])
		}
	}
	decoded, err := rangeCodec6{}.ReadLeaf(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !(rangeCodec6{}).Equal(decoded.from, r.from) || !(rangeCodec6{}).Equal(decoded.to, r.to) || decoded.value != r.value {
		t.Fatalf("round-trip = %#v, want %#v", decoded, r)
	}
}

// TestOverlappingRangesApplyInArrivalOrder mirrors the Rust test.
func TestOverlappingRangesApplyInArrivalOrder(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)

	if changed, err := rangeAssign(ctx, v4key(0), v4key(100), 1); err != nil || !changed {
		t.Fatalf("assign(0,100,1) = %v, %v", changed, err)
	}
	if changed, err := rangeAssign(ctx, v4key(20), v4key(30), 2); err != nil || !changed {
		t.Fatalf("assign(20,30,2) = %v, %v", changed, err)
	}
	checkRanges4(t, rangesV4(m, root), []format.RangeRecordV4{
		{From: 0, To: 19, Value: 1}, {From: 20, To: 30, Value: 2}, {From: 31, To: 100, Value: 1},
	})

	if changed, err := rangeAssign(ctx, v4key(25), v4key(120), 3); err != nil || !changed {
		t.Fatalf("assign(25,120,3) = %v, %v", changed, err)
	}
	if changed, err := rangeAssign(ctx, v4key(121), v4key(130), 3); err != nil || !changed {
		t.Fatalf("assign(121,130,3) = %v, %v", changed, err)
	}
	checkRanges4(t, rangesV4(m, root), []format.RangeRecordV4{
		{From: 0, To: 19, Value: 1}, {From: 20, To: 24, Value: 2}, {From: 25, To: 130, Value: 3},
	})
	if count != 3 {
		t.Fatalf("record count = %d, want 3", count)
	}

	if changed, err := rangeAssign(ctx, v4key(40), v4key(50), 3); err != nil || changed {
		t.Fatalf("same-value assign = %v, %v; want false", changed, err)
	}
	if count != 3 {
		t.Fatalf("record count after no-op = %d, want 3", count)
	}
}

// TestClearSplitsAndCoalescesWithoutTouchingAbsentSpace mirrors the Rust
// test including the work counters.
func TestClearSplitsAndCoalescesWithoutTouchingAbsentSpace(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	if _, err := rangeAssign(ctx, v4key(0), v4key(100), 7); err != nil {
		t.Fatal(err)
	}

	cleared, err := rangeClear(ctx, v4key(40), v4key(60))
	if err != nil || !cleared {
		t.Fatalf("clear(40,60) = %v, %v", cleared, err)
	}
	checkRanges4(t, rangesV4(m, root), []format.RangeRecordV4{
		{From: 0, To: 39, Value: 7}, {From: 61, To: 100, Value: 7},
	})
	if count != 2 {
		t.Fatalf("record count = %d, want 2", count)
	}

	if cleared, err := rangeClear(ctx, v4key(40), v4key(60)); err != nil || cleared {
		t.Fatalf("absent clear = %v, %v; want false", cleared, err)
	}

	if changed, err := rangeAssign(ctx, v4key(40), v4key(60), 7); err != nil || !changed {
		t.Fatalf("reassign = %v, %v", changed, err)
	}
	checkRanges4(t, rangesV4(m, root), []format.RangeRecordV4{{From: 0, To: 100, Value: 7}})
	if count != 1 {
		t.Fatalf("record count = %d, want 1", count)
	}
}

// TestEndpointArithmeticHandlesBothFullAddressSpaces mirrors the Rust
// test: full-space ranges on both families split at the extreme edges.
func TestEndpointArithmeticHandlesBothFullAddressSpaces(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	if _, err := rangeAssign(ctx, key4(0), key4(0xFFFFFFFF), 11); err != nil {
		t.Fatal(err)
	}
	if _, err := rangeAssign(ctx, key4(1), key4(0xFFFFFFFE), 12); err != nil {
		t.Fatal(err)
	}
	checkRanges4(t, rangesV4(m, root), []format.RangeRecordV4{
		{From: 0, To: 0, Value: 11}, {From: 1, To: 0xFFFFFFFE, Value: 12}, {From: 0xFFFFFFFF, To: 0xFFFFFFFF, Value: 11},
	})

	m6 := newRangeMemoryStore()
	root6 := uint32(0)
	count6 := uint64(0)
	ctx6 := newV6Ctx(m6, &root6, &count6)
	if _, err := rangeAssign(ctx6, key6{}, key6{hi: ^uint64(0), lo: ^uint64(0)}, 21); err != nil {
		t.Fatal(err)
	}
	if _, err := rangeAssign(ctx6, key6{hi: 0, lo: 1}, key6{hi: ^uint64(0), lo: ^uint64(0) - 1}, 22); err != nil {
		t.Fatal(err)
	}
	checkRanges6(t, rangesV6(m6, root6), []format.RangeRecordV6{
		{FromHi: 0, FromLo: 0, ToHi: 0, ToLo: 0, Value: 21},
		{FromHi: 0, FromLo: 1, ToHi: ^uint64(0), ToLo: ^uint64(0) - 1, Value: 22},
		{FromHi: ^uint64(0), FromLo: ^uint64(0), ToHi: ^uint64(0), ToLo: ^uint64(0), Value: 21},
	})
}

// TestTransformsMatchScalarStateAfterEachNonIdempotentOperation mirrors
// the Rust test.
func TestTransformsMatchScalarStateAfterEachNonIdempotentOperation(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	var expected [256]optionalValue
	random := uint32(0x9e3779b9)

	for step := 0; step < 200; step++ {
		random = random*1664525 + 1013904223
		first := int(random & 255)
		random = random*1664525 + 1013904223
		second := int(random & 255)
		from, to := first, second
		if from > to {
			from, to = to, from
		}
		mode := step % 4
		if _, err := rangeTransform(ctx, v4key(uint32(from)), v4key(uint32(to)), func(store RangeStore, old optionalValue) (optionalValue, error) {
			return mapped(old, mode), nil
		}); err != nil {
			t.Fatal(err)
		}
		for i := from; i <= to; i++ {
			expected[i] = mapped(expected[i], mode)
		}
		for address, wanted := range expected {
			pred, ok, err := tree.Predecessor(rangeCodec4{}, m, root, (rangeCodec4{}).KeyOf(key4(uint32(address))))
			if err != nil {
				t.Fatal(err)
			}
			actual := noneValue()
			if ok && !(rangeCodec4{}).Less(pred.to, key4(uint32(address))) {
				actual = someValue(pred.value)
			}
			if !sameOptional(actual, wanted) {
				t.Fatalf("step %d, address %d: value %v, want %v", step, address, actual, wanted)
			}
		}
	}
}

func mapped(value optionalValue, mode int) optionalValue {
	switch mode {
	case 0:
		if !value.present {
			return noneValue()
		}
		v := value.value ^ 3
		if v == 0 {
			return noneValue()
		}
		return someValue(v)
	case 1:
		v := uint32(0)
		if value.present {
			v = value.value
		}
		return someValue(v | 4)
	case 2:
		if value.present && value.value == 7 {
			return noneValue()
		}
		return value
	default:
		if !value.present {
			return someValue(9)
		}
		return noneValue()
	}
}

// TestRandomizedSequenceMatchesAScalarReferenceMap mirrors the Rust test.
func TestRandomizedSequenceMatchesAScalarReferenceMap(t *testing.T) {
	const space = 256
	var expected [space]optionalValue
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	random := uint32(0x6d2b79f5)

	for operation := 0; operation < 1000; operation++ {
		random = random*1664525 + 1013904223
		a := int(random) % space
		random = random*1664525 + 1013904223
		b := int(random) % space
		from, to := a, b
		if from > to {
			from, to = to, from
		}
		random = random*1664525 + 1013904223

		if operation%4 == 0 {
			if _, err := rangeClear(ctx, v4key(uint32(from)), v4key(uint32(to))); err != nil {
				t.Fatal(err)
			}
			for i := from; i <= to; i++ {
				expected[i] = noneValue()
			}
		} else {
			value := random % 7
			if _, err := rangeAssign(ctx, v4key(uint32(from)), v4key(uint32(to)), value); err != nil {
				t.Fatal(err)
			}
			for i := from; i <= to; i++ {
				expected[i] = someValue(value)
			}
		}

		actual := rangesV4(m, root)
		if uint64(len(actual)) != count {
			t.Fatalf("operation %d: record count = %d, tree has %d", operation, count, len(actual))
		}
		for address, wanted := range expected {
			found := noneValue()
			for _, r := range actual {
				if r.From <= uint32(address) && uint32(address) <= r.To {
					found = someValue(r.Value)
					break
				}
			}
			if !sameOptional(found, wanted) {
				t.Fatalf("operation %d, address %d: value %v, want %v", operation, address, found, wanted)
			}
		}
		for i := 1; i < len(actual); i++ {
			if !(actual[i-1].To < actual[i].From) {
				t.Fatalf("operation %d: overlapping records %v %v", operation, actual[i-1], actual[i])
			}
			if actual[i-1].To+1 == actual[i].From && actual[i-1].Value == actual[i].Value {
				t.Fatalf("operation %d: uncoalesced records %v %v", operation, actual[i-1], actual[i])
			}
		}
	}
}

// TestManyDisjointRangesSplitLeavesAndCowOnlyOncePerPath mirrors the Rust
// test: after the COW txn bumps, the second same-path write retires
// nothing.
func TestManyDisjointRangesSplitLeavesAndCowOnlyOncePerPath(t *testing.T) {
	m := newRangeMemoryStore()
	root := uint32(0)
	count := uint64(0)
	ctx := newV4Ctx(m, &root, &count)
	for key := int32(1999); key >= 0; key-- {
		if _, err := rangeAssign(ctx, v4key(uint32(key*2)), v4key(uint32(key*2)), uint32(key)); err != nil {
			t.Fatal(err)
		}
	}
	if count != 2000 {
		t.Fatalf("record count = %d, want 2000", count)
	}
	committed := make([][format.PageSize]byte, len(m.pages))
	copy(committed, m.pages)
	m.targetTxn = 2

	if _, err := rangeAssign(ctx, v4key(8000), v4key(8000), 9); err != nil {
		t.Fatal(err)
	}
	if len(m.retired) == 0 {
		t.Fatal("first COW assignment retired nothing")
	}
	for i := range committed {
		if committed[i] != m.pages[i] {
			t.Fatalf("committed page %d changed", i)
		}
	}

	retiredAfterFirst := len(m.retired)
	if _, err := rangeAssign(ctx, v4key(8002), v4key(8002), 10); err != nil {
		t.Fatal(err)
	}
	if len(m.retired) != retiredAfterFirst {
		t.Fatalf("second same-path assignment retired %d pages, want %d", len(m.retired), retiredAfterFirst)
	}
	if count != 2002 {
		t.Fatalf("record count = %d, want 2002", count)
	}
}

func v4key(v uint32) key4 { return key4(v) }

func checkRanges4(t *testing.T, got []format.RangeRecordV4, want []format.RangeRecordV4) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranges = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranges = %v, want %v", got, want)
		}
	}
}

func checkRanges6(t *testing.T, got []format.RangeRecordV6, want []format.RangeRecordV6) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranges = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranges = %v, want %v", got, want)
		}
	}
}
