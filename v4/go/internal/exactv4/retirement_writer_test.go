package exactv4

import (
	"encoding/binary"
	"math/bits"
	"testing"
)

type retirementWriterTestSource struct {
	data        []byte
	pageCount   uint64
	accessCalls int
	failAccess  int
}

type retirementPoolSnapshot struct {
	slots                                        []privatePagePoolSlot
	checkpointSequence, activeCheckpointID       uint64
	generation, mutationEpoch                    uint64
	allocationCursor                             int
	tokenEpoch, activeTokenEpoch, activeTokenGen uint64
}

func snapshotRetirementPool(arena *privatePageArena) retirementPoolSnapshot {
	return retirementPoolSnapshot{
		slots:              append([]privatePagePoolSlot(nil), arena.pool.slots...),
		checkpointSequence: arena.pool.checkpointSequence,
		activeCheckpointID: arena.pool.activeCheckpointID,
		generation:         arena.pool.generation,
		mutationEpoch:      arena.pool.mutationEpoch,
		allocationCursor:   arena.allocationCursor,
		tokenEpoch:         arena.tokenEpoch,
		activeTokenEpoch:   arena.activeTokenEpoch,
		activeTokenGen:     arena.activeTokenGen,
	}
}

func requireRetirementPoolSnapshot(t *testing.T, arena *privatePageArena, want retirementPoolSnapshot) {
	t.Helper()
	if arena.pool.checkpointSequence != want.checkpointSequence ||
		arena.pool.activeCheckpointID != want.activeCheckpointID || arena.pool.generation != want.generation ||
		arena.pool.mutationEpoch != want.mutationEpoch || arena.allocationCursor != want.allocationCursor ||
		arena.tokenEpoch != want.tokenEpoch || arena.activeTokenEpoch != want.activeTokenEpoch ||
		arena.activeTokenGen != want.activeTokenGen {
		t.Fatal("retirement pool scalar/checkpoint state changed")
	}
	for index := range want.slots {
		if arena.pool.slots[index] != want.slots[index] {
			t.Fatalf("retirement pool slot %d changed", index)
		}
	}
}

func pageRoleLookupWork(index *pageRoleIndex, pageNumber uint32) (bool, int) {
	work := 0
	for slot := index.root; slot != pageRoleNoIndex; {
		work++
		switch {
		case pageNumber < index.slots[slot].pageNumber:
			slot = index.slots[slot].left
		case pageNumber > index.slots[slot].pageNumber:
			slot = index.slots[slot].right
		default:
			return true, work
		}
	}
	return false, work
}

func verifyPageRoleAVL(
	t *testing.T,
	index *pageRoleIndex,
	root int,
) (minimum, maximum uint32, height uint8, visited int, present bool) {
	t.Helper()
	if root == pageRoleNoIndex {
		return 0, 0, 0, 0, false
	}
	leftMin, leftMax, leftHeight, leftVisited, hasLeft := verifyPageRoleAVL(t, index, index.slots[root].left)
	rightMin, rightMax, rightHeight, rightVisited, hasRight := verifyPageRoleAVL(t, index, index.slots[root].right)
	if hasLeft && leftMax >= index.slots[root].pageNumber {
		t.Fatalf("left maximum %d >= root %d", leftMax, index.slots[root].pageNumber)
	}
	if hasRight && rightMin <= index.slots[root].pageNumber {
		t.Fatalf("right minimum %d <= root %d", rightMin, index.slots[root].pageNumber)
	}
	if leftHeight > rightHeight+1 || rightHeight > leftHeight+1 {
		t.Fatalf("AVL imbalance at %d: %d/%d", index.slots[root].pageNumber, leftHeight, rightHeight)
	}
	height = leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	if index.slots[root].height != height {
		t.Fatalf("stored height at %d = %d, want %d", index.slots[root].pageNumber, index.slots[root].height, height)
	}
	minimum, maximum = index.slots[root].pageNumber, index.slots[root].pageNumber
	if hasLeft {
		minimum = leftMin
	}
	if hasRight {
		maximum = rightMax
	}
	return minimum, maximum, height, leftVisited + rightVisited + 1, true
}

func TestPageRoleIndexAdversarialCollisionsRemainLogarithmicAndAllocationFree(t *testing.T) {
	sizes := []int{512, 4096, 8192}
	workBySize := make([]int, len(sizes))
	for sizeIndex, size := range sizes {
		keys := make([]uint32, size)
		for index := range keys {
			// Every key had the same starting bucket in the former modulo hash.
			keys[index] = uint32(2 + index*size)
		}
		roles := newPageRoleIndex(make([]pageRoleIndexSlot, size))
		var problem retirementWriteError
		allocations := testing.AllocsPerRun(10, func() {
			roles.clear()
			for _, pageNumber := range keys {
				problem = roles.insertExclusive(pageNumber, rolePrivate, pageRolePrivateAuthorization)
				if problem.failed() {
					return
				}
			}
		})
		if problem.failed() {
			t.Fatalf("size %d insertion failed: %v", size, problem)
		}
		if allocations != 0 {
			t.Fatalf("size %d allocations = %v, want 0", size, allocations)
		}
		_, _, height, visited, present := verifyPageRoleAVL(t, &roles, roles.root)
		if !present || visited != size {
			t.Fatalf("size %d visited = %d/present=%v", size, visited, present)
		}
		maxWork := 0
		for _, pageNumber := range keys {
			found, work := pageRoleLookupWork(&roles, pageNumber)
			if !found {
				t.Fatalf("size %d missing page %d", size, pageNumber)
			}
			workBySize[sizeIndex] += work
			if work > maxWork {
				maxWork = work
			}
		}
		logBound := 2*bits.Len(uint(size)) + 1
		if int(height) > logBound || maxWork > logBound || workBySize[sizeIndex] > size*logBound {
			t.Fatalf("size %d height/work/total = %d/%d/%d, logarithmic bound %d/%d", size, height, maxWork, workBySize[sizeIndex], logBound, size*logBound)
		}
		t.Logf("size=%d height=%d max_lookup_work=%d total_lookup_work=%d", size, height, maxWork, workBySize[sizeIndex])
	}
	if workBySize[1] >= workBySize[0]*12 {
		t.Fatalf("512->4096 work ratio is quadratic: %d -> %d", workBySize[0], workBySize[1])
	}
	if workBySize[2] >= workBySize[1]*3 {
		t.Fatalf("4096->8192 work ratio is quadratic: %d -> %d", workBySize[1], workBySize[2])
	}
}

func TestRetirementAllocationBatchPreflightIsExactAndReusable(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*privatePageArena)
		repair func(*privatePageArena)
		want   retirementWriteErrorCode
	}{
		{
			name:   "later owner",
			inject: func(arena *privatePageArena) { arena.pool.slots[1].owner = privatePageOwnerBitmap },
			repair: func(arena *privatePageArena) { arena.pool.slots[1].owner = privatePageOwnerNone },
			want:   retirementWriteErrPrivateSlotAlreadyInUse,
		},
		{
			name:   "later slot epoch",
			inject: func(arena *privatePageArena) { arena.pool.slots[1].epoch = ^uint64(0) - 1 },
			repair: func(arena *privatePageArena) { arena.pool.slots[1].epoch = 1 },
			want:   retirementWriteErrArithmeticOverflow,
		},
		{
			name:   "aggregate mutation headroom",
			inject: func(arena *privatePageArena) { arena.pool.mutationEpoch = ^uint64(0) - 5 },
			repair: func(arena *privatePageArena) { arena.pool.mutationEpoch = 0 },
			want:   retirementWriteErrArithmeticOverflow,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			slots := writerSlots(20, 3)
			arena, problem := newPrivatePageArena(slots, 20, 23, 2)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.inject(&arena)
			before := snapshotRetirementPool(&arena)
			var got retirementWriteError
			allocations := testing.AllocsPerRun(100, func() {
				_, got = arena.beginWithAllocationBatch(2)
			})
			if allocations != 0 {
				t.Fatalf("failed allocation preflight allocations = %v, want 0", allocations)
			}
			requireRetirementWriteCode(t, got, test.want)
			requireRetirementPoolSnapshot(t, &arena, before)

			test.repair(&arena)
			checkpoint, got := arena.beginWithAllocationBatch(2)
			if got.failed() {
				t.Fatalf("reused allocation preflight failed: %v", got)
			}
			var page [PageSize]byte
			for index := 0; index < 2; index++ {
				pageNumber := arena.allocatePrepared(checkpoint, privatePageRetirementTree)
				arena.writePage(pageNumber, &page)
			}
			if got = arena.rollback(checkpoint); got.failed() {
				t.Fatalf("reused checkpoint rollback failed: %v", got)
			}
		})
	}
}

func TestRetirementCommitBatchPreflightCleansMarkersAndIsReusable(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*privatePageArena, []uint32) []uint32
		repair func(*privatePageArena)
		want   retirementWriteErrorCode
	}{
		{
			name:   "duplicate release",
			inject: func(_ *privatePageArena, pages []uint32) []uint32 { return []uint32{pages[0], pages[0]} },
			repair: func(_ *privatePageArena) {},
			want:   retirementWriteErrPrivateSlotAlreadyInUse,
		},
		{
			name: "later owner",
			inject: func(arena *privatePageArena, pages []uint32) []uint32 {
				index, _ := arena.pool.slotIndex(pages[1])
				arena.pool.slots[index].owner = privatePageOwnerBitmap
				return pages
			},
			repair: func(arena *privatePageArena) { arena.pool.slots[1].owner = privatePageOwnerRetirement },
			want:   retirementWriteErrPrivateSlotAlreadyInUse,
		},
		{
			name: "later slot epoch",
			inject: func(arena *privatePageArena, pages []uint32) []uint32 {
				index, _ := arena.pool.slotIndex(pages[1])
				arena.pool.slots[index].epoch = ^uint64(0)
				return pages
			},
			repair: func(arena *privatePageArena) { arena.pool.slots[1].epoch = 2 },
			want:   retirementWriteErrArithmeticOverflow,
		},
		{
			name: "aggregate mutation headroom",
			inject: func(arena *privatePageArena, pages []uint32) []uint32 {
				arena.pool.mutationEpoch = ^uint64(0) - 1
				return pages
			},
			repair: func(arena *privatePageArena) { arena.pool.mutationEpoch = 4 },
			want:   retirementWriteErrArithmeticOverflow,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			slots := writerSlots(20, 3)
			arena, problem := newPrivatePageArena(slots, 20, 23, 2)
			if problem.failed() {
				t.Fatal(problem)
			}
			checkpoint, problem := arena.beginWithAllocationBatch(2)
			if problem.failed() {
				t.Fatal(problem)
			}
			pages := make([]uint32, 2)
			var page [PageSize]byte
			for index := range pages {
				pages[index] = arena.allocatePrepared(checkpoint, privatePageRetirementTree)
				arena.writePage(pages[index], &page)
			}
			invalid := test.inject(&arena, pages)
			before := snapshotRetirementPool(&arena)
			var got retirementWriteError
			allocations := testing.AllocsPerRun(100, func() {
				got = arena.preflightCommit(checkpoint, invalid)
			})
			if allocations != 0 {
				t.Fatalf("failed commit preflight allocations = %v, want 0", allocations)
			}
			requireRetirementWriteCode(t, got, test.want)
			requireRetirementPoolSnapshot(t, &arena, before)
			for index := range arena.pool.slots {
				if arena.pool.slots[index].batchMarked {
					t.Fatalf("slot %d retained commit batch marker", index)
				}
			}

			test.repair(&arena)
			if got = arena.preflightCommit(checkpoint, pages); got.failed() {
				t.Fatalf("reused commit preflight failed: %v", got)
			}
			if got = arena.commit(checkpoint, pages); got.failed() {
				t.Fatalf("reused checkpoint commit failed: %v", got)
			}
		})
	}
}

func TestRetirementTokenCleanupPreservesPrimaryAndTypedCleanupFailure(t *testing.T) {
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	pageNumbers := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: pageNumbers})
	if problem.failed() {
		t.Fatal(problem)
	}
	rootIndex, found := arena.pool.slotIndex(token.root)
	if !found {
		t.Fatal("token root missing from pool")
	}
	originalEpoch := arena.pool.slots[rootIndex].epoch
	arena.pool.slots[rootIndex].epoch = ^uint64(0)
	before := snapshotRetirementPool(&arena)
	source := &retirementWriterTestSource{pageCount: 20, failAccess: 1}
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	_, problem = upsertNewestRetirement(
		source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token,
		make([]retirementPathFrame, 2), writerScanScratch(), &replacements, &releases, &roles,
	)
	requireRetirementWriteCode(t, problem, retirementWriteErrSource)
	if problem.cleanupCode != retirementWriteErrArithmeticOverflow || problem.cleanupPage != token.root {
		t.Fatalf("cleanup evidence = code %d/page %d, want arithmetic/page %d", problem.cleanupCode, problem.cleanupPage, token.root)
	}
	requireRetirementPoolSnapshot(t, &arena, before)
	if token.arena != &arena || arena.activeTokenEpoch == 0 {
		t.Fatal("failed typed cleanup invalidated the retryable token")
	}
	arena.pool.slots[rootIndex].epoch = originalEpoch
	if cleanup := token.discard(); cleanup.failed() {
		t.Fatalf("cleanup retry failed: %v", cleanup)
	}
	if arena.inUseCount() != 0 || arena.activeTokenEpoch != 0 {
		t.Fatal("cleanup retry did not release token pages")
	}
}

func TestRetirementBlobTokenEpochOverflowPrecedesAllMutation(t *testing.T) {
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	arena.tokenEpoch = ^uint64(0)
	pages := []uint32{7}
	pageNumbers := []uint32{77, 88}
	pagesBefore := append([]uint32(nil), pages...)
	pageNumbersBefore := append([]uint32(nil), pageNumbers...)
	before := snapshotRetirementPool(&arena)
	var token retirementBlobToken
	allocations := testing.AllocsPerRun(100, func() {
		token, problem = buildRetirementBlob(pages, &arena, &blobBuildScratch{pageNumbers: pageNumbers})
		if problem.code != retirementWriteErrArithmeticOverflow {
			panic("maximal token epoch did not fail before mutation")
		}
	})
	if allocations != 0 {
		t.Fatalf("token epoch overflow allocations = %v, want 0", allocations)
	}
	if token != (retirementBlobToken{}) {
		t.Fatalf("overflow returned token %+v", token)
	}
	requireRetirementPoolSnapshot(t, &arena, before)
	for index := range pages {
		if pages[index] != pagesBefore[index] {
			t.Fatalf("input page %d changed", index)
		}
	}
	for index := range pageNumbers {
		if pageNumbers[index] != pageNumbersBefore[index] {
			t.Fatalf("page-number scratch %d changed", index)
		}
	}

	// Active-token rejection retains its existing precedence over epoch wrap.
	arena.activeTokenEpoch = 1
	_, problem = buildRetirementBlob(pages, &arena, &blobBuildScratch{pageNumbers: pageNumbers})
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobTokenStale)
	arena.activeTokenEpoch = 0

	// MaxUint64 is a valid token epoch; only wrapping to zero is rejected.
	arena.tokenEpoch = ^uint64(0) - 1
	token, problem = buildRetirementBlob(pages, &arena, &blobBuildScratch{pageNumbers: pageNumbers})
	if problem.failed() {
		t.Fatalf("maximal nonzero token epoch build failed: %v", problem)
	}
	if token.epoch != ^uint64(0) || arena.tokenEpoch != ^uint64(0) || arena.activeTokenEpoch != ^uint64(0) {
		t.Fatalf("maximal token epoch = token/arena/active %d/%d/%d", token.epoch, arena.tokenEpoch, arena.activeTokenEpoch)
	}
	if cleanup := token.discard(); cleanup.failed() {
		t.Fatalf("maximal token cleanup failed: %v", cleanup)
	}
	if arena.inUseCount() != 0 || arena.activeTokenEpoch != 0 {
		t.Fatal("maximal token cleanup did not release private pages")
	}
}

func (s *retirementWriterTestSource) checkAccessStatus() pageSourceStatus {
	s.accessCalls++
	if s.failAccess != 0 && s.accessCalls == s.failAccess {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	return pageSourceStatus{}
}

func (s *retirementWriterTestSource) readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	return newImmutableSlicePageSource(s.data, s.pageCount).readPageStatus(pageNumber, destination)
}

func retirementWriterImage(pageCount uint64) []byte { return make([]byte, int(pageCount)*PageSize) }

func retirementWriterPage(image []byte, pageNumber uint32) []byte {
	start := int(pageNumber) * PageSize
	return image[start : start+PageSize]
}

func putWriterBlobLeaf(page []byte, bornTxn, logicalOffset uint64, values []uint32) {
	clear(page)
	dataLength := len(values) * 4
	header := PageHeader{PageType: PageTypeBlobLeaf, BornTxn: bornTxn, ItemCount: 1, Lower: uint16(blobLeafDataOffset + dataLength), Upper: PageSize, Aux: uint32(blobKindRetirementPageList)}
	_ = header.EncodeInto(page)
	binary.LittleEndian.PutUint64(page[32:40], logicalOffset)
	binary.LittleEndian.PutUint16(page[40:42], uint16(dataLength))
	for index, value := range values {
		binary.LittleEndian.PutUint32(page[blobLeafDataOffset+index*4:], value)
	}
	_, _ = WritePageCRC32C(page)
}

func putWriterBlobBranch(page []byte, bornTxn uint64, level uint16, entries []blobBranchEntry) {
	clear(page)
	header := PageHeader{PageType: PageTypeBlobBranch, BornTxn: bornTxn, ItemCount: uint16(len(entries)), Level: level, Lower: uint16(int(PageHeaderSize) + len(entries)*blobBranchEntrySize), Upper: PageSize, Aux: uint32(blobKindRetirementPageList)}
	_ = header.EncodeInto(page)
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*blobBranchEntrySize
		binary.LittleEndian.PutUint64(page[at:at+8], entry.logicalOffset)
		binary.LittleEndian.PutUint32(page[at+8:at+12], entry.childPage)
	}
	_, _ = WritePageCRC32C(page)
}

func putWriterRetirementLeaf(page []byte, bornTxn uint64, batches []retirementBatch) {
	clear(page)
	header := PageHeader{PageType: PageTypeRetirementLeaf, BornTxn: bornTxn, ItemCount: uint16(len(batches)), Lower: uint16(int(PageHeaderSize) + len(batches)*retirementLeafRecordSize), Upper: PageSize}
	_ = header.EncodeInto(page)
	for index, batch := range batches {
		at := int(PageHeaderSize) + index*retirementLeafRecordSize
		binary.LittleEndian.PutUint64(page[at+8:at+16], batch.retiredByTxn)
		binary.LittleEndian.PutUint64(page[at+16:at+24], batch.pageCount)
		binary.LittleEndian.PutUint32(page[at+24:at+28], batch.pageListBlobRoot)
	}
	_, _ = WritePageCRC32C(page)
}

func putWriterRetirementBranch(page []byte, bornTxn uint64, level uint16, entries []retirementBranchEntry) {
	clear(page)
	header := PageHeader{PageType: PageTypeRetirementBranch, BornTxn: bornTxn, ItemCount: uint16(len(entries)), Level: level, Lower: uint16(int(PageHeaderSize) + len(entries)*retirementBranchEntrySize), Upper: PageSize}
	_ = header.EncodeInto(page)
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*retirementBranchEntrySize
		binary.LittleEndian.PutUint64(page[at:at+8], entry.maxRetiredByTxn)
		binary.LittleEndian.PutUint32(page[at+8:at+12], entry.childPage)
	}
	_, _ = WritePageCRC32C(page)
}

func writerSlots(first uint32, count int) []privatePageSlot {
	slots := make([]privatePageSlot, count)
	for index := range slots {
		slots[index] = newPrivatePageSlot(first+uint32(index), privatePageAppended)
	}
	return slots
}

func writerScanScratch() *retirementBlobScanScratch {
	return &retirementBlobScanScratch{pages: make([]retirementBlobScanPage, 4)}
}

func requireRetirementWriteCode(t *testing.T, problem retirementWriteError, code retirementWriteErrorCode) {
	t.Helper()
	if problem.code != code {
		t.Fatalf("writer error code = %d, want %d (%+v)", problem.code, code, problem)
	}
}

func TestRetirementBlobBuilderExactGeometryTokenAndRollback(t *testing.T) {
	values := make([]uint32, retirementValuesPerBlobLeaf+1)
	for index := range values {
		values[index] = uint32(index + 2)
	}
	slots := writerSlots(5000, 3)
	arena, problem := newPrivatePageArena(slots, 5000, 5003, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := make([]uint32, 3)
	token, problem := buildRetirementBlob(values, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	if token.pageCount != uint64(len(values)) || token.byteLength != uint64(len(values))*4 || token.privatePages != 3 || arena.inUseCount() != 3 {
		t.Fatalf("token=%+v in-use=%d", token, arena.inUseCount())
	}
	root := arena.testSlot(token.root)
	branch, status := openBlobBranchStatus(root.bytes[:], 2, blobKindRetirementPageList, arena.pendingPageCount)
	if status.failed() || branch.level != 1 || branch.len() != 2 {
		t.Fatalf("root branch=%+v status=%+v", branch, status)
	}
	first, _ := branch.entryStatus(0)
	second, _ := branch.entryStatus(1)
	if first.logicalOffset != 0 || second.logicalOffset != uint64(retirementValuesPerBlobLeaf*4) {
		t.Fatalf("offsets=%d/%d", first.logicalOffset, second.logicalOffset)
	}
	copyToken := token
	token.discard()
	if arena.inUseCount() != 0 {
		t.Fatalf("discard left %d pages", arena.inUseCount())
	}
	requireRetirementWriteCode(t, copyToken.valid(), retirementWriteErrBlobTokenStale)

	badValues := []uint32{4, 4}
	_, problem = buildRetirementBlob(badValues, &arena, &blobBuildScratch{pageNumbers: order})
	requireRetirementWriteCode(t, problem, retirementWriteErrRetirementStreamOrder)
	if arena.inUseCount() != 0 {
		t.Fatal("failed preflight mutated arena")
	}
}

func TestRetirementTokenBindingMismatchReleasesRealGeneration(t *testing.T) {
	slots := writerSlots(20, 3)
	arena, problem := newPrivatePageArena(slots, 20, 23, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	token.bornTxn = 99
	path := make([]retirementPathFrame, 2)
	replacementStorage := make([]committedPageReplacement, 2)
	replacements := newCommittedReplacementLedger(replacementStorage)
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	source := &retirementWriterTestSource{pageCount: 20}
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobTokenTransactionMismatch)
	if arena.inUseCount() != 0 {
		t.Fatal("transaction mismatch leaked blob generation")
	}

	token, problem = buildRetirementBlob([]uint32{8}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	token.generation = 99
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobTokenGenerationMismatch)
	if arena.inUseCount() != 0 {
		t.Fatal("generation mismatch leaked real blob generation")
	}
}

func TestRetirementEmptyUpsertAndSameTransactionReplacementRecycle(t *testing.T) {
	slots := writerSlots(20, 6)
	arena, problem := newPrivatePageArena(slots, 20, 26, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 4))
	releases := newPrivateReleaseBuffer(make([]uint32, 6))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	source := &retirementWriterTestSource{pageCount: 20}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	first, problem := upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatal(problem)
	}
	if first.batchCount != 1 || first.privatePages != 1 || arena.inUseCount() != 2 || replacements.length != 0 {
		t.Fatalf("first=%+v in-use=%d replacements=%d", first, arena.inUseCount(), replacements.length)
	}
	root := arena.testSlot(first.root)
	leaf, status := openRetirementLeafStatus(root.bytes[:], 2, 26)
	if status.failed() {
		t.Fatal(status)
	}
	batch, status := leaf.batchStatus(0)
	if status.failed() || batch.retiredByTxn != 2 || batch.pageCount != 1 {
		t.Fatalf("batch=%+v status=%+v", batch, status)
	}
	oldRoot, oldBlob := first.root, batch.pageListBlobRoot

	token, problem = buildRetirementBlob([]uint32{7, 8}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	second, problem := upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20, root: first.root, batchCount: 1}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatalf("replacement failed: code=%d page=%d second=%d first64=%d second64=%d roles=%d/%d", problem.code, problem.page, problem.secondPage, problem.first64, problem.second64, problem.existingRole, problem.requestedRole)
	}
	if second.batchCount != 1 || arena.inUseCount() != 2 || arena.testSlot(oldRoot).inUse || arena.testSlot(oldBlob).inUse {
		t.Fatalf("second=%+v old tree/blob live=%v/%v count=%d", second, arena.testSlot(oldRoot).inUse, arena.testSlot(oldBlob).inUse, arena.inUseCount())
	}
}

func TestRetirementFixedPointOmissionIsAtomic(t *testing.T) {
	image := retirementWriterImage(20)
	putWriterBlobLeaf(retirementWriterPage(image, 3), 1, 0, []uint32{10})
	putWriterRetirementLeaf(retirementWriterPage(image, 2), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3}})
	slots := writerSlots(20, 3)
	arena, problem := newPrivatePageArena(slots, 20, 23, 3)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{11}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 4))
	releases := newPrivateReleaseBuffer(make([]uint32, 3))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
	source := &retirementWriterTestSource{data: image, pageCount: 20}
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 2, pageCount: 20, root: 2, batchCount: 1}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrRetirementListOmission)
	if problem.page != 2 || arena.inUseCount() != 0 || replacements.length != 0 || releases.length != 0 {
		t.Fatalf("problem=%+v in-use=%d replacements=%d releases=%d", problem, arena.inUseCount(), replacements.length, releases.length)
	}
}

func TestRetirementDeleteOldestPrefixLocalCOW(t *testing.T) {
	image := retirementWriterImage(20)
	putWriterBlobLeaf(retirementWriterPage(image, 3), 1, 0, []uint32{10})
	putWriterBlobLeaf(retirementWriterPage(image, 4), 1, 0, []uint32{11})
	putWriterRetirementLeaf(retirementWriterPage(image, 2), 1, []retirementBatch{
		{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3},
		{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 4},
	})
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 4)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 4))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
	source := &retirementWriterTestSource{data: image, pageCount: 20}
	result, problem := deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 3, pageCount: 20, root: 2, batchCount: 2}, 1, &arena, path, writerScanScratch(), &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatalf("delete code=%d page=%d", problem.code, problem.page)
	}
	if result.batchCount != 1 || result.privatePages != 1 || result.committedReplacements != 2 || arena.inUseCount() != 1 {
		t.Fatalf("result=%+v in-use=%d", result, arena.inUseCount())
	}
	if replacements.length != 2 || replacements.entries[0] != (committedPageReplacement{pageNumber: 2, origin: committedPageRetirementTree}) || replacements.entries[1] != (committedPageReplacement{pageNumber: 3, origin: committedPageRetirementBlob}) {
		t.Fatalf("replacements=%+v", replacements.used())
	}
	leaf, status := openRetirementLeafStatus(arena.testSlot(result.root).bytes[:], 4, 22)
	if status.failed() || leaf.len() != 1 {
		t.Fatalf("leaf=%+v status=%+v", leaf, status)
	}
	batch, status := leaf.batchStatus(0)
	if status.failed() || batch.retiredByTxn != 3 || batch.pageListBlobRoot != 4 {
		t.Fatalf("batch=%+v status=%+v", batch, status)
	}
}

func TestRetirementCombinedDeleteAndUpsertOneFixedPoint(t *testing.T) {
	image := retirementWriterImage(20)
	putWriterBlobLeaf(retirementWriterPage(image, 3), 1, 0, []uint32{10})
	putWriterBlobLeaf(retirementWriterPage(image, 4), 1, 0, []uint32{11})
	putWriterRetirementLeaf(retirementWriterPage(image, 2), 1, []retirementBatch{
		{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3},
		{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 4},
	})
	slots := writerSlots(20, 4)
	arena, problem := newPrivatePageArena(slots, 20, 24, 4)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{2, 3, 12}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	deletePath, upsertPath := make([]retirementPathFrame, 2), make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 6))
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	source := &retirementWriterTestSource{data: image, pageCount: 20}
	result, problem := deleteOldestAndUpsertNewestRetirement(source, retirementTreeState{selectedTxn: 3, pageCount: 20, root: 2, batchCount: 2}, 1, &token, deletePath, upsertPath, writerScanScratch(), &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatalf("combined code=%d page=%d second=%d roles=%d/%d", problem.code, problem.page, problem.secondPage, problem.existingRole, problem.requestedRole)
	}
	if result.batchCount != 2 || result.committedReplacements != 2 || arena.inUseCount() != 2 || replacements.length != 2 {
		t.Fatalf("result=%+v in-use=%d replacements=%+v", result, arena.inUseCount(), replacements.used())
	}
	leaf, status := openRetirementLeafStatus(arena.testSlot(result.root).bytes[:], 4, 24)
	if status.failed() || leaf.len() != 2 {
		t.Fatalf("leaf=%+v status=%+v", leaf, status)
	}
	first, _ := leaf.batchStatus(0)
	newest, _ := leaf.batchStatus(1)
	if first.retiredByTxn != 3 || first.pageListBlobRoot != 4 || newest.retiredByTxn != 4 || newest.pageCount != 3 {
		t.Fatalf("batches=%+v/%+v", first, newest)
	}
}

func TestRetirementCommittedParentCannotSplicePrivateChild(t *testing.T) {
	image := retirementWriterImage(20)
	putWriterRetirementBranch(retirementWriterPage(image, 2), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 3, childPage: 20}})
	slots := writerSlots(20, 4)
	slots[0].inUse, slots[0].origin, slots[0].generation = true, privatePageRetirementTree, 5
	putWriterRetirementLeaf(slots[0].bytes[:], 4, []retirementBatch{{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 5}})
	arena, problem := newInitializedPrivatePageArena(slots, 20, 24, 4)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{10}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 3)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 4))
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	source := &retirementWriterTestSource{data: image, pageCount: 20}
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 3, pageCount: 20, root: 2, batchCount: 1}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrCommittedParentPrivateChild)
	if problem.page != 2 || problem.secondPage != 20 || arena.inUseCount() != 1 || !arena.pool.slots[0].inUse {
		t.Fatalf("problem=%+v in-use=%d", problem, arena.inUseCount())
	}
}

func TestRetirementPrivateBlobRequiresOneGeneration(t *testing.T) {
	slots := writerSlots(20, 4)
	slots[0].inUse, slots[0].origin, slots[0].generation = true, privatePageRetirementTree, 5
	putWriterRetirementLeaf(slots[0].bytes[:], 3, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 21}})
	slots[1].inUse, slots[1].origin, slots[1].generation = true, privatePageRetirementBlob, 5
	putWriterBlobBranch(slots[1].bytes[:], 3, 1, []blobBranchEntry{{logicalOffset: 0, childPage: 22}})
	slots[2].inUse, slots[2].origin, slots[2].generation = true, privatePageRetirementBlob, 4
	putWriterBlobLeaf(slots[2].bytes[:], 3, 0, []uint32{10})
	arena, problem := newInitializedPrivatePageArena(slots, 20, 24, 3)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
	source := &retirementWriterTestSource{pageCount: 20}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 2, pageCount: 20, root: 20, batchCount: 1}, 1, &arena, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobTokenGenerationMismatch)
	if arena.inUseCount() != 3 || replacements.length != 0 || releases.length != 0 {
		t.Fatalf("in-use=%d replacements=%d releases=%d", arena.inUseCount(), replacements.length, releases.length)
	}
}

func TestRetirementCrossLeafOrderAndRoleAliasesRejected(t *testing.T) {
	image := retirementWriterImage(5000)
	putWriterRetirementLeaf(retirementWriterPage(image, 2), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1013, pageListBlobRoot: 3}})
	putWriterBlobBranch(retirementWriterPage(image, 3), 1, 1, []blobBranchEntry{{logicalOffset: 0, childPage: 4}, {logicalOffset: uint64(retirementValuesPerBlobLeaf * 4), childPage: 5}})
	first := make([]uint32, retirementValuesPerBlobLeaf)
	for index := range first {
		first[index] = uint32(1000 + index)
	}
	putWriterBlobLeaf(retirementWriterPage(image, 4), 1, 0, first)
	putWriterBlobLeaf(retirementWriterPage(image, 5), 1, uint64(retirementValuesPerBlobLeaf*4), []uint32{1500})
	slots := writerSlots(5000, 1)
	arena, problem := newPrivatePageArena(slots, 5000, 5001, 3)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
	releases := newPrivateReleaseBuffer(make([]uint32, 1))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 1100))
	source := &retirementWriterTestSource{data: image, pageCount: 5000}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 2, pageCount: 5000, root: 2, batchCount: 1}, 1, &arena, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrRetirementStreamOrder)
	if problem.page != 2011 || problem.secondPage != 1500 || arena.inUseCount() != 0 || replacements.length != 0 {
		t.Fatalf("problem code=%d pages=%d/%d", problem.code, problem.page, problem.secondPage)
	}

	alias := retirementWriterImage(20)
	putWriterRetirementLeaf(retirementWriterPage(alias, 2), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 2}})
	noSlots := []privatePageSlot{}
	aliasArena, problem := newPrivatePageArena(noSlots, 20, 20, 3)
	if problem.failed() {
		t.Fatal(problem)
	}
	aliasReplacements := newCommittedReplacementLedger(make([]committedPageReplacement, 4))
	aliasReleases := newPrivateReleaseBuffer(nil)
	aliasRoles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	aliasSource := &retirementWriterTestSource{data: alias, pageCount: 20}
	_, problem = deleteOldestRetirementPrefix(aliasSource, retirementTreeState{selectedTxn: 2, pageCount: 20, root: 2, batchCount: 1}, 1, &aliasArena, path, writerScanScratch(), &aliasReplacements, &aliasReleases, &aliasRoles)
	requireRetirementWriteCode(t, problem, retirementWriteErrPageRoleConflict)
}

func TestRetirementAccessFirstAndFinalApplyCheckAreAtomic(t *testing.T) {
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	source := &retirementWriterTestSource{pageCount: 20, failAccess: 1}
	_, problem = upsertNewestRetirement(source, retirementTreeState{}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrSource)
	if arena.inUseCount() != 0 || replacements.length != 0 || releases.length != 0 {
		t.Fatal("access-first failure was not atomic")
	}

	token, problem = buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	source = &retirementWriterTestSource{pageCount: 20, failAccess: 3}
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrSource)
	if source.accessCalls != 3 || arena.inUseCount() != 0 || replacements.length != 0 || releases.length != 0 {
		t.Fatalf("calls=%d in-use=%d replacements=%d releases=%d", source.accessCalls, arena.inUseCount(), replacements.length, releases.length)
	}
}

func TestRetirementNewestUpsertOnlyReadsRightEdgeAndPreservesPrefixLedger(t *testing.T) {
	image := retirementWriterImage(30)
	putWriterRetirementBranch(retirementWriterPage(image, 2), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}, {maxRetiredByTxn: 3, childPage: 4}})
	putWriterRetirementLeaf(retirementWriterPage(image, 4), 1, []retirementBatch{{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 5}})
	slots := writerSlots(30, 6)
	arena, problem := newPrivatePageArena(slots, 30, 36, 4)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{2, 4, 10}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 3)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 6))
	releases := newPrivateReleaseBuffer(make([]uint32, 6))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 40))
	source := &retirementWriterTestSource{data: image, pageCount: 30}
	scratch := writerScanScratch()
	first, problem := upsertNewestRetirement(source, retirementTreeState{selectedTxn: 3, pageCount: 30, root: 2, batchCount: 2}, &token, path, scratch, &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatalf("right-edge append code=%d page=%d", problem.code, problem.page)
	}
	if first.batchCount != 3 || replacements.length != 2 || replacements.entries[0].pageNumber != 4 || replacements.entries[1].pageNumber != 2 {
		t.Fatalf("first=%+v replacements=%+v", first, replacements.used())
	}

	token, problem = buildRetirementBlob([]uint32{2, 4, 10, 11}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	second, problem := upsertNewestRetirement(source, retirementTreeState{selectedTxn: 3, pageCount: 30, root: first.root, batchCount: first.batchCount}, &token, path, scratch, &replacements, &releases, &roles)
	if problem.failed() {
		t.Fatalf("prefix convergence code=%d page=%d", problem.code, problem.page)
	}
	if second.batchCount != first.batchCount || second.committedReplacements != 0 || replacements.length != 2 {
		t.Fatalf("second=%+v replacements=%+v", second, replacements.used())
	}
}

func TestRetirementGlobalTransactionOrderAcrossLeaves(t *testing.T) {
	image := retirementWriterImage(40)
	putWriterRetirementBranch(retirementWriterPage(image, 2), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 5, childPage: 3}, {maxRetiredByTxn: 6, childPage: 4}})
	putWriterRetirementLeaf(retirementWriterPage(image, 3), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 10}, {retiredByTxn: 5, pageCount: 1, pageListBlobRoot: 11}})
	putWriterRetirementLeaf(retirementWriterPage(image, 4), 1, []retirementBatch{{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 12}, {retiredByTxn: 6, pageCount: 1, pageListBlobRoot: 13}})
	for pageNumber, listed := range map[uint32]uint32{10: 20, 11: 21, 12: 22, 13: 23} {
		putWriterBlobLeaf(retirementWriterPage(image, pageNumber), 1, 0, []uint32{listed})
	}
	slots := writerSlots(40, 4)
	arena, problem := newPrivatePageArena(slots, 40, 44, 7)
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 3)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 12))
	releases := newPrivateReleaseBuffer(make([]uint32, 4))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 64))
	source := &retirementWriterTestSource{data: image, pageCount: 40}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 6, pageCount: 40, root: 2, batchCount: 4}, 3, &arena, path, writerScanScratch(), &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrRetirementTreeOrder)
	if problem.first64 != 5 || problem.second64 != 4 || arena.inUseCount() != 0 || replacements.length != 0 {
		t.Fatalf("problem=%d/%d code=%d", problem.first64, problem.second64, problem.code)
	}
}

func TestRetirementCombinedPromotedRootEpochAndBackreference(t *testing.T) {
	t.Run("reselect retained root", func(t *testing.T) {
		image := retirementWriterImage(30)
		putWriterRetirementBranch(retirementWriterPage(image, 2), 1, 2, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}, {maxRetiredByTxn: 4, childPage: 5}})
		putWriterRetirementBranch(retirementWriterPage(image, 3), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 4}})
		putWriterRetirementLeaf(retirementWriterPage(image, 4), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 10}})
		putWriterRetirementBranch(retirementWriterPage(image, 5), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 3, childPage: 6}, {maxRetiredByTxn: 4, childPage: 7}})
		putWriterRetirementLeaf(retirementWriterPage(image, 6), 1, []retirementBatch{{retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 11}})
		putWriterRetirementLeaf(retirementWriterPage(image, 7), 1, []retirementBatch{{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 12}})
		putWriterBlobLeaf(retirementWriterPage(image, 10), 1, 0, []uint32{14})
		slots := writerSlots(30, 3)
		arena, problem := newPrivatePageArena(slots, 30, 33, 5)
		if problem.failed() {
			t.Fatal(problem)
		}
		order := []uint32{0}
		token, problem := buildRetirementBlob([]uint32{2, 3, 4, 5, 7, 10, 13}, &arena, &blobBuildScratch{pageNumbers: order})
		if problem.failed() {
			t.Fatal(problem)
		}
		deletePath, upsertPath := make([]retirementPathFrame, 4), make([]retirementPathFrame, 4)
		replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 8))
		releases := newPrivateReleaseBuffer(make([]uint32, 3))
		roles := newPageRoleIndex(make([]pageRoleIndexSlot, 64))
		source := &retirementWriterTestSource{data: image, pageCount: 30}
		result, problem := deleteOldestAndUpsertNewestRetirement(source, retirementTreeState{selectedTxn: 4, pageCount: 30, root: 2, batchCount: 3}, 1, &token, deletePath, upsertPath, writerScanScratch(), &replacements, &releases, &roles)
		if problem.failed() {
			t.Fatalf("combined promotion code=%d page=%d roles=%d/%d", problem.code, problem.page, problem.existingRole, problem.requestedRole)
		}
		if result.batchCount != 3 || result.committedReplacements != 6 || replacements.length != 6 || arena.inUseCount() != 3 {
			t.Fatalf("result=%+v replacements=%+v in-use=%d", result, replacements.used(), arena.inUseCount())
		}
	})

	t.Run("reject deeper backreference", func(t *testing.T) {
		image := retirementWriterImage(30)
		putWriterRetirementBranch(retirementWriterPage(image, 2), 1, 3, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}, {maxRetiredByTxn: 4, childPage: 5}})
		putWriterRetirementBranch(retirementWriterPage(image, 3), 1, 2, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 4}})
		putWriterRetirementBranch(retirementWriterPage(image, 4), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 6}})
		putWriterRetirementLeaf(retirementWriterPage(image, 6), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 10}})
		putWriterRetirementBranch(retirementWriterPage(image, 5), 1, 2, []retirementBranchEntry{{maxRetiredByTxn: 3, childPage: 7}, {maxRetiredByTxn: 4, childPage: 8}})
		putWriterRetirementBranch(retirementWriterPage(image, 8), 1, 1, []retirementBranchEntry{{maxRetiredByTxn: 3, childPage: 5}, {maxRetiredByTxn: 4, childPage: 9}})
		putWriterRetirementLeaf(retirementWriterPage(image, 9), 1, []retirementBatch{{retiredByTxn: 4, pageCount: 1, pageListBlobRoot: 12}})
		putWriterBlobLeaf(retirementWriterPage(image, 10), 1, 0, []uint32{14})
		slots := writerSlots(30, 4)
		arena, problem := newPrivatePageArena(slots, 30, 34, 5)
		if problem.failed() {
			t.Fatal(problem)
		}
		order := []uint32{0}
		token, problem := buildRetirementBlob([]uint32{13}, &arena, &blobBuildScratch{pageNumbers: order})
		if problem.failed() {
			t.Fatal(problem)
		}
		deletePath, upsertPath := make([]retirementPathFrame, 5), make([]retirementPathFrame, 5)
		replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 12))
		releases := newPrivateReleaseBuffer(make([]uint32, 4))
		roles := newPageRoleIndex(make([]pageRoleIndexSlot, 64))
		source := &retirementWriterTestSource{data: image, pageCount: 30}
		_, problem = deleteOldestAndUpsertNewestRetirement(source, retirementTreeState{selectedTxn: 4, pageCount: 30, root: 2, batchCount: 3}, 1, &token, deletePath, upsertPath, writerScanScratch(), &replacements, &releases, &roles)
		requireRetirementWriteCode(t, problem, retirementWriteErrPageRoleConflict)
		if problem.page != 5 || arena.inUseCount() != 0 || replacements.length != 0 || releases.length != 0 {
			t.Fatalf("problem page=%d in-use=%d replacements=%d releases=%d", problem.page, arena.inUseCount(), replacements.length, releases.length)
		}
	})
}

func TestRetirementBlobScanScratchBudgetIsExactAndAtomic(t *testing.T) {
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &arena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	source := &retirementWriterTestSource{pageCount: 20}
	scratch := &retirementBlobScanScratch{}
	_, problem = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobScanScratchTooSmall)
	if problem.required != 1 || problem.actual != 0 || arena.inUseCount() != 0 || replacements.length != 0 || releases.length != 0 {
		t.Fatalf("problem=%d/%d in-use=%d", problem.required, problem.actual, arena.inUseCount())
	}
}

func TestRetirementTransactionCountAndTokenBorrowContracts(t *testing.T) {
	source := &retirementWriterTestSource{pageCount: 20}
	path := make([]retirementPathFrame, 2)
	scratch := writerScanScratch()
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))

	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 7)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 5, pageCount: 20}, 0, &arena, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrTransactionOrder)
	if problem.first64 != 5 || problem.second64 != 7 {
		t.Fatalf("transaction evidence=%d/%d", problem.first64, problem.second64)
	}

	overflowArena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: ^uint64(0), pageCount: 20}, 0, &overflowArena, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrSelectedTransactionOverflow)

	borrowArena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	order := []uint32{0}
	token, problem := buildRetirementBlob([]uint32{7}, &borrowArena, &blobBuildScratch{pageNumbers: order})
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, 0, &borrowArena, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrBlobTokenStale)
	if borrowArena.inUseCount() != 1 {
		t.Fatal("borrow rejection mutated active token")
	}
	token.discard()

	countArena, problem := newPrivatePageArena(slots, 20, 22, 3)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 2, pageCount: 20, root: 2}, 0, &countArena, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrRootCountMismatch)
	_, problem = deleteOldestRetirementPrefix(source, retirementTreeState{selectedTxn: 2, pageCount: 20}, 1, &countArena, path, scratch, &replacements, &releases, &roles)
	requireRetirementWriteCode(t, problem, retirementWriteErrDeleteCountOutOfRange)
}

func TestRetirementWriterHotPathsAllocateNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	values, order := []uint32{7}, []uint32{0}
	slots := writerSlots(20, 2)
	arena, problem := newPrivatePageArena(slots, 20, 22, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	var last retirementWriteError
	allocations := testing.AllocsPerRun(100, func() {
		token, current := buildRetirementBlob(values, &arena, &blobBuildScratch{pageNumbers: order})
		last = current
		if !current.failed() {
			token.discard()
		}
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("blob build allocations=%v code=%d", allocations, last.code)
	}

	path := make([]retirementPathFrame, 2)
	replacements := newCommittedReplacementLedger(make([]committedPageReplacement, 2))
	releases := newPrivateReleaseBuffer(make([]uint32, 2))
	roles := newPageRoleIndex(make([]pageRoleIndexSlot, 16))
	source := &retirementWriterTestSource{pageCount: 20}
	scanScratch := writerScanScratch()
	allocations = testing.AllocsPerRun(100, func() {
		token, current := buildRetirementBlob(values, &arena, &blobBuildScratch{pageNumbers: order})
		if !current.failed() {
			_, current = upsertNewestRetirement(source, retirementTreeState{selectedTxn: 1, pageCount: 20}, &token, path, scanScratch, &replacements, &releases, &roles)
		}
		last = current
		resetRetirementWriterArena(&arena)
		replacements.length, releases.length = 0, 0
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("empty upsert allocations=%v code=%d", allocations, last.code)
	}

	committed := retirementWriterImage(20)
	putWriterBlobLeaf(retirementWriterPage(committed, 3), 1, 0, []uint32{10})
	putWriterRetirementLeaf(retirementWriterPage(committed, 2), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3}})
	appendSource := &retirementWriterTestSource{data: committed, pageCount: 20}
	appendValues := []uint32{2, 12}
	appendSlots := writerSlots(20, 3)
	appendArena, current := newPrivatePageArena(appendSlots, 20, 23, 3)
	if current.failed() {
		t.Fatal(current)
	}
	appendOrder := []uint32{0}
	appendPath := make([]retirementPathFrame, 2)
	appendScratch := writerScanScratch()
	appendReplacements := newCommittedReplacementLedger(make([]committedPageReplacement, 3))
	appendReleases := newPrivateReleaseBuffer(make([]uint32, 3))
	appendRoles := newPageRoleIndex(make([]pageRoleIndexSlot, 24))
	allocations = testing.AllocsPerRun(100, func() {
		token, operation := buildRetirementBlob(appendValues, &appendArena, &blobBuildScratch{pageNumbers: appendOrder})
		if !operation.failed() {
			_, operation = upsertNewestRetirement(appendSource, retirementTreeState{selectedTxn: 2, pageCount: 20, root: 2, batchCount: 1}, &token, appendPath, appendScratch, &appendReplacements, &appendReleases, &appendRoles)
		}
		last = operation
		resetRetirementWriterArena(&appendArena)
		appendReplacements.length, appendReleases.length = 0, 0
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("append allocations=%v code=%d", allocations, last.code)
	}

	replaceValues, replaceCarryValues := []uint32{7}, []uint32{7, 8}
	replaceSlots := writerSlots(20, 6)
	replaceArena, current := newPrivatePageArena(replaceSlots, 20, 26, 2)
	if current.failed() {
		t.Fatal(current)
	}
	replaceOrder := []uint32{0}
	replacePath := make([]retirementPathFrame, 2)
	replaceScratch := writerScanScratch()
	replaceReplacements := newCommittedReplacementLedger(make([]committedPageReplacement, 3))
	replaceReleases := newPrivateReleaseBuffer(make([]uint32, 6))
	replaceRoles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	replaceSource := &retirementWriterTestSource{pageCount: 20}
	allocations = testing.AllocsPerRun(100, func() {
		firstToken, operation := buildRetirementBlob(replaceValues, &replaceArena, &blobBuildScratch{pageNumbers: replaceOrder})
		firstResult := retirementTreeEditResult{}
		if !operation.failed() {
			firstResult, operation = upsertNewestRetirement(replaceSource, retirementTreeState{selectedTxn: 1, pageCount: 20}, &firstToken, replacePath, replaceScratch, &replaceReplacements, &replaceReleases, &replaceRoles)
		}
		if !operation.failed() {
			secondToken, secondProblem := buildRetirementBlob(replaceCarryValues, &replaceArena, &blobBuildScratch{pageNumbers: replaceOrder})
			operation = secondProblem
			if !operation.failed() {
				_, operation = upsertNewestRetirement(replaceSource, retirementTreeState{selectedTxn: 1, pageCount: 20, root: firstResult.root, batchCount: 1}, &secondToken, replacePath, replaceScratch, &replaceReplacements, &replaceReleases, &replaceRoles)
			}
		}
		last = operation
		resetRetirementWriterArena(&replaceArena)
		replaceReplacements.length, replaceReleases.length = 0, 0
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("replace allocations=%v code=%d", allocations, last.code)
	}

	deleteImage := retirementWriterImage(20)
	putWriterBlobLeaf(retirementWriterPage(deleteImage, 3), 1, 0, []uint32{10})
	putWriterBlobLeaf(retirementWriterPage(deleteImage, 4), 1, 0, []uint32{11})
	putWriterRetirementLeaf(retirementWriterPage(deleteImage, 2), 1, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3}, {retiredByTxn: 3, pageCount: 1, pageListBlobRoot: 4}})
	deleteSlots := writerSlots(20, 4)
	deleteArena, current := newPrivatePageArena(deleteSlots, 20, 24, 4)
	if current.failed() {
		t.Fatal(current)
	}
	deleteSource := &retirementWriterTestSource{data: deleteImage, pageCount: 20}
	deletePath := make([]retirementPathFrame, 2)
	deleteScratch := writerScanScratch()
	deleteReplacements := newCommittedReplacementLedger(make([]committedPageReplacement, 5))
	deleteReleases := newPrivateReleaseBuffer(make([]uint32, 4))
	deleteRoles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	allocations = testing.AllocsPerRun(100, func() {
		_, last = deleteOldestRetirementPrefix(deleteSource, retirementTreeState{selectedTxn: 3, pageCount: 20, root: 2, batchCount: 2}, 1, &deleteArena, deletePath, deleteScratch, &deleteReplacements, &deleteReleases, &deleteRoles)
		resetRetirementWriterArena(&deleteArena)
		deleteReplacements.length, deleteReleases.length = 0, 0
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("delete allocations=%v code=%d", allocations, last.code)
	}

	combinedSlots := writerSlots(20, 4)
	combinedArena, current := newPrivatePageArena(combinedSlots, 20, 24, 4)
	if current.failed() {
		t.Fatal(current)
	}
	combinedValues, combinedOrder := []uint32{2, 3, 12}, []uint32{0}
	combinedDeletePath, combinedUpsertPath := make([]retirementPathFrame, 2), make([]retirementPathFrame, 2)
	combinedScratch := writerScanScratch()
	combinedReplacements := newCommittedReplacementLedger(make([]committedPageReplacement, 6))
	combinedReleases := newPrivateReleaseBuffer(make([]uint32, 4))
	combinedRoles := newPageRoleIndex(make([]pageRoleIndexSlot, 32))
	allocations = testing.AllocsPerRun(100, func() {
		token, operation := buildRetirementBlob(combinedValues, &combinedArena, &blobBuildScratch{pageNumbers: combinedOrder})
		if !operation.failed() {
			_, operation = deleteOldestAndUpsertNewestRetirement(deleteSource, retirementTreeState{selectedTxn: 3, pageCount: 20, root: 2, batchCount: 2}, 1, &token, combinedDeletePath, combinedUpsertPath, combinedScratch, &combinedReplacements, &combinedReleases, &combinedRoles)
		}
		last = operation
		resetRetirementWriterArena(&combinedArena)
		combinedReplacements.length, combinedReleases.length = 0, 0
	})
	if last.failed() || allocations != 0 {
		t.Fatalf("combined allocations=%v code=%d", allocations, last.code)
	}
}

func resetRetirementWriterArena(arena *privatePageArena) {
	for index := range arena.pool.slots {
		slot := &arena.pool.slots[index]
		slot.state, slot.inUse = privatePageAvailable, false
		slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
		slot.pendingTxn, slot.generation, slot.committedOrigin = 0, 0, 0
		slot.checkpointID, slot.pendingReturnState = 0, 0
		clear(slot.bytes[:])
		slot.epoch++
	}
	arena.pool.activeCheckpointID, arena.pool.generation = 0, 0
	arena.pool.rebuildIndexFree(arena.pool.indexRoot)
	arena.allocationCursor, arena.activeTokenEpoch, arena.activeTokenGen = 0, 0, 0
}
