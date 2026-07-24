package exactv4

// rangeTreePayloadReservationSlot records one exact shadow-pool slot selected
// for a logical range page. The later coordinator assigns its own final pool
// slot, so this provenance stays separate from the terminal page journal.
type rangeTreePayloadReservationSlot struct {
	slot  int
	epoch uint64
}

type rangeTreePayloadScratch struct {
	assignments   []rangeTreePhysicalAssignment
	slots         []rangeTreePayloadReservationSlot
	terminalPages []privateWriterProducedTerminalPage
}

type rangeTreePayloadStageErrorCode uint8

const (
	rangeTreePayloadStageErrInvalid rangeTreePayloadStageErrorCode = iota + 1
	rangeTreePayloadStageErrTransaction
	rangeTreePayloadStageErrPayloadBudget
	rangeTreePayloadStageErrScratch
	rangeTreePayloadStageErrAvailable
	rangeTreePayloadStageErrSlot
	rangeTreePayloadStageErrOrder
	rangeTreePayloadStageErrPreMutationBitmap
	rangeTreePayloadStageErrPreMutationStaging
	rangeTreePayloadStageErrDiscard
)

// rangeTreePayloadStageError makes the recovery boundary explicit. Before a
// slot claim the attachment remains reusable. Once one claim has succeeded,
// the caller must discard the whole draft attempt rather than publish or retry
// through the changed shadow scope.
type rangeTreePayloadStageError struct {
	code             rangeTreePayloadStageErrorCode
	required, actual int
	page, previous   uint32
	bitmap           freeBitmapCOWError
	staging          error
}

func (e rangeTreePayloadStageError) failed() bool { return e.code != 0 }

func clearRangeTreePayloadSelection(
	assignments []rangeTreePhysicalAssignment,
	slots []rangeTreePayloadReservationSlot,
) {
	clear(assignments)
	clear(slots)
}

func clearRangeTreePayloadOutput(scratch *rangeTreePayloadScratch, count int) {
	if scratch == nil || count < 0 || count > len(scratch.terminalPages) {
		return
	}
	clear(scratch.terminalPages[:count])
}

// stageRangePayload assigns the lowest currently available shadow pages to a
// completed logical range tree. It uses no file or temporary scratch: all
// backing storage is supplied by the caller and every fallible check completes
// before the first pool claim.
func stageRangePayload[K rangeKey[K]](
	p *freeBitmapReservationAttachment,
	staging *rangeTreeStaging[K],
	staged rangeTreeStagedResult,
	scratch *rangeTreePayloadScratch,
) (rangeTreeMaterializedResult, rangeTreePayloadStageError) {
	if p == nil || staging == nil || scratch == nil || p.cow.pool == nil ||
		p.scope.pool != p.cow.pool || !p.cow.scoped || p.cow.scope != p.scope {
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{code: rangeTreePayloadStageErrInvalid}
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPreMutationBitmap, bitmap: problem,
		}
	}
	if staging.bornTransaction() != p.cow.pendingTxn {
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrTransaction,
		}
	}
	required := staged.pageCount
	if required < 0 {
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{code: rangeTreePayloadStageErrInvalid}
	}
	if required > p.cow.payloadPageBudget {
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPayloadBudget, required: required, actual: p.cow.payloadPageBudget,
		}
	}
	if len(scratch.assignments) < required || len(scratch.slots) < required || len(scratch.terminalPages) < required {
		actual := len(scratch.assignments)
		if len(scratch.slots) < actual {
			actual = len(scratch.slots)
		}
		if len(scratch.terminalPages) < actual {
			actual = len(scratch.terminalPages)
		}
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrScratch, required: required, actual: actual,
		}
	}
	if p.cow.availableLen < 0 || p.cow.availableLen > len(p.cow.availableSlots) || required > p.cow.availableLen {
		actual := p.cow.availableLen
		if actual < 0 {
			actual = 0
		}
		if actual > len(p.cow.availableSlots) {
			actual = len(p.cow.availableSlots)
		}
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrAvailable, required: required, actual: actual,
		}
	}

	assignments := scratch.assignments[:required]
	slots := scratch.slots[:required]
	terminalPages := scratch.terminalPages[:required]
	for index := range assignments {
		assignments[index] = rangeTreePhysicalAssignment{}
		slots[index] = rangeTreePayloadReservationSlot{}
	}
	var previous uint32
	havePrevious := false
	for index := 0; index < required; index++ {
		slot := p.cow.availableSlots[p.cow.availableLen-1-index]
		info, poolProblem := p.cow.pool.slotInfo(slot)
		if poolProblem.failed() || !info.bound || info.scopeID != p.scope.id ||
			slot < 0 || slot >= len(p.cow.pool.slots) ||
			p.cow.pool.slots[slot].scopeAnchorIndex != p.scope.anchor ||
			info.state != privatePageAvailable || info.owner != privatePageOwnerNone ||
			info.origin != privatePageOriginNone {
			clearRangeTreePayloadSelection(assignments, slots)
			return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
				code: rangeTreePayloadStageErrSlot, page: info.pageNumber,
			}
		}
		if havePrevious && info.pageNumber <= previous {
			clearRangeTreePayloadSelection(assignments, slots)
			return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
				code: rangeTreePayloadStageErrOrder, previous: previous, page: info.pageNumber,
			}
		}
		previous, havePrevious = info.pageNumber, true
		assignments[index] = rangeTreePhysicalAssignment{
			pageNumber: info.pageNumber, authorization: info.authorization,
		}
		slots[index] = rangeTreePayloadReservationSlot{slot: slot, epoch: info.epoch}
	}

	if required == 0 {
		materialized, err := staging.materialize(staged, p.cow.pageCount, assignments, terminalPages)
		if err != nil {
			clearRangeTreePayloadSelection(assignments, slots)
			return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
				code: rangeTreePayloadStageErrPreMutationStaging, staging: err,
			}
		}
		return materialized, rangeTreePayloadStageError{}
	}

	steps := uint64(required)
	if steps > ^uint64(0)-2 {
		clearRangeTreePayloadSelection(assignments, slots)
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{code: rangeTreePayloadStageErrPreMutationBitmap}
	}
	if poolProblem := p.cow.pool.requireMutationSteps(steps + 2); poolProblem.failed() {
		clearRangeTreePayloadSelection(assignments, slots)
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPreMutationBitmap, bitmap: bitmapPoolError(poolProblem),
		}
	}
	checkpoint, poolProblem := p.cow.pool.preflightCheckpoint()
	if poolProblem.failed() {
		clearRangeTreePayloadSelection(assignments, slots)
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPreMutationBitmap, bitmap: bitmapPoolError(poolProblem),
		}
	}
	materialized, err := staging.materialize(staged, p.cow.pageCount, assignments, terminalPages)
	if err != nil {
		clearRangeTreePayloadSelection(assignments, slots)
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPreMutationStaging, staging: err,
		}
	}
	if poolProblem = p.cow.pool.beginCheckpointPrepared(checkpoint); poolProblem.failed() {
		clearRangeTreePayloadSelection(assignments, slots)
		clearRangeTreePayloadOutput(scratch, required)
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrPreMutationBitmap, bitmap: bitmapPoolError(poolProblem),
		}
	}
	for index := range terminalPages {
		selected := slots[index]
		if poolProblem = p.cow.pool.claimSlotWithOwnerAndBytesInScopeForCheckpointPrepared(
			checkpoint, p.scope, selected.slot, selected.epoch, terminalPages[index].pageNumber,
			privatePageOwnerRange, privatePageRange, &terminalPages[index].bytes,
		); poolProblem.failed() {
			p.cow.pool.abortRequired = true
			return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
				code: rangeTreePayloadStageErrDiscard, bitmap: bitmapPoolError(poolProblem),
			}
		}
	}
	if poolProblem = p.cow.pool.commitCheckpointInScopePrepared(checkpoint, p.scope); poolProblem.failed() {
		p.cow.pool.abortRequired = true
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrDiscard, bitmap: bitmapPoolError(poolProblem),
		}
	}
	if problem := p.cow.synchronizeScopedBindings(p.scope); problem.failed() {
		p.cow.pool.abortRequired = true
		return rangeTreeMaterializedResult{}, rangeTreePayloadStageError{
			code: rangeTreePayloadStageErrDiscard, bitmap: problem,
		}
	}
	p.cow.payloadPageBudget -= required
	return materialized, rangeTreePayloadStageError{}
}
