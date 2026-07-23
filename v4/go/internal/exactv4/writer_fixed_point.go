package exactv4

import "sync/atomic"

type privateWriterFixedPointErrorCode uint8

const (
	privateWriterFixedPointErrInvalidArgument privateWriterFixedPointErrorCode = iota + 1
	privateWriterFixedPointErrStalePredecessor
	privateWriterFixedPointErrScratchTooSmall
	privateWriterFixedPointErrRecordExhausted
	privateWriterFixedPointErrDuplicateWorkUnit
	privateWriterFixedPointErrAdvertisedOwnedPage
	privateWriterFixedPointErrStaleProvenance
	privateWriterFixedPointErrSource
	privateWriterFixedPointErrPool
	privateWriterFixedPointErrExhausted
)

type privateWriterFixedPointError struct {
	code   privateWriterFixedPointErrorCode
	page   uint32
	pool   privatePagePoolError
	bitmap freeBitmapCOWError
	source pageSourceStatus
}

func (e privateWriterFixedPointError) failed() bool { return e.code != 0 }

type privateWriterPageResidenceKind uint8

const (
	privateWriterPageSelectedCommitted privateWriterPageResidenceKind = iota + 1
	privateWriterPageCurrentScopePrivate
	privateWriterPagePriorScopePrivate
)

type privateWriterDraftPageProvenance struct {
	workUnit     uint64
	scopeID      uint64
	scopeAnchor  int
	slot         int
	pageNumber   uint32
	bindingEpoch uint64
	owner        privatePageOwner
	origin       privatePageOrigin
	generation   uint64
}

type privateWriterDraftPageResidence struct {
	kind       privateWriterPageResidenceKind
	provenance privateWriterDraftPageProvenance
}

// privateWriterSealedBitmapWorkUnitRecord owns the only coordinator-valid
// copy of a finalized output. The caller-backed slot-to-record map is the live
// binding authority, so accepting a work unit never scans earlier work units.
type privateWriterSealedBitmapWorkUnitRecord struct {
	workUnit uint64
	output   sealedFreeBitmapOutput
	cleanup  freeBitmapFinalizationPredecessor
	active   bool
}

func (r *privateWriterSealedBitmapWorkUnitRecord) initialize(
	workUnit uint64,
	result freeBitmapFinalizationResult,
) privateWriterFixedPointError {
	if r == nil || r.active || workUnit == 0 || result.output.pool == nil ||
		result.output.boundLen < 0 || result.output.boundLen > len(result.output.bindings) {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	for index := 0; index < result.output.boundLen; index++ {
		binding := result.output.bindings[index]
		if binding.poolSlot < 0 || binding.poolSlot >= len(result.output.pool.slots) {
			return privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance}
		}
		slot := &result.output.pool.slots[binding.poolSlot]
		nodeIndex, found := result.output.findIndexedPage(binding.pageNumber)
		if !found || nodeIndex != index || slot.state != privatePageInUse ||
			!slot.inUse || !validPrivatePageOwnerOrigin(slot.owner, slot.origin) {
			return privateWriterFixedPointError{
				code: privateWriterFixedPointErrStaleProvenance, page: binding.pageNumber,
			}
		}
	}
	predecessor, problem := result.successor.consume()
	if problem.failed() {
		return privateWriterFixedPointError{
			code: privateWriterFixedPointErrStalePredecessor, bitmap: problem,
		}
	}
	*r = privateWriterSealedBitmapWorkUnitRecord{
		workUnit: workUnit,
		output:   result.output,
		cleanup:  predecessor,
		active:   true,
	}
	return privateWriterFixedPointError{}
}

// privateWriterDraftPageSource is intentionally separate from
// committedPageSource. It resolves transaction-private pages first and falls
// back to the selected committed generation only when the pool does not own
// the requested page.
type privateWriterDraftPageSource struct {
	selected    committedPageSource
	pool        *privatePagePool
	records     []privateWriterSealedBitmapWorkUnitRecord
	slotRecords []int
}

func (s *privateWriterDraftPageSource) checkAccessStatus() pageSourceStatus {
	if s == nil || s.pool == nil || s.pool.self != s.pool ||
		len(s.slotRecords) < len(s.pool.slots) {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	if s.selected != nil {
		return s.selected.checkAccessStatus()
	}
	return pageSourceStatus{}
}

func (s *privateWriterDraftPageSource) recordForSlot(
	slotIndex int,
) (*privateWriterSealedBitmapWorkUnitRecord, int, privateWriterFixedPointError) {
	if s == nil || s.pool == nil || slotIndex < 0 || slotIndex >= len(s.pool.slots) ||
		slotIndex >= len(s.slotRecords) {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	recordIndex := s.slotRecords[slotIndex] - 1
	if recordIndex < 0 || recordIndex >= len(s.records) {
		return nil, 0, privateWriterFixedPointError{
			code: privateWriterFixedPointErrAdvertisedOwnedPage,
			page: s.pool.slots[slotIndex].pageNumber,
		}
	}
	record := &s.records[recordIndex]
	if !record.active || record.output.pool != s.pool {
		return nil, 0, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance,
			page: s.pool.slots[slotIndex].pageNumber,
		}
	}
	nodeIndex, found := record.output.findIndexedPage(s.pool.slots[slotIndex].pageNumber)
	if !found || nodeIndex < 0 || nodeIndex >= record.output.boundLen ||
		record.output.bindings[nodeIndex].poolSlot != slotIndex {
		return nil, 0, privateWriterFixedPointError{
			code: privateWriterFixedPointErrAdvertisedOwnedPage,
			page: s.pool.slots[slotIndex].pageNumber,
		}
	}
	return record, nodeIndex, privateWriterFixedPointError{}
}

func (s *privateWriterDraftPageSource) residence(
	pageNumber uint32,
) (privateWriterDraftPageResidence, privateWriterFixedPointError) {
	if s == nil || s.pool == nil || s.pool.self != s.pool || len(s.slotRecords) < len(s.pool.slots) {
		return privateWriterDraftPageResidence{}, privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	slotIndex, owned := s.pool.slotIndex(pageNumber)
	if !owned {
		return privateWriterDraftPageResidence{kind: privateWriterPageSelectedCommitted}, privateWriterFixedPointError{}
	}
	record, bindingIndex, problem := s.recordForSlot(slotIndex)
	if problem.failed() {
		return privateWriterDraftPageResidence{}, problem
	}
	slot := &s.pool.slots[slotIndex]
	binding := record.output.bindings[bindingIndex]
	if !slot.bound || slot.pageNumber != pageNumber || slot.scopeID != record.output.scope.id ||
		slot.scopeAnchorIndex != record.output.scope.anchor || binding.poolEpoch != slot.epoch ||
		slot.state != privatePageInUse || !slot.inUse {
		return privateWriterDraftPageResidence{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStaleProvenance, page: pageNumber,
		}
	}
	return privateWriterDraftPageResidence{
		kind: privateWriterPagePriorScopePrivate,
		provenance: privateWriterDraftPageProvenance{
			workUnit: record.workUnit,
			scopeID:  record.output.scope.id, scopeAnchor: record.output.scope.anchor,
			slot: slotIndex, pageNumber: pageNumber, bindingEpoch: slot.epoch,
			owner: slot.owner, origin: slot.origin, generation: slot.generation,
		},
	}, privateWriterFixedPointError{}
}

func (s *privateWriterDraftPageSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	if destination == nil {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	residence, problem := s.residence(pageNumber)
	if problem.failed() {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	if residence.kind == privateWriterPageSelectedCommitted {
		if s.selected == nil {
			return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
		}
		return s.selected.readPageStatus(pageNumber, destination)
	}
	slot := &s.pool.slots[residence.provenance.slot]
	*destination = slot.bytes
	return pageSourceStatus{}
}

func (s *privateWriterDraftPageSource) installRecordSlots(
	recordIndex int,
) privateWriterFixedPointError {
	if s == nil || recordIndex < 0 || recordIndex >= len(s.records) ||
		len(s.slotRecords) < len(s.pool.slots) {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	record := &s.records[recordIndex]
	for bindingIndex := 0; bindingIndex < record.output.boundLen; bindingIndex++ {
		binding := record.output.bindings[bindingIndex]
		if binding.poolSlot < 0 || binding.poolSlot >= len(s.pool.slots) ||
			s.slotRecords[binding.poolSlot] != 0 {
			return privateWriterFixedPointError{
				code: privateWriterFixedPointErrAdvertisedOwnedPage,
				page: binding.pageNumber,
			}
		}
	}
	for bindingIndex := 0; bindingIndex < record.output.boundLen; bindingIndex++ {
		s.slotRecords[record.output.bindings[bindingIndex].poolSlot] = recordIndex + 1
	}
	return privateWriterFixedPointError{}
}

func (s *privateWriterDraftPageSource) returnPriorPrivate(
	provenance privateWriterDraftPageProvenance,
	current ...*freeBitmapCOW,
) privateWriterFixedPointError {
	if s == nil || s.pool == nil || provenance.slot < 0 ||
		provenance.slot >= len(s.pool.slots) || provenance.slot >= len(s.slotRecords) ||
		len(current) > 1 || (len(current) == 1 && (current[0] == nil || current[0].pool != s.pool)) {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance}
	}
	record, nodeIndex, problem := s.validatePriorPrivate(provenance)
	if problem.failed() {
		return problem
	}
	if slot := &s.pool.slots[provenance.slot]; slot.epoch > ^uint64(0)-2 {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrExhausted, page: provenance.pageNumber}
	}
	if poolProblem := s.pool.requireMutationSteps(2); poolProblem.failed() {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrPool, pool: poolProblem, page: provenance.pageNumber}
	}
	checkpoint, poolProblem := s.pool.begin()
	if poolProblem.failed() {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrPool, pool: poolProblem}
	}
	if poolProblem = s.pool.releaseSealedSlotForCheckpointPrepared(
		checkpoint, record.output.scope, provenance.slot, privatePageAvailable,
	); poolProblem.failed() {
		_ = s.pool.rollback(checkpoint)
		return privateWriterFixedPointError{code: privateWriterFixedPointErrPool, pool: poolProblem}
	}
	if poolProblem = s.pool.commit(checkpoint); poolProblem.failed() {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrPool, pool: poolProblem}
	}
	s.finishPriorPrivateReturn(record, nodeIndex, provenance)
	if len(current) == 1 {
		current[0].mutationEpoch = s.pool.mutationEpoch
	}
	return privateWriterFixedPointError{}
}

func (s *privateWriterDraftPageSource) validatePriorPrivate(
	provenance privateWriterDraftPageProvenance,
) (*privateWriterSealedBitmapWorkUnitRecord, int, privateWriterFixedPointError) {
	if s == nil || s.pool == nil || provenance.slot < 0 ||
		provenance.slot >= len(s.pool.slots) || provenance.slot >= len(s.slotRecords) {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance}
	}
	recordIndex := s.slotRecords[provenance.slot] - 1
	if recordIndex < 0 || recordIndex >= len(s.records) {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance, page: provenance.pageNumber}
	}
	record := &s.records[recordIndex]
	if !record.active || record.workUnit != provenance.workUnit ||
		record.output.scope.id != provenance.scopeID ||
		record.output.scope.anchor != provenance.scopeAnchor {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance, page: provenance.pageNumber}
	}
	nodeIndex, found := record.output.findIndexedPage(provenance.pageNumber)
	if !found || nodeIndex < 0 || nodeIndex >= record.output.boundLen ||
		record.output.bindings[nodeIndex].poolSlot != provenance.slot {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance, page: provenance.pageNumber}
	}
	slot := &s.pool.slots[provenance.slot]
	if !slot.bound || slot.pageNumber != provenance.pageNumber ||
		slot.epoch != provenance.bindingEpoch || slot.owner != provenance.owner ||
		slot.origin != provenance.origin ||
		slot.generation != provenance.generation || slot.scopeID != provenance.scopeID ||
		slot.scopeAnchorIndex != provenance.scopeAnchor ||
		slot.state != privatePageInUse || !slot.inUse {
		return nil, 0, privateWriterFixedPointError{code: privateWriterFixedPointErrStaleProvenance, page: provenance.pageNumber}
	}
	return record, nodeIndex, privateWriterFixedPointError{}
}

func (s *privateWriterDraftPageSource) finishPriorPrivateReturn(
	record *privateWriterSealedBitmapWorkUnitRecord,
	nodeIndex int,
	provenance privateWriterDraftPageProvenance,
) {
	s.slotRecords[provenance.slot] = 0
	record.output.bindings[nodeIndex].poolEpoch = s.pool.slots[provenance.slot].epoch
}

func (s *privateWriterDraftPageSource) finishValidatedPriorPrivateReturn(
	provenance privateWriterDraftPageProvenance,
) {
	recordIndex := s.slotRecords[provenance.slot] - 1
	record := &s.records[recordIndex]
	nodeIndex, _ := record.output.findIndexedPage(provenance.pageNumber)
	s.finishPriorPrivateReturn(record, nodeIndex, provenance)
}

type privateWriterFixedPointPredecessor struct {
	coordinator *privateWriterFixedPointCoordinator
	incarnation uint64
	nonce       uint64
	sequence    uint64
	root        uint32
	pageCount   uint64
}

type privateWriterFixedPointCoordinator struct {
	self                      *privateWriterFixedPointCoordinator
	pool                      *privatePagePool
	sourceState               privateWriterDraftPageSource
	selectedTxn               uint64
	pendingTxn                uint64
	selectedPageCount         uint64
	root                      uint32
	pageCount                 uint64
	incarnation               uint64
	predecessorNonce          uint64
	predecessorUsed           bool
	sequence                  uint64
	recordLen                 int
	lastWorkUnit              uint64
	preparedSlots             []privateWriterFixedPointPreparedWork
	preparationScratch        []uint64
	preparationScratchPerSlot int
	predecessorGeneration     uint64
	carried                   privateWriterCarriedSource
	activePrepared            *privateWriterFixedPointPreparedWork
	transactionCore           *privateWriterTransactionCore
	workspace                 *privateWriterWorkspace
	workFence                 *privateWriterWorkFence
}

var privateWriterFixedPointIncarnation atomic.Uint64

func nextPrivateWriterFixedPointIdentity() (uint64, uint64, bool) {
	for {
		current := privateWriterFixedPointIncarnation.Load()
		if current > ^uint64(0)-2 {
			return 0, 0, false
		}
		if privateWriterFixedPointIncarnation.CompareAndSwap(current, current+2) {
			return current + 1, current + 2, true
		}
	}
}

func initializePrivateWriterFixedPointCoordinator(
	coordinator *privateWriterFixedPointCoordinator,
	pool *privatePagePool,
	selected committedPageSource,
	selectedTxn, pendingTxn, selectedPageCount uint64,
	root uint32,
	pageCount uint64,
	records []privateWriterSealedBitmapWorkUnitRecord,
	slotRecords ...[]int,
) (privateWriterFixedPointPredecessor, privateWriterFixedPointError) {
	if len(slotRecords) > 1 {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrInvalidArgument,
		}
	}
	var slotMap []int
	if len(slotRecords) == 1 {
		slotMap = slotRecords[0]
	}
	return initializePrivateWriterFixedPointCoordinatorWithStorage(
		coordinator, pool, selected,
		selectedTxn, pendingTxn, selectedPageCount,
		root, pageCount, records, slotMap, false,
	)
}

func initializePrivateWriterFixedPointCoordinatorWithStorage(
	coordinator *privateWriterFixedPointCoordinator,
	pool *privatePagePool,
	selected committedPageSource,
	selectedTxn, pendingTxn, selectedPageCount uint64,
	root uint32,
	pageCount uint64,
	records []privateWriterSealedBitmapWorkUnitRecord,
	slotMap []int,
	storageReady bool,
) (privateWriterFixedPointPredecessor, privateWriterFixedPointError) {
	if coordinator == nil || coordinator.self == coordinator || pool == nil || pool.self != pool ||
		selectedTxn == 0 || pendingTxn != selectedTxn+1 ||
		selectedPageCount < 2 || pageCount < selectedPageCount || pageCount > MaxPageCount ||
		(root != 0 && (root < 2 || uint64(root) >= pageCount)) || len(records) == 0 {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	status, poolProblem := pool.status()
	if poolProblem.failed() || status.pendingTxn != pendingTxn ||
		status.committedPageCount != selectedPageCount || status.pendingPageCount != pageCount {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrPool, pool: poolProblem}
	}
	if len(slotMap) < len(pool.slots) {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrScratchTooSmall}
	}
	incarnation, nonce, ok := nextPrivateWriterFixedPointIdentity()
	if !ok {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrExhausted}
	}
	if !storageReady {
		clear(records)
		clear(slotMap)
	}
	*coordinator = privateWriterFixedPointCoordinator{
		self: coordinator, pool: pool,
		sourceState: privateWriterDraftPageSource{
			selected: selected, pool: pool, records: records, slotRecords: slotMap,
		},
		selectedTxn: selectedTxn, pendingTxn: pendingTxn, selectedPageCount: selectedPageCount,
		root: root, pageCount: pageCount, incarnation: incarnation, predecessorNonce: nonce,
	}
	return privateWriterFixedPointPredecessor{
		coordinator: coordinator, incarnation: incarnation, nonce: nonce,
		root: root, pageCount: pageCount,
	}, privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) source() *privateWriterDraftPageSource {
	if c == nil || c.self != c {
		return nil
	}
	return &c.sourceState
}

func (c *privateWriterFixedPointCoordinator) validatePredecessor(
	predecessor privateWriterFixedPointPredecessor,
) privateWriterFixedPointError {
	if problem := c.validatePredecessorAuthority(predecessor); problem.failed() {
		return problem
	}
	status, problem := c.pool.status()
	if problem.failed() || status.pendingPageCount != c.pageCount {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrStalePredecessor, pool: problem}
	}
	return privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) validatePredecessorAuthority(
	predecessor privateWriterFixedPointPredecessor,
) privateWriterFixedPointError {
	if c == nil || c.self != c || predecessor.coordinator != c ||
		predecessor.incarnation != c.incarnation || predecessor.nonce == 0 ||
		predecessor.nonce != c.predecessorNonce || c.predecessorUsed ||
		predecessor.sequence != c.sequence || predecessor.root != c.root ||
		predecessor.pageCount != c.pageCount {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrStalePredecessor}
	}
	status, problem := c.pool.status()
	if problem.failed() || status.pendingTxn != c.pendingTxn ||
		status.committedPageCount != c.selectedPageCount {
		return privateWriterFixedPointError{code: privateWriterFixedPointErrStalePredecessor, pool: problem}
	}
	return privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) acceptFinalized(
	predecessor privateWriterFixedPointPredecessor,
	workUnit uint64,
	result freeBitmapFinalizationResult,
) (privateWriterFixedPointPredecessor, privateWriterFixedPointError) {
	if problem := c.validatePredecessorAuthority(predecessor); problem.failed() {
		return privateWriterFixedPointPredecessor{}, problem
	}
	if workUnit == 0 || result.output.pool != c.pool ||
		result.output.selectedTxn != c.selectedTxn ||
		result.output.pendingTxn != c.pendingTxn ||
		result.output.committedPageCount != c.selectedPageCount ||
		result.output.pageCount < c.selectedPageCount {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrInvalidArgument}
	}
	status, poolProblem := c.pool.status()
	if poolProblem.failed() || status.pendingPageCount != result.output.pageCount {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{
			code: privateWriterFixedPointErrStalePredecessor, pool: poolProblem,
		}
	}
	if workUnit <= c.lastWorkUnit {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrDuplicateWorkUnit}
	}
	if c.recordLen == len(c.sourceState.records) {
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrRecordExhausted}
	}
	record := &c.sourceState.records[c.recordLen]
	if problem := record.initialize(workUnit, result); problem.failed() {
		return privateWriterFixedPointPredecessor{}, problem
	}
	if problem := c.sourceState.installRecordSlots(c.recordLen); problem.failed() {
		// The finalizer seed has been consumed, so this is a transaction-wide
		// abort condition. Do not issue another predecessor.
		c.predecessorUsed = true
		return privateWriterFixedPointPredecessor{}, problem
	}
	nextIncarnation, nextNonce, ok := nextPrivateWriterFixedPointIdentity()
	if !ok {
		c.predecessorUsed = true
		return privateWriterFixedPointPredecessor{}, privateWriterFixedPointError{code: privateWriterFixedPointErrExhausted}
	}
	c.predecessorUsed = true
	c.recordLen++
	c.lastWorkUnit = workUnit
	c.root = result.output.root
	c.pageCount = result.output.pageCount
	c.sequence++
	c.incarnation = nextIncarnation
	c.predecessorNonce = nextNonce
	c.predecessorUsed = false
	return privateWriterFixedPointPredecessor{
		coordinator: c, incarnation: nextIncarnation, nonce: nextNonce,
		sequence: c.sequence, root: c.root, pageCount: c.pageCount,
	}, privateWriterFixedPointError{}
}

func (c *privateWriterFixedPointCoordinator) consumeFinal(
	predecessor privateWriterFixedPointPredecessor,
) (uint32, uint64, privateWriterFixedPointError) {
	if problem := c.validatePredecessor(predecessor); problem.failed() {
		return 0, 0, problem
	}
	c.predecessorUsed = true
	return c.root, c.pageCount, privateWriterFixedPointError{}
}
