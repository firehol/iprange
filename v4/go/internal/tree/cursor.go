// Forward cursor over one healthy fixed tree through a page source that
// may free pages behind the cursor (Rust fixed_tree/cursor.rs Cursor,
// forward subset). One allocation-free cursor serves the writer-side
// base-generation range scans and the draft-private delta drains; the
// reader keeps its seek-capable cursor in the reader package.

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/work"
)

// ForwardStore is the page provider of one forward cursor (Rust Store +
// PageSource subset). Consuming cursors free pages through
// DiscardPrivate while passing them; shared cursors never call it.
type ForwardStore interface {
	TargetTxn() uint64
	PageLimit() uint64
	Inspect(pageNumber uint32, fn func(page []byte) error) error
	DiscardPrivate(pageNumber uint32) error
}

// ForwardCursor iterates one fixed tree from its first record in
// ascending key order (Rust Cursor::open + next with Direction::Forward).
// Every page view is validated against the pinned selected transaction
// and page limit captured at open, exactly like the Rust Shared/Consuming
// access wrappers, so a base generation can be scanned through a draft
// mapping without ever exposing draft pages to the scan.
type ForwardCursor struct {
	codec        Codec
	source       ForwardStore
	consume      bool
	selectedTxn  uint64
	pageLimit    uint64
	root         uint32
	path         [maxPath]cursorFrame
	depth        int
	leafPage     uint32
	leafHeader   *Header
	index        int
	needsAdvance bool
	finished     bool
}

// NewForwardCursor opens a forward cursor over root (Rust
// Cursor::open): the root is validated against the source page bounds
// before the leftmost-edge descent, and a consuming cursor additionally
// proves every descended page was born in the selected transaction.
func NewForwardCursor(codec Codec, source ForwardStore, root uint32, consume bool) (*ForwardCursor, error) {
	selectedTxn := source.TargetTxn()
	pageLimit := source.PageLimit()
	if root != 0 && (uint64(root) < 2 || uint64(root) >= pageLimit) {
		return nil, corrupt("B+tree root is outside page bounds")
	}
	cursor := &ForwardCursor{
		codec:       codec,
		source:      source,
		consume:     consume,
		selectedTxn: selectedTxn,
		pageLimit:   pageLimit,
		root:        root,
		finished:    root == 0,
	}
	if !cursor.finished {
		if err := cursor.descendEdge(codec, root, nil); err != nil {
			return nil, err
		}
	}
	work.SourcePass(1)
	return cursor, nil
}

// Finished reports whether the cursor exhausted the tree.
func (c *ForwardCursor) Finished() bool { return c.finished }

// Next positions on the next record and runs read with the current leaf
// cell, mirroring Rust next_inner: records arrive in ascending key order,
// and a consuming cursor frees each page into the store's private stack
// as soon as the cursor passes it. read must not retain the cell bytes.
func (c *ForwardCursor) Next(read func(cell []byte, header *Header, pageNumber uint32, index int) error) error {
	if err := c.requireAccess(); err != nil {
		return err
	}
	if c.finished {
		return nil
	}
	if c.needsAdvance {
		if err := c.advance(); err != nil {
			return err
		}
		if c.finished {
			return nil
		}
	}
	pageNumber := c.leafPage
	header := c.leafHeader
	index := c.index
	if err := c.source.Inspect(pageNumber, func(page []byte) error {
		cell, err := codecCell(c.codec, page, header, index)
		if err != nil {
			return err
		}
		return read(cell, header, pageNumber, index)
	}); err != nil {
		return err
	}
	c.needsAdvance = true
	if c.consume {
		return c.advance()
	}
	return nil
}

// requireAccess rejects a source whose generation changed under the
// cursor (Rust require_access; the consuming variant tolerates a grown
// page limit but never a different transaction).
func (c *ForwardCursor) requireAccess() error {
	if c.source.TargetTxn() != c.selectedTxn || c.source.PageLimit() < c.pageLimit {
		return corrupt("B+tree cursor source changed")
	}
	return nil
}

// advance moves to the next record: within the leaf, or into the next
// leaf, freeing the exhausted pages of a consuming cursor as it leaves
// them (Rust advance + advance_leaf).
func (c *ForwardCursor) advance() error {
	if c.index+1 < int(c.leafHeader.ItemCount) {
		c.index++
		c.needsAdvance = false
		return nil
	}
	if c.consume {
		if err := c.source.DiscardPrivate(c.leafPage); err != nil {
			return err
		}
	}
	return c.advanceLeaf()
}

// advanceLeaf walks up the path to the nearest branch with a following
// sibling and descends its leftmost edge (Rust advance_leaf).
func (c *ForwardCursor) advanceLeaf() error {
	c.leafPage = 0
	c.leafHeader = nil
	for c.depth > 0 {
		slot := c.depth - 1
		frame := c.path[slot]
		if frame.index+1 >= frame.itemCount {
			c.depth = slot
			if c.consume {
				if err := c.source.DiscardPrivate(frame.pageNumber); err != nil {
					return err
				}
			}
			continue
		}
		frame.index++
		c.path[slot] = frame
		c.depth = slot + 1
		child, err := c.inspectBranch(frame)
		if err != nil {
			return err
		}
		work.TreeDescent(1)
		level := frame.level - 1
		return c.descendEdge(c.codec, child, &level)
	}
	c.needsAdvance = false
	c.finished = true
	return nil
}

// inspectBranch re-verifies one branch frame and returns its newly
// selected child (Rust advance_leaf: the frame's item count must be
// unchanged since the frame was pushed).
func (c *ForwardCursor) inspectBranch(frame cursorFrame) (uint32, error) {
	var child uint32
	err := c.source.Inspect(frame.pageNumber, func(page []byte) error {
		expected := frame.level
		header, err := parse(c.codec, page, c.selectedTxn, &expected)
		if err != nil {
			return err
		}
		if int(header.ItemCount) != frame.itemCount {
			return corrupt("B+tree branch changed during traversal")
		}
		child, err = branchChild(c.codec, page, header, frame.index, c.pageLimit)
		return err
	})
	return child, err
}

// descendEdge descends the leftmost edge from pageNumber to the first
// leaf record, pushing every branch frame (Rust descend_edge).
func (c *ForwardCursor) descendEdge(codec Codec, pageNumber uint32, expectedLevel *uint16) error {
	for {
		type step struct {
			header *Header
			child  uint32
			leaf   bool
		}
		var chosen step
		if err := c.source.Inspect(pageNumber, func(page []byte) error {
			header, err := parse(codec, page, c.selectedTxn, expectedLevel)
			if err != nil {
				return err
			}
			if c.consume && header.BornTxn != c.selectedTxn {
				return corrupt("consumed B+tree contains a committed page")
			}
			chosen.header = header
			chosen.leaf = header.Level == 0
			if chosen.leaf {
				return nil
			}
			child, err := branchChild(codec, page, header, 0, c.pageLimit)
			chosen.child = child
			return err
		}); err != nil {
			return err
		}
		if chosen.leaf {
			c.leafPage = pageNumber
			c.leafHeader = chosen.header
			c.index = 0
			c.needsAdvance = false
			c.finished = false
			return nil
		}
		if err := c.push(pageNumber, 0, chosen.header); err != nil {
			return err
		}
		pageNumber = chosen.child
		level := chosen.header.Level - 1
		expectedLevel = &level
		work.TreeDescent(1)
	}
}

// push records one branch frame of the descent path (Rust push).
func (c *ForwardCursor) push(pageNumber uint32, index int, header *Header) error {
	if c.depth >= len(c.path) {
		return corrupt("B+tree exceeds its maximum height")
	}
	c.path[c.depth] = cursorFrame{
		pageNumber: pageNumber,
		index:      index,
		itemCount:  int(header.ItemCount),
		level:      header.Level,
	}
	c.depth++
	return nil
}
