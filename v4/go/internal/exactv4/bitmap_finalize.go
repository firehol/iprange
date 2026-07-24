package exactv4

import "sync/atomic"

// freeBitmapFinalizationScratch is the complete caller-owned working set for
// finalization. It may be reused after either success or failure.
type freeBitmapFinalizationScratch struct {
	releasePages []uint32
	insertPages  []freeBitmapInsertPage
	cachedPages  []freeBitmapFinalizationCachedPage
	indexStack   []int
	cache        *freeBitmapFinalizationCachedSource
	cleanup      freeBitmapCleanupScratch
}

type freeBitmapCleanupScratch struct {
	nodes   []freeBitmapCleanupOverlayNode
	path    []int
	targets []int
}

type freeBitmapCleanupOverlayNode struct {
	slot int
	tree privatePageDeleteTree

	left   int
	right  int
	height int8
	free   uint64
	inUse  uint64

	selfFree    uint64
	selfInUse   uint64
	dirty       bool
	pathOrdinal int
	successor   bool
}

type freeBitmapCleanupScratchSeal struct {
	nodes   freeBitmapReservationSliceSeal[freeBitmapCleanupOverlayNode]
	path    freeBitmapReservationSliceSeal[int]
	targets freeBitmapReservationSliceSeal[int]
}

func sealFreeBitmapCleanupScratch(scratch freeBitmapCleanupScratch) freeBitmapCleanupScratchSeal {
	return freeBitmapCleanupScratchSeal{
		nodes:   sealFreeBitmapReservationSlice(scratch.nodes),
		path:    sealFreeBitmapReservationSlice(scratch.path),
		targets: sealFreeBitmapReservationSlice(scratch.targets),
	}
}

func (seal freeBitmapCleanupScratchSeal) matches(scratch freeBitmapCleanupScratch) bool {
	return seal.nodes.matches(scratch.nodes) && seal.path.matches(scratch.path) &&
		seal.targets.matches(scratch.targets)
}

func (scratch freeBitmapCleanupScratch) clear() {
	clear(scratch.nodes)
	clear(scratch.path)
	clear(scratch.targets)
}

type freeBitmapFinalizationCachedPage struct {
	pageNumber   uint32
	bytes        [PageSize]byte
	contentSeal  uint64
	metadataSeal uint64
	left         int
	right        int
	height       uint8
}

type freeBitmapFinalizationCachedSource struct {
	base       committedPageSource
	cow        *freeBitmapCOW
	pages      []freeBitmapFinalizationCachedPage
	length     int
	root       int
	stack      []int
	sealKey    uint64
	sealed     bool
	problem    freeBitmapCOWError
	nodeVisits uint64
	failure    freeBitmapFinalizationFailureFence
}

type freeBitmapFinalizationCacheControlSeal struct {
	base       committedPageSource
	cow        *freeBitmapCOW
	pages      freeBitmapReservationSliceSeal[freeBitmapFinalizationCachedPage]
	length     int
	root       int
	stack      freeBitmapReservationSliceSeal[int]
	sealKey    uint64
	sealed     bool
	problem    freeBitmapCOWError
	nodeVisits uint64
	failure    freeBitmapFinalizationFailureFence
}

type freeBitmapFinalizationFailureFence struct {
	armed            bool
	cow              *freeBitmapCOW
	cowSeal          freeBitmapReservationCOWSeal
	cowFingerprint   uint64
	poolSeal         freeBitmapReservationPoolSeal
	scopeFingerprint uint64
}

type sealedFreeBitmapOutput struct {
	committed          committedPageSource
	selectedTxn        uint64
	pendingTxn         uint64
	committedPageCount uint64
	pageCount          uint64
	root               uint32
	pool               *privatePagePool
	scope              privatePageReservationScope
	bindings           []bitmapCOWArenaBinding
	boundLen           int
	indexNodes         []bitmapCOWIndexNode
	indexRoot          int
	cleanupScratch     freeBitmapCleanupScratch
	cleanupScratchSeal freeBitmapCleanupScratchSeal
}

type freeBitmapFinalizationPredecessor struct {
	output sealedFreeBitmapOutput
	nonce  uint64
}

// freeBitmapFinalizationSuccessorSeed is intentionally opaque and copy-safe:
// the single-use state lives in the sealed scope, not in this Go value.
type freeBitmapFinalizationSuccessorSeed struct {
	output sealedFreeBitmapOutput
	nonce  uint64
}

type freeBitmapFinalizationResult struct {
	output              sealedFreeBitmapOutput
	successor           freeBitmapFinalizationSuccessorSeed
	released            unusedReservationRelease
	reinsertedReclaimed int
}

type freeBitmapFinalizationRelease struct {
	unusedReservationRelease
	reinsertedReclaimed  int
	releasedFreeBindings int
}

type freeBitmapFinalizationLiveSeal struct {
	attachment            *freeBitmapReservationAttachment
	privatePages          int
	payloadPages          int
	verifiedLen           int
	indexRequired         int
	scope                 privatePageReservationScope
	poolGeneration        uint64
	poolMutationEpoch     uint64
	cow                   freeBitmapReservationCOWSeal
	pool                  freeBitmapReservationPoolSeal
	buffers               freeBitmapReservationBufferSeal
	scopeFingerprint      uint64
	cowContentFingerprint uint64
}

type preparedFreeBitmapFinalization struct {
	checkpoint    privatePagePoolCheckpoint
	shadow        *freeBitmapCOW
	newTail       uint64
	boundLen      int
	tailTargetLen int
	nonce         uint64
	released      freeBitmapFinalizationRelease
	deletes       preparedPrivatePageDeletes
}

type preparedSealedFreeBitmapCleanup struct {
	checkpoint privatePagePoolCheckpoint
	output     sealedFreeBitmapOutput
	deletes    preparedPrivatePageDeletes
}

type freeBitmapFinalizationScratchRequirements struct {
	releasePages int
	insertPages  int
	cachedPages  int
	indexStack   int
	cleanupNodes int
	cleanupPath  int
	cleanupSlots int
}

type privatePageDeleteTree uint8

const (
	privatePageDeleteGlobal privatePageDeleteTree = iota + 1
	privatePageDeleteScope
)

type privatePageDeleteNodeState struct {
	left   int
	right  int
	height int
	free   uint64
	inUse  uint64
}

type privatePageDeleteOverlay struct {
	pool          *privatePagePool
	scope         privatePageReservationScope
	scratch       freeBitmapCleanupScratch
	nodeLen       int
	indexRoot     int
	scopeRoot     int
	work          uint64
	workLimit     uint64
	rotations     uint64
	successors    uint64
	deleteOrdinal int
}

type preparedPrivatePageDeletes struct {
	scratch    freeBitmapCleanupScratch
	nodeLen    int
	targetLen  int
	indexRoot  int
	scopeRoot  int
	work       uint64
	workLimit  uint64
	rotations  uint64
	successors uint64
}

func maximumPrivatePageAVLHeight(nodes int) int {
	if nodes <= 0 {
		return 0
	}
	// Minimum nodes at heights 0 and 1. The recurrence is the exact AVL
	// minimum, so the last value not exceeding nodes is the maximum valid
	// height for this pool.
	lower, current, height := 0, 1, 1
	for {
		if current > int(^uint(0)>>1)-lower-1 {
			return height
		}
		next := current + lower + 1
		if next > nodes {
			return height
		}
		lower, current = current, next
		height++
	}
}

func privatePageDeleteScratchRequirements(
	poolSlots, targets, refreshPages int,
) (nodes, path int, ok bool) {
	if poolSlots < 0 || targets < 0 || targets > poolSlots ||
		refreshPages < 0 || refreshPages > poolSlots {
		return 0, 0, false
	}
	if targets == 0 && refreshPages == 0 {
		return 0, 0, true
	}
	height := maximumPrivatePageAVLHeight(poolSlots)
	maxInt := int(^uint(0) >> 1)
	// In one AVL, delete plus detach-min follows one root-to-leaf chain of at
	// most height nodes. Rebalancing each chain node can additionally copy only
	// its heavy child and that child's inner child, so one tree touches at most
	// 3*height slots. The global and exact-scope trees therefore touch at most
	// 6*height slots per target. Sequential deletes can only reuse those slots.
	if height > maxInt/6 {
		return 0, 0, false
	}
	perTarget := 6 * height
	if targets > maxInt/perTarget {
		return 0, 0, false
	}
	nodes = targets * perTarget
	// A retained refresh materializes at most one root-to-page path in each
	// tree. The per-tree overlay namespaces are separate, so this contributes
	// at most 2*height nodes per retained page before the same 2N cap.
	if height > maxInt/2 {
		return 0, 0, false
	}
	perRefresh := 2 * height
	if refreshPages > maxInt/perRefresh ||
		refreshPages*perRefresh > maxInt-nodes {
		return 0, 0, false
	}
	nodes += refreshPages * perRefresh
	// Global and exact-scope trees have separate overlay namespaces. The same
	// pool slot may therefore have one copy in each tree, but never more.
	if poolSlots <= maxInt/2 && nodes > poolSlots*2 {
		nodes = poolSlots * 2
	}
	path = height
	return nodes, path, true
}

func privatePageDeleteWorkLimit(poolSlots, targets, refreshPages int) (uint64, bool) {
	if poolSlots < 0 || targets < 0 || targets > poolSlots ||
		refreshPages < 0 || refreshPages > poolSlots {
		return 0, false
	}
	height := uint64(maximumPrivatePageAVLHeight(poolSlots))
	const referenceWorkPerTargetHeight = uint64(192)
	// Final checked normalization always consumes the two prepared roots.
	// A no-change plan may additionally preserve an ordinary root, whose two
	// immediate child references are proved locally in each tree.
	work := uint64(6)
	if targets != 0 {
		if height > ^uint64(0)/referenceWorkPerTargetHeight {
			return 0, false
		}
		perTarget := height * referenceWorkPerTargetHeight
		if uint64(targets) > (^uint64(0)-work)/perTarget {
			return 0, false
		}
		// One tree level costs fewer than 64 resolutions: at most 12 for the
		// delete/detach visit and 52 for refresh, heavy-child validation, and the
		// worst double rotation. Both trees therefore cost less than 128*height.
		// Final normalization of at most 6*height overlay nodes costs
		// 12*height. 192*height per target is a checked conservative bound.
		work += uint64(targets) * perTarget
	}
	// A retained-page refresh prepares one path in each final tree. Per level,
	// top-down materialization and local validation resolve the current node and
	// both children; bottom-up cache preparation resolves the current node and
	// both children again. Final normalization adds two references per
	// materialized node, for at most another 4*height. That is 16*height
	// charged references per retained page.
	const referenceWorkPerRefreshHeight = uint64(16)
	if height > ^uint64(0)/referenceWorkPerRefreshHeight {
		return 0, false
	}
	perRefresh := height * referenceWorkPerRefreshHeight
	if refreshPages != 0 &&
		(uint64(refreshPages) > ^uint64(0)/perRefresh ||
			uint64(refreshPages)*perRefresh > ^uint64(0)-work) {
		return 0, false
	}
	return work + uint64(refreshPages)*perRefresh, true
}

func freeBitmapCleanupScratchCanonical(scratch freeBitmapCleanupScratch) bool {
	for index := range scratch.nodes {
		if scratch.nodes[index] != (freeBitmapCleanupOverlayNode{}) {
			return false
		}
	}
	for _, values := range [2][]int{scratch.path, scratch.targets} {
		for _, value := range values {
			if value != 0 {
				return false
			}
		}
	}
	return true
}

func validateFreeBitmapCleanupScratchAliases(
	scratch freeBitmapCleanupScratch,
	indexStack []int,
) bool {
	values := [3][]int{scratch.path, scratch.targets, indexStack}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if reservationSlicesOverlap(values[left], values[right]) {
				return false
			}
		}
	}
	return true
}

func privatePageDeleteOverlayReference(nodeIndex int) int {
	return -(nodeIndex + 2)
}

func privatePageDeleteOverlayIndex(reference int) (int, bool) {
	if reference > -2 {
		return 0, false
	}
	return -(reference + 2), true
}

// resolve is the only reference lookup. Original pool slots are nonnegative,
// nil is -1, and overlay nodes are <= -2. Every lookup is constant time and is
// charged to the checked work budget.
func (overlay *privatePageDeleteOverlay) resolve(
	tree privatePageDeleteTree,
	reference int,
) (int, *freeBitmapCleanupOverlayNode, bool) {
	if overlay.work >= overlay.workLimit {
		return 0, nil, false
	}
	overlay.work++
	if reference == privatePagePoolNoIndex {
		return privatePagePoolNoIndex, nil, true
	}
	if reference >= 0 {
		if reference >= len(overlay.pool.slots) {
			return 0, nil, false
		}
		return reference, nil, true
	}
	nodeIndex, ok := privatePageDeleteOverlayIndex(reference)
	if !ok || nodeIndex < 0 || nodeIndex >= overlay.nodeLen {
		return 0, nil, false
	}
	node := &overlay.scratch.nodes[nodeIndex]
	if node.tree != tree || node.slot < 0 || node.slot >= len(overlay.pool.slots) {
		return 0, nil, false
	}
	return node.slot, node, true
}

// materialize copies an original node once in this tree. A valid strict BST has
// one reachable reference for each key. Every recursive caller immediately
// replaces that reference with the returned overlay reference, while rotations
// and detach-min only move existing references. The same original therefore
// cannot be copied twice or remain ambiguously reachable within one evolving
// tree. Global and scope trees intentionally get separate tagged copies.
func (overlay *privatePageDeleteOverlay) materialize(
	tree privatePageDeleteTree,
	reference int,
) (int, *freeBitmapCleanupOverlayNode, bool) {
	slot, node, ok := overlay.resolve(tree, reference)
	if !ok || slot == privatePagePoolNoIndex {
		return 0, nil, false
	}
	if node != nil {
		return reference, node, true
	}
	if overlay.nodeLen >= len(overlay.scratch.nodes) ||
		overlay.nodeLen > int(^uint(0)>>1)-2 {
		return 0, nil, false
	}
	live := &overlay.pool.slots[slot]
	selfFree, selfInUse, valid := privatePageDeleteStateCounts(live)
	if !valid {
		return 0, nil, false
	}
	nodeIndex := overlay.nodeLen
	overlay.nodeLen++
	node = &overlay.scratch.nodes[nodeIndex]
	*node = freeBitmapCleanupOverlayNode{
		slot: slot, tree: tree, selfFree: selfFree, selfInUse: selfInUse,
	}
	if tree == privatePageDeleteGlobal {
		node.left, node.right = live.indexLeft, live.indexRight
		node.height, node.free, node.inUse = live.indexHeight, live.indexFree, live.indexInUse
	} else {
		node.left, node.right = live.scopeLeft, live.scopeRight
		node.height, node.free, node.inUse = live.scopeHeight, live.scopeFree, live.scopeInUse
	}
	return privatePageDeleteOverlayReference(nodeIndex), node, true
}

func (overlay *privatePageDeleteOverlay) state(
	tree privatePageDeleteTree,
	reference int,
) (privatePageDeleteNodeState, bool) {
	slot, node, ok := overlay.resolve(tree, reference)
	if !ok {
		return privatePageDeleteNodeState{}, false
	}
	if slot == privatePagePoolNoIndex {
		return privatePageDeleteNodeState{}, true
	}
	if node != nil {
		return privatePageDeleteNodeState{
			left: node.left, right: node.right, height: int(node.height),
			free: node.free, inUse: node.inUse,
		}, true
	}
	live := &overlay.pool.slots[slot]
	if tree == privatePageDeleteGlobal {
		return privatePageDeleteNodeState{
			left: live.indexLeft, right: live.indexRight, height: int(live.indexHeight),
			free: live.indexFree, inUse: live.indexInUse,
		}, true
	}
	return privatePageDeleteNodeState{
		left: live.scopeLeft, right: live.scopeRight, height: int(live.scopeHeight),
		free: live.scopeFree, inUse: live.scopeInUse,
	}, true
}

func (overlay *privatePageDeleteOverlay) setState(
	tree privatePageDeleteTree,
	reference int,
	state privatePageDeleteNodeState,
) bool {
	if state.height < 0 || state.height > 127 {
		return false
	}
	_, node, ok := overlay.resolve(tree, reference)
	if !ok || node == nil {
		return false
	}
	node.left, node.right = state.left, state.right
	node.height, node.free, node.inUse = int8(state.height), state.free, state.inUse
	node.dirty = true
	return true
}

func (overlay *privatePageDeleteOverlay) markPath(
	tree privatePageDeleteTree,
	reference int,
	successor bool,
) bool {
	_, node, ok := overlay.resolve(tree, reference)
	if !ok || node == nil {
		return false
	}
	if node.pathOrdinal == 0 {
		node.pathOrdinal = overlay.deleteOrdinal + 1
	}
	node.successor = node.successor || successor
	return true
}

func privatePageDeleteStateCounts(slot *privatePagePoolSlot) (uint64, uint64, bool) {
	if slot.inUse != (slot.state == privatePageInUse) {
		return 0, 0, false
	}
	switch slot.state {
	case privatePageAvailable:
		return 1, 0, true
	case privatePageInUse:
		return 0, 1, true
	case privatePagePendingReturn, privatePageReleasedFree, privatePageReleasedTail:
		return 0, 0, true
	default:
		return 0, 0, false
	}
}

func (overlay *privatePageDeleteOverlay) validateLocal(
	tree privatePageDeleteTree,
	reference int,
	lower, upper uint64,
) (privatePageDeleteNodeState, freeBitmapCOWError) {
	slot, _, ok := overlay.resolve(tree, reference)
	if !ok || slot == privatePagePoolNoIndex {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	live := &overlay.pool.slots[slot]
	page := uint64(live.pageNumber)
	if !live.bound || page < 2 || page <= lower || page >= upper ||
		(tree == privatePageDeleteScope &&
			(live.scopeID != overlay.scope.id || live.scopeAnchorIndex != overlay.scope.anchor ||
				live.scopeAnchor != (slot == overlay.scope.anchor))) {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
		}
	}
	state, ok := overlay.state(tree, reference)
	if !ok || state.height <= 0 {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
		}
	}
	children := [2]struct {
		slot         int
		lower, upper uint64
	}{
		{state.left, lower, page},
		{state.right, page, upper},
	}
	var childState [2]privatePageDeleteNodeState
	for index, child := range children {
		if child.slot == privatePagePoolNoIndex {
			continue
		}
		childSlot, _, resolved := overlay.resolve(tree, child.slot)
		if !resolved || childSlot == privatePagePoolNoIndex || childSlot == slot {
			return privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
			}
		}
		childLive := &overlay.pool.slots[childSlot]
		childPage := uint64(childLive.pageNumber)
		if !childLive.bound || childPage <= child.lower || childPage >= child.upper ||
			(tree == privatePageDeleteScope &&
				(childLive.scopeID != overlay.scope.id || childLive.scopeAnchorIndex != overlay.scope.anchor)) {
			return privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: childLive.pageNumber,
			}
		}
		childState[index], ok = overlay.state(tree, child.slot)
		if !ok || childState[index].height <= 0 {
			return privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: childLive.pageNumber,
			}
		}
	}
	leftHeight, rightHeight := childState[0].height, childState[1].height
	height := leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	if height > 127 || leftHeight-rightHeight > 1 || rightHeight-leftHeight > 1 {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
		}
	}
	selfFree, selfInUse, ok := privatePageDeleteStateCounts(live)
	if !ok || childState[0].free > ^uint64(0)-childState[1].free ||
		childState[0].free+childState[1].free > ^uint64(0)-selfFree ||
		childState[0].inUse > ^uint64(0)-childState[1].inUse ||
		childState[0].inUse+childState[1].inUse > ^uint64(0)-selfInUse {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
		}
	}
	free := childState[0].free + childState[1].free + selfFree
	inUse := childState[0].inUse + childState[1].inUse + selfInUse
	if state.height != height || state.free != free || state.inUse != inUse ||
		free > uint64(len(overlay.pool.slots)) || inUse > uint64(len(overlay.pool.slots))-free {
		return privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
		}
	}
	return state, freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) refresh(
	tree privatePageDeleteTree,
	reference int,
) freeBitmapCOWError {
	state, ok := overlay.state(tree, reference)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	left, ok := overlay.state(tree, state.left)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	right, ok := overlay.state(tree, state.right)
	if !ok || left.height < 0 || right.height < 0 ||
		left.free > ^uint64(0)-right.free || left.inUse > ^uint64(0)-right.inUse {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	slot, node, ok := overlay.resolve(tree, reference)
	if !ok || slot == privatePagePoolNoIndex || node == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if left.free+right.free > ^uint64(0)-node.selfFree ||
		left.inUse+right.inUse > ^uint64(0)-node.selfInUse {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	state.height = left.height
	if right.height > state.height {
		state.height = right.height
	}
	state.height++
	state.free = left.free + right.free + node.selfFree
	state.inUse = left.inUse + right.inUse + node.selfInUse
	if state.height <= 0 || state.height > 127 ||
		state.free > uint64(len(overlay.pool.slots)) ||
		state.inUse > uint64(len(overlay.pool.slots))-state.free ||
		!overlay.setState(tree, reference, state) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) rotateRight(
	tree privatePageDeleteTree,
	root int,
) (int, freeBitmapCOWError) {
	overlay.rotations++
	var ok bool
	root, _, ok = overlay.materialize(tree, root)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootState, ok := overlay.state(tree, root)
	if !ok || rootState.left == privatePagePoolNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	left, _, ok := overlay.materialize(tree, rootState.left)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	leftState, ok := overlay.state(tree, left)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootState.left = leftState.right
	leftState.right = root
	if !overlay.setState(tree, root, rootState) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if !overlay.setState(tree, left, leftState) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if problem := overlay.refresh(tree, root); problem.failed() {
		return 0, problem
	}
	if problem := overlay.refresh(tree, left); problem.failed() {
		return 0, problem
	}
	return left, freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) rotateLeft(
	tree privatePageDeleteTree,
	root int,
) (int, freeBitmapCOWError) {
	overlay.rotations++
	var ok bool
	root, _, ok = overlay.materialize(tree, root)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootState, ok := overlay.state(tree, root)
	if !ok || rootState.right == privatePagePoolNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	right, _, ok := overlay.materialize(tree, rootState.right)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rightState, ok := overlay.state(tree, right)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootState.right = rightState.left
	rightState.left = root
	if !overlay.setState(tree, root, rootState) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if !overlay.setState(tree, right, rightState) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if problem := overlay.refresh(tree, root); problem.failed() {
		return 0, problem
	}
	if problem := overlay.refresh(tree, right); problem.failed() {
		return 0, problem
	}
	return right, freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) rebalance(
	tree privatePageDeleteTree,
	root int,
	lower, upper uint64,
) (int, freeBitmapCOWError) {
	if problem := overlay.refresh(tree, root); problem.failed() {
		return 0, problem
	}
	state, ok := overlay.state(tree, root)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	left, ok := overlay.state(tree, state.left)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	right, ok := overlay.state(tree, state.right)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	balance := left.height - right.height
	if balance > 2 || balance < -2 {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootSlot, _, ok := overlay.resolve(tree, root)
	if !ok || rootSlot == privatePagePoolNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	page := uint64(overlay.pool.slots[rootSlot].pageNumber)
	if balance > 1 {
		leftState, problem := overlay.validateLocal(tree, state.left, lower, page)
		if problem.failed() {
			return 0, problem
		}
		leftLeft, ok := overlay.state(tree, leftState.left)
		if !ok {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		leftRight, ok := overlay.state(tree, leftState.right)
		if !ok {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		if leftRight.height > leftLeft.height {
			rotated, problem := overlay.rotateLeft(tree, state.left)
			if problem.failed() {
				return 0, problem
			}
			state.left = rotated
			if !overlay.setState(tree, root, state) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
		}
		return overlay.rotateRight(tree, root)
	}
	if balance < -1 {
		rightState, problem := overlay.validateLocal(tree, state.right, page, upper)
		if problem.failed() {
			return 0, problem
		}
		rightLeft, ok := overlay.state(tree, rightState.left)
		if !ok {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		rightRight, ok := overlay.state(tree, rightState.right)
		if !ok {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		if rightLeft.height > rightRight.height {
			rotated, problem := overlay.rotateRight(tree, state.right)
			if problem.failed() {
				return 0, problem
			}
			state.right = rotated
			if !overlay.setState(tree, root, state) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
		}
		return overlay.rotateLeft(tree, root)
	}
	return root, freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) detachMinimum(
	tree privatePageDeleteTree,
	root int,
	lower, upper uint64,
	depth int,
) (int, int, freeBitmapCOWError) {
	if depth < 0 || depth >= len(overlay.scratch.path) {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	var ok bool
	root, _, ok = overlay.materialize(tree, root)
	if !ok {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	overlay.scratch.path[depth] = root
	if !overlay.markPath(tree, root, true) {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	state, problem := overlay.validateLocal(tree, root, lower, upper)
	if problem.failed() {
		return 0, 0, problem
	}
	if state.left == privatePagePoolNoIndex {
		return state.right, root, freeBitmapCOWError{}
	}
	rootSlot, _, ok := overlay.resolve(tree, root)
	if !ok || rootSlot == privatePagePoolNoIndex {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	page := uint64(overlay.pool.slots[rootSlot].pageNumber)
	newLeft, minimum, problem := overlay.detachMinimum(tree, state.left, lower, page, depth+1)
	if problem.failed() {
		return 0, 0, problem
	}
	state.left = newLeft
	if !overlay.setState(tree, root, state) {
		return 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	root, problem = overlay.rebalance(tree, root, lower, upper)
	return root, minimum, problem
}

func (overlay *privatePageDeleteOverlay) delete(
	tree privatePageDeleteTree,
	root, target int,
	lower, upper uint64,
	depth int,
) (int, freeBitmapCOWError) {
	if depth < 0 || depth >= len(overlay.scratch.path) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	var ok bool
	root, _, ok = overlay.materialize(tree, root)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	overlay.scratch.path[depth] = root
	if !overlay.markPath(tree, root, false) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	state, problem := overlay.validateLocal(tree, root, lower, upper)
	if problem.failed() {
		return 0, problem
	}
	targetSlot, _, ok := overlay.resolve(tree, target)
	if !ok || targetSlot == privatePagePoolNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	rootSlot, _, ok := overlay.resolve(tree, root)
	if !ok || rootSlot == privatePagePoolNoIndex {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	targetPage := overlay.pool.slots[targetSlot].pageNumber
	rootPage := overlay.pool.slots[rootSlot].pageNumber
	if targetPage < rootPage {
		newLeft, problem := overlay.delete(tree, state.left, target, lower, uint64(rootPage), depth+1)
		if problem.failed() {
			return 0, problem
		}
		state.left = newLeft
		if !overlay.setState(tree, root, state) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		return overlay.rebalance(tree, root, lower, upper)
	}
	if targetPage > rootPage {
		newRight, problem := overlay.delete(tree, state.right, target, uint64(rootPage), upper, depth+1)
		if problem.failed() {
			return 0, problem
		}
		state.right = newRight
		if !overlay.setState(tree, root, state) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		return overlay.rebalance(tree, root, lower, upper)
	}
	if rootSlot != targetSlot {
		return 0, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: targetPage,
		}
	}
	if state.left == privatePagePoolNoIndex {
		return state.right, freeBitmapCOWError{}
	}
	if state.right == privatePagePoolNoIndex {
		return state.left, freeBitmapCOWError{}
	}
	right, successor, problem := overlay.detachMinimum(
		tree, state.right, uint64(rootPage), upper, depth+1,
	)
	if problem.failed() {
		return 0, problem
	}
	overlay.successors++
	successorState, ok := overlay.state(tree, successor)
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	successorState.left, successorState.right = state.left, right
	if !overlay.setState(tree, successor, successorState) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return overlay.rebalance(tree, successor, lower, upper)
}

func preparePrivatePageDeletes(
	pool *privatePagePool,
	scope privatePageReservationScope,
	scratch freeBitmapCleanupScratch,
	targetLen int,
	refreshPages int,
) (preparedPrivatePageDeletes, freeBitmapCOWError) {
	workLimit, ok := privatePageDeleteWorkLimit(len(pool.slots), targetLen, refreshPages)
	if !ok {
		return preparedPrivatePageDeletes{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	if targetLen == 0 {
		return preparedPrivatePageDeletes{
			scratch: scratch, indexRoot: pool.indexRoot,
			scopeRoot: pool.slots[scope.anchor].scopeRoot, workLimit: workLimit,
		}, freeBitmapCOWError{}
	}
	overlay := privatePageDeleteOverlay{
		pool: pool, scope: scope, scratch: scratch,
		indexRoot: pool.indexRoot, scopeRoot: pool.slots[scope.anchor].scopeRoot,
		workLimit: workLimit,
	}
	upper := uint64(1) << 32
	for targetIndex := 0; targetIndex < targetLen; targetIndex++ {
		overlay.deleteOrdinal = targetIndex
		target := scratch.targets[targetIndex]
		if target < 0 || target >= len(pool.slots) {
			return preparedPrivatePageDeletes{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		var problem freeBitmapCOWError
		overlay.indexRoot, problem = overlay.delete(
			privatePageDeleteGlobal, overlay.indexRoot, target, 0, upper, 0,
		)
		if problem.failed() {
			return preparedPrivatePageDeletes{}, problem
		}
		overlay.scopeRoot, problem = overlay.delete(
			privatePageDeleteScope, overlay.scopeRoot, target, 0, upper, 0,
		)
		if problem.failed() {
			return preparedPrivatePageDeletes{}, problem
		}
	}
	return preparedPrivatePageDeletes{
		scratch: scratch, nodeLen: overlay.nodeLen, targetLen: targetLen,
		indexRoot: overlay.indexRoot, scopeRoot: overlay.scopeRoot, work: overlay.work,
		workLimit: overlay.workLimit, rotations: overlay.rotations, successors: overlay.successors,
	}, freeBitmapCOWError{}
}

func privatePageCheckpointTagsCanonical(slot *privatePagePoolSlot) bool {
	return slot != nil &&
		slot.checkpointID == 0 && slot.checkpointSlotNext == privatePagePoolNoIndex &&
		slot.indexCheckpointID == 0 && slot.indexCheckpointNext == privatePagePoolNoIndex &&
		slot.scopeCheckpointID == 0 && slot.scopeCheckpointNext == privatePagePoolNoIndex
}

func privatePageDeleteResolvedState(
	pool *privatePagePool,
	tree privatePageDeleteTree,
	slotIndex int,
	node *freeBitmapCleanupOverlayNode,
) privatePageDeleteNodeState {
	if slotIndex == privatePagePoolNoIndex {
		return privatePageDeleteNodeState{}
	}
	if node != nil {
		return privatePageDeleteNodeState{
			left: node.left, right: node.right, height: int(node.height),
			free: node.free, inUse: node.inUse,
		}
	}
	slot := &pool.slots[slotIndex]
	if tree == privatePageDeleteGlobal {
		return privatePageDeleteNodeState{
			left: slot.indexLeft, right: slot.indexRight, height: int(slot.indexHeight),
			free: slot.indexFree, inUse: slot.indexInUse,
		}
	}
	return privatePageDeleteNodeState{
		left: slot.scopeLeft, right: slot.scopeRight, height: int(slot.scopeHeight),
		free: slot.scopeFree, inUse: slot.scopeInUse,
	}
}

func privatePageDeleteSummaryValid(
	state privatePageDeleteNodeState,
	poolSlots int,
) bool {
	if state.height < 0 || state.height > 127 ||
		state.free > uint64(poolSlots) ||
		state.inUse > uint64(poolSlots)-state.free {
		return false
	}
	if state.height == 0 {
		return state.free == 0 && state.inUse == 0
	}
	return true
}

func (overlay *privatePageDeleteOverlay) prepareRefreshLocal(
	tree privatePageDeleteTree,
	reference int,
	lower, upper uint64,
) (int, *freeBitmapCleanupOverlayNode, privatePageDeleteNodeState, freeBitmapCOWError) {
	// Refresh preflight is deliberately local: it proves the path node and the
	// two child summaries that its cache equation reads. It does not recursively
	// validate an off-path subtree; a self-consistent corruption there remains
	// the explicit Validate operation's responsibility.
	reference, node, ok := overlay.materialize(tree, reference)
	if !ok || node == nil {
		return 0, nil, privatePageDeleteNodeState{},
			freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	slotIndex := node.slot
	slot := &overlay.pool.slots[slotIndex]
	page := uint64(slot.pageNumber)
	if !slot.bound || page < 2 || page <= lower || page >= upper ||
		(tree == privatePageDeleteScope &&
			(slot.scopeID != overlay.scope.id ||
				slot.scopeAnchorIndex != overlay.scope.anchor ||
				slot.scopeAnchor != (slotIndex == overlay.scope.anchor))) {
		return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	state := privatePageDeleteResolvedState(overlay.pool, tree, slotIndex, node)
	children := [2]struct {
		reference    int
		lower, upper uint64
	}{
		{state.left, lower, page},
		{state.right, page, upper},
	}
	var childStates [2]privatePageDeleteNodeState
	for childIndex, child := range children {
		childSlot, childNode, resolved := overlay.resolve(tree, child.reference)
		if !resolved {
			return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
			}
		}
		if childSlot == privatePagePoolNoIndex {
			continue
		}
		childLive := &overlay.pool.slots[childSlot]
		childPage := uint64(childLive.pageNumber)
		if childSlot == slotIndex || !childLive.bound ||
			childPage < 2 || childPage <= child.lower || childPage >= child.upper ||
			(tree == privatePageDeleteScope &&
				(childLive.scopeID != overlay.scope.id ||
					childLive.scopeAnchorIndex != overlay.scope.anchor ||
					childLive.scopeAnchor != (childSlot == overlay.scope.anchor))) {
			return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: childLive.pageNumber,
			}
		}
		childStates[childIndex] = privatePageDeleteResolvedState(
			overlay.pool, tree, childSlot, childNode,
		)
		if !privatePageDeleteSummaryValid(childStates[childIndex], len(overlay.pool.slots)) {
			return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: childLive.pageNumber,
			}
		}
	}
	leftHeight, rightHeight := childStates[0].height, childStates[1].height
	if leftHeight-rightHeight > 1 || rightHeight-leftHeight > 1 {
		return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	height := leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	if height > 127 ||
		childStates[0].free > ^uint64(0)-childStates[1].free ||
		childStates[0].free+childStates[1].free > ^uint64(0)-node.selfFree ||
		childStates[0].inUse > ^uint64(0)-childStates[1].inUse ||
		childStates[0].inUse+childStates[1].inUse > ^uint64(0)-node.selfInUse {
		return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	free := childStates[0].free + childStates[1].free + node.selfFree
	inUse := childStates[0].inUse + childStates[1].inUse + node.selfInUse
	if state.height != height || state.free != free || state.inUse != inUse ||
		free > uint64(len(overlay.pool.slots)) ||
		inUse > uint64(len(overlay.pool.slots))-free {
		return 0, nil, privatePageDeleteNodeState{}, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	return reference, node, state, freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) prepareRefreshNode(
	tree privatePageDeleteTree,
	reference int,
) freeBitmapCOWError {
	_, node, ok := overlay.resolve(tree, reference)
	if !ok || node == nil {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	children := [2]privatePageDeleteNodeState{}
	for childIndex, child := range [2]int{node.left, node.right} {
		childSlot, childNode, resolved := overlay.resolve(tree, child)
		if !resolved {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		children[childIndex] = privatePageDeleteResolvedState(
			overlay.pool, tree, childSlot, childNode,
		)
	}
	leftHeight, rightHeight := children[0].height, children[1].height
	if leftHeight-rightHeight > 1 || rightHeight-leftHeight > 1 ||
		children[0].free > ^uint64(0)-children[1].free ||
		children[0].free+children[1].free > ^uint64(0)-node.selfFree ||
		children[0].inUse > ^uint64(0)-children[1].inUse ||
		children[0].inUse+children[1].inUse > ^uint64(0)-node.selfInUse {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	height := leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	free := children[0].free + children[1].free + node.selfFree
	inUse := children[0].inUse + children[1].inUse + node.selfInUse
	if height <= 0 || height > 127 || free > uint64(len(overlay.pool.slots)) ||
		inUse > uint64(len(overlay.pool.slots))-free {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	node.height, node.free, node.inUse = int8(height), free, inUse
	node.dirty = true
	return freeBitmapCOWError{}
}

func (overlay *privatePageDeleteOverlay) prepareRefreshPath(
	tree privatePageDeleteTree,
	root int,
	targetSlot int,
	desired *privatePagePoolSlot,
) (int, freeBitmapCOWError) {
	if targetSlot < 0 || targetSlot >= len(overlay.pool.slots) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	targetPage := overlay.pool.slots[targetSlot].pageNumber
	if desired == nil || desired.pageNumber != targetPage {
		return 0, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: targetPage,
		}
	}
	desiredFree, desiredInUse, valid := privatePageDeleteStateCounts(desired)
	if !valid {
		return 0, freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: targetPage,
		}
	}
	reference := root
	lower, upper := uint64(0), uint64(1)<<32
	maxDepth := maximumPrivatePageAVLHeight(len(overlay.pool.slots))
	var parent *freeBitmapCleanupOverlayNode
	parentLeft := false
	pathLen := 0
	for depth := 0; depth < maxDepth; depth++ {
		preparedReference, node, state, problem := overlay.prepareRefreshLocal(
			tree, reference, lower, upper,
		)
		if problem.failed() {
			return 0, problem
		}
		if parent == nil {
			root = preparedReference
		} else if parentLeft {
			parent.left = preparedReference
		} else {
			parent.right = preparedReference
		}
		overlay.scratch.path[pathLen] = preparedReference
		pathLen++
		slot := &overlay.pool.slots[node.slot]
		page := uint64(slot.pageNumber)
		switch {
		case targetPage < slot.pageNumber:
			parent, parentLeft = node, true
			reference, upper = state.left, page
		case targetPage > slot.pageNumber:
			parent, parentLeft = node, false
			reference, lower = state.right, page
		default:
			if node.slot != targetSlot {
				return 0, freeBitmapCOWError{
					code: freeBitmapCOWErrArenaPageConflict, page: targetPage,
				}
			}
			node.selfFree, node.selfInUse = desiredFree, desiredInUse
			for pathIndex := pathLen - 1; pathIndex >= 0; pathIndex-- {
				if problem = overlay.prepareRefreshNode(
					tree, overlay.scratch.path[pathIndex],
				); problem.failed() {
					return 0, problem
				}
			}
			clear(overlay.scratch.path[:pathLen])
			return root, freeBitmapCOWError{}
		}
	}
	return 0, freeBitmapCOWError{
		code: freeBitmapCOWErrArenaPageConflict, page: targetPage,
	}
}

func prepareRetainedPrivatePageRefreshes(
	pool *privatePagePool,
	scope privatePageReservationScope,
	prepared *preparedPrivatePageDeletes,
	retained []bitmapCOWArenaBinding,
	desired []privatePagePoolSlot,
) freeBitmapCOWError {
	if len(retained) != len(desired) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	overlay := privatePageDeleteOverlay{
		pool: pool, scope: scope, scratch: prepared.scratch,
		nodeLen: prepared.nodeLen, indexRoot: prepared.indexRoot, scopeRoot: prepared.scopeRoot,
		work: prepared.work, workLimit: prepared.workLimit,
	}
	for bindingIndex, binding := range retained {
		if binding.poolSlot < 0 || binding.poolSlot >= len(pool.slots) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		var problem freeBitmapCOWError
		overlay.indexRoot, problem = overlay.prepareRefreshPath(
			privatePageDeleteGlobal, overlay.indexRoot, binding.poolSlot, &desired[bindingIndex],
		)
		if problem.failed() {
			return problem
		}
		overlay.scopeRoot, problem = overlay.prepareRefreshPath(
			privatePageDeleteScope, overlay.scopeRoot, binding.poolSlot, &desired[bindingIndex],
		)
		if problem.failed() {
			return problem
		}
	}
	prepared.nodeLen, prepared.indexRoot, prepared.scopeRoot =
		overlay.nodeLen, overlay.indexRoot, overlay.scopeRoot
	prepared.work = overlay.work
	return freeBitmapCOWError{}
}

func prepareSparseRetainedPrivatePageRefreshes(
	pool *privatePagePool,
	scope privatePageReservationScope,
	prepared *preparedPrivatePageDeletes,
	live []bitmapCOWArenaBinding,
	desired []privatePagePoolSlot,
) freeBitmapCOWError {
	if len(live) != len(desired) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	overlay := privatePageDeleteOverlay{
		pool: pool, scope: scope, scratch: prepared.scratch,
		nodeLen: prepared.nodeLen, indexRoot: prepared.indexRoot, scopeRoot: prepared.scopeRoot,
		work: prepared.work, workLimit: prepared.workLimit,
	}
	for bindingIndex, binding := range live {
		if !desired[bindingIndex].bound {
			continue
		}
		if binding.poolSlot < 0 || binding.poolSlot >= len(pool.slots) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		var problem freeBitmapCOWError
		overlay.indexRoot, problem = overlay.prepareRefreshPath(
			privatePageDeleteGlobal, overlay.indexRoot, binding.poolSlot, &desired[bindingIndex],
		)
		if problem.failed() {
			return problem
		}
		overlay.scopeRoot, problem = overlay.prepareRefreshPath(
			privatePageDeleteScope, overlay.scopeRoot, binding.poolSlot, &desired[bindingIndex],
		)
		if problem.failed() {
			return problem
		}
	}
	prepared.nodeLen, prepared.indexRoot, prepared.scopeRoot =
		overlay.nodeLen, overlay.indexRoot, overlay.scopeRoot
	prepared.work = overlay.work
	return freeBitmapCOWError{}
}

func normalizePreparedPrivatePageReference(
	pool *privatePagePool,
	prepared *preparedPrivatePageDeletes,
	tree privatePageDeleteTree,
	reference int,
) (int, bool) {
	if prepared.work >= prepared.workLimit {
		return 0, false
	}
	prepared.work++
	if reference == privatePagePoolNoIndex {
		return reference, true
	}
	if reference >= 0 {
		if reference >= len(pool.slots) {
			return 0, false
		}
		return reference, true
	}
	nodeIndex, ok := privatePageDeleteOverlayIndex(reference)
	if !ok || nodeIndex < 0 || nodeIndex >= prepared.nodeLen {
		return 0, false
	}
	node := &prepared.scratch.nodes[nodeIndex]
	if node.tree != tree || node.slot < 0 || node.slot >= len(pool.slots) {
		return 0, false
	}
	return node.slot, true
}

func validatePreservedPrivatePageRoot(
	pool *privatePagePool,
	scope privatePageReservationScope,
	prepared *preparedPrivatePageDeletes,
	tree privatePageDeleteTree,
	root int,
) freeBitmapCOWError {
	if root == privatePagePoolNoIndex {
		return freeBitmapCOWError{}
	}
	if root < 0 || root >= len(pool.slots) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	slot := &pool.slots[root]
	page := uint64(slot.pageNumber)
	if !slot.bound || page < 2 ||
		(tree == privatePageDeleteScope &&
			(slot.scopeID != scope.id || slot.scopeAnchorIndex != scope.anchor ||
				slot.scopeAnchor != (root == scope.anchor))) {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	selfFree, selfInUse, valid := privatePageDeleteStateCounts(slot)
	if !valid {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	state := privatePageDeleteResolvedState(pool, tree, root, nil)
	children := [2]struct {
		reference    int
		lower, upper uint64
	}{
		{state.left, 0, page},
		{state.right, page, uint64(1) << 32},
	}
	var childStates [2]privatePageDeleteNodeState
	for childIndex, child := range children {
		childSlot, ok := normalizePreparedPrivatePageReference(
			pool, prepared, tree, child.reference,
		)
		if !ok || childSlot != child.reference || childSlot == root {
			return freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
			}
		}
		if childSlot == privatePagePoolNoIndex {
			continue
		}
		child := &pool.slots[childSlot]
		childPage := uint64(child.pageNumber)
		if !child.bound || childPage < 2 ||
			childPage <= children[childIndex].lower ||
			childPage >= children[childIndex].upper ||
			(tree == privatePageDeleteScope &&
				(child.scopeID != scope.id ||
					child.scopeAnchorIndex != scope.anchor ||
					child.scopeAnchor != (childSlot == scope.anchor))) {
			return freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: child.pageNumber,
			}
		}
		childStates[childIndex] = privatePageDeleteResolvedState(
			pool, tree, childSlot, nil,
		)
		if !privatePageDeleteSummaryValid(childStates[childIndex], len(pool.slots)) {
			return freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: child.pageNumber,
			}
		}
	}
	leftHeight, rightHeight := childStates[0].height, childStates[1].height
	if leftHeight-rightHeight > 1 || rightHeight-leftHeight > 1 ||
		childStates[0].free > ^uint64(0)-childStates[1].free ||
		childStates[0].free+childStates[1].free > ^uint64(0)-selfFree ||
		childStates[0].inUse > ^uint64(0)-childStates[1].inUse ||
		childStates[0].inUse+childStates[1].inUse > ^uint64(0)-selfInUse {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	height := leftHeight
	if rightHeight > height {
		height = rightHeight
	}
	height++
	free := childStates[0].free + childStates[1].free + selfFree
	inUse := childStates[0].inUse + childStates[1].inUse + selfInUse
	if state.height != height || state.free != free || state.inUse != inUse ||
		free > uint64(len(pool.slots)) || inUse > uint64(len(pool.slots))-free {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
		}
	}
	return freeBitmapCOWError{}
}

// normalizePreparedPrivatePageReferences is the last tagged-reference
// consumer. After it succeeds, terminal apply contains only ordinary nil or
// in-range pool indexes and performs no fallible decoding after checkpoint.
func normalizePreparedPrivatePageReferences(
	pool *privatePagePool,
	scope privatePageReservationScope,
	prepared *preparedPrivatePageDeletes,
) freeBitmapCOWError {
	if pool == nil || scope.anchor < 0 || scope.anchor >= len(pool.slots) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for nodeIndex := 0; nodeIndex < prepared.nodeLen; nodeIndex++ {
		node := &prepared.scratch.nodes[nodeIndex]
		left, ok := normalizePreparedPrivatePageReference(
			pool, prepared, node.tree, node.left,
		)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		right, ok := normalizePreparedPrivatePageReference(
			pool, prepared, node.tree, node.right,
		)
		if !ok {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		node.left, node.right = left, right
	}
	indexRoot, ok := normalizePreparedPrivatePageReference(
		pool, prepared, privatePageDeleteGlobal, prepared.indexRoot,
	)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	scopeRoot, ok := normalizePreparedPrivatePageReference(
		pool, prepared, privatePageDeleteScope, prepared.scopeRoot,
	)
	if !ok {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	finalScopeBound := pool.slots[scope.anchor].scopeBound - prepared.targetLen
	if finalScopeBound < 0 ||
		(finalScopeBound == 0) != (scopeRoot == privatePagePoolNoIndex) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if prepared.nodeLen == 0 && prepared.targetLen == 0 {
		if problem := validatePreservedPrivatePageRoot(
			pool, scope, prepared, privatePageDeleteGlobal, indexRoot,
		); problem.failed() {
			return problem
		}
		if problem := validatePreservedPrivatePageRoot(
			pool, scope, prepared, privatePageDeleteScope, scopeRoot,
		); problem.failed() {
			return problem
		}
	}
	prepared.indexRoot, prepared.scopeRoot = indexRoot, scopeRoot
	return freeBitmapCOWError{}
}

// validatePreparedPrivatePageCheckpointTouches checks exactly the slots that
// terminal apply will journal or turn into vacancies. With no active
// checkpoint, every tag must be zero and every journal link must be nil.
func validatePreparedPrivatePageCheckpointTouches(
	pool *privatePagePool,
	scope privatePageReservationScope,
	prepared *preparedPrivatePageDeletes,
) freeBitmapCOWError {
	if pool == nil || scope.anchor < 0 || scope.anchor >= len(pool.slots) ||
		!privatePageCheckpointTagsCanonical(&pool.slots[scope.anchor]) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for targetIndex := 0; targetIndex < prepared.targetLen; targetIndex++ {
		slotIndex := prepared.scratch.targets[targetIndex]
		if slotIndex < 0 || slotIndex >= len(pool.slots) ||
			!privatePageCheckpointTagsCanonical(&pool.slots[slotIndex]) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	for nodeIndex := 0; nodeIndex < prepared.nodeLen; nodeIndex++ {
		node := &prepared.scratch.nodes[nodeIndex]
		if !node.dirty {
			continue
		}
		if node.slot < 0 || node.slot >= len(pool.slots) ||
			!privatePageCheckpointTagsCanonical(&pool.slots[node.slot]) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	return freeBitmapCOWError{}
}

func (pool *privatePagePool) applyPreparedPrivatePageDeleteTrees(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	prepared preparedPrivatePageDeletes,
) {
	if prepared.nodeLen == 0 && prepared.targetLen == 0 {
		return
	}
	for nodeIndex := 0; nodeIndex < prepared.nodeLen; nodeIndex++ {
		node := &prepared.scratch.nodes[nodeIndex]
		if !node.dirty {
			continue
		}
		pool.rememberIndex(node.slot, checkpoint)
		slot := &pool.slots[node.slot]
		if node.tree == privatePageDeleteGlobal {
			slot.indexLeft, slot.indexRight = node.left, node.right
			slot.indexHeight = node.height
			slot.indexFree, slot.indexInUse = node.free, node.inUse
		} else {
			slot.scopeLeft, slot.scopeRight = node.left, node.right
			slot.scopeHeight = node.height
			slot.scopeFree, slot.scopeInUse = node.free, node.inUse
		}
	}
	pool.indexRoot = prepared.indexRoot
	pool.rememberScopeHeader(scope.anchor, checkpoint)
	pool.slots[scope.anchor].scopeRoot = prepared.scopeRoot
}

func (pool *privatePagePool) unbindPreparedPrivatePageTarget(
	checkpoint privatePagePoolCheckpoint,
	scope privatePageReservationScope,
	index int,
	shrinkTail bool,
) {
	anchor := &pool.slots[scope.anchor]
	slot := &pool.slots[index]
	pool.remember(index, checkpoint)
	pool.rememberIndex(index, checkpoint)
	pool.rememberScopeHeader(scope.anchor, checkpoint)
	anchor.scopeBound--
	slot.bound = false
	slot.pageNumber = 0
	slot.authorization = privatePageAuthorizationNone
	slot.state, slot.inUse = privatePageAvailable, false
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
	if shrinkTail {
		pool.pendingPageCount--
	}
	slot.epoch++
	pool.advanceMutationPrepared()
}

var freeBitmapFinalizationNonce atomic.Uint64

func finalizationCacheContentSeal(key uint64, pageNumber uint32, page *[PageSize]byte) uint64 {
	hash := freeBitmapFingerprintUint64(1469598103934665603^key, uint64(pageNumber))
	return freeBitmapFingerprintBytes(hash, page[:])
}

func finalizationCacheMetadataSeal(key uint64, page *freeBitmapFinalizationCachedPage) uint64 {
	hash := freeBitmapFingerprintUint64(1099511628211^key, uint64(page.pageNumber))
	hash = freeBitmapFingerprintUint64(hash, uint64(page.left+1))
	hash = freeBitmapFingerprintUint64(hash, uint64(page.right+1))
	hash = freeBitmapFingerprintUint64(hash, uint64(page.height))
	return freeBitmapFingerprintUint64(hash, page.contentSeal)
}

func (source *freeBitmapFinalizationCachedSource) validMetadata(index int) bool {
	return index >= 0 && index < source.length &&
		source.pages[index].metadataSeal == finalizationCacheMetadataSeal(source.sealKey, &source.pages[index])
}

func (source *freeBitmapFinalizationCachedSource) find(pageNumber uint32) (int, bool) {
	for index, visited := source.root, 0; index != bitmapCOWNoIndex; visited++ {
		source.nodeVisits++
		if visited >= source.length || !source.validMetadata(index) {
			source.problem = staleFreeBitmapReservationBind()
			return 0, false
		}
		page := &source.pages[index]
		switch {
		case pageNumber < page.pageNumber:
			index = page.left
		case pageNumber > page.pageNumber:
			index = page.right
		default:
			if page.contentSeal != finalizationCacheContentSeal(source.sealKey, page.pageNumber, &page.bytes) {
				source.problem = staleFreeBitmapReservationBind()
				return 0, false
			}
			return index, true
		}
	}
	return 0, false
}

func (source *freeBitmapFinalizationCachedSource) height(index int) (uint8, bool) {
	if index == bitmapCOWNoIndex {
		return 0, true
	}
	if !source.validMetadata(index) {
		return 0, false
	}
	return source.pages[index].height, true
}

func (source *freeBitmapFinalizationCachedSource) refresh(index int) bool {
	if index < 0 || index >= source.length {
		return false
	}
	page := &source.pages[index]
	left, leftOK := source.height(page.left)
	right, rightOK := source.height(page.right)
	if !leftOK || !rightOK {
		return false
	}
	if right > left {
		left = right
	}
	if left == ^uint8(0) {
		return false
	}
	page.height = left + 1
	page.metadataSeal = finalizationCacheMetadataSeal(source.sealKey, page)
	return true
}

func (source *freeBitmapFinalizationCachedSource) rotateLeft(root int) (int, bool) {
	if !source.validMetadata(root) {
		return 0, false
	}
	pivot := source.pages[root].right
	if !source.validMetadata(pivot) {
		return 0, false
	}
	middle := source.pages[pivot].left
	source.pages[root].right = middle
	if !source.refresh(root) {
		return 0, false
	}
	source.pages[pivot].left = root
	if !source.refresh(pivot) {
		return 0, false
	}
	return pivot, true
}

func (source *freeBitmapFinalizationCachedSource) rotateRight(root int) (int, bool) {
	if !source.validMetadata(root) {
		return 0, false
	}
	pivot := source.pages[root].left
	if !source.validMetadata(pivot) {
		return 0, false
	}
	middle := source.pages[pivot].right
	source.pages[root].left = middle
	if !source.refresh(root) {
		return 0, false
	}
	source.pages[pivot].right = root
	if !source.refresh(pivot) {
		return 0, false
	}
	return pivot, true
}

func (source *freeBitmapFinalizationCachedSource) rebalance(root int) (int, bool) {
	if !source.refresh(root) {
		return 0, false
	}
	page := &source.pages[root]
	leftHeight, leftOK := source.height(page.left)
	rightHeight, rightOK := source.height(page.right)
	if !leftOK || !rightOK {
		return 0, false
	}
	balance := int(leftHeight) - int(rightHeight)
	if balance > 1 {
		left := page.left
		if !source.validMetadata(left) {
			return 0, false
		}
		leftLeft, ok1 := source.height(source.pages[left].left)
		leftRight, ok2 := source.height(source.pages[left].right)
		if !ok1 || !ok2 {
			return 0, false
		}
		if leftRight > leftLeft {
			rotated, ok := source.rotateLeft(left)
			if !ok {
				return 0, false
			}
			page.left = rotated
			page.metadataSeal = finalizationCacheMetadataSeal(source.sealKey, page)
		}
		return source.rotateRight(root)
	}
	if balance < -1 {
		right := page.right
		if !source.validMetadata(right) {
			return 0, false
		}
		rightLeft, ok1 := source.height(source.pages[right].left)
		rightRight, ok2 := source.height(source.pages[right].right)
		if !ok1 || !ok2 {
			return 0, false
		}
		if rightLeft > rightRight {
			rotated, ok := source.rotateRight(right)
			if !ok {
				return 0, false
			}
			page.right = rotated
			page.metadataSeal = finalizationCacheMetadataSeal(source.sealKey, page)
		}
		return source.rotateLeft(root)
	}
	return root, true
}

func (source *freeBitmapFinalizationCachedSource) insert(
	pageNumber uint32,
	bytes *[PageSize]byte,
) bool {
	if source.length == len(source.pages) {
		return false
	}
	top := 0
	for index, visited := source.root, 0; index != bitmapCOWNoIndex; visited++ {
		source.nodeVisits++
		if visited >= source.length || !source.validMetadata(index) {
			return false
		}
		if top == len(source.stack) {
			source.problem = freeBitmapCOWError{
				code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceIndexNodes,
				required: top + 1, actual: len(source.stack),
			}
			return false
		}
		source.stack[top] = index
		top++
		if pageNumber < source.pages[index].pageNumber {
			index = source.pages[index].left
		} else if pageNumber > source.pages[index].pageNumber {
			index = source.pages[index].right
		} else {
			return false
		}
	}
	inserted := source.length
	source.length++
	source.pages[inserted] = freeBitmapFinalizationCachedPage{
		pageNumber: pageNumber, bytes: *bytes, left: bitmapCOWNoIndex, right: bitmapCOWNoIndex, height: 1,
	}
	source.pages[inserted].contentSeal = finalizationCacheContentSeal(source.sealKey, pageNumber, bytes)
	source.pages[inserted].metadataSeal = finalizationCacheMetadataSeal(source.sealKey, &source.pages[inserted])
	subtree := inserted
	for top > 0 {
		top--
		index := source.stack[top]
		if !source.validMetadata(index) {
			source.problem = staleFreeBitmapReservationBind()
			return false
		}
		if pageNumber < source.pages[index].pageNumber {
			source.pages[index].left = subtree
		} else {
			source.pages[index].right = subtree
		}
		source.pages[index].metadataSeal = finalizationCacheMetadataSeal(source.sealKey, &source.pages[index])
		var ok bool
		subtree, ok = source.rebalance(index)
		if !ok {
			source.problem = staleFreeBitmapReservationBind()
			return false
		}
	}
	source.root = subtree
	return true
}

func (source *freeBitmapFinalizationCachedSource) checkAccessStatus() pageSourceStatus {
	if source.sealed || source.base == nil {
		return pageSourceStatus{}
	}
	seal := sealFreeBitmapFinalizationCacheControl(source)
	status := source.base.checkAccessStatus()
	if !seal.matches(source) {
		source.problem = staleFreeBitmapReservationBind()
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	return status
}

func (source *freeBitmapFinalizationCachedSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	if source.cow != nil {
		indexed, found, valid := finalizationCheckedIndexedPage(source.cow, pageNumber)
		if !valid {
			source.problem = staleFreeBitmapReservationBind()
			return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
		}
		if found && indexed.kind == indexedBitmapPageVerified {
			if indexed.slot < 0 || indexed.slot >= len(source.cow.verifiedPages) {
				source.problem = staleFreeBitmapReservationBind()
				return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
			}
			*destination = source.cow.verifiedPages[indexed.slot].bytes
			return pageSourceStatus{}
		}
	}
	if index, found := source.find(pageNumber); found {
		*destination = source.pages[index].bytes
		return pageSourceStatus{}
	}
	if source.sealed || source.base == nil {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	if source.problem.failed() {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	if source.length == len(source.pages) {
		source.problem = freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceVerifiedPages,
			required: source.length + 1, actual: len(source.pages),
		}
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	control := sealFreeBitmapFinalizationCacheControl(source)
	base := source.base
	page := &source.pages[source.length].bytes
	status := base.readPageStatus(pageNumber, page)
	if !control.matches(source) {
		source.problem = staleFreeBitmapReservationBind()
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	if status.failed() {
		return status
	}
	index, found := source.find(pageNumber)
	if source.problem.failed() {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	if found {
		if source.pages[index].bytes != *page {
			source.problem = staleFreeBitmapReservationBind()
			return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
		}
		*destination = *page
		return pageSourceStatus{}
	}
	if !source.insert(pageNumber, page) {
		if !source.problem.failed() {
			source.problem = staleFreeBitmapReservationBind()
		}
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	*destination = *page
	return pageSourceStatus{}
}

func sealFreeBitmapFinalizationCacheControl(
	source *freeBitmapFinalizationCachedSource,
) freeBitmapFinalizationCacheControlSeal {
	return freeBitmapFinalizationCacheControlSeal{
		base: source.base, cow: source.cow, pages: sealFreeBitmapReservationSlice(source.pages),
		length: source.length, root: source.root, stack: sealFreeBitmapReservationSlice(source.stack),
		sealKey: source.sealKey, sealed: source.sealed, problem: source.problem, nodeVisits: source.nodeVisits,
		failure: source.failure,
	}
}

func finalizationCOWSealsEqual(left, right freeBitmapReservationCOWSeal) bool {
	if !sameFreeBitmapReservationCommittedSource(left.committed, right.committed) {
		return false
	}
	left.committed = nil
	right.committed = nil
	return left == right
}

func (fence freeBitmapFinalizationFailureFence) same(other freeBitmapFinalizationFailureFence) bool {
	return fence.armed == other.armed && fence.cow == other.cow &&
		finalizationCOWSealsEqual(fence.cowSeal, other.cowSeal) &&
		fence.cowFingerprint == other.cowFingerprint && fence.poolSeal == other.poolSeal &&
		fence.scopeFingerprint == other.scopeFingerprint
}

func (source *freeBitmapFinalizationCachedSource) armFailureFence(cow *freeBitmapCOW) {
	source.failure = freeBitmapFinalizationFailureFence{
		armed: true, cow: cow, cowSeal: sealFreeBitmapReservationCOW(cow),
		cowFingerprint:   freeBitmapReservationCOWFingerprint(cow),
		poolSeal:         sealFreeBitmapReservationPool(cow.pool),
		scopeFingerprint: freeBitmapReservationScopeFingerprint(cow.pool, cow.scope),
	}
}

func (fence freeBitmapFinalizationFailureFence) matches() bool {
	return fence.armed && fence.cow != nil && finalizationCOWSealMatches(fence.cowSeal, fence.cow) &&
		freeBitmapReservationCOWFingerprint(fence.cow) == fence.cowFingerprint &&
		fence.poolSeal.matches(fence.cow.pool) &&
		freeBitmapReservationScopeFingerprint(fence.cow.pool, fence.cow.scope) == fence.scopeFingerprint
}

func (seal freeBitmapFinalizationCacheControlSeal) matches(
	source *freeBitmapFinalizationCachedSource,
) bool {
	return source != nil && sameFreeBitmapReservationCommittedSource(seal.base, source.base) &&
		seal.cow == source.cow && seal.pages.matches(source.pages) && seal.length == source.length &&
		seal.root == source.root && seal.stack.matches(source.stack) && seal.sealKey == source.sealKey &&
		seal.sealed == source.sealed && seal.problem == source.problem && seal.nodeVisits == source.nodeVisits &&
		seal.failure.same(source.failure)
}

func finalizationCheckedIndexedPage(
	cow *freeBitmapCOW,
	pageNumber uint32,
) (indexedBitmapPage, bool, bool) {
	if cow == nil {
		return indexedBitmapPage{}, false, false
	}
	index := cow.indexRoot
	for visited := 0; visited <= len(cow.indexNodes); visited++ {
		if index == bitmapCOWNoIndex {
			return indexedBitmapPage{}, false, true
		}
		if index < 0 || index >= len(cow.indexNodes) {
			return indexedBitmapPage{}, false, false
		}
		node := &cow.indexNodes[index]
		switch {
		case pageNumber < node.pageNumber:
			index = node.left
		case pageNumber > node.pageNumber:
			index = node.right
		default:
			return node.page, true, true
		}
	}
	return indexedBitmapPage{}, false, false
}

func finalizationCacheFingerprint(pages []freeBitmapFinalizationCachedPage) uint64 {
	hash := freeBitmapFingerprintUint64(1469598103934665603, uint64(len(pages)))
	for _, page := range pages {
		hash = freeBitmapFingerprintUint64(hash, uint64(page.pageNumber))
		hash = freeBitmapFingerprintUint64(hash, page.contentSeal)
		hash = freeBitmapFingerprintUint64(hash, page.metadataSeal)
		hash = freeBitmapFingerprintUint64(hash, uint64(page.left+1))
		hash = freeBitmapFingerprintUint64(hash, uint64(page.right+1))
		hash = freeBitmapFingerprintUint64(hash, uint64(page.height))
		hash = freeBitmapFingerprintBytes(hash, page.bytes[:])
	}
	return hash
}

func (source *freeBitmapFinalizationCachedSource) validate() freeBitmapCOWError {
	if source.length < 0 || source.length > len(source.pages) {
		return staleFreeBitmapReservationBind()
	}
	top, visited := 0, 0
	index := source.root
	previous := uint32(0)
	havePrevious := false
	for index != bitmapCOWNoIndex || top != 0 {
		for index != bitmapCOWNoIndex {
			source.nodeVisits++
			if !source.validMetadata(index) {
				return staleFreeBitmapReservationBind()
			}
			if top == len(source.stack) {
				return freeBitmapCOWError{
					code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceIndexNodes,
					required: top + 1, actual: len(source.stack),
				}
			}
			source.stack[top] = index
			top++
			index = source.pages[index].left
		}
		top--
		index = source.stack[top]
		visited++
		if visited > source.length {
			return staleFreeBitmapReservationBind()
		}
		page := &source.pages[index]
		if (havePrevious && page.pageNumber <= previous) ||
			page.contentSeal != finalizationCacheContentSeal(source.sealKey, page.pageNumber, &page.bytes) {
			return staleFreeBitmapReservationBind()
		}
		previous, havePrevious = page.pageNumber, true
		index = page.right
	}
	if visited != source.length {
		return staleFreeBitmapReservationBind()
	}
	return freeBitmapCOWError{}
}

func mintFreeBitmapFinalizationNonce() (uint64, bool) {
	for {
		current := freeBitmapFinalizationNonce.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		if freeBitmapFinalizationNonce.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
}

func captureFreeBitmapFinalizationLiveSeal(
	p *freeBitmapReservationAttachment,
) (freeBitmapFinalizationLiveSeal, freeBitmapCOWError) {
	if p == nil || p.privatePages <= 0 || p.payloadPages < 0 || p.verifiedLen < 0 ||
		p.indexRequired < 0 || p.cow.pool == nil || p.cow.pool.self != p.cow.pool ||
		p.scope.pool != p.cow.pool || p.cow.scope != p.scope || !p.cow.scoped ||
		p.cow.scopeCapacity != p.privatePages || p.reclamationRequest.nonce != 0 ||
		len(p.cow.arenaBindings) != p.privatePages || len(p.cow.availableSlots) != p.privatePages {
		return freeBitmapFinalizationLiveSeal{}, staleFreeBitmapReservationBind()
	}
	if problem := p.validateStageBuffers(nil); problem.failed() {
		return freeBitmapFinalizationLiveSeal{}, problem
	}
	if problem := p.cow.validateScopedBindings(); problem.failed() {
		return freeBitmapFinalizationLiveSeal{}, problem
	}
	status, poolProblem := p.cow.pool.status()
	if poolProblem.failed() || status.pendingPageCount != p.cow.pageCount {
		return freeBitmapFinalizationLiveSeal{}, staleFreeBitmapReservationBind()
	}
	return freeBitmapFinalizationLiveSeal{
		attachment: p, privatePages: p.privatePages, payloadPages: p.payloadPages,
		verifiedLen: p.verifiedLen, indexRequired: p.indexRequired, scope: p.scope,
		poolGeneration: p.poolGeneration, poolMutationEpoch: p.poolMutationEpoch,
		cow: sealFreeBitmapReservationCOW(&p.cow), pool: sealFreeBitmapReservationPool(p.cow.pool),
		buffers:               sealFreeBitmapReservationBuffers(p.buffers),
		scopeFingerprint:      freeBitmapReservationScopeFingerprint(p.cow.pool, p.scope),
		cowContentFingerprint: freeBitmapReservationCOWFingerprint(&p.cow),
	}, freeBitmapCOWError{}
}

func (seal freeBitmapFinalizationLiveSeal) matches(p *freeBitmapReservationAttachment) bool {
	return p != nil && p == seal.attachment && p.privatePages == seal.privatePages &&
		p.payloadPages == seal.payloadPages && p.verifiedLen == seal.verifiedLen &&
		p.indexRequired == seal.indexRequired && p.scope == seal.scope &&
		p.poolGeneration == seal.poolGeneration && p.poolMutationEpoch == seal.poolMutationEpoch &&
		finalizationCOWSealMatches(seal.cow, &p.cow) && seal.pool.matches(p.cow.pool) && seal.buffers.matches(p.buffers) &&
		freeBitmapReservationScopeFingerprint(p.cow.pool, p.scope) == seal.scopeFingerprint &&
		freeBitmapReservationCOWFingerprint(&p.cow) == seal.cowContentFingerprint
}

func finalizationCOWSealMatches(seal freeBitmapReservationCOWSeal, cow *freeBitmapCOW) bool {
	return cow != nil && sameFreeBitmapReservationCommittedSource(cow.committed, seal.committed) &&
		cow.selectedTxn == seal.selectedTxn && cow.pendingTxn == seal.pendingTxn &&
		cow.committedPageCount == seal.committedPageCount && cow.pageCount == seal.pageCount &&
		cow.pageCountsDistinct == seal.pageCountsDistinct && cow.root == seal.root && cow.pool == seal.pool &&
		cow.scope == seal.scope && cow.scoped == seal.scoped && cow.scopeCapacity == seal.scopeCapacity &&
		cow.replacementLen == seal.replacementLen && cow.candidateLen == seal.candidateLen &&
		cow.indexRoot == seal.indexRoot && cow.indexLen == seal.indexLen &&
		cow.availableLen == seal.availableLen && cow.plannedCandidateLen == seal.plannedCandidateLen &&
		cow.selectedCandidateLen == seal.selectedCandidateLen &&
		cow.candidateSelectionSet == seal.candidateSelectionSet &&
		cow.reservationPlanned == seal.reservationPlanned &&
		cow.payloadPageBudget == seal.payloadPageBudget &&
		cow.plannedRequiredPrivatePages == seal.plannedRequiredPrivatePages &&
		cow.mutationEpoch == seal.mutationEpoch && cow.singleInsertPage == seal.singleInsertPage &&
		seal.arenaBindings.matches(cow.arenaBindings) && seal.replacements.matches(cow.replacements) &&
		seal.candidates.matches(cow.candidates) && seal.indexNodes.matches(cow.indexNodes) &&
		seal.availableSlots.matches(cow.availableSlots) && seal.verifiedPages.matches(cow.verifiedPages)
}

func (p *freeBitmapReservationAttachment) buildFinalizationShadow(
	committed committedPageSource,
) (*freeBitmapCOW, freeBitmapCOWError) {
	stage := &p.buffers.stage
	livePool := p.cow.pool
	if poolProblem := initVacantPrivatePagePool(
		stage.pool, stage.arena[:p.privatePages], p.cow.committedPageCount,
		p.cow.pageCount, p.cow.pendingTxn,
	); poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	clear(stage.arenaBindings[:p.privatePages])
	clear(stage.replacements[:len(p.cow.replacements)])
	clear(stage.indexNodes[:len(p.cow.indexNodes)])
	clear(stage.availableSlots[:p.privatePages])
	scope, poolProblem := stage.pool.reserveScope(p.privatePages)
	if poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	copy(stage.replacements, p.cow.replacements)
	ledger := freeBitmapCOWLedger{
		arena: stage.arena[:p.privatePages], arenaBindings: stage.arenaBindings[:p.privatePages],
		replacements: stage.replacements[:len(p.cow.replacements)], replacementLen: p.cow.replacementLen,
		candidates: p.cow.candidates, candidateLen: 0,
		indexNodes:     stage.indexNodes[:len(p.cow.indexNodes)],
		availableSlots: stage.availableSlots[:p.privatePages], verifiedPages: nil,
		plannedCandidateLen: p.cow.plannedCandidateLen, reservationPlanned: p.cow.reservationPlanned,
		payloadPageBudget: p.cow.payloadPageBudget, plannedPrivatePages: p.cow.plannedRequiredPrivatePages,
	}
	if problem := initializeFreeBitmapCOWWithScopedPoolTransactions(
		stage.cow, committed, p.cow.selectedTxn, p.cow.sourceTxn, p.cow.pendingTxn,
		p.cow.pageCount, p.cow.root, stage.pool, scope, ledger,
	); problem.failed() {
		return nil, problem
	}
	shadow := stage.cow
	checkpoint, poolProblem := stage.pool.begin()
	if poolProblem.failed() {
		return nil, bitmapPoolError(poolProblem)
	}
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		binding := p.cow.arenaBindings[bindingIndex]
		if !binding.bound || binding.poolSlot < 0 || binding.poolSlot >= len(livePool.slots) {
			return nil, rollbackDisposableBitmapShadow(
				stage.pool, checkpoint, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict},
			)
		}
		live := &livePool.slots[binding.poolSlot]
		if _, poolProblem = stage.pool.bindPage(checkpoint, scope, live.pageNumber, live.authorization); poolProblem.failed() {
			return nil, rollbackDisposableBitmapShadow(stage.pool, checkpoint, bitmapPoolError(poolProblem))
		}
	}
	if poolProblem = stage.pool.commit(checkpoint); poolProblem.failed() {
		return nil, rollbackDisposableBitmapShadow(stage.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		live := &livePool.slots[p.cow.arenaBindings[bindingIndex].poolSlot]
		shadow := &stage.pool.slots[bindingIndex]
		shadow.state, shadow.inUse = live.state, live.inUse
		shadow.owner, shadow.origin = live.owner, live.origin
		shadow.pendingTxn, shadow.generation = live.pendingTxn, live.generation
		shadow.committedOrigin, shadow.bytes = live.committedOrigin, live.bytes
		shadow.pendingReturnState = live.pendingReturnState
		shadow.epoch++
	}
	stage.pool.rebuildAllIndexCounts()
	if problem := shadow.selectPlannedCandidatePrefix(p.cow.selectedCandidateTarget()); problem.failed() {
		return nil, problem
	}
	if problem := shadow.synchronizeScopedBindingsForCandidatePrefix(
		scope, p.cow.selectedCandidateTarget(),
	); problem.failed() {
		return nil, problem
	}
	shadow.candidateLen = p.cow.candidateLen
	shadow.mutationEpoch = p.cow.mutationEpoch
	if problem := shadow.validateScopedBindings(); problem.failed() {
		return nil, problem
	}
	return shadow, freeBitmapCOWError{}
}

func finalizationProjectedTail(cow *freeBitmapCOW) uint64 {
	tail := cow.pool.pendingPageCount
	for tail > cow.committedPageCount {
		pageNumber := uint32(tail - 1)
		slotIndex, found := cow.pool.slotIndex(pageNumber)
		if !found {
			break
		}
		slot := &cow.pool.slots[slotIndex]
		if slot.scopeID != cow.scope.id || slot.scopeAnchorIndex != cow.scope.anchor ||
			slot.authorization != privatePageAppended || slot.state != privatePageAvailable ||
			slot.inUse || slot.owner != privatePageOwnerNone || slot.origin != privatePageOriginNone {
			break
		}
		tail--
	}
	return tail
}

func finalizationPotentialReleaseCount(cow *freeBitmapCOW, governingPageCount uint64) (int, freeBitmapCOWError) {
	count := 0
	for _, binding := range cow.arenaBindings {
		if !binding.bound || binding.poolSlot < 0 || binding.poolSlot >= len(cow.pool.slots) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		slot := &cow.pool.slots[binding.poolSlot]
		if slot.scopeID != cow.scope.id || slot.scopeAnchorIndex != cow.scope.anchor ||
			slot.pageNumber != binding.pageNumber {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: binding.pageNumber}
		}
		if slot.state != privatePageAvailable || uint64(slot.pageNumber) >= governingPageCount {
			continue
		}
		switch slot.authorization {
		case privatePageCommittedFree, privatePageReclaimed, privatePageAppended:
			if count == int(^uint(0)>>1) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
			}
			count++
		}
	}
	return count, freeBitmapCOWError{}
}

func finalizationScratchRequirements(
	p *freeBitmapReservationAttachment,
) (freeBitmapFinalizationScratchRequirements, freeBitmapCOWError) {
	if p == nil || p.cow.pool == nil || p.privatePages < 0 {
		return freeBitmapFinalizationScratchRequirements{}, staleFreeBitmapReservationBind()
	}
	cleanupNodes, cleanupPath, ok := privatePageDeleteScratchRequirements(
		len(p.cow.pool.slots), p.privatePages, p.privatePages,
	)
	if !ok {
		return freeBitmapFinalizationScratchRequirements{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	newTail := finalizationProjectedTail(&p.cow)
	releaseCount, problem := finalizationPotentialReleaseCount(&p.cow, newTail)
	if problem.failed() {
		return freeBitmapFinalizationScratchRequirements{}, problem
	}
	requirements := freeBitmapFinalizationScratchRequirements{
		releasePages: p.privatePages,
		cachedPages:  1,
		indexStack:   len(p.cow.indexNodes),
		cleanupNodes: cleanupNodes,
		cleanupPath:  cleanupPath,
		cleanupSlots: p.privatePages,
	}
	if releaseCount == 0 && newTail == p.cow.pageCount {
		return requirements, freeBitmapCOWError{}
	}
	if p.cow.root == 0 {
		requirements.insertPages = freeBitmapPathCapacity
		return requirements, freeBitmapCOWError{}
	}
	if p.privatePages > (int(^uint(0)>>1)-freeBitmapPathCapacity)/freeBitmapPathCapacity {
		return freeBitmapFinalizationScratchRequirements{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	requirements.insertPages = p.privatePages*freeBitmapPathCapacity + freeBitmapPathCapacity
	requirements.cachedPages = p.privatePages * freeBitmapPathCapacity
	return requirements, freeBitmapCOWError{}
}

func validateFreeBitmapFinalizationScratchAliases(
	p *freeBitmapReservationAttachment,
	scratch freeBitmapFinalizationScratch,
) freeBitmapCOWError {
	for _, live := range [7][]uint32{
		p.cow.candidates,
		p.cow.replacements,
		p.buffers.poolValidation,
		p.buffers.candidates,
		p.buffers.replacements,
		p.buffers.stage.poolValidation,
		p.buffers.stage.replacements,
	} {
		if reservationSlicesOverlap(scratch.releasePages, live) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	if p.reclamationRequest.ticket != nil &&
		reservationSlicesOverlap(scratch.releasePages, p.reclamationRequest.ticket.pages) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	if reservationSlicesOverlap(scratch.releasePages, p.cow.singleInsertPage[:]) ||
		reservationSlicesOverlap(scratch.releasePages, p.buffers.stage.cow.singleInsertPage[:]) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for _, live := range [3][]int{
		p.cow.availableSlots,
		p.buffers.availableSlots,
		p.buffers.stage.availableSlots,
	} {
		if reservationSlicesOverlap(scratch.indexStack, live) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		for _, cleanup := range [2][]int{scratch.cleanup.path, scratch.cleanup.targets} {
			if reservationSlicesOverlap(cleanup, live) {
				return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
		}
	}
	if reservationSlicesOverlap(scratch.indexStack, p.cow.cloneSlots[:]) ||
		reservationSlicesOverlap(scratch.indexStack, p.buffers.stage.cow.cloneSlots[:]) ||
		!validateFreeBitmapCleanupScratchAliases(scratch.cleanup, scratch.indexStack) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for _, cleanup := range [2][]int{scratch.cleanup.path, scratch.cleanup.targets} {
		if reservationSlicesOverlap(cleanup, p.cow.cloneSlots[:]) ||
			reservationSlicesOverlap(cleanup, p.buffers.stage.cow.cloneSlots[:]) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	return freeBitmapCOWError{}
}

func validateFreeBitmapFinalizationScratch(
	p *freeBitmapReservationAttachment,
	scratch freeBitmapFinalizationScratch,
) freeBitmapCOWError {
	if scratch.cache == nil {
		return freeBitmapCOWError{
			code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceVerifiedPages,
			required: 1,
		}
	}
	if problem := validateFreeBitmapFinalizationScratchAliases(p, scratch); problem.failed() {
		return problem
	}
	required, problem := finalizationScratchRequirements(p)
	if problem.failed() {
		return problem
	}
	for _, budget := range []struct {
		resource freeBitmapReservationResource
		required int
		actual   int
	}{
		{freeBitmapResourceCandidatePages, required.releasePages, len(scratch.releasePages)},
		{freeBitmapResourceArenaPages, required.insertPages, len(scratch.insertPages)},
		{freeBitmapResourceVerifiedPages, required.cachedPages, len(scratch.cachedPages)},
		{freeBitmapResourceIndexNodes, required.indexStack, len(scratch.indexStack)},
		{freeBitmapResourceIndexNodes, required.cleanupNodes, len(scratch.cleanup.nodes)},
		{freeBitmapResourceAvailableSlots, required.cleanupPath, len(scratch.cleanup.path)},
		{freeBitmapResourceAvailableSlots, required.cleanupSlots, len(scratch.cleanup.targets)},
	} {
		if budget.actual < budget.required {
			return freeBitmapCOWError{
				code: freeBitmapCOWErrInsufficientResourceBudget, resource: budget.resource,
				required: budget.required, actual: budget.actual,
			}
		}
	}
	if !freeBitmapCleanupScratchCanonical(scratch.cleanup) {
		return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	return freeBitmapCOWError{}
}

func collectFinalizationReleasePages(
	cow *freeBitmapCOW,
	releasePages []uint32,
	indexStack []int,
	governingPageCount uint64,
) (int, freeBitmapCOWError) {
	length, top, visited := 0, 0, 0
	nodeIndex := cow.indexRoot
	previous := uint32(0)
	havePrevious := false
	for nodeIndex != bitmapCOWNoIndex || top != 0 {
		for nodeIndex != bitmapCOWNoIndex {
			if nodeIndex < 0 || nodeIndex >= len(cow.indexNodes) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
			if top == len(indexStack) {
				if top >= len(cow.indexNodes) {
					return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
				}
				return 0, freeBitmapCOWError{
					code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceIndexNodes,
					required: top + 1, actual: len(indexStack),
				}
			}
			indexStack[top] = nodeIndex
			top++
			nodeIndex = cow.indexNodes[nodeIndex].left
		}
		top--
		nodeIndex = indexStack[top]
		visited++
		if visited > len(cow.indexNodes) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		node := &cow.indexNodes[nodeIndex]
		if havePrevious && node.pageNumber <= previous {
			return 0, freeBitmapCOWError{
				code: freeBitmapCOWErrInsertPageOrderRegression, previousPage: previous, page: node.pageNumber,
			}
		}
		previous, havePrevious = node.pageNumber, true
		if node.page.kind == indexedBitmapPageArena {
			if node.page.slot < 0 || node.page.slot >= len(cow.pool.slots) {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: node.pageNumber}
			}
			page := &cow.pool.slots[node.page.slot]
			if page.scopeID != cow.scope.id || page.scopeAnchorIndex != cow.scope.anchor ||
				!page.bound || page.pageNumber != node.pageNumber {
				return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: node.pageNumber}
			}
			if page.state == privatePageAvailable && uint64(page.pageNumber) < governingPageCount {
				switch page.authorization {
				case privatePageCommittedFree, privatePageReclaimed, privatePageAppended:
					if length == len(releasePages) {
						return 0, freeBitmapCOWError{
							code: freeBitmapCOWErrInsufficientResourceBudget, resource: freeBitmapResourceCandidatePages,
							required: length + 1, actual: len(releasePages),
						}
					}
					releasePages[length] = page.pageNumber
					length++
				}
			}
		}
		nodeIndex = node.right
	}
	return length, freeBitmapCOWError{}
}

func countFinalizationReleaseKinds(
	cow *freeBitmapCOW,
	pages []uint32,
) (committed, reclaimed, appended int, problem freeBitmapCOWError) {
	for _, pageNumber := range pages {
		index, found := cow.pool.slotIndex(pageNumber)
		if !found {
			return 0, 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		switch cow.pool.slots[index].authorization {
		case privatePageCommittedFree:
			committed++
		case privatePageReclaimed:
			reclaimed++
		case privatePageAppended:
			appended++
		default:
			return 0, 0, 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
	}
	return committed, reclaimed, appended, freeBitmapCOWError{}
}

func unbindFinalizationShadowTail(cow *freeBitmapCOW, oldTail, newTail uint64) freeBitmapCOWError {
	if oldTail == newTail {
		return freeBitmapCOWError{}
	}
	checkpoint, poolProblem := cow.pool.begin()
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	for page := oldTail; page > newTail; page-- {
		if poolProblem = cow.pool.unbindPage(checkpoint, cow.scope, uint32(page-1)); poolProblem.failed() {
			return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
		}
	}
	if poolProblem = cow.pool.commit(checkpoint); poolProblem.failed() {
		return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	return cow.synchronizeScopedBindingsForCandidatePrefix(cow.scope, cow.selectedCandidateTarget())
}

func terminalizeFinalizationShadow(cow *freeBitmapCOW) freeBitmapCOWError {
	for bindingIndex := 0; bindingIndex < cow.scopeCapacity; bindingIndex++ {
		binding := &cow.arenaBindings[bindingIndex]
		if !binding.bound {
			continue
		}
		active := &cow.indexNodes[binding.activeNode]
		if !active.candidateMapped {
			continue
		}
		pageNumber := binding.pageNumber
		var removed int
		cow.indexRoot, removed = pageIndexDelete(cow.indexNodes, cow.indexRoot, pageNumber)
		if removed != binding.activeNode || binding.storageNode < 0 || binding.storageNode >= len(cow.indexNodes) {
			return freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: pageNumber}
		}
		cow.indexNodes[removed] = bitmapCOWIndexNode{left: bitmapCOWNoIndex, right: bitmapCOWNoIndex}
		pageIndexInsertExistingPrechecked(
			cow.indexNodes, &cow.indexRoot, binding.storageNode, pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageArena, slot: binding.poolSlot},
		)
		binding.activeNode = binding.storageNode
	}
	cow.candidateLen = 0
	cow.plannedCandidateLen = 0
	cow.selectedCandidateLen = 0
	cow.candidateSelectionSet = false
	cow.reservationPlanned = false
	cow.payloadPageBudget = 0
	cow.plannedRequiredPrivatePages = 0
	cow.availableLen = 0
	clear(cow.availableSlots)
	return cow.validateScopedBindings()
}

func unbindFinalizationShadowReleasedFree(
	cow *freeBitmapCOW,
	bindingTargets []int,
) (int, freeBitmapCOWError) {
	targetLen := 0
	for bindingIndex, binding := range cow.arenaBindings {
		if !binding.bound || binding.poolSlot < 0 || binding.poolSlot >= len(cow.pool.slots) {
			continue
		}
		if cow.pool.slots[binding.poolSlot].state == privatePageReleasedFree {
			if targetLen == len(bindingTargets) {
				return 0, freeBitmapCOWError{
					code:     freeBitmapCOWErrInsufficientResourceBudget,
					resource: freeBitmapResourceAvailableSlots,
					required: targetLen + 1,
					actual:   len(bindingTargets),
				}
			}
			bindingTargets[targetLen] = bindingIndex
			targetLen++
		}
	}
	if targetLen == 0 {
		return 0, freeBitmapCOWError{}
	}
	checkpoint, poolProblem := cow.pool.begin()
	if poolProblem.failed() {
		return 0, bitmapPoolError(poolProblem)
	}
	for targetIndex := 0; targetIndex < targetLen; targetIndex++ {
		binding := cow.arenaBindings[bindingTargets[targetIndex]]
		slot := &cow.pool.slots[binding.poolSlot]
		if poolProblem = cow.pool.unbindPage(
			checkpoint, cow.scope, slot.pageNumber,
		); poolProblem.failed() {
			return 0, rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
		}
	}
	if poolProblem = cow.pool.commit(checkpoint); poolProblem.failed() {
		return 0, rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	if problem := cow.synchronizeScopedBindingsForCandidatePrefix(cow.scope, 0); problem.failed() {
		return 0, problem
	}
	return targetLen, freeBitmapCOWError{}
}

func promoteFinalizationEmptyRoot(cow *freeBitmapCOW, pageNumber uint32) freeBitmapCOWError {
	if cow == nil || cow.root != 0 || cow.mutationEpoch == ^uint64(0) {
		return freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan, page: pageNumber}
	}
	checkpoint, poolProblem := cow.pool.begin()
	if poolProblem.failed() {
		return bitmapPoolError(poolProblem)
	}
	token, poolProblem := cow.pool.claimPageInScope(
		checkpoint, cow.scope, pageNumber, privatePageOwnerBitmap, privatePageBitmap,
	)
	if poolProblem.failed() {
		return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	// The shadow owns fixed output pages for COW encoding. Reuse one here so
	// promoting a legal empty root stays within the caller-reserved workspace.
	page := &cow.outputs[0]
	clear(page[:])
	writeFreeBitmapHeader(
		page, PageTypeBitmapLeaf, cow.pendingTxn, 0, 0, bitmapLeafLower,
	)
	if poolProblem = cow.pool.writePageInScope(cow.scope, token, page); poolProblem.failed() {
		return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	if poolProblem = cow.pool.setCommittedOriginInScope(cow.scope, token, 0); poolProblem.failed() {
		return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	if poolProblem = cow.pool.commit(checkpoint); poolProblem.failed() {
		return rollbackDisposableBitmapShadow(cow.pool, checkpoint, bitmapPoolError(poolProblem))
	}
	if problem := cow.synchronizeScopedBindingsForCandidatePrefix(
		cow.scope, cow.selectedCandidateTarget(),
	); problem.failed() {
		return problem
	}
	cow.root = pageNumber
	cow.mutationEpoch++
	return freeBitmapCOWError{}
}

func runFreeBitmapFinalizationFixedPoint(
	shadow *freeBitmapCOW,
	scratch freeBitmapFinalizationScratch,
) (freeBitmapFinalizationRelease, freeBitmapCOWError) {
	clear(scratch.cleanup.targets)
	limit, ok := checkedIntAdd(shadow.scopeCapacity, shadow.scopeCapacity)
	if !ok {
		return freeBitmapFinalizationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	limit, ok = checkedIntAdd(limit, freeBitmapPathCapacity+1)
	if !ok {
		return freeBitmapFinalizationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrCoverageOverflow}
	}
	result := freeBitmapFinalizationRelease{}
	for iteration := 0; iteration < limit; iteration++ {
		oldTail := shadow.pool.pendingPageCount
		newTail := finalizationProjectedTail(shadow)
		releaseLen, problem := collectFinalizationReleasePages(
			shadow, scratch.releasePages, scratch.indexStack, newTail,
		)
		if problem.failed() {
			return freeBitmapFinalizationRelease{}, problem
		}
		if releaseLen == 0 && newTail == oldTail {
			if shadow.availableLen != 0 {
				return freeBitmapFinalizationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
			if problem := terminalizeFinalizationShadow(shadow); problem.failed() {
				return freeBitmapFinalizationRelease{}, problem
			}
			releasedFreeBindings, problem := unbindFinalizationShadowReleasedFree(
				shadow, scratch.cleanup.targets,
			)
			if problem.failed() {
				return freeBitmapFinalizationRelease{}, problem
			}
			result.releasedFreeBindings = releasedFreeBindings
			result.pendingPageCount = shadow.pool.pendingPageCount
			return result, freeBitmapCOWError{}
		}
		if source, ok := shadow.committed.(*freeBitmapFinalizationCachedSource); ok && !source.sealed {
			source.armFailureFence(shadow)
		}
		plannedStart, plannedLen := 0, releaseLen
		var prepared preparedFreeBitmapInsertion
		for {
			preflight, preflightProblem := newFreeBitmapInsertPreflight(
				shadow, scratch.releasePages[plannedStart:plannedStart+plannedLen], newTail, scratch.insertPages,
			)
			if preflightProblem.failed() {
				return freeBitmapFinalizationRelease{}, preflightProblem
			}
			prepared, problem = preflight.plan()
			if !problem.failed() {
				break
			}
			if problem.code != freeBitmapCOWErrInsufficientResourceBudget ||
				problem.resource != freeBitmapResourceArenaPages || plannedLen == 0 {
				return freeBitmapFinalizationRelease{}, problem
			}
			deficit := problem.required - problem.actual
			if deficit <= 0 {
				deficit = 1
			}
			if deficit > plannedLen {
				deficit = plannedLen
			}
			plannedStart += deficit
			plannedLen -= deficit
		}
		if plannedLen == 0 && releaseLen != 0 && shadow.root == 0 {
			if problem = promoteFinalizationEmptyRoot(
				shadow, scratch.releasePages[0],
			); problem.failed() {
				return freeBitmapFinalizationRelease{}, problem
			}
			continue
		}
		committed, reclaimed, appended, problem := countFinalizationReleaseKinds(
			shadow, scratch.releasePages[plannedStart:plannedStart+plannedLen],
		)
		if problem.failed() {
			return freeBitmapFinalizationRelease{}, problem
		}
		if newTail != oldTail {
			prepared.releaseTailFrom = newTail
		}
		if _, problem = shadow.applyPreparedFreeBitmapInsertion(prepared); problem.failed() {
			return freeBitmapFinalizationRelease{}, problem
		}
		if problem = unbindFinalizationShadowTail(shadow, oldTail, newTail); problem.failed() {
			return freeBitmapFinalizationRelease{}, problem
		}
		result.reinsertedCandidates += committed + prepared.autoReinsertedCandidates
		result.reinsertedReclaimed += reclaimed
		result.reinsertedAppended += appended + prepared.autoReinsertedAppended
		result.truncatedAppended += int(oldTail - newTail)
	}
	return freeBitmapFinalizationRelease{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleInsertionPlan}
}

func validateFreeBitmapFinalizationSourceFailure(
	p *freeBitmapReservationAttachment,
	seal freeBitmapFinalizationLiveSeal,
	cache *freeBitmapFinalizationCachedSource,
) freeBitmapCOWError {
	if cache == nil {
		return staleFreeBitmapReservationBind()
	}
	if cache.problem.failed() {
		return cache.problem
	}
	if !seal.matches(p) {
		return staleFreeBitmapReservationBind()
	}
	if problem := cache.validate(); problem.failed() {
		return problem
	}
	if !cache.failure.matches() {
		return staleFreeBitmapReservationBind()
	}
	if problem := p.validateStageBuffers(nil); problem.failed() {
		return problem
	}
	return freeBitmapCOWError{}
}

func (p *freeBitmapReservationAttachment) preflightFinalizationApply(
	seal freeBitmapFinalizationLiveSeal,
	shadow *freeBitmapCOW,
	released freeBitmapFinalizationRelease,
	cleanupScratch freeBitmapCleanupScratch,
	cleanupSeal freeBitmapCleanupScratchSeal,
) (preparedFreeBitmapFinalization, freeBitmapCOWError) {
	if shadow == nil || !seal.matches(p) {
		return preparedFreeBitmapFinalization{}, staleFreeBitmapReservationBind()
	}
	if !cleanupSeal.matches(cleanupScratch) {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for _, node := range cleanupScratch.nodes {
		if node != (freeBitmapCleanupOverlayNode{}) {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	for _, value := range cleanupScratch.path {
		if value != 0 {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	releasedFreeBindings := released.releasedFreeBindings
	if releasedFreeBindings < 0 || releasedFreeBindings > p.privatePages ||
		releasedFreeBindings > len(cleanupScratch.targets) {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	previousReleasedBinding := -1
	for targetIndex, bindingIndex := range cleanupScratch.targets {
		if targetIndex < releasedFreeBindings {
			if bindingIndex <= previousReleasedBinding || bindingIndex < 0 || bindingIndex >= p.privatePages {
				return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
			}
			previousReleasedBinding = bindingIndex
		} else if bindingIndex != 0 {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	if problem := shadow.validateScopedBindings(); problem.failed() {
		return preparedFreeBitmapFinalization{}, problem
	}
	shadowAnchor, poolProblem := shadow.pool.validateScope(shadow.scope)
	if poolProblem.failed() || shadow.availableLen != 0 || shadow.pool.pendingPageCount != shadow.pageCount {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	livePool := p.cow.pool
	liveAnchor, poolProblem := livePool.validateScope(p.scope)
	if poolProblem.failed() || liveAnchor.scopeCapacity != p.privatePages ||
		shadowAnchor.scopeCapacity != p.privatePages || liveAnchor.scopeGeneration == ^uint64(0) {
		return preparedFreeBitmapFinalization{}, bitmapPoolError(poolProblem)
	}
	liveTail := livePool.pendingPageCount
	newTail := shadow.pool.pendingPageCount
	if newTail > liveTail || liveTail-newTail > uint64(p.privatePages) {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	tailTargetLen := int(liveTail - newTail)
	if tailTargetLen > len(cleanupScratch.targets)-releasedFreeBindings {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsufficientResourceBudget,
			resource: freeBitmapResourceAvailableSlots,
			required: tailTargetLen + releasedFreeBindings,
			actual:   len(cleanupScratch.targets),
		}
	}
	releasedTargetIndex := 0
	tailTargetsSeen := 0
	boundLen := 0
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		liveBinding := p.cow.arenaBindings[bindingIndex]
		shadowBinding := shadow.arenaBindings[bindingIndex]
		if liveBinding.poolSlot < 0 || liveBinding.poolSlot >= len(livePool.slots) ||
			shadowBinding.poolSlot != bindingIndex {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		live := &livePool.slots[liveBinding.poolSlot]
		desired := &shadow.pool.slots[bindingIndex]
		if live.epoch == ^uint64(0) || !live.bound || live.scopeID != p.scope.id ||
			live.scopeAnchorIndex != p.scope.anchor || live.pageNumber != liveBinding.pageNumber {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber}
		}
		releasedTarget := releasedTargetIndex < releasedFreeBindings &&
			cleanupScratch.targets[releasedTargetIndex] == bindingIndex
		if releasedTarget {
			releasedTargetIndex++
		}
		tailTarget := live.authorization == privatePageAppended &&
			uint64(live.pageNumber) >= newTail && uint64(live.pageNumber) < liveTail
		if !desired.bound {
			if releasedTarget == tailTarget {
				return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber}
			}
			if tailTarget {
				tailTargetsSeen++
			}
			continue
		}
		if releasedTarget || tailTarget || desired.pageNumber != live.pageNumber ||
			desired.authorization != live.authorization || desired.state != privatePageInUse ||
			!desired.inUse || desired.pendingTxn != p.cow.pendingTxn {
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: desired.pageNumber}
		}
		boundLen++
	}
	if releasedTargetIndex != releasedFreeBindings || tailTargetsSeen != tailTargetLen ||
		p.privatePages-boundLen != tailTargetLen+releasedFreeBindings {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	steps := uint64(p.privatePages) + 1
	if poolProblem = livePool.requireMutationSteps(steps); poolProblem.failed() {
		return preparedFreeBitmapFinalization{}, bitmapPoolError(poolProblem)
	}
	checkpoint, poolProblem := livePool.preflightCheckpoint()
	if poolProblem.failed() {
		return preparedFreeBitmapFinalization{}, bitmapPoolError(poolProblem)
	}
	nonce, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		return preparedFreeBitmapFinalization{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	for targetIndex := releasedFreeBindings - 1; targetIndex >= 0; targetIndex-- {
		bindingIndex := cleanupScratch.targets[targetIndex]
		cleanupScratch.targets[tailTargetLen+targetIndex] = p.cow.arenaBindings[bindingIndex].poolSlot
	}
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		binding := p.cow.arenaBindings[bindingIndex]
		live := &livePool.slots[binding.poolSlot]
		if live.authorization != privatePageAppended ||
			uint64(live.pageNumber) < newTail || uint64(live.pageNumber) >= liveTail {
			continue
		}
		targetIndex := int(liveTail - 1 - uint64(live.pageNumber))
		if targetIndex < 0 || targetIndex >= tailTargetLen {
			cleanupScratch.clear()
			return preparedFreeBitmapFinalization{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: live.pageNumber,
			}
		}
		cleanupScratch.targets[targetIndex] = binding.poolSlot
	}
	targetLen := tailTargetLen + releasedFreeBindings
	deletes, problem := preparePrivatePageDeletes(
		livePool, p.scope, cleanupScratch, targetLen, boundLen,
	)
	if problem.failed() {
		cleanupScratch.clear()
		return preparedFreeBitmapFinalization{}, problem
	}
	if problem = prepareSparseRetainedPrivatePageRefreshes(
		livePool, p.scope, &deletes, p.cow.arenaBindings,
		shadow.pool.slots,
	); problem.failed() {
		cleanupScratch.clear()
		return preparedFreeBitmapFinalization{}, problem
	}
	if problem = normalizePreparedPrivatePageReferences(
		livePool, p.scope, &deletes,
	); problem.failed() {
		cleanupScratch.clear()
		return preparedFreeBitmapFinalization{}, problem
	}
	if problem = validatePreparedPrivatePageCheckpointTouches(
		livePool, p.scope, &deletes,
	); problem.failed() {
		cleanupScratch.clear()
		return preparedFreeBitmapFinalization{}, problem
	}
	released.pendingPageCount = newTail
	return preparedFreeBitmapFinalization{
		checkpoint: checkpoint, shadow: shadow, newTail: newTail,
		boundLen: boundLen, tailTargetLen: tailTargetLen,
		nonce: nonce, released: released, deletes: deletes,
	}, freeBitmapCOWError{}
}

func (pool *privatePagePool) copyFinalizedSlotForCheckpointTerminalPrepared(
	checkpoint privatePagePoolCheckpoint,
	index int,
	desired *privatePagePoolSlot,
) {
	slot := &pool.slots[index]
	pool.remember(index, checkpoint)
	slot.state, slot.inUse = desired.state, desired.inUse
	slot.owner, slot.origin = desired.owner, desired.origin
	slot.pendingTxn = desired.pendingTxn
	if desired.state == privatePageInUse {
		slot.generation = checkpoint.generation
	} else {
		slot.generation = 0
	}
	slot.committedOrigin, slot.bytes = desired.committedOrigin, desired.bytes
	slot.pendingReturnState = 0
	slot.epoch++
	pool.advanceMutationPrepared()
}

func (p *freeBitmapReservationAttachment) applyPreparedFinalization(
	prepared preparedFreeBitmapFinalization,
) freeBitmapFinalizationResult {
	pool := p.cow.pool
	pool.beginCheckpointPrepared(prepared.checkpoint)
	pool.applyPreparedPrivatePageDeleteTrees(prepared.checkpoint, p.scope, prepared.deletes)
	for targetIndex := 0; targetIndex < prepared.deletes.targetLen; targetIndex++ {
		pool.unbindPreparedPrivatePageTarget(
			prepared.checkpoint, p.scope, prepared.deletes.scratch.targets[targetIndex],
			targetIndex < prepared.tailTargetLen,
		)
	}
	for bindingIndex := 0; bindingIndex < p.privatePages; bindingIndex++ {
		if !prepared.shadow.pool.slots[bindingIndex].bound {
			continue
		}
		realSlot := p.cow.arenaBindings[bindingIndex].poolSlot
		pool.copyFinalizedSlotForCheckpointTerminalPrepared(
			prepared.checkpoint, realSlot, &prepared.shadow.pool.slots[bindingIndex],
		)
	}
	anchor := &pool.slots[p.scope.anchor]
	anchor.scopeGeneration++
	anchor.scopeSealed = true
	anchor.scopeSuccessor = prepared.nonce
	anchor.successorConsumed = false
	pool.advanceMutationPrepared()
	sealedScope := p.scope
	sealedScope.generation = anchor.scopeGeneration
	p.terminalWork = pool.commitCheckpointInScopeTerminalPrepared(prepared.checkpoint, p.scope)
	p.applyPreparedCOWState(prepared.shadow)
	p.cow.candidateLen = 0
	p.cow.plannedCandidateLen = 0
	p.cow.selectedCandidateLen = 0
	p.cow.candidateSelectionSet = false
	p.cow.reservationPlanned = false
	p.cow.payloadPageBudget = 0
	p.cow.plannedRequiredPrivatePages = 0
	p.cow.availableLen = 0
	clear(p.cow.availableSlots)
	p.cow.pageCount = pool.pendingPageCount
	p.cow.pageCountsDistinct = p.cow.committedPageCount != p.cow.pageCount
	p.compactFinalizedBindings(prepared.boundLen)
	output := sealedFreeBitmapOutput{
		committed: p.cow.committed, selectedTxn: p.cow.selectedTxn, pendingTxn: p.cow.pendingTxn,
		committedPageCount: p.cow.committedPageCount, pageCount: p.cow.pageCount, root: p.cow.root,
		pool: pool, scope: sealedScope, bindings: p.cow.arenaBindings, boundLen: prepared.boundLen,
		indexNodes: p.cow.indexNodes, indexRoot: p.cow.indexRoot,
		cleanupScratch:     prepared.deletes.scratch,
		cleanupScratchSeal: sealFreeBitmapCleanupScratch(prepared.deletes.scratch),
	}
	return freeBitmapFinalizationResult{
		output:              output,
		successor:           freeBitmapFinalizationSuccessorSeed{output: output, nonce: prepared.nonce},
		released:            prepared.released.unusedReservationRelease,
		reinsertedReclaimed: prepared.released.reinsertedReclaimed,
	}
}

func (p *freeBitmapReservationAttachment) compactFinalizedBindings(boundLen int) {
	write := 0
	for read := 0; read < p.privatePages; read++ {
		binding := p.cow.arenaBindings[read]
		if !binding.bound {
			continue
		}
		binding.storageNode = write
		binding.activeNode = write
		p.cow.arenaBindings[write] = binding
		write++
	}
	clear(p.cow.arenaBindings[write:])
	clear(p.cow.indexNodes)
	p.cow.indexRoot = bitmapCOWNoIndex
	p.cow.indexLen = 0
	for bindingIndex := 0; bindingIndex < boundLen; bindingIndex++ {
		binding := &p.cow.arenaBindings[bindingIndex]
		pageIndexInsertExistingPrechecked(
			p.cow.indexNodes, &p.cow.indexRoot, bindingIndex, binding.pageNumber,
			indexedBitmapPage{kind: indexedBitmapPageArena, slot: binding.poolSlot},
		)
		p.cow.indexLen++
	}
}

// previewTerminalReplacements runs the same two-pass bitmap finalization in
// the attachment's detached shadow storage, but never applies the result to
// the live scope. It is the range-root fixed-point's read-only bitmap input.
// The caller owns output; no error writes to it.
func (p *freeBitmapReservationAttachment) previewTerminalReplacements(
	scratch freeBitmapFinalizationScratch,
	output []uint32,
) (int, freeBitmapCOWError) {
	seal, problem := captureFreeBitmapFinalizationLiveSeal(p)
	if problem.failed() {
		return 0, problem
	}
	if problem = validateFreeBitmapFinalizationScratch(p, scratch); problem.failed() {
		return 0, problem
	}
	if len(output) < len(p.cow.replacements) {
		return 0, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsufficientResourceBudget,
			resource: freeBitmapResourceReplacementPages,
			required: len(p.cow.replacements), actual: len(output),
		}
	}
	for _, live := range [9][]uint32{
		p.cow.candidates,
		p.cow.replacements,
		p.buffers.poolValidation,
		p.buffers.candidates,
		p.buffers.replacements,
		p.buffers.stage.poolValidation,
		p.buffers.stage.replacements,
		p.cow.singleInsertPage[:],
		p.buffers.stage.cow.singleInsertPage[:],
	} {
		if reservationSlicesOverlap(output, live) {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	if (p.reclamationRequest.ticket != nil &&
		reservationSlicesOverlap(output, p.reclamationRequest.ticket.pages)) ||
		reservationSlicesOverlap(output, scratch.releasePages) {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}

	// The replay cleanup is only useful to a real finalization apply. Keeping
	// it canonical here makes the supplied scratch immediately reusable.
	defer scratch.cleanup.clear()
	cacheKey, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		return 0, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	clear(scratch.cachedPages)
	cache := scratch.cache
	*cache = freeBitmapFinalizationCachedSource{
		base: p.cow.committed, cow: &p.cow, pages: scratch.cachedPages,
		root: bitmapCOWNoIndex, stack: scratch.indexStack, sealKey: cacheKey,
	}

	// The discovery pass is allowed to read the committed source into the
	// cache, but all COW and pool mutation remains in the detached stage.
	discovery, problem := p.buildFinalizationShadow(cache)
	if problem.failed() {
		if problem.code == freeBitmapCOWErrSource {
			if fenceProblem := validateFreeBitmapFinalizationSourceFailure(p, seal, cache); fenceProblem.failed() {
				return 0, fenceProblem
			}
		}
		return 0, problem
	}
	discovered, problem := runFreeBitmapFinalizationFixedPoint(discovery, scratch)
	if problem.failed() {
		if cache.problem.failed() {
			return 0, cache.problem
		}
		if problem.code == freeBitmapCOWErrSource {
			if fenceProblem := validateFreeBitmapFinalizationSourceFailure(p, seal, cache); fenceProblem.failed() {
				return 0, fenceProblem
			}
		}
		return 0, problem
	}
	scratch.cleanup.clear()
	discoveryRoot := discovery.root
	discoveryPageCount := discovery.pageCount
	if problem = cache.validate(); problem.failed() {
		return 0, problem
	}
	cacheFingerprint := finalizationCacheFingerprint(cache.pages[:cache.length])
	discoverySeal := sealFreeBitmapReservationCOW(discovery)
	discoveryFingerprint := freeBitmapReservationCOWFingerprint(discovery)
	discoveryPoolSeal := sealFreeBitmapReservationPool(discovery.pool)
	discoveryScopeFingerprint := freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope)
	if cache.base != nil {
		status := cache.checkAccessStatus()
		if cache.problem.failed() {
			return 0, cache.problem
		}
		// The content/source fence must complete before a source status is
		// returned, so corruption cannot be hidden by the final callback.
		if !seal.matches(p) ||
			finalizationCacheFingerprint(cache.pages[:cache.length]) != cacheFingerprint ||
			!finalizationCOWSealMatches(discoverySeal, discovery) ||
			freeBitmapReservationCOWFingerprint(discovery) != discoveryFingerprint ||
			!discoveryPoolSeal.matches(discovery.pool) ||
			freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope) != discoveryScopeFingerprint {
			return 0, staleFreeBitmapReservationBind()
		}
		if status.failed() {
			return 0, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	} else if !seal.matches(p) ||
		finalizationCacheFingerprint(cache.pages[:cache.length]) != cacheFingerprint ||
		!finalizationCOWSealMatches(discoverySeal, discovery) ||
		freeBitmapReservationCOWFingerprint(discovery) != discoveryFingerprint ||
		!discoveryPoolSeal.matches(discovery.pool) ||
		freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope) != discoveryScopeFingerprint {
		return 0, staleFreeBitmapReservationBind()
	}
	cache.sealed = true
	shadow, problem := p.buildFinalizationShadow(cache)
	if problem.failed() {
		return 0, problem
	}
	released, problem := runFreeBitmapFinalizationFixedPoint(shadow, scratch)
	if problem.failed() {
		return 0, problem
	}
	if released != discovered || shadow.root != discoveryRoot ||
		shadow.pageCount != discoveryPageCount || cache.problem.failed() {
		return 0, freeBitmapCOWError{
			code: freeBitmapCOWErrStaleInsertionPlan, page: shadow.root, previousPage: discoveryRoot,
			pageCount: shadow.pageCount,
		}
	}
	replacements := shadow.replacementPages()
	if len(replacements) > len(output) {
		return 0, freeBitmapCOWError{
			code:     freeBitmapCOWErrInsufficientResourceBudget,
			resource: freeBitmapResourceReplacementPages,
			required: len(replacements), actual: len(output),
		}
	}
	copy(output, replacements)
	return len(replacements), freeBitmapCOWError{}
}

// finalize resolves the complete scope in shadow and has no fallible branch
// after the first live mutation.
func (p *freeBitmapReservationAttachment) finalize(
	scratch freeBitmapFinalizationScratch,
) (freeBitmapFinalizationResult, freeBitmapCOWError) {
	seal, problem := captureFreeBitmapFinalizationLiveSeal(p)
	if problem.failed() {
		return freeBitmapFinalizationResult{}, problem
	}
	if problem = validateFreeBitmapFinalizationScratch(p, scratch); problem.failed() {
		return freeBitmapFinalizationResult{}, problem
	}
	cleanupSeal := sealFreeBitmapCleanupScratch(scratch.cleanup)
	cacheKey, ok := mintFreeBitmapFinalizationNonce()
	if !ok {
		return freeBitmapFinalizationResult{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	clear(scratch.cachedPages)
	cache := scratch.cache
	*cache = freeBitmapFinalizationCachedSource{
		base: p.cow.committed, cow: &p.cow, pages: scratch.cachedPages,
		root: bitmapCOWNoIndex, stack: scratch.indexStack, sealKey: cacheKey,
	}
	// The discovery pass may call the committed source for paths that were not
	// known until exact reclaimed identities were bound. It mutates stage only.
	discovery, problem := p.buildFinalizationShadow(cache)
	if problem.failed() {
		if problem.code == freeBitmapCOWErrSource {
			if fenceProblem := validateFreeBitmapFinalizationSourceFailure(p, seal, cache); fenceProblem.failed() {
				return freeBitmapFinalizationResult{}, fenceProblem
			}
		}
		return freeBitmapFinalizationResult{}, problem
	}
	discovered, problem := runFreeBitmapFinalizationFixedPoint(discovery, scratch)
	if problem.failed() {
		scratch.cleanup.clear()
		if cache.problem.failed() {
			return freeBitmapFinalizationResult{}, cache.problem
		}
		if problem.code == freeBitmapCOWErrSource {
			if fenceProblem := validateFreeBitmapFinalizationSourceFailure(p, seal, cache); fenceProblem.failed() {
				return freeBitmapFinalizationResult{}, fenceProblem
			}
		}
		return freeBitmapFinalizationResult{}, problem
	}
	scratch.cleanup.clear()
	discoveryRoot := discovery.root
	discoveryPageCount := discovery.pageCount
	if problem := cache.validate(); problem.failed() {
		return freeBitmapFinalizationResult{}, problem
	}
	cacheFingerprint := finalizationCacheFingerprint(cache.pages[:cache.length])
	discoverySeal := sealFreeBitmapReservationCOW(discovery)
	discoveryFingerprint := freeBitmapReservationCOWFingerprint(discovery)
	discoveryPoolSeal := sealFreeBitmapReservationPool(discovery.pool)
	discoveryScopeFingerprint := freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope)
	if cache.base != nil {
		status := cache.checkAccessStatus()
		if cache.problem.failed() {
			return freeBitmapFinalizationResult{}, cache.problem
		}
		// Corruption is more important than a callback failure. Run the complete
		// persistent-state fence before exposing the source status.
		if !seal.matches(p) ||
			finalizationCacheFingerprint(cache.pages[:cache.length]) != cacheFingerprint ||
			!finalizationCOWSealMatches(discoverySeal, discovery) ||
			freeBitmapReservationCOWFingerprint(discovery) != discoveryFingerprint ||
			!discoveryPoolSeal.matches(discovery.pool) ||
			freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope) != discoveryScopeFingerprint {
			return freeBitmapFinalizationResult{}, staleFreeBitmapReservationBind()
		}
		if status.failed() {
			return freeBitmapFinalizationResult{}, freeBitmapCOWError{code: freeBitmapCOWErrSource, source: status}
		}
	} else if !seal.matches(p) ||
		finalizationCacheFingerprint(cache.pages[:cache.length]) != cacheFingerprint ||
		!finalizationCOWSealMatches(discoverySeal, discovery) ||
		freeBitmapReservationCOWFingerprint(discovery) != discoveryFingerprint ||
		!discoveryPoolSeal.matches(discovery.pool) ||
		freeBitmapReservationScopeFingerprint(discovery.pool, discovery.scope) != discoveryScopeFingerprint {
		return freeBitmapFinalizationResult{}, staleFreeBitmapReservationBind()
	}
	cache.sealed = true
	shadow, problem := p.buildFinalizationShadow(cache)
	if problem.failed() {
		return freeBitmapFinalizationResult{}, problem
	}
	released, problem := runFreeBitmapFinalizationFixedPoint(shadow, scratch)
	if problem.failed() {
		scratch.cleanup.clear()
		return freeBitmapFinalizationResult{}, problem
	}
	if released != discovered || shadow.root != discoveryRoot ||
		shadow.pageCount != discoveryPageCount || cache.problem.failed() {
		scratch.cleanup.clear()
		return freeBitmapFinalizationResult{}, freeBitmapCOWError{
			code: freeBitmapCOWErrStaleInsertionPlan, page: shadow.root, previousPage: discoveryRoot,
			pageCount: shadow.pageCount,
		}
	}
	prepared, problem := p.preflightFinalizationApply(
		seal, shadow, released, scratch.cleanup, cleanupSeal,
	)
	if problem.failed() {
		scratch.cleanup.clear()
		return freeBitmapFinalizationResult{}, problem
	}
	result := p.applyPreparedFinalization(prepared)
	scratch.cleanup.clear()
	return result, freeBitmapCOWError{}
}

func (output sealedFreeBitmapOutput) checkAccessStatus() pageSourceStatus {
	if output.pool == nil {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	if _, problem := output.pool.validateSealedScope(output.scope); problem.failed() {
		return pageSourceStatus{code: pageSourceErrForkedHandle}
	}
	if output.committed != nil {
		return output.committed.checkAccessStatus()
	}
	return pageSourceStatus{}
}

func (output sealedFreeBitmapOutput) findIndexedPage(pageNumber uint32) (int, bool) {
	index := output.indexRoot
	for visited := 0; visited <= len(output.indexNodes); visited++ {
		if index == bitmapCOWNoIndex {
			return 0, false
		}
		if index < 0 || index >= len(output.indexNodes) {
			return bitmapCOWNoIndex, false
		}
		node := &output.indexNodes[index]
		switch {
		case pageNumber < node.pageNumber:
			index = node.left
		case pageNumber > node.pageNumber:
			index = node.right
		default:
			return index, true
		}
	}
	return bitmapCOWNoIndex, false
}

func (output sealedFreeBitmapOutput) readPageStatus(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	if destination == nil || output.pool == nil || pageNumber < 2 || uint64(pageNumber) >= output.pageCount {
		return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
	}
	if _, problem := output.pool.validateSealedScope(output.scope); problem.failed() {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	nodeIndex, found := output.findIndexedPage(pageNumber)
	if nodeIndex == bitmapCOWNoIndex {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	if found {
		node := &output.indexNodes[nodeIndex]
		if node.page.kind != indexedBitmapPageArena || nodeIndex >= output.boundLen {
			return pageSourceStatus{code: pageSourceErrPageOutOfBounds, page: pageNumber}
		}
		binding := output.bindings[nodeIndex]
		if !binding.bound || binding.activeNode != nodeIndex || binding.storageNode != nodeIndex ||
			binding.poolSlot != node.page.slot || binding.poolSlot < 0 || binding.poolSlot >= len(output.pool.slots) {
			return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
		}
		slot := &output.pool.slots[binding.poolSlot]
		if !slot.bound || slot.pageNumber != pageNumber || slot.epoch != binding.poolEpoch ||
			slot.scopeID != output.scope.id || slot.scopeAnchorIndex != output.scope.anchor ||
			slot.state != privatePageInUse || slot.owner != privatePageOwnerBitmap || slot.origin != privatePageBitmap {
			return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
		}
		*destination = slot.bytes
		return pageSourceStatus{}
	}
	if uint64(pageNumber) >= output.committedPageCount || output.committed == nil {
		return pageSourceStatus{code: pageSourceErrForkedHandle, page: pageNumber}
	}
	return output.committed.readPageStatus(pageNumber, destination)
}

func (output sealedFreeBitmapOutput) readPage(pageNumber uint32, destination *[PageSize]byte) pageSourceStatus {
	return output.readPageStatus(pageNumber, destination)
}

func (seed freeBitmapFinalizationSuccessorSeed) consume() (freeBitmapFinalizationPredecessor, freeBitmapCOWError) {
	if seed.nonce == 0 || seed.output.pool == nil {
		return freeBitmapFinalizationPredecessor{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	anchor, problem := seed.output.pool.validateSealedScope(seed.output.scope)
	if problem.failed() || anchor.scopeSuccessor != seed.nonce || anchor.successorConsumed {
		return freeBitmapFinalizationPredecessor{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	if poolProblem := seed.output.pool.requireMutationSteps(1); poolProblem.failed() {
		return freeBitmapFinalizationPredecessor{}, bitmapPoolError(poolProblem)
	}
	anchor.successorConsumed = true
	seed.output.pool.advanceMutationPrepared()
	return freeBitmapFinalizationPredecessor{output: seed.output, nonce: seed.nonce}, freeBitmapCOWError{}
}

func (predecessor freeBitmapFinalizationPredecessor) preflightCleanup() (preparedSealedFreeBitmapCleanup, freeBitmapCOWError) {
	output := predecessor.output
	if output.pool == nil || output.boundLen < 0 || output.boundLen > len(output.bindings) {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	anchor, poolProblem := output.pool.validateSealedScope(output.scope)
	if poolProblem.failed() || anchor.scopeCapacity != len(output.bindings) ||
		anchor.scopeBound != output.boundLen || output.pool.activeScopes <= 0 ||
		!anchor.successorConsumed || predecessor.nonce == 0 || anchor.scopeSuccessor != predecessor.nonce ||
		!output.pool.validUnscopedVacancyHeader() {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrStaleReservationPredecessor}
	}
	if !output.cleanupScratchSeal.matches(output.cleanupScratch) ||
		!freeBitmapCleanupScratchCanonical(output.cleanupScratch) ||
		!validateFreeBitmapCleanupScratchAliases(output.cleanupScratch, nil) {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	previous := uint32(0)
	member := anchor.scopeMemberHead
	bindingIndex := 0
	memberCount := 0
	vacantCount := 0
	for member != privatePagePoolNoIndex {
		if memberCount >= len(output.bindings) || member < 0 || member >= len(output.pool.slots) {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		slot := &output.pool.slots[member]
		if slot.scopeID != output.scope.id ||
			slot.scopeAnchorIndex != output.scope.anchor ||
			slot.scopeAnchor != (member == output.scope.anchor) {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
			}
		}
		if !slot.bound {
			if !output.pool.validScopedVacancySlot(output.scope, member) {
				return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
					code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
				}
			}
			if slot.epoch == ^uint64(0) {
				return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
					code: freeBitmapCOWErrMutationEpochExhausted, page: slot.pageNumber,
				}
			}
			vacantCount++
			memberCount++
			member = slot.scopeMemberNext
			continue
		}
		if bindingIndex >= output.boundLen {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
			}
		}
		binding := output.bindings[bindingIndex]
		if binding.poolSlot != member || !binding.bound ||
			binding.storageNode != bindingIndex || binding.activeNode != bindingIndex {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
				code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber,
			}
		}
		requiredEpochs := uint64(2)
		if slot.epoch > ^uint64(0)-requiredEpochs {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{
				code: freeBitmapCOWErrMutationEpochExhausted, page: slot.pageNumber,
			}
		}
		if binding.poolEpoch != slot.epoch || binding.pageNumber != slot.pageNumber ||
			(bindingIndex != 0 && slot.pageNumber <= previous) {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict, page: slot.pageNumber}
		}
		previous = slot.pageNumber
		bindingIndex++
		memberCount++
		member = slot.scopeMemberNext
	}
	if memberCount != len(output.bindings) || bindingIndex != output.boundLen ||
		vacantCount != len(output.bindings)-output.boundLen {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	for suffix := output.boundLen; suffix < len(output.bindings); suffix++ {
		if output.bindings[suffix] != (bitmapCOWArenaBinding{}) {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
	}
	vacant := anchor.scopeVacantHead
	for visited := 0; visited < vacantCount; visited++ {
		if !output.pool.validScopedVacancySlot(output.scope, vacant) {
			return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
		}
		vacant = output.pool.slots[vacant].scopeVacantNext
	}
	if vacant != privatePagePoolNoIndex ||
		(vacantCount == 0 && anchor.scopeVacantHead != privatePagePoolNoIndex) {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrArenaPageConflict}
	}
	steps, ok := checkedIntAdd(anchor.scopeCapacity, output.boundLen)
	if !ok {
		return preparedSealedFreeBitmapCleanup{}, freeBitmapCOWError{code: freeBitmapCOWErrMutationEpochExhausted}
	}
	if poolProblem = output.pool.requireMutationSteps(uint64(steps)); poolProblem.failed() {
		return preparedSealedFreeBitmapCleanup{}, bitmapPoolError(poolProblem)
	}
	checkpoint, poolProblem := output.pool.preflightCheckpoint()
	if poolProblem.failed() {
		return preparedSealedFreeBitmapCleanup{}, bitmapPoolError(poolProblem)
	}
	for targetIndex := 0; targetIndex < output.boundLen; targetIndex++ {
		bindingIndex := output.boundLen - 1 - targetIndex
		output.cleanupScratch.targets[targetIndex] = output.bindings[bindingIndex].poolSlot
	}
	deletes, problem := preparePrivatePageDeletes(
		output.pool, output.scope, output.cleanupScratch, output.boundLen, 0,
	)
	if problem.failed() {
		output.cleanupScratch.clear()
		return preparedSealedFreeBitmapCleanup{}, problem
	}
	if problem = normalizePreparedPrivatePageReferences(
		output.pool, output.scope, &deletes,
	); problem.failed() {
		output.cleanupScratch.clear()
		return preparedSealedFreeBitmapCleanup{}, problem
	}
	if problem = validatePreparedPrivatePageCheckpointTouches(
		output.pool, output.scope, &deletes,
	); problem.failed() {
		output.cleanupScratch.clear()
		return preparedSealedFreeBitmapCleanup{}, problem
	}
	return preparedSealedFreeBitmapCleanup{
		checkpoint: checkpoint, output: output, deletes: deletes,
	}, freeBitmapCOWError{}
}

func (pool *privatePagePool) closeSealedScopeTerminalPrepared(scope privatePageReservationScope) {
	anchor := &pool.slots[scope.anchor]
	member := anchor.scopeMemberHead
	for member != privatePagePoolNoIndex {
		slot := &pool.slots[member]
		next := slot.scopeMemberNext
		slot.unscopedPrevious = pool.unscopedVacantTail
		slot.unscopedNext = privatePagePoolNoIndex
		if pool.unscopedVacantTail == privatePagePoolNoIndex {
			pool.unscopedVacantHead = member
		} else {
			pool.slots[pool.unscopedVacantTail].unscopedNext = member
		}
		pool.unscopedVacantTail = member
		pool.unscopedVacantCount++
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
		pool.advanceMutationPrepared()
		member = next
	}
	pool.activeScopes--
}

func (predecessor freeBitmapFinalizationPredecessor) cleanup() freeBitmapCOWError {
	prepared, problem := predecessor.preflightCleanup()
	if problem.failed() {
		return problem
	}
	defer prepared.deletes.scratch.clear()
	output := predecessor.output
	pool := output.pool
	pool.beginCheckpointPrepared(prepared.checkpoint)
	pool.applyPreparedPrivatePageDeleteTrees(prepared.checkpoint, output.scope, prepared.deletes)
	for targetIndex := 0; targetIndex < prepared.deletes.targetLen; targetIndex++ {
		pool.unbindPreparedPrivatePageTarget(
			prepared.checkpoint, output.scope, prepared.deletes.scratch.targets[targetIndex], false,
		)
	}
	pool.commitCheckpointInScopeTerminalPrepared(prepared.checkpoint, output.scope)
	pool.closeSealedScopeTerminalPrepared(output.scope)
	return freeBitmapCOWError{}
}
