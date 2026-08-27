// Page-local insertion and split propagation (Rust fixed_tree/insert.rs).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// LeafTarget identifies one leaf edit position after a COW descent.
type LeafTarget struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Index      int
	Exists     bool
}

// RequireLeaf validates one leaf cell against the codec (Rust
// require_leaf).
func RequireLeaf[T any](codec Codec[T], leafCell []byte) error {
	if err := requireCodec(codec); err != nil {
		return err
	}
	if len(leafCell) == 0 || len(leafCell) > MaxLeafSize(codec) {
		return invalid("wrong B+tree leaf size")
	}
	work.LeafValidation(1)
	_, err := codec.ReadLeaf(leafCell)
	return err
}

// RequireReplacement validates a 2-3 cell replacement (Rust
// require_replacement).
func RequireReplacement[T any](codec Codec[T], key Key, cells [][]byte) error {
	if len(cells) < 2 || len(cells) > 3 {
		return invalid("B+tree leaf replacement requires two or three cells")
	}
	previous := Key{}
	havePrevious := false
	for _, cell := range cells {
		if err := RequireLeaf(codec, cell); err != nil {
			return err
		}
		current, err := codec.ReadKey(cell, 0)
		if err != nil {
			return err
		}
		if havePrevious && !previous.Less(current) {
			return invalid("B+tree replacement keys are not increasing")
		}
		previous = current
		havePrevious = true
	}
	first, err := codec.ReadKey(cells[0], 0)
	if err != nil {
		return err
	}
	if !first.Equal(key) {
		return invalid("B+tree replacement changed its first key")
	}
	return nil
}

// Insert inserts one leaf cell into the tree rooted at root and reports
// whether the tree changed (Rust fixed_tree::insert). The caller retires
// the returned pages after the operation.
func Insert[T any](codec Codec[T], store Store, root *uint32, leafCell []byte, retired RetiredPages) (RetiredPages, bool, error) {
	if err := RequireLeaf(codec, leafCell); err != nil {
		return RetiredPages{}, false, err
	}
	if *root == 0 {
		pageNumber, err := NewLeaf(codec, store, leafCell)
		if err != nil {
			return RetiredPages{}, false, err
		}
		*root = pageNumber
		return retired, true, nil
	}
	key, err := codec.ReadKey(leafCell, 0)
	if err != nil {
		return RetiredPages{}, false, err
	}
	leaf, retired, err := PrivatePath(codec, store, root, key, retired)
	if err != nil {
		return RetiredPages{}, false, err
	}
	target := LeafTarget{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: leaf.Index, Exists: leaf.Exists}
	changed, err := EditLeaf(codec, store, root, leafCell, &target)
	return retired, changed, err
}

// ReplaceLeafWith replaces one existing leaf cell with 2-3 cells and
// propagates the resulting split (Rust replace_leaf_with).
func ReplaceLeafWith[T any](codec Codec[T], store Store, root *uint32, key Key, cells [][]byte, retired RetiredPages) (RetiredPages, error) {
	if err := RequireReplacement(codec, key, cells); err != nil {
		return RetiredPages{}, err
	}
	leaf, retired, err := PrivatePath(codec, store, root, key, retired)
	if err != nil {
		return RetiredPages{}, err
	}
	target := LeafTarget{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: leaf.Index, Exists: leaf.Exists}
	if !target.Exists {
		return RetiredPages{}, corrupt("B+tree replacement key is missing")
	}
	return retired, replaceTarget(codec, store, root, target, cells)
}

func replaceTarget[T any](codec Codec[T], store Store, root *uint32, target LeafTarget, cells [][]byte) error {
	edit := Replacement{index: target.Index, cells: cells}
	split, ok, err := applyReplacement(codec, store, target.PageNumber, &target.Header, edit)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if split.level != 0 {
		return corrupt("B+tree leaf replacement changed level")
	}
	return propagateSplit(codec, store, root, &target.Path, target.PageNumber, split.leftFirst, split.rightPage, split.rightFirst, 0)
}

// NewLeaf builds a fresh single-record leaf page (Rust new_leaf).
func NewLeaf[T any](codec Codec[T], store Store, cell []byte) (uint32, error) {
	pageNumber, err := store.Allocate()
	if err != nil {
		return 0, err
	}
	txn := store.TargetTxn()
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return 0, err
	}
	b := format.NewSlottedBuilder(page, codec.LeafType(), txn, 0, codec.Aux())
	if err := b.Push(page, cell); err != nil {
		return 0, corrupt("B+tree leaf build failed: " + err.Error())
	}
	if err := b.Finish(page); err != nil {
		return 0, err
	}
	return pageNumber, store.RestoreDirty(pageNumber, tag)
}

// EditLeaf applies one leaf edit, splitting the page when the record does
// not fit (Rust edit_leaf). Reports whether the key was newly inserted.
func EditLeaf[T any](codec Codec[T], store Store, root *uint32, leafCell []byte, target *LeafTarget) (bool, error) {
	edit := Edit{index: target.Index, replace: target.Exists, cell: leafCell}
	page, err := store.Inspect(target.PageNumber)
	if err != nil {
		return false, err
	}
	fits, err := editFits(codec, page, &target.Header, edit)
	if err != nil {
		return false, err
	}
	if !fits {
		if err := splitLeaf(codec, store, root, target, edit); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := applyLeafEdit(codec, store, target.PageNumber, &target.Header, edit); err != nil {
		return false, err
	}
	if target.Index == 0 {
		key, err := codec.ReadKey(edit.cell, 0)
		if err != nil {
			return false, err
		}
		if err := PropagateFirst(codec, store, root, &target.Path, key); err != nil {
			return false, err
		}
	}
	return !target.Exists, nil
}

func applyLeafEdit[T any](codec Codec[T], store Store, pageNumber uint32, header *Header, edit Edit) error {
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	if err := applyEdit(codec, page, header, edit); err != nil {
		return err
	}
	return store.RestoreDirty(pageNumber, tag)
}

func applyEdit[T any](codec Codec[T], page []byte, header *Header, edit Edit) error {
	var changed bool
	var err error
	if edit.replace {
		oldCell, err := codecCell(codec, page, header, edit.index)
		if err != nil {
			return err
		}
		changed, err = format.SlottedReplace(page, header, edit.index, len(oldCell), edit.cell)
	} else {
		changed, err = format.SlottedInsert(page, header, edit.index, edit.cell)
	}
	if err != nil {
		return err
	}
	if !changed {
		return corrupt("B+tree edit no longer fits")
	}
	return nil
}

func splitLeaf[T any](codec Codec[T], store Store, root *uint32, target *LeafTarget, edit Edit) error {
	total := edit.total(int(target.Header.ItemCount))
	page, err := store.Inspect(target.PageNumber)
	if err != nil {
		return err
	}
	middle, err := splitIndex(codec, page, &target.Header, edit)
	if err != nil {
		return err
	}
	return splitLeafAt(codec, store, root, target, edit, middle, total)
}

func splitLeafAt[T any](codec Codec[T], store Store, root *uint32, target *LeafTarget, edit Edit, middle, total int) error {
	rightPage, err := store.Allocate()
	if err != nil {
		return err
	}
	src, dst, tag, err := store.CopyPage(target.PageNumber, rightPage)
	if err != nil {
		return err
	}
	if err := buildEdit(codec, src, &target.Header, edit, middle, total, dst); err != nil {
		return err
	}
	if err := store.RestoreDirty(rightPage, tag); err != nil {
		return err
	}
	if err := keepLeftEdit(codec, store, target.PageNumber, &target.Header, edit, middle); err != nil {
		return err
	}
	leftFirst, err := FirstKey(codec, store, target.PageNumber, 0)
	if err != nil {
		return err
	}
	rightFirst, err := FirstKey(codec, store, rightPage, 0)
	if err != nil {
		return err
	}
	if err := propagateSplit(codec, store, root, &target.Path, target.PageNumber, leftFirst, rightPage, rightFirst, 0); err != nil {
		return err
	}
	work.PageSplit(1)
	return nil
}

// FirstKey reads the first key of one page (Rust first_key).
func FirstKey[T any](codec Codec[T], store Store, pageNumber uint32, level uint16) (Key, error) {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return Key{}, err
	}
	header, err := parse(codec, page, targetTxn, level, true)
	if err != nil {
		return Key{}, err
	}
	return keyAt(codec, page, &header, 0)
}

func keepLeftEdit[T any](codec Codec[T], store Store, pageNumber uint32, header *Header, edit Edit, middle int) error {
	txn := store.TargetTxn()
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	editInLeft := edit.index < middle
	keep := middle
	if editInLeft && !edit.replace {
		keep--
	}
	if keep == 0 {
		pageType := codec.LeafType()
		if header.Level != 0 {
			pageType = codec.BranchType()
		}
		b := format.NewSlottedBuilder(page, pageType, txn, header.Level, codec.Aux())
		if err := b.Push(page, edit.cell); err != nil {
			return corrupt("B+tree leaf build failed: " + err.Error())
		}
		if err := b.Finish(page); err != nil {
			return err
		}
		return store.RestoreDirty(pageNumber, tag)
	}
	left, err := truncate(codec, page, header, keep)
	if err != nil {
		return err
	}
	if editInLeft {
		if err := applyEdit(codec, page, &left, edit); err != nil {
			return err
		}
	}
	return store.RestoreDirty(pageNumber, tag)
}

type branchSplit struct {
	rightPage  uint32
	rightFirst Key
	leftFirst  Key
	level      uint16
}

func propagateSplit[T any](codec Codec[T], store Store, root *uint32, path *Path, leftPage uint32, leftFirst Key, rightPage uint32, rightFirst Key, childLevel uint16) error {
	return propagateSplitFrom(codec, store, root, path, path.Depth(), leftPage, leftFirst, rightPage, rightFirst, childLevel)
}

func propagateSplitFrom[T any](codec Codec[T], store Store, root *uint32, path *Path, depth int, leftPage uint32, leftFirst Key, rightPage uint32, rightFirst Key, childLevel uint16) error {
	for {
		if depth == 0 {
			pageNumber, err := newRoot(codec, store, leftPage, leftFirst, rightPage, rightFirst, childLevel+1)
			if err != nil {
				return err
			}
			*root = pageNumber
			return nil
		}
		depth--
		frame := path.Frame(depth)
		split, ok, err := insertBranch(codec, store, frame, leftFirst, rightPage, rightFirst)
		if err != nil {
			return err
		}
		if !ok {
			if frame.Index == 0 {
				if err := propagateFirstFrom(codec, store, root, path, depth, leftFirst); err != nil {
					return err
				}
			}
			return nil
		}
		leftPage = frame.PageNumber
		leftFirst = split.leftFirst
		rightPage = split.rightPage
		rightFirst = split.rightFirst
		childLevel = split.level
	}
}

func insertBranch[T any](codec Codec[T], store Store, frame Frame, leftFirst Key, rightPage uint32, rightFirst Key) (branchSplit, bool, error) {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(frame.PageNumber)
	if err != nil {
		return branchSplit{}, false, err
	}
	header, err := parse(codec, page, targetTxn, 0, false)
	if err != nil {
		return branchSplit{}, false, err
	}
	leftChild, err := branchChild(codec, page, &header, frame.Index, store.PageLimit())
	if err != nil {
		return branchSplit{}, false, err
	}
	var left, right CellBuf
	if err := newBranchCell(codec, leftFirst, leftChild, &left); err != nil {
		return branchSplit{}, false, err
	}
	if err := newBranchCell(codec, rightFirst, rightPage, &right); err != nil {
		return branchSplit{}, false, err
	}
	edit := Replacement{index: frame.Index, cells: [][]byte{left.Bytes(), right.Bytes()}}
	split, ok, err := applyReplacement(codec, store, frame.PageNumber, &header, edit)
	if err != nil {
		return branchSplit{}, false, err
	}
	work.FirstFenceUpdate(1)
	return split, ok, nil
}

func applyReplacement[T any](codec Codec[T], store Store, pageNumber uint32, header *Header, edit Replacement) (branchSplit, bool, error) {
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return branchSplit{}, false, err
	}
	fits, err := replacementFits(codec, page, header, edit)
	if err != nil {
		return branchSplit{}, false, err
	}
	if fits {
		page, tag, err := store.Update(pageNumber)
		if err != nil {
			return branchSplit{}, false, err
		}
		if err := applyCells(codec, page, header, edit.index, edit.cells); err != nil {
			return branchSplit{}, false, err
		}
		return branchSplit{}, false, store.RestoreDirty(pageNumber, tag)
	}
	return splitReplacement(codec, store, pageNumber, header, edit)
}

func applyCells[T any](codec Codec[T], page []byte, header *Header, index int, cells [][]byte) error {
	oldCell, err := codecCell(codec, page, header, index)
	if err != nil {
		return err
	}
	ok, err := format.SlottedReplace(page, header, index, len(oldCell), cells[0])
	if err != nil {
		return err
	}
	if !ok {
		return corrupt("B+tree replacement no longer fits")
	}
	for offset, cell := range cells[1:] {
		current, err := parse(codec, page, ^uint64(0), header.Level, true)
		if err != nil {
			return err
		}
		ok, err := format.SlottedInsert(page, &current, index+offset+1, cell)
		if err != nil {
			return err
		}
		if !ok {
			return corrupt("B+tree replacement insertion no longer fits")
		}
	}
	return nil
}

func splitReplacement[T any](codec Codec[T], store Store, pageNumber uint32, header *Header, edit Replacement) (branchSplit, bool, error) {
	total := edit.total(int(header.ItemCount))
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return branchSplit{}, false, err
	}
	middle, err := replacementSplitIndex(codec, page, header, edit)
	if err != nil {
		return branchSplit{}, false, err
	}
	rightPage, err := store.Allocate()
	if err != nil {
		return branchSplit{}, false, err
	}
	src, dst, tag, err := store.CopyPage(pageNumber, rightPage)
	if err != nil {
		return branchSplit{}, false, err
	}
	if err := buildReplacement(codec, src, header, edit, middle, total, dst); err != nil {
		return branchSplit{}, false, err
	}
	if err := store.RestoreDirty(rightPage, tag); err != nil {
		return branchSplit{}, false, err
	}
	if err := keepLeftReplacement(codec, store, pageNumber, header, edit, middle); err != nil {
		return branchSplit{}, false, err
	}
	work.PageSplit(1)
	rightFirst, err := FirstKey(codec, store, rightPage, header.Level)
	if err != nil {
		return branchSplit{}, false, err
	}
	leftFirst, err := FirstKey(codec, store, pageNumber, header.Level)
	if err != nil {
		return branchSplit{}, false, err
	}
	return branchSplit{rightPage: rightPage, rightFirst: rightFirst, leftFirst: leftFirst, level: header.Level}, true, nil
}

func keepLeftReplacement[T any](codec Codec[T], store Store, pageNumber uint32, header *Header, edit Replacement, middle int) error {
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	if middle <= edit.index {
		_, err := truncate(codec, page, header, middle)
		if err != nil {
			return err
		}
		return store.RestoreDirty(pageNumber, tag)
	}
	if middle < edit.index+len(edit.cells) {
		left, err := truncate(codec, page, header, edit.index+1)
		if err != nil {
			return err
		}
		if err := applyCells(codec, page, &left, edit.index, edit.cells[:middle-edit.index]); err != nil {
			return err
		}
		return store.RestoreDirty(pageNumber, tag)
	}
	keep := middle - (len(edit.cells) - 1)
	left, err := truncate(codec, page, header, keep)
	if err != nil {
		return err
	}
	if err := applyCells(codec, page, &left, edit.index, edit.cells); err != nil {
		return err
	}
	return store.RestoreDirty(pageNumber, tag)
}

func newRoot[T any](codec Codec[T], store Store, leftPage uint32, leftFirst Key, rightPage uint32, rightFirst Key, level uint16) (uint32, error) {
	if level > format.MaxTreeLevel {
		return 0, invalid("B+tree height limit reached")
	}
	pageNumber, err := store.Allocate()
	if err != nil {
		return 0, err
	}
	var left, right CellBuf
	if err := newBranchCell(codec, leftFirst, leftPage, &left); err != nil {
		return 0, err
	}
	if err := newBranchCell(codec, rightFirst, rightPage, &right); err != nil {
		return 0, err
	}
	txn := store.TargetTxn()
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return 0, err
	}
	b := format.NewSlottedBuilder(page, codec.BranchType(), txn, level, codec.Aux())
	if err := b.Push(page, left.Bytes()); err != nil {
		return 0, corrupt("B+tree branch build failed: " + err.Error())
	}
	if err := b.Push(page, right.Bytes()); err != nil {
		return 0, corrupt("B+tree branch build failed: " + err.Error())
	}
	if err := b.Finish(page); err != nil {
		return 0, err
	}
	return pageNumber, store.RestoreDirty(pageNumber, tag)
}

// PropagateFirst updates the ancestor first-key fences after an index-0
// leaf change (Rust propagate_first).
func PropagateFirst[T any](codec Codec[T], store Store, root *uint32, path *Path, key Key) error {
	return propagateFirstFrom(codec, store, root, path, path.Depth(), key)
}

func propagateFirstFrom[T any](codec Codec[T], store Store, root *uint32, path *Path, depth int, key Key) error {
	for depth > 0 {
		depth--
		frame := path.Frame(depth)
		split, ok, err := replaceFirstBranch(codec, store, frame, key)
		if err != nil {
			return err
		}
		if ok {
			return propagateSplitFrom(codec, store, root, path, depth, frame.PageNumber, split.leftFirst, split.rightPage, split.rightFirst, split.level)
		}
		if frame.Index != 0 {
			break
		}
	}
	return nil
}

func replaceFirstBranch[T any](codec Codec[T], store Store, frame Frame, key Key) (branchSplit, bool, error) {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(frame.PageNumber)
	if err != nil {
		return branchSplit{}, false, err
	}
	header, err := parse(codec, page, targetTxn, 0, false)
	if err != nil {
		return branchSplit{}, false, err
	}
	child, err := branchChild(codec, page, &header, frame.Index, store.PageLimit())
	if err != nil {
		return branchSplit{}, false, err
	}
	var replacement CellBuf
	if err := newBranchCell(codec, key, child, &replacement); err != nil {
		return branchSplit{}, false, err
	}
	edit := Replacement{index: frame.Index, cells: [][]byte{replacement.Bytes()}}
	split, ok, err := applyReplacement(codec, store, frame.PageNumber, &header, edit)
	if err != nil {
		return branchSplit{}, false, err
	}
	work.FirstFenceUpdate(1)
	return split, ok, nil
}

// SplitLeafAtEdge splits the target private leaf, keeping the side that
// holds the new edge cell (Rust split_leaf_at_edge). The edit is an
// insertion at the first or last position of the leaf.
func SplitLeafAtEdge[T any](codec Codec[T], store Store, root *uint32, target *LeafTarget, leafCell []byte, edge Edge) error {
	edit := Edit{index: 0, replace: false, cell: leafCell}
	total := int(target.Header.ItemCount) + 1
	middle := 1
	if edge == EdgeLast {
		edit.index = total - 1
		middle = total - 1
	}
	return splitLeafAt(codec, store, root, target, edit, middle, total)
}

// locatePrivatePosition re-descends the private tree and returns the leaf
// position of key without retiring any page (Rust locate_private_position).
func locatePrivatePosition[T any](codec Codec[T], store Store, root *uint32, key Key) (PrivatePosition, error) {
	leaf, retired, err := PrivatePath(codec, store, root, key, RetiredPages{})
	if err != nil {
		return PrivatePosition{}, err
	}
	if retired.Len() != 0 {
		return PrivatePosition{}, corrupt("private B+tree position retired a page")
	}
	return PrivatePosition{Path: leaf.Path, PageNumber: leaf.PageNumber}, nil
}

// LeafU64Mutation is the outcome of one leaf u64 field decision (Rust
// LeafU64Mutation).
type LeafU64Mutation struct {
	// Replace names the new u64 field value written in place.
	Replace uint64
	// DoReplace selects Replace; Delete removes the whole record.
	DoReplace bool
}

// existingLeaf is the selector output of MutateLeafU64: the record
// position inside its private leaf plus the decoded value.
type existingLeaf[T any] struct {
	position existingLeafPosition
	value    T
}

// existingLeafPosition locates one leaf cell inside its page: the logical
// index, the physical offset of the cell, and the cell length.
type existingLeafPosition struct {
	index   int
	offset  int
	cellLen int
}

// MutateLeafU64 reads one existing leaf record, runs decide, and either
// writes one u64 field in place at fieldOffset or deletes the record
// (Rust fixed_tree::mutate_leaf_u64). The key must exist; the returned
// value is the decoded leaf (Rust C::Leaf). The field write happens on
// the private leaf page at the exact cell offset, so variable-length
// records are handled by construction. The descent is closure-free: the
// leaf selection runs after privateDescent on the descent's page view, so
// no address-taken stack value crosses an indirect call.
func MutateLeafU64[T any](codec Codec[T], store Store, root *uint32, key Key, fieldOffset int, retired RetiredPages, decide func(T) (LeafU64Mutation, error)) (RetiredPages, T, error) {
	var zero T
	leaf, retired, err := privatePathSelect(codec, store, root, key, retired)
	if err != nil {
		return RetiredPages{}, zero, err
	}
	header := leaf.Header
	found, err := existingLeafAt(codec, leaf.Page, &header, key)
	if err != nil {
		return RetiredPages{}, zero, err
	}
	mutation, err := decide(found.value)
	if err != nil {
		return RetiredPages{}, zero, err
	}
	if mutation.DoReplace {
		if fieldOffset < 0 || fieldOffset+8 > found.position.cellLen {
			return RetiredPages{}, zero, corrupt("B+tree update field is outside its leaf")
		}
		page, tag, err := store.Update(leaf.PageNumber)
		if err != nil {
			return RetiredPages{}, zero, err
		}
		format.PutU64(page[found.position.offset+fieldOffset:], mutation.Replace)
		if err := store.RestoreDirty(leaf.PageNumber, tag); err != nil {
			return RetiredPages{}, zero, err
		}
		return retired, found.value, nil
	}
	if err := DeleteTarget(codec, store, root, &Target{
		Path:       leaf.Path,
		PageNumber: leaf.PageNumber,
		Header:     leaf.Header,
		Index:      found.position.index,
	}); err != nil {
		return RetiredPages{}, zero, err
	}
	return retired, found.value, nil
}

// existingLeafAt locates one existing leaf record inside a leaf page view
// and decodes it: the logical index, the physical cell offset, the cell
// length, and the decoded value (Rust mutate_leaf_u64's leaf selection).
func existingLeafAt[T any](codec Codec[T], page []byte, header *Header, key Key) (existingLeaf[T], error) {
	index, exists, err := lowerBound(codec, page, header, key, true)
	if err != nil {
		return existingLeaf[T]{}, err
	}
	if !exists {
		return existingLeaf[T]{}, corrupt("B+tree update key is missing")
	}
	cell, err := codecCell(codec, page, header, index)
	if err != nil {
		return existingLeaf[T]{}, err
	}
	value, err := codec.ReadLeaf(cell)
	if err != nil {
		return existingLeaf[T]{}, err
	}
	offset, ok := format.SlottedCellOffset(page, header, index)
	if !ok {
		return existingLeaf[T]{}, corrupt("B+tree update cell offset is invalid")
	}
	return existingLeaf[T]{
		position: existingLeafPosition{index: index, offset: offset, cellLen: len(cell)},
		value:    value,
	}, nil
}
