// Used bitmap tests (Rust used_bitmap_tests.rs).

package bitmap

import (
	"bytes"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// usedMemoryStore is the Rust MemoryStore of used_bitmap_tests.rs.
type usedMemoryStore struct {
	txn       uint64
	pages     [][format.PageSize]byte
	discarded []uint32
	retired   []uint32
}

func newUsedMemoryStore() *usedMemoryStore {
	return &usedMemoryStore{txn: 1, pages: make([][format.PageSize]byte, 2)}
}

func (m *usedMemoryStore) TargetTxn() uint64 { return m.txn }
func (m *usedMemoryStore) PageLimit() uint64 { return uint64(len(m.pages)) }

func (m *usedMemoryStore) pageView(pageNumber uint32) ([]byte, error) {
	if int(pageNumber) >= len(m.pages) {
		return nil, corrupt("test bitmap page is out of bounds")
	}
	return m.pages[pageNumber][:], nil
}

func (m *usedMemoryStore) Inspect(pageNumber uint32) ([]byte, error) {
	return m.pageView(pageNumber)
}

func (m *usedMemoryStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	page, err := m.pageView(pageNumber)
	if err != nil {
		return nil, 0, err
	}
	return page, 0, nil
}

func (m *usedMemoryStore) FinishEdit(page []byte, tag uint32) error {
	return nil
}

func (m *usedMemoryStore) Allocate() (uint32, error) {
	if uint64(len(m.pages)) >= 1<<32 {
		return 0, invalid("test page space exhausted")
	}
	pageNumber := uint32(len(m.pages))
	m.pages = append(m.pages, [format.PageSize]byte{})
	return pageNumber, nil
}

func (m *usedMemoryStore) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	src, err := m.pageView(source)
	if err != nil {
		return nil, nil, 0, err
	}
	dst, err := m.pageView(destination)
	if err != nil {
		return nil, nil, 0, err
	}
	return src, dst, 0, nil
}

func (m *usedMemoryStore) DiscardPrivate(pageNumber uint32) error {
	m.discarded = append(m.discarded, pageNumber)
	return nil
}

func (m *usedMemoryStore) RetirePages(retired tree.RetiredPages) error {
	m.retired = append(m.retired, retired.Slice()...)
	return nil
}

func TestBigEndianPortableBitmapLeafMatchesLiteralBytes(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	if err := SetUsed(m, &root, 512, KindFeed, 8, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	page := m.pages[root][:]
	if !bytes.Equal(page[0:8], []byte{'I', 'P', '4', 'P', 0x0f, 0x00, 0x20, 0x00}) {
		t.Fatalf("header prefix % x", page[0:8])
	}
	if !bytes.Equal(page[8:16], []byte{1, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("born txn % x", page[8:16])
	}
	if !bytes.Equal(page[16:24], []byte{1, 0, 0, 0, 0xc0, 0x0f, 0, 0x10}) {
		t.Fatalf("count/level/lower/upper % x", page[16:24])
	}
	if !bytes.Equal(page[24:28], []byte{2, 0, 0, 0}) {
		t.Fatalf("aux % x", page[24:28])
	}
	if !bytes.Equal(page[32:40], []byte{0, 1, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("leaf word 0 % x", page[32:40])
	}
}

func TestLowestZeroCrossesLeafBoundaryAndReusesClearedBit(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	for bit := uint32(0); bit < 32_000; bit++ {
		if err := SetUsed(m, &root, 32_002, KindFeed, bit, tree.NewRetiredPages()); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := TakeLowestUsed(m, &root, 32_002, KindFeed, tree.NewRetiredPages())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != 32_000 {
		t.Fatalf("take_lowest = (%d, %v), want (32000, true)", got, ok)
	}
	cleared, err := ClearUsed(m, &root, 32_002, KindFeed, 17, tree.NewRetiredPages())
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("clear(17) reported no change")
	}
	got, ok, err = TakeLowestUsed(m, &root, 32_002, KindFeed, tree.NewRetiredPages())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != 17 {
		t.Fatalf("take_lowest after clear = (%d, %v), want (17, true)", got, ok)
	}
}

func TestMembershipZeroIsNeverAnAllocationCandidate(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	for _, want := range []uint32{1, 2} {
		got, ok, err := TakeLowestUsed(m, &root, 3, KindMembership, tree.NewRetiredPages())
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != want {
			t.Fatalf("take_lowest = (%d, %v), want (%d, true)", got, ok, want)
		}
	}
	got, ok, err := TakeLowestUsed(m, &root, 3, KindMembership, tree.NewRetiredPages())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("membership take_lowest = %d, want none", got)
	}
}

func TestSequentialWordReadsCrossSparseLeafBoundaries(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	limit := uint64(32_002)
	for _, bit := range []uint32{3, 31_999, 32_001} {
		if err := SetUsed(m, &root, limit, KindFeed, bit, tree.NewRetiredPages()); err != nil {
			t.Fatal(err)
		}
	}

	words := make([]uint64, 501)
	for index := range words {
		words[index] = ^uint64(0)
	}
	if err := ReadWords(m, root, limit, KindFeed, 0, words); err != nil {
		t.Fatal(err)
	}
	if words[0] != 1<<3 {
		t.Fatalf("word 0 = %#x, want 1<<3", words[0])
	}
	for index := 1; index < 499; index++ {
		if words[index] != 0 {
			t.Fatalf("word %d = %#x, want 0", index, words[index])
		}
	}
	if words[499] != 1<<63 {
		t.Fatalf("word 499 = %#x, want 1<<63", words[499])
	}
	if words[500] != 1<<1 {
		t.Fatalf("word 500 = %#x, want 1<<1", words[500])
	}

	var crossing [2]uint64
	if err := ReadWords(m, root, limit, KindFeed, 499, crossing[:]); err != nil {
		t.Fatal(err)
	}
	if crossing[0] != 1<<63 || crossing[1] != 1<<1 {
		t.Fatalf("crossing words = [%#x %#x]", crossing[0], crossing[1])
	}
	if err := ReadWords(m, root, limit, KindFeed, 500, make([]uint64, 2)); err == nil {
		t.Fatal("word range beyond the limit was accepted")
	}
}

func TestClearOfFinalBitOmitsTheRoot(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	if err := SetUsed(m, &root, 8, KindFeed, 3, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	cleared, err := ClearUsed(m, &root, 8, KindFeed, 3, tree.NewRetiredPages())
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("clear(3) reported no change")
	}
	if root != 0 {
		t.Fatalf("root after final clear is %d, want 0", root)
	}
	if len(m.discarded) != 1 {
		t.Fatalf("discarded %d pages, want 1", len(m.discarded))
	}
}

func TestCommittedPathsAreCopiedBeforeMutation(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	if err := SetUsed(m, &root, 40_000, KindFeed, 32_001, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	committedRoot := root
	committed := make([][format.PageSize]byte, len(m.pages))
	copy(committed, m.pages)
	m.txn = 2
	retired := tree.NewRetiredPages()
	cleared, err := ClearUsed(m, &root, 40_000, KindFeed, 32_001, retired)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("clear(32001) reported no change")
	}
	if root == committedRoot {
		t.Fatal("root was mutated in place")
	}
	for index := range committed {
		if committed[index] != m.pages[index] {
			t.Fatalf("committed page %d changed during the draft", index)
		}
	}
	if retired.Len() != 2 {
		t.Fatalf("cross-draft clear retired %d pages, want 2", retired.Len())
	}
}

func TestMembershipLimitAndRootLevelShrinkWithTheHighestID(t *testing.T) {
	m := newUsedMemoryStore()
	root := uint32(0)
	for _, bit := range []uint32{1, 32_001} {
		if err := SetUsed(m, &root, 32_002, KindMembership, bit, tree.NewRetiredPages()); err != nil {
			t.Fatal(err)
		}
	}
	m.txn = 2
	retired := tree.NewRetiredPages()
	cleared, err := ClearUsed(m, &root, 32_002, KindMembership, 32_001, retired)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("clear(32001) reported no change")
	}
	if err := m.RetirePages(*retired); err != nil {
		t.Fatal(err)
	}
	limit, err := ShrinkMembership(m, &root, 32_002)
	if err != nil {
		t.Fatal(err)
	}
	if limit != 2 {
		t.Fatalf("shrunk limit = %d, want 2", limit)
	}
	if root == 0 {
		t.Fatal("root vanished after shrink")
	}
	if level := format.U16(m.pages[root][format.HeaderLevel:]); level != 0 {
		t.Fatalf("root level after shrink = %d, want 0", level)
	}
	if ok, err := contains(m, root, limit, KindMembership, 1); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("membership id 1 disappeared during shrink")
	}

	retired = tree.NewRetiredPages()
	cleared, err = ClearUsed(m, &root, limit, KindMembership, 1, retired)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("final clear reported no change")
	}
	if err := m.RetirePages(*retired); err != nil {
		t.Fatal(err)
	}
	limit, err = ShrinkMembership(m, &root, limit)
	if err != nil {
		t.Fatal(err)
	}
	if limit != 1 {
		t.Fatalf("final shrunk limit = %d, want 1", limit)
	}
	if root != 0 {
		t.Fatalf("root after final shrink is %d, want 0", root)
	}
}
