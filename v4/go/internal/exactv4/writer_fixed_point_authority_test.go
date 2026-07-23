package exactv4

import (
	"reflect"
	"testing"
)

type preparedWorkCallbackSource struct {
	*cowSparsePages
	core             *privateWriterTransactionCore
	handle           privateWriterTransactionHandle
	fail             bool
	reenter          bool
	reentryProblem   privateWriterTransactionError
	overrideProgress bool
	progress         privateWriterFixedPointSourceProgress
	calls            int
}

func (s *preparedWorkCallbackSource) nextFixedPointSource(
	current privateWriterCarriedSource,
) (privateWriterFixedPointSourceProgress, pageSourceStatus) {
	s.calls++
	if s.reenter {
		_, _, s.reentryProblem = s.core.consumeFixedPointWork(
			s.handle, privateWriterFixedPointPreparedToken{},
		)
	}
	if s.fail {
		return privateWriterFixedPointSourceProgress{}, pageSourceStatus{code: pageSourceErrIO}
	}
	if status := s.cowSparsePages.checkAccessStatus(); status.failed() {
		return privateWriterFixedPointSourceProgress{}, status
	}
	if s.overrideProgress {
		return s.progress, pageSourceStatus{}
	}
	return privateWriterFixedPointSourceProgress{
		sourceIdentity: current.identity,
		ordinal:        current.ordinal + 1,
		lastPage:       5,
		sourceEpoch:    current.epoch + 1,
	}, pageSourceStatus{}
}

type preparedFixedPointFixture struct {
	core      privateWriterTransactionCore
	handle    privateWriterTransactionHandle
	source    *preparedWorkCallbackSource
	workspace *privateWriterWorkspace
}

func newPreparedFixedPointFixture(t *testing.T) *preparedFixedPointFixture {
	return newPreparedFixedPointFixtureSized(t, 2)
}

func newPreparedFixedPointFixtureSized(
	t *testing.T,
	preparedSlots int,
) *preparedFixedPointFixture {
	return newPreparedFixedPointFixtureCapacity(
		t, preparedSlots, 32, 1<<20,
	)
}

func newPreparedFixedPointFixtureCapacity(
	t *testing.T,
	preparedSlots int,
	privatePages int,
	maxBytes uint64,
) *preparedFixedPointFixture {
	t.Helper()
	fixture := &preparedFixedPointFixture{}
	var workspaceProblem privateWriterWorkspaceError
	fixture.workspace, workspaceProblem = newPrivateWriterWorkspace(
		privateWriterWorkspaceBudget{
			maxBytes: maxBytes, privatePages: privatePages, records: 4, preparedSlots: preparedSlots,
			scratchWordsPerSlot: 8,
		},
		privateWriterResourceBudget{
			maxHeapBytes: maxBytes, maxPrivatePages: uint64(privatePages),
			maxFileGrowthPages: uint64(privatePages), maxOpenFiles: 4,
		},
	)
	if workspaceProblem.failed() {
		t.Fatal(workspaceProblem)
	}
	selected := Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		DatabaseID:    [16]byte{1},
		TxnID:         1,
		CommitNonce:   [16]byte{2},
		PageCount:     20,
	}
	if problem := initPrivateWriterTransactionCoreWithWorkspace(
		&fixture.core,
		selected,
		privateWriterResourceBudget{
			maxHeapBytes: maxBytes, maxPrivatePages: uint64(privatePages),
			maxFileGrowthPages: uint64(privatePages), maxOpenFiles: 4,
		},
		fixture.workspace,
		make([]privateWriterCleanupObligation, 2),
		make([]privateWriterCleanupOwner, 2),
	); problem.failed() {
		t.Fatal(problem)
	}
	var problem privateWriterTransactionError
	fixture.handle, problem = fixture.core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	fixture.source = &preparedWorkCallbackSource{
		cowSparsePages: &cowSparsePages{
			pages: []cowSparsePage{cowLeaf(t, 2, 1, 5, 9)},
		},
		core: &fixture.core, handle: fixture.handle,
	}
	if problem = fixture.core.startPreparedFixedPoint(
		fixture.handle, fixture.source, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	return fixture
}

func (f *preparedFixedPointFixture) request() privateWriterFixedPointPrepareRequest {
	return privateWriterFixedPointPrepareRequest{
		workUnit: 1, expectedRoot: 2, expectedPageCount: 20,
		scopePages: 2,
	}
}

func TestPreparedFixedPointDirectScopeBypassAndStandaloneCompatibility(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	if _, problem := fixture.core.pool.reserveScope(1); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("coordinator bypass = %#v", problem)
	}
	var standalone privatePagePool
	if problem := initVacantPrivatePagePoolForDraft(
		&standalone, make([]privatePagePoolSlot, 2), 20, 20, 2,
	); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem := standalone.reserveScope(1); problem.failed() {
		t.Fatalf("standalone reserve = %#v", problem)
	}
	if _, problem := fixture.core.pool.begin(); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw checkpoint begin = %#v", problem)
	}
	if _, problem := fixture.core.pool.preflightCheckpoint(); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw checkpoint preflight = %#v", problem)
	}
	if _, problem := fixture.core.pool.beginOperation(); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw operation begin = %#v", problem)
	}
	if _, problem := fixture.core.pool.preflightOperation(); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw operation preflight = %#v", problem)
	}
	if _, problem := fixture.core.pool.bindPage(
		privatePagePoolCheckpoint{}, privatePageReservationScope{},
		7, privatePageReclaimed,
	); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw bind = %#v", problem)
	}
	if _, problem := fixture.core.pool.claimPage(
		privatePagePoolCheckpoint{}, 7, privatePageOwnerBitmap, privatePageBitmap,
	); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw claim = %#v", problem)
	}
	if problem := fixture.core.pool.commit(
		privatePagePoolCheckpoint{},
	); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw checkpoint commit = %#v", problem)
	}
	if problem := fixture.core.pool.commitOperation(
		privatePagePoolOperation{},
	); problem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("raw operation commit = %#v", problem)
	}
}

func TestPreparedFixedPointWrongPredecessorAndCallbackFailuresAreNeutral(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	request := fixture.request()
	request.expectedRoot++
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, request,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("wrong root = %#v", problem)
	}
	request = fixture.request()
	request.expectedPageCount++
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, request,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("wrong tail = %#v", problem)
	}
	request = fixture.request()
	fixture.source.fail = true
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, request,
	); problem.code != privateWriterTransactionErrFixedPoint ||
		fixture.core.state != privateWriterTransactionPending {
		t.Fatalf("callback failure = %#v", problem)
	}
}

func TestPreparedFixedPointCopiedTokenSiblingAndAddressCopyAreSingleUse(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	request := fixture.request()
	first, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, request,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	second, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, request,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	slotCopy := *first.slot
	forged := first
	forged.slot = &slotCopy
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, forged,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("address copy = %#v", problem)
	}
	substituted := first
	substituted.slot = second.slot
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, substituted,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("substituted slot = %#v", problem)
	}
	wrongNonce := first
	wrongNonce.nonce++
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, wrongNonce,
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("wrong nonce = %#v", problem)
	}
	other := newPreparedFixedPointFixture(t)
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle,
		privateWriterFixedPointPreparedToken{
			coordinator: other.core.fixedPointPredecessor.coordinator,
			slot:        &other.workspace.preparedSlots[0],
			generation:  1,
			nonce:       1,
		},
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("foreign token = %#v", problem)
	}
	copied := first
	callbackCalls := fixture.source.calls
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, copied,
	); problem.failed() {
		t.Fatalf("copied valid token = %#v", problem)
	}
	if fixture.source.calls != callbackCalls {
		t.Fatal("active consume performed a source callback")
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, first,
	); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("replayed token = %#v", problem)
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, second,
	); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("sibling token = %#v", problem)
	}
}

func TestPreparedFixedPointScratchCursorScopeAndAbortFences(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	fixture.workspace.scratch[0] = 1
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("noncanonical scratch = %#v", problem)
	}
	fixture.workspace.scratch[0] = 0
	current := fixture.core.fixedPointCoordinator.carriedSource()
	fixture.source.overrideProgress = true
	fixture.source.progress = privateWriterFixedPointSourceProgress{
		sourceIdentity: current.identity,
		ordinal:        current.ordinal,
		lastPage:       current.lastPage,
		sourceEpoch:    current.epoch + 1,
	}
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	); problem.code != privateWriterTransactionErrFixedPoint {
		t.Fatalf("cursor regression = %#v", problem)
	}
	fixture.source.overrideProgress = false
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	); problem.failed() {
		t.Fatal(problem)
	}
	if _, _, problem = fixture.core.finishFixedPoint(
		fixture.handle,
	); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("finish with active work = %#v", problem)
	}
	if problem = fixture.core.preflightCommit(
		fixture.handle,
	); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("commit with unaccepted scope = %#v", problem)
	}
}

func TestPreparedFixedPointCursorIsCoordinatorDerivedAndExact(t *testing.T) {
	requestType := reflect.TypeOf(privateWriterFixedPointPrepareRequest{})
	for _, forbidden := range []string{
		"currentSource", "nextSource", "callback",
		"candidatePage", "candidateOrdinal", "candidateCarried",
	} {
		if _, found := requestType.FieldByName(forbidden); found {
			t.Fatalf("caller-controlled cursor field %q remains", forbidden)
		}
	}
	testCases := []struct {
		name   string
		change func(*privateWriterFixedPointSourceProgress)
	}{
		{"identity", func(progress *privateWriterFixedPointSourceProgress) { progress.sourceIdentity++ }},
		{"ordinal omission", func(progress *privateWriterFixedPointSourceProgress) { progress.ordinal++ }},
		{"ordinal regression", func(progress *privateWriterFixedPointSourceProgress) { progress.ordinal = 0 }},
		{"epoch omission", func(progress *privateWriterFixedPointSourceProgress) { progress.sourceEpoch++ }},
		{"epoch regression", func(progress *privateWriterFixedPointSourceProgress) { progress.sourceEpoch = 0 }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreparedFixedPointFixture(t)
			current := fixture.core.fixedPointCoordinator.carriedSource()
			progress := privateWriterFixedPointSourceProgress{
				sourceIdentity: current.identity,
				ordinal:        current.ordinal + 1,
				lastPage:       5,
				sourceEpoch:    current.epoch + 1,
			}
			testCase.change(&progress)
			fixture.source.overrideProgress = true
			fixture.source.progress = progress
			if _, problem := fixture.core.prepareFixedPointWork(
				fixture.handle, fixture.request(),
			); problem.code != privateWriterTransactionErrFixedPoint ||
				fixture.core.state != privateWriterTransactionPending {
				t.Fatalf("cursor rejection = %#v", problem)
			}
		})
	}
	t.Run("page regression", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		fixture.core.fixedPointCoordinator.carried.lastPage = 5
		current := fixture.core.fixedPointCoordinator.carriedSource()
		fixture.source.overrideProgress = true
		fixture.source.progress = privateWriterFixedPointSourceProgress{
			sourceIdentity: current.identity,
			ordinal:        current.ordinal + 1,
			lastPage:       4,
			sourceEpoch:    current.epoch + 1,
		}
		if _, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		); problem.code != privateWriterTransactionErrFixedPoint ||
			fixture.core.state != privateWriterTransactionPending {
			t.Fatalf("page regression = %#v", problem)
		}
	})
}

func TestPreparedFixedPointWorkspaceIsOpaqueAndCallbackReentryIsRejected(t *testing.T) {
	requestType := reflect.TypeOf(privateWriterFixedPointPrepareRequest{})
	for index := 0; index < requestType.NumField(); index++ {
		if requestType.Field(index).Type.Kind() == reflect.Slice ||
			requestType.Field(index).Type.Kind() == reflect.Pointer {
			t.Fatalf("request exposes workspace storage through %q", requestType.Field(index).Name)
		}
	}
	sourceType := reflect.TypeOf((*privateWriterFixedPointOrderedSource)(nil)).Elem()
	method, found := sourceType.MethodByName("nextFixedPointSource")
	if !found || method.Type.NumIn() != 1 ||
		method.Type.In(0) != reflect.TypeOf(privateWriterCarriedSource{}) {
		t.Fatalf("ordered source callback surface = %#v", method)
	}

	fixture := newPreparedFixedPointFixture(t)
	before := fixture.workspace.versions()
	fixture.source.reenter = true
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if fixture.source.reentryProblem.code != privateWriterTransactionErrCallbackActive ||
		fixture.workspace.callbackActive ||
		fixture.workspace.versions() != before {
		t.Fatalf("callback reentry = problem %#v workspace %#v", fixture.source.reentryProblem, fixture.workspace)
	}
	if token.slot == nil || token.slot.workspace != fixture.workspace {
		t.Fatal("prepared token is not bound to the private workspace")
	}
}

func TestPreparedFixedPointSourcePoolAndStrictPageFences(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	fixture.core.fixedPointCoordinator.sourceState.pool = &privatePagePool{}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	); problem.code != privateWriterTransactionErrAbortRequired {
		t.Fatalf("source pool substitution = %#v", problem)
	}

	fixture = newPreparedFixedPointFixture(t)
	fixture.core.fixedPointCoordinator.carried.lastPage = 5
	current := fixture.core.fixedPointCoordinator.carriedSource()
	fixture.source.overrideProgress = true
	fixture.source.progress = privateWriterFixedPointSourceProgress{
		sourceIdentity: current.identity,
		ordinal:        current.ordinal + 1,
		lastPage:       current.lastPage,
		sourceEpoch:    current.epoch + 1,
	}
	if _, problem = fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	); problem.code != privateWriterTransactionErrFixedPoint ||
		fixture.core.fixedPointCoordinator.predecessorUsed {
		t.Fatalf("equal page replay = %#v", problem)
	}
}

func TestPreparedFixedPointScopePreflightRejectsBeforeConsume(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*privatePagePool)
	}{
		{"vacancy chain", func(pool *privatePagePool) { pool.slots[0].unscopedNext = 0 }},
		{"vacancy epoch", func(pool *privatePagePool) { pool.slots[0].epoch = ^uint64(0) }},
		{"scope sequence", func(pool *privatePagePool) { pool.scopeSequence = ^uint64(0) }},
		{"mutation headroom", func(pool *privatePagePool) {
			pool.mutationEpoch = ^uint64(0) - pool.abortMutationReserve
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreparedFixedPointFixture(t)
			testCase.mutate(&fixture.core.pool)
			beforeEpoch := fixture.core.pool.mutationEpoch
			if _, problem := fixture.core.prepareFixedPointWork(
				fixture.handle, fixture.request(),
			); problem.code != privateWriterTransactionErrFixedPoint ||
				fixture.core.fixedPointCoordinator.predecessorUsed ||
				fixture.core.pool.registeredWorkFence != nil ||
				fixture.core.pool.mutationEpoch != beforeEpoch {
				t.Fatalf("scope preflight = %#v pool %#v", problem, fixture.core.pool)
			}
		})
	}
}

func TestPreparedFixedPointRegistersEveryOwnerBeforeScopeMutation(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	startEpoch := fixture.core.pool.mutationEpoch
	active, scope, problem := fixture.core.consumeFixedPointWork(fixture.handle, token)
	if problem.failed() {
		t.Fatal(problem)
	}
	slot := fixture.core.fixedPointCoordinator.activePrepared
	if slot == nil || slot.phase != privateWriterFixedPointWorkActive ||
		active.workID != slot.workID ||
		fixture.core.fixedPointRegisteredWorkID != slot.workID ||
		fixture.core.pool.registeredWorkID != slot.workID ||
		fixture.core.fixedPointRegisteredWorkPhase != privateWriterFixedPointWorkActive ||
		fixture.core.pool.registeredWorkPhase != uint8(privateWriterFixedPointWorkActive) ||
		fixture.core.pool.registeredWorkStartEpoch != startEpoch ||
		!fixture.core.pool.registeredWorkMutation ||
		fixture.core.pool.mutationEpoch <= startEpoch ||
		fixture.core.pool.registeredScopeID != scope.id {
		t.Fatalf("registration/scope ordering = core %#v pool %#v slot %#v", fixture.core, fixture.core.pool, slot)
	}
}

func TestPreparedFixedPointScopeMutationRequiresAllThreeRegistries(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	slot, fixedProblem := fixture.core.fixedPointCoordinator.validatePreparedWork(token)
	if fixedProblem.failed() {
		t.Fatal(fixedProblem)
	}
	if fixedProblem = fixture.core.fixedPointCoordinator.registerPreparedWork(slot); fixedProblem.failed() {
		t.Fatal(fixedProblem)
	}
	if poolProblem := fixture.core.pool.registerPreparedCoordinatorWork(slot); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	beforeEpoch := fixture.core.pool.mutationEpoch
	beforeFirst := fixture.core.pool.slots[0]
	beforeSecond := fixture.core.pool.slots[1]
	if _, poolProblem := fixture.core.pool.applyPreparedCoordinatorScope(
		nil,
	); poolProblem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("missing core registry = %#v", poolProblem)
	}
	if fixture.core.pool.mutationEpoch != beforeEpoch ||
		fixture.core.pool.slots[0] != beforeFirst ||
		fixture.core.pool.slots[1] != beforeSecond ||
		fixture.core.pool.registeredWorkMutation {
		t.Fatal("scope mutation began before the writer core registered work")
	}
	fixture.core.fixedPointWorkActive = true
	fixture.core.fixedPointRegisteredWorkID = slot.workID
	fixture.core.fixedPointRegisteredWorkGeneration = slot.generation
	fixture.core.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkRegistered
	if poolProblem := fixture.core.installPreparedWorkFence(slot); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	copiedFence := *fixture.core.fixedPointWorkFence
	if _, poolProblem := fixture.core.pool.applyPreparedCoordinatorScope(
		&copiedFence,
	); poolProblem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("copied fence = %#v", poolProblem)
	}
	if fixture.core.pool.mutationEpoch != beforeEpoch ||
		fixture.core.pool.registeredWorkMutation {
		t.Fatal("copied fence began scope mutation")
	}
	if _, poolProblem := fixture.core.pool.applyPreparedCoordinatorScope(
		fixture.core.fixedPointWorkFence,
	); poolProblem.failed() {
		t.Fatalf("canonical fence = %#v", poolProblem)
	}
}

func TestPreparedFixedPointLegacyMutationBooleanCannotOverrideCoordinator(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem = fixture.core.fixedPointOperationFailed(
		fixture.handle,
		privateWriterFixedPointError{code: privateWriterFixedPointErrSource},
		false,
	); problem.code != privateWriterTransactionErrAbortRequired ||
		fixture.core.state != privateWriterTransactionAbortRequired {
		t.Fatalf("false mutation report bypass = %#v", problem)
	}
}

func TestPreparedFixedPointWorkspaceBudgetAndLayout(t *testing.T) {
	for _, budget := range []privateWriterWorkspaceBudget{
		{},
		{maxBytes: 1 << 20, privatePages: 32, records: 4, preparedSlots: 2, scratchWordsPerSlot: int(^uint(0) >> 1)},
		{maxBytes: 1 << 20, privatePages: -1, records: 4, preparedSlots: 2, scratchWordsPerSlot: 8},
	} {
		if workspace, problem := newPrivateWriterWorkspace(
			budget,
			privateWriterResourceBudget{maxHeapBytes: 1 << 20},
		); workspace != nil ||
			problem.code != privateWriterWorkspaceErrInvalidBudget {
			t.Fatalf("invalid budget = workspace %#v problem %#v", workspace, problem)
		}
	}
	fixture := newPreparedFixedPointFixture(t)
	if fixture.workspace.self != fixture.workspace ||
		fixture.workspace.layout.bytes == 0 ||
		fixture.workspace.partitionBytes == 0 ||
		fixture.workspace.partitionBytes != fixture.workspace.layout.bytes ||
		fixture.workspace.partitionBytes > fixture.workspace.writerHeapBudget ||
		fixture.core.resources.budget.maxHeapBytes !=
			fixture.workspace.writerHeapBudget-fixture.workspace.partitionBytes ||
		fixture.workspace.layoutGeneration == 0 ||
		len(fixture.workspace.poolSlots) != 32 ||
		len(fixture.workspace.records) != 4 ||
		len(fixture.workspace.slotRecords) != 32 ||
		len(fixture.workspace.preparedSlots) != 2 ||
		len(fixture.workspace.scratch) != 16 {
		t.Fatalf("workspace layout = %#v", fixture.workspace)
	}
}

func TestPreparedFixedPointWorkspaceRejectsNativeAddressOverflow(t *testing.T) {
	if ^uintptr(0) > uintptr(^uint32(0)) {
		t.Skip("native-address overflow budget is specific to 32-bit hosts")
	}
	maxInt := int(^uint(0) >> 1)
	workspace, problem := newPrivateWriterWorkspace(
		privateWriterWorkspaceBudget{
			maxBytes: ^uint64(0), privatePages: maxInt, records: 1,
			preparedSlots: 1, scratchWordsPerSlot: 1,
		},
		privateWriterResourceBudget{
			maxHeapBytes: ^uint64(0), maxPrivatePages: uint64(maxInt),
		},
	)
	if workspace != nil || problem.code != privateWriterWorkspaceErrInvalidBudget {
		t.Fatalf("native overflow = workspace %#v problem %#v", workspace, problem)
	}
}

func TestPreparedFixedPointWorkspaceHasOneTransactionInitializationOwner(t *testing.T) {
	const privatePages = 4096
	const records = 130
	const preparedSlots = 130
	const scratchWords = 8
	const maxBytes = 64 << 20
	budget := privateWriterResourceBudget{
		maxHeapBytes: maxBytes, maxPrivatePages: privatePages,
		maxFileGrowthPages: privatePages, maxOpenFiles: 4,
	}
	workspace, workspaceProblem := newPrivateWriterWorkspace(
		privateWriterWorkspaceBudget{
			maxBytes: maxBytes, privatePages: privatePages,
			records: records, preparedSlots: preparedSlots,
			scratchWordsPerSlot: scratchWords,
		},
		budget,
	)
	if workspaceProblem.failed() {
		t.Fatal(workspaceProblem)
	}
	if workspace.transactionResetCount != 0 ||
		workspace.transactionResetVisits != 0 {
		t.Fatalf("constructor reset workspace: %#v", workspace)
	}
	workspace.poolSlots[privatePages-1].epoch = 77
	var core privateWriterTransactionCore
	var cleanupObligations [2]privateWriterCleanupObligation
	var cleanupOwners [2]privateWriterCleanupOwner
	selected := Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		DatabaseID:    [16]byte{1},
		TxnID:         1,
		CommitNonce:   [16]byte{2},
		PageCount:     20,
	}
	if problem := initPrivateWriterTransactionCoreWithWorkspace(
		&core, selected, budget, workspace,
		cleanupObligations[:], cleanupOwners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if workspace.transactionResetCount != 0 ||
		workspace.poolSlots[privatePages-1].epoch != 77 {
		t.Fatal("core initialization reset transaction storage")
	}
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	expectedVisits := uint64(
		privatePages + records + privatePages + preparedSlots +
			preparedSlots*scratchWords + records + preparedSlots + preparedSlots,
	)
	if workspace.transactionResetCount != 1 ||
		workspace.transactionResetVisits != expectedVisits {
		t.Fatalf(
			"transaction reset = count %d visits %d, want 1/%d",
			workspace.transactionResetCount,
			workspace.transactionResetVisits,
			expectedVisits,
		)
	}
	if workspace.poolSlots[privatePages-1].epoch != 1 {
		t.Fatal("Begin did not initialize the page arena")
	}
	resetVisits := workspace.transactionResetVisits
	recordSentinel := privateWriterSealedBitmapWorkUnitRecord{workUnit: 99, active: true}
	workspace.records[records-1] = recordSentinel
	workspace.slotRecords[privatePages-1] = 99
	workspace.scratch[len(workspace.scratch)-1] = 99
	workspace.preparedSlots[preparedSlots-1].storageIndex = 99
	sourcePages := [1]cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}
	source := &preparedWorkCallbackSource{
		cowSparsePages: &cowSparsePages{pages: sourcePages[:]},
		core:           &core,
		handle:         handle,
	}
	if problem = core.startPreparedFixedPoint(handle, source, 2); problem.failed() {
		t.Fatal(problem)
	}
	if workspace.transactionResetVisits != resetVisits ||
		!reflect.DeepEqual(workspace.records[records-1], recordSentinel) ||
		workspace.slotRecords[privatePages-1] != 99 ||
		workspace.scratch[len(workspace.scratch)-1] != 99 ||
		workspace.preparedSlots[preparedSlots-1].storageIndex != 99 {
		t.Fatal("prepared fixed-point startup traversed untouched workspace capacity")
	}
}

func TestPreparedFixedPointCanonicalFencePointerOnly(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	); problem.failed() {
		t.Fatal(problem)
	}
	fence := fixture.core.fixedPointWorkFence
	if fence == nil || fence != fixture.core.pool.registeredWorkFence ||
		fence != fixture.core.fixedPointCoordinator.workFence ||
		fence.self != fence {
		t.Fatalf("canonical fence = %#v", fence)
	}
	if poolProblem := fixture.core.pool.validateWorkFence(
		nil, privateWriterFixedPointWorkActive,
	); poolProblem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("nil fence = %#v", poolProblem)
	}
	copied := *fence
	if poolProblem := fixture.core.pool.validateWorkFence(
		&copied, privateWriterFixedPointWorkActive,
	); poolProblem.code != privatePagePoolErrCoordinatorRequired {
		t.Fatalf("copied fence = %#v", poolProblem)
	}
	if poolProblem := fixture.core.pool.validateWorkFence(
		fence, privateWriterFixedPointWorkActive,
	); poolProblem.failed() {
		t.Fatalf("canonical fence rejected = %#v", poolProblem)
	}
}

func TestPreparedFixedPointMutationHelpersRejectNoncanonicalFences(t *testing.T) {
	type mutationClass struct {
		name string
		call func(
			*privatePagePool,
			privatePageReservationScope,
			*privateWriterWorkFence,
		) privatePagePoolError
	}
	var page [PageSize]byte
	classes := []mutationClass{
		{
			name: "close",
			call: func(pool *privatePagePool, scope privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.closeScope(scope, fence)
			},
		},
		{
			name: "checkpoint prepared bind",
			call: func(pool *privatePagePool, scope privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				_, problem := pool.bindPageForCheckpointPrepared(
					privatePagePoolCheckpoint{}, scope, 7, privatePageReclaimed, fence,
				)
				return problem
			},
		},
		{
			name: "terminal claim",
			call: func(pool *privatePagePool, _ privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.claimSlotForOperationTerminalPrepared(
					privatePagePoolOperation{}, 0,
					privatePageOwnerBitmap, privatePageBitmap, fence,
				)
			},
		},
		{
			name: "prepared operation write",
			call: func(pool *privatePagePool, _ privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.writeSlotForOperationInScopePrepared(
					privatePagePoolOperation{}, 0, &page, fence,
				)
			},
		},
		{
			name: "retirement write",
			call: func(pool *privatePagePool, scope privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.installRetirementPageInScopePrepared(
					privatePagePoolCheckpoint{}, scope, 0,
					privatePageRetirementTree, &page, fence,
				)
			},
		},
		{
			name: "scoped write",
			call: func(pool *privatePagePool, scope privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.writePageInScope(
					scope, privatePageToken{}, &page, fence,
				)
			},
		},
		{
			name: "scoped generation release",
			call: func(pool *privatePagePool, scope privatePageReservationScope, fence *privateWriterWorkFence) privatePagePoolError {
				return pool.releaseGenerationInScope(
					scope, 1, privatePageOwnerBitmap, privatePageBitmap, fence,
				)
			},
		},
	}
	for _, class := range classes {
		for _, kind := range []string{"nil", "copied", "forged", "wrong phase"} {
			t.Run(class.name+"/"+kind, func(t *testing.T) {
				fixture := newPreparedFixedPointFixture(t)
				token, problem := fixture.core.prepareFixedPointWork(
					fixture.handle, fixture.request(),
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				_, scope, problem := fixture.core.consumeFixedPointWork(
					fixture.handle, token,
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				canonical := fixture.core.fixedPointWorkFence
				var candidate *privateWriterWorkFence
				switch kind {
				case "nil":
					candidate = nil
				case "copied":
					copied := *canonical
					candidate = &copied
				case "forged":
					forged := privateWriterWorkFence{}
					forged.self = &forged
					candidate = &forged
				case "wrong phase":
					canonical.phase = privateWriterFixedPointWorkRegistered
					candidate = canonical
				}
				beforeEpoch := fixture.core.pool.mutationEpoch
				beforeSlot := fixture.core.pool.slots[0]
				if poolProblem := class.call(
					&fixture.core.pool, scope, candidate,
				); poolProblem.code != privatePagePoolErrCoordinatorRequired {
					t.Fatalf("problem = %#v", poolProblem)
				}
				if fixture.core.pool.mutationEpoch != beforeEpoch ||
					fixture.core.pool.slots[0] != beforeSlot {
					t.Fatal("rejected fence mutated the pool")
				}
			})
		}
	}
}

func TestPreparedFixedPointAllNamedMutationHelpersRejectMissingFence(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, scope, problem := fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool := &fixture.core.pool
	var page [PageSize]byte
	checks := []struct {
		name string
		call func() privatePagePoolError
	}{
		{"close counted", func() privatePagePoolError {
			_, problem := pool.closeScopeCounted(scope)
			return problem
		}},
		{"checkpoint claim", func() privatePagePoolError {
			return pool.claimSlotForCheckpointPrepared(privatePagePoolCheckpoint{}, 0)
		}},
		{"checkpoint write", func() privatePagePoolError {
			return pool.writeSlotForCheckpointPrepared(0, &page)
		}},
		{"checkpoint origin", func() privatePagePoolError {
			return pool.setSlotCommittedOriginForCheckpointPrepared(0, 1)
		}},
		{"checkpoint direct claim", func() privatePagePoolError {
			_, problem := pool.claimSlotPrepared(
				privatePagePoolCheckpoint{}, 0,
				privatePageOwnerBitmap, privatePageBitmap,
			)
			return problem
		}},
		{"checkpoint begin", func() privatePagePoolError {
			return pool.beginCheckpointPrepared(privatePagePoolCheckpoint{})
		}},
		{"checkpoint commit", func() privatePagePoolError {
			return pool.commitCheckpointPrepared(privatePagePoolCheckpoint{})
		}},
		{"checkpoint scoped commit", func() privatePagePoolError {
			return pool.commitCheckpointInScopePrepared(
				privatePagePoolCheckpoint{}, scope,
			)
		}},
		{"checkpoint release", func() privatePagePoolError {
			return pool.releaseSlotForCheckpointPrepared(
				privatePagePoolCheckpoint{}, 0, privatePageAvailable,
			)
		}},
		{"checkpoint scoped release", func() privatePagePoolError {
			return pool.releaseSlotForCheckpointInScopePrepared(
				privatePagePoolCheckpoint{}, scope, 0, privatePageAvailable,
			)
		}},
		{"checkpoint sealed release", func() privatePagePoolError {
			return pool.releaseSealedSlotForCheckpointPrepared(
				privatePagePoolCheckpoint{}, scope, 0, privatePageAvailable,
			)
		}},
		{"operation claim", func() privatePagePoolError {
			return pool.claimSlotForOperationPrepared(
				privatePagePoolOperation{}, 0,
				privatePageOwnerBitmap, privatePageBitmap,
			)
		}},
		{"operation begin", func() privatePagePoolError {
			return pool.beginOperationPrepared(privatePagePoolOperation{})
		}},
		{"operation commit", func() privatePagePoolError {
			return pool.commitOperationPrepared(privatePagePoolOperation{})
		}},
		{"operation scoped claim", func() privatePagePoolError {
			return pool.claimSlotForOperationInScopePrepared(
				privatePagePoolOperation{}, 0,
				privatePageOwnerBitmap, privatePageBitmap,
			)
		}},
		{"operation scoped write", func() privatePagePoolError {
			return pool.writeSlotForOperationInScopePrepared(
				privatePagePoolOperation{}, 0, &page,
			)
		}},
		{"operation scoped origin", func() privatePagePoolError {
			return pool.setSlotCommittedOriginForOperationInScopePrepared(
				privatePagePoolOperation{}, 0, 1,
			)
		}},
		{"operation scoped release", func() privatePagePoolError {
			return pool.releaseSlotForOperationInScopePrepared(
				privatePagePoolOperation{}, 0, privatePageAvailable,
			)
		}},
		{"terminal scoped claim", func() privatePagePoolError {
			return pool.claimSlotForOperationInScopeTerminalPrepared(
				privatePagePoolOperation{}, 0, privatePageOwnerBitmap, privatePageBitmap,
			)
		}},
		{"terminal write", func() privatePagePoolError {
			return pool.writeSlotForOperationInScopeTerminalPrepared(0, &page)
		}},
		{"terminal origin", func() privatePagePoolError {
			return pool.setSlotCommittedOriginForOperationInScopeTerminalPrepared(0, 1)
		}},
		{"terminal release", func() privatePagePoolError {
			return pool.releaseSlotForOperationInScopeTerminalPrepared(
				privatePagePoolOperation{}, 0, privatePageAvailable,
			)
		}},
		{"direct prepared write", func() privatePagePoolError {
			return pool.writeSlotPrepared(0, &page)
		}},
		{"direct scoped prepared write", func() privatePagePoolError {
			return pool.writeSlotInScopePrepared(scope, 0, &page)
		}},
		{"direct prepared origin", func() privatePagePoolError {
			return pool.setSlotCommittedOriginPrepared(0, 1)
		}},
		{"direct prepared release", func() privatePagePoolError {
			return pool.releaseSlotPrepared(0, privatePageAvailable)
		}},
		{"retirement release", func() privatePagePoolError {
			return pool.releaseRetirementSlotInScopePrepared(
				privatePagePoolCheckpoint{}, scope, 0,
			)
		}},
		{"scoped origin", func() privatePagePoolError {
			return pool.setCommittedOriginInScope(scope, privatePageToken{}, 1)
		}},
		{"direct page write", func() privatePagePoolError {
			return pool.writePage(privatePageToken{slot: 0}, &page)
		}},
		{"direct page origin", func() privatePagePoolError {
			return pool.setCommittedOrigin(privatePageToken{slot: 0}, 1)
		}},
		{"unbind", func() privatePagePoolError {
			return pool.unbindPage(privatePagePoolCheckpoint{}, scope, 7)
		}},
		{"transfer", func() privatePagePoolError {
			_, problem := pool.transfer(
				privatePagePoolCheckpoint{}, privatePageToken{slot: 0},
				privatePageOwnerRetirement, privatePageRetirementTree,
			)
			return problem
		}},
		{"scoped transfer", func() privatePagePoolError {
			_, problem := pool.transferInScope(
				privatePagePoolCheckpoint{}, scope, privatePageToken{slot: 0},
				privatePageOwnerRetirement, privatePageRetirementTree,
			)
			return problem
		}},
		{"change origin", func() privatePagePoolError {
			_, problem := pool.changeOrigin(
				privatePageToken{slot: 0}, privatePageRetirementTree,
			)
			return problem
		}},
		{"recycle", func() privatePagePoolError {
			return pool.recycle(privatePageToken{slot: 0})
		}},
		{"return released", func() privatePagePoolError {
			return pool.returnReleased(privatePageToken{slot: 0})
		}},
		{"scoped return", func() privatePagePoolError {
			return pool.returnUnownedInScope(scope, 7, privatePageReleasedFree)
		}},
		{"scoped release", func() privatePagePoolError {
			return pool.releaseInScope(scope, privatePageToken{}, privatePageAvailable)
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			beforeEpoch := pool.mutationEpoch
			beforeSlot := pool.slots[0]
			if poolProblem := check.call(); poolProblem.code != privatePagePoolErrCoordinatorRequired {
				t.Fatalf("problem = %#v", poolProblem)
			}
			if pool.mutationEpoch != beforeEpoch || pool.slots[0] != beforeSlot {
				t.Fatal("missing fence mutated the pool")
			}
		})
	}
}

func TestPreparedFixedPointTerminalMutationsRejectForeignSlots(t *testing.T) {
	type terminalMutation struct {
		name    string
		prepare func(
			*privatePagePool,
			*privateWriterWorkFence,
		) privatePagePoolError
		call func(
			*privatePagePool,
			privatePageReservationScope,
			*privateWriterWorkFence,
			int,
		) privatePagePoolError
	}
	var page [PageSize]byte
	mutations := []terminalMutation{
		{
			name: "operation claim",
			prepare: func(
				pool *privatePagePool,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return pool.beginOperationPrepared(
					fence.slot.terminalJournal.operation, fence,
				)
			},
			call: func(
				pool *privatePagePool,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
				index int,
			) privatePagePoolError {
				return pool.claimSlotForOperationTerminalPrepared(
					fence.slot.terminalJournal.operation,
					index, privatePageOwnerBitmap, privatePageBitmap, fence,
				)
			},
		},
		{
			name: "retirement install",
			prepare: func(
				pool *privatePagePool,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return pool.beginCheckpointPrepared(
					fence.slot.terminalJournal.checkpoint, fence,
				)
			},
			call: func(
				pool *privatePagePool,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
				index int,
			) privatePagePoolError {
				return pool.installRetirementPageInScopePrepared(
					fence.slot.terminalJournal.checkpoint, scope, index,
					privatePageRetirementTree, &page, fence,
				)
			},
		},
		{
			name: "retirement release",
			prepare: func(
				pool *privatePagePool,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return pool.beginCheckpointPrepared(
					fence.slot.terminalJournal.checkpoint, fence,
				)
			},
			call: func(
				pool *privatePagePool,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
				index int,
			) privatePagePoolError {
				return pool.releaseRetirementSlotInScopePrepared(
					fence.slot.terminalJournal.checkpoint,
					scope, index, fence,
				)
			},
		},
	}
	targets := []struct {
		name  string
		index int
	}{
		{name: "foreign", index: 2},
		{name: "negative", index: -1},
		{name: "past-end", index: 32},
	}
	for _, mutation := range mutations {
		for _, target := range targets {
			t.Run(mutation.name+"/"+target.name, func(t *testing.T) {
				fixture := newPreparedFixedPointFixture(t)
				token, problem := fixture.core.prepareFixedPointWork(
					fixture.handle, fixture.request(),
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				_, scope, problem := fixture.core.consumeFixedPointWork(
					fixture.handle, token,
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				if poolProblem := mutation.prepare(
					&fixture.core.pool,
					fixture.core.fixedPointWorkFence,
				); poolProblem.failed() {
					t.Fatal(poolProblem)
				}
				beforeEpoch := fixture.core.pool.mutationEpoch
				beforeSlots := append(
					[]privatePagePoolSlot(nil), fixture.core.pool.slots...,
				)
				poolProblem := mutation.call(
					&fixture.core.pool, scope,
					fixture.core.fixedPointWorkFence, target.index,
				)
				if !poolProblem.failed() {
					t.Fatal("foreign terminal slot accepted")
				}
				if fixture.core.pool.mutationEpoch != beforeEpoch ||
					!reflect.DeepEqual(fixture.core.pool.slots, beforeSlots) {
					t.Fatal("foreign terminal slot mutated the pool")
				}
			})
		}
	}
}

func TestPreparedFixedPointScopedGenerationReleaseUsesCanonicalFence(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, scope, problem := fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool := &fixture.core.pool
	fence := fixture.core.fixedPointWorkFence
	checkpoint := fence.slot.terminalJournal.checkpoint
	if poolProblem := pool.beginCheckpointPrepared(
		checkpoint, fence,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	index, poolProblem := pool.bindPageForCheckpointPrepared(
		checkpoint, scope, 7, privatePageReclaimed, fence,
	)
	if poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if poolProblem = pool.claimSlotForCheckpointPrepared(
		checkpoint, index, fence,
	); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if slot := pool.slots[index]; !slot.bound ||
		slot.state != privatePageInUse || !slot.inUse ||
		slot.owner != privatePageOwnerBitmap ||
		slot.origin != privatePageBitmap ||
		slot.generation != checkpoint.generation {
		t.Fatalf("test page is not a live owned generation member: %#v", slot)
	}

	assertUnchanged := func(
		t *testing.T,
		beforeEpoch uint64,
		beforeSlots []privatePagePoolSlot,
	) {
		t.Helper()
		if pool.mutationEpoch != beforeEpoch ||
			!reflect.DeepEqual(pool.slots, beforeSlots) {
			t.Fatal("rejected generation release mutated the pool")
		}
	}
	reject := func(
		t *testing.T,
		name string,
		call func() privatePagePoolError,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			beforeEpoch := pool.mutationEpoch
			beforeSlots := append([]privatePagePoolSlot(nil), pool.slots...)
			if problem := call(); problem.code != privatePagePoolErrCoordinatorRequired {
				t.Fatalf("release = %#v", problem)
			}
			assertUnchanged(t, beforeEpoch, beforeSlots)
		})
	}

	copiedFence := *fence
	copiedFence.self = &copiedFence
	reject(t, "wrong fence", func() privatePagePoolError {
		return pool.releaseGenerationInScope(
			scope, checkpoint.generation,
			privatePageOwnerBitmap, privatePageBitmap, &copiedFence,
		)
	})
	reject(t, "wrong generation", func() privatePagePoolError {
		return pool.releaseGenerationInScope(
			scope, checkpoint.generation+1,
			privatePageOwnerBitmap, privatePageBitmap, fence,
		)
	})
	wrongScope := scope
	wrongScope.id++
	reject(t, "wrong scope", func() privatePagePoolError {
		return pool.releaseGenerationInScope(
			wrongScope, checkpoint.generation,
			privatePageOwnerBitmap, privatePageBitmap, fence,
		)
	})
	foreignIndex := privatePagePoolNoIndex
	for candidate := range pool.slots {
		if pool.slots[candidate].scopeID != scope.id {
			foreignIndex = candidate
			break
		}
	}
	if foreignIndex == privatePagePoolNoIndex {
		t.Fatal("test fixture has no foreign page")
	}
	reject(t, "wrong page", func() privatePagePoolError {
		return pool.releaseSlotForCheckpointInScopePrepared(
			checkpoint, scope, foreignIndex, privatePageAvailable, fence,
		)
	})

	beforeMutationEpoch := pool.mutationEpoch
	beforeSlotEpoch := pool.slots[index].epoch
	if poolProblem = pool.releaseGenerationInScope(
		scope, checkpoint.generation,
		privatePageOwnerBitmap, privatePageBitmap, fence,
	); poolProblem.failed() {
		t.Fatalf("canonical generation release = %#v", poolProblem)
	}
	slot := pool.slots[index]
	if !slot.bound || slot.state != privatePageAvailable || slot.inUse ||
		slot.owner != privatePageOwnerNone ||
		slot.origin != privatePageOriginNone ||
		slot.pendingTxn != 0 || slot.generation != 0 {
		t.Fatalf("canonical generation release left wrong page state: %#v", slot)
	}
	if pool.mutationEpoch != beforeMutationEpoch+1 ||
		slot.epoch != beforeSlotEpoch+1 {
		t.Fatalf(
			"canonical release epochs = pool %d→%d slot %d→%d",
			beforeMutationEpoch, pool.mutationEpoch, beforeSlotEpoch, slot.epoch,
		)
	}
}

func TestPreparedFixedPointTerminalLifecycleRejectsCallerAuthoredState(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	_, scope, problem := fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	pool := &fixture.core.pool
	fence := fixture.core.fixedPointWorkFence
	before := *pool
	forgedOperation := privatePagePoolOperation{
		pool: pool, poolEpoch: pool.epoch, id: 77,
		pendingTxn: pool.pendingTxn, generation: pool.generation + 1,
		scopeID: scope.id, scopeAnchor: scope.anchor,
		startEpoch: pool.mutationEpoch,
	}
	if poolProblem := pool.beginOperationPrepared(
		forgedOperation, fence,
	); !poolProblem.failed() {
		t.Fatal("caller-authored operation accepted")
	}
	if pool.activeOperationID != before.activeOperationID ||
		pool.operationSequence != before.operationSequence ||
		pool.operationStartEpoch != before.operationStartEpoch {
		t.Fatal("caller-authored operation mutated lifecycle state")
	}
	forgedCheckpoint := privatePagePoolCheckpoint{
		pool: pool, poolEpoch: pool.epoch, id: 88,
		generation: pool.generation + 1,
		indexRoot:  pool.indexRoot, pendingPageCount: pool.pendingPageCount,
	}
	if poolProblem := pool.beginCheckpointPrepared(
		forgedCheckpoint, fence,
	); !poolProblem.failed() {
		t.Fatal("caller-authored checkpoint accepted")
	}
	if pool.activeCheckpointID != before.activeCheckpointID ||
		pool.checkpointSequence != before.checkpointSequence {
		t.Fatal("caller-authored checkpoint mutated lifecycle state")
	}
}

func TestPreparedFixedPointTerminalLifecycleConsumesSealedJournal(t *testing.T) {
	t.Run("operation", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, _, problem = fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		); problem.failed() {
			t.Fatal(problem)
		}
		pool := &fixture.core.pool
		fence := fixture.core.fixedPointWorkFence
		operation := fence.slot.terminalJournal.operation
		if poolProblem := pool.beginOperationPrepared(
			operation, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		beforeEpoch := pool.mutationEpoch
		if _, poolProblem := pool.claimPageForOperationInScope(
			operation, 7, privatePageOwnerBitmap, privatePageBitmap,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("raw coordinator operation = %#v", poolProblem)
		}
		if pool.mutationEpoch != beforeEpoch {
			t.Fatal("raw coordinator operation mutated the pool")
		}
		substituted := operation
		substituted.generation++
		beforeGeneration := pool.generation
		if poolProblem := pool.commitOperationPrepared(
			substituted, fence,
		); !poolProblem.failed() {
			t.Fatal("substituted operation commit accepted")
		}
		if pool.generation != beforeGeneration ||
			pool.activeOperationID != operation.id {
			t.Fatal("substituted operation changed lifecycle state")
		}
		if poolProblem := pool.commitOperationPrepared(
			operation, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		if poolProblem := pool.commitOperationPrepared(
			operation, fence,
		); !poolProblem.failed() {
			t.Fatal("consumed operation journal reused")
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		_, scope, problem := fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		pool := &fixture.core.pool
		fence := fixture.core.fixedPointWorkFence
		checkpoint := fence.slot.terminalJournal.checkpoint
		if poolProblem := pool.beginCheckpointPrepared(
			checkpoint, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		substituted := checkpoint
		substituted.id++
		beforeGeneration := pool.generation
		if poolProblem := pool.commitCheckpointInScopePrepared(
			substituted, scope, fence,
		); !poolProblem.failed() {
			t.Fatal("substituted checkpoint commit accepted")
		}
		if pool.generation != beforeGeneration ||
			pool.activeCheckpointID != checkpoint.id {
			t.Fatal("substituted checkpoint changed lifecycle state")
		}
		if poolProblem := pool.commitCheckpointInScopePrepared(
			checkpoint, scope, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		if poolProblem := pool.commitCheckpointInScopePrepared(
			checkpoint, scope, fence,
		); !poolProblem.failed() {
			t.Fatal("consumed checkpoint journal reused")
		}
	})

	t.Run("copied journal", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, _, problem = fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		); problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		copied := fence.slot.terminalJournal
		fence.slot.terminalJournal.self = &copied
		if poolProblem := fixture.core.pool.beginOperationPrepared(
			copied.operation, fence,
		); !poolProblem.failed() {
			t.Fatal("copied terminal journal accepted")
		}
	})
}

func TestPreparedFixedPointMutationRejectsEveryRegistryCorruption(t *testing.T) {
	type corruption struct {
		name   string
		mutate func(*preparedFixedPointFixture)
	}
	corruptions := []corruption{
		{"core session id", func(f *preparedFixedPointFixture) { f.core.fixedPointSessionID++ }},
		{"core session generation", func(f *preparedFixedPointFixture) { f.core.fixedPointSessionGeneration++ }},
		{"core work inactive", func(f *preparedFixedPointFixture) { f.core.fixedPointWorkActive = false }},
		{"core work id", func(f *preparedFixedPointFixture) { f.core.fixedPointRegisteredWorkID++ }},
		{"core work generation", func(f *preparedFixedPointFixture) { f.core.fixedPointRegisteredWorkGeneration++ }},
		{"core work phase", func(f *preparedFixedPointFixture) {
			f.core.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkRegistered
		}},
		{"core work fence", func(f *preparedFixedPointFixture) { f.core.fixedPointWorkFence = nil }},
		{"core transaction state", func(f *preparedFixedPointFixture) {
			f.core.state = privateWriterTransactionAbortRequired
		}},
		{"core fixed point inactive", func(f *preparedFixedPointFixture) { f.core.fixedPointActive = false }},
		{"core fixed point finished", func(f *preparedFixedPointFixture) { f.core.fixedPointFinished = true }},
		{"core prepared mode", func(f *preparedFixedPointFixture) { f.core.fixedPointPreparedMode = false }},
		{"pool session id", func(f *preparedFixedPointFixture) { f.core.pool.coordinatorSessionID++ }},
		{"pool session generation", func(f *preparedFixedPointFixture) {
			f.core.pool.coordinatorSessionGeneration++
		}},
		{"pool work id", func(f *preparedFixedPointFixture) { f.core.pool.registeredWorkID++ }},
		{"pool work generation", func(f *preparedFixedPointFixture) { f.core.pool.registeredWorkGeneration++ }},
		{"pool work phase", func(f *preparedFixedPointFixture) {
			f.core.pool.registeredWorkPhase = uint8(privateWriterFixedPointWorkRegistered)
		}},
		{"pool work fence", func(f *preparedFixedPointFixture) { f.core.pool.registeredWorkFence = nil }},
		{"pool work start", func(f *preparedFixedPointFixture) { f.core.pool.registeredWorkStartEpoch++ }},
		{"pool mutation epoch", func(f *preparedFixedPointFixture) {
			f.core.pool.mutationEpoch = f.core.pool.registeredWorkStartEpoch
		}},
		{"pool mutation marker", func(f *preparedFixedPointFixture) {
			f.core.pool.registeredWorkMutation = false
		}},
		{"pool scope id", func(f *preparedFixedPointFixture) { f.core.pool.registeredScopeID++ }},
		{"pool scope anchor", func(f *preparedFixedPointFixture) { f.core.pool.registeredScopeAnchor++ }},
		{"pool unaccepted scopes", func(f *preparedFixedPointFixture) { f.core.pool.unacceptedScopes = 0 }},
		{"pool cleanup marker", func(f *preparedFixedPointFixture) {
			f.core.pool.coordinatorCleanupPending = 1
		}},
		{"pool active scopes", func(f *preparedFixedPointFixture) { f.core.pool.activeScopes = 0 }},
		{"pool abort required", func(f *preparedFixedPointFixture) { f.core.pool.abortRequired = true }},
		{"coordinator active work", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.activePrepared = nil
		}},
		{"coordinator work fence", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.workFence = nil
		}},
		{"coordinator predecessor state", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.predecessorUsed = false
		}},
		{"coordinator generation", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.predecessorGeneration++
		}},
		{"coordinator carried state", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.carried.epoch++
		}},
		{"coordinator root", func(f *preparedFixedPointFixture) { f.core.fixedPointCoordinator.root++ }},
		{"coordinator page count", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.pageCount++
		}},
		{"coordinator record length", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.recordLen++
		}},
		{"coordinator last work", func(f *preparedFixedPointFixture) {
			f.core.fixedPointCoordinator.lastWorkUnit++
		}},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newPreparedFixedPointFixture(t)
			token, problem := fixture.core.prepareFixedPointWork(
				fixture.handle, fixture.request(),
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			_, scope, problem := fixture.core.consumeFixedPointWork(
				fixture.handle, token,
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			fence := fixture.core.fixedPointWorkFence
			operation := fence.slot.terminalJournal.operation
			if poolProblem := fixture.core.pool.beginOperationPrepared(
				operation, fence,
			); poolProblem.failed() {
				t.Fatal(poolProblem)
			}
			corruption.mutate(fixture)
			beforeEpoch := fixture.core.pool.mutationEpoch
			beforeSlot := fixture.core.pool.slots[scope.anchor]
			if poolProblem := fixture.core.pool.claimSlotForOperationTerminalPrepared(
				operation, scope.anchor,
				privatePageOwnerBitmap, privatePageBitmap, fence,
			); poolProblem.code != privatePagePoolErrCoordinatorRequired {
				t.Fatalf("registry corruption accepted: %#v", poolProblem)
			}
			if fixture.core.pool.mutationEpoch != beforeEpoch ||
				fixture.core.pool.slots[scope.anchor] != beforeSlot {
				t.Fatal("registry corruption mutated the pool")
			}
		})
	}
}

func TestPreparedFixedPointMutationRejectsEveryLifecycleRegistryCorruption(t *testing.T) {
	type lifecyclePhase struct {
		name  string
		setup func(
			*testing.T,
			*preparedFixedPointFixture,
			privatePageReservationScope,
			*privateWriterWorkFence,
		)
		call func(
			*preparedFixedPointFixture,
			privatePageReservationScope,
			*privateWriterWorkFence,
		) privatePagePoolError
	}
	phases := []lifecyclePhase{
		{
			name: "ready operation",
			setup: func(
				*testing.T,
				*preparedFixedPointFixture,
				privatePageReservationScope,
				*privateWriterWorkFence,
			) {
			},
			call: func(
				f *preparedFixedPointFixture,
				_ privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return f.core.pool.beginOperationPrepared(
					fence.slot.terminalJournal.operation, fence,
				)
			},
		},
		{
			name: "ready checkpoint",
			setup: func(
				*testing.T,
				*preparedFixedPointFixture,
				privatePageReservationScope,
				*privateWriterWorkFence,
			) {
			},
			call: func(
				f *preparedFixedPointFixture,
				_ privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return f.core.pool.beginCheckpointPrepared(
					fence.slot.terminalJournal.checkpoint, fence,
				)
			},
		},
		{
			name: "operation active",
			setup: func(
				t *testing.T,
				f *preparedFixedPointFixture,
				_ privatePageReservationScope,
				fence *privateWriterWorkFence,
			) {
				t.Helper()
				if problem := f.core.pool.beginOperationPrepared(
					fence.slot.terminalJournal.operation, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
			},
			call: func(
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return f.core.pool.claimSlotForOperationTerminalPrepared(
					fence.slot.terminalJournal.operation, scope.anchor,
					privatePageOwnerBitmap, privatePageBitmap, fence,
				)
			},
		},
		{
			name: "checkpoint active",
			setup: func(
				t *testing.T,
				f *preparedFixedPointFixture,
				_ privatePageReservationScope,
				fence *privateWriterWorkFence,
			) {
				t.Helper()
				if problem := f.core.pool.beginCheckpointPrepared(
					fence.slot.terminalJournal.checkpoint, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
			},
			call: func(
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				return f.core.pool.claimSlotForCheckpointPrepared(
					fence.slot.terminalJournal.checkpoint,
					scope.anchor, fence,
				)
			},
		},
		{
			name: "operation consumed",
			setup: func(
				t *testing.T,
				f *preparedFixedPointFixture,
				_ privatePageReservationScope,
				fence *privateWriterWorkFence,
			) {
				t.Helper()
				operation := fence.slot.terminalJournal.operation
				if problem := f.core.pool.beginOperationPrepared(
					operation, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
				if problem := f.core.pool.commitOperationPrepared(
					operation, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
			},
			call: func(
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				_, problem := f.core.pool.closeScopeCounted(scope, fence)
				return problem
			},
		},
		{
			name: "checkpoint consumed",
			setup: func(
				t *testing.T,
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) {
				t.Helper()
				checkpoint := fence.slot.terminalJournal.checkpoint
				if problem := f.core.pool.beginCheckpointPrepared(
					checkpoint, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
				if problem := f.core.pool.commitCheckpointInScopePrepared(
					checkpoint, scope, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
			},
			call: func(
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				_, problem := f.core.pool.closeScopeCounted(scope, fence)
				return problem
			},
		},
		{
			name: "checkpoint rollback consumed",
			setup: func(
				t *testing.T,
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) {
				t.Helper()
				checkpoint := fence.slot.terminalJournal.checkpoint
				if problem := f.core.pool.beginCheckpointPrepared(
					checkpoint, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
				if problem := f.core.pool.rollbackCheckpointInScope(
					checkpoint, scope, fence,
				); problem.failed() {
					t.Fatal(problem)
				}
			},
			call: func(
				f *preparedFixedPointFixture,
				scope privatePageReservationScope,
				fence *privateWriterWorkFence,
			) privatePagePoolError {
				_, problem := f.core.pool.closeScopeCounted(scope, fence)
				return problem
			},
		},
	}
	type corruption struct {
		name   string
		mutate func(*privatePagePool)
	}
	corruptions := []corruption{
		{"pending transaction", func(p *privatePagePool) { p.pendingTxn++ }},
		{"pool epoch", func(p *privatePagePool) { p.epoch++ }},
		{"pool generation", func(p *privatePagePool) { p.generation++ }},
		{"operation sequence", func(p *privatePagePool) { p.operationSequence++ }},
		{"checkpoint sequence", func(p *privatePagePool) { p.checkpointSequence++ }},
		{"active operation", func(p *privatePagePool) {
			if p.activeOperationID == 0 {
				p.activeOperationID = 1
			} else {
				p.activeOperationID = 0
			}
		}},
		{"operation start", func(p *privatePagePool) {
			if p.operationStartEpoch == 0 {
				p.operationStartEpoch = 1
			} else {
				p.operationStartEpoch++
			}
		}},
		{"active checkpoint", func(p *privatePagePool) {
			if p.activeCheckpointID == 0 {
				p.activeCheckpointID = 1
			} else {
				p.activeCheckpointID = 0
			}
		}},
	}
	for _, phase := range phases {
		for _, corruption := range corruptions {
			t.Run(phase.name+"/"+corruption.name, func(t *testing.T) {
				fixture := newPreparedFixedPointFixture(t)
				token, problem := fixture.core.prepareFixedPointWork(
					fixture.handle, fixture.request(),
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				_, scope, problem := fixture.core.consumeFixedPointWork(
					fixture.handle, token,
				)
				if problem.failed() {
					t.Fatal(problem)
				}
				fence := fixture.core.fixedPointWorkFence
				phase.setup(t, fixture, scope, fence)
				corruption.mutate(&fixture.core.pool)
				beforeEpoch := fixture.core.pool.mutationEpoch
				beforeSlots := append(
					[]privatePagePoolSlot(nil), fixture.core.pool.slots...,
				)
				if poolProblem := phase.call(
					fixture, scope, fence,
				); poolProblem.code != privatePagePoolErrCoordinatorRequired {
					t.Fatalf("lifecycle corruption accepted: %#v", poolProblem)
				}
				if fixture.core.pool.mutationEpoch != beforeEpoch ||
					!reflect.DeepEqual(fixture.core.pool.slots, beforeSlots) {
					t.Fatal("lifecycle corruption mutated the pool")
				}
			})
		}
	}
}

func TestPreparedFixedPointTerminalJournalContentIsIndependentlySealed(t *testing.T) {
	t.Run("operation before begin", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, _, problem = fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		); problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		fence.slot.terminalJournal.operation.pendingTxn++
		before := fixture.core.pool
		if poolProblem := fixture.core.pool.beginOperationPrepared(
			fence.slot.terminalJournal.operation, fence,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("mutated canonical operation accepted: %#v", poolProblem)
		}
		if fixture.core.pool.activeOperationID != before.activeOperationID ||
			fixture.core.pool.operationSequence != before.operationSequence {
			t.Fatal("mutated canonical operation changed lifecycle state")
		}
	})

	t.Run("operation after begin", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, _, problem = fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		); problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		operation := fence.slot.terminalJournal.operation
		if poolProblem := fixture.core.pool.beginOperationPrepared(
			operation, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		fence.slot.terminalJournal.operation.generation++
		beforeGeneration := fixture.core.pool.generation
		if poolProblem := fixture.core.pool.commitOperationPrepared(
			fence.slot.terminalJournal.operation, fence,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("mutated active operation accepted: %#v", poolProblem)
		}
		if fixture.core.pool.generation != beforeGeneration ||
			fixture.core.pool.activeOperationID != operation.id {
			t.Fatal("mutated active operation changed lifecycle state")
		}
	})

	t.Run("checkpoint before begin", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if _, _, problem = fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		); problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		fence.slot.terminalJournal.checkpoint.indexRoot++
		before := fixture.core.pool
		if poolProblem := fixture.core.pool.beginCheckpointPrepared(
			fence.slot.terminalJournal.checkpoint, fence,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("mutated canonical checkpoint accepted: %#v", poolProblem)
		}
		if fixture.core.pool.activeCheckpointID != before.activeCheckpointID ||
			fixture.core.pool.checkpointSequence != before.checkpointSequence {
			t.Fatal("mutated canonical checkpoint changed lifecycle state")
		}
	})

	t.Run("checkpoint after begin", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		_, scope, problem := fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		checkpoint := fence.slot.terminalJournal.checkpoint
		if poolProblem := fixture.core.pool.beginCheckpointPrepared(
			checkpoint, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		fence.slot.terminalJournal.checkpoint.indexRoot++
		beforeGeneration := fixture.core.pool.generation
		if poolProblem := fixture.core.pool.commitCheckpointInScopePrepared(
			fence.slot.terminalJournal.checkpoint, scope, fence,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("mutated active checkpoint accepted: %#v", poolProblem)
		}
		if fixture.core.pool.generation != beforeGeneration ||
			fixture.core.pool.activeCheckpointID != checkpoint.id {
			t.Fatal("mutated active checkpoint changed lifecycle state")
		}
	})

	t.Run("consumed journal before close", func(t *testing.T) {
		fixture := newPreparedFixedPointFixture(t)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, fixture.request(),
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		_, scope, problem := fixture.core.consumeFixedPointWork(
			fixture.handle, token,
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		fence := fixture.core.fixedPointWorkFence
		operation := fence.slot.terminalJournal.operation
		if poolProblem := fixture.core.pool.beginOperationPrepared(
			operation, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		if poolProblem := fixture.core.pool.commitOperationPrepared(
			operation, fence,
		); poolProblem.failed() {
			t.Fatal(poolProblem)
		}
		fence.slot.terminalJournal.operation.pendingTxn++
		beforeEpoch := fixture.core.pool.mutationEpoch
		if _, poolProblem := fixture.core.pool.closeScopeCounted(
			scope, fence,
		); poolProblem.code != privatePagePoolErrCoordinatorRequired {
			t.Fatalf("mutated consumed journal accepted: %#v", poolProblem)
		}
		if fixture.core.pool.mutationEpoch != beforeEpoch {
			t.Fatal("mutated consumed journal changed the pool")
		}
	})

	for _, kind := range []string{"operation", "checkpoint"} {
		t.Run(kind+" copied address", func(t *testing.T) {
			fixture := newPreparedFixedPointFixture(t)
			token, problem := fixture.core.prepareFixedPointWork(
				fixture.handle, fixture.request(),
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			if _, _, problem = fixture.core.consumeFixedPointWork(
				fixture.handle, token,
			); problem.failed() {
				t.Fatal(problem)
			}
			fence := fixture.core.fixedPointWorkFence
			copied := fence.slot.terminalJournal
			copied.self = &copied
			fence.slot.terminalJournal = copied
			var poolProblem privatePagePoolError
			if kind == "operation" {
				poolProblem = fixture.core.pool.beginOperationPrepared(
					copied.operation, fence,
				)
			} else {
				poolProblem = fixture.core.pool.beginCheckpointPrepared(
					copied.checkpoint, fence,
				)
			}
			if poolProblem.code != privatePagePoolErrCoordinatorRequired {
				t.Fatalf("copied journal address accepted: %#v", poolProblem)
			}
		})
	}
}

func TestPreparedFixedPointAdvertisedOwnedPagePoisonsDraft(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	fixture.source.overrideProgress = true
	current := fixture.core.fixedPointCoordinator.carriedSource()
	fixture.source.progress = privateWriterFixedPointSourceProgress{
		sourceIdentity: current.identity,
		ordinal:        current.ordinal + 1,
		lastPage:       7,
		sourceEpoch:    current.epoch + 1,
	}
	fixture.core.pool.slots[0].bound = true
	fixture.core.pool.slots[0].pageNumber = 7
	fixture.core.pool.slots[0].state = privatePageInUse
	fixture.core.pool.slots[0].inUse = true
	fixture.core.pool.slots[0].owner = privatePageOwnerBitmap
	fixture.core.pool.slots[0].origin = privatePageBitmap
	fixture.core.pool.indexRoot = 0
	if _, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	); problem.code != privateWriterTransactionErrAbortRequired ||
		problem.fixedPoint.code != privateWriterFixedPointErrAdvertisedOwnedPage ||
		fixture.core.state != privateWriterTransactionAbortRequired {
		t.Fatalf("advertised owned page = %#v", problem)
	}
}

func TestPreparedFixedPointAbortInvalidatesSavedToken(t *testing.T) {
	fixture := newPreparedFixedPointFixture(t)
	token, problem := fixture.core.prepareFixedPointWork(
		fixture.handle, fixture.request(),
	)
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, problem = fixture.core.abort(); problem.failed() {
		t.Fatal(problem)
	}
	if _, _, problem = fixture.core.consumeFixedPointWork(
		fixture.handle, token,
	); problem.code != privateWriterTransactionErrStaleHandle {
		t.Fatalf("saved token after abort = %#v", problem)
	}
	if fixture.core.pool.coordinatorSessionID != 0 ||
		fixture.core.pool.registeredWorkID != 0 {
		t.Fatal("abort retained coordinator pool authority")
	}
}

func TestPreparedFixedPointPreparationScalesWithTouchedScopeOnly(t *testing.T) {
	const plans = 128
	fixture := newPreparedFixedPointFixtureSized(t, plans)
	for index := 0; index < plans; index++ {
		request := fixture.request()
		request.workUnit = uint64(index + 1)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, request,
		)
		if problem.failed() {
			t.Fatalf("plan %d = %#v", index, problem)
		}
		if token.slot == nil || token.slot.scopePlan.visits != request.scopePages {
			t.Fatalf("plan %d touched %d nodes", index, token.slot.scopePlan.visits)
		}
	}
	if fixture.workspace.scopePreflightVisits != plans*2 {
		t.Fatalf("scope preflight visits = %d, want %d", fixture.workspace.scopePreflightVisits, plans*2)
	}
}

func TestPreparedFixedPoint4096SlotPreparationIsTouchedOnlyAndZeroAllocation(t *testing.T) {
	const (
		privatePages = 4096
		plans        = 130
	)
	fixture := newPreparedFixedPointFixtureCapacity(
		t, plans, privatePages, 64<<20,
	)
	if len(fixture.core.pool.slots) != privatePages {
		t.Fatalf("pool slots = %d, want %d", len(fixture.core.pool.slots), privatePages)
	}
	for work := 1; work <= plans; work++ {
		request := fixture.request()
		request.workUnit = uint64(work)
		token, problem := fixture.core.prepareFixedPointWork(
			fixture.handle, request,
		)
		if problem.failed() {
			t.Fatal(problem)
		}
		if token.slot.scopePlan.visits != request.scopePages {
			t.Fatal("preflight visited outside the requested scope")
		}
	}
	if fixture.workspace.nextPreparedSlot != plans ||
		fixture.workspace.scopePreflightVisits != plans*2 {
		t.Fatalf(
			"explicit preparation = slots %d visits %d, want %d/%d",
			fixture.workspace.nextPreparedSlot,
			fixture.workspace.scopePreflightVisits,
			plans, plans*2,
		)
	}

	const allocationRuns = 64
	allocationFixture := newPreparedFixedPointFixtureCapacity(
		t, allocationRuns+1, privatePages, 64<<20,
	)
	nextWork := uint64(1)
	allocations := testing.AllocsPerRun(allocationRuns, func() {
		request := allocationFixture.request()
		request.workUnit = nextWork
		nextWork++
		token, problem := allocationFixture.core.prepareFixedPointWork(
			allocationFixture.handle, request,
		)
		if problem.failed() {
			panic(problem)
		}
		if token.slot.scopePlan.visits != request.scopePages {
			panic("preflight visited outside the requested scope")
		}
	})
	if allocations != 0 {
		t.Fatalf("4096-slot preparation allocations = %f", allocations)
	}
	if allocationFixture.workspace.scopePreflightVisits !=
		(allocationRuns+1)*2 {
		t.Fatalf(
			"scope preflight visits = %d, want %d",
			allocationFixture.workspace.scopePreflightVisits,
			(allocationRuns+1)*2,
		)
	}
}

func TestPreparedFixedPointPrepareConsumeAbortAllocatesNothing(t *testing.T) {
	var core privateWriterTransactionCore
	var cleanupObligations [2]privateWriterCleanupObligation
	var cleanupOwners [2]privateWriterCleanupOwner
	workspace, workspaceProblem := newPrivateWriterWorkspace(
		privateWriterWorkspaceBudget{
			maxBytes: 1 << 20, privatePages: 32, records: 4, preparedSlots: 2,
			scratchWordsPerSlot: 8,
		},
		privateWriterResourceBudget{
			maxHeapBytes: 1 << 20, maxPrivatePages: 32,
			maxFileGrowthPages: 32, maxOpenFiles: 4,
		},
	)
	if workspaceProblem.failed() {
		t.Fatal(workspaceProblem)
	}
	sourcePages := [1]cowSparsePage{cowLeaf(t, 2, 1, 5, 9)}
	source := preparedWorkCallbackSource{
		cowSparsePages: &cowSparsePages{pages: sourcePages[:]},
	}
	selected := Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		DatabaseID:    [16]byte{1},
		TxnID:         1,
		CommitNonce:   [16]byte{2},
		PageCount:     20,
	}
	budget := privateWriterResourceBudget{
		maxHeapBytes: 1 << 20, maxPrivatePages: 32,
		maxFileGrowthPages: 32, maxOpenFiles: 4,
	}
	allocations := testing.AllocsPerRun(100, func() {
		if problem := initPrivateWriterTransactionCoreWithWorkspace(
			&core, selected, budget,
			workspace,
			cleanupObligations[:], cleanupOwners[:],
		); problem.failed() {
			panic(problem)
		}
		handle, problem := core.begin([16]byte{3})
		if problem.failed() {
			panic(problem)
		}
		source.core = &core
		source.handle = handle
		if problem = core.startPreparedFixedPoint(
			handle, &source, 2,
		); problem.failed() {
			panic(problem)
		}
		token, problem := core.prepareFixedPointWork(
			handle,
			privateWriterFixedPointPrepareRequest{
				workUnit: 1, expectedRoot: 2, expectedPageCount: 20,
				scopePages: 2,
			},
		)
		if problem.failed() {
			panic(problem)
		}
		if _, _, problem = core.consumeFixedPointWork(handle, token); problem.failed() {
			panic(problem)
		}
		if _, problem = core.abort(); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 {
		t.Fatalf("prepared fixed-point lifecycle allocations = %f", allocations)
	}
}

func TestPreparedFixedPointWorkspaceRejectsTinyWriterHeapBudget(t *testing.T) {
	workspaceBudget := privateWriterWorkspaceBudget{
		maxBytes: 1 << 20, privatePages: 32, records: 4,
		preparedSlots: 2, scratchWordsPerSlot: 8,
	}
	workspace, problem := newPrivateWriterWorkspace(
		workspaceBudget,
		privateWriterResourceBudget{maxHeapBytes: 1},
	)
	if workspace != nil || problem.code != privateWriterWorkspaceErrInvalidBudget ||
		problem.required <= problem.actual {
		t.Fatalf("tiny writer heap = workspace %#v problem %#v", workspace, problem)
	}
}

func TestPrivateWriterCommitRejectsGenericActiveScope(t *testing.T) {
	var core privateWriterTransactionCore
	var slots [2]privatePagePoolSlot
	var validation [2]uint32
	initPrivateWriterTestCore(t, &core, slots[:], validation[:], nil, nil)
	handle, problem := core.begin([16]byte{3})
	if problem.failed() {
		t.Fatal(problem)
	}
	if _, poolProblem := core.pool.reserveScope(1); poolProblem.failed() {
		t.Fatal(poolProblem)
	}
	if problem = core.preflightCommit(handle); problem.code != privateWriterTransactionErrAbortRequired ||
		core.state != privateWriterTransactionAbortRequired {
		t.Fatalf("active generic scope commit = %#v core %#v", problem, core)
	}
}

func TestPrivateWriterCommitRejectsEveryResidualWorkMarker(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*privateWriterTransactionCore)
	}{
		{"core work active", func(core *privateWriterTransactionCore) {
			core.fixedPointWorkActive = true
		}},
		{"core work id", func(core *privateWriterTransactionCore) {
			core.fixedPointRegisteredWorkID = 1
		}},
		{"core work generation", func(core *privateWriterTransactionCore) {
			core.fixedPointRegisteredWorkGeneration = 1
		}},
		{"core work phase", func(core *privateWriterTransactionCore) {
			core.fixedPointRegisteredWorkPhase = privateWriterFixedPointWorkPrepared
		}},
		{"core work fence", func(core *privateWriterTransactionCore) {
			core.fixedPointWorkFence = &privateWriterWorkFence{}
		}},
		{"pool work id", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkID = 1
		}},
		{"pool work generation", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkGeneration = 1
		}},
		{"pool work phase", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkPhase = uint8(privateWriterFixedPointWorkPrepared)
		}},
		{"pool work start epoch", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkStartEpoch = 1
		}},
		{"pool work mutation", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkMutation = true
		}},
		{"pool work fence", func(core *privateWriterTransactionCore) {
			core.pool.registeredWorkFence = &privateWriterWorkFence{}
		}},
		{"pool scope id", func(core *privateWriterTransactionCore) {
			core.pool.registeredScopeID = 1
		}},
		{"pool scope anchor", func(core *privateWriterTransactionCore) {
			core.pool.registeredScopeAnchor = 1
		}},
		{"pool unaccepted scope", func(core *privateWriterTransactionCore) {
			core.pool.unacceptedScopes = 1
		}},
		{"coordinator active prepared", func(core *privateWriterTransactionCore) {
			core.fixedPointCoordinator.activePrepared =
				&privateWriterFixedPointPreparedWork{}
		}},
		{"coordinator work fence", func(core *privateWriterTransactionCore) {
			core.fixedPointCoordinator.workFence = &privateWriterWorkFence{}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			var core privateWriterTransactionCore
			var slots [2]privatePagePoolSlot
			var validation [2]uint32
			initPrivateWriterTestCore(
				t, &core, slots[:], validation[:], nil, nil,
			)
			handle, problem := core.begin([16]byte{3})
			if problem.failed() {
				t.Fatal(problem)
			}
			mutation.mutate(&core)
			if problem = core.preflightCommit(
				handle,
			); problem.code != privateWriterTransactionErrAbortRequired ||
				core.state != privateWriterTransactionAbortRequired ||
				!core.pool.abortRequired {
				t.Fatalf("residual marker accepted: %#v", problem)
			}
		})
	}
}
