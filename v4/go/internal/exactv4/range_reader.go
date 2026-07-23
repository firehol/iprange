package exactv4

import "fmt"

const rangePathCapacity = int(MaxTreeLevel) + 1

type rangeReadErrorCode uint8

const (
	rangeReadErrBootstrap rangeReadErrorCode = iota + 1
	rangeReadErrSource
	rangeReadErrPage
	rangeReadErrWrongKeyFamily
	rangeReadErrRootType
	rangeReadErrChildType
	rangeReadErrChildLevel
	rangeReadErrSummaryMismatch
	rangeReadErrRecordCountMismatch
	rangeReadErrCardinalityOverflow
	rangeReadErrCursorFailed
)

type rangeReadError struct {
	code          rangeReadErrorCode
	cause         error
	page          uint32
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
}

func (e *rangeReadError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range read: error %d", e.code)
}

func (e *rangeReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type rangeTree[K rangeKey[K]] struct {
	pages pinnedPageSource
}

func openImmutableRangeTree[K rangeKey[K]](data []byte) (rangeTree[K], error) {
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		return rangeTree[K]{}, &rangeReadError{code: rangeReadErrBootstrap, cause: err}
	}
	return newRangeTree[K](newSlicePageRead(data), bootstrap)
}

func newRangeTree[K rangeKey[K]](source positionalPageRead, bootstrap Bootstrap) (rangeTree[K], error) {
	var key K
	if bootstrap.Meta.AddressFamily != key.family() {
		return rangeTree[K]{}, &rangeReadError{code: rangeReadErrWrongKeyFamily}
	}
	pages, err := newPinnedPageSource(source, bootstrap)
	if err != nil {
		return rangeTree[K]{}, &rangeReadError{code: rangeReadErrSource, cause: err}
	}
	return rangeTree[K]{pages: pages}, nil
}

func (t *rangeTree[K]) meta() Meta { return t.pages.bootstrap.Meta }

func (t *rangeTree[K]) cursor() rangeCursor[K] {
	return rangeCursor[K]{tree: t, state: rangeCursorUnpositioned}
}

func (t *rangeTree[K]) lookup(target K) (rangeRecord[K], bool, error) {
	if err := t.checkAccess(); err != nil {
		return rangeRecord[K]{}, false, err
	}
	cursor := t.cursor()
	positioned, err := cursor.seekInner(target)
	if err != nil || !positioned {
		return rangeRecord[K]{}, false, err
	}
	record, ok, err := cursor.currentInner()
	if err != nil {
		return rangeRecord[K]{}, false, err
	}
	if !ok {
		return rangeRecord[K]{}, false, &rangeReadError{code: rangeReadErrSummaryMismatch}
	}
	if record.from.compare(target) <= 0 && target.compare(record.to) <= 0 {
		return record, true, nil
	}
	return rangeRecord[K]{}, false, nil
}

func (t *rangeTree[K]) countAddresses() (Cardinality129, error) {
	if err := t.checkAccess(); err != nil {
		return Cardinality129{}, err
	}
	cursor := t.cursor()
	positioned, err := cursor.firstInner()
	if err != nil {
		return Cardinality129{}, err
	}
	if !positioned {
		return CardinalityZero(), nil
	}
	total := CardinalityZero()
	for {
		record, ok, err := cursor.currentInner()
		if err != nil {
			return Cardinality129{}, err
		}
		if !ok {
			return Cardinality129{}, &rangeReadError{code: rangeReadErrSummaryMismatch}
		}
		fromHi, fromLo := record.from.halves()
		toHi, toLo := record.to.halves()
		var count Cardinality129
		if t.meta().AddressFamily == AddressFamilyIPv4 {
			count, err = IPv4Inclusive(uint32(fromLo), uint32(toLo))
		} else {
			count, err = IPv6Inclusive(fromHi, fromLo, toHi, toLo)
		}
		if err != nil {
			return Cardinality129{}, &rangeReadError{code: rangeReadErrCardinalityOverflow, cause: err}
		}
		total, err = total.Add(count)
		if err != nil {
			return Cardinality129{}, &rangeReadError{code: rangeReadErrCardinalityOverflow, cause: err}
		}
		positioned, err = cursor.nextInner()
		if err != nil {
			return Cardinality129{}, err
		}
		if !positioned {
			break
		}
	}
	return total, nil
}

func (t *rangeTree[K]) checkAccess() error {
	if err := t.pages.checkAccess(); err != nil {
		return &rangeReadError{code: rangeReadErrSource, cause: err}
	}
	return nil
}

func (t *rangeTree[K]) header(pageNumber uint32, page []byte) (PageHeader, error) {
	header, err := DecodePageHeader(page, t.meta().TxnID)
	if err != nil {
		return PageHeader{}, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	return header, nil
}

type rangeFrame struct {
	page  uint32
	index uint16
}

type rangeCursorState uint8

const (
	rangeCursorUnpositioned rangeCursorState = iota
	rangeCursorEmpty
	rangeCursorAt
	rangeCursorBeforeFirst
	rangeCursorAfterLast
	rangeCursorFailed
)

type rangeCursor[K rangeKey[K]] struct {
	tree         *rangeTree[K]
	path         [rangePathCapacity]rangeFrame
	rootLevel    uint16
	state        rangeCursorState
	scratch      [PageSize]byte
	scratchPage  uint32
	scratchValid bool
}

func (c *rangeCursor[K]) first() (bool, error) {
	if err := c.checkAccess(); err != nil {
		return c.finishMove(false, err)
	}
	if c.state == rangeCursorFailed {
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	positioned, err := c.firstInner()
	return c.finishMove(positioned, err)
}

func (c *rangeCursor[K]) firstInner() (bool, error) {
	root, level, ok, err := c.root()
	if err != nil {
		return false, err
	}
	if !ok {
		c.state = rangeCursorEmpty
		return false, nil
	}
	c.rootLevel = level
	positioned, err := c.descendEdge(0, root, level, rangeEdgeFirst)
	if err != nil {
		return false, err
	}
	if positioned {
		c.state = rangeCursorAt
		return true, nil
	}
	if c.tree.meta().RangeRecordCount != 0 {
		return false, &rangeReadError{code: rangeReadErrRecordCountMismatch}
	}
	c.state = rangeCursorEmpty
	return false, nil
}

func (c *rangeCursor[K]) last() (bool, error) {
	if err := c.checkAccess(); err != nil {
		return c.finishMove(false, err)
	}
	if c.state == rangeCursorFailed {
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	positioned, err := c.lastInner()
	return c.finishMove(positioned, err)
}

func (c *rangeCursor[K]) lastInner() (bool, error) {
	root, level, ok, err := c.root()
	if err != nil {
		return false, err
	}
	if !ok {
		c.state = rangeCursorEmpty
		return false, nil
	}
	c.rootLevel = level
	positioned, err := c.descendEdge(0, root, level, rangeEdgeLast)
	if err != nil {
		return false, err
	}
	if positioned {
		c.state = rangeCursorAt
		return true, nil
	}
	if c.tree.meta().RangeRecordCount != 0 {
		return false, &rangeReadError{code: rangeReadErrRecordCountMismatch}
	}
	c.state = rangeCursorEmpty
	return false, nil
}

// seek positions at the record covering target, or at its first successor.
func (c *rangeCursor[K]) seek(target K) (bool, error) {
	if err := c.checkAccess(); err != nil {
		return c.finishMove(false, err)
	}
	if c.state == rangeCursorFailed {
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	positioned, err := c.seekInner(target)
	return c.finishMove(positioned, err)
}

func (c *rangeCursor[K]) seekInner(target K) (bool, error) {
	root, level, ok, err := c.root()
	if err != nil {
		return false, err
	}
	if !ok {
		c.state = rangeCursorEmpty
		return false, nil
	}
	c.rootLevel = level
	positioned, err := c.descendPredecessor(0, root, level, target)
	if err != nil {
		return false, err
	}
	if !positioned {
		return c.firstInner()
	}
	c.state = rangeCursorAt
	record, ok, err := c.currentInner()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, &rangeReadError{code: rangeReadErrSummaryMismatch}
	}
	if record.to.compare(target) >= 0 {
		return true, nil
	}
	return c.nextInner()
}

func (c *rangeCursor[K]) next() (bool, error) {
	if err := c.checkAccess(); err != nil {
		return c.finishMove(false, err)
	}
	if c.state == rangeCursorFailed {
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	positioned, err := c.nextInner()
	return c.finishMove(positioned, err)
}

func (c *rangeCursor[K]) nextInner() (bool, error) {
	switch c.state {
	case rangeCursorUnpositioned, rangeCursorBeforeFirst:
		return c.firstInner()
	case rangeCursorEmpty, rangeCursorAfterLast:
		return false, nil
	case rangeCursorFailed:
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	case rangeCursorAt:
	}

	leafDepth := int(c.rootLevel)
	leafFrame := c.path[leafDepth]
	leafCount, err := c.leafCount(leafFrame.page)
	if err != nil {
		return false, err
	}
	if int(leafFrame.index)+1 < leafCount {
		c.path[leafDepth].index++
		return true, nil
	}

	for depth := leafDepth; depth != 0; {
		depth--
		frame := c.path[depth]
		level := c.rootLevel - uint16(depth)
		index, entry, found, err := c.branchNextNonempty(frame.page, level, int(frame.index)+1)
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		c.path[depth].index = uint16(index)
		positioned, err := c.descendEdge(depth+1, entry.childPage, level-1, rangeEdgeFirst)
		if err != nil {
			return false, err
		}
		if !positioned {
			return false, &rangeReadError{code: rangeReadErrSummaryMismatch}
		}
		return true, nil
	}
	c.state = rangeCursorAfterLast
	return false, nil
}

func (c *rangeCursor[K]) previous() (bool, error) {
	if err := c.checkAccess(); err != nil {
		return c.finishMove(false, err)
	}
	if c.state == rangeCursorFailed {
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	positioned, err := c.previousInner()
	return c.finishMove(positioned, err)
}

func (c *rangeCursor[K]) previousInner() (bool, error) {
	switch c.state {
	case rangeCursorUnpositioned, rangeCursorAfterLast:
		return c.lastInner()
	case rangeCursorEmpty, rangeCursorBeforeFirst:
		return false, nil
	case rangeCursorFailed:
		return false, &rangeReadError{code: rangeReadErrCursorFailed}
	case rangeCursorAt:
	}

	leafDepth := int(c.rootLevel)
	if c.path[leafDepth].index != 0 {
		c.path[leafDepth].index--
		return true, nil
	}
	for depth := leafDepth; depth != 0; {
		depth--
		frame := c.path[depth]
		level := c.rootLevel - uint16(depth)
		index, entry, found, err := c.branchPreviousNonempty(frame.page, level, int(frame.index))
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		c.path[depth].index = uint16(index)
		positioned, err := c.descendEdge(depth+1, entry.childPage, level-1, rangeEdgeLast)
		if err != nil {
			return false, err
		}
		if !positioned {
			return false, &rangeReadError{code: rangeReadErrSummaryMismatch}
		}
		return true, nil
	}
	c.state = rangeCursorBeforeFirst
	return false, nil
}

func (c *rangeCursor[K]) current() (rangeRecord[K], bool, error) {
	if err := c.checkAccess(); err != nil {
		c.state = rangeCursorFailed
		return rangeRecord[K]{}, false, err
	}
	return c.currentInner()
}

func (c *rangeCursor[K]) currentInner() (rangeRecord[K], bool, error) {
	if c.state == rangeCursorFailed {
		return rangeRecord[K]{}, false, &rangeReadError{code: rangeReadErrCursorFailed}
	}
	if c.state != rangeCursorAt {
		return rangeRecord[K]{}, false, nil
	}
	frame := c.path[int(c.rootLevel)]
	record, err := c.leafRecord(frame.page, int(frame.index))
	if err != nil {
		c.state = rangeCursorFailed
		return rangeRecord[K]{}, false, err
	}
	return record, true, nil
}

func (c *rangeCursor[K]) root() (uint32, uint16, bool, error) {
	root := c.tree.meta().RangeRoot
	if root == 0 {
		return 0, 0, false, nil
	}
	header, err := c.header(root)
	if err != nil {
		return 0, 0, false, err
	}
	switch header.PageType {
	case PageTypeRangeLeaf:
		return root, 0, true, nil
	case PageTypeRangeBranch:
		return root, header.Level, true, nil
	default:
		return 0, 0, false, &rangeReadError{code: rangeReadErrRootType, page: root, pageType: header.PageType}
	}
}

type rangeEdge uint8

const (
	rangeEdgeFirst rangeEdge = iota
	rangeEdgeLast
)

func (c *rangeCursor[K]) descendEdge(depth int, pageNumber uint32, level uint16, edge rangeEdge) (bool, error) {
	for {
		if depth >= rangePathCapacity {
			return false, &rangeReadError{code: rangeReadErrChildLevel, expectedLevel: 0, actualLevel: level}
		}
		if level == 0 {
			header, err := c.header(pageNumber)
			if err != nil {
				return false, err
			}
			if header.PageType != PageTypeRangeLeaf {
				return false, &rangeReadError{code: rangeReadErrChildType, page: pageNumber, pageType: header.PageType}
			}
			leafCount, err := c.leafCount(pageNumber)
			if err != nil {
				return false, err
			}
			if leafCount == 0 {
				return false, nil
			}
			index := uint16(0)
			if edge == rangeEdgeLast {
				index = uint16(leafCount - 1)
			}
			c.path[depth] = rangeFrame{page: pageNumber, index: index}
			c.state = rangeCursorAt
			return true, nil
		}

		header, err := c.header(pageNumber)
		if err != nil {
			return false, err
		}
		if header.PageType != PageTypeRangeBranch {
			return false, &rangeReadError{code: rangeReadErrChildType, page: pageNumber, pageType: header.PageType}
		}
		var (
			index int
			entry rangeBranchEntry[K]
			found bool
		)
		if edge == rangeEdgeFirst {
			index, entry, found, err = c.branchNextNonempty(pageNumber, level, 0)
		} else {
			index, entry, found, err = c.branchPreviousNonempty(pageNumber, level, int(^uint(0)>>1))
		}
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		c.path[depth] = rangeFrame{page: pageNumber, index: uint16(index)}
		pageNumber = entry.childPage
		level--
		depth++
	}
}

func (c *rangeCursor[K]) descendPredecessor(depth int, pageNumber uint32, level uint16, target K) (bool, error) {
	for {
		if depth >= rangePathCapacity {
			return false, &rangeReadError{code: rangeReadErrChildLevel, expectedLevel: 0, actualLevel: level}
		}
		if level == 0 {
			header, err := c.header(pageNumber)
			if err != nil {
				return false, err
			}
			if header.PageType != PageTypeRangeLeaf {
				return false, &rangeReadError{code: rangeReadErrChildType, page: pageNumber, pageType: header.PageType}
			}
			index, found, err := c.leafPredecessor(pageNumber, target)
			if err != nil {
				return false, err
			}
			if !found {
				return c.fallbackPredecessor(depth)
			}
			c.path[depth] = rangeFrame{page: pageNumber, index: uint16(index)}
			return true, nil
		}

		header, err := c.header(pageNumber)
		if err != nil {
			return false, err
		}
		if header.PageType != PageTypeRangeBranch {
			return false, &rangeReadError{code: rangeReadErrChildType, page: pageNumber, pageType: header.PageType}
		}
		index, entry, found, err := c.branchPredecessorFor(pageNumber, level, target)
		if err != nil {
			return false, err
		}
		if !found {
			return c.fallbackPredecessor(depth)
		}
		c.path[depth] = rangeFrame{page: pageNumber, index: uint16(index)}
		pageNumber = entry.childPage
		level--
		depth++
	}
}

func (c *rangeCursor[K]) fallbackPredecessor(depth int) (bool, error) {
	for depth != 0 {
		depth--
		frame := c.path[depth]
		level := c.rootLevel - uint16(depth)
		index, entry, found, err := c.branchPreviousNonempty(frame.page, level, int(frame.index))
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		c.path[depth].index = uint16(index)
		positioned, err := c.descendEdge(depth+1, entry.childPage, level-1, rangeEdgeLast)
		if err != nil {
			return false, err
		}
		if !positioned {
			return false, &rangeReadError{code: rangeReadErrSummaryMismatch}
		}
		return true, nil
	}
	return false, nil
}

func (c *rangeCursor[K]) finishMove(positioned bool, err error) (bool, error) {
	if err != nil {
		c.state = rangeCursorFailed
		return false, err
	}
	return positioned, nil
}

func (c *rangeCursor[K]) loadPage(pageNumber uint32) error {
	if c.scratchValid && c.scratchPage == pageNumber {
		return nil
	}
	c.scratchValid = false
	if err := c.tree.pages.readPage(pageNumber, &c.scratch); err != nil {
		return &rangeReadError{code: rangeReadErrSource, cause: err, page: pageNumber}
	}
	c.scratchPage = pageNumber
	c.scratchValid = true
	return nil
}

func (c *rangeCursor[K]) checkAccess() error {
	return c.tree.checkAccess()
}

func (c *rangeCursor[K]) header(pageNumber uint32) (PageHeader, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return PageHeader{}, err
	}
	return c.tree.header(pageNumber, c.scratch[:])
}

func (c *rangeCursor[K]) leafCount(pageNumber uint32) (int, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return 0, err
	}
	meta := c.tree.meta()
	leaf, err := inspectRangeLeaf[K](c.scratch[:], meta.TxnID, meta.AddressFamily, meta.ValueKind)
	if err != nil {
		return 0, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	return leaf.count, nil
}

func (c *rangeCursor[K]) leafRecord(pageNumber uint32, index int) (rangeRecord[K], error) {
	if err := c.loadPage(pageNumber); err != nil {
		return rangeRecord[K]{}, err
	}
	meta := c.tree.meta()
	leaf, err := inspectRangeLeaf[K](c.scratch[:], meta.TxnID, meta.AddressFamily, meta.ValueKind)
	if err != nil {
		return rangeRecord[K]{}, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	record, err := decodeRangeRecord[K](c.scratch[:], leaf, index)
	if err != nil {
		return rangeRecord[K]{}, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	return record, nil
}

func (c *rangeCursor[K]) leafPredecessor(pageNumber uint32, target K) (int, bool, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return 0, false, err
	}
	meta := c.tree.meta()
	leaf, err := inspectRangeLeaf[K](c.scratch[:], meta.TxnID, meta.AddressFamily, meta.ValueKind)
	if err != nil {
		return 0, false, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	low, high := 0, leaf.count
	for low < high {
		middle := low + (high-low)/2
		record, err := decodeRangeRecord[K](c.scratch[:], leaf, middle)
		if err != nil {
			return 0, false, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
		}
		if record.from.compare(target) <= 0 {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == 0 {
		return 0, false, nil
	}
	return low - 1, true, nil
}

func (c *rangeCursor[K]) branchNextNonempty(
	pageNumber uint32,
	expectedLevel uint16,
	from int,
) (int, rangeBranchEntry[K], bool, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	branch, err := c.branchInfo(pageNumber, expectedLevel)
	if err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	if from < 0 {
		from = 0
	}
	for index := from; index < branch.count; index++ {
		entry, err := decodeRangeBranchEntry[K](c.scratch[:], branch, index)
		if err != nil {
			return c.branchNavigationError(pageNumber, err)
		}
		if !entry.empty() {
			return index, entry, true, nil
		}
	}
	return 0, rangeBranchEntry[K]{}, false, nil
}

func (c *rangeCursor[K]) branchPreviousNonempty(
	pageNumber uint32,
	expectedLevel uint16,
	before int,
) (int, rangeBranchEntry[K], bool, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	branch, err := c.branchInfo(pageNumber, expectedLevel)
	if err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	if before > branch.count {
		before = branch.count
	}
	for index := before - 1; index >= 0; index-- {
		entry, err := decodeRangeBranchEntry[K](c.scratch[:], branch, index)
		if err != nil {
			return c.branchNavigationError(pageNumber, err)
		}
		if !entry.empty() {
			return index, entry, true, nil
		}
	}
	return 0, rangeBranchEntry[K]{}, false, nil
}

func (c *rangeCursor[K]) branchPredecessorFor(
	pageNumber uint32,
	expectedLevel uint16,
	target K,
) (int, rangeBranchEntry[K], bool, error) {
	if err := c.loadPage(pageNumber); err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	branch, err := c.branchInfo(pageNumber, expectedLevel)
	if err != nil {
		return 0, rangeBranchEntry[K]{}, false, err
	}
	result := 0
	found := false
	var resultEntry rangeBranchEntry[K]
	for index := 0; index < branch.count; index++ {
		entry, err := decodeRangeBranchEntry[K](c.scratch[:], branch, index)
		if err != nil {
			return c.branchNavigationError(pageNumber, err)
		}
		if !entry.empty() && entry.firstFrom.compare(target) <= 0 {
			result, resultEntry, found = index, entry, true
		}
	}
	return result, resultEntry, found, nil
}

func (c *rangeCursor[K]) branchInfo(
	pageNumber uint32,
	expectedLevel uint16,
) (rangeBranchInfo, error) {
	meta := c.tree.meta()
	branch, err := inspectRangeBranch[K](
		c.scratch[:],
		meta.TxnID,
		meta.AddressFamily,
		meta.PageCount,
	)
	if err != nil {
		return rangeBranchInfo{}, &rangeReadError{code: rangeReadErrPage, cause: err, page: pageNumber}
	}
	if branch.level != expectedLevel {
		return rangeBranchInfo{}, &rangeReadError{
			code:          rangeReadErrChildLevel,
			page:          pageNumber,
			expectedLevel: expectedLevel,
			actualLevel:   branch.level,
		}
	}
	return branch, nil
}

func (c *rangeCursor[K]) branchNavigationError(
	pageNumber uint32,
	err error,
) (int, rangeBranchEntry[K], bool, error) {
	return 0, rangeBranchEntry[K]{}, false, &rangeReadError{
		code:  rangeReadErrPage,
		cause: err,
		page:  pageNumber,
	}
}
