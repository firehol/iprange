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

// Path is the fixed-depth descent stack (Rust Path). It is a value: the
// COW descent returns it embedded in the selection result, never boxed.
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
// header, the descent path, and the leaf selection. It is a value; the
// caller moves it (Rust returns the same struct by value).
type PrivateLeaf struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Index      int
	Exists     bool
}

// PrivateLeafSelect is the outcome of a selector-based COW descent: the
// private leaf page, its header, the descent path, and the leaf selection
// (Rust private_path_select PrivateLeaf<L::Selection>). Both type
// parameters are concrete, so the descent never boxes the selection.
type PrivateLeafSelect[T any, S any] struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Selection  S
}

// leafSelector decides the leaf position during one COW descent (Rust
// LeafSelector::select). The selector receives the page view, the parsed
// header, and the descent path by value: only the leaf page is presented
// and no address-taken stack value crosses the indirect call, so the
// descent stays allocation-free. The selection runs before the leaf is
// privatized, when the page view is still the view the store handed out
// (a COW copy or a mapping growth never invalidates it).
type leafSelector[S any] func(page []byte, header Header, path Path) (S, error)

// privatePathSelect is the single authoritative COW root-to-leaf descent
// (Rust private_path_select). It descends from the root, privatizing
// every committed page along the path for the draft transaction, and
// returns the private leaf plus the custom leaf selection.
func privatePathSelect[T any, S any](codec Codec[T], store Store, root *uint32, key Key, retired RetiredPages, selectLeaf leafSelector[S]) (PrivateLeafSelect[T, S], RetiredPages, error) {
	work.TreeLookup(1)
	var path Path
	pageNumber := *root
	var expectedLevel *uint16
	hasParent := false
	parentPage := uint32(0)
	parentIndex := 0

	for {
		page, err := store.Inspect(pageNumber)
		if err != nil {
			return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
		}
		header, err := parse(codec, page, store.TargetTxn(), expectedLevel)
		if err != nil {
			return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
		}
		born := format.U64(page[format.HeaderBorn:])
		var selection S
		var index int
		var child uint32
		isLeaf := header.Level == 0
		if isLeaf {
			selection, err = selectLeaf(page, header, path)
			if err != nil {
				return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
			}
		} else {
			index, _, err = lowerBound(codec, page, &header, key, false)
			if err != nil {
				return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
			}
			child, err = branchChild(codec, page, &header, index, store.PageLimit())
			if err != nil {
				return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
			}
		}

		activePage := pageNumber
		if born != store.TargetTxn() {
			copied, err := touch(store, pageNumber, &retired)
			if err != nil {
				return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
			}
			activePage = copied
			if hasParent {
				if err := replaceBranchChild(codec, store, parentPage, parentIndex, copied); err != nil {
					return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
				}
			} else {
				*root = copied
			}
		}
		if isLeaf {
			return PrivateLeafSelect[T, S]{Path: path, PageNumber: activePage, Header: header, Selection: selection}, retired, nil
		}
		if err := path.Push(Frame{PageNumber: activePage, Index: index, ItemCount: int(header.ItemCount)}); err != nil {
			return PrivateLeafSelect[T, S]{}, RetiredPages{}, err
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

// PrivatePath descends from the root, privatizing every committed page
// along the path for the draft transaction, and returns the private leaf
// plus the lower-bound selection of key (Rust private_path + KeySelector).
func PrivatePath[T any](codec Codec[T], store Store, root *uint32, key Key, retired RetiredPages) (PrivateLeaf, RetiredPages, error) {
	leaf, retired, err := privatePathSelect(codec, store, root, key, retired, func(page []byte, header Header, _ Path) (keySelection, error) {
		index, exists, err := lowerBound(codec, page, &header, key, true)
		if err != nil {
			return keySelection{}, err
		}
		return keySelection{index: index, exists: exists}, nil
	})
	if err != nil {
		return PrivateLeaf{}, RetiredPages{}, err
	}
	sel := leaf.Selection
	return PrivateLeaf{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: sel.index, Exists: sel.exists}, retired, nil
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
func replaceBranchChild[T any](codec Codec[T], store Store, pageNumber uint32, index int, child uint32) error {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	header, err := parse(codec, page, targetTxn, nil)
	if err != nil {
		return err
	}
	key, err := keyAt(codec, page, &header, index)
	if err != nil {
		return err
	}
	cell, err := codecCell(codec, page, &header, index)
	if err != nil {
		return err
	}
	oldLen := len(cell)
	if _, err := branchChild(codec, page, &header, index, store.PageLimit()); err != nil {
		return err
	}
	replacement, err := newBranchCell(codec, key, child)
	if err != nil {
		return err
	}
	if len(replacement.Bytes()) != oldLen {
		return corrupt("B+tree child replacement changed key size")
	}
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	ok, err := format.SlottedReplace(page, &header, index, oldLen, replacement.Bytes())
	if err != nil {
		return err
	}
	if !ok {
		return corrupt("B+tree child replacement no longer fits")
	}
	return store.RestoreDirty(pageNumber, tag)
}
