package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func putBitmapHeader(
	t *testing.T,
	page []byte,
	pageType PageType,
	kind bitmapKind,
	count uint16,
	level uint16,
) {
	t.Helper()
	header := PageHeader{
		PageType:  pageType,
		BornTxn:   1,
		ItemCount: count,
		Level:     level,
		Lower:     bitmapBranchLower,
		Upper:     PageSize,
		Aux:       uint32(kind),
	}
	if pageType == PageTypeBitmapLeaf {
		header.Lower = bitmapLeafLower
	}
	if err := header.EncodeInto(page); err != nil {
		t.Fatal(err)
	}
}

func putBitmapLeaf(t *testing.T, page []byte, kind bitmapKind, words map[int]uint64) {
	t.Helper()
	clear(page)
	count := 0
	for _, word := range words {
		if word != 0 {
			count++
		}
	}
	putBitmapHeader(t, page, PageTypeBitmapLeaf, kind, uint16(count), 0)
	for index, word := range words {
		at := bitmapSummaryOffset + index*8
		binary.LittleEndian.PutUint64(page[at:at+8], word)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func putBitmapBranch(
	t *testing.T,
	page []byte,
	kind bitmapKind,
	level uint16,
	summary []int,
	children map[int]uint32,
) {
	t.Helper()
	clear(page)
	count := 0
	for _, child := range children {
		if child != 0 {
			count++
		}
	}
	putBitmapHeader(t, page, PageTypeBitmapBranch, kind, uint16(count), level)
	for _, index := range summary {
		at := bitmapSummaryOffset + (index/64)*8
		word := binary.LittleEndian.Uint64(page[at : at+8])
		binary.LittleEndian.PutUint64(page[at:at+8], word|(uint64(1)<<uint(index%64)))
	}
	for index, child := range children {
		at := bitmapChildrenOffset + index*4
		binary.LittleEndian.PutUint32(page[at:at+4], child)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func requireBitmapPageCode(t *testing.T, err error, want bitmapPageErrorCode) *bitmapPageError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected bitmap-page error %d", want)
	}
	var got *bitmapPageError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *bitmapPageError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("bitmap-page code = %d, want %d", got.code, want)
	}
	return got
}

func TestBitmapLeafExactGeometryCountAndDomain(t *testing.T) {
	page := make([]byte, PageSize)
	putBitmapLeaf(t, page, bitmapKindFreePages, map[int]uint64{0: 1 << 2})
	leaf, err := openBitmapLeaf(page, 1, bitmapKindFreePages)
	if err != nil {
		t.Fatal(err)
	}
	if got := leaf.word(0); got != 4 {
		t.Fatalf("word 0 = %d, want 4", got)
	}
	if err := leaf.verifyLocal(bitmapKindFreePages, 0, 3); err != nil {
		t.Fatal(err)
	}

	putBitmapLeaf(t, page, bitmapKindFreePages, map[int]uint64{0: 0b11})
	leaf, err = openBitmapLeaf(page, 1, bitmapKindFreePages)
	if err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(t, leaf.verifyLocal(bitmapKindFreePages, 0, 3), bitmapPageErrBitOutsideLimit)

	putBitmapLeaf(t, page, bitmapKindMembershipUsed, map[int]uint64{0: 1})
	leaf, err = openBitmapLeaf(page, 1, bitmapKindMembershipUsed)
	if err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(t, leaf.verifyLocal(bitmapKindMembershipUsed, 0, 3), bitmapPageErrBitOutsideLimit)
}

func TestBitmapLeafVerificationChecksCRCReservedAndCount(t *testing.T) {
	page := make([]byte, PageSize)
	putBitmapLeaf(t, page, bitmapKindFeedUsed, map[int]uint64{4: 1})
	leaf, err := openBitmapLeaf(page, 1, bitmapKindFeedUsed)
	if err != nil {
		t.Fatal(err)
	}

	page[PageCRCOffset] ^= 1
	requireBitmapPageCode(t, leaf.verifyLocal(bitmapKindFeedUsed, 0, 1000), bitmapPageErrChecksum)
	page[PageCRCOffset] ^= 1

	page[bitmapLeafLower] = 1
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(t, leaf.verifyLocal(bitmapKindFeedUsed, 0, 1000), bitmapPageErrReservedNonzero)
	page[bitmapLeafLower] = 0

	binary.LittleEndian.PutUint64(page[bitmapSummaryOffset+8:bitmapSummaryOffset+16], 1)
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(t, leaf.verifyLocal(bitmapKindFeedUsed, 0, 1000), bitmapPageErrItemCountMismatch)
}

func TestBitmapBranchUsesLSBFirstSummaryAndExactChildCount(t *testing.T) {
	page := make([]byte, PageSize)
	putBitmapBranch(
		t,
		page,
		bitmapKindFeedUsed,
		1,
		[]int{3, 63, 130},
		map[int]uint32{3: 7},
	)
	branch, err := openBitmapBranch(page, 1, bitmapKindFeedUsed)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		start int
		want  int
	}{
		{start: 0, want: 3},
		{start: 4, want: 63},
		{start: 64, want: 130},
	} {
		got, ok := branch.nextSummary(test.start)
		if !ok || got != test.want {
			t.Fatalf("next summary from %d = %d/%t, want %d/true", test.start, got, ok, test.want)
		}
	}
	if _, ok := branch.nextSummary(131); ok {
		t.Fatal("unexpected summary at or after 131")
	}
	if got := branch.child(3); got != 7 {
		t.Fatalf("child 3 = %d, want 7", got)
	}
	if err := branch.verifyLocal(0, BitmapLeafBits, BitmapLeafBits*256, 8); err != nil {
		t.Fatal(err)
	}

	binary.LittleEndian.PutUint32(page[bitmapChildrenOffset+4*4:bitmapChildrenOffset+5*4], 8)
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(
		t,
		branch.verifyLocal(0, BitmapLeafBits, BitmapLeafBits*256, 9),
		bitmapPageErrItemCountMismatch,
	)
}

func TestBitmapBranchVerificationRejectsCoverageOutsideLimit(t *testing.T) {
	page := make([]byte, PageSize)
	putBitmapBranch(
		t,
		page,
		bitmapKindFreePages,
		1,
		[]int{2},
		map[int]uint32{2: 7},
	)
	branch, err := openBitmapBranch(page, 1, bitmapKindFreePages)
	if err != nil {
		t.Fatal(err)
	}
	requireBitmapPageCode(
		t,
		branch.verifyLocal(0, BitmapLeafBits, BitmapLeafBits+1, 8),
		bitmapPageErrChildOutsideLimit,
	)
}

func TestBitmapPagesRejectWrongKindTypeGeometryAndEmptyPages(t *testing.T) {
	page := make([]byte, PageSize)
	putBitmapLeaf(t, page, bitmapKindFreePages, map[int]uint64{0: 1 << 2})
	requireBitmapPageCode(
		t,
		openBitmapLeafError(page, bitmapKindFeedUsed),
		bitmapPageErrWrongKind,
	)

	page[4] = byte(PageTypeRangeLeaf)
	requireBitmapPageCode(
		t,
		openBitmapLeafError(page, bitmapKindFreePages),
		bitmapPageErrWrongPageType,
	)

	clear(page)
	putBitmapHeader(t, page, PageTypeBitmapLeaf, bitmapKindFreePages, 0, 0)
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	empty, err := openBitmapLeaf(page, 1, bitmapKindFreePages)
	if err != nil {
		t.Fatalf("open legal empty leaf = %v", err)
	}
	if err = empty.verifyLocal(bitmapKindFreePages, 0, 64); err != nil {
		t.Fatalf("verify legal empty leaf = %v", err)
	}

	binary.LittleEndian.PutUint16(page[20:22], bitmapLeafLower-1)
	requireBitmapPageCode(
		t,
		openBitmapLeafError(page, bitmapKindFreePages),
		bitmapPageErrFixedGeometry,
	)

	page[0] = 0
	err = openBitmapLeafError(page, bitmapKindFreePages)
	pageErr := requireBitmapPageCode(t, err, bitmapPageErrHeader)
	var headerErr *PageHeaderError
	if !errors.As(pageErr, &headerErr) || headerErr.Code != PageHeaderErrMagic {
		t.Fatalf("wrapped header error = %v", pageErr)
	}
}

func openBitmapLeafError(page []byte, kind bitmapKind) error {
	_, err := openBitmapLeaf(page, 1, kind)
	return err
}
