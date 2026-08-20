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

// DirectJoinCell is one exact mapped or unmapped direct-provider result.
// DirectValue is nil for the single unmapped cell of every feed and
// points at the direct value otherwise.
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
// buffer; a nil yield discards. The source must be a direct-value
// database of the scope's address family.
func (s *MembershipScope) JoinDirect(source *ImmutableReader, budget DirectJoinBudget, cellYield func([]DirectJoinCell) error, cancellation *CancellationToken) (DirectJoinReport, error) {
	if err := s.r.checkOpen(); err != nil {
		return DirectJoinReport{}, err
	}
	if err := source.checkOpen(); err != nil {
		return DirectJoinReport{}, err
	}
	sourceMeta := source.inner.Meta()
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
	report, err := s.r.inner.JoinDirect(s.data, s.family(), source.inner, limit, cancellation.check, func(batch []reader.DirectJoinCell) error {
		if cellYield == nil {
			return nil
		}
		out := make([]DirectJoinCell, len(batch))
		for i, record := range batch {
			out[i] = DirectJoinCell{
				Feed:        string(record.Feed),
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
// from one reusable per-operation buffer each; nil yields discard.
func (s *MembershipScope) JoinMembership(right *MembershipScope, crossYield func([]MembershipCrossCell) error, uncoveredYield func([]UncoveredFeed) error, cancellation *CancellationToken) (MembershipJoinReport, error) {
	if err := s.r.checkOpen(); err != nil {
		return MembershipJoinReport{}, err
	}
	if err := right.r.checkOpen(); err != nil {
		return MembershipJoinReport{}, err
	}
	if s.family() != right.family() {
		return MembershipJoinReport{}, &Error{Code: ErrorWrongAddressFamily, Detail: "membership join source families differ"}
	}
	report, err := s.r.inner.JoinMembership(
		s.data, right.data, s.family(), right.r.inner, cancellation.check,
		func(batch []reader.MembershipCrossCell) error {
			if crossYield == nil {
				return nil
			}
			out := make([]MembershipCrossCell, len(batch))
			for i, record := range batch {
				out[i] = MembershipCrossCell{
					Left:      string(record.Left),
					Right:     string(record.Right),
					Addresses: record.Addresses,
				}
			}
			return crossYield(out)
		},
		func(batch []reader.UncoveredFeed) error {
			if uncoveredYield == nil {
				return nil
			}
			out := make([]UncoveredFeed, len(batch))
			for i, record := range batch {
				out[i] = UncoveredFeed{
					Side:      record.Side,
					Feed:      string(record.Feed),
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
