// COW root-to-leaf descent with per-level page privatization (Rust
// fixed_tree.rs private_path/private_path_select).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Frame records one level of a descent path (Rust Frame).
type Frame struct {
	PageNumber uint32
	Index      int
	ItemCount  int
}

// Path is the fixed-depth descent stack (Rust Path).
type Path struct {
	frames [maxPath]Frame
	depth  int
}

// Depth reports the number of recorded frames.
func (p *Path) Depth() int { return p.depth }

// Push records one branch frame.
func (p *Path) Push(frame Frame) error {
	if p.depth >= maxPath {
		return corrupt("B+tree exceeds its maximum height")
	}
	p.frames[p.depth] = frame
	p.depth++
	return nil
}

// Frame returns the frame at index.
func (p *Path) Frame(index int) Frame { return p.frames[index] }

// Slice returns the recorded frames.
func (p *Path) Slice() []Frame { return p.frames[:p.depth] }

// PrivateLeaf is the outcome of a COW descent: the private leaf page, its
// header, the descent path, and the leaf selection.
type PrivateLeaf struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Index      int
	Exists     bool
}

// visitedBranch describes one inspected page during the descent loop.
type visitedBranch struct {
	header  Header
	private bool
	index   int
	child   uint32
}

// PrivatePath descends from the root, privatizing every committed page
// along the path for the draft transaction, and returns the private leaf
// plus the lower-bound selection of key (Rust private_path + KeySelector).
// The descent itself is the single authoritative privatePathSelect loop.
func PrivatePath(codec Codec, store Store, root *uint32, key Key, retired *RetiredPages) (*PrivateLeaf, error) {
	leaf, err := privatePathSelect(codec, store, root, key, retired, func(page []byte, header *Header, path *Path) (any, error) {
		index, exists, err := lowerBound(codec, page, header, key, true)
		if err != nil {
			return nil, err
		}
		return keySelection{index: index, exists: exists}, nil
	})
	if err != nil {
		return nil, err
	}
	sel := leaf.Selection.(keySelection)
	return &PrivateLeaf{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: sel.index, Exists: sel.exists}, nil
}

// PrivateLeafSelect is the outcome of a selector-based COW descent: the
// private leaf page, its header, the descent path, and the leaf selection
// (Rust private_path_select PrivateLeaf<L::Selection>).
type PrivateLeafSelect struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Selection  any
}

// leafSelector decides the leaf position during one COW descent (Rust
// LeafSelector::select). It receives the page view, the parsed header, and
// the descent path; only the leaf page is presented.
type leafSelector func(page []byte, header *Header, path *Path) (any, error)

// privatePathSelect is the single authoritative COW root-to-leaf descent
// (Rust private_path_select). It descends from the root, privatizing every
// committed page along the path for the draft transaction, and returns the
// private leaf plus the custom leaf selection.
func privatePathSelect(codec Codec, store Store, root *uint32, key Key, retired *RetiredPages, selectLeaf leafSelector) (*PrivateLeafSelect, error) {
	work.TreeLookup(1)
	var path Path
	pageNumber := *root
	var expectedLevel *uint16
	hasParent := false
	parentPage := uint32(0)
	parentIndex := 0

	for {
		var header *Header
		var born uint64
		isLeaf := false
		var selection any
		index := 0
		child := uint32(0)
		if err := store.Inspect(pageNumber, func(page []byte) error {
			h, err := parse(codec, page, store.TargetTxn(), expectedLevel)
			if err != nil {
				return err
			}
			header = h
			born = format.U64(page[format.HeaderBorn:])
			if h.Level == 0 {
				isLeaf = true
				selection, err = selectLeaf(page, h, &path)
				return err
			}
			index, _, err = lowerBound(codec, page, h, key, false)
			if err != nil {
				return err
			}
			child, err = branchChild(codec, page, h, index, store.PageLimit())
			return err
		}); err != nil {
			return nil, err
		}

		activePage := pageNumber
		if born != store.TargetTxn() {
			copied, err := touch(store, pageNumber, retired)
			if err != nil {
				return nil, err
			}
			activePage = copied
			if hasParent {
				if err := replaceBranchChild(codec, store, parentPage, parentIndex, copied); err != nil {
					return nil, err
				}
			} else {
				*root = copied
			}
		}
		if isLeaf {
			return &PrivateLeafSelect{Path: path, PageNumber: activePage, Header: *header, Selection: selection}, nil
		}
		if err := path.Push(Frame{PageNumber: activePage, Index: index, ItemCount: int(header.ItemCount)}); err != nil {
			return nil, err
		}
		hasParent = true
		parentPage = activePage
		parentIndex = index
		pageNumber = child
		level := header.Level - 1
		expectedLevel = &level
		work.TreeDescent(1)
	}
}

// keySelection is the standard lower-bound leaf selection.
type keySelection struct {
	index  int
	exists bool
}

// touch allocates a private copy of a committed page and records the
// original for retirement (Rust touch).
func touch(store Store, pageNumber uint32, retired *RetiredPages) (uint32, error) {
	privatePage, err := store.Allocate()
	if err != nil {
		return 0, err
	}
	if err := CopyForCow(store, pageNumber, privatePage); err != nil {
		return 0, err
	}
	if err := retired.Push(pageNumber); err != nil {
		return 0, err
	}
	return privatePage, nil
}

// replaceBranchChild updates one branch slot to name a new child page
// (Rust replace_branch_child). The branch page must already be private.
func replaceBranchChild(codec Codec, store Store, pageNumber uint32, index int, child uint32) error {
	targetTxn := store.TargetTxn()
	var header *Header
	var key Key
	var oldLen int
	if err := store.Inspect(pageNumber, func(page []byte) error {
		h, err := parse(codec, page, targetTxn, nil)
		if err != nil {
			return err
		}
		header = h
		k, err := keyAt(codec, page, h, index)
		if err != nil {
			return err
		}
		key = k
		cell, err := codecCell(codec, page, h, index)
		if err != nil {
			return err
		}
		oldLen = len(cell)
		if _, err := branchChild(codec, page, h, index, store.PageLimit()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	replacement, err := newBranchCell(codec, key, child)
	if err != nil {
		return err
	}
	if len(replacement.Bytes()) != oldLen {
		return corrupt("B+tree child replacement changed key size")
	}
	return store.Update(pageNumber, func(page []byte) error {
		ok, err := format.SlottedReplace(page, header, index, oldLen, replacement.Bytes())
		if err != nil {
			return err
		}
		if !ok {
			return corrupt("B+tree child replacement no longer fits")
		}
		return nil
	})
}
