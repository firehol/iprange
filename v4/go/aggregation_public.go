// Exact one-scan feed cardinality and overlap aggregation over one
// reusable membership scope (Rust membership_query/aggregation.rs
// parity). Every mode walks the scoped membership ranges once; the
// requested pair plan folds the same pass into exact per-pair overlaps.
// Sinks receive bounded batches from one reusable per-operation buffer
// (32 records), so delivery never allocates per result; nil sinks
// discard. The facade reuses one growable output slice per yield, so
// the conversion to the public batch types also allocates nothing
// steady-state (the per-distinct-name string cache is the only retained
// allocation). All operation heap charges mirror the Rust size_of model, so
// identical scopes admit identically. Feeding names are owned string
// copies; the mapped catalog views are converted here, at the package
// boundary, exactly once per delivered record.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// FeedCardinality is the exact address count of one selected feed.
type FeedCardinality struct {
	Feed      string
	Addresses format.Cardinality129
}

// FeedOverlap is the exact address overlap of one unordered feed pair.
type FeedOverlap struct {
	Left, Right string
	Addresses   format.Cardinality129
}

// MembershipAggregationReport is the exact work and output count of one
// completed membership scan.
type MembershipAggregationReport = reader.MembershipAggregationReport

// MembershipAggregationMode selects the pair work of one scoped
// membership scan. Construct the mode with one of Cardinalities,
// AllPairs, TargetAgainstScope, or SelectedPairs.
type MembershipAggregationMode struct {
	kind   reader.AggregationMode
	target string
	pairs  []FeedPair
}

// MembershipAggregationCardinalities reports every scoped feed count and
// no pairs (Rust MembershipAggregationMode::Cardinalities).
func MembershipAggregationCardinalities() MembershipAggregationMode {
	return MembershipAggregationMode{kind: reader.AggregationCardinalities}
}

// MembershipAggregationAllPairs reports every scoped feed count and
// every unordered feed pair overlap (Rust AllPairs).
func MembershipAggregationAllPairs() MembershipAggregationMode {
	return MembershipAggregationMode{kind: reader.AggregationAllPairs}
}

// MembershipAggregationTargetAgainstScope reports every scoped feed
// count and the overlap of one target feed with every other scoped feed
// (Rust TargetAgainstScope). Unknown names are NameNotFound; a name
// outside the scope is InvalidArgument.
func MembershipAggregationTargetAgainstScope(target string) MembershipAggregationMode {
	return MembershipAggregationMode{kind: reader.AggregationTargetAgainstScope, target: target}
}

// MembershipAggregationSelectedPairs reports every scoped feed count and
// the exact overlap of every requested unordered feed pair (Rust
// SelectedPairs). The list must be nonempty, unique, and free of
// self-pairs; unknown names are NameNotFound; names outside the scope
// are InvalidArgument.
func MembershipAggregationSelectedPairs(pairs []FeedPair) MembershipAggregationMode {
	return MembershipAggregationMode{kind: reader.AggregationSelectedPairs, pairs: pairs}
}

// Aggregate scans this scope once and delivers the exact feed counts and
// the requested pair overlaps in bounded batches. feedYield receives
// every scoped feed count (ascending feed index order); overlapYield
// receives the requested overlaps in ascending (left,right) order. A nil
// yield discards its channel; an error from either sink stops the
// operation and passes through unchanged. The operation heap is the
// scope budget minus its construction charge (Rust operation_heap).
func (s *MembershipScope) Aggregate(mode MembershipAggregationMode, feedYield func([]FeedCardinality) error, overlapYield func([]FeedOverlap) error, cancellation *CancellationToken) (MembershipAggregationReport, error) {
	if err := s.r.checkOpen(); err != nil {
		return MembershipAggregationReport{}, err
	}
	// Feed names repeat across batches and are views of the pinned
	// catalog records, so one owned string per distinct feed is shared
	// by every delivered record (the byte-keyed lookup converts on the
	// stack; only the cached string is retained).
	names := make(map[string]string)
	var cardOutput []FeedCardinality
	var overlapOutput []FeedOverlap
	report, err := s.r.core().AggregateScope(
		s.data, s.family(), mode.kind, mode.target, mode.pairs, cancellation.check,
		func(batch []reader.FeedCardinality) error {
			if feedYield == nil {
				return nil
			}
			// One growable output slice reused across batches: the
			// yield contract keeps each batch valid only until the
			// next batch is delivered (Rust batch-lifetime parity).
			if cap(cardOutput) < len(batch) {
				cardOutput = make([]FeedCardinality, len(batch))
			}
			out := cardOutput[:len(batch)]
			for i, record := range batch {
				name, ok := names[string(record.Feed)]
				if !ok {
					name = string(record.Feed)
					names[name] = name
				}
				out[i] = FeedCardinality{Feed: name, Addresses: record.Addresses}
			}
			return feedYield(out)
		},
		func(batch []reader.FeedOverlap) error {
			if overlapYield == nil {
				return nil
			}
			if cap(overlapOutput) < len(batch) {
				overlapOutput = make([]FeedOverlap, len(batch))
			}
			out := overlapOutput[:len(batch)]
			for i, record := range batch {
				left, ok := names[string(record.Left)]
				if !ok {
					left = string(record.Left)
					names[left] = left
				}
				right, ok := names[string(record.Right)]
				if !ok {
					right = string(record.Right)
					names[right] = right
				}
				out[i] = FeedOverlap{Left: left, Right: right, Addresses: record.Addresses}
			}
			return overlapYield(out)
		},
	)
	if err != nil {
		return MembershipAggregationReport{}, publicError(err)
	}
	return report, nil
}
