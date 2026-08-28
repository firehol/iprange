// Fixed-tree mutation tests mirroring the Rust fixed_tree_tests.rs
// vectors, adapted to the Go fixed-cell tree core.

package tree

import (
	"encoding/binary"
	"sort"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// u32Codec is the U32Codec of the Rust tests: 4-byte keys in 8-byte leaf
// cells (key, value).
type u32Codec struct{}

func (u32Codec) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (u32Codec) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (u32Codec) Aux() uint32                 { return 0 }
func (u32Codec) KeySize() int                { return 4 }
func (u32Codec) LeafSize() int               { return 8 }
func (u32Codec) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 4 {
		return Key{}, corrupt("test u32 key is truncated")
	}
	return Key{Hi: uint64(format.U32(cell))}, nil
}
func (u32Codec) PrefixKeyProbe() {}
func (u32Codec) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 4 {
		return 0, corrupt("test u32 key is truncated")
	}
	return cmpU32(format.U32(cell), uint32(target.Hi)), nil
}
func (u32Codec) ReadLeaf(cell []byte) (u32Leaf, error) {
	if len(cell) != 8 {
		return u32Leaf{}, corrupt("test leaf size is invalid")
	}
	return u32Leaf{key: format.U32(cell), value: format.U32(cell[4:])}, nil
}
func (u32Codec) WriteKey(key Key, output []byte) {
	format.PutU32(output, uint32(key.Hi))
}

type u32Leaf struct {
	key   uint32
	value uint32
}

// wideCodec is the WideCodec of the Rust tests: 56-byte keys in 64-byte
// leaf cells. The Go Key keeps the first 16 key bytes; all test keys
// differ in their first four bytes.
type wideCodec struct{}

func (wideCodec) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (wideCodec) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (wideCodec) Aux() uint32                 { return 0 }
func (wideCodec) KeySize() int                { return 56 }
func (wideCodec) LeafSize() int               { return 64 }
func (wideCodec) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 16 {
		return Key{}, corrupt("test wide key is truncated")
	}
	return Key{
		Hi: binary.BigEndian.Uint64(cell),
		Lo: binary.BigEndian.Uint64(cell[8:]),
	}, nil
}
func (wideCodec) CompareKey(cell []byte, _ uint16, target Key) (int, error) {
	if len(cell) < 16 {
		return 0, corrupt("test wide key is truncated")
	}
	hi := binary.BigEndian.Uint64(cell)
	lo := binary.BigEndian.Uint64(cell[8:])
	if compare := cmpU64(hi, target.Hi); compare != 0 {
		return compare, nil
	}
	return cmpU64(lo, target.Lo), nil
}
func (wideCodec) ReadLeaf(cell []byte) (wideLeaf, error) {
	if len(cell) != 64 {
		return wideLeaf{}, corrupt("test leaf size is invalid")
	}
	return wideLeaf{key: binary.BigEndian.Uint32(cell), value: binary.LittleEndian.Uint64(cell[56:])}, nil
}
func (wideCodec) WriteKey(key Key, output []byte) {
	binary.BigEndian.PutUint64(output, key.Hi)
	binary.BigEndian.PutUint64(output[8:], key.Lo)
}

type wideLeaf struct {
	key   uint32
	value uint64
}

func u32Record(key, value uint32) []byte {
	cell := make([]byte, 8)
	format.PutU32(cell, key)
	format.PutU32(cell[4:], value)
	return cell
}

func wideRecord(key uint32) []byte {
	cell := make([]byte, 64)
	binary.BigEndian.PutUint32(cell, key)
	binary.LittleEndian.PutUint64(cell[56:], uint64(key))
	return cell
}

func u32Key(key uint32) Key { return Key{Hi: uint64(key)} }

func wideKey(key uint32) Key { return Key{Hi: uint64(key) << 32} }

// memoryStore is the MemoryStore of the Rust tests: owned pages, counters,
// and a discard log. Tests own complete pages; production stores never do.
type memoryStore struct {
	targetTxn   uint64
	pages       [][format.PageSize]byte
	discarded   []uint32
	reads       uint64
	inspections uint64
	writes      uint64
	updates     uint64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{targetTxn: 1, pages: make([][format.PageSize]byte, 2)}
}

func (m *memoryStore) TargetTxn() uint64 { return m.targetTxn }
func (m *memoryStore) PageLimit() uint64 { return uint64(len(m.pages)) }

func pageView(pages [][format.PageSize]byte, pageNumber uint32) ([]byte, error) {
	if int(pageNumber) >= len(pages) {
		return nil, corrupt("test page is out of bounds")
	}
	return pages[pageNumber][:], nil
}

func (m *memoryStore) Inspect(pageNumber uint32) ([]byte, error) {
	m.inspections++
	return pageView(m.pages, pageNumber)
}

func (m *memoryStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	m.updates++
	page, err := pageView(m.pages, pageNumber)
	if err != nil {
		return nil, 0, err
	}
	return page, 0, nil
}

// RestoreDirty re-arms the dirty tag after a mutation; the memory store
// has no checksum slot, so the tag is a no-op.
func (m *memoryStore) RestoreDirty(pageNumber uint32, tag uint32) error {
	return nil
}

func (m *memoryStore) Allocate() (uint32, error) {
	if len(m.pages) >= 1<<32 {
		return 0, invalid("test page space exhausted")
	}
	pageNumber := uint32(len(m.pages))
	m.pages = append(m.pages, [format.PageSize]byte{})
	return pageNumber, nil
}

func (m *memoryStore) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	m.reads++
	m.writes++
	src, err := pageView(m.pages, source)
	if err != nil {
		return nil, nil, 0, err
	}
	dst, err := pageView(m.pages, destination)
	if err != nil {
		return nil, nil, 0, err
	}
	return src, dst, 0, nil
}

func (m *memoryStore) DiscardPrivate(pageNumber uint32) error {
	m.discarded = append(m.discarded, pageNumber)
	return nil
}

// lookupU32 is the test lookup of the Rust tests: a plain descent that
// returns the value stored for key, if present.
func lookupU32(m *memoryStore, root uint32, key uint32) (uint32, bool, error) {
	if root == 0 {
		return 0, false, nil
	}
	pageNumber := root
	var expectedLevel uint16
	checkLevel := false
	for {
		var value uint32
		found := false
		child := uint32(0)
		page, err := m.Inspect(pageNumber)
		if err != nil {
			return 0, false, err
		}
		header, err := parse(u32Codec{}, page, m.TargetTxn(), expectedLevel, checkLevel)
		if err != nil {
			return 0, false, err
		}
		if header.Level == 0 {
			index, exists, err := lowerBound(u32Codec{}, page, &header, u32Key(key), true)
			if err != nil {
				return 0, false, err
			}
			if exists {
				cell, err := codecCell(u32Codec{}, page, &header, index)
				if err != nil {
					return 0, false, err
				}
				value = format.U32(cell[4:])
				found = true
			}
			return value, found, nil
		}
		index, _, err := lowerBound(u32Codec{}, page, &header, u32Key(key), false)
		if err != nil {
			return 0, false, err
		}
		child, err = branchChild(u32Codec{}, page, &header, index, m.PageLimit())
		if err != nil {
			return 0, false, err
		}
		expectedLevel = header.Level - 1
		checkLevel = true
		pageNumber = child
	}
}

func TestInsertReplaceSplitOrder(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 999; key >= 0; key-- {
		retired := RetiredPages{}
		retired, changed, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key+10)), retired)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Fatalf("key %d was not reported as inserted", key)
		}
		if retired.Len() != 0 {
			t.Fatalf("fresh-draft insert retired %v", retired.Slice())
		}
	}
	if root == 0 {
		t.Fatal("tree root is empty")
	}
	for key := 0; key < 1000; key++ {
		got, ok, err := lookupU32(m, root, uint32(key))
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != uint32(key+10) {
			t.Fatalf("key %d: got (%d, %v), want (%d, true)", key, got, ok, key+10)
		}
	}
	if _, ok, err := lookupU32(m, root, 1001); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("key 1001 was found in a 0..999 tree")
	}

	pred, found, err := Predecessor(u32Codec{}, m, root, u32Key(501))
	if err != nil {
		t.Fatal(err)
	}
	if !found || pred.key != 501 {
		t.Fatalf("predecessor(501) = %#v, want key 501", pred)
	}
	next, found, err := AtOrAfter(u32Codec{}, m, root, u32Key(501))
	if err != nil {
		t.Fatal(err)
	}
	if !found || next.key != 501 {
		t.Fatalf("at_or_after(501) = %#v, want key 501", next)
	}
	if _, found, err := Predecessor(u32Codec{}, m, root, u32Key(0)); err != nil {
		t.Fatal(err)
	} else if !found {
		t.Fatal("predecessor(0) is unexpectedly empty")
	}
	if next, found, err := AtOrAfter(u32Codec{}, m, root, u32Key(1001)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatalf("at_or_after(1001) = %#v, want none", next)
	}

	retired := RetiredPages{}
	retired, changed, err := Insert(u32Codec{}, m, &root, u32Record(500, 7), retired)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("duplicate insert reported a change")
	}
	if got, ok, err := lookupU32(m, root, 500); err != nil {
		t.Fatal(err)
	} else if !ok || got != 7 {
		t.Fatalf("key 500 after replace: (%d, %v), want (7, true)", got, ok)
	}
}

func TestOneShotReadsVisitEachPathPageOnce(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key+10)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	header, err := parse(u32Codec{}, m.pages[root][:], m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	pathPages := uint64(header.Level) + 1

	m.inspections = 0
	value, found, err := Predecessor(u32Codec{}, m, root, u32Key(501))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value.key != 501 {
		t.Fatalf("predecessor(501) = %#v", value)
	}
	if m.inspections != pathPages {
		t.Fatalf("predecessor visited %d pages, want %d", m.inspections, pathPages)
	}

	m.inspections = 0
	value, found, err = AtOrAfter(u32Codec{}, m, root, u32Key(501))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value.key != 501 {
		t.Fatalf("at_or_after(501) = %#v", value)
	}
	if m.inspections != pathPages {
		t.Fatalf("at_or_after visited %d pages, want %d", m.inspections, pathPages)
	}
}

// TestAtOrAfterAdvancesAcrossLeafBoundaries pins the cursor NextLeaf
// advance: a seek key that lands at the end of a non-rightmost leaf must
// continue into the next leaf instead of stopping.
func TestAtOrAfterAdvancesAcrossLeafBoundaries(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key += 2 {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	for odd := uint32(1); odd < 998; odd += 2 {
		next, found, err := AtOrAfter(u32Codec{}, m, root, u32Key(odd))
		if err != nil {
			t.Fatal(err)
		}
		if !found || next.key != odd+1 {
			t.Fatalf("at_or_after(%d) = %#v, want key %d", odd, next, odd+1)
		}
		pred, found, err := Predecessor(u32Codec{}, m, root, u32Key(odd))
		if err != nil {
			t.Fatal(err)
		}
		if !found || pred.key != odd-1 {
			t.Fatalf("predecessor(%d) = %#v, want key %d", odd, pred, odd-1)
		}
	}
	if next, found, err := AtOrAfter(u32Codec{}, m, root, u32Key(999)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatalf("at_or_after(999) = %#v, want none", next)
	}
	// Rust parity: predecessor returns the exact match when the key
	// exists, so predecessor(0) is the record for key 0 itself.
	if pred, found, err := Predecessor(u32Codec{}, m, root, u32Key(0)); err != nil {
		t.Fatal(err)
	} else if !found || pred.key != 0 {
		t.Fatalf("predecessor(0) = %#v, want key 0", pred)
	}
}

func TestFixedSearchRejectsForgedPageShape(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	if _, _, err := Insert(u32Codec{}, m, &root, u32Record(1, 1), RetiredPages{}); err != nil {
		t.Fatal(err)
	}
	header, err := parse(u32Codec{}, m.pages[root][:], m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	header.ItemCount++
	if _, _, err := lowerBound(u32Codec{}, m.pages[root][:], &header, u32Key(1), true); err == nil {
		t.Fatal("forged item count was accepted")
	}
}

func TestFixedSearchChecksEachPersistentSlotExtent(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	if _, _, err := Insert(u32Codec{}, m, &root, u32Record(1, 1), RetiredPages{}); err != nil {
		t.Fatal(err)
	}
	if _, err := parse(u32Codec{}, m.pages[root][:], m.TargetTxn(), 0, false); err != nil {
		t.Fatal(err)
	}
	format.PutU16(m.pages[root][format.SlottedHeaderSize:], 0)
	if _, _, err := lowerBound(u32Codec{}, m.pages[root][:], &Header{ItemCount: 1}, u32Key(1), true); err == nil {
		t.Fatal("corrupt slot extent was accepted")
	}
}

func TestNextTransactionCopiesOnlyItsSelectedPath(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot := root
	committed := make([][format.PageSize]byte, len(m.pages))
	copy(committed, m.pages)
	m.targetTxn = 2

	retired := RetiredPages{}
	retired, changed, err := Insert(u32Codec{}, m, &root, u32Record(1000, 99), retired)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("key 1000 was not inserted")
	}
	if retired.Len() != 2 {
		t.Fatalf("cross-draft insert retired %d pages, want 2", retired.Len())
	}
	if root == oldRoot {
		t.Fatal("root was copied in place")
	}
	for index := range committed {
		if committed[index] != m.pages[index] {
			t.Fatalf("committed page %d changed during the draft", index)
		}
	}
	if got, ok, err := lookupU32(m, root, 999); err != nil {
		t.Fatal(err)
	} else if !ok || got != 999 {
		t.Fatalf("key 999 after draft: (%d, %v)", got, ok)
	}
	if got, ok, err := lookupU32(m, root, 1000); err != nil {
		t.Fatal(err)
	} else if !ok || got != 99 {
		t.Fatalf("key 1000 after draft: (%d, %v)", got, ok)
	}

	samePath := RetiredPages{}
	if _, _, err := Insert(u32Codec{}, m, &root, u32Record(1001, 100), samePath); err != nil {
		t.Fatal(err)
	}
	if samePath.Len() != 0 {
		t.Fatalf("same-path insert retired %v", samePath.Slice())
	}
}

func TestPrivateTreeReleaseVisitsEveryPageOnce(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 1000; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	pageCount := len(m.pages)
	if err := DiscardPrivateTree(u32Codec{}, m, root, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	sort.Slice(m.discarded, func(i, j int) bool { return m.discarded[i] < m.discarded[j] })
	want := make([]uint32, 0, pageCount-2)
	for page := uint32(2); page < uint32(pageCount); page++ {
		want = append(want, page)
	}
	if len(m.discarded) != len(want) {
		t.Fatalf("discarded %d pages, want %d", len(m.discarded), len(want))
	}
	for index := range want {
		if m.discarded[index] != want[index] {
			t.Fatalf("discarded page %d, want %d", m.discarded[index], want[index])
		}
	}
}

func TestDeletionRemovesEmptyChildrenAndCollapsesRoot(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 0; key < 900; key++ {
		if _, _, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	for key := 0; key < 900; key++ {
		if _, err := DeleteExisting(u32Codec{}, m, &root, u32Key(uint32(key)), RetiredPages{}); err != nil {
			t.Fatalf("delete %d: %v", key, err)
		}
		if _, ok, err := lookupU32(m, root, uint32(key)); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatalf("key %d survived deletion", key)
		}
	}
	if root != 0 {
		t.Fatalf("root after full deletion is %d, want 0", root)
	}
	if len(m.discarded) == 0 {
		t.Fatal("full deletion discarded no private pages")
	}
}

func TestBranchSplitsCreateAndSearchThreeLevelTree(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for key := 4999; key >= 0; key-- {
		if _, _, err := Insert(wideCodec{}, m, &root, wideRecord(uint32(key)), RetiredPages{}); err != nil {
			t.Fatal(err)
		}
	}
	header, err := parse(wideCodec{}, m.pages[root][:], m.TargetTxn(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if header.Level != 2 {
		t.Fatalf("wide tree root level is %d, want 2", header.Level)
	}
	pred, found, err := Predecessor(wideCodec{}, m, root, wideKey(4999))
	if err != nil {
		t.Fatal(err)
	}
	if !found || pred.key != 4999 {
		t.Fatalf("predecessor(4999) = %#v", pred)
	}
}

// TestRemoveLeafRunMidRunRejection removes only the accepted prefix when
// the include predicate rejects a record in the middle of the leaf (Rust
// remove_leaf_run parity). Regression: the early-return shape reported
// the accepted prefix without applying the physical page edit, leaving
// the prefix in the tree and the removal count at zero.
func TestRemoveLeafRunMidRunRejection(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for _, key := range []uint32{0, 1, 2} {
		retired := RetiredPages{}
		retired, _, err := Insert(u32Codec{}, m, &root, u32Record(key, key+10), retired)
		if err != nil {
			t.Fatal(err)
		}
		if retired.Len() != 0 {
			t.Fatalf("insert of key %d retired pages", key)
		}
	}
	run, err := RemoveLeafRun(u32Codec{}, m, &root, u32Key(0), func(leaf u32Leaf) (bool, error) {
		return leaf.key == 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Removed != 1 {
		t.Fatalf("Removed = %d, want 1", run.Removed)
	}
	if !run.HasFollowing {
		t.Fatal("Following is absent, want the rejected record at key 1")
	}
	if !run.Following.Key.Equal(u32Key(1)) || run.Following.Leaf.key != 1 || run.Following.Leaf.value != 11 {
		t.Fatalf("Following = %#v, want key 1 value 11", run.Following)
	}
	for _, key := range []uint32{0, 1, 2} {
		got, ok, err := lookupU32(m, root, key)
		if err != nil {
			t.Fatal(err)
		}
		wantPresent := key != 0
		if ok != wantPresent {
			t.Fatalf("key %d present = %v, want %v", key, ok, wantPresent)
		}
		if ok && got != key+10 {
			t.Fatalf("key %d value = %d, want %d", key, got, key+10)
		}
	}
}

// TestRemoveLeafRunWholeLeafAndImmediateRejection covers the run
// endpoints: a run covering the whole leaf discards the leaf and reports
// no following edge; an include predicate that rejects the first record
// removes nothing and reports the rejected record as the following edge.
func TestRemoveLeafRunWholeLeafAndImmediateRejection(t *testing.T) {
	m := newMemoryStore()
	root := uint32(0)
	for _, key := range []uint32{0, 1, 2} {
		retired := RetiredPages{}
		retired, _, err := Insert(u32Codec{}, m, &root, u32Record(key, key+10), retired)
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err := RemoveLeafRun(u32Codec{}, m, &root, u32Key(0), func(leaf u32Leaf) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Removed != 3 {
		t.Fatalf("whole-leaf Removed = %d, want 3", run.Removed)
	}
	if run.HasFollowing {
		t.Fatalf("whole-leaf Following = %#v, want absent", run.Following)
	}
	if root != 0 {
		t.Fatalf("whole-leaf removal left root %d, want empty root", root)
	}

	m2 := newMemoryStore()
	root2 := uint32(0)
	for _, key := range []uint32{0, 1, 2} {
		retired := RetiredPages{}
		retired, _, err := Insert(u32Codec{}, m2, &root2, u32Record(key, key+10), retired)
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err = RemoveLeafRun(u32Codec{}, m2, &root2, u32Key(0), func(leaf u32Leaf) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Removed != 0 {
		t.Fatalf("immediate-rejection Removed = %d, want 0", run.Removed)
	}
	if !run.HasFollowing || !run.Following.Key.Equal(u32Key(0)) {
		t.Fatalf("immediate-rejection Following = %#v, want key 0", run.Following)
	}
	for _, key := range []uint32{0, 1, 2} {
		got, ok, err := lookupU32(m2, root2, key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != key+10 {
			t.Fatalf("key %d changed by rejected removal: (%d, %v)", key, got, ok)
		}
	}
}
