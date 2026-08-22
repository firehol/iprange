// One-shot reads through the canonical ordered tree traversal (Rust
// fixed_tree/read.rs + the cursor seek path).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/work"
)

// LeafLocation identifies one leaf record position (Rust LeafLocation).
// It is a value: the hot lookup paths return it embedded in their result
// instead of allocating one location per record.
type LeafLocation struct {
	PageNumber uint32
	Header     Header
	Index      int
}

// Predecessor returns the greatest leaf value at or below key when the key
// exists, otherwise the greatest value strictly below it (Rust
// fixed_tree::predecessor; used by retirement neighbor classification).
// The bool reports whether the tree had any qualifying record.
func Predecessor[T any](codec Codec[T], store Store, root uint32, key Key) (T, bool, error) {
	value, _, found, err := cursorLookup(codec, store, root, key, backward, seekPrevious)
	return value, found, err
}

// PredecessorLocated is Predecessor plus the selected leaf location
// (Rust fixed_tree::predecessor_located): callers that need to re-inspect
// the record's cell later keep the location instead of re-descending.
func PredecessorLocated[T any](codec Codec[T], store Store, root uint32, key Key) (T, LeafLocation, bool, error) {
	return cursorLookup(codec, store, root, key, backward, seekPrevious)
}

// AtOrAfter returns the first leaf value at or after key, mirroring
// fixed_tree::at_or_after.
func AtOrAfter[T any](codec Codec[T], store Store, root uint32, key Key) (T, bool, error) {
	value, _, found, err := cursorLookup(codec, store, root, key, forward, seekCurrentOrNext)
	return value, found, err
}

// InspectLeaf re-verifies one located leaf page and runs inspect on its
// cell (Rust inspect_leaf).
func InspectLeaf[T any](codec Codec[T], store Store, pageNumber uint32, itemCount uint16, index int, inspect func(cell []byte) error) error {
	level := uint16(0)
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	header, err := parse(codec, page, store.TargetTxn(), &level)
	if err != nil {
		return err
	}
	if header.ItemCount != itemCount {
		return corrupt("B+tree leaf changed during inspection")
	}
	cell, err := codecCell(codec, page, &header, index)
	if err != nil {
		return err
	}
	work.LeafValidation(1)
	return inspect(cell)
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

type seekPolicy func(position int, exact bool, direction cursorDirection, itemCount int) (seekPosition, int, error)

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
// the item inside the seek closure). Every page view is fetched through
// the closure-free Store.Inspect, so one lookup never allocates.
func cursorLookup[T any](codec Codec[T], store Store, root uint32, key Key, direction cursorDirection, policy seekPolicy) (T, LeafLocation, bool, error) {
	work.TreeLookup(1)
	if root == 0 {
		var zero T
		return zero, LeafLocation{}, false, nil
	}
	if root < 2 || uint64(root) >= store.PageLimit() {
		var zero T
		return zero, LeafLocation{}, false, corrupt("B+tree root is outside page bounds")
	}
	pageNumber := root
	var expectedLevel *uint16
	var path [maxPath]cursorFrame
	depth := 0
	for {
		page, err := store.Inspect(pageNumber)
		if err != nil {
			var zero T
			return zero, LeafLocation{}, false, err
		}
		header, err := parse(codec, page, store.TargetTxn(), expectedLevel)
		if err != nil {
			var zero T
			return zero, LeafLocation{}, false, err
		}
		position, exact, err := lowerBound(codec, page, &header, key, true)
		if err != nil {
			var zero T
			return zero, LeafLocation{}, false, err
		}
		if header.Level == 0 {
			selected, index, err := policy(position, exact, direction, int(header.ItemCount))
			if err != nil {
				var zero T
				return zero, LeafLocation{}, false, err
			}
			var value T
			var zero T
			var location LeafLocation
			switch selected {
			case seekFinished:
				return value, location, false, nil
			case seekIndex:
				if index >= int(header.ItemCount) {
					return zero, LeafLocation{}, false, corrupt("B+tree seek index is invalid")
				}
				cell, err := codecCell(codec, page, &header, index)
				if err != nil {
					return zero, LeafLocation{}, false, err
				}
				value, err = codec.ReadLeaf(cell)
				if err != nil {
					return zero, LeafLocation{}, false, err
				}
				return value, LeafLocation{PageNumber: pageNumber, Header: header, Index: index}, true, nil
			}
			// seekNextLeaf: move to the sibling leaf and read the record
			// there (Rust set_leaf + advance_leaf + read_current).
			location, found, err := advanceLeaf(codec, store, &path, &depth, direction)
			if err != nil {
				return zero, LeafLocation{}, false, err
			}
			if !found {
				return zero, LeafLocation{}, false, nil
			}
			value, err = readOn(codec, store, location)
			if err != nil {
				return zero, LeafLocation{}, false, err
			}
			return value, location, true, nil
		}
		index := position
		if !exact {
			if position == 0 {
				if direction == backward {
					// The seek walked before the first record: no
					// qualifying record exists (Rust seek returns None,
					// not an error).
					var zero T
					return zero, LeafLocation{}, false, nil
				}
				index = 0
			} else {
				index = position - 1
			}
		}
		child, err := branchChild(codec, page, &header, index, store.PageLimit())
		if err != nil {
			var zero T
			return zero, LeafLocation{}, false, err
		}
		if depth >= maxPath {
			var zero T
			return zero, LeafLocation{}, false, corrupt("B+tree exceeds its maximum height")
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
	}
}

// advanceLeaf moves an edge-seek into the sibling subtree on the same side
// (Rust Cursor::advance_leaf followed by descend_edge): walk up the path
// to the nearest branch with a sibling on the seek side, then descend to
// the edge leaf of that sibling. Returns found=false when the seek is
// finished.
func advanceLeaf[T any](codec Codec[T], store Store, path *[maxPath]cursorFrame, depth *int, direction cursorDirection) (LeafLocation, bool, error) {
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
		page, err := store.Inspect(frame.pageNumber)
		if err != nil {
			return LeafLocation{}, false, err
		}
		expected := frame.level
		header, err := parse(codec, page, store.TargetTxn(), &expected)
		if err != nil {
			return LeafLocation{}, false, err
		}
		if int(header.ItemCount) != frame.itemCount {
			return LeafLocation{}, false, corrupt("B+tree branch changed during traversal")
		}
		child, err := branchChild(codec, page, &header, index, store.PageLimit())
		if err != nil {
			return LeafLocation{}, false, err
		}
		level := frame.level - 1
		pageNumber := child
		expectedLevel := &level
		for {
			page, err := store.Inspect(pageNumber)
			if err != nil {
				return LeafLocation{}, false, err
			}
			header, err := parse(codec, page, store.TargetTxn(), expectedLevel)
			if err != nil {
				return LeafLocation{}, false, err
			}
			idx := 0
			if direction == backward {
				idx = int(header.ItemCount) - 1
			}
			if header.Level == 0 {
				return LeafLocation{PageNumber: pageNumber, Header: header, Index: idx}, true, nil
			}
			child, err := branchChild(codec, page, &header, idx, store.PageLimit())
			if err != nil {
				return LeafLocation{}, false, err
			}
			if *depth >= maxPath {
				return LeafLocation{}, false, corrupt("B+tree exceeds its maximum height")
			}
			path[*depth] = cursorFrame{
				pageNumber: pageNumber,
				index:      idx,
				itemCount:  int(header.ItemCount),
				level:      header.Level,
			}
			*depth++
			pageNumber = child
			expected := *expectedLevel - 1
			expectedLevel = &expected
			work.TreeDescent(1)
		}
	}
	return LeafLocation{}, false, nil
}

func readOn[T any](codec Codec[T], store Store, location LeafLocation) (T, error) {
	var value T
	level := uint16(0)
	page, err := store.Inspect(location.PageNumber)
	if err != nil {
		return value, err
	}
	header, err := parse(codec, page, store.TargetTxn(), &level)
	if err != nil {
		return value, err
	}
	cell, err := codecCell(codec, page, &header, location.Index)
	if err != nil {
		return value, err
	}
	return codec.ReadLeaf(cell)
}

func seekPrevious(position int, exact bool, _ cursorDirection, _ int) (seekPosition, int, error) {
	if exact {
		return seekIndex, position, nil
	}
	if position == 0 {
		return seekFinished, 0, nil
	}
	return seekIndex, position - 1, nil
}

func seekCurrentOrNext(position int, _ bool, _ cursorDirection, itemCount int) (seekPosition, int, error) {
	if position < itemCount {
		return seekIndex, position, nil
	}
	return seekNextLeaf, 0, nil
}

// adjacentLeaf descends the sibling subtree of the deepest branch that has
// one (Rust adjacent_leaf).
func adjacentLeaf[T any](codec Codec[T], store Store, path *Path, direction AdjacentLeafDirection) (adjacentLeafResult[T], bool, error) {
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
		page, err := store.Inspect(frame.PageNumber)
		if err != nil {
			return adjacentLeafResult[T]{}, false, err
		}
		header, err := parse(codec, page, store.TargetTxn(), nil)
		if err != nil {
			return adjacentLeafResult[T]{}, false, err
		}
		if int(header.ItemCount) != frame.ItemCount || header.Level == 0 {
			return adjacentLeafResult[T]{}, false, corrupt("B+tree path changed during adjacent-leaf traversal")
		}
		child, err := branchChild(codec, page, &header, sibling, store.PageLimit())
		if err != nil {
			return adjacentLeafResult[T]{}, false, err
		}
		pageNumber := child
		expectedLevel := header.Level - 1
		for {
			page, err := store.Inspect(pageNumber)
			if err != nil {
				return adjacentLeafResult[T]{}, false, err
			}
			header, err := parse(codec, page, store.TargetTxn(), &expectedLevel)
			if err != nil {
				return adjacentLeafResult[T]{}, false, err
			}
			index := 0
			if direction == AdjacentBefore {
				index = int(header.ItemCount) - 1
			}
			cell, err := codecCell(codec, page, &header, index)
			if err != nil {
				return adjacentLeafResult[T]{}, false, err
			}
			if header.Level == 0 {
				key, err := codec.ReadKey(cell, 0)
				if err != nil {
					return adjacentLeafResult[T]{}, false, err
				}
				value, err := codec.ReadLeaf(cell)
				if err != nil {
					return adjacentLeafResult[T]{}, false, err
				}
				return adjacentLeafResult[T]{key: key, leaf: value}, true, nil
			}
			child, err := branchChild(codec, page, &header, index, store.PageLimit())
			if err != nil {
				return adjacentLeafResult[T]{}, false, err
			}
			pageNumber = child
			expectedLevel--
			work.TreeDescent(1)
		}
	}
	return adjacentLeafResult[T]{}, false, nil
}

// AdjacentLeafDirection selects the neighbor leaf for run-adjacency reads.
type AdjacentLeafDirection uint8

const (
	AdjacentBefore AdjacentLeafDirection = iota
	AdjacentAfter
)

type adjacentLeafResult[T any] struct {
	key  Key
	leaf T
}
