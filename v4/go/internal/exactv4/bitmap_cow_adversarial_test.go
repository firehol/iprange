package exactv4

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"testing"
)

func requireNoAllocCOWFailure(
	t *testing.T,
	cow *freeBitmapCOW,
	want freeBitmapCOWErrorCode,
) freeBitmapCOWError {
	t.Helper()
	var got freeBitmapCOWError
	allocations := testing.AllocsPerRun(100, func() {
		_, _, got = cow.removeLowest()
	})
	if allocations != 0 {
		t.Fatalf("free-bitmap COW error %d allocations = %v, want 0", want, allocations)
	}
	return requireFreeBitmapCOWCode(t, got, want)
}

func mustNewFreeBitmapCOW(
	t *testing.T,
	committed committedPageSource,
	pageCount uint64,
	root uint32,
	ledger freeBitmapCOWLedger,
) *freeBitmapCOW {
	t.Helper()
	cow, problem := newFreeBitmapCOW(committed, 1, pageCount, root, ledger)
	if problem.failed() {
		t.Fatal(problem)
	}
	return cow
}

func TestFreeBitmapCOWWordBoundary63And64(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 63, 64, 65)}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10)}
	replacements := make([]uint32, 1)
	candidates := make([]uint32, 3)
	cow := mustNewFreeBitmapCOW(
		t,
		source,
		100,
		2,
		emptyFreeBitmapCOWLedger(arena, replacements, candidates),
	)
	initialIndex := append([]bitmapCOWIndexNode(nil), cow.indexNodes...)
	initialAvailable := append([]int(nil), cow.availableSlots...)
	initialArena := arena[0]
	initialRoot, initialIndexLen, initialAvailableLen := cow.indexRoot, cow.indexLen, cow.availableLen
	allocations := testing.AllocsPerRun(100, func() {
		arena[0] = initialArena
		clear(replacements)
		clear(candidates)
		copy(cow.indexNodes, initialIndex)
		copy(cow.availableSlots, initialAvailable)
		cow.root = 2
		cow.replacementLen = 0
		cow.candidateLen = 0
		cow.indexRoot = initialRoot
		cow.indexLen = initialIndexLen
		cow.availableLen = initialAvailableLen
		for _, want := range []uint32{63, 64, 65} {
			reserved, ok, problem := cow.removeLowest()
			if problem.failed() || !ok || reserved.pageNumber != want {
				panic("unexpected word-boundary removal")
			}
		}
	})
	if allocations != 0 {
		t.Fatalf("word-boundary allocations = %v, want 0", allocations)
	}
	if cow.root != 0 || !equalU32(cow.candidatePages(), []uint32{63, 64, 65}) {
		t.Fatalf("final root/candidates = %d/%v", cow.root, cow.candidatePages())
	}
}

func TestFreeBitmapCOWRejectsBitsZeroAndOneWithoutAllocation(t *testing.T) {
	for _, bit := range []uint32{0, 1} {
		t.Run(string(rune('0'+bit)), func(t *testing.T) {
			source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, bit, 5)}}
			cow := mustNewFreeBitmapCOW(
				t,
				source,
				20,
				2,
				emptyFreeBitmapCOWLedger(
					[]reservedBitmapPage{newReservedBitmapPage(10)},
					make([]uint32, 1),
					make([]uint32, 1),
				),
			)
			problem := requireNoAllocCOWFailure(t, cow, freeBitmapCOWErrPage)
			if problem.pageProblem.code != bitmapPageErrBitOutsideLimit {
				t.Fatalf("page problem = %d, want bit-outside-limit", problem.pageProblem.code)
			}
			if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 {
				t.Fatal("illegal low bit mutated draft")
			}
		})
	}
}

func TestFreeBitmapCOWCorruptionMatrixAllocatesNothing(t *testing.T) {
	assertPageProblem := func(
		name string,
		page cowSparsePage,
		pageCount uint64,
		want bitmapPageErrorCode,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			source := &cowSparsePages{pages: []cowSparsePage{page}}
			arena := []reservedBitmapPage{newReservedBitmapPage(10)}
			cow := mustNewFreeBitmapCOW(
				t,
				source,
				pageCount,
				2,
				emptyFreeBitmapCOWLedger(arena, make([]uint32, 1), make([]uint32, 1)),
			)
			pristine := cow.pool.slots[0]
			problem := requireNoAllocCOWFailure(t, cow, freeBitmapCOWErrPage)
			if problem.pageProblem.code != want {
				t.Fatalf("page problem = %d, want %d", problem.pageProblem.code, want)
			}
			if cow.root != 2 || len(cow.replacementPages()) != 0 ||
				len(cow.candidatePages()) != 0 || cow.pool.slots[0] != pristine {
				t.Fatal("corruption failure mutated draft")
			}
		})
	}

	checksum := cowLeaf(t, 2, 1, 5)
	checksum.bytes[100] ^= 1
	assertPageProblem("checksum", checksum, 20, bitmapPageErrChecksum)

	reserved := cowLeaf(t, 2, 1, 5)
	reserved.bytes[int(bitmapLeafLower)] = 1
	if _, err := WritePageCRC32C(reserved.bytes[:]); err != nil {
		t.Fatal(err)
	}
	assertPageProblem("reserved", reserved, 20, bitmapPageErrReservedNonzero)

	wrongCount := cowLeaf(t, 2, 1, 5)
	binary.LittleEndian.PutUint16(wrongCount.bytes[16:18], 2)
	if _, err := WritePageCRC32C(wrongCount.bytes[:]); err != nil {
		t.Fatal(err)
	}
	assertPageProblem("count", wrongCount, 20, bitmapPageErrItemCountMismatch)

	wrongKind := cowLeaf(t, 2, 1, 5)
	binary.LittleEndian.PutUint32(wrongKind.bytes[24:28], uint32(bitmapKindFeedUsed))
	if _, err := WritePageCRC32C(wrongKind.bytes[:]); err != nil {
		t.Fatal(err)
	}
	assertPageProblem("kind", wrongKind, 20, bitmapPageErrWrongKind)

	badHeader := cowLeaf(t, 2, 1, 5)
	badHeader.bytes[0] ^= 1
	assertPageProblem("header", badHeader, 20, bitmapPageErrHeader)

	badChild := cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 1})
	assertPageProblem("child", badChild, 32_001, bitmapPageErrChildPageOutOfBounds)
}

func TestDecodePageHeaderNoAllocEveryStatusAndEvidence(t *testing.T) {
	leafPage := func() []byte {
		page := cowLeaf(t, 2, 1, 5)
		result := make([]byte, PageSize)
		copy(result, page.bytes[:])
		return result
	}
	branchPage := func() []byte {
		page := cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3})
		result := make([]byte, PageSize)
		copy(result, page.bytes[:])
		return result
	}

	invalidMagic := leafPage()
	invalidMagic[0] ^= 1
	invalidType := leafPage()
	invalidType[4] = 0xff
	invalidFlags := leafPage()
	invalidFlags[5] = 7
	invalidHeaderSize := leafPage()
	binary.LittleEndian.PutUint16(invalidHeaderSize[6:8], PageHeaderSize-1)
	zeroBornTxn := leafPage()
	binary.LittleEndian.PutUint64(zeroBornTxn[8:16], 0)
	futureBornTxn := leafPage()
	binary.LittleEndian.PutUint64(futureBornTxn[8:16], 2)
	levelTooHigh := leafPage()
	binary.LittleEndian.PutUint16(levelTooHigh[18:20], MaxTreeLevel+1)
	branchLevelZero := branchPage()
	binary.LittleEndian.PutUint16(branchLevelZero[18:20], 0)
	nonBranchLevel := leafPage()
	binary.LittleEndian.PutUint16(nonBranchLevel[18:20], 1)
	invalidBounds := leafPage()
	binary.LittleEndian.PutUint16(invalidBounds[20:22], PageHeaderSize-1)

	valid := leafPage()
	wantValid := PageHeader{
		PageType:   PageTypeBitmapLeaf,
		BornTxn:    1,
		ItemCount:  1,
		Level:      0,
		Lower:      bitmapLeafLower,
		Upper:      PageSize,
		Aux:        uint32(bitmapKindFreePages),
		PageCRC32C: binary.LittleEndian.Uint32(valid[PageCRCOffset : PageCRCOffset+4]),
	}

	for _, test := range []struct {
		name        string
		page        []byte
		selectedTxn uint64
		wantHeader  PageHeader
		wantProblem bitmapCOWHeaderProblem
	}{
		{
			name: "page-size", page: make([]byte, PageSize-1), selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrPageSize, length: PageSize - 1},
		},
		{
			name: "magic", page: invalidMagic, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrMagic},
		},
		{
			name: "page-type", page: invalidType, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrPageType, wireType: 0xff},
		},
		{
			name: "flags", page: invalidFlags, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrFlags, flags: 7},
		},
		{
			name: "header-size", page: invalidHeaderSize, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrHeaderSize, headerSize: PageHeaderSize - 1},
		},
		{
			name: "born-zero", page: zeroBornTxn, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrBornTransactionZero},
		},
		{
			name: "born-future", page: futureBornTxn, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{
				code: PageHeaderErrBornTransactionFuture, bornTxn: 2, selectedTxn: 1,
			},
		},
		{
			name: "level-high", page: levelTooHigh, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrLevelTooHigh, level: MaxTreeLevel + 1},
		},
		{
			name: "branch-level-zero", page: branchLevelZero, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{code: PageHeaderErrBranchLevelZero, pageType: PageTypeBitmapBranch},
		},
		{
			name: "nonbranch-level", page: nonBranchLevel, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{
				code: PageHeaderErrNonBranchLevelNonzero, pageType: PageTypeBitmapLeaf, level: 1,
			},
		},
		{
			name: "bounds", page: invalidBounds, selectedTxn: 1,
			wantProblem: bitmapCOWHeaderProblem{
				code: PageHeaderErrBounds, lower: PageHeaderSize - 1, upper: PageSize,
			},
		},
		{name: "success", page: valid, selectedTxn: 1, wantHeader: wantValid},
	} {
		t.Run(test.name, func(t *testing.T) {
			var header PageHeader
			var problem bitmapCOWHeaderProblem
			allocations := testing.AllocsPerRun(100, func() {
				header, problem = decodePageHeaderNoAlloc(test.page, test.selectedTxn)
			})
			if allocations != 0 {
				t.Fatalf("decode allocations = %v, want 0", allocations)
			}
			if problem != test.wantProblem {
				t.Fatalf("problem = %+v, want %+v", problem, test.wantProblem)
			}
			if header != test.wantHeader {
				t.Fatalf("header = %+v, want %+v", header, test.wantHeader)
			}
		})
	}
}

func TestFreeBitmapCOWIndexedConflictFailuresAllocateNothing(t *testing.T) {
	assertFailure := func(
		name string,
		arenaPage *uint32,
		replacementPage *uint32,
		candidatePage *uint32,
		freeBit uint32,
		want freeBitmapCOWErrorCode,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var arena []reservedBitmapPage
			if arenaPage != nil {
				arena = []reservedBitmapPage{newReservedBitmapPage(*arenaPage)}
			}
			replacements := make([]uint32, 1)
			replacementLen := 0
			if replacementPage != nil {
				replacements[0] = *replacementPage
				replacementLen = 1
			}
			candidates := make([]uint32, 2)
			candidateLen := 0
			if candidatePage != nil {
				candidates[0] = *candidatePage
				candidateLen = 1
			}
			ledger := prefixedFreeBitmapCOWLedger(
				arena, replacements, replacementLen, candidates, candidateLen,
			)
			source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, freeBit)}}
			cow := mustNewFreeBitmapCOW(t, source, 20, 2, ledger)
			requireNoAllocCOWFailure(t, cow, want)
			if cow.root != 2 || cow.replacementLen != replacementLen || cow.candidateLen != candidateLen {
				t.Fatal("indexed conflict mutated draft")
			}
		})
	}
	value := func(value uint32) *uint32 { return &value }
	assertFailure("candidate-arena", value(5), nil, nil, 5, freeBitmapCOWErrCandidateIsArenaPage)
	assertFailure("candidate-replacement", nil, value(5), nil, 5, freeBitmapCOWErrCandidateIsDraftReplacement)
	assertFailure("candidate-duplicate", nil, nil, value(5), 5, freeBitmapCOWErrCandidateAlreadyReserved)
	assertFailure("candidate-regression", nil, nil, value(6), 5, freeBitmapCOWErrCandidateOrderRegression)
	assertFailure("path-arena", value(2), nil, nil, 5, freeBitmapCOWErrArenaPageConflict)
	assertFailure("path-replacement", nil, value(2), nil, 5, freeBitmapCOWErrRepeatedCommittedPage)
	assertFailure("candidate-is-path", nil, nil, nil, 2, freeBitmapCOWErrCandidateIsPathPage)
}

func TestFreeBitmapCOWCapacityFailuresAllocateNothing(t *testing.T) {
	for _, test := range []struct {
		name                string
		arena, replacements int
		candidates          int
		want                freeBitmapCOWErrorCode
	}{
		{name: "candidate", arena: 1, replacements: 1, candidates: 0, want: freeBitmapCOWErrCandidateLedgerExhausted},
		{name: "replacement", arena: 1, replacements: 0, candidates: 1, want: freeBitmapCOWErrReplacementLedgerExhausted},
		{name: "arena", arena: 0, replacements: 1, candidates: 1, want: freeBitmapCOWErrPrivateArenaExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
			arenaStorage := []reservedBitmapPage{newReservedBitmapPage(10)}
			cow := mustNewFreeBitmapCOW(
				t,
				source,
				20,
				2,
				emptyFreeBitmapCOWLedger(
					arenaStorage[:test.arena],
					make([]uint32, test.replacements),
					make([]uint32, test.candidates),
				),
			)
			requireNoAllocCOWFailure(t, cow, test.want)
			if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 {
				t.Fatal("capacity failure mutated draft")
			}
		})
	}
}

func TestFreeBitmapCOWPathFailuresAllocateNothing(t *testing.T) {
	assertCommitted := func(
		name string,
		source *cowSparsePages,
		pageCount uint64,
		want freeBitmapCOWErrorCode,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			cow := mustNewFreeBitmapCOW(
				t, source, pageCount, 2,
				emptyFreeBitmapCOWLedger(
					[]reservedBitmapPage{newReservedBitmapPage(19)},
					make([]uint32, 1), make([]uint32, 1),
				),
			)
			requireNoAllocCOWFailure(t, cow, want)
			if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 {
				t.Fatal("path failure mutated draft")
			}
		})
	}
	assertCommitted("missing", &cowSparsePages{}, 20, freeBitmapCOWErrSource)

	wrongType := cowLeaf(t, 2, 1, 5)
	wrongType.bytes[4] = byte(PageTypeRangeLeaf)
	binary.LittleEndian.PutUint16(wrongType.bytes[20:22], PageHeaderSize)
	if _, err := WritePageCRC32C(wrongType.bytes[:]); err != nil {
		t.Fatal(err)
	}
	assertCommitted("type", &cowSparsePages{pages: []cowSparsePage{wrongType}}, 20, freeBitmapCOWErrUnexpectedPageType)
	assertCommitted("root-level", &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}),
	}}, 20, freeBitmapCOWErrRootLevel)
	assertCommitted("child-level", &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}),
		cowBranch(t, 3, 1, 1, cowChild{index: 0, page: 4}),
	}}, 32_001, freeBitmapCOWErrChildLevel)
	assertCommitted("cycle", &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 2}),
	}}, 32_001, freeBitmapCOWErrRepeatedPathPage)

	privateFailure := func(
		name string,
		arena []reservedBitmapPage,
		replacements []uint32,
		root uint32,
		want freeBitmapCOWErrorCode,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			ledger := prefixedFreeBitmapCOWLedger(
				arena, replacements, len(replacements), make([]uint32, 1), 0,
			)
			cow := mustNewFreeBitmapCOW(t, &cowSparsePages{}, 32_001, root, ledger)
			requireNoAllocCOWFailure(t, cow, want)
		})
	}

	outside := newReservedBitmapPage(10)
	outside.bytes = cowBranch(t, 10, 2, 1, cowChild{index: 2, page: 11}).bytes
	outside.state = privateBitmapPageInUse
	privateFailure("coverage", []reservedBitmapPage{outside}, []uint32{2}, 10, freeBitmapCOWErrSelectedCoverageOutsideLimit)

	missingChild := newReservedBitmapPage(10)
	missingChild.bytes = cowBranch(t, 10, 2, 1, cowChild{index: 0, page: 11}).bytes
	binary.LittleEndian.PutUint32(missingChild.bytes[bitmapChildrenOffset:bitmapChildrenOffset+4], 0)
	missingChild.state = privateBitmapPageInUse
	privateFailure("child-missing", []reservedBitmapPage{missingChild}, []uint32{2}, 10, freeBitmapCOWErrSelectedChildMissing)

	leaf := newReservedBitmapPage(10)
	leaf.bytes = cowLeaf(t, 10, 2, 1).bytes
	leaf.state = privateBitmapPageInUse
	branch := newReservedBitmapPage(11)
	branch.bytes = cowBranch(t, 11, 2, 1, cowChild{index: 0, page: 10}).bytes
	branch.state = privateBitmapPageInUse
	privateFailure("summary", []reservedBitmapPage{leaf, branch}, []uint32{2, 3}, 11, freeBitmapCOWErrSummaryMismatch)

	outOfBounds := newReservedBitmapPage(10)
	outOfBounds.bytes = cowBranch(t, 10, 2, 1, cowChild{index: 0, page: 32_001}).bytes
	outOfBounds.state = privateBitmapPageInUse
	privateFailure("page-out-of-bounds", []reservedBitmapPage{outOfBounds}, []uint32{2}, 10, freeBitmapCOWErrRootPageOutOfBounds)

	cow := mustNewFreeBitmapCOW(
		t, &cowSparsePages{}, 20, 0,
		emptyFreeBitmapCOWLedger(nil, nil, nil),
	)
	cow.root = 2
	cow.pageCount = ^uint64(0)
	requireNoAllocCOWFailure(t, cow, freeBitmapCOWErrCoverageOverflow)
}

func TestFreeBitmapCOWSuccessfulConstructorAllocationBoundary(t *testing.T) {
	arena := []reservedBitmapPage{newReservedBitmapPage(10)}
	ledger := newFreeBitmapCOWLedger(
		arena,
		make([]uint32, 1),
		make([]uint32, 1),
		make([]bitmapCOWIndexNode, 2),
		make([]int, 1),
	)
	var cow *freeBitmapCOW
	var problem freeBitmapCOWError
	pool := &privatePagePool{}
	if poolProblem := initPrivatePagePool(pool, arena, make([]uint32, len(arena)), 20, 20, 2, privatePageOwnerBitmap); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	allocations := testing.AllocsPerRun(100, func() {
		cow, problem = newFreeBitmapCOWWithPool(nil, 1, 20, 0, pool, ledger)
	})
	if allocations != 1 {
		t.Fatalf("successful constructor allocations = %v, want exactly 1 pre-lock COW object", allocations)
	}
	if cow == nil || problem.failed() {
		t.Fatalf("successful constructor = %p/%v", cow, problem)
	}

	hotAllocations := testing.AllocsPerRun(100, func() {
		_, found, hotProblem := cow.removeLowest()
		if found || hotProblem.failed() {
			panic("empty free bitmap removal did not return absence")
		}
	})
	if hotAllocations != 0 {
		t.Fatalf("hot empty removal allocations = %v, want 0", hotAllocations)
	}
}

func TestFreeBitmapCOWConstructorPreparationGuards(t *testing.T) {
	assertEarly := func(
		name string,
		selectedTxn, pageCount uint64,
		root uint32,
		ledger freeBitmapCOWLedger,
		want freeBitmapCOWErrorCode,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var problem freeBitmapCOWError
			allocations := testing.AllocsPerRun(100, func() {
				_, problem = newFreeBitmapCOWWithPool(nil, selectedTxn, pageCount, root, nil, ledger)
			})
			if allocations != 0 {
				t.Fatalf("early constructor error allocations = %v, want 0", allocations)
			}
			requireFreeBitmapCOWCode(t, problem, want)
		})
	}
	empty := emptyFreeBitmapCOWLedger(nil, nil, nil)
	assertEarly("transaction-zero", 0, 20, 0, empty, freeBitmapCOWErrSelectedTransactionZero)
	assertEarly("transaction-overflow", ^uint64(0), 20, 0, empty, freeBitmapCOWErrTransactionExhausted)
	assertEarly("page-count-low", 1, 1, 0, empty, freeBitmapCOWErrPageCountOutOfRange)
	assertEarly("page-count-high", 1, MaxPageCount+1, 0, empty, freeBitmapCOWErrPageCountOutOfRange)
	assertEarly("root-low", 1, 20, 1, empty, freeBitmapCOWErrRootPageOutOfBounds)
	assertEarly("root-high", 1, 20, 20, empty, freeBitmapCOWErrRootPageOutOfBounds)
	prefix := emptyFreeBitmapCOWLedger(nil, make([]uint32, 1), make([]uint32, 1))
	prefix.replacementLen = 2
	assertEarly("replacement-prefix", 1, 20, 0, prefix, freeBitmapCOWErrLedgerPrefixOutOfBounds)
	prefix.replacementLen = 0
	prefix.candidateLen = 2
	assertEarly("candidate-prefix", 1, 20, 0, prefix, freeBitmapCOWErrLedgerPrefixOutOfBounds)

	assertProblem := func(name string, ledger freeBitmapCOWLedger, want freeBitmapCOWErrorCode) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var problem freeBitmapCOWError
			pool := &privatePagePool{}
			validation := make([]uint32, len(ledger.arena))
			allocations := testing.AllocsPerRun(100, func() {
				poolCommitted := uint64(20)
				for index := range ledger.arena {
					if ledger.arena[index].authorization == privatePageAppended && uint64(ledger.arena[index].pageNumber) < poolCommitted {
						poolCommitted = uint64(ledger.arena[index].pageNumber)
					}
				}
				poolProblem := initPrivatePagePool(pool, ledger.arena, validation, poolCommitted, 20, 2, privatePageOwnerBitmap)
				if poolProblem.failed() {
					problem = bitmapPoolError(poolProblem)
					return
				}
				_, problem = newFreeBitmapCOWWithPool(nil, 1, 20, 0, pool, ledger)
			})
			if allocations != 0 {
				t.Fatalf("constructor error allocations = %v, want 0", allocations)
			}
			requireFreeBitmapCOWCode(t, problem, want)
		})
	}
	assertProblem(
		"index-capacity",
		newFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(10)},
			make([]uint32, 1), nil,
			make([]bitmapCOWIndexNode, 1), make([]int, 1),
		),
		freeBitmapCOWErrIndexCapacityTooSmall,
	)
	assertProblem(
		"available-capacity",
		newFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(10)},
			nil, nil,
			make([]bitmapCOWIndexNode, 1), nil,
		),
		freeBitmapCOWErrAvailableSlotCapacityTooSmall,
	)
	assertProblem(
		"duplicate-arena",
		newFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(10), newReservedBitmapPage(10)},
			nil, nil,
			make([]bitmapCOWIndexNode, 2), make([]int, 2),
		),
		freeBitmapCOWErrDuplicateArenaPage,
	)
	assertProblem(
		"arena-out-of-bounds",
		newFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(20)}, nil, nil,
			make([]bitmapCOWIndexNode, 1), make([]int, 1),
		),
		freeBitmapCOWErrLedgerPageOutOfBounds,
	)
	duplicateReplacements := prefixedFreeBitmapCOWLedger(nil, []uint32{5, 5}, 2, nil, 0)
	assertProblem("duplicate-replacement", duplicateReplacements, freeBitmapCOWErrDuplicateReplacement)

	duplicateCandidates := prefixedFreeBitmapCOWLedger(nil, nil, 0, []uint32{5, 5}, 2)
	assertProblem("duplicate-candidate", duplicateCandidates, freeBitmapCOWErrDuplicateCandidate)
	regressedCandidates := prefixedFreeBitmapCOWLedger(nil, nil, 0, []uint32{6, 5}, 2)
	assertProblem("candidate-order", regressedCandidates, freeBitmapCOWErrCandidateOrderRegression)
	conflict := prefixedFreeBitmapCOWLedger(
		[]reservedBitmapPage{newReservedBitmapPage(10)}, nil, 0, []uint32{10}, 1,
	)
	assertProblem("ledger-conflict", conflict, freeBitmapCOWErrLedgerPageConflict)
}

func TestFreeBitmapCOWConstructorFailureResetsScratchForRetry(t *testing.T) {
	arena := []reservedBitmapPage{newReservedBitmapPage(10), newReservedBitmapPage(11)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(
		pool, arena, make([]uint32, len(arena)), 20, 20, 2, privatePageOwnerBitmap,
	); problem.failed() {
		t.Fatal(problem)
	}
	replacements := []uint32{5, 6, 6}
	candidates := []uint32{8}
	indexNodes := make([]bitmapCOWIndexNode, len(arena)+len(replacements))
	availableSlots := make([]int, len(arena))
	for index := range indexNodes {
		indexNodes[index] = bitmapCOWIndexNode{pageNumber: uint32(100 + index), left: 17, right: 19, height: 7}
	}
	for index := range availableSlots {
		availableSlots[index] = 23 + index
	}
	ledger := newFreeBitmapCOWLedger(arena, replacements, candidates, indexNodes, availableSlots)
	ledger.replacementLen = len(replacements)
	ledger.candidateLen = len(candidates)

	poolBefore := append([]privatePagePoolSlot(nil), pool.slots...)
	statusBefore, problem := pool.status()
	if problem.failed() {
		t.Fatal(problem)
	}
	replacementsBefore := append([]uint32(nil), replacements...)
	candidatesBefore := append([]uint32(nil), candidates...)
	if _, cowProblem := newFreeBitmapCOWWithPool(nil, 1, 20, 0, pool, ledger); cowProblem.code != freeBitmapCOWErrDuplicateReplacement {
		t.Fatalf("constructor error = %v, want duplicate replacement", cowProblem)
	}
	for index, node := range indexNodes {
		if node != (bitmapCOWIndexNode{}) {
			t.Fatalf("index scratch %d not reset: %+v", index, node)
		}
	}
	for index, slot := range availableSlots {
		if slot != 0 {
			t.Fatalf("available scratch %d = %d, want 0", index, slot)
		}
	}
	for index := range pool.slots {
		if pool.slots[index] != poolBefore[index] {
			t.Fatalf("pool slot %d changed on constructor failure", index)
		}
	}
	statusAfter, problem := pool.status()
	if problem.failed() {
		t.Fatal(problem)
	}
	if statusAfter != statusBefore {
		t.Fatalf("pool status changed: %+v -> %+v", statusBefore, statusAfter)
	}
	for index := range replacements {
		if replacements[index] != replacementsBefore[index] {
			t.Fatalf("replacement %d changed", index)
		}
	}
	for index := range candidates {
		if candidates[index] != candidatesBefore[index] {
			t.Fatalf("candidate %d changed", index)
		}
	}

	replacements[2] = 7
	cow, cowProblem := newFreeBitmapCOWWithPool(nil, 1, 20, 0, pool, ledger)
	if cowProblem.failed() || cow == nil {
		t.Fatalf("retry constructor = %p/%v", cow, cowProblem)
	}
}

func verifyBitmapCOWAVL(
	t *testing.T,
	nodes []bitmapCOWIndexNode,
	root int,
) (minimum, maximum uint32, height uint8, visited int, present bool) {
	t.Helper()
	if root == bitmapCOWNoIndex {
		return 0, 0, 0, 0, false
	}
	leftMin, leftMax, leftHeight, leftVisited, hasLeft := verifyBitmapCOWAVL(t, nodes, nodes[root].left)
	rightMin, rightMax, rightHeight, rightVisited, hasRight := verifyBitmapCOWAVL(t, nodes, nodes[root].right)
	if hasLeft && leftMax >= nodes[root].pageNumber {
		t.Fatalf("left maximum %d >= root %d", leftMax, nodes[root].pageNumber)
	}
	if hasRight && rightMin <= nodes[root].pageNumber {
		t.Fatalf("right minimum %d <= root %d", rightMin, nodes[root].pageNumber)
	}
	if leftHeight > rightHeight+1 || rightHeight > leftHeight+1 {
		t.Fatalf("AVL imbalance at %d: %d/%d", nodes[root].pageNumber, leftHeight, rightHeight)
	}
	height = leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	if nodes[root].height != height {
		t.Fatalf("stored height at %d = %d, want %d", nodes[root].pageNumber, nodes[root].height, height)
	}
	minimum, maximum = nodes[root].pageNumber, nodes[root].pageNumber
	if hasLeft {
		minimum = leftMin
	}
	if hasRight {
		maximum = rightMax
	}
	return minimum, maximum, height, leftVisited + rightVisited + 1, true
}

// pageIndexFindCounted is test-only evidence for lookup depth. Production uses
// pageIndexFind and does not update probe accounting on the hot path.
func pageIndexFindCounted(
	nodes []bitmapCOWIndexNode,
	root int,
	pageNumber uint32,
) (indexedBitmapPage, bool, int) {
	probes := 0
	for root != bitmapCOWNoIndex {
		probes++
		node := nodes[root]
		if pageNumber < node.pageNumber {
			root = node.left
		} else if pageNumber > node.pageNumber {
			root = node.right
		} else {
			return node.page, true, probes
		}
	}
	return indexedBitmapPage{}, false, probes
}

func TestFreeBitmapCOWAVLAdversarialOrders(t *testing.T) {
	const count = 4096
	ascending := make([]uint32, count)
	descending := make([]uint32, count)
	alternating := make([]uint32, count)
	permuted := make([]uint32, count)
	for index := 0; index < count; index++ {
		ascending[index] = uint32(index + 2)
		descending[index] = uint32(count + 1 - index)
		offset := index / 2
		if index%2 != 0 {
			offset = count - 1 - index/2
		}
		alternating[index] = uint32(offset + 2)
		permuted[index] = uint32(((index * 4051) & (count - 1)) + 2)
	}
	for name, order := range map[string][]uint32{
		"ascending":   ascending,
		"descending":  descending,
		"alternating": alternating,
		"permuted":    permuted,
	} {
		t.Run(name, func(t *testing.T) {
			nodes := make([]bitmapCOWIndexNode, count)
			root := bitmapCOWNoIndex
			length := 0
			allocations := testing.AllocsPerRun(100, func() {
				root = bitmapCOWNoIndex
				length = 0
				for _, pageNumber := range order {
					if !pageIndexInsert(
						nodes, &root, &length, pageNumber,
						indexedBitmapPage{kind: indexedBitmapPageReplacement},
					) {
						panic("unexpected duplicate AVL page")
					}
				}
			})
			if allocations != 0 {
				t.Fatalf("AVL insertion allocations = %v, want 0", allocations)
			}
			_, _, height, visited, present := verifyBitmapCOWAVL(t, nodes, root)
			if !present || visited != count {
				t.Fatalf("visited = %d/%t, want %d/true", visited, present, count)
			}
			bound := uint8(2 * bits.Len(uint(count)))
			if height > bound {
				t.Fatalf("AVL height = %d, bound %d", height, bound)
			}
		})
	}
}

func TestFreeBitmapCOWPreparationScalingIsStructurallyBalanced(t *testing.T) {
	for _, count := range []int{512, 1024, 2048, 4096, 8192} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			arena := make([]reservedBitmapPage, count-1)
			for index := range arena {
				arena[index] = newReservedBitmapPage(uint32(10_000 + index))
			}
			replacements := []uint32{2}
			ledger := newFreeBitmapCOWLedger(
				arena,
				replacements,
				nil,
				make([]bitmapCOWIndexNode, count),
				make([]int, len(arena)),
			)
			ledger.replacementLen = 1
			cow := mustNewFreeBitmapCOW(t, nil, MaxPageCount, 0, ledger)
			if cow.indexLen != count {
				t.Fatalf("prepared index length = %d, want %d", cow.indexLen, count)
			}
			_, _, height, visited, present := verifyBitmapCOWAVL(t, cow.indexNodes, cow.indexRoot)
			if !present || visited != count {
				t.Fatalf("prepared AVL visited = %d/%t, want %d/true", visited, present, count)
			}
			bound := uint8(2 * bits.Len(uint(count)))
			if height > bound {
				t.Fatalf("prepared AVL height = %d, bound %d", height, bound)
			}

			for _, pageNumber := range []uint32{2, 10_000, uint32(10_000 + count/2), uint32(10_000 + count - 2), 3} {
				indexed, found, probes := pageIndexFindCounted(cow.indexNodes, cow.indexRoot, pageNumber)
				productionIndexed, productionFound := pageIndexFind(cow.indexNodes, cow.indexRoot, pageNumber)
				if found != productionFound || indexed != productionIndexed {
					t.Fatalf("test/production lookup disagree for page %d", pageNumber)
				}
				if probes > int(height) {
					t.Fatalf("page %d lookup probes = %d, tree height %d", pageNumber, probes, height)
				}
				if pageNumber == 3 && found {
					t.Fatal("missing page 3 was found")
				}
				if pageNumber != 3 && !found {
					t.Fatalf("page %d was not found", pageNumber)
				}
			}
		})
	}
}

func BenchmarkFreeBitmapCOWPreparationScaling(b *testing.B) {
	for _, count := range []int{512, 1024, 2048, 4096, 8192} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			arena := make([]reservedBitmapPage, count)
			for index := range arena {
				arena[index] = newReservedBitmapPage(uint32(10_000 + index))
			}
			replacements := make([]uint32, 1)
			candidates := make([]uint32, count)
			index := make([]bitmapCOWIndexNode, count+1)
			available := make([]int, count)
			ledger := newFreeBitmapCOWLedger(arena, replacements, candidates, index, available)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, problem := newFreeBitmapCOW(nil, 1, MaxPageCount, 0, ledger); problem.failed() {
					b.Fatal(problem)
				}
			}
		})
	}
}
