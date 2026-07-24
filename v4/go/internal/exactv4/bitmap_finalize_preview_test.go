package exactv4

import (
	"reflect"
	"slices"
	"testing"
)

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
	if reflect.DeepEqual(output, beforeOutput) {
		t.Fatal("preview did not write its successful output")
	}

	expected := newSelectiveForeignFinalizationFixture(t)
	if _, problem = expected.attachment.finalize(
		finalizationScratchForAttachment(&expected.attachment),
	); problem.failed() {
		t.Fatalf("finalize = %#v", problem)
	}
	want := expected.attachment.cow.replacementPages()
	if !reflect.DeepEqual(output[:length], want) {
		t.Fatalf("preview replacements = %v, want %v", output[:length], want)
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

	source.failAt = 0
	if _, problem := attachment.previewTerminalReplacements(scratch, output); problem.failed() {
		t.Fatalf("retry = %#v", problem)
	}
}

func TestSelectiveFinalizationPreviewUsesNoHeapAfterSetup(t *testing.T) {
	attachment := newSelectiveForeignFinalizationFixture(t).attachment
	scratch := finalizationScratchForAttachment(&attachment)
	output := make([]uint32, len(attachment.cow.replacements))
	var problem freeBitmapCOWError
	allocations := testing.AllocsPerRun(100, func() {
		_, problem = attachment.previewTerminalReplacements(scratch, output)
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("preview allocations = %g, problem = %#v", allocations, problem)
	}
}
