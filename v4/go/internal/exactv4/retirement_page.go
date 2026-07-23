package exactv4

import (
	"encoding/binary"
	"fmt"
)

const (
	retirementBranchEntrySize = 16
	retirementLeafRecordSize  = 32
	maxRetirementBatchPages   = uint64(1) << 32
)

type retirementPageErrorCode uint8

const (
	retirementPageErrHeader retirementPageErrorCode = iota + 1
	retirementPageErrWrongPageType
	retirementPageErrWrongAux
	retirementPageErrFixedGeometry
	retirementPageErrEmptyPage
	retirementPageErrIndexOutOfBounds
	retirementPageErrChildOutOfBounds
	retirementPageErrReservedNonzero
	retirementPageErrKeysNotStrict
	retirementPageErrRetiredTransactionOutOfRange
	retirementPageErrBatchPageCountOutOfRange
	retirementPageErrBlobRootOutOfBounds
	retirementPageErrBlobLengthOverflow
	retirementPageErrChecksum
)

type retirementPageError struct {
	code        retirementPageErrorCode
	cause       error
	pageType    PageType
	wireAux     uint32
	childPage   uint32
	transaction uint64
	pageCount   uint64
	blobRoot    uint32
}

type retirementPageStatus struct {
	code        retirementPageErrorCode
	header      PageHeaderError
	hasHeader   bool
	pageType    PageType
	wireAux     uint32
	childPage   uint32
	transaction uint64
	pageCount   uint64
	blobRoot    uint32
}

func (status retirementPageStatus) failed() bool { return status.code != 0 }

func (status retirementPageStatus) asError() *retirementPageError {
	if !status.failed() {
		return nil
	}
	errorValue := &retirementPageError{
		code:        status.code,
		pageType:    status.pageType,
		wireAux:     status.wireAux,
		childPage:   status.childPage,
		transaction: status.transaction,
		pageCount:   status.pageCount,
		blobRoot:    status.blobRoot,
	}
	if status.hasHeader {
		header := status.header
		errorValue.cause = &header
	}
	return errorValue
}

func (status retirementPageStatus) err() error {
	if !status.failed() {
		return nil
	}
	return status.asError()
}

func retirementHeaderStatus(page []byte, selectedTxn uint64) (PageHeader, retirementPageStatus) {
	header, problem := decodePageHeaderNoAlloc(page, selectedTxn)
	if problem.code == 0 {
		return header, retirementPageStatus{}
	}
	return PageHeader{}, retirementPageStatus{
		code:      retirementPageErrHeader,
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

func (e *retirementPageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 retirement page: error %d", e.code)
}

func (e *retirementPageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type retirementBranchEntry struct {
	maxRetiredByTxn uint64
	childPage       uint32
}

type retirementBranch struct {
	page        []byte
	count       int
	level       uint16
	pageCount   uint64
	selectedTxn uint64
}

func openRetirementBranch(
	page []byte,
	selectedTxn uint64,
	pageCount uint64,
) (retirementBranch, error) {
	branch, status := openRetirementBranchStatus(page, selectedTxn, pageCount)
	return branch, status.err()
}

func openRetirementBranchStatus(
	page []byte,
	selectedTxn uint64,
	pageCount uint64,
) (retirementBranch, retirementPageStatus) {
	header, status := retirementHeaderStatus(page, selectedTxn)
	if status.failed() {
		return retirementBranch{}, status
	}
	if header.PageType != PageTypeRetirementBranch {
		return retirementBranch{}, retirementPageStatus{
			code:     retirementPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	if header.Aux != 0 {
		return retirementBranch{}, retirementPageStatus{
			code:    retirementPageErrWrongAux,
			wireAux: header.Aux,
		}
	}
	count := int(header.ItemCount)
	if count == 0 {
		return retirementBranch{}, retirementPageStatus{code: retirementPageErrEmptyPage}
	}
	bodyBytes, ok := checkedMul(uint64(count), retirementBranchEntrySize)
	if !ok {
		return retirementBranch{}, retirementPageStatus{code: retirementPageErrFixedGeometry}
	}
	lower, ok := checkedAdd(uint64(PageHeaderSize), bodyBytes)
	if !ok || lower != uint64(header.Lower) || header.Upper != PageSize {
		return retirementBranch{}, retirementPageStatus{code: retirementPageErrFixedGeometry}
	}
	if anyNonzero(page[int(lower):]) {
		return retirementBranch{}, retirementPageStatus{code: retirementPageErrReservedNonzero}
	}

	branch := retirementBranch{
		page:        page,
		count:       count,
		level:       header.Level,
		pageCount:   pageCount,
		selectedTxn: selectedTxn,
	}
	var previous uint64
	havePrevious := false
	for index := 0; index < count; index++ {
		entry, entryStatus := branch.entryStatus(index)
		if entryStatus.failed() {
			return retirementBranch{}, entryStatus
		}
		if havePrevious && entry.maxRetiredByTxn <= previous {
			return retirementBranch{}, retirementPageStatus{code: retirementPageErrKeysNotStrict}
		}
		previous = entry.maxRetiredByTxn
		havePrevious = true
	}
	return branch, retirementPageStatus{}
}

func (b retirementBranch) len() int { return b.count }

func (b retirementBranch) entry(index int) (retirementBranchEntry, error) {
	entry, status := b.entryStatus(index)
	return entry, status.err()
}

func (b retirementBranch) entryStatus(index int) (retirementBranchEntry, retirementPageStatus) {
	if index < 0 || index >= b.count {
		return retirementBranchEntry{}, retirementPageStatus{code: retirementPageErrIndexOutOfBounds}
	}
	at := int(PageHeaderSize) + index*retirementBranchEntrySize
	if binary.LittleEndian.Uint32(b.page[at+12:at+16]) != 0 {
		return retirementBranchEntry{}, retirementPageStatus{code: retirementPageErrReservedNonzero}
	}
	transaction := binary.LittleEndian.Uint64(b.page[at : at+8])
	if transaction <= 1 || transaction > b.selectedTxn {
		return retirementBranchEntry{}, retirementPageStatus{
			code:        retirementPageErrRetiredTransactionOutOfRange,
			transaction: transaction,
		}
	}
	childPage := binary.LittleEndian.Uint32(b.page[at+8 : at+12])
	if childPage < 2 || uint64(childPage) >= b.pageCount {
		return retirementBranchEntry{}, retirementPageStatus{
			code:      retirementPageErrChildOutOfBounds,
			childPage: childPage,
		}
	}
	return retirementBranchEntry{maxRetiredByTxn: transaction, childPage: childPage}, retirementPageStatus{}
}

func (b retirementBranch) maximumKey() (uint64, error) {
	key, status := b.maximumKeyStatus()
	return key, status.err()
}

func (b retirementBranch) maximumKeyStatus() (uint64, retirementPageStatus) {
	entry, status := b.entryStatus(b.count - 1)
	if status.failed() {
		return 0, status
	}
	return entry.maxRetiredByTxn, retirementPageStatus{}
}

func (b retirementBranch) verifyCRC() error {
	return b.verifyCRCStatus().err()
}

func (b retirementBranch) verifyCRCStatus() retirementPageStatus {
	if !VerifyPageCRC32C(b.page) {
		return retirementPageStatus{code: retirementPageErrChecksum}
	}
	return retirementPageStatus{}
}

type retirementBatch struct {
	retiredByTxn     uint64
	pageCount        uint64
	pageListBlobRoot uint32
}

func (b retirementBatch) blobLength() (uint64, error) {
	length, status := b.blobLengthStatus()
	return length, status.err()
}

func (b retirementBatch) blobLengthStatus() (uint64, retirementPageStatus) {
	length, ok := checkedMul(b.pageCount, 4)
	if !ok {
		return 0, retirementPageStatus{code: retirementPageErrBlobLengthOverflow}
	}
	return length, retirementPageStatus{}
}

type retirementLeaf struct {
	page        []byte
	count       int
	selectedTxn uint64
	pageCount   uint64
}

func openRetirementLeaf(
	page []byte,
	selectedTxn uint64,
	pageCount uint64,
) (retirementLeaf, error) {
	leaf, status := openRetirementLeafStatus(page, selectedTxn, pageCount)
	return leaf, status.err()
}

func openRetirementLeafStatus(
	page []byte,
	selectedTxn uint64,
	pageCount uint64,
) (retirementLeaf, retirementPageStatus) {
	header, status := retirementHeaderStatus(page, selectedTxn)
	if status.failed() {
		return retirementLeaf{}, status
	}
	if header.PageType != PageTypeRetirementLeaf {
		return retirementLeaf{}, retirementPageStatus{
			code:     retirementPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	if header.Aux != 0 {
		return retirementLeaf{}, retirementPageStatus{
			code:    retirementPageErrWrongAux,
			wireAux: header.Aux,
		}
	}
	count := int(header.ItemCount)
	if count == 0 {
		return retirementLeaf{}, retirementPageStatus{code: retirementPageErrEmptyPage}
	}
	bodyBytes, ok := checkedMul(uint64(count), retirementLeafRecordSize)
	if !ok {
		return retirementLeaf{}, retirementPageStatus{code: retirementPageErrFixedGeometry}
	}
	lower, ok := checkedAdd(uint64(PageHeaderSize), bodyBytes)
	if !ok || lower != uint64(header.Lower) || header.Upper != PageSize {
		return retirementLeaf{}, retirementPageStatus{code: retirementPageErrFixedGeometry}
	}
	if anyNonzero(page[int(lower):]) {
		return retirementLeaf{}, retirementPageStatus{code: retirementPageErrReservedNonzero}
	}

	leaf := retirementLeaf{
		page:        page,
		count:       count,
		selectedTxn: selectedTxn,
		pageCount:   pageCount,
	}
	var previous uint64
	havePrevious := false
	for index := 0; index < count; index++ {
		batch, batchStatus := leaf.batchStatus(index)
		if batchStatus.failed() {
			return retirementLeaf{}, batchStatus
		}
		if havePrevious && batch.retiredByTxn <= previous {
			return retirementLeaf{}, retirementPageStatus{code: retirementPageErrKeysNotStrict}
		}
		previous = batch.retiredByTxn
		havePrevious = true
	}
	return leaf, retirementPageStatus{}
}

func (l retirementLeaf) len() int { return l.count }

func (l retirementLeaf) batch(index int) (retirementBatch, error) {
	batch, status := l.batchStatus(index)
	return batch, status.err()
}

func (l retirementLeaf) batchStatus(index int) (retirementBatch, retirementPageStatus) {
	if index < 0 || index >= l.count {
		return retirementBatch{}, retirementPageStatus{code: retirementPageErrIndexOutOfBounds}
	}
	at := int(PageHeaderSize) + index*retirementLeafRecordSize
	if binary.LittleEndian.Uint64(l.page[at:at+8]) != 0 ||
		binary.LittleEndian.Uint32(l.page[at+28:at+32]) != 0 {
		return retirementBatch{}, retirementPageStatus{code: retirementPageErrReservedNonzero}
	}
	transaction := binary.LittleEndian.Uint64(l.page[at+8 : at+16])
	if transaction <= 1 || transaction > l.selectedTxn {
		return retirementBatch{}, retirementPageStatus{
			code:        retirementPageErrRetiredTransactionOutOfRange,
			transaction: transaction,
		}
	}
	pageCount := binary.LittleEndian.Uint64(l.page[at+16 : at+24])
	if pageCount == 0 || pageCount > maxRetirementBatchPages {
		return retirementBatch{}, retirementPageStatus{
			code:      retirementPageErrBatchPageCountOutOfRange,
			pageCount: pageCount,
		}
	}
	blobRoot := binary.LittleEndian.Uint32(l.page[at+24 : at+28])
	if blobRoot < 2 || uint64(blobRoot) >= l.pageCount {
		return retirementBatch{}, retirementPageStatus{
			code:     retirementPageErrBlobRootOutOfBounds,
			blobRoot: blobRoot,
		}
	}
	batch := retirementBatch{
		retiredByTxn:     transaction,
		pageCount:        pageCount,
		pageListBlobRoot: blobRoot,
	}
	if _, status := batch.blobLengthStatus(); status.failed() {
		return retirementBatch{}, status
	}
	return batch, retirementPageStatus{}
}

func (l retirementLeaf) maximumKey() (uint64, error) {
	key, status := l.maximumKeyStatus()
	return key, status.err()
}

func (l retirementLeaf) maximumKeyStatus() (uint64, retirementPageStatus) {
	batch, status := l.batchStatus(l.count - 1)
	if status.failed() {
		return 0, status
	}
	return batch.retiredByTxn, retirementPageStatus{}
}

func (l retirementLeaf) verifyCRC() error {
	return l.verifyCRCStatus().err()
}

func (l retirementLeaf) verifyCRCStatus() retirementPageStatus {
	if !VerifyPageCRC32C(l.page) {
		return retirementPageStatus{code: retirementPageErrChecksum}
	}
	return retirementPageStatus{}
}
