package exactv4

import "encoding/binary"

type freeBitmapInsertOrigin uint8

const (
	freeBitmapInsertOriginNone freeBitmapInsertOrigin = iota
	freeBitmapInsertOriginCommitted
	freeBitmapInsertOriginVerified
	freeBitmapInsertOriginPrivate
	freeBitmapInsertOriginNew
)

type freeBitmapInsertPage struct {
	bytes           [PageSize]byte
	base            uint64
	level           uint16
	sourcePage      uint32
	resultPage      uint32
	origin          freeBitmapInsertOrigin
	originSlot      int
	destinationSlot int
	changed         bool
	sourceLeft      int
	sourceRight     int
	sourceHeight    uint8
}

type freeBitmapInsertResult struct {
	inserted              int
	alreadyFree           int
	committedReplacements int
	newBitmapPages        int
	recycledPrivatePages  int
}

type unusedReservationRelease struct {
	reinsertedCandidates int
	reinsertedAppended   int
	truncatedAppended    int
	pendingPageCount     uint64
}

type preparedFreeBitmapInsertion struct {
	cow                      *freeBitmapCOW
	epoch                    uint64
	poolMutationEpoch        uint64
	pages                    []uint32
	scratch                  []freeBitmapInsertPage
	scratchLen               int
	root                     uint32
	governingPageCount       uint64
	destinationCount         int
	demotedSlots             [freeBitmapPathCapacity - 1]int
	demotedLen               int
	autoReleasePages         [freeBitmapPathCapacity - 1]uint32
	autoReleaseLen           int
	autoReinsertedCandidates int
	autoReinsertedAppended   int
	inserted                 int
	alreadyFree              int
	committedReplacements    int
	newBitmapPages           int
	releaseTailFrom          uint64
}

type freeBitmapInsertPreflight struct {
	cow                      *freeBitmapCOW
	pages                    []uint32
	governingPageCount       uint64
	scratch                  []freeBitmapInsertPage
	scratchLen               int
	sourceIndexRoot          int
	root                     uint32
	rootLevel                uint16
	desiredLevel             uint16
	plannedRoot              int
	previousPath             [freeBitmapPathCapacity]int
	destinationCount         int
	newIndexCount            int
	availableCursor          int
	usableAvailable          int
	demotedSlots             [freeBitmapPathCapacity - 1]int
	demotedLen               int
	demotedDestinationSlots  [freeBitmapPathCapacity - 1]int
	demotedDestinationLen    int
	demotedDestinationCursor int
	autoReleasePages         [freeBitmapPathCapacity - 1]uint32
	autoReleaseLen           int
	autoReinsertedCandidates int
	autoReinsertedAppended   int
	inserted                 int
	alreadyFree              int
	committedReplacements    int
	newBitmapPages           int
}

func (c *freeBitmapCOW) insertFree(
	pageNumber uint32,
	scratch []freeBitmapInsertPage,
) (freeBitmapInsertResult, freeBitmapCOWError) {
	if problem := c.checkBitmapAccess(); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	if _, problem := c.mutationEpochAfter(1); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	c.singleInsertPage[0] = pageNumber
	return c.insertFreePages(c.singleInsertPage[:], scratch)
}

func (c *freeBitmapCOW) insertFreePages(
	pages []uint32,
	scratch []freeBitmapInsertPage,
) (freeBitmapInsertResult, freeBitmapCOWError) {
	return c.insertFreePagesForPageCount(pages, c.pageCount, scratch)
}

// insertFreePagesForPageCount keeps preflight and application on one receiver
// and one call stack. A stale or cross-draft insertion plan cannot escape.
func (c *freeBitmapCOW) insertFreePagesForPageCount(
	pages []uint32,
	governingPageCount uint64,
	scratch []freeBitmapInsertPage,
) (freeBitmapInsertResult, freeBitmapCOWError) {
	if problem := c.checkBitmapAccess(); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	if _, problem := c.mutationEpochAfter(1); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	preflight, problem := newFreeBitmapInsertPreflight(c, pages, governingPageCount, scratch)
	if problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	prepared, problem := preflight.plan()
	if problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	if problem = c.checkBitmapAccess(); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	return c.applyPreparedFreeBitmapInsertion(prepared)
}

func (c *freeBitmapCOW) releaseUnusedReservations(
	releasePages []uint32,
	scratch []freeBitmapInsertPage,
) (unusedReservationRelease, freeBitmapCOWError) {
	if problem := c.checkBitmapAccess(); problem.failed() {
		return unusedReservationRelease{}, problem
	}
	if c.scoped {
		if problem := c.validateScopedBindings(); problem.failed() {
			return unusedReservationRelease{}, problem
		}
	}
	if _, problem := c.mutationEpochAfter(1); problem.failed() {
		return unusedReservationRelease{}, problem
	}
	oldPageCount := c.pageCount
	newPageCount := oldPageCount
	for newPageCount > c.committedPageCount {
		pageNumber64 := newPageCount - 1
		if pageNumber64 > uint64(^uint32(0)) {
			return unusedReservationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		indexed, found := c.indexedPage(uint32(pageNumber64))
		if !found || indexed.kind != indexedBitmapPageArena {
			break
		}
		page, infoProblem := c.bitmapSlotInfo(indexed.slot)
		if infoProblem.failed() {
			return unusedReservationRelease{}, infoProblem
		}
		if page.authorization != privateBitmapPageAppended || page.state != privateBitmapPageAvailable {
			break
		}
		newPageCount--
	}

	releaseLen := 0
	candidateCount := 0
	appendedCount := 0
	scanLen := c.pagePool().capacity()
	if c.scoped {
		scanLen = c.scopeCapacity
	}
	for bindingIndex := 0; bindingIndex < scanLen; bindingIndex++ {
		slotIndex := bindingIndex
		if c.scoped {
			slotIndex = c.arenaBindings[bindingIndex].poolSlot
		}
		page, infoProblem := c.bitmapSlotInfo(slotIndex)
		if infoProblem.failed() {
			return unusedReservationRelease{}, infoProblem
		}
		if page.state != privateBitmapPageAvailable {
			continue
		}
		release := false
		switch page.authorization {
		case privateBitmapPageCommittedFreeCandidate:
			candidateCount++
			release = true
		case privateBitmapPageAppended:
			if uint64(page.pageNumber) < newPageCount {
				appendedCount++
				release = true
			}
		}
		if !release {
			continue
		}
		nextReleaseLen, ok := checkedIntAdd(releaseLen, 1)
		if !ok {
			return unusedReservationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if releaseLen == len(releasePages) {
			return unusedReservationRelease{}, freeBitmapCOWError{
				code:     freeBitmapCOWErrInsufficientResourceBudget,
				resource: freeBitmapResourceCandidatePages,
				required: nextReleaseLen, actual: len(releasePages),
			}
		}
		if releaseLen != 0 && page.pageNumber <= releasePages[releaseLen-1] {
			return unusedReservationRelease{}, freeBitmapCOWError{
				code:         freeBitmapCOWErrInsertPageOrderRegression,
				previousPage: releasePages[releaseLen-1], page: page.pageNumber,
			}
		}
		releasePages[releaseLen] = page.pageNumber
		releaseLen = nextReleaseLen
	}

	preflight, problem := newFreeBitmapInsertPreflight(
		c, releasePages[:releaseLen], newPageCount, scratch,
	)
	if problem.failed() {
		return unusedReservationRelease{}, problem
	}
	prepared, problem := preflight.plan()
	if problem.failed() {
		return unusedReservationRelease{}, problem
	}
	nextCandidateCount, ok := checkedIntAdd(candidateCount, prepared.autoReinsertedCandidates)
	if !ok {
		return unusedReservationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	candidateCount = nextCandidateCount
	nextAppendedCount, ok := checkedIntAdd(appendedCount, prepared.autoReinsertedAppended)
	if !ok {
		return unusedReservationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	appendedCount = nextAppendedCount
	difference := oldPageCount - newPageCount
	if difference > uint64(^uint(0)>>1) {
		return unusedReservationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	truncated := int(difference)
	if problem = c.checkBitmapAccess(); problem.failed() {
		return unusedReservationRelease{}, problem
	}
	prepared.releaseTailFrom = newPageCount
	if _, problem = c.applyPreparedFreeBitmapInsertion(prepared); problem.failed() {
		return unusedReservationRelease{}, problem
	}
	return unusedReservationRelease{
		reinsertedCandidates: candidateCount,
		reinsertedAppended:   appendedCount,
		truncatedAppended:    truncated,
		pendingPageCount:     newPageCount,
	}, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) checkBitmapAccess() freeBitmapCOWError {
	if c.committed != nil {
		if status := c.committed.checkAccessStatus(); status.failed() {
			return freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) transferBitmapPageToRetirement(
	checkpoint privatePagePoolCheckpoint,
	pageNumber uint32,
	origin privatePageOrigin,
) (privatePageToken, freeBitmapCOWError) {
	if problem := c.checkBitmapAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	var token privatePageToken
	var poolProblem privatePagePoolError
	if c.scoped {
		token, poolProblem = c.pagePool().borrowExactInScope(c.scope, pageNumber, privatePageOwnerBitmap, privatePageBitmap)
	} else {
		token, poolProblem = c.pagePool().borrowExact(pageNumber, privatePageOwnerBitmap, privatePageBitmap)
	}
	if poolProblem.failed() {
		return privatePageToken{}, bitmapPoolError(poolProblem)
	}
	var transferred privatePageToken
	if c.scoped {
		transferred, poolProblem = c.pagePool().transferInScope(checkpoint, c.scope, token, privatePageOwnerRetirement, origin)
	} else {
		transferred, poolProblem = c.pagePool().transfer(checkpoint, token, privatePageOwnerRetirement, origin)
	}
	if poolProblem.failed() {
		return privatePageToken{}, bitmapPoolError(poolProblem)
	}
	return transferred, freeBitmapCOWError{}
}

func newFreeBitmapInsertPreflight(
	cow *freeBitmapCOW,
	pages []uint32,
	governingPageCount uint64,
	scratch []freeBitmapInsertPage,
) (freeBitmapInsertPreflight, freeBitmapCOWError) {
	if governingPageCount < cow.committedPageCount || governingPageCount > cow.pageCount {
		return freeBitmapInsertPreflight{}, freeBitmapCOWError{
			code: freeBitmapCOWErrPageCountOutOfRange, pageCount: governingPageCount,
		}
	}
	var previous uint32
	for index, pageNumber := range pages {
		if pageNumber < 2 || uint64(pageNumber) >= governingPageCount {
			return freeBitmapInsertPreflight{}, freeBitmapCOWError{code: freeBitmapCOWErrInsertPageOutOfBounds, page: pageNumber}
		}
		if index != 0 && pageNumber <= previous {
			return freeBitmapInsertPreflight{}, freeBitmapCOWError{
				code: freeBitmapCOWErrInsertPageOrderRegression, previousPage: previous, page: pageNumber,
			}
		}
		previous = pageNumber
		if indexed, found := cow.indexedPage(pageNumber); found {
			if indexed.kind == indexedBitmapPageArena {
				info, infoProblem := cow.bitmapSlotInfo(indexed.slot)
				if infoProblem.failed() {
					return freeBitmapInsertPreflight{}, infoProblem
				}
				if info.state == privateBitmapPageInUse {
					return freeBitmapInsertPreflight{}, freeBitmapCOWError{code: freeBitmapCOWErrInsertPageInUse, page: pageNumber}
				}
			}
			if indexed.kind == indexedBitmapPageVerified {
				return freeBitmapInsertPreflight{}, freeBitmapCOWError{code: freeBitmapCOWErrInsertPageIsBitmapPath, page: pageNumber}
			}
		}
	}
	desiredLevel, ok := minimumFreeBitmapLevel(governingPageCount)
	if !ok {
		return freeBitmapInsertPreflight{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	p := freeBitmapInsertPreflight{
		cow: cow, pages: pages, governingPageCount: governingPageCount, scratch: scratch,
		sourceIndexRoot: bitmapCOWNoIndex, root: cow.root,
		rootLevel: desiredLevel, desiredLevel: desiredLevel,
		plannedRoot: bitmapCOWNoIndex, availableCursor: cow.availableLen,
	}
	for index := range p.previousPath {
		p.previousPath[index] = bitmapCOWNoIndex
	}
	for index := range p.demotedSlots {
		p.demotedSlots[index] = bitmapCOWNoIndex
		p.demotedDestinationSlots[index] = bitmapCOWNoIndex
	}
	return p, freeBitmapCOWError{}
}

func (p freeBitmapInsertPreflight) plan() (preparedFreeBitmapInsertion, freeBitmapCOWError) {
	usable, problem := p.countUsableAvailable()
	if problem.failed() {
		return preparedFreeBitmapInsertion{}, problem
	}
	p.usableAvailable = usable
	if problem := p.prepareRoot(); problem.failed() {
		return preparedFreeBitmapInsertion{}, problem
	}
	if _, ok := checkedIntAdd(len(p.pages), p.autoReleaseLen); !ok {
		return preparedFreeBitmapInsertion{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	requested := 0
	automatic := 0
	for requested < len(p.pages) || automatic < p.autoReleaseLen {
		var pageNumber uint32
		if automatic == p.autoReleaseLen ||
			(requested < len(p.pages) && p.pages[requested] < p.autoReleasePages[automatic]) {
			pageNumber = p.pages[requested]
			requested++
		} else {
			pageNumber = p.autoReleasePages[automatic]
			automatic++
		}
		if problem := p.planOne(pageNumber); problem.failed() {
			return preparedFreeBitmapInsertion{}, problem
		}
	}
	return preparedFreeBitmapInsertion{
		cow: p.cow, epoch: p.cow.mutationEpoch, poolMutationEpoch: p.cow.poolMutationEpoch(),
		pages: p.pages, scratch: p.scratch, scratchLen: p.scratchLen,
		root: p.root, governingPageCount: p.governingPageCount,
		destinationCount: p.destinationCount,
		demotedSlots:     p.demotedSlots, demotedLen: p.demotedLen,
		autoReleasePages: p.autoReleasePages, autoReleaseLen: p.autoReleaseLen,
		autoReinsertedCandidates: p.autoReinsertedCandidates,
		autoReinsertedAppended:   p.autoReinsertedAppended,
		inserted:                 p.inserted, alreadyFree: p.alreadyFree,
		committedReplacements: p.committedReplacements,
		newBitmapPages:        p.newBitmapPages,
	}, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) prepareRoot() freeBitmapCOWError {
	if p.root == 0 {
		p.rootLevel = p.desiredLevel
		return freeBitmapCOWError{}
	}
	page := &p.cow.snapshots[0]
	origin, originSlot, selectedTxn, problem := p.copySource(p.root, page)
	if problem.failed() {
		return problem
	}
	header, headerProblem := decodePageHeaderNoAlloc(page[:], selectedTxn)
	if headerProblem.code != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: p.root, pageProblem: bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}}
	}
	level, problem := insertPageLevel(header, p.root)
	if problem.failed() {
		return problem
	}
	committedLevel, ok := minimumFreeBitmapLevel(p.cow.committedPageCount)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	pendingLevel, ok := minimumFreeBitmapLevel(p.cow.pageCount)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	validLevel := false
	switch origin {
	case freeBitmapInsertOriginCommitted, freeBitmapInsertOriginVerified:
		validLevel = level == committedLevel
	case freeBitmapInsertOriginPrivate:
		validLevel = level >= committedLevel && level <= pendingLevel
	}
	if !validLevel {
		return freeBitmapCOWError{code: freeBitmapCOWErrRootLevel, page: p.root, expectedLevel: committedLevel, actualLevel: level}
	}

	for level > p.desiredLevel {
		if origin != freeBitmapInsertOriginPrivate {
			return freeBitmapCOWError{code: freeBitmapCOWErrNonCanonicalRootDemotion}
		}
		branch, pageProblem := openBitmapBranchNoAlloc(page[:], selectedTxn, bitmapKindFreePages)
		if pageProblem.code != 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: p.root, pageProblem: pageProblem}
		}
		if problem = p.verifyBranch(branch, p.root, origin, 0, level); problem.failed() {
			return problem
		}
		if header.ItemCount != 1 || branch.child(0) == 0 || !branch.summaryBit(0) {
			return freeBitmapCOWError{code: freeBitmapCOWErrNonCanonicalRootDemotion}
		}
		for index := 1; uint64(index) < BitmapFanout; index++ {
			if branch.child(index) != 0 || branch.summaryBit(index) {
				return freeBitmapCOWError{code: freeBitmapCOWErrNonCanonicalRootDemotion}
			}
		}
		p.demotedSlots[p.demotedLen] = originSlot
		p.demotedLen++
		p.root = branch.child(0)
		level--
		origin, originSlot, selectedTxn, problem = p.copySource(p.root, page)
		if problem.failed() {
			return problem
		}
		header, headerProblem = decodePageHeaderNoAlloc(page[:], selectedTxn)
		if headerProblem.code != 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: p.root, pageProblem: bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}}
		}
		actual, problem := insertPageLevel(header, p.root)
		if problem.failed() {
			return problem
		}
		if actual != level {
			return freeBitmapCOWError{code: freeBitmapCOWErrChildLevel, page: p.root, expectedLevel: level, actualLevel: actual}
		}
	}
	p.rootLevel = level
	if problem := p.classifyDemotedSlots(); problem.failed() {
		return problem
	}
	usableAvailable, ok := checkedIntAdd(p.usableAvailable, p.demotedDestinationLen)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	p.usableAvailable = usableAvailable
	retainedRoot, problem := p.appendRetainedSource(p.root, 0, level, page, origin, originSlot, selectedTxn)
	if problem.failed() {
		return problem
	}
	retainedPosition := int(p.desiredLevel - level)
	p.previousPath[retainedPosition] = retainedRoot
	if level == p.desiredLevel {
		p.plannedRoot = retainedRoot
	}
	if level < p.desiredLevel {
		child := p.root
		for promotionLevel := level + 1; promotionLevel <= p.desiredLevel; promotionLevel++ {
			node, problem := p.appendNew(0, promotionLevel)
			if problem.failed() {
				return problem
			}
			if problem = p.ensureChanged(node); problem.failed() {
				return problem
			}
			mutateFreeBitmapBranchChild(&p.scratch[node].bytes, p.cow.pendingTxn, promotionLevel, 0, child)
			child = p.scratch[node].resultPage
			position := int(p.desiredLevel - promotionLevel)
			p.previousPath[position] = node
			p.plannedRoot = node
		}
		p.root = child
		p.rootLevel = p.desiredLevel
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) classifyDemotedSlots() freeBitmapCOWError {
	for index := 0; index < p.demotedLen; index++ {
		slot := p.demotedSlots[index]
		page, problem := p.cow.bitmapSlotInfo(slot)
		if problem.failed() {
			return problem
		}
		autoRelease := false
		switch page.authorization {
		case privateBitmapPageCommittedFreeCandidate:
			p.autoReinsertedCandidates++
			autoRelease = true
		case privateBitmapPageAppended:
			if uint64(page.pageNumber) < p.governingPageCount {
				p.autoReinsertedAppended++
				autoRelease = true
			}
		}
		if autoRelease {
			at := p.autoReleaseLen
			for at != 0 && p.autoReleasePages[at-1] > page.pageNumber {
				p.autoReleasePages[at] = p.autoReleasePages[at-1]
				at--
			}
			p.autoReleasePages[at] = page.pageNumber
			p.autoReleaseLen++
		} else if uint64(page.pageNumber) < p.governingPageCount &&
			page.authorization == privatePageReclaimed {
			p.demotedDestinationSlots[p.demotedDestinationLen] = slot
			p.demotedDestinationLen++
		}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) isReleasePage(pageNumber uint32) bool {
	return sortedU32Contains(p.pages, pageNumber) ||
		sortedU32Contains(p.autoReleasePages[:p.autoReleaseLen], pageNumber)
}

func (p *freeBitmapInsertPreflight) planOne(pageNumber uint32) freeBitmapCOWError {
	path := [freeBitmapPathCapacity]int{bitmapCOWNoIndex, bitmapCOWNoIndex, bitmapCOWNoIndex, bitmapCOWNoIndex}
	for position := 0; position <= int(p.desiredLevel); position++ {
		level := p.desiredLevel - uint16(position)
		span, ok := freeBitmapCoverage(level)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		base := (uint64(pageNumber) / span) * span
		cached := p.previousPath[position]
		var node int
		var problem freeBitmapCOWError
		if cached != bitmapCOWNoIndex && p.scratch[cached].base == base && p.scratch[cached].level == level {
			node = cached
		} else if position == 0 {
			if p.plannedRoot != bitmapCOWNoIndex {
				node = p.plannedRoot
			} else if p.root == 0 {
				node, problem = p.appendNew(base, level)
			} else {
				node, problem = p.appendSource(p.root, base, level)
			}
		} else {
			parent := path[position-1]
			childSpan, ok := freeBitmapCoverage(level)
			if !ok {
				return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			childIndex64 := (base - p.scratch[parent].base) / childSpan
			if childIndex64 >= BitmapFanout {
				return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			childIndex := int(childIndex64)
			child := rawFreeBitmapBranchChild(&p.scratch[parent].bytes, childIndex)
			if child == 0 {
				node, problem = p.appendNew(base, level)
			} else {
				node, problem = p.appendSource(child, base, level)
			}
		}
		if problem.failed() {
			return problem
		}
		path[position] = node
	}
	leafIndex := int(p.desiredLevel)
	leaf := path[leafIndex]
	local := uint64(pageNumber) - p.scratch[leaf].base
	wordIndex64 := local / 64
	if wordIndex64 >= BitmapLeafWords {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	wordIndex := int(wordIndex64)
	mask := uint64(1) << uint(local%64)
	if rawFreeBitmapLeafWord(&p.scratch[leaf].bytes, wordIndex)&mask != 0 {
		p.alreadyFree++
		p.previousPath = path
		return freeBitmapCOWError{}
	}
	if problem := p.ensureChanged(leaf); problem.failed() {
		return problem
	}
	mutateFreeBitmapLeafSet(&p.scratch[leaf].bytes, p.cow.pendingTxn, p.scratch[leaf].base, pageNumber)
	child := p.scratch[leaf].resultPage
	for position := leafIndex - 1; position >= 0; position-- {
		parent := path[position]
		level := p.scratch[parent].level
		childSpan, ok := freeBitmapCoverage(level - 1)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		childIndex64 := (p.scratch[path[position+1]].base - p.scratch[parent].base) / childSpan
		if childIndex64 >= BitmapFanout {
			return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if problem := p.ensureChanged(parent); problem.failed() {
			return problem
		}
		mutateFreeBitmapBranchChild(&p.scratch[parent].bytes, p.cow.pendingTxn, level, int(childIndex64), child)
		child = p.scratch[parent].resultPage
	}
	p.root = child
	p.plannedRoot = path[0]
	p.previousPath = path
	p.inserted++
	return freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) appendNew(base uint64, level uint16) (int, freeBitmapCOWError) {
	index, problem := p.reserveScratch()
	if problem.failed() {
		return 0, problem
	}
	p.scratch[index] = emptyFreeBitmapInsertPage()
	p.scratch[index].base = base
	p.scratch[index].level = level
	p.scratch[index].origin = freeBitmapInsertOriginNew
	return index, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) appendSource(
	pageNumber uint32,
	base uint64,
	expectedLevel uint16,
) (int, freeBitmapCOWError) {
	page := &p.cow.snapshots[0]
	origin, originSlot, selectedTxn, problem := p.copySource(pageNumber, page)
	if problem.failed() {
		return 0, problem
	}
	return p.appendRetainedSource(pageNumber, base, expectedLevel, page, origin, originSlot, selectedTxn)
}

func (p *freeBitmapInsertPreflight) appendRetainedSource(
	pageNumber uint32,
	base uint64,
	expectedLevel uint16,
	page *[PageSize]byte,
	origin freeBitmapInsertOrigin,
	originSlot int,
	selectedTxn uint64,
) (int, freeBitmapCOWError) {
	index, problem := p.reserveScratch()
	if problem.failed() {
		return 0, problem
	}
	if p.isReleasePage(pageNumber) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrInsertPageIsBitmapPath, page: pageNumber}
	}
	if insertSourceFind(p.scratch, p.sourceIndexRoot, pageNumber) != bitmapCOWNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrRepeatedCommittedPage, page: pageNumber}
	}
	if problem = p.verifySource(page, pageNumber, origin, originSlot, selectedTxn, base, expectedLevel); problem.failed() {
		return 0, problem
	}
	p.scratch[index] = freeBitmapInsertPage{
		bytes: *page, base: base, level: expectedLevel,
		sourcePage: pageNumber, resultPage: pageNumber,
		origin: origin, originSlot: originSlot, destinationSlot: bitmapCOWNoIndex,
		sourceLeft: bitmapCOWNoIndex, sourceRight: bitmapCOWNoIndex, sourceHeight: 1,
	}
	p.sourceIndexRoot = insertSourceInsertUnique(p.scratch, p.sourceIndexRoot, index)
	return index, freeBitmapCOWError{}
}

func emptyFreeBitmapInsertPage() freeBitmapInsertPage {
	return freeBitmapInsertPage{
		originSlot: bitmapCOWNoIndex, destinationSlot: bitmapCOWNoIndex,
		sourceLeft: bitmapCOWNoIndex, sourceRight: bitmapCOWNoIndex,
	}
}

func (p *freeBitmapInsertPreflight) reserveScratch() (int, freeBitmapCOWError) {
	nextScratchLen, ok := checkedIntAdd(p.scratchLen, 1)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if p.scratchLen == len(p.scratch) {
		return 0, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsertScratchExhausted,
			required: nextScratchLen, actual: len(p.scratch),
		}
	}
	index := p.scratchLen
	p.scratchLen = nextScratchLen
	return index, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) ensureChanged(index int) freeBitmapCOWError {
	if p.scratch[index].changed {
		return freeBitmapCOWError{}
	}
	origin := p.scratch[index].origin
	switch origin {
	case freeBitmapInsertOriginPrivate:
	case freeBitmapInsertOriginCommitted, freeBitmapInsertOriginVerified:
		required, ok := checkedIntAdd(p.cow.replacementLen, p.committedReplacements)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		required, ok = checkedIntAdd(required, 1)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if required > len(p.cow.replacements) {
			return freeBitmapCOWError{
				code:     freeBitmapCOWErrInsufficientResourceBudget,
				resource: freeBitmapResourceReplacementPages,
				required: required, actual: len(p.cow.replacements),
			}
		}
		if origin == freeBitmapInsertOriginCommitted {
			requiredIndex, ok := checkedIntAdd(p.cow.indexLen, p.newIndexCount)
			if !ok {
				return freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
			}
			requiredIndex, ok = checkedIntAdd(requiredIndex, 1)
			if !ok {
				return freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
			}
			if requiredIndex > len(p.cow.indexNodes) {
				return freeBitmapCOWError{
					code:     freeBitmapCOWErrInsufficientResourceBudget,
					resource: freeBitmapResourceIndexNodes,
					required: requiredIndex, actual: len(p.cow.indexNodes),
				}
			}
			p.newIndexCount++
		}
		slot, problem := p.nextDestinationSlot()
		if problem.failed() {
			return problem
		}
		info, infoProblem := p.cow.bitmapSlotInfo(slot)
		if infoProblem.failed() {
			return infoProblem
		}
		p.scratch[index].destinationSlot = slot
		p.scratch[index].resultPage = info.pageNumber
		p.committedReplacements++
	case freeBitmapInsertOriginNew:
		slot, problem := p.nextDestinationSlot()
		if problem.failed() {
			return problem
		}
		info, infoProblem := p.cow.bitmapSlotInfo(slot)
		if infoProblem.failed() {
			return infoProblem
		}
		p.scratch[index].destinationSlot = slot
		p.scratch[index].resultPage = info.pageNumber
		p.newBitmapPages++
	default:
		panic("unreachable free-bitmap insertion origin")
	}
	p.scratch[index].changed = true
	return freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) nextDestinationSlot() (int, freeBitmapCOWError) {
	required, ok := checkedIntAdd(p.destinationCount, 1)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if required > p.usableAvailable {
		return 0, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsufficientResourceBudget,
			resource: freeBitmapResourceArenaPages,
			required: required, actual: p.usableAvailable,
		}
	}
	var slot int
	if p.demotedDestinationCursor < p.demotedDestinationLen {
		slot = p.demotedDestinationSlots[p.demotedDestinationCursor]
		p.demotedDestinationCursor++
	} else {
		for {
			p.availableCursor--
			slot = p.cow.availableSlots[p.availableCursor]
			page, problem := p.cow.bitmapSlotInfo(slot)
			if problem.failed() {
				return 0, problem
			}
			if page.state == privateBitmapPageAvailable &&
				uint64(page.pageNumber) < p.governingPageCount &&
				!p.isReleasePage(page.pageNumber) {
				break
			}
		}
	}
	p.destinationCount = required
	return slot, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) countUsableAvailable() (int, freeBitmapCOWError) {
	count := 0
	for _, slot := range p.cow.availableSlots[:p.cow.availableLen] {
		page, problem := p.cow.bitmapSlotInfo(slot)
		if problem.failed() {
			return 0, problem
		}
		if page.state == privateBitmapPageAvailable &&
			uint64(page.pageNumber) < p.governingPageCount &&
			!sortedU32Contains(p.pages, page.pageNumber) {
			count++
		}
	}
	return count, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) copySource(
	pageNumber uint32,
	destination *[PageSize]byte,
) (freeBitmapInsertOrigin, int, uint64, freeBitmapCOWError) {
	if pageNumber < 2 || uint64(pageNumber) >= p.cow.pageCount {
		return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
			freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: pageNumber}
	}
	if indexed, found := p.cow.indexedPage(pageNumber); found {
		switch indexed.kind {
		case indexedBitmapPageArena:
			info, infoProblem := p.cow.bitmapSlotInfo(indexed.slot)
			if infoProblem.failed() {
				return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0, infoProblem
			}
			if info.state != privateBitmapPageInUse {
				return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
					freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
			}
			if problem := p.cow.readBitmapSlot(indexed.slot, destination); problem.failed() {
				return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0, problem
			}
			return freeBitmapInsertOriginPrivate, indexed.slot, p.cow.pendingTxn, freeBitmapCOWError{}
		case indexedBitmapPageVerified:
			*destination = p.cow.verifiedPages[indexed.slot].bytes
			return freeBitmapInsertOriginVerified, indexed.slot, p.cow.sourceTxn, freeBitmapCOWError{}
		case indexedBitmapPageReplacement:
			return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
				freeBitmapCOWError{code: freeBitmapCOWErrRepeatedCommittedPage, page: pageNumber}
		case indexedBitmapPagePlannedCandidate:
			return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
				freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
	}
	if uint64(pageNumber) >= p.cow.committedPageCount {
		return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
			freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: pageNumber}
	}
	if p.cow.committed == nil {
		return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
			freeBitmapCOWError{code: freeBitmapCOWErrMissingCommittedPage, page: pageNumber}
	}
	if status := p.cow.committed.readPageStatus(pageNumber, destination); status.failed() {
		return freeBitmapInsertOriginNone, bitmapCOWNoIndex, 0,
			freeBitmapCOWError{code: freeBitmapCOWErrSource, page: pageNumber, source: status}
	}
	return freeBitmapInsertOriginCommitted, bitmapCOWNoIndex, p.cow.sourceTxn, freeBitmapCOWError{}
}

func (p *freeBitmapInsertPreflight) verifySource(
	page *[PageSize]byte,
	pageNumber uint32,
	origin freeBitmapInsertOrigin,
	originSlot int,
	selectedTxn, base uint64,
	expectedLevel uint16,
) freeBitmapCOWError {
	if origin == freeBitmapInsertOriginVerified {
		cached := p.cow.verifiedPages[originSlot]
		if cached.pageNumber != pageNumber || cached.base != base || cached.level != expectedLevel {
			return freeBitmapCOWError{
				code: freeBitmapCOWErrVerifiedPageIdentityMismatch, page: pageNumber,
				expectedBase: base, actualBase: cached.base,
				expectedLevel: expectedLevel, actualLevel: cached.level,
			}
		}
		return freeBitmapCOWError{}
	}
	header, headerProblem := decodePageHeaderNoAlloc(page[:], selectedTxn)
	if headerProblem.code != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}}
	}
	actualLevel, problem := insertPageLevel(header, pageNumber)
	if problem.failed() {
		return problem
	}
	if actualLevel != expectedLevel {
		return freeBitmapCOWError{code: freeBitmapCOWErrChildLevel, page: pageNumber, expectedLevel: expectedLevel, actualLevel: actualLevel}
	}
	if expectedLevel == 0 {
		leaf, pageProblem := openBitmapLeafNoAlloc(page[:], selectedTxn, bitmapKindFreePages)
		if pageProblem.code == 0 {
			pageProblem = verifyBitmapLeafNoAlloc(leaf, bitmapKindFreePages, base, p.governingPageCount)
		}
		if pageProblem.code != 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
		}
		return freeBitmapCOWError{}
	}
	branch, pageProblem := openBitmapBranchNoAlloc(page[:], selectedTxn, bitmapKindFreePages)
	if pageProblem.code != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
	}
	return p.verifyBranch(branch, pageNumber, origin, base, expectedLevel)
}

func (p *freeBitmapInsertPreflight) verifyBranch(
	branch bitmapBranch,
	pageNumber uint32,
	origin freeBitmapInsertOrigin,
	base uint64,
	level uint16,
) freeBitmapCOWError {
	childSpan, ok := freeBitmapCoverage(level - 1)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	physicalLimit := p.governingPageCount
	if origin == freeBitmapInsertOriginCommitted || origin == freeBitmapInsertOriginVerified {
		physicalLimit = p.cow.committedPageCount
	}
	pageProblem := verifyBitmapBranchNoAlloc(branch, base, childSpan, p.governingPageCount, physicalLimit)
	if pageProblem.code != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
	}
	for index := 0; uint64(index) < BitmapFanout; index++ {
		if branch.summaryBit(index) != (branch.child(index) != 0) {
			return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch, page: pageNumber}
		}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) preflightPreparedFreeBitmapInsertion(
	prepared preparedFreeBitmapInsertion,
	operation privatePagePoolOperation,
) freeBitmapCOWError {
	pool := c.pagePool()
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() || poolStatus.pendingTxn != c.pendingTxn || prepared.poolMutationEpoch != poolStatus.mutationEpoch {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	var prospective privatePagePoolOperation
	if c.scoped {
		prospective, poolProblem = pool.preflightOperationInScope(c.scope)
	} else {
		prospective, poolProblem = pool.preflightOperation()
	}
	if poolProblem.failed() || prospective != operation {
		return bitmapPoolError(poolProblem)
	}
	steps := uint64(0)
	addSteps := func(additional uint64) bool {
		if additional > ^uint64(0)-steps {
			return false
		}
		steps += additional
		return true
	}
	destinationUses := func(slot int) bool {
		for index := 0; index < prepared.scratchLen; index++ {
			node := &prepared.scratch[index]
			if node.changed && node.origin != freeBitmapInsertOriginPrivate && node.destinationSlot == slot {
				return true
			}
		}
		return false
	}
	autoReleases := func(pageNumber uint32) bool {
		return sortedU32Contains(prepared.autoReleasePages[:prepared.autoReleaseLen], pageNumber)
	}
	demoted := func(slot int) bool {
		for index := 0; index < prepared.demotedLen; index++ {
			if prepared.demotedSlots[index] == slot {
				return true
			}
		}
		return false
	}

	for index := 0; index < prepared.demotedLen; index++ {
		slot := prepared.demotedSlots[index]
		info, problem := c.bitmapSlotInfo(slot)
		if problem.failed() {
			return problem
		}
		advances := uint64(1)
		if destinationUses(slot) {
			advances++
		}
		if autoReleases(info.pageNumber) {
			advances++
		}
		if info.state != privatePageInUse || info.owner != privatePageOwnerBitmap ||
			info.origin != privatePageBitmap || info.pendingTxn != c.pendingTxn ||
			info.epoch > ^uint64(0)-advances {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		if !addSteps(1) {
			return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
		}
	}

	for index := 0; index < prepared.scratchLen; index++ {
		node := &prepared.scratch[index]
		if !node.changed {
			continue
		}
		if node.origin == freeBitmapInsertOriginPrivate {
			info, problem := c.bitmapSlotInfo(node.originSlot)
			if problem.failed() {
				return problem
			}
			if info.state != privatePageInUse || info.owner != privatePageOwnerBitmap ||
				info.origin != privatePageBitmap || info.pendingTxn != c.pendingTxn {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
			if !addSteps(1) {
				return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
			}
			continue
		}
		if node.origin != freeBitmapInsertOriginCommitted &&
			node.origin != freeBitmapInsertOriginVerified &&
			node.origin != freeBitmapInsertOriginNew {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
		}
		info, problem := c.bitmapSlotInfo(node.destinationSlot)
		if problem.failed() {
			return problem
		}
		isDemoted := demoted(node.destinationSlot)
		if (!isDemoted && (info.state != privatePageAvailable || info.owner != privatePageOwnerNone || info.origin != privatePageOriginNone)) ||
			(isDemoted && (info.state != privatePageInUse || info.owner != privatePageOwnerBitmap || info.origin != privatePageBitmap)) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		advances := uint64(1)
		if isDemoted {
			advances++
		}
		if info.epoch > ^uint64(0)-advances || !addSteps(3) {
			return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: info.pageNumber}
		}
	}

	for _, pageNumber := range prepared.pages {
		indexed, found := c.indexedPage(pageNumber)
		if !found || indexed.kind != indexedBitmapPageArena {
			continue
		}
		info, problem := c.bitmapSlotInfo(indexed.slot)
		if problem.failed() {
			return problem
		}
		if info.state != privatePageAvailable || info.owner != privatePageOwnerNone ||
			info.origin != privatePageOriginNone || info.epoch == ^uint64(0) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		if !addSteps(1) {
			return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
		}
	}
	for index := 0; index < prepared.autoReleaseLen; index++ {
		pageNumber := prepared.autoReleasePages[index]
		indexed, found := c.indexedPage(pageNumber)
		if !found || indexed.kind != indexedBitmapPageArena {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		info, problem := c.bitmapSlotInfo(indexed.slot)
		if problem.failed() {
			return problem
		}
		if !demoted(indexed.slot) || info.pendingTxn != c.pendingTxn {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		if !addSteps(1) {
			return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
		}
	}
	if prepared.releaseTailFrom != 0 {
		scanLen := pool.capacity()
		if c.scoped {
			scanLen = c.scopeCapacity
		}
		for bindingIndex := 0; bindingIndex < scanLen; bindingIndex++ {
			slot := bindingIndex
			if c.scoped {
				slot = c.arenaBindings[bindingIndex].poolSlot
			}
			info, problem := c.bitmapSlotInfo(slot)
			if problem.failed() {
				return problem
			}
			if info.authorization != privatePageAppended || uint64(info.pageNumber) < prepared.releaseTailFrom ||
				info.state != privatePageAvailable {
				continue
			}
			if info.owner != privatePageOwnerNone || info.origin != privatePageOriginNone || info.epoch == ^uint64(0) {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
			if !addSteps(1) {
				return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: info.pageNumber}
			}
		}
	}
	if problem := pool.requireMutationSteps(steps); problem.failed() {
		return bitmapPoolError(problem)
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) applyPreparedFreeBitmapInsertion(
	prepared preparedFreeBitmapInsertion,
) (freeBitmapInsertResult, freeBitmapCOWError) {
	if problem := c.checkBitmapAccess(); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	if prepared.cow != c || prepared.epoch != c.mutationEpoch {
		return freeBitmapInsertResult{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	poolStatus, poolProblem := c.pagePool().status()
	if poolProblem.failed() || prepared.poolMutationEpoch != poolStatus.mutationEpoch {
		return freeBitmapInsertResult{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	nextEpoch, problem := c.mutationEpochAfter(1)
	if problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	var operation privatePagePoolOperation
	if c.scoped {
		operation, poolProblem = c.pagePool().preflightOperationInScope(c.scope)
	} else {
		operation, poolProblem = c.pagePool().preflightOperation()
	}
	if poolProblem.failed() {
		return freeBitmapInsertResult{}, bitmapPoolError(poolProblem)
	}
	if problem = c.preflightPreparedFreeBitmapInsertion(prepared, operation); problem.failed() {
		return freeBitmapInsertResult{}, problem
	}
	c.pagePool().beginOperationPrepared(operation)
	for index := 0; index < prepared.demotedLen; index++ {
		slot := prepared.demotedSlots[index]
		if c.scoped {
			c.pagePool().releaseSlotForOperationInScopeTerminalPrepared(operation, slot, privatePageAvailable)
			c.refreshArenaBindingEpoch(slot)
		} else {
			c.pagePool().releaseSlotPrepared(slot, privatePageAvailable)
		}
	}
	for index := 0; index < prepared.scratchLen; index++ {
		node := &prepared.scratch[index]
		if !node.changed {
			continue
		}
		switch node.origin {
		case freeBitmapInsertOriginPrivate:
			if c.scoped {
				c.pagePool().writeSlotForOperationInScopeTerminalPrepared(node.originSlot, &node.bytes)
			} else {
				c.pagePool().writeSlotPrepared(node.originSlot, &node.bytes)
			}
		case freeBitmapInsertOriginCommitted, freeBitmapInsertOriginVerified:
			slot := node.destinationSlot
			c.claimBitmapSlotTerminalPrepared(operation, slot, &node.bytes, node.sourcePage)
			c.replacements[c.replacementLen] = node.sourcePage
			c.replacementLen++
			if node.origin == freeBitmapInsertOriginVerified {
				pageIndexReplace(c.indexNodes, c.indexRoot, node.sourcePage, indexedBitmapPage{kind: indexedBitmapPageReplacement})
			} else {
				pageIndexInsertPrechecked(c.indexNodes, &c.indexRoot, &c.indexLen, node.sourcePage, indexedBitmapPage{kind: indexedBitmapPageReplacement})
			}
		case freeBitmapInsertOriginNew:
			slot := node.destinationSlot
			c.claimBitmapSlotTerminalPrepared(operation, slot, &node.bytes, 0)
		default:
			// Rejected by the aggregate preflight.
		}
	}
	for _, pageNumber := range prepared.pages {
		if indexed, found := c.indexedPage(pageNumber); found && indexed.kind == indexedBitmapPageArena {
			if c.scoped {
				c.pagePool().releaseSlotForOperationInScopeTerminalPrepared(operation, indexed.slot, privatePageReleasedFree)
				c.refreshArenaBindingEpoch(indexed.slot)
			} else {
				c.pagePool().releaseSlotPrepared(indexed.slot, privatePageReleasedFree)
			}
		}
	}
	for index := 0; index < prepared.autoReleaseLen; index++ {
		pageNumber := prepared.autoReleasePages[index]
		if indexed, found := c.indexedPage(pageNumber); found && indexed.kind == indexedBitmapPageArena {
			if c.scoped {
				c.pagePool().releaseSlotForOperationInScopeTerminalPrepared(operation, indexed.slot, privatePageReleasedFree)
				c.refreshArenaBindingEpoch(indexed.slot)
			} else {
				c.pagePool().releaseSlotPrepared(indexed.slot, privatePageReleasedFree)
			}
		}
	}
	if prepared.releaseTailFrom != 0 {
		scanLen := c.pagePool().capacity()
		if c.scoped {
			scanLen = c.scopeCapacity
		}
		for bindingIndex := 0; bindingIndex < scanLen; bindingIndex++ {
			slot := bindingIndex
			if c.scoped {
				slot = c.arenaBindings[bindingIndex].poolSlot
			}
			page := &c.pagePool().slots[slot]
			if page.authorization == privatePageAppended && uint64(page.pageNumber) >= prepared.releaseTailFrom &&
				page.state == privatePageAvailable {
				if c.scoped {
					c.pagePool().releaseSlotForOperationInScopeTerminalPrepared(operation, slot, privatePageReleasedTail)
					c.refreshArenaBindingEpoch(slot)
				} else {
					c.pagePool().releaseSlotPrepared(slot, privatePageReleasedTail)
				}
			}
		}
	}
	c.root = prepared.root
	c.pageCount = prepared.governingPageCount
	c.pageCountsDistinct = true
	c.rebuildAvailableBitmapSlotsPrepared()
	c.pagePool().commitOperationPrepared(operation)
	c.mutationEpoch = nextEpoch
	return freeBitmapInsertResult{
		inserted: prepared.inserted, alreadyFree: prepared.alreadyFree,
		committedReplacements: prepared.committedReplacements,
		newBitmapPages:        prepared.newBitmapPages,
		recycledPrivatePages:  prepared.demotedLen,
	}, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) rebuildAvailableBitmapSlotsPrepared() {
	c.availableLen = 0
	if c.scoped {
		for bindingIndex := c.scopeCapacity - 1; bindingIndex >= 0; bindingIndex-- {
			slot := c.arenaBindings[bindingIndex].poolSlot
			if c.pagePool().slots[slot].state == privateBitmapPageAvailable {
				c.availableSlots[c.availableLen] = slot
				c.availableLen++
			}
		}
		return
	}
	for slot := c.pagePool().capacity() - 1; slot >= 0; slot-- {
		page := &c.pagePool().slots[slot]
		if page.state == privateBitmapPageAvailable {
			c.availableSlots[c.availableLen] = slot
			c.availableLen++
		}
	}
}

func (c *freeBitmapCOW) rebuildAvailableBitmapSlots() freeBitmapCOWError {
	c.availableLen = 0
	for slot := c.pagePool().capacity() - 1; slot >= 0; slot-- {
		info, problem := c.bitmapSlotInfo(slot)
		if problem.failed() {
			return problem
		}
		if info.state == privateBitmapPageAvailable {
			c.availableSlots[c.availableLen] = slot
			c.availableLen++
		}
	}
	return freeBitmapCOWError{}
}

func insertPageLevel(header PageHeader, pageNumber uint32) (uint16, freeBitmapCOWError) {
	switch header.PageType {
	case PageTypeBitmapLeaf:
		return 0, freeBitmapCOWError{}
	case PageTypeBitmapBranch:
		return header.Level, freeBitmapCOWError{}
	default:
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrUnexpectedPageType, page: pageNumber, pageType: header.PageType}
	}
}

func rawFreeBitmapLeafWord(page *[PageSize]byte, index int) uint64 {
	offset := bitmapSummaryOffset + index*8
	return binary.LittleEndian.Uint64(page[offset : offset+8])
}

func rawFreeBitmapBranchChild(page *[PageSize]byte, index int) uint32 {
	offset := bitmapChildrenOffset + index*4
	return binary.LittleEndian.Uint32(page[offset : offset+4])
}

func mutateFreeBitmapLeafSet(
	page *[PageSize]byte,
	pendingTxn, base uint64,
	pageNumber uint32,
) {
	local := uint64(pageNumber) - base
	wordIndex := int(local / 64)
	offset := bitmapSummaryOffset + wordIndex*8
	word := binary.LittleEndian.Uint64(page[offset : offset+8])
	binary.LittleEndian.PutUint64(page[offset:offset+8], word|(uint64(1)<<uint(local%64)))
	itemCount := uint16(0)
	for index := 0; index < BitmapLeafWords; index++ {
		if rawFreeBitmapLeafWord(page, index) != 0 {
			itemCount++
		}
	}
	writeFreeBitmapHeader(page, PageTypeBitmapLeaf, pendingTxn, itemCount, 0, bitmapLeafLower)
}

func sortedU32Contains(values []uint32, value uint32) bool {
	low, high := 0, len(values)
	for low < high {
		middle := low + (high-low)/2
		if values[middle] < value {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low < len(values) && values[low] == value
}

func insertSourceFind(nodes []freeBitmapInsertPage, root int, pageNumber uint32) int {
	for root != bitmapCOWNoIndex {
		if pageNumber < nodes[root].sourcePage {
			root = nodes[root].sourceLeft
		} else if pageNumber > nodes[root].sourcePage {
			root = nodes[root].sourceRight
		} else {
			return root
		}
	}
	return bitmapCOWNoIndex
}

func insertSourceInsertUnique(nodes []freeBitmapInsertPage, root, newIndex int) int {
	if root == bitmapCOWNoIndex {
		return newIndex
	}
	pageNumber := nodes[newIndex].sourcePage
	if pageNumber < nodes[root].sourcePage {
		nodes[root].sourceLeft = insertSourceInsertUnique(nodes, nodes[root].sourceLeft, newIndex)
	} else {
		nodes[root].sourceRight = insertSourceInsertUnique(nodes, nodes[root].sourceRight, newIndex)
	}
	insertSourceUpdateHeight(nodes, root)
	balance := insertSourceBalance(nodes, root)
	if balance > 1 {
		left := nodes[root].sourceLeft
		if pageNumber > nodes[left].sourcePage {
			nodes[root].sourceLeft = insertSourceRotateLeft(nodes, left)
		}
		return insertSourceRotateRight(nodes, root)
	}
	if balance < -1 {
		right := nodes[root].sourceRight
		if pageNumber < nodes[right].sourcePage {
			nodes[root].sourceRight = insertSourceRotateRight(nodes, right)
		}
		return insertSourceRotateLeft(nodes, root)
	}
	return root
}

func insertSourceHeight(nodes []freeBitmapInsertPage, index int) uint8 {
	if index == bitmapCOWNoIndex {
		return 0
	}
	return nodes[index].sourceHeight
}

func insertSourceUpdateHeight(nodes []freeBitmapInsertPage, index int) {
	left := insertSourceHeight(nodes, nodes[index].sourceLeft)
	right := insertSourceHeight(nodes, nodes[index].sourceRight)
	if right > left {
		left = right
	}
	nodes[index].sourceHeight = left + 1
}

func insertSourceBalance(nodes []freeBitmapInsertPage, index int) int16 {
	return int16(insertSourceHeight(nodes, nodes[index].sourceLeft)) -
		int16(insertSourceHeight(nodes, nodes[index].sourceRight))
}

func insertSourceRotateLeft(nodes []freeBitmapInsertPage, root int) int {
	pivot := nodes[root].sourceRight
	middle := nodes[pivot].sourceLeft
	nodes[pivot].sourceLeft = root
	nodes[root].sourceRight = middle
	insertSourceUpdateHeight(nodes, root)
	insertSourceUpdateHeight(nodes, pivot)
	return pivot
}

func insertSourceRotateRight(nodes []freeBitmapInsertPage, root int) int {
	pivot := nodes[root].sourceLeft
	middle := nodes[pivot].sourceRight
	nodes[pivot].sourceRight = root
	nodes[root].sourceLeft = middle
	insertSourceUpdateHeight(nodes, root)
	insertSourceUpdateHeight(nodes, pivot)
	return pivot
}
