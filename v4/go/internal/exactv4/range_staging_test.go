package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func requireRangeTreeStagingCode(
	t *testing.T,
	err error,
	want rangeTreeStagingErrorCode,
) *rangeTreeStagingError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-tree staging error %d", want)
	}
	var got *rangeTreeStagingError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangeTreeStagingError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-tree staging code = %d, want %d", got.code, want)
	}
	return got
}

func rangeTreeStagingAssignment(pageNumber uint32) rangeTreePhysicalAssignment {
	return rangeTreePhysicalAssignment{
		pageNumber: pageNumber, authorization: privatePageCommittedFree,
	}
}

func rangeTreeStagingRecordV4(value uint32) rangeRecord[IPv4] {
	address := value * 2
	return rangeRecord[IPv4]{from: IPv4(address), to: IPv4(address), value: 1}
}

func rangeTreeStagingRecordV6(value uint64) rangeRecord[IPv6] {
	address := value * 2
	return rangeRecord[IPv6]{
		from: IPv6{Lo: address}, to: IPv6{Lo: address}, value: 1,
	}
}

func buildRangeTreeStagingV4(
	t *testing.T,
	staging *rangeTreeStaging[IPv4],
	workspace *rangeTreeBuildWorkspace[IPv4],
	records []rangeRecord[IPv4],
) rangeTreeStagedResult {
	t.Helper()
	builder, err := workspace.begin(2, ValueKindDirect, staging.logicalPageCount())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err = builder.push(staging, record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(staging)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := staging.finish(result)
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func buildRangeTreeStagingV6(
	t *testing.T,
	staging *rangeTreeStaging[IPv6],
	workspace *rangeTreeBuildWorkspace[IPv6],
	records []rangeRecord[IPv6],
) rangeTreeStagedResult {
	t.Helper()
	builder, err := workspace.begin(2, ValueKindDirect, staging.logicalPageCount())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err = builder.push(staging, record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(staging)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := staging.finish(result)
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func rangeTreeStagingImage[K rangeKey[K]](
	t *testing.T,
	result rangeTreeMaterializedResult,
	pages []privateWriterProducedTerminalPage,
	pageCount int,
) []byte {
	t.Helper()
	var key K
	meta := emptyDirectMeta(2)
	meta.AddressFamily = key.family()
	meta.ValueKind = ValueKindDirect
	meta.PageCount = uint64(pageCount)
	meta.RangeRoot = result.rootPage
	meta.RangeRecordCount = result.recordCount
	data := make([]byte, pageCount*PageSize)
	encoded := meta.EncodePage()
	copy(data[:PageSize], encoded[:])
	copy(data[PageSize:2*PageSize], encoded[:])
	for _, page := range pages {
		start := int(page.pageNumber) * PageSize
		copy(data[start:start+PageSize], page.bytes[:])
	}
	return data
}

func TestRangeTreeStagingMaterializesOneLogicalLeafForBothFamilies(t *testing.T) {
	t.Run("IPv4", func(t *testing.T) {
		var pages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv4](pages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		var workspace rangeTreeBuildWorkspace[IPv4]
		staged := buildRangeTreeStagingV4(t, &staging, &workspace, []rangeRecord[IPv4]{rangeTreeStagingRecordV4(1)})
		var terminal [1]privateWriterProducedTerminalPage
		result, err := staging.materialize(staged, 12, []rangeTreePhysicalAssignment{rangeTreeStagingAssignment(7)}, terminal[:])
		if err != nil {
			t.Fatal(err)
		}
		if result.rootPage != 7 || result.pageCount != 1 || terminal[0].owner != privatePageOwnerRange ||
			terminal[0].origin != privatePageRange || !VerifyPageCRC32C(terminal[0].bytes[:]) {
			t.Fatalf("materialized result/page = %+v/%+v", result, terminal[0])
		}
	})

	t.Run("IPv6", func(t *testing.T) {
		var pages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv6](pages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		var workspace rangeTreeBuildWorkspace[IPv6]
		staged := buildRangeTreeStagingV6(t, &staging, &workspace, []rangeRecord[IPv6]{rangeTreeStagingRecordV6(1)})
		var terminal [1]privateWriterProducedTerminalPage
		result, err := staging.materialize(staged, 12, []rangeTreePhysicalAssignment{rangeTreeStagingAssignment(7)}, terminal[:])
		if err != nil {
			t.Fatal(err)
		}
		header, decodeErr := DecodePageHeader(terminal[0].bytes[:], 2)
		if result.rootPage != 7 || header.Aux != uint32(AddressFamilyIPv6) || decodeErr != nil ||
			!VerifyPageCRC32C(terminal[0].bytes[:]) {
			t.Fatalf("materialized result/header = %+v/%+v/%v", result, header, decodeErr)
		}
	})
}

func TestRangeTreeStagingRemapsMultilevelIPv4AndReopens(t *testing.T) {
	count := rangeLeafCapacity[IPv4]() + 1
	records := make([]rangeRecord[IPv4], count)
	for index := range records {
		records[index] = rangeTreeStagingRecordV4(uint32(index))
	}
	var pages [3]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](pages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv4]
	staged := buildRangeTreeStagingV4(t, &staging, &workspace, records)
	if staged.pageCount != 3 {
		t.Fatalf("staged pages = %d, want 3", staged.pageCount)
	}
	assignments := []rangeTreePhysicalAssignment{
		rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(9), rangeTreeStagingAssignment(17),
	}
	var terminal [3]privateWriterProducedTerminalPage
	result, err := staging.materialize(staged, 20, assignments, terminal[:])
	if err != nil {
		t.Fatal(err)
	}
	branch, err := openRangeBranch[IPv4](terminal[2].bytes[:], 2, AddressFamilyIPv4, 20)
	if err != nil {
		t.Fatal(err)
	}
	left, leftErr := branch.entry(0)
	right, rightErr := branch.entry(1)
	if result.rootPage != 17 || leftErr != nil || rightErr != nil || left.childPage != 3 || right.childPage != 9 ||
		!VerifyPageCRC32C(terminal[2].bytes[:]) {
		t.Fatalf("result/children = %+v/%+v/%v/%+v/%v", result, left, leftErr, right, rightErr)
	}
	tree, err := openImmutableRangeTree[IPv4](rangeTreeStagingImage[IPv4](t, result, terminal[:], 20))
	if err != nil {
		t.Fatal(err)
	}
	got, found, lookupErr := tree.lookup(IPv4((count - 1) * 2))
	if lookupErr != nil || !found || got.value != 1 {
		t.Fatalf("reopened lookup = %+v/%t/%v", got, found, lookupErr)
	}
}

func TestRangeTreeStagingRemapsMultilevelIPv6AndReopens(t *testing.T) {
	count := rangeLeafCapacity[IPv6]() + 1
	records := make([]rangeRecord[IPv6], count)
	for index := range records {
		records[index] = rangeTreeStagingRecordV6(uint64(index))
	}
	var pages [3]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv6](pages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv6]
	staged := buildRangeTreeStagingV6(t, &staging, &workspace, records)
	if staged.pageCount != 3 {
		t.Fatalf("staged pages = %d, want 3", staged.pageCount)
	}
	assignments := []rangeTreePhysicalAssignment{
		rangeTreeStagingAssignment(4), rangeTreeStagingAssignment(10), rangeTreeStagingAssignment(19),
	}
	var terminal [3]privateWriterProducedTerminalPage
	result, err := staging.materialize(staged, 20, assignments, terminal[:])
	if err != nil {
		t.Fatal(err)
	}
	branch, err := openRangeBranch[IPv6](terminal[2].bytes[:], 2, AddressFamilyIPv6, 20)
	if err != nil {
		t.Fatal(err)
	}
	left, leftErr := branch.entry(0)
	right, rightErr := branch.entry(1)
	if result.rootPage != 19 || leftErr != nil || rightErr != nil || left.childPage != 4 || right.childPage != 10 ||
		!VerifyPageCRC32C(terminal[2].bytes[:]) {
		t.Fatalf("result/children = %+v/%+v/%v/%+v/%v", result, left, leftErr, right, rightErr)
	}
	tree, err := openImmutableRangeTree[IPv6](rangeTreeStagingImage[IPv6](t, result, terminal[:], 20))
	if err != nil {
		t.Fatal(err)
	}
	got, found, lookupErr := tree.lookup(IPv6{Lo: uint64((count - 1) * 2)})
	if lookupErr != nil || !found || got.value != 1 {
		t.Fatalf("reopened lookup = %+v/%t/%v", got, found, lookupErr)
	}
}

func TestRangeTreeStagingRejectsBadAssignmentsAndLogicalChildrenAtomically(t *testing.T) {
	count := rangeLeafCapacity[IPv4]() + 1
	records := make([]rangeRecord[IPv4], count)
	for index := range records {
		records[index] = rangeTreeStagingRecordV4(uint32(index))
	}
	var pages [3]rangeTreeStagingPage
	staging, err := newRangeTreeStaging[IPv4](pages[:], 2, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	var workspace rangeTreeBuildWorkspace[IPv4]
	staged := buildRangeTreeStagingV4(t, &staging, &workspace, records)
	var before [3]privateWriterProducedTerminalPage
	terminal := before
	problem := requireRangeTreeStagingCode(
		t,
		func() error {
			_, materializeErr := staging.materialize(staged, 20, []rangeTreePhysicalAssignment{
				rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(9),
			}, terminal[:])
			return materializeErr
		}(),
		rangeTreeStagingErrPhysicalPageOrder,
	)
	if problem.previous != 3 || problem.page != 3 || terminal != before {
		t.Fatalf("duplicate assignment/output = %+v/%+v", problem, terminal)
	}

	problem = requireRangeTreeStagingCode(
		t,
		func() error {
			_, materializeErr := staging.materialize(staged, 20, []rangeTreePhysicalAssignment{
				rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(5),
			}, terminal[:])
			return materializeErr
		}(),
		rangeTreeStagingErrAssignmentCount,
	)
	if problem.required != 3 || problem.actual != 2 || terminal != before {
		t.Fatalf("missing assignment/output = %+v/%+v", problem, terminal)
	}
	problem = requireRangeTreeStagingCode(
		t,
		func() error {
			_, materializeErr := staging.materialize(staged, 1, []rangeTreePhysicalAssignment{
				rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(5), rangeTreeStagingAssignment(9),
			}, terminal[:])
			return materializeErr
		}(),
		rangeTreeStagingErrFinalPageCount,
	)
	if terminal != before {
		t.Fatalf("invalid final page count changed output = %+v", terminal)
	}

	childOffset := rangeTreeStagingBranchChildOffset[IPv4](0)
	binary.LittleEndian.PutUint32(staging.pages[2].bytes[childOffset:childOffset+4], 99)
	if _, err = WritePageCRC32C(staging.pages[2].bytes[:]); err != nil {
		t.Fatal(err)
	}
	problem = requireRangeTreeStagingCode(
		t,
		func() error {
			_, materializeErr := staging.materialize(staged, 20, []rangeTreePhysicalAssignment{
				rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(5), rangeTreeStagingAssignment(9),
			}, terminal[:])
			return materializeErr
		}(),
		rangeTreeStagingErrLogicalChildOutOfBounds,
	)
	if problem.index != 2 || problem.child != 99 || terminal != before {
		t.Fatalf("invalid logical child/output = %+v/%+v", problem, terminal)
	}

	staging.pages[0].bytes[0] ^= 1
	problem = requireRangeTreeStagingCode(
		t,
		func() error {
			_, materializeErr := staging.materialize(staged, 20, []rangeTreePhysicalAssignment{
				rangeTreeStagingAssignment(3), rangeTreeStagingAssignment(5), rangeTreeStagingAssignment(9),
			}, terminal[:])
			return materializeErr
		}(),
		rangeTreeStagingErrInvalidStagedPage,
	)
	if problem.index != 0 || terminal != before {
		t.Fatalf("invalid staged page/output = %+v/%+v", problem, terminal)
	}
}

func TestRangeTreeStagingEmptyTreesHaveNoTerminalPages(t *testing.T) {
	t.Run("IPv4", func(t *testing.T) {
		var pages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv4](pages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		var workspace rangeTreeBuildWorkspace[IPv4]
		builder, err := workspace.begin(2, ValueKindDirect, staging.logicalPageCount())
		if err != nil {
			t.Fatal(err)
		}
		result, err := builder.finish(&staging)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := staging.finish(result)
		if err != nil {
			t.Fatal(err)
		}
		materialized, err := staging.materialize(staged, 2, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if materialized.rootPage != 0 || materialized.pageCount != 0 {
			t.Fatalf("empty materialized result = %+v", materialized)
		}
	})

	t.Run("IPv6", func(t *testing.T) {
		var pages [1]rangeTreeStagingPage
		staging, err := newRangeTreeStaging[IPv6](pages[:], 2, ValueKindDirect)
		if err != nil {
			t.Fatal(err)
		}
		var workspace rangeTreeBuildWorkspace[IPv6]
		builder, err := workspace.begin(2, ValueKindDirect, staging.logicalPageCount())
		if err != nil {
			t.Fatal(err)
		}
		result, err := builder.finish(&staging)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := staging.finish(result)
		if err != nil {
			t.Fatal(err)
		}
		materialized, err := staging.materialize(staged, 2, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if materialized.rootPage != 0 || materialized.pageCount != 0 {
			t.Fatalf("empty materialized result = %+v", materialized)
		}
	})
}

func TestRangeTreeStagingHotPathAllocatesNothingAfterFixedSetup(t *testing.T) {
	var pages [1]rangeTreeStagingPage
	var workspace rangeTreeBuildWorkspace[IPv4]
	var terminal [1]privateWriterProducedTerminalPage
	var assignments [1]rangeTreePhysicalAssignment
	var staging rangeTreeStaging[IPv4]
	var builder rangeTreeBuilder[IPv4]
	allocations := testing.AllocsPerRun(100, func() {
		clear(pages[:])
		clear(terminal[:])
		assignments[0] = rangeTreeStagingAssignment(3)
		var err error
		staging, err = newRangeTreeStaging[IPv4](pages[:], 2, ValueKindDirect)
		if err != nil {
			panic(err)
		}
		builder, err = workspace.begin(2, ValueKindDirect, staging.logicalPageCount())
		if err != nil {
			panic(err)
		}
		if err = builder.push(&staging, rangeTreeStagingRecordV4(1)); err != nil {
			panic(err)
		}
		built, err := builder.finish(&staging)
		if err != nil {
			panic(err)
		}
		staged, err := staging.finish(built)
		if err != nil {
			panic(err)
		}
		if _, err = staging.materialize(staged, 8, assignments[:], terminal[:]); err != nil {
			panic(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("allocations = %v, want zero", allocations)
	}
}
