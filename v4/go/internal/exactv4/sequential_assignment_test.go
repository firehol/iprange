package exactv4

import (
	"errors"
	"testing"
)

func assignmentTestPool(t *testing.T, count int) *privatePagePool {
	t.Helper()
	slots := make([]privatePagePoolSlot, count)
	for index := range slots {
		slots[index] = newPrivatePageSlot(uint32(index+3), privatePageCommittedFree)
	}
	return testPrivatePagePool(t, slots, 400, 400)
}

func requireSequentialAssignmentCode(t *testing.T, err error, want sequentialAssignmentErrorCode) *sequentialAssignmentError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected sequential-assignment error %d", want)
	}
	var got *sequentialAssignmentError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *sequentialAssignmentError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("sequential-assignment code = %d, want %d", got.code, want)
	}
	return got
}

func singleLeafAssignmentRecords[K rangeKey[K]](t *testing.T, sink *rangeTreeBuildTestSink) []rangeRecord[K] {
	t.Helper()
	if len(sink.pages) != 1 {
		t.Fatalf("range pages = %d, want one leaf", len(sink.pages))
	}
	var key K
	leaf, err := openRangeLeaf[K](sink.pages[0].page[:], 2, key.family(), ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]rangeRecord[K], leaf.count)
	for index := range records {
		records[index], err = leaf.record(index)
		if err != nil {
			t.Fatal(err)
		}
	}
	return records
}

func sequentialAssignmentTreeImage(t *testing.T, pool *privatePagePool, result rangeTreeBuildResult) []byte {
	t.Helper()
	index, found := pool.slotIndex(result.rootPage)
	if !found {
		t.Fatalf("range root %d was not claimed from the private pool", result.rootPage)
	}
	meta := emptyDirectMeta(2)
	meta.AddressFamily = AddressFamilyIPv4
	meta.ValueKind = ValueKindDirect
	meta.PageCount = pool.pendingPageCount
	meta.RangeRoot = result.rootPage
	meta.RangeRecordCount = result.recordCount
	data := make([]byte, int(meta.PageCount)*PageSize)
	page := meta.EncodePage()
	copy(data[:PageSize], page[:])
	copy(data[PageSize:2*PageSize], page[:])
	copy(data[int(result.rootPage)*PageSize:], pool.slots[index].bytes[:])
	return data
}

func TestSequentialAssignmentPreservesUncoveredSidesInArrivalOrder(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 8, 10_000, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(10, 30, 1); err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(15, 20, 2); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	result, err := engine.buildFinalTree(&treeWorkspace, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage != 2 || result.recordCount != 3 || !workspace.clean() {
		t.Fatalf("result/workspace = %+v/%+v", result, workspace)
	}
	got := singleLeafAssignmentRecords[IPv4](t, sink)
	want := []rangeRecord[IPv4]{
		{from: 10, to: 14, value: 1},
		{from: 15, to: 20, value: 2},
		{from: 21, to: 30, value: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("record[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentAcceptsEmptyInputWithoutClaimingAPage(t *testing.T) {
	pool := assignmentTestPool(t, 2)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	workspace := newSequentialAssignmentWorkspace(nil)
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 0, 1_000, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	result, err := engine.buildFinalTree(&treeWorkspace, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage != 0 || result.recordCount != 0 || len(sink.pages) != 0 || pool.available() != 2 || !workspace.clean() {
		t.Fatalf("empty result = %+v, pages=%d available=%d workspace=%+v", result, len(sink.pages), pool.available(), workspace)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentBuildsTheFinalTreeInActualPoolPages(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 4, 10_000, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(10, 20, 7); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink, err := newRangeTreePoolSink(pool, checkpoint, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.buildFinalTree(&treeWorkspace, &sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage == 0 || !workspace.clean() {
		t.Fatalf("result/workspace = %+v/%+v", result, workspace)
	}
	index, found := pool.slotIndex(result.rootPage)
	if !found {
		t.Fatalf("final root %d not claimed from pool", result.rootPage)
	}
	slot := pool.slots[index]
	if slot.owner != privatePageOwnerRange || slot.origin != privatePageRange || slot.state != privatePageInUse {
		t.Fatalf("final root ownership = %+v", slot)
	}
	header, decodeErr := DecodePageHeader(slot.bytes[:], 2)
	if decodeErr != nil || header.PageType != PageTypeRangeLeaf || header.Aux != uint32(AddressFamilyIPv4) {
		t.Fatalf("final root header = %+v/%v", header, decodeErr)
	}
	tree, err := openImmutableRangeTree[IPv4](sequentialAssignmentTreeImage(t, pool, result))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record != (rangeRecord[IPv4]{from: 10, to: 20, value: 7}) {
		t.Fatalf("reopened range = %#v/%t/%v", record, found, err)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentAllowsDirectZeroAndRejectsMembershipZeroBeforeClaim(t *testing.T) {
	t.Run("direct zero", func(t *testing.T) {
		pool := assignmentTestPool(t, 4)
		checkpoint, problem := pool.begin()
		if problem.failed() {
			t.Fatal(problem)
		}
		var pageSlots [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(pageSlots[:])
		engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 2, 1_000, 1_000)
		if err != nil {
			t.Fatal(err)
		}
		if err = engine.assign(4, 5, 0); err != nil {
			t.Fatal(err)
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		sink := newRangeTreeBuildTestSink()
		if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
			t.Fatal(err)
		}
		got := singleLeafAssignmentRecords[IPv4](t, sink)
		if len(got) != 1 || got[0] != (rangeRecord[IPv4]{from: 4, to: 5, value: 0}) {
			t.Fatalf("direct zero records = %#v", got)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	})

	t.Run("membership zero", func(t *testing.T) {
		pool := assignmentTestPool(t, 4)
		checkpoint, problem := pool.begin()
		if problem.failed() {
			t.Fatal(problem)
		}
		var pageSlots [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(pageSlots[:])
		engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindMembership, 2, 1_000, 1_000)
		if err != nil {
			t.Fatal(err)
		}
		requireSequentialAssignmentCode(t, engine.assign(4, 5, 0), sequentialAssignmentErrMembershipValueZero)
		if pool.available() != 4 || !workspace.clean() {
			t.Fatalf("membership-zero claimed private state: available=%d workspace=%+v", pool.available(), workspace)
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		requireSequentialAssignmentCode(t, func() error {
			_, buildErr := engine.buildFinalTree(&treeWorkspace, newRangeTreeBuildTestSink())
			return buildErr
		}(), sequentialAssignmentErrFailed)
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
	})
}

func TestSequentialAssignmentClearRemovesOnlyItsArrivalInterval(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 4, 10_000, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(10, 30, 1); err != nil {
		t.Fatal(err)
	}
	if err = engine.clear(15, 20); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
		t.Fatal(err)
	}
	got := singleLeafAssignmentRecords[IPv4](t, sink)
	want := []rangeRecord[IPv4]{
		{from: 10, to: 14, value: 1},
		{from: 21, to: 30, value: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("record[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentCoalescesAdjacentFinalValues(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 4, 10_000, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(10, 20, 7); err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(21, 30, 7); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
		t.Fatal(err)
	}
	got := singleLeafAssignmentRecords[IPv4](t, sink)
	want := []rangeRecord[IPv4]{{from: 10, to: 30, value: 7}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentMatchesSmallPerAddressArrivalOrderOracle(t *testing.T) {
	type expectedValue struct {
		set   bool
		value uint32
	}

	pool := assignmentTestPool(t, 64)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	workspace := newSequentialAssignmentWorkspace(make([]sequentialAssignmentPage, 32))
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 128, 1_000_000, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	var want [256]expectedValue
	state := uint32(0x9e3779b9)
	for step := 0; step < 128; step++ {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		from, to := int(state&0xff), int(state>>8&0xff)
		if from > to {
			from, to = to, from
		}
		if state&0x10000 != 0 {
			err = engine.clear(IPv4(from), IPv4(to))
			for address := from; address <= to; address++ {
				want[address] = expectedValue{}
			}
		} else {
			value := state >> 17 & 3
			err = engine.assign(IPv4(from), IPv4(to), value)
			for address := from; address <= to; address++ {
				want[address] = expectedValue{set: true, value: value}
			}
		}
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
		t.Fatal(err)
	}
	got := [256]expectedValue{}
	for _, record := range singleLeafAssignmentRecords[IPv4](t, sink) {
		if record.to > 255 {
			t.Fatalf("unexpected range outside oracle domain: %#v", record)
		}
		for address := record.from; address <= record.to; address++ {
			got[address] = expectedValue{set: true, value: record.value}
		}
	}
	for address := range want {
		if got[address] != want[address] {
			t.Fatalf("address %d = %#v, want %#v", address, got[address], want[address])
		}
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentHandlesFullIPv6SpaceWithoutWrap(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [3]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv6](pool, checkpoint, &workspace, 2, ValueKindDirect, 4, 100_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	maximum := IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}
	if err = engine.assign(IPv6{}, maximum, 1); err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(maximum, maximum, 2); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv6]
	sink := newRangeTreeBuildTestSink()
	if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
		t.Fatal(err)
	}
	got := singleLeafAssignmentRecords[IPv6](t, sink)
	want := []rangeRecord[IPv6]{
		{from: IPv6{}, to: IPv6{Hi: ^uint64(0), Lo: ^uint64(0) - 1}, value: 1},
		{from: maximum, to: maximum, value: 2},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("full IPv6 records = %#v, want %#v", got, want)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestSequentialAssignmentPageExhaustionRollsBackWholeDraft(t *testing.T) {
	pool := assignmentTestPool(t, 8)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [1]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv6](pool, checkpoint, &workspace, 2, ValueKindDirect, 2, 100_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	requireSequentialAssignmentCode(t, engine.assign(IPv6FromHalves(0, 1), IPv6FromHalves(0, 1), 1), sequentialAssignmentErrWorkspacePageLimit)
	if pool.available() >= 8 {
		t.Fatal("expected a partial private draft before whole-checkpoint rollback")
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv6]
	requireSequentialAssignmentCode(t, func() error {
		_, err := engine.buildFinalTree(&treeWorkspace, newRangeTreeBuildTestSink())
		return err
	}(), sequentialAssignmentErrFailed)
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	workspace.discardAfterRollback()
	if pool.available() != 8 || !workspace.clean() {
		t.Fatalf("rollback/workspace = %d/%+v", pool.available(), workspace)
	}
}

func TestSequentialAssignmentWorkBudgetRollsBackWholeDraft(t *testing.T) {
	pool := assignmentTestPool(t, 4)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	var pageSlots [1]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 2, 1, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	requireSequentialAssignmentCode(t, engine.assign(10, 20, 1), sequentialAssignmentErrWorkBudget)
	if pool.available() >= 4 {
		t.Fatal("expected a private page claim before the bounded-work failure")
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	requireSequentialAssignmentCode(t, func() error {
		_, buildErr := engine.buildFinalTree(&treeWorkspace, newRangeTreeBuildTestSink())
		return buildErr
	}(), sequentialAssignmentErrFailed)
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	workspace.discardAfterRollback()
	if pool.available() != 4 || !workspace.clean() {
		t.Fatalf("rollback/workspace = %d/%+v", pool.available(), workspace)
	}
}

func TestSequentialAssignmentNestedWorkScalesLinearly(t *testing.T) {
	run := func(t *testing.T, count int) uint64 {
		t.Helper()
		pool := assignmentTestPool(t, 128)
		checkpoint, problem := pool.begin()
		if problem.failed() {
			t.Fatal(problem)
		}
		var pageSlots [64]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(pageSlots[:])
		engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, uint64(count), 1_000_000, 1_000_000)
		if err != nil {
			t.Fatal(err)
		}
		for value := 0; value < count; value++ {
			if err = engine.assign(IPv4(value), IPv4(^uint32(0)-uint32(value)), uint32(value&1)); err != nil {
				t.Fatal(err)
			}
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		sink := newRangeTreeBuildTestSink()
		if _, err = engine.buildFinalTree(&treeWorkspace, sink); err != nil {
			t.Fatal(err)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		return engine.work
	}

	small := run(t, 32)
	large := run(t, 64)
	if large > small*3 {
		t.Fatalf("nested work grew superlinearly: 32=%d 64=%d", small, large)
	}
}

func TestSequentialAssignmentHotPathAllocatesNothingAfterFixedSetup(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(3, privatePageCommittedFree),
		newPrivatePageSlot(4, privatePageCommittedFree),
		newPrivatePageSlot(5, privatePageCommittedFree),
	}
	pool := testPrivatePagePool(t, slots, 20, 20)
	var pageSlots [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pageSlots[:])
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	var sink fixedRangeTreeBuildSink
	allocations := testing.AllocsPerRun(100, func() {
		checkpoint, problem := pool.begin()
		if problem.failed() {
			panic(problem)
		}
		engine, err := newSequentialAssignmentEngine[IPv4](pool, checkpoint, &workspace, 2, ValueKindDirect, 2, 10_000, 10_000)
		if err != nil {
			panic(err)
		}
		if err = engine.assign(10, 20, 1); err != nil {
			panic(err)
		}
		sink.reset()
		if _, err = engine.buildFinalTree(&treeWorkspace, &sink); err != nil {
			panic(err)
		}
		if problem = pool.rollback(checkpoint); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 || pool.available() != 3 || !workspace.clean() {
		t.Fatalf("allocations/available/workspace = %v/%d/%+v", allocations, pool.available(), workspace)
	}
}
