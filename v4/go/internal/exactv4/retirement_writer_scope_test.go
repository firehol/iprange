package exactv4

import (
	"reflect"
	"testing"
)

type scopedRetirementFixture struct {
	pool         privatePagePool
	slots        []privatePagePoolSlot
	foreignScope privatePageReservationScope
	workScope    privatePageReservationScope
	arena        privatePageArena
}

func newScopedRetirementFixture(
	t *testing.T,
	foreignPages, workPages []uint32,
	workCapacity int,
) *scopedRetirementFixture {
	return newScopedRetirementFixtureAtTxn(t, foreignPages, workPages, workCapacity, 2)
}

func newScopedRetirementFixtureAtTxn(
	t *testing.T,
	foreignPages, workPages []uint32,
	workCapacity int,
	pendingTxn uint64,
) *scopedRetirementFixture {
	t.Helper()
	capacity := len(foreignPages) + workCapacity
	fixture := &scopedRetirementFixture{slots: make([]privatePagePoolSlot, capacity)}
	pageCount := uint64(100)
	for _, pages := range [][]uint32{foreignPages, workPages} {
		for _, page := range pages {
			if uint64(page)+1 > pageCount {
				pageCount = uint64(page) + 1
			}
		}
	}
	if problem := initVacantPrivatePagePool(&fixture.pool, fixture.slots, pageCount, pageCount, pendingTxn); problem.failed() {
		t.Fatal(problem)
	}
	var problem privatePagePoolError
	if len(foreignPages) != 0 {
		fixture.foreignScope, problem = fixture.pool.reserveScope(len(foreignPages))
		if problem.failed() {
			t.Fatal(problem)
		}
	}
	fixture.workScope, problem = fixture.pool.reserveScope(workCapacity)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := fixture.pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for index := len(foreignPages) - 1; index >= 0; index-- {
		if _, problem = fixture.pool.bindPage(checkpoint, fixture.foreignScope, foreignPages[index], privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
	}
	for index := len(workPages) - 1; index >= 0; index-- {
		if _, problem = fixture.pool.bindPage(checkpoint, fixture.workScope, workPages[index], privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem = fixture.pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	var arenaProblem retirementWriteError
	fixture.arena, arenaProblem = newPrivatePageArenaInScope(&fixture.pool, fixture.workScope, pendingTxn)
	if arenaProblem.failed() {
		t.Fatal(arenaProblem)
	}
	return fixture
}

type scopedRetirementForeignState struct {
	bound           bool
	pageNumber      uint32
	authorization   privatePageAuthorization
	scopeID         uint64
	state           privatePageState
	owner           privatePageOwner
	origin          privatePageOrigin
	pendingTxn      uint64
	generation      uint64
	epoch           uint64
	committedOrigin uint32
	bytes           [PageSize]byte
}

type scopedRetirementMutationSource struct {
	base               immutableSlicePageSource
	pool               *privatePagePool
	expectedPoolHash   uint64
	callbacks          int
	readCallbacks      int
	checkpointObserved bool
	poolDriftObserved  bool
	mutateCheck        func()
	mutateRead         func()
}

func (s *scopedRetirementMutationSource) observe() {
	s.callbacks++
	if s.pool.activeCheckpointID != 0 {
		s.checkpointObserved = true
	}
	if retirementPoolHash(s.pool) != s.expectedPoolHash {
		s.poolDriftObserved = true
	}
}

func (s *scopedRetirementMutationSource) checkAccessStatus() pageSourceStatus {
	s.observe()
	status := s.base.checkAccessStatus()
	if s.mutateCheck != nil {
		s.mutateCheck()
	}
	return status
}

func (s *scopedRetirementMutationSource) readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	s.observe()
	s.readCallbacks++
	status := s.base.readPageStatus(pageNumber, destination)
	if s.mutateRead != nil {
		s.mutateRead()
	}
	return status
}

func scopedRetirementForeignSnapshot(pool *privatePagePool, scope privatePageReservationScope) []scopedRetirementForeignState {
	result := make([]scopedRetirementForeignState, 0)
	for index := range pool.slots {
		slot := &pool.slots[index]
		if slot.scopeID != scope.id {
			continue
		}
		result = append(result, scopedRetirementForeignState{
			bound: slot.bound, pageNumber: slot.pageNumber, authorization: slot.authorization,
			scopeID: slot.scopeID, state: slot.state, owner: slot.owner, origin: slot.origin,
			pendingTxn: slot.pendingTxn, generation: slot.generation, epoch: slot.epoch,
			committedOrigin: slot.committedOrigin, bytes: slot.bytes,
		})
	}
	return result
}

func claimScopedRetirementPage(
	t *testing.T,
	pool *privatePagePool,
	scope privatePageReservationScope,
	page uint32,
	origin privatePageOrigin,
	marker byte,
) privatePageToken {
	t.Helper()
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	token, problem := pool.claimPageInScope(checkpoint, scope, page, privatePageOwnerRetirement, origin)
	if problem.failed() {
		t.Fatal(problem)
	}
	var bytes [PageSize]byte
	bytes[0], bytes[PageSize-1] = marker, marker^0xff
	if problem = pool.writePage(token, &bytes); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	return token
}

func TestScopedRetirementUsesOwnAscendingBoundPages(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3, 11}, []uint32{12, 10}, 2)
	foreignBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope)
	scratch := blobBuildScratch{pageNumbers: make([]uint32, 1)}
	token, problem := buildRetirementBlob([]uint32{2, 7}, &fixture.arena, &scratch)
	if problem.failed() {
		t.Fatal(problem)
	}
	if token.root != 10 {
		t.Fatalf("root = %d, want lowest work-scope page 10", token.root)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("retirement allocation changed the foreign scope")
	}
	if problem = token.discard(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestScopedRetirementRejectsLegacyGlobalArena(t *testing.T) {
	fixture := newScopedRetirementFixture(t, nil, []uint32{10}, 1)
	if _, problem := newPrivatePageArenaWithPool(&fixture.pool, 2); problem.code != retirementWriteErrPrivateScopeMismatch {
		t.Fatalf("legacy global arena with active scope = %#v", problem)
	}
}

func TestScopedRetirementExhaustionIgnoresForeignCapacity(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10}, 1)
	checkpoint, problem := fixture.pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = fixture.pool.claimPageInScope(
		checkpoint, fixture.workScope, 10, privatePageOwnerBitmap, privatePageBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if problem := fixture.arena.requirePages(1); problem.code != retirementWriteErrPrivatePageBudgetTooSmall ||
		problem.required != 2 || problem.actual != 1 || problem.scopeCapacity != 1 ||
		problem.scopeAvailable != 0 || problem.scopeInUse != 1 {
		t.Fatalf("own-scope exhaustion = %#v", problem)
	}
	if available, _ := fixture.pool.scopedAvailable(fixture.foreignScope); available != 1 {
		t.Fatalf("foreign availability = %d, want 1", available)
	}
}

func TestScopedRetirementUpsertAndDeleteEndToEnd(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10, 11, 12}, 3)
	foreignBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope)
	source := &retirementWriterTestSource{pageCount: fixture.arena.committedPageCount}
	path := make([]retirementPathFrame, retirementWriterPathCapacity)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
	releases := newPrivateReleaseBuffer(make([]uint32, 8))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	blobScratch := writerScanScratch()
	token, problem := buildRetirementBlob(
		[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	inserted, problem := upsertNewestRetirementInScope(
		source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
		&token, path, blobScratch, &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if inserted.root != 11 || inserted.batchCount != 1 || fixture.pool.activeCheckpointID != 0 {
		t.Fatalf("scoped insert = %#v checkpoint=%d", inserted, fixture.pool.activeCheckpointID)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("scoped upsert changed the foreign scope")
	}

	deleted, problem := deleteOldestRetirementPrefixInScope(
		source,
		retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount, root: inserted.root, batchCount: 1},
		1, &fixture.arena, path, blobScratch, &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if deleted.root != 0 || deleted.batchCount != 0 || fixture.arena.inUseCount() != 0 || fixture.pool.activeCheckpointID != 0 {
		t.Fatalf("scoped delete = %#v in-use=%d checkpoint=%d", deleted, fixture.arena.inUseCount(), fixture.pool.activeCheckpointID)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("scoped delete changed the foreign scope")
	}
}

func TestScopedRetirementCombinedUsesVirtualDeleteOutput(t *testing.T) {
	fixture := newScopedRetirementFixtureAtTxn(t, []uint32{15}, []uint32{10, 11, 12, 13}, 4, 4)
	foreignBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope)
	image := retirementWriterImage(fixture.arena.committedPageCount)
	putWriterBlobLeaf(retirementWriterPage(image, 3), 3, 0, []uint32{8})
	putWriterBlobLeaf(retirementWriterPage(image, 4), 3, 0, []uint32{9})
	putWriterRetirementLeaf(retirementWriterPage(image, 2), 3, []retirementBatch{
		{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3},
		{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 4},
	})
	source := &retirementWriterTestSource{data: image, pageCount: fixture.arena.committedPageCount}
	token, problem := buildRetirementBlob(
		[]uint32{2, 3}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	deletePath := make([]retirementPathFrame, retirementWriterPathCapacity)
	upsertPath := make([]retirementPathFrame, retirementWriterPathCapacity)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
	releases := newPrivateReleaseBuffer(make([]uint32, 8))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
	result, problem := deleteOldestAndUpsertNewestRetirementInScope(
		source,
		retirementTreeState{selectedTxn: 3, pageCount: fixture.arena.committedPageCount, root: 2, batchCount: 2},
		1, &token, deletePath, upsertPath, writerScanScratch(), &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if result.root != 11 || result.batchCount != 2 || result.privatePages != 1 || result.committedReplacements != 2 {
		t.Fatalf("combined result = %#v", result)
	}
	if fixture.arena.inUseCount() != 2 || fixture.pool.activeCheckpointID != 0 {
		t.Fatalf("combined in-use=%d checkpoint=%d", fixture.arena.inUseCount(), fixture.pool.activeCheckpointID)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("combined edit changed the foreign scope")
	}
}

func TestScopedRetirementRejectsEveryCallbackMutationAndRetries(t *testing.T) {
	tests := []struct {
		name    string
		binding retirementEditBinding
		mutate  func(*privatePageArena, []retirementPathFrame, []retirementPathFrame, *retirementBlobScanScratch, *committedReplacementLedger, *privateReleaseBuffer, *pageRoleIndex, *retirementBlobToken)
	}{
		{name: "arena scope", binding: retirementEditBindingArena, mutate: func(arena *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			arena.scope.id++
		}},
		{name: "delete path", binding: retirementEditBindingDeletePath, mutate: func(_ *privatePageArena, path []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			path[0].keepFrom++
		}},
		{name: "upsert path", binding: retirementEditBindingUpsertPath, mutate: func(_ *privatePageArena, _ []retirementPathFrame, path []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			path[0].keepFrom++
		}},
		{name: "replacement ledger", binding: retirementEditBindingReplacementLedger, mutate: func(_ *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, ledger *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			ledger.length++
		}},
		{name: "release ledger", binding: retirementEditBindingReleaseLedger, mutate: func(_ *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, releases *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			releases.length++
		}},
		{name: "roles", binding: retirementEditBindingRoles, mutate: func(_ *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, roles *pageRoleIndex, _ *retirementBlobToken) {
			roles.referenceEpoch++
		}},
		{name: "blob scratch", binding: retirementEditBindingBlobScratch, mutate: func(_ *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, scratch *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, _ *retirementBlobToken) {
			scratch.pages = nil
		}},
		{name: "blob token", binding: retirementEditBindingBlobToken, mutate: func(_ *privatePageArena, _ []retirementPathFrame, _ []retirementPathFrame, _ *retirementBlobScanScratch, _ *committedReplacementLedger, _ *privateReleaseBuffer, _ *pageRoleIndex, token *retirementBlobToken) {
			token.root++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedRetirementFixtureAtTxn(t, []uint32{15}, []uint32{10, 11, 12, 13}, 4, 4)
			image := retirementWriterImage(fixture.arena.committedPageCount)
			putWriterBlobLeaf(retirementWriterPage(image, 3), 3, 0, []uint32{8})
			putWriterBlobLeaf(retirementWriterPage(image, 4), 3, 0, []uint32{9})
			putWriterRetirementLeaf(retirementWriterPage(image, 2), 3, []retirementBatch{
				{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3},
				{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 4},
			})
			token, problem := buildRetirementBlob(
				[]uint32{2, 3}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			deletePath := make([]retirementPathFrame, retirementWriterPathCapacity)
			upsertPath := make([]retirementPathFrame, retirementWriterPathCapacity)
			replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
			releases := newPrivateReleaseBuffer(make([]uint32, 8))
			roles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
			blobScratch := writerScanScratch()
			poolBefore := retirementPoolHash(&fixture.pool)
			source := &scopedRetirementMutationSource{
				base: newImmutableSlicePageSource(image, fixture.arena.committedPageCount),
				pool: &fixture.pool, expectedPoolHash: poolBefore,
			}
			source.mutateRead = func() {
				test.mutate(&fixture.arena, deletePath, upsertPath, blobScratch, &replacements, &releases, &roles, &token)
				source.mutateRead = nil
			}
			_, problem = planScopedRetirementCombined(
				source,
				retirementTreeState{selectedTxn: 3, pageCount: fixture.arena.committedPageCount, root: 2, batchCount: 2},
				1, &token, deletePath, upsertPath, blobScratch, &replacements, &releases, &roles,
			)
			if problem.code != retirementWriteErrStaleEditPlan || problem.binding != test.binding {
				t.Fatalf("callback mutation = %#v", problem)
			}
			if source.checkpointObserved || source.poolDriftObserved || fixture.pool.activeCheckpointID != 0 || retirementPoolHash(&fixture.pool) != poolBefore {
				t.Fatalf("callback observed live mutation: checkpoint=%v pool-drift=%v", source.checkpointObserved, source.poolDriftObserved)
			}

			plan, problem := planScopedRetirementCombined(
				source,
				retirementTreeState{selectedTxn: 3, pageCount: fixture.arena.committedPageCount, root: 2, batchCount: 2},
				1, &token, deletePath, upsertPath, blobScratch, &replacements, &releases, &roles,
			)
			if problem.failed() {
				t.Fatalf("deterministic retry planning failed: %#v", problem)
			}
			result, problem := plan.apply()
			if problem.failed() || result.root != 11 || result.batchCount != 2 {
				t.Fatalf("deterministic retry = result %#v problem %#v", result, problem)
			}
		})
	}
}

func TestScopedRetirementFinalCallbackPrecedesOneShotApply(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10, 11, 12}, 3)
	token, problem := buildRetirementBlob(
		[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, retirementWriterPathCapacity)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
	releases := newPrivateReleaseBuffer(make([]uint32, 8))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	poolBefore := retirementPoolHash(&fixture.pool)
	source := &scopedRetirementMutationSource{
		base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
		pool: &fixture.pool, expectedPoolHash: poolBefore,
	}
	plan, problem := planScopedRetirementUpsert(
		source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
		&token, path, writerScanScratch(), &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	callbacksBeforeApply := source.callbacks
	source.mutateCheck = func() {
		path[0].keepFrom++
		source.mutateCheck = nil
	}
	_, problem = plan.apply()
	if problem.code != retirementWriteErrStaleEditPlan || problem.binding != retirementEditBindingUpsertPath {
		t.Fatalf("final callback mutation = %#v", problem)
	}
	if source.callbacks != callbacksBeforeApply+1 || source.checkpointObserved || source.poolDriftObserved || retirementPoolHash(&fixture.pool) != poolBefore {
		t.Fatalf("final callback count=%d/%d checkpoint=%v drift=%v", source.callbacks, callbacksBeforeApply, source.checkpointObserved, source.poolDriftObserved)
	}
	if _, problem = plan.apply(); problem.code != retirementWriteErrEditPlanConsumed {
		t.Fatalf("failed plan reuse = %#v", problem)
	}
	plan, problem = planScopedRetirementUpsert(
		source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
		&token, path, writerScanScratch(), &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatalf("deterministic replan failed: %#v", problem)
	}
	result, problem := plan.apply()
	if problem.failed() || result.root != 11 {
		t.Fatalf("deterministic replan apply = %#v %#v", result, problem)
	}
	if _, problem = plan.apply(); problem.code != retirementWriteErrEditPlanConsumed {
		t.Fatalf("successful plan reuse = %#v", problem)
	}
}

func TestScopedRetirementFinalCallbackPreservesLegalForeignMutation(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10, 11}, 2)
	token, problem := buildRetirementBlob(
		[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, retirementWriterPathCapacity)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
	releases := newPrivateReleaseBuffer(make([]uint32, 8))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	source := &scopedRetirementMutationSource{
		base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
		pool: &fixture.pool, expectedPoolHash: retirementPoolHash(&fixture.pool),
	}
	plan, problem := planScopedRetirementUpsert(
		source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
		&token, path, writerScanScratch(), &replacements, &releases, &roles,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	targetBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.workScope)
	mutationBefore := fixture.pool.mutationEpoch
	foreignMutationApplied := false
	source.mutateCheck = func() {
		checkpoint, poolProblem := fixture.pool.begin()
		if poolProblem.failed() {
			t.Fatalf("foreign begin: %#v", poolProblem)
		}
		if _, poolProblem = fixture.pool.claimPageInScope(
			checkpoint, fixture.foreignScope, 3, privatePageOwnerBitmap, privatePageBitmap,
		); poolProblem.failed() {
			t.Fatalf("foreign claim: %#v", poolProblem)
		}
		if poolProblem = fixture.pool.commit(checkpoint); poolProblem.failed() {
			t.Fatalf("foreign commit: %#v", poolProblem)
		}
		foreignMutationApplied = true
		source.mutateCheck = nil
	}
	if _, problem = plan.apply(); problem.code != retirementWriteErrStaleEditPlan || problem.binding != retirementEditBindingPool {
		t.Fatalf("foreign mutation apply = %#v", problem)
	}
	if !foreignMutationApplied || fixture.pool.mutationEpoch <= mutationBefore || fixture.pool.activeCheckpointID != 0 {
		t.Fatalf("foreign mutation was concealed: applied=%t epochs=%d/%d checkpoint=%d",
			foreignMutationApplied, mutationBefore, fixture.pool.mutationEpoch, fixture.pool.activeCheckpointID)
	}
	foreign, poolProblem := fixture.pool.borrowExactInScope(
		fixture.foreignScope, 3, privatePageOwnerBitmap, privatePageBitmap,
	)
	if poolProblem.failed() || foreign.slotEpoch != fixture.pool.slots[foreign.slot].epoch {
		t.Fatalf("foreign mutation not preserved: token %#v problem %#v", foreign, poolProblem)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.workScope), targetBefore) {
		t.Fatal("rejected plan changed its target scope")
	}
	if poolProblem = fixture.pool.validateScopeMembers(fixture.foreignScope); poolProblem.failed() {
		t.Fatalf("foreign scope invalid after rejected plan: %#v", poolProblem)
	}
	if poolProblem = fixture.pool.validateScopeMembers(fixture.workScope); poolProblem.failed() {
		t.Fatalf("target scope invalid after rejected plan: %#v", poolProblem)
	}
	global := verifyPrivatePageAVL(t, &fixture.pool, fixture.pool.indexRoot, false, 0)
	foreignAVL := verifyPrivatePageAVL(t, &fixture.pool, fixture.pool.slots[fixture.foreignScope.anchor].scopeRoot, true, fixture.foreignScope.id)
	targetAVL := verifyPrivatePageAVL(t, &fixture.pool, fixture.pool.slots[fixture.workScope.anchor].scopeRoot, true, fixture.workScope.id)
	if global.count != 3 || global.inUse != 2 || foreignAVL.count != 1 || foreignAVL.inUse != 1 ||
		targetAVL.count != 2 || targetAVL.inUse != 1 {
		t.Fatalf("pool proof after rejected plan = global %#v foreign %#v target %#v", global, foreignAVL, targetAVL)
	}
}

func TestScopedRetirementCallbackFenceRejectsMalformedAuthorityWithoutPanic(t *testing.T) {
	tests := []struct {
		name    string
		binding retirementEditBinding
		mutate  func(*scopedRetirementFixture)
		repair  func(*scopedRetirementFixture, privatePagePool, privatePagePoolSlot)
	}{
		{name: "nil arena pool", binding: retirementEditBindingArena, mutate: func(f *scopedRetirementFixture) {
			f.arena.pool = nil
		}},
		{name: "substituted arena pool", binding: retirementEditBindingArena, mutate: func(f *scopedRetirementFixture) {
			f.arena.pool = &privatePagePool{}
		}},
		{name: "negative scope anchor", binding: retirementEditBindingArena, mutate: func(f *scopedRetirementFixture) {
			f.arena.scope.anchor = -2
		}},
		{name: "nil pool slots", binding: retirementEditBindingPool, mutate: func(f *scopedRetirementFixture) {
			f.pool.slots = nil
		}, repair: func(f *scopedRetirementFixture, original privatePagePool, _ privatePagePoolSlot) {
			f.pool.slots = original.slots
		}},
		{name: "substituted pool slots", binding: retirementEditBindingPool, mutate: func(f *scopedRetirementFixture) {
			f.pool.slots = append([]privatePagePoolSlot(nil), f.pool.slots...)
		}, repair: func(f *scopedRetirementFixture, original privatePagePool, _ privatePagePoolSlot) {
			f.pool.slots = original.slots
		}},
		{name: "nil pool self", binding: retirementEditBindingPool, mutate: func(f *scopedRetirementFixture) {
			f.pool.self = nil
		}, repair: func(f *scopedRetirementFixture, original privatePagePool, _ privatePagePoolSlot) {
			f.pool.self = original.self
		}},
		{name: "negative scope capacity", binding: retirementEditBindingPool, mutate: func(f *scopedRetirementFixture) {
			f.pool.slots[f.workScope.anchor].scopeCapacity = -1
		}, repair: func(f *scopedRetirementFixture, _ privatePagePool, original privatePagePoolSlot) {
			f.pool.slots[f.workScope.anchor].scopeCapacity = original.scopeCapacity
		}},
		{name: "invalid member head", binding: retirementEditBindingPool, mutate: func(f *scopedRetirementFixture) {
			f.pool.slots[f.workScope.anchor].scopeMemberHead = -2
		}, repair: func(f *scopedRetirementFixture, _ privatePagePool, original privatePagePoolSlot) {
			f.pool.slots[f.workScope.anchor].scopeMemberHead = original.scopeMemberHead
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedRetirementFixture(t, nil, []uint32{10, 11, 12}, 3)
			token, problem := buildRetirementBlob(
				[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			path := make([]retirementPathFrame, retirementWriterPathCapacity)
			replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
			releases := newPrivateReleaseBuffer(make([]uint32, 8))
			roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
			originalPool := fixture.pool
			originalAnchor := fixture.pool.slots[fixture.workScope.anchor]
			poolBefore := retirementPoolHash(&fixture.pool)
			source := &scopedRetirementMutationSource{
				base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
				pool: &fixture.pool, expectedPoolHash: poolBefore,
			}
			source.mutateCheck = func() {
				test.mutate(fixture)
				source.mutateCheck = nil
			}
			_, problem = planScopedRetirementUpsert(
				source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
				&token, path, writerScanScratch(), &replacements, &releases, &roles,
			)
			if problem.code != retirementWriteErrStaleEditPlan || problem.binding != test.binding {
				t.Fatalf("malformed callback authority = %#v", problem)
			}
			if test.repair != nil {
				test.repair(fixture, originalPool, originalAnchor)
			}
			if fixture.arena.pool != &fixture.pool || fixture.pool.self != &fixture.pool ||
				fixture.pool.activeCheckpointID != 0 || retirementPoolHash(&fixture.pool) != poolBefore {
				t.Fatal("callback rejection did not restore authority headers")
			}
			plan, problem := planScopedRetirementUpsert(
				source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
				&token, path, writerScanScratch(), &replacements, &releases, &roles,
			)
			if problem.failed() {
				t.Fatalf("retry planning = %#v", problem)
			}
			if _, problem = plan.apply(); problem.failed() {
				t.Fatalf("retry apply = %#v", problem)
			}
		})
	}
}

func TestScopedRetirementGuardSealAllowsNilArenaRejection(t *testing.T) {
	fixture := newScopedRetirementFixture(t, nil, []uint32{10}, 1)
	source := &scopedRetirementMutationSource{
		base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
		pool: &fixture.pool, expectedPoolHash: retirementPoolHash(&fixture.pool),
	}
	guard := newGuardedRetirementSource(source, &fixture.arena, nil, nil, nil, nil, nil, nil, nil)
	source.mutateCheck = func() { guard.arena = nil }
	status := guard.checkAccessStatus()
	problem := guard.checkedProblem(retirementSourceProblem(status))
	if problem.code != retirementWriteErrStaleEditPlan || problem.binding != retirementEditBindingArena ||
		guard.arena != &fixture.arena || fixture.arena.pool != &fixture.pool {
		t.Fatalf("nil guarded arena = status %#v problem %#v", status, problem)
	}
}

func TestScopedRetirementRejectsMalformedMemberChainBeforeCallback(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*scopedRetirementFixture)
	}{
		{name: "truncated", mutate: func(f *scopedRetirementFixture) {
			head := f.pool.slots[f.workScope.anchor].scopeMemberHead
			f.pool.slots[head].scopeMemberNext = privatePagePoolNoIndex
		}},
		{name: "foreign member", mutate: func(f *scopedRetirementFixture) {
			head := f.pool.slots[f.workScope.anchor].scopeMemberHead
			f.pool.slots[head].scopeMemberNext = f.foreignScope.anchor
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedRetirementFixture(t, []uint32{20}, []uint32{10, 11, 12}, 3)
			token, problem := buildRetirementBlob(
				[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.mutate(fixture)
			source := &scopedRetirementMutationSource{
				base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
				pool: &fixture.pool, expectedPoolHash: retirementPoolHash(&fixture.pool),
			}
			roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
			_, problem = planScopedRetirementUpsert(
				source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
				&token, make([]retirementPathFrame, retirementWriterPathCapacity), writerScanScratch(),
				&committedReplacementLedger{}, &privateReleaseBuffer{}, &roles,
			)
			if problem.code != retirementWriteErrPrivateScopeMismatch || source.callbacks != 0 || roles.activePlan != 0 {
				t.Fatalf("pre-callback malformed chain = %#v callbacks=%d active=%d", problem, source.callbacks, roles.activePlan)
			}
		})
	}
}

func TestScopedRetirementFinalFenceBindsLifecycleAndTokenArena(t *testing.T) {
	tests := []struct {
		name    string
		binding retirementEditBinding
		mutate  func(*pageRoleIndex, *retirementBlobToken, *privatePageArena)
	}{
		{name: "plan sequence", binding: retirementEditBindingRoles, mutate: func(roles *pageRoleIndex, _ *retirementBlobToken, _ *privatePageArena) {
			roles.planSequence++
		}},
		{name: "active plan", binding: retirementEditBindingRoles, mutate: func(roles *pageRoleIndex, _ *retirementBlobToken, _ *privatePageArena) {
			roles.activePlan++
		}},
		{name: "token arena", binding: retirementEditBindingBlobToken, mutate: func(_ *pageRoleIndex, token *retirementBlobToken, arena *privatePageArena) {
			token.arena = arena
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedRetirementFixture(t, nil, []uint32{10, 11, 12}, 3)
			token, problem := buildRetirementBlob(
				[]uint32{2, 7}, &fixture.arena, &blobBuildScratch{pageNumbers: make([]uint32, 1)},
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			path := make([]retirementPathFrame, retirementWriterPathCapacity)
			replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
			releases := newPrivateReleaseBuffer(make([]uint32, 8))
			roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
			source := &scopedRetirementMutationSource{
				base: newImmutableSlicePageSource(nil, fixture.arena.committedPageCount),
				pool: &fixture.pool, expectedPoolHash: retirementPoolHash(&fixture.pool),
			}
			plan, problem := planScopedRetirementUpsert(
				source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
				&token, path, writerScanScratch(), &replacements, &releases, &roles,
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			alternateArena := fixture.arena
			source.mutateCheck = func() {
				test.mutate(&roles, &token, &alternateArena)
				source.mutateCheck = nil
			}
			_, problem = plan.apply()
			if problem.code != retirementWriteErrStaleEditPlan || problem.binding != test.binding {
				t.Fatalf("final lifecycle mutation = %#v", problem)
			}
			if roles.planSequence != plan.planID || roles.activePlan != 0 || token.arena != &fixture.arena {
				t.Fatalf("failed plan lifecycle/token = sequence %d active %d token arena %p", roles.planSequence, roles.activePlan, token.arena)
			}
			if _, problem = plan.apply(); problem.code != retirementWriteErrEditPlanConsumed {
				t.Fatalf("failed plan reuse = %#v", problem)
			}
			plan, problem = planScopedRetirementUpsert(
				source, retirementTreeState{selectedTxn: 1, pageCount: fixture.arena.committedPageCount},
				&token, path, writerScanScratch(), &replacements, &releases, &roles,
			)
			if problem.failed() {
				t.Fatalf("replan = %#v", problem)
			}
			if _, problem = plan.apply(); problem.failed() {
				t.Fatalf("replan apply = %#v", problem)
			}
		})
	}
}

func TestScopedRetirementRejectsBindingAndScopeDrift(t *testing.T) {
	fixture := newScopedRetirementFixture(t, nil, []uint32{10}, 1)
	checkpoint, problem := fixture.arena.beginWithAllocationBatch(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	index, _ := fixture.pool.slotIndex(10)
	fixture.pool.slots[index].epoch++
	fixture.arena.allocatePrepared(checkpoint, privatePageRetirementTree)
	if problem = fixture.arena.commit(checkpoint, nil); problem.code != retirementWriteErrPrivateBindingDrift {
		t.Fatalf("binding drift = %#v", problem)
	}
	if problem = fixture.arena.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}

	stale := fixture.arena
	stale.scope.id++
	if _, problem = stale.begin(); problem.code != retirementWriteErrPrivateScopeMismatch {
		t.Fatalf("scope drift = %#v", problem)
	}
}

func TestScopedRetirementRejectsForeignReleaseAndTransfer(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10}, 1)
	claimScopedRetirementPage(t, &fixture.pool, fixture.foreignScope, 3, privatePageRetirementTree, 0xa5)
	checkpoint, problem := fixture.arena.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.arena.preflightCommit(checkpoint, []uint32{3}); problem.code != retirementWriteErrPrivateScopeMismatch {
		t.Fatalf("foreign release = %#v", problem)
	}
	if _, problem = fixture.arena.transferRetirementPageToBitmap(checkpoint.pool, 3, privatePageRetirementTree); problem.code != retirementWriteErrPrivateScopeMismatch {
		t.Fatalf("foreign transfer = %#v", problem)
	}
	if problem = fixture.arena.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestScopedBitmapTransferToRetirementStaysInScope(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10}, 1)
	checkpoint, poolProblem := fixture.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if _, poolProblem = fixture.pool.claimPageInScope(
		checkpoint, fixture.workScope, 10, privatePageOwnerBitmap, privatePageBitmap,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = fixture.pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	foreignBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope)
	cow := freeBitmapCOW{pool: &fixture.pool, scoped: true, scope: fixture.workScope}
	checkpoint, poolProblem = fixture.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	transferred, problem := cow.transferBitmapPageToRetirement(checkpoint, 10, privatePageRetirementTree)
	if problem.failed() {
		t.Fatal(problem)
	}
	if poolProblem = fixture.pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if transferred.scopeID != fixture.workScope.id || transferred.owner != privatePageOwnerRetirement {
		t.Fatalf("transferred token = %#v", transferred)
	}
	if _, poolProblem = fixture.pool.borrowExactInScope(
		fixture.workScope, 10, privatePageOwnerRetirement, privatePageRetirementTree,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("bitmap-to-retirement transfer changed foreign scope")
	}
}

func TestScopedRetirementRollbackCoversClaimWriteReleaseAndTransfer(t *testing.T) {
	fixture := newScopedRetirementFixture(t, nil, []uint32{10, 11}, 2)
	claimScopedRetirementPage(t, &fixture.pool, fixture.workScope, 11, privatePageRetirementTree, 0x5a)
	before := scopedRetirementForeignSnapshot(&fixture.pool, fixture.workScope)

	checkpoint, problem := fixture.arena.beginWithAllocationBatch(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	page := fixture.arena.allocatePrepared(checkpoint, privatePageRetirementBlob)
	var bytes [PageSize]byte
	bytes[0] = 0x33
	fixture.arena.writePage(page, &bytes)
	if _, problem = fixture.arena.transferRetirementPageToBitmap(checkpoint.pool, 11, privatePageRetirementTree); problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.arena.preflightCommit(checkpoint, []uint32{10}); problem.failed() {
		t.Fatal(problem)
	}
	index, _ := fixture.pool.slotIndex(10)
	if poolProblem := fixture.pool.releaseSlotForCheckpointInScopePrepared(
		checkpoint.pool, fixture.workScope, index, privatePageAvailable,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem = fixture.arena.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	after := scopedRetirementForeignSnapshot(&fixture.pool, fixture.workScope)
	for index := range before {
		before[index].epoch, after[index].epoch = 0, 0
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("claim/write/release/transfer rollback did not restore the exact scope")
	}
}

func TestScopedRetirementCommitReleaseAndTransferAreExact(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10, 11}, 2)
	claimScopedRetirementPage(t, &fixture.pool, fixture.foreignScope, 3, privatePageRetirementTree, 0xa5)
	claimScopedRetirementPage(t, &fixture.pool, fixture.workScope, 10, privatePageRetirementTree, 0x10)
	claimScopedRetirementPage(t, &fixture.pool, fixture.workScope, 11, privatePageRetirementTree, 0x11)
	foreignBefore := scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope)

	checkpoint, problem := fixture.arena.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = fixture.arena.transferRetirementPageToBitmap(
		checkpoint.pool, 11, privatePageRetirementTree,
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.arena.commit(checkpoint, []uint32{10}); problem.failed() {
		t.Fatal(problem)
	}
	page10, _ := fixture.pool.slotIndex(10)
	page11, _ := fixture.pool.slotIndex(11)
	if fixture.pool.slots[page10].state != privatePageAvailable ||
		fixture.pool.slots[page11].owner != privatePageOwnerBitmap ||
		fixture.pool.slots[page11].origin != privatePageBitmap {
		t.Fatal("exact-scope release/transfer commit did not publish its prepared state")
	}
	if !reflect.DeepEqual(scopedRetirementForeignSnapshot(&fixture.pool, fixture.foreignScope), foreignBefore) {
		t.Fatal("exact-scope release/transfer commit changed the foreign scope")
	}
}

func TestScopedRetirementGenerationCleanupIsExact(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10}, 1)
	foreign := claimScopedRetirementPage(t, &fixture.pool, fixture.foreignScope, 3, privatePageRetirementBlob, 0xa5)
	own := claimScopedRetirementPage(t, &fixture.pool, fixture.workScope, 10, privatePageRetirementBlob, 0x5a)
	// Align the generation labels to prove cleanup is selected by scope, not by
	// the generation number alone.
	fixture.pool.slots[foreign.slot].generation = own.generation
	if problem := fixture.arena.releaseGeneration(own.generation, privatePageRetirementBlob); problem.failed() {
		t.Fatal(problem)
	}
	if fixture.pool.slots[own.slot].state != privatePageAvailable ||
		fixture.pool.slots[foreign.slot].state != privatePageInUse ||
		fixture.pool.slots[foreign.slot].bytes[0] != 0xa5 {
		t.Fatal("generation cleanup crossed its exact scope")
	}
}

func TestScopedRetirementSkipsUnboundSlotsAndPageZero(t *testing.T) {
	fixture := newScopedRetirementFixture(t, nil, []uint32{10}, 3)
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 1))
	replacements := newCommittedReplacementLedger(nil)
	if problem := roles.prepare(&fixture.arena, &replacements); problem.failed() {
		t.Fatal(problem)
	}
	if roles.used != 1 || roles.slots[0].pageNumber != 10 {
		t.Fatalf("roles include unbound/page-zero slots: %#v", roles.slots[:roles.used])
	}
}

func TestScopedRetirementDetectsGlobalCommittedCollision(t *testing.T) {
	fixture := newScopedRetirementFixture(t, []uint32{3}, []uint32{10}, 1)
	image := retirementWriterImage(100)
	putWriterRetirementLeaf(retirementWriterPage(image, 3), 1, []retirementBatch{{retiredByTxn: 1, pageCount: 1, pageListBlobRoot: 4}})
	source := &retirementWriterTestSource{data: image, pageCount: 100}
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 4))
	if problem := roles.prepare(&fixture.arena, &committedReplacementLedger{}); problem.failed() {
		t.Fatal(problem)
	}
	var page [PageSize]byte
	_, problem := readMetadataPage(
		source, retirementTreeState{selectedTxn: 1, pageCount: 100, root: 3, batchCount: 1},
		&fixture.arena, 3, privatePageRetirementTree, pageRoleSelectedRetirementTree, &page, &roles,
	)
	if problem.code != retirementWriteErrPrivatePageUnavailable {
		t.Fatalf("foreign/committed collision = %#v", problem)
	}
}

func TestScopedRetirementStep3ScopeAndZeroAllocation(t *testing.T) {
	storage := newLateBitmapPlannerStorage(4, 4, 4, 8)
	attachment := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	arena, problem := newPrivatePageArenaInScope(attachment.cow.pool, attachment.scope, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	scratch := blobBuildScratch{pageNumbers: make([]uint32, 1)}
	pages := []uint32{2, 7}
	var token retirementBlobToken
	allocations := testing.AllocsPerRun(20, func() {
		token, problem = buildRetirementBlob(pages, &arena, &scratch)
		if problem.failed() {
			return
		}
		problem = token.discard()
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("step-3 scope build/discard = %#v, allocations=%v", problem, allocations)
	}
}

func TestScopedRetirementRolePreparationScalesWithExactScope(t *testing.T) {
	for _, size := range []int{512, 4096} {
		work := make([]uint32, size)
		for index := range work {
			work[index] = uint32(10 + index*2)
		}
		fixture := newScopedRetirementFixture(t, nil, work, size)
		roles := newPageRoleIndex(make([]pageRoleIndexSlot, size))
		replacements := newCommittedReplacementLedger(nil)
		var problem retirementWriteError
		allocations := testing.AllocsPerRun(10, func() {
			problem = roles.prepare(&fixture.arena, &replacements)
		})
		if problem.failed() || allocations != 0 || roles.used != size {
			t.Fatalf("size %d prepare = %#v, allocations=%v, used=%d", size, problem, allocations, roles.used)
		}
		_, _, _, visited, present := verifyPageRoleAVL(t, &roles, roles.root)
		if !present || visited != size {
			t.Fatalf("size %d visited=%d/present=%v", size, visited, present)
		}
	}
}
