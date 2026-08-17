// Read-only lowest-zero and exact-bit searches (Rust
// used_bitmap/search.rs).

package bitmap

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/tree"
)

// findLowest returns the lowest unused ID of the namespace at or above the
// kind's first candidate (Rust find_lowest). An absent subtree means the
// hole is its first candidate bit.
func findLowest(store tree.Store, root uint32, limit uint64, kind Kind) (uint32, bool, error) {
	if kind.FirstCandidate() >= limit {
		return 0, false, nil
	}
	if root == 0 {
		return uint32(kind.FirstCandidate()), true, nil
	}
	pageNumber := root
	level, err := RequiredLevel(limit)
	if err != nil {
		return 0, false, err
	}
	base := uint64(0)
	start := kind.FirstCandidate()
	selected := false
	for {
		targetTxn := store.TargetTxn()
		pageLimit := store.PageLimit()
		found := uint32(0)
		missing := uint32(0)
		child := uint32(0)
		nextBase := uint64(0)
		step := 0 // 0 = leaf result, 1 = finished, 2 = descend
		if err := store.Inspect(pageNumber, func(page []byte) error {
			header, err := InspectHeader(page, targetTxn, kind, &level)
			if err != nil {
				return err
			}
			if level == 0 {
				bit, ok, err := lowestLeaf(page, base, start, limit)
				if err != nil {
					return err
				}
				if !ok && selected {
					return corrupt("used bitmap summary has no candidate")
				}
				if !ok {
					step = 1
					return nil
				}
				found = uint32(bit)
				step = 0
				return nil
			}
			span, err := Coverage(level - 1)
			if err != nil {
				return err
			}
			first := 0
			if start > base {
				first = int((start - base) / span)
			}
			index, okSummary, err := FirstSummary(page, first)
			if err != nil {
				return err
			}
			if !okSummary {
				if selected {
					return corrupt("used bitmap summary has no candidate")
				}
				step = 1
				return nil
			}
			childBase, err := childBaseAt(base, span, index)
			if err != nil {
				return err
			}
			if childBase >= limit {
				return corrupt("used bitmap candidate is outside its limit")
			}
			c, err := CheckedBranchChild(page, header, index, pageLimit)
			if err != nil {
				return err
			}
			if c == 0 {
				step = 1
				missing = uint32(start)
				if childBase > start {
					missing = uint32(childBase)
				}
				return nil
			}
			step = 2
			child = c
			nextBase = childBase
			nextStart := start
			if childBase > start {
				nextStart = childBase
			}
			_ = nextStart
			return nil
		}); err != nil {
			return 0, false, err
		}
		switch step {
		case 0:
			return found, true, nil
		case 1:
			if level == 0 {
				return 0, false, nil
			}
			return missing, true, nil
		}
		pageNumber = child
		level--
		base = nextBase
		if nextBase > start {
			start = nextBase
		}
		selected = true
	}
}

// contains reports whether the ID bit is set (Rust contains).
func contains(store tree.Store, root uint32, limit uint64, kind Kind, bit uint32) (bool, error) {
	if root == 0 {
		return false, nil
	}
	pageNumber := root
	level, err := RequiredLevel(limit)
	if err != nil {
		return false, err
	}
	base := uint64(0)
	for {
		targetTxn := store.TargetTxn()
		pageLimit := store.PageLimit()
		result := false
		missing := false
		child := uint32(0)
		nextBase := uint64(0)
		if err := store.Inspect(pageNumber, func(page []byte) error {
			header, err := InspectHeader(page, targetTxn, kind, &level)
			if err != nil {
				return err
			}
			if level == 0 {
				word, err := LeafWord(page, LeafWordIndex(bit))
				if err != nil {
					return err
				}
				result = word&(uint64(1)<<(uint64(bit)%64)) != 0
				return nil
			}
			span, err := Coverage(level - 1)
			if err != nil {
				return err
			}
			index, err := ChildIndex(bit, level)
			if err != nil {
				return err
			}
			nextBase, err = childBaseAt(base, span, index)
			if err != nil {
				return err
			}
			c, err := CheckedBranchChild(page, header, index, pageLimit)
			if err != nil {
				return err
			}
			if c == 0 {
				missing = true
				return nil
			}
			child = c
			return nil
		}); err != nil {
			return false, err
		}
		if missing {
			return false, nil
		}
		if level == 0 {
			return result, nil
		}
		pageNumber = child
		base = nextBase
		level--
	}
}

// ReadWords copies the used-bitmap words of the namespace into output,
// zeroing every word whose subtree is absent (Rust read_words).
func ReadWords(store tree.Store, root uint32, limit uint64, kind Kind, start uint32, output []uint64) error {
	end, err := requireWordRange(limit, start, len(output))
	if err != nil {
		return err
	}
	clear(output)
	if _, err := RequiredLevel(limit); err != nil {
		return err
	}
	if root == 0 || len(output) == 0 {
		return nil
	}
	at := uint64(start)
	for at < end {
		base := at * 64
		leafBase := base / LeafBits * LeafBits
		within := int((base - leafBase) / 64)
		offset := int(at - uint64(start))
		count := len(output) - offset
		if count > int(LeafBits/64)-within {
			count = int(LeafBits/64) - within
		}
		if pageNumber, ok, err := findLeaf(store, root, limit, kind, base, leafBase); err != nil {
			return err
		} else if ok {
			if err := store.Inspect(pageNumber, func(page []byte) error {
				for index := 0; index < count; index++ {
					word, err := LeafWord(page, within+index)
					if err != nil {
						return err
					}
					output[offset+index] = word
				}
				return nil
			}); err != nil {
				return err
			}
		}
		at += uint64(count)
	}
	maskLastWord(start, limit, output)
	return nil
}

func requireWordRange(limit uint64, start uint32, count int) (uint64, error) {
	if uint64(count) > ^uint64(0)-uint64(start) {
		return 0, corrupt("used bitmap word range")
	}
	end := uint64(start) + uint64(count)
	if limit > ^uint64(0)-63 {
		return 0, corrupt("used bitmap word limit")
	}
	wordLimit := (limit + 63) / 64
	if end > wordLimit {
		return 0, invalid("used bitmap word range exceeds its limit")
	}
	return end, nil
}

// findLeaf locates the leaf page covering one word base, without reading
// the words (Rust find_leaf). ok=false means the subtree is absent.
func findLeaf(store tree.Store, root uint32, limit uint64, kind Kind, base, leafBase uint64) (uint32, bool, error) {
	if base >= 1<<32 {
		return 0, false, corrupt("used bitmap word is invalid")
	}
	bit := uint32(base)
	pageNumber := root
	level, err := RequiredLevel(limit)
	if err != nil {
		return 0, false, err
	}
	pageBase := uint64(0)
	for {
		targetTxn := store.TargetTxn()
		pageLimit := store.PageLimit()
		child := uint32(0)
		isLeaf := false
		if err := store.Inspect(pageNumber, func(page []byte) error {
			header, err := InspectHeader(page, targetTxn, kind, &level)
			if err != nil {
				return err
			}
			if level == 0 {
				if pageBase != leafBase {
					return corrupt("used bitmap leaf coverage is invalid")
				}
				isLeaf = true
				return nil
			}
			span, err := Coverage(level - 1)
			if err != nil {
				return err
			}
			index, err := ChildIndex(bit, level)
			if err != nil {
				return err
			}
			nextBase, err := childBaseAt(pageBase, span, index)
			if err != nil {
				return err
			}
			c, err := CheckedBranchChild(page, header, index, pageLimit)
			if err != nil {
				return err
			}
			if c == 0 {
				return nil // absent subtree
			}
			child = c
			pageBase = nextBase
			return nil
		}); err != nil {
			return 0, false, err
		}
		if isLeaf {
			return pageNumber, true, nil
		}
		if child == 0 {
			return 0, false, nil
		}
		pageNumber = child
		level--
	}
}

func maskLastWord(start uint32, limit uint64, output []uint64) {
	tail := limit % 64
	if tail == 0 || len(output) == 0 {
		return
	}
	last := uint32(limit / 64)
	if last >= start && uint64(last-start) < uint64(len(output)) {
		output[last-start] &= (uint64(1) << tail) - 1
	}
}

// greatest returns the highest set ID of the namespace (Rust greatest).
func greatest(store tree.Store, root uint32, limit uint64, kind Kind) (uint32, bool, error) {
	if root == 0 {
		return 0, false, nil
	}
	pageNumber := root
	level, err := RequiredLevel(limit)
	if err != nil {
		return 0, false, err
	}
	base := uint64(0)
	for {
		targetTxn := store.TargetTxn()
		pageLimit := store.PageLimit()
		child := uint32(0)
		nextBase := uint64(0)
		isLeaf := false
		if err := store.Inspect(pageNumber, func(page []byte) error {
			header, err := InspectHeader(page, targetTxn, kind, &level)
			if err != nil {
				return err
			}
			if level == 0 {
				isLeaf = true
				return nil
			}
			span, err := Coverage(level - 1)
			if err != nil {
				return err
			}
			for index := BranchChildren - 1; index >= 0; index-- {
				c, err := CheckedBranchChild(page, header, index, pageLimit)
				if err != nil {
					return err
				}
				if c != 0 {
					child = c
					nextBase, err = childBaseAt(base, span, index)
					if err != nil {
						return err
					}
					return nil
				}
			}
			return corrupt("used bitmap branch has no child")
		}); err != nil {
			return 0, false, err
		}
		if isLeaf {
			var bit uint32
			if err := store.Inspect(pageNumber, func(page []byte) error {
				b, ok, err := greatestLeaf(page, base, limit)
				if err != nil {
					return err
				}
				if !ok {
					return corrupt("used bitmap leaf has no set bit")
				}
				bit = b
				return nil
			}); err != nil {
				return 0, false, err
			}
			return bit, true, nil
		}
		pageNumber = child
		base = nextBase
		level--
	}
}

// greatestLeaf returns the highest set bit of one leaf below limit (Rust
// greatest_leaf).
func greatestLeaf(page []byte, base uint64, limit uint64) (uint32, bool, error) {
	for index := LeafWords - 1; index >= 0; index-- {
		wordBase := base + uint64(index)*64
		if wordBase >= limit {
			continue
		}
		word, err := LeafWord(page, index)
		if err != nil {
			return 0, false, err
		}
		if limit-wordBase < 64 {
			word &= (uint64(1) << (limit - wordBase)) - 1
		}
		if word != 0 {
			return uint32(wordBase + uint64(63-bits.LeadingZeros64(word))), true, nil
		}
	}
	return 0, false, nil
}
