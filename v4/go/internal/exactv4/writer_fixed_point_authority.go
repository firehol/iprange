package exactv4

type privateWriterFixedPointWorkPhase uint8

const (
	privateWriterFixedPointWorkPrepared privateWriterFixedPointWorkPhase = iota + 1
	privateWriterFixedPointWorkRegistered
	privateWriterFixedPointWorkActive
)

type privateWriterPreparedTerminalPhase uint8

const (
	privateWriterPreparedTerminalReady privateWriterPreparedTerminalPhase = iota + 1
	privateWriterPreparedTerminalOperationActive
	privateWriterPreparedTerminalCheckpointActive
	privateWriterPreparedTerminalConsumed
)

type privateWriterCarriedSource struct {
	identity uint64
	ordinal  uint64
	lastPage uint32
	epoch    uint64
}

type privateWriterFixedPointSourceProgress struct {
	sourceIdentity uint64
	ordinal        uint64
	lastPage       uint32
	sourceEpoch    uint64
}

type privateWriterFixedPointOrderedSource interface {
	committedPageSource
	nextFixedPointSource(
		privateWriterCarriedSource,
	) (privateWriterFixedPointSourceProgress, pageSourceStatus)
}

type privateWriterFixedPointPrepareRequest struct {
	workUnit          uint64
	expectedRoot      uint32
	expectedPageCount uint64
	scopePages        int
}

type privateWriterPreparedScopePlan struct {
	nodes          []uint64
	epochs         []uint64
	scopeID        uint64
	anchor         int
	remainingHead  int
	previous       int
	remainingCount int
	head           int
	tail           int
	vacantCount    int
	activeScopes   int
	scopeSequence  uint64
	mutationEpoch  uint64
	visits         int
}

type privateWriterWorkFence struct {
	self        *privateWriterWorkFence
	slot        *privateWriterFixedPointPreparedWork
	coordinator *privateWriterFixedPointCoordinator
	core        *privateWriterTransactionCore
	pool        *privatePagePool

	sessionID         uint64
	sessionGeneration uint64
	workID            uint64
	generation        uint64
	nonce             uint64
	scopeID           uint64
	scopeAnchor       int
	phase             privateWriterFixedPointWorkPhase
}

type privateWriterFixedPointPreparedWork struct {
	self        *privateWriterFixedPointPreparedWork
	coordinator *privateWriterFixedPointCoordinator
	workspace   *privateWriterWorkspace
	fence       privateWriterWorkFence

	storageIndex       int
	scratch            []uint64
	workspaceLayout    uint64
	preparedGeneration uint64
	scratchGeneration  uint64

	sessionID             uint64
	sessionGeneration     uint64
	predecessorGeneration uint64
	predecessorNonce      uint64
	workID                uint64
	generation            uint64
	nonce                 uint64
	root                  uint32
	pageCount             uint64
	currentSource         privateWriterCarriedSource
	progress              privateWriterFixedPointSourceProgress
	scopePages            int
	source                privateWriterFixedPointOrderedSource
	coordinatorSeal       privateWriterFixedPointCoordinatorSeal
	poolSeal              freeBitmapReservationPoolSeal
	workspaceVersions     privateWriterWorkspaceVersions
	poolMutationEpoch     uint64
	recordLen             int
	lastWorkUnit          uint64
	scopePlan             privateWriterPreparedScopePlan
	terminalJournal       privateWriterPreparedTerminalJournal
	terminalCommitment    privateWriterPreparedTerminalCommitment
	phase                 privateWriterFixedPointWorkPhase
}

type privateWriterPreparedTerminalJournal struct {
	self       *privateWriterPreparedTerminalJournal
	scopeID    uint64
	anchor     int
	operation  privatePagePoolOperation
	checkpoint privatePagePoolCheckpoint
	phase      privateWriterPreparedTerminalPhase
}

type privateWriterPreparedTerminalCommitment struct {
	scopeID             uint64
	anchor              int
	operation           privatePagePoolOperation
	checkpoint          privatePagePoolCheckpoint
	phase               privateWriterPreparedTerminalPhase
	pendingTxn          uint64
	poolEpoch           uint64
	poolGeneration      uint64
	operationSequence   uint64
	checkpointSequence  uint64
	activeOperationID   uint64
	operationStartEpoch uint64
	activeCheckpointID  uint64
	workID              uint64
	generation          uint64
	nonce               uint64
}

type privateWriterFixedPointPreparedToken struct {
	coordinator *privateWriterFixedPointCoordinator
	slot        *privateWriterFixedPointPreparedWork
	generation  uint64
	nonce       uint64
}

type privateWriterFixedPointActiveToken struct {
	coordinator *privateWriterFixedPointCoordinator
	workID      uint64
	generation  uint64
	nonce       uint64
}

type privateWriterFixedPointCoordinatorSeal struct {
	self                      *privateWriterFixedPointCoordinator
	pool                      *privatePagePool
	selectedTxn               uint64
	pendingTxn                uint64
	selectedPageCount         uint64
	root                      uint32
	pageCount                 uint64
	incarnation               uint64
	predecessorNonce          uint64
	predecessorUsed           bool
	sequence                  uint64
	recordLen                 int
	lastWorkUnit              uint64
	preparedSlots             freeBitmapReservationSliceSeal[privateWriterFixedPointPreparedWork]
	preparationScratch        freeBitmapReservationSliceSeal[uint64]
	preparationScratchPerSlot int
	predecessorGeneration     uint64
	carried                   privateWriterCarriedSource
	activePrepared            *privateWriterFixedPointPreparedWork
	sourcePool                *privatePagePool
	sourceRecords             freeBitmapReservationSliceSeal[privateWriterSealedBitmapWorkUnitRecord]
	sourceSlotRecords         freeBitmapReservationSliceSeal[int]
	transactionCore           *privateWriterTransactionCore
	workspace                 *privateWriterWorkspace
	workFence                 *privateWriterWorkFence
}

func sealPrivateWriterFixedPointCoordinator(
	coordinator *privateWriterFixedPointCoordinator,
) privateWriterFixedPointCoordinatorSeal {
	return privateWriterFixedPointCoordinatorSeal{
		self: coordinator.self, pool: coordinator.pool,
		selectedTxn: coordinator.selectedTxn, pendingTxn: coordinator.pendingTxn,
		selectedPageCount: coordinator.selectedPageCount,
		root:              coordinator.root, pageCount: coordinator.pageCount,
		incarnation: coordinator.incarnation, predecessorNonce: coordinator.predecessorNonce,
		predecessorUsed: coordinator.predecessorUsed, sequence: coordinator.sequence,
		recordLen: coordinator.recordLen, lastWorkUnit: coordinator.lastWorkUnit,
		preparedSlots:             sealFreeBitmapReservationSlice(coordinator.preparedSlots),
		preparationScratch:        sealFreeBitmapReservationSlice(coordinator.preparationScratch),
		preparationScratchPerSlot: coordinator.preparationScratchPerSlot,
		predecessorGeneration:     coordinator.predecessorGeneration,
		carried:                   coordinator.carried, activePrepared: coordinator.activePrepared,
		sourcePool:        coordinator.sourceState.pool,
		sourceRecords:     sealFreeBitmapReservationSlice(coordinator.sourceState.records),
		sourceSlotRecords: sealFreeBitmapReservationSlice(coordinator.sourceState.slotRecords),
		transactionCore:   coordinator.transactionCore,
		workspace:         coordinator.workspace, workFence: coordinator.workFence,
	}
}

func (seal privateWriterFixedPointCoordinatorSeal) matches(
	coordinator *privateWriterFixedPointCoordinator,
) bool {
	return coordinator != nil &&
		coordinator.self == seal.self && coordinator.pool == seal.pool &&
		coordinator.selectedTxn == seal.selectedTxn &&
		coordinator.pendingTxn == seal.pendingTxn &&
		coordinator.selectedPageCount == seal.selectedPageCount &&
		coordinator.root == seal.root && coordinator.pageCount == seal.pageCount &&
		coordinator.incarnation == seal.incarnation &&
		coordinator.predecessorNonce == seal.predecessorNonce &&
		coordinator.predecessorUsed == seal.predecessorUsed &&
		coordinator.sequence == seal.sequence &&
		coordinator.recordLen == seal.recordLen &&
		coordinator.lastWorkUnit == seal.lastWorkUnit &&
		seal.preparedSlots.matches(coordinator.preparedSlots) &&
		seal.preparationScratch.matches(coordinator.preparationScratch) &&
		coordinator.preparationScratchPerSlot == seal.preparationScratchPerSlot &&
		coordinator.predecessorGeneration == seal.predecessorGeneration &&
		coordinator.carried == seal.carried &&
		coordinator.activePrepared == seal.activePrepared &&
		coordinator.sourceState.pool == seal.sourcePool &&
		seal.sourceRecords.matches(coordinator.sourceState.records) &&
		seal.sourceSlotRecords.matches(coordinator.sourceState.slotRecords) &&
		coordinator.transactionCore == seal.transactionCore &&
		coordinator.workspace == seal.workspace &&
		coordinator.workFence == seal.workFence
}

func (seal privateWriterFixedPointCoordinatorSeal) matchesRegistered(
	coordinator *privateWriterFixedPointCoordinator,
	slot *privateWriterFixedPointPreparedWork,
) bool {
	return coordinator != nil && slot != nil &&
		coordinator.self == seal.self &&
		coordinator.pool == seal.pool &&
		coordinator.selectedTxn == seal.selectedTxn &&
		coordinator.pendingTxn == seal.pendingTxn &&
		coordinator.selectedPageCount == seal.selectedPageCount &&
		coordinator.root == seal.root &&
		coordinator.pageCount == seal.pageCount &&
		coordinator.incarnation == seal.incarnation &&
		coordinator.predecessorNonce == seal.predecessorNonce &&
		coordinator.predecessorUsed &&
		seal.predecessorGeneration != ^uint64(0) &&
		coordinator.predecessorGeneration == seal.predecessorGeneration+1 &&
		coordinator.sequence == seal.sequence &&
		coordinator.recordLen == seal.recordLen &&
		coordinator.lastWorkUnit == seal.lastWorkUnit &&
		seal.preparedSlots.matches(coordinator.preparedSlots) &&
		seal.preparationScratch.matches(coordinator.preparationScratch) &&
		coordinator.preparationScratchPerSlot == seal.preparationScratchPerSlot &&
		coordinator.carried == (privateWriterCarriedSource{
			identity: slot.progress.sourceIdentity,
			ordinal:  slot.progress.ordinal,
			lastPage: slot.progress.lastPage,
			epoch:    slot.progress.sourceEpoch,
		}) &&
		coordinator.activePrepared == slot &&
		coordinator.sourceState.pool == seal.sourcePool &&
		seal.sourceRecords.matches(coordinator.sourceState.records) &&
		seal.sourceSlotRecords.matches(coordinator.sourceState.slotRecords) &&
		coordinator.transactionCore == seal.transactionCore &&
		coordinator.workspace == seal.workspace
}

func privateWriterPreparedScratchCanonical(scratch []uint64) bool {
	for _, value := range scratch {
		if value != 0 {
			return false
		}
	}
	return true
}

func (c *privateWriterTransactionCore) callbackActive() bool {
	return c != nil && c.workspace != nil && c.workspace.self == c.workspace &&
		c.workspace.callbackActive
}

func (c *privateWriterTransactionCore) preparedFixedPointAuthorityConsistent() bool {
	if c == nil || c.self != c || !c.fixedPointPreparedMode ||
		c.workspace == nil || c.workspace.self != c.workspace ||
		c.fixedPointCoordinator.workspace != c.workspace {
		return false
	}
	if c.fixedPointSessionID == 0 || c.fixedPointSessionGeneration == 0 ||
		c.fixedPointSessionID != c.fixedPointCoordinator.incarnation ||
		(!c.fixedPointWorkActive &&
			c.fixedPointSessionGeneration != c.fixedPointCoordinator.predecessorGeneration) ||
		c.pool.coordinatorSessionID != c.fixedPointSessionID ||
		c.pool.coordinatorSessionGeneration != c.fixedPointSessionGeneration {
		return false
	}
	if !c.fixedPointWorkActive {
		return c.fixedPointRegisteredWorkID == 0 &&
			c.fixedPointRegisteredWorkGeneration == 0 &&
			c.fixedPointRegisteredWorkPhase == 0 &&
			c.fixedPointWorkFence == nil &&
			c.fixedPointCoordinator.workFence == nil &&
			c.pool.registeredWorkID == 0 &&
			c.pool.registeredWorkGeneration == 0 &&
			c.pool.registeredWorkPhase == 0 &&
			c.pool.registeredWorkFence == nil &&
			c.pool.unacceptedScopes == 0
	}
	active := c.fixedPointCoordinator.activePrepared
	return active != nil && active.phase == privateWriterFixedPointWorkActive &&
		c.fixedPointRegisteredWorkID == active.workID &&
		c.fixedPointRegisteredWorkGeneration == active.generation &&
		c.fixedPointRegisteredWorkPhase == privateWriterFixedPointWorkActive &&
		c.fixedPointWorkFence == &active.fence &&
		c.fixedPointCoordinator.workFence == c.fixedPointWorkFence &&
		c.pool.registeredWorkID == c.fixedPointRegisteredWorkID &&
		c.pool.registeredWorkGeneration == c.fixedPointRegisteredWorkGeneration &&
		c.pool.registeredWorkPhase == uint8(c.fixedPointRegisteredWorkPhase) &&
		c.pool.registeredWorkFence == c.fixedPointWorkFence &&
		c.pool.unacceptedScopes == 1
}

func (p *privatePagePool) beginCoordinatorSession(
	sessionID, generation uint64,
) privatePagePoolError {
	if p == nil || p.self != p || sessionID == 0 || generation == 0 ||
		p.coordinatorSessionID != 0 || p.registeredWorkID != 0 ||
		p.registeredWorkFence != nil ||
		p.activeCheckpointID != 0 || p.activeOperationID != 0 ||
		p.activeScopes != 0 || p.unacceptedScopes != 0 ||
		p.coordinatorCleanupPending != 0 || p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	p.coordinatorSessionID = sessionID
	p.coordinatorSessionGeneration = generation
	p.registeredScopeAnchor = privatePagePoolNoIndex
	return privatePagePoolError{}
}

func (p *privatePagePool) finishCoordinatorSession(
	sessionID, generation uint64,
) privatePagePoolError {
	if p == nil || p.self != p ||
		sessionID == 0 || sessionID != p.coordinatorSessionID ||
		generation == 0 || generation != p.coordinatorSessionGeneration ||
		p.registeredWorkID != 0 || p.registeredWorkGeneration != 0 ||
		p.registeredWorkPhase != 0 || p.registeredWorkStartEpoch != 0 ||
		p.registeredWorkMutation || p.registeredWorkFence != nil ||
		p.registeredScopeID != 0 ||
		p.registeredScopeAnchor != privatePagePoolNoIndex ||
		p.unacceptedScopes != 0 || p.coordinatorCleanupPending != 0 ||
		p.activeScopes != 0 || p.activeCheckpointID != 0 ||
		p.activeOperationID != 0 || p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	p.coordinatorSessionID = 0
	p.coordinatorSessionGeneration = 0
	return privatePagePoolError{}
}

func (c *privateWriterFixedPointCoordinator) configureWorkspace(
	workspace *privateWriterWorkspace,
) privateWriterFixedPointError {
	if c == nil || c.self != c || workspace == nil || workspace.self != workspace ||
		workspace.layoutGeneration == 0 || len(workspace.preparedSlots) == 0 ||
		workspace.scratchWordsPerSlot <= 0 ||
		len(workspace.scratch) != len(workspace.preparedSlots)*workspace.scratchWordsPerSlot {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	c.workspace = workspace
	c.preparedSlots = workspace.preparedSlots
	c.preparationScratch = workspace.scratch
	c.preparationScratchPerSlot = workspace.scratchWordsPerSlot
	c.predecessorGeneration = 1
	c.carried = privateWriterCarriedSource{identity: c.incarnation, epoch: 1}
	return privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) carriedSource() privateWriterCarriedSource {
	if c == nil || c.self != c {
		return privateWriterCarriedSource{}
	}
	return c.carried
}

func (p *privatePagePool) preflightPreparedScope(
	count int,
	scratch []uint64,
) (privateWriterPreparedScopePlan, privatePagePoolError) {
	if p == nil || p.self != p {
		return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.checkpointCleanup != 0 || p.checkpointSlotHead != privatePagePoolNoIndex ||
		p.checkpointSlotCount != 0 || p.checkpointIndexHead != privatePagePoolNoIndex ||
		p.checkpointIndexCount != 0 || p.checkpointScopeHead != privatePagePoolNoIndex ||
		p.checkpointScopeCount != 0 || count <= 0 || count > len(p.slots) ||
		count > len(scratch)/2 || !p.validUnscopedVacancyHeader() {
		return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if count > p.unscopedVacantCount {
		return privateWriterPreparedScopePlan{}, privatePagePoolError{
			code: privatePagePoolErrBudget, required: count, actual: p.unscopedVacantCount,
		}
	}
	id := p.scopeSequence + 1
	if id == 0 || p.activeScopes == int(^uint(0)>>1) {
		return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	nodes := scratch[:count]
	epochs := scratch[count : count*2]
	anchor := p.unscopedVacantHead
	member := anchor
	previous := privatePagePoolNoIndex
	visits := 0
	for visits < count {
		if member < 0 || member >= len(p.slots) {
			clear(scratch[:count*2])
			return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		slot := &p.slots[member]
		if !privatePageSlotIsCanonicalUnscopedVacant(slot) ||
			slot.unscopedPrevious != previous {
			clear(scratch[:count*2])
			return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		if slot.epoch == ^uint64(0) {
			clear(scratch[:count*2])
			return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
		}
		nodes[visits] = uint64(member + 1)
		epochs[visits] = slot.epoch
		previous = member
		member = slot.unscopedNext
		visits++
	}
	remainingCount := p.unscopedVacantCount - count
	if !p.validUnscopedVacancyHeadAfterDetach(member, previous, remainingCount) {
		clear(scratch[:count*2])
		return privateWriterPreparedScopePlan{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if problem := p.requireMutationSteps(uint64(count)); problem.failed() {
		clear(scratch[:count*2])
		return privateWriterPreparedScopePlan{}, problem
	}
	return privateWriterPreparedScopePlan{
		nodes: nodes, epochs: epochs, scopeID: id, anchor: anchor,
		remainingHead: member, previous: previous, remainingCount: remainingCount,
		head: p.unscopedVacantHead, tail: p.unscopedVacantTail,
		vacantCount: p.unscopedVacantCount, activeScopes: p.activeScopes,
		scopeSequence: p.scopeSequence, mutationEpoch: p.mutationEpoch, visits: visits,
	}, privatePagePoolError{}
}

func (p *privatePagePool) revalidatePreparedScope(
	plan privateWriterPreparedScopePlan,
) privatePagePoolError {
	if p == nil || p.self != p || plan.visits <= 0 ||
		len(plan.nodes) != plan.visits || len(plan.epochs) != plan.visits ||
		p.unscopedVacantHead != plan.head || p.unscopedVacantTail != plan.tail ||
		p.unscopedVacantCount != plan.vacantCount ||
		p.activeScopes != plan.activeScopes ||
		p.scopeSequence != plan.scopeSequence ||
		p.mutationEpoch != plan.mutationEpoch ||
		p.scopeSequence+1 != plan.scopeID ||
		!p.validUnscopedVacancyHeader() {
		return privatePagePoolError{code: privatePagePoolErrStaleToken}
	}
	previous := privatePagePoolNoIndex
	for index, encoded := range plan.nodes {
		if encoded == 0 || encoded-1 > uint64(len(p.slots)-1) {
			return privatePagePoolError{code: privatePagePoolErrStaleToken}
		}
		member := int(encoded - 1)
		slot := &p.slots[member]
		if !privatePageSlotIsCanonicalUnscopedVacant(slot) ||
			slot.unscopedPrevious != previous || slot.epoch != plan.epochs[index] {
			return privatePagePoolError{code: privatePagePoolErrStaleToken}
		}
		previous = member
	}
	last := int(plan.nodes[len(plan.nodes)-1] - 1)
	if p.slots[last].unscopedNext != plan.remainingHead ||
		!p.validUnscopedVacancyHeadAfterDetach(
			plan.remainingHead, plan.previous, plan.remainingCount,
		) {
		return privatePagePoolError{code: privatePagePoolErrStaleToken}
	}
	if problem := p.requireMutationSteps(uint64(plan.visits)); problem.failed() {
		return problem
	}
	return privatePagePoolError{}
}

func (c *privateWriterFixedPointCoordinator) prepareWork(
	predecessor privateWriterFixedPointPredecessor,
	slot *privateWriterFixedPointPreparedWork,
	request privateWriterFixedPointPrepareRequest,
) (privateWriterFixedPointPreparedToken, privateWriterFixedPointError) {
	if problem := c.validatePredecessor(predecessor); problem.failed() {
		return privateWriterFixedPointPreparedToken{}, problem
	}
	if slot == nil || slot.self != slot || slot.coordinator != c ||
		slot.workspace != c.workspace || slot.workspaceLayout != c.workspace.layoutGeneration ||
		slot.storageIndex < 0 || slot.storageIndex >= len(c.preparedSlots) ||
		slot != &c.preparedSlots[slot.storageIndex] || slot.phase != 0 ||
		request.workUnit == 0 || request.workUnit <= c.lastWorkUnit ||
		request.expectedRoot != predecessor.root ||
		request.expectedPageCount != predecessor.pageCount ||
		request.scopePages <= 0 || request.scopePages > c.pool.unscopedVacantCount ||
		request.scopePages > len(slot.scratch)/2 ||
		!privateWriterPreparedScratchCanonical(slot.scratch[:request.scopePages*2]) {
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrInvalidArgument,
		}
	}
	source, ok := c.sourceState.selected.(privateWriterFixedPointOrderedSource)
	if !ok {
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrInvalidArgument,
		}
	}
	beforePool := sealFreeBitmapReservationPool(c.pool)
	beforeCoordinator := sealPrivateWriterFixedPointCoordinator(c)
	beforeVersions := c.workspace.versions()
	beforeMutation := c.pool.mutationEpoch
	beforePageCount := c.pool.pendingPageCount
	beforeRoot := c.root
	beforeRecordLen := c.recordLen
	beforeLastWorkUnit := c.lastWorkUnit
	beforeSourcePool := c.sourceState.pool
	c.workspace.callbackActive = true
	progress, status := source.nextFixedPointSource(c.carried)
	c.workspace.callbackActive = false
	if !beforePool.matches(c.pool) ||
		!beforeCoordinator.matches(c) ||
		c.workspace.versions() != beforeVersions ||
		c.pool.mutationEpoch != beforeMutation ||
		c.pool.pendingPageCount != beforePageCount || c.root != beforeRoot ||
		c.recordLen != beforeRecordLen || c.lastWorkUnit != beforeLastWorkUnit ||
		c.sourceState.pool != beforeSourcePool || beforeSourcePool != c.pool {
		c.pool.abortRequired = true
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance,
		}
	}
	if status.failed() {
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrSource, source: status,
		}
	}
	if c.carried.ordinal == ^uint64(0) || c.carried.epoch == ^uint64(0) ||
		progress.sourceIdentity != c.carried.identity ||
		progress.ordinal != c.carried.ordinal+1 ||
		progress.sourceEpoch != c.carried.epoch+1 ||
		progress.lastPage <= c.carried.lastPage ||
		progress.lastPage < 2 || uint64(progress.lastPage) >= c.pageCount {
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStalePredecessor, page: progress.lastPage,
		}
	}
	if _, owned := c.pool.slotIndex(progress.lastPage); owned {
		c.pool.abortRequired = true
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrAdvertisedOwnedPage, page: progress.lastPage,
		}
	}
	scopePlan, poolProblem := c.pool.preflightPreparedScope(request.scopePages, slot.scratch)
	if poolProblem.failed() {
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrPool, pool: poolProblem,
		}
	}
	c.workspace.scopePreflightVisits += scopePlan.visits
	_, nonce, identityOK := nextPrivateWriterFixedPointIdentity()
	index := slot.storageIndex
	if !identityOK || c.workspace.preparedGenerations[index] == ^uint64(0) ||
		c.workspace.scratchGenerations[index] == ^uint64(0) {
		clear(slot.scratch[:request.scopePages*2])
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrExhausted,
		}
	}
	generation := c.workspace.preparedGenerations[index] + 1
	scratchGeneration := c.workspace.scratchGenerations[index] + 1
	if c.pool.operationSequence == ^uint64(0) ||
		c.pool.checkpointSequence == ^uint64(0) ||
		c.pool.generation == ^uint64(0) ||
		scopePlan.mutationEpoch > ^uint64(0)-uint64(scopePlan.visits) {
		clear(slot.scratch[:request.scopePages*2])
		return privateWriterFixedPointPreparedToken{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrExhausted,
		}
	}
	c.workspace.preparedGenerations[index] = generation
	c.workspace.scratchGenerations[index] = scratchGeneration
	scratch := slot.scratch
	storageIndex := slot.storageIndex
	*slot = privateWriterFixedPointPreparedWork{
		self: slot, coordinator: c, workspace: c.workspace,
		storageIndex: storageIndex, scratch: scratch,
		workspaceLayout:    c.workspace.layoutGeneration,
		preparedGeneration: generation, scratchGeneration: scratchGeneration,
		sessionID:             c.pool.coordinatorSessionID,
		sessionGeneration:     c.pool.coordinatorSessionGeneration,
		predecessorGeneration: c.predecessorGeneration,
		predecessorNonce:      predecessor.nonce,
		workID:                request.workUnit, generation: generation, nonce: nonce,
		root: predecessor.root, pageCount: predecessor.pageCount,
		currentSource: c.carried, progress: progress,
		scopePages: request.scopePages, source: source,
		coordinatorSeal:   sealPrivateWriterFixedPointCoordinator(c),
		poolSeal:          sealFreeBitmapReservationPool(c.pool),
		workspaceVersions: c.workspace.versions(),
		poolMutationEpoch: beforeMutation,
		recordLen:         beforeRecordLen, lastWorkUnit: beforeLastWorkUnit,
		scopePlan: scopePlan, phase: privateWriterFixedPointWorkPrepared,
	}
	journal := &slot.terminalJournal
	*journal = privateWriterPreparedTerminalJournal{
		self: journal, scopeID: scopePlan.scopeID, anchor: scopePlan.anchor,
		operation: privatePagePoolOperation{
			pool: c.pool, poolEpoch: c.pool.epoch,
			id:         c.pool.operationSequence + 1,
			pendingTxn: c.pool.pendingTxn,
			generation: c.pool.generation + 1,
			scopeID:    scopePlan.scopeID, scopeAnchor: scopePlan.anchor,
			startEpoch: scopePlan.mutationEpoch + uint64(scopePlan.visits),
		},
		checkpoint: privatePagePoolCheckpoint{
			pool: c.pool, poolEpoch: c.pool.epoch,
			id:               c.pool.checkpointSequence + 1,
			generation:       c.pool.generation + 1,
			indexRoot:        c.pool.indexRoot,
			pendingPageCount: c.pool.pendingPageCount,
		},
		phase: privateWriterPreparedTerminalReady,
	}
	slot.terminalCommitment = privateWriterPreparedTerminalCommitment{
		scopeID: journal.scopeID, anchor: journal.anchor,
		operation: journal.operation, checkpoint: journal.checkpoint,
		phase:               journal.phase,
		pendingTxn:          c.pool.pendingTxn,
		poolEpoch:           c.pool.epoch,
		poolGeneration:      c.pool.generation,
		operationSequence:   c.pool.operationSequence,
		checkpointSequence:  c.pool.checkpointSequence,
		activeOperationID:   c.pool.activeOperationID,
		operationStartEpoch: c.pool.operationStartEpoch,
		activeCheckpointID:  c.pool.activeCheckpointID,
		workID:              slot.workID,
		generation:          slot.generation,
		nonce:               slot.nonce,
	}
	return privateWriterFixedPointPreparedToken{
		coordinator: c, slot: slot, generation: generation, nonce: nonce,
	}, privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) validatePreparedWork(
	token privateWriterFixedPointPreparedToken,
) (*privateWriterFixedPointPreparedWork, privateWriterFixedPointError) {
	slot := token.slot
	if c == nil || c.self != c || token.coordinator != c || slot == nil ||
		slot.self != slot || slot.coordinator != c || slot.workspace != c.workspace ||
		c.workspace == nil || c.workspace.self != c.workspace ||
		slot.storageIndex < 0 || slot.storageIndex >= len(c.preparedSlots) ||
		slot != &c.preparedSlots[slot.storageIndex] ||
		token.generation == 0 || token.generation != slot.generation ||
		token.nonce == 0 || token.nonce != slot.nonce ||
		slot.phase != privateWriterFixedPointWorkPrepared {
		return nil, privateWriterFixedPointError{code: privateWriterFixedPointErrStalePredecessor}
	}
	index := slot.storageIndex
	if slot.workspaceLayout != c.workspace.layoutGeneration ||
		slot.preparedGeneration != c.workspace.preparedGenerations[index] ||
		slot.scratchGeneration != c.workspace.scratchGenerations[index] ||
		slot.predecessorGeneration != c.predecessorGeneration ||
		slot.predecessorNonce != c.predecessorNonce ||
		slot.root != c.root || slot.pageCount != c.pageCount ||
		slot.currentSource != c.carried ||
		slot.recordLen != c.recordLen || slot.lastWorkUnit != c.lastWorkUnit ||
		!slot.coordinatorSeal.matches(c) ||
		!slot.poolSeal.matches(c.pool) ||
		slot.workspaceVersions != c.workspace.versions() ||
		slot.poolMutationEpoch != c.pool.mutationEpoch ||
		c.sourceState.pool != c.pool {
		c.pool.abortRequired = true
		return nil, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance}
	}
	if poolProblem := c.pool.revalidatePreparedScope(slot.scopePlan); poolProblem.failed() {
		c.pool.abortRequired = true
		return nil, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, pool: poolProblem,
		}
	}
	journal := &slot.terminalJournal
	commitment := slot.terminalCommitment
	if journal.self != journal ||
		journal.scopeID != slot.scopePlan.scopeID ||
		journal.anchor != slot.scopePlan.anchor ||
		journal.phase != privateWriterPreparedTerminalReady ||
		commitment.scopeID != slot.scopePlan.scopeID ||
		commitment.anchor != slot.scopePlan.anchor ||
		commitment.phase != privateWriterPreparedTerminalReady ||
		commitment.pendingTxn != c.pool.pendingTxn ||
		commitment.poolEpoch != c.pool.epoch ||
		commitment.poolGeneration != c.pool.generation ||
		commitment.operationSequence != c.pool.operationSequence ||
		commitment.checkpointSequence != c.pool.checkpointSequence ||
		commitment.activeOperationID != c.pool.activeOperationID ||
		commitment.operationStartEpoch != c.pool.operationStartEpoch ||
		commitment.activeCheckpointID != c.pool.activeCheckpointID ||
		commitment.workID != slot.workID ||
		commitment.generation != slot.generation ||
		commitment.nonce != slot.nonce ||
		journal.scopeID != commitment.scopeID ||
		journal.anchor != commitment.anchor ||
		journal.phase != commitment.phase ||
		journal.operation != commitment.operation ||
		journal.checkpoint != commitment.checkpoint ||
		journal.operation.pool != c.pool ||
		journal.operation.poolEpoch != c.pool.epoch ||
		journal.operation.id != c.pool.operationSequence+1 ||
		journal.operation.pendingTxn != c.pool.pendingTxn ||
		journal.operation.generation != c.pool.generation+1 ||
		journal.operation.scopeID != slot.scopePlan.scopeID ||
		journal.operation.scopeAnchor != slot.scopePlan.anchor ||
		journal.operation.startEpoch !=
			slot.scopePlan.mutationEpoch+uint64(slot.scopePlan.visits) ||
		journal.checkpoint.pool != c.pool ||
		journal.checkpoint.poolEpoch != c.pool.epoch ||
		journal.checkpoint.id != c.pool.checkpointSequence+1 ||
		journal.checkpoint.generation != c.pool.generation+1 ||
		journal.checkpoint.indexRoot != c.pool.indexRoot ||
		journal.checkpoint.pendingPageCount != c.pool.pendingPageCount {
		c.pool.abortRequired = true
		return nil, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance,
		}
	}
	return slot, privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) registerPreparedWork(
	slot *privateWriterFixedPointPreparedWork,
) privateWriterFixedPointError {
	if c == nil || c.self != c || slot == nil || slot.self != slot ||
		slot.coordinator != c || slot.phase != privateWriterFixedPointWorkPrepared ||
		c.activePrepared != nil || c.predecessorUsed || c.workFence != nil {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrStalePredecessor}
	}
	c.predecessorUsed = true
	c.predecessorGeneration++
	c.carried = privateWriterCarriedSource{
		identity: slot.progress.sourceIdentity,
		ordinal:  slot.progress.ordinal,
		lastPage: slot.progress.lastPage,
		epoch:    slot.progress.sourceEpoch,
	}
	c.activePrepared = slot
	slot.phase = privateWriterFixedPointWorkRegistered
	return privateWriterFixedPointError{}
}

func (p *privatePagePool) registerPreparedCoordinatorWork(
	slot *privateWriterFixedPointPreparedWork,
) privatePagePoolError {
	if p == nil || p.self != p || slot == nil || slot.self != slot ||
		p.coordinatorSessionID == 0 ||
		slot.sessionID != p.coordinatorSessionID ||
		slot.sessionGeneration != p.coordinatorSessionGeneration ||
		slot.phase != privateWriterFixedPointWorkRegistered ||
		p.registeredWorkID != 0 || p.registeredWorkFence != nil ||
		p.unacceptedScopes != 0 || p.mutationEpoch != slot.poolMutationEpoch ||
		p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrStaleToken}
	}
	p.registeredWorkID = slot.workID
	p.registeredWorkGeneration = slot.generation
	p.registeredWorkPhase = uint8(privateWriterFixedPointWorkRegistered)
	p.registeredWorkStartEpoch = p.mutationEpoch
	p.registeredWorkMutation = false
	p.registeredScopeID = 0
	p.registeredScopeAnchor = privatePagePoolNoIndex
	return privatePagePoolError{}
}

func (c *privateWriterTransactionCore) installPreparedWorkFence(
	slot *privateWriterFixedPointPreparedWork,
) privatePagePoolError {
	if c == nil || c.self != c || slot == nil || slot.self != slot ||
		slot.coordinator != &c.fixedPointCoordinator ||
		c.fixedPointCoordinator.activePrepared != slot ||
		slot.phase != privateWriterFixedPointWorkRegistered ||
		c.fixedPointWorkFence != nil || c.fixedPointCoordinator.workFence != nil ||
		c.pool.registeredWorkFence != nil ||
		c.fixedPointRegisteredWorkID != slot.workID ||
		c.fixedPointRegisteredWorkGeneration != slot.generation ||
		c.fixedPointRegisteredWorkPhase != privateWriterFixedPointWorkRegistered ||
		c.pool.registeredWorkID != slot.workID ||
		c.pool.registeredWorkGeneration != slot.generation ||
		c.pool.registeredWorkPhase != uint8(privateWriterFixedPointWorkRegistered) {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	fence := &slot.fence
	*fence = privateWriterWorkFence{
		self: fence, slot: slot, coordinator: &c.fixedPointCoordinator,
		core: c, pool: &c.pool, sessionID: slot.sessionID,
		sessionGeneration: slot.sessionGeneration, workID: slot.workID,
		generation: slot.generation, nonce: slot.nonce,
		scopeAnchor: privatePagePoolNoIndex,
		phase:       privateWriterFixedPointWorkRegistered,
	}
	c.fixedPointWorkFence = fence
	c.fixedPointCoordinator.workFence = fence
	c.pool.registeredWorkFence = fence
	return privatePagePoolError{}
}

func (p *privatePagePool) validateWorkFence(
	fence *privateWriterWorkFence,
	phase privateWriterFixedPointWorkPhase,
) privatePagePoolError {
	if p == nil || p.self != p || fence == nil || fence.self != fence ||
		fence.pool != p || fence.slot == nil || fence.slot.self != fence.slot ||
		fence != &fence.slot.fence || fence.slot.fence.self != fence ||
		fence.coordinator == nil || fence.coordinator.self != fence.coordinator ||
		fence.core == nil || fence.core.self != fence.core ||
		fence.coordinator.pool != p || &fence.core.pool != p ||
		fence.slot.coordinator != fence.coordinator ||
		fence.coordinator.transactionCore != fence.core ||
		fence.coordinator.activePrepared != fence.slot ||
		!fence.slot.coordinatorSeal.matchesRegistered(fence.coordinator, fence.slot) ||
		fence.coordinator.incarnation != fence.sessionID ||
		!fence.coordinator.predecessorUsed ||
		fence.coordinator.predecessorGeneration != fence.sessionGeneration+1 ||
		fence.coordinator.root != fence.slot.root ||
		fence.coordinator.pageCount != fence.slot.pageCount ||
		fence.coordinator.recordLen != fence.slot.recordLen ||
		fence.coordinator.lastWorkUnit != fence.slot.lastWorkUnit ||
		fence.core.fixedPointWorkFence != fence ||
		fence.coordinator.workFence != fence ||
		p.registeredWorkFence != fence ||
		fence.core.state != privateWriterTransactionPending ||
		!fence.core.fixedPointActive ||
		fence.core.fixedPointFinished ||
		!fence.core.fixedPointPreparedMode ||
		!fence.core.fixedPointWorkActive ||
		fence.core.fixedPointSessionID != fence.sessionID ||
		fence.core.fixedPointSessionGeneration != fence.sessionGeneration ||
		fence.phase != phase || fence.slot.phase != phase ||
		fence.workID == 0 || fence.workID != fence.slot.workID ||
		fence.generation == 0 || fence.generation != fence.slot.generation ||
		fence.nonce == 0 || fence.nonce != fence.slot.nonce ||
		fence.sessionID == 0 || fence.sessionID != fence.slot.sessionID ||
		fence.sessionGeneration == 0 ||
		fence.sessionGeneration != fence.slot.sessionGeneration ||
		fence.sessionID != p.coordinatorSessionID ||
		fence.sessionGeneration != p.coordinatorSessionGeneration ||
		p.abortRequired || p.coordinatorCleanupPending != 0 ||
		p.registeredWorkID != fence.workID ||
		p.registeredWorkGeneration != fence.generation ||
		p.registeredWorkPhase != uint8(phase) ||
		p.registeredWorkStartEpoch != fence.slot.poolMutationEpoch ||
		fence.core.fixedPointRegisteredWorkID != fence.workID ||
		fence.core.fixedPointRegisteredWorkGeneration != fence.generation ||
		fence.core.fixedPointRegisteredWorkPhase != phase {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	switch phase {
	case privateWriterFixedPointWorkRegistered:
		if p.registeredWorkMutation ||
			p.registeredScopeID != 0 ||
			p.registeredScopeAnchor != privatePagePoolNoIndex ||
			p.unacceptedScopes != 0 ||
			p.activeScopes != 0 ||
			fence.scopeID != 0 ||
			fence.scopeAnchor != privatePagePoolNoIndex {
			return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
		}
	case privateWriterFixedPointWorkActive:
		if fence.slot.scopePlan.mutationEpoch != p.registeredWorkStartEpoch ||
			fence.slot.scopePlan.mutationEpoch >
				^uint64(0)-uint64(fence.slot.scopePlan.visits) ||
			p.mutationEpoch <
				fence.slot.scopePlan.mutationEpoch+uint64(fence.slot.scopePlan.visits) ||
			!p.registeredWorkMutation ||
			p.registeredScopeID != fence.slot.scopePlan.scopeID ||
			p.registeredScopeAnchor != fence.slot.scopePlan.anchor ||
			p.unacceptedScopes != 1 ||
			p.activeScopes != 1 ||
			fence.scopeID != fence.slot.scopePlan.scopeID ||
			fence.scopeAnchor != fence.slot.scopePlan.anchor {
			return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
		}
	default:
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) authorizeCoordinatorMutation(
	phase privateWriterFixedPointWorkPhase,
	scope *privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if p == nil || p.self != p {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.coordinatorSessionID == 0 {
		if len(fences) != 0 {
			return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
		}
		return privatePagePoolError{}
	}
	if len(fences) != 1 {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	fence := fences[0]
	if problem := p.validateWorkFence(fence, phase); problem.failed() {
		return problem
	}
	if scope != nil &&
		(fence.scopeID != scope.id || fence.scopeAnchor != scope.anchor ||
			fence.slot == nil || fence.slot.scopePlan.scopeID != scope.id ||
			fence.slot.scopePlan.anchor != scope.anchor) {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) authorizeCoordinatorSlotMutation(
	phase privateWriterFixedPointWorkPhase,
	index int,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if p == nil || p.self != p {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.coordinatorSessionID == 0 {
		return p.authorizeCoordinatorMutation(phase, nil, fences...)
	}
	if index < 0 || index >= len(p.slots) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot := &p.slots[index]
	scope := privatePageReservationScope{
		id: slot.scopeID, anchor: slot.scopeAnchorIndex,
	}
	if scope.id == 0 || scope.anchor == privatePagePoolNoIndex {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	if problem := p.authorizeCoordinatorMutation(
		phase, &scope, fences...,
	); problem.failed() {
		return problem
	}
	fence := fences[0]
	journal := &fence.slot.terminalJournal
	commitment := fence.slot.terminalCommitment
	if journal.self != journal ||
		commitment.workID != fence.workID ||
		commitment.generation != fence.generation ||
		commitment.nonce != fence.nonce ||
		journal.scopeID != commitment.scopeID ||
		journal.anchor != commitment.anchor ||
		journal.phase != commitment.phase ||
		journal.operation != commitment.operation ||
		journal.checkpoint != commitment.checkpoint ||
		journal.scopeID != slot.scopeID ||
		journal.anchor != slot.scopeAnchorIndex ||
		(journal.phase != privateWriterPreparedTerminalOperationActive &&
			journal.phase != privateWriterPreparedTerminalCheckpointActive) {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	if !p.matchesPreparedTerminalLifecycle(commitment, journal.phase) {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) authorizeCoordinatorTerminalSlotMutation(
	index int,
	phase privateWriterPreparedTerminalPhase,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	if p.coordinatorSessionID == 0 {
		return privatePagePoolError{}
	}
	journal := &fences[0].slot.terminalJournal
	commitment := fences[0].slot.terminalCommitment
	if journal.self != journal || journal.phase != phase ||
		commitment.phase != phase ||
		journal.scopeID != commitment.scopeID ||
		journal.anchor != commitment.anchor ||
		journal.operation != commitment.operation ||
		journal.checkpoint != commitment.checkpoint {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) authorizeCoordinatorTerminalOperation(
	operation privatePagePoolOperation,
	phase privateWriterPreparedTerminalPhase,
	fences ...*privateWriterWorkFence,
) (*privateWriterPreparedTerminalJournal, privatePagePoolError) {
	if p == nil || p.self != p {
		return nil, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.coordinatorSessionID == 0 {
		if problem := p.authorizeCoordinatorMutation(
			privateWriterFixedPointWorkActive, nil, fences...,
		); problem.failed() {
			return nil, problem
		}
		return nil, privatePagePoolError{}
	}
	if len(fences) != 1 {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	fence := fences[0]
	scope := privatePageReservationScope{
		id: operation.scopeID, anchor: operation.scopeAnchor,
	}
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fence,
	); problem.failed() {
		return nil, problem
	}
	journal := &fence.slot.terminalJournal
	commitment := fence.slot.terminalCommitment
	if journal.self != journal || journal.phase != phase ||
		commitment.phase != phase ||
		commitment.workID != fence.workID ||
		commitment.generation != fence.generation ||
		commitment.nonce != fence.nonce ||
		commitment.scopeID != fence.scopeID ||
		commitment.anchor != fence.scopeAnchor ||
		journal.scopeID != commitment.scopeID ||
		journal.anchor != commitment.anchor ||
		journal.operation != commitment.operation ||
		journal.checkpoint != commitment.checkpoint ||
		commitment.operation != operation {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	if !p.matchesPreparedTerminalLifecycle(commitment, phase) {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return journal, privatePagePoolError{}
}

func (p *privatePagePool) authorizeCoordinatorTerminalCheckpoint(
	checkpoint privatePagePoolCheckpoint,
	scope *privatePageReservationScope,
	phase privateWriterPreparedTerminalPhase,
	fences ...*privateWriterWorkFence,
) (*privateWriterPreparedTerminalJournal, privatePagePoolError) {
	if p == nil || p.self != p {
		return nil, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.coordinatorSessionID == 0 {
		if problem := p.authorizeCoordinatorMutation(
			privateWriterFixedPointWorkActive, scope, fences...,
		); problem.failed() {
			return nil, problem
		}
		return nil, privatePagePoolError{}
	}
	if len(fences) != 1 {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	fence := fences[0]
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, scope, fence,
	); problem.failed() {
		return nil, problem
	}
	journal := &fence.slot.terminalJournal
	commitment := fence.slot.terminalCommitment
	if journal.self != journal || journal.phase != phase ||
		commitment.phase != phase ||
		commitment.workID != fence.workID ||
		commitment.generation != fence.generation ||
		commitment.nonce != fence.nonce ||
		commitment.scopeID != fence.scopeID ||
		commitment.anchor != fence.scopeAnchor ||
		journal.scopeID != commitment.scopeID ||
		journal.anchor != commitment.anchor ||
		journal.operation != commitment.operation ||
		journal.checkpoint != commitment.checkpoint ||
		commitment.checkpoint != checkpoint {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	if !p.matchesPreparedTerminalLifecycle(commitment, phase) {
		return nil, privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return journal, privatePagePoolError{}
}

func (p *privatePagePool) matchesPreparedTerminalLifecycle(
	commitment privateWriterPreparedTerminalCommitment,
	phase privateWriterPreparedTerminalPhase,
) bool {
	operation := commitment.operation
	checkpoint := commitment.checkpoint
	if p == nil || p.self != p ||
		commitment.phase != phase ||
		commitment.pendingTxn != p.pendingTxn ||
		commitment.poolEpoch != p.epoch ||
		commitment.poolGeneration != p.generation ||
		commitment.operationSequence != p.operationSequence ||
		commitment.checkpointSequence != p.checkpointSequence ||
		commitment.activeOperationID != p.activeOperationID ||
		commitment.operationStartEpoch != p.operationStartEpoch ||
		commitment.activeCheckpointID != p.activeCheckpointID ||
		operation.pool != p || checkpoint.pool != p ||
		operation.poolEpoch != p.epoch || checkpoint.poolEpoch != p.epoch ||
		operation.pendingTxn != p.pendingTxn ||
		operation.id == 0 || checkpoint.id == 0 ||
		operation.generation == 0 ||
		operation.generation != checkpoint.generation {
		return false
	}
	baseGeneration := operation.generation - 1
	baseOperationSequence := operation.id - 1
	baseCheckpointSequence := checkpoint.id - 1
	switch phase {
	case privateWriterPreparedTerminalReady:
		return commitment.poolGeneration == baseGeneration &&
			commitment.operationSequence == baseOperationSequence &&
			commitment.checkpointSequence == baseCheckpointSequence &&
			commitment.activeOperationID == 0 &&
			commitment.operationStartEpoch == 0 &&
			commitment.activeCheckpointID == 0 &&
			p.indexRoot == checkpoint.indexRoot &&
			p.pendingPageCount == checkpoint.pendingPageCount
	case privateWriterPreparedTerminalOperationActive:
		return commitment.poolGeneration == baseGeneration &&
			commitment.operationSequence == operation.id &&
			commitment.checkpointSequence == baseCheckpointSequence &&
			commitment.activeOperationID == operation.id &&
			commitment.operationStartEpoch == operation.startEpoch &&
			commitment.activeCheckpointID == 0
	case privateWriterPreparedTerminalCheckpointActive:
		return commitment.poolGeneration == baseGeneration &&
			commitment.operationSequence == baseOperationSequence &&
			commitment.checkpointSequence == checkpoint.id &&
			commitment.activeOperationID == 0 &&
			commitment.operationStartEpoch == 0 &&
			commitment.activeCheckpointID == checkpoint.id
	case privateWriterPreparedTerminalConsumed:
		operationConsumed := commitment.poolGeneration == operation.generation &&
			commitment.operationSequence == operation.id &&
			commitment.checkpointSequence == baseCheckpointSequence
		checkpointConsumed := commitment.poolGeneration == checkpoint.generation &&
			commitment.operationSequence == baseOperationSequence &&
			commitment.checkpointSequence == checkpoint.id
		checkpointRolledBack := commitment.poolGeneration == baseGeneration &&
			commitment.operationSequence == baseOperationSequence &&
			commitment.checkpointSequence == checkpoint.id
		return (operationConsumed || checkpointConsumed || checkpointRolledBack) &&
			commitment.activeOperationID == 0 &&
			commitment.operationStartEpoch == 0 &&
			commitment.activeCheckpointID == 0
	default:
		return false
	}
}

func (c *privateWriterPreparedTerminalCommitment) setOperationActive() {
	c.phase = privateWriterPreparedTerminalOperationActive
	c.operationSequence = c.operation.id
	c.activeOperationID = c.operation.id
	c.operationStartEpoch = c.operation.startEpoch
}

func (c *privateWriterPreparedTerminalCommitment) setCheckpointActive() {
	c.phase = privateWriterPreparedTerminalCheckpointActive
	c.checkpointSequence = c.checkpoint.id
	c.activeCheckpointID = c.checkpoint.id
}

func (c *privateWriterPreparedTerminalCommitment) setOperationConsumed() {
	c.phase = privateWriterPreparedTerminalConsumed
	c.poolGeneration = c.operation.generation
	c.activeOperationID = 0
	c.operationStartEpoch = 0
}

func (c *privateWriterPreparedTerminalCommitment) setCheckpointConsumed() {
	c.phase = privateWriterPreparedTerminalConsumed
	c.poolGeneration = c.checkpoint.generation
	c.activeCheckpointID = 0
}

func (c *privateWriterPreparedTerminalCommitment) setCheckpointRolledBack() {
	c.phase = privateWriterPreparedTerminalConsumed
	c.activeCheckpointID = 0
}

func (p *privatePagePool) applyPreparedCoordinatorScope(
	fence *privateWriterWorkFence,
) (privatePageReservationScope, privatePagePoolError) {
	if fence == nil || fence.slot == nil {
		return privatePageReservationScope{}, privatePagePoolError{
			code: privatePagePoolErrCoordinatorRequired,
		}
	}
	if problem := p.validateWorkFence(
		fence, privateWriterFixedPointWorkRegistered,
	); problem.failed() {
		return privatePageReservationScope{}, problem
	}
	slot := fence.slot
	if p.registeredWorkMutation || p.registeredScopeID != 0 ||
		p.registeredScopeAnchor != privatePagePoolNoIndex ||
		p.unacceptedScopes != 0 || p.mutationEpoch != slot.poolMutationEpoch {
		return privatePageReservationScope{}, privatePagePoolError{
			code: privatePagePoolErrStaleToken,
		}
	}
	if problem := p.revalidatePreparedScope(slot.scopePlan); problem.failed() {
		return privatePageReservationScope{}, problem
	}

	// All fallible checks end here. The canonical fence is installed in every
	// owner before this mutation-start marker is written.
	p.registeredWorkMutation = true
	plan := slot.scopePlan
	for assigned, encoded := range plan.nodes {
		member := int(encoded - 1)
		pageSlot := &p.slots[member]
		scopeNext := privatePagePoolNoIndex
		if assigned+1 < plan.visits {
			scopeNext = int(plan.nodes[assigned+1] - 1)
		}
		pageSlot.scopeID = plan.scopeID
		pageSlot.scopeAnchorIndex = plan.anchor
		pageSlot.scopeVacantNext = scopeNext
		pageSlot.scopeMemberNext = scopeNext
		pageSlot.unscopedNext = privatePagePoolNoIndex
		pageSlot.unscopedPrevious = privatePagePoolNoIndex
		pageSlot.epoch++
		p.advanceMutationPrepared()
	}
	p.unscopedVacantHead = plan.remainingHead
	p.unscopedVacantCount = plan.remainingCount
	if plan.remainingHead == privatePagePoolNoIndex {
		p.unscopedVacantTail = privatePagePoolNoIndex
	} else {
		p.slots[plan.remainingHead].unscopedPrevious = privatePagePoolNoIndex
	}
	anchorSlot := &p.slots[plan.anchor]
	anchorSlot.scopeAnchor = true
	anchorSlot.scopeRoot = privatePagePoolNoIndex
	anchorSlot.scopeVacantHead = plan.anchor
	anchorSlot.scopeMemberHead = plan.anchor
	anchorSlot.scopeCapacity = plan.visits
	anchorSlot.scopeBound = 0
	anchorSlot.scopeGeneration = 1
	anchorSlot.scopeSealed = false
	anchorSlot.scopeSuccessor = 0
	anchorSlot.successorConsumed = false
	p.scopeSequence = plan.scopeID
	p.activeScopes++
	p.registeredWorkPhase = uint8(privateWriterFixedPointWorkActive)
	p.registeredScopeID = plan.scopeID
	p.registeredScopeAnchor = plan.anchor
	p.unacceptedScopes = 1
	fence.scopeID = plan.scopeID
	fence.scopeAnchor = plan.anchor
	return privatePageReservationScope{
		pool: p, poolEpoch: p.epoch, id: plan.scopeID, generation: 1,
		pendingTxn: p.pendingTxn, anchor: plan.anchor,
	}, privatePagePoolError{}
}

func (c *privateWriterFixedPointCoordinator) activatePreparedWork(
	slot *privateWriterFixedPointPreparedWork,
) privateWriterFixedPointActiveToken {
	slot.phase = privateWriterFixedPointWorkActive
	slot.fence.phase = privateWriterFixedPointWorkActive
	c.workFence.phase = privateWriterFixedPointWorkActive
	c.transactionCore.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkActive
	return privateWriterFixedPointActiveToken{
		coordinator: c, workID: slot.workID,
		generation: slot.generation, nonce: slot.nonce,
	}
}
