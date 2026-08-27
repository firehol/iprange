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
// parsed header, the descent path, and the leaf selection. Page is the
// leaf view the descent already inspected: carrying it avoids a second
// page inspection after the descent (Rust returns the inspected page
// view inside the same struct). Page stays valid until the caller
// mutates the tree; a page re-inspection is required only before an
// Update. It is a value; the caller moves it.
type PrivateLeaf struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Page       []byte
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

// privatePathSelect is the single authoritative COW root-to-leaf descent
// (Rust private_path_select). It descends from the root, privatizing
// every committed page along the path for the draft transaction, and
// returns the private leaf (page number, parsed header, descent path,
// and the leaf page view). The leaf selection is deliberately NOT part
// of the descent: a generic selection callback would be instantiated
// through go.shape interfaces, boxing the selector on every record.
// Callers run their selection on the returned page view with concrete
// types; the descent inspects each path page exactly once, matching the
// Rust inspection counts.
func privatePathSelect[T any](codec Codec[T], store Store, root *uint32, key Key, retired RetiredPages) (PrivateLeaf, RetiredPages, error) {
	work.TreeLookup(1)
	var path Path
	pageNumber := *root
	var expectedLevel uint16
	checkLevel := false
	hasParent := false
	parentPage := uint32(0)
	parentIndex := 0

	for {
		page, err := store.Inspect(pageNumber)
		if err != nil {
			return PrivateLeaf{}, RetiredPages{}, err
		}
		header, err := parse(codec, page, store.TargetTxn(), expectedLevel, checkLevel)
		if err != nil {
			return PrivateLeaf{}, RetiredPages{}, err
		}
		born := format.U64(page[format.HeaderBorn:])
		if header.Level == 0 {
			activePage := pageNumber
			if born != store.TargetTxn() {
				copied, err := touch(store, pageNumber, &retired)
				if err != nil {
					return PrivateLeaf{}, RetiredPages{}, err
				}
				activePage = copied
				if hasParent {
					if err := replaceBranchChild(codec, store, parentPage, parentIndex, copied); err != nil {
						return PrivateLeaf{}, RetiredPages{}, err
					}
				} else {
					*root = copied
				}
				// The leaf moved to a fresh private page; re-inspect it.
				// The private page view is required for the caller's
				// selection, and the source mapping view may be a growth
				// or COW buffer the draft no longer owns.
				if page, err = store.Inspect(copied); err != nil {
					return PrivateLeaf{}, RetiredPages{}, err
				}
				if header, err = parse(codec, page, store.TargetTxn(), expectedLevel, checkLevel); err != nil {
					return PrivateLeaf{}, RetiredPages{}, err
				}
			}
			return PrivateLeaf{Path: path, PageNumber: activePage, Header: header, Page: page}, retired, nil
		}
		index, _, err := lowerBound(codec, page, &header, key, false)
		if err != nil {
			return PrivateLeaf{}, RetiredPages{}, err
		}
		child, err := branchChild(codec, page, &header, index, store.PageLimit())
		if err != nil {
			return PrivateLeaf{}, RetiredPages{}, err
		}
		activePage := pageNumber
		if born != store.TargetTxn() {
			copied, err := touch(store, pageNumber, &retired)
			if err != nil {
				return PrivateLeaf{}, RetiredPages{}, err
			}
			activePage = copied
			if hasParent {
				if err := replaceBranchChild(codec, store, parentPage, parentIndex, copied); err != nil {
					return PrivateLeaf{}, RetiredPages{}, err
				}
			} else {
				*root = copied
			}
		}
		if err := path.Push(Frame{PageNumber: activePage, Index: index, ItemCount: int(header.ItemCount)}); err != nil {
			return PrivateLeaf{}, RetiredPages{}, err
		}
		hasParent = true
		parentPage = activePage
		parentIndex = index
		pageNumber = child
		expectedLevel = header.Level - 1
		checkLevel = true
		work.TreeDescent(1)
	}
}

// PrivatePath descends from the root, privatizing every committed page
// along the path for the draft transaction, and returns the private leaf
// plus the lower-bound selection of key (Rust private_path + KeySelector).
func PrivatePath[T any](codec Codec[T], store Store, root *uint32, key Key, retired RetiredPages) (PrivateLeaf, RetiredPages, error) {
	leaf, retired, err := privatePathSelect(codec, store, root, key, retired)
	if err != nil {
		return PrivateLeaf{}, RetiredPages{}, err
	}
	header := leaf.Header
	index, exists, err := lowerBound(codec, leaf.Page, &header, key, true)
	if err != nil {
		return PrivateLeaf{}, RetiredPages{}, err
	}
	return PrivateLeaf{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Page: leaf.Page, Index: index, Exists: exists}, retired, nil
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
	header, err := parse(codec, page, targetTxn, 0, false)
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
	var replacement CellBuf
	if err := newBranchCell(codec, key, child, &replacement); err != nil {
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
