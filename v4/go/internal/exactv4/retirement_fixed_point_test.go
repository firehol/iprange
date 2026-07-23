package exactv4

import "testing"

type retirementFixedPointFixture struct {
	pool       privatePagePool
	slots      []privatePagePoolSlot
	priorScope privatePageReservationScope
	workScope  privatePageReservationScope
	source     privateWriterDraftPageSource
	arena      privatePageArena
}

func newRetirementFixedPointFixture(t *testing.T) *retirementFixedPointFixture {
	t.Helper()
	fixture := &retirementFixedPointFixture{
		slots: make([]privatePagePoolSlot, 8),
	}
	if problem := initVacantPrivatePagePool(
		&fixture.pool, fixture.slots, 100, 100, 4,
	); problem.failed() {
		t.Fatal(problem)
	}
	var poolProblem privatePagePoolError
	fixture.priorScope, poolProblem = fixture.pool.reserveScope(2)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	fixture.workScope, poolProblem = fixture.pool.reserveScope(4)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	checkpoint, poolProblem := fixture.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	for _, binding := range []struct {
		scope privatePageReservationScope
		page  uint32
	}{
		{fixture.priorScope, 3},
		{fixture.priorScope, 4},
		{fixture.workScope, 10},
		{fixture.workScope, 11},
		{fixture.workScope, 12},
		{fixture.workScope, 13},
	} {
		if _, poolProblem = fixture.pool.bindPage(
			checkpoint, binding.scope, binding.page, privatePageReclaimed,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
	}
	treeToken, poolProblem := fixture.pool.claimPageInScope(
		checkpoint, fixture.priorScope, 3,
		privatePageOwnerRetirement, privatePageRetirementTree,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	blobToken, poolProblem := fixture.pool.claimPageInScope(
		checkpoint, fixture.priorScope, 4,
		privatePageOwnerRetirement, privatePageRetirementBlob,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	var tree, blob [PageSize]byte
	encodeRetirementLeafPage(&tree, 4, 1, func(int) retirementBatch {
		return retirementBatch{
			retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 4,
		}
	})
	encodeRetirementBlobLeaf(&blob, 4, 0, []uint32{5})
	if poolProblem = fixture.pool.writePage(treeToken, &tree); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = fixture.pool.writePage(blobToken, &blob); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = fixture.pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	fixture.pool.slots[fixture.priorScope.anchor].scopeSealed = true

	bindings := make([]bitmapCOWArenaBinding, 2)
	nodes := make([]bitmapCOWIndexNode, 2)
	root := bitmapCOWNoIndex
	length := 0
	for index, pageNumber := range []uint32{3, 4} {
		slot, found := fixture.pool.slotIndex(pageNumber)
		if !found {
			t.Fatalf("missing prior page %d", pageNumber)
		}
		bindings[index] = bitmapCOWArenaBinding{
			poolSlot: slot, poolEpoch: fixture.pool.slots[slot].epoch,
			pageNumber: pageNumber, storageNode: index, activeNode: index,
			bound: true,
		}
		inserted := pageIndexInsert(
			nodes, &root, &length, pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageArena, slot: index},
		)
		if !inserted {
			t.Fatalf("duplicate prior page %d", pageNumber)
		}
	}
	records := make([]privateWriterSealedBitmapWorkUnitRecord, 1)
	records[0] = privateWriterSealedBitmapWorkUnitRecord{
		workUnit: 1,
		output: sealedFreeBitmapOutput{
			selectedTxn: 3, pendingTxn: 4,
			committedPageCount: 100, pageCount: 100,
			pool: &fixture.pool, scope: fixture.priorScope,
			bindings: bindings, boundLen: len(bindings),
			indexNodes: nodes, indexRoot: root,
		},
		active: true,
	}
	fixture.source = privateWriterDraftPageSource{
		selected: &retirementWriterTestSource{
			data: retirementWriterImage(100), pageCount: 100,
		},
		pool:        &fixture.pool,
		records:     records,
		slotRecords: make([]int, len(fixture.pool.slots)),
	}
	if problem := fixture.source.installRecordSlots(0); problem.failed() {
		t.Fatal(problem)
	}
	var arenaProblem retirementWriteError
	fixture.arena, arenaProblem = newPrivatePageArenaInScope(
		&fixture.pool, fixture.workScope, 4,
	)
	if arenaProblem.failed() {
		t.Fatal(arenaProblem)
	}
	return fixture
}

func (f *retirementFixedPointFixture) useCommittedBlob(t *testing.T) {
	t.Helper()
	var tree [PageSize]byte
	encodeRetirementLeafPage(&tree, 4, 1, func(int) retirementBatch {
		return retirementBatch{
			retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 5,
		}
	})
	treeSlot, found := f.pool.slotIndex(3)
	if !found {
		t.Fatal("missing prior tree")
	}
	f.pool.slots[treeSlot].bytes = tree
	selected := f.source.selected.(*retirementWriterTestSource)
	encodeRetirementBlobLeaf(
		(*[PageSize]byte)(retirementWriterPage(selected.data, 5)),
		3, 0, []uint32{6},
	)
}

func TestRetirementFixedPointReturnsPriorTreeAndBlobWithoutRetiringThem(t *testing.T) {
	fixture := newRetirementFixedPointFixture(t)
	replacements := newCommittedReplacementLedger(
		make([]committedPageReplacement, 4),
	)
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 12))
	result, problem := deleteOldestRetirementPrefixInScope(
		&fixture.source,
		retirementTreeState{
			selectedTxn: 3, pageCount: 100, root: 3, batchCount: 1,
		},
		1,
		&fixture.arena,
		make([]retirementPathFrame, retirementWriterPathCapacity),
		writerScanScratch(),
		&replacements,
		&releases,
		&roles,
	)
	if problem.failed() {
		t.Fatalf("%#v", problem)
	}
	if result.root != 0 || result.batchCount != 0 ||
		result.committedReplacements != 0 || replacements.length != 0 {
		t.Fatalf("result=%#v replacements=%#v", result, replacements.used())
	}
	for _, pageNumber := range []uint32{3, 4} {
		slotIndex, found := fixture.pool.slotIndex(pageNumber)
		if !found {
			t.Fatalf("returned prior page %d lost its exact binding", pageNumber)
		}
		slot := &fixture.pool.slots[slotIndex]
		if slot.state != privatePageAvailable || slot.inUse ||
			slot.scopeID != fixture.priorScope.id ||
			slot.scopeAnchorIndex != fixture.priorScope.anchor {
			t.Fatalf("prior page %d = %#v", pageNumber, slot)
		}
		if fixture.source.slotRecords[slotIndex] != 0 {
			t.Fatalf("prior page %d remains live", pageNumber)
		}
	}
	if !fixture.pool.slots[fixture.priorScope.anchor].scopeSealed {
		t.Fatal("prior scope was reopened")
	}
}

func TestRetirementFixedPointSeparatesCommittedAndPriorDisposition(t *testing.T) {
	fixture := newRetirementFixedPointFixture(t)
	fixture.useCommittedBlob(t)
	replacements := newCommittedReplacementLedger(
		make([]committedPageReplacement, 4),
	)
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 12))
	result, problem := deleteOldestRetirementPrefixInScope(
		&fixture.source,
		retirementTreeState{
			selectedTxn: 3, pageCount: 100, root: 3, batchCount: 1,
		},
		1,
		&fixture.arena,
		make([]retirementPathFrame, retirementWriterPathCapacity),
		writerScanScratch(),
		&replacements,
		&releases,
		&roles,
	)
	if problem.failed() {
		t.Fatalf("%#v", problem)
	}
	if result.committedReplacements != 1 || replacements.length != 1 ||
		replacements.entries[0] != (committedPageReplacement{
			pageNumber: 5, origin: committedPageRetirementBlob,
		}) {
		t.Fatalf("committed disposition = %#v %#v", result, replacements.used())
	}
	treeSlot, _ := fixture.pool.slotIndex(3)
	if fixture.pool.slots[treeSlot].state != privatePageAvailable ||
		fixture.source.slotRecords[treeSlot] != 0 {
		t.Fatal("prior retirement tree was not returned exactly")
	}
	blobSlot, _ := fixture.pool.slotIndex(4)
	if fixture.pool.slots[blobSlot].state != privatePageInUse ||
		fixture.source.slotRecords[blobSlot] == 0 {
		t.Fatal("unreferenced prior page was incorrectly returned")
	}
}

func TestRetirementFixedPointPlanningFailurePreservesPriorScope(t *testing.T) {
	fixture := newRetirementFixedPointFixture(t)
	fixture.useCommittedBlob(t)
	before := scopedRetirementForeignSnapshot(&fixture.pool, fixture.priorScope)
	replacements := newCommittedReplacementLedger(nil)
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 12))
	_, problem := deleteOldestRetirementPrefixInScope(
		&fixture.source,
		retirementTreeState{
			selectedTxn: 3, pageCount: 100, root: 3, batchCount: 1,
		},
		1,
		&fixture.arena,
		make([]retirementPathFrame, retirementWriterPathCapacity),
		writerScanScratch(),
		&replacements,
		&releases,
		&roles,
	)
	if problem.code != retirementWriteErrReplacementLedgerTooSmall {
		t.Fatalf("short replacement ledger = %#v", problem)
	}
	after := scopedRetirementForeignSnapshot(&fixture.pool, fixture.priorScope)
	if len(before) != len(after) {
		t.Fatalf("prior scope length changed: %d != %d", len(before), len(after))
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("prior scope slot %d changed", index)
		}
	}
	treeSlot, _ := fixture.pool.slotIndex(3)
	blobSlot, _ := fixture.pool.slotIndex(4)
	if fixture.source.slotRecords[treeSlot] == 0 ||
		fixture.source.slotRecords[blobSlot] == 0 {
		t.Fatal("failed planning changed prior provenance")
	}
}

func TestRetirementFixedPointSourceFailurePreservesPriorScope(t *testing.T) {
	fixture := newRetirementFixedPointFixture(t)
	selected := fixture.source.selected.(*retirementWriterTestSource)
	selected.failAccess = 1
	before := scopedRetirementForeignSnapshot(&fixture.pool, fixture.priorScope)
	replacements := newCommittedReplacementLedger(
		make([]committedPageReplacement, 4),
	)
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 12))
	_, problem := deleteOldestRetirementPrefixInScope(
		&fixture.source,
		retirementTreeState{
			selectedTxn: 3, pageCount: 100, root: 3, batchCount: 1,
		},
		1,
		&fixture.arena,
		make([]retirementPathFrame, retirementWriterPathCapacity),
		writerScanScratch(),
		&replacements,
		&releases,
		&roles,
	)
	if problem.code != retirementWriteErrSource {
		t.Fatalf("source failure = %#v", problem)
	}
	after := scopedRetirementForeignSnapshot(&fixture.pool, fixture.priorScope)
	if len(before) != len(after) {
		t.Fatalf("prior scope length changed: %d != %d", len(before), len(after))
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("prior scope slot %d changed", index)
		}
	}
	treeSlot, _ := fixture.pool.slotIndex(3)
	blobSlot, _ := fixture.pool.slotIndex(4)
	if fixture.source.slotRecords[treeSlot] == 0 ||
		fixture.source.slotRecords[blobSlot] == 0 {
		t.Fatal("source failure changed prior provenance")
	}
}

func TestRetirementFixedPointRejectsStalePriorReferenceBeforeApply(t *testing.T) {
	fixture := newRetirementFixedPointFixture(t)
	replacements := newCommittedReplacementLedger(
		make([]committedPageReplacement, 4),
	)
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 12))
	plan, problem := planScopedRetirementDelete(
		&fixture.source,
		retirementTreeState{
			selectedTxn: 3, pageCount: 100, root: 3, batchCount: 1,
		},
		1,
		&fixture.arena,
		make([]retirementPathFrame, retirementWriterPathCapacity),
		writerScanScratch(),
		&replacements,
		&releases,
		&roles,
	)
	if problem.failed() {
		t.Fatalf("plan = %#v", problem)
	}
	residence, fixedProblem := fixture.source.residence(3)
	if fixedProblem.failed() {
		t.Fatal(fixedProblem)
	}
	if fixedProblem = fixture.source.returnPriorPrivate(
		residence.provenance,
	); fixedProblem.failed() {
		t.Fatal(fixedProblem)
	}
	workBefore := scopedRetirementForeignSnapshot(
		&fixture.pool, fixture.workScope,
	)
	if _, problem = plan.apply(); problem.code != retirementWriteErrStaleEditPlan {
		t.Fatalf("stale prior reference = %#v", problem)
	}
	workAfter := scopedRetirementForeignSnapshot(
		&fixture.pool, fixture.workScope,
	)
	if len(workBefore) != len(workAfter) {
		t.Fatalf("work scope length changed: %d != %d", len(workBefore), len(workAfter))
	}
	for index := range workBefore {
		if workBefore[index] != workAfter[index] {
			t.Fatalf("work scope slot %d changed", index)
		}
	}
}
