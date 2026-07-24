package exactv4

import "fmt"

// rangeRootTransactionProof is private evidence that a replacement range root
// cannot bypass retirement of the selected committed range tree. It remains
// detached from allocator ownership and target metadata until terminal
// composition consumes it.
type rangeRootTransactionProof struct {
	selected     rangeRootTransactionIdentity
	materialized rangeTreeMaterializedResult
	rangePages   []privateWriterProducedTerminalPage
	seed         *pageNumberIndex
	first        *pageNumberIndex
	second       *pageNumberIndex
	candidate    pageNumberIndexFixedPointCandidate
	protectedLen uint64
	seal         uint64
}

type rangeRootTransactionIdentity struct {
	txnID                uint64
	pageCount            uint64
	rangeRoot            uint32
	rangeRecordCount     uint64
	retirementRoot       uint32
	retirementBatchCount uint64
	addressFamily        AddressFamily
	valueKind            ValueKind
}

type rangeRootTransactionProofErrorCode uint8

const (
	rangeRootTransactionProofErrInvalidArgument rangeRootTransactionProofErrorCode = iota + 1
	rangeRootTransactionProofErrSelectedIdentity
	rangeRootTransactionProofErrRangeJournal
	rangeRootTransactionProofErrRangeRoot
	rangeRootTransactionProofErrOwnership
	rangeRootTransactionProofErrFixedPoint
	rangeRootTransactionProofErrProtectedOverlap
	rangeRootTransactionProofErrStale
)

type rangeRootTransactionProofError struct {
	code  rangeRootTransactionProofErrorCode
	page  uint32
	cause error
}

// rangeRootRetirementStage is private evidence that the proof's protected
// pages were appended to the selected retirement tree in the reservation that
// will later finalize the replacement range root. It has no metadata or
// publication authority on its own.
type rangeRootRetirementStage struct {
	attachment    *freeBitmapReservationAttachment
	proof         *rangeRootTransactionProof
	scope         privatePageReservationScope
	selectedTxn   uint64
	pendingTxn    uint64
	pageCount     uint64
	retirement    retirementTreeEditResult
	blobPages     int
	terminalPages int
	protectedLen  uint64
	seal          uint64
}

type rangeRootRetirementStageScratch struct {
	blobPages     []uint32
	path          []retirementPathFrame
	blobScanPages []retirementBlobScanPage
	replacements  []committedPageReplacement
	releases      []uint32
	roles         []pageRoleIndexSlot

	arena             privatePageArena
	token             retirementBlobToken
	blobScan          retirementBlobScanScratch
	replacementLedger committedReplacementLedger
	releaseBuffer     privateReleaseBuffer
	roleIndex         pageRoleIndex
	guard             guardedRetirementSource
}

type rangeRootRetirementStageErrorCode uint8

const (
	rangeRootRetirementStageErrInvalidArgument rangeRootRetirementStageErrorCode = iota + 1
	rangeRootRetirementStageErrPreMutationProof
	rangeRootRetirementStageErrPreMutationBitmap
	rangeRootRetirementStageErrPreMutationRetirement
	rangeRootRetirementStageErrPostMutationRetirement
	rangeRootRetirementStageErrPostMutationBitmap
	rangeRootRetirementStageErrPostMutationCapacity
)

type rangeRootRetirementStageError struct {
	code       rangeRootRetirementStageErrorCode
	proof      error
	bitmap     freeBitmapCOWError
	retirement retirementWriteError
	required   int
	actual     int
}

func (e rangeRootRetirementStageError) failed() bool { return e.code != 0 }

func (e rangeRootRetirementStageError) Error() string {
	return fmt.Sprintf("exact v4 range-root retirement stage: error %d", e.code)
}

func (e rangeRootRetirementStageError) Unwrap() error {
	if e.proof != nil {
		return e.proof
	}
	if e.bitmap.failed() {
		return e.bitmap
	}
	if e.retirement.failed() {
		return e.retirement
	}
	return nil
}

func (e rangeRootRetirementStageError) discardRequired() bool {
	return e.code == rangeRootRetirementStageErrPostMutationRetirement ||
		e.code == rangeRootRetirementStageErrPostMutationBitmap ||
		e.code == rangeRootRetirementStageErrPostMutationCapacity
}

func (e *rangeRootTransactionProofError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range-root transaction proof: error %d", e.code)
}

func (e *rangeRootTransactionProofError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func rangeRootTransactionIdentityFromMeta(
	selected Meta,
) (rangeRootTransactionIdentity, error) {
	if selected.TxnID == 0 || selected.PageCount < 2 ||
		(selected.RangeRoot == 0 && selected.RangeRecordCount != 0) ||
		(selected.RangeRoot != 0 &&
			(selected.RangeRoot < 2 || uint64(selected.RangeRoot) >= selected.PageCount)) ||
		selected.RetirementBatchCount > selected.TxnID-1 ||
		(selected.RetirementRoot == 0 && selected.RetirementBatchCount != 0) ||
		(selected.RetirementRoot != 0 &&
			(selected.RetirementBatchCount == 0 || selected.RetirementRoot < 2 ||
				uint64(selected.RetirementRoot) >= selected.PageCount)) {
		return rangeRootTransactionIdentity{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrSelectedIdentity,
		}
	}
	return rangeRootTransactionIdentity{
		txnID:                selected.TxnID,
		pageCount:            selected.PageCount,
		rangeRoot:            selected.RangeRoot,
		rangeRecordCount:     selected.RangeRecordCount,
		retirementRoot:       selected.RetirementRoot,
		retirementBatchCount: selected.RetirementBatchCount,
		addressFamily:        selected.AddressFamily,
		valueKind:            selected.ValueKind,
	}, nil
}

func validateRangeRootTransactionJournal(
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
) error {
	if materialized.pageCount < 0 || materialized.pageCount != len(rangePages) ||
		(materialized.rootPage == 0 &&
			(materialized.pageCount != 0 || materialized.recordCount != 0)) ||
		(materialized.rootPage != 0 && materialized.rootPage < 2) ||
		(materialized.recordCount != 0 && materialized.rootPage == 0) {
		return &rangeRootTransactionProofError{code: rangeRootTransactionProofErrRangeJournal}
	}
	if problem := validatePrivateWriterTerminalJournalSource(0, rangePages); problem.failed() {
		return &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrRangeJournal,
			page: problem.page,
		}
	}
	foundRoot := materialized.rootPage == 0
	for _, page := range rangePages {
		if page.owner != privatePageOwnerRange || page.origin != privatePageRange {
			return &rangeRootTransactionProofError{
				code: rangeRootTransactionProofErrRangeJournal,
				page: page.pageNumber,
			}
		}
		if page.pageNumber == materialized.rootPage {
			foundRoot = true
		}
	}
	if !foundRoot {
		return &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrRangeRoot,
			page: materialized.rootPage,
		}
	}
	return nil
}

func rangeRootTransactionProofCandidate(
	candidate pageNumberIndexFixedPointCandidate,
	first, second *pageNumberIndex,
) *pageNumberIndex {
	switch candidate {
	case pageNumberIndexFixedPointFirst:
		return first
	case pageNumberIndexFixedPointSecond:
		return second
	default:
		return nil
	}
}

func rangeRootTransactionProofOtherCandidate(
	candidate pageNumberIndexFixedPointCandidate,
	first, second *pageNumberIndex,
) *pageNumberIndex {
	switch candidate {
	case pageNumberIndexFixedPointFirst:
		return second
	case pageNumberIndexFixedPointSecond:
		return first
	default:
		return nil
	}
}

func rangeRootTransactionProofDisjoint(
	rangePages []privateWriterProducedTerminalPage,
	protected *pageNumberIndex,
) error {
	cursor, err := newPageNumberIndexCursor(protected)
	if err != nil {
		return &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	protectedPage, available, err := cursor.next()
	if err != nil {
		return &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	for _, rangePage := range rangePages {
		for available && protectedPage < rangePage.pageNumber {
			protectedPage, available, err = cursor.next()
			if err != nil {
				return &rangeRootTransactionProofError{
					code:  rangeRootTransactionProofErrFixedPoint,
					cause: err,
				}
			}
		}
		if available && protectedPage == rangePage.pageNumber {
			return &rangeRootTransactionProofError{
				code: rangeRootTransactionProofErrProtectedOverlap,
				page: rangePage.pageNumber,
			}
		}
	}
	return nil
}

func rangeRootTransactionProofHashIndex(
	hash uint64,
	index *pageNumberIndex,
) (uint64, error) {
	cursor, err := newPageNumberIndexCursor(index)
	if err != nil {
		return 0, err
	}
	for {
		page, available, nextErr := cursor.next()
		if nextErr != nil {
			return 0, nextErr
		}
		if !available {
			return hash, nil
		}
		hash = privateWriterAggregateHashWord(hash, uint64(page))
	}
}

func sealRangeRootTransactionProof(
	selected rangeRootTransactionIdentity,
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
	seed, protected *pageNumberIndex,
	candidate pageNumberIndexFixedPointCandidate,
) (uint64, error) {
	hash := privateWriterAggregateHashSeed ^ 0x98f0_4adf_c3e2_719b
	for _, value := range [...]uint64{
		selected.txnID,
		selected.pageCount,
		uint64(selected.rangeRoot),
		selected.rangeRecordCount,
		uint64(selected.retirementRoot),
		selected.retirementBatchCount,
		uint64(selected.addressFamily),
		uint64(selected.valueKind),
		uint64(materialized.rootPage),
		uint64(materialized.rootLevel),
		materialized.recordCount,
		uint64(materialized.pageCount),
		uint64(candidate),
		uint64(len(rangePages)),
	} {
		hash = privateWriterAggregateHashWord(hash, value)
	}
	for _, page := range rangePages {
		hash = privateWriterProducedPageHash(hash, page)
	}
	if hash, err := rangeRootTransactionProofHashIndex(hash, seed); err != nil {
		return 0, err
	} else {
		return rangeRootTransactionProofHashIndex(hash, protected)
	}
}

func (proof *rangeRootTransactionProof) protectedIndex() (*pageNumberIndex, error) {
	if proof == nil || proof.seed == nil || proof.first == nil || proof.second == nil ||
		proof.seed == proof.first || proof.seed == proof.second || proof.first == proof.second {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale}
	}
	protected := rangeRootTransactionProofCandidate(proof.candidate, proof.first, proof.second)
	other := rangeRootTransactionProofOtherCandidate(proof.candidate, proof.first, proof.second)
	if protected == nil || other == nil || !other.isEmptyAndClean() ||
		protected.len() != proof.protectedLen ||
		validatePageNumberIndexCommittedRange(proof.seed, proof.selected.pageCount) != nil ||
		validatePageNumberIndexCommittedRange(protected, proof.selected.pageCount) != nil ||
		validateRangeRootTransactionJournal(proof.materialized, proof.rangePages) != nil ||
		rangeRootTransactionProofDisjoint(proof.rangePages, protected) != nil {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale}
	}
	seal, err := sealRangeRootTransactionProof(
		proof.selected, proof.materialized, proof.rangePages, proof.seed, protected, proof.candidate,
	)
	if err != nil || seal != proof.seal {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale, cause: err}
	}
	return protected, nil
}

// retirementInputs returns the selected retirement state and protected index
// after rechecking the proof. The later composition obtains its reader only
// from its bound bitmap reservation, never from this proof's caller.
func (proof *rangeRootTransactionProof) retirementInputs() (
	retirementTreeState,
	*pageNumberIndex,
	error,
) {
	protected, err := proof.protectedIndex()
	if err != nil {
		return retirementTreeState{}, nil, err
	}
	return retirementTreeState{
		selectedTxn: proof.selected.txnID,
		pageCount:   proof.selected.pageCount,
		root:        proof.selected.retirementRoot,
		batchCount:  proof.selected.retirementBatchCount,
	}, protected, nil
}

func sealRangeRootRetirementStage(stage rangeRootRetirementStage) uint64 {
	if stage.attachment == nil || stage.proof == nil || stage.attachment.cow.pool == nil {
		return 0
	}
	hash := privateWriterAggregateHashSeed ^ 0x7a2f_5db9_c190_4e63
	for _, value := range [...]uint64{
		stage.proof.seal,
		stage.selectedTxn,
		stage.pendingTxn,
		stage.pageCount,
		stage.scope.id,
		uint64(stage.scope.anchor + 1),
		stage.scope.generation,
		uint64(stage.retirement.root),
		stage.retirement.batchCount,
		uint64(stage.retirement.privatePages),
		uint64(stage.retirement.committedReplacements),
		uint64(stage.blobPages),
		uint64(stage.terminalPages),
		stage.protectedLen,
		freeBitmapReservationCOWFingerprint(&stage.attachment.cow),
		freeBitmapReservationScopeFingerprint(stage.attachment.cow.pool, stage.scope),
	} {
		hash = privateWriterAggregateHashWord(hash, value)
	}
	return hash
}

func rangeRootRetirementStageProofStateError() rangeRootRetirementStageError {
	return rangeRootRetirementStageError{
		code:  rangeRootRetirementStageErrPreMutationProof,
		proof: &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale},
	}
}

// verify rechecks the private capability before the later terminal boundary
// consumes it. It intentionally does not touch target metadata or file bytes.
func (stage *rangeRootRetirementStage) verify() rangeRootRetirementStageError {
	if stage == nil || stage.attachment == nil || stage.proof == nil ||
		stage.attachment.cow.pool == nil || stage.scope.pool != stage.attachment.cow.pool ||
		!stage.attachment.cow.scoped || stage.attachment.cow.scope != stage.scope {
		return rangeRootRetirementStageError{code: rangeRootRetirementStageErrInvalidArgument}
	}
	if stage.attachment.cow.pool.abortRequired {
		return rangeRootRetirementStageError{
			code:   rangeRootRetirementStageErrPostMutationBitmap,
			bitmap: bitmapPoolError(privatePagePoolError{code: privatePagePoolErrAbortRequired}),
		}
	}
	state, protected, err := stage.proof.retirementInputs()
	if err != nil || state.selectedTxn != stage.selectedTxn || state.pageCount != stage.pageCount ||
		protected.len() != stage.protectedLen {
		problem := rangeRootRetirementStageProofStateError()
		if err != nil {
			problem.proof = err
		}
		return problem
	}
	p := stage.attachment
	expectedPending := state.selectedTxn + 1
	if expectedPending == 0 || p.selectedTxn != state.selectedTxn ||
		p.committedPageCount != state.pageCount || p.cow.selectedTxn != state.selectedTxn ||
		p.cow.committedPageCount != state.pageCount || p.cow.pendingTxn != expectedPending ||
		stage.pendingTxn != expectedPending || stage.scope.pendingTxn != expectedPending {
		return rangeRootRetirementStageProofStateError()
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPreMutationBitmap, bitmap: problem,
		}
	}
	if stage.protectedLen == 0 {
		if stage.retirement.root != state.root || stage.retirement.batchCount != state.batchCount ||
			stage.retirement.privatePages != 0 || stage.retirement.committedReplacements != 0 ||
			stage.blobPages != 0 || stage.terminalPages != 0 {
			return rangeRootRetirementStageProofStateError()
		}
	} else {
		if state.batchCount == ^uint64(0) || stage.retirement.root < 2 ||
			uint64(stage.retirement.root) >= p.cow.pageCount ||
			stage.retirement.batchCount != state.batchCount+1 || stage.retirement.privatePages < 0 ||
			stage.retirement.committedReplacements < 0 || stage.blobPages <= 0 ||
			stage.terminalPages < stage.blobPages ||
			stage.terminalPages != stage.blobPages+stage.retirement.privatePages {
			return rangeRootRetirementStageProofStateError()
		}
	}
	if stage.seal == 0 || stage.seal != sealRangeRootRetirementStage(*stage) {
		return rangeRootRetirementStageProofStateError()
	}
	return rangeRootRetirementStageError{}
}

// discardAfterAbort returns proof scratch only after the whole private draft is
// being discarded. A successful stage remains live until terminal composition
// consumes it.
func (stage *rangeRootRetirementStage) discardAfterAbort() {
	if stage == nil {
		return
	}
	if stage.proof != nil {
		stage.proof.discardAfterAbort()
	}
	*stage = rangeRootRetirementStage{}
}

// stageRangeRootRetirement appends the proof's already-converged protected
// pages to the selected retirement tree inside the exact reservation that will
// finalize the replacement bitmap/range scope. The source is deliberately
// taken only from that reservation, never from the proof caller.
func stageRangeRootRetirement(
	p *freeBitmapReservationAttachment,
	proof *rangeRootTransactionProof,
	scratch *rangeRootRetirementStageScratch,
) (rangeRootRetirementStage, rangeRootRetirementStageError) {
	if p == nil || proof == nil || scratch == nil || p.cow.pool == nil ||
		p.scope.pool != p.cow.pool || !p.cow.scoped || p.cow.scope != p.scope ||
		p.cow.committed == nil {
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrInvalidArgument,
		}
	}
	if p.cow.pool.abortRequired {
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code:   rangeRootRetirementStageErrPostMutationBitmap,
			bitmap: bitmapPoolError(privatePagePoolError{code: privatePagePoolErrAbortRequired}),
		}
	}
	state, protected, err := proof.retirementInputs()
	if err != nil {
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPreMutationProof, proof: err,
		}
	}
	expectedPending := state.selectedTxn + 1
	if expectedPending == 0 || p.selectedTxn != state.selectedTxn ||
		p.committedPageCount != state.pageCount || p.cow.selectedTxn != state.selectedTxn ||
		p.cow.committedPageCount != state.pageCount || p.cow.pendingTxn != expectedPending ||
		p.scope.pendingTxn != expectedPending {
		return rangeRootRetirementStage{}, rangeRootRetirementStageProofStateError()
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPreMutationBitmap, bitmap: problem,
		}
	}
	if protected.len() == 0 {
		stage := rangeRootRetirementStage{
			attachment: p, proof: proof, scope: p.scope,
			selectedTxn: state.selectedTxn, pendingTxn: expectedPending, pageCount: state.pageCount,
			retirement: retirementTreeEditResult{
				root: state.root, batchCount: state.batchCount,
			},
		}
		stage.seal = sealRangeRootRetirementStage(stage)
		if stage.seal == 0 {
			return rangeRootRetirementStage{}, rangeRootRetirementStageProofStateError()
		}
		return stage, rangeRootRetirementStageError{}
	}

	arena, problem := newPrivatePageArenaInScope(p.cow.pool, p.scope, expectedPending)
	if problem.failed() {
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPreMutationRetirement, retirement: problem,
		}
	}
	scratch.arena = arena
	scratch.token, problem = buildRetirementBlobFromIndex(
		protected, &scratch.arena, &blobBuildScratch{pageNumbers: scratch.blobPages},
	)
	if problem.failed() {
		code := rangeRootRetirementStageErrPreMutationRetirement
		if p.cow.pool.abortRequired {
			code = rangeRootRetirementStageErrPostMutationRetirement
		}
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: code, retirement: problem,
		}
	}
	scratch.blobScan = retirementBlobScanScratch{pages: scratch.blobScanPages}
	scratch.replacementLedger = newCommittedReplacementLedger(scratch.replacements)
	scratch.releaseBuffer = newPrivateReleaseBuffer(scratch.releases)
	scratch.roleIndex = newPageRoleIndex(scratch.roles)
	result, problem := upsertNewestRetirementInScopeWithGuard(
		&scratch.guard, p.cow.committed, state, &scratch.token, scratch.path,
		&scratch.blobScan,
		&scratch.replacementLedger, &scratch.releaseBuffer, &scratch.roleIndex,
	)
	if problem.failed() {
		// The blob is already committed to the shared scoped arena. Even when
		// the editor cleans its local token, retrying this draft would reuse a
		// mutated allocator generation, so only whole-draft abort is safe.
		p.cow.pool.abortRequired = true
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPostMutationRetirement, retirement: problem,
		}
	}
	terminalPages, ok := checkedIntAdd(scratch.token.privatePages, result.privatePages)
	if !ok || terminalPages <= 0 || terminalPages > p.privatePages {
		p.cow.pool.abortRequired = true
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code:     rangeRootRetirementStageErrPostMutationCapacity,
			required: terminalPages, actual: p.privatePages,
		}
	}
	if bitmapProblem := p.cow.synchronizeScopedBindings(p.scope); bitmapProblem.failed() {
		p.cow.pool.abortRequired = true
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPostMutationBitmap, bitmap: bitmapProblem,
		}
	}
	stage := rangeRootRetirementStage{
		attachment: p, proof: proof, scope: p.scope,
		selectedTxn: state.selectedTxn, pendingTxn: expectedPending, pageCount: state.pageCount,
		retirement: result, blobPages: scratch.token.privatePages, terminalPages: terminalPages,
		protectedLen: protected.len(),
	}
	stage.seal = sealRangeRootRetirementStage(stage)
	if stage.seal == 0 {
		p.cow.pool.abortRequired = true
		return rangeRootRetirementStage{}, rangeRootRetirementStageError{
			code: rangeRootRetirementStageErrPostMutationRetirement,
		}
	}
	return stage, rangeRootRetirementStageError{}
}

func (proof *rangeRootTransactionProof) discardAfterAbort() {
	if proof == nil {
		return
	}
	proof.seed.discardAfterAbort()
	proof.first.discardAfterAbort()
	proof.second.discardAfterAbort()
	*proof = rangeRootTransactionProof{}
}

// prepareRangeRootTransactionProof collects the selected old range tree,
// converges every prospective committed replacement page, and retains only
// private evidence. It deliberately does not create retirement output, bind a
// terminal journal, change target metadata, or publish a root.
func prepareRangeRootTransactionProof[K rangeKey[K], S committedPageSource](
	source S,
	selected Meta,
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
	seed, first, second *pageNumberIndex,
	ownershipScratch *rangeTreeOwnershipScratch,
	maxRangeWork uint64,
	maxIterations int,
	preview pageNumberIndexFixedPointPreview,
) (rangeRootTransactionProof, error) {
	identity, err := rangeRootTransactionIdentityFromMeta(selected)
	if err != nil {
		return rangeRootTransactionProof{}, err
	}
	if seed == nil || first == nil || second == nil || ownershipScratch == nil ||
		seed == first || seed == second || first == second ||
		seed.workspace == nil || first.workspace == nil || second.workspace == nil ||
		seed.workspace == first.workspace || seed.workspace == second.workspace || first.workspace == second.workspace ||
		!seed.isEmptyAndClean() || !first.isEmptyAndClean() || !second.isEmptyAndClean() ||
		preview == nil || maxIterations <= 0 {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrInvalidArgument,
		}
	}
	if err = validateRangeRootTransactionJournal(materialized, rangePages); err != nil {
		return rangeRootTransactionProof{}, err
	}
	completed := false
	defer func() {
		if !completed {
			seed.discardAfterAbort()
			first.discardAfterAbort()
			second.discardAfterAbort()
		}
	}()
	if _, err = collectRangeTreeOwnership[K](source, selected, seed, ownershipScratch, maxRangeWork); err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrOwnership,
			cause: err,
		}
	}
	candidate, err := convergePageNumberIndex(
		seed, first, second, identity.pageCount, maxIterations, preview,
	)
	if err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	protected := rangeRootTransactionProofCandidate(candidate, first, second)
	other := rangeRootTransactionProofOtherCandidate(candidate, first, second)
	if protected == nil || other == nil || !other.isEmptyAndClean() ||
		validatePageNumberIndexCommittedRange(protected, identity.pageCount) != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrFixedPoint,
		}
	}
	if err = rangeRootTransactionProofDisjoint(rangePages, protected); err != nil {
		return rangeRootTransactionProof{}, err
	}
	seal, err := sealRangeRootTransactionProof(identity, materialized, rangePages, seed, protected, candidate)
	if err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	completed = true
	return rangeRootTransactionProof{
		selected: identity, materialized: materialized, rangePages: rangePages,
		seed: seed, first: first, second: second, candidate: candidate,
		protectedLen: protected.len(), seal: seal,
	}, nil
}
