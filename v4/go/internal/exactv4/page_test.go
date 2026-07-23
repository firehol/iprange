package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

var pageCRCTestSink uint32

func TestPageCRCDoesNotAllocatePerVerifiedPage(t *testing.T) {
	page := make([]byte, PageSize)
	allocations := testing.AllocsPerRun(100, func() {
		for range 128 {
			pageCRCTestSink = pageCRC32C(page)
		}
	})
	if allocations != 0 {
		t.Fatalf("128 page CRCs allocated %v times, want 0", allocations)
	}
}

var allPageTypes = [...]PageType{
	PageTypeRangeBranch,
	PageTypeRangeLeaf,
	PageTypeCatalogNameBranch,
	PageTypeCatalogNameLeaf,
	PageTypeCatalogIndexBranch,
	PageTypeCatalogIndexLeaf,
	PageTypeMembershipIDBranch,
	PageTypeMembershipIDLeaf,
	PageTypeMembershipHashBranch,
	PageTypeMembershipHashLeaf,
	PageTypeBlobBranch,
	PageTypeBlobLeaf,
	PageTypeMetadataChunk,
	PageTypeBitmapBranch,
	PageTypeBitmapLeaf,
	PageTypeRetirementBranch,
	PageTypeRetirementLeaf,
}

func testPageHeader(pageType PageType) PageHeader {
	level := uint16(0)
	if pageType.IsBranch() {
		level = 1
	}
	return PageHeader{
		PageType:  pageType,
		BornTxn:   7,
		ItemCount: 3,
		Level:     level,
		Lower:     PageHeaderSize,
		Upper:     PageSize,
		Aux:       0x0605_0403,
	}
}

func testPage(t *testing.T, pageType PageType) [PageSize]byte {
	t.Helper()
	var page [PageSize]byte
	if err := testPageHeader(pageType).EncodeInto(page[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePageCRC32C(page[:]); err != nil {
		t.Fatal(err)
	}
	return page
}

func requirePageHeaderCode(t *testing.T, err error, want PageHeaderErrorCode) *PageHeaderError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected page-header error %d", want)
	}
	var got *PageHeaderError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *PageHeaderError: %v", err, err)
	}
	if got.Code != want {
		t.Fatalf("page-header code = %d, want %d", got.Code, want)
	}
	return got
}

func TestExactPageHeaderLayoutAndCRC(t *testing.T) {
	var page [PageSize]byte
	page[100] = 0x5a
	expected := testPageHeader(PageTypeRangeBranch)
	if err := expected.EncodeInto(page[:]); err != nil {
		t.Fatal(err)
	}
	checksum, err := WritePageCRC32C(page[:])
	if err != nil {
		t.Fatal(err)
	}

	if string(page[0:4]) != PageMagic || page[4] != 1 || page[5] != 0 {
		t.Fatalf("fixed header = %q/%d/%d", page[0:4], page[4], page[5])
	}
	if got := binary.LittleEndian.Uint16(page[6:8]); got != 32 {
		t.Fatalf("header size = %d", got)
	}
	if got := binary.LittleEndian.Uint64(page[8:16]); got != 7 {
		t.Fatalf("born txn = %d", got)
	}
	if got := binary.LittleEndian.Uint16(page[16:18]); got != 3 {
		t.Fatalf("item count = %d", got)
	}
	if got := binary.LittleEndian.Uint16(page[18:20]); got != 1 {
		t.Fatalf("level = %d", got)
	}
	if lower, upper := binary.LittleEndian.Uint16(page[20:22]), binary.LittleEndian.Uint16(page[22:24]); lower != 32 || upper != 4096 {
		t.Fatalf("bounds = %d/%d", lower, upper)
	}
	if got := binary.LittleEndian.Uint32(page[24:28]); got != 0x0605_0403 {
		t.Fatalf("aux = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(page[28:32]); got != checksum {
		t.Fatalf("stored CRC = %#x, want %#x", got, checksum)
	}
	if page[100] != 0x5a {
		t.Fatal("header encoding modified a type-specific body byte")
	}
	if !VerifyPageCRC32C(page[:]) {
		t.Fatal("explicit CRC verification rejected sealed page")
	}

	decoded, err := DecodePageHeader(page[:], 7)
	if err != nil {
		t.Fatal(err)
	}
	expected.PageCRC32C = checksum
	if decoded != expected {
		t.Fatalf("decoded = %+v, want %+v", decoded, expected)
	}
}

func TestAllAndOnlySeventeenPageTypesDecode(t *testing.T) {
	for i, pageType := range allPageTypes {
		wire := uint8(i + 1)
		if uint8(pageType) != wire {
			t.Fatalf("page type %d has wire value %d", i, pageType)
		}
		page := testPage(t, pageType)
		decoded, err := DecodePageHeader(page[:], 7)
		if err != nil || decoded.PageType != pageType {
			t.Fatalf("page type %d decoded as %+v/%v", pageType, decoded, err)
		}
	}
	for _, wire := range []uint8{0, 18, 255} {
		page := testPage(t, PageTypeRangeLeaf)
		page[4] = wire
		_, err := DecodePageHeader(page[:], 7)
		got := requirePageHeaderCode(t, err, PageHeaderErrPageType)
		if got.WireType != wire {
			t.Fatalf("wire type = %d, want %d", got.WireType, wire)
		}
	}
}

func TestPageHeaderFixedFieldsFailClosed(t *testing.T) {
	valid := testPage(t, PageTypeRangeLeaf)

	bad := valid
	bad[0] ^= 1
	_, err := DecodePageHeader(bad[:], 7)
	requirePageHeaderCode(t, err, PageHeaderErrMagic)

	bad = valid
	bad[5] = 1
	_, err = DecodePageHeader(bad[:], 7)
	got := requirePageHeaderCode(t, err, PageHeaderErrFlags)
	if got.Flags != 1 {
		t.Fatalf("flags = %d", got.Flags)
	}

	for _, size := range []uint16{0, 16, 31, 33, 65535} {
		bad = valid
		binary.LittleEndian.PutUint16(bad[6:8], size)
		_, err = DecodePageHeader(bad[:], 7)
		got = requirePageHeaderCode(t, err, PageHeaderErrHeaderSize)
		if got.HeaderSize != size {
			t.Fatalf("header size = %d, want %d", got.HeaderSize, size)
		}
	}
}

func TestBornTransactionMustBeCurrentOrOlderAndNonzero(t *testing.T) {
	page := testPage(t, PageTypeRangeLeaf)
	binary.LittleEndian.PutUint64(page[8:16], 0)
	_, err := DecodePageHeader(page[:], 7)
	requirePageHeaderCode(t, err, PageHeaderErrBornTransactionZero)

	binary.LittleEndian.PutUint64(page[8:16], 8)
	_, err = DecodePageHeader(page[:], 7)
	got := requirePageHeaderCode(t, err, PageHeaderErrBornTransactionFuture)
	if got.BornTxn != 8 || got.SelectedTxn != 7 {
		t.Fatalf("future transaction = %d/%d", got.BornTxn, got.SelectedTxn)
	}

	binary.LittleEndian.PutUint64(page[8:16], 6)
	decoded, err := DecodePageHeader(page[:], 7)
	if err != nil || decoded.BornTxn != 6 {
		t.Fatalf("older transaction = %+v/%v", decoded, err)
	}
}

func TestBranchAndNonBranchLevelsAreExactAndBounded(t *testing.T) {
	for _, pageType := range allPageTypes {
		page := testPage(t, pageType)
		if pageType.IsBranch() {
			binary.LittleEndian.PutUint16(page[18:20], 0)
			_, err := DecodePageHeader(page[:], 7)
			got := requirePageHeaderCode(t, err, PageHeaderErrBranchLevelZero)
			if got.PageType != pageType {
				t.Fatalf("branch type = %d, want %d", got.PageType, pageType)
			}

			binary.LittleEndian.PutUint16(page[18:20], MaxTreeLevel)
			decoded, err := DecodePageHeader(page[:], 7)
			if err != nil || decoded.Level != MaxTreeLevel {
				t.Fatalf("maximum branch level = %+v/%v", decoded, err)
			}
		} else {
			binary.LittleEndian.PutUint16(page[18:20], 1)
			_, err := DecodePageHeader(page[:], 7)
			got := requirePageHeaderCode(t, err, PageHeaderErrNonBranchLevelNonzero)
			if got.PageType != pageType || got.Level != 1 {
				t.Fatalf("non-branch type/level = %d/%d", got.PageType, got.Level)
			}
		}

		binary.LittleEndian.PutUint16(page[18:20], MaxTreeLevel+1)
		_, err := DecodePageHeader(page[:], 7)
		got := requirePageHeaderCode(t, err, PageHeaderErrLevelTooHigh)
		if got.Level != MaxTreeLevel+1 {
			t.Fatalf("high level = %d", got.Level)
		}
	}
}

func TestPageUsedAreaBoundariesStayInsidePage(t *testing.T) {
	valid := testPage(t, PageTypeRangeLeaf)
	for _, bounds := range [][2]uint16{{31, 4096}, {33, 32}, {32, 4097}, {65535, 65535}} {
		bad := valid
		binary.LittleEndian.PutUint16(bad[20:22], bounds[0])
		binary.LittleEndian.PutUint16(bad[22:24], bounds[1])
		_, err := DecodePageHeader(bad[:], 7)
		got := requirePageHeaderCode(t, err, PageHeaderErrBounds)
		if got.Lower != bounds[0] || got.Upper != bounds[1] {
			t.Fatalf("bad bounds = %d/%d", got.Lower, got.Upper)
		}
	}
	for _, bounds := range [][2]uint16{{32, 32}, {32, 4096}, {4096, 4096}} {
		legal := valid
		binary.LittleEndian.PutUint16(legal[20:22], bounds[0])
		binary.LittleEndian.PutUint16(legal[22:24], bounds[1])
		decoded, err := DecodePageHeader(legal[:], 7)
		if err != nil || decoded.Lower != bounds[0] || decoded.Upper != bounds[1] {
			t.Fatalf("legal bounds %d/%d = %+v/%v", bounds[0], bounds[1], decoded, err)
		}
	}
}

func TestOrdinaryDecodeSkipsCRCButExplicitVerificationDoesNot(t *testing.T) {
	valid := testPage(t, PageTypeRangeLeaf)
	if !VerifyPageCRC32C(valid[:]) {
		t.Fatal("valid CRC rejected")
	}

	badBody := valid
	badBody[PageHeaderSize] ^= 1
	if _, err := DecodePageHeader(badBody[:], 7); err != nil {
		t.Fatalf("ordinary decode verified body CRC: %v", err)
	}
	if VerifyPageCRC32C(badBody[:]) {
		t.Fatal("explicit CRC verification accepted changed body")
	}

	badField := valid
	badField[PageCRCOffset] ^= 1
	if _, err := DecodePageHeader(badField[:], 7); err != nil {
		t.Fatalf("ordinary decode verified stored CRC: %v", err)
	}
	if VerifyPageCRC32C(badField[:]) {
		t.Fatal("explicit CRC verification accepted changed CRC field")
	}

	if _, err := WritePageCRC32C(badBody[:]); err != nil {
		t.Fatal(err)
	}
	if !VerifyPageCRC32C(badBody[:]) {
		t.Fatal("resealed changed body failed explicit verification")
	}
}

func TestPageCodecRejectsNonPageSlices(t *testing.T) {
	for _, size := range []int{0, int(PageHeaderSize), PageSize - 1, PageSize + 1} {
		page := make([]byte, size)
		_, err := DecodePageHeader(page, 1)
		got := requirePageHeaderCode(t, err, PageHeaderErrPageSize)
		if got.Length != size {
			t.Fatalf("decode length = %d, want %d", got.Length, size)
		}
		if err := testPageHeader(PageTypeRangeLeaf).EncodeInto(page); err == nil {
			t.Fatalf("encode accepted length %d", size)
		}
		if _, err := WritePageCRC32C(page); err == nil {
			t.Fatalf("CRC write accepted length %d", size)
		}
		if VerifyPageCRC32C(page) {
			t.Fatalf("CRC verify accepted length %d", size)
		}
	}
}
