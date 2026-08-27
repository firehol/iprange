package validation

// Structure-ID table validation walk (Rust validation/structure_table.rs
// over the structured_value/table.rs geometry): the dense radix table
// walk with the directory/record page header inspection, the per-slot
// record discovery, the found/item-count proof, and the reserved-tail
// scan. The walk is allocation-free and follows the graph claims; the
// record cells alias the inspected page.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// structureTableMaxDepth mirrors Rust MAX_DEPTH: one path slot per
// level, for at most the level-3 directory root plus its record page.
const structureTableMaxDepth = 4

// structureTableWalkResult mirrors Rust WalkResult.
type structureTableWalkResult struct {
	records uint64
}

// walkStructureTable mirrors Rust structure_table::walk over the root:
// a zero root is an empty table, every nonzero record slot runs leaf
// with its implied id, and the result carries the decoded record count.
func walkStructureTable(ctx *context, root uint32, leaf func(*context, uint32, uint64, []byte) error) (structureTableWalkResult, error) {
	if root == 0 {
		return structureTableWalkResult{}, nil
	}
	level, err := structureTableRequiredLevel(ctx.meta.StructureIDLimit)
	if err != nil {
		return structureTableWalkResult{}, err
	}
	var path [structureTableMaxDepth]uint32
	records := uint64(0)
	if err := walkStructureTableNode(ctx, root, level, 0, &path, 0, &records, leaf); err != nil {
		return structureTableWalkResult{}, err
	}
	return structureTableWalkResult{records: records}, nil
}

// structureTableRequiredLevel mirrors structured_value/table.rs
// required_level: the ID limit must fit the u32 namespace and the
// smallest level whose coverage reaches it is the root level.
func structureTableRequiredLevel(limit uint64) (uint16, error) {
	if limit < 1 || limit > uint64(1)<<32 {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "structure table ID limit is invalid"}
	}
	level, ok := format.StructureRootLevel(limit)
	if !ok {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "structure table ID limit is invalid"}
	}
	return uint16(level), nil
}

// walkStructureTableNode visits one dense table node (Rust walk_node):
// the path slot, the graph page, the classified header, the reserved
// tail, and the leaf or directory walk.
func walkStructureTableNode(ctx *context, pageNumber uint32, expectedLevel uint16, base uint64, path *[structureTableMaxDepth]uint32, depth int, records *uint64, leaf func(*context, uint32, uint64, []byte) error) error {
	if depth >= len(path) {
		pageCopy := pageNumber
		if err := ctx.emit(ReasonTreeLevelInvalid, ObjectStructureDictionary, &pageCopy, nil, nil); err != nil {
			return err
		}
		return nil
	}
	path[depth] = pageNumber
	page, err := ctx.readGraphPage(pageNumber, ObjectStructureDictionary, path[:depth])
	if err != nil || page == nil {
		return err
	}
	header, ok, err := structureTablePageHeader(ctx, pageNumber, page, &expectedLevel)
	if err != nil || !ok {
		return err
	}
	if !format.AllZero(page[header.Lower:format.PageSize]) {
		pageCopy := pageNumber
		if err := ctx.emit(ReasonPageReservedNonzero, ObjectStructureDictionary, &pageCopy, nil, nil); err != nil {
			return err
		}
	}
	if header.Level == 0 {
		return walkStructureTableLeaf(ctx, pageNumber, page, base, header, records, leaf)
	}
	return walkStructureTableBranch(ctx, pageNumber, page, base, header, path, depth, records, leaf)
}

// structureTablePageHeader runs the dense-table header inspection and
// streams the classified finding on refusal (Rust structure_table
// header_reason: the same classes as the tree pages).
func structureTablePageHeader(ctx *context, pageNumber uint32, page []byte, expectedLevel *uint16) (format.PageHeader, bool, error) {
	header, problem := format.InspectStructureTableHeader(page, ctx.meta.TxnID, uint32(ctx.meta.StructureKind), expectedLevel)
	if problem != format.TreeHeaderProblemNone {
		pageCopy := pageNumber
		if err := ctx.emit(treeHeaderProblemReason(problem), ObjectStructureDictionary, &pageCopy, nil, nil); err != nil {
			return format.PageHeader{}, false, err
		}
		return format.PageHeader{}, false, nil
	}
	return header, true, nil
}

// walkStructureTableLeaf visits every nonzero record slot (Rust
// walk_leaf): the slot cells in id order, the record count, and the
// leaf callback with the implied id base+slot.
func walkStructureTableLeaf(ctx *context, pageNumber uint32, page []byte, base uint64, header format.PageHeader, records *uint64, leaf func(*context, uint32, uint64, []byte) error) error {
	found := 0
	for slot := 0; slot < format.StructureRecordSlots; slot++ {
		if err := ctx.checkpoint(); err != nil {
			return err
		}
		cell := page[32+slot*format.StructureRecordSize : 32+(slot+1)*format.StructureRecordSize]
		if format.AllZero(cell) {
			continue
		}
		found++
		if *records == ^uint64(0) {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation structure record count"}
		}
		*records++
		if err := leaf(ctx, pageNumber, base+uint64(slot), cell); err != nil {
			return err
		}
	}
	if found != int(header.ItemCount) {
		return structureTableShapeFinding(ctx, pageNumber)
	}
	return nil
}

// walkStructureTableBranch visits every nonzero directory child (Rust
// walk_branch): the child span at the level below, the child base
// arithmetic, and the recursion with the next lower level.
func walkStructureTableBranch(ctx *context, pageNumber uint32, page []byte, base uint64, header format.PageHeader, path *[structureTableMaxDepth]uint32, depth int, records *uint64, leaf func(*context, uint32, uint64, []byte) error) error {
	span, ok := format.StructureSpanOfLevel(uint32(header.Level))
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation structure coverage"}
	}
	found := 0
	for index := 0; index < format.StructureDirectoryChildCount; index++ {
		if err := ctx.checkpoint(); err != nil {
			return err
		}
		child := format.U32(page[32+index*4 : 36+index*4])
		if child == 0 {
			continue
		}
		found++
		offset := span * uint64(index)
		if index != 0 && offset/uint64(index) != span {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation structure coverage"}
		}
		childBase := base + offset
		if childBase < base {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation structure coverage"}
		}
		if err := walkStructureTableNode(ctx, child, header.Level-1, childBase, path, depth+1, records, leaf); err != nil {
			return err
		}
	}
	if found != int(header.ItemCount) {
		return structureTableShapeFinding(ctx, pageNumber)
	}
	return nil
}

// structureTableShapeFinding streams the header class of a record or
// directory page whose found cells disagree with its item count (Rust
// page_shape_finding).
func structureTableShapeFinding(ctx *context, pageNumber uint32) error {
	pageCopy := pageNumber
	return ctx.emit(ReasonPageHeaderInvalid, ObjectStructureDictionary, &pageCopy, nil, nil)
}
