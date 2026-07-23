package exactv4

import "reflect"

type privateWriterProducedTerminalPage struct {
	pageNumber      uint32
	authorization   privatePageAuthorization
	owner           privatePageOwner
	origin          privatePageOrigin
	committedOrigin uint32
	bytes           [PageSize]byte
}

type privateWriterProducedBitmapTerminalContent struct {
	pages     []privateWriterProducedTerminalPage
	prior     []privateWriterDraftPageProvenance
	root      uint32
	pageCount uint64

	selectedTxn        uint64
	pendingTxn         uint64
	committedPageCount uint64
	pageLen            int
	priorLen           int
	released           unusedReservationRelease
	reinserted         int
	producerSeal       uint64
	priorSealed        uint64
}

type privateWriterProducedBitmapTerminalSlot struct {
	self       *privateWriterProducedBitmapTerminalSlot
	generation uint64
	nonce      uint64
	ready      bool
	content    privateWriterProducedBitmapTerminalContent
}

type privateWriterProducedBitmapTerminal struct {
	slot       *privateWriterProducedBitmapTerminalSlot
	generation uint64
	nonce      uint64
}

type privateWriterProducedRetirementTerminalContent struct {
	pages     []privateWriterProducedTerminalPage
	prior     []privateWriterDraftPageProvenance
	result    retirementTreeEditResult
	pageLen   int
	priorLen  int
	seal      uint64
	priorSeal uint64
}

type privateWriterProducedRetirementTerminalSlot struct {
	self       *privateWriterProducedRetirementTerminalSlot
	generation uint64
	nonce      uint64
	ready      bool
	content    privateWriterProducedRetirementTerminalContent
}

type privateWriterProducedRetirementTerminal struct {
	slot       *privateWriterProducedRetirementTerminalSlot
	generation uint64
	nonce      uint64
}

const privateWriterAggregateHashSeed = uint64(0xcbf29ce484222325)

func privateWriterAggregateHashWord(hash, value uint64) uint64 {
	hash ^= value
	hash *= 0x100000001b3
	return hash
}

func privateWriterProducedPageHash(
	hash uint64,
	page privateWriterProducedTerminalPage,
) uint64 {
	hash = privateWriterAggregateHashWord(hash, uint64(page.pageNumber))
	hash = privateWriterAggregateHashWord(hash, uint64(page.authorization))
	hash = privateWriterAggregateHashWord(hash, uint64(page.owner))
	hash = privateWriterAggregateHashWord(hash, uint64(page.origin))
	hash = privateWriterAggregateHashWord(hash, uint64(page.committedOrigin))
	for offset := 0; offset < len(page.bytes); offset += 8 {
		hash = privateWriterAggregateHashWord(
			hash,
			uint64(page.bytes[offset])|
				uint64(page.bytes[offset+1])<<8|
				uint64(page.bytes[offset+2])<<16|
				uint64(page.bytes[offset+3])<<24|
				uint64(page.bytes[offset+4])<<32|
				uint64(page.bytes[offset+5])<<40|
				uint64(page.bytes[offset+6])<<48|
				uint64(page.bytes[offset+7])<<56,
		)
	}
	return hash
}

func privateWriterProvenanceHash(
	hash uint64,
	provenance privateWriterDraftPageProvenance,
) uint64 {
	hash = privateWriterAggregateHashWord(hash, provenance.workUnit)
	hash = privateWriterAggregateHashWord(hash, provenance.scopeID)
	hash = privateWriterAggregateHashWord(hash, uint64(provenance.scopeAnchor+1))
	hash = privateWriterAggregateHashWord(hash, uint64(provenance.slot+1))
	hash = privateWriterAggregateHashWord(hash, uint64(provenance.pageNumber))
	hash = privateWriterAggregateHashWord(hash, provenance.bindingEpoch)
	hash = privateWriterAggregateHashWord(hash, uint64(provenance.owner))
	hash = privateWriterAggregateHashWord(hash, uint64(provenance.origin))
	return privateWriterAggregateHashWord(hash, provenance.generation)
}

func sealPrivateWriterProducedBitmap(
	produced *privateWriterProducedBitmapTerminalContent,
) uint64 {
	hash := privateWriterAggregateHashSeed
	hash = privateWriterAggregateHashWord(hash, uint64(produced.root))
	hash = privateWriterAggregateHashWord(hash, produced.pageCount)
	hash = privateWriterAggregateHashWord(hash, produced.selectedTxn)
	hash = privateWriterAggregateHashWord(hash, produced.pendingTxn)
	hash = privateWriterAggregateHashWord(hash, produced.committedPageCount)
	hash = privateWriterAggregateHashWord(hash, uint64(produced.pageLen))
	hash = privateWriterAggregateHashWord(hash, uint64(produced.priorLen))
	for index := 0; index < produced.pageLen; index++ {
		hash = privateWriterProducedPageHash(hash, produced.pages[index])
	}
	for index := 0; index < produced.priorLen; index++ {
		hash = privateWriterProvenanceHash(hash, produced.prior[index])
	}
	return hash
}

func sealPrivateWriterProducedPrior(
	prior []privateWriterDraftPageProvenance,
) uint64 {
	hash := privateWriterAggregateHashSeed ^ 0x3c6ef372fe94f82b
	hash = privateWriterAggregateHashWord(hash, uint64(len(prior)))
	for _, provenance := range prior {
		hash = privateWriterProvenanceHash(hash, provenance)
	}
	return hash
}

func privateWriterProducedScratchCanonical[T comparable](values []T) bool {
	var zero T
	for _, value := range values {
		if value != zero {
			return false
		}
	}
	return true
}

func (p *freeBitmapReservationAttachment) finalizeFixedPointBitmapProducer(
	scratch freeBitmapFinalizationScratch,
	workspace *privateWriterFixedPointAggregateWorkspace,
) (
	producedToken privateWriterProducedBitmapTerminal,
	fixedProblem privateWriterFixedPointError,
) {
	if p == nil || workspace == nil || workspace.self != workspace ||
		workspace.bitmapProducer.ready ||
		len(workspace.bitmapPages) < p.privatePages ||
		!privateWriterProducedScratchCanonical(workspace.bitmapPages) ||
		!privateWriterProducedScratchCanonical(workspace.bitmapPrior) {
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrScratchTooSmall,
		}
	}
	pageScratch := workspace.bitmapPages
	priorScratch := workspace.bitmapPrior
	result, bitmapProblem := p.finalize(scratch)
	if bitmapProblem.failed() {
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, bitmap: bitmapProblem,
		}
	}
	predecessor, bitmapProblem := result.successor.consume()
	if bitmapProblem.failed() {
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, bitmap: bitmapProblem,
		}
	}
	cleanupPending := true
	defer func() {
		if !cleanupPending {
			return
		}
		cleanupProblem := predecessor.cleanup()
		if cleanupProblem.failed() && !fixedProblem.failed() {
			producedToken = privateWriterProducedBitmapTerminal{}
			fixedProblem = privateWriterFixedPointError{
				code:   privateWriterFixedPointErrStaleProvenance,
				bitmap: cleanupProblem,
			}
		}
	}()
	pageLen := 0
	priorLen := 0
	clearProduced := func() {
		clear(pageScratch[:pageLen])
		clear(priorScratch[:priorLen])
	}
	output := result.output
	if output.pool == nil || output.boundLen < 0 ||
		output.boundLen > len(output.bindings) ||
		output.boundLen > len(pageScratch) {
		clearProduced()
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance,
		}
	}
	for index := 0; index < output.boundLen; index++ {
		binding := output.bindings[index]
		if binding.poolSlot < 0 || binding.poolSlot >= len(output.pool.slots) {
			clearProduced()
			return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
				code: privateWriterFixedPointErrStaleProvenance,
			}
		}
		slot := &output.pool.slots[binding.poolSlot]
		if !slot.bound || slot.pageNumber != binding.pageNumber ||
			slot.epoch != binding.poolEpoch ||
			slot.state != privatePageInUse || !slot.inUse ||
			slot.owner != privatePageOwnerBitmap ||
			slot.origin != privatePageBitmap {
			clearProduced()
			return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
				code: privateWriterFixedPointErrStaleProvenance,
				page: binding.pageNumber,
			}
		}
		pageScratch[index] = privateWriterProducedTerminalPage{
			pageNumber: binding.pageNumber, authorization: slot.authorization,
			owner: slot.owner, origin: slot.origin,
			committedOrigin: slot.committedOrigin, bytes: slot.bytes,
		}
		pageLen++
	}
	draft := privateWriterDraftSourceFrom(p.cow.committed)
	for _, pageNumber := range p.cow.replacementPages() {
		if draft == nil {
			break
		}
		residence, problem := draft.residence(pageNumber)
		if problem.failed() {
			clearProduced()
			return privateWriterProducedBitmapTerminal{}, problem
		}
		if residence.kind != privateWriterPagePriorScopePrivate {
			continue
		}
		if priorLen == len(priorScratch) {
			clearProduced()
			return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
				code: privateWriterFixedPointErrScratchTooSmall,
			}
		}
		priorScratch[priorLen] = residence.provenance
		priorLen++
	}
	produced := privateWriterProducedBitmapTerminalContent{
		pages: pageScratch, prior: priorScratch,
		root: output.root, pageCount: output.pageCount,
		selectedTxn: output.selectedTxn, pendingTxn: output.pendingTxn,
		committedPageCount: output.committedPageCount,
		pageLen:            output.boundLen, priorLen: priorLen,
		released: result.released, reinserted: result.reinsertedReclaimed,
	}
	produced.priorSealed = sealPrivateWriterProducedPrior(
		produced.prior[:produced.priorLen],
	)
	produced.producerSeal = sealPrivateWriterProducedBitmap(&produced)
	bitmapProblem = predecessor.cleanup()
	cleanupPending = false
	if bitmapProblem.failed() {
		clearProduced()
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, bitmap: bitmapProblem,
		}
	}
	if workspace.bitmapProducer.generation == ^uint64(0) {
		clearProduced()
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrExhausted,
		}
	}
	nonce, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		clearProduced()
		return privateWriterProducedBitmapTerminal{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrExhausted,
		}
	}
	slot := &workspace.bitmapProducer
	generation := slot.generation + 1
	*slot = privateWriterProducedBitmapTerminalSlot{
		self: slot, generation: generation, nonce: nonce, ready: true,
		content: produced,
	}
	return privateWriterProducedBitmapTerminal{
		slot: slot, generation: generation, nonce: nonce,
	}, privateWriterFixedPointError{}
}

func (p privateWriterProducedBitmapTerminal) authority(
	canonical *privateWriterProducedBitmapTerminalSlot,
) (*privateWriterProducedBitmapTerminalContent, bool) {
	if canonical == nil || p.slot != canonical || canonical.self != canonical ||
		!canonical.ready || p.generation == 0 ||
		p.generation != canonical.generation || p.nonce == 0 ||
		p.nonce != canonical.nonce {
		return nil, false
	}
	content := &canonical.content
	return content, content.producerSeal != 0 && content.priorSealed != 0 &&
		content.pageLen > 0 && content.pageLen <= len(content.pages) &&
		content.priorLen >= 0 && content.priorLen <= len(content.prior) &&
		content.producerSeal == sealPrivateWriterProducedBitmap(content) &&
		content.priorSealed ==
			sealPrivateWriterProducedPrior(content.prior[:content.priorLen])
}

func sealPrivateWriterProducedRetirement(
	content *privateWriterProducedRetirementTerminalContent,
) uint64 {
	hash := privateWriterAggregateHashSeed ^ 0xa54ff53a5f1d36f1
	hash = privateWriterAggregateHashWord(hash, uint64(content.result.root))
	hash = privateWriterAggregateHashWord(hash, content.result.batchCount)
	hash = privateWriterAggregateHashWord(hash, uint64(content.result.privatePages))
	hash = privateWriterAggregateHashWord(
		hash, uint64(content.result.committedReplacements),
	)
	for index := 0; index < content.pageLen; index++ {
		hash = privateWriterProducedPageHash(hash, content.pages[index])
	}
	return hash
}

func (w *privateWriterFixedPointAggregateWorkspace) prepareEmptyRetirementProducer() (
	privateWriterProducedRetirementTerminal,
	privateWriterFixedPointError,
) {
	if w == nil || w.self != w || w.retirementProducer.ready ||
		!privateWriterProducedScratchCanonical(w.retirementPages) ||
		!privateWriterProducedScratchCanonical(w.retirementPrior) {
		return privateWriterProducedRetirementTerminal{},
			privateWriterFixedPointError{
				code: privateWriterFixedPointErrStaleProvenance,
			}
	}
	slot := &w.retirementProducer
	if slot.generation == ^uint64(0) {
		return privateWriterProducedRetirementTerminal{},
			privateWriterFixedPointError{code: privateWriterFixedPointErrExhausted}
	}
	nonce, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		return privateWriterProducedRetirementTerminal{},
			privateWriterFixedPointError{code: privateWriterFixedPointErrExhausted}
	}
	content := privateWriterProducedRetirementTerminalContent{
		pages: w.retirementPages[:0:0],
		prior: w.retirementPrior[:0:0],
	}
	content.seal = sealPrivateWriterProducedRetirement(&content)
	content.priorSeal = sealPrivateWriterProducedPrior(content.prior)
	generation := slot.generation + 1
	*slot = privateWriterProducedRetirementTerminalSlot{
		self: slot, generation: generation, nonce: nonce, ready: true,
		content: content,
	}
	return privateWriterProducedRetirementTerminal{
		slot: slot, generation: generation, nonce: nonce,
	}, privateWriterFixedPointError{}
}

func (p privateWriterProducedRetirementTerminal) authority(
	canonical *privateWriterProducedRetirementTerminalSlot,
) (*privateWriterProducedRetirementTerminalContent, bool) {
	if canonical == nil || p.slot != canonical || canonical.self != canonical ||
		!canonical.ready || p.generation == 0 ||
		p.generation != canonical.generation || p.nonce == 0 ||
		p.nonce != canonical.nonce {
		return nil, false
	}
	content := &canonical.content
	if content.pageLen < 0 || content.pageLen > len(content.pages) ||
		content.priorLen < 0 || content.priorLen > len(content.prior) ||
		content.seal == 0 || content.priorSeal == 0 {
		return nil, false
	}
	return content,
		content.seal == sealPrivateWriterProducedRetirement(content) &&
			content.priorSeal ==
				sealPrivateWriterProducedPrior(content.prior[:content.priorLen])
}

type privateWriterFixedPointAggregateWorkspaceBudget struct {
	maxBytes      uint64
	privatePages  int
	terminalPages int
	priorReturns  int
	touchedSlots  int
}

type privateWriterAggregateReplaySlot struct {
	slotIndex int
	before    privatePagePoolSlot
	after     privatePagePoolSlot
}

type privateWriterAggregatePriorBinding struct {
	provenance   privateWriterDraftPageProvenance
	recordIndex  int
	bindingIndex int
}

type privateWriterFixedPointPreparedAggregate struct {
	self       *privateWriterFixedPointPreparedAggregate
	workspace  *privateWriterFixedPointAggregateWorkspace
	core       *privateWriterTransactionCore
	base       privateWriterFixedPointPreparedToken
	generation uint64
	nonce      uint64

	pagesLen    int
	priorLen    int
	replayLen   int
	recordIndex int
	scope       privatePageReservationScope
	bitmap      privateWriterProducedBitmapTerminalContent
	retirement  retirementTreeEditResult
	output      sealedFreeBitmapOutput
	poolAfter   privateWriterAggregatePoolAfter
	seal        uint64
}

type privateWriterFixedPointAggregateToken struct {
	slot       *privateWriterFixedPointPreparedAggregate
	workspace  *privateWriterFixedPointAggregateWorkspace
	generation uint64
	nonce      uint64
}

type privateWriterFixedPointAggregateResult struct {
	active      privateWriterFixedPointActiveToken
	bitmap      privateWriterProducedBitmapTerminalContent
	retirement  retirementTreeEditResult
	recordIndex int
}

type privateWriterAggregatePoolAfter struct {
	pendingPageCount    uint64
	generation          uint64
	mutationEpoch       uint64
	checkpointSequence  uint64
	indexRoot           int
	scopeSequence       uint64
	activeScopes        int
	unscopedVacantHead  int
	unscopedVacantTail  int
	unscopedVacantCount int
}

type privateWriterFixedPointAggregateWorkspace struct {
	self             *privateWriterFixedPointAggregateWorkspace
	partitionBytes   uint64
	writerHeapBudget uint64
	privatePages     int

	slot               privateWriterFixedPointPreparedAggregate
	bitmapProducer     privateWriterProducedBitmapTerminalSlot
	retirementProducer privateWriterProducedRetirementTerminalSlot
	bitmapPages        []privateWriterProducedTerminalPage
	bitmapPrior        []privateWriterDraftPageProvenance
	retirementPages    []privateWriterProducedTerminalPage
	retirementPrior    []privateWriterDraftPageProvenance
	pages              []privateWriterProducedTerminalPage
	prior              []privateWriterDraftPageProvenance
	priorBindings      []privateWriterAggregatePriorBinding
	replay             []privateWriterAggregateReplaySlot
	bindings           []bitmapCOWArenaBinding
	indexNodes         []bitmapCOWIndexNode
	directEntries      []int
	directGenerations  []uint64
	touchedMapSlots    []int
	mapGeneration      uint64
	slotGeneration     uint64
}

type privateWriterFixedPointAggregateWorkspaceErrorCode uint8

const (
	privateWriterFixedPointAggregateWorkspaceErrInvalidBudget privateWriterFixedPointAggregateWorkspaceErrorCode = iota + 1
	privateWriterFixedPointAggregateWorkspaceErrInvalidState
	privateWriterFixedPointAggregateWorkspaceErrExhausted
)

type privateWriterFixedPointAggregateWorkspaceError struct {
	code     privateWriterFixedPointAggregateWorkspaceErrorCode
	required uint64
	actual   uint64
}

func (e privateWriterFixedPointAggregateWorkspaceError) failed() bool {
	return e.code != 0
}

func privateWriterAggregateWorkspaceBytes(
	budget privateWriterFixedPointAggregateWorkspaceBudget,
) (uint64, bool) {
	if budget.maxBytes == 0 || budget.privatePages <= 0 ||
		budget.terminalPages <= 0 || budget.priorReturns <= 0 ||
		budget.touchedSlots <= 0 || budget.touchedSlots > budget.privatePages {
		return 0, false
	}
	total := uint64(reflect.TypeOf(privateWriterFixedPointAggregateWorkspace{}).Size())
	components := [...]struct {
		count int
		size  uintptr
	}{
		{budget.terminalPages, reflect.TypeOf(privateWriterProducedTerminalPage{}).Size()},
		{budget.priorReturns, reflect.TypeOf(privateWriterDraftPageProvenance{}).Size()},
		{budget.terminalPages, reflect.TypeOf(privateWriterProducedTerminalPage{}).Size()},
		{budget.priorReturns, reflect.TypeOf(privateWriterDraftPageProvenance{}).Size()},
		{budget.terminalPages, reflect.TypeOf(privateWriterProducedTerminalPage{}).Size()},
		{budget.priorReturns, reflect.TypeOf(privateWriterDraftPageProvenance{}).Size()},
		{budget.priorReturns, reflect.TypeOf(privateWriterAggregatePriorBinding{}).Size()},
		{budget.touchedSlots, reflect.TypeOf(privateWriterAggregateReplaySlot{}).Size()},
		{budget.terminalPages, reflect.TypeOf(bitmapCOWArenaBinding{}).Size()},
		{budget.terminalPages, reflect.TypeOf(bitmapCOWIndexNode{}).Size()},
		{budget.privatePages, reflect.TypeOf(int(0)).Size()},
		{budget.privatePages, reflect.TypeOf(uint64(0)).Size()},
		{budget.touchedSlots, reflect.TypeOf(int(0)).Size()},
	}
	for _, component := range components {
		items := uint64(component.count)
		size := uint64(component.size)
		if size != 0 && items > ^uint64(0)/size {
			return 0, false
		}
		bytes := items * size
		if bytes > ^uint64(0)-total {
			return 0, false
		}
		total += bytes
	}
	return total, total <= budget.maxBytes
}

func newPrivateWriterFixedPointAggregateWorkspace(
	budget privateWriterFixedPointAggregateWorkspaceBudget,
	writerBudget privateWriterResourceBudget,
) (*privateWriterFixedPointAggregateWorkspace, privateWriterFixedPointAggregateWorkspaceError) {
	bytes, ok := privateWriterAggregateWorkspaceBytes(budget)
	if !ok || bytes > writerBudget.maxHeapBytes ||
		uint64(budget.privatePages) > writerBudget.maxPrivatePages {
		return nil, privateWriterFixedPointAggregateWorkspaceError{
			code:     privateWriterFixedPointAggregateWorkspaceErrInvalidBudget,
			required: bytes, actual: writerBudget.maxHeapBytes,
		}
	}
	workspace := &privateWriterFixedPointAggregateWorkspace{
		partitionBytes: bytes, writerHeapBudget: writerBudget.maxHeapBytes,
		privatePages:      budget.privatePages,
		bitmapPages:       make([]privateWriterProducedTerminalPage, budget.terminalPages),
		bitmapPrior:       make([]privateWriterDraftPageProvenance, budget.priorReturns),
		retirementPages:   make([]privateWriterProducedTerminalPage, budget.terminalPages),
		retirementPrior:   make([]privateWriterDraftPageProvenance, budget.priorReturns),
		pages:             make([]privateWriterProducedTerminalPage, budget.terminalPages),
		prior:             make([]privateWriterDraftPageProvenance, budget.priorReturns),
		priorBindings:     make([]privateWriterAggregatePriorBinding, budget.priorReturns),
		replay:            make([]privateWriterAggregateReplaySlot, budget.touchedSlots),
		bindings:          make([]bitmapCOWArenaBinding, budget.terminalPages),
		indexNodes:        make([]bitmapCOWIndexNode, budget.terminalPages),
		directEntries:     make([]int, budget.privatePages),
		directGenerations: make([]uint64, budget.privatePages),
		touchedMapSlots:   make([]int, budget.touchedSlots),
	}
	workspace.self = workspace
	return workspace, privateWriterFixedPointAggregateWorkspaceError{}
}

func (w *privateWriterFixedPointAggregateWorkspace) clearPrepared() {
	if w == nil || w.self != w {
		return
	}
	clear(w.pages)
	clear(w.prior)
	clear(w.priorBindings)
	clear(w.replay)
	clear(w.bindings)
	clear(w.indexNodes)
	clear(w.touchedMapSlots)
	w.slot = privateWriterFixedPointPreparedAggregate{}
}

func (w *privateWriterFixedPointAggregateWorkspace) beginMap() bool {
	if w == nil || w.self != w || w.mapGeneration == ^uint64(0) {
		return false
	}
	w.mapGeneration++
	if w.mapGeneration == 0 {
		return false
	}
	return true
}

func (w *privateWriterFixedPointAggregateWorkspace) replayIndex(
	slotIndex int,
) (int, bool) {
	if slotIndex < 0 || slotIndex >= len(w.directEntries) ||
		w.directGenerations[slotIndex] != w.mapGeneration {
		return 0, false
	}
	index := w.directEntries[slotIndex] - 1
	return index, index >= 0 && index < len(w.replay)
}

func (w *privateWriterFixedPointAggregateWorkspace) rememberBefore(
	pool *privatePagePool,
	slotIndex int,
	replayLen *int,
) bool {
	if _, found := w.replayIndex(slotIndex); found {
		return true
	}
	if pool == nil || replayLen == nil || slotIndex < 0 ||
		slotIndex >= len(pool.slots) || *replayLen >= len(w.replay) ||
		*replayLen >= len(w.touchedMapSlots) {
		return false
	}
	index := *replayLen
	w.replay[index] = privateWriterAggregateReplaySlot{
		slotIndex: slotIndex, before: pool.slots[slotIndex],
	}
	w.directEntries[slotIndex] = index + 1
	w.directGenerations[slotIndex] = w.mapGeneration
	w.touchedMapSlots[index] = slotIndex
	*replayLen++
	return true
}

func privateWriterAggregateApplyScope(
	pool *privatePagePool,
	plan privateWriterPreparedScopePlan,
) privatePageReservationScope {
	for assigned, encoded := range plan.nodes {
		member := int(encoded - 1)
		slot := &pool.slots[member]
		next := privatePagePoolNoIndex
		if assigned+1 < plan.visits {
			next = int(plan.nodes[assigned+1] - 1)
		}
		slot.scopeID = plan.scopeID
		slot.scopeAnchorIndex = plan.anchor
		slot.scopeVacantNext = next
		slot.scopeMemberNext = next
		slot.unscopedNext = privatePagePoolNoIndex
		slot.unscopedPrevious = privatePagePoolNoIndex
		slot.epoch++
		pool.advanceMutationPrepared()
	}
	pool.unscopedVacantHead = plan.remainingHead
	pool.unscopedVacantCount = plan.remainingCount
	if plan.remainingHead == privatePagePoolNoIndex {
		pool.unscopedVacantTail = privatePagePoolNoIndex
	} else {
		pool.slots[plan.remainingHead].unscopedPrevious = privatePagePoolNoIndex
	}
	anchor := &pool.slots[plan.anchor]
	anchor.scopeAnchor = true
	anchor.scopeRoot = privatePagePoolNoIndex
	anchor.scopeVacantHead = plan.anchor
	anchor.scopeMemberHead = plan.anchor
	anchor.scopeCapacity = plan.visits
	anchor.scopeBound = 0
	anchor.scopeGeneration = 1
	anchor.scopeSealed = false
	anchor.scopeSuccessor = 0
	anchor.successorConsumed = false
	pool.scopeSequence = plan.scopeID
	pool.activeScopes++
	return privatePageReservationScope{
		pool: pool, poolEpoch: pool.epoch, id: plan.scopeID,
		anchor: plan.anchor, generation: 1, pendingTxn: pool.pendingTxn,
	}
}

func privateWriterAggregatePoolAfterFrom(
	pool *privatePagePool,
) privateWriterAggregatePoolAfter {
	return privateWriterAggregatePoolAfter{
		pendingPageCount: pool.pendingPageCount,
		generation:       pool.generation, mutationEpoch: pool.mutationEpoch,
		checkpointSequence: pool.checkpointSequence,
		indexRoot:          pool.indexRoot, scopeSequence: pool.scopeSequence,
		activeScopes:        pool.activeScopes,
		unscopedVacantHead:  pool.unscopedVacantHead,
		unscopedVacantTail:  pool.unscopedVacantTail,
		unscopedVacantCount: pool.unscopedVacantCount,
	}
}

func privateWriterAggregateRestorePool(
	pool *privatePagePool,
	before privatePagePool,
	replay []privateWriterAggregateReplaySlot,
) {
	for index := range replay {
		pool.slots[replay[index].slotIndex] = replay[index].before
	}
	*pool = before
}

func privateWriterAggregateClonePriorScope(
	pool *privatePagePool,
	provenance privateWriterDraftPageProvenance,
) privatePageReservationScope {
	return privatePageReservationScope{
		pool: pool, poolEpoch: pool.epoch, id: provenance.scopeID,
		anchor: provenance.scopeAnchor, pendingTxn: pool.pendingTxn,
		generation: pool.slots[provenance.scopeAnchor].scopeGeneration,
	}
}

type privateWriterAggregateOverlay struct {
	pool      *privatePagePool
	workspace *privateWriterFixedPointAggregateWorkspace
	replayLen int

	pendingPageCount    uint64
	generation          uint64
	mutationEpoch       uint64
	checkpointSequence  uint64
	indexRoot           int
	scopeSequence       uint64
	activeScopes        int
	unscopedVacantHead  int
	unscopedVacantTail  int
	unscopedVacantCount int
	scopeRoot           int
	scopeAnchor         int
	failed              bool
}

func newPrivateWriterAggregateOverlay(
	pool *privatePagePool,
	workspace *privateWriterFixedPointAggregateWorkspace,
) privateWriterAggregateOverlay {
	return privateWriterAggregateOverlay{
		pool: pool, workspace: workspace,
		pendingPageCount: pool.pendingPageCount,
		generation:       pool.generation, mutationEpoch: pool.mutationEpoch,
		checkpointSequence: pool.checkpointSequence,
		indexRoot:          pool.indexRoot, scopeSequence: pool.scopeSequence,
		activeScopes:        pool.activeScopes,
		unscopedVacantHead:  pool.unscopedVacantHead,
		unscopedVacantTail:  pool.unscopedVacantTail,
		unscopedVacantCount: pool.unscopedVacantCount,
		scopeAnchor:         privatePagePoolNoIndex,
	}
}

func (o *privateWriterAggregateOverlay) read(index int) privatePagePoolSlot {
	if index < 0 || index >= len(o.pool.slots) {
		o.failed = true
		return privatePagePoolSlot{}
	}
	if replayIndex, found := o.workspace.replayIndex(index); found &&
		replayIndex < o.replayLen {
		return o.workspace.replay[replayIndex].after
	}
	return o.pool.slots[index]
}

func (o *privateWriterAggregateOverlay) touch(index int) *privatePagePoolSlot {
	if index < 0 || index >= len(o.pool.slots) {
		o.failed = true
		return nil
	}
	if replayIndex, found := o.workspace.replayIndex(index); found &&
		replayIndex < o.replayLen {
		return &o.workspace.replay[replayIndex].after
	}
	if o.replayLen >= len(o.workspace.replay) ||
		o.replayLen >= len(o.workspace.touchedMapSlots) {
		o.failed = true
		return nil
	}
	replayIndex := o.replayLen
	before := o.pool.slots[index]
	o.workspace.replay[replayIndex] = privateWriterAggregateReplaySlot{
		slotIndex: index, before: before, after: before,
	}
	o.workspace.directEntries[index] = replayIndex + 1
	o.workspace.directGenerations[index] = o.workspace.mapGeneration
	o.workspace.touchedMapSlots[replayIndex] = index
	o.replayLen++
	return &o.workspace.replay[replayIndex].after
}

func (o *privateWriterAggregateOverlay) indexHeight(index int) int8 {
	if index == privatePagePoolNoIndex {
		return 0
	}
	return o.read(index).indexHeight
}

func (o *privateWriterAggregateOverlay) scopeHeight(index int) int8 {
	if index == privatePagePoolNoIndex {
		return 0
	}
	return o.read(index).scopeHeight
}

func (o *privateWriterAggregateOverlay) refreshIndex(index int) {
	slot := o.touch(index)
	if slot == nil {
		return
	}
	leftHeight, rightHeight := o.indexHeight(slot.indexLeft), o.indexHeight(slot.indexRight)
	if rightHeight > leftHeight {
		leftHeight = rightHeight
	}
	slot.indexHeight = leftHeight + 1
	slot.indexFree, slot.indexInUse = 0, 0
	if slot.state == privatePageAvailable {
		slot.indexFree = 1
	} else if slot.state == privatePageInUse {
		slot.indexInUse = 1
	}
	if slot.indexLeft != privatePagePoolNoIndex {
		child := o.read(slot.indexLeft)
		slot.indexFree += child.indexFree
		slot.indexInUse += child.indexInUse
	}
	if slot.indexRight != privatePagePoolNoIndex {
		child := o.read(slot.indexRight)
		slot.indexFree += child.indexFree
		slot.indexInUse += child.indexInUse
	}
}

func (o *privateWriterAggregateOverlay) refreshScope(index int) {
	slot := o.touch(index)
	if slot == nil {
		return
	}
	leftHeight, rightHeight := o.scopeHeight(slot.scopeLeft), o.scopeHeight(slot.scopeRight)
	if rightHeight > leftHeight {
		leftHeight = rightHeight
	}
	slot.scopeHeight = leftHeight + 1
	slot.scopeFree, slot.scopeInUse = 0, 0
	if slot.state == privatePageAvailable {
		slot.scopeFree = 1
	} else if slot.state == privatePageInUse {
		slot.scopeInUse = 1
	}
	if slot.scopeLeft != privatePagePoolNoIndex {
		child := o.read(slot.scopeLeft)
		slot.scopeFree += child.scopeFree
		slot.scopeInUse += child.scopeInUse
	}
	if slot.scopeRight != privatePagePoolNoIndex {
		child := o.read(slot.scopeRight)
		slot.scopeFree += child.scopeFree
		slot.scopeInUse += child.scopeInUse
	}
}

func (o *privateWriterAggregateOverlay) rotateIndexRight(root int) int {
	rootSlot := o.touch(root)
	if rootSlot == nil {
		return root
	}
	left := rootSlot.indexLeft
	leftSlot := o.touch(left)
	if leftSlot == nil {
		return root
	}
	rootSlot.indexLeft = leftSlot.indexRight
	leftSlot.indexRight = root
	o.refreshIndex(root)
	o.refreshIndex(left)
	return left
}

func (o *privateWriterAggregateOverlay) rotateIndexLeft(root int) int {
	rootSlot := o.touch(root)
	if rootSlot == nil {
		return root
	}
	right := rootSlot.indexRight
	rightSlot := o.touch(right)
	if rightSlot == nil {
		return root
	}
	rootSlot.indexRight = rightSlot.indexLeft
	rightSlot.indexLeft = root
	o.refreshIndex(root)
	o.refreshIndex(right)
	return right
}

func (o *privateWriterAggregateOverlay) rebalanceIndex(root int) int {
	o.refreshIndex(root)
	slot := o.read(root)
	balance := int(o.indexHeight(slot.indexLeft)) - int(o.indexHeight(slot.indexRight))
	if balance > 1 {
		left := slot.indexLeft
		leftSlot := o.read(left)
		if o.indexHeight(leftSlot.indexRight) > o.indexHeight(leftSlot.indexLeft) {
			o.touch(root).indexLeft = o.rotateIndexLeft(left)
		}
		return o.rotateIndexRight(root)
	}
	if balance < -1 {
		right := slot.indexRight
		rightSlot := o.read(right)
		if o.indexHeight(rightSlot.indexLeft) > o.indexHeight(rightSlot.indexRight) {
			o.touch(root).indexRight = o.rotateIndexRight(right)
		}
		return o.rotateIndexLeft(root)
	}
	return root
}

func (o *privateWriterAggregateOverlay) insertIndex(root, inserted int) int {
	if root == privatePagePoolNoIndex {
		o.touch(inserted)
		return inserted
	}
	insertedPage := o.read(inserted).pageNumber
	rootPage := o.read(root).pageNumber
	if insertedPage == rootPage {
		o.failed = true
		return root
	}
	rootSlot := o.touch(root)
	if insertedPage < rootPage {
		rootSlot.indexLeft = o.insertIndex(rootSlot.indexLeft, inserted)
	} else {
		rootSlot.indexRight = o.insertIndex(rootSlot.indexRight, inserted)
	}
	return o.rebalanceIndex(root)
}

func (o *privateWriterAggregateOverlay) rotateScopeRight(root int) int {
	rootSlot := o.touch(root)
	if rootSlot == nil {
		return root
	}
	left := rootSlot.scopeLeft
	leftSlot := o.touch(left)
	if leftSlot == nil {
		return root
	}
	rootSlot.scopeLeft = leftSlot.scopeRight
	leftSlot.scopeRight = root
	o.refreshScope(root)
	o.refreshScope(left)
	return left
}

func (o *privateWriterAggregateOverlay) rotateScopeLeft(root int) int {
	rootSlot := o.touch(root)
	if rootSlot == nil {
		return root
	}
	right := rootSlot.scopeRight
	rightSlot := o.touch(right)
	if rightSlot == nil {
		return root
	}
	rootSlot.scopeRight = rightSlot.scopeLeft
	rightSlot.scopeLeft = root
	o.refreshScope(root)
	o.refreshScope(right)
	return right
}

func (o *privateWriterAggregateOverlay) rebalanceScope(root int) int {
	o.refreshScope(root)
	slot := o.read(root)
	balance := int(o.scopeHeight(slot.scopeLeft)) - int(o.scopeHeight(slot.scopeRight))
	if balance > 1 {
		left := slot.scopeLeft
		leftSlot := o.read(left)
		if o.scopeHeight(leftSlot.scopeRight) > o.scopeHeight(leftSlot.scopeLeft) {
			o.touch(root).scopeLeft = o.rotateScopeLeft(left)
		}
		return o.rotateScopeRight(root)
	}
	if balance < -1 {
		right := slot.scopeRight
		rightSlot := o.read(right)
		if o.scopeHeight(rightSlot.scopeLeft) > o.scopeHeight(rightSlot.scopeRight) {
			o.touch(root).scopeRight = o.rotateScopeRight(right)
		}
		return o.rotateScopeLeft(root)
	}
	return root
}

func (o *privateWriterAggregateOverlay) insertScope(root, inserted int) int {
	if root == privatePagePoolNoIndex {
		o.touch(inserted)
		return inserted
	}
	insertedPage := o.read(inserted).pageNumber
	rootPage := o.read(root).pageNumber
	if insertedPage == rootPage {
		o.failed = true
		return root
	}
	rootSlot := o.touch(root)
	if insertedPage < rootPage {
		rootSlot.scopeLeft = o.insertScope(rootSlot.scopeLeft, inserted)
	} else {
		rootSlot.scopeRight = o.insertScope(rootSlot.scopeRight, inserted)
	}
	return o.rebalanceScope(root)
}

func (o *privateWriterAggregateOverlay) findPage(pageNumber uint32) (int, bool) {
	for root, visits := o.indexRoot, 0; root != privatePagePoolNoIndex; visits++ {
		if visits > len(o.pool.slots) {
			o.failed = true
			return 0, false
		}
		slot := o.read(root)
		switch {
		case pageNumber < slot.pageNumber:
			root = slot.indexLeft
		case pageNumber > slot.pageNumber:
			root = slot.indexRight
		default:
			return root, true
		}
	}
	return 0, false
}

func (o *privateWriterAggregateOverlay) refreshIndexPath(root int, pageNumber uint32) {
	if root == privatePagePoolNoIndex {
		return
	}
	slot := o.read(root)
	if pageNumber < slot.pageNumber {
		o.refreshIndexPath(slot.indexLeft, pageNumber)
	} else if pageNumber > slot.pageNumber {
		o.refreshIndexPath(slot.indexRight, pageNumber)
	}
	o.refreshIndex(root)
}

func (o *privateWriterAggregateOverlay) refreshScopePath(root int, pageNumber uint32) {
	if root == privatePagePoolNoIndex {
		return
	}
	slot := o.read(root)
	if pageNumber < slot.pageNumber {
		o.refreshScopePath(slot.scopeLeft, pageNumber)
	} else if pageNumber > slot.pageNumber {
		o.refreshScopePath(slot.scopeRight, pageNumber)
	}
	o.refreshScope(root)
}

func (o *privateWriterAggregateOverlay) applyScope(
	plan privateWriterPreparedScopePlan,
) {
	for assigned, encoded := range plan.nodes {
		member := int(encoded - 1)
		slot := o.touch(member)
		if slot == nil {
			return
		}
		next := privatePagePoolNoIndex
		if assigned+1 < plan.visits {
			next = int(plan.nodes[assigned+1] - 1)
		}
		slot.scopeID = plan.scopeID
		slot.scopeAnchorIndex = plan.anchor
		slot.scopeVacantNext = next
		slot.scopeMemberNext = next
		slot.unscopedNext = privatePagePoolNoIndex
		slot.unscopedPrevious = privatePagePoolNoIndex
		slot.epoch++
		o.mutationEpoch++
	}
	o.unscopedVacantHead = plan.remainingHead
	o.unscopedVacantCount = plan.remainingCount
	if plan.remainingHead == privatePagePoolNoIndex {
		o.unscopedVacantTail = privatePagePoolNoIndex
	} else if head := o.touch(plan.remainingHead); head != nil {
		head.unscopedPrevious = privatePagePoolNoIndex
	}
	anchor := o.touch(plan.anchor)
	if anchor == nil {
		return
	}
	anchor.scopeAnchor = true
	anchor.scopeRoot = privatePagePoolNoIndex
	anchor.scopeVacantHead = plan.anchor
	anchor.scopeMemberHead = plan.anchor
	anchor.scopeCapacity = plan.visits
	anchor.scopeBound = 0
	anchor.scopeGeneration = 1
	anchor.scopeSealed = false
	anchor.scopeSuccessor = 0
	anchor.successorConsumed = false
	o.scopeSequence = plan.scopeID
	o.activeScopes++
	o.scopeRoot = privatePagePoolNoIndex
	o.scopeAnchor = plan.anchor
}

func (o *privateWriterAggregateOverlay) bind(
	page privateWriterProducedTerminalPage,
) (int, bool) {
	if _, found := o.findPage(page.pageNumber); found {
		return 0, false
	}
	anchor := o.touch(o.scopeAnchor)
	if anchor == nil || anchor.scopeVacantHead == privatePagePoolNoIndex {
		return 0, false
	}
	index := anchor.scopeVacantHead
	slot := o.touch(index)
	if slot == nil {
		return 0, false
	}
	anchor.scopeVacantHead = slot.scopeVacantNext
	anchor.scopeBound++
	slot.bound = true
	slot.pageNumber = page.pageNumber
	slot.authorization = page.authorization
	slot.scopeVacantNext = privatePagePoolNoIndex
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = page.owner, page.origin
	slot.pendingTxn = o.pool.pendingTxn
	slot.generation = o.generation + 1
	slot.committedOrigin = page.committedOrigin
	slot.bytes = page.bytes
	slot.indexLeft, slot.indexRight, slot.indexHeight =
		privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.indexFree, slot.indexInUse = 0, 1
	slot.scopeLeft, slot.scopeRight, slot.scopeHeight =
		privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.scopeFree, slot.scopeInUse = 0, 1
	slot.epoch += 2
	o.indexRoot = o.insertIndex(o.indexRoot, index)
	o.scopeRoot = o.insertScope(o.scopeRoot, index)
	o.touch(o.scopeAnchor).scopeRoot = o.scopeRoot
	if page.authorization == privatePageAppended {
		if uint64(page.pageNumber) != o.pendingPageCount {
			o.failed = true
			return 0, false
		}
		o.pendingPageCount++
	}
	o.mutationEpoch += 4
	return index, !o.failed
}

func (o *privateWriterAggregateOverlay) releasePrior(
	provenance privateWriterDraftPageProvenance,
) bool {
	slot := o.touch(provenance.slot)
	if slot == nil || slot.pageNumber != provenance.pageNumber ||
		slot.epoch != provenance.bindingEpoch ||
		slot.state != privatePageInUse || !slot.inUse {
		return false
	}
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageAvailable, false
	slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
	slot.pendingTxn, slot.generation, slot.committedOrigin = 0, 0, 0
	slot.pendingReturnState = 0
	slot.epoch += 2
	o.refreshIndexPath(o.indexRoot, provenance.pageNumber)
	anchor := o.read(provenance.scopeAnchor)
	o.refreshScopePath(anchor.scopeRoot, provenance.pageNumber)
	o.mutationEpoch += 2
	return !o.failed
}

func (o *privateWriterAggregateOverlay) sealScope(nonce uint64) bool {
	anchor := o.touch(o.scopeAnchor)
	if anchor == nil || anchor.scopeGeneration == ^uint64(0) {
		return false
	}
	anchor.scopeGeneration++
	anchor.scopeSealed = true
	anchor.scopeSuccessor = nonce
	anchor.successorConsumed = true
	o.mutationEpoch++
	o.generation++
	o.checkpointSequence++
	return !o.failed
}

func (o *privateWriterAggregateOverlay) poolAfter() privateWriterAggregatePoolAfter {
	return privateWriterAggregatePoolAfter{
		pendingPageCount: o.pendingPageCount,
		generation:       o.generation, mutationEpoch: o.mutationEpoch,
		checkpointSequence: o.checkpointSequence,
		indexRoot:          o.indexRoot, scopeSequence: o.scopeSequence,
		activeScopes:        o.activeScopes,
		unscopedVacantHead:  o.unscopedVacantHead,
		unscopedVacantTail:  o.unscopedVacantTail,
		unscopedVacantCount: o.unscopedVacantCount,
	}
}

func (c *privateWriterTransactionCore) prepareFixedPointAggregate(
	handle privateWriterTransactionHandle,
	base privateWriterFixedPointPreparedToken,
	bitmap privateWriterProducedBitmapTerminal,
	retirement privateWriterProducedRetirementTerminal,
	workspace *privateWriterFixedPointAggregateWorkspace,
) (
	privateWriterFixedPointAggregateToken,
	privateWriterTransactionError,
) {
	fail := func(code privateWriterFixedPointErrorCode) (
		privateWriterFixedPointAggregateToken,
		privateWriterTransactionError,
	) {
		if workspace != nil && workspace.slot.self == nil {
			workspace.clearPrepared()
		}
		return privateWriterFixedPointAggregateToken{}, privateWriterTransactionError{
			code:       privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{code: code},
		}
	}
	if problem := c.validateHandle(handle); problem.failed() {
		return privateWriterFixedPointAggregateToken{}, problem
	}
	if workspace == nil || workspace.self != workspace ||
		workspace.privatePages != len(c.pool.slots) ||
		workspace.slot.self != nil || c.fixedPointWorkActive ||
		c.pool.registeredWorkID != 0 {
		return fail(privateWriterFixedPointErrStaleProvenance)
	}
	bitmapProduced, bitmapValid := bitmap.authority(&workspace.bitmapProducer)
	retirementProduced, retirementValid :=
		retirement.authority(&workspace.retirementProducer)
	if !bitmapValid || !retirementValid {
		return fail(privateWriterFixedPointErrStaleProvenance)
	}
	prepared, fixedProblem := c.fixedPointCoordinator.validatePreparedWork(base)
	if fixedProblem.failed() {
		return fail(fixedProblem.code)
	}
	if bitmapProduced.selectedTxn != c.selected.TxnID ||
		bitmapProduced.pendingTxn != c.target.TxnID ||
		bitmapProduced.committedPageCount != c.selected.PageCount ||
		bitmapProduced.pageCount < c.selected.PageCount ||
		prepared.root != c.fixedPointPredecessor.root ||
		prepared.pageCount != c.fixedPointPredecessor.pageCount ||
		c.fixedPointCoordinator.recordLen < 0 ||
		c.fixedPointCoordinator.recordLen >= len(c.workspace.records) ||
		c.workspace.records[c.fixedPointCoordinator.recordLen].active {
		return fail(privateWriterFixedPointErrStaleProvenance)
	}
	totalPages, ok := checkedIntAdd(
		bitmapProduced.pageLen, retirementProduced.pageLen,
	)
	if !ok || totalPages != prepared.scopePages ||
		totalPages > len(workspace.pages) ||
		totalPages > len(workspace.bindings) ||
		totalPages > len(workspace.indexNodes) {
		return fail(privateWriterFixedPointErrScratchTooSmall)
	}
	totalPrior, ok := checkedIntAdd(
		bitmapProduced.priorLen, retirementProduced.priorLen,
	)
	if !ok || totalPrior > len(workspace.prior) ||
		totalPrior > len(workspace.priorBindings) {
		return fail(privateWriterFixedPointErrScratchTooSmall)
	}
	workspace.clearPrepared()
	if !workspace.beginMap() || workspace.slotGeneration == ^uint64(0) {
		return fail(privateWriterFixedPointErrExhausted)
	}

	// Both producers emit strict physical-page order. Merge without sorting so
	// preparation is linear and duplicate pages are rejected before mutation.
	left, right := 0, 0
	for output := 0; output < totalPages; output++ {
		var page privateWriterProducedTerminalPage
		switch {
		case left == bitmapProduced.pageLen:
			page = retirementProduced.pages[right]
			right++
		case right == retirementProduced.pageLen:
			page = bitmapProduced.pages[left]
			left++
		case bitmapProduced.pages[left].pageNumber <
			retirementProduced.pages[right].pageNumber:
			page = bitmapProduced.pages[left]
			left++
		case retirementProduced.pages[right].pageNumber <
			bitmapProduced.pages[left].pageNumber:
			page = retirementProduced.pages[right]
			right++
		default:
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
		if output != 0 && page.pageNumber <= workspace.pages[output-1].pageNumber {
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
		if !validPrivatePageOwnerOrigin(page.owner, page.origin) {
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
		workspace.pages[output] = page
	}

	source := &c.fixedPointCoordinator.sourceState
	priorLen := 0
	appendPrior := func(values []privateWriterDraftPageProvenance) bool {
		for _, provenance := range values {
			if provenance.slot < 0 || provenance.slot >= len(c.pool.slots) {
				return false
			}
			if workspace.directGenerations[provenance.slot] == workspace.mapGeneration {
				return false
			}
			record, bindingIndex, problem := source.validatePriorPrivate(provenance)
			if problem.failed() {
				return false
			}
			recordIndex := source.slotRecords[provenance.slot] - 1
			if record != &source.records[recordIndex] {
				return false
			}
			workspace.prior[priorLen] = provenance
			workspace.priorBindings[priorLen] = privateWriterAggregatePriorBinding{
				provenance: provenance, recordIndex: recordIndex,
				bindingIndex: bindingIndex,
			}
			workspace.directEntries[provenance.slot] = priorLen + 1
			workspace.directGenerations[provenance.slot] = workspace.mapGeneration
			priorLen++
		}
		return true
	}
	if !appendPrior(bitmapProduced.prior[:bitmapProduced.priorLen]) ||
		!appendPrior(
			retirementProduced.prior[:retirementProduced.priorLen],
		) ||
		priorLen != totalPrior {
		return fail(privateWriterFixedPointErrStaleProvenance)
	}

	// The direct map is reused for sparse replay construction. Advancing its
	// generation invalidates the prior-return dedup entries in O(1).
	if !workspace.beginMap() {
		return fail(privateWriterFixedPointErrExhausted)
	}
	overlay := newPrivateWriterAggregateOverlay(&c.pool, workspace)
	overlay.applyScope(prepared.scopePlan)
	if overlay.failed {
		return fail(privateWriterFixedPointErrScratchTooSmall)
	}
	for index := 0; index < totalPages; index++ {
		if _, ok := overlay.bind(workspace.pages[index]); !ok {
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
	}
	for index := 0; index < priorLen; index++ {
		if !overlay.releasePrior(workspace.prior[index]) {
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
	}
	nonce, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		return fail(privateWriterFixedPointErrExhausted)
	}
	if !overlay.sealScope(nonce) {
		return fail(privateWriterFixedPointErrExhausted)
	}
	replayLen := overlay.replayLen
	poolAfter := overlay.poolAfter()
	for index := 0; index < totalPages; index++ {
		slotIndex, found := overlay.findPage(workspace.pages[index].pageNumber)
		if !found {
			return fail(privateWriterFixedPointErrStaleProvenance)
		}
		workspace.bindings[index] = bitmapCOWArenaBinding{
			poolSlot: slotIndex, poolEpoch: overlay.read(slotIndex).epoch,
			pageNumber:  workspace.pages[index].pageNumber,
			storageNode: index, activeNode: index, bound: true,
		}
	}
	clear(workspace.indexNodes)
	indexRoot := bitmapCOWNoIndex
	for index := 0; index < totalPages; index++ {
		pageIndexInsertExistingPrechecked(
			workspace.indexNodes, &indexRoot, index,
			workspace.bindings[index].pageNumber,
			indexedBitmapPage{
				kind: indexedBitmapPageArena,
				slot: workspace.bindings[index].poolSlot,
			},
		)
	}
	anchor := overlay.read(prepared.scopePlan.anchor)
	liveScope := privatePageReservationScope{
		pool: &c.pool, poolEpoch: c.pool.epoch,
		id: prepared.scopePlan.scopeID, anchor: prepared.scopePlan.anchor,
		generation: anchor.scopeGeneration, pendingTxn: c.pool.pendingTxn,
	}
	output := sealedFreeBitmapOutput{
		committed: source, selectedTxn: bitmapProduced.selectedTxn,
		pendingTxn:         bitmapProduced.pendingTxn,
		committedPageCount: bitmapProduced.committedPageCount,
		pageCount:          bitmapProduced.pageCount,
		root:               bitmapProduced.root,
		pool:               &c.pool, scope: liveScope,
		bindings: workspace.bindings, boundLen: totalPages,
		indexNodes: workspace.indexNodes, indexRoot: indexRoot,
	}
	workspace.slotGeneration++
	generation := workspace.slotGeneration
	slot := &workspace.slot
	*slot = privateWriterFixedPointPreparedAggregate{
		self: slot, workspace: workspace, core: c, base: base,
		generation: generation, nonce: nonce,
		pagesLen: totalPages, priorLen: priorLen, replayLen: replayLen,
		recordIndex: c.fixedPointCoordinator.recordLen,
		scope:       liveScope, bitmap: *bitmapProduced,
		retirement: retirementProduced.result,
		output:     output, poolAfter: poolAfter,
	}
	slot.seal = privateWriterAggregateHashWord(
		privateWriterAggregateHashSeed,
		uint64(slot.generation)^slot.nonce^uint64(slot.pagesLen)<<32^
			uint64(slot.priorLen)^uint64(slot.replayLen)<<16^
			uint64(slot.recordIndex+1)^uint64(slot.output.root)^slot.output.pageCount,
	)
	workspace.bitmapProducer.ready = false
	workspace.retirementProducer.ready = false
	return privateWriterFixedPointAggregateToken{
		slot: slot, workspace: workspace, generation: generation, nonce: nonce,
	}, privateWriterTransactionError{}
}

func (c *privateWriterTransactionCore) executeFixedPointAggregate(
	handle privateWriterTransactionHandle,
	token privateWriterFixedPointAggregateToken,
) (
	privateWriterFixedPointAggregateResult,
	privateWriterTransactionError,
) {
	if problem := c.validateHandle(handle); problem.failed() {
		return privateWriterFixedPointAggregateResult{}, problem
	}
	slot := token.slot
	if slot == nil || slot.self != slot || token.workspace == nil ||
		token.workspace.self != token.workspace ||
		slot.workspace != token.workspace || slot.core != c ||
		token.generation == 0 || token.generation != slot.generation ||
		token.nonce == 0 || token.nonce != slot.nonce ||
		slot.seal != privateWriterAggregateHashWord(
			privateWriterAggregateHashSeed,
			uint64(slot.generation)^slot.nonce^uint64(slot.pagesLen)<<32^
				uint64(slot.priorLen)^uint64(slot.replayLen)<<16^
				uint64(slot.recordIndex+1)^uint64(slot.output.root)^slot.output.pageCount,
		) ||
		slot.recordIndex != c.fixedPointCoordinator.recordLen ||
		slot.recordIndex < 0 || slot.recordIndex >= len(c.workspace.records) ||
		c.workspace.records[slot.recordIndex].active {
		return privateWriterFixedPointAggregateResult{}, privateWriterTransactionError{
			code: privateWriterTransactionErrFixedPoint,
			fixedPoint: privateWriterFixedPointError{
				code: privateWriterFixedPointErrStaleProvenance,
			},
		}
	}
	active, scope, problem := c.consumeFixedPointWork(handle, slot.base)
	if problem.failed() {
		return privateWriterFixedPointAggregateResult{}, problem
	}
	if scope.id != slot.scope.id || scope.anchor != slot.scope.anchor {
		c.pool.abortRequired = true
		c.state = privateWriterTransactionAbortRequired
		return privateWriterFixedPointAggregateResult{}, privateWriterTransactionError{
			code: privateWriterTransactionErrAbortRequired,
		}
	}

	// Registration and the exact scope install above are the first live
	// mutations. Everything below is direct, bounds-prebound replay.
	for index := 0; index < slot.replayLen; index++ {
		replay := &slot.workspace.replay[index]
		c.pool.slots[replay.slotIndex] = replay.after
	}
	after := slot.poolAfter
	c.pool.pendingPageCount = after.pendingPageCount
	c.pool.generation = after.generation
	c.pool.mutationEpoch = after.mutationEpoch
	c.pool.checkpointSequence = after.checkpointSequence
	c.pool.indexRoot = after.indexRoot
	c.pool.scopeSequence = after.scopeSequence
	c.pool.activeScopes = after.activeScopes
	c.pool.unscopedVacantHead = after.unscopedVacantHead
	c.pool.unscopedVacantTail = after.unscopedVacantTail
	c.pool.unscopedVacantCount = after.unscopedVacantCount
	for index := 0; index < slot.priorLen; index++ {
		binding := slot.workspace.priorBindings[index]
		c.workspace.slotRecords[binding.provenance.slot] = 0
		record := &c.workspace.records[binding.recordIndex]
		record.output.bindings[binding.bindingIndex].poolEpoch =
			c.pool.slots[binding.provenance.slot].epoch
	}
	record := &c.workspace.records[slot.recordIndex]
	*record = privateWriterSealedBitmapWorkUnitRecord{
		workUnit: slot.base.slot.workID,
		output:   slot.output,
		cleanup: freeBitmapFinalizationPredecessor{
			output: slot.output, nonce: slot.nonce,
		},
		active: true,
	}
	for index := 0; index < slot.output.boundLen; index++ {
		c.workspace.slotRecords[slot.output.bindings[index].poolSlot] =
			slot.recordIndex + 1
	}
	c.fixedPointCoordinator.recordLen++
	c.fixedPointCoordinator.lastWorkUnit = record.workUnit
	c.fixedPointCoordinator.root = slot.output.root
	c.fixedPointCoordinator.pageCount = slot.output.pageCount
	c.target.PageCount = slot.output.pageCount
	resultBitmap := slot.bitmap
	resultBitmap.pages = nil
	resultBitmap.prior = nil
	result := privateWriterFixedPointAggregateResult{
		active: active, bitmap: resultBitmap,
		retirement: slot.retirement, recordIndex: slot.recordIndex,
	}
	slot.self = nil
	slot.seal = 0
	return result, privateWriterTransactionError{}
}
