package exactv4

import (
	"encoding/binary"
	"testing"
)

func fixedPointFinalizedFixture(t *testing.T) (freeBitmapFinalizationResult, *privatePagePool) {
	t.Helper()
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t,
		&cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2,
		2,
		&storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	result := finalizeLateBitmapAttachment(t, &attachment)
	return result, attachment.cow.pool
}

func TestDraftPageSourceClassifiesExactSealedPrivateProvenance(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	record := privateWriterSealedBitmapWorkUnitRecord{}
	if problem := record.initialize(41, result); problem.failed() {
		t.Fatal(problem)
	}
	source := privateWriterDraftPageSource{
		selected:    result.output.committed,
		pool:        pool,
		records:     []privateWriterSealedBitmapWorkUnitRecord{record},
		slotRecords: make([]int, len(pool.slots)),
	}
	if problem := source.installRecordSlots(0); problem.failed() {
		t.Fatal(problem)
	}

	seen := 0
	for index := 0; index < result.output.boundLen; index++ {
		seen++
		binding := result.output.bindings[index]
		residence, problem := source.residence(binding.pageNumber)
		if problem.failed() {
			t.Fatal(problem)
		}
		if residence.kind != privateWriterPagePriorScopePrivate ||
			residence.provenance.workUnit != 41 ||
			residence.provenance.scopeID != result.output.scope.id ||
			residence.provenance.scopeAnchor != result.output.scope.anchor ||
			residence.provenance.slot != binding.poolSlot ||
			residence.provenance.pageNumber != binding.pageNumber ||
			residence.provenance.bindingEpoch != pool.slots[binding.poolSlot].epoch ||
			residence.provenance.owner != privatePageOwnerBitmap ||
			residence.provenance.origin != privatePageBitmap ||
			residence.provenance.generation != pool.slots[binding.poolSlot].generation {
			t.Fatalf("residence[%d] = %#v", index, residence)
		}
		var page [PageSize]byte
		if status := source.readPageStatus(binding.pageNumber, &page); status.failed() {
			t.Fatalf("read[%d] = %#v", index, status)
		}
		if binary.LittleEndian.Uint64(page[8:16]) != result.output.pendingTxn {
			t.Fatalf("private born_txn[%d] = %d", index, binary.LittleEndian.Uint64(page[8:16]))
		}
	}
	if seen == 0 {
		t.Fatal("fixture retained no live private bitmap page")
	}
}

func TestFixedPointSuccessorIsLinearAndCopySafe(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 3)
	var coordinator privateWriterFixedPointCoordinator
	predecessor, problem := initializePrivateWriterFixedPointCoordinator(
		&coordinator,
		pool,
		result.output.committed,
		result.output.selectedTxn,
		result.output.pendingTxn,
		result.output.committedPageCount,
		result.output.root,
		result.output.committedPageCount,
		records,
		make([]int, len(pool.slots)),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	copyOfPredecessor := predecessor
	successor, problem := coordinator.acceptFinalized(
		predecessor, 11, result,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = coordinator.acceptFinalized(copyOfPredecessor, 12, result); problem.code != privateWriterFixedPointErrStalePredecessor {
		t.Fatalf("replayed predecessor = %#v", problem)
	}
	if successor.root != result.output.root || successor.pageCount != result.output.pageCount ||
		successor.sequence != 1 {
		t.Fatalf("successor = %#v", successor)
	}
	if status := coordinator.source().checkAccessStatus(); status.failed() {
		t.Fatalf("composite source = %#v", status)
	}
}

func TestFixedPointShortSlotMapFailsBeforeCoordinatorCreation(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 1)
	var coordinator privateWriterFixedPointCoordinator
	if _, problem := initializePrivateWriterFixedPointCoordinator(
		&coordinator,
		pool,
		result.output.committed,
		result.output.selectedTxn,
		result.output.pendingTxn,
		result.output.committedPageCount,
		result.output.root,
		result.output.committedPageCount,
		records,
		make([]int, len(pool.slots)-1),
	); problem.code != privateWriterFixedPointErrScratchTooSmall ||
		coordinator.self != nil {
		t.Fatalf("short slot map = %#v", problem)
	}
}

func TestFixedPointRejectsNonMonotonicWorkWithoutScanning(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 2)
	slotRecords := make([]int, len(pool.slots))
	var coordinator privateWriterFixedPointCoordinator
	predecessor, problem := initializePrivateWriterFixedPointCoordinator(
		&coordinator,
		pool,
		result.output.committed,
		result.output.selectedTxn,
		result.output.pendingTxn,
		result.output.committedPageCount,
		result.output.root,
		result.output.committedPageCount,
		records,
		slotRecords,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	successor, problem := coordinator.acceptFinalized(
		predecessor, 10, result,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = coordinator.acceptFinalized(
		successor, 10, result,
	); problem.code != privateWriterFixedPointErrDuplicateWorkUnit {
		t.Fatalf("duplicate work unit = %#v", problem)
	}
	if _, problem = coordinator.acceptFinalized(
		successor, 9, result,
	); problem.code != privateWriterFixedPointErrDuplicateWorkUnit {
		t.Fatalf("regressed work unit = %#v", problem)
	}
	if _, _, problem = coordinator.consumeFinal(successor); problem.failed() {
		t.Fatalf("neutral failures consumed predecessor = %#v", problem)
	}
}

func TestFixedPointRecordExhaustionLeavesPredecessorRetryable(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 1)
	var coordinator privateWriterFixedPointCoordinator
	predecessor, problem := initializePrivateWriterFixedPointCoordinator(
		&coordinator,
		pool,
		result.output.committed,
		result.output.selectedTxn,
		result.output.pendingTxn,
		result.output.committedPageCount,
		result.output.root,
		result.output.committedPageCount,
		records,
		make([]int, len(pool.slots)),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	successor, problem := coordinator.acceptFinalized(
		predecessor, 1, result,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = coordinator.acceptFinalized(
		successor, 2, result,
	); problem.code != privateWriterFixedPointErrRecordExhausted {
		t.Fatalf("record exhaustion = %#v", problem)
	}
	if _, _, problem = coordinator.consumeFinal(successor); problem.failed() {
		t.Fatalf("record exhaustion consumed predecessor = %#v", problem)
	}
}

func TestDraftSourceRetiresPriorPrivatePageWithoutReopeningScope(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	record := privateWriterSealedBitmapWorkUnitRecord{}
	if problem := record.initialize(7, result); problem.failed() {
		t.Fatal(problem)
	}
	source := privateWriterDraftPageSource{
		selected: result.output.committed,
		pool:     pool, records: []privateWriterSealedBitmapWorkUnitRecord{record},
		slotRecords: make([]int, len(pool.slots)),
	}
	if problem := source.installRecordSlots(0); problem.failed() {
		t.Fatal(problem)
	}
	bindingIndex := -1
	for index, binding := range result.output.bindings[:result.output.boundLen] {
		if source.slotRecords[binding.poolSlot] != 0 {
			bindingIndex = index
			break
		}
	}
	if bindingIndex < 0 {
		t.Fatal("fixture retained no live private bitmap page")
	}
	pageNumber := result.output.bindings[bindingIndex].pageNumber
	residence, problem := source.residence(pageNumber)
	if problem.failed() {
		t.Fatal(problem)
	}
	beforeAnchor := pool.slots[result.output.scope.anchor]
	if problem = source.returnPriorPrivate(residence.provenance); problem.failed() {
		t.Fatal(problem)
	}
	afterAnchor := pool.slots[result.output.scope.anchor]
	if !afterAnchor.scopeSealed || afterAnchor.scopeID != beforeAnchor.scopeID ||
		afterAnchor.scopeGeneration != beforeAnchor.scopeGeneration {
		t.Fatal("prior scope was reopened or its authority changed")
	}
	if source.slotRecords[result.output.bindings[bindingIndex].poolSlot] != 0 {
		t.Fatal("returned binding remains live in the draft source")
	}
	if _, problem = source.residence(pageNumber); problem.code != privateWriterFixedPointErrAdvertisedOwnedPage {
		t.Fatalf("returned page residence = %#v", problem)
	}
}

func TestFixedPointWarmedSourceLookupAllocatesNothing(t *testing.T) {
	result, pool := fixedPointFinalizedFixture(t)
	record := privateWriterSealedBitmapWorkUnitRecord{}
	if problem := record.initialize(5, result); problem.failed() {
		t.Fatal(problem)
	}
	source := privateWriterDraftPageSource{
		selected: result.output.committed,
		pool:     pool, records: []privateWriterSealedBitmapWorkUnitRecord{record},
		slotRecords: make([]int, len(pool.slots)),
	}
	if problem := source.installRecordSlots(0); problem.failed() {
		t.Fatal(problem)
	}
	bindingIndex := -1
	for index, binding := range result.output.bindings[:result.output.boundLen] {
		if source.slotRecords[binding.poolSlot] != 0 {
			bindingIndex = index
			break
		}
	}
	if bindingIndex < 0 {
		t.Fatal("fixture retained no live private bitmap page")
	}
	pageNumber := result.output.bindings[bindingIndex].pageNumber
	var page [PageSize]byte
	if status := source.readPageStatus(pageNumber, &page); status.failed() {
		t.Fatal(status)
	}
	if allocations := testing.AllocsPerRun(1000, func() {
		if status := source.readPageStatus(pageNumber, &page); status.failed() {
			panic(status)
		}
	}); allocations != 0 {
		t.Fatalf("allocations = %f", allocations)
	}
}

func TestFixedPointSecondBitmapWorkUnitReplansFromPrivateRootAndReturnsIt(t *testing.T) {
	first, pool := fixedPointFinalizedFixture(t)
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 3)
	slotRecords := make([]int, len(pool.slots))
	var coordinator privateWriterFixedPointCoordinator
	predecessor, problem := initializePrivateWriterFixedPointCoordinator(
		&coordinator,
		pool,
		first.output.committed,
		first.output.selectedTxn,
		first.output.pendingTxn,
		first.output.committedPageCount,
		first.output.root,
		first.output.pageCount,
		records,
		slotRecords,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	predecessor, problem = coordinator.acceptFinalized(
		predecessor, 1, first,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	var advertised [PageSize]byte
	if status := coordinator.source().readPageStatus(predecessor.root, &advertised); status.failed() {
		t.Fatalf("read first root = %#v", status)
	}
	leaf, pageProblem := openBitmapLeafNoAlloc(
		advertised[:], first.output.pendingTxn, bitmapKindFreePages,
	)
	if pageProblem.code != 0 {
		t.Fatalf("open first root = %#v", pageProblem)
	}
	advertisedPage, found, valid := searchFreeBitmapLeafFromNoAlloc(
		leaf, 0, predecessor.pageCount, 0,
	)
	if !valid || !found || advertisedPage != 9 {
		t.Fatalf("first root lowest free = page=%d found=%t valid=%t", advertisedPage, found, valid)
	}
	if ownedSlot, owned := pool.slotIndex(uint32(advertisedPage)); owned {
		t.Fatalf("advertised-free page %d remains privately owned in slot %d state=%d scope=%d",
			advertisedPage, ownedSlot, pool.slots[ownedSlot].state, pool.slots[ownedSlot].scopeID)
	}
	oldRootResidence, problem := coordinator.source().residence(predecessor.root)
	if problem.failed() || oldRootResidence.kind != privateWriterPagePriorScopePrivate {
		t.Fatalf("old root residence = %#v %#v", oldRootResidence, problem)
	}

	secondStorage := newLateBitmapPlannerStorage(12, 12, 12, 24)
	planner, bitmapProblem := newFreeBitmapReservationPlannerForDraft(
		coordinator.source(),
		first.output.selectedTxn,
		first.output.pendingTxn,
		first.output.committedPageCount,
		predecessor.pageCount,
		predecessor.root,
		1,
		secondStorage.buffers(),
	)
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	capacity, bitmapProblem := planner.planCapacity()
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	scope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	second, bitmapProblem := capacity.attachDraft(pool, scope, coordinator.source())
	if bitmapProblem.failed() {
		t.Fatalf("attach draft = %#v", bitmapProblem)
	}
	proof, bitmapProblem := completeFreeBitmapReclamation(second.reclamationRequest, 0, nil)
	if bitmapProblem.failed() {
		t.Fatalf("proof second = %#v", bitmapProblem)
	}
	if _, bitmapProblem = second.bind(&proof); bitmapProblem.failed() {
		t.Fatalf("bind second = %#v", bitmapProblem)
	}

	priorReturns := 0
	committedReturns := 0
	for _, pageNumber := range second.cow.replacementPages() {
		residence, residenceProblem := coordinator.source().residence(pageNumber)
		if residenceProblem.failed() {
			t.Fatalf("replacement %d = %#v", pageNumber, residenceProblem)
		}
		switch residence.kind {
		case privateWriterPagePriorScopePrivate:
			if returnProblem := coordinator.source().returnPriorPrivate(
				residence.provenance, &second.cow,
			); returnProblem.failed() {
				t.Fatalf("return %d = %#v", pageNumber, returnProblem)
			}
			priorReturns++
		case privateWriterPageSelectedCommitted:
			committedReturns++
		default:
			t.Fatalf("replacement %d residence = %#v", pageNumber, residence)
		}
	}
	if priorReturns == 0 {
		t.Fatalf("second unit returned no prior-private page; committed=%d", committedReturns)
	}
	secondResult := finalizeLateBitmapAttachment(t, &second)
	predecessor, problem = coordinator.acceptFinalized(
		predecessor, 2, secondResult,
	)
	if problem.failed() {
		t.Fatal(problem)
	}

	thirdStorage := newLateBitmapPlannerStorage(12, 12, 12, 24)
	planner, bitmapProblem = newFreeBitmapReservationPlannerForDraft(
		coordinator.source(),
		first.output.selectedTxn,
		first.output.pendingTxn,
		first.output.committedPageCount,
		predecessor.pageCount,
		predecessor.root,
		1,
		thirdStorage.buffers(),
	)
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	capacity, bitmapProblem = planner.planCapacity()
	if bitmapProblem.failed() {
		t.Fatalf("third capacity = %#v", bitmapProblem)
	}
	scope, poolProblem = pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	third, bitmapProblem := capacity.attachDraft(pool, scope, coordinator.source())
	if bitmapProblem.failed() {
		t.Fatalf("attach third draft = %#v", bitmapProblem)
	}
	proof, bitmapProblem = completeFreeBitmapReclamation(third.reclamationRequest, 0, nil)
	if bitmapProblem.failed() {
		t.Fatalf("proof third = %#v", bitmapProblem)
	}
	if _, bitmapProblem = third.bind(&proof); bitmapProblem.failed() {
		t.Fatalf("bind third = %#v", bitmapProblem)
	}
	thirdPriorReturns := 0
	for _, pageNumber := range third.cow.replacementPages() {
		residence, residenceProblem := coordinator.source().residence(pageNumber)
		if residenceProblem.failed() {
			t.Fatalf("third replacement %d = %#v", pageNumber, residenceProblem)
		}
		if residence.kind == privateWriterPagePriorScopePrivate {
			if returnProblem := coordinator.source().returnPriorPrivate(
				residence.provenance, &third.cow,
			); returnProblem.failed() {
				t.Fatalf("third return %d = %#v", pageNumber, returnProblem)
			}
			thirdPriorReturns++
		}
	}
	if thirdPriorReturns == 0 {
		t.Fatal("third unit returned no prior-private page")
	}
	thirdResult := finalizeLateBitmapAttachment(t, &third)
	if _, problem = coordinator.acceptFinalized(
		predecessor, 3, thirdResult,
	); problem.failed() {
		t.Fatal(problem)
	}
}
