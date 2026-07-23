package exactv4

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func retirementImage(pages int) []byte {
	return make([]byte, pages*PageSize)
}

func retirementImagePage(data []byte, page int) []byte {
	return data[page*PageSize : (page+1)*PageSize]
}

func testRetirementIdentity(pageCount uint64, root uint32, batchCount uint64) retirementIdentity {
	return retirementIdentity{
		databaseID:  [16]byte{1},
		txnID:       8,
		commitNonce: [16]byte{2},
		pageCount:   pageCount,
		root:        root,
		batchCount:  batchCount,
	}
}

func putRetirementBlob(t *testing.T, page []byte, pages []uint32) {
	t.Helper()
	payload := make([]byte, len(pages)*4)
	for index, value := range pages {
		binary.LittleEndian.PutUint32(payload[index*4:index*4+4], value)
	}
	putBlobLeafPage(t, page, blobKindRetirementPageList, 0, payload)
}

func sampleRetirementImage(t *testing.T) []byte {
	t.Helper()
	data := retirementImage(20)
	putRetirementLeafPage(t, retirementImagePage(data, 2), []retirementBatch{
		{retiredByTxn: 2, pageCount: 2, pageListBlobRoot: 3},
		{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 4},
		{retiredByTxn: 6, pageCount: 3, pageListBlobRoot: 5},
	})
	putRetirementBlob(t, retirementImagePage(data, 3), []uint32{10, 11})
	putRetirementBlob(t, retirementImagePage(data, 4), []uint32{12})
	putRetirementBlob(t, retirementImagePage(data, 5), []uint32{13, 14, 15})
	return data
}

func mustRetirementTree(
	t *testing.T,
	data []byte,
	identity retirementIdentity,
) retirementTree[immutableSlicePageSource] {
	t.Helper()
	tree, err := newRetirementTree(data, identity)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func mustRetirementSelection[S committedPageSource](
	t *testing.T,
	tree *retirementTree[S],
	readerThreshold uint64,
	maxBatches uint64,
	maxPages uint64,
) retirementSelection {
	t.Helper()
	selection, ok, err := tree.selectOldestEligible(readerThreshold, maxBatches, maxPages)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected retirement selection")
	}
	return selection
}

func requireRetirementReadCode(
	t *testing.T,
	err error,
	want retirementReadErrorCode,
) *retirementReadError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected retirement-read error %d", want)
	}
	var got *retirementReadError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *retirementReadError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("retirement-read code = %d, want %d", got.code, want)
	}
	return got
}

func requireRetirementSecondPassCode(
	t *testing.T,
	err error,
	want retirementSecondPassErrorCode,
) *retirementSecondPassError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected retirement-second-pass error %d", want)
	}
	var got *retirementSecondPassError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *retirementSecondPassError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("retirement-second-pass code = %d, want %d", got.code, want)
	}
	return got
}

func TestRetirementTreeConstructorChecksIdentityRootCountAndCommittedBounds(t *testing.T) {
	data := retirementImage(3)
	invalid := testRetirementIdentity(3, 0, 0)
	invalid.databaseID = [16]byte{}
	_, err := newRetirementTree(data, invalid)
	requireRetirementReadCode(t, err, retirementReadErrIdentityInvalid)

	_, err = newRetirementTree(data, testRetirementIdentity(3, 2, 0))
	requireRetirementReadCode(t, err, retirementReadErrRootCountMismatch)

	_, err = newRetirementTree(nil, testRetirementIdentity(0, 0, 0))
	requireRetirementReadCode(t, err, retirementReadErrCommittedPageCountOutOfRange)

	_, err = newRetirementTree(nil, testRetirementIdentity(MaxPageCount+1, 0, 0))
	requireRetirementReadCode(t, err, retirementReadErrCommittedPageCountOutOfRange)

	_, err = newRetirementTree(data, testRetirementIdentity(3, 0, 1))
	requireRetirementReadCode(t, err, retirementReadErrRootCountMismatch)

	_, err = newRetirementTree(data, testRetirementIdentity(4, 2, 1))
	requireRetirementReadCode(t, err, retirementReadErrPageOutOfBounds)

	invalid = testRetirementIdentity(3, 2, 1)
	invalid.txnID = 1
	_, err = newRetirementTree(data, invalid)
	requireRetirementReadCode(t, err, retirementReadErrBatchCountOutOfRange)

	empty := mustRetirementTree(t, data[:2*PageSize], testRetirementIdentity(2, 0, 0))
	if _, ok, err := empty.selectOldestEligible(8, 1, 1); err != nil || ok {
		t.Fatalf("empty selection = %t/%v, want false/nil", ok, err)
	}
}

func TestRetirementSelectionUsesOldestCompleteEligiblePrefixAndExactLimits(t *testing.T) {
	data := sampleRetirementImage(t)
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))

	selection := mustRetirementSelection(t, &tree, 4, 10, 10)
	want := retirementSelection{
		identity:         testRetirementIdentity(20, 2, 3),
		batchCount:       2,
		pageCount:        3,
		lastRetiredByTxn: 4,
	}
	if selection != want {
		t.Fatalf("selection = %+v, want %+v", selection, want)
	}
	if got := mustRetirementSelection(t, &tree, 3, 10, 10); got.batchCount != 1 {
		t.Fatalf("threshold selection batches = %d, want 1", got.batchCount)
	}
	for _, threshold := range []uint64{0, 1} {
		if _, ok, err := tree.selectOldestEligible(threshold, 10, 10); err != nil || ok {
			t.Fatalf("threshold %d selection = %t/%v, want false/nil", threshold, ok, err)
		}
	}
	if got := mustRetirementSelection(t, &tree, 8, 1, 10); got.pageCount != 2 {
		t.Fatalf("batch-limited pages = %d, want 2", got.pageCount)
	}
	if got := mustRetirementSelection(t, &tree, 8, 10, 2); got.batchCount != 1 {
		t.Fatalf("page-limited batches = %d, want 1", got.batchCount)
	}
	_, _, err := tree.selectOldestEligible(8, 10, 1)
	readErr := requireRetirementReadCode(t, err, retirementReadErrWorkLimitTooSmall)
	if readErr.requiredPages != 2 {
		t.Fatalf("required pages = %d, want 2", readErr.requiredPages)
	}
	_, _, err = tree.selectOldestEligible(8, 0, 1)
	requireRetirementReadCode(t, err, retirementReadErrWorkLimitZero)

	data[2*PageSize+PageCRCOffset] ^= 1
	tree = mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	if got := mustRetirementSelection(t, &tree, 3, 10, 10); got.batchCount != 1 {
		t.Fatalf("ordinary selection checked CRC, batches = %d", got.batchCount)
	}
}

func TestRetirementVerifiedFirstPassAndSecondPassYieldExactPages(t *testing.T) {
	data := sampleRetirementImage(t)
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	selection := mustRetirementSelection(t, &tree, 4, 10, 10)
	scratch := make([]retirementBatch, 2)
	verified, err := tree.verifySelection(selection, scratch)
	if err != nil {
		t.Fatal(err)
	}
	type yieldedPage struct {
		transaction uint64
		page        uint32
	}
	var yielded []yieldedPage
	result, err := verified.secondPass(&tree, func(batch retirementBatch, page uint32) error {
		yielded = append(yielded, yieldedPage{transaction: batch.retiredByTxn, page: page})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []yieldedPage{{2, 10}, {2, 11}, {4, 12}}
	if !reflect.DeepEqual(yielded, want) {
		t.Fatalf("yielded = %v, want %v", yielded, want)
	}
	if result != (retirementPassResult{batchCount: 2, pageCount: 3}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestRetirementFirstPassChecksCRCLengthOrderRangeAndScratch(t *testing.T) {
	data := sampleRetirementImage(t)
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	selection := mustRetirementSelection(t, &tree, 4, 10, 10)
	_, err := tree.verifySelection(selection, make([]retirementBatch, 1))
	readErr := requireRetirementReadCode(t, err, retirementReadErrVerificationBufferTooSmall)
	if readErr.requiredBatches != 2 {
		t.Fatalf("required batches = %d, want 2", readErr.requiredBatches)
	}

	bad := append([]byte(nil), data...)
	bad[2*PageSize+PageCRCOffset] ^= 1
	badTree := mustRetirementTree(t, bad, testRetirementIdentity(20, 2, 3))
	_, err = badTree.verifySelection(selection, make([]retirementBatch, 2))
	readErr = requireRetirementReadCode(t, err, retirementReadErrPage)
	requireRetirementPageCode(t, readErr, retirementPageErrChecksum)

	bad = append([]byte(nil), data...)
	bad[3*PageSize+PageCRCOffset] ^= 1
	badTree = mustRetirementTree(t, bad, testRetirementIdentity(20, 2, 3))
	_, err = badTree.verifySelection(selection, make([]retirementBatch, 2))
	readErr = requireRetirementReadCode(t, err, retirementReadErrBlob)
	requireBlobReadCode(t, readErr, blobReadErrPage)

	bad = append([]byte(nil), data...)
	putRetirementBlob(t, retirementImagePage(bad, 3), []uint32{10, 10})
	badTree = mustRetirementTree(t, bad, testRetirementIdentity(20, 2, 3))
	_, err = badTree.verifySelection(selection, make([]retirementBatch, 2))
	readErr = requireRetirementReadCode(t, err, retirementReadErrBlob)
	requireBlobReadCode(t, readErr, blobReadErrRetirementPageOrder)

	bad = append([]byte(nil), data...)
	putRetirementBlob(t, retirementImagePage(bad, 3), []uint32{1, 10})
	badTree = mustRetirementTree(t, bad, testRetirementIdentity(20, 2, 3))
	_, err = badTree.verifySelection(selection, make([]retirementBatch, 2))
	readErr = requireRetirementReadCode(t, err, retirementReadErrBlob)
	requireBlobReadCode(t, readErr, blobReadErrRetirementPageOutOfBounds)

	bad = append([]byte(nil), data...)
	putRetirementBlob(t, retirementImagePage(bad, 3), []uint32{10})
	badTree = mustRetirementTree(t, bad, testRetirementIdentity(20, 2, 3))
	_, err = badTree.verifySelection(selection, make([]retirementBatch, 2))
	readErr = requireRetirementReadCode(t, err, retirementReadErrBlob)
	requireBlobReadCode(t, readErr, blobReadErrNonfinalLeafLength)
}

func TestRetirementLateListCorruptionCannotProduceVerifiedOutput(t *testing.T) {
	const listedPages = blobLeafCapacity/4 + 2
	data := retirementImage(listedPages + 10)
	putRetirementLeafPage(t, retirementImagePage(data, 2), []retirementBatch{{
		retiredByTxn:     2,
		pageCount:        listedPages,
		pageListBlobRoot: 3,
	}})
	putBlobBranchPage(t, retirementImagePage(data, 3), blobKindRetirementPageList, 1, []blobBranchEntry{
		{logicalOffset: 0, childPage: 4},
		{logicalOffset: blobLeafCapacity, childPage: 5},
	})
	first := make([]byte, blobLeafCapacity)
	for index := 0; index < blobLeafCapacity/4; index++ {
		binary.LittleEndian.PutUint32(first[index*4:index*4+4], uint32(index+6))
	}
	putBlobLeafPage(t, retirementImagePage(data, 4), blobKindRetirementPageList, 0, first)
	last := make([]byte, 8)
	binary.LittleEndian.PutUint32(last[0:4], uint32(listedPages+4))
	binary.LittleEndian.PutUint32(last[4:8], uint32(listedPages+5))
	putBlobLeafPage(t, retirementImagePage(data, 5), blobKindRetirementPageList, blobLeafCapacity, last)
	retirementImagePage(data, 5)[PageCRCOffset] ^= 1

	identity := testRetirementIdentity(uint64(len(data)/PageSize), 2, 1)
	tree := mustRetirementTree(t, data, identity)
	selection := mustRetirementSelection(t, &tree, 8, 1, listedPages)
	_, err := tree.verifySelection(selection, make([]retirementBatch, 1))
	readErr := requireRetirementReadCode(t, err, retirementReadErrBlob)
	requireBlobReadCode(t, readErr, blobReadErrPage)
}

func TestRetirementSecondPassPreflightsIdentityAndAllBatchRootsBeforeSink(t *testing.T) {
	data := sampleRetirementImage(t)
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	selection := mustRetirementSelection(t, &tree, 4, 10, 10)
	scratch := make([]retirementBatch, 2)
	verified, err := tree.verifySelection(selection, scratch)
	if err != nil {
		t.Fatal(err)
	}

	changedIdentity := testRetirementIdentity(20, 2, 3)
	changedIdentity.commitNonce = [16]byte{3}
	changedTree := mustRetirementTree(t, data, changedIdentity)
	calls := 0
	_, err = verified.secondPass(&changedTree, func(retirementBatch, uint32) error {
		calls++
		return nil
	})
	requireRetirementSecondPassCode(t, err, retirementSecondPassErrRead)
	if calls != 0 {
		t.Fatalf("sink calls after identity change = %d, want 0", calls)
	}

	changedData := append([]byte(nil), data...)
	secondRootAt := int(PageHeaderSize) + retirementLeafRecordSize + 24
	binary.LittleEndian.PutUint32(retirementImagePage(changedData, 2)[secondRootAt:secondRootAt+4], 6)
	changedTree = mustRetirementTree(t, changedData, testRetirementIdentity(20, 2, 3))
	_, err = verified.secondPass(&changedTree, func(retirementBatch, uint32) error {
		calls++
		return nil
	})
	requireRetirementSecondPassCode(t, err, retirementSecondPassErrRead)
	if calls != 0 {
		t.Fatalf("sink calls after root change = %d, want 0", calls)
	}

	scratch[0].pageListBlobRoot = 6
	_, err = verified.secondPass(&tree, func(retirementBatch, uint32) error {
		calls++
		return nil
	})
	requireRetirementSecondPassCode(t, err, retirementSecondPassErrRead)
	if calls != 0 {
		t.Fatalf("sink calls after scratch change = %d, want 0", calls)
	}

	verified, err = tree.verifySelection(selection, scratch)
	if err != nil {
		t.Fatal(err)
	}
	sinkErr := errors.New("sink failed")
	_, err = verified.secondPass(&tree, func(retirementBatch, uint32) error {
		calls++
		return sinkErr
	})
	secondErr := requireRetirementSecondPassCode(t, err, retirementSecondPassErrSink)
	if !errors.Is(secondErr, sinkErr) {
		t.Fatalf("sink error = %v, want %v", secondErr, sinkErr)
	}
	if calls != 1 {
		t.Fatalf("sink calls on sink failure = %d, want 1", calls)
	}
}

func TestRetirementSecondPassRejectsSameSourceMutationBeforeRelease(t *testing.T) {
	data := sampleRetirementImage(t)
	source := &controlledPageSource{base: newImmutableSlicePageSource(data, 20)}
	tree, err := newRetirementTreeFromSource(source, testRetirementIdentity(20, 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	selection := mustRetirementSelection(t, &tree, 4, 10, 10)
	scratch := make([]retirementBatch, 2)
	verified, err := tree.verifySelection(selection, scratch)
	if err != nil {
		t.Fatal(err)
	}
	secondRootAt := int(PageHeaderSize) + retirementLeafRecordSize + 24
	binary.LittleEndian.PutUint32(
		retirementImagePage(data, 2)[secondRootAt:secondRootAt+4],
		6,
	)
	calls := 0
	_, err = verified.secondPass(&tree, func(retirementBatch, uint32) error {
		calls++
		return nil
	})
	requireRetirementSecondPassCode(t, err, retirementSecondPassErrRead)
	if calls != 0 {
		t.Fatalf("sink calls after same-source mutation = %d, want 0", calls)
	}
}

func TestRetirementBranchTreeChecksMaximaCrossPageOrderLevelsAndDepth(t *testing.T) {
	data := retirementImage(20)
	putRetirementBranchPage(t, retirementImagePage(data, 2), 1, []retirementBranchEntry{
		{maxRetiredByTxn: 4, childPage: 3},
		{maxRetiredByTxn: 6, childPage: 4},
	})
	putRetirementLeafPage(t, retirementImagePage(data, 3), []retirementBatch{
		{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 5},
		{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 6},
	})
	putRetirementLeafPage(t, retirementImagePage(data, 4), []retirementBatch{
		{retiredByTxn: 6, pageCount: 1, pageListBlobRoot: 7},
	})
	putRetirementBlob(t, retirementImagePage(data, 5), []uint32{10})
	putRetirementBlob(t, retirementImagePage(data, 6), []uint32{11})
	putRetirementBlob(t, retirementImagePage(data, 7), []uint32{12})
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	selection := mustRetirementSelection(t, &tree, 8, 10, 10)
	if _, err := tree.verifySelection(selection, make([]retirementBatch, 3)); err != nil {
		t.Fatal(err)
	}

	putRetirementBranchPage(t, retirementImagePage(data, 2), 1, []retirementBranchEntry{
		{maxRetiredByTxn: 3, childPage: 3},
		{maxRetiredByTxn: 6, childPage: 4},
	})
	tree = mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	_, _, err := tree.selectOldestEligible(8, 10, 10)
	readErr := requireRetirementReadCode(t, err, retirementReadErrChildMaximumMismatch)
	if readErr.expected != 3 || readErr.actual != 4 {
		t.Fatalf("maximum mismatch = %d/%d, want 3/4", readErr.expected, readErr.actual)
	}

	putRetirementBranchPage(t, retirementImagePage(data, 2), 2, []retirementBranchEntry{{maxRetiredByTxn: 6, childPage: 3}})
	tree = mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	_, _, err = tree.selectOldestEligible(8, 10, 10)
	requireRetirementReadCode(t, err, retirementReadErrChildType)

	putRetirementBranchPage(t, retirementImagePage(data, 2), 1, []retirementBranchEntry{
		{maxRetiredByTxn: 4, childPage: 3},
		{maxRetiredByTxn: 6, childPage: 4},
	})
	putRetirementLeafPage(t, retirementImagePage(data, 4), []retirementBatch{
		{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 7},
		{retiredByTxn: 6, pageCount: 1, pageListBlobRoot: 8},
	})
	putRetirementBlob(t, retirementImagePage(data, 8), []uint32{13})
	tree = mustRetirementTree(t, data, testRetirementIdentity(20, 2, 4))
	_, _, err = tree.selectOldestEligible(8, 10, 10)
	requireRetirementReadCode(t, err, retirementReadErrKeysNotStrict)

	deep := retirementImage(36)
	for level := MaxTreeLevel; level >= 1; level-- {
		page := int(MaxTreeLevel-level) + 2
		putRetirementBranchPage(t, retirementImagePage(deep, page), level, []retirementBranchEntry{{
			maxRetiredByTxn: 2,
			childPage:       uint32(page + 1),
		}})
	}
	putRetirementLeafPage(t, retirementImagePage(deep, 33), []retirementBatch{{
		retiredByTxn:     2,
		pageCount:        1,
		pageListBlobRoot: 34,
	}})
	putRetirementBlob(t, retirementImagePage(deep, 34), []uint32{35})
	deepIdentity := testRetirementIdentity(36, 2, 1)
	deepIdentity.txnID = 3
	deepTree := mustRetirementTree(t, deep, deepIdentity)
	deepSelection := mustRetirementSelection(t, &deepTree, 3, 1, 1)
	if _, err := deepTree.verifySelection(deepSelection, make([]retirementBatch, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementCursorReportsDeclaredBatchCountMismatchAtomically(t *testing.T) {
	data := retirementImage(6)
	putRetirementLeafPage(t, retirementImagePage(data, 2), []retirementBatch{{
		retiredByTxn:     2,
		pageCount:        1,
		pageListBlobRoot: 3,
	}})
	putRetirementBlob(t, retirementImagePage(data, 3), []uint32{4})
	tree := mustRetirementTree(t, data, testRetirementIdentity(6, 2, 2))
	cursor := tree.cursor(retirementPageCheckOrdinary)
	_, _, err := cursor.nextBatch()
	readErr := requireRetirementReadCode(t, err, retirementReadErrBatchCountMismatch)
	if readErr.expected != 2 || readErr.actual != 1 {
		t.Fatalf("batch count mismatch = %d/%d, want 2/1", readErr.expected, readErr.actual)
	}
	_, _, err = cursor.nextBatch()
	requireRetirementReadCode(t, err, retirementReadErrCursorFailed)
}

func TestRetirementTraversalUsesFixedMemoryAndCallerScratch(t *testing.T) {
	data := sampleRetirementImage(t)
	tree := mustRetirementTree(t, data, testRetirementIdentity(20, 2, 3))
	scratch := make([]retirementBatch, 2)
	workspace := retirementReadWorkspace[immutableSlicePageSource]{}
	allocations := testing.AllocsPerRun(100, func() {
		selection, ok, status := tree.selectOldestEligibleWithWorkspace(4, 2, 3, &workspace)
		if status.failed() || !ok {
			panic("selection failed")
		}
		verified, status := tree.verifySelectionWithWorkspace(selection, scratch, &workspace)
		if status.failed() {
			panic("verification failed")
		}
		result, passStatus := verified.secondPassWithWorkspace(
			&tree,
			&workspace,
			func(retirementBatch, uint32) retirementSinkStatus { return retirementSinkStatus{} },
		)
		if passStatus.failed() || result.pageCount != 3 {
			panic("second pass failed")
		}
	})
	if allocations != 0 {
		t.Fatalf("retirement traversal allocations = %v, want 0", allocations)
	}
}

func TestRetirementReaderPreservesPositionalFailuresTornPagesAndCachedAccess(t *testing.T) {
	data := sampleRetirementImage(t)
	identity := testRetirementIdentity(20, 2, 3)
	forkEvidence := &pageSourceError{code: pageSourceErrForkedHandle}
	forked := &controlledPageSource{access: forkEvidence}
	tree, err := newRetirementTreeFromSource(forked, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tree.selectOldestEligible(4, 2, 3)
	readErr := requireRetirementReadCode(t, err, retirementReadErrSource)
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
	tree, err = newRetirementTreeFromSource(failing, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tree.selectOldestEligible(4, 2, 3)
	readErr = requireRetirementReadCode(t, err, retirementReadErrSource)
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != ioEvidence.status() {
		t.Fatalf("I/O evidence changed: %+v", readErr.cause)
	}

	short := newImmutableSlicePageSource(data[:2*PageSize+17], 20)
	shortTree, err := newRetirementTreeFromSource(short, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = shortTree.selectOldestEligible(4, 2, 3)
	readErr = requireRetirementReadCode(t, err, retirementReadErrSource)
	if !errors.As(readErr, &sourceErr) || sourceErr.code != pageSourceErrShortRead ||
		sourceErr.offset != 2*PageSize || sourceErr.expected != PageSize || sourceErr.actual != 17 {
		t.Fatalf("short evidence = %+v", sourceErr)
	}

	torn := &controlledPageSource{
		base: newImmutableSlicePageSource(data, 20),
		mutate: func(pageNumber uint32, page *[PageSize]byte) {
			if pageNumber == 2 {
				rootAt := int(PageHeaderSize) + 24
				binary.LittleEndian.PutUint32(page[rootAt:rootAt+4], ^uint32(0))
			}
		},
	}
	tree, err = newRetirementTreeFromSource(torn, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tree.selectOldestEligible(4, 2, 3)
	readErr = requireRetirementReadCode(t, err, retirementReadErrPage)
	requireRetirementPageCode(t, readErr, retirementPageErrBlobRootOutOfBounds)

	controlled := &controlledPageSource{base: newImmutableSlicePageSource(data, 20)}
	tree, err = newRetirementTreeFromSource(controlled, identity)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor(retirementPageCheckOrdinary)
	if _, ok, err := cursor.nextBatch(); err != nil || !ok {
		t.Fatalf("first controlled batch = %t/%v", ok, err)
	}
	reads := controlled.reads
	controlled.access = forkEvidence
	_, ok, err := cursor.nextBatch()
	if ok {
		t.Fatal("forked cached cursor yielded a batch")
	}
	readErr = requireRetirementReadCode(t, err, retirementReadErrSource)
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != forkEvidence.status() || controlled.reads != reads {
		t.Fatalf("cached access evidence/reads = %v/%d->%d", readErr.cause, reads, controlled.reads)
	}
}
