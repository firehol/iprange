package exactv4

import (
	"encoding/binary"
	"math/bits"
)

// This layer reserves physical pages and applies COW free-bitmap mutations using
// caller-owned storage. It never proves reclamation safety or publishes meta.
const freeBitmapPathCapacity = 4

// indexNodes and availableSlots are expendable constructor scratch. A failed
// preparation resets both complete buffers to their zero value so the same
// ledger can be corrected and retried without caller-side cleanup.
type freeBitmapCOWLedger struct {
	arena          []reservedBitmapPage
	replacements   []uint32
	replacementLen int
	candidates     []uint32
	candidateLen   int
	indexNodes     []bitmapCOWIndexNode
	availableSlots []int

	// The scoped late-binding path keeps physical-slot identity separate from
	// AVL node storage. The reservation planner fills the optional planning
	// fields before any physical page is selected.
	arenaBindings       []bitmapCOWArenaBinding
	verifiedPages       []verifiedBitmapPage
	plannedCandidateLen int
	reservationPlanned  bool
	payloadPageBudget   int
	plannedPrivatePages int
}

func newFreeBitmapCOWLedger(
	arena []reservedBitmapPage,
	replacements []uint32,
	candidates []uint32,
	indexNodes []bitmapCOWIndexNode,
	availableSlots []int,
) freeBitmapCOWLedger {
	return freeBitmapCOWLedger{
		arena:          arena,
		replacements:   replacements,
		candidates:     candidates,
		indexNodes:     indexNodes,
		availableSlots: availableSlots,
	}
}

type reservedFreePage struct {
	pageNumber uint32
}

type freeBitmapCOWErrorCode uint8

const (
	freeBitmapCOWErrSelectedTransactionZero freeBitmapCOWErrorCode = iota + 1
	freeBitmapCOWErrTransactionExhausted
	freeBitmapCOWErrPageCountOutOfRange
	freeBitmapCOWErrRootPageOutOfBounds
	freeBitmapCOWErrMissingCommittedPage
	freeBitmapCOWErrSource
	freeBitmapCOWErrPage
	freeBitmapCOWErrUnexpectedPageType
	freeBitmapCOWErrRootLevel
	freeBitmapCOWErrChildLevel
	freeBitmapCOWErrCoverageOverflow
	freeBitmapCOWErrSelectedChildMissing
	freeBitmapCOWErrSelectedCoverageOutsideLimit
	freeBitmapCOWErrSummaryMismatch
	freeBitmapCOWErrRepeatedPathPage
	freeBitmapCOWErrRepeatedCommittedPage
	freeBitmapCOWErrLedgerPrefixOutOfBounds
	freeBitmapCOWErrLedgerPageOutOfBounds
	freeBitmapCOWErrDuplicateArenaPage
	freeBitmapCOWErrDuplicateReplacement
	freeBitmapCOWErrDuplicateCandidate
	freeBitmapCOWErrCandidateOrderRegression
	freeBitmapCOWErrLedgerPageConflict
	freeBitmapCOWErrIndexCapacityOverflow
	freeBitmapCOWErrIndexCapacityTooSmall
	freeBitmapCOWErrAvailableSlotCapacityTooSmall
	freeBitmapCOWErrCandidateIsPathPage
	freeBitmapCOWErrCandidateAlreadyReserved
	freeBitmapCOWErrCandidateIsDraftReplacement
	freeBitmapCOWErrCandidateIsArenaPage
	freeBitmapCOWErrArenaPageConflict
	freeBitmapCOWErrCandidateLedgerExhausted
	freeBitmapCOWErrReplacementLedgerExhausted
	freeBitmapCOWErrPrivateArenaExhausted
	freeBitmapCOWErrPlannedCandidateMismatch
	freeBitmapCOWErrPlannedCandidatesRemain
	freeBitmapCOWErrVerifiedPageIdentityMismatch
	freeBitmapCOWErrInsertPageOutOfBounds
	freeBitmapCOWErrInsertPageOrderRegression
	freeBitmapCOWErrInsertPageInUse
	freeBitmapCOWErrInsertPageIsBitmapPath
	freeBitmapCOWErrInsertScratchExhausted
	freeBitmapCOWErrNonCanonicalRootDemotion
	freeBitmapCOWErrInsufficientResourceBudget
	freeBitmapCOWErrPageSpaceExhausted
	freeBitmapCOWErrStaleReservationPredecessor
	freeBitmapCOWErrStaleInsertionPlan
	freeBitmapCOWErrMutationEpochExhausted
)

type freeBitmapReservationResource uint8

const (
	freeBitmapResourceArenaPages freeBitmapReservationResource = iota + 1
	freeBitmapResourceCandidatePages
	freeBitmapResourceVerifiedPages
	freeBitmapResourceIndexNodes
	freeBitmapResourceReplacementPages
	freeBitmapResourceAvailableSlots
	freeBitmapResourceArenaBindings
	freeBitmapResourceSourceNodes
	freeBitmapResourceStagedArenaPages
	freeBitmapResourceReclamationTicket
	freeBitmapResourceFinalizationStage
)

type freeBitmapCOWError struct {
	code          freeBitmapCOWErrorCode
	page          uint32
	previousPage  uint32
	pageCount     uint64
	pageType      PageType
	expectedLevel uint16
	actualLevel   uint16
	expectedBase  uint64
	actualBase    uint64
	required      int
	actual        int
	remaining     int
	resource      freeBitmapReservationResource
	source        pageSourceStatus
	pageProblem   bitmapCOWPageProblem
}

func (e freeBitmapCOWError) Error() string {
	return "exact v4 free bitmap COW failure"
}

func (e freeBitmapCOWError) failed() bool { return e.code != 0 }

func (e freeBitmapCOWError) Unwrap() error { return e.source.err() }

type bitmapCOWPageProblem struct {
	code          bitmapPageErrorCode
	headerProblem bitmapCOWHeaderProblem
	pageType      PageType
	wireKind      uint32
	itemCount     uint16
	childPage     uint32
}

type bitmapCOWHeaderProblem struct {
	code        PageHeaderErrorCode
	length      int
	wireType    uint8
	flags       uint8
	headerSize  uint16
	bornTxn     uint64
	selectedTxn uint64
	pageType    PageType
	level       uint16
	lower       uint16
	upper       uint16
}

type indexedBitmapPageKind uint8

const (
	indexedBitmapPageArena indexedBitmapPageKind = iota + 1
	indexedBitmapPageVerified
	indexedBitmapPagePlannedCandidate
	indexedBitmapPageReplacement
)

type indexedBitmapPage struct {
	kind indexedBitmapPageKind
	slot int
}

const bitmapCOWNoIndex = -1

// bitmapCOWIndexNode is caller-owned AVL storage. Preparation initializes only
// the prefix needed by this transaction.
type bitmapCOWIndexNode struct {
	pageNumber uint32
	page       indexedBitmapPage
	left       int
	right      int
	height     uint8

	// Planned-candidate identity survives a temporary Arena role so an unbind
	// plus resynchronization can restore the exact planned node in place.
	candidatePage   uint32
	candidateIndex  int
	candidateMapped bool
}

type bitmapCOWArenaBinding struct {
	poolSlot    int
	poolEpoch   uint64
	pageNumber  uint32
	storageNode int
	activeNode  int
	bound       bool
}

type freeBitmapPathFrame struct {
	pageNumber   uint32
	committed    bool
	privateSlot  int
	verifiedSlot int
	base         uint64
	level        uint16
	childIndex   int
	childCount   uint16
}

type freeBitmapCOW struct {
	committed                   committedPageSource
	selectedTxn                 uint64
	sourceTxn                   uint64
	pendingTxn                  uint64
	committedPageCount          uint64
	pageCount                   uint64
	pageCountsDistinct          bool
	root                        uint32
	pool                        *privatePagePool
	scope                       privatePageReservationScope
	scoped                      bool
	scopeCapacity               int
	arenaBindings               []bitmapCOWArenaBinding
	replacements                []uint32
	replacementLen              int
	candidates                  []uint32
	candidateLen                int
	indexNodes                  []bitmapCOWIndexNode
	indexRoot                   int
	indexLen                    int
	availableSlots              []int
	availableLen                int
	verifiedPages               []verifiedBitmapPage
	plannedCandidateLen         int
	selectedCandidateLen        int
	candidateSelectionSet       bool
	reservationPlanned          bool
	payloadPageBudget           int
	plannedRequiredPrivatePages int
	mutationEpoch               uint64
	scopedFullValidations       uint64
	scopedMemberVisits          uint64
	singleInsertPage            [1]uint32

	frames     [freeBitmapPathCapacity]freeBitmapPathFrame
	snapshots  [freeBitmapPathCapacity][PageSize]byte
	outputs    [freeBitmapPathCapacity][PageSize]byte
	survives   [freeBitmapPathCapacity]bool
	cloneSlots [freeBitmapPathCapacity]int
	pathLen    int
	candidate  uint32
}

func newFreeBitmapCOWWithPool(
	committed committedPageSource,
	selectedTxn uint64,
	pendingPageCount uint64,
	root uint32,
	pool *privatePagePool,
	ledger freeBitmapCOWLedger,
) (*freeBitmapCOW, freeBitmapCOWError) {
	if selectedTxn == 0 {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrSelectedTransactionZero}
	}
	pendingTxn := selectedTxn + 1
	if pendingTxn == 0 {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrTransactionExhausted}
	}
	if pendingPageCount < 2 || pendingPageCount > MaxPageCount {
		return nil, freeBitmapCOWError{
			code:      freeBitmapCOWErrPageCountOutOfRange,
			pageCount: pendingPageCount,
		}
	}
	if root != 0 && (root < 2 || uint64(root) >= pendingPageCount) {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: root}
	}
	if ledger.replacementLen < 0 || ledger.replacementLen > len(ledger.replacements) ||
		ledger.candidateLen < 0 || ledger.candidateLen > len(ledger.candidates) {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPrefixOutOfBounds}
	}
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() || poolStatus.pendingTxn != pendingTxn || poolStatus.pendingPageCount != pendingPageCount {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	prepared, problem := prepareFreeBitmapCOWLedger(pendingPageCount, pool, ledger)
	if problem.failed() {
		return nil, problem
	}
	return &freeBitmapCOW{
		committed:          committed,
		selectedTxn:        selectedTxn,
		sourceTxn:          selectedTxn,
		pendingTxn:         pendingTxn,
		committedPageCount: poolStatus.committedPageCount,
		pageCount:          pendingPageCount,
		root:               root,
		pageCountsDistinct: poolStatus.committedPageCount != pendingPageCount,
		pool:               pool,
		replacements:       ledger.replacements,
		replacementLen:     ledger.replacementLen,
		candidates:         ledger.candidates,
		candidateLen:       ledger.candidateLen,
		indexNodes:         ledger.indexNodes,
		indexRoot:          prepared.indexRoot,
		indexLen:           prepared.indexLen,
		availableSlots:     ledger.availableSlots,
		availableLen:       prepared.availableLen,
	}, freeBitmapCOWError{}
}

func newFreeBitmapCOWWithScopedPool(
	committed committedPageSource,
	selectedTxn uint64,
	pendingPageCount uint64,
	root uint32,
	pool *privatePagePool,
	scope privatePageReservationScope,
	ledger freeBitmapCOWLedger,
) (*freeBitmapCOW, freeBitmapCOWError) {
	cow := &freeBitmapCOW{}
	problem := initializeFreeBitmapCOWWithScopedPool(
		cow, committed, selectedTxn, pendingPageCount, root, pool, scope, ledger,
	)
	if problem.failed() {
		return nil, problem
	}
	return cow, freeBitmapCOWError{}
}

func initializeFreeBitmapCOWWithScopedPool(
	destination *freeBitmapCOW,
	committed committedPageSource,
	selectedTxn uint64,
	pendingPageCount uint64,
	root uint32,
	pool *privatePagePool,
	scope privatePageReservationScope,
	ledger freeBitmapCOWLedger,
) freeBitmapCOWError {
	return initializeFreeBitmapCOWWithScopedPoolTransactions(
		destination, committed, selectedTxn, selectedTxn, selectedTxn+1,
		pendingPageCount, root, pool, scope, ledger,
	)
}

func initializeFreeBitmapCOWWithScopedPoolTransactions(
	destination *freeBitmapCOW,
	committed committedPageSource,
	selectedTxn, sourceTxn, pendingTxn uint64,
	pendingPageCount uint64,
	root uint32,
	pool *privatePagePool,
	scope privatePageReservationScope,
	ledger freeBitmapCOWLedger,
) freeBitmapCOWError {
	if destination == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if selectedTxn == 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrSelectedTransactionZero}
	}
	if sourceTxn == 0 || pendingTxn == 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrTransactionExhausted}
	}
	if pendingPageCount < 2 || pendingPageCount > MaxPageCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrPageCountOutOfRange, pageCount: pendingPageCount}
	}
	if root != 0 && (root < 2 || uint64(root) >= pendingPageCount) {
		return freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: root}
	}
	if ledger.replacementLen < 0 || ledger.replacementLen > len(ledger.replacements) ||
		ledger.candidateLen < 0 || ledger.candidateLen > len(ledger.candidates) ||
		ledger.plannedCandidateLen < 0 || ledger.plannedCandidateLen > len(ledger.candidates) ||
		(ledger.reservationPlanned && ledger.candidateLen > ledger.plannedCandidateLen) ||
		(!ledger.reservationPlanned && ledger.plannedCandidateLen != 0) {
		return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPrefixOutOfBounds}
	}
	anchor, poolProblem := pool.validateScope(scope)
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() || poolStatus.pendingTxn != pendingTxn || poolStatus.pendingPageCount != pendingPageCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	prepared, problem := prepareScopedFreeBitmapCOWLedger(pendingPageCount, pool, scope, anchor.scopeCapacity, ledger)
	if problem.failed() {
		return problem
	}
	*destination = freeBitmapCOW{
		committed: committed, selectedTxn: selectedTxn, sourceTxn: sourceTxn, pendingTxn: pendingTxn,
		committedPageCount: poolStatus.committedPageCount, pageCount: pendingPageCount,
		root: root, pageCountsDistinct: poolStatus.committedPageCount != pendingPageCount,
		pool: pool, scope: scope, scoped: true, scopeCapacity: anchor.scopeCapacity,
		arenaBindings: ledger.arenaBindings,
		replacements:  ledger.replacements, replacementLen: ledger.replacementLen,
		candidates: ledger.candidates, candidateLen: ledger.candidateLen,
		indexNodes: ledger.indexNodes, indexRoot: prepared.indexRoot, indexLen: prepared.indexLen,
		availableSlots: ledger.availableSlots, availableLen: prepared.availableLen,
		verifiedPages: ledger.verifiedPages, plannedCandidateLen: ledger.plannedCandidateLen,
		reservationPlanned: ledger.reservationPlanned, payloadPageBudget: ledger.payloadPageBudget,
		plannedRequiredPrivatePages: ledger.plannedPrivatePages,
		scopedMemberVisits:          uint64(prepared.scopeMemberVisits),
	}
	return freeBitmapCOWError{}
}

func bitmapPoolError(problem privatePagePoolError) freeBitmapCOWError {
	switch problem.code {
	case privatePagePoolErrPageOutOfBounds:
		return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: problem.page}
	case privatePagePoolErrPagesNotStrict:
		return freeBitmapCOWError{code: freeBitmapCOWErrDuplicateArenaPage, page: problem.page, previousPage: problem.previousPage}
	case privatePagePoolErrArithmeticOverflow:
		return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: problem.page}
	case privatePagePoolErrBudget, privatePagePoolErrUnavailable:
		return freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted, page: problem.page}
	default:
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: problem.page}
	}
}

func rollbackDisposableBitmapShadow(
	pool *privatePagePool,
	checkpoint privatePagePoolCheckpoint,
	original freeBitmapCOWError,
) freeBitmapCOWError {
	if rollbackProblem := rollbackDisposablePrivatePagePool(pool, checkpoint); rollbackProblem.failed() {
		return bitmapPoolError(rollbackProblem)
	}
	return original
}

func (c *freeBitmapCOW) pagePool() *privatePagePool {
	return c.pool
}

func (c *freeBitmapCOW) poolMutationEpoch() uint64 {
	status, problem := c.pagePool().status()
	if problem.failed() {
		return 0
	}
	return status.mutationEpoch
}

func (c *freeBitmapCOW) selectedCandidateTarget() int {
	if c.candidateSelectionSet {
		return c.selectedCandidateLen
	}
	return c.plannedCandidateLen
}

func (c *freeBitmapCOW) selectPlannedCandidatePrefix(selected int) freeBitmapCOWError {
	if !c.reservationPlanned || selected < c.candidateLen || selected > c.plannedCandidateLen {
		return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPrefixOutOfBounds, required: selected, actual: c.plannedCandidateLen}
	}
	c.selectedCandidateLen = selected
	c.candidateSelectionSet = true
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) arenaMappingNode(slot int) (int, bool) {
	left, right := 0, c.scopeCapacity
	for left < right {
		middle := left + (right-left)/2
		mapped := c.arenaBindings[middle].poolSlot
		if slot < mapped {
			right = middle
		} else if slot > mapped {
			left = middle + 1
		} else {
			return middle, true
		}
	}
	return 0, false
}

func (c *freeBitmapCOW) validateScopedArenaMapping() freeBitmapCOWError {
	if !c.scoped || c.pool == nil || c.scope.pool != c.pool ||
		c.scopeCapacity < 0 || len(c.arenaBindings) < c.scopeCapacity ||
		len(c.indexNodes) < c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	slotIndex, capacity, poolProblem := c.pool.scopeMemberStart(c.scope)
	if poolProblem.failed() || capacity != c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	mapped := 0
	for slotIndex != privatePagePoolNoIndex {
		c.scopedMemberVisits++
		slot := &c.pool.slots[slotIndex]
		if mapped >= c.scopeCapacity {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		binding := &c.arenaBindings[mapped]
		if binding.poolSlot != slotIndex || binding.storageNode != mapped {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber}
		}
		storage := &c.indexNodes[mapped]
		if storage.candidateMapped || storage.candidatePage != 0 || storage.candidateIndex != 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber}
		}
		mapped++
		slotIndex, poolProblem = c.pool.scopeMemberNextInScope(c.scope, slotIndex)
		if poolProblem.failed() {
			return bitmapPoolError(poolProblem)
		}
	}
	if mapped != c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) validatePlannedCandidatePrefix(selected int) freeBitmapCOWError {
	if !c.reservationPlanned {
		if selected != 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPrefixOutOfBounds, required: selected}
		}
		return freeBitmapCOWError{}
	}
	if selected < c.candidateLen || selected > c.plannedCandidateLen || c.plannedCandidateLen > len(c.candidates) {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrLedgerPrefixOutOfBounds, required: selected, actual: c.plannedCandidateLen,
		}
	}
	var previous uint32
	for index := 0; index < c.plannedCandidateLen; index++ {
		pageNumber := c.candidates[index]
		if pageNumber < 2 || uint64(pageNumber) >= c.committedPageCount {
			return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if index != 0 && pageNumber <= previous {
			code := freeBitmapCOWErrCandidateOrderRegression
			if pageNumber == previous {
				code = freeBitmapCOWErrDuplicateCandidate
			}
			return freeBitmapCOWError{code: code, previousPage: previous, page: pageNumber}
		}
		previous = pageNumber
	}
	for index := 0; index < c.plannedCandidateLen; index++ {
		pageNumber := c.candidates[index]
		nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, pageNumber)
		expectedNode := c.scopeCapacity + len(c.replacements) + index
		if !found || expectedNode < 0 || expectedNode >= len(c.indexNodes) || nodeIndex != expectedNode {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		node := c.indexNodes[nodeIndex]
		if !node.candidateMapped || node.candidatePage != pageNumber || node.candidateIndex != index {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		switch node.page.kind {
		case indexedBitmapPagePlannedCandidate:
			if node.page.slot != index {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
			}
		case indexedBitmapPageArena:
		default:
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) selectedCandidateNode(pageNumber uint32, selected int) (int, bool) {
	left, right := 0, selected
	for left < right {
		middle := left + (right-left)/2
		candidate := c.candidates[middle]
		if pageNumber < candidate {
			right = middle
		} else if pageNumber > candidate {
			left = middle + 1
		} else {
			nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, pageNumber)
			if !found {
				return 0, false
			}
			node := c.indexNodes[nodeIndex]
			return nodeIndex, node.candidateMapped && node.candidateIndex == middle && node.candidatePage == pageNumber
		}
	}
	return 0, false
}

func (c *freeBitmapCOW) validateScopedBindings() freeBitmapCOWError {
	c.scopedFullValidations++
	if problem := c.validateScopedArenaMapping(); problem.failed() {
		return problem
	}
	anchor, problem := c.pool.validateScope(c.scope)
	if problem.failed() {
		return bitmapPoolError(problem)
	}
	if anchor.scopeCapacity != c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	selected := c.selectedCandidateTarget()
	if problem := c.validatePlannedCandidatePrefix(selected); problem.failed() {
		return problem
	}
	status, problem := c.pool.status()
	if problem.failed() || status.pendingTxn != c.pendingTxn || status.pendingPageCount != c.pageCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	bound, selectedBound := 0, 0
	for bindingIndex := 0; bindingIndex < c.scopeCapacity; bindingIndex++ {
		binding := &c.arenaBindings[bindingIndex]
		if binding.poolSlot < 0 || binding.poolSlot >= len(c.pool.slots) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		info, poolProblem := c.pool.slotInfo(binding.poolSlot)
		if poolProblem.failed() || info.scopeID != c.scope.id ||
			c.pool.slots[binding.poolSlot].scopeAnchorIndex != c.scope.anchor || info.epoch != binding.poolEpoch ||
			info.bound != binding.bound {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		if !info.bound {
			if binding.pageNumber != 0 || binding.activeNode != bitmapCOWNoIndex {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
			storage := c.indexNodes[binding.storageNode]
			if storage.pageNumber != 0 || storage.page.kind != 0 || storage.height != 0 {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
			continue
		}
		bound++
		if binding.pageNumber != info.pageNumber || binding.activeNode < 0 || binding.activeNode >= len(c.indexNodes) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, info.pageNumber)
		if !found || nodeIndex != binding.activeNode || c.indexNodes[nodeIndex].page.kind != indexedBitmapPageArena ||
			c.indexNodes[nodeIndex].page.slot != binding.poolSlot {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		node := c.indexNodes[nodeIndex]
		if node.candidateMapped {
			expectedNode := c.scopeCapacity + len(c.replacements) + node.candidateIndex
			if node.candidateIndex < 0 || node.candidateIndex >= selected ||
				nodeIndex != expectedNode ||
				node.candidatePage != info.pageNumber || c.candidates[node.candidateIndex] != info.pageNumber ||
				info.authorization != privatePageCommittedFree {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
			selectedBound++
			storage := c.indexNodes[binding.storageNode]
			if storage.pageNumber != 0 || storage.page.kind != 0 || storage.height != 0 {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
		} else if nodeIndex != binding.storageNode {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
	}
	if bound != anchor.scopeBound || selectedBound != selected {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) synchronizeScopedBindings(
	scope privatePageReservationScope,
) freeBitmapCOWError {
	return c.synchronizeScopedBindingsForCandidatePrefix(scope, c.selectedCandidateTarget())
}

func (c *freeBitmapCOW) synchronizeScopedBindingsForCandidatePrefix(
	scope privatePageReservationScope,
	selected int,
) freeBitmapCOWError {
	if !c.scoped || scope != c.scope || c.pool == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if problem := c.validateScopedArenaMapping(); problem.failed() {
		return problem
	}
	if problem := c.validatePlannedCandidatePrefix(selected); problem.failed() {
		return problem
	}
	anchor, poolProblem := c.pool.validateScope(scope)
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	if anchor.scopeCapacity != c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	status, poolProblem := c.pool.status()
	if poolProblem.failed() || status.pendingTxn != c.pendingTxn || status.pendingPageCount < status.committedPageCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if len(c.arenaBindings) < c.scopeCapacity {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	bound, available, selectedBound := 0, 0, 0
	for bindingIndex := 0; bindingIndex < c.scopeCapacity; bindingIndex++ {
		binding := &c.arenaBindings[bindingIndex]
		if binding.poolSlot < 0 || binding.poolSlot >= len(c.pool.slots) ||
			binding.storageNode < 0 || binding.storageNode >= len(c.indexNodes) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		info, problem := c.pool.slotInfo(binding.poolSlot)
		if problem.failed() || info.scopeID != scope.id || c.pool.slots[binding.poolSlot].scopeAnchorIndex != scope.anchor {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		if binding.bound {
			if binding.activeNode < 0 || binding.activeNode >= len(c.indexNodes) {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
			}
			nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, binding.pageNumber)
			if !found || nodeIndex != binding.activeNode || c.indexNodes[nodeIndex].page.kind != indexedBitmapPageArena ||
				c.indexNodes[nodeIndex].page.slot != binding.poolSlot {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
			}
			node := c.indexNodes[nodeIndex]
			if node.candidateMapped {
				expectedNode := c.scopeCapacity + len(c.replacements) + node.candidateIndex
				if node.candidateIndex < 0 || node.candidateIndex >= c.plannedCandidateLen ||
					nodeIndex != expectedNode || node.candidatePage != binding.pageNumber {
					return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
				}
				storage := c.indexNodes[binding.storageNode]
				if storage.pageNumber != 0 || storage.page.kind != 0 || storage.height != 0 {
					return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
				}
			} else if nodeIndex != binding.storageNode {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
			}
		} else if binding.pageNumber != 0 || binding.activeNode != bitmapCOWNoIndex {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		} else {
			storage := c.indexNodes[binding.storageNode]
			if storage.pageNumber != 0 || storage.page.kind != 0 || storage.height != 0 {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
		}
		if !info.bound {
			continue
		}
		bound++
		if info.pageNumber < 2 || uint64(info.pageNumber) >= status.pendingPageCount {
			return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: info.pageNumber}
		}
		if info.state == privatePageAvailable {
			available++
		}
		candidateNode, selectedCandidate := c.selectedCandidateNode(info.pageNumber, selected)
		if selectedCandidate {
			if info.authorization != privatePageCommittedFree {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
			nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, info.pageNumber)
			if !found || nodeIndex != candidateNode ||
				(c.indexNodes[nodeIndex].page.kind == indexedBitmapPageArena &&
					(!binding.bound || binding.activeNode != nodeIndex || binding.pageNumber != info.pageNumber)) {
				return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: info.pageNumber}
			}
			selectedBound++
			continue
		}
		if _, candidate := c.selectedCandidateNode(info.pageNumber, c.plannedCandidateLen); candidate {
			return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: info.pageNumber}
		}
		if nodeIndex, found := pageIndexFindNode(c.indexNodes, c.indexRoot, info.pageNumber); found &&
			(!binding.bound || binding.pageNumber != info.pageNumber || binding.activeNode != nodeIndex) {
			return freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: info.pageNumber}
		}
	}
	if bound != anchor.scopeBound || selectedBound != selected || available > len(c.availableSlots) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}

	for bindingIndex := 0; bindingIndex < c.scopeCapacity; bindingIndex++ {
		binding := &c.arenaBindings[bindingIndex]
		info, _ := c.pool.slotInfo(binding.poolSlot)
		if binding.bound && (!info.bound || binding.pageNumber != info.pageNumber) {
			activeNode := binding.activeNode
			c.indexRoot, _ = pageIndexDelete(c.indexNodes, c.indexRoot, binding.pageNumber)
			if c.indexNodes[activeNode].candidateMapped {
				restorePlannedCandidateNode(c.indexNodes, &c.indexRoot, activeNode)
			} else {
				clearBitmapCOWActiveNode(&c.indexNodes[activeNode])
			}
			binding.pageNumber = 0
			binding.activeNode = bitmapCOWNoIndex
			binding.bound = false
		}
	}
	c.availableLen = 0
	for bindingIndex := 0; bindingIndex < c.scopeCapacity; bindingIndex++ {
		binding := &c.arenaBindings[bindingIndex]
		info, _ := c.pool.slotInfo(binding.poolSlot)
		if info.bound && !binding.bound {
			activeNode := binding.storageNode
			if candidateNode, selectedCandidate := c.selectedCandidateNode(info.pageNumber, selected); selectedCandidate {
				activeNode = candidateNode
				pageIndexReplace(
					c.indexNodes, c.indexRoot, info.pageNumber,
					indexedBitmapPage{kind: indexedBitmapPageArena, slot: binding.poolSlot},
				)
			} else {
				pageIndexInsertExistingPrechecked(
					c.indexNodes, &c.indexRoot, activeNode, info.pageNumber,
					indexedBitmapPage{kind: indexedBitmapPageArena, slot: binding.poolSlot},
				)
			}
			binding.pageNumber = info.pageNumber
			binding.activeNode = activeNode
			binding.bound = true
		}
		binding.poolEpoch = info.epoch
	}
	for bindingIndex := c.scopeCapacity - 1; bindingIndex >= 0; bindingIndex-- {
		binding := &c.arenaBindings[bindingIndex]
		info, _ := c.pool.slotInfo(binding.poolSlot)
		if info.bound && info.state == privatePageAvailable {
			c.availableSlots[c.availableLen] = binding.poolSlot
			c.availableLen++
		}
	}
	c.pageCount = status.pendingPageCount
	c.pageCountsDistinct = c.committedPageCount != c.pageCount
	c.selectedCandidateLen = selected
	c.candidateSelectionSet = true
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) bitmapSlotInfo(slot int) (privatePageSlotInfo, freeBitmapCOWError) {
	info, problem := c.pagePool().slotInfo(slot)
	if problem.failed() {
		return privatePageSlotInfo{}, bitmapPoolError(problem)
	}
	if c.scoped {
		bindingIndex, found := c.arenaMappingNode(slot)
		if !found {
			return privatePageSlotInfo{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		binding := &c.arenaBindings[bindingIndex]
		if !info.bound || !binding.bound || info.scopeID != c.scope.id ||
			c.pagePool().slots[slot].scopeAnchorIndex != c.scope.anchor || info.epoch != binding.poolEpoch ||
			info.pageNumber != binding.pageNumber {
			return privatePageSlotInfo{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
	}
	return info, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) refreshArenaBindingEpoch(slot int) {
	if !c.scoped {
		return
	}
	if bindingIndex, found := c.arenaMappingNode(slot); found {
		c.arenaBindings[bindingIndex].poolEpoch = c.pagePool().slots[slot].epoch
	}
}

func (c *freeBitmapCOW) bitmapToken(slot int) (privatePageToken, freeBitmapCOWError) {
	info, infoProblem := c.bitmapSlotInfo(slot)
	if infoProblem.failed() {
		return privatePageToken{}, infoProblem
	}
	var token privatePageToken
	var problem privatePagePoolError
	if c.scoped {
		token, problem = c.pagePool().borrowExactInScope(c.scope, info.pageNumber, privatePageOwnerBitmap, privatePageBitmap)
	} else {
		token, problem = c.pagePool().borrowExact(info.pageNumber, privatePageOwnerBitmap, privatePageBitmap)
	}
	if problem.failed() {
		return privatePageToken{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
	}
	return token, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) readBitmapSlot(slot int, destination *[PageSize]byte) freeBitmapCOWError {
	token, problem := c.bitmapToken(slot)
	if problem.failed() {
		return problem
	}
	var poolProblem privatePagePoolError
	if c.scoped {
		poolProblem = c.pagePool().readPageInScope(c.scope, token, destination)
	} else {
		poolProblem = c.pagePool().readPage(token, destination)
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) writeBitmapSlot(slot int, source *[PageSize]byte) freeBitmapCOWError {
	token, problem := c.bitmapToken(slot)
	if problem.failed() {
		return problem
	}
	var poolProblem privatePagePoolError
	if c.scoped {
		poolProblem = c.pagePool().writePageInScope(c.scope, token, source)
	} else {
		poolProblem = c.pagePool().writePage(token, source)
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) claimBitmapSlot(
	operation privatePagePoolOperation,
	slot int,
	source *[PageSize]byte,
	committedOrigin uint32,
) freeBitmapCOWError {
	if source == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	info, problem := c.bitmapSlotInfo(slot)
	if problem.failed() {
		return problem
	}
	var token privatePageToken
	var poolProblem privatePagePoolError
	if c.scoped {
		token, poolProblem = c.pagePool().claimPageForOperationInScope(
			operation, info.pageNumber, privatePageOwnerBitmap, privatePageBitmap,
		)
	} else {
		token, poolProblem = c.pagePool().claimPageForOperation(
			operation, info.pageNumber, privatePageOwnerBitmap, privatePageBitmap,
		)
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	if c.scoped {
		poolProblem = c.pagePool().writePageInScope(c.scope, token, source)
	} else {
		poolProblem = c.pagePool().writePage(token, source)
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	if c.scoped {
		poolProblem = c.pagePool().setCommittedOriginInScope(c.scope, token, committedOrigin)
	} else {
		poolProblem = c.pagePool().setCommittedOrigin(token, committedOrigin)
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	c.refreshArenaBindingEpoch(slot)
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) claimBitmapSlotPrepared(
	operation privatePagePoolOperation,
	slot int,
	source *[PageSize]byte,
	committedOrigin uint32,
) freeBitmapCOWError {
	pool := c.pagePool()
	if c.scoped {
		if problem := pool.claimSlotForOperationInScopePrepared(operation, slot, privatePageOwnerBitmap, privatePageBitmap); problem.failed() {
			return bitmapPoolError(problem)
		}
		if problem := pool.writeSlotForOperationInScopePrepared(operation, slot, source); problem.failed() {
			return bitmapPoolError(problem)
		}
		if problem := pool.setSlotCommittedOriginForOperationInScopePrepared(operation, slot, committedOrigin); problem.failed() {
			return bitmapPoolError(problem)
		}
	} else {
		if problem := pool.claimSlotForOperationPrepared(operation, slot, privatePageOwnerBitmap, privatePageBitmap); problem.failed() {
			return bitmapPoolError(problem)
		}
		pool.writeSlotPrepared(slot, source)
		pool.setSlotCommittedOriginPrepared(slot, committedOrigin)
	}
	c.refreshArenaBindingEpoch(slot)
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) claimBitmapSlotTerminalPrepared(
	operation privatePagePoolOperation,
	slot int,
	source *[PageSize]byte,
	committedOrigin uint32,
) {
	pool := c.pagePool()
	if c.scoped {
		pool.claimSlotForOperationInScopeTerminalPrepared(operation, slot, privatePageOwnerBitmap, privatePageBitmap)
		pool.writeSlotForOperationInScopeTerminalPrepared(slot, source)
		pool.setSlotCommittedOriginForOperationInScopeTerminalPrepared(slot, committedOrigin)
	} else {
		pool.claimSlotForOperationTerminalPrepared(operation, slot, privatePageOwnerBitmap, privatePageBitmap)
		pool.writeSlotPrepared(slot, source)
		pool.setSlotCommittedOriginPrepared(slot, committedOrigin)
	}
	c.refreshArenaBindingEpoch(slot)
}

func (c *freeBitmapCOW) releaseBitmapSlot(slot int, state privatePageState) freeBitmapCOWError {
	page, problem := c.bitmapSlotInfo(slot)
	if problem.failed() {
		return problem
	}
	var poolProblem privatePagePoolError
	if page.state == privatePageInUse {
		token, problem := c.bitmapToken(slot)
		if problem.failed() {
			return problem
		}
		if c.scoped {
			poolProblem = c.pagePool().releaseInScope(c.scope, token, state)
		} else {
			poolProblem = c.pagePool().release(token, state)
		}
	} else if state == privatePageAvailable {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: page.pageNumber}
	} else {
		if c.scoped {
			poolProblem = c.pagePool().returnUnownedInScope(c.scope, page.pageNumber, state)
		} else {
			poolProblem = c.pagePool().returnUnowned(page.pageNumber, state)
		}
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	c.refreshArenaBindingEpoch(slot)
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) replacementPages() []uint32 {
	return c.replacements[:c.replacementLen]
}

func (c *freeBitmapCOW) candidatePages() []uint32 {
	return c.candidates[:c.candidateLen]
}

func (c *freeBitmapCOW) copyPrivatePage(pageNumber uint32, destination *[PageSize]byte) bool {
	page, found := c.indexedPage(pageNumber)
	if !found || page.kind != indexedBitmapPageArena || destination == nil {
		return false
	}
	info, problem := c.bitmapSlotInfo(page.slot)
	if problem.failed() || info.state != privateBitmapPageInUse {
		return false
	}
	return !c.readBitmapSlot(page.slot, destination).failed()
}

func (c *freeBitmapCOW) availablePrivatePages() int {
	return c.availableLen
}

func (c *freeBitmapCOW) committedBitmapLimit() uint64 {
	if c.pageCountsDistinct {
		return c.committedPageCount
	}
	return c.pageCount
}

func (c *freeBitmapCOW) applyPlannedReservation() freeBitmapCOWError {
	if c.committed != nil {
		if status := c.committed.checkAccessStatus(); status.failed() {
			return freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	return c.applyPlannedReservationAfterAccess()
}

func (c *freeBitmapCOW) applyPlannedReservationAfterAccess() freeBitmapCOWError {
	target := c.selectedCandidateTarget()
	if !c.reservationPlanned || c.candidateLen > target {
		return freeBitmapCOWError{code: freeBitmapCOWErrPlannedCandidatesRemain}
	}
	privateCapacity := c.pagePool().capacity()
	if c.scoped {
		privateCapacity = c.scopeCapacity
	}
	if privateCapacity < c.plannedRequiredPrivatePages ||
		len(c.availableSlots) < c.plannedRequiredPrivatePages {
		return freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted}
	}
	if len(c.replacements) < len(c.verifiedPages) {
		return freeBitmapCOWError{code: freeBitmapCOWErrReplacementLedgerExhausted}
	}
	remaining := target - c.candidateLen
	if _, problem := c.mutationEpochAfter(remaining); problem.failed() {
		return problem
	}
	if c.scoped {
		if problem := c.validateScopedBindings(); problem.failed() {
			return problem
		}
	}
	for c.candidateLen < target {
		_, found, problem := c.removeLowestPrevalidatedAfterAccess(c.scoped)
		if problem.failed() {
			return problem
		}
		if !found {
			return freeBitmapCOWError{
				code:      freeBitmapCOWErrPlannedCandidatesRemain,
				remaining: target - c.candidateLen,
			}
		}
	}
	if c.availableLen < c.payloadPageBudget {
		return freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted}
	}
	return freeBitmapCOWError{}
}

// materializePlannedEmptyRoot moves the one legal zero-item source leaf into
// this work unit before finalization. Finalization must never discover a new
// prior-scope replacement after the caller's exact-return pass has completed.
func (c *freeBitmapCOW) materializePlannedEmptyRoot() freeBitmapCOWError {
	if c.root == 0 || len(c.verifiedPages) != 1 {
		return freeBitmapCOWError{}
	}
	verified := &c.verifiedPages[0]
	if verified.pageNumber != c.root || verified.parent != bitmapCOWNoIndex ||
		verified.level != 0 || verified.remaining != 0 || !verified.survives {
		return freeBitmapCOWError{}
	}
	indexed, found := c.indexedPage(c.root)
	if !found || indexed.kind != indexedBitmapPageVerified || indexed.slot != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrVerifiedPageIdentityMismatch, page: c.root}
	}
	leaf, pageProblem := openBitmapLeafNoAlloc(
		verified.bytes[:], c.sourceTxn, bitmapKindFreePages,
	)
	if pageProblem.code != 0 {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrPage, page: c.root, pageProblem: pageProblem,
		}
	}
	if leaf.header.ItemCount != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch, page: c.root}
	}
	if c.replacementLen == len(c.replacements) {
		return freeBitmapCOWError{code: freeBitmapCOWErrReplacementLedgerExhausted, page: c.root}
	}
	if c.availableLen <= c.payloadPageBudget {
		return freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted, page: c.root}
	}
	nextEpoch, problem := c.mutationEpochAfter(1)
	if problem.failed() {
		return problem
	}
	slot := c.availableSlots[c.availableLen-1]
	info, problem := c.bitmapSlotInfo(slot)
	if problem.failed() {
		return problem
	}
	if info.state != privatePageAvailable || info.owner != privatePageOwnerNone ||
		info.origin != privatePageOriginNone || info.epoch == ^uint64(0) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
	}
	var operation privatePagePoolOperation
	var poolProblem privatePagePoolError
	if c.scoped {
		operation, poolProblem = c.pagePool().preflightOperationInScope(c.scope)
	} else {
		operation, poolProblem = c.pagePool().preflightOperation()
	}
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	if poolProblem = c.pagePool().requireMutationSteps(3); poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	var page [PageSize]byte
	writeFreeBitmapHeader(
		&page, PageTypeBitmapLeaf, c.pendingTxn, 0, 0, bitmapLeafLower,
	)
	c.pagePool().beginOperationPrepared(operation)
	if problem = c.claimBitmapSlotPrepared(operation, slot, &page, c.root); problem.failed() {
		abortProblem := c.pagePool().abortOperation(operation)
		if abortProblem.failed() && abortProblem.code != privatePagePoolErrAbortRequired {
			return bitmapPoolError(abortProblem)
		}
		return problem
	}
	c.pagePool().commitOperationPrepared(operation)
	oldRoot := c.root
	c.availableLen--
	c.replacements[c.replacementLen] = oldRoot
	c.replacementLen++
	pageIndexReplace(
		c.indexNodes, c.indexRoot, oldRoot,
		indexedBitmapPage{kind: indexedBitmapPageReplacement},
	)
	c.root = info.pageNumber
	c.mutationEpoch = nextEpoch
	return freeBitmapCOWError{}
}

// removeLowest removes and reserves the lowest free page in the current draft.
// Absence returns ok=false; this layer never appends a replacement page.
func (c *freeBitmapCOW) removeLowest() (reservedFreePage, bool, freeBitmapCOWError) {
	if c.committed != nil {
		if status := c.committed.checkAccessStatus(); status.failed() {
			return reservedFreePage{}, false, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	return c.removeLowestAfterAccess()
}

func (c *freeBitmapCOW) removeLowestAfterAccess() (reservedFreePage, bool, freeBitmapCOWError) {
	return c.removeLowestPrevalidatedAfterAccess(false)
}

func (c *freeBitmapCOW) removeLowestPrevalidatedAfterAccess(
	scopedPrevalidated bool,
) (reservedFreePage, bool, freeBitmapCOWError) {
	if c.reservationPlanned && c.candidateLen == c.selectedCandidateTarget() {
		return reservedFreePage{}, false, freeBitmapCOWError{}
	}
	if c.root == 0 {
		return reservedFreePage{}, false, freeBitmapCOWError{}
	}
	nextEpoch, problem := c.mutationEpochAfter(1)
	if problem.failed() {
		return reservedFreePage{}, false, problem
	}
	found, problem := c.selectVerifiedPath()
	if problem.failed() || !found {
		return reservedFreePage{}, false, problem
	}
	if problem = c.preflightRemoval(); problem.failed() {
		return reservedFreePage{}, false, problem
	}
	var operation privatePagePoolOperation
	var poolProblem privatePagePoolError
	if c.scoped {
		if !scopedPrevalidated {
			if problem = c.validateScopedBindings(); problem.failed() {
				return reservedFreePage{}, false, problem
			}
		}
		operation, poolProblem = c.pagePool().preflightOperationInScope(c.scope)
	} else {
		operation, poolProblem = c.pagePool().preflightOperation()
	}
	if poolProblem.failed() {
		return reservedFreePage{}, false, bitmapPoolError(poolProblem)
	}
	if problem = c.preflightPoolRemoval(operation); problem.failed() {
		return reservedFreePage{}, false, problem
	}
	c.pagePool().beginOperationPrepared(operation)
	reserved, applyProblem := c.applyRemoval(nextEpoch, operation)
	if applyProblem.failed() {
		abortProblem := c.pagePool().abortOperation(operation)
		if abortProblem.failed() && abortProblem.code != privatePagePoolErrAbortRequired {
			return reservedFreePage{}, false, bitmapPoolError(abortProblem)
		}
		return reservedFreePage{}, false, applyProblem
	}
	c.pagePool().commitOperationPrepared(operation)
	return reserved, true, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) preflightPoolRemoval(operation privatePagePoolOperation) freeBitmapCOWError {
	pool := c.pagePool()
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() || poolStatus.pendingTxn != c.pendingTxn || poolStatus.pendingPageCount != c.pageCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
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
	for index := 0; index < c.pathLen; index++ {
		frame := c.frames[index]
		if frame.committed {
			if !c.survives[index] {
				continue
			}
			info, problem := c.bitmapSlotInfo(c.cloneSlots[index])
			if problem.failed() {
				return problem
			}
			if info.state != privatePageAvailable || info.owner != privatePageOwnerNone ||
				info.origin != privatePageOriginNone {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
			}
			if info.epoch == ^uint64(0) {
				return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: info.pageNumber}
			}
			for previous := 0; previous < index; previous++ {
				if c.frames[previous].committed && c.survives[previous] && c.cloneSlots[previous] == c.cloneSlots[index] {
					return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
				}
			}
			steps += 3 // claim, page write, and committed-origin tag.
			continue
		}
		info, problem := c.bitmapSlotInfo(frame.privateSlot)
		if problem.failed() {
			return problem
		}
		if info.state != privatePageInUse || info.owner != privatePageOwnerBitmap ||
			info.origin != privatePageBitmap || info.pendingTxn != c.pendingTxn {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: info.pageNumber}
		}
		if !c.survives[index] && info.epoch == ^uint64(0) {
			return freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: info.pageNumber}
		}
		steps++ // either one page write or one release.
	}
	if problem := pool.requireMutationSteps(steps); problem.failed() {
		return bitmapPoolError(problem)
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) mutationEpochAfter(advances int) (uint64, freeBitmapCOWError) {
	if advances < 0 {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	next, ok := checkedAdd(c.mutationEpoch, uint64(advances))
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	return next, freeBitmapCOWError{}
}

func checkedIntAdd(left, right int) (int, bool) {
	if left < 0 || right < 0 || left > int(^uint(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}

func (c *freeBitmapCOW) resetRemovalScratch() {
	c.frames = [freeBitmapPathCapacity]freeBitmapPathFrame{}
	c.survives = [freeBitmapPathCapacity]bool{}
	c.pathLen = 0
	c.candidate = 0
	for index := range c.cloneSlots {
		c.cloneSlots[index] = -1
	}
}

func (c *freeBitmapCOW) selectVerifiedPath() (bool, freeBitmapCOWError) {
	c.resetRemovalScratch()
	if c.root == 0 {
		return false, freeBitmapCOWError{}
	}
	committedLimit := c.committedBitmapLimit()
	expectedLevel, covered := minimumFreeBitmapLevel(committedLimit)
	if !covered || expectedLevel >= freeBitmapPathCapacity {
		return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	pageNumber := c.root
	base := uint64(0)
	selectedBySummary := false

	for {
		if c.pathLen == freeBitmapPathCapacity {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		indexed, indexedFound := c.indexedPage(pageNumber)
		pageLimit := committedLimit
		if indexedFound && indexed.kind == indexedBitmapPageArena {
			pageLimit = c.pageCount
		}
		if pageNumber < 2 || uint64(pageNumber) >= pageLimit {
			return false, freeBitmapCOWError{
				code: freeBitmapCOWErrRootPageOutOfBounds,
				page: pageNumber,
			}
		}
		for index := 0; index < c.pathLen; index++ {
			if c.frames[index].pageNumber == pageNumber {
				return false, freeBitmapCOWError{
					code: freeBitmapCOWErrRepeatedPathPage,
					page: pageNumber,
				}
			}
		}

		committed := true
		privateSlot := -1
		var page []byte
		verifiedSlot := -1
		if indexedFound {
			switch indexed.kind {
			case indexedBitmapPageArena:
				info, infoProblem := c.bitmapSlotInfo(indexed.slot)
				if infoProblem.failed() {
					return false, infoProblem
				}
				if info.state != privateBitmapPageInUse {
					return false, freeBitmapCOWError{
						code: freeBitmapCOWErrArenaPageConflict,
						page: pageNumber,
					}
				}
				committed = false
				privateSlot = indexed.slot
				if problem := c.readBitmapSlot(indexed.slot, &c.snapshots[c.pathLen]); problem.failed() {
					return false, problem
				}
				page = c.snapshots[c.pathLen][:]
			case indexedBitmapPageVerified:
				verifiedSlot = indexed.slot
				page = c.verifiedPages[indexed.slot].bytes[:]
			case indexedBitmapPagePlannedCandidate:
				return false, freeBitmapCOWError{
					code: freeBitmapCOWErrArenaPageConflict,
					page: pageNumber,
				}
			case indexedBitmapPageReplacement:
				return false, freeBitmapCOWError{
					code: freeBitmapCOWErrRepeatedCommittedPage,
					page: pageNumber,
				}
			}
		}
		if committed && verifiedSlot < 0 {
			if c.committed == nil {
				return false, freeBitmapCOWError{
					code: freeBitmapCOWErrMissingCommittedPage,
					page: pageNumber,
				}
			}
			if status := c.committed.readPageStatus(pageNumber, &c.snapshots[c.pathLen]); status.failed() {
				return false, freeBitmapCOWError{
					code:   freeBitmapCOWErrSource,
					page:   pageNumber,
					source: status,
				}
			}
			page = c.snapshots[c.pathLen][:]
		}

		pageTxn := c.pendingTxn
		if committed {
			pageTxn = c.sourceTxn
		}
		header, headerProblem := decodePageHeaderNoAlloc(page, pageTxn)
		if headerProblem.code != 0 {
			return false, freeBitmapCOWError{
				code: freeBitmapCOWErrPage,
				page: pageNumber,
				pageProblem: bitmapCOWPageProblem{
					code:          bitmapPageErrHeader,
					headerProblem: headerProblem,
				},
			}
		}
		actualLevel := uint16(0)
		switch header.PageType {
		case PageTypeBitmapLeaf:
		case PageTypeBitmapBranch:
			actualLevel = header.Level
		default:
			return false, freeBitmapCOWError{
				code:     freeBitmapCOWErrUnexpectedPageType,
				page:     pageNumber,
				pageType: header.PageType,
			}
		}
		if actualLevel != expectedLevel {
			code := freeBitmapCOWErrChildLevel
			if c.pathLen == 0 {
				code = freeBitmapCOWErrRootLevel
			}
			return false, freeBitmapCOWError{
				code:          code,
				page:          pageNumber,
				expectedLevel: expectedLevel,
				actualLevel:   actualLevel,
			}
		}

		if expectedLevel == 0 {
			leaf, pageProblem := openBitmapLeafNoAlloc(page, pageTxn, bitmapKindFreePages)
			if pageProblem.code != 0 {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
			}
			if committed && verifiedSlot < 0 {
				if pageProblem = verifyBitmapLeafNoAlloc(leaf, bitmapKindFreePages, base, committedLimit); pageProblem.code != 0 {
					return false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
				}
			}
			if verifiedSlot >= 0 {
				cached := c.verifiedPages[verifiedSlot]
				if cached.pageNumber != pageNumber || cached.base != base || cached.level != expectedLevel {
					return false, freeBitmapCOWError{
						code: freeBitmapCOWErrVerifiedPageIdentityMismatch, page: pageNumber,
						expectedBase: base, actualBase: cached.base,
						expectedLevel: expectedLevel, actualLevel: cached.level,
					}
				}
			}
			candidate, found, valid := searchFreeBitmapLeafNoAlloc(leaf, base, committedLimit)
			if !valid {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			if !found {
				if selectedBySummary {
					return false, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
				}
				return false, freeBitmapCOWError{}
			}
			if candidate > uint64(^uint32(0)) {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			c.frames[c.pathLen] = freeBitmapPathFrame{
				pageNumber:   pageNumber,
				committed:    committed,
				privateSlot:  privateSlot,
				verifiedSlot: verifiedSlot,
				base:         base,
			}
			c.pathLen++
			c.candidate = uint32(candidate)
			c.survives[c.pathLen-1] = freeBitmapLeafSurvives(leaf, base, candidate)
			return true, freeBitmapCOWError{}
		}

		branch, pageProblem := openBitmapBranchNoAlloc(page, pageTxn, bitmapKindFreePages)
		if pageProblem.code != 0 {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
		}
		childSpan, valid := freeBitmapCoverage(expectedLevel - 1)
		if !valid {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if committed && verifiedSlot < 0 {
			if pageProblem = verifyBitmapBranchNoAlloc(branch, base, childSpan, committedLimit, committedLimit); pageProblem.code != 0 {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
			}
		}
		if verifiedSlot >= 0 {
			cached := c.verifiedPages[verifiedSlot]
			if cached.pageNumber != pageNumber || cached.base != base || cached.level != expectedLevel {
				return false, freeBitmapCOWError{
					code: freeBitmapCOWErrVerifiedPageIdentityMismatch, page: pageNumber,
					expectedBase: base, actualBase: cached.base,
					expectedLevel: expectedLevel, actualLevel: cached.level,
				}
			}
		}
		firstChild := 0
		if base < 2 {
			firstChild64 := (2 - base) / childSpan
			firstChild = int(firstChild64)
			if uint64(firstChild) != firstChild64 {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
		}
		childIndex, found := branch.nextSummary(firstChild)
		if !found {
			if selectedBySummary {
				return false, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
			}
			return false, freeBitmapCOWError{}
		}
		childOffset, ok := checkedMul(childSpan, uint64(childIndex))
		if !ok {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		childBase, ok := checkedAdd(base, childOffset)
		if !ok {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if childBase >= committedLimit {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrSelectedCoverageOutsideLimit}
		}
		child := branch.child(childIndex)
		if child == 0 {
			return false, freeBitmapCOWError{code: freeBitmapCOWErrSelectedChildMissing}
		}
		c.frames[c.pathLen] = freeBitmapPathFrame{
			pageNumber:   pageNumber,
			committed:    committed,
			privateSlot:  privateSlot,
			verifiedSlot: verifiedSlot,
			base:         base,
			level:        expectedLevel,
			childIndex:   childIndex,
			childCount:   countFreeBitmapChildren(branch),
		}
		c.pathLen++
		pageNumber = child
		expectedLevel--
		base = childBase
		selectedBySummary = true
	}
}

func (c *freeBitmapCOW) preflightRemoval() freeBitmapCOWError {
	for index := 0; index < c.pathLen; index++ {
		if c.frames[index].pageNumber == c.candidate {
			frame := c.frames[index]
			if frame.committed || frame.privateSlot < 0 || !c.reservationPlanned {
				return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsPathPage, page: c.candidate}
			}
			info, problem := c.bitmapSlotInfo(frame.privateSlot)
			if problem.failed() {
				return problem
			}
			if info.authorization != privateBitmapPageCommittedFreeCandidate {
				return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsPathPage, page: c.candidate}
			}
		}
	}
	if c.candidateLen != 0 {
		previous := c.candidates[c.candidateLen-1]
		if c.candidate == previous {
			return freeBitmapCOWError{code: freeBitmapCOWErrCandidateAlreadyReserved, page: c.candidate}
		}
		if c.candidate < previous {
			return freeBitmapCOWError{
				code:         freeBitmapCOWErrCandidateOrderRegression,
				previousPage: previous,
				page:         c.candidate,
			}
		}
	}
	if c.reservationPlanned {
		target := c.selectedCandidateTarget()
		if c.candidateLen >= target {
			return freeBitmapCOWError{code: freeBitmapCOWErrPlannedCandidatesRemain}
		}
		expected := c.candidates[c.candidateLen]
		if c.candidate != expected {
			return freeBitmapCOWError{
				code:         freeBitmapCOWErrPlannedCandidateMismatch,
				previousPage: expected,
				page:         c.candidate,
			}
		}
	}
	if indexed, found := c.indexedPage(c.candidate); found {
		if indexed.kind == indexedBitmapPageReplacement {
			return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsDraftReplacement, page: c.candidate}
		}
		candidateReserved := false
		if indexed.kind == indexedBitmapPageArena && c.reservationPlanned {
			info, problem := c.bitmapSlotInfo(indexed.slot)
			if problem.failed() {
				return problem
			}
			candidateReserved = info.authorization == privateBitmapPageCommittedFreeCandidate
		}
		if candidateReserved {
			// The reserved candidate may fund the COW mutation that clears its own bit.
		} else if indexed.kind == indexedBitmapPageVerified {
			return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsPathPage, page: c.candidate}
		} else if indexed.kind == indexedBitmapPagePlannedCandidate {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: c.candidate}
		} else {
			return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsArenaPage, page: c.candidate}
		}
	}
	if c.candidateLen == len(c.candidates) {
		return freeBitmapCOWError{code: freeBitmapCOWErrCandidateLedgerExhausted}
	}

	for index := c.pathLen - 2; index >= 0; index-- {
		remaining := c.frames[index].childCount
		if !c.survives[index+1] {
			if remaining == 0 {
				return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
			}
			remaining--
		}
		c.survives[index] = remaining != 0
	}

	committedCount := 0
	cloneCount := 0
	for index := 0; index < c.pathLen; index++ {
		if c.frames[index].committed {
			committedCount++
			if c.survives[index] {
				cloneCount++
			}
		}
	}
	if committedCount > len(c.replacements)-c.replacementLen {
		return freeBitmapCOWError{code: freeBitmapCOWErrReplacementLedgerExhausted}
	}

	if c.availableLen < cloneCount {
		return freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted}
	}
	nextSlot := c.availableLen
	for index := 0; index < c.pathLen; index++ {
		if c.frames[index].committed && c.survives[index] {
			nextSlot--
			c.cloneSlots[index] = c.availableSlots[nextSlot]
		}
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) applyRemoval(
	nextEpoch uint64,
	operation privatePagePoolOperation,
) (reservedFreePage, freeBitmapCOWError) {
	cloneCount := 0
	for index := 0; index < c.pathLen; index++ {
		if c.frames[index].committed && c.survives[index] {
			cloneCount++
		}
	}
	c.availableLen -= cloneCount

	childReference := uint32(0)
	for index := c.pathLen - 1; index >= 0; index-- {
		frame := c.frames[index]
		if !c.survives[index] {
			if !frame.committed {
				if problem := c.releasePrivatePagePrepared(operation, frame.privateSlot); problem.failed() {
					return reservedFreePage{}, problem
				}
			}
			childReference = 0
			continue
		}
		if frame.level == 0 {
			if frame.committed {
				slot := c.cloneSlots[index]
				pageNumber := c.pagePool().slots[slot].pageNumber
				source := &c.snapshots[index]
				if frame.verifiedSlot >= 0 {
					source = &c.verifiedPages[frame.verifiedSlot].bytes
				}
				page := &c.outputs[index]
				encodeFreeBitmapLeafClear(
					page,
					source,
					c.pendingTxn,
					frame.base,
					c.candidate,
				)
				if problem := c.claimBitmapSlotPrepared(operation, slot, page, frame.pageNumber); problem.failed() {
					return reservedFreePage{}, problem
				}
				childReference = pageNumber
			} else {
				page := &c.snapshots[index]
				mutateFreeBitmapLeafClear(
					page,
					c.pendingTxn,
					frame.base,
					c.candidate,
				)
				if problem := c.writeBitmapSlotForOperationPrepared(operation, frame.privateSlot, page); problem.failed() {
					return reservedFreePage{}, problem
				}
				childReference = frame.pageNumber
			}
			continue
		}
		if frame.committed {
			slot := c.cloneSlots[index]
			pageNumber := c.pagePool().slots[slot].pageNumber
			source := &c.snapshots[index]
			if frame.verifiedSlot >= 0 {
				source = &c.verifiedPages[frame.verifiedSlot].bytes
			}
			page := &c.outputs[index]
			encodeFreeBitmapBranchChild(
				page,
				source,
				c.pendingTxn,
				frame.level,
				frame.childIndex,
				childReference,
			)
			if problem := c.claimBitmapSlotPrepared(operation, slot, page, frame.pageNumber); problem.failed() {
				return reservedFreePage{}, problem
			}
			childReference = pageNumber
		} else {
			page := &c.snapshots[index]
			mutateFreeBitmapBranchChild(
				page,
				c.pendingTxn,
				frame.level,
				frame.childIndex,
				childReference,
			)
			if problem := c.writeBitmapSlotForOperationPrepared(operation, frame.privateSlot, page); problem.failed() {
				return reservedFreePage{}, problem
			}
			childReference = frame.pageNumber
		}
	}

	for index := 0; index < c.pathLen; index++ {
		if c.frames[index].committed {
			pageNumber := c.frames[index].pageNumber
			c.replacements[c.replacementLen] = pageNumber
			c.replacementLen++
			if c.frames[index].verifiedSlot >= 0 {
				pageIndexReplace(
					c.indexNodes, c.indexRoot, pageNumber,
					indexedBitmapPage{kind: indexedBitmapPageReplacement},
				)
			} else {
				if c.scoped {
					pageIndexInsertExistingPrechecked(
						c.indexNodes, &c.indexRoot, c.scopeCapacity+c.replacementLen-1,
						pageNumber, indexedBitmapPage{kind: indexedBitmapPageReplacement},
					)
				} else {
					pageIndexInsertPrechecked(
						c.indexNodes, &c.indexRoot, &c.indexLen, pageNumber,
						indexedBitmapPage{kind: indexedBitmapPageReplacement},
					)
				}
			}
		}
	}
	c.candidates[c.candidateLen] = c.candidate
	c.candidateLen++
	c.root = childReference
	c.mutationEpoch = nextEpoch
	return reservedFreePage{pageNumber: c.candidate}, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) writeBitmapSlotForOperationPrepared(
	operation privatePagePoolOperation,
	slot int,
	source *[PageSize]byte,
) freeBitmapCOWError {
	if c.scoped {
		if problem := c.pagePool().writeSlotForOperationInScopePrepared(operation, slot, source); problem.failed() {
			return bitmapPoolError(problem)
		}
	} else {
		c.pagePool().writeSlotPrepared(slot, source)
	}
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) releasePrivatePagePrepared(
	operation privatePagePoolOperation,
	slot int,
) freeBitmapCOWError {
	var problem privatePagePoolError
	if c.scoped {
		problem = c.pagePool().releaseSlotForOperationInScopePrepared(operation, slot, privatePageAvailable)
	} else {
		problem = c.pagePool().releaseSlotPrepared(slot, privatePageAvailable)
	}
	if problem.failed() {
		return bitmapPoolError(problem)
	}
	c.refreshArenaBindingEpoch(slot)
	c.availableSlots[c.availableLen] = slot
	c.availableLen++
	return freeBitmapCOWError{}
}

func (c *freeBitmapCOW) releasePrivatePage(slot int) freeBitmapCOWError {
	if problem := c.releaseBitmapSlot(slot, privatePageAvailable); problem.failed() {
		return problem
	}
	c.availableSlots[c.availableLen] = slot
	c.availableLen++
	return freeBitmapCOWError{}
}

type preparedFreeBitmapCOWLedger struct {
	indexRoot         int
	indexLen          int
	availableLen      int
	scopeMemberVisits int
}

func prepareFreeBitmapCOWLedger(
	pageCount uint64,
	pool *privatePagePool,
	ledger freeBitmapCOWLedger,
) (preparedFreeBitmapCOWLedger, freeBitmapCOWError) {
	preparedSuccessfully := false
	defer func() {
		if !preparedSuccessfully {
			clear(ledger.indexNodes)
			clear(ledger.availableSlots)
		}
	}()

	poolCapacity := pool.capacity()
	if poolCapacity > int(^uint(0)>>1)-len(ledger.replacements) {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	requiredIndex := poolCapacity + len(ledger.replacements)
	if len(ledger.indexNodes) < requiredIndex {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
			code:     freeBitmapCOWErrIndexCapacityTooSmall,
			required: requiredIndex,
			actual:   len(ledger.indexNodes),
		}
	}
	if len(ledger.availableSlots) < poolCapacity {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
			code:     freeBitmapCOWErrAvailableSlotCapacityTooSmall,
			required: poolCapacity,
			actual:   len(ledger.availableSlots),
		}
	}

	root := bitmapCOWNoIndex
	indexLen := 0
	for slot := 0; slot < poolCapacity; slot++ {
		info, poolProblem := pool.slotInfo(slot)
		if poolProblem.failed() {
			return preparedFreeBitmapCOWLedger{}, bitmapPoolError(poolProblem)
		}
		pageNumber := info.pageNumber
		if pageNumber < 2 || uint64(pageNumber) >= pageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if !pageIndexInsert(
			ledger.indexNodes,
			&root,
			&indexLen,
			pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageArena, slot: slot},
		) {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrDuplicateArenaPage, page: pageNumber}
		}
	}
	for index := 0; index < ledger.replacementLen; index++ {
		pageNumber := ledger.replacements[index]
		if pageNumber < 2 || uint64(pageNumber) >= pageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if existing, found := pageIndexFind(ledger.indexNodes, root, pageNumber); found {
			code := freeBitmapCOWErrDuplicateReplacement
			if existing.kind == indexedBitmapPageArena {
				code = freeBitmapCOWErrLedgerPageConflict
			}
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: code, page: pageNumber}
		}
		pageIndexInsertPrechecked(
			ledger.indexNodes,
			&root,
			&indexLen,
			pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageReplacement},
		)
	}
	var previous uint32
	for index := 0; index < ledger.candidateLen; index++ {
		pageNumber := ledger.candidates[index]
		if pageNumber < 2 || uint64(pageNumber) >= pageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if index != 0 {
			if pageNumber == previous {
				return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrDuplicateCandidate, page: pageNumber}
			}
			if pageNumber < previous {
				return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
					code:         freeBitmapCOWErrCandidateOrderRegression,
					previousPage: previous,
					page:         pageNumber,
				}
			}
		}
		previous = pageNumber
		if _, found := pageIndexFind(ledger.indexNodes, root, pageNumber); found {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: pageNumber}
		}
	}

	availableLen := 0
	for slot := poolCapacity - 1; slot >= 0; slot-- {
		info, poolProblem := pool.slotInfo(slot)
		if poolProblem.failed() {
			return preparedFreeBitmapCOWLedger{}, bitmapPoolError(poolProblem)
		}
		if info.state == privateBitmapPageAvailable {
			ledger.availableSlots[availableLen] = slot
			availableLen++
		}
	}
	preparedSuccessfully = true
	return preparedFreeBitmapCOWLedger{
		indexRoot:    root,
		indexLen:     indexLen,
		availableLen: availableLen,
	}, freeBitmapCOWError{}
}

func prepareScopedFreeBitmapCOWLedger(
	pageCount uint64,
	pool *privatePagePool,
	scope privatePageReservationScope,
	scopeCapacity int,
	ledger freeBitmapCOWLedger,
) (preparedFreeBitmapCOWLedger, freeBitmapCOWError) {
	preparedSuccessfully := false
	defer func() {
		if !preparedSuccessfully {
			clear(ledger.indexNodes)
			clear(ledger.availableSlots)
			clear(ledger.arenaBindings)
		}
	}()
	if scopeCapacity < 0 || len(ledger.arenaBindings) < scopeCapacity ||
		scopeCapacity > int(^uint(0)>>1)-len(ledger.replacements) {
		if len(ledger.arenaBindings) < scopeCapacity {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
				code: freeBitmapCOWErrAvailableSlotCapacityTooSmall, required: scopeCapacity, actual: len(ledger.arenaBindings),
			}
		}
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	requiredIndex := scopeCapacity + len(ledger.replacements)
	plannedCandidates := 0
	if ledger.reservationPlanned {
		plannedCandidates = ledger.plannedCandidateLen
	}
	if plannedCandidates > int(^uint(0)>>1)-requiredIndex {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	requiredIndex += plannedCandidates
	if len(ledger.verifiedPages) > int(^uint(0)>>1)-requiredIndex {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	requiredIndex += len(ledger.verifiedPages)
	if len(ledger.indexNodes) < requiredIndex {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
			code: freeBitmapCOWErrIndexCapacityTooSmall, required: requiredIndex, actual: len(ledger.indexNodes),
		}
	}
	if len(ledger.availableSlots) < scopeCapacity {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{
			code: freeBitmapCOWErrAvailableSlotCapacityTooSmall, required: scopeCapacity, actual: len(ledger.availableSlots),
		}
	}
	clear(ledger.indexNodes)
	clear(ledger.availableSlots)
	clear(ledger.arenaBindings)
	root, mapped := bitmapCOWNoIndex, 0
	slotIndex, memberCapacity, memberProblem := pool.scopeMemberStart(scope)
	if memberProblem.failed() || memberCapacity != scopeCapacity {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for slotIndex != privatePagePoolNoIndex {
		info, poolProblem := pool.slotInfo(slotIndex)
		if poolProblem.failed() {
			return preparedFreeBitmapCOWLedger{}, bitmapPoolError(poolProblem)
		}
		if mapped == scopeCapacity {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		binding := &ledger.arenaBindings[mapped]
		*binding = bitmapCOWArenaBinding{
			poolSlot: slotIndex, poolEpoch: info.epoch, storageNode: mapped,
			activeNode: bitmapCOWNoIndex, bound: info.bound,
		}
		if info.bound {
			if info.pageNumber < 2 || uint64(info.pageNumber) >= pageCount {
				return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: info.pageNumber}
			}
			pageIndexInsertExistingPrechecked(
				ledger.indexNodes, &root, mapped, info.pageNumber,
				indexedBitmapPage{kind: indexedBitmapPageArena, slot: slotIndex},
			)
			binding.pageNumber = info.pageNumber
			binding.activeNode = mapped
		}
		mapped++
		slotIndex, memberProblem = pool.scopeMemberNextInScope(scope, slotIndex)
		if memberProblem.failed() {
			return preparedFreeBitmapCOWLedger{}, bitmapPoolError(memberProblem)
		}
	}
	if mapped != scopeCapacity {
		return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for index := 0; index < ledger.replacementLen; index++ {
		pageNumber := ledger.replacements[index]
		if pageNumber < 2 || uint64(pageNumber) >= pageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if _, found := pageIndexFind(ledger.indexNodes, root, pageNumber); found {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: pageNumber}
		}
		pageIndexInsertExistingPrechecked(
			ledger.indexNodes, &root, scopeCapacity+index, pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageReplacement},
		)
	}
	verifiedOffset := scopeCapacity + len(ledger.replacements) + plannedCandidates
	for index := range ledger.verifiedPages {
		verified := ledger.verifiedPages[index]
		if verified.pageNumber < 2 || uint64(verified.pageNumber) >= pageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: verified.pageNumber}
		}
		if _, found := pageIndexFind(ledger.indexNodes, root, verified.pageNumber); found {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: verified.pageNumber}
		}
		pageIndexInsertExistingPrechecked(
			ledger.indexNodes, &root, verifiedOffset+index, verified.pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageVerified, slot: index},
		)
	}
	var previous uint32
	candidateLimit := ledger.candidateLen
	if ledger.reservationPlanned {
		candidateLimit = ledger.plannedCandidateLen
	}
	for index := 0; index < candidateLimit; index++ {
		pageNumber := ledger.candidates[index]
		if pageNumber < 2 || uint64(pageNumber) >= pool.committedPageCount {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if index != 0 && pageNumber <= previous {
			code := freeBitmapCOWErrCandidateOrderRegression
			if pageNumber == previous {
				code = freeBitmapCOWErrDuplicateCandidate
			}
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: code, previousPage: previous, page: pageNumber}
		}
		previous = pageNumber
		if _, found := pageIndexFind(ledger.indexNodes, root, pageNumber); found {
			return preparedFreeBitmapCOWLedger{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: pageNumber}
		}
		if ledger.reservationPlanned {
			pageIndexInsertExistingPrechecked(
				ledger.indexNodes, &root, scopeCapacity+len(ledger.replacements)+index, pageNumber,
				indexedBitmapPage{kind: indexedBitmapPagePlannedCandidate, slot: index},
			)
		}
	}
	availableLen := 0
	for bindingIndex := scopeCapacity - 1; bindingIndex >= 0; bindingIndex-- {
		binding := ledger.arenaBindings[bindingIndex]
		info, poolProblem := pool.slotInfo(binding.poolSlot)
		if poolProblem.failed() {
			return preparedFreeBitmapCOWLedger{}, bitmapPoolError(poolProblem)
		}
		if info.bound && info.state == privatePageAvailable {
			ledger.availableSlots[availableLen] = binding.poolSlot
			availableLen++
		}
	}
	preparedSuccessfully = true
	return preparedFreeBitmapCOWLedger{
		indexRoot: root, indexLen: requiredIndex, availableLen: availableLen,
		scopeMemberVisits: mapped,
	}, freeBitmapCOWError{}
}

func (c *freeBitmapCOW) indexedPage(pageNumber uint32) (indexedBitmapPage, bool) {
	return pageIndexFind(c.indexNodes, c.indexRoot, pageNumber)
}

func pageIndexFind(
	nodes []bitmapCOWIndexNode,
	root int,
	pageNumber uint32,
) (indexedBitmapPage, bool) {
	node, found := pageIndexFindNode(nodes, root, pageNumber)
	if !found {
		return indexedBitmapPage{}, false
	}
	return nodes[node].page, true
}

func pageIndexFindNode(
	nodes []bitmapCOWIndexNode,
	root int,
	pageNumber uint32,
) (int, bool) {
	for root != bitmapCOWNoIndex {
		node := nodes[root]
		if pageNumber < node.pageNumber {
			root = node.left
		} else if pageNumber > node.pageNumber {
			root = node.right
		} else {
			return root, true
		}
	}
	return 0, false
}

func pageIndexReplace(
	nodes []bitmapCOWIndexNode,
	root int,
	pageNumber uint32,
	page indexedBitmapPage,
) (indexedBitmapPage, bool) {
	for root != bitmapCOWNoIndex {
		if pageNumber < nodes[root].pageNumber {
			root = nodes[root].left
		} else if pageNumber > nodes[root].pageNumber {
			root = nodes[root].right
		} else {
			previous := nodes[root].page
			nodes[root].page = page
			return previous, true
		}
	}
	return indexedBitmapPage{}, false
}

func pageIndexInsert(
	nodes []bitmapCOWIndexNode,
	root, length *int,
	pageNumber uint32,
	page indexedBitmapPage,
) bool {
	if _, found := pageIndexFind(nodes, *root, pageNumber); found || *length == len(nodes) {
		return false
	}
	pageIndexInsertPrechecked(nodes, root, length, pageNumber, page)
	return true
}

func pageIndexInsertPrechecked(
	nodes []bitmapCOWIndexNode,
	root, length *int,
	pageNumber uint32,
	page indexedBitmapPage,
) {
	newIndex := *length
	nodes[newIndex] = bitmapCOWIndexNode{
		pageNumber: pageNumber,
		page:       page,
		left:       bitmapCOWNoIndex,
		right:      bitmapCOWNoIndex,
		height:     1,
	}
	if page.kind == indexedBitmapPagePlannedCandidate {
		nodes[newIndex].candidatePage = pageNumber
		nodes[newIndex].candidateIndex = page.slot
		nodes[newIndex].candidateMapped = true
	}
	*length++
	*root = pageIndexInsertUnique(nodes, *root, newIndex)
}

func pageIndexInsertExistingPrechecked(
	nodes []bitmapCOWIndexNode,
	root *int,
	index int,
	pageNumber uint32,
	page indexedBitmapPage,
) {
	node := &nodes[index]
	node.pageNumber = pageNumber
	node.page = page
	node.left = bitmapCOWNoIndex
	node.right = bitmapCOWNoIndex
	node.height = 1
	if page.kind == indexedBitmapPagePlannedCandidate {
		node.candidatePage = pageNumber
		node.candidateIndex = page.slot
		node.candidateMapped = true
	}
	*root = pageIndexInsertUnique(nodes, *root, index)
}

func clearBitmapCOWActiveNode(node *bitmapCOWIndexNode) {
	node.pageNumber = 0
	node.page = indexedBitmapPage{}
	node.left = bitmapCOWNoIndex
	node.right = bitmapCOWNoIndex
	node.height = 0
}

func restorePlannedCandidateNode(nodes []bitmapCOWIndexNode, root *int, index int) {
	node := &nodes[index]
	pageNumber, candidateIndex := node.candidatePage, node.candidateIndex
	clearBitmapCOWActiveNode(node)
	pageIndexInsertExistingPrechecked(
		nodes, root, index, pageNumber,
		indexedBitmapPage{kind: indexedBitmapPagePlannedCandidate, slot: candidateIndex},
	)
}

func pageIndexInsertUnique(nodes []bitmapCOWIndexNode, root, newIndex int) int {
	if root == bitmapCOWNoIndex {
		return newIndex
	}
	newPageNumber := nodes[newIndex].pageNumber
	if newPageNumber < nodes[root].pageNumber {
		nodes[root].left = pageIndexInsertUnique(nodes, nodes[root].left, newIndex)
	} else {
		nodes[root].right = pageIndexInsertUnique(nodes, nodes[root].right, newIndex)
	}
	pageIndexUpdateHeight(nodes, root)
	balance := pageIndexBalance(nodes, root)
	if balance > 1 {
		left := nodes[root].left
		if newPageNumber > nodes[left].pageNumber {
			nodes[root].left = pageIndexRotateLeft(nodes, left)
		}
		return pageIndexRotateRight(nodes, root)
	}
	if balance < -1 {
		right := nodes[root].right
		if newPageNumber < nodes[right].pageNumber {
			nodes[root].right = pageIndexRotateRight(nodes, right)
		}
		return pageIndexRotateLeft(nodes, root)
	}
	return root
}

func pageIndexHeight(nodes []bitmapCOWIndexNode, index int) uint8 {
	if index == bitmapCOWNoIndex {
		return 0
	}
	return nodes[index].height
}

func pageIndexUpdateHeight(nodes []bitmapCOWIndexNode, index int) {
	left := pageIndexHeight(nodes, nodes[index].left)
	right := pageIndexHeight(nodes, nodes[index].right)
	if right > left {
		left = right
	}
	nodes[index].height = left + 1
}

func pageIndexBalance(nodes []bitmapCOWIndexNode, index int) int16 {
	return int16(pageIndexHeight(nodes, nodes[index].left)) -
		int16(pageIndexHeight(nodes, nodes[index].right))
}

func pageIndexRotateLeft(nodes []bitmapCOWIndexNode, root int) int {
	pivot := nodes[root].right
	middle := nodes[pivot].left
	nodes[pivot].left = root
	nodes[root].right = middle
	pageIndexUpdateHeight(nodes, root)
	pageIndexUpdateHeight(nodes, pivot)
	return pivot
}

func pageIndexRotateRight(nodes []bitmapCOWIndexNode, root int) int {
	pivot := nodes[root].left
	middle := nodes[pivot].right
	nodes[pivot].right = root
	nodes[root].left = middle
	pageIndexUpdateHeight(nodes, root)
	pageIndexUpdateHeight(nodes, pivot)
	return pivot
}

func pageIndexRebalance(nodes []bitmapCOWIndexNode, root int) int {
	pageIndexUpdateHeight(nodes, root)
	balance := pageIndexBalance(nodes, root)
	if balance > 1 {
		left := nodes[root].left
		if pageIndexBalance(nodes, left) < 0 {
			nodes[root].left = pageIndexRotateLeft(nodes, left)
		}
		return pageIndexRotateRight(nodes, root)
	}
	if balance < -1 {
		right := nodes[root].right
		if pageIndexBalance(nodes, right) > 0 {
			nodes[root].right = pageIndexRotateRight(nodes, right)
		}
		return pageIndexRotateLeft(nodes, root)
	}
	return root
}

func pageIndexDetachMinimum(nodes []bitmapCOWIndexNode, root int) (int, int) {
	if nodes[root].left == bitmapCOWNoIndex {
		return nodes[root].right, root
	}
	var minimum int
	nodes[root].left, minimum = pageIndexDetachMinimum(nodes, nodes[root].left)
	return pageIndexRebalance(nodes, root), minimum
}

func pageIndexDelete(
	nodes []bitmapCOWIndexNode,
	root int,
	pageNumber uint32,
) (int, int) {
	if pageNumber < nodes[root].pageNumber {
		var removed int
		nodes[root].left, removed = pageIndexDelete(nodes, nodes[root].left, pageNumber)
		return pageIndexRebalance(nodes, root), removed
	}
	if pageNumber > nodes[root].pageNumber {
		var removed int
		nodes[root].right, removed = pageIndexDelete(nodes, nodes[root].right, pageNumber)
		return pageIndexRebalance(nodes, root), removed
	}
	left, right := nodes[root].left, nodes[root].right
	if left == bitmapCOWNoIndex {
		return right, root
	}
	if right == bitmapCOWNoIndex {
		return left, root
	}
	var successor int
	right, successor = pageIndexDetachMinimum(nodes, right)
	nodes[successor].left = left
	nodes[successor].right = right
	return pageIndexRebalance(nodes, successor), root
}

func minimumFreeBitmapLevel(limit uint64) (uint16, bool) {
	level := uint16(0)
	covered := uint64(BitmapLeafBits)
	for covered < limit {
		var ok bool
		covered, ok = checkedMul(covered, BitmapFanout)
		if !ok || level == ^uint16(0) {
			return 0, false
		}
		level++
		if level >= freeBitmapPathCapacity {
			return 0, false
		}
	}
	return level, true
}

func freeBitmapCoverage(level uint16) (uint64, bool) {
	covered := uint64(BitmapLeafBits)
	for index := uint16(0); index < level; index++ {
		var ok bool
		covered, ok = checkedMul(covered, BitmapFanout)
		if !ok {
			return 0, false
		}
	}
	return covered, true
}

// These decoders return concrete status values so corrupt committed pages do
// not allocate error objects while the operation lock is held.
func decodePageHeaderNoAlloc(page []byte, selectedTxn uint64) (PageHeader, bitmapCOWHeaderProblem) {
	if len(page) != PageSize {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrPageSize, length: len(page)}
	}
	if page[0] != PageMagic[0] || page[1] != PageMagic[1] ||
		page[2] != PageMagic[2] || page[3] != PageMagic[3] {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrMagic}
	}
	pageType := PageType(page[4])
	if !validPageType(pageType) {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrPageType, wireType: page[4]}
	}
	if page[5] != 0 {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrFlags, flags: page[5]}
	}
	headerSize := binary.LittleEndian.Uint16(page[6:8])
	if headerSize != PageHeaderSize {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrHeaderSize, headerSize: headerSize}
	}
	bornTxn := binary.LittleEndian.Uint64(page[8:16])
	if bornTxn == 0 {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrBornTransactionZero}
	}
	if bornTxn > selectedTxn {
		return PageHeader{}, bitmapCOWHeaderProblem{
			code: PageHeaderErrBornTransactionFuture, bornTxn: bornTxn, selectedTxn: selectedTxn,
		}
	}
	level := binary.LittleEndian.Uint16(page[18:20])
	if level > MaxTreeLevel {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrLevelTooHigh, level: level}
	}
	if pageType.IsBranch() {
		if level == 0 {
			return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrBranchLevelZero, pageType: pageType}
		}
	} else if level != 0 {
		return PageHeader{}, bitmapCOWHeaderProblem{
			code: PageHeaderErrNonBranchLevelNonzero, pageType: pageType, level: level,
		}
	}
	lower := binary.LittleEndian.Uint16(page[20:22])
	upper := binary.LittleEndian.Uint16(page[22:24])
	if lower < PageHeaderSize || lower > upper || uint64(upper) > PageSize {
		return PageHeader{}, bitmapCOWHeaderProblem{code: PageHeaderErrBounds, lower: lower, upper: upper}
	}
	return PageHeader{
		PageType:   pageType,
		BornTxn:    bornTxn,
		ItemCount:  binary.LittleEndian.Uint16(page[16:18]),
		Level:      level,
		Lower:      lower,
		Upper:      upper,
		Aux:        binary.LittleEndian.Uint32(page[24:28]),
		PageCRC32C: binary.LittleEndian.Uint32(page[PageCRCOffset : PageCRCOffset+4]),
	}, bitmapCOWHeaderProblem{}
}

func openBitmapLeafNoAlloc(
	page []byte,
	selectedTxn uint64,
	kind bitmapKind,
) (bitmapLeaf, bitmapCOWPageProblem) {
	header, headerProblem := decodePageHeaderNoAlloc(page, selectedTxn)
	if headerProblem.code != 0 {
		return bitmapLeaf{}, bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}
	}
	if header.PageType != PageTypeBitmapLeaf {
		return bitmapLeaf{}, bitmapCOWPageProblem{code: bitmapPageErrWrongPageType, pageType: header.PageType}
	}
	wireKind, valid := bitmapKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return bitmapLeaf{}, bitmapCOWPageProblem{code: bitmapPageErrWrongKind, wireKind: header.Aux}
	}
	if header.Lower != bitmapLeafLower || header.Upper != PageSize {
		return bitmapLeaf{}, bitmapCOWPageProblem{code: bitmapPageErrFixedGeometry}
	}
	if int(header.ItemCount) > BitmapLeafWords {
		return bitmapLeaf{}, bitmapCOWPageProblem{code: bitmapPageErrTooManyItems, itemCount: header.ItemCount}
	}
	return bitmapLeaf{page: page, header: header}, bitmapCOWPageProblem{}
}

func verifyBitmapLeafNoAlloc(
	leaf bitmapLeaf,
	kind bitmapKind,
	base, limit uint64,
) bitmapCOWPageProblem {
	if !VerifyPageCRC32C(leaf.page) {
		return bitmapCOWPageProblem{code: bitmapPageErrChecksum}
	}
	if anyNonzero(leaf.page[int(bitmapLeafLower):]) {
		return bitmapCOWPageProblem{code: bitmapPageErrReservedNonzero}
	}
	actualNonzero := 0
	for index := 0; index < BitmapLeafWords; index++ {
		word := leaf.word(index)
		if word == 0 {
			continue
		}
		actualNonzero++
		wordBase, ok := checkedAdd(base, uint64(index)*64)
		if !ok {
			return bitmapCOWPageProblem{code: bitmapPageErrBitOutsideLimit}
		}
		for word != 0 {
			bit := uint64(bits.TrailingZeros64(word))
			absolute, ok := checkedAdd(wordBase, bit)
			if !ok || absolute >= limit ||
				(kind == bitmapKindFreePages && absolute < 2) ||
				(kind == bitmapKindMembershipUsed && absolute == 0) {
				return bitmapCOWPageProblem{code: bitmapPageErrBitOutsideLimit}
			}
			word &= word - 1
		}
	}
	if actualNonzero != int(leaf.header.ItemCount) {
		return bitmapCOWPageProblem{code: bitmapPageErrItemCountMismatch}
	}
	return bitmapCOWPageProblem{}
}

func openBitmapBranchNoAlloc(
	page []byte,
	selectedTxn uint64,
	kind bitmapKind,
) (bitmapBranch, bitmapCOWPageProblem) {
	header, headerProblem := decodePageHeaderNoAlloc(page, selectedTxn)
	if headerProblem.code != 0 {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}
	}
	if header.PageType != PageTypeBitmapBranch {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrWrongPageType, pageType: header.PageType}
	}
	wireKind, valid := bitmapKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrWrongKind, wireKind: header.Aux}
	}
	if header.Lower != bitmapBranchLower || header.Upper != PageSize {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrFixedGeometry}
	}
	if header.ItemCount == 0 {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrEmptyPage}
	}
	if uint64(header.ItemCount) > BitmapFanout {
		return bitmapBranch{}, bitmapCOWPageProblem{code: bitmapPageErrTooManyItems, itemCount: header.ItemCount}
	}
	return bitmapBranch{page: page, header: header}, bitmapCOWPageProblem{}
}

func verifyBitmapBranchNoAlloc(
	branch bitmapBranch,
	base, childSpan, limit, pageCount uint64,
) bitmapCOWPageProblem {
	if !VerifyPageCRC32C(branch.page) {
		return bitmapCOWPageProblem{code: bitmapPageErrChecksum}
	}
	if anyNonzero(branch.page[int(bitmapBranchLower):]) {
		return bitmapCOWPageProblem{code: bitmapPageErrReservedNonzero}
	}
	actualNonzero := 0
	for index := 0; uint64(index) < BitmapFanout; index++ {
		child := branch.child(index)
		if child != 0 {
			actualNonzero++
			if child < 2 || uint64(child) >= pageCount {
				return bitmapCOWPageProblem{code: bitmapPageErrChildPageOutOfBounds, childPage: child}
			}
		}
		offset, ok := checkedMul(childSpan, uint64(index))
		if !ok {
			return bitmapCOWPageProblem{code: bitmapPageErrChildOutsideLimit}
		}
		childBase, ok := checkedAdd(base, offset)
		if !ok {
			return bitmapCOWPageProblem{code: bitmapPageErrChildOutsideLimit}
		}
		if childBase >= limit && (child != 0 || branch.summaryBit(index)) {
			return bitmapCOWPageProblem{code: bitmapPageErrChildOutsideLimit}
		}
	}
	if actualNonzero != int(branch.header.ItemCount) {
		return bitmapCOWPageProblem{code: bitmapPageErrItemCountMismatch}
	}
	return bitmapCOWPageProblem{}
}

func searchFreeBitmapLeafNoAlloc(leaf bitmapLeaf, base, limit uint64) (uint64, bool, bool) {
	first := base
	if first < 2 {
		first = 2
	}
	if first >= limit {
		return 0, false, true
	}
	local := first - base
	firstWord64 := local / 64
	firstWord := int(firstWord64)
	if uint64(firstWord) != firstWord64 {
		return 0, false, false
	}
	for wordIndex := firstWord; wordIndex < BitmapLeafWords; wordIndex++ {
		wordBase, ok := checkedAdd(base, uint64(wordIndex)*64)
		if !ok {
			return 0, false, false
		}
		if wordBase >= limit {
			break
		}
		candidates := leaf.word(wordIndex)
		if wordIndex == firstWord {
			candidates &= ^uint64(0) << uint(local%64)
		}
		remaining := limit - wordBase
		if remaining < 64 {
			candidates &= (uint64(1) << uint(remaining)) - 1
		}
		if candidates != 0 {
			candidate, ok := checkedAdd(wordBase, uint64(bits.TrailingZeros64(candidates)))
			if !ok {
				return 0, false, false
			}
			return candidate, true, true
		}
	}
	return 0, false, true
}

func freeBitmapLeafSurvives(leaf bitmapLeaf, base, candidate uint64) bool {
	local := candidate - base
	selectedWord := int(local / 64)
	selectedMask := uint64(1) << uint(local%64)
	for index := 0; index < BitmapLeafWords; index++ {
		word := leaf.word(index)
		if index == selectedWord {
			word &^= selectedMask
		}
		if word != 0 {
			return true
		}
	}
	return false
}

func countFreeBitmapChildren(branch bitmapBranch) uint16 {
	count := uint16(0)
	for index := 0; uint64(index) < BitmapFanout; index++ {
		if branch.child(index) != 0 {
			count++
		}
	}
	return count
}

func encodeFreeBitmapLeafClear(
	destination, source *[PageSize]byte,
	pendingTxn, base uint64,
	candidate uint32,
) {
	clear(destination[:])
	copy(destination[bitmapSummaryOffset:int(bitmapLeafLower)], source[bitmapSummaryOffset:int(bitmapLeafLower)])
	mutateFreeBitmapLeafClear(destination, pendingTxn, base, candidate)
}

func mutateFreeBitmapLeafClear(
	page *[PageSize]byte,
	pendingTxn, base uint64,
	candidate uint32,
) {
	local := uint64(candidate) - base
	wordIndex := int(local / 64)
	bit := uint(local % 64)
	offset := bitmapSummaryOffset + wordIndex*8
	word := binary.LittleEndian.Uint64(page[offset : offset+8])
	binary.LittleEndian.PutUint64(page[offset:offset+8], word&^(uint64(1)<<bit))
	itemCount := uint16(0)
	for index := 0; index < BitmapLeafWords; index++ {
		at := bitmapSummaryOffset + index*8
		if binary.LittleEndian.Uint64(page[at:at+8]) != 0 {
			itemCount++
		}
	}
	writeFreeBitmapHeader(page, PageTypeBitmapLeaf, pendingTxn, itemCount, 0, bitmapLeafLower)
}

func encodeFreeBitmapBranchChild(
	destination, source *[PageSize]byte,
	pendingTxn uint64,
	level uint16,
	childIndex int,
	childPage uint32,
) {
	clear(destination[:])
	copy(destination[bitmapSummaryOffset:int(bitmapBranchLower)], source[bitmapSummaryOffset:int(bitmapBranchLower)])
	mutateFreeBitmapBranchChild(destination, pendingTxn, level, childIndex, childPage)
}

func mutateFreeBitmapBranchChild(
	page *[PageSize]byte,
	pendingTxn uint64,
	level uint16,
	childIndex int,
	childPage uint32,
) {
	childOffset := bitmapChildrenOffset + childIndex*4
	binary.LittleEndian.PutUint32(page[childOffset:childOffset+4], childPage)
	summaryOffset := bitmapSummaryOffset + (childIndex/64)*8
	summary := binary.LittleEndian.Uint64(page[summaryOffset : summaryOffset+8])
	mask := uint64(1) << uint(childIndex%64)
	if childPage == 0 {
		summary &^= mask
	} else {
		summary |= mask
	}
	binary.LittleEndian.PutUint64(page[summaryOffset:summaryOffset+8], summary)
	itemCount := uint16(0)
	for index := 0; uint64(index) < BitmapFanout; index++ {
		at := bitmapChildrenOffset + index*4
		if binary.LittleEndian.Uint32(page[at:at+4]) != 0 {
			itemCount++
		}
	}
	writeFreeBitmapHeader(page, PageTypeBitmapBranch, pendingTxn, itemCount, level, bitmapBranchLower)
}

func writeFreeBitmapHeader(
	page *[PageSize]byte,
	pageType PageType,
	pendingTxn uint64,
	itemCount, level, lower uint16,
) {
	header := PageHeader{
		PageType:  pageType,
		BornTxn:   pendingTxn,
		ItemCount: itemCount,
		Level:     level,
		Lower:     lower,
		Upper:     PageSize,
		Aux:       uint32(bitmapKindFreePages),
	}
	if err := header.EncodeInto(page[:]); err != nil {
		panic(err)
	}
	if _, err := WritePageCRC32C(page[:]); err != nil {
		panic(err)
	}
}
