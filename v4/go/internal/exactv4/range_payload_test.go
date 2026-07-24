package exactv4

import "testing"

func buildRangePayloadV4(
	t *testing.T,
	bornTxn uint64,
) (rangeTreeStaging[IPv4], rangeTreeStagedResult) {
	t.Helper()
	pages := make([]rangeTreeStagingPage, 1)
	staging, err := newRangeTreeStaging[IPv4](pages, bornTxn, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv4]
	builder, err := workspace.begin(bornTxn, ValueKindDirect, staging.logicalPageCount())
	if err != nil {
		t.Fatal(err)
	}
	if err = builder.push(&staging, rangeTreeBuildRecordV4(1)); err != nil {
		t.Fatal(err)
	}
	built, err := builder.finish(&staging)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := staging.finish(built)
	if err != nil {
		t.Fatal(err)
	}
	return staging, staged
}

func TestRangePayloadUsesLowestReservedSlotAndSurvivesBitmapFinalization(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	if attachment.cow.availableLen == 0 {
		t.Fatal("fixture has no payload slot")
	}
	wantSlot := attachment.cow.availableSlots[attachment.cow.availableLen-1]
	wantPage := attachment.cow.pool.slots[wantSlot].pageNumber
	assignments := make([]rangeTreePhysicalAssignment, 1)
	slots := make([]rangeTreePayloadReservationSlot, 1)
	terminal := make([]privateWriterProducedTerminalPage, 1)
	materialized, problem := stageRangePayload(
		&attachment,
		&staging,
		staged,
		&rangeTreePayloadScratch{
			assignments: assignments, slots: slots, terminalPages: terminal,
		},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if materialized.rootPage != wantPage || terminal[0].pageNumber != wantPage ||
		terminal[0].owner != privatePageOwnerRange || terminal[0].origin != privatePageRange ||
		slots[0].slot != wantSlot {
		t.Fatalf("materialized payload = %+v/%+v/%+v", materialized, terminal[0], slots[0])
	}
	if attachment.cow.payloadPageBudget != 1 {
		t.Fatalf("remaining payload budget = %d, want 1", attachment.cow.payloadPageBudget)
	}
	if slot := &attachment.cow.pool.slots[wantSlot]; slot.state != privatePageInUse ||
		slot.owner != privatePageOwnerRange || slot.origin != privatePageRange ||
		slot.pageNumber != wantPage {
		t.Fatalf("range slot = %+v", slot)
	}

	_ = finalizeLateBitmapAttachment(t, &attachment)
	index, found := attachment.cow.pool.slotIndex(wantPage)
	if !found {
		t.Fatal("bitmap finalization released the range payload page")
	}
	if slot := &attachment.cow.pool.slots[index]; slot.state != privatePageInUse ||
		slot.owner != privatePageOwnerRange || slot.origin != privatePageRange {
		t.Fatalf("range slot after finalization = %+v", slot)
	}
}

func TestRangePayloadHotPathAllocatesNothingAfterFixedSetup(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	assignments := make([]rangeTreePhysicalAssignment, 1)
	slots := make([]rangeTreePayloadReservationSlot, 1)
	terminal := make([]privateWriterProducedTerminalPage, 1)
	pool := attachment.cow.pool
	baselinePool := *pool
	baselineSlots := append([]privatePagePoolSlot(nil), pool.slots...)
	baselineCOW := attachment.cow
	baselineBindings := append([]bitmapCOWArenaBinding(nil), attachment.cow.arenaBindings...)
	baselineIndexNodes := append([]bitmapCOWIndexNode(nil), attachment.cow.indexNodes...)
	baselineAvailable := append([]int(nil), attachment.cow.availableSlots...)
	restore := func() {
		copy(pool.slots, baselineSlots)
		*pool = baselinePool
		copy(attachment.cow.arenaBindings, baselineBindings)
		copy(attachment.cow.indexNodes, baselineIndexNodes)
		copy(attachment.cow.availableSlots, baselineAvailable)
		attachment.cow = baselineCOW
		clear(assignments)
		clear(slots)
		clear(terminal)
	}
	allocations := testing.AllocsPerRun(100, func() {
		restore()
		if _, problem := stageRangePayload(
			&attachment,
			&staging,
			staged,
			&rangeTreePayloadScratch{
				assignments: assignments, slots: slots, terminalPages: terminal,
			},
		); problem.failed() {
			panic(problem)
		}
		restore()
	})
	restore()
	if allocations != 0 {
		t.Fatalf("allocations = %v, want zero", allocations)
	}
}

func TestRangePayloadRejectsDirtyTerminalScratchWithoutMutation(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	assignments := make([]rangeTreePhysicalAssignment, 1)
	slots := make([]rangeTreePayloadReservationSlot, 1)
	terminal := make([]privateWriterProducedTerminalPage, 1)
	terminal[0].pageNumber = 99
	before := snapshotLateBitmapLive(t, &attachment)
	_, problem := stageRangePayload(
		&attachment,
		&staging,
		staged,
		&rangeTreePayloadScratch{
			assignments: assignments, slots: slots, terminalPages: terminal,
		},
	)
	if problem.code != rangeTreePayloadStageErrPreMutationStaging || terminal[0].pageNumber != 99 {
		t.Fatalf("dirty terminal result = %+v/%+v", problem, terminal[0])
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
}

func TestRangePayloadRejectsWrongTransactionBeforeMutation(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn+1)
	assignments := make([]rangeTreePhysicalAssignment, 1)
	slots := make([]rangeTreePayloadReservationSlot, 1)
	terminal := make([]privateWriterProducedTerminalPage, 1)
	before := snapshotLateBitmapLive(t, &attachment)
	_, problem := stageRangePayload(
		&attachment,
		&staging,
		staged,
		&rangeTreePayloadScratch{
			assignments: assignments, slots: slots, terminalPages: terminal,
		},
	)
	if problem.code != rangeTreePayloadStageErrTransaction {
		t.Fatalf("transaction mismatch = %+v", problem)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
}

func TestRangePayloadEmptyTreeIsNoOp(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	pages := make([]rangeTreeStagingPage, 1)
	staging, err := newRangeTreeStaging[IPv6](pages, attachment.cow.pendingTxn, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv6]
	builder, err := workspace.begin(attachment.cow.pendingTxn, ValueKindDirect, staging.logicalPageCount())
	if err != nil {
		t.Fatal(err)
	}
	built, err := builder.finish(&staging)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := staging.finish(built)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotLateBitmapLive(t, &attachment)
	materialized, problem := stageRangePayload(
		&attachment,
		&staging,
		staged,
		&rangeTreePayloadScratch{},
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if materialized.rootPage != 0 || materialized.pageCount != 0 || attachment.cow.payloadPageBudget != 2 {
		t.Fatalf("empty materialization = %+v, remaining budget %d", materialized, attachment.cow.payloadPageBudget)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
}

func TestRangePayloadPreparedClaimRejectsStaleSlotEpochWithoutMutation(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	slot := attachment.cow.availableSlots[attachment.cow.availableLen-1]
	info, poolProblem := attachment.cow.pool.slotInfo(slot)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	checkpoint, poolProblem := attachment.cow.pool.preflightCheckpoint()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = attachment.cow.pool.beginCheckpointPrepared(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	before := snapshotLateBitmapLive(t, &attachment)
	var bytes [PageSize]byte
	poolProblem = attachment.cow.pool.claimSlotWithOwnerAndBytesInScopeForCheckpointPrepared(
		checkpoint, attachment.scope, slot, info.epoch+1, info.pageNumber,
		privatePageOwnerRange, privatePageRange, &bytes,
	)
	if poolProblem.code != privatePagePoolErrInvalidState {
		t.Fatalf("stale epoch claim = %+v", poolProblem)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
	if poolProblem = attachment.cow.pool.rollbackCheckpointInScope(checkpoint, attachment.scope); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
}

func TestRangePayloadRejectsNonascendingAllocatorOrderWithoutMutation(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	if attachment.cow.availableLen < 2 {
		t.Fatal("fixture has fewer than two payload slots")
	}
	lowIndex := attachment.cow.availableLen - 1
	middleIndex := attachment.cow.availableLen - 2
	attachment.cow.availableSlots[lowIndex], attachment.cow.availableSlots[middleIndex] =
		attachment.cow.availableSlots[middleIndex], attachment.cow.availableSlots[lowIndex]
	pages := make([]rangeTreeStagingPage, 1)
	staging, err := newRangeTreeStaging[IPv4](pages, attachment.cow.pendingTxn, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotLateBitmapLive(t, &attachment)
	assignments := make([]rangeTreePhysicalAssignment, 2)
	slots := make([]rangeTreePayloadReservationSlot, 2)
	terminal := make([]privateWriterProducedTerminalPage, 2)
	_, problem := stageRangePayload(
		&attachment,
		&staging,
		rangeTreeStagedResult{logicalRoot: 2, pageCount: 2},
		&rangeTreePayloadScratch{
			assignments: assignments, slots: slots, terminalPages: terminal,
		},
	)
	if problem.code != rangeTreePayloadStageErrOrder || problem.page >= problem.previous {
		t.Fatalf("nonascending order = %+v", problem)
	}
	if terminal[0] != (privateWriterProducedTerminalPage{}) || terminal[1] != (privateWriterProducedTerminalPage{}) {
		t.Fatalf("nonascending order changed terminal output = %+v", terminal)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
}
