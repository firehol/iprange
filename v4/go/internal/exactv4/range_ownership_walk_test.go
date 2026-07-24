package exactv4

import (
	"errors"
	"slices"
	"testing"
)

func rangeOwnershipMeta(pageCount uint64, root uint32, records uint64) Meta {
	return Meta{
		AddressFamily:    AddressFamilyIPv4,
		ValueKind:        ValueKindDirect,
		TxnID:            3,
		PageCount:        pageCount,
		RangeRoot:        root,
		RangeRecordCount: records,
	}
}

func newRangeOwnershipIndex(t *testing.T, capacity int) (*pageNumberIndex, *pageNumberIndexWorkspace) {
	t.Helper()
	pages := make([]pageNumberIndexPage, capacity)
	workspace := newPageNumberIndexWorkspace(pages)
	index, err := newPageNumberIndex(&workspace)
	if err != nil {
		t.Fatalf("new ownership index: %v", err)
	}
	return &index, &workspace
}

func requireRangeOwnershipCode(t *testing.T, err error, want rangeOwnershipErrorCode) *rangeOwnershipError {
	t.Helper()
	var got *rangeOwnershipError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want range ownership error: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("ownership error = %d, want %d: %#v", got.code, want, got)
	}
	return got
}

func ownershipWalkImage(t *testing.T) ([]byte, Meta) {
	t.Helper()
	const pageCount = 12
	data := make([]byte, pageCount*PageSize)
	putIPv4Branch(t, rangeImagePage(data, 8), 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 11, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		{lowerFence: 100, childPage: 3},
		{lowerFence: 200, childPage: 4, subtreeRecordCount: 1, firstFrom: 210, lastFrom: 210, lastTo: 220},
	})
	putRangeLeaf(t, rangeImagePage(data, 11), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{})
	putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 210, to: 220, value: 2}})
	return data, rangeOwnershipMeta(pageCount, 8, 2)
}

func TestRangeOwnershipWalkIncludesEmptyChildrenAndSortsPages(t *testing.T) {
	data, meta := ownershipWalkImage(t)
	index, _ := newRangeOwnershipIndex(t, 4)
	var scratch rangeTreeOwnershipScratch
	work, err := collectRangeTreeOwnership[IPv4](
		newImmutableSlicePageSource(data, meta.PageCount), meta, index, &scratch, 4,
	)
	if err != nil || work != 4 {
		t.Fatalf("ownership walk = work:%d error:%v", work, err)
	}
	if got, want := collectPageNumberIndex(t, index), []uint32{3, 4, 8, 11}; !slices.Equal(got, want) {
		t.Fatalf("retirement page order = %v, want %v", got, want)
	}
}

func TestRangeOwnershipWalkHandlesMultilevelTree(t *testing.T) {
	const pageCount = 14
	data := make([]byte, pageCount*PageSize)
	putIPv4Branch(t, rangeImagePage(data, 12), 2, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 7, subtreeRecordCount: 2, firstFrom: 10, lastFrom: 210, lastTo: 220},
	})
	putIPv4Branch(t, rangeImagePage(data, 7), 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 11, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		{lowerFence: 200, childPage: 3, subtreeRecordCount: 1, firstFrom: 210, lastFrom: 210, lastTo: 220},
	})
	putRangeLeaf(t, rangeImagePage(data, 11), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 210, to: 220, value: 2}})
	meta := rangeOwnershipMeta(pageCount, 12, 2)
	index, _ := newRangeOwnershipIndex(t, 4)
	var scratch rangeTreeOwnershipScratch
	work, err := collectRangeTreeOwnership[IPv4](
		newImmutableSlicePageSource(data, pageCount), meta, index, &scratch, 4,
	)
	if err != nil || work != 4 {
		t.Fatalf("multilevel ownership walk = work:%d error:%v", work, err)
	}
	if got, want := collectPageNumberIndex(t, index), []uint32{3, 7, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("multilevel order = %v, want %v", got, want)
	}
}

func TestRangeOwnershipWalkSupportsIPv6Leaf(t *testing.T) {
	data := make([]byte, 3*PageSize)
	putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv6]{
		{from: IPv6{Hi: 0x20010db8, Lo: 1}, to: IPv6{Hi: 0x20010db8, Lo: 2}, value: 7},
	})
	meta := rangeOwnershipMeta(3, 2, 1)
	meta.AddressFamily = AddressFamilyIPv6
	index, _ := newRangeOwnershipIndex(t, 1)
	var scratch rangeTreeOwnershipScratch
	work, err := collectRangeTreeOwnership[IPv6](
		newImmutableSlicePageSource(data, meta.PageCount), meta, index, &scratch, 1,
	)
	if err != nil || work != 1 {
		t.Fatalf("IPv6 ownership walk = work:%d error:%v", work, err)
	}
	if got, want := collectPageNumberIndex(t, index), []uint32{2}; !slices.Equal(got, want) {
		t.Fatalf("IPv6 pages = %v, want %v", got, want)
	}
}

func TestRangeOwnershipWalkUsesExactMaximumDepth(t *testing.T) {
	pageCount := uint64(MaxTreeLevel) + 3
	data := make([]byte, int(pageCount)*PageSize)
	for level := int(MaxTreeLevel); level >= 1; level-- {
		page := uint32(2 + int(MaxTreeLevel) - level)
		putIPv4Branch(t, rangeImagePage(data, int(page)), uint16(level), []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: page + 1},
		})
	}
	putRangeLeaf(t, rangeImagePage(data, int(pageCount-1)), []rangeRecord[IPv4]{})
	meta := rangeOwnershipMeta(pageCount, 2, 0)
	index, _ := newRangeOwnershipIndex(t, 1)
	var scratch rangeTreeOwnershipScratch
	wantWork := uint64(MaxTreeLevel) + 1
	work, err := collectRangeTreeOwnership[IPv4](
		newImmutableSlicePageSource(data, pageCount), meta, index, &scratch, wantWork,
	)
	if err != nil || work != wantWork {
		t.Fatalf("maximum-depth walk = work:%d error:%v", work, err)
	}
	values := collectPageNumberIndex(t, index)
	if len(values) != int(wantWork) || values[0] != 2 || values[len(values)-1] != uint32(pageCount-1) {
		t.Fatalf("maximum-depth pages = %v", values)
	}
}

func TestRangeOwnershipWalkRejectsWrongChildTypeAndLevel(t *testing.T) {
	t.Run("type", func(t *testing.T) {
		data := make([]byte, 5*PageSize)
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{{lowerFence: 0, childPage: 3}})
		putRangeHeader(t, rangeImagePage(data, 3), PageTypeMetadataChunk, 0, 0, PageHeaderSize, AddressFamilyIPv4)
		if _, err := WritePageCRC32C(rangeImagePage(data, 3)); err != nil {
			t.Fatal(err)
		}
		index, _ := newRangeOwnershipIndex(t, 2)
		var scratch rangeTreeOwnershipScratch
		_, err := collectRangeTreeOwnership[IPv4](
			newImmutableSlicePageSource(data, 5), rangeOwnershipMeta(5, 2, 0), index, &scratch, 2,
		)
		requireRangeOwnershipCode(t, err, rangeOwnershipErrChildType)
	})

	t.Run("level", func(t *testing.T) {
		data := make([]byte, 5*PageSize)
		putIPv4Branch(t, rangeImagePage(data, 2), 2, []ipv4BranchTestEntry{{lowerFence: 0, childPage: 3}})
		putIPv4Branch(t, rangeImagePage(data, 3), 2, []ipv4BranchTestEntry{{lowerFence: 0, childPage: 4}})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{})
		index, _ := newRangeOwnershipIndex(t, 3)
		var scratch rangeTreeOwnershipScratch
		_, err := collectRangeTreeOwnership[IPv4](
			newImmutableSlicePageSource(data, 5), rangeOwnershipMeta(5, 2, 0), index, &scratch, 3,
		)
		requireRangeOwnershipCode(t, err, rangeOwnershipErrChildLevel)
	})
}

func TestRangeOwnershipWalkBoundsWorkAndPropagatesSourceFailure(t *testing.T) {
	data, meta := ownershipWalkImage(t)
	t.Run("work", func(t *testing.T) {
		index, _ := newRangeOwnershipIndex(t, 4)
		var scratch rangeTreeOwnershipScratch
		work, err := collectRangeTreeOwnership[IPv4](
			newImmutableSlicePageSource(data, meta.PageCount), meta, index, &scratch, 2,
		)
		if work != 2 {
			t.Fatalf("bounded work = %d, want 2", work)
		}
		requireRangeOwnershipCode(t, err, rangeOwnershipErrWorkBudget)
	})
	t.Run("source", func(t *testing.T) {
		index, _ := newRangeOwnershipIndex(t, 4)
		var scratch rangeTreeOwnershipScratch
		work, err := collectRangeTreeOwnership[IPv4](
			newImmutableSlicePageSource(data[:9*PageSize], meta.PageCount), meta, index, &scratch, 4,
		)
		if work != 1 {
			t.Fatalf("source-failure work = %d, want 1", work)
		}
		requireRangeOwnershipCode(t, err, rangeOwnershipErrSource)
	})
}

func TestRangeOwnershipWalkEmptyRootAndNoHeapAfterSetup(t *testing.T) {
	empty := rangeOwnershipMeta(2, 0, 0)
	index, _ := newRangeOwnershipIndex(t, 1)
	var scratch rangeTreeOwnershipScratch
	work, err := collectRangeTreeOwnership[IPv4](
		newImmutableSlicePageSource(nil, 2), empty, index, &scratch, 1,
	)
	if err != nil || work != 0 || index.len() != 0 {
		t.Fatalf("empty ownership walk = work:%d len:%d error:%v", work, index.len(), err)
	}
	if _, err = collectRangeTreeOwnership[IPv4](
		newImmutableSlicePageSource(nil, 2), rangeOwnershipMeta(2, 0, 1), index, &scratch, 1,
	); err == nil {
		t.Fatal("accepted root zero with nonzero record count")
	} else {
		requireRangeOwnershipCode(t, err, rangeOwnershipErrRootRecordCount)
	}

	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	data, meta := ownershipWalkImage(t)
	index, _ = newRangeOwnershipIndex(t, 4)
	source := newImmutableSlicePageSource(data, meta.PageCount)
	if _, err = collectRangeTreeOwnership[IPv4](
		source, meta, index, &scratch, 4,
	); err != nil {
		t.Fatalf("warmup ownership walk: %v", err)
	}
	allocations := testing.AllocsPerRun(20, func() {
		index.discardAfterAbort()
		work, walkErr := collectRangeTreeOwnership[IPv4](
			source, meta, index, &scratch, 4,
		)
		if walkErr != nil || work != 4 {
			t.Fatalf("allocation-free ownership walk = work:%d error:%v", work, walkErr)
		}
	})
	if allocations != 0 {
		t.Fatalf("ownership walk allocations = %v, want 0", allocations)
	}
}
