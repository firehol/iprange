// Free bitmap tests across leaf and branch boundaries (Rust
// free_bitmap_tests.rs).

package bitmap

import (
	"bytes"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

type bitmapMemoryStore struct {
	txn       uint64
	pages     [][format.PageSize]byte
	discarded []uint32
	forbidden uint32
}

func newBitmapMemoryStore(txn uint64) *bitmapMemoryStore {
	return &bitmapMemoryStore{txn: txn, pages: make([][format.PageSize]byte, 2)}
}

func (m *bitmapMemoryStore) TargetTxn() uint64 { return m.txn }
func (m *bitmapMemoryStore) PageLimit() uint64 { return uint64(len(m.pages)) }

func (m *bitmapMemoryStore) pageView(pageNumber uint32) ([]byte, error) {
	if int(pageNumber) >= len(m.pages) {
		return nil, corrupt("test page is out of bounds")
	}
	return m.pages[pageNumber][:], nil
}

func (m *bitmapMemoryStore) Inspect(pageNumber uint32) ([]byte, error) {
	return m.pageView(pageNumber)
}

func (m *bitmapMemoryStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	page, err := m.pageView(pageNumber)
	if err != nil {
		return nil, 0, err
	}
	return page, 0, nil
}

func (m *bitmapMemoryStore) FinishEdit(page []byte, tag uint32) error {
	return nil
}

func (m *bitmapMemoryStore) Allocate() (uint32, error) { return m.AllocateBitmapPage() }

func (m *bitmapMemoryStore) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
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

func (m *bitmapMemoryStore) DiscardPrivate(pageNumber uint32) error {
	m.discarded = append(m.discarded, pageNumber)
	return nil
}

func (m *bitmapMemoryStore) AllocateBitmapPage() (uint32, error) {
	if uint64(len(m.pages)) >= 1<<32 {
		return 0, invalid("test page space exhausted")
	}
	pageNumber := uint32(len(m.pages))
	m.pages = append(m.pages, [format.PageSize]byte{})
	return pageNumber, nil
}

func (m *bitmapMemoryStore) AllocationForbidden(pageNumber uint32) bool {
	return pageNumber == m.forbidden
}

// sealCurrent stamps the checksum of every private draft page (Rust
// MemoryStore::seal_current).
func (m *bitmapMemoryStore) sealCurrent() error {
	for index := 2; index < len(m.pages); index++ {
		page := m.pages[index][:]
		if bytes.Equal(page[:4], format.PageMagic[:]) && format.U64(page[format.HeaderBorn:]) == m.txn {
			if err := format.SealPageChecksum(page); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestLowestFreePageIsSelectedAcrossSparseSubtrees(t *testing.T) {
	m := newBitmapMemoryStore(2)
	root := uint32(0)
	limit := uint64(9_000_000)
	for _, bit := range []uint32{8_500_000, 64, 32_001, 3, 32_000} {
		if err := SetFree(m, &root, limit, bit, tree.NewRetiredPages()); err != nil {
			t.Fatal(err)
		}
	}
	var selected []uint32
	for {
		page, ok, err := TakeLowest(m, &root, limit, tree.NewRetiredPages())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		selected = append(selected, page)
	}
	want := []uint32{3, 64, 32_000, 32_001, 8_500_000}
	if len(selected) != len(want) {
		t.Fatalf("selected %v, want %v", selected, want)
	}
	for index := range want {
		if selected[index] != want[index] {
			t.Fatalf("selected %v, want %v", selected, want)
		}
	}
	if root != 0 {
		t.Fatalf("root after full drain is %d, want 0", root)
	}
	if len(m.discarded) == 0 {
		t.Fatal("full drain discarded no pages")
	}
}

func TestCommittedPathIsCopiedOnceAndChecksumChecked(t *testing.T) {
	m := newBitmapMemoryStore(2)
	root := uint32(0)
	if err := SetFree(m, &root, 100_000, 40_000, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	if err := m.sealCurrent(); err != nil {
		t.Fatal(err)
	}
	committed := make([][format.PageSize]byte, len(m.pages))
	copy(committed, m.pages)
	m.txn = 3

	retired := tree.NewRetiredPages()
	if err := SetFree(m, &root, 100_000, 40_001, retired); err != nil {
		t.Fatal(err)
	}
	if retired.Len() != 2 {
		t.Fatalf("cross-draft set_free retired %d pages, want 2", retired.Len())
	}
	for index := range committed {
		if committed[index] != m.pages[index] {
			t.Fatalf("committed page %d changed during the draft", index)
		}
	}

	second := tree.NewRetiredPages()
	if err := SetFree(m, &root, 100_000, 40_002, second); err != nil {
		t.Fatal(err)
	}
	if second.Len() != 0 {
		t.Fatalf("same-path set_free retired %v", second.Slice())
	}

	corruptStore := newBitmapMemoryStore(2)
	corruptRoot := uint32(0)
	if err := SetFree(corruptStore, &corruptRoot, 100, 50, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	if err := corruptStore.sealCurrent(); err != nil {
		t.Fatal(err)
	}
	corruptStore.pages[corruptRoot][100] ^= 1
	corruptStore.txn = 3
	if _, _, err := TakeLowest(corruptStore, &corruptRoot, 100, tree.NewRetiredPages()); err == nil {
		t.Fatal("corrupted committed bitmap body was accepted")
	}
}

func TestProtectedPageIsRejectedBeforeOverwrite(t *testing.T) {
	m := newBitmapMemoryStore(2)
	root := uint32(0)
	if err := SetFree(m, &root, 100, 50, tree.NewRetiredPages()); err != nil {
		t.Fatal(err)
	}
	m.forbidden = 50
	if _, _, err := TakeLowest(m, &root, 100, tree.NewRetiredPages()); err == nil {
		t.Fatal("protected page was allocated")
	}
}
