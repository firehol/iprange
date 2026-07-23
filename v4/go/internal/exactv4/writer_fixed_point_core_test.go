package exactv4

import "testing"

type fixedPointCoreFixture struct {
	core        privateWriterTransactionCore
	handle      privateWriterTransactionHandle
	source      *cowSparsePages
	records     []privateWriterSealedBitmapWorkUnitRecord
	slotRecords []int
	result      freeBitmapFinalizationResult
}

func newFixedPointCoreFixture(t *testing.T) *fixedPointCoreFixture {
	t.Helper()
	fixture := &fixedPointCoreFixture{
		source: &cowSparsePages{
			pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)},
		},
		records:     make([]privateWriterSealedBitmapWorkUnitRecord, 4),
		slotRecords: make([]int, 32),
	}
	selected := Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		DatabaseID:    [16]byte{1},
		TxnID:         1,
		CommitNonce:   [16]byte{2},
		PageCount:     20,
	}
	slots := make([]privatePagePoolSlot, 32)
	if problem := initPrivateWriterTransactionCore(
		&fixture.core,
		selected,
		privateWriterResourceBudget{
			maxHeapBytes: 1 << 20, maxPrivatePages: uint64(len(slots)),
			maxFileGrowthPages: uint64(len(slots)), maxOpenFiles: 4,
		},
		slots,
		make([]uint32, len(slots)),
		make([]privateWriterCleanupObligation, 2),
		make([]privateWriterCleanupOwner, 2),
	); problem.failed() {
		t.Fatal(problem)
	}
	var transactionProblem privateWriterTransactionError
	fixture.handle, transactionProblem = fixture.core.begin([16]byte{3})
	if transactionProblem.failed() {
		t.Fatal(transactionProblem)
	}
	if transactionProblem = fixture.core.startFixedPoint(
		fixture.handle, fixture.source, 2,
		fixture.records, fixture.slotRecords,
	); transactionProblem.failed() {
		t.Fatal(transactionProblem)
	}

	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	planner, bitmapProblem := newFreeBitmapReservationPlanner(
		fixture.source, 1, 20, 2, 2, storage.buffers(),
	)
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	capacity, bitmapProblem := planner.planCapacity()
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	scope, poolProblem := fixture.core.pool.reserveScope(capacity.privatePages)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	attachment, bitmapProblem := capacity.attach(&fixture.core.pool, scope)
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	proof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, bitmapProblem = attachment.bind(&proof); bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	fixture.result = finalizeLateBitmapAttachment(t, &attachment)
	return fixture
}

func TestWriterCoreOwnsFixedPointFinalFenceAndAbort(t *testing.T) {
	fixture := newFixedPointCoreFixture(t)
	source, problem := fixture.core.fixedPointSource(fixture.handle)
	if problem.failed() || source == nil {
		t.Fatalf("source = %p %#v", source, problem)
	}
	if problem = fixture.core.acceptFixedPointFinalized(
		fixture.handle, 1, fixture.result,
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.core.preflightCommit(
		fixture.handle,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("unconsumed final predecessor = %#v", problem)
	}
	root, pageCount, problem := fixture.core.finishFixedPoint(fixture.handle)
	if problem.failed() {
		t.Fatal(problem)
	}
	if root != fixture.result.output.root ||
		pageCount != fixture.result.output.pageCount {
		t.Fatalf("final root/page count = %d/%d", root, pageCount)
	}
	if problem = fixture.core.preflightCommit(
		fixture.handle,
	); problem.code != privateWriterTransactionErrAbortRequired ||
		fixture.core.state != privateWriterTransactionAbortRequired ||
		!fixture.core.pool.abortRequired {
		t.Fatalf("generic finalization scope reached commit = %#v", problem)
	}
	if _, problem = fixture.core.abort(); problem.failed() {
		t.Fatal(problem)
	}
	if fixture.core.fixedPointActive || fixture.core.fixedPointFinished ||
		fixture.core.fixedPointCoordinator.self != nil {
		t.Fatal("abort retained fixed-point authority")
	}
	for index := range fixture.records {
		if fixture.records[index].active ||
			fixture.records[index].workUnit != 0 ||
			fixture.records[index].output.pool != nil {
			t.Fatalf("record %d survived abort", index)
		}
	}
	for index, record := range fixture.slotRecords {
		if record != 0 {
			t.Fatalf("slot record %d survived abort: %d", index, record)
		}
	}
	if status := source.checkAccessStatus(); !status.failed() {
		t.Fatal("saved draft source survived abort")
	}
}

func TestWriterCoreFixedPointNeutralFailureRetries(t *testing.T) {
	fixture := newFixedPointCoreFixture(t)
	problem := fixture.core.acceptFixedPointFinalized(
		fixture.handle, 0, fixture.result,
	)
	if problem.code != privateWriterTransactionErrFixedPoint ||
		problem.fixedPoint.code != privateWriterFixedPointErrInvalidArgument ||
		fixture.core.state != privateWriterTransactionPending ||
		fixture.core.pool.abortRequired {
		t.Fatalf("neutral failure = state %d %#v", fixture.core.state, problem)
	}
	if problem = fixture.core.acceptFixedPointFinalized(
		fixture.handle, 1, fixture.result,
	); problem.failed() {
		t.Fatalf("retry = %#v", problem)
	}
}

func TestWriterCoreFixedPointPostConsumeFailureRequiresAbort(t *testing.T) {
	fixture := newFixedPointCoreFixture(t)
	liveSlot := fixture.result.output.bindings[0].poolSlot
	fixture.slotRecords[liveSlot] = 1
	problem := fixture.core.acceptFixedPointFinalized(
		fixture.handle, 1, fixture.result,
	)
	if problem.code != privateWriterTransactionErrAbortRequired ||
		fixture.core.state != privateWriterTransactionAbortRequired ||
		!fixture.core.pool.abortRequired ||
		!fixture.core.fixedPointCoordinator.predecessorUsed {
		t.Fatalf("post-consume failure = state %d %#v", fixture.core.state, problem)
	}
	if commitProblem := fixture.core.preflightCommit(
		fixture.handle,
	); commitProblem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("commit after post-consume failure = %#v", commitProblem)
	}
	if _, abortProblem := fixture.core.abort(); abortProblem.failed() {
		t.Fatal(abortProblem)
	}
}

func TestWriterCoreFixedPointFailureMappingUsesWriterHandle(t *testing.T) {
	fixture := newFixedPointCoreFixture(t)
	stale := fixture.handle
	stale.epoch++
	fixedProblem := privateWriterFixedPointError{
		code: privateWriterFixedPointErrSource,
	}
	if problem := fixture.core.fixedPointOperationFailed(
		stale, fixedProblem, true,
	); problem.code != privateWriterTransactionErrStaleHandle ||
		fixture.core.state != privateWriterTransactionPending {
		t.Fatalf("stale failure mapper = state %d %#v", fixture.core.state, problem)
	}
	if problem := fixture.core.fixedPointOperationFailed(
		fixture.handle, fixedProblem, false,
	); problem.code != privateWriterTransactionErrFixedPoint ||
		fixture.core.state != privateWriterTransactionPending {
		t.Fatalf("neutral failure mapper = state %d %#v", fixture.core.state, problem)
	}
	if problem := fixture.core.fixedPointOperationFailed(
		fixture.handle, fixedProblem, true,
	); problem.code != privateWriterTransactionErrAbortRequired ||
		fixture.core.state != privateWriterTransactionAbortRequired ||
		!fixture.core.pool.abortRequired {
		t.Fatalf("mutated failure mapper = state %d %#v", fixture.core.state, problem)
	}
}
