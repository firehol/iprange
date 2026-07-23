package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

type tornRangePageRead struct {
	data []byte
	torn bool
}

func (source *tornRangePageRead) readPageAt(offset uint64, page *[PageSize]byte) *pageSourceError {
	if offset != 2*PageSize || source.torn {
		reader := newSlicePageRead(source.data)
		return reader.readPageAt(offset, page)
	}
	start := int(offset)
	split := int(PageHeaderSize) + 4
	copy(page[:split], source.data[start:start+split])
	binary.LittleEndian.PutUint32(source.data[start+split:start+split+4], ^uint32(0))
	copy(page[split:], source.data[start+split:start+PageSize])
	source.torn = true
	return nil
}

type countingPageAccess struct {
	delegate         pageAccessCheck
	publicChecks     int
	readAccessChecks int
}

func (access *countingPageAccess) checkPageAccessStatus(kind pageAccessKind) pageSourceStatus {
	if kind == pageAccessPublicEntry {
		access.publicChecks++
	} else {
		access.readAccessChecks++
	}
	return access.delegate.checkPageAccessStatus(kind)
}

func countingSlicePageRead(data []byte) (positionalPageRead, *countingPageAccess) {
	source := newSlicePageRead(data)
	access := &countingPageAccess{delegate: source.access}
	source.access = access
	return source, access
}

func rangeImage[K rangeKey[K]](
	t testing.TB,
	root uint32,
	count uint64,
	pages int,
	fill func([]byte),
) []byte {
	t.Helper()
	meta := emptyDirectMeta(1)
	var key K
	meta.AddressFamily = key.family()
	meta.ValueKind = ValueKindDirect
	meta.PageCount = uint64(pages)
	meta.RangeRoot = root
	meta.RangeRecordCount = count

	data := make([]byte, pages*PageSize)
	page0 := meta.EncodePage()
	page1 := meta.EncodePage()
	copy(data[:PageSize], page0[:])
	copy(data[PageSize:2*PageSize], page1[:])
	fill(data)
	return data
}

func rangeImagePage(data []byte, page int) []byte {
	return data[page*PageSize : (page+1)*PageSize]
}

func requireRangeReadCode(t *testing.T, err error, want rangeReadErrorCode) *rangeReadError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected range-read error %d", want)
	}
	var got *rangeReadError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *rangeReadError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-read code = %d, want %d", got.code, want)
	}
	return got
}

func requireCurrentIPv4(t *testing.T, cursor *rangeCursor[IPv4], want IPv4) {
	t.Helper()
	record, ok, err := cursor.current()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || record.from != want {
		t.Fatalf("current = %+v/%t, want from %d", record, ok, want)
	}
}

func TestRangeTreeSingleLeafLookupSeekAndCount(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 2, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{
			{from: 10, to: 20, value: 1},
			{from: 30, to: 39, value: 2},
		})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("lookup 15 = %+v/%t/%v", record, found, err)
	}
	if _, found, err := tree.lookup(25); err != nil || found {
		t.Fatalf("lookup 25 = %t/%v, want false/nil", found, err)
	}
	if got, err := tree.countAddresses(); err != nil || got != CardinalityFromUint64(21) {
		t.Fatalf("address count = %v/%v, want 21/nil", got, err)
	}
}

func TestRangeCursorSkipsLegalEmptyLeaves(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 2, 6, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			{lowerFence: 100, childPage: 4},
			{lowerFence: 200, childPage: 5, subtreeRecordCount: 1, firstFrom: 210, lastFrom: 210, lastTo: 220},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{})
		putRangeLeaf(t, rangeImagePage(data, 5), []rangeRecord[IPv4]{{from: 210, to: 220, value: 2}})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor()
	if positioned, err := cursor.first(); err != nil || !positioned {
		t.Fatalf("first = %t/%v", positioned, err)
	}
	requireCurrentIPv4(t, &cursor, 10)
	if positioned, err := cursor.next(); err != nil || !positioned {
		t.Fatalf("next = %t/%v", positioned, err)
	}
	requireCurrentIPv4(t, &cursor, 210)
	if positioned, err := cursor.next(); err != nil || positioned {
		t.Fatalf("next past end = %t/%v", positioned, err)
	}
	if positioned, err := cursor.previous(); err != nil || !positioned {
		t.Fatalf("previous from end = %t/%v", positioned, err)
	}
	requireCurrentIPv4(t, &cursor, 210)
	if positioned, err := cursor.previous(); err != nil || !positioned {
		t.Fatalf("previous = %t/%v", positioned, err)
	}
	requireCurrentIPv4(t, &cursor, 10)
	if positioned, err := cursor.seek(150); err != nil || !positioned {
		t.Fatalf("seek 150 = %t/%v", positioned, err)
	}
	requireCurrentIPv4(t, &cursor, 210)
}

func TestRangeTreeAllEmptyBranchRepresentsEmptyMap(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 0, 5, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3},
			{lowerFence: 100, childPage: 4},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor()
	if positioned, err := cursor.first(); err != nil || positioned {
		t.Fatalf("first = %t/%v, want false/nil", positioned, err)
	}
	if got, err := tree.countAddresses(); err != nil || got != CardinalityZero() {
		t.Fatalf("address count = %v/%v, want zero/nil", got, err)
	}
}

func TestRangeTreeIPv6CardinalityIsExactAtFullSpace(t *testing.T) {
	maximum := IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}
	t.Run("single full-space record", func(t *testing.T) {
		data := rangeImage[IPv6](t, 2, 1, 3, func(data []byte) {
			putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv6]{
				{from: IPv6{}, to: maximum, value: 42},
			})
		})
		tree, err := openImmutableRangeTree[IPv6](data)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := tree.countAddresses(); err != nil || got != FullIPv6Space() {
			t.Fatalf("address count = %v/%v, want 2^128/nil", got, err)
		}
		record, found, err := tree.lookup(maximum)
		if err != nil || !found || record.value != 42 {
			t.Fatalf("maximum lookup = %+v/%t/%v", record, found, err)
		}
	})

	t.Run("split records carry into bit 128", func(t *testing.T) {
		data := rangeImage[IPv6](t, 2, 2, 3, func(data []byte) {
			putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv6]{
				{from: IPv6{}, to: IPv6{Hi: ^uint64(0), Lo: ^uint64(0) - 1}, value: 1},
				{from: maximum, to: maximum, value: 2},
			})
		})
		tree, err := openImmutableRangeTree[IPv6](data)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := tree.countAddresses(); err != nil || got != FullIPv6Space() {
			t.Fatalf("address count = %v/%v, want 2^128/nil", got, err)
		}
	})
}

func TestRangeTreeOrdinaryReadsDoNotValidatePageCRC(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{
			{from: 10, to: 20, value: 1},
		})
	})
	data[2*PageSize+PageCRCOffset] ^= 1
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("lookup with stale page CRC = %+v/%t/%v", record, found, err)
	}
}

func TestRangeTreeTornSourceCannotCauseOutOfBoundsRead(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 4, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := tornRangePageRead{data: data}
	var tornRoot [PageSize]byte
	if sourceErr := fixture.readPageAt(2*PageSize, &tornRoot); sourceErr != nil {
		t.Fatal(sourceErr)
	}
	copy(rangeImagePage(data, 2), tornRoot[:])
	tree, err := newRangeTree[IPv4](newSlicePageRead(data), bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tree.lookup(15)
	requireRangeReadCode(t, err, rangeReadErrPage)
	var pageError *rangePageError
	if !errors.As(err, &pageError) || pageError.code != rangePageErrChildOutOfBounds {
		t.Fatalf("torn lookup cause = %T/%v, want bounded child error", err, err)
	}
}

func TestRangeTreeLookupReadsEachVisitedPageOnce(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 4, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	source, access := countingSlicePageRead(data)
	tree, err := newRangeTree[IPv4](source, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("lookup = %+v/%t/%v", record, found, err)
	}
	if access.readAccessChecks != 2 {
		t.Fatalf("page reads = %d, want root + leaf", access.readAccessChecks)
	}
	if access.publicChecks != 1 {
		t.Fatalf(
			"public access checks = %d, want one lookup entry",
			access.publicChecks,
		)
	}
}

func TestRangeTreePublicEntriesCheckAccessExactlyOnce(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	source, access := countingSlicePageRead(data)
	tree, err := newRangeTree[IPv4](source, bootstrap)
	if err != nil {
		t.Fatal(err)
	}

	record, found, err := tree.lookup(15)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("lookup = %+v/%t/%v", record, found, err)
	}
	if access.publicChecks != 1 || access.readAccessChecks != 1 {
		t.Fatalf("lookup checks/reads = %d/%d, want 1/1", access.publicChecks, access.readAccessChecks)
	}

	access.publicChecks, access.readAccessChecks = 0, 0
	cursor := tree.cursor()
	if positioned, err := cursor.seek(15); err != nil || !positioned {
		t.Fatalf("seek = %t/%v", positioned, err)
	}
	if _, ok, err := cursor.current(); err != nil || !ok {
		t.Fatalf("current = %t/%v", ok, err)
	}
	if positioned, err := cursor.next(); err != nil || positioned {
		t.Fatalf("next = %t/%v", positioned, err)
	}
	if positioned, err := cursor.previous(); err != nil || !positioned {
		t.Fatalf("previous = %t/%v", positioned, err)
	}
	if positioned, err := cursor.first(); err != nil || !positioned {
		t.Fatalf("first = %t/%v", positioned, err)
	}
	if positioned, err := cursor.last(); err != nil || !positioned {
		t.Fatalf("last = %t/%v", positioned, err)
	}
	if access.publicChecks != 6 || access.readAccessChecks != 1 {
		t.Fatalf("cursor checks/reads = %d/%d, want 6/1", access.publicChecks, access.readAccessChecks)
	}

	access.publicChecks, access.readAccessChecks = 0, 0
	if count, err := tree.countAddresses(); err != nil || count != CardinalityFromUint64(11) {
		t.Fatalf("count = %v/%v", count, err)
	}
	if access.publicChecks != 1 || access.readAccessChecks != 1 {
		t.Fatalf("count checks/reads = %d/%d, want 1/1", access.publicChecks, access.readAccessChecks)
	}
}

func TestRangeTreeGapLookupMatchesCrossLeafReadCount(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 2, 5, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			{lowerFence: 30, childPage: 4, subtreeRecordCount: 1, firstFrom: 30, lastFrom: 30, lastTo: 40},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 30, to: 40, value: 2}})
	})
	bootstrap, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	lookupSource, lookupAccess := countingSlicePageRead(data)
	lookupTree, err := newRangeTree[IPv4](lookupSource, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := lookupTree.lookup(25); err != nil || found {
		t.Fatalf("gap lookup = %t/%v, want false/nil", found, err)
	}
	if lookupAccess.publicChecks != 1 || lookupAccess.readAccessChecks != 4 {
		t.Fatalf("gap checks/reads = %d/%d, want 1/4", lookupAccess.publicChecks, lookupAccess.readAccessChecks)
	}

	cursorSource, cursorAccess := countingSlicePageRead(data)
	cursorTree, err := newRangeTree[IPv4](cursorSource, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	cursor := cursorTree.cursor()
	if positioned, err := cursor.first(); err != nil || !positioned {
		t.Fatalf("first = %t/%v", positioned, err)
	}
	if positioned, err := cursor.next(); err != nil || !positioned {
		t.Fatalf("cross-leaf next = %t/%v", positioned, err)
	}
	if cursorAccess.publicChecks != 2 || cursorAccess.readAccessChecks != lookupAccess.readAccessChecks {
		t.Fatalf(
			"cross-leaf checks/reads = %d/%d, want 2/%d",
			cursorAccess.publicChecks,
			cursorAccess.readAccessChecks,
			lookupAccess.readAccessChecks,
		)
	}
}

func TestRangeTreeWarmedLookupAndCursorAllocateNothing(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tree.lookup(15); err != nil {
		t.Fatal(err)
	}
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	if allocations := testing.AllocsPerRun(1000, func() {
		record, found, lookupErr := tree.lookup(15)
		if lookupErr != nil || !found || record.value != 1 {
			panic("warmed lookup failed")
		}
	}); allocations != 0 {
		t.Fatalf("warmed lookup allocations = %v, want zero", allocations)
	}

	cursor := tree.cursor()
	if positioned, err := cursor.seek(15); err != nil || !positioned {
		t.Fatalf("warm cursor = %t/%v", positioned, err)
	}
	if allocations := testing.AllocsPerRun(1000, func() {
		positioned, seekErr := cursor.seek(15)
		if seekErr != nil || !positioned {
			panic("warmed cursor failed")
		}
	}); allocations != 0 {
		t.Fatalf("warmed cursor allocations = %v, want zero", allocations)
	}
}

func TestRangeCursorStructuralFailureIsTerminalAndBounded(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 4, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		})
		putIPv4Branch(t, rangeImagePage(data, 3), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 2, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
		})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor()
	positioned, err := cursor.first()
	if positioned {
		t.Fatal("structurally invalid tree positioned cursor")
	}
	requireRangeReadCode(t, err, rangeReadErrChildType)
	if cursor.state != rangeCursorFailed {
		t.Fatalf("cursor state = %d, want failed", cursor.state)
	}

	_, _, err = cursor.current()
	requireRangeReadCode(t, err, rangeReadErrCursorFailed)
	_, err = cursor.next()
	requireRangeReadCode(t, err, rangeReadErrCursorFailed)
	_, err = cursor.previous()
	requireRangeReadCode(t, err, rangeReadErrCursorFailed)
}

func TestRangeCursorCurrentDecodeFailureIsTerminal(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{
			{from: 20, to: 10, value: 1},
		})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor()
	if positioned, err := cursor.first(); err != nil || !positioned {
		t.Fatalf("first = %t/%v", positioned, err)
	}
	_, _, err = cursor.current()
	requireRangeReadCode(t, err, rangeReadErrPage)
	if cursor.state != rangeCursorFailed {
		t.Fatalf("cursor state = %d, want failed", cursor.state)
	}
	_, _, err = cursor.current()
	requireRangeReadCode(t, err, rangeReadErrCursorFailed)
}

func TestRangeCursorPredecessorFallsBackToNearestEarlierAncestor(t *testing.T) {
	data := rangeImage[IPv4](t, 2, 2, 7, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 2, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 4, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 150},
			{lowerFence: 50, childPage: 3, subtreeRecordCount: 1, firstFrom: 50, lastFrom: 50, lastTo: 250},
		})
		putIPv4Branch(t, rangeImagePage(data, 4), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 5, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 150},
		})
		putIPv4Branch(t, rangeImagePage(data, 3), 1, []ipv4BranchTestEntry{
			{lowerFence: 50, childPage: 6, subtreeRecordCount: 1, firstFrom: 200, lastFrom: 200, lastTo: 250},
		})
		putRangeLeaf(t, rangeImagePage(data, 5), []rangeRecord[IPv4]{{from: 10, to: 150, value: 1}})
		putRangeLeaf(t, rangeImagePage(data, 6), []rangeRecord[IPv4]{{from: 200, to: 250, value: 2}})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(100)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("lookup 100 = %+v/%t/%v", record, found, err)
	}
}

func TestRangeCursorMaximumLegalLevelUsesFixedStackExactly(t *testing.T) {
	const pages = int(MaxTreeLevel) + 3
	data := rangeImage[IPv4](t, 2, 1, pages, func(data []byte) {
		for level := MaxTreeLevel; level >= 1; level-- {
			page := int(MaxTreeLevel-level) + 2
			putIPv4Branch(t, rangeImagePage(data, page), level, []ipv4BranchTestEntry{
				{lowerFence: 0, childPage: uint32(page + 1), subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			})
		}
		putRangeLeaf(t, rangeImagePage(data, int(MaxTreeLevel)+2), []rangeRecord[IPv4]{
			{from: 10, to: 20, value: 1},
		})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := tree.lookup(15)
	if err != nil || !found || record.value != 1 {
		t.Fatalf("maximum-depth lookup = %+v/%t/%v", record, found, err)
	}
}

func TestRangeTreeChecksRootChildAndLevelContracts(t *testing.T) {
	t.Run("wrong key family", func(t *testing.T) {
		data := rangeImage[IPv4](t, 0, 0, 2, func([]byte) {})
		_, err := openImmutableRangeTree[IPv6](data)
		requireRangeReadCode(t, err, rangeReadErrWrongKeyFamily)
	})

	t.Run("wrong root type", func(t *testing.T) {
		data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
			putRangeHeader(t, rangeImagePage(data, 2), PageTypeMetadataChunk, 0, 0, PageHeaderSize, AddressFamilyIPv4)
		})
		tree, err := openImmutableRangeTree[IPv4](data)
		if err != nil {
			t.Fatal(err)
		}
		cursor := tree.cursor()
		_, err = cursor.first()
		requireRangeReadCode(t, err, rangeReadErrRootType)
	})

	t.Run("child out of bounds", func(t *testing.T) {
		data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
			putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
				{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			})
		})
		tree, err := openImmutableRangeTree[IPv4](data)
		if err != nil {
			t.Fatal(err)
		}
		cursor := tree.cursor()
		_, err = cursor.first()
		requireRangeReadCode(t, err, rangeReadErrPage)
		var pageError *rangePageError
		if !errors.As(err, &pageError) || pageError.code != rangePageErrChildOutOfBounds {
			t.Fatalf("cause = %T/%v, want child-out-of-bounds range-page error", err, err)
		}
	})

	t.Run("child level mismatch", func(t *testing.T) {
		data := rangeImage[IPv4](t, 2, 1, 5, func(data []byte) {
			putIPv4Branch(t, rangeImagePage(data, 2), 2, []ipv4BranchTestEntry{
				{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			})
			putIPv4Branch(t, rangeImagePage(data, 3), 2, []ipv4BranchTestEntry{
				{lowerFence: 0, childPage: 4, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			})
			putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		})
		tree, err := openImmutableRangeTree[IPv4](data)
		if err != nil {
			t.Fatal(err)
		}
		cursor := tree.cursor()
		_, err = cursor.first()
		got := requireRangeReadCode(t, err, rangeReadErrChildLevel)
		if got.expectedLevel != 1 || got.actualLevel != 2 {
			t.Fatalf("levels = %d/%d, want 1/2", got.expectedLevel, got.actualLevel)
		}
	})
}

func BenchmarkRangeTreePositionalLookup(b *testing.B) {
	data := rangeImage[IPv4](b, 2, 1, 3, func(data []byte) {
		putRangeLeaf(b, rangeImagePage(data, 2), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	tree, err := openImmutableRangeTree[IPv4](data)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		record, found, err := tree.lookup(15)
		if err != nil || !found || record.value != 1 {
			b.Fatalf("lookup = %+v/%t/%v", record, found, err)
		}
	}
}
