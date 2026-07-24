package exactv4

import (
	"reflect"
	"testing"
)

func producePreparedAggregateBitmap(
	t *testing.T,
	source committedPageSource,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterProducedBitmapTerminal {
	t.Helper()
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(t, source, 2, 1, &storage)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	produced, problem := attachment.finalizeFixedPointBitmapProducer(
		finalizationScratchForAttachment(&attachment), workspace,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	return produced
}

func producePreparedAggregateBitmapRange(
	t *testing.T,
	source committedPageSource,
	workspace *privateWriterFixedPointAggregateWorkspace,
) (
	privateWriterProducedBitmapTerminal,
	privateWriterProducedRangeTerminal,
	rangeTreeMaterializedResult,
	[]privateWriterProducedTerminalPage,
) {
	t.Helper()
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(t, source, 2, 1, &storage)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	rangeTerminal := make([]privateWriterProducedTerminalPage, staged.pageCount)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: rangeTerminal,
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	bitmap, ranges, finalizationProblem := attachment.finalizeFixedPointBitmapRangeProducers(
		finalizationScratchForAttachment(&attachment), materialized, workspace,
	)
	if finalizationProblem.failed() {
		t.Fatal(finalizationProblem)
	}
	return bitmap, ranges, materialized, rangeTerminal
}

func aggregateBitmapContent(
	t *testing.T,
	produced privateWriterProducedBitmapTerminal,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterProducedBitmapTerminalContent {
	t.Helper()
	content, ok := produced.authority(&workspace.bitmapProducer)
	if !ok {
		t.Fatal("bitmap producer authority is not valid")
	}
	return *content
}

func aggregateRangeContent(
	t *testing.T,
	produced privateWriterProducedRangeTerminal,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterProducedRangeTerminalContent {
	t.Helper()
	content, ok := produced.authority(&workspace.rangeProducer)
	if !ok {
		t.Fatal("range producer authority is not valid")
	}
	return *content
}

func produceEmptyAggregateRetirement(
	t *testing.T,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterProducedRetirementTerminal {
	t.Helper()
	produced, problem := workspace.prepareEmptyRetirementProducer()
	if problem.failed() {
		t.Fatal(problem)
	}
	return produced
}

func newAggregateWorkspaceForTest(
	t *testing.T,
	privatePages int,
) *privateWriterFixedPointAggregateWorkspace {
	t.Helper()
	workspace, problem := newPrivateWriterFixedPointAggregateWorkspace(
		privateWriterFixedPointAggregateWorkspaceBudget{
			maxBytes:      8 << 20,
			privatePages:  privatePages,
			terminalPages: privatePages,
			priorReturns:  privatePages,
			touchedSlots:  privatePages,
		},
		privateWriterResourceBudget{
			maxHeapBytes:       8 << 20,
			maxPrivatePages:    uint64(privatePages),
			maxFileGrowthPages: uint64(privatePages),
			maxOpenFiles:       4,
		},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	return workspace
}

func TestFixedPointAggregateActualBitmapProducerExecutesInseparably(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	aggregateWorkspace := newAggregateWorkspaceForTest(
		t, len(fixture.core.pool.slots),
	)
	produced := producePreparedAggregateBitmap(
		t, fixture.source, aggregateWorkspace,
	)
	content := aggregateBitmapContent(t, produced, aggregateWorkspace)
	if content.pageCount == 0 || content.pageLen == 0 {
		t.Fatalf("actual producer returned %+v", content)
	}
	request := fixture.request()
	request.scopePages = content.pageLen
	token, problem := fixture.core.prepareFixedPointWork(fixture.handle, request)
	if problem.failed() {
		t.Fatal(problem)
	}
	aggregate, problem := fixture.core.prepareFixedPointAggregate(
		fixture.handle,
		token,
		produced,
		produceEmptyAggregateRetirement(t, aggregateWorkspace),
		aggregateWorkspace,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	sealed, problem := fixture.core.executeFixedPointAggregate(
		fixture.handle, aggregate,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if sealed.bitmap.root != content.root ||
		sealed.bitmap.pageCount != content.pageCount ||
		sealed.recordIndex != 0 ||
		fixture.core.fixedPointRegisteredWorkPhase != privateWriterFixedPointWorkActive {
		t.Fatalf("sealed aggregate = %+v", sealed)
	}
	record := &fixture.workspace.records[sealed.recordIndex]
	if !record.active || record.workUnit != request.workUnit ||
		record.output.root != content.root ||
		record.output.boundLen != content.pageLen {
		t.Fatalf("canonical record = %+v", record)
	}
	for index := 0; index < record.output.boundLen; index++ {
		binding := record.output.bindings[index]
		slot := fixture.core.pool.slots[binding.poolSlot]
		if slot.pageNumber != binding.pageNumber ||
			slot.owner != privatePageOwnerBitmap ||
			slot.origin != privatePageBitmap ||
			slot.state != privatePageInUse ||
			!slot.inUse {
			t.Fatalf("binding %d = %+v slot = %+v", index, binding, slot)
		}
	}
	if _, abortProblem := fixture.core.abort(); abortProblem.failed() {
		t.Fatal(abortProblem)
	}
}

func TestFixedPointAggregateActualRangeAndBitmapProducersSplitOneSealedScope(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	workspace := newAggregateWorkspaceForTest(t, len(fixture.core.pool.slots))
	bitmap, ranges, materialized, stagedRange := producePreparedAggregateBitmapRange(
		t, fixture.source, workspace,
	)
	bitmapContent := aggregateBitmapContent(t, bitmap, workspace)
	rangeContent := aggregateRangeContent(t, ranges, workspace)
	if bitmap.nonce == 0 || bitmap.nonce != ranges.nonce ||
		bitmapContent.pageLen == 0 || rangeContent.pageLen == 0 ||
		rangeContent.materialized != materialized ||
		rangeContent.pageLen != len(stagedRange) {
		t.Fatalf("split terminal producers = %#v/%#v", bitmapContent, rangeContent)
	}
	for index, page := range rangeContent.pages[:rangeContent.pageLen] {
		if page.owner != privatePageOwnerRange || page.origin != privatePageRange ||
			page != stagedRange[index] ||
			(index > 0 && rangeContent.pages[index-1].pageNumber >= page.pageNumber) {
			t.Fatalf("range page %d = %#v", index, page)
		}
	}
	for index, page := range bitmapContent.pages[:bitmapContent.pageLen] {
		if page.owner != privatePageOwnerBitmap || page.origin != privatePageBitmap ||
			(index > 0 && bitmapContent.pages[index-1].pageNumber >= page.pageNumber) {
			t.Fatalf("bitmap page %d = %#v", index, page)
		}
	}
	combined := make([]privateWriterProducedTerminalPage, rangeContent.pageLen+bitmapContent.pageLen)
	if problem := mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{
			rangeContent.pages[:rangeContent.pageLen],
			bitmapContent.pages[:bitmapContent.pageLen],
			nil,
		},
		combined,
	); problem.failed() {
		t.Fatalf("split terminal journals did not merge: %#v", problem)
	}
}

func TestFixedPointAggregateRangeProducerRejectsMismatchedMaterializationAndLeavesNoAuthority(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	workspace := newAggregateWorkspaceForTest(t, len(fixture.core.pool.slots))
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(t, fixture.source, 2, 1, &storage)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: make([]privateWriterProducedTerminalPage, staged.pageCount),
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	materialized.rootPage = 0
	bitmap, ranges, finalizationProblem := attachment.finalizeFixedPointBitmapRangeProducers(
		finalizationScratchForAttachment(&attachment), materialized, workspace,
	)
	if !finalizationProblem.failed() || bitmap != (privateWriterProducedBitmapTerminal{}) ||
		ranges != (privateWriterProducedRangeTerminal{}) ||
		workspace.bitmapProducer.ready || workspace.rangeProducer.ready ||
		!privateWriterProducedScratchCanonical(workspace.bitmapPages) ||
		!privateWriterProducedScratchCanonical(workspace.bitmapPrior) {
		t.Fatalf("mismatched materialization result = %#v/%#v/%#v", bitmap, ranges, finalizationProblem)
	}
}

func TestFixedPointAggregateSubstitutionRejectsBeforeConsumeAndRestoresState(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	aggregateWorkspace := newAggregateWorkspaceForTest(
		t, len(fixture.core.pool.slots),
	)
	produced := producePreparedAggregateBitmap(
		t, fixture.source, aggregateWorkspace,
	)
	content := aggregateBitmapContent(t, produced, aggregateWorkspace)
	request := fixture.request()
	request.scopePages = content.pageLen
	token, problem := fixture.core.prepareFixedPointWork(fixture.handle, request)
	if problem.failed() {
		t.Fatal(problem)
	}
	poolBefore := fixture.core.pool
	slotsBefore := append([]privatePagePoolSlot(nil), fixture.core.pool.slots...)
	recordsBefore := append(
		[]privateWriterSealedBitmapWorkUnitRecord(nil),
		fixture.workspace.records...,
	)
	slotRecordsBefore := append([]int(nil), fixture.workspace.slotRecords...)
	retirement := produceEmptyAggregateRetirement(t, aggregateWorkspace)

	forged := produced
	forged.nonce++
	if _, problem = fixture.core.prepareFixedPointAggregate(
		fixture.handle,
		token,
		forged,
		retirement,
		aggregateWorkspace,
	); problem.code != privateWriterTransactionErrFixedPoint ||
		problem.fixedPoint.code != privateWriterFixedPointErrStaleProvenance {
		t.Fatalf("substituted producer = %#v", problem)
	}
	if fixture.core.fixedPointCoordinator.predecessorUsed ||
		fixture.core.fixedPointWorkActive ||
		fixture.core.pool.registeredWorkID != 0 ||
		fixture.core.pool.mutationEpoch != poolBefore.mutationEpoch ||
		fixture.core.pool.indexRoot != poolBefore.indexRoot ||
		!reflect.DeepEqual(fixture.core.pool.slots, slotsBefore) ||
		!reflect.DeepEqual(fixture.workspace.records, recordsBefore) ||
		!reflect.DeepEqual(fixture.workspace.slotRecords, slotRecordsBefore) {
		t.Fatal("pre-consume rejection changed live or caller-owned state")
	}

	aggregate, problem := fixture.core.prepareFixedPointAggregate(
		fixture.handle,
		token,
		produced,
		retirement,
		aggregateWorkspace,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = fixture.core.executeFixedPointAggregate(
		fixture.handle, aggregate,
	); problem.failed() {
		t.Fatal(problem)
	}
	if _, abortProblem := fixture.core.abort(); abortProblem.failed() {
		t.Fatal(abortProblem)
	}
}

func TestFixedPointAggregateMandatoryPriorReturnJournalIsExplicit(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	aggregateWorkspace := newAggregateWorkspaceForTest(
		t, len(fixture.core.pool.slots),
	)
	produced := producePreparedAggregateBitmap(
		t, fixture.source, aggregateWorkspace,
	)
	content := aggregateBitmapContent(t, produced, aggregateWorkspace)
	if content.priorSealed == 0 {
		t.Fatal("actual producer omitted the mandatory prior-return journal")
	}
	if content.priorLen != 0 {
		t.Fatalf("fresh producer prior returns = %d", content.priorLen)
	}
	empty := produceEmptyAggregateRetirement(t, aggregateWorkspace)
	emptyContent, ok := empty.authority(&aggregateWorkspace.retirementProducer)
	if !ok || emptyContent.seal == 0 ||
		emptyContent.pageLen != 0 || emptyContent.priorLen != 0 {
		t.Fatalf("canonical empty retirement producer = %+v", emptyContent)
	}
}
