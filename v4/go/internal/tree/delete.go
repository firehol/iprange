// Fixed-tree deletion without occupancy rebalancing (Rust
// fixed_tree/delete.rs).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Following carries the first surviving leaf value after a removed run.
type Following struct {
	Key  Key
	Leaf any
}

// RemovedRun reports the outcome of one leaf-run removal.
type RemovedRun struct {
	Removed   uint64
	Following *Following
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
func DeleteExisting(codec Codec, store Store, root *uint32, key Key, retired *RetiredPages) error {
	leaf, err := PrivatePath(codec, store, root, key, retired)
	if err != nil {
		return err
	}
	if !leaf.Exists {
		return corrupt("B+tree key disappeared during deletion")
	}
	return DeleteTarget(codec, store, root, &Target{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: leaf.Index})
}

// RemoveLeafRun removes every consecutive leaf record from index of the
// key's private leaf while the include predicate holds, mirroring Rust
// remove_leaf_run. Fixed-size leaf cells are required.
func RemoveLeafRun(codec Codec, store Store, root *uint32, key Key, include func(leaf any) (bool, error)) (*RemovedRun, error) {
	retired := NewRetiredPages()
	leaf, err := PrivatePath(codec, store, root, key, retired)
	if err != nil {
		return nil, err
	}
	if retired.Len() != 0 {
		return nil, corrupt("private B+tree run retired a page")
	}
	if !leaf.Exists {
		return nil, corrupt("B+tree run start key is missing")
	}
	index := leaf.Index
	var end int
	var following *Following
	if err := store.Inspect(leaf.PageNumber, func(page []byte) error {
		end = index
		for end < int(leaf.Header.ItemCount) {
			cell, err := codecCell(codec, page, &leaf.Header, end)
			if err != nil {
				return err
			}
			item, err := codec.ReadLeaf(cell)
			if err != nil {
				return err
			}
			keep, err := include(item)
			if err != nil {
				return err
			}
			if !keep {
				key, err := codec.ReadKey(cell, 0)
				if err != nil {
					return err
				}
				following = &Following{Key: key, Leaf: item}
				return nil
			}
			end++
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if end == index {
		return &RemovedRun{Removed: 0, Following: following}, nil
	}
	if following == nil {
		if adjacent, err := adjacentLeaf(codec, store, &leaf.Path, AdjacentAfter); err != nil {
			return nil, err
		} else if adjacent != nil {
			following = &Following{Key: adjacent.key, Leaf: adjacent.leaf}
		}
	}
	removed := uint64(end - index)
	if end-index == int(leaf.Header.ItemCount) {
		if err := store.DiscardPrivate(leaf.PageNumber); err != nil {
			return nil, err
		}
		if err := removeEmptyChild(codec, store, root, &leaf.Path); err != nil {
			return nil, err
		}
	} else {
		cellLen, ok := FixedCellSize(codec, 0)
		if !ok {
			return nil, unsupported("B+tree run removal requires fixed leaf cells")
		}
		if err := store.Update(leaf.PageNumber, func(page []byte) error {
			_, err := format.SlottedRemoveFixedRange(page, &leaf.Header, index, end-index, cellLen)
			return err
		}); err != nil {
			return nil, err
		}
		if index == 0 {
			first, err := FirstKey(codec, store, leaf.PageNumber, 0)
			if err != nil {
				return nil, err
			}
			if err := PropagateFirst(codec, store, root, &leaf.Path, first); err != nil {
				return nil, err
			}
		}
	}
	return &RemovedRun{Removed: removed, Following: following}, nil
}

// DeleteTarget deletes one record from a private leaf, collapsing the tree
// when the leaf or an ancestor becomes empty (Rust delete_target).
func DeleteTarget(codec Codec, store Store, root *uint32, target *Target) error {
	if target.Header.ItemCount > 1 {
		return removeLeafRecord(codec, store, root, target)
	}
	if err := store.DiscardPrivate(target.PageNumber); err != nil {
		return err
	}
	return removeEmptyChild(codec, store, root, &target.Path)
}

func removeLeafRecord(codec Codec, store Store, root *uint32, target *Target) error {
	if err := store.Update(target.PageNumber, func(page []byte) error {
		return removeAt(codec, page, &target.Header, target.Index)
	}); err != nil {
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

func removeEmptyChild(codec Codec, store Store, root *uint32, path *Path) error {
	depth := path.Depth()
	if depth == 0 {
		*root = 0
		return nil
	}
	for depth > 0 {
		depth--
		frame := path.Frame(depth)
		targetTxn := store.TargetTxn()
		var header *Header
		if err := store.Inspect(frame.PageNumber, func(page []byte) error {
			h, err := parse(codec, page, targetTxn, nil)
			if err != nil {
				return err
			}
			header = h
			return nil
		}); err != nil {
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
		if err := store.Update(frame.PageNumber, func(page []byte) error {
			return removeAt(codec, page, header, frame.Index)
		}); err != nil {
			return err
		}
		outputCount := int(header.ItemCount) - 1
		if depth == 0 && outputCount == 1 {
			var child uint32
			if err := store.Inspect(frame.PageNumber, func(page []byte) error {
				output, err := parse(codec, page, targetTxn, &header.Level)
				if err != nil {
					return err
				}
				c, err := branchChild(codec, page, output, 0, store.PageLimit())
				if err != nil {
					return err
				}
				child = c
				return nil
			}); err != nil {
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

func removeAt(codec Codec, page []byte, header *Header, index int) error {
	oldCell, err := codecCell(codec, page, header, index)
	if err != nil {
		return err
	}
	return format.SlottedRemove(page, header, index, len(oldCell))
}
