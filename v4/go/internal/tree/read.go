// One-shot reads through the canonical ordered tree traversal (Rust
// fixed_tree/read.rs + the cursor seek path).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/work"
)

// LeafLocation identifies one leaf record position (Rust LeafLocation).
type LeafLocation struct {
	PageNumber uint32
	Header     Header
	Index      int
}

// Predecessor returns the greatest leaf value at or below key when the key
// exists, otherwise the greatest value strictly below it (Rust
// fixed_tree::predecessor; used by retirement neighbor classification).
func Predecessor(codec Codec, store Store, root uint32, key Key) (any, error) {
	output, _, err := cursorLookup(codec, store, root, key, backward, seekPrevious)
	return output, err
}

// AtOrAfter returns the first leaf value at or after key, mirroring
// fixed_tree::at_or_after.
func AtOrAfter(codec Codec, store Store, root uint32, key Key) (any, error) {
	output, _, err := cursorLookup(codec, store, root, key, forward, seekCurrentOrNext)
	return output, err
}

// InspectLeaf re-verifies one located leaf page and runs inspect on its
// cell (Rust inspect_leaf).
func InspectLeaf(codec Codec, store Store, pageNumber uint32, itemCount uint16, index int, inspect func(cell []byte) error) error {
	level := uint16(0)
	return store.Inspect(pageNumber, func(page []byte) error {
		header, err := parse(codec, page, store.TargetTxn(), &level)
		if err != nil {
			return err
		}
		if header.ItemCount != itemCount {
			return corrupt("B+tree leaf changed during inspection")
		}
		cell, err := codecCell(codec, page, header, index)
		if err != nil {
			return err
		}
		work.LeafValidation(1)
		return inspect(cell)
	})
}

type cursorDirection uint8

const (
	forward cursorDirection = iota
	backward
)

type seekPosition uint8

const (
	seekIndex seekPosition = iota
	seekNextLeaf
	seekFinished
)

type seekPolicy func(page []byte, header *Header, position int, exact bool, direction cursorDirection) (seekPosition, int, error)

// cursorFrame is the descent record for one branch level (Rust
// Cursor::Frame). The frame stores the child index chosen at that level so
// an edge seek can advance into the sibling subtree.
type cursorFrame struct {
	pageNumber uint32
	index      int
	itemCount  int
	level      uint16
}

// cursorLookup is the allocation-free one-shot seek of the Rust cursor:
// descend to the positioning leaf, apply the policy, and read the
// selected record inside the final page view (Rust seek_read_inner reads
// the item inside the seek closure). Returns the item value and the
// selected leaf location.
func cursorLookup(codec Codec, store Store, root uint32, key Key, direction cursorDirection, policy seekPolicy) (any, *LeafLocation, error) {
	work.TreeLookup(1)
	if root != 0 && (root < 2 || uint64(root) >= store.PageLimit()) {
		return nil, nil, corrupt("B+tree root is outside page bounds")
	}
	if root == 0 {
		return nil, nil, nil
	}
	pageNumber := root
	var expectedLevel *uint16
	var path [maxPath]cursorFrame
	depth := 0
	for {
		var header *Header
		position := 0
		exact := false
		child := uint32(0)
		index := 0
		selected := seekFinished
		value := any(nil)
		var location *LeafLocation
		advance := false
		if err := store.Inspect(pageNumber, func(page []byte) error {
			h, err := parse(codec, page, store.TargetTxn(), expectedLevel)
			if err != nil {
				return err
			}
			header = h
			pos, ex, err := lowerBound(codec, page, h, key, true)
			if err != nil {
				return err
			}
			position, exact = pos, ex
			if h.Level == 0 {
				selected, index, err = policy(page, h, position, exact, direction)
				if err != nil {
					return err
				}
				if selected == seekIndex {
					if index >= int(h.ItemCount) {
						return corrupt("B+tree seek index is invalid")
					}
					cell, err := codecCell(codec, page, h, index)
					if err != nil {
						return err
					}
					value, err = codec.ReadLeaf(cell)
					if err != nil {
						return err
					}
					location = &LeafLocation{PageNumber: pageNumber, Header: *h, Index: index}
				}
				return nil
			}
			index = position
			if !exact {
				if position == 0 {
					if direction == backward {
						return errSeekFinished
					}
					index = 0
				} else {
					index = position - 1
				}
			}
			c, err := branchChild(codec, page, h, index, store.PageLimit())
			if err != nil {
				return err
			}
			child = c
			advance = true
			return nil
		}); err != nil {
			if err == errSeekFinished {
				return nil, nil, nil
			}
			return nil, nil, err
		}
		if advance {
			if depth >= maxPath {
				return nil, nil, corrupt("B+tree exceeds its maximum height")
			}
			path[depth] = cursorFrame{
				pageNumber: pageNumber,
				index:      index,
				itemCount:  int(header.ItemCount),
				level:      header.Level,
			}
			depth++
			level := header.Level - 1
			expectedLevel = &level
			pageNumber = child
			work.TreeDescent(1)
			continue
		}
		// Leaf reached: the policy already read the Index case.
		switch selected {
		case seekFinished:
			return nil, nil, nil
		case seekIndex:
			return value, location, nil
		}
		// seekNextLeaf: move to the sibling leaf and read the record
		// there (Rust set_leaf + advance_leaf + read_current).
		location, err := advanceLeaf(codec, store, &path, &depth, direction)
		if err != nil {
			return nil, nil, err
		}
		if location == nil {
			return nil, nil, nil
		}
		value, err = readOn(codec, store, *location)
		if err != nil {
			return nil, nil, err
		}
		return value, location, nil
	}
}

// advanceLeaf moves an edge-seek into the sibling subtree on the same side
// (Rust Cursor::advance_leaf followed by descend_edge): walk up the path
// to the nearest branch with a sibling on the seek side, then descend to
// the edge leaf of that sibling. Returns nil when the seek is finished.
func advanceLeaf(codec Codec, store Store, path *[maxPath]cursorFrame, depth *int, direction cursorDirection) (*LeafLocation, error) {
	for *depth > 0 {
		slot := *depth - 1
		frame := path[slot]
		hasSibling := false
		if direction == forward {
			hasSibling = frame.index+1 < frame.itemCount
		} else {
			hasSibling = frame.index > 0
		}
		if !hasSibling {
			*depth = slot
			continue
		}
		index := frame.index + 1
		if direction == backward {
			index = frame.index - 1
		}
		path[slot].index = index
		*depth = slot + 1
		child := uint32(0)
		if err := store.Inspect(frame.pageNumber, func(page []byte) error {
			expected := frame.level
			header, err := parse(codec, page, store.TargetTxn(), &expected)
			if err != nil {
				return err
			}
			if int(header.ItemCount) != frame.itemCount {
				return corrupt("B+tree branch changed during traversal")
			}
			c, err := branchChild(codec, page, header, index, store.PageLimit())
			if err != nil {
				return err
			}
			child = c
			return nil
		}); err != nil {
			return nil, err
		}
		level := frame.level - 1
		pageNumber := child
		expectedLevel := &level
		for {
			var location *LeafLocation
			child := uint32(0)
			isBranch := false
			if err := store.Inspect(pageNumber, func(page []byte) error {
				header, err := parse(codec, page, store.TargetTxn(), expectedLevel)
				if err != nil {
					return err
				}
				idx := 0
				if direction == backward {
					idx = int(header.ItemCount) - 1
				}
				if header.Level == 0 {
					location = &LeafLocation{PageNumber: pageNumber, Header: *header, Index: idx}
					return nil
				}
				c, err := branchChild(codec, page, header, idx, store.PageLimit())
				if err != nil {
					return err
				}
				if *depth >= maxPath {
					return corrupt("B+tree exceeds its maximum height")
				}
				path[*depth] = cursorFrame{
					pageNumber: pageNumber,
					index:      idx,
					itemCount:  int(header.ItemCount),
					level:      header.Level,
				}
				*depth++
				child = c
				isBranch = true
				return nil
			}); err != nil {
				return nil, err
			}
			if !isBranch {
				return location, nil
			}
			pageNumber = child
			expected := *expectedLevel - 1
			expectedLevel = &expected
			work.TreeDescent(1)
		}
	}
	return nil, nil
}

var errSeekFinished = corrupt("seek finished")

func readOn(codec Codec, store Store, location LeafLocation) (any, error) {
	var value any
	level := uint16(0)
	err := store.Inspect(location.PageNumber, func(page []byte) error {
		header, err := parse(codec, page, store.TargetTxn(), &level)
		if err != nil {
			return err
		}
		cell, err := codecCell(codec, page, header, location.Index)
		if err != nil {
			return err
		}
		value, err = codec.ReadLeaf(cell)
		return err
	})
	return value, err
}

func seekPrevious(_ []byte, _ *Header, position int, exact bool, _ cursorDirection) (seekPosition, int, error) {
	if exact {
		return seekIndex, position, nil
	}
	if position == 0 {
		return seekFinished, 0, nil
	}
	return seekIndex, position - 1, nil
}

func seekCurrentOrNext(_ []byte, header *Header, position int, _ bool, _ cursorDirection) (seekPosition, int, error) {
	if position < int(header.ItemCount) {
		return seekIndex, position, nil
	}
	return seekNextLeaf, 0, nil
}

// adjacentLeaf descends the sibling subtree of the deepest branch that has
// one (Rust adjacent_leaf).
func adjacentLeaf(codec Codec, store Store, path *Path, direction AdjacentLeafDirection) (*adjacentLeafResult, error) {
	depth := path.Depth()
	for depth > 0 {
		depth--
		frame := path.Frame(depth)
		sibling := -1
		if direction == AdjacentBefore {
			sibling = frame.Index - 1
		} else if frame.Index+1 < frame.ItemCount {
			sibling = frame.Index + 1
		}
		if sibling < 0 {
			continue
		}
		pageNumber := uint32(0)
		var expectedLevel uint16
		if err := store.Inspect(frame.PageNumber, func(page []byte) error {
			header, err := parse(codec, page, store.TargetTxn(), nil)
			if err != nil {
				return err
			}
			if int(header.ItemCount) != frame.ItemCount || header.Level == 0 {
				return corrupt("B+tree path changed during adjacent-leaf traversal")
			}
			child, err := branchChild(codec, page, header, sibling, store.PageLimit())
			if err != nil {
				return err
			}
			pageNumber = child
			expectedLevel = header.Level - 1
			return nil
		}); err != nil {
			return nil, err
		}
		for {
			var leafResult *adjacentLeafResult
			child := uint32(0)
			branch := true
			if err := store.Inspect(pageNumber, func(page []byte) error {
				header, err := parse(codec, page, store.TargetTxn(), &expectedLevel)
				if err != nil {
					return err
				}
				index := 0
				if direction == AdjacentBefore {
					index = int(header.ItemCount) - 1
				}
				cell, err := codecCell(codec, page, header, index)
				if err != nil {
					return err
				}
				if header.Level == 0 {
					key, err := codec.ReadKey(cell, 0)
					if err != nil {
						return err
					}
					value, err := codec.ReadLeaf(cell)
					if err != nil {
						return err
					}
					leafResult = &adjacentLeafResult{key: key, leaf: value}
					branch = false
					return nil
				}
				c, err := branchChild(codec, page, header, index, store.PageLimit())
				if err != nil {
					return err
				}
				child = c
				return nil
			}); err != nil {
				return nil, err
			}
			if !branch {
				return leafResult, nil
			}
			pageNumber = child
			expectedLevel--
			work.TreeDescent(1)
		}
	}
	return nil, nil
}

// AdjacentLeafDirection selects the neighbor leaf for run-adjacency reads.
type AdjacentLeafDirection uint8

const (
	AdjacentBefore AdjacentLeafDirection = iota
	AdjacentAfter
)

type adjacentLeafResult struct {
	key  Key
	leaf any
}
