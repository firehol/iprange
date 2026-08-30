// Coverage-union ingestion for unordered feed ranges (Rust
// range_mutation/coverage.rs + coverage/input.rs): the streamed union
// input routes each range either into an in-memory ordered-prefix bulk
// build (when the input proves strictly ascending with no overlap) or
// into the private-tree monotonic edge/gap union state, falling back to
// the general assignment input once the input proves unordered. The
// workflow coverage tree is built untracked: every store operation
// through this file no-ops the per-record value accounting (Rust
// Untracked), because the coverage tree is internal to the feed workflow
// and its membership refcounts are accounted exactly once by the merge
// that consumes it.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// unionOrder is the monotonic-direction evidence of one union state (Rust
// UnionOrder).
type unionOrder uint8

const (
	unionOrderUnknown unionOrder = iota
	unionOrderFirst
	unionOrderLast
	unionOrderGeneral
)

// unionState is the monotonic edge-insertion state of one unordered union
// input (Rust UnionState). edge is embedded by value: the edge insert
// mutates it in place through the tree API, so the steady-state edge
// path never allocates.
type unionState[K any] struct {
	lastFrom K
	hasLast  bool
	order    unionOrder
	edge     tree.PrivateEdge
	hasEdge  bool
}

func (s *unionState[K]) isGeneral() bool { return s.order == unionOrderGeneral }

func (s *unionState[K]) startGeneral() {
	var zero K
	s.lastFrom = zero
	s.hasLast = false
	s.order = unionOrderGeneral
	s.edge = tree.PrivateEdge{}
	s.hasEdge = false
}

// plan returns the order and edge direction the new from proves (Rust
// UnionState::plan; hasDirection=false means no direction evidence yet).
func (s *unionState[K]) plan(codec rangeFamily[K], from K) (unionOrder, tree.Edge, bool) {
	if !s.hasLast {
		return unionOrderUnknown, tree.EdgeFirst, false
	}
	previous := s.lastFrom
	switch s.order {
	case unionOrderUnknown:
		if codec.Less(from, previous) {
			return unionOrderFirst, tree.EdgeFirst, true
		}
		if codec.Less(previous, from) {
			return unionOrderLast, tree.EdgeLast, true
		}
		return unionOrderUnknown, tree.EdgeFirst, false
	case unionOrderFirst:
		if !codec.Less(previous, from) {
			return unionOrderFirst, tree.EdgeFirst, true
		}
	case unionOrderLast:
		if !codec.Less(from, previous) {
			return unionOrderLast, tree.EdgeLast, true
		}
	}
	return unionOrderGeneral, tree.EdgeFirst, false
}

// finish records the accepted range and its edge (Rust UnionState::finish).
func (s *unionState[K]) finish(from K, order unionOrder, edge *tree.PrivateEdge) {
	s.lastFrom = from
	s.hasLast = true
	s.order = order
	if order == unionOrderGeneral || edge == nil {
		s.edge = tree.PrivateEdge{}
		s.hasEdge = false
	} else {
		s.edge = *edge
		s.hasEdge = true
	}
}

// orderedPrefixState is the adaptive ordered-prefix construction state
// (Rust OrderedPrefix).
type orderedPrefixState uint8

const (
	orderedAvailable orderedPrefixState = iota
	orderedBuilding
	orderedFinished
)

// orderedPrefix is the ordered-prefix state of one union input (Rust
// OrderedPrefix). builder and the address counts are disjoint across the
// states: Available retains nothing, Building retains the builder and its
// running count, Finished retains the sealed count only when a prefix was
// actually built (Rust Finished(Option<Cardinality129>)). The builder is
// embedded by value, exactly like the Rust Builder lives inside the
// OrderedPrefix variant: the ordered path therefore never allocates.
type orderedPrefix[K any] struct {
	state     orderedPrefixState
	builder   rangeBulkBuilder[K]
	addresses format.Cardinality129
	finished  format.Cardinality129
	hasBuilt  bool
}

// UnionInput[K] is the streamed coverage input of one feed workflow
// (Rust UnionInput over the family key): the queued pending range, the
// union state, the ordered prefix, and the lazy assignment input for the
// general fallback. The input is created once per workflow and lives for
// all of its ranges, so none of its fields allocate per record.
type unionInput[K any] struct {
	pending    rangeRecord[K]
	hasPending bool
	pendingGap tree.Edge
	hasGap     bool
	union      unionState[K]
	ordered    orderedPrefix[K]
	valueKind  uint8
	family     uint8
	assignment privateInput[K]
}

// newUnionInput starts one coverage input for a workflow of the given
// value kind and address family (Rust UnionInput::new; the assignment
// input stays lazy until the input proves general).
func newUnionInput[K any](codec rangeFamily[K], family uint8, valueKind uint8, maxHeapBytes uint64) unionInput[K] {
	return unionInput[K]{
		valueKind: valueKind,
		family:    family,
		assignment: privateInput[K]{
			probeLocator:        true,
			pendingLocatorBytes: unionLocatorBytes(family, maxHeapBytes),
			adaptive:            true,
			family:              codec,
		},
	}
}

// isGeneral reports the input fell back to the general assignment path
// (Rust UnionInput::is_general).
func (u *unionInput[K]) isGeneral() bool { return u.union.isGeneral() }

// orderedAddresses returns the sealed ordered-prefix address count, when
// one was built (Rust UnionInput::ordered_addresses).
func (u *unionInput[K]) orderedAddresses() (format.Cardinality129, bool) {
	if u.ordered.state == orderedFinished && u.ordered.hasBuilt {
		return u.ordered.finished, true
	}
	return format.CardinalityZero(), false
}

func (u *unionInput[K]) enableGeneral() {
	u.assignment.enable()
}

// startGeneral flips the union state to general and enables the
// assignment input (Rust UnionInput::start_general).
func (u *unionInput[K]) startGeneral() {
	u.union.startGeneral()
	u.assignment.enable()
}

// pushOrdered appends one pending range to the ordered prefix when the
// input is still strictly ascending, reporting handled=true when the
// range was consumed by the prefix (Rust UnionInput::push_ordered).
func (u *unionInput[K]) pushOrdered(ctx *rangeCtx[K], codec rangeFamily[K], r rangeRecord[K]) (changed bool, handled bool, err error) {
	if u.ordered.state == orderedAvailable {
		if *ctx.root != 0 || *ctx.count != 0 {
			u.ordered = orderedPrefix[K]{state: orderedFinished}
			return false, false, nil
		}
		provenAscending := false
		if u.hasPending {
			next := u.pending
			provenAscending = codec.Less(r.To, next.From) &&
				(r.Value != next.Value || !touchesCodec(codec, r.To, next.From))
		}
		if !provenAscending {
			u.ordered = orderedPrefix[K]{state: orderedFinished}
			return false, false, nil
		}
		u.ordered = orderedPrefix[K]{
			state:     orderedBuilding,
			addresses: format.CardinalityZero(),
		}
		u.ordered.builder.init(ctx.store.TargetTxn(), u.valueKind, ctx.family)
	}
	if u.ordered.state != orderedBuilding {
		return false, false, nil
	}
	pushed, err := u.ordered.builder.tryPush(ctx.storeView, r)
	if err != nil {
		return false, false, err
	}
	if pushed {
		count, err := familyInclusiveCardinalityOf(codec, r.From, r.To)
		if err != nil {
			return false, false, err
		}
		u.ordered.addresses, err = u.ordered.addresses.Add(count)
		if err != nil {
			return false, false, overflow("ordered range address count")
		}
		return true, true, nil
	}
	if _, err := u.finishOrdered(ctx); err != nil {
		return false, false, err
	}
	u.ordered = orderedPrefix[K]{state: orderedFinished}
	u.startGeneral()
	return false, false, nil
}

// finishOrdered seals the built prefix into the coverage tree and keeps
// its address count (Rust UnionInput::finish_ordered).
func (u *unionInput[K]) finishOrdered(ctx *rangeCtx[K]) (bool, error) {
	if u.ordered.state != orderedBuilding {
		return false, nil
	}
	if *ctx.root != 0 || *ctx.count != 0 {
		return false, corrupt("ordered range prefix has an existing destination tree")
	}
	builder := &u.ordered.builder
	addresses := u.ordered.addresses
	root, count, err := builder.finishInline(ctx.storeView)
	if err != nil {
		return false, err
	}
	*ctx.root = root
	*ctx.count = count
	u.ordered = orderedPrefix[K]{state: orderedFinished, finished: addresses, hasBuilt: true}
	return true, nil
}

// queue merges one incoming range into the pending range when they are
// same-value and touching; otherwise the previous pending range is
// returned for application and the incoming range becomes pending with
// the gap evidence between them (Rust UnionInput::queue). apply=false
// reports the incoming range was absorbed by the pending range.
func (u *unionInput[K]) queue(codec rangeFamily[K], incoming rangeRecord[K]) (pending rangeRecord[K], knownGap tree.Edge, hasGap bool, apply bool) {
	if !u.hasPending {
		u.pending = incoming
		u.hasPending = true
		return rangeRecord[K]{}, tree.EdgeFirst, false, false
	}
	pending = u.pending
	touching := false
	if !codec.Less(incoming.From, pending.From) {
		touching = touchesCodec(codec, pending.To, incoming.From)
	} else {
		touching = touchesCodec(codec, incoming.To, pending.From)
	}
	if pending.Value == incoming.Value && touching {
		extendsTowardPrevious := false
		if u.hasGap {
			switch u.pendingGap {
			case tree.EdgeFirst:
				extendsTowardPrevious = codec.Less(pending.To, incoming.To)
			case tree.EdgeLast:
				extendsTowardPrevious = codec.Less(incoming.From, pending.From)
			}
		}
		if extendsTowardPrevious {
			u.hasGap = false
		}
		if codec.Less(incoming.From, pending.From) {
			pending.From = incoming.From
		}
		if codec.Less(pending.To, incoming.To) {
			pending.To = incoming.To
		}
		u.pending = pending
		u.hasPending = true
		work.RangeCoalesced(1)
		return rangeRecord[K]{}, tree.EdgeFirst, false, false
	}
	knownGap = u.pendingGap
	hasGap = u.hasGap
	u.pending = incoming
	u.hasPending = true
	if touching {
		u.hasGap = false
	} else if codec.Less(pending.From, incoming.From) {
		u.pendingGap = tree.EdgeLast
		u.hasGap = true
	} else {
		u.pendingGap = tree.EdgeFirst
		u.hasGap = true
	}
	return pending, knownGap, hasGap, true
}

// takePending removes and returns the pending range (Rust
// UnionInput::take_pending).
func (u *unionInput[K]) takePending() (rangeRecord[K], tree.Edge, bool, bool) {
	if !u.hasPending {
		return rangeRecord[K]{}, tree.EdgeFirst, false, false
	}
	pending := u.pending
	gap := u.pendingGap
	hasGap := u.hasGap
	u.pending = rangeRecord[K]{}
	u.hasPending = false
	u.hasGap = false
	return pending, gap, hasGap, true
}

// pushPrivateUntracked feeds one coverage range into the workflow tree
// (Rust push_private_untracked): the general path applies it through the
// assignment input, otherwise it is queued, proven ascending, or applied
// through the union state.
func pushPrivateUntracked[K any](ctx *rangeCtx[K], from, to K, value uint32, input *unionInput[K]) (bool, error) {
	ctx.markUntracked()
	if input.union.isGeneral() {
		return unionPrivateUntrackedGeneral(ctx, from, to, value, &input.assignment)
	}
	if ctx.family.Less(to, from) {
		return false, invalid("range start is after its end")
	}
	pending, knownGap, hasGap, apply := input.queue(ctx.family, rangeRecord[K]{From: from, To: to, Value: value})
	if !apply {
		return false, nil
	}
	wasGeneral := input.union.isGeneral()
	if changed, handled, err := input.pushOrdered(ctx, ctx.family, pending); err != nil {
		return false, err
	} else if handled {
		return changed, nil
	}
	changed, err := applyPending(ctx, pending, knownGap, hasGap, input)
	if err != nil {
		return false, err
	}
	if !wasGeneral && input.union.isGeneral() {
		input.enableGeneral()
		if p, _, _, has := input.takePending(); has {
			c, err := unionPrivateUntrackedGeneral(ctx, p.From, p.To, p.Value, &input.assignment)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	return changed, nil
}

// finishInputUntracked drains the pending range, seals the ordered
// prefix, flushes the union edge, and releases the assignment input
// (Rust finish_input_untracked).
func finishInputUntracked[K any](ctx *rangeCtx[K], input *unionInput[K]) (bool, error) {
	ctx.markUntracked()
	changed := false
	if p, knownGap, hasGap, has := input.takePending(); has {
		if orderedChanged, handled, err := input.pushOrdered(ctx, ctx.family, p); err != nil {
			return false, err
		} else if handled {
			changed = changed || orderedChanged
		} else {
			c, err := applyPending(ctx, p, knownGap, hasGap, input)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	built, err := input.finishOrdered(ctx)
	if err != nil {
		return false, err
	}
	changed = changed || built
	if err := finishPrivateUntracked(ctx, &input.union); err != nil {
		return false, err
	}
	input.assignment.release()
	return changed, nil
}

// applyPending routes one pending range through the union state or the
// general assignment input (Rust apply_pending).
func applyPending[K any](ctx *rangeCtx[K], pending rangeRecord[K], knownGap tree.Edge, hasGap bool, input *unionInput[K]) (bool, error) {
	if input.union.isGeneral() {
		return unionPrivateUntrackedGeneral(ctx, pending.From, pending.To, pending.Value, &input.assignment)
	}
	return unionPrivateUntrackedGap(ctx, pending, knownGap, hasGap, &input.union)
}

// unionPrivateUntrackedGap applies one range through the monotonic union
// state (Rust union_private_untracked_gap).
func unionPrivateUntrackedGap[K any](ctx *rangeCtx[K], incoming rangeRecord[K], knownGap tree.Edge, hasGap bool, state *unionState[K]) (bool, error) {
	if ctx.family.Less(incoming.To, incoming.From) {
		return false, invalid("range start is after its end")
	}
	return applyPrivate(ctx, incoming, state, knownGap, hasGap)
}

// unionPrivateUntrackedGeneral applies one range through the general
// assignment input (Rust union_private_untracked_general).
func unionPrivateUntrackedGeneral[K any](ctx *rangeCtx[K], from, to K, value uint32, input *privateInput[K]) (bool, error) {
	if ctx.family.Less(to, from) {
		return false, invalid("range start is after its end")
	}
	incoming := rangeRecord[K]{From: from, To: to, Value: value}
	if input.disabled() {
		return applyGeneral(ctx, incoming)
	}
	switch result, err := insertPrivateInputGap(ctx, incoming, input); {
	case err != nil:
		return false, err
	case result.inserted:
		return true, nil
	default:
		changed, _, _, err := mergeRejected(ctx, incoming, *result.reject)
		return changed, err
	}
}

// finishPrivateUntracked flushes the pending union edge (Rust
// finish_private_untracked / finish_private).
func finishPrivateUntracked[K any](ctx *rangeCtx[K], state *unionState[K]) error {
	if !state.hasEdge {
		return nil
	}
	if err := tree.FlushEdge(ctx.family, ctx.storeView, ctx.root, &state.edge); err != nil {
		return err
	}
	return nil
}

// insertPrivateEdge inserts one range at a cached tree edge (Rust
// insert_private_edge).
func insertPrivateEdge[K any](ctx *rangeCtx[K], incoming rangeRecord[K], position *tree.PrivateEdge, edge tree.Edge, knownGap tree.Edge, hasGap bool) (tree.EdgeInsert[rangeRecord[K]], error) {
	cell, err := ctx.encodeRecord(0, incoming)
	if err != nil {
		return tree.EdgeInsert[rangeRecord[K]]{}, err
	}
	result, err := gapEdgeInsert(ctx, incoming, cell, position, edge, hasGap && knownGap == edge)
	if err != nil {
		return tree.EdgeInsert[rangeRecord[K]]{}, err
	}
	if result.Inserted {
		if err := rangeRecordAdded(ctx, incoming.Value); err != nil {
			return tree.EdgeInsert[rangeRecord[K]]{}, err
		}
	}
	return result, nil
}

// applyPrivate applies one range through the monotonic union state (Rust
// apply_private).
func applyPrivate[K any](ctx *rangeCtx[K], incoming rangeRecord[K], state *unionState[K], knownGap tree.Edge, hasGap bool) (bool, error) {
	if state.order == unionOrderGeneral {
		return applyGeneral(ctx, incoming)
	}
	order, direction, hasDirection := state.plan(ctx.family, incoming.From)
	hasCached := hasDirection && state.hasEdge
	if !hasDirection && state.hasEdge {
		if err := finishPrivateUntracked(ctx, state); err != nil {
			return false, err
		}
	}
	if hasCached {
		state.hasEdge = false
	}
	wasEmpty := *ctx.root == 0
	var rejected tree.LocalReject[rangeRecord[K]]
	var hasRejected bool
	if hasCached {
		result, err := insertPrivateEdge(ctx, incoming, &state.edge, direction, knownGap, hasGap)
		if err != nil {
			return false, err
		}
		if result.Inserted {
			state.finish(incoming.From, order, result.Edge)
			return true, nil
		}
		rejected, hasRejected = result.Reject, result.Rejected
	} else {
		var rejectedSlot tree.LocalReject[rangeRecord[K]]
		inserted, _, err := insertPrivateGap(ctx, incoming, &rejectedSlot)
		if err != nil {
			return false, err
		}
		if inserted {
			var edge *tree.PrivateEdge
			if wasEmpty {
				e := tree.RootEdge(*ctx.root)
				edge = &e
			}
			state.finish(incoming.From, order, edge)
			return true, nil
		}
		rejected, hasRejected = rejectedSlot, true
	}
	if !hasRejected {
		return false, corrupt("private union gap rejection is missing")
	}
	changed, position, hasPosition, err := mergeRejected(ctx, incoming, rejected)
	if err != nil {
		return false, err
	}
	var edge *tree.PrivateEdge
	if hasPosition {
		e := tree.ConsistentEdge(position)
		edge = &e
	}
	state.finish(incoming.From, order, edge)
	return changed, nil
}

// applyGeneral applies one range through the general merge path (Rust
// apply_general).
func applyGeneral[K any](ctx *rangeCtx[K], incoming rangeRecord[K]) (bool, error) {
	var rejectedSlot tree.LocalReject[rangeRecord[K]]
	inserted, _, err := insertPrivateGap(ctx, incoming, &rejectedSlot)
	if err != nil {
		return false, err
	}
	if inserted {
		return true, nil
	}
	changed, _, _, err := mergeRejected(ctx, incoming, rejectedSlot)
	return changed, err
}

// mergeRejected completes one rejected union range (Rust merge_rejected):
// the local union plan replaces a touching neighbor run in place when
// possible; otherwise the general run covers the external sides (Rust
// union_run).
func mergeRejected[K any](ctx *rangeCtx[K], incoming rangeRecord[K], rejected tree.LocalReject[rangeRecord[K]]) (bool, tree.PrivatePosition, bool, error) {
	decision, ok := localUnionPlan(ctx.family, rejected, incoming)
	if !ok {
		return unionRun(ctx, incoming, rejected)
	}
	if decision.noChange {
		return false, rejected.IntoPosition(), true, nil
	}
	cell, err := ctx.encodeRecord(0, decision.merged)
	if err != nil {
		return false, tree.PrivatePosition{}, false, err
	}
	if err := localRunReplace(ctx, &rejected, decision.run, cell); err != nil {
		return false, tree.PrivatePosition{}, false, err
	}
	if decision.removed < 1 || *ctx.count < decision.removed-1 {
		return false, tree.PrivatePosition{}, false, overflow("range record count")
	}
	*ctx.count -= decision.removed - 1
	work.RangeCoalesced(decision.removed)
	work.RangeEmitted(1)
	return true, rejected.IntoPosition(), true, nil
}

// localUnionDecision is the outcome of one local union plan (Rust
// LocalUnion): NoChange keeps the existing records, Replace overwrites
// one local neighbor run.
type localUnionDecision[K any] struct {
	noChange bool
	run      tree.LocalRun
	merged   rangeRecord[K]
	removed  uint64
}

// localUnionPlan proves the rejected gap can be replaced locally (Rust
// local_union_plan): ok=false sends the range to the general run.
func localUnionPlan[K any](family rangeFamily[K], rejected tree.LocalReject[rangeRecord[K]], incoming rangeRecord[K]) (localUnionDecision[K], bool) {
	predecessor, hasPredecessor := rejected.Predecessor()
	if hasPredecessor && !family.Less(predecessor.To, incoming.To) {
		return localUnionDecision[K]{noChange: true}, true
	}
	usePredecessor := hasPredecessor && touchesCodec(family, predecessor.To, incoming.From)
	if !usePredecessor && !hasPredecessor && !rejected.PredecessorComplete() {
		return localUnionDecision[K]{}, false
	}
	merged := incoming
	if usePredecessor {
		merged.From = predecessor.From
		if family.Less(merged.To, predecessor.To) {
			merged.To = predecessor.To
		}
	}
	successor, hasSuccessor := rejected.Successor()
	useSuccessor := hasSuccessor && touchesCodec(family, merged.To, successor.From)
	if useSuccessor {
		if family.Less(successor.To, merged.To) {
			return localUnionDecision[K]{}, false
		}
		merged.To = successor.To
	} else if !hasSuccessor && !rejected.SuccessorComplete() {
		return localUnionDecision[K]{}, false
	}
	switch {
	case usePredecessor && useSuccessor:
		return localUnionDecision[K]{run: tree.LocalRunBoth, merged: merged, removed: 2}, true
	case usePredecessor:
		return localUnionDecision[K]{run: tree.LocalRunPredecessor, merged: merged, removed: 1}, true
	case useSuccessor:
		return localUnionDecision[K]{run: tree.LocalRunSuccessor, merged: merged, removed: 1}, true
	default:
		return localUnionDecision[K]{}, false
	}
}

// touchesCodec reports one range boundary touches the next boundary
// (Rust touches): the ranges overlap or are adjacent in the address
// space.
func touchesCodec[K any](family rangeFamily[K], leftTo, rightFrom K) bool {
	if !family.Less(leftTo, rightFrom) {
		return true
	}
	next, ok := family.Next(leftTo)
	return ok && family.Equal(next, rightFrom)
}

// unionRun covers the rejected range against the external sides and
// inserts the merged run (Rust union_run). hasPosition reports the
// rejected position is reusable as the next edge.
func unionRun[K any](ctx *rangeCtx[K], incoming rangeRecord[K], rejected tree.LocalReject[rangeRecord[K]]) (bool, tree.PrivatePosition, bool, error) {
	predecessor, hasPredecessor := rejected.Predecessor()
	if !hasPredecessor && !rejected.PredecessorComplete() {
		var err error
		predecessor, hasPredecessor, err = tree.ExternalPredecessor(ctx.family, ctx.storeView, rejected.Target.Path)
		if err != nil {
			return false, tree.PrivatePosition{}, false, err
		}
	}
	if hasPredecessor && !ctx.family.Less(predecessor.To, incoming.To) {
		return false, rejected.IntoPosition(), true, nil
	}
	merged := incoming
	// first is the selected side record the run starts at (Rust first):
	// the touching predecessor, or else the touching successor. The
	// remove loop below starts at exactly that record's key, never at a
	// key the tree may not own.
	var runStart K
	hasFirst := false
	if hasPredecessor && touchesCodec(ctx.family, predecessor.To, incoming.From) {
		merged.From = predecessor.From
		if ctx.family.Less(merged.To, predecessor.To) {
			merged.To = predecessor.To
		}
		runStart = merged.From
		hasFirst = true
	} else {
		successor, hasSuccessor := rejected.Successor()
		if !hasSuccessor && !rejected.SuccessorComplete() {
			var err error
			successor, hasSuccessor, err = tree.ExternalSuccessor(ctx.family, ctx.storeView, rejected.Target.Path)
			if err != nil {
				return false, tree.PrivatePosition{}, false, err
			}
		}
		if hasSuccessor && touchesCodec(ctx.family, merged.To, successor.From) {
			runStart = successor.From
			hasFirst = true
		}
	}
	if !hasFirst {
		// The position is reported only when the record fit the rejected
		// leaf; a split path caches nothing, exactly like Rust
		// insert_private_rejected's Option<PrivatePosition>.
		position, fits, err := insertPrivateRejected(ctx, incoming, rejected)
		if err != nil {
			return false, tree.PrivatePosition{}, false, err
		}
		return true, position, fits, nil
	}
	nextStart := runStart
	removed := uint64(0)
	for {
		result, err := tree.RemoveLeafRun(ctx.family, ctx.storeView, ctx.root, ctx.family.KeyOf(nextStart), func(r rangeRecord[K]) (bool, error) {
			if r.Value != incoming.Value {
				return false, corrupt("constant-value tree contains another value")
			}
			if !touchesCodec(ctx.family, merged.To, r.From) {
				return false, nil
			}
			if ctx.family.Less(r.From, merged.From) {
				merged.From = r.From
			}
			if ctx.family.Less(merged.To, r.To) {
				merged.To = r.To
			}
			return true, nil
		})
		if err != nil {
			return false, tree.PrivatePosition{}, false, err
		}
		if result.Removed > ^uint64(0)-removed {
			return false, tree.PrivatePosition{}, false, overflow("coverage removed ranges")
		}
		removed += result.Removed
		if result.Removed > *ctx.count {
			return false, tree.PrivatePosition{}, false, overflow("range record count")
		}
		*ctx.count -= result.Removed
		if !result.HasFollowing {
			break
		}
		if !touchesCodec(ctx.family, merged.To, result.Following.Leaf.From) {
			break
		}
		nextStart = decodeCodecKey(ctx.family, result.Following.Key)
	}
	if removed == 0 {
		return false, tree.PrivatePosition{}, false, corrupt("coverage run did not remove its first range")
	}
	if err := insert(ctx, merged); err != nil {
		return false, tree.PrivatePosition{}, false, err
	}
	work.RangeCoalesced(removed)
	return true, tree.PrivatePosition{}, false, nil
}

// markUntracked no-ops the per-record value accounting of the context
// for the rest of the operation (Rust coverage Untracked wrapper):
// coverage ranges are internal to the workflow and never account
// per-record value refcounts. One flag replaces the per-record wrapper
// context allocation of the coverage input path.
func (ctx *rangeCtx[K]) markUntracked() { ctx.untracked = true }

// UnionInput is the public per-workflow coverage input facade (Rust
// UnionInput over the workflow address family). It owns exactly one
// family side, selected at construction; the edit arms route the
// per-family methods to that side, so the public workflows never see
// the family key types (Rust UnionInput<K>).
type UnionInput struct {
	family uint8
	v4     unionInput[key4]
	v6     unionInput[key6]
}

// NewUnionInput starts one coverage input for a workflow of the given
// value kind and address family (Rust UnionInput::new).
func NewUnionInput(family uint8, valueKind uint8, maxHeapBytes uint64) UnionInput {
	if family == format.AddressFamilyIPv4 {
		return UnionInput{family: family, v4: newUnionInput(rangeCodec4{}, family, valueKind, maxHeapBytes)}
	}
	return UnionInput{family: family, v6: newUnionInput(rangeCodec6{}, family, valueKind, maxHeapBytes)}
}

// IsGeneral reports the input fell back to the general assignment path
// (Rust UnionInput::is_general).
func (u *UnionInput) IsGeneral() bool {
	if u.family == format.AddressFamilyIPv4 {
		return u.v4.isGeneral()
	}
	return u.v6.isGeneral()
}

// isGeneral is the internal form of IsGeneral (Rust
// UnionInput::is_general).
func (u *UnionInput) isGeneral() bool {
	if u.family == format.AddressFamilyIPv4 {
		return u.v4.isGeneral()
	}
	return u.v6.isGeneral()
}

// orderedAddresses returns the sealed ordered-prefix address count, when
// one was built (Rust UnionInput::ordered_addresses).
func (u *UnionInput) orderedAddresses() (format.Cardinality129, bool) {
	if u.family == format.AddressFamilyIPv4 {
		return u.v4.orderedAddresses()
	}
	return u.v6.orderedAddresses()
}

// startGeneral flips the lazy assignment input general for the
// buffered fallback (Rust UnionInput::start_general).
func (u *UnionInput) startGeneral() {
	if u.family == format.AddressFamilyIPv4 {
		u.v4.startGeneral()
		return
	}
	u.v6.startGeneral()
}
