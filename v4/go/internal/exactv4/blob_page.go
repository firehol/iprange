package exactv4

import (
	"encoding/binary"
	"fmt"
)

const (
	blobBranchEntrySize = 16
	blobLeafDataOffset  = 48
	blobLeafCapacity    = PageSize - blobLeafDataOffset
)

type blobKind uint32

const (
	blobKindMembershipBitmap   blobKind = 1
	blobKindRetirementPageList blobKind = 2
)

func blobKindFromWire(value uint32) (blobKind, bool) {
	switch blobKind(value) {
	case blobKindMembershipBitmap, blobKindRetirementPageList:
		return blobKind(value), true
	default:
		return 0, false
	}
}

func (k blobKind) alignment() uint64 {
	switch k {
	case blobKindMembershipBitmap:
		return 8
	case blobKindRetirementPageList:
		return 4
	default:
		return 0
	}
}

type blobPageErrorCode uint8

const (
	blobPageErrHeader blobPageErrorCode = iota + 1
	blobPageErrWrongPageType
	blobPageErrWrongKind
	blobPageErrFixedGeometry
	blobPageErrEmptyBranch
	blobPageErrIndexOutOfBounds
	blobPageErrChildOutOfBounds
	blobPageErrReservedNonzero
	blobPageErrOffsetsNotStrict
	blobPageErrLeafItemCount
	blobPageErrDataLength
	blobPageErrDataAlignment
	blobPageErrChecksum
)

type blobPageError struct {
	code       blobPageErrorCode
	cause      error
	pageType   PageType
	wireKind   uint32
	childPage  uint32
	itemCount  uint16
	dataLength uint16
	alignment  uint64
}

type blobPageStatus struct {
	code       blobPageErrorCode
	header     PageHeaderError
	hasHeader  bool
	pageType   PageType
	wireKind   uint32
	childPage  uint32
	itemCount  uint16
	dataLength uint16
	alignment  uint64
}

func (status blobPageStatus) failed() bool { return status.code != 0 }

func (status blobPageStatus) asError() *blobPageError {
	if !status.failed() {
		return nil
	}
	errorValue := &blobPageError{
		code:       status.code,
		pageType:   status.pageType,
		wireKind:   status.wireKind,
		childPage:  status.childPage,
		itemCount:  status.itemCount,
		dataLength: status.dataLength,
		alignment:  status.alignment,
	}
	if status.hasHeader {
		header := status.header
		errorValue.cause = &header
	}
	return errorValue
}

func (status blobPageStatus) err() error {
	if !status.failed() {
		return nil
	}
	return status.asError()
}

func blobHeaderStatus(page []byte, selectedTxn uint64) (PageHeader, blobPageStatus) {
	header, problem := decodePageHeaderNoAlloc(page, selectedTxn)
	if problem.code == 0 {
		return header, blobPageStatus{}
	}
	return PageHeader{}, blobPageStatus{
		code:      blobPageErrHeader,
		hasHeader: true,
		header: PageHeaderError{
			Code:        problem.code,
			Length:      problem.length,
			WireType:    problem.wireType,
			Flags:       problem.flags,
			HeaderSize:  problem.headerSize,
			BornTxn:     problem.bornTxn,
			SelectedTxn: problem.selectedTxn,
			PageType:    problem.pageType,
			Level:       problem.level,
			Lower:       problem.lower,
			Upper:       problem.upper,
		},
	}
}

func (e *blobPageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 blob page: error %d", e.code)
}

func (e *blobPageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type blobBranchEntry struct {
	logicalOffset uint64
	childPage     uint32
}

type blobBranch struct {
	page      []byte
	count     int
	level     uint16
	pageCount uint64
}

func openBlobBranch(
	page []byte,
	selectedTxn uint64,
	kind blobKind,
	pageCount uint64,
) (blobBranch, error) {
	branch, status := openBlobBranchStatus(page, selectedTxn, kind, pageCount)
	return branch, status.err()
}

func openBlobBranchStatus(
	page []byte,
	selectedTxn uint64,
	kind blobKind,
	pageCount uint64,
) (blobBranch, blobPageStatus) {
	header, status := blobHeaderStatus(page, selectedTxn)
	if status.failed() {
		return blobBranch{}, status
	}
	if header.PageType != PageTypeBlobBranch {
		return blobBranch{}, blobPageStatus{
			code:     blobPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	wireKind, valid := blobKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return blobBranch{}, blobPageStatus{
			code:     blobPageErrWrongKind,
			wireKind: header.Aux,
		}
	}
	count := int(header.ItemCount)
	if count == 0 {
		return blobBranch{}, blobPageStatus{code: blobPageErrEmptyBranch}
	}
	bodyBytes, ok := checkedMul(uint64(count), blobBranchEntrySize)
	if !ok {
		return blobBranch{}, blobPageStatus{code: blobPageErrFixedGeometry}
	}
	lower, ok := checkedAdd(uint64(PageHeaderSize), bodyBytes)
	if !ok || lower != uint64(header.Lower) || header.Upper != PageSize {
		return blobBranch{}, blobPageStatus{code: blobPageErrFixedGeometry}
	}
	return blobBranch{
		page:      page,
		count:     count,
		level:     header.Level,
		pageCount: pageCount,
	}, blobPageStatus{}
}

func (b blobBranch) verifyLocal() error {
	return b.verifyLocalStatus().err()
}

func (b blobBranch) verifyLocalStatus() blobPageStatus {
	if !VerifyPageCRC32C(b.page) {
		return blobPageStatus{code: blobPageErrChecksum}
	}
	lower := int(PageHeaderSize) + b.count*blobBranchEntrySize
	if anyNonzero(b.page[lower:]) {
		return blobPageStatus{code: blobPageErrReservedNonzero}
	}
	var previous uint64
	havePrevious := false
	for index := 0; index < b.count; index++ {
		entry, status := b.entryStatus(index)
		if status.failed() {
			return status
		}
		if havePrevious && entry.logicalOffset <= previous {
			return blobPageStatus{code: blobPageErrOffsetsNotStrict}
		}
		previous = entry.logicalOffset
		havePrevious = true
	}
	return blobPageStatus{}
}

func (b blobBranch) len() int { return b.count }

func (b blobBranch) entry(index int) (blobBranchEntry, error) {
	entry, status := b.entryStatus(index)
	return entry, status.err()
}

func (b blobBranch) entryStatus(index int) (blobBranchEntry, blobPageStatus) {
	if index < 0 || index >= b.count {
		return blobBranchEntry{}, blobPageStatus{code: blobPageErrIndexOutOfBounds}
	}
	at := int(PageHeaderSize) + index*blobBranchEntrySize
	if binary.LittleEndian.Uint32(b.page[at+12:at+16]) != 0 {
		return blobBranchEntry{}, blobPageStatus{code: blobPageErrReservedNonzero}
	}
	childPage := binary.LittleEndian.Uint32(b.page[at+8 : at+12])
	if childPage < 2 || uint64(childPage) >= b.pageCount {
		return blobBranchEntry{}, blobPageStatus{
			code:      blobPageErrChildOutOfBounds,
			childPage: childPage,
		}
	}
	return blobBranchEntry{
		logicalOffset: binary.LittleEndian.Uint64(b.page[at : at+8]),
		childPage:     childPage,
	}, blobPageStatus{}
}

func (b blobBranch) predecessorFor(logicalOffset uint64) (int, error) {
	index, status := b.predecessorForStatus(logicalOffset)
	return index, status.err()
}

func (b blobBranch) predecessorForStatus(logicalOffset uint64) (int, blobPageStatus) {
	low, high := 0, b.count
	for low < high {
		middle := low + (high-low)/2
		entry, status := b.entryStatus(middle)
		if status.failed() {
			return 0, status
		}
		if entry.logicalOffset <= logicalOffset {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == 0 {
		return 0, blobPageStatus{code: blobPageErrOffsetsNotStrict}
	}
	return low - 1, blobPageStatus{}
}

type blobLeaf struct {
	page          []byte
	logicalOffset uint64
	dataLength    uint16
}

func openBlobLeaf(page []byte, selectedTxn uint64, kind blobKind) (blobLeaf, error) {
	leaf, status := openBlobLeafStatus(page, selectedTxn, kind)
	return leaf, status.err()
}

func openBlobLeafStatus(page []byte, selectedTxn uint64, kind blobKind) (blobLeaf, blobPageStatus) {
	header, status := blobHeaderStatus(page, selectedTxn)
	if status.failed() {
		return blobLeaf{}, status
	}
	if header.PageType != PageTypeBlobLeaf {
		return blobLeaf{}, blobPageStatus{
			code:     blobPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	wireKind, valid := blobKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return blobLeaf{}, blobPageStatus{
			code:     blobPageErrWrongKind,
			wireKind: header.Aux,
		}
	}
	if header.ItemCount != 1 {
		return blobLeaf{}, blobPageStatus{
			code:      blobPageErrLeafItemCount,
			itemCount: header.ItemCount,
		}
	}
	dataLength := binary.LittleEndian.Uint16(page[40:42])
	if dataLength == 0 || int(dataLength) > blobLeafCapacity {
		return blobLeaf{}, blobPageStatus{
			code:       blobPageErrDataLength,
			dataLength: dataLength,
		}
	}
	alignment := kind.alignment()
	if alignment == 0 || uint64(dataLength)%alignment != 0 {
		return blobLeaf{}, blobPageStatus{
			code:       blobPageErrDataAlignment,
			dataLength: dataLength,
			alignment:  alignment,
		}
	}
	lower, ok := checkedAdd(blobLeafDataOffset, uint64(dataLength))
	if !ok || lower != uint64(header.Lower) || header.Upper != PageSize {
		return blobLeaf{}, blobPageStatus{code: blobPageErrFixedGeometry}
	}
	if anyNonzero(page[42:blobLeafDataOffset]) {
		return blobLeaf{}, blobPageStatus{code: blobPageErrReservedNonzero}
	}
	return blobLeaf{
		page:          page,
		logicalOffset: binary.LittleEndian.Uint64(page[32:40]),
		dataLength:    dataLength,
	}, blobPageStatus{}
}

func (l blobLeaf) data() []byte {
	return l.page[blobLeafDataOffset : blobLeafDataOffset+int(l.dataLength)]
}

func (l blobLeaf) verifyLocal() error {
	return l.verifyLocalStatus().err()
}

func (l blobLeaf) verifyLocalStatus() blobPageStatus {
	if !VerifyPageCRC32C(l.page) {
		return blobPageStatus{code: blobPageErrChecksum}
	}
	lower := blobLeafDataOffset + int(l.dataLength)
	if anyNonzero(l.page[lower:]) {
		return blobPageStatus{code: blobPageErrReservedNonzero}
	}
	return blobPageStatus{}
}
