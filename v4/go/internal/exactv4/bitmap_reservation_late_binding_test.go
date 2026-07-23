package exactv4

import (
	"math/bits"
	"reflect"
	"testing"
)

type lateBitmapPlannerStorage struct {
	pool              privatePagePool
	poolValidation    []uint32
	arena             []reservedBitmapPage
	arenaBindings     []bitmapCOWArenaBinding
	candidates        []uint32
	verified          []verifiedBitmapPage
	replacements      []uint32
	indexNodes        []bitmapCOWIndexNode
	availableSlots    []int
	sourceNodes       []freeBitmapReservationSourceNode
	reclamation       freeBitmapReclamationTicket
	stageCOW          freeBitmapCOW
	stagePool         privatePagePool
	stageValidation   []uint32
	stageArena        []reservedBitmapPage
	stageBindings     []bitmapCOWArenaBinding
	stageReplacements []uint32
	stageIndex        []bitmapCOWIndexNode
	stageAvailable    []int
}

func newLateBitmapPlannerStorage(capacity, candidates, verified, sources int) lateBitmapPlannerStorage {
	index := capacity + candidates + verified*2
	return lateBitmapPlannerStorage{
		poolValidation: make([]uint32, capacity), arena: make([]reservedBitmapPage, capacity),
		arenaBindings: make([]bitmapCOWArenaBinding, capacity), candidates: make([]uint32, candidates),
		verified: make([]verifiedBitmapPage, verified), replacements: make([]uint32, verified),
		indexNodes: make([]bitmapCOWIndexNode, index), availableSlots: make([]int, capacity),
		sourceNodes:     make([]freeBitmapReservationSourceNode, sources),
		stageValidation: make([]uint32, capacity), stageArena: make([]reservedBitmapPage, capacity),
		stageBindings: make([]bitmapCOWArenaBinding, capacity), stageReplacements: make([]uint32, verified),
		stageIndex: make([]bitmapCOWIndexNode, index), stageAvailable: make([]int, capacity),
	}
}

func (s *lateBitmapPlannerStorage) buffers() freeBitmapReservationBuffers {
	return freeBitmapReservationBuffers{
		poolValidation: s.poolValidation, arenaBindings: s.arenaBindings,
		candidates: s.candidates, verifiedPages: s.verified, replacements: s.replacements,
		indexNodes: s.indexNodes, availableSlots: s.availableSlots, sourceNodes: s.sourceNodes,
		reclamation: &s.reclamation,
		stage: freeBitmapReservationStageBuffers{
			cow: &s.stageCOW, pool: &s.stagePool, poolValidation: s.stageValidation, arena: s.stageArena,
			arenaBindings: s.stageBindings, replacements: s.stageReplacements,
			indexNodes: s.stageIndex, availableSlots: s.stageAvailable,
		},
	}
}

func newLateBitmapPlan(
	t *testing.T,
	source committedPageSource,
	root uint32,
	payload int,
	storage *lateBitmapPlannerStorage,
) freeBitmapReservationAttachment {
	return newLateBitmapPlanAt(t, source, 20, root, payload, storage)
}

func newLateBitmapPlanAt(
	t *testing.T,
	source committedPageSource,
	committedPageCount uint64,
	root uint32,
	payload int,
	storage *lateBitmapPlannerStorage,
) freeBitmapReservationAttachment {
	t.Helper()
	plan := newLateBitmapCapacityPlanAt(t, source, committedPageCount, root, payload, storage)
	if poolProblem := initVacantPrivatePagePool(
		&storage.pool, storage.arena, committedPageCount, committedPageCount, 2,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	scope, poolProblem := storage.pool.reserveScope(plan.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := plan.attach(&storage.pool, scope)
	if problem.failed() {
		t.Fatal(problem)
	}
	return attachment
}

func newLateBitmapCapacityPlanAt(
	t *testing.T,
	source committedPageSource,
	committedPageCount uint64,
	root uint32,
	payload int,
	storage *lateBitmapPlannerStorage,
) freeBitmapReservationCapacityPlan {
	t.Helper()
	planner, problem := newFreeBitmapReservationPlanner(source, 1, committedPageCount, root, payload, storage.buffers())
	if problem.failed() {
		t.Fatal(problem)
	}
	plan, problem := planner.planCapacity()
	if problem.failed() {
		t.Fatal(problem)
	}
	return plan
}

func completeLateBitmapProof(
	t *testing.T,
	plan *freeBitmapReservationAttachment,
	batch uint64,
	pages []uint32,
) freeBitmapReclamationProof {
	t.Helper()
	proof, problem := completeFreeBitmapReclamation(plan.reclamationRequest, batch, pages)
	if problem.failed() {
		t.Fatal(problem)
	}
	return proof
}

func bindForeignLateBitmapPage(
	t *testing.T,
	pool *privatePagePool,
	scope privatePageReservationScope,
	pageNumber uint32,
	authorization privatePageAuthorization,
	claim bool,
) int {
	t.Helper()
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	index, problem := pool.bindPage(checkpoint, scope, pageNumber, authorization)
	if problem.failed() {
		t.Fatal(problem)
	}
	if claim {
		token, claimProblem := pool.claimPageInScope(
			checkpoint, scope, pageNumber, privatePageOwnerRetirement, privatePageRetirementTree,
		)
		if claimProblem.failed() {
			t.Fatal(claimProblem)
		}
		var bytes [PageSize]byte
		bytes[0], bytes[PageSize-1] = 0xa5, 0x5a
		if writeProblem := pool.writePage(token, &bytes); writeProblem.failed() {
			t.Fatal(writeProblem)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	return index
}

func normalizedForeignLateBitmapSlot(slot privatePagePoolSlot) privatePagePoolSlot {
	slot.indexLeft, slot.indexRight, slot.indexHeight = 0, 0, 0
	slot.indexFree, slot.indexInUse = 0, 0
	slot.indexCheckpointID = 0
	slot.indexCheckpointNext = 0
	slot.checkpointIndexLeft, slot.checkpointIndexRight, slot.checkpointIndexHeight = 0, 0, 0
	slot.checkpointIndexFree, slot.checkpointIndexInUse = 0, 0
	slot.checkpointScopeLeft, slot.checkpointScopeRight, slot.checkpointScopeHeight = 0, 0, 0
	slot.checkpointScopeFree, slot.checkpointScopeInUse = 0, 0
	return slot
}

func normalizedForeignLateBitmapScopeFingerprint(
	pool *privatePagePool,
	scope privatePageReservationScope,
) uint64 {
	hash := retirementHashUint64(retirementHashOffset, scope.id)
	member, capacity, problem := pool.scopeMemberStart(scope)
	if problem.failed() {
		return retirementHashUint64(hash, ^uint64(0))
	}
	for visited := 0; visited < capacity; visited++ {
		if member < 0 || member >= len(pool.slots) {
			return retirementHashUint64(hash, ^uint64(0)-1)
		}
		slot := normalizedForeignLateBitmapSlot(pool.slots[member])
		hash = retirementPoolSlotHash(hash, member, &slot)
		member = slot.scopeMemberNext
	}
	if member != privatePagePoolNoIndex {
		return retirementHashUint64(hash, ^uint64(0)-2)
	}
	return hash
}

func TestFreeBitmapCapacityPlanIsPoolAndScopePure(t *testing.T) {
	storage := newLateBitmapPlannerStorage(8, 8, 8, 16)
	for index := range storage.poolValidation {
		storage.poolValidation[index] = uint32(100 + index)
	}
	poolValidation := append([]uint32(nil), storage.poolValidation...)
	arena := append([]reservedBitmapPage(nil), storage.arena...)
	buffers := storage.buffers()
	buffers.pool = nil
	buffers.arena = nil
	planner, problem := newFreeBitmapReservationPlanner(
		&cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		1, 20, 2, 2, buffers,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	plan, problem := planner.planCapacity()
	if problem.failed() {
		t.Fatal(problem)
	}
	if plan.privatePages != 3 || plan.buffers.pool != nil || plan.buffers.arena != nil ||
		storage.pool.self != nil || storage.pool.activeScopes != 0 ||
		storage.reclamation.state.Load() != 0 ||
		!reflect.DeepEqual(storage.poolValidation, poolValidation) || !reflect.DeepEqual(storage.arena, arena) {
		t.Fatal("capacity planning retained or initialized legacy pool/arena authority")
	}

	sharedArena := make([]reservedBitmapPage, plan.privatePages)
	var sharedPool privatePagePool
	if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	scope, poolProblem := sharedPool.reserveScope(plan.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := plan.attach(&sharedPool, scope)
	if problem.failed() || attachment.buffers.pool != nil || attachment.buffers.arena != nil {
		t.Fatalf("external shared-pool attach = %#v", problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 100, []uint32{3, 7})
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatalf("external shared-pool bind = %#v", problem)
	}
}

func TestFreeBitmapCapacityPlanDropsLegacyPoolAndArenaAuthority(t *testing.T) {
	storage := newLateBitmapPlannerStorage(3, 2, 1, 4)
	buffers := storage.buffers()
	buffers.pool = &storage.pool
	buffers.arena = storage.arena
	planner, problem := newFreeBitmapReservationPlanner(
		&cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		1, 20, 2, 2, buffers,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	plan, problem := planner.planCapacity()
	if problem.failed() {
		t.Fatal(problem)
	}
	if plan.buffers.pool != nil || plan.buffers.arena != nil || storage.pool.self != nil {
		t.Fatal("capacity plan retained legacy pool or arena authority")
	}
	for index := range storage.arena {
		if storage.arena[index] != (reservedBitmapPage{}) {
			t.Fatalf("capacity planning changed legacy arena slot %d", index)
		}
	}
}

func TestFreeBitmapAttachmentPreservesBoundForeignScope(t *testing.T) {
	storage := newLateBitmapPlannerStorage(8, 8, 8, 16)
	capacity := newLateBitmapCapacityPlanAt(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 20, 2, 2, &storage,
	)
	sharedArena := make([]reservedBitmapPage, capacity.privatePages+2)
	var sharedPool privatePagePool
	if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	foreign, problem := sharedPool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	foreignSlot := bindForeignLateBitmapPage(t, &sharedPool, foreign, 3, privatePageReclaimed, true)
	bitmapScope, problem := sharedPool.reserveScope(capacity.privatePages)
	if problem.failed() {
		t.Fatal(problem)
	}
	foreignBefore := normalizedForeignLateBitmapSlot(sharedPool.slots[foreignSlot])
	rootBefore := sharedPool.slots[foreign.anchor].scopeRoot
	attachment, attachProblem := capacity.attach(&sharedPool, bitmapScope)
	if attachProblem.failed() {
		t.Fatal(attachProblem)
	}
	proof := completeLateBitmapProof(t, &attachment, 101, []uint32{7})
	binding, bindProblem := attachment.bind(&proof)
	if bindProblem.failed() || binding != (freeBitmapReservationBinding{committed: 2, reclaimed: 1}) {
		t.Fatalf("shared bind = %+v/%#v", binding, bindProblem)
	}
	foreignAfter := normalizedForeignLateBitmapSlot(sharedPool.slots[foreignSlot])
	if foreignAfter != foreignBefore || sharedPool.slots[foreign.anchor].scopeRoot != rootBefore ||
		sharedPool.slots[foreignSlot].pageNumber != 3 || sharedPool.slots[foreignSlot].bytes[0] != 0xa5 ||
		sharedPool.slots[foreignSlot].bytes[PageSize-1] != 0x5a {
		t.Fatal("bitmap bind changed foreign scope authority, state, epoch, or bytes")
	}
	for _, page := range boundLateBitmapPages(t, &attachment) {
		if page == 3 {
			t.Fatal("bitmap scope reused the foreign page")
		}
	}
}

func TestFreeBitmapSharedGlobalAVLHeadroomAndRollback(t *testing.T) {
	type fixture struct {
		storage      lateBitmapPlannerStorage
		capacity     freeBitmapReservationCapacityPlan
		attachment   freeBitmapReservationAttachment
		pool         privatePagePool
		bitmapScope  privatePageReservationScope
		foreignScope privatePageReservationScope
		foreignSlots [3]int
	}
	build := func(t *testing.T, mutationEpoch uint64) *fixture {
		t.Helper()
		result := &fixture{storage: newLateBitmapPlannerStorage(8, 4, 4, 8)}
		result.capacity = newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, 20, 0, 3, &result.storage)
		if problem := initVacantPrivatePagePool(
			&result.pool, make([]reservedBitmapPage, 8), 20, 20, 2,
		); problem.failed() {
			t.Fatal(problem)
		}
		var problem privatePagePoolError
		result.foreignScope, problem = result.pool.reserveScope(3)
		if problem.failed() {
			t.Fatal(problem)
		}
		for index, page := range [3]uint32{4, 8, 12} {
			result.foreignSlots[index] = bindForeignLateBitmapPage(
				t, &result.pool, result.foreignScope, page, privatePageReclaimed, index == 1,
			)
		}
		result.bitmapScope, problem = result.pool.reserveScope(result.capacity.privatePages)
		if problem.failed() {
			t.Fatal(problem)
		}
		result.pool.mutationEpoch = mutationEpoch
		var attachProblem freeBitmapCOWError
		result.attachment, attachProblem = result.capacity.attach(&result.pool, result.bitmapScope)
		if attachProblem.failed() {
			t.Fatal(attachProblem)
		}
		return result
	}

	t.Run("rotation-rollback", func(t *testing.T) {
		state := build(t, 100)
		proof := completeLateBitmapProof(t, &state.attachment, 105, []uint32{3, 5, 7})
		if problem := state.attachment.validateStageBuffers(proof.pages); problem.failed() {
			t.Fatal(problem)
		}
		reclaimed, problem := state.attachment.consumeReclamationProof(&proof)
		if problem.failed() {
			t.Fatal(problem)
		}
		reclaimedRoot, problem := state.attachment.buildReclaimedSource(reclaimed)
		if problem.failed() {
			t.Fatal(problem)
		}
		defer clear(state.attachment.buffers.sourceNodes[state.attachment.committedSourceLen : state.attachment.committedSourceLen+len(reclaimed)])
		selected, selectedCommitted, problem := state.attachment.selectPhysicalPages(reclaimed, reclaimedRoot)
		if problem.failed() {
			t.Fatal(problem)
		}
		shadow, problem := state.attachment.buildShadow(selected, selectedCommitted)
		if problem.failed() {
			t.Fatal(problem)
		}
		checkpoint, problem := state.attachment.preflightRealApply(shadow, selected)
		if problem.failed() {
			t.Fatal(problem)
		}
		rootBefore := state.pool.indexRoot
		statusBefore, _ := state.pool.status()
		foreignBefore := [3]privatePagePoolSlot{}
		foreignIndexBefore := [3][5]int64{}
		for index, slotIndex := range state.foreignSlots {
			slot := state.pool.slots[slotIndex]
			foreignBefore[index] = normalizedForeignLateBitmapSlot(slot)
			foreignIndexBefore[index] = [5]int64{
				int64(slot.indexLeft), int64(slot.indexRight), int64(slot.indexHeight),
				int64(slot.indexFree), int64(slot.indexInUse),
			}
		}
		state.pool.beginCheckpointPrepared(checkpoint)
		for index := 0; index < selected; index++ {
			page := state.attachment.buffers.stage.poolValidation[index]
			state.pool.bindPageForCheckpointPrepared(checkpoint, state.bitmapScope, page, privatePageReclaimed)
		}
		rotated := false
		for index, slotIndex := range state.foreignSlots {
			slot := state.pool.slots[slotIndex]
			current := [5]int64{
				int64(slot.indexLeft), int64(slot.indexRight), int64(slot.indexHeight),
				int64(slot.indexFree), int64(slot.indexInUse),
			}
			rotated = rotated || current != foreignIndexBefore[index]
		}
		if !rotated {
			t.Fatal("interleaved prepared binds did not exercise foreign global-AVL updates")
		}
		if rollbackProblem := state.pool.rollback(checkpoint); rollbackProblem.failed() {
			t.Fatal(rollbackProblem)
		}
		statusAfter, _ := state.pool.status()
		if state.pool.indexRoot != rootBefore || statusAfter.pendingPageCount != statusBefore.pendingPageCount ||
			statusAfter.generation != statusBefore.generation {
			t.Fatal("rollback did not restore global root, tail, and generation")
		}
		for index, slotIndex := range state.foreignSlots {
			if normalizedForeignLateBitmapSlot(state.pool.slots[slotIndex]) != foreignBefore[index] {
				t.Fatalf("rollback changed foreign slot %d", index)
			}
		}
		fresh, attachProblem := state.capacity.attach(&state.pool, state.bitmapScope)
		if attachProblem.failed() {
			t.Fatal(attachProblem)
		}
		freshProof := completeLateBitmapProof(t, &fresh, 106, []uint32{3, 5, 7})
		if _, bindProblem := fresh.bind(&freshProof); bindProblem.failed() {
			t.Fatalf("fresh bind after exact rollback = %#v", bindProblem)
		}
	})

	t.Run("logical-mutation-headroom", func(t *testing.T) {
		// Three forward binds and three possible logical rollback transitions
		// consume six epochs. Foreign global-AVL journal nodes consume none.
		const required = uint64(6)
		minusOne := build(t, ^uint64(0)-(required-1))
		proof := completeLateBitmapProof(t, &minusOne.attachment, 107, []uint32{3, 5, 7})
		before := snapshotLateBitmapLive(t, &minusOne.attachment)
		if _, problem := minusOne.attachment.bind(&proof); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("logical mutation headroom minus one = %#v", problem)
		}
		requireLateBitmapLiveSnapshot(t, &minusOne.attachment, before)

		exact := build(t, ^uint64(0)-required)
		exactProof := completeLateBitmapProof(t, &exact.attachment, 108, []uint32{3, 5, 7})
		if _, problem := exact.attachment.bind(&exactProof); problem.failed() {
			t.Fatalf("exact logical mutation headroom = %#v", problem)
		}
	})
}

func TestFreeBitmapAttachmentRejectsStaleInitialPredecessor(t *testing.T) {
	tests := []struct {
		name          string
		foreignPage   uint32
		authorization privatePageAuthorization
	}{
		{name: "eligible-candidate", foreignPage: 5, authorization: privatePageCommittedFree},
		{name: "live-tail", foreignPage: 20, authorization: privatePageAppended},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(8, 8, 8, 16)
			capacity := newLateBitmapCapacityPlanAt(
				t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 20, 2, 2, &storage,
			)
			sharedArena := make([]reservedBitmapPage, capacity.privatePages+1)
			var sharedPool privatePagePool
			if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
				t.Fatal(problem)
			}
			foreign, problem := sharedPool.reserveScope(1)
			if problem.failed() {
				t.Fatal(problem)
			}
			bindForeignLateBitmapPage(t, &sharedPool, foreign, test.foreignPage, test.authorization, false)
			bitmapScope, problem := sharedPool.reserveScope(capacity.privatePages)
			if problem.failed() {
				t.Fatal(problem)
			}
			beforePool := sharedPool
			beforePool.slots = nil
			beforeSlots := append([]reservedBitmapPage(nil), sharedPool.slots...)
			if _, attachProblem := capacity.attach(&sharedPool, bitmapScope); attachProblem.code != freeBitmapCOWErrStaleReservationPredecessor {
				t.Fatalf("stale predecessor = %#v", attachProblem)
			}
			afterPool := sharedPool
			afterPool.slots = nil
			if !reflect.DeepEqual(beforePool, afterPool) || !reflect.DeepEqual(beforeSlots, sharedPool.slots) || storage.reclamation.state.Load() != 0 {
				t.Fatal("stale predecessor rejection changed shared pool or issued authority")
			}
		})
	}
}

func TestFreeBitmapAttachmentRejectsPoolDriftAndReattaches(t *testing.T) {
	storage := newLateBitmapPlannerStorage(4, 2, 2, 8)
	capacity := newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, 20, 0, 2, &storage)
	sharedArena := make([]reservedBitmapPage, capacity.privatePages+1)
	var sharedPool privatePagePool
	if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	bitmapScope, problem := sharedPool.reserveScope(capacity.privatePages)
	if problem.failed() {
		t.Fatal(problem)
	}
	first, attachProblem := capacity.attach(&sharedPool, bitmapScope)
	if attachProblem.failed() {
		t.Fatal(attachProblem)
	}
	proof := completeLateBitmapProof(t, &first, 102, []uint32{3, 7})
	if _, problem = sharedPool.reserveScope(1); problem.failed() {
		t.Fatal(problem)
	}
	before := append([]reservedBitmapPage(nil), sharedPool.slots...)
	statusBefore, _ := sharedPool.status()
	if _, bindProblem := first.bind(&proof); bindProblem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("pool drift = %#v", bindProblem)
	}
	statusAfter, _ := sharedPool.status()
	if statusAfter != statusBefore || !reflect.DeepEqual(before, sharedPool.slots) {
		t.Fatal("stale attachment mutated the shared pool")
	}
	second, attachProblem := capacity.attach(&sharedPool, bitmapScope)
	if attachProblem.failed() {
		t.Fatal(attachProblem)
	}
	fresh := completeLateBitmapProof(t, &second, 103, []uint32{3, 7})
	if _, bindProblem := second.bind(&fresh); bindProblem.failed() {
		t.Fatalf("fresh attachment failed after pool drift: %#v", bindProblem)
	}
}

func setLateBitmapUint32Scratch(
	buffers *freeBitmapReservationBuffers,
	index int,
	values []uint32,
) {
	switch index {
	case 0:
		buffers.poolValidation = values
	case 1:
		buffers.candidates = values
	case 2:
		buffers.replacements = values
	case 3:
		buffers.stage.poolValidation = values
	case 4:
		buffers.stage.replacements = values
	}
}

func TestFreeBitmapReservationRejectsEveryStaticUint32AliasBeforePlanningWrites(t *testing.T) {
	names := [5]string{"pool-validation", "candidates", "replacements", "stage-selection", "stage-replacements"}
	for left := 0; left < len(names); left++ {
		for right := left + 1; right < len(names); right++ {
			t.Run(names[left]+"-"+names[right], func(t *testing.T) {
				storage := newLateBitmapPlannerStorage(8, 8, 8, 16)
				buffers := storage.buffers()
				shared := make([]uint32, 8)
				setLateBitmapUint32Scratch(&buffers, left, shared)
				setLateBitmapUint32Scratch(&buffers, right, shared)
				if _, problem := newFreeBitmapReservationPlanner(&cowSparsePages{}, 1, 20, 0, 2, buffers); problem.code != freeBitmapCOWErrArenaPageConflict {
					t.Fatalf("static alias = %#v", problem)
				}
				if !equalU32(shared, make([]uint32, len(shared))) || storage.pool.self != nil ||
					storage.reclamation.state.Load() != 0 || storage.sourceNodes[0] != (freeBitmapReservationSourceNode{}) {
					t.Fatal("static alias rejection wrote planning or authority state")
				}
			})
		}
	}
}

func TestFreeBitmapReservationRejectsEveryProofUint32AliasBeforeConsumption(t *testing.T) {
	names := [5]string{"pool-validation", "candidates", "replacements", "stage-selection", "stage-replacements"}
	for index, name := range names {
		t.Run(name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(4, 4, 4, 8)
			attachment := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
			aliased := make([]uint32, 4)
			aliased[0], aliased[1] = 3, 7
			setLateBitmapUint32Scratch(&attachment.buffers, index, aliased)
			proof := completeLateBitmapProof(t, &attachment, 104, aliased[:2])
			before := snapshotLateBitmapLive(t, &attachment)
			if _, problem := attachment.bind(&proof); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("proof alias = %#v", problem)
			}
			requireLateBitmapLiveSnapshot(t, &attachment, before)
			setLateBitmapUint32Scratch(&attachment.buffers, index, make([]uint32, 4))
			if _, problem := attachment.bind(&proof); problem.failed() {
				t.Fatalf("pre-consumption proof alias prevented retry: %#v", problem)
			}
		})
	}
}

func TestFreeBitmapLateBindingMergesCommittedAndReclaimedGlobally(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
	storage := newLateBitmapPlannerStorage(8, 8, 8, 16)
	plan := newLateBitmapPlan(t, source, 2, 2, &storage)
	status, poolProblem := plan.cow.pool.status()
	if poolProblem.failed() || status.pendingPageCount != 20 || plan.privatePages != 3 ||
		!equalU32(plan.cow.candidates, []uint32{5, 9}) || plan.cow.candidateLen != 0 {
		t.Fatalf("capacity plan = status %#v problem %#v pages %d candidates %v/%d",
			status, poolProblem, plan.privatePages, plan.cow.candidates, plan.cow.candidateLen)
	}
	for index, slot := range plan.cow.pool.slots {
		if slot.bound || slot.pageNumber != 0 || slot.authorization != privatePageAuthorizationNone ||
			slot.state != privatePageAvailable || slot.bytes != ([PageSize]byte{}) {
			t.Fatalf("pre-lock slot %d is not vacant: %#v", index, slot)
		}
	}
	proof := completeLateBitmapProof(t, &plan, 1, []uint32{3, 7})
	binding, problem := plan.bind(&proof)
	if problem.failed() {
		t.Fatalf("bind = %#v", problem)
	}
	if binding != (freeBitmapReservationBinding{committed: 1, reclaimed: 2}) {
		t.Fatalf("binding = %#v", binding)
	}
	for index, expected := range []struct {
		page uint32
		auth privatePageAuthorization
	}{{3, privatePageReclaimed}, {5, privatePageCommittedFree}, {7, privatePageReclaimed}} {
		info, problem := plan.cow.pool.slotInfo(index)
		if problem.failed() || info.pageNumber != expected.page || info.authorization != expected.auth {
			t.Fatalf("slot %d = %#v problem %#v", index, info, problem)
		}
	}
	status, _ = plan.cow.pool.status()
	if status.pendingPageCount != 20 || !equalU32(plan.cow.candidatePages(), []uint32{5}) {
		t.Fatalf("bound status %#v candidates %v", status, plan.cow.candidatePages())
	}
	requirePrivateFreeBit(t, &plan.cow, 9)
}

func TestFreeBitmapLateBindingRejectsProofReplayAndLeavesLiveStateUntouched(t *testing.T) {
	storage := newLateBitmapPlannerStorage(4, 4, 2, 8)
	plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
	beforePool := append([]reservedBitmapPage(nil), plan.cow.pool.slots...)
	beforeCOW := plan.cow
	proof := completeLateBitmapProof(t, &plan, 7, []uint32{3, 7})
	proof.pages[0] = 4
	if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("fingerprint failure = %#v", problem)
	}
	if !reflect.DeepEqual(beforePool, plan.cow.pool.slots) || beforeCOW.root != plan.cow.root ||
		beforeCOW.pageCount != plan.cow.pageCount || beforeCOW.candidateLen != plan.cow.candidateLen {
		t.Fatal("failed proof changed live pool/COW")
	}
	proof.pages[0] = 3
	if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("proof replay = %#v", problem)
	}
}

func boundLateBitmapPages(t *testing.T, plan *freeBitmapReservationAttachment) []uint32 {
	t.Helper()
	pages := make([]uint32, 0, plan.privatePages)
	for _, binding := range plan.cow.arenaBindings[:plan.privatePages] {
		info, problem := plan.cow.pool.slotInfo(binding.poolSlot)
		if problem.failed() {
			t.Fatal(problem)
		}
		if info.bound {
			pages = append(pages, info.pageNumber)
		}
	}
	return pages
}

func TestFreeBitmapLateBindingCoversZeroPartialAllAndAppendSources(t *testing.T) {
	tests := []struct {
		name      string
		reclaimed []uint32
		want      []uint32
		binding   freeBitmapReservationBinding
	}{
		{name: "partial-committed", reclaimed: []uint32{3, 7}, want: []uint32{3, 5, 7}, binding: freeBitmapReservationBinding{committed: 1, reclaimed: 2}},
		{name: "all-committed", want: []uint32{5, 9, 20}, binding: freeBitmapReservationBinding{committed: 2, appended: 1}},
		{name: "reclaimed-then-append", reclaimed: []uint32{3}, want: []uint32{3, 5, 9}, binding: freeBitmapReservationBinding{committed: 2, reclaimed: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			plan := newLateBitmapPlan(t, source, 2, 2, &storage)
			selectionID := uint64(10)
			if len(test.reclaimed) == 0 {
				selectionID = 0
			}
			proof := completeLateBitmapProof(t, &plan, selectionID, test.reclaimed)
			binding, problem := plan.bind(&proof)
			if problem.failed() {
				t.Fatalf("bind = %#v", problem)
			}
			if binding != test.binding || !equalU32(boundLateBitmapPages(t, &plan), test.want) {
				t.Fatalf("binding/pages = %#v/%v, want %#v/%v", binding, boundLateBitmapPages(t, &plan), test.binding, test.want)
			}
			status, _ := plan.cow.pool.status()
			wantCount := uint64(20 + test.binding.appended)
			if status.pendingPageCount != wantCount {
				t.Fatalf("pending page count = %d, want %d", status.pendingPageCount, wantCount)
			}
		})
	}

	t.Run("zero-committed-prefix", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
		plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 9, 10)}}, 2, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 9, []uint32{3, 4, 7})
		binding, problem := plan.bind(&proof)
		if problem.failed() || binding != (freeBitmapReservationBinding{reclaimed: 3}) ||
			!equalU32(boundLateBitmapPages(t, &plan), []uint32{3, 4, 7}) {
			t.Fatalf("zero prefix = %#v %#v %v", binding, problem, boundLateBitmapPages(t, &plan))
		}
	})

	t.Run("reclaimed-only", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
		plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 3, &storage)
		proof := completeLateBitmapProof(t, &plan, 11, []uint32{3, 7, 9})
		binding, problem := plan.bind(&proof)
		if problem.failed() || binding != (freeBitmapReservationBinding{reclaimed: 3}) ||
			!equalU32(boundLateBitmapPages(t, &plan), []uint32{3, 7, 9}) {
			t.Fatalf("reclaimed only = %#v %#v %v", binding, problem, boundLateBitmapPages(t, &plan))
		}
	})

	t.Run("append-only", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
		plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 3, &storage)
		proof := completeLateBitmapProof(t, &plan, 0, nil)
		binding, problem := plan.bind(&proof)
		if problem.failed() || binding != (freeBitmapReservationBinding{appended: 3}) ||
			!equalU32(boundLateBitmapPages(t, &plan), []uint32{20, 21, 22}) {
			t.Fatalf("append only = %#v %#v %v", binding, problem, boundLateBitmapPages(t, &plan))
		}
	})
}

func TestFreeBitmapLateBindingRejectsInvalidReclaimedSetsAtomically(t *testing.T) {
	tests := []struct {
		name  string
		pages []uint32
	}{
		{name: "duplicate", pages: []uint32{3, 3}},
		{name: "order", pages: []uint32{7, 3}},
		{name: "low-oob", pages: []uint32{1}},
		{name: "high-oob", pages: []uint32{20}},
		{name: "candidate-conflict", pages: []uint32{5}},
		{name: "bitmap-path-conflict", pages: []uint32{2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
			beforePool := append([]reservedBitmapPage(nil), plan.cow.pool.slots...)
			beforeCount := plan.cow.pageCount
			proof := completeLateBitmapProof(t, &plan, 20, test.pages)
			if _, problem := plan.bind(&proof); !problem.failed() {
				t.Fatal("invalid reclaimed set was accepted")
			}
			if !reflect.DeepEqual(beforePool, plan.cow.pool.slots) || plan.cow.pageCount != beforeCount || plan.cow.candidateLen != 0 {
				t.Fatal("rejected reclaimed set changed live state")
			}
		})
	}
}

func TestFreeBitmapLateBindingProofCapabilityIsMoveOnlyAndRequestBound(t *testing.T) {
	firstStorage := newLateBitmapPlannerStorage(4, 2, 2, 8)
	secondStorage := newLateBitmapPlannerStorage(4, 2, 2, 8)
	first := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &firstStorage)
	second := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &secondStorage)
	forged := first.reclamationRequest
	forged.poolGeneration++
	if _, problem := completeFreeBitmapReclamation(forged, 30, []uint32{3, 7}); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("cross-generation completion = %#v", problem)
	}
	forged = first.reclamationRequest
	forged.candidateFingerprint++
	if _, problem := completeFreeBitmapReclamation(forged, 30, []uint32{3, 7}); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("cross-source completion = %#v", problem)
	}
	proof := completeLateBitmapProof(t, &first, 30, []uint32{3, 7})
	if _, problem := completeFreeBitmapReclamation(first.reclamationRequest, 31, []uint32{3, 7}); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("completion replay = %#v", problem)
	}
	secondPool := append([]reservedBitmapPage(nil), second.cow.pool.slots...)
	if _, problem := second.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("cross request = %#v", problem)
	}
	if !reflect.DeepEqual(secondPool, second.cow.pool.slots) {
		t.Fatal("cross-request proof changed destination pool")
	}
	if _, problem := first.bind(&proof); problem.failed() {
		t.Fatalf("cross-request rejection consumed owner proof: %#v", problem)
	}
	if _, problem := first.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("bind replay = %#v", problem)
	}
}

func TestFreeBitmapLateBindingMissingOrLateFailedInputNeverBinds(t *testing.T) {
	for _, stage := range []string{"first-pass", "second-pass", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(4, 2, 2, 8)
			plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
			before := append([]reservedBitmapPage(nil), plan.cow.pool.slots...)
			if _, problem := plan.bind(nil); problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("missing verifier capability = %#v", problem)
			}
			if !reflect.DeepEqual(before, plan.cow.pool.slots) || plan.cow.pageCount != 20 {
				t.Fatal("missing verifier capability bound a page")
			}
		})
	}

	t.Run("late-source-access", func(t *testing.T) {
		source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
		storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
		plan := newLateBitmapPlan(t, source, 2, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 41, []uint32{3, 7})
		before := append([]reservedBitmapPage(nil), plan.cow.pool.slots...)
		source.access = &pageSourceError{code: pageSourceErrForkedHandle}
		if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrSource {
			t.Fatalf("late source failure = %#v", problem)
		}
		if !reflect.DeepEqual(before, plan.cow.pool.slots) || plan.cow.pageCount != 20 {
			t.Fatal("late source failure changed live state")
		}
	})
}

type lateBitmapLiveSnapshot struct {
	status               privatePagePoolStatus
	slots                []reservedBitmapPage
	bindings             []bitmapCOWArenaBinding
	replacements         []uint32
	indexNodes           []bitmapCOWIndexNode
	available            []int
	root                 uint32
	pageCount            uint64
	candidateLen         int
	replacementLen       int
	availableLen         int
	mutationEpoch        uint64
	checkpointIndexHead  int
	checkpointIndexCount int
}

func snapshotLateBitmapLive(t *testing.T, plan *freeBitmapReservationAttachment) lateBitmapLiveSnapshot {
	t.Helper()
	status, problem := plan.cow.pool.status()
	if problem.failed() {
		t.Fatal(problem)
	}
	return lateBitmapLiveSnapshot{
		status: status, slots: append([]reservedBitmapPage(nil), plan.cow.pool.slots...),
		bindings:     append([]bitmapCOWArenaBinding(nil), plan.cow.arenaBindings...),
		replacements: append([]uint32(nil), plan.cow.replacements...),
		indexNodes:   append([]bitmapCOWIndexNode(nil), plan.cow.indexNodes...),
		available:    append([]int(nil), plan.cow.availableSlots...),
		root:         plan.cow.root, pageCount: plan.cow.pageCount, candidateLen: plan.cow.candidateLen,
		replacementLen: plan.cow.replacementLen, availableLen: plan.cow.availableLen,
		mutationEpoch:        plan.cow.mutationEpoch,
		checkpointIndexHead:  plan.cow.pool.checkpointIndexHead,
		checkpointIndexCount: plan.cow.pool.checkpointIndexCount,
	}
}

func requireLateBitmapLiveSnapshot(t *testing.T, plan *freeBitmapReservationAttachment, want lateBitmapLiveSnapshot) {
	t.Helper()
	got := snapshotLateBitmapLive(t, plan)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("failed bind did not preserve exact live pool/COW/index state")
	}
}

func TestFreeBitmapLateBindingRejectsStagedRealAliasesAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*freeBitmapReservationAttachment, []uint32)
	}{
		{name: "cow", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) { plan.buffers.stage.cow = &plan.cow }},
		{name: "arena", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.arena = plan.cow.pool.slots
		}},
		{name: "arena-bindings", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.arenaBindings = plan.cow.arenaBindings
		}},
		{name: "replacements", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.replacements = plan.cow.replacements
		}},
		{name: "index", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.indexNodes = plan.cow.indexNodes
		}},
		{name: "available", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.availableSlots = plan.cow.availableSlots
		}},
		{name: "candidate", mutate: func(plan *freeBitmapReservationAttachment, _ []uint32) {
			plan.buffers.stage.poolValidation = plan.buffers.candidates[:plan.privatePages]
		}},
		{name: "reclaimed", mutate: func(plan *freeBitmapReservationAttachment, pages []uint32) {
			plan.buffers.stage.poolValidation = pages
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
			pages := []uint32{3, 7, 8}
			proof := completeLateBitmapProof(t, &plan, 50, pages)
			test.mutate(&plan, pages)
			before := snapshotLateBitmapLive(t, &plan)
			if _, problem := plan.bind(&proof); !problem.failed() {
				t.Fatal("staged/live alias was accepted")
			}
			requireLateBitmapLiveSnapshot(t, &plan, before)
			plan.buffers.stage = storage.buffers().stage
			if _, problem := plan.bind(&proof); problem.failed() {
				t.Fatalf("pre-consumption alias rejection prevented retry: %#v", problem)
			}
		})
	}
}

func TestFreeBitmapLateBindingRejectsLateEpochScopeAndScratchDriftAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*freeBitmapReservationAttachment)
	}{
		{name: "pool-epoch", mutate: func(plan *freeBitmapReservationAttachment) { plan.cow.pool.mutationEpoch++ }},
		{name: "pending-tail", mutate: func(plan *freeBitmapReservationAttachment) { plan.cow.pool.pendingPageCount++ }},
		{name: "scope", mutate: func(plan *freeBitmapReservationAttachment) { plan.scope.id++ }},
		{name: "scope-capacity", mutate: func(plan *freeBitmapReservationAttachment) { plan.cow.pool.slots[plan.scope.anchor].scopeCapacity++ }},
		{name: "slot", mutate: func(plan *freeBitmapReservationAttachment) { plan.cow.pool.slots[0].epoch++ }},
		{name: "cow-index", mutate: func(plan *freeBitmapReservationAttachment) { plan.cow.indexNodes[plan.cow.indexRoot].height++ }},
		{name: "source-avl", mutate: func(plan *freeBitmapReservationAttachment) { plan.buffers.sourceNodes[0].height++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
			proof := completeLateBitmapProof(t, &plan, 60, []uint32{3, 7})
			test.mutate(&plan)
			before := snapshotLateBitmapLive(t, &plan)
			if _, problem := plan.bind(&proof); !problem.failed() {
				t.Fatal("late drift was accepted")
			}
			requireLateBitmapLiveSnapshot(t, &plan, before)
		})
	}
}

func TestFreeBitmapLateBindingSelectionIdentityCoversWholeOptionalResult(t *testing.T) {
	storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
	plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
	if _, problem := completeFreeBitmapReclamation(plan.reclamationRequest, 1, nil); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("nonzero empty selection identity = %#v", problem)
	}
	if _, problem := completeFreeBitmapReclamation(plan.reclamationRequest, 0, []uint32{3, 7}); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("zero nonempty selection identity = %#v", problem)
	}
	proof := completeLateBitmapProof(t, &plan, 80, []uint32{3, 7})
	proof.lastPage = 9
	before := snapshotLateBitmapLive(t, &plan)
	if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("selection boundary drift = %#v", problem)
	}
	requireLateBitmapLiveSnapshot(t, &plan, before)
}

type lateFailingAccessSource struct {
	base     cowSparsePages
	checks   int
	failAt   int
	hook     func(int)
	readHook func()
}

func (s *lateFailingAccessSource) checkAccessStatus() pageSourceStatus {
	s.checks++
	if s.hook != nil {
		s.hook(s.checks)
	}
	if s.checks == s.failAt {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	return pageSourceStatus{}
}

func (s *lateFailingAccessSource) readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	if s.readHook != nil {
		s.readHook()
	}
	return s.base.readPageStatus(pageNumber, destination)
}

func TestFreeBitmapLateBindingFinalSourceCheckPrecedesLiveMutation(t *testing.T) {
	source := &lateFailingAccessSource{
		base:   cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		failAt: 4,
	}
	storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
	plan := newLateBitmapPlan(t, source, 2, 2, &storage)
	proof := completeLateBitmapProof(t, &plan, 90, []uint32{3, 7})
	before := snapshotLateBitmapLive(t, &plan)
	if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrSource || source.checks != 4 {
		t.Fatalf("final source failure = %#v checks %d", problem, source.checks)
	}
	requireLateBitmapLiveSnapshot(t, &plan, before)
}

func TestFreeBitmapLateBindingRejectsPostCallbackAttachmentIdentityDrift(t *testing.T) {
	otherSource := &cowSparsePages{}
	otherPool := &privatePagePool{}
	tests := []struct {
		name   string
		mutate func(*freeBitmapReservationAttachment)
	}{
		{name: "negative-private-pages", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.privatePages = -1
		}},
		{name: "nil-cow-pool", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.cow.pool = nil
		}},
		{name: "substituted-pool", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.cow.pool = otherPool
		}},
		{name: "substituted-committed-source", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.committed = otherSource
			plan.cow.committed = otherSource
		}},
		{name: "nil-committed-source", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.committed = nil
			plan.cow.committed = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &lateFailingAccessSource{
				base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
			}
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			capacity := newLateBitmapCapacityPlanAt(t, source, 20, 2, 2, &storage)
			if poolProblem := initVacantPrivatePagePool(
				&storage.pool, storage.arena, 20, 20, 2,
			); poolProblem.failed() {
				t.Fatal(poolProblem)
			}
			scope, poolProblem := storage.pool.reserveScope(capacity.privatePages)
			if poolProblem.failed() {
				t.Fatal(poolProblem)
			}
			attachment, problem := capacity.attach(&storage.pool, scope)
			if problem.failed() {
				t.Fatal(problem)
			}
			proof := completeLateBitmapProof(t, &attachment, 109, []uint32{3, 7})
			before := snapshotLateBitmapLive(t, &attachment)
			original := attachment
			source.hook = func(check int) {
				if check == 4 {
					test.mutate(&attachment)
				}
			}

			if _, problem = attachment.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("post-callback drift = %#v", problem)
			}
			if source.checks != 4 || proof.ticket.state.Load() != 3 {
				t.Fatalf("callback/proof terminal = checks %d state %d", source.checks, proof.ticket.state.Load())
			}
			attachment = original
			requireLateBitmapLiveSnapshot(t, &attachment, before)
			if _, replayProblem := attachment.bind(&proof); replayProblem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("consumed proof replay = %#v", replayProblem)
			}
			if source.checks != 4 {
				t.Fatalf("proof replay invoked source callback: %d", source.checks)
			}

			source.hook = nil
			fresh, attachProblem := capacity.attach(&storage.pool, scope)
			if attachProblem.failed() {
				t.Fatalf("fresh attachment after stale callback = %#v", attachProblem)
			}
			freshProof := completeLateBitmapProof(t, &fresh, 110, []uint32{3, 7})
			if _, bindProblem := fresh.bind(&freshProof); bindProblem.failed() {
				t.Fatalf("fresh retry after stale callback = %#v", bindProblem)
			}
		})
	}
}

func TestFreeBitmapLateBindingSealsNonComparableImmutableSource(t *testing.T) {
	data := make([]byte, 20*PageSize)
	leaf := cowLeaf(t, 2, 1, 5, 9)
	copy(data[2*PageSize:3*PageSize], leaf.bytes[:])
	source := newImmutableSlicePageSource(data, 20)
	storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
	attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
	proof := completeLateBitmapProof(t, &attachment, 113, []uint32{3, 7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatalf("immutable source bind = %#v", problem)
	}
}

func TestFreeBitmapLateBindingRejectsPostCallbackForeignMutationDespiteCacheConcealment(t *testing.T) {
	source := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
	capacity := newLateBitmapCapacityPlanAt(t, source, 20, 2, 2, &storage)
	sharedArena := make([]reservedBitmapPage, capacity.privatePages+2)
	var sharedPool privatePagePool
	if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	foreign, poolProblem := sharedPool.reserveScope(2)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bindForeignLateBitmapPage(t, &sharedPool, foreign, 3, privatePageReclaimed, false)
	bitmapScope, poolProblem := sharedPool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(&sharedPool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 111, []uint32{7})
	var afterCallback lateBitmapLiveSnapshot
	callbackRan := false
	source.hook = func(check int) {
		if check != 4 {
			return
		}
		bindForeignLateBitmapPage(t, &sharedPool, foreign, 4, privatePageReclaimed, false)
		status, statusProblem := sharedPool.status()
		if statusProblem.failed() {
			t.Fatal(statusProblem)
		}
		attachment.poolGeneration = status.generation
		attachment.poolMutationEpoch = status.mutationEpoch
		attachment.reclamationRequest.poolGeneration = status.generation
		attachment.reclamationRequest.poolMutationEpoch = status.mutationEpoch
		proof.ticket.poolGeneration = status.generation
		proof.ticket.poolMutationEpoch = status.mutationEpoch
		afterCallback = snapshotLateBitmapLive(t, &attachment)
		callbackRan = true
	}
	if _, problem = attachment.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("concealed foreign mutation = %#v", problem)
	}
	if !callbackRan || source.checks != 4 || proof.ticket.state.Load() != 3 {
		t.Fatalf("callback/proof terminal = ran %t checks %d state %d", callbackRan, source.checks, proof.ticket.state.Load())
	}
	requireLateBitmapLiveSnapshot(t, &attachment, afterCallback)
	anchor, scopeProblem := sharedPool.validateScope(bitmapScope)
	if scopeProblem.failed() || anchor.scopeBound != 0 {
		t.Fatal("stale rejection changed the bitmap scope")
	}
	if _, replayProblem := attachment.bind(&proof); replayProblem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("consumed proof replay = %#v", replayProblem)
	}
	if source.checks != 4 {
		t.Fatalf("proof replay invoked source callback: %d", source.checks)
	}

	source.hook = nil
	fresh, attachProblem := capacity.attach(&sharedPool, bitmapScope)
	if attachProblem.failed() {
		t.Fatalf("fresh attachment after foreign mutation = %#v", attachProblem)
	}
	freshProof := completeLateBitmapProof(t, &fresh, 112, []uint32{7})
	if _, bindProblem := fresh.bind(&freshProof); bindProblem.failed() {
		t.Fatalf("fresh retry after foreign mutation = %#v", bindProblem)
	}
}

func TestFreeBitmapLateBindingPostCallbackPoolFenceIsAtomic(t *testing.T) {
	source := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
	capacity := newLateBitmapCapacityPlanAt(t, source, 20, 2, 2, &storage)
	sharedArena := make([]reservedBitmapPage, capacity.privatePages+2)
	var sharedPool privatePagePool
	if problem := initVacantPrivatePagePool(&sharedPool, sharedArena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	foreign, poolProblem := sharedPool.reserveScope(2)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bindForeignLateBitmapPage(t, &sharedPool, foreign, 3, privatePageReclaimed, false)
	bitmapScope, poolProblem := sharedPool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(&sharedPool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 90, []uint32{7})
	var afterCallback lateBitmapLiveSnapshot
	callbackRan := false
	source.hook = func(check int) {
		if check != 4 {
			return
		}
		bindForeignLateBitmapPage(t, &sharedPool, foreign, 4, privatePageReclaimed, false)
		afterCallback = snapshotLateBitmapLive(t, &attachment)
		callbackRan = true
	}
	if _, problem = attachment.bind(&proof); problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("post-callback pool drift = %#v", problem)
	}
	if !callbackRan || source.checks != 4 {
		t.Fatalf("final callback count = %d, ran %t", source.checks, callbackRan)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, afterCallback)
	anchor, poolProblem := sharedPool.validateScope(bitmapScope)
	if poolProblem.failed() || anchor.scopeBound != 0 {
		t.Fatal("stale rejection bound pages in the bitmap scope")
	}
}

func TestFreeBitmapLateBindingRebuildsStageAfterFinalCallback(t *testing.T) {
	source := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
	attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
	proof := completeLateBitmapProof(t, &attachment, 91, []uint32{3, 7})
	var poisonedPage [PageSize]byte
	for index := range poisonedPage {
		poisonedPage[index] = 0xa5
	}
	latePhaseCallback := false
	source.readHook = func() {
		latePhaseCallback = true
	}
	source.hook = func(check int) {
		if storage.stagePool.inUseCount() != 0 || attachment.cow.pool.activeCheckpointID != 0 {
			latePhaseCallback = true
		}
		if check != 4 {
			return
		}
		for index := range storage.stageArena {
			storage.stageArena[index].bytes = poisonedPage
		}
		for index := range storage.stageValidation {
			storage.stageValidation[index] = ^uint32(0)
		}
		for index := range storage.stageReplacements {
			storage.stageReplacements[index] = ^uint32(0)
		}
		for index := range storage.stageIndex {
			storage.stageIndex[index] = bitmapCOWIndexNode{pageNumber: ^uint32(0), height: 99}
		}
		for index := range storage.stageAvailable {
			storage.stageAvailable[index] = -2
		}
		for index := range storage.stageBindings {
			storage.stageBindings[index] = bitmapCOWArenaBinding{poolSlot: -2, activeNode: -2, bound: true}
		}
		storage.stageCOW.root = ^uint32(0)
		storage.stagePool.mutationEpoch = ^uint64(0)
	}
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatalf("stage poison bind = %#v", problem)
	}
	if source.checks != 4 || latePhaseCallback {
		t.Fatalf("callbacks = %d, late phase %t", source.checks, latePhaseCallback)
	}
	if problem := attachment.cow.validateScopedBindings(); problem.failed() {
		t.Fatalf("rebuilt scoped state = %#v", problem)
	}
	for index, value := range attachment.cow.replacements[attachment.cow.replacementLen:] {
		if value != 0 {
			t.Fatalf("unused live replacement %d retained poison", index)
		}
	}
	inUse := 0
	for index := range attachment.cow.pool.slots {
		slot := &attachment.cow.pool.slots[index]
		if slot.scopeID != attachment.scope.id {
			continue
		}
		if slot.state == privatePageInUse {
			inUse++
		}
		if slot.bytes == poisonedPage {
			t.Fatalf("live scope slot %d retained poisoned stage bytes", index)
		}
		if slot.state != privatePageInUse && slot.bytes != ([PageSize]byte{}) {
			t.Fatalf("unused live scope slot %d retained nonzero bytes", index)
		}
	}
	if inUse == 0 {
		t.Fatal("stage-rebuild fixture did not produce an in-use shadow page")
	}
}

func TestFreeBitmapLateBindingCapacityAndHeadroomFailuresAreAtomic(t *testing.T) {
	t.Run("stage-index", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
		plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 91, []uint32{3, 7})
		plan.buffers.stage.indexNodes = plan.buffers.stage.indexNodes[:len(plan.cow.indexNodes)-1]
		before := snapshotLateBitmapLive(t, &plan)
		if _, problem := plan.bind(&proof); !problem.failed() {
			t.Fatal("short stage index was accepted")
		}
		requireLateBitmapLiveSnapshot(t, &plan, before)
	})

	t.Run("source-nodes", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(8, 8, 8, 2)
		plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 92, []uint32{3})
		before := snapshotLateBitmapLive(t, &plan)
		if _, problem := plan.bind(&proof); problem.resource != freeBitmapResourceSourceNodes {
			t.Fatalf("short source scratch = %#v", problem)
		}
		requireLateBitmapLiveSnapshot(t, &plan, before)
	})

	t.Run("mutation-headroom", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
		capacity := newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, 20, 0, 2, &storage)
		if poolProblem := initVacantPrivatePagePool(
			&storage.pool, storage.arena, 20, 20, 2,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		scope, poolProblem := storage.pool.reserveScope(capacity.privatePages)
		if poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		storage.pool.mutationEpoch = ^uint64(0) - 1
		plan, attachProblem := capacity.attach(&storage.pool, scope)
		if attachProblem.failed() {
			t.Fatal(attachProblem)
		}
		proof := completeLateBitmapProof(t, &plan, 93, []uint32{3, 7})
		before := snapshotLateBitmapLive(t, &plan)
		if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("mutation headroom = %#v", problem)
		}
		requireLateBitmapLiveSnapshot(t, &plan, before)
	})

	t.Run("slot-headroom", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
		plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 94, []uint32{3, 7})
		plan.cow.pool.slots[0].epoch = ^uint64(0) - 1
		plan.cow.arenaBindings[0].poolEpoch = plan.cow.pool.slots[0].epoch
		plan.cowScratchFingerprint = freeBitmapReservationCOWFingerprint(&plan.cow)
		before := snapshotLateBitmapLive(t, &plan)
		if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("slot headroom = %#v", problem)
		}
		requireLateBitmapLiveSnapshot(t, &plan, before)
	})

	t.Run("stale-index-checkpoint-chain", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
		plan := newLateBitmapPlan(t, &cowSparsePages{}, 0, 2, &storage)
		proof := completeLateBitmapProof(t, &plan, 95, []uint32{3, 7})
		plan.cow.pool.checkpointIndexHead = 0
		plan.cow.pool.checkpointIndexCount = 1
		before := snapshotLateBitmapLive(t, &plan)
		if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrArenaPageConflict {
			t.Fatalf("stale checkpoint chain = %#v", problem)
		}
		requireLateBitmapLiveSnapshot(t, &plan, before)
	})
}

func TestFreeBitmapAttachmentNonceExhaustionIsPermanentAndAtomic(t *testing.T) {
	storage := newLateBitmapPlannerStorage(3, 2, 1, 4)
	capacity := newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, 20, 0, 3, &storage)
	if problem := initVacantPrivatePagePool(&storage.pool, storage.arena, 20, 20, 2); problem.failed() {
		t.Fatal(problem)
	}
	scope, poolProblem := storage.pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}

	beforePool := storage.pool
	beforePool.slots = nil
	beforeSlots := append([]reservedBitmapPage(nil), storage.pool.slots...)
	oldNonce := freeBitmapReclamationNonce.Load()
	freeBitmapReclamationNonce.Store(^uint64(0))
	defer freeBitmapReclamationNonce.Store(oldNonce)

	for attempt := 0; attempt < 2; attempt++ {
		if _, problem := capacity.attach(&storage.pool, scope); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("exhausted nonce attempt %d = %#v", attempt, problem)
		}
		afterPool := storage.pool
		afterPool.slots = nil
		if !reflect.DeepEqual(beforePool, afterPool) || !reflect.DeepEqual(beforeSlots, storage.pool.slots) {
			t.Fatalf("exhausted nonce attempt %d changed the shared pool", attempt)
		}
		if storage.reclamation.state.Load() != 0 || storage.reclamation.nonce != 0 ||
			storage.reclamation.selectedTxn != 0 || storage.reclamation.committedPageCount != 0 ||
			storage.reclamation.pendingPageCount != 0 || storage.reclamation.root != 0 ||
			storage.reclamation.poolEpoch != 0 || storage.reclamation.poolGeneration != 0 ||
			storage.reclamation.poolMutationEpoch != 0 || storage.reclamation.scopeID != 0 ||
			storage.reclamation.scopeAnchor != 0 || storage.reclamation.candidateFingerprint != 0 ||
			storage.reclamation.selectionID != 0 || storage.reclamation.pages != nil ||
			storage.reclamation.pageCount != 0 || storage.reclamation.firstPage != 0 ||
			storage.reclamation.lastPage != 0 || storage.reclamation.fingerprint != 0 {
			t.Fatalf("exhausted nonce attempt %d changed the reclamation ticket", attempt)
		}
		if freeBitmapReclamationNonce.Load() != ^uint64(0) {
			t.Fatalf("exhausted nonce attempt %d wrapped the global counter", attempt)
		}
	}
}

func TestFreeBitmapCapacityPlanRejectsPageSpaceAndBudgetBeforePoolInitialization(t *testing.T) {
	t.Run("page-space", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(1, 1, 1, 2)
		planner, problem := newFreeBitmapReservationPlanner(&cowSparsePages{}, 1, MaxPageCount, 0, 1, storage.buffers())
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, problem = planner.planCapacity(); problem.code != freeBitmapCOWErrPageSpaceExhausted {
			t.Fatalf("page space = %#v", problem)
		}
		if storage.pool.self != nil || storage.arena[0] != (reservedBitmapPage{}) {
			t.Fatal("page-space failure initialized the live pool")
		}
	})

	t.Run("arena-minus-one", func(t *testing.T) {
		storage := newLateBitmapPlannerStorage(2, 2, 1, 4)
		planner, problem := newFreeBitmapReservationPlanner(
			&cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 1, 20, 2, 2, storage.buffers(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, problem = planner.planCapacity(); problem.code != freeBitmapCOWErrInsufficientResourceBudget ||
			problem.resource != freeBitmapResourceArenaPages || problem.required != 3 || problem.actual != 2 {
			t.Fatalf("arena minus one = %#v", problem)
		}
		if storage.pool.self != nil {
			t.Fatal("budget failure initialized the live pool")
		}
	})

	t.Run("every-caller-resource-minus-one", func(t *testing.T) {
		tests := []struct {
			name     string
			resource freeBitmapReservationResource
			mutate   func(*lateBitmapPlannerStorage)
		}{
			{name: "pool-validation", resource: freeBitmapResourceArenaPages, mutate: func(s *lateBitmapPlannerStorage) { s.poolValidation = s.poolValidation[:2] }},
			{name: "arena-bindings", resource: freeBitmapResourceArenaBindings, mutate: func(s *lateBitmapPlannerStorage) { s.arenaBindings = s.arenaBindings[:2] }},
			{name: "candidates", resource: freeBitmapResourceCandidatePages, mutate: func(s *lateBitmapPlannerStorage) { s.candidates = s.candidates[:1] }},
			{name: "verified", resource: freeBitmapResourceVerifiedPages, mutate: func(s *lateBitmapPlannerStorage) { s.verified = s.verified[:0] }},
			{name: "replacements", resource: freeBitmapResourceReplacementPages, mutate: func(s *lateBitmapPlannerStorage) { s.replacements = s.replacements[:0] }},
			{name: "index", resource: freeBitmapResourceIndexNodes, mutate: func(s *lateBitmapPlannerStorage) { s.indexNodes = s.indexNodes[:6] }},
			{name: "available", resource: freeBitmapResourceAvailableSlots, mutate: func(s *lateBitmapPlannerStorage) { s.availableSlots = s.availableSlots[:2] }},
			{name: "sources", resource: freeBitmapResourceSourceNodes, mutate: func(s *lateBitmapPlannerStorage) { s.sourceNodes = s.sourceNodes[:1] }},
			{name: "ticket", resource: freeBitmapResourceReclamationTicket, mutate: func(s *lateBitmapPlannerStorage) { s.reclamation = freeBitmapReclamationTicket{} }},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				storage := newLateBitmapPlannerStorage(3, 2, 1, 2)
				test.mutate(&storage)
				buffers := storage.buffers()
				if test.name == "ticket" {
					buffers.reclamation = nil
				}
				planner, problem := newFreeBitmapReservationPlanner(
					&cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 1, 20, 2, 2, buffers,
				)
				if !problem.failed() {
					_, problem = planner.planCapacity()
				}
				if problem.code != freeBitmapCOWErrInsufficientResourceBudget || problem.resource != test.resource {
					t.Fatalf("minus one = %#v, want resource %d", problem, test.resource)
				}
				if storage.pool.self != nil {
					t.Fatal("resource failure initialized the live pool")
				}
			})
		}
	})
}

func TestFreeBitmapLateBindingRejectsEveryStageResourceMinusOneAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*freeBitmapReservationAttachment)
	}{
		{name: "cow", mutate: func(plan *freeBitmapReservationAttachment) { plan.buffers.stage.cow = nil }},
		{name: "pool", mutate: func(plan *freeBitmapReservationAttachment) { plan.buffers.stage.pool = nil }},
		{name: "selection", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.poolValidation = plan.buffers.stage.poolValidation[:plan.privatePages-1]
		}},
		{name: "arena", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.arena = plan.buffers.stage.arena[:plan.privatePages-1]
		}},
		{name: "bindings", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.arenaBindings = plan.buffers.stage.arenaBindings[:plan.privatePages-1]
		}},
		{name: "replacements", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.replacements = plan.buffers.stage.replacements[:0]
		}},
		{name: "index", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.indexNodes = plan.buffers.stage.indexNodes[:len(plan.cow.indexNodes)-1]
		}},
		{name: "available", mutate: func(plan *freeBitmapReservationAttachment) {
			plan.buffers.stage.availableSlots = plan.buffers.stage.availableSlots[:plan.privatePages-1]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := newLateBitmapPlannerStorage(8, 8, 8, 20)
			plan := newLateBitmapPlan(t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}, 2, 2, &storage)
			proof := completeLateBitmapProof(t, &plan, 95, []uint32{3, 7})
			test.mutate(&plan)
			before := snapshotLateBitmapLive(t, &plan)
			if _, problem := plan.bind(&proof); problem.code != freeBitmapCOWErrInsufficientResourceBudget {
				t.Fatalf("stage minus one = %#v", problem)
			}
			requireLateBitmapLiveSnapshot(t, &plan, before)
		})
	}
}

func TestFreeBitmapReservationMergedSourceSelectionScalesDeterministically(t *testing.T) {
	for _, count := range []int{512, 4096} {
		committedCount := count
		reclaimedCount := count
		nodes := make([]freeBitmapReservationSourceNode, committedCount+reclaimedCount)
		candidates := make([]uint32, committedCount)
		reclaimed := make([]uint32, reclaimedCount)
		root := bitmapCOWNoIndex
		for index := 0; index < committedCount; index++ {
			candidates[index] = uint32(4 + index*2)
			root = freeBitmapSourceInsert(nodes, root, index, candidates[index], freeBitmapReservationSourceCommitted, count)
			reclaimed[index] = uint32(3 + index*2)
		}
		selection := make([]uint32, count)
		plan := freeBitmapReservationAttachment{
			freeBitmapReservationCapacityPlan: freeBitmapReservationCapacityPlan{
				committedSourceRoot: root, committedSourceLen: committedCount,
				privatePages: count, payloadPages: count,
				buffers: freeBitmapReservationBuffers{
					sourceNodes: nodes,
					stage:       freeBitmapReservationStageBuffers{poolValidation: selection},
				},
			},
			cow: freeBitmapCOW{committedPageCount: uint64(count*2 + 10), candidates: candidates, plannedCandidateLen: committedCount},
		}
		reclaimedRoot, problem := plan.buildReclaimedSource(reclaimed)
		if problem.failed() {
			t.Fatal(problem)
		}
		selected, selectedCommitted, problem := plan.selectPhysicalPages(reclaimed, reclaimedRoot)
		if problem.failed() || selected != count || selectedCommitted != count/2 {
			t.Fatalf("merge %d = selected %d committed %d problem %#v", count, selected, selectedCommitted, problem)
		}
		for index, page := range selection {
			if page != uint32(index+3) {
				t.Fatalf("merge %d source %d = %d", count, index, page)
			}
		}
	}
}

func TestFreeBitmapReservationSourceAVLIsDeterministicAndLogarithmic(t *testing.T) {
	for _, count := range []int{512, 4096} {
		first := make([]freeBitmapReservationSourceNode, count)
		second := make([]freeBitmapReservationSourceNode, count)
		firstRoot, secondRoot := bitmapCOWNoIndex, bitmapCOWNoIndex
		for index := 0; index < count; index++ {
			page := uint32(index + 2)
			firstRoot = freeBitmapSourceInsert(first, firstRoot, index, page, freeBitmapReservationSourceReclaimed, 0)
			secondRoot = freeBitmapSourceInsert(second, secondRoot, index, page, freeBitmapReservationSourceReclaimed, 0)
		}
		if !reflect.DeepEqual(first, second) || firstRoot != secondRoot ||
			first[firstRoot].subtreeCount != uint32(count) || int(first[firstRoot].height) > bits.Len(uint(count))+1 {
			t.Fatalf("AVL %d is non-deterministic or unbounded: roots %d/%d height %d", count, firstRoot, secondRoot, first[firstRoot].height)
		}
		for rank := 0; rank < count; rank++ {
			node, found := freeBitmapSourceAt(first, firstRoot, rank)
			if !found || node.pageNumber != uint32(rank+2) {
				t.Fatalf("AVL %d rank %d = %#v/%t", count, rank, node, found)
			}
		}
	}
}

func TestFreeBitmapScopedAttachEnumeratesOnlyTargetScope(t *testing.T) {
	for _, foreignCount := range []int{512, 4096} {
		t.Run(stringInt(foreignCount), func(t *testing.T) {
			const targetCapacity = 2
			pageCount := uint64(foreignCount*2 + 100)
			storage := newLateBitmapPlannerStorage(targetCapacity, 1, 1, 2)
			capacity := newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, pageCount, 0, targetCapacity, &storage)
			if capacity.privatePages != targetCapacity {
				t.Fatalf("target private pages = %d, want %d", capacity.privatePages, targetCapacity)
			}

			var pool privatePagePool
			arena := make([]reservedBitmapPage, foreignCount+targetCapacity)
			if problem := initVacantPrivatePagePool(&pool, arena, pageCount, pageCount, 2); problem.failed() {
				t.Fatal(problem)
			}
			prefix, problem := pool.reserveScope(foreignCount / 2)
			if problem.failed() {
				t.Fatal(problem)
			}
			target, problem := pool.reserveScope(targetCapacity)
			if problem.failed() {
				t.Fatal(problem)
			}
			suffix, problem := pool.reserveScope(foreignCount - foreignCount/2)
			if problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, problem := pool.begin()
			if problem.failed() {
				t.Fatal(problem)
			}
			for index := foreignCount/2 - 1; index >= 0; index-- {
				if _, problem = pool.bindPage(checkpoint, prefix, uint32(4+index*2), privatePageReclaimed); problem.failed() {
					t.Fatal(problem)
				}
			}
			for index := foreignCount - foreignCount/2 - 1; index >= 0; index-- {
				if _, problem = pool.bindPage(checkpoint, suffix, uint32(3+index*2), privatePageReclaimed); problem.failed() {
					t.Fatal(problem)
				}
			}
			if problem = pool.commit(checkpoint); problem.failed() {
				t.Fatal(problem)
			}
			prefixBefore := freeBitmapReservationScopeFingerprint(&pool, prefix)
			suffixBefore := freeBitmapReservationScopeFingerprint(&pool, suffix)
			attachment, attachProblem := capacity.attach(&pool, target)
			if attachProblem.failed() {
				t.Fatal(attachProblem)
			}
			if attachment.cow.scopedFullValidations != 1 ||
				attachment.cow.scopedMemberVisits != 2*targetCapacity {
				t.Fatalf("target attach work = validations %d visits %d, want 1/%d",
					attachment.cow.scopedFullValidations, attachment.cow.scopedMemberVisits, 2*targetCapacity)
			}
			member := pool.slots[target.anchor].scopeMemberHead
			for index := 0; index < targetCapacity; index++ {
				if member == privatePagePoolNoIndex || attachment.cow.arenaBindings[index].poolSlot != member {
					t.Fatalf("canonical target member %d = %d/binding %d", index, member, attachment.cow.arenaBindings[index].poolSlot)
				}
				member = pool.slots[member].scopeMemberNext
			}
			if member != privatePagePoolNoIndex ||
				freeBitmapReservationScopeFingerprint(&pool, prefix) != prefixBefore ||
				freeBitmapReservationScopeFingerprint(&pool, suffix) != suffixBefore {
				t.Fatal("target attach traversed or changed foreign scope state")
			}

			checkpoint, problem = pool.begin()
			if problem.failed() {
				t.Fatal(problem)
			}
			reversePages := [targetCapacity]uint32{uint32(pageCount - 1), uint32(pageCount - 2)}
			for _, pageNumber := range reversePages {
				if _, problem = pool.bindPage(checkpoint, target, pageNumber, privatePageReclaimed); problem.failed() {
					t.Fatal(problem)
				}
			}
			if problem = pool.commit(checkpoint); problem.failed() {
				t.Fatal(problem)
			}
			visitsBefore := attachment.cow.scopedMemberVisits
			if syncProblem := attachment.cow.synchronizeScopedBindingsForCandidatePrefix(target, 0); syncProblem.failed() {
				t.Fatal(syncProblem)
			}
			if attachment.cow.scopedMemberVisits-visitsBefore != targetCapacity {
				t.Fatalf("reverse bind sync visited %d members, want %d",
					attachment.cow.scopedMemberVisits-visitsBefore, targetCapacity)
			}
			for index, pageNumber := range reversePages {
				if attachment.cow.arenaBindings[index].pageNumber != pageNumber {
					t.Fatalf("reverse bind %d = %d, want %d", index, attachment.cow.arenaBindings[index].pageNumber, pageNumber)
				}
			}
		})
	}
}

func TestFreeBitmapLateBindTerminalCommitIsScopeBounded(t *testing.T) {
	for _, foreignCount := range []int{512, 4096} {
		t.Run(stringInt(foreignCount), func(t *testing.T) {
			const targetCapacity = 2
			pageCount := uint64(foreignCount*2 + 100)
			storage := newLateBitmapPlannerStorage(targetCapacity, 1, 1, 2)
			capacity := newLateBitmapCapacityPlanAt(t, &cowSparsePages{}, pageCount, 0, targetCapacity, &storage)
			if capacity.privatePages != targetCapacity {
				t.Fatalf("target private pages = %d, want %d", capacity.privatePages, targetCapacity)
			}

			var pool privatePagePool
			arena := make([]reservedBitmapPage, foreignCount+targetCapacity)
			if problem := initVacantPrivatePagePool(&pool, arena, pageCount, pageCount, 2); problem.failed() {
				t.Fatal(problem)
			}
			prefixCount := foreignCount / 2
			prefix, problem := pool.reserveScope(prefixCount)
			if problem.failed() {
				t.Fatal(problem)
			}
			target, problem := pool.reserveScope(targetCapacity)
			if problem.failed() {
				t.Fatal(problem)
			}
			suffix, problem := pool.reserveScope(foreignCount - prefixCount)
			if problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, problem := pool.begin()
			if problem.failed() {
				t.Fatal(problem)
			}
			for index := 0; index < prefixCount; index++ {
				if _, problem = pool.bindPage(checkpoint, prefix, uint32(2+index*2), privatePageReclaimed); problem.failed() {
					t.Fatal(problem)
				}
			}
			for index := 0; index < foreignCount-prefixCount; index++ {
				if _, problem = pool.bindPage(checkpoint, suffix, uint32(3+index*2), privatePageReclaimed); problem.failed() {
					t.Fatal(problem)
				}
			}
			if problem = pool.commit(checkpoint); problem.failed() {
				t.Fatal(problem)
			}
			prefixBefore := normalizedForeignLateBitmapScopeFingerprint(&pool, prefix)
			suffixBefore := normalizedForeignLateBitmapScopeFingerprint(&pool, suffix)
			generationBefore := pool.generation

			attachment, attachProblem := capacity.attach(&pool, target)
			if attachProblem.failed() {
				t.Fatal(attachProblem)
			}
			proof := completeLateBitmapProof(
				t, &attachment, uint64(200+foreignCount), []uint32{uint32(pageCount - 2), uint32(pageCount - 1)},
			)
			if _, bindProblem := attachment.bind(&proof); bindProblem.failed() {
				t.Fatal(bindProblem)
			}

			work := attachment.terminalWork
			maximumIndexVisits := 8 * (bits.Len(uint(foreignCount+targetCapacity)) + targetCapacity)
			if work.scopeSlotVisits != targetCapacity || work.scopeHeaderVisits != 1 ||
				work.indexSlotVisits <= 0 || work.indexSlotVisits > maximumIndexVisits ||
				work.scopeSlotVisits+work.indexSlotVisits+work.scopeHeaderVisits >= foreignCount {
				t.Fatalf("terminal work amid %d foreign slots = %+v, index limit %d",
					foreignCount, work, maximumIndexVisits)
			}
			if pool.activeCheckpointID != 0 || pool.checkpointCleanup != 0 ||
				pool.checkpointIndexHead != privatePagePoolNoIndex || pool.checkpointIndexCount != 0 ||
				pool.generation != generationBefore+1 {
				t.Fatalf("terminal checkpoint header = active %d cleanup %d index %d/%d generation %d, want 0/0/-1/0/%d",
					pool.activeCheckpointID, pool.checkpointCleanup, pool.checkpointIndexHead,
					pool.checkpointIndexCount, pool.generation, generationBefore+1)
			}
			for index := range pool.slots {
				slot := &pool.slots[index]
				if slot.checkpointID != 0 || slot.indexCheckpointID != 0 ||
					slot.indexCheckpointNext != privatePagePoolNoIndex || slot.scopeCheckpointID != 0 {
					t.Fatalf("slot %d retained checkpoint scratch", index)
				}
			}
			if normalizedForeignLateBitmapScopeFingerprint(&pool, prefix) != prefixBefore ||
				normalizedForeignLateBitmapScopeFingerprint(&pool, suffix) != suffixBefore {
				t.Fatal("scope-bounded terminal commit changed foreign semantic state")
			}
			global := verifyPrivatePageAVL(t, &pool, pool.indexRoot, false, 0)
			targetAVL := verifyPrivatePageAVL(t, &pool, pool.slots[target.anchor].scopeRoot, true, target.id)
			if global.count != foreignCount+targetCapacity || targetAVL.count != targetCapacity {
				t.Fatalf("terminal AVL counts = global %d target %d", global.count, targetAVL.count)
			}
		})
	}
}

func TestFreeBitmapAllCommittedBatchValidatesScopeOnce(t *testing.T) {
	for _, committedRank := range []int{512, 4096} {
		t.Run(stringInt(committedRank), func(t *testing.T) {
			bits := make([]uint32, committedRank)
			for index := range bits {
				bits[index] = uint32(index + 3)
			}
			pageCount := uint64(committedRank + 100)
			source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, bits...)}}
			storage := newLateBitmapPlannerStorage(committedRank+1, committedRank, 2, committedRank+2)
			capacity := newLateBitmapCapacityPlanAt(t, source, pageCount, 2, committedRank, &storage)
			if capacity.committedSourceLen != committedRank {
				t.Fatalf("committed source length = %d, want %d", capacity.committedSourceLen, committedRank)
			}
			arena := make([]reservedBitmapPage, capacity.privatePages)
			var pool privatePagePool
			if problem := initVacantPrivatePagePool(&pool, arena, pageCount, pageCount, 2); problem.failed() {
				t.Fatal(problem)
			}
			scope, poolProblem := pool.reserveScope(capacity.privatePages)
			if poolProblem.failed() {
				t.Fatal(poolProblem)
			}
			attachment, problem := capacity.attach(&pool, scope)
			if problem.failed() {
				t.Fatal(problem)
			}
			selected, selectedCommitted, problem := attachment.selectPhysicalPages(nil, bitmapCOWNoIndex)
			if problem.failed() || selectedCommitted != committedRank || selected != capacity.privatePages {
				t.Fatalf("selection = %d committed_rank=%d problem=%#v, want %d/%d",
					selected, selectedCommitted, problem, capacity.privatePages, committedRank)
			}
			shadow, problem := attachment.buildShadow(selected, selectedCommitted)
			if problem.failed() {
				t.Fatal(problem)
			}
			if shadow.candidateLen != committedRank || shadow.scopedFullValidations != 2 ||
				shadow.scopedMemberVisits != uint64(4*capacity.privatePages) {
				t.Fatalf("batch %d work = candidates %d validations %d visits %d, want %d/2/%d",
					committedRank, shadow.candidateLen, shadow.scopedFullValidations,
					shadow.scopedMemberVisits, committedRank, 4*capacity.privatePages)
			}
		})
	}
}

func TestFreeBitmapLateBindingAllocatesNothing(t *testing.T) {
	storage := newLateBitmapPlannerStorage(4, 1, 1, 8)
	reclaimed := []uint32{3, 7, 9}
	source := &cowSparsePages{}
	failed := false
	allocations := testing.AllocsPerRun(100, func() {
		planner, problem := newFreeBitmapReservationPlanner(source, 1, 20, 0, 3, storage.buffers())
		if problem.failed() {
			failed = true
			return
		}
		plan, problem := planner.planCapacity()
		if problem.failed() {
			failed = true
			return
		}
		if poolProblem := initVacantPrivatePagePool(&storage.pool, storage.arena, 20, 20, 2); poolProblem.failed() {
			failed = true
			return
		}
		scope, poolProblem := storage.pool.reserveScope(plan.privatePages)
		if poolProblem.failed() {
			failed = true
			return
		}
		attachment, problem := plan.attach(&storage.pool, scope)
		if problem.failed() {
			failed = true
			return
		}
		proof, problem := completeFreeBitmapReclamation(attachment.reclamationRequest, 70, reclaimed)
		if problem.failed() {
			failed = true
			return
		}
		if _, problem = attachment.bind(&proof); problem.failed() {
			failed = true
		}
	})
	if failed || allocations != 0 {
		t.Fatalf("late bind allocations = %f", allocations)
	}
}
