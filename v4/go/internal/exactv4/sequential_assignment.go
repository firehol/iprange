package exactv4

import (
	"encoding/binary"
	"fmt"
)

const (
	sequentialAssignmentNodeBytes    = 32
	sequentialAssignmentNodesPerPage = PageSize / sequentialAssignmentNodeBytes
)

const sequentialAssignmentNodeNone uint64 = ^uint64(0)

type sequentialAssignmentMode uint8

const (
	sequentialAssignmentNone sequentialAssignmentMode = iota
	sequentialAssignmentValue
	sequentialAssignmentClear
)

type sequentialAssignmentTag struct {
	ordinal uint64
	mode    sequentialAssignmentMode
	value   uint32
}

type sequentialAssignmentNode struct {
	tag         sequentialAssignmentTag
	left, right uint64
}

type sequentialAssignmentNodeRef uint64

func (r sequentialAssignmentNodeRef) none() bool { return uint64(r) == sequentialAssignmentNodeNone }

func newSequentialAssignmentNodeRef(page, node int) (sequentialAssignmentNodeRef, error) {
	if page < 0 || uint64(page) > uint64(^uint32(0)) || node < 0 || node >= sequentialAssignmentNodesPerPage {
		return 0, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspacePageLimit}
	}
	return sequentialAssignmentNodeRef(uint64(uint32(page))<<32 | uint64(node)), nil
}

func (r sequentialAssignmentNodeRef) parts() (page, node int, ok bool) {
	if r.none() {
		return 0, 0, false
	}
	page = int(uint64(r) >> 32)
	node = int(uint32(r))
	return page, node, node < sequentialAssignmentNodesPerPage
}

type sequentialAssignmentPage struct {
	bytes [PageSize]byte
	used  uint16
}

// sequentialAssignmentWorkspace is caller-owned fixed node storage. Its
// logical pages are never private-pool or physical v4 pages.
type sequentialAssignmentWorkspace struct {
	pages []sequentialAssignmentPage
}

func newSequentialAssignmentWorkspace(pages []sequentialAssignmentPage) sequentialAssignmentWorkspace {
	return sequentialAssignmentWorkspace{pages: pages}
}

func (w *sequentialAssignmentWorkspace) clean() bool {
	if w == nil {
		return false
	}
	// Every node slot is fully initialized before its first read, so setup
	// checks only occupancy and does not scan caller memory as input data.
	for _, page := range w.pages {
		if page.used != 0 {
			return false
		}
	}
	return true
}

func (w *sequentialAssignmentWorkspace) reset() {
	if w == nil {
		return
	}
	clear(w.pages)
}

// discardAfterAbort clears unpublished node bytes only after the enclosing
// draft has been abandoned.
func (w *sequentialAssignmentWorkspace) discardAfterAbort() { w.reset() }

type sequentialAssignmentErrorCode uint8

const (
	sequentialAssignmentErrBornTransactionZero sequentialAssignmentErrorCode = iota + 1
	sequentialAssignmentErrWorkspaceBusy
	sequentialAssignmentErrWorkspacePageLimit
	sequentialAssignmentErrAssignmentBudget
	sequentialAssignmentErrWorkBudget
	sequentialAssignmentErrMutationBudget
	sequentialAssignmentErrOrdinalExhausted
	sequentialAssignmentErrRangeReversed
	sequentialAssignmentErrMembershipValueZero
	sequentialAssignmentErrInvalidNodeReference
	sequentialAssignmentErrInvalidNodeEncoding
	sequentialAssignmentErrInvalidValueKind
	sequentialAssignmentErrFailed
)

type sequentialAssignmentError struct {
	code     sequentialAssignmentErrorCode
	required uint64
	actual   uint64
}

func (e *sequentialAssignmentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 sequential assignment: error %d", e.code)
}

// sequentialAssignmentEngine applies direct/membership logical assignments in
// arrival order. A later prefix tag wins only on the addresses it covers; old
// descendants stay in private storage until the final ordered walk resolves
// their ordinal relationship.
type sequentialAssignmentEngine[K rangeKey[K]] struct {
	workspace      *sequentialAssignmentWorkspace
	bornTxn        uint64
	valueKind      ValueKind
	maxAssignments uint64
	maxWork        uint64
	maxMutations   uint64
	assignments    uint64
	work           uint64
	mutations      uint64
	pageCount      int
	ordinal        uint64
	root           sequentialAssignmentNodeRef
	finished       bool
	failed         bool
}

func newSequentialAssignmentEngine[K rangeKey[K]](
	workspace *sequentialAssignmentWorkspace,
	bornTxn uint64,
	valueKind ValueKind,
	maxAssignments, maxWork, maxMutations uint64,
) (sequentialAssignmentEngine[K], error) {
	if bornTxn == 0 {
		return sequentialAssignmentEngine[K]{}, &sequentialAssignmentError{code: sequentialAssignmentErrBornTransactionZero}
	}
	if workspace == nil {
		return sequentialAssignmentEngine[K]{}, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspaceBusy}
	}
	if uint64(len(workspace.pages)) > uint64(^uint32(0)) {
		return sequentialAssignmentEngine[K]{}, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspacePageLimit}
	}
	if !workspace.clean() {
		return sequentialAssignmentEngine[K]{}, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspaceBusy}
	}
	if !validValueKind(valueKind) {
		return sequentialAssignmentEngine[K]{}, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidValueKind}
	}
	return sequentialAssignmentEngine[K]{
		workspace: workspace, bornTxn: bornTxn, valueKind: valueKind,
		maxAssignments: maxAssignments, maxWork: maxWork, maxMutations: maxMutations,
		root: sequentialAssignmentNodeRef(sequentialAssignmentNodeNone),
	}, nil
}

func (e *sequentialAssignmentEngine[K]) assign(from, to K, value uint32) error {
	if e.finished || e.workspace == nil {
		return &sequentialAssignmentError{code: sequentialAssignmentErrWorkspaceBusy}
	}
	if e.failed {
		return &sequentialAssignmentError{code: sequentialAssignmentErrFailed}
	}
	if e.valueKind == ValueKindMembership && value == 0 {
		return e.fail(&sequentialAssignmentError{code: sequentialAssignmentErrMembershipValueZero})
	}
	return e.apply(from, to, sequentialAssignmentValue, value)
}

func (e *sequentialAssignmentEngine[K]) clear(from, to K) error {
	return e.apply(from, to, sequentialAssignmentClear, 0)
}

func (e *sequentialAssignmentEngine[K]) apply(from, to K, mode sequentialAssignmentMode, value uint32) error {
	if e.finished || e.workspace == nil {
		return &sequentialAssignmentError{code: sequentialAssignmentErrWorkspaceBusy}
	}
	if e.failed {
		return &sequentialAssignmentError{code: sequentialAssignmentErrFailed}
	}
	if from.compare(to) > 0 {
		return e.fail(&sequentialAssignmentError{code: sequentialAssignmentErrRangeReversed})
	}
	if e.assignments == e.maxAssignments {
		return e.fail(&sequentialAssignmentError{
			code: sequentialAssignmentErrAssignmentBudget, required: nextSequentialAssignmentCount(e.assignments), actual: e.maxAssignments,
		})
	}
	if e.ordinal == ^uint64(0) {
		return e.fail(&sequentialAssignmentError{code: sequentialAssignmentErrOrdinalExhausted})
	}
	tag := sequentialAssignmentTag{ordinal: e.ordinal + 1, mode: mode, value: value}
	root, err := e.applyNode(
		e.root, 0, assignmentAddress{}, assignmentAddressFromKey(from), assignmentAddressFromKey(to), tag,
	)
	if err != nil {
		return e.fail(err)
	}
	e.root = root
	e.ordinal = tag.ordinal
	e.assignments++
	return nil
}

func (e *sequentialAssignmentEngine[K]) fail(err error) error {
	e.failed = true
	return err
}

func (e *sequentialAssignmentEngine[K]) applyNode(
	reference sequentialAssignmentNodeRef,
	depth int,
	prefix, from, to assignmentAddress,
	tag sequentialAssignmentTag,
) (sequentialAssignmentNodeRef, error) {
	bits := assignmentKeyBits[K]()
	lower, upper := assignmentRegionBounds(bits, depth, prefix)
	if assignmentLess(to, lower) || assignmentLess(upper, from) {
		return reference, nil
	}
	if reference.none() {
		var err error
		reference, err = e.allocateNode()
		if err != nil {
			return 0, err
		}
	}
	node, err := e.readNode(reference)
	if err != nil {
		return 0, err
	}
	if !assignmentLess(lower, from) && !assignmentLess(to, upper) {
		node.tag = tag
		if err = e.writeNode(reference, node); err != nil {
			return 0, err
		}
		return reference, nil
	}
	if depth == bits {
		return 0, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeEncoding}
	}
	nextDepth := depth + 1
	rightPrefix := assignmentSetBit(lower, bits-nextDepth)
	_, leftUpper := assignmentRegionBounds(bits, nextDepth, lower)
	rightLower, _ := assignmentRegionBounds(bits, nextDepth, rightPrefix)
	changed := false
	if !assignmentLess(leftUpper, from) && !assignmentLess(to, lower) {
		child, childErr := e.applyNode(sequentialAssignmentNodeRef(node.left), nextDepth, lower, from, to, tag)
		if childErr != nil {
			return 0, childErr
		}
		if uint64(child) != node.left {
			node.left = uint64(child)
			changed = true
		}
	}
	if !assignmentLess(upper, from) && !assignmentLess(to, rightLower) {
		child, childErr := e.applyNode(sequentialAssignmentNodeRef(node.right), nextDepth, rightPrefix, from, to, tag)
		if childErr != nil {
			return 0, childErr
		}
		if uint64(child) != node.right {
			node.right = uint64(child)
			changed = true
		}
	}
	if changed {
		if err = e.writeNode(reference, node); err != nil {
			return 0, err
		}
	}
	return reference, nil
}

func (e *sequentialAssignmentEngine[K]) allocateNode() (sequentialAssignmentNodeRef, error) {
	if err := e.reserveMutation(); err != nil {
		return 0, err
	}
	if e.pageCount == 0 || int(e.workspace.pages[e.pageCount-1].used) == sequentialAssignmentNodesPerPage {
		if e.pageCount == len(e.workspace.pages) {
			return 0, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspacePageLimit}
		}
		e.workspace.pages[e.pageCount] = sequentialAssignmentPage{}
		e.pageCount++
	}
	page := e.pageCount - 1
	node := int(e.workspace.pages[page].used)
	reference, err := newSequentialAssignmentNodeRef(page, node)
	if err != nil {
		return 0, err
	}
	e.workspace.pages[page].used++
	if err = e.writeNodeReserved(reference, sequentialAssignmentNode{
		left: sequentialAssignmentNodeNone, right: sequentialAssignmentNodeNone,
	}); err != nil {
		return 0, err
	}
	return reference, nil
}

func (e *sequentialAssignmentEngine[K]) chargeWork() error {
	if e.work == e.maxWork {
		return &sequentialAssignmentError{code: sequentialAssignmentErrWorkBudget, required: nextSequentialAssignmentCount(e.work), actual: e.maxWork}
	}
	e.work++
	return nil
}

func (e *sequentialAssignmentEngine[K]) reserveMutation() error {
	if err := e.chargeWork(); err != nil {
		return err
	}
	if e.mutations == e.maxMutations {
		return &sequentialAssignmentError{
			code: sequentialAssignmentErrMutationBudget, required: nextSequentialAssignmentCount(e.mutations), actual: e.maxMutations,
		}
	}
	e.mutations++
	return nil
}

func (e *sequentialAssignmentEngine[K]) nodePage(reference sequentialAssignmentNodeRef) (int, int, error) {
	pageIndex, nodeIndex, ok := reference.parts()
	if !ok || pageIndex >= e.pageCount || pageIndex >= len(e.workspace.pages) {
		return 0, 0, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeReference}
	}
	page := e.workspace.pages[pageIndex]
	if nodeIndex >= int(page.used) {
		return 0, 0, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeReference}
	}
	return pageIndex, nodeIndex * sequentialAssignmentNodeBytes, nil
}

func (e *sequentialAssignmentEngine[K]) readNode(reference sequentialAssignmentNodeRef) (sequentialAssignmentNode, error) {
	if err := e.chargeWork(); err != nil {
		return sequentialAssignmentNode{}, err
	}
	pageIndex, offset, err := e.nodePage(reference)
	if err != nil {
		return sequentialAssignmentNode{}, err
	}
	return decodeSequentialAssignmentNode(e.workspace.pages[pageIndex].bytes[offset : offset+sequentialAssignmentNodeBytes])
}

func (e *sequentialAssignmentEngine[K]) writeNode(reference sequentialAssignmentNodeRef, node sequentialAssignmentNode) error {
	if err := e.reserveMutation(); err != nil {
		return err
	}
	return e.writeNodeReserved(reference, node)
}

func (e *sequentialAssignmentEngine[K]) writeNodeReserved(reference sequentialAssignmentNodeRef, node sequentialAssignmentNode) error {
	pageIndex, offset, err := e.nodePage(reference)
	if err != nil {
		return err
	}
	encodeSequentialAssignmentNode(e.workspace.pages[pageIndex].bytes[offset:offset+sequentialAssignmentNodeBytes], node)
	return nil
}

func (e *sequentialAssignmentEngine[K]) buildStagedTree(
	treeWorkspace *rangeTreeBuildWorkspace[K], staging *rangeTreeStaging[K],
) (rangeTreeStagedResult, error) {
	if e.finished {
		return rangeTreeStagedResult{}, &sequentialAssignmentError{code: sequentialAssignmentErrWorkspaceBusy}
	}
	if e.failed {
		return rangeTreeStagedResult{}, &sequentialAssignmentError{code: sequentialAssignmentErrFailed}
	}
	if staging == nil {
		return rangeTreeStagedResult{}, e.fail(&rangeTreeStagingError{code: rangeTreeStagingErrFinished})
	}
	builder, err := treeWorkspace.begin(e.bornTxn, e.valueKind, staging.logicalPageCount())
	if err != nil {
		return rangeTreeStagedResult{}, e.fail(err)
	}
	var pending rangeRecord[K]
	havePending := false
	if err = e.emitNode(e.root, 0, assignmentAddress{}, sequentialAssignmentTag{}, &builder, staging, &pending, &havePending); err != nil {
		return rangeTreeStagedResult{}, e.fail(err)
	}
	if havePending {
		if err = builder.push(staging, pending); err != nil {
			return rangeTreeStagedResult{}, e.fail(err)
		}
	}
	result, err := builder.finish(staging)
	if err != nil {
		return rangeTreeStagedResult{}, e.fail(err)
	}
	staged, err := staging.finish(result)
	if err != nil {
		return rangeTreeStagedResult{}, e.fail(err)
	}
	e.clearWorkspaceNodes()
	e.finished = true
	return staged, nil
}

func (e *sequentialAssignmentEngine[K]) emitNode(
	reference sequentialAssignmentNodeRef,
	depth int,
	prefix assignmentAddress,
	inherited sequentialAssignmentTag,
	builder *rangeTreeBuilder[K],
	sink rangeTreePageSink,
	pending *rangeRecord[K],
	havePending *bool,
) error {
	bits := assignmentKeyBits[K]()
	if reference.none() {
		return e.emitRegion(depth, prefix, inherited, builder, sink, pending, havePending)
	}
	node, err := e.readNode(reference)
	if err != nil {
		return err
	}
	effective := inherited
	if node.tag.mode != sequentialAssignmentNone && node.tag.ordinal > inherited.ordinal {
		effective = node.tag
	}
	if depth == bits || (node.left == sequentialAssignmentNodeNone && node.right == sequentialAssignmentNodeNone) {
		return e.emitRegion(depth, prefix, effective, builder, sink, pending, havePending)
	}
	lower, _ := assignmentRegionBounds(bits, depth, prefix)
	nextDepth := depth + 1
	if err = e.emitNode(sequentialAssignmentNodeRef(node.left), nextDepth, lower, effective, builder, sink, pending, havePending); err != nil {
		return err
	}
	rightPrefix := assignmentSetBit(lower, bits-nextDepth)
	return e.emitNode(sequentialAssignmentNodeRef(node.right), nextDepth, rightPrefix, effective, builder, sink, pending, havePending)
}

func (e *sequentialAssignmentEngine[K]) emitRegion(
	depth int,
	prefix assignmentAddress,
	tag sequentialAssignmentTag,
	builder *rangeTreeBuilder[K],
	sink rangeTreePageSink,
	pending *rangeRecord[K],
	havePending *bool,
) error {
	if tag.mode != sequentialAssignmentValue {
		return nil
	}
	from, to := assignmentRegionBounds(assignmentKeyBits[K](), depth, prefix)
	record := rangeRecord[K]{from: assignmentAddressToKey[K](from), to: assignmentAddressToKey[K](to), value: tag.value}
	if *havePending {
		if next, ok := pending.to.next(); ok && next.compare(record.from) == 0 && pending.value == record.value {
			pending.to = record.to
			return nil
		}
		if err := builder.push(sink, *pending); err != nil {
			return err
		}
	}
	*pending = record
	*havePending = true
	return nil
}

func (e *sequentialAssignmentEngine[K]) clearWorkspaceNodes() {
	e.workspace.reset()
	e.pageCount = 0
}

func nextSequentialAssignmentCount(value uint64) uint64 {
	if value == ^uint64(0) {
		return value
	}
	return value + 1
}

func encodeSequentialAssignmentNode(dst []byte, node sequentialAssignmentNode) {
	clear(dst)
	binary.LittleEndian.PutUint64(dst[0:8], node.tag.ordinal)
	binary.LittleEndian.PutUint64(dst[8:16], node.left)
	binary.LittleEndian.PutUint64(dst[16:24], node.right)
	binary.LittleEndian.PutUint32(dst[24:28], node.tag.value)
	dst[28] = byte(node.tag.mode)
}

func decodeSequentialAssignmentNode(src []byte) (sequentialAssignmentNode, error) {
	if len(src) != sequentialAssignmentNodeBytes || src[29] != 0 || src[30] != 0 || src[31] != 0 {
		return sequentialAssignmentNode{}, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeEncoding}
	}
	mode := sequentialAssignmentMode(src[28])
	if mode != sequentialAssignmentNone && mode != sequentialAssignmentValue && mode != sequentialAssignmentClear {
		return sequentialAssignmentNode{}, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeEncoding}
	}
	ordinal := binary.LittleEndian.Uint64(src[0:8])
	if (mode == sequentialAssignmentNone) != (ordinal == 0) {
		return sequentialAssignmentNode{}, &sequentialAssignmentError{code: sequentialAssignmentErrInvalidNodeEncoding}
	}
	return sequentialAssignmentNode{
		tag:  sequentialAssignmentTag{ordinal: ordinal, mode: mode, value: binary.LittleEndian.Uint32(src[24:28])},
		left: binary.LittleEndian.Uint64(src[8:16]), right: binary.LittleEndian.Uint64(src[16:24]),
	}, nil
}

type assignmentAddress struct{ hi, lo uint64 }

func assignmentAddressFromKey[K rangeKey[K]](key K) assignmentAddress {
	hi, lo := key.halves()
	return assignmentAddress{hi: hi, lo: lo}
}

func assignmentAddressToKey[K rangeKey[K]](address assignmentAddress) K {
	var key K
	return key.fromHalves(address.hi, address.lo)
}

func assignmentKeyBits[K rangeKey[K]]() int {
	var key K
	return key.width() * 8
}

func assignmentLess(left, right assignmentAddress) bool {
	return left.hi < right.hi || left.hi == right.hi && left.lo < right.lo
}

func assignmentSetBit(value assignmentAddress, position int) assignmentAddress {
	if position < 64 {
		value.lo |= uint64(1) << position
	} else {
		value.hi |= uint64(1) << (position - 64)
	}
	return value
}

func assignmentRegionBounds(bits, depth int, prefix assignmentAddress) (assignmentAddress, assignmentAddress) {
	if depth < 0 || depth > bits {
		panic("invalid fixed assignment depth")
	}
	remaining := bits - depth
	var suffix assignmentAddress
	switch {
	case remaining == 0:
	case remaining < 64:
		suffix.lo = uint64(1)<<remaining - 1
	case remaining == 64:
		suffix.lo = ^uint64(0)
	case remaining < 128:
		suffix.lo = ^uint64(0)
		suffix.hi = uint64(1)<<(remaining-64) - 1
	default:
		suffix.hi, suffix.lo = ^uint64(0), ^uint64(0)
	}
	lower := assignmentAddress{hi: prefix.hi &^ suffix.hi, lo: prefix.lo &^ suffix.lo}
	return lower, assignmentAddress{hi: lower.hi | suffix.hi, lo: lower.lo | suffix.lo}
}
