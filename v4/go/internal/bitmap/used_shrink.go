// Membership namespace shrink after trailing IDs disappear (Rust
// used_bitmap/shrink.rs).

package bitmap

import (
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// ShrinkMembership lowers the membership ID limit to the highest used ID
// plus one, dropping now-empty trailing subtrees (Rust shrink_membership).
func ShrinkMembership(store tree.RetiringStore, root *uint32, oldLimit uint64) (uint64, error) {
	return shrink(store, root, oldLimit, KindMembership)
}

// ShrinkStructure lowers the structure ID limit (Rust shrink_structure).
func ShrinkStructure(store tree.RetiringStore, root *uint32, oldLimit uint64) (uint64, error) {
	return shrink(store, root, oldLimit, KindStructure)
}

func shrink(store tree.RetiringStore, root *uint32, oldLimit uint64, kind Kind) (uint64, error) {
	id, ok, err := greatest(store, *root, oldLimit, kind)
	if err != nil {
		return 0, err
	}
	newLimit := uint64(1)
	if ok {
		newLimit = uint64(id) + 1
	}
	if newLimit == oldLimit || *root == 0 {
		return newLimit, nil
	}
	var retired tree.RetiredPages
	if err := trimRoot(store, root, oldLimit, newLimit, kind, &retired); err != nil {
		return 0, err
	}
	if *root != 0 {
		required, err := RequiredLevel(newLimit)
		if err != nil {
			return 0, err
		}
		refreshed, err := refreshPage(store, *root, required, 0, newLimit, kind, &retired)
		if err != nil {
			return 0, err
		}
		*root = refreshed
	}
	if err := store.RetirePages(retired); err != nil {
		return 0, err
	}
	return newLimit, nil
}

// trimRoot drops whole root levels that lie entirely above the new limit,
// keeping only the retained child (Rust trim_root).
func trimRoot(store tree.Store, root *uint32, oldLimit, newLimit uint64, kind Kind, retired *tree.RetiredPages) error {
	level, err := RequiredLevel(oldLimit)
	if err != nil {
		return err
	}
	required, err := RequiredLevel(newLimit)
	if err != nil {
		return err
	}
	for level > required {
		private, header, err := touch(store, *root, kind, level, 0, oldLimit, retired)
		if err != nil {
			return err
		}
		*root = private
		pageLimit := store.PageLimit()
		page, err := store.Inspect(private)
		if err != nil {
			return err
		}
		child, err := CheckedBranchChild(page, header, 0, pageLimit)
		if err != nil {
			return err
		}
		if child == 0 {
			return corrupt("used bitmap root has no retained child")
		}
		for index := 1; index < BranchChildren; index++ {
			extra, err := CheckedBranchChild(page, header, index, pageLimit)
			if err != nil {
				return err
			}
			if extra != 0 {
				return corrupt("used bitmap root has data above its new limit")
			}
		}
		if err := store.DiscardPrivate(private); err != nil {
			return err
		}
		*root = child
		level--
	}
	return nil
}

// refreshPage re-touches one page into the draft and recursively prunes
// children whose span lies above the new limit (Rust refresh_page).
func refreshPage(store tree.Store, pageNumber uint32, level uint16, base uint64, limit uint64, kind Kind, retired *tree.RetiredPages) (uint32, error) {
	private, header, err := touch(store, pageNumber, kind, level, base, limit, retired)
	if err != nil {
		return 0, err
	}
	if level == 0 {
		return private, nil
	}
	span, err := Coverage(level - 1)
	if err != nil {
		return 0, err
	}
	for index := 0; index < BranchChildren; index++ {
		if err := refreshChild(store, private, header, index, level, base, span, limit, kind, retired); err != nil {
			return 0, err
		}
	}
	return private, nil
}

func refreshChild(store tree.Store, pageNumber uint32, header Header, index int, level uint16, base uint64, span uint64, limit uint64, kind Kind, retired *tree.RetiredPages) error {
	childBase, err := childBaseAt(base, span, index)
	if err != nil {
		return err
	}
	pageLimit := store.PageLimit()
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	child, err := CheckedBranchChild(page, header, index, pageLimit)
	if err != nil {
		return err
	}
	if childBase >= limit {
		if child != 0 {
			return corrupt("used bitmap has data above its new limit")
		}
		page, tag, err := store.Update(pageNumber)
		if err != nil {
			return err
		}
		if err := setBranchChild(page, header, index, 0, false); err != nil {
			return err
		}
		return store.RestoreDirty(pageNumber, tag)
	}
	if childBase+span <= limit {
		return nil
	}
	if child == 0 {
		page, tag, err := store.Update(pageNumber)
		if err != nil {
			return err
		}
		if err := setBranchChild(page, header, index, 0, true); err != nil {
			return err
		}
		return store.RestoreDirty(pageNumber, tag)
	}
	refreshed, err := refreshPage(store, child, level-1, childBase, limit, kind, retired)
	if err != nil {
		return err
	}
	candidate, err := subtreeHasCandidate(store, refreshed, kind, childBase, limit)
	if err != nil {
		return err
	}
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	if err := setBranchChild(page, header, index, refreshed, candidate); err != nil {
		return err
	}
	return store.RestoreDirty(pageNumber, tag)
}
