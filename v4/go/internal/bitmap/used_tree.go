// Sparse-bitmap structural edits (Rust used_bitmap/mutation/tree.rs):
// root growth, subtree creation, summary propagation, and empty-path
// removal.

package bitmap

import (
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// propagate recomputes every ancestor candidate summary of one changed
// child by inspecting the child subtree (Rust propagate).
func propagate(store tree.Store, frames []usedFrame, childPage uint32, childBase uint64, limit uint64, kind Kind) error {
	return propagateInner(store, frames, childPage, childBase, limit, kind, nil)
}

// propagateKnown updates every ancestor candidate summary with one known
// candidate value (Rust propagate_known). The known value applies to the
// first (deepest) frame; ancestors are recomputed.
func propagateKnown(store tree.Store, frames []usedFrame, childPage uint32, childBase uint64, limit uint64, kind Kind, candidate bool) error {
	return propagateInner(store, frames, childPage, childBase, limit, kind, &candidate)
}

func propagateInner(store tree.Store, frames []usedFrame, childPage uint32, childBase uint64, limit uint64, kind Kind, known *bool) error {
	depth := len(frames)
	for depth > 0 {
		depth--
		frame := frames[depth]
		parentBase, err := parentBase(frame)
		if err != nil {
			return err
		}
		candidate := false
		if known != nil {
			candidate = *known
			known = nil
		} else {
			candidate, err = subtreeHasCandidate(store, childPage, kind, childBase, limit)
			if err != nil {
				return err
			}
		}
		targetTxn := store.TargetTxn()
		page, tag, err := store.Update(frame.pageNumber)
		if err != nil {
			return err
		}
		header, err := InspectHeader(page, targetTxn, kind, &frame.level)
		if err != nil {
			return err
		}
		child, err := BranchChild(page, frame.childIndex)
		if err != nil {
			return err
		}
		if err := setBranchChild(page, header, frame.childIndex, child, candidate); err != nil {
			return err
		}
		if err := store.RestoreDirty(frame.pageNumber, tag); err != nil {
			return err
		}
		childPage = frame.pageNumber
		childBase = parentBase
	}
	return nil
}

// removeEmptyPath drops the emptied child from every ancestor and
// collapses the root when the whole tree is empty (Rust remove_empty_path).
func removeEmptyPath(store tree.Store, root *uint32, path *editPath, limit uint64, kind Kind) error {
	depth := path.depth
	for depth > 0 {
		depth--
		frame := path.frames[depth]
		parentBase, err := parentBase(frame)
		if err != nil {
			return err
		}
		span, err := Coverage(frame.level - 1)
		if err != nil {
			return err
		}
		candidate := coverageIntersects(frame.childBase, span, kind.FirstCandidate(), limit)
		targetTxn := store.TargetTxn()
		page, tag, err := store.Update(frame.pageNumber)
		if err != nil {
			return err
		}
		header, err := InspectHeader(page, targetTxn, kind, &frame.level)
		if err != nil {
			return err
		}
		remaining, err := ReplaceBranchChild(page, header, frame.childIndex, 0, candidate)
		if err != nil {
			return err
		}
		if err := store.RestoreDirty(frame.pageNumber, tag); err != nil {
			return err
		}
		if remaining != 0 {
			return propagate(store, path.frames[:depth], frame.pageNumber, parentBase, limit, kind)
		}
		if err := store.DiscardPrivate(frame.pageNumber); err != nil {
			return err
		}
	}
	*root = 0
	return nil
}

// parentBase reconstructs the parent bit base of one frame (Rust
// parent_base).
func parentBase(frame usedFrame) (uint64, error) {
	offset, err := Coverage(frame.level - 1)
	if err != nil {
		return 0, err
	}
	offset *= uint64(frame.childIndex)
	if frame.childBase < offset {
		return 0, corrupt("used bitmap child base underflows")
	}
	return frame.childBase - offset, nil
}

// growRoot raises the used bitmap root until its level covers the limit
// (Rust grow_root).
func growUsedRoot(store tree.Store, root *uint32, kind Kind, required uint16, limit uint64) error {
	targetTxn := store.TargetTxn()
	page, err := store.Inspect(*root)
	if err != nil {
		return err
	}
	header, err := InspectHeader(page, targetTxn, kind, nil)
	if err != nil {
		return err
	}
	level := header.Level
	if level > required {
		return corrupt("used bitmap root level is too high")
	}
	for level < required {
		candidate, err := subtreeHasCandidate(store, *root, kind, 0, limit)
		if err != nil {
			return err
		}
		parent, err := store.Allocate()
		if err != nil {
			return err
		}
		child := *root
		nextLevel := level + 1
		span, err := Coverage(level)
		if err != nil {
			return err
		}
		page, tag, err := store.Update(parent)
		if err != nil {
			return err
		}
		Initialize(page, targetTxn, nextLevel, kind)
		if err := initializeSummary(page, 0, span, kind.FirstCandidate(), limit); err != nil {
			return err
		}
		if err := setBranchChild(page, Header{Level: nextLevel}, 0, child, candidate); err != nil {
			return err
		}
		if err := store.RestoreDirty(parent, tag); err != nil {
			return err
		}
		*root = parent
		level = nextLevel
	}
	return nil
}

// newSubtree builds the single-path subtree that contains bit (Rust
// new_subtree). The leaf word is stamped and every branch summary is
// precomputed for the whole span.
func newUsedSubtree(store tree.Store, kind Kind, level uint16, base uint64, limit uint64, bit uint32) (uint32, error) {
	if level == 0 {
		pageNumber, err := store.Allocate()
		if err != nil {
			return 0, err
		}
		txn := store.TargetTxn()
		page, tag, err := store.Update(pageNumber)
		if err != nil {
			return 0, err
		}
		Initialize(page, txn, 0, kind)
		if err := SetLeafWord(page, LeafWordIndex(bit), uint64(1)<<(uint64(bit)%64)); err != nil {
			return 0, err
		}
		stampLeaf(page, 1)
		if err := store.RestoreDirty(pageNumber, tag); err != nil {
			return 0, err
		}
		return pageNumber, nil
	}
	span, err := Coverage(level - 1)
	if err != nil {
		return 0, err
	}
	index, err := ChildIndex(bit, level)
	if err != nil {
		return 0, err
	}
	childBase, err := childBaseAt(base, span, index)
	if err != nil {
		return 0, err
	}
	child, err := newUsedSubtree(store, kind, level-1, childBase, limit, bit)
	if err != nil {
		return 0, err
	}
	candidate, err := subtreeHasCandidate(store, child, kind, childBase, limit)
	if err != nil {
		return 0, err
	}
	pageNumber, err := store.Allocate()
	if err != nil {
		return 0, err
	}
	txn := store.TargetTxn()
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return 0, err
	}
	Initialize(page, txn, level, kind)
	if err := initializeSummary(page, base, span, kind.FirstCandidate(), limit); err != nil {
		return 0, err
	}
	if err := setBranchChild(page, Header{Level: level}, index, child, candidate); err != nil {
		return 0, err
	}
	if err := store.RestoreDirty(pageNumber, tag); err != nil {
		return 0, err
	}
	return pageNumber, nil
}

// initializeSummary stamps every summary bit of a fresh branch from the
// coverage intersection of its child span (Rust initialize_summary).
func initializeSummary(page []byte, base, span uint64, first uint64, limit uint64) error {
	for index := 0; index < BranchChildren; index++ {
		childBase := base + span*uint64(index)
		if err := SetSummary(page, index, coverageIntersects(childBase, span, first, limit)); err != nil {
			return err
		}
	}
	return nil
}

// coverageIntersects reports whether [base, base+span) and [first, limit)
// overlap (Rust coverage_intersects).
func coverageIntersects(base, span uint64, first uint64, limit uint64) bool {
	lo := base
	if first > lo {
		lo = first
	}
	hi := base + span
	if hi < base {
		hi = ^uint64(0)
	}
	if limit < hi {
		hi = limit
	}
	return lo < hi
}

// setPointer rewrites one child keeping its current summary bit (Rust
// set_pointer).
func setPointer(page []byte, header Header, index int, child uint32) error {
	candidate, err := SummaryBit(page, index)
	if err != nil {
		return err
	}
	return setBranchChild(page, header, index, child, candidate)
}

// setBranchChild writes one child and its summary bit (Rust
// used_bitmap/page.rs set_branch_child).
func setBranchChild(page []byte, header Header, index int, child uint32, candidate bool) error {
	_, err := ReplaceBranchChild(page, header, index, child, candidate)
	return err
}
