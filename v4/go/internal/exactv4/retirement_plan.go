package exactv4

// retirementEditBinding identifies the mutable authority which changed while
// a source callback was running or between planning and apply.
type retirementEditBinding uint8

const (
	retirementEditBindingSource retirementEditBinding = iota + 1
	retirementEditBindingArena
	retirementEditBindingPool
	retirementEditBindingScope
	retirementEditBindingDeletePath
	retirementEditBindingUpsertPath
	retirementEditBindingReplacementLedger
	retirementEditBindingReleaseLedger
	retirementEditBindingRoles
	retirementEditBindingBlobScratch
	retirementEditBindingBlobToken
	retirementEditBindingDestination
	retirementEditBindingRelease
	retirementEditBindingHeadroom
)

const retirementHashOffset uint64 = 1469598103934665603
const retirementHashPrime uint64 = 1099511628211

func retirementHashUint64(hash, value uint64) uint64 {
	for shift := uint(0); shift < 64; shift += 8 {
		hash ^= uint64(byte(value >> shift))
		hash *= retirementHashPrime
	}
	return hash
}

func retirementHashBool(hash uint64, value bool) uint64 {
	if value {
		return retirementHashUint64(hash, 1)
	}
	return retirementHashUint64(hash, 0)
}

func retirementProvenanceHash(
	hash uint64,
	provenance privateWriterDraftPageProvenance,
) uint64 {
	hash = retirementHashUint64(hash, provenance.workUnit)
	hash = retirementHashUint64(hash, provenance.scopeID)
	hash = retirementHashUint64(hash, uint64(provenance.scopeAnchor+1))
	hash = retirementHashUint64(hash, uint64(provenance.slot+1))
	hash = retirementHashUint64(hash, uint64(provenance.pageNumber))
	hash = retirementHashUint64(hash, provenance.bindingEpoch)
	hash = retirementHashUint64(hash, uint64(provenance.owner))
	hash = retirementHashUint64(hash, uint64(provenance.origin))
	return retirementHashUint64(hash, provenance.generation)
}

func retirementHashPage(hash uint64, page *[PageSize]byte) uint64 {
	for _, value := range page {
		hash ^= uint64(value)
		hash *= retirementHashPrime
	}
	return hash
}

func retirementPathHash(path []retirementPathFrame, excluded *[PageSize]byte) uint64 {
	hash := retirementHashUint64(retirementHashOffset, uint64(len(path)))
	hash = retirementHashUint64(hash, uint64(cap(path)))
	for index := range path {
		frame := &path[index]
		hash = retirementHashUint64(hash, uint64(frame.pageNumber))
		hash = retirementHashUint64(hash, uint64(frame.level))
		hash = retirementHashUint64(hash, frame.decodeTxn)
		hash = retirementHashBool(hash, frame.private)
		hash = retirementHashUint64(hash, uint64(frame.residence.kind))
		hash = retirementHashUint64(hash, frame.residence.generation)
		hash = retirementProvenanceHash(hash, frame.residence.prior)
		hash = retirementHashUint64(hash, uint64(frame.keepFrom))
		hash = retirementHashUint64(hash, uint64(frame.destinationSlot+1))
		hash = retirementHashUint64(hash, frame.destinationEpoch)
		hash = retirementHashUint64(hash, uint64(frame.destinationAuthorization))
		hash = retirementHashUint64(hash, uint64(frame.destinationOwner))
		hash = retirementHashUint64(hash, frame.scratchEpoch)
		if excluded != &frame.page {
			hash = retirementHashPage(hash, &frame.page)
		}
	}
	return hash
}

func retirementReplacementHash(ledger *committedReplacementLedger) uint64 {
	if ledger == nil {
		return 0
	}
	hash := retirementHashUint64(retirementHashOffset, uint64(len(ledger.entries)))
	hash = retirementHashUint64(hash, uint64(cap(ledger.entries)))
	hash = retirementHashUint64(hash, uint64(ledger.length))
	for _, entry := range ledger.entries {
		hash = retirementHashUint64(hash, uint64(entry.pageNumber))
		hash = retirementHashUint64(hash, uint64(entry.origin))
	}
	return hash
}

func retirementReleaseHash(releases *privateReleaseBuffer) uint64 {
	if releases == nil {
		return 0
	}
	hash := retirementHashUint64(retirementHashOffset, uint64(len(releases.pageNumbers)))
	hash = retirementHashUint64(hash, uint64(cap(releases.pageNumbers)))
	hash = retirementHashUint64(hash, uint64(releases.length))
	for _, pageNumber := range releases.pageNumbers {
		hash = retirementHashUint64(hash, uint64(pageNumber))
	}
	return hash
}

func retirementRolesHash(roles *pageRoleIndex) uint64 {
	if roles == nil {
		return 0
	}
	hash := retirementHashUint64(retirementHashOffset, uint64(len(roles.slots)))
	hash = retirementHashUint64(hash, uint64(cap(roles.slots)))
	hash = retirementHashUint64(hash, uint64(roles.used))
	hash = retirementHashUint64(hash, uint64(roles.root+1))
	hash = retirementHashUint64(hash, uint64(roles.referenceEpoch))
	hash = retirementHashBool(hash, roles.replacementsMustBeListed)
	hash = retirementHashUint64(hash, roles.planSequence)
	hash = retirementHashUint64(hash, roles.activePlan)
	for index := range roles.slots {
		slot := &roles.slots[index]
		hash = retirementHashUint64(hash, uint64(slot.pageNumber))
		hash = retirementHashUint64(hash, uint64(slot.roles))
		hash = retirementHashUint64(hash, uint64(slot.referenceEpoch))
		hash = retirementHashUint64(hash, uint64(slot.selectedEpoch))
		hash = retirementHashUint64(hash, uint64(slot.preparedSlot+1))
		hash = retirementHashUint64(hash, slot.preparedEpoch)
		hash = retirementHashUint64(hash, uint64(slot.preparedAuth))
		hash = retirementHashUint64(hash, uint64(slot.preparedOwner))
		hash = retirementHashUint64(hash, uint64(slot.preparedOrigin))
		hash = retirementHashUint64(hash, slot.preparedTxn)
		hash = retirementHashUint64(hash, slot.preparedGen)
		hash = retirementHashBool(hash, slot.priorPrivate)
		hash = retirementProvenanceHash(hash, slot.prior)
		hash = retirementHashUint64(hash, uint64(slot.left+1))
		hash = retirementHashUint64(hash, uint64(slot.right+1))
		hash = retirementHashUint64(hash, uint64(slot.height))
		hash = retirementHashBool(hash, slot.occupied)
	}
	return hash
}

func retirementBlobScratchHash(scratch *retirementBlobScanScratch, excluded *[PageSize]byte) uint64 {
	if scratch == nil {
		return 0
	}
	hash := retirementHashUint64(retirementHashOffset, uint64(len(scratch.pages)))
	hash = retirementHashUint64(hash, uint64(cap(scratch.pages)))
	for index := range scratch.pages {
		if excluded != &scratch.pages[index].bytes {
			hash = retirementHashPage(hash, &scratch.pages[index].bytes)
		}
	}
	return hash
}

func retirementArenaHash(arena *privatePageArena) uint64 {
	if arena == nil {
		return 0
	}
	hash := retirementHashOffset
	hash = retirementHashUint64(hash, arena.scope.id)
	hash = retirementHashUint64(hash, arena.scope.poolEpoch)
	hash = retirementHashUint64(hash, uint64(arena.scope.anchor+1))
	hash = retirementHashBool(hash, arena.scoped)
	hash = retirementHashUint64(hash, arena.committedPageCount)
	hash = retirementHashUint64(hash, arena.pendingPageCount)
	hash = retirementHashUint64(hash, arena.bornTxn)
	hash = retirementHashUint64(hash, uint64(arena.allocationCursor+1))
	hash = retirementHashUint64(hash, arena.plannedFingerprint)
	hash = retirementHashUint64(hash, arena.appliedFingerprint)
	hash = retirementHashUint64(hash, uint64(arena.plannedDestinations))
	hash = retirementHashUint64(hash, uint64(arena.appliedDestinations))
	hash = retirementHashUint64(hash, arena.tokenEpoch)
	hash = retirementHashUint64(hash, arena.activeTokenEpoch)
	hash = retirementHashUint64(hash, arena.activeTokenGen)
	return hash
}

func retirementPoolHeaderHash(pool *privatePagePool) uint64 {
	hash := retirementHashOffset
	for _, value := range []uint64{
		pool.committedPageCount, pool.pendingPageCount, pool.pendingTxn, pool.epoch,
		pool.invalidationEpoch, pool.generation, pool.mutationEpoch, pool.abortMutationReserve,
		pool.checkpointSequence, pool.activeCheckpointID,
		pool.checkpointCleanup, uint64(pool.checkpointIndexHead + 1), uint64(pool.checkpointIndexCount),
		pool.operationSequence, pool.activeOperationID, pool.operationStartEpoch,
		retirementBoolUint64(pool.abortRequired), pool.scopeSequence,
		uint64(pool.activeScopes), uint64(pool.unscopedVacantHead + 1), uint64(pool.unscopedVacantTail + 1),
		uint64(pool.unscopedVacantCount), uint64(pool.indexRoot + 1), uint64(len(pool.slots)), uint64(cap(pool.slots)),
	} {
		hash = retirementHashUint64(hash, value)
	}
	return hash
}

func retirementBoolUint64(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func retirementPoolSlotHash(hash uint64, index int, slot *privatePagePoolSlot) uint64 {
	hash = retirementHashUint64(hash, uint64(index+1))
	for _, value := range []uint64{
		uint64(slot.pageNumber), uint64(slot.authorization), slot.scopeID,
		uint64(slot.scopeAnchorIndex + 1), uint64(slot.scopeVacantNext + 1), uint64(slot.scopeMemberNext + 1),
		uint64(slot.unscopedNext + 1), uint64(slot.unscopedPrevious + 1),
		uint64(slot.scopeRoot + 1), uint64(slot.scopeVacantHead + 1), uint64(slot.scopeMemberHead + 1),
		uint64(slot.scopeCapacity), uint64(slot.scopeBound), uint64(slot.state),
		uint64(slot.owner), uint64(slot.origin), slot.pendingTxn, slot.generation,
		slot.epoch, uint64(slot.committedOrigin), slot.checkpointID,
		uint64(slot.checkpointPageNumber), uint64(slot.checkpointAuthorization), slot.checkpointScopeID,
		uint64(slot.checkpointScopeAnchorIndex + 1), uint64(slot.checkpointScopeVacantNext + 1),
		uint64(slot.checkpointState), uint64(slot.checkpointOwner), uint64(slot.checkpointOrigin),
		slot.checkpointPendingTxn, slot.checkpointGeneration, uint64(slot.checkpointCommittedOrigin),
		uint64(slot.pendingReturnState), slot.indexCheckpointID, uint64(slot.indexCheckpointNext + 1),
		uint64(slot.checkpointIndexLeft + 1),
		uint64(slot.checkpointIndexRight + 1), uint64(int64(slot.checkpointIndexHeight) + 1),
		slot.checkpointIndexFree, slot.checkpointIndexInUse, uint64(slot.checkpointScopeLeft + 1),
		uint64(slot.checkpointScopeRight + 1), uint64(int64(slot.checkpointScopeHeight) + 1),
		slot.checkpointScopeFree, slot.checkpointScopeInUse, slot.scopeCheckpointID,
		uint64(slot.checkpointScopeRoot + 1), uint64(slot.checkpointScopeVacantHead + 1),
		uint64(slot.checkpointScopeBound), uint64(slot.indexLeft + 1), uint64(slot.indexRight + 1),
		uint64(int64(slot.indexHeight) + 1), slot.indexFree, slot.indexInUse,
		uint64(slot.scopeLeft + 1), uint64(slot.scopeRight + 1), uint64(int64(slot.scopeHeight) + 1),
		slot.scopeFree, slot.scopeInUse,
	} {
		hash = retirementHashUint64(hash, value)
	}
	for _, value := range []bool{
		slot.bound, slot.scopeAnchor, slot.inUse, slot.checkpointBound,
		slot.checkpointScopeAnchor, slot.checkpointInUse, slot.batchMarked,
	} {
		hash = retirementHashBool(hash, value)
	}
	return retirementHashPage(hash, &slot.bytes)
}

func retirementPoolHash(pool *privatePagePool) uint64 {
	if pool == nil {
		return 0
	}
	hash := retirementPoolHeaderHash(pool)
	for index := range pool.slots {
		hash = retirementPoolSlotHash(hash, index, &pool.slots[index])
	}
	return hash
}

func retirementPoolHashInScope(pool *privatePagePool, scope privatePageReservationScope) uint64 {
	if pool == nil {
		return 0
	}
	hash := retirementPoolHeaderHash(pool)
	hash = retirementHashUint64(hash, scope.poolEpoch)
	hash = retirementHashUint64(hash, scope.id)
	hash = retirementHashUint64(hash, scope.pendingTxn)
	hash = retirementHashUint64(hash, uint64(scope.anchor+1))
	member, capacity, problem := pool.scopeMemberStart(scope)
	if problem.failed() {
		return retirementHashUint64(hash, ^uint64(0))
	}
	visited := 0
	for member != privatePagePoolNoIndex {
		if visited >= capacity || member < 0 || member >= len(pool.slots) {
			return retirementHashUint64(hash, ^uint64(0)-1)
		}
		slot := &pool.slots[member]
		hash = retirementPoolSlotHash(hash, member, slot)
		member = slot.scopeMemberNext
		visited++
	}
	if visited != capacity {
		return retirementHashUint64(hash, ^uint64(0)-2)
	}
	hash = retirementHashUint64(hash, uint64(visited))
	return hash
}

func retirementTokenHash(token *retirementBlobToken) uint64 {
	if token == nil {
		return 0
	}
	hash := retirementHashOffset
	hash = retirementHashUint64(hash, uint64(token.root))
	hash = retirementHashUint64(hash, token.pageCount)
	hash = retirementHashUint64(hash, token.byteLength)
	hash = retirementHashUint64(hash, uint64(token.privatePages))
	hash = retirementHashUint64(hash, token.generation)
	hash = retirementHashUint64(hash, token.bornTxn)
	hash = retirementHashUint64(hash, token.epoch)
	hash = retirementHashUint64(hash, token.cleanupGeneration)
	hash = retirementHashUint64(hash, token.cleanupTokenEpoch)
	return hash
}

type retirementMutableSeal struct {
	arena, pool, deletePath, upsertPath uint64
	replacements, releases, roles       uint64
	blobScratch, token                  uint64
	poolSlots                           freeBitmapReservationSliceSeal[privatePagePoolSlot]
	arenaPointer                        *privatePageArena
	arenaPool                           *privatePagePool
	arenaScope                          privatePageReservationScope
	poolSelf                            *privatePagePool
	tokenArena                          *privatePageArena
	deletePathSlice                     freeBitmapReservationSliceSeal[retirementPathFrame]
	upsertPathSlice                     freeBitmapReservationSliceSeal[retirementPathFrame]
	replacementSlice                    freeBitmapReservationSliceSeal[committedPageReplacement]
	releaseSlice                        freeBitmapReservationSliceSeal[uint32]
	rolesSlice                          freeBitmapReservationSliceSeal[pageRoleIndexSlot]
	blobScratchSlice                    freeBitmapReservationSliceSeal[retirementBlobScanPage]
}

type guardedRetirementSource struct {
	source       committedPageSource
	arena        *privatePageArena
	deletePath   []retirementPathFrame
	upsertPath   []retirementPathFrame
	replacements *committedReplacementLedger
	releases     *privateReleaseBuffer
	roles        *pageRoleIndex
	blobScratch  *retirementBlobScanScratch
	token        *retirementBlobToken
	drift        retirementEditBinding

	originalArena        privatePageArena
	originalArenaPointer *privatePageArena
	originalReplacements committedReplacementLedger
	originalReleases     privateReleaseBuffer
	originalRoles        pageRoleIndex
	originalBlobPages    []retirementBlobScanPage
	originalToken        retirementBlobToken
}

func newGuardedRetirementSource(
	source committedPageSource,
	arena *privatePageArena,
	deletePath, upsertPath []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
	token *retirementBlobToken,
) guardedRetirementSource {
	guard := guardedRetirementSource{
		source: source, arena: arena, deletePath: deletePath, upsertPath: upsertPath,
		blobScratch: blobScratch, replacements: replacements, releases: releases, roles: roles, token: token,
	}
	if arena != nil {
		guard.originalArenaPointer = arena
		guard.originalArena = *arena
	}
	if replacements != nil {
		guard.originalReplacements = *replacements
	}
	if releases != nil {
		guard.originalReleases = *releases
	}
	if roles != nil {
		guard.originalRoles = *roles
	}
	if blobScratch != nil {
		guard.originalBlobPages = blobScratch.pages
	}
	if token != nil {
		guard.originalToken = *token
	}
	return guard
}

func (g *guardedRetirementSource) seal(excluded *[PageSize]byte) retirementMutableSeal {
	seal := retirementMutableSeal{arena: retirementArenaHash(g.arena), arenaPointer: g.arena}
	var scope privatePageReservationScope
	if g.arena != nil {
		seal.arenaPool = g.arena.pool
		seal.arenaScope = g.arena.scope
		scope = g.arena.scope
	}
	pool := seal.arenaPool
	if pool != nil {
		seal.pool = retirementPoolHashInScope(pool, scope)
		seal.poolSelf = pool.self
		seal.poolSlots = sealFreeBitmapReservationSlice(pool.slots)
	}
	seal.deletePath = retirementPathHash(g.deletePath, excluded)
	seal.upsertPath = retirementPathHash(g.upsertPath, excluded)
	seal.deletePathSlice = sealFreeBitmapReservationSlice(g.deletePath)
	seal.upsertPathSlice = sealFreeBitmapReservationSlice(g.upsertPath)
	seal.replacements = retirementReplacementHash(g.replacements)
	seal.releases = retirementReleaseHash(g.releases)
	seal.roles = retirementRolesHash(g.roles)
	seal.blobScratch = retirementBlobScratchHash(g.blobScratch, excluded)
	seal.token = retirementTokenHash(g.token)
	if g.replacements != nil {
		seal.replacementSlice = sealFreeBitmapReservationSlice(g.replacements.entries)
	}
	if g.releases != nil {
		seal.releaseSlice = sealFreeBitmapReservationSlice(g.releases.pageNumbers)
	}
	if g.roles != nil {
		seal.rolesSlice = sealFreeBitmapReservationSlice(g.roles.slots)
	}
	if g.blobScratch != nil {
		seal.blobScratchSlice = sealFreeBitmapReservationSlice(g.blobScratch.pages)
	}
	if g.token != nil {
		seal.tokenArena = g.token.arena
	}
	return seal
}

func retirementSealDifference(before, after retirementMutableSeal) retirementEditBinding {
	switch {
	case before.arena != after.arena || before.arenaPointer != after.arenaPointer ||
		before.arenaPool != after.arenaPool || before.arenaScope != after.arenaScope:
		return retirementEditBindingArena
	case before.pool != after.pool || before.poolSelf != after.poolSelf || before.poolSlots != after.poolSlots:
		return retirementEditBindingPool
	case before.deletePath != after.deletePath || before.deletePathSlice != after.deletePathSlice:
		return retirementEditBindingDeletePath
	case before.upsertPath != after.upsertPath || before.upsertPathSlice != after.upsertPathSlice:
		return retirementEditBindingUpsertPath
	case before.replacements != after.replacements || before.replacementSlice != after.replacementSlice:
		return retirementEditBindingReplacementLedger
	case before.releases != after.releases || before.releaseSlice != after.releaseSlice:
		return retirementEditBindingReleaseLedger
	case before.roles != after.roles || before.rolesSlice != after.rolesSlice:
		return retirementEditBindingRoles
	case before.blobScratch != after.blobScratch || before.blobScratchSlice != after.blobScratchSlice:
		return retirementEditBindingBlobScratch
	case before.token != after.token || before.tokenArena != after.tokenArena:
		return retirementEditBindingBlobToken
	default:
		return 0
	}
}

func (g *guardedRetirementSource) checkAccessStatus() pageSourceStatus {
	before := g.seal(nil)
	status := g.source.checkAccessStatus()
	after := g.seal(nil)
	if g.drift == 0 {
		g.drift = retirementSealDifference(before, after)
	}
	if g.drift != 0 {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	return status
}

func (g *guardedRetirementSource) readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	before := g.seal(destination)
	status := g.source.readPageStatus(pageNumber, destination)
	after := g.seal(destination)
	if g.drift == 0 {
		g.drift = retirementSealDifference(before, after)
	}
	if g.drift != 0 {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	return status
}

func (g *guardedRetirementSource) residence(
	pageNumber uint32,
) (privateWriterDraftPageResidence, privateWriterFixedPointError) {
	before := g.seal(nil)
	source, ok := g.source.(privateWriterResidenceSource)
	if !ok {
		return privateWriterDraftPageResidence{
			kind: privateWriterPageSelectedCommitted,
		}, privateWriterFixedPointError{}
	}
	residence, problem := source.residence(pageNumber)
	after := g.seal(nil)
	if g.drift == 0 {
		g.drift = retirementSealDifference(before, after)
	}
	if g.drift != 0 {
		return privateWriterDraftPageResidence{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, page: pageNumber,
		}
	}
	return residence, problem
}

func (g *guardedRetirementSource) restoreHeaders() {
	if g.originalArenaPointer != nil {
		*g.originalArenaPointer = g.originalArena
		g.arena = g.originalArenaPointer
	}
	if g.replacements != nil {
		*g.replacements = g.originalReplacements
	}
	if g.releases != nil {
		*g.releases = g.originalReleases
	}
	if g.roles != nil {
		*g.roles = g.originalRoles
	}
	if g.blobScratch != nil {
		g.blobScratch.pages = g.originalBlobPages
	}
	if g.token != nil {
		*g.token = g.originalToken
	}
}

func (g *guardedRetirementSource) checkedProblem(problem retirementWriteError) retirementWriteError {
	if g.drift == 0 {
		return problem
	}
	g.restoreHeaders()
	return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: g.drift}
}

type retirementPlannedDestination struct {
	slot          int
	pageNumber    uint32
	epoch         uint64
	authorization privatePageAuthorization
	owner         privatePageOwner
}

type retirementDestinationCursor struct{ selected int }

func (c *retirementDestinationCursor) take(arena *privatePageArena) (retirementPlannedDestination, retirementWriteError) {
	if problem := arena.requirePages(c.selected + 1); problem.failed() {
		return retirementPlannedDestination{}, problem
	}
	index, problem := arena.pagePool().availableSlotAtRankInScope(arena.scope, c.selected)
	if problem.failed() {
		return retirementPlannedDestination{}, retirementPoolError(problem, arena.committedPageCount, arena.pendingPageCount)
	}
	slot := &arena.pagePool().slots[index]
	if slot.state != privatePageAvailable || slot.owner != privatePageOwnerNone || slot.scopeID != arena.scope.id || slot.scopeAnchorIndex != arena.scope.anchor {
		return retirementPlannedDestination{}, retirementWriteError{code: retirementWriteErrPrivateBindingDrift, page: slot.pageNumber}
	}
	c.selected++
	return retirementPlannedDestination{
		slot: index, pageNumber: slot.pageNumber, epoch: slot.epoch,
		authorization: slot.authorization, owner: slot.owner,
	}, retirementWriteError{}
}

func retirementSourceDestination(frame *retirementPathFrame, arena *privatePageArena, cursor *retirementDestinationCursor) (retirementPlannedDestination, retirementWriteError) {
	if frame.destinationSlot != privatePagePoolNoIndex && frame.scratchEpoch != 0 {
		return retirementPlannedDestination{
			slot: frame.destinationSlot, pageNumber: frame.pageNumber, epoch: frame.destinationEpoch,
			authorization: frame.destinationAuthorization, owner: frame.destinationOwner,
		}, retirementWriteError{}
	}
	return cursor.take(arena)
}

func finishRetirementPlannedFrame(frame *retirementPathFrame, destination retirementPlannedDestination, level uint16, bornTxn, scratchEpoch uint64, page [PageSize]byte) {
	*frame = retirementPathFrame{
		pageNumber: destination.pageNumber, level: level, decodeTxn: bornTxn, private: true,
		residence:       pageResidence{kind: pageResidenceCurrentScopePrivate},
		page:            page,
		destinationSlot: destination.slot, destinationEpoch: destination.epoch,
		destinationAuthorization: destination.authorization, destinationOwner: destination.owner,
		scratchEpoch: scratchEpoch,
	}
}

type retirementScratchBinding struct {
	epoch        uint64
	length       int
	fingerprints [retirementWriterPathCapacity]uint64
}

func nextRetirementScratchEpoch(path []retirementPathFrame) (uint64, retirementWriteError) {
	if len(path) == 0 {
		return 1, retirementWriteError{}
	}
	next := path[0].scratchEpoch + 1
	if next == 0 {
		return 0, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	return next, retirementWriteError{}
}

func bindRetirementScratch(path []retirementPathFrame, length int, epoch uint64) retirementScratchBinding {
	binding := retirementScratchBinding{epoch: epoch, length: length}
	for index := 0; index < length; index++ {
		binding.fingerprints[index] = retirementHashPage(retirementHashOffset, &path[index].page)
	}
	return binding
}

func prepareRetirementUpsertPages(state retirementTreeState, batch retirementBatch, arena *privatePageArena, path []retirementPathFrame, plan appendPlan, cursor *retirementDestinationCursor, scratchEpoch uint64) (retirementTreeEditResult, retirementScratchBinding, retirementWriteError) {
	if plan.pages > len(path) || plan.pages > retirementWriterPathCapacity {
		return retirementTreeEditResult{}, retirementScratchBinding{}, retirementWriteError{code: retirementWriteErrPathBufferTooSmall, required: plan.pages, actual: len(path)}
	}
	if state.root == 0 {
		destination, problem := cursor.take(arena)
		if problem.failed() {
			return retirementTreeEditResult{}, retirementScratchBinding{}, problem
		}
		var page [PageSize]byte
		encodeRetirementLeafPage(&page, arena.bornTxn, 1, func(int) retirementBatch { return batch })
		finishRetirementPlannedFrame(&path[0], destination, 0, arena.bornTxn, scratchEpoch, page)
		return retirementTreeEditResult{root: destination.pageNumber, batchCount: 1, privatePages: 1}, bindRetirementScratch(path, 1, scratchEpoch), retirementWriteError{}
	}

	leafDepth := plan.pathLength - 1
	destination, problem := retirementSourceDestination(&path[leafDepth], arena, cursor)
	if problem.failed() {
		return retirementTreeEditResult{}, retirementScratchBinding{}, problem
	}
	oldCount := int(binaryLittleEndianUint16(path[leafDepth].page[16:18]))
	var page [PageSize]byte
	carry := false
	switch {
	case plan.mode == upsertReplace:
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount, func(index int) retirementBatch {
			if index+1 == oldCount {
				return batch
			}
			return rawRetirementBatch(&path[leafDepth].page, index)
		})
	case oldCount < retirementLeafCapacity:
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount+1, func(index int) retirementBatch {
			if index == oldCount {
				return batch
			}
			return rawRetirementBatch(&path[leafDepth].page, index)
		})
	default:
		encodeRetirementLeafPage(&page, arena.bornTxn, 1, func(int) retirementBatch { return batch })
		carry = true
	}
	finishRetirementPlannedFrame(&path[leafDepth], destination, 0, arena.bornTxn, scratchEpoch, page)
	current := childReference{maximum: batch.retiredByTxn, pageNumber: destination.pageNumber}
	for depth := leafDepth - 1; depth >= 0; depth-- {
		frame := &path[depth]
		count := int(binaryLittleEndianUint16(frame.page[16:18]))
		destination, problem = retirementSourceDestination(frame, arena, cursor)
		if problem.failed() {
			return retirementTreeEditResult{}, retirementScratchBinding{}, problem
		}
		level := frame.level
		if carry && count == retirementBranchCapacity {
			encodeRetirementBranchPage(&page, arena.bornTxn, level, 1, func(int) childReference { return current })
		} else {
			appendChild := carry
			outputCount := count
			if appendChild {
				outputCount++
			}
			encodeRetirementBranchPage(&page, arena.bornTxn, level, outputCount, func(index int) childReference {
				if appendChild && index == count || !appendChild && index+1 == count {
					return current
				}
				child := rawRetirementBranchEntry(&frame.page, index)
				child.level = level - 1
				return child
			})
			carry = false
		}
		finishRetirementPlannedFrame(frame, destination, level, arena.bornTxn, scratchEpoch, page)
		current = childReference{maximum: current.maximum, pageNumber: destination.pageNumber, level: level}
	}
	if carry {
		destination, problem = cursor.take(arena)
		if problem.failed() {
			return retirementTreeEditResult{}, retirementScratchBinding{}, problem
		}
		level := plan.oldRoot.level + 1
		encodeRetirementBranchPage(&page, arena.bornTxn, level, 2, func(index int) childReference {
			if index == 0 {
				return plan.oldRoot
			}
			return current
		})
		finishRetirementPlannedFrame(&path[plan.pathLength], destination, level, arena.bornTxn, scratchEpoch, page)
		current = childReference{maximum: current.maximum, pageNumber: destination.pageNumber, level: level}
	}
	batchCount := state.batchCount
	if plan.mode == upsertAppend {
		batchCount++
	}
	return retirementTreeEditResult{root: current.pageNumber, batchCount: batchCount, privatePages: plan.pages}, bindRetirementScratch(path, plan.pages, scratchEpoch), retirementWriteError{}
}

// binaryLittleEndianUint16 keeps the page-preparation helpers independent of
// decoding operations after their final validation callback.
func binaryLittleEndianUint16(bytes []byte) uint16 {
	return uint16(bytes[0]) | uint16(bytes[1])<<8
}

func prepareRetirementDeletePages(state retirementTreeState, arena *privatePageArena, path []retirementPathFrame, plan deletePlan, cursor *retirementDestinationCursor, scratchEpoch uint64) (retirementTreeEditResult, retirementScratchBinding, retirementWriteError) {
	if plan.pages > len(path) || plan.pages > retirementWriterPathCapacity {
		return retirementTreeEditResult{}, retirementScratchBinding{}, retirementWriteError{code: retirementWriteErrPathBufferTooSmall, required: plan.pages, actual: len(path)}
	}
	if !plan.hasBoundary {
		return retirementTreeEditResult{}, retirementScratchBinding{}, retirementWriteError{}
	}
	var reserved [retirementWriterPathCapacity]retirementPlannedDestination
	for index := 0; index < plan.pages; index++ {
		destination, problem := cursor.take(arena)
		if problem.failed() {
			return retirementTreeEditResult{}, retirementScratchBinding{}, problem
		}
		reserved[index] = destination
	}
	used := 0
	boundary := plan.boundary
	current := boundary.base
	if boundary.partialLeaf {
		depth := boundary.deepestDepth
		frame := &path[depth]
		oldCount := int(binaryLittleEndianUint16(frame.page[16:18]))
		keepFrom := int(frame.keepFrom)
		var page [PageSize]byte
		encodeRetirementLeafPage(&page, arena.bornTxn, oldCount-keepFrom, func(index int) retirementBatch {
			return rawRetirementBatch(&frame.page, keepFrom+index)
		})
		destination := reserved[used]
		used++
		maximum := rawRetirementMaximum(frame)
		finishRetirementPlannedFrame(frame, destination, 0, arena.bornTxn, scratchEpoch, page)
		current = childReference{maximum: maximum, pageNumber: destination.pageNumber}
	}
	if boundary.hasNewRootDepth {
		for depth := boundary.deepestDepth; depth >= boundary.newRootDepth; depth-- {
			frame := &path[depth]
			if frame.level == 0 {
				continue
			}
			oldCount := int(binaryLittleEndianUint16(frame.page[16:18]))
			keepFrom := int(frame.keepFrom)
			level := frame.level
			maximum := rawRetirementMaximum(frame)
			var page [PageSize]byte
			encodeRetirementBranchPage(&page, arena.bornTxn, level, oldCount-keepFrom, func(index int) childReference {
				if index == 0 {
					return current
				}
				child := rawRetirementBranchEntry(&frame.page, keepFrom+index)
				child.level = level - 1
				return child
			})
			destination := reserved[used]
			used++
			finishRetirementPlannedFrame(frame, destination, level, arena.bornTxn, scratchEpoch, page)
			current = childReference{maximum: maximum, pageNumber: destination.pageNumber, level: level}
		}
	}
	if used != plan.pages {
		return retirementTreeEditResult{}, retirementScratchBinding{}, retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
	}
	output := 0
	for depth := 0; depth <= boundary.deepestDepth; depth++ {
		if path[depth].scratchEpoch != scratchEpoch {
			continue
		}
		if output != depth {
			path[output] = path[depth]
		}
		output++
	}
	if output != plan.pages {
		return retirementTreeEditResult{}, retirementScratchBinding{}, retirementWriteError{code: retirementWriteErrPrivateBindingDrift}
	}
	return retirementTreeEditResult{root: current.pageNumber, batchCount: state.batchCount, privatePages: plan.pages}, bindRetirementScratch(path, plan.pages, scratchEpoch), retirementWriteError{}
}

func privateWriterDraftSourceFrom(
	source committedPageSource,
) *privateWriterDraftPageSource {
	switch typed := source.(type) {
	case *privateWriterDraftPageSource:
		return typed
	case *guardedRetirementSource:
		return privateWriterDraftSourceFrom(typed.source)
	default:
		return nil
	}
}

func prepareRetirementReleaseDescriptors(source committedPageSource, arena *privatePageArena, releases *privateReleaseBuffer, start, count int, roles *pageRoleIndex) retirementWriteError {
	if start < 0 || count < 0 || start > releases.length || count > releases.length-start {
		return retirementWriteError{code: retirementWriteErrPrivateBindingDrift, binding: retirementEditBindingRelease}
	}
	for _, pageNumber := range releases.pageNumbers[start : start+count] {
		roleIndex, found, problem := roles.locate(pageNumber)
		if problem.failed() {
			return problem
		}
		if !found {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		role := &roles.slots[roleIndex]
		if role.priorPrivate {
			draft := privateWriterDraftSourceFrom(source)
			if draft == nil {
				return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: pageNumber}
			}
			if _, _, fixedProblem := draft.validatePriorPrivate(role.prior); fixedProblem.failed() {
				return retirementSourceProblem(pageSourceStatus{
					code: pageSourceErrForkedHandle, page: pageNumber,
				})
			}
		}
		slotIndex, found := arena.pagePool().slotIndex(pageNumber)
		if !found {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		slot := &arena.pagePool().slots[slotIndex]
		if !role.priorPrivate &&
			(slot.scopeID != arena.scope.id || slot.scopeAnchorIndex != arena.scope.anchor) {
			return retirementWriteError{code: retirementWriteErrPrivateScopeMismatch, page: pageNumber}
		}
		if slot.state != privatePageInUse || !slot.inUse || slot.owner != privatePageOwnerRetirement ||
			(slot.origin != privatePageRetirementTree && slot.origin != privatePageRetirementBlob) || slot.pendingTxn != arena.bornTxn {
			return retirementWriteError{code: retirementWriteErrPrivatePageUnavailable, page: pageNumber}
		}
		role.preparedSlot = slotIndex
		role.preparedEpoch = slot.epoch
		role.preparedAuth = slot.authorization
		role.preparedOwner = slot.owner
		role.preparedOrigin = slot.origin
		role.preparedTxn = slot.pendingTxn
		role.preparedGen = slot.generation
	}
	return retirementWriteError{}
}

type retirementEditPlan struct {
	guard              guardedRetirementSource
	arena              *privatePageArena
	token              *retirementBlobToken
	deletePath         []retirementPathFrame
	upsertPath         []retirementPathFrame
	replacements       *committedReplacementLedger
	releases           *privateReleaseBuffer
	roles              *pageRoleIndex
	scope              privatePageReservationScope
	seal               retirementMutableSeal
	deleteScratch      retirementScratchBinding
	upsertScratch      retirementScratchBinding
	replacementBase    int
	releaseBase        int
	stagedReplacements int
	stagedReleases     int
	planID             uint64
	result             retirementTreeEditResult
}

func (p *retirementEditPlan) activate() retirementWriteError {
	next := p.roles.planSequence + 1
	if next == 0 {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	p.roles.planSequence = next
	p.roles.activePlan = next
	p.planID = next
	p.seal = p.guard.seal(nil)
	return retirementWriteError{}
}

func validateRetirementScratch(path []retirementPathFrame, binding retirementScratchBinding, stale retirementEditBinding) retirementWriteError {
	if binding.length == 0 {
		return retirementWriteError{}
	}
	if binding.length > len(path) || binding.length > retirementWriterPathCapacity {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: stale}
	}
	for index := 0; index < binding.length; index++ {
		frame := &path[index]
		if frame.scratchEpoch != binding.epoch || frame.destinationSlot == privatePagePoolNoIndex ||
			retirementHashPage(retirementHashOffset, &frame.page) != binding.fingerprints[index] {
			return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: stale}
		}
	}
	return retirementWriteError{}
}

func retirementOutputReused(pageNumber uint32, upsert []retirementPathFrame) bool {
	for index := range upsert {
		if upsert[index].pageNumber == pageNumber {
			return true
		}
	}
	return false
}

func (p *retirementEditPlan) validateDestination(frame *retirementPathFrame) retirementWriteError {
	if frame.destinationSlot < 0 || frame.destinationSlot >= len(p.arena.pagePool().slots) {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingDestination, page: frame.pageNumber}
	}
	slot := &p.arena.pagePool().slots[frame.destinationSlot]
	if slot.pageNumber != frame.pageNumber || slot.epoch != frame.destinationEpoch ||
		slot.authorization != frame.destinationAuthorization || slot.owner != frame.destinationOwner ||
		slot.state != privatePageAvailable || slot.inUse || slot.scopeID != p.scope.id || slot.scopeAnchorIndex != p.scope.anchor {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingDestination, page: frame.pageNumber}
	}
	if slot.epoch == ^uint64(0) {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow, page: frame.pageNumber}
	}
	return retirementWriteError{}
}

func (p *retirementEditPlan) validateRelease(pageNumber uint32) retirementWriteError {
	roleIndex, found, problem := p.roles.locate(pageNumber)
	if problem.failed() || !found {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
	}
	role := &p.roles.slots[roleIndex]
	if role.preparedSlot < 0 || role.preparedSlot >= len(p.arena.pagePool().slots) {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
	}
	slot := &p.arena.pagePool().slots[role.preparedSlot]
	if slot.pageNumber != pageNumber || slot.epoch != role.preparedEpoch || slot.authorization != role.preparedAuth ||
		slot.owner != role.preparedOwner || slot.origin != role.preparedOrigin || slot.pendingTxn != role.preparedTxn ||
		slot.generation != role.preparedGen || slot.state != privatePageInUse || !slot.inUse {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
	}
	if role.priorPrivate {
		draft := privateWriterDraftSourceFrom(p.guard.source)
		if draft == nil {
			return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
		}
		if _, _, problem := draft.validatePriorPrivate(role.prior); problem.failed() {
			return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
		}
	} else if slot.scopeID != p.scope.id || slot.scopeAnchorIndex != p.scope.anchor {
		return retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingRelease, page: pageNumber}
	}
	if slot.epoch > ^uint64(0)-2 {
		return retirementWriteError{code: retirementWriteErrArithmeticOverflow, page: pageNumber}
	}
	return retirementWriteError{}
}

func (p *retirementEditPlan) validatePriorReferences() retirementWriteError {
	draft := privateWriterDraftSourceFrom(p.guard.source)
	for index := range p.roles.slots {
		role := &p.roles.slots[index]
		if !role.occupied || !role.priorPrivate {
			continue
		}
		if draft == nil {
			return retirementWriteError{
				code:    retirementWriteErrStaleEditPlan,
				binding: retirementEditBindingSource,
				page:    role.pageNumber,
			}
		}
		if _, _, problem := draft.validatePriorPrivate(role.prior); problem.failed() {
			return retirementWriteError{
				code:    retirementWriteErrStaleEditPlan,
				binding: retirementEditBindingSource,
				page:    role.pageNumber,
			}
		}
	}
	return retirementWriteError{}
}

func (p *retirementEditPlan) apply() (retirementTreeEditResult, retirementWriteError) {
	if p.planID == 0 || p.roles.activePlan != p.planID {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrEditPlanConsumed}
	}
	difference := retirementSealDifference(p.seal, p.guard.seal(nil))
	p.roles.activePlan = 0
	if difference != 0 {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: difference}
	}
	consumedSeal := p.guard.seal(nil)
	if status := p.guard.checkAccessStatus(); status.failed() {
		problem := p.guard.checkedProblem(retirementSourceProblem(status))
		if problem.code == retirementWriteErrStaleEditPlan {
			p.roles.planSequence = p.planID
			p.roles.activePlan = 0
		}
		return retirementTreeEditResult{}, problem
	}
	if difference := retirementSealDifference(consumedSeal, p.guard.seal(nil)); difference != 0 {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: difference}
	}
	if p.token != nil && p.token.arena != p.arena {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingBlobToken}
	}
	if p.arena.scope != p.scope {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingScope}
	}
	if problem := validateRetirementScratch(p.deletePath, p.deleteScratch, retirementEditBindingDeletePath); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if problem := validateRetirementScratch(p.upsertPath, p.upsertScratch, retirementEditBindingUpsertPath); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	if p.replacements.length != p.replacementBase || p.stagedReplacements < 0 || p.stagedReplacements > len(p.replacements.entries)-p.replacementBase {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingReplacementLedger}
	}
	if p.releases.length != p.releaseBase || p.stagedReleases < 0 || p.stagedReleases > len(p.releases.pageNumbers)-p.releaseBase {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrStaleEditPlan, binding: retirementEditBindingReleaseLedger}
	}

	deleteFrames := p.deletePath[:p.deleteScratch.length]
	upsertFrames := p.upsertPath[:p.upsertScratch.length]
	uniqueDelete := 0
	for index := range deleteFrames {
		if !retirementOutputReused(deleteFrames[index].pageNumber, upsertFrames) {
			uniqueDelete++
		}
	}
	outputCount, ok := checkedIntAdd(uniqueDelete, len(upsertFrames))
	if !ok {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	for index := range deleteFrames {
		if problem := p.validateDestination(&deleteFrames[index]); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
	}
	for index := range upsertFrames {
		if problem := p.validateDestination(&upsertFrames[index]); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
	}
	for _, pageNumber := range p.releases.pageNumbers[p.releaseBase : p.releaseBase+p.stagedReleases] {
		if problem := p.validateRelease(pageNumber); problem.failed() {
			return retirementTreeEditResult{}, problem
		}
	}
	if problem := p.validatePriorReferences(); problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	checkpoint, poolProblem := p.arena.pagePool().preflightCheckpoint()
	if poolProblem.failed() {
		return retirementTreeEditResult{}, retirementPoolError(poolProblem, p.arena.committedPageCount, p.arena.pendingPageCount)
	}
	steps, ok := checkedMul(uint64(outputCount), 2)
	if !ok {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	releaseSteps, ok := checkedMul(uint64(p.stagedReleases), 2)
	if !ok || steps > ^uint64(0)-releaseSteps {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}
	if poolProblem = p.arena.pagePool().requireMutationSteps(steps + releaseSteps); poolProblem.failed() {
		return retirementTreeEditResult{}, retirementPoolError(poolProblem, p.arena.committedPageCount, p.arena.pendingPageCount)
	}
	if uint64(outputCount+p.stagedReleases) > ^uint64(0)-p.arena.pagePool().checkpointCleanup {
		return retirementTreeEditResult{}, retirementWriteError{code: retirementWriteErrArithmeticOverflow}
	}

	pool := p.arena.pagePool()
	pool.beginCheckpointPrepared(checkpoint)
	for index := range deleteFrames {
		frame := &deleteFrames[index]
		if !retirementOutputReused(frame.pageNumber, upsertFrames) {
			pool.installRetirementPageInScopePrepared(checkpoint, p.scope, frame.destinationSlot, privatePageRetirementTree, &frame.page)
		}
	}
	for index := range upsertFrames {
		frame := &upsertFrames[index]
		pool.installRetirementPageInScopePrepared(checkpoint, p.scope, frame.destinationSlot, privatePageRetirementTree, &frame.page)
	}
	for _, pageNumber := range p.releases.pageNumbers[p.releaseBase : p.releaseBase+p.stagedReleases] {
		roleIndex, _, _ := p.roles.locate(pageNumber)
		role := &p.roles.slots[roleIndex]
		if role.priorPrivate {
			draft := privateWriterDraftSourceFrom(p.guard.source)
			recordIndex := draft.slotRecords[role.prior.slot] - 1
			record := &draft.records[recordIndex]
			if problem := pool.releaseSealedSlotForCheckpointPrepared(
				checkpoint, record.output.scope, role.preparedSlot, privatePageAvailable,
			); problem.failed() {
				pool.abortRequired = true
				return retirementTreeEditResult{}, retirementPoolError(
					problem, p.arena.committedPageCount, p.arena.pendingPageCount,
				)
			}
		} else {
			pool.releaseRetirementSlotInScopePrepared(checkpoint, p.scope, role.preparedSlot)
		}
	}
	pool.commitCheckpointInScopeTerminalPrepared(checkpoint, p.scope)
	if draft := privateWriterDraftSourceFrom(p.guard.source); draft != nil {
		for _, pageNumber := range p.releases.pageNumbers[p.releaseBase : p.releaseBase+p.stagedReleases] {
			roleIndex, _, _ := p.roles.locate(pageNumber)
			role := &p.roles.slots[roleIndex]
			if role.priorPrivate {
				draft.finishValidatedPriorPrivateReturn(role.prior)
			}
		}
	}
	p.replacements.length = p.replacementBase + p.stagedReplacements
	p.releases.length = p.releaseBase
	p.arena.allocationCursor = 0
	p.arena.plannedFingerprint, p.arena.appliedFingerprint = 0, 0
	p.arena.plannedDestinations, p.arena.appliedDestinations = 0, 0
	if p.token != nil {
		p.token.stabilize()
	}
	return p.result, retirementWriteError{}
}

func planScopedRetirementUpsert(
	source committedPageSource,
	state retirementTreeState,
	token *retirementBlobToken,
	path []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementEditPlan, retirementWriteError) {
	if token == nil || token.arena == nil || !token.arena.scoped {
		return retirementEditPlan{}, retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	arena := token.arena
	guard := newGuardedRetirementSource(source, arena, nil, path, blobScratch, replacements, releases, roles, token)
	fail := func(problem retirementWriteError) (retirementEditPlan, retirementWriteError) {
		return retirementEditPlan{}, guard.checkedProblem(problem)
	}
	if problem := arena.validateAuthority(); problem.failed() {
		return fail(problem)
	}
	if status := guard.checkAccessStatus(); status.failed() {
		return fail(retirementSourceProblem(status))
	}
	if problem := token.valid(); problem.failed() {
		return fail(problem)
	}
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return fail(problem)
	}
	if token.bornTxn != arena.bornTxn {
		return fail(retirementWriteError{code: retirementWriteErrBlobTokenTransactionMismatch, first64: arena.bornTxn, second64: token.bornTxn})
	}
	rootToken, rootProblem := arena.privateToken(token.root, privatePageRetirementBlob)
	if rootProblem.failed() || rootToken.generation != token.generation {
		return fail(retirementWriteError{code: retirementWriteErrBlobTokenGenerationMismatch, first64: token.generation})
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return fail(problem)
	}
	roles.requireNewReplacements()
	scratchEpoch, problem := nextRetirementScratchEpoch(path)
	if problem.failed() {
		return fail(problem)
	}
	localReplacements, localReleases := *replacements, *releases
	replacementBase, releaseBase := replacements.length, releases.length
	batch := retirementBatch{retiredByTxn: token.bornTxn, pageCount: token.pageCount, pageListBlobRoot: token.root}
	append, problem := preflightRetirementUpsert(&guard, state, batch, arena, path, &localReplacements, &localReleases, roles)
	if problem.failed() {
		return fail(problem)
	}
	if append.mode == upsertReplace {
		if problem = scanRetirementBatchBlob(&guard, state, arena, append.oldBatch, 0, false, listedPageMarkRequired, true, &localReplacements, &localReleases, roles, blobScratch); problem.failed() {
			return fail(problem)
		}
	}
	if problem = scanRetirementBatchBlob(&guard, state, arena, batch, token.generation, true, listedPageSatisfyRequired, false, &localReplacements, &localReleases, roles, blobScratch); problem.failed() {
		return fail(problem)
	}
	if pageNumber, found := roles.firstUnsatisfiedRequired(); found {
		return fail(retirementWriteError{code: retirementWriteErrRetirementListOmission, page: pageNumber})
	}
	cursor := retirementDestinationCursor{}
	result, upsertScratch, problem := prepareRetirementUpsertPages(state, batch, arena, path, append, &cursor, scratchEpoch)
	if problem.failed() {
		return fail(problem)
	}
	if problem = arena.requirePages(cursor.selected); problem.failed() {
		return fail(problem)
	}
	stagedReplacements := localReplacements.length - replacementBase
	stagedReleases := localReleases.length - releaseBase
	if problem = prepareRetirementReleaseDescriptors(&guard, arena, &localReleases, releaseBase, stagedReleases, roles); problem.failed() {
		return fail(problem)
	}
	result.committedReplacements = stagedReplacements
	plan := retirementEditPlan{
		guard: guard, arena: arena, token: token, upsertPath: path,
		replacements: replacements, releases: releases, roles: roles, scope: arena.scope,
		upsertScratch: upsertScratch, replacementBase: replacementBase, releaseBase: releaseBase,
		stagedReplacements: stagedReplacements, stagedReleases: stagedReleases, result: result,
	}
	if problem = plan.activate(); problem.failed() {
		return fail(problem)
	}
	return plan, retirementWriteError{}
}

func planScopedRetirementDelete(
	source committedPageSource,
	state retirementTreeState,
	deleteCount uint64,
	arena *privatePageArena,
	path []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementEditPlan, retirementWriteError) {
	if arena == nil || !arena.scoped {
		return retirementEditPlan{}, retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	guard := newGuardedRetirementSource(source, arena, path, nil, blobScratch, replacements, releases, roles, nil)
	fail := func(problem retirementWriteError) (retirementEditPlan, retirementWriteError) {
		return retirementEditPlan{}, guard.checkedProblem(problem)
	}
	if problem := arena.validateAuthority(); problem.failed() {
		return fail(problem)
	}
	if status := guard.checkAccessStatus(); status.failed() {
		return fail(retirementSourceProblem(status))
	}
	if arena.activeTokenEpoch != 0 {
		return fail(retirementWriteError{code: retirementWriteErrBlobTokenStale})
	}
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return fail(problem)
	}
	if deleteCount > state.batchCount {
		return fail(retirementWriteError{code: retirementWriteErrDeleteCountOutOfRange, first64: deleteCount, second64: state.batchCount})
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return fail(problem)
	}
	scratchEpoch, problem := nextRetirementScratchEpoch(path)
	if problem.failed() {
		return fail(problem)
	}
	localReplacements, localReleases := *replacements, *releases
	replacementBase, releaseBase := replacements.length, releases.length
	cursor := retirementDestinationCursor{}
	result := retirementTreeEditResult{root: state.root, batchCount: state.batchCount}
	deleteScratch := retirementScratchBinding{}
	if deleteCount != 0 {
		scanner := retirementDeleteScanner{
			source: &guard, state: state, arena: arena, path: path,
			replacements: &localReplacements, releases: &localReleases, roles: roles,
			blobScratch: blobScratch, remaining: deleteCount,
		}
		outcome, scanProblem := scanner.scanNode(state.root, 0, false, 0, false, 0)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		if scanner.remaining != 0 || scanner.deleted != deleteCount {
			return fail(retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: deleteCount, second64: scanner.deleted})
		}
		delete, scanProblem := scanner.finishPlan(outcome, deleteCount)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		result, deleteScratch, scanProblem = prepareRetirementDeletePages(state, arena, path, delete, &cursor, scratchEpoch)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		result.batchCount = state.batchCount - deleteCount
	}
	if problem = arena.requirePages(cursor.selected); problem.failed() {
		return fail(problem)
	}
	stagedReplacements := localReplacements.length - replacementBase
	stagedReleases := localReleases.length - releaseBase
	if problem = prepareRetirementReleaseDescriptors(&guard, arena, &localReleases, releaseBase, stagedReleases, roles); problem.failed() {
		return fail(problem)
	}
	result.committedReplacements = stagedReplacements
	plan := retirementEditPlan{
		guard: guard, arena: arena, deletePath: path,
		replacements: replacements, releases: releases, roles: roles, scope: arena.scope,
		deleteScratch: deleteScratch, replacementBase: replacementBase, releaseBase: releaseBase,
		stagedReplacements: stagedReplacements, stagedReleases: stagedReleases, result: result,
	}
	if problem = plan.activate(); problem.failed() {
		return fail(problem)
	}
	return plan, retirementWriteError{}
}

func planScopedRetirementCombined(
	source committedPageSource,
	state retirementTreeState,
	deleteCount uint64,
	token *retirementBlobToken,
	deletePath, upsertPath []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementEditPlan, retirementWriteError) {
	if token == nil || token.arena == nil || !token.arena.scoped {
		return retirementEditPlan{}, retirementWriteError{code: retirementWriteErrPrivateScopeMismatch}
	}
	arena := token.arena
	guard := newGuardedRetirementSource(source, arena, deletePath, upsertPath, blobScratch, replacements, releases, roles, token)
	fail := func(problem retirementWriteError) (retirementEditPlan, retirementWriteError) {
		return retirementEditPlan{}, guard.checkedProblem(problem)
	}
	if problem := arena.validateAuthority(); problem.failed() {
		return fail(problem)
	}
	if status := guard.checkAccessStatus(); status.failed() {
		return fail(retirementSourceProblem(status))
	}
	if problem := token.valid(); problem.failed() {
		return fail(problem)
	}
	if problem := validateRetirementEditInputs(state, arena, replacements); problem.failed() {
		return fail(problem)
	}
	if token.bornTxn != arena.bornTxn {
		return fail(retirementWriteError{code: retirementWriteErrBlobTokenTransactionMismatch, first64: arena.bornTxn, second64: token.bornTxn})
	}
	if deleteCount > state.batchCount {
		return fail(retirementWriteError{code: retirementWriteErrDeleteCountOutOfRange, first64: deleteCount, second64: state.batchCount})
	}
	rootToken, rootProblem := arena.privateToken(token.root, privatePageRetirementBlob)
	if rootProblem.failed() || rootToken.generation != token.generation {
		return fail(retirementWriteError{code: retirementWriteErrBlobTokenGenerationMismatch, first64: token.generation})
	}
	if problem := roles.prepare(arena, replacements); problem.failed() {
		return fail(problem)
	}
	roles.requireNewReplacements()
	deleteEpoch, problem := nextRetirementScratchEpoch(deletePath)
	if problem.failed() {
		return fail(problem)
	}
	upsertEpoch, problem := nextRetirementScratchEpoch(upsertPath)
	if problem.failed() {
		return fail(problem)
	}
	localReplacements, localReleases := *replacements, *releases
	replacementBase, releaseBase := replacements.length, releases.length
	cursor := retirementDestinationCursor{}
	intermediate := retirementTreeEditResult{root: state.root, batchCount: state.batchCount}
	deleteScratch := retirementScratchBinding{}
	if deleteCount != 0 {
		scanner := retirementDeleteScanner{
			source: &guard, state: state, arena: arena, path: deletePath,
			replacements: &localReplacements, releases: &localReleases, roles: roles,
			blobScratch: blobScratch, remaining: deleteCount,
		}
		outcome, scanProblem := scanner.scanNode(state.root, 0, false, 0, false, 0)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		if scanner.remaining != 0 || scanner.deleted != deleteCount {
			return fail(retirementWriteError{code: retirementWriteErrBatchCountMismatch, first64: deleteCount, second64: scanner.deleted})
		}
		delete, scanProblem := scanner.finishPlan(outcome, deleteCount)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		intermediate, deleteScratch, scanProblem = prepareRetirementDeletePages(state, arena, deletePath, delete, &cursor, deleteEpoch)
		if scanProblem.failed() {
			return fail(scanProblem)
		}
		intermediate.batchCount = state.batchCount - deleteCount
		if problem = roles.advanceReferenceEpoch(); problem.failed() {
			return fail(problem)
		}
	}
	intermediateState := state
	intermediateState.root, intermediateState.batchCount = intermediate.root, intermediate.batchCount
	overlay := retirementVirtualOverlay{frames: deletePath, length: deleteScratch.length, generation: arena.pagePool().generation + 1}
	if overlay.generation == 0 {
		return fail(retirementWriteError{code: retirementWriteErrArithmeticOverflow})
	}
	batch := retirementBatch{retiredByTxn: token.bornTxn, pageCount: token.pageCount, pageListBlobRoot: token.root}
	append, problem := preflightRetirementUpsertWithOverlay(&guard, intermediateState, batch, arena, upsertPath, &localReplacements, &localReleases, roles, &overlay)
	if problem.failed() {
		return fail(problem)
	}
	if append.mode == upsertReplace {
		if problem = scanRetirementBatchBlob(&guard, intermediateState, arena, append.oldBatch, 0, false, listedPageMarkRequired, true, &localReplacements, &localReleases, roles, blobScratch); problem.failed() {
			return fail(problem)
		}
	}
	if problem = scanRetirementBatchBlob(&guard, intermediateState, arena, batch, token.generation, true, listedPageSatisfyRequired, false, &localReplacements, &localReleases, roles, blobScratch); problem.failed() {
		return fail(problem)
	}
	if pageNumber, found := roles.firstUnsatisfiedRequired(); found {
		return fail(retirementWriteError{code: retirementWriteErrRetirementListOmission, page: pageNumber})
	}
	result, upsertScratch, problem := prepareRetirementUpsertPages(intermediateState, batch, arena, upsertPath, append, &cursor, upsertEpoch)
	if problem.failed() {
		return fail(problem)
	}
	reused := 0
	for index := 0; index < deleteScratch.length; index++ {
		if retirementOutputReused(deletePath[index].pageNumber, upsertPath[:upsertScratch.length]) {
			reused++
		}
	}
	combinedPrivatePages, ok := checkedIntAdd(intermediate.privatePages, result.privatePages)
	if !ok {
		return fail(retirementWriteError{code: retirementWriteErrArithmeticOverflow})
	}
	if reused > combinedPrivatePages {
		return fail(retirementWriteError{code: retirementWriteErrArithmeticOverflow})
	}
	result.privatePages = combinedPrivatePages - reused
	if problem = arena.requirePages(cursor.selected); problem.failed() {
		return fail(problem)
	}
	stagedReplacements := localReplacements.length - replacementBase
	stagedReleases := localReleases.length - releaseBase
	if problem = prepareRetirementReleaseDescriptors(&guard, arena, &localReleases, releaseBase, stagedReleases, roles); problem.failed() {
		return fail(problem)
	}
	result.committedReplacements = stagedReplacements
	plan := retirementEditPlan{
		guard: guard, arena: arena, token: token, deletePath: deletePath, upsertPath: upsertPath,
		replacements: replacements, releases: releases, roles: roles, scope: arena.scope,
		deleteScratch: deleteScratch, upsertScratch: upsertScratch,
		replacementBase: replacementBase, releaseBase: releaseBase,
		stagedReplacements: stagedReplacements, stagedReleases: stagedReleases, result: result,
	}
	if problem = plan.activate(); problem.failed() {
		return fail(problem)
	}
	return plan, retirementWriteError{}
}

func upsertNewestRetirementInScope(
	source committedPageSource,
	state retirementTreeState,
	token *retirementBlobToken,
	path []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementTreeEditResult, retirementWriteError) {
	plan, problem := planScopedRetirementUpsert(source, state, token, path, blobScratch, replacements, releases, roles)
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	result, problem := plan.apply()
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	return result, retirementWriteError{}
}

func deleteOldestRetirementPrefixInScope(
	source committedPageSource,
	state retirementTreeState,
	deleteCount uint64,
	arena *privatePageArena,
	path []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementTreeEditResult, retirementWriteError) {
	plan, problem := planScopedRetirementDelete(source, state, deleteCount, arena, path, blobScratch, replacements, releases, roles)
	if problem.failed() {
		return retirementTreeEditResult{}, problem
	}
	return plan.apply()
}

func deleteOldestAndUpsertNewestRetirementInScope(
	source committedPageSource,
	state retirementTreeState,
	deleteCount uint64,
	token *retirementBlobToken,
	deletePath, upsertPath []retirementPathFrame,
	blobScratch *retirementBlobScanScratch,
	replacements *committedReplacementLedger,
	releases *privateReleaseBuffer,
	roles *pageRoleIndex,
) (retirementTreeEditResult, retirementWriteError) {
	plan, problem := planScopedRetirementCombined(source, state, deleteCount, token, deletePath, upsertPath, blobScratch, replacements, releases, roles)
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	result, problem := plan.apply()
	if problem.failed() {
		return retirementTreeEditResult{}, retirementWithCleanup(problem, token.discard())
	}
	return result, retirementWriteError{}
}
