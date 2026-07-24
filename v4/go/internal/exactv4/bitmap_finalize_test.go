package exactv4

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func cleanupScratchForAttachment(attachment *freeBitmapReservationAttachment) freeBitmapCleanupScratch {
	nodes, path, ok := privatePageDeleteScratchRequirements(
		len(attachment.cow.pool.slots), attachment.privatePages, attachment.privatePages,
	)
	if !ok {
		panic("invalid cleanup scratch requirement")
	}
	return freeBitmapCleanupScratch{
		nodes:   make([]freeBitmapCleanupOverlayNode, nodes),
		path:    make([]int, path),
		targets: make([]int, attachment.privatePages),
	}
}

func finalizeLateBitmapAttachment(
	t *testing.T,
	attachment *freeBitmapReservationAttachment,
) freeBitmapFinalizationResult {
	t.Helper()
	capacity := attachment.privatePages
	result, problem := attachment.finalize(freeBitmapFinalizationScratch{
		releasePages: make([]uint32, capacity),
		insertPages:  make([]freeBitmapInsertPage, capacity*freeBitmapPathCapacity+freeBitmapPathCapacity),
		cachedPages:  make([]freeBitmapFinalizationCachedPage, capacity*freeBitmapPathCapacity),
		indexStack:   make([]int, len(attachment.cow.indexNodes)),
		cache:        &freeBitmapFinalizationCachedSource{},
		cleanup:      cleanupScratchForAttachment(attachment),
	})
	if problem.failed() {
		t.Fatalf("finalize = %#v", problem)
	}
	return result
}

func finalizationScratchForAttachment(attachment *freeBitmapReservationAttachment) freeBitmapFinalizationScratch {
	capacity := attachment.privatePages
	return freeBitmapFinalizationScratch{
		releasePages: make([]uint32, capacity),
		insertPages:  make([]freeBitmapInsertPage, capacity*freeBitmapPathCapacity+freeBitmapPathCapacity),
		cachedPages:  make([]freeBitmapFinalizationCachedPage, capacity*freeBitmapPathCapacity),
		indexStack:   make([]int, len(attachment.cow.indexNodes)),
		cache:        &freeBitmapFinalizationCachedSource{},
		stage:        &freeBitmapFinalizationDetachedStage{},
		cleanup:      cleanupScratchForAttachment(attachment),
	}
}

func newFinalizationScratchFixture(t *testing.T) freeBitmapReservationAttachment {
	t.Helper()
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 2, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 899, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	return attachment
}

func TestSelectiveFinalizationScratchCapacityPreflightIsZeroMutationAndRetryable(t *testing.T) {
	for _, test := range []struct {
		name     string
		resource freeBitmapReservationResource
	}{
		{"release-pages", freeBitmapResourceCandidatePages},
		{"insert-pages", freeBitmapResourceArenaPages},
		{"cached-pages", freeBitmapResourceVerifiedPages},
		{"index-stack", freeBitmapResourceIndexNodes},
	} {
		t.Run(test.name, func(t *testing.T) {
			attachment := newFinalizationScratchFixture(t)
			scratch := finalizationScratchForAttachment(&attachment)
			required, problem := finalizationScratchRequirements(&attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			var expected int
			switch test.resource {
			case freeBitmapResourceCandidatePages:
				expected = required.releasePages
			case freeBitmapResourceArenaPages:
				expected = required.insertPages
			case freeBitmapResourceVerifiedPages:
				expected = required.cachedPages
			case freeBitmapResourceIndexNodes:
				expected = required.indexStack
			}
			if expected <= 0 {
				t.Fatalf("fixture has no required budget for resource %d", test.resource)
			}
			switch test.resource {
			case freeBitmapResourceCandidatePages:
				scratch.releasePages = scratch.releasePages[:expected-1]
			case freeBitmapResourceArenaPages:
				scratch.insertPages = scratch.insertPages[:expected-1]
			case freeBitmapResourceVerifiedPages:
				scratch.cachedPages = scratch.cachedPages[:expected-1]
			case freeBitmapResourceIndexNodes:
				scratch.indexStack = scratch.indexStack[:expected-1]
			}
			beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
			beforeRelease := make([]uint32, len(scratch.releasePages))
			beforeInsert := make([]freeBitmapInsertPage, len(scratch.insertPages))
			beforeCached := make([]freeBitmapFinalizationCachedPage, len(scratch.cachedPages))
			beforeStack := make([]int, len(scratch.indexStack))
			copy(beforeRelease, scratch.releasePages)
			copy(beforeInsert, scratch.insertPages)
			copy(beforeCached, scratch.cachedPages)
			copy(beforeStack, scratch.indexStack)
			beforeCache := *scratch.cache
			_, problem = attachment.finalize(scratch)
			if problem.code != freeBitmapCOWErrInsufficientResourceBudget || problem.resource != test.resource ||
				problem.required != expected || problem.actual != expected-1 {
				t.Fatalf("capacity preflight = %#v", problem)
			}
			if freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) != beforeScope ||
				!beforePool.matches(attachment.cow.pool) ||
				!reflect.DeepEqual(scratch.releasePages, beforeRelease) ||
				!reflect.DeepEqual(scratch.insertPages, beforeInsert) ||
				!reflect.DeepEqual(scratch.cachedPages, beforeCached) ||
				!reflect.DeepEqual(scratch.indexStack, beforeStack) || !reflect.DeepEqual(*scratch.cache, beforeCache) {
				t.Fatal("capacity preflight mutated live or scratch state")
			}
			if _, problem := attachment.finalize(finalizationScratchForAttachment(&attachment)); problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationScratchAliasesAreRejectedBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		alias func(*freeBitmapReservationAttachment, *freeBitmapFinalizationScratch)
	}{
		{name: "cow-candidates", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.cow.candidates
		}},
		{name: "cow-replacements", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.cow.replacements
		}},
		{name: "pool-validation", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.buffers.poolValidation
		}},
		{name: "stage-validation", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.buffers.stage.poolValidation
		}},
		{name: "stage-replacements", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.buffers.stage.replacements
		}},
		{name: "cow-single-insert", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.cow.singleInsertPage[:]
		}},
		{name: "stage-single-insert", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.releasePages = p.buffers.stage.cow.singleInsertPage[:]
		}},
		{name: "available-slots", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.indexStack = p.cow.availableSlots
		}},
		{name: "stage-available", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.indexStack = p.buffers.stage.availableSlots
		}},
		{name: "cow-clone-slots", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.indexStack = p.cow.cloneSlots[:]
		}},
		{name: "stage-clone-slots", alias: func(p *freeBitmapReservationAttachment, s *freeBitmapFinalizationScratch) {
			s.indexStack = p.buffers.stage.cow.cloneSlots[:]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			attachment := newFinalizationScratchFixture(t)
			scratch := finalizationScratchForAttachment(&attachment)
			test.alias(&attachment, &scratch)
			beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
			if _, problem := attachment.finalize(scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("alias preflight = %#v", problem)
			}
			if freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) != beforeScope ||
				!beforePool.matches(attachment.cow.pool) {
				t.Fatal("alias preflight mutated live state")
			}
			if _, problem := attachment.finalize(finalizationScratchForAttachment(&attachment)); problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationCleanupScratchCapacityAndAliasesAreAtomic(t *testing.T) {
	for _, field := range []string{"nodes", "path", "targets"} {
		t.Run("capacity-"+field, func(t *testing.T) {
			attachment := newFinalizationScratchFixture(t)
			scratch := finalizationScratchForAttachment(&attachment)
			required, problem := finalizationScratchRequirements(&attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			expected, resource := 0, freeBitmapResourceAvailableSlots
			switch field {
			case "nodes":
				expected, resource = required.cleanupNodes, freeBitmapResourceIndexNodes
				scratch.cleanup.nodes = scratch.cleanup.nodes[:expected-1]
			case "path":
				expected = required.cleanupPath
				scratch.cleanup.path = scratch.cleanup.path[:expected-1]
			case "targets":
				expected = required.cleanupSlots
				scratch.cleanup.targets = scratch.cleanup.targets[:expected-1]
			}
			if expected <= 0 {
				t.Fatalf("zero requirement for %s", field)
			}
			beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
			beforeSeal := sealFreeBitmapCleanupScratch(scratch.cleanup)
			beforeNodes := append([]freeBitmapCleanupOverlayNode(nil), scratch.cleanup.nodes...)
			beforePath := append([]int(nil), scratch.cleanup.path...)
			beforeTargets := append([]int(nil), scratch.cleanup.targets...)
			if _, problem = attachment.finalize(scratch); problem.code != freeBitmapCOWErrInsufficientResourceBudget ||
				problem.resource != resource || problem.required != expected || problem.actual != expected-1 {
				t.Fatalf("cleanup scratch capacity = %#v", problem)
			}
			if freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) != beforeScope ||
				!beforePool.matches(attachment.cow.pool) || !beforeSeal.matches(scratch.cleanup) ||
				!reflect.DeepEqual(scratch.cleanup.nodes, beforeNodes) ||
				!reflect.DeepEqual(scratch.cleanup.path, beforePath) ||
				!reflect.DeepEqual(scratch.cleanup.targets, beforeTargets) {
				t.Fatal("cleanup capacity rejection mutated live state or caller scratch")
			}
			if _, problem = attachment.finalize(finalizationScratchForAttachment(&attachment)); problem.failed() {
				t.Fatalf("cleanup capacity retry = %#v", problem)
			}
		})
	}

	for _, alias := range []string{"path-targets", "index-path", "index-targets"} {
		t.Run("alias-"+alias, func(t *testing.T) {
			attachment := newFinalizationScratchFixture(t)
			scratch := finalizationScratchForAttachment(&attachment)
			required, problem := finalizationScratchRequirements(&attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			sharedLen := 0
			for _, candidate := range []int{required.cleanupPath, required.cleanupSlots, required.indexStack} {
				if candidate > sharedLen {
					sharedLen = candidate
				}
			}
			shared := make([]int, sharedLen)
			switch alias {
			case "path-targets":
				scratch.cleanup.path = shared[:required.cleanupPath]
				scratch.cleanup.targets = shared[:required.cleanupSlots]
			case "index-path":
				scratch.indexStack = shared[:required.indexStack]
				scratch.cleanup.path = shared[:required.cleanupPath]
			case "index-targets":
				scratch.indexStack = shared[:required.indexStack]
				scratch.cleanup.targets = shared[:required.cleanupSlots]
			}
			beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
			if _, problem = attachment.finalize(scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("cleanup scratch alias = %#v", problem)
			}
			if freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) != beforeScope ||
				!beforePool.matches(attachment.cow.pool) {
				t.Fatal("cleanup alias rejection mutated live state")
			}
		})
	}
}

func TestSelectiveFinalizationSealsExactScopeAndSuccessor(t *testing.T) {
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 2, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 900, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatalf("bind = %#v", problem)
	}
	oldScope := attachment.scope
	oldBindings := append([]bitmapCOWArenaBinding(nil), attachment.cow.arenaBindings...)
	result := finalizeLateBitmapAttachment(t, &attachment)
	if result.output.root == 0 || result.output.pageCount != attachment.cow.pool.pendingPageCount ||
		result.released.pendingPageCount != result.output.pageCount {
		t.Fatalf("sealed output = root %d pages %d released %+v", result.output.root, result.output.pageCount, result.released)
	}
	if _, problem := attachment.cow.pool.validateScope(oldScope); problem.code != privatePagePoolErrStaleScope {
		t.Fatalf("old scope remains active: %#v", problem)
	}
	if problem := attachment.cow.validateScopedBindings(); !problem.failed() {
		t.Fatal("pre-finalization bitmap authority remains valid")
	}
	for index, old := range oldBindings {
		binding := result.output.bindings[index]
		if binding.bound && binding.poolEpoch == old.poolEpoch {
			t.Fatalf("retained binding %d epoch did not advance", index)
		}
	}
	var page [PageSize]byte
	if status := result.output.readPage(result.output.root, &page); status.failed() {
		t.Fatalf("sealed output root unreadable: %#v", status)
	}
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatalf("first successor consume = %#v", problem)
	}
	if _, problem := result.successor.consume(); problem.code != freeBitmapCOWErrStaleReservationPredecessor {
		t.Fatalf("copied successor reused = %#v", problem)
	}
	anchor, poolProblem := attachment.cow.pool.validateSealedScope(result.output.scope)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	for member, visited := anchor.scopeMemberHead, 0; member != privatePagePoolNoIndex; visited++ {
		if visited >= anchor.scopeCapacity {
			t.Fatal("sealed scope member cycle")
		}
		slot := &attachment.cow.pool.slots[member]
		if slot.bound && slot.state == privatePageAvailable {
			t.Fatalf("sealed scope retains available page %d", slot.pageNumber)
		}
		member = slot.scopeMemberNext
	}
	if problem := predecessor.cleanup(); problem.failed() {
		t.Fatalf("sealed cleanup = %#v", problem)
	}
	if attachment.cow.pool.activeScopes != 0 || attachment.cow.pool.unscopedVacantCount != len(attachment.cow.pool.slots) {
		t.Fatal("sealed cleanup did not return exact scope capacity")
	}
	if _, poolProblem := attachment.cow.pool.reserveScope(attachment.privatePages); poolProblem.failed() {
		t.Fatalf("cleaned finalization capacity was not reusable: %#v", poolProblem)
	}
	if status := result.output.checkAccessStatus(); status.code != pageSourceErrForkedHandle {
		t.Fatalf("cleaned output remains readable: %#v", status)
	}
}

func TestSelectiveFinalizationBudgetFailureIsRetryable(t *testing.T) {
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 2, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 901, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	before := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	_, problem := attachment.finalize(freeBitmapFinalizationScratch{})
	if !problem.failed() {
		t.Fatal("missing finalization scratch succeeded")
	}
	if after := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope); after != before {
		t.Fatal("failed finalization mutated live scope")
	}
	if _, poolProblem := attachment.cow.pool.validateScope(attachment.scope); poolProblem.failed() {
		t.Fatalf("failed finalization consumed active scope: %#v", poolProblem)
	}
	finalizeLateBitmapAttachment(t, &attachment)
}

func TestSelectiveFinalizationCleanupRequiresConsumedSuccessor(t *testing.T) {
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 2, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 902, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	result := finalizeLateBitmapAttachment(t, &attachment)
	beforeMutation := result.output.pool.mutationEpoch
	beforeSlots := append([]privatePagePoolSlot(nil), result.output.pool.slots...)
	unauthorized := freeBitmapFinalizationPredecessor{output: result.output}
	if problem := unauthorized.cleanup(); problem.code != freeBitmapCOWErrStaleReservationPredecessor {
		t.Fatalf("cleanup before successor consume = %#v", problem)
	}
	if result.output.pool.mutationEpoch != beforeMutation || !reflect.DeepEqual(result.output.pool.slots, beforeSlots) {
		t.Fatal("rejected cleanup mutated sealed scope")
	}
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}
	forged := predecessor
	forged.nonce++
	beforeMutation = result.output.pool.mutationEpoch
	if problem := forged.cleanup(); problem.code != freeBitmapCOWErrStaleReservationPredecessor {
		t.Fatalf("forged cleanup permit = %#v", problem)
	}
	var page [PageSize]byte
	outputCopy := result.output
	if result.output.pool.mutationEpoch != beforeMutation || outputCopy.readPage(outputCopy.root, &page).failed() {
		t.Fatal("forged cleanup changed copied sealed output")
	}
	if problem := predecessor.cleanup(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSelectiveFinalizationCleanupRejectsCorruptMemberChainBeforeMutation(t *testing.T) {
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 2, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 903, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	result := finalizeLateBitmapAttachment(t, &attachment)
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}
	anchor := &result.output.pool.slots[result.output.scope.anchor]
	anchor.scopeMemberNext = result.output.scope.anchor
	beforeMutation := result.output.pool.mutationEpoch
	beforeSlots := append([]privatePagePoolSlot(nil), result.output.pool.slots...)
	if problem := predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("cleanup with corrupt member chain = %#v", problem)
	}
	if result.output.pool.mutationEpoch != beforeMutation || !reflect.DeepEqual(result.output.pool.slots, beforeSlots) {
		t.Fatal("corrupt-chain rejection mutated sealed scope")
	}
}

func newSelectiveFinalizationCleanupBoundaryAttachment(t *testing.T) freeBitmapReservationAttachment {
	t.Helper()
	storage := newLateBitmapPlannerStorage(32, 32, 32, 64)
	attachment := newLateBitmapPlan(
		t, &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
		2, 4, &storage,
	)
	proof := completeLateBitmapProof(t, &attachment, 898, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	return attachment
}

func finalizedSelectiveCleanupBoundaryAttachment(
	t *testing.T,
) freeBitmapFinalizationResult {
	t.Helper()
	attachment := newSelectiveFinalizationCleanupBoundaryAttachment(t)
	result, problem := attachment.finalize(finalizationScratchForAttachment(&attachment))
	if problem.failed() {
		t.Fatal(problem)
	}
	if result.output.boundLen <= 0 || result.output.boundLen >= len(result.output.bindings) {
		t.Fatalf(
			"cleanup boundary fixture needs bound and unbound members: bound=%d capacity=%d",
			result.output.boundLen, len(result.output.bindings),
		)
	}
	return result
}

type selectiveForeignCleanupFixture struct {
	result       freeBitmapFinalizationResult
	foreignScope privatePageReservationScope
	foreignSlots []int
}

type selectiveForeignFinalizationFixture struct {
	attachment   freeBitmapReservationAttachment
	foreignScope privatePageReservationScope
	foreignSlots []int
}

func retainSelectiveFinalizationPages(
	t *testing.T,
	attachment *freeBitmapReservationAttachment,
	count int,
) {
	t.Helper()
	checkpoint, poolProblem := attachment.cow.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	retained := 0
	for _, binding := range attachment.cow.arenaBindings {
		slot := &attachment.cow.pool.slots[binding.poolSlot]
		if slot.state != privatePageAvailable || slot.authorization == privatePageAppended {
			continue
		}
		token, claimProblem := attachment.cow.pool.claimPageInScope(
			checkpoint, attachment.scope, slot.pageNumber,
			privatePageOwnerBitmap, privatePageBitmap,
		)
		if claimProblem.failed() {
			_ = attachment.cow.pool.rollback(checkpoint)
			t.Fatal(claimProblem)
		}
		var page [PageSize]byte
		writeFreeBitmapHeader(
			&page, PageTypeBitmapLeaf, attachment.cow.pendingTxn, 0, 0, bitmapLeafLower,
		)
		if poolProblem = attachment.cow.pool.writePageInScope(
			attachment.scope, token, &page,
		); poolProblem.failed() {
			_ = attachment.cow.pool.rollback(checkpoint)
			t.Fatal(poolProblem)
		}
		retained++
		if retained == count {
			break
		}
	}
	if retained != count {
		_ = attachment.cow.pool.rollback(checkpoint)
		t.Fatalf("retained selective pages = %d, want %d", retained, count)
	}
	if poolProblem = attachment.cow.pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem := attachment.cow.synchronizeScopedBindings(
		attachment.scope,
	); problem.failed() {
		t.Fatal(problem)
	}
	attachment.cow.mutationEpoch = attachment.cow.pool.mutationEpoch
}

func requireValidPrivatePageDeleteTree(
	t *testing.T,
	pool *privatePagePool,
	scope privatePageReservationScope,
	tree privatePageDeleteTree,
	root int,
) {
	t.Helper()
	seen := make(map[int]struct{})
	var visit func(int, uint64, uint64) (int, uint64, uint64)
	visit = func(slotIndex int, lower, upper uint64) (int, uint64, uint64) {
		if slotIndex == privatePagePoolNoIndex {
			return 0, 0, 0
		}
		if slotIndex < 0 || slotIndex >= len(pool.slots) {
			t.Fatalf("tree %d link %d outside pool", tree, slotIndex)
		}
		if _, duplicate := seen[slotIndex]; duplicate {
			t.Fatalf("tree %d repeats slot %d", tree, slotIndex)
		}
		seen[slotIndex] = struct{}{}
		slot := &pool.slots[slotIndex]
		page := uint64(slot.pageNumber)
		if !slot.bound || page <= lower || page >= upper ||
			(tree == privatePageDeleteScope &&
				(slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor)) {
			t.Fatalf("tree %d invalid slot %d page %d bounds %d..%d", tree, slotIndex, page, lower, upper)
		}
		left, right, height, free, inUse :=
			slot.indexLeft, slot.indexRight, slot.indexHeight, slot.indexFree, slot.indexInUse
		if tree == privatePageDeleteScope {
			left, right, height, free, inUse =
				slot.scopeLeft, slot.scopeRight, slot.scopeHeight, slot.scopeFree, slot.scopeInUse
		}
		leftHeight, leftFree, leftInUse := visit(left, lower, page)
		rightHeight, rightFree, rightInUse := visit(right, page, upper)
		wantHeight := leftHeight
		if rightHeight > wantHeight {
			wantHeight = rightHeight
		}
		wantHeight++
		selfFree, selfInUse, ok := privatePageDeleteStateCounts(slot)
		if !ok || int(height) != wantHeight ||
			free != leftFree+rightFree+selfFree ||
			inUse != leftInUse+rightInUse+selfInUse {
			t.Fatalf(
				"tree %d slot %d cache h=%d/%d free=%d/%d inuse=%d/%d",
				tree, slotIndex, height, wantHeight, free, leftFree+rightFree+selfFree,
				inUse, leftInUse+rightInUse+selfInUse,
			)
		}
		return wantHeight, free, inUse
	}
	visit(root, 0, uint64(1)<<32)
}

func newSelectiveForeignFinalizationFixture(t *testing.T) selectiveForeignFinalizationFixture {
	t.Helper()
	storage := newLateBitmapPlannerStorage(32, 32, 32, 64)
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
	capacity := newLateBitmapCapacityPlanAt(t, source, 20, 2, 4, &storage)
	foreignPages := []uint32{3, 4, 6, 8, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]reservedBitmapPage, capacity.privatePages+len(foreignPages)), 20, 20, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	foreignScope, poolProblem := pool.reserveScope(len(foreignPages))
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bitmapScope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(pool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 897, []uint32{7})
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	retainSelectiveFinalizationPages(t, &attachment, 2)
	foreignSlots := make([]int, len(foreignPages))
	for index, page := range foreignPages {
		foreignSlots[index] = bindForeignLateBitmapPage(
			t, pool, foreignScope, page, privatePageReclaimed, index%5 == 0,
		)
	}
	return selectiveForeignFinalizationFixture{
		attachment: attachment, foreignScope: foreignScope, foreignSlots: foreignSlots,
	}
}

func newSelectiveForeignZeroTailFinalizationFixture(
	t *testing.T,
) selectiveForeignFinalizationFixture {
	t.Helper()
	const committedPages = 64
	storage := newLateBitmapPlannerStorage(64, 64, 64, 128)
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
	capacity := newLateBitmapCapacityPlanAt(t, source, committedPages, 2, 4, &storage)
	foreignPages := []uint32{4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28, 30}
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]reservedBitmapPage, capacity.privatePages+len(foreignPages)),
		committedPages, committedPages, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	foreignScope, poolProblem := pool.reserveScope(len(foreignPages))
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bitmapScope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(pool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	reclaimedCandidates := []uint32{3, 7, 11, 13, 15, 17, 19, 21}
	reclaimed := reclaimedCandidates[:capacity.privatePages]
	proof := completeLateBitmapProof(t, &attachment, 896, reclaimed)
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatalf("zero-tail bind = %#v private=%d reclaimed=%v",
			problem, capacity.privatePages, reclaimed)
	}
	retainSelectiveFinalizationPages(t, &attachment, 2)
	foreignSlots := make([]int, len(foreignPages))
	for index, page := range foreignPages {
		foreignSlots[index] = bindForeignLateBitmapPage(
			t, pool, foreignScope, page, privatePageReclaimed, index%5 == 0,
		)
	}
	if pool.pendingPageCount != committedPages {
		t.Fatalf("zero-tail fixture appended pages: pending=%d committed=%d",
			pool.pendingPageCount, committedPages)
	}
	return selectiveForeignFinalizationFixture{
		attachment: attachment, foreignScope: foreignScope, foreignSlots: foreignSlots,
	}
}

func newSelectiveForeignNonzeroTailRefreshFixture(
	t *testing.T,
) selectiveForeignFinalizationFixture {
	t.Helper()
	const committedPages = 512
	storage := newLateBitmapPlannerStorage(256, 256, 256, 512)
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}}
	capacity := newLateBitmapCapacityPlanAt(t, source, committedPages, 2, 16, &storage)
	foreignPages := make([]uint32, 96)
	for index := range foreignPages {
		foreignPages[index] = uint32(index*2 + 4)
	}
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]reservedBitmapPage, capacity.privatePages+len(foreignPages)),
		committedPages, committedPages, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	foreignScope, poolProblem := pool.reserveScope(len(foreignPages))
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bitmapScope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(pool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	reclaimedCandidates := []uint32{
		3, 7, 25, 51, 75, 99, 123, 147, 171, 191, 201, 211, 221, 231,
	}
	reclaimed := reclaimedCandidates[:capacity.privatePages-3]
	proof := completeLateBitmapProof(t, &attachment, 895, reclaimed)
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	retainSelectiveFinalizationPages(t, &attachment, 8)
	foreignSlots := make([]int, len(foreignPages))
	for index, page := range foreignPages {
		foreignSlots[index] = bindForeignLateBitmapPage(
			t, pool, foreignScope, page, privatePageReclaimed, index%5 == 0,
		)
	}
	if pool.pendingPageCount == committedPages {
		t.Fatal("nonzero-tail fixture appended no pages")
	}
	return selectiveForeignFinalizationFixture{
		attachment: attachment, foreignScope: foreignScope, foreignSlots: foreignSlots,
	}
}

func finalizedSelectiveForeignCleanupFixture(t *testing.T) selectiveForeignCleanupFixture {
	t.Helper()
	fixture := newSelectiveForeignFinalizationFixture(t)
	result, problem := fixture.attachment.finalize(
		finalizationScratchForAttachment(&fixture.attachment),
	)
	if problem.failed() {
		t.Fatalf("foreign fixture finalization = %#v", problem)
	}
	if result.output.boundLen < 3 {
		t.Fatalf("foreign cleanup fixture bound pages = %d", result.output.boundLen)
	}
	requireValidPrivatePageDeleteTree(
		t, result.output.pool, result.output.scope, privatePageDeleteGlobal, result.output.pool.indexRoot,
	)
	requireValidPrivatePageDeleteTree(
		t, result.output.pool, result.output.scope, privatePageDeleteScope,
		result.output.pool.slots[result.output.scope.anchor].scopeRoot,
	)
	return selectiveForeignCleanupFixture{
		result: result, foreignScope: fixture.foreignScope, foreignSlots: fixture.foreignSlots,
	}
}

func TestSelectiveFinalizationCleanupExactPerMemberEpochHeadroom(t *testing.T) {
	for _, test := range []struct {
		name      string
		bound     bool
		remaining uint64
		succeeds  bool
	}{
		{name: "bound-exact-two", bound: true, remaining: 2, succeeds: true},
		{name: "bound-one-short", bound: true, remaining: 1},
		{name: "unbound-exact-one", remaining: 1, succeeds: true},
		{name: "unbound-one-short", remaining: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calibration := finalizedSelectiveCleanupBoundaryAttachment(t)
			target := 0
			if !test.bound {
				target = calibration.output.boundLen
			}
			attachment := newSelectiveFinalizationCleanupBoundaryAttachment(t)
			liveBinding := &attachment.cow.arenaBindings[target]
			liveSlot := &attachment.cow.pool.slots[liveBinding.poolSlot]
			livePoolSlot := liveBinding.poolSlot
			expectedOutputEpoch := ^uint64(0) - test.remaining
			liveSlot.epoch = expectedOutputEpoch - 1
			liveBinding.poolEpoch = liveSlot.epoch
			result, problem := attachment.finalize(finalizationScratchForAttachment(&attachment))
			if problem.failed() {
				t.Fatalf("boundary finalization = %#v", problem)
			}
			if result.output.boundLen != calibration.output.boundLen ||
				result.output.pool.slots[livePoolSlot].bound != test.bound {
				t.Fatalf("boundary classification changed: bound=%d slot=%+v",
					result.output.boundLen, result.output.pool.slots[livePoolSlot])
			}
			slot := &result.output.pool.slots[livePoolSlot]
			if slot.epoch != expectedOutputEpoch {
				t.Fatalf("finalization epoch refresh = slot %d expected %d", slot.epoch, expectedOutputEpoch)
			}
			if test.bound && result.output.bindings[target].poolEpoch != expectedOutputEpoch {
				t.Fatalf("finalization binding epoch = %d expected %d",
					result.output.bindings[target].poolEpoch, expectedOutputEpoch)
			}
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}

			beforeSlots := append([]privatePagePoolSlot(nil), result.output.pool.slots...)
			beforeBindings := append([]bitmapCOWArenaBinding(nil), result.output.bindings...)
			beforeMutation := result.output.pool.mutationEpoch
			beforeActiveScopes := result.output.pool.activeScopes
			problem = predecessor.cleanup()
			if test.succeeds {
				if problem.failed() {
					t.Fatalf("exact cleanup = %#v", problem)
				}
				if result.output.pool.slots[livePoolSlot].epoch != ^uint64(0) ||
					result.output.pool.activeScopes != beforeActiveScopes-1 {
					t.Fatal("exact cleanup did not consume the exact remaining epoch headroom")
				}
				return
			}
			if problem.code != freeBitmapCOWErrMutationEpochExhausted {
				t.Fatalf("one-short cleanup = %#v", problem)
			}
			if result.output.pool.mutationEpoch != beforeMutation ||
				result.output.pool.activeScopes != beforeActiveScopes ||
				!reflect.DeepEqual(result.output.pool.slots, beforeSlots) ||
				!reflect.DeepEqual(result.output.bindings, beforeBindings) {
				t.Fatal("one-short cleanup mutated sealed state")
			}
		})
	}
}

func TestSelectiveFinalizationCleanupRejectsNoncanonicalUnboundSuffixAtomically(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*privatePagePoolSlot)
	}{
		{name: "authorization", mutate: func(slot *privatePagePoolSlot) { slot.authorization = privatePageReclaimed }},
		{name: "state", mutate: func(slot *privatePagePoolSlot) { slot.state = privatePageInUse }},
		{name: "owner-origin", mutate: func(slot *privatePagePoolSlot) {
			slot.owner, slot.origin = privatePageOwnerBitmap, privatePageBitmap
		}},
		{name: "transaction", mutate: func(slot *privatePagePoolSlot) { slot.pendingTxn = 2 }},
		{name: "generation", mutate: func(slot *privatePagePoolSlot) { slot.generation = 1 }},
		{name: "committed-origin", mutate: func(slot *privatePagePoolSlot) { slot.committedOrigin = 7 }},
		{name: "in-use", mutate: func(slot *privatePagePoolSlot) { slot.inUse = true }},
		{name: "pending-return", mutate: func(slot *privatePagePoolSlot) { slot.pendingReturnState = privatePageReleasedFree }},
		{name: "validation-mark", mutate: func(slot *privatePagePoolSlot) { slot.batchMarked = true }},
		{name: "global-index", mutate: func(slot *privatePagePoolSlot) {
			slot.indexLeft, slot.indexHeight, slot.indexFree = 0, 1, 1
		}},
		{name: "scope-index", mutate: func(slot *privatePagePoolSlot) {
			slot.scopeRight, slot.scopeHeight, slot.scopeInUse = 0, 1, 1
		}},
		{name: "payload", mutate: func(slot *privatePagePoolSlot) { slot.bytes[PageHeaderSize] = 0xa5 }},
		{name: "slot-checkpoint", mutate: func(slot *privatePagePoolSlot) { slot.checkpointID = 1 }},
		{name: "index-checkpoint", mutate: func(slot *privatePagePoolSlot) { slot.indexCheckpointNext = 0 }},
		{name: "scope-checkpoint", mutate: func(slot *privatePagePoolSlot) { slot.scopeCheckpointID = 1 }},
		{name: "unscoped-link", mutate: func(slot *privatePagePoolSlot) { slot.unscopedPrevious = 0 }},
		{name: "scoped-vacancy-link", mutate: func(slot *privatePagePoolSlot) { slot.scopeVacantNext = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := finalizedSelectiveCleanupBoundaryAttachment(t)
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}
			target := result.output.pool.slots[result.output.scope.anchor].scopeVacantHead
			if target == privatePagePoolNoIndex {
				t.Fatal("cleanup fixture has no returned-free vacancy")
			}
			slot := &result.output.pool.slots[target]
			test.mutate(slot)
			beforeSlots := append([]privatePagePoolSlot(nil), result.output.pool.slots...)
			beforeMutation := result.output.pool.mutationEpoch
			beforeActiveScopes := result.output.pool.activeScopes
			beforeVacantHead, beforeVacantTail, beforeVacantCount :=
				result.output.pool.unscopedVacantHead,
				result.output.pool.unscopedVacantTail,
				result.output.pool.unscopedVacantCount
			if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("noncanonical suffix cleanup = %#v", problem)
			}
			if result.output.pool.mutationEpoch != beforeMutation ||
				result.output.pool.activeScopes != beforeActiveScopes ||
				result.output.pool.unscopedVacantHead != beforeVacantHead ||
				result.output.pool.unscopedVacantTail != beforeVacantTail ||
				result.output.pool.unscopedVacantCount != beforeVacantCount ||
				!reflect.DeepEqual(result.output.pool.slots, beforeSlots) {
				t.Fatal("noncanonical suffix rejection mutated sealed state")
			}
		})
	}
}

func TestSelectiveFinalizationCanonicalUnboundSuffixCleanupIsReusable(t *testing.T) {
	for _, kind := range []string{"mixed", "all-unbound"} {
		t.Run(kind, func(t *testing.T) {
			var result freeBitmapFinalizationResult
			if kind == "mixed" {
				result = finalizedSelectiveCleanupBoundaryAttachment(t)
			} else {
				attachment, scratch := newAppendOnlyFinalizationFixture(t, 4)
				var problem freeBitmapCOWError
				result, problem = attachment.finalize(scratch)
				if problem.failed() || result.output.boundLen != 0 {
					t.Fatalf("all-unbound finalization = %#v bound=%d", problem, result.output.boundLen)
				}
			}
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}
			capacity := len(result.output.bindings)
			if problem = predecessor.cleanup(); problem.failed() {
				t.Fatal(problem)
			}
			if _, poolProblem := result.output.pool.reserveScope(capacity); poolProblem.failed() {
				t.Fatalf("canonical cleaned suffix was not reusable: %#v", poolProblem)
			}
		})
	}
}

func TestSelectiveFinalizationAllUnboundCleanupRejectsMalformedRootsAtomically(t *testing.T) {
	for _, corruption := range []struct {
		name        string
		scope       bool
		bindingSlot bool
		value       func(*privatePagePool) int
	}{
		{name: "global-first-tag", value: func(*privatePagePool) int {
			return privatePageDeleteOverlayReference(0)
		}},
		{name: "global-out-of-range-tag", value: func(*privatePagePool) int {
			return privatePageDeleteOverlayReference(7)
		}},
		{name: "global-invalid-original", value: func(pool *privatePagePool) int {
			return len(pool.slots)
		}},
		{name: "global-scoped-vacancy", bindingSlot: true},
		{name: "scope-first-tag", scope: true, value: func(*privatePagePool) int {
			return privatePageDeleteOverlayReference(0)
		}},
		{name: "scope-out-of-range-tag", scope: true, value: func(*privatePagePool) int {
			return privatePageDeleteOverlayReference(7)
		}},
		{name: "scope-invalid-original", scope: true, value: func(pool *privatePagePool) int {
			return len(pool.slots)
		}},
		{name: "scope-scoped-vacancy", scope: true, bindingSlot: true},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			attachment, scratch := newAppendOnlyFinalizationFixture(t, 4)
			result, problem := attachment.finalize(scratch)
			if problem.failed() || result.output.boundLen != 0 {
				t.Fatalf("all-unbound finalization = %#v bound=%d",
					problem, result.output.boundLen)
			}
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}
			root := &result.output.pool.indexRoot
			if corruption.scope {
				root = &result.output.pool.slots[result.output.scope.anchor].scopeRoot
			}
			canonical := *root
			if canonical != privatePagePoolNoIndex {
				t.Fatalf("all-unbound canonical root=%d", canonical)
			}
			if corruption.bindingSlot {
				slotIndex := result.output.bindings[0].poolSlot
				if !result.output.pool.validScopedVacancySlot(
					result.output.scope, slotIndex,
				) {
					t.Fatalf("wrong-role root fixture slot %d is not a scoped vacancy", slotIndex)
				}
				*root = slotIndex
			} else {
				*root = corruption.value(result.output.pool)
			}
			before := snapshotSelectiveCleanupAtomic(result.output)
			if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("malformed all-unbound root cleanup = %#v", problem)
			}
			requireSelectiveCleanupAtomic(t, result.output, before)
			*root = canonical
			if problem = predecessor.cleanup(); problem.failed() {
				t.Fatalf("cleanup after root repair = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationAllUnboundCleanupPreservesCanonicalForeignRoot(t *testing.T) {
	attachment, foreignSlot := newAppendOnlyForeignFinalizationFixture(t, 4)
	result, problem := attachment.finalize(finalizationScratchForAttachment(&attachment))
	if problem.failed() || result.output.boundLen != 0 ||
		result.output.pool.indexRoot != foreignSlot {
		t.Fatalf("foreign all-unbound finalization = %#v bound=%d root=%d/%d",
			problem, result.output.boundLen, result.output.pool.indexRoot, foreignSlot)
	}
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}
	foreignBefore := result.output.pool.slots[foreignSlot]
	if problem = predecessor.cleanup(); problem.failed() {
		t.Fatal(problem)
	}
	if result.output.pool.indexRoot != foreignSlot ||
		result.output.pool.slots[foreignSlot] != foreignBefore {
		t.Fatal("all-unbound cleanup rewrote canonical foreign root")
	}
}

func TestSelectiveFinalizationCleanupRejectsCorruptScopedVacancyHeaderAtomically(t *testing.T) {
	result := finalizedSelectiveCleanupBoundaryAttachment(t)
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}
	anchor := &result.output.pool.slots[result.output.scope.anchor]
	anchor.scopeVacantHead = privatePagePoolNoIndex
	beforeSlots := append([]privatePagePoolSlot(nil), result.output.pool.slots...)
	beforeMutation := result.output.pool.mutationEpoch
	if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("corrupt vacancy header cleanup = %#v", problem)
	}
	if result.output.pool.mutationEpoch != beforeMutation ||
		!reflect.DeepEqual(result.output.pool.slots, beforeSlots) {
		t.Fatal("corrupt vacancy header rejection mutated sealed state")
	}
}

type selectiveCleanupAtomicSnapshot struct {
	pool     privatePagePool
	slots    []privatePagePoolSlot
	bindings []bitmapCOWArenaBinding
	nodes    []freeBitmapCleanupOverlayNode
	path     []int
	targets  []int
}

func snapshotSelectiveCleanupAtomic(output sealedFreeBitmapOutput) selectiveCleanupAtomicSnapshot {
	pool := *output.pool
	pool.slots = nil
	return selectiveCleanupAtomicSnapshot{
		pool: pool, slots: append([]privatePagePoolSlot(nil), output.pool.slots...),
		bindings: append([]bitmapCOWArenaBinding(nil), output.bindings...),
		nodes:    append([]freeBitmapCleanupOverlayNode(nil), output.cleanupScratch.nodes...),
		path:     append([]int(nil), output.cleanupScratch.path...),
		targets:  append([]int(nil), output.cleanupScratch.targets...),
	}
}

func requireSelectiveCleanupAtomic(
	t *testing.T,
	output sealedFreeBitmapOutput,
	before selectiveCleanupAtomicSnapshot,
) {
	t.Helper()
	pool := *output.pool
	pool.slots = nil
	if !reflect.DeepEqual(pool, before.pool) ||
		!reflect.DeepEqual(output.pool.slots, before.slots) ||
		!reflect.DeepEqual(output.bindings, before.bindings) ||
		!reflect.DeepEqual(output.cleanupScratch.nodes, before.nodes) ||
		!reflect.DeepEqual(output.cleanupScratch.path, before.path) ||
		!reflect.DeepEqual(output.cleanupScratch.targets, before.targets) {
		t.Fatal("rejected cleanup changed authoritative state or retained scratch")
	}
}

func TestSelectiveFinalizationCleanupRejectsCorruptDeleteTreesAtomically(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*privatePagePool, privatePageReservationScope)
	}{
		{name: "global-root", mutate: func(pool *privatePagePool, _ privatePageReservationScope) {
			pool.indexRoot = privatePagePoolNoIndex
		}},
		{name: "global-link-bounds", mutate: func(pool *privatePagePool, _ privatePageReservationScope) {
			pool.slots[pool.indexRoot].indexRight = len(pool.slots)
		}},
		{name: "global-cycle", mutate: func(pool *privatePagePool, _ privatePageReservationScope) {
			pool.slots[pool.indexRoot].indexLeft = pool.indexRoot
		}},
		{name: "global-height-cache", mutate: func(pool *privatePagePool, _ privatePageReservationScope) {
			pool.slots[pool.indexRoot].indexHeight++
		}},
		{name: "global-count-cache", mutate: func(pool *privatePagePool, _ privatePageReservationScope) {
			pool.slots[pool.indexRoot].indexFree++
		}},
		{name: "scope-root", mutate: func(pool *privatePagePool, scope privatePageReservationScope) {
			pool.slots[scope.anchor].scopeRoot = privatePagePoolNoIndex
		}},
		{name: "scope-link-bounds", mutate: func(pool *privatePagePool, scope privatePageReservationScope) {
			root := pool.slots[scope.anchor].scopeRoot
			pool.slots[root].scopeRight = len(pool.slots)
		}},
		{name: "scope-cycle", mutate: func(pool *privatePagePool, scope privatePageReservationScope) {
			root := pool.slots[scope.anchor].scopeRoot
			pool.slots[root].scopeLeft = root
		}},
		{name: "scope-height-cache", mutate: func(pool *privatePagePool, scope privatePageReservationScope) {
			root := pool.slots[scope.anchor].scopeRoot
			pool.slots[root].scopeHeight++
		}},
		{name: "scope-count-cache", mutate: func(pool *privatePagePool, scope privatePageReservationScope) {
			root := pool.slots[scope.anchor].scopeRoot
			pool.slots[root].scopeFree++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := finalizedSelectiveCleanupBoundaryAttachment(t)
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}
			test.mutate(result.output.pool, result.output.scope)
			before := snapshotSelectiveCleanupAtomic(result.output)
			if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("corrupt delete tree cleanup = %#v", problem)
			}
			requireSelectiveCleanupAtomic(t, result.output, before)
		})
	}
}

func TestSelectiveFinalizationCleanupScratchOwnershipAndDrift(t *testing.T) {
	result := finalizedSelectiveCleanupBoundaryAttachment(t)
	if !freeBitmapCleanupScratchCanonical(result.output.cleanupScratch) {
		t.Fatal("successful finalization did not transfer canonical cleanup scratch")
	}
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}

	forged := predecessor
	forged.output.cleanupScratch.path = forged.output.cleanupScratch.targets
	before := snapshotSelectiveCleanupAtomic(result.output)
	if problem = forged.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("forged scratch alias = %#v", problem)
	}
	requireSelectiveCleanupAtomic(t, result.output, before)

	result.output.cleanupScratch.path[0] = 1
	before = snapshotSelectiveCleanupAtomic(result.output)
	if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("retained scratch drift = %#v", problem)
	}
	requireSelectiveCleanupAtomic(t, result.output, before)
	result.output.cleanupScratch.path[0] = 0
	if problem = predecessor.cleanup(); problem.failed() {
		t.Fatalf("cleanup after scratch repair = %#v", problem)
	}
	if !freeBitmapCleanupScratchCanonical(result.output.cleanupScratch) {
		t.Fatal("successful cleanup did not release canonical scratch")
	}
}

func TestSelectiveFinalizationInterleavedForeignCleanupAndGlobalReuse(t *testing.T) {
	fixture := finalizedSelectiveForeignCleanupFixture(t)
	result := fixture.result
	foreignBefore := make([]privatePagePoolSlot, len(fixture.foreignSlots))
	for index, slot := range fixture.foreignSlots {
		foreignBefore[index] = normalizedForeignLateBitmapSlot(result.output.pool.slots[slot])
	}
	for targetIndex := 0; targetIndex < result.output.boundLen; targetIndex++ {
		bindingIndex := result.output.boundLen - 1 - targetIndex
		result.output.cleanupScratch.targets[targetIndex] = result.output.bindings[bindingIndex].poolSlot
	}
	plan, problem := preparePrivatePageDeletes(
		result.output.pool, result.output.scope, result.output.cleanupScratch, result.output.boundLen, 0,
	)
	if problem.failed() || plan.rotations == 0 || plan.successors == 0 ||
		plan.work == 0 || plan.nodeLen > len(result.output.cleanupScratch.nodes) {
		t.Fatalf("interleaved delete plan = %#v rotations=%d successors=%d work=%d nodes=%d/%d",
			problem, plan.rotations, plan.successors, plan.work, plan.nodeLen, len(result.output.cleanupScratch.nodes))
	}
	result.output.cleanupScratch.clear()

	predecessor, problem := result.successor.consume()
	if problem.failed() {
		t.Fatal(problem)
	}
	reusablePage := result.output.bindings[0].pageNumber
	capacity := len(result.output.bindings)
	if problem = predecessor.cleanup(); problem.failed() {
		t.Fatalf("interleaved cleanup = %#v", problem)
	}
	for index, slot := range fixture.foreignSlots {
		if got := normalizedForeignLateBitmapSlot(result.output.pool.slots[slot]); got != foreignBefore[index] {
			t.Fatalf("foreign slot %d changed outside global index metadata", slot)
		}
	}
	if _, poolProblem := result.output.pool.validateScope(fixture.foreignScope); poolProblem.failed() {
		t.Fatalf("foreign scope lost after cleanup: %#v", poolProblem)
	}
	reuseScope, poolProblem := result.output.pool.reserveScope(capacity)
	if poolProblem.failed() {
		t.Fatalf("cleaned scope capacity is not reusable: %#v", poolProblem)
	}
	checkpoint, poolProblem := result.output.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if _, poolProblem = result.output.pool.bindPage(
		checkpoint, reuseScope, reusablePage, privatePageReclaimed,
	); poolProblem.failed() {
		t.Fatalf("cleaned global page is not reusable: %#v", poolProblem)
	}
	if poolProblem = result.output.pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
}

func TestSelectiveFinalizationCleanupIsZeroAllocation(t *testing.T) {
	fixtures := [2]selectiveForeignCleanupFixture{
		finalizedSelectiveForeignCleanupFixture(t),
		finalizedSelectiveForeignCleanupFixture(t),
	}
	predecessors := [2]freeBitmapFinalizationPredecessor{}
	for index := range fixtures {
		var problem freeBitmapCOWError
		predecessors[index], problem = fixtures[index].result.successor.consume()
		if problem.failed() {
			t.Fatal(problem)
		}
	}
	call := 0
	var cleanupProblem freeBitmapCOWError
	allocations := testing.AllocsPerRun(1, func() {
		cleanupProblem = predecessors[call].cleanup()
		call++
	})
	if allocations != 0 || cleanupProblem.failed() {
		t.Fatalf("cleanup allocations=%g problem=%#v", allocations, cleanupProblem)
	}
}

func selectiveForeignCleanupDiagnosticPage(
	t *testing.T,
	kind string,
) uint32 {
	t.Helper()
	fixture := finalizedSelectiveForeignCleanupFixture(t)
	result := fixture.result
	for targetIndex := 0; targetIndex < result.output.boundLen; targetIndex++ {
		bindingIndex := result.output.boundLen - 1 - targetIndex
		result.output.cleanupScratch.targets[targetIndex] = result.output.bindings[bindingIndex].poolSlot
	}
	plan, problem := preparePrivatePageDeletes(
		result.output.pool, result.output.scope, result.output.cleanupScratch, result.output.boundLen, 0,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	var page uint32
	for nodeIndex := 0; nodeIndex < plan.nodeLen; nodeIndex++ {
		node := &plan.scratch.nodes[nodeIndex]
		slot := &result.output.pool.slots[node.slot]
		if node.tree != privatePageDeleteGlobal || slot.scopeID == result.output.scope.id {
			continue
		}
		match := false
		switch kind {
		case "successor":
			match = node.successor
		case "rotation":
			match = node.dirty && node.pathOrdinal == 0
		case "later-path":
			match = node.pathOrdinal > 1
		case "foreign-ancestor":
			match = node.dirty && node.pathOrdinal != 0
		}
		if match {
			page = slot.pageNumber
			break
		}
	}
	result.output.cleanupScratch.clear()
	if page == 0 {
		t.Fatalf("fixture has no %s foreign node: rotations=%d successors=%d nodes=%d",
			kind, plan.rotations, plan.successors, plan.nodeLen)
	}
	return page
}

func setSelectiveCheckpointCorruption(
	pool *privatePagePool,
	slotIndex int,
	field string,
	corrupt bool,
) {
	slot := &pool.slots[slotIndex]
	id, link := uint64(0), privatePagePoolNoIndex
	if corrupt {
		id, link = pool.checkpointSequence+1, slotIndex
	}
	switch field {
	case "slot-tag":
		slot.checkpointID = id
	case "slot-link":
		slot.checkpointSlotNext = link
	case "index-tag":
		slot.indexCheckpointID = id
	case "index-link":
		slot.indexCheckpointNext = link
	case "scope-tag":
		slot.scopeCheckpointID = id
	case "scope-link":
		slot.scopeCheckpointNext = link
	default:
		panic("unknown checkpoint corruption")
	}
}

func TestSelectiveFinalizationCleanupRejectsCheckpointTagsAndLinksOnExactTouchedSet(t *testing.T) {
	diagnosticPages := map[string]uint32{
		"rotation":         selectiveForeignCleanupDiagnosticPage(t, "rotation"),
		"successor":        selectiveForeignCleanupDiagnosticPage(t, "successor"),
		"foreign-ancestor": selectiveForeignCleanupDiagnosticPage(t, "foreign-ancestor"),
	}
	roles := []string{"target", "anchor", "rotation", "successor", "foreign-ancestor"}
	fields := []string{"slot-tag", "slot-link", "index-tag", "index-link", "scope-tag", "scope-link"}
	for _, role := range roles {
		for _, field := range fields {
			t.Run(role+"/"+field, func(t *testing.T) {
				fixture := finalizedSelectiveForeignCleanupFixture(t)
				result := fixture.result
				predecessor, problem := result.successor.consume()
				if problem.failed() {
					t.Fatal(problem)
				}
				slotIndex := result.output.scope.anchor
				switch role {
				case "target":
					slotIndex = result.output.bindings[result.output.boundLen-1].poolSlot
				case "rotation", "successor", "foreign-ancestor":
					page := diagnosticPages[role]
					var found bool
					slotIndex, found = result.output.pool.slotIndex(page)
					if !found {
						t.Fatalf("%s page %d is absent", role, page)
					}
				}
				setSelectiveCheckpointCorruption(result.output.pool, slotIndex, field, true)
				before := snapshotSelectiveCleanupAtomic(result.output)
				if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
					t.Fatalf("checkpoint corruption cleanup = %#v", problem)
				}
				requireSelectiveCleanupAtomic(t, result.output, before)
				setSelectiveCheckpointCorruption(result.output.pool, slotIndex, field, false)
				if problem = predecessor.cleanup(); problem.failed() {
					t.Fatalf("cleanup after checkpoint repair = %#v", problem)
				}
			})
		}
	}
}

func selectiveFinalizationForeignAncestor(
	t *testing.T,
	attachment *freeBitmapReservationAttachment,
	targetBinding int,
) int {
	t.Helper()
	target := attachment.cow.arenaBindings[targetBinding]
	node := attachment.cow.pool.indexRoot
	foreign := privatePagePoolNoIndex
	for node != privatePagePoolNoIndex {
		slot := &attachment.cow.pool.slots[node]
		if node == target.poolSlot {
			break
		}
		if slot.scopeID != attachment.scope.id {
			foreign = node
		}
		if target.pageNumber < slot.pageNumber {
			node = slot.indexLeft
		} else {
			node = slot.indexRight
		}
	}
	if node != target.poolSlot || foreign == privatePagePoolNoIndex {
		t.Fatalf("target page %d has no foreign structural ancestor", target.pageNumber)
	}
	return foreign
}

func TestSelectiveFinalizationApplyRejectsCheckpointTagsAndLinksOnExactTouchedSet(t *testing.T) {
	calibration := finalizedSelectiveForeignCleanupFixture(t).result
	boundLen := calibration.output.boundLen
	if boundLen <= 0 || boundLen >= len(calibration.output.bindings) {
		t.Fatalf("finalization fixture bound=%d capacity=%d", boundLen, len(calibration.output.bindings))
	}
	roles := []string{"target", "anchor", "retained", "foreign-ancestor"}
	fields := []string{"slot-tag", "slot-link", "index-tag", "index-link", "scope-tag", "scope-link"}
	for _, role := range roles {
		for _, field := range fields {
			t.Run(role+"/"+field, func(t *testing.T) {
				fixture := newSelectiveForeignFinalizationFixture(t)
				attachment := &fixture.attachment
				slotIndex := attachment.scope.anchor
				switch role {
				case "target":
					slotIndex = attachment.cow.arenaBindings[boundLen].poolSlot
				case "retained":
					slotIndex = attachment.cow.arenaBindings[0].poolSlot
				case "foreign-ancestor":
					slotIndex = selectiveFinalizationForeignAncestor(t, attachment, boundLen)
				}
				setSelectiveCheckpointCorruption(attachment.cow.pool, slotIndex, field, true)
				beforePool := *attachment.cow.pool
				beforePool.slots = nil
				beforeSlots := append([]privatePagePoolSlot(nil), attachment.cow.pool.slots...)
				scratch := finalizationScratchForAttachment(attachment)
				if _, problem := attachment.finalize(scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
					t.Fatalf("checkpoint corruption finalization = %#v", problem)
				}
				afterPool := *attachment.cow.pool
				afterPool.slots = nil
				if !reflect.DeepEqual(afterPool, beforePool) ||
					!reflect.DeepEqual(attachment.cow.pool.slots, beforeSlots) ||
					!freeBitmapCleanupScratchCanonical(scratch.cleanup) {
					t.Fatal("checkpoint rejection changed live pool or retained cleanup scratch")
				}
				setSelectiveCheckpointCorruption(attachment.cow.pool, slotIndex, field, false)
				if _, problem := attachment.finalize(
					finalizationScratchForAttachment(attachment),
				); problem.failed() {
					t.Fatalf("finalization after checkpoint repair = %#v", problem)
				}
			})
		}
	}
}

func selectiveFinalizationRefreshPathAncestor(
	t *testing.T,
	attachment *freeBitmapReservationAttachment,
	boundLen int,
	tree privatePageDeleteTree,
) int {
	t.Helper()
	tailCount := attachment.privatePages - boundLen
	scratch := finalizationScratchForAttachment(attachment).cleanup
	targets := make(map[int]struct{}, tailCount)
	for targetIndex := 0; targetIndex < tailCount; targetIndex++ {
		bindingIndex := attachment.privatePages - 1 - targetIndex
		slotIndex := attachment.cow.arenaBindings[bindingIndex].poolSlot
		scratch.targets[targetIndex] = slotIndex
		targets[slotIndex] = struct{}{}
	}
	plan, problem := preparePrivatePageDeletes(
		attachment.cow.pool, attachment.scope, scratch, tailCount, boundLen,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if boundLen != 0 && plan.workLimit == 0 {
		t.Fatal("retained refresh pages have no work budget")
	}
	overlay := privatePageDeleteOverlay{
		pool: attachment.cow.pool, scope: attachment.scope, scratch: plan.scratch,
		nodeLen: plan.nodeLen, indexRoot: plan.indexRoot, scopeRoot: plan.scopeRoot,
		work: plan.work, workLimit: plan.workLimit,
	}
	for bindingIndex := 0; bindingIndex < boundLen; bindingIndex++ {
		target := attachment.cow.arenaBindings[bindingIndex]
		reference := overlay.indexRoot
		if tree == privatePageDeleteScope {
			reference = overlay.scopeRoot
		}
		for depth := 0; depth < maximumPrivatePageAVLHeight(len(overlay.pool.slots)); depth++ {
			slotIndex, node, ok := overlay.resolve(tree, reference)
			if !ok || slotIndex == privatePagePoolNoIndex {
				break
			}
			slot := &overlay.pool.slots[slotIndex]
			_, deleted := targets[slotIndex]
			if slotIndex != target.poolSlot && slotIndex != attachment.scope.anchor &&
				!deleted && (node == nil || !node.dirty) &&
				(tree != privatePageDeleteGlobal || slot.scopeID != attachment.scope.id) {
				plan.scratch.clear()
				return slotIndex
			}
			left, right := slot.indexLeft, slot.indexRight
			if tree == privatePageDeleteScope {
				left, right = slot.scopeLeft, slot.scopeRight
			}
			if node != nil {
				left, right = node.left, node.right
			}
			switch {
			case target.pageNumber < slot.pageNumber:
				reference = left
			case target.pageNumber > slot.pageNumber:
				reference = right
			default:
				depth = maximumPrivatePageAVLHeight(len(overlay.pool.slots))
			}
		}
	}
	plan.scratch.clear()
	t.Fatalf("fixture has no non-dirty tree %d retained-refresh ancestor: bound=%d tail=%d nodes=%d",
		tree, boundLen, tailCount, plan.nodeLen)
	return privatePagePoolNoIndex
}

func TestSelectiveFinalizationApplyRejectsRefreshPathCheckpointCollisions(t *testing.T) {
	nonzeroCalibration := newSelectiveForeignNonzeroTailRefreshFixture(t)
	nonzeroResult, problem := nonzeroCalibration.attachment.finalize(
		finalizationScratchForAttachment(&nonzeroCalibration.attachment),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	nonzeroTailBoundLen := nonzeroResult.output.boundLen
	if nonzeroTailBoundLen <= 1 ||
		nonzeroTailBoundLen >= len(nonzeroResult.output.bindings) {
		t.Fatalf("nonzero-tail refresh fixture bound=%d capacity=%d",
			nonzeroTailBoundLen, len(nonzeroResult.output.bindings))
	}
	for _, tail := range []struct {
		name    string
		fixture func(*testing.T) selectiveForeignFinalizationFixture
		bound   func(*freeBitmapReservationAttachment) int
	}{
		{
			name: "zero-tail", fixture: newSelectiveForeignZeroTailFinalizationFixture,
			bound: func(attachment *freeBitmapReservationAttachment) int {
				return attachment.privatePages
			},
		},
		{
			name: "nonzero-tail", fixture: newSelectiveForeignNonzeroTailRefreshFixture,
			bound: func(*freeBitmapReservationAttachment) int {
				return nonzeroTailBoundLen
			},
		},
	} {
		for _, tree := range []struct {
			name string
			tree privatePageDeleteTree
		}{
			{"global", privatePageDeleteGlobal},
			{"scope", privatePageDeleteScope},
		} {
			for _, field := range []string{"index-tag", "index-link"} {
				t.Run(tail.name+"/"+tree.name+"/"+field, func(t *testing.T) {
					fixture := tail.fixture(t)
					attachment := &fixture.attachment
					boundLen := tail.bound(attachment)
					tailCount := attachment.privatePages - boundLen
					if (tail.name == "zero-tail") != (tailCount == 0) {
						t.Fatalf("%s fixture tail count=%d", tail.name, tailCount)
					}
					slotIndex := selectiveFinalizationRefreshPathAncestor(
						t, attachment, boundLen, tree.tree,
					)
					setSelectiveCheckpointCorruption(attachment.cow.pool, slotIndex, field, true)
					beforePool := *attachment.cow.pool
					beforePool.slots = nil
					beforeSlots := append([]privatePagePoolSlot(nil), attachment.cow.pool.slots...)
					scratch := finalizationScratchForAttachment(attachment)
					if _, problem := attachment.finalize(scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
						t.Fatalf("refresh-path checkpoint collision = %#v", problem)
					}
					afterPool := *attachment.cow.pool
					afterPool.slots = nil
					if !reflect.DeepEqual(afterPool, beforePool) ||
						!reflect.DeepEqual(attachment.cow.pool.slots, beforeSlots) ||
						!freeBitmapCleanupScratchCanonical(scratch.cleanup) {
						t.Fatal("refresh-path checkpoint rejection changed live pool or cleanup scratch")
					}
					setSelectiveCheckpointCorruption(attachment.cow.pool, slotIndex, field, false)
					if _, problem := attachment.finalize(
						finalizationScratchForAttachment(attachment),
					); problem.failed() {
						t.Fatalf("finalization after refresh-path repair = %#v", problem)
					}
				})
			}
		}
	}
}

type selectiveFinalizationRefreshInput struct {
	pathNode     int
	offPathChild int
	offPathSide  int
}

func selectiveFinalizationRefreshInputSlot(
	t *testing.T,
	attachment *freeBitmapReservationAttachment,
	boundLen int,
	tree privatePageDeleteTree,
	requiredOffPathSide int,
) selectiveFinalizationRefreshInput {
	t.Helper()
	tailCount := attachment.privatePages - boundLen
	scratch := finalizationScratchForAttachment(attachment).cleanup
	targets := make(map[int]struct{}, tailCount)
	for targetIndex := 0; targetIndex < tailCount; targetIndex++ {
		bindingIndex := attachment.privatePages - 1 - targetIndex
		slotIndex := attachment.cow.arenaBindings[bindingIndex].poolSlot
		scratch.targets[targetIndex] = slotIndex
		targets[slotIndex] = struct{}{}
	}
	plan, problem := preparePrivatePageDeletes(
		attachment.cow.pool, attachment.scope, scratch, tailCount, boundLen,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	overlay := privatePageDeleteOverlay{
		pool: attachment.cow.pool, scope: attachment.scope, scratch: plan.scratch,
		nodeLen: plan.nodeLen, indexRoot: plan.indexRoot, scopeRoot: plan.scopeRoot,
		work: plan.work, workLimit: plan.workLimit,
	}
	for bindingIndex := 0; bindingIndex < boundLen; bindingIndex++ {
		target := attachment.cow.arenaBindings[bindingIndex]
		reference := overlay.indexRoot
		if tree == privatePageDeleteScope {
			reference = overlay.scopeRoot
		}
		for depth := 0; depth < maximumPrivatePageAVLHeight(len(overlay.pool.slots)); depth++ {
			slotIndex, node, ok := overlay.resolve(tree, reference)
			if !ok || slotIndex == privatePagePoolNoIndex {
				break
			}
			slot := &overlay.pool.slots[slotIndex]
			state := privatePageDeleteResolvedState(overlay.pool, tree, slotIndex, node)
			next, offPath, offPathSide := state.left, state.right, 1
			switch {
			case target.pageNumber < slot.pageNumber:
				next, offPath, offPathSide = state.left, state.right, 1
			case target.pageNumber > slot.pageNumber:
				next, offPath, offPathSide = state.right, state.left, -1
			default:
				next = privatePagePoolNoIndex
			}
			offPathSlot, offPathNode, offPathOK := overlay.resolve(tree, offPath)
			_, pathDeleted := targets[slotIndex]
			_, offPathDeleted := targets[offPathSlot]
			if next != privatePagePoolNoIndex &&
				slotIndex != attachment.scope.anchor && !pathDeleted &&
				(node == nil || !node.dirty) &&
				offPathOK && offPathSlot != privatePagePoolNoIndex &&
				!offPathDeleted && (offPathNode == nil || !offPathNode.dirty) &&
				(requiredOffPathSide == 0 || requiredOffPathSide == offPathSide) {
				plan.scratch.clear()
				return selectiveFinalizationRefreshInput{
					pathNode: slotIndex, offPathChild: offPathSlot, offPathSide: offPathSide,
				}
			}
			reference = next
		}
	}
	plan.scratch.clear()
	t.Fatalf("fixture has no tree %d refresh input with off-path side %d",
		tree, requiredOffPathSide)
	return selectiveFinalizationRefreshInput{}
}

func selectiveTreeSummary(
	slot *privatePagePoolSlot,
	tree privatePageDeleteTree,
) (int8, uint64, uint64) {
	if tree == privatePageDeleteGlobal {
		return slot.indexHeight, slot.indexFree, slot.indexInUse
	}
	return slot.scopeHeight, slot.scopeFree, slot.scopeInUse
}

func setSelectiveTreeSummary(
	slot *privatePagePoolSlot,
	tree privatePageDeleteTree,
	height int8,
	free, inUse uint64,
) {
	if tree == privatePageDeleteGlobal {
		slot.indexHeight, slot.indexFree, slot.indexInUse = height, free, inUse
	} else {
		slot.scopeHeight, slot.scopeFree, slot.scopeInUse = height, free, inUse
	}
}

func selectiveTreeChildren(
	slot *privatePagePoolSlot,
	tree privatePageDeleteTree,
) (int, int) {
	if tree == privatePageDeleteGlobal {
		return slot.indexLeft, slot.indexRight
	}
	return slot.scopeLeft, slot.scopeRight
}

func TestSelectiveFinalizationApplyRejectsRefreshCacheInputsAtomically(t *testing.T) {
	nonzeroCalibration := newSelectiveForeignNonzeroTailRefreshFixture(t)
	nonzeroResult, problem := nonzeroCalibration.attachment.finalize(
		finalizationScratchForAttachment(&nonzeroCalibration.attachment),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	nonzeroBoundLen := nonzeroResult.output.boundLen
	for _, tail := range []struct {
		name    string
		fixture func(*testing.T) selectiveForeignFinalizationFixture
		bound   func(*freeBitmapReservationAttachment) int
	}{
		{
			name: "zero-tail", fixture: newSelectiveForeignZeroTailFinalizationFixture,
			bound: func(attachment *freeBitmapReservationAttachment) int {
				return attachment.privatePages
			},
		},
		{
			name: "nonzero-tail", fixture: newSelectiveForeignNonzeroTailRefreshFixture,
			bound: func(*freeBitmapReservationAttachment) int {
				return nonzeroBoundLen
			},
		},
	} {
		for _, tree := range []struct {
			name string
			tree privatePageDeleteTree
		}{
			{"global", privatePageDeleteGlobal},
			{"scope", privatePageDeleteScope},
		} {
			for _, corruption := range []struct {
				name  string
				side  int
				apply func(*privatePagePool, privatePageDeleteTree, selectiveFinalizationRefreshInput)
			}{
				{
					name: "path-state",
					apply: func(pool *privatePagePool, _ privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						pool.slots[input.pathNode].state = privatePageInUse
						pool.slots[input.pathNode].inUse = false
					},
				},
				{
					name: "path-height",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.pathNode]
						height, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height+1, free, inUse)
					},
				},
				{
					name: "path-free",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.pathNode]
						height, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height, free+1, inUse)
					},
				},
				{
					name: "path-in-use",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.pathNode]
						height, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height, free, inUse+1)
					},
				},
				{
					name: "off-path-height",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.offPathChild]
						_, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, 0, free, inUse)
					},
				},
				{
					name: "off-path-free",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.offPathChild]
						height, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height, free+1, inUse)
					},
				},
				{
					name: "off-path-in-use",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.offPathChild]
						height, free, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height, free, inUse+1)
					},
				},
				{
					name: "off-path-overflow",
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						slot := &pool.slots[input.offPathChild]
						height, _, inUse := selectiveTreeSummary(slot, tree)
						setSelectiveTreeSummary(slot, tree, height, ^uint64(0), inUse)
					},
				},
				{
					name: "left-heavy-balance", side: -1,
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						path := &pool.slots[input.pathNode]
						_, right := selectiveTreeChildren(path, tree)
						rightHeight := int8(0)
						if right != privatePagePoolNoIndex {
							rightHeight, _, _ = selectiveTreeSummary(&pool.slots[right], tree)
						}
						off := &pool.slots[input.offPathChild]
						_, free, inUse := selectiveTreeSummary(off, tree)
						setSelectiveTreeSummary(off, tree, rightHeight+2, free, inUse)
						_, pathFree, pathInUse := selectiveTreeSummary(path, tree)
						setSelectiveTreeSummary(path, tree, rightHeight+3, pathFree, pathInUse)
					},
				},
				{
					name: "right-heavy-balance", side: 1,
					apply: func(pool *privatePagePool, tree privatePageDeleteTree, input selectiveFinalizationRefreshInput) {
						path := &pool.slots[input.pathNode]
						left, _ := selectiveTreeChildren(path, tree)
						leftHeight := int8(0)
						if left != privatePagePoolNoIndex {
							leftHeight, _, _ = selectiveTreeSummary(&pool.slots[left], tree)
						}
						off := &pool.slots[input.offPathChild]
						_, free, inUse := selectiveTreeSummary(off, tree)
						setSelectiveTreeSummary(off, tree, leftHeight+2, free, inUse)
						_, pathFree, pathInUse := selectiveTreeSummary(path, tree)
						setSelectiveTreeSummary(path, tree, leftHeight+3, pathFree, pathInUse)
					},
				},
			} {
				t.Run(tail.name+"/"+tree.name+"/"+corruption.name, func(t *testing.T) {
					fixture := tail.fixture(t)
					attachment := &fixture.attachment
					boundLen := tail.bound(attachment)
					input := selectiveFinalizationRefreshInputSlot(
						t, attachment, boundLen, tree.tree, corruption.side,
					)
					beforePath := attachment.cow.pool.slots[input.pathNode]
					beforeOffPath := attachment.cow.pool.slots[input.offPathChild]
					corruption.apply(attachment.cow.pool, tree.tree, input)
					beforePool := *attachment.cow.pool
					beforePool.slots = nil
					beforeSlots := append([]privatePagePoolSlot(nil), attachment.cow.pool.slots...)
					scratch := finalizationScratchForAttachment(attachment)
					if _, problem := attachment.finalize(scratch); problem.code != freeBitmapCOWErrArenaPageConflict {
						t.Fatalf("refresh cache corruption finalization = %#v", problem)
					}
					afterPool := *attachment.cow.pool
					afterPool.slots = nil
					if !reflect.DeepEqual(afterPool, beforePool) ||
						!reflect.DeepEqual(attachment.cow.pool.slots, beforeSlots) ||
						!freeBitmapCleanupScratchCanonical(scratch.cleanup) {
						t.Fatal("refresh cache rejection changed live pool or cleanup scratch")
					}
					attachment.cow.pool.slots[input.pathNode] = beforePath
					attachment.cow.pool.slots[input.offPathChild] = beforeOffPath
					if _, problem := attachment.finalize(
						finalizationScratchForAttachment(attachment),
					); problem.failed() {
						t.Fatalf("finalization after refresh cache repair = %#v", problem)
					}
				})
			}
		}
	}
}

func TestSelectiveFinalizationPreparedRefreshCachesAreExact(t *testing.T) {
	for _, fixture := range []struct {
		name string
		new  func(*testing.T) selectiveForeignFinalizationFixture
	}{
		{"zero-tail", newSelectiveForeignZeroTailFinalizationFixture},
		{"nonzero-tail", newSelectiveForeignNonzeroTailRefreshFixture},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			state := fixture.new(t)
			result, problem := state.attachment.finalize(
				finalizationScratchForAttachment(&state.attachment),
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			requireValidPrivatePageDeleteTree(
				t, result.output.pool, result.output.scope,
				privatePageDeleteGlobal, result.output.pool.indexRoot,
			)
			requireValidPrivatePageDeleteTree(
				t, result.output.pool, result.output.scope,
				privatePageDeleteScope,
				result.output.pool.slots[result.output.scope.anchor].scopeRoot,
			)
		})
	}
}

func TestSelectiveFinalizationCleanupRejectsSuccessorRotationAndEvolvingPathCorruption(t *testing.T) {
	for _, kind := range []string{"successor", "rotation", "later-path"} {
		t.Run(kind, func(t *testing.T) {
			page := selectiveForeignCleanupDiagnosticPage(t, kind)
			fixture := finalizedSelectiveForeignCleanupFixture(t)
			result := fixture.result
			predecessor, problem := result.successor.consume()
			if problem.failed() {
				t.Fatal(problem)
			}
			slotIndex, found := result.output.pool.slotIndex(page)
			if !found || result.output.pool.slots[slotIndex].scopeID == result.output.scope.id {
				t.Fatalf("diagnostic page %d is not foreign", page)
			}
			result.output.pool.slots[slotIndex].indexLeft = len(result.output.pool.slots)
			before := snapshotSelectiveCleanupAtomic(result.output)
			if problem = predecessor.cleanup(); problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("%s corruption cleanup = %#v", kind, problem)
			}
			requireSelectiveCleanupAtomic(t, result.output, before)
		})
	}
}

type selectiveCleanupScaleFixture struct {
	pool      *privatePagePool
	scope     privatePageReservationScope
	scratch   freeBitmapCleanupScratch
	targets   int
	maxHeight int
}

func newSelectiveCleanupScaleFixture(
	t *testing.T,
	targetCount, foreignCount int,
) selectiveCleanupScaleFixture {
	t.Helper()
	if targetCount <= 0 || foreignCount <= 0 {
		t.Fatal("scaling fixture requires target and foreign pages")
	}
	total := targetCount + foreignCount
	pageCount := uint64(total + 2)
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]reservedBitmapPage, total), pageCount, pageCount, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	targetScope, problem := pool.reserveScope(targetCount)
	if problem.failed() {
		t.Fatal(problem)
	}
	foreignScope, problem := pool.reserveScope(foreignCount)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	targetSlots := make([]int, 0, targetCount)
	targetsBound := 0
	for ordinal := 0; ordinal < total; ordinal++ {
		scope := foreignScope
		if (ordinal+1)*targetCount/total > ordinal*targetCount/total {
			scope = targetScope
			targetsBound++
		}
		slot, bindProblem := pool.bindPage(
			checkpoint, scope, uint32(ordinal+2), privatePageReclaimed,
		)
		if bindProblem.failed() {
			t.Fatal(bindProblem)
		}
		if scope.id == targetScope.id {
			targetSlots = append(targetSlots, slot)
		}
	}
	if targetsBound != targetCount {
		t.Fatalf("bound %d target pages, want %d", targetsBound, targetCount)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	nodes, path, ok := privatePageDeleteScratchRequirements(total, targetCount, 0)
	if !ok {
		t.Fatal("invalid scaling scratch requirement")
	}
	scratch := freeBitmapCleanupScratch{
		nodes: make([]freeBitmapCleanupOverlayNode, nodes),
		path:  make([]int, path), targets: make([]int, targetCount),
	}
	for index := range targetSlots {
		scratch.targets[index] = targetSlots[len(targetSlots)-1-index]
	}
	return selectiveCleanupScaleFixture{
		pool: pool, scope: targetScope, scratch: scratch, targets: targetCount,
		maxHeight: maximumPrivatePageAVLHeight(total),
	}
}

func prepareSelectiveCleanupScale(
	t *testing.T,
	fixture selectiveCleanupScaleFixture,
) preparedPrivatePageDeletes {
	t.Helper()
	plan, problem := preparePrivatePageDeletes(
		fixture.pool, fixture.scope, fixture.scratch, fixture.targets, 0,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	maxWork, ok := privatePageDeleteWorkLimit(len(fixture.pool.slots), fixture.targets, 0)
	if !ok {
		t.Fatal("invalid cleanup work limit")
	}
	if plan.work == 0 || plan.work > maxWork ||
		plan.nodeLen <= 0 || plan.nodeLen > len(fixture.scratch.nodes) {
		t.Fatalf(
			"delete work=%d/%d nodes=%d/%d targets=%d height=%d",
			plan.work, maxWork, plan.nodeLen, len(fixture.scratch.nodes),
			fixture.targets, fixture.maxHeight,
		)
	}
	seen := make(map[[2]int]struct{}, plan.nodeLen)
	for nodeIndex := 0; nodeIndex < plan.nodeLen; nodeIndex++ {
		node := plan.scratch.nodes[nodeIndex]
		key := [2]int{int(node.tree), node.slot}
		if node.tree != privatePageDeleteGlobal && node.tree != privatePageDeleteScope {
			t.Fatalf("overlay node %d has invalid tree %d", nodeIndex, node.tree)
		}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("overlay copied tree %d slot %d more than once", node.tree, node.slot)
		}
		seen[key] = struct{}{}
	}
	return plan
}

func TestPrivatePageDeleteWorkLimitIncludesRetainedRefreshes(t *testing.T) {
	const (
		poolSlots    = 4096
		targets      = 8
		refreshPages = 13
	)
	height := uint64(maximumPrivatePageAVLHeight(poolSlots))
	for _, test := range []struct {
		name          string
		targets       int
		refreshPages  int
		expectedWork  uint64
		expectedValid bool
	}{
		{name: "none", expectedWork: 6, expectedValid: true},
		{
			name: "refresh-only", refreshPages: refreshPages,
			expectedWork: 6 + refreshPages*16*height, expectedValid: true,
		},
		{
			name: "delete-and-refresh", targets: targets, refreshPages: refreshPages,
			expectedWork:  6 + targets*192*height + refreshPages*16*height,
			expectedValid: true,
		},
		{name: "too-many-refresh-pages", refreshPages: poolSlots + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			work, valid := privatePageDeleteWorkLimit(
				poolSlots, test.targets, test.refreshPages,
			)
			if valid != test.expectedValid || work != test.expectedWork {
				t.Fatalf("work limit=%d,%v want %d,%v",
					work, valid, test.expectedWork, test.expectedValid)
			}
		})
	}
}

func TestPrivatePageDeleteScratchIncludesRetainedRefreshes(t *testing.T) {
	const poolSlots = 4096
	height := maximumPrivatePageAVLHeight(poolSlots)
	for _, test := range []struct {
		name          string
		targets       int
		refreshPages  int
		expectedNodes int
		expectedValid bool
	}{
		{name: "none", expectedValid: true},
		{
			name: "refresh-only", refreshPages: 13,
			expectedNodes: 13 * 2 * height, expectedValid: true,
		},
		{
			name: "delete-and-refresh", targets: 8, refreshPages: 13,
			expectedNodes: (8*6 + 13*2) * height, expectedValid: true,
		},
		{
			name: "two-tree-cap", targets: poolSlots, refreshPages: poolSlots,
			expectedNodes: poolSlots * 2, expectedValid: true,
		},
		{name: "too-many-refresh-pages", refreshPages: poolSlots + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			nodes, path, valid := privatePageDeleteScratchRequirements(
				poolSlots, test.targets, test.refreshPages,
			)
			expectedPath := 0
			if test.expectedValid && (test.targets != 0 || test.refreshPages != 0) {
				expectedPath = height
			}
			if valid != test.expectedValid || nodes != test.expectedNodes || path != expectedPath {
				t.Fatalf("scratch=%d,%d,%v want %d,%d,%v",
					nodes, path, valid, test.expectedNodes, expectedPath, test.expectedValid)
			}
		})
	}
}

func TestSelectiveFinalizationCleanupWorkIsIndependentOfForeignPopulation(t *testing.T) {
	const targets = 8
	small := prepareSelectiveCleanupScale(t, newSelectiveCleanupScaleFixture(t, targets, 512))
	large := prepareSelectiveCleanupScale(t, newSelectiveCleanupScaleFixture(t, targets, 4096))
	if large.work > small.work*2 {
		t.Fatalf(
			"fixed-target work grew with foreign population: 512=%d 4096=%d",
			small.work, large.work,
		)
	}
}

func TestSelectiveFinalizationCleanupWorkScalesTargetLogarithmically(t *testing.T) {
	small := prepareSelectiveCleanupScale(t, newSelectiveCleanupScaleFixture(t, 512, 512))
	large := prepareSelectiveCleanupScale(t, newSelectiveCleanupScaleFixture(t, 4096, 4096))
	if large.work > small.work*12 {
		t.Fatalf(
			"target cleanup exceeded O(k log N) growth: 512=%d 4096=%d",
			small.work, large.work,
		)
	}
}

func TestSelectiveFinalizationCleanupAdversarialSlotIdentitiesHaveConstantLookupWork(t *testing.T) {
	const poolSlots = 4096
	height := maximumPrivatePageAVLHeight(poolSlots)
	legacyNodes := 6 * height
	legacyBuckets := legacyNodes*2 + 1
	families := make([][]int, legacyBuckets)
	for slot := 0; slot < poolSlots; slot++ {
		bucket := int((uint64(uint(slot)+1) * 11400714819323198485) % uint64(legacyBuckets))
		families[bucket] = append(families[bucket], slot)
	}
	var colliding []int
	for _, family := range families {
		if len(family) > len(colliding) {
			colliding = family
		}
	}
	if len(colliding) < height {
		t.Fatalf("largest legacy collision family=%d, want at least AVL height %d", len(colliding), height)
	}
	colliding = colliding[:height]
	copies := len(colliding)
	pool := &privatePagePool{slots: make([]privatePagePoolSlot, poolSlots)}
	overlay := privatePageDeleteOverlay{
		pool: pool, scratch: freeBitmapCleanupScratch{
			nodes: make([]freeBitmapCleanupOverlayNode, copies*2),
		},
		workLimit: uint64(copies * 4),
	}
	for _, tree := range []privatePageDeleteTree{privatePageDeleteGlobal, privatePageDeleteScope} {
		for _, slot := range colliding {
			reference, node, ok := overlay.materialize(tree, slot)
			if !ok || reference >= -1 || node.slot != slot || node.tree != tree {
				t.Fatalf("materialize tree=%d slot=%d ref=%d node=%+v", tree, slot, reference, node)
			}
			again, repeated, ok := overlay.materialize(tree, reference)
			if !ok || again != reference || repeated != node {
				t.Fatalf("repeat tree=%d slot=%d ref=%d/%d", tree, slot, reference, again)
			}
		}
	}
	if overlay.nodeLen != copies*2 || overlay.work != uint64(copies*4) {
		t.Fatalf(
			"colliding identities used nodes=%d/%d work=%d/%d",
			overlay.nodeLen, copies*2, overlay.work, copies*4,
		)
	}
}

func TestSelectiveFinalizationRejectsCallbackStageIndexCorruption(t *testing.T) {
	calibrationSource := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	calibrationStorage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	calibration := newLateBitmapPlan(t, calibrationSource, 2, 2, &calibrationStorage)
	calibrationProof := completeLateBitmapProof(t, &calibration, 904, []uint32{7})
	if _, problem := calibration.bind(&calibrationProof); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem := calibration.finalize(finalizationScratchForAttachment(&calibration)); problem.failed() {
		t.Fatal(problem)
	}
	lastCallback := calibrationSource.checks
	for _, corrupt := range []struct {
		name string
		left func([]bitmapCOWIndexNode) int
	}{
		{name: "cycle", left: func([]bitmapCOWIndexNode) int { return 0 }},
		{name: "out-of-bounds", left: func(nodes []bitmapCOWIndexNode) int { return len(nodes) + 1 }},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			source := &lateFailingAccessSource{
				base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
			}
			storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
			attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
			proof := completeLateBitmapProof(t, &attachment, 904, []uint32{7})
			if _, problem := attachment.bind(&proof); problem.failed() {
				t.Fatal(problem)
			}
			before := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			armed := true
			source.hook = func(int) {
				if armed && source.checks == lastCallback {
					storage.stageIndex[0].left = corrupt.left(storage.stageIndex)
					armed = false
				}
			}
			scratch := finalizationScratchForAttachment(&attachment)
			if _, problem := attachment.finalize(scratch); problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("corrupt callback = %#v", problem)
			}
			if after := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope); after != before {
				t.Fatal("callback-corruption rejection mutated live scope")
			}
			source.hook = nil
			if _, problem := attachment.finalize(scratch); problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationCacheSealsACommittedPageForCallbackFreeReplay(t *testing.T) {
	// Step 3 binds an exact low-page prefix. A reclaimed page in a later,
	// unverified subtree therefore cannot also belong to this scope today.
	// The general cache remains the defensive contract for future producers.
	base := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 4, 1, 1001)}}
	pages := make([]freeBitmapFinalizationCachedPage, 4)
	stack := make([]int, 4)
	cache := freeBitmapFinalizationCachedSource{
		base: base, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 123,
	}
	var first, replay [PageSize]byte
	if status := cache.readPageStatus(4, &first); status.failed() || base.pageReads(4) != 1 || cache.length != 1 {
		t.Fatalf("discovery read = %#v reads=%d length=%d", status, base.pageReads(4), cache.length)
	}
	if problem := cache.validate(); problem.failed() {
		t.Fatalf("cache validation = %#v", problem)
	}
	cache.sealed = true
	if status := cache.readPageStatus(4, &replay); status.failed() || replay != first || base.pageReads(4) != 1 {
		t.Fatalf("sealed replay = %#v reads=%d", status, base.pageReads(4))
	}
}

func TestSelectiveFinalizationRejectsLastCallbackLiveIndexCorruption(t *testing.T) {
	calibrationSource := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	calibrationStorage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	calibration := newLateBitmapPlan(t, calibrationSource, 2, 2, &calibrationStorage)
	calibrationProof := completeLateBitmapProof(t, &calibration, 906, []uint32{7})
	if _, problem := calibration.bind(&calibrationProof); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem := calibration.finalize(finalizationScratchForAttachment(&calibration)); problem.failed() {
		t.Fatal(problem)
	}
	lastCallback := calibrationSource.checks

	for _, corrupt := range []struct {
		name   string
		mutate func(*freeBitmapCOW)
	}{
		{name: "root", mutate: func(cow *freeBitmapCOW) { cow.indexRoot = len(cow.indexNodes) + 1 }},
		{name: "child-cycle", mutate: func(cow *freeBitmapCOW) {
			cow.indexNodes[cow.indexRoot].left = cow.indexRoot
		}},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			source := &lateFailingAccessSource{
				base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
			}
			storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
			attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
			proof := completeLateBitmapProof(t, &attachment, 907, []uint32{7})
			if _, problem := attachment.bind(&proof); problem.failed() {
				t.Fatal(problem)
			}
			originalRoot := attachment.cow.indexRoot
			originalNodes := append([]bitmapCOWIndexNode(nil), attachment.cow.indexNodes...)
			mutatedFingerprint := uint64(0)
			source.hook = func(int) {
				if source.checks == lastCallback {
					corrupt.mutate(&attachment.cow)
					mutatedFingerprint = freeBitmapReservationCOWFingerprint(&attachment.cow)
				}
			}
			if _, problem := attachment.finalize(finalizationScratchForAttachment(&attachment)); problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("live callback corruption = %#v", problem)
			}
			if mutatedFingerprint == 0 || freeBitmapReservationCOWFingerprint(&attachment.cow) != mutatedFingerprint {
				t.Fatal("finalizer mutated live COW after hostile callback")
			}
			attachment.cow.indexRoot = originalRoot
			copy(attachment.cow.indexNodes, originalNodes)
			source.hook = nil
			if _, problem := attachment.finalize(finalizationScratchForAttachment(&attachment)); problem.failed() {
				t.Fatalf("retry = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationLastCallbackCorruptionPrecedesSourceFailure(t *testing.T) {
	calibrationSource := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	calibrationStorage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	calibration := newLateBitmapPlan(t, calibrationSource, 2, 2, &calibrationStorage)
	calibrationProof := completeLateBitmapProof(t, &calibration, 908, []uint32{7})
	if _, problem := calibration.bind(&calibrationProof); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem := calibration.finalize(finalizationScratchForAttachment(&calibration)); problem.failed() {
		t.Fatal(problem)
	}
	lastCallback := calibrationSource.checks

	for _, kind := range []string{"clean", "live-pool", "live-cow", "stage", "cache-control"} {
		t.Run(kind, func(t *testing.T) {
			source := &lateFailingAccessSource{
				base:   cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
				failAt: lastCallback,
			}
			storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
			attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
			proof := completeLateBitmapProof(t, &attachment, 909, []uint32{7})
			if _, problem := attachment.bind(&proof); problem.failed() {
				t.Fatal(problem)
			}
			scratch := finalizationScratchForAttachment(&attachment)
			source.hook = func(callback int) {
				if callback != lastCallback {
					return
				}
				switch kind {
				case "live-pool":
					attachment.cow.pool.mutationEpoch++
				case "live-cow":
					attachment.cow.root++
				case "stage":
					storage.stageCOW.mutationEpoch++
				case "cache-control":
					scratch.cache.failure.scopeFingerprint++
				}
			}
			_, problem := attachment.finalize(scratch)
			if kind == "clean" {
				if problem.code != freeBitmapCOWErrSource || problem.source.code != pageSourceErrForkedHandle {
					t.Fatalf("clean source failure = %#v", problem)
				}
				source.failAt = 0
				source.hook = nil
				if _, problem = attachment.finalize(scratch); problem.failed() {
					t.Fatalf("clean retry = %#v", problem)
				}
				return
			}
			if problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("%s corruption lost to source error: %#v", kind, problem)
			}
		})
	}
}

func TestSelectiveFinalizationCacheRejectsCallbackMetadataCorruption(t *testing.T) {
	for _, corrupt := range []struct {
		name   string
		mutate func(*freeBitmapFinalizationCachedPage)
	}{
		{name: "page-number", mutate: func(page *freeBitmapFinalizationCachedPage) { page.pageNumber++ }},
		{name: "content", mutate: func(page *freeBitmapFinalizationCachedPage) { page.bytes[PageSize-1]++ }},
		{name: "content-seal", mutate: func(page *freeBitmapFinalizationCachedPage) { page.contentSeal++ }},
		{name: "metadata-seal", mutate: func(page *freeBitmapFinalizationCachedPage) { page.metadataSeal++ }},
		{name: "child-cycle", mutate: func(page *freeBitmapFinalizationCachedPage) { page.left = 0 }},
		{name: "child-out-of-bounds", mutate: func(page *freeBitmapFinalizationCachedPage) { page.right = 99 }},
		{name: "height", mutate: func(page *freeBitmapFinalizationCachedPage) { page.height++ }},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			pages := make([]freeBitmapFinalizationCachedPage, 4)
			stack := make([]int, 4)
			var cache freeBitmapFinalizationCachedSource
			armed := false
			source := &lateFailingAccessSource{
				base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 4, 1, 1001), cowLeaf(t, 5, 1, 1002)}},
				readHook: func() {
					if armed {
						corrupt.mutate(&pages[0])
						armed = false
					}
				},
			}
			cache = freeBitmapFinalizationCachedSource{
				base: source, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 124,
			}
			var page [PageSize]byte
			if status := cache.readPageStatus(4, &page); status.failed() {
				t.Fatal(status)
			}
			armed = true
			status := cache.readPageStatus(5, &page)
			validation := cache.validate()
			if !status.failed() && validation.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("cache corruption escaped read and fence: status=%#v validation=%#v", status, validation)
			}
			if status.failed() && (status.code != pageSourceErrForkedHandle ||
				cache.problem.code != freeBitmapCOWErrStaleInsertionPlan) {
				t.Fatalf("cache corruption = %#v problem=%#v", status, cache.problem)
			}
			clear(pages)
			cache.length, cache.root, cache.problem = 0, bitmapCOWNoIndex, freeBitmapCOWError{}
			source.readHook = nil
			if status := cache.readPageStatus(4, &page); status.failed() {
				t.Fatalf("retry first read = %#v", status)
			}
			if status := cache.readPageStatus(5, &page); status.failed() {
				t.Fatalf("retry second read = %#v", status)
			}
			if problem := cache.validate(); problem.failed() {
				t.Fatalf("retry validation = %#v", problem)
			}
		})
	}
}

func TestSelectiveFinalizationCacheRejectsCallbackControlCorruption(t *testing.T) {
	for _, corrupt := range []struct {
		name   string
		mutate func(*freeBitmapFinalizationCachedSource)
	}{
		{name: "base", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.base = nil }},
		{name: "cow", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.cow = &freeBitmapCOW{} }},
		{name: "pages", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.pages = cache.pages[:3] }},
		{name: "length", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.length++ }},
		{name: "root", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.root = 0 }},
		{name: "stack", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.stack = cache.stack[:3] }},
		{name: "seal-key", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.sealKey++ }},
		{name: "sealed", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.sealed = true }},
		{name: "problem", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.problem = staleFreeBitmapReservationBind() }},
		{name: "visits", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.nodeVisits++ }},
		{name: "failure-fence", mutate: func(cache *freeBitmapFinalizationCachedSource) { cache.failure.armed = true }},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			pages := make([]freeBitmapFinalizationCachedPage, 4)
			stack := make([]int, 4)
			var cache freeBitmapFinalizationCachedSource
			armed := true
			source := &lateFailingAccessSource{
				base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 4, 1, 1001)}},
				readHook: func() {
					if armed {
						corrupt.mutate(&cache)
						armed = false
					}
				},
			}
			cache = freeBitmapFinalizationCachedSource{
				base: source, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 125,
			}
			var page [PageSize]byte
			if status := cache.readPageStatus(4, &page); status.code != pageSourceErrForkedHandle ||
				cache.problem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("cache control corruption = %#v problem=%#v", status, cache.problem)
			}

			clear(pages)
			source.readHook = nil
			cache = freeBitmapFinalizationCachedSource{
				base: source, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 125,
			}
			if status := cache.readPageStatus(4, &page); status.failed() {
				t.Fatalf("retry = %#v", status)
			}
		})
	}
}

type finalizationInjectedFailureSource struct {
	page   [PageSize]byte
	reads  int
	failAt int
	hook   func()
}

func (source *finalizationInjectedFailureSource) checkAccessStatus() pageSourceStatus {
	return pageSourceStatus{}
}

func (source *finalizationInjectedFailureSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	source.reads++
	if source.hook != nil {
		source.hook()
	}
	if source.reads == source.failAt {
		destination[0] = 0xa5
		return pageSourceStatus{code: pageSourceErrShortRead, page: pageNumber, expected: PageSize, actual: 1}
	}
	*destination = source.page
	return pageSourceStatus{}
}

func TestSelectiveFinalizationReadFailureRunsCompleteCorruptionFence(t *testing.T) {
	for _, kind := range []string{"clean", "live", "stage", "cache-content", "cache-metadata", "cache-control"} {
		t.Run(kind, func(t *testing.T) {
			attachment := newFinalizationScratchFixture(t)
			liveSeal, problem := captureFreeBitmapFinalizationLiveSeal(&attachment)
			if problem.failed() {
				t.Fatal(problem)
			}
			source := &finalizationInjectedFailureSource{failAt: 1}
			pages := make([]freeBitmapFinalizationCachedPage, 4)
			cache := freeBitmapFinalizationCachedSource{
				base: source, cow: &attachment.cow, pages: pages,
				root: bitmapCOWNoIndex, stack: make([]int, len(attachment.cow.indexNodes)), sealKey: 0xf411,
			}
			discovery, problem := attachment.buildFinalizationShadow(&cache)
			if problem.failed() {
				t.Fatal(problem)
			}
			cache.armFailureFence(discovery)
			if kind == "cache-content" || kind == "cache-metadata" {
				source.failAt = 2
				var first [PageSize]byte
				if status := cache.readPageStatus(100, &first); status.failed() {
					t.Fatal(status)
				}
			}
			source.hook = func() {
				switch kind {
				case "live":
					attachment.cow.root++
				case "stage":
					discovery.mutationEpoch++
				case "cache-content":
					cache.pages[0].bytes[PageSize-1]++
				case "cache-metadata":
					cache.pages[0].metadataSeal++
				case "cache-control":
					cache.failure.scopeFingerprint++
				}
				source.hook = nil
			}
			var destination [PageSize]byte
			status := cache.readPageStatus(101, &destination)
			if !status.failed() {
				t.Fatal("injected source failure succeeded")
			}
			fenceProblem := validateFreeBitmapFinalizationSourceFailure(&attachment, liveSeal, &cache)
			if kind == "clean" {
				if fenceProblem.failed() || status.code != pageSourceErrShortRead {
					t.Fatalf("clean failure = status %#v fence %#v", status, fenceProblem)
				}
				source.failAt = 0
				if status = cache.readPageStatus(101, &destination); status.failed() {
					t.Fatalf("clean retry = %#v", status)
				}
				return
			}
			if fenceProblem.code != freeBitmapCOWErrStaleInsertionPlan {
				t.Fatalf("corruption lost to source error: status %#v fence %#v", status, fenceProblem)
			}
		})
	}
}

func TestSelectiveFinalizationCacheRejectsFinalCallbackControlCorruption(t *testing.T) {
	pages := make([]freeBitmapFinalizationCachedPage, 2)
	stack := make([]int, 2)
	var cache freeBitmapFinalizationCachedSource
	armed := false
	source := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 4, 1, 1001)}},
		hook: func(int) {
			if armed {
				cache.length++
				armed = false
			}
		},
	}
	cache = freeBitmapFinalizationCachedSource{
		base: source, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 126,
	}
	var page [PageSize]byte
	if status := cache.readPageStatus(4, &page); status.failed() {
		t.Fatal(status)
	}
	armed = true
	if status := cache.checkAccessStatus(); status.code != pageSourceErrForkedHandle ||
		cache.problem.code != freeBitmapCOWErrStaleInsertionPlan {
		t.Fatalf("final callback control corruption = %#v problem=%#v", status, cache.problem)
	}

	clear(pages)
	source.hook = nil
	cache = freeBitmapFinalizationCachedSource{
		base: source, pages: pages, stack: stack, root: bitmapCOWNoIndex, sealKey: 126,
	}
	if status := cache.readPageStatus(4, &page); status.failed() {
		t.Fatalf("retry read = %#v", status)
	}
	if status := cache.checkAccessStatus(); status.failed() {
		t.Fatalf("retry final callback = %#v", status)
	}
}

type finalizationPartialFailureSource struct {
	page  [PageSize]byte
	fail  bool
	reads int
}

func (source *finalizationPartialFailureSource) checkAccessStatus() pageSourceStatus {
	return pageSourceStatus{}
}

func (source *finalizationPartialFailureSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	source.reads++
	if source.fail {
		destination[0], destination[PageSize-1] = 0xa5, 0x5a
		return pageSourceStatus{code: pageSourceErrShortRead, page: pageNumber, expected: PageSize, actual: 2}
	}
	*destination = source.page
	return pageSourceStatus{}
}

func TestSelectiveFinalizationCacheSourceFailureIsRetryable(t *testing.T) {
	expected := cowLeaf(t, 4, 1, 1001).bytes
	source := &finalizationPartialFailureSource{page: expected, fail: true}
	pages := make([]freeBitmapFinalizationCachedPage, 1)
	cache := freeBitmapFinalizationCachedSource{
		base: source, pages: pages, stack: make([]int, 1), root: bitmapCOWNoIndex, sealKey: 127,
	}
	var page [PageSize]byte
	if status := cache.readPageStatus(4, &page); status.code != pageSourceErrShortRead ||
		cache.length != 0 || cache.problem.failed() {
		t.Fatalf("partial source failure = %#v length=%d problem=%#v", status, cache.length, cache.problem)
	}
	if problem := cache.validate(); problem.failed() {
		t.Fatalf("failed read entered cache = %#v", problem)
	}
	source.fail = false
	if status := cache.readPageStatus(4, &page); status.failed() || page != expected ||
		cache.length != 1 || source.reads != 2 {
		t.Fatalf("retry = %#v length=%d reads=%d", status, cache.length, source.reads)
	}
	if problem := cache.validate(); problem.failed() {
		t.Fatalf("retry validation = %#v", problem)
	}
}

func newAppendOnlyFinalizationFixture(
	t *testing.T,
	capacity int,
) (freeBitmapReservationAttachment, freeBitmapFinalizationScratch) {
	t.Helper()
	storage := newLateBitmapPlannerStorage(capacity, 1, 1, 2)
	attachment := newLateBitmapPlanAt(t, &cowSparsePages{}, 2, 0, capacity, &storage)
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	return attachment, freeBitmapFinalizationScratch{
		releasePages: make([]uint32, capacity),
		insertPages:  make([]freeBitmapInsertPage, freeBitmapPathCapacity),
		cachedPages:  make([]freeBitmapFinalizationCachedPage, 1),
		indexStack:   make([]int, len(attachment.cow.indexNodes)),
		cache:        &freeBitmapFinalizationCachedSource{},
		cleanup:      cleanupScratchForAttachment(&attachment),
	}
}

func newAppendOnlyForeignFinalizationFixture(
	t *testing.T,
	payload int,
) (freeBitmapReservationAttachment, int) {
	t.Helper()
	const committedPages = 64
	storage := newLateBitmapPlannerStorage(payload+4, 4, 4, 8)
	capacity := newLateBitmapCapacityPlanAt(
		t, &cowSparsePages{}, committedPages, 0, payload, &storage,
	)
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]reservedBitmapPage, capacity.privatePages+1),
		committedPages, committedPages, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	foreignScope, poolProblem := pool.reserveScope(1)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bitmapScope, poolProblem := pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, problem := capacity.attach(pool, bitmapScope)
	if problem.failed() {
		t.Fatal(problem)
	}
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem = attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	foreignSlot := bindForeignLateBitmapPage(
		t, pool, foreignScope, 3, privatePageReclaimed, false,
	)
	return attachment, foreignSlot
}

func TestSelectiveFinalizationTargetScopeScalingAndAllocations(t *testing.T) {
	sizes := []int{512, 4096}
	work := make([]uint64, len(sizes))
	for sizeIndex, size := range sizes {
		attachment, scratch := newAppendOnlyFinalizationFixture(t, size)
		visitsBefore := attachment.cow.scopedMemberVisits
		result, problem := attachment.finalize(scratch)
		if problem.failed() || result.released.truncatedAppended != size || result.output.pageCount != 2 {
			t.Fatalf("size %d finalization = %#v released=%+v pages=%d", size, problem, result.released, result.output.pageCount)
		}
		work[sizeIndex] = attachment.cow.scopedMemberVisits - visitsBefore +
			uint64(attachment.terminalWork.scopeSlotVisits+attachment.terminalWork.indexSlotVisits+attachment.terminalWork.scopeHeaderVisits)
		if work[sizeIndex] == 0 || work[sizeIndex] > uint64(size*64) {
			t.Fatalf("size %d target work = %d", size, work[sizeIndex])
		}

		attachments := make([]freeBitmapReservationAttachment, 2)
		scratches := make([]freeBitmapFinalizationScratch, 2)
		for index := range attachments {
			attachments[index], scratches[index] = newAppendOnlyFinalizationFixture(t, size)
		}
		call := 0
		var allocationProblem freeBitmapCOWError
		allocations := testing.AllocsPerRun(1, func() {
			_, allocationProblem = attachments[call].finalize(scratches[call])
			call++
		})
		if allocationProblem.failed() || allocations != 0 {
			t.Fatalf("size %d allocations=%v problem=%#v", size, allocations, allocationProblem)
		}
	}
	if work[1] > work[0]*9 {
		t.Fatalf("target work scales too quickly: %d -> %d", work[0], work[1])
	}
}

type finalizationCacheScaleSource struct {
	pages  [][PageSize]byte
	reads  uint64
	checks uint64
}

func (source *finalizationCacheScaleSource) checkAccessStatus() pageSourceStatus {
	source.checks++
	return pageSourceStatus{}
}

func (source *finalizationCacheScaleSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	if pageNumber < 2 || uint64(pageNumber-2) >= uint64(len(source.pages)) {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	source.reads++
	*destination = source.pages[pageNumber-2]
	return pageSourceStatus{}
}

type finalizationCacheScaleFixture struct {
	source      finalizationCacheScaleSource
	cache       freeBitmapFinalizationCachedSource
	destination [PageSize]byte
}

func newFinalizationCacheScaleFixture(pageCount int) *finalizationCacheScaleFixture {
	fixture := &finalizationCacheScaleFixture{}
	fixture.source.pages = make([][PageSize]byte, pageCount)
	for index := range fixture.source.pages {
		binary.LittleEndian.PutUint32(fixture.source.pages[index][:4], uint32(index+2))
	}
	fixture.cache = freeBitmapFinalizationCachedSource{
		base: &fixture.source, pages: make([]freeBitmapFinalizationCachedPage, pageCount),
		root: bitmapCOWNoIndex, stack: make([]int, 64), sealKey: 0x5ca1e,
	}
	return fixture
}

// exerciseFinalizationCacheScale follows the production finalization fence:
// discovery, complete cache validation, genuinely last source callback,
// content fence, seal, and callback-free replay.
func exerciseFinalizationCacheScale(
	fixture *finalizationCacheScaleFixture,
) (uint64, freeBitmapCOWError, pageSourceStatus, bool) {
	pageCount := len(fixture.source.pages)
	readsBefore, checksBefore := fixture.source.reads, fixture.source.checks
	visitsBefore := fixture.cache.nodeVisits
	for index := 0; index < pageCount; index++ {
		if status := fixture.cache.readPageStatus(uint32(index+2), &fixture.destination); status.failed() {
			return 0, fixture.cache.problem, status, false
		}
	}
	if problem := fixture.cache.validate(); problem.failed() {
		return 0, problem, pageSourceStatus{}, false
	}
	fingerprint := finalizationCacheFingerprint(fixture.cache.pages[:fixture.cache.length])
	if status := fixture.cache.checkAccessStatus(); status.failed() {
		return 0, fixture.cache.problem, status, false
	}
	if finalizationCacheFingerprint(fixture.cache.pages[:fixture.cache.length]) != fingerprint {
		return 0, staleFreeBitmapReservationBind(), pageSourceStatus{}, false
	}
	fixture.cache.sealed = true
	for index := 0; index < pageCount; index++ {
		if status := fixture.cache.readPageStatus(uint32(index+2), &fixture.destination); status.failed() {
			return 0, fixture.cache.problem, status, false
		}
	}
	valid := fixture.source.reads-readsBefore == uint64(pageCount) &&
		fixture.source.checks-checksBefore == 1 && fixture.cache.length == pageCount
	// Each page is content-sealed on insertion, validation, both sides of the
	// final callback, and replay. Count those bytes plus actual AVL node visits.
	work := fixture.cache.nodeVisits - visitsBefore + uint64(pageCount)*PageSize*5
	return work, freeBitmapCOWError{}, pageSourceStatus{}, valid
}

func TestSelectiveFinalizationCacheScalingAndAllocations(t *testing.T) {
	sizes := []int{512, 4096}
	work := make([]uint64, len(sizes))
	for sizeIndex, size := range sizes {
		fixture := newFinalizationCacheScaleFixture(size)
		var problem freeBitmapCOWError
		var status pageSourceStatus
		var valid bool
		work[sizeIndex], problem, status, valid = exerciseFinalizationCacheScale(fixture)
		if problem.failed() || status.failed() || !valid {
			t.Fatalf("size %d cache fence = problem %#v status %#v valid %t", size, problem, status, valid)
		}

		fixtures := []*finalizationCacheScaleFixture{
			newFinalizationCacheScaleFixture(size), newFinalizationCacheScaleFixture(size),
		}
		call := 0
		allocations := testing.AllocsPerRun(1, func() {
			_, problem, status, valid = exerciseFinalizationCacheScale(fixtures[call])
			call++
		})
		if allocations != 0 || problem.failed() || status.failed() || !valid {
			t.Fatalf("size %d allocations=%g problem=%#v status=%#v valid=%t", size, allocations, problem, status, valid)
		}
	}
	if work[1] > work[0]*9 {
		t.Fatalf("4096-page cache work %d exceeded 9x 512-page work %d", work[1], work[0])
	}
}

func assertCheckpointJournalsCanonical(t *testing.T, pool *privatePagePool) {
	t.Helper()
	if pool.activeCheckpointID != 0 || pool.checkpointCleanup != 0 ||
		pool.checkpointSlotHead != privatePagePoolNoIndex || pool.checkpointSlotCount != 0 ||
		pool.checkpointIndexHead != privatePagePoolNoIndex || pool.checkpointIndexCount != 0 ||
		pool.checkpointScopeHead != privatePagePoolNoIndex || pool.checkpointScopeCount != 0 {
		t.Fatalf("checkpoint headers are not canonical: %+v", *pool)
	}
	for index := range pool.slots {
		slot := &pool.slots[index]
		if slot.checkpointID != 0 || slot.checkpointSlotNext != privatePagePoolNoIndex ||
			slot.indexCheckpointID != 0 || slot.indexCheckpointNext != privatePagePoolNoIndex ||
			slot.scopeCheckpointID != 0 || slot.scopeCheckpointNext != privatePagePoolNoIndex {
			t.Fatalf("slot %d retained checkpoint journal state", index)
		}
	}
}

func clearPrivatePageCheckpointScratch(slot *privatePagePoolSlot) {
	slot.checkpointID = 0
	slot.checkpointSlotNext = 0
	slot.checkpointBound = false
	slot.checkpointPageNumber = 0
	slot.checkpointAuthorization = 0
	slot.checkpointScopeID = 0
	slot.checkpointScopeAnchor = false
	slot.checkpointScopeAnchorIndex = 0
	slot.checkpointScopeVacantNext = 0
	slot.checkpointState = 0
	slot.checkpointOwner = 0
	slot.checkpointOrigin = 0
	slot.checkpointPendingTxn = 0
	slot.checkpointGeneration = 0
	slot.checkpointCommittedOrigin = 0
	slot.checkpointInUse = false
	slot.indexCheckpointID = 0
	slot.indexCheckpointNext = 0
	slot.checkpointIndexLeft = 0
	slot.checkpointIndexRight = 0
	slot.checkpointIndexHeight = 0
	slot.checkpointIndexFree = 0
	slot.checkpointIndexInUse = 0
	slot.checkpointScopeLeft = 0
	slot.checkpointScopeRight = 0
	slot.checkpointScopeHeight = 0
	slot.checkpointScopeFree = 0
	slot.checkpointScopeInUse = 0
	slot.scopeCheckpointID = 0
	slot.scopeCheckpointNext = 0
	slot.checkpointScopeRoot = 0
	slot.checkpointScopeVacantHead = 0
	slot.checkpointScopeBound = 0
}

func newVacantCheckpointPool(t *testing.T, capacity int) *privatePagePool {
	t.Helper()
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(
		pool, make([]privatePagePoolSlot, capacity), 20, 20, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	return pool
}

func TestPrivatePagePoolScopedUnbindJournalCommitRollbackAndReuse(t *testing.T) {
	pool := newVacantCheckpointPool(t, 3)
	scope, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 20, privatePageAppended); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commitCheckpointInScopePrepared(checkpoint, scope); problem.failed() {
		t.Fatal(problem)
	}
	assertCheckpointJournalsCanonical(t, pool)

	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scope, 20); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.rollbackCheckpointInScope(checkpoint, scope); problem.failed() {
		t.Fatal(problem)
	}
	anchor, problem := pool.validateScope(scope)
	if problem.failed() || anchor.scopeBound != 1 || anchor.scopeRoot == privatePagePoolNoIndex ||
		pool.pendingPageCount != 21 {
		t.Fatalf("rollback did not restore scoped tail: anchor=%+v problem=%+v tail=%d", anchor, problem, pool.pendingPageCount)
	}
	if index, found := pool.slotIndex(20); !found || !pool.slots[index].bound {
		t.Fatal("rollback did not restore the unbound page")
	}
	assertCheckpointJournalsCanonical(t, pool)

	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scope, 20); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commitCheckpointInScopePrepared(checkpoint, scope); problem.failed() {
		t.Fatal(problem)
	}
	anchor, problem = pool.validateScope(scope)
	if problem.failed() || anchor.scopeBound != 0 || anchor.scopeRoot != privatePagePoolNoIndex ||
		pool.pendingPageCount != 20 {
		t.Fatalf("commit did not retain scoped tail removal: anchor=%+v problem=%+v tail=%d", anchor, problem, pool.pendingPageCount)
	}
	assertCheckpointJournalsCanonical(t, pool)
	if problem = pool.closeScope(scope); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.reserveScope(3); problem.failed() {
		t.Fatalf("cleaned capacity was not reusable: %+v", problem)
	}
}

func TestPrivatePagePoolGlobalCheckpointJournalSupportsTwoScopes(t *testing.T) {
	pool := newVacantCheckpointPool(t, 6)
	scopeA, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	scopeB, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scopeA, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scopeB, 8, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	assertCheckpointJournalsCanonical(t, pool)

	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scopeA, 7); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scopeB, 8); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if _, found := pool.slotIndex(7); !found {
		t.Fatal("global rollback lost scope A")
	}
	if _, found := pool.slotIndex(8); !found {
		t.Fatal("global rollback lost scope B")
	}
	assertCheckpointJournalsCanonical(t, pool)

	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scopeA, 7); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.unbindPage(checkpoint, scopeB, 8); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if _, found := pool.slotIndex(7); found {
		t.Fatal("global commit restored scope A")
	}
	if _, found := pool.slotIndex(8); found {
		t.Fatal("global commit restored scope B")
	}
	assertCheckpointJournalsCanonical(t, pool)
	if problem = pool.closeScope(scopeA); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.closeScope(scopeB); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.reserveScope(6); problem.failed() {
		t.Fatalf("two-scope capacity was not reusable: %+v", problem)
	}
}

func TestPrivatePagePoolScopedRollbackRestoresForeignGlobalIndexAncestors(t *testing.T) {
	pool := newVacantCheckpointPool(t, 6)
	scopeA, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	scopeB, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scopeA, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scopeB, 8, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	foreignBefore := pool.slots[scopeA.anchor]
	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.claimPageInScope(
		checkpoint, scopeB, 8, privatePageOwnerBitmap, privatePageBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	foundForeignAncestor := false
	for index, visited := pool.checkpointIndexHead, 0; index != privatePagePoolNoIndex; visited++ {
		if visited >= pool.checkpointIndexCount {
			t.Fatal("index checkpoint journal cycle")
		}
		if pool.slots[index].scopeID == scopeA.id {
			foundForeignAncestor = true
		}
		index = pool.slots[index].indexCheckpointNext
	}
	if !foundForeignAncestor {
		t.Fatal("fixture did not exercise a foreign global-index ancestor")
	}
	if problem = pool.rollbackCheckpointInScope(checkpoint, scopeB); problem.failed() {
		t.Fatal(problem)
	}
	foreignAfter := pool.slots[scopeA.anchor]
	clearPrivatePageCheckpointScratch(&foreignBefore)
	clearPrivatePageCheckpointScratch(&foreignAfter)
	if foreignAfter != foreignBefore {
		t.Fatal("scoped rollback changed foreign semantic state")
	}
	if index, found := pool.slotIndex(8); !found || pool.slots[index].state != privatePageAvailable {
		t.Fatal("scoped rollback did not restore target state")
	}
	assertCheckpointJournalsCanonical(t, pool)
}

func TestPrivatePagePoolScopedCheckpointJournalCorruptionFailsBeforeMutation(t *testing.T) {
	for _, kind := range []string{
		"head", "count", "next", "tag", "foreign-slot", "index-head", "scope-tag", "foreign-scope-header",
	} {
		t.Run(kind, func(t *testing.T) {
			pool := newVacantCheckpointPool(t, 6)
			scope, problem := pool.reserveScope(3)
			if problem.failed() {
				t.Fatal(problem)
			}
			foreign, problem := pool.reserveScope(3)
			if problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, problem := pool.begin()
			if problem.failed() {
				t.Fatal(problem)
			}
			bound, problem := pool.bindPage(checkpoint, scope, 7, privatePageReclaimed)
			if problem.failed() {
				t.Fatal(problem)
			}
			if problem = pool.commitCheckpointInScopePrepared(checkpoint, scope); problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, problem = pool.begin()
			if problem.failed() {
				t.Fatal(problem)
			}
			if problem = pool.unbindPage(checkpoint, scope, 7); problem.failed() {
				t.Fatal(problem)
			}
			switch kind {
			case "head":
				pool.checkpointSlotHead = len(pool.slots)
			case "count":
				pool.checkpointSlotCount++
			case "next":
				pool.slots[bound].checkpointSlotNext = bound
			case "tag":
				pool.slots[bound].checkpointID++
			case "foreign-slot":
				foreignSlot := foreign.anchor
				pool.slots[bound].checkpointSlotNext = foreignSlot
				pool.slots[foreignSlot].checkpointID = checkpoint.id
				pool.slots[foreignSlot].checkpointSlotNext = privatePagePoolNoIndex
				pool.checkpointSlotCount++
				pool.checkpointCleanup++
			case "index-head":
				pool.checkpointIndexHead = len(pool.slots)
			case "scope-tag":
				pool.slots[scope.anchor].scopeCheckpointID++
			case "foreign-scope-header":
				pool.slots[scope.anchor].scopeCheckpointNext = foreign.anchor
				pool.slots[foreign.anchor].scopeCheckpointID = checkpoint.id
				pool.slots[foreign.anchor].scopeCheckpointNext = privatePagePoolNoIndex
				pool.checkpointScopeCount++
			}
			beforeSlots := append([]privatePagePoolSlot(nil), pool.slots...)
			beforeMutation := pool.mutationEpoch
			beforeSlotHead, beforeSlotCount := pool.checkpointSlotHead, pool.checkpointSlotCount
			beforeIndexHead, beforeIndexCount := pool.checkpointIndexHead, pool.checkpointIndexCount
			beforeScopeHead, beforeScopeCount := pool.checkpointScopeHead, pool.checkpointScopeCount
			if problem = pool.rollbackCheckpointInScope(checkpoint, scope); !problem.failed() {
				t.Fatal("corrupt journal rollback succeeded")
			}
			if pool.mutationEpoch != beforeMutation || !reflect.DeepEqual(pool.slots, beforeSlots) ||
				pool.checkpointSlotHead != beforeSlotHead || pool.checkpointSlotCount != beforeSlotCount ||
				pool.checkpointIndexHead != beforeIndexHead || pool.checkpointIndexCount != beforeIndexCount ||
				pool.checkpointScopeHead != beforeScopeHead || pool.checkpointScopeCount != beforeScopeCount {
				t.Fatal("corrupt journal rejection mutated checkpoint state")
			}
		})
	}
}
