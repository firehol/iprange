package exactv4

import (
	"errors"
	"io"
	"os"
	"syscall"
)

type pageIOKind uint8

const (
	pageIOUnknown pageIOKind = iota
	pageIONotFound
	pageIOPermissionDenied
	pageIOUnexpectedEOF
	pageIOInvalidInput
	pageIOInterrupted
	pageIOOutOfMemory
	pageIOOther
)

type pageIOEvidence struct {
	kind         pageIOKind
	rawOSCode    uint64
	hasRawOSCode bool
}

func pageIOEvidenceFromError(err error) pageIOEvidence {
	evidence := pageIOEvidence{kind: pageIOOther}
	switch {
	case errors.Is(err, os.ErrNotExist):
		evidence.kind = pageIONotFound
	case errors.Is(err, os.ErrPermission):
		evidence.kind = pageIOPermissionDenied
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		evidence.kind = pageIOUnexpectedEOF
	case errors.Is(err, os.ErrInvalid):
		evidence.kind = pageIOInvalidInput
	case errors.Is(err, syscall.EINTR):
		evidence.kind = pageIOInterrupted
	case errors.Is(err, syscall.ENOMEM):
		evidence.kind = pageIOOutOfMemory
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		evidence.rawOSCode = uint64(errno)
		evidence.hasRawOSCode = true
	}
	return evidence
}

type pageSourceErrorCode uint8

const (
	pageSourceErrPageOutOfBounds pageSourceErrorCode = iota + 1
	pageSourceErrOffsetOverflow
	pageSourceErrCommittedLengthMismatch
	pageSourceErrForkedHandle
	pageSourceErrShortRead
	pageSourceErrIO
)

type pageSourceError struct {
	code     pageSourceErrorCode
	page     uint32
	offset   uint64
	expected int
	actual   int
	evidence pageIOEvidence
	cause    error
}

// pageSourceStatus is the allocation-free committed-page failure contract.
// The zero value is success. It deliberately contains only scalar evidence so
// it can cross live under-lock readers without pointer or interface boxing.
type pageSourceStatus struct {
	code      pageSourceErrorCode
	page      uint32
	offset    uint64
	expected  int
	actual    int
	ioKind    pageIOKind
	rawOSCode uint64
	hasRaw    bool
}

func (status pageSourceStatus) failed() bool { return status.code != 0 }

func (status pageSourceStatus) asError() *pageSourceError {
	if !status.failed() {
		return nil
	}
	errorValue := &pageSourceError{
		code:     status.code,
		page:     status.page,
		offset:   status.offset,
		expected: status.expected,
		actual:   status.actual,
		evidence: pageIOEvidence{
			kind:         status.ioKind,
			rawOSCode:    status.rawOSCode,
			hasRawOSCode: status.hasRaw,
		},
	}
	if status.code == pageSourceErrIO && status.hasRaw {
		errorValue.cause = syscall.Errno(status.rawOSCode)
	} else if status.code == pageSourceErrShortRead {
		errorValue.cause = io.ErrUnexpectedEOF
	}
	return errorValue
}

func (status pageSourceStatus) err() error {
	if !status.failed() {
		return nil
	}
	return status.asError()
}

func (e *pageSourceError) status() pageSourceStatus {
	if e == nil {
		return pageSourceStatus{}
	}
	return pageSourceStatus{
		code:      e.code,
		page:      e.page,
		offset:    e.offset,
		expected:  e.expected,
		actual:    e.actual,
		ioKind:    e.evidence.kind,
		rawOSCode: e.evidence.rawOSCode,
		hasRaw:    e.evidence.hasRawOSCode,
	}
}

func (e *pageSourceError) Error() string { return "exact v4 positional page read failed" }

func (e *pageSourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type pageAccessKind uint8

const (
	pageAccessPublicEntry pageAccessKind = iota + 1
	pageAccessRead
)

type pageAccessCheck interface {
	checkPageAccessStatus(pageAccessKind) pageSourceStatus
}

type unrestrictedPageAccess struct{}

func (unrestrictedPageAccess) checkPageAccessStatus(pageAccessKind) pageSourceStatus {
	return pageSourceStatus{}
}

type processPageAccess struct {
	creatorPID int
}

func (access *processPageAccess) checkPageAccessStatus(pageAccessKind) pageSourceStatus {
	if os.Getpid() != access.creatorPID {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	return pageSourceStatus{}
}

type positionalPageRead struct {
	data   []byte
	file   *os.File
	fd     int
	access pageAccessCheck
}

// committedPageSource is the single read boundary for selected committed
// pages. Implementations copy into caller-owned storage and never expose live
// file bytes through a borrowed slice.
type committedPageSource interface {
	checkAccessStatus() pageSourceStatus
	readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus
}

// immutableSlicePageSource keeps the declared committed extent independent
// from the physical slice length so truncation retains exact positional
// short-read evidence.
type immutableSlicePageSource struct {
	data      []byte
	pageCount uint64
}

func newImmutableSlicePageSource(data []byte, pageCount uint64) immutableSlicePageSource {
	return immutableSlicePageSource{data: data, pageCount: pageCount}
}

func (immutableSlicePageSource) checkAccessStatus() pageSourceStatus { return pageSourceStatus{} }

func (source immutableSlicePageSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	if pageNumber < 2 || uint64(pageNumber) >= source.pageCount {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	offset, ok := checkedMul(uint64(pageNumber), PageSize)
	if !ok {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow, page: pageNumber}
	}
	end, ok := checkedAdd(offset, PageSize)
	if !ok {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow, page: pageNumber}
	}
	if end > uint64(len(source.data)) {
		actual := 0
		if offset < uint64(len(source.data)) {
			actual = len(source.data) - int(offset)
			if actual > PageSize {
				actual = PageSize
			}
		}
		return pageSourceStatus{
			code:     pageSourceErrShortRead,
			page:     pageNumber,
			offset:   offset,
			expected: PageSize,
			actual:   actual,
		}
	}
	startAt, endAt := int(offset), int(end)
	if uint64(startAt) != offset || uint64(endAt) != end {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow, page: pageNumber}
	}
	copy(destination[:], source.data[startAt:endAt])
	return pageSourceStatus{}
}

func (source immutableSlicePageSource) readPage(
	pageNumber uint32,
	destination *[PageSize]byte,
) *pageSourceError {
	return source.readPageStatus(pageNumber, destination).asError()
}

func newSlicePageRead(data []byte) positionalPageRead {
	return positionalPageRead{data: data, fd: -1, access: unrestrictedPageAccess{}}
}

func newFilePageRead(file *os.File, creatorPID int) positionalPageRead {
	fd := -1
	if file != nil {
		fd = int(file.Fd())
	}
	return positionalPageRead{
		file:   file,
		fd:     fd,
		access: &processPageAccess{creatorPID: creatorPID},
	}
}

func (source *positionalPageRead) checkPageAccess() *pageSourceError {
	return source.checkPageAccessStatus().asError()
}

func (source *positionalPageRead) checkPageAccessStatus() pageSourceStatus {
	return source.access.checkPageAccessStatus(pageAccessPublicEntry)
}

func (source *positionalPageRead) readPageAtStatus(
	offset uint64,
	page *[PageSize]byte,
) pageSourceStatus {
	if status := source.access.checkPageAccessStatus(pageAccessRead); status.failed() {
		return status
	}
	if source.file != nil {
		return readFilePageAtStatus(source.file, source.fd, offset, page)
	}
	end, ok := checkedAdd(offset, PageSize)
	if !ok {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow}
	}
	if end > uint64(len(source.data)) {
		actual := 0
		if offset < uint64(len(source.data)) {
			actual = len(source.data) - int(offset)
			if actual > PageSize {
				actual = PageSize
			}
		}
		return pageSourceStatus{
			code:     pageSourceErrShortRead,
			offset:   offset,
			expected: PageSize,
			actual:   actual,
		}
	}
	startAt, endAt := int(offset), int(end)
	if uint64(startAt) != offset || uint64(endAt) != end {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow}
	}
	copy(page[:], source.data[startAt:endAt])
	return pageSourceStatus{}
}

func (source *positionalPageRead) readPageAt(
	offset uint64,
	page *[PageSize]byte,
) *pageSourceError {
	return source.readPageAtStatus(offset, page).asError()
}

func readFilePageAt(file *os.File, offset uint64, page *[PageSize]byte) *pageSourceError {
	return readFilePageAtStatus(file, int(file.Fd()), offset, page).asError()
}

type pinnedPageSource struct {
	source    positionalPageRead
	bootstrap Bootstrap
}

func newPinnedPageSource(source positionalPageRead, bootstrap Bootstrap) (pinnedPageSource, *pageSourceError) {
	expected, ok := checkedMul(bootstrap.Meta.PageCount, PageSize)
	if !ok {
		return pinnedPageSource{}, &pageSourceError{code: pageSourceErrOffsetOverflow}
	}
	if expected != bootstrap.CommittedBytes {
		return pinnedPageSource{}, &pageSourceError{code: pageSourceErrCommittedLengthMismatch}
	}
	return pinnedPageSource{source: source, bootstrap: bootstrap}, nil
}

func (source *pinnedPageSource) readPageStatus(
	pageNumber uint32,
	page *[PageSize]byte,
) pageSourceStatus {
	if pageNumber < 2 || uint64(pageNumber) >= source.bootstrap.Meta.PageCount {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	offset, ok := checkedMul(uint64(pageNumber), PageSize)
	if !ok {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow, page: pageNumber}
	}
	end, ok := checkedAdd(offset, PageSize)
	if !ok {
		return pageSourceStatus{code: pageSourceErrOffsetOverflow, page: pageNumber}
	}
	if end > source.bootstrap.CommittedBytes {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	status := source.source.readPageAtStatus(offset, page)
	status.page = pageNumber
	return status
}

func (source *pinnedPageSource) readPage(
	pageNumber uint32,
	page *[PageSize]byte,
) *pageSourceError {
	return source.readPageStatus(pageNumber, page).asError()
}

func (source *pinnedPageSource) checkAccessStatus() pageSourceStatus {
	return source.source.checkPageAccessStatus()

}

func (source *pinnedPageSource) checkAccess() *pageSourceError {
	return source.checkAccessStatus().asError()
}
