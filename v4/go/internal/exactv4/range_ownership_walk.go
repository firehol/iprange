package exactv4

import "fmt"

const (
	rangeOwnershipPathCapacity = int(MaxTreeLevel) + 1
	rangeOwnershipRootLevel    = ^uint16(0)
)

type rangeOwnershipFrame struct {
	page          uint32
	expectedLevel uint16
	nextChild     int
	loaded        bool
}

// rangeTreeOwnershipScratch is fixed caller-owned control storage for one
// complete selected-range traversal. Its buffers are logical scratch only.
type rangeTreeOwnershipScratch struct {
	pages  [rangeOwnershipPathCapacity][PageSize]byte
	frames [rangeOwnershipPathCapacity]rangeOwnershipFrame
}

func (s *rangeTreeOwnershipScratch) reset() {
	if s != nil {
		clear(s.frames[:])
	}
}

type rangeOwnershipErrorCode uint8

const (
	rangeOwnershipErrSource rangeOwnershipErrorCode = iota + 1
	rangeOwnershipErrPage
	rangeOwnershipErrWrongKeyFamily
	rangeOwnershipErrRootRecordCount
	rangeOwnershipErrRootBounds
	rangeOwnershipErrRootType
	rangeOwnershipErrChildType
	rangeOwnershipErrChildLevel
	rangeOwnershipErrWorkBudget
	rangeOwnershipErrIndex
	rangeOwnershipErrScratch
)

type rangeOwnershipError struct {
	code          rangeOwnershipErrorCode
	cause         error
	page          uint32
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
	required      uint64
	actual        uint64
}

func (e *rangeOwnershipError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range ownership walk: error %d", e.code)
}

func (e *rangeOwnershipError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func rangeOwnershipNextWork(work uint64) uint64 {
	if work == ^uint64(0) {
		return work
	}
	return work + 1
}

// collectRangeTreeOwnership adds every selected reachable range page to
// index. It performs ordinary local access checks only; global ownership and
// range validation remain the explicit Validate operation.
func collectRangeTreeOwnership[K rangeKey[K], S committedPageSource](
	source S,
	meta Meta,
	index *pageNumberIndex,
	scratch *rangeTreeOwnershipScratch,
	maxWork uint64,
) (uint64, error) {
	var key K
	if key.family() != meta.AddressFamily {
		return 0, &rangeOwnershipError{code: rangeOwnershipErrWrongKeyFamily}
	}
	if index == nil || scratch == nil {
		return 0, &rangeOwnershipError{code: rangeOwnershipErrScratch}
	}
	if meta.RangeRoot == 0 {
		if meta.RangeRecordCount != 0 {
			return 0, &rangeOwnershipError{code: rangeOwnershipErrRootRecordCount}
		}
		return 0, nil
	}
	if meta.RangeRoot < 2 || uint64(meta.RangeRoot) >= meta.PageCount {
		return 0, &rangeOwnershipError{code: rangeOwnershipErrRootBounds, page: meta.RangeRoot}
	}
	if maxWork == 0 {
		return 0, &rangeOwnershipError{code: rangeOwnershipErrWorkBudget, required: 1}
	}
	if status := source.checkAccessStatus(); status.failed() {
		return 0, &rangeOwnershipError{code: rangeOwnershipErrSource, cause: status.asError()}
	}

	scratch.reset()
	scratch.frames[0] = rangeOwnershipFrame{page: meta.RangeRoot, expectedLevel: rangeOwnershipRootLevel}
	depth, work := 0, uint64(0)
	for {
		frame := &scratch.frames[depth]
		if !frame.loaded {
			if work >= maxWork {
				return work, &rangeOwnershipError{
					code: rangeOwnershipErrWorkBudget, required: rangeOwnershipNextWork(work), actual: maxWork,
				}
			}
			if status := source.readPageStatus(frame.page, &scratch.pages[depth]); status.failed() {
				return work, &rangeOwnershipError{code: rangeOwnershipErrSource, cause: status.asError(), page: frame.page}
			}
			header, err := DecodePageHeader(scratch.pages[depth][:], meta.TxnID)
			if err != nil {
				return work, &rangeOwnershipError{code: rangeOwnershipErrPage, cause: err, page: frame.page}
			}

			switch header.PageType {
			case PageTypeRangeLeaf:
				if frame.expectedLevel != rangeOwnershipRootLevel && frame.expectedLevel != 0 {
					return work, &rangeOwnershipError{code: rangeOwnershipErrChildType, page: frame.page, pageType: header.PageType}
				}
				if _, err = inspectRangeLeaf[K](scratch.pages[depth][:], meta.TxnID, meta.AddressFamily, meta.ValueKind); err != nil {
					return work, &rangeOwnershipError{code: rangeOwnershipErrPage, cause: err, page: frame.page}
				}
				if _, err = index.insert(frame.page); err != nil {
					return work, &rangeOwnershipError{code: rangeOwnershipErrIndex, cause: err, page: frame.page}
				}
				work++
				if depth == 0 {
					return work, nil
				}
				depth--
				continue

			case PageTypeRangeBranch:
				branch, branchErr := openRangeBranch[K](scratch.pages[depth][:], meta.TxnID, meta.AddressFamily, meta.PageCount)
				if branchErr != nil {
					return work, &rangeOwnershipError{code: rangeOwnershipErrPage, cause: branchErr, page: frame.page}
				}
				if frame.expectedLevel == rangeOwnershipRootLevel {
					frame.expectedLevel = branch.level
				} else if frame.expectedLevel != branch.level {
					return work, &rangeOwnershipError{
						code: rangeOwnershipErrChildLevel, page: frame.page,
						expectedLevel: frame.expectedLevel, actualLevel: branch.level,
					}
				}
				if _, err = index.insert(frame.page); err != nil {
					return work, &rangeOwnershipError{code: rangeOwnershipErrIndex, cause: err, page: frame.page}
				}
				work++
				frame.loaded = true
				frame.nextChild = 0

			default:
				code := rangeOwnershipErrChildType
				if depth == 0 {
					code = rangeOwnershipErrRootType
				}
				return work, &rangeOwnershipError{code: code, page: frame.page, pageType: header.PageType}
			}
		}

		frame = &scratch.frames[depth]
		branch, err := openRangeBranch[K](scratch.pages[depth][:], meta.TxnID, meta.AddressFamily, meta.PageCount)
		if err != nil {
			return work, &rangeOwnershipError{code: rangeOwnershipErrPage, cause: err, page: frame.page}
		}
		if frame.nextChild == branch.count {
			if depth == 0 {
				return work, nil
			}
			depth--
			continue
		}
		entry, entryErr := branch.entry(frame.nextChild)
		if entryErr != nil {
			return work, &rangeOwnershipError{code: rangeOwnershipErrPage, cause: entryErr, page: frame.page}
		}
		frame.nextChild++
		if depth+1 == rangeOwnershipPathCapacity {
			return work, &rangeOwnershipError{
				code: rangeOwnershipErrChildLevel, page: entry.childPage,
				expectedLevel: 0, actualLevel: frame.expectedLevel,
			}
		}
		if frame.expectedLevel == 0 {
			return work, &rangeOwnershipError{
				code: rangeOwnershipErrChildLevel, page: entry.childPage,
				expectedLevel: 0, actualLevel: 0,
			}
		}
		depth++
		scratch.frames[depth] = rangeOwnershipFrame{
			page: entry.childPage, expectedLevel: frame.expectedLevel - 1,
		}
	}
}
