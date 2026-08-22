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

// rangeCtx carries the mutable range-tree state of one draft (the Rust
// range_mutation function parameters: store, root, record_count).
type rangeCtx struct {
	family rangeFamily
	store  RangeStore
	root   *uint32
	count  *uint64
}

// change is one requested range rewrite (Rust Change).
type change struct {
	from  tree.Key
	to    tree.Key
	value optionalValue
}

// encodedRange is a fixed-size encoded range record (Rust EncodedRange;
// MAX_RECORD_SIZE 36).
type encodedRange struct {
	bytes [format.RangeRecordV6Size]byte
	len   int
}

func newEncodedRange(family rangeFamily, r rangeRecord) (encodedRange, error) {
	var e encodedRange
	n, err := family.EncodeRecord(r, e.bytes[:])
	if err != nil {
		return encodedRange{}, err
	}
	e.len = n
	return e, nil
}

func (e *encodedRange) slice() []byte { return e.bytes[:e.len] }

// assign replaces the [from, to] interval with one value (Rust assign).
func rangeAssign(ctx *rangeCtx, from, to tree.Key, value uint32) (bool, error) {
	return rangeReplaceWithHint(ctx, change{from: from, to: to, value: someValue(value)}, nil)
}

// assignPrivate inserts one range into the private tree when the physical
// gap around it is open (Rust assign_private).
func rangeAssignPrivate(ctx *rangeCtx, from, to tree.Key, value uint32) (bool, error) {
	if to.Less(from) {
		return false, invalid("range start is after its end")
	}
	r := rangeRecord{from: from, to: to, value: value}
	switch result, err := insertPrivateGap(ctx, r); {
	case err != nil:
		return false, err
	case result.Inserted:
		return true, nil
	default:
		return assignWithHint(ctx, r, result.Reject)
	}
}

// assignWithHint completes a private assignment through a previous gap
// rejection (Rust assign_with_hint).
func assignWithHint(ctx *rangeCtx, r rangeRecord, hint *tree.LocalReject[rangeRecord]) (bool, error) {
	return rangeReplaceWithHint(ctx, change{from: r.from, to: r.to, value: someValue(r.value)}, hint)
}

// clear removes the [from, to] interval (Rust clear).
func rangeClear(ctx *rangeCtx, from, to tree.Key) (bool, error) {
	return rangeReplaceWithHint(ctx, change{from: from, to: to}, nil)
}

// retireTree retires every page of one private range tree (Rust
// retire_tree).
func rangeRetireTree(ctx *rangeCtx, root uint32, checkpoint func() error) error {
	return tree.RetireTree(ctx.family, ctx.store, root, checkpoint)
}

// transform walks [from, to] in range order, applying operation to each
// present-value segment and replacing the segments whose value changed
// (Rust transform).
func rangeTransform(ctx *rangeCtx, from, to tree.Key, operation func(store RangeStore, value optionalValue) (optionalValue, error)) (bool, error) {
	if to.Less(from) {
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
			if _, err := rangeReplaceWithHint(ctx, change{from: cursor, to: segment.to, value: value}, nil); err != nil {
				return false, err
			}
			changed = true
		}
		if segment.to.Equal(to) {
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
type segment struct {
	to    tree.Key
	value optionalValue
}

func segmentAt(ctx *rangeCtx, from, to tree.Key) (*segment, error) {
	if predecessor, hasPredecessor, err := readPredecessor(ctx, *ctx.root, from); err != nil {
		return nil, err
	} else if hasPredecessor && !predecessor.to.Less(from) {
		end := predecessor.to
		if to.Less(end) {
			end = to
		}
		return &segment{to: end, value: someValue(predecessor.value)}, nil
	}
	if next, hasNext, err := readAtOrAfter(ctx, *ctx.root, from); err != nil {
		return nil, err
	} else if hasNext && !to.Less(next.from) {
		previous, ok := ctx.family.Previous(next.from)
		if !ok {
			return nil, corrupt("range gap does not advance")
		}
		return &segment{to: previous}, nil
	}
	return &segment{to: to}, nil
}

// rangeReplace rewrites the [from, to] interval with value (Rust replace).
func rangeReplace(ctx *rangeCtx, from, to tree.Key, hints ...*tree.LocalReject[rangeRecord]) (bool, error) {
	var hint *tree.LocalReject[rangeRecord]
	if len(hints) > 0 {
		hint = hints[0]
	}
	return rangeReplaceWithHint(ctx, change{from: from, to: to}, hint)
}

func rangeReplaceWithHint(ctx *rangeCtx, change change, hint *tree.LocalReject[rangeRecord]) (bool, error) {
	if change.to.Less(change.from) {
		return false, invalid("range start is after its end")
	}
	var predecessor rangeRecord
	var hasPredecessor bool
	if hint != nil {
		if pred, ok := hint.Predecessor(); ok {
			predecessor = pred
			hasPredecessor = true
		} else {
			var err error
			predecessor, hasPredecessor, err = readPredecessor(ctx, *ctx.root, change.from)
			if err != nil {
				return false, err
			}
			hint = nil
		}
	} else {
		var err error
		predecessor, hasPredecessor, err = readPredecessor(ctx, *ctx.root, change.from)
		if err != nil {
			return false, err
		}
	}
	if change.value.present && hasPredecessor && !predecessor.to.Less(change.to) && predecessor.value == change.value.value {
		return false, nil
	}
	if hasPredecessor && predecessor.from.Less(change.from) && change.to.Less(predecessor.to) {
		return replaceStrictlyInside(ctx, predecessor, change, hint)
	}
	rewrite, err := trimPredecessor(ctx, predecessor, hasPredecessor, change.from, change.to)
	if err != nil {
		return false, err
	}
	if err := trimFollowing(ctx, change.from, change.to, rewrite); err != nil {
		return false, err
	}
	return writeReplacement(ctx, change.from, change.to, change.value, rewrite)
}

// replaceStrictlyInside replaces one range strictly inside an existing
// range with up to three records (Rust replace_strictly_inside).
func replaceStrictlyInside(ctx *rangeCtx, old rangeRecord, change change, hint *tree.LocalReject[rangeRecord]) (bool, error) {
	leftPrevious, ok := ctx.family.Previous(change.from)
	if !ok {
		return false, corrupt("range rewrite does not advance")
	}
	rightNext, ok := ctx.family.Next(change.to)
	if !ok {
		return false, corrupt("range rewrite does not advance")
	}
	left, err := newEncodedRange(ctx.family, rangeRecord{from: old.from, to: leftPrevious, value: old.value})
	if err != nil {
		return false, err
	}
	right, err := newEncodedRange(ctx.family, rangeRecord{from: rightNext, to: old.to, value: old.value})
	if err != nil {
		return false, err
	}
	var middle *encodedRange
	if change.value.present {
		encoded, err := newEncodedRange(ctx.family, rangeRecord{from: change.from, to: change.to, value: change.value.value})
		if err != nil {
			return false, err
		}
		middle = &encoded
	}
	var retired tree.RetiredPages
	switch {
	case middle != nil:
		cells := [][]byte{left.slice(), middle.slice(), right.slice()}
		if err := replaceStrictCells(ctx, old.from, cells, hint, &retired); err != nil {
			return false, err
		}
	default:
		cells := [][]byte{left.slice(), right.slice()}
		if err := replaceStrictCells(ctx, old.from, cells, hint, &retired); err != nil {
			return false, err
		}
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return false, err
	}
	if middle != nil {
		if err := addCount(ctx.count, 2); err != nil {
			return false, err
		}
	} else {
		if err := addCount(ctx.count, 1); err != nil {
			return false, err
		}
	}
	recorded := old.value
	if change.value.present {
		recorded = change.value.value
	}
	if err := ctx.store.RangeRecordAdded(recorded); err != nil {
		return false, err
	}
	if middle != nil {
		if err := ctx.store.RangeRecordAdded(old.value); err != nil {
			return false, err
		}
	}
	emitted := uint64(2)
	if middle != nil {
		emitted = 3
	}
	work.RangeEmitted(emitted)
	work.RangeSplit(1)
	return true, nil
}

func replaceStrictCells(ctx *rangeCtx, oldKey tree.Key, cells [][]byte, hint *tree.LocalReject[rangeRecord], retired *tree.RetiredPages) error {
	if hint != nil {
		return tree.ReplaceLocalPredecessorWith(ctx.family, ctx.store, ctx.root, hint, oldKey, cells)
	}
	r, err := tree.ReplaceLeafWith(ctx.family, ctx.store, ctx.root, oldKey, cells, *retired)
	*retired = r
	return err
}

// rewrite accumulates the trimmed sides of one interval rewrite (Rust
// Rewrite).
type rewrite struct {
	left    *rangeRecord
	right   *rangeRecord
	changed bool
}

// trimPredecessor trims the overlapping head of the predecessor range
// (Rust trim_predecessor).
func trimPredecessor(ctx *rangeCtx, predecessor rangeRecord, hasPredecessor bool, from, to tree.Key) (*rewrite, error) {
	rewrite := &rewrite{}
	if !hasPredecessor || predecessor.to.Less(from) {
		return rewrite, nil
	}
	if err := rangeRemove(ctx, predecessor); err != nil {
		return nil, err
	}
	rewrite.changed = true
	if predecessor.from.Less(from) {
		previous, ok := ctx.family.Previous(from)
		if !ok {
			return nil, corrupt("range rewrite does not advance")
		}
		rewrite.left = &rangeRecord{from: predecessor.from, to: previous, value: predecessor.value}
	}
	if to.Less(predecessor.to) {
		next, ok := ctx.family.Next(to)
		if !ok {
			return nil, corrupt("range rewrite does not advance")
		}
		rewrite.right = &rangeRecord{from: next, to: predecessor.to, value: predecessor.value}
	}
	if rewrite.left != nil && rewrite.right != nil {
		work.RangeSplit(1)
	}
	return rewrite, nil
}

// trimFollowing removes every range overlapping [from, to] after the
// predecessor (Rust trim_following).
func trimFollowing(ctx *rangeCtx, from, to tree.Key, rewrite *rewrite) error {
	for {
		old, hasOld, err := readAtOrAfter(ctx, *ctx.root, from)
		if err != nil {
			return err
		}
		if !hasOld {
			return nil
		}
		if to.Less(old.from) {
			return nil
		}
		rewrite.changed = true
		if err := rangeRemove(ctx, old); err != nil {
			return err
		}
		if to.Less(old.to) {
			next, ok := ctx.family.Next(to)
			if !ok {
				return corrupt("range rewrite does not advance")
			}
			rewrite.right = &rangeRecord{from: next, to: old.to, value: old.value}
			return nil
		}
	}
}

// writeReplacement writes the rewritten sides and the new value (Rust
// write_replacement).
func writeReplacement(ctx *rangeCtx, from, to tree.Key, value optionalValue, rewrite *rewrite) (bool, error) {
	if rewrite.left != nil {
		if err := insertCoalesced(ctx, *rewrite.left); err != nil {
			return false, err
		}
	}
	if value.present {
		if err := insertCoalesced(ctx, rangeRecord{from: from, to: to, value: value.value}); err != nil {
			return false, err
		}
	}
	if rewrite.right != nil {
		if err := insertCoalesced(ctx, *rewrite.right); err != nil {
			return false, err
		}
	}
	return rewrite.changed || value.present, nil
}

// insertCoalesced inserts one range after merging it with its same-value
// neighbors (Rust insert_coalesced).
func insertCoalesced(ctx *rangeCtx, r rangeRecord) error {
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

func mergePrevious(ctx *rangeCtx, r rangeRecord) (rangeRecord, error) {
	previous, hasPrevious, err := readPredecessor(ctx, *ctx.root, r.from)
	if err != nil || !hasPrevious {
		return r, err
	}
	if previous.value == r.value {
		if next, ok := ctx.family.Next(previous.to); ok && next.Equal(r.from) {
			if err := rangeRemove(ctx, previous); err != nil {
				return r, err
			}
			r.from = previous.from
			work.RangeCoalesced(1)
		}
	}
	return r, nil
}

func mergeNext(ctx *rangeCtx, r rangeRecord) (rangeRecord, error) {
	next, hasNext, err := readAtOrAfter(ctx, *ctx.root, r.from)
	if err != nil || !hasNext {
		return r, err
	}
	if next.value == r.value {
		if boundary, ok := ctx.family.Next(r.to); ok && boundary.Equal(next.from) {
			if err := rangeRemove(ctx, next); err != nil {
				return r, err
			}
			r.to = next.to
			work.RangeCoalesced(1)
		}
	}
	return r, nil
}

// insert writes one range record into the tree, retiring COW victims and
// accounting the record (Rust range_mutation::insert).
func insert(ctx *rangeCtx, r rangeRecord) error {
	encoded, err := newEncodedRange(ctx.family, r)
	if err != nil {
		return err
	}
	var retired tree.RetiredPages
	retired, inserted, err := tree.Insert(ctx.family, ctx.store, ctx.root, encoded.slice(), retired)
	if err != nil {
		return err
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return err
	}
	if inserted {
		if err := addCount(ctx.count, 1); err != nil {
			return err
		}
		if err := ctx.store.RangeRecordAdded(r.value); err != nil {
			return err
		}
		work.RangeEmitted(1)
	}
	return nil
}

// rangeRemove deletes one existing range record (Rust remove).
func rangeRemove(ctx *rangeCtx, r rangeRecord) error {
	var retired tree.RetiredPages
	retired, err := tree.DeleteExisting(ctx.family, ctx.store, ctx.root, r.from, retired)
	if err != nil {
		return err
	}
	if err := ctx.store.RetirePages(retired); err != nil {
		return err
	}
	if err := subCount(ctx.count); err != nil {
		return err
	}
	return ctx.store.RangeRecordRemoved(r.value)
}

// insertPrivateGap inserts one range into the private tree through the gap
// machinery (Rust insert_private_gap).
func insertPrivateGap(ctx *rangeCtx, r rangeRecord) (tree.LocalInsert[rangeRecord], error) {
	encoded, err := newEncodedRange(ctx.family, r)
	if err != nil {
		return tree.LocalInsert[rangeRecord]{}, err
	}
	var gap privateGap
	gap.init(ctx.family, r)
	retired, result, err := tree.InsertIfLocalGap(ctx.family, ctx.store, ctx.root, encoded.slice(), tree.RetiredPages{}, &gap)
	if err != nil {
		return tree.LocalInsert[rangeRecord]{}, err
	}
	if retired.Len() != 0 {
		return tree.LocalInsert[rangeRecord]{}, corrupt("private range insertion retired a page")
	}
	if result.Inserted {
		if err := rangeRecordAdded(ctx, r.value); err != nil {
			return tree.LocalInsert[rangeRecord]{}, err
		}
	}
	return result, nil
}

// insertPrivateRejected completes a rejected private gap insertion after
// the caller proved the external sides (Rust insert_private_rejected).
func insertPrivateRejected(ctx *rangeCtx, r rangeRecord, rejected *tree.LocalReject[rangeRecord]) (tree.PrivatePosition, bool, error) {
	encoded, err := newEncodedRange(ctx.family, r)
	if err != nil {
		return tree.PrivatePosition{}, false, err
	}
	position, fits, err := tree.InsertRejectedGap(ctx.family, ctx.store, ctx.root, encoded.slice(), rejected)
	if err != nil {
		return tree.PrivatePosition{}, false, err
	}
	if err := rangeRecordAdded(ctx, r.value); err != nil {
		return tree.PrivatePosition{}, false, err
	}
	return position, fits, nil
}

func rangeRecordAdded(ctx *rangeCtx, value uint32) error {
	if err := addCount(ctx.count, 1); err != nil {
		return err
	}
	if err := ctx.store.RangeRecordAdded(value); err != nil {
		return err
	}
	work.RangeEmitted(1)
	return nil
}

func readPredecessor(ctx *rangeCtx, root uint32, key tree.Key) (rangeRecord, bool, error) {
	return tree.Predecessor(ctx.family, ctx.store, root, key)
}

func readAtOrAfter(ctx *rangeCtx, root uint32, key tree.Key) (rangeRecord, bool, error) {
	return tree.AtOrAfter(ctx.family, ctx.store, root, key)
}

// privateGap evaluates the local gap around one candidate range (Rust
// range_mutation::PrivateGap).
type privateGap struct {
	family rangeFamily
	r      rangeRecord
}

func (g *privateGap) init(family rangeFamily, r rangeRecord) {
	g.family = family
	g.r = r
}

func (g *privateGap) decode(cell []byte) (rangeRecord, error) {
	return g.family.ReadLeaf(cell)
}

func (g *privateGap) Previous(exact bool, cell []byte) (tree.LocalPrevious, rangeRecord, error) {
	if cell == nil {
		return tree.LocalPreviousAccept, rangeRecord{}, nil
	}
	previous, err := g.decode(cell)
	if err != nil {
		return 0, rangeRecord{}, err
	}
	bridges := false
	if next, ok := g.family.Next(previous.to); ok {
		bridges = next.Equal(g.r.from)
	}
	if exact || !previous.to.Less(g.r.from) ||
		(previous.value == g.r.value && bridges) {
		return tree.LocalPreviousReject, previous, nil
	}
	return tree.LocalPreviousAccept, rangeRecord{}, nil
}

func (g *privateGap) Next(cell []byte) (tree.LocalNext, rangeRecord, error) {
	if cell == nil {
		return tree.LocalNextAccept, rangeRecord{}, nil
	}
	next, err := g.decode(cell)
	if err != nil {
		return 0, rangeRecord{}, err
	}
	bridges := false
	if boundary, ok := g.family.Next(g.r.to); ok {
		bridges = boundary.Equal(next.from)
	}
	if g.r.to.Less(next.from) && (next.value != g.r.value || !bridges) {
		return tree.LocalNextAccept, rangeRecord{}, nil
	}
	return tree.LocalNextReject, next, nil
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
