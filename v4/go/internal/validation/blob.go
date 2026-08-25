package validation

// Membership blob scanning (Rust validation/blob.rs): one bounded walk
// over a membership blob tree feeding the record bitmap scan. The
// branch offsets prove the byte span continuity, the leaf geometry
// proves each data window, and every defect streams as the blob class
// with the page attribution, following the graph claims of the
// MembershipBlob object.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// blobSpan is one proved byte span of the walked blob (Rust Span).
type blobSpan struct {
	start    uint64
	end      uint64
	complete bool
}

// scanMembership walks the blob root and feeds every covered byte slice
// to the callback, returning whether the whole declared length was
// covered in one contiguous span (Rust blob::scan_membership).
func scanMembership(ctx *context, root uint32, length uint64, consume func(*context, []byte) error) (bool, error) {
	if !blobRequestValid(root, length) {
		if err := blobFinding(ctx, nil); err != nil {
			return false, err
		}
		return false, nil
	}
	var path [format.MaxTreeLevel + 1]uint32
	result, err := scanBlobNode(ctx, root, nil, 0, length, &path, 0, consume)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	return finishBlobSpan(ctx, root, length, *result)
}

// blobRequestValid mirrors Rust request_valid: a blob needs a root, a
// nonzero length, and an 8-byte aligned length.
func blobRequestValid(root uint32, length uint64) bool {
	return root != 0 && length != 0 && length%8 == 0
}

// finishBlobSpan proves the whole declared length covered (Rust
// finish_span: the walk must end complete at exactly the declared
// end).
func finishBlobSpan(ctx *context, root uint32, length uint64, span blobSpan) (bool, error) {
	if !span.complete {
		return false, nil
	}
	complete := span.start == 0 && span.end == length
	if !complete {
		if err := blobFinding(ctx, &root); err != nil {
			return false, err
		}
	}
	return complete, nil
}

// scanBlobNode visits one blob page (Rust scan_node: the path slot, the
// graph claim, and the leaf/branch split on the page type).
func scanBlobNode(ctx *context, pageNumber uint32, expectedLevel *uint16, expectedStart uint64, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int, consume func(*context, []byte) error) (*blobSpan, error) {
	if depth >= len(path) {
		if err := ctx.emit(ReasonTreeLevelInvalid, ObjectMembershipBlob, &pageNumber, nil, nil); err != nil {
			return nil, err
		}
		if err := ctx.markUntraversable(false); err != nil {
			return nil, err
		}
		return nil, nil
	}
	path[depth] = pageNumber
	page, err := ctx.readGraphPage(pageNumber, ObjectMembershipBlob, path[:depth])
	if err != nil || page == nil {
		return nil, err
	}
	switch format.PageType(page[4]) {
	case format.PageTypeBlobLeaf:
		return scanBlobLeaf(ctx, pageNumber, page, expectedLevel, expectedStart, length, consume)
	case format.PageTypeBlobBranch:
		return scanBlobBranch(ctx, pageNumber, page, expectedLevel, expectedStart, length, path, depth, consume)
	default:
		if err := invalidBlobPage(ctx, pageNumber, ReasonPageTypeMismatch); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// scanBlobLeaf proves one leaf span and feeds its data (Rust scan_leaf:
// the common identity, the exact geometry, the reserved tail, and the
// consumed bytes).
func scanBlobLeaf(ctx *context, pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart uint64, length uint64, consume func(*context, []byte) error) (*blobSpan, error) {
	ok, err := blobCommonIdentity(ctx, pageNumber, page)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	geometry, err := format.DecodeBlobLeafGeometry(page, expectedLevel, expectedStart, length)
	if err != nil {
		if err := blobFinding(ctx, &pageNumber); err != nil {
			return nil, err
		}
		if err := ctx.markUntraversable(false); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if !allZero(page[format.BlobLeafData+geometry.DataLen : format.PageSize]) {
		if err := ctx.emit(ReasonPageReservedNonzero, ObjectMembershipBlob, &pageNumber, nil, nil); err != nil {
			return nil, err
		}
	}
	bytes := page[format.BlobLeafData : format.BlobLeafData+geometry.DataLen]
	if err := consume(ctx, bytes); err != nil {
		return nil, err
	}
	return &blobSpan{start: geometry.Start, end: geometry.End, complete: true}, nil
}

// blobCommonIdentity proves the non-slot common header of one blob page
// (Rust common_identity: the header class then the born class; a
// refusal returns false with the finding already streamed).
func blobCommonIdentity(ctx *context, pageNumber uint32, page []byte) (bool, error) {
	if !format.BlobCommonValid(page) {
		if err := invalidBlobPage(ctx, pageNumber, ReasonPageHeaderInvalid); err != nil {
			return false, err
		}
		return false, nil
	}
	if !format.BlobBornValid(page, ctx.meta.TxnID) {
		if err := invalidBlobPage(ctx, pageNumber, ReasonPageBornTxnInvalid); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// scanBlobBranch proves the branch cells and recurses into the children
// (Rust scan_branch: the slotted header, the fixed cells, the record
// validity, and the offset-continuity of the child spans).
func scanBlobBranch(ctx *context, pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart uint64, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int, consume func(*context, []byte) error) (*blobSpan, error) {
	header, cells, ok, err := blobBranchHeader(ctx, pageNumber, page, expectedLevel)
	if err != nil || !ok {
		return nil, err
	}
	if !blobBranchRecordsValid(ctx, pageNumber, &cells, expectedStart, length) {
		if err := ctx.markUntraversable(false); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return scanBlobBranchChildren(ctx, pageNumber, &cells, header, length, path, depth, consume)
}

// blobBranchHeader runs the slotted header and layout checks of one blob
// branch (Rust branch_header: a refused page or a zero level is
// untraversable).
func blobBranchHeader(ctx *context, pageNumber uint32, page []byte, expectedLevel *uint16) (*format.PageHeader, format.LayoutInspection, bool, error) {
	header, err := treePageHeader(ctx, pageNumber, page, ObjectMembershipBlob, treePageSpec{
		branchType:    byte(format.PageTypeBlobBranch),
		leafType:      byte(format.PageTypeBlobLeaf),
		aux:           format.BlobKindMembership,
		expectedLevel: expectedLevel,
	})
	if err != nil || header == nil {
		if err == nil {
			err = ctx.markUntraversable(false)
		}
		return nil, format.LayoutInspection{}, false, err
	}
	cells, ok, err := validateFixedCells(ctx, pageNumber, page, ObjectMembershipBlob, header, format.BlobBranchSize)
	if err != nil || !ok {
		if err == nil {
			err = ctx.markUntraversable(false)
		}
		return nil, format.LayoutInspection{}, false, err
	}
	if header.Level == 0 {
		if err := ctx.markUntraversable(false); err != nil {
			return nil, format.LayoutInspection{}, false, err
		}
		return nil, format.LayoutInspection{}, false, nil
	}
	return header, cells, true, nil
}

// blobBranchRecordsValid proves the branch records (Rust
// branch_records_valid: page-bounded children, increasing offsets inside
// the declared length, and the first offset equal to the expected
// start).
func blobBranchRecordsValid(ctx *context, pageNumber uint32, cells *format.LayoutInspection, expectedStart uint64, length uint64) bool {
	previous := uint64(0)
	hasPrevious := false
	index := 0
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		offset, child, err := format.DecodeBlobBranchFields(cell)
		if err != nil {
			if err := blobFinding(ctx, &pageNumber); err != nil {
				return false
			}
			return false
		}
		if !blobBranchRecordValid(ctx, offset, child, previous, hasPrevious, index, expectedStart, length) {
			if err := blobFinding(ctx, &pageNumber); err != nil {
				return false
			}
			return false
		}
		previous = offset
		hasPrevious = true
		index++
	}
	return true
}

// blobBranchRecordValid proves one branch record (Rust
// branch_record_valid).
func blobBranchRecordValid(ctx *context, offset uint64, child uint32, previous uint64, hasPrevious bool, index int, expectedStart uint64, length uint64) bool {
	return child >= 2 && uint64(child) < ctx.meta.PageCount &&
		offset < length &&
		(!hasPrevious || previous < offset) &&
		(index != 0 || offset == expectedStart)
}

// scanBlobBranchChildren recurses into every branch child and joins the
// spans (Rust scan_branch_children: a gap or a shifted start is the blob
// class on the branch page).
func scanBlobBranchChildren(ctx *context, pageNumber uint32, cells *format.LayoutInspection, header *format.PageHeader, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int, consume func(*context, []byte) error) (*blobSpan, error) {
	expected := header.Level - 1
	var first *uint64
	var previousEnd *uint64
	complete := true
	iterator := cells.Cells()
	for {
		cell, ok := iterator.Next()
		if !ok {
			break
		}
		offset, child, err := format.DecodeBlobBranchFields(cell)
		if err != nil {
			// The record validity pass already proved every cell.
			return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
		}
		result, err := scanBlobNode(ctx, child, &expected, offset, length, path, depth+1, consume)
		if err != nil {
			return nil, err
		}
		if result == nil {
			complete = false
			previousEnd = nil
			continue
		}
		span := *result
		if first == nil {
			first = &span.start
		}
		if span.start != offset || (previousEnd != nil && *previousEnd != span.start) {
			if err := blobFinding(ctx, &pageNumber); err != nil {
				return nil, err
			}
			complete = false
		}
		previousEnd = &span.end
		complete = complete && span.complete
	}
	if first == nil {
		return nil, nil
	}
	end := *first
	if previousEnd != nil {
		end = *previousEnd
	}
	return &blobSpan{start: *first, end: end, complete: complete}, nil
}

// invalidBlobPage streams one page-class finding and marks the subgraph
// untraversable (Rust invalid_page).
func invalidBlobPage(ctx *context, pageNumber uint32, reason ValidationReason) error {
	if err := ctx.emit(reason, ObjectMembershipBlob, &pageNumber, nil, nil); err != nil {
		return err
	}
	return ctx.markUntraversable(false)
}

// blobFinding streams the blob class finding (Rust finding).
func blobFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonBlobInvalid, ObjectMembershipBlob, page, nil, nil)
}

// allZero reports one all-zero byte span.
func allZero(bytes []byte) bool {
	for _, b := range bytes {
		if b != 0 {
			return false
		}
	}
	return true
}
