package exactv4

import (
	"fmt"
)

const retirementPathCapacity = int(MaxTreeLevel) + 1

type retirementPageCheck uint8

const (
	retirementPageCheckOrdinary retirementPageCheck = iota + 1
	retirementPageCheckVerified
)

type retirementIdentity struct {
	databaseID  [16]byte
	txnID       uint64
	commitNonce [16]byte
	pageCount   uint64
	root        uint32
	batchCount  uint64
}

type retirementReadErrorCode uint8

const (
	retirementReadErrSource retirementReadErrorCode = iota + 1
	retirementReadErrPage
	retirementReadErrBlob
	retirementReadErrPageOutOfBounds
	retirementReadErrPageOffsetOverflow
	retirementReadErrIdentityInvalid
	retirementReadErrCommittedPageCountOutOfRange
	retirementReadErrRootCountMismatch
	retirementReadErrBatchCountOutOfRange
	retirementReadErrRootType
	retirementReadErrChildType
	retirementReadErrChildLevel
	retirementReadErrChildMaximumMismatch
	retirementReadErrKeysNotStrict
	retirementReadErrBatchCountMismatch
	retirementReadErrWorkLimitZero
	retirementReadErrWorkLimitTooSmall
	retirementReadErrArithmeticOverflow
	retirementReadErrVerificationBufferTooSmall
	retirementReadErrSelectionChanged
	retirementReadErrListedPageOutOfBounds
	retirementReadErrBlobPageCountMismatch
	retirementReadErrPathChanged
	retirementReadErrCursorFailed
)

type retirementReadError struct {
	code            retirementReadErrorCode
	cause           error
	page            uint32
	pageType        PageType
	expectedLevel   uint16
	actualLevel     uint16
	expected        uint64
	actual          uint64
	requiredPages   uint64
	requiredBatches uint64
}

type retirementReadStatus struct {
	code            retirementReadErrorCode
	source          pageSourceStatus
	pageProblem     retirementPageStatus
	blobProblem     blobReadStatus
	page            uint32
	pageType        PageType
	expectedLevel   uint16
	actualLevel     uint16
	expected        uint64
	actual          uint64
	requiredPages   uint64
	requiredBatches uint64
}

func (status retirementReadStatus) failed() bool { return status.code != 0 }

func (status retirementReadStatus) asError() *retirementReadError {
	if !status.failed() {
		return nil
	}
	errorValue := &retirementReadError{
		code:            status.code,
		page:            status.page,
		pageType:        status.pageType,
		expectedLevel:   status.expectedLevel,
		actualLevel:     status.actualLevel,
		expected:        status.expected,
		actual:          status.actual,
		requiredPages:   status.requiredPages,
		requiredBatches: status.requiredBatches,
	}
	switch status.code {
	case retirementReadErrSource:
		errorValue.cause = status.source.asError()
	case retirementReadErrPage:
		errorValue.cause = status.pageProblem.asError()
	case retirementReadErrBlob:
		errorValue.cause = status.blobProblem.asError()
	}
	return errorValue
}

func (status retirementReadStatus) err() error {
	if !status.failed() {
		return nil
	}
	return status.asError()
}

func (e *retirementReadError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 retirement read: error %d", e.code)
}

func (e *retirementReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type retirementSelection struct {
	identity         retirementIdentity
	batchCount       uint64
	pageCount        uint64
	lastRetiredByTxn uint64
}

type retirementPassResult struct {
	batchCount uint64
	pageCount  uint64
}

type retirementTree[S committedPageSource] struct {
	pages    S
	identity retirementIdentity
}

// retirementReadWorkspace is traversal storage only. Reclaim integration must
// supply its existing exclusive-operation lock and current reader threshold;
// this layer does not establish or claim lock ownership.
type retirementReadWorkspace[S committedPageSource] struct {
	cursor retirementCursor[S]
	blob   blobRetirementPageReader[S]
}

func newRetirementTree(data []byte, identity retirementIdentity) (retirementTree[immutableSlicePageSource], error) {
	if identity.databaseID == ([16]byte{}) ||
		identity.commitNonce == ([16]byte{}) ||
		identity.txnID == 0 {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{code: retirementReadErrIdentityInvalid}
	}
	if identity.pageCount < 2 || identity.pageCount > MaxPageCount {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{
			code:   retirementReadErrCommittedPageCountOutOfRange,
			actual: identity.pageCount,
		}
	}
	if (identity.root == 0) != (identity.batchCount == 0) {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{code: retirementReadErrRootCountMismatch}
	}
	if identity.batchCount > identity.txnID-1 {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{code: retirementReadErrBatchCountOutOfRange}
	}
	if identity.root != 0 &&
		(identity.root < 2 || uint64(identity.root) >= identity.pageCount) {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{
			code: retirementReadErrPageOutOfBounds,
			page: identity.root,
		}
	}
	committedBytes, ok := checkedMul(identity.pageCount, PageSize)
	if !ok {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{code: retirementReadErrPageOffsetOverflow}
	}
	committed := int(committedBytes)
	if uint64(committed) != committedBytes {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{code: retirementReadErrPageOffsetOverflow}
	}
	if committed > len(data) {
		return retirementTree[immutableSlicePageSource]{}, &retirementReadError{
			code: retirementReadErrPageOutOfBounds,
			page: identity.root,
		}
	}
	return newRetirementTreeFromSource(
		newImmutableSlicePageSource(data[:committed], identity.pageCount),
		identity,
	)
}

func newRetirementTreeFromSource[S committedPageSource](
	pages S,
	identity retirementIdentity,
) (retirementTree[S], error) {
	if identity.databaseID == ([16]byte{}) ||
		identity.commitNonce == ([16]byte{}) ||
		identity.txnID == 0 {
		return retirementTree[S]{}, &retirementReadError{code: retirementReadErrIdentityInvalid}
	}
	if identity.pageCount < 2 || identity.pageCount > MaxPageCount {
		return retirementTree[S]{}, &retirementReadError{code: retirementReadErrCommittedPageCountOutOfRange, actual: identity.pageCount}
	}
	if (identity.root == 0) != (identity.batchCount == 0) {
		return retirementTree[S]{}, &retirementReadError{code: retirementReadErrRootCountMismatch}
	}
	if identity.batchCount > identity.txnID-1 {
		return retirementTree[S]{}, &retirementReadError{code: retirementReadErrBatchCountOutOfRange}
	}
	if identity.root != 0 && (identity.root < 2 || uint64(identity.root) >= identity.pageCount) {
		return retirementTree[S]{}, &retirementReadError{code: retirementReadErrPageOutOfBounds, page: identity.root}
	}
	return retirementTree[S]{pages: pages, identity: identity}, nil
}

func (t *retirementTree[S]) selectOldestEligible(
	readerThreshold uint64,
	maxBatches uint64,
	maxPages uint64,
) (retirementSelection, bool, error) {
	var workspace retirementReadWorkspace[S]
	selection, ok, status := t.selectOldestEligibleWithWorkspace(
		readerThreshold,
		maxBatches,
		maxPages,
		&workspace,
	)
	return selection, ok, status.err()
}

func (t *retirementTree[S]) selectOldestEligibleWithWorkspace(
	readerThreshold uint64,
	maxBatches uint64,
	maxPages uint64,
	workspace *retirementReadWorkspace[S],
) (retirementSelection, bool, retirementReadStatus) {
	if sourceStatus := t.pages.checkAccessStatus(); sourceStatus.failed() {
		return retirementSelection{}, false, retirementReadStatus{code: retirementReadErrSource, source: sourceStatus}
	}
	if maxBatches == 0 || maxPages == 0 {
		return retirementSelection{}, false, retirementReadStatus{code: retirementReadErrWorkLimitZero}
	}
	if t.identity.root == 0 {
		return retirementSelection{}, false, retirementReadStatus{}
	}

	workspace.cursor.reset(t, retirementPageCheckOrdinary)
	cursor := &workspace.cursor
	var selectedBatches uint64
	var selectedPages uint64
	var lastRetiredByTxn uint64
	for {
		if selectedBatches == maxBatches {
			break
		}
		batch, ok, status := cursor.nextBatchStatus()
		if status.failed() {
			return retirementSelection{}, false, status
		}
		if !ok || batch.retiredByTxn > readerThreshold {
			break
		}
		remaining := maxPages - selectedPages
		if batch.pageCount > remaining {
			if selectedBatches == 0 {
				return retirementSelection{}, false, retirementReadStatus{
					code:          retirementReadErrWorkLimitTooSmall,
					requiredPages: batch.pageCount,
				}
			}
			break
		}
		var addOK bool
		selectedBatches, addOK = checkedAdd(selectedBatches, 1)
		if !addOK {
			return retirementSelection{}, false, retirementReadStatus{code: retirementReadErrArithmeticOverflow}
		}
		selectedPages, addOK = checkedAdd(selectedPages, batch.pageCount)
		if !addOK {
			return retirementSelection{}, false, retirementReadStatus{code: retirementReadErrArithmeticOverflow}
		}
		lastRetiredByTxn = batch.retiredByTxn
	}
	if selectedBatches == 0 {
		return retirementSelection{}, false, retirementReadStatus{}
	}
	return retirementSelection{
		identity:         t.identity,
		batchCount:       selectedBatches,
		pageCount:        selectedPages,
		lastRetiredByTxn: lastRetiredByTxn,
	}, true, retirementReadStatus{}
}

func (t *retirementTree[S]) verifySelection(
	selection retirementSelection,
	scratch []retirementBatch,
) (verifiedRetirementSelection[S], error) {
	var workspace retirementReadWorkspace[S]
	verified, status := t.verifySelectionWithWorkspace(selection, scratch, &workspace)
	return verified, status.err()
}

func (t *retirementTree[S]) verifySelectionWithWorkspace(
	selection retirementSelection,
	scratch []retirementBatch,
	workspace *retirementReadWorkspace[S],
) (verifiedRetirementSelection[S], retirementReadStatus) {
	if sourceStatus := t.pages.checkAccessStatus(); sourceStatus.failed() {
		return verifiedRetirementSelection[S]{}, retirementReadStatus{code: retirementReadErrSource, source: sourceStatus}
	}
	if selection.identity != t.identity {
		return verifiedRetirementSelection[S]{}, retirementReadStatus{code: retirementReadErrSelectionChanged}
	}
	required := int(selection.batchCount)
	if uint64(required) != selection.batchCount || len(scratch) < required {
		return verifiedRetirementSelection[S]{}, retirementReadStatus{
			code:            retirementReadErrVerificationBufferTooSmall,
			requiredBatches: selection.batchCount,
		}
	}

	workspace.cursor.reset(t, retirementPageCheckVerified)
	cursor := &workspace.cursor
	var actualPages uint64
	var actualLast uint64
	for index := 0; index < required; index++ {
		batch, ok, status := cursor.nextBatchStatus()
		if status.failed() {
			return verifiedRetirementSelection[S]{}, status
		}
		if !ok {
			return verifiedRetirementSelection[S]{}, retirementReadStatus{code: retirementReadErrSelectionChanged}
		}
		if status := t.verifyBatchBlobWithWorkspace(batch, &workspace.blob); status.failed() {
			return verifiedRetirementSelection[S]{}, status
		}
		scratch[index] = batch
		var addOK bool
		actualPages, addOK = checkedAdd(actualPages, batch.pageCount)
		if !addOK {
			return verifiedRetirementSelection[S]{}, retirementReadStatus{code: retirementReadErrArithmeticOverflow}
		}
		actualLast = batch.retiredByTxn
	}
	if actualPages != selection.pageCount || actualLast != selection.lastRetiredByTxn {
		return verifiedRetirementSelection[S]{}, retirementReadStatus{code: retirementReadErrSelectionChanged}
	}
	return verifiedRetirementSelection[S]{
		identity:  t.identity,
		selection: selection,
		batches:   scratch[:required],
	}, retirementReadStatus{}
}

func (t *retirementTree[S]) verifyBatchBlob(batch retirementBatch) error {
	var workspace blobRetirementPageReader[S]
	return t.verifyBatchBlobWithWorkspace(batch, &workspace).err()
}

func (t *retirementTree[S]) verifyBatchBlobWithWorkspace(
	batch retirementBatch,
	workspace *blobRetirementPageReader[S],
) retirementReadStatus {
	length, pageStatus := batch.blobLengthStatus()
	if pageStatus.failed() {
		return retirementReadStatus{code: retirementReadErrPage, pageProblem: pageStatus}
	}
	blob, blobStatus := newBlobTreeFromSourceStatus(
		t.pages,
		t.identity.txnID,
		t.identity.pageCount,
		batch.pageListBlobRoot,
		blobKindRetirementPageList,
		length,
	)
	if blobStatus.failed() {
		return retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus}
	}
	reader, blobStatus := blob.retirementPagesWithWorkspaceStatus(blobPageCheckVerified, workspace)
	if blobStatus.failed() {
		return retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus}
	}
	var count uint64
	for {
		page, ok, blobStatus := reader.nextPageStatus()
		if blobStatus.failed() {
			return retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus}
		}
		if !ok {
			break
		}
		if status := t.requireListedPageStatus(page); status.failed() {
			return status
		}
		var addOK bool
		count, addOK = checkedAdd(count, 1)
		if !addOK {
			return retirementReadStatus{code: retirementReadErrArithmeticOverflow}
		}
	}
	if count != batch.pageCount {
		return retirementReadStatus{
			code:     retirementReadErrBlobPageCountMismatch,
			expected: batch.pageCount,
			actual:   count,
		}
	}
	return retirementReadStatus{}
}

func (t *retirementTree[S]) requireListedPage(page uint32) error {
	return t.requireListedPageStatus(page).err()
}

func (t *retirementTree[S]) requireListedPageStatus(page uint32) retirementReadStatus {
	if page < 2 || uint64(page) >= t.identity.pageCount {
		return retirementReadStatus{code: retirementReadErrListedPageOutOfBounds, page: page}
	}
	return retirementReadStatus{}
}

func (t *retirementTree[S]) cursor(check retirementPageCheck) retirementCursor[S] {
	return retirementCursor[S]{tree: t, check: check, state: retirementCursorUnpositioned}
}

func (t *retirementTree[S]) rootLevel(page *[PageSize]byte) (uint16, bool, retirementReadStatus) {
	if t.identity.root == 0 {
		return 0, false, retirementReadStatus{}
	}
	if status := t.readPage(t.identity.root, page); status.failed() {
		return 0, false, status
	}
	header, pageStatus := retirementHeaderStatus(page[:], t.identity.txnID)
	if pageStatus.failed() {
		return 0, false, t.pageError(t.identity.root, pageStatus)
	}
	switch header.PageType {
	case PageTypeRetirementLeaf:
		return 0, true, retirementReadStatus{}
	case PageTypeRetirementBranch:
		return header.Level, true, retirementReadStatus{}
	default:
		return 0, false, retirementReadStatus{
			code:     retirementReadErrRootType,
			page:     t.identity.root,
			pageType: header.PageType,
		}
	}
}

func (t *retirementTree[S]) branch(
	pageNumber uint32,
	page *[PageSize]byte,
	expectedLevel uint16,
	check retirementPageCheck,
) (retirementBranch, retirementReadStatus) {
	branch, pageStatus := openRetirementBranchStatus(page[:], t.identity.txnID, t.identity.pageCount)
	if pageStatus.failed() {
		return retirementBranch{}, t.pageError(pageNumber, pageStatus)
	}
	if branch.level != expectedLevel {
		return retirementBranch{}, retirementReadStatus{
			code:          retirementReadErrChildLevel,
			page:          pageNumber,
			expectedLevel: expectedLevel,
			actualLevel:   branch.level,
		}
	}
	if check == retirementPageCheckVerified {
		if pageStatus := branch.verifyCRCStatus(); pageStatus.failed() {
			return retirementBranch{}, t.pageError(pageNumber, pageStatus)
		}
	}
	return branch, retirementReadStatus{}
}

func (t *retirementTree[S]) leaf(
	pageNumber uint32,
	page *[PageSize]byte,
	check retirementPageCheck,
) (retirementLeaf, retirementReadStatus) {
	leaf, pageStatus := openRetirementLeafStatus(page[:], t.identity.txnID, t.identity.pageCount)
	if pageStatus.failed() {
		return retirementLeaf{}, t.pageError(pageNumber, pageStatus)
	}
	if check == retirementPageCheckVerified {
		if pageStatus := leaf.verifyCRCStatus(); pageStatus.failed() {
			return retirementLeaf{}, t.pageError(pageNumber, pageStatus)
		}
	}
	return leaf, retirementReadStatus{}
}

func (t *retirementTree[S]) readPage(pageNumber uint32, destination *[PageSize]byte) retirementReadStatus {
	if sourceStatus := t.pages.readPageStatus(pageNumber, destination); sourceStatus.failed() {
		return retirementReadStatus{code: retirementReadErrSource, source: sourceStatus, page: pageNumber}
	}
	return retirementReadStatus{}
}

func (t *retirementTree[S]) pageError(pageNumber uint32, problem retirementPageStatus) retirementReadStatus {
	return retirementReadStatus{code: retirementReadErrPage, pageProblem: problem, page: pageNumber}
}

type retirementFrame struct {
	page  uint32
	index uint16
	len   uint16
}

type retirementCursorState uint8

const (
	retirementCursorUnpositioned retirementCursorState = iota
	retirementCursorAt
	retirementCursorDone
	retirementCursorFailed
)

type retirementCursor[S committedPageSource] struct {
	tree            *retirementTree[S]
	check           retirementPageCheck
	path            [retirementPathCapacity]retirementFrame
	rootLevel       uint16
	leafIndex       uint16
	previousKey     uint64
	havePreviousKey bool
	yielded         uint64
	scratch         [PageSize]byte
	scratchPage     uint32
	state           retirementCursorState
}

func (c *retirementCursor[S]) reset(
	tree *retirementTree[S],
	check retirementPageCheck,
) {
	*c = retirementCursor[S]{
		tree:  tree,
		check: check,
		state: retirementCursorUnpositioned,
	}
}

func (c *retirementCursor[S]) nextBatch() (retirementBatch, bool, error) {
	batch, ok, status := c.nextBatchStatus()
	return batch, ok, status.err()
}

func (c *retirementCursor[S]) nextBatchStatus() (retirementBatch, bool, retirementReadStatus) {
	if sourceStatus := c.tree.pages.checkAccessStatus(); sourceStatus.failed() {
		c.state = retirementCursorFailed
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrSource, source: sourceStatus}
	}
	if c.state == retirementCursorFailed {
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrCursorFailed}
	}
	batch, ok, status := c.nextBatchInner()
	if status.failed() {
		c.state = retirementCursorFailed
		return retirementBatch{}, false, status
	}
	return batch, ok, retirementReadStatus{}
}

func (c *retirementCursor[S]) nextBatchInner() (retirementBatch, bool, retirementReadStatus) {
	switch c.state {
	case retirementCursorUnpositioned:
		level, ok, status := c.tree.rootLevel(&c.scratch)
		if status.failed() {
			return retirementBatch{}, false, status
		}
		if !ok {
			c.state = retirementCursorDone
			return retirementBatch{}, false, retirementReadStatus{}
		}
		c.rootLevel = level
		if status := c.descendFirst(0, c.tree.identity.root, level, 0, false); status.failed() {
			return retirementBatch{}, false, status
		}
	case retirementCursorDone:
		return retirementBatch{}, false, retirementReadStatus{}
	case retirementCursorFailed:
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrCursorFailed}
	case retirementCursorAt:
	}

	leaf, status := c.tree.leaf(c.scratchPage, &c.scratch, c.check)
	if status.failed() {
		return retirementBatch{}, false, status
	}
	batch, pageStatus := leaf.batchStatus(int(c.leafIndex))
	if pageStatus.failed() {
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrPage, pageProblem: pageStatus}
	}
	if c.havePreviousKey && batch.retiredByTxn <= c.previousKey {
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrKeysNotStrict}
	}
	c.previousKey = batch.retiredByTxn
	c.havePreviousKey = true
	var addOK bool
	c.yielded, addOK = checkedAdd(c.yielded, 1)
	if !addOK {
		return retirementBatch{}, false, retirementReadStatus{code: retirementReadErrArithmeticOverflow}
	}
	if status := c.advance(); status.failed() {
		return retirementBatch{}, false, status
	}
	if c.state == retirementCursorDone && c.yielded != c.tree.identity.batchCount {
		return retirementBatch{}, false, retirementReadStatus{
			code:     retirementReadErrBatchCountMismatch,
			expected: c.tree.identity.batchCount,
			actual:   c.yielded,
		}
	}
	return batch, true, retirementReadStatus{}
}

func (c *retirementCursor[S]) descendFirst(
	depth int,
	pageNumber uint32,
	level uint16,
	expectedMaximum uint64,
	haveExpectedMaximum bool,
) retirementReadStatus {
	for {
		if depth >= retirementPathCapacity {
			return retirementReadStatus{
				code:          retirementReadErrChildLevel,
				expectedLevel: 0,
				actualLevel:   level,
			}
		}
		if status := c.loadPage(pageNumber); status.failed() {
			return status
		}
		header, pageStatus := retirementHeaderStatus(c.scratch[:], c.tree.identity.txnID)
		if pageStatus.failed() {
			return c.tree.pageError(pageNumber, pageStatus)
		}
		if level == 0 {
			if header.PageType != PageTypeRetirementLeaf {
				return retirementReadStatus{
					code:     retirementReadErrChildType,
					page:     pageNumber,
					pageType: header.PageType,
				}
			}
			leaf, status := c.tree.leaf(pageNumber, &c.scratch, c.check)
			if status.failed() {
				return status
			}
			actual, pageStatus := leaf.maximumKeyStatus()
			if pageStatus.failed() {
				return c.tree.pageError(pageNumber, pageStatus)
			}
			if haveExpectedMaximum && actual != expectedMaximum {
				return retirementReadStatus{
					code:     retirementReadErrChildMaximumMismatch,
					expected: expectedMaximum,
					actual:   actual,
				}
			}
			c.leafIndex = 0
			c.state = retirementCursorAt
			return retirementReadStatus{}
		}

		if header.PageType != PageTypeRetirementBranch {
			return retirementReadStatus{
				code:     retirementReadErrChildType,
				page:     pageNumber,
				pageType: header.PageType,
			}
		}
		branch, status := c.tree.branch(pageNumber, &c.scratch, level, c.check)
		if status.failed() {
			return status
		}
		actual, pageStatus := branch.maximumKeyStatus()
		if pageStatus.failed() {
			return c.tree.pageError(pageNumber, pageStatus)
		}
		if haveExpectedMaximum && actual != expectedMaximum {
			return retirementReadStatus{
				code:     retirementReadErrChildMaximumMismatch,
				expected: expectedMaximum,
				actual:   actual,
			}
		}
		entry, pageStatus := branch.entryStatus(0)
		if pageStatus.failed() {
			return c.tree.pageError(pageNumber, pageStatus)
		}
		c.path[depth] = retirementFrame{
			page:  pageNumber,
			index: 0,
			len:   uint16(branch.len()),
		}
		pageNumber = entry.childPage
		expectedMaximum = entry.maxRetiredByTxn
		haveExpectedMaximum = true
		level--
		depth++
	}
}

func (c *retirementCursor[S]) advance() retirementReadStatus {
	leaf, status := c.tree.leaf(c.scratchPage, &c.scratch, c.check)
	if status.failed() {
		return status
	}
	if int(c.leafIndex)+1 < leaf.len() {
		c.leafIndex++
		return retirementReadStatus{}
	}

	leafDepth := int(c.rootLevel)
	for depth := leafDepth - 1; depth >= 0; depth-- {
		frame := c.path[depth]
		level := c.rootLevel - uint16(depth)
		if status := c.loadPage(frame.page); status.failed() {
			return status
		}
		branch, status := c.tree.branch(frame.page, &c.scratch, level, c.check)
		if status.failed() {
			return status
		}
		if branch.len() != int(frame.len) {
			return retirementReadStatus{code: retirementReadErrPathChanged, page: frame.page}
		}
		nextIndex := int(frame.index) + 1
		if nextIndex == branch.len() {
			continue
		}
		c.path[depth].index = uint16(nextIndex)
		entry, pageStatus := branch.entryStatus(nextIndex)
		if pageStatus.failed() {
			return c.tree.pageError(frame.page, pageStatus)
		}
		return c.descendFirst(
			depth+1,
			entry.childPage,
			level-1,
			entry.maxRetiredByTxn,
			true,
		)
	}
	c.state = retirementCursorDone
	return retirementReadStatus{}
}

func (c *retirementCursor[S]) loadPage(pageNumber uint32) retirementReadStatus {
	if status := c.tree.readPage(pageNumber, &c.scratch); status.failed() {
		return status
	}
	c.scratchPage = pageNumber
	return retirementReadStatus{}
}

type verifiedRetirementSelection[S committedPageSource] struct {
	identity  retirementIdentity
	selection retirementSelection
	batches   []retirementBatch
}

type retirementSecondPassErrorCode uint8

const (
	retirementSecondPassErrRead retirementSecondPassErrorCode = iota + 1
	retirementSecondPassErrSink
)

type retirementSecondPassError struct {
	code  retirementSecondPassErrorCode
	cause error
}

type retirementSinkStatusCode uint8

const retirementSinkFailed retirementSinkStatusCode = 1

type retirementSinkStatus struct {
	code   retirementSinkStatusCode
	detail uint64
}

func (status retirementSinkStatus) failed() bool { return status.code != 0 }

type retirementPageSink func(retirementBatch, uint32) retirementSinkStatus

type retirementSecondPassStatus struct {
	code retirementSecondPassErrorCode
	read retirementReadStatus
	sink retirementSinkStatus
}

func (status retirementSecondPassStatus) failed() bool { return status.code != 0 }

func (status retirementSecondPassStatus) asError(sinkCause error) error {
	if !status.failed() {
		return nil
	}
	if status.code == retirementSecondPassErrRead {
		return &retirementSecondPassError{code: status.code, cause: status.read.asError()}
	}
	return &retirementSecondPassError{code: status.code, cause: sinkCause}
}

func (e *retirementSecondPassError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 retirement second pass: error %d", e.code)
}

func (e *retirementSecondPassError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (v verifiedRetirementSelection[S]) secondPass(
	tree *retirementTree[S],
	sink func(retirementBatch, uint32) error,
) (retirementPassResult, error) {
	var workspace retirementReadWorkspace[S]
	var sinkCause error
	result, status := v.secondPassWithWorkspace(
		tree,
		&workspace,
		func(batch retirementBatch, page uint32) retirementSinkStatus {
			sinkCause = sink(batch, page)
			if sinkCause != nil {
				return retirementSinkStatus{code: retirementSinkFailed}
			}
			return retirementSinkStatus{}
		},
	)
	return result, status.asError(sinkCause)
}

func (v verifiedRetirementSelection[S]) secondPassWithWorkspace(
	tree *retirementTree[S],
	workspace *retirementReadWorkspace[S],
	sink retirementPageSink,
) (retirementPassResult, retirementSecondPassStatus) {
	if sourceStatus := tree.pages.checkAccessStatus(); sourceStatus.failed() {
		return retirementPassResult{}, retirementSecondPassStatus{
			code: retirementSecondPassErrRead,
			read: retirementReadStatus{code: retirementReadErrSource, source: sourceStatus},
		}
	}
	if tree.identity != v.identity || v.selection.identity != v.identity {
		return retirementPassResult{}, retirementSecondPassStatus{
			code: retirementSecondPassErrRead,
			read: retirementReadStatus{code: retirementReadErrSelectionChanged},
		}
	}

	workspace.cursor.reset(tree, retirementPageCheckOrdinary)
	preflight := &workspace.cursor
	for _, expected := range v.batches {
		actual, ok, readStatus := preflight.nextBatchStatus()
		if readStatus.failed() {
			return retirementPassResult{}, retirementSecondPassStatus{code: retirementSecondPassErrRead, read: readStatus}
		}
		if !ok || actual != expected {
			return retirementPassResult{}, retirementSecondPassStatus{
				code: retirementSecondPassErrRead,
				read: retirementReadStatus{code: retirementReadErrSelectionChanged},
			}
		}
	}

	workspace.cursor.reset(tree, retirementPageCheckOrdinary)
	cursor := &workspace.cursor
	var pages uint64
	for _, expected := range v.batches {
		batch, ok, readStatus := cursor.nextBatchStatus()
		if readStatus.failed() {
			return retirementPassResult{}, retirementSecondPassStatus{code: retirementSecondPassErrRead, read: readStatus}
		}
		if !ok || batch != expected {
			return retirementPassResult{}, retirementSecondPassStatus{
				code: retirementSecondPassErrRead,
				read: retirementReadStatus{code: retirementReadErrSelectionChanged},
			}
		}
		length, pageStatus := batch.blobLengthStatus()
		if pageStatus.failed() {
			return retirementPassResult{}, retirementSecondPassStatus{
				code: retirementSecondPassErrRead,
				read: retirementReadStatus{code: retirementReadErrPage, pageProblem: pageStatus},
			}
		}
		blob, blobStatus := newBlobTreeFromSourceStatus(
			tree.pages,
			tree.identity.txnID,
			tree.identity.pageCount,
			batch.pageListBlobRoot,
			blobKindRetirementPageList,
			length,
		)
		if blobStatus.failed() {
			return retirementPassResult{}, retirementSecondPassStatus{
				code: retirementSecondPassErrRead,
				read: retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus},
			}
		}
		reader, blobStatus := blob.retirementPagesWithWorkspaceStatus(blobPageCheckOrdinary, &workspace.blob)
		if blobStatus.failed() {
			return retirementPassResult{}, retirementSecondPassStatus{
				code: retirementSecondPassErrRead,
				read: retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus},
			}
		}
		for {
			page, ok, blobStatus := reader.nextPageStatus()
			if blobStatus.failed() {
				return retirementPassResult{}, retirementSecondPassStatus{
					code: retirementSecondPassErrRead,
					read: retirementReadStatus{code: retirementReadErrBlob, blobProblem: blobStatus},
				}
			}
			if !ok {
				break
			}
			if readStatus := tree.requireListedPageStatus(page); readStatus.failed() {
				return retirementPassResult{}, retirementSecondPassStatus{code: retirementSecondPassErrRead, read: readStatus}
			}
			if sinkStatus := sink(batch, page); sinkStatus.failed() {
				return retirementPassResult{}, retirementSecondPassStatus{code: retirementSecondPassErrSink, sink: sinkStatus}
			}
			var addOK bool
			pages, addOK = checkedAdd(pages, 1)
			if !addOK {
				return retirementPassResult{}, retirementSecondPassStatus{
					code: retirementSecondPassErrRead,
					read: retirementReadStatus{code: retirementReadErrArithmeticOverflow},
				}
			}
		}
	}
	if pages != v.selection.pageCount {
		return retirementPassResult{}, retirementSecondPassStatus{
			code: retirementSecondPassErrRead,
			read: retirementReadStatus{code: retirementReadErrSelectionChanged},
		}
	}
	return retirementPassResult{batchCount: v.selection.batchCount, pageCount: pages}, retirementSecondPassStatus{}
}
