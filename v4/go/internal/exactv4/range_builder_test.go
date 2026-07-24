package exactv4

import (
	"errors"
	"testing"
)

var errRangeTreeBuildSink = errors.New("test sink rejected page")

type rangeTreeBuildTestPage struct {
	pageNumber uint32
	page       [PageSize]byte
}

type rangeTreeBuildTestSink struct {
	nextPage      uint32
	pages         []rangeTreeBuildTestPage
	fail          bool
	forcedPage    uint32
	hasForcedPage bool
}

func newRangeTreeBuildTestSink() *rangeTreeBuildTestSink {
	return &rangeTreeBuildTestSink{nextPage: 2}
}

func (s *rangeTreeBuildTestSink) writeRangePage(page []byte) (uint32, error) {
	if s.fail {
		return 0, errRangeTreeBuildSink
	}
	pageNumber := s.nextPage
	if s.hasForcedPage {
		pageNumber = s.forcedPage
	}
	s.nextPage++
	var copied [PageSize]byte
	copy(copied[:], page)
	s.pages = append(s.pages, rangeTreeBuildTestPage{pageNumber: pageNumber, page: copied})
	return pageNumber, nil
}

type fixedRangeTreeBuildSink struct {
	nextPage uint32
	pages    [4][PageSize]byte
	length   int
}

func (s *fixedRangeTreeBuildSink) reset() {
	s.nextPage = 2
	s.length = 0
}

func (s *fixedRangeTreeBuildSink) writeRangePage(page []byte) (uint32, error) {
	if s.length == len(s.pages) {
		return 0, errRangeTreeBuildSink
	}
	copy(s.pages[s.length][:], page)
	s.length++
	pageNumber := s.nextPage
	s.nextPage++
	return pageNumber, nil
}

func requireRangeTreeBuildCode(t *testing.T, err error, want rangeTreeBuildErrorCode) *rangeTreeBuildError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-tree build error %d", want)
	}
	var got *rangeTreeBuildError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangeTreeBuildError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-tree build code = %d, want %d", got.code, want)
	}
	return got
}

func requireRangeTreeBuildStartCode(t *testing.T, err error, want rangeTreeBuildStartErrorCode) *rangeTreeBuildStartError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-tree build start error %d", want)
	}
	var got *rangeTreeBuildStartError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangeTreeBuildStartError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-tree build start code = %d, want %d", got.code, want)
	}
	return got
}

func rangeTreeBuildImage[K rangeKey[K]](
	t *testing.T,
	sink *rangeTreeBuildTestSink,
	result rangeTreeBuildResult,
	pageCount int,
) []byte {
	t.Helper()
	var key K
	meta := emptyDirectMeta(1)
	meta.AddressFamily = key.family()
	meta.ValueKind = ValueKindDirect
	meta.PageCount = uint64(pageCount)
	meta.RangeRoot = result.rootPage
	meta.RangeRecordCount = result.recordCount
	data := make([]byte, pageCount*PageSize)
	page0 := meta.EncodePage()
	page1 := meta.EncodePage()
	copy(data[:PageSize], page0[:])
	copy(data[PageSize:2*PageSize], page1[:])
	for _, stored := range sink.pages {
		start := int(stored.pageNumber) * PageSize
		copy(data[start:start+PageSize], stored.page[:])
	}
	return data
}

func rangeTreeBuildRecordV4(value uint32) rangeRecord[IPv4] {
	address := value * 2
	return rangeRecord[IPv4]{from: IPv4(address), to: IPv4(address), value: 1}
}

func TestRangeTreeBuilderEmptyInputWritesNoPages(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 3)
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.finish(sink)
	if err != nil {
		t.Fatal(err)
	}
	if result != (rangeTreeBuildResult{}) || len(sink.pages) != 0 {
		t.Fatalf("result/pages = %+v/%d", result, len(sink.pages))
	}
}

func TestRangeTreeBuilderOneLeafIsRootAndReopens(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []uint32{5, 10} {
		if err := builder.push(sink, rangeTreeBuildRecordV4(value)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage != 2 || result.rootLevel != 0 || result.recordCount != 2 || len(sink.pages) != 1 {
		t.Fatalf("result/pages = %+v/%d", result, len(sink.pages))
	}

	tree, err := openImmutableRangeTree[IPv4](rangeTreeBuildImage[IPv4](t, sink, result, 8))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := tree.lookup(10)
	if err != nil || !ok || got != rangeTreeBuildRecordV4(5) {
		t.Fatalf("lookup 10 = (%+v, %t, %v)", got, ok, err)
	}
	_, ok, err = tree.lookup(11)
	if err != nil || ok {
		t.Fatalf("lookup gap = (%t, %v)", ok, err)
	}
}

func TestRangeTreeBuilderTwoLeavesFormRootBranchAndReopen(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value < rangeLeafCapacity[IPv4]()+1; value++ {
		if err := builder.push(sink, rangeTreeBuildRecordV4(uint32(value))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootPage != 4 || result.rootLevel != 1 || result.recordCount != uint64(rangeLeafCapacity[IPv4]()+1) || len(sink.pages) != 3 {
		t.Fatalf("result/pages = %+v/%d", result, len(sink.pages))
	}

	tree, err := openImmutableRangeTree[IPv4](rangeTreeBuildImage[IPv4](t, sink, result, 8))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := tree.lookup(IPv4(rangeLeafCapacity[IPv4]() * 2))
	if err != nil || !ok || got != rangeTreeBuildRecordV4(uint32(rangeLeafCapacity[IPv4]())) {
		t.Fatalf("lookup split = (%+v, %t, %v)", got, ok, err)
	}
}

func TestRangeTreeBuilderIPv6LeafSplitUsesSameBoundedPacker(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv6]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value < rangeLeafCapacity[IPv6]()+1; value++ {
		address := uint64(value * 2)
		if err := builder.push(sink, rangeRecord[IPv6]{
			from:  IPv6{Lo: address},
			to:    IPv6{Lo: address},
			value: 7,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootLevel != 1 {
		t.Fatalf("root level = %d", result.rootLevel)
	}
	tree, err := openImmutableRangeTree[IPv6](rangeTreeBuildImage[IPv6](t, sink, result, 8))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := tree.lookup(IPv6{Lo: 2})
	if err != nil || !ok || got.value != 7 {
		t.Fatalf("lookup = (%+v, %t, %v)", got, ok, err)
	}
}

func TestRangeTreeBuilderRebalancesFinalSingletonBranch(t *testing.T) {
	recordCount := rangeBranchCapacity[IPv4]()*rangeLeafCapacity[IPv4]() + 1
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 256)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value < recordCount; value++ {
		if err := builder.push(sink, rangeTreeBuildRecordV4(uint32(value))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := builder.finish(sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.rootLevel != 2 || result.recordCount != uint64(recordCount) {
		t.Fatalf("result = %+v", result)
	}

	var levelOne [2]uint16
	levelOneLen := 0
	levelTwoLen := 0
	for _, stored := range sink.pages {
		header, err := DecodePageHeader(stored.page[:], 1)
		if err != nil {
			t.Fatal(err)
		}
		if header.PageType != PageTypeRangeBranch {
			continue
		}
		if header.ItemCount < 2 {
			t.Fatalf("branch level %d has one child", header.Level)
		}
		switch header.Level {
		case 1:
			if levelOneLen == len(levelOne) {
				t.Fatalf("too many level-one branches")
			}
			levelOne[levelOneLen] = header.ItemCount
			levelOneLen++
		case 2:
			if header.ItemCount != 2 {
				t.Fatalf("root item count = %d", header.ItemCount)
			}
			levelTwoLen++
		default:
			t.Fatalf("unexpected branch level %d", header.Level)
		}
	}
	if levelOneLen != 2 || levelTwoLen != 1 ||
		!((levelOne[0] == 2 && levelOne[1] == uint16(rangeBranchCapacity[IPv4]()-1)) ||
			(levelOne[1] == 2 && levelOne[0] == uint16(rangeBranchCapacity[IPv4]()-1))) {
		t.Fatalf("branch shape = level1=%v/%d level2=%d", levelOne, levelOneLen, levelTwoLen)
	}

	tree, err := openImmutableRangeTree[IPv4](rangeTreeBuildImage[IPv4](t, sink, result, 256))
	if err != nil {
		t.Fatal(err)
	}
	last := uint32(recordCount - 1)
	got, ok, err := tree.lookup(IPv4(last * 2))
	if err != nil || !ok || got != rangeTreeBuildRecordV4(last) {
		t.Fatalf("last lookup = (%+v, %t, %v)", got, ok, err)
	}
}

func TestRangeTreeBuilderChecksCanonicalityAcrossLeafsAndPoisonsOnFailure(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	for value := 0; value < rangeLeafCapacity[IPv4](); value++ {
		if err := builder.push(sink, rangeTreeBuildRecordV4(uint32(value))); err != nil {
			t.Fatal(err)
		}
	}
	requireRangeTreeBuildCode(t, builder.push(sink, rangeTreeBuildRecordV4(0)), rangeTreeBuildErrRangeOverlap)
	_, err = builder.finish(sink)
	requireRangeTreeBuildCode(t, err, rangeTreeBuildErrFailed)
}

func TestRangeTreeBuilderRejectsAdjacentEqualAndZeroMembershipValues(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.push(sink, rangeRecord[IPv4]{from: 10, to: 10, value: 4}); err != nil {
		t.Fatal(err)
	}
	requireRangeTreeBuildCode(t, builder.push(sink, rangeRecord[IPv4]{from: 11, to: 11, value: 4}), rangeTreeBuildErrAdjacentEqualValue)

	var membershipWorkspace rangeTreeBuildWorkspace[IPv4]
	membershipBuilder, err := membershipWorkspace.begin(1, ValueKindMembership, 8)
	if err != nil {
		t.Fatal(err)
	}
	requireRangeTreeBuildCode(t, membershipBuilder.push(sink, rangeRecord[IPv4]{from: 10, to: 10}), rangeTreeBuildErrMembershipValueZero)
}

func TestRangeTreeBuilderAbortsOnSinkFailureOrInvalidReturnedPage(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	sink := newRangeTreeBuildTestSink()
	sink.fail = true
	builder, err := workspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.push(sink, rangeTreeBuildRecordV4(1)); err != nil {
		t.Fatal(err)
	}
	_, err = builder.finish(sink)
	problem := requireRangeTreeBuildCode(t, err, rangeTreeBuildErrSink)
	if !errors.Is(problem, errRangeTreeBuildSink) {
		t.Fatalf("sink error was not preserved: %v", problem)
	}
	requireRangeTreeBuildCode(t, builder.push(sink, rangeTreeBuildRecordV4(2)), rangeTreeBuildErrFailed)

	var invalidWorkspace rangeTreeBuildWorkspace[IPv4]
	invalidSink := newRangeTreeBuildTestSink()
	invalidSink.forcedPage = 8
	invalidSink.hasForcedPage = true
	invalidBuilder, err := invalidWorkspace.begin(1, ValueKindDirect, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := invalidBuilder.push(invalidSink, rangeTreeBuildRecordV4(1)); err != nil {
		t.Fatal(err)
	}
	_, err = invalidBuilder.finish(invalidSink)
	problem = requireRangeTreeBuildCode(t, err, rangeTreeBuildErrSinkPageOutOfBounds)
	if problem.page != 8 {
		t.Fatalf("returned page = %d", problem.page)
	}
}

func TestRangeTreeBuilderHotPathAllocatesNothingAfterWorkspaceConstruction(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	var sink fixedRangeTreeBuildSink
	allocations := testing.AllocsPerRun(100, func() {
		sink.reset()
		builder, err := workspace.begin(1, ValueKindDirect, 4)
		if err != nil {
			panic(err)
		}
		if err := builder.push(&sink, rangeTreeBuildRecordV4(1)); err != nil {
			panic(err)
		}
		if err := builder.push(&sink, rangeTreeBuildRecordV4(3)); err != nil {
			panic(err)
		}
		if _, err := builder.finish(&sink); err != nil {
			panic(err)
		}
	})
	if allocations != 0 || sink.length != 1 {
		t.Fatalf("allocations/pages = %v/%d", allocations, sink.length)
	}
}

func TestRangeTreeBuilderStartRejectsImpossibleTransactionOrPageCount(t *testing.T) {
	var workspace rangeTreeBuildWorkspace[IPv4]
	_, err := workspace.begin(0, ValueKindDirect, 2)
	requireRangeTreeBuildStartCode(t, err, rangeTreeBuildStartErrBornTransactionZero)
	_, err = workspace.begin(1, ValueKindDirect, 1)
	problem := requireRangeTreeBuildStartCode(t, err, rangeTreeBuildStartErrPageCount)
	if problem.pages != 1 {
		t.Fatalf("page count = %d", problem.pages)
	}
	_, err = workspace.begin(1, ValueKindDirect, MaxPageCount+1)
	problem = requireRangeTreeBuildStartCode(t, err, rangeTreeBuildStartErrPageCount)
	if problem.pages != MaxPageCount+1 {
		t.Fatalf("page count = %d", problem.pages)
	}
}
