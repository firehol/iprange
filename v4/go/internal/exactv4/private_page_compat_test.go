package exactv4

// These helpers intentionally exist only in tests. Production obtains exact
// authority from the reservation planner and initializes one canonical pool.

const (
	privateBitmapPageLegacyReserved = privatePageReclaimed
	privateBitmapPageGenericPrivate = privatePageReclaimed
)

func newReservedBitmapPage(pageNumber uint32) reservedBitmapPage {
	return reservedBitmapPage{pageNumber: pageNumber, authorization: privatePageReclaimed}
}

func (p *privatePagePoolSlot) authorizeGeneric(pageNumber uint32) {
	p.authorize(pageNumber, privatePageReclaimed)
}

func newPrivatePageSlot(pageNumber uint32, authorization privatePageAuthorization) privatePageSlot {
	return privatePageSlot{pageNumber: pageNumber, authorization: authorization}
}

func newFreeBitmapCOW(
	committed committedPageSource,
	selectedTxn uint64,
	pageCount uint64,
	root uint32,
	ledger freeBitmapCOWLedger,
) (*freeBitmapCOW, freeBitmapCOWError) {
	pendingTxn := selectedTxn + 1
	if selectedTxn == 0 || pendingTxn == 0 {
		return newFreeBitmapCOWWithPool(committed, selectedTxn, pageCount, root, nil, ledger)
	}
	poolCommitted := pageCount
	for index := range ledger.arena {
		if ledger.arena[index].authorization == privatePageAppended && uint64(ledger.arena[index].pageNumber) < poolCommitted {
			poolCommitted = uint64(ledger.arena[index].pageNumber)
		}
	}
	pool := &privatePagePool{}
	validation := make([]uint32, len(ledger.arena))
	if problem := initPrivatePagePool(
		pool, ledger.arena, validation, poolCommitted, pageCount, pendingTxn, privatePageOwnerBitmap,
	); problem.failed() {
		return nil, bitmapPoolError(problem)
	}
	return newFreeBitmapCOWWithPool(committed, selectedTxn, pageCount, root, pool, ledger)
}

func newPrivatePageArena(
	slots []privatePageSlot,
	committedPageCount, pendingPageCount, bornTxn uint64,
) (privatePageArena, retirementWriteError) {
	if bornTxn <= 1 {
		return privatePageArena{}, retirementWriteError{code: retirementWriteErrPendingTransactionOutOfRange, first64: bornTxn}
	}
	var previous uint32
	for index := range slots {
		if slots[index].inUse || slots[index].state == privatePageInUse {
			return privatePageArena{}, retirementWriteError{code: retirementWriteErrPrivateSlotAlreadyInUse, page: slots[index].pageNumber}
		}
		if index != 0 && slots[index].pageNumber <= previous {
			return privatePageArena{}, retirementWriteError{code: retirementWriteErrPrivatePagesNotStrict, page: previous, secondPage: slots[index].pageNumber}
		}
		previous = slots[index].pageNumber
	}
	pool := &privatePagePool{}
	validation := make([]uint32, len(slots))
	if problem := initPrivatePagePool(
		pool, slots, validation, committedPageCount, pendingPageCount, bornTxn, privatePageOwnerRetirement,
	); problem.failed() {
		return privatePageArena{}, retirementPoolError(problem, committedPageCount, pendingPageCount)
	}
	return newPrivatePageArenaWithPool(pool, bornTxn)
}

func newInitializedPrivatePageArena(
	slots []privatePageSlot,
	committedPageCount, pendingPageCount, bornTxn uint64,
) (privatePageArena, retirementWriteError) {
	pool := &privatePagePool{}
	validation := make([]uint32, len(slots))
	if problem := initPrivatePagePool(
		pool, slots, validation, committedPageCount, pendingPageCount, bornTxn, privatePageOwnerRetirement,
	); problem.failed() {
		return privatePageArena{}, retirementPoolError(problem, committedPageCount, pendingPageCount)
	}
	return newPrivatePageArenaWithPool(pool, bornTxn)
}

func (a *privatePageArena) testSlot(pageNumber uint32) *privatePageSlot {
	index, found := a.pool.slotIndex(pageNumber)
	if !found {
		return nil
	}
	return &a.pool.slots[index]
}
