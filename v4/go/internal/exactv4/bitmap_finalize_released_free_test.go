package exactv4

import (
	"reflect"
	"testing"
)

type releasedFreeFinalizationFixture struct {
	attachment  freeBitmapReservationAttachment
	scratch     freeBitmapFinalizationScratch
	foreign     privatePageReservationScope
	foreignSlot []int
}

func newReleasedFreeFinalizationFixture(
	t *testing.T,
	committedPageCount uint64,
	foreignPages []uint32,
	stampForeign bool,
) releasedFreeFinalizationFixture {
	t.Helper()
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	capacity := newLateBitmapCapacityPlanAt(
		t, source, committedPageCount, 2, 2, &storage,
	)
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool,
		make([]reservedBitmapPage, capacity.privatePages+len(foreignPages)),
		committedPageCount,
		committedPageCount,
		2,
	); problem.failed() {
		t.Fatal(problem)
	}

	var foreign privatePageReservationScope
	if len(foreignPages) != 0 {
		var poolProblem privatePagePoolError
		foreign, poolProblem = pool.reserveScope(len(foreignPages))
		if poolProblem.failed() {
			t.Fatal(poolProblem)
		}
	}
	bitmapScope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(pool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 771, []uint32{7})
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}

	foreignSlots := make([]int, len(foreignPages))
	if len(foreignPages) != 0 {
		checkpoint, bindProblem := pool.begin()
		if bindProblem.failed() {
			t.Fatal(bindProblem)
		}
		for index, pageNumber := range foreignPages {
			slot, bindProblem := pool.bindPage(
				checkpoint, foreign, pageNumber, privatePageReclaimed,
			)
			if bindProblem.failed() {
				t.Fatal(bindProblem)
			}
			foreignSlots[index] = slot
			if !stampForeign || index%5 != 0 {
				continue
			}
			token, claimProblem := pool.claimPageInScope(
				checkpoint,
				foreign,
				pageNumber,
				privatePageOwnerRetirement,
				privatePageRetirementTree,
			)
			if claimProblem.failed() {
				t.Fatal(claimProblem)
			}
			var bytes [PageSize]byte
			bytes[0] = byte(index + 1)
			bytes[PageHeaderSize] = byte(index ^ 0xa5)
			bytes[PageSize-1] = byte(index ^ 0x5a)
			if writeProblem := pool.writePage(token, &bytes); writeProblem.failed() {
				t.Fatal(writeProblem)
			}
		}
		if bindProblem = pool.commit(checkpoint); bindProblem.failed() {
			t.Fatal(bindProblem)
		}
	}

	return releasedFreeFinalizationFixture{
		attachment:  attachment,
		scratch:     finalizationScratchForAttachment(&attachment),
		foreign:     foreign,
		foreignSlot: foreignSlots,
	}
}

func requireSealedFreeBitmapBit(
	t *testing.T,
	output sealedFreeBitmapOutput,
	pageNumber uint32,
) {
	t.Helper()
	level, ok := minimumFreeBitmapLevel(output.pageCount)
	if !ok || output.root == 0 {
		t.Fatalf("free page %d has no representable sealed root", pageNumber)
	}
	current := output.root
	base := uint64(0)
	for {
		var page [PageSize]byte
		if status := output.readPage(current, &page); status.failed() {
			t.Fatalf("read free page %d path page %d = %#v", pageNumber, current, status)
		}
		if level == 0 {
			local := uint64(pageNumber) - base
			if rawFreeBitmapLeafWord(&page, int(local/64))&
				(uint64(1)<<uint(local%64)) == 0 {
				t.Fatalf("sealed free page %d bit is zero", pageNumber)
			}
			return
		}
		span, valid := freeBitmapCoverage(level - 1)
		if !valid || uint64(pageNumber) < base {
			t.Fatalf("invalid sealed free bitmap coverage for page %d", pageNumber)
		}
		childIndex := int((uint64(pageNumber) - base) / span)
		current = rawFreeBitmapBranchChild(&page, childIndex)
		if current == 0 {
			t.Fatalf("sealed free page %d path is absent", pageNumber)
		}
		base += uint64(childIndex) * span
		level--
	}
}

func requireReleasedFreeTargetsDetached(
	t *testing.T,
	result freeBitmapFinalizationResult,
	pages ...uint32,
) {
	t.Helper()
	if result.output.boundLen != 1 {
		t.Fatalf("retained binding count = %d, want 1", result.output.boundLen)
	}
	bound := 0
	for bindingIndex, binding := range result.output.bindings {
		if !binding.bound {
			continue
		}
		bound++
		if bindingIndex >= result.output.boundLen {
			t.Fatalf("binding %d remained bound beyond compact prefix", bindingIndex)
		}
		slot := &result.output.pool.slots[binding.poolSlot]
		if slot.state != privatePageInUse || !slot.inUse {
			t.Fatalf("retained binding %d is not an in-use output: %#v", bindingIndex, *slot)
		}
	}
	if bound != result.output.boundLen {
		t.Fatalf("bound binding count = %d, want %d", bound, result.output.boundLen)
	}
	for _, pageNumber := range pages {
		requireSealedFreeBitmapBit(t, result.output, pageNumber)
		if slot, owned := result.output.pool.slotIndex(pageNumber); owned {
			t.Fatalf(
				"advertised-free page %d remains pool-owned by slot %d in state %d",
				pageNumber,
				slot,
				result.output.pool.slots[slot].state,
			)
		}
	}
}

func TestSelectiveFinalizationDetachesLowMiddleAndHighReturnedFreeTargets(t *testing.T) {
	for _, test := range []struct {
		name         string
		foreignPages []uint32
	}{
		{name: "low", foreignPages: []uint32{11, 13, 15, 17, 19}},
		{name: "middle", foreignPages: []uint32{3, 4, 6, 8, 10, 12}},
		{name: "high", foreignPages: []uint32{3, 4, 6}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleasedFreeFinalizationFixture(t, 64, test.foreignPages, true)
			foreignBefore := make([]privatePagePoolSlot, len(fixture.foreignSlot))
			for index, slot := range fixture.foreignSlot {
				foreignBefore[index] = normalizedForeignLateBitmapSlot(
					fixture.attachment.cow.pool.slots[slot],
				)
			}

			result, problem := fixture.attachment.finalize(fixture.scratch)
			if problem.failed() {
				t.Fatalf("finalize = %#v", problem)
			}
			requireReleasedFreeTargetsDetached(t, result, 7, 9)
			for index, slot := range fixture.foreignSlot {
				got := normalizedForeignLateBitmapSlot(result.output.pool.slots[slot])
				if got != foreignBefore[index] {
					t.Fatalf("foreign slot %d changed outside global index metadata", slot)
				}
			}
			if _, poolProblem := result.output.pool.validateSealedScope(result.output.scope); poolProblem.failed() {
				t.Fatalf("target scope is not sealed: %#v", poolProblem)
			}
			if _, poolProblem := result.output.pool.validateScope(fixture.foreign); poolProblem.failed() {
				t.Fatalf("foreign scope changed: %#v", poolProblem)
			}
		})
	}
}

func TestSelectiveFinalizationReleasedFreeScratchShortageIsAtomicAndRetryable(t *testing.T) {
	for _, field := range []string{"nodes", "path", "targets"} {
		t.Run(field, func(t *testing.T) {
			fixture := newReleasedFreeFinalizationFixture(t, 64, nil, false)
			required, problem := finalizationScratchRequirements(&fixture.attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			if required.cleanupSlots < 2 {
				t.Fatalf(
					"returned-free cleanup target requirement = %d, want coverage for both targets",
					required.cleanupSlots,
				)
			}
			expected, resource := 0, freeBitmapResourceAvailableSlots
			switch field {
			case "nodes":
				expected, resource = required.cleanupNodes, freeBitmapResourceIndexNodes
				fixture.scratch.cleanup.nodes = fixture.scratch.cleanup.nodes[:expected-1]
			case "path":
				expected = required.cleanupPath
				fixture.scratch.cleanup.path = fixture.scratch.cleanup.path[:expected-1]
			case "targets":
				expected = required.cleanupSlots
				fixture.scratch.cleanup.targets = fixture.scratch.cleanup.targets[:expected-1]
			}
			if expected <= 0 {
				t.Fatalf("zero %s requirement", field)
			}

			beforeScope := freeBitmapReservationScopeFingerprint(
				fixture.attachment.cow.pool,
				fixture.attachment.scope,
			)
			beforePool := sealFreeBitmapReservationPool(fixture.attachment.cow.pool)
			beforeNodes := append(
				[]freeBitmapCleanupOverlayNode(nil),
				fixture.scratch.cleanup.nodes...,
			)
			beforePath := append([]int(nil), fixture.scratch.cleanup.path...)
			beforeTargets := append([]int(nil), fixture.scratch.cleanup.targets...)
			if _, problem = fixture.attachment.finalize(fixture.scratch); problem.code != freeBitmapCOWErrInsufficientResourceBudget ||
				problem.resource != resource ||
				problem.required != expected ||
				problem.actual != expected-1 {
				t.Fatalf("short returned-free %s scratch = %#v", field, problem)
			}
			if freeBitmapReservationScopeFingerprint(
				fixture.attachment.cow.pool,
				fixture.attachment.scope,
			) != beforeScope ||
				!beforePool.matches(fixture.attachment.cow.pool) ||
				!reflect.DeepEqual(fixture.scratch.cleanup.nodes, beforeNodes) ||
				!reflect.DeepEqual(fixture.scratch.cleanup.path, beforePath) ||
				!reflect.DeepEqual(fixture.scratch.cleanup.targets, beforeTargets) {
				t.Fatal("short returned-free scratch rejection mutated live state or scratch")
			}

			result, problem := fixture.attachment.finalize(
				finalizationScratchForAttachment(&fixture.attachment),
			)
			if problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
			requireReleasedFreeTargetsDetached(t, result, 7, 9)
		})
	}
}

func TestSelectiveFinalizationReleasedFreeScratchAliasIsAtomicAndRetryable(t *testing.T) {
	for _, alias := range []string{"path-targets", "index-targets"} {
		t.Run(alias, func(t *testing.T) {
			fixture := newReleasedFreeFinalizationFixture(t, 64, nil, false)
			required, problem := finalizationScratchRequirements(&fixture.attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			if required.cleanupSlots < 2 {
				t.Fatalf(
					"returned-free cleanup target requirement = %d, want coverage for both targets",
					required.cleanupSlots,
				)
			}
			sharedLen := required.cleanupSlots
			if required.cleanupPath > sharedLen {
				sharedLen = required.cleanupPath
			}
			if required.indexStack > sharedLen {
				sharedLen = required.indexStack
			}
			shared := make([]int, sharedLen)
			fixture.scratch.cleanup.targets = shared[:required.cleanupSlots]
			switch alias {
			case "path-targets":
				fixture.scratch.cleanup.path = shared[:required.cleanupPath]
			case "index-targets":
				fixture.scratch.indexStack = shared[:required.indexStack]
			}
			beforeScope := freeBitmapReservationScopeFingerprint(
				fixture.attachment.cow.pool,
				fixture.attachment.scope,
			)
			beforePool := sealFreeBitmapReservationPool(fixture.attachment.cow.pool)
			if _, problem = fixture.attachment.finalize(fixture.scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("returned-free scratch alias = %#v", problem)
			}
			if freeBitmapReservationScopeFingerprint(
				fixture.attachment.cow.pool,
				fixture.attachment.scope,
			) != beforeScope ||
				!beforePool.matches(fixture.attachment.cow.pool) {
				t.Fatal("returned-free scratch alias rejection mutated live state")
			}
			result, problem := fixture.attachment.finalize(
				finalizationScratchForAttachment(&fixture.attachment),
			)
			if problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
			requireReleasedFreeTargetsDetached(t, result, 7, 9)
		})
	}
}

func TestSelectiveFinalizationReleasedFreeCleanupAfterThreeUnits(t *testing.T) {
	const (
		units          = 3
		poolCapacity   = 16
		committedPages = 64
	)
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool,
		make([]reservedBitmapPage, poolCapacity),
		committedPages,
		committedPages,
		2,
	); problem.failed() {
		t.Fatal(problem)
	}
	predecessors := make([]freeBitmapFinalizationPredecessor, 0, units)
	for unit := 0; unit < units; unit++ {
		offset := uint32(unit * 10)
		source := &cowSparsePages{pages: []cowSparsePage{
			cowLeaf(t, 2, 1, 5+offset, 9+offset),
		}}
		storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
		capacity := newLateBitmapCapacityPlanAt(
			t, source, committedPages, 2, 2, &storage,
		)
		scope, poolProblem := pool.reserveScope(capacity.privatePages)
		if poolProblem.failed() {
			t.Fatalf("unit %d reserve = %#v", unit, poolProblem)
		}
		attachment, problem := capacity.attach(pool, scope)
		if problem.failed() {
			t.Fatalf("unit %d attach = %#v", unit, problem)
		}
		proof := completeLateBitmapProof(t, &attachment, uint64(800+unit), []uint32{7 + offset})
		if _, problem = attachment.bind(&proof); problem.failed() {
			t.Fatalf("unit %d bind = %#v", unit, problem)
		}
		result, problem := attachment.finalize(
			finalizationScratchForAttachment(&attachment),
		)
		if problem.failed() {
			t.Fatalf("unit %d finalize = %#v", unit, problem)
		}
		requireReleasedFreeTargetsDetached(t, result, 7+offset, 9+offset)
		predecessor, problem := result.successor.consume()
		if problem.failed() {
			t.Fatalf("unit %d successor = %#v", unit, problem)
		}
		predecessors = append(predecessors, predecessor)
	}
	for unit := range predecessors {
		if problem := predecessors[unit].cleanup(); problem.failed() {
			t.Fatalf("unit %d cleanup after three finalized units = %#v", unit, problem)
		}
	}
	if pool.activeScopes != 0 || pool.unscopedVacantCount != poolCapacity {
		t.Fatalf(
			"three-unit cleanup left active=%d vacant=%d/%d",
			pool.activeScopes,
			pool.unscopedVacantCount,
			poolCapacity,
		)
	}
}

func releasedFreeFinalizationWork(
	t *testing.T,
	foreignCount int,
) uint64 {
	t.Helper()
	foreignPages := make([]uint32, foreignCount)
	for index := range foreignPages {
		foreignPages[index] = uint32(index*2 + 4)
	}
	fixture := newReleasedFreeFinalizationFixture(t, 16_384, foreignPages, false)
	visitsBefore := fixture.attachment.cow.scopedMemberVisits
	result, problem := fixture.attachment.finalize(fixture.scratch)
	if problem.failed() {
		t.Fatalf("foreign=%d finalize = %#v", foreignCount, problem)
	}
	requireReleasedFreeTargetsDetached(t, result, 7, 9)
	work := fixture.attachment.cow.scopedMemberVisits - visitsBefore +
		uint64(fixture.attachment.terminalWork.scopeSlotVisits+
			fixture.attachment.terminalWork.indexSlotVisits+
			fixture.attachment.terminalWork.scopeHeaderVisits)
	if work == 0 {
		t.Fatalf("foreign=%d returned-free finalization reported zero work", foreignCount)
	}
	return work
}

func TestSelectiveFinalizationReleasedFreeWorkIsTouchedScopeBounded(t *testing.T) {
	small := releasedFreeFinalizationWork(t, 512)
	large := releasedFreeFinalizationWork(t, 4096)
	if large > small*2 {
		t.Fatalf(
			"fixed returned-free target work grew with foreign scope: 512=%d 4096=%d",
			small,
			large,
		)
	}
}

func TestSelectiveFinalizationReleasedFreeIsZeroAllocation(t *testing.T) {
	fixtures := [2]releasedFreeFinalizationFixture{
		newReleasedFreeFinalizationFixture(t, 512, []uint32{3, 4, 6, 8, 10, 12}, false),
		newReleasedFreeFinalizationFixture(t, 512, []uint32{3, 4, 6, 8, 10, 12}, false),
	}
	results := [2]freeBitmapFinalizationResult{}
	call := 0
	var problem freeBitmapCOWError
	allocations := testing.AllocsPerRun(1, func() {
		results[call], problem = fixtures[call].attachment.finalize(fixtures[call].scratch)
		call++
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("returned-free allocations=%g problem=%#v", allocations, problem)
	}
	for index := range results {
		requireReleasedFreeTargetsDetached(t, results[index], 7, 9)
	}
}

func TestSelectiveFinalizationDetachesReleasedFreeAcrossBranchAndLeafReads(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}),
		cowLeaf(t, 3, 1, 5, 9),
	}}
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(t, source, 32_001, 2, 2, &storage)
	proof := completeLateBitmapProof(t, &attachment, 990, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	result, problem := attachment.finalize(finalizationScratchForAttachment(&attachment))
	if problem.failed() {
		t.Fatal(problem)
	}
	if result.output.boundLen != 2 {
		t.Fatalf("branch/leaf retained bindings = %d, want 2", result.output.boundLen)
	}
	requireSealedFreeBitmapBit(t, result.output, 9)
	if slot, owned := result.output.pool.slotIndex(9); owned {
		t.Fatalf(
			"branch/leaf advertised-free page 9 remains pool-owned by slot %d in state %d",
			slot,
			result.output.pool.slots[slot].state,
		)
	}
	var root [PageSize]byte
	if status := result.output.readPage(result.output.root, &root); status.failed() {
		t.Fatalf("read sealed branch = %#v", status)
	}
	if _, pageProblem := openBitmapBranchNoAlloc(
		root[:],
		result.output.pendingTxn,
		bitmapKindFreePages,
	); pageProblem.code != 0 {
		t.Fatalf("sealed root is not a valid branch: %#v", pageProblem)
	}
	child := rawFreeBitmapBranchChild(&root, 0)
	if child == 0 {
		t.Fatal("sealed branch has no leaf child for returned-free page 9")
	}
	var leaf [PageSize]byte
	if status := result.output.readPage(child, &leaf); status.failed() {
		t.Fatalf("read sealed leaf = %#v", status)
	}
	if _, pageProblem := openBitmapLeafNoAlloc(
		leaf[:],
		result.output.pendingTxn,
		bitmapKindFreePages,
	); pageProblem.code != 0 {
		t.Fatalf("sealed child is not a valid leaf: %#v", pageProblem)
	}
}
