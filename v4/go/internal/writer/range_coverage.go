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
type unionState struct {
	lastFrom tree.Key
	hasLast  bool
	order    unionOrder
	edge     tree.PrivateEdge
	hasEdge  bool
}

func (s *unionState) isGeneral() bool { return s.order == unionOrderGeneral }

func (s *unionState) startGeneral() {
	s.lastFrom = tree.Key{}
	s.hasLast = false
	s.order = unionOrderGeneral
	s.edge = tree.PrivateEdge{}
	s.hasEdge = false
}

// plan returns the order and edge direction the new from proves (Rust
// UnionState::plan; hasDirection=false means no direction evidence yet).
func (s *unionState) plan(from tree.Key) (unionOrder, tree.Edge, bool) {
	if !s.hasLast {
		return unionOrderUnknown, tree.EdgeFirst, false
	}
	previous := s.lastFrom
	switch s.order {
	case unionOrderUnknown:
		if from.Less(previous) {
			return unionOrderFirst, tree.EdgeFirst, true
		}
		if previous.Less(from) {
			return unionOrderLast, tree.EdgeLast, true
		}
		return unionOrderUnknown, tree.EdgeFirst, false
	case unionOrderFirst:
		if !previous.Less(from) {
			return unionOrderFirst, tree.EdgeFirst, true
		}
	case unionOrderLast:
		if !from.Less(previous) {
			return unionOrderLast, tree.EdgeLast, true
		}
	}
	return unionOrderGeneral, tree.EdgeFirst, false
}

// finish records the accepted range and its edge (Rust UnionState::finish).
func (s *unionState) finish(from tree.Key, order unionOrder, edge *tree.PrivateEdge) {
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
type orderedPrefix struct {
	state     orderedPrefixState
	builder   rangeBulkBuilder
	addresses format.Cardinality129
	finished  format.Cardinality129
	hasBuilt  bool
}

// UnionInput is the streamed coverage input of one feed workflow (Rust
// UnionInput): the queued pending range, the union state, the ordered
// prefix, and the lazy assignment input for the general fallback. The
// input is created once per workflow and lives for all of its ranges, so
// none of its fields allocate per record.
type UnionInput struct {
	pending    rangeRecord
	hasPending bool
	pendingGap tree.Edge
	hasGap     bool
	union      unionState
	ordered    orderedPrefix
	valueKind  uint8
	family     uint8
	assignment privateInput
}

// NewUnionInput starts one coverage input for a workflow of the given
// value kind and address family (Rust UnionInput::new; the assignment
// input stays lazy until the input proves general).
func NewUnionInput(family uint8, valueKind uint8, maxHeapBytes uint64) UnionInput {
	return UnionInput{
		valueKind: valueKind,
		family:    family,
		assignment: privateInput{
			probeLocator:        true,
			pendingLocatorBytes: unionLocatorBytes(family, maxHeapBytes),
			adaptive:            true,
			family:              family,
		},
	}
}

// isGeneral reports the input fell back to the general assignment path
// (Rust UnionInput::is_general).
func (u *UnionInput) isGeneral() bool { return u.union.isGeneral() }

// orderedAddresses returns the sealed ordered-prefix address count, when
// one was built (Rust UnionInput::ordered_addresses).
func (u *UnionInput) orderedAddresses() (format.Cardinality129, bool) {
	if u.ordered.state == orderedFinished && u.ordered.hasBuilt {
		return u.ordered.finished, true
	}
	return format.CardinalityZero(), false
}

func (u *UnionInput) enableGeneral() {
	u.assignment.enable()
}

// startGeneral flips the union state to general and enables the
// assignment input (Rust UnionInput::start_general).
func (u *UnionInput) startGeneral() {
	u.union.startGeneral()
	u.assignment.enable()
}

// pushOrdered appends one pending range to the ordered prefix when the
// input is still strictly ascending, reporting handled=true when the
// range was consumed by the prefix (Rust UnionInput::push_ordered).
func (u *UnionInput) pushOrdered(ctx *rangeCtx, r rangeRecord) (changed bool, handled bool, err error) {
	if u.ordered.state == orderedAvailable {
		if *ctx.root != 0 || *ctx.count != 0 {
			u.ordered = orderedPrefix{state: orderedFinished}
			return false, false, nil
		}
		provenAscending := false
		if u.hasPending {
			next := u.pending
			provenAscending = r.to.Less(next.from) &&
				(r.value != next.value || !touchesFamily(u.family, r.to, next.from))
		}
		if !provenAscending {
			u.ordered = orderedPrefix{state: orderedFinished}
			return false, false, nil
		}
		u.ordered = orderedPrefix{
			state:     orderedBuilding,
			addresses: format.CardinalityZero(),
		}
		u.ordered.builder.init(ctx.store.TargetTxn(), u.valueKind, u.family)
	}
	if u.ordered.state != orderedBuilding {
		return false, false, nil
	}
	pushed, err := u.ordered.builder.tryPush(ctx.storeView, r)
	if err != nil {
		return false, false, err
	}
	if pushed {
		count, err := familyInclusiveCardinality(u.family, r.from, r.to)
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
	u.ordered = orderedPrefix{state: orderedFinished}
	u.startGeneral()
	return false, false, nil
}

// finishOrdered seals the built prefix into the coverage tree and keeps
// its address count (Rust UnionInput::finish_ordered).
func (u *UnionInput) finishOrdered(ctx *rangeCtx) (bool, error) {
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
	u.ordered = orderedPrefix{state: orderedFinished, finished: addresses, hasBuilt: true}
	return true, nil
}

// queue merges one incoming range into the pending range when they are
// same-value and touching; otherwise the previous pending range is
// returned for application and the incoming range becomes pending with
// the gap evidence between them (Rust UnionInput::queue). apply=false
// reports the incoming range was absorbed by the pending range.
func (u *UnionInput) queue(incoming rangeRecord) (pending rangeRecord, knownGap tree.Edge, hasGap bool, apply bool) {
	if !u.hasPending {
		u.pending = incoming
		u.hasPending = true
		return rangeRecord{}, tree.EdgeFirst, false, false
	}
	pending = u.pending
	touching := false
	if !incoming.from.Less(pending.from) {
		touching = touchesFamily(u.family, pending.to, incoming.from)
	} else {
		touching = touchesFamily(u.family, incoming.to, pending.from)
	}
	if pending.value == incoming.value && touching {
		extendsTowardPrevious := false
		if u.hasGap {
			switch u.pendingGap {
			case tree.EdgeFirst:
				extendsTowardPrevious = pending.to.Less(incoming.to)
			case tree.EdgeLast:
				extendsTowardPrevious = incoming.from.Less(pending.from)
			}
		}
		if extendsTowardPrevious {
			u.hasGap = false
		}
		if incoming.from.Less(pending.from) {
			pending.from = incoming.from
		}
		if pending.to.Less(incoming.to) {
			pending.to = incoming.to
		}
		u.pending = pending
		u.hasPending = true
		work.RangeCoalesced(1)
		return rangeRecord{}, tree.EdgeFirst, false, false
	}
	knownGap = u.pendingGap
	hasGap = u.hasGap
	u.pending = incoming
	u.hasPending = true
	if touching {
		u.hasGap = false
	} else if pending.from.Less(incoming.from) {
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
func (u *UnionInput) takePending() (rangeRecord, tree.Edge, bool, bool) {
	if !u.hasPending {
		return rangeRecord{}, tree.EdgeFirst, false, false
	}
	pending := u.pending
	gap := u.pendingGap
	hasGap := u.hasGap
	u.pending = rangeRecord{}
	u.hasPending = false
	u.hasGap = false
	return pending, gap, hasGap, true
}

// pushPrivateUntracked feeds one coverage range into the workflow tree
// (Rust push_private_untracked): the general path applies it through the
// assignment input, otherwise it is queued, proven ascending, or applied
// through the union state.
func pushPrivateUntracked(ctx *rangeCtx, from, to tree.Key, value uint32, input *UnionInput) (bool, error) {
	ctx.markUntracked()
	if input.union.isGeneral() {
		return unionPrivateUntrackedGeneral(ctx, from, to, value, &input.assignment)
	}
	if to.Less(from) {
		return false, invalid("range start is after its end")
	}
	pending, knownGap, hasGap, apply := input.queue(rangeRecord{from: from, to: to, value: value})
	if !apply {
		return false, nil
	}
	wasGeneral := input.union.isGeneral()
	if changed, handled, err := input.pushOrdered(ctx, pending); err != nil {
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
			c, err := unionPrivateUntrackedGeneral(ctx, p.from, p.to, p.value, &input.assignment)
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
func finishInputUntracked(ctx *rangeCtx, input *UnionInput) (bool, error) {
	ctx.markUntracked()
	changed := false
	if p, knownGap, hasGap, has := input.takePending(); has {
		if orderedChanged, handled, err := input.pushOrdered(ctx, p); err != nil {
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
func applyPending(ctx *rangeCtx, pending rangeRecord, knownGap tree.Edge, hasGap bool, input *UnionInput) (bool, error) {
	if input.union.isGeneral() {
		return unionPrivateUntrackedGeneral(ctx, pending.from, pending.to, pending.value, &input.assignment)
	}
	return unionPrivateUntrackedGap(ctx, pending, knownGap, hasGap, &input.union)
}

// unionPrivateUntrackedGap applies one range through the monotonic union
// state (Rust union_private_untracked_gap).
func unionPrivateUntrackedGap(ctx *rangeCtx, incoming rangeRecord, knownGap tree.Edge, hasGap bool, state *unionState) (bool, error) {
	if incoming.to.Less(incoming.from) {
		return false, invalid("range start is after its end")
	}
	return applyPrivate(ctx, incoming, state, knownGap, hasGap)
}

// unionPrivateUntrackedGeneral applies one range through the general
// assignment input (Rust union_private_untracked_general).
func unionPrivateUntrackedGeneral(ctx *rangeCtx, from, to tree.Key, value uint32, input *privateInput) (bool, error) {
	if to.Less(from) {
		return false, invalid("range start is after its end")
	}
	incoming := rangeRecord{from: from, to: to, value: value}
	if input.disabled() {
		return applyGeneral(ctx, incoming)
	}
	switch result, err := insertPrivateInputGap(ctx, incoming, input); {
	case err != nil:
		return false, err
	case result.inserted:
		return true, nil
	default:
		changed, _, _, err := mergeRejected(ctx, incoming, result.reject)
		return changed, err
	}
}

// finishPrivateUntracked flushes the pending union edge (Rust
// finish_private_untracked / finish_private).
func finishPrivateUntracked(ctx *rangeCtx, state *unionState) error {
	if !state.hasEdge {
		return nil
	}
	if err := tree.FlushEdge(ctx.family, ctx.store, ctx.root, &state.edge); err != nil {
		return err
	}
	return nil
}

// insertPrivateEdge inserts one range at a cached tree edge (Rust
// insert_private_edge).
func insertPrivateEdge(ctx *rangeCtx, incoming rangeRecord, position *tree.PrivateEdge, edge tree.Edge, knownGap tree.Edge, hasGap bool) (tree.EdgeInsert[rangeRecord], error) {
	cell, err := ctx.encodeRecord(0, incoming)
	if err != nil {
		return tree.EdgeInsert[rangeRecord]{}, err
	}
	gap := privateGap{family: ctx.family, r: incoming}
	result, err := tree.InsertIfEdgeGap(ctx.family, ctx.store, ctx.root, cell, position, edge, hasGap && knownGap == edge, gap)
	if err != nil {
		return tree.EdgeInsert[rangeRecord]{}, err
	}
	if result.Inserted {
		if err := rangeRecordAdded(ctx, incoming.value); err != nil {
			return tree.EdgeInsert[rangeRecord]{}, err
		}
	}
	return result, nil
}

// applyPrivate applies one range through the monotonic union state (Rust
// apply_private).
func applyPrivate(ctx *rangeCtx, incoming rangeRecord, state *unionState, knownGap tree.Edge, hasGap bool) (bool, error) {
	if state.order == unionOrderGeneral {
		return applyGeneral(ctx, incoming)
	}
	order, direction, hasDirection := state.plan(incoming.from)
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
	var rejected tree.LocalReject[rangeRecord]
	var hasRejected bool
	if hasCached {
		result, err := insertPrivateEdge(ctx, incoming, &state.edge, direction, knownGap, hasGap)
		if err != nil {
			return false, err
		}
		if result.Inserted {
			state.finish(incoming.from, order, result.Edge)
			return true, nil
		}
		rejected, hasRejected = result.Reject, result.Rejected
	} else {
		result, err := insertPrivateGap(ctx, incoming)
		if err != nil {
			return false, err
		}
		if result.Inserted {
			var edge *tree.PrivateEdge
			if wasEmpty {
				e := tree.RootEdge(*ctx.root)
				edge = &e
			}
			state.finish(incoming.from, order, edge)
			return true, nil
		}
		rejected, hasRejected = result.Reject, result.Rejected
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
	state.finish(incoming.from, order, edge)
	return changed, nil
}

// applyGeneral applies one range through the general merge path (Rust
// apply_general).
func applyGeneral(ctx *rangeCtx, incoming rangeRecord) (bool, error) {
	result, err := insertPrivateGap(ctx, incoming)
	if err != nil {
		return false, err
	}
	if result.Inserted {
		return true, nil
	}
	if !result.Rejected {
		return false, corrupt("general union gap rejection is missing")
	}
	changed, _, _, err := mergeRejected(ctx, incoming, result.Reject)
	return changed, err
}

// mergeRejected completes one rejected union range (Rust merge_rejected):
// the local union plan replaces a touching neighbor run in place when
// possible; otherwise the general run covers the external sides (Rust
// union_run).
func mergeRejected(ctx *rangeCtx, incoming rangeRecord, rejected tree.LocalReject[rangeRecord]) (bool, tree.PrivatePosition, bool, error) {
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
	if err := tree.ReplaceLocalRun(ctx.family, ctx.store, ctx.root, rejected, decision.run, cell); err != nil {
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
type localUnionDecision struct {
	noChange bool
	run      tree.LocalRun
	merged   rangeRecord
	removed  uint64
}

// localUnionPlan proves the rejected gap can be replaced locally (Rust
// local_union_plan): ok=false sends the range to the general run.
func localUnionPlan(family rangeFamily, rejected tree.LocalReject[rangeRecord], incoming rangeRecord) (localUnionDecision, bool) {
	predecessor, hasPredecessor := rejected.Predecessor()
	if hasPredecessor && !predecessor.to.Less(incoming.to) {
		return localUnionDecision{noChange: true}, true
	}
	usePredecessor := hasPredecessor && touchesCodec(family, predecessor.to, incoming.from)
	if !usePredecessor && !hasPredecessor && !rejected.PredecessorComplete() {
		return localUnionDecision{}, false
	}
	merged := incoming
	if usePredecessor {
		merged.from = predecessor.from
		if merged.to.Less(predecessor.to) {
			merged.to = predecessor.to
		}
	}
	successor, hasSuccessor := rejected.Successor()
	useSuccessor := hasSuccessor && touchesCodec(family, merged.to, successor.from)
	if useSuccessor {
		if successor.to.Less(merged.to) {
			return localUnionDecision{}, false
		}
		merged.to = successor.to
	} else if !hasSuccessor && !rejected.SuccessorComplete() {
		return localUnionDecision{}, false
	}
	switch {
	case usePredecessor && useSuccessor:
		return localUnionDecision{run: tree.LocalRunBoth, merged: merged, removed: 2}, true
	case usePredecessor:
		return localUnionDecision{run: tree.LocalRunPredecessor, merged: merged, removed: 1}, true
	case useSuccessor:
		return localUnionDecision{run: tree.LocalRunSuccessor, merged: merged, removed: 1}, true
	default:
		return localUnionDecision{}, false
	}
}

// touchesCodec reports one range boundary touches the next boundary
// (Rust touches): the ranges overlap or are adjacent in the address
// space.
func touchesCodec(family rangeFamily, leftTo, rightFrom tree.Key) bool {
	if !leftTo.Less(rightFrom) {
		return true
	}
	next, ok := family.Next(leftTo)
	return ok && next.Equal(rightFrom)
}

// touchesFamily is touchesCodec over the address-family byte, using the
// nextKey helper of the ordered builder.
func touchesFamily(family uint8, leftTo, rightFrom tree.Key) bool {
	if !leftTo.Less(rightFrom) {
		return true
	}
	next, ok := nextKey(family, leftTo)
	return ok && next.Equal(rightFrom)
}

// unionRun covers the rejected range against the external sides and
// inserts the merged run (Rust union_run). hasPosition reports the
// rejected position is reusable as the next edge.
func unionRun(ctx *rangeCtx, incoming rangeRecord, rejected tree.LocalReject[rangeRecord]) (bool, tree.PrivatePosition, bool, error) {
	predecessor, hasPredecessor := rejected.Predecessor()
	if !hasPredecessor && !rejected.PredecessorComplete() {
		var err error
		predecessor, hasPredecessor, err = tree.ExternalPredecessor(ctx.family, ctx.store, rejected.Target.Path)
		if err != nil {
			return false, tree.PrivatePosition{}, false, err
		}
	}
	if hasPredecessor && !predecessor.to.Less(incoming.to) {
		return false, rejected.IntoPosition(), true, nil
	}
	merged := incoming
	// first is the selected side record the run starts at (Rust first):
	// the touching predecessor, or else the touching successor. The
	// remove loop below starts at exactly that record's key, never at a
	// key the tree may not own.
	runStart := tree.Key{}
	hasFirst := false
	if hasPredecessor && touchesCodec(ctx.family, predecessor.to, incoming.from) {
		merged.from = predecessor.from
		if merged.to.Less(predecessor.to) {
			merged.to = predecessor.to
		}
		runStart = merged.from
		hasFirst = true
	} else {
		successor, hasSuccessor := rejected.Successor()
		if !hasSuccessor && !rejected.SuccessorComplete() {
			var err error
			successor, hasSuccessor, err = tree.ExternalSuccessor(ctx.family, ctx.store, rejected.Target.Path)
			if err != nil {
				return false, tree.PrivatePosition{}, false, err
			}
		}
		if hasSuccessor && touchesCodec(ctx.family, merged.to, successor.from) {
			runStart = successor.from
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
		result, err := tree.RemoveLeafRun(ctx.family, ctx.store, ctx.root, nextStart, func(r rangeRecord) (bool, error) {
			if r.value != incoming.value {
				return false, corrupt("constant-value tree contains another value")
			}
			if !touchesCodec(ctx.family, merged.to, r.from) {
				return false, nil
			}
			if r.from.Less(merged.from) {
				merged.from = r.from
			}
			if merged.to.Less(r.to) {
				merged.to = r.to
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
		if result.Following == nil {
			break
		}
		if !touchesCodec(ctx.family, merged.to, result.Following.Leaf.from) {
			break
		}
		nextStart = result.Following.Key
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
func (ctx *rangeCtx) markUntracked() { ctx.untracked = true }
