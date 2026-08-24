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
	scanner := &blobScanner{m: m, meta: meta, pages: pages, check: check, rep: rep, consume: consume}
	var path [format.MaxTreeLevel + 1]uint32
	span, err := scanner.node(root, nil, 0, length, &path, 0)
	if err != nil || span == nil {
		return false, err
	}
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

func (s *blobScanner) node(pageNumber uint32, expectedLevel *uint16, expectedStart, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (*blobSpan, error) {
	if err := live.Checkpoint(s.check); err != nil {
		return nil, err
	}
	claimed, reason, err := s.pages.claim(pageNumber, s.meta.PageCount, path[:], depth)
	if err != nil {
		return nil, err
	}
	if !claimed {
		if err := emitBlobUnknown(s.rep, reason, &pageNumber); err != nil {
			return nil, err
		}
		return nil, nil
	}
	page, problem := checkedPage(s.m, pageNumber, s.meta.PageCount)
	if problem != nil {
		return nil, s.reject(pageNumber, problem.reason, problem.ioUnreadable)
	}
	switch format.PageType(page[4]) {
	case format.PageTypeBlobLeaf:
		return s.leaf(pageNumber, page, expectedLevel, expectedStart, length)
	case format.PageTypeBlobBranch:
		return s.branch(pageNumber, page, expectedLevel, expectedStart, length, path, depth)
	default:
		return nil, s.reject(pageNumber, validation.ReasonPageTypeMismatch, false)
	}
}

func (s *blobScanner) leaf(pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart, length uint64) (*blobSpan, error) {
	geometry, err := format.DecodeBlobLeafGeometry(page, expectedLevel, expectedStart, length)
	if err != nil {
		return nil, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if !format.AllZero(page[format.BlobLeafData+geometry.DataLen:]) {
		return nil, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if err := s.rep.pageAccepted(); err != nil {
		return nil, err
	}
	bytes := page[format.BlobLeafData : format.BlobLeafData+geometry.DataLen]
	if err := s.consume(bytes); err != nil {
		return nil, err
	}
	return &blobSpan{start: geometry.Start, end: geometry.End, complete: true}, nil
}

func (s *blobScanner) branch(pageNumber uint32, page []byte, expectedLevel *uint16, expectedStart, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (*blobSpan, error) {
	header, err := format.DecodePageHeader(page, s.meta.TxnID)
	if err != nil || header.PageType != format.PageTypeBlobBranch || header.Aux != format.BlobKindMembership ||
		header.Level == 0 ||
		(expectedLevel != nil && header.Level != *expectedLevel) {
		return nil, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	inspection := format.InspectLayout(page, &header, format.FixedLayout(format.BlobBranchSize))
	if inspection == nil || inspection.ReservedNonzero {
		return nil, s.reject(pageNumber, validation.ReasonBlobInvalid, false)
	}
	if err := s.rep.pageAccepted(); err != nil {
		return nil, err
	}
	valid, err := s.branchRecordsValid(page, &header, expectedStart, length)
	if err != nil {
		return nil, err
	}
	if !valid {
		if err := emitBlobUnknown(s.rep, validation.ReasonBlobInvalid, &pageNumber); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return s.branchChildren(pageNumber, page, header, length, path, depth)
}

func (s *blobScanner) branchChildren(pageNumber uint32, page []byte, header format.PageHeader, length uint64, path *[format.MaxTreeLevel + 1]uint32, depth int) (*blobSpan, error) {
	cells := format.InspectLayout(page, &header, format.FixedLayout(format.BlobBranchSize)).Cells()
	var first *uint64
	var previousEnd *uint64
	complete := true
	for index := 0; index < int(header.ItemCount); index++ {
		if err := live.Checkpoint(s.check); err != nil {
			return nil, err
		}
		cell, ok := cells.Next()
		if !ok {
			return nil, pageDecodeError()
		}
		offset, child, err := format.DecodeBlobBranchFields(cell)
		if err != nil {
			return nil, pageDecodeError()
		}
		record := format.BlobBranchRecord{LogicalOffset: offset, Child: child}
		expected := header.Level - 1
		span, err := s.node(record.Child, &expected, record.LogicalOffset, length, path, depth+1)
		if err != nil {
			return nil, err
		}
		if span == nil {
			complete = false
			previousEnd = nil
			continue
		}
		if first == nil {
			value := span.start
			first = &value
		}
		if span.start != record.LogicalOffset || (previousEnd != nil && *previousEnd != span.start) {
			if err := emitBlobUnknown(s.rep, validation.ReasonBlobInvalid, &pageNumber); err != nil {
				return nil, err
			}
			complete = false
		}
		value := span.end
		previousEnd = &value
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

func (s *blobScanner) reject(pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := s.rep.pageRejected(ioUnreadable); err != nil {
		return err
	}
	return emitBlobUnknown(s.rep, reason, &pageNumber)
}

func (s *blobScanner) branchRecordsValid(page []byte, header *format.PageHeader, expectedStart, length uint64) (bool, error) {
	cells := format.InspectLayout(page, header, format.FixedLayout(format.BlobBranchSize)).Cells()
	var previous *uint64
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
		if !blobBranchRecordValid(record, previous, index == 0, expectedStart, length, s.meta.PageCount) {
			return false, nil
		}
		value := record.LogicalOffset
		previous = &value
	}
	return true, nil
}

func blobBranchRecordValid(record format.BlobBranchRecord, previous *uint64, first bool, expectedStart, length, pageCount uint64) bool {
	if record.Child < 2 || uint64(record.Child) >= pageCount {
		return false
	}
	if record.LogicalOffset >= length {
		return false
	}
	if previous != nil && *previous >= record.LogicalOffset {
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
