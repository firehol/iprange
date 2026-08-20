// Output-side algebra machinery (Rust membership_query/algebra/output.rs
// parity): one materialized set operation built through the writer's
// one-shot output builder. The reader core owns everything that is not a
// writer call - plan resolution, the global-to-output map, the per-
// segment membership sink with its sequence cache and position words -
// and reaches the writer through separate func-typed call parameters
// (the rangeOps adapter pattern), because internal/reader may not
// import internal/writer. The module root composes the full publish
// pipeline (budget, attempt, builder, metadata, publication).

package reader

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// algebraOperationKind selects the set semantics of one published output
// (Rust Plan).
type algebraOperationKind uint8

const (
	algebraOperationUnion algebraOperationKind = iota
	algebraOperationIntersection
	algebraOperationExclusion
)

// AlgebraSetOperation is one set operation over virtual global feeds
// (Rust AlgebraSetOperation).
type AlgebraSetOperation struct {
	kind     algebraOperationKind
	selected FeedSelection
	included FeedSelection
	excluded FeedSelection
}

// AlgebraSetUnion selects the union of one feed selection.
func AlgebraSetUnion(selection FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraOperationUnion, selected: selection}
}

// AlgebraSetIntersection selects the intersection of one feed selection.
func AlgebraSetIntersection(selection FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraOperationIntersection, selected: selection}
}

// AlgebraSetExclusion selects the feeds of included minus the feeds of
// excluded (Rust AlgebraSetOperation::Exclusion).
func AlgebraSetExclusion(included, excluded FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraOperationExclusion, included: included, excluded: excluded}
}

// AlgebraOutputMode is the catalog shape of one published output (Rust
// AlgebraOutputMode): PreserveFeeds keeps one output feed per selected
// global catalog position; Flat materializes one named feed.
type AlgebraOutputMode struct {
	preserve bool
	flatName string
}

// AlgebraOutputModePreserveFeeds preserves the selected global feeds as
// one output feed each (Rust PreserveFeeds).
func AlgebraOutputModePreserveFeeds() AlgebraOutputMode {
	return AlgebraOutputMode{preserve: true}
}

// AlgebraOutputModeFlat materializes one named output feed (Rust
// Flat(FeedName)); the name must satisfy the v4 feed-name rule.
func AlgebraOutputModeFlat(name string) (AlgebraOutputMode, error) {
	if !format.FeedNameValidString(name) {
		return AlgebraOutputMode{}, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
	}
	return AlgebraOutputMode{flatName: name}, nil
}

// algebraOutputPlan is the resolved set plan of one output (Rust Plan:
// Union/Intersection/Exclusion over resolved selections).
type algebraOutputPlan struct {
	kind      algebraOperationKind
	selection *algebraSelection
	included  *algebraSelection
	excluded  *algebraSelection
}

// resolveAlgebraOutputPlan resolves one operation into its selections
// (Rust Plan::resolve): the intersection must be non-empty.
func resolveAlgebraOutputPlan(a *MembershipAlgebra, operation AlgebraSetOperation, heap *operationHeap, check checkpoint) (*algebraOutputPlan, error) {
	switch operation.kind {
	case algebraOperationUnion:
		selection, err := resolveAlgebraSelection(a, operation.selected, heap, check)
		if err != nil {
			return nil, err
		}
		return &algebraOutputPlan{kind: algebraOperationUnion, selection: selection}, nil
	case algebraOperationIntersection:
		selection, err := resolveAlgebraSelection(a, operation.selected, heap, check)
		if err != nil {
			return nil, err
		}
		if selection.len() == 0 {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership algebra intersection is empty"}
		}
		return &algebraOutputPlan{kind: algebraOperationIntersection, selection: selection}, nil
	case algebraOperationExclusion:
		included, err := resolveAlgebraSelection(a, operation.included, heap, check)
		if err != nil {
			return nil, err
		}
		excluded, err := resolveAlgebraSelection(a, operation.excluded, heap, check)
		if err != nil {
			return nil, err
		}
		return &algebraOutputPlan{kind: algebraOperationExclusion, included: included, excluded: excluded}, nil
	default:
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership algebra operation is invalid"}
	}
}

// qualifies reports whether one maximal segment belongs to the set
// (Rust Plan::qualifies: union-any, intersection-all, exclusion
// included-and-not-excluded).
func (p *algebraOutputPlan) qualifies(present, counts []uint32, check checkpoint) (bool, error) {
	switch p.kind {
	case algebraOperationUnion:
		return p.selection.any(present, counts, check)
	case algebraOperationIntersection:
		return p.selection.allPresent(present, counts, check)
	default:
		in, err := p.included.any(present, counts, check)
		if err != nil || !in {
			return in, err
		}
		ex, err := p.excluded.any(present, counts, check)
		return !ex, err
	}
}

// catalogPositions collects every plan position under the output-heap
// label (Rust Plan::catalog_positions): the union/intersection or the
// exclusion's included side.
func (p *algebraOutputPlan) catalogPositions(heap *operationHeap, check checkpoint) ([]uint32, error) {
	selection := p.selection
	if p.kind == algebraOperationExclusion {
		selection = p.included
	}
	if err := heap.filled(uint64(selection.len()), rustU32Size, "membership algebra output heap"); err != nil {
		return nil, err
	}
	positions := make([]uint32, 0, selection.len())
	if err := selection.forEachPosition(check, func(position uint32) error {
		positions = append(positions, position)
		return nil
	}); err != nil {
		return nil, err
	}
	return positions, nil
}

// fillOutput maps the present selected globals to output positions
// (Rust Plan::fill_output): the intersection visits its sorted
// positions; union and exclusion visit the present-selected positions
// and report whether the visit was ascending so the caller sorts the
// near-sorted vector exactly like sort_unstable.
func (p *algebraOutputPlan) fillOutput(present, counts []uint32, globalToOutput []uint32, output *[]uint32, check checkpoint) (bool, error) {
	push := func(global uint32) error {
		mapped := globalToOutput[global]
		if mapped == ^uint32(0) {
			return corrupt("membership algebra output feed disappeared")
		}
		*output = append(*output, mapped)
		return nil
	}
	var sorted bool
	var err error
	switch p.kind {
	case algebraOperationIntersection:
		err = p.selection.forEachPosition(check, push)
		sorted = true
	case algebraOperationUnion:
		sorted, err = p.selection.forEachPresent(present, counts, check, push)
	default:
		sorted, err = p.included.forEachPresent(present, counts, check, push)
	}
	if err != nil {
		return false, err
	}
	if !sorted {
		sort.Slice(*output, func(i, j int) bool { return (*output)[i] < (*output)[j] })
	}
	return true, nil
}

// len reports the resolved selection length (Rust Selection::len).
func (s *algebraSelection) len() int {
	if s.all {
		return s.count
	}
	return len(s.positions)
}

// all reports whether every selected global feed is present (Rust
// Selection::all).
func (s *algebraSelection) allPresent(present, counts []uint32, check checkpoint) (bool, error) {
	if s.all {
		return s.count != 0 && len(present) == s.count, nil
	}
	if len(s.positions) > len(present) {
		return false, nil
	}
	for work, position := range s.positions {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return false, err
			}
		}
		if counts[position] == 0 {
			return false, nil
		}
	}
	return len(s.positions) != 0, nil
}

// forEachPosition visits every selected position in ascending order
// (Rust Selection::for_each_position).
func (s *algebraSelection) forEachPosition(check checkpoint, apply func(position uint32) error) error {
	if s.all {
		for position := 0; position < s.count; position++ {
			if position&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return err
				}
			}
			if err := apply(uint32(position)); err != nil {
				return err
			}
		}
		return nil
	}
	for work, position := range s.positions {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if err := apply(position); err != nil {
			return err
		}
	}
	return nil
}

// forEachPresent visits every present selected position and reports
// whether the visit order was ascending (Rust Selection::
// for_each_present: present scans lose sortedness after global-state
// swap-removes, position scans stay sorted).
func (s *algebraSelection) forEachPresent(present, counts []uint32, check checkpoint, apply func(position uint32) error) (bool, error) {
	if s.all {
		for work, position := range present {
			if work&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return false, err
				}
			}
			if err := apply(position); err != nil {
				return false, err
			}
		}
		return len(present) < 2, nil
	}
	if len(s.positions) <= len(present) {
		for work, position := range s.positions {
			if work&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return false, err
				}
			}
			if counts[position] != 0 {
				if err := apply(position); err != nil {
					return false, err
				}
			}
		}
		return true, nil
	}
	for work, position := range present {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return false, err
			}
		}
		if s.flags[position] != 0 {
			if err := apply(position); err != nil {
				return false, err
			}
		}
	}
	return len(present) < 2, nil
}

// AlgebraOutputPrepared is the resolved plan of one published output
// (Rust output.rs Prepared): the operation heap, the plan, the catalog
// positions, and the output feed count.
type AlgebraOutputPrepared struct {
	heap            *operationHeap
	plan            *algebraOutputPlan
	catalogGlobals  []uint32
	outputFeedCount int
}

// PrepareAlgebraOutput resolves one set operation into the plan the
// build reuses (Rust Prepared::new): the operation heap is created with
// the algebra's unreserved remainder, the selections resolve under the
// selection-heap label, the catalog positions collect under the
// output-heap label, and the feed count is gated by the Rust
// "membership algebra output feeds" budget class.
func (a *MembershipAlgebra) PrepareAlgebraOutput(operation AlgebraSetOperation, mode AlgebraOutputMode, check checkpoint) (*AlgebraOutputPrepared, error) {
	heap, err := a.operationHeapReserved(0)
	if err != nil {
		return nil, err
	}
	plan, err := resolveAlgebraOutputPlan(a, operation, heap, check)
	if err != nil {
		return nil, err
	}
	catalogGlobals, err := plan.catalogPositions(heap, check)
	if err != nil {
		return nil, err
	}
	feedCount := algebraOutputFeedCount(mode, len(catalogGlobals))
	if uint64(feedCount) > uint64(^uint32(0)) {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra output feeds"}
	}
	return &AlgebraOutputPrepared{
		heap:            heap,
		plan:            plan,
		catalogGlobals:  catalogGlobals,
		outputFeedCount: feedCount,
	}, nil
}

// HeapRemaining reports the uncharged operation heap of one prepared
// output (Rust Prepared.heap.remaining(), the metadata write budget).
func (p *AlgebraOutputPrepared) HeapRemaining() uint64 {
	return p.heap.remainingBytes()
}

// OutputFeedCount reports the resolved output feed count (Rust
// Prepared.output_feed_count; the output catalog feed-index limit).
func (p *AlgebraOutputPrepared) OutputFeedCount() int {
	return p.outputFeedCount
}

// referenceBatchSlotSize and referenceBatchEntryLimit are the Rust
// immutable reference-batch shape constants
// (immutable_output/reference_batch.rs: Slot{id: u32, count: i64} is 16
// bytes; ENTRY_LIMIT is 1024).
const (
	referenceBatchSlotSize   = 16
	referenceBatchEntryLimit = 1024
)

// ChargeReferenceBatch sizes and charges the immutable output reference
// batch against the operation heap exactly like Rust ReferenceBatch::new
// (immutable_output/reference_batch.rs): the entry capacity is the floor
// power of two of the affordable slot pairs (two 16-byte slots per
// entry), capped at 1024; a heap that cannot fit one entry disables the
// batch with no charge. The returned entry count is the batch capacity
// the output builder must construct, so the admission and metadata
// budgets match the authority.
func (p *AlgebraOutputPrepared) ChargeReferenceBatch() (int, error) {
	// The cap is applied in uint64 before the platform-int conversion so
	// a 32-bit int cannot overflow on a very large heap (Rust sizes the
	// entry count with usize: afford / 32, capped at ENTRY_LIMIT).
	affordable := p.heap.remainingBytes() / (2 * referenceBatchSlotSize)
	if affordable > referenceBatchEntryLimit {
		affordable = referenceBatchEntryLimit
	}
	entries := floorPowerOfTwo(int(affordable))
	if entries == 0 {
		return 0, nil
	}
	if err := p.heap.filled(uint64(entries*2), referenceBatchSlotSize, "immutable reference batch"); err != nil {
		return 0, err
	}
	return entries, nil
}

// algebraOutputFeedCount is the output catalog size of one mode (Rust
// output_feed_count): preserved selections keep one feed per global
// position, Flat materializes one feed.
func algebraOutputFeedCount(mode AlgebraOutputMode, preserved int) int {
	if mode.preserve {
		return preserved
	}
	return 1
}

// AlgebraOutputReport is the exact work and content facts of one
// materialized output (Rust AlgebraSetReport without the publication
// facts; the root composes the report and the publication result).
type AlgebraOutputReport struct {
	SourceCount        uint64
	SourceRangeCount   uint64
	JoinedSegmentCount uint64
	OutputFeedCount    uint64
	OutputRangeCount   uint64
	OutputAddresses    format.Cardinality129
}

// BuildAlgebraOutput materializes one prepared set operation through
// the supplied writer hooks (Rust output.rs build_mapped + build_family):
// the feed catalog first (PreserveFeeds one feed per global position,
// Flat one named feed), then the global-to-output map, then one family
// sweep into the output sink, then the terminal flush. All vectors are
// charged against the prepared operation heap under the Rust
// "membership algebra output heap" label.
func (a *MembershipAlgebra) BuildAlgebraOutput(prepared *AlgebraOutputPrepared, mode AlgebraOutputMode, feed func(name string, index uint32) error, intern func(words []uint64) (uint32, error), pushV4 func(from, to, value uint32) error, pushV6 func(fromHi, fromLo, toHi, toLo uint64, value uint32) error, check checkpoint) (AlgebraOutputReport, error) {
	report := AlgebraOutputReport{
		SourceCount:     uint64(a.state.inputCount()),
		OutputFeedCount: uint64(prepared.outputFeedCount),
	}
	// The catalog names are decoded feed names (bounded below a
	// complete page by the v4 name grammar); the State accessor keeps
	// the bounded provenance visible to the ownership gate the same
	// way the public Feeds boundary does.
	entries := a.State().Names()
	if mode.preserve {
		for index, global := range prepared.catalogGlobals {
			if index&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return report, err
				}
			}
			entry := entries[global]
			if err := feed(string(entry.Name), uint32(index)); err != nil {
				return report, err
			}
		}
	} else {
		if err := feed(mode.flatName, 0); err != nil {
			return report, err
		}
	}
	if err := prepared.heap.filled(uint64(len(a.state.names)), rustU32Size, "membership algebra output heap"); err != nil {
		return report, err
	}
	globalToOutput := make([]uint32, len(a.state.names))
	for index := range globalToOutput {
		globalToOutput[index] = ^uint32(0)
	}
	if mode.preserve {
		for output, global := range prepared.catalogGlobals {
			if output&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return report, err
				}
			}
			globalToOutput[global] = uint32(output)
		}
	}
	capacity := prepared.outputFeedCount
	// The three sweep vectors mirror the three Rust heap.vector charges
	// (output.rs current / pending_positions / interned_positions): each
	// charges capacity * u32 under the output-heap label, so the
	// admission and metadata budgets match the authority exactly.
	for range 3 {
		if err := prepared.heap.filled(uint64(capacity), rustU32Size, "membership algebra output heap"); err != nil {
			return report, err
		}
	}
	output := &algebraOutputSink{
		feed:              feed,
		intern:            intern,
		pushV4:            pushV4,
		pushV6:            pushV6,
		family:            a.state.family,
		mode:              mode,
		plan:              prepared.plan,
		globalToOutput:    globalToOutput,
		current:           make([]uint32, 0, capacity),
		pendingPositions:  make([]uint32, 0, capacity),
		internedPositions: make([]uint32, 0, capacity),
	}
	sink := &algebraSink{output: output}
	var scanned scanReport
	var err error
	if a.state.family == format.AddressFamilyIPv4 {
		scanned, err = algebraScan(a, ops4, prepared.heap, sink, check)
	} else {
		scanned, err = algebraScan(a, ops6, prepared.heap, sink, check)
	}
	if err != nil {
		return report, err
	}
	if err := output.finish(check); err != nil {
		return report, err
	}
	report.SourceRangeCount = scanned.sourceRanges
	report.JoinedSegmentCount = scanned.segments
	report.OutputRangeCount = output.outputRanges
	report.OutputAddresses = output.outputAddresses
	return report, nil
}

// algebraOutputSink materializes one maximal segment of the algebra
// sweep into membership ranges over the writer hooks (Rust output.rs
// OutputSink): plan qualification, output-position collection, adjacent
// range coalescing, the sequence-keyed intern cache, and the per-4096
// cancellation cadence.
type algebraOutputSink struct {
	feed               func(name string, index uint32) error
	intern             func(words []uint64) (uint32, error)
	pushV4             func(from, to, value uint32) error
	pushV6             func(fromHi, fromLo, toHi, toLo uint64, value uint32) error
	family             uint8
	mode               AlgebraOutputMode
	plan               *algebraOutputPlan
	globalToOutput     []uint32
	current            []uint32
	pendingPositions   []uint32
	internedPositions  []uint32
	internedMembership uint32
	pendingFrom        addrKey
	pendingTo          addrKey
	hasPending         bool
	outputRanges       uint64
	outputAddresses    format.Cardinality129
	cancellationWork   uint16
	cache              sequenceCache
	words              []uint64
}

// enableCache sizes the sink's sequence cache (Rust
// SegmentSink::enable_cache over cache.rs SequenceCache::enable).
func (s *algebraOutputSink) enableCache(heap *operationHeap, maxBytes uint64) error {
	return s.cache.enable(heap, maxBytes)
}

// segment folds one maximal segment into the pending output membership
// (Rust OutputSink::segment).
func (s *algebraOutputSink) segment(from, to addrKey, present, counts []uint32, ops rangeOps, check checkpoint) error {
	s.cancellationWork++
	if s.cancellationWork == 4096 {
		s.cancellationWork = 0
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
	}
	qualifies, err := s.plan.qualifies(present, counts, check)
	if err != nil {
		return err
	}
	if !qualifies {
		return s.flush(check)
	}
	s.current = s.current[:0]
	if s.mode.preserve {
		if _, err := s.plan.fillOutput(present, counts, s.globalToOutput, &s.current, check); err != nil {
			return err
		}
	} else {
		s.current = append(s.current, 0)
	}
	if len(s.current) == 0 {
		return corrupt("membership algebra selected empty output membership")
	}
	addresses, err := ops.inclusive(from, to)
	if err != nil {
		return err
	}
	sum, err := s.outputAddresses.Add(addresses)
	if err != nil {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership algebra output addresses"}
	}
	s.outputAddresses = sum
	if s.hasPending {
		next, nextErr := ops.next(s.pendingTo)
		if nextErr == nil && next == from && equalU32(s.current, s.pendingPositions) {
			s.pendingTo = to
			return nil
		}
	}
	if err := s.flush(check); err != nil {
		return err
	}
	s.pendingPositions = append(s.pendingPositions[:0], s.current...)
	s.pendingFrom, s.pendingTo, s.hasPending = from, to, true
	return nil
}

// finish flushes the terminal pending range (Rust OutputSink::finish).
func (s *algebraOutputSink) finish(check checkpoint) error {
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	return s.flush(check)
}

// flush interns one pending membership and pushes its range (Rust
// OutputSink::flush): the exact-same-sequence fast path, the
// sequence-keyed cache with the membership_intern_cache_hit counter,
// the intern bridge, and the interned-sequence memo.
func (s *algebraOutputSink) flush(check checkpoint) error {
	if !s.hasPending {
		return nil
	}
	// Rust Option::take(): the pending range is consumed here, so a
	// later non-qualifying segment cannot flush an empty pending.
	s.hasPending = false
	membership := s.internedMembership
	if equalU32(s.pendingPositions, s.internedPositions) {
		if membership == 0 {
			return corrupt("algebra output membership cache is empty")
		}
	} else if cached, ok, err := s.cache.sequenceValue(s.pendingPositions, check); err != nil {
		return err
	} else if ok {
		work.MembershipInternCacheHit(1)
		membership = cached
	} else {
		words, err := s.positionWords()
		if err != nil {
			return err
		}
		membership, err = s.intern(words)
		if err != nil {
			return err
		}
		if err := s.cache.insertSequence(s.pendingPositions, membership, check); err != nil {
			return err
		}
	}
	if !equalU32(s.pendingPositions, s.internedPositions) {
		s.internedPositions = append(s.internedPositions[:0], s.pendingPositions...)
		s.internedMembership = membership
	}
	if s.family == format.AddressFamilyIPv4 {
		if err := s.pushV4(uint32(s.pendingFrom.lo), uint32(s.pendingTo.lo), membership); err != nil {
			return err
		}
	} else {
		if err := s.pushV6(s.pendingFrom.hi, s.pendingFrom.lo, s.pendingTo.hi, s.pendingTo.lo, membership); err != nil {
			return err
		}
	}
	ranges, err := increment64(s.outputRanges, "membership algebra output range count")
	if err != nil {
		return err
	}
	s.outputRanges = ranges
	s.pendingPositions = s.pendingPositions[:0]
	return nil
}

// positionWords materializes the pending positions as membership words
// (Rust PositionWords): word_count is the last position's word plus
// one and every selected position sets one bit. The scratch is sink-
// owned and reused; growth is bounded by the feed-index limit and
// amortized across flushes.
func (s *algebraOutputSink) positionWords() ([]uint64, error) {
	last := s.pendingPositions[len(s.pendingPositions)-1]
	wordCount := int(last/64) + 1
	if wordCount > len(s.words) {
		if wordCount > cap(s.words) {
			s.words = make([]uint64, wordCount)
		}
		s.words = s.words[:wordCount]
	}
	for index := range wordCount {
		s.words[index] = 0
	}
	for _, position := range s.pendingPositions {
		s.words[position>>6] |= uint64(1) << (position & 63)
	}
	return s.words[:wordCount], nil
}

// equalU32 reports exact slice equality (Rust slice == slice).
func equalU32(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
