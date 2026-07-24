package exactv4

import (
	"math/bits"
	"sync/atomic"
)

type verifiedBitmapPage struct {
	pageNumber uint32
	bytes      [PageSize]byte
	base       uint64
	level      uint16
	parent     int
	remaining  uint32
	survives   bool
}

type freeBitmapReservationBuffers struct {
	pool           *privatePagePool
	poolValidation []uint32
	arena          []reservedBitmapPage
	arenaBindings  []bitmapCOWArenaBinding
	candidates     []uint32
	verifiedPages  []verifiedBitmapPage
	replacements   []uint32
	indexNodes     []bitmapCOWIndexNode
	availableSlots []int
	sourceNodes    []freeBitmapReservationSourceNode
	reclamation    *freeBitmapReclamationTicket
	stage          freeBitmapReservationStageBuffers
}

type freeBitmapReservationStageBuffers struct {
	cow            *freeBitmapCOW
	pool           *privatePagePool
	poolValidation []uint32
	arena          []reservedBitmapPage
	arenaBindings  []bitmapCOWArenaBinding
	replacements   []uint32
	indexNodes     []bitmapCOWIndexNode
	availableSlots []int
}

type freeBitmapReservationSourceKind uint8

const (
	freeBitmapReservationSourceCommitted freeBitmapReservationSourceKind = iota + 1
	freeBitmapReservationSourceReclaimed
)

// freeBitmapReservationSourceNode is caller-owned AVL storage. Committed
// candidates occupy an immutable prefix prepared before the writer lock;
// reclaimed pages occupy a temporary suffix populated only after the complete
// retirement second pass.
type freeBitmapReservationSourceNode struct {
	pageNumber   uint32
	kind         freeBitmapReservationSourceKind
	required     int
	left         int
	right        int
	height       uint8
	subtreeCount uint32
}

type freeBitmapReclamationRequest struct {
	nonce                uint64
	selectedTxn          uint64
	committedPageCount   uint64
	pendingPageCount     uint64
	root                 uint32
	poolEpoch            uint64
	poolGeneration       uint64
	poolMutationEpoch    uint64
	scopeID              uint64
	scopeAnchor          int
	candidateFingerprint uint64
	ticket               *freeBitmapReclamationTicket
}

type freeBitmapReclamationTicket struct {
	nonce                uint64
	selectedTxn          uint64
	committedPageCount   uint64
	pendingPageCount     uint64
	root                 uint32
	poolEpoch            uint64
	poolGeneration       uint64
	poolMutationEpoch    uint64
	scopeID              uint64
	scopeAnchor          int
	candidateFingerprint uint64
	state                atomic.Uint32
	selectionID          uint64
	pages                []uint32
	pageCount            int
	firstPage            uint32
	lastPage             uint32
	fingerprint          uint64
}

// freeBitmapReclamationProof is the opaque move-only capability defined for
// the locked two-pass retirement verifier. Step 3 tests this boundary; verifier
// integration is later. The binder accepts no raw reclaimed slice and
// atomically consumes this ticket before inspecting its pages.
type freeBitmapReclamationProof struct {
	ticket      *freeBitmapReclamationTicket
	nonce       uint64
	selectionID uint64
	pages       []uint32
	pageCount   int
	firstPage   uint32
	lastPage    uint32
	fingerprint uint64
}

var freeBitmapReclamationNonce atomic.Uint64

func mintFreeBitmapReclamationNonce() (uint64, bool) {
	for {
		current := freeBitmapReclamationNonce.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		next := current + 1
		if freeBitmapReclamationNonce.CompareAndSwap(current, next) {
			return next, true
		}
	}
}

type freeBitmapReservationBinding struct {
	committed int
	reclaimed int
	appended  int
}

type freeBitmapReservationCapacityPlan struct {
	committed           committedPageSource
	draft               *privateWriterDraftPageSource
	selectedTxn         uint64
	sourceTxn           uint64
	pendingTxn          uint64
	poolCommittedPages  uint64
	committedPageCount  uint64
	root                uint32
	committedSourceRoot int
	committedSourceLen  int
	privatePages        int
	payloadPages        int
	verifiedLen         int
	indexRequired       int
	capacityFingerprint uint64
	sourceFingerprint   uint64
	buffers             freeBitmapReservationBuffers
}

type freeBitmapReservationAttachment struct {
	freeBitmapReservationCapacityPlan
	cow                   freeBitmapCOW
	scope                 privatePageReservationScope
	poolGeneration        uint64
	poolMutationEpoch     uint64
	reclamationRequest    freeBitmapReclamationRequest
	cowScratchFingerprint uint64
	terminalWork          privatePagePoolCheckpointTerminalWork
}

// freeBitmapReservationSliceSeal records the complete safe-Go identity of a
// caller-owned slice. The backing pointer is taken from capacity, rather than
// length, so an empty slice with reserved storage cannot be substituted.
type freeBitmapReservationSliceSeal[T any] struct {
	length   int
	capacity int
	isNil    bool
	first    *T
}

func sealFreeBitmapReservationSlice[T any](values []T) freeBitmapReservationSliceSeal[T] {
	var first *T
	if cap(values) != 0 {
		first = &values[:1][0]
	}
	return freeBitmapReservationSliceSeal[T]{
		length: len(values), capacity: cap(values), isNil: values == nil, first: first,
	}
}

func (seal freeBitmapReservationSliceSeal[T]) matches(values []T) bool {
	return seal == sealFreeBitmapReservationSlice(values)
}

type freeBitmapReservationCOWSeal struct {
	committed                   committedPageSource
	selectedTxn                 uint64
	pendingTxn                  uint64
	committedPageCount          uint64
	pageCount                   uint64
	pageCountsDistinct          bool
	root                        uint32
	pool                        *privatePagePool
	scope                       privatePageReservationScope
	scoped                      bool
	scopeCapacity               int
	replacementLen              int
	candidateLen                int
	indexRoot                   int
	indexLen                    int
	availableLen                int
	plannedCandidateLen         int
	selectedCandidateLen        int
	candidateSelectionSet       bool
	reservationPlanned          bool
	payloadPageBudget           int
	plannedRequiredPrivatePages int
	mutationEpoch               uint64
	singleInsertPage            [1]uint32
	arenaBindings               freeBitmapReservationSliceSeal[bitmapCOWArenaBinding]
	replacements                freeBitmapReservationSliceSeal[uint32]
	candidates                  freeBitmapReservationSliceSeal[uint32]
	indexNodes                  freeBitmapReservationSliceSeal[bitmapCOWIndexNode]
	availableSlots              freeBitmapReservationSliceSeal[int]
	verifiedPages               freeBitmapReservationSliceSeal[verifiedBitmapPage]
}

type freeBitmapReservationBufferSeal struct {
	pool              *privatePagePool
	poolValidation    freeBitmapReservationSliceSeal[uint32]
	arena             freeBitmapReservationSliceSeal[reservedBitmapPage]
	arenaBindings     freeBitmapReservationSliceSeal[bitmapCOWArenaBinding]
	candidates        freeBitmapReservationSliceSeal[uint32]
	verifiedPages     freeBitmapReservationSliceSeal[verifiedBitmapPage]
	replacements      freeBitmapReservationSliceSeal[uint32]
	indexNodes        freeBitmapReservationSliceSeal[bitmapCOWIndexNode]
	availableSlots    freeBitmapReservationSliceSeal[int]
	sourceNodes       freeBitmapReservationSliceSeal[freeBitmapReservationSourceNode]
	reclamation       *freeBitmapReclamationTicket
	stageCow          *freeBitmapCOW
	stagePool         *privatePagePool
	stageValidation   freeBitmapReservationSliceSeal[uint32]
	stageArena        freeBitmapReservationSliceSeal[reservedBitmapPage]
	stageBindings     freeBitmapReservationSliceSeal[bitmapCOWArenaBinding]
	stageReplacements freeBitmapReservationSliceSeal[uint32]
	stageIndex        freeBitmapReservationSliceSeal[bitmapCOWIndexNode]
	stageAvailable    freeBitmapReservationSliceSeal[int]
}

type freeBitmapReservationPoolSeal struct {
	self                         *privatePagePool
	slots                        freeBitmapReservationSliceSeal[privatePagePoolSlot]
	committedPageCount           uint64
	pendingPageCount             uint64
	pendingTxn                   uint64
	epoch                        uint64
	invalidationEpoch            uint64
	generation                   uint64
	mutationEpoch                uint64
	abortMutationReserve         uint64
	checkpointSequence           uint64
	activeCheckpointID           uint64
	checkpointCleanup            uint64
	checkpointSlotHead           int
	checkpointSlotCount          int
	checkpointIndexHead          int
	checkpointIndexCount         int
	checkpointScopeHead          int
	checkpointScopeCount         int
	operationSequence            uint64
	activeOperationID            uint64
	operationStartEpoch          uint64
	abortRequired                bool
	scopeSequence                uint64
	activeScopes                 int
	unscopedVacantHead           int
	unscopedVacantTail           int
	unscopedVacantCount          int
	indexRoot                    int
	coordinatorSessionID         uint64
	coordinatorSessionGeneration uint64
	registeredWorkID             uint64
	registeredWorkGeneration     uint64
	registeredWorkPhase          uint8
	registeredWorkStartEpoch     uint64
	registeredWorkMutation       bool
	registeredWorkFence          *privateWriterWorkFence
	registeredScopeID            uint64
	registeredScopeAnchor        int
	unacceptedScopes             int
	coordinatorCleanupPending    int
}

type freeBitmapReservationTicketSeal struct {
	ticket               *freeBitmapReclamationTicket
	nonce                uint64
	selectedTxn          uint64
	committedPageCount   uint64
	pendingPageCount     uint64
	root                 uint32
	poolEpoch            uint64
	poolGeneration       uint64
	poolMutationEpoch    uint64
	scopeID              uint64
	scopeAnchor          int
	candidateFingerprint uint64
	selectionID          uint64
	pages                freeBitmapReservationSliceSeal[uint32]
	pageCount            int
	firstPage            uint32
	lastPage             uint32
	fingerprint          uint64
}

type freeBitmapReservationProofSeal struct {
	proof       *freeBitmapReclamationProof
	ticket      *freeBitmapReclamationTicket
	nonce       uint64
	selectionID uint64
	pages       freeBitmapReservationSliceSeal[uint32]
	pageCount   int
	firstPage   uint32
	lastPage    uint32
	fingerprint uint64
}

type freeBitmapReservationBindSeal struct {
	committed                 committedPageSource
	selectedTxn               uint64
	committedPageCount        uint64
	root                      uint32
	committedSourceRoot       int
	committedSourceLen        int
	privatePages              int
	payloadPages              int
	verifiedLen               int
	indexRequired             int
	capacityFingerprint       uint64
	sourceFingerprint         uint64
	buffers                   freeBitmapReservationBufferSeal
	cow                       freeBitmapReservationCOWSeal
	scope                     privatePageReservationScope
	poolGeneration            uint64
	poolMutationEpoch         uint64
	request                   freeBitmapReclamationRequest
	cowFingerprint            uint64
	poolValidationFingerprint uint64
	scopeFingerprint          uint64
	pool                      freeBitmapReservationPoolSeal
	ticket                    freeBitmapReservationTicketSeal
	proof                     freeBitmapReservationProofSeal
}

type freeBitmapReservationCursor struct {
	verified int
	next     int
}

type freeBitmapReservationPlanner struct {
	committed          committedPageSource
	draft              *privateWriterDraftPageSource
	selectedTxn        uint64
	sourceTxn          uint64
	pendingTxn         uint64
	poolCommittedPages uint64
	committedPageCount uint64
	root               uint32
	committedRootLevel uint16
	payloadPages       int
	candidateLen       int
	verifiedLen        int
	survivingMetadata  int
	peakLiveMetadata   int
	peakPrivatePages   int
	indexRoot          int
	indexLen           int
	cursor             [freeBitmapPathCapacity]freeBitmapReservationCursor
	cursorLen          int
	cursorStarted      bool
	capacityPlanning   bool
	sourceRoot         int
	sourceLen          int
	buffers            freeBitmapReservationBuffers
}

func newFreeBitmapReservationPlanner(
	committed committedPageSource,
	selectedTxn, committedPageCount uint64,
	root uint32,
	payloadPages int,
	buffers freeBitmapReservationBuffers,
) (freeBitmapReservationPlanner, freeBitmapCOWError) {
	return newFreeBitmapReservationPlannerTransactions(
		committed, selectedTxn, selectedTxn, selectedTxn+1,
		committedPageCount, committedPageCount, root, payloadPages, buffers,
	)
}

func newFreeBitmapReservationPlannerForDraft(
	draft *privateWriterDraftPageSource,
	selectedTxn, pendingTxn, selectedPageCount, currentPageCount uint64,
	root uint32,
	payloadPages int,
	buffers freeBitmapReservationBuffers,
) (freeBitmapReservationPlanner, freeBitmapCOWError) {
	return newFreeBitmapReservationPlannerTransactions(
		draft, selectedTxn, pendingTxn, pendingTxn,
		selectedPageCount, currentPageCount, root, payloadPages, buffers,
	)
}

func newFreeBitmapReservationPlannerTransactions(
	committed committedPageSource,
	selectedTxn, sourceTxn, pendingTxn, poolCommittedPages, committedPageCount uint64,
	root uint32,
	payloadPages int,
	buffers freeBitmapReservationBuffers,
) (freeBitmapReservationPlanner, freeBitmapCOWError) {
	if committed != nil {
		if status := committed.checkAccessStatus(); status.failed() {
			return freeBitmapReservationPlanner{}, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	if selectedTxn == 0 {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{code: freeBitmapCOWErrSelectedTransactionZero}
	}
	if sourceTxn == 0 || pendingTxn == 0 || poolCommittedPages < 2 ||
		poolCommittedPages > committedPageCount {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{code: freeBitmapCOWErrTransactionExhausted}
	}
	if committedPageCount < 2 || committedPageCount > MaxPageCount {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{
			code: freeBitmapCOWErrPageCountOutOfRange, pageCount: committedPageCount,
		}
	}
	if root != 0 && (root < 2 || uint64(root) >= committedPageCount) {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: root}
	}
	committedRootLevel, ok := minimumFreeBitmapLevel(committedPageCount)
	if !ok {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if payloadPages < 0 {
		return freeBitmapReservationPlanner{}, freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceArenaPages,
			required: payloadPages,
		}
	}
	if problem := validateFreeBitmapReservationStaticAliases(buffers); problem.failed() {
		return freeBitmapReservationPlanner{}, problem
	}
	planner := freeBitmapReservationPlanner{
		committed: committed, selectedTxn: selectedTxn, sourceTxn: sourceTxn, pendingTxn: pendingTxn,
		poolCommittedPages: poolCommittedPages,
		committedPageCount: committedPageCount, root: root,
		committedRootLevel: committedRootLevel, payloadPages: payloadPages,
		indexRoot: bitmapCOWNoIndex, sourceRoot: bitmapCOWNoIndex, buffers: buffers,
	}
	planner.draft, _ = committed.(*privateWriterDraftPageSource)
	for index := range planner.cursor {
		planner.cursor[index].verified = bitmapCOWNoIndex
	}
	return planner, freeBitmapCOWError{}
}

// planCapacity verifies every committed candidate path and computes one exact
// work-unit capacity. It has no pool or scope authority: it initializes,
// reserves, binds, and advances nothing in the shared transaction pool.
func (p *freeBitmapReservationPlanner) planCapacity() (freeBitmapReservationCapacityPlan, freeBitmapCOWError) {
	p.capacityPlanning = true
	if p.committed != nil {
		if status := p.committed.checkAccessStatus(); status.failed() {
			return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	for {
		required, problem := p.capacityRequiredPrivatePages()
		if problem.failed() {
			return freeBitmapReservationCapacityPlan{}, problem
		}
		if p.candidateLen >= required {
			return p.finishCapacity(0)
		}
		candidate, path, pathLen, found, problem := p.nextCandidate()
		if problem.failed() {
			return freeBitmapReservationCapacityPlan{}, problem
		}
		if !found {
			legalEmptyRoot := p.survivingMetadata == 1 && p.verifiedLen == 1 &&
				p.buffers.verifiedPages[0].pageNumber == p.root &&
				p.buffers.verifiedPages[0].parent == bitmapCOWNoIndex &&
				p.buffers.verifiedPages[0].level == 0 &&
				p.buffers.verifiedPages[0].remaining == 0
			if p.survivingMetadata != 0 && !legalEmptyRoot {
				return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
			}
			appended, problem := p.appendedDeficit()
			if problem.failed() {
				return freeBitmapReservationCapacityPlan{}, problem
			}
			return p.finishCapacity(appended)
		}
		if problem = p.reserveCandidate(candidate, path[:pathLen]); problem.failed() {
			return freeBitmapReservationCapacityPlan{}, problem
		}
	}
}

func (p *freeBitmapReservationPlanner) nextCandidate() (
	uint32,
	[freeBitmapPathCapacity]int,
	int,
	bool,
	freeBitmapCOWError,
) {
	path := [freeBitmapPathCapacity]int{bitmapCOWNoIndex, bitmapCOWNoIndex, bitmapCOWNoIndex, bitmapCOWNoIndex}
	if !p.cursorStarted {
		p.cursorStarted = true
		if p.root == 0 {
			return 0, path, 0, false, freeBitmapCOWError{}
		}
		root, problem := p.loadVerified(p.root, 0, p.committedRootLevel, bitmapCOWNoIndex)
		if problem.failed() {
			return 0, path, 0, false, problem
		}
		next := 0
		if p.committedRootLevel == 0 {
			next = 2
		}
		p.cursor[0] = freeBitmapReservationCursor{verified: root, next: next}
		p.cursorLen = 1
	}

	for p.cursorLen != 0 {
		frameIndex := p.cursorLen - 1
		verifiedIndex := p.cursor[frameIndex].verified
		page := &p.buffers.verifiedPages[verifiedIndex]
		if page.level == 0 {
			leaf, pageProblem := openBitmapLeafNoAlloc(page.bytes[:], p.sourceTxn, bitmapKindFreePages)
			if pageProblem.code != 0 {
				return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: page.pageNumber, pageProblem: pageProblem}
			}
			start, ok := checkedAdd(page.base, uint64(p.cursor[frameIndex].next))
			if !ok {
				return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			candidate, found, valid := searchFreeBitmapLeafFromNoAlloc(leaf, page.base, p.committedPageCount, start)
			if !valid {
				return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			if found {
				local := candidate - page.base
				if candidate > uint64(^uint32(0)) || local > uint64(^uint(0)>>1) {
					return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
				}
				next, ok := checkedIntAdd(int(local), 1)
				if !ok {
					return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
				}
				p.cursor[frameIndex].next = next
				for index := 0; index < p.cursorLen; index++ {
					path[index] = p.cursor[index].verified
				}
				return uint32(candidate), path, p.cursorLen, true, freeBitmapCOWError{}
			}
			p.cursorLen--
			continue
		}

		branch, pageProblem := openBitmapBranchNoAlloc(page.bytes[:], p.sourceTxn, bitmapKindFreePages)
		if pageProblem.code != 0 {
			return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: page.pageNumber, pageProblem: pageProblem}
		}
		childIndex, found := branch.nextSummary(p.cursor[frameIndex].next)
		if !found {
			p.cursorLen--
			continue
		}
		p.cursor[frameIndex].next = childIndex + 1
		child := branch.child(childIndex)
		if child == 0 {
			return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrSelectedChildMissing}
		}
		childSpan, ok := freeBitmapCoverage(page.level - 1)
		if !ok {
			return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		offset, ok := checkedMul(childSpan, uint64(childIndex))
		if !ok {
			return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		childBase, ok := checkedAdd(page.base, offset)
		if !ok {
			return 0, path, 0, false, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if childBase >= p.committedPageCount || p.cursorLen == freeBitmapPathCapacity {
			code := freeBitmapCOWErrSelectedCoverageOutsideLimit
			if p.cursorLen == freeBitmapPathCapacity {
				code = freeBitmapCOWErrCoverageOverflow
			}
			return 0, path, 0, false, freeBitmapCOWError{code: code}
		}
		verified, problem := p.loadVerified(child, childBase, page.level-1, verifiedIndex)
		if problem.failed() {
			return 0, path, 0, false, problem
		}
		p.cursor[p.cursorLen] = freeBitmapReservationCursor{verified: verified}
		p.cursorLen++
	}
	return 0, path, 0, false, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) loadVerified(
	pageNumber uint32,
	base uint64,
	expectedLevel uint16,
	parent int,
) (int, freeBitmapCOWError) {
	if pageNumber < 2 || uint64(pageNumber) >= p.committedPageCount {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrRootPageOutOfBounds, page: pageNumber}
	}
	if _, found := pageIndexFind(p.buffers.indexNodes, p.indexRoot, pageNumber); found {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrRepeatedCommittedPage, page: pageNumber}
	}
	nextVerifiedLen, ok := checkedIntAdd(p.verifiedLen, 1)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if problem := p.ensureRoom(freeBitmapResourceVerifiedPages, nextVerifiedLen, len(p.buffers.verifiedPages)); problem.failed() {
		return 0, problem
	}
	nextIndexLen, ok := checkedIntAdd(p.indexLen, 1)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	if problem := p.ensureRoom(freeBitmapResourceIndexNodes, nextIndexLen, len(p.buffers.indexNodes)); problem.failed() {
		return 0, problem
	}
	nextSurvivingMetadata, ok := checkedIntAdd(p.survivingMetadata, 1)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if nextSurvivingMetadata > nextVerifiedLen {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
	}
	if problem := p.ensureRoom(freeBitmapResourceVerifiedPages, nextSurvivingMetadata, len(p.buffers.verifiedPages)); problem.failed() {
		return 0, problem
	}
	if p.committed == nil {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrMissingCommittedPage, page: pageNumber}
	}
	verified := p.verifiedLen
	page := &p.buffers.verifiedPages[verified].bytes
	if status := p.committed.readPageStatus(pageNumber, page); status.failed() {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrSource, page: pageNumber, source: status}
	}
	header, headerProblem := decodePageHeaderNoAlloc(page[:], p.sourceTxn)
	if headerProblem.code != 0 {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: bitmapCOWPageProblem{code: bitmapPageErrHeader, headerProblem: headerProblem}}
	}
	actualLevel := uint16(0)
	switch header.PageType {
	case PageTypeBitmapLeaf:
	case PageTypeBitmapBranch:
		actualLevel = header.Level
	default:
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrUnexpectedPageType, page: pageNumber, pageType: header.PageType}
	}
	if actualLevel != expectedLevel {
		code := freeBitmapCOWErrChildLevel
		if parent == bitmapCOWNoIndex {
			code = freeBitmapCOWErrRootLevel
		}
		return 0, freeBitmapCOWError{code: code, page: pageNumber, expectedLevel: expectedLevel, actualLevel: actualLevel}
	}

	var remaining uint32
	if expectedLevel == 0 {
		leaf, pageProblem := openBitmapLeafNoAlloc(page[:], p.sourceTxn, bitmapKindFreePages)
		if pageProblem.code == 0 {
			pageProblem = verifyBitmapLeafNoAlloc(leaf, bitmapKindFreePages, base, p.committedPageCount)
		}
		if pageProblem.code != 0 {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
		}
		for index := 0; index < BitmapLeafWords; index++ {
			remaining += uint32(bits.OnesCount64(leaf.word(index)))
		}
	} else {
		branch, pageProblem := openBitmapBranchNoAlloc(page[:], p.sourceTxn, bitmapKindFreePages)
		childSpan, ok := freeBitmapCoverage(expectedLevel - 1)
		if !ok {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		if pageProblem.code == 0 {
			pageProblem = verifyBitmapBranchNoAlloc(branch, base, childSpan, p.committedPageCount, p.committedPageCount)
		}
		if pageProblem.code != 0 {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrPage, page: pageNumber, pageProblem: pageProblem}
		}
		for index := 0; uint64(index) < BitmapFanout; index++ {
			if branch.summaryBit(index) != (branch.child(index) != 0) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
			}
		}
		remaining = uint32(header.ItemCount)
	}
	if remaining == 0 &&
		(parent != bitmapCOWNoIndex || expectedLevel != 0 || pageNumber != p.root) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
	}

	entry := &p.buffers.verifiedPages[verified]
	entry.pageNumber = pageNumber
	entry.base = base
	entry.level = expectedLevel
	entry.parent = parent
	entry.remaining = remaining
	entry.survives = true
	pageIndexInsertPrechecked(
		p.buffers.indexNodes, &p.indexRoot, &p.indexLen, pageNumber,
		indexedBitmapPage{kind: indexedBitmapPageVerified, slot: verified},
	)
	p.verifiedLen = nextVerifiedLen
	p.survivingMetadata = nextSurvivingMetadata
	return verified, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) reserveCandidate(
	candidate uint32,
	path []int,
) freeBitmapCOWError {
	if p.candidateLen != 0 {
		previous := p.buffers.candidates[p.candidateLen-1]
		if candidate <= previous {
			code := freeBitmapCOWErrCandidateOrderRegression
			if candidate == previous {
				code = freeBitmapCOWErrDuplicateCandidate
			}
			return freeBitmapCOWError{code: code, previousPage: previous, page: candidate}
		}
	}
	nextCandidateLen, ok := checkedIntAdd(p.candidateLen, 1)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	nextIndexLen, ok := checkedIntAdd(p.indexLen, 1)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	nextSourceLen := p.sourceLen
	if p.capacityPlanning {
		nextSourceLen, ok = checkedIntAdd(p.sourceLen, 1)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
		}
	}
	if problem := p.ensureRoom(freeBitmapResourceCandidatePages, nextCandidateLen, len(p.buffers.candidates)); problem.failed() {
		return problem
	}
	// The all-bound compatibility adapter is the only path that assigns
	// candidates directly into legacy arena storage. Production capacity
	// planning retains no pool or arena authority.
	if !p.capacityPlanning {
		if problem := p.ensureRoom(freeBitmapResourceArenaPages, nextCandidateLen, len(p.buffers.arena)); problem.failed() {
			return problem
		}
	}
	for _, check := range []struct {
		resource  freeBitmapReservationResource
		required  int
		available int
	}{
		{freeBitmapResourceIndexNodes, nextIndexLen, len(p.buffers.indexNodes)},
		{freeBitmapResourceSourceNodes, nextSourceLen, len(p.buffers.sourceNodes)},
	} {
		if problem := p.ensureRoom(check.resource, check.required, check.available); problem.failed() {
			return problem
		}
	}
	if _, found := pageIndexFind(p.buffers.indexNodes, p.indexRoot, candidate); found {
		return freeBitmapCOWError{code: freeBitmapCOWErrCandidateIsPathPage, page: candidate}
	}
	p.buffers.candidates[p.candidateLen] = candidate
	pageIndexInsertPrechecked(
		p.buffers.indexNodes, &p.indexRoot, &p.indexLen, candidate,
		indexedBitmapPage{kind: indexedBitmapPagePlannedCandidate, slot: p.candidateLen},
	)
	p.candidateLen = nextCandidateLen
	if len(path) == 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
	}
	leaf := path[len(path)-1]
	if p.buffers.verifiedPages[leaf].remaining == 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
	}
	p.buffers.verifiedPages[leaf].remaining--
	if p.buffers.verifiedPages[leaf].remaining == 0 {
		if problem := p.collapsePath(leaf); problem.failed() {
			return problem
		}
	}
	if p.survivingMetadata > p.peakLiveMetadata {
		p.peakLiveMetadata = p.survivingMetadata
	}
	currentRequired, problem := p.currentRequiredPrivatePages()
	if problem.failed() {
		return problem
	}
	if currentRequired > p.peakPrivatePages {
		p.peakPrivatePages = currentRequired
	}
	if p.capacityPlanning {
		required, problem := p.capacityRequiredPrivatePages()
		if problem.failed() {
			return problem
		}
		p.sourceRoot = freeBitmapSourceInsert(
			p.buffers.sourceNodes, p.sourceRoot, p.sourceLen, candidate,
			freeBitmapReservationSourceCommitted, required,
		)
		p.sourceLen = nextSourceLen
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) collapsePath(page int) freeBitmapCOWError {
	for {
		if !p.buffers.verifiedPages[page].survives || p.survivingMetadata == 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
		}
		p.buffers.verifiedPages[page].survives = false
		p.survivingMetadata--
		parent := p.buffers.verifiedPages[page].parent
		if parent == bitmapCOWNoIndex {
			return freeBitmapCOWError{}
		}
		if p.buffers.verifiedPages[parent].remaining == 0 {
			return freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
		}
		p.buffers.verifiedPages[parent].remaining--
		if p.buffers.verifiedPages[parent].remaining != 0 {
			return freeBitmapCOWError{}
		}
		page = parent
	}
}

func (p *freeBitmapReservationPlanner) currentRequiredPrivatePages() (int, freeBitmapCOWError) {
	result, ok := checkedIntAdd(p.payloadPages, p.survivingMetadata)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if p.peakLiveMetadata > result {
		result = p.peakLiveMetadata
	}
	return result, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) requiredPrivatePages() (int, freeBitmapCOWError) {
	return p.currentRequiredPrivatePages()
}

func (p *freeBitmapReservationPlanner) capacityRequiredPrivatePages() (int, freeBitmapCOWError) {
	result, problem := p.currentRequiredPrivatePages()
	if problem.failed() {
		return 0, problem
	}
	if p.peakPrivatePages > result {
		result = p.peakPrivatePages
	}
	return result, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) appendedDeficit() (int, freeBitmapCOWError) {
	required, problem := p.requiredPrivatePages()
	if p.capacityPlanning {
		required, problem = p.capacityRequiredPrivatePages()
	}
	if problem.failed() {
		return 0, problem
	}
	appended := required - p.candidateLen
	if appended < 0 {
		appended = 0
	}
	if uint64(appended) > MaxPageCount-p.committedPageCount {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
	}
	return appended, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) finishCapacity(
	appendedLen int,
) (freeBitmapReservationCapacityPlan, freeBitmapCOWError) {
	privatePages, ok := checkedIntAdd(p.candidateLen, appendedLen)
	if !ok {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	requiredPrivatePages, problem := p.capacityRequiredPrivatePages()
	if problem.failed() {
		return freeBitmapReservationCapacityPlan{}, problem
	}
	if privatePages < requiredPrivatePages {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceArenaPages,
			required: requiredPrivatePages, actual: privatePages,
		}
	}
	indexRequired, ok := checkedIntAdd(privatePages, p.verifiedLen)
	if ok {
		indexRequired, ok = checkedIntAdd(indexRequired, p.candidateLen)
	}
	if ok {
		indexRequired, ok = checkedIntAdd(indexRequired, p.verifiedLen)
	}
	if !ok {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	for _, check := range []struct {
		resource  freeBitmapReservationResource
		required  int
		available int
	}{
		{freeBitmapResourceArenaPages, privatePages, len(p.buffers.poolValidation)},
		{freeBitmapResourceArenaBindings, privatePages, len(p.buffers.arenaBindings)},
		{freeBitmapResourceAvailableSlots, privatePages, len(p.buffers.availableSlots)},
		{freeBitmapResourceReplacementPages, p.verifiedLen, len(p.buffers.replacements)},
		{freeBitmapResourceIndexNodes, indexRequired, len(p.buffers.indexNodes)},
		{freeBitmapResourceSourceNodes, p.candidateLen, len(p.buffers.sourceNodes)},
	} {
		if problem := p.ensureRoom(check.resource, check.required, check.available); problem.failed() {
			return freeBitmapReservationCapacityPlan{}, problem
		}
	}
	if privatePages == 0 {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceArenaPages,
			required: 1, actual: 0,
		}
	}
	if p.buffers.reclamation == nil {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceReclamationTicket,
			required: 1, actual: 0,
		}
	}
	if uint64(appendedLen) > MaxPageCount-p.committedPageCount {
		return freeBitmapReservationCapacityPlan{}, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
	}
	productionBuffers := p.buffers
	productionBuffers.pool = nil
	productionBuffers.arena = nil
	return freeBitmapReservationCapacityPlan{
		committed: p.committed, selectedTxn: p.selectedTxn,
		draft:     p.draft,
		sourceTxn: p.sourceTxn, pendingTxn: p.pendingTxn, poolCommittedPages: p.poolCommittedPages,
		committedPageCount: p.committedPageCount, root: p.root,
		committedSourceRoot: p.sourceRoot, committedSourceLen: p.sourceLen,
		privatePages: privatePages, payloadPages: p.payloadPages,
		verifiedLen: p.verifiedLen, indexRequired: indexRequired,
		capacityFingerprint: freeBitmapCapacityFingerprint(
			p.buffers.candidates[:p.candidateLen], p.buffers.verifiedPages[:p.verifiedLen],
		),
		sourceFingerprint: freeBitmapSourceFingerprint(p.buffers.sourceNodes[:p.sourceLen]),
		buffers:           productionBuffers,
	}, freeBitmapCOWError{}
}

// attach binds a pure capacity plan to one already-reserved vacant scope in
// the coordinator-owned shared pool. privatePages is this work unit's complete
// bitmap-metadata-plus-payload capacity. Foreign work-unit scopes remain
// outside this attachment. Step 6 will define their predecessor sequencing and
// remaining-source authority; step 3 accepts only the initial predecessor.
func (p *freeBitmapReservationCapacityPlan) attach(
	pool *privatePagePool,
	scope privatePageReservationScope,
) (freeBitmapReservationAttachment, freeBitmapCOWError) {
	return p.attachAtPredecessor(pool, scope, nil)
}

func (p *freeBitmapReservationCapacityPlan) attachDraft(
	pool *privatePagePool,
	scope privatePageReservationScope,
	draft *privateWriterDraftPageSource,
) (freeBitmapReservationAttachment, freeBitmapCOWError) {
	if draft == nil || p.committed != draft {
		return freeBitmapReservationAttachment{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	return p.attachAtPredecessor(pool, scope, draft)
}

func (p *freeBitmapReservationCapacityPlan) attachAtPredecessor(
	pool *privatePagePool,
	scope privatePageReservationScope,
	draft *privateWriterDraftPageSource,
) (freeBitmapReservationAttachment, freeBitmapCOWError) {
	if problem := p.validateCapacityScratch(); problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	if problem := p.validateAttachmentScratch(pool); problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	status, poolProblem := pool.status()
	if poolProblem.failed() || status.pendingTxn != p.pendingTxn ||
		status.committedPageCount != p.poolCommittedPages {
		return freeBitmapReservationAttachment{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	// Step 3 attaches only to the initial predecessor. A later live tail or an
	// already-owned source page requires the step-6 successor chain to replan
	// from the current draft; silently skipping it would invalidate prefix COW
	// evidence.
	if status.pendingPageCount != p.committedPageCount {
		return freeBitmapReservationAttachment{}, freeBitmapCOWError{
			code: freeBitmapCOWErrStaleReservationPredecessor, pageCount: status.pendingPageCount,
		}
	}
	for _, pageNumber := range p.buffers.candidates[:p.committedSourceLen] {
		if pool.contains(pageNumber) {
			return freeBitmapReservationAttachment{}, freeBitmapCOWError{
				code: freeBitmapCOWErrStaleReservationPredecessor, page: pageNumber,
			}
		}
	}
	for _, verified := range p.buffers.verifiedPages[:p.verifiedLen] {
		if pool.contains(verified.pageNumber) {
			if draft == nil {
				return freeBitmapReservationAttachment{}, freeBitmapCOWError{
					code: freeBitmapCOWErrStaleReservationPredecessor, page: verified.pageNumber,
				}
			}
			residence, residenceProblem := draft.residence(verified.pageNumber)
			if residenceProblem.failed() || residence.kind != privateWriterPagePriorScopePrivate {
				return freeBitmapReservationAttachment{}, freeBitmapCOWError{
					code: freeBitmapCOWErrStaleReservationPredecessor, page: verified.pageNumber,
				}
			}
		}
	}
	if problem := p.validateVacantAttachmentScope(pool, scope); problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	nonce, minted := mintFreeBitmapReclamationNonce()
	if !minted {
		return freeBitmapReservationAttachment{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}

	stage := &p.buffers.stage
	stageLedger := freeBitmapCOWLedger{
		arena: stage.arena[:p.privatePages], arenaBindings: stage.arenaBindings[:p.privatePages],
		replacements: stage.replacements[:p.verifiedLen],
		candidates:   p.buffers.candidates[:p.committedSourceLen],
		indexNodes:   stage.indexNodes[:p.indexRequired], availableSlots: stage.availableSlots[:p.privatePages],
		verifiedPages: p.buffers.verifiedPages[:p.verifiedLen], plannedCandidateLen: p.committedSourceLen,
		reservationPlanned: true, payloadPageBudget: p.payloadPages, plannedPrivatePages: p.privatePages,
	}
	problem := initializeFreeBitmapCOWWithScopedPoolTransactions(
		stage.cow,
		p.committed, p.selectedTxn, p.sourceTxn, p.pendingTxn, p.committedPageCount, p.root,
		pool, scope, stageLedger,
	)
	if problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	if problem = stage.cow.selectPlannedCandidatePrefix(0); problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	if problem = stage.cow.validateScopedBindings(); problem.failed() {
		return freeBitmapReservationAttachment{}, problem
	}
	statusAfter, poolProblem := pool.status()
	if poolProblem.failed() || statusAfter != status {
		return freeBitmapReservationAttachment{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}

	copy(p.buffers.arenaBindings[:p.privatePages], stage.cow.arenaBindings)
	clear(p.buffers.replacements[:p.verifiedLen])
	copy(p.buffers.indexNodes[:p.indexRequired], stage.cow.indexNodes)
	copy(p.buffers.availableSlots[:p.privatePages], stage.cow.availableSlots)
	cow := *stage.cow
	cow.arenaBindings = p.buffers.arenaBindings[:p.privatePages]
	cow.replacements = p.buffers.replacements[:p.verifiedLen]
	cow.candidates = p.buffers.candidates[:p.committedSourceLen]
	cow.indexNodes = p.buffers.indexNodes[:p.indexRequired]
	cow.availableSlots = p.buffers.availableSlots[:p.privatePages]
	cow.verifiedPages = p.buffers.verifiedPages[:p.verifiedLen]

	request := freeBitmapReclamationRequest{
		nonce: nonce, selectedTxn: p.selectedTxn,
		committedPageCount: p.committedPageCount, pendingPageCount: status.pendingPageCount,
		root: p.root, poolEpoch: scope.poolEpoch,
		poolGeneration: status.generation, poolMutationEpoch: status.mutationEpoch,
		scopeID: scope.id, scopeAnchor: scope.anchor,
		candidateFingerprint: freeBitmapPageFingerprint(cow.candidates),
		ticket:               p.buffers.reclamation,
	}
	ticket := p.buffers.reclamation
	ticket.state.Store(0)
	ticket.nonce = request.nonce
	ticket.selectedTxn = request.selectedTxn
	ticket.committedPageCount = request.committedPageCount
	ticket.pendingPageCount = request.pendingPageCount
	ticket.root = request.root
	ticket.poolEpoch = request.poolEpoch
	ticket.poolGeneration = request.poolGeneration
	ticket.poolMutationEpoch = request.poolMutationEpoch
	ticket.scopeID = request.scopeID
	ticket.scopeAnchor = request.scopeAnchor
	ticket.candidateFingerprint = request.candidateFingerprint
	ticket.selectionID = 0
	ticket.pages = nil
	ticket.pageCount = 0
	ticket.firstPage = 0
	ticket.lastPage = 0
	ticket.fingerprint = 0
	ticket.state.Store(1)

	return freeBitmapReservationAttachment{
		freeBitmapReservationCapacityPlan: *p,
		cow:                               cow,
		scope:                             scope,
		poolGeneration:                    status.generation,
		poolMutationEpoch:                 status.mutationEpoch,
		reclamationRequest:                request,
		cowScratchFingerprint:             freeBitmapReservationCOWFingerprint(&cow),
	}, freeBitmapCOWError{}
}

// completeFreeBitmapReclamation is the narrow hand-off that the retirement
// verifier will call only after its second pass. Step 3 defines and tests the
// capability boundary; retirement-reader integration is deliberately later.
// A failed scan never calls this function and therefore issues no proof. An
// empty result still requires this authenticated state transition after locked
// source selection; selectionID zero is only its canonical encoding, never its
// authority.
func completeFreeBitmapReclamation(
	request freeBitmapReclamationRequest,
	selectionID uint64,
	pages []uint32,
) (freeBitmapReclamationProof, freeBitmapCOWError) {
	ticket := request.ticket
	if request.nonce == 0 || ((len(pages) == 0) != (selectionID == 0)) || ticket == nil ||
		ticket.nonce != request.nonce || ticket.selectedTxn != request.selectedTxn ||
		ticket.committedPageCount != request.committedPageCount ||
		ticket.pendingPageCount != request.pendingPageCount || ticket.root != request.root ||
		ticket.poolEpoch != request.poolEpoch || ticket.poolGeneration != request.poolGeneration ||
		ticket.poolMutationEpoch != request.poolMutationEpoch ||
		ticket.scopeID != request.scopeID || ticket.scopeAnchor != request.scopeAnchor ||
		ticket.candidateFingerprint != request.candidateFingerprint ||
		!ticket.state.CompareAndSwap(1, 2) {
		return freeBitmapReclamationProof{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	fingerprint := freeBitmapPageFingerprint(pages)
	firstPage, lastPage := uint32(0), uint32(0)
	if len(pages) != 0 {
		firstPage, lastPage = pages[0], pages[len(pages)-1]
	}
	ticket.selectionID = selectionID
	ticket.pages = pages
	ticket.pageCount = len(pages)
	ticket.firstPage = firstPage
	ticket.lastPage = lastPage
	ticket.fingerprint = fingerprint
	return freeBitmapReclamationProof{
		ticket: ticket, nonce: request.nonce, selectionID: selectionID, pages: pages,
		pageCount: len(pages), firstPage: firstPage, lastPage: lastPage, fingerprint: fingerprint,
	}, freeBitmapCOWError{}
}

func reservationSlicesOverlap[T any](left, right []T) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	for index := range right {
		if &left[0] == &right[index] {
			return true
		}
	}
	for index := range left {
		if &right[0] == &left[index] {
			return true
		}
	}
	return false
}

func validateFreeBitmapReservationStaticAliases(
	buffers freeBitmapReservationBuffers,
) freeBitmapCOWError {
	scratch := [5][]uint32{
		buffers.poolValidation,
		buffers.candidates,
		buffers.replacements,
		buffers.stage.poolValidation,
		buffers.stage.replacements,
	}
	for left := 0; left < len(scratch); left++ {
		for right := left + 1; right < len(scratch); right++ {
			if reservationSlicesOverlap(scratch[left], scratch[right]) {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
		}
	}
	return freeBitmapCOWError{}
}

func validateFreeBitmapReservationProofAliases(
	buffers freeBitmapReservationBuffers,
	pages []uint32,
) freeBitmapCOWError {
	if problem := validateFreeBitmapReservationStaticAliases(buffers); problem.failed() {
		return problem
	}
	for _, scratch := range [5][]uint32{
		buffers.poolValidation,
		buffers.candidates,
		buffers.replacements,
		buffers.stage.poolValidation,
		buffers.stage.replacements,
	} {
		if reservationSlicesOverlap(scratch, pages) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationCapacityPlan) validateCapacityScratch() freeBitmapCOWError {
	if p == nil || p.selectedTxn == 0 || p.selectedTxn == ^uint64(0) ||
		p.committedPageCount < 2 || p.committedPageCount > MaxPageCount ||
		p.privatePages <= 0 || p.payloadPages < 0 || p.verifiedLen < 0 ||
		p.committedSourceLen < 0 || p.indexRequired < 0 ||
		p.committedSourceLen > len(p.buffers.candidates) ||
		p.committedSourceLen > len(p.buffers.sourceNodes) ||
		p.verifiedLen > len(p.buffers.verifiedPages) ||
		p.privatePages > len(p.buffers.poolValidation) ||
		p.privatePages > len(p.buffers.arenaBindings) ||
		p.privatePages > len(p.buffers.availableSlots) ||
		p.verifiedLen > len(p.buffers.replacements) ||
		p.indexRequired > len(p.buffers.indexNodes) || p.buffers.reclamation == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if p.committed != nil {
		if status := p.committed.checkAccessStatus(); status.failed() {
			return freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	if problem := validateFreeBitmapReservationStaticAliases(p.buffers); problem.failed() {
		return problem
	}
	if freeBitmapCapacityFingerprint(
		p.buffers.candidates[:p.committedSourceLen],
		p.buffers.verifiedPages[:p.verifiedLen],
	) != p.capacityFingerprint ||
		freeBitmapSourceFingerprint(p.buffers.sourceNodes[:p.committedSourceLen]) != p.sourceFingerprint {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if p.committedSourceLen == 0 {
		if p.committedSourceRoot != bitmapCOWNoIndex {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
		}
		return freeBitmapCOWError{}
	}
	if p.committedSourceRoot < 0 || p.committedSourceRoot >= p.committedSourceLen ||
		p.buffers.sourceNodes[p.committedSourceRoot].subtreeCount != uint32(p.committedSourceLen) {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	for rank := 0; rank < p.committedSourceLen; rank++ {
		node, found := freeBitmapSourceAt(
			p.buffers.sourceNodes[:p.committedSourceLen], p.committedSourceRoot, rank,
		)
		if !found || node.kind != freeBitmapReservationSourceCommitted ||
			node.pageNumber != p.buffers.candidates[rank] ||
			node.required < p.payloadPages || node.required > p.privatePages {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan, page: node.pageNumber}
		}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationCapacityPlan) validateAttachmentScratch(
	pool *privatePagePool,
) freeBitmapCOWError {
	stage := &p.buffers.stage
	if stage.cow == nil || stage.pool == nil || stage.pool == pool ||
		len(stage.poolValidation) < p.privatePages || len(stage.arena) < p.privatePages ||
		len(stage.arenaBindings) < p.privatePages || len(stage.replacements) < p.verifiedLen ||
		len(stage.indexNodes) < p.indexRequired || len(stage.availableSlots) < p.privatePages {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceStagedArenaPages,
			required: p.privatePages, actual: len(stage.arena),
		}
	}
	if reservationSlicesOverlap(pool.slots, stage.arena[:p.privatePages]) ||
		reservationSlicesOverlap(p.buffers.arenaBindings[:p.privatePages], stage.arenaBindings[:p.privatePages]) ||
		reservationSlicesOverlap(p.buffers.indexNodes[:p.indexRequired], stage.indexNodes[:p.indexRequired]) ||
		reservationSlicesOverlap(p.buffers.availableSlots[:p.privatePages], stage.availableSlots[:p.privatePages]) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationCapacityPlan) validateVacantAttachmentScope(
	pool *privatePagePool,
	scope privatePageReservationScope,
) freeBitmapCOWError {
	if pool == nil || scope.pool != pool || pool.activeCheckpointID != 0 || pool.activeOperationID != 0 {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	anchor, poolProblem := pool.validateScope(scope)
	if poolProblem.failed() || anchor.scopeCapacity != p.privatePages || anchor.scopeBound != 0 ||
		anchor.scopeRoot != privatePagePoolNoIndex {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	vacant := anchor.scopeVacantHead
	member := anchor.scopeMemberHead
	mapped := 0
	for member != privatePagePoolNoIndex {
		if mapped >= p.privatePages || member < 0 || member >= len(pool.slots) {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
		}
		slot := &pool.slots[member]
		if vacant != member || !pool.validScopedVacancySlot(scope, member) {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor, page: slot.pageNumber}
		}
		vacant = slot.scopeVacantNext
		member = slot.scopeMemberNext
		mapped++
	}
	if mapped != p.privatePages || vacant != privatePagePoolNoIndex {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	return freeBitmapCOWError{}
}

func sameFreeBitmapReservationCommittedSource(left, right committedPageSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftSlice, leftIsSlice := left.(immutableSlicePageSource)
	rightSlice, rightIsSlice := right.(immutableSlicePageSource)
	if leftIsSlice || rightIsSlice {
		return leftIsSlice && rightIsSlice && leftSlice.pageCount == rightSlice.pageCount &&
			sealFreeBitmapReservationSlice(leftSlice.data) == sealFreeBitmapReservationSlice(rightSlice.data)
	}
	return sameComparableFreeBitmapReservationCommittedSource(left, right)
}

// All production sources other than immutableSlicePageSource are pointers and
// therefore comparable. An unknown non-comparable implementation is rejected
// as stale instead of allowing interface equality to panic.
func sameComparableFreeBitmapReservationCommittedSource(
	left, right committedPageSource,
) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return left == right
}

func freeBitmapReservationCOWScratchIsZero(cow *freeBitmapCOW) bool {
	if cow == nil || cow.singleInsertPage != ([1]uint32{}) || cow.pathLen != 0 || cow.candidate != 0 {
		return false
	}
	for index := range cow.frames {
		if cow.frames[index] != (freeBitmapPathFrame{}) ||
			cow.snapshots[index] != ([PageSize]byte{}) || cow.outputs[index] != ([PageSize]byte{}) ||
			cow.survives[index] || cow.cloneSlots[index] != 0 {
			return false
		}
	}
	return true
}

func sealFreeBitmapReservationCOW(cow *freeBitmapCOW) freeBitmapReservationCOWSeal {
	return freeBitmapReservationCOWSeal{
		committed: cow.committed, selectedTxn: cow.selectedTxn, pendingTxn: cow.pendingTxn,
		committedPageCount: cow.committedPageCount, pageCount: cow.pageCount,
		pageCountsDistinct: cow.pageCountsDistinct, root: cow.root, pool: cow.pool, scope: cow.scope,
		scoped: cow.scoped, scopeCapacity: cow.scopeCapacity, replacementLen: cow.replacementLen,
		candidateLen: cow.candidateLen, indexRoot: cow.indexRoot, indexLen: cow.indexLen,
		availableLen: cow.availableLen, plannedCandidateLen: cow.plannedCandidateLen,
		selectedCandidateLen: cow.selectedCandidateLen, candidateSelectionSet: cow.candidateSelectionSet,
		reservationPlanned: cow.reservationPlanned, payloadPageBudget: cow.payloadPageBudget,
		plannedRequiredPrivatePages: cow.plannedRequiredPrivatePages, mutationEpoch: cow.mutationEpoch,
		singleInsertPage: cow.singleInsertPage,
		arenaBindings:    sealFreeBitmapReservationSlice(cow.arenaBindings),
		replacements:     sealFreeBitmapReservationSlice(cow.replacements),
		candidates:       sealFreeBitmapReservationSlice(cow.candidates),
		indexNodes:       sealFreeBitmapReservationSlice(cow.indexNodes),
		availableSlots:   sealFreeBitmapReservationSlice(cow.availableSlots),
		verifiedPages:    sealFreeBitmapReservationSlice(cow.verifiedPages),
	}
}

func (seal freeBitmapReservationCOWSeal) matches(cow *freeBitmapCOW) bool {
	return cow != nil && sameFreeBitmapReservationCommittedSource(cow.committed, seal.committed) &&
		cow.selectedTxn == seal.selectedTxn && cow.pendingTxn == seal.pendingTxn &&
		cow.committedPageCount == seal.committedPageCount && cow.pageCount == seal.pageCount &&
		cow.pageCountsDistinct == seal.pageCountsDistinct && cow.root == seal.root && cow.pool == seal.pool &&
		cow.scope == seal.scope && cow.scoped == seal.scoped && cow.scopeCapacity == seal.scopeCapacity &&
		cow.replacementLen == seal.replacementLen && cow.candidateLen == seal.candidateLen &&
		cow.indexRoot == seal.indexRoot && cow.indexLen == seal.indexLen &&
		cow.availableLen == seal.availableLen && cow.plannedCandidateLen == seal.plannedCandidateLen &&
		cow.selectedCandidateLen == seal.selectedCandidateLen &&
		cow.candidateSelectionSet == seal.candidateSelectionSet &&
		cow.reservationPlanned == seal.reservationPlanned &&
		cow.payloadPageBudget == seal.payloadPageBudget &&
		cow.plannedRequiredPrivatePages == seal.plannedRequiredPrivatePages &&
		cow.mutationEpoch == seal.mutationEpoch && cow.singleInsertPage == seal.singleInsertPage &&
		seal.arenaBindings.matches(cow.arenaBindings) && seal.replacements.matches(cow.replacements) &&
		seal.candidates.matches(cow.candidates) && seal.indexNodes.matches(cow.indexNodes) &&
		seal.availableSlots.matches(cow.availableSlots) && seal.verifiedPages.matches(cow.verifiedPages) &&
		freeBitmapReservationCOWScratchIsZero(cow)
}

func sealFreeBitmapReservationBuffers(
	buffers freeBitmapReservationBuffers,
) freeBitmapReservationBufferSeal {
	stage := buffers.stage
	return freeBitmapReservationBufferSeal{
		pool: buffers.pool, poolValidation: sealFreeBitmapReservationSlice(buffers.poolValidation),
		arena:          sealFreeBitmapReservationSlice(buffers.arena),
		arenaBindings:  sealFreeBitmapReservationSlice(buffers.arenaBindings),
		candidates:     sealFreeBitmapReservationSlice(buffers.candidates),
		verifiedPages:  sealFreeBitmapReservationSlice(buffers.verifiedPages),
		replacements:   sealFreeBitmapReservationSlice(buffers.replacements),
		indexNodes:     sealFreeBitmapReservationSlice(buffers.indexNodes),
		availableSlots: sealFreeBitmapReservationSlice(buffers.availableSlots),
		sourceNodes:    sealFreeBitmapReservationSlice(buffers.sourceNodes), reclamation: buffers.reclamation,
		stageCow: stage.cow, stagePool: stage.pool,
		stageValidation:   sealFreeBitmapReservationSlice(stage.poolValidation),
		stageArena:        sealFreeBitmapReservationSlice(stage.arena),
		stageBindings:     sealFreeBitmapReservationSlice(stage.arenaBindings),
		stageReplacements: sealFreeBitmapReservationSlice(stage.replacements),
		stageIndex:        sealFreeBitmapReservationSlice(stage.indexNodes),
		stageAvailable:    sealFreeBitmapReservationSlice(stage.availableSlots),
	}
}

func (seal freeBitmapReservationBufferSeal) matches(buffers freeBitmapReservationBuffers) bool {
	stage := buffers.stage
	return buffers.pool == seal.pool && seal.poolValidation.matches(buffers.poolValidation) &&
		seal.arena.matches(buffers.arena) && seal.arenaBindings.matches(buffers.arenaBindings) &&
		seal.candidates.matches(buffers.candidates) && seal.verifiedPages.matches(buffers.verifiedPages) &&
		seal.replacements.matches(buffers.replacements) && seal.indexNodes.matches(buffers.indexNodes) &&
		seal.availableSlots.matches(buffers.availableSlots) && seal.sourceNodes.matches(buffers.sourceNodes) &&
		buffers.reclamation == seal.reclamation && stage.cow == seal.stageCow && stage.pool == seal.stagePool &&
		seal.stageValidation.matches(stage.poolValidation) && seal.stageArena.matches(stage.arena) &&
		seal.stageBindings.matches(stage.arenaBindings) &&
		seal.stageReplacements.matches(stage.replacements) && seal.stageIndex.matches(stage.indexNodes) &&
		seal.stageAvailable.matches(stage.availableSlots)
}

func sealFreeBitmapReservationPool(pool *privatePagePool) freeBitmapReservationPoolSeal {
	return freeBitmapReservationPoolSeal{
		self: pool.self, slots: sealFreeBitmapReservationSlice(pool.slots),
		committedPageCount: pool.committedPageCount, pendingPageCount: pool.pendingPageCount,
		pendingTxn: pool.pendingTxn, epoch: pool.epoch, generation: pool.generation,
		invalidationEpoch: pool.invalidationEpoch,
		mutationEpoch:     pool.mutationEpoch, abortMutationReserve: pool.abortMutationReserve,
		checkpointSequence: pool.checkpointSequence,
		activeCheckpointID: pool.activeCheckpointID, checkpointCleanup: pool.checkpointCleanup,
		checkpointSlotHead: pool.checkpointSlotHead, checkpointSlotCount: pool.checkpointSlotCount,
		checkpointIndexHead: pool.checkpointIndexHead, checkpointIndexCount: pool.checkpointIndexCount,
		checkpointScopeHead: pool.checkpointScopeHead, checkpointScopeCount: pool.checkpointScopeCount,
		operationSequence: pool.operationSequence, activeOperationID: pool.activeOperationID,
		operationStartEpoch: pool.operationStartEpoch, abortRequired: pool.abortRequired,
		scopeSequence: pool.scopeSequence, activeScopes: pool.activeScopes,
		unscopedVacantHead: pool.unscopedVacantHead, unscopedVacantTail: pool.unscopedVacantTail,
		unscopedVacantCount: pool.unscopedVacantCount, indexRoot: pool.indexRoot,
		coordinatorSessionID:         pool.coordinatorSessionID,
		coordinatorSessionGeneration: pool.coordinatorSessionGeneration,
		registeredWorkID:             pool.registeredWorkID,
		registeredWorkGeneration:     pool.registeredWorkGeneration,
		registeredWorkPhase:          pool.registeredWorkPhase,
		registeredWorkStartEpoch:     pool.registeredWorkStartEpoch,
		registeredWorkMutation:       pool.registeredWorkMutation,
		registeredWorkFence:          pool.registeredWorkFence,
		registeredScopeID:            pool.registeredScopeID,
		registeredScopeAnchor:        pool.registeredScopeAnchor,
		unacceptedScopes:             pool.unacceptedScopes,
		coordinatorCleanupPending:    pool.coordinatorCleanupPending,
	}
}

func (seal freeBitmapReservationPoolSeal) matches(pool *privatePagePool) bool {
	return pool != nil && pool.self == seal.self && seal.slots.matches(pool.slots) &&
		pool.committedPageCount == seal.committedPageCount && pool.pendingPageCount == seal.pendingPageCount &&
		pool.pendingTxn == seal.pendingTxn && pool.epoch == seal.epoch && pool.generation == seal.generation &&
		pool.invalidationEpoch == seal.invalidationEpoch &&
		pool.mutationEpoch == seal.mutationEpoch && pool.abortMutationReserve == seal.abortMutationReserve &&
		pool.checkpointSequence == seal.checkpointSequence &&
		pool.activeCheckpointID == seal.activeCheckpointID && pool.checkpointCleanup == seal.checkpointCleanup &&
		pool.checkpointSlotHead == seal.checkpointSlotHead && pool.checkpointSlotCount == seal.checkpointSlotCount &&
		pool.checkpointIndexHead == seal.checkpointIndexHead && pool.checkpointIndexCount == seal.checkpointIndexCount &&
		pool.checkpointScopeHead == seal.checkpointScopeHead && pool.checkpointScopeCount == seal.checkpointScopeCount &&
		pool.operationSequence == seal.operationSequence && pool.activeOperationID == seal.activeOperationID &&
		pool.operationStartEpoch == seal.operationStartEpoch && pool.abortRequired == seal.abortRequired &&
		pool.scopeSequence == seal.scopeSequence && pool.activeScopes == seal.activeScopes &&
		pool.unscopedVacantHead == seal.unscopedVacantHead && pool.unscopedVacantTail == seal.unscopedVacantTail &&
		pool.unscopedVacantCount == seal.unscopedVacantCount &&
		pool.indexRoot == seal.indexRoot &&
		pool.coordinatorSessionID == seal.coordinatorSessionID &&
		pool.coordinatorSessionGeneration == seal.coordinatorSessionGeneration &&
		pool.registeredWorkID == seal.registeredWorkID &&
		pool.registeredWorkGeneration == seal.registeredWorkGeneration &&
		pool.registeredWorkPhase == seal.registeredWorkPhase &&
		pool.registeredWorkStartEpoch == seal.registeredWorkStartEpoch &&
		pool.registeredWorkMutation == seal.registeredWorkMutation &&
		pool.registeredWorkFence == seal.registeredWorkFence &&
		pool.registeredScopeID == seal.registeredScopeID &&
		pool.registeredScopeAnchor == seal.registeredScopeAnchor &&
		pool.unacceptedScopes == seal.unacceptedScopes &&
		pool.coordinatorCleanupPending == seal.coordinatorCleanupPending
}

func sealFreeBitmapReservationTicket(
	ticket *freeBitmapReclamationTicket,
) freeBitmapReservationTicketSeal {
	return freeBitmapReservationTicketSeal{
		ticket: ticket, nonce: ticket.nonce, selectedTxn: ticket.selectedTxn,
		committedPageCount: ticket.committedPageCount, pendingPageCount: ticket.pendingPageCount,
		root: ticket.root, poolEpoch: ticket.poolEpoch, poolGeneration: ticket.poolGeneration,
		poolMutationEpoch: ticket.poolMutationEpoch, scopeID: ticket.scopeID, scopeAnchor: ticket.scopeAnchor,
		candidateFingerprint: ticket.candidateFingerprint, selectionID: ticket.selectionID,
		pages: sealFreeBitmapReservationSlice(ticket.pages), pageCount: ticket.pageCount,
		firstPage: ticket.firstPage, lastPage: ticket.lastPage, fingerprint: ticket.fingerprint,
	}
}

func (seal freeBitmapReservationTicketSeal) matches(ticket *freeBitmapReclamationTicket) bool {
	return ticket == seal.ticket && ticket != nil && ticket.nonce == seal.nonce &&
		ticket.selectedTxn == seal.selectedTxn && ticket.committedPageCount == seal.committedPageCount &&
		ticket.pendingPageCount == seal.pendingPageCount && ticket.root == seal.root &&
		ticket.poolEpoch == seal.poolEpoch && ticket.poolGeneration == seal.poolGeneration &&
		ticket.poolMutationEpoch == seal.poolMutationEpoch && ticket.scopeID == seal.scopeID &&
		ticket.scopeAnchor == seal.scopeAnchor && ticket.candidateFingerprint == seal.candidateFingerprint &&
		ticket.selectionID == seal.selectionID && seal.pages.matches(ticket.pages) &&
		ticket.pageCount == seal.pageCount && ticket.firstPage == seal.firstPage &&
		ticket.lastPage == seal.lastPage && ticket.fingerprint == seal.fingerprint
}

func sealFreeBitmapReservationProof(proof *freeBitmapReclamationProof) freeBitmapReservationProofSeal {
	return freeBitmapReservationProofSeal{
		proof: proof, ticket: proof.ticket, nonce: proof.nonce, selectionID: proof.selectionID,
		pages: sealFreeBitmapReservationSlice(proof.pages), pageCount: proof.pageCount,
		firstPage: proof.firstPage, lastPage: proof.lastPage, fingerprint: proof.fingerprint,
	}
}

func (seal freeBitmapReservationProofSeal) matches(proof *freeBitmapReclamationProof) bool {
	return proof == seal.proof && proof != nil && proof.ticket == seal.ticket && proof.nonce == seal.nonce &&
		proof.selectionID == seal.selectionID && seal.pages.matches(proof.pages) &&
		proof.pageCount == seal.pageCount && proof.firstPage == seal.firstPage &&
		proof.lastPage == seal.lastPage && proof.fingerprint == seal.fingerprint
}

func staleFreeBitmapReservationBind() freeBitmapCOWError {
	return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
}

func freeBitmapReservationFingerprintBool(hash uint64, value bool) uint64 {
	if value {
		return freeBitmapFingerprintUint64(hash, 1)
	}
	return freeBitmapFingerprintUint64(hash, 0)
}

func freeBitmapReservationScopeFingerprint(
	pool *privatePagePool,
	scope privatePageReservationScope,
) uint64 {
	hash := freeBitmapFingerprintUint64(1469598103934665603, scope.id)
	member, capacity, problem := pool.scopeMemberStart(scope)
	if problem.failed() {
		return freeBitmapFingerprintUint64(hash, ^uint64(0))
	}
	visited := 0
	for member != privatePagePoolNoIndex {
		if visited >= capacity || member < 0 || member >= len(pool.slots) {
			return freeBitmapFingerprintUint64(hash, ^uint64(0)-1)
		}
		index := member
		slot := &pool.slots[index]
		hash = freeBitmapFingerprintUint64(hash, uint64(index+1))
		for _, value := range []uint64{
			uint64(slot.pageNumber), uint64(slot.authorization), slot.scopeID,
			uint64(slot.scopeAnchorIndex + 1), uint64(slot.scopeVacantNext + 1), uint64(slot.scopeMemberNext + 1),
			uint64(slot.unscopedNext + 1), uint64(slot.unscopedPrevious + 1),
			uint64(slot.scopeRoot + 1), uint64(slot.scopeVacantHead + 1), uint64(slot.scopeMemberHead + 1),
			uint64(slot.scopeCapacity), uint64(slot.scopeBound), slot.scopeGeneration,
			slot.scopeSuccessor, uint64(slot.state),
			uint64(slot.owner), uint64(slot.origin), slot.pendingTxn, slot.generation,
			slot.epoch, uint64(slot.committedOrigin), slot.checkpointID,
			uint64(slot.checkpointSlotNext + 1),
			uint64(slot.checkpointPageNumber), uint64(slot.checkpointAuthorization),
			slot.checkpointScopeID, uint64(slot.checkpointScopeAnchorIndex + 1),
			uint64(slot.checkpointScopeVacantNext + 1), uint64(slot.checkpointState),
			uint64(slot.checkpointOwner), uint64(slot.checkpointOrigin), slot.checkpointPendingTxn,
			slot.checkpointGeneration, uint64(slot.checkpointCommittedOrigin),
			uint64(slot.pendingReturnState), slot.indexCheckpointID, uint64(slot.indexCheckpointNext + 1),
			uint64(slot.checkpointIndexLeft + 1), uint64(slot.checkpointIndexRight + 1),
			uint64(int64(slot.checkpointIndexHeight) + 1), slot.checkpointIndexFree,
			slot.checkpointIndexInUse, uint64(slot.checkpointScopeLeft + 1),
			uint64(slot.checkpointScopeRight + 1), uint64(int64(slot.checkpointScopeHeight) + 1),
			slot.checkpointScopeFree, slot.checkpointScopeInUse, slot.scopeCheckpointID,
			uint64(slot.scopeCheckpointNext + 1),
			uint64(slot.checkpointScopeRoot + 1), uint64(slot.checkpointScopeVacantHead + 1),
			uint64(slot.checkpointScopeBound), uint64(slot.indexLeft + 1), uint64(slot.indexRight + 1),
			uint64(int64(slot.indexHeight) + 1), slot.indexFree, slot.indexInUse,
			uint64(slot.scopeLeft + 1), uint64(slot.scopeRight + 1),
			uint64(int64(slot.scopeHeight) + 1), slot.scopeFree, slot.scopeInUse,
		} {
			hash = freeBitmapFingerprintUint64(hash, value)
		}
		for _, value := range []bool{
			slot.bound, slot.scopeAnchor, slot.inUse, slot.checkpointBound,
			slot.checkpointScopeAnchor, slot.checkpointInUse, slot.batchMarked,
			slot.scopeSealed, slot.successorConsumed,
		} {
			hash = freeBitmapReservationFingerprintBool(hash, value)
		}
		hash = freeBitmapFingerprintBytes(hash, slot.bytes[:])
		member = slot.scopeMemberNext
		visited++
	}
	hash = freeBitmapFingerprintUint64(hash, uint64(visited))
	return hash
}

func captureFreeBitmapReservationBindSeal(
	p *freeBitmapReservationAttachment,
	proof *freeBitmapReclamationProof,
) (freeBitmapReservationBindSeal, freeBitmapCOWError) {
	// Establish every signed bound and required pointer before slicing or
	// dereferencing caller-controlled attachment state.
	if p == nil || proof == nil || p.privatePages <= 0 || p.payloadPages < 0 ||
		p.verifiedLen < 0 || p.indexRequired < 0 || p.committedSourceLen < 0 ||
		p.selectedTxn == 0 || p.selectedTxn == ^uint64(0) ||
		p.committedPageCount < 2 || p.committedPageCount > MaxPageCount ||
		(p.root != 0 && (p.root < 2 || uint64(p.root) >= p.committedPageCount)) ||
		p.buffers.pool != nil || p.buffers.arena != nil || p.cow.pool == nil ||
		p.scope.pool == nil || p.scope.pool != p.cow.pool || p.cow.pool.self != p.cow.pool ||
		p.buffers.reclamation == nil || p.reclamationRequest.ticket == nil ||
		p.buffers.reclamation != p.reclamationRequest.ticket || proof.ticket == nil ||
		proof.ticket != p.reclamationRequest.ticket || proof.pageCount < 0 {
		return freeBitmapReservationBindSeal{}, staleFreeBitmapReservationBind()
	}
	if p.committedSourceLen > len(p.buffers.candidates) ||
		p.committedSourceLen > len(p.buffers.sourceNodes) || p.verifiedLen > len(p.buffers.verifiedPages) ||
		p.privatePages > len(p.buffers.poolValidation) || p.privatePages > len(p.buffers.arenaBindings) ||
		p.privatePages > len(p.buffers.availableSlots) || p.verifiedLen > len(p.buffers.replacements) ||
		p.indexRequired > len(p.buffers.indexNodes) || len(p.cow.arenaBindings) != p.privatePages ||
		len(p.cow.replacements) != p.verifiedLen || len(p.cow.candidates) != p.committedSourceLen ||
		len(p.cow.indexNodes) != p.indexRequired || len(p.cow.availableSlots) != p.privatePages ||
		len(p.cow.verifiedPages) != p.verifiedLen ||
		p.scope.anchor < 0 || p.scope.anchor >= len(p.cow.pool.slots) ||
		proof.pageCount != len(proof.pages) || proof.ticket.pageCount != len(proof.ticket.pages) {
		return freeBitmapReservationBindSeal{}, staleFreeBitmapReservationBind()
	}
	if !sameFreeBitmapReservationCommittedSource(p.committed, p.cow.committed) ||
		p.cow.selectedTxn != p.selectedTxn || p.cow.sourceTxn != p.sourceTxn ||
		p.cow.pendingTxn != p.pendingTxn ||
		p.cow.committedPageCount != p.poolCommittedPages ||
		p.cow.pageCount != p.reclamationRequest.pendingPageCount || p.cow.root != p.root ||
		p.cow.scope != p.scope || !p.cow.scoped || p.cow.scopeCapacity != p.privatePages ||
		p.cow.plannedCandidateLen != p.committedSourceLen || !p.cow.reservationPlanned ||
		p.cow.payloadPageBudget != p.payloadPages ||
		p.cow.plannedRequiredPrivatePages != p.privatePages ||
		!freeBitmapReservationCOWScratchIsZero(&p.cow) || p.scope.id == 0 ||
		p.scope.poolEpoch != p.cow.pool.epoch || p.scope.pendingTxn != p.cow.pendingTxn ||
		p.reclamationRequest.nonce == 0 || p.reclamationRequest.selectedTxn != p.selectedTxn ||
		p.reclamationRequest.committedPageCount != p.committedPageCount ||
		p.reclamationRequest.pendingPageCount != p.cow.pageCount ||
		p.reclamationRequest.root != p.root || p.reclamationRequest.poolEpoch != p.scope.poolEpoch ||
		p.reclamationRequest.poolGeneration != p.poolGeneration ||
		p.reclamationRequest.poolMutationEpoch != p.poolMutationEpoch ||
		p.reclamationRequest.scopeID != p.scope.id ||
		p.reclamationRequest.scopeAnchor != p.scope.anchor {
		return freeBitmapReservationBindSeal{}, staleFreeBitmapReservationBind()
	}
	status, poolProblem := p.cow.pool.status()
	if poolProblem.failed() || status.committedPageCount != p.poolCommittedPages ||
		status.pendingPageCount != p.reclamationRequest.pendingPageCount ||
		status.pendingTxn != p.cow.pendingTxn || status.generation != p.poolGeneration ||
		status.mutationEpoch != p.poolMutationEpoch ||
		p.reclamationRequest.candidateFingerprint != freeBitmapPageFingerprint(p.cow.candidates) ||
		p.capacityFingerprint != freeBitmapCapacityFingerprint(
			p.buffers.candidates[:p.committedSourceLen], p.buffers.verifiedPages[:p.verifiedLen],
		) || p.sourceFingerprint != freeBitmapSourceFingerprint(p.buffers.sourceNodes[:p.committedSourceLen]) ||
		p.cowScratchFingerprint != freeBitmapReservationCOWFingerprint(&p.cow) {
		return freeBitmapReservationBindSeal{}, staleFreeBitmapReservationBind()
	}
	ticket := proof.ticket
	if ticket.state.Load() != 2 || ticket.nonce != p.reclamationRequest.nonce ||
		ticket.selectedTxn != p.reclamationRequest.selectedTxn ||
		ticket.committedPageCount != p.reclamationRequest.committedPageCount ||
		ticket.pendingPageCount != p.reclamationRequest.pendingPageCount ||
		ticket.root != p.reclamationRequest.root || ticket.poolEpoch != p.reclamationRequest.poolEpoch ||
		ticket.poolGeneration != p.reclamationRequest.poolGeneration ||
		ticket.poolMutationEpoch != p.reclamationRequest.poolMutationEpoch ||
		ticket.scopeID != p.reclamationRequest.scopeID ||
		ticket.scopeAnchor != p.reclamationRequest.scopeAnchor ||
		ticket.candidateFingerprint != p.reclamationRequest.candidateFingerprint ||
		ticket.selectionID != proof.selectionID || ticket.pageCount != proof.pageCount ||
		ticket.firstPage != proof.firstPage || ticket.lastPage != proof.lastPage ||
		ticket.fingerprint != proof.fingerprint ||
		sealFreeBitmapReservationSlice(ticket.pages) != sealFreeBitmapReservationSlice(proof.pages) ||
		((len(proof.pages) == 0) != (proof.selectionID == 0)) {
		return freeBitmapReservationBindSeal{}, staleFreeBitmapReservationBind()
	}
	if problem := p.validateStageBuffers(proof.pages); problem.failed() {
		return freeBitmapReservationBindSeal{}, problem
	}
	if problem := p.validateCommittedSources(); problem.failed() {
		return freeBitmapReservationBindSeal{}, problem
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return freeBitmapReservationBindSeal{}, problem
	}
	if problem := p.validateVacantAttachmentScope(p.cow.pool, p.scope); problem.failed() {
		return freeBitmapReservationBindSeal{}, problem
	}
	return freeBitmapReservationBindSeal{
		committed: p.committed, selectedTxn: p.selectedTxn, committedPageCount: p.committedPageCount,
		root: p.root, committedSourceRoot: p.committedSourceRoot,
		committedSourceLen: p.committedSourceLen, privatePages: p.privatePages,
		payloadPages: p.payloadPages, verifiedLen: p.verifiedLen, indexRequired: p.indexRequired,
		capacityFingerprint: p.capacityFingerprint, sourceFingerprint: p.sourceFingerprint,
		buffers: sealFreeBitmapReservationBuffers(p.buffers), cow: sealFreeBitmapReservationCOW(&p.cow),
		scope: p.scope, poolGeneration: p.poolGeneration, poolMutationEpoch: p.poolMutationEpoch,
		request: p.reclamationRequest, cowFingerprint: p.cowScratchFingerprint,
		poolValidationFingerprint: freeBitmapPageFingerprint(p.buffers.poolValidation[:p.privatePages]),
		scopeFingerprint:          freeBitmapReservationScopeFingerprint(p.cow.pool, p.scope),
		pool:                      sealFreeBitmapReservationPool(p.cow.pool), ticket: sealFreeBitmapReservationTicket(ticket),
		proof: sealFreeBitmapReservationProof(proof),
	}, freeBitmapCOWError{}
}

func (seal *freeBitmapReservationBindSeal) validateAfterCallback(
	p *freeBitmapReservationAttachment,
	proof *freeBitmapReclamationProof,
) ([]uint32, freeBitmapCOWError) {
	// This first phase reads only scalars, pointers, and slice headers. No
	// callback-controlled length is sliced and no callback-controlled pointer is
	// dereferenced until its exact pre-callback identity has been restored.
	if seal == nil || p == nil || proof == nil || p.selectedTxn != seal.selectedTxn ||
		p.committedPageCount != seal.committedPageCount || p.root != seal.root ||
		p.committedSourceRoot != seal.committedSourceRoot ||
		p.committedSourceLen != seal.committedSourceLen || p.privatePages != seal.privatePages ||
		p.payloadPages != seal.payloadPages || p.verifiedLen != seal.verifiedLen ||
		p.indexRequired != seal.indexRequired || p.capacityFingerprint != seal.capacityFingerprint ||
		p.sourceFingerprint != seal.sourceFingerprint || p.scope != seal.scope ||
		p.poolGeneration != seal.poolGeneration || p.poolMutationEpoch != seal.poolMutationEpoch ||
		p.reclamationRequest != seal.request || p.cowScratchFingerprint != seal.cowFingerprint ||
		!sameFreeBitmapReservationCommittedSource(p.committed, seal.committed) ||
		!seal.buffers.matches(p.buffers) || !seal.cow.matches(&p.cow) ||
		p.cow.pool != seal.pool.self || p.scope.pool != seal.pool.self ||
		!seal.pool.matches(p.cow.pool) || !seal.ticket.matches(seal.ticket.ticket) ||
		!seal.proof.matches(proof) || seal.ticket.ticket.state.Load() != 3 {
		return nil, staleFreeBitmapReservationBind()
	}
	if p.committedSourceLen < 0 || p.privatePages <= 0 || p.verifiedLen < 0 || p.indexRequired < 0 ||
		p.committedSourceLen > len(p.buffers.candidates) ||
		p.committedSourceLen > len(p.buffers.sourceNodes) || p.verifiedLen > len(p.buffers.verifiedPages) ||
		p.privatePages > len(p.buffers.poolValidation) {
		return nil, staleFreeBitmapReservationBind()
	}
	// Slice bounds and identities are now sealed. Content checks compare live
	// bytes directly with the stack commitments, never mutable cache to cache.
	if freeBitmapCapacityFingerprint(
		p.buffers.candidates[:p.committedSourceLen], p.buffers.verifiedPages[:p.verifiedLen],
	) != seal.capacityFingerprint ||
		freeBitmapSourceFingerprint(p.buffers.sourceNodes[:p.committedSourceLen]) != seal.sourceFingerprint ||
		freeBitmapReservationCOWFingerprint(&p.cow) != seal.cowFingerprint ||
		freeBitmapPageFingerprint(p.buffers.poolValidation[:p.privatePages]) != seal.poolValidationFingerprint ||
		freeBitmapReservationScopeFingerprint(p.cow.pool, p.scope) != seal.scopeFingerprint ||
		freeBitmapPageFingerprint(proof.pages) != seal.proof.fingerprint {
		return nil, staleFreeBitmapReservationBind()
	}
	if problem := p.validateStageBuffers(proof.pages); problem.failed() {
		return nil, problem
	}
	reclaimed, problem := p.validateConsumedReclamationProof(proof)
	if problem.failed() {
		return nil, problem
	}
	if problem = p.validateCommittedSources(); problem.failed() {
		return nil, problem
	}
	status, poolProblem := p.cow.pool.status()
	if poolProblem.failed() || status.committedPageCount != seal.pool.committedPageCount ||
		status.pendingPageCount != seal.pool.pendingPageCount || status.pendingTxn != seal.pool.pendingTxn ||
		status.generation != seal.pool.generation || status.mutationEpoch != seal.pool.mutationEpoch {
		return nil, staleFreeBitmapReservationBind()
	}
	if problem = p.cow.validateScopedBindings(); problem.failed() {
		return nil, staleFreeBitmapReservationBind()
	}
	if problem = p.validateVacantAttachmentScope(p.cow.pool, p.scope); problem.failed() {
		return nil, staleFreeBitmapReservationBind()
	}
	return reclaimed, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) consumeReclamationProof(
	proof *freeBitmapReclamationProof,
) ([]uint32, freeBitmapCOWError) {
	if proof == nil || proof.ticket == nil || proof.ticket != p.reclamationRequest.ticket ||
		proof.nonce != p.reclamationRequest.nonce || ((len(proof.pages) == 0) != (proof.selectionID == 0)) {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if !proof.ticket.state.CompareAndSwap(2, 3) {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	ticket := proof.ticket
	firstPage, lastPage := uint32(0), uint32(0)
	if len(proof.pages) != 0 {
		firstPage, lastPage = proof.pages[0], proof.pages[len(proof.pages)-1]
	}
	if ticket.nonce != proof.nonce || ticket.selectionID != proof.selectionID ||
		ticket.selectedTxn != p.reclamationRequest.selectedTxn ||
		ticket.committedPageCount != p.reclamationRequest.committedPageCount ||
		ticket.pendingPageCount != p.reclamationRequest.pendingPageCount ||
		ticket.root != p.reclamationRequest.root || ticket.poolEpoch != p.reclamationRequest.poolEpoch ||
		ticket.poolGeneration != p.reclamationRequest.poolGeneration ||
		ticket.poolMutationEpoch != p.reclamationRequest.poolMutationEpoch ||
		ticket.scopeID != p.reclamationRequest.scopeID ||
		ticket.scopeAnchor != p.reclamationRequest.scopeAnchor ||
		ticket.candidateFingerprint != p.reclamationRequest.candidateFingerprint ||
		ticket.pageCount != proof.pageCount || ticket.pageCount != len(proof.pages) ||
		ticket.firstPage != proof.firstPage || ticket.lastPage != proof.lastPage ||
		proof.firstPage != firstPage || proof.lastPage != lastPage ||
		ticket.fingerprint != proof.fingerprint ||
		freeBitmapPageFingerprint(proof.pages) != proof.fingerprint {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	return proof.pages, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) validateConsumedReclamationProof(
	proof *freeBitmapReclamationProof,
) ([]uint32, freeBitmapCOWError) {
	if proof == nil || proof.ticket == nil || proof.ticket != p.reclamationRequest.ticket ||
		proof.nonce != p.reclamationRequest.nonce || ((len(proof.pages) == 0) != (proof.selectionID == 0)) {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	ticket := proof.ticket
	firstPage, lastPage := uint32(0), uint32(0)
	if len(proof.pages) != 0 {
		firstPage, lastPage = proof.pages[0], proof.pages[len(proof.pages)-1]
	}
	if ticket.state.Load() != 3 || ticket.nonce != proof.nonce || ticket.selectionID != proof.selectionID ||
		ticket.selectedTxn != p.reclamationRequest.selectedTxn ||
		ticket.committedPageCount != p.reclamationRequest.committedPageCount ||
		ticket.pendingPageCount != p.reclamationRequest.pendingPageCount ||
		ticket.root != p.reclamationRequest.root || ticket.poolEpoch != p.reclamationRequest.poolEpoch ||
		ticket.poolGeneration != p.reclamationRequest.poolGeneration ||
		ticket.poolMutationEpoch != p.reclamationRequest.poolMutationEpoch ||
		ticket.scopeID != p.reclamationRequest.scopeID ||
		ticket.scopeAnchor != p.reclamationRequest.scopeAnchor ||
		ticket.candidateFingerprint != p.reclamationRequest.candidateFingerprint ||
		ticket.pageCount != proof.pageCount || ticket.pageCount != len(proof.pages) ||
		ticket.firstPage != proof.firstPage || ticket.lastPage != proof.lastPage ||
		proof.firstPage != firstPage || proof.lastPage != lastPage ||
		ticket.fingerprint != proof.fingerprint ||
		freeBitmapPageFingerprint(proof.pages) != proof.fingerprint {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	return proof.pages, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) validateCommittedSources() freeBitmapCOWError {
	if p.committedSourceLen != p.cow.plannedCandidateLen || p.committedSourceLen < 0 ||
		p.committedSourceLen > len(p.buffers.sourceNodes) ||
		freeBitmapSourceFingerprint(p.buffers.sourceNodes[:p.committedSourceLen]) != p.sourceFingerprint ||
		freeBitmapReservationCOWFingerprint(&p.cow) != p.cowScratchFingerprint {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if p.committedSourceLen == 0 {
		if p.committedSourceRoot != bitmapCOWNoIndex {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
		}
		return freeBitmapCOWError{}
	}
	if p.committedSourceRoot < 0 || p.committedSourceRoot >= p.committedSourceLen ||
		p.buffers.sourceNodes[p.committedSourceRoot].subtreeCount != uint32(p.committedSourceLen) {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	for rank := 0; rank < p.committedSourceLen; rank++ {
		node, found := freeBitmapSourceAt(
			p.buffers.sourceNodes[:p.committedSourceLen], p.committedSourceRoot, rank,
		)
		if !found || node.kind != freeBitmapReservationSourceCommitted ||
			node.pageNumber != p.cow.candidates[rank] ||
			node.required < p.payloadPages || node.required > p.privatePages {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan, page: node.pageNumber}
		}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) validateStageBuffers(reclaimed []uint32) freeBitmapCOWError {
	stage := &p.buffers.stage
	verifiedLen := len(p.cow.verifiedPages)
	if stage.cow == nil || stage.cow == &p.cow || stage.pool == nil || stage.pool == p.cow.pool ||
		len(stage.poolValidation) < p.privatePages || len(stage.arena) < p.privatePages ||
		len(stage.arenaBindings) < p.privatePages || len(stage.replacements) < verifiedLen ||
		len(stage.indexNodes) < len(p.cow.indexNodes) || len(stage.availableSlots) < p.privatePages {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceStagedArenaPages,
			required: p.privatePages, actual: len(stage.arena),
		}
	}
	if problem := validateFreeBitmapReservationProofAliases(p.buffers, reclaimed); problem.failed() {
		return problem
	}
	if reservationSlicesOverlap(p.cow.pool.slots, stage.arena[:p.privatePages]) ||
		reservationSlicesOverlap(p.cow.arenaBindings, stage.arenaBindings[:p.privatePages]) ||
		reservationSlicesOverlap(p.cow.indexNodes, stage.indexNodes[:len(p.cow.indexNodes)]) ||
		reservationSlicesOverlap(p.cow.availableSlots, stage.availableSlots[:p.privatePages]) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) validateSelectedPages(
	reclaimed []uint32,
	selected, selectedCommitted int,
) freeBitmapCOWError {
	if selected != p.privatePages {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	expectedCommitted, expectedAppended, problem := p.reservationSourceCounts(len(reclaimed))
	if problem.failed() {
		return problem
	}
	if selectedCommitted != expectedCommitted {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	committedRank, reclaimedRank, appended := 0, 0, 0
	pages := p.buffers.stage.poolValidation[:selected]
	for _, pageNumber := range pages {
		var expected uint32
		switch {
		case committedRank < expectedCommitted &&
			(reclaimedRank == len(reclaimed) || p.cow.candidates[committedRank] < reclaimed[reclaimedRank]):
			expected = p.cow.candidates[committedRank]
			committedRank++
		case reclaimedRank < len(reclaimed):
			expected = reclaimed[reclaimedRank]
			reclaimedRank++
		default:
			page64 := p.cow.pageCount + uint64(appended)
			if page64 > uint64(^uint32(0)) {
				return freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
			}
			expected = uint32(page64)
			appended++
		}
		if pageNumber != expected {
			return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan, previousPage: expected, page: pageNumber}
		}
	}
	if committedRank != expectedCommitted || reclaimedRank != len(reclaimed) || appended != expectedAppended {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) buildReclaimedSource(
	reclaimed []uint32,
) (int, freeBitmapCOWError) {
	if _, _, problem := p.reservationSourceCounts(len(reclaimed)); problem.failed() {
		return 0, problem
	}
	needed, ok := checkedIntAdd(p.committedSourceLen, len(reclaimed))
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	if needed > len(p.buffers.sourceNodes) {
		return 0, freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceSourceNodes,
			required: needed, actual: len(p.buffers.sourceNodes),
		}
	}
	previous := uint32(0)
	for index, pageNumber := range reclaimed {
		if pageNumber < 2 || uint64(pageNumber) >= p.cow.committedPageCount {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		if index != 0 && pageNumber <= previous {
			code := freeBitmapCOWErrCandidateOrderRegression
			if pageNumber == previous {
				code = freeBitmapCOWErrDuplicateCandidate
			}
			return 0, freeBitmapCOWError{code: code, previousPage: previous, page: pageNumber}
		}
		if freeBitmapSourceFind(
			p.buffers.sourceNodes[:p.committedSourceLen], p.committedSourceRoot, pageNumber,
		) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageConflict, page: pageNumber}
		}
		previous = pageNumber
	}
	root := bitmapCOWNoIndex
	for index, pageNumber := range reclaimed {
		nodeIndex := p.committedSourceLen + index
		root = freeBitmapSourceInsert(
			p.buffers.sourceNodes, root, nodeIndex, pageNumber,
			freeBitmapReservationSourceReclaimed, 0,
		)
	}
	return root, freeBitmapCOWError{}
}

// reservationSourceCounts reserves every proven reclaimed page in the exact
// private scope before it considers ordinary bitmap candidates. Reclaimed
// pages are already safe to reuse and must not be silently omitted from the
// draft; ordinary candidates fill only the remaining capacity.
func (p *freeBitmapReservationAttachment) reservationSourceCounts(
	reclaimedCount int,
) (selectedCommitted, selectedAppended int, problem freeBitmapCOWError) {
	if p == nil || reclaimedCount < 0 || p.privatePages < 0 || p.committedSourceLen < 0 {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if reclaimedCount > p.privatePages {
		return 0, 0, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsufficientResourceBudget,
			resource: freeBitmapResourceArenaPages,
			required: reclaimedCount,
			actual:   p.privatePages,
		}
	}
	remaining := p.privatePages - reclaimedCount
	selectedCommitted = p.committedSourceLen
	if selectedCommitted > remaining {
		selectedCommitted = remaining
	}
	return selectedCommitted, remaining - selectedCommitted, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) selectPhysicalPages(
	reclaimed []uint32,
	reclaimedRoot int,
) (int, int, freeBitmapCOWError) {
	selectedCommitted, selectedAppended, problem := p.reservationSourceCounts(len(reclaimed))
	if problem.failed() {
		return 0, 0, problem
	}
	selected, committedRank, reclaimedRank, appended := 0, 0, 0, 0
	stagePages := p.buffers.stage.poolValidation[:p.privatePages]
	clear(stagePages)
	for {
		if selected == p.privatePages {
			if committedRank != selectedCommitted || reclaimedRank != len(reclaimed) ||
				appended != selectedAppended {
				return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
			}
			return selected, committedRank, freeBitmapCOWError{}
		}
		committedNode, haveCommitted := freeBitmapReservationSourceNode{}, false
		if committedRank < selectedCommitted {
			committedNode, haveCommitted = freeBitmapSourceAt(
				p.buffers.sourceNodes[:p.committedSourceLen], p.committedSourceRoot, committedRank,
			)
		}
		reclaimedNode, haveReclaimed := freeBitmapReservationSourceNode{}, false
		if reclaimedRank < len(reclaimed) && reclaimedRoot != bitmapCOWNoIndex {
			reclaimedNode, haveReclaimed = freeBitmapSourceAt(p.buffers.sourceNodes, reclaimedRoot, reclaimedRank)
		}
		if haveCommitted && (!haveReclaimed || committedNode.pageNumber < reclaimedNode.pageNumber) {
			stagePages[selected] = committedNode.pageNumber
			committedRank++
			selected++
			continue
		}
		if haveReclaimed {
			stagePages[selected] = reclaimedNode.pageNumber
			reclaimedRank++
			selected++
			continue
		}
		if appended == selectedAppended || p.cow.pageCount > MaxPageCount ||
			uint64(selectedAppended-appended) > MaxPageCount-p.cow.pageCount {
			return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
		}
		page64 := p.cow.pageCount + uint64(appended)
		if page64 > uint64(^uint32(0)) {
			return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
		}
		stagePages[selected] = uint32(page64)
		appended++
		selected++
	}
}

func reservationPageAuthorization(
	pageNumber uint32,
	committed []uint32,
	committedPageCount uint64,
) privatePageAuthorization {
	left, right := 0, len(committed)
	for left < right {
		middle := left + (right-left)/2
		switch {
		case pageNumber < committed[middle]:
			right = middle
		case pageNumber > committed[middle]:
			left = middle + 1
		default:
			return privatePageCommittedFree
		}
	}
	if uint64(pageNumber) >= committedPageCount {
		return privatePageAppended
	}
	return privatePageReclaimed
}

func (p *freeBitmapReservationAttachment) buildShadow(
	selected, selectedCommitted int,
) (*freeBitmapCOW, freeBitmapCOWError) {
	stage := &p.buffers.stage
	if poolProblem := initVacantPrivatePagePool(
		stage.pool, stage.arena[:p.privatePages],
		p.cow.committedPageCount, p.cow.pageCount, p.cow.pendingTxn,
	); poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	// A successful source callback may have touched caller-owned stage bytes.
	// Every stage resource consumed below is rebuilt; replacements need an
	// explicit clear because a zero replacement prefix otherwise leaves its
	// unused backing entries unchanged and they are copied into live scratch.
	clear(stage.replacements[:len(p.cow.replacements)])
	scope, poolProblem := stage.pool.reserveScope(p.privatePages)
	if poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	ledger := freeBitmapCOWLedger{
		arena: stage.arena[:p.privatePages], arenaBindings: stage.arenaBindings[:p.privatePages],
		replacements:   stage.replacements[:len(p.cow.replacements)],
		candidates:     p.cow.candidates[:p.cow.plannedCandidateLen],
		indexNodes:     stage.indexNodes[:len(p.cow.indexNodes)],
		availableSlots: stage.availableSlots[:p.privatePages],
		verifiedPages:  p.cow.verifiedPages, plannedCandidateLen: p.cow.plannedCandidateLen,
		reservationPlanned: true, payloadPageBudget: p.payloadPages, plannedPrivatePages: p.privatePages,
	}
	problem := initializeFreeBitmapCOWWithScopedPoolTransactions(
		stage.cow,
		// Every committed path needed by this exact plan is already present in
		// verifiedPages. A nil source makes any missing verification an internal
		// error instead of permitting another callback after the final access
		// check in bind.
		nil, p.cow.selectedTxn, p.cow.sourceTxn, p.cow.pendingTxn,
		p.cow.pageCount, p.cow.root,
		stage.pool, scope, ledger,
	)
	if problem.failed() {
		return nil, problem
	}
	shadow := stage.cow
	if problem = shadow.selectPlannedCandidatePrefix(0); problem.failed() {
		return nil, problem
	}
	checkpoint, poolProblem := stage.pool.begin()
	if poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	selectedCandidates := p.cow.candidates[:selectedCommitted]
	for index := 0; index < selected; index++ {
		pageNumber := stage.poolValidation[index]
		authorization := reservationPageAuthorization(pageNumber, selectedCandidates, p.cow.committedPageCount)
		if _, poolProblem = stage.pool.bindPage(checkpoint, scope, pageNumber, authorization); poolProblem.failed() {
			return nil, rollbackDisposableBitmapShadow(stage.pool, checkpoint, bitmapPoolError(poolProblem))
		}
	}
	if poolProblem = stage.pool.commit(checkpoint); poolProblem.failed() {
		return nil, rollbackDisposableBitmapShadow(stage.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	if problem = shadow.synchronizeScopedBindingsForCandidatePrefix(scope, selectedCommitted); problem.failed() {
		return nil, problem
	}
	if problem = shadow.materializePlannedEmptyRoot(); problem.failed() {
		return nil, problem
	}
	if problem = shadow.applyPlannedReservationAfterAccess(); problem.failed() {
		return nil, problem
	}
	if shadow.candidateLen != selectedCommitted || shadow.availableLen < p.payloadPages {
		return nil, freeBitmapCOWError{code: freeBitmapCOWErrPrivateArenaExhausted}
	}
	if problem = shadow.validateScopedBindings(); problem.failed() {
		return nil, problem
	}
	return shadow, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) preflightRealApply(
	shadow *freeBitmapCOW,
	selected int,
) (privatePagePoolCheckpoint, freeBitmapCOWError) {
	pool := p.cow.pool
	if shadow == nil || selected < 0 || selected > p.privatePages {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	status, poolProblem := pool.status()
	if poolProblem.failed() || status.pendingTxn != p.cow.pendingTxn ||
		status.pendingPageCount != p.reclamationRequest.pendingPageCount ||
		pool.epoch != p.reclamationRequest.poolEpoch ||
		status.generation != p.poolGeneration || status.mutationEpoch != p.poolMutationEpoch {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	ticket := p.reclamationRequest.ticket
	if ticket == nil {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	firstPage, lastPage := uint32(0), uint32(0)
	if len(ticket.pages) != 0 {
		firstPage, lastPage = ticket.pages[0], ticket.pages[len(ticket.pages)-1]
	}
	if ticket.state.Load() != 3 || ticket.nonce != p.reclamationRequest.nonce ||
		((ticket.pageCount == 0) != (ticket.selectionID == 0)) || ticket.pageCount != len(ticket.pages) ||
		ticket.firstPage != firstPage || ticket.lastPage != lastPage ||
		freeBitmapPageFingerprint(ticket.pages) != ticket.fingerprint ||
		ticket.poolEpoch != p.reclamationRequest.poolEpoch ||
		ticket.poolGeneration != p.reclamationRequest.poolGeneration ||
		ticket.poolMutationEpoch != p.reclamationRequest.poolMutationEpoch ||
		ticket.scopeID != p.reclamationRequest.scopeID ||
		ticket.scopeAnchor != p.reclamationRequest.scopeAnchor ||
		ticket.candidateFingerprint != p.reclamationRequest.candidateFingerprint ||
		p.cow.selectedTxn != p.reclamationRequest.selectedTxn ||
		p.cow.committedPageCount != p.reclamationRequest.committedPageCount ||
		p.cow.pageCount != p.reclamationRequest.pendingPageCount ||
		p.scope.poolEpoch != p.reclamationRequest.poolEpoch ||
		p.scope.id != p.reclamationRequest.scopeID || p.scope.anchor != p.reclamationRequest.scopeAnchor ||
		p.cow.root != p.reclamationRequest.root {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	if problem := p.validateCommittedSources(); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	if problem := p.validateSelectedPages(ticket.pages, selected, shadow.candidateLen); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	if problem := shadow.validateScopedBindings(); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	if len(shadow.arenaBindings) != len(p.cow.arenaBindings) ||
		len(shadow.replacements) != len(p.cow.replacements) ||
		len(shadow.indexNodes) != len(p.cow.indexNodes) ||
		len(shadow.availableSlots) != len(p.cow.availableSlots) ||
		shadow.replacementLen < 0 || shadow.replacementLen > len(shadow.replacements) ||
		shadow.availableLen < 0 || shadow.availableLen > len(shadow.availableSlots) {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
	}
	for _, node := range shadow.indexNodes {
		if node.page.kind == indexedBitmapPageArena && (node.page.slot < 0 || node.page.slot >= p.privatePages) {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: node.pageNumber}
		}
	}
	for index := 0; index < shadow.availableLen; index++ {
		stageSlot := shadow.availableSlots[index]
		if stageSlot < 0 || stageSlot >= p.privatePages {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	anchor, poolProblem := pool.validateScope(p.scope)
	if poolProblem.failed() || anchor.scopeCapacity != p.privatePages || anchor.scopeBound != 0 {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	stagePages := p.buffers.stage.poolValidation[:selected]
	previous := uint32(0)
	appended := 0
	vacant := anchor.scopeVacantHead
	inUse := 0
	for index, pageNumber := range stagePages {
		if index != 0 && pageNumber <= previous {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrCandidateOrderRegression, previousPage: previous, page: pageNumber}
		}
		if _, found := pool.slotIndex(pageNumber); found || vacant == privatePagePoolNoIndex ||
			vacant != p.cow.arenaBindings[index].poolSlot {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		slot := &pool.slots[vacant]
		if !pool.validScopedVacancySlot(p.scope, vacant) {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		authorization := reservationPageAuthorization(
			pageNumber, p.cow.candidates[:shadow.candidateLen], p.cow.committedPageCount,
		)
		if authorization == privatePageAppended {
			expected := p.cow.committedPageCount + uint64(appended)
			if uint64(pageNumber) != expected || expected >= MaxPageCount {
				return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted, page: pageNumber}
			}
			appended++
		} else if pageNumber < 2 || uint64(pageNumber) >= p.cow.committedPageCount {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrLedgerPageOutOfBounds, page: pageNumber}
		}
		stageInfo, stageProblem := shadow.pool.slotInfo(index)
		if stageProblem.failed() || !stageInfo.bound || stageInfo.pageNumber != pageNumber || stageInfo.authorization != authorization {
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		switch stageInfo.state {
		case privatePageAvailable:
			if slot.epoch > ^uint64(0)-2 {
				return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: pageNumber}
			}
		case privatePageInUse:
			if stageInfo.owner != privatePageOwnerBitmap || stageInfo.origin != privatePageBitmap ||
				stageInfo.pendingTxn != p.cow.pendingTxn || slot.epoch > ^uint64(0)-3 {
				return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted, page: pageNumber}
			}
			inUse++
		default:
			return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		previous = pageNumber
		vacant = slot.scopeVacantNext
	}
	if !pool.validScopedVacancyHead(p.scope, vacant, p.privatePages-selected) {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	forward := uint64(selected)
	if uint64(inUse) > (^uint64(0)-forward)/3 {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	forward += uint64(inUse) * 3
	// Each selected slot can need one logical rollback transition. Global and
	// scope AVL rotations snapshot only inline index fields; restoring those
	// fields neither advances mutationEpoch nor consumes checkpoint cleanup.
	rollback := uint64(selected)
	if rollback > ^uint64(0)-forward {
		return privatePagePoolCheckpoint{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	if poolProblem = pool.requireMutationSteps(forward + rollback); poolProblem.failed() {
		return privatePagePoolCheckpoint{}, bitmapPoolError(poolProblem)
	}
	checkpoint, poolProblem := pool.preflightCheckpoint()
	if poolProblem.failed() {
		return privatePagePoolCheckpoint{}, bitmapPoolError(poolProblem)
	}
	return checkpoint, freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) applyPreparedCOWState(shadow *freeBitmapCOW) {
	destination := &p.cow
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		realSlot := destination.arenaBindings[bindingIndex].poolSlot
		binding := shadow.arenaBindings[bindingIndex]
		binding.poolSlot = realSlot
		binding.poolEpoch = destination.pool.slots[realSlot].epoch
		destination.arenaBindings[bindingIndex] = binding
	}
	copy(destination.replacements, shadow.replacements)
	copy(destination.indexNodes, shadow.indexNodes)
	for index := range destination.indexNodes {
		node := &destination.indexNodes[index]
		if node.page.kind == indexedBitmapPageArena {
			node.page.slot = destination.arenaBindings[node.page.slot].poolSlot
		}
	}
	clear(destination.availableSlots)
	for index := 0; index < shadow.availableLen; index++ {
		destination.availableSlots[index] = destination.arenaBindings[shadow.availableSlots[index]].poolSlot
	}
	destination.replacementLen = shadow.replacementLen
	destination.candidateLen = shadow.candidateLen
	destination.indexRoot = shadow.indexRoot
	destination.indexLen = shadow.indexLen
	destination.availableLen = shadow.availableLen
	destination.selectedCandidateLen = shadow.selectedCandidateLen
	destination.candidateSelectionSet = shadow.candidateSelectionSet
	destination.pageCount = shadow.pageCount
	destination.pageCountsDistinct = shadow.pageCountsDistinct
	destination.root = shadow.root
	destination.mutationEpoch = shadow.mutationEpoch
	destination.resetRemovalScratch()
}

// bind consumes one verifier-issued reclamation capability, proves the complete
// merge and COW result in caller-owned shadow storage, and then executes one
// mechanically infallible checkpoint apply against the live scope.
func (p *freeBitmapReservationAttachment) bind(
	proof *freeBitmapReclamationProof,
) (freeBitmapReservationBinding, freeBitmapCOWError) {
	if p == nil {
		return freeBitmapReservationBinding{}, staleFreeBitmapReservationBind()
	}
	committed := p.committed
	seal, problem := captureFreeBitmapReservationBindSeal(p, proof)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	_, problem = p.consumeReclamationProof(proof)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	var sourceStatus pageSourceStatus
	if committed != nil {
		sourceStatus = committed.checkAccessStatus()
	}
	// The sealed source check above is the final callback. The single complete
	// post-callback fence first restores every scalar, pointer, and slice-header
	// identity, then authenticates content before any mutable slice is consumed.
	reclaimed, problem := seal.validateAfterCallback(p, proof)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	if sourceStatus.failed() {
		return freeBitmapReservationBinding{}, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: sourceStatus}
	}
	reclaimedRoot, problem := p.buildReclaimedSource(reclaimed)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	defer clear(p.buffers.sourceNodes[p.committedSourceLen : p.committedSourceLen+len(reclaimed)])
	selected, selectedCommitted, problem := p.selectPhysicalPages(reclaimed, reclaimedRoot)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	shadow, problem := p.buildShadow(selected, selectedCommitted)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}
	checkpoint, problem := p.preflightRealApply(shadow, selected)
	if problem.failed() {
		return freeBitmapReservationBinding{}, problem
	}

	// Everything below is bounds-proved, callback-free, allocation-free, and
	// infallible. The checkpoint retains exact rollback headroom, but no error
	// branch remains after the first live binding mutation.
	pool := p.cow.pool
	pool.beginCheckpointPrepared(checkpoint)
	selectedCandidates := p.cow.candidates[:selectedCommitted]
	for index := 0; index < selected; index++ {
		pageNumber := p.buffers.stage.poolValidation[index]
		authorization := reservationPageAuthorization(pageNumber, selectedCandidates, p.cow.committedPageCount)
		pool.bindPageForCheckpointPrepared(checkpoint, p.scope, pageNumber, authorization)
	}
	for index := 0; index < selected; index++ {
		stageSlot := &shadow.pool.slots[index]
		if stageSlot.state != privatePageInUse {
			continue
		}
		realSlot := p.cow.arenaBindings[index].poolSlot
		pool.claimSlotForCheckpointPrepared(checkpoint, realSlot)
		pool.writeSlotForCheckpointPrepared(realSlot, &stageSlot.bytes)
		pool.setSlotCommittedOriginForCheckpointPrepared(realSlot, stageSlot.committedOrigin)
	}
	p.terminalWork = pool.commitCheckpointInScopeTerminalPrepared(checkpoint, p.scope)
	p.applyPreparedCOWState(shadow)
	p.reclamationRequest.nonce = 0

	reclaimedSelected := selected - selectedCommitted
	appended := 0
	for index := 0; index < selected; index++ {
		if uint64(p.buffers.stage.poolValidation[index]) >= p.cow.committedPageCount {
			appended++
		}
	}
	reclaimedSelected -= appended
	return freeBitmapReservationBinding{
		committed: selectedCommitted, reclaimed: reclaimedSelected, appended: appended,
	}, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) ensureRoom(
	resource freeBitmapReservationResource,
	required, available int,
) freeBitmapCOWError {
	if required > available {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: resource,
			required: required, actual: available,
		}
	}
	return freeBitmapCOWError{}
}

func searchFreeBitmapLeafFromNoAlloc(
	leaf bitmapLeaf,
	base, limit, start uint64,
) (uint64, bool, bool) {
	first := start
	if first < base {
		first = base
	}
	if first < 2 {
		first = 2
	}
	if first >= limit {
		return 0, false, true
	}
	local := first - base
	firstWord64 := local / 64
	if firstWord64 > uint64(^uint(0)>>1) {
		return 0, false, false
	}
	firstWord := int(firstWord64)
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
			return candidate, true, ok
		}
	}
	return 0, false, true
}

func freeBitmapSourceHeight(nodes []freeBitmapReservationSourceNode, index int) uint8 {
	if index == bitmapCOWNoIndex {
		return 0
	}
	return nodes[index].height
}

func freeBitmapSourceCount(nodes []freeBitmapReservationSourceNode, index int) uint32 {
	if index == bitmapCOWNoIndex {
		return 0
	}
	return nodes[index].subtreeCount
}

func freeBitmapSourceRefresh(nodes []freeBitmapReservationSourceNode, index int) {
	leftHeight := freeBitmapSourceHeight(nodes, nodes[index].left)
	rightHeight := freeBitmapSourceHeight(nodes, nodes[index].right)
	if rightHeight > leftHeight {
		leftHeight = rightHeight
	}
	nodes[index].height = leftHeight + 1
	nodes[index].subtreeCount = 1 + freeBitmapSourceCount(nodes, nodes[index].left) + freeBitmapSourceCount(nodes, nodes[index].right)
}

func freeBitmapSourceRotateLeft(nodes []freeBitmapReservationSourceNode, root int) int {
	pivot := nodes[root].right
	nodes[root].right = nodes[pivot].left
	nodes[pivot].left = root
	freeBitmapSourceRefresh(nodes, root)
	freeBitmapSourceRefresh(nodes, pivot)
	return pivot
}

func freeBitmapSourceRotateRight(nodes []freeBitmapReservationSourceNode, root int) int {
	pivot := nodes[root].left
	nodes[root].left = nodes[pivot].right
	nodes[pivot].right = root
	freeBitmapSourceRefresh(nodes, root)
	freeBitmapSourceRefresh(nodes, pivot)
	return pivot
}

func freeBitmapSourceInsert(
	nodes []freeBitmapReservationSourceNode,
	root, index int,
	pageNumber uint32,
	kind freeBitmapReservationSourceKind,
	required int,
) int {
	if root == bitmapCOWNoIndex {
		nodes[index] = freeBitmapReservationSourceNode{
			pageNumber: pageNumber, kind: kind, required: required,
			left: bitmapCOWNoIndex, right: bitmapCOWNoIndex, height: 1, subtreeCount: 1,
		}
		return index
	}
	if pageNumber < nodes[root].pageNumber {
		nodes[root].left = freeBitmapSourceInsert(nodes, nodes[root].left, index, pageNumber, kind, required)
	} else {
		nodes[root].right = freeBitmapSourceInsert(nodes, nodes[root].right, index, pageNumber, kind, required)
	}
	freeBitmapSourceRefresh(nodes, root)
	balance := int16(freeBitmapSourceHeight(nodes, nodes[root].left)) - int16(freeBitmapSourceHeight(nodes, nodes[root].right))
	if balance > 1 {
		if pageNumber > nodes[nodes[root].left].pageNumber {
			nodes[root].left = freeBitmapSourceRotateLeft(nodes, nodes[root].left)
		}
		return freeBitmapSourceRotateRight(nodes, root)
	}
	if balance < -1 {
		if pageNumber < nodes[nodes[root].right].pageNumber {
			nodes[root].right = freeBitmapSourceRotateRight(nodes, nodes[root].right)
		}
		return freeBitmapSourceRotateLeft(nodes, root)
	}
	return root
}

func freeBitmapSourceAt(
	nodes []freeBitmapReservationSourceNode,
	root, rank int,
) (freeBitmapReservationSourceNode, bool) {
	for steps := 0; root != bitmapCOWNoIndex && steps <= len(nodes); steps++ {
		if root < 0 || root >= len(nodes) || rank < 0 {
			return freeBitmapReservationSourceNode{}, false
		}
		left := int(freeBitmapSourceCount(nodes, nodes[root].left))
		switch {
		case rank < left:
			root = nodes[root].left
		case rank == left:
			return nodes[root], true
		default:
			rank -= left + 1
			root = nodes[root].right
		}
	}
	return freeBitmapReservationSourceNode{}, false
}

func freeBitmapSourceFind(
	nodes []freeBitmapReservationSourceNode,
	root int,
	pageNumber uint32,
) bool {
	for steps := 0; root != bitmapCOWNoIndex && steps <= len(nodes); steps++ {
		if root < 0 || root >= len(nodes) {
			return false
		}
		switch {
		case pageNumber < nodes[root].pageNumber:
			root = nodes[root].left
		case pageNumber > nodes[root].pageNumber:
			root = nodes[root].right
		default:
			return true
		}
	}
	return false
}

func freeBitmapFingerprintUint64(hash, value uint64) uint64 {
	const prime = uint64(1099511628211)
	for shift := uint(0); shift < 64; shift += 8 {
		hash ^= uint64(byte(value >> shift))
		hash *= prime
	}
	return hash
}

func freeBitmapPageFingerprint(pages []uint32) uint64 {
	hash := freeBitmapFingerprintUint64(1469598103934665603, uint64(len(pages)))
	for _, page := range pages {
		hash = freeBitmapFingerprintUint64(hash, uint64(page))
	}
	return hash
}

func freeBitmapCapacityFingerprint(
	candidates []uint32,
	verifiedPages []verifiedBitmapPage,
) uint64 {
	hash := freeBitmapPageFingerprint(candidates)
	hash = freeBitmapFingerprintUint64(hash, uint64(len(verifiedPages)))
	for _, verified := range verifiedPages {
		for _, value := range []uint64{
			uint64(verified.pageNumber), verified.base, uint64(verified.level),
			uint64(verified.parent + 1), uint64(verified.remaining),
		} {
			hash = freeBitmapFingerprintUint64(hash, value)
		}
		if verified.survives {
			hash = freeBitmapFingerprintUint64(hash, 1)
		} else {
			hash = freeBitmapFingerprintUint64(hash, 0)
		}
		hash = freeBitmapFingerprintBytes(hash, verified.bytes[:])
	}
	return hash
}

func freeBitmapSourceFingerprint(nodes []freeBitmapReservationSourceNode) uint64 {
	hash := freeBitmapFingerprintUint64(1469598103934665603, uint64(len(nodes)))
	for _, node := range nodes {
		hash = freeBitmapFingerprintUint64(hash, uint64(node.pageNumber))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.kind))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.required))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.left+1))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.right+1))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.height))
		hash = freeBitmapFingerprintUint64(hash, uint64(node.subtreeCount))
	}
	return hash
}

func freeBitmapFingerprintBytes(hash uint64, values []byte) uint64 {
	const prime = uint64(1099511628211)
	for _, value := range values {
		hash ^= uint64(value)
		hash *= prime
	}
	return hash
}

func freeBitmapReservationCOWFingerprint(cow *freeBitmapCOW) uint64 {
	if cow == nil {
		return 0
	}
	hash := freeBitmapFingerprintUint64(1469598103934665603, cow.selectedTxn)
	for _, value := range []uint64{
		cow.pendingTxn, cow.committedPageCount, cow.pageCount, uint64(cow.root),
		uint64(cow.scope.id), uint64(cow.scope.anchor + 1), uint64(cow.scopeCapacity),
		uint64(cow.replacementLen), uint64(cow.candidateLen), uint64(cow.indexRoot + 1),
		uint64(cow.indexLen), uint64(cow.availableLen), uint64(cow.plannedCandidateLen),
		uint64(cow.selectedCandidateLen), uint64(cow.payloadPageBudget),
		uint64(cow.plannedRequiredPrivatePages), cow.mutationEpoch,
	} {
		hash = freeBitmapFingerprintUint64(hash, value)
	}
	for _, flag := range []bool{
		cow.pageCountsDistinct, cow.scoped, cow.candidateSelectionSet, cow.reservationPlanned,
	} {
		if flag {
			hash = freeBitmapFingerprintUint64(hash, 1)
		} else {
			hash = freeBitmapFingerprintUint64(hash, 0)
		}
	}
	for _, binding := range cow.arenaBindings {
		for _, value := range []uint64{
			uint64(binding.poolSlot + 1), binding.poolEpoch, uint64(binding.pageNumber),
			uint64(binding.storageNode + 1), uint64(binding.activeNode + 1),
		} {
			hash = freeBitmapFingerprintUint64(hash, value)
		}
		if binding.bound {
			hash = freeBitmapFingerprintUint64(hash, 1)
		} else {
			hash = freeBitmapFingerprintUint64(hash, 0)
		}
	}
	for _, value := range cow.replacements {
		hash = freeBitmapFingerprintUint64(hash, uint64(value))
	}
	for _, node := range cow.indexNodes {
		for _, value := range []uint64{
			uint64(node.pageNumber), uint64(node.page.kind), uint64(node.page.slot + 1),
			uint64(node.left + 1), uint64(node.right + 1), uint64(node.height),
			uint64(node.candidatePage), uint64(node.candidateIndex + 1),
		} {
			hash = freeBitmapFingerprintUint64(hash, value)
		}
		if node.candidateMapped {
			hash = freeBitmapFingerprintUint64(hash, 1)
		} else {
			hash = freeBitmapFingerprintUint64(hash, 0)
		}
	}
	for _, value := range cow.availableSlots {
		hash = freeBitmapFingerprintUint64(hash, uint64(value+1))
	}
	for _, value := range cow.candidates {
		hash = freeBitmapFingerprintUint64(hash, uint64(value))
	}
	for _, verified := range cow.verifiedPages {
		for _, value := range []uint64{
			uint64(verified.pageNumber), verified.base, uint64(verified.level),
			uint64(verified.parent + 1), uint64(verified.remaining),
		} {
			hash = freeBitmapFingerprintUint64(hash, value)
		}
		if verified.survives {
			hash = freeBitmapFingerprintUint64(hash, 1)
		} else {
			hash = freeBitmapFingerprintUint64(hash, 0)
		}
		hash = freeBitmapFingerprintBytes(hash, verified.bytes[:])
	}
	return hash
}
