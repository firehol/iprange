// Copy-on-write used-page bitmap mutation (Rust used_bitmap.rs +
// used_bitmap/mutation.rs): the sparse dictionary-ID bitmaps (feed,
// membership, structure) over the same hierarchical layout as the free
// bitmap. A set bit means the ID is in use; allocation takes the lowest
// zero bit, so summaries advertise candidate zeros rather than nonzero
// children.

package bitmap

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// usedFrame records one level of an edit descent (Rust EditPath::Frame).
type usedFrame struct {
	pageNumber uint32
	childIndex int
	level      uint16
	childBase  uint64
}

// editPath is the fixed-depth descent stack of a used-bitmap edit (Rust
// EditPath).
type editPath struct {
	frames [MaxLevel + 1]usedFrame
	depth  int
}

func (p *editPath) push(frame usedFrame) error {
	if p.depth >= len(p.frames) {
		return corrupt("used bitmap path exceeds maximum height")
	}
	p.frames[p.depth] = frame
	p.depth++
	return nil
}

type usedCursor struct {
	pageNumber uint32
	header     Header
	base       uint64
}

type branchStep struct {
	index     int
	child     uint32
	childBase uint64
}

type setSpec struct {
	limit         uint64
	kind          Kind
	bit           uint32
	candidateHint *bool
}

// AllocateLowestID returns the lowest unused ID of a namespace: a hole
// from the used bitmap, or (for a dense namespace) the new id at the limit
// (Rust used_bitmap::allocate_lowest_id).
func AllocateLowestID(store tree.RetiringStore, root *uint32, limit *uint64, liveCount uint64, kind Kind, exhausted func() error) (uint32, error) {
	if *limit == 0 {
		return 0, corrupt("ID namespace limit is zero")
	}
	if liveCount > *limit-1 {
		return 0, corrupt("ID namespace count exceeds its limit")
	}
	if liveCount < *limit-1 {
		var retired tree.RetiredPages
		id, ok, err := TakeLowestUsed(store, root, *limit, kind, &retired)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, corrupt("ID used bitmap has no advertised hole")
		}
		if err := store.RetirePages(retired); err != nil {
			return 0, err
		}
		return id, nil
	}
	if *limit == 1<<32 {
		return 0, exhausted()
	}
	id := uint32(*limit)
	(*limit)++
	var retired tree.RetiredPages
	if err := setDenseAppend(store, root, *limit, kind, id, &retired); err != nil {
		return 0, err
	}
	if err := store.RetirePages(retired); err != nil {
		return 0, err
	}
	return id, nil
}

func touch(store tree.Store, pageNumber uint32, kind Kind, level uint16, base, limit uint64, retired *tree.RetiredPages) (uint32, Header, error) {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return 0, Header{}, err
	}
	header, err := InspectHeader(page, targetTxn, kind, &level)
	if err != nil {
		return 0, Header{}, err
	}
	private := format.U64(page[format.HeaderBorn:]) == targetTxn
	if private {
		return pageNumber, header, nil
	}
	privatePage, err := store.Allocate()
	if err != nil {
		return 0, Header{}, err
	}
	if err := tree.CopyForCow(store, pageNumber, privatePage); err != nil {
		return 0, Header{}, err
	}
	if err := retired.Push(pageNumber); err != nil {
		return 0, Header{}, err
	}
	return privatePage, header, nil
}

func subtreeHasCandidate(store tree.Store, pageNumber uint32, kind Kind, base, limit uint64) (bool, error) {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return false, err
	}
	header, err := InspectHeader(page, targetTxn, kind, nil)
	if err != nil {
		return false, err
	}
	return pageHasCandidate(page, header.Level, base, limit, kind)
}

// pageHasCandidate reports whether the page has any zero candidate bit in
// [first_candidate, limit) (Rust used_bitmap/page.rs page_has_candidate).
func pageHasCandidate(page []byte, level uint16, base, limit uint64, kind Kind) (bool, error) {
	if level == 0 {
		first := base
		if kind.FirstCandidate() > first {
			first = kind.FirstCandidate()
		}
		_, found, err := lowestLeaf(page, base, first, limit)
		return found, err
	}
	_, found, err := FirstSummary(page, 0)
	return found, err
}

// lowestLeaf finds the lowest zero bit of one leaf at or after start and
// below limit (Rust used_bitmap/page.rs lowest_leaf).
func lowestLeaf(page []byte, base, start, limit uint64) (uint64, bool, error) {
	local := uint64(0)
	if start > base {
		local = start - base
	}
	index := int(local / 64)
	for index < LeafWords {
		wordBase := base + uint64(index)*64
		if wordBase >= limit {
			return 0, false, nil
		}
		word, err := LeafWord(page, index)
		if err != nil {
			return 0, false, err
		}
		candidates := ^word
		if uint64(index) == local/64 {
			candidates &= ^uint64(0) << (local % 64)
		}
		if limit-wordBase < 64 {
			candidates &= (uint64(1) << (limit - wordBase)) - 1
		}
		if candidates != 0 {
			return wordBase + uint64(trailingZeros(candidates)), true, nil
		}
		index++
	}
	return 0, false, nil
}

// wordHasCandidate reports whether one word has a zero in the eligible
// span of the namespace (Rust mutation::word_has_candidate).
func wordHasCandidate(word uint64, index int, base, limit uint64, kind Kind) bool {
	wordBase := base + uint64(index)*64
	first := wordBase
	if kind.FirstCandidate() > first {
		first = kind.FirstCandidate()
	}
	end := wordBase + 64
	if limit < end {
		end = limit
	}
	if first >= end {
		return false
	}
	startBit := uint32(first - wordBase)
	endBit := uint32(end - wordBase)
	lower := ^uint64(0) << startBit
	upper := ^uint64(0)
	if endBit != 64 {
		upper = (uint64(1) << endBit) - 1
	}
	eligible := lower & upper
	return word&eligible != eligible
}

func requireBit(limit uint64, kind Kind, bit uint32) error {
	if _, err := RequiredLevel(limit); err != nil {
		return err
	}
	if uint64(bit) < kind.FirstCandidate() || uint64(bit) >= limit {
		return invalid("used bitmap bit is outside its namespace")
	}
	return nil
}

// TakeLowestUsed returns the lowest unused ID and marks it used (Rust
// used_bitmap::take_lowest).
func TakeLowestUsed(store tree.Store, root *uint32, limit uint64, kind Kind, retired *tree.RetiredPages) (uint32, bool, error) {
	bit, ok, err := findLowest(store, *root, limit, kind)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	if err := setInner(store, root, limit, kind, bit, retired, nil); err != nil {
		return 0, false, err
	}
	return bit, true, nil
}

// SetUsed marks one ID used (Rust used_bitmap::set).
func SetUsed(store tree.Store, root *uint32, limit uint64, kind Kind, bit uint32, retired *tree.RetiredPages) error {
	return setInner(store, root, limit, kind, bit, retired, nil)
}

func setDenseAppend(store tree.Store, root *uint32, limit uint64, kind Kind, bit uint32, retired *tree.RetiredPages) error {
	if uint64(bit)+1 != limit {
		return corrupt("dense bitmap append does not extend its limit")
	}
	hint := false
	return setInner(store, root, limit, kind, bit, retired, &hint)
}

// setInner is the shared used-bit editor; the optional candidateHint pins
// the first summary update (Rust set_inner).
func setInner(store tree.Store, root *uint32, limit uint64, kind Kind, bit uint32, retired *tree.RetiredPages, candidateHint *bool) error {
	if err := requireBit(limit, kind, bit); err != nil {
		return err
	}
	spec := setSpec{limit: limit, kind: kind, bit: bit, candidateHint: candidateHint}
	level, err := RequiredLevel(limit)
	if err != nil {
		return err
	}
	if *root == 0 {
		pageNumber, err := newUsedSubtree(store, kind, level, 0, limit, bit)
		if err != nil {
			return err
		}
		*root = pageNumber
		return nil
	}
	if err := growUsedRoot(store, root, kind, level, limit); err != nil {
		return err
	}
	cursor, err := start(store, *root, kind, level, limit, retired)
	if err != nil {
		return err
	}
	*root = cursor.pageNumber
	path := &editPath{}
	for cursor.header.Level > 0 {
		step, err := branchStepOf(store, cursor, bit)
		if err != nil {
			return err
		}
		if err := path.push(frame(cursor, &step)); err != nil {
			return err
		}
		if step.child == 0 {
			return insertMissing(store, cursor, path, step, spec)
		}
		cursor, err = touchChild(store, cursor, step, limit, kind, retired)
		if err != nil {
			return err
		}
	}
	return setLeaf(store, cursor, path, spec)
}

// ClearUsed clears one used ID and reports whether it was set (Rust
// used_bitmap::clear).
func ClearUsed(store tree.Store, root *uint32, limit uint64, kind Kind, bit uint32, retired *tree.RetiredPages) (bool, error) {
	if err := requireBit(limit, kind, bit); err != nil {
		return false, err
	}
	contains, err := contains(store, *root, limit, kind, bit)
	if err != nil {
		return false, err
	}
	if !contains {
		return false, nil
	}
	if *root == 0 {
		return false, corrupt("used bitmap root disappeared")
	}
	level, err := RequiredLevel(limit)
	if err != nil {
		return false, err
	}
	cursor, err := start(store, *root, kind, level, limit, retired)
	if err != nil {
		return false, err
	}
	*root = cursor.pageNumber
	path := &editPath{}
	for cursor.header.Level > 0 {
		step, err := branchStepOf(store, cursor, bit)
		if err != nil {
			return false, err
		}
		if step.child == 0 {
			return false, corrupt("used bitmap bit path disappeared")
		}
		if err := path.push(frame(cursor, &step)); err != nil {
			return false, err
		}
		cursor, err = touchChild(store, cursor, step, limit, kind, retired)
		if err != nil {
			return false, err
		}
	}
	empty, err := clearLeaf(store, cursor, path, limit, kind, bit)
	if err != nil {
		return false, err
	}
	if empty {
		if err := removeEmptyPath(store, root, path, limit, kind); err != nil {
			return false, err
		}
	}
	return true, nil
}

func start(store tree.Store, root uint32, kind Kind, level uint16, limit uint64, retired *tree.RetiredPages) (usedCursor, error) {
	pageNumber, header, err := touch(store, root, kind, level, 0, limit, retired)
	if err != nil {
		return usedCursor{}, err
	}
	return usedCursor{pageNumber: pageNumber, header: header}, nil
}

func branchStepOf(store tree.Store, cursor usedCursor, bit uint32) (branchStep, error) {
	index, err := ChildIndex(bit, cursor.header.Level)
	if err != nil {
		return branchStep{}, err
	}
	span, err := Coverage(cursor.header.Level - 1)
	if err != nil {
		return branchStep{}, err
	}
	childBase, err := childBaseAt(cursor.base, span, index)
	if err != nil {
		return branchStep{}, err
	}
	limit := store.PageLimit()
	page, err := store.Inspect(cursor.pageNumber)
	if err != nil {
		return branchStep{}, err
	}
	child, err := CheckedBranchChild(page, cursor.header, index, limit)
	if err != nil {
		return branchStep{}, err
	}
	return branchStep{index: index, child: child, childBase: childBase}, nil
}

func frame(cursor usedCursor, step *branchStep) usedFrame {
	return usedFrame{
		pageNumber: cursor.pageNumber,
		childIndex: step.index,
		level:      cursor.header.Level,
		childBase:  step.childBase,
	}
}

func touchChild(store tree.Store, parent usedCursor, step branchStep, limit uint64, kind Kind, retired *tree.RetiredPages) (usedCursor, error) {
	pageNumber, header, err := touch(store, step.child, kind, parent.header.Level-1, step.childBase, limit, retired)
	if err != nil {
		return usedCursor{}, err
	}
	if pageNumber != step.child {
		page, tag, err := store.Update(parent.pageNumber)
		if err != nil {
			return usedCursor{}, err
		}
		if err := setPointer(page, parent.header, step.index, pageNumber); err != nil {
			return usedCursor{}, err
		}
		if err := store.FinishEdit(page, tag); err != nil {
			return usedCursor{}, err
		}
	}
	return usedCursor{pageNumber: pageNumber, header: header, base: step.childBase}, nil
}

func insertMissing(store tree.Store, cursor usedCursor, path *editPath, step branchStep, spec setSpec) error {
	child, err := newUsedSubtree(store, spec.kind, cursor.header.Level-1, step.childBase, spec.limit, spec.bit)
	if err != nil {
		return err
	}
	candidate := false
	if spec.candidateHint != nil {
		candidate = *spec.candidateHint
	} else {
		candidate, err = subtreeHasCandidate(store, child, spec.kind, step.childBase, spec.limit)
		if err != nil {
			return err
		}
	}
	page, tag, err := store.Update(cursor.pageNumber)
	if err != nil {
		return err
	}
	if err := setBranchChild(page, cursor.header, step.index, child, candidate); err != nil {
		return err
	}
	if err := store.FinishEdit(page, tag); err != nil {
		return err
	}
	if spec.candidateHint != nil {
		return propagateKnown(store, path.frames[:path.depth-1], cursor.pageNumber, cursor.base, spec.limit, spec.kind, candidate)
	}
	return propagate(store, path.frames[:path.depth-1], cursor.pageNumber, cursor.base, spec.limit, spec.kind)
}

func setLeaf(store tree.Store, cursor usedCursor, path *editPath, spec setSpec) error {
	wordIndex := LeafWordIndex(spec.bit)
	page, tag, err := store.Update(cursor.pageNumber)
	if err != nil {
		return err
	}
	word, err := LeafWord(page, wordIndex)
	if err != nil {
		return err
	}
	mask := uint64(1) << (uint64(spec.bit) % 64)
	if word&mask != 0 {
		return corrupt("used bitmap bit is already set")
	}
	next := word | mask
	if err := SetLeafWord(page, wordIndex, next); err != nil {
		return err
	}
	count := cursor.header.ItemCount
	if word == 0 {
		count++
	}
	stampLeaf(page, count)
	wordCandidate := wordHasCandidate(next, wordIndex, cursor.base, spec.limit, spec.kind)
	if err := store.FinishEdit(page, tag); err != nil {
		return err
	}
	candidate := false
	if spec.candidateHint != nil {
		candidate = *spec.candidateHint
	} else {
		candidate = wordCandidate
		if !candidate {
			c, err := subtreeHasCandidate(store, cursor.pageNumber, spec.kind, cursor.base, spec.limit)
			if err != nil {
				return err
			}
			candidate = c
		}
	}
	return propagateKnown(store, path.frames[:path.depth], cursor.pageNumber, cursor.base, spec.limit, spec.kind, candidate)
}

// clearLeaf clears one bit of the private leaf and reports whether the
// leaf became empty (Rust clear_leaf).
func clearLeaf(store tree.Store, cursor usedCursor, path *editPath, limit uint64, kind Kind, bit uint32) (bool, error) {
	wordIndex := LeafWordIndex(bit)
	page, tag, err := store.Update(cursor.pageNumber)
	if err != nil {
		return false, err
	}
	word, err := LeafWord(page, wordIndex)
	if err != nil {
		return false, err
	}
	mask := uint64(1) << (uint64(bit) % 64)
	if word&mask == 0 {
		return false, corrupt("used bitmap bit disappeared")
	}
	next := word &^ mask
	if err := SetLeafWord(page, wordIndex, next); err != nil {
		return false, err
	}
	count := cursor.header.ItemCount
	if next == 0 {
		count--
	}
	if err := store.FinishEdit(page, tag); err != nil {
		return false, err
	}
	if count == 0 {
		if err := store.DiscardPrivate(cursor.pageNumber); err != nil {
			return false, err
		}
		return true, nil
	}
	page, tag, err = store.Update(cursor.pageNumber)
	if err != nil {
		return false, err
	}
	stampLeaf(page, count)
	if err := store.FinishEdit(page, tag); err != nil {
		return false, err
	}
	if err := propagateKnown(store, path.frames[:path.depth], cursor.pageNumber, cursor.base, limit, kind, true); err != nil {
		return false, err
	}
	return false, nil
}

// stampLeaf writes the leaf word count into the header (Rust stamp_leaf).
func stampLeaf(page []byte, count int) {
	format.PutU16(page[format.HeaderCount:], uint16(count))
	work.BytesMoved(2) // Rust used_bitmap stamp_leaf: page.put_u16
}

// childBaseAt is the used-bitmap child-base arithmetic, taking the child
// span directly (Rust used_bitmap add_child_base).
func childBaseAt(base, span uint64, index int) (uint64, error) {
	product := span * uint64(index)
	if span != 0 && product/span != uint64(index) {
		return 0, overflow("used bitmap coverage")
	}
	sum := base + product
	if sum < base {
		return 0, overflow("used bitmap coverage")
	}
	return sum, nil
}
