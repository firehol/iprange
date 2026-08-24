package validation

// Bitmap containment query of the catalog cross-check (Rust
// validation/bitmap/query.rs): one root-to-leaf probe over the raw
// mapping, outside the graph claims, re-verifying the bitmap header at
// every step. Any header change or decode defect is the Corrupt class,
// which the cross-check folds into a bijection mismatch.

import (
	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// bitmapContains reports whether one bit is set (Rust bitmap::contains):
// a zero root or an out-of-range bit is absent, and the walked header is
// re-inspected at every level (Rust require_header).
func bitmapContains(ctx *context, root uint32, limit uint64, kind bitmap.Kind, bit uint32) (bool, error) {
	if root == 0 || !bitmapBitInRange(kind, bit, limit) {
		return false, nil
	}
	pageNumber := root
	level, err := bitmap.RequiredLevel(limit)
	if err != nil {
		return false, formatError(err)
	}
	base := uint64(0)
	for {
		page, err := ctx.mapping.Page(pageNumber)
		if err != nil {
			return false, err
		}
		header, problem := bitmap.CheckedHeader(page, ctx.meta.TxnID, kind, &level)
		if problem != bitmap.HeaderProblemNone {
			return false, &format.Error{Code: format.CodeFormatInvalid, Detail: "validated bitmap changed during cross-check"}
		}
		if header.Level == 0 {
			return bitmapQueryLeaf(page, bit, base), nil
		}
		child, childBase, ok, err := bitmapQueryChild(page, bit, base, header.Level)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		pageNumber = child
		base = childBase
		level = header.Level - 1
	}
}

// bitmapBitInRange mirrors Rust bit_in_range: the bit must sit inside
// the kind's candidate window and below the limit.
func bitmapBitInRange(kind bitmap.Kind, bit uint32, limit uint64) bool {
	return uint64(bit) >= kind.FirstCandidate() && uint64(bit) < limit
}

// bitmapQueryLeaf tests one bit in one leaf page (Rust query_leaf).
func bitmapQueryLeaf(page []byte, bit uint32, base uint64) bool {
	local := uint64(bit) - base
	word, err := bitmap.LeafWord(page, int(local/64))
	if err != nil {
		return false
	}
	return word&(1<<(local%64)) != 0
}

// bitmapQueryChild selects the child of one bit in one branch page (Rust
// query_child: an absent child answers false).
func bitmapQueryChild(page []byte, bit uint32, base uint64, level uint16) (uint32, uint64, bool, error) {
	span, err := bitmap.Coverage(level - 1)
	if err != nil {
		return 0, 0, false, formatError(err)
	}
	index, err := bitmap.ChildIndex(bit, level)
	if err != nil {
		return 0, 0, false, formatError(err)
	}
	child, err := bitmap.BranchChild(page, index)
	if err != nil {
		return 0, 0, false, formatError(err)
	}
	if child == 0 {
		return 0, 0, false, nil
	}
	childBase, err := bitmapChildBase(base, span, index)
	if err != nil {
		return 0, 0, false, err
	}
	return child, childBase, true, nil
}
