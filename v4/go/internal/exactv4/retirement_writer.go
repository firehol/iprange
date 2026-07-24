package exactv4

import "encoding/binary"

const (
	retirementBlobBranchCapacity = (PageSize - int(PageHeaderSize)) / blobBranchEntrySize
	retirementLeafCapacity       = (PageSize - int(PageHeaderSize)) / retirementLeafRecordSize
	retirementBranchCapacity     = (PageSize - int(PageHeaderSize)) / retirementBranchEntrySize
	retirementWriterPathCapacity = int(MaxTreeLevel) + 1
	retirementValuesPerBlobLeaf  = blobLeafCapacity / 4
)

type committedPageOrigin uint8

const (
	committedPageRetirementTree committedPageOrigin = iota + 1
	committedPageRetirementBlob
)

type committedPageReplacement struct {
	pageNumber uint32
	origin     committedPageOrigin
}

type committedReplacementLedger struct {
	entries []committedPageReplacement
	length  int
}

func newCommittedReplacementLedger(entries []committedPageReplacement) committedReplacementLedger {
	return committedReplacementLedger{entries: entries}
}

func committedReplacementLedgerWithPrefix(entries []committedPageReplacement, length int) (committedReplacementLedger, retirementWriteError) {
	if length < 0 || length > len(entries) {
		return committedReplacementLedger{}, retirementWriteError{code: retirementWriteErrReplacementLedgerTooSmall, required: length, actual: len(entries)}
	}
	return committedReplacementLedger{entries: entries, length: length}, retirementWriteError{}
}

func (l *committedReplacementLedger) used() []committedPageReplacement { return l.entries[:l.length] }
func (l *committedReplacementLedger) checkpoint() int                  { return l.length }
func (l *committedReplacementLedger) rollback(checkpoint int)          { l.length = checkpoint }

func (l *committedReplacementLedger) requireAdditional(additional int) retirementWriteError {
	required, ok := checkedIntAdd(l.length, additional)
	if !ok {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	if required > len(l.entries) {
		return retirementWriteError{code: retirementWriteErrReplacementLedgerTooSmall, required: required, actual: len(l.entries)}
	}
	return retirementWriteError{}
}

func (l *committedReplacementLedger) append(entry committedPageReplacement) retirementWriteError {
	if l.length == len(l.entries) {
		return retirementWriteError{code: retirementWriteErrReplacementLedgerTooSmall, required: l.length + 1, actual: len(l.entries)}
	}
	l.entries[l.length] = entry
	l.length++
	return retirementWriteError{}
}

type privatePageArena struct {
	pool                *privatePagePool
	scope               privatePageReservationScope
	scoped              bool
	committedPageCount  uint64
	pendingPageCount    uint64
	bornTxn             uint64
	allocationCursor    int
	plannedFingerprint  uint64
	appliedFingerprint  uint64
	plannedDestinations int
	appliedDestinations int
	tokenEpoch          uint64
	activeTokenEpoch    uint64
	activeTokenGen      uint64
}

type arenaCheckpoint struct {
	generation          uint64
	allocationCursor    int
	plannedFingerprint  uint64
	appliedFingerprint  uint64
	plannedDestinations int
	appliedDestinations int
	pool                privatePagePoolCheckpoint
}

func newPrivatePageArenaWithPool(pool *privatePagePool, bornTxn uint64) (privatePageArena, retirementWriteError) {
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrPrivatePageBudgetTooSmall}
	}
	if bornTxn <= 1 {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrPendingTransactionOutOfRange, first64: bornTxn}
	}
	if poolStatus.pendingTxn != bornTxn {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrTransactionOrder, first64: poolStatus.pendingTxn, second64: bornTxn}
	}
	if pool.activeScopes != 0 {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	return privatePageArena{
		pool:               pool,
		committedPageCount: poolStatus.committedPageCount, pendingPageCount: poolStatus.pendingPageCount, bornTxn: bornTxn,
	}, retirementWriteError{}
}

func newPrivatePageArenaInScope(
	pool *privatePagePool,
	scope privatePageReservationScope,
	bornTxn uint64,
) (privatePageArena, retirementWriteError) {
	poolStatus, poolProblem := pool.status()
	if poolProblem.failed() {
		return privatePageArena{}, retirementPoolError(poolProblem, 0, 0)
	}
	if bornTxn <= 1 {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrPendingTransactionOutOfRange, first64: bornTxn}
	}
	if poolStatus.pendingTxn != bornTxn {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrTransactionOrder, first64: poolStatus.pendingTxn, second64: bornTxn}
	}
	if _, poolProblem = pool.validateScope(scope); poolProblem.failed() {
		return privatePageArena{}, retirementPoolError(poolProblem, poolStatus.committedPageCount, poolStatus.pendingPageCount)
	}
	return privatePageArena{
		pool: pool, scope: scope, scoped: true,
		committedPageCount: poolStatus.committedPageCount,
		pendingPageCount:   poolStatus.pendingPageCount,
		bornTxn:            bornTxn,
	}, retirementWriteError{}
}

func retirementPoolError(problem privatePagePoolError, committedPageCount, pendingPageCount uint64) retirementWriteError {
	switch problem.code {
	case privatePagePoolErrInvalidBounds:
		return retirementWriteError{code: retirementWriteErrPageCountOutOfRange, first64: committedPageCount, second64: pendingPageCount}
	case privatePagePoolErrInvalidAuthorization:
		return retirementWriteError{code: retirementWriteErrPrivateAuthorizationMismatch, page: problem.page, authorization: problem.authorization}
	case privatePagePoolErrPageOutOfBounds:
		return retirementWriteError{code: retirementWriteErrPrivatePageOutOfBounds, page: problem.page}
	case privatePagePoolErrPagesNotStrict:
		return retirementWriteError{code: retirementWriteErrPrivatePagesNotStrict, page: problem.previousPage, secondPage: problem.page}
	case privatePagePoolErrBudget:
		return retirementWriteError{code: retirementWriteErrPrivatePageBudgetTooSmall, required: problem.required, actual: problem.actual}
	case privatePagePoolErrArithmeticOverflow:
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow, page: problem.page}
	case privatePagePoolErrCrossPool, privatePagePoolErrStaleScope, privatePagePoolErrScopeMismatch:
		return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: problem.page}
	default:
		return retirementWriteError{code: retirementWriteErrPrivateSlotAlreadyInUse, page: problem.page}
	}
}

func (a *privatePageArena) pagePool() *privatePagePool {
	return a.pool
}

func (a *privatePageArena) inUseCount() int {
	if a.scoped {
		count, _ := a.pagePool().scopedInUse(a.scope)
		return count
	}
	return a.pagePool().inUseCount()
}

func (a *privatePageArena) available() int {
	if a.scoped {
		count, _ := a.pagePool().scopedAvailable(a.scope)
		return count
	}
	return a.pagePool().available()
}

func (a *privatePageArena) validateAuthority() retirementWriteError {
	if a == nil || a.pool == nil {
		return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	status, problem := a.pool.status()
	if problem.failed() || status.pendingTxn != a.bornTxn ||
		status.committedPageCount != a.committedPageCount || status.pendingPageCount != a.pendingPageCount {
		return retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
	}
	if a.scoped {
		if problem = a.pool.validateScopeMembers(a.scope); problem.failed() {
			return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
		}
	} else if a.pool.activeScopes != 0 {
		return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	return retirementWriteError{}
}

func (a *privatePageArena) requirePages(count int) retirementWriteError {
	inUse, available := a.inUseCount(), a.available()
	if count < 0 || count > available {
		problem := retirementWriteError{
			code:     retirementWriteErrPrivatePageBudgetTooSmall,
			required: inUse + count, actual: inUse + available,
			scopeAvailable: available, scopeInUse: inUse,
		}
		if a.scoped {
			if anchor, scopeProblem := a.pool.validateScope(a.scope); !scopeProblem.failed() {
				problem.scopeCapacity = anchor.scopeCapacity
			}
		} else {
			problem.scopeCapacity = a.pagePool().capacity()
		}
		return problem
	}
	return retirementWriteError{}
}

func (a *privatePageArena) begin() (arenaCheckpoint, retirementWriteError) {
	return a.beginWithAllocationBatch(0)
}

func (a *privatePageArena) beginWithAllocationBatch(count int) (arenaCheckpoint, retirementWriteError) {
	if problem := a.validateAuthority(); problem.failed() {
		return arenaCheckpoint{}, problem
	}
	checkpoint, problem := a.pagePool().preflightCheckpoint()
	if problem.failed() {
		return arenaCheckpoint{}, retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	prepared := arenaCheckpoint{
		generation: checkpoint.generation, allocationCursor: a.allocationCursor, pool: checkpoint,
		plannedFingerprint: a.plannedFingerprint, appliedFingerprint: a.appliedFingerprint,
		plannedDestinations: a.plannedDestinations, appliedDestinations: a.appliedDestinations,
	}
	if problem := a.preflightAllocationBatch(prepared, count, false); problem.failed() {
		return arenaCheckpoint{}, problem
	}
	a.pagePool().beginCheckpointPrepared(checkpoint)
	return prepared, retirementWriteError{}
}

func (a *privatePageArena) preflightAdditionalAllocations(checkpoint arenaCheckpoint, count int) retirementWriteError {
	return a.preflightAllocationBatch(checkpoint, count, true)
}

func (a *privatePageArena) preflightAllocationBatch(
	checkpoint arenaCheckpoint,
	count int,
	active bool,
) retirementWriteError {
	if problem := a.validateAuthority(); problem.failed() {
		return problem
	}
	if problem := a.requirePages(count); problem.failed() {
		return problem
	}
	pool := a.pagePool()
	if active {
		if problem := pool.validateCheckpoint(checkpoint.pool); problem.failed() {
			return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
		}
	} else {
		prospective, problem := pool.preflightCheckpoint()
		if problem.failed() || prospective != checkpoint.pool {
			return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
		}
	}
	rollbackSteps := uint64(0)
	if active {
		if a.scoped {
			anchor, scopeProblem := pool.validateScope(a.scope)
			if scopeProblem.failed() {
				return retirementPoolError(scopeProblem, a.committedPageCount, a.pendingPageCount)
			}
			var pendingReturns uint64
			rollbackSteps, pendingReturns, scopeProblem = pool.checkpointScopeStats(checkpoint.pool, a.scope, anchor.scopeRoot)
			_ = pendingReturns
			if scopeProblem.failed() || rollbackSteps != pool.checkpointCleanup {
				return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
			}
		} else {
			for index := range pool.slots {
				slot := &pool.slots[index]
				if slot.checkpointID != checkpoint.pool.id {
					continue
				}
				if slot.epoch == ^uint64(0) {
					return retirementWriteError{code: retirementWriteErrArithmeticOverflow, page: slot.pageNumber}
				}
				rollbackSteps++
			}
		}
	}
	remaining := count
	fingerprint := a.plannedFingerprint
	if !active {
		fingerprint = 0xcbf29ce484222325
	}
	var epochProblem privatePagePoolError
	if a.scoped {
		anchor, scopeProblem := pool.validateScope(a.scope)
		if scopeProblem.failed() {
			return retirementPoolError(scopeProblem, a.committedPageCount, a.pendingPageCount)
		}
		epochProblem = pool.preflightLowestAvailableEpochsInScope(a.scope, anchor.scopeRoot, &remaining, &fingerprint)
	} else {
		epochProblem = pool.preflightLowestAvailableEpochs(pool.indexRoot, &remaining)
	}
	if epochProblem.failed() {
		return retirementPoolError(epochProblem, a.committedPageCount, a.pendingPageCount)
	}
	if remaining != 0 {
		return retirementWriteError{code: retirementWriteErrPrivatePageBudgetTooSmall, required: a.inUseCount() + count, actual: pool.capacity()}
	}
	allocationSteps, ok := checkedMul(uint64(count), 3) // claim, write, and reserved rollback.
	if !ok || rollbackSteps > ^uint64(0)-allocationSteps {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	if problem := pool.requireMutationSteps(rollbackSteps + allocationSteps); problem.failed() {
		return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	if a.scoped {
		if active && (a.plannedDestinations != a.appliedDestinations || a.plannedFingerprint != a.appliedFingerprint) {
			return retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
		}
		if !active {
			a.plannedDestinations, a.appliedDestinations = 0, 0
			a.appliedFingerprint = 0xcbf29ce484222325
		}
		a.plannedFingerprint = fingerprint
		a.plannedDestinations += count
	}
	return retirementWriteError{}
}

// allocatePrepared is infallible after requirePages and begin. Keeping mutation
// behind this method makes apply phases pure page construction, not validation.
func (a *privatePageArena) allocatePrepared(checkpoint arenaCheckpoint, origin privatePageOrigin) uint32 {
	pool := a.pagePool()
	index := privatePagePoolNoIndex
	if a.scoped {
		index, _ = pool.lowestAvailableSlotInScope(a.scope)
		slot := &pool.slots[index]
		a.appliedFingerprint = privatePageDestinationFingerprint(
			a.appliedFingerprint, a.scope.id, index, slot.pageNumber, slot.epoch,
		)
		a.appliedDestinations++
		index = pool.claimLowestInScopeForCheckpointPrepared(checkpoint.pool, a.scope, privatePageOwnerRetirement, origin)
	} else {
		index = pool.claimLowestForCheckpointPrepared(checkpoint.pool, privatePageOwnerRetirement, origin)
	}
	a.allocationCursor = index + 1
	return pool.slots[index].pageNumber
}

func (a *privatePageArena) rollback(checkpoint arenaCheckpoint) retirementWriteError {
	var problem privatePagePoolError
	if a.scoped {
		problem = a.pagePool().rollbackCheckpointInScope(checkpoint.pool, a.scope)
	} else {
		problem = a.pagePool().rollback(checkpoint.pool)
	}
	if problem.failed() {
		return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	a.allocationCursor = checkpoint.allocationCursor
	a.plannedFingerprint, a.appliedFingerprint = checkpoint.plannedFingerprint, checkpoint.appliedFingerprint
	a.plannedDestinations, a.appliedDestinations = checkpoint.plannedDestinations, checkpoint.appliedDestinations
	return retirementWriteError{}
}

func (a *privatePageArena) preflightCommit(checkpoint arenaCheckpoint, releases []uint32) retirementWriteError {
	pool := a.pagePool()
	if problem := pool.validateCheckpoint(checkpoint.pool); problem.failed() {
		return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	if a.scoped && (a.plannedDestinations != a.appliedDestinations || a.plannedFingerprint != a.appliedFingerprint) {
		return retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
	}
	marked := 0
	defer func() {
		for _, pageNumber := range releases[:marked] {
			if index, found := pool.slotIndex(pageNumber); found {
				pool.slots[index].batchMarked = false
			}
		}
	}()
	forwardSteps, prospectiveCleanup := uint64(0), uint64(0)
	for _, pageNumber := range releases {
		index, found := pool.slotIndex(pageNumber)
		if !found {
			return retirementWriteError{code: retirementWriteErrPrivateSlotAlreadyInUse, page: pageNumber}
		}
		slot := &pool.slots[index]
		if a.scoped && (slot.scopeID != a.scope.id || slot.scopeAnchorIndex != a.scope.anchor) {
			return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: pageNumber}
		}
		if slot.batchMarked || slot.state != privatePageInUse || !slot.inUse ||
			slot.owner != privatePageOwnerRetirement ||
			(slot.origin != privatePageRetirementTree && slot.origin != privatePageRetirementBlob) ||
			slot.pendingTxn != pool.pendingTxn {
			return retirementWriteError{code: retirementWriteErrPrivateSlotAlreadyInUse, page: pageNumber}
		}
		slot.batchMarked = true
		marked++
		epochAdvances := uint64(1)
		if slot.checkpointID != checkpoint.pool.id {
			prospectiveCleanup++
		}
		if slot.checkpointID != checkpoint.pool.id || slot.checkpointState == privatePageInUse {
			epochAdvances = 2
		}
		if slot.epoch > ^uint64(0)-epochAdvances || forwardSteps == ^uint64(0) {
			return retirementWriteError{code: retirementWriteErrArithmeticOverflow, page: pageNumber}
		}
		forwardSteps++
	}
	if problem := pool.requireCheckpointForwardSteps(forwardSteps, prospectiveCleanup); problem.failed() {
		return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	return retirementWriteError{}
}

func (a *privatePageArena) commit(checkpoint arenaCheckpoint, releases []uint32) retirementWriteError {
	if problem := a.preflightCommit(checkpoint, releases); problem.failed() {
		return problem
	}
	for _, pageNumber := range releases {
		index, _ := a.pagePool().slotIndex(pageNumber)
		if a.scoped {
			if problem := a.pagePool().releaseSlotForCheckpointInScopePrepared(checkpoint.pool, a.scope, index, privatePageAvailable); problem.failed() {
				return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
			}
		} else {
			a.pagePool().releaseSlotForCheckpointPrepared(checkpoint.pool, index, privatePageAvailable)
		}
	}
	if a.scoped {
		if problem := a.pagePool().commitCheckpointInScopePrepared(checkpoint.pool, a.scope); problem.failed() {
			return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
		}
	} else {
		a.pagePool().commitCheckpointPrepared(checkpoint.pool)
	}
	a.allocationCursor = 0
	a.plannedFingerprint, a.appliedFingerprint = 0, 0
	a.plannedDestinations, a.appliedDestinations = 0, 0
	return retirementWriteError{}
}

func (a *privatePageArena) releaseGeneration(generation uint64, origin privatePageOrigin) retirementWriteError {
	var problem privatePagePoolError
	if a.scoped {
		problem = a.pagePool().releaseGenerationInScope(a.scope, generation, privatePageOwnerRetirement, origin)
	} else {
		problem = a.pagePool().releaseGeneration(generation, privatePageOwnerRetirement, origin)
	}
	if problem.failed() {
		return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
	}
	a.allocationCursor = 0
	return retirementWriteError{}
}

func (a *privatePageArena) privateToken(pageNumber uint32, origin privatePageOrigin) (privatePageToken, privatePagePoolError) {
	if a.scoped {
		return a.pagePool().borrowExactInScope(a.scope, pageNumber, privatePageOwnerRetirement, origin)
	}
	return a.pagePool().borrowExact(pageNumber, privatePageOwnerRetirement, origin)
}

func (a *privatePageArena) transferRetirementPageToBitmap(
	checkpoint privatePagePoolCheckpoint,
	pageNumber uint32,
	origin privatePageOrigin,
) (privatePageToken, retirementWriteError) {
	token, poolProblem := a.privateToken(pageNumber, origin)
	if poolProblem.failed() {
		return privatePageToken{}, retirementPoolError(poolProblem, a.committedPageCount, a.pendingPageCount)
	}
	var transferred privatePageToken
	if a.scoped {
		transferred, poolProblem = a.pagePool().transferInScope(
			checkpoint, a.scope, token, privatePageOwnerBitmap, privatePageBitmap,
		)
	} else {
		transferred, poolProblem = a.pagePool().transfer(
			checkpoint, token, privatePageOwnerBitmap, privatePageBitmap,
		)
	}
	if poolProblem.failed() {
		return privatePageToken{}, retirementPoolError(poolProblem, a.committedPageCount, a.pendingPageCount)
	}
	return transferred, retirementWriteError{}
}

func (a *privatePageArena) readPrivatePage(
	pageNumber uint32,
	origin privatePageOrigin,
	destination *[PageSize]byte,
) privatePagePoolError {
	token, problem := a.privateToken(pageNumber, origin)
	if problem.failed() {
		return problem
	}
	if a.scoped {
		return a.pagePool().readPageInScope(a.scope, token, destination)
	}
	return a.pagePool().readPage(token, destination)
}

func (a *privatePageArena) readPrivateUint64(token privatePageToken, offset int) (uint64, privatePagePoolError) {
	if a.scoped {
		return a.pagePool().readUint64InScope(a.scope, token, offset)
	}
	return a.pagePool().readUint64(token, offset)
}

func (a *privatePageArena) writePage(pageNumber uint32, page *[PageSize]byte) retirementWriteError {
	if a == nil || a.pagePool() == nil || page == nil {
		return retirementWriteError{code: retirementWriteErrPrivateBindingDrift, page: pageNumber}
	}
	index, found := a.pagePool().slotIndex(pageNumber)
	if !found {
		return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
	}
	if a.scoped {
		slot := &a.pagePool().slots[index]
		if slot.scopeID != a.scope.id || slot.scopeAnchorIndex != a.scope.anchor {
			return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: pageNumber}
		}
		if problem := a.pagePool().writeSlotInScopePrepared(a.scope, index, page); problem.failed() {
			return retirementPoolError(problem, a.committedPageCount, a.pendingPageCount)
		}
		return retirementWriteError{}
	}
	a.pagePool().writeSlotPrepared(index, page)
	return retirementWriteError{}
}

type blobBuildScratch struct{ pageNumbers []uint32 }

type privateReleaseBuffer struct {
	pageNumbers []uint32
	length      int
}

func newPrivateReleaseBuffer(pageNumbers []uint32) privateReleaseBuffer {
	return privateReleaseBuffer{pageNumbers: pageNumbers}
}

func (b *privateReleaseBuffer) checkpoint() int         { return b.length }
func (b *privateReleaseBuffer) rollback(checkpoint int) { b.length = checkpoint }
func (b *privateReleaseBuffer) entriesFrom(checkpoint int) []uint32 {
	return b.pageNumbers[checkpoint:b.length]
}
func (b *privateReleaseBuffer) push(pageNumber uint32) retirementWriteError {
	if b.length == len(b.pageNumbers) {
		return retirementWriteError{code: retirementWriteErrPrivateReleaseBufferTooSmall, required: b.length + 1, actual: len(b.pageNumbers)}
	}
	b.pageNumbers[b.length] = pageNumber
	b.length++
	return retirementWriteError{}
}

type pageRole uint8

const (
	pageRolePrivateAuthorization pageRole = iota + 1
	pageRoleReferencedRetirementTree
	pageRoleReferencedRetirementBlob
	pageRoleSelectedRetirementTree
	pageRoleSelectedRetirementBlob
	pageRoleListedReclaimed
	pageRoleRequiredRetirementList
	pageRoleReplacementRetirementTree
	pageRoleReplacementRetirementBlob
)

const (
	rolePrivate uint16 = 1 << iota
	roleTree
	roleBlob
	roleListed
	roleReplacementTree
	roleReplacementBlob
	roleReferenceTree
	roleReferenceBlob
	roleOldRequired
	rolePrefixReplacement
	rolePrivateRetired
)

type pageRoleIndexSlot struct {
	pageNumber     uint32
	roles          uint16
	referenceEpoch uint8
	selectedEpoch  uint8
	preparedSlot   int
	preparedEpoch  uint64
	preparedAuth   privatePageAuthorization
	preparedOwner  privatePageOwner
	preparedOrigin privatePageOrigin
	preparedTxn    uint64
	preparedGen    uint64
	priorPrivate   bool
	prior          privateWriterDraftPageProvenance
	left           int
	right          int
	height         uint8
	occupied       bool
}

const pageRoleNoIndex = -1

type pageRoleIndex struct {
	slots                    []pageRoleIndexSlot
	used                     int
	root                     int
	referenceEpoch           uint8
	replacementsMustBeListed bool
	planSequence             uint64
	activePlan               uint64
}

func newPageRoleIndex(slots []pageRoleIndexSlot) pageRoleIndex {
	return pageRoleIndex{slots: slots, root: pageRoleNoIndex, referenceEpoch: 1}
}

func (i *pageRoleIndex) clear() {
	for index := range i.slots {
		i.slots[index] = pageRoleIndexSlot{}
	}
	i.used = 0
	i.root = pageRoleNoIndex
	i.referenceEpoch = 1
	i.replacementsMustBeListed = false
	i.activePlan = 0
}

func (i *pageRoleIndex) prepare(arena *privatePageArena, replacements *committedReplacementLedger) retirementWriteError {
	i.clear()
	pool := arena.pagePool()
	if arena.scoped {
		anchor, poolProblem := pool.validateScope(arena.scope)
		if poolProblem.failed() {
			return retirementPoolError(poolProblem, arena.committedPageCount, arena.pendingPageCount)
		}
		if problem := i.prepareScopeNode(pool, anchor.scopeRoot, arena.scope); problem.failed() {
			return problem
		}
	} else {
		for index := 0; index < pool.capacity(); index++ {
			info, poolProblem := pool.slotInfo(index)
			if poolProblem.failed() {
				return retirementPoolError(poolProblem, arena.committedPageCount, arena.pendingPageCount)
			}
			if !info.bound || info.state == privatePageInUse && info.owner != privatePageOwnerRetirement {
				continue
			}
			if problem := i.insertExclusive(info.pageNumber, rolePrivate, pageRolePrivateAuthorization); problem.failed() {
				return problem
			}
		}
	}
	for _, replacement := range replacements.used() {
		bit, role := roleReplacementTree, pageRoleReplacementRetirementTree
		if replacement.origin == committedPageRetirementBlob {
			bit, role = roleReplacementBlob, pageRoleReplacementRetirementBlob
		}
		if problem := i.insertExclusive(replacement.pageNumber, bit|rolePrefixReplacement, role); problem.failed() {
			return problem
		}
	}
	return retirementWriteError{}
}

func (i *pageRoleIndex) prepareScopeNode(
	pool *privatePagePool,
	root int,
	scope privatePageReservationScope,
) retirementWriteError {
	if root == privatePagePoolNoIndex {
		return retirementWriteError{}
	}
	slot := &pool.slots[root]
	if !slot.bound || slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: slot.pageNumber}
	}
	if problem := i.prepareScopeNode(pool, slot.scopeLeft, scope); problem.failed() {
		return problem
	}
	if slot.state != privatePageInUse || slot.owner == privatePageOwnerRetirement {
		if problem := i.insertExclusive(slot.pageNumber, rolePrivate, pageRolePrivateAuthorization); problem.failed() {
			return problem
		}
	}
	return i.prepareScopeNode(pool, slot.scopeRight, scope)
}

func (i *pageRoleIndex) locate(pageNumber uint32) (int, bool, retirementWriteError) {
	for index := i.root; index != pageRoleNoIndex; {
		slot := &i.slots[index]
		switch {
		case pageNumber < slot.pageNumber:
			index = slot.left
		case pageNumber > slot.pageNumber:
			index = slot.right
		default:
			return index, true, retirementWriteError{}
		}
	}
	if i.used == len(i.slots) {
		return 0, false, retirementWriteError{code: retirementWriteErrPageRoleIndexTooSmall, required: i.used + 1, actual: len(i.slots)}
	}
	return i.used, false, retirementWriteError{}
}

func (i *pageRoleIndex) insertAt(index int, pageNumber uint32, roles uint16) {
	i.slots[index] = pageRoleIndexSlot{
		pageNumber:   pageNumber,
		roles:        roles,
		preparedSlot: privatePagePoolNoIndex,
		left:         pageRoleNoIndex,
		right:        pageRoleNoIndex,
		height:       1,
		occupied:     true,
	}
	i.used++
	i.root = i.insertUnique(i.root, index)
}

func (i *pageRoleIndex) insertUnique(root, newIndex int) int {
	if root == pageRoleNoIndex {
		return newIndex
	}
	pageNumber := i.slots[newIndex].pageNumber
	if pageNumber < i.slots[root].pageNumber {
		i.slots[root].left = i.insertUnique(i.slots[root].left, newIndex)
	} else {
		i.slots[root].right = i.insertUnique(i.slots[root].right, newIndex)
	}
	i.updateHeight(root)
	balance := i.balance(root)
	if balance > 1 {
		left := i.slots[root].left
		if pageNumber > i.slots[left].pageNumber {
			i.slots[root].left = i.rotateLeft(left)
		}
		return i.rotateRight(root)
	}
	if balance < -1 {
		right := i.slots[root].right
		if pageNumber < i.slots[right].pageNumber {
			i.slots[root].right = i.rotateRight(right)
		}
		return i.rotateLeft(root)
	}
	return root
}

func (i *pageRoleIndex) slotHeight(index int) uint8 {
	if index == pageRoleNoIndex {
		return 0
	}
	return i.slots[index].height
}

func (i *pageRoleIndex) updateHeight(index int) {
	height := i.slotHeight(i.slots[index].left)
	if right := i.slotHeight(i.slots[index].right); right > height {
		height = right
	}
	i.slots[index].height = height + 1
}

func (i *pageRoleIndex) balance(index int) int16 {
	return int16(i.slotHeight(i.slots[index].left)) - int16(i.slotHeight(i.slots[index].right))
}

func (i *pageRoleIndex) rotateLeft(root int) int {
	pivot := i.slots[root].right
	middle := i.slots[pivot].left
	i.slots[pivot].left = root
	i.slots[root].right = middle
	i.updateHeight(root)
	i.updateHeight(pivot)
	return pivot
}

func (i *pageRoleIndex) rotateRight(root int) int {
	pivot := i.slots[root].left
	middle := i.slots[pivot].right
	i.slots[pivot].right = root
	i.slots[root].left = middle
	i.updateHeight(root)
	i.updateHeight(pivot)
	return pivot
}

func (i *pageRoleIndex) insertExclusive(pageNumber uint32, bit uint16, requested pageRole) retirementWriteError {
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if found {
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: roleFromBits(i.slots[index].roles), requestedRole: requested}
	}
	i.insertAt(index, pageNumber, bit)
	return retirementWriteError{}
}

func (i *pageRoleIndex) bindPriorPrivate(
	pageNumber uint32,
	provenance privateWriterDraftPageProvenance,
) retirementWriteError {
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if !found {
		i.insertAt(index, pageNumber, rolePrivate)
		i.slots[index].priorPrivate = true
		i.slots[index].prior = provenance
		return retirementWriteError{}
	}
	slot := &i.slots[index]
	if slot.roles&rolePrivate == 0 || !slot.priorPrivate || slot.prior != provenance {
		return retirementWriteError{
			code: retirementWriteErrPageRoleConflict, page: pageNumber,
			existingRole: roleFromBits(slot.roles), requestedRole: pageRolePrivateAuthorization,
		}
	}
	return retirementWriteError{}
}

func roleFromBits(bits uint16) pageRole {
	switch {
	case bits&rolePrivate != 0:
		return pageRolePrivateAuthorization
	case bits&roleReferenceTree != 0:
		return pageRoleReferencedRetirementTree
	case bits&roleReferenceBlob != 0:
		return pageRoleReferencedRetirementBlob
	case bits&roleTree != 0:
		return pageRoleSelectedRetirementTree
	case bits&roleBlob != 0:
		return pageRoleSelectedRetirementBlob
	case bits&roleListed != 0:
		return pageRoleListedReclaimed
	case bits&roleOldRequired != 0:
		return pageRoleRequiredRetirementList
	case bits&roleReplacementTree != 0:
		return pageRoleReplacementRetirementTree
	default:
		return pageRoleReplacementRetirementBlob
	}
}

func (i *pageRoleIndex) selectPage(pageNumber uint32, requested pageRole, private bool) retirementWriteError {
	bit, reference := roleTree, roleReferenceTree
	if requested == pageRoleSelectedRetirementBlob {
		bit, reference = roleBlob, roleReferenceBlob
	}
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if !found {
		if private {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		i.insertAt(index, pageNumber, bit)
		i.slots[index].selectedEpoch = i.referenceEpoch
		return retirementWriteError{}
	}
	existing := i.slots[index].roles
	base := uint16(0)
	if private {
		base = rolePrivate
	}
	allowed := existing == base || existing == base|reference
	same := existing == base|bit || existing == base|reference|bit
	if same && i.slots[index].selectedEpoch < i.referenceEpoch {
		i.slots[index].selectedEpoch = i.referenceEpoch
		if existing&reference != 0 {
			i.slots[index].referenceEpoch = i.referenceEpoch
		}
		return retirementWriteError{}
	}
	if !allowed {
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: roleFromBits(existing), requestedRole: requested}
	}
	i.slots[index].roles = existing | bit
	i.slots[index].selectedEpoch = i.referenceEpoch
	if existing&reference != 0 {
		i.slots[index].referenceEpoch = i.referenceEpoch
	}
	return retirementWriteError{}
}

func (i *pageRoleIndex) reference(pageNumber uint32, requested pageRole, private bool) retirementWriteError {
	bit, selected := roleReferenceTree, roleTree
	if requested == pageRoleReferencedRetirementBlob {
		bit, selected = roleReferenceBlob, roleBlob
	}
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if !found {
		if private {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		i.insertAt(index, pageNumber, bit)
		i.slots[index].referenceEpoch = i.referenceEpoch
		return retirementWriteError{}
	}
	existing := i.slots[index].roles
	if private && existing == rolePrivate {
		i.slots[index].roles |= bit
		i.slots[index].referenceEpoch = i.referenceEpoch
		return retirementWriteError{}
	}
	base := uint16(0)
	if private {
		base = rolePrivate
	}
	same := existing == base|bit || existing == base|bit|selected
	if same && i.slots[index].referenceEpoch < i.referenceEpoch {
		i.slots[index].referenceEpoch = i.referenceEpoch
		return retirementWriteError{}
	}
	return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: roleFromBits(existing), requestedRole: requested}
}

func (i *pageRoleIndex) advanceReferenceEpoch() retirementWriteError {
	if i.referenceEpoch == ^uint8(0) {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	i.referenceEpoch++
	return retirementWriteError{}
}

func (i *pageRoleIndex) requireNewReplacements() {
	i.replacementsMustBeListed = true
	for index := range i.slots {
		if i.slots[index].occupied && i.slots[index].roles&(roleReplacementTree|roleReplacementBlob) != 0 {
			i.slots[index].roles |= roleOldRequired
		}
	}
}

func (i *pageRoleIndex) requireInNewList(pageNumber uint32) retirementWriteError {
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if found {
		existing := i.slots[index].roles
		if existing&rolePrefixReplacement != 0 && existing&roleOldRequired != 0 && existing&(roleReplacementTree|roleReplacementBlob) != 0 {
			return retirementWriteError{}
		}
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: roleFromBits(existing), requestedRole: pageRoleListedReclaimed}
	}
	i.insertAt(index, pageNumber, roleOldRequired)
	return retirementWriteError{}
}

func (i *pageRoleIndex) listed(pageNumber uint32, satisfy bool) retirementWriteError {
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if found {
		existing := i.slots[index].roles
		if satisfy && existing&roleOldRequired != 0 {
			i.slots[index].roles = existing&^roleOldRequired | roleListed
			return retirementWriteError{}
		}
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: roleFromBits(existing), requestedRole: pageRoleListedReclaimed}
	}
	i.insertAt(index, pageNumber, roleListed)
	return retirementWriteError{}
}

func (i *pageRoleIndex) firstUnsatisfiedRequired() (uint32, bool) {
	var first uint32
	found := false
	for index := range i.slots {
		if i.slots[index].occupied && i.slots[index].roles&roleOldRequired != 0 && (!found || i.slots[index].pageNumber < first) {
			first, found = i.slots[index].pageNumber, true
		}
	}
	return first, found
}

func (i *pageRoleIndex) retireCommitted(pageNumber uint32, origin committedPageOrigin) retirementWriteError {
	expected, replacement, requested := roleTree, roleReplacementTree, pageRoleReplacementRetirementTree
	if origin == committedPageRetirementBlob {
		expected, replacement, requested = roleBlob, roleReplacementBlob, pageRoleReplacementRetirementBlob
	}
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if !found || i.slots[index].roles&expected == 0 {
		existing := pageRolePrivateAuthorization
		if found {
			existing = roleFromBits(i.slots[index].roles)
		}
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: existing, requestedRole: requested}
	}
	i.slots[index].roles = replacement
	if i.replacementsMustBeListed {
		i.slots[index].roles |= roleOldRequired
	}
	return retirementWriteError{}
}

func (i *pageRoleIndex) retirePrivate(pageNumber uint32, origin privatePageOrigin) retirementWriteError {
	expected, requested := roleTree, pageRoleReplacementRetirementTree
	if origin == privatePageRetirementBlob {
		expected, requested = roleBlob, pageRoleReplacementRetirementBlob
	}
	index, found, problem := i.locate(pageNumber)
	if problem.failed() {
		return problem
	}
	if !found || i.slots[index].roles&rolePrivate == 0 || i.slots[index].roles&expected == 0 {
		existing := pageRolePrivateAuthorization
		if found {
			existing = roleFromBits(i.slots[index].roles)
		}
		return retirementWriteError{code: retirementWriteErrPageRoleConflict, page: pageNumber, existingRole: existing, requestedRole: requested}
	}
	i.slots[index].roles = rolePrivate | rolePrivateRetired
	return retirementWriteError{}
}

type retirementWriteErrorCode uint8

const (
	retirementWriteErrSource retirementWriteErrorCode = iota + 1
	retirementWriteErrHeader
	retirementWriteErrBlobPage
	retirementWriteErrRetirementPage
	retirementWriteErrPageCountOutOfRange
	retirementWriteErrPendingTransactionOutOfRange
	retirementWriteErrSelectedTransactionOutOfRange
	retirementWriteErrTransactionOrder
	retirementWriteErrSelectedTransactionOverflow
	retirementWriteErrRootCountMismatch
	retirementWriteErrRootOutOfBounds
	retirementWriteErrPrivatePageOutOfBounds
	retirementWriteErrPrivateAuthorizationMismatch
	retirementWriteErrPrivatePagesNotStrict
	retirementWriteErrPrivateSlotAlreadyInUse
	retirementWriteErrPrivatePageBudgetTooSmall
	retirementWriteErrBlobBuildScratchTooSmall
	retirementWriteErrPrivateReleaseBufferTooSmall
	retirementWriteErrPageRoleIndexTooSmall
	retirementWriteErrPageRoleConflict
	retirementWriteErrPrivatePageUnavailable
	retirementWriteErrPrivatePageOriginMismatch
	retirementWriteErrCommittedParentPrivateChild
	retirementWriteErrReplacementLedgerTooSmall
	retirementWriteErrPathBufferTooSmall
	retirementWriteErrEmptyRetirementStream
	retirementWriteErrRetirementStreamTooLong
	retirementWriteErrRetirementStreamOrder
	retirementWriteErrRetirementTreeOrder
	retirementWriteErrRetirementStreamPageOutOfBounds
	retirementWriteErrArithmeticOverflow
	retirementWriteErrTreeDepthExceeded
	retirementWriteErrRootType
	retirementWriteErrChildType
	retirementWriteErrChildLevel
	retirementWriteErrChildMaximumMismatch
	retirementWriteErrBatchCountOutOfRange
	retirementWriteErrBatchCountMismatch
	retirementWriteErrDeleteCountOutOfRange
	retirementWriteErrBlobOffsetMismatch
	retirementWriteErrBlobLengthMismatch
	retirementWriteErrBlobPageCountMismatch
	retirementWriteErrRetirementListOmission
	retirementWriteErrPrivateBlobNonPrivateChild
	retirementWriteErrBlobResidenceMismatch
	retirementWriteErrBlobTokenTransactionMismatch
	retirementWriteErrBlobTokenGenerationMismatch
	retirementWriteErrBlobTokenStale
	retirementWriteErrCommittedReplacementIsPrivate
	retirementWriteErrBlobScanScratchTooSmall
	retirementWriteErrPrivateScopeMismatch
	retirementWriteErrPrivateBindingDrift
	retirementWriteErrStaleEditPlan
	retirementWriteErrEditPlanConsumed
	retirementWriteErrRetirementStreamCountMismatch
	retirementWriteErrPageNumberIndex
)

type retirementWriteError struct {
	code                        retirementWriteErrorCode
	source                      pageSourceStatus
	header                      bitmapCOWHeaderProblem
	blob                        blobPageStatus
	retirement                  retirementPageStatus
	page, secondPage            uint32
	first64, second64           uint64
	required, actual            int
	pageType                    PageType
	authorization               privatePageAuthorization
	existingRole, requestedRole pageRole
	cleanupCode                 retirementWriteErrorCode
	cleanupPage                 uint32
	binding                     retirementEditBinding
	scopeCapacity               int
	scopeAvailable              int
	scopeInUse                  int
}

func (e retirementWriteError) failed() bool  { return e.code != 0 }
func (e retirementWriteError) Error() string { return "exact v4 retirement writer failed" }

func retirementSourceProblem(status pageSourceStatus) retirementWriteError {
	return retirementWriteError{code: retirementWriteErrSource, source: status}
}
func retirementHeaderProblem(status bitmapCOWHeaderProblem) retirementWriteError {
	return retirementWriteError{code: retirementWriteErrHeader, header: status}
}
func retirementBlobProblem(status blobPageStatus) retirementWriteError {
	return retirementWriteError{code: retirementWriteErrBlobPage, blob: status}
}
func retirementPageProblem(status retirementPageStatus) retirementWriteError {
	return retirementWriteError{code: retirementWriteErrRetirementPage, retirement: status}
}

func retirementWithCleanup(primary, cleanup retirementWriteError) retirementWriteError {
	if cleanup.failed() {
		primary.cleanupCode = cleanup.code
		primary.cleanupPage = cleanup.page
	}
	return primary
}

type retirementBlobToken struct {
	arena                                *privatePageArena
	root                                 uint32
	pageCount, byteLength                uint64
	privatePages                         int
	generation, bornTxn, epoch           uint64
	cleanupGeneration, cleanupTokenEpoch uint64
}

func (t *retirementBlobToken) valid() retirementWriteError {
	if t == nil || t.arena == nil || t.cleanupTokenEpoch == 0 ||
		t.arena.activeTokenEpoch != t.cleanupTokenEpoch ||
		t.arena.activeTokenGen != t.cleanupGeneration ||
		t.epoch != t.cleanupTokenEpoch {
		return retirementWriteError{code: retirementWriteErrBlobTokenStale}
	}
	return retirementWriteError{}
}

func (t *retirementBlobToken) discard() retirementWriteError {
	if t == nil || t.arena == nil {
		return retirementWriteError{}
	}
	if t.arena.activeTokenEpoch == t.cleanupTokenEpoch && t.arena.activeTokenGen == t.cleanupGeneration {
		if problem := t.arena.releaseGeneration(t.cleanupGeneration, privatePageRetirementBlob); problem.failed() {
			return problem
		}
		t.arena.activeTokenEpoch, t.arena.activeTokenGen = 0, 0
	}
	t.arena, t.epoch, t.cleanupTokenEpoch = nil, 0, 0
	return retirementWriteError{}
}

func (t *retirementBlobToken) stabilize() {
	if t == nil || t.arena == nil {
		return
	}
	if t.arena.activeTokenEpoch == t.cleanupTokenEpoch && t.arena.activeTokenGen == t.cleanupGeneration {
		t.arena.activeTokenEpoch, t.arena.activeTokenGen = 0, 0
	}
	t.arena, t.epoch, t.cleanupTokenEpoch = nil, 0, 0
}

type blobGeometry struct {
	valueCount            uint64
	leafCount, totalPages int
	rootLevel             uint16
}

// retirementIndexStreamStop stops the index visitor after the local blob
// writer has captured its typed failure. It has no allocation path.
type retirementIndexStreamStop struct{}

func (retirementIndexStreamStop) Error() string { return "retirement index stream stopped" }

func buildRetirementBlob(pages []uint32, arena *privatePageArena, scratch *blobBuildScratch) (retirementBlobToken, retirementWriteError) {
	geometry, problem := preflightRetirementBlob(pages, arena, len(scratch.pageNumbers))
	if problem.failed() {
		return retirementBlobToken{}, problem
	}
	if arena.activeTokenEpoch != 0 {
		return retirementBlobToken{}, retirementWriteError{code: retirementWriteErrBlobTokenStale}
	}
	nextTokenEpoch := arena.tokenEpoch + 1
	if nextTokenEpoch == 0 {
		return retirementBlobToken{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	checkpoint, problem := arena.beginWithAllocationBatch(geometry.totalPages)
	if problem.failed() {
		return retirementBlobToken{}, problem
	}
	for index := 0; index < geometry.totalPages; index++ {
		scratch.pageNumbers[index] = arena.allocatePrepared(checkpoint, privatePageRetirementBlob)
	}
	valueIndex := uint64(0)
	for leafIndex := 0; leafIndex < geometry.leafCount; leafIndex++ {
		values := geometry.valueCount - valueIndex
		if values > retirementValuesPerBlobLeaf {
			values = retirementValuesPerBlobLeaf
		}
		var page [PageSize]byte
		encodeRetirementBlobLeaf(&page, arena.bornTxn, valueIndex*4, pages[valueIndex:valueIndex+values])
		if problem = arena.writePage(scratch.pageNumbers[leafIndex], &page); problem.failed() {
			return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
		}
		valueIndex += values
	}
	inputStart, inputCount, outputStart, level := 0, geometry.leafCount, geometry.leafCount, uint16(1)
	for inputCount > 1 {
		outputCount := (inputCount + retirementBlobBranchCapacity - 1) / retirementBlobBranchCapacity
		for outputIndex := 0; outputIndex < outputCount; outputIndex++ {
			childStart := inputStart + outputIndex*retirementBlobBranchCapacity
			childCount := inputCount - outputIndex*retirementBlobBranchCapacity
			if childCount > retirementBlobBranchCapacity {
				childCount = retirementBlobBranchCapacity
			}
			var page [PageSize]byte
			if problem = encodeRetirementBlobBranch(&page, arena.bornTxn, level, scratch.pageNumbers[childStart:childStart+childCount], arena); problem.failed() {
				return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
			}
			if problem = arena.writePage(scratch.pageNumbers[outputStart+outputIndex], &page); problem.failed() {
				return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
			}
		}
		inputStart, inputCount, outputStart, level = outputStart, outputCount, outputStart+outputCount, level+1
	}
	if problem = arena.commit(checkpoint, nil); problem.failed() {
		return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
	}
	arena.tokenEpoch = nextTokenEpoch
	arena.activeTokenEpoch, arena.activeTokenGen = arena.tokenEpoch, checkpoint.generation
	return retirementBlobToken{
		arena: arena, root: scratch.pageNumbers[inputStart], pageCount: geometry.valueCount,
		byteLength: geometry.valueCount * 4, privatePages: geometry.totalPages,
		generation: checkpoint.generation, bornTxn: arena.bornTxn, epoch: arena.tokenEpoch,
		cleanupGeneration: checkpoint.generation, cleanupTokenEpoch: arena.tokenEpoch,
	}, retirementWriteError{}
}

// buildRetirementBlobFromIndex streams the already-sorted private index into
// immutable blob leaves. It never materializes its input as a page-number slice.
func buildRetirementBlobFromIndex(index *pageNumberIndex, arena *privatePageArena, scratch *blobBuildScratch) (retirementBlobToken, retirementWriteError) {
	geometry, problem := preflightRetirementBlobFromIndex(index, arena, len(scratch.pageNumbers))
	if problem.failed() {
		return retirementBlobToken{}, problem
	}
	if arena.activeTokenEpoch != 0 {
		return retirementBlobToken{}, retirementWriteError{code: retirementWriteErrBlobTokenStale}
	}
	nextTokenEpoch := arena.tokenEpoch + 1
	if nextTokenEpoch == 0 {
		return retirementBlobToken{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	checkpoint, problem := arena.beginWithAllocationBatch(geometry.totalPages)
	if problem.failed() {
		return retirementBlobToken{}, problem
	}
	for pageIndex := 0; pageIndex < geometry.totalPages; pageIndex++ {
		scratch.pageNumbers[pageIndex] = arena.allocatePrepared(checkpoint, privatePageRetirementBlob)
	}
	if problem = writeRetirementBlobIndexLeaves(index, arena, scratch, geometry); problem.failed() {
		return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
	}
	inputStart, inputCount, outputStart, level := 0, geometry.leafCount, geometry.leafCount, uint16(1)
	for inputCount > 1 {
		outputCount := (inputCount + retirementBlobBranchCapacity - 1) / retirementBlobBranchCapacity
		for outputIndex := 0; outputIndex < outputCount; outputIndex++ {
			childStart := inputStart + outputIndex*retirementBlobBranchCapacity
			childCount := inputCount - outputIndex*retirementBlobBranchCapacity
			if childCount > retirementBlobBranchCapacity {
				childCount = retirementBlobBranchCapacity
			}
			var page [PageSize]byte
			if problem = encodeRetirementBlobBranch(&page, arena.bornTxn, level, scratch.pageNumbers[childStart:childStart+childCount], arena); problem.failed() {
				return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
			}
			if problem = arena.writePage(scratch.pageNumbers[outputStart+outputIndex], &page); problem.failed() {
				return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
			}
		}
		inputStart, inputCount, outputStart, level = outputStart, outputCount, outputStart+outputCount, level+1
	}
	if problem = arena.commit(checkpoint, nil); problem.failed() {
		return retirementBlobToken{}, retirementWithCleanup(problem, arena.rollback(checkpoint))
	}
	arena.tokenEpoch = nextTokenEpoch
	arena.activeTokenEpoch, arena.activeTokenGen = arena.tokenEpoch, checkpoint.generation
	return retirementBlobToken{
		arena: arena, root: scratch.pageNumbers[inputStart], pageCount: geometry.valueCount,
		byteLength: geometry.valueCount * 4, privatePages: geometry.totalPages,
		generation: checkpoint.generation, bornTxn: arena.bornTxn, epoch: arena.tokenEpoch,
		cleanupGeneration: checkpoint.generation, cleanupTokenEpoch: arena.tokenEpoch,
	}, retirementWriteError{}
}

func preflightRetirementBlob(pages []uint32, arena *privatePageArena, scratchLength int) (blobGeometry, retirementWriteError) {
	valueCount := uint64(len(pages))
	if valueCount == 0 {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrEmptyRetirementStream}
	}
	if valueCount > uint64(^uint32(0))+1 {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrRetirementStreamTooLong, first64: valueCount}
	}
	var previous uint32
	for index, current := range pages {
		if current < 2 || uint64(current) >= arena.committedPageCount {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrRetirementStreamPageOutOfBounds, page: current}
		}
		if index != 0 && current <= previous {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrRetirementStreamOrder, page: previous, secondPage: current}
		}
		previous = current
	}
	leafCount64, ok := checkedAdd(valueCount, retirementValuesPerBlobLeaf-1)
	if !ok {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	leafCount64 /= retirementValuesPerBlobLeaf
	if leafCount64 > uint64(^uint(0)>>1) {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	leaves := int(leafCount64)
	nodes, total, level := leaves, leaves, uint16(0)
	for nodes > 1 {
		next, ok := checkedIntAdd(nodes, retirementBlobBranchCapacity-1)
		if !ok {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		nodes = next / retirementBlobBranchCapacity
		total, ok = checkedIntAdd(total, nodes)
		if !ok {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		if level == MaxTreeLevel {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrTreeDepthExceeded}
		}
		level++
	}
	if problem := arena.requirePages(total); problem.failed() {
		return blobGeometry{}, problem
	}
	if scratchLength < total {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrBlobBuildScratchTooSmall, required: total, actual: scratchLength}
	}
	return blobGeometry{valueCount: valueCount, leafCount: leaves, totalPages: total, rootLevel: level}, retirementWriteError{}
}

func preflightRetirementBlobFromIndex(index *pageNumberIndex, arena *privatePageArena, scratchLength int) (blobGeometry, retirementWriteError) {
	if index == nil {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrPageNumberIndex}
	}
	if arena == nil {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
	}
	valueCount := index.len()
	if valueCount == 0 {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrEmptyRetirementStream}
	}
	if valueCount > uint64(^uint32(0))+1 {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrRetirementStreamTooLong, first64: valueCount}
	}
	leafCount64, ok := checkedAdd(valueCount, retirementValuesPerBlobLeaf-1)
	if !ok {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	leafCount64 /= retirementValuesPerBlobLeaf
	if leafCount64 > uint64(^uint(0)>>1) {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	leaves := int(leafCount64)
	nodes, total, level := leaves, leaves, uint16(0)
	for nodes > 1 {
		next, ok := checkedIntAdd(nodes, retirementBlobBranchCapacity-1)
		if !ok {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		nodes = next / retirementBlobBranchCapacity
		total, ok = checkedIntAdd(total, nodes)
		if !ok {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		if level == MaxTreeLevel {
			return blobGeometry{}, retirementWriteError{code: retirementWriteErrTreeDepthExceeded}
		}
		level++
	}
	geometry := blobGeometry{valueCount: valueCount, leafCount: leaves, totalPages: total, rootLevel: level}
	var previous uint32
	var visited uint64
	var streamProblem retirementWriteError
	err := index.visitAscending(func(current uint32) error {
		if visited == geometry.valueCount {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamCountMismatch, first64: geometry.valueCount, second64: visited + 1}
			return retirementIndexStreamStop{}
		}
		if current < 2 || uint64(current) >= arena.committedPageCount {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamPageOutOfBounds, page: current}
			return retirementIndexStreamStop{}
		}
		if visited != 0 && current <= previous {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamOrder, page: previous, secondPage: current}
			return retirementIndexStreamStop{}
		}
		previous = current
		visited++
		return nil
	})
	if err != nil {
		if _, stopped := err.(retirementIndexStreamStop); stopped {
			return blobGeometry{}, streamProblem
		}
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrPageNumberIndex}
	}
	if visited != geometry.valueCount {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrRetirementStreamCountMismatch, first64: geometry.valueCount, second64: visited}
	}
	if problem := arena.requirePages(geometry.totalPages); problem.failed() {
		return blobGeometry{}, problem
	}
	if scratchLength < geometry.totalPages {
		return blobGeometry{}, retirementWriteError{code: retirementWriteErrBlobBuildScratchTooSmall, required: geometry.totalPages, actual: scratchLength}
	}
	return geometry, retirementWriteError{}
}

func writeRetirementBlobIndexLeaves(index *pageNumberIndex, arena *privatePageArena, scratch *blobBuildScratch, geometry blobGeometry) retirementWriteError {
	var leaf [PageSize]byte
	var previous uint32
	var visited uint64
	var streamProblem retirementWriteError
	err := index.visitAscending(func(current uint32) error {
		if visited == geometry.valueCount {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamCountMismatch, first64: geometry.valueCount, second64: visited + 1}
			return retirementIndexStreamStop{}
		}
		if current < 2 || uint64(current) >= arena.committedPageCount {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamPageOutOfBounds, page: current}
			return retirementIndexStreamStop{}
		}
		if visited != 0 && current <= previous {
			streamProblem = retirementWriteError{code: retirementWriteErrRetirementStreamOrder, page: previous, secondPage: current}
			return retirementIndexStreamStop{}
		}
		leafIndex := int(visited / uint64(retirementValuesPerBlobLeaf))
		valueIndex := int(visited % uint64(retirementValuesPerBlobLeaf))
		leafValues := geometry.valueCount - uint64(leafIndex)*uint64(retirementValuesPerBlobLeaf)
		if leafValues > uint64(retirementValuesPerBlobLeaf) {
			leafValues = uint64(retirementValuesPerBlobLeaf)
		}
		if valueIndex == 0 {
			beginRetirementBlobLeaf(&leaf, arena.bornTxn, visited*4, leafValues)
		}
		binary.LittleEndian.PutUint32(leaf[blobLeafDataOffset+valueIndex*4:], current)
		previous = current
		visited++
		if uint64(valueIndex+1) == leafValues {
			sealPageNoFail(&leaf)
			if streamProblem = arena.writePage(scratch.pageNumbers[leafIndex], &leaf); streamProblem.failed() {
				return retirementIndexStreamStop{}
			}
		}
		return nil
	})
	if err != nil {
		if _, stopped := err.(retirementIndexStreamStop); stopped {
			return streamProblem
		}
		return retirementWriteError{code: retirementWriteErrPageNumberIndex}
	}
	if visited != geometry.valueCount {
		return retirementWriteError{code: retirementWriteErrRetirementStreamCountMismatch, first64: geometry.valueCount, second64: visited}
	}
	return retirementWriteError{}
}

func encodePageHeaderNoFail(page *[PageSize]byte, pageType PageType, bornTxn uint64, count, level, lower uint16, aux uint32) {
	copy(page[0:4], PageMagic)
	page[4] = byte(pageType)
	binary.LittleEndian.PutUint16(page[6:8], PageHeaderSize)
	binary.LittleEndian.PutUint64(page[8:16], bornTxn)
	binary.LittleEndian.PutUint16(page[16:18], count)
	binary.LittleEndian.PutUint16(page[18:20], level)
	binary.LittleEndian.PutUint16(page[20:22], lower)
	binary.LittleEndian.PutUint16(page[22:24], PageSize)
	binary.LittleEndian.PutUint32(page[24:28], aux)
}

func sealPageNoFail(page *[PageSize]byte) {
	crc := ^uint32(0)
	for index := 0; index < PageSize; index++ {
		value := page[index]
		if index >= PageCRCOffset && index < PageCRCOffset+4 {
			value = 0
		}
		crc = castagnoliTable[byte(crc)^value] ^ (crc >> 8)
	}
	binary.LittleEndian.PutUint32(page[PageCRCOffset:PageCRCOffset+4], ^crc)
}

func beginRetirementBlobLeaf(page *[PageSize]byte, bornTxn, logicalOffset, valueCount uint64) {
	*page = [PageSize]byte{}
	dataLength := int(valueCount) * 4
	encodePageHeaderNoFail(page, PageTypeBlobLeaf, bornTxn, 1, 0, uint16(blobLeafDataOffset+dataLength), uint32(blobKindRetirementPageList))
	binary.LittleEndian.PutUint64(page[32:40], logicalOffset)
	binary.LittleEndian.PutUint16(page[40:42], uint16(dataLength))
}

func encodeRetirementBlobLeaf(page *[PageSize]byte, bornTxn, logicalOffset uint64, values []uint32) {
	beginRetirementBlobLeaf(page, bornTxn, logicalOffset, uint64(len(values)))
	for index, value := range values {
		binary.LittleEndian.PutUint32(page[blobLeafDataOffset+index*4:], value)
	}
	sealPageNoFail(page)
}

func encodeRetirementBlobBranch(page *[PageSize]byte, bornTxn uint64, level uint16, children []uint32, arena *privatePageArena) retirementWriteError {
	*page = [PageSize]byte{}
	encodePageHeaderNoFail(page, PageTypeBlobBranch, bornTxn, uint16(len(children)), level, uint16(int(PageHeaderSize)+len(children)*blobBranchEntrySize), uint32(blobKindRetirementPageList))
	for index, childNumber := range children {
		token, poolProblem := arena.privateToken(childNumber, privatePageRetirementBlob)
		if poolProblem.failed() {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: childNumber}
		}
		logicalOffset, poolProblem := arena.readPrivateUint64(token, 32)
		if poolProblem.failed() {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: childNumber}
		}
		at := int(PageHeaderSize) + index*blobBranchEntrySize
		binary.LittleEndian.PutUint64(page[at:at+8], logicalOffset)
		binary.LittleEndian.PutUint32(page[at+8:at+12], childNumber)
	}
	sealPageNoFail(page)
	return retirementWriteError{}
}

type retirementTreeState struct {
	selectedTxn uint64
	pageCount   uint64
	root        uint32
	batchCount  uint64
}

type retirementPathFrame struct {
	pageNumber               uint32
	level                    uint16
	decodeTxn                uint64
	private                  bool
	residence                pageResidence
	page                     [PageSize]byte
	keepFrom                 uint16
	destinationSlot          int
	destinationEpoch         uint64
	destinationAuthorization privatePageAuthorization
	destinationOwner         privatePageOwner
	scratchEpoch             uint64
}

type retirementVirtualOverlay struct {
	frames     []retirementPathFrame
	length     int
	generation uint64
}

func (o *retirementVirtualOverlay) find(pageNumber uint32) (*retirementPathFrame, bool) {
	if o == nil || o.length < 0 || o.length > len(o.frames) {
		return nil, false
	}
	for index := 0; index < o.length; index++ {
		if o.frames[index].pageNumber == pageNumber {
			return &o.frames[index], true
		}
	}
	return nil, false
}

type retirementTreeEditResult struct {
	root                  uint32
	batchCount            uint64
	privatePages          int
	committedReplacements int
}

type childReference struct {
	maximum    uint64
	pageNumber uint32
	level      uint16
}

type upsertMode uint8

const (
	upsertAppend upsertMode = iota + 1
	upsertReplace
)

type appendPlan struct {
	pathLength int
	pages      int
	oldRoot    childReference
	mode       upsertMode
	oldBatch   retirementBatch
}

type pageResidenceKind uint8

const (
	pageResidenceSelectedCommitted pageResidenceKind = iota + 1
	pageResidenceCurrentScopePrivate
	pageResidencePriorScopePrivate
)

type pageResidence struct {
	kind       pageResidenceKind
	generation uint64
	prior      privateWriterDraftPageProvenance
}

func (r pageResidence) private() bool {
	return r.kind == pageResidenceCurrentScopePrivate || r.kind == pageResidencePriorScopePrivate
}

type blobResidenceKind uint8

const (
	blobResidenceDerive blobResidenceKind = iota + 1
	blobResidenceCommitted
	blobResidenceCurrentPrivate
	blobResidencePriorPrivate
)

type blobResidenceExpectation struct {
	kind       blobResidenceKind
	generation uint64
	prior      privateWriterDraftPageProvenance
}

type listedPagePolicy uint8

const (
	listedPageRegister listedPagePolicy = iota + 1
	listedPageMarkRequired
	listedPageSatisfyRequired
)

func validateRetirementEditInputs(state retirementTreeState, arena *privatePageArena, replacements *committedReplacementLedger) retirementWriteError {
	if state.selectedTxn == 0 {
		return retirementWriteError{code: retirementWriteErrSelectedTransactionOutOfRange}
	}
	if state.pageCount != arena.committedPageCount {
		return retirementWriteError{code: retirementWriteErrPageCountOutOfRange, first64: state.pageCount, second64: arena.pendingPageCount}
	}
	if (state.root == 0) != (state.batchCount == 0) {
		return retirementWriteError{code: retirementWriteErrRootCountMismatch}
	}
	expected := state.selectedTxn + 1
	if expected == 0 {
		return retirementWriteError{code: retirementWriteErrSelectedTransactionOverflow, first64: state.selectedTxn}
	}
	if arena.bornTxn != expected {
		return retirementWriteError{code: retirementWriteErrTransactionOrder, first64: state.selectedTxn, second64: arena.bornTxn}
	}
	if state.batchCount > arena.bornTxn-1 {
		return retirementWriteError{code: retirementWriteErrBatchCountOutOfRange, first64: state.batchCount}
	}
	if state.root != 0 && (state.root < 2 || uint64(state.root) >= arena.pendingPageCount) {
		return retirementWriteError{code: retirementWriteErrRootOutOfBounds, page: state.root}
	}
	for _, replacement := range replacements.used() {
		if replacement.pageNumber < 2 || uint64(replacement.pageNumber) >= state.pageCount {
			return retirementWriteError{code: retirementWriteErrRootOutOfBounds, page: replacement.pageNumber}
		}
		if arena.pagePool().contains(replacement.pageNumber) {
			return retirementWriteError{code: retirementWriteErrCommittedReplacementIsPrivate, page: replacement.pageNumber}
		}
	}
	return retirementWriteError{}
}

type privateWriterResidenceSource interface {
	residence(uint32) (privateWriterDraftPageResidence, privateWriterFixedPointError)
}

func retirementCurrentScopeSlot(
	arena *privatePageArena,
	pageNumber uint32,
) (int, bool) {
	if arena == nil || arena.pagePool() == nil {
		return 0, false
	}
	index, found := arena.pagePool().slotIndex(pageNumber)
	if !found {
		return 0, false
	}
	if !arena.scoped {
		return index, true
	}
	slot := &arena.pagePool().slots[index]
	return index, slot.scopeID == arena.scope.id && slot.scopeAnchorIndex == arena.scope.anchor
}

func readMetadataPage(source committedPageSource, state retirementTreeState, arena *privatePageArena, pageNumber uint32, expectedOrigin privatePageOrigin, selectedRole pageRole, destination *[PageSize]byte, roles *pageRoleIndex) (pageResidence, retirementWriteError) {
	if _, current := retirementCurrentScopeSlot(arena, pageNumber); current {
		token, poolProblem := arena.privateToken(pageNumber, expectedOrigin)
		if poolProblem.code == privatePagePoolErrOriginMismatch {
			return pageResidence{}, retirementWriteError{code: retirementWriteErrPrivatePageOriginMismatch, page: pageNumber, requestedRole: selectedRole}
		}
		if poolProblem.failed() {
			return pageResidence{}, retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		if problem := roles.selectPage(pageNumber, selectedRole, true); problem.failed() {
			return pageResidence{}, problem
		}
		if arena.scoped {
			poolProblem = arena.pagePool().readPageInScope(arena.scope, token, destination)
		} else {
			poolProblem = arena.pagePool().readPage(token, destination)
		}
		if poolProblem.failed() {
			return pageResidence{}, retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		return pageResidence{
			kind: pageResidenceCurrentScopePrivate, generation: token.generation,
		}, retirementWriteError{}
	}
	if draft, ok := source.(privateWriterResidenceSource); ok {
		residence, fixedProblem := draft.residence(pageNumber)
		if fixedProblem.failed() {
			return pageResidence{}, retirementSourceProblem(pageSourceStatus{
				code: pageSourceErrForkedHandle, page: pageNumber,
			})
		}
		if residence.kind == privateWriterPagePriorScopePrivate {
			provenance := residence.provenance
			if provenance.owner != privatePageOwnerRetirement || provenance.origin != expectedOrigin {
				return pageResidence{}, retirementWriteError{
					code: retirementWriteErrPrivatePageOriginMismatch,
					page: pageNumber, requestedRole: selectedRole,
				}
			}
			if problem := roles.bindPriorPrivate(pageNumber, provenance); problem.failed() {
				return pageResidence{}, problem
			}
			if problem := roles.selectPage(pageNumber, selectedRole, true); problem.failed() {
				return pageResidence{}, problem
			}
			if status := source.readPageStatus(pageNumber, destination); status.failed() {
				return pageResidence{}, retirementSourceProblem(status)
			}
			return pageResidence{
				kind:       pageResidencePriorScopePrivate,
				generation: provenance.generation,
				prior:      provenance,
			}, retirementWriteError{}
		}
	}
	if arena.pagePool().contains(pageNumber) {
		return pageResidence{}, retirementWriteError{
			code: retirementWriteErrPrivatePageUnavailable, page: pageNumber,
		}
	}
	if pageNumber < 2 || uint64(pageNumber) >= state.pageCount {
		return pageResidence{}, retirementWriteError{code: retirementWriteErrRootOutOfBounds, page: pageNumber}
	}
	if status := source.readPageStatus(pageNumber, destination); status.failed() {
		return pageResidence{}, retirementSourceProblem(status)
	}
	if problem := roles.selectPage(pageNumber, selectedRole, false); problem.failed() {
		return pageResidence{}, problem
	}
	return pageResidence{kind: pageResidenceSelectedCommitted}, retirementWriteError{}
}

func readRetirementTreeFrame(source committedPageSource, state retirementTreeState, arena *privatePageArena, pageNumber uint32, frame *retirementPathFrame, roles *pageRoleIndex) retirementWriteError {
	return readRetirementTreeFrameWithOverlay(source, state, arena, pageNumber, frame, roles, nil)
}

func readRetirementTreeFrameWithOverlay(
	source committedPageSource,
	state retirementTreeState,
	arena *privatePageArena,
	pageNumber uint32,
	frame *retirementPathFrame,
	roles *pageRoleIndex,
	overlay *retirementVirtualOverlay,
) retirementWriteError {
	if virtual, found := overlay.find(pageNumber); found {
		if problem := roles.selectPage(pageNumber, pageRoleSelectedRetirementTree, true); problem.failed() {
			return problem
		}
		*frame = *virtual
		return retirementWriteError{}
	}
	residence, problem := readMetadataPage(source, state, arena, pageNumber, privatePageRetirementTree, pageRoleSelectedRetirementTree, &frame.page, roles)
	if problem.failed() {
		return problem
	}
	frame.pageNumber, frame.residence, frame.private, frame.keepFrom =
		pageNumber, residence, residence.private(), 0
	frame.destinationSlot = privatePagePoolNoIndex
	frame.destinationEpoch = 0
	frame.destinationAuthorization = privatePageAuthorizationNone
	frame.destinationOwner = privatePageOwnerNone
	frame.scratchEpoch = 0
	frame.decodeTxn = state.selectedTxn
	if residence.private() {
		frame.decodeTxn = arena.bornTxn
	}
	header, headerProblem := decodePageHeaderNoAlloc(frame.page[:], frame.decodeTxn)
	if headerProblem.code != 0 {
		return retirementHeaderProblem(headerProblem)
	}
	frame.level = header.Level
	return retirementWriteError{}
}

func classifyRetirementPageResidence(
	source committedPageSource,
	state retirementTreeState,
	arena *privatePageArena,
	pageNumber uint32,
	expectedOrigin privatePageOrigin,
	expectedRole pageRole,
) (pageResidence, retirementWriteError) {
	if _, current := retirementCurrentScopeSlot(arena, pageNumber); current {
		token, problem := arena.privateToken(pageNumber, expectedOrigin)
		if problem.code == privatePagePoolErrOriginMismatch {
			return pageResidence{}, retirementWriteError{
				code: retirementWriteErrPrivatePageOriginMismatch,
				page: pageNumber, requestedRole: expectedRole,
			}
		}
		if problem.failed() {
			return pageResidence{}, retirementWriteError{
				code: retirementWriteErrPrivatePageUnavailable, page: pageNumber,
			}
		}
		return pageResidence{
			kind: pageResidenceCurrentScopePrivate, generation: token.generation,
		}, retirementWriteError{}
	}
	if draft, ok := source.(privateWriterResidenceSource); ok {
		residence, problem := draft.residence(pageNumber)
		if problem.failed() {
			return pageResidence{}, retirementSourceProblem(pageSourceStatus{
				code: pageSourceErrForkedHandle, page: pageNumber,
			})
		}
		if residence.kind == privateWriterPagePriorScopePrivate {
			provenance := residence.provenance
			if provenance.owner != privatePageOwnerRetirement || provenance.origin != expectedOrigin {
				return pageResidence{}, retirementWriteError{
					code: retirementWriteErrPrivatePageOriginMismatch,
					page: pageNumber, requestedRole: expectedRole,
				}
			}
			return pageResidence{
				kind:       pageResidencePriorScopePrivate,
				generation: provenance.generation,
				prior:      provenance,
			}, retirementWriteError{}
		}
	}
	if arena.pagePool().contains(pageNumber) {
		return pageResidence{}, retirementWriteError{
			code: retirementWriteErrPrivatePageUnavailable, page: pageNumber,
		}
	}
	if pageNumber < 2 || uint64(pageNumber) >= state.pageCount {
		return pageResidence{}, retirementWriteError{
			code: retirementWriteErrPrivatePageUnavailable, page: pageNumber,
		}
	}
	return pageResidence{kind: pageResidenceSelectedCommitted}, retirementWriteError{}
}

func requireParentChild(
	source committedPageSource,
	parent uint32,
	parentResidence pageResidence,
	child uint32,
	state retirementTreeState,
	arena *privatePageArena,
	expectedOrigin privatePageOrigin,
	expectedRole pageRole,
) (bool, retirementWriteError) {
	return requireParentChildWithOverlay(
		source, parent, parentResidence, child, state, arena, expectedOrigin, expectedRole, nil,
	)
}

func requireParentChildWithOverlay(
	source committedPageSource,
	parent uint32,
	parentResidence pageResidence,
	child uint32,
	state retirementTreeState,
	arena *privatePageArena,
	expectedOrigin privatePageOrigin,
	expectedRole pageRole,
	overlay *retirementVirtualOverlay,
) (bool, retirementWriteError) {
	if parentResidence.kind == pageResidenceSelectedCommitted {
		childResidence, problem := classifyRetirementPageResidence(
			source, state, arena, child, expectedOrigin, expectedRole,
		)
		if problem.failed() {
			return false, problem
		}
		if childResidence.kind != pageResidenceSelectedCommitted {
			return false, retirementWriteError{code: retirementWriteErrCommittedParentPrivateChild, page: parent, secondPage: child}
		}
		return false, retirementWriteError{}
	}
	if _, found := overlay.find(child); found {
		return true, retirementWriteError{}
	}
	childResidence, problem := classifyRetirementPageResidence(
		source, state, arena, child, expectedOrigin, expectedRole,
	)
	if problem.failed() {
		return false, problem
	}
	if parentResidence.kind == pageResidencePriorScopePrivate &&
		childResidence.kind == pageResidenceCurrentScopePrivate {
		return false, retirementWriteError{
			code: retirementWriteErrPrivateScopeMismatch, page: parent,
			secondPage: child,
		}
	}
	if parentResidence.kind == pageResidencePriorScopePrivate &&
		childResidence.kind == pageResidencePriorScopePrivate &&
		(parentResidence.prior.workUnit != childResidence.prior.workUnit ||
			parentResidence.prior.scopeID != childResidence.prior.scopeID ||
			parentResidence.prior.scopeAnchor != childResidence.prior.scopeAnchor) {
		return false, retirementWriteError{
			code: retirementWriteErrPrivateScopeMismatch, page: parent,
			secondPage: child,
		}
	}
	return childResidence.private(), retirementWriteError{}
}

func validateRetirementBranchChildren(source committedPageSource, frame *retirementPathFrame, branch retirementBranch, state retirementTreeState, arena *privatePageArena, roles *pageRoleIndex) retirementWriteError {
	return validateRetirementBranchChildrenWithOverlay(source, frame, branch, state, arena, roles, nil)
}

func validateRetirementBranchChildrenWithOverlay(
	source committedPageSource,
	frame *retirementPathFrame,
	branch retirementBranch,
	state retirementTreeState,
	arena *privatePageArena,
	roles *pageRoleIndex,
	overlay *retirementVirtualOverlay,
) retirementWriteError {
	for index := 0; index < branch.len(); index++ {
		entry, status := branch.entryStatus(index)
		if status.failed() {
			return retirementPageProblem(status)
		}
		private, problem := requireParentChildWithOverlay(source, frame.pageNumber, frame.residence, entry.childPage, state, arena, privatePageRetirementTree, pageRoleSelectedRetirementTree, overlay)
		if problem.failed() {
			return problem
		}
		if private {
			childResidence, childProblem := classifyRetirementPageResidence(
				source, state, arena, entry.childPage,
				privatePageRetirementTree, pageRoleSelectedRetirementTree,
			)
			if childProblem.failed() {
				return childProblem
			}
			if childResidence.kind == pageResidencePriorScopePrivate {
				if problem = roles.bindPriorPrivate(entry.childPage, childResidence.prior); problem.failed() {
					return problem
				}
			}
		}
		if problem = roles.reference(entry.childPage, pageRoleReferencedRetirementTree, private); problem.failed() {
			return problem
		}
	}
	return retirementWriteError{}
}

func validateRetirementLeafBlobRoots(source committedPageSource, frame *retirementPathFrame, leaf retirementLeaf, state retirementTreeState, arena *privatePageArena, roles *pageRoleIndex) retirementWriteError {
	for index := 0; index < leaf.len(); index++ {
		batch, status := leaf.batchStatus(index)
		if status.failed() {
			return retirementPageProblem(status)
		}
		private, problem := requireParentChild(source, frame.pageNumber, frame.residence, batch.pageListBlobRoot, state, arena, privatePageRetirementBlob, pageRoleSelectedRetirementBlob)
		if problem.failed() {
			return problem
		}
		if private {
			childResidence, childProblem := classifyRetirementPageResidence(
				source, state, arena, batch.pageListBlobRoot,
				privatePageRetirementBlob, pageRoleSelectedRetirementBlob,
			)
			if childProblem.failed() {
				return childProblem
			}
			if childResidence.kind == pageResidencePriorScopePrivate {
				if problem = roles.bindPriorPrivate(batch.pageListBlobRoot, childResidence.prior); problem.failed() {
					return problem
				}
			}
		}
		if problem = roles.reference(batch.pageListBlobRoot, pageRoleReferencedRetirementBlob, private); problem.failed() {
			return problem
		}
	}
	return retirementWriteError{}
}

func requireMaximum(expected uint64, hasExpected bool, actual uint64) retirementWriteError {
	if hasExpected && expected != actual {
		return retirementWriteError{code: retirementWriteErrChildMaximumMismatch, first64: expected, second64: actual}
	}
	return retirementWriteError{}
}

func retireTreeFrame(frame *retirementPathFrame, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) retirementWriteError {
	if frame.destinationSlot != privatePagePoolNoIndex && frame.scratchEpoch != 0 {
		return retirementWriteError{}
	}
	if frame.residence.private() {
		if problem := releases.push(frame.pageNumber); problem.failed() {
			return problem
		}
		return roles.retirePrivate(frame.pageNumber, privatePageRetirementTree)
	}
	entry := committedPageReplacement{pageNumber: frame.pageNumber, origin: committedPageRetirementTree}
	if problem := replacements.append(entry); problem.failed() {
		return problem
	}
	return roles.retireCommitted(frame.pageNumber, entry.origin)
}

func retireBlobPage(pageNumber uint32, residence pageResidence, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) retirementWriteError {
	if residence.private() {
		if problem := releases.push(pageNumber); problem.failed() {
			return problem
		}
		return roles.retirePrivate(pageNumber, privatePageRetirementBlob)
	}
	entry := committedPageReplacement{pageNumber: pageNumber, origin: committedPageRetirementBlob}
	if problem := replacements.append(entry); problem.failed() {
		return problem
	}
	return roles.retireCommitted(pageNumber, entry.origin)
}

func requireBlobChild(source committedPageSource, parent, child uint32, expected blobResidenceExpectation, state retirementTreeState, arena *privatePageArena) (bool, retirementWriteError) {
	if expected.kind == blobResidenceCommitted {
		return requireParentChild(
			source, parent, pageResidence{kind: pageResidenceSelectedCommitted},
			child, state, arena, privatePageRetirementBlob, pageRoleSelectedRetirementBlob,
		)
	}
	if expected.kind != blobResidenceCurrentPrivate && expected.kind != blobResidencePriorPrivate {
		return false, retirementWriteError{code: retirementWriteErrBlobResidenceMismatch, page: child}
	}
	parentResidence := pageResidence{
		kind: pageResidenceCurrentScopePrivate, generation: expected.generation,
	}
	if expected.kind == blobResidencePriorPrivate {
		parentResidence.kind = pageResidencePriorScopePrivate
		parentResidence.prior = expected.prior
	}
	private, problem := requireParentChild(
		source, parent, parentResidence, child, state, arena,
		privatePageRetirementBlob, pageRoleSelectedRetirementBlob,
	)
	if problem.failed() {
		return false, problem
	}
	if !private {
		return false, retirementWriteError{
			code: retirementWriteErrPrivateBlobNonPrivateChild,
			page: parent, secondPage: child, first64: expected.generation,
		}
	}
	residence, problem := classifyRetirementPageResidence(
		source, state, arena, child, privatePageRetirementBlob, pageRoleSelectedRetirementBlob,
	)
	if problem.failed() {
		return false, problem
	}
	if residence.generation != expected.generation {
		return false, retirementWriteError{
			code:    retirementWriteErrBlobTokenGenerationMismatch,
			first64: expected.generation, second64: residence.generation,
		}
	}
	if expected.kind == blobResidenceCurrentPrivate &&
		residence.kind != pageResidenceCurrentScopePrivate {
		return false, retirementWriteError{code: retirementWriteErrBlobResidenceMismatch, page: child}
	}
	if expected.kind == blobResidencePriorPrivate &&
		(residence.kind != pageResidencePriorScopePrivate ||
			residence.prior.workUnit != expected.prior.workUnit ||
			residence.prior.scopeID != expected.prior.scopeID ||
			residence.prior.scopeAnchor != expected.prior.scopeAnchor) {
		return false, retirementWriteError{code: retirementWriteErrBlobResidenceMismatch, page: child}
	}
	return true, retirementWriteError{}
}

type blobScanState struct {
	previous     uint32
	havePrevious bool
	values       uint64
}

type retirementBlobScanPage struct{ bytes [PageSize]byte }
type retirementBlobScanScratch struct{ pages []retirementBlobScanPage }

func verifyWriterPageCRC(page *[PageSize]byte) bool {
	want := binary.LittleEndian.Uint32(page[PageCRCOffset : PageCRCOffset+4])
	crc := ^uint32(0)
	for index := 0; index < PageSize; index++ {
		value := page[index]
		if index >= PageCRCOffset && index < PageCRCOffset+4 {
			value = 0
		}
		crc = castagnoliTable[byte(crc)^value] ^ (crc >> 8)
	}
	return ^crc == want
}

func writerBytesNonzero(page *[PageSize]byte, start, end int) bool {
	for index := start; index < end; index++ {
		if page[index] != 0 {
			return true
		}
	}
	return false
}

func validateWriterBlobLeaf(page *[PageSize]byte, header PageHeader) (uint64, int, retirementWriteError) {
	if header.Aux != uint32(blobKindRetirementPageList) {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrWrongKind, wireKind: header.Aux})
	}
	if header.ItemCount != 1 {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrLeafItemCount, itemCount: header.ItemCount})
	}
	dataLength := int(binary.LittleEndian.Uint16(page[40:42]))
	if dataLength == 0 || dataLength > blobLeafCapacity {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrDataLength, dataLength: uint16(dataLength)})
	}
	if dataLength%4 != 0 {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrDataAlignment, dataLength: uint16(dataLength), alignment: 4})
	}
	if int(header.Lower) != blobLeafDataOffset+dataLength || header.Upper != PageSize {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrFixedGeometry})
	}
	if writerBytesNonzero(page, 42, blobLeafDataOffset) || writerBytesNonzero(page, blobLeafDataOffset+dataLength, PageSize) {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrReservedNonzero})
	}
	if !verifyWriterPageCRC(page) {
		return 0, 0, retirementBlobProblem(blobPageStatus{code: blobPageErrChecksum})
	}
	return binary.LittleEndian.Uint64(page[32:40]), dataLength, retirementWriteError{}
}

func validateWriterBlobBranch(page *[PageSize]byte, header PageHeader, pageCount uint64) (int, retirementWriteError) {
	if header.Aux != uint32(blobKindRetirementPageList) {
		return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrWrongKind, wireKind: header.Aux})
	}
	count := int(header.ItemCount)
	if count == 0 {
		return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrEmptyBranch})
	}
	lower := int(PageHeaderSize) + count*blobBranchEntrySize
	if int(header.Lower) != lower || header.Upper != PageSize {
		return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrFixedGeometry})
	}
	if writerBytesNonzero(page, lower, PageSize) {
		return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrReservedNonzero})
	}
	if !verifyWriterPageCRC(page) {
		return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrChecksum})
	}
	var previous uint64
	for index := 0; index < count; index++ {
		at := int(PageHeaderSize) + index*blobBranchEntrySize
		if binary.LittleEndian.Uint32(page[at+12:at+16]) != 0 {
			return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrReservedNonzero})
		}
		child := binary.LittleEndian.Uint32(page[at+8 : at+12])
		if child < 2 || uint64(child) >= pageCount {
			return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrChildOutOfBounds, childPage: child})
		}
		offset := binary.LittleEndian.Uint64(page[at : at+8])
		if index != 0 && offset <= previous {
			return 0, retirementBlobProblem(blobPageStatus{code: blobPageErrOffsetsNotStrict})
		}
		previous = offset
	}
	return count, retirementWriteError{}
}

func writerBlobBranchEntry(page *[PageSize]byte, index int) blobBranchEntry {
	at := int(PageHeaderSize) + index*blobBranchEntrySize
	return blobBranchEntry{logicalOffset: binary.LittleEndian.Uint64(page[at : at+8]), childPage: binary.LittleEndian.Uint32(page[at+8 : at+12])}
}

func scanRetirementBatchBlob(source committedPageSource, state retirementTreeState, arena *privatePageArena, batch retirementBatch, expectedGeneration uint64, forceGeneration bool, policy listedPagePolicy, retire bool, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex, scratch *retirementBlobScanScratch) retirementWriteError {
	length, status := batch.blobLengthStatus()
	if status.failed() {
		return retirementPageProblem(status)
	}
	expected := blobResidenceExpectation{kind: blobResidenceDerive}
	if forceGeneration {
		expected = blobResidenceExpectation{
			kind: blobResidenceCurrentPrivate, generation: expectedGeneration,
		}
	}
	scan := blobScanState{}
	if problem := scanRetirementBlobNode(source, state, arena, batch.pageListBlobRoot, 0, false, 0, length, length, expected, policy, retire, replacements, releases, roles, scratch, &scan, 0); problem.failed() {
		return problem
	}
	if scan.values != batch.pageCount {
		return retirementWriteError{code: retirementWriteErrBlobPageCountMismatch, first64: batch.pageCount, second64: scan.values}
	}
	return retirementWriteError{}
}

func scanRetirementBlobNode(source committedPageSource, state retirementTreeState, arena *privatePageArena, pageNumber uint32, expectedLevel uint16, hasExpectedLevel bool, expectedStart, expectedEnd, ownerLength uint64, expected blobResidenceExpectation, policy listedPagePolicy, retire bool, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex, scratch *retirementBlobScanScratch, scan *blobScanState, depth int) retirementWriteError {
	if depth > int(MaxTreeLevel) {
		return retirementWriteError{code: retirementWriteErrTreeDepthExceeded}
	}
	if scratch == nil || depth >= len(scratch.pages) {
		actual := 0
		if scratch != nil {
			actual = len(scratch.pages)
		}
		return retirementWriteError{code: retirementWriteErrBlobScanScratchTooSmall, required: depth + 1, actual: actual}
	}
	page := &scratch.pages[depth].bytes
	residence, problem := readMetadataPage(source, state, arena, pageNumber, privatePageRetirementBlob, pageRoleSelectedRetirementBlob, page, roles)
	if problem.failed() {
		return problem
	}
	switch expected.kind {
	case blobResidenceDerive:
		switch residence.kind {
		case pageResidenceSelectedCommitted:
			expected = blobResidenceExpectation{kind: blobResidenceCommitted}
		case pageResidenceCurrentScopePrivate:
			expected = blobResidenceExpectation{
				kind: blobResidenceCurrentPrivate, generation: residence.generation,
			}
		case pageResidencePriorScopePrivate:
			expected = blobResidenceExpectation{
				kind:       blobResidencePriorPrivate,
				generation: residence.generation,
				prior:      residence.prior,
			}
		}
	case blobResidenceCommitted:
		if residence.kind != pageResidenceSelectedCommitted {
			return retirementWriteError{code: retirementWriteErrBlobResidenceMismatch, page: pageNumber}
		}
	case blobResidenceCurrentPrivate:
		if residence.kind != pageResidenceCurrentScopePrivate ||
			residence.generation != expected.generation {
			return retirementWriteError{code: retirementWriteErrBlobTokenGenerationMismatch, first64: expected.generation, second64: residence.generation}
		}
	case blobResidencePriorPrivate:
		if residence.kind != pageResidencePriorScopePrivate ||
			residence.generation != expected.generation ||
			residence.prior.workUnit != expected.prior.workUnit ||
			residence.prior.scopeID != expected.prior.scopeID ||
			residence.prior.scopeAnchor != expected.prior.scopeAnchor {
			return retirementWriteError{code: retirementWriteErrBlobResidenceMismatch, page: pageNumber}
		}
	}
	decodeTxn := state.selectedTxn
	if residence.private() {
		decodeTxn = arena.bornTxn
	}
	header, headerProblem := decodePageHeaderNoAlloc(page[:], decodeTxn)
	if headerProblem.code != 0 {
		return retirementHeaderProblem(headerProblem)
	}
	if hasExpectedLevel && header.Level != expectedLevel {
		return retirementWriteError{code: retirementWriteErrChildLevel, first64: uint64(expectedLevel), second64: uint64(header.Level)}
	}
	switch header.PageType {
	case PageTypeBlobLeaf:
		if hasExpectedLevel && expectedLevel != 0 {
			return retirementWriteError{code: retirementWriteErrChildType, pageType: PageTypeBlobLeaf}
		}
		logicalOffset, dataLength, leafProblem := validateWriterBlobLeaf(page, header)
		if leafProblem.failed() {
			return leafProblem
		}
		if logicalOffset != expectedStart {
			return retirementWriteError{code: retirementWriteErrBlobOffsetMismatch, first64: expectedStart, second64: logicalOffset}
		}
		actualEnd, ok := checkedAdd(expectedStart, uint64(dataLength))
		if !ok {
			return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		if actualEnd != expectedEnd || (actualEnd < ownerLength && dataLength != blobLeafCapacity) {
			return retirementWriteError{code: retirementWriteErrBlobLengthMismatch, first64: expectedEnd, second64: actualEnd}
		}
		if retire {
			if problem = retireBlobPage(pageNumber, residence, replacements, releases, roles); problem.failed() {
				return problem
			}
		}
		for offset := 0; offset < dataLength; offset += 4 {
			current := binary.LittleEndian.Uint32(page[blobLeafDataOffset+offset : blobLeafDataOffset+offset+4])
			if current < 2 || uint64(current) >= state.pageCount {
				return retirementWriteError{code: retirementWriteErrRetirementStreamPageOutOfBounds, page: current}
			}
			if scan.havePrevious && current <= scan.previous {
				return retirementWriteError{code: retirementWriteErrRetirementStreamOrder, page: scan.previous, secondPage: current}
			}
			switch policy {
			case listedPageRegister:
				problem = roles.listed(current, false)
			case listedPageMarkRequired:
				problem = roles.requireInNewList(current)
			case listedPageSatisfyRequired:
				problem = roles.listed(current, true)
			}
			if problem.failed() {
				return problem
			}
			scan.previous, scan.havePrevious = current, true
			scan.values++
			if scan.values == 0 {
				return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
			}
		}
		return retirementWriteError{}
	case PageTypeBlobBranch:
		count, branchProblem := validateWriterBlobBranch(page, header, arena.pendingPageCount)
		if branchProblem.failed() {
			return branchProblem
		}
		for index := 0; index < count; index++ {
			entry := writerBlobBranchEntry(page, index)
			private, childProblem := requireBlobChild(source, pageNumber, entry.childPage, expected, state, arena)
			if childProblem.failed() {
				return childProblem
			}
			if expected.kind == blobResidencePriorPrivate {
				childResidence, residenceProblem := classifyRetirementPageResidence(
					source, state, arena, entry.childPage,
					privatePageRetirementBlob, pageRoleSelectedRetirementBlob,
				)
				if residenceProblem.failed() {
					return residenceProblem
				}
				if childProblem = roles.bindPriorPrivate(entry.childPage, childResidence.prior); childProblem.failed() {
					return childProblem
				}
			}
			if childProblem = roles.reference(entry.childPage, pageRoleReferencedRetirementBlob, private); childProblem.failed() {
				return childProblem
			}
		}
		first := writerBlobBranchEntry(page, 0)
		if first.logicalOffset != expectedStart {
			return retirementWriteError{code: retirementWriteErrBlobOffsetMismatch, first64: expectedStart, second64: first.logicalOffset}
		}
		if retire {
			if problem = retireBlobPage(pageNumber, residence, replacements, releases, roles); problem.failed() {
				return problem
			}
		}
		for index := 0; index < count; index++ {
			entry := writerBlobBranchEntry(page, index)
			childEnd := expectedEnd
			if index+1 < count {
				next := writerBlobBranchEntry(page, index+1)
				childEnd = next.logicalOffset
			}
			if childEnd <= entry.logicalOffset || childEnd > expectedEnd {
				return retirementWriteError{code: retirementWriteErrBlobLengthMismatch, first64: expectedEnd, second64: childEnd}
			}
			if problem = scanRetirementBlobNode(source, state, arena, entry.childPage, header.Level-1, true, entry.logicalOffset, childEnd, ownerLength, expected, policy, retire, replacements, releases, roles, scratch, scan, depth+1); problem.failed() {
				return problem
			}
		}
		return retirementWriteError{}
	default:
		return retirementWriteError{code: retirementWriteErrChildType, pageType: header.PageType}
	}
}

func preflightRetirementUpsert(source committedPageSource, state retirementTreeState, batch retirementBatch, arena *privatePageArena, path []retirementPathFrame, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) (appendPlan, retirementWriteError) {
	return preflightRetirementUpsertWithOverlay(source, state, batch, arena, path, replacements, releases, roles, nil)
}

func preflightRetirementUpsertWithOverlay(
	source committedPageSource,
	state retirementTreeState,
	batch retirementBatch,
	arena *privatePageArena,
	path []retirementPathFrame,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
	overlay *retirementVirtualOverlay,
) (appendPlan, retirementWriteError) {
	if status := source.checkAccessStatus(); status.failed() {
		return appendPlan{}, retirementSourceProblem(status)
	}
	if state.root == 0 {
		return appendPlan{pages: 1, mode: upsertAppend}, retirementWriteError{}
	}
	pageNumber, depth := state.root, 0
	var expectedLevel uint16
	hasExpectedLevel := false
	var expectedMaximum uint64
	hasExpectedMaximum := false
	var lowerBound uint64
	hasLowerBound := false
	for {
		if depth >= len(path) || depth >= retirementWriterPathCapacity {
			return appendPlan{}, retirementWriteError{code: retirementWriteErrPathBufferTooSmall, required: depth + 1, actual: len(path)}
		}
		if problem := readRetirementTreeFrameWithOverlay(source, state, arena, pageNumber, &path[depth], roles, overlay); problem.failed() {
			return appendPlan{}, problem
		}
		frame := &path[depth]
		if hasExpectedLevel && frame.level != expectedLevel {
			return appendPlan{}, retirementWriteError{code: retirementWriteErrChildLevel, first64: uint64(expectedLevel), second64: uint64(frame.level)}
		}
		header, headerProblem := decodePageHeaderNoAlloc(frame.page[:], frame.decodeTxn)
		if headerProblem.code != 0 {
			return appendPlan{}, retirementHeaderProblem(headerProblem)
		}
		switch header.PageType {
		case PageTypeRetirementLeaf:
			leaf, status := openRetirementLeafStatus(frame.page[:], frame.decodeTxn, arena.pendingPageCount)
			if status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if status = leaf.verifyCRCStatus(); status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if problem := validateRetirementLeafBlobRoots(source, frame, leaf, state, arena, roles); problem.failed() {
				return appendPlan{}, problem
			}
			maximum, status := leaf.maximumKeyStatus()
			if status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if problem := requireMaximum(expectedMaximum, hasExpectedMaximum, maximum); problem.failed() {
				return appendPlan{}, problem
			}
			if hasLowerBound {
				first, s := leaf.batchStatus(0)
				if s.failed() {
					return appendPlan{}, retirementPageProblem(s)
				}
				if first.retiredByTxn <= lowerBound {
					return appendPlan{}, retirementWriteError{code: retirementWriteErrRetirementTreeOrder, first64: lowerBound, second64: first.retiredByTxn}
				}
			}
			depth++
			goto descended
		case PageTypeRetirementBranch:
			branch, status := openRetirementBranchStatus(frame.page[:], frame.decodeTxn, arena.pendingPageCount)
			if status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if status = branch.verifyCRCStatus(); status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if problem := validateRetirementBranchChildrenWithOverlay(source, frame, branch, state, arena, roles, overlay); problem.failed() {
				return appendPlan{}, problem
			}
			maximum, status := branch.maximumKeyStatus()
			if status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			if problem := requireMaximum(expectedMaximum, hasExpectedMaximum, maximum); problem.failed() {
				return appendPlan{}, problem
			}
			last := branch.len() - 1
			if last > 0 {
				sibling, s := branch.entryStatus(last - 1)
				if s.failed() {
					return appendPlan{}, retirementPageProblem(s)
				}
				if !hasLowerBound || sibling.maxRetiredByTxn > lowerBound {
					lowerBound = sibling.maxRetiredByTxn
				}
				hasLowerBound = true
			}
			entry, status := branch.entryStatus(last)
			if status.failed() {
				return appendPlan{}, retirementPageProblem(status)
			}
			pageNumber, expectedLevel, hasExpectedLevel = entry.childPage, branch.level-1, true
			expectedMaximum, hasExpectedMaximum = entry.maxRetiredByTxn, true
			depth++
		default:
			code := retirementWriteErrChildType
			if depth == 0 {
				code = retirementWriteErrRootType
			}
			return appendPlan{}, retirementWriteError{code: code, pageType: header.PageType}
		}
	}

descended:
	leafFrame := &path[depth-1]
	leaf, status := openRetirementLeafStatus(leafFrame.page[:], leafFrame.decodeTxn, arena.pendingPageCount)
	if status.failed() {
		return appendPlan{}, retirementPageProblem(status)
	}
	maximum, status := leaf.maximumKeyStatus()
	if status.failed() {
		return appendPlan{}, retirementPageProblem(status)
	}
	if maximum > batch.retiredByTxn {
		return appendPlan{}, retirementWriteError{code: retirementWriteErrTransactionOrder, first64: maximum, second64: batch.retiredByTxn}
	}
	plan := appendPlan{pathLength: depth, pages: 1, oldRoot: childReference{maximum: rawRetirementMaximum(&path[0]), pageNumber: state.root, level: path[0].level}, mode: upsertAppend}
	if maximum == batch.retiredByTxn {
		plan.mode = upsertReplace
		plan.oldBatch, status = leaf.batchStatus(leaf.len() - 1)
		if status.failed() {
			return appendPlan{}, retirementPageProblem(status)
		}
		if problem := replacements.requireAdditional(countCommittedTreeFrames(path[:depth])); problem.failed() {
			return appendPlan{}, problem
		}
		if problem := requirePrivateReleases(releases, countPrivateTreeFrames(path[:depth])); problem.failed() {
			return appendPlan{}, problem
		}
		for index := 0; index < depth; index++ {
			if problem := retireTreeFrame(&path[index], replacements, releases, roles); problem.failed() {
				return appendPlan{}, problem
			}
		}
		plan.pages = depth
		return plan, retirementWriteError{}
	}
	carry := leaf.len() == retirementLeafCapacity
	if !carry {
		if problem := retireTreeFrame(leafFrame, replacements, releases, roles); problem.failed() {
			return appendPlan{}, problem
		}
	}
	for index := depth - 2; index >= 0; index-- {
		branch, status := openRetirementBranchStatus(path[index].page[:], path[index].decodeTxn, arena.pendingPageCount)
		if status.failed() {
			return appendPlan{}, retirementPageProblem(status)
		}
		plan.pages++
		if plan.pages <= 0 {
			return appendPlan{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
		}
		if carry && branch.len() == retirementBranchCapacity {
			continue
		}
		carry = false
		if problem := retireTreeFrame(&path[index], replacements, releases, roles); problem.failed() {
			return appendPlan{}, problem
		}
	}
	if carry {
		if plan.oldRoot.level == MaxTreeLevel {
			return appendPlan{}, retirementWriteError{code: retirementWriteErrTreeDepthExceeded}
		}
		plan.pages++
	}
	return plan, retirementWriteError{}
}

func countCommittedTreeFrames(path []retirementPathFrame) int {
	count := 0
	for index := range path {
		if !path[index].private {
			count++
		}
	}
	return count
}
func countPrivateTreeFrames(path []retirementPathFrame) int {
	count := 0
	for index := range path {
		if path[index].private {
			count++
		}
	}
	return count
}
func requirePrivateReleases(releases *privateReleaseBuffer, additional int) retirementWriteError {
	required, ok := checkedIntAdd(releases.length, additional)
	if !ok {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	if required > len(releases.pageNumbers) {
		return retirementWriteError{code: retirementWriteErrPrivateReleaseBufferTooSmall, required: required, actual: len(releases.pageNumbers)}
	}
	return retirementWriteError{}
}

func rawRetirementMaximum(frame *retirementPathFrame) uint64 {
	count := int(binary.LittleEndian.Uint16(frame.page[16:18]))
	size := retirementLeafRecordSize
	if frame.level != 0 {
		size = retirementBranchEntrySize
	}
	at := int(PageHeaderSize) + (count-1)*size
	if frame.level == 0 {
		return binary.LittleEndian.Uint64(frame.page[at+8 : at+16])
	}
	return binary.LittleEndian.Uint64(frame.page[at : at+8])
}

func rawRetirementBatch(page *[PageSize]byte, index int) retirementBatch {
	at := int(PageHeaderSize) + index*retirementLeafRecordSize
	return retirementBatch{retiredByTxn: binary.LittleEndian.Uint64(page[at+8 : at+16]), pageCount: binary.LittleEndian.Uint64(page[at+16 : at+24]), pageListBlobRoot: binary.LittleEndian.Uint32(page[at+24 : at+28])}
}

func rawRetirementBranchEntry(page *[PageSize]byte, index int) childReference {
	at := int(PageHeaderSize) + index*retirementBranchEntrySize
	return childReference{maximum: binary.LittleEndian.Uint64(page[at : at+8]), pageNumber: binary.LittleEndian.Uint32(page[at+8 : at+12])}
}

func applyRetirementUpsert(arena *privatePageArena, state retirementTreeState, batch retirementBatch, path []retirementPathFrame, plan appendPlan, checkpoint arenaCheckpoint) (retirementTreeEditResult, retirementWriteError) {
	if state.root == 0 {
		pageNumber := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
		var page [PageSize]byte
		encodeRetirementLeafPage(&page, arena.bornTxn, 1, func(index int) retirementBatch { return batch })
		if problem := arena.writePage(pageNumber, &page); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		return retirementTreeEditResult{root: pageNumber, batchCount: 1, privatePages: 1}, retirementWriteError{}
	}
	leafFrame := &path[plan.pathLength-1]
	oldCount := int(binary.LittleEndian.Uint16(leafFrame.page[16:18]))
	pageNumber := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
	var page [PageSize]byte
	carry := false
	switch {
	case plan.mode == upsertReplace:
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount, func(index int) retirementBatch {
			if index+1 == oldCount {
				return batch
			}
			return rawRetirementBatch(&leafFrame.page, index)
		})
	case oldCount < retirementLeafCapacity:
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount+1, func(index int) retirementBatch {
			if index == oldCount {
				return batch
			}
			return rawRetirementBatch(&leafFrame.page, index)
		})
	default:
		encodeRetirementLeafPage(&page, arena.bornTxn, 1, func(index int) retirementBatch { return batch })
		carry = true
	}
	if problem := arena.writePage(pageNumber, &page); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	current := childReference{maximum: batch.retiredByTxn, pageNumber: pageNumber}
	for depth := plan.pathLength - 2; depth >= 0; depth-- {
		frame := &path[depth]
		count := int(binary.LittleEndian.Uint16(frame.page[16:18]))
		destination := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
		if carry && count == retirementBranchCapacity {
			encodeRetirementBranchPage(&page, arena.bornTxn, frame.level, 1, func(index int) childReference { return current })
		} else {
			appendChild := carry
			outputCount := count
			if appendChild {
				outputCount++
			}
			encodeRetirementBranchPage(&page, arena.bornTxn, frame.level, outputCount, func(index int) childReference {
				if (appendChild && index == count) || (!appendChild && index+1 == count) {
					return current
				}
				child := rawRetirementBranchEntry(&frame.page, index)
				child.level = frame.level - 1
				return child
			})
			carry = false
		}
		if problem := arena.writePage(destination, &page); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		current = childReference{maximum: current.maximum, pageNumber: destination, level: frame.level}
	}
	if carry {
		destination := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
		encodeRetirementBranchPage(&page, arena.bornTxn, plan.oldRoot.level+1, 2, func(index int) childReference {
			if index == 0 {
				return plan.oldRoot
			}
			return current
		})
		if problem := arena.writePage(destination, &page); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		current = childReference{maximum: current.maximum, pageNumber: destination, level: plan.oldRoot.level + 1}
	}
	batchCount := state.batchCount
	if plan.mode == upsertAppend {
		batchCount++
	}
	return retirementTreeEditResult{root: current.pageNumber, batchCount: batchCount, privatePages: plan.pages}, retirementWriteError{}
}

func encodeRetirementLeafPage(page *[PageSize]byte, bornTxn uint64, count int, batchAt func(int) retirementBatch) {
	*page = [PageSize]byte{}
	encodePageHeaderNoFail(page, PageTypeRetirementLeaf, bornTxn, uint16(count), 0, uint16(int(PageHeaderSize)+count*retirementLeafRecordSize), 0)
	for index := 0; index < count; index++ {
		at := int(PageHeaderSize) + index*retirementLeafRecordSize
		batch := batchAt(index)
		binary.LittleEndian.PutUint64(page[at+8:at+16], batch.retiredByTxn)
		binary.LittleEndian.PutUint64(page[at+16:at+24], batch.pageCount)
		binary.LittleEndian.PutUint32(page[at+24:at+28], batch.pageListBlobRoot)
	}
	sealPageNoFail(page)
}

func encodeRetirementBranchPage(page *[PageSize]byte, bornTxn uint64, level uint16, count int, childAt func(int) childReference) {
	*page = [PageSize]byte{}
	encodePageHeaderNoFail(page, PageTypeRetirementBranch, bornTxn, uint16(count), level, uint16(int(PageHeaderSize)+count*retirementBranchEntrySize), 0)
	for index := 0; index < count; index++ {
		at := int(PageHeaderSize) + index*retirementBranchEntrySize
		child := childAt(index)
		binary.LittleEndian.PutUint64(page[at:at+8], child.maximum)
		binary.LittleEndian.PutUint32(page[at+8:at+12], child.pageNumber)
	}
	sealPageNoFail(page)
}

func upsertNewestRetirement(source committedPageSource, state retirementTreeState, token *retirementBlobToken, path []retirementPathFrame, blobScratch *retirementBlobScanScratch, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) (retirementTreeEditResult, retirementWriteError) {
	if status := source.checkAccessStatus(); status.failed() {
		problem := retirementSourceProblem(status)
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	if problem := token.valid(); problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	arena := token.arena
	if token.bornTxn != arena.bornTxn {
		actual := token.bornTxn
		problem := retirementWriteError{code: retirementWriteErrBlobTokenTransactionMismatch, first64: arena.bornTxn, second64: actual}
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	rootToken, rootProblem := arena.privateToken(token.root, privatePageRetirementBlob)
	if rootProblem.failed() || rootToken.generation != token.generation {
		generation := token.generation
		problem := retirementWriteError{code: retirementWriteErrBlobTokenGenerationMismatch, first64: generation}
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	batch := retirementBatch{retiredByTxn: token.bornTxn, pageCount: token.pageCount, pageListBlobRoot: token.root}
	result, problem := editRetirementUpsert(source, state, batch, token.generation, arena, path, blobScratch, replacements, releases, roles)
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	token.stabilize()
	return result, retirementWriteError{}
}

func editRetirementUpsert(source committedPageSource, state retirementTreeState, batch retirementBatch, tokenGeneration uint64, arena *privatePageArena, path []retirementPathFrame, blobScratch *retirementBlobScanScratch, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) (retirementTreeEditResult, retirementWriteError) {
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if batch.retiredByTxn != arena.bornTxn {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrBlobTokenTransactionMismatch, first64: arena.bornTxn, second64: batch.retiredByTxn}
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	roles.requireNewReplacements()
	replacementCheckpoint, releaseCheckpoint := replacements.checkpoint(), releases.checkpoint()
	rollback := func(problem retirementWriteError) (retirementTreeEditResult, retirementWriteError) {
		replacements.rollback(replacementCheckpoint)
		releases.rollback(releaseCheckpoint)
		return retirementTreeEditResult{}, problem
	}
	plan, problem := preflightRetirementUpsert(source, state, batch, arena, path, replacements, releases, roles)
	if problem.failed() {
		return rollback(problem)
	}
	if plan.mode == upsertReplace {
		if problem = scanRetirementBatchBlob(source, state, arena, plan.oldBatch, 0, false, listedPageMarkRequired, true, replacements, releases, roles, blobScratch); problem.failed() {
			return rollback(problem)
		}
	}
	if problem = scanRetirementBatchBlob(source, state, arena, batch, tokenGeneration, true, listedPageSatisfyRequired, false, replacements, releases, roles, blobScratch); problem.failed() {
		return rollback(problem)
	}
	if pageNumber, found := roles.firstUnsatisfiedRequired(); found {
		return rollback(retirementWriteError{code: retirementWriteErrRetirementListOmission, page: pageNumber})
	}
	if problem = arena.requirePages(plan.pages); problem.failed() {
		return rollback(problem)
	}
	if status := source.checkAccessStatus(); status.failed() {
		return rollback(retirementSourceProblem(status))
	}
	checkpoint, problem := arena.beginWithAllocationBatch(plan.pages)
	if problem.failed() {
		return rollback(problem)
	}
	result, problem := applyRetirementUpsert(arena, state, batch, path, plan, checkpoint)
	if problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		return rollback(problem)
	}
	result.committedReplacements = replacements.length - replacementCheckpoint
	if problem = arena.commit(checkpoint, releases.entriesFrom(releaseCheckpoint)); problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		return rollback(problem)
	}
	releases.rollback(releaseCheckpoint)
	return result, retirementWriteError{}
}

type deleteNodeOutcome struct {
	fullyRemoved bool
	maximum      uint64
	boundary     deleteBoundary
}

type deleteBoundary struct {
	base         childReference
	deepestDepth int
	partialLeaf  bool
}

type finalDeleteBoundary struct {
	base            childReference
	deepestDepth    int
	partialLeaf     bool
	newRootDepth    int
	hasNewRootDepth bool
}

type deletePlan struct {
	boundary    finalDeleteBoundary
	hasBoundary bool
	pages       int
}

type retirementDeleteScanner struct {
	source                 committedPageSource
	state                  retirementTreeState
	arena                  *privatePageArena
	path                   []retirementPathFrame
	replacements           *committedReplacementLedger
	releases               *privateReleaseBuffer
	roles                  *pageRoleIndex
	blobScratch            *retirementBlobScanScratch
	remaining, deleted     uint64
	previousRetiredTxn     uint64
	havePreviousRetiredTxn bool
}

func (s *retirementDeleteScanner) scanNode(pageNumber uint32, expectedLevel uint16, hasExpectedLevel bool, expectedMaximum uint64, hasExpectedMaximum bool, depth int) (deleteNodeOutcome, retirementWriteError) {
	if depth >= retirementWriterPathCapacity || depth >= len(s.path) {
		return deleteNodeOutcome{}, retirementWriteError{code: retirementWriteErrPathBufferTooSmall, required: depth + 1, actual: len(s.path)}
	}
	frame := &s.path[depth]
	*frame = retirementPathFrame{}
	if problem := readRetirementTreeFrame(s.source, s.state, s.arena, pageNumber, frame, s.roles); problem.failed() {
		return deleteNodeOutcome{}, problem
	}
	header, headerProblem := decodePageHeaderNoAlloc(frame.page[:], frame.decodeTxn)
	if headerProblem.code != 0 {
		return deleteNodeOutcome{}, retirementHeaderProblem(headerProblem)
	}
	if hasExpectedLevel && header.Level != expectedLevel {
		return deleteNodeOutcome{}, retirementWriteError{code: retirementWriteErrChildLevel, first64: uint64(expectedLevel), second64: uint64(header.Level)}
	}
	switch header.PageType {
	case PageTypeRetirementLeaf:
		leaf, status := openRetirementLeafStatus(frame.page[:], frame.decodeTxn, s.arena.pendingPageCount)
		if status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if status = leaf.verifyCRCStatus(); status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if problem := validateRetirementLeafBlobRoots(s.source, frame, leaf, s.state, s.arena, s.roles); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		maximum, status := leaf.maximumKeyStatus()
		if status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if problem := requireMaximum(expectedMaximum, hasExpectedMaximum, maximum); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		if problem := retireTreeFrame(frame, s.replacements, s.releases, s.roles); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		deleteHere := s.remaining
		if deleteHere > uint64(leaf.len()) {
			deleteHere = uint64(leaf.len())
		}
		inspect := deleteHere
		if deleteHere < uint64(leaf.len()) {
			inspect++
		}
		for index := uint64(0); index < inspect; index++ {
			batch, st := leaf.batchStatus(int(index))
			if st.failed() {
				return deleteNodeOutcome{}, retirementPageProblem(st)
			}
			if s.havePreviousRetiredTxn && batch.retiredByTxn <= s.previousRetiredTxn {
				return deleteNodeOutcome{}, retirementWriteError{code: retirementWriteErrRetirementTreeOrder, first64: s.previousRetiredTxn, second64: batch.retiredByTxn}
			}
			s.previousRetiredTxn, s.havePreviousRetiredTxn = batch.retiredByTxn, true
		}
		for index := uint64(0); index < deleteHere; index++ {
			batch, _ := leaf.batchStatus(int(index))
			if problem := scanRetirementBatchBlob(s.source, s.state, s.arena, batch, 0, false, listedPageRegister, true, s.replacements, s.releases, s.roles, s.blobScratch); problem.failed() {
				return deleteNodeOutcome{}, problem
			}
		}
		s.remaining -= deleteHere
		s.deleted += deleteHere
		if deleteHere == uint64(leaf.len()) {
			return deleteNodeOutcome{fullyRemoved: true, maximum: maximum}, retirementWriteError{}
		}
		frame.keepFrom = uint16(deleteHere)
		return deleteNodeOutcome{boundary: deleteBoundary{base: childReference{maximum: maximum}, deepestDepth: depth, partialLeaf: true}}, retirementWriteError{}
	case PageTypeRetirementBranch:
		branch, status := openRetirementBranchStatus(frame.page[:], frame.decodeTxn, s.arena.pendingPageCount)
		if status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if status = branch.verifyCRCStatus(); status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if problem := validateRetirementBranchChildren(s.source, frame, branch, s.state, s.arena, s.roles); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		maximum, status := branch.maximumKeyStatus()
		if status.failed() {
			return deleteNodeOutcome{}, retirementPageProblem(status)
		}
		if problem := requireMaximum(expectedMaximum, hasExpectedMaximum, maximum); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		if problem := retireTreeFrame(frame, s.replacements, s.releases, s.roles); problem.failed() {
			return deleteNodeOutcome{}, problem
		}
		for index := 0; index < branch.len(); index++ {
			entry, st := branch.entryStatus(index)
			if st.failed() {
				return deleteNodeOutcome{}, retirementPageProblem(st)
			}
			if s.remaining == 0 {
				frame.keepFrom = uint16(index)
				return deleteNodeOutcome{boundary: deleteBoundary{base: childReference{maximum: entry.maxRetiredByTxn, pageNumber: entry.childPage, level: branch.level - 1}, deepestDepth: depth}}, retirementWriteError{}
			}
			outcome, problem := s.scanNode(entry.childPage, branch.level-1, true, entry.maxRetiredByTxn, true, depth+1)
			if problem.failed() {
				return deleteNodeOutcome{}, problem
			}
			if outcome.fullyRemoved {
				if problem = requireMaximum(entry.maxRetiredByTxn, true, outcome.maximum); problem.failed() {
					return deleteNodeOutcome{}, problem
				}
				continue
			}
			frame.keepFrom = uint16(index)
			return outcome, retirementWriteError{}
		}
		return deleteNodeOutcome{fullyRemoved: true, maximum: maximum}, retirementWriteError{}
	default:
		code := retirementWriteErrChildType
		if depth == 0 {
			code = retirementWriteErrRootType
		}
		return deleteNodeOutcome{}, retirementWriteError{code: code, pageType: header.PageType}
	}
}

func (s *retirementDeleteScanner) finishPlan(outcome deleteNodeOutcome, deleteCount uint64) (deletePlan, retirementWriteError) {
	if outcome.fullyRemoved {
		if deleteCount != s.state.batchCount {
			return deletePlan{}, retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: s.state.batchCount, second64: deleteCount}
		}
		return deletePlan{}, retirementWriteError{}
	}
	boundary := outcome.boundary
	if deleteCount == s.state.batchCount {
		return deletePlan{}, retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: s.state.batchCount, second64: deleteCount + 1}
	}
	newRootDepth, hasNewRootDepth := 0, false
	for depth := 0; depth <= boundary.deepestDepth; depth++ {
		frame := &s.path[depth]
		if frame.level == 0 {
			continue
		}
		count := int(binary.LittleEndian.Uint16(frame.page[16:18]))
		retained := count - int(frame.keepFrom)
		if retained >= 2 {
			newRootDepth, hasNewRootDepth = depth, true
			break
		}
	}
	if !hasNewRootDepth && !boundary.partialLeaf {
		var problem retirementWriteError
		boundary.base, problem = s.collapsePromotedRoot(boundary.base)
		if problem.failed() {
			return deletePlan{}, problem
		}
	}
	pages := 0
	if boundary.partialLeaf {
		pages = 1
	}
	if hasNewRootDepth {
		for depth := newRootDepth; depth <= boundary.deepestDepth; depth++ {
			if s.path[depth].level > 0 {
				pages++
			}
		}
	}
	return deletePlan{hasBoundary: true, pages: pages, boundary: finalDeleteBoundary{base: boundary.base, deepestDepth: boundary.deepestDepth, partialLeaf: boundary.partialLeaf, newRootDepth: newRootDepth, hasNewRootDepth: hasNewRootDepth}}, retirementWriteError{}
}

func (s *retirementDeleteScanner) collapsePromotedRoot(child childReference) (childReference, retirementWriteError) {
	for child.level > 0 {
		frame := &s.path[0]
		*frame = retirementPathFrame{}
		if problem := readRetirementTreeFrame(s.source, s.state, s.arena, child.pageNumber, frame, s.roles); problem.failed() {
			return childReference{}, problem
		}
		branch, status := openRetirementBranchStatus(frame.page[:], frame.decodeTxn, s.arena.pendingPageCount)
		if status.failed() {
			return childReference{}, retirementPageProblem(status)
		}
		if status = branch.verifyCRCStatus(); status.failed() {
			return childReference{}, retirementPageProblem(status)
		}
		if problem := validateRetirementBranchChildren(s.source, frame, branch, s.state, s.arena, s.roles); problem.failed() {
			return childReference{}, problem
		}
		if branch.level != child.level {
			return childReference{}, retirementWriteError{code: retirementWriteErrChildLevel, first64: uint64(child.level), second64: uint64(branch.level)}
		}
		maximum, status := branch.maximumKeyStatus()
		if status.failed() {
			return childReference{}, retirementPageProblem(status)
		}
		if problem := requireMaximum(child.maximum, true, maximum); problem.failed() {
			return childReference{}, problem
		}
		if branch.len() != 1 {
			break
		}
		if problem := retireTreeFrame(frame, s.replacements, s.releases, s.roles); problem.failed() {
			return childReference{}, problem
		}
		entry, status := branch.entryStatus(0)
		if status.failed() {
			return childReference{}, retirementPageProblem(status)
		}
		child = childReference{maximum: entry.maxRetiredByTxn, pageNumber: entry.childPage, level: branch.level - 1}
	}
	return child, retirementWriteError{}
}

func applyRetirementDelete(arena *privatePageArena, path []retirementPathFrame, plan deletePlan, checkpoint arenaCheckpoint) (uint32, retirementWriteError) {
	if !plan.hasBoundary {
		return 0, retirementWriteError{}
	}
	boundary := plan.boundary
	current := boundary.base
	var page [PageSize]byte
	if boundary.partialLeaf {
		frame := &path[boundary.deepestDepth]
		oldCount := int(binary.LittleEndian.Uint16(frame.page[16:18]))
		keepFrom := int(frame.keepFrom)
		destination := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount-keepFrom, func(index int) retirementBatch { return rawRetirementBatch(&frame.page, keepFrom+index) })
		if problem := arena.writePage(destination, &page); problem.failed() {
			return 0, problem
		}
		current = childReference{maximum: rawRetirementMaximum(frame), pageNumber: destination}
	}
	if boundary.hasNewRootDepth {
		for depth := boundary.deepestDepth; depth >= boundary.newRootDepth; depth-- {
			frame := &path[depth]
			if frame.level == 0 {
				continue
			}
			oldCount := int(binary.LittleEndian.Uint16(frame.page[16:18]))
			keepFrom := int(frame.keepFrom)
			destination := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
			encodeRetirementBranchPage(&page, arena.bornTxn, frame.level, oldCount-keepFrom, func(index int) childReference {
				if index == 0 {
					return current
				}
				child := rawRetirementBranchEntry(&frame.page, keepFrom+index)
				child.level = frame.level - 1
				return child
			})
			if problem := arena.writePage(destination, &page); problem.failed() {
				return 0, problem
			}
			current = childReference{maximum: rawRetirementMaximum(frame), pageNumber: destination, level: frame.level}
		}
	}
	return current.pageNumber, retirementWriteError{}
}

func deleteOldestRetirementPrefix(source committedPageSource, state retirementTreeState, deleteCount uint64, arena *privatePageArena, path []retirementPathFrame, blobScratch *retirementBlobScanScratch, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) (retirementTreeEditResult, retirementWriteError) {
	if status := source.checkAccessStatus(); status.failed() {
		return retirementTreeEditResult{}, retirementSourceProblem(status)
	}
	if arena.activeTokenEpoch != 0 {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrBlobTokenStale}
	}
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if deleteCount > state.batchCount {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrDeleteCountOutOfRange, first64: deleteCount, second64: state.batchCount}
	}
	if deleteCount == 0 {
		return retirementTreeEditResult{root: state.root, batchCount: state.batchCount}, retirementWriteError{}
	}
	replacementCheckpoint, releaseCheckpoint := replacements.checkpoint(), releases.checkpoint()
	rollback := func(problem retirementWriteError) (retirementTreeEditResult, retirementWriteError) {
		replacements.rollback(replacementCheckpoint)
		releases.rollback(releaseCheckpoint)
		return retirementTreeEditResult{}, problem
	}
	scanner := retirementDeleteScanner{source: source, state: state, arena: arena, path: path, replacements: replacements, releases: releases, roles: roles, blobScratch: blobScratch, remaining: deleteCount}
	if status := source.checkAccessStatus(); status.failed() {
		return rollback(retirementSourceProblem(status))
	}
	outcome, problem := scanner.scanNode(state.root, 0, false, 0, false, 0)
	if problem.failed() {
		return rollback(problem)
	}
	if scanner.remaining != 0 || scanner.deleted != deleteCount {
		return rollback(retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: deleteCount, second64: scanner.deleted})
	}
	plan, problem := scanner.finishPlan(outcome, deleteCount)
	if problem.failed() {
		return rollback(problem)
	}
	if problem = arena.requirePages(plan.pages); problem.failed() {
		return rollback(problem)
	}
	if status := source.checkAccessStatus(); status.failed() {
		return rollback(retirementSourceProblem(status))
	}
	checkpoint, problem := arena.beginWithAllocationBatch(plan.pages)
	if problem.failed() {
		return rollback(problem)
	}
	root, problem := applyRetirementDelete(arena, path, plan, checkpoint)
	if problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		return rollback(problem)
	}
	committedReplacements := replacements.length - replacementCheckpoint
	if problem = arena.commit(checkpoint, releases.entriesFrom(releaseCheckpoint)); problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		return rollback(problem)
	}
	releases.rollback(releaseCheckpoint)
	return retirementTreeEditResult{root: root, batchCount: state.batchCount - deleteCount, privatePages: plan.pages, committedReplacements: committedReplacements}, retirementWriteError{}
}

func deleteOldestAndUpsertNewestRetirement(source committedPageSource, state retirementTreeState, deleteCount uint64, token *retirementBlobToken, deletePath, upsertPath []retirementPathFrame, blobScratch *retirementBlobScanScratch, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex) (retirementTreeEditResult, retirementWriteError) {
	if status := source.checkAccessStatus(); status.failed() {
		problem := retirementSourceProblem(status)
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	if problem := token.valid(); problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	arena := token.arena
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	if token.bornTxn != arena.bornTxn {
		expected, actual := arena.bornTxn, token.bornTxn
		problem := retirementWriteError{code: retirementWriteErrBlobTokenTransactionMismatch, first64: expected, second64: actual}
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	rootToken, rootProblem := arena.privateToken(token.root, privatePageRetirementBlob)
	if rootProblem.failed() || rootToken.generation != token.generation {
		expected := token.generation
		problem := retirementWriteError{code: retirementWriteErrBlobTokenGenerationMismatch, first64: expected}
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	roles.requireNewReplacements()
	if deleteCount > state.batchCount {
		problem := retirementWriteError{code: retirementWriteErrDeleteCountOutOfRange, first64: deleteCount, second64: state.batchCount}
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	replacementCheckpoint, releaseCheckpoint := replacements.checkpoint(), releases.checkpoint()
	checkpoint, problem := arena.beginWithAllocationBatch(0)
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	result, problem := applyCombinedRetirementEdit(source, state, deleteCount, token, deletePath, upsertPath, blobScratch, replacements, releases, roles, checkpoint, replacementCheckpoint)
	if problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		replacements.rollback(replacementCheckpoint)
		releases.rollback(releaseCheckpoint)
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	if problem = arena.commit(checkpoint, releases.entriesFrom(releaseCheckpoint)); problem.failed() {
		problem = retirementWithCleanup(problem, arena.rollback(checkpoint))
		replacements.rollback(replacementCheckpoint)
		releases.rollback(releaseCheckpoint)
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	releases.rollback(releaseCheckpoint)
	token.stabilize()
	return result, retirementWriteError{}
}

func applyCombinedRetirementEdit(source committedPageSource, state retirementTreeState, deleteCount uint64, token *retirementBlobToken, deletePath, upsertPath []retirementPathFrame, blobScratch *retirementBlobScanScratch, replacements *committedReplacementLedger, releases *privateReleaseBuffer, roles *pageRoleIndex, checkpoint arenaCheckpoint, replacementCheckpoint int) (retirementTreeEditResult, retirementWriteError) {
	arena := token.arena
	intermediate := retirementTreeEditResult{root: state.root, batchCount: state.batchCount}
	if deleteCount != 0 {
		scanner := retirementDeleteScanner{source: source, state: state, arena: arena, path: deletePath, replacements: replacements, releases: releases, roles: roles, blobScratch: blobScratch, remaining: deleteCount}
		if status := source.checkAccessStatus(); status.failed() {
			return retirementTreeEditResult{}, retirementSourceProblem(status)
		}
		outcome, problem := scanner.scanNode(state.root, 0, false, 0, false, 0)
		if problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		if scanner.remaining != 0 || scanner.deleted != deleteCount {
			return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: deleteCount, second64: scanner.deleted}
		}
		plan, problem := scanner.finishPlan(outcome, deleteCount)
		if problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		if problem = arena.requirePages(plan.pages); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		if status := source.checkAccessStatus(); status.failed() {
			return retirementTreeEditResult{}, retirementSourceProblem(status)
		}
		if problem = arena.preflightAdditionalAllocations(checkpoint, plan.pages); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
		root, applyProblem := applyRetirementDelete(arena, deletePath, plan, checkpoint)
		if applyProblem.failed() {
			return retirementTreeEditResult{}, applyProblem
		}
		intermediate = retirementTreeEditResult{root: root, batchCount: state.batchCount - deleteCount, privatePages: plan.pages}
		if problem = roles.advanceReferenceEpoch(); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
	}
	if status := source.checkAccessStatus(); status.failed() {
		return retirementTreeEditResult{}, retirementSourceProblem(status)
	}
	intermediateState := state
	intermediateState.root, intermediateState.batchCount = intermediate.root, intermediate.batchCount
	batch := retirementBatch{retiredByTxn: token.bornTxn, pageCount: token.pageCount, pageListBlobRoot: token.root}
	plan, problem := preflightRetirementUpsert(source, intermediateState, batch, arena, upsertPath, replacements, releases, roles)
	if problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if plan.mode == upsertReplace {
		if problem = scanRetirementBatchBlob(source, intermediateState, arena, plan.oldBatch, 0, false, listedPageMarkRequired, true, replacements, releases, roles, blobScratch); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
	}
	if problem = scanRetirementBatchBlob(source, intermediateState, arena, batch, token.generation, true, listedPageSatisfyRequired, false, replacements, releases, roles, blobScratch); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if pageNumber, found := roles.firstUnsatisfiedRequired(); found {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrRetirementListOmission, page: pageNumber}
	}
	if problem = arena.requirePages(plan.pages); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if status := source.checkAccessStatus(); status.failed() {
		return retirementTreeEditResult{}, retirementSourceProblem(status)
	}
	if problem = arena.preflightAdditionalAllocations(checkpoint, plan.pages); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	upsert, applyProblem := applyRetirementUpsert(arena, intermediateState, batch, upsertPath, plan, checkpoint)
	if applyProblem.failed() {
		return retirementTreeEditResult{}, applyProblem
	}
	upsert.privatePages += intermediate.privatePages
	upsert.committedReplacements = replacements.length - replacementCheckpoint
	return upsert, retirementWriteError{}
}
