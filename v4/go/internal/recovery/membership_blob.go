package recovery

// Complete CRC-checked recovery scan of one membership bitmap blob
// (Rust recovery/membership_blob.rs): the blob tree is walked under
// the page-ownership set, the leaf bytes stream into the consumer,
// and the span proofs (root bounds, branch offsets and order, leaf
// geometry, contiguous coverage) decide the complete fact.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// blobSpan is the covered byte span of one blob node (Rust Span).
type blobSpan struct {
	start    uint64
	end      uint64
	complete bool
}

// blobSpanOption is one optional blob span passed by value (the Rust
// Option<Span> walk result): the ok flag carries the None separation,
// so the walk never forms a per-node pointer.
type blobSpanOption struct {
	span blobSpan
	ok   bool
}

// scanMembershipBlob walks one membership bitmap blob (Rust
// membership_blob::scan: an invalid root emits without a page; an
// incomplete span emits on the root page; the consumed bytes stream
// through the consumer).
func scanMembershipBlob(m *mapping.Mapping, meta format.Meta, root uint32, wordCount uint32, pages *pageSet, check func() error, rep *reporter, consume func(bytes []byte) error) (bool, error) {
	length := uint64(wordCount) * 8
	if root < 2 || length == 0 {
		if err := emitBlobUnknown(rep, validation.ReasonBlobInvalid, nil); err != nil {
			return false, err
		}
		return false, nil
	}
	var scanner blobScanner
	scanner.m = m
	scanner.meta = meta
	scanner.pages = pages
	scanner.check = check
	scanner.rep = rep
	scanner.consume = consume
	var path [format.MaxTreeLevel + 1]uint32
	result, err := scanner.node(root, nil, 0, length, &path, 0)
	if err != nil || !result.ok {
		return false, err
	}
	span := result.span
	complete := span.complete && span.start == 0 && span.end == length
	if !complete {
		if err := emitBlobUnknown(rep, validation.ReasonBlobInvalid, &root); err != nil {
			return false, err
		}
	}
	return complete, nil
}

// blobScanner is one blob tree walk (Rust Scanner).
type blobScanner struct {
	m       *mapping.Mapping
	meta    format.Meta
	pages   *pageSet
	check   func() error
	rep     *reporter
	consume func(bytes []byte) error
}

func (s *blobScanner) node(pageNumber uint32, expectedLevel *uint16, expectedStart, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (blobSpanOption, error) {
	if err := live.Checkpoint(s.check); err != nil {
		return blobSpanOption{}, err
	}
	claimed, reason, err := s.pages.claim(pageNumber, s.meta.PageCount, path[:], depth)
	if err != nil {
		return blobSpanOption{}, err
	}
	if !claimed {
		pageCopy := pageNumber
		if err := emitBlobUnknown(s.rep, reason, &pageCopy); err != nil {
			return blobSpanOption{}, err
		}
		return blobSpanOption{}, nil
	}
	page, problem := checkedPage(s.m, pageNumber, s.meta.PageCount)
	if problem != nil {
		return blobSpanOption{}, s.reject(pageNumber, problem.reason, problem.ioUnreadable)
	}
	switch format.PageType(page[4]) {
	case format.PageTypeBlobLeaf:
		return s.leaf(pageNumber, page, expectedLevel, expectedStart, length)
	case format.PageTypeBlobBranch:
		return s.branch(pageNumber, page, expectedLevel, expectedStart, length, path, depth)
	default:
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonPageTypeMismatch, false)
	}
}

func (s *blobScanner) leaf(pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart, length uint64) (blobSpanOption, error) {
	// The common and born identity arms of the Rust parse_leaf_info
	// (require_leaf_identity) run before the geometry proof; the Go
	// reader path performs the same split over DecodePageHeader.
	if !format.BlobCommonValid(page) || !format.BlobBornValid(page, s.meta.TxnID) {
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	geometry, err := format.DecodeBlobLeafGeometry(page, expectedLevel, expectedStart, length)
	if err != nil {
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if !format.AllZero(page[format.BlobLeafData+geometry.DataLen:]) {
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if err := s.rep.pageAccepted(); err != nil {
		return blobSpanOption{}, err
	}
	bytes := page[format.BlobLeafData : format.BlobLeafData+geometry.DataLen]
	if err := s.consume(bytes); err != nil {
		return blobSpanOption{}, err
	}
	return blobSpanOption{span: blobSpan{start: geometry.Start, end: geometry.End, complete: true}, ok: true}, nil
}

func (s *blobScanner) branch(pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (blobSpanOption, error) {
	header, err := format.DecodePageHeader(page, s.meta.TxnID)
	if err != nil || header.PageType != format.PageTypeBlobBranch || header.Aux != format.BlobKindMembership ||
		header.Level == 0 ||
		(expectedLevel != nil && header.Level != *expectedLevel) {
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	// The single layout proof of the branch page (Rust branch(): parse
	// and inspect_layout once); the record validation and the child
	// walk reuse the same inspection instead of re-proving the page.
	inspection, layoutOK := format.InspectLayout(page, &header, format.FixedLayout(format.BlobBranchSize))
	if !layoutOK || inspection.ReservedNonzero {
		return blobSpanOption{}, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if err := s.rep.pageAccepted(); err != nil {
		return blobSpanOption{}, err
	}
	valid, err := s.branchRecordsValid(&inspection, &header, expectedStart, length)
	if err != nil {
		return blobSpanOption{}, err
	}
	if !valid {
		pageCopy := pageNumber
		if err := emitBlobUnknown(s.rep, validation.ReasonBlobInvalid, &pageCopy); err != nil {
			return blobSpanOption{}, err
		}
		return blobSpanOption{}, nil
	}
	return s.branchChildren(pageNumber, &inspection, header, length, path, depth)
}

func (s *blobScanner) branchChildren(pageNumber uint32, inspection *format.LayoutInspection, header format.PageHeader, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (blobSpanOption, error) {
	cells := inspection.Cells()
	var first uint64
	var hasFirst bool
	var previousEnd uint64
	var hasPreviousEnd bool
	complete := true
	for index := 0; index < int(header.ItemCount); index++ {
		if err := live.Checkpoint(s.check); err != nil {
			return blobSpanOption{}, err
		}
		cell, ok := cells.Next()
		if !ok {
			return blobSpanOption{}, pageDecodeError()
		}
		offset, child, err := format.DecodeBlobBranchFields(cell)
		if err != nil {
			return blobSpanOption{}, pageDecodeError()
		}
		record := format.BlobBranchRecord{LogicalOffset: offset, Child: child}
		expected := header.Level - 1
		result, err := s.node(record.Child, &expected, record.LogicalOffset, length, path, depth+1)
		if err != nil {
			return blobSpanOption{}, err
		}
		if !result.ok {
			complete = false
			hasPreviousEnd = false
			continue
		}
		span := result.span
		if !hasFirst {
			first = span.start
			hasFirst = true
		}
		if span.start != record.LogicalOffset || (hasPreviousEnd && previousEnd != span.start) {
			pageCopy := pageNumber
			if err := emitBlobUnknown(s.rep, validation.ReasonBlobInvalid, &pageCopy); err != nil {
				return blobSpanOption{}, err
			}
			complete = false
		}
		previousEnd = span.end
		hasPreviousEnd = true
		complete = complete && span.complete
	}
	if !hasFirst {
		return blobSpanOption{}, nil
	}
	end := first
	if hasPreviousEnd {
		end = previousEnd
	}
	return blobSpanOption{span: blobSpan{start: first, end: end, complete: complete}, ok: true}, nil
}

func (s *blobScanner) reject(pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := s.rep.pageRejected(ioUnreadable); err != nil {
		return err
	}
	return emitBlobUnknown(s.rep, reason, &pageNumber)
}

func (s *blobScanner) branchRecordsValid(inspection *format.LayoutInspection, header *format.PageHeader, expectedStart, length uint64) (bool, error) {
	cells := inspection.Cells()
	var previous uint64
	var hasPrevious bool
	for index := 0; index < int(header.ItemCount); index++ {
		cell, ok := cells.Next()
		if !ok {
			return false, pageDecodeError()
		}
		offset, child, err := format.DecodeBlobBranchFields(cell)
		if err != nil {
			return false, nil
		}
		record := format.BlobBranchRecord{LogicalOffset: offset, Child: child}
		if !blobBranchRecordValid(record, previous, hasPrevious, index == 0, expectedStart, length, s.meta.PageCount) {
			return false, nil
		}
		previous = record.LogicalOffset
		hasPrevious = true
	}
	return true, nil
}

func blobBranchRecordValid(record format.BlobBranchRecord, previous uint64, hasPrevious, first bool, expectedStart, length, pageCount uint64) bool {
	if record.Child < 2 || uint64(record.Child) >= pageCount {
		return false
	}
	if record.LogicalOffset >= length {
		return false
	}
	if hasPrevious && previous >= record.LogicalOffset {
		return false
	}
	if first && record.LogicalOffset != expectedStart {
		return false
	}
	return true
}

// emitBlobUnknown streams one blob envelope (Rust membership_blob::emit
// over the MembershipBlob object).
func emitBlobUnknown(rep *reporter, reason validation.ValidationReason, page *uint32) error {
	return rep.emitPageUnknown(reason, validation.ObjectMembershipBlob, page)
}
