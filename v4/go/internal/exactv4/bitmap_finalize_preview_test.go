package exactv4

import (
	"reflect"
	"slices"
	"testing"
)

type selectiveFinalizationPreviewStageWitness struct {
	rootPage         uint32
	pageCount        int
	terminalPage     uint32
	remainingPayload int
}

type selectiveFinalizationPreviewStageFailure struct{}

func (selectiveFinalizationPreviewStageFailure) Error() string {
	return "selective finalization preview stage failed"
}

func noOpSelectiveFinalizationPreviewStage(*freeBitmapFinalizationDetachedStage) (struct{}, error) {
	return struct{}{}, nil
}

func requireSelectiveFinalizationPreviewStageClear(
	t *testing.T,
	scratch freeBitmapFinalizationScratch,
) {
	t.Helper()
	if scratch.stage == nil || !reflect.DeepEqual(*scratch.stage, freeBitmapFinalizationDetachedStage{}) {
		t.Fatal("preview left detached stage scratch dirty")
	}
}

func requireSelectiveFinalizationPreviewStageDetached(
	t *testing.T,
	stage *freeBitmapFinalizationDetachedStage,
) {
	t.Helper()
	if stage == nil {
		t.Fatal("preview stage is nil")
	}
	attachment := &stage.attachment
	if attachment.committed != nil || attachment.draft != nil || attachment.selectedTxn != 0 ||
		attachment.buffers.pool != nil || attachment.buffers.reclamation != nil ||
		len(attachment.buffers.poolValidation) != 0 || len(attachment.buffers.arena) != 0 ||
		len(attachment.buffers.arenaBindings) != 0 || len(attachment.buffers.candidates) != 0 ||
		len(attachment.buffers.verifiedPages) != 0 || len(attachment.buffers.replacements) != 0 ||
		len(attachment.buffers.indexNodes) != 0 || len(attachment.buffers.availableSlots) != 0 ||
		len(attachment.buffers.sourceNodes) != 0 || attachment.reclamationRequest.ticket != nil {
		t.Fatal("preview stage exposes live reservation storage")
	}
}

func stageSelectiveFinalizationPreviewRangePayload(
	t *testing.T,
	stage *freeBitmapFinalizationDetachedStage,
	staging *rangeTreeStaging[IPv4],
	staged rangeTreeStagedResult,
	scratch *rangeTreePayloadScratch,
) (selectiveFinalizationPreviewStageWitness, error) {
	t.Helper()
	if stage == nil || staging == nil || scratch == nil {
		t.Fatal("stage range payload input is nil")
	}
	requireSelectiveFinalizationPreviewStageDetached(t, stage)
	clear(scratch.assignments)
	clear(scratch.slots)
	clear(scratch.terminalPages)
	materialized, problem := stageRangePayload(
		&stage.attachment, staging, staged, scratch,
	)
	if problem.failed() {
		t.Fatalf("stage range payload = %#v", problem)
	}
	if materialized.pageCount != 1 || materialized.rootPage == 0 || len(scratch.terminalPages) != 1 ||
		scratch.terminalPages[0].pageNumber != materialized.rootPage ||
		scratch.terminalPages[0].owner != privatePageOwnerRange ||
		scratch.terminalPages[0].origin != privatePageRange {
		t.Fatalf("stage range payload = %#v/%#v", materialized, scratch.terminalPages)
	}
	return selectiveFinalizationPreviewStageWitness{
		rootPage: materialized.rootPage, pageCount: materialized.pageCount,
		terminalPage:     scratch.terminalPages[0].pageNumber,
		remainingPayload: stage.attachment.cow.payloadPageBudget,
	}, nil
}

func TestSelectiveFinalizationPreviewMatchesFinalizedReplacementPages(t *testing.T) {
	preview := newSelectiveForeignFinalizationFixture(t)
	if len(preview.attachment.cow.replacements) == 0 {
		t.Fatal("fixture has no replacement scratch")
	}
	scratch := finalizationScratchForAttachment(&preview.attachment)
	output := make([]uint32, len(preview.attachment.cow.replacements))
	beforeOutput := append([]uint32(nil), output...)
	beforePool := sealFreeBitmapReservationPool(preview.attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(
		preview.attachment.cow.pool, preview.attachment.scope,
	)
	beforeCOW := freeBitmapReservationCOWFingerprint(&preview.attachment.cow)

	length, problem := preview.attachment.previewTerminalReplacements(scratch, output)
	if problem.failed() {
		t.Fatalf("preview = %#v", problem)
	}
	if length <= 0 || length > len(output) {
		t.Fatalf("preview length = %d, output = %d", length, len(output))
	}
	if !beforePool.matches(preview.attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(preview.attachment.cow.pool, preview.attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&preview.attachment.cow) {
		t.Fatal("read-only preview changed the live bitmap reservation")
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		t.Fatal("preview left cleanup scratch dirty")
	}
	requireSelectiveFinalizationPreviewStageClear(t, scratch)
	if reflect.DeepEqual(output, beforeOutput) {
		t.Fatal("preview did not write its successful output")
	}

	expected := newSelectiveForeignFinalizationFixture(t)
	if _, finalizeProblem := expected.attachment.finalize(
		finalizationScratchForAttachment(&expected.attachment),
	); finalizeProblem.failed() {
		t.Fatalf("finalize = %#v", finalizeProblem)
	}
	want := expected.attachment.cow.replacementPages()
	if !reflect.DeepEqual(output[:length], want) {
		t.Fatalf("preview replacements = %v, want %v", output[:length], want)
	}
}

func TestSelectiveFinalizationPreviewStageStagesRangePayloadInBothPasses(t *testing.T) {
	attachment := newFinalizationScratchFixture(t)
	staging, staged := buildRangePayloadV4(t, attachment.cow.pendingTxn)
	payloadScratch := rangeTreePayloadScratch{
		assignments:   make([]rangeTreePhysicalAssignment, 1),
		slots:         make([]rangeTreePayloadReservationSlot, 1),
		terminalPages: make([]privateWriterProducedTerminalPage, 1),
	}
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)
	calls := 0
	length, problem := previewTerminalReplacementsWithStage(
		&attachment, scratch, output,
		func(stage *freeBitmapFinalizationDetachedStage) (selectiveFinalizationPreviewStageWitness, error) {
			calls++
			return stageSelectiveFinalizationPreviewRangePayload(
				t, stage, &staging, staged, &payloadScratch,
			)
		},
	)
	if problem.failed() {
		t.Fatalf("staged preview = %#v", problem)
	}
	if calls != 2 {
		t.Fatalf("stage calls = %d, want 2", calls)
	}
	if length <= 0 || length > len(output) || payloadScratch.terminalPages[0].pageNumber == 0 ||
		payloadScratch.terminalPages[0].owner != privatePageOwnerRange {
		t.Fatalf("staged preview output = %v/%#v", output[:length], payloadScratch.terminalPages[0])
	}
	if !beforePool.matches(attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&attachment.cow) {
		t.Fatal("staged preview changed the live bitmap reservation")
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		t.Fatal("staged preview left cleanup scratch dirty")
	}
	requireSelectiveFinalizationPreviewStageClear(t, scratch)
}

func TestSelectiveFinalizationPreviewStageRejectsUnstableWitnessWithoutMutation(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	for index := range output {
		output[index] = ^uint32(0)
	}
	beforeOutput := append([]uint32(nil), output...)
	beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)
	calls := 0

	_, problem := previewTerminalReplacementsWithStage(
		&attachment, scratch, output,
		func(*freeBitmapFinalizationDetachedStage) (uint8, error) {
			calls++
			return uint8(calls), nil
		},
	)
	if problem.code != freeBitmapCOWErrStaleInsertionPlan || problem.stage != nil {
		t.Fatalf("unstable stage = %#v", problem)
	}
	if calls != 2 {
		t.Fatalf("stage calls = %d, want 2", calls)
	}
	if !slices.Equal(output, beforeOutput) || !beforePool.matches(attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&attachment.cow) {
		t.Fatal("unstable stage changed caller output or live bitmap state")
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		t.Fatal("unstable stage left cleanup scratch dirty")
	}
	requireSelectiveFinalizationPreviewStageClear(t, scratch)
	if _, retryProblem := attachment.previewTerminalReplacements(scratch, output); retryProblem.failed() {
		t.Fatalf("retry after unstable stage = %#v", retryProblem)
	}
}

func TestSelectiveFinalizationPreviewStageFailureLeavesLiveStateAndScratchReusable(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	for index := range output {
		output[index] = ^uint32(0)
	}
	beforeOutput := append([]uint32(nil), output...)
	beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)
	calls := 0

	_, problem := previewTerminalReplacementsWithStage(
		&attachment, scratch, output,
		func(*freeBitmapFinalizationDetachedStage) (struct{}, error) {
			calls++
			return struct{}{}, selectiveFinalizationPreviewStageFailure{}
		},
	)
	if _, ok := problem.stage.(selectiveFinalizationPreviewStageFailure); !ok || problem.freeBitmapCOWError.failed() {
		t.Fatalf("stage failure = %#v", problem)
	}
	if calls != 1 {
		t.Fatalf("stage calls = %d, want 1", calls)
	}
	if !slices.Equal(output, beforeOutput) || !beforePool.matches(attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&attachment.cow) {
		t.Fatal("stage failure changed caller output or live bitmap state")
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		t.Fatal("stage failure left cleanup scratch dirty")
	}
	requireSelectiveFinalizationPreviewStageClear(t, scratch)
	if _, retryProblem := attachment.previewTerminalReplacements(scratch, output); retryProblem.failed() {
		t.Fatalf("retry after stage failure = %#v", retryProblem)
	}
}

func TestSelectiveFinalizationPreviewRejectsOutputCapacityAndAliasesWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected freeBitmapCOWErrorCode
		output   func(*freeBitmapReservationAttachment, freeBitmapFinalizationScratch) []uint32
	}{
		{
			name:     "short",
			expected: freeBitmapCOWErrInsufficientResourceBudget,
			output: func(attachment *freeBitmapReservationAttachment, _ freeBitmapFinalizationScratch) []uint32 {
				return make([]uint32, len(attachment.cow.replacements)-1)
			},
		},
		{
			name:     "release-scratch-alias",
			expected: freeBitmapCOWErrArenaPageConflict,
			output: func(_ *freeBitmapReservationAttachment, scratch freeBitmapFinalizationScratch) []uint32 {
				return scratch.releasePages
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			attachment := newSelectiveForeignFinalizationFixture(t).attachment
			scratch := finalizationScratchForAttachment(&attachment)
			output := test.output(&attachment, scratch)
			beforeOutput := append([]uint32(nil), output...)
			beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
			beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)
			beforeCache := *scratch.cache

			_, problem := attachment.previewTerminalReplacements(scratch, output)
			if problem.code != test.expected {
				t.Fatalf("preview error = %#v", problem)
			}
			outputUnchanged := slices.Equal(output, beforeOutput)
			poolUnchanged := beforePool.matches(attachment.cow.pool)
			scopeUnchanged := beforeScope == freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
			cowUnchanged := beforeCOW == freeBitmapReservationCOWFingerprint(&attachment.cow)
			cacheUnchanged := reflect.DeepEqual(*scratch.cache, beforeCache)
			if !outputUnchanged || !poolUnchanged || !scopeUnchanged || !cowUnchanged || !cacheUnchanged {
				t.Fatalf(
					"rejected preview changed state: output=%t pool=%t scope=%t cow=%t cache=%t",
					outputUnchanged, poolUnchanged, scopeUnchanged, cowUnchanged, cacheUnchanged,
				)
			}
		})
	}
}

func TestSelectiveFinalizationPreviewRequiresCallerOwnedStageScratchWithoutMutation(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	scratch.stage = nil
	output := make([]uint32, len(attachment.cow.replacements))
	for index := range output {
		output[index] = ^uint32(0)
	}
	beforeOutput := append([]uint32(nil), output...)
	beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)

	_, problem := attachment.previewTerminalReplacements(scratch, output)
	if problem.code != freeBitmapCOWErrInsufficientResourceBudget ||
		problem.resource != freeBitmapResourceFinalizationStage || problem.required != 1 || problem.actual != 0 {
		t.Fatalf("missing stage scratch = %#v", problem)
	}
	if !slices.Equal(output, beforeOutput) || !beforePool.matches(attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&attachment.cow) {
		t.Fatal("missing stage scratch changed caller output or live bitmap state")
	}
}

func TestSelectiveFinalizationPreviewSourceFailureLeavesLiveStateAndScratchReusable(t *testing.T) {
	source := &lateFailingAccessSource{
		base: cowSparsePages{pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}},
	}
	storage := newLateBitmapPlannerStorage(16, 16, 16, 32)
	attachment := newLateBitmapPlan(t, source, 2, 2, &storage)
	proof := completeLateBitmapProof(t, &attachment, 911, []uint32{7})
	if _, problem := attachment.bind(&proof); problem.failed() {
		t.Fatal(problem)
	}
	source.failAt = source.checks + 1
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	for index := range output {
		output[index] = ^uint32(0)
	}
	beforeOutput := append([]uint32(nil), output...)
	beforePool := sealFreeBitmapReservationPool(attachment.cow.pool)
	beforeScope := freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope)
	beforeCOW := freeBitmapReservationCOWFingerprint(&attachment.cow)

	if _, problem := attachment.previewTerminalReplacements(scratch, output); problem.code != freeBitmapCOWErrSource ||
		problem.source.code != pageSourceErrForkedHandle {
		t.Fatalf("source failure = %#v", problem)
	}
	if !slices.Equal(output, beforeOutput) || !beforePool.matches(attachment.cow.pool) ||
		beforeScope != freeBitmapReservationScopeFingerprint(attachment.cow.pool, attachment.scope) ||
		beforeCOW != freeBitmapReservationCOWFingerprint(&attachment.cow) {
		t.Fatal("failed preview changed caller output or live bitmap state")
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		t.Fatal("failed preview left cleanup scratch dirty")
	}
	requireSelectiveFinalizationPreviewStageClear(t, scratch)

	source.failAt = 0
	if _, problem := attachment.previewTerminalReplacements(scratch, output); problem.failed() {
		t.Fatalf("retry = %#v", problem)
	}
}

func TestSelectiveFinalizationPreviewUsesNoHeapAfterSetup(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	var problem freeBitmapFinalizationPreviewProblem
	allocations := testing.AllocsPerRun(100, func() {
		_, problem = attachment.previewTerminalReplacements(scratch, output)
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("preview allocations = %g, problem = %#v", allocations, problem)
	}
}

func TestSelectiveFinalizationPreviewStageUsesNoHeapAfterSetup(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	var problem freeBitmapFinalizationPreviewProblem
	allocations := testing.AllocsPerRun(100, func() {
		_, problem = previewTerminalReplacementsWithStage(
			&attachment, scratch, output, noOpSelectiveFinalizationPreviewStage,
		)
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("staged preview allocations = %g, problem = %#v", allocations, problem)
	}
}
