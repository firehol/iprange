// Copy-on-write free-page bitmap mutation (Rust free_bitmap.rs +
// free_bitmap/mutation.rs). This is the allocation core's free-page
// authority: a hierarchical bitmap of allocatable page numbers over the
// draft store, with COW descent that privatizes committed pages.

package bitmap

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// BitmapStore is the draft store surface the free bitmap needs beyond the
// tree Store: bitmap-page allocation and the protected-page predicate
// (Rust free_bitmap::BitmapStore).
type BitmapStore interface {
	tree.Store
	AllocateBitmapPage() (uint32, error)
	AllocationForbidden(pageNumber uint32) bool
}

type freeFrame struct {
	pageNumber uint32
	childIndex int
	level      uint16
}

type freeCursor struct {
	pageNumber uint32
	header     *Header
}

// SetFree marks one page free in the bitmap, growing the tree as needed
// (Rust free_bitmap::set_free).
func SetFree(store BitmapStore, root *uint32, limit uint64, bit uint32, retired *tree.RetiredPages) error {
	if err := requireFreeBit(limit, bit); err != nil {
		return err
	}
	required, err := RequiredLevel(limit)
	if err != nil {
		return err
	}
	if *root == 0 {
		pageNumber, err := newSubtree(store, required, bit)
		if err != nil {
			return err
		}
		*root = pageNumber
		return nil
	}
	if err := growRoot(store, root, required); err != nil {
		return err
	}
	cursor, err := touchCursor(store, *root, required, retired)
	if err != nil {
		return err
	}
	*root = cursor.pageNumber
	return insertFree(store, cursor, bit, retired)
}

func insertFree(store BitmapStore, cursor *freeCursor, bit uint32, retired *tree.RetiredPages) error {
	for cursor.header.Level > 0 {
		next, descended, err := descendForInsert(store, cursor, bit, retired)
		if err != nil {
			return err
		}
		if !descended {
			return nil
		}
		cursor = next
	}
	return markLeafFree(store, cursor, bit)
}

func descendForInsert(store BitmapStore, cursor *freeCursor, bit uint32, retired *tree.RetiredPages) (*freeCursor, bool, error) {
	index, err := ChildIndex(bit, cursor.header.Level)
	if err != nil {
		return nil, false, err
	}
	limit := store.PageLimit()
	var child uint32
	if err := store.Inspect(cursor.pageNumber, func(page []byte) error {
		c, err := CheckedBranchChild(page, cursor.header, index, limit)
		child = c
		return err
	}); err != nil {
		return nil, false, err
	}
	if child == 0 {
		subtree, err := newSubtree(store, cursor.header.Level-1, bit)
		if err != nil {
			return nil, false, err
		}
		if err := replaceChild(store, cursor.pageNumber, cursor.header, index, subtree); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	next, err := touchCursor(store, child, cursor.header.Level-1, retired)
	if err != nil {
		return nil, false, err
	}
	if next.pageNumber != child {
		if err := replaceChild(store, cursor.pageNumber, cursor.header, index, next.pageNumber); err != nil {
			return nil, false, err
		}
	}
	return next, true, nil
}

func replaceChild(store BitmapStore, pageNumber uint32, header *Header, index int, child uint32) error {
	return store.Update(pageNumber, func(page []byte) error {
		_, err := ReplaceBranchChild(page, header, index, child, child != 0)
		return err
	})
}

func markLeafFree(store BitmapStore, cursor *freeCursor, bit uint32) error {
	wordIndex := LeafWordIndex(bit)
	mask := uint64(1) << (uint64(bit) % 64)
	return store.Update(cursor.pageNumber, func(page []byte) error {
		word, err := LeafWord(page, wordIndex)
		if err != nil {
			return err
		}
		if word&mask != 0 {
			return corrupt("page is already free")
		}
		if err := SetLeafWord(page, wordIndex, word|mask); err != nil {
			return err
		}
		count := cursor.header.ItemCount
		if word == 0 {
			count++
		}
		format.PutU16(page[format.HeaderCount:], uint16(count))
		return nil
	})
}

// TakeLowest takes the lowest allocatable free page, mirroring Rust
// free_bitmap::take_lowest. Reports false when the bitmap is empty.
func TakeLowest(store BitmapStore, root *uint32, limit uint64, retired *tree.RetiredPages) (uint32, bool, error) {
	if *root == 0 {
		return 0, false, nil
	}
	selected, err := takeFromNonempty(store, root, limit, retired)
	if err != nil {
		return 0, false, err
	}
	return selected, true, nil
}

func takeFromNonempty(store BitmapStore, root *uint32, limit uint64, retired *tree.RetiredPages) (uint32, error) {
	required, err := RequiredLevel(limit)
	if err != nil {
		return 0, err
	}
	cursor, err := touchCursor(store, *root, required, retired)
	if err != nil {
		return 0, err
	}
	*root = cursor.pageNumber
	var path [MaxLevel + 1]freeFrame
	leaf, depth, base, err := descendLowest(store, cursor, retired, &path)
	if err != nil {
		return 0, err
	}
	selected, wordIndex, word, bitInWord, err := selectFreeLeaf(store, leaf, path[:depth], base, limit)
	if err != nil {
		return 0, err
	}
	nonempty, err := clearLeafBit(store, leaf, wordIndex, word, bitInWord)
	if err != nil {
		return 0, err
	}
	if !nonempty {
		if err := pruneEmptyPath(store, root, path[:depth], depth); err != nil {
			return 0, err
		}
	}
	return selected, nil
}

func descendLowest(store BitmapStore, cursor *freeCursor, retired *tree.RetiredPages, path *[MaxLevel + 1]freeFrame) (*freeCursor, int, uint64, error) {
	depth := 0
	base := uint64(0)
	for cursor.header.Level > 0 {
		var index int
		if err := store.Inspect(cursor.pageNumber, func(page []byte) error {
			i, found, err := FirstSummary(page, 0)
			if err != nil {
				return err
			}
			if !found {
				return corrupt("free summary is empty")
			}
			index = i
			return nil
		}); err != nil {
			return nil, 0, 0, err
		}
		path[depth] = freeFrame{pageNumber: cursor.pageNumber, childIndex: index, level: cursor.header.Level}
		offset, err := addChildBase(base, cursor.header.Level, index)
		if err != nil {
			return nil, 0, 0, err
		}
		base = offset
		limit := store.PageLimit()
		var child uint32
		if err := store.Inspect(cursor.pageNumber, func(page []byte) error {
			c, err := CheckedBranchChild(page, cursor.header, index, limit)
			child = c
			return err
		}); err != nil {
			return nil, 0, 0, err
		}
		if child == 0 {
			return nil, 0, 0, corrupt("free summary names an absent child")
		}
		next, err := touchCursor(store, child, cursor.header.Level-1, retired)
		if err != nil {
			return nil, 0, 0, err
		}
		if next.pageNumber != child {
			if err := replaceChild(store, cursor.pageNumber, cursor.header, index, next.pageNumber); err != nil {
				return nil, 0, 0, err
			}
		}
		cursor = next
		depth++
	}
	return cursor, depth, base, nil
}

func addChildBase(base uint64, level uint16, index int) (uint64, error) {
	coverage, err := Coverage(level - 1)
	if err != nil {
		return 0, err
	}
	return base + coverage*uint64(index), nil
}

func selectFreeLeaf(store BitmapStore, leaf *freeCursor, path []freeFrame, base uint64, limit uint64) (uint32, int, uint64, uint64, error) {
	var wordIndex int
	var word uint64
	if err := store.Inspect(leaf.pageNumber, func(page []byte) error {
		index, value, err := FirstLeafWord(page)
		if err != nil {
			return err
		}
		if value == 0 {
			return corrupt("free leaf is empty")
		}
		wordIndex = index
		word = value
		return nil
	}); err != nil {
		return 0, 0, 0, 0, err
	}
	bitInWord := uint64(trailingZeros(word))
	selected := base + uint64(wordIndex)*64 + bitInWord
	selected32, err := validateSelected(store, leaf.pageNumber, path, selected, limit)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return selected32, wordIndex, word, bitInWord, nil
}

func validateSelected(store BitmapStore, leafPage uint32, path []freeFrame, selected uint64, limit uint64) (uint32, error) {
	if selected < 2 || selected >= limit || selected >= 1<<32 {
		return 0, corrupt("free bit is outside allocatable bounds")
	}
	selected32 := uint32(selected)
	protects := store.AllocationForbidden(selected32)
	if !protects {
		for _, frame := range path {
			if frame.pageNumber == selected32 {
				protects = true
				break
			}
		}
	}
	if selected32 == leafPage || protects {
		return 0, corrupt("free bit names protected allocator state")
	}
	return selected32, nil
}

// clearLeafBit clears one bit of the private leaf and reports whether the
// leaf still has set bits. An emptied leaf is discarded (Rust
// take_from_nonempty + clear_leaf_bit).
func clearLeafBit(store BitmapStore, leaf *freeCursor, wordIndex int, word uint64, bitInWord uint64) (bool, error) {
	next := word &^ (uint64(1) << bitInWord)
	nonempty := false
	err := store.Update(leaf.pageNumber, func(page []byte) error {
		if err := SetLeafWord(page, wordIndex, next); err != nil {
			return err
		}
		count := leaf.header.ItemCount
		if next == 0 {
			count--
		}
		if count < 0 {
			return corrupt("free bitmap leaf count underflows")
		}
		format.PutU16(page[format.HeaderCount:], uint16(count))
		nonempty = count != 0
		return nil
	})
	if err != nil {
		return false, err
	}
	if !nonempty {
		if err := store.DiscardPrivate(leaf.pageNumber); err != nil {
			return false, err
		}
	}
	return nonempty, nil
}

func pruneEmptyPath(store BitmapStore, root *uint32, path []freeFrame, depth int) error {
	for depth > 0 {
		depth--
		nonempty, err := pruneParent(store, path[depth])
		if err != nil {
			return err
		}
		if nonempty {
			return nil
		}
	}
	*root = 0
	return nil
}

func pruneParent(store BitmapStore, frame freeFrame) (bool, error) {
	targetTxn := store.TargetTxn()
	nonempty := false
	err := store.Update(frame.pageNumber, func(page []byte) error {
		header, err := InspectHeader(page, targetTxn, KindFree, &frame.level)
		if err != nil {
			return err
		}
		count, err := ReplaceBranchChild(page, header, frame.childIndex, 0, false)
		if err != nil {
			return err
		}
		nonempty = count != 0
		return nil
	})
	if err != nil {
		return false, err
	}
	if nonempty {
		return true, nil
	}
	if err := store.DiscardPrivate(frame.pageNumber); err != nil {
		return false, err
	}
	return false, nil
}

// EnsureLevel grows the free bitmap root until it covers the page limit
// (Rust free_bitmap::ensure_level).
func EnsureLevel(store BitmapStore, root *uint32, limit uint64) error {
	if *root == 0 {
		return nil
	}
	for {
		effective := limit
		if store.PageLimit() > effective {
			effective = store.PageLimit()
		}
		required, err := RequiredLevel(effective)
		if err != nil {
			return err
		}
		before := store.PageLimit()
		if err := growRoot(store, root, required); err != nil {
			return err
		}
		if store.PageLimit() == before {
			return nil
		}
	}
}

// validateBody verifies the reserved tail, the first set entry, and the
// header count against the body of one committed free bitmap page (Rust
// free_bitmap.rs validate_body).
func validateBody(page []byte, header *Header) error {
	if header.Level == 0 {
		if !ReservedZero(page, header.Level) {
			return corrupt("free bitmap leaf is invalid")
		}
		if _, word, err := FirstLeafWord(page); err != nil {
			return err
		} else if word == 0 {
			return corrupt("free bitmap leaf is invalid")
		}
		count, err := NonzeroLeafWords(page)
		if err != nil {
			return err
		}
		if count != header.ItemCount {
			return corrupt("free bitmap leaf is invalid")
		}
		return nil
	}
	if !ReservedZero(page, header.Level) {
		return corrupt("free bitmap branch is invalid")
	}
	if _, found, err := FirstSummary(page, 0); err != nil {
		return err
	} else if !found {
		return corrupt("free bitmap branch is invalid")
	}
	count, err := NonzeroChildren(page)
	if err != nil {
		return err
	}
	if count != header.ItemCount {
		return corrupt("free bitmap branch is invalid")
	}
	return nil
}

func touchCursor(store BitmapStore, pageNumber uint32, expectedLevel uint16, retired *tree.RetiredPages) (*freeCursor, error) {
	targetTxn := store.TargetTxn()
	var header *Header
	private := false
	if err := store.Inspect(pageNumber, func(page []byte) error {
		born := format.U64(page[format.HeaderBorn:])
		h, err := InspectHeader(page, targetTxn, KindFree, &expectedLevel)
		if err != nil {
			return err
		}
		if born != targetTxn {
			if !format.PageChecksumValid(page) {
				return corrupt("free bitmap checksum is invalid")
			}
			if err := validateBody(page, h); err != nil {
				return err
			}
		}
		header = h
		private = born == targetTxn
		return nil
	}); err != nil {
		return nil, err
	}
	if private {
		return &freeCursor{pageNumber: pageNumber, header: header}, nil
	}
	privatePage, err := store.AllocateBitmapPage()
	if err != nil {
		return nil, err
	}
	if err := tree.CopyForCow(store, pageNumber, privatePage); err != nil {
		return nil, err
	}
	if err := retired.Push(pageNumber); err != nil {
		return nil, err
	}
	return &freeCursor{pageNumber: privatePage, header: header}, nil
}

func growRoot(store BitmapStore, root *uint32, required uint16) error {
	targetTxn := store.TargetTxn()
	var level uint16
	if err := store.Inspect(*root, func(page []byte) error {
		var err error
		h, err := InspectHeader(page, targetTxn, KindFree, nil)
		if err != nil {
			return err
		}
		level = h.Level
		return nil
	}); err != nil {
		return err
	}
	if level > required {
		return corrupt("free bitmap root level is too high")
	}
	for level < required {
		parent, err := store.AllocateBitmapPage()
		if err != nil {
			return err
		}
		child := *root
		nextLevel := level + 1
		if err := store.Update(parent, func(page []byte) error {
			Initialize(page, targetTxn, nextLevel, KindFree)
			_, err := ReplaceBranchChild(page, &Header{Level: nextLevel}, 0, child, true)
			return err
		}); err != nil {
			return err
		}
		*root = parent
		level = nextLevel
	}
	return nil
}

func newSubtree(store BitmapStore, level uint16, bit uint32) (uint32, error) {
	if level == 0 {
		pageNumber, err := store.AllocateBitmapPage()
		if err != nil {
			return 0, err
		}
		txn := store.TargetTxn()
		wordIndex := LeafWordIndex(bit)
		if err := store.Update(pageNumber, func(page []byte) error {
			Initialize(page, txn, 0, KindFree)
			if err := SetLeafWord(page, wordIndex, uint64(1)<<(uint64(bit)%64)); err != nil {
				return err
			}
			format.PutU16(page[format.HeaderCount:], 1)
			return nil
		}); err != nil {
			return 0, err
		}
		return pageNumber, nil
	}
	child, err := newSubtree(store, level-1, bit)
	if err != nil {
		return 0, err
	}
	pageNumber, err := store.AllocateBitmapPage()
	if err != nil {
		return 0, err
	}
	txn := store.TargetTxn()
	index, err := ChildIndex(bit, level)
	if err != nil {
		return 0, err
	}
	if err := store.Update(pageNumber, func(page []byte) error {
		Initialize(page, txn, level, KindFree)
		_, err := ReplaceBranchChild(page, &Header{Level: level}, index, child, true)
		return err
	}); err != nil {
		return 0, err
	}
	return pageNumber, nil
}

func requireFreeBit(limit uint64, bit uint32) error {
	if _, err := RequiredLevel(limit); err != nil {
		return err
	}
	if bit < 2 || uint64(bit) >= limit {
		return invalid("free page is outside the bitmap limit")
	}
	return nil
}
