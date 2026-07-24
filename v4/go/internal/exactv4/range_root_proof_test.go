package exactv4

import (
	"errors"
	"slices"
	"testing"
)

type rangeRootProofIndexes struct {
	seedPages   []pageNumberIndexPage
	firstPages  []pageNumberIndexPage
	secondPages []pageNumberIndexPage

	seedWorkspace   pageNumberIndexWorkspace
	firstWorkspace  pageNumberIndexWorkspace
	secondWorkspace pageNumberIndexWorkspace

	seed   pageNumberIndex
	first  pageNumberIndex
	second pageNumberIndex
}

func newRangeRootProofIndexes(t *testing.T, capacity int) *rangeRootProofIndexes {
	t.Helper()
	indexes := &rangeRootProofIndexes{
		seedPages:   make([]pageNumberIndexPage, capacity),
		firstPages:  make([]pageNumberIndexPage, capacity),
		secondPages: make([]pageNumberIndexPage, capacity),
	}
	indexes.seedWorkspace = newPageNumberIndexWorkspace(indexes.seedPages)
	indexes.firstWorkspace = newPageNumberIndexWorkspace(indexes.firstPages)
	indexes.secondWorkspace = newPageNumberIndexWorkspace(indexes.secondPages)
	var err error
	if indexes.seed, err = newPageNumberIndex(&indexes.seedWorkspace); err != nil {
		t.Fatalf("new range-root proof seed: %v", err)
	}
	if indexes.first, err = newPageNumberIndex(&indexes.firstWorkspace); err != nil {
		t.Fatalf("new range-root proof first candidate: %v", err)
	}
	if indexes.second, err = newPageNumberIndex(&indexes.secondWorkspace); err != nil {
		t.Fatalf("new range-root proof second candidate: %v", err)
	}
	return indexes
}

func (indexes *rangeRootProofIndexes) requireClean(t *testing.T) {
	t.Helper()
	if !indexes.seed.isEmptyAndClean() || !indexes.first.isEmptyAndClean() || !indexes.second.isEmptyAndClean() {
		t.Fatalf("range-root proof scratch was not clean: seed=%d first=%d second=%d",
			indexes.seed.len(), indexes.first.len(), indexes.second.len())
	}
}

func rangeRootProofMaterialized(page uint32) (rangeTreeMaterializedResult, []privateWriterProducedTerminalPage) {
	pages := []privateWriterProducedTerminalPage{
		terminalJournalPage(page, privatePageOwnerRange, privatePageRange),
	}
	return rangeTreeMaterializedResult{
		rootPage: page, recordCount: 1, pageCount: len(pages),
	}, pages
}

func requireRangeRootProofCode(
	t *testing.T,
	err error,
	want rangeRootTransactionProofErrorCode,
) *rangeRootTransactionProofError {
	t.Helper()
	var got *rangeRootTransactionProofError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want range-root proof error: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("range-root proof error = %d, want %d: %#v", got.code, want, got)
	}
	return got
}

func TestRangeRootTransactionProofConvergesOldRangeOwnership(t *testing.T) {
	data, selected := ownershipWalkImage(t)
	materialized, rangePages := rangeRootProofMaterialized(5)
	indexes := newRangeRootProofIndexes(t, 4)
	var ownershipScratch rangeTreeOwnershipScratch
	calls := 0
	proof, err := prepareRangeRootTransactionProof[IPv4](
		newImmutableSlicePageSource(data, selected.PageCount), selected,
		materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 4,
		func(current *pageNumberIndex, additions pageNumberIndexFixedPointAdder) error {
			calls++
			switch current.len() {
			case 4:
				_, addErr := additions.add(6)
				return addErr
			case 5:
				_, addErr := additions.add(7)
				return addErr
			default:
				return nil
			}
		},
	)
	if err != nil {
		t.Fatalf("prepare range-root proof: %v", err)
	}
	if calls != 3 {
		t.Fatalf("preview calls = %d, want 3", calls)
	}
	if got, want := collectPageNumberIndex(t, proof.seed), []uint32{3, 4, 8, 11}; !slices.Equal(got, want) {
		t.Fatalf("old range ownership = %v, want %v", got, want)
	}
	protected, err := proof.protectedIndex()
	if err != nil {
		t.Fatalf("validate range-root proof: %v", err)
	}
	if got, want := collectPageNumberIndex(t, protected), []uint32{3, 4, 6, 7, 8, 11}; !slices.Equal(got, want) {
		t.Fatalf("protected pages = %v, want %v", got, want)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootTransactionProofSupportsLegalEmptySelectedRoot(t *testing.T) {
	selected := rangeOwnershipMeta(12, 0, 0)
	materialized, rangePages := rangeRootProofMaterialized(5)
	indexes := newRangeRootProofIndexes(t, 1)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		newImmutableSlicePageSource(nil, selected.PageCount), selected,
		materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare empty-root proof: %v", err)
	}
	protected, err := proof.protectedIndex()
	if err != nil || protected.len() != 0 || proof.seed.len() != 0 {
		t.Fatalf("empty-root proof = protected:%d seed:%d error:%v", protected.len(), proof.seed.len(), err)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootTransactionProofBindsSelectedRetirementIdentity(t *testing.T) {
	selected := rangeOwnershipMeta(12, 0, 0)
	selected.RetirementRoot = 6
	selected.RetirementBatchCount = 1
	source := newImmutableSlicePageSource(nil, selected.PageCount)
	materialized, rangePages := rangeRootProofMaterialized(5)
	indexes := newRangeRootProofIndexes(t, 1)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		source, selected, materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare range-root proof: %v", err)
	}
	state, protected, err := proof.retirementInputs()
	if err != nil || state.selectedTxn != selected.TxnID || state.pageCount != selected.PageCount ||
		state.root != selected.RetirementRoot || state.batchCount != selected.RetirementBatchCount ||
		protected.len() != 0 {
		t.Fatalf("retirement inputs = state:%+v protected:%v error:%v", state, protected, err)
	}

	proof.selected.retirementRoot = 7
	if _, err = proof.protectedIndex(); err == nil {
		t.Fatal("accepted proof after selected retirement identity substitution")
	} else {
		requireRangeRootProofCode(t, err, rangeRootTransactionProofErrStale)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootTransactionProofRejectsInvalidSelectedRetirementIdentity(t *testing.T) {
	base := rangeOwnershipMeta(12, 0, 0)
	for _, test := range []struct {
		name   string
		mutate func(*Meta)
	}{
		{
			name: "zero root with batches",
			mutate: func(meta *Meta) {
				meta.RetirementBatchCount = 1
			},
		},
		{
			name: "root without batches",
			mutate: func(meta *Meta) {
				meta.RetirementRoot = 2
			},
		},
		{
			name: "root outside selected extent",
			mutate: func(meta *Meta) {
				meta.RetirementRoot = uint32(meta.PageCount)
				meta.RetirementBatchCount = 1
			},
		},
		{
			name: "more batches than selected transaction allows",
			mutate: func(meta *Meta) {
				meta.RetirementRoot = 2
				meta.RetirementBatchCount = meta.TxnID
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := base
			test.mutate(&selected)
			if _, err := rangeRootTransactionIdentityFromMeta(selected); err == nil {
				t.Fatal("accepted invalid selected retirement identity")
			} else {
				requireRangeRootProofCode(t, err, rangeRootTransactionProofErrSelectedIdentity)
			}
		})
	}
}

func TestRangeRootTransactionProofRejectsJournalAndProtectedOverlap(t *testing.T) {
	t.Run("journal", func(t *testing.T) {
		selected := rangeOwnershipMeta(12, 0, 0)
		indexes := newRangeRootProofIndexes(t, 1)
		var ownershipScratch rangeTreeOwnershipScratch
		materialized, rangePages := rangeRootProofMaterialized(5)
		materialized.rootPage = 6
		_, err := prepareRangeRootTransactionProof[IPv4](
			newImmutableSlicePageSource(nil, selected.PageCount), selected,
			materialized, rangePages,
			&indexes.seed, &indexes.first, &indexes.second,
			&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
		)
		requireRangeRootProofCode(t, err, rangeRootTransactionProofErrRangeRoot)
		indexes.requireClean(t)

		materialized, rangePages = rangeRootProofMaterialized(5)
		materialized.pageCount = 0
		_, err = prepareRangeRootTransactionProof[IPv4](
			newImmutableSlicePageSource(nil, selected.PageCount), selected,
			materialized, rangePages,
			&indexes.seed, &indexes.first, &indexes.second,
			&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
		)
		requireRangeRootProofCode(t, err, rangeRootTransactionProofErrRangeJournal)
		indexes.requireClean(t)

		materialized, rangePages = rangeRootProofMaterialized(5)
		rangePages[0].owner = privatePageOwnerBitmap
		rangePages[0].origin = privatePageBitmap
		_, err = prepareRangeRootTransactionProof[IPv4](
			newImmutableSlicePageSource(nil, selected.PageCount), selected,
			materialized, rangePages,
			&indexes.seed, &indexes.first, &indexes.second,
			&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
		)
		requireRangeRootProofCode(t, err, rangeRootTransactionProofErrRangeJournal)
		indexes.requireClean(t)
	})

	t.Run("overlap", func(t *testing.T) {
		data, selected := ownershipWalkImage(t)
		materialized, rangePages := rangeRootProofMaterialized(3)
		indexes := newRangeRootProofIndexes(t, 4)
		var ownershipScratch rangeTreeOwnershipScratch
		_, err := prepareRangeRootTransactionProof[IPv4](
			newImmutableSlicePageSource(data, selected.PageCount), selected,
			materialized, rangePages,
			&indexes.seed, &indexes.first, &indexes.second,
			&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
		)
		problem := requireRangeRootProofCode(t, err, rangeRootTransactionProofErrProtectedOverlap)
		if problem.page != 3 {
			t.Fatalf("overlap page = %d, want 3", problem.page)
		}
		indexes.requireClean(t)
	})
}

func TestRangeRootTransactionProofFailureCleansScratchAndRejectsStaleState(t *testing.T) {
	data, selected := ownershipWalkImage(t)
	materialized, rangePages := rangeRootProofMaterialized(5)
	indexes := newRangeRootProofIndexes(t, 4)
	var ownershipScratch rangeTreeOwnershipScratch
	_, err := prepareRangeRootTransactionProof[IPv4](
		newImmutableSlicePageSource(data[:9*PageSize], selected.PageCount), selected,
		materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	requireRangeRootProofCode(t, err, rangeRootTransactionProofErrOwnership)
	indexes.requireClean(t)

	stop := errors.New("preview stopped")
	_, err = prepareRangeRootTransactionProof[IPv4](
		newImmutableSlicePageSource(data, selected.PageCount), selected,
		materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 1,
		func(_ *pageNumberIndex, _ pageNumberIndexFixedPointAdder) error { return stop },
	)
	problem := requireRangeRootProofCode(t, err, rangeRootTransactionProofErrFixedPoint)
	if !errors.Is(problem, stop) {
		t.Fatalf("preview error was not preserved: %v", problem)
	}
	indexes.requireClean(t)

	proof, err := prepareRangeRootTransactionProof[IPv4](
		newImmutableSlicePageSource(data, selected.PageCount), selected,
		materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("retry range-root proof: %v", err)
	}
	protected, err := proof.protectedIndex()
	if err != nil {
		t.Fatalf("initial proof validation: %v", err)
	}
	if inserted, insertErr := protected.insert(6); insertErr != nil || !inserted {
		t.Fatalf("mutate proof candidate: inserted=%v error=%v", inserted, insertErr)
	}
	if _, err = proof.protectedIndex(); err == nil {
		t.Fatal("accepted stale range-root proof")
	} else {
		requireRangeRootProofCode(t, err, rangeRootTransactionProofErrStale)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootTransactionProofUsesNoHeapAfterSetup(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	data, selected := ownershipWalkImage(t)
	materialized, rangePages := rangeRootProofMaterialized(5)
	indexes := newRangeRootProofIndexes(t, 4)
	var ownershipScratch rangeTreeOwnershipScratch
	source := newImmutableSlicePageSource(data, selected.PageCount)
	run := func() {
		proof, err := prepareRangeRootTransactionProof[IPv4](
			source, selected, materialized, rangePages,
			&indexes.seed, &indexes.first, &indexes.second,
			&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
		)
		if err != nil {
			t.Fatalf("prepare allocation proof: %v", err)
		}
		if _, err = proof.protectedIndex(); err != nil {
			t.Fatalf("validate allocation proof: %v", err)
		}
		proof.discardAfterAbort()
	}
	run()
	allocations := testing.AllocsPerRun(20, run)
	if allocations != 0 {
		t.Fatalf("range-root proof allocations = %v, want 0", allocations)
	}
	indexes.requireClean(t)
}

func newRangeRootRetirementStageScratch() rangeRootRetirementStageScratch {
	return rangeRootRetirementStageScratch{
		blobPages:     make([]uint32, 1),
		path:          make([]retirementPathFrame, retirementWriterPathCapacity),
		blobScanPages: make([]retirementBlobScanPage, 4),
		replacements:  make([]committedPageReplacement, 8),
		releases:      make([]uint32, 8),
		roles:         make([]pageRoleIndexSlot, 16),
	}
}

func prepareRangeRootRetirementStageFixture(t *testing.T) (
	*freeBitmapReservationAttachment,
	*rangeRootTransactionProof,
	*rangeRootProofIndexes,
) {
	return prepareRangeRootRetirementStageFixtureWithPayload(t, 3)
}

func prepareRangeRootRetirementStageFixtureWithPayload(t *testing.T, payload int) (
	*freeBitmapReservationAttachment,
	*rangeRootTransactionProof,
	*rangeRootProofIndexes,
) {
	t.Helper()
	data, selected := ownershipWalkImage(t)
	selected.TxnID = 1
	source := newImmutableSlicePageSource(data, selected.PageCount)
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(t, source, selected.PageCount, 0, payload, &storage)
	bitmapProof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&bitmapProof); problem.failed() {
		t.Fatal(problem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	rangePages := make([]privateWriterProducedTerminalPage, staged.pageCount)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: rangePages,
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	indexes := newRangeRootProofIndexes(t, 4)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		source, selected, materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 4, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare range-root retirement proof: %v", err)
	}
	return &attachment, &proof, indexes
}

func TestRangeRootRetirementStageConsumesProofInsideBoundScope(t *testing.T) {
	attachment, proof, indexes := prepareRangeRootRetirementStageFixture(t)
	scratch := newRangeRootRetirementStageScratch()
	stage, problem := stageRangeRootRetirement(attachment, proof, &scratch)
	if problem.failed() {
		t.Fatalf("stage range-root retirement: %#v", problem)
	}
	if stage.retirement.root < 2 || stage.retirement.batchCount != 1 ||
		stage.blobPages != 1 || stage.retirement.privatePages != 1 || stage.terminalPages != 2 ||
		stage.protectedLen != 4 {
		t.Fatalf("retirement stage = %#v", stage)
	}
	if problem = stage.verify(); problem.failed() {
		t.Fatalf("verify staged retirement: %#v", problem)
	}
	member, capacity, poolProblem := attachment.cow.pool.scopeMemberStart(attachment.scope)
	if poolProblem.failed() || capacity != attachment.privatePages {
		t.Fatalf("scope members = member:%d capacity:%d problem:%#v", member, capacity, poolProblem)
	}
	rangePages, retirementPages := 0, 0
	foundRoot := false
	for visited := 0; member != privatePagePoolNoIndex; visited++ {
		if visited >= capacity {
			t.Fatal("scope member walk did not terminate")
		}
		slot := &attachment.cow.pool.slots[member]
		switch slot.owner {
		case privatePageOwnerRange:
			rangePages++
		case privatePageOwnerRetirement:
			retirementPages++
			foundRoot = foundRoot || slot.pageNumber == stage.retirement.root &&
				slot.origin == privatePageRetirementTree
		}
		member = slot.scopeMemberNext
	}
	if rangePages != 1 || retirementPages != stage.terminalPages || !foundRoot {
		t.Fatalf("scope ownership = range:%d retirement:%d root:%v", rangePages, retirementPages, foundRoot)
	}
	attachment.cow.pool.abortRequired = true
	if verify := stage.verify(); verify.code != rangeRootRetirementStageErrPostMutationBitmap ||
		!verify.discardRequired() {
		t.Fatalf("poisoned stage verify = %#v", verify)
	}
	stage.discardAfterAbort()
	indexes.requireClean(t)
}
func TestRangeRootRetirementStageRejectsShortScratchBeforeMutationAndRetries(t *testing.T) {
	attachment, proof, indexes := prepareRangeRootRetirementStageFixture(t)
	short := newRangeRootRetirementStageScratch()
	short.blobPages = short.blobPages[:0]
	before := snapshotLateBitmapLive(t, attachment)
	_, problem := stageRangeRootRetirement(attachment, proof, &short)
	if problem.code != rangeRootRetirementStageErrPreMutationRetirement ||
		problem.retirement.code != retirementWriteErrBlobBuildScratchTooSmall || problem.discardRequired() {
		t.Fatalf("short blob scratch = %#v", problem)
	}
	requireLateBitmapLiveSnapshot(t, attachment, before)

	retry := newRangeRootRetirementStageScratch()
	stage, problem := stageRangeRootRetirement(attachment, proof, &retry)
	if problem.failed() {
		t.Fatalf("retry stage = %#v", problem)
	}
	stage.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootRetirementStagePoisonsDraftAfterBlobMutation(t *testing.T) {
	// One range payload page and one blob page fit, but the retirement-tree
	// page does not. The blob has already entered the shared scope, so this
	// is a whole-draft abort rather than a retryable capacity error.
	attachment, proof, indexes := prepareRangeRootRetirementStageFixtureWithPayload(t, 2)
	scratch := newRangeRootRetirementStageScratch()
	_, problem := stageRangeRootRetirement(attachment, proof, &scratch)
	if problem.code != rangeRootRetirementStageErrPostMutationRetirement ||
		problem.retirement.code != retirementWriteErrPrivatePageBudgetTooSmall || !problem.discardRequired() ||
		!attachment.cow.pool.abortRequired {
		t.Fatalf("post-blob capacity failure = %#v abort=%v", problem, attachment.cow.pool.abortRequired)
	}
	_, retry := stageRangeRootRetirement(attachment, proof, &scratch)
	if retry.code != rangeRootRetirementStageErrPostMutationBitmap || !retry.discardRequired() ||
		retry.bitmap.code != freeBitmapCOWErrArenaPageConflict {
		t.Fatalf("poisoned draft retry = %#v", retry)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootRetirementStageRejectsProofSubstitutionBeforeMutation(t *testing.T) {
	attachment, proof, indexes := prepareRangeRootRetirementStageFixture(t)
	before := snapshotLateBitmapLive(t, attachment)
	proof.selected.retirementRoot = 2
	scratch := newRangeRootRetirementStageScratch()
	_, problem := stageRangeRootRetirement(attachment, proof, &scratch)
	if problem.code != rangeRootRetirementStageErrPreMutationProof || problem.discardRequired() {
		t.Fatalf("proof substitution = %#v", problem)
	}
	requireLateBitmapLiveSnapshot(t, attachment, before)
	proof.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootRetirementStageSupportsLegalEmptySelectedRoot(t *testing.T) {
	selected := rangeOwnershipMeta(12, 0, 0)
	selected.TxnID = 1
	source := newImmutableSlicePageSource(nil, selected.PageCount)
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlanAt(t, source, selected.PageCount, 0, 1, &storage)
	bitmapProof := completeLateBitmapProof(t, &attachment, 0, nil)
	if _, problem := attachment.bind(&bitmapProof); problem.failed() {
		t.Fatal(problem)
	}
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	rangePages := make([]privateWriterProducedTerminalPage, staged.pageCount)
	materialized, stageProblem := stageRangePayload(
		&attachment, &staging, staged,
		&rangeTreePayloadScratch{
			assignments:   make([]rangeTreePhysicalAssignment, staged.pageCount),
			slots:         make([]rangeTreePayloadReservationSlot, staged.pageCount),
			terminalPages: rangePages,
		},
	)
	if stageProblem.failed() {
		t.Fatal(stageProblem)
	}
	indexes := newRangeRootProofIndexes(t, 1)
	var ownershipScratch rangeTreeOwnershipScratch
	proof, err := prepareRangeRootTransactionProof[IPv4](
		source, selected, materialized, rangePages,
		&indexes.seed, &indexes.first, &indexes.second,
		&ownershipScratch, 1, 1, pageNumberIndexNoOpFixedPointPreview,
	)
	if err != nil {
		t.Fatalf("prepare empty range-root proof: %v", err)
	}
	before := snapshotLateBitmapLive(t, &attachment)
	scratch := newRangeRootRetirementStageScratch()
	stage, problem := stageRangeRootRetirement(&attachment, &proof, &scratch)
	if problem.failed() || stage.terminalPages != 0 || stage.retirement.root != 0 ||
		stage.retirement.batchCount != 0 || stage.protectedLen != 0 {
		t.Fatalf("empty stage = %#v/%#v", stage, problem)
	}
	requireLateBitmapLiveSnapshot(t, &attachment, before)
	if problem = stage.verify(); problem.failed() {
		t.Fatalf("verify empty stage = %#v", problem)
	}
	stage.discardAfterAbort()
	indexes.requireClean(t)
}

func TestRangeRootRetirementStageUsesNoHeapAfterSetup(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	attachment, proof, indexes := prepareRangeRootRetirementStageFixture(t)
	scratch := newRangeRootRetirementStageScratch()
	pool := attachment.cow.pool
	baselinePool := *pool
	baselineSlots := append([]privatePagePoolSlot(nil), pool.slots...)
	baselineCOW := attachment.cow
	baselineBindings := append([]bitmapCOWArenaBinding(nil), attachment.cow.arenaBindings...)
	baselineIndexNodes := append([]bitmapCOWIndexNode(nil), attachment.cow.indexNodes...)
	baselineAvailable := append([]int(nil), attachment.cow.availableSlots...)
	restore := func() {
		copy(pool.slots, baselineSlots)
		*pool = baselinePool
		copy(attachment.cow.arenaBindings, baselineBindings)
		copy(attachment.cow.indexNodes, baselineIndexNodes)
		copy(attachment.cow.availableSlots, baselineAvailable)
		attachment.cow = baselineCOW
		clear(scratch.blobPages)
		clear(scratch.path)
		clear(scratch.blobScanPages)
		clear(scratch.replacements)
		clear(scratch.releases)
		clear(scratch.roles)
		scratch.arena = privatePageArena{}
		scratch.token = retirementBlobToken{}
		scratch.blobScan = retirementBlobScanScratch{}
		scratch.replacementLedger = committedReplacementLedger{}
		scratch.releaseBuffer = privateReleaseBuffer{}
		scratch.roleIndex = pageRoleIndex{}
		scratch.guard = guardedRetirementSource{}
	}
	run := func() {
		stage, problem := stageRangeRootRetirement(attachment, proof, &scratch)
		if problem.failed() {
			panic(problem)
		}
		if problem = stage.verify(); problem.failed() {
			panic(problem)
		}
		restore()
	}
	run()
	allocations := testing.AllocsPerRun(20, run)
	restore()
	if allocations != 0 {
		t.Fatalf("range-root retirement stage allocations = %v, want zero", allocations)
	}
	proof.discardAfterAbort()
	indexes.requireClean(t)
}
