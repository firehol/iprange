package exactv4

import "testing"

func privateWriterTestMeta() Meta {
	return Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		DatabaseID:    [16]byte{1},
		TxnID:         7,
		CommitNonce:   [16]byte{2},
		PageCount:     32,
	}
}

func initPrivateWriterTestCore(
	t *testing.T,
	core *privateWriterTransactionCore,
	slots []privatePagePoolSlot,
	validation []uint32,
	cleanupObligations []privateWriterCleanupObligation,
	cleanupOwners []privateWriterCleanupOwner,
) Meta {
	t.Helper()
	selected := privateWriterTestMeta()
	problem := initPrivateWriterTransactionCore(
		core, selected,
		privateWriterResourceBudget{
			maxHeapBytes: 1 << 20, maxPrivatePages: uint64(len(slots)),
			maxFileGrowthPages: uint64(len(slots)), maxOpenFiles: 4,
		},
		slots, validation, cleanupObligations, cleanupOwners,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	return selected
}

func bindPrivateWriterTestScope(
	t *testing.T,
	core *privateWriterTransactionCore,
	count int,
) privatePageReservationScope {
	t.Helper()
	scope, problem := core.pool.reserveScope(count)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := core.pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	for index := 0; index < count; index++ {
		if _, problem = core.pool.bindPage(
			checkpoint, scope, uint32(2+index), privatePageReclaimed,
		); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem = core.pool.commitCheckpointInScopePrepared(checkpoint, scope); problem.failed() {
		t.Fatal(problem)
	}
	return scope
}

func TestPrivateWriterTransactionPostMutationFailureRequiresWholeAbort(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 3)
	selected := initPrivateWriterTestCore(
		t, &core, slots, make([]uint32, 3),
		make([]privateWriterCleanupObligation, 2), make([]privateWriterCleanupOwner, 2),
	)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	scope := bindPrivateWriterTestScope(t, &core, 3)
	operation, poolProblem := core.pool.beginOperationInScope(scope)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	token, poolProblem := core.pool.claimPageForOperationInScope(
		operation, 2, privatePageOwnerBitmap, privatePageBitmap,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	var page [PageSize]byte
	page[PageHeaderSize] = 0xa5
	if poolProblem = core.pool.writeSlotForOperationInScopePrepared(operation, token.slot, &page); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem = core.operationFailed(handle, operation); problem.code != privateWriterTransactionErrAbortRequired ||
		core.state != privateWriterTransactionAbortRequired || !core.pool.abortRequired ||
		core.pool.activeOperationID != operation.id {
		t.Fatalf("post-write failure = core=%d pool=%+v active=%d", core.state, problem, core.pool.activeOperationID)
	}
	if _, poolProblem = core.pool.begin(); poolProblem.code != privatePagePoolErrAbortRequired {
		t.Fatalf("checkpoint after failure = %+v", poolProblem)
	}
	if _, poolProblem = core.pool.beginOperationInScope(scope); poolProblem.code != privatePagePoolErrAbortRequired {
		t.Fatalf("operation after failure = %+v", poolProblem)
	}
	if poolProblem = core.pool.commitOperation(operation); poolProblem.code != privatePagePoolErrAbortRequired {
		t.Fatalf("operation commit after failure = %+v", poolProblem)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("writer commit after failure = %+v", problem)
	}

	visits, problem := core.abort()
	if problem.failed() || visits != uint64(len(slots)) {
		t.Fatalf("abort = visits %d error %+v", visits, problem)
	}
	if core.selected != selected || core.target != (Meta{}) || core.state != privateWriterTransactionClean {
		t.Fatalf("abort identity/state = selected %#v target %#v state %d", core.selected, core.target, core.state)
	}
	for index := range slots {
		if slots[index].bound || slots[index].scopeID != 0 || slots[index].state != privatePageAvailable ||
			slots[index].bytes[PageHeaderSize] != 0 {
			t.Fatalf("slot %d survived abort: %+v", index, slots[index])
		}
	}
	if _, poolProblem = core.pool.claimPageForOperationInScope(
		operation, 2, privatePageOwnerBitmap, privatePageBitmap,
	); poolProblem.code != privatePagePoolErrCrossPool {
		t.Fatalf("old operation survived abort = %+v", poolProblem)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("old writer handle survived abort = %+v", problem)
	}
	if _, problem = core.abort(); problem.code != privateWriterTransactionErrNoPendingTransaction {
		t.Fatalf("repeated completed abort = %+v", problem)
	}
	fresh, problem := core.begin([16]byte{4})
	if problem.failed() || fresh.epoch == handle.epoch || core.target.TxnID != selected.TxnID+1 ||
		core.target.CommitNonce != ([16]byte{4}) {
		t.Fatalf("fresh transaction = handle %#v target %#v error %+v", fresh, core.target, problem)
	}
}

func TestPrivatePagePoolAbortRequiredAfterEachMutationStage(t *testing.T) {
	for _, stage := range []string{"claim", "write", "release"} {
		t.Run(stage, func(t *testing.T) {
			slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
			var pool privatePagePool
			if problem := initPrivatePagePool(
				&pool, slots, make([]uint32, 1), 32, 32, 8, privatePageOwnerNone,
			); problem.failed() {
				t.Fatal(problem)
			}
			operation, problem := pool.beginOperation()
			if problem.failed() {
				t.Fatal(problem)
			}
			token, problem := pool.claimPageForOperation(
				operation, 7, privatePageOwnerBitmap, privatePageBitmap,
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			if stage != "claim" {
				var page [PageSize]byte
				if problem = pool.writePage(token, &page); problem.failed() {
					t.Fatal(problem)
				}
			}
			if stage == "release" {
				if problem = pool.recycle(token); problem.failed() {
					t.Fatal(problem)
				}
			}
			if problem = pool.abortOperation(operation); problem.code != privatePagePoolErrAbortRequired ||
				!pool.abortRequired || pool.activeOperationID != operation.id {
				t.Fatalf("%s abort = %+v active=%d", stage, problem, pool.activeOperationID)
			}
			if _, problem = pool.beginOperation(); problem.code != privatePagePoolErrAbortRequired {
				t.Fatalf("%s permitted next operation: %+v", stage, problem)
			}
			if problem = pool.commitOperation(operation); problem.code != privatePagePoolErrAbortRequired {
				t.Fatalf("%s permitted commit: %+v", stage, problem)
			}
		})
	}
}

func TestPrivatePagePoolPostMutationAbortIdentityErrorStillPoisonsPool(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	var pool privatePagePool
	if problem := initPrivatePagePool(
		&pool, slots, make([]uint32, 1), 32, 32, 8, privatePageOwnerNone,
	); problem.failed() {
		t.Fatal(problem)
	}
	operation, problem := pool.beginOperation()
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = pool.claimPageForOperation(
		operation, 7, privatePageOwnerBitmap, privatePageBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	forged := operation
	forged.poolEpoch++
	if problem = pool.abortOperation(forged); problem.code != privatePagePoolErrCrossPool ||
		!pool.abortRequired || pool.activeOperationID != operation.id {
		t.Fatalf("post-mutation identity failure = %+v pool=%+v", problem, pool)
	}
	if _, problem = pool.begin(); problem.code != privatePagePoolErrAbortRequired {
		t.Fatalf("poisoned pool permitted checkpoint: %+v", problem)
	}
}

func TestPrivateWriterTransactionPreMutationFailureIsDraftNeutral(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 1)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 1), nil, nil)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	scope := bindPrivateWriterTestScope(t, &core, 1)
	before := core.pool.mutationEpoch
	operation, poolProblem := core.pool.beginOperationInScope(scope)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem = core.operationFailed(handle, operation); problem.failed() {
		t.Fatalf("pre-mutation failure changed transaction state: %+v", problem)
	}
	if core.state != privateWriterTransactionPending || core.pool.abortRequired ||
		core.pool.activeOperationID != 0 || core.pool.mutationEpoch != before {
		t.Fatalf("pre-mutation failure = core=%d pool=%+v", core.state, core.pool)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrAbortRequired ||
		core.state != privateWriterTransactionAbortRequired {
		t.Fatalf("active generic scope reached commit: %+v", problem)
	}
}

func TestPrivateWriterTransactionHandleIdentitySurvivesCoreReinitialization(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 1)
	validation := make([]uint32, 1)
	selected := initPrivateWriterTestCore(t, &core, slots, validation, nil, nil)
	oldHandle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = core.abort(); problem.failed() {
		t.Fatal(problem)
	}
	invalidatingEpoch := core.handleEpoch
	if problem = initPrivateWriterTransactionCore(
		&core, selected, core.resources.budget, slots, validation, nil, nil,
	); problem.failed() {
		t.Fatal(problem)
	}
	if core.handleEpoch != invalidatingEpoch {
		t.Fatalf("clean reinitialization reset handle epoch: got %d want %d", core.handleEpoch, invalidatingEpoch)
	}
	newHandle, problem := core.begin([16]byte{4})
	if problem.failed() {
		t.Fatal(problem)
	}
	if newHandle.epoch == oldHandle.epoch {
		t.Fatalf("clean reinitialization revived handle epoch %d", oldHandle.epoch)
	}
	if problem = core.validateHandle(oldHandle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("old handle survived clean reinitialization: %+v", problem)
	}
	if _, problem = core.abort(); problem.failed() {
		t.Fatal(problem)
	}

	// Reusing the same address after the clean object's storage is reset must
	// still not recreate either prior handle identity.
	core = privateWriterTransactionCore{}
	if problem = initPrivateWriterTransactionCore(
		&core, selected, privateWriterResourceBudget{maxPrivatePages: 1},
		slots, validation, nil, nil,
	); problem.failed() {
		t.Fatal(problem)
	}
	reusedHandle, problem := core.begin([16]byte{5})
	if problem.failed() {
		t.Fatal(problem)
	}
	if reusedHandle.epoch == oldHandle.epoch || reusedHandle.epoch == newHandle.epoch {
		t.Fatalf("reset storage revived an old identity: old=%d new=%d reused=%d", oldHandle.epoch, newHandle.epoch, reusedHandle.epoch)
	}
}

func TestPrivateWriterTransactionBeginReservesAbortIncarnation(t *testing.T) {
	saved := privateWriterHandleIncarnation.Load()
	defer privateWriterHandleIncarnation.Store(saved)

	var core privateWriterTransactionCore
	var slots [1]privatePagePoolSlot
	var validation [1]uint32
	initPrivateWriterTestCore(t, &core, slots[:], validation[:], nil, nil)

	privateWriterHandleIncarnation.Store(^uint64(0) - 1)
	selectedBefore := core.selected
	if _, problem := core.begin([16]byte{3}); problem.code != privateWriterTransactionErrTransactionExhausted {
		t.Fatalf("unabortable Begin = %+v", problem)
	}
	if core.state != privateWriterTransactionClean || core.selected != selectedBefore ||
		core.target != (Meta{}) || core.pool.self != nil || core.handleEpoch != 0 || core.abortEpoch != 0 {
		t.Fatalf("rejected Begin changed clean core: %#v", core)
	}

	privateWriterHandleIncarnation.Store(^uint64(0) - 2)
	handle, problem := core.begin([16]byte{4})
	if problem.failed() || handle.epoch != ^uint64(0)-1 || core.abortEpoch != ^uint64(0) {
		t.Fatalf("reserved terminal pair = handle %#v core %#v error %+v", handle, core, problem)
	}
	if _, problem = core.abort(); problem.failed() || core.handleEpoch != ^uint64(0) ||
		core.abortEpoch != 0 || core.state != privateWriterTransactionClean {
		t.Fatalf("reserved terminal invalidation = core %#v error %+v", core, problem)
	}
	if problem = core.validateHandle(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("terminal Abort did not stale returned handle: %+v", problem)
	}
	if _, problem = core.begin([16]byte{5}); problem.code != privateWriterTransactionErrTransactionExhausted {
		t.Fatalf("exhausted global incarnation admitted Begin: %+v", problem)
	}
}

func TestPrivateWriterTransactionBeginReservesPoolInvalidationIncarnation(t *testing.T) {
	saved := privatePagePoolIncarnation.Load()
	defer privatePagePoolIncarnation.Store(saved)

	var core privateWriterTransactionCore
	var slots [1]privatePagePoolSlot
	var validation [1]uint32
	initPrivateWriterTestCore(t, &core, slots[:], validation[:], nil, nil)

	privatePagePoolIncarnation.Store(^uint64(0) - 1)
	selectedBefore := core.selected
	if _, problem := core.begin([16]byte{3}); problem.code != privateWriterTransactionErrPool ||
		problem.pool.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("pool without invalidation incarnation admitted Begin: %+v", problem)
	}
	if core.state != privateWriterTransactionClean || core.selected != selectedBefore ||
		core.target != (Meta{}) || core.pool.self != nil || core.abortScrubbed {
		t.Fatalf("pool-incarnation rejection changed clean core: %#v", core)
	}

	privatePagePoolIncarnation.Store(^uint64(0) - 2)
	handle, problem := core.begin([16]byte{4})
	if problem.failed() || core.pool.epoch != ^uint64(0)-1 ||
		core.pool.invalidationEpoch != ^uint64(0) {
		t.Fatalf("final pool reservation = handle %#v pool %#v error %+v", handle, core.pool, problem)
	}
	if _, problem = core.abort(); problem.failed() || core.pool.epoch != ^uint64(0) ||
		core.state != privateWriterTransactionClean {
		t.Fatalf("final pool invalidation = core %#v error %+v", core, problem)
	}
	if problem = core.validateHandle(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("final pool Abort retained writer handle: %+v", problem)
	}
}

func TestPrivateWriterTransactionPoolPreservesExactAbortHeadroom(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 2)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 2), nil, nil)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	if core.pool.abortMutationReserve != 2 {
		t.Fatalf("whole-draft reserve = %d", core.pool.abortMutationReserve)
	}
	core.pool.mutationEpoch = ^uint64(0) - core.pool.abortMutationReserve - 1
	if _, poolProblem := core.pool.reserveScope(1); poolProblem.failed() ||
		core.pool.mutationEpoch != ^uint64(0)-core.pool.abortMutationReserve {
		t.Fatalf("last legal forward mutation = epoch %d error %+v", core.pool.mutationEpoch, poolProblem)
	}
	beforePool := sealFreeBitmapReservationPool(&core.pool)
	beforeSlots := append([]privatePagePoolSlot(nil), slots...)
	if _, poolProblem := core.pool.reserveScope(1); poolProblem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("mutation consumed Abort reserve: %+v", poolProblem)
	}
	if !beforePool.matches(&core.pool) {
		t.Fatal("rejected mutation changed pool header")
	}
	for index := range slots {
		if slots[index] != beforeSlots[index] {
			t.Fatalf("rejected mutation changed slot %d", index)
		}
	}
	visits, problem := core.abort()
	if problem.failed() || visits != 2 || core.pool.mutationEpoch != ^uint64(0) {
		t.Fatalf("Abort at exact reserved headroom = visits %d epoch %d error %+v", visits, core.pool.mutationEpoch, problem)
	}
	if problem = core.validateHandle(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("exact-headroom Abort retained handle: %+v", problem)
	}
}

func TestPrivateWriterTransactionCheckpointPreservesAbortHeadroom(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 2)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 2), nil, nil)
	if _, problem := core.begin([16]byte{3}); problem.failed() {
		t.Fatal(problem)
	}
	scope, poolProblem := core.pool.reserveScope(1)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	core.pool.mutationEpoch = ^uint64(0) - core.pool.abortMutationReserve - 2
	checkpoint, poolProblem := core.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if _, poolProblem = core.pool.bindPage(
		checkpoint, scope, 7, privatePageReclaimed,
	); poolProblem.failed() || core.pool.checkpointCleanup != 1 ||
		core.pool.mutationEpoch != ^uint64(0)-core.pool.abortMutationReserve-1 {
		t.Fatalf(
			"checkpoint exact forward+cleanup = epoch %d cleanup %d error %+v",
			core.pool.mutationEpoch, core.pool.checkpointCleanup, poolProblem,
		)
	}
	beforePool := sealFreeBitmapReservationPool(&core.pool)
	beforeSlots := append([]privatePagePoolSlot(nil), slots...)
	if _, poolProblem = core.pool.claimLowestInScope(
		checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap,
	); poolProblem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("checkpoint mutation consumed cleanup/Abort reserve: %+v", poolProblem)
	}
	if !beforePool.matches(&core.pool) {
		t.Fatal("rejected checkpoint mutation changed pool header")
	}
	for index := range slots {
		if slots[index] != beforeSlots[index] {
			t.Fatalf("rejected checkpoint mutation changed slot %d", index)
		}
	}
	if poolProblem = core.pool.rollbackCheckpointInScope(checkpoint, scope); poolProblem.failed() ||
		core.pool.mutationEpoch != ^uint64(0)-core.pool.abortMutationReserve {
		t.Fatalf("checkpoint cleanup did not preserve Abort reserve: epoch %d error %+v", core.pool.mutationEpoch, poolProblem)
	}
	if visits, problem := core.abort(); problem.failed() || visits != 2 ||
		core.pool.mutationEpoch != ^uint64(0) {
		t.Fatalf("Abort after terminal checkpoint cleanup = visits %d epoch %d error %+v", visits, core.pool.mutationEpoch, problem)
	}
}

func TestPrivateWriterTransactionAbortEpochPreflightPreservesAuthority(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 2)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 2), nil, nil)
	if _, problem := core.begin([16]byte{3}); problem.failed() {
		t.Fatal(problem)
	}
	scope := bindPrivateWriterTestScope(t, &core, 2)
	checkpoint, poolProblem := core.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	token, poolProblem := core.pool.claimLowestInScope(
		checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	beforePool := sealFreeBitmapReservationPool(&core.pool)
	beforeSlots := append([]privatePagePoolSlot(nil), slots...)
	beforeTarget := core.target
	beforeState := core.state
	core.abortEpoch = core.handleEpoch
	if _, problem := core.abort(); problem.code != privateWriterTransactionErrTransactionExhausted {
		t.Fatalf("invalid reserved abort epoch = %+v", problem)
	}
	if core.state != beforeState || core.target != beforeTarget || core.abortScrubbed ||
		!beforePool.matches(&core.pool) {
		t.Fatalf("Abort epoch preflight changed core/pool: %#v", core)
	}
	for index := range slots {
		if slots[index] != beforeSlots[index] {
			t.Fatalf("Abort epoch preflight changed slot %d", index)
		}
	}
	if _, tokenProblem := core.pool.validateToken(token); tokenProblem.failed() {
		t.Fatalf("Abort epoch preflight consumed page token: %+v", tokenProblem)
	}
	if checkpointProblem := core.pool.validateCheckpoint(checkpoint); checkpointProblem.failed() {
		t.Fatalf("Abort epoch preflight consumed checkpoint: %+v", checkpointProblem)
	}
}

func TestPrivateWriterTransactionAbortFailurePreservesAuthorityForRetry(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 2)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 2), nil, nil)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	scope := bindPrivateWriterTestScope(t, &core, 2)
	checkpoint, poolProblem := core.pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	token, poolProblem := core.pool.claimLowestInScope(
		checkpoint, scope, privatePageOwnerBitmap, privatePageBitmap,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	originalMutationEpoch := core.pool.mutationEpoch
	core.pool.mutationEpoch = ^uint64(0) - 1
	if _, problem = core.abort(); problem.code != privateWriterTransactionErrAbortIncomplete ||
		core.state != privateWriterTransactionAbortIncomplete {
		t.Fatalf("injected abort failure = state %d error %+v", core.state, problem)
	}
	core.pool.mutationEpoch = originalMutationEpoch
	if _, tokenProblem := core.pool.validateToken(token); tokenProblem.failed() {
		t.Fatalf("failed abort consumed page authority: %+v", tokenProblem)
	}
	if checkpointProblem := core.pool.validateCheckpoint(checkpoint); checkpointProblem.failed() {
		t.Fatalf("failed abort consumed checkpoint authority: %+v", checkpointProblem)
	}
	visits, problem := core.abort()
	if problem.failed() || visits != 2 || core.state != privateWriterTransactionClean {
		t.Fatalf("retried abort = visits %d state %d error %+v", visits, core.state, problem)
	}
	if _, tokenProblem := core.pool.validateToken(token); tokenProblem.code != privatePagePoolErrCrossPool {
		t.Fatalf("successful abort retained page authority: %+v", tokenProblem)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("successful abort retained writer handle: %+v", problem)
	}
}

func TestPrivateWriterTransactionPreparedAbortAllocatesNothing(t *testing.T) {
	var slots [4]privatePagePoolSlot
	var validation [4]uint32
	var core privateWriterTransactionCore
	selected := privateWriterTestMeta()
	budget := privateWriterResourceBudget{
		maxHeapBytes: 1 << 20, maxPrivatePages: 4, maxFileGrowthPages: 4, maxOpenFiles: 4,
	}
	allocations := testing.AllocsPerRun(100, func() {
		if problem := initPrivateWriterTransactionCore(
			&core, selected, budget, slots[:], validation[:], nil, nil,
		); problem.failed() {
			panic(problem)
		}
		if _, problem := core.begin([16]byte{3}); problem.failed() {
			panic(problem)
		}
		if visits, problem := core.abort(); problem.failed() || visits != 4 {
			panic(problem)
		}
	})
	if allocations != 0 {
		t.Fatalf("prepared begin/abort allocations = %f", allocations)
	}
}

func failFirstPrivateWriterCleanup(
	obligation privateWriterCleanupObligation,
	authority *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError {
	authority.state++
	if authority.state == 1 {
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id,
			detail:       17,
		}
	}
	return privateWriterCleanupError{}
}

func TestPrivateWriterTransactionCleanupLedgerRetryDoesNotRescrub(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 3)
	obligations := make([]privateWriterCleanupObligation, 1)
	owners := make([]privateWriterCleanupOwner, 1)
	initPrivateWriterTestCore(
		t, &core, slots, make([]uint32, 3), obligations, owners,
	)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	if cleanupProblem := core.cleanup.append(
		privateWriterCleanupObligation{id: 7},
		privateWriterCleanupOwner{obligationID: 7},
	); cleanupProblem.failed() {
		t.Fatal(cleanupProblem)
	}
	visits, problem := core.abortWithCleanup(failFirstPrivateWriterCleanup)
	if visits != 0 || problem.code != privateWriterTransactionErrAbortIncomplete ||
		problem.cleanup.code != privateWriterCleanupErrExecutionFailed ||
		problem.cleanup.obligationID != 7 || problem.cleanup.detail != 17 ||
		!core.abortScrubbed || core.cleanup.length != 1 ||
		core.cleanup.owners[0].authority.state != 1 ||
		core.cleanup.owners[0].lastError != problem.cleanup ||
		core.cleanup.owners[0].provenClean {
		t.Fatalf("first cleanup = visits %d state %d ledger %#v error %+v", visits, core.state, core.cleanup, problem)
	}
	selectedBeforeRetry := core.selected
	if reinitProblem := initPrivateWriterTransactionCore(
		&core, privateWriterTestMeta(), core.resources.budget, slots, make([]uint32, 3),
		obligations, owners,
	); reinitProblem.code != privateWriterTransactionErrAbortIncomplete || core.selected != selectedBeforeRetry ||
		core.cleanup.length != 1 {
		t.Fatalf("cleanup bypass reinitialization = core %#v error %+v", core, reinitProblem)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("scrubbed handle remained valid: %+v", problem)
	}
	visits, problem = core.abortWithCleanup(failFirstPrivateWriterCleanup)
	if problem.failed() || visits != 3 || core.cleanup.length != 0 ||
		core.state != privateWriterTransactionClean || core.abortVisits != 3 {
		t.Fatalf("cleanup retry = visits %d state %d ledger %#v error %+v", visits, core.state, core.cleanup, problem)
	}
}

func TestPrivateWriterTransactionResourceReleaseRetryDoesNotRescrub(t *testing.T) {
	var core privateWriterTransactionCore
	slots := make([]privatePagePoolSlot, 2)
	initPrivateWriterTestCore(t, &core, slots, make([]uint32, 2), nil, nil)
	if _, problem := core.begin([16]byte{3}); problem.failed() {
		t.Fatal(problem)
	}
	usage := privateWriterResourceUsage{
		heapBytes: 1, privatePages: 1, fileGrowthPages: 1, openFiles: 1,
	}
	if problem := core.resources.acquire(usage); problem.failed() {
		t.Fatal(problem)
	}
	visits, problem := core.abort()
	if visits != 0 || problem.code != privateWriterTransactionErrAbortIncomplete ||
		problem.resource.code != privateWriterResourceErrInvalidState ||
		!core.abortScrubbed || core.abortVisits != 2 {
		t.Fatalf("resource-retaining abort = visits %d core %#v error %+v", visits, core, problem)
	}
	if resourceProblem := core.resources.release(usage); resourceProblem.failed() {
		t.Fatal(resourceProblem)
	}
	visits, problem = core.abort()
	if problem.failed() || visits != 2 || core.abortVisits != 2 ||
		core.state != privateWriterTransactionClean {
		t.Fatalf("resource-release retry = visits %d core %#v error %+v", visits, core, problem)
	}
}

func TestPrivateWriterTransactionCleanReinitCannotChangeBudget(t *testing.T) {
	var core privateWriterTransactionCore
	var slots [1]privatePagePoolSlot
	var validation [1]uint32
	selected := initPrivateWriterTestCore(
		t, &core, slots[:], validation[:], nil, nil,
	)
	beforeBudget := core.resources.budget
	if problem := initPrivateWriterTransactionCore(
		&core, selected,
		privateWriterResourceBudget{
			maxHeapBytes:       beforeBudget.maxHeapBytes + 1,
			maxPrivatePages:    beforeBudget.maxPrivatePages,
			maxFileGrowthPages: beforeBudget.maxFileGrowthPages,
			maxOpenFiles:       beforeBudget.maxOpenFiles,
		},
		slots[:], validation[:], nil, nil,
	); problem.code != privateWriterTransactionErrInvalidArgument {
		t.Fatalf("changed-budget reinit = %+v", problem)
	}
	if core.resources.budget != beforeBudget || core.state != privateWriterTransactionClean {
		t.Fatalf("changed-budget reinit mutated core: %#v", core)
	}
}
