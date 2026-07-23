package exactv4

import "testing"

type bitmapPlannerStorage struct {
	pool           privatePagePool
	poolValidation []uint32
	arena          []reservedBitmapPage
	candidates     []uint32
	verified       []verifiedBitmapPage
	replacements   []uint32
	indexNodes     []bitmapCOWIndexNode
	availableSlots []int
}

func newBitmapPlannerStorage(arena, candidates, verified, index int) bitmapPlannerStorage {
	return bitmapPlannerStorage{
		poolValidation: make([]uint32, arena),
		arena:          make([]reservedBitmapPage, arena),
		candidates:     make([]uint32, candidates),
		verified:       make([]verifiedBitmapPage, verified),
		replacements:   make([]uint32, verified),
		indexNodes:     make([]bitmapCOWIndexNode, index),
		availableSlots: make([]int, arena),
	}
}

func (s *bitmapPlannerStorage) buffers() freeBitmapReservationBuffers {
	return freeBitmapReservationBuffers{
		pool: &s.pool, poolValidation: s.poolValidation,
		arena: s.arena, candidates: s.candidates, verifiedPages: s.verified,
		replacements: s.replacements, indexNodes: s.indexNodes,
		availableSlots: s.availableSlots,
	}
}

func requirePrivateFreeBit(t *testing.T, cow *freeBitmapCOW, pageNumber uint32) {
	t.Helper()
	level, ok := minimumFreeBitmapLevel(cow.pageCount)
	if !ok || cow.root == 0 {
		t.Fatalf("free page %d has no representable root", pageNumber)
	}
	current := cow.root
	base := uint64(0)
	for {
		page, found := copiedPrivateBitmapPage(cow, current)
		if !found {
			t.Fatalf("free page %d reached non-private page %d", pageNumber, current)
		}
		if level == 0 {
			local := uint64(pageNumber) - base
			if rawFreeBitmapLeafWord(&page, int(local/64))&(uint64(1)<<uint(local%64)) == 0 {
				t.Fatalf("free page %d bit is zero", pageNumber)
			}
			return
		}
		span, valid := freeBitmapCoverage(level - 1)
		if !valid {
			t.Fatal("invalid bitmap coverage")
		}
		childIndex := int((uint64(pageNumber) - base) / span)
		current = rawFreeBitmapBranchChild(&page, childIndex)
		if current == 0 {
			t.Fatalf("free page %d path is absent", pageNumber)
		}
		base += uint64(childIndex) * span
		level--
	}
}

func TestFreeBitmapReservationUsesVerifiedLowestCandidatesAndSelfFunding(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6, 7)}}
	storage := newLateBitmapPlannerStorage(2, 2, 1, 8)
	plan := newLateBitmapPlan(t, source, 2, 1, &storage)
	if !equalU32(plan.cow.candidates, []uint32{5, 6}) || plan.cow.pageCount != 20 || source.pageReads(2) != 1 {
		t.Fatalf("plan = candidates %v, page-count %d, reads %d", plan.cow.candidates, plan.cow.pageCount, source.pageReads(2))
	}
	proof := completeLateBitmapProof(t, &plan, 0, nil)
	if _, problem := plan.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	if plan.cow.root != 5 || !equalU32(plan.cow.candidatePages(), []uint32{5, 6}) ||
		plan.cow.availablePrivatePages() != 1 || source.pageReads(2) != 1 {
		t.Fatalf("applied = root %d, candidates %v, available %d, reads %d", plan.cow.root, plan.cow.candidatePages(), plan.cow.availablePrivatePages(), source.pageReads(2))
	}
	if storage.arena[0].state != privateBitmapPageInUse || storage.arena[0].committedOrigin != 2 {
		t.Fatal("lowest candidate did not fund its own bitmap COW")
	}
}

func TestFreeBitmapReservationAppendsOnlyAfterProvenExhaustion(t *testing.T) {
	source := &cowSparsePages{}
	storage := newLateBitmapPlannerStorage(3, 1, 1, 4)
	plan := newLateBitmapPlan(t, source, 0, 3, &storage)
	if len(plan.cow.candidates) != 0 || plan.cow.pageCount != 20 || source.reads != 0 {
		t.Fatalf("capacity plan = candidates %v, page-count %d, reads %d", plan.cow.candidates, plan.cow.pageCount, source.reads)
	}
	proof := completeLateBitmapProof(t, &plan, 0, nil)
	if _, problem := plan.bind(&proof); problem.failed() || plan.cow.availablePrivatePages() != 3 {
		t.Fatalf("append bind = %v, available %d", problem, plan.cow.availablePrivatePages())
	}
	for index, page := range storage.arena {
		if page.pageNumber != uint32(20+index) || page.authorization != privateBitmapPageAppended {
			t.Fatalf("appended slot %d = page %d/auth %d", index, page.pageNumber, page.authorization)
		}
	}
}

func TestFreeBitmapReservationPeakPrefixAndBudgetMinusOneAreExact(t *testing.T) {
	pageCount := mustBitmapCoverage(t, 2) + 1
	pages := []cowSparsePage{
		cowBranch(t, 2, 1, 3, cowChild{index: 0, page: 3}),
		cowBranch(t, 3, 1, 2, cowChild{index: 0, page: 4}),
		cowBranch(t, 4, 1, 1, cowChild{index: 0, page: 5}),
		cowLeaf(t, 5, 1, 10, 11),
	}
	storage := newLateBitmapPlannerStorage(5, 2, 4, 12)
	plan := newLateBitmapPlanAt(t, &cowSparsePages{pages: pages}, pageCount, 2, 1, &storage)
	if !equalU32(plan.cow.candidates, []uint32{10, 11}) || plan.cow.pool.capacity() != 5 || plan.cow.pageCount != pageCount {
		t.Fatalf("peak plan = candidates %v, arena %d, page-count %d", plan.cow.candidates, plan.cow.pool.capacity(), plan.cow.pageCount)
	}
	proof := completeLateBitmapProof(t, &plan, 0, nil)
	if _, problem := plan.bind(&proof); problem.failed() || plan.cow.root != 0 || plan.cow.availablePrivatePages() != 5 {
		t.Fatalf("peak bind = %v, root %d, available %d", problem, plan.cow.root, plan.cow.availablePrivatePages())
	}

	minusOne := newLateBitmapPlannerStorage(4, 2, 4, 12)
	planner, problem := newFreeBitmapReservationPlanner(
		&cowSparsePages{pages: pages}, 1, pageCount, 2, 1, minusOne.buffers(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = planner.planCapacity()
	problem = requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrInsufficientResourceBudget)
	if problem.resource != freeBitmapResourceArenaPages || problem.required != 5 || problem.actual != 4 {
		t.Fatalf("minus-one evidence = resource %d, required %d, actual %d", problem.resource, problem.required, problem.actual)
	}
	for index := range minusOne.arena {
		if minusOne.arena[index] != (reservedBitmapPage{}) {
			t.Fatalf("minus-one authorized arena slot %d", index)
		}
	}
}

func TestFreeBitmapReservationDeepPostBindUsesExactScopedOperation(t *testing.T) {
	pageCount := mustBitmapCoverage(t, 2) + 1
	pages := []cowSparsePage{
		cowBranch(t, 2, 1, 3, cowChild{index: 0, page: 3}),
		cowBranch(t, 3, 1, 2, cowChild{index: 0, page: 4}),
		cowBranch(t, 4, 1, 1, cowChild{index: 0, page: 5}),
		cowLeaf(t, 5, 1, 10, 11),
	}
	storage := newLateBitmapPlannerStorage(8, 4, 8, 16)
	attachment := newLateBitmapPlanAt(
		t, &cowSparsePages{pages: pages}, pageCount, 2, 1, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	if problem := attachment.cow.validateScopedBindings(); problem.failed() {
		t.Fatalf("post-bind scoped state = %#v bindings=%+v slots=%+v", problem, attachment.cow.arenaBindings, attachment.cow.pool.slots)
	}

	// Step 3 ends at a valid scoped bind. Step 5 now carries that exact scope
	// through insertion and finalization instead of falling back to the global
	// pool operation.
	insertScratch := make([]freeBitmapInsertPage, freeBitmapPathCapacity)
	inserted, problem := attachment.cow.insertFree(6, insertScratch)
	if problem.failed() {
		t.Fatal(problem)
	}
	if inserted.inserted != 1 || attachment.cow.pool.activeOperationID != 0 {
		t.Fatalf("scoped insertion = %#v operation=%d", inserted, attachment.cow.pool.activeOperationID)
	}

	if problem := attachment.cow.validateScopedBindings(); problem.failed() {
		t.Fatalf("Step 5 boundary damaged scoped bindings = %#v", problem)
	}
}

func TestFreeBitmapInsertionBuildsCanonicalBoundariesAndMaximumPage(t *testing.T) {
	boundaries := []uint64{BitmapLeafBits, BitmapLeafBits * BitmapFanout, BitmapLeafBits * BitmapFanout * BitmapFanout}
	for oldLevel, boundary := range boundaries {
		t.Run(string(rune('0'+oldLevel)), func(t *testing.T) {
			arena := make([]reservedBitmapPage, oldLevel+2)
			for index := range arena {
				arena[index] = newReservedBitmapPage(uint32(100 + index))
			}
			cow, problem := newFreeBitmapCOW(nil, 1, boundary, 0, emptyFreeBitmapCOWLedger(arena, make([]uint32, 0), nil))
			if problem.failed() {
				t.Fatal(problem)
			}
			cow.pageCount = boundary + 1
			cow.pageCountsDistinct = true
			scratch := make([]freeBitmapInsertPage, oldLevel+2)
			result, problem := cow.insertFree(5, scratch)
			if problem.failed() || result.newBitmapPages != oldLevel+2 {
				t.Fatalf("boundary insertion = %+v/%v", result, problem)
			}
			root, found := copiedPrivateBitmapPage(cow, cow.root)
			if !found {
				t.Fatal("boundary root is not private")
			}
			header, headerProblem := decodePageHeaderNoAlloc(root[:], 2)
			if headerProblem.code != 0 || header.Level != uint16(oldLevel+1) {
				t.Fatalf("boundary root = %+v/%+v", header, headerProblem)
			}
			requirePrivateFreeBit(t, cow, 5)
			demoted, problem := cow.insertFreePagesForPageCount(nil, boundary, scratch)
			if problem.failed() || demoted.recycledPrivatePages != 1 || cow.pageCount != boundary {
				t.Fatalf("boundary demotion = %+v/%v, page-count %d", demoted, problem, cow.pageCount)
			}
			root, found = copiedPrivateBitmapPage(cow, cow.root)
			if !found {
				t.Fatal("demoted root is not private")
			}
			header, headerProblem = decodePageHeaderNoAlloc(root[:], 2)
			if headerProblem.code != 0 || header.Level != uint16(oldLevel) {
				t.Fatalf("demoted root = %+v/%+v", header, headerProblem)
			}
			requirePrivateFreeBit(t, cow, 5)
		})
	}

	arena := []reservedBitmapPage{
		newReservedBitmapPage(100), newReservedBitmapPage(101),
		newReservedBitmapPage(102), newReservedBitmapPage(103),
	}
	cow, problem := newFreeBitmapCOW(nil, 1, MaxPageCount, 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	result, problem := cow.insertFree(^uint32(0), make([]freeBitmapInsertPage, freeBitmapPathCapacity))
	if problem.failed() || result.newBitmapPages != freeBitmapPathCapacity {
		t.Fatalf("maximum insertion = %+v/%v", result, problem)
	}
	requirePrivateFreeBit(t, cow, ^uint32(0))
}

func TestFreeBitmapInsertionRejectsStaleCrossDraftAndBudgetAliasesAtomically(t *testing.T) {
	arena := []reservedBitmapPage{newReservedBitmapPage(50), newReservedBitmapPage(51)}
	cow, problem := newFreeBitmapCOW(nil, 1, 40_000, 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	scratch := make([]freeBitmapInsertPage, 2)
	preflight, problem := newFreeBitmapInsertPreflight(cow, []uint32{5}, cow.pageCount, scratch)
	if problem.failed() {
		t.Fatal(problem)
	}
	prepared, problem := preflight.plan()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = cow.insertFree(6, scratch); problem.failed() {
		t.Fatal(problem)
	}
	beforeRoot := cow.root
	_, problem = cow.applyPreparedFreeBitmapInsertion(prepared)
	requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrStaleInsertionPlan)
	if cow.root != beforeRoot {
		t.Fatal("stale insertion plan mutated its original draft")
	}

	otherArena := []reservedBitmapPage{newReservedBitmapPage(60), newReservedBitmapPage(61)}
	other, problem := newFreeBitmapCOW(nil, 1, 40_000, 0, emptyFreeBitmapCOWLedger(otherArena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = other.applyPreparedFreeBitmapInsertion(prepared)
	requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrStaleInsertionPlan)
	if other.root != 0 {
		t.Fatal("cross-draft insertion plan mutated another draft")
	}

	aliasArena := []reservedBitmapPage{newReservedBitmapPage(5)}
	alias, problem := newFreeBitmapCOW(nil, 1, 20, 0, emptyFreeBitmapCOWLedger(aliasArena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	aliasBefore := alias.pool.slots[0]
	_, problem = alias.insertFree(5, []freeBitmapInsertPage{{}})
	problem = requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrInsufficientResourceBudget)
	if problem.resource != freeBitmapResourceArenaPages || alias.root != 0 || alias.pool.slots[0] != aliasBefore {
		t.Fatal("release/destination alias failure was not atomic")
	}

	shortArena := []reservedBitmapPage{newReservedBitmapPage(70), newReservedBitmapPage(71)}
	short, problem := newFreeBitmapCOW(nil, 1, 40_000, 0, emptyFreeBitmapCOWLedger(shortArena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = short.insertFree(5, []freeBitmapInsertPage{{}})
	requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrInsertScratchExhausted)
	if short.root != 0 || short.availablePrivatePages() != 2 {
		t.Fatal("scratch budget failure mutated draft")
	}
}

func TestFreeBitmapInsertionLatePoolFailuresLeaveExactState(t *testing.T) {
	type insertionSnapshot struct {
		root, pageCount                         uint64
		replacementLen, candidateLen            int
		indexRoot, indexLen, availableLen       int
		mutationEpoch, poolMutation, generation uint64
		operationSequence, activeOperation      uint64
		slots                                   []privatePagePoolSlot
		replacements, candidates                []uint32
		indexNodes                              []bitmapCOWIndexNode
		availableSlots                          []int
		scratch                                 []freeBitmapInsertPage
	}

	prepare := func(t *testing.T) (*freeBitmapCOW, preparedFreeBitmapInsertion, []freeBitmapInsertPage) {
		t.Helper()
		arena := []reservedBitmapPage{newReservedBitmapPage(50), newReservedBitmapPage(51)}
		cow, problem := newFreeBitmapCOW(nil, 1, 40_000, 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
		if problem.failed() {
			t.Fatal(problem)
		}
		scratch := make([]freeBitmapInsertPage, 2)
		preflight, problem := newFreeBitmapInsertPreflight(cow, []uint32{5}, cow.pageCount, scratch)
		if problem.failed() {
			t.Fatal(problem)
		}
		prepared, problem := preflight.plan()
		if problem.failed() {
			t.Fatal(problem)
		}
		return cow, prepared, scratch
	}
	snapshot := func(cow *freeBitmapCOW, scratch []freeBitmapInsertPage) insertionSnapshot {
		return insertionSnapshot{
			root: uint64(cow.root), pageCount: cow.pageCount,
			replacementLen: cow.replacementLen, candidateLen: cow.candidateLen,
			indexRoot: cow.indexRoot, indexLen: cow.indexLen, availableLen: cow.availableLen,
			mutationEpoch: cow.mutationEpoch, poolMutation: cow.pool.mutationEpoch, generation: cow.pool.generation,
			operationSequence: cow.pool.operationSequence, activeOperation: cow.pool.activeOperationID,
			slots:        append([]privatePagePoolSlot(nil), cow.pool.slots...),
			replacements: append([]uint32(nil), cow.replacements...), candidates: append([]uint32(nil), cow.candidates...),
			indexNodes:     append([]bitmapCOWIndexNode(nil), cow.indexNodes...),
			availableSlots: append([]int(nil), cow.availableSlots...),
			scratch:        append([]freeBitmapInsertPage(nil), scratch...),
		}
	}
	assertExact := func(t *testing.T, cow *freeBitmapCOW, scratch []freeBitmapInsertPage, before insertionSnapshot) {
		t.Helper()
		if uint64(cow.root) != before.root || cow.pageCount != before.pageCount ||
			cow.replacementLen != before.replacementLen || cow.candidateLen != before.candidateLen ||
			cow.indexRoot != before.indexRoot || cow.indexLen != before.indexLen || cow.availableLen != before.availableLen ||
			cow.mutationEpoch != before.mutationEpoch || cow.pool.mutationEpoch != before.poolMutation ||
			cow.pool.generation != before.generation || cow.pool.operationSequence != before.operationSequence ||
			cow.pool.activeOperationID != before.activeOperation {
			t.Fatal("rejected insertion changed scalar or operation state")
		}
		for index := range before.slots {
			if cow.pool.slots[index] != before.slots[index] {
				t.Fatalf("rejected insertion changed pool slot %d", index)
			}
		}
		for index := range before.replacements {
			if cow.replacements[index] != before.replacements[index] {
				t.Fatalf("rejected insertion changed replacement %d", index)
			}
		}
		for index := range before.candidates {
			if cow.candidates[index] != before.candidates[index] {
				t.Fatalf("rejected insertion changed candidate %d", index)
			}
		}
		for index := range before.indexNodes {
			if cow.indexNodes[index] != before.indexNodes[index] {
				t.Fatalf("rejected insertion changed index node %d", index)
			}
		}
		for index := range before.availableSlots {
			if cow.availableSlots[index] != before.availableSlots[index] {
				t.Fatalf("rejected insertion changed available slot %d", index)
			}
		}
		for index := range before.scratch {
			if scratch[index] != before.scratch[index] {
				t.Fatalf("rejected insertion changed scratch node %d", index)
			}
		}
	}
	lateDestination := func(t *testing.T, prepared preparedFreeBitmapInsertion) int {
		t.Helper()
		found := 0
		for index := 0; index < prepared.scratchLen; index++ {
			node := prepared.scratch[index]
			if node.changed && node.origin != freeBitmapInsertOriginPrivate {
				if found == 1 {
					return node.destinationSlot
				}
				found++
			}
		}
		t.Fatal("test plan has fewer than two destinations")
		return 0
	}

	for _, test := range []struct {
		name   string
		inject func(*freeBitmapCOW, *preparedFreeBitmapInsertion, int)
		want   freeBitmapCOWErrorCode
	}{
		{
			name: "late owner",
			inject: func(cow *freeBitmapCOW, _ *preparedFreeBitmapInsertion, slot int) {
				cow.pool.slots[slot].owner = privatePageOwnerRetirement
			},
			want: freeBitmapCOWErrArenaPageConflict,
		},
		{
			name: "late slot epoch",
			inject: func(cow *freeBitmapCOW, _ *preparedFreeBitmapInsertion, slot int) {
				cow.pool.slots[slot].epoch = ^uint64(0)
			},
			want: freeBitmapCOWErrMutationEpochExhausted,
		},
		{
			name: "aggregate mutation headroom",
			inject: func(cow *freeBitmapCOW, prepared *preparedFreeBitmapInsertion, _ int) {
				cow.pool.mutationEpoch = ^uint64(0) - 5
				prepared.poolMutationEpoch = cow.pool.mutationEpoch
			},
			want: freeBitmapCOWErrMutationEpochExhausted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cow, prepared, scratch := prepare(t)
			slot := lateDestination(t, prepared)
			test.inject(cow, &prepared, slot)
			before := snapshot(cow, scratch)
			_, problem := cow.applyPreparedFreeBitmapInsertion(prepared)
			requireFreeBitmapCOWCode(t, problem, test.want)
			assertExact(t, cow, scratch, before)
		})
	}
}

func TestFreeBitmapInsertionReusesVerifiedIdentityExactlyOnce(t *testing.T) {
	makeCOW := func() (*cowSparsePages, freeBitmapCOW) {
		source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5)}}
		storage := newBitmapPlannerStorage(4, 4, 4, 8)
		planner, problem := newFreeBitmapReservationPlanner(source, 1, 20, 2, 1, storage.buffers())
		if problem.failed() {
			t.Fatal(problem)
		}
		// This is an isolated insertion-layer verified-page fixture. It stays on
		// the test-only all-bound adapter until Step 5 integrates scoped mutation.
		cow, problem := planner.plan()
		if problem.failed() {
			t.Fatal(problem)
		}
		return source, cow
	}

	source, cow := makeCOW()
	cow.verifiedPages[0].bytes[PageCRCOffset] ^= 0x80
	reads := source.reads
	result, problem := cow.insertFree(6, []freeBitmapInsertPage{{}})
	if problem.failed() || result.committedReplacements != 1 || source.reads != reads {
		t.Fatalf("verified reuse = %+v/%v, reads %d -> %d", result, problem, reads, source.reads)
	}
	requirePrivateFreeBit(t, &cow, 6)

	source, cow = makeCOW()
	cow.verifiedPages[0].base = BitmapLeafBits
	before := cow.pool.slots[0]
	_, problem = cow.insertFree(6, []freeBitmapInsertPage{{}})
	problem = requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrVerifiedPageIdentityMismatch)
	if problem.expectedBase != 0 || problem.actualBase != BitmapLeafBits || source.reads != 1 ||
		cow.root != 2 || cow.pool.slots[0] != before {
		t.Fatal("verified identity mismatch did not fail atomically with exact evidence")
	}
}

func TestFreeBitmapInsertionRequiresCommittedRootLevelAfterPendingGrowth(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}), cowLeaf(t, 3, 1, 5),
	}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10), newReservedBitmapPage(11)}
	cow, problem := newFreeBitmapCOW(source, 1, 32_000, 2, emptyFreeBitmapCOWLedger(arena, make([]uint32, 2), nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	cow.pageCount = 32_001
	cow.pageCountsDistinct = true
	_, problem = cow.insertFree(5, make([]freeBitmapInsertPage, 2))
	problem = requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrRootLevel)
	if problem.expectedLevel != 0 || problem.actualLevel != 1 || cow.root != 2 || cow.replacementLen != 0 {
		t.Fatal("committed root at pending level was accepted or mutated")
	}
}

func TestFreeBitmapPlannerInsertionAndFinalizerAreAccessFirst(t *testing.T) {
	denied := pageSourceError{code: pageSourceErrForkedHandle}
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5)}, access: &denied}
	storage := newBitmapPlannerStorage(1, 1, 1, 2)
	_, problem := newFreeBitmapReservationPlanner(source, 0, 1, 1, -1, storage.buffers())
	problem = requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrSource)
	if problem.source != denied.status() || source.reads != 0 {
		t.Fatal("planner validation ran before source access")
	}

	cow, problem := newFreeBitmapCOW(
		source, 1, 20, 2,
		emptyFreeBitmapCOWLedger([]reservedBitmapPage{newReservedBitmapPage(10)}, make([]uint32, 1), nil),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	cow.mutationEpoch = ^uint64(0)
	before := cow.pool.slots[0]
	allocations := testing.AllocsPerRun(100, func() {
		_, problem = cow.insertFree(5, nil)
		if problem.code != freeBitmapCOWErrSource {
			panic("insertion did not reject access first")
		}
	})
	if allocations != 0 || source.reads != 0 || cow.root != 2 || cow.pool.slots[0] != before {
		t.Fatalf("access-first insertion = allocations %v, reads %d, root %d, arena changed %t", allocations, source.reads, cow.root, cow.pool.slots[0] != before)
	}
	_, problem = cow.releaseUnusedReservations(nil, nil)
	requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrSource)
	if source.reads != 0 || cow.root != 2 {
		t.Fatal("access-first finalizer read or mutated")
	}
}

func TestFreeBitmapFinalizationDemotionReleasesCandidatesAndAppendedInSamePlan(t *testing.T) {
	t.Run("candidate", func(t *testing.T) {
		arena := []reservedBitmapPage{
			newReservedBitmapPage(10), newReservedBitmapPage(11), newReservedBitmapPage(32_000),
		}
		arena[0].bytes = cowBranch(t, 10, 2, 1, cowChild{index: 0, page: 11}).bytes
		arena[0].state = privateBitmapPageInUse
		arena[0].authorization = privateBitmapPageCommittedFreeCandidate
		arena[1].bytes = cowLeaf(t, 11, 2, 5).bytes
		arena[1].state = privateBitmapPageInUse
		arena[2].authorization = privateBitmapPageAppended
		cow, problem := newFreeBitmapCOW(nil, 1, 32_001, 10, emptyFreeBitmapCOWLedger(arena, nil, nil))
		if problem.failed() {
			t.Fatal(problem)
		}
		cow.committedPageCount = 32_000
		cow.pageCountsDistinct = true
		release := make([]uint32, 1)
		result, problem := cow.releaseUnusedReservations(release, []freeBitmapInsertPage{{}})
		if problem.failed() || result != (unusedReservationRelease{1, 0, 1, 32_000}) {
			t.Fatalf("candidate finalization = %+v/%v", result, problem)
		}
		if release[0] != 0 || cow.root != 11 || arena[0].state != privateBitmapPageReleasedFree ||
			arena[2].state != privateBitmapPageReleasedTail || cow.availablePrivatePages() != 0 {
			t.Fatal("demoted candidate or appended tail remained available")
		}
		requirePrivateFreeBit(t, cow, 10)
	})

	t.Run("appended", func(t *testing.T) {
		arena := []reservedBitmapPage{
			newReservedBitmapPage(100), newReservedBitmapPage(11), newReservedBitmapPage(32_000),
		}
		arena[0].bytes = cowBranch(t, 100, 2, 1, cowChild{index: 0, page: 11}).bytes
		arena[0].state = privateBitmapPageInUse
		arena[0].authorization = privateBitmapPageAppended
		arena[1].bytes = cowLeaf(t, 11, 2, 5).bytes
		arena[1].state = privateBitmapPageInUse
		arena[2].authorization = privateBitmapPageAppended
		cow, problem := newFreeBitmapCOW(nil, 1, 32_001, 100, emptyFreeBitmapCOWLedger(arena, nil, nil))
		if problem.failed() {
			t.Fatal(problem)
		}
		cow.committedPageCount = 100
		cow.pageCountsDistinct = true
		result, problem := cow.releaseUnusedReservations(nil, []freeBitmapInsertPage{{}})
		if problem.failed() || result != (unusedReservationRelease{0, 1, 1, 32_000}) {
			t.Fatalf("appended finalization = %+v/%v", result, problem)
		}
		if cow.root != 11 || arena[0].state != privateBitmapPageReleasedFree ||
			arena[2].state != privateBitmapPageReleasedTail || cow.availablePrivatePages() != 0 {
			t.Fatal("demoted appended page or tail remained available")
		}
		requirePrivateFreeBit(t, cow, 100)
	})
}

func TestFreeBitmapFinalizationDemotionBudgetFailureIsAtomic(t *testing.T) {
	arena := []reservedBitmapPage{
		newReservedBitmapPage(10), newReservedBitmapPage(11), newReservedBitmapPage(32_000),
	}
	arena[0].bytes = cowBranch(t, 10, 2, 1, cowChild{index: 0, page: 11}).bytes
	arena[0].state = privateBitmapPageInUse
	arena[0].authorization = privateBitmapPageCommittedFreeCandidate
	arena[1].bytes = cowLeaf(t, 11, 2, 5).bytes
	arena[1].state = privateBitmapPageInUse
	arena[2].authorization = privateBitmapPageAppended
	cow, problem := newFreeBitmapCOW(nil, 1, 32_001, 10, emptyFreeBitmapCOWLedger(arena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	cow.committedPageCount = 32_000
	cow.pageCountsDistinct = true
	before := append([]reservedBitmapPage(nil), arena...)
	_, problem = cow.releaseUnusedReservations(make([]uint32, 1), nil)
	requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrInsertScratchExhausted)
	if cow.root != 10 || cow.pageCount != 32_001 || cow.replacementLen != 0 {
		t.Fatal("demotion budget failure changed draft metadata")
	}
	for index := range arena {
		if arena[index] != before[index] {
			t.Fatalf("demotion budget failure changed arena slot %d", index)
		}
	}
}

func TestFreeBitmapInsertionAndPlannedApplyAllocateNothing(t *testing.T) {
	plannerSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6, 7)}}
	plannerStorage := newBitmapPlannerStorage(2, 2, 1, 3)
	allocations := testing.AllocsPerRun(100, func() {
		planner, problem := newFreeBitmapReservationPlanner(
			plannerSource, 1, 20, 2, 1, plannerStorage.buffers(),
		)
		if problem.failed() {
			panic("unexpected allocation-test planner construction")
		}
		planned, problem := planner.plan()
		if problem.failed() || len(planned.candidates) != 2 {
			panic("unexpected allocation-test reservation plan")
		}
	})
	if allocations != 0 {
		t.Fatalf("bitmap reservation planner allocations = %v, want 0", allocations)
	}

	const leaves = 32
	pages := make([]uint32, leaves)
	for index := range pages {
		pages[index] = uint32(2 + uint64(index)*BitmapLeafBits)
	}
	arena := make([]reservedBitmapPage, leaves+1)
	for index := range arena {
		arena[index] = newReservedBitmapPage(uint32(10_000 + index))
	}
	cow, problem := newFreeBitmapCOW(nil, 1, mustBitmapCoverage(t, 1), 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	scratch := make([]freeBitmapInsertPage, leaves+1)
	base := *cow
	arenaBefore := append([]reservedBitmapPage(nil), arena...)
	indexBefore := append([]bitmapCOWIndexNode(nil), cow.indexNodes...)
	availableBefore := append([]int(nil), cow.availableSlots...)
	allocations = testing.AllocsPerRun(100, func() {
		copy(arena, arenaBefore)
		copy(cow.indexNodes, indexBefore)
		copy(cow.availableSlots, availableBefore)
		*cow = base
		result, problem := cow.insertFreePages(pages, scratch)
		if problem.failed() || result.newBitmapPages != leaves+1 {
			panic("unexpected allocation-test insertion")
		}
	})
	if allocations != 0 {
		t.Fatalf("bitmap insertion allocations = %v, want 0", allocations)
	}
	requirePrivateFreeBit(t, cow, pages[0])
	requirePrivateFreeBit(t, cow, pages[len(pages)-1])

	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6, 7)}}
	storage := newBitmapPlannerStorage(2, 2, 1, 3)
	planner, problem := newFreeBitmapReservationPlanner(source, 1, 20, 2, 1, storage.buffers())
	if problem.failed() {
		t.Fatal(problem)
	}
	planned, problem := planner.plan()
	if problem.failed() {
		t.Fatal(problem)
	}
	plannedBase := planned
	plannedArena := append([]reservedBitmapPage(nil), planned.pool.slots...)
	plannedIndex := append([]bitmapCOWIndexNode(nil), planned.indexNodes...)
	plannedAvailable := append([]int(nil), planned.availableSlots...)
	allocations = testing.AllocsPerRun(100, func() {
		copy(planned.pool.slots, plannedArena)
		copy(planned.indexNodes, plannedIndex)
		copy(planned.availableSlots, plannedAvailable)
		clear(planned.replacements)
		*(&planned) = plannedBase
		if problem := planned.applyPlannedReservation(); problem.failed() {
			panic("unexpected allocation-test planned apply")
		}
	})
	if allocations != 0 {
		t.Fatalf("planned reservation apply allocations = %v, want 0", allocations)
	}
}

func TestFreeBitmapMutationEpochExhaustionIsAccessFirstAndAtomic(t *testing.T) {
	const exhausted = ^uint64(0)
	denied := pageSourceError{code: pageSourceErrForkedHandle}

	t.Run("direct prepared apply", func(t *testing.T) {
		source := &cowSparsePages{}
		arena := []reservedBitmapPage{newReservedBitmapPage(10)}
		cow, problem := newFreeBitmapCOW(source, 1, 20, 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
		if problem.failed() {
			t.Fatal(problem)
		}
		scratch := []freeBitmapInsertPage{{}}
		preflight, problem := newFreeBitmapInsertPreflight(cow, []uint32{5}, cow.pageCount, scratch)
		if problem.failed() {
			t.Fatal(problem)
		}
		prepared, problem := preflight.plan()
		if problem.failed() {
			t.Fatal(problem)
		}
		cow.mutationEpoch = exhausted
		beforeArena := append([]reservedBitmapPage(nil), arena...)
		beforeRoot := cow.root
		beforeAvailable := cow.availableLen
		beforeIndexLen := cow.indexLen
		source.access = &denied
		allocations := testing.AllocsPerRun(100, func() {
			_, problem = cow.applyPreparedFreeBitmapInsertion(prepared)
			if problem.code != freeBitmapCOWErrSource {
				panic("direct prepared apply did not check access first")
			}
		})
		if allocations != 0 {
			t.Fatalf("denied direct prepared apply allocations = %v, want 0", allocations)
		}
		source.access = nil
		prepared.epoch = exhausted
		allocations = testing.AllocsPerRun(100, func() {
			_, problem = cow.applyPreparedFreeBitmapInsertion(prepared)
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("direct prepared apply did not reject exhausted epoch")
			}
		})
		if allocations != 0 {
			t.Fatalf("epoch-exhausted direct prepared apply allocations = %v, want 0", allocations)
		}
		if cow.mutationEpoch != exhausted || cow.root != beforeRoot || cow.availableLen != beforeAvailable ||
			cow.indexLen != beforeIndexLen || arena[0] != beforeArena[0] {
			t.Fatal("rejected direct prepared apply mutated the draft")
		}
	})

	t.Run("wrappers", func(t *testing.T) {
		arena := []reservedBitmapPage{newReservedBitmapPage(10)}
		cow, problem := newFreeBitmapCOW(nil, 1, 20, 0, emptyFreeBitmapCOWLedger(arena, nil, nil))
		if problem.failed() {
			t.Fatal(problem)
		}
		cow.mutationEpoch = exhausted
		pages := []uint32{5}
		releasePages := []uint32{99}
		beforeArena := arena[0]
		allocations := testing.AllocsPerRun(100, func() {
			_, problem = cow.insertFree(5, nil)
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("single insertion did not reject exhausted epoch")
			}
			_, problem = cow.insertFreePages(pages, nil)
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("batch insertion did not reject exhausted epoch")
			}
			_, problem = cow.releaseUnusedReservations(releasePages, nil)
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("finalizer did not reject exhausted epoch")
			}
		})
		if allocations != 0 {
			t.Fatalf("epoch-exhausted wrapper allocations = %v, want 0", allocations)
		}
		if cow.root != 0 || cow.mutationEpoch != exhausted || cow.singleInsertPage[0] != 0 ||
			arena[0] != beforeArena || releasePages[0] != 99 {
			t.Fatal("epoch-exhausted wrapper mutated draft or caller scratch")
		}
	})

	t.Run("removal and planned apply", func(t *testing.T) {
		removeSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5)}}
		removeArena := []reservedBitmapPage{newReservedBitmapPage(10)}
		removeCow, problem := newFreeBitmapCOW(
			removeSource, 1, 20, 2,
			emptyFreeBitmapCOWLedger(removeArena, make([]uint32, 1), make([]uint32, 1)),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		removeCow.mutationEpoch = exhausted
		beforeArena := removeArena[0]
		removeSource.access = &denied
		allocations := testing.AllocsPerRun(100, func() {
			_, _, problem = removeCow.removeLowest()
			if problem.code != freeBitmapCOWErrSource {
				panic("removal did not check access before exhausted epoch")
			}
		})
		if allocations != 0 || removeSource.reads != 0 || removeCow.root != 2 ||
			removeCow.mutationEpoch != exhausted || removeArena[0] != beforeArena {
			t.Fatalf("access-denied removal = allocations %v, reads %d", allocations, removeSource.reads)
		}
		removeSource.access = nil
		allocations = testing.AllocsPerRun(100, func() {
			_, _, problem = removeCow.removeLowest()
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("removal did not reject exhausted epoch")
			}
		})
		if allocations != 0 || removeSource.reads != 0 || removeCow.root != 2 ||
			removeCow.mutationEpoch != exhausted || removeArena[0] != beforeArena {
			t.Fatalf("epoch-exhausted removal = allocations %v, reads %d", allocations, removeSource.reads)
		}

		planSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6, 7)}}
		storage := newBitmapPlannerStorage(2, 2, 1, 3)
		planner, problem := newFreeBitmapReservationPlanner(planSource, 1, 20, 2, 1, storage.buffers())
		if problem.failed() {
			t.Fatal(problem)
		}
		planned, problem := planner.plan()
		if problem.failed() {
			t.Fatal(problem)
		}
		planned.mutationEpoch = exhausted
		plannedArena := append([]reservedBitmapPage(nil), planned.pool.slots...)
		reads := planSource.reads
		allocations = testing.AllocsPerRun(100, func() {
			problem = planned.applyPlannedReservation()
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				panic("planned apply did not reject exhausted epoch")
			}
		})
		if allocations != 0 || planSource.reads != reads || planned.root != 2 ||
			planned.candidateLen != 0 || planned.mutationEpoch != exhausted {
			t.Fatalf("epoch-exhausted planned apply = allocations %v, reads %d -> %d", allocations, reads, planSource.reads)
		}
		for index := range planned.pool.slots {
			if planned.pool.slots[index] != plannedArena[index] {
				t.Fatalf("epoch-exhausted planned apply mutated arena slot %d", index)
			}
		}
		planned.mutationEpoch = exhausted - 1
		problem = planned.applyPlannedReservation()
		requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrMutationEpochExhausted)
		if planned.root != 2 || planned.candidateLen != 0 || planned.mutationEpoch != exhausted-1 {
			t.Fatal("planned apply consumed a partial prefix without enough epoch headroom")
		}
		for index := range planned.pool.slots {
			if planned.pool.slots[index] != plannedArena[index] {
				t.Fatalf("epoch-headroom rejection mutated arena slot %d", index)
			}
		}
	})
}

func TestFreeBitmapSuccessfulMutationsAdvanceEpochExactlyOnce(t *testing.T) {
	insertArena := []reservedBitmapPage{newReservedBitmapPage(10)}
	insertCow, problem := newFreeBitmapCOW(nil, 1, 20, 0, emptyFreeBitmapCOWLedger(insertArena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	insertCow.mutationEpoch = 41
	if _, problem = insertCow.insertFree(5, []freeBitmapInsertPage{{}}); problem.failed() {
		t.Fatal(problem)
	}
	if insertCow.mutationEpoch != 42 {
		t.Fatalf("insertion epoch = %d, want 42", insertCow.mutationEpoch)
	}

	removeSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5)}}
	removeArena := []reservedBitmapPage{newReservedBitmapPage(10)}
	removeCow, problem := newFreeBitmapCOW(
		removeSource, 1, 20, 2,
		emptyFreeBitmapCOWLedger(removeArena, make([]uint32, 1), make([]uint32, 1)),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	removeCow.mutationEpoch = 41
	if _, found, problem := removeCow.removeLowest(); problem.failed() || !found {
		t.Fatalf("successful removal = found %t/problem %v", found, problem)
	}
	if removeCow.mutationEpoch != 42 {
		t.Fatalf("removal epoch = %d, want 42", removeCow.mutationEpoch)
	}

	planSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6, 7)}}
	storage := newBitmapPlannerStorage(2, 2, 1, 3)
	planner, problem := newFreeBitmapReservationPlanner(planSource, 1, 20, 2, 1, storage.buffers())
	if problem.failed() {
		t.Fatal(problem)
	}
	planned, problem := planner.plan()
	if problem.failed() {
		t.Fatal(problem)
	}
	planned.mutationEpoch = 41
	if problem = planned.applyPlannedReservation(); problem.failed() {
		t.Fatal(problem)
	}
	if planned.mutationEpoch != 43 || planned.candidateLen != 2 {
		t.Fatalf("two-removal planned apply = epoch %d, candidates %d", planned.mutationEpoch, planned.candidateLen)
	}

	finalArena := []reservedBitmapPage{newReservedBitmapPage(20)}
	finalArena[0].authorization = privateBitmapPageAppended
	finalCow, problem := newFreeBitmapCOW(nil, 1, 21, 0, emptyFreeBitmapCOWLedger(finalArena, nil, nil))
	if problem.failed() {
		t.Fatal(problem)
	}
	finalCow.committedPageCount = 20
	finalCow.pageCountsDistinct = true
	finalCow.mutationEpoch = 41
	if _, problem = finalCow.releaseUnusedReservations(nil, nil); problem.failed() {
		t.Fatal(problem)
	}
	if finalCow.mutationEpoch != 42 || finalCow.pageCount != 20 ||
		finalArena[0].state != privateBitmapPageReleasedTail {
		t.Fatalf("finalization = epoch %d, page-count %d, state %d", finalCow.mutationEpoch, finalCow.pageCount, finalArena[0].state)
	}
}

func TestFreeBitmapIntegerBudgetOverflowIsAtomic(t *testing.T) {
	maxInt := int(^uint(0) >> 1)

	t.Run("reservation index total", func(t *testing.T) {
		arena := []reservedBitmapPage{{}}
		planner := freeBitmapReservationPlanner{
			committedPageCount: 2,
			payloadPages:       1,
			indexLen:           maxInt,
			buffers: freeBitmapReservationBuffers{
				arena: arena, availableSlots: make([]int, 1), indexNodes: make([]bitmapCOWIndexNode, 1),
			},
		}
		_, problem := planner.finish(1)
		requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrIndexCapacityOverflow)
		if arena[0] != (reservedBitmapPage{}) {
			t.Fatal("overflowing reservation index total authorized an arena page")
		}
	})

	t.Run("insertion replacement total", func(t *testing.T) {
		cow := &freeBitmapCOW{replacementLen: maxInt}
		scratch := []freeBitmapInsertPage{{origin: freeBitmapInsertOriginCommitted}}
		preflight := freeBitmapInsertPreflight{cow: cow, scratch: scratch, committedReplacements: 1}
		problem := preflight.ensureChanged(0)
		requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrCoverageOverflow)
		if scratch[0].changed {
			t.Fatal("overflowing replacement total changed insertion scratch")
		}
	})

	t.Run("insertion index total", func(t *testing.T) {
		cow := &freeBitmapCOW{indexLen: maxInt, replacements: make([]uint32, 1)}
		scratch := []freeBitmapInsertPage{{origin: freeBitmapInsertOriginCommitted}}
		preflight := freeBitmapInsertPreflight{cow: cow, scratch: scratch}
		problem := preflight.ensureChanged(0)
		requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrIndexCapacityOverflow)
		if scratch[0].changed {
			t.Fatal("overflowing insertion index total changed insertion scratch")
		}
	})

	t.Run("requested plus automatic total", func(t *testing.T) {
		cow := &freeBitmapCOW{committedPageCount: 2, pageCount: 2}
		preflight := freeBitmapInsertPreflight{
			cow: cow, pages: []uint32{2}, autoReleaseLen: maxInt,
			root: 0, desiredLevel: 0, plannedRoot: bitmapCOWNoIndex,
		}
		_, problem := preflight.plan()
		requireFreeBitmapCOWCode(t, problem, freeBitmapCOWErrCoverageOverflow)
		if cow.root != 0 || cow.mutationEpoch != 0 {
			t.Fatal("overflowing combined insertion count mutated the draft")
		}
	})
}
