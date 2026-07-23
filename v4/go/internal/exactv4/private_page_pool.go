package exactv4

import (
	"encoding/binary"
	"slices"
	"sync/atomic"
)

// privatePageAuthorization is the proof carried by a transaction-private
// physical page. It is deliberately about reuse authority, not an engine role.
type privatePageAuthorization uint8

const (
	privatePageAuthorizationNone privatePageAuthorization = iota
	privatePageCommittedFree
	privatePageReclaimed
	privatePageAppended
)

func validPrivatePageOwnerOrigin(owner privatePageOwner, origin privatePageOrigin) bool {
	switch owner {
	case privatePageOwnerBitmap:
		return origin == privatePageBitmap
	case privatePageOwnerRetirement:
		return origin == privatePageRetirementTree || origin == privatePageRetirementBlob
	default:
		return false
	}
}

func validPrivatePageExistingOwner(owner privatePageOwner) bool {
	return owner == privatePageOwnerNone || owner == privatePageOwnerBitmap || owner == privatePageOwnerRetirement
}

type privatePageState uint8

const (
	privatePageAvailable privatePageState = iota
	privatePageInUse
	privatePagePendingReturn
	privatePageReleasedFree
	privatePageReleasedTail
)

type privatePageOwner uint8

const (
	privatePageOwnerNone privatePageOwner = iota
	privatePageOwnerBitmap
	privatePageOwnerRetirement
)

// privatePageOrigin is an engine-local semantic tag. Ownership, rather than
// this tag, controls access to the physical page.
type privatePageOrigin uint8

const (
	privatePageOriginNone privatePageOrigin = iota
	privatePageBitmap
	privatePageRetirementTree
	privatePageRetirementBlob
)

// privatePagePoolSlot is the only 4 KiB physical-page backing store used by
// transaction-private writers. Callers allocate a fixed slice before the
// transaction starts; the embedded AVL index accepts any caller order.
type privatePagePoolSlot struct {
	bound             bool
	pageNumber        uint32
	authorization     privatePageAuthorization
	scopeID           uint64
	scopeAnchor       bool
	scopeAnchorIndex  int
	scopeVacantNext   int
	scopeMemberNext   int
	unscopedNext      int
	unscopedPrevious  int
	scopeRoot         int
	scopeVacantHead   int
	scopeMemberHead   int
	scopeCapacity     int
	scopeBound        int
	scopeGeneration   uint64
	scopeSealed       bool
	scopeSuccessor    uint64
	successorConsumed bool
	state             privatePageState
	owner             privatePageOwner
	origin            privatePageOrigin
	pendingTxn        uint64
	generation        uint64
	epoch             uint64
	committedOrigin   uint32
	bytes             [PageSize]byte

	// inUse is retained as an internal compatibility view while the accepted
	// retirement writer is moved onto pool operations. Pool methods keep it in
	// exact agreement with state.
	inUse bool

	checkpointID               uint64
	checkpointSlotNext         int
	checkpointBound            bool
	checkpointPageNumber       uint32
	checkpointAuthorization    privatePageAuthorization
	checkpointScopeID          uint64
	checkpointScopeAnchor      bool
	checkpointScopeAnchorIndex int
	checkpointScopeVacantNext  int
	checkpointState            privatePageState
	checkpointOwner            privatePageOwner
	checkpointOrigin           privatePageOrigin
	checkpointPendingTxn       uint64
	checkpointGeneration       uint64
	checkpointCommittedOrigin  uint32
	checkpointInUse            bool
	pendingReturnState         privatePageState

	indexCheckpointID     uint64
	indexCheckpointNext   int
	checkpointIndexLeft   int
	checkpointIndexRight  int
	checkpointIndexHeight int8
	checkpointIndexFree   uint64
	checkpointIndexInUse  uint64
	checkpointScopeLeft   int
	checkpointScopeRight  int
	checkpointScopeHeight int8
	checkpointScopeFree   uint64
	checkpointScopeInUse  uint64

	scopeCheckpointID         uint64
	scopeCheckpointNext       int
	checkpointScopeRoot       int
	checkpointScopeVacantHead int
	checkpointScopeBound      int

	indexLeft   int
	indexRight  int
	indexHeight int8
	indexFree   uint64
	indexInUse  uint64
	batchMarked bool

	scopeLeft   int
	scopeRight  int
	scopeHeight int8
	scopeFree   uint64
	scopeInUse  uint64
}

type privatePagePoolErrorCode uint8

const (
	privatePagePoolErrInvalidBounds privatePagePoolErrorCode = iota + 1
	privatePagePoolErrInvalidAuthorization
	privatePagePoolErrPageOutOfBounds
	privatePagePoolErrPagesNotStrict
	privatePagePoolErrInvalidState
	privatePagePoolErrOwnerMismatch
	privatePagePoolErrOriginMismatch
	privatePagePoolErrUnavailable
	privatePagePoolErrBudget
	privatePagePoolErrCrossPool
	privatePagePoolErrStaleToken
	privatePagePoolErrCheckpointActive
	privatePagePoolErrCheckpointInactive
	privatePagePoolErrTransferPending
	privatePagePoolErrArithmeticOverflow
	privatePagePoolErrStaleScope
	privatePagePoolErrScopeMismatch
	privatePagePoolErrScopeNotEmpty
	privatePagePoolErrScopeSealed
	privatePagePoolErrAbortRequired
	privatePagePoolErrCoordinatorRequired
)

type privatePagePoolError struct {
	code          privatePagePoolErrorCode
	page          uint32
	previousPage  uint32
	authorization privatePageAuthorization
	required      int
	actual        int
}

func (e privatePagePoolError) failed() bool { return e.code != 0 }

type privatePagePool struct {
	self                 *privatePagePool
	slots                []privatePagePoolSlot
	committedPageCount   uint64
	pendingPageCount     uint64
	pendingTxn           uint64
	epoch                uint64
	invalidationEpoch    uint64
	generation           uint64
	mutationEpoch        uint64
	abortMutationReserve uint64
	checkpointSequence   uint64
	activeCheckpointID   uint64
	checkpointCleanup    uint64
	checkpointSlotHead   int
	checkpointSlotCount  int
	checkpointIndexHead  int
	checkpointIndexCount int
	checkpointScopeHead  int
	checkpointScopeCount int
	operationSequence    uint64
	activeOperationID    uint64
	operationStartEpoch  uint64
	abortRequired        bool
	scopeSequence        uint64
	activeScopes         int
	unscopedVacantHead   int
	unscopedVacantTail   int
	unscopedVacantCount  int
	indexRoot            int

	coordinatorSessionID         uint64
	coordinatorSessionGeneration uint64
	registeredWorkID             uint64
	registeredWorkGeneration     uint64
	registeredWorkPhase          uint8
	registeredWorkStartEpoch     uint64
	registeredWorkMutation       bool
	registeredWorkFence          *privateWriterWorkFence
	registeredScopeID            uint64
	registeredScopeAnchor        int
	unacceptedScopes             int
	coordinatorCleanupPending    int
}

type privatePageSlotInfo struct {
	bound           bool
	pageNumber      uint32
	authorization   privatePageAuthorization
	scopeID         uint64
	state           privatePageState
	owner           privatePageOwner
	origin          privatePageOrigin
	pendingTxn      uint64
	generation      uint64
	epoch           uint64
	committedOrigin uint32
}

type privatePagePoolStatus struct {
	committedPageCount uint64
	pendingPageCount   uint64
	pendingTxn         uint64
	generation         uint64
	mutationEpoch      uint64
}

const privatePagePoolNoIndex = -1

var privatePagePoolIncarnation atomic.Uint64

func nextPrivatePagePoolIncarnation() (uint64, bool) {
	for {
		current := privatePagePoolIncarnation.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		if privatePagePoolIncarnation.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
}

func reservePrivatePagePoolIncarnations() (uint64, uint64, bool) {
	for {
		current := privatePagePoolIncarnation.Load()
		if current > ^uint64(0)-2 {
			return 0, 0, false
		}
		if privatePagePoolIncarnation.CompareAndSwap(current, current+2) {
			return current + 1, current + 2, true
		}
	}
}

func initPrivatePagePool(
	pool *privatePagePool,
	slots []privatePagePoolSlot,
	validationPages []uint32,
	committedPageCount, pendingPageCount, pendingTxn uint64,
	existingOwner privatePageOwner,
) privatePagePoolError {
	if pool == nil || committedPageCount < 2 || committedPageCount > MaxPageCount ||
		pendingPageCount < committedPageCount || pendingPageCount > MaxPageCount || pendingTxn <= 1 {
		return privatePagePoolError{code: privatePagePoolErrInvalidBounds}
	}
	if !validPrivatePageExistingOwner(existingOwner) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if len(validationPages) < len(slots) {
		return privatePagePoolError{code: privatePagePoolErrBudget, required: len(slots), actual: len(validationPages)}
	}
	incarnation, ok := nextPrivatePagePoolIncarnation()
	if !ok {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}

	// Validate the complete caller buffer before changing any byte in it. The
	// caller scratch keeps duplicate detection O(n log n) while
	// preserving failure atomicity for reverse-ordered caller storage.
	pageNumbers := validationPages[:len(slots)]
	for index := range slots {
		slot := slots[index]
		pageNumbers[index] = slot.pageNumber
		if slot.pageNumber < 2 || uint64(slot.pageNumber) >= pendingPageCount {
			return privatePagePoolError{code: privatePagePoolErrPageOutOfBounds, page: slot.pageNumber}
		}
		switch slot.authorization {
		case privatePageCommittedFree, privatePageReclaimed:
			if uint64(slot.pageNumber) >= committedPageCount {
				return privatePagePoolError{
					code: privatePagePoolErrInvalidAuthorization, page: slot.pageNumber, authorization: slot.authorization,
				}
			}
		case privatePageAppended:
			if uint64(slot.pageNumber) < committedPageCount {
				return privatePagePoolError{
					code: privatePagePoolErrInvalidAuthorization, page: slot.pageNumber, authorization: slot.authorization,
				}
			}
		default:
			return privatePagePoolError{
				code: privatePagePoolErrInvalidAuthorization, page: slot.pageNumber, authorization: slot.authorization,
			}
		}

		// Accepted writer tests construct an in-use page through either historic
		// state view. Normalize once, before the pool becomes observable.
		if slot.inUse || slot.state == privatePageInUse {
			owner := slot.owner
			origin := slot.origin
			if owner == privatePageOwnerNone {
				owner = existingOwner
			}
			if origin == privatePageOriginNone && existingOwner == privatePageOwnerBitmap {
				origin = privatePageBitmap
			}
			if !validPrivatePageOwnerOrigin(owner, origin) ||
				(slot.pendingTxn != 0 && slot.pendingTxn != pendingTxn) {
				return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
			}
		} else {
			if slot.state != privatePageAvailable && slot.state != privatePageReleasedFree && slot.state != privatePageReleasedTail {
				return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
			}
			if slot.owner != privatePageOwnerNone || slot.origin != privatePageOriginNone || slot.pendingTxn != 0 || slot.generation != 0 {
				return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
			}
		}
	}
	slices.Sort(pageNumbers)
	for index := 1; index < len(pageNumbers); index++ {
		if pageNumbers[index] == pageNumbers[index-1] {
			return privatePagePoolError{
				code: privatePagePoolErrPagesNotStrict, previousPage: pageNumbers[index-1], page: pageNumbers[index],
			}
		}
	}

	initialGeneration := uint64(0)
	for index := range slots {
		slot := &slots[index]
		slot.bound = true
		slot.scopeID = 0
		slot.scopeAnchor = false
		slot.scopeAnchorIndex = privatePagePoolNoIndex
		slot.scopeVacantNext = privatePagePoolNoIndex
		slot.scopeMemberNext = privatePagePoolNoIndex
		slot.unscopedNext = privatePagePoolNoIndex
		slot.unscopedPrevious = privatePagePoolNoIndex
		slot.scopeRoot = privatePagePoolNoIndex
		slot.scopeVacantHead = privatePagePoolNoIndex
		slot.scopeMemberHead = privatePagePoolNoIndex
		slot.scopeCapacity = 0
		slot.scopeBound = 0
		if slot.inUse || slot.state == privatePageInUse {
			slot.state, slot.inUse = privatePageInUse, true
			if slot.owner == privatePageOwnerNone {
				slot.owner = existingOwner
			}
			if slot.origin == privatePageOriginNone && existingOwner == privatePageOwnerBitmap {
				slot.origin = privatePageBitmap
			}
			slot.pendingTxn = pendingTxn
			if slot.generation > initialGeneration {
				initialGeneration = slot.generation
			}
		} else {
			slot.inUse = false
		}
		if slot.epoch == 0 {
			slot.epoch = 1
		}
		slot.checkpointID = 0
		slot.checkpointSlotNext = privatePagePoolNoIndex
		slot.indexCheckpointID = 0
		slot.indexCheckpointNext = privatePagePoolNoIndex
		slot.scopeCheckpointID = 0
		slot.scopeCheckpointNext = privatePagePoolNoIndex
		slot.batchMarked = false
		slot.indexLeft, slot.indexRight, slot.indexHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 1
		slot.indexFree = 0
		slot.indexInUse = 0
		slot.scopeLeft, slot.scopeRight, slot.scopeHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 0
		slot.scopeFree, slot.scopeInUse = 0, 0
		if slot.state == privatePageAvailable {
			slot.indexFree = 1
		} else if slot.state == privatePageInUse {
			slot.indexInUse = 1
		}
	}
	prepared := privatePagePool{
		self:  pool,
		slots: slots, committedPageCount: committedPageCount, pendingPageCount: pendingPageCount, pendingTxn: pendingTxn,
		epoch: incarnation, generation: initialGeneration,
		unscopedVacantHead:  privatePagePoolNoIndex,
		unscopedVacantTail:  privatePagePoolNoIndex,
		checkpointIndexHead: privatePagePoolNoIndex,
		checkpointSlotHead:  privatePagePoolNoIndex,
		checkpointScopeHead: privatePagePoolNoIndex,
		indexRoot:           privatePagePoolNoIndex,
	}
	for index := range slots {
		var duplicate int
		prepared.indexRoot, duplicate = prepared.indexInsert(prepared.indexRoot, index)
		if duplicate != privatePagePoolNoIndex {
			return privatePagePoolError{
				code:         privatePagePoolErrPagesNotStrict,
				previousPage: prepared.slots[duplicate].pageNumber,
				page:         slots[index].pageNumber,
			}
		}
	}
	*pool = prepared
	return privatePagePoolError{}
}

// initVacantPrivatePagePool initializes caller-owned backing capacity without
// assigning physical page numbers. Reservation scopes bind that capacity only
// after the locked source-selection step has established exact reuse authority.
func initVacantPrivatePagePool(
	pool *privatePagePool,
	slots []privatePagePoolSlot,
	committedPageCount, pendingPageCount, pendingTxn uint64,
) privatePagePoolError {
	if pool == nil || committedPageCount < 2 || committedPageCount > MaxPageCount ||
		pendingPageCount < committedPageCount || pendingPageCount > MaxPageCount || pendingTxn <= 1 {
		return privatePagePoolError{code: privatePagePoolErrInvalidBounds}
	}
	incarnation, ok := nextPrivatePagePoolIncarnation()
	if !ok {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	initVacantPrivatePagePoolPrepared(
		pool, slots, committedPageCount, pendingPageCount, pendingTxn,
		incarnation, 0, 0,
	)
	return privatePagePoolError{}
}

func initVacantPrivatePagePoolForDraft(
	pool *privatePagePool,
	slots []privatePagePoolSlot,
	committedPageCount, pendingPageCount, pendingTxn uint64,
) privatePagePoolError {
	if pool == nil || committedPageCount < 2 || committedPageCount > MaxPageCount ||
		pendingPageCount < committedPageCount || pendingPageCount > MaxPageCount || pendingTxn <= 1 {
		return privatePagePoolError{code: privatePagePoolErrInvalidBounds}
	}
	incarnation, invalidation, ok := reservePrivatePagePoolIncarnations()
	if !ok {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	initVacantPrivatePagePoolPrepared(
		pool, slots, committedPageCount, pendingPageCount, pendingTxn,
		incarnation, invalidation, uint64(len(slots)),
	)
	return privatePagePoolError{}
}

func initVacantPrivatePagePoolPrepared(
	pool *privatePagePool,
	slots []privatePagePoolSlot,
	committedPageCount, pendingPageCount, pendingTxn uint64,
	incarnation, invalidation, abortMutationReserve uint64,
) {
	for index := range slots {
		next := privatePagePoolNoIndex
		if index+1 < len(slots) {
			next = index + 1
		}
		previous := privatePagePoolNoIndex
		if index > 0 {
			previous = index - 1
		}
		slots[index] = privatePagePoolSlot{
			epoch:               1,
			scopeVacantNext:     privatePagePoolNoIndex,
			scopeMemberNext:     privatePagePoolNoIndex,
			unscopedNext:        next,
			unscopedPrevious:    previous,
			scopeAnchorIndex:    privatePagePoolNoIndex,
			checkpointSlotNext:  privatePagePoolNoIndex,
			indexCheckpointNext: privatePagePoolNoIndex,
			scopeRoot:           privatePagePoolNoIndex,
			scopeVacantHead:     privatePagePoolNoIndex,
			scopeMemberHead:     privatePagePoolNoIndex,
			scopeCheckpointNext: privatePagePoolNoIndex,
			indexLeft:           privatePagePoolNoIndex,
			indexRight:          privatePagePoolNoIndex,
			scopeLeft:           privatePagePoolNoIndex,
			scopeRight:          privatePagePoolNoIndex,
		}
	}
	head, tail := privatePagePoolNoIndex, privatePagePoolNoIndex
	if len(slots) != 0 {
		head, tail = 0, len(slots)-1
	}
	*pool = privatePagePool{
		self:                 pool,
		slots:                slots,
		committedPageCount:   committedPageCount,
		pendingPageCount:     pendingPageCount,
		pendingTxn:           pendingTxn,
		epoch:                incarnation,
		invalidationEpoch:    invalidation,
		abortMutationReserve: abortMutationReserve,
		unscopedVacantHead:   head,
		unscopedVacantTail:   tail,
		unscopedVacantCount:  len(slots),
		checkpointIndexHead:  privatePagePoolNoIndex,
		checkpointSlotHead:   privatePagePoolNoIndex,
		checkpointScopeHead:  privatePagePoolNoIndex,
		indexRoot:            privatePagePoolNoIndex,
	}
}

// rollbackDisposablePrivatePagePool is only for a private shadow that has
// never escaped to the live draft. A failed rollback destroys the whole shadow
// so no caller can accidentally continue with stranded page authority.
func rollbackDisposablePrivatePagePool(
	pool *privatePagePool,
	checkpoint privatePagePoolCheckpoint,
) privatePagePoolError {
	problem := pool.rollback(checkpoint)
	if problem.failed() {
		abortDisposablePrivatePagePool(pool)
	}
	return problem
}

func abortDisposablePrivatePagePool(pool *privatePagePool) int {
	if pool == nil {
		return 0
	}
	slots := pool.slots
	for index := range slots {
		clear(slots[index].bytes[:])
		slots[index] = privatePagePoolSlot{}
	}
	*pool = privatePagePool{}
	return len(slots)
}

func (p *privatePagePool) slotIndex(pageNumber uint32) (int, bool) {
	if p == nil || p.self != p {
		return 0, false
	}
	for index := p.indexRoot; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		switch {
		case pageNumber < slot.pageNumber:
			index = slot.indexLeft
		case pageNumber > slot.pageNumber:
			index = slot.indexRight
		default:
			return index, true
		}
	}
	return 0, false
}

func (p *privatePagePool) indexHeight(index int) int8 {
	if index == privatePagePoolNoIndex {
		return 0
	}
	return p.slots[index].indexHeight
}

func (p *privatePagePool) refreshIndexHeight(index int) {
	left := p.indexHeight(p.slots[index].indexLeft)
	right := p.indexHeight(p.slots[index].indexRight)
	if right > left {
		left = right
	}
	p.slots[index].indexHeight = left + 1
	free := uint64(0)
	inUse := uint64(0)
	if p.slots[index].state == privatePageAvailable {
		free = 1
	} else if p.slots[index].state == privatePageInUse {
		inUse = 1
	}
	if child := p.slots[index].indexLeft; child != privatePagePoolNoIndex {
		free += p.slots[child].indexFree
		inUse += p.slots[child].indexInUse
	}
	if child := p.slots[index].indexRight; child != privatePagePoolNoIndex {
		free += p.slots[child].indexFree
		inUse += p.slots[child].indexInUse
	}
	p.slots[index].indexFree = free
	p.slots[index].indexInUse = inUse
}

func (p *privatePagePool) rotateIndexRight(root int) int {
	left := p.slots[root].indexLeft
	p.slots[root].indexLeft = p.slots[left].indexRight
	p.slots[left].indexRight = root
	p.refreshIndexHeight(root)
	p.refreshIndexHeight(left)
	return left
}

func (p *privatePagePool) rotateIndexLeft(root int) int {
	right := p.slots[root].indexRight
	p.slots[root].indexRight = p.slots[right].indexLeft
	p.slots[right].indexLeft = root
	p.refreshIndexHeight(root)
	p.refreshIndexHeight(right)
	return right
}

func (p *privatePagePool) rebalanceIndex(root int) int {
	p.refreshIndexHeight(root)
	balance := int(p.indexHeight(p.slots[root].indexLeft)) - int(p.indexHeight(p.slots[root].indexRight))
	if balance > 1 {
		left := p.slots[root].indexLeft
		if p.indexHeight(p.slots[left].indexRight) > p.indexHeight(p.slots[left].indexLeft) {
			p.slots[root].indexLeft = p.rotateIndexLeft(left)
		}
		return p.rotateIndexRight(root)
	}
	if balance < -1 {
		right := p.slots[root].indexRight
		if p.indexHeight(p.slots[right].indexLeft) > p.indexHeight(p.slots[right].indexRight) {
			p.slots[root].indexRight = p.rotateIndexRight(right)
		}
		return p.rotateIndexLeft(root)
	}
	return root
}

func (p *privatePagePool) indexInsert(root, inserted int) (int, int) {
	if root == privatePagePoolNoIndex {
		return inserted, privatePagePoolNoIndex
	}
	if p.slots[inserted].pageNumber == p.slots[root].pageNumber {
		return root, root
	}
	var duplicate int
	if p.slots[inserted].pageNumber < p.slots[root].pageNumber {
		p.slots[root].indexLeft, duplicate = p.indexInsert(p.slots[root].indexLeft, inserted)
	} else {
		p.slots[root].indexRight, duplicate = p.indexInsert(p.slots[root].indexRight, inserted)
	}
	if duplicate != privatePagePoolNoIndex {
		return root, duplicate
	}
	return p.rebalanceIndex(root), privatePagePoolNoIndex
}

func (p *privatePagePool) rotateIndexRightPrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	left := p.slots[root].indexLeft
	p.rememberIndex(root, checkpoint)
	p.rememberIndex(left, checkpoint)
	p.slots[root].indexLeft = p.slots[left].indexRight
	p.slots[left].indexRight = root
	p.refreshIndexHeight(root)
	p.refreshIndexHeight(left)
	return left
}

func (p *privatePagePool) rotateIndexLeftPrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	right := p.slots[root].indexRight
	p.rememberIndex(root, checkpoint)
	p.rememberIndex(right, checkpoint)
	p.slots[root].indexRight = p.slots[right].indexLeft
	p.slots[right].indexLeft = root
	p.refreshIndexHeight(root)
	p.refreshIndexHeight(right)
	return right
}

func (p *privatePagePool) rebalanceIndexPrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	p.rememberIndex(root, checkpoint)
	p.refreshIndexHeight(root)
	balance := int(p.indexHeight(p.slots[root].indexLeft)) - int(p.indexHeight(p.slots[root].indexRight))
	if balance > 1 {
		left := p.slots[root].indexLeft
		if p.indexHeight(p.slots[left].indexRight) > p.indexHeight(p.slots[left].indexLeft) {
			p.slots[root].indexLeft = p.rotateIndexLeftPrepared(left, checkpoint)
		}
		return p.rotateIndexRightPrepared(root, checkpoint)
	}
	if balance < -1 {
		right := p.slots[root].indexRight
		if p.indexHeight(p.slots[right].indexLeft) > p.indexHeight(p.slots[right].indexRight) {
			p.slots[root].indexRight = p.rotateIndexRightPrepared(right, checkpoint)
		}
		return p.rotateIndexLeftPrepared(root, checkpoint)
	}
	return root
}

func (p *privatePagePool) indexInsertPrepared(root, inserted int, checkpoint privatePagePoolCheckpoint) int {
	if root == privatePagePoolNoIndex {
		p.rememberIndex(inserted, checkpoint)
		return inserted
	}
	p.rememberIndex(root, checkpoint)
	if p.slots[inserted].pageNumber < p.slots[root].pageNumber {
		p.slots[root].indexLeft = p.indexInsertPrepared(p.slots[root].indexLeft, inserted, checkpoint)
	} else {
		p.slots[root].indexRight = p.indexInsertPrepared(p.slots[root].indexRight, inserted, checkpoint)
	}
	return p.rebalanceIndexPrepared(root, checkpoint)
}

func (p *privatePagePool) detachIndexMinimumPrepared(root int, checkpoint privatePagePoolCheckpoint) (int, int) {
	p.rememberIndex(root, checkpoint)
	if p.slots[root].indexLeft == privatePagePoolNoIndex {
		return p.slots[root].indexRight, root
	}
	var minimum int
	p.slots[root].indexLeft, minimum = p.detachIndexMinimumPrepared(p.slots[root].indexLeft, checkpoint)
	return p.rebalanceIndexPrepared(root, checkpoint), minimum
}

func (p *privatePagePool) indexDeletePrepared(root int, pageNumber uint32, checkpoint privatePagePoolCheckpoint) (int, int) {
	p.rememberIndex(root, checkpoint)
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		var removed int
		slot.indexLeft, removed = p.indexDeletePrepared(slot.indexLeft, pageNumber, checkpoint)
		return p.rebalanceIndexPrepared(root, checkpoint), removed
	}
	if pageNumber > slot.pageNumber {
		var removed int
		slot.indexRight, removed = p.indexDeletePrepared(slot.indexRight, pageNumber, checkpoint)
		return p.rebalanceIndexPrepared(root, checkpoint), removed
	}
	left, right := slot.indexLeft, slot.indexRight
	if left == privatePagePoolNoIndex {
		return right, root
	}
	if right == privatePagePoolNoIndex {
		return left, root
	}
	var successor int
	right, successor = p.detachIndexMinimumPrepared(right, checkpoint)
	p.rememberIndex(successor, checkpoint)
	p.slots[successor].indexLeft = left
	p.slots[successor].indexRight = right
	return p.rebalanceIndexPrepared(successor, checkpoint), root
}

func (p *privatePagePool) scopeIndexHeight(index int) int8 {
	if index == privatePagePoolNoIndex {
		return 0
	}
	return p.slots[index].scopeHeight
}

func (p *privatePagePool) refreshScopeIndexHeight(index int) {
	slot := &p.slots[index]
	left, right := p.scopeIndexHeight(slot.scopeLeft), p.scopeIndexHeight(slot.scopeRight)
	if right > left {
		left = right
	}
	slot.scopeHeight = left + 1
	free, inUse := uint64(0), uint64(0)
	if slot.state == privatePageAvailable {
		free = 1
	} else if slot.state == privatePageInUse {
		inUse = 1
	}
	if child := slot.scopeLeft; child != privatePagePoolNoIndex {
		free += p.slots[child].scopeFree
		inUse += p.slots[child].scopeInUse
	}
	if child := slot.scopeRight; child != privatePagePoolNoIndex {
		free += p.slots[child].scopeFree
		inUse += p.slots[child].scopeInUse
	}
	slot.scopeFree, slot.scopeInUse = free, inUse
}

func (p *privatePagePool) rotateScopeRightPrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	left := p.slots[root].scopeLeft
	p.rememberIndex(root, checkpoint)
	p.rememberIndex(left, checkpoint)
	p.slots[root].scopeLeft = p.slots[left].scopeRight
	p.slots[left].scopeRight = root
	p.refreshScopeIndexHeight(root)
	p.refreshScopeIndexHeight(left)
	return left
}

func (p *privatePagePool) rotateScopeLeftPrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	right := p.slots[root].scopeRight
	p.rememberIndex(root, checkpoint)
	p.rememberIndex(right, checkpoint)
	p.slots[root].scopeRight = p.slots[right].scopeLeft
	p.slots[right].scopeLeft = root
	p.refreshScopeIndexHeight(root)
	p.refreshScopeIndexHeight(right)
	return right
}

func (p *privatePagePool) rebalanceScopePrepared(root int, checkpoint privatePagePoolCheckpoint) int {
	p.rememberIndex(root, checkpoint)
	p.refreshScopeIndexHeight(root)
	balance := int(p.scopeIndexHeight(p.slots[root].scopeLeft)) - int(p.scopeIndexHeight(p.slots[root].scopeRight))
	if balance > 1 {
		left := p.slots[root].scopeLeft
		if p.scopeIndexHeight(p.slots[left].scopeRight) > p.scopeIndexHeight(p.slots[left].scopeLeft) {
			p.slots[root].scopeLeft = p.rotateScopeLeftPrepared(left, checkpoint)
		}
		return p.rotateScopeRightPrepared(root, checkpoint)
	}
	if balance < -1 {
		right := p.slots[root].scopeRight
		if p.scopeIndexHeight(p.slots[right].scopeLeft) > p.scopeIndexHeight(p.slots[right].scopeRight) {
			p.slots[root].scopeRight = p.rotateScopeRightPrepared(right, checkpoint)
		}
		return p.rotateScopeLeftPrepared(root, checkpoint)
	}
	return root
}

func (p *privatePagePool) scopeInsertPrepared(root, inserted int, checkpoint privatePagePoolCheckpoint) int {
	if root == privatePagePoolNoIndex {
		p.rememberIndex(inserted, checkpoint)
		return inserted
	}
	p.rememberIndex(root, checkpoint)
	if p.slots[inserted].pageNumber < p.slots[root].pageNumber {
		p.slots[root].scopeLeft = p.scopeInsertPrepared(p.slots[root].scopeLeft, inserted, checkpoint)
	} else {
		p.slots[root].scopeRight = p.scopeInsertPrepared(p.slots[root].scopeRight, inserted, checkpoint)
	}
	return p.rebalanceScopePrepared(root, checkpoint)
}

func (p *privatePagePool) detachScopeMinimumPrepared(root int, checkpoint privatePagePoolCheckpoint) (int, int) {
	p.rememberIndex(root, checkpoint)
	if p.slots[root].scopeLeft == privatePagePoolNoIndex {
		return p.slots[root].scopeRight, root
	}
	var minimum int
	p.slots[root].scopeLeft, minimum = p.detachScopeMinimumPrepared(p.slots[root].scopeLeft, checkpoint)
	return p.rebalanceScopePrepared(root, checkpoint), minimum
}

func (p *privatePagePool) scopeDeletePrepared(root int, pageNumber uint32, checkpoint privatePagePoolCheckpoint) (int, int) {
	p.rememberIndex(root, checkpoint)
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		var removed int
		slot.scopeLeft, removed = p.scopeDeletePrepared(slot.scopeLeft, pageNumber, checkpoint)
		return p.rebalanceScopePrepared(root, checkpoint), removed
	}
	if pageNumber > slot.pageNumber {
		var removed int
		slot.scopeRight, removed = p.scopeDeletePrepared(slot.scopeRight, pageNumber, checkpoint)
		return p.rebalanceScopePrepared(root, checkpoint), removed
	}
	left, right := slot.scopeLeft, slot.scopeRight
	if left == privatePagePoolNoIndex {
		return right, root
	}
	if right == privatePagePoolNoIndex {
		return left, root
	}
	var successor int
	right, successor = p.detachScopeMinimumPrepared(right, checkpoint)
	p.rememberIndex(successor, checkpoint)
	p.slots[successor].scopeLeft = left
	p.slots[successor].scopeRight = right
	return p.rebalanceScopePrepared(successor, checkpoint), root
}

func (p *privatePagePool) refreshIndexPage(root int, pageNumber uint32) {
	if root == privatePagePoolNoIndex {
		return
	}
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		p.refreshIndexPage(slot.indexLeft, pageNumber)
	} else if pageNumber > slot.pageNumber {
		p.refreshIndexPage(slot.indexRight, pageNumber)
	}
	p.refreshIndexHeight(root)
}

func (p *privatePagePool) refreshIndexPagePrepared(
	root int,
	pageNumber uint32,
	checkpoint privatePagePoolCheckpoint,
) {
	if root == privatePagePoolNoIndex {
		return
	}
	p.rememberIndex(root, checkpoint)
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		p.refreshIndexPagePrepared(slot.indexLeft, pageNumber, checkpoint)
	} else if pageNumber > slot.pageNumber {
		p.refreshIndexPagePrepared(slot.indexRight, pageNumber, checkpoint)
	}
	p.refreshIndexHeight(root)
}

func (p *privatePagePool) refreshScopePage(root int, pageNumber uint32) {
	if root == privatePagePoolNoIndex {
		return
	}
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		p.refreshScopePage(slot.scopeLeft, pageNumber)
	} else if pageNumber > slot.pageNumber {
		p.refreshScopePage(slot.scopeRight, pageNumber)
	}
	p.refreshScopeIndexHeight(root)
}

func (p *privatePagePool) refreshScopePagePrepared(
	root int,
	pageNumber uint32,
	checkpoint privatePagePoolCheckpoint,
) {
	if root == privatePagePoolNoIndex {
		return
	}
	p.rememberIndex(root, checkpoint)
	slot := &p.slots[root]
	if pageNumber < slot.pageNumber {
		p.refreshScopePagePrepared(slot.scopeLeft, pageNumber, checkpoint)
	} else if pageNumber > slot.pageNumber {
		p.refreshScopePagePrepared(slot.scopeRight, pageNumber, checkpoint)
	}
	p.refreshScopeIndexHeight(root)
}

func (p *privatePagePool) refreshSlotIndexes(slot *privatePagePoolSlot) {
	if p.activeCheckpointID != 0 {
		checkpoint := privatePagePoolCheckpoint{id: p.activeCheckpointID}
		p.refreshIndexPagePrepared(p.indexRoot, slot.pageNumber, checkpoint)
		if slot.scopeID != 0 && slot.scopeAnchorIndex != privatePagePoolNoIndex {
			p.refreshScopePagePrepared(p.slots[slot.scopeAnchorIndex].scopeRoot, slot.pageNumber, checkpoint)
		}
		return
	}
	p.refreshIndexPage(p.indexRoot, slot.pageNumber)
	if slot.scopeID == 0 {
		return
	}
	anchor := slot.scopeAnchorIndex
	if anchor != privatePagePoolNoIndex {
		p.refreshScopePage(p.slots[anchor].scopeRoot, slot.pageNumber)
	}
}

func (p *privatePagePool) rebuildIndexFree(root int) uint64 {
	if root == privatePagePoolNoIndex {
		return 0
	}
	left := p.rebuildIndexFree(p.slots[root].indexLeft)
	right := p.rebuildIndexFree(p.slots[root].indexRight)
	free := left + right
	inUse := uint64(0)
	if child := p.slots[root].indexLeft; child != privatePagePoolNoIndex {
		inUse += p.slots[child].indexInUse
	}
	if child := p.slots[root].indexRight; child != privatePagePoolNoIndex {
		inUse += p.slots[child].indexInUse
	}
	if p.slots[root].state == privatePageAvailable {
		free++
	} else if p.slots[root].state == privatePageInUse {
		inUse++
	}
	p.slots[root].indexFree = free
	p.slots[root].indexInUse = inUse
	return free
}

func (p *privatePagePool) rebuildScopeCounts(root int) (uint64, uint64) {
	if root == privatePagePoolNoIndex {
		return 0, 0
	}
	leftFree, leftInUse := p.rebuildScopeCounts(p.slots[root].scopeLeft)
	rightFree, rightInUse := p.rebuildScopeCounts(p.slots[root].scopeRight)
	free, inUse := leftFree+rightFree, leftInUse+rightInUse
	if p.slots[root].state == privatePageAvailable {
		free++
	} else if p.slots[root].state == privatePageInUse {
		inUse++
	}
	p.slots[root].scopeFree, p.slots[root].scopeInUse = free, inUse
	return free, inUse
}

func (p *privatePagePool) rebuildAllIndexCounts() {
	p.rebuildIndexFree(p.indexRoot)
	for index := range p.slots {
		if p.slots[index].scopeAnchor {
			p.rebuildScopeCounts(p.slots[index].scopeRoot)
		}
	}
}

func (p *privatePagePool) capacity() int {
	if p == nil || p.self != p {
		return 0
	}
	return len(p.slots)
}

func (p *privatePagePool) status() (privatePagePoolStatus, privatePagePoolError) {
	if p == nil || p.self != p {
		return privatePagePoolStatus{}, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	return privatePagePoolStatus{
		committedPageCount: p.committedPageCount, pendingPageCount: p.pendingPageCount,
		pendingTxn: p.pendingTxn, generation: p.generation, mutationEpoch: p.mutationEpoch,
	}, privatePagePoolError{}
}

func (p *privatePagePool) requireMutationSteps(steps uint64) privatePagePoolError {
	if p == nil || p.self != p {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	if p.abortMutationReserve > ^uint64(0)-p.mutationEpoch ||
		steps > ^uint64(0)-p.mutationEpoch-p.abortMutationReserve {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	return privatePagePoolError{}
}

// requireCheckpointForwardSteps preserves the mutation-epoch headroom already
// owned by the active checkpoint. The reserved steps are consumed only by
// rollback or commit cleanup, never by later forward work.
func (p *privatePagePool) requireCheckpointForwardSteps(steps, prospectiveCleanup uint64) privatePagePoolError {
	if p == nil || p.self != p {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	if p.abortMutationReserve > ^uint64(0)-p.mutationEpoch ||
		prospectiveCleanup > ^uint64(0)-p.checkpointCleanup {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	reserved := p.checkpointCleanup + prospectiveCleanup
	available := ^uint64(0) - p.mutationEpoch - p.abortMutationReserve
	if reserved > available || steps > available-reserved {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) requireForwardMutationSteps(steps uint64) privatePagePoolError {
	if p != nil && p.self == p && p.activeCheckpointID != 0 {
		return p.requireCheckpointForwardSteps(steps, 0)
	}
	return p.requireMutationSteps(steps)
}

func (p *privatePagePool) requireCheckpointSlotMutation(
	checkpoint privatePagePoolCheckpoint,
	slot *privatePagePoolSlot,
	mutationSteps, slotEpochSteps uint64,
) privatePagePoolError {
	prospectiveCleanup := uint64(0)
	if slot.checkpointID != checkpoint.id {
		prospectiveCleanup = 1
	}
	if slotEpochSteps > ^uint64(0)-1 || slot.epoch > ^uint64(0)-slotEpochSteps-1 {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
	}
	return p.requireCheckpointForwardSteps(mutationSteps, prospectiveCleanup)
}

func (p *privatePagePool) advanceMutationPrepared() {
	p.mutationEpoch++
}

func (p *privatePagePool) slotInfo(index int) (privatePageSlotInfo, privatePagePoolError) {
	if p == nil || p.self != p || index < 0 || index >= len(p.slots) {
		return privatePageSlotInfo{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot := p.slots[index]
	return privatePageSlotInfo{
		bound: slot.bound, pageNumber: slot.pageNumber, authorization: slot.authorization, scopeID: slot.scopeID, state: slot.state,
		owner: slot.owner, origin: slot.origin, pendingTxn: slot.pendingTxn,
		generation: slot.generation, epoch: slot.epoch, committedOrigin: slot.committedOrigin,
	}, privatePagePoolError{}
}

func (p *privatePagePool) pageInfo(pageNumber uint32) (privatePageSlotInfo, bool) {
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePageSlotInfo{}, false
	}
	info, problem := p.slotInfo(index)
	return info, !problem.failed()
}

func (p *privatePagePool) contains(pageNumber uint32) bool {
	_, found := p.slotIndex(pageNumber)
	return found
}

func (p *privatePagePool) tokenInfo(token privatePageToken) (privatePageSlotInfo, privatePagePoolError) {
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return privatePageSlotInfo{}, problem
	}
	return privatePageSlotInfo{
		bound: slot.bound, pageNumber: slot.pageNumber, authorization: slot.authorization, scopeID: slot.scopeID, state: slot.state,
		owner: slot.owner, origin: slot.origin, pendingTxn: slot.pendingTxn,
		generation: slot.generation, epoch: slot.epoch, committedOrigin: slot.committedOrigin,
	}, privatePagePoolError{}
}

func (p *privatePagePool) inUseCount() int {
	if p == nil || p.self != p {
		return 0
	}
	if p.indexRoot == privatePagePoolNoIndex {
		return 0
	}
	return int(p.slots[p.indexRoot].indexInUse)
}

func (p *privatePagePool) available() int {
	if p == nil || p.self != p {
		return 0
	}
	if p.indexRoot == privatePagePoolNoIndex {
		return 0
	}
	return int(p.slots[p.indexRoot].indexFree)
}

type privatePageReservationScope struct {
	pool       *privatePagePool
	poolEpoch  uint64
	id         uint64
	generation uint64
	pendingTxn uint64
	anchor     int
}

func privatePageSlotHasCanonicalVacantState(slot *privatePagePoolSlot) bool {
	return slot != nil && !slot.bound && slot.pageNumber == 0 &&
		slot.authorization == privatePageAuthorizationNone && slot.state == privatePageAvailable &&
		slot.owner == privatePageOwnerNone && slot.origin == privatePageOriginNone &&
		slot.pendingTxn == 0 && slot.generation == 0 && slot.committedOrigin == 0 &&
		!slot.inUse && slot.pendingReturnState == 0 && !slot.batchMarked &&
		slot.indexLeft == privatePagePoolNoIndex && slot.indexRight == privatePagePoolNoIndex &&
		slot.indexHeight == 0 && slot.indexFree == 0 && slot.indexInUse == 0 &&
		slot.scopeLeft == privatePagePoolNoIndex && slot.scopeRight == privatePagePoolNoIndex &&
		slot.scopeHeight == 0 && slot.scopeFree == 0 && slot.scopeInUse == 0 &&
		slot.bytes == ([PageSize]byte{})
}

func privatePageSlotIsCanonicalVacant(slot *privatePagePoolSlot) bool {
	return privatePageSlotHasCanonicalVacantState(slot) && slot.checkpointID == 0 &&
		slot.checkpointSlotNext == privatePagePoolNoIndex &&
		slot.indexCheckpointID == 0 && slot.indexCheckpointNext == privatePagePoolNoIndex &&
		slot.scopeCheckpointID == 0 && slot.scopeCheckpointNext == privatePagePoolNoIndex
}

// Checkpoint snapshot values intentionally survive commit and rollback. They
// are inert scratch when all three checkpoint IDs are zero, so reusable slots
// require zero IDs but do not require zero historical snapshots.

func privatePageSlotIsCanonicalUnscopedVacant(slot *privatePagePoolSlot) bool {
	return privatePageSlotIsCanonicalVacant(slot) && slot.scopeID == 0 && !slot.scopeAnchor &&
		slot.scopeAnchorIndex == privatePagePoolNoIndex && slot.scopeVacantNext == privatePagePoolNoIndex &&
		slot.scopeMemberNext == privatePagePoolNoIndex && slot.scopeRoot == privatePagePoolNoIndex &&
		slot.scopeVacantHead == privatePagePoolNoIndex && slot.scopeMemberHead == privatePagePoolNoIndex &&
		slot.scopeCapacity == 0 && slot.scopeBound == 0 && slot.scopeGeneration == 0 && !slot.scopeSealed &&
		slot.scopeSuccessor == 0 && !slot.successorConsumed
}

func (p *privatePagePool) validUnscopedVacancyHeader() bool {
	if p.unscopedVacantCount < 0 || p.unscopedVacantCount > len(p.slots) {
		return false
	}
	if p.unscopedVacantCount == 0 {
		return p.unscopedVacantHead == privatePagePoolNoIndex && p.unscopedVacantTail == privatePagePoolNoIndex
	}
	if p.unscopedVacantHead < 0 || p.unscopedVacantHead >= len(p.slots) ||
		p.unscopedVacantTail < 0 || p.unscopedVacantTail >= len(p.slots) {
		return false
	}
	head := &p.slots[p.unscopedVacantHead]
	tail := &p.slots[p.unscopedVacantTail]
	if !privatePageSlotIsCanonicalUnscopedVacant(head) || head.unscopedPrevious != privatePagePoolNoIndex ||
		!privatePageSlotIsCanonicalUnscopedVacant(tail) || tail.unscopedNext != privatePagePoolNoIndex {
		return false
	}
	if p.unscopedVacantCount == 1 {
		return p.unscopedVacantHead == p.unscopedVacantTail &&
			head.unscopedNext == privatePagePoolNoIndex && tail.unscopedPrevious == privatePagePoolNoIndex
	}
	if p.unscopedVacantHead == p.unscopedVacantTail || head.unscopedNext < 0 || head.unscopedNext >= len(p.slots) ||
		tail.unscopedPrevious < 0 || tail.unscopedPrevious >= len(p.slots) {
		return false
	}
	headNext := &p.slots[head.unscopedNext]
	tailPrevious := &p.slots[tail.unscopedPrevious]
	return privatePageSlotIsCanonicalUnscopedVacant(headNext) && headNext.unscopedPrevious == p.unscopedVacantHead &&
		privatePageSlotIsCanonicalUnscopedVacant(tailPrevious) && tailPrevious.unscopedNext == p.unscopedVacantTail
}

func (p *privatePagePool) validUnscopedVacancyHeadAfterDetach(head, previous, count int) bool {
	if count < 0 {
		return false
	}
	if count == 0 {
		return head == privatePagePoolNoIndex && previous == p.unscopedVacantTail
	}
	if head < 0 || head >= len(p.slots) {
		return false
	}
	headSlot := &p.slots[head]
	if !privatePageSlotIsCanonicalUnscopedVacant(headSlot) || headSlot.unscopedPrevious != previous {
		return false
	}
	if count == 1 {
		return head == p.unscopedVacantTail && headSlot.unscopedNext == privatePagePoolNoIndex
	}
	if head == p.unscopedVacantTail || headSlot.unscopedNext < 0 || headSlot.unscopedNext >= len(p.slots) {
		return false
	}
	next := &p.slots[headSlot.unscopedNext]
	return (count == 2) == (headSlot.unscopedNext == p.unscopedVacantTail) &&
		privatePageSlotIsCanonicalUnscopedVacant(next) && next.unscopedPrevious == head
}

func (p *privatePagePool) validScopedVacancySlot(scope privatePageReservationScope, index int) bool {
	if index < 0 || index >= len(p.slots) {
		return false
	}
	slot := &p.slots[index]
	validCheckpointID := func(id uint64) bool { return id == 0 || id == p.activeCheckpointID }
	validIndexCheckpointLink := slot.indexCheckpointID != 0 &&
		(slot.indexCheckpointNext == privatePagePoolNoIndex ||
			(slot.indexCheckpointNext >= 0 && slot.indexCheckpointNext < len(p.slots) && slot.indexCheckpointNext != index))
	if slot.indexCheckpointID == 0 {
		validIndexCheckpointLink = slot.indexCheckpointNext == privatePagePoolNoIndex
	}
	validSlotCheckpointLink := slot.checkpointID != 0 &&
		(slot.checkpointSlotNext == privatePagePoolNoIndex ||
			(slot.checkpointSlotNext >= 0 && slot.checkpointSlotNext < len(p.slots) && slot.checkpointSlotNext != index))
	if slot.checkpointID == 0 {
		validSlotCheckpointLink = slot.checkpointSlotNext == privatePagePoolNoIndex
	}
	validScopeCheckpointLink := slot.scopeCheckpointID != 0 &&
		(slot.scopeCheckpointNext == privatePagePoolNoIndex ||
			(slot.scopeCheckpointNext >= 0 && slot.scopeCheckpointNext < len(p.slots) && slot.scopeCheckpointNext != index))
	if slot.scopeCheckpointID == 0 {
		validScopeCheckpointLink = slot.scopeCheckpointNext == privatePagePoolNoIndex
	}
	return privatePageSlotHasCanonicalVacantState(slot) && validCheckpointID(slot.checkpointID) &&
		validCheckpointID(slot.indexCheckpointID) && validCheckpointID(slot.scopeCheckpointID) &&
		validSlotCheckpointLink && validIndexCheckpointLink && validScopeCheckpointLink &&
		(index == scope.anchor || slot.scopeCheckpointID == 0) &&
		slot.scopeID == scope.id &&
		slot.scopeAnchor == (index == scope.anchor) && slot.scopeAnchorIndex == scope.anchor &&
		slot.unscopedNext == privatePagePoolNoIndex && slot.unscopedPrevious == privatePagePoolNoIndex
}

func (p *privatePagePool) validScopedVacancyHead(scope privatePageReservationScope, head, count int) bool {
	if count < 0 {
		return false
	}
	if count == 0 {
		return head == privatePagePoolNoIndex
	}
	if !p.validScopedVacancySlot(scope, head) {
		return false
	}
	next := p.slots[head].scopeVacantNext
	if count == 1 {
		return next == privatePagePoolNoIndex
	}
	if next == head || !p.validScopedVacancySlot(scope, next) {
		return false
	}
	return (count == 2) == (p.slots[next].scopeVacantNext == privatePagePoolNoIndex)
}

func (p *privatePagePool) reserveScope(count int) (privatePageReservationScope, privatePagePoolError) {
	scope, _, problem := p.reserveScopeCounted(count)
	return scope, problem
}

func (p *privatePagePool) rejectRawCoordinatorAccess() privatePagePoolError {
	if p != nil && p.self == p && p.coordinatorSessionID != 0 {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	return privatePagePoolError{}
}

// reserveScopeCounted exposes the number of scope-slot visits to tests without
// putting diagnostic state in the pool's correctness contract.
func (p *privatePagePool) reserveScopeCounted(count int) (privatePageReservationScope, int, privatePagePoolError) {
	if p == nil || p.self != p {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	return p.reserveScopeCountedStandalone(count)
}

func (p *privatePagePool) reserveScopeCountedStandalone(
	count int,
) (privatePageReservationScope, int, privatePagePoolError) {
	if p == nil || p.self != p {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.coordinatorSessionID != 0 {
		return privatePageReservationScope{}, 0, privatePagePoolError{
			code: privatePagePoolErrCoordinatorRequired,
		}
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.checkpointCleanup != 0 || p.checkpointSlotHead != privatePagePoolNoIndex || p.checkpointSlotCount != 0 ||
		p.checkpointIndexHead != privatePagePoolNoIndex || p.checkpointIndexCount != 0 ||
		p.checkpointScopeHead != privatePagePoolNoIndex || p.checkpointScopeCount != 0 {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if count <= 0 || count > len(p.slots) {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrBudget, required: count, actual: len(p.slots)}
	}
	if !p.validUnscopedVacancyHeader() {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if count > p.unscopedVacantCount {
		return privatePageReservationScope{}, 0, privatePagePoolError{
			code: privatePagePoolErrBudget, required: count, actual: p.unscopedVacantCount,
		}
	}
	id := p.scopeSequence + 1
	if id == 0 || p.activeScopes == int(^uint(0)>>1) {
		return privatePageReservationScope{}, 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}

	anchor := p.unscopedVacantHead
	member := anchor
	previous := privatePagePoolNoIndex
	visits := 0
	for visits < count {
		if member < 0 || member >= len(p.slots) {
			return privatePageReservationScope{}, visits, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		slot := &p.slots[member]
		if !privatePageSlotIsCanonicalUnscopedVacant(slot) || slot.unscopedPrevious != previous {
			return privatePageReservationScope{}, visits + 1, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		if slot.epoch == ^uint64(0) {
			return privatePageReservationScope{}, visits + 1, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
		}
		previous = member
		member = slot.unscopedNext
		visits++
	}
	remainingHead := member
	remainingCount := p.unscopedVacantCount - count
	if !p.validUnscopedVacancyHeadAfterDetach(remainingHead, previous, remainingCount) {
		return privatePageReservationScope{}, visits, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if problem := p.requireMutationSteps(uint64(count)); problem.failed() {
		return privatePageReservationScope{}, visits, problem
	}

	member = anchor
	for assigned := 0; assigned < count; assigned++ {
		slot := &p.slots[member]
		next := slot.unscopedNext
		scopeNext := next
		if assigned+1 == count {
			scopeNext = privatePagePoolNoIndex
		}
		slot.scopeID = id
		slot.scopeAnchorIndex = anchor
		slot.scopeVacantNext = scopeNext
		slot.scopeMemberNext = scopeNext
		slot.unscopedNext = privatePagePoolNoIndex
		slot.unscopedPrevious = privatePagePoolNoIndex
		slot.epoch++
		p.advanceMutationPrepared()
		member = next
		visits++
	}
	p.unscopedVacantHead = remainingHead
	p.unscopedVacantCount -= count
	if remainingHead == privatePagePoolNoIndex {
		p.unscopedVacantTail = privatePagePoolNoIndex
	} else {
		p.slots[remainingHead].unscopedPrevious = privatePagePoolNoIndex
	}
	anchorSlot := &p.slots[anchor]
	anchorSlot.scopeAnchor = true
	anchorSlot.scopeRoot = privatePagePoolNoIndex
	anchorSlot.scopeVacantHead = anchor
	anchorSlot.scopeMemberHead = anchor
	anchorSlot.scopeCapacity = count
	anchorSlot.scopeBound = 0
	anchorSlot.scopeGeneration = 1
	anchorSlot.scopeSealed = false
	anchorSlot.scopeSuccessor = 0
	anchorSlot.successorConsumed = false
	p.scopeSequence = id
	p.activeScopes++
	return privatePageReservationScope{
		pool: p, poolEpoch: p.epoch, id: id, generation: 1, pendingTxn: p.pendingTxn, anchor: anchor,
	}, visits, privatePagePoolError{}
}

func (p *privatePagePool) validateScopeIdentity(scope privatePageReservationScope) (*privatePagePoolSlot, privatePagePoolError) {
	if p == nil || p.self != p || scope.pool != p || scope.poolEpoch != p.epoch ||
		scope.pendingTxn != p.pendingTxn || scope.anchor < 0 || scope.anchor >= len(p.slots) {
		return nil, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	anchor := &p.slots[scope.anchor]
	if scope.id == 0 || scope.generation == 0 || anchor.scopeID != scope.id ||
		anchor.scopeGeneration != scope.generation || !anchor.scopeAnchor || anchor.scopeAnchorIndex != scope.anchor {
		return nil, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	return anchor, privatePagePoolError{}
}

func (p *privatePagePool) validateScope(scope privatePageReservationScope) (*privatePagePoolSlot, privatePagePoolError) {
	anchor, problem := p.validateScopeIdentity(scope)
	if problem.failed() {
		return nil, problem
	}
	if anchor.scopeSealed {
		return nil, privatePagePoolError{code: privatePagePoolErrScopeSealed}
	}
	return anchor, privatePagePoolError{}
}

func (p *privatePagePool) validateSealedScope(scope privatePageReservationScope) (*privatePagePoolSlot, privatePagePoolError) {
	anchor, problem := p.validateScopeIdentity(scope)
	if problem.failed() {
		return nil, problem
	}
	if !anchor.scopeSealed {
		return nil, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	return anchor, privatePagePoolError{}
}

func (p *privatePagePool) scopeMemberStart(
	scope privatePageReservationScope,
) (int, int, privatePagePoolError) {
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return privatePagePoolNoIndex, 0, problem
	}
	return anchor.scopeMemberHead, anchor.scopeCapacity, privatePagePoolError{}
}

func (p *privatePagePool) scopeMemberNextInScope(
	scope privatePageReservationScope,
	index int,
) (int, privatePagePoolError) {
	if _, problem := p.validateScope(scope); problem.failed() {
		return privatePagePoolNoIndex, problem
	}
	if index < 0 || index >= len(p.slots) {
		return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	return slot.scopeMemberNext, privatePagePoolError{}
}

func (p *privatePagePool) validateScopeMembers(scope privatePageReservationScope) privatePagePoolError {
	member, capacity, problem := p.scopeMemberStart(scope)
	if problem.failed() {
		return problem
	}
	if capacity <= 0 {
		return privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	visited := 0
	anchorSeen := false
	for member != privatePagePoolNoIndex {
		if visited >= capacity || member < 0 || member >= len(p.slots) {
			return privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		slot := &p.slots[member]
		if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor || slot.scopeAnchor != (member == scope.anchor) {
			return privatePagePoolError{code: privatePagePoolErrStaleScope, page: slot.pageNumber}
		}
		anchorSeen = anchorSeen || member == scope.anchor
		visited++
		member = slot.scopeMemberNext
	}
	if visited != capacity || !anchorSeen {
		return privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) scopedAvailable(scope privatePageReservationScope) (int, privatePagePoolError) {
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return 0, problem
	}
	if anchor.scopeRoot == privatePagePoolNoIndex {
		return 0, privatePagePoolError{}
	}
	return int(p.slots[anchor.scopeRoot].scopeFree), privatePagePoolError{}
}

func (p *privatePagePool) scopedInUse(scope privatePageReservationScope) (int, privatePagePoolError) {
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return 0, problem
	}
	if anchor.scopeRoot == privatePagePoolNoIndex {
		return 0, privatePagePoolError{}
	}
	return int(p.slots[anchor.scopeRoot].scopeInUse), privatePagePoolError{}
}

func (p *privatePagePool) closeScope(
	scope privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	_, problem := p.closeScopeCounted(scope, fences...)
	return problem
}

// closeScopeCounted exposes the number of exact-member visits to tests.
func (p *privatePagePool) closeScopeCounted(
	scope privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) (int, privatePagePoolError) {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return 0, problem
	}
	if p.coordinatorSessionID != 0 {
		prepared := fences[0].slot
		journal := &prepared.terminalJournal
		commitment := prepared.terminalCommitment
		if journal.self != journal ||
			journal.phase != privateWriterPreparedTerminalConsumed ||
			commitment.phase != privateWriterPreparedTerminalConsumed ||
			commitment.workID != prepared.workID ||
			commitment.generation != prepared.generation ||
			commitment.nonce != prepared.nonce ||
			journal.scopeID != commitment.scopeID ||
			journal.anchor != commitment.anchor ||
			journal.phase != commitment.phase ||
			journal.operation != commitment.operation ||
			journal.checkpoint != commitment.checkpoint ||
			!p.matchesPreparedTerminalLifecycle(
				commitment, privateWriterPreparedTerminalConsumed,
			) {
			return 0, privatePagePoolError{
				code: privatePagePoolErrCoordinatorRequired,
			}
		}
	}
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return 0, problem
	}
	if p.activeScopes <= 0 {
		return 0, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return 0, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.checkpointCleanup != 0 || p.checkpointSlotHead != privatePagePoolNoIndex || p.checkpointSlotCount != 0 ||
		p.checkpointIndexHead != privatePagePoolNoIndex || p.checkpointIndexCount != 0 ||
		p.checkpointScopeHead != privatePagePoolNoIndex || p.checkpointScopeCount != 0 {
		return 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if anchor.scopeBound != 0 || anchor.scopeRoot != privatePagePoolNoIndex {
		return 0, privatePagePoolError{code: privatePagePoolErrScopeNotEmpty, actual: anchor.scopeBound}
	}
	if anchor.scopeCapacity <= 0 {
		return 0, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	if !p.validUnscopedVacancyHeader() || anchor.scopeCapacity > len(p.slots)-p.unscopedVacantCount {
		return 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	visits := 0
	marked := 0
	member := anchor.scopeMemberHead
	anchorSeen := false
	// batchMarked is validation-only scratch. The member walk rejects stale
	// marks and marks each canonical member; the vacancy walk consumes each mark
	// exactly once. Capacity plus terminal-link checks therefore prove an exact
	// permutation. Every failure clears the validated member prefix before
	// return, and lifecycle mutation starts only after all marks are clear.
	for member != privatePagePoolNoIndex {
		if visits >= anchor.scopeCapacity || member < 0 || member >= len(p.slots) {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits, privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		slot := &p.slots[member]
		if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor || slot.scopeAnchor != (member == scope.anchor) {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + 1, privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		if slot.bound {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + 1, privatePagePoolError{code: privatePagePoolErrScopeNotEmpty, page: slot.pageNumber}
		}
		if !privatePageSlotIsCanonicalVacant(slot) || slot.unscopedNext != privatePagePoolNoIndex ||
			slot.unscopedPrevious != privatePagePoolNoIndex {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + 1, privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		if slot.epoch == ^uint64(0) {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + 1, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
		}
		slot.batchMarked = true
		marked++
		anchorSeen = anchorSeen || member == scope.anchor
		visits++
		member = slot.scopeMemberNext
	}
	if visits != anchor.scopeCapacity || !anchorSeen {
		p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
		return visits, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}

	vacant := anchor.scopeVacantHead
	vacantVisits := 0
	for vacant != privatePagePoolNoIndex {
		if vacantVisits >= anchor.scopeCapacity || vacant < 0 || vacant >= len(p.slots) {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + vacantVisits, privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		slot := &p.slots[vacant]
		if !slot.batchMarked || slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor || slot.bound {
			p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
			return visits + vacantVisits + 1, privatePagePoolError{code: privatePagePoolErrStaleScope}
		}
		slot.batchMarked = false
		vacantVisits++
		vacant = slot.scopeVacantNext
	}
	visits += vacantVisits
	if vacantVisits != anchor.scopeCapacity {
		p.clearScopeValidationMarks(anchor.scopeMemberHead, marked)
		return visits, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	if problem = p.requireMutationSteps(uint64(anchor.scopeCapacity)); problem.failed() {
		return visits, problem
	}
	member = anchor.scopeMemberHead
	for member != privatePagePoolNoIndex {
		slot := &p.slots[member]
		next := slot.scopeMemberNext
		slot.unscopedPrevious = p.unscopedVacantTail
		slot.unscopedNext = privatePagePoolNoIndex
		if p.unscopedVacantTail == privatePagePoolNoIndex {
			p.unscopedVacantHead = member
		} else {
			p.slots[p.unscopedVacantTail].unscopedNext = member
		}
		p.unscopedVacantTail = member
		p.unscopedVacantCount++
		slot.scopeID = 0
		slot.scopeAnchor = false
		slot.scopeAnchorIndex = privatePagePoolNoIndex
		slot.scopeVacantNext = privatePagePoolNoIndex
		slot.scopeMemberNext = privatePagePoolNoIndex
		slot.scopeRoot = privatePagePoolNoIndex
		slot.scopeVacantHead = privatePagePoolNoIndex
		slot.scopeMemberHead = privatePagePoolNoIndex
		slot.scopeCapacity = 0
		slot.scopeBound = 0
		slot.scopeGeneration = 0
		slot.scopeSealed = false
		slot.scopeSuccessor = 0
		slot.successorConsumed = false
		slot.epoch++
		p.advanceMutationPrepared()
		visits++
		member = next
	}
	p.activeScopes--
	return visits, privatePagePoolError{}
}

func (p *privatePagePool) clearScopeValidationMarks(member, count int) {
	for cleared := 0; cleared < count; cleared++ {
		slot := &p.slots[member]
		next := slot.scopeMemberNext
		slot.batchMarked = false
		member = next
	}
}

type privatePagePoolCheckpoint struct {
	pool             *privatePagePool
	poolEpoch        uint64
	id               uint64
	generation       uint64
	indexRoot        int
	pendingPageCount uint64
}

func (p *privatePagePool) begin() (privatePagePoolCheckpoint, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	checkpoint, problem := p.preflightCheckpoint()
	if problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	if problem = p.beginCheckpointPrepared(checkpoint); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	return checkpoint, privatePagePoolError{}
}

func (p *privatePagePool) preflightCheckpoint() (privatePagePoolCheckpoint, privatePagePoolError) {
	if p == nil || p.self != p {
		return privatePagePoolCheckpoint{}, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolCheckpoint{}, problem
	}
	if p.abortRequired {
		return privatePagePoolCheckpoint{}, privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privatePagePoolCheckpoint{}, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.checkpointCleanup != 0 || p.checkpointSlotHead != privatePagePoolNoIndex || p.checkpointSlotCount != 0 ||
		p.checkpointIndexHead != privatePagePoolNoIndex || p.checkpointIndexCount != 0 ||
		p.checkpointScopeHead != privatePagePoolNoIndex || p.checkpointScopeCount != 0 {
		return privatePagePoolCheckpoint{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	id := p.checkpointSequence + 1
	generation := p.generation + 1
	if id == 0 || generation == 0 {
		return privatePagePoolCheckpoint{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	return privatePagePoolCheckpoint{
		pool: p, poolEpoch: p.epoch, id: id, generation: generation,
		indexRoot: p.indexRoot, pendingPageCount: p.pendingPageCount,
	}, privatePagePoolError{}
}

func (p *privatePagePool) beginCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil, privateWriterPreparedTerminalReady, fences...,
	)
	if problem.failed() {
		return problem
	}
	p.checkpointSequence = checkpoint.id
	p.activeCheckpointID = checkpoint.id
	p.checkpointCleanup = 0
	p.checkpointSlotHead = privatePagePoolNoIndex
	p.checkpointSlotCount = 0
	p.checkpointIndexHead = privatePagePoolNoIndex
	p.checkpointIndexCount = 0
	p.checkpointScopeHead = privatePagePoolNoIndex
	p.checkpointScopeCount = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalCheckpointActive
		fences[0].slot.terminalCommitment.setCheckpointActive()
	}
	return privatePagePoolError{}
}

type privatePagePoolOperation struct {
	pool        *privatePagePool
	poolEpoch   uint64
	id          uint64
	pendingTxn  uint64
	generation  uint64
	scopeID     uint64
	scopeAnchor int
	startEpoch  uint64
}

func (p *privatePagePool) beginOperation() (privatePagePoolOperation, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	operation, problem := p.preflightOperation()
	if problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	if problem = p.beginOperationPrepared(operation); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	return operation, privatePagePoolError{}
}

func (p *privatePagePool) beginOperationInScope(
	scope privatePageReservationScope,
) (privatePagePoolOperation, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	operation, problem := p.preflightOperationInScope(scope)
	if problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	if problem = p.beginOperationPrepared(operation); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	return operation, privatePagePoolError{}
}

func (p *privatePagePool) preflightOperation() (privatePagePoolOperation, privatePagePoolError) {
	if p == nil || p.self != p {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	if p.abortRequired {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.activeScopes != 0 {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	id := p.operationSequence + 1
	generation := p.generation + 1
	if id == 0 || generation == 0 {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	return privatePagePoolOperation{
		pool: p, poolEpoch: p.epoch, id: id, pendingTxn: p.pendingTxn, generation: generation,
		scopeAnchor: privatePagePoolNoIndex, startEpoch: p.mutationEpoch,
	}, privatePagePoolError{}
}

func (p *privatePagePool) preflightOperationInScope(
	scope privatePageReservationScope,
) (privatePagePoolOperation, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	if p == nil || p.self != p {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if p.abortRequired {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return privatePagePoolOperation{}, problem
	}
	if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	id := p.operationSequence + 1
	generation := p.generation + 1
	if id == 0 || generation == 0 {
		return privatePagePoolOperation{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	return privatePagePoolOperation{
		pool: p, poolEpoch: p.epoch, id: id, pendingTxn: p.pendingTxn, generation: generation,
		scopeID: scope.id, scopeAnchor: scope.anchor, startEpoch: p.mutationEpoch,
	}, privatePagePoolError{}
}

func (p *privatePagePool) beginOperationPrepared(
	operation privatePagePoolOperation,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalReady, fences...,
	)
	if problem.failed() {
		return problem
	}
	p.operationSequence = operation.id
	p.activeOperationID = operation.id
	p.operationStartEpoch = operation.startEpoch
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalOperationActive
		fences[0].slot.terminalCommitment.setOperationActive()
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) validateOperationIdentity(operation privatePagePoolOperation) privatePagePoolError {
	if p == nil || p.self != p || operation.pool != p || operation.poolEpoch != p.epoch || operation.pendingTxn != p.pendingTxn {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if operation.id == 0 || p.activeOperationID != operation.id || operation.generation != p.generation+1 {
		return privatePagePoolError{code: privatePagePoolErrCheckpointInactive}
	}
	if operation.startEpoch != p.operationStartEpoch {
		return privatePagePoolError{code: privatePagePoolErrCheckpointInactive}
	}
	if operation.scopeID == 0 {
		if operation.scopeAnchor != privatePagePoolNoIndex || p.activeScopes != 0 {
			return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
		}
		return privatePagePoolError{}
	}
	if operation.scopeAnchor < 0 || operation.scopeAnchor >= len(p.slots) {
		return privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	anchor := &p.slots[operation.scopeAnchor]
	if !anchor.scopeAnchor || anchor.scopeID != operation.scopeID || anchor.scopeAnchorIndex != operation.scopeAnchor {
		return privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) validateOperation(operation privatePagePoolOperation) privatePagePoolError {
	if problem := p.validateOperationIdentity(operation); problem.failed() {
		return problem
	}
	if p.abortRequired {
		return privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) abortOperation(operation privatePagePoolOperation) privatePagePoolError {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return problem
	}
	if p != nil && p.self == p && p.activeOperationID != 0 &&
		p.mutationEpoch != p.operationStartEpoch {
		p.abortRequired = true
	}
	if problem := p.validateOperationIdentity(operation); problem.failed() {
		return problem
	}
	if p.abortRequired || p.mutationEpoch != operation.startEpoch {
		p.abortRequired = true
		return privatePagePoolError{code: privatePagePoolErrAbortRequired}
	}
	p.activeOperationID = 0
	p.operationStartEpoch = 0
	return privatePagePoolError{}
}

func (p *privatePagePool) commitOperation(operation privatePagePoolOperation) privatePagePoolError {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return problem
	}
	if problem := p.validateOperation(operation); problem.failed() {
		return problem
	}
	return p.commitOperationPrepared(operation)
}

func (p *privatePagePool) commitOperationPrepared(
	operation privatePagePoolOperation,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	)
	if problem.failed() {
		return problem
	}
	if journal != nil &&
		(p.activeOperationID != operation.id ||
			p.operationStartEpoch != operation.startEpoch) {
		return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
	}
	p.generation = operation.generation
	p.activeOperationID = 0
	p.operationStartEpoch = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalConsumed
		fences[0].slot.terminalCommitment.setOperationConsumed()
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) validateCheckpoint(checkpoint privatePagePoolCheckpoint) privatePagePoolError {
	if p == nil || p.self != p || checkpoint.pool != p || checkpoint.poolEpoch != p.epoch {
		return privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	if checkpoint.id == 0 || p.activeCheckpointID != checkpoint.id || checkpoint.generation != p.generation+1 {
		return privatePagePoolError{code: privatePagePoolErrCheckpointInactive}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) remember(index int, checkpoint privatePagePoolCheckpoint) {
	slot := &p.slots[index]
	if slot.checkpointID == checkpoint.id {
		return
	}
	p.checkpointCleanup++
	p.checkpointSlotCount++
	slot.checkpointID = checkpoint.id
	slot.checkpointSlotNext = p.checkpointSlotHead
	p.checkpointSlotHead = index
	slot.checkpointBound = slot.bound
	slot.checkpointPageNumber = slot.pageNumber
	slot.checkpointAuthorization = slot.authorization
	slot.checkpointScopeID = slot.scopeID
	slot.checkpointScopeAnchor = slot.scopeAnchor
	slot.checkpointScopeAnchorIndex = slot.scopeAnchorIndex
	slot.checkpointScopeVacantNext = slot.scopeVacantNext
	slot.checkpointState = slot.state
	slot.checkpointOwner = slot.owner
	slot.checkpointOrigin = slot.origin
	slot.checkpointPendingTxn = slot.pendingTxn
	slot.checkpointGeneration = slot.generation
	slot.checkpointCommittedOrigin = slot.committedOrigin
	slot.checkpointInUse = slot.inUse
}

func (p *privatePagePool) rememberIndex(index int, checkpoint privatePagePoolCheckpoint) {
	if index == privatePagePoolNoIndex {
		return
	}
	slot := &p.slots[index]
	if slot.indexCheckpointID == checkpoint.id {
		return
	}
	slot.indexCheckpointID = checkpoint.id
	slot.indexCheckpointNext = p.checkpointIndexHead
	p.checkpointIndexHead = index
	p.checkpointIndexCount++
	slot.checkpointIndexLeft = slot.indexLeft
	slot.checkpointIndexRight = slot.indexRight
	slot.checkpointIndexHeight = slot.indexHeight
	slot.checkpointIndexFree = slot.indexFree
	slot.checkpointIndexInUse = slot.indexInUse
	slot.checkpointScopeLeft = slot.scopeLeft
	slot.checkpointScopeRight = slot.scopeRight
	slot.checkpointScopeHeight = slot.scopeHeight
	slot.checkpointScopeFree = slot.scopeFree
	slot.checkpointScopeInUse = slot.scopeInUse
}

func (p *privatePagePool) rememberScopeHeader(anchor int, checkpoint privatePagePoolCheckpoint) {
	slot := &p.slots[anchor]
	if slot.scopeCheckpointID == checkpoint.id {
		return
	}
	slot.scopeCheckpointID = checkpoint.id
	slot.scopeCheckpointNext = p.checkpointScopeHead
	p.checkpointScopeHead = anchor
	p.checkpointScopeCount++
	slot.checkpointScopeRoot = slot.scopeRoot
	slot.checkpointScopeVacantHead = slot.scopeVacantHead
	slot.checkpointScopeBound = slot.scopeBound
}

func (p *privatePagePool) validateBindingPage(
	pageNumber uint32,
	authorization privatePageAuthorization,
) (bool, privatePagePoolError) {
	if pageNumber < 2 {
		return false, privatePagePoolError{code: privatePagePoolErrPageOutOfBounds, page: pageNumber}
	}
	switch authorization {
	case privatePageCommittedFree, privatePageReclaimed:
		if uint64(pageNumber) >= p.committedPageCount {
			return false, privatePagePoolError{code: privatePagePoolErrInvalidAuthorization, page: pageNumber, authorization: authorization}
		}
		return false, privatePagePoolError{}
	case privatePageAppended:
		page := uint64(pageNumber)
		if page < p.committedPageCount || page > p.pendingPageCount {
			return false, privatePagePoolError{code: privatePagePoolErrInvalidAuthorization, page: pageNumber, authorization: authorization}
		}
		if page == p.pendingPageCount {
			if p.pendingPageCount == MaxPageCount {
				return false, privatePagePoolError{code: privatePagePoolErrPageOutOfBounds, page: pageNumber}
			}
			return true, privatePagePoolError{}
		}
		return false, privatePagePoolError{}
	default:
		return false, privatePagePoolError{code: privatePagePoolErrInvalidAuthorization, page: pageNumber, authorization: authorization}
	}
}

func (p *privatePagePool) bindPage(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	pageNumber uint32,
	authorization privatePageAuthorization,
) (int, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return 0, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return 0, problem
	}
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return 0, problem
	}
	extendsTail, problem := p.validateBindingPage(pageNumber, authorization)
	if problem.failed() {
		return 0, problem
	}
	if previous, found := p.slotIndex(pageNumber); found {
		return 0, privatePagePoolError{
			code: privatePagePoolErrPagesNotStrict, previousPage: p.slots[previous].pageNumber, page: pageNumber,
		}
	}
	index := anchor.scopeVacantHead
	if index == privatePagePoolNoIndex {
		return 0, privatePagePoolError{code: privatePagePoolErrBudget, required: anchor.scopeBound + 1, actual: anchor.scopeCapacity}
	}
	if anchor.scopeCapacity <= 0 || anchor.scopeBound < 0 || anchor.scopeBound >= anchor.scopeCapacity ||
		index < 0 || index >= len(p.slots) {
		return 0, privatePagePoolError{code: privatePagePoolErrStaleScope}
	}
	slot := &p.slots[index]
	remainingVacant := anchor.scopeCapacity - anchor.scopeBound - 1
	if !p.validScopedVacancySlot(scope, index) ||
		!p.validScopedVacancyHead(scope, slot.scopeVacantNext, remainingVacant) {
		return 0, privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	if anchor.scopeBound == int(^uint(0)>>1) {
		return 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: pageNumber}
	}
	if problem = p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
		return 0, problem
	}

	p.remember(index, checkpoint)
	p.rememberIndex(index, checkpoint)
	p.rememberScopeHeader(scope.anchor, checkpoint)
	anchor.scopeVacantHead = slot.scopeVacantNext
	anchor.scopeBound++
	slot.bound = true
	slot.pageNumber = pageNumber
	slot.authorization = authorization
	slot.scopeVacantNext = privatePagePoolNoIndex
	slot.state = privatePageAvailable
	slot.inUse = false
	slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
	slot.pendingTxn, slot.generation, slot.committedOrigin = 0, 0, 0
	clear(slot.bytes[:])
	slot.indexLeft, slot.indexRight, slot.indexHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.indexFree, slot.indexInUse = 1, 0
	slot.scopeLeft, slot.scopeRight, slot.scopeHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.scopeFree, slot.scopeInUse = 1, 0
	p.indexRoot = p.indexInsertPrepared(p.indexRoot, index, checkpoint)
	anchor.scopeRoot = p.scopeInsertPrepared(anchor.scopeRoot, index, checkpoint)
	if extendsTail {
		p.pendingPageCount++
	}
	slot.epoch++
	p.advanceMutationPrepared()
	return index, privatePagePoolError{}
}

// bindPageForCheckpointPrepared is the infallible apply half used after the
// reservation binder has validated the complete source set, canonical scope
// mapping, tail sequence, slot epochs, and aggregate checkpoint headroom.
func (p *privatePagePool) bindPageForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	pageNumber uint32,
	authorization privatePageAuthorization,
	fences ...*privateWriterWorkFence,
) (int, privatePagePoolError) {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex, problem
	}
	anchor := &p.slots[scope.anchor]
	index := anchor.scopeVacantHead
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex, problem
	}
	slot := &p.slots[index]
	p.remember(index, checkpoint)
	p.rememberIndex(index, checkpoint)
	p.rememberScopeHeader(scope.anchor, checkpoint)
	anchor.scopeVacantHead = slot.scopeVacantNext
	anchor.scopeBound++
	slot.bound = true
	slot.pageNumber = pageNumber
	slot.authorization = authorization
	slot.scopeVacantNext = privatePagePoolNoIndex
	slot.state = privatePageAvailable
	slot.inUse = false
	slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
	slot.pendingTxn, slot.generation, slot.committedOrigin = 0, 0, 0
	clear(slot.bytes[:])
	slot.indexLeft, slot.indexRight, slot.indexHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.indexFree, slot.indexInUse = 1, 0
	slot.scopeLeft, slot.scopeRight, slot.scopeHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 1
	slot.scopeFree, slot.scopeInUse = 1, 0
	p.indexRoot = p.indexInsertPrepared(p.indexRoot, index, checkpoint)
	anchor.scopeRoot = p.scopeInsertPrepared(anchor.scopeRoot, index, checkpoint)
	if authorization == privatePageAppended {
		p.pendingPageCount++
	}
	slot.epoch++
	p.advanceMutationPrepared()
	return index, privatePagePoolError{}
}

func (p *privatePagePool) claimSlotForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	index int,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	p.remember(index, checkpoint)
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = privatePageOwnerBitmap, privatePageBitmap
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) writeSlotForCheckpointPrepared(
	index int,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) setSlotCommittedOriginForCheckpointPrepared(
	index int,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].committedOrigin = committedOrigin
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) unbindPage(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	pageNumber uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return problem
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	if !slot.bound || (slot.state != privatePageAvailable && slot.state != privatePageReleasedFree && slot.state != privatePageReleasedTail) ||
		slot.inUse || slot.owner != privatePageOwnerNone || slot.origin != privatePageOriginNone ||
		slot.pendingTxn != 0 || slot.generation != 0 || anchor.scopeCapacity <= 0 ||
		anchor.scopeBound <= 0 || anchor.scopeBound > anchor.scopeCapacity {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	if !p.validScopedVacancyHead(scope, anchor.scopeVacantHead, anchor.scopeCapacity-anchor.scopeBound) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	shrinksTail := slot.authorization == privatePageAppended && p.pendingPageCount != 0 && uint64(pageNumber) == p.pendingPageCount-1
	if problem = p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
		return problem
	}

	p.remember(index, checkpoint)
	p.rememberIndex(index, checkpoint)
	p.rememberScopeHeader(scope.anchor, checkpoint)
	p.indexRoot, _ = p.indexDeletePrepared(p.indexRoot, pageNumber, checkpoint)
	anchor.scopeRoot, _ = p.scopeDeletePrepared(anchor.scopeRoot, pageNumber, checkpoint)
	anchor.scopeBound--
	slot.bound = false
	slot.pageNumber = 0
	slot.authorization = privatePageAuthorizationNone
	slot.state = privatePageAvailable
	slot.inUse = false
	slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
	slot.pendingTxn, slot.generation, slot.committedOrigin = 0, 0, 0
	slot.pendingReturnState = 0
	clear(slot.bytes[:])
	slot.indexLeft, slot.indexRight, slot.indexHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 0
	slot.indexFree, slot.indexInUse = 0, 0
	slot.scopeLeft, slot.scopeRight, slot.scopeHeight = privatePagePoolNoIndex, privatePagePoolNoIndex, 0
	slot.scopeFree, slot.scopeInUse = 0, 0
	slot.scopeVacantNext = anchor.scopeVacantHead
	anchor.scopeVacantHead = index
	if shrinksTail {
		p.pendingPageCount--
	}
	slot.epoch++
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) lowestAvailableSlotInScope(scope privatePageReservationScope) (int, privatePagePoolError) {
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return privatePagePoolNoIndex, problem
	}
	lowest := anchor.scopeRoot
	for lowest != privatePagePoolNoIndex {
		left := p.slots[lowest].scopeLeft
		if left != privatePagePoolNoIndex && p.slots[left].scopeFree != 0 {
			lowest = left
			continue
		}
		if p.slots[lowest].state == privatePageAvailable {
			return lowest, privatePagePoolError{}
		}
		lowest = p.slots[lowest].scopeRight
	}
	return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrBudget, required: anchor.scopeBound + 1, actual: anchor.scopeCapacity}
}

func (p *privatePagePool) availableSlotAtRankInScope(
	scope privatePageReservationScope,
	rank int,
) (int, privatePagePoolError) {
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return privatePagePoolNoIndex, problem
	}
	available := 0
	if anchor.scopeRoot != privatePagePoolNoIndex {
		available = int(p.slots[anchor.scopeRoot].scopeFree)
	}
	if rank < 0 || rank >= available {
		return privatePagePoolNoIndex, privatePagePoolError{
			code: privatePagePoolErrBudget, required: rank + 1, actual: available,
		}
	}
	for root := anchor.scopeRoot; root != privatePagePoolNoIndex; {
		slot := &p.slots[root]
		if !slot.bound || slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
			return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
		}
		leftFree := 0
		if slot.scopeLeft != privatePagePoolNoIndex {
			leftFree = int(p.slots[slot.scopeLeft].scopeFree)
		}
		if rank < leftFree {
			root = slot.scopeLeft
			continue
		}
		rank -= leftFree
		if slot.state == privatePageAvailable {
			if rank == 0 {
				return root, privatePagePoolError{}
			}
			rank--
		}
		root = slot.scopeRight
	}
	return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrInvalidState}
}

func (p *privatePagePool) claimLowestInScope(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return privatePageToken{}, problem
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	index, problem := p.lowestAvailableSlotInScope(scope)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	return p.claimSlotPrepared(checkpoint, index, owner, origin)
}

func (p *privatePagePool) claimPageInScope(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return privatePageToken{}, problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return privatePageToken{}, problem
	}
	index, found := p.slotIndex(pageNumber)
	if !found || p.slots[index].state != privatePageAvailable {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if p.slots[index].scopeID != scope.id || p.slots[index].scopeAnchorIndex != scope.anchor {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	return p.claimSlotPrepared(checkpoint, index, owner, origin)
}

type privatePageToken struct {
	pool       *privatePagePool
	poolEpoch  uint64
	slotEpoch  uint64
	slot       int
	owner      privatePageOwner
	pendingTxn uint64
	generation uint64
	scopeID    uint64
}

func (p *privatePagePool) tokenFor(index int) privatePageToken {
	slot := &p.slots[index]
	return privatePageToken{
		pool: p, poolEpoch: p.epoch, slotEpoch: slot.epoch, slot: index, owner: slot.owner,
		pendingTxn: slot.pendingTxn, generation: slot.generation, scopeID: slot.scopeID,
	}
}

func (p *privatePagePool) validateToken(token privatePageToken) (*privatePagePoolSlot, privatePagePoolError) {
	if p == nil || p.self != p || token.pool != p || token.poolEpoch != p.epoch || token.slot < 0 || token.slot >= len(p.slots) {
		return nil, privatePagePoolError{code: privatePagePoolErrCrossPool}
	}
	slot := &p.slots[token.slot]
	if !slot.bound || token.slotEpoch != slot.epoch || token.owner != slot.owner || token.scopeID != slot.scopeID || token.pendingTxn != p.pendingTxn ||
		slot.pendingTxn != p.pendingTxn || token.pendingTxn != slot.pendingTxn || token.generation != slot.generation ||
		slot.state != privatePageInUse || !slot.inUse {
		return nil, privatePagePoolError{code: privatePagePoolErrStaleToken, page: slot.pageNumber}
	}
	return slot, privatePagePoolError{}
}

func (p *privatePagePool) readPage(token privatePageToken, destination *[PageSize]byte) privatePagePoolError {
	if destination == nil {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return problem
	}
	*destination = slot.bytes
	return privatePagePoolError{}
}

func (p *privatePagePool) readPageInScope(
	scope privatePageReservationScope,
	token privatePageToken,
	destination *[PageSize]byte,
) privatePagePoolError {
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.readPage(token, destination)
}

func (p *privatePagePool) writePage(
	token privatePageToken,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, token.slot, fences...,
	); problem.failed() {
		return problem
	}
	if source == nil {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return problem
	}
	if p.activeCheckpointID != 0 &&
		(slot.checkpointID != p.activeCheckpointID || slot.checkpointState == privatePageInUse) {
		return privatePagePoolError{code: privatePagePoolErrTransferPending, page: slot.pageNumber}
	}
	if problem = p.requireForwardMutationSteps(1); problem.failed() {
		return problem
	}
	slot.bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) writePageInScope(
	scope privatePageReservationScope,
	token privatePageToken,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.writePage(token, source, fences...)
}

func (p *privatePagePool) borrow(pageNumber uint32, owner privatePageOwner) (privatePageToken, privatePagePoolError) {
	if p != nil && p.self == p && p.activeScopes != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	slot := &p.slots[index]
	if slot.state != privatePageInUse || !slot.inUse {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if slot.owner != owner {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOwnerMismatch, page: pageNumber}
	}
	return p.tokenFor(index), privatePagePoolError{}
}

func (p *privatePagePool) borrowInScope(
	scope privatePageReservationScope,
	pageNumber uint32,
	owner privatePageOwner,
) (privatePageToken, privatePagePoolError) {
	if _, problem := p.validateScope(scope); problem.failed() {
		return privatePageToken{}, problem
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	if slot.state != privatePageInUse || !slot.inUse {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if slot.owner != owner {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOwnerMismatch, page: pageNumber}
	}
	return p.tokenFor(index), privatePagePoolError{}
}

func (p *privatePagePool) borrowExact(
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	token, problem := p.borrow(pageNumber, owner)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	if p.slots[token.slot].origin != origin {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOriginMismatch, page: pageNumber}
	}
	return token, privatePagePoolError{}
}

func (p *privatePagePool) borrowExactInScope(
	scope privatePageReservationScope,
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	token, problem := p.borrowInScope(scope, pageNumber, owner)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	if p.slots[token.slot].origin != origin {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOriginMismatch, page: pageNumber}
	}
	return token, privatePagePoolError{}
}

func (p *privatePagePool) readUint64(token privatePageToken, offset int) (uint64, privatePagePoolError) {
	if offset < 0 || offset > PageSize-8 {
		return 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return 0, problem
	}
	return binary.LittleEndian.Uint64(slot.bytes[offset : offset+8]), privatePagePoolError{}
}

func (p *privatePagePool) readUint64InScope(
	scope privatePageReservationScope,
	token privatePageToken,
	offset int,
) (uint64, privatePagePoolError) {
	if _, problem := p.validateScope(scope); problem.failed() {
		return 0, problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return 0, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.readUint64(token, offset)
}

func (p *privatePagePool) setCommittedOrigin(
	token privatePageToken,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, token.slot, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return problem
	}
	if p.activeCheckpointID != 0 &&
		(slot.checkpointID != p.activeCheckpointID || slot.checkpointState == privatePageInUse) {
		return privatePagePoolError{code: privatePagePoolErrTransferPending, page: slot.pageNumber}
	}
	if problem = p.requireForwardMutationSteps(1); problem.failed() {
		return problem
	}
	slot.committedOrigin = committedOrigin
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) setCommittedOriginInScope(
	scope privatePageReservationScope,
	token privatePageToken,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.setCommittedOrigin(token, committedOrigin, fences...)
}

func (p *privatePagePool) claimLowest(
	checkpoint privatePagePoolCheckpoint,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return privatePageToken{}, problem
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	if p.activeScopes != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	lowest := p.indexRoot
	for lowest != privatePagePoolNoIndex {
		left := p.slots[lowest].indexLeft
		if left != privatePagePoolNoIndex && p.slots[left].indexFree != 0 {
			lowest = left
			continue
		}
		if p.slots[lowest].state == privatePageAvailable {
			break
		}
		lowest = p.slots[lowest].indexRight
	}
	if lowest != privatePagePoolNoIndex {
		return p.claimSlotPrepared(checkpoint, lowest, owner, origin)
	}
	return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrBudget, required: p.inUseCount() + 1, actual: len(p.slots)}
}

func (p *privatePagePool) claimPage(
	checkpoint privatePagePoolCheckpoint,
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return privatePageToken{}, problem
	}
	if p.activeScopes != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	index, found := p.slotIndex(pageNumber)
	if !found || p.slots[index].state != privatePageAvailable {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if p.slots[index].scopeID != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	return p.claimSlotPrepared(checkpoint, index, owner, origin)
}

func (p *privatePagePool) claimPageForOperation(
	operation privatePagePoolOperation,
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateOperation(operation); problem.failed() {
		return privatePageToken{}, problem
	}
	if p.activeScopes != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	index, found := p.slotIndex(pageNumber)
	if !found || p.slots[index].state != privatePageAvailable {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if p.slots[index].scopeID != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	slot := &p.slots[index]
	nextEpoch := slot.epoch + 1
	if nextEpoch == 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: pageNumber}
	}
	if problem := p.requireMutationSteps(1); problem.failed() {
		return privatePageToken{}, problem
	}
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = operation.generation
	slot.committedOrigin = 0
	slot.epoch = nextEpoch
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return p.tokenFor(index), privatePagePoolError{}
}

func (p *privatePagePool) operationSlotInScope(
	operation privatePagePoolOperation,
	index int,
) (*privatePagePoolSlot, privatePagePoolError) {
	if problem := p.validateOperation(operation); problem.failed() {
		return nil, problem
	}
	if operation.scopeID == 0 || index < 0 || index >= len(p.slots) {
		return nil, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	slot := &p.slots[index]
	if !slot.bound || slot.scopeID != operation.scopeID || slot.scopeAnchorIndex != operation.scopeAnchor {
		return nil, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	return slot, privatePagePoolError{}
}

func (p *privatePagePool) claimPageForOperationInScope(
	operation privatePagePoolOperation,
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) (privatePageToken, privatePagePoolError) {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateOperation(operation); problem.failed() {
		return privatePageToken{}, problem
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	slot, problem := p.operationSlotInScope(operation, index)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	if slot.state != privatePageAvailable || slot.inUse || slot.owner != privatePageOwnerNone ||
		slot.origin != privatePageOriginNone || slot.pendingTxn != 0 || slot.generation != 0 {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	if slot.epoch == ^uint64(0) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: pageNumber}
	}
	if problem = p.requireMutationSteps(1); problem.failed() {
		return privatePageToken{}, problem
	}
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = operation.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return p.tokenFor(index), privatePagePoolError{}
}

// The prepared operation helpers are valid only after one aggregate preflight
// has proved every slot transition and the complete mutation/epoch headroom.
// Their remaining errors defend operation and scope identity; a correct
// preflight makes those checks unreachable during the apply phase.
func (p *privatePagePool) claimSlotForOperationPrepared(
	operation privatePagePoolOperation,
	index int,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	if p.activeScopes != 0 || slot.scopeID != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	if problem := p.validateOperation(operation); problem.failed() {
		return problem
	}
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = operation.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) claimSlotForOperationInScopePrepared(
	operation privatePagePoolOperation,
	index int,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.operationSlotInScope(operation, index)
	if problem.failed() {
		return problem
	}
	if slot.state != privatePageAvailable || slot.inUse || slot.owner != privatePageOwnerNone ||
		slot.origin != privatePageOriginNone || slot.pendingTxn != 0 || slot.generation != 0 ||
		!validPrivatePageOwnerOrigin(owner, origin) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = operation.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) writeSlotForOperationInScopePrepared(
	operation privatePagePoolOperation,
	index int,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.operationSlotInScope(operation, index)
	if problem.failed() {
		return problem
	}
	if source == nil || slot.state != privatePageInUse || slot.owner != privatePageOwnerBitmap ||
		slot.origin != privatePageBitmap || slot.pendingTxn != p.pendingTxn {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	slot.bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) setSlotCommittedOriginForOperationInScopePrepared(
	operation privatePagePoolOperation,
	index int,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.operationSlotInScope(operation, index)
	if problem.failed() {
		return problem
	}
	if slot.state != privatePageInUse || slot.owner != privatePageOwnerBitmap ||
		slot.origin != privatePageBitmap || slot.pendingTxn != p.pendingTxn || slot.generation != operation.generation {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	slot.committedOrigin = committedOrigin
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) releaseSlotForOperationInScopePrepared(
	operation privatePagePoolOperation,
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.operationSlotInScope(operation, index)
	if problem.failed() {
		return problem
	}
	if slot.state != privatePageInUse || !slot.inUse || slot.pendingTxn != p.pendingTxn ||
		(state != privatePageAvailable && state != privatePageReleasedFree && state != privatePageReleasedTail) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	return p.releaseSlotMutationPrepared(index, state, fences...)
}

func (p *privatePagePool) claimSlotForOperationInScopeTerminalPrepared(
	operation privatePagePoolOperation,
	index int,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	return p.claimSlotForOperationTerminalPrepared(
		operation, index, owner, origin, fences...,
	)
}

func (p *privatePagePool) claimSlotForOperationTerminalPrepared(
	operation privatePagePoolOperation,
	index int,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = operation.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) writeSlotForOperationInScopeTerminalPrepared(
	index int,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) setSlotCommittedOriginForOperationInScopeTerminalPrepared(
	index int,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].committedOrigin = committedOrigin
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) releaseSlotForOperationInScopeTerminalPrepared(
	operation privatePagePoolOperation,
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalOperation(
		operation, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalOperationActive, fences...,
	); problem.failed() {
		return problem
	}
	return p.releaseSlotMutationPrepared(index, state, fences...)
}

func (p *privatePagePool) writeSlotPrepared(
	index int,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) writeSlotInScopePrepared(
	scope privatePageReservationScope,
	index int,
	source *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if source == nil || index < 0 || index >= len(p.slots) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot := &p.slots[index]
	if !slot.bound || slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	if slot.state != privatePageInUse || slot.owner != privatePageOwnerRetirement ||
		(slot.origin != privatePageRetirementTree && slot.origin != privatePageRetirementBlob) ||
		slot.pendingTxn != p.pendingTxn {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	slot.bytes = *source
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) setSlotCommittedOriginPrepared(
	index int,
	committedOrigin uint32,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	p.slots[index].committedOrigin = committedOrigin
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) releaseSlotPrepared(
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	if p.activeScopes != 0 || slot.scopeID != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	return p.releaseSlotMutationPrepared(index, state, fences...)
}

func (p *privatePagePool) claimSlotPrepared(
	checkpoint privatePagePoolCheckpoint,
	index int,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) (privatePageToken, privatePagePoolError) {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	slot := &p.slots[index]
	if problem := p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
		return privatePageToken{}, problem
	}
	nextEpoch := slot.epoch + 1
	p.remember(index, checkpoint)
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch = nextEpoch
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return p.tokenFor(index), privatePagePoolError{}
}

func (p *privatePagePool) lowestAvailableSlot() int {
	lowest := p.indexRoot
	for lowest != privatePagePoolNoIndex {
		left := p.slots[lowest].indexLeft
		if left != privatePagePoolNoIndex && p.slots[left].indexFree != 0 {
			lowest = left
			continue
		}
		if p.slots[lowest].state == privatePageAvailable {
			return lowest
		}
		lowest = p.slots[lowest].indexRight
	}
	return privatePagePoolNoIndex
}

func (p *privatePagePool) claimLowestForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex
	}
	index := p.lowestAvailableSlot()
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex
	}
	slot := &p.slots[index]
	p.remember(index, checkpoint)
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return index
}

func (p *privatePagePool) claimLowestInScopeForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex
	}
	index, _ := p.lowestAvailableSlotInScope(scope)
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePagePoolNoIndex
	}
	slot := &p.slots[index]
	p.remember(index, checkpoint)
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return index
}

func (p *privatePagePool) preflightLowestAvailableEpochs(root int, remaining *int) privatePagePoolError {
	if p.activeScopes != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	if root == privatePagePoolNoIndex || *remaining == 0 {
		return privatePagePoolError{}
	}
	left := p.slots[root].indexLeft
	if left != privatePagePoolNoIndex && p.slots[left].indexFree != 0 {
		if problem := p.preflightLowestAvailableEpochs(left, remaining); problem.failed() {
			return problem
		}
	}
	if *remaining != 0 && p.slots[root].state == privatePageAvailable {
		if p.slots[root].inUse || p.slots[root].owner != privatePageOwnerNone ||
			p.slots[root].origin != privatePageOriginNone || p.slots[root].pendingTxn != 0 ||
			p.slots[root].generation != 0 {
			return privatePagePoolError{code: privatePagePoolErrInvalidState, page: p.slots[root].pageNumber}
		}
		if p.slots[root].epoch > ^uint64(0)-2 {
			return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: p.slots[root].pageNumber}
		}
		*remaining--
	}
	right := p.slots[root].indexRight
	if *remaining != 0 && right != privatePagePoolNoIndex && p.slots[right].indexFree != 0 {
		return p.preflightLowestAvailableEpochs(right, remaining)
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) preflightLowestAvailableEpochsInScope(
	scope privatePageReservationScope,
	root int,
	remaining *int,
	fingerprint *uint64,
) privatePagePoolError {
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if root == privatePagePoolNoIndex || *remaining == 0 {
		return privatePagePoolError{}
	}
	slot := &p.slots[root]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor || !slot.bound {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	left := slot.scopeLeft
	if left != privatePagePoolNoIndex && p.slots[left].scopeFree != 0 {
		if problem := p.preflightLowestAvailableEpochsInScope(scope, left, remaining, fingerprint); problem.failed() {
			return problem
		}
	}
	if *remaining != 0 && slot.state == privatePageAvailable {
		if slot.inUse || slot.owner != privatePageOwnerNone ||
			slot.origin != privatePageOriginNone || slot.pendingTxn != 0 ||
			slot.generation != 0 {
			return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
		}
		if slot.epoch > ^uint64(0)-2 {
			return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
		}
		*fingerprint = privatePageDestinationFingerprint(*fingerprint, scope.id, root, slot.pageNumber, slot.epoch)
		*remaining--
	}
	right := slot.scopeRight
	if *remaining != 0 && right != privatePagePoolNoIndex && p.slots[right].scopeFree != 0 {
		return p.preflightLowestAvailableEpochsInScope(scope, right, remaining, fingerprint)
	}
	return privatePagePoolError{}
}

func privatePageDestinationFingerprint(current, scopeID uint64, storageSlot int, pageNumber uint32, epoch uint64) uint64 {
	current ^= scopeID + 0x9e3779b97f4a7c15 + (current << 6) + (current >> 2)
	current ^= uint64(storageSlot) + 0x9e3779b97f4a7c15 + (current << 6) + (current >> 2)
	current ^= uint64(pageNumber) + 0x9e3779b97f4a7c15 + (current << 6) + (current >> 2)
	current ^= epoch + 0x9e3779b97f4a7c15 + (current << 6) + (current >> 2)
	return current
}

func (p *privatePagePool) releaseSlotForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	if p.activeScopes != 0 || slot.scopeID != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	if slot.checkpointID != checkpoint.id {
		p.remember(index, checkpoint)
	}
	if slot.checkpointState == privatePageInUse {
		slot.state = privatePagePendingReturn
		slot.inUse = false
		slot.pendingReturnState = state
		slot.epoch++
		p.refreshSlotIndexes(slot)
		p.advanceMutationPrepared()
		return privatePagePoolError{}
	}
	return p.releaseSlotMutationPrepared(index, state, fences...)
}

func (p *privatePagePool) releaseSlotForCheckpointInScopePrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if index < 0 || index >= len(p.slots) {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	if slot.checkpointID != checkpoint.id {
		p.remember(index, checkpoint)
	}
	if slot.checkpointState == privatePageInUse {
		slot.state = privatePagePendingReturn
		slot.inUse = false
		slot.pendingReturnState = state
		slot.epoch++
		p.refreshSlotIndexes(slot)
		p.advanceMutationPrepared()
		return privatePagePoolError{}
	}
	return p.releaseSlotMutationPrepared(index, state, fences...)
}

// releaseSealedSlotForCheckpointPrepared is the coordinator-only counterpart
// of scoped release. It preserves the sealed scope and its cleanup authority;
// exact provenance validation is completed before the checkpoint starts.
func (p *privatePagePool) releaseSealedSlotForCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	if _, problem := p.validateSealedScope(scope); problem.failed() {
		return problem
	}
	if index < 0 || index >= len(p.slots) {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	if slot.checkpointID != checkpoint.id {
		p.remember(index, checkpoint)
	}
	if slot.checkpointState != privatePageInUse {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	slot.state = privatePagePendingReturn
	slot.inUse = false
	slot.pendingReturnState = state
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) installRetirementPageInScopePrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	index int,
	origin privatePageOrigin,
	page *[PageSize]byte,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	p.remember(index, checkpoint)
	clear(slot.bytes[:])
	slot.state, slot.inUse = privatePageInUse, true
	slot.owner, slot.origin = privatePageOwnerRetirement, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	slot.bytes = *page
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) releaseRetirementSlotInScopePrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	index int,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		index, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	if slot.checkpointID != checkpoint.id {
		p.remember(index, checkpoint)
	}
	if slot.checkpointState == privatePageInUse {
		slot.state = privatePagePendingReturn
		slot.inUse = false
		slot.pendingReturnState = privatePageAvailable
		slot.epoch++
		p.refreshSlotIndexes(slot)
		p.advanceMutationPrepared()
		return privatePagePoolError{}
	}
	return p.releaseSlotMutationPrepared(index, privatePageAvailable, fences...)
}

func (p *privatePagePool) checkpointSlotJournalNext(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	scoped bool,
	index int,
) (int, privatePagePoolError) {
	if index < 0 || index >= len(p.slots) {
		return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	slot := &p.slots[index]
	if slot.checkpointID != checkpoint.id {
		return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	if scoped {
		anchor := index == scope.anchor
		if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor || slot.scopeAnchor != anchor ||
			slot.checkpointScopeID != scope.id || slot.checkpointScopeAnchorIndex != scope.anchor ||
			slot.checkpointScopeAnchor != anchor {
			return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
		}
	} else if p.activeScopes != 0 {
		if slot.scopeID == 0 || slot.scopeID != slot.checkpointScopeID ||
			slot.scopeAnchorIndex != slot.checkpointScopeAnchorIndex ||
			slot.scopeAnchor != slot.checkpointScopeAnchor ||
			slot.scopeAnchorIndex < 0 || slot.scopeAnchorIndex >= len(p.slots) {
			return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
		}
		anchor := &p.slots[slot.scopeAnchorIndex]
		if !anchor.scopeAnchor || anchor.scopeID != slot.scopeID || anchor.scopeAnchorIndex != slot.scopeAnchorIndex {
			return privatePagePoolNoIndex, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
		}
	}
	return slot.checkpointSlotNext, privatePagePoolError{}
}

func (p *privatePagePool) checkpointSlotJournalStats(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	scoped bool,
) (remembered, pendingReturns int, problem privatePagePoolError) {
	if p.checkpointSlotCount < 0 || p.checkpointSlotCount > len(p.slots) ||
		uint64(p.checkpointSlotCount) != p.checkpointCleanup ||
		(p.checkpointSlotCount == 0) != (p.checkpointSlotHead == privatePagePoolNoIndex) {
		return 0, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	// Detect a corrupt cycle without mutating caller-owned journal state.
	slow, fast := p.checkpointSlotHead, p.checkpointSlotHead
	for steps := 0; fast != privatePagePoolNoIndex && steps <= p.checkpointSlotCount; steps++ {
		slow, problem = p.checkpointSlotJournalNext(checkpoint, scope, scoped, slow)
		if problem.failed() {
			return 0, 0, problem
		}
		fast, problem = p.checkpointSlotJournalNext(checkpoint, scope, scoped, fast)
		if problem.failed() {
			return 0, 0, problem
		}
		if fast != privatePagePoolNoIndex {
			fast, problem = p.checkpointSlotJournalNext(checkpoint, scope, scoped, fast)
			if problem.failed() {
				return 0, 0, problem
			}
		}
		if slow != privatePagePoolNoIndex && slow == fast {
			return 0, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
	}
	index := p.checkpointSlotHead
	for index != privatePagePoolNoIndex {
		if remembered >= p.checkpointSlotCount {
			return 0, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		next, nextProblem := p.checkpointSlotJournalNext(checkpoint, scope, scoped, index)
		if nextProblem.failed() {
			return 0, 0, nextProblem
		}
		if p.slots[index].state == privatePagePendingReturn {
			if p.slots[index].epoch == ^uint64(0) {
				return 0, 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: p.slots[index].pageNumber}
			}
			pendingReturns++
		}
		remembered++
		index = next
	}
	if remembered != p.checkpointSlotCount {
		return 0, 0, privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	return remembered, pendingReturns, privatePagePoolError{}
}

func (p *privatePagePool) checkpointScopeStats(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	root int,
) (uint64, uint64, privatePagePoolError) {
	if scope.anchor < 0 || scope.anchor >= len(p.slots) || p.slots[scope.anchor].scopeRoot != root {
		return 0, 0, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	remembered, pending, problem := p.checkpointSlotJournalStats(checkpoint, scope, true)
	return uint64(remembered), uint64(pending), problem
}

func (p *privatePagePool) checkpointIndexJournalValid(checkpoint privatePagePoolCheckpoint) privatePagePoolError {
	if p.checkpointIndexCount < 0 || p.checkpointIndexCount > len(p.slots) ||
		(p.checkpointIndexCount == 0) != (p.checkpointIndexHead == privatePagePoolNoIndex) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	index := p.checkpointIndexHead
	for visited := 0; index != privatePagePoolNoIndex; visited++ {
		if visited >= p.checkpointIndexCount || index < 0 || index >= len(p.slots) {
			return privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		slot := &p.slots[index]
		if slot.indexCheckpointID != checkpoint.id || slot.indexCheckpointNext == index {
			return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
		}
		index = slot.indexCheckpointNext
		if index == privatePagePoolNoIndex && visited+1 != p.checkpointIndexCount {
			return privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) checkpointScopeJournalValid(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	scoped bool,
) privatePagePoolError {
	if p.checkpointScopeCount < 0 || p.checkpointScopeCount > len(p.slots) ||
		(p.checkpointScopeCount == 0) != (p.checkpointScopeHead == privatePagePoolNoIndex) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	index := p.checkpointScopeHead
	for visited := 0; index != privatePagePoolNoIndex; visited++ {
		if visited >= p.checkpointScopeCount || index < 0 || index >= len(p.slots) {
			return privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
		slot := &p.slots[index]
		if slot.scopeCheckpointID != checkpoint.id || slot.scopeCheckpointNext == index ||
			!slot.scopeAnchor || slot.scopeAnchorIndex != index || slot.scopeID == 0 {
			return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
		}
		if scoped && (slot.scopeID != scope.id || index != scope.anchor) {
			return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
		}
		index = slot.scopeCheckpointNext
		if index == privatePagePoolNoIndex && visited+1 != p.checkpointScopeCount {
			return privatePagePoolError{code: privatePagePoolErrInvalidState}
		}
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) commitCheckpointSlotsPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointSlotHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.checkpointSlotNext
		if slot.state == privatePagePendingReturn {
			_ = p.releaseSlotMutationPrepared(index, slot.pendingReturnState, fences...)
		}
		slot.checkpointID = 0
		slot.checkpointSlotNext = privatePagePoolNoIndex
		index = next
		visits++
	}
	p.checkpointCleanup = 0
	p.checkpointSlotHead = privatePagePoolNoIndex
	p.checkpointSlotCount = 0
	return visits
}

func (p *privatePagePool) commitCheckpointPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	)
	if problem.failed() {
		return problem
	}
	p.commitCheckpointSlotsPrepared(checkpoint, fences...)
	p.clearCheckpointIndexesTerminalPrepared(checkpoint, fences...)
	p.clearCheckpointScopesTerminalPrepared(checkpoint, fences...)
	p.generation = checkpoint.generation
	p.activeCheckpointID = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalConsumed
		fences[0].slot.terminalCommitment.setCheckpointConsumed()
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) rollbackCheckpointSlotsPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointSlotHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.checkpointSlotNext
		if slot.checkpointState != privatePageInUse {
			clear(slot.bytes[:])
		}
		slot.bound = slot.checkpointBound
		slot.pageNumber = slot.checkpointPageNumber
		slot.authorization = slot.checkpointAuthorization
		slot.scopeID = slot.checkpointScopeID
		slot.scopeAnchor = slot.checkpointScopeAnchor
		slot.scopeAnchorIndex = slot.checkpointScopeAnchorIndex
		slot.scopeVacantNext = slot.checkpointScopeVacantNext
		slot.state = slot.checkpointState
		slot.owner = slot.checkpointOwner
		slot.origin = slot.checkpointOrigin
		slot.pendingTxn = slot.checkpointPendingTxn
		slot.generation = slot.checkpointGeneration
		slot.committedOrigin = slot.checkpointCommittedOrigin
		slot.inUse = slot.checkpointInUse
		slot.pendingReturnState = 0
		slot.epoch++
		slot.checkpointID = 0
		slot.checkpointSlotNext = privatePagePoolNoIndex
		p.advanceMutationPrepared()
		index = next
		visits++
	}
	p.checkpointCleanup = 0
	p.checkpointSlotHead = privatePagePoolNoIndex
	p.checkpointSlotCount = 0
	return visits
}

func (p *privatePagePool) rollbackCheckpointIndexesPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointIndexHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.indexCheckpointNext
		slot.indexLeft = slot.checkpointIndexLeft
		slot.indexRight = slot.checkpointIndexRight
		slot.indexHeight = slot.checkpointIndexHeight
		slot.indexFree = slot.checkpointIndexFree
		slot.indexInUse = slot.checkpointIndexInUse
		slot.scopeLeft = slot.checkpointScopeLeft
		slot.scopeRight = slot.checkpointScopeRight
		slot.scopeHeight = slot.checkpointScopeHeight
		slot.scopeFree = slot.checkpointScopeFree
		slot.scopeInUse = slot.checkpointScopeInUse
		slot.indexCheckpointID = 0
		slot.indexCheckpointNext = privatePagePoolNoIndex
		index = next
		visits++
	}
	p.checkpointIndexHead = privatePagePoolNoIndex
	p.checkpointIndexCount = 0
	return visits
}

func (p *privatePagePool) rollbackCheckpointInScope(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	)
	if problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	_, problem = p.validateScope(scope)
	if problem.failed() {
		return problem
	}
	remembered, _, problem := p.checkpointSlotJournalStats(checkpoint, scope, true)
	if problem.failed() {
		return problem
	}
	if problem = p.checkpointIndexJournalValid(checkpoint); problem.failed() {
		return problem
	}
	if problem = p.checkpointScopeJournalValid(checkpoint, scope, true); problem.failed() {
		return problem
	}
	if problem = p.requireMutationSteps(uint64(remembered)); problem.failed() {
		return problem
	}
	p.rollbackCheckpointSlotsPrepared(checkpoint, fences...)
	p.rollbackCheckpointIndexesPrepared(checkpoint, fences...)
	p.rollbackCheckpointScopesPrepared(checkpoint, fences...)
	p.indexRoot = checkpoint.indexRoot
	p.pendingPageCount = checkpoint.pendingPageCount
	p.activeCheckpointID = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalConsumed
		fences[0].slot.terminalCommitment.setCheckpointRolledBack()
	}
	return privatePagePoolError{}
}

func (p *privatePagePool) commitCheckpointInScopePrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	journal, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	)
	if problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	_, problem = p.validateScope(scope)
	if problem.failed() {
		return problem
	}
	_, pendingReturns, problem := p.checkpointSlotJournalStats(checkpoint, scope, true)
	if problem.failed() {
		return problem
	}
	if problem = p.checkpointIndexJournalValid(checkpoint); problem.failed() {
		return problem
	}
	if problem = p.checkpointScopeJournalValid(checkpoint, scope, true); problem.failed() {
		return problem
	}
	if problem = p.requireMutationSteps(uint64(pendingReturns)); problem.failed() {
		return problem
	}
	p.commitCheckpointSlotsPrepared(checkpoint, fences...)
	p.clearCheckpointIndexesTerminalPrepared(checkpoint, fences...)
	p.clearCheckpointScopesTerminalPrepared(checkpoint, fences...)
	p.generation = checkpoint.generation
	p.activeCheckpointID = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalConsumed
		fences[0].slot.terminalCommitment.setCheckpointConsumed()
	}
	return privatePagePoolError{}
}

type privatePagePoolCheckpointTerminalWork struct {
	scopeSlotVisits   int
	indexSlotVisits   int
	scopeHeaderVisits int
}

func (p *privatePagePool) clearCheckpointIndexesTerminalPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointIndexHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.indexCheckpointNext
		slot.indexCheckpointID = 0
		slot.indexCheckpointNext = privatePagePoolNoIndex
		index = next
		visits++
	}
	p.checkpointIndexHead = privatePagePoolNoIndex
	p.checkpointIndexCount = 0
	return visits
}

func (p *privatePagePool) clearCheckpointScopesTerminalPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointScopeHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.scopeCheckpointNext
		slot.scopeCheckpointID = 0
		slot.scopeCheckpointNext = privatePagePoolNoIndex
		index = next
		visits++
	}
	p.checkpointScopeHead = privatePagePoolNoIndex
	p.checkpointScopeCount = 0
	return visits
}

func (p *privatePagePool) rollbackCheckpointScopesPrepared(
	checkpoint privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) int {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return 0
	}
	visits := 0
	for index := p.checkpointScopeHead; index != privatePagePoolNoIndex; {
		slot := &p.slots[index]
		next := slot.scopeCheckpointNext
		slot.scopeRoot = slot.checkpointScopeRoot
		slot.scopeVacantHead = slot.checkpointScopeVacantHead
		slot.scopeBound = slot.checkpointScopeBound
		slot.scopeCheckpointID = 0
		slot.scopeCheckpointNext = privatePagePoolNoIndex
		index = next
		visits++
	}
	p.checkpointScopeHead = privatePagePoolNoIndex
	p.checkpointScopeCount = 0
	return visits
}

func (p *privatePagePool) commitCheckpointInScopeTerminalPrepared(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	fences ...*privateWriterWorkFence,
) privatePagePoolCheckpointTerminalWork {
	journal, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	)
	if problem.failed() {
		return privatePagePoolCheckpointTerminalWork{}
	}
	work := privatePagePoolCheckpointTerminalWork{
		scopeSlotVisits: p.commitCheckpointSlotsPrepared(checkpoint, fences...),
		indexSlotVisits: p.clearCheckpointIndexesTerminalPrepared(checkpoint, fences...),
	}
	work.scopeHeaderVisits = p.clearCheckpointScopesTerminalPrepared(checkpoint, fences...)
	p.generation = checkpoint.generation
	p.activeCheckpointID = 0
	if journal != nil {
		journal.phase = privateWriterPreparedTerminalConsumed
		fences[0].slot.terminalCommitment.setCheckpointConsumed()
	}
	return work
}

func (p *privatePagePool) transfer(
	checkpoint privatePagePoolCheckpoint,
	token privatePageToken,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) (privatePageToken, privatePagePoolError) {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, nil,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.authorizeCoordinatorTerminalSlotMutation(
		token.slot, privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return privatePageToken{}, problem
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	if owner == slot.owner {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOwnerMismatch, page: slot.pageNumber}
	}
	if problem := p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
		return privatePageToken{}, problem
	}
	nextEpoch := slot.epoch + 1
	p.remember(token.slot, checkpoint)
	slot.owner, slot.origin = owner, origin
	slot.pendingTxn = p.pendingTxn
	slot.generation = checkpoint.generation
	slot.committedOrigin = 0
	slot.epoch = nextEpoch
	p.advanceMutationPrepared()
	return p.tokenFor(token.slot), privatePagePoolError{}
}

func (p *privatePagePool) transferInScope(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	token privatePageToken,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) (privatePageToken, privatePagePoolError) {
	if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
		checkpoint, &scope,
		privateWriterPreparedTerminalCheckpointActive, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return privatePageToken{}, problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.transfer(checkpoint, token, owner, origin, fences...)
}

func (p *privatePagePool) changeOrigin(
	token privatePageToken,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) (privatePageToken, privatePagePoolError) {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, token.slot, fences...,
	); problem.failed() {
		return privatePageToken{}, problem
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return privatePageToken{}, problem
	}
	if !validPrivatePageOwnerOrigin(slot.owner, origin) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrOriginMismatch, page: slot.pageNumber}
	}
	if p.activeCheckpointID != 0 &&
		(slot.checkpointID != p.activeCheckpointID || slot.checkpointState == privatePageInUse) {
		return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrTransferPending, page: slot.pageNumber}
	}
	if p.activeCheckpointID != 0 {
		checkpoint := privatePagePoolCheckpoint{id: p.activeCheckpointID}
		if problem := p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
			return privatePageToken{}, problem
		}
	} else {
		if slot.epoch == ^uint64(0) {
			return privatePageToken{}, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
		}
		if problem := p.requireMutationSteps(1); problem.failed() {
			return privatePageToken{}, problem
		}
	}
	slot.origin = origin
	slot.epoch++
	p.advanceMutationPrepared()
	return p.tokenFor(token.slot), privatePagePoolError{}
}

func (p *privatePagePool) recycle(
	token privatePageToken,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	return p.release(token, privatePageAvailable, fences...)
}

func (p *privatePagePool) returnReleased(
	token privatePageToken,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, token.slot, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return problem
	}
	state := privatePageReleasedFree
	if slot.authorization == privatePageAppended {
		state = privatePageReleasedTail
	}
	return p.release(token, state, fences...)
}

func (p *privatePagePool) returnUnowned(pageNumber uint32, state privatePageState) privatePagePoolError {
	if p == nil || p.self != p || p.activeCheckpointID != 0 {
		return privatePagePoolError{code: privatePagePoolErrCheckpointActive, page: pageNumber}
	}
	if p.activeScopes != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	return p.returnUnownedAt(index, pageNumber, state)
}

func (p *privatePagePool) returnUnownedInScope(
	scope privatePageReservationScope,
	pageNumber uint32,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	if p == nil || p.self != p || p.activeCheckpointID != 0 {
		return privatePagePoolError{code: privatePagePoolErrCheckpointActive, page: pageNumber}
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	index, found := p.slotIndex(pageNumber)
	if !found {
		return privatePagePoolError{code: privatePagePoolErrUnavailable, page: pageNumber}
	}
	slot := &p.slots[index]
	if slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: pageNumber}
	}
	return p.returnUnownedAt(index, pageNumber, state, fences...)
}

func (p *privatePagePool) returnUnownedAt(
	index int,
	pageNumber uint32,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	if slot.state == privatePageInUse || slot.owner != privatePageOwnerNone || slot.origin != privatePageOriginNone {
		return privatePagePoolError{code: privatePagePoolErrOwnerMismatch, page: pageNumber}
	}
	if state != privatePageReleasedFree && state != privatePageReleasedTail {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: pageNumber}
	}
	if state == privatePageReleasedTail && slot.authorization != privatePageAppended {
		return privatePagePoolError{code: privatePagePoolErrInvalidAuthorization, page: pageNumber, authorization: slot.authorization}
	}
	nextEpoch := slot.epoch + 1
	if nextEpoch == 0 {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: pageNumber}
	}
	if problem := p.requireMutationSteps(1); problem.failed() {
		return problem
	}
	clear(slot.bytes[:])
	slot.state = state
	slot.inUse = false
	slot.pendingTxn = 0
	slot.generation = 0
	slot.committedOrigin = 0
	slot.epoch = nextEpoch
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) release(
	token privatePageToken,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, token.slot, fences...,
	); problem.failed() {
		return problem
	}
	slot, problem := p.validateToken(token)
	if problem.failed() {
		return problem
	}
	if state != privatePageAvailable && state != privatePageReleasedFree && state != privatePageReleasedTail {
		return privatePagePoolError{code: privatePagePoolErrInvalidState, page: slot.pageNumber}
	}
	if state == privatePageReleasedTail && slot.authorization != privatePageAppended {
		return privatePagePoolError{code: privatePagePoolErrInvalidAuthorization, page: slot.pageNumber, authorization: slot.authorization}
	}
	if state == privatePageReleasedFree && slot.authorization == privatePageAppended {
		// Appended pages below a retained pending page-count become ordinary
		// free-page authority, but are still not committed until publication.
		state = privatePageReleasedFree
	}
	if p.activeCheckpointID != 0 {
		checkpoint := privatePagePoolCheckpoint{id: p.activeCheckpointID}
		if problem := p.requireCheckpointSlotMutation(checkpoint, slot, 1, 1); problem.failed() {
			return problem
		}
		p.remember(token.slot, checkpoint)
		if slot.checkpointState == privatePageInUse {
			slot.state = privatePagePendingReturn
			slot.inUse = false
			slot.pendingReturnState = state
			slot.epoch++
			p.refreshSlotIndexes(slot)
			p.advanceMutationPrepared()
			return privatePagePoolError{}
		}
		return p.releaseSlotMutationPrepared(token.slot, state, fences...)
	}
	return p.releasePrepared(token, slot, state, fences...)
}

func (p *privatePagePool) releaseInScope(
	scope privatePageReservationScope,
	token privatePageToken,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	if _, problem := p.validateScope(scope); problem.failed() {
		return problem
	}
	if token.scopeID != scope.id || token.slot < 0 || token.slot >= len(p.slots) ||
		p.slots[token.slot].scopeAnchorIndex != scope.anchor {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.release(token, state, fences...)
}

func (p *privatePagePool) releaseSlotMutationPrepared(
	index int,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorSlotMutation(
		privateWriterFixedPointWorkActive, index, fences...,
	); problem.failed() {
		return problem
	}
	slot := &p.slots[index]
	clear(slot.bytes[:])
	slot.state, slot.inUse = state, false
	slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
	slot.pendingTxn = 0
	slot.generation = 0
	slot.committedOrigin = 0
	slot.pendingReturnState = 0
	slot.epoch++
	p.refreshSlotIndexes(slot)
	p.advanceMutationPrepared()
	return privatePagePoolError{}
}

func (p *privatePagePool) releasePrepared(
	token privatePageToken,
	slot *privatePagePoolSlot,
	state privatePageState,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, nil, fences...,
	); problem.failed() {
		return problem
	}
	nextEpoch := slot.epoch + 1
	if nextEpoch == 0 {
		return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
	}
	if problem := p.requireMutationSteps(1); problem.failed() {
		return problem
	}
	return p.releaseSlotMutationPrepared(token.slot, state, fences...)
}

func (p *privatePagePool) rollback(checkpoint privatePagePoolCheckpoint) privatePagePoolError {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	remembered, _, problem := p.checkpointSlotJournalStats(checkpoint, privatePageReservationScope{}, false)
	if problem.failed() {
		return problem
	}
	if problem = p.checkpointIndexJournalValid(checkpoint); problem.failed() {
		return problem
	}
	if problem = p.checkpointScopeJournalValid(checkpoint, privatePageReservationScope{}, false); problem.failed() {
		return problem
	}
	if problem = p.requireMutationSteps(uint64(remembered)); problem.failed() {
		return problem
	}
	p.rollbackCheckpointSlotsPrepared(checkpoint)
	p.rollbackCheckpointIndexesPrepared(checkpoint)
	p.rollbackCheckpointScopesPrepared(checkpoint)
	p.indexRoot = checkpoint.indexRoot
	p.pendingPageCount = checkpoint.pendingPageCount
	p.activeCheckpointID = 0
	return privatePagePoolError{}
}

func (p *privatePagePool) commit(checkpoint privatePagePoolCheckpoint) privatePagePoolError {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return problem
	}
	if problem := p.validateCheckpoint(checkpoint); problem.failed() {
		return problem
	}
	_, pendingReturns, problem := p.checkpointSlotJournalStats(checkpoint, privatePageReservationScope{}, false)
	if problem.failed() {
		return problem
	}
	if problem = p.checkpointIndexJournalValid(checkpoint); problem.failed() {
		return problem
	}
	if problem = p.checkpointScopeJournalValid(checkpoint, privatePageReservationScope{}, false); problem.failed() {
		return problem
	}
	if problem = p.requireMutationSteps(uint64(pendingReturns)); problem.failed() {
		return problem
	}
	p.commitCheckpointSlotsPrepared(checkpoint)
	p.clearCheckpointIndexesTerminalPrepared(checkpoint)
	p.clearCheckpointScopesTerminalPrepared(checkpoint)
	p.generation = checkpoint.generation
	p.activeCheckpointID = 0
	return privatePagePoolError{}
}

func (p *privatePagePool) releaseGeneration(generation uint64, owner privatePageOwner, origin privatePageOrigin) privatePagePoolError {
	if problem := p.rejectRawCoordinatorAccess(); problem.failed() {
		return problem
	}
	if p == nil || p.self != p || p.activeCheckpointID != 0 || p.activeOperationID != 0 {
		return privatePagePoolError{code: privatePagePoolErrCheckpointActive}
	}
	if p.activeScopes != 0 {
		return privatePagePoolError{code: privatePagePoolErrScopeMismatch}
	}
	return p.releaseGenerationMatching(0, generation, owner, origin)
}

func (p *privatePagePool) releaseGenerationInScope(
	scope privatePageReservationScope,
	generation uint64,
	owner privatePageOwner,
	origin privatePageOrigin,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	var checkpoint *privatePagePoolCheckpoint
	if p.coordinatorSessionID == 0 {
		if p.activeCheckpointID != 0 || p.activeOperationID != 0 {
			return privatePagePoolError{code: privatePagePoolErrCheckpointActive}
		}
	} else {
		fence := fences[0]
		commitment := fence.slot.terminalCommitment
		switch commitment.phase {
		case privateWriterPreparedTerminalOperationActive:
			operation := commitment.operation
			if _, problem := p.authorizeCoordinatorTerminalOperation(
				operation, privateWriterPreparedTerminalOperationActive, fence,
			); problem.failed() {
				return problem
			}
			if generation != operation.generation {
				return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
			}
		case privateWriterPreparedTerminalCheckpointActive:
			canonicalCheckpoint := commitment.checkpoint
			if _, problem := p.authorizeCoordinatorTerminalCheckpoint(
				canonicalCheckpoint, &scope,
				privateWriterPreparedTerminalCheckpointActive, fence,
			); problem.failed() {
				return problem
			}
			if generation != canonicalCheckpoint.generation {
				return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
			}
			checkpoint = &canonicalCheckpoint
		default:
			return privatePagePoolError{code: privatePagePoolErrCoordinatorRequired}
		}
	}
	anchor, problem := p.validateScope(scope)
	if problem.failed() {
		return problem
	}
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	steps, problem := p.preflightReleaseGenerationInScopeNode(
		scope, anchor.scopeRoot, generation, owner, origin,
	)
	if problem.failed() {
		return problem
	}
	if problem = p.requireMutationSteps(steps); problem.failed() {
		return problem
	}
	return p.releaseGenerationInScopeNode(
		scope, anchor.scopeRoot, generation, owner, origin, checkpoint, fences...,
	)
}

func (p *privatePagePool) preflightReleaseGenerationInScopeNode(
	scope privatePageReservationScope,
	root int,
	generation uint64,
	owner privatePageOwner,
	origin privatePageOrigin,
) (uint64, privatePagePoolError) {
	if root == privatePagePoolNoIndex {
		return 0, privatePagePoolError{}
	}
	slot := &p.slots[root]
	if !slot.bound || slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor {
		return 0, privatePagePoolError{code: privatePagePoolErrScopeMismatch, page: slot.pageNumber}
	}
	left, problem := p.preflightReleaseGenerationInScopeNode(scope, slot.scopeLeft, generation, owner, origin)
	if problem.failed() {
		return 0, problem
	}
	right, problem := p.preflightReleaseGenerationInScopeNode(scope, slot.scopeRight, generation, owner, origin)
	if problem.failed() || left > ^uint64(0)-right {
		if problem.failed() {
			return 0, problem
		}
		return 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow}
	}
	steps := left + right
	if slot.state == privatePageInUse && slot.generation == generation && slot.owner == owner && slot.origin == origin {
		if slot.epoch == ^uint64(0) || steps == ^uint64(0) {
			return 0, privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
		}
		steps++
	}
	return steps, privatePagePoolError{}
}

func (p *privatePagePool) releaseGenerationInScopeNode(
	scope privatePageReservationScope,
	root int,
	generation uint64,
	owner privatePageOwner,
	origin privatePageOrigin,
	checkpoint *privatePagePoolCheckpoint,
	fences ...*privateWriterWorkFence,
) privatePagePoolError {
	if problem := p.authorizeCoordinatorMutation(
		privateWriterFixedPointWorkActive, &scope, fences...,
	); problem.failed() {
		return problem
	}
	if root == privatePagePoolNoIndex {
		return privatePagePoolError{}
	}
	slot := &p.slots[root]
	left, right := slot.scopeLeft, slot.scopeRight
	if problem := p.releaseGenerationInScopeNode(
		scope, left, generation, owner, origin, checkpoint, fences...,
	); problem.failed() {
		return problem
	}
	if problem := p.releaseGenerationInScopeNode(
		scope, right, generation, owner, origin, checkpoint, fences...,
	); problem.failed() {
		return problem
	}
	if slot.state != privatePageInUse || slot.generation != generation || slot.owner != owner || slot.origin != origin {
		return privatePagePoolError{}
	}
	if checkpoint != nil {
		return p.releaseSlotForCheckpointInScopePrepared(
			*checkpoint, scope, root, privatePageAvailable, fences...,
		)
	}
	return p.releaseSlotMutationPrepared(root, privatePageAvailable, fences...)
}

func (p *privatePagePool) releaseGenerationMatching(
	scopeID, generation uint64,
	owner privatePageOwner,
	origin privatePageOrigin,
) privatePagePoolError {
	if !validPrivatePageOwnerOrigin(owner, origin) {
		return privatePagePoolError{code: privatePagePoolErrInvalidState}
	}
	// Preflight every epoch before the first destructive change.
	steps := uint64(0)
	for index := range p.slots {
		slot := &p.slots[index]
		if slot.scopeID == scopeID && slot.state == privatePageInUse && slot.generation == generation && slot.owner == owner && slot.origin == origin {
			if slot.epoch == ^uint64(0) {
				return privatePagePoolError{code: privatePagePoolErrArithmeticOverflow, page: slot.pageNumber}
			}
			steps++
		}
	}
	if problem := p.requireMutationSteps(steps); problem.failed() {
		return problem
	}
	for index := range p.slots {
		slot := &p.slots[index]
		if slot.scopeID != scopeID || slot.state != privatePageInUse || slot.generation != generation || slot.owner != owner || slot.origin != origin {
			continue
		}
		clear(slot.bytes[:])
		slot.state, slot.inUse = privatePageAvailable, false
		slot.owner, slot.origin = privatePageOwnerNone, privatePageOriginNone
		slot.pendingTxn = 0
		slot.generation = 0
		slot.committedOrigin = 0
		slot.epoch++
		p.advanceMutationPrepared()
	}
	p.rebuildAllIndexCounts()
	return privatePagePoolError{}
}

// Compatibility names used by the accepted bitmap writer. Their values map
// onto the exact pool authority and state contract.
type privateBitmapPageState = privatePageState

const (
	privateBitmapPageAvailable    = privatePageAvailable
	privateBitmapPageInUse        = privatePageInUse
	privateBitmapPageReleasedFree = privatePageReleasedFree
	privateBitmapPageReleasedTail = privatePageReleasedTail
)

type privateBitmapPageAuthorization = privatePageAuthorization

const (
	privateBitmapPageUnassigned             = privatePageAuthorizationNone
	privateBitmapPageCommittedFreeCandidate = privatePageCommittedFree
	privateBitmapPageAppended               = privatePageAppended
)

type reservedBitmapPage = privatePagePoolSlot
type privatePageSlot = privatePagePoolSlot

func (p *privatePagePoolSlot) authorize(pageNumber uint32, authorization privateBitmapPageAuthorization) {
	*p = privatePagePoolSlot{pageNumber: pageNumber, authorization: authorization}
}
