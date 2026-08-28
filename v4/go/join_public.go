// Ordered provider joins over one reusable membership scope (Rust
// membership_query/join.rs parity). JoinDirect merges the scope's
// selected runs with one pinned direct provider and interns every exact
// (feed, direct-value) result cell under an explicit budget;
// JoinMembership merges two scopes into exact cross overlaps plus
// per-side uncovered coverage. Both are read-only, allocate nothing per
// result, and fail closed on family and value-kind mismatches. Feeding
// names are owned string copies converted at this package boundary.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// DirectJoinSource is the pinned direct-value side of one
// membership/direct join (Rust DirectJoinSource): an immutable or a
// live reader, pinned for the join duration by the caller's open
// handle. The zero value names no reader and is refused.
type DirectJoinSource struct {
	immutable *ImmutableReader
	live      *LiveReader
}

// DirectJoinSourceImmutable builds the immutable-reader variant (Rust
// DirectJoinSource::Immutable).
func DirectJoinSourceImmutable(r *ImmutableReader) DirectJoinSource {
	return DirectJoinSource{immutable: r}
}

// DirectJoinSourceLive builds the live-reader variant (Rust
// DirectJoinSource::Live): the join resolves the pinned generation
// through the live reader's open state, so a closing or closed reader
// reports WrongState before any page is touched.
func DirectJoinSourceLive(r *LiveReader) DirectJoinSource {
	return DirectJoinSource{live: r}
}

// core resolves the pinned reader core of the selected variant with
// the variant's open proof (Rust Source::new: the immutable core is
// unconditionally open, the live core requires the open state).
func (s DirectJoinSource) core() (*reader.ImmutableReader, error) {
	if s.immutable != nil {
		if err := s.immutable.checkOpen(); err != nil {
			return nil, err
		}
		return s.immutable.inner, nil
	}
	if s.live != nil {
		if err := s.live.checkOpen(); err != nil {
			return nil, err
		}
		return s.live.core(), nil
	}
	return nil, &Error{Code: ErrorInvalidArgument, Detail: "direct join source is empty"}
}

// DirectJoinCell is one exact mapped or unmapped direct-provider result.
// DirectValue is nil for the single unmapped cell of every feed and
// points at the direct value otherwise. The pointed value is owned by
// the batch's reusable buffer: it stays valid until the next batch is
// delivered to the callback (Rust Option<u32> batch-lifetime parity).
type DirectJoinCell struct {
	Feed        string
	DirectValue *uint32
	Addresses   format.Cardinality129
}

// DirectJoinReport is the exact traversal and union-coverage facts of
// one membership/direct join.
type DirectJoinReport = reader.DirectJoinReport

// UncoveredSide identifies the side owning one uncovered feed result.
type UncoveredSide = reader.UncoveredSide

// UncoveredLeft identifies a left-side uncovered feed.
const UncoveredLeft = reader.UncoveredLeft

// UncoveredRight identifies a right-side uncovered feed.
const UncoveredRight = reader.UncoveredRight

// MembershipCrossCell is one exact cross-file membership overlap.
type MembershipCrossCell struct {
	Left, Right string
	Addresses   format.Cardinality129
}

// UncoveredFeed is one feed's coverage not covered by any selected feed
// on the other side.
type UncoveredFeed struct {
	Side      UncoveredSide
	Feed      string
	Addresses format.Cardinality129
}

// MembershipJoinReport is the exact traversal and selected-union facts
// of one membership join.
type MembershipJoinReport = reader.MembershipJoinReport

// DirectJoinBudget bounds the distinct (feed, direct-value) result cells
// retained by one membership/direct join.
type DirectJoinBudget struct {
	MaxResultCells uint64
}

// JoinDirect merges this scope with one pinned direct provider
// (same address family) and delivers every exact result cell in
// ascending (feed, direct-value) order. A cell with a nil direct value
// is the feed's unmapped total; the budget bounds the distinct cell
// count, and exceeding it fails with ErrorInsufficientResourceBudget.
// cellYield receives bounded batches from one reusable per-operation
// buffer; a nil yield discards. The facade reuses one growable output
// slice across batches, so steady-state conversion allocates nothing
// (the per-distinct-name string cache is the only retained allocation).
// The source must be a direct-value
// database of the scope's address family; the live variant resolves
// the join through the reader's pinned generation.
func (s *MembershipScope) JoinDirect(source DirectJoinSource, budget DirectJoinBudget, cellYield func([]DirectJoinCell) error, cancellation *CancellationToken) (DirectJoinReport, error) {
	if s == nil {
		return DirectJoinReport{}, &Error{Code: ErrorInvalidArgument, Detail: "membership scope is required"}
	}
	if err := s.r.checkOpen(); err != nil {
		return DirectJoinReport{}, err
	}
	sourceReader, err := source.core()
	if err != nil {
		return DirectJoinReport{}, err
	}
	sourceMeta := sourceReader.Meta()
	if sourceMeta.ValueKind != format.ValueKindDirect {
		return DirectJoinReport{}, &Error{Code: ErrorWrongValueKind, Detail: "membership/direct join requires a direct source"}
	}
	if sourceMeta.AddressFamily != s.family() {
		return DirectJoinReport{}, &Error{Code: ErrorWrongAddressFamily, Detail: "direct join source family differs"}
	}
	limit, err := budgetAsLimit(budget.MaxResultCells)
	if err != nil {
		return DirectJoinReport{}, err
	}
	names := make(map[string]string)
	var directOutput []DirectJoinCell
	report, err := s.r.core().JoinDirect(s.data, s.family(), sourceReader, limit, cancellation.check, func(batch []reader.DirectJoinCell) error {
		if cellYield == nil {
			return nil
		}
		// One growable output slice reused across batches (Rust
		// batch-lifetime parity: a batch stays valid only until the
		// next batch is delivered).
		if cap(directOutput) < len(batch) {
			directOutput = make([]DirectJoinCell, len(batch))
		}
		out := directOutput[:len(batch)]
		for i, record := range batch {
			name, ok := names[string(record.Feed)]
			if !ok {
				name = string(record.Feed)
				names[name] = name
			}
			out[i] = DirectJoinCell{
				Feed:        name,
				DirectValue: record.DirectValue,
				Addresses:   record.Addresses,
			}
		}
		return cellYield(out)
	})
	if err != nil {
		return DirectJoinReport{}, publicError(err)
	}
	return report, nil
}

// JoinMembership merges this scope with another pinned scope of the
// same address family and delivers the exact cross overlaps (ascending
// left feed, then right feed) and the per-side uncovered feeds (left
// side first). crossYield and uncoveredYield receive bounded batches
// from one reusable per-operation buffer each; nil yields discard. Both
// facades reuse one growable output slice across batches, so
// steady-state conversion allocates nothing (the per-distinct-name
// string cache is the only retained allocation).
func (s *MembershipScope) JoinMembership(right *MembershipScope, crossYield func([]MembershipCrossCell) error, uncoveredYield func([]UncoveredFeed) error, cancellation *CancellationToken) (MembershipJoinReport, error) {
	if s == nil {
		return MembershipJoinReport{}, &Error{Code: ErrorInvalidArgument, Detail: "membership scope is required"}
	}
	if right == nil {
		return MembershipJoinReport{}, &Error{Code: ErrorInvalidArgument, Detail: "membership join requires a right scope"}
	}
	if err := s.r.checkOpen(); err != nil {
		return MembershipJoinReport{}, err
	}
	if err := right.r.checkOpen(); err != nil {
		return MembershipJoinReport{}, err
	}
	if s.family() != right.family() {
		return MembershipJoinReport{}, &Error{Code: ErrorWrongAddressFamily, Detail: "membership join source families differ"}
	}
	names := make(map[string]string)
	var crossOutput []MembershipCrossCell
	var uncoveredOutput []UncoveredFeed
	report, err := s.r.core().JoinMembership(
		s.data, right.data, s.family(), right.r.core(), cancellation.check,
		func(batch []reader.MembershipCrossCell) error {
			if crossYield == nil {
				return nil
			}
			if cap(crossOutput) < len(batch) {
				crossOutput = make([]MembershipCrossCell, len(batch))
			}
			out := crossOutput[:len(batch)]
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
				out[i] = MembershipCrossCell{Left: left, Right: right, Addresses: record.Addresses}
			}
			return crossYield(out)
		},
		func(batch []reader.UncoveredFeed) error {
			if uncoveredYield == nil {
				return nil
			}
			if cap(uncoveredOutput) < len(batch) {
				uncoveredOutput = make([]UncoveredFeed, len(batch))
			}
			out := uncoveredOutput[:len(batch)]
			for i, record := range batch {
				name, ok := names[string(record.Feed)]
				if !ok {
					name = string(record.Feed)
					names[name] = name
				}
				out[i] = UncoveredFeed{
					Side:      record.Side,
					Feed:      name,
					Addresses: record.Addresses,
				}
			}
			return uncoveredYield(out)
		},
	)
	if err != nil {
		return MembershipJoinReport{}, publicError(err)
	}
	return report, nil
}

// budgetAsLimit converts the result-cell budget to an int limit,
// refusing values beyond the platform width exactly like Rust's usize
// conversion.
func budgetAsLimit(cells uint64) (int, error) {
	if uint64(int(^uint(0)>>1)) < cells {
		return 0, &Error{Code: ErrorInsufficientResourceBudget, Detail: "direct join result cells"}
	}
	return int(cells), nil
}
