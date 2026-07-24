package exactv4

import (
	"errors"
	"testing"
)

func requireRangeTreePoolSinkError(t *testing.T, err error, want rangeTreePoolSinkErrorCode) *rangeTreePoolSinkError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-tree pool sink error %d", want)
	}
	var got *rangeTreePoolSinkError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangeTreePoolSinkError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-tree pool sink code = %d, want %d", got.code, want)
	}
	return got
}

func TestRangeTreePoolSinkBuildsIntoActualPrivatePages(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(3, privatePageCommittedFree),
		newPrivatePageSlot(5, privatePageCommittedFree),
		newPrivatePageSlot(7, privatePageCommittedFree),
	}
	pool := testPrivatePagePool(t, slots, 20, 20)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	sink, err := newRangeTreePoolSink(pool, checkpoint, 2)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv4]
	builder, err := workspace.begin(2, ValueKindDirect, 20)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value <= rangeLeafCapacity[IPv4](); value++ {
		if err = builder.push(&sink, rangeTreeBuildRecordV4(uint32(value))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(&sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage != 7 || result.rootLevel != 1 || pool.available() != 0 {
		t.Fatalf("result/available = %+v/%d", result, pool.available())
	}
	for index, wantType := range []PageType{PageTypeRangeLeaf, PageTypeRangeLeaf, PageTypeRangeBranch} {
		slot := &pool.slots[index]
		if slot.owner != privatePageOwnerRange || slot.origin != privatePageRange ||
			slot.pendingTxn != 2 || slot.state != privatePageInUse {
			t.Fatalf("slot %d ownership = %+v", index, slot)
		}
		header, decodeErr := DecodePageHeader(slot.bytes[:], 2)
		if decodeErr != nil || header.PageType != wantType || header.Aux != uint32(AddressFamilyIPv4) ||
			!VerifyPageCRC32C(slot.bytes[:]) {
			t.Fatalf("slot %d header = %+v/%v", index, header, decodeErr)
		}
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if pool.available() != 3 {
		t.Fatalf("available after rollback = %d", pool.available())
	}
	for index := range pool.slots {
		slot := &pool.slots[index]
		if slot.state != privatePageAvailable || slot.owner != privatePageOwnerNone ||
			slot.origin != privatePageOriginNone || slot.bytes != ([PageSize]byte{}) {
			t.Fatalf("slot %d survived rollback: %+v", index, slot)
		}
	}
}

func TestRangeTreePoolSinkRejectsWrongTransactionBeforeClaiming(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(3, privatePageCommittedFree)}
	pool := testPrivatePagePool(t, slots, 20, 20)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	_, err := newRangeTreePoolSink(pool, checkpoint, 3)
	sinkProblem := requireRangeTreePoolSinkError(t, err, rangeTreePoolSinkErrPendingTransaction)
	if sinkProblem.requested != 3 || sinkProblem.poolPending != 2 || pool.available() != 1 {
		t.Fatalf("mismatch/available = %+v/%d", sinkProblem, pool.available())
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestRangeTreePoolSinkRejectsAStaleCheckpointBeforeClaiming(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(3, privatePageCommittedFree)}
	pool := testPrivatePagePool(t, slots, 20, 20)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	sink, err := newRangeTreePoolSink(pool, checkpoint, 2)
	if err != nil {
		t.Fatal(err)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	var page [PageSize]byte
	_, err = sink.writeRangePage(&page)
	sinkProblem := requireRangeTreePoolSinkError(t, err, rangeTreePoolSinkErrPool)
	if sinkProblem.poolProblem.code != privatePagePoolErrCheckpointInactive || pool.available() != 1 {
		t.Fatalf("stale checkpoint/available = %+v/%d", sinkProblem.poolProblem, pool.available())
	}
}

func TestRangeTreePoolSinkLeavesPartialBuildForCheckpointRollback(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(3, privatePageCommittedFree),
		newPrivatePageSlot(5, privatePageCommittedFree),
	}
	pool := testPrivatePagePool(t, slots, 20, 20)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	sink, err := newRangeTreePoolSink(pool, checkpoint, 2)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv4]
	builder, err := workspace.begin(2, ValueKindDirect, 20)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value <= rangeLeafCapacity[IPv4](); value++ {
		if err = builder.push(&sink, rangeTreeBuildRecordV4(uint32(value))); err != nil {
			t.Fatal(err)
		}
	}
	_, err = builder.finish(&sink)
	buildProblem := requireRangeTreeBuildCode(t, err, rangeTreeBuildErrSink)
	sinkProblem := requireRangeTreePoolSinkError(t, buildProblem, rangeTreePoolSinkErrPool)
	if sinkProblem.poolProblem.code != privatePagePoolErrBudget || pool.available() != 0 {
		t.Fatalf("capacity/available = %+v/%d", sinkProblem.poolProblem, pool.available())
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if pool.available() != 2 {
		t.Fatalf("available after rollback = %d", pool.available())
	}
	for index := range pool.slots {
		slot := &pool.slots[index]
		if slot.state != privatePageAvailable || slot.owner != privatePageOwnerNone ||
			slot.origin != privatePageOriginNone || slot.bytes != ([PageSize]byte{}) {
			t.Fatalf("slot %d survived rollback: %+v", index, slot)
		}
	}
}

func TestRangeTreePoolSinkHotPathAllocatesNothingAfterFixedSetup(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(3, privatePageCommittedFree)}
	pool := testPrivatePagePool(t, slots, 20, 20)
	var workspace rangeTreeBuildWorkspace[IPv4]
	var sink rangeTreePoolSink
	allocations := testing.AllocsPerRun(100, func() {
		checkpoint, problem := pool.begin()
		if problem.failed() {
			panic(problem)
		}
		var err error
		sink, err = newRangeTreePoolSink(pool, checkpoint, 2)
		if err != nil {
			panic(err)
		}
		builder, err := workspace.begin(2, ValueKindDirect, 20)
		if err != nil {
			panic(err)
		}
		if err = builder.push(&sink, rangeTreeBuildRecordV4(1)); err != nil {
			panic(err)
		}
		if _, err = builder.finish(&sink); err != nil {
			panic(err)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 || pool.available() != 1 {
		t.Fatalf("allocations/available = %v/%d", allocations, pool.available())
	}
}
