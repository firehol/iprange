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
