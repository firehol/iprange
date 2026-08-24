package validation

// Range tree validation (Rust validation/range.rs): the family walk over
// the committed range tree with the record order, reversed, overlap, and
// coalescing findings, the value-kind reverse-count arms, and the root
// record-count proof. Every tree page is validated through the shared
// page authorities; every cell decodes through the format record codecs.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// validateRange runs the range tree validator (Rust range::validate): a
// zero root proves the record count is zero, any other root walks the
// selected family and the walk count must equal the declared record
// count.
func validateRange(ctx *context) error {
	if ctx.meta.RangeRoot == 0 {
		if ctx.meta.RangeRecordCount != 0 {
			return rangeCountMismatch(ctx)
		}
		return nil
	}
	var count uint64
	var err error
	switch ctx.meta.AddressFamily {
	case 4:
		count, err = validateRangeFamily4(ctx)
	case 6:
		count, err = validateRangeFamily6(ctx)
	default:
		// Unreachable through the bootstrap family check; kept as the
		// Rust enum exhaustiveness arm.
		return nil
	}
	if err != nil {
		return err
	}
	if count != ctx.meta.RangeRecordCount {
		return rangeCountMismatch(ctx)
	}
	return nil
}

func rangeCountMismatch(ctx *context) error {
	return ctx.emit(ReasonRootCountInvalid, ObjectRangeTree, nil, nil, nil)
}

// rangeState carries the walk count and the previous record of the
// neighbor checks across an ordered range walk (Rust RangeState).
type rangeState struct {
	count    uint64
	previous *format.RangeRecordV4
	prev6    *format.RangeRecordV6
	family   uint8
}

// validateRangeFamily4 walks the IPv4 range tree and returns the record
// count (Rust validate_family).
func validateRangeFamily4(ctx *context) (uint64, error) {
	state := rangeState{family: 4}
	var path [format.MaxTreeLevel + 1]uint32
	if _, _, err := walkRangeNode4(ctx, ctx.meta.RangeRoot, nil, true, &path, 0, &state); err != nil {
		return 0, err
	}
	return state.count, nil
}

// validateRangeFamily6 walks the IPv6 range tree and returns the record
// count.
func validateRangeFamily6(ctx *context) (uint64, error) {
	state := rangeState{family: 6}
	var path [format.MaxTreeLevel + 1]uint32
	if _, _, err := walkRangeNode6(ctx, ctx.meta.RangeRoot, nil, true, &path, 0, &state); err != nil {
		return 0, err
	}
	return state.count, nil
}

// rangePageHeader validates one range tree page and returns its header
// (Rust validate_range_page over page::slotted_header; the degenerate
// single-record root branch is the TreeLevelInvalid class).
func rangePageHeader(ctx *context, pageNumber uint32, page []byte, object ValidationObject, family uint8, expectedLevel *uint16, root bool) (*format.PageHeader, error) {
	header, err := treePageHeader(ctx, pageNumber, page, object, treePageSpec{
		branchType:    byte(format.PageTypeRangeBranch),
		leafType:      byte(format.PageTypeRangeLeaf),
		aux:           uint32(family),
		expectedLevel: expectedLevel,
	})
	if err != nil || header == nil {
		return nil, err
	}
	if err := validateRootShape(ctx, pageNumber, object, root, header); err != nil {
		return nil, err
	}
	return header, nil
}

func walkRangeNode4(ctx *context, pageNumber uint32, expectedLevel *uint16, root bool, path *[format.MaxTreeLevel + 1]uint32, depth int, state *rangeState) (tree.Key, bool, error) {
	page, err := readTreeNodePage(ctx, pageNumber, ObjectRangeTree, path, depth)
	if err != nil || page == nil {
		return tree.Key{}, false, err
	}
	header, err := rangePageHeader(ctx, pageNumber, page, ObjectRangeTree, 4, expectedLevel, root)
	if err != nil || header == nil {
		return tree.Key{}, false, err
	}
	cells, err := validateFixedCells(ctx, pageNumber, page, ObjectRangeTree, header, rangeCellLen4(header.Level))
	if err != nil || cells == nil {
		return tree.Key{}, false, err
	}
	if header.Level == 0 {
		return walkRangeLeaf4(ctx, pageNumber, cells, state)
	}
	return walkRangeBranch4(ctx, pageNumber, cells, header, path, depth, state)
}

func walkRangeNode6(ctx *context, pageNumber uint32, expectedLevel *uint16, root bool, path *[format.MaxTreeLevel + 1]uint32, depth int, state *rangeState) (tree.Key, bool, error) {
	page, err := readTreeNodePage(ctx, pageNumber, ObjectRangeTree, path, depth)
	if err != nil || page == nil {
		return tree.Key{}, false, err
	}
	header, err := rangePageHeader(ctx, pageNumber, page, ObjectRangeTree, 6, expectedLevel, root)
	if err != nil || header == nil {
		return tree.Key{}, false, err
	}
	cells, err := validateFixedCells(ctx, pageNumber, page, ObjectRangeTree, header, rangeCellLen6(header.Level))
	if err != nil || cells == nil {
		return tree.Key{}, false, err
	}
	if header.Level == 0 {
		return walkRangeLeaf6(ctx, pageNumber, cells, state)
	}
	return walkRangeBranch6(ctx, pageNumber, cells, header, path, depth, state)
}

// rangeCellLen4/6 mirror range.rs range_cell_len: the record size at
// level zero, the branch entry size above.
func rangeCellLen4(level uint16) int {
	if level == 0 {
		return format.RangeRecordV4Size
	}
	return format.RangeEntryV4Size
}

func rangeCellLen6(level uint16) int {
	if level == 0 {
		return format.RangeRecordV6Size
	}
	return format.RangeEntryV6Size
}

// walkRangeLeaf4 visits every leaf cell in order: the per-leaf key
// order, one range count per record, the reversed/overlap/coalescing
// findings, and the value-kind reverse-count arms (Rust validate_leaf +
// validate_leaf_cells).
func walkRangeLeaf4(ctx *context, pageNumber uint32, cells *format.LayoutInspection, state *rangeState) (tree.Key, bool, error) {
	var order leafOrder
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		record, err := format.DecodeRangeFieldsV4(cell)
		if err != nil {
			// The fixed layout proved the exact record size; a decode
			// failure here is the Rust Corrupt propagation.
			return tree.Key{}, false, formatError(err)
		}
		if order.observe(tree.Key{Lo: uint64(record.From)}) {
			if err := emitRangeFinding(ctx, pageNumber, ReasonTreeOrderInvalid); err != nil {
				return tree.Key{}, false, err
			}
		}
		if state.count == ^uint64(0) {
			return tree.Key{}, false, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation range record count"}
		}
		state.count++
		if record.From > record.To {
			if err := emitRangeFinding(ctx, pageNumber, ReasonRangeReversed); err != nil {
				return tree.Key{}, false, err
			}
			state.previous = nil
			continue
		}
		if err := validateRangeValue(ctx, pageNumber, record.Value, state); err != nil {
			return tree.Key{}, false, err
		}
		if state.previous != nil {
			if reason := neighborProblem4(state.previous, record); reason != 0 {
				if err := emitRangeFinding(ctx, pageNumber, reason); err != nil {
					return tree.Key{}, false, err
				}
			}
		}
		copyRecord := record
		state.previous = &copyRecord
	}
	return order.first, order.hasFirst, nil
}

// walkRangeLeaf6 is the IPv6 form of walkRangeLeaf4.
func walkRangeLeaf6(ctx *context, pageNumber uint32, cells *format.LayoutInspection, state *rangeState) (tree.Key, bool, error) {
	var order leafOrder
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		record, err := format.DecodeRangeFieldsV6(cell)
		if err != nil {
			return tree.Key{}, false, formatError(err)
		}
		if order.observe(tree.Key{Hi: record.FromHi, Lo: record.FromLo}) {
			if err := emitRangeFinding(ctx, pageNumber, ReasonTreeOrderInvalid); err != nil {
				return tree.Key{}, false, err
			}
		}
		if state.count == ^uint64(0) {
			return tree.Key{}, false, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation range record count"}
		}
		state.count++
		if record.FromHi > record.ToHi || (record.FromHi == record.ToHi && record.FromLo > record.ToLo) {
			if err := emitRangeFinding(ctx, pageNumber, ReasonRangeReversed); err != nil {
				return tree.Key{}, false, err
			}
			state.previous = nil
			state.prev6 = nil
			continue
		}
		if err := validateRangeValue(ctx, pageNumber, record.Value, state); err != nil {
			return tree.Key{}, false, err
		}
		if state.prev6 != nil {
			if reason := neighborProblem6(state.prev6, record); reason != 0 {
				if err := emitRangeFinding(ctx, pageNumber, reason); err != nil {
					return tree.Key{}, false, err
				}
			}
		}
		copyRecord := record
		state.prev6 = &copyRecord
		state.previous = nil
	}
	return order.first, order.hasFirst, nil
}

// leafOrder is the per-leaf first/previous key cursor (Rust LeafOrder).
type leafOrder struct {
	first       tree.Key
	hasFirst    bool
	previous    tree.Key
	hasPrevious bool
}

// observe records one key and reports a non-increasing key.
func (o *leafOrder) observe(key tree.Key) bool {
	if !o.hasFirst {
		o.first = key
		o.hasFirst = true
	}
	invalid := o.hasPrevious && !o.previous.Less(key)
	o.previous = key
	o.hasPrevious = true
	return invalid
}

// validateRangeValue runs the value-kind arm of one range record (Rust
// validate_leaf_cell KIND arms: Direct has none, Membership counts the
// value in the membership table, Structured in the structure table; a
// zero value is its missing class).
func validateRangeValue(ctx *context, pageNumber uint32, value uint32, state *rangeState) error {
	switch ctx.meta.ValueKind {
	case format.ValueKindMembership:
		if value == 0 {
			return emitRangeFinding(ctx, pageNumber, ReasonMembershipBitmapInvalid)
		}
		switch ctx.countMembershipOwner(value) {
		case CountFull:
			return emitRangeFinding(ctx, pageNumber, ReasonMembershipRefcountInvalid)
		case CountCancelled:
			return &format.Error{Code: format.CodeCancelled, Detail: "validation cancelled"}
		case CountUnavailable:
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "membership validation has no membership table"}
		}
	case format.ValueKindStructured:
		if value == 0 {
			return emitRangeFinding(ctx, pageNumber, ReasonStructureMissing)
		}
		switch ctx.countStructureRange(value) {
		case CountFull:
			return emitRangeFinding(ctx, pageNumber, ReasonStructureRefcountInvalid)
		case CountCancelled:
			return &format.Error{Code: format.CodeCancelled, Detail: "validation cancelled"}
		case CountUnavailable:
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "structured validation has no structure table"}
		}
	}
	return nil
}

// neighborProblem4 mirrors Rust neighbor_problem over the previous
// record: an overlapping start is the overlap class, an exactly
// adjacent start with the same value is the not-coalesced class.
func neighborProblem4(previous *format.RangeRecordV4, current format.RangeRecordV4) ValidationReason {
	if current.From <= previous.To {
		return ReasonRangeOverlap
	}
	if current.From == previous.To+1 && previous.Value == current.Value {
		return ReasonRangeNotCoalesced
	}
	return 0
}

// neighborProblem6 is the IPv6 form of neighborProblem4.
func neighborProblem6(previous *format.RangeRecordV6, current format.RangeRecordV6) ValidationReason {
	if current.FromHi < previous.ToHi || (current.FromHi == previous.ToHi && current.FromLo <= previous.ToLo) {
		return ReasonRangeOverlap
	}
	adjacent := previous.ToLo == ^uint64(0)
	if adjacent {
		adjacent = previous.ToHi != ^uint64(0) && current.FromHi == previous.ToHi+1 && current.FromLo == 0
	} else {
		adjacent = current.FromHi == previous.ToHi && current.FromLo == previous.ToLo+1
	}
	if adjacent && previous.Value == current.Value {
		return ReasonRangeNotCoalesced
	}
	return 0
}

// walkRangeBranch4 visits every branch cell in order: the per-page key
// order, the child subtree at the expected level, and the child-first
// fence (Rust validate_branch).
func walkRangeBranch4(ctx *context, pageNumber uint32, cells *format.LayoutInspection, header *format.PageHeader, path *[format.MaxTreeLevel + 1]uint32, depth int, state *rangeState) (tree.Key, bool, error) {
	var keys branchKeys
	expected := header.Level - 1
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		first, child, err := format.DecodeRangeEntryFieldsV4(cell)
		if err != nil {
			return tree.Key{}, false, formatError(err)
		}
		key := tree.Key{Lo: uint64(first)}
		if err := recordBranchKey(ctx, pageNumber, ObjectRangeTree, key, &keys); err != nil {
			return tree.Key{}, false, err
		}
		actual, hasActual, err := walkRangeNode4(ctx, child, &expected, false, path, depth+1, state)
		if err != nil {
			return tree.Key{}, false, err
		}
		if err := validateFence(ctx, pageNumber, ObjectRangeTree, key, actual, hasActual); err != nil {
			return tree.Key{}, false, err
		}
	}
	return keys.first, keys.hasFirst, nil
}

// walkRangeBranch6 is the IPv6 form of walkRangeBranch4.
func walkRangeBranch6(ctx *context, pageNumber uint32, cells *format.LayoutInspection, header *format.PageHeader, path *[format.MaxTreeLevel + 1]uint32, depth int, state *rangeState) (tree.Key, bool, error) {
	var keys branchKeys
	expected := header.Level - 1
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		firstHi, firstLo, child, err := format.DecodeRangeEntryFieldsV6(cell)
		if err != nil {
			return tree.Key{}, false, formatError(err)
		}
		key := tree.Key{Hi: firstHi, Lo: firstLo}
		if err := recordBranchKey(ctx, pageNumber, ObjectRangeTree, key, &keys); err != nil {
			return tree.Key{}, false, err
		}
		actual, hasActual, err := walkRangeNode6(ctx, child, &expected, false, path, depth+1, state)
		if err != nil {
			return tree.Key{}, false, err
		}
		if err := validateFence(ctx, pageNumber, ObjectRangeTree, key, actual, hasActual); err != nil {
			return tree.Key{}, false, err
		}
	}
	return keys.first, keys.hasFirst, nil
}

// emitRangeFinding streams one range finding of the page (Rust
// emit_range_finding).
func emitRangeFinding(ctx *context, pageNumber uint32, reason ValidationReason) error {
	return ctx.emit(reason, ObjectRangeTree, &pageNumber, nil, nil)
}

// formatError maps a format codec error to the typed Corrupt class (the
// Rust decode propagation class; unreachable for fixed cells because
// the layout inspection proved the exact extents).
func formatError(err error) error {
	if err == nil {
		return nil
	}
	return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
}
