package exactv4

import (
	"errors"
	"testing"
)

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

func singleLeafStagedAssignmentRecords[K rangeKey[K]](
	t *testing.T,
	staging *rangeTreeStaging[K],
	valueKind ValueKind,
) []rangeRecord[K] {
	t.Helper()
	if staging.len() != 1 {
		t.Fatalf("staged range pages = %d, want one leaf", staging.len())
	}
	var key K
	leaf, err := openRangeLeaf[K](staging.pages[0].bytes[:], 2, key.family(), valueKind)
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

func TestSequentialAssignmentPreservesUncoveredSidesInArrivalOrder(t *testing.T) {
	var nodePages [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 8, 10_000, 10_000)
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
	var stagingPages [2]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := engine.buildStagedTree(&treeWorkspace, &staging)
	if err != nil {
		t.Fatal(err)
	}
	if staged.logicalRoot != 2 || staged.recordCount != 3 || !workspace.clean() {
		t.Fatalf("staged/workspace = %+v/%+v", staged, workspace)
	}
	got := singleLeafStagedAssignmentRecords[IPv4](t, &staging, ValueKindDirect)
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
}

func TestSequentialAssignmentStagesBeforePhysicalMaterialization(t *testing.T) {
	var nodePages [1]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 2, 10_000, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.assign(10, 20, 7); err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	var stagingPages [1]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := engine.buildStagedTree(&treeWorkspace, &staging)
	if err != nil {
		t.Fatal(err)
	}
	if staged.logicalRoot != 2 || staging.pages[0].bytes == ([PageSize]byte{}) {
		t.Fatalf("logical staging = %+v/%x", staged, staging.pages[0].bytes[:PageHeaderSize])
	}
	var terminal [1]privateWriterProducedTerminalPage
	materialized, err := staging.materialize(
		staged,
		12,
		[]rangeTreePhysicalAssignment{rangeTreeStagingAssignment(7)},
		terminal[:],
	)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.rootPage != 7 || terminal[0].pageNumber != 7 {
		t.Fatalf("materialized/terminal = %+v/%+v", materialized, terminal[0])
	}
	tree, err := openImmutableRangeTree[IPv4](rangeTreeStagingImage[IPv4](t, materialized, terminal[:], 12))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record != (rangeRecord[IPv4]{from: 10, to: 20, value: 7}) {
		t.Fatalf("reopened range = %#v/%t/%v", record, found, err)
	}
}

func TestSequentialAssignmentAcceptsEmptyInputWithoutStagingAPage(t *testing.T) {
	workspace := newSequentialAssignmentWorkspace(nil)
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 0, 1_000, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	var stagingPages [1]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := engine.buildStagedTree(&treeWorkspace, &staging)
	if err != nil {
		t.Fatal(err)
	}
	if staged.logicalRoot != 0 || staged.recordCount != 0 || staging.len() != 0 || !workspace.clean() {
		t.Fatalf("empty staged/workspace = %+v/%+v", staged, workspace)
	}
}

func TestSequentialAssignmentAllowsDirectZeroAndRejectsMembershipZeroBeforeNodeWrite(t *testing.T) {
	t.Run("direct zero", func(t *testing.T) {
		var nodePages [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(nodePages[:])
		engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 2, 1_000, 1_000)
		if err != nil {
			t.Fatal(err)
		}
		if err = engine.assign(4, 5, 0); err != nil {
			t.Fatal(err)
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		var stagingPages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
			t.Fatal(err)
		}
		got := singleLeafStagedAssignmentRecords[IPv4](t, &staging, ValueKindDirect)
		if len(got) != 1 || got[0] != (rangeRecord[IPv4]{from: 4, to: 5, value: 0}) {
			t.Fatalf("direct-zero records = %#v", got)
		}
	})

	t.Run("membership zero", func(t *testing.T) {
		var nodePages [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(nodePages[:])
		engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindMembership, 2, 1_000, 1_000)
		if err != nil {
			t.Fatal(err)
		}
		requireSequentialAssignmentCode(t, engine.assign(4, 5, 0), sequentialAssignmentErrMembershipValueZero)
		if !workspace.clean() {
			t.Fatalf("membership zero wrote node storage: %+v", workspace)
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		var stagingPages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindMembership)
		if err != nil {
			t.Fatal(err)
		}
		requireSequentialAssignmentCode(t, func() error {
			_, buildErr := engine.buildStagedTree(&treeWorkspace, &staging)
			return buildErr
		}(), sequentialAssignmentErrFailed)
	})
}

func TestSequentialAssignmentClearRemovesOnlyItsArrivalInterval(t *testing.T) {
	var nodePages [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 4, 10_000, 10_000)
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
	var stagingPages [2]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
		t.Fatal(err)
	}
	got := singleLeafStagedAssignmentRecords[IPv4](t, &staging, ValueKindDirect)
	want := []rangeRecord[IPv4]{{from: 10, to: 14, value: 1}, {from: 21, to: 30, value: 1}}
	if len(got) != len(want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("record[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestSequentialAssignmentCoalescesAdjacentFinalValues(t *testing.T) {
	var nodePages [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 4, 10_000, 10_000)
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
	var stagingPages [1]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
		t.Fatal(err)
	}
	got := singleLeafStagedAssignmentRecords[IPv4](t, &staging, ValueKindDirect)
	want := rangeRecord[IPv4]{from: 10, to: 30, value: 7}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestSequentialAssignmentMatchesSmallPerAddressArrivalOrderOracle(t *testing.T) {
	type expectedValue struct {
		set   bool
		value uint32
	}

	var nodePages [32]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 128, 1_000_000, 1_000_000)
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
	var stagingPages [4]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
		t.Fatal(err)
	}
	got := [256]expectedValue{}
	for _, record := range singleLeafStagedAssignmentRecords[IPv4](t, &staging, ValueKindDirect) {
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
}

func TestSequentialAssignmentHandlesFullIPv6SpaceWithoutWrap(t *testing.T) {
	var nodePages [3]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv6](&workspace, 2, ValueKindDirect, 4, 100_000, 100_000)
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
	var stagingPages [1]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv6](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
		t.Fatal(err)
	}
	got := singleLeafStagedAssignmentRecords[IPv6](t, &staging, ValueKindDirect)
	want := []rangeRecord[IPv6]{
		{from: IPv6{}, to: IPv6{Hi: ^uint64(0), Lo: ^uint64(0) - 1}, value: 1},
		{from: maximum, to: maximum, value: 2},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("full IPv6 records = %#v, want %#v", got, want)
	}
}

func TestSequentialAssignmentStagesAndMaterializesMultilevelIPv4(t *testing.T) {
	count := rangeLeafCapacity[IPv4]() + 1
	var nodePages [80]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, uint64(count), 1_000_000, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		address := IPv4(index * 2)
		if err = engine.assign(address, address, 1); err != nil {
			t.Fatalf("assignment %d: %v", index, err)
		}
	}
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	var stagingPages [3]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := engine.buildStagedTree(&treeWorkspace, &staging)
	if err != nil {
		t.Fatal(err)
	}
	if staged.pageCount != 3 || !workspace.clean() {
		t.Fatalf("staged/workspace = %+v/%t", staged, workspace.clean())
	}
	assignments := []rangeTreePhysicalAssignment{
		rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(9), rangeTreeStagingAssignment(17),
	}
	var terminal [3]privateWriterProducedTerminalPage
	materialized, err := staging.materialize(staged, 20, assignments, terminal[:])
	if err != nil {
		t.Fatal(err)
	}
	branch, err := openRangeBranch[IPv4](terminal[2].bytes[:], 2, AddressFamilyIPv4, 20)
	if err != nil {
		t.Fatal(err)
	}
	left, leftErr := branch.entry(0)
	right, rightErr := branch.entry(1)
	if materialized.rootPage != 17 || leftErr != nil || rightErr != nil || left.childPage != 3 || right.childPage != 9 ||
		!VerifyPageCRC32C(terminal[2].bytes[:]) {
		t.Fatalf("materialized branch = %+v/%+v/%v/%+v/%v", materialized, left, leftErr, right, rightErr)
	}
}

func TestSequentialAssignmentFailureRequiresAbortBeforeWorkspaceReuse(t *testing.T) {
	t.Run("node workspace", func(t *testing.T) {
		var nodePages [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(nodePages[:])
		engine, err := newSequentialAssignmentEngine[IPv6](&workspace, 2, ValueKindDirect, 2, 100_000, 100_000)
		if err != nil {
			t.Fatal(err)
		}
		requireSequentialAssignmentCode(t, engine.assign(IPv6FromHalves(0, 1), IPv6FromHalves(0, 1), 1), sequentialAssignmentErrWorkspacePageLimit)
		if workspace.clean() {
			t.Fatal("expected failed draft to retain dirty node workspace until abort")
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv6]
		var stagingPages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv6](stagingPages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		requireSequentialAssignmentCode(t, func() error {
			_, buildErr := engine.buildStagedTree(&treeWorkspace, &staging)
			return buildErr
		}(), sequentialAssignmentErrFailed)
		workspace.discardAfterAbort()
		staging.discardAfterAbort()
		if !workspace.clean() || stagingPages[0] != (rangeTreeStagingPage{}) {
			t.Fatalf("abort did not scrub workspace/staging: %+v/%+v", workspace, stagingPages[0])
		}
	})

	t.Run("staging capacity", func(t *testing.T) {
		var nodePages [1]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(nodePages[:])
		engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 1, 10_000, 10_000)
		if err != nil {
			t.Fatal(err)
		}
		if err = engine.assign(10, 20, 1); err != nil {
			t.Fatal(err)
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		staging, err := newRangeTreeStaging[IPv4](nil, 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		requireRangeTreeStagingCode(t, func() error {
			_, buildErr := engine.buildStagedTree(&treeWorkspace, &staging)
			return buildErr
		}(), rangeTreeStagingErrCapacityExhausted)
		if workspace.clean() {
			t.Fatal("staging failure unexpectedly made the failed draft reusable")
		}
		workspace.discardAfterAbort()
		staging.discardAfterAbort()
		if !workspace.clean() {
			t.Fatalf("abort did not scrub node workspace: %+v", workspace)
		}
	})
}

func TestSequentialAssignmentNestedWorkScalesLinearly(t *testing.T) {
	run := func(t *testing.T, count int) uint64 {
		t.Helper()
		var nodePages [64]sequentialAssignmentPage
		workspace := newSequentialAssignmentWorkspace(nodePages[:])
		engine, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, uint64(count), 1_000_000, 1_000_000)
		if err != nil {
			t.Fatal(err)
		}
		for value := 0; value < count; value++ {
			if err = engine.assign(IPv4(value), IPv4(^uint32(0)-uint32(value)), uint32(value&1)); err != nil {
				t.Fatal(err)
			}
		}
		var treeWorkspace rangeTreeBuildWorkspace[IPv4]
		var stagingPages [4]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
			t.Fatal(err)
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
	var nodePages [2]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(nodePages[:])
	var treeWorkspace rangeTreeBuildWorkspace[IPv4]
	var stagingPages [1]rangeTreeStagingPage
	var engine sequentialAssignmentEngine[IPv4]
	var staging rangeTreeStaging[IPv4]
	allocations := testing.AllocsPerRun(100, func() {
		clear(nodePages[:])
		clear(stagingPages[:])
		var err error
		engine, err = newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 2, 10_000, 10_000)
		if err != nil {
			panic(err)
		}
		if err = engine.assign(10, 20, 1); err != nil {
			panic(err)
		}
		staging, err = newRangeTreeStaging[IPv4](stagingPages[:], 2, ValueKindDirect)
		if err != nil {
			panic(err)
		}
		if _, err = engine.buildStagedTree(&treeWorkspace, &staging); err != nil {
			panic(err)
		}
		staging.discardAfterAbort()
	})
	if allocations != 0 || !workspace.clean() || stagingPages[0] != (rangeTreeStagingPage{}) {
		t.Fatalf("allocations/workspace/staging = %v/%t/%t", allocations, workspace.clean(), stagingPages[0] == (rangeTreeStagingPage{}))
	}
}

func TestSequentialAssignmentRejectsZeroBirthGenerationBeforeInput(t *testing.T) {
	var pages [1]sequentialAssignmentPage
	workspace := newSequentialAssignmentWorkspace(pages[:])
	requireSequentialAssignmentCode(
		t,
		func() error {
			_, err := newSequentialAssignmentEngine[IPv4](&workspace, 0, ValueKindDirect, 1, 1, 1)
			return err
		}(),
		sequentialAssignmentErrBornTransactionZero,
	)
	if !workspace.clean() {
		t.Fatalf("zero birth generation changed workspace: %+v", workspace)
	}
}

func TestSequentialAssignmentRejectsAnOccupiedWorkspaceBeforeInput(t *testing.T) {
	var pages [1]sequentialAssignmentPage
	pages[0].used = 1
	workspace := newSequentialAssignmentWorkspace(pages[:])
	requireSequentialAssignmentCode(
		t,
		func() error {
			_, err := newSequentialAssignmentEngine[IPv4](&workspace, 2, ValueKindDirect, 1, 1, 1)
			return err
		}(),
		sequentialAssignmentErrWorkspaceBusy,
	)
	if pages[0].used != 1 {
		t.Fatalf("occupied workspace changed before rejection: %+v", pages[0])
	}
}
