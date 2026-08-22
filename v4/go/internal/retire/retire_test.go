// Retirement extent tests (Rust retirement_tests.rs).

package retire

import (
	"bytes"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

type memoryStore struct {
	txn       uint64
	pages     [][format.PageSize]byte
	discarded []uint32
}

func newMemoryStore(txn uint64) *memoryStore {
	return &memoryStore{txn: txn, pages: make([][format.PageSize]byte, 2)}
}

func (m *memoryStore) TargetTxn() uint64 { return m.txn }
func (m *memoryStore) PageLimit() uint64 { return uint64(len(m.pages)) }

func (m *memoryStore) pageView(pageNumber uint32) ([]byte, error) {
	if int(pageNumber) >= len(m.pages) {
		return nil, corrupt("test page is out of bounds")
	}
	return m.pages[pageNumber][:], nil
}

func (m *memoryStore) Inspect(pageNumber uint32) ([]byte, error) {
	return m.pageView(pageNumber)
}

func (m *memoryStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	page, err := m.pageView(pageNumber)
	if err != nil {
		return nil, 0, err
	}
	return page, 0, nil
}

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

func (m *memoryStore) DiscardPrivate(pageNumber uint32) error {
	m.discarded = append(m.discarded, pageNumber)
	return nil
}

// extents walks every retirement extent in key order (Rust extents()).
func extents(m *memoryStore, root uint32) ([][3]uint64, error) {
	var output [][3]uint64
	key := Key{Txn: 0, First: 0}
	for {
		extent, hasExtent, err := atOrAfter(m, root, key)
		if err != nil {
			return nil, err
		}
		if !hasExtent {
			break
		}
		output = append(output, [3]uint64{extent.Key.Txn, uint64(extent.Key.First), uint64(extent.Count)})
		first := extent.Key.First + 1
		if first == 0 {
			break
		}
		key = Key{Txn: extent.Key.Txn, First: first}
	}
	return output, nil
}

func TestBigEndianPortableRetirementExtentMatchesLiteralBytes(t *testing.T) {
	extent := Extent{Key: Key{Txn: 0x0807060504030201, First: 0x0c0b0a09}, Count: 0x100f0e0d}
	cell := encode(extent)
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	if !bytes.Equal(cell, want) {
		t.Fatalf("encoded extent % x, want % x", cell, want)
	}
	decoded, err := (codec{}).ReadLeaf(cell)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Key.Txn != extent.Key.Txn || decoded.Key.First != extent.Key.First || decoded.Count != extent.Count {
		t.Fatalf("decoded extent %#v, want %#v", decoded, extent)
	}
}

func TestArbitraryPageOrderCoalescesWithinEachTransaction(t *testing.T) {
	m := newMemoryStore(9)
	root := uint32(0)
	count := uint64(0)
	for _, page := range []uint32{10, 12, 11, 20, 22, 21, 9, 13} {
		retired, err := AddPage(m, &root, &count, 9, page)
		if err != nil {
			t.Fatal(err)
		}
		if retired.Len() != 0 {
			t.Fatalf("fresh-draft add_page retired %v", retired.Slice())
		}
	}
	got, err := extents(m, root)
	if err != nil {
		t.Fatal(err)
	}
	want := [][3]uint64{{9, 9, 5}, {9, 20, 3}}
	if len(got) != len(want) {
		t.Fatalf("extents %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("extents %v, want %v", got, want)
		}
	}
	if count != 2 {
		t.Fatalf("extent count = %d, want 2", count)
	}

	if retired, err := AddPage(m, &root, &count, 10, 14); err != nil {
		t.Fatal(err)
	} else if retired.Len() != 0 {
		t.Fatalf("add_page(txn10) retired %v", retired.Slice())
	}
	got, err = extents(m, root)
	if err != nil {
		t.Fatal(err)
	}
	want = [][3]uint64{{9, 9, 5}, {9, 20, 3}, {10, 14, 1}}
	if len(got) != len(want) || got[2] != want[2] {
		t.Fatalf("extents %v, want %v", got, want)
	}
	if count != 3 {
		t.Fatalf("extent count = %d, want 3", count)
	}
	if _, err := AddPage(m, &root, &count, 9, 10); err == nil {
		t.Fatal("duplicate page 10 within txn 9 was accepted")
	}
	if _, err := AddPage(m, &root, &count, 9, 20); err == nil {
		t.Fatal("duplicate page 20 within txn 9 was accepted")
	}
}

func TestFirstChangeOfACommittedTreeReportsOnlyItsOldPath(t *testing.T) {
	m := newMemoryStore(2)
	root := uint32(0)
	count := uint64(0)
	for page := uint32(2); page < 1000; page += 2 {
		if _, err := AddPage(m, &root, &count, 2, page); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot := root
	committed := make([][format.PageSize]byte, len(m.pages))
	copy(committed, m.pages)
	m.txn = 3

	retired, err := AddPage(m, &root, &count, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if root == oldRoot {
		t.Fatal("root was copied in place")
	}
	if retired.Len() != 2 {
		t.Fatalf("cross-draft add_page retired %d pages, want 2", retired.Len())
	}
	for index := range committed {
		if committed[index] != m.pages[index] {
			t.Fatalf("committed page %d changed during the draft", index)
		}
	}

	second, err := AddPage(m, &root, &count, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if second.Len() != 0 {
		t.Fatalf("same-path add_page retired %v", second.Slice())
	}
}

func TestReclamationSelectsOnlyCompleteOldestSafeTransactions(t *testing.T) {
	m := newMemoryStore(5)
	m.pages = make([][format.PageSize]byte, 100)
	root := uint32(0)
	count := uint64(0)
	for _, add := range [][2]uint64{{2, 10}, {2, 11}, {3, 20}, {3, 22}, {4, 30}} {
		if _, err := AddPage(m, &root, &count, add[0], uint32(add[1])); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := func() error { return nil }

	oldest := uint64(3)
	got, err := SelectReclamation(m, root, 4, &oldest, 10, 10, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	want := &Reclamation{Transactions: 2, Pages: 4, ThroughTxn: 3}
	if got == nil || *got != *want {
		t.Fatalf("reclamation %#v, want %#v", got, want)
	}

	got, err = SelectReclamation(m, root, 4, nil, 1, 10, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	want = &Reclamation{Transactions: 1, Pages: 2, ThroughTxn: 2}
	if got == nil || *got != *want {
		t.Fatalf("reclamation %#v, want %#v", got, want)
	}

	got, err = SelectReclamation(m, root, 4, nil, 10, 3, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	want = &Reclamation{Transactions: 1, Pages: 2, ThroughTxn: 2}
	if got == nil || *got != *want {
		t.Fatalf("reclamation %#v, want %#v", got, want)
	}

	if _, err := SelectReclamation(m, root, 4, nil, 10, 1, checkpoint); err == nil {
		t.Fatal("too-small reclamation limit was accepted")
	} else if fe, ok := err.(*format.Error); !ok || fe.Code != format.CodeWorkLimitTooSmall {
		t.Fatalf("too-small limit error = %v", err)
	}
}
