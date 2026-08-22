// Fixed-tree deletion without occupancy rebalancing (Rust
// fixed_tree/delete.rs).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Following carries the first surviving leaf value after a removed run.
type Following[T any] struct {
	Key  Key
	Leaf T
}

// RemovedRun reports the outcome of one leaf-run removal.
type RemovedRun[T any] struct {
	Removed   uint64
	Following *Following[T]
}

// Target identifies one deletion position in a private leaf.
type Target struct {
	Path       Path
	PageNumber uint32
	Header     Header
	Index      int
}

// DeleteExisting deletes the existing key from the tree (Rust
// delete_existing); the caller must have proven the key exists.
func DeleteExisting[T any](codec Codec[T], store Store, root *uint32, key Key, retired RetiredPages) (RetiredPages, error) {
	leaf, retired, err := PrivatePath(codec, store, root, key, retired)
	if err != nil {
		return RetiredPages{}, err
	}
	if !leaf.Exists {
		return RetiredPages{}, corrupt("B+tree key disappeared during deletion")
	}
	return retired, DeleteTarget(codec, store, root, &Target{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: leaf.Index})
}

// RemoveLeafRun removes every consecutive leaf record from index of the
// key's private leaf while the include predicate holds, mirroring Rust
// remove_leaf_run. Fixed-size leaf cells are required. include is a
// direct function parameter: the caller's closure stays on the stack.
func RemoveLeafRun[T any](codec Codec[T], store Store, root *uint32, key Key, include func(T) (bool, error)) (RemovedRun[T], error) {
	leaf, retired, err := PrivatePath(codec, store, root, key, RetiredPages{})
	if err != nil {
		return RemovedRun[T]{}, err
	}
	if retired.Len() != 0 {
		return RemovedRun[T]{}, corrupt("private B+tree run retired a page")
	}
	if !leaf.Exists {
		return RemovedRun[T]{}, corrupt("B+tree run start key is missing")
	}
	index := leaf.Index
	var following *Following[T]
	end := index
	for end < int(leaf.Header.ItemCount) {
		cell, err := codecCell(codec, leaf.Page, &leaf.Header, end)
		if err != nil {
			return RemovedRun[T]{}, err
		}
		item, err := codec.ReadLeaf(cell)
		if err != nil {
			return RemovedRun[T]{}, err
		}
		keep, err := include(item)
		if err != nil {
			return RemovedRun[T]{}, err
		}
		if !keep {
			key, err := codec.ReadKey(cell, 0)
			if err != nil {
				return RemovedRun[T]{}, err
			}
			following = &Following[T]{Key: key, Leaf: item}
			return RemovedRun[T]{Removed: 0, Following: following}, nil
		}
		end++
	}
	if end == index {
		return RemovedRun[T]{Removed: 0, Following: following}, nil
	}
	if following == nil {
		if adjacent, found, err := adjacentLeaf(codec, store, leaf.Path, AdjacentAfter); err != nil {
			return RemovedRun[T]{}, err
		} else if found {
			following = &Following[T]{Key: adjacent.key, Leaf: adjacent.leaf}
		}
	}
	removed := uint64(end - index)
	if end-index == int(leaf.Header.ItemCount) {
		if err := store.DiscardPrivate(leaf.PageNumber); err != nil {
			return RemovedRun[T]{}, err
		}
		if err := removeEmptyChild(codec, store, root, &leaf.Path); err != nil {
			return RemovedRun[T]{}, err
		}
	} else {
		cellLen, ok := FixedCellSize(codec, 0)
		if !ok {
			return RemovedRun[T]{}, unsupported("B+tree run removal requires fixed leaf cells")
		}
		page, tag, err := store.Update(leaf.PageNumber)
		if err != nil {
			return RemovedRun[T]{}, err
		}
		if _, err := format.SlottedRemoveFixedRange(page, &leaf.Header, index, end-index, cellLen); err != nil {
			return RemovedRun[T]{}, err
		}
		if err := store.RestoreDirty(leaf.PageNumber, tag); err != nil {
			return RemovedRun[T]{}, err
		}
		if index == 0 {
			first, err := FirstKey(codec, store, leaf.PageNumber, 0)
			if err != nil {
				return RemovedRun[T]{}, err
			}
			if err := PropagateFirst(codec, store, root, &leaf.Path, first); err != nil {
				return RemovedRun[T]{}, err
			}
		}
	}
	return RemovedRun[T]{Removed: removed, Following: following}, nil
}

// DeleteTarget deletes one record from a private leaf, collapsing the tree
// when the leaf or an ancestor becomes empty (Rust delete_target).
func DeleteTarget[T any](codec Codec[T], store Store, root *uint32, target *Target) error {
	if target.Header.ItemCount > 1 {
		return removeLeafRecord(codec, store, root, target)
	}
	if err := store.DiscardPrivate(target.PageNumber); err != nil {
		return err
	}
	return removeEmptyChild(codec, store, root, &target.Path)
}

func removeLeafRecord[T any](codec Codec[T], store Store, root *uint32, target *Target) error {
	page, tag, err := store.Update(target.PageNumber)
	if err != nil {
		return err
	}
	if err := removeAt(codec, page, &target.Header, target.Index); err != nil {
		return err
	}
	if err := store.RestoreDirty(target.PageNumber, tag); err != nil {
		return err
	}
	if target.Index != 0 {
		return nil
	}
	first, err := FirstKey(codec, store, target.PageNumber, 0)
	if err != nil {
		return err
	}
	return PropagateFirst(codec, store, root, &target.Path, first)
}

func removeEmptyChild[T any](codec Codec[T], store Store, root *uint32, path *Path) error {
	depth := path.Depth()
	if depth == 0 {
		*root = 0
		return nil
	}
	for depth > 0 {
		depth--
		frame := path.Frame(depth)
		targetTxn := store.TargetTxn()
		page, err := store.Inspect(frame.PageNumber)
		if err != nil {
			return err
		}
		header, err := parse(codec, page, targetTxn, 0, false)
		if err != nil {
			return err
		}
		if header.ItemCount == 1 {
			if err := store.DiscardPrivate(frame.PageNumber); err != nil {
				return err
			}
			if depth == 0 {
				*root = 0
				return nil
			}
			continue
		}
		page, tag, err := store.Update(frame.PageNumber)
		if err != nil {
			return err
		}
		if err := removeAt(codec, page, &header, frame.Index); err != nil {
			return err
		}
		if err := store.RestoreDirty(frame.PageNumber, tag); err != nil {
			return err
		}
		outputCount := int(header.ItemCount) - 1
		if depth == 0 && outputCount == 1 {
			page, err := store.Inspect(frame.PageNumber)
			if err != nil {
				return err
			}
			output, err := parse(codec, page, targetTxn, header.Level, true)
			if err != nil {
				return err
			}
			child, err := branchChild(codec, page, &output, 0, store.PageLimit())
			if err != nil {
				return err
			}
			*root = child
			return store.DiscardPrivate(frame.PageNumber)
		}
		if frame.Index == 0 {
			first, err := FirstKey(codec, store, frame.PageNumber, header.Level)
			if err != nil {
				return err
			}
			if err := propagateFirstFrom(codec, store, root, path, depth, first); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func removeAt[T any](codec Codec[T], page []byte, header *Header, index int) error {
	oldCell, err := codecCell(codec, page, header, index)
	if err != nil {
		return err
	}
	return format.SlottedRemove(page, header, index, len(oldCell))
}
