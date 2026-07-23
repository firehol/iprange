package exactv4

import "testing"

func testVacantPrivatePagePool(
	t *testing.T,
	capacity int,
	committedPageCount, pendingPageCount uint64,
) (*privatePagePool, []privatePagePoolSlot) {
	t.Helper()
	slots := make([]privatePagePoolSlot, capacity)
	pool := &privatePagePool{}
	if problem := initVacantPrivatePagePool(pool, slots, committedPageCount, pendingPageCount, 2); problem.failed() {
		t.Fatal(problem)
	}
	return pool, slots
}

type privatePageBindingSnapshot struct {
	bound            bool
	pageNumber       uint32
	authorization    privatePageAuthorization
	scopeID          uint64
	scopeAnchor      bool
	scopeAnchorIndex int
	scopeVacantNext  int
	scopeMemberNext  int
	unscopedNext     int
	unscopedPrevious int
	scopeRoot        int
	scopeVacantHead  int
	scopeMemberHead  int
	scopeCapacity    int
	scopeBound       int
	state            privatePageState
	owner            privatePageOwner
	origin           privatePageOrigin
	pendingTxn       uint64
	generation       uint64
	committedOrigin  uint32
	bytes            [PageSize]byte
	inUse            bool
	indexLeft        int
	indexRight       int
	indexHeight      int8
	indexFree        uint64
	indexInUse       uint64
	scopeLeft        int
	scopeRight       int
	scopeHeight      int8
	scopeFree        uint64
	scopeInUse       uint64
}

func snapshotPrivatePageBindings(slots []privatePagePoolSlot) []privatePageBindingSnapshot {
	result := make([]privatePageBindingSnapshot, len(slots))
	for index := range slots {
		slot := &slots[index]
		result[index] = privatePageBindingSnapshot{
			bound: slot.bound, pageNumber: slot.pageNumber, authorization: slot.authorization,
			scopeID: slot.scopeID, scopeAnchor: slot.scopeAnchor, scopeAnchorIndex: slot.scopeAnchorIndex,
			scopeVacantNext: slot.scopeVacantNext, scopeMemberNext: slot.scopeMemberNext,
			unscopedNext: slot.unscopedNext, unscopedPrevious: slot.unscopedPrevious, scopeRoot: slot.scopeRoot,
			scopeVacantHead: slot.scopeVacantHead, scopeMemberHead: slot.scopeMemberHead,
			scopeCapacity: slot.scopeCapacity, scopeBound: slot.scopeBound,
			state: slot.state, owner: slot.owner, origin: slot.origin, pendingTxn: slot.pendingTxn,
			generation: slot.generation, committedOrigin: slot.committedOrigin, bytes: slot.bytes, inUse: slot.inUse,
			indexLeft: slot.indexLeft, indexRight: slot.indexRight, indexHeight: slot.indexHeight,
			indexFree: slot.indexFree, indexInUse: slot.indexInUse,
			scopeLeft: slot.scopeLeft, scopeRight: slot.scopeRight, scopeHeight: slot.scopeHeight,
			scopeFree: slot.scopeFree, scopeInUse: slot.scopeInUse,
		}
	}
	return result
}

func requirePrivatePageBindingSnapshot(
	t *testing.T,
	slots []privatePagePoolSlot,
	want []privatePageBindingSnapshot,
) {
	t.Helper()
	got := snapshotPrivatePageBindings(slots)
	if len(got) != len(want) {
		t.Fatalf("binding snapshot length = %d, want %d", len(got), len(want))
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("binding snapshot slot %d changed", index)
		}
	}
}

type privatePageAVLProof struct {
	minimum uint32
	maximum uint32
	height  int
	count   int
	free    uint64
	inUse   uint64
}

func verifyPrivatePageAVL(t *testing.T, pool *privatePagePool, root int, scoped bool, scopeID uint64) privatePageAVLProof {
	t.Helper()
	if root == privatePagePoolNoIndex {
		return privatePageAVLProof{}
	}
	if root < 0 || root >= len(pool.slots) {
		t.Fatalf("AVL root %d is outside caller storage", root)
	}
	slot := &pool.slots[root]
	if !slot.bound {
		t.Fatalf("AVL contains vacant slot %d", root)
	}
	if scoped && slot.scopeID != scopeID {
		t.Fatalf("scope AVL slot %d belongs to scope %d, want %d", root, slot.scopeID, scopeID)
	}
	left, right, height := slot.indexLeft, slot.indexRight, int(slot.indexHeight)
	wantFree, wantInUse := slot.indexFree, slot.indexInUse
	if scoped {
		left, right, height = slot.scopeLeft, slot.scopeRight, int(slot.scopeHeight)
		wantFree, wantInUse = slot.scopeFree, slot.scopeInUse
	}
	leftProof := verifyPrivatePageAVL(t, pool, left, scoped, scopeID)
	rightProof := verifyPrivatePageAVL(t, pool, right, scoped, scopeID)
	if leftProof.count != 0 && leftProof.maximum >= slot.pageNumber {
		t.Fatalf("AVL left maximum %d >= page %d", leftProof.maximum, slot.pageNumber)
	}
	if rightProof.count != 0 && rightProof.minimum <= slot.pageNumber {
		t.Fatalf("AVL right minimum %d <= page %d", rightProof.minimum, slot.pageNumber)
	}
	wantHeight := leftProof.height
	if rightProof.height > wantHeight {
		wantHeight = rightProof.height
	}
	wantHeight++
	if height != wantHeight || leftProof.height-rightProof.height < -1 || leftProof.height-rightProof.height > 1 {
		t.Fatalf("AVL page %d height/balance = %d/%d:%d", slot.pageNumber, height, leftProof.height, rightProof.height)
	}
	free, inUse := leftProof.free+rightProof.free, leftProof.inUse+rightProof.inUse
	if slot.state == privatePageAvailable {
		free++
	} else if slot.state == privatePageInUse {
		inUse++
	}
	if free != wantFree || inUse != wantInUse {
		t.Fatalf("AVL page %d counts = %d/%d, want %d/%d", slot.pageNumber, wantFree, wantInUse, free, inUse)
	}
	minimum, maximum := slot.pageNumber, slot.pageNumber
	if leftProof.count != 0 {
		minimum = leftProof.minimum
	}
	if rightProof.count != 0 {
		maximum = rightProof.maximum
	}
	return privatePageAVLProof{
		minimum: minimum, maximum: maximum, height: wantHeight,
		count: leftProof.count + rightProof.count + 1, free: free, inUse: inUse,
	}
}

func TestPrivatePagePoolVacantScopeReverseBindAndLowest(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 8, 100, 100)
	scope, problem := pool.reserveScope(len(slots))
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for page := uint32(9); page >= 2; page-- {
		if _, problem = pool.bindPage(checkpoint, scope, page, privatePageCommittedFree); problem.failed() {
			t.Fatalf("reverse bind page %d: %v", page, problem)
		}
	}
	if pool.pendingPageCount != 100 {
		t.Fatalf("committed binds grew pending page-count to %d", pool.pendingPageCount)
	}
	global := verifyPrivatePageAVL(t, pool, pool.indexRoot, false, 0)
	scopeRoot := pool.slots[scope.anchor].scopeRoot
	scoped := verifyPrivatePageAVL(t, pool, scopeRoot, true, scope.id)
	available, availableProblem := pool.scopedAvailable(scope)
	if availableProblem.failed() || available != len(slots) {
		t.Fatalf("scoped available = %d/problem %v, want %d", available, availableProblem, len(slots))
	}
	if global.count != len(slots) || scoped.count != len(slots) || global.minimum != 2 || scoped.minimum != 2 {
		t.Fatalf("reverse bind proof = global %#v scoped %#v", global, scoped)
	}
	token, problem := pool.claimLowestInScope(checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() || pool.slots[token.slot].pageNumber != 2 {
		t.Fatalf("scoped lowest claim = page %d/problem %v", pool.slots[token.slot].pageNumber, problem)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if pool.indexRoot != privatePagePoolNoIndex || pool.pendingPageCount != 100 {
		t.Fatalf("reverse rollback root/page-count = %d/%d", pool.indexRoot, pool.pendingPageCount)
	}
	for index := range slots {
		if slots[index].bound || slots[index].scopeID != scope.id {
			t.Fatalf("rollback slot %d = bound %t scope %d", index, slots[index].bound, slots[index].scopeID)
		}
	}
}

func TestPrivatePagePoolScopeLifecycleVisitsOnlySelectedMembers(t *testing.T) {
	for _, foreignCount := range []int{512, 4096} {
		t.Run(stringInt(foreignCount), func(t *testing.T) {
			const targetCapacity = 2
			pool, _ := testVacantPrivatePagePool(t, foreignCount+targetCapacity, 10000, 10000)

			prefixCount := foreignCount / 2
			prefix, visits, problem := pool.reserveScopeCounted(prefixCount)
			if problem.failed() || visits != 2*prefixCount {
				t.Fatalf("prefix reserve = scope %#v visits %d problem %#v, want %d visits",
					prefix, visits, problem, 2*prefixCount)
			}
			target, visits, problem := pool.reserveScopeCounted(targetCapacity)
			if problem.failed() || visits != 2*targetCapacity {
				t.Fatalf("target reserve amid %d foreign slots = visits %d problem %#v, want %d",
					foreignCount, visits, problem, 2*targetCapacity)
			}
			suffixCount := foreignCount - prefixCount
			suffix, visits, problem := pool.reserveScopeCounted(suffixCount)
			if problem.failed() || visits != 2*suffixCount {
				t.Fatalf("suffix reserve = scope %#v visits %d problem %#v, want %d visits",
					suffix, visits, problem, 2*suffixCount)
			}
			if pool.unscopedVacantCount != 0 {
				t.Fatalf("unscoped vacancy count = %d, want 0", pool.unscopedVacantCount)
			}

			visits, problem = pool.closeScopeCounted(target)
			if problem.failed() || visits != 3*targetCapacity {
				t.Fatalf("target close amid %d foreign slots = visits %d problem %#v, want %d",
					foreignCount, visits, problem, 3*targetCapacity)
			}
			if pool.unscopedVacantCount != targetCapacity || pool.unscopedVacantHead != target.anchor {
				t.Fatalf("returned vacancy queue = count %d head %d, want %d/%d",
					pool.unscopedVacantCount, pool.unscopedVacantHead, targetCapacity, target.anchor)
			}
			replacement, visits, problem := pool.reserveScopeCounted(targetCapacity)
			if problem.failed() || visits != 2*targetCapacity || replacement.anchor != target.anchor {
				t.Fatalf("replacement reserve = anchor %d visits %d problem %#v, want %d/%d",
					replacement.anchor, visits, problem, target.anchor, 2*targetCapacity)
			}
			visits, problem = pool.closeScopeCounted(replacement)
			if problem.failed() || visits != 3*targetCapacity {
				t.Fatalf("replacement close = visits %d problem %#v, want %d", visits, problem, 3*targetCapacity)
			}
		})
	}
}

func TestPrivatePagePoolScopeLifecycleRejectsNonVacantQueueState(t *testing.T) {
	t.Run("reserve", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 2, 20, 20)
		slots[pool.unscopedVacantHead].owner = privatePageOwnerBitmap
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		if _, _, problem := pool.reserveScopeCounted(1); problem.code != privatePagePoolErrInvalidState {
			t.Fatalf("reserve corrupted vacancy = %#v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
	})

	for _, test := range []struct {
		name   string
		mutate func(*privatePagePool, *privatePagePoolSlot)
	}{
		{name: "reserve checkpoint tag", mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.checkpointID = 7 }},
		{name: "reserve index checkpoint tag", mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.indexCheckpointID = 7 }},
		{name: "reserve scope checkpoint tag", mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.scopeCheckpointID = 7 }},
		{name: "reserve validation marker", mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.batchMarked = true }},
		{name: "reserve payload", mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.bytes[17] = 0xa5 }},
		{name: "reserve checkpoint cleanup", mutate: func(pool *privatePagePool, _ *privatePagePoolSlot) { pool.checkpointCleanup = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, slots := testVacantPrivatePagePool(t, 2, 20, 20)
			test.mutate(pool, &slots[pool.unscopedVacantHead])
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), slots...)
			if _, _, problem := pool.reserveScopeCounted(1); problem.code != privatePagePoolErrInvalidState {
				t.Fatalf("reserve noncanonical vacancy = %#v", problem)
			}
			requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		})
	}

	for _, test := range []struct {
		name   string
		want   privatePagePoolErrorCode
		mutate func(*privatePagePool, *privatePagePoolSlot)
	}{
		{name: "close checkpoint tag", want: privatePagePoolErrStaleScope, mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.checkpointID = 7 }},
		{name: "close index checkpoint tag", want: privatePagePoolErrStaleScope, mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.indexCheckpointID = 7 }},
		{name: "close scope checkpoint tag", want: privatePagePoolErrStaleScope, mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.scopeCheckpointID = 7 }},
		{name: "close validation marker", want: privatePagePoolErrStaleScope, mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.batchMarked = true }},
		{name: "close payload", want: privatePagePoolErrStaleScope, mutate: func(_ *privatePagePool, slot *privatePagePoolSlot) { slot.bytes[17] = 0xa5 }},
		{name: "close checkpoint cleanup", want: privatePagePoolErrInvalidState, mutate: func(pool *privatePagePool, _ *privatePagePoolSlot) { pool.checkpointCleanup = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, slots := testVacantPrivatePagePool(t, 2, 20, 20)
			scope, problem := pool.reserveScope(2)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.mutate(pool, &slots[scope.anchor])
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), slots...)
			if _, problem := pool.closeScopeCounted(scope); problem.code != test.want {
				t.Fatalf("close noncanonical vacancy = %#v", problem)
			}
			requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		})
	}

	t.Run("close", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 2, 20, 20)
		scope, problem := pool.reserveScope(2)
		if problem.failed() {
			t.Fatal(problem)
		}
		slots[scope.anchor].unscopedNext = scope.anchor
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		if _, problem := pool.closeScopeCounted(scope); problem.code != privatePagePoolErrStaleScope {
			t.Fatalf("close corrupted member = %#v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
	})

	for _, test := range []struct {
		name   string
		mutate func(*privatePagePool, []privatePagePoolSlot)
	}{
		{name: "destination head bounds", mutate: func(pool *privatePagePool, _ []privatePagePoolSlot) {
			pool.unscopedVacantHead = len(pool.slots)
		}},
		{name: "destination tail bounds", mutate: func(pool *privatePagePool, _ []privatePagePoolSlot) {
			pool.unscopedVacantTail = len(pool.slots)
		}},
		{name: "destination count", mutate: func(pool *privatePagePool, _ []privatePagePoolSlot) {
			pool.unscopedVacantCount = 1
		}},
		{name: "destination tail state", mutate: func(pool *privatePagePool, slots []privatePagePoolSlot) {
			slots[pool.unscopedVacantTail].owner = privatePageOwnerBitmap
		}},
		{name: "destination tail reciprocal", mutate: func(pool *privatePagePool, slots []privatePagePoolSlot) {
			slots[pool.unscopedVacantTail].unscopedPrevious = pool.unscopedVacantTail
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, slots := testVacantPrivatePagePool(t, 4, 20, 20)
			scope, problem := pool.reserveScope(2)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.mutate(pool, slots)
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), slots...)
			if _, problem := pool.closeScopeCounted(scope); problem.code != privatePagePoolErrInvalidState {
				t.Fatalf("close corrupted destination queue = %#v", problem)
			}
			requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		})
	}
}

func TestPrivatePagePoolReserveRejectsCorruptFirstUnselectedVacancy(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]privatePagePoolSlot)
	}{
		{name: "payload", mutate: func(slots []privatePagePoolSlot) { slots[2].bytes[17] = 0xa5 }},
		{name: "owner", mutate: func(slots []privatePagePoolSlot) { slots[2].owner = privatePageOwnerBitmap }},
		{name: "forward reciprocal", mutate: func(slots []privatePagePoolSlot) { slots[2].unscopedNext = 4 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, slots := testVacantPrivatePagePool(t, 6, 20, 20)
			test.mutate(slots)
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), slots...)
			if _, _, problem := pool.reserveScopeCounted(2); problem.code != privatePagePoolErrInvalidState {
				t.Fatalf("reserve corrupt first unselected vacancy = %#v", problem)
			}
			requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		})
	}
}

func TestPrivatePagePoolBindingRejectsCorruptVacancyBoundary(t *testing.T) {
	for _, operation := range []struct {
		name    string
		prepare func(*testing.T, *privatePagePool, privatePageReservationScope, privatePagePoolCheckpoint) int
		apply   func(*privatePagePool, privatePageReservationScope, privatePagePoolCheckpoint) privatePagePoolError
	}{
		{
			name: "bind",
			prepare: func(_ *testing.T, pool *privatePagePool, scope privatePageReservationScope, _ privatePagePoolCheckpoint) int {
				return pool.slots[scope.anchor].scopeVacantNext
			},
			apply: func(pool *privatePagePool, scope privatePageReservationScope, checkpoint privatePagePoolCheckpoint) privatePagePoolError {
				_, problem := pool.bindPage(checkpoint, scope, 7, privatePageReclaimed)
				return problem
			},
		},
		{
			name: "unbind",
			prepare: func(t *testing.T, pool *privatePagePool, scope privatePageReservationScope, checkpoint privatePagePoolCheckpoint) int {
				for _, page := range []uint32{7, 8} {
					if _, problem := pool.bindPage(checkpoint, scope, page, privatePageReclaimed); problem.failed() {
						t.Fatal(problem)
					}
				}
				return pool.slots[scope.anchor].scopeVacantHead
			},
			apply: func(pool *privatePagePool, scope privatePageReservationScope, checkpoint privatePagePoolCheckpoint) privatePagePoolError {
				return pool.unbindPage(checkpoint, scope, 7)
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, corruption := range []struct {
				name   string
				mutate func([]privatePagePoolSlot, int)
			}{
				{name: "payload", mutate: func(slots []privatePagePoolSlot, head int) { slots[head].bytes[17] = 0xa5 }},
				{name: "owner", mutate: func(slots []privatePagePoolSlot, head int) { slots[head].owner = privatePageOwnerBitmap }},
				{name: "forward link", mutate: func(slots []privatePagePoolSlot, head int) { slots[head].scopeVacantNext = len(slots) }},
			} {
				t.Run(corruption.name, func(t *testing.T) {
					pool, slots := testVacantPrivatePagePool(t, 4, 20, 20)
					scope, problem := pool.reserveScope(4)
					if problem.failed() {
						t.Fatal(problem)
					}
					checkpoint, problem := pool.begin()
					if problem.failed() {
						t.Fatal(problem)
					}
					head := operation.prepare(t, pool, scope, checkpoint)
					corruption.mutate(slots, head)
					beforePool := snapshotPrivatePagePoolScalars(pool)
					beforeSlots := append([]privatePagePoolSlot(nil), slots...)
					if problem = operation.apply(pool, scope, checkpoint); problem.code != privatePagePoolErrInvalidState {
						t.Fatalf("%s corrupt vacancy boundary = %#v", operation.name, problem)
					}
					requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
				})
			}
		})
	}
}

func TestPrivatePagePoolCloseRequiresExactVacancyPermutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*privatePagePool, privatePageReservationScope, privatePageReservationScope)
	}{
		{name: "omitted member", mutate: func(pool *privatePagePool, work, _ privatePageReservationScope) {
			pool.slots[work.anchor].scopeVacantNext = privatePagePoolNoIndex
		}},
		{name: "duplicate cycle", mutate: func(pool *privatePagePool, work, _ privatePageReservationScope) {
			head := pool.slots[work.anchor].scopeVacantHead
			pool.slots[head].scopeVacantNext = head
		}},
		{name: "foreign link", mutate: func(pool *privatePagePool, work, foreign privatePageReservationScope) {
			head := pool.slots[work.anchor].scopeVacantHead
			pool.slots[head].scopeVacantNext = foreign.anchor
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, slots := testVacantPrivatePagePool(t, 4, 20, 20)
			work, problem := pool.reserveScope(3)
			if problem.failed() {
				t.Fatal(problem)
			}
			foreign, problem := pool.reserveScope(1)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.mutate(pool, work, foreign)
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), slots...)
			if _, problem := pool.closeScopeCounted(work); problem.code != privatePagePoolErrStaleScope {
				t.Fatalf("close non-permutation = %#v", problem)
			}
			requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		})
	}
}

func TestPrivatePagePoolCloseAcceptsPermutedVacanciesAndRetainedSnapshots(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 3, 20, 20)
	scope, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for _, page := range []uint32{7, 8, 9} {
		if _, problem = pool.bindPage(checkpoint, scope, page, privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem = pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for _, page := range []uint32{7, 8, 9} {
		if problem = pool.unbindPage(checkpoint, scope, page); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	anchor := &slots[scope.anchor]
	if anchor.scopeVacantHead == anchor.scopeMemberHead {
		t.Fatal("fixture did not permute the scope vacancy chain")
	}
	retainedSnapshot := false
	for member := anchor.scopeMemberHead; member != privatePagePoolNoIndex; member = slots[member].scopeMemberNext {
		slot := &slots[member]
		if slot.checkpointID != 0 || slot.indexCheckpointID != 0 || slot.scopeCheckpointID != 0 {
			t.Fatalf("active checkpoint tag survived commit in slot %d", member)
		}
		retainedSnapshot = retainedSnapshot || slot.checkpointBound || slot.checkpointPageNumber != 0 ||
			slot.checkpointScopeID != 0 || slot.checkpointScopeBound != 0
	}
	if !retainedSnapshot {
		t.Fatal("fixture did not retain inert checkpoint snapshot values")
	}
	visits, problem := pool.closeScopeCounted(scope)
	if problem.failed() || visits != 3*len(slots) {
		t.Fatalf("close permuted vacancy = visits %d problem %#v, want %d", visits, problem, 3*len(slots))
	}
}

func TestPrivatePagePoolCloseAcceptsRetainedRollbackSnapshots(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
	scope, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	slot := &slots[scope.anchor]
	if slot.checkpointID != 0 || slot.indexCheckpointID != 0 || slot.scopeCheckpointID != 0 {
		t.Fatalf("active checkpoint tags survived rollback: %d/%d/%d",
			slot.checkpointID, slot.indexCheckpointID, slot.scopeCheckpointID)
	}
	if slot.checkpointScopeID != scope.id {
		t.Fatal("fixture did not retain inert rollback snapshot values")
	}
	visits, problem := pool.closeScopeCounted(scope)
	if problem.failed() || visits != 3 {
		t.Fatalf("close rollback-restored vacancy = visits %d problem %#v", visits, problem)
	}
}

func TestPrivatePagePoolBindingFailuresAreAtomicAndScoped(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 4, 20, 20)
	first, problem := pool.reserveScope(2)
	if problem.failed() {
		t.Fatal(problem)
	}
	second, problem := pool.reserveScope(2)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ := pool.begin()
	if _, problem = pool.bindPage(checkpoint, first, 7, privatePageCommittedFree); problem.failed() {
		t.Fatal(problem)
	}

	assertAtomic := func(name string, want privatePagePoolErrorCode, operation func() privatePagePoolError) {
		t.Helper()
		before := append([]privatePagePoolSlot(nil), slots...)
		root, pageCount, mutation := pool.indexRoot, pool.pendingPageCount, pool.mutationEpoch
		generation, checkpointID, scopeSequence := pool.generation, pool.activeCheckpointID, pool.scopeSequence
		problem := operation()
		if problem.code != want {
			t.Fatalf("%s = %+v, want code %d", name, problem, want)
		}
		for index := range slots {
			if slots[index] != before[index] {
				t.Fatalf("%s changed slot %d", name, index)
			}
		}
		if pool.indexRoot != root || pool.pendingPageCount != pageCount || pool.mutationEpoch != mutation ||
			pool.generation != generation || pool.activeCheckpointID != checkpointID || pool.scopeSequence != scopeSequence {
			t.Fatalf("%s changed pool scalars", name)
		}
	}
	assertAtomic("duplicate", privatePagePoolErrPagesNotStrict, func() privatePagePoolError {
		_, problem := pool.bindPage(checkpoint, second, 7, privatePageReclaimed)
		return problem
	})
	assertAtomic("cross-scope unbind", privatePagePoolErrScopeMismatch, func() privatePagePoolError {
		return pool.unbindPage(checkpoint, second, 7)
	})
	assertAtomic("page zero", privatePagePoolErrPageOutOfBounds, func() privatePagePoolError {
		_, problem := pool.bindPage(checkpoint, second, 0, privatePageCommittedFree)
		return problem
	})
	assertAtomic("committed authorization", privatePagePoolErrInvalidAuthorization, func() privatePagePoolError {
		_, problem := pool.bindPage(checkpoint, second, 20, privatePageCommittedFree)
		return problem
	})
	assertAtomic("appended gap", privatePagePoolErrInvalidAuthorization, func() privatePagePoolError {
		_, problem := pool.bindPage(checkpoint, second, 21, privatePageAppended)
		return problem
	})
	forged := first
	forged.id++
	assertAtomic("forged scope", privatePagePoolErrStaleScope, func() privatePagePoolError {
		_, problem := pool.bindPage(checkpoint, forged, 8, privatePageReclaimed)
		return problem
	})
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivatePagePoolArbitraryAVLDeletionPreservesIndexes(t *testing.T) {
	const count = 127
	pool, _ := testVacantPrivatePagePool(t, count, 1000, 1000)
	scope, problem := pool.reserveScope(count)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ := pool.begin()
	for index := 0; index < count; index++ {
		page := uint32((index*53)%count + 2)
		if _, problem = pool.bindPage(checkpoint, scope, page, privatePageReclaimed); problem.failed() {
			t.Fatalf("bind permutation index %d/page %d: %v", index, page, problem)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ = pool.begin()
	remaining := count
	for index := 0; index < count; index++ {
		page := uint32((index*89)%count + 2)
		if problem = pool.unbindPage(checkpoint, scope, page); problem.failed() {
			t.Fatalf("delete permutation index %d/page %d: %v", index, page, problem)
		}
		remaining--
		global := verifyPrivatePageAVL(t, pool, pool.indexRoot, false, 0)
		scoped := verifyPrivatePageAVL(t, pool, pool.slots[scope.anchor].scopeRoot, true, scope.id)
		if global.count != remaining || scoped.count != remaining {
			t.Fatalf("delete %d left global/scoped %d/%d, want %d", page, global.count, scoped.count, remaining)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.closeScope(scope); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivatePagePoolBindingRollbackRestoresExactStateAndTail(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 3, 20, 20)
	scope, problem := pool.reserveScope(3)
	if problem.failed() {
		t.Fatal(problem)
	}
	before := snapshotPrivatePageBindings(slots)
	rootBefore, pageCountBefore := pool.indexRoot, pool.pendingPageCount
	checkpoint, _ := pool.begin()
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 20, privatePageAppended); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 21, privatePageAppended); problem.failed() {
		t.Fatal(problem)
	}
	if pool.pendingPageCount != 22 {
		t.Fatalf("bound appended suffix page-count = %d, want 22", pool.pendingPageCount)
	}
	if problem = pool.unbindPage(checkpoint, scope, 20); problem.failed() {
		t.Fatal(problem)
	}
	if pool.pendingPageCount != 22 {
		t.Fatalf("unbound appended hole page-count = %d, want 22", pool.pendingPageCount)
	}
	if problem = pool.unbindPage(checkpoint, scope, 21); problem.failed() {
		t.Fatal(problem)
	}
	token, problem := pool.claimLowestInScope(checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	var contents [PageSize]byte
	contents[37] = 0xa5
	if problem = pool.writePage(token, &contents); problem.failed() {
		t.Fatal(problem)
	}
	if pool.pendingPageCount != 21 {
		t.Fatalf("bound tail page-count = %d, want 21", pool.pendingPageCount)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	requirePrivatePageBindingSnapshot(t, slots, before)
	if pool.indexRoot != rootBefore || pool.pendingPageCount != pageCountBefore || pool.activeCheckpointID != 0 {
		t.Fatalf("rollback scalars = root %d page-count %d active %d", pool.indexRoot, pool.pendingPageCount, pool.activeCheckpointID)
	}
	if pool.readPage(token, &contents).code != privatePagePoolErrStaleToken {
		t.Fatal("rollback token retained authority")
	}
}

func TestPrivatePagePoolUnbindRebindInvalidatesTokens(t *testing.T) {
	pool, _ := testVacantPrivatePagePool(t, 1, 20, 20)
	scope, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ := pool.begin()
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	oldToken, problem := pool.claimLowestInScope(checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.recycle(oldToken); problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ = pool.begin()
	if problem = pool.unbindPage(checkpoint, scope, 7); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	newToken, problem := pool.claimLowestInScope(checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	var contents [PageSize]byte
	if pool.readPage(oldToken, &contents).code != privatePagePoolErrStaleToken || pool.readPage(newToken, &contents).failed() {
		t.Fatal("unbind/rebind admitted an ABA token")
	}
}

func TestPrivatePagePoolScopeAndBindingIdentityWrapAreAtomic(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
	pool.scopeSequence = ^uint64(0)
	before := slots[0]
	if _, problem := pool.reserveScope(1); problem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("wrapped scope = %+v", problem)
	}
	if slots[0] != before || pool.scopeSequence != ^uint64(0) {
		t.Fatal("wrapped scope mutated caller storage")
	}
	pool.scopeSequence = 0
	scope, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ := pool.begin()
	slots[0].epoch = ^uint64(0)
	before = slots[0]
	mutationBefore := pool.mutationEpoch
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("wrapped slot epoch = %+v", problem)
	}
	if slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("wrapped slot epoch mutated pool")
	}
	slots[0].epoch = 2
	pool.mutationEpoch = ^uint64(0)
	before = slots[0]
	if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("wrapped mutation epoch = %+v", problem)
	}
	if slots[0] != before || pool.mutationEpoch != ^uint64(0) {
		t.Fatal("wrapped mutation epoch changed binding")
	}
	pool.mutationEpoch = mutationBefore
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}

	other, _ := testVacantPrivatePagePool(t, 1, 20, 20)
	otherCheckpoint, _ := other.begin()
	if _, problem = other.bindPage(otherCheckpoint, scope, 7, privatePageReclaimed); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("cross-pool scope = %+v", problem)
	}
	if problem = other.rollback(otherCheckpoint); problem.failed() {
		t.Fatal(problem)
	}
	pool.checkpointSequence = ^uint64(0)
	if _, problem = pool.begin(); problem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("wrapped checkpoint = %+v", problem)
	}
}

type privatePagePoolScalarSnapshot struct {
	committedPageCount   uint64
	pendingPageCount     uint64
	pendingTxn           uint64
	epoch                uint64
	generation           uint64
	mutationEpoch        uint64
	checkpointSequence   uint64
	activeCheckpointID   uint64
	checkpointCleanup    uint64
	checkpointIndexHead  int
	checkpointIndexCount int
	operationSequence    uint64
	activeOperationID    uint64
	scopeSequence        uint64
	activeScopes         int
	unscopedVacantHead   int
	unscopedVacantTail   int
	unscopedVacantCount  int
	indexRoot            int
}

func snapshotPrivatePagePoolScalars(pool *privatePagePool) privatePagePoolScalarSnapshot {
	return privatePagePoolScalarSnapshot{
		committedPageCount: pool.committedPageCount, pendingPageCount: pool.pendingPageCount,
		pendingTxn: pool.pendingTxn, epoch: pool.epoch, generation: pool.generation,
		mutationEpoch: pool.mutationEpoch, checkpointSequence: pool.checkpointSequence,
		activeCheckpointID: pool.activeCheckpointID, checkpointCleanup: pool.checkpointCleanup,
		checkpointIndexHead: pool.checkpointIndexHead, checkpointIndexCount: pool.checkpointIndexCount,
		operationSequence: pool.operationSequence, activeOperationID: pool.activeOperationID,
		scopeSequence: pool.scopeSequence, activeScopes: pool.activeScopes,
		unscopedVacantHead: pool.unscopedVacantHead, unscopedVacantTail: pool.unscopedVacantTail,
		unscopedVacantCount: pool.unscopedVacantCount, indexRoot: pool.indexRoot,
	}
}

func requirePrivatePagePoolRawState(
	t *testing.T,
	pool *privatePagePool,
	slots []privatePagePoolSlot,
	wantPool privatePagePoolScalarSnapshot,
	wantSlots []privatePagePoolSlot,
) {
	t.Helper()
	if got := snapshotPrivatePagePoolScalars(pool); got != wantPool {
		t.Fatalf("pool scalars changed: got %+v want %+v", got, wantPool)
	}
	for index := range slots {
		if slots[index] != wantSlots[index] {
			t.Fatalf("slot %d changed", index)
		}
	}
}

func TestPrivatePagePoolCheckpointCleanupHeadroomIsProspective(t *testing.T) {
	t.Run("slot rejection is atomic", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
		scope, problem := pool.reserveScope(1)
		if problem.failed() {
			t.Fatal(problem)
		}
		checkpoint, _ := pool.begin()
		slots[0].epoch = ^uint64(0) - 1
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("terminal slot bind = %+v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		if problem = pool.rollback(checkpoint); problem.failed() || pool.activeCheckpointID != 0 || pool.checkpointCleanup != 0 {
			t.Fatalf("empty rollback = %+v active/cleanup %d/%d", problem, pool.activeCheckpointID, pool.checkpointCleanup)
		}
	})

	t.Run("aggregate rejection is atomic", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
		scope, problem := pool.reserveScope(1)
		if problem.failed() {
			t.Fatal(problem)
		}
		checkpoint, _ := pool.begin()
		pool.mutationEpoch = ^uint64(0) - 1
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("terminal aggregate bind = %+v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	})

	t.Run("later mutation rejects and exact rollback survives", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 2, 20, 20)
		scope, problem := pool.reserveScope(2)
		if problem.failed() {
			t.Fatal(problem)
		}
		before := snapshotPrivatePageBindings(slots)
		checkpoint, _ := pool.begin()
		pool.mutationEpoch = ^uint64(0) - 3
		if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
		firstMutation := pool.mutationEpoch
		if _, problem = pool.bindPage(checkpoint, scope, 8, privatePageReclaimed); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("later aggregate bind = %+v", problem)
		}
		if pool.mutationEpoch != firstMutation || pool.checkpointCleanup != 1 {
			t.Fatal("later rejection consumed reserved cleanup")
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		requirePrivatePageBindingSnapshot(t, slots, before)
		if pool.activeCheckpointID != 0 || pool.checkpointCleanup != 0 {
			t.Fatal("rollback retained checkpoint authority")
		}
	})

	t.Run("last cleanup epoch invalidates token", func(t *testing.T) {
		slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
		pool := testPrivatePagePool(t, slots, 20, 20)
		slots[0].epoch = ^uint64(0) - 2
		before := snapshotPrivatePageBindings(slots)
		checkpoint, _ := pool.begin()
		token, problem := pool.claimPage(checkpoint, 7, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() {
			t.Fatal(problem)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		requirePrivatePageBindingSnapshot(t, slots, before)
		if slots[0].epoch != ^uint64(0) || pool.readPage(token, &[PageSize]byte{}).code != privatePagePoolErrStaleToken {
			t.Fatal("terminal rollback did not consume cleanup epoch and invalidate token")
		}
	})

	t.Run("later write cannot consume rollback reservation", func(t *testing.T) {
		slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
		pool := testPrivatePagePool(t, slots, 20, 20)
		before := snapshotPrivatePageBindings(slots)
		checkpoint, _ := pool.begin()
		pool.mutationEpoch = ^uint64(0) - 3
		token, problem := pool.claimPage(checkpoint, 7, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() {
			t.Fatal(problem)
		}
		first := [PageSize]byte{31: 0xa5}
		if problem = pool.writePage(token, &first); problem.failed() {
			t.Fatal(problem)
		}
		beforeRejectedWrite := slots[0]
		second := [PageSize]byte{31: 0x5a}
		if problem = pool.writePage(token, &second); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("write consuming cleanup reservation = %+v", problem)
		}
		if slots[0] != beforeRejectedWrite || pool.checkpointCleanup != 1 {
			t.Fatal("rejected write changed slot or cleanup reservation")
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		requirePrivatePageBindingSnapshot(t, slots, before)
		if pool.readPage(token, &[PageSize]byte{}).code != privatePagePoolErrStaleToken {
			t.Fatal("aggregate-terminal rollback retained token")
		}
	})

	t.Run("transfer preserves rollback epoch", func(t *testing.T) {
		slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
		pool := testPrivatePagePool(t, slots, 20, 20)
		operation, _ := pool.beginOperation()
		token, problem := pool.claimPageForOperation(operation, 7, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() {
			t.Fatal(problem)
		}
		if problem = pool.commitOperation(operation); problem.failed() {
			t.Fatal(problem)
		}
		slots[0].epoch = ^uint64(0) - 2
		token = pool.tokenFor(0)
		before := snapshotPrivatePageBindings(slots)
		checkpoint, _ := pool.begin()
		transferred, problem := pool.transfer(checkpoint, token, privatePageOwnerRetirement, privatePageRetirementTree)
		if problem.failed() {
			t.Fatal(problem)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		requirePrivatePageBindingSnapshot(t, slots, before)
		if slots[0].epoch != ^uint64(0) || pool.readPage(transferred, &[PageSize]byte{}).code != privatePagePoolErrStaleToken {
			t.Fatal("terminal transfer rollback retained authority")
		}
	})

	t.Run("unbind terminal rejection is atomic", func(t *testing.T) {
		pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
		scope, problem := pool.reserveScope(1)
		if problem.failed() {
			t.Fatal(problem)
		}
		checkpoint, _ := pool.begin()
		if _, problem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
		if problem = pool.commit(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		slots[0].epoch = ^uint64(0) - 1
		checkpoint, _ = pool.begin()
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		if problem = pool.unbindPage(checkpoint, scope, 7); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("terminal unbind = %+v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	})

	t.Run("prepared scan rejects a terminal later slot", func(t *testing.T) {
		slots := []privatePagePoolSlot{
			newPrivatePageSlot(7, privatePageReclaimed),
			newPrivatePageSlot(8, privatePageReclaimed),
			newPrivatePageSlot(9, privatePageReclaimed),
		}
		pool := testPrivatePagePool(t, slots, 20, 20)
		slotIndex, _ := pool.slotIndex(9)
		pool.slots[slotIndex].epoch = ^uint64(0) - 1
		checkpoint, _ := pool.begin()
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), slots...)
		remaining := len(slots)
		if problem := pool.preflightLowestAvailableEpochs(pool.indexRoot, &remaining); problem.code != privatePagePoolErrArithmeticOverflow {
			t.Fatalf("prepared terminal scan = %+v", problem)
		}
		requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
		if problem := pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	})
}

func TestPrivatePagePoolCheckpointCommitOwnsTerminalCleanup(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := testPrivatePagePool(t, slots, 20, 20)
	operation, _ := pool.beginOperation()
	token, problem := pool.claimPageForOperation(operation, 7, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commitOperation(operation); problem.failed() {
		t.Fatal(problem)
	}
	slots[0].epoch = ^uint64(0) - 2
	token = pool.tokenFor(0)
	checkpoint, _ := pool.begin()
	if problem = pool.recycle(token); problem.failed() {
		t.Fatal(problem)
	}
	if slots[0].state != privatePagePendingReturn || slots[0].epoch != ^uint64(0)-1 || pool.checkpointCleanup != 1 {
		t.Fatal("checkpoint release did not retain terminal commit headroom")
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if slots[0].state != privatePageAvailable || slots[0].epoch != ^uint64(0) || pool.checkpointCleanup != 0 {
		t.Fatal("commit did not consume exactly the reserved cleanup")
	}
}

func TestPrivatePagePoolScopeAuthorityIsExactAndSelective(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 3, 20, 20)
	first, problem := pool.reserveScope(2)
	if problem.failed() {
		t.Fatal(problem)
	}
	second, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, _ := pool.begin()
	if _, problem = pool.bindPage(checkpoint, first, 7, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, first, 9, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, second, 8, privatePageReclaimed); problem.failed() {
		t.Fatal(problem)
	}
	firstToken, problem := pool.claimPageInScope(checkpoint, first, 7, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	secondToken, problem := pool.claimPageInScope(checkpoint, second, 8, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}

	if _, problem = pool.borrow(7, privatePageOwnerBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global borrow = %+v", problem)
	}
	if _, problem = pool.borrowExact(7, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global exact borrow = %+v", problem)
	}
	if _, problem = pool.borrowExactInScope(second, 7, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("cross-scope exact borrow = %+v", problem)
	}
	if _, problem = pool.borrowExactInScope(first, 7, privatePageOwnerBitmap, privatePageBitmap); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.returnUnowned(9, privatePageReleasedFree); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global unowned return = %+v", problem)
	}
	if problem = pool.returnUnownedInScope(second, 9, privatePageReleasedFree); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("cross-scope unowned return = %+v", problem)
	}
	pageNine, _ := pool.slotIndex(9)
	beforePreparedRelease := slots[pageNine]
	if problem = pool.claimSlotForOperationPrepared(
		privatePagePoolOperation{}, pageNine, privatePageOwnerBitmap, privatePageBitmap,
	); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global prepared operation claim = %+v", problem)
	}
	if slots[pageNine] != beforePreparedRelease {
		t.Fatal("global prepared operation claim changed scoped slot")
	}
	if problem = pool.releaseSlotPrepared(pageNine, privatePageAvailable); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global prepared release = %+v", problem)
	}
	if slots[pageNine] != beforePreparedRelease {
		t.Fatal("global prepared release changed scoped slot")
	}

	checkpoint, _ = pool.begin()
	if problem = pool.releaseSlotForCheckpointPrepared(checkpoint, pageNine, privatePageAvailable); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global prepared checkpoint release = %+v", problem)
	}
	if slots[pageNine] != beforePreparedRelease {
		t.Fatal("global prepared checkpoint release changed scoped slot")
	}
	if _, problem = pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global lowest claim = %+v", problem)
	}
	if _, problem = pool.claimPage(checkpoint, 9, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global exact claim = %+v", problem)
	}
	if _, problem = pool.claimPageInScope(checkpoint, second, 9, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("cross-scope exact claim = %+v", problem)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.beginOperation(); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global operation preflight = %+v", problem)
	}

	before := append([]privatePagePoolSlot(nil), slots...)
	if problem = pool.releaseGeneration(firstToken.generation, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrScopeMismatch {
		t.Fatalf("global generation release = %+v", problem)
	}
	for index := range slots {
		if slots[index] != before[index] {
			t.Fatalf("global generation release changed slot %d", index)
		}
	}
	if problem = pool.releaseGenerationInScope(first, firstToken.generation, privatePageOwnerBitmap, privatePageBitmap); problem.failed() {
		t.Fatal(problem)
	}
	if pool.readPage(firstToken, &[PageSize]byte{}).code != privatePagePoolErrStaleToken ||
		pool.readPage(secondToken, &[PageSize]byte{}).failed() {
		t.Fatal("selective release crossed scope authority")
	}
	if problem = pool.releaseGenerationInScope(second, secondToken.generation, privatePageOwnerBitmap, privatePageBitmap); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivatePagePoolScopedOperationAuthorityIsExact(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 3, 20, 20)
	owned, problem := pool.reserveScope(2)
	if problem.failed() {
		t.Fatal(problem)
	}
	foreign, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for _, binding := range []struct {
		scope privatePageReservationScope
		page  uint32
	}{{owned, 7}, {foreign, 8}, {owned, 9}} {
		if _, problem = pool.bindPage(checkpoint, binding.scope, binding.page, privatePageReclaimed); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}

	operation, problem := pool.preflightOperationInScope(owned)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool.beginOperationPrepared(operation)
	pageSeven, _ := pool.slotIndex(7)
	pageEight, _ := pool.slotIndex(8)
	pageNine, _ := pool.slotIndex(9)
	foreignBefore := slots[pageEight]
	if problem = pool.claimSlotForOperationInScopePrepared(
		operation, pageEight, privatePageOwnerBitmap, privatePageBitmap,
	); problem.code != privatePagePoolErrScopeMismatch || slots[pageEight] != foreignBefore {
		t.Fatalf("foreign prepared claim = %+v/changed %t", problem, slots[pageEight] != foreignBefore)
	}
	if problem = pool.claimSlotForOperationInScopePrepared(
		operation, pageSeven, privatePageOwnerBitmap, privatePageBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	var page [PageSize]byte
	page[PageHeaderSize] = 0x5a
	if problem = pool.writeSlotForOperationInScopePrepared(operation, pageSeven, &page); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.setSlotCommittedOriginForOperationInScopePrepared(operation, pageSeven, 3); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.claimSlotForOperationInScopePrepared(
		operation, pageNine, privatePageOwnerBitmap, privatePageBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.releaseSlotForOperationInScopePrepared(operation, pageNine, privatePageAvailable); problem.failed() {
		t.Fatal(problem)
	}
	pool.commitOperationPrepared(operation)
	if slots[pageSeven].state != privatePageInUse || slots[pageSeven].bytes != page ||
		slots[pageSeven].committedOrigin != 3 || slots[pageNine].state != privatePageAvailable {
		t.Fatal("scoped prepared operation did not apply exact transitions")
	}
	if problem = pool.writeSlotForOperationInScopePrepared(operation, pageSeven, &page); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("stale operation = %+v", problem)
	}

	second, problem := pool.preflightOperationInScope(owned)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool.beginOperationPrepared(second)
	page[PageHeaderSize]++
	if problem = pool.writeSlotForOperationInScopePrepared(second, pageSeven, &page); problem.failed() {
		t.Fatalf("later exact scoped write = %+v", problem)
	}
	pool.commitOperationPrepared(second)
	if slots[pageSeven].bytes != page {
		t.Fatal("later exact scoped write did not update the owned page")
	}
	forged := owned
	forged.id = foreign.id
	if _, problem = pool.preflightOperationInScope(forged); problem.code != privatePagePoolErrStaleScope {
		t.Fatalf("forged operation scope = %+v", problem)
	}
	copied := *pool
	if _, problem = copied.preflightOperationInScope(owned); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("copied pool operation = %+v", problem)
	}
}

func TestPrivatePagePoolCloseScopeGuardsActiveCount(t *testing.T) {
	pool, slots := testVacantPrivatePagePool(t, 1, 20, 20)
	scope, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool.activeScopes = 0
	beforePool := snapshotPrivatePagePoolScalars(pool)
	beforeSlots := append([]privatePagePoolSlot(nil), slots...)
	if problem = pool.closeScope(scope); problem.code != privatePagePoolErrStaleScope {
		t.Fatalf("zero-active close = %+v", problem)
	}
	requirePrivatePagePoolRawState(t, pool, slots, beforePool, beforeSlots)
	pool.activeScopes = 1
	if problem = pool.closeScope(scope); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivatePagePoolBindingUsesNoHeap(t *testing.T) {
	pool, _ := testVacantPrivatePagePool(t, 1, 20, 20)
	scope, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	allocations := testing.AllocsPerRun(100, func() {
		checkpoint, bindProblem := pool.begin()
		if bindProblem.failed() {
			panic("checkpoint")
		}
		if _, bindProblem = pool.bindPage(checkpoint, scope, 7, privatePageReclaimed); bindProblem.failed() {
			panic("bind")
		}
		if bindProblem = pool.unbindPage(checkpoint, scope, 7); bindProblem.failed() {
			panic("unbind")
		}
		if bindProblem = pool.rollback(checkpoint); bindProblem.failed() {
			panic("rollback")
		}
	})
	if allocations != 0 {
		t.Fatalf("binding allocations = %v, want 0", allocations)
	}
}

func TestPrivatePagePoolBindingAVLScalingIsNonQuadratic(t *testing.T) {
	for _, count := range []int{512, 4096} {
		t.Run(stringInt(count), func(t *testing.T) {
			pool, _ := testVacantPrivatePagePool(t, count, uint64(count+10), uint64(count+10))
			scope, problem := pool.reserveScope(count)
			if problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, _ := pool.begin()
			for page := uint32(count + 1); page >= 2; page-- {
				if _, problem = pool.bindPage(checkpoint, scope, page, privatePageReclaimed); problem.failed() {
					t.Fatalf("bind page %d: %v", page, problem)
				}
			}
			global := verifyPrivatePageAVL(t, pool, pool.indexRoot, false, 0)
			scoped := verifyPrivatePageAVL(t, pool, pool.slots[scope.anchor].scopeRoot, true, scope.id)
			maximumHeight := 2 * bitsRequiredForPositiveInt(count+1)
			if global.count != count || scoped.count != count || global.height > maximumHeight || scoped.height > maximumHeight {
				t.Fatalf("%d-slot AVL = global %#v scoped %#v max-height %d", count, global, scoped, maximumHeight)
			}
			lowest, lowestProblem := pool.lowestAvailableSlotInScope(scope)
			if lowestProblem.failed() || pool.slots[lowest].pageNumber != 2 {
				t.Fatalf("%d-slot lowest = slot %d/problem %v", count, lowest, lowestProblem)
			}
			if problem = pool.rollback(checkpoint); problem.failed() {
				t.Fatal(problem)
			}
		})
	}
}

func bitsRequiredForPositiveInt(value int) int {
	bits := 0
	for value != 0 {
		bits++
		value >>= 1
	}
	return bits
}
