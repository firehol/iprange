// Forward-cursor tests (Rust fixed_tree/cursor.rs Cursor::open + next,
// forward subset): ascending order across leaf boundaries, the consuming
// variant's page release into the private stack, and the malformed-tree
// rejections.

package tree

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// buildU32Tree inserts keys 0..count-1 into one fresh tree (ascending
// cell order regardless of insertion order) and returns the root.
func buildU32Tree(t *testing.T, m *memoryStore, count int) uint32 {
	t.Helper()
	root := uint32(0)
	for key := count - 1; key >= 0; key-- {
		retired := NewRetiredPages()
		changed, err := Insert(u32Codec{}, m, &root, u32Record(uint32(key), uint32(key+10)), retired)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Fatalf("key %d was not reported as inserted", key)
		}
		if retired.Len() != 0 {
			t.Fatalf("fresh insert retired %v", retired.Slice())
		}
	}
	if root == 0 {
		t.Fatal("tree root is empty")
	}
	return root
}

// TestForwardCursorReadsAscendingAcrossLeafBoundaries pins the cursor's
// contract: every record arrives exactly once, in ascending key order,
// with the decoded leaf cell (Rust Cursor::next forward).
func TestForwardCursorReadsAscendingAcrossLeafBoundaries(t *testing.T) {
	m := newMemoryStore()
	root := buildU32Tree(t, m, 1000)

	cursor, err := NewForwardCursor(u32Codec{}, m, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Finished() {
		t.Fatal("fresh cursor over a populated tree reports finished")
	}
	seen := 0
	for {
		visited := false
		err = cursor.Next(func(cell []byte, header *Header, pageNumber uint32, index int) error {
			visited = true
			leaf, err := u32Codec{}.ReadLeaf(cell)
			if err != nil {
				return err
			}
			got := leaf.(u32Leaf)
			if got.key != uint32(seen) || got.value != uint32(seen+10) {
				t.Fatalf("record %d = (%d, %d), want (%d, %d)", seen, got.key, got.value, seen, seen+10)
			}
			if header == nil || header.Level != 0 {
				t.Fatalf("record %d header is not a leaf header", seen)
			}
			if pageNumber < 2 || index < 0 {
				t.Fatalf("record %d has an invalid page/index (%d, %d)", seen, pageNumber, index)
			}
			seen++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !visited {
			break
		}
	}
	if seen != 1000 {
		t.Fatalf("cursor visited %d records, want 1000", seen)
	}
	if !cursor.Finished() {
		t.Fatal("cursor is not finished after the last record")
	}
	// A finished cursor yields nothing more.
	err = cursor.Next(func(cell []byte, header *Header, pageNumber uint32, index int) error {
		t.Fatal("finished cursor yielded a record")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The non-consuming cursor released nothing.
	if len(m.discarded) != 0 {
		t.Fatalf("non-consuming cursor discarded %v", m.discarded)
	}
}

// TestForwardCursorConsumeReleasesEveryPassedPage pins the consuming
// cursor: after the full walk every tree page sits on the private stack
// (Rust Cursor::next_consuming), and shared walks release nothing.
func TestForwardCursorConsumeReleasesEveryPassedPage(t *testing.T) {
	m := newMemoryStore()
	root := buildU32Tree(t, m, 1000)
	for page := uint32(2); page < uint32(len(m.pages)); page++ {
		_m := m.pages[page]
		_ = _m
	}

	cursor, err := NewForwardCursor(u32Codec{}, m, root, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for {
		visited := false
		err = cursor.Next(func(cell []byte, header *Header, pageNumber uint32, index int) error {
			visited = true
			seen++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !visited {
			break
		}
	}
	if seen != 1000 {
		t.Fatalf("cursor visited %d records, want 1000", seen)
	}
	allocated := len(m.pages) - 2
	if len(m.discarded) != allocated {
		t.Fatalf("consuming cursor discarded %d pages, want %d", len(m.discarded), allocated)
	}
	discardedSet := make(map[uint32]bool, len(m.discarded))
	for _, page := range m.discarded {
		if discardedSet[page] {
			t.Fatalf("page %d discarded twice", page)
		}
		discardedSet[page] = true
	}
	for page := uint32(2); page < uint32(len(m.pages)); page++ {
		if !discardedSet[page] {
			t.Fatalf("page %d was never discarded", page)
		}
	}
}

// TestForwardCursorConsumeRejectsCommittedPages pins the consuming
// cursor's ownership rule: a consuming open over a tree that was not
// born in the selected transaction fails closed (Rust new_consuming
// require_owned).
func TestForwardCursorConsumeRejectsCommittedPages(t *testing.T) {
	m := newMemoryStore()
	m.targetTxn = 1
	root := buildU32Tree(t, m, 50)

	// The tree is committed (born txn 1); a cursor consuming for txn 2
	// must refuse the committed pages.
	other := newMemoryStore()
	other.targetTxn = 2
	other.pages = append(other.pages, m.pages[2:]...)
	if _, err := NewForwardCursor(u32Codec{}, other, root, true); err == nil {
		t.Fatal("consuming cursor over committed pages did not fail")
	}
	// The shared cursor over the same tree works.
	cursor, err := NewForwardCursor(u32Codec{}, m, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Finished() {
		t.Fatal("shared cursor over the committed tree reports finished")
	}
}

// TestForwardCursorRejectsMalformedTrees pins the open-time shape
// checks: a root outside the source page bounds is refused before any
// descent, and a root page with the wrong geometry fails the parse.
func TestForwardCursorRejectsMalformedTrees(t *testing.T) {
	m := newMemoryStore()
	if _, err := NewForwardCursor(u32Codec{}, m, 1, false); err == nil {
		t.Fatal("root below page 2 accepted")
	}
	if _, err := NewForwardCursor(u32Codec{}, m, uint32(len(m.pages)), false); err == nil {
		t.Fatal("root at the page limit accepted")
	}
	// A data page whose header names the range leaf type but whose
	// geometry does not match the u32 codec is a malformed tree.
	root := buildU32Tree(t, m, 1)
	m.pages[root][0] = 0 // clobber the page magic
	if _, err := NewForwardCursor(u32Codec{}, m, root, false); err == nil {
		t.Fatal("malformed root page accepted")
	}
}

// TestForwardCursorEmptyTreeIsFinished pins the empty-root shortcut
// (Rust Cursor::open over root 0).
func TestForwardCursorEmptyTreeIsFinished(t *testing.T) {
	m := newMemoryStore()
	cursor, err := NewForwardCursor(u32Codec{}, m, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cursor.Finished() {
		t.Fatal("empty tree cursor is not finished")
	}
	if err := cursor.Next(func(cell []byte, header *Header, pageNumber uint32, index int) error {
		t.Fatal("empty cursor yielded a record")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestForwardCursorCellHeaderRoundTrip pins the borrowed cell contract:
// the read callback sees the live page header and cell, and the cell
// decodes through the codec (no owned page anywhere).
func TestForwardCursorCellHeaderRoundTrip(t *testing.T) {
	m := newMemoryStore()
	root := buildU32Tree(t, m, 3)
	cursor, err := NewForwardCursor(u32Codec{}, m, root, false)
	if err != nil {
		t.Fatal(err)
	}
	first := true
	err = cursor.Next(func(cell []byte, header *Header, pageNumber uint32, index int) error {
		if !first {
			return nil
		}
		first = false
		leaf, err := u32Codec{}.ReadLeaf(cell)
		if err != nil {
			return err
		}
		if got := leaf.(u32Leaf); got.key != 0 || got.value != 10 {
			t.Fatalf("first record = %#v, want {0 10}", got)
		}
		if format.U32(cell) != 0 {
			t.Fatalf("cell first word = %d, want 0", format.U32(cell))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
