package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func bitmapImage(meta Meta, pages int, fill func([]byte)) []byte {
	meta.PageCount = uint64(pages)
	data := make([]byte, pages*PageSize)
	page0 := meta.EncodePage()
	page1 := meta.EncodePage()
	copy(data[:PageSize], page0[:])
	copy(data[PageSize:2*PageSize], page1[:])
	fill(data)
	return data
}

func bitmapImagePage(data []byte, page int) []byte {
	return data[page*PageSize : (page+1)*PageSize]
}

func logicalBitmapTree(
	t *testing.T,
	data []byte,
	root uint32,
	kind bitmapKind,
	limit uint64,
	first uint64,
) bitmapTree[immutableSlicePageSource] {
	t.Helper()
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	return newBitmapTree(
		newImmutableSlicePageSource(data, bootstrap.Meta.PageCount),
		bootstrap,
		root,
		kind,
		limit,
		first,
	)
}

func requireBitmapReadCode(t *testing.T, err error, want bitmapReadErrorCode) *bitmapReadError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected bitmap-read error %d", want)
	}
	var got *bitmapReadError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *bitmapReadError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("bitmap-read code = %d, want %d", got.code, want)
	}
	return got
}

func TestFreeBitmapLeafFindsLowestPageAndCRCIsExplicit(t *testing.T) {
	meta := emptyDirectMeta(1)
	meta.FreeBitmapRoot = 2
	data := bitmapImage(meta, 431, func(data []byte) {
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			map[int]uint64{6: 1 << 45},
		)
	})
	tree, err := openImmutableFreeBitmapTree(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := tree.lowestFree(); err != nil || !ok || got != 429 {
		t.Fatalf("ordinary lowest free = %d/%t/%v, want 429/true/nil", got, ok, err)
	}
	if got, ok, err := tree.lowestFreeVerified(); err != nil || !ok || got != 429 {
		t.Fatalf("verified lowest free = %d/%t/%v, want 429/true/nil", got, ok, err)
	}

	data[2*PageSize+PageCRCOffset] ^= 1
	if got, ok, err := tree.lowestFree(); err != nil || !ok || got != 429 {
		t.Fatalf("ordinary search after CRC damage = %d/%t/%v", got, ok, err)
	}
	readErr := requireBitmapReadCode(
		t,
		lowestFreeVerifiedError(tree),
		bitmapReadErrPage,
	)
	if readErr.page != 2 {
		t.Fatalf("verified error page = %d, want 2", readErr.page)
	}
	requireBitmapPageCode(t, readErr, bitmapPageErrChecksum)
}

func TestVerifiedFreeBitmapRejectsReservedMetaPages(t *testing.T) {
	meta := emptyDirectMeta(1)
	meta.FreeBitmapRoot = 2
	data := bitmapImage(meta, 3, func(data []byte) {
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			map[int]uint64{0: 0b110},
		)
	})
	tree, err := openImmutableFreeBitmapTree(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := tree.lowestFree(); err != nil || !ok || got != 2 {
		t.Fatalf("ordinary lowest free = %d/%t/%v, want 2/true/nil", got, ok, err)
	}
	readErr := requireBitmapReadCode(
		t,
		lowestFreeVerifiedError(tree),
		bitmapReadErrPage,
	)
	requireBitmapPageCode(t, readErr, bitmapPageErrBitOutsideLimit)
}

func TestUsedBitmapAbsentChildIsCandidate(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFeedUsed,
			1,
			[]int{0},
			map[int]uint32{1: 3},
		)
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 3),
			bitmapKindFeedUsed,
			map[int]uint64{0: 1},
		)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindFeedUsed, 32_001, 0)
	if got, ok, err := tree.lowestUnused(); err != nil || !ok || got != 0 {
		t.Fatalf("lowest unused feed = %d/%t/%v, want 0/true/nil", got, ok, err)
	}
}

func TestMembershipBitmapNeverAllocatesIDZero(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 3, func(data []byte) {
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 2),
			bitmapKindMembershipUsed,
			map[int]uint64{0: 0b10},
		)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindMembershipUsed, 10, 1)
	if got, ok, err := tree.lowestUnused(); err != nil || !ok || got != 2 {
		t.Fatalf("lowest unused membership = %d/%t/%v, want 2/true/nil", got, ok, err)
	}

	zeroRoot := logicalBitmapTree(t, data, 0, bitmapKindMembershipUsed, 10, 1)
	if got, ok, err := zeroRoot.lowestUnused(); err != nil || !ok || got != 1 {
		t.Fatalf("zero-root membership candidate = %d/%t/%v, want 1/true/nil", got, ok, err)
	}
}

func TestFreeSummaryRequiresChildAndExactMinimumRootLevel(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			1,
			[]int{0},
			map[int]uint32{1: 3},
		)
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 3),
			bitmapKindFreePages,
			map[int]uint64{0: 1 << 2},
		)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindFreePages, 32_001, 2)
	requireBitmapReadCode(t, lowestFreeError(tree), bitmapReadErrSelectedChildMissing)

	putBitmapLeaf(
		t,
		bitmapImagePage(data, 2),
		bitmapKindFreePages,
		map[int]uint64{0: 1 << 2},
	)
	tree = logicalBitmapTree(t, data, 2, bitmapKindFreePages, 32_001, 2)
	err := lowestFreeError(tree)
	readErr := requireBitmapReadCode(t, err, bitmapReadErrRootLevel)
	if readErr.expectedLevel != 1 || readErr.actualLevel != 0 {
		t.Fatalf("root levels = %d/%d, want 1/0", readErr.expectedLevel, readErr.actualLevel)
	}
}

func TestVerifiedFreeSearchChecksEntireSelectedPath(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			1,
			[]int{0},
			map[int]uint32{0: 3},
		)
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 3),
			bitmapKindFreePages,
			map[int]uint64{0: 1 << 2},
		)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindFreePages, 32_001, 2)
	if got, ok, err := tree.lowestFreeVerified(); err != nil || !ok || got != 2 {
		t.Fatalf("verified path = %d/%t/%v, want 2/true/nil", got, ok, err)
	}

	data[2*PageSize+PageCRCOffset] ^= 1
	readErr := requireBitmapReadCode(t, lowestFreeVerifiedError(tree), bitmapReadErrPage)
	if readErr.page != 2 {
		t.Fatalf("branch CRC error page = %d, want 2", readErr.page)
	}
	requireBitmapPageCode(t, readErr, bitmapPageErrChecksum)
	data[2*PageSize+PageCRCOffset] ^= 1

	data[3*PageSize+PageCRCOffset] ^= 1
	readErr = requireBitmapReadCode(t, lowestFreeVerifiedError(tree), bitmapReadErrPage)
	if readErr.page != 3 {
		t.Fatalf("leaf CRC error page = %d, want 3", readErr.page)
	}
	requireBitmapPageCode(t, readErr, bitmapPageErrChecksum)
	if got, ok, err := tree.lowestFree(); err != nil || !ok || got != 2 {
		t.Fatalf("ordinary search after selected-path CRC damage = %d/%t/%v", got, ok, err)
	}
}

func TestVerifiedFreeSearchRejectsUnselectedInvalidChild(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			1,
			[]int{0},
			map[int]uint32{0: 3, 1: 1},
		)
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 3),
			bitmapKindFreePages,
			map[int]uint64{0: 1 << 2},
		)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindFreePages, 32_001, 2)
	readErr := requireBitmapReadCode(t, lowestFreeVerifiedError(tree), bitmapReadErrPage)
	pageErr := requireBitmapPageCode(t, readErr, bitmapPageErrChildPageOutOfBounds)
	if pageErr.childPage != 1 {
		t.Fatalf("invalid unselected child = %d, want 1", pageErr.childPage)
	}
}

func TestBitmapSummaryMismatchAndSelectedCoverageAreTyped(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFeedUsed,
			1,
			[]int{0},
			map[int]uint32{0: 3},
		)
		full := make(map[int]uint64, BitmapLeafWords)
		for index := 0; index < BitmapLeafWords; index++ {
			full[index] = ^uint64(0)
		}
		putBitmapLeaf(t, bitmapImagePage(data, 3), bitmapKindFeedUsed, full)
	})
	tree := logicalBitmapTree(t, data, 2, bitmapKindFeedUsed, 32_001, 0)
	requireBitmapReadCode(t, lowestUnusedError(tree), bitmapReadErrSummaryMismatch)

	putBitmapBranch(
		t,
		bitmapImagePage(data, 2),
		bitmapKindFeedUsed,
		1,
		[]int{2},
		map[int]uint32{2: 3},
	)
	tree = logicalBitmapTree(t, data, 2, bitmapKindFeedUsed, 32_001, 0)
	requireBitmapReadCode(t, lowestUnusedError(tree), bitmapReadErrSelectedCoverageOutsideLimit)
}

func TestBitmapSearchUsesFixedMemory(t *testing.T) {
	meta := emptyDirectMeta(1)
	meta.FreeBitmapRoot = 2
	data := bitmapImage(meta, 431, func(data []byte) {
		putBitmapLeaf(
			t,
			bitmapImagePage(data, 2),
			bitmapKindFreePages,
			map[int]uint64{6: 1 << 45},
		)
	})
	tree, err := openImmutableFreeBitmapTree(data)
	if err != nil {
		t.Fatal(err)
	}
	workspace := bitmapReadWorkspace{}
	allocations := testing.AllocsPerRun(100, func() {
		got, ok, err := tree.lowestFreeWithWorkspace(&workspace)
		if err != nil || !ok || got != 429 {
			panic("unexpected bitmap search result")
		}
	})
	if allocations != 0 {
		t.Fatalf("ordinary bitmap search allocations = %v, want 0", allocations)
	}
}

func TestBitmapReaderPreservesPositionalFailuresAndTornPages(t *testing.T) {
	data := bitmapImage(emptyDirectMeta(1), 4, func(data []byte) {
		putBitmapBranch(t, bitmapImagePage(data, 2), bitmapKindFreePages, 1, []int{0}, map[int]uint32{0: 3})
		putBitmapLeaf(t, bitmapImagePage(data, 3), bitmapKindFreePages, map[int]uint64{0: 1 << 2})
	})
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	forkEvidence := &pageSourceError{code: pageSourceErrForkedHandle}
	forked := &controlledPageSource{access: forkEvidence}
	tree := newBitmapTree(forked, bootstrap, 0, bitmapKindFreePages, 4, 2)
	_, _, err = tree.lowestFree()
	readErr := requireBitmapReadCode(t, err, bitmapReadErrSource)
	var sourceErr *pageSourceError
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != forkEvidence.status() || forked.reads != 0 {
		t.Fatalf("fork evidence/reads = %v/%d", readErr.cause, forked.reads)
	}

	ioEvidence := &pageSourceError{
		code: pageSourceErrIO,
		evidence: pageIOEvidence{
			kind:         pageIOPermissionDenied,
			rawOSCode:    13,
			hasRawOSCode: true,
		},
	}
	failing := &controlledPageSource{readError: ioEvidence}
	tree = newBitmapTree(failing, bootstrap, 2, bitmapKindFreePages, 32_001, 2)
	_, _, err = tree.lowestFree()
	readErr = requireBitmapReadCode(t, err, bitmapReadErrSource)
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != ioEvidence.status() {
		t.Fatalf("I/O evidence changed: %+v", readErr.cause)
	}

	short := newImmutableSlicePageSource(data[:2*PageSize+17], 4)
	shortTree := newBitmapTree(short, bootstrap, 2, bitmapKindFreePages, 32_001, 2)
	_, _, err = shortTree.lowestFree()
	readErr = requireBitmapReadCode(t, err, bitmapReadErrSource)
	if !errors.As(readErr, &sourceErr) || sourceErr.code != pageSourceErrShortRead ||
		sourceErr.offset != 2*PageSize || sourceErr.expected != PageSize || sourceErr.actual != 17 {
		t.Fatalf("short evidence = %+v", sourceErr)
	}

	torn := &controlledPageSource{
		base: newImmutableSlicePageSource(data, 4),
		mutate: func(pageNumber uint32, page *[PageSize]byte) {
			if pageNumber == 2 {
				binary.LittleEndian.PutUint32(page[bitmapChildrenOffset:bitmapChildrenOffset+4], ^uint32(0))
			}
		},
	}
	tree = newBitmapTree(torn, bootstrap, 2, bitmapKindFreePages, 32_001, 2)
	_, _, err = tree.lowestFree()
	readErr = requireBitmapReadCode(t, err, bitmapReadErrSource)
	if !errors.As(readErr, &sourceErr) || sourceErr.code != pageSourceErrPageOutOfBounds || sourceErr.page != ^uint32(0) {
		t.Fatalf("torn-page evidence = %+v", sourceErr)
	}
}

func TestMinimumBitmapLevelAndCoverageAreChecked(t *testing.T) {
	for _, test := range []struct {
		limit uint64
		want  uint16
	}{
		{limit: 0, want: 0},
		{limit: 32_000, want: 0},
		{limit: 32_001, want: 1},
		{limit: 1 << 32, want: 3},
	} {
		got, err := minimumBitmapLevel(test.limit)
		if err != nil || got != test.want {
			t.Fatalf("minimum level(%d) = %d/%v, want %d/nil", test.limit, got, err, test.want)
		}
	}
	if got, err := bitmapCoverage(0); err != nil || got != 32_000 {
		t.Fatalf("coverage(0) = %d/%v, want 32000/nil", got, err)
	}
	if got, err := bitmapCoverage(1); err != nil || got != 8_192_000 {
		t.Fatalf("coverage(1) = %d/%v, want 8192000/nil", got, err)
	}
	requireBitmapReadCode(t, bitmapCoverageError(MaxTreeLevel+1), bitmapReadErrCoverageOverflow)
}

func lowestFreeError[S committedPageSource](tree bitmapTree[S]) error {
	_, _, err := tree.lowestFree()
	return err
}

func lowestFreeVerifiedError[S committedPageSource](tree bitmapTree[S]) error {
	_, _, err := tree.lowestFreeVerified()
	return err
}

func lowestUnusedError[S committedPageSource](tree bitmapTree[S]) error {
	_, _, err := tree.lowestUnused()
	return err
}

func bitmapCoverageError(level uint16) error {
	_, err := bitmapCoverage(level)
	return err
}
