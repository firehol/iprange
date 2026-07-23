package exactv4

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func copiedPrivateBitmapPage(cow *freeBitmapCOW, pageNumber uint32) ([PageSize]byte, bool) {
	var page [PageSize]byte
	found := cow.copyPrivatePage(pageNumber, &page)
	return page, found
}

type cowSparsePage struct {
	pageNumber uint32
	bytes      [PageSize]byte
	reads      int
}

type cowSparsePages struct {
	pages   []cowSparsePage
	reads   int
	access  *pageSourceError
	missing pageSourceError
}

func (s *cowSparsePages) checkAccessStatus() pageSourceStatus { return s.access.status() }

func (s *cowSparsePages) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	s.reads++
	for index := range s.pages {
		if s.pages[index].pageNumber == pageNumber {
			s.pages[index].reads++
			copy(destination[:], s.pages[index].bytes[:])
			return pageSourceStatus{}
		}
	}
	s.missing = pageSourceError{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	return s.missing.status()
}

func (s *cowSparsePages) pageReads(pageNumber uint32) int {
	for index := range s.pages {
		if s.pages[index].pageNumber == pageNumber {
			return s.pages[index].reads
		}
	}
	return 0
}

type cowChild struct {
	index int
	page  uint32
}

func cowLeaf(t *testing.T, pageNumber uint32, txn uint64, setBits ...uint32) cowSparsePage {
	t.Helper()
	var page cowSparsePage
	page.pageNumber = pageNumber
	for _, bit := range setBits {
		wordIndex := int(bit / 64)
		if wordIndex >= BitmapLeafWords {
			t.Fatalf("test leaf bit %d is outside a leaf", bit)
		}
		at := bitmapSummaryOffset + wordIndex*8
		word := binary.LittleEndian.Uint64(page.bytes[at : at+8])
		binary.LittleEndian.PutUint64(page.bytes[at:at+8], word|(uint64(1)<<uint(bit%64)))
	}
	itemCount := uint16(0)
	for index := 0; index < BitmapLeafWords; index++ {
		at := bitmapSummaryOffset + index*8
		if binary.LittleEndian.Uint64(page.bytes[at:at+8]) != 0 {
			itemCount++
		}
	}
	cowSealBitmapPage(t, &page.bytes, PageTypeBitmapLeaf, txn, itemCount, 0, bitmapLeafLower)
	return page
}

func cowBranch(
	t *testing.T,
	pageNumber uint32,
	txn uint64,
	level uint16,
	children ...cowChild,
) cowSparsePage {
	t.Helper()
	var page cowSparsePage
	page.pageNumber = pageNumber
	for _, child := range children {
		if child.index < 0 || uint64(child.index) >= BitmapFanout || child.page == 0 {
			t.Fatalf("invalid test branch child %d/%d", child.index, child.page)
		}
		summaryAt := bitmapSummaryOffset + (child.index/64)*8
		summary := binary.LittleEndian.Uint64(page.bytes[summaryAt : summaryAt+8])
		binary.LittleEndian.PutUint64(
			page.bytes[summaryAt:summaryAt+8],
			summary|(uint64(1)<<uint(child.index%64)),
		)
		childAt := bitmapChildrenOffset + child.index*4
		binary.LittleEndian.PutUint32(page.bytes[childAt:childAt+4], child.page)
	}
	cowSealBitmapPage(
		t,
		&page.bytes,
		PageTypeBitmapBranch,
		txn,
		uint16(len(children)),
		level,
		bitmapBranchLower,
	)
	return page
}

func cowSealBitmapPage(
	t *testing.T,
	page *[PageSize]byte,
	pageType PageType,
	txn uint64,
	itemCount, level, lower uint16,
) {
	t.Helper()
	header := PageHeader{
		PageType:  pageType,
		BornTxn:   txn,
		ItemCount: itemCount,
		Level:     level,
		Lower:     lower,
		Upper:     PageSize,
		Aux:       uint32(bitmapKindFreePages),
	}
	if err := header.EncodeInto(page[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePageCRC32C(page[:]); err != nil {
		t.Fatal(err)
	}
}

func emptyFreeBitmapCOWLedger(
	arena []reservedBitmapPage,
	replacements []uint32,
	candidates []uint32,
) freeBitmapCOWLedger {
	return newFreeBitmapCOWLedger(
		arena,
		replacements,
		candidates,
		make([]bitmapCOWIndexNode, len(arena)+len(replacements)),
		make([]int, len(arena)),
	)
}

func prefixedFreeBitmapCOWLedger(
	arena []reservedBitmapPage,
	replacements []uint32,
	replacementLen int,
	candidates []uint32,
	candidateLen int,
) freeBitmapCOWLedger {
	ledger := emptyFreeBitmapCOWLedger(arena, replacements, candidates)
	ledger.replacementLen = replacementLen
	ledger.candidateLen = candidateLen
	return ledger
}

func requireFreeBitmapCOWCode(
	t *testing.T,
	problem freeBitmapCOWError,
	want freeBitmapCOWErrorCode,
) freeBitmapCOWError {
	t.Helper()
	if !problem.failed() {
		t.Fatalf("expected free-bitmap COW error %d", want)
	}
	if problem.code != want {
		t.Fatalf("free-bitmap COW code = %d, want %d", problem.code, want)
	}
	return problem
}

func mustBitmapCoverage(t *testing.T, level uint16) uint64 {
	t.Helper()
	covered, err := bitmapCoverage(level)
	if err != nil {
		t.Fatal(err)
	}
	return covered
}

func TestFreeBitmapCOWCanonicalEncodingAndVerifiedOncePrivatePath(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}),
		cowLeaf(t, 3, 1, 5, 6),
	}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10), newReservedBitmapPage(11)}
	replacements := make([]uint32, 4)
	candidates := make([]uint32, 4)
	cow, err := newFreeBitmapCOW(
		source,
		1,
		32_001,
		2,
		emptyFreeBitmapCOWLedger(arena, replacements, candidates),
	)
	if err.failed() {
		t.Fatal(err)
	}

	reserved, ok, err := cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 5 {
		t.Fatalf("first removal = %d/%t/%v, want 5/true/nil", reserved.pageNumber, ok, err)
	}
	if cow.root != 10 || !equalU32(cow.replacementPages(), []uint32{2, 3}) ||
		!equalU32(cow.candidatePages(), []uint32{5}) {
		t.Fatalf("first draft = root %d, replacements %v, candidates %v", cow.root, cow.replacementPages(), cow.candidatePages())
	}
	readsAfterFirst := source.reads

	root, found := copiedPrivateBitmapPage(cow, 10)
	expectedRoot := cowBranch(t, 10, 2, 1, cowChild{index: 0, page: 11})
	if !found || !bytes.Equal(root[:], expectedRoot.bytes[:]) || !VerifyPageCRC32C(root[:]) {
		t.Fatal("private branch is not the exact canonical encoding")
	}
	leaf, found := copiedPrivateBitmapPage(cow, 11)
	expectedLeaf := cowLeaf(t, 11, 2, 6)
	if !found || !bytes.Equal(leaf[:], expectedLeaf.bytes[:]) || !VerifyPageCRC32C(leaf[:]) {
		t.Fatal("private leaf is not the exact canonical encoding")
	}

	reserved, ok, err = cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 6 {
		t.Fatalf("second removal = %d/%t/%v, want 6/true/nil", reserved.pageNumber, ok, err)
	}
	if source.reads != readsAfterFirst {
		t.Fatalf("second private-path removal reread committed pages: %d -> %d", readsAfterFirst, source.reads)
	}
	if cow.root != 0 || !equalU32(cow.replacementPages(), []uint32{2, 3}) ||
		!equalU32(cow.candidatePages(), []uint32{5, 6}) || cow.availablePrivatePages() != 2 {
		t.Fatalf("collapsed draft = root %d, replacements %v, candidates %v, available %d", cow.root, cow.replacementPages(), cow.candidatePages(), cow.availablePrivatePages())
	}
}

func TestFreeBitmapCOWChecksAccessBeforePrivateCachedPath(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	cow, problem := newFreeBitmapCOW(
		source,
		1,
		20,
		2,
		emptyFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(10)},
			make([]uint32, 1),
			make([]uint32, 2),
		),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if page, ok, problem := cow.removeLowest(); problem.failed() || !ok || page.pageNumber != 5 {
		t.Fatalf("first removal = %+v/%t/%v", page, ok, problem)
	}
	reads := source.reads
	forkEvidence := &pageSourceError{code: pageSourceErrForkedHandle}
	source.access = forkEvidence
	_, ok, problem := cow.removeLowest()
	if ok || problem.code != freeBitmapCOWErrSource || problem.source != forkEvidence.status() {
		t.Fatalf("cached access result = %t/%+v", ok, problem)
	}
	if source.reads != reads {
		t.Fatalf("cached access performed read: %d -> %d", reads, source.reads)
	}
}

func TestFreeBitmapCOWSiblingSwitchDoesNotRereadPrivateRoot(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}, cowChild{index: 1, page: 4}),
		cowLeaf(t, 3, 1, 5),
		cowLeaf(t, 4, 1, 0),
	}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10)}
	cow, err := newFreeBitmapCOW(
		source,
		1,
		32_001,
		2,
		emptyFreeBitmapCOWLedger(arena, make([]uint32, 3), make([]uint32, 2)),
	)
	if err.failed() {
		t.Fatal(err)
	}

	reserved, ok, err := cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 5 {
		t.Fatalf("first removal = %d/%t/%v", reserved.pageNumber, ok, err)
	}
	if source.pageReads(2) != 1 || source.pageReads(3) != 1 || source.pageReads(4) != 0 {
		t.Fatalf("first path reads = root %d, leaf0 %d, leaf1 %d", source.pageReads(2), source.pageReads(3), source.pageReads(4))
	}
	expectedRoot := cowBranch(t, 10, 2, 1, cowChild{index: 1, page: 4})
	if root, found := copiedPrivateBitmapPage(cow, 10); !found || !bytes.Equal(root[:], expectedRoot.bytes[:]) {
		t.Fatal("surviving sibling root is not canonical")
	}

	reserved, ok, err = cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 32_000 {
		t.Fatalf("sibling removal = %d/%t/%v", reserved.pageNumber, ok, err)
	}
	if source.pageReads(2) != 1 || source.pageReads(3) != 1 || source.pageReads(4) != 1 {
		t.Fatalf("second path reads = root %d, leaf0 %d, leaf1 %d", source.pageReads(2), source.pageReads(3), source.pageReads(4))
	}
	if cow.root != 0 || !equalU32(cow.replacementPages(), []uint32{2, 3, 4}) ||
		!equalU32(cow.candidatePages(), []uint32{5, 32_000}) || cow.availablePrivatePages() != 1 {
		t.Fatalf("final sibling draft = root %d, replacements %v, candidates %v", cow.root, cow.replacementPages(), cow.candidatePages())
	}
}

func TestFreeBitmapCOWNonzeroLeafBaseUsesLocalOffset(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 1, page: 3}),
		cowLeaf(t, 3, 1, 0, 1),
	}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10), newReservedBitmapPage(11)}
	cow, err := newFreeBitmapCOW(
		source,
		1,
		32_002,
		2,
		emptyFreeBitmapCOWLedger(arena, make([]uint32, 2), make([]uint32, 1)),
	)
	if err.failed() {
		t.Fatal(err)
	}
	reserved, ok, err := cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 32_000 {
		t.Fatalf("removal = %d/%t/%v, want 32000/true/nil", reserved.pageNumber, ok, err)
	}
	expectedRoot := cowBranch(t, 10, 2, 1, cowChild{index: 1, page: 11})
	expectedLeaf := cowLeaf(t, 11, 2, 1)
	root, rootFound := copiedPrivateBitmapPage(cow, 10)
	leaf, leafFound := copiedPrivateBitmapPage(cow, 11)
	if !rootFound || !leafFound || !bytes.Equal(root[:], expectedRoot.bytes[:]) ||
		!bytes.Equal(leaf[:], expectedLeaf.bytes[:]) {
		t.Fatal("nonzero-base COW pages are not canonical")
	}
}

func TestFreeBitmapCOWLastBitCollapsesCommittedMultilevelPath(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 1, cowChild{index: 0, page: 3}),
		cowLeaf(t, 3, 1, 5),
	}}
	cow, err := newFreeBitmapCOW(
		source,
		1,
		32_001,
		2,
		emptyFreeBitmapCOWLedger(nil, make([]uint32, 2), make([]uint32, 1)),
	)
	if err.failed() {
		t.Fatal(err)
	}
	reserved, ok, err := cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != 5 {
		t.Fatalf("removal = %d/%t/%v", reserved.pageNumber, ok, err)
	}
	if cow.root != 0 || !equalU32(cow.replacementPages(), []uint32{2, 3}) ||
		!equalU32(cow.candidatePages(), []uint32{5}) || cow.availablePrivatePages() != 0 {
		t.Fatalf("collapsed draft = root %d, replacements %v, candidates %v", cow.root, cow.replacementPages(), cow.candidatePages())
	}
}

func TestFreeBitmapCOWCorruptionAbortsBeforeDraftMutation(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	source.pages[0].bytes[100] ^= 1
	arena := []reservedBitmapPage{newReservedBitmapPage(10)}
	cow, err := newFreeBitmapCOW(
		source,
		1,
		20,
		2,
		emptyFreeBitmapCOWLedger(arena, make([]uint32, 1), make([]uint32, 1)),
	)
	if err.failed() {
		t.Fatal(err)
	}
	pristine := cow.pool.slots[0]
	_, _, err = cow.removeLowest()
	cowErr := requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrPage)
	if cowErr.pageProblem.code != bitmapPageErrChecksum {
		t.Fatalf("corruption cause = %d, want bitmap checksum", cowErr.pageProblem.code)
	}
	if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 || cow.pool.slots[0] != pristine {
		t.Fatal("checksum failure mutated draft state")
	}
}

func TestFreeBitmapCOWCapacityFailuresAreAtomic(t *testing.T) {
	run := func(arenaCapacity, replacementCapacity, candidateCapacity int) freeBitmapCOWErrorCode {
		t.Helper()
		source := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
		arenaStorage := [1]reservedBitmapPage{newReservedBitmapPage(10)}
		replacementStorage := [1]uint32{}
		candidateStorage := [1]uint32{}
		cow, err := newFreeBitmapCOW(
			source,
			1,
			20,
			2,
			emptyFreeBitmapCOWLedger(
				arenaStorage[:arenaCapacity],
				replacementStorage[:replacementCapacity],
				candidateStorage[:candidateCapacity],
			),
		)
		if err.failed() {
			t.Fatal(err)
		}
		var pristine reservedBitmapPage
		if arenaCapacity != 0 {
			pristine = cow.pool.slots[0]
		}
		_, _, err = cow.removeLowest()
		cowErr := err
		if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 {
			t.Fatal("capacity failure mutated ledgers or root")
		}
		if arenaCapacity != 0 && cow.pool.slots[0] != pristine {
			t.Fatal("capacity failure mutated arena")
		}
		return cowErr.code
	}

	if got := run(0, 1, 1); got != freeBitmapCOWErrPrivateArenaExhausted {
		t.Fatalf("arena failure = %d", got)
	}
	if got := run(1, 0, 1); got != freeBitmapCOWErrReplacementLedgerExhausted {
		t.Fatalf("replacement failure = %d", got)
	}
	if got := run(1, 1, 0); got != freeBitmapCOWErrCandidateLedgerExhausted {
		t.Fatalf("candidate failure = %d", got)
	}
}

func TestFreeBitmapCOWEveryInsufficientMultilevelCapacityIsAtomic(t *testing.T) {
	run := func(arenaCapacity, replacementCapacity int) freeBitmapCOWErrorCode {
		t.Helper()
		source := &cowSparsePages{pages: []cowSparsePage{
			cowBranch(t, 2, 1, 2, cowChild{index: 0, page: 3}),
			cowBranch(t, 3, 1, 1, cowChild{index: 0, page: 4}),
			cowLeaf(t, 4, 1, 5, 6),
		}}
		arenaStorage := [3]reservedBitmapPage{
			newReservedBitmapPage(10),
			newReservedBitmapPage(11),
			newReservedBitmapPage(12),
		}
		replacementStorage := [3]uint32{}
		candidateStorage := [1]uint32{}
		cow, err := newFreeBitmapCOW(
			source,
			1,
			mustBitmapCoverage(t, 1)+1,
			2,
			emptyFreeBitmapCOWLedger(
				arenaStorage[:arenaCapacity],
				replacementStorage[:replacementCapacity],
				candidateStorage[:],
			),
		)
		if err.failed() {
			t.Fatal(err)
		}
		pristine := append([]reservedBitmapPage(nil), cow.pool.slots...)
		_, _, err = cow.removeLowest()
		cowErr := err
		if cow.root != 2 || len(cow.replacementPages()) != 0 || len(cow.candidatePages()) != 0 ||
			cow.availablePrivatePages() != arenaCapacity {
			t.Fatal("multilevel capacity failure mutated draft")
		}
		for index := range cow.pool.slots {
			if cow.pool.slots[index] != pristine[index] {
				t.Fatalf("arena slot %d mutated", index)
			}
		}
		return cowErr.code
	}

	for arenaCapacity := 0; arenaCapacity < 3; arenaCapacity++ {
		if got := run(arenaCapacity, 3); got != freeBitmapCOWErrPrivateArenaExhausted {
			t.Fatalf("arena capacity %d error = %d", arenaCapacity, got)
		}
	}
	for replacementCapacity := 0; replacementCapacity < 3; replacementCapacity++ {
		if got := run(3, replacementCapacity); got != freeBitmapCOWErrReplacementLedgerExhausted {
			t.Fatalf("replacement capacity %d error = %d", replacementCapacity, got)
		}
	}
}

func TestFreeBitmapCOWLaterCloneEpochOverflowIsAtomic(t *testing.T) {
	newCOW := func(t *testing.T) *freeBitmapCOW {
		t.Helper()
		source := &cowSparsePages{pages: []cowSparsePage{
			cowBranch(t, 2, 1, 2, cowChild{index: 0, page: 3}),
			cowBranch(t, 3, 1, 1, cowChild{index: 0, page: 4}),
			cowLeaf(t, 4, 1, 5, 6),
		}}
		arena := []reservedBitmapPage{
			newReservedBitmapPage(10),
			newReservedBitmapPage(11),
			newReservedBitmapPage(12),
		}
		cow, problem := newFreeBitmapCOW(
			source, 1, mustBitmapCoverage(t, 1)+1, 2,
			emptyFreeBitmapCOWLedger(arena, make([]uint32, 3), make([]uint32, 1)),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		return cow
	}
	for _, test := range []struct {
		name   string
		inject func(*freeBitmapCOW)
		want   freeBitmapCOWErrorCode
	}{
		{
			name: "late slot epoch",
			inject: func(cow *freeBitmapCOW) {
				// Removal applies the leaf clone before this parent clone.
				cow.pool.slots[1].epoch = ^uint64(0)
			},
			want: freeBitmapCOWErrMutationEpochExhausted,
		},
		{
			name: "late slot owner",
			inject: func(cow *freeBitmapCOW) {
				cow.pool.slots[1].owner = privatePageOwnerRetirement
			},
			want: freeBitmapCOWErrArenaPageConflict,
		},
		{
			name: "aggregate mutation headroom",
			inject: func(cow *freeBitmapCOW) {
				cow.pool.mutationEpoch = ^uint64(0) - 8
			},
			want: freeBitmapCOWErrMutationEpochExhausted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cow := newCOW(t)
			test.inject(cow)
			slotsBefore := append([]privatePagePoolSlot(nil), cow.pool.slots...)
			replacementsBefore := append([]uint32(nil), cow.replacements...)
			candidatesBefore := append([]uint32(nil), cow.candidates...)
			indexBefore := append([]bitmapCOWIndexNode(nil), cow.indexNodes...)
			availableBefore := append([]int(nil), cow.availableSlots...)
			mutationBefore, generationBefore := cow.pool.mutationEpoch, cow.pool.generation
			operationSequenceBefore := cow.pool.operationSequence
			_, _, problem := cow.removeLowest()
			requireFreeBitmapCOWCode(t, problem, test.want)
			for index := range slotsBefore {
				if cow.pool.slots[index] != slotsBefore[index] {
					t.Fatalf("rejected removal mutated slot %d", index)
				}
			}
			for index := range replacementsBefore {
				if cow.replacements[index] != replacementsBefore[index] {
					t.Fatalf("rejected removal mutated replacement %d", index)
				}
			}
			for index := range candidatesBefore {
				if cow.candidates[index] != candidatesBefore[index] {
					t.Fatalf("rejected removal mutated candidate %d", index)
				}
			}
			for index := range indexBefore {
				if cow.indexNodes[index] != indexBefore[index] {
					t.Fatalf("rejected removal mutated index %d", index)
				}
			}
			for index := range availableBefore {
				if cow.availableSlots[index] != availableBefore[index] {
					t.Fatalf("rejected removal mutated available slot %d", index)
				}
			}
			if cow.root != 2 || cow.replacementLen != 0 || cow.candidateLen != 0 ||
				cow.pool.mutationEpoch != mutationBefore || cow.pool.generation != generationBefore ||
				cow.pool.operationSequence != operationSequenceBefore || cow.pool.activeOperationID != 0 {
				t.Fatal("rejected removal partially mutated bitmap or operation state")
			}
		})
	}
}

func TestFreeBitmapCOWMaximumPageCountRemovesUint32Max(t *testing.T) {
	candidate := uint64(^uint32(0))
	levelTwoSpan := mustBitmapCoverage(t, 2)
	levelOneSpan := mustBitmapCoverage(t, 1)
	leafSpan := mustBitmapCoverage(t, 0)
	rootIndex := int(candidate / levelTwoSpan)
	afterRoot := candidate % levelTwoSpan
	levelTwoIndex := int(afterRoot / levelOneSpan)
	afterLevelTwo := afterRoot % levelOneSpan
	levelOneIndex := int(afterLevelTwo / leafSpan)
	leafBit := uint32(afterLevelTwo % leafSpan)
	if rootIndex != 2 || levelTwoIndex != 12 || levelOneIndex != 73 || leafBit != 23_295 {
		t.Fatalf("maximum path = %d/%d/%d/%d", rootIndex, levelTwoIndex, levelOneIndex, leafBit)
	}

	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 3, cowChild{index: rootIndex, page: 3}),
		cowBranch(t, 3, 1, 2, cowChild{index: levelTwoIndex, page: 4}),
		cowBranch(t, 4, 1, 1, cowChild{index: levelOneIndex, page: 5}),
		cowLeaf(t, 5, 1, leafBit),
	}}
	cow, err := newFreeBitmapCOW(
		source,
		1,
		MaxPageCount,
		2,
		emptyFreeBitmapCOWLedger(nil, make([]uint32, freeBitmapPathCapacity), make([]uint32, 1)),
	)
	if err.failed() {
		t.Fatal(err)
	}
	reserved, ok, err := cow.removeLowest()
	if err.failed() || !ok || reserved.pageNumber != ^uint32(0) {
		t.Fatalf("maximum removal = %d/%t/%v", reserved.pageNumber, ok, err)
	}
	if cow.root != 0 || !equalU32(cow.replacementPages(), []uint32{2, 3, 4, 5}) ||
		!equalU32(cow.candidatePages(), []uint32{^uint32(0)}) {
		t.Fatalf("maximum draft = root %d, replacements %v, candidates %v", cow.root, cow.replacementPages(), cow.candidatePages())
	}
	for pageNumber := uint32(2); pageNumber <= 5; pageNumber++ {
		if source.pageReads(pageNumber) != 1 {
			t.Fatalf("page %d reads = %d, want 1", pageNumber, source.pageReads(pageNumber))
		}
	}
}

func TestFreeBitmapCOWDuplicateSelfAndLedgerAliasesFailBeforeMutation(t *testing.T) {
	duplicateSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	arena := []reservedBitmapPage{newReservedBitmapPage(10)}
	replacements := make([]uint32, 1)
	candidates := []uint32{5, 0}
	ledger := emptyFreeBitmapCOWLedger(arena, replacements, candidates)
	ledger.candidateLen = 1
	duplicate, err := newFreeBitmapCOW(duplicateSource, 1, 20, 2, ledger)
	if err.failed() {
		t.Fatal(err)
	}
	_, _, err = duplicate.removeLowest()
	requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrCandidateAlreadyReserved)
	if duplicate.root != 2 || len(duplicate.replacementPages()) != 0 ||
		!equalU32(duplicate.candidatePages(), []uint32{5}) {
		t.Fatal("duplicate candidate failure mutated draft")
	}

	selfSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 2)}}
	self, err := newFreeBitmapCOW(
		selfSource,
		1,
		20,
		2,
		emptyFreeBitmapCOWLedger(nil, make([]uint32, 1), make([]uint32, 1)),
	)
	if err.failed() {
		t.Fatal(err)
	}
	_, _, err = self.removeLowest()
	requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrCandidateIsPathPage)
	if self.root != 2 || len(self.replacementPages()) != 0 || len(self.candidatePages()) != 0 {
		t.Fatal("self candidate failure mutated draft")
	}

	for _, test := range []struct {
		name   string
		ledger freeBitmapCOWLedger
	}{
		{
			name: "arena-replacement",
			ledger: prefixedFreeBitmapCOWLedger(
				[]reservedBitmapPage{newReservedBitmapPage(5)},
				[]uint32{5}, 1, nil, 0,
			),
		},
		{
			name: "arena-candidate",
			ledger: prefixedFreeBitmapCOWLedger(
				[]reservedBitmapPage{newReservedBitmapPage(5)},
				nil, 0, []uint32{5}, 1,
			),
		},
		{
			name: "replacement-candidate",
			ledger: prefixedFreeBitmapCOWLedger(
				nil, []uint32{5}, 1, []uint32{5}, 1,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newFreeBitmapCOW(nil, 1, 20, 0, test.ledger)
			requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrLedgerPageConflict)
		})
	}

	arenaCandidateSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	arenaCandidate, err := newFreeBitmapCOW(
		arenaCandidateSource,
		1,
		20,
		2,
		emptyFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(5)},
			make([]uint32, 1),
			make([]uint32, 1),
		),
	)
	if err.failed() {
		t.Fatal(err)
	}
	_, _, err = arenaCandidate.removeLowest()
	requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrCandidateIsArenaPage)

	draftReplacementSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	draftReplacementLedger := emptyFreeBitmapCOWLedger(
		[]reservedBitmapPage{newReservedBitmapPage(10)},
		[]uint32{5, 0},
		make([]uint32, 1),
	)
	draftReplacementLedger.replacementLen = 1
	draftReplacement, err := newFreeBitmapCOW(
		draftReplacementSource,
		1,
		20,
		2,
		draftReplacementLedger,
	)
	if err.failed() {
		t.Fatal(err)
	}
	_, _, err = draftReplacement.removeLowest()
	requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrCandidateIsDraftReplacement)

	pathAliasSource := &cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 6)}}
	pathAlias, err := newFreeBitmapCOW(
		pathAliasSource,
		1,
		20,
		2,
		emptyFreeBitmapCOWLedger(
			[]reservedBitmapPage{newReservedBitmapPage(2)},
			make([]uint32, 1),
			make([]uint32, 1),
		),
	)
	if err.failed() {
		t.Fatal(err)
	}
	_, _, err = pathAlias.removeLowest()
	requireFreeBitmapCOWCode(t, err, freeBitmapCOWErrArenaPageConflict)
}

func TestFreeBitmapCOWRemovalAllocatesNothing(t *testing.T) {
	source := &cowSparsePages{pages: []cowSparsePage{
		cowBranch(t, 2, 1, 2, cowChild{index: 0, page: 3}),
		cowBranch(t, 3, 1, 1, cowChild{index: 0, page: 4}),
		cowLeaf(t, 4, 1, 5, 6),
	}}
	arena := []reservedBitmapPage{
		newReservedBitmapPage(10),
		newReservedBitmapPage(11),
		newReservedBitmapPage(12),
	}
	replacements := make([]uint32, 3)
	candidates := make([]uint32, 1)
	cow, err := newFreeBitmapCOW(
		source,
		1,
		mustBitmapCoverage(t, 1)+1,
		2,
		emptyFreeBitmapCOWLedger(arena, replacements, candidates),
	)
	if err.failed() {
		t.Fatal(err)
	}
	initialIndex := append([]bitmapCOWIndexNode(nil), cow.indexNodes...)
	initialAvailable := append([]int(nil), cow.availableSlots...)
	initialArena := append([]reservedBitmapPage(nil), arena...)
	initialIndexRoot := cow.indexRoot
	initialIndexLen := cow.indexLen
	initialAvailableLen := cow.availableLen

	allocations := testing.AllocsPerRun(100, func() {
		copy(arena, initialArena)
		clear(replacements)
		clear(candidates)
		copy(cow.indexNodes, initialIndex)
		copy(cow.availableSlots, initialAvailable)
		cow.root = 2
		cow.replacementLen = 0
		cow.candidateLen = 0
		cow.indexRoot = initialIndexRoot
		cow.indexLen = initialIndexLen
		cow.availableLen = initialAvailableLen
		reserved, ok, err := cow.removeLowest()
		if err.failed() || !ok || reserved.pageNumber != 5 {
			panic("unexpected free-bitmap COW result")
		}
	})
	if allocations != 0 {
		t.Fatalf("free-bitmap COW allocations = %v, want 0", allocations)
	}
}

func equalU32(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
