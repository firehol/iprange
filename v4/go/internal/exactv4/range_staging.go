package exactv4

import (
	"encoding/binary"
	"fmt"
)

// rangeTreeStagingPage is one fixed logical page slot. It is private
// operation workspace, never a physical v4 page identity.
type rangeTreeStagingPage struct {
	bytes [PageSize]byte
}

func (p rangeTreeStagingPage) empty() bool {
	return p.bytes == ([PageSize]byte{})
}

// rangeTreePhysicalAssignment is one allocator-selected physical destination
// for the matching logical page slot. The caller obtains this authority only
// under the live finalization lock.
type rangeTreePhysicalAssignment struct {
	pageNumber    uint32
	authorization privatePageAuthorization
}

type rangeTreeStagedResult struct {
	logicalRoot uint32
	rootLevel   uint16
	recordCount uint64
	pageCount   int
}

type rangeTreeMaterializedResult struct {
	rootPage    uint32
	rootLevel   uint16
	recordCount uint64
	pageCount   int
}

type rangeTreeStagingErrorCode uint8

const (
	rangeTreeStagingErrBornTransactionZero rangeTreeStagingErrorCode = iota + 1
	rangeTreeStagingErrLogicalPageCapacity
	rangeTreeStagingErrWorkspaceDirty
	rangeTreeStagingErrFinished
	rangeTreeStagingErrCapacityExhausted
	rangeTreeStagingErrInvalidEncodedPage
	rangeTreeStagingErrInvalidLogicalResult
	rangeTreeStagingErrStagedResultMismatch
	rangeTreeStagingErrFinalPageCount
	rangeTreeStagingErrAssignmentCount
	rangeTreeStagingErrTerminalOutputCount
	rangeTreeStagingErrTerminalOutputDirty
	rangeTreeStagingErrPhysicalPageOutOfBounds
	rangeTreeStagingErrPhysicalPageOrder
	rangeTreeStagingErrInvalidStagedPage
	rangeTreeStagingErrLogicalChildOutOfBounds
	rangeTreeStagingErrLogicalChildOrder
)

type rangeTreeStagingError struct {
	code     rangeTreeStagingErrorCode
	capacity int
	required int
	actual   int
	page     uint32
	previous uint32
	index    int
	child    uint32
}

func (e *rangeTreeStagingError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range tree staging: error %d", e.code)
}

// rangeTreeStaging is a fixed-capacity logical sink for one ordered range
// tree build. The ordered builder sees temporary IDs 2..N+1 only; they are
// translated to allocator-authorized physical pages at materialization.
type rangeTreeStaging[K rangeKey[K]] struct {
	pages            []rangeTreeStagingPage
	bornTxn          uint64
	valueKind        ValueKind
	logicalPageLimit uint64
	length           int
	finished         bool
}

func newRangeTreeStaging[K rangeKey[K]](
	pages []rangeTreeStagingPage,
	bornTxn uint64,
	valueKind ValueKind,
) (rangeTreeStaging[K], error) {
	if bornTxn == 0 {
		return rangeTreeStaging[K]{}, &rangeTreeStagingError{code: rangeTreeStagingErrBornTransactionZero}
	}
	capacity := uint64(len(pages))
	if capacity > MaxPageCount-2 {
		return rangeTreeStaging[K]{}, &rangeTreeStagingError{
			code: rangeTreeStagingErrLogicalPageCapacity, capacity: len(pages),
		}
	}
	for index := range pages {
		if !pages[index].empty() {
			return rangeTreeStaging[K]{}, &rangeTreeStagingError{code: rangeTreeStagingErrWorkspaceDirty}
		}
	}
	return rangeTreeStaging[K]{
		pages:            pages,
		bornTxn:          bornTxn,
		valueKind:        valueKind,
		logicalPageLimit: capacity + 2,
	}, nil
}

// logicalPageCount is the temporary page-count bound supplied to the existing
// builder. No value in this range is a final file page number.
func (s *rangeTreeStaging[K]) logicalPageCount() uint64 {
	if s == nil {
		return 0
	}
	return s.logicalPageLimit
}

func (s *rangeTreeStaging[K]) bornTransaction() uint64 {
	if s == nil {
		return 0
	}
	return s.bornTxn
}

func (s *rangeTreeStaging[K]) len() int {
	if s == nil {
		return 0
	}
	return s.length
}

// discardAfterAbort erases unpublished logical output after the enclosing
// draft has been abandoned. The stale staging object stays finished so it
// cannot be reused with its old transaction generation.
func (s *rangeTreeStaging[K]) discardAfterAbort() {
	if s == nil {
		return
	}
	clear(s.pages)
	s.length = 0
	s.finished = true
}

func (s *rangeTreeStaging[K]) finish(result rangeTreeBuildResult) (rangeTreeStagedResult, error) {
	if s == nil || s.finished {
		return rangeTreeStagedResult{}, &rangeTreeStagingError{code: rangeTreeStagingErrFinished}
	}
	if (s.length == 0 && result.rootPage != 0) ||
		(s.length != 0 && (result.rootPage < 2 || uint64(result.rootPage-2) >= uint64(s.length))) {
		return rangeTreeStagedResult{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidLogicalResult}
	}
	s.finished = true
	return rangeTreeStagedResult{
		logicalRoot: result.rootPage,
		rootLevel:   result.rootLevel,
		recordCount: result.recordCount,
		pageCount:   s.length,
	}, nil
}

func (s *rangeTreeStaging[K]) checkStagedResult(result rangeTreeStagedResult) error {
	if s == nil || !s.finished || result.pageCount != s.length ||
		(s.length == 0 && result.logicalRoot != 0) ||
		(s.length != 0 && (result.logicalRoot < 2 || uint64(result.logicalRoot-2) >= uint64(s.length))) {
		return &rangeTreeStagingError{code: rangeTreeStagingErrStagedResultMismatch}
	}
	return nil
}

func (s *rangeTreeStaging[K]) validateLeaf(page *[PageSize]byte) error {
	var key K
	// This is operation-private output from the ordered builder, not an
	// input-file validation pass. Check only the geometry needed to hand the
	// page to the terminal coordinator.
	if _, err := openRangeLeaf[K](page[:], s.bornTxn, key.family(), s.valueKind); err != nil {
		return &rangeTreeStagingError{code: rangeTreeStagingErrInvalidEncodedPage}
	}
	return nil
}

func rangeTreeStagingBranchChildOffset[K rangeKey[K]](index int) int {
	var key K
	offset := int(PageHeaderSize) + index*rangeBranchEntrySize[K]()
	if key.width() == 4 {
		return offset + 4
	}
	return offset + 16
}

func (s *rangeTreeStaging[K]) validateBranch(pageIndex int, page *[PageSize]byte) error {
	var key K
	branch, err := openRangeBranch[K](page[:], s.bornTxn, key.family(), s.logicalPageLimit)
	if err != nil {
		return &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: pageIndex}
	}
	for index := 0; index < branch.count; index++ {
		childOffset := rangeTreeStagingBranchChildOffset[K](index)
		child := binary.LittleEndian.Uint32(page[childOffset : childOffset+4])
		if child < 2 || uint64(child-2) >= uint64(s.length) {
			return &rangeTreeStagingError{
				code:  rangeTreeStagingErrLogicalChildOutOfBounds,
				index: pageIndex,
				child: child,
			}
		}
		if int(child-2) >= pageIndex {
			return &rangeTreeStagingError{
				code:  rangeTreeStagingErrLogicalChildOrder,
				index: pageIndex,
				child: child,
			}
		}
	}
	return nil
}

func (s *rangeTreeStaging[K]) validateStagingPage(index int) (PageHeader, error) {
	if s == nil || index < 0 || index >= len(s.pages) {
		return PageHeader{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: index}
	}
	page := &s.pages[index].bytes
	header, err := DecodePageHeader(page[:], s.bornTxn)
	if err != nil || !VerifyPageCRC32C(page[:]) {
		return PageHeader{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: index}
	}
	var key K
	if header.Aux != uint32(key.family()) {
		return PageHeader{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: index}
	}
	switch header.PageType {
	case PageTypeRangeLeaf:
		if err = s.validateLeaf(page); err != nil {
			return PageHeader{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: index}
		}
	case PageTypeRangeBranch:
		if err = s.validateBranch(index, page); err != nil {
			return PageHeader{}, err
		}
	default:
		return PageHeader{}, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidStagedPage, index: index}
	}
	return header, nil
}

func (s *rangeTreeStaging[K]) validateMaterialization(
	result rangeTreeStagedResult,
	finalPageCount uint64,
	assignments []rangeTreePhysicalAssignment,
	output []privateWriterProducedTerminalPage,
) error {
	if err := s.checkStagedResult(result); err != nil {
		return err
	}
	if finalPageCount < 2 || finalPageCount > MaxPageCount {
		return &rangeTreeStagingError{code: rangeTreeStagingErrFinalPageCount}
	}
	if len(assignments) != s.length {
		return &rangeTreeStagingError{
			code: rangeTreeStagingErrAssignmentCount, required: s.length, actual: len(assignments),
		}
	}
	if len(output) != s.length {
		return &rangeTreeStagingError{
			code: rangeTreeStagingErrTerminalOutputCount, required: s.length, actual: len(output),
		}
	}
	var empty privateWriterProducedTerminalPage
	for index := range output {
		if output[index] != empty {
			return &rangeTreeStagingError{code: rangeTreeStagingErrTerminalOutputDirty}
		}
	}
	var previous uint32
	havePrevious := false
	for index := range assignments {
		assignment := assignments[index]
		if assignment.pageNumber < 2 || uint64(assignment.pageNumber) >= finalPageCount {
			return &rangeTreeStagingError{code: rangeTreeStagingErrPhysicalPageOutOfBounds, page: assignment.pageNumber}
		}
		if havePrevious && assignment.pageNumber <= previous {
			return &rangeTreeStagingError{
				code: rangeTreeStagingErrPhysicalPageOrder, previous: previous, page: assignment.pageNumber,
			}
		}
		if _, err := s.validateStagingPage(index); err != nil {
			return err
		}
		previous, havePrevious = assignment.pageNumber, true
	}
	return nil
}

func (s *rangeTreeStaging[K]) patchBranchChildren(
	page *[PageSize]byte,
	count int,
	assignments []rangeTreePhysicalAssignment,
) {
	for index := 0; index < count; index++ {
		childOffset := rangeTreeStagingBranchChildOffset[K](index)
		logical := binary.LittleEndian.Uint32(page[childOffset : childOffset+4])
		physical := assignments[int(logical-2)].pageNumber
		binary.LittleEndian.PutUint32(page[childOffset:childOffset+4], physical)
	}
}

// materialize converts one fully staged logical tree to allocator-authorized
// terminal pages. All fallible checks happen before it changes output.
func (s *rangeTreeStaging[K]) materialize(
	result rangeTreeStagedResult,
	finalPageCount uint64,
	assignments []rangeTreePhysicalAssignment,
	output []privateWriterProducedTerminalPage,
) (rangeTreeMaterializedResult, error) {
	if err := s.validateMaterialization(result, finalPageCount, assignments, output); err != nil {
		return rangeTreeMaterializedResult{}, err
	}
	for index := range output {
		output[index] = privateWriterProducedTerminalPage{
			pageNumber:    assignments[index].pageNumber,
			authorization: assignments[index].authorization,
			owner:         privatePageOwnerRange,
			origin:        privatePageRange,
		}
		output[index].bytes = s.pages[index].bytes
		if output[index].bytes[4] == byte(PageTypeRangeBranch) {
			count := int(binary.LittleEndian.Uint16(output[index].bytes[16:18]))
			s.patchBranchChildren(&output[index].bytes, count, assignments)
			binary.LittleEndian.PutUint32(
				output[index].bytes[PageCRCOffset:PageCRCOffset+4],
				pageCRC32C(output[index].bytes[:]),
			)
		}
	}
	rootPage := uint32(0)
	if result.logicalRoot != 0 {
		rootPage = assignments[int(result.logicalRoot-2)].pageNumber
	}
	return rangeTreeMaterializedResult{
		rootPage: rootPage, rootLevel: result.rootLevel,
		recordCount: result.recordCount, pageCount: s.length,
	}, nil
}

func (s *rangeTreeStaging[K]) writeRangePage(page *[PageSize]byte) (uint32, error) {
	if s == nil || s.finished {
		return 0, &rangeTreeStagingError{code: rangeTreeStagingErrFinished}
	}
	if s.length == len(s.pages) {
		return 0, &rangeTreeStagingError{
			code:     rangeTreeStagingErrCapacityExhausted,
			required: s.length + 1,
			actual:   len(s.pages),
		}
	}
	if page == nil {
		return 0, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidEncodedPage}
	}
	header, err := DecodePageHeader(page[:], s.bornTxn)
	var key K
	if err != nil || !VerifyPageCRC32C(page[:]) || header.Aux != uint32(key.family()) ||
		(header.PageType != PageTypeRangeLeaf && header.PageType != PageTypeRangeBranch) {
		return 0, &rangeTreeStagingError{code: rangeTreeStagingErrInvalidEncodedPage}
	}
	logical := uint64(s.length) + 2
	if logical >= s.logicalPageLimit || logical > uint64(^uint32(0)) {
		return 0, &rangeTreeStagingError{code: rangeTreeStagingErrLogicalPageCapacity, capacity: len(s.pages)}
	}
	s.pages[s.length].bytes = *page
	s.length++
	return uint32(logical), nil
}
