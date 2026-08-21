package reader

// One-scan exact feed cardinality and overlap aggregation (Rust
// membership_query/aggregation.rs parity). The scope's membership range
// tree is walked once; every range contributes its inclusive address
// count to the feeds selected by its bitmap, and the requested pair
// plan folds the same count into every selected pair. Emission is
// batched (32 records) through caller sinks; the modeled operation heap
// charges every Rust allocation with the exact size_of parity.

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const aggregationResultBatch = 32

// FeedCardinality is the exact address count of one selected feed. The
// name aliases the catalog mapping; only the root facade converts it to
// an owned string (Rust FeedName parity).
type FeedCardinality struct {
	Feed      []byte
	Addresses format.Cardinality129
}

// FeedOverlap is the exact address overlap of one unordered feed pair.
type FeedOverlap struct {
	Left, Right []byte
	Addresses   format.Cardinality129
}

// MembershipAggregationReport is the exact work and output count of one
// completed membership scan.
type MembershipAggregationReport struct {
	ScannedRangeCount uint64
	ScannedAddresses  format.Cardinality129
	FeedResultCount   uint64
	PairResultCount   uint64
}

// AggregationMode selects the pair work of one scoped membership scan
// (Rust MembershipAggregationMode).
type AggregationMode uint8

const (
	AggregationCardinalities AggregationMode = iota
	AggregationAllPairs
	AggregationTargetAgainstScope
	AggregationSelectedPairs
)

// FeedPair is one caller-selected feed pair (join and aggregation
// inputs; Rust FeedPair).
type FeedPair struct {
	Left, Right string
}

// pairCell is one folded overlap cell (Rust PairCell).
type pairCell struct {
	left, right, owner, other uint32
	addresses                 format.Cardinality129
}

// pairPlan is the mode-specific pair accumulator.
type pairPlan struct {
	kind    AggregationMode
	totals  []format.Cardinality129 // AllPairs
	cells   []pairCell              // Listed (target/selected)
	offsets []int
}

// aggregateScope runs the one-scan aggregation over one resolved scope.
func (r *ImmutableReader) AggregateScope(scope *ScopeData, family uint8, mode AggregationMode, target string, pairs []FeedPair, check checkpoint, emitFeeds func([]FeedCardinality) error, emitOverlaps func([]FeedOverlap) error) (MembershipAggregationReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return MembershipAggregationReport{}, err
		}
	}
	heap, err := scope.operationHeapReserved(0)
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	operationHeap := newOperationHeap(heap)
	totals, err := makeCardSlice(operationHeap, len(scope.entries), "membership aggregation heap")
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	plan, err := newPairPlan(r, scope, mode, target, pairs, operationHeap, check)
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	scr, err := newScratch(len(scope.entries), operationHeap)
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	if err := scr.cache.enable(operationHeap, operationHeap.remainingBytes()); err != nil {
		return MembershipAggregationReport{}, err
	}

	var stream *membershipIterator
	var ops rangeOps
	if family == format.AddressFamilyIPv4 {
		cursor, err := r.NewMembershipRangeCursor4()
		if err != nil {
			return MembershipAggregationReport{}, err
		}
		stream = &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv4}
		ops = ops4
	} else {
		cursor, err := r.NewMembershipRangeCursor6()
		if err != nil {
			return MembershipAggregationReport{}, err
		}
		stream = &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv6}
		ops = ops6
	}

	scannedRangeCount, scannedAddresses, err := aggregationScan(r, scope, stream, ops, totals, plan, scr, r.meta.RangeRecordCount, check)
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	if err := emitFeedsBatched(scope, totals, check, emitFeeds); err != nil {
		return MembershipAggregationReport{}, err
	}
	pairResultCount, err := plan.emit(scope, check, emitOverlaps)
	if err != nil {
		return MembershipAggregationReport{}, err
	}
	return MembershipAggregationReport{
		ScannedRangeCount: scannedRangeCount,
		ScannedAddresses:  scannedAddresses,
		FeedResultCount:   uint64(len(scope.entries)),
		PairResultCount:   pairResultCount,
	}, nil
}

// makeCardSlice allocates one cardinality vector under the modeled heap.
func makeCardSlice(heap *operationHeap, count int, label string) ([]format.Cardinality129, error) {
	if err := heap.filled(uint64(count), rustCardSize, label); err != nil {
		return nil, err
	}
	return make([]format.Cardinality129, count), nil
}

// newPairPlan builders mirror Rust PairPlan::new/all/target/selected/
// listed, including the exact failure order.
func newPairPlan(r *ImmutableReader, scope *ScopeData, mode AggregationMode, target string, pairs []FeedPair, heap *operationHeap, check checkpoint) (*pairPlan, error) {
	switch mode {
	case AggregationCardinalities:
		return &pairPlan{kind: mode}, nil
	case AggregationAllPairs:
		return planAll(len(scope.entries), heap, check)
	case AggregationTargetAgainstScope:
		position, err := scope.positionName(r, target)
		if err != nil {
			return nil, err
		}
		return planTarget(scope, position, heap, check)
	case AggregationSelectedPairs:
		return planSelected(r, scope, pairs, heap, check)
	default:
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "unknown membership aggregation mode"}
	}
}

func planAll(feeds int, heap *operationHeap, check checkpoint) (*pairPlan, error) {
	count, err := pairCount(feeds)
	if err != nil {
		return nil, err
	}
	totals, err := makeCardSlice(heap, count, "membership pair aggregation heap")
	if err != nil {
		return nil, err
	}
	offsets, err := makeIntSlice(heap, feeds, "membership pair aggregation heap")
	if err != nil {
		return nil, err
	}
	next := 0
	for left := 0; left < feeds; left++ {
		if left&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		offsets[left] = next
		next += feeds - left - 1
	}
	if next != count {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership pair index"}
	}
	return &pairPlan{kind: AggregationAllPairs, totals: totals, offsets: offsets}, nil
}

func planTarget(scope *ScopeData, target int, heap *operationHeap, check checkpoint) (*pairPlan, error) {
	capacity, err := sizedCapacity(len(scope.entries)-1, rustCellSize, heap, "membership pair aggregation heap")
	if err != nil {
		return nil, err
	}
	cells := make([]pairCell, 0, capacity)
	for other := 0; other < len(scope.entries); other++ {
		if other&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		if other == target {
			continue
		}
		left, right := orderedPair(target, other)
		cells = append(cells, pairCell{
			left:  uint32(left),
			right: uint32(right),
			owner: uint32(target),
			other: uint32(other),
		})
	}
	return planListed(cells, len(scope.entries), heap, check)
}

func planSelected(r *ImmutableReader, scope *ScopeData, pairs []FeedPair, heap *operationHeap, check checkpoint) (*pairPlan, error) {
	if len(pairs) == 0 {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "selected feed pairs are empty"}
	}
	capacity, err := sizedCapacity(len(pairs), rustCellSize, heap, "membership pair aggregation heap")
	if err != nil {
		return nil, err
	}
	cells := make([]pairCell, 0, capacity)
	for work, pair := range pairs {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		left, err := scope.positionName(r, pair.Left)
		if err != nil {
			return nil, err
		}
		right, err := scope.positionName(r, pair.Right)
		if err != nil {
			return nil, err
		}
		if left == right {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "a feed pair must contain two different feeds"}
		}
		orderedLeft, orderedRight := orderedPair(left, right)
		cells = append(cells, pairCell{
			left:  uint32(orderedLeft),
			right: uint32(orderedRight),
			owner: uint32(orderedLeft),
			other: uint32(orderedRight),
		})
	}
	return planListed(cells, len(scope.entries), heap, check)
}

// sizedCapacity charges a vector under the modeled heap and returns its
// capacity (Rust heap.vector<T>).
func sizedCapacity(count int, size uint64, heap *operationHeap, label string) (int, error) {
	if err := heap.filled(uint64(count), size, label); err != nil {
		return 0, err
	}
	return count, nil
}

func planListed(cells []pairCell, feeds int, heap *operationHeap, check checkpoint) (*pairPlan, error) {
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].owner != cells[j].owner {
			return cells[i].owner < cells[j].owner
		}
		if cells[i].left != cells[j].left {
			return cells[i].left < cells[j].left
		}
		return cells[i].right < cells[j].right
	})
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	for work := 0; work+1 < len(cells); work++ {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		if cells[work].left == cells[work+1].left && cells[work].right == cells[work+1].right {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "selected feed pairs are not unique"}
		}
	}
	offsets, err := makeIntSlice(heap, feeds+1, "membership pair aggregation heap")
	if err != nil {
		return nil, err
	}
	for work, cell := range cells {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		next := cell.owner + 1
		offsets[next] = offsets[next] + 1
	}
	for index := 1; index < len(offsets); index++ {
		if index&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		offsets[index] = offsets[index] + offsets[index-1]
	}
	return &pairPlan{kind: AggregationSelectedPairs, cells: cells, offsets: offsets}, nil
}

// makeIntSlice allocates one usize vector under the modeled heap.
func makeIntSlice(heap *operationHeap, count int, label string) ([]int, error) {
	if err := heap.filled(uint64(count), rustUsize, label); err != nil {
		return nil, err
	}
	return make([]int, count), nil
}

// orderedPair normalizes one unordered pair to ascending order.
func orderedPair(left, right int) (int, int) {
	if left <= right {
		return left, right
	}
	return right, left
}

// pairCount computes feeds*(feeds-1)/2 in 128-bit arithmetic (Rust
// pair_count).
func pairCount(feeds int) (int, error) {
	n := uint64(feeds)
	if n < 2 {
		return 0, nil
	}
	hi, lo := mul64bits(n, n-1)
	lo = (hi << 63) | (lo >> 1)
	hi >>= 1
	if hi != 0 || lo > uint64(^uint(0)>>1) {
		return 0, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership pair aggregation heap"}
	}
	return int(lo), nil
}

// mul64bits is the 64x64->128 product Go lacks natively (Rust pair_count
// uses u128); hi/lo hold the 128-bit result.
func mul64bits(a, b uint64) (hi, lo uint64) {
	const mask32 = uint64(0xFFFFFFFF)
	al, ah := a&mask32, a>>32
	bl, bh := b&mask32, b>>32
	p00 := al * bl
	p10 := ah * bl
	p01 := al * bh
	p11 := ah * bh
	mid := (p00 >> 32) + (p10 & mask32) + (p01 & mask32)
	hi = p11 + (p10 >> 32) + (p01 >> 32) + (mid >> 32)
	lo = (mid << 32) | (p00 & mask32)
	return hi, lo
}

// aggregationScan walks the physical membership ranges once, folding
// every range's inclusive count into the active membership group (Rust
// aggregation scan()).
func aggregationScan(r *ImmutableReader, scope *ScopeData, stream *membershipIterator, ops rangeOps, totals []format.Cardinality129, plan *pairPlan, scr *scratch, expectedRanges uint64, check checkpoint) (uint64, format.Cardinality129, error) {
	if len(scope.entries) == 0 {
		if expectedRanges != 0 {
			return 0, format.CardinalityZero(), corrupt("an empty catalog has membership ranges")
		}
		return 0, format.CardinalityZero(), nil
	}
	work.InputSourcePass(1)
	var scannedRangeCount uint64
	scannedAddresses := format.CardinalityZero()
	pendingMembership := uint32(0)
	havePending := false
	pendingAddresses := format.CardinalityZero()
	for {
		rangeRecord, ok, err := stream.next()
		if err != nil {
			return 0, format.CardinalityZero(), err
		}
		if !ok {
			break
		}
		if scannedRangeCount&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return 0, format.CardinalityZero(), err
			}
		}
		count, err := ops.inclusive(rangeRecord.from, rangeRecord.to)
		if err != nil {
			return 0, format.CardinalityZero(), err
		}
		if havePending && pendingMembership == rangeRecord.membershipID {
			pendingAddresses, err = addCard(pendingAddresses, count)
			if err != nil {
				return 0, format.CardinalityZero(), err
			}
		} else {
			if err := contribute(totals, plan, scr, pendingAddresses, check); err != nil {
				return 0, format.CardinalityZero(), err
			}
			if err := scr.load(r, rangeRecord.membershipID, scope, check); err != nil {
				return 0, format.CardinalityZero(), err
			}
			pendingMembership = rangeRecord.membershipID
			havePending = true
			pendingAddresses = count
		}
		scannedRangeCount, err = increment64(scannedRangeCount, "membership scan range count")
		if err != nil {
			return 0, format.CardinalityZero(), err
		}
		scannedAddresses, err = addCard(scannedAddresses, count)
		if err != nil {
			return 0, format.CardinalityZero(), err
		}
	}
	if err := contribute(totals, plan, scr, pendingAddresses, check); err != nil {
		return 0, format.CardinalityZero(), err
	}
	if check != nil {
		if err := check(); err != nil {
			return 0, format.CardinalityZero(), err
		}
	}
	if scannedRangeCount != expectedRanges {
		return 0, format.CardinalityZero(), corrupt("membership range count disagrees")
	}
	return scannedRangeCount, scannedAddresses, nil
}

// contribute folds one membership group into the feed totals and the
// pair plan (Rust aggregation contribute()).
func contribute(totals []format.Cardinality129, plan *pairPlan, scr *scratch, count format.Cardinality129, check checkpoint) error {
	if count.Compare(format.CardinalityZero()) == 0 {
		return nil
	}
	for step, position := range scr.presentList() {
		if step&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		var err error
		totals[position], err = addCard(totals[position], count)
		if err != nil {
			return err
		}
		work.AggregationContribution(1)
	}
	return plan.add(scr.presentList(), scr.flags, count, check)
}

// add folds one group into the requested pair cells (Rust
// PairPlan::add).
func (p *pairPlan) add(present []uint32, flags []byte, count format.Cardinality129, check checkpoint) error {
	if count.Compare(format.CardinalityZero()) == 0 {
		return nil
	}
	var steps int
	switch p.kind {
	case AggregationCardinalities:
		return nil
	case AggregationAllPairs:
		for leftOffset, left := range present {
			for _, right := range present[leftOffset+1:] {
				if steps&4095 == 4095 && check != nil {
					if err := check(); err != nil {
						return err
					}
				}
				index := int64(p.offsets[left]) + int64(right) - int64(left) - 1
				if index < 0 || index >= int64(len(p.totals)) {
					return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership pair index"}
				}
				var err error
				p.totals[int(index)], err = addCard(p.totals[int(index)], count)
				if err != nil {
					return err
				}
				work.AggregationContribution(1)
				steps++
			}
		}
		return nil
	default: // listed
		for _, owner := range present {
			start := p.offsets[owner]
			end := p.offsets[owner+1]
			for i := start; i < end; i++ {
				if steps&4095 == 4095 && check != nil {
					if err := check(); err != nil {
						return err
					}
				}
				if flags[p.cells[i].other] != 0 {
					var err error
					p.cells[i].addresses, err = addCard(p.cells[i].addresses, count)
					if err != nil {
						return err
					}
					work.AggregationContribution(1)
				}
				steps++
			}
		}
		return nil
	}
}

// emit delivers the feed cardinalities (Rust emit_feeds).
func emitFeedsBatched(scope *ScopeData, totals []format.Cardinality129, check checkpoint, emit func([]FeedCardinality) error) error {
	if len(scope.entries) == 0 {
		return nil
	}
	batch := make([]FeedCardinality, 0, aggregationResultBatch)
	for i, entry := range scope.entries {
		batch = append(batch, FeedCardinality{Feed: entry.Name, Addresses: totals[i]})
		if len(batch) == aggregationResultBatch {
			if err := flushAggregation(check, emit, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) != 0 {
		return flushAggregation(check, emit, batch)
	}
	return nil
}

func flushAggregation(check checkpoint, emit func([]FeedCardinality) error, batch []FeedCardinality) error {
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

// emit delivers the pair overlaps (Rust PairPlan::emit).
func (p *pairPlan) emit(scope *ScopeData, check checkpoint, emit func([]FeedOverlap) error) (uint64, error) {
	switch p.kind {
	case AggregationCardinalities:
		return 0, nil
	case AggregationAllPairs:
		return emitAllPairs(scope, p.totals, check, emit)
	default:
		return emitListedPairs(scope, p.cells, check, emit)
	}
}

func emitAllPairs(scope *ScopeData, totals []format.Cardinality129, check checkpoint, emit func([]FeedOverlap) error) (uint64, error) {
	count, err := pairCount(len(scope.entries))
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	batch := make([]FeedOverlap, 0, aggregationResultBatch)
	index := 0
	for left := 0; left < len(scope.entries); left++ {
		for right := left + 1; right < len(scope.entries); right++ {
			batch = append(batch, FeedOverlap{
				Left:      scope.entries[left].Name,
				Right:     scope.entries[right].Name,
				Addresses: totals[index],
			})
			index++
			if len(batch) == aggregationResultBatch {
				if err := flushOverlaps(check, emit, batch); err != nil {
					return 0, err
				}
				batch = batch[:0]
			}
		}
	}
	if len(batch) != 0 {
		if err := flushOverlaps(check, emit, batch); err != nil {
			return 0, err
		}
	}
	return uint64(count), nil
}

func emitListedPairs(scope *ScopeData, cells []pairCell, check checkpoint, emit func([]FeedOverlap) error) (uint64, error) {
	if len(cells) == 0 {
		return 0, nil
	}
	batch := make([]FeedOverlap, 0, aggregationResultBatch)
	for _, cell := range cells {
		batch = append(batch, FeedOverlap{
			Left:      scope.entries[cell.left].Name,
			Right:     scope.entries[cell.right].Name,
			Addresses: cell.addresses,
		})
		if len(batch) == aggregationResultBatch {
			if err := flushOverlaps(check, emit, batch); err != nil {
				return 0, err
			}
			batch = batch[:0]
		}
	}
	if len(batch) != 0 {
		if err := flushOverlaps(check, emit, batch); err != nil {
			return 0, err
		}
	}
	return uint64(len(cells)), nil
}

func flushOverlaps(check checkpoint, emit func([]FeedOverlap) error, batch []FeedOverlap) error {
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
