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
	return producePreparedAggregateBitmapAt(t, source, 20, workspace)
}

func producePreparedAggregateBitmapAt(
	t *testing.T,
	source committedPageSource,
	committedPageCount uint64,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterProducedBitmapTerminal {
	t.Helper()
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(
		t, source, committedPageCount, 2, 1, &storage,
	)
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

func prepareAggregateRangeRetirementStage(t *testing.T) (
	*freeBitmapReservationAttachment,
	*rangeRootTransactionProof,
	rangeRootRetirementStage,
	*rangeRootProofIndexes,
) {
	t.Helper()
	data, selected := ownershipWalkImage(t)
	selected.TxnID = 1
	pages := []cowSparsePage{cowLeaf(t, 2, selected.TxnID, 5, 6, 7, 9, 10)}
	for _, pageNumber := range []uint32{3, 4, 8, 11} {
		var page cowSparsePage
		page.pageNumber = pageNumber
		copy(page.bytes[:], rangeImagePage(data, int(pageNumber)))
		pages = append(pages, page)
	}
	source := &cowSparsePages{pages: pages}
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(t, source, selected.PageCount, 2, 3, &storage)
	bitmapProof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&bitmapProof); problem.failed() {
		t.Fatal(problem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	rangePages := make([]privateWriterProducedTerminalPage, staged.pageCount)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: rangePages,
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	indexes := newRangeRootProofIndexes(t, 4)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		source, selected, materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare range-root proof: %v", err)
	}
	stage, problem := stageRangeRootRetirement(
		&attachment, &proof, &rangeRootRetirementStageScratch{
			blobPages:     make([]uint32, 1),
			path:          make([]retirementPathFrame, retirementWriterPathCapacity),
			blobScanPages: make([]retirementBlobScanPage, 4),
			replacements:  make([]committedPageReplacement, 8),
			releases:      make([]uint32, 8),
			roles:         make([]pageRoleIndexSlot, 16),
		},
	)
	if problem.failed() {
		t.Fatalf("stage range-root retirement: %#v", problem)
	}
	return &attachment, &proof, stage, indexes
}

func prepareAggregateEmptyRangeRetirementStage(t *testing.T) (
	*freeBitmapReservationAttachment,
	*rangeRootTransactionProof,
	rangeRootRetirementStage,
	*rangeRootProofIndexes,
) {
	return prepareAggregateEmptyRangeRetirementStageWithReclaimed(t, 0, nil)
}

func prepareAggregateEmptyRangeRetirementStageWithReclaimed(
	t *testing.T,
	batch uint64,
	reclaimed []uint32,
) (
	*freeBitmapReservationAttachment,
	*rangeRootTransactionProof,
	rangeRootRetirementStage,
	*rangeRootProofIndexes,
) {
	t.Helper()
	selected := rangeOwnershipMeta(20, 0, 0)
	selected.TxnID = 1
	freePages := []uint32{5, 6, 7}
	if len(reclaimed) != 0 {
		// Keep one ordinary free page for the bitmap planner. The range payload
		// below is then able to use the separately proven reclaimed page, leaving
		// this selected bitmap root untouched.
		freePages = []uint32{5}
	}
	source := &cowSparsePages{pages: []cowSparsePage{
		cowLeaf(t, 2, selected.TxnID, freePages...),
	}}
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(t, source, selected.PageCount, 2, 1, &storage)
	bitmapProof := completeLateBitmapProof(t, &attachment, batch, reclaimed)
	if _, bindProblem := attachment.bind(&bitmapProof); bindProblem.failed() {
		t.Fatal(bindProblem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	rangePages := make([]privateWriterProducedTerminalPage, staged.pageCount)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: rangePages,
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	indexes := newRangeRootProofIndexes(t, 1)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		source, selected, materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare empty range-root proof: %v", err)
	}
	stage, problem := stageRangeRootRetirement(
		&attachment, &proof, &rangeRootRetirementStageScratch{
			blobPages:     make([]uint32, 1),
			path:          make([]retirementPathFrame, retirementWriterPathCapacity),
			blobScanPages: make([]retirementBlobScanPage, 4),
			replacements:  make([]committedPageReplacement, 8),
			releases:      make([]uint32, 8),
			roles:         make([]pageRoleIndexSlot, 16),
		},
	)
	if problem.failed() {
		t.Fatalf("stage empty range-root retirement: %#v", problem)
	}
	return &attachment, &proof, stage, indexes
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
	selected Meta,
) privateWriterProducedRetirementTerminal {
	t.Helper()
	produced, problem := workspace.prepareEmptyRetirementProducer(selected)
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

func newPreparedRangeAggregateFixture(t *testing.T) *preparedFixedPointFixture {
	t.Helper()
	selected := rangeOwnershipMeta(12, 8, 2)
	selected.TxnID = 1
	selected.DatabaseID = [16]byte{1}
	selected.CommitNonce = [16]byte{2}
	fixture := &preparedFixedPointFixture{}
	var workspaceProblem privateWriterWorkspaceError
	fixture.workspace, workspaceProblem = newPrivateWriterWorkspace(
		privateWriterWorkspaceBudget{
			maxBytes: 1 << 20, privatePages: 32, records: 4, preparedSlots: 2,
			scratchWordsPerSlot: 8,
		},
		privateWriterResourceBudget{
			maxHeapBytes: 1 << 20, maxPrivatePages: 32,
			maxFileGrowthPages: 32, maxOpenFiles: 4,
		},
	)
	if workspaceProblem.failed() {
		t.Fatal(workspaceProblem)
	}
	if problem := initPrivateWriterTransactionCoreWithWorkspace(
		&fixture.core, selected,
		privateWriterResourceBudget{
			maxHeapBytes: 1 << 20, maxPrivatePages: 32,
			maxFileGrowthPages: 32, maxOpenFiles: 4,
		},
		fixture.workspace,
		make([]privateWriterCleanupObligation, 2),
		make([]privateWriterCleanupOwner, 2),
	); problem.failed() {
		t.Fatal(problem)
	}
	var problem privateWriterTransactionError
	fixture.handle, problem = fixture.core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	fixture.source = &preparedWorkCallbackSource{
		cowSparsePages: &cowSparsePages{
			pages: []cowSparsePage{cowLeaf(t, 2, selected.TxnID, 5, 6, 7, 9, 10)},
		},
		core: &fixture.core, handle: fixture.handle,
	}
	if problem = fixture.core.startPreparedFixedPoint(fixture.handle, fixture.source, 2); problem.failed() {
		t.Fatal(problem)
	}
	return fixture
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
		produceEmptyAggregateRetirement(t, aggregateWorkspace, fixture.core.selected),
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

func TestFixedPointAggregateNormalPathRetainsSelectedRangeTarget(t *testing.T) {
	fixture := newPreparedRangeAggregateFixture(t)
	aggregateWorkspace := newAggregateWorkspaceForTest(t, len(fixture.core.pool.slots))
	produced := producePreparedAggregateBitmapAt(
		t, fixture.source, fixture.core.selected.PageCount, aggregateWorkspace,
	)
	content := aggregateBitmapContent(t, produced, aggregateWorkspace)
	request := privateWriterFixedPointPrepareRequest{
		workUnit: 1, expectedRoot: 2, expectedPageCount: 12, scopePages: content.pageLen,
	}
	token, problem := fixture.core.prepareFixedPointWork(fixture.handle, request)
	if problem.failed() {
		t.Fatal(problem)
	}
	selectedRangeRoot, selectedRangeRecords := fixture.core.target.RangeRoot, fixture.core.target.RangeRecordCount
	aggregate, problem := fixture.core.prepareFixedPointAggregate(
		fixture.handle,
		token,
		produced,
		produceEmptyAggregateRetirement(t, aggregateWorkspace, fixture.core.selected),
		aggregateWorkspace,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = fixture.core.executeFixedPointAggregate(fixture.handle, aggregate); problem.failed() {
		t.Fatal(problem)
	}
	if fixture.core.target.RangeRoot != selectedRangeRoot ||
		fixture.core.target.RangeRecordCount != selectedRangeRecords ||
		fixture.core.target.FreeBitmapRoot != content.root ||
		fixture.core.target.PageCount != content.pageCount {
		t.Fatalf("normal aggregate target = %#v", fixture.core.target)
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

func TestFixedPointAggregateProofBoundTripleProducerKeepsAllOwners(t *testing.T) {
	attachment, proof, stage, indexes := prepareAggregateRangeRetirementStage(t)
	if stageProblem := stage.verify(); stageProblem.failed() {
		t.Fatalf("pre-finalization stage verify: %#v", stageProblem)
	}
	workspace := newAggregateWorkspaceForTest(t, len(attachment.cow.pool.slots))
	bitmap, ranges, retirement, problem := attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	)
	if problem.failed() {
		t.Fatalf("finalize proof-bound triple producer: %#v", problem)
	}
	bitmapContent := aggregateBitmapContent(t, bitmap, workspace)
	rangeContent := aggregateRangeContent(t, ranges, workspace)
	retirementContent, ok := retirement.authority(&workspace.retirementProducer)
	if !ok {
		t.Fatal("retirement producer authority is not valid")
	}
	if bitmap.nonce == 0 || bitmap.nonce != ranges.nonce || bitmap.nonce != retirement.nonce ||
		bitmapContent.pageLen == 0 || rangeContent.pageLen == 0 ||
		retirementContent.pageLen != stage.terminalPages ||
		retirementContent.result != stage.retirement ||
		retirementContent.proofSeal != proof.seal ||
		retirementContent.protectedLen != stage.protectedLen {
		t.Fatalf("triple terminal producers = bitmap:%#v range:%#v retirement:%#v", bitmapContent, rangeContent, retirementContent)
	}
	combined := make([]privateWriterProducedTerminalPage,
		bitmapContent.pageLen+rangeContent.pageLen+retirementContent.pageLen,
	)
	if journalProblem := mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{
			rangeContent.pages[:rangeContent.pageLen],
			bitmapContent.pages[:bitmapContent.pageLen],
			retirementContent.pages[:retirementContent.pageLen],
		},
		combined,
	); journalProblem.failed() {
		t.Fatalf("three-owner terminal merge: %#v", journalProblem)
	}
	previous := uint32(0)
	for index, page := range combined {
		if index != 0 && page.pageNumber <= previous {
			t.Fatalf("merged page order at %d: %d <= %d", index, page.pageNumber, previous)
		}
		previous = page.pageNumber
	}
	for _, page := range retirementContent.pages[:retirementContent.pageLen] {
		if page.owner != privatePageOwnerRetirement ||
			(page.origin != privatePageRetirementTree && page.origin != privatePageRetirementBlob) {
			t.Fatalf("retirement terminal page = %#v", page)
		}
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestFixedPointAggregateTripleProducerRejectsDirtyRetirementScratchBeforeFinalization(t *testing.T) {
	attachment, proof, stage, indexes := prepareAggregateRangeRetirementStage(t)
	workspace := newAggregateWorkspaceForTest(t, len(attachment.cow.pool.slots))
	workspace.retirementPages[0].pageNumber = 99
	before := snapshotLateBitmapLive(t, attachment)
	bitmap, ranges, retirement, problem := attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	)
	if problem.code != privateWriterFixedPointErrScratchTooSmall ||
		bitmap != (privateWriterProducedBitmapTerminal{}) ||
		ranges != (privateWriterProducedRangeTerminal{}) ||
		retirement != (privateWriterProducedRetirementTerminal{}) {
		t.Fatalf("dirty retirement scratch = %#v/%#v/%#v/%#v", bitmap, ranges, retirement, problem)
	}
	requireLateBitmapLiveSnapshot(t, attachment, before)
	if stageProblem := stage.verify(); stageProblem.failed() {
		t.Fatalf("dirty-scratch stage retry authority: %#v", stageProblem)
	}
	clear(workspace.retirementPages)
	if _, _, _, problem = attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	); problem.failed() {
		t.Fatalf("dirty-scratch retry: %#v", problem)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestFixedPointAggregateTripleProducerSupportsLegalEmptySelectedRangeRoot(t *testing.T) {
	attachment, proof, stage, indexes := prepareAggregateEmptyRangeRetirementStage(t)
	workspace := newAggregateWorkspaceForTest(t, len(attachment.cow.pool.slots))
	bitmap, ranges, retirement, problem := attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	)
	if problem.failed() {
		t.Fatalf("finalize legal empty range-root triple: %#v", problem)
	}
	bitmapContent := aggregateBitmapContent(t, bitmap, workspace)
	rangeContent := aggregateRangeContent(t, ranges, workspace)
	retirementContent, ok := retirement.authority(&workspace.retirementProducer)
	if !ok || bitmap.nonce == 0 || bitmap.nonce != ranges.nonce ||
		bitmap.nonce != retirement.nonce || bitmapContent.pageLen == 0 ||
		rangeContent.pageLen == 0 || retirementContent.pageLen != 0 ||
		retirementContent.result.root != 0 || retirementContent.result.batchCount != 0 ||
		retirementContent.protectedLen != 0 {
		t.Fatalf("legal empty range-root producers = bitmap(root:%d selected:%d pages:%d) range(root:%d pages:%d) retirement(root:%d pages:%d)",
			bitmapContent.root, bitmapContent.selectedRoot, bitmapContent.pageLen,
			rangeContent.materialized.rootPage, rangeContent.pageLen,
			retirementContent.result.root, retirementContent.pageLen,
		)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestFixedPointAggregateTripleProducerRetainsAnUnchangedSelectedBitmapRoot(t *testing.T) {
	attachment, proof, stage, indexes := prepareAggregateEmptyRangeRetirementStageWithReclaimed(
		t, 2, []uint32{10},
	)
	workspace := newAggregateWorkspaceForTest(t, len(attachment.cow.pool.slots))
	bitmap, ranges, retirement, problem := attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	)
	if problem.failed() {
		t.Fatalf("finalize unchanged-bitmap-root triple: %#v", problem)
	}
	bitmapContent := aggregateBitmapContent(t, bitmap, workspace)
	rangeContent := aggregateRangeContent(t, ranges, workspace)
	retirementContent, ok := retirement.authority(&workspace.retirementProducer)
	if !ok || bitmapContent.root != 2 || bitmapContent.selectedRoot != 2 ||
		bitmapContent.pageLen != 0 || rangeContent.pageLen == 0 ||
		retirementContent.pageLen != 0 || stage.terminalPages != 0 {
		t.Fatalf("unchanged bitmap root producers = bitmap(root:%d selected:%d pages:%d) range(root:%d pages:%d) retirement(root:%d pages:%d)",
			bitmapContent.root, bitmapContent.selectedRoot, bitmapContent.pageLen,
			rangeContent.materialized.rootPage, rangeContent.pageLen,
			retirementContent.result.root, retirementContent.pageLen,
		)
	}
	combined := make([]privateWriterProducedTerminalPage, rangeContent.pageLen)
	if journalProblem := mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{
			rangeContent.pages[:rangeContent.pageLen],
			bitmapContent.pages[:bitmapContent.pageLen],
			retirementContent.pages[:retirementContent.pageLen],
		},
		combined,
	); journalProblem.failed() || len(combined) == 0 ||
		combined[0].owner != privatePageOwnerRange {
		t.Fatalf("unchanged bitmap root journal = %#v/%#v", combined, journalProblem)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestFixedPointAggregateProofBoundTripleCoordinatorRetainsPrivateRange(t *testing.T) {
	attachment, proof, stage, indexes := prepareAggregateRangeRetirementStage(t)
	workspace := newAggregateWorkspaceForTest(t, 32)
	bitmap, ranges, retirement, producerProblem := attachment.finalizeFixedPointBitmapRangeRetirementProducers(
		finalizationScratchForAttachment(attachment), &stage, workspace,
	)
	if producerProblem.failed() {
		t.Fatalf("finalize proof-bound triple producer: %#v", producerProblem)
	}
	bitmapContent := aggregateBitmapContent(t, bitmap, workspace)
	rangeContent := aggregateRangeContent(t, ranges, workspace)
	retirementContent, ok := retirement.authority(&workspace.retirementProducer)
	if !ok {
		t.Fatal("retirement producer authority is not valid")
	}
	fixture := newPreparedRangeAggregateFixture(t)
	base, problem := fixture.core.prepareFixedPointWork(
		fixture.handle,
		privateWriterFixedPointPrepareRequest{
			workUnit: 1, expectedRoot: 2, expectedPageCount: 12,
			scopePages: bitmapContent.pageLen + rangeContent.pageLen + retirementContent.pageLen,
		},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	forgedProof := *proof
	forgedProof.seal++
	if _, problem = fixture.core.prepareFixedPointRangeAggregate(
		fixture.handle, base, bitmap, ranges, &forgedProof, retirement, workspace,
	); problem.code != privateWriterTransactionErrFixedPoint ||
		problem.fixedPoint.code != privateWriterFixedPointErrStaleProvenance ||
		fixture.core.fixedPointWorkActive || fixture.core.pool.registeredWorkID != 0 {
		t.Fatalf("forged proof aggregate = %#v", problem)
	}
	aggregate, problem := fixture.core.prepareFixedPointRangeAggregate(
		fixture.handle, base, bitmap, ranges, proof, retirement, workspace,
	)
	if problem.failed() {
		t.Fatalf("prepare proof-bound aggregate: %#v", problem)
	}
	sealed, problem := fixture.core.executeFixedPointAggregate(fixture.handle, aggregate)
	if problem.failed() {
		t.Fatalf("execute proof-bound aggregate: %#v", problem)
	}
	if sealed.rangeResult != proof.materialized || sealed.rangeProof != proof ||
		sealed.retirement != stage.retirement ||
		fixture.core.target.PageCount != bitmapContent.pageCount ||
		fixture.core.target.FreeBitmapRoot != bitmapContent.root ||
		fixture.core.target.RangeRoot != rangeContent.materialized.rootPage ||
		fixture.core.target.RangeRecordCount != rangeContent.materialized.recordCount ||
		fixture.core.target.RetirementRoot != retirementContent.result.root ||
		fixture.core.target.RetirementBatchCount != retirementContent.result.batchCount {
		t.Fatalf("private triple aggregate result = %#v target = %#v", sealed, fixture.core.target)
	}
	owners := map[privatePageOwner]int{}
	record := &fixture.workspace.records[sealed.recordIndex]
	for _, binding := range record.output.bindings[:record.output.boundLen] {
		owners[fixture.core.pool.slots[binding.poolSlot].owner]++
	}
	if owners[privatePageOwnerBitmap] != bitmapContent.pageLen ||
		owners[privatePageOwnerRange] != rangeContent.pageLen ||
		owners[privatePageOwnerRetirement] != retirementContent.pageLen {
		t.Fatalf("coordinator owner counts = %#v", owners)
	}
	if _, abortProblem := fixture.core.abort(); abortProblem.failed() {
		t.Fatal(abortProblem)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestFixedPointAggregateRangeBitmapPairRejectsRetirementOwner(t *testing.T) {
	attachment, proof, _, indexes := prepareAggregateRangeRetirementStage(t)
	workspace := newAggregateWorkspaceForTest(t, len(attachment.cow.pool.slots))
	bitmap, ranges, problem := attachment.finalizeFixedPointBitmapRangeProducers(
		finalizationScratchForAttachment(attachment), proof.materialized, workspace,
	)
	if !problem.failed() || bitmap != (privateWriterProducedBitmapTerminal{}) ||
		ranges != (privateWriterProducedRangeTerminal{}) || workspace.bitmapProducer.ready ||
		workspace.rangeProducer.ready {
		t.Fatalf("two-owner path accepted retirement scope: bitmap:%#v range:%#v problem:%#v", bitmap, ranges, problem)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
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
	retirement := produceEmptyAggregateRetirement(t, aggregateWorkspace, fixture.core.selected)

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

func TestFixedPointAggregateRejectsSubstitutedTargetBeforeConsume(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	aggregateWorkspace := newAggregateWorkspaceForTest(t, len(fixture.core.pool.slots))
	produced := producePreparedAggregateBitmap(t, fixture.source, aggregateWorkspace)
	content := aggregateBitmapContent(t, produced, aggregateWorkspace)
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
		produceEmptyAggregateRetirement(t, aggregateWorkspace, fixture.core.selected),
		aggregateWorkspace,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	poolBefore := fixture.core.pool
	slotsBefore := append([]privatePagePoolSlot(nil), fixture.core.pool.slots...)
	fixture.core.target.MetadataRoot = 1
	if _, problem = fixture.core.executeFixedPointAggregate(fixture.handle, aggregate); problem.code != privateWriterTransactionErrFixedPoint ||
		problem.fixedPoint.code != privateWriterFixedPointErrStaleProvenance ||
		fixture.core.fixedPointWorkActive || fixture.core.pool.registeredWorkID != 0 ||
		fixture.core.pool.mutationEpoch != poolBefore.mutationEpoch ||
		!reflect.DeepEqual(fixture.core.pool.slots, slotsBefore) {
		t.Fatalf("substituted target execute = %#v", problem)
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
	empty := produceEmptyAggregateRetirement(t, aggregateWorkspace, fixture.core.selected)
	emptyContent, ok := empty.authority(&aggregateWorkspace.retirementProducer)
	if !ok || emptyContent.seal == 0 ||
		emptyContent.pageLen != 0 || emptyContent.priorLen != 0 {
		t.Fatalf("canonical empty retirement producer = %+v", emptyContent)
	}
}
