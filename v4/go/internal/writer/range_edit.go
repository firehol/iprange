// Ordered range-assignment semantics over the generic COW tree (Rust
// range_mutation.rs + range_mutation/assign.rs). The store owns the
// mapped pages and the value accounting; this file owns the range state
// machine only.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// RangeStore extends the retiring store with per-record value accounting
// (Rust range_mutation::RangeStore).
type RangeStore interface {
	tree.RetiringStore
	// RangeRecordAdded accounts one range record with value (Rust
	// range_record_added).
	RangeRecordAdded(value uint32) error
	// RangeRecordRemoved accounts one removed range record with value
	// (Rust range_record_removed).
	RangeRecordRemoved(value uint32) error
}

// rangeCtx[K] carries the mutable range-tree state of one draft
// operation (the Rust range_mutation function parameters: store, root,
// record_count). The draft store owns one context per family and every
// entry point resets it before the operation: the context holds
// interface fields whose method calls escape a stack context, so a
// per-record context would allocate once per record.
type rangeCtx[K any] struct {
	family rangeFamily[K]
	store  RangeStore
	// storeView is the same store pre-widened to the pure tree-core
	// surface once per edit (Rust passes the concrete store everywhere;
	// Go widens a concrete *DraftStore into tree.Store for free at edit
	// start, so the per-record builder calls pass an identical
	// interface instead of converting RangeStore to tree.Store on every
	// record, which would hit the Go runtime's cached type-assertion
	// path and its probabilistic heap cache update).
	storeView tree.Store
	root      *uint32
	count     *uint64
	// untracked no-ops the per-record value accounting of this draft
	// operation (Rust Untracked wrapper): coverage ranges are internal
	// to one workflow and never account membership refcounts. One flag
	// replaces the per-record wrapper context of the coverage input
	// path. Every entry point resets the flag before the operation and
	// the untracked entry points mark it before the first operation; a
	// marked context must never be reused for a tracked operation
	// without resetting the flag (it would silently skip membership
	// accounting).
	untracked bool
	// scratch borrows the draft-owned range encode targets (Rust
	// EncodedRange locals): the generic rangeFamily interface makes
	// stack targets escape, so the context points at the DraftStore
	// array allocated once per draft, never per record.
	scratch *[3][format.RangeRecordV6Size]byte
	// rejectSlot reuses one rejection proof across the records of one
	// operation (Rust LocalReject locals): the private-gap seam fills it
	// through a caller-owned pointer so the hot overwrite path never
	// returns the ~340-byte proof by value. The slot is consumed before
	// the next record overwrites it; operations that must retain a
	// rejection across further gap attempts use a local slot instead.
	rejectSlot tree.LocalReject[rangeRecord[K]]
}

// change is one requested range rewrite (Rust Change).
type change[K any] struct {
	from  K
	to    K
	value optionalValue
}

// encodeRecord renders one range record into the ctx-owned scratch slot
// and returns the valid byte view (Rust EncodedRange::new). The view
// stays valid until the next encode reuses the same slot.
func (ctx *rangeCtx[K]) encodeRecord(slot int, r rangeRecord[K]) ([]byte, error) {
	if slot < 0 || slot >= len(ctx.scratch) {
		return nil, corrupt("range encode slot out of range")
	}
	output := ctx.scratch[slot][:]
	n, err := ctx.family.EncodeRecord(r, output)
	if err != nil {
		return nil, err
	}
	return output[:n], nil
}

// assign replaces the [from, to] interval with one value (Rust assign).
func rangeAssign[K any](ctx *rangeCtx[K], from, to K, value uint32) (bool, error) {
	return rangeReplaceWithHint(ctx, change[K]{from: from, to: to, value: someValue(value)}, nil, false)
}

// assignPrivate inserts one range into the private tree when the physical
// gap around it is open (Rust assign_private).
func rangeAssignPrivate[K any](ctx *rangeCtx[K], from, to K, value uint32) (bool, error) {
	if ctx.family.Less(to, from) {
		return false, invalid("range start is after its end")
	}
	r := rangeRecord[K]{From: from, To: to, Value: value}
	inserted, _, err := insertPrivateGap(ctx, r, &ctx.rejectSlot)
	if err != nil {
		return false, err
	}
	if inserted {
		return true, nil
	}
	return assignWithHint(ctx, r, &ctx.rejectSlot, true)
}

// assignWithHint completes a private assignment through a previous gap
// rejection (Rust assign_with_hint). The rejection is threaded by
// pointer through the whole replace chain: the ~340-byte proof record
// is copied exactly once (out of the tree return), not at every
// wrapper frame, on the hot overwrite path.
func assignWithHint[K any](ctx *rangeCtx[K], r rangeRecord[K], hint *tree.LocalReject[rangeRecord[K]], hasHint bool) (bool, error) {
	return rangeReplaceWithHint(ctx, change[K]{from: r.From, to: r.To, value: someValue(r.Value)}, hint, hasHint)
}

// clear removes the [from, to] interval (Rust clear).
func rangeClear[K any](ctx *rangeCtx[K], from, to K) (bool, error) {
	return rangeReplaceWithHint(ctx, change[K]{from: from, to: to}, nil, false)
}

// retireTree retires every page of one private range tree (Rust
// retire_tree).
func rangeRetireTree[K any](ctx *rangeCtx[K], root uint32, checkpoint func() error) error {
	return tree.RetireTree(ctx.family, ctx.store, root, checkpoint)
}

// transform walks [from, to] in range order, applying operation to each
// present-value segment and replacing the segments whose value changed
// (Rust transform).
func rangeTransform[K any](ctx *rangeCtx[K], from, to K, operation func(store RangeStore, value optionalValue) (optionalValue, error)) (bool, error) {
	if ctx.family.Less(to, from) {
		return false, invalid("range start is after its end")
	}
	cursor := from
	changed := false
	for {
		segment, err := segmentAt(ctx, cursor, to)
		if err != nil {
			return false, err
		}
		value, err := operation(ctx.store, segment.value)
		if err != nil {
			return false, err
		}
		if !sameOptional(value, segment.value) {
			if _, err := rangeReplaceWithHint(ctx, change[K]{from: cursor, to: segment.to, value: value}, nil, false); err != nil {
				return false, err
			}
			changed = true
		}
		if ctx.family.Equal(segment.to, to) {
			return changed, nil
		}
		next, ok := ctx.family.Next(segment.to)
		if !ok {
			return false, overflow("membership range cursor")
		}
		cursor = next
	}
}

// segment is one present or absent interval of a transform walk (Rust
// Segment).
type segment[K any] struct {
	to    K
	value optionalValue
}

func segmentAt[K any](ctx *rangeCtx[K], from, to K) (segment[K], error) {
	if predecessor, hasPredecessor, err := readPredecessor(ctx, *ctx.root, from); err != nil {
		return segment[K]{}, err
	} else if hasPredecessor && !ctx.family.Less(predecessor.To, from) {
		end := predecessor.To
		if ctx.family.Less(to, end) {
			end = to
		}
		return segment[K]{to: end, value: someValue(predecessor.Value)}, nil
	}
	if next, hasNext, err := readAtOrAfter(ctx, *ctx.root, from); err != nil {
		return segment[K]{}, err
	} else if hasNext && !ctx.family.Less(to, next.From) {
		previous, ok := ctx.family.Previous(next.From)
		if !ok {
			return segment[K]{}, corrupt("range gap does not advance")
		}
		return segment[K]{to: previous}, nil
	}
	return segment[K]{to: to}, nil
}

func rangeReplaceWithHint[K any](ctx *rangeCtx[K], change change[K], hint *tree.LocalReject[rangeRecord[K]], hasHint bool) (bool, error) {
	if ctx.family.Less(change.to, change.from) {
		return false, invalid("range start is after its end")
	}

	var predecessor rangeRecord[K]
	var hasPredecessor bool
	if hasHint {
		if pred, ok := hint.Predecessor(); ok {
			predecessor = pred
			hasPredecessor = true
		} else {
			var err error
			predecessor, hasPredecessor, err = readPredecessor(ctx, *ctx.root, change.from)
			if err != nil {
				return false, err
			}
			hasHint = false
		}
	} else {
		var err error
		predecessor, hasPredecessor, err = readPredecessor(ctx, *ctx.root, change.from)
		if err != nil {
			return false, err
		}
	}
	if change.value.present && hasPredecessor && !ctx.family.Less(predecessor.To, change.to) && predecessor.Value == change.value.value {
		return false, nil
	}
	if hasPredecessor && ctx.family.Less(predecessor.From, change.from) && ctx.family.Less(change.to, predecessor.To) {
		return replaceStrictlyInside(ctx, predecessor, change, hint, hasHint)
	}
	rewrite, err := trimPredecessor(ctx, predecessor, hasPredecessor, change.from, change.to)
	if err != nil {
		return false, err
	}
	if err := trimFollowing(ctx, change.from, change.to, &rewrite); err != nil {
		return false, err
	}
	return writeReplacement(ctx, change.from, change.to, change.value, &rewrite)
}

// replaceStrictlyInside replaces one range strictly inside an existing
// range with up to three records (Rust replace_strictly_inside).
func replaceStrictlyInside[K any](ctx *rangeCtx[K], old rangeRecord[K], change change[K], hint *tree.LocalReject[rangeRecord[K]], hasHint bool) (bool, error) {
	leftPrevious, ok := ctx.family.Previous(change.from)
	if !ok {
		return false, corrupt("range rewrite does not advance")
	}
	rightNext, ok := ctx.family.Next(change.to)
	if !ok {
		return false, corrupt("range rewrite does not advance")
	}
	left, err := ctx.encodeRecord(0, rangeRecord[K]{From: old.From, To: leftPrevious, Value: old.Value})
	if err != nil {
		return false, err
	}
	right, err := ctx.encodeRecord(1, rangeRecord[K]{From: rightNext, To: old.To, Value: old.Value})
	if err != nil {
		return false, err
	}
	var middle []byte
	if change.value.present {
		middle, err = ctx.encodeRecord(2, rangeRecord[K]{From: change.from, To: change.to, Value: change.value.value})
		if err != nil {
			return false, err
		}
	}
	var retired tree.RetiredPages
	switch {
	case change.value.present:
		cells := [][]byte{left, middle, right}
		if err := replaceStrictCells(ctx, old.From, cells, hint, hasHint, &retired); err != nil {
			return false, err
		}
	default:
		cells := [][]byte{left, right}
		if err := replaceStrictCells(ctx, old.From, cells, hint, hasHint, &retired); err != nil {
			return false, err
		}
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return false, err
	}
	if change.value.present {
		if err := addCount(ctx.count, 2); err != nil {
			return false, err
		}
	} else {
		if err := addCount(ctx.count, 1); err != nil {
			return false, err
		}
	}
	recorded := old.Value
	if change.value.present {
		recorded = change.value.value
	}
	if err := storeRecordAdded(ctx, recorded); err != nil {
		return false, err
	}
	if change.value.present {
		if err := storeRecordAdded(ctx, old.Value); err != nil {
			return false, err
		}
	}
	emitted := uint64(2)
	if change.value.present {
		emitted = 3
	}
	work.RangeEmitted(emitted)
	work.RangeSplit(1)
	return true, nil
}

func replaceStrictCells[K any](ctx *rangeCtx[K], oldKey K, cells [][]byte, hint *tree.LocalReject[rangeRecord[K]], hasHint bool, retired *tree.RetiredPages) error {
	if hasHint {
		return predecessorReplace(ctx, hint, oldKey, cells)
	}
	r, err := tree.ReplaceLeafWith(ctx.family, ctx.storeView, ctx.root, ctx.family.KeyOf(oldKey), cells, *retired)
	*retired = r
	return err
}

// rewrite accumulates the trimmed sides of one interval rewrite (Rust
// Rewrite): the sides are carried by value with presence flags, so a
// clear or delete never allocates (Rust Option<RangeRecord> value
// semantics; a heap rewrite would allocate per record).
type rewrite[K any] struct {
	left     rangeRecord[K]
	right    rangeRecord[K]
	hasLeft  bool
	hasRight bool
	changed  bool
}

// trimPredecessor trims the overlapping head of the predecessor range
// (Rust trim_predecessor).
func trimPredecessor[K any](ctx *rangeCtx[K], predecessor rangeRecord[K], hasPredecessor bool, from, to K) (rewrite[K], error) {
	var rewrite rewrite[K]
	if !hasPredecessor || ctx.family.Less(predecessor.To, from) {
		return rewrite, nil
	}
	if err := rangeRemove(ctx, predecessor); err != nil {
		return rewrite, err
	}
	rewrite.changed = true
	if ctx.family.Less(predecessor.From, from) {
		previous, ok := ctx.family.Previous(from)
		if !ok {
			return rewrite, corrupt("range rewrite does not advance")
		}
		rewrite.left = rangeRecord[K]{From: predecessor.From, To: previous, Value: predecessor.Value}
		rewrite.hasLeft = true
	}
	if ctx.family.Less(to, predecessor.To) {
		next, ok := ctx.family.Next(to)
		if !ok {
			return rewrite, corrupt("range rewrite does not advance")
		}
		rewrite.right = rangeRecord[K]{From: next, To: predecessor.To, Value: predecessor.Value}
		rewrite.hasRight = true
	}
	if rewrite.hasLeft && rewrite.hasRight {
		work.RangeSplit(1)
	}
	return rewrite, nil
}

// trimFollowing removes every range overlapping [from, to] after the
// predecessor (Rust trim_following).
func trimFollowing[K any](ctx *rangeCtx[K], from, to K, rewrite *rewrite[K]) error {
	for {
		old, hasOld, err := readAtOrAfter(ctx, *ctx.root, from)
		if err != nil {
			return err
		}
		if !hasOld {
			return nil
		}
		if ctx.family.Less(to, old.From) {
			return nil
		}
		rewrite.changed = true
		if err := rangeRemove(ctx, old); err != nil {
			return err
		}
		if ctx.family.Less(to, old.To) {
			next, ok := ctx.family.Next(to)
			if !ok {
				return corrupt("range rewrite does not advance")
			}
			rewrite.right = rangeRecord[K]{From: next, To: old.To, Value: old.Value}
			rewrite.hasRight = true
			return nil
		}
	}
}

// writeReplacement writes the rewritten sides and the new value (Rust
// write_replacement).
func writeReplacement[K any](ctx *rangeCtx[K], from, to K, value optionalValue, rewrite *rewrite[K]) (bool, error) {
	if rewrite.hasLeft {
		if err := insertCoalesced(ctx, rewrite.left); err != nil {
			return false, err
		}
	}
	if value.present {
		if err := insertCoalesced(ctx, rangeRecord[K]{From: from, To: to, Value: value.value}); err != nil {
			return false, err
		}
	}
	if rewrite.hasRight {
		if err := insertCoalesced(ctx, rewrite.right); err != nil {
			return false, err
		}
	}
	return rewrite.changed || value.present, nil
}

// insertCoalesced inserts one range after merging it with its same-value
// neighbors (Rust insert_coalesced).
func insertCoalesced[K any](ctx *rangeCtx[K], r rangeRecord[K]) error {
	merged, err := mergePrevious(ctx, r)
	if err != nil {
		return err
	}
	merged, err = mergeNext(ctx, merged)
	if err != nil {
		return err
	}
	return insert(ctx, merged)
}

func mergePrevious[K any](ctx *rangeCtx[K], r rangeRecord[K]) (rangeRecord[K], error) {
	previous, hasPrevious, err := readPredecessor(ctx, *ctx.root, r.From)
	if err != nil || !hasPrevious {
		return r, err
	}
	if previous.Value == r.Value {
		if next, ok := ctx.family.Next(previous.To); ok && ctx.family.Equal(next, r.From) {
			if err := rangeRemove(ctx, previous); err != nil {
				return r, err
			}
			r.From = previous.From
			work.RangeCoalesced(1)
		}
	}
	return r, nil
}

func mergeNext[K any](ctx *rangeCtx[K], r rangeRecord[K]) (rangeRecord[K], error) {
	next, hasNext, err := readAtOrAfter(ctx, *ctx.root, r.From)
	if err != nil || !hasNext {
		return r, err
	}
	if next.Value == r.Value {
		if boundary, ok := ctx.family.Next(r.To); ok && ctx.family.Equal(boundary, next.From) {
			if err := rangeRemove(ctx, next); err != nil {
				return r, err
			}
			r.To = next.To
			work.RangeCoalesced(1)
		}
	}
	return r, nil
}

// insert writes one range record into the tree, retiring COW victims and
// accounting the record (Rust range_mutation::insert).
func insert[K any](ctx *rangeCtx[K], r rangeRecord[K]) error {
	cell, err := ctx.encodeRecord(0, r)
	if err != nil {
		return err
	}
	var retired tree.RetiredPages
	retired, inserted, err := tree.Insert(ctx.family, ctx.storeView, ctx.root, cell, retired)
	if err != nil {
		return err
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return err
	}
	if inserted {
		if err := rangeRecordAdded(ctx, r.Value); err != nil {
			return err
		}
	}
	return nil
}

// rangeRemove deletes one existing range record (Rust remove).
func rangeRemove[K any](ctx *rangeCtx[K], r rangeRecord[K]) error {
	var retired tree.RetiredPages
	retired, err := tree.DeleteExisting(ctx.family, ctx.storeView, ctx.root, ctx.family.KeyOf(r.From), retired)
	if err != nil {
		return err
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return err
	}
	if err := subCount(ctx.count); err != nil {
		return err
	}
	return rangeRecordRemoved(ctx, r.Value)
}

// insertPrivateGap inserts one range into the private tree through the gap
// machinery (Rust insert_private_gap).
func insertPrivateGap[K any](ctx *rangeCtx[K], r rangeRecord[K], reject *tree.LocalReject[rangeRecord[K]]) (bool, uint32, error) {
	cell, err := ctx.encodeRecord(0, r)
	if err != nil {
		return false, 0, err
	}
	retired, outcome, err := gapLocalInsert(ctx, r, cell, tree.RetiredPages{}, reject)
	if err != nil {
		return false, 0, err
	}
	if retired.Len() != 0 {
		return false, 0, corrupt("private range insertion retired a page")
	}
	if outcome.Inserted {
		if err := rangeRecordAdded(ctx, r.Value); err != nil {
			return false, 0, err
		}
	}
	return outcome.Inserted, outcome.PageNumber, nil
}

// insertPrivateRejected completes a rejected private gap insertion after
// the caller proved the external sides (Rust insert_private_rejected).
func insertPrivateRejected[K any](ctx *rangeCtx[K], r rangeRecord[K], rejected tree.LocalReject[rangeRecord[K]]) (tree.PrivatePosition, bool, error) {
	cell, err := ctx.encodeRecord(0, r)
	if err != nil {
		return tree.PrivatePosition{}, false, err
	}
	position, fits, err := gapRejectedInsert(ctx, cell, rejected)
	if err != nil {
		return tree.PrivatePosition{}, false, err
	}
	if err := rangeRecordAdded(ctx, r.Value); err != nil {
		return tree.PrivatePosition{}, false, err
	}
	return position, fits, nil
}

// rangeRecordAdded accounts one added record end to end: the record
// count, the untracked-aware store charge, and the emitted-work
// counter (Rust range_record_added under insert).
func rangeRecordAdded[K any](ctx *rangeCtx[K], value uint32) error {
	if err := addCount(ctx.count, 1); err != nil {
		return err
	}
	if err := storeRecordAdded(ctx, value); err != nil {
		return err
	}
	work.RangeEmitted(1)
	return nil
}

// storeRecordAdded charges one recorded value unless the operation is
// untracked (Rust range_record_added / Untracked). Callers that manage
// their own count and work counters use it directly.
func storeRecordAdded[K any](ctx *rangeCtx[K], value uint32) error {
	if ctx.untracked {
		return nil
	}
	return ctx.store.RangeRecordAdded(value)
}

// rangeRecordRemoved accounts one removed record unless the operation is
// untracked (Rust range_record_removed / Untracked).
func rangeRecordRemoved[K any](ctx *rangeCtx[K], value uint32) error {
	if ctx.untracked {
		return nil
	}
	return ctx.store.RangeRecordRemoved(value)
}

func readPredecessor[K any](ctx *rangeCtx[K], root uint32, key K) (rangeRecord[K], bool, error) {
	return tree.Predecessor(ctx.family, ctx.storeView, root, ctx.family.KeyOf(key))
}

func readAtOrAfter[K any](ctx *rangeCtx[K], root uint32, key K) (rangeRecord[K], bool, error) {
	return tree.AtOrAfter(ctx.family, ctx.storeView, root, ctx.family.KeyOf(key))
}

// addCount adds n to the record count with overflow failure (Rust
// checked_add on record counts).
func addCount(count *uint64, n uint64) error {
	if *count > ^uint64(0)-n {
		return overflow("range record count")
	}
	*count += n
	return nil
}

// subCount subtracts one from the record count with underflow failure
// (Rust checked_sub on record counts).
func subCount(count *uint64) error {
	if *count == 0 {
		return overflow("range record count")
	}
	*count--
	return nil
}

// assignPrivateInput assigns one private range through the locator input
// (Rust assign_private_input): the disabled input takes the ordinary
// private path, otherwise the locator probe runs first and the rejected
// gap completes through the hint.
func rangeAssignPrivateInput[K any](ctx *rangeCtx[K], from, to K, value uint32, input *privateInput[K]) (bool, error) {
	if input.disabled() {
		return rangeAssignPrivate(ctx, from, to, value)
	}
	r := rangeRecord[K]{From: from, To: to, Value: value}
	switch result, err := insertPrivateInputGap(ctx, r, input); {
	case err != nil:
		return false, err
	case result.inserted:
		return true, nil
	default:
		return assignWithHint(ctx, r, result.reject, result.rejected)
	}
}
