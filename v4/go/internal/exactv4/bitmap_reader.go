package exactv4

import (
	"fmt"
	"math/bits"
)

type bitmapReadErrorCode uint8

const (
	bitmapReadErrBootstrap bitmapReadErrorCode = iota + 1
	bitmapReadErrSource
	bitmapReadErrPage
	bitmapReadErrPageOutOfBounds
	bitmapReadErrPageOffsetOverflow
	bitmapReadErrRootType
	bitmapReadErrRootLevel
	bitmapReadErrChildLevel
	bitmapReadErrCoverageOverflow
	bitmapReadErrSelectedChildMissing
	bitmapReadErrSelectedCoverageOutsideLimit
	bitmapReadErrSummaryMismatch
)

type bitmapReadError struct {
	code          bitmapReadErrorCode
	cause         error
	page          uint32
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
}

func (e *bitmapReadError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 bitmap read: error %d", e.code)
}

func (e *bitmapReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type bitmapTree[S committedPageSource] struct {
	pages         S
	selectedTxn   uint64
	pageCount     uint64
	root          uint32
	kind          bitmapKind
	limit         uint64
	firstEligible uint64
}

type bitmapReadWorkspace struct {
	page [PageSize]byte
}

func openImmutableFreeBitmapTree(data []byte) (bitmapTree[immutableSlicePageSource], error) {
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		return bitmapTree[immutableSlicePageSource]{}, &bitmapReadError{code: bitmapReadErrBootstrap, cause: err}
	}
	if bootstrap.CommittedBytes > uint64(len(data)) {
		return bitmapTree[immutableSlicePageSource]{}, &bitmapReadError{code: bitmapReadErrPageOffsetOverflow}
	}
	committed := int(bootstrap.CommittedBytes)
	if uint64(committed) != bootstrap.CommittedBytes {
		return bitmapTree[immutableSlicePageSource]{}, &bitmapReadError{code: bitmapReadErrPageOffsetOverflow}
	}
	return newBitmapTree(
		newImmutableSlicePageSource(data[:committed], bootstrap.Meta.PageCount),
		bootstrap,
		bootstrap.Meta.FreeBitmapRoot,
		bitmapKindFreePages,
		bootstrap.Meta.PageCount,
		2,
	), nil
}

func newBitmapTree[S committedPageSource](
	pages S,
	bootstrap Bootstrap,
	root uint32,
	kind bitmapKind,
	limit uint64,
	firstEligible uint64,
) bitmapTree[S] {
	return bitmapTree[S]{
		pages:         pages,
		selectedTxn:   bootstrap.Meta.TxnID,
		pageCount:     bootstrap.Meta.PageCount,
		root:          root,
		kind:          kind,
		limit:         limit,
		firstEligible: firstEligible,
	}
}

// lowestFree finds a candidate without checking page CRCs. It is for
// inspection only; destructive reuse must call lowestFreeVerified.
func (t bitmapTree[S]) lowestFree() (uint64, bool, error) {
	return t.lowestCandidate(false)
}

func (t bitmapTree[S]) lowestFreeWithWorkspace(
	workspace *bitmapReadWorkspace,
) (uint64, bool, error) {
	return t.lowestCandidateInto(false, &workspace.page)
}

// lowestFreeVerified performs allocator-grade CRC and local checks on the
// selected path. Writer integration must retain these verified pages in its
// transaction-owned COW state so each committed page is checked at most once
// before destructive reuse.
func (t bitmapTree[S]) lowestFreeVerified() (uint64, bool, error) {
	return t.lowestCandidate(true)
}

func (t bitmapTree[S]) lowestFreeVerifiedWithWorkspace(
	workspace *bitmapReadWorkspace,
) (uint64, bool, error) {
	return t.lowestCandidateInto(true, &workspace.page)
}

func (t bitmapTree[S]) lowestUnused() (uint64, bool, error) {
	return t.lowestCandidate(false)
}

func (t bitmapTree[S]) lowestUnusedWithWorkspace(
	workspace *bitmapReadWorkspace,
) (uint64, bool, error) {
	return t.lowestCandidateInto(false, &workspace.page)
}

func (t bitmapTree[S]) lowestCandidate(verifySelectedPath bool) (uint64, bool, error) {
	var page [PageSize]byte
	return t.lowestCandidateInto(verifySelectedPath, &page)
}

func (t bitmapTree[S]) lowestCandidateInto(
	verifySelectedPath bool,
	page *[PageSize]byte,
) (uint64, bool, error) {
	if status := t.pages.checkAccessStatus(); status.failed() {
		return 0, false, &bitmapReadError{code: bitmapReadErrSource, cause: status.asError()}
	}
	start := t.firstEligible
	if start >= t.limit {
		return 0, false, nil
	}
	if t.root == 0 {
		if t.kind == bitmapKindFreePages {
			return 0, false, nil
		}
		return start, true, nil
	}

	expectedRootLevel, err := minimumBitmapLevel(t.limit)
	if err != nil {
		return 0, false, err
	}
	rootHeader, err := t.header(t.root, page)
	if err != nil {
		return 0, false, err
	}
	var actualRootLevel uint16
	switch rootHeader.PageType {
	case PageTypeBitmapLeaf:
		actualRootLevel = 0
	case PageTypeBitmapBranch:
		actualRootLevel = rootHeader.Level
	default:
		return 0, false, &bitmapReadError{
			code:     bitmapReadErrRootType,
			page:     t.root,
			pageType: rootHeader.PageType,
		}
	}
	if actualRootLevel != expectedRootLevel {
		return 0, false, &bitmapReadError{
			code:          bitmapReadErrRootLevel,
			page:          t.root,
			expectedLevel: expectedRootLevel,
			actualLevel:   actualRootLevel,
		}
	}

	pageNumber := t.root
	level := actualRootLevel
	base := uint64(0)
	selectedBySummary := false
	for level != 0 {
		if err := t.page(pageNumber, page); err != nil {
			return 0, false, err
		}
		branch, err := openBitmapBranch(page[:], t.selectedTxn, t.kind)
		if err != nil {
			return 0, false, &bitmapReadError{
				code:  bitmapReadErrPage,
				cause: err,
				page:  pageNumber,
			}
		}
		if branch.level() != level {
			return 0, false, &bitmapReadError{
				code:          bitmapReadErrChildLevel,
				page:          pageNumber,
				expectedLevel: level,
				actualLevel:   branch.level(),
			}
		}
		childSpan, err := bitmapCoverage(level - 1)
		if err != nil {
			return 0, false, err
		}
		if verifySelectedPath {
			if err := branch.verifyLocal(base, childSpan, t.limit, t.pageCount); err != nil {
				return 0, false, &bitmapReadError{
					code:  bitmapReadErrPage,
					cause: err,
					page:  pageNumber,
				}
			}
		}

		firstChild := 0
		if start > base {
			firstChild64 := (start - base) / childSpan
			firstChild = int(firstChild64)
			if uint64(firstChild) != firstChild64 {
				return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
			}
		}
		index, ok := branch.nextSummary(firstChild)
		if !ok {
			if selectedBySummary {
				return 0, false, &bitmapReadError{code: bitmapReadErrSummaryMismatch}
			}
			return 0, false, nil
		}
		offset, ok := checkedMul(childSpan, uint64(index))
		if !ok {
			return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
		childBase, ok := checkedAdd(base, offset)
		if !ok {
			return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
		if childBase >= t.limit {
			return 0, false, &bitmapReadError{code: bitmapReadErrSelectedCoverageOutsideLimit}
		}
		child := branch.child(index)
		if child == 0 {
			if t.kind == bitmapKindFreePages {
				return 0, false, &bitmapReadError{code: bitmapReadErrSelectedChildMissing}
			}
			if start > childBase {
				return start, true, nil
			}
			return childBase, true, nil
		}

		childHeader, err := t.header(child, page)
		if err != nil {
			return 0, false, err
		}
		var childLevel uint16
		switch childHeader.PageType {
		case PageTypeBitmapLeaf:
			childLevel = 0
		case PageTypeBitmapBranch:
			childLevel = childHeader.Level
		default:
			return 0, false, &bitmapReadError{
				code:     bitmapReadErrRootType,
				page:     child,
				pageType: childHeader.PageType,
			}
		}
		if childLevel != level-1 {
			return 0, false, &bitmapReadError{
				code:          bitmapReadErrChildLevel,
				page:          child,
				expectedLevel: level - 1,
				actualLevel:   childLevel,
			}
		}
		pageNumber = child
		level--
		base = childBase
		selectedBySummary = true
	}

	if err := t.page(pageNumber, page); err != nil {
		return 0, false, err
	}
	leaf, err := openBitmapLeaf(page[:], t.selectedTxn, t.kind)
	if err != nil {
		return 0, false, &bitmapReadError{
			code:  bitmapReadErrPage,
			cause: err,
			page:  pageNumber,
		}
	}
	if verifySelectedPath {
		if err := leaf.verifyLocal(t.kind, base, t.limit); err != nil {
			return 0, false, &bitmapReadError{
				code:  bitmapReadErrPage,
				cause: err,
				page:  pageNumber,
			}
		}
	}
	found, ok, err := t.searchLeaf(leaf, base, start)
	if err != nil {
		return 0, false, err
	}
	if !ok && selectedBySummary {
		return 0, false, &bitmapReadError{code: bitmapReadErrSummaryMismatch}
	}
	return found, ok, nil
}

func (t bitmapTree[S]) searchLeaf(leaf bitmapLeaf, base, start uint64) (uint64, bool, error) {
	first := base
	if start > first {
		first = start
	}
	if first >= t.limit {
		return 0, false, nil
	}
	local := first - base
	firstWord64 := local / 64
	wordIndex := int(firstWord64)
	if uint64(wordIndex) != firstWord64 {
		return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
	}
	firstWord := wordIndex
	firstBit := uint(local % 64)
	for wordIndex < BitmapLeafWords {
		wordBase, ok := checkedAdd(base, uint64(wordIndex)*64)
		if !ok {
			return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
		if wordBase >= t.limit {
			break
		}
		stored := leaf.word(wordIndex)
		candidates := stored
		if t.kind != bitmapKindFreePages {
			candidates = ^stored
		}
		if wordIndex == firstWord {
			candidates &= ^uint64(0) << firstBit
		}
		remaining := t.limit - wordBase
		if remaining < 64 {
			candidates &= (uint64(1) << uint(remaining)) - 1
		}
		if candidates != 0 {
			candidate, ok := checkedAdd(wordBase, uint64(bits.TrailingZeros64(candidates)))
			if !ok {
				return 0, false, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
			}
			return candidate, true, nil
		}
		wordIndex++
	}
	return 0, false, nil
}

func (t bitmapTree[S]) header(pageNumber uint32, page *[PageSize]byte) (PageHeader, error) {
	if err := t.page(pageNumber, page); err != nil {
		return PageHeader{}, err
	}
	header, err := DecodePageHeader(page[:], t.selectedTxn)
	if err != nil {
		return PageHeader{}, &bitmapReadError{
			code: bitmapReadErrPage,
			cause: &bitmapPageError{
				code:  bitmapPageErrHeader,
				cause: err,
			},
			page: pageNumber,
		}
	}
	return header, nil
}

func (t bitmapTree[S]) page(pageNumber uint32, destination *[PageSize]byte) error {
	if status := t.pages.readPageStatus(pageNumber, destination); status.failed() {
		return &bitmapReadError{code: bitmapReadErrSource, cause: status.asError(), page: pageNumber}
	}
	return nil
}

func minimumBitmapLevel(limit uint64) (uint16, error) {
	level := uint16(0)
	covered := BitmapLeafBits
	for covered < limit {
		if level == MaxTreeLevel {
			return 0, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
		var ok bool
		covered, ok = checkedMul(covered, BitmapFanout)
		if !ok {
			return 0, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
		level++
	}
	return level, nil
}

func bitmapCoverage(level uint16) (uint64, error) {
	if level > MaxTreeLevel {
		return 0, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
	}
	covered := BitmapLeafBits
	for range level {
		var ok bool
		covered, ok = checkedMul(covered, BitmapFanout)
		if !ok {
			return 0, &bitmapReadError{code: bitmapReadErrCoverageOverflow}
		}
	}
	return covered, nil
}
