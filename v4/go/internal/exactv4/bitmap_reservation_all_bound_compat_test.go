package exactv4

// plan is retained only as a test compatibility adapter for the earlier
// all-bound reservation suite. Production code has only planCapacity + bind.
func (p *freeBitmapReservationPlanner) plan() (freeBitmapCOW, freeBitmapCOWError) {
	if p.committed != nil {
		if status := p.committed.checkAccessStatus(); status.failed() {
			return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	}
	for {
		required, problem := p.requiredPrivatePages()
		if problem.failed() {
			return freeBitmapCOW{}, problem
		}
		if p.candidateLen >= required {
			return p.finishAllBoundCompatibility(0)
		}
		candidate, path, pathLen, found, problem := p.nextCandidate()
		if problem.failed() {
			return freeBitmapCOW{}, problem
		}
		if !found {
			if p.survivingMetadata != 0 {
				return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrSummaryMismatch}
			}
			appended, problem := p.appendedDeficit()
			if problem.failed() {
				return freeBitmapCOW{}, problem
			}
			return p.finishAllBoundCompatibility(appended)
		}
		if problem = p.reserveCandidate(candidate, path[:pathLen]); problem.failed() {
			return freeBitmapCOW{}, problem
		}
	}
}

func (p *freeBitmapReservationPlanner) finishAllBoundCompatibility(appendedLen int) (freeBitmapCOW, freeBitmapCOWError) {
	reservedLen, ok := checkedIntAdd(p.candidateLen, appendedLen)
	if !ok {
		return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	requiredPrivatePages, problem := p.requiredPrivatePages()
	if problem.failed() {
		return freeBitmapCOW{}, problem
	}
	requiredIndexLen, ok := checkedIntAdd(p.indexLen, appendedLen)
	if !ok {
		return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrIndexCapacityOverflow}
	}
	for _, check := range []struct {
		resource  freeBitmapReservationResource
		required  int
		available int
	}{
		{freeBitmapResourceArenaPages, requiredPrivatePages, reservedLen},
		{freeBitmapResourceArenaPages, reservedLen, len(p.buffers.arena)},
		{freeBitmapResourceAvailableSlots, reservedLen, len(p.buffers.availableSlots)},
		{freeBitmapResourceReplacementPages, p.verifiedLen, len(p.buffers.replacements)},
		{freeBitmapResourceIndexNodes, requiredIndexLen, len(p.buffers.indexNodes)},
	} {
		if problem := p.ensureRoom(check.resource, check.required, check.available); problem.failed() {
			return freeBitmapCOW{}, problem
		}
	}
	if uint64(appendedLen) > MaxPageCount-p.committedPageCount {
		return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
	}
	pendingPageCount, ok := checkedAdd(p.committedPageCount, uint64(appendedLen))
	if !ok {
		return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
	}
	for slot := 0; slot < p.candidateLen; slot++ {
		pageNumber := p.buffers.candidates[slot]
		p.buffers.arena[slot].authorize(pageNumber, privateBitmapPageCommittedFreeCandidate)
		pageIndexReplace(p.buffers.indexNodes, p.indexRoot, pageNumber, indexedBitmapPage{kind: indexedBitmapPageArena, slot: slot})
	}
	for offset := 0; offset < appendedLen; offset++ {
		slot, ok := checkedIntAdd(p.candidateLen, offset)
		if !ok {
			return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
		}
		pageNumber64, ok := checkedAdd(p.committedPageCount, uint64(offset))
		if !ok || pageNumber64 > uint64(^uint32(0)) {
			return freeBitmapCOW{}, freeBitmapCOWError{code: freeBitmapCOWErrPageSpaceExhausted}
		}
		pageNumber := uint32(pageNumber64)
		p.buffers.arena[slot].authorize(pageNumber, privateBitmapPageAppended)
		pageIndexInsertPrechecked(p.buffers.indexNodes, &p.indexRoot, &p.indexLen, pageNumber, indexedBitmapPage{kind: indexedBitmapPageArena, slot: slot})
	}
	for index := 0; index < reservedLen; index++ {
		p.buffers.availableSlots[index] = reservedLen - 1 - index
	}
	poolProblem := initPrivatePagePool(
		p.buffers.pool, p.buffers.arena[:reservedLen], p.buffers.poolValidation,
		p.committedPageCount, pendingPageCount, p.pendingTxn, privatePageOwnerBitmap,
	)
	if poolProblem.failed() {
		return freeBitmapCOW{}, bitmapPoolError(poolProblem)
	}
	return freeBitmapCOW{
		committed: p.committed, selectedTxn: p.selectedTxn, sourceTxn: p.sourceTxn, pendingTxn: p.pendingTxn,
		committedPageCount: p.committedPageCount, pageCount: pendingPageCount, root: p.root,
		pageCountsDistinct: true, pool: p.buffers.pool, replacements: p.buffers.replacements,
		candidates: p.buffers.candidates[:p.candidateLen], indexNodes: p.buffers.indexNodes,
		indexRoot: p.indexRoot, indexLen: p.indexLen,
		availableSlots: p.buffers.availableSlots[:reservedLen], availableLen: reservedLen,
		verifiedPages: p.buffers.verifiedPages[:p.verifiedLen], plannedCandidateLen: p.candidateLen,
		reservationPlanned: true, payloadPageBudget: p.payloadPages,
		plannedRequiredPrivatePages: requiredPrivatePages,
	}, freeBitmapCOWError{}
}

func (p *freeBitmapReservationPlanner) finish(appendedLen int) (freeBitmapCOW, freeBitmapCOWError) {
	return p.finishAllBoundCompatibility(appendedLen)
}
