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

// incomingValue is one Rust-style Option<V> carried by value for the
// ordered-merge input records: the old and new segment values stay u32
// (the range-tree wire values) while the incoming value type is owned
// by the policy (Rust OrderedMerge<_, V, _>: history and timestamp
// input values are u32, the import merge input is the translated
// membership pair).
type incomingValue[V any] struct {
	value   V
	present bool
}

// someIncoming returns the present incoming option of value (Rust
// Some(V)).
func someIncoming[V any](value V) incomingValue[V] {
	return incomingValue[V]{value: value, present: true}
}

// noneIncoming returns the absent incoming option (Rust None).
func noneIncoming[V any]() incomingValue[V] {
	return incomingValue[V]{}
}

// incomingRange is one input interval with its value (Rust
// draft_store/range_merge.rs Incoming<V>; the value is the last-seen
// timestamp for the history projection and the translated membership
// for the name-based import).
type incomingRange[V any, K any] struct {
	from  K
	to    K
	value V
}

// mergePolicy is the per-operation semantics of one ordered merge (Rust
// Policy trait). preserveWithoutInput mirrors the trait constant: the
// history projection never preserves an untouched base, the feed merges
// (which do) reuse this merge later.
type mergePolicy[V any, P any, K any] interface {
	transform(store *DraftStore, old optionalValue, incoming incomingValue[V]) (optionalValue, error)
	observe(from, to K, old optionalValue, incoming incomingValue[V], new optionalValue) error
	finish() (P, error)
	preserveWithoutInput() bool
}

// mergeOutput is the coalescing range output of one merge (Rust
// Output): adjacent same-value records merge into one, each distinct
// record is charged through the membership refcount run, and the bulk
// builder seals the new tree at finish. pending is embedded by value so
// emitting one record never allocates.
type mergeOutput[K any] struct {
	builder     *rangeBulkBuilder[K]
	pending     rangeRecord[K]
	hasPending  bool
	refcounts   refcountRun
	hasRefcount bool
}

func newMergeOutput[K any](transaction uint64, valueKind uint8, family rangeFamily[K]) mergeOutput[K] {
	output := mergeOutput[K]{builder: newRangeBulkBuilder(transaction, valueKind, family)}
	if valueKind == format.ValueKindMembership {
		output.hasRefcount = true
	}
	return output
}

// emit appends one output record, coalescing it into the pending record
// when the values match and the ranges are adjacent (Rust Output::emit).
func (o *mergeOutput[K]) emit(store *DraftStore, record rangeRecord[K]) error {
	if o.hasPending && o.pending.Value == record.Value {
		if next, ok := o.builder.family.Next(o.pending.To); ok && o.builder.family.Equal(next, record.From) {
			o.pending.To = record.To
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
func (o *mergeOutput[K]) finish(store *DraftStore) (uint32, uint64, error) {
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
func (o *mergeOutput[K]) push(store *DraftStore, record rangeRecord[K]) error {
	if o.hasRefcount {
		if err := o.refcounts.add(store, record.Value, 1); err != nil {
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
type orderedMerge[V any, P any, K any] struct {
	family              rangeFamily[K]
	oldCursor           *rangeCursor[K]
	old                 rangeRecord[K]
	hasOld              bool
	oldAccounted        bool
	previousInputEnd    K
	hasPreviousInputEnd bool
	output              mergeOutput[K]
	policy              mergePolicy[V, P, K]
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
func newOrderedMerge[V any, P any, K any](store *DraftStore, base format.Meta, codec rangeFamily[K], policy mergePolicy[V, P, K], check func() error) (*orderedMerge[V, P, K], error) {
	if err := check(); err != nil {
		return nil, err
	}
	oldCursor, err := newRangeCursor[K](store, base, codec, false)
	if err != nil {
		return nil, err
	}
	merge := &orderedMerge[V, P, K]{
		family:    codec,
		oldCursor: oldCursor,
		output:    newMergeOutput(store.draft.meta.TxnID, base.ValueKind, codec),
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
func (m *orderedMerge[V, P, K]) push(store *DraftStore, incoming incomingRange[V, K], check func() error) error {
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
			return m.emit(store, copy.from, copy.to, noneValue(), someIncoming(copy.value))
		}
		if m.family.Less(m.old.To, copy.from) {
			if err := m.accountOld(store); err != nil {
				return err
			}
			old := m.old
			if err := m.emit(store, old.From, old.To, someValue(old.Value), noneIncoming[V]()); err != nil {
				return err
			}
			if err := m.advanceOld(store); err != nil {
				return err
			}
			continue
		}
		if m.family.Less(copy.to, m.old.From) {
			return m.emit(store, copy.from, copy.to, noneValue(), someIncoming(copy.value))
		}
		if err := m.accountOld(store); err != nil {
			return err
		}
		if m.family.Less(m.old.From, copy.from) {
			end, err := orderedPrevious(m.family, copy.from, "ordered merge old prefix")
			if err != nil {
				return err
			}
			old := m.old
			if err := m.emit(store, old.From, end, someValue(old.Value), noneIncoming[V]()); err != nil {
				return err
			}
			m.old.From = copy.from
			continue
		}
		if m.family.Less(copy.from, m.old.From) {
			end, err := orderedPrevious(m.family, m.old.From, "ordered merge input prefix")
			if err != nil {
				return err
			}
			if err := m.emit(store, copy.from, end, noneValue(), someIncoming(copy.value)); err != nil {
				return err
			}
			copy.from = m.old.From
			continue
		}
		end := m.old.To
		if m.family.Less(copy.to, end) {
			end = copy.to
		}
		old := m.old
		if err := m.emit(store, old.From, end, someValue(old.Value), someIncoming(copy.value)); err != nil {
			return err
		}
		if m.family.Equal(m.old.To, end) {
			if err := m.advanceOld(store); err != nil {
				return err
			}
		} else {
			next, err := orderedNext(m.family, end, "ordered merge old remainder")
			if err != nil {
				return err
			}
			m.old.From = next
		}
		if m.family.Equal(copy.to, end) {
			return nil
		}
		next, err := orderedNext(m.family, end, "ordered merge input remainder")
		if err != nil {
			return err
		}
		copy.from = next
	}
}

// finish drains the remaining base records, retires the base tree, and
// publishes the merge output into the draft meta (Rust
// OrderedMerge::finish).
func (m *orderedMerge[V, P, K]) finish(store *DraftStore, check func() error) (P, error) {
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
		if err := m.emit(store, old.From, old.To, someValue(old.Value), noneIncoming[V]()); err != nil {
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
	if err := tree.RetireTree(m.family, store, m.baseRoot, func() error {
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
func (m *orderedMerge[V, P, K]) finishPreserved(store *DraftStore, check func() error) (P, error) {
	var zero P
	for m.hasOld {
		if err := m.checkpoint(check); err != nil {
			return zero, err
		}
		old := m.old
		value := old.Value
		if err := m.policy.observe(old.From, old.To, someValue(value), noneIncoming[V](), someValue(value)); err != nil {
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
func (m *orderedMerge[V, P, K]) checkpoint(check func() error) error {
	m.cancellationWork++
	if m.cancellationWork == 4096 {
		m.cancellationWork = 0
		return check()
	}
	return nil
}

// emit transforms one segment through the policy and appends the new
// value (Rust OrderedMerge::emit).
func (m *orderedMerge[V, P, K]) emit(store *DraftStore, from, to K, old optionalValue, incoming incomingValue[V]) error {
	new, err := m.policy.transform(store, old, incoming)
	if err != nil {
		return err
	}
	if err := m.policy.observe(from, to, old, incoming, new); err != nil {
		return err
	}
	if new.present {
		return m.output.emit(store, rangeRecord[K]{From: from, To: to, Value: new.value})
	}
	return nil
}

// accountOld charges the current base record into the old refcount run
// exactly once (Rust OrderedMerge::account_old).
func (m *orderedMerge[V, P, K]) accountOld(store *DraftStore) error {
	if !m.oldAccounted {
		if !m.hasOld {
			return corrupt("ordered merge lost its old range")
		}
		if m.hasOldRefcounts {
			if err := m.oldRefcounts.add(store, m.old.Value, -1); err != nil {
				return err
			}
		}
		m.oldAccounted = true
	}
	return nil
}

// advanceOld moves the base cursor to the next record (Rust
// OrderedMerge::advance_old).
func (m *orderedMerge[V, P, K]) advanceOld(store *DraftStore) error {
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
func (m *orderedMerge[V, P, K]) requireInput(incoming incomingRange[V, K]) error {
	if m.family.Less(incoming.to, incoming.from) {
		return corrupt("ordered merge input range is reversed")
	}
	if m.hasPreviousInputEnd && !m.family.Less(m.previousInputEnd, incoming.from) {
		return corrupt("ordered merge input ranges overlap or are out of order")
	}
	return nil
}

// mergeCoverage merges one draft-private coverage tree over the
// committed base generation (Rust merge_coverage): the coverage tree is
// consumed record by record through the draft-generation cursor and fed
// as the ordered merge input; the caller receives the input interval
// count plus the finished policy output. The coverage records are the
// timestamp refresh input values (uint32), so the merge input type is
// fixed at uint32 exactly like the Rust timestamp policies.
func mergeCoverage[P any, K any](store *DraftStore, source format.Meta, base format.Meta, codec rangeFamily[K], policy mergePolicy[uint32, P, K], check func() error, countContext string) (uint64, P, error) {
	var zero P
	coverage, err := newRangeCursor[K](store, source, codec, true)
	if err != nil {
		return 0, zero, err
	}
	merge, err := newOrderedMerge[uint32, P, K](store, base, codec, policy, check)
	if err != nil {
		return 0, zero, err
	}
	var inputIntervals uint64
	for {
		record, ok, err := coverage.next()
		if err != nil {
			return 0, zero, err
		}
		if !ok {
			break
		}
		inputIntervals, err = checkedAdd(inputIntervals, 1, countContext)
		if err != nil {
			return 0, zero, err
		}
		if err := merge.push(store, incomingRange[uint32, K]{from: record.From, to: record.To, value: record.Value}, check); err != nil {
			return 0, zero, err
		}
	}
	finished, err := merge.finish(store, check)
	if err != nil {
		return 0, zero, err
	}
	return inputIntervals, finished, nil
}

func orderedPrevious[K any](codec rangeFamily[K], key K, context string) (K, error) {
	previous, ok := codec.Previous(key)
	if !ok {
		var zero K
		return zero, overflow(context)
	}
	return previous, nil
}

func orderedNext[K any](codec rangeFamily[K], key K, context string) (K, error) {
	next, ok := codec.Next(key)
	if !ok {
		var zero K
		return zero, overflow(context)
	}
	return next, nil
}
