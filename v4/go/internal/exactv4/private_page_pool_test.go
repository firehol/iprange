package exactv4

import "testing"

var privatePageBenchmarkSink [PageSize]byte

func testPrivatePagePool(t *testing.T, slots []privatePagePoolSlot, committed, pending uint64) *privatePagePool {
	t.Helper()
	pool := &privatePagePool{}
	problem := initPrivatePagePool(pool, slots, make([]uint32, len(slots)), committed, pending, 2, privatePageOwnerNone)
	if problem.failed() {
		t.Fatal(problem)
	}
	return pool
}

func TestPrivatePagePoolTransfersOnePhysicalPageAcrossEngines(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(7, privatePageCommittedFree),
		newPrivatePageSlot(20, privatePageAppended),
	}
	pool := testPrivatePagePool(t, slots, 20, 21)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	bitmap, problem := pool.claimPage(checkpoint, 7, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	var contents [PageSize]byte
	contents[PageHeaderSize] = 0xa5
	if problem = pool.writePage(bitmap, &contents); problem.failed() {
		t.Fatal(problem)
	}
	physical := &pool.slots[bitmap.slot].bytes
	retirement, problem := pool.transfer(
		checkpoint, bitmap, privatePageOwnerRetirement, privatePageRetirementTree,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	var transferred [PageSize]byte
	if problem = pool.readPage(retirement, &transferred); problem.failed() ||
		&pool.slots[retirement.slot].bytes != physical || transferred[PageHeaderSize] != 0xa5 {
		t.Fatal("ownership transfer copied or lost the physical page")
	}
	if problem = pool.readPage(bitmap, &transferred); problem.code != privatePagePoolErrStaleToken {
		t.Fatal("pre-transfer token retained access")
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivatePagePoolAdaptersAlternateOnePhysicalPage(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(pool, slots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
		t.Fatal(problem)
	}
	ledger := newFreeBitmapCOWLedger(
		nil, nil, nil, make([]bitmapCOWIndexNode, 1), make([]int, 1),
	)
	bitmap, problem := newFreeBitmapCOWWithPool(nil, 1, 20, 0, pool, ledger)
	if problem.failed() {
		t.Fatal(problem)
	}
	retirement, retirementProblem := newPrivatePageArenaWithPool(pool, 2)
	if retirementProblem.failed() {
		t.Fatal(retirementProblem)
	}
	if bitmap.pagePool() != pool || retirement.pagePool() != pool {
		t.Fatal("engine adapters do not share the canonical pool pointer")
	}

	payload := [PageSize]byte{}
	payload[PageHeaderSize+9] = 0xa5
	operation, poolProblem := pool.beginOperation()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem = bitmap.claimBitmapSlot(operation, 0, &payload, 0); problem.failed() {
		t.Fatal(problem)
	}
	if poolProblem = pool.commitOperation(operation); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	physical := &pool.slots[0].bytes
	claimed, _ := pool.pageInfo(7)
	if claimed.owner != privatePageOwnerBitmap || claimed.pendingTxn != 2 || claimed.generation != 1 || claimed.epoch != 2 {
		t.Fatalf("bitmap claim metadata = %+v", claimed)
	}
	if _, poolProblem = retirement.privateToken(7, privatePageRetirementTree); poolProblem.code != privatePagePoolErrOwnerMismatch {
		t.Fatalf("retirement borrowed bitmap-owned page: %+v", poolProblem)
	}

	checkpoint, poolProblem := pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	denied := pageSourceError{code: pageSourceErrForkedHandle}
	bitmap.committed = &cowSparsePages{access: &denied}
	beforeDenied := pool.slots[0]
	if _, transferProblem := bitmap.transferBitmapPageToRetirement(checkpoint, 7, privatePageRetirementTree); transferProblem.code != freeBitmapCOWErrSource {
		t.Fatalf("access-first transfer = %+v", transferProblem)
	}
	if pool.slots[0] != beforeDenied {
		t.Fatal("access-first rejection mutated private-page authority")
	}
	bitmap.committed = nil
	retirementToken, transferProblem := bitmap.transferBitmapPageToRetirement(checkpoint, 7, privatePageRetirementTree)
	if transferProblem.failed() {
		t.Fatal(transferProblem)
	}
	if _, bitmapProblem := bitmap.bitmapToken(0); !bitmapProblem.failed() {
		t.Fatal("bitmap retained access after retirement transfer")
	}
	if &pool.slots[retirementToken.slot].bytes != physical {
		t.Fatal("bitmap-to-retirement transfer changed physical backing")
	}
	if poolProblem = pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	retired, _ := pool.pageInfo(7)
	if retired.owner != privatePageOwnerRetirement || retired.pendingTxn != 2 ||
		retired.generation != 2 || retired.epoch != 3 || pool.generation != 2 {
		t.Fatalf("retirement transfer metadata = %+v pool-generation=%d", retired, pool.generation)
	}

	checkpoint, poolProblem = pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	bitmapToken, retirementProblem := retirement.transferRetirementPageToBitmap(
		checkpoint, 7, privatePageRetirementTree,
	)
	if retirementProblem.failed() {
		t.Fatal(retirementProblem)
	}
	if poolProblem = pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	var copied [PageSize]byte
	if poolProblem = pool.readPage(bitmapToken, &copied); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	returned, _ := pool.pageInfo(7)
	if &pool.slots[bitmapToken.slot].bytes != physical || copied[PageHeaderSize+9] != 0xa5 ||
		returned.owner != privatePageOwnerBitmap || returned.pendingTxn != 2 ||
		returned.generation != 3 || returned.epoch != 4 || pool.generation != 3 {
		t.Fatalf("retirement-to-bitmap transfer metadata = %+v pool-generation=%d", returned, pool.generation)
	}
}

func TestPrivatePagePoolRejectsCopiedStorageAndWrongTransactionCapabilities(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(pool, slots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
		t.Fatal(problem)
	}

	copied := *pool
	if _, problem := copied.begin(); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("copied pool began a checkpoint: %+v", problem)
	}
	if copied.capacity() != 0 {
		t.Fatal("copied pool exposed aliased slot capacity")
	}

	ledger := newFreeBitmapCOWLedger(nil, nil, nil, make([]bitmapCOWIndexNode, 1), make([]int, 1))
	if _, problem := newFreeBitmapCOWWithPool(nil, 2, 20, 0, pool, ledger); problem.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("wrong-transaction bitmap adapter = %+v", problem)
	}
	if _, problem := newPrivatePageArenaWithPool(pool, 3); problem.code != retirementWriteErrTransactionOrder {
		t.Fatalf("wrong-transaction retirement adapter = %+v", problem)
	}

	operation, problem := pool.beginOperation()
	if problem.failed() {
		t.Fatal(problem)
	}
	wrongTxn := operation
	wrongTxn.pendingTxn++
	if _, problem = pool.claimPageForOperation(
		wrongTxn, 7, privatePageOwnerBitmap, privatePageBitmap,
	); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("wrong-transaction operation = %+v", problem)
	}
	wrongGeneration := operation
	wrongGeneration.generation++
	if _, problem = pool.claimPageForOperation(
		wrongGeneration, 7, privatePageOwnerBitmap, privatePageBitmap,
	); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("fabricated operation generation = %+v", problem)
	}
	if problem = pool.abortOperation(operation); problem.failed() {
		t.Fatal(problem)
	}

	appendedSlots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	appendedPool := &privatePagePool{}
	if problem := initPrivatePagePool(
		appendedPool, appendedSlots, make([]uint32, 1), 20, 21, 2, privatePageOwnerNone,
	); problem.failed() {
		t.Fatal(problem)
	}
	appendedLedger := newFreeBitmapCOWLedger(nil, nil, nil, make([]bitmapCOWIndexNode, 1), make([]int, 1))
	appendedBitmap, bitmapProblem := newFreeBitmapCOWWithPool(nil, 1, 21, 0, appendedPool, appendedLedger)
	if bitmapProblem.failed() {
		t.Fatal(bitmapProblem)
	}
	if appendedBitmap.committedPageCount != 20 || appendedBitmap.pageCount != 21 || !appendedBitmap.pageCountsDistinct {
		t.Fatalf(
			"shared bitmap page counts = committed %d pending %d distinct %t",
			appendedBitmap.committedPageCount, appendedBitmap.pageCount, appendedBitmap.pageCountsDistinct,
		)
	}
	if _, bitmapProblem = newFreeBitmapCOWWithPool(nil, 1, 21, 21, appendedPool, appendedLedger); bitmapProblem.code != freeBitmapCOWErrRootPageOutOfBounds {
		t.Fatalf("out-of-range pending bitmap root = %+v", bitmapProblem)
	}
}

func TestPrivatePagePoolRejectsInvalidCrossEngineTagsAtomically(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(pool, slots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
		t.Fatal(problem)
	}
	retirement, problem := newPrivatePageArenaWithPool(pool, 2)
	if problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, poolProblem := pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	token, poolProblem := pool.claimLowest(checkpoint, privatePageOwnerRetirement, privatePageRetirementTree)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = pool.commit(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	checkpoint, poolProblem = pool.begin()
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	before := pool.slots[0]
	mutationBefore := pool.mutationEpoch
	if _, poolProblem = pool.transfer(
		checkpoint, token, privatePageOwner(^uint8(0)), privatePageBitmap,
	); poolProblem.code != privatePagePoolErrInvalidState {
		t.Fatalf("maximal destination owner = %+v", poolProblem)
	}
	if pool.slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("invalid destination owner partially transferred ownership")
	}
	if _, poolProblem = pool.transfer(
		checkpoint, token, privatePageOwnerBitmap, privatePageOrigin(^uint8(0)),
	); poolProblem.code != privatePagePoolErrInvalidState {
		t.Fatalf("maximal destination tag = %+v", poolProblem)
	}
	if pool.slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("invalid destination tag partially transferred ownership")
	}
	if _, problem = retirement.transferRetirementPageToBitmap(
		checkpoint, 7, privatePageOrigin(^uint8(0)),
	); !problem.failed() {
		t.Fatal("retirement adapter accepted maximal source tag")
	}
	if pool.slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("invalid retirement source tag partially transferred ownership")
	}
	if poolProblem = pool.rollback(checkpoint); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	before = pool.slots[0]
	mutationBefore = pool.mutationEpoch
	if poolProblem = pool.releaseGeneration(
		token.generation, privatePageOwner(^uint8(0)), privatePageRetirementTree,
	); poolProblem.code != privatePagePoolErrInvalidState {
		t.Fatalf("maximal release owner = %+v", poolProblem)
	}
	if pool.slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("invalid release owner mutated the page")
	}
	if poolProblem = pool.releaseGeneration(
		token.generation, privatePageOwnerRetirement, privatePageOrigin(^uint8(0)),
	); poolProblem.code != privatePagePoolErrInvalidState {
		t.Fatalf("maximal release origin = %+v", poolProblem)
	}
	if pool.slots[0] != before || pool.mutationEpoch != mutationBefore {
		t.Fatal("invalid release origin mutated the page")
	}
}

func TestPrivatePagePoolInitializationFailureIsAtomic(t *testing.T) {
	stableSlots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(pool, stableSlots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
		t.Fatal(problem)
	}
	stableSelf, stableEpoch := pool.self, pool.epoch
	stableBacking := &pool.slots[0]

	cases := []struct {
		name  string
		slots []privatePagePoolSlot
		code  privatePagePoolErrorCode
	}{
		{
			name: "late-invalid-authorization",
			slots: []privatePagePoolSlot{
				newPrivatePageSlot(6, privatePageReclaimed),
				newPrivatePageSlot(20, privatePageCommittedFree),
			},
			code: privatePagePoolErrInvalidAuthorization,
		},
		{
			name: "duplicate-after-valid-prefix",
			slots: []privatePagePoolSlot{
				newPrivatePageSlot(6, privatePageReclaimed),
				newPrivatePageSlot(8, privatePageReclaimed),
				newPrivatePageSlot(6, privatePageReclaimed),
			},
			code: privatePagePoolErrPagesNotStrict,
		},
		{
			name: "invalid-owner-origin-decode",
			slots: []privatePagePoolSlot{{
				pageNumber: 7, authorization: privatePageReclaimed, state: privatePageInUse,
				inUse: true, owner: privatePageOwner(^uint8(0)), origin: privatePageRetirementTree,
			}},
			code: privatePagePoolErrInvalidState,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := append([]privatePagePoolSlot(nil), test.slots...)
			validation := make([]uint32, len(test.slots))
			allocations := testing.AllocsPerRun(100, func() {
				problem := initPrivatePagePool(pool, test.slots, validation, 20, 21, 2, privatePageOwnerNone)
				if problem.code != test.code {
					panic("unexpected initialization failure")
				}
			})
			if allocations != 0 {
				t.Fatalf("failed initialization allocations = %v", allocations)
			}
			for index := range before {
				if test.slots[index] != before[index] {
					t.Fatalf("failed initialization mutated caller slot %d", index)
				}
			}
			if pool.self != stableSelf || pool.epoch != stableEpoch || &pool.slots[0] != stableBacking {
				t.Fatal("failed initialization replaced stable canonical pool storage")
			}
		})
	}
	invalidOwnerSlots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	invalidOwnerBefore := invalidOwnerSlots[0]
	if problem := initPrivatePagePool(
		pool, invalidOwnerSlots, make([]uint32, 1), 20, 20, 2, privatePageOwner(^uint8(0)),
	); problem.code != privatePagePoolErrInvalidState {
		t.Fatalf("maximal existing owner = %+v", problem)
	}
	if invalidOwnerSlots[0] != invalidOwnerBefore || pool.self != stableSelf || pool.epoch != stableEpoch || &pool.slots[0] != stableBacking {
		t.Fatal("invalid existing owner mutated caller or canonical pool storage")
	}
}

func TestPrivatePagePoolMutationEpochIsMonotonicAndNonwrapping(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
	pool := &privatePagePool{}
	if problem := initPrivatePagePool(pool, slots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
		t.Fatal(problem)
	}
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() || pool.mutationEpoch != 1 {
		t.Fatalf("claim mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}
	page := [PageSize]byte{}
	if problem = pool.writePage(token, &page); problem.failed() || pool.mutationEpoch != 2 {
		t.Fatalf("write mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}
	retirement, problem := pool.transfer(checkpoint, token, privatePageOwnerRetirement, privatePageRetirementTree)
	if problem.failed() || pool.mutationEpoch != 3 {
		t.Fatalf("transfer mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() || pool.mutationEpoch != 3 {
		t.Fatalf("commit mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}
	retirement, problem = pool.changeOrigin(retirement, privatePageRetirementBlob)
	if problem.failed() || pool.mutationEpoch != 4 {
		t.Fatalf("origin mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}
	if problem = pool.recycle(retirement); problem.failed() || pool.mutationEpoch != 5 {
		t.Fatalf("release mutation epoch = %d/%+v", pool.mutationEpoch, problem)
	}

	pool.mutationEpoch = ^uint64(0)
	operation, problem := pool.beginOperation()
	if problem.failed() {
		t.Fatal(problem)
	}
	before := pool.slots[0]
	if _, problem = pool.claimPageForOperation(operation, 7, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrArithmeticOverflow {
		t.Fatalf("wrapped mutation epoch claim = %+v", problem)
	}
	if pool.slots[0] != before || pool.mutationEpoch != ^uint64(0) {
		t.Fatal("mutation epoch exhaustion changed page authority")
	}
	if problem = pool.abortOperation(operation); problem.failed() {
		t.Fatal(problem)
	}
}

func BenchmarkPrivatePagePoolCheckedPageRoundTrip(b *testing.B) {
	source := [PageSize]byte{}
	for index := range source {
		source[index] = byte(index)
	}
	b.SetBytes(2 * PageSize)

	b.Run("direct-backing", func(b *testing.B) {
		var slot privatePagePoolSlot
		b.SetBytes(2 * PageSize)
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			slot.bytes = source
			privatePageBenchmarkSink = slot.bytes
		}
	})

	b.Run("checked-token", func(b *testing.B) {
		slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageReclaimed)}
		pool := &privatePagePool{}
		if problem := initPrivatePagePool(pool, slots, make([]uint32, 1), 20, 20, 2, privatePageOwnerNone); problem.failed() {
			b.Fatal(problem)
		}
		checkpoint, problem := pool.begin()
		if problem.failed() {
			b.Fatal(problem)
		}
		token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() {
			b.Fatal(problem)
		}
		if problem = pool.commit(checkpoint); problem.failed() {
			b.Fatal(problem)
		}
		var destination [PageSize]byte
		b.SetBytes(2 * PageSize)
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			if problem = pool.writePage(token, &source); problem.failed() {
				b.Fatal(problem)
			}
			if problem = pool.readPage(token, &destination); problem.failed() {
				b.Fatal(problem)
			}
			privatePageBenchmarkSink = destination
		}
	})
}

func BenchmarkSharedPrivatePagePoolPreparation(b *testing.B) {
	for _, count := range []int{512, 4096, 8192} {
		b.Run(stringInt(count), func(b *testing.B) {
			pageCount := uint64(count + 2)
			slots := make([]privatePagePoolSlot, count)
			for index := range slots {
				slots[index] = newPrivatePageSlot(uint32(count+1-index), privatePageReclaimed)
			}
			validation := make([]uint32, count)
			pool := &privatePagePool{}
			ledger := newFreeBitmapCOWLedger(
				nil, nil, nil, make([]bitmapCOWIndexNode, count), make([]int, count),
			)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if problem := initPrivatePagePool(
					pool, slots, validation, pageCount, pageCount, 2, privatePageOwnerNone,
				); problem.failed() {
					b.Fatal(problem)
				}
				cow, problem := newFreeBitmapCOWWithPool(nil, 1, pageCount, 0, pool, ledger)
				if problem.failed() {
					b.Fatal(problem)
				}
				if cow.pool != pool {
					b.Fatal("shared preparation replaced the canonical pool")
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(count), "slots/op")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*count), "ns/slot")
		})
	}
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value != 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

func TestPrivatePagePoolRejectsStaleCrossPoolAndDoubleOwnerTokens(t *testing.T) {
	firstSlots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	secondSlots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	first := testPrivatePagePool(t, firstSlots, 20, 21)
	second := testPrivatePagePool(t, secondSlots, 20, 21)
	checkpoint, _ := first.begin()
	token, problem := first.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	copyOfToken := token
	beforeSecond := second.slots[0]
	if _, problem = second.transfer(checkpoint, token, privatePageOwnerRetirement, privatePageRetirementTree); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("cross-pool transfer = %+v", problem)
	}
	if second.slots[0] != beforeSecond {
		t.Fatal("cross-pool failure mutated the destination pool")
	}
	if _, problem = first.borrow(20, privatePageOwnerRetirement); problem.code != privatePagePoolErrOwnerMismatch {
		t.Fatalf("double-owner borrow = %+v", problem)
	}
	retirement, problem := first.transfer(checkpoint, token, privatePageOwnerRetirement, privatePageRetirementBlob)
	if problem.failed() {
		t.Fatal(problem)
	}
	var contents [PageSize]byte
	if first.readPage(token, &contents).code != privatePagePoolErrStaleToken ||
		first.readPage(copyOfToken, &contents).code != privatePagePoolErrStaleToken ||
		first.readPage(retirement, &contents).failed() {
		t.Fatal("token epoch did not reject stale copies")
	}
	if problem = first.commit(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if problem = first.recycle(retirement); problem.failed() {
		t.Fatal(problem)
	}
	if first.readPage(retirement, &contents).code != privatePagePoolErrStaleToken {
		t.Fatal("released token retained ABA access")
	}
	checkpoint, problem = first.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	newToken, problem := first.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() || first.readPage(newToken, &contents).failed() ||
		first.readPage(retirement, &contents).code != privatePagePoolErrStaleToken {
		t.Fatalf("ABA claim = %+v", problem)
	}
}

func TestPrivatePagePoolCheckpointRollbackIsAtomicAndInvalidatesTokens(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(6, privatePageReclaimed),
		newPrivatePageSlot(20, privatePageAppended),
	}
	pool := testPrivatePagePool(t, slots, 20, 21)
	checkpoint, _ := pool.begin()
	token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() || token.pool.slots[token.slot].pageNumber != 6 {
		t.Fatalf("lowest page claim = %+v", problem)
	}
	var contents [PageSize]byte
	contents[17] = 9
	if problem = pool.writePage(token, &contents); problem.failed() {
		t.Fatal(problem)
	}
	beforeFailure := pool.slots[1]
	wrong := checkpoint
	wrong.id++
	if problem = pool.rollback(wrong); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("stale rollback = %+v", problem)
	}
	if pool.slots[1] != beforeFailure || pool.readPage(token, &contents).failed() {
		t.Fatal("failed rollback mutated pool state")
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}
	if pool.readPage(token, &contents).code != privatePagePoolErrStaleToken ||
		pool.slots[0].state != privatePageAvailable || pool.slots[0].bytes[17] != 0 {
		t.Fatal("rollback retained claimed state or stale authority")
	}
}

func TestPrivatePagePoolRejectsForgedCheckpointGenerationWithoutMutation(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(6, privatePageReclaimed),
		newPrivatePageSlot(20, privatePageAppended),
	}
	pool := testPrivatePagePool(t, slots, 20, 21)
	checkpoint, problem := pool.begin()
	if problem.failed() {
		t.Fatal(problem)
	}
	forged := checkpoint
	forged.generation++
	before := [2]privatePagePoolSlot{pool.slots[0], pool.slots[1]}
	mutationBefore, generationBefore := pool.mutationEpoch, pool.generation
	activeBefore := pool.activeCheckpointID

	if _, problem = pool.claimLowest(forged, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("forged-generation claim = %+v", problem)
	}
	if problem = pool.commit(forged); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("forged-generation commit = %+v", problem)
	}
	if problem = pool.rollback(forged); problem.code != privatePagePoolErrCheckpointInactive {
		t.Fatalf("forged-generation rollback = %+v", problem)
	}
	if pool.slots[0] != before[0] || pool.slots[1] != before[1] ||
		pool.mutationEpoch != mutationBefore || pool.generation != generationBefore ||
		pool.activeCheckpointID != activeBefore {
		t.Fatal("forged checkpoint generation mutated pool or consumed the real checkpoint")
	}

	token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatalf("real checkpoint was not reusable: %+v", problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() {
		t.Fatalf("real checkpoint commit = %+v", problem)
	}
	if token.generation != generationBefore+1 || pool.generation != generationBefore+1 || pool.activeCheckpointID != 0 {
		t.Fatalf("real checkpoint generation = token %d pool %d active %d", token.generation, pool.generation, pool.activeCheckpointID)
	}
}

func TestPrivatePagePoolTransferredPageCannotMutateBeforeCommit(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageCommittedFree)}
	pool := testPrivatePagePool(t, slots, 20, 20)
	first, _ := pool.begin()
	original, problem := pool.claimLowest(first, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	var originalBytes [PageSize]byte
	originalBytes[123] = 0x5a
	if problem = pool.writePage(original, &originalBytes); problem.failed() {
		t.Fatal(problem)
	}
	if problem = pool.commit(first); problem.failed() {
		t.Fatal(problem)
	}
	transferCheckpoint, _ := pool.begin()
	retirement, problem := pool.transfer(
		transferCheckpoint, original, privatePageOwnerRetirement, privatePageRetirementTree,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	mutated := originalBytes
	mutated[123] = 0xff
	before := pool.slots[0]
	if problem = pool.writePage(retirement, &mutated); problem.code != privatePagePoolErrTransferPending {
		t.Fatalf("pending transfer write = %+v", problem)
	}
	if pool.slots[0] != before {
		t.Fatal("rejected pending-transfer write changed physical bytes")
	}
	if problem = pool.recycle(retirement); problem.failed() {
		t.Fatalf("pending transfer release = %+v", problem)
	}
	if pool.slots[0].state != privatePagePendingReturn || pool.slots[0].bytes != before.bytes {
		t.Fatal("pending return destroyed bytes before checkpoint outcome")
	}
	if problem = pool.rollback(transferCheckpoint); problem.failed() {
		t.Fatal(problem)
	}
	restored, problem := pool.borrow(7, privatePageOwnerBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	var restoredBytes [PageSize]byte
	if problem = pool.readPage(restored, &restoredBytes); problem.failed() || restoredBytes != originalBytes {
		t.Fatal("transfer rollback returned corrupted bytes to prior owner")
	}
}

func TestPrivatePagePoolReleasedPagesKeepExactReuseAuthority(t *testing.T) {
	slots := []privatePagePoolSlot{
		newPrivatePageSlot(7, privatePageCommittedFree),
		newPrivatePageSlot(8, privatePageReclaimed),
		newPrivatePageSlot(20, privatePageAppended),
	}
	pool := testPrivatePagePool(t, slots, 20, 21)
	checkpoint, _ := pool.begin()
	for expectedPage := uint32(7); expectedPage <= 8; expectedPage++ {
		token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() || pool.slots[token.slot].pageNumber != expectedPage {
			t.Fatalf("claim %d = %+v", expectedPage, problem)
		}
		if problem = pool.returnReleased(token); problem.failed() {
			t.Fatal(problem)
		}
		if pool.slots[token.slot].state != privatePageReleasedFree {
			t.Fatal("committed authority did not return as free")
		}
		if problem = pool.commit(checkpoint); problem.failed() {
			t.Fatal(problem)
		}
		checkpoint, problem = pool.begin()
		if problem.failed() {
			t.Fatal(problem)
		}
	}
	appended, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() || pool.slots[appended.slot].pageNumber != 20 {
		t.Fatalf("appended claim = %+v", problem)
	}
	if problem = pool.returnReleased(appended); problem.failed() || pool.slots[appended.slot].state != privatePageReleasedTail {
		t.Fatalf("appended release = %+v", problem)
	}
	if problem = pool.commit(checkpoint); problem.failed() || pool.available() != 0 {
		t.Fatalf("returned pages remained reusable: %+v available=%d", problem, pool.available())
	}
	checkpoint, _ = pool.begin()
	if _, problem = pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap); problem.code != privatePagePoolErrBudget {
		t.Fatalf("returned page was silently reclaimed: %+v", problem)
	}
	if problem = pool.rollback(checkpoint); problem.failed() {
		t.Fatal(problem)
	}

	// A later transaction reconstructs authority from its selected committed
	// state; the old draft never flips a returned slot back to Available.
	nextSlots := []privatePagePoolSlot{newPrivatePageSlot(7, privatePageCommittedFree)}
	next := testPrivatePagePool(t, nextSlots, 20, 20)
	nextCheckpoint, _ := next.begin()
	nextToken, problem := next.claimLowest(nextCheckpoint, privatePageOwnerRetirement, privatePageRetirementTree)
	if problem.failed() || next.slots[nextToken.slot].pageNumber != 7 {
		t.Fatalf("later transaction authorization = %+v", problem)
	}
}

func TestPrivatePagePoolGenerationReleaseRejectsActiveCheckpoint(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	pool := testPrivatePagePool(t, slots, 20, 21)
	checkpoint, _ := pool.begin()
	token, problem := pool.claimLowest(checkpoint, privatePageOwnerRetirement, privatePageRetirementBlob)
	if problem.failed() {
		t.Fatal(problem)
	}
	var contents [PageSize]byte
	contents[7] = 9
	if problem = pool.writePage(token, &contents); problem.failed() {
		t.Fatal(problem)
	}
	before := pool.slots[0]
	if problem = pool.releaseGeneration(token.generation, privatePageOwnerRetirement, privatePageRetirementBlob); problem.code != privatePagePoolErrCheckpointActive {
		t.Fatalf("active-checkpoint generation release = %+v", problem)
	}
	if pool.slots[0] != before {
		t.Fatal("rejected generation release mutated pool")
	}
}

func TestPrivatePagePoolIncarnationRejectsSameAddressReinitialization(t *testing.T) {
	firstSlots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	pool := testPrivatePagePool(t, firstSlots, 20, 21)
	checkpoint, _ := pool.begin()
	stale, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
	if problem.failed() {
		t.Fatal(problem)
	}
	secondSlots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	reinitialized := testPrivatePagePool(t, secondSlots, 20, 21)
	pool = reinitialized
	var destination [PageSize]byte
	if problem = pool.readPage(stale, &destination); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("same-address stale token = %+v", problem)
	}
	if problem = pool.rollback(checkpoint); problem.code != privatePagePoolErrCrossPool {
		t.Fatalf("same-address stale checkpoint = %+v", problem)
	}
}

func TestPrivatePagePoolAvailabilityIndexScalesAndStaysOrdered(t *testing.T) {
	for _, count := range []int{512, 4096} {
		slots := make([]privatePagePoolSlot, count)
		for index := range slots {
			// Deliberately reverse physical slot order; the caller-owned AVL index
			// still selects exact lowest page numbers.
			slots[index] = newPrivatePageSlot(uint32(2+count-1-index), privatePageReclaimed)
		}
		pool := testPrivatePagePool(t, slots, uint64(count+2), uint64(count+2))
		checkpoint, problem := pool.begin()
		if problem.failed() {
			t.Fatal(problem)
		}
		for expected := uint32(2); expected < uint32(count+2); expected++ {
			token, current := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
			if current.failed() || pool.slots[token.slot].pageNumber != expected {
				t.Fatalf("count=%d expected=%d problem=%+v", count, expected, current)
			}
		}
		if pool.available() != 0 || pool.inUseCount() != count {
			t.Fatalf("count=%d available=%d in-use=%d", count, pool.available(), pool.inUseCount())
		}
		if problem = pool.rollback(checkpoint); problem.failed() || pool.available() != count {
			t.Fatalf("count=%d rollback=%+v available=%d", count, problem, pool.available())
		}
	}
}

func TestPrivatePagePoolHotPathAllocatesNothing(t *testing.T) {
	slots := []privatePagePoolSlot{newPrivatePageSlot(20, privatePageAppended)}
	pool := testPrivatePagePool(t, slots, 20, 21)
	allocations := testing.AllocsPerRun(100, func() {
		checkpoint, problem := pool.begin()
		if problem.failed() {
			panic(problem)
		}
		token, problem := pool.claimLowest(checkpoint, privatePageOwnerBitmap, privatePageBitmap)
		if problem.failed() {
			panic(problem)
		}
		var contents [PageSize]byte
		contents[31] = 1
		if problem = pool.writePage(token, &contents); problem.failed() {
			panic(problem)
		}
		if problem = pool.recycle(token); problem.failed() {
			panic(problem)
		}
		if problem = pool.commit(checkpoint); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 {
		t.Fatalf("pool hot path allocations = %f", allocations)
	}
}
