package exactv4

import "sync/atomic"

type privateWriterTransactionState uint8

const (
	privateWriterTransactionClean privateWriterTransactionState = iota + 1
	privateWriterTransactionPending
	privateWriterTransactionAbortRequired
	privateWriterTransactionAbortIncomplete
)

type privateWriterTransactionErrorCode uint8

const (
	privateWriterTransactionErrInvalidArgument privateWriterTransactionErrorCode = iota + 1
	privateWriterTransactionErrTransactionExhausted
	privateWriterTransactionErrInsufficientBudget
	privateWriterTransactionErrAlreadyPending
	privateWriterTransactionErrNoPendingTransaction
	privateWriterTransactionErrAbortRequired
	privateWriterTransactionErrAbortIncomplete
	privateWriterTransactionErrStaleHandle
	privateWriterTransactionErrPool
	privateWriterTransactionErrFixedPoint
	privateWriterTransactionErrCallbackActive
)

type privateWriterTransactionError struct {
	code         privateWriterTransactionErrorCode
	pool         privatePagePoolError
	resource     privateWriterResourceError
	cleanup      privateWriterCleanupError
	coordination privateWriterCoordinationError
	fixedPoint   privateWriterFixedPointError
	required     uint64
	actual       uint64
}

func (e privateWriterTransactionError) failed() bool { return e.code != 0 }

type privateWriterTransactionHandle struct {
	core  *privateWriterTransactionCore
	epoch uint64
}

var privateWriterHandleIncarnation atomic.Uint64

func reservePrivateWriterHandleIncarnations() (uint64, uint64, bool) {
	for {
		current := privateWriterHandleIncarnation.Load()
		if current > ^uint64(0)-2 {
			return 0, 0, false
		}
		if privateWriterHandleIncarnation.CompareAndSwap(current, current+2) {
			return current + 1, current + 2, true
		}
	}
}

// privateWriterTransactionCore is the sole owner of one unpublished draft.
// It deliberately contains no publication logic.
type privateWriterTransactionCore struct {
	self        *privateWriterTransactionCore
	selected    Meta
	target      Meta
	resources   privateWriterResourceLedger
	state       privateWriterTransactionState
	handleEpoch uint64
	abortEpoch  uint64

	pool           privatePagePool
	poolSlots      []privatePagePoolSlot
	poolValidation []uint32
	cleanup        privateWriterCleanupLedger
	coordination   privateWriterCoordinationCleanup
	abortVisits    uint64
	abortScrubbed  bool
	workspace      *privateWriterWorkspace

	fixedPointCoordinator              privateWriterFixedPointCoordinator
	fixedPointPredecessor              privateWriterFixedPointPredecessor
	fixedPointActive                   bool
	fixedPointFinished                 bool
	fixedPointPreparedMode             bool
	fixedPointWorkActive               bool
	fixedPointSessionID                uint64
	fixedPointSessionGeneration        uint64
	fixedPointRegisteredWorkID         uint64
	fixedPointRegisteredWorkGeneration uint64
	fixedPointRegisteredWorkPhase      privateWriterFixedPointWorkPhase
	fixedPointWorkFence                *privateWriterWorkFence
	fixedPointRoot                     uint32
	fixedPointPageCount                uint64
	fixedPointPoolEpoch                uint64
}

func initPrivateWriterTransactionCoreWithWorkspace(
	core *privateWriterTransactionCore,
	selected Meta,
	budget privateWriterResourceBudget,
	workspace *privateWriterWorkspace,
	cleanupObligations []privateWriterCleanupObligation,
	cleanupOwners []privateWriterCleanupOwner,
) privateWriterTransactionError {
	if workspace == nil || workspace.self != workspace ||
		workspace.layout.bytes == 0 || workspace.partitionBytes == 0 ||
		workspace.writerHeapBudget != budget.maxHeapBytes ||
		workspace.partitionBytes > budget.maxHeapBytes ||
		uint64(len(workspace.poolSlots)) > budget.maxPrivatePages {
		return privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
	}
	effectiveBudget := budget
	effectiveBudget.maxHeapBytes -= workspace.partitionBytes
	problem := initPrivateWriterTransactionCore(
		core, selected, effectiveBudget,
		workspace.poolSlots, workspace.poolValidation,
		cleanupObligations, cleanupOwners,
	)
	if problem.failed() {
		return problem
	}
	core.workspace = workspace
	return privateWriterTransactionError{}
}

func initPrivateWriterTransactionCore(
	core *privateWriterTransactionCore,
	selected Meta,
	budget privateWriterResourceBudget,
	poolSlots []privatePagePoolSlot,
	poolValidation []uint32,
	cleanupObligations []privateWriterCleanupObligation,
	cleanupOwners []privateWriterCleanupOwner,
) privateWriterTransactionError {
	if core == nil || selected.DatabaseID == ([16]byte{}) || selected.TxnID == 0 ||
		selected.CommitNonce == ([16]byte{}) || selected.PageCount < 2 || selected.PageCount > MaxPageCount ||
		len(cleanupObligations) != len(cleanupOwners) {
		return privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
	}
	handleEpoch := uint64(0)
	if core.self == core {
		if budget != core.resources.budget {
			return privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
		}
		if core.state != privateWriterTransactionClean || core.cleanup.length != 0 || core.abortScrubbed ||
			!core.resources.empty() ||
			derivePrivateWriterCleanupState(&core.cleanup, &core.coordination) != privateWriterCleanupClean {
			code := privateWriterTransactionErrAlreadyPending
			if core.state == privateWriterTransactionAbortIncomplete ||
				core.state == privateWriterTransactionAbortRequired ||
				core.cleanup.length != 0 || core.abortScrubbed {
				code = privateWriterTransactionErrAbortIncomplete
			}
			return privateWriterTransactionError{code: code}
		}
		if core.handleEpoch == 0 {
			if core.abortEpoch != 0 {
				return privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
			}
		} else if core.abortEpoch != 0 {
			return privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
		}
		handleEpoch = core.handleEpoch
	}
	if uint64(len(poolSlots)) > budget.maxPrivatePages || len(poolValidation) < len(poolSlots) {
		return privateWriterTransactionError{
			code:     privateWriterTransactionErrInsufficientBudget,
			required: uint64(len(poolSlots)), actual: budget.maxPrivatePages,
		}
	}
	*core = privateWriterTransactionCore{
		self: core, selected: selected,
		state: privateWriterTransactionClean, handleEpoch: handleEpoch,
		poolSlots: poolSlots, poolValidation: poolValidation,
	}
	if problem := initPrivateWriterResourceLedger(&core.resources, budget); problem.failed() {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrInvalidArgument, resource: problem,
		}
	}
	if problem := initPrivateWriterCleanupLedger(
		&core.cleanup, cleanupObligations, cleanupOwners,
	); problem.failed() {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrInvalidArgument, cleanup: problem,
		}
	}
	if problem := initPrivateWriterNoCoordinationCleanup(&core.coordination); problem.failed() {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrInvalidArgument, coordination: problem,
		}
	}
	return privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) begin(
	commitNonce [16]byte,
) (privateWriterTransactionHandle, privateWriterTransactionError) {
	if c == nil || c.self != c || commitNonce == ([16]byte{}) {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
	}
	if c.state != privateWriterTransactionClean {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrAlreadyPending}
	}
	targetTxn := c.selected.TxnID + 1
	if targetTxn == 0 {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrTransactionExhausted}
	}
	if c.cleanup.length != 0 {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrAbortIncomplete}
	}
	if !c.resources.empty() ||
		derivePrivateWriterCleanupState(&c.cleanup, &c.coordination) != privateWriterCleanupClean {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrAbortIncomplete}
	}
	// Reserve the returned handle incarnation and mandatory later Abort
	// invalidation together. Consuming an unused pair after a later neutral
	// failure is safe; reusing either identity is not.
	handleEpoch, abortEpoch, ok := reservePrivateWriterHandleIncarnations()
	if !ok {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrTransactionExhausted}
	}
	if c.workspace != nil {
		if problem := c.workspace.resetForTransaction(); problem.failed() {
			return privateWriterTransactionHandle{}, privateWriterTransactionError{
				code: privateWriterTransactionErrInvalidArgument,
			}
		}
	} else {
		clear(c.poolValidation)
	}
	if problem := initVacantPrivatePagePoolForDraft(
		&c.pool, c.poolSlots, c.selected.PageCount, c.selected.PageCount, targetTxn,
	); problem.failed() {
		return privateWriterTransactionHandle{}, privateWriterTransactionError{code: privateWriterTransactionErrPool, pool: problem}
	}
	c.target = c.selected
	c.target.TxnID = targetTxn
	c.target.CommitNonce = commitNonce
	c.state = privateWriterTransactionPending
	c.abortVisits = 0
	c.abortScrubbed = false
	c.handleEpoch = handleEpoch
	c.abortEpoch = abortEpoch
	c.fixedPointCoordinator = privateWriterFixedPointCoordinator{}
	c.fixedPointPredecessor = privateWriterFixedPointPredecessor{}
	c.fixedPointActive = false
	c.fixedPointFinished = false
	c.fixedPointPreparedMode = false
	c.fixedPointWorkActive = false
	c.fixedPointSessionID = 0
	c.fixedPointSessionGeneration = 0
	c.fixedPointRegisteredWorkID = 0
	c.fixedPointRegisteredWorkGeneration = 0
	c.fixedPointRegisteredWorkPhase = 0
	c.fixedPointWorkFence = nil
	c.fixedPointRoot = 0
	c.fixedPointPageCount = 0
	c.fixedPointPoolEpoch = 0
	return privateWriterTransactionHandle{core: c, epoch: c.handleEpoch}, privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) validateHandle(
	handle privateWriterTransactionHandle,
) privateWriterTransactionError {
	if c == nil || c.self != c || handle.core != c || handle.epoch == 0 || handle.epoch != c.handleEpoch {
		return privateWriterTransactionError{code: privateWriterTransactionErrStaleHandle}
	}
	if c.callbackActive() {
		return privateWriterTransactionError{code: privateWriterTransactionErrCallbackActive}
	}
	return privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) operationFailed(
	handle privateWriterTransactionHandle,
	operation privatePagePoolOperation,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if c.state != privateWriterTransactionPending {
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	problem := c.pool.abortOperation(operation)
	if !problem.failed() {
		return privateWriterTransactionError{}
	}
	if problem.code == privatePagePoolErrAbortRequired {
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired, pool: problem}
	}
	return privateWriterTransactionError{code: privateWriterTransactionErrPool, pool: problem}
}

func (c *privateWriterTransactionCore) startFixedPoint(
	handle privateWriterTransactionHandle,
	selected committedPageSource,
	root uint32,
	records []privateWriterSealedBitmapWorkUnitRecord,
	slotRecords []int,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if c.state != privateWriterTransactionPending || c.pool.abortRequired {
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	if c.fixedPointActive || c.fixedPointFinished {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{
				code: privateWriterFixedPointErrStalePredecessor,
			},
		}
	}
	storageReady := c.workspace != nil && c.workspace.self == c.workspace &&
		len(records) == len(c.workspace.records) &&
		len(slotRecords) == len(c.workspace.slotRecords) &&
		len(records) != 0 && len(slotRecords) != 0 &&
		&records[0] == &c.workspace.records[0] &&
		&slotRecords[0] == &c.workspace.slotRecords[0]
	poolStatus, poolProblem := c.pool.status()
	if poolProblem.failed() {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrPool, pool: poolProblem,
		}
	}
	c.target.PageCount = poolStatus.pendingPageCount
	predecessor, fixedProblem := initializePrivateWriterFixedPointCoordinatorWithStorage(
		&c.fixedPointCoordinator,
		&c.pool,
		selected,
		c.selected.TxnID,
		c.target.TxnID,
		c.selected.PageCount,
		root,
		poolStatus.pendingPageCount,
		records,
		slotRecords,
		storageReady,
	)
	if fixedProblem.failed() {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
		}
	}
	c.fixedPointPredecessor = predecessor
	c.fixedPointActive = true
	return privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) startPreparedFixedPoint(
	handle privateWriterTransactionHandle,
	selected committedPageSource,
	root uint32,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if c.workspace == nil || c.workspace.self != c.workspace ||
		len(c.workspace.records) == 0 ||
		len(c.workspace.slotRecords) < len(c.pool.slots) {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrInvalidArgument,
		}
	}
	if problem := c.startFixedPoint(
		handle, selected, root, c.workspace.records, c.workspace.slotRecords,
	); problem.failed() {
		return problem
	}
	c.fixedPointCoordinator.transactionCore = c
	if fixedProblem := c.fixedPointCoordinator.configureWorkspace(c.workspace); fixedProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired, fixedPoint: fixedProblem,
		}
	}
	if poolProblem := c.pool.beginCoordinatorSession(
		c.fixedPointCoordinator.incarnation,
		c.fixedPointCoordinator.predecessorGeneration,
	); poolProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired, pool: poolProblem,
		}
	}
	c.fixedPointPreparedMode = true
	c.fixedPointSessionID = c.pool.coordinatorSessionID
	c.fixedPointSessionGeneration = c.pool.coordinatorSessionGeneration
	return privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) prepareFixedPointWork(
	handle privateWriterTransactionHandle,
	request privateWriterFixedPointPrepareRequest,
) (privateWriterFixedPointPreparedToken, privateWriterTransactionError) {
	if problem := c.validateHandle(handle); problem.failed() {
		return privateWriterFixedPointPreparedToken{}, problem
	}
	if c.state != privateWriterTransactionPending || !c.fixedPointActive ||
		c.fixedPointFinished || !c.fixedPointPreparedMode ||
		c.fixedPointWorkActive || c.pool.abortRequired {
		return privateWriterFixedPointPreparedToken{}, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired,
		}
	}
	if !c.preparedFixedPointAuthorityConsistent() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointPreparedToken{}, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired,
		}
	}
	if c.workspace.nextPreparedSlot < 0 ||
		c.workspace.nextPreparedSlot >= len(c.workspace.preparedSlots) {
		return privateWriterFixedPointPreparedToken{}, privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{
				code: privateWriterFixedPointErrExhausted,
			},
		}
	}
	index := c.workspace.nextPreparedSlot
	slot := &c.workspace.preparedSlots[index]
	scratch := c.workspace.scratch[index*c.workspace.scratchWordsPerSlot : (index+1)*c.workspace.scratchWordsPerSlot]
	*slot = privateWriterFixedPointPreparedWork{
		self: slot, coordinator: &c.fixedPointCoordinator, workspace: c.workspace,
		storageIndex: index, scratch: scratch,
		workspaceLayout: c.workspace.layoutGeneration,
	}
	token, fixedProblem := c.fixedPointCoordinator.prepareWork(
		c.fixedPointPredecessor, slot, request,
	)
	if fixedProblem.failed() {
		if c.pool.abortRequired {
			c.state = privateWriterTransactionAbortRequired
			return privateWriterFixedPointPreparedToken{}, privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, fixedPoint: fixedProblem,
			}
		}
		return privateWriterFixedPointPreparedToken{}, privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
		}
	}
	c.workspace.nextPreparedSlot++
	return token, privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) consumeFixedPointWork(
	handle privateWriterTransactionHandle,
	token privateWriterFixedPointPreparedToken,
) (
	privateWriterFixedPointActiveToken,
	privatePageReservationScope,
	privateWriterTransactionError,
) {
	if problem := c.validateHandle(handle); problem.failed() {
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{}, problem
	}
	if c.state != privateWriterTransactionPending || !c.fixedPointActive ||
		c.fixedPointFinished || !c.fixedPointPreparedMode ||
		c.fixedPointWorkActive || c.pool.abortRequired {
		if c.fixedPointWorkActive || c.pool.registeredWorkID != 0 {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
		}
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	if !c.preparedFixedPointAuthorityConsistent() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	slot, fixedProblem := c.fixedPointCoordinator.validatePreparedWork(token)
	if fixedProblem.failed() {
		if c.fixedPointCoordinator.predecessorUsed ||
			c.pool.registeredWorkID != 0 || c.pool.abortRequired {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
			return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
				privateWriterTransactionError{
					code: privateWriterTransactionErrAbortRequired, fixedPoint: fixedProblem,
				}
		}
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{
				code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
			}
	}
	if fixedProblem = c.fixedPointCoordinator.registerPreparedWork(slot); fixedProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, fixedPoint: fixedProblem,
			}
	}
	if poolProblem := c.pool.registerPreparedCoordinatorWork(slot); poolProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, pool: poolProblem,
			}
	}
	c.fixedPointWorkActive = true
	c.fixedPointRegisteredWorkID = slot.workID
	c.fixedPointRegisteredWorkGeneration = slot.generation
	c.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkRegistered
	if poolProblem := c.installPreparedWorkFence(slot); poolProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, pool: poolProblem,
			}
	}
	scope, poolProblem := c.pool.applyPreparedCoordinatorScope(c.fixedPointWorkFence)
	if poolProblem.failed() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointActiveToken{}, privatePageReservationScope{},
			privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, pool: poolProblem,
			}
	}
	active := c.fixedPointCoordinator.activatePreparedWork(slot)
	c.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkActive
	return active, scope, privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) fixedPointSource(
	handle privateWriterTransactionHandle,
) (*privateWriterDraftPageSource, privateWriterTransactionError) {
	if problem := c.validateHandle(handle); problem.failed() {
		return nil, problem
	}
	if c.state != privateWriterTransactionPending || !c.fixedPointActive ||
		c.fixedPointFinished || c.pool.abortRequired ||
		c.fixedPointPreparedMode ||
		c.fixedPointWorkActive || c.pool.registeredWorkID != 0 ||
		c.pool.unacceptedScopes != 0 {
		return nil, privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{
				code: privateWriterFixedPointErrStalePredecessor,
			},
		}
	}
	return c.fixedPointCoordinator.source(), privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) acceptFixedPointFinalized(
	handle privateWriterTransactionHandle,
	workUnit uint64,
	result freeBitmapFinalizationResult,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if c.state != privateWriterTransactionPending || !c.fixedPointActive ||
		c.fixedPointFinished || c.pool.abortRequired ||
		c.fixedPointWorkActive || c.pool.registeredWorkID != 0 ||
		c.pool.unacceptedScopes != 0 {
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	if c.fixedPointPreparedMode {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	successor, fixedProblem := c.fixedPointCoordinator.acceptFinalized(
		c.fixedPointPredecessor, workUnit, result,
	)
	if fixedProblem.failed() {
		if c.fixedPointCoordinator.predecessorUsed || c.pool.abortRequired {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
			return privateWriterTransactionError{
				code:       privateWriterTransactionErrAbortRequired,
				fixedPoint: fixedProblem,
			}
		}
		return privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
		}
	}
	c.fixedPointPredecessor = successor
	c.target.PageCount = successor.pageCount
	return privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) fixedPointOperationFailed(
	handle privateWriterTransactionHandle,
	fixedProblem privateWriterFixedPointError,
	mutated bool,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if !fixedProblem.failed() {
		return privateWriterTransactionError{}
	}
	if c.fixedPointPreparedMode {
		registered := c.fixedPointRegisteredWorkPhase != 0 ||
			c.pool.registeredWorkPhase != 0
		mutatedByState := registered &&
			(c.pool.registeredWorkMutation ||
				c.pool.mutationEpoch != c.pool.registeredWorkStartEpoch)
		if registered || mutatedByState {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
			return privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, fixedPoint: fixedProblem,
			}
		}
		return privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{
				code: privateWriterFixedPointErrInvalidArgument,
			},
		}
	}
	if mutated || c.pool.abortRequired {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{
			code:       privateWriterTransactionErrAbortRequired,
			fixedPoint: fixedProblem,
		}
	}
	return privateWriterTransactionError{
		code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
	}
}

func (c *privateWriterTransactionCore) finishFixedPoint(
	handle privateWriterTransactionHandle,
) (uint32, uint64, privateWriterTransactionError) {
	if problem := c.validateHandle(handle); problem.failed() {
		return 0, 0, problem
	}
	if c.state != privateWriterTransactionPending || !c.fixedPointActive ||
		c.fixedPointFinished || c.pool.abortRequired ||
		c.fixedPointWorkActive || c.pool.registeredWorkID != 0 ||
		c.pool.unacceptedScopes != 0 {
		return 0, 0, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired,
		}
	}
	if c.fixedPointPreparedMode && !c.preparedFixedPointAuthorityConsistent() {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return 0, 0, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired,
		}
	}
	root, pageCount, fixedProblem := c.fixedPointCoordinator.consumeFinal(
		c.fixedPointPredecessor,
	)
	if fixedProblem.failed() {
		return 0, 0, privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint, fixedPoint: fixedProblem,
		}
	}
	if c.fixedPointPreparedMode {
		if poolProblem := c.pool.finishCoordinatorSession(
			c.fixedPointSessionID, c.fixedPointSessionGeneration,
		); poolProblem.failed() {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
			return 0, 0, privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired, pool: poolProblem,
			}
		}
		c.fixedPointSessionID = 0
		c.fixedPointSessionGeneration = 0
	}
	c.fixedPointFinished = true
	c.fixedPointRoot = root
	c.fixedPointPageCount = pageCount
	c.fixedPointPoolEpoch = c.pool.mutationEpoch
	return root, pageCount, privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) preflightCommit(
	handle privateWriterTransactionHandle,
) privateWriterTransactionError {
	if problem := c.validateHandle(handle); problem.failed() {
		return problem
	}
	if c.state == privateWriterTransactionAbortRequired || c.state == privateWriterTransactionAbortIncomplete ||
		c.pool.abortRequired {
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	if c.state != privateWriterTransactionPending {
		return privateWriterTransactionError{code: privateWriterTransactionErrNoPendingTransaction}
	}
	readyScopeAnchor := 0
	if c.fixedPointPreparedMode {
		readyScopeAnchor = privatePagePoolNoIndex
	}
	if c.fixedPointWorkActive ||
		c.fixedPointRegisteredWorkID != 0 ||
		c.fixedPointRegisteredWorkGeneration != 0 ||
		c.fixedPointRegisteredWorkPhase != 0 ||
		c.fixedPointWorkFence != nil ||
		c.fixedPointCoordinator.activePrepared != nil ||
		c.fixedPointCoordinator.workFence != nil ||
		c.pool.registeredWorkID != 0 ||
		c.pool.registeredWorkGeneration != 0 ||
		c.pool.registeredWorkPhase != 0 ||
		c.pool.registeredWorkStartEpoch != 0 ||
		c.pool.registeredWorkMutation ||
		c.pool.registeredWorkFence != nil ||
		c.pool.registeredScopeID != 0 ||
		c.pool.registeredScopeAnchor != readyScopeAnchor ||
		c.pool.unacceptedScopes != 0 ||
		c.pool.coordinatorCleanupPending != 0 ||
		c.pool.coordinatorSessionID != 0 ||
		c.pool.coordinatorSessionGeneration != 0 ||
		c.fixedPointSessionID != 0 ||
		c.fixedPointSessionGeneration != 0 {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	if c.pool.activeOperationID != 0 || c.pool.activeCheckpointID != 0 {
		return privateWriterTransactionError{
			code: privateWriterTransactionErrPool,
			pool: privatePagePoolError{code: privatePagePoolErrCheckpointActive},
		}
	}
	if c.fixedPointActive {
		if !c.fixedPointFinished {
			return privateWriterTransactionError{
				code: privateWriterTransactionErrFixedPoint,
				fixedPoint: privateWriterFixedPointError{
					code: privateWriterFixedPointErrStalePredecessor,
				},
			}
		}
		poolStatus, poolProblem := c.pool.status()
		if poolProblem.failed() {
			return privateWriterTransactionError{
				code: privateWriterTransactionErrPool, pool: poolProblem,
			}
		}
		if c.fixedPointCoordinator.root != c.fixedPointRoot ||
			c.fixedPointCoordinator.pageCount != c.fixedPointPageCount ||
			c.target.PageCount != c.fixedPointPageCount ||
			c.pool.mutationEpoch != c.fixedPointPoolEpoch ||
			poolStatus.pendingTxn != c.target.TxnID ||
			poolStatus.committedPageCount != c.selected.PageCount ||
			poolStatus.pendingPageCount != c.fixedPointPageCount ||
			!c.fixedPointCoordinator.predecessorUsed {
			c.pool.abortRequired = true
			c.state = privateWriterTransactionAbortRequired
			return privateWriterTransactionError{
				code: privateWriterTransactionErrAbortRequired,
				fixedPoint: privateWriterFixedPointError{
					code: privateWriterFixedPointErrStalePredecessor,
				},
			}
		}
	}
	if c.pool.activeScopes != 0 {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterTransactionError{code: privateWriterTransactionErrAbortRequired}
	}
	return privateWriterTransactionError{}
}

// abort discards the whole draft, including successful earlier work. Cleanup is
// non-consuming: a failed cleanup leaves the same core and ledger for retry.
func (c *privateWriterTransactionCore) abort() (uint64, privateWriterTransactionError) {
	return c.abortWithCleanup(nil)
}

func (c *privateWriterTransactionCore) abortWithCleanup(
	executor privateWriterCleanupExecutor,
) (uint64, privateWriterTransactionError) {
	if c == nil || c.self != c {
		return 0, privateWriterTransactionError{code: privateWriterTransactionErrInvalidArgument}
	}
	if c.callbackActive() {
		return 0, privateWriterTransactionError{code: privateWriterTransactionErrCallbackActive}
	}
	if c.state == privateWriterTransactionClean {
		return 0, privateWriterTransactionError{code: privateWriterTransactionErrNoPendingTransaction}
	}
	if !c.abortScrubbed {
		// This is defensive: Begin reserves this increment. Check again before
		// the first scrub so corrupted state cannot revive an ancient handle.
		if c.handleEpoch == ^uint64(0) || c.abortEpoch != c.handleEpoch+1 {
			return 0, privateWriterTransactionError{code: privateWriterTransactionErrTransactionExhausted}
		}
		visits, problem := discardPrivatePagePoolDraft(&c.pool)
		if problem.failed() {
			c.state = privateWriterTransactionAbortIncomplete
			return 0, privateWriterTransactionError{code: privateWriterTransactionErrAbortIncomplete, pool: problem}
		}
		c.abortVisits += uint64(visits)
		if c.fixedPointActive {
			clear(c.fixedPointCoordinator.sourceState.records)
			clear(c.fixedPointCoordinator.sourceState.slotRecords)
			clear(c.fixedPointCoordinator.preparedSlots)
			clear(c.fixedPointCoordinator.preparationScratch)
		}
		c.fixedPointCoordinator = privateWriterFixedPointCoordinator{}
		c.fixedPointPredecessor = privateWriterFixedPointPredecessor{}
		c.fixedPointActive = false
		c.fixedPointFinished = false
		c.fixedPointPreparedMode = false
		c.fixedPointWorkActive = false
		c.fixedPointSessionID = 0
		c.fixedPointSessionGeneration = 0
		c.fixedPointRegisteredWorkID = 0
		c.fixedPointRegisteredWorkGeneration = 0
		c.fixedPointRegisteredWorkPhase = 0
		c.fixedPointWorkFence = nil
		c.fixedPointRoot = 0
		c.fixedPointPageCount = 0
		c.fixedPointPoolEpoch = 0
		c.abortScrubbed = true
		c.state = privateWriterTransactionAbortIncomplete
		c.target = Meta{}
		c.handleEpoch = c.abortEpoch
		c.abortEpoch = 0
	}
	if result := c.cleanup.retry(executor); result.firstCause.failed() {
		return 0, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortIncomplete, cleanup: result.firstCause,
		}
	}
	if !c.resources.empty() {
		return 0, privateWriterTransactionError{
			code:     privateWriterTransactionErrAbortIncomplete,
			resource: privateWriterResourceError{code: privateWriterResourceErrInvalidState},
		}
	}
	if derivePrivateWriterCleanupState(
		&c.cleanup, &c.coordination,
	) != privateWriterCleanupClean {
		return 0, privateWriterTransactionError{
			code:         privateWriterTransactionErrAbortIncomplete,
			coordination: privateWriterCoordinationError{code: privateWriterCoordinationErrGuardBusy},
		}
	}
	c.state = privateWriterTransactionClean
	c.abortScrubbed = false
	return c.abortVisits, privateWriterTransactionError{}
}

// discardPrivatePagePoolDraft invalidates every page/scope/checkpoint/operation
// capability before making the fixed slot storage reusable.
func discardPrivatePagePoolDraft(p *privatePagePool) (int, privatePagePoolError) {
	if p == nil || p.self != p {
		return 0, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	scrubSteps := uint64(len(p.slots))
	if p.epoch == ^uint64(0) || p.invalidationEpoch != p.epoch+1 ||
		p.abortMutationReserve != scrubSteps {
		return 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if scrubSteps > ^uint64(0)-p.mutationEpoch {
		return 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	slots := p.slots
	committedPageCount := p.committedPageCount
	pendingPageCount := p.pendingPageCount
	pendingTxn := p.pendingTxn
	nextEpoch := p.invalidationEpoch
	nextMutationEpoch := p.mutationEpoch + scrubSteps
	for index := range slots {
		clear(slots[index].bytes[:])
		next := privatePagePoolNoIndex
		if index+1 < len(slots) {
			next = index + 1
		}
		previous := privatePagePoolNoIndex
		if index != 0 {
			previous = index - 1
		}
		slots[index] = privatePagePoolSlot{
			epoch:           nextEpoch,
			scopeVacantNext: privatePagePoolNoIndex, scopeMemberNext: privatePagePoolNoIndex,
			unscopedNext: next, unscopedPrevious: previous,
			scopeAnchorIndex:   privatePagePoolNoIndex,
			checkpointSlotNext: privatePagePoolNoIndex, indexCheckpointNext: privatePagePoolNoIndex,
			scopeRoot: privatePagePoolNoIndex, scopeVacantHead: privatePagePoolNoIndex,
			scopeMemberHead: privatePagePoolNoIndex, scopeCheckpointNext: privatePagePoolNoIndex,
			indexLeft: privatePagePoolNoIndex, indexRight: privatePagePoolNoIndex,
			scopeLeft: privatePagePoolNoIndex, scopeRight: privatePagePoolNoIndex,
		}
	}
	head, tail := privatePagePoolNoIndex, privatePagePoolNoIndex
	if len(slots) != 0 {
		head, tail = 0, len(slots)-1
	}
	*p = privatePagePool{
		self: p, slots: slots,
		committedPageCount: committedPageCount, pendingPageCount: pendingPageCount, pendingTxn: pendingTxn,
		epoch: nextEpoch, mutationEpoch: nextMutationEpoch,
		unscopedVacantHead: head, unscopedVacantTail: tail, unscopedVacantCount: len(slots),
		checkpointIndexHead: privatePagePoolNoIndex, checkpointSlotHead: privatePagePoolNoIndex,
		checkpointScopeHead: privatePagePoolNoIndex, indexRoot: privatePagePoolNoIndex,
	}
	return len(slots), privatePagePoolError{}
}
