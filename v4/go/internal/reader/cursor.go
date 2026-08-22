package reader

// Ordered fixed-tree traversal without page copies (Rust fixed_tree::Cursor
// parity). The cursor keeps a bounded branch frame path plus the current
// leaf location; every branch page is re-validated on each climb and each
// leaf page is validated once, with the validated leaf cached exactly like
// Rust caches Leaf { page_number, header } (the immutable mapping cannot
// change under a reader). Leaf cells are decoded by the caller from mapped
// views, so no record is ever copied into owned memory.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// cursorDir selects the traversal direction.
type cursorDir uint8

const (
	cursorForward cursorDir = iota
	cursorBackward
)

// cursorFrame is one branch page on the descent path. position is the
// branch record already selected during the descent; the traversal moves
// to the neighbouring record when the current leaf is exhausted.
type cursorFrame struct {
	pageNumber uint32
	itemCount  uint16
	position   int
}

// treeCursor walks one fixed tree. branchType/leafType select the page
// family, and aux the slotted-page aux value (range trees carry the
// address family, catalog trees zero). Family-specific record policies
// (branch child decode, seek leaf/branch selection) dispatch inside the
// cursor on (branchType, aux): every persistent tree family has one
// authoritative traversal, and no record is ever copied into owned memory.
type treeCursor struct {
	r          *ImmutableReader
	root       uint32
	dir        cursorDir
	branchType format.PageType
	leafType   format.PageType
	aux        uint32
	// seek4/seekHi/seekLo are the transient seek target of the current
	// reposition; only range trees seek, and the family selects which
	// fields carry the key.
	seek4     uint32
	seekHi    uint64
	seekLo    uint64
	page      uint32
	level     uint16
	itemCount uint16
	index     int
	path      [format.MaxTreeLevel + 1]cursorFrame
	depth     int
	finished  bool
	// leafPage/leafSl cache the validated current leaf (Rust Cursor::leaf):
	// one header decode per leaf, never one per cell.
	leafPage uint32
	leafSl   format.SlottedPage
	leafOK   bool
}

// newTreeCursor validates the root bounds and positions the cursor at the
// direction's edge leaf (leftmost for forward, rightmost for backward).
func (r *ImmutableReader) newTreeCursor(root uint32, dir cursorDir, branchType, leafType format.PageType, aux uint32) (*treeCursor, error) {
	// A zero root is a valid empty tree: the cursor is finished from
	// construction (Rust fixed_tree::Cursor::unpositioned). Any other
	// out-of-bounds root is corruption.
	if root == 0 {
		work.SourcePass(1)
		return &treeCursor{r: r, root: root, dir: dir, branchType: branchType, leafType: leafType, aux: aux, finished: true}, nil
	}
	if root < 2 || uint64(root) >= r.meta.PageCount {
		return nil, corrupt("tree root is outside page bounds")
	}
	c := &treeCursor{r: r, root: root, dir: dir, branchType: branchType, leafType: leafType, aux: aux}
	if err := c.descendEdge(root); err != nil {
		return nil, err
	}
	work.SourcePass(1)
	return c, nil
}

// descendEdge descends from one validated page to the direction's edge
// leaf, storing branch frames on the way. The child must sit exactly one
// level below its parent on every step (bounded by MaxTreeLevel).
func (c *treeCursor) descendEdge(child uint32) error {
	expected := -1 // the root's own level starts the descent
	for {
		frame, leaf, err := c.readPage(child, expected)
		if err != nil {
			return err
		}
		if leaf {
			c.page = child
			c.level = frame.level
			c.itemCount = frame.itemCount
			if c.dir == cursorBackward {
				c.index = int(frame.itemCount) - 1
			} else {
				c.index = 0
			}
			return nil
		}
		// Branch: pick the edge child and record the frame.
		if c.depth >= len(c.path) {
			return corrupt("tree exceeds its maximum height")
		}
		pos := 0
		if c.dir == cursorBackward {
			pos = int(frame.itemCount) - 1
		}
		next, err := c.branchAt(frame.sl, pos, frame.level)
		if err != nil {
			return err
		}
		c.path[c.depth] = cursorFrame{pageNumber: child, itemCount: frame.itemCount, position: pos}
		c.depth++
		child, expected = next, int(frame.level)-1
	}
}

// seekPosition descends to the leaf that would hold the current seek
// target, then stores the descent path so advance can continue from the
// chosen leaf position. leafSeekPolicy selects the starting cell inside
// the target leaf (reporting when the seek is finished); branchSelectPolicy
// selects one branch child (greatest first-key <= target). Both dispatch
// on the tree family recorded at construction.
func (c *treeCursor) seekPosition() error {
	if c.root == 0 {
		c.finished = true
		return nil
	}
	expected := -1
	child := c.root
	c.depth = 0
	for {
		frame, leaf, err := c.readPage(child, expected)
		if err != nil {
			return err
		}
		if leaf {
			index, finished, err := c.leafSeekPolicy(frame.sl)
			if err != nil {
				return err
			}
			if finished {
				c.finished = true
				return nil
			}
			c.page = child
			c.level = frame.level
			c.itemCount = frame.itemCount
			c.index = index
			c.finished = false
			return nil
		}
		if c.depth >= len(c.path) {
			return corrupt("tree exceeds its maximum height")
		}
		pos, next, finished, err := c.branchSelectPolicy(frame.sl, frame.level)
		if err != nil {
			return err
		}
		if finished {
			c.finished = true
			return nil
		}
		// next is a validated child (2 <= next < PageCount); the frame
		// records the selected slot so the traversal can cross to the
		// neighbouring sibling later.
		c.path[c.depth] = cursorFrame{pageNumber: child, itemCount: frame.itemCount, position: pos}
		c.depth++
		work.TreeDescent(1)
		child, expected = next, int(frame.level)-1
	}
}

// nodeView is one validated page read during traversal.
type nodeView struct {
	sl        format.SlottedPage
	level     uint16
	itemCount uint16
}

// readPage validates one page against the expected level and returns its
// slotted view; leaf reports whether the page is a leaf of this family.
func (c *treeCursor) readPage(pageNumber uint32, expected int) (nodeView, bool, error) {
	page, err := c.r.page(pageNumber)
	if err != nil {
		return nodeView{}, false, err
	}
	h, err := format.DecodePageHeader(page, c.r.meta.TxnID)
	if err != nil {
		return nodeView{}, false, err
	}
	if expected >= 0 && int(h.Level) != expected {
		return nodeView{}, false, corrupt("tree level %d expected %d", h.Level, expected)
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, c.aux, format.SlotItemsPerPage)
	if err != nil {
		return nodeView{}, false, err
	}
	switch h.PageType {
	case c.branchType:
		if h.Level == 0 {
			return nodeView{}, false, corrupt("zero-level tree branch")
		}
		return nodeView{sl: sl, level: h.Level, itemCount: sl.Header.ItemCount}, false, nil
	case c.leafType:
		if h.Level != 0 {
			return nodeView{}, false, corrupt("tree leaf at nonzero level %d", h.Level)
		}
		return nodeView{sl: sl, level: h.Level, itemCount: sl.Header.ItemCount}, true, nil
	default:
		return nodeView{}, false, corrupt("unexpected tree page type %d", h.PageType)
	}
}

// branchAt decodes and validates one branch record at position pos.
func (c *treeCursor) branchAt(sl format.SlottedPage, pos int, level uint16) (uint32, error) {
	b, err := sl.Record(pos)
	if err != nil {
		return 0, err
	}
	child, err := c.branchChild(b)
	if err != nil {
		return 0, err
	}
	work.TreeDescent(1)
	return child, nil
}

// branchChild decodes one branch record of the tree family, returning its
// child page number. The family dispatch lives here: the range tree
// decodes by address family (aux), the catalog index tree by its record
// shape; every other page type is corruption.
func (c *treeCursor) branchChild(b []byte) (uint32, error) {
	switch c.branchType {
	case format.PageTypeRangeBranch:
		if c.aux == 4 {
			return decodeRangeBranch4(b)
		}
		return decodeRangeBranch6(b)
	case format.PageTypeCatalogIndexBranch:
		return decodeIndexBranch(b)
	}
	return 0, corrupt("tree branch page type %d has no decoder", c.branchType)
}

// leafSeekPolicy selects the starting leaf cell for the current seek
// target (Rust RangeSeek parity); finished reports a target strictly
// before or after every cell of the tree in the cursor's direction.
func (c *treeCursor) leafSeekPolicy(sl format.SlottedPage) (int, bool, error) {
	switch c.branchType {
	case format.PageTypeRangeBranch:
		if c.aux == 4 {
			return rangeLeafSeek4(sl, c.seek4, c.dir)
		}
		return rangeLeafSeek6(sl, c.seekHi, c.seekLo, c.dir)
	}
	return 0, true, corrupt("tree leaf seek is unsupported for page type %d", c.branchType)
}

// branchSelectPolicy selects the branch child for the current seek
// target (greatest first-key <= target), mirroring the per-family branch
// selectors used during seeks.
func (c *treeCursor) branchSelectPolicy(sl format.SlottedPage, level uint16) (int, uint32, bool, error) {
	switch c.branchType {
	case format.PageTypeRangeBranch:
		if c.aux == 4 {
			return rangeBranchSeek4(sl, c.seek4, c.dir, c.r.meta.PageCount)
		}
		return rangeBranchSeek6(sl, c.seekHi, c.seekLo, c.dir, c.r.meta.PageCount)
	}
	return 0, 0, false, corrupt("tree branch seek is unsupported for page type %d", c.branchType)
}

// advance moves to the next leaf cell in the cursor's direction, crossing
// to sibling leaves through the frame path. It reports the new page
// number and leaf cell index; the caller decodes the cell from the mapped
// page. finished is reported through c.finished.
func (c *treeCursor) advance() (uint32, int, error) {
	if c.finished {
		return 0, 0, nil
	}
	next := c.index + 1
	if c.dir == cursorBackward {
		next = c.index - 1
	}
	if (c.dir == cursorForward && next < int(c.itemCount)) || (c.dir == cursorBackward && next >= 0) {
		c.index = next
		return c.page, c.index, nil
	}
	// Move up to the nearest branch with a sibling on this side, then
	// descend to that sibling's edge leaf.
	for c.depth > 0 {
		c.depth--
		fr := &c.path[c.depth]
		page, err := c.r.page(fr.pageNumber)
		if err != nil {
			return 0, 0, err
		}
		h, err := format.DecodePageHeader(page, c.r.meta.TxnID)
		if err != nil {
			return 0, 0, err
		}
		if h.PageType != c.branchType {
			return 0, 0, corrupt("tree branch changed during traversal")
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, c.aux, format.SlotItemsPerPage)
		if err != nil {
			return 0, 0, err
		}
		if uint16(sl.Header.ItemCount) != fr.itemCount {
			return 0, 0, corrupt("tree branch changed during traversal")
		}
		pos := fr.position
		if c.dir == cursorForward {
			pos++
		} else {
			pos--
		}
		if pos < 0 || pos >= int(fr.itemCount) {
			continue // no sibling on this side; keep climbing
		}
		child, err := c.branchAt(sl, pos, h.Level)
		if err != nil {
			return 0, 0, err
		}
		fr.position = pos
		c.depth++
		if err := c.descendEdge(child); err != nil {
			return 0, 0, err
		}
		return c.page, c.index, nil
	}
	c.finished = true
	return 0, 0, nil
}

// openLeaf returns the current leaf's slotted view plus its page number.
// The leaf is validated once and cached (Rust Cursor::leaf); the mapped
// page cannot change under a reader, so later calls on the same leaf
// reuse the validated header and slotted shape instead of re-decoding
// them per cell.
func (c *treeCursor) openLeaf() (format.SlottedPage, uint32, error) {
	if c.leafOK && c.leafPage == c.page {
		return c.leafSl, c.page, nil
	}
	frame, leaf, err := c.readPage(c.page, int(c.level))
	if err != nil {
		return format.SlottedPage{}, 0, err
	}
	if !leaf || int(frame.itemCount) != int(c.itemCount) {
		return format.SlottedPage{}, 0, corrupt("tree leaf changed during traversal")
	}
	c.leafOK = true
	c.leafPage = c.page
	c.leafSl = frame.sl
	return frame.sl, c.page, nil
}
