package reader

// Ordered membership/direct join with bounded result interning (Rust
// membership_query/join/direct.rs parity). One sweep merges the scope's
// selected membership runs with the direct provider's ranges; every
// selected (feed, direct-value) pair folds the mapped segment count into
// one bounded open-addressing table, which is sorted and emitted in
// batches at the end.

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const joinResultBatch = 32

// DirectJoinCell is one exact mapped or unmapped direct-provider result.
// The feed name aliases the catalog mapping; only the root facade
// converts it to an owned string.
type DirectJoinCell struct {
	Feed        []byte
	DirectValue *uint32
	Addresses   format.Cardinality129
}

// DirectJoinReport is the exact traversal and union-coverage facts of
// one membership/direct join.
type DirectJoinReport struct {
	MembershipRangeCount uint64
	DirectRangesVisited  uint64
	JoinedSegmentCount   uint64
	SelectedAddresses    format.Cardinality129
	MappedAddresses      format.Cardinality129
	UnmappedAddresses    format.Cardinality129
	ResultCellCount      uint64
}

// joinDirectTable is the bounded open-addressing result intern table
// (Rust direct.rs Table).
type joinDirectTable struct {
	cells []joinDirectCell
	slots []joinDirectSlot
	mask  uint64
	limit int
}

type joinDirectCell struct {
	feed      uint32
	direct    uint64
	addresses format.Cardinality129
}

type joinDirectSlot struct {
	feed        uint32
	direct      uint64
	cellPlusOne uint64
}

// newJoinDirectTable sizes the table under the modeled heap (Rust
// Table::new): the cells vector is charged at the budget limit, the
// slots at the next power of two above twice the limit.
func newJoinDirectTable(limit int, heap *operationHeap) (*joinDirectTable, error) {
	if err := heap.filled(uint64(limit), rustCellSize, "direct join result heap"); err != nil {
		return nil, err
	}
	table := &joinDirectTable{cells: make([]joinDirectCell, 0, limit), limit: limit}
	if limit == 0 {
		return table, nil
	}
	slotsLen, ok := nextPowerOfTwoChecked(uint64(limit) * 2)
	if !ok {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "direct join result heap"}
	}
	if err := heap.filled(slotsLen, rustSlotSize, "direct join result heap"); err != nil {
		return nil, err
	}
	table.slots = make([]joinDirectSlot, slotsLen)
	table.mask = slotsLen - 1
	return table, nil
}

// add folds one mapped segment into the table (Rust Table::add). The
// direct value is encoded value+1 so zero means absent.
func (t *joinDirectTable) add(feed uint32, direct uint64, count format.Cardinality129, check checkpoint) error {
	if t.limit == 0 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "direct join result cells"}
	}
	slot := joinCellHash(feed, direct) & t.mask
	var probes uint64
	for {
		if probes&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		current := t.slots[slot]
		if current.cellPlusOne == 0 {
			if len(t.cells) == t.limit {
				return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "direct join result cells"}
			}
			cell := len(t.cells)
			t.cells = append(t.cells, joinDirectCell{feed: feed, direct: direct, addresses: count})
			t.slots[slot] = joinDirectSlot{feed: feed, direct: direct, cellPlusOne: uint64(cell) + 1}
			return nil
		}
		if current.feed == feed && current.direct == direct {
			var err error
			t.cells[current.cellPlusOne-1].addresses, err = addCard(t.cells[current.cellPlusOne-1].addresses, count)
			if err != nil {
				return err
			}
			return nil
		}
		slot = (slot + 1) & t.mask
		probes++
	}
}

// joinCellHash is the Rust direct.rs Table hash.
func joinCellHash(feed uint32, direct uint64) uint64 {
	value := direct ^ uint64(feed)*0x9e37_79b9_7f4a_7c15
	value ^= value >> 30
	value *= 0xbf58_476d_1ce4_e5b9
	value ^= value >> 27
	return value
}

// joinDirectStats accumulates the sweep facts (Rust Stats).
type joinDirectStats struct {
	directRanges uint64
	segments     uint64
	selected     format.Cardinality129
	mapped       format.Cardinality129
	unmapped     format.Cardinality129
}

// joinDirectSweep merges the selected membership runs with one direct
// provider cursor (Rust direct.rs Sweep).
type joinDirectSweep struct {
	membership *selectedRanges
	direct     *directIterator
	ops        rangeOps
	left       *selectedRange
	right      *directRangeFrame
	table      *joinDirectTable
	stats      joinDirectStats
}

// joinDirect runs the full direct join (Rust direct.rs run): family and
// capacity setup, one sweep, the range-count agreement, and the emitted
// table.
func (r *ImmutableReader) JoinDirect(scope *ScopeData, family uint8, source *ImmutableReader, limit int, check checkpoint, emit func([]DirectJoinCell) error) (DirectJoinReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return DirectJoinReport{}, err
		}
	}
	if len(scope.entries) == 0 {
		return DirectJoinReport{}, nil
	}
	heapBytes, err := scope.operationHeapReserved(0)
	if err != nil {
		return DirectJoinReport{}, err
	}
	heap := newOperationHeap(heapBytes)
	table, err := newJoinDirectTable(limit, heap)
	if err != nil {
		return DirectJoinReport{}, err
	}

	var membership *selectedRanges
	var sweep *joinDirectSweep
	if family == format.AddressFamilyIPv4 {
		cursor, err := r.NewMembershipRangeCursor4()
		if err != nil {
			return DirectJoinReport{}, err
		}
		stream := &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv4}
		membership, err = newSelectedRanges(r, scope, stream, ops4, heap)
		if err != nil {
			return DirectJoinReport{}, err
		}
		if err := membership.enableCache(heap, heap.remainingBytes()); err != nil {
			return DirectJoinReport{}, err
		}
		directCursor, err := source.NewDirectCursor4(RangeForward)
		if err != nil {
			return DirectJoinReport{}, err
		}
		sweep, err = newJoinDirectSweep(membership, &directIterator{cursor: directCursor.state, family: format.AddressFamilyIPv4}, ops4, table, check)
		if err != nil {
			return DirectJoinReport{}, err
		}
	} else {
		cursor, err := r.NewMembershipRangeCursor6()
		if err != nil {
			return DirectJoinReport{}, err
		}
		stream := &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv6}
		membership, err = newSelectedRanges(r, scope, stream, ops6, heap)
		if err != nil {
			return DirectJoinReport{}, err
		}
		if err := membership.enableCache(heap, heap.remainingBytes()); err != nil {
			return DirectJoinReport{}, err
		}
		directCursor, err := source.NewDirectCursor6(RangeForward)
		if err != nil {
			return DirectJoinReport{}, err
		}
		sweep, err = newJoinDirectSweep(membership, &directIterator{cursor: directCursor.state, family: format.AddressFamilyIPv6}, ops6, table, check)
		if err != nil {
			return DirectJoinReport{}, err
		}
	}

	stats, err := sweep.run(check)
	if err != nil {
		return DirectJoinReport{}, err
	}
	if membership.count() != r.meta.RangeRecordCount {
		return DirectJoinReport{}, corrupt("membership join range count disagrees")
	}
	if err := table.emit(scope, check, emit); err != nil {
		return DirectJoinReport{}, err
	}
	return DirectJoinReport{
		MembershipRangeCount: r.meta.RangeRecordCount,
		DirectRangesVisited:  stats.directRanges,
		JoinedSegmentCount:   stats.segments,
		SelectedAddresses:    stats.selected,
		MappedAddresses:      stats.mapped,
		UnmappedAddresses:    stats.unmapped,
		ResultCellCount:      uint64(len(table.cells)),
	}, nil
}

func newJoinDirectSweep(membership *selectedRanges, direct *directIterator, ops rangeOps, table *joinDirectTable, check checkpoint) (*joinDirectSweep, error) {
	work.InputSourcePass(2)
	sweep := &joinDirectSweep{membership: membership, direct: direct, ops: ops, table: table}
	if err := sweep.advanceLeft(check); err != nil {
		return nil, err
	}
	if err := sweep.advanceRight(check); err != nil {
		return nil, err
	}
	return sweep, nil
}

func (s *joinDirectSweep) run(check checkpoint) (joinDirectStats, error) {
	for s.left != nil {
		if err := s.step(check); err != nil {
			return joinDirectStats{}, err
		}
	}
	if check != nil {
		if err := check(); err != nil {
			return joinDirectStats{}, err
		}
	}
	return s.stats, nil
}

func (s *joinDirectSweep) step(check checkpoint) error {
	if s.left == nil {
		return corrupt("direct join lost its membership range")
	}
	current := *s.left
	for s.right != nil && s.right.to.Less(current.from) {
		if err := s.advanceRight(check); err != nil {
			return err
		}
	}
	if s.right == nil {
		if err := s.consume(current.from, current.to, nil, check); err != nil {
			return err
		}
		return s.advanceLeft(check)
	}
	provider := *s.right
	if current.to.Less(provider.from) {
		if err := s.consume(current.from, current.to, nil, check); err != nil {
			return err
		}
		return s.advanceLeft(check)
	}
	if current.from.Less(provider.from) {
		end, err := s.ops.previous(provider.from)
		if err != nil {
			return err
		}
		if err := s.consume(current.from, end, nil, check); err != nil {
			return err
		}
		current.from = provider.from
		s.left = &current
		return nil
	}

	from := current.from
	if provider.from.Less(from) {
		from = provider.from
	}
	to := current.to
	if provider.to.Less(to) {
		to = provider.to
	}
	directValue := uint64(provider.value) + 1
	if err := s.consume(from, to, &directValue, check); err != nil {
		return err
	}
	if current.to == to {
		if err := s.advanceLeft(check); err != nil {
			return err
		}
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		current.from = next
		s.left = &current
	}
	if provider.to == to {
		if err := s.advanceRight(check); err != nil {
			return err
		}
	} else {
		next, err := s.ops.next(to)
		if err != nil {
			return err
		}
		provider.from = next
		s.right = &provider
	}
	return nil
}

func (s *joinDirectSweep) advanceLeft(check checkpoint) error {
	left, err := s.membership.next(check)
	if err != nil {
		return err
	}
	s.left = left
	return nil
}

func (s *joinDirectSweep) advanceRight(check checkpoint) error {
	if s.stats.directRanges&4095 == 4095 && check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	next, ok, err := s.direct.next()
	if err != nil {
		return err
	}
	if ok {
		var err error
		s.stats.directRanges, err = increment64(s.stats.directRanges, "direct join range count")
		if err != nil {
			return err
		}
		frame := next
		s.right = &frame
	} else {
		s.right = nil
	}
	return nil
}

// consume folds one segment into the table and the stats (Rust
// Accumulator::consume). direct == nil means unmapped.
func (s *joinDirectSweep) consume(from, to addrKey, direct *uint64, check checkpoint) error {
	present := s.membership.present()
	if len(present) == 0 {
		return nil
	}
	count, err := s.ops.inclusive(from, to)
	if err != nil {
		return err
	}
	var err2 error
	s.stats.segments, err2 = increment64(s.stats.segments, "direct join segment count")
	if err2 != nil {
		return err2
	}
	s.stats.selected, err2 = addCard(s.stats.selected, count)
	if err2 != nil {
		return err2
	}
	if direct != nil {
		s.stats.mapped, err2 = addCard(s.stats.mapped, count)
		if err2 != nil {
			return err2
		}
	} else {
		s.stats.unmapped, err2 = addCard(s.stats.unmapped, count)
		if err2 != nil {
			return err2
		}
	}
	encoded := uint64(0)
	if direct != nil {
		encoded = *direct
	}
	for step, feed := range present {
		if step&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if err := s.table.add(feed, encoded, count, check); err != nil {
			return err
		}
		work.AggregationContribution(1)
	}
	work.JoinAdvance(1)
	return nil
}

// emit sorts the cells and delivers bounded batches (Rust Table::emit).
func (t *joinDirectTable) emit(scope *ScopeData, check checkpoint, emit func([]DirectJoinCell) error) error {
	if len(t.cells) == 0 {
		return nil
	}
	sort.Slice(t.cells, func(i, j int) bool {
		if t.cells[i].feed != t.cells[j].feed {
			return t.cells[i].feed < t.cells[j].feed
		}
		return t.cells[i].direct < t.cells[j].direct
	})
	batch := make([]DirectJoinCell, 0, joinResultBatch)
	for _, cell := range t.cells {
		var value *uint32
		if cell.direct != 0 {
			v := uint32(cell.direct - 1)
			value = &v
		}
		batch = append(batch, DirectJoinCell{
			Feed:        scope.entries[cell.feed].Name,
			DirectValue: value,
			Addresses:   cell.addresses,
		})
		if len(batch) == joinResultBatch {
			if err := flushJoinCells(check, emit, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) != 0 {
		return flushJoinCells(check, emit, batch)
	}
	return nil
}

func flushJoinCells(check checkpoint, emit func([]DirectJoinCell) error, batch []DirectJoinCell) error {
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
