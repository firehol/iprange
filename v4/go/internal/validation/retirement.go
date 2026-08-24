package validation

// Retirement tree validation (Rust validation/retirement.rs): the extent
// walk over the retirement root with the exact extent-validity, overlap,
// and count checks, marking every retired page in the allocation
// partition.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/retire"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// retirementCodec carries the retirement tree wire decoding (Rust
// RetirementCodec: fixed 16-byte branch and leaf cells, the default
// branch-invalid class, and the RetirementListInvalid leaf class).
var retirementCodec = treeCodec{
	branchType:    byte(format.PageTypeRetirementBranch),
	leafType:      byte(format.PageTypeRetirementLeaf),
	aux:           0,
	branchLayout:  format.FixedLayout(retire.CellSize),
	leafLayout:    format.FixedLayout(retire.CellSize),
	branchInvalid: ReasonTreeOrderInvalid,
	leafInvalid:   ReasonRetirementListInvalid,
	branchKey: func(cell []byte) (tree.Key, bool) {
		key, ok := retire.DecodeKey(cell)
		if !ok {
			return tree.Key{}, false
		}
		return key.ToTree(), true
	},
	branchChild: func(cell []byte) (uint32, bool) {
		return retire.DecodeBranchChild(cell)
	},
	leafKey: func(cell []byte) (tree.Key, bool) {
		extent, ok := retire.DecodeRaw(cell)
		if !ok {
			return tree.Key{}, false
		}
		return extent.Key.ToTree(), true
	},
}

// validateRetirement runs the retirement validator (Rust
// retirement::validate): the walk records every decoded extent, the
// retired-extent count in the meta must match, and every retired page is
// marked in the allocation partition.
func validateRetirement(ctx *context) error {
	root := ctx.meta.RetirementRoot
	if root == 0 {
		if ctx.meta.RetiredExtentCount != 0 {
			return retirementCountMismatch(ctx)
		}
		return nil
	}
	var previous retire.Extent
	hasPrevious := false
	result, err := walkTree(ctx, root, ObjectRetirementTree, retirementCodec, func(ctx *context, pageNumber uint32, cell []byte) error {
		extent, ok := retire.DecodeRaw(cell)
		if !ok {
			return nil
		}
		if err := validateRetirementExtent(ctx, pageNumber, extent, previous, hasPrevious); err != nil {
			return err
		}
		previous = extent
		hasPrevious = true
		return markRetiredPages(ctx, extent)
	})
	if err != nil {
		return err
	}
	if result.records != ctx.meta.RetiredExtentCount {
		return retirementCountMismatch(ctx)
	}
	return nil
}

// validateRetirementExtent mirrors Rust validate_extent: an invalid
// extent reports the RetirementListInvalid class, an overlapping pair of
// the same transaction the RetirementOrderInvalid class; both findings
// keep the walk running.
func validateRetirementExtent(ctx *context, pageNumber uint32, extent retire.Extent, previous retire.Extent, hasPrevious bool) error {
	end := uint64(extent.FirstPage()) + extent.PageCount()
	if !retirementExtentValid(ctx, extent, end) {
		if err := ctx.emit(ReasonRetirementListInvalid, ObjectRetirementTree, &pageNumber, nil, nil); err != nil {
			return err
		}
		return nil
	}
	if hasPrevious && retirementExtentsOverlap(previous, extent) {
		if err := ctx.emit(ReasonRetirementOrderInvalid, ObjectRetirementTree, &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// retirementExtentValid mirrors Rust extent_valid: the retiring
// transaction is above creation and inside the selected generation, the
// first page is a data page, the extent is nonempty, and its endpoint
// stays inside the committed page count (the endpoint addition cannot
// overflow: both operands are u32-bounded and extend to u64).
func retirementExtentValid(ctx *context, extent retire.Extent, end uint64) bool {
	return extent.Transaction() > 1 &&
		extent.Transaction() <= ctx.meta.TxnID &&
		extent.FirstPage() >= 2 &&
		extent.PageCount() != 0 &&
		end <= ctx.meta.PageCount
}

// retirementExtentsOverlap mirrors Rust extents_overlap: two extents of
// the same transaction overlap when the previous one reaches the first
// page of the current one.
func retirementExtentsOverlap(previous, current retire.Extent) bool {
	return previous.Transaction() == current.Transaction() &&
		uint64(previous.FirstPage())+previous.PageCount() >= uint64(current.FirstPage())
}

// markRetiredPages mirrors Rust mark_extent: every page of the extent is
// marked in the allocation partition, clamped to the committed page
// count.
func markRetiredPages(ctx *context, extent retire.Extent) error {
	if extent.FirstPage() < 2 || extent.PageCount() == 0 {
		return nil
	}
	end := uint64(extent.FirstPage()) + extent.PageCount()
	if end > ctx.meta.PageCount {
		end = ctx.meta.PageCount
	}
	for page := uint64(extent.FirstPage()); page < end; page++ {
		if err := ctx.markAllocated(uint32(page), ObjectRetirementTree); err != nil {
			return err
		}
	}
	return nil
}

// retirementCountMismatch reports the RootCountInvalid class (Rust
// count_mismatch).
func retirementCountMismatch(ctx *context) error {
	return ctx.emit(ReasonRootCountInvalid, ObjectRetirementTree, nil, nil, nil)
}
