// First-seen and last-seen refresh semantics over the authoritative
// ordered range merge (Rust draft_store/timestamp_refresh.rs parity):
// the workflow coverage tree built by the constant-range input is merged
// over the committed base through the timestamp policies, producing the
// exact input-address count and the before/after value classification.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// TimestampMerge is the outcome of one first-seen or last-seen refresh
// merge (Rust TimestampMerge).
type TimestampMerge struct {
	InputIntervals uint64
	InputAddresses format.Cardinality129
	Comparison     Comparison
}

// timestampOutput is the finished policy output of one timestamp merge
// (Rust TimestampOutput).
type timestampOutput struct {
	inputAddresses format.Cardinality129
	comparison     Comparison
}

// timestampCounters accumulates the input-address count and the
// before/after value classification of one timestamp merge (Rust
// TimestampCounters over MapComparison). The comparison balances at
// finish exactly like the Rust MapComparison::finish.
type timestampCounters[K any] struct {
	inputAddresses format.Cardinality129
	comparison     Comparison
	codec          rangeFamily[K]
}

func (c *timestampCounters[K]) observe(from, to K, old, incoming, new optionalValue) (format.Cardinality129, error) {
	count, err := familyInclusiveCardinalityOf(c.codec, from, to)
	if err != nil {
		return format.CardinalityZero(), err
	}
	if incoming.present {
		c.inputAddresses, err = c.inputAddresses.Add(count)
		if err != nil {
			return format.CardinalityZero(), overflow("ordered merge address count")
		}
	}
	if old.present {
		c.comparison.Before, err = c.comparison.Before.Add(count)
		if err != nil {
			return format.CardinalityZero(), overflow("ordered merge address count")
		}
	}
	if new.present {
		c.comparison.After, err = c.comparison.After.Add(count)
		if err != nil {
			return format.CardinalityZero(), overflow("ordered merge address count")
		}
	}
	switch {
	case old.present && new.present && old.value == new.value:
		c.comparison.Unchanged, err = c.comparison.Unchanged.Add(count)
	case old.present && new.present:
		c.comparison.Changed, err = c.comparison.Changed.Add(count)
	case old.present:
		c.comparison.Removed, err = c.comparison.Removed.Add(count)
	case new.present:
		c.comparison.Added, err = c.comparison.Added.Add(count)
	}
	if err != nil {
		return format.CardinalityZero(), overflow("ordered merge address count")
	}
	return count, nil
}

// finish balances the classification like Rust MapComparison::finish:
// the before and after totals recomputed from the classes must equal the
// directly counted totals.
func (c *timestampCounters[K]) finish() (timestampOutput, error) {
	before, err := c.comparison.Unchanged.Add(c.comparison.Changed)
	if err != nil {
		return timestampOutput{}, overflow("ordered merge address count")
	}
	before, err = before.Add(c.comparison.Removed)
	if err != nil {
		return timestampOutput{}, overflow("ordered merge address count")
	}
	after, err := c.comparison.Unchanged.Add(c.comparison.Changed)
	if err != nil {
		return timestampOutput{}, overflow("ordered merge address count")
	}
	after, err = after.Add(c.comparison.Added)
	if err != nil {
		return timestampOutput{}, overflow("ordered merge address count")
	}
	if before.Compare(c.comparison.Before) != 0 || after.Compare(c.comparison.After) != 0 {
		return timestampOutput{}, corrupt("ordered merge counters do not balance")
	}
	return timestampOutput{inputAddresses: c.inputAddresses, comparison: c.comparison}, nil
}

// firstSeenPolicy preserves old values on covered addresses and stamps
// the refresh value on newly covered ones; uncovered addresses are
// removed and streamed through the removal observer (Rust
// FirstSeenPolicy over RemovalObserver).
type firstSeenPolicy[K any] struct {
	refreshValue uint32
	counters     timestampCounters[K]
	removals     removalObserver[K]
}

func newFirstSeenPolicy[K any](refreshValue uint32, codec rangeFamily[K]) *firstSeenPolicy[K] {
	return &firstSeenPolicy[K]{refreshValue: refreshValue, counters: timestampCounters[K]{codec: codec}, removals: noRemovals[K]{}}
}

func newFirstSeenPolicyWithRemovals[K any](refreshValue uint32, codec rangeFamily[K], removals removalObserver[K]) *firstSeenPolicy[K] {
	return &firstSeenPolicy[K]{refreshValue: refreshValue, counters: timestampCounters[K]{codec: codec}, removals: removals}
}

func (p *firstSeenPolicy[K]) transform(store *DraftStore, old optionalValue, incoming incomingValue[uint32]) (optionalValue, error) {
	switch {
	case old.present && incoming.present:
		return old, nil
	case !old.present && incoming.present:
		return someValue(p.refreshValue), nil
	default:
		return noneValue(), nil
	}
}

func (p *firstSeenPolicy[K]) observe(from, to K, old optionalValue, incoming incomingValue[uint32], new optionalValue) error {
	addresses, err := p.counters.observe(from, to, old, optionalValue{value: incoming.value, present: incoming.present}, new)
	if err != nil {
		return err
	}
	if old.present && !incoming.present {
		return p.removals.push(firstSeenRemoval[K]{from: from, to: to, firstSeen: old.value, addresses: addresses})
	}
	return nil
}

func (p *firstSeenPolicy[K]) finish() (timestampOutput, error) {
	if err := p.removals.finish(); err != nil {
		return timestampOutput{}, err
	}
	return p.counters.finish()
}

func (p *firstSeenPolicy[K]) preserveWithoutInput() bool { return false }

// firstSeenRemoval is one first-seen interval removed by a refresh
// (Rust FirstSeenRemoval<K>).
type firstSeenRemoval[K any] struct {
	from, to  K
	firstSeen uint32
	addresses format.Cardinality129
}

// removalObserver consumes first-seen removals as the merge produces
// them (Rust RemovalObserver): push receives each removal, finish
// flushes the tail.
type removalObserver[K any] interface {
	push(removal firstSeenRemoval[K]) error
	finish() error
}

// noRemovals discards removals (Rust NoRemovals).
type noRemovals[K any] struct{}

func (noRemovals[K]) push(firstSeenRemoval[K]) error { return nil }
func (noRemovals[K]) finish() error                  { return nil }

// firstSeenRemovalBatch is the bounded removal batch size (Rust
// REMOVAL_BATCH_CAPACITY).
const firstSeenRemovalBatch = 64

// batchedRemovals4 buffers first-seen removals into bounded IPv4
// batches and flushes them through the sink (Rust BatchedRemovals over
// FirstSeenRemoval<Ipv4Key>). The batch slice is borrowed for the
// synchronous sink call; the sink must not retain it.
type batchedRemovals4 struct {
	sink    FirstSeenRemoval4Sink
	records [firstSeenRemovalBatch]FirstSeenRemoval4
	length  int
}

func (b *batchedRemovals4) push(removal firstSeenRemoval[key4]) error {
	b.records[b.length] = FirstSeenRemoval4{From: uint32(removal.from), To: uint32(removal.to), FirstSeen: removal.firstSeen, Addresses: removal.addresses}
	b.length++
	if b.length == firstSeenRemovalBatch {
		return b.flush()
	}
	return nil
}

func (b *batchedRemovals4) flush() error {
	if b.length == 0 {
		return nil
	}
	if err := b.sink(b.records[:b.length]); err != nil {
		return err
	}
	b.length = 0
	return nil
}

func (b *batchedRemovals4) finish() error { return b.flush() }

// batchedRemovals6 buffers first-seen removals into bounded IPv6
// batches (Rust BatchedRemovals over FirstSeenRemoval<Ipv6Key>).
type batchedRemovals6 struct {
	sink    FirstSeenRemoval6Sink
	records [firstSeenRemovalBatch]FirstSeenRemoval6
	length  int
}

func (b *batchedRemovals6) push(removal firstSeenRemoval[key6]) error {
	b.records[b.length] = FirstSeenRemoval6{FromHi: removal.from.Hi, FromLo: removal.from.Lo, ToHi: removal.to.Hi, ToLo: removal.to.Lo, FirstSeen: removal.firstSeen, Addresses: removal.addresses}
	b.length++
	if b.length == firstSeenRemovalBatch {
		return b.flush()
	}
	return nil
}

func (b *batchedRemovals6) flush() error {
	if b.length == 0 {
		return nil
	}
	if err := b.sink(b.records[:b.length]); err != nil {
		return err
	}
	b.length = 0
	return nil
}

func (b *batchedRemovals6) finish() error { return b.flush() }

// lastSeenPolicy refreshes covered values to at least the refresh value,
// keeps recent absence, and expires absence at or below the cutoff (Rust
// LastSeenPolicy).
type lastSeenPolicy[K any] struct {
	refreshValue uint32
	cutoff       uint32
	counters     timestampCounters[K]
}

func newLastSeenPolicy[K any](refreshValue, cutoff uint32, codec rangeFamily[K]) *lastSeenPolicy[K] {
	return &lastSeenPolicy[K]{
		refreshValue: refreshValue,
		cutoff:       cutoff,
		counters:     timestampCounters[K]{codec: codec},
	}
}

func (p *lastSeenPolicy[K]) transform(store *DraftStore, old optionalValue, incoming incomingValue[uint32]) (optionalValue, error) {
	switch {
	case old.present && incoming.present:
		if old.value >= p.refreshValue {
			return old, nil
		}
		return someValue(p.refreshValue), nil
	case !old.present && incoming.present:
		return someValue(p.refreshValue), nil
	case old.present && old.value > p.cutoff:
		return old, nil
	default:
		return noneValue(), nil
	}
}

func (p *lastSeenPolicy[K]) observe(from, to K, old optionalValue, incoming incomingValue[uint32], new optionalValue) error {
	_, err := p.counters.observe(from, to, old, optionalValue{value: incoming.value, present: incoming.present}, new)
	return err
}

func (p *lastSeenPolicy[K]) finish() (timestampOutput, error) {
	return p.counters.finish()
}

func (p *lastSeenPolicy[K]) preserveWithoutInput() bool { return false }

// mergeFirstSeen merges the workflow coverage tree over the committed
// base with the first-seen policy (Rust DraftStore::merge_first_seen).
func (s *DraftStore) mergeFirstSeen(base format.Meta, refreshValue uint32, check func() error) (TimestampMerge, error) {
	if base.AddressFamily == format.AddressFamilyIPv4 {
		return mergeTimestamp(s, rangeCodec4{}, base, newFirstSeenPolicy(refreshValue, rangeCodec4{}), check)
	}
	return mergeTimestamp(s, rangeCodec6{}, base, newFirstSeenPolicy(refreshValue, rangeCodec6{}), check)
}

// mergeFirstSeenWithRemovals4 merges with the first-seen policy and
// streams every removed interval through the IPv4 sink in bounded
// batches (Rust DraftStore::merge_first_seen_with_removals over
// Ipv4Key). The sink family must match the database family (Rust
// K::FAMILY check); a mismatch reports the exact Rust error class and
// detail.
func (s *DraftStore) mergeFirstSeenWithRemovals4(base format.Meta, refreshValue uint32, sink FirstSeenRemoval4Sink, check func() error) (TimestampMerge, error) {
	if base.AddressFamily != format.AddressFamilyIPv4 {
		return TimestampMerge{}, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "removal sink family does not match the database"}
	}
	return mergeTimestamp(s, rangeCodec4{}, base, newFirstSeenPolicyWithRemovals(refreshValue, rangeCodec4{}, &batchedRemovals4{sink: sink}), check)
}

// mergeFirstSeenWithRemovals6 merges with the first-seen policy and
// streams every removed interval through the IPv6 sink in bounded
// batches (Rust DraftStore::merge_first_seen_with_removals over
// Ipv6Key).
func (s *DraftStore) mergeFirstSeenWithRemovals6(base format.Meta, refreshValue uint32, sink FirstSeenRemoval6Sink, check func() error) (TimestampMerge, error) {
	if base.AddressFamily != format.AddressFamilyIPv6 {
		return TimestampMerge{}, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "removal sink family does not match the database"}
	}
	return mergeTimestamp(s, rangeCodec6{}, base, newFirstSeenPolicyWithRemovals(refreshValue, rangeCodec6{}, &batchedRemovals6{sink: sink}), check)
}

// mergeLastSeen merges the workflow coverage tree over the committed
// base with the last-seen policy (Rust DraftStore::merge_last_seen).
func (s *DraftStore) mergeLastSeen(base format.Meta, refreshValue, cutoff uint32, check func() error) (TimestampMerge, error) {
	if base.AddressFamily == format.AddressFamilyIPv4 {
		return mergeTimestamp(s, rangeCodec4{}, base, newLastSeenPolicy(refreshValue, cutoff, rangeCodec4{}), check)
	}
	return mergeTimestamp(s, rangeCodec6{}, base, newLastSeenPolicy(refreshValue, cutoff, rangeCodec6{}), check)
}

// mergeTimestamp drives the timestamp policy through the coverage merge
// over the draft input tree and the committed base (Rust
// merge_timestamp_family): the draft meta is the input, the merge output
// replaces the draft range root, and the base tree retires inside the
// merge finish.
func mergeTimestamp[K any](s *DraftStore, codec rangeFamily[K], base format.Meta, policy mergePolicy[uint32, timestampOutput, K], check func() error) (TimestampMerge, error) {
	inputIntervals, finished, err := mergeCoverage[timestampOutput, K](s, s.draft.meta, base, codec, policy, check, "timestamp input intervals")
	if err != nil {
		return TimestampMerge{}, err
	}
	return TimestampMerge{
		InputIntervals: inputIntervals,
		InputAddresses: finished.inputAddresses,
		Comparison:     finished.comparison,
	}, nil
}

// FirstSeenRemoval4 is one IPv4 first-seen interval removed by a
// complete refresh (Rust FirstSeenRemoval<Ipv4Key>).
type FirstSeenRemoval4 struct {
	From, To  uint32
	FirstSeen uint32
	Addresses format.Cardinality129
}

// FirstSeenRemoval6 is one IPv6 first-seen interval removed by a
// complete refresh (Rust FirstSeenRemoval<Ipv6Key>).
type FirstSeenRemoval6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	FirstSeen                  uint32
	Addresses                  format.Cardinality129
}

// FirstSeenRemoval4Sink receives bounded batches of IPv4 first-seen
// removals (Rust FirstSeenRemovalSink<Ipv4Key>). The batch slice is
// borrowed for the synchronous call; the sink must not retain it.
type FirstSeenRemoval4Sink func(removals []FirstSeenRemoval4) error

// FirstSeenRemoval6Sink receives bounded batches of IPv6 first-seen
// removals (Rust FirstSeenRemovalSink<Ipv6Key>). The batch slice is
// borrowed for the synchronous call; the sink must not retain it.
type FirstSeenRemoval6Sink func(removals []FirstSeenRemoval6) error
