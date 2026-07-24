package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func putRangeHeader(
	t testing.TB,
	page []byte,
	pageType PageType,
	count uint16,
	level uint16,
	lower uint16,
	family AddressFamily,
) {
	t.Helper()
	if err := (PageHeader{
		PageType:  pageType,
		BornTxn:   1,
		ItemCount: count,
		Level:     level,
		Lower:     lower,
		Upper:     PageSize,
		Aux:       uint32(family),
	}).EncodeInto(page); err != nil {
		t.Fatal(err)
	}
}

func putRangeLeaf[K rangeKey[K]](t testing.TB, page []byte, records []rangeRecord[K]) {
	t.Helper()
	var key K
	putRangeHeader(
		t,
		page,
		PageTypeRangeLeaf,
		uint16(len(records)),
		0,
		uint16(int(PageHeaderSize)+len(records)*rangeRecordSize[K]()),
		key.family(),
	)
	width := key.width()
	for index, record := range records {
		at := int(PageHeaderSize) + index*rangeRecordSize[K]()
		record.from.writeLE(page[at : at+width])
		record.to.writeLE(page[at+width : at+2*width])
		binary.LittleEndian.PutUint32(page[at+2*width:at+2*width+4], record.value)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

type ipv4BranchTestEntry struct {
	lowerFence         IPv4
	childPage          uint32
	subtreeRecordCount uint64
	firstFrom          IPv4
	lastFrom           IPv4
	lastTo             IPv4
}

func putIPv4Branch(t testing.TB, page []byte, level uint16, entries []ipv4BranchTestEntry) {
	t.Helper()
	putRangeHeader(
		t,
		page,
		PageTypeRangeBranch,
		uint16(len(entries)),
		level,
		uint16(int(PageHeaderSize)+len(entries)*rangeBranchEntrySize[IPv4]()),
		AddressFamilyIPv4,
	)
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*rangeBranchEntrySize[IPv4]()
		entry.lowerFence.writeLE(page[at : at+4])
		binary.LittleEndian.PutUint32(page[at+4:at+8], entry.childPage)
		binary.LittleEndian.PutUint64(page[at+8:at+16], entry.subtreeRecordCount)
		entry.firstFrom.writeLE(page[at+16 : at+20])
		entry.lastFrom.writeLE(page[at+20 : at+24])
		entry.lastTo.writeLE(page[at+24 : at+28])
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func requireRangePageCode(t *testing.T, err error, want rangePageErrorCode) *rangePageError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-page error %d", want)
	}
	var got *rangePageError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangePageError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-page code = %d, want %d", got.code, want)
	}
	return got
}

func requireRangePageWriteCode(t *testing.T, err error, want rangePageWriteErrorCode) *rangePageWriteError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-page write error %d", want)
	}
	var got *rangePageWriteError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangePageWriteError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-page write code = %d, want %d", got.code, want)
	}
	return got
}

func TestRangeLeafAcceptsLegalEmptyPagesForBothFamilies(t *testing.T) {
	for _, test := range []struct {
		name   string
		family AddressFamily
		open   func([]byte) error
	}{
		{
			name:   "IPv4",
			family: AddressFamilyIPv4,
			open: func(page []byte) error {
				leaf, err := openRangeLeaf[IPv4](page, 1, AddressFamilyIPv4, ValueKindDirect)
				if err == nil && leaf.count != 0 {
					t.Fatalf("leaf count = %d, want 0", leaf.count)
				}
				return err
			},
		},
		{
			name:   "IPv6",
			family: AddressFamilyIPv6,
			open: func(page []byte) error {
				leaf, err := openRangeLeaf[IPv6](page, 1, AddressFamilyIPv6, ValueKindMembership)
				if err == nil && leaf.count != 0 {
					t.Fatalf("leaf count = %d, want 0", leaf.count)
				}
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := make([]byte, PageSize)
			putRangeHeader(t, page, PageTypeRangeLeaf, 0, 0, PageHeaderSize, test.family)
			if _, err := WritePageCRC32C(page); err != nil {
				t.Fatal(err)
			}
			if err := test.open(page); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRangeLeafRejectsBadGeometryAndRecords(t *testing.T) {
	page := make([]byte, PageSize)
	putRangeLeaf(t, page, []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})

	badGeometry := append([]byte(nil), page...)
	binary.LittleEndian.PutUint16(badGeometry[20:22], PageHeaderSize)
	_, err := openRangeLeaf[IPv4](badGeometry, 1, AddressFamilyIPv4, ValueKindDirect)
	requireRangePageCode(t, err, rangePageErrFixedGeometry)

	reversed := append([]byte(nil), page...)
	IPv4(30).writeLE(reversed[PageHeaderSize : PageHeaderSize+4])
	leaf, err := openRangeLeaf[IPv4](reversed, 1, AddressFamilyIPv4, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	_, err = leaf.record(0)
	requireRangePageCode(t, err, rangePageErrRangeReversed)

	zeroMembership := append([]byte(nil), page...)
	binary.LittleEndian.PutUint32(zeroMembership[PageHeaderSize+8:PageHeaderSize+12], 0)
	leaf, err = openRangeLeaf[IPv4](zeroMembership, 1, AddressFamilyIPv4, ValueKindMembership)
	if err != nil {
		t.Fatal(err)
	}
	_, err = leaf.record(0)
	requireRangePageCode(t, err, rangePageErrMembershipValueZero)
}

func TestRangePagesRejectWrongFamilyTypeAndAux(t *testing.T) {
	leafPage := make([]byte, PageSize)
	putRangeLeaf(t, leafPage, []rangeRecord[IPv4]{})

	_, err := openRangeLeaf[IPv6](leafPage, 1, AddressFamilyIPv4, ValueKindDirect)
	requireRangePageCode(t, err, rangePageErrWrongKeyFamily)
	_, err = openRangeBranch[IPv4](leafPage, 1, AddressFamilyIPv4, 3)
	requireRangePageCode(t, err, rangePageErrWrongPageType)

	badLeafAux := append([]byte(nil), leafPage...)
	binary.LittleEndian.PutUint32(badLeafAux[24:28], uint32(AddressFamilyIPv6))
	_, err = openRangeLeaf[IPv4](badLeafAux, 1, AddressFamilyIPv4, ValueKindDirect)
	requireRangePageCode(t, err, rangePageErrWrongAux)

	branchPage := make([]byte, PageSize)
	putIPv4Branch(t, branchPage, 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 2},
	})
	_, err = openRangeLeaf[IPv4](branchPage, 1, AddressFamilyIPv4, ValueKindDirect)
	requireRangePageCode(t, err, rangePageErrWrongPageType)
	binary.LittleEndian.PutUint32(branchPage[24:28], uint32(AddressFamilyIPv6))
	_, err = openRangeBranch[IPv4](branchPage, 1, AddressFamilyIPv4, 3)
	requireRangePageCode(t, err, rangePageErrWrongAux)
}

func TestRangeBranchNavigationSkipsLegalEmptyChildren(t *testing.T) {
	page := make([]byte, PageSize)
	putIPv4Branch(t, page, 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		{lowerFence: 100, childPage: 4},
		{lowerFence: 200, childPage: 5, subtreeRecordCount: 1, firstFrom: 210, lastFrom: 210, lastTo: 220},
	})
	branch, err := openRangeBranch[IPv4](page, 1, AddressFamilyIPv4, 6)
	if err != nil {
		t.Fatal(err)
	}

	if index, found, err := branch.nextNonempty(1); err != nil || !found || index != 2 {
		t.Fatalf("next nonempty = %d/%t/%v, want 2/true/nil", index, found, err)
	}
	if index, found, err := branch.previousNonempty(2); err != nil || !found || index != 0 {
		t.Fatalf("previous nonempty = %d/%t/%v, want 0/true/nil", index, found, err)
	}
	if index, found, err := branch.predecessorFor(150); err != nil || !found || index != 0 {
		t.Fatalf("predecessor = %d/%t/%v, want 0/true/nil", index, found, err)
	}
}

func TestRangeBranchAcceptsAllEmptyButNotZeroEntries(t *testing.T) {
	page := make([]byte, PageSize)
	putIPv4Branch(t, page, 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 3},
		{lowerFence: 100, childPage: 4},
	})
	branch, err := openRangeBranch[IPv4](page, 1, AddressFamilyIPv4, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := branch.nextNonempty(0); err != nil || found {
		t.Fatalf("all-empty next = %t/%v, want false/nil", found, err)
	}

	zero := make([]byte, PageSize)
	putRangeHeader(t, zero, PageTypeRangeBranch, 0, 1, PageHeaderSize, AddressFamilyIPv4)
	_, err = openRangeBranch[IPv4](zero, 1, AddressFamilyIPv4, 5)
	requireRangePageCode(t, err, rangePageErrEmptyBranch)
}

func TestRangeBranchRejectsUnsafeEntries(t *testing.T) {
	base := make([]byte, PageSize)
	putIPv4Branch(t, base, 1, []ipv4BranchTestEntry{
		{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
	})

	for _, test := range []struct {
		name string
		edit func([]byte)
		code rangePageErrorCode
	}{
		{
			name: "child below data pages",
			edit: func(page []byte) { binary.LittleEndian.PutUint32(page[36:40], 1) },
			code: rangePageErrChildOutOfBounds,
		},
		{
			name: "child beyond page count",
			edit: func(page []byte) { binary.LittleEndian.PutUint32(page[36:40], 5) },
			code: rangePageErrChildOutOfBounds,
		},
		{
			name: "reserved",
			edit: func(page []byte) { binary.LittleEndian.PutUint32(page[60:64], 1) },
			code: rangePageErrReservedNonzero,
		},
		{
			name: "empty nonzero summary",
			edit: func(page []byte) {
				binary.LittleEndian.PutUint64(page[40:48], 0)
			},
			code: rangePageErrEmptySummaryNonzero,
		},
		{
			name: "summary order",
			edit: func(page []byte) {
				IPv4(11).writeLE(page[48:52])
				IPv4(10).writeLE(page[52:56])
			},
			code: rangePageErrSummaryOrder,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := append([]byte(nil), base...)
			test.edit(page)
			branch, err := openRangeBranch[IPv4](page, 1, AddressFamilyIPv4, 5)
			if err != nil {
				t.Fatal(err)
			}
			_, err = branch.entry(0)
			requireRangePageCode(t, err, test.code)
		})
	}
}

func TestIPv6BranchEntryUsesLowLimbFirstWireLayout(t *testing.T) {
	page := make([]byte, PageSize)
	putRangeHeader(t, page, PageTypeRangeBranch, 1, 2, 112, AddressFamilyIPv6)
	at := int(PageHeaderSize)
	lower := IPv6{Hi: 0x1112_1314_1516_1718, Lo: 0x0102_0304_0506_0708}
	first := IPv6{Hi: 0x3132_3334_3536_3738, Lo: 0x2122_2324_2526_2728}
	last := IPv6{Hi: 0x5152_5354_5556_5758, Lo: 0x4142_4344_4546_4748}
	to := IPv6{Hi: 0x7172_7374_7576_7778, Lo: 0x6162_6364_6566_6768}
	lower.writeLE(page[at : at+16])
	binary.LittleEndian.PutUint32(page[at+16:at+20], 3)
	binary.LittleEndian.PutUint64(page[at+24:at+32], 7)
	first.writeLE(page[at+32 : at+48])
	last.writeLE(page[at+48 : at+64])
	to.writeLE(page[at+64 : at+80])

	if got := binary.LittleEndian.Uint64(page[at : at+8]); got != lower.Lo {
		t.Fatalf("first IPv6 wire limb = %#x, want low %#x", got, lower.Lo)
	}
	if got := binary.LittleEndian.Uint64(page[at+8 : at+16]); got != lower.Hi {
		t.Fatalf("second IPv6 wire limb = %#x, want high %#x", got, lower.Hi)
	}

	branch, err := openRangeBranch[IPv6](page, 1, AddressFamilyIPv6, 4)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := branch.entry(0)
	if err != nil {
		t.Fatal(err)
	}
	if entry.lowerFence != lower || entry.firstFrom != first || entry.lastFrom != last || entry.lastTo != to {
		t.Fatalf("decoded IPv6 entry = %+v", entry)
	}
}

func TestRangeLeafEncoderIsCanonicalAtomicAndRoundTrips(t *testing.T) {
	if got := rangeLeafCapacity[IPv4](); got != 338 {
		t.Fatalf("IPv4 leaf capacity = %d, want 338", got)
	}
	if got := rangeLeafCapacity[IPv6](); got != 112 {
		t.Fatalf("IPv6 leaf capacity = %d, want 112", got)
	}
	records := []rangeRecord[IPv4]{
		{from: 10, to: 20, value: 0},
		{from: 21, to: 30, value: 7},
	}
	page := make([]byte, PageSize)
	for index := range page {
		page[index] = 0xa5
	}
	if err := encodeRangeLeaf(page, 7, ValueKindDirect, records); err != nil {
		t.Fatal(err)
	}
	if !VerifyPageCRC32C(page) {
		t.Fatal("encoded leaf CRC is invalid")
	}
	header, err := DecodePageHeader(page, 7)
	if err != nil {
		t.Fatal(err)
	}
	if header.PageType != PageTypeRangeLeaf || header.ItemCount != 2 {
		t.Fatalf("encoded leaf header = %#v", header)
	}
	for _, value := range page[header.Lower:] {
		if value != 0 {
			t.Fatal("encoded leaf did not zero unused bytes")
		}
	}
	leaf, err := openRangeLeaf[IPv4](page, 7, AddressFamilyIPv4, ValueKindDirect)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range records {
		got, err := leaf.record(index)
		if err != nil || got != want {
			t.Fatalf("record %d = %#v/%v, want %#v", index, got, err, want)
		}
	}

	before := append([]byte(nil), page...)
	requireRangePageWriteCode(t, encodeRangeLeaf(page, 7, ValueKindMembership, []rangeRecord[IPv4]{{from: 10, to: 20}}), rangePageWriteErrMembershipValueZero)
	if string(page) != string(before) {
		t.Fatal("membership-value rejection changed destination page")
	}
	requireRangePageWriteCode(t, encodeRangeLeaf(page, 7, ValueKindDirect, []rangeRecord[IPv4]{{from: 20, to: 10, value: 7}}), rangePageWriteErrRangeReversed)
	if string(page) != string(before) {
		t.Fatal("reversed-range rejection changed destination page")
	}
	requireRangePageWriteCode(t, encodeRangeLeaf(page, 7, ValueKindDirect, []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}, {from: 20, to: 30, value: 2}}), rangePageWriteErrRangeOverlap)
	if string(page) != string(before) {
		t.Fatal("overlap rejection changed destination page")
	}
	requireRangePageWriteCode(t, encodeRangeLeaf(page, 7, ValueKindDirect, []rangeRecord[IPv4]{{from: 10, to: 20, value: 7}, {from: 21, to: 30, value: 7}}), rangePageWriteErrAdjacentEqualValue)
	if string(page) != string(before) {
		t.Fatal("adjacent-value rejection changed destination page")
	}

	tooMany := make([]rangeRecord[IPv4], rangeLeafCapacity[IPv4]()+1)
	err = encodeRangeLeaf(page, 7, ValueKindDirect, tooMany)
	got := requireRangePageWriteCode(t, err, rangePageWriteErrTooManyRecords)
	if got.required != len(tooMany) || got.actual != rangeLeafCapacity[IPv4]() {
		t.Fatalf("capacity = %#v", got)
	}
	if string(page) != string(before) {
		t.Fatal("capacity rejection changed destination page")
	}
}

func TestRangeBranchEncoderIsCanonicalAtomicAndRoundTrips(t *testing.T) {
	if got := rangeBranchCapacity[IPv4](); got != 127 {
		t.Fatalf("IPv4 branch capacity = %d, want 127", got)
	}
	if got := rangeBranchCapacity[IPv6](); got != 50 {
		t.Fatalf("IPv6 branch capacity = %d, want 50", got)
	}
	entries := []rangeBranchEntry[IPv4]{
		{lowerFence: 0, childPage: 2, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		{lowerFence: 100, childPage: 3},
		{lowerFence: 200, childPage: 4, subtreeRecordCount: 1, firstFrom: 210, lastFrom: 210, lastTo: 220},
	}
	page := make([]byte, PageSize)
	for index := range page {
		page[index] = 0x5a
	}
	if err := encodeRangeBranch(page, 7, 1, 6, IPv4(0), IPv4(0), false, entries); err != nil {
		t.Fatal(err)
	}
	if !VerifyPageCRC32C(page) {
		t.Fatal("encoded branch CRC is invalid")
	}
	branch, err := openRangeBranch[IPv4](page, 7, AddressFamilyIPv4, 6)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range entries {
		got, err := branch.entry(index)
		if err != nil || got != want {
			t.Fatalf("entry %d = %#v/%v, want %#v", index, got, err, want)
		}
	}

	before := append([]byte(nil), page...)
	wrongFirst := append([]rangeBranchEntry[IPv4](nil), entries...)
	wrongFirst[0].lowerFence = 1
	requireRangePageWriteCode(t, encodeRangeBranch(page, 7, 1, 6, IPv4(0), IPv4(0), false, wrongFirst), rangePageWriteErrFirstFence)
	if string(page) != string(before) {
		t.Fatal("first-fence rejection changed destination page")
	}
	invalidBounds := []rangeBranchEntry[IPv4]{
		{lowerFence: 100, childPage: 2, subtreeRecordCount: 1, firstFrom: 110, lastFrom: 110, lastTo: 120},
	}
	requireRangePageWriteCode(t, encodeRangeBranch(page, 7, 1, 4, IPv4(100), IPv4(100), true, invalidBounds), rangePageWriteErrFenceBounds)
	if string(page) != string(before) {
		t.Fatal("fence-bounds rejection changed destination page")
	}
	overlapping := append([]rangeBranchEntry[IPv4](nil), entries...)
	overlapping[0].lastTo = 220
	requireRangePageWriteCode(t, encodeRangeBranch(page, 7, 1, 6, IPv4(0), IPv4(0), false, overlapping), rangePageWriteErrSummaryOverlap)
	if string(page) != string(before) {
		t.Fatal("summary-overlap rejection changed destination page")
	}
	outsideFence := []rangeBranchEntry[IPv4]{
		{lowerFence: 100, childPage: 2, subtreeRecordCount: 1, firstFrom: 110, lastFrom: 200, lastTo: 220},
	}
	requireRangePageWriteCode(t, encodeRangeBranch(page, 7, 1, 4, IPv4(100), IPv4(200), true, outsideFence), rangePageWriteErrSummaryOutsideFence)
	if string(page) != string(before) {
		t.Fatal("summary-outside-fence rejection changed destination page")
	}

	v6 := []rangeBranchEntry[IPv6]{
		{
			childPage:          3,
			subtreeRecordCount: 1,
			firstFrom:          IPv6{Hi: 1, Lo: 2},
			lastFrom:           IPv6{Hi: 1, Lo: 2},
			lastTo:             IPv6{Hi: 1, Lo: 3},
		},
	}
	if err := encodeRangeBranch(page, 7, 1, 4, IPv6{}, IPv6{}, false, v6); err != nil {
		t.Fatal(err)
	}
	for _, value := range page[52:56] {
		if value != 0 {
			t.Fatal("IPv6 branch reserved bytes are nonzero")
		}
	}
	branchV6, err := openRangeBranch[IPv6](page, 7, AddressFamilyIPv6, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := branchV6.entry(0); err != nil || got != v6[0] {
		t.Fatalf("IPv6 entry = %#v/%v, want %#v", got, err, v6[0])
	}
}
