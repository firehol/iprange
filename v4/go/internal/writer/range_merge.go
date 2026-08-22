// One authoritative ordered old/input merge into mapped range pages
// (Rust draft_store/range_merge.rs): the merge walks the committed base
// generation forward through the draft mapping and the incoming records
// in lockstep, calling the policy for every segment and emitting the
// new canonical tree through the range bulk builder. Membership values
// account refcount changes in coalesced runs; the base tree is retired
// once the merge publishes its output. Every per-record state below is
// embedded by value: the drive loop must not allocate per segment (Rust
// stores the same state as value Option fields).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// optionalValue is one Rust-style Option<u32> carried by value: the
// ordered-merge and policy hot paths pass segment values without
// allocating per record.
type optionalValue struct {
	value   uint32
	present bool
}

// someValue returns the present option of value (Rust Some(value)).
func someValue(value uint32) optionalValue { return optionalValue{value: value, present: true} }

// noneValue returns the absent option (Rust None).
func noneValue() optionalValue { return optionalValue{} }

// sameOptional compares two options (Rust Option<u32> equality: both
// absent, or both present with equal values).
func sameOptional(left, right optionalValue) bool {
	if !left.present || !right.present {
		return left.present == right.present
	}
	return left.value == right.value
}

// incomingRange is one input interval with its value (Rust
// draft_store/range_merge.rs Incoming; the value is the last-seen
// timestamp for the history projection).
type incomingRange struct {
	from  tree.Key
	to    tree.Key
	value uint32
}

// mergePolicy is the per-operation semantics of one ordered merge (Rust
// Policy trait). preserveWithoutInput mirrors the trait constant: the
// history projection never preserves an untouched base, the feed merges
// (which do) reuse this merge later.
type mergePolicy[P any] interface {
	transform(store *DraftStore, old, incoming optionalValue) (optionalValue, error)
	observe(from, to tree.Key, old, incoming, new optionalValue) error
	finish() (P, error)
	preserveWithoutInput() bool
}

// mergeOutput is the coalescing range output of one merge (Rust
// Output): adjacent same-value records merge into one, each distinct
// record is charged through the membership refcount run, and the bulk
// builder seals the new tree at finish. pending is embedded by value so
// emitting one record never allocates.
type mergeOutput struct {
	builder     *rangeBulkBuilder
	pending     rangeRecord
	hasPending  bool
	refcounts   refcountRun
	hasRefcount bool
}

func newMergeOutput(transaction uint64, valueKind, family uint8) mergeOutput {
	output := mergeOutput{builder: newRangeBulkBuilder(transaction, valueKind, family)}
	if valueKind == format.ValueKindMembership {
		output.hasRefcount = true
	}
	return output
}

// emit appends one output record, coalescing it into the pending record
// when the values match and the ranges are adjacent (Rust Output::emit).
func (o *mergeOutput) emit(store *DraftStore, record rangeRecord) error {
	if o.hasPending && o.pending.value == record.value {
		if next, ok := nextKey(store.draft.meta.AddressFamily, o.pending.to); ok && next.Equal(record.from) {
			o.pending.to = record.to
			work.RangeCoalesced(1)
			return nil
		}
	}
	if o.hasPending {
		if err := o.push(store, o.pending); err != nil {
			return err
		}
		o.hasPending = false
	}
	o.pending = record
	o.hasPending = true
	return nil
}

// finish seals the output tree and returns its root and record count
// (Rust Output::finish).
func (o *mergeOutput) finish(store *DraftStore) (uint32, uint64, error) {
	if o.hasPending {
		if err := o.push(store, o.pending); err != nil {
			return 0, 0, err
		}
		o.hasPending = false
	}
	if o.hasRefcount {
		if err := o.refcounts.flush(store); err != nil {
			return 0, 0, err
		}
	}
	return o.builder.finish(store)
}

// push charges one distinct output record and appends it (Rust
// Output::push).
func (o *mergeOutput) push(store *DraftStore, record rangeRecord) error {
	if o.hasRefcount {
		if err := o.refcounts.add(store, record.value, 1); err != nil {
			return err
		}
	}
	return o.builder.push(store, record)
}

// refcountRun coalesces consecutive refcount changes of one membership
// id into one delta (Rust RefcountRun). current is embedded by value.
type refcountRun struct {
	current    refcountEntry
	hasCurrent bool
}

type refcountEntry struct {
	id    uint32
	count uint64
}

// add records one signed change of id; when the id changes, the
// previous run flushes with the caller's sign exactly like Rust
// RefcountRun::add (the output sweep passes +1, the base-retirement
// drain -1).
func (r *refcountRun) add(store *DraftStore, id uint32, sign int64) error {
	if r.hasCurrent {
		if r.current.id == id {
			next, err := checkedAdd(r.current.count, 1, "membership refcount run")
			if err != nil {
				return err
			}
			r.current.count = next
			return nil
		}
		if err := r.flushSigned(store, sign); err != nil {
			return err
		}
	}
	r.current = refcountEntry{id: id, count: 1}
	r.hasCurrent = true
	return nil
}

// flush applies and clears the current run with the +1 sign (Rust
// Output::finish RefcountRun::flush parity).
func (r *refcountRun) flush(store *DraftStore) error {
	return r.flushSigned(store, 1)
}

// flushSigned applies and clears the current run with a caller sign
// (Rust RefcountRun::flush; -1 drains the accounted base run).
func (r *refcountRun) flushSigned(store *DraftStore, sign int64) error {
	if !r.hasCurrent {
		return nil
	}
	var signed int64
	if sign < 0 && r.current.count > uint64(^uint64(0)>>1) {
		return overflow("membership refcount run")
	}
	if sign < 0 {
		signed = -int64(r.current.count)
	} else {
		signed = int64(r.current.count)
		if signed < 0 {
			return overflow("membership refcount run")
		}
	}
	if err := store.trackMembershipRefcount(r.current.id, signed); err != nil {
		return err
	}
	r.hasCurrent = false
	return nil
}

// orderedMerge walks the base generation and the incoming records in
// lockstep, emitting the policy result per segment (Rust OrderedMerge).
// old and previousInputEnd are embedded by value; the cursor hands back
// one record at a time without allocating.
type orderedMerge[P any] struct {
	codec               rangeFamily
	family              uint8
	oldCursor           *rangeCursor
	old                 rangeRecord
	hasOld              bool
	oldAccounted        bool
	previousInputEnd    tree.Key
	hasPreviousInputEnd bool
	output              mergeOutput
	policy              mergePolicy[P]
	baseRoot            uint32
	baseCount           uint64
	inputSeen           bool
	oldRefcounts        refcountRun
	hasOldRefcounts     bool
	cancellationWork    uint16
}

// newOrderedMerge opens the base cursor and the output builder (Rust
// OrderedMerge::new). The old cursor reads the committed base generation
// with base-meta selection, never the draft.
func newOrderedMerge[P any](store *DraftStore, base format.Meta, codec rangeFamily, policy mergePolicy[P], check func() error) (*orderedMerge[P], error) {
	if err := check(); err != nil {
		return nil, err
	}
	oldCursor, err := newRangeCursor(store, base)
	if err != nil {
		return nil, err
	}
	merge := &orderedMerge[P]{
		codec:     codec,
		family:    base.AddressFamily,
		oldCursor: oldCursor,
		output:    newMergeOutput(store.draft.meta.TxnID, base.ValueKind, base.AddressFamily),
		policy:    policy,
		baseRoot:  base.RangeRoot,
		baseCount: base.RangeRecordCount,
	}
	if base.ValueKind == format.ValueKindMembership {
		merge.hasOldRefcounts = true
	}
	old, has, err := oldCursor.next()
	if err != nil {
		return nil, err
	}
	merge.old = old
	merge.hasOld = has
	return merge, nil
}

// push merges one incoming interval, emitting every segment before it
// (Rust OrderedMerge::push).
func (m *orderedMerge[P]) push(store *DraftStore, incoming incomingRange, check func() error) error {
	if err := m.requireInput(incoming); err != nil {
		return err
	}
	m.inputSeen = true
	copy := incoming
	m.previousInputEnd = copy.to
	m.hasPreviousInputEnd = true
	for {
		if err := m.checkpoint(check); err != nil {
			return err
		}
		if !m.hasOld {
			return m.emit(store, copy.from, copy.to, noneValue(), someValue(copy.value))
		}
		if m.old.to.Less(copy.from) {
			if err := m.accountOld(store); err != nil {
				return err
			}
			old := m.old
			if err := m.emit(store, old.from, old.to, someValue(old.value), noneValue()); err != nil {
				return err
			}
			if err := m.advanceOld(store); err != nil {
				return err
			}
			continue
		}
		if copy.to.Less(m.old.from) {
			return m.emit(store, copy.from, copy.to, noneValue(), someValue(copy.value))
		}
		if err := m.accountOld(store); err != nil {
			return err
		}
		if m.old.from.Less(copy.from) {
			end, err := orderedPrevious(m.codec, copy.from, "ordered merge old prefix")
			if err != nil {
				return err
			}
			old := m.old
			if err := m.emit(store, old.from, end, someValue(old.value), noneValue()); err != nil {
				return err
			}
			m.old.from = copy.from
			continue
		}
		if copy.from.Less(m.old.from) {
			end, err := orderedPrevious(m.codec, m.old.from, "ordered merge input prefix")
			if err != nil {
				return err
			}
			if err := m.emit(store, copy.from, end, noneValue(), someValue(copy.value)); err != nil {
				return err
			}
			copy.from = m.old.from
			continue
		}
		end := m.old.to
		if copy.to.Less(end) {
			end = copy.to
		}
		old := m.old
		if err := m.emit(store, old.from, end, someValue(old.value), someValue(copy.value)); err != nil {
			return err
		}
		if m.old.to.Equal(end) {
			if err := m.advanceOld(store); err != nil {
				return err
			}
		} else {
			next, err := orderedNext(m.codec, end, "ordered merge old remainder")
			if err != nil {
				return err
			}
			m.old.from = next
		}
		if copy.to.Equal(end) {
			return nil
		}
		next, err := orderedNext(m.codec, end, "ordered merge input remainder")
		if err != nil {
			return err
		}
		copy.from = next
	}
}

// finish drains the remaining base records, retires the base tree, and
// publishes the merge output into the draft meta (Rust
// OrderedMerge::finish).
func (m *orderedMerge[P]) finish(store *DraftStore, check func() error) (P, error) {
	var zero P
	if !m.inputSeen && m.policy.preserveWithoutInput() {
		return m.finishPreserved(store, check)
	}
	for m.hasOld {
		if err := m.checkpoint(check); err != nil {
			return zero, err
		}
		if err := m.accountOld(store); err != nil {
			return zero, err
		}
		old := m.old
		if err := m.emit(store, old.from, old.to, someValue(old.value), noneValue()); err != nil {
			return zero, err
		}
		if err := m.advanceOld(store); err != nil {
			return zero, err
		}
	}
	if m.hasOldRefcounts {
		if err := m.oldRefcounts.flushSigned(store, -1); err != nil {
			return zero, err
		}
	}
	root, count, err := m.output.finish(store)
	if err != nil {
		return zero, err
	}
	retirementWork := 0
	if err := tree.RetireTree(m.codec, store, m.baseRoot, func() error {
		retirementWork++
		if retirementWork == 4096 {
			retirementWork = 0
			return check()
		}
		return nil
	}); err != nil {
		return zero, err
	}
	if err := check(); err != nil {
		return zero, err
	}
	store.draft.baseRangeTreeRetired = true
	store.draft.meta.RangeRoot = root
	store.draft.meta.RangeRecordCount = count
	return m.policy.finish()
}

// finishPreserved keeps the untouched base tree when the policy
// preserves without input, observing each base record (Rust
// OrderedMerge::finish_preserved).
func (m *orderedMerge[P]) finishPreserved(store *DraftStore, check func() error) (P, error) {
	var zero P
	for m.hasOld {
		if err := m.checkpoint(check); err != nil {
			return zero, err
		}
		old := m.old
		value := old.value
		if err := m.policy.observe(old.from, old.to, someValue(value), noneValue(), someValue(value)); err != nil {
			return zero, err
		}
		if err := m.advanceOld(store); err != nil {
			return zero, err
		}
	}
	store.draft.meta.RangeRoot = m.baseRoot
	store.draft.meta.RangeRecordCount = m.baseCount
	return m.policy.finish()
}

// checkpoint runs the caller's cancellation every 4096 merge steps
// (Rust OrderedMerge::checkpoint).
func (m *orderedMerge[P]) checkpoint(check func() error) error {
	m.cancellationWork++
	if m.cancellationWork == 4096 {
		m.cancellationWork = 0
		return check()
	}
	return nil
}

// emit transforms one segment through the policy and appends the new
// value (Rust OrderedMerge::emit).
func (m *orderedMerge[P]) emit(store *DraftStore, from, to tree.Key, old, incoming optionalValue) error {
	new, err := m.policy.transform(store, old, incoming)
	if err != nil {
		return err
	}
	if err := m.policy.observe(from, to, old, incoming, new); err != nil {
		return err
	}
	if new.present {
		return m.output.emit(store, rangeRecord{from: from, to: to, value: new.value})
	}
	return nil
}

// accountOld charges the current base record into the old refcount run
// exactly once (Rust OrderedMerge::account_old).
func (m *orderedMerge[P]) accountOld(store *DraftStore) error {
	if !m.oldAccounted {
		if !m.hasOld {
			return corrupt("ordered merge lost its old range")
		}
		if m.hasOldRefcounts {
			if err := m.oldRefcounts.add(store, m.old.value, -1); err != nil {
				return err
			}
		}
		m.oldAccounted = true
	}
	return nil
}

// advanceOld moves the base cursor to the next record (Rust
// OrderedMerge::advance_old).
func (m *orderedMerge[P]) advanceOld(store *DraftStore) error {
	next, has, err := m.oldCursor.next()
	if err != nil {
		return err
	}
	m.old = next
	m.hasOld = has
	m.oldAccounted = false
	return nil
}

// requireInput rejects reversed or overlapping input ranges (Rust
// OrderedMerge::require_input).
func (m *orderedMerge[P]) requireInput(incoming incomingRange) error {
	if incoming.to.Less(incoming.from) {
		return corrupt("ordered merge input range is reversed")
	}
	if m.hasPreviousInputEnd && !m.previousInputEnd.Less(incoming.from) {
		return corrupt("ordered merge input ranges overlap or are out of order")
	}
	return nil
}

func orderedPrevious(codec rangeFamily, key tree.Key, context string) (tree.Key, error) {
	previous, ok := codec.Previous(key)
	if !ok {
		return tree.Key{}, overflow(context)
	}
	return previous, nil
}

func orderedNext(codec rangeFamily, key tree.Key, context string) (tree.Key, error) {
	next, ok := codec.Next(key)
	if !ok {
		return tree.Key{}, overflow(context)
	}
	return next, nil
}
