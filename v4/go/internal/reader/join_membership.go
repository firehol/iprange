package reader

// Ordered cross-membership join and per-side uncovered coverage (Rust
// membership_query/join/membership.rs parity). One sweep over two
// selected-range streams produces exactly three output families: every
// left x right overlap cell, and each side's uncovered address totals,
// all emitted in bounded batches.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// UncoveredSide identifies the side owning one uncovered feed result.
type UncoveredSide uint8

const (
	UncoveredLeft  UncoveredSide = 0
	UncoveredRight UncoveredSide = 1
)

// MembershipCrossCell is one exact cross-file membership overlap. The
// names alias the catalog mapping; only the root facade converts them to
// owned strings.
type MembershipCrossCell struct {
	Left, Right []byte
	Addresses   format.Cardinality129
}

// UncoveredFeed is one feed's coverage not covered by any selected feed
// on the other side. The name aliases the catalog mapping.
type UncoveredFeed struct {
	Side      UncoveredSide
	Feed      []byte
	Addresses format.Cardinality129
}

// MembershipJoinReport is the exact traversal and selected-union facts
// of one membership join.
type MembershipJoinReport struct {
	LeftRangeCount          uint64
	RightRangeCount         uint64
	JoinedSegmentCount      uint64
	LeftAddresses           format.Cardinality129
	RightAddresses          format.Cardinality129
	OverlapAddresses        format.Cardinality129
	LeftUncoveredAddresses  format.Cardinality129
	RightUncoveredAddresses format.Cardinality129
	CrossResultCount        uint64
	UncoveredResultCount    uint64
}

// membershipJoinResults holds the exact cross and uncovered totals (Rust
// Results).
type membershipJoinResults struct {
	cross          []format.Cardinality129
	leftUncovered  []format.Cardinality129
	rightUncovered []format.Cardinality129
}

type membershipJoinStats struct {
	segments   uint64
	left       format.Cardinality129
	right      format.Cardinality129
	overlap    format.Cardinality129
	leftUncov  format.Cardinality129
	rightUncov format.Cardinality129
}

type membershipJoinSweep struct {
	leftRanges  *selectedRanges
	rightRanges *selectedRanges
	ops         rangeOps
	left        *selectedRange
	right       *selectedRange
	results     *membershipJoinResults
	stats       membershipJoinStats
	rightWidth  int
}

// membershipCoverage marks which side owns one segment.
type membershipCoverage uint8

const (
	coverageLeft membershipCoverage = iota
	coverageRight
	coverageBoth
)

// joinMembership runs the full membership join (Rust membership.rs run).
func (r *ImmutableReader) JoinMembership(scope, right *ScopeData, family uint8, rightReader *ImmutableReader, check checkpoint, emitCross func([]MembershipCrossCell) error, emitUncovered func([]UncoveredFeed) error) (MembershipJoinReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return MembershipJoinReport{}, err
		}
	}
	heapBytes, err := scope.operationHeapReserved(0)
	if err != nil {
		return MembershipJoinReport{}, err
	}
	heap := newOperationHeap(heapBytes)

	crossCount, ok := mulChecked(uint64(len(scope.entries)), uint64(len(right.entries)))
	if !ok {
		return MembershipJoinReport{}, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership join result heap"}
	}
	results := &membershipJoinResults{}
	if err := heap.filled(crossCount, rustCardSize, "membership join result heap"); err != nil {
		return MembershipJoinReport{}, err
	}
	results.cross = make([]format.Cardinality129, crossCount)
	if err := heap.filled(uint64(len(scope.entries)), rustCardSize, "membership join result heap"); err != nil {
		return MembershipJoinReport{}, err
	}
	results.leftUncovered = make([]format.Cardinality129, len(scope.entries))
	if err := heap.filled(uint64(len(right.entries)), rustCardSize, "membership join result heap"); err != nil {
		return MembershipJoinReport{}, err
	}
	results.rightUncovered = make([]format.Cardinality129, len(right.entries))

	var stats membershipJoinStats
	if family == format.AddressFamilyIPv4 {
		leftCursor, err := r.NewMembershipRangeCursor4()
		if err != nil {
			return MembershipJoinReport{}, err
		}
		rightCursor, err := rightReader.NewMembershipRangeCursor4()
		if err != nil {
			return MembershipJoinReport{}, err
		}
		leftStream := &membershipIterator{cursor: leftCursor.state, family: format.AddressFamilyIPv4}
		rightStream := &membershipIterator{cursor: rightCursor.state, family: format.AddressFamilyIPv4}
		stats, err = joinMembershipFamily(r, scope, leftStream, rightReader, right, rightStream, ops4, results, heap, check)
		if err != nil {
			return MembershipJoinReport{}, err
		}
	} else {
		leftCursor, err := r.NewMembershipRangeCursor6()
		if err != nil {
			return MembershipJoinReport{}, err
		}
		rightCursor, err := rightReader.NewMembershipRangeCursor6()
		if err != nil {
			return MembershipJoinReport{}, err
		}
		leftStream := &membershipIterator{cursor: leftCursor.state, family: format.AddressFamilyIPv6}
		rightStream := &membershipIterator{cursor: rightCursor.state, family: format.AddressFamilyIPv6}
		stats, err = joinMembershipFamily(r, scope, leftStream, rightReader, right, rightStream, ops6, results, heap, check)
		if err != nil {
			return MembershipJoinReport{}, err
		}
	}

	if err := emitCrossCells(scope, right, results.cross, check, emitCross); err != nil {
		return MembershipJoinReport{}, err
	}
	if err := emitUncoveredFeeds(scope, right, results, check, emitUncovered); err != nil {
		return MembershipJoinReport{}, err
	}
	return MembershipJoinReport{
		LeftRangeCount:          r.meta.RangeRecordCount,
		RightRangeCount:         rightReader.meta.RangeRecordCount,
		JoinedSegmentCount:      stats.segments,
		LeftAddresses:           stats.left,
		RightAddresses:          stats.right,
		OverlapAddresses:        stats.overlap,
		LeftUncoveredAddresses:  stats.leftUncov,
		RightUncoveredAddresses: stats.rightUncov,
		CrossResultCount:        crossCount,
		UncoveredResultCount:    uint64(len(scope.entries) + len(right.entries)),
	}, nil
}

func joinMembershipFamily(leftR *ImmutableReader, scope *ScopeData, leftStream *membershipIterator, rightR *ImmutableReader, right *ScopeData, rightStream *membershipIterator, ops rangeOps, results *membershipJoinResults, heap *operationHeap, check checkpoint) (membershipJoinStats, error) {
	leftRanges, err := newSelectedRanges(leftR, scope, leftStream, ops, heap)
	if err != nil {
		return membershipJoinStats{}, err
	}
	rightRanges, err := newSelectedRanges(rightR, right, rightStream, ops, heap)
	if err != nil {
		return membershipJoinStats{}, err
	}
	cacheBytes := heap.remainingBytes() / 2
	if err := leftRanges.enableCache(heap, cacheBytes); err != nil {
		return membershipJoinStats{}, err
	}
	if err := rightRanges.enableCache(heap, cacheBytes); err != nil {
		return membershipJoinStats{}, err
	}
	sweep, err := newMembershipJoinSweep(len(right.entries), leftRanges, rightRanges, ops, results, check)
	if err != nil {
		return membershipJoinStats{}, err
	}
	stats, err := sweep.run(check)
	if err != nil {
		return membershipJoinStats{}, err
	}
	if leftRanges.count() != leftR.meta.RangeRecordCount ||
		rightRanges.count() != rightR.meta.RangeRecordCount {
		return membershipJoinStats{}, corrupt("membership join range count disagrees")
	}
	return stats, nil
}

func newMembershipJoinSweep(rightWidth int, leftRanges, rightRanges *selectedRanges, ops rangeOps, results *membershipJoinResults, check checkpoint) (*membershipJoinSweep, error) {
	work.InputSourcePass(2)
	left, err := leftRanges.next(check)
	if err != nil {
		return nil, err
	}
	right, err := rightRanges.next(check)
	if err != nil {
		return nil, err
	}
	return &membershipJoinSweep{
		leftRanges:  leftRanges,
		rightRanges: rightRanges,
		ops:         ops,
		left:        left,
		right:       right,
		results:     results,
		rightWidth:  rightWidth,
	}, nil
}

func (s *membershipJoinSweep) run(check checkpoint) (membershipJoinStats, error) {
	for s.left != nil || s.right != nil {
		if err := s.step(check); err != nil {
			return membershipJoinStats{}, err
		}
	}
	if check != nil {
		if err := check(); err != nil {
			return membershipJoinStats{}, err
		}
	}
	return s.stats, nil
}

func (s *membershipJoinSweep) step(check checkpoint) error {
	switch {
	case s.left != nil && s.right == nil:
		return s.consumeLeft(*s.left, s.left.to, check)
	case s.left == nil && s.right != nil:
		return s.consumeRight(*s.right, s.right.to, check)
	case s.left != nil && s.right != nil && s.left.to.Less(s.right.from):
		return s.consumeLeft(*s.left, s.left.to, check)
	case s.left != nil && s.right != nil && s.right.to.Less(s.left.from):
		return s.consumeRight(*s.right, s.right.to, check)
	case s.left != nil && s.right != nil && s.left.from.Less(s.right.from):
		end, err := s.ops.previous(s.right.from)
		if err != nil {
			return err
		}
		return s.consumeLeft(*s.left, end, check)
	case s.left != nil && s.right != nil && s.right.from.Less(s.left.from):
		end, err := s.ops.previous(s.left.from)
		if err != nil {
			return err
		}
		return s.consumeRight(*s.right, end, check)
	case s.left != nil && s.right != nil:
		return s.consumeOverlap(*s.left, *s.right, check)
	}
	return nil
}

func (s *membershipJoinSweep) consumeLeft(rangeRecord selectedRange, to addrKey, check checkpoint) error {
	if err := s.accumulate(s.leftRanges.present(), s.rightRanges.present(), coverageLeft, rangeRecord.from, to, check); err != nil {
		return err
	}
	if rangeRecord.to == to {
		next, err := s.leftRanges.next(check)
		if err != nil {
			return err
		}
		s.left = next
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		rangeRecord.from = next
		s.left = &rangeRecord
	}
	return nil
}

func (s *membershipJoinSweep) consumeRight(rangeRecord selectedRange, to addrKey, check checkpoint) error {
	if err := s.accumulate(s.leftRanges.present(), s.rightRanges.present(), coverageRight, rangeRecord.from, to, check); err != nil {
		return err
	}
	if rangeRecord.to == to {
		next, err := s.rightRanges.next(check)
		if err != nil {
			return err
		}
		s.right = next
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		rangeRecord.from = next
		s.right = &rangeRecord
	}
	return nil
}

func (s *membershipJoinSweep) consumeOverlap(left, right selectedRange, check checkpoint) error {
	to := left.to
	if right.to.Less(to) {
		to = right.to
	}
	if err := s.accumulate(s.leftRanges.present(), s.rightRanges.present(), coverageBoth, left.from, to, check); err != nil {
		return err
	}
	if left.to == to {
		next, err := s.leftRanges.next(check)
		if err != nil {
			return err
		}
		s.left = next
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		left.from = next
		s.left = &left
	}
	if right.to == to {
		next, err := s.rightRanges.next(check)
		if err != nil {
			return err
		}
		s.right = next
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		right.from = next
		s.right = &right
	}
	return nil
}

// accumulate folds one segment into the cross/uncovered totals (Rust
// Accumulator::consume).
func (s *membershipJoinSweep) accumulate(left, right []uint32, coverage membershipCoverage, from, to addrKey, check checkpoint) error {
	leftPresent := []uint32(nil)
	rightPresent := []uint32(nil)
	switch coverage {
	case coverageLeft:
		leftPresent = left
	case coverageRight:
		rightPresent = right
	case coverageBoth:
		leftPresent = left
		rightPresent = right
	}
	if len(leftPresent) == 0 && len(rightPresent) == 0 {
		return nil
	}
	count, err := s.ops.inclusive(from, to)
	if err != nil {
		return err
	}
	s.stats.segments, err = increment64(s.stats.segments, "membership join segment count")
	if err != nil {
		return err
	}
	if len(leftPresent) != 0 {
		s.stats.left, err = addCard(s.stats.left, count)
		if err != nil {
			return err
		}
	}
	if len(rightPresent) != 0 {
		s.stats.right, err = addCard(s.stats.right, count)
		if err != nil {
			return err
		}
	}
	switch {
	case len(leftPresent) != 0 && len(rightPresent) != 0:
		if err := s.addCross(leftPresent, rightPresent, count, check); err != nil {
			return err
		}
	case len(leftPresent) != 0:
		if err := s.addLeftUncovered(leftPresent, count, check); err != nil {
			return err
		}
	case len(rightPresent) != 0:
		if err := s.addRightUncovered(rightPresent, count, check); err != nil {
			return err
		}
	}
	work.JoinAdvance(1)
	return nil
}

func (s *membershipJoinSweep) addCross(left, right []uint32, count format.Cardinality129, check checkpoint) error {
	var err error
	s.stats.overlap, err = addCard(s.stats.overlap, count)
	if err != nil {
		return err
	}
	for step, l := range left {
		for _, r := range right {
			if err := checkEvery(step, check); err != nil {
				return err
			}
			index := int(l)*s.rightWidth + int(r)
			var err error
			s.results.cross[index], err = addCard(s.results.cross[index], count)
			if err != nil {
				return err
			}
			work.AggregationContribution(1)
		}
	}
	return nil
}

func (s *membershipJoinSweep) addLeftUncovered(present []uint32, count format.Cardinality129, check checkpoint) error {
	var err error
	s.stats.leftUncov, err = addCard(s.stats.leftUncov, count)
	if err != nil {
		return err
	}
	return addUncovered(s.results.leftUncovered, present, count, check)
}

func (s *membershipJoinSweep) addRightUncovered(present []uint32, count format.Cardinality129, check checkpoint) error {
	var err error
	s.stats.rightUncov, err = addCard(s.stats.rightUncov, count)
	if err != nil {
		return err
	}
	return addUncovered(s.results.rightUncovered, present, count, check)
}

func addUncovered(output []format.Cardinality129, present []uint32, count format.Cardinality129, check checkpoint) error {
	for step, feed := range present {
		if err := checkEvery(step, check); err != nil {
			return err
		}
		var err error
		output[feed], err = addCard(output[feed], count)
		if err != nil {
			return err
		}
		work.AggregationContribution(1)
	}
	return nil
}

// emitCrossCells delivers the cross cells in left-major index order
// (Rust emit_cross).
func emitCrossCells(scope, right *ScopeData, totals []format.Cardinality129, check checkpoint, emit func([]MembershipCrossCell) error) error {
	if len(totals) == 0 {
		return nil
	}
	batch := make([]MembershipCrossCell, 0, joinResultBatch)
	index := 0
	for _, leftEntry := range scope.entries {
		for _, rightEntry := range right.entries {
			batch = append(batch, MembershipCrossCell{
				Left:      leftEntry.Name,
				Right:     rightEntry.Name,
				Addresses: totals[index],
			})
			index++
			if len(batch) == joinResultBatch {
				if err := flushCrossCells(check, emit, batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
	}
	if len(batch) != 0 {
		return flushCrossCells(check, emit, batch)
	}
	return nil
}

func flushCrossCells(check checkpoint, emit func([]MembershipCrossCell) error, batch []MembershipCrossCell) error {
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if emit != nil {
		if err := emit(batch); err != nil {
			return err
		}
	}
	work.AggregationResult(uint64(len(batch)))
	return nil
}

// emitUncoveredFeeds delivers the per-side uncovered feeds, left side
// first (Rust emit_uncovered).
func emitUncoveredFeeds(scope, right *ScopeData, results *membershipJoinResults, check checkpoint, emit func([]UncoveredFeed) error) error {
	if len(scope.entries) == 0 && len(right.entries) == 0 {
		return nil
	}
	leftBatch := make([]UncoveredFeed, 0, joinResultBatch)
	for i, entry := range scope.entries {
		leftBatch = append(leftBatch, UncoveredFeed{Side: UncoveredLeft, Feed: entry.Name, Addresses: results.leftUncovered[i]})
		if len(leftBatch) == joinResultBatch {
			if err := flushUncovered(check, emit, leftBatch); err != nil {
				return err
			}
			leftBatch = leftBatch[:0]
		}
	}
	if len(leftBatch) != 0 {
		if err := flushUncovered(check, emit, leftBatch); err != nil {
			return err
		}
	}
	rightBatch := make([]UncoveredFeed, 0, joinResultBatch)
	for i, entry := range right.entries {
		rightBatch = append(rightBatch, UncoveredFeed{Side: UncoveredRight, Feed: entry.Name, Addresses: results.rightUncovered[i]})
		if len(rightBatch) == joinResultBatch {
			if err := flushUncovered(check, emit, rightBatch); err != nil {
				return err
			}
			rightBatch = rightBatch[:0]
		}
	}
	if len(rightBatch) != 0 {
		return flushUncovered(check, emit, rightBatch)
	}
	return nil
}

func flushUncovered(check checkpoint, emit func([]UncoveredFeed) error, batch []UncoveredFeed) error {
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if emit != nil {
		if err := emit(batch); err != nil {
			return err
		}
	}
	work.AggregationResult(uint64(len(batch)))
	return nil
}
