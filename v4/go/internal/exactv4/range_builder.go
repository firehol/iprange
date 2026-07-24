package exactv4

import "fmt"

// IPv6 has the smaller branch fanout (50). Six compact branch levels can
// cover more than every addressable non-meta page in a u32 page space.
const (
	compactRangeBranchLevels   = 6
	maxRangeBuilderLeafRecords = (PageSize - int(PageHeaderSize)) / 12
	maxRangeBuilderEntries     = (PageSize - int(PageHeaderSize)) / 32
)

// rangeTreePageSink owns private page allocation for one draft. It must copy
// or persist page before returning: the packer reuses its page buffer after
// every call and never owns allocation, cleanup, or rollback.
type rangeTreePageSink interface {
	writeRangePage(page *[PageSize]byte) (uint32, error)
}

type rangeTreeBuildStartErrorCode uint8

const (
	rangeTreeBuildStartErrBornTransactionZero rangeTreeBuildStartErrorCode = iota + 1
	rangeTreeBuildStartErrPageCount
)

type rangeTreeBuildStartError struct {
	code  rangeTreeBuildStartErrorCode
	pages uint64
}

func (e *rangeTreeBuildStartError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range tree build start: error %d", e.code)
}

type rangeTreeBuildErrorCode uint8

const (
	rangeTreeBuildErrFinished rangeTreeBuildErrorCode = iota + 1
	rangeTreeBuildErrFailed
	rangeTreeBuildErrRangeReversed
	rangeTreeBuildErrMembershipValueZero
	rangeTreeBuildErrRangeOverlap
	rangeTreeBuildErrAdjacentEqualValue
	rangeTreeBuildErrRecordCountOverflow
	rangeTreeBuildErrPage
	rangeTreeBuildErrSink
	rangeTreeBuildErrSinkPageOutOfBounds
	rangeTreeBuildErrTreeTooDeep
)

type rangeTreeBuildError struct {
	code  rangeTreeBuildErrorCode
	cause error
	page  uint32
}

func (e *rangeTreeBuildError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range tree build: error %d", e.code)
}

func (e *rangeTreeBuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type rangeTreeBuildResult struct {
	rootPage    uint32
	rootLevel   uint16
	recordCount uint64
}

type rangeTreeNode[K rangeKey[K]] struct {
	page        uint32
	level       uint16
	recordCount uint64
	lowerFence  K
	firstFrom   K
	lastFrom    K
	lastTo      K
}

func (n rangeTreeNode[K]) branchEntry() rangeBranchEntry[K] {
	return rangeBranchEntry[K]{
		lowerFence:         n.lowerFence,
		childPage:          n.page,
		subtreeRecordCount: n.recordCount,
		firstFrom:          n.firstFrom,
		lastFrom:           n.lastFrom,
		lastTo:             n.lastTo,
	}
}

type rangeTreeBuildLevel[K rangeKey[K]] struct {
	current      [maxRangeBuilderEntries]rangeBranchEntry[K]
	currentLen   int
	pending      [maxRangeBuilderEntries]rangeBranchEntry[K]
	pendingLen   int
	emittedCount uint64
}

func (l *rangeTreeBuildLevel[K]) reset() {
	l.currentLen = 0
	l.pendingLen = 0
	l.emittedCount = 0
}

// rangeTreeBuildWorkspace is fixed reusable storage for an ordered tree
// build. It retains only one unfinished leaf and two branch groups at each
// compact level; normal builds allocate no memory after construction.
type rangeTreeBuildWorkspace[K rangeKey[K]] struct {
	leaf    [maxRangeBuilderLeafRecords]rangeRecord[K]
	leafLen int
	levels  [compactRangeBranchLevels]rangeTreeBuildLevel[K]
	page    [PageSize]byte
}

func (w *rangeTreeBuildWorkspace[K]) begin(
	bornTxn uint64,
	valueKind ValueKind,
	pageCount uint64,
) (rangeTreeBuilder[K], error) {
	if bornTxn == 0 {
		return rangeTreeBuilder[K]{}, &rangeTreeBuildStartError{code: rangeTreeBuildStartErrBornTransactionZero}
	}
	if pageCount < 2 || pageCount > MaxPageCount {
		return rangeTreeBuilder[K]{}, &rangeTreeBuildStartError{code: rangeTreeBuildStartErrPageCount, pages: pageCount}
	}
	w.leafLen = 0
	for index := range w.levels {
		w.levels[index].reset()
	}
	return rangeTreeBuilder[K]{
		workspace: w,
		bornTxn:   bornTxn,
		valueKind: valueKind,
		pageCount: pageCount,
	}, nil
}

// rangeTreeBuilder packs a canonical ordered range stream into private pages.
// It intentionally does not normalize records or choose page numbers.
type rangeTreeBuilder[K rangeKey[K]] struct {
	workspace   *rangeTreeBuildWorkspace[K]
	bornTxn     uint64
	valueKind   ValueKind
	pageCount   uint64
	recordCount uint64
	leafCount   uint64
	lastRecord  rangeRecord[K]
	hasLast     bool
	finished    bool
	failed      bool
}

// push adds one canonical ordered record. Any rejected input or sink failure
// poisons this builder because the surrounding transaction must discard the
// whole draft on failure.
func (b *rangeTreeBuilder[K]) push(sink rangeTreePageSink, record rangeRecord[K]) error {
	if b.finished {
		return &rangeTreeBuildError{code: rangeTreeBuildErrFinished}
	}
	if b.failed {
		return &rangeTreeBuildError{code: rangeTreeBuildErrFailed}
	}
	err := b.pushInner(sink, record)
	if err != nil {
		b.failed = true
	}
	return err
}

// finish seals the input and returns the root summary for the next meta page.
// Empty input has root page zero and writes no range pages.
func (b *rangeTreeBuilder[K]) finish(sink rangeTreePageSink) (rangeTreeBuildResult, error) {
	if b.finished {
		return rangeTreeBuildResult{}, &rangeTreeBuildError{code: rangeTreeBuildErrFinished}
	}
	if b.failed {
		return rangeTreeBuildResult{}, &rangeTreeBuildError{code: rangeTreeBuildErrFailed}
	}
	result, err := b.finishInner(sink)
	if err != nil {
		b.failed = true
		return rangeTreeBuildResult{}, err
	}
	b.finished = true
	return result, nil
}

func (b *rangeTreeBuilder[K]) pushInner(sink rangeTreePageSink, record rangeRecord[K]) error {
	if err := b.validateRecord(record); err != nil {
		return err
	}
	if b.recordCount == ^uint64(0) {
		return &rangeTreeBuildError{code: rangeTreeBuildErrRecordCountOverflow}
	}
	b.recordCount++
	b.workspace.leaf[b.workspace.leafLen] = record
	b.workspace.leafLen++
	b.lastRecord = record
	b.hasLast = true
	if b.workspace.leafLen == rangeLeafCapacity[K]() {
		return b.flushLeaf(sink)
	}
	return nil
}

func (b *rangeTreeBuilder[K]) validateRecord(record rangeRecord[K]) error {
	if record.from.compare(record.to) > 0 {
		return &rangeTreeBuildError{code: rangeTreeBuildErrRangeReversed}
	}
	if b.valueKind == ValueKindMembership && record.value == 0 {
		return &rangeTreeBuildError{code: rangeTreeBuildErrMembershipValueZero}
	}
	if !b.hasLast {
		return nil
	}
	if b.lastRecord.to.compare(record.from) >= 0 {
		return &rangeTreeBuildError{code: rangeTreeBuildErrRangeOverlap}
	}
	if next, ok := b.lastRecord.to.next(); ok && next.compare(record.from) == 0 && b.lastRecord.value == record.value {
		return &rangeTreeBuildError{code: rangeTreeBuildErrAdjacentEqualValue}
	}
	return nil
}

func (b *rangeTreeBuilder[K]) flushLeaf(sink rangeTreePageSink) error {
	length := b.workspace.leafLen
	if length == 0 {
		return &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
	}
	records := b.workspace.leaf[:length]
	if err := encodeRangeLeaf(b.workspace.page[:], b.bornTxn, b.valueKind, records); err != nil {
		return &rangeTreeBuildError{code: rangeTreeBuildErrPage, cause: err}
	}
	firstFrom := records[0].from
	lastFrom := records[length-1].from
	lastTo := records[length-1].to
	page, err := b.writeEncodedPage(sink)
	if err != nil {
		return err
	}
	lowerFence := firstFrom
	if b.leafCount == 0 {
		lowerFence = lowerFence.minimum()
	}
	b.leafCount++
	b.workspace.leafLen = 0
	return b.pushNode(sink, 1, rangeTreeNode[K]{
		page:        page,
		level:       0,
		recordCount: uint64(length),
		lowerFence:  lowerFence,
		firstFrom:   firstFrom,
		lastFrom:    lastFrom,
		lastTo:      lastTo,
	})
}

func (b *rangeTreeBuilder[K]) pushNode(sink rangeTreePageSink, branchLevel int, node rangeTreeNode[K]) error {
	if branchLevel == 0 || branchLevel > compactRangeBranchLevels {
		return &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
	}
	capacity := rangeBranchCapacity[K]()
	state := &b.workspace.levels[branchLevel-1]
	if state.currentLen >= capacity {
		return &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
	}
	state.current[state.currentLen] = node.branchEntry()
	state.currentLen++

	if state.currentLen == capacity {
		if state.pendingLen != 0 {
			if err := b.flushPending(sink, branchLevel); err != nil {
				return err
			}
		}
		state = &b.workspace.levels[branchLevel-1]
		copy(state.pending[:capacity], state.current[:capacity])
		state.pendingLen = capacity
		state.currentLen = 0
	} else if state.pendingLen != 0 && state.currentLen == 2 {
		if err := b.flushPending(sink, branchLevel); err != nil {
			return err
		}
	}
	return nil
}

func (b *rangeTreeBuilder[K]) flushPending(sink rangeTreePageSink, branchLevel int) error {
	state := &b.workspace.levels[branchLevel-1]
	if state.pendingLen == 0 || state.currentLen < 2 {
		return &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
	}
	length := state.pendingLen
	upperFence := state.current[0].lowerFence
	node, err := b.emitBranch(sink, uint16(branchLevel), true, length, upperFence, true)
	if err != nil {
		return err
	}
	state = &b.workspace.levels[branchLevel-1]
	state.pendingLen = 0
	state.emittedCount++
	return b.pushNode(sink, branchLevel+1, node)
}

func (b *rangeTreeBuilder[K]) emitBranch(
	sink rangeTreePageSink,
	level uint16,
	pending bool,
	length int,
	upperFence K,
	hasUpperFence bool,
) (rangeTreeNode[K], error) {
	if length == 0 {
		return rangeTreeNode[K]{}, &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
	}
	state := &b.workspace.levels[int(level)-1]
	entries := state.current[:length]
	if pending {
		entries = state.pending[:length]
	}
	first := entries[0]
	last := entries[length-1]
	var recordCount uint64
	for _, entry := range entries {
		if ^uint64(0)-recordCount < entry.subtreeRecordCount {
			return rangeTreeNode[K]{}, &rangeTreeBuildError{code: rangeTreeBuildErrRecordCountOverflow}
		}
		recordCount += entry.subtreeRecordCount
	}
	if err := encodeRangeBranch(
		b.workspace.page[:],
		b.bornTxn,
		level,
		b.pageCount,
		first.lowerFence,
		upperFence,
		hasUpperFence,
		entries,
	); err != nil {
		return rangeTreeNode[K]{}, &rangeTreeBuildError{code: rangeTreeBuildErrPage, cause: err}
	}
	page, err := b.writeEncodedPage(sink)
	if err != nil {
		return rangeTreeNode[K]{}, err
	}
	return rangeTreeNode[K]{
		page:        page,
		level:       level,
		recordCount: recordCount,
		lowerFence:  first.lowerFence,
		firstFrom:   first.firstFrom,
		lastFrom:    last.lastFrom,
		lastTo:      last.lastTo,
	}, nil
}

func (b *rangeTreeBuilder[K]) writeEncodedPage(sink rangeTreePageSink) (uint32, error) {
	page, err := sink.writeRangePage(&b.workspace.page)
	if err != nil {
		return 0, &rangeTreeBuildError{code: rangeTreeBuildErrSink, cause: err}
	}
	if page < 2 || uint64(page) >= b.pageCount {
		return 0, &rangeTreeBuildError{code: rangeTreeBuildErrSinkPageOutOfBounds, page: page}
	}
	return page, nil
}

func (b *rangeTreeBuilder[K]) finishInner(sink rangeTreePageSink) (rangeTreeBuildResult, error) {
	if b.workspace.leafLen != 0 {
		if err := b.flushLeaf(sink); err != nil {
			return rangeTreeBuildResult{}, err
		}
	}
	if b.recordCount == 0 {
		return rangeTreeBuildResult{}, nil
	}
	for branchLevel := 1; branchLevel <= compactRangeBranchLevels; branchLevel++ {
		root, ok, err := b.finishLevel(sink, branchLevel)
		if err != nil {
			return rangeTreeBuildResult{}, err
		}
		if ok {
			return rangeTreeBuildResult{
				rootPage:    root.page,
				rootLevel:   root.level,
				recordCount: b.recordCount,
			}, nil
		}
	}
	return rangeTreeBuildResult{}, &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
}

// finishLevel either returns the sole root, or sends every final node to the
// following level. A non-root branch always receives at least two children.
func (b *rangeTreeBuilder[K]) finishLevel(
	sink rangeTreePageSink,
	branchLevel int,
) (rangeTreeNode[K], bool, error) {
	var noUpperFence K
	state := &b.workspace.levels[branchLevel-1]
	pendingLen, currentLen, emittedCount := state.pendingLen, state.currentLen, state.emittedCount
	if pendingLen == 0 && currentLen == 0 {
		return rangeTreeNode[K]{}, false, nil
	}
	if pendingLen == 0 && currentLen == 1 {
		if emittedCount != 0 {
			return rangeTreeNode[K]{}, false, &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
		}
		entry := state.current[0]
		return rangeTreeNode[K]{
			page:        entry.childPage,
			level:       uint16(branchLevel - 1),
			recordCount: entry.subtreeRecordCount,
			lowerFence:  entry.lowerFence,
			firstFrom:   entry.firstFrom,
			lastFrom:    entry.lastFrom,
			lastTo:      entry.lastTo,
		}, true, nil
	}

	if pendingLen != 0 && currentLen == 1 {
		if pendingLen < 2 {
			return rangeTreeNode[K]{}, false, &rangeTreeBuildError{code: rangeTreeBuildErrTreeTooDeep}
		}
		state.current[1] = state.current[0]
		state.current[0] = state.pending[pendingLen-1]
		state.currentLen = 2
		state.pendingLen--
		left, err := b.emitBranch(
			sink,
			uint16(branchLevel),
			true,
			pendingLen-1,
			state.current[0].lowerFence,
			true,
		)
		if err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		right, err := b.emitBranch(sink, uint16(branchLevel), false, 2, noUpperFence, false)
		if err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		if err := b.pushNode(sink, branchLevel+1, left); err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		if err := b.pushNode(sink, branchLevel+1, right); err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		return rangeTreeNode[K]{}, false, nil
	}

	if pendingLen != 0 && currentLen >= 2 {
		left, err := b.emitBranch(
			sink,
			uint16(branchLevel),
			true,
			pendingLen,
			state.current[0].lowerFence,
			true,
		)
		if err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		right, err := b.emitBranch(sink, uint16(branchLevel), false, currentLen, noUpperFence, false)
		if err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		if err := b.pushNode(sink, branchLevel+1, left); err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		if err := b.pushNode(sink, branchLevel+1, right); err != nil {
			return rangeTreeNode[K]{}, false, err
		}
		return rangeTreeNode[K]{}, false, nil
	}

	node, err := b.emitBranch(sink, uint16(branchLevel), pendingLen != 0, max(pendingLen, currentLen), noUpperFence, false)
	if err != nil {
		return rangeTreeNode[K]{}, false, err
	}
	if emittedCount == 0 {
		return node, true, nil
	}
	if err := b.pushNode(sink, branchLevel+1, node); err != nil {
		return rangeTreeNode[K]{}, false, err
	}
	return rangeTreeNode[K]{}, false, nil
}
