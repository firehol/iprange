package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func blobImage(pages int, fill func([]byte)) []byte {
	data := make([]byte, pages*PageSize)
	fill(data)
	return data
}

func blobImagePage(data []byte, page int) []byte {
	return data[page*PageSize : (page+1)*PageSize]
}

func mustBlobTree(
	t *testing.T,
	data []byte,
	root uint32,
	kind blobKind,
	length uint64,
) blobTree[immutableSlicePageSource] {
	t.Helper()
	tree, err := newBlobTree(data, 1, uint64(len(data)/PageSize), root, kind, length)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func requireBlobReadCode(t *testing.T, err error, want blobReadErrorCode) *blobReadError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected blob-read error %d", want)
	}
	var got *blobReadError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *blobReadError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("blob-read code = %d, want %d", got.code, want)
	}
	return got
}

func TestBlobOwnerContractRejectsImpossibleInputs(t *testing.T) {
	data := blobImage(3, func([]byte) {})
	_, err := newBlobTree(data, 1, 3, 2, blobKindMembershipBitmap, 0)
	requireBlobReadCode(t, err, blobReadErrOwnerLengthZero)

	_, err = newBlobTree(data, 1, 3, 2, blobKindMembershipBitmap, 4)
	readErr := requireBlobReadCode(t, err, blobReadErrOwnerLengthAlignment)
	if readErr.length != 4 || readErr.alignment != 8 {
		t.Fatalf("length/alignment = %d/%d, want 4/8", readErr.length, readErr.alignment)
	}

	_, err = newBlobTree(data, 1, 3, 0, blobKindRetirementPageList, 4)
	requireBlobReadCode(t, err, blobReadErrPageOutOfBounds)

	_, err = newBlobTree(data, 1, 4, 2, blobKindRetirementPageList, 4)
	requireBlobReadCode(t, err, blobReadErrPageOutOfBounds)

	_, err = newBlobTree(data, 1, 3, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	requireBlobReadCode(t, err, blobReadErrOwnerLengthTooLarge)
}

func TestBlobSingleLeafStreamLookupAndCRCMode(t *testing.T) {
	var page [PageSize]byte
	data := blobImage(3, func(data []byte) {
		putBlobLeafPage(
			t,
			blobImagePage(data, 2),
			blobKindMembershipBitmap,
			0,
			[]byte{0x5a, 0x5a, 0x5a, 0x5a, 0x5a, 0x5a, 0x5a, 0x5a},
		)
	})
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader := tree.stream(blobPageCheckOrdinary)
	chunk, ok, err := reader.nextChunk()
	if err != nil || !ok || chunk.logicalOffset != 0 || len(chunk.data) != 8 || chunk.data[0] != 0x5a {
		t.Fatalf("first chunk = %+v/%t/%v", chunk, ok, err)
	}
	if _, ok, err := reader.nextChunk(); err != nil || ok {
		t.Fatalf("stream end = %t/%v, want false/nil", ok, err)
	}
	for _, offset := range []uint64{0, 7} {
		chunk, err := tree.chunkAt(offset, blobPageCheckOrdinary, &page)
		if err != nil || chunk.logicalOffset != 0 || len(chunk.data) != 8 {
			t.Fatalf("chunkAt(%d) = %+v/%v", offset, chunk, err)
		}
	}
	_, err = tree.chunkAt(8, blobPageCheckOrdinary, &page)
	requireBlobReadCode(t, err, blobReadErrRequestOutsideLength)

	data[2*PageSize+PageCRCOffset] ^= 1
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader = tree.stream(blobPageCheckOrdinary)
	if _, ok, err := reader.nextChunk(); err != nil || !ok {
		t.Fatalf("ordinary stream checked CRC: %t/%v", ok, err)
	}
	reader = tree.stream(blobPageCheckVerified)
	readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrPage)
	requireBlobPageCode(t, readErr, blobPageErrChecksum)
}

func TestBlobOrdinaryLookupDoesNotScanUnselectedEntriesOrTails(t *testing.T) {
	var page [PageSize]byte
	data := blobImage(6, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 1, []blobBranchEntry{
			{logicalOffset: 0, childPage: 3},
			{logicalOffset: blobLeafCapacity, childPage: 4},
			{logicalOffset: 2 * blobLeafCapacity, childPage: 5},
		})
		putBlobLeafPage(t, blobImagePage(data, 3), blobKindMembershipBitmap, 0, make([]byte, blobLeafCapacity))
		putBlobLeafPage(t, blobImagePage(data, 4), blobKindMembershipBitmap, blobLeafCapacity, make([]byte, blobLeafCapacity))
		putBlobLeafPage(t, blobImagePage(data, 5), blobKindMembershipBitmap, 2*blobLeafCapacity, make([]byte, 8))
	})

	branchPage := blobImagePage(data, 2)
	binary.LittleEndian.PutUint32(branchPage[72:76], 6) // Unselected third child.
	if _, err := WritePageCRC32C(branchPage); err != nil {
		t.Fatal(err)
	}
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, 2*blobLeafCapacity+8)
	if _, err := tree.chunkAt(0, blobPageCheckOrdinary, &page); err != nil {
		t.Fatalf("ordinary lookup scanned unselected child: %v", err)
	}
	_, err := tree.chunkAt(0, blobPageCheckVerified, &page)
	readErr := requireBlobReadCode(t, err, blobReadErrPage)
	requireBlobPageCode(t, readErr, blobPageErrChildOutOfBounds)

	binary.LittleEndian.PutUint32(branchPage[72:76], 5)
	branchPage[80] = 1 // Branch reserved tail begins at byte 80.
	if _, err := WritePageCRC32C(branchPage); err != nil {
		t.Fatal(err)
	}
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 2*blobLeafCapacity+8)
	if _, err := tree.chunkAt(0, blobPageCheckOrdinary, &page); err != nil {
		t.Fatalf("ordinary lookup scanned branch tail: %v", err)
	}
	_, err = tree.chunkAt(0, blobPageCheckVerified, &page)
	readErr = requireBlobReadCode(t, err, blobReadErrPage)
	requireBlobPageCode(t, readErr, blobPageErrReservedNonzero)

	branchPage[80] = 0
	if _, err := WritePageCRC32C(branchPage); err != nil {
		t.Fatal(err)
	}
	leafPage := blobImagePage(data, 5)
	leafPage[PageSize-1] = 1
	if _, err := WritePageCRC32C(leafPage); err != nil {
		t.Fatal(err)
	}
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 2*blobLeafCapacity+8)
	if _, err := tree.chunkAt(2*blobLeafCapacity, blobPageCheckOrdinary, &page); err != nil {
		t.Fatalf("ordinary lookup scanned leaf tail: %v", err)
	}
	_, err = tree.chunkAt(2*blobLeafCapacity, blobPageCheckVerified, &page)
	readErr = requireBlobReadCode(t, err, blobReadErrPage)
	requireBlobPageCode(t, readErr, blobPageErrReservedNonzero)
}

func TestBlobBranchStreamAndLookupUseCallerOwnedBuffers(t *testing.T) {
	var page [PageSize]byte
	data := twoBlobLeafImage(t, blobLeafCapacity, blobLeafCapacity, 8)
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	reader := tree.stream(blobPageCheckOrdinary)
	first, ok, err := reader.nextChunk()
	if err != nil || !ok || first.logicalOffset != 0 || len(first.data) != blobLeafCapacity {
		t.Fatalf("first chunk = offset %d len %d/%t/%v", first.logicalOffset, len(first.data), ok, err)
	}
	firstBuffer := &first.data[0]
	first.data[0] = 0x7f
	if blobImagePage(data, 3)[blobLeafDataOffset] == 0x7f {
		t.Fatal("chunk data aliases immutable source")
	}
	final, ok, err := reader.nextChunk()
	if err != nil || !ok || final.logicalOffset != blobLeafCapacity || len(final.data) != 8 {
		t.Fatalf("final chunk = offset %d len %d/%t/%v", final.logicalOffset, len(final.data), ok, err)
	}
	if firstBuffer != &final.data[0] {
		t.Fatal("blob cursor did not reuse its single page buffer")
	}
	if _, ok, err := reader.nextChunk(); err != nil || ok {
		t.Fatalf("stream end = %t/%v", ok, err)
	}
	chunk, err := tree.chunkAt(blobLeafCapacity+1, blobPageCheckOrdinary, &page)
	if err != nil || chunk.logicalOffset != blobLeafCapacity {
		t.Fatalf("lookup second leaf = %+v/%v", chunk, err)
	}
}

func TestRetirementBlobStrictOrderBoundsAndTerminalFailure(t *testing.T) {
	data := blobImage(1016, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindRetirementPageList, 1, []blobBranchEntry{
			{logicalOffset: 0, childPage: 3},
			{logicalOffset: blobLeafCapacity, childPage: 4},
		})
		firstData := make([]byte, blobLeafCapacity)
		for index := 0; index < blobLeafCapacity/4; index++ {
			binary.LittleEndian.PutUint32(firstData[index*4:index*4+4], uint32(index+2))
		}
		putBlobLeafPage(t, blobImagePage(data, 3), blobKindRetirementPageList, 0, firstData)
		finalData := make([]byte, 8)
		binary.LittleEndian.PutUint32(finalData[0:4], 1014)
		binary.LittleEndian.PutUint32(finalData[4:8], 1015)
		putBlobLeafPage(t, blobImagePage(data, 4), blobKindRetirementPageList, blobLeafCapacity, finalData)
	})
	tree := mustBlobTree(t, data, 2, blobKindRetirementPageList, blobLeafCapacity+8)
	reader := tree.stream(blobPageCheckVerified)
	if chunk, ok, err := reader.nextChunk(); err != nil || !ok || len(chunk.data) != blobLeafCapacity {
		t.Fatalf("first retirement chunk = len %d/%t/%v", len(chunk.data), ok, err)
	}
	if chunk, ok, err := reader.nextChunk(); err != nil || !ok || len(chunk.data) != 8 {
		t.Fatalf("final retirement chunk = len %d/%t/%v", len(chunk.data), ok, err)
	}
	if _, ok, err := reader.nextChunk(); err != nil || ok {
		t.Fatalf("retirement end = %t/%v", ok, err)
	}

	finalPage := blobImagePage(data, 4)
	binary.LittleEndian.PutUint32(finalPage[blobLeafDataOffset:blobLeafDataOffset+4], 1013)
	if _, err := WritePageCRC32C(finalPage); err != nil {
		t.Fatal(err)
	}
	tree = mustBlobTree(t, data, 2, blobKindRetirementPageList, blobLeafCapacity+8)
	reader = tree.stream(blobPageCheckVerified)
	if _, ok, err := reader.nextChunk(); err != nil || !ok {
		t.Fatalf("first retirement chunk before duplicate = %t/%v", ok, err)
	}
	readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrRetirementPageOrder)
	if readErr.previousPage != 1013 || readErr.currentPage != 1013 {
		t.Fatalf("duplicate pair = %d/%d", readErr.previousPage, readErr.currentPage)
	}
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrCursorFailed)
}

func TestRetirementBlobRejectsReservedAndUncommittedPages(t *testing.T) {
	for _, value := range []uint32{0, 1, 5} {
		data := blobImage(5, func(data []byte) {
			payload := make([]byte, 4)
			binary.LittleEndian.PutUint32(payload, value)
			putBlobLeafPage(t, blobImagePage(data, 2), blobKindRetirementPageList, 0, payload)
		})
		tree := mustBlobTree(t, data, 2, blobKindRetirementPageList, 4)
		reader := tree.stream(blobPageCheckOrdinary)
		readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrRetirementPageOutOfBounds)
		if readErr.currentPage != value {
			t.Fatalf("bad retirement page = %d, want %d", readErr.currentPage, value)
		}
		requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrCursorFailed)
	}
}

func TestBlobStreamRejectsGapOverlapAndLengthContradictions(t *testing.T) {
	for _, secondOffset := range []uint64{blobLeafCapacity + 8, blobLeafCapacity - 8} {
		data := twoBlobLeafImage(t, secondOffset, blobLeafCapacity, 8)
		tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, blobLeafCapacity+16)
		reader := tree.stream(blobPageCheckOrdinary)
		if _, ok, err := reader.nextChunk(); err != nil || !ok {
			t.Fatalf("first chunk before offset mismatch = %t/%v", ok, err)
		}
		readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrOffsetMismatch)
		if readErr.expected != blobLeafCapacity || readErr.actual != secondOffset {
			t.Fatalf("offset mismatch = %d/%d", readErr.expected, readErr.actual)
		}
		requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrCursorFailed)
	}

	data := twoBlobLeafImage(t, blobLeafCapacity, blobLeafCapacity, 8)
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, blobLeafCapacity)
	reader := tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrTrailingData)

	data = blobImage(4, func(data []byte) {
		putBlobLeafPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 0, make([]byte, blobLeafCapacity))
	})
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	reader = tree.stream(blobPageCheckOrdinary)
	readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrLengthShort)
	if readErr.expected != blobLeafCapacity+8 || readErr.actual != blobLeafCapacity {
		t.Fatalf("short length = %d/%d", readErr.expected, readErr.actual)
	}

	data = twoBlobLeafImage(t, 8, 8, 8)
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 16)
	reader = tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrNonfinalLeafLength)

	data = blobImage(3, func(data []byte) {
		putBlobLeafPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 0, make([]byte, 16))
	})
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader = tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrLengthExceeded)
}

func TestBlobRootChildOffsetsTypesAndLevelsAreChecked(t *testing.T) {
	data := blobImage(3, func(data []byte) {
		putBlobHeader(t, blobImagePage(data, 2), PageTypeMetadataChunk, blobKindMembershipBitmap, 1, 0, PageHeaderSize)
	})
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader := tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrRootType)

	data = blobImage(4, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 1, []blobBranchEntry{{logicalOffset: 8, childPage: 3}})
		putBlobLeafPage(t, blobImagePage(data, 3), blobKindMembershipBitmap, 8, make([]byte, 8))
	})
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader = tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrOffsetMismatch)

	data = blobImage(5, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 2, []blobBranchEntry{{childPage: 3}})
		putBlobBranchPage(t, blobImagePage(data, 3), blobKindMembershipBitmap, 2, []blobBranchEntry{{childPage: 4}})
		putBlobLeafPage(t, blobImagePage(data, 4), blobKindMembershipBitmap, 0, make([]byte, 8))
	})
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader = tree.stream(blobPageCheckOrdinary)
	readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrChildLevel)
	if readErr.expectedLevel != 1 || readErr.actualLevel != 2 {
		t.Fatalf("child levels = %d/%d", readErr.expectedLevel, readErr.actualLevel)
	}

	data = blobImage(4, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 1, []blobBranchEntry{{childPage: 2}})
	})
	tree = mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader = tree.stream(blobPageCheckOrdinary)
	requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrChildType)
}

func TestBlobMaximumDepthAndOrdinaryStreamUseFixedMemory(t *testing.T) {
	pages := int(MaxTreeLevel) + 3
	data := blobImage(pages, func(data []byte) {
		for level := MaxTreeLevel; level >= 1; level-- {
			page := int(MaxTreeLevel-level) + 2
			putBlobBranchPage(
				t,
				blobImagePage(data, page),
				blobKindMembershipBitmap,
				level,
				[]blobBranchEntry{{childPage: uint32(page + 1)}},
			)
		}
		putBlobLeafPage(
			t,
			blobImagePage(data, int(MaxTreeLevel)+2),
			blobKindMembershipBitmap,
			0,
			make([]byte, 8),
		)
	})
	tree := mustBlobTree(t, data, 2, blobKindMembershipBitmap, 8)
	reader := tree.stream(blobPageCheckVerified)
	if chunk, ok, err := reader.nextChunk(); err != nil || !ok || len(chunk.data) != 8 {
		t.Fatalf("maximum depth chunk = len %d/%t/%v", len(chunk.data), ok, err)
	}
	if _, ok, err := reader.nextChunk(); err != nil || ok {
		t.Fatalf("maximum depth end = %t/%v", ok, err)
	}

	workspace := blobReadWorkspace[immutableSlicePageSource]{}
	allocations := testing.AllocsPerRun(100, func() {
		reader := tree.streamWithWorkspace(blobPageCheckOrdinary, &workspace)
		chunk, ok, err := reader.nextChunk()
		if err != nil || !ok || len(chunk.data) != 8 {
			panic("unexpected blob stream result")
		}
	})
	if allocations != 0 {
		t.Fatalf("ordinary blob stream allocations = %v, want 0", allocations)
	}
}

func TestBlobReaderPreservesPositionalFailuresTornPagesAndCachedAccess(t *testing.T) {
	data := twoBlobLeafImage(t, blobLeafCapacity, blobLeafCapacity, 8)
	ioEvidence := &pageSourceError{
		code: pageSourceErrIO,
		evidence: pageIOEvidence{
			kind:         pageIOPermissionDenied,
			rawOSCode:    13,
			hasRawOSCode: true,
		},
	}
	failing := &controlledPageSource{readError: ioEvidence}
	tree, err := newBlobTreeFromSource(failing, 1, 5, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	if err != nil {
		t.Fatal(err)
	}
	reader := tree.stream(blobPageCheckOrdinary)
	readErr := requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrSource)
	var sourceErr *pageSourceError
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != ioEvidence.status() {
		t.Fatalf("I/O evidence changed: %+v", readErr.cause)
	}

	short := newImmutableSlicePageSource(data[:2*PageSize+17], 5)
	shortTree, err := newBlobTreeFromSource(short, 1, 5, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	if err != nil {
		t.Fatal(err)
	}
	shortReader := shortTree.stream(blobPageCheckOrdinary)
	readErr = requireBlobReadCode(t, nextBlobChunkError(&shortReader), blobReadErrSource)
	if !errors.As(readErr, &sourceErr) || sourceErr.code != pageSourceErrShortRead ||
		sourceErr.offset != 2*PageSize || sourceErr.expected != PageSize || sourceErr.actual != 17 {
		t.Fatalf("short evidence = %+v", sourceErr)
	}

	torn := &controlledPageSource{
		base: newImmutableSlicePageSource(data, 5),
		mutate: func(pageNumber uint32, page *[PageSize]byte) {
			if pageNumber == 2 {
				binary.LittleEndian.PutUint32(page[PageHeaderSize+8:PageHeaderSize+12], ^uint32(0))
			}
		},
	}
	tree, err = newBlobTreeFromSource(torn, 1, 5, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	if err != nil {
		t.Fatal(err)
	}
	reader = tree.stream(blobPageCheckOrdinary)
	readErr = requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrPage)
	requireBlobPageCode(t, readErr, blobPageErrChildOutOfBounds)

	controlled := &controlledPageSource{base: newImmutableSlicePageSource(data, 5)}
	tree, err = newBlobTreeFromSource(controlled, 1, 5, 2, blobKindMembershipBitmap, blobLeafCapacity+8)
	if err != nil {
		t.Fatal(err)
	}
	reader = tree.stream(blobPageCheckOrdinary)
	if _, ok, err := reader.nextChunk(); err != nil || !ok {
		t.Fatalf("first controlled chunk = %t/%v", ok, err)
	}
	reads := controlled.reads
	forkEvidence := &pageSourceError{code: pageSourceErrForkedHandle}
	controlled.access = forkEvidence
	readErr = requireBlobReadCode(t, nextBlobChunkError(&reader), blobReadErrSource)
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != forkEvidence.status() || controlled.reads != reads {
		t.Fatalf("cached access evidence/reads = %v/%d->%d", readErr.cause, reads, controlled.reads)
	}
}

func TestBlobRetirementPageCursorChecksAccessBeforeCachedValue(t *testing.T) {
	data := blobImage(5, func(data []byte) {
		pageList := make([]byte, 8)
		binary.LittleEndian.PutUint32(pageList[0:4], 3)
		binary.LittleEndian.PutUint32(pageList[4:8], 4)
		putBlobLeafPage(
			t,
			blobImagePage(data, 2),
			blobKindRetirementPageList,
			0,
			pageList,
		)
	})
	source := &controlledPageSource{base: newImmutableSlicePageSource(data, 5)}
	tree, err := newBlobTreeFromSource(
		source,
		1,
		5,
		2,
		blobKindRetirementPageList,
		8,
	)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := tree.retirementPages(blobPageCheckOrdinary)
	if err != nil {
		t.Fatal(err)
	}
	page, ok, err := reader.nextPage()
	if err != nil || !ok || page != 3 {
		t.Fatalf("first page = %d/%t/%v", page, ok, err)
	}
	reads := source.reads
	forkEvidence := &pageSourceError{code: pageSourceErrForkedHandle}
	source.access = forkEvidence
	_, ok, err = reader.nextPage()
	if ok {
		t.Fatal("forked cursor yielded a cached page number")
	}
	readErr := requireBlobReadCode(t, err, blobReadErrSource)
	var sourceErr *pageSourceError
	if !errors.As(readErr.cause, &sourceErr) || sourceErr.status() != forkEvidence.status() || source.reads != reads {
		t.Fatalf("cached access evidence/reads = %v/%d->%d", readErr.cause, reads, source.reads)
	}
}

func twoBlobLeafImage(t *testing.T, secondOffset uint64, firstLength, secondLength int) []byte {
	t.Helper()
	return blobImage(5, func(data []byte) {
		putBlobBranchPage(t, blobImagePage(data, 2), blobKindMembershipBitmap, 1, []blobBranchEntry{
			{logicalOffset: 0, childPage: 3},
			{logicalOffset: secondOffset, childPage: 4},
		})
		putBlobLeafPage(t, blobImagePage(data, 3), blobKindMembershipBitmap, 0, make([]byte, firstLength))
		putBlobLeafPage(t, blobImagePage(data, 4), blobKindMembershipBitmap, secondOffset, make([]byte, secondLength))
	})
}

func nextBlobChunkError[S committedPageSource](reader *blobReader[S]) error {
	_, _, err := reader.nextChunk()
	return err
}
