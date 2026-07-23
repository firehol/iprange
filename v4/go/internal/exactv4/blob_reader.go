package exactv4

import (
	"encoding/binary"
	"fmt"
)

const blobPathCapacity = int(MaxTreeLevel) + 1

type blobPageCheck uint8

const (
	blobPageCheckOrdinary blobPageCheck = iota + 1
	blobPageCheckVerified
)

type blobReadErrorCode uint8

const (
	blobReadErrSource blobReadErrorCode = iota + 1
	blobReadErrPage
	blobReadErrPageOutOfBounds
	blobReadErrPageOffsetOverflow
	blobReadErrOwnerLengthZero
	blobReadErrOwnerLengthAlignment
	blobReadErrOwnerLengthTooLarge
	blobReadErrRootType
	blobReadErrChildType
	blobReadErrChildLevel
	blobReadErrOffsetMismatch
	blobReadErrLogicalOffsetOverflow
	blobReadErrRequestOutsideLength
	blobReadErrNonfinalLeafLength
	blobReadErrLengthExceeded
	blobReadErrLengthShort
	blobReadErrTrailingData
	blobReadErrRetirementPageOrder
	blobReadErrRetirementPageOutOfBounds
	blobReadErrWrongRetirementStreamKind
	blobReadErrPathChanged
	blobReadErrCursorFailed
)

type blobReadError struct {
	code          blobReadErrorCode
	cause         error
	page          uint32
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
	length        uint64
	alignment     uint64
	expected      uint64
	actual        uint64
	dataLength    uint16
	previousPage  uint32
	currentPage   uint32
}

type blobReadStatus struct {
	code          blobReadErrorCode
	source        pageSourceStatus
	pageProblem   blobPageStatus
	page          uint32
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
	length        uint64
	alignment     uint64
	expected      uint64
	actual        uint64
	dataLength    uint16
	previousPage  uint32
	currentPage   uint32
}

func (status blobReadStatus) failed() bool { return status.code != 0 }

func (status blobReadStatus) asError() *blobReadError {
	if !status.failed() {
		return nil
	}
	errorValue := &blobReadError{
		code:          status.code,
		page:          status.page,
		pageType:      status.pageType,
		expectedLevel: status.expectedLevel,
		actualLevel:   status.actualLevel,
		length:        status.length,
		alignment:     status.alignment,
		expected:      status.expected,
		actual:        status.actual,
		dataLength:    status.dataLength,
		previousPage:  status.previousPage,
		currentPage:   status.currentPage,
	}
	switch status.code {
	case blobReadErrSource:
		errorValue.cause = status.source.asError()
	case blobReadErrPage:
		errorValue.cause = status.pageProblem.asError()
	}
	return errorValue
}

func (status blobReadStatus) err() error {
	if !status.failed() {
		return nil
	}
	return status.asError()
}

func (e *blobReadError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 blob read: error %d", e.code)
}

func (e *blobReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type blobChunk struct {
	logicalOffset uint64
	data          []byte
}

type blobTree[S committedPageSource] struct {
	pages       S
	selectedTxn uint64
	pageCount   uint64
	root        uint32
	kind        blobKind
	length      uint64
}

func newBlobTree(
	data []byte,
	selectedTxn uint64,
	pageCount uint64,
	root uint32,
	kind blobKind,
	length uint64,
) (blobTree[immutableSlicePageSource], error) {
	committedBytes, ok := checkedMul(pageCount, PageSize)
	if !ok {
		return blobTree[immutableSlicePageSource]{}, &blobReadError{code: blobReadErrPageOffsetOverflow}
	}
	committed := int(committedBytes)
	if uint64(committed) != committedBytes {
		return blobTree[immutableSlicePageSource]{}, &blobReadError{code: blobReadErrPageOffsetOverflow}
	}
	if committed > len(data) {
		return blobTree[immutableSlicePageSource]{}, &blobReadError{
			code: blobReadErrPageOutOfBounds,
			page: root,
		}
	}
	return newBlobTreeFromSource(
		newImmutableSlicePageSource(data[:committed], pageCount),
		selectedTxn,
		pageCount,
		root,
		kind,
		length,
	)
}

func newBlobTreeFromSource[S committedPageSource](
	pages S,
	selectedTxn uint64,
	pageCount uint64,
	root uint32,
	kind blobKind,
	length uint64,
) (blobTree[S], error) {
	tree, status := newBlobTreeFromSourceStatus(pages, selectedTxn, pageCount, root, kind, length)
	return tree, status.err()
}

func newBlobTreeFromSourceStatus[S committedPageSource](
	pages S,
	selectedTxn uint64,
	pageCount uint64,
	root uint32,
	kind blobKind,
	length uint64,
) (blobTree[S], blobReadStatus) {
	if length == 0 {
		return blobTree[S]{}, blobReadStatus{code: blobReadErrOwnerLengthZero}
	}
	alignment := kind.alignment()
	if alignment == 0 || length%alignment != 0 {
		return blobTree[S]{}, blobReadStatus{code: blobReadErrOwnerLengthAlignment, length: length, alignment: alignment}
	}
	if root < 2 || uint64(root) >= pageCount {
		return blobTree[S]{}, blobReadStatus{code: blobReadErrPageOutOfBounds, page: root}
	}
	maximumLength, ok := checkedMul(pageCount-2, blobLeafCapacity)
	if !ok || length > maximumLength {
		return blobTree[S]{}, blobReadStatus{code: blobReadErrOwnerLengthTooLarge}
	}
	return blobTree[S]{
		pages:       pages,
		selectedTxn: selectedTxn,
		pageCount:   pageCount,
		root:        root,
		kind:        kind,
		length:      length,
	}, blobReadStatus{}
}

func (t *blobTree[S]) stream(check blobPageCheck) blobReader[S] {
	return blobReader[S]{tree: *t, check: check, state: blobReaderUnpositioned}
}

func (t *blobTree[S]) streamWithWorkspace(
	check blobPageCheck,
	workspace *blobReadWorkspace[S],
) *blobReader[S] {
	workspace.reader.reset(*t, check)
	return &workspace.reader
}

func (t *blobTree[S]) retirementPages(check blobPageCheck) (blobRetirementPageReader[S], error) {
	if status := t.pages.checkAccessStatus(); status.failed() {
		return blobRetirementPageReader[S]{}, &blobReadError{code: blobReadErrSource, cause: status.asError()}
	}
	if t.kind != blobKindRetirementPageList {
		return blobRetirementPageReader[S]{}, &blobReadError{
			code:   blobReadErrWrongRetirementStreamKind,
			actual: uint64(t.kind),
		}
	}
	return blobRetirementPageReader[S]{reader: t.stream(check)}, nil
}

func (t *blobTree[S]) retirementPagesWithWorkspace(
	check blobPageCheck,
	workspace *blobRetirementPageReader[S],
) (*blobRetirementPageReader[S], error) {
	reader, status := t.retirementPagesWithWorkspaceStatus(check, workspace)
	return reader, status.err()
}

func (t *blobTree[S]) retirementPagesWithWorkspaceStatus(
	check blobPageCheck,
	workspace *blobRetirementPageReader[S],
) (*blobRetirementPageReader[S], blobReadStatus) {
	if sourceStatus := t.pages.checkAccessStatus(); sourceStatus.failed() {
		return nil, blobReadStatus{code: blobReadErrSource, source: sourceStatus}
	}
	if t.kind != blobKindRetirementPageList {
		return nil, blobReadStatus{
			code:   blobReadErrWrongRetirementStreamKind,
			actual: uint64(t.kind),
		}
	}
	workspace.reset(*t, check)
	return workspace, blobReadStatus{}
}

func (t *blobTree[S]) chunkAt(
	logicalOffset uint64,
	check blobPageCheck,
	page *[PageSize]byte,
) (blobChunk, error) {
	chunk, status := t.chunkAtStatus(logicalOffset, check, page)
	return chunk, status.err()
}

func (t *blobTree[S]) chunkAtStatus(
	logicalOffset uint64,
	check blobPageCheck,
	page *[PageSize]byte,
) (blobChunk, blobReadStatus) {
	if sourceStatus := t.pages.checkAccessStatus(); sourceStatus.failed() {
		return blobChunk{}, blobReadStatus{code: blobReadErrSource, source: sourceStatus}
	}
	if logicalOffset >= t.length {
		return blobChunk{}, blobReadStatus{
			code:   blobReadErrRequestOutsideLength,
			actual: logicalOffset,
		}
	}
	pageNumber := t.root
	level, status := t.rootLevel(page)
	if status.failed() {
		return blobChunk{}, status
	}
	expectedOffset := uint64(0)
	hasSuccessor := false
	root := true
	for {
		if status := t.readPage(pageNumber, page); status.failed() {
			return blobChunk{}, status
		}
		header, pageStatus := blobHeaderStatus(page[:], t.selectedTxn)
		if pageStatus.failed() {
			return blobChunk{}, t.pageError(pageNumber, pageStatus)
		}
		if level == 0 {
			if header.PageType != PageTypeBlobLeaf {
				code := blobReadErrChildType
				if root {
					code = blobReadErrRootType
				}
				return blobChunk{}, blobReadStatus{
					code:     code,
					page:     pageNumber,
					pageType: header.PageType,
				}
			}
			leaf, status := t.leaf(pageNumber, page, check)
			if status.failed() {
				return blobChunk{}, status
			}
			if leaf.logicalOffset != expectedOffset {
				return blobChunk{}, blobReadStatus{
					code:     blobReadErrOffsetMismatch,
					expected: expectedOffset,
					actual:   leaf.logicalOffset,
				}
			}
			end, ok := checkedAdd(leaf.logicalOffset, uint64(leaf.dataLength))
			if !ok {
				return blobChunk{}, blobReadStatus{code: blobReadErrLogicalOffsetOverflow}
			}
			if status := t.checkLeafEnd(leaf, end, hasSuccessor); status.failed() {
				return blobChunk{}, status
			}
			if logicalOffset < leaf.logicalOffset || logicalOffset >= end {
				return blobChunk{}, blobReadStatus{
					code:     blobReadErrOffsetMismatch,
					expected: logicalOffset,
					actual:   leaf.logicalOffset,
				}
			}
			if _, _, status := t.checkRetirementChunk(leaf.data(), 0, false); status.failed() {
				return blobChunk{}, status
			}
			return blobChunk{logicalOffset: leaf.logicalOffset, data: leaf.data()}, blobReadStatus{}
		}

		if header.PageType != PageTypeBlobBranch {
			code := blobReadErrChildType
			if root {
				code = blobReadErrRootType
			}
			return blobChunk{}, blobReadStatus{
				code:     code,
				page:     pageNumber,
				pageType: header.PageType,
			}
		}
		root = false
		branch, status := t.branch(pageNumber, page, level, check)
		if status.failed() {
			return blobChunk{}, status
		}
		first, pageStatus := branch.entryStatus(0)
		if pageStatus.failed() {
			return blobChunk{}, t.pageError(pageNumber, pageStatus)
		}
		if first.logicalOffset != expectedOffset {
			return blobChunk{}, blobReadStatus{
				code:     blobReadErrOffsetMismatch,
				expected: expectedOffset,
				actual:   first.logicalOffset,
			}
		}
		index, pageStatus := branch.predecessorForStatus(logicalOffset)
		if pageStatus.failed() {
			return blobChunk{}, t.pageError(pageNumber, pageStatus)
		}
		hasSuccessor = hasSuccessor || index+1 < branch.len()
		entry, pageStatus := branch.entryStatus(index)
		if pageStatus.failed() {
			return blobChunk{}, t.pageError(pageNumber, pageStatus)
		}
		expectedOffset = entry.logicalOffset
		pageNumber = entry.childPage
		level--
	}
}

func (t *blobTree[S]) checkLeafEnd(leaf blobLeaf, end uint64, hasSuccessor bool) blobReadStatus {
	if end > t.length {
		return blobReadStatus{
			code:     blobReadErrLengthExceeded,
			expected: t.length,
			actual:   end,
		}
	}
	if end < t.length {
		if int(leaf.dataLength) != blobLeafCapacity {
			return blobReadStatus{
				code:       blobReadErrNonfinalLeafLength,
				dataLength: leaf.dataLength,
			}
		}
		if !hasSuccessor {
			return blobReadStatus{
				code:     blobReadErrLengthShort,
				expected: t.length,
				actual:   end,
			}
		}
	} else if hasSuccessor {
		return blobReadStatus{code: blobReadErrTrailingData}
	}
	return blobReadStatus{}
}

func (t *blobTree[S]) checkRetirementChunk(
	data []byte,
	previous uint32,
	havePrevious bool,
) (uint32, bool, blobReadStatus) {
	if t.kind != blobKindRetirementPageList {
		return previous, havePrevious, blobReadStatus{}
	}
	for offset := 0; offset < len(data); offset += 4 {
		current := binary.LittleEndian.Uint32(data[offset : offset+4])
		if current < 2 || uint64(current) >= t.pageCount {
			return 0, false, blobReadStatus{
				code:        blobReadErrRetirementPageOutOfBounds,
				currentPage: current,
			}
		}
		if havePrevious && current <= previous {
			return 0, false, blobReadStatus{
				code:         blobReadErrRetirementPageOrder,
				previousPage: previous,
				currentPage:  current,
			}
		}
		previous = current
		havePrevious = true
	}
	return previous, havePrevious, blobReadStatus{}
}

func (t *blobTree[S]) rootLevel(page *[PageSize]byte) (uint16, blobReadStatus) {
	if status := t.readPage(t.root, page); status.failed() {
		return 0, status
	}
	header, pageStatus := blobHeaderStatus(page[:], t.selectedTxn)
	if pageStatus.failed() {
		return 0, t.pageError(t.root, pageStatus)
	}
	switch header.PageType {
	case PageTypeBlobLeaf:
		return 0, blobReadStatus{}
	case PageTypeBlobBranch:
		return header.Level, blobReadStatus{}
	default:
		return 0, blobReadStatus{
			code:     blobReadErrRootType,
			page:     t.root,
			pageType: header.PageType,
		}
	}
}

func (t *blobTree[S]) branch(
	pageNumber uint32,
	page *[PageSize]byte,
	expectedLevel uint16,
	check blobPageCheck,
) (blobBranch, blobReadStatus) {
	branch, pageStatus := openBlobBranchStatus(page[:], t.selectedTxn, t.kind, t.pageCount)
	if pageStatus.failed() {
		return blobBranch{}, t.pageError(pageNumber, pageStatus)
	}
	if branch.level != expectedLevel {
		return blobBranch{}, blobReadStatus{
			code:          blobReadErrChildLevel,
			page:          pageNumber,
			expectedLevel: expectedLevel,
			actualLevel:   branch.level,
		}
	}
	if check == blobPageCheckVerified {
		if pageStatus := branch.verifyLocalStatus(); pageStatus.failed() {
			return blobBranch{}, t.pageError(pageNumber, pageStatus)
		}
	}
	return branch, blobReadStatus{}
}

func (t *blobTree[S]) leaf(pageNumber uint32, page *[PageSize]byte, check blobPageCheck) (blobLeaf, blobReadStatus) {
	leaf, pageStatus := openBlobLeafStatus(page[:], t.selectedTxn, t.kind)
	if pageStatus.failed() {
		return blobLeaf{}, t.pageError(pageNumber, pageStatus)
	}
	if check == blobPageCheckVerified {
		if pageStatus := leaf.verifyLocalStatus(); pageStatus.failed() {
			return blobLeaf{}, t.pageError(pageNumber, pageStatus)
		}
	}
	return leaf, blobReadStatus{}
}

func (t *blobTree[S]) readPage(pageNumber uint32, destination *[PageSize]byte) blobReadStatus {
	if sourceStatus := t.pages.readPageStatus(pageNumber, destination); sourceStatus.failed() {
		return blobReadStatus{code: blobReadErrSource, source: sourceStatus, page: pageNumber}
	}
	return blobReadStatus{}
}

func (t *blobTree[S]) pageError(pageNumber uint32, problem blobPageStatus) blobReadStatus {
	return blobReadStatus{
		code:        blobReadErrPage,
		pageProblem: problem,
		page:        pageNumber,
	}
}

type blobFrame struct {
	page  uint32
	index uint16
	len   uint16
}

type blobReaderState uint8

const (
	blobReaderUnpositioned blobReaderState = iota
	blobReaderReady
	blobReaderYielded
	blobReaderDone
	blobReaderFailed
)

type blobReader[S committedPageSource] struct {
	tree                   blobTree[S]
	check                  blobPageCheck
	path                   [blobPathCapacity]blobFrame
	rootLevel              uint16
	nextOffset             uint64
	previousRetirementPage uint32
	havePreviousPage       bool
	scratch                [PageSize]byte
	scratchPage            uint32
	state                  blobReaderState
}

type blobReadWorkspace[S committedPageSource] struct {
	reader blobReader[S]
}

type blobRetirementPageReader[S committedPageSource] struct {
	reader     blobReader[S]
	byteOffset int
	dataLength int
}

func (r *blobReader[S]) reset(tree blobTree[S], check blobPageCheck) {
	*r = blobReader[S]{tree: tree, check: check, state: blobReaderUnpositioned}
}

func (r *blobRetirementPageReader[S]) reset(tree blobTree[S], check blobPageCheck) {
	*r = blobRetirementPageReader[S]{}
	r.reader.reset(tree, check)
}

func (r *blobReader[S]) nextChunk() (blobChunk, bool, error) {
	chunk, ok, status := r.nextChunkStatus()
	return chunk, ok, status.err()
}

func (r *blobReader[S]) nextChunkStatus() (blobChunk, bool, blobReadStatus) {
	if sourceStatus := r.tree.pages.checkAccessStatus(); sourceStatus.failed() {
		r.state = blobReaderFailed
		return blobChunk{}, false, blobReadStatus{code: blobReadErrSource, source: sourceStatus}
	}
	if r.state == blobReaderFailed {
		return blobChunk{}, false, blobReadStatus{code: blobReadErrCursorFailed}
	}
	logicalOffset, dataLength, ok, status := r.prepareNextChunk()
	if !status.failed() && ok {
		r.state = blobReaderYielded
		return blobChunk{
			logicalOffset: logicalOffset,
			data:          r.scratch[blobLeafDataOffset : blobLeafDataOffset+int(dataLength)],
		}, true, blobReadStatus{}
	}
	if status.failed() {
		r.state = blobReaderFailed
	}
	return blobChunk{}, false, status
}

func (r *blobReader[S]) prepareNextChunk() (uint64, uint16, bool, blobReadStatus) {
	switch r.state {
	case blobReaderUnpositioned:
		if status := r.loadPage(r.tree.root); status.failed() {
			return 0, 0, false, status
		}
		header, pageStatus := blobHeaderStatus(r.scratch[:], r.tree.selectedTxn)
		if pageStatus.failed() {
			return 0, 0, false, r.tree.pageError(r.tree.root, pageStatus)
		}
		switch header.PageType {
		case PageTypeBlobLeaf:
			r.rootLevel = 0
		case PageTypeBlobBranch:
			r.rootLevel = header.Level
		default:
			return 0, 0, false, blobReadStatus{code: blobReadErrRootType, page: r.tree.root, pageType: header.PageType}
		}
		if status := r.descendFirst(0, r.tree.root, r.rootLevel, 0); status.failed() {
			return 0, 0, false, status
		}
	case blobReaderDone:
		return 0, 0, false, blobReadStatus{}
	case blobReaderFailed:
		return 0, 0, false, blobReadStatus{code: blobReadErrCursorFailed}
	case blobReaderYielded:
		advanced, status := r.advance()
		if status.failed() || !advanced {
			return 0, 0, false, status
		}
	case blobReaderReady:
	}

	leaf, status := r.tree.leaf(r.scratchPage, &r.scratch, r.check)
	if status.failed() {
		return 0, 0, false, status
	}
	if leaf.logicalOffset != r.nextOffset {
		return 0, 0, false, blobReadStatus{
			code:     blobReadErrOffsetMismatch,
			expected: r.nextOffset,
			actual:   leaf.logicalOffset,
		}
	}
	end, ok := checkedAdd(leaf.logicalOffset, uint64(leaf.dataLength))
	if !ok {
		return 0, 0, false, blobReadStatus{code: blobReadErrLogicalOffsetOverflow}
	}
	if end > r.tree.length {
		return 0, 0, false, blobReadStatus{
			code:     blobReadErrLengthExceeded,
			expected: r.tree.length,
			actual:   end,
		}
	}
	if end < r.tree.length && int(leaf.dataLength) != blobLeafCapacity {
		return 0, 0, false, blobReadStatus{
			code:       blobReadErrNonfinalLeafLength,
			dataLength: leaf.dataLength,
		}
	}

	r.previousRetirementPage, r.havePreviousPage, status = r.tree.checkRetirementChunk(
		leaf.data(),
		r.previousRetirementPage,
		r.havePreviousPage,
	)
	if status.failed() {
		return 0, 0, false, status
	}
	hasSuccessor := r.hasSuccessor()
	if end == r.tree.length {
		if hasSuccessor {
			return 0, 0, false, blobReadStatus{code: blobReadErrTrailingData}
		}
	} else if !hasSuccessor {
		return 0, 0, false, blobReadStatus{
			code:     blobReadErrLengthShort,
			expected: r.tree.length,
			actual:   end,
		}
	}
	r.nextOffset = end
	return leaf.logicalOffset, leaf.dataLength, true, blobReadStatus{}
}

func (r *blobReader[S]) descendFirst(
	depth int,
	pageNumber uint32,
	level uint16,
	expectedOffset uint64,
) blobReadStatus {
	for {
		if depth >= blobPathCapacity {
			return blobReadStatus{
				code:          blobReadErrChildLevel,
				expectedLevel: 0,
				actualLevel:   level,
			}
		}
		if !(depth == 0 && pageNumber == r.tree.root) {
			if status := r.loadPage(pageNumber); status.failed() {
				return status
			}
		}
		header, pageStatus := blobHeaderStatus(r.scratch[:], r.tree.selectedTxn)
		if pageStatus.failed() {
			return r.tree.pageError(pageNumber, pageStatus)
		}
		if level == 0 {
			if header.PageType != PageTypeBlobLeaf {
				return blobReadStatus{
					code:     blobReadErrChildType,
					page:     pageNumber,
					pageType: header.PageType,
				}
			}
			leaf, status := r.tree.leaf(pageNumber, &r.scratch, r.check)
			if status.failed() {
				return status
			}
			if leaf.logicalOffset != expectedOffset {
				return blobReadStatus{
					code:     blobReadErrOffsetMismatch,
					expected: expectedOffset,
					actual:   leaf.logicalOffset,
				}
			}
			r.state = blobReaderReady
			return blobReadStatus{}
		}

		if header.PageType != PageTypeBlobBranch {
			return blobReadStatus{
				code:     blobReadErrChildType,
				page:     pageNumber,
				pageType: header.PageType,
			}
		}
		branch, status := r.tree.branch(pageNumber, &r.scratch, level, r.check)
		if status.failed() {
			return status
		}
		entry, pageStatus := branch.entryStatus(0)
		if pageStatus.failed() {
			return r.tree.pageError(pageNumber, pageStatus)
		}
		if entry.logicalOffset != expectedOffset {
			return blobReadStatus{
				code:     blobReadErrOffsetMismatch,
				expected: expectedOffset,
				actual:   entry.logicalOffset,
			}
		}
		r.path[depth] = blobFrame{page: pageNumber, index: 0, len: uint16(branch.len())}
		pageNumber = entry.childPage
		expectedOffset = entry.logicalOffset
		level--
		depth++
	}
}

func (r *blobReader[S]) advance() (bool, blobReadStatus) {
	leafDepth := int(r.rootLevel)
	for depth := leafDepth - 1; depth >= 0; depth-- {
		frame := r.path[depth]
		level := r.rootLevel - uint16(depth)
		if status := r.loadPage(frame.page); status.failed() {
			return false, status
		}
		branch, status := r.tree.branch(frame.page, &r.scratch, level, r.check)
		if status.failed() {
			return false, status
		}
		if branch.len() != int(frame.len) {
			return false, blobReadStatus{code: blobReadErrPathChanged, page: frame.page}
		}
		nextIndex := int(frame.index) + 1
		if nextIndex == branch.len() {
			continue
		}
		r.path[depth].index = uint16(nextIndex)
		entry, pageStatus := branch.entryStatus(nextIndex)
		if pageStatus.failed() {
			return false, r.tree.pageError(frame.page, pageStatus)
		}
		if status := r.descendFirst(depth+1, entry.childPage, level-1, entry.logicalOffset); status.failed() {
			return false, status
		}
		return true, blobReadStatus{}
	}
	r.state = blobReaderDone
	return false, blobReadStatus{}
}

func (r *blobReader[S]) hasSuccessor() bool {
	for index := 0; index < int(r.rootLevel); index++ {
		frame := r.path[index]
		if int(frame.index)+1 < int(frame.len) {
			return true
		}
	}
	return false
}

func (r *blobReader[S]) loadPage(pageNumber uint32) blobReadStatus {
	if status := r.tree.readPage(pageNumber, &r.scratch); status.failed() {
		return status
	}
	r.scratchPage = pageNumber
	return blobReadStatus{}
}

// nextPage yields one copied page number. No view into the reusable page
// buffer survives this call.
func (r *blobRetirementPageReader[S]) nextPage() (uint32, bool, error) {
	page, ok, status := r.nextPageStatus()
	return page, ok, status.err()
}

func (r *blobRetirementPageReader[S]) nextPageStatus() (uint32, bool, blobReadStatus) {
	if sourceStatus := r.reader.tree.pages.checkAccessStatus(); sourceStatus.failed() {
		r.reader.state = blobReaderFailed
		return 0, false, blobReadStatus{code: blobReadErrSource, source: sourceStatus}
	}
	if r.reader.state == blobReaderFailed {
		return 0, false, blobReadStatus{code: blobReadErrCursorFailed}
	}
	if r.byteOffset == r.dataLength {
		_, dataLength, ok, status := r.reader.prepareNextChunk()
		if status.failed() {
			r.reader.state = blobReaderFailed
			return 0, false, status
		}
		if !ok {
			return 0, false, blobReadStatus{}
		}
		r.reader.state = blobReaderYielded
		r.byteOffset = 0
		r.dataLength = int(dataLength)
	}
	start := blobLeafDataOffset + r.byteOffset
	page := binary.LittleEndian.Uint32(r.reader.scratch[start : start+4])
	r.byteOffset += 4
	return page, true, blobReadStatus{}
}
