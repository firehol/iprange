package exactv4

import (
	"testing"
	"time"
)

func scopedFreeBitmapCOWLedger(
	capacity int,
	replacementCapacity int,
	candidates []uint32,
	verified []verifiedBitmapPage,
	planned bool,
) freeBitmapCOWLedger {
	plannedLen := 0
	if planned {
		plannedLen = len(candidates)
	}
	return freeBitmapCOWLedger{
		replacements:        make([]uint32, replacementCapacity),
		candidates:          candidates,
		indexNodes:          make([]bitmapCOWIndexNode, capacity+replacementCapacity+plannedLen+len(verified)),
		availableSlots:      make([]int, capacity),
		arenaBindings:       make([]bitmapCOWArenaBinding, capacity),
		verifiedPages:       verified,
		plannedCandidateLen: plannedLen,
		reservationPlanned:  planned,
		plannedPrivatePages: capacity,
	}
}

func newScopedFreeBitmapCOWForTest(
	t *testing.T,
	capacity int,
	pageCount uint64,
	root uint32,
	ledger freeBitmapCOWLedger,
) (*freeBitmapCOW, *privatePagePool, privatePageReservationScope) {
	t.Helper()
	pool, _ := testVacantPrivatePagePool(t, capacity, pageCount, pageCount)
	scope, problem := pool.reserveScope(capacity)
	if problem.failed() {
		t.Fatal(problem)
	}
	cow, cowProblem := newFreeBitmapCOWWithScopedPool(nil, 1, pageCount, root, pool, scope, ledger)
	if cowProblem.failed() {
		t.Fatal(cowProblem)
	}
	return cow, pool, scope
}

func bindScopedPages(
	t *testing.T,
	pool *privatePagePool,
	scope privatePageReservationScope,
	pages []uint32,
	authorization privatePageAuthorization,
	commit bool,
) privatePagePoolCheckpoint {
	t.Helper()
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for _, page := range pages {
		if _, problem = pool.bindPage(checkpoint, scope, page, authorization); problem.failed() {
			t.Fatalf("bind page %d: %+v", page, problem)
		}
	}
	if commit {
		if problem = pool.commit(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	}
	return checkpoint
}

type scopedCOWSnapshot struct {
	root                  uint32
	pageCount             uint64
	pageCountsDistinct    bool
	indexRoot             int
	indexLen              int
	availableLen          int
	replacementLen        int
	candidateLen          int
	selectedCandidateLen  int
	candidateSelectionSet bool
	mutationEpoch         uint64
	pathLen               int
	candidate             uint32
	frames                [freeBitmapPathCapacity]freeBitmapPathFrame
	snapshots             [freeBitmapPathCapacity][PageSize]byte
	outputs               [freeBitmapPathCapacity][PageSize]byte
	survives              [freeBitmapPathCapacity]bool
	cloneSlots            [freeBitmapPathCapacity]int
	nodes                 []bitmapCOWIndexNode
	bindings              []bitmapCOWArenaBinding
	available             []int
	replacements          []uint32
	candidates            []uint32
	verified              []verifiedBitmapPage
}

func snapshotScopedCOW(cow *freeBitmapCOW) scopedCOWSnapshot {
	return scopedCOWSnapshot{
		root: cow.root, pageCount: cow.pageCount, pageCountsDistinct: cow.pageCountsDistinct,
		indexRoot: cow.indexRoot, indexLen: cow.indexLen, availableLen: cow.availableLen,
		replacementLen: cow.replacementLen, candidateLen: cow.candidateLen, selectedCandidateLen: cow.selectedCandidateLen,
		candidateSelectionSet: cow.candidateSelectionSet,
		mutationEpoch:         cow.mutationEpoch, pathLen: cow.pathLen, candidate: cow.candidate,
		frames: cow.frames, snapshots: cow.snapshots, outputs: cow.outputs,
		survives: cow.survives, cloneSlots: cow.cloneSlots,
		nodes:        append([]bitmapCOWIndexNode(nil), cow.indexNodes...),
		bindings:     append([]bitmapCOWArenaBinding(nil), cow.arenaBindings...),
		available:    append([]int(nil), cow.availableSlots...),
		replacements: append([]uint32(nil), cow.replacements...),
		candidates:   append([]uint32(nil), cow.candidates...),
		verified:     append([]verifiedBitmapPage(nil), cow.verifiedPages...),
	}
}

func requireScopedCOWSnapshot(t *testing.T, cow *freeBitmapCOW, want scopedCOWSnapshot) {
	t.Helper()
	got := snapshotScopedCOW(cow)
	if got.root != want.root || got.pageCount != want.pageCount || got.pageCountsDistinct != want.pageCountsDistinct ||
		got.indexRoot != want.indexRoot || got.indexLen != want.indexLen || got.availableLen != want.availableLen ||
		got.replacementLen != want.replacementLen || got.candidateLen != want.candidateLen ||
		got.selectedCandidateLen != want.selectedCandidateLen || got.candidateSelectionSet != want.candidateSelectionSet ||
		got.mutationEpoch != want.mutationEpoch || got.pathLen != want.pathLen || got.candidate != want.candidate ||
		got.frames != want.frames || got.snapshots != want.snapshots || got.outputs != want.outputs ||
		got.survives != want.survives || got.cloneSlots != want.cloneSlots {
		t.Fatal("rejected scoped synchronization changed COW scalars")
	}
	for index := range got.nodes {
		if got.nodes[index] != want.nodes[index] {
			t.Fatalf("rejected scoped synchronization changed index node %d", index)
		}
	}
	for index := range got.bindings {
		if got.bindings[index] != want.bindings[index] {
			t.Fatalf("rejected scoped synchronization changed binding %d", index)
		}
	}
	for index := range got.available {
		if got.available[index] != want.available[index] {
			t.Fatalf("rejected scoped synchronization changed available slot %d", index)
		}
	}
	for index := range got.replacements {
		if got.replacements[index] != want.replacements[index] {
			t.Fatalf("rejected scoped synchronization changed replacement scratch %d", index)
		}
	}
	for index := range got.candidates {
		if got.candidates[index] != want.candidates[index] {
			t.Fatalf("rejected scoped synchronization changed candidate scratch %d", index)
		}
	}
	for index := range got.verified {
		if got.verified[index] != want.verified[index] {
			t.Fatalf("rejected scoped synchronization changed verified scratch %d", index)
		}
	}
}

func synchronizeScopedBindingsBounded(
	t *testing.T,
	cow *freeBitmapCOW,
	scope privatePageReservationScope,
	selected int,
) freeBitmapCOWError {
	t.Helper()
	completed := make(chan freeBitmapCOWError, 1)
	go func() {
		completed <- cow.synchronizeScopedBindingsForCandidatePrefix(scope, selected)
	}()
	select {
	case problem := <-completed:
		return problem
	case <-time.After(2 * time.Second):
		t.Fatal("scoped synchronization did not return within its bounded deadline")
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
}

func ignoreScopedCOWRemovalScratch(snapshot *scopedCOWSnapshot, cow *freeBitmapCOW) {
	snapshot.pathLen = cow.pathLen
	snapshot.candidate = cow.candidate
	snapshot.frames = cow.frames
	snapshot.snapshots = cow.snapshots
	snapshot.outputs = cow.outputs
	snapshot.survives = cow.survives
	snapshot.cloneSlots = cow.cloneSlots
}

func TestFreeBitmapCOWScopedPoolStartsVacantAndSynchronizesReverseBindings(t *testing.T) {
	cow, pool, scope := newScopedFreeBitmapCOWForTest(
		t, 3, 20, 0, scopedFreeBitmapCOWLedger(3, 0, nil, nil, false),
	)
	if cow.indexRoot != bitmapCOWNoIndex || cow.availableLen != 0 {
		t.Fatalf("vacant COW = root %d/available %d", cow.indexRoot, cow.availableLen)
	}
	bindScopedPages(t, pool, scope, []uint32{9, 7, 8}, privatePageReclaimed, true)
	if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 0); problem.failed() {
		t.Fatal(problem)
	}
	if cow.availableLen != 3 {
		t.Fatalf("synchronized available = %d, want 3", cow.availableLen)
	}
	for _, page := range []uint32{7, 8, 9} {
		indexed, found := cow.indexedPage(page)
		if !found || indexed.kind != indexedBitmapPageArena || pool.slots[indexed.slot].pageNumber != page {
			t.Fatalf("page %d index = %+v/%t", page, indexed, found)
		}
	}
	if problem := cow.validateScopedBindings(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestFreeBitmapCOWScopedCandidatePrefixRemap(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected int
		pages    []uint32
		want     []indexedBitmapPageKind
	}{
		{name: "none", selected: 0, pages: []uint32{11, 10}, want: []indexedBitmapPageKind{indexedBitmapPagePlannedCandidate, indexedBitmapPagePlannedCandidate}},
		{name: "partial", selected: 1, pages: []uint32{10, 5}, want: []indexedBitmapPageKind{indexedBitmapPageArena, indexedBitmapPagePlannedCandidate}},
		{name: "all", selected: 2, pages: []uint32{6, 5}, want: []indexedBitmapPageKind{indexedBitmapPageArena, indexedBitmapPageArena}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := []uint32{5, 6}
			cow, pool, scope := newScopedFreeBitmapCOWForTest(
				t, 2, 20, 0, scopedFreeBitmapCOWLedger(2, 0, candidates, nil, true),
			)
			bindScopedPages(t, pool, scope, test.pages, privatePageCommittedFree, true)
			if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, test.selected); problem.failed() {
				t.Fatal(problem)
			}
			for index, page := range candidates {
				indexed, found := cow.indexedPage(page)
				if !found || indexed.kind != test.want[index] {
					t.Fatalf("candidate %d index = %+v/%t, want kind %d", page, indexed, found, test.want[index])
				}
			}
			if cow.selectedCandidateTarget() != test.selected || cow.availableLen != len(test.pages) {
				t.Fatalf("selection/available = %d/%d", cow.selectedCandidateTarget(), cow.availableLen)
			}
		})
	}
}

func TestFreeBitmapCOWScopedSelectedCandidateFundsOwnRemoval(t *testing.T) {
	leaf := cowLeaf(t, 2, 1, 5, 6)
	verified := []verifiedBitmapPage{{pageNumber: 2, bytes: leaf.bytes, base: 0, level: 0, survives: true}}
	cow, pool, scope := newScopedFreeBitmapCOWForTest(
		t, 1, 20, 2, scopedFreeBitmapCOWLedger(1, 1, []uint32{5}, verified, true),
	)
	bindScopedPages(t, pool, scope, []uint32{5}, privatePageCommittedFree, true)
	if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 1); problem.failed() {
		t.Fatal(problem)
	}
	cowBase, poolBase := *cow, *pool
	poolSlots := append([]privatePagePoolSlot(nil), pool.slots...)
	nodes := append([]bitmapCOWIndexNode(nil), cow.indexNodes...)
	bindings := append([]bitmapCOWArenaBinding(nil), cow.arenaBindings...)
	available := append([]int(nil), cow.availableSlots...)
	replacements := append([]uint32(nil), cow.replacements...)
	candidates := append([]uint32(nil), cow.candidates...)
	allocations := testing.AllocsPerRun(100, func() {
		*pool = poolBase
		copy(pool.slots, poolSlots)
		*cow = cowBase
		copy(cow.indexNodes, nodes)
		copy(cow.arenaBindings, bindings)
		copy(cow.availableSlots, available)
		copy(cow.replacements, replacements)
		copy(cow.candidates, candidates)
		if problem := cow.applyPlannedReservation(); problem.failed() {
			panic("scoped planned reservation")
		}
	})
	if allocations != 0 {
		t.Fatalf("scoped candidate-funded removal allocations = %v, want 0", allocations)
	}
	if cow.root != 5 || cow.candidateLen != 1 || cow.candidates[0] != 5 || cow.availableLen != 0 {
		t.Fatalf("candidate-funded result = root %d/candidates %d/available %d", cow.root, cow.candidateLen, cow.availableLen)
	}
	indexed, found := cow.indexedPage(5)
	if !found || indexed.kind != indexedBitmapPageArena || pool.slots[indexed.slot].state != privatePageInUse {
		t.Fatalf("candidate-funded arena = %+v/%t", indexed, found)
	}
	page, copied := copiedPrivateBitmapPage(cow, 5)
	if !copied {
		t.Fatal("candidate-funded root was not readable")
	}
	opened, pageProblem := openBitmapLeafNoAlloc(page[:], 2, bitmapKindFreePages)
	if pageProblem.code != 0 || opened.word(0)&(uint64(1)<<5) != 0 || opened.word(0)&(uint64(1)<<6) == 0 {
		t.Fatalf("candidate-funded bitmap = problem %+v/word %x", pageProblem, opened.word(0))
	}
}

func TestFreeBitmapCOWScopedGrowthRollbackRestoresPlannedCandidate(t *testing.T) {
	cow, pool, scope := newScopedFreeBitmapCOWForTest(
		t, 2, 20, 0, scopedFreeBitmapCOWLedger(2, 0, []uint32{5}, nil, true),
	)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 5, privatePageCommittedFree); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.bindPage(checkpoint, scope, 20, privatePageAppended); problem.failed() {
		t.Fatal(problem)
	}
	if cowProblem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 1); cowProblem.failed() {
		t.Fatal(cowProblem)
	}
	if cow.pageCount != 21 || cow.selectedCandidateTarget() != 1 {
		t.Fatalf("grown COW = page-count %d/selection %d", cow.pageCount, cow.selectedCandidateTarget())
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if cowProblem := cow.validateScopedBindings(); cowProblem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("rolled-back stale COW = %+v", cowProblem)
	}
	if cowProblem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 0); cowProblem.failed() {
		t.Fatal(cowProblem)
	}
	indexed, found := cow.indexedPage(5)
	if !found || indexed.kind != indexedBitmapPagePlannedCandidate || indexed.slot != 0 ||
		cow.pageCount != 20 || cow.availableLen != 0 {
		t.Fatalf("rollback resync = index %+v/%t page-count %d available %d", indexed, found, cow.pageCount, cow.availableLen)
	}
}

func TestFreeBitmapCOWScopedSyncRejectsForeignAndStaleIdentityAtomically(t *testing.T) {
	pool, _ := testVacantPrivatePagePool(t, 3, 20, 20)
	owned, problem := pool.reserveScope(2)
	if problem.failed() {
		t.Fatal(problem)
	}
	foreign, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	ledger := scopedFreeBitmapCOWLedger(2, 0, []uint32{5, 6}, nil, true)
	cow, cowProblem := newFreeBitmapCOWWithScopedPool(nil, 1, 20, 0, pool, owned, ledger)
	if cowProblem.failed() {
		t.Fatal(cowProblem)
	}
	bindScopedPages(t, pool, foreign, []uint32{7}, privatePageReclaimed, true)
	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(owned, 0); cowProblem.failed() {
		t.Fatal(cowProblem)
	}
	if _, found := cow.indexedPage(7); found || cow.availableLen != 0 {
		t.Fatal("foreign binding entered the owned COW arena")
	}
	before := snapshotScopedCOW(cow)
	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(owned, 1); cowProblem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("missing selected candidate sync = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)

	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(foreign, 0); cowProblem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("foreign scope sync = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)

	bindScopedPages(t, pool, owned, []uint32{6}, privatePageCommittedFree, true)
	before = snapshotScopedCOW(cow)
	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(owned, 1); cowProblem.code != freeBitmapCOWErrLedgerPageConflict {
		t.Fatalf("unselected candidate bind = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)

	cow.candidates[0], cow.candidates[1] = cow.candidates[1], cow.candidates[0]
	before = snapshotScopedCOW(cow)
	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(owned, 2); cowProblem.code != freeBitmapCOWErrCandidateOrderRegression {
		t.Fatalf("reordered candidate sync = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)
	cow.candidates[0], cow.candidates[1] = cow.candidates[1], cow.candidates[0]

	forged := owned
	forged.id++
	before = snapshotScopedCOW(cow)
	if cowProblem = cow.synchronizeScopedBindingsForCandidatePrefix(forged, 0); cowProblem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("forged scope sync = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)
}

func TestFreeBitmapCOWScopedSyncRejectsMutableMappingAliasesAtomically(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*freeBitmapCOW) func()
	}{
		{
			name: "duplicate pool slot",
			corrupt: func(cow *freeBitmapCOW) func() {
				previous := cow.arenaBindings[2].poolSlot
				cow.arenaBindings[2].poolSlot = cow.arenaBindings[1].poolSlot
				return func() { cow.arenaBindings[2].poolSlot = previous }
			},
		},
		{
			name: "reordered pool slots",
			corrupt: func(cow *freeBitmapCOW) func() {
				cow.arenaBindings[1].poolSlot, cow.arenaBindings[2].poolSlot =
					cow.arenaBindings[2].poolSlot, cow.arenaBindings[1].poolSlot
				return func() {
					cow.arenaBindings[1].poolSlot, cow.arenaBindings[2].poolSlot =
						cow.arenaBindings[2].poolSlot, cow.arenaBindings[1].poolSlot
				}
			},
		},
		{
			name: "duplicate storage node",
			corrupt: func(cow *freeBitmapCOW) func() {
				previous := cow.arenaBindings[2].storageNode
				cow.arenaBindings[2].storageNode = cow.arenaBindings[1].storageNode
				return func() { cow.arenaBindings[2].storageNode = previous }
			},
		},
		{
			name: "reordered storage nodes",
			corrupt: func(cow *freeBitmapCOW) func() {
				cow.arenaBindings[1].storageNode, cow.arenaBindings[2].storageNode =
					cow.arenaBindings[2].storageNode, cow.arenaBindings[1].storageNode
				return func() {
					cow.arenaBindings[1].storageNode, cow.arenaBindings[2].storageNode =
						cow.arenaBindings[2].storageNode, cow.arenaBindings[1].storageNode
				}
			},
		},
		{
			name: "active node alias",
			corrupt: func(cow *freeBitmapCOW) func() {
				previous := cow.arenaBindings[2].activeNode
				cow.arenaBindings[2].activeNode = cow.arenaBindings[1].activeNode
				return func() { cow.arenaBindings[2].activeNode = previous }
			},
		},
		{
			name: "candidate node alias",
			corrupt: func(cow *freeBitmapCOW) func() {
				previous := cow.arenaBindings[1].activeNode
				cow.arenaBindings[1].activeNode = cow.arenaBindings[0].activeNode
				return func() { cow.arenaBindings[1].activeNode = previous }
			},
		},
		{
			name: "candidate node fingerprint",
			corrupt: func(cow *freeBitmapCOW) func() {
				node := cow.arenaBindings[0].activeNode
				previous := cow.indexNodes[node].candidateIndex
				cow.indexNodes[node].candidateIndex = 1
				return func() { cow.indexNodes[node].candidateIndex = previous }
			},
		},
		{
			name: "storage node candidate alias",
			corrupt: func(cow *freeBitmapCOW) func() {
				node := cow.arenaBindings[1].storageNode
				previous := cow.indexNodes[node]
				cow.indexNodes[node].candidateMapped = true
				cow.indexNodes[node].candidatePage = 5
				return func() { cow.indexNodes[node] = previous }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cow, pool, scope := newScopedFreeBitmapCOWForTest(
				t, 3, 20, 0, scopedFreeBitmapCOWLedger(3, 0, []uint32{5}, nil, true),
			)
			bindScopedPages(t, pool, scope, []uint32{5, 10, 11}, privatePageCommittedFree, true)
			if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 1); problem.failed() {
				t.Fatal(problem)
			}
			restore := test.corrupt(cow)
			beforeCOW := snapshotScopedCOW(cow)
			beforePool := snapshotPrivatePagePoolScalars(pool)
			beforeSlots := append([]privatePagePoolSlot(nil), pool.slots...)
			problem := synchronizeScopedBindingsBounded(t, cow, scope, 1)
			if problem.code != freeBitmapCOWErrArenaPageConflict {
				t.Fatalf("corrupt mapping sync = %+v", problem)
			}
			requireScopedCOWSnapshot(t, cow, beforeCOW)
			requirePrivatePagePoolRawState(t, pool, pool.slots, beforePool, beforeSlots)
			restore()
			if problem = synchronizeScopedBindingsBounded(t, cow, scope, 1); problem.failed() {
				t.Fatalf("corrected mapping retry = %+v", problem)
			}
			if problem = cow.validateScopedBindings(); problem.failed() {
				t.Fatalf("corrected mapping validation = %+v", problem)
			}
		})
	}
}

func TestFreeBitmapCOWScopedSyncRejectsClosedScopeIdentityAtomically(t *testing.T) {
	cow, pool, scope := newScopedFreeBitmapCOWForTest(
		t, 1, 20, 0, scopedFreeBitmapCOWLedger(1, 0, nil, nil, false),
	)
	before := snapshotScopedCOW(cow)
	if problem := pool.closeScope(scope); problem.failed() {
		t.Fatal(problem)
	}
	if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 0); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("closed scope sync = %+v", problem)
	}
	requireScopedCOWSnapshot(t, cow, before)
	replacement, problem := pool.reserveScope(1)
	if problem.failed() {
		t.Fatal(problem)
	}
	if replacement.id == scope.id {
		t.Fatal("replacement scope reused stale identity")
	}
	if cowProblem := cow.synchronizeScopedBindingsForCandidatePrefix(replacement, 0); cowProblem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("replacement scope sync = %+v", cowProblem)
	}
	requireScopedCOWSnapshot(t, cow, before)
}

func TestFreeBitmapCOWScopedRemovalPreflightsCompleteHeadroom(t *testing.T) {
	newCOW := func(t *testing.T) (*freeBitmapCOW, *privatePagePool, privatePageReservationScope) {
		t.Helper()
		leaf := cowLeaf(t, 2, 1, 5, 6)
		verified := []verifiedBitmapPage{{pageNumber: 2, bytes: leaf.bytes, base: 0, level: 0, survives: true}}
		cow, pool, scope := newScopedFreeBitmapCOWForTest(
			t, 1, 20, 2, scopedFreeBitmapCOWLedger(1, 1, []uint32{5}, verified, true),
		)
		bindScopedPages(t, pool, scope, []uint32{5}, privatePageCommittedFree, true)
		if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 1); problem.failed() {
			t.Fatal(problem)
		}
		return cow, pool, scope
	}

	t.Run("slot epoch", func(t *testing.T) {
		cow, pool, _ := newCOW(t)
		binding := &cow.arenaBindings[0]
		pool.slots[binding.poolSlot].epoch = ^uint64(0)
		binding.poolEpoch = ^uint64(0)
		beforeCOW := snapshotScopedCOW(cow)
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), pool.slots...)
		if problem := cow.applyPlannedReservation(); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("terminal slot epoch = %+v", problem)
		}
		ignoreScopedCOWRemovalScratch(&beforeCOW, cow)
		requireScopedCOWSnapshot(t, cow, beforeCOW)
		requirePrivatePagePoolRawState(t, pool, pool.slots, beforePool, beforeSlots)
	})

	t.Run("aggregate epoch", func(t *testing.T) {
		cow, pool, _ := newCOW(t)
		pool.mutationEpoch = ^uint64(0) - 2
		beforeCOW := snapshotScopedCOW(cow)
		beforePool := snapshotPrivatePagePoolScalars(pool)
		beforeSlots := append([]privatePagePoolSlot(nil), pool.slots...)
		if problem := cow.applyPlannedReservation(); problem.code != freeBitmapCOWErrMutationEpochExhausted {
			t.Fatalf("aggregate epoch = %+v", problem)
		}
		ignoreScopedCOWRemovalScratch(&beforeCOW, cow)
		requireScopedCOWSnapshot(t, cow, beforeCOW)
		requirePrivatePagePoolRawState(t, pool, pool.slots, beforePool, beforeSlots)
	})
}

func TestFreeBitmapCOWScopedSyncUsesNoHeapAndScales(t *testing.T) {
	for _, count := range []int{512, 4096} {
		t.Run(stringInt(count), func(t *testing.T) {
			pageCount := uint64(count + 10)
			cow, pool, scope := newScopedFreeBitmapCOWForTest(
				t, count, pageCount, 0, scopedFreeBitmapCOWLedger(count, 0, nil, nil, false),
			)
			pages := make([]uint32, count)
			for index := range pages {
				pages[index] = uint32(count + 1 - index)
			}
			bindScopedPages(t, pool, scope, pages, privatePageReclaimed, true)
			if problem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 0); problem.failed() {
				t.Fatal(problem)
			}
			if cow.availableLen != count {
				t.Fatalf("%d-slot available = %d", count, cow.availableLen)
			}
			maximumHeight := uint8(2 * bitsRequiredForPositiveInt(count+1))
			if cow.indexRoot == bitmapCOWNoIndex || cow.indexNodes[cow.indexRoot].height > maximumHeight {
				t.Fatalf("%d-slot COW AVL height = %d, max %d", count, cow.indexNodes[cow.indexRoot].height, maximumHeight)
			}
			allocations := testing.AllocsPerRun(100, func() {
				if syncProblem := cow.synchronizeScopedBindingsForCandidatePrefix(scope, 0); syncProblem.failed() {
					panic("scoped sync")
				}
			})
			if allocations != 0 {
				t.Fatalf("%d-slot scoped sync allocations = %v, want 0", count, allocations)
			}
		})
	}
}
