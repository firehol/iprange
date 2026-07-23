package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func putBlobHeader(
	t *testing.T,
	page []byte,
	pageType PageType,
	kind blobKind,
	count uint16,
	level uint16,
	lower uint16,
) {
	t.Helper()
	clear(page)
	header := PageHeader{
		PageType:  pageType,
		BornTxn:   1,
		ItemCount: count,
		Level:     level,
		Lower:     lower,
		Upper:     PageSize,
		Aux:       uint32(kind),
	}
	if err := header.EncodeInto(page); err != nil {
		t.Fatal(err)
	}
}

func putBlobBranchPage(
	t *testing.T,
	page []byte,
	kind blobKind,
	level uint16,
	entries []blobBranchEntry,
) {
	t.Helper()
	lower := int(PageHeaderSize) + len(entries)*blobBranchEntrySize
	putBlobHeader(t, page, PageTypeBlobBranch, kind, uint16(len(entries)), level, uint16(lower))
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*blobBranchEntrySize
		binary.LittleEndian.PutUint64(page[at:at+8], entry.logicalOffset)
		binary.LittleEndian.PutUint32(page[at+8:at+12], entry.childPage)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func putBlobLeafPage(
	t *testing.T,
	page []byte,
	kind blobKind,
	logicalOffset uint64,
	data []byte,
) {
	t.Helper()
	putBlobHeader(
		t,
		page,
		PageTypeBlobLeaf,
		kind,
		1,
		0,
		uint16(blobLeafDataOffset+len(data)),
	)
	binary.LittleEndian.PutUint64(page[32:40], logicalOffset)
	binary.LittleEndian.PutUint16(page[40:42], uint16(len(data)))
	copy(page[blobLeafDataOffset:], data)
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func requireBlobPageCode(t *testing.T, err error, want blobPageErrorCode) *blobPageError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected blob-page error %d", want)
	}
	var got *blobPageError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *blobPageError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("blob-page code = %d, want %d", got.code, want)
	}
	return got
}

func TestBlobBranchExactLayoutLookupAndVerification(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobBranchPage(t, page, blobKindMembershipBitmap, 2, []blobBranchEntry{
		{logicalOffset: 0, childPage: 3},
		{logicalOffset: 4048, childPage: 4},
		{logicalOffset: 8096, childPage: 5},
	})
	if got := binary.LittleEndian.Uint64(page[32:40]); got != 0 {
		t.Fatalf("first logical offset = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(page[40:44]); got != 3 {
		t.Fatalf("first child = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(page[44:48]); got != 0 {
		t.Fatalf("first reserved field = %d, want 0", got)
	}

	branch, err := openBlobBranch(page, 1, blobKindMembershipBitmap, 6)
	if err != nil {
		t.Fatal(err)
	}
	if branch.len() != 3 || branch.level != 2 {
		t.Fatalf("branch count/level = %d/%d, want 3/2", branch.len(), branch.level)
	}
	entry, err := branch.entry(1)
	if err != nil {
		t.Fatal(err)
	}
	if entry.logicalOffset != 4048 || entry.childPage != 4 {
		t.Fatalf("entry 1 = %+v", entry)
	}
	for _, test := range []struct {
		offset uint64
		want   int
	}{
		{offset: 0, want: 0},
		{offset: 8095, want: 1},
		{offset: ^uint64(0), want: 2},
	} {
		got, err := branch.predecessorFor(test.offset)
		if err != nil || got != test.want {
			t.Fatalf("predecessor(%d) = %d/%v, want %d/nil", test.offset, got, err, test.want)
		}
	}
	if err := branch.verifyLocal(); err != nil {
		t.Fatal(err)
	}
}

func TestBlobBranchOrdinaryOpenDoesNotScanWholePage(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobBranchPage(t, page, blobKindRetirementPageList, 1, []blobBranchEntry{
		{logicalOffset: 0, childPage: 3},
		{logicalOffset: 4048, childPage: 4},
	})
	page[60] = 1 // Reserved field in the second, currently unselected entry.
	page[PageSize-1] = 1
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	branch, err := openBlobBranch(page, 1, blobKindRetirementPageList, 5)
	if err != nil {
		t.Fatalf("ordinary open scanned unselected bytes: %v", err)
	}
	if _, err := branch.entry(0); err != nil {
		t.Fatalf("selected entry failed: %v", err)
	}
	requireBlobPageCode(t, branch.verifyLocal(), blobPageErrReservedNonzero)
}

func TestBlobBranchVerifiedChecksAllEntriesAndStrictOffsets(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobBranchPage(t, page, blobKindRetirementPageList, 1, []blobBranchEntry{
		{logicalOffset: 0, childPage: 3},
		{logicalOffset: 4048, childPage: 5},
	})
	branch, err := openBlobBranch(page, 1, blobKindRetirementPageList, 5)
	if err != nil {
		t.Fatal(err)
	}
	pageErr := requireBlobPageCode(t, branch.verifyLocal(), blobPageErrChildOutOfBounds)
	if pageErr.childPage != 5 {
		t.Fatalf("bad child = %d, want 5", pageErr.childPage)
	}

	putBlobBranchPage(t, page, blobKindRetirementPageList, 1, []blobBranchEntry{
		{logicalOffset: 0, childPage: 3},
		{logicalOffset: 0, childPage: 4},
	})
	branch, err = openBlobBranch(page, 1, blobKindRetirementPageList, 5)
	if err != nil {
		t.Fatal(err)
	}
	requireBlobPageCode(t, branch.verifyLocal(), blobPageErrOffsetsNotStrict)
}

func TestBlobBranchRejectsTypeKindEmptyAndGeometry(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobBranchPage(t, page, blobKindMembershipBitmap, 1, []blobBranchEntry{{childPage: 3}})
	_, err := openBlobBranch(page, 1, blobKindRetirementPageList, 4)
	requireBlobPageCode(t, err, blobPageErrWrongKind)

	page[4] = byte(PageTypeRangeBranch)
	_, err = openBlobBranch(page, 1, blobKindMembershipBitmap, 4)
	requireBlobPageCode(t, err, blobPageErrWrongPageType)

	putBlobHeader(t, page, PageTypeBlobBranch, blobKindMembershipBitmap, 0, 1, PageHeaderSize)
	_, err = openBlobBranch(page, 1, blobKindMembershipBitmap, 4)
	requireBlobPageCode(t, err, blobPageErrEmptyBranch)

	putBlobBranchPage(t, page, blobKindMembershipBitmap, 1, []blobBranchEntry{{childPage: 3}})
	binary.LittleEndian.PutUint16(page[20:22], PageHeaderSize)
	_, err = openBlobBranch(page, 1, blobKindMembershipBitmap, 4)
	requireBlobPageCode(t, err, blobPageErrFixedGeometry)
}

func TestBlobLeafExactLayoutAndExplicitVerification(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobLeafPage(t, page, blobKindMembershipBitmap, 4048, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	leaf, err := openBlobLeaf(page, 1, blobKindMembershipBitmap)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.logicalOffset != 4048 || leaf.dataLength != 8 {
		t.Fatalf("leaf offset/length = %d/%d", leaf.logicalOffset, leaf.dataLength)
	}
	if got := leaf.data(); len(got) != 8 || got[0] != 1 || got[7] != 8 {
		t.Fatalf("leaf data = %v", got)
	}
	if err := leaf.verifyLocal(); err != nil {
		t.Fatal(err)
	}

	page[PageCRCOffset] ^= 1
	leaf, err = openBlobLeaf(page, 1, blobKindMembershipBitmap)
	if err != nil {
		t.Fatalf("ordinary leaf open checked CRC: %v", err)
	}
	requireBlobPageCode(t, leaf.verifyLocal(), blobPageErrChecksum)
}

func TestBlobLeafRejectsCountLengthAlignmentGeometryAndHeaderReserved(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, []byte{1, 2, 3, 4})
	binary.LittleEndian.PutUint16(page[16:18], 0)
	_, err := openBlobLeaf(page, 1, blobKindRetirementPageList)
	requireBlobPageCode(t, err, blobPageErrLeafItemCount)

	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, []byte{1, 2, 3, 4})
	binary.LittleEndian.PutUint16(page[40:42], 0)
	_, err = openBlobLeaf(page, 1, blobKindRetirementPageList)
	requireBlobPageCode(t, err, blobPageErrDataLength)

	putBlobHeader(t, page, PageTypeBlobLeaf, blobKindRetirementPageList, 1, 0, blobLeafDataOffset+2)
	binary.LittleEndian.PutUint16(page[40:42], 2)
	_, err = openBlobLeaf(page, 1, blobKindRetirementPageList)
	requireBlobPageCode(t, err, blobPageErrDataAlignment)

	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, []byte{1, 2, 3, 4})
	binary.LittleEndian.PutUint16(page[20:22], blobLeafDataOffset+5)
	_, err = openBlobLeaf(page, 1, blobKindRetirementPageList)
	requireBlobPageCode(t, err, blobPageErrFixedGeometry)

	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, []byte{1, 2, 3, 4})
	page[42] = 1
	_, err = openBlobLeaf(page, 1, blobKindRetirementPageList)
	requireBlobPageCode(t, err, blobPageErrReservedNonzero)
}

func TestBlobLeafOrdinaryIgnoresTailButVerifiedRejectsIt(t *testing.T) {
	page := make([]byte, PageSize)
	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, []byte{2, 0, 0, 0})
	page[PageSize-1] = 1
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
	leaf, err := openBlobLeaf(page, 1, blobKindRetirementPageList)
	if err != nil {
		t.Fatalf("ordinary leaf open scanned tail: %v", err)
	}
	requireBlobPageCode(t, leaf.verifyLocal(), blobPageErrReservedNonzero)
}
