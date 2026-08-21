package reader

// Reusable global-name algebra over pinned membership scopes (Rust
// membership_query/algebra.rs parity). MembershipAlgebra resolves
// same-named feeds across every source scope into one sorted global
// catalog, then Count/Compare run one ordered N-way event sweep per
// selection (algebra/scan.rs) with the exact Rust corruption classes,
// budget labels, and 4096-unit cancellation cadence. The public facade
// converts names at the boundary; this core stays zero-alloc per
// result.

import (
	"bytes"
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// MembershipAlgebraBudget bounds one pinned algebra (Rust
// MembershipAlgebraBudget: max_heap_bytes, max_sources).
type MembershipAlgebraBudget struct {
	MaxHeapBytes uint64
	MaxSources   uint32
}

// Rust struct sizes used by the modeled-heap charges (size_of parity;
// computed from the Rust field layouts cited per constant).
const (
	rustFeedNameSize         = 256  // feed.rs FeedName { bytes: [u8;255], len: u8 }
	rustInputStateSize       = 32   // algebra.rs InputState { expected_ranges: u64, local_to_global: Vec<u32> }
	rustGenerationReaderSize = 216  // generation.rs GenerationReader { meta: MetaV4(200), mapping: &Mapping, owner_identity: Option<ProcessIdentity> }
	rustAlgebraSourceSize    = 224  // algebra.rs Source { reader: GenerationReader, scope: &ScopeData }
	rustAlgebraSourceState4  = 1664 // scan.rs SourceState<Ipv4Key> { input: AlgebraInput(256), ranges: SelectedRanges(1384), range: Option<Range> }
	rustAlgebraSourceState6  = 1720 // scan.rs SourceState<Ipv6Key>
	rustAlgebraEvent4        = 16   // scan.rs Event<Ipv4Key> { at, source: u32, kind }
	rustAlgebraEvent6        = 32   // scan.rs Event<Ipv6Key>
)

// feedSelectionKind is the internal FeedSelection discriminant.
type feedSelectionKind uint8

const (
	selectionAll feedSelectionKind = iota
	selectionNamed
)

// FeedSelection is one caller feed selection over the global catalog
// (Rust FeedSelection: All or Named names; names are &str strings, so
// the Go port carries []string).
type FeedSelection struct {
	kind  feedSelectionKind
	names []string
}

// AlgebraCountReport is the exact union cardinality of one selection.
type AlgebraCountReport struct {
	SourceCount        uint64
	SourceRangeCount   uint64
	JoinedSegmentCount uint64
	Addresses          format.Cardinality129
}

// AlgebraComparisonReport is the exact comparison of two selections.
type AlgebraComparisonReport struct {
	SourceCount        uint64
	SourceRangeCount   uint64
	JoinedSegmentCount uint64
	LeftAddresses      format.Cardinality129
	RightAddresses     format.Cardinality129
	OverlapAddresses   format.Cardinality129
	LeftOnlyAddresses  format.Cardinality129
	RightOnlyAddresses format.Cardinality129
	UnionAddresses     format.Cardinality129
	Equal              bool
}

// algebraInput is one source's resolved input (Rust AlgebraInput).
type algebraInput struct {
	reader         *ImmutableReader
	scope          *ScopeData
	expectedRanges uint64
	localToGlobal  []uint32
}

// algebraState is the pinned global catalog plus its construction
// charge (Rust MembershipAlgebraState).
type algebraState struct {
	family   uint8
	inputs   []algebraInput
	names    []FeedEntry
	maxHeap  uint64
	heapUsed uint64
}

func (s *algebraState) inputCount() int { return len(s.inputs) }

// operationHeapReserved returns the remaining operation heap of one
// algebra, minus an externally reserved byte count (Rust
// AlgebraAccess::operation_heap_reserved; BudgetExceeded with the
// algebra label on underflow).
func (s *algebraState) operationHeapReserved(reserved uint64) (*operationHeap, error) {
	remaining, ok := subChecked(s.maxHeap, s.heapUsed)
	if !ok {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra heap"}
	}
	remaining, ok = subChecked(remaining, reserved)
	if !ok {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra heap"}
	}
	return newOperationHeap(remaining), nil
}

// AlgebraSource is one pinned source scope (Rust Source).
type AlgebraSource struct {
	Reader *ImmutableReader
	Scope  *ScopeData
}

// MembershipAlgebra is the pinned, reusable virtual catalog over one or
// more membership databases (Rust MembershipAlgebra).
type MembershipAlgebra struct {
	state   algebraState
	sources []AlgebraSource
}

// NewMembershipAlgebra resolves same-named feeds across every supplied
// scope into one global identity (Rust MembershipAlgebra::new): the
// source-count rules and the modeled source-heap admission charge run
// first, then the state construction (family agreement, global catalog
// with sort/dedup, per-source local-to-global maps) charges the same
// bytes as Rust.
func NewMembershipAlgebra(sources []AlgebraSource, budget MembershipAlgebraBudget, check checkpoint) (*MembershipAlgebra, error) {
	if err := requireAlgebraSources(len(sources), budget); err != nil {
		return nil, err
	}
	minimum := uint64(len(sources)) * rustAlgebraSourceSize
	admission := newOperationHeap(budget.MaxHeapBytes)
	if err := admission.charge(minimum, "membership algebra source heap"); err != nil {
		return nil, err
	}
	retained := minimum // Go slices reserve exactly their length
	state, err := newAlgebraState(sources, budget, retained, check)
	if err != nil {
		return nil, err
	}
	return &MembershipAlgebra{state: *state, sources: sources}, nil
}

func requireAlgebraSources(count int, budget MembershipAlgebraBudget) error {
	if count == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "membership algebra has no sources"}
	}
	if uint64(count) > uint64(budget.MaxSources) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra sources"}
	}
	return nil
}

// newAlgebraState charges and constructs the global catalog (Rust
// MembershipAlgebraState::new: inspect_sources, collect_names,
// build_inputs).
func newAlgebraState(sources []AlgebraSource, budget MembershipAlgebraBudget, retained uint64, check checkpoint) (*algebraState, error) {
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if err := requireAlgebraSources(len(sources), budget); err != nil {
		return nil, err
	}
	family, totalEntries, err := inspectAlgebraSources(sources, check)
	if err != nil {
		return nil, err
	}
	heap := newOperationHeap(budget.MaxHeapBytes)
	if err := heap.charge(retained, "membership algebra source heap"); err != nil {
		return nil, err
	}
	names, err := collectAlgebraNames(sources, totalEntries, heap, check)
	if err != nil {
		return nil, err
	}
	inputs, err := buildAlgebraInputs(sources, names, heap, check)
	if err != nil {
		return nil, err
	}
	return &algebraState{
		family:   family,
		inputs:   inputs,
		names:    names,
		maxHeap:  budget.MaxHeapBytes,
		heapUsed: budget.MaxHeapBytes - heap.remainingBytes(),
	}, nil
}

func inspectAlgebraSources(sources []AlgebraSource, check checkpoint) (uint8, uint64, error) {
	family := sources[0].Reader.Meta().AddressFamily
	var total uint64
	for index := range sources {
		if index&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return 0, 0, err
			}
		}
		if sources[index].Reader.Meta().AddressFamily != family {
			return 0, 0, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "membership algebra source families differ"}
		}
		var ok bool
		total, ok = addCheckedU64(total, uint64(len(sources[index].Scope.entries)))
		if !ok {
			return 0, 0, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra catalog heap"}
		}
	}
	return family, total, nil
}

func addCheckedU64(a, b uint64) (uint64, bool) {
	c := a + b
	return c, c >= a
}

// collectAlgebraNames gathers, sorts, and dedups every source feed name
// into the global catalog (Rust collect_names; the catalog heap charge
// is total entries times the Rust FeedName size). Each source is
// gathered through a direct ScopeData parameter (the aggregation emit
// shape), so the mapped name views stay bounded by the v4 name grammar
// (never complete pages).
func collectAlgebraNames(sources []AlgebraSource, totalEntries uint64, heap *operationHeap, check checkpoint) ([]FeedEntry, error) {
	if err := heap.filled(totalEntries, rustFeedNameSize, "membership algebra catalog heap"); err != nil {
		return nil, err
	}
	names := make([]FeedEntry, 0, totalEntries)
	var entryWork int
	for index := range sources {
		if index&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		for _, entry := range sources[index].Scope.entries {
			names = append(names, FeedEntry{Name: entry.Name})
			entryWork++
			if entryWork >= 4096 {
				entryWork -= 4096
				if check != nil {
					if err := check(); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return dedupAlgebraNames(names), nil
}

// dedupAlgebraNames sorts and dedups the gathered names into the global
// catalog (Rust collect_names tail). The container parameter carries
// struct elements so the element reads stay bounded; the position field
// is the entry index (Rust names are FeedName values; the position is
// the binary-search slot).
func dedupAlgebraNames(names []FeedEntry) []FeedEntry {
	sort.Slice(names, func(i, j int) bool { return bytes.Compare(names[i].Name, names[j].Name) < 0 })
	deduped := make([]FeedEntry, 0, len(names))
	for i := range names {
		if i == 0 || !bytes.Equal(deduped[len(deduped)-1].Name, names[i].Name) {
			deduped = append(deduped, names[i])
		}
	}
	for position := range deduped {
		deduped[position].FeedIndex = uint32(position)
	}
	return deduped
}

// buildAlgebraInputs resolves every source's local feed positions to
// global catalog positions (Rust build_inputs; a missing name is a
// state corruption class, impossible after dedup).
func buildAlgebraInputs(sources []AlgebraSource, names []FeedEntry, heap *operationHeap, check checkpoint) ([]algebraInput, error) {
	if err := heap.filled(uint64(len(sources)), rustInputStateSize, "membership algebra source heap"); err != nil {
		return nil, err
	}
	inputs := make([]algebraInput, 0, len(sources))
	for index := range sources {
		if index&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		source := sources[index]
		local, err := algebraSourcePositions(source.Scope, names, heap, check)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, algebraInput{
			reader:         source.Reader,
			scope:          source.Scope,
			expectedRanges: source.Reader.Meta().RangeRecordCount,
			localToGlobal:  local,
		})
	}
	return inputs, nil
}

// algebraSourcePositions resolves one source's local entries to global
// catalog positions (Rust build_inputs per source; the direct ScopeData
// parameter keeps the name views bounded).
func algebraSourcePositions(scope *ScopeData, names []FeedEntry, heap *operationHeap, check checkpoint) ([]uint32, error) {
	if err := heap.filled(uint64(len(scope.entries)), rustU32Size, "membership algebra source heap"); err != nil {
		return nil, err
	}
	local := make([]uint32, 0, len(scope.entries))
	for work, entry := range scope.entries {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		position := algebraNamePosition(names, entry.Name)
		if position < 0 {
			return nil, corrupt("global feed name disappeared")
		}
		if uint64(position) > uint64(^uint32(0)) {
			return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra feeds"}
		}
		local = append(local, uint32(position))
	}
	return local, nil
}

// algebraNamePosition binary-searches the sorted global catalog (Rust
// names().binary_search); -1 when absent. The catalog entries are
// struct carriers (scope entries shape), so the probe comparisons read
// the mapped name field without ever exposing a raw byte-slice element.
func algebraNamePosition(names []FeedEntry, name []byte) int {
	index := sort.Search(len(names), func(i int) bool { return bytes.Compare(names[i].Name, name) >= 0 })
	if index < len(names) && bytes.Equal(names[index].Name, name) {
		return index
	}
	return -1
}

// AddressFamily returns the family shared by every source.
func (a *MembershipAlgebra) AddressFamily() uint8 { return a.state.family }

// FeedCount returns the unique global feed name count.
func (a *MembershipAlgebra) FeedCount() int { return len(a.state.names) }

// Count computes the exact address union of one selection in one
// ordered pass (Rust MembershipAlgebra::count).
func (a *MembershipAlgebra) Count(feeds FeedSelection, check checkpoint) (AlgebraCountReport, error) {
	return algebraCount(a, feeds, 0, check)
}

// Compare computes the exact comparison of two selections in one
// ordered pass (Rust MembershipAlgebra::compare).
func (a *MembershipAlgebra) Compare(left, right FeedSelection, check checkpoint) (AlgebraComparisonReport, error) {
	return algebraCompare(a, left, right, 0, check)
}

// State returns the pinned global state handle (Rust
// AlgebraAccess::state). The handle mirrors the scope-data pointer
// pattern used by the public facade for the catalog entries access.
func (a *MembershipAlgebra) State() *algebraState { return &a.state }

// Names returns the sorted global catalog as struct carriers (mapped
// name views with the global catalog position; the facade converts the
// names to owned strings, exactly like ScopeData.Entries).
func (s *algebraState) Names() []FeedEntry { return s.names }

// source resolves one source input (Rust AlgebraAccess::source).
func (a *MembershipAlgebra) source(index int) (algebraInput, error) {
	if index < 0 || index >= len(a.sources) {
		return algebraInput{}, corrupt("membership algebra source disappeared")
	}
	if index < 0 || index >= len(a.state.inputs) {
		return algebraInput{}, corrupt("membership algebra input disappeared")
	}
	return a.state.inputs[index], nil
}

// operationHeapReserved delegates to the state (Rust
// AlgebraAccess::operation_heap_reserved).
func (a *MembershipAlgebra) operationHeapReserved(reserved uint64) (*operationHeap, error) {
	return a.state.operationHeapReserved(reserved)
}

// algebraSelection is the bounded resolution of one caller selection to
// global positions (Rust algebra/selection.rs).
type algebraSelection struct {
	all       bool
	count     int
	positions []uint32
	flags     []byte
}

// resolveAlgebraSelection resolves names against the global catalog
// (Rust Selection::resolve): binary search per name, sorted unique
// positions, and the presence flags vector, all under the selection
// heap label with the 4096-unit checkpoint cadence.
func resolveAlgebraSelection(a *MembershipAlgebra, requested FeedSelection, heap *operationHeap, check checkpoint) (*algebraSelection, error) {
	if requested.kind == selectionAll {
		return &algebraSelection{all: true, count: len(a.state.names)}, nil
	}
	if len(requested.names) == 0 {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership algebra feed selection is empty"}
	}
	if err := heap.filled(uint64(len(requested.names)), rustU32Size, "membership algebra selection heap"); err != nil {
		return nil, err
	}
	positions := make([]uint32, 0, len(requested.names))
	for work, name := range requested.names {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		position := algebraNamePosition(a.state.names, []byte(name))
		if position < 0 {
			return nil, &format.Error{Code: format.CodeNameNotFound}
		}
		if uint64(position) > uint64(^uint32(0)) {
			return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra feeds"}
		}
		positions = append(positions, uint32(position))
	}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	for work := 1; work < len(positions); work++ {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		if positions[work-1] == positions[work] {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership algebra feed selection is not unique"}
		}
	}
	if err := heap.filled(uint64(len(a.state.names)), 1, "membership algebra selection heap"); err != nil {
		return nil, err
	}
	flags := make([]byte, len(a.state.names))
	for work, position := range positions {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		flags[position] = 1
	}
	return &algebraSelection{positions: positions, flags: flags}, nil
}

// any reports whether any selected global feed is present (Rust
// Selection::any: the smaller side is iterated).
func (s *algebraSelection) any(present, counts []uint32, check checkpoint) (bool, error) {
	if s.all {
		return len(present) != 0, nil
	}
	if len(s.positions) < len(present) {
		for work, position := range s.positions {
			if work&4095 == 4095 && check != nil {
				if err := check(); err != nil {
					return false, err
				}
			}
			if counts[position] != 0 {
				return true, nil
			}
		}
		return false, nil
	}
	for work, slot := range present {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return false, err
			}
		}
		if s.flags[slot] != 0 {
			return true, nil
		}
	}
	return false, nil
}

// algebraSink consumes one maximal segment of the global sweep (Rust
// SegmentSink, implemented by CountSink and ComparisonSink). Go has no
// traits, so one concrete sink carries both analyses: nil right selects
// the union count, a set right selects the four-case comparison.
type algebraSink struct {
	left, right *algebraSelection

	addresses          format.Cardinality129
	leftAddresses      format.Cardinality129
	rightAddresses     format.Cardinality129
	overlapAddresses   format.Cardinality129
	leftOnlyAddresses  format.Cardinality129
	rightOnlyAddresses format.Cardinality129
	unionAddresses     format.Cardinality129

	// output selects the materialized-output mode (Rust output.rs
	// OutputSink): nil in the count and comparison analyses. One
	// concrete sink carries every algebra consumer so the sweep stays a
	// single provably scanned concrete scanner; the output state is
	// reader-owned and the writer calls travel through the output
	// hooks.
	output *algebraOutputSink
}

// enableCache reserves the sink's operation-heap share (Rust
// SegmentSink::enable_cache; a no-op for both algebra sinks, the
// output sink sizes its sequence cache).
func (s *algebraSink) enableCache(heap *operationHeap, maxBytes uint64) error {
	if s.output != nil {
		return s.output.enableCache(heap, maxBytes)
	}
	return nil
}

// segment folds one maximal segment into the active analysis (Rust
// CountSink::segment and ComparisonSink::segment).
func (s *algebraSink) segment(from, to addrKey, present, counts []uint32, ops rangeOps, check checkpoint) error {
	if s.output != nil {
		return s.output.segment(from, to, present, counts, ops, check)
	}
	if s.right == nil {
		selected, err := s.left.any(present, counts, check)
		if err != nil {
			return err
		}
		if !selected {
			return nil
		}
		count, err := ops.inclusive(from, to)
		if err != nil {
			return err
		}
		s.addresses, err = addCardAlgebra(s.addresses, count)
		return err
	}
	inLeft, err := s.left.any(present, counts, check)
	if err != nil {
		return err
	}
	inRight, err := s.right.any(present, counts, check)
	if err != nil {
		return err
	}
	if !inLeft && !inRight {
		return nil
	}
	count, err := ops.inclusive(from, to)
	if err != nil {
		return err
	}
	switch {
	case inLeft && inRight:
		if s.leftAddresses, err = addCardAlgebra(s.leftAddresses, count); err != nil {
			return err
		}
		if s.rightAddresses, err = addCardAlgebra(s.rightAddresses, count); err != nil {
			return err
		}
		if s.overlapAddresses, err = addCardAlgebra(s.overlapAddresses, count); err != nil {
			return err
		}
		if s.unionAddresses, err = addCardAlgebra(s.unionAddresses, count); err != nil {
			return err
		}
	case inLeft:
		if s.leftAddresses, err = addCardAlgebra(s.leftAddresses, count); err != nil {
			return err
		}
		if s.leftOnlyAddresses, err = addCardAlgebra(s.leftOnlyAddresses, count); err != nil {
			return err
		}
		if s.unionAddresses, err = addCardAlgebra(s.unionAddresses, count); err != nil {
			return err
		}
	case inRight:
		if s.rightAddresses, err = addCardAlgebra(s.rightAddresses, count); err != nil {
			return err
		}
		if s.rightOnlyAddresses, err = addCardAlgebra(s.rightOnlyAddresses, count); err != nil {
			return err
		}
		if s.unionAddresses, err = addCardAlgebra(s.unionAddresses, count); err != nil {
			return err
		}
	}
	return nil
}

// addCardAlgebra folds one exact count with the Rust checked-add
// message (analysis.rs: ArithmeticOverflow "membership algebra
// addresses").
func addCardAlgebra(left, right format.Cardinality129) (format.Cardinality129, error) {
	sum, err := left.Add(right)
	if err != nil {
		return format.Cardinality129{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership algebra addresses"}
	}
	return sum, nil
}

// AlgebraFeedSelectionAll constructs the all-feeds selection (Rust
// FeedSelection::All). The catalog boundary resolves it to the full
// global name list without allocating positions.
func AlgebraFeedSelectionAll() FeedSelection {
	return FeedSelection{kind: selectionAll}
}

// AlgebraFeedSelectionNamed constructs a named selection over the given
// names (Rust FeedSelection::Named). The names are borrowed: the
// selection heap charge and the resolve/validation rules run at
// selection time (resolveAlgebraSelection), exactly like Rust, and the
// public facade validates caller strings before conversion.
func AlgebraFeedSelectionNamed(names []string) FeedSelection {
	return FeedSelection{kind: selectionNamed, names: names}
}

// algebraCount runs the one-pass union count (Rust analysis::count).
func algebraCount(a *MembershipAlgebra, feeds FeedSelection, reserved uint64, check checkpoint) (AlgebraCountReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return AlgebraCountReport{}, err
		}
	}
	heap, err := a.operationHeapReserved(reserved)
	if err != nil {
		return AlgebraCountReport{}, err
	}
	selection, err := resolveAlgebraSelection(a, feeds, heap, check)
	if err != nil {
		return AlgebraCountReport{}, err
	}
	report := AlgebraCountReport{SourceCount: uint64(a.state.inputCount())}
	var scanned scanReport
	if a.state.family == format.AddressFamilyIPv4 {
		sink := &algebraSink{left: selection}
		scanned, err = algebraScan(a, ops4, heap, sink, check)
		report.Addresses = sink.addresses
	} else {
		sink := &algebraSink{left: selection}
		scanned, err = algebraScan(a, ops6, heap, sink, check)
		report.Addresses = sink.addresses
	}
	if err != nil {
		return AlgebraCountReport{}, err
	}
	report.SourceRangeCount = scanned.sourceRanges
	report.JoinedSegmentCount = scanned.segments
	return report, nil
}

// algebraCompare runs the one-pass comparison (Rust analysis::compare).
func algebraCompare(a *MembershipAlgebra, left, right FeedSelection, reserved uint64, check checkpoint) (AlgebraComparisonReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return AlgebraComparisonReport{}, err
		}
	}
	heap, err := a.operationHeapReserved(reserved)
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	leftSelection, err := resolveAlgebraSelection(a, left, heap, check)
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	rightSelection, err := resolveAlgebraSelection(a, right, heap, check)
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	report := AlgebraComparisonReport{SourceCount: uint64(a.state.inputCount())}
	var scanned scanReport
	if a.state.family == format.AddressFamilyIPv4 {
		sink := &algebraSink{left: leftSelection, right: rightSelection}
		scanned, err = algebraScan(a, ops4, heap, sink, check)
		report.LeftAddresses, report.RightAddresses = sink.leftAddresses, sink.rightAddresses
		report.OverlapAddresses, report.LeftOnlyAddresses = sink.overlapAddresses, sink.leftOnlyAddresses
		report.RightOnlyAddresses, report.UnionAddresses = sink.rightOnlyAddresses, sink.unionAddresses
	} else {
		sink := &algebraSink{left: leftSelection, right: rightSelection}
		scanned, err = algebraScan(a, ops6, heap, sink, check)
		report.LeftAddresses, report.RightAddresses = sink.leftAddresses, sink.rightAddresses
		report.OverlapAddresses, report.LeftOnlyAddresses = sink.overlapAddresses, sink.leftOnlyAddresses
		report.RightOnlyAddresses, report.UnionAddresses = sink.rightOnlyAddresses, sink.unionAddresses
	}
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	report.SourceRangeCount = scanned.sourceRanges
	report.JoinedSegmentCount = scanned.segments
	report.Equal = report.LeftOnlyAddresses.Compare(format.CardinalityZero()) == 0 && report.RightOnlyAddresses.Compare(format.CardinalityZero()) == 0
	return report, nil
}

// scanReport carries the traversal facts of one algebra sweep (Rust
// ScanReport).
type scanReport struct {
	sourceRanges uint64
	segments     uint64
}

// algebraEventKind orders one boundary event (Rust EventKind; End is
// processed before Start at the same address).
type algebraEventKind uint8

const (
	algebraEventEnd algebraEventKind = iota
	algebraEventStart
)

// algebraEvent is one source boundary at one address (Rust Event).
type algebraEvent struct {
	at     addrKey
	source uint32
	kind   algebraEventKind
}

// algebraEventBefore is the Rust event order: address ascending, ties
// by (source, kind) with End before Start.
func algebraEventBefore(left, right algebraEvent) bool {
	if left.at.Less(right.at) {
		return true
	}
	if right.at.Less(left.at) {
		return false
	}
	if left.source != right.source {
		return left.source < right.source
	}
	return left.kind < right.kind
}

// algebraEvents is the bounded event queue (Rust Events): a linear scan
// for up to four sources, a binary min-heap beyond.
type algebraEvents struct {
	values []algebraEvent
	small  bool
}

func (e *algebraEvents) peek() (algebraEvent, bool) {
	if len(e.values) == 0 {
		return algebraEvent{}, false
	}
	if e.small {
		index := e.smallest()
		return e.values[index], true
	}
	return e.values[0], true
}

func (e *algebraEvents) push(event algebraEvent) {
	e.values = append(e.values, event)
	if e.small {
		return
	}
	child := len(e.values) - 1
	for child != 0 {
		parent := (child - 1) / 2
		if !algebraEventBefore(e.values[child], e.values[parent]) {
			break
		}
		e.values[child], e.values[parent] = e.values[parent], e.values[child]
		child = parent
	}
}

func (e *algebraEvents) pop() (algebraEvent, bool) {
	if len(e.values) == 0 {
		return algebraEvent{}, false
	}
	if e.small {
		index := e.smallest()
		event := e.values[index]
		e.values[index] = e.values[len(e.values)-1]
		e.values = e.values[:len(e.values)-1]
		return event, true
	}
	root := e.values[0]
	last := e.values[len(e.values)-1]
	e.values = e.values[:len(e.values)-1]
	if len(e.values) != 0 {
		e.values[0] = last
		parent := 0
		for {
			left := parent*2 + 1
			if left >= len(e.values) {
				break
			}
			right := left + 1
			child := left
			if right < len(e.values) && algebraEventBefore(e.values[right], e.values[left]) {
				child = right
			}
			if !algebraEventBefore(e.values[child], e.values[parent]) {
				break
			}
			e.values[parent], e.values[child] = e.values[child], e.values[parent]
			parent = child
		}
	}
	return root, true
}

func (e *algebraEvents) smallest() int {
	smallest := 0
	for index := 1; index < len(e.values); index++ {
		if algebraEventBefore(e.values[index], e.values[smallest]) {
			smallest = index
		}
	}
	return smallest
}

// algebraGlobalState tracks the present global feeds across sources
// (Rust scan.rs GlobalState: counts, 1-based presence slots, and the
// swap-removed present list).
type algebraGlobalState struct {
	counts  []uint32
	slots   []uint32
	present []uint32
}

func newAlgebraGlobalState(feeds int, heap *operationHeap) (*algebraGlobalState, error) {
	if err := heap.filled(uint64(feeds), rustU32Size, "membership algebra scan heap"); err != nil {
		return nil, err
	}
	if err := heap.filled(uint64(feeds), rustU32Size, "membership algebra scan heap"); err != nil {
		return nil, err
	}
	if err := heap.filled(uint64(feeds), rustU32Size, "membership algebra scan heap"); err != nil {
		return nil, err
	}
	return &algebraGlobalState{
		counts:  make([]uint32, feeds),
		slots:   make([]uint32, feeds),
		present: make([]uint32, 0, feeds),
	}, nil
}

// add folds one source's present local feeds into the global presence
// (Rust GlobalState::add).
func (g *algebraGlobalState) add(present, localToGlobal []uint32, check checkpoint) error {
	for work, local := range present {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		global := localToGlobal[local]
		count := g.counts[global]
		var ok bool
		g.counts[global], ok = addCheckedU32(count, 1)
		if !ok {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "global feed source count"}
		}
		if count == 0 {
			slot, ok := addCheckedU32(uint32(len(g.present)), 1)
			if !ok {
				return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra feeds"}
			}
			g.slots[global] = slot
			g.present = append(g.present, global)
		}
	}
	return nil
}

// remove folds one source's leaving local feeds out of the global
// presence (Rust GlobalState::remove, swap-remove with slot repair).
func (g *algebraGlobalState) remove(present, localToGlobal []uint32, check checkpoint) error {
	for work, local := range present {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		global := localToGlobal[local]
		count := g.counts[global]
		if count == 0 {
			return corrupt("global feed source count underflow")
		}
		count--
		g.counts[global] = count
		if count == 0 {
			slot := int(g.slots[global])
			if slot == 0 {
				return corrupt("global feed presence slot is absent")
			}
			slot--
			removed := g.present[slot]
			if removed != global {
				return corrupt("global feed presence slot disagrees")
			}
			g.present[slot] = g.present[len(g.present)-1]
			g.present = g.present[:len(g.present)-1]
			g.slots[global] = 0
			if slot < len(g.present) {
				g.slots[g.present[slot]] = uint32(slot) + 1
			}
		}
	}
	return nil
}

// algebraSourceState is one source's sweep state (Rust SourceState).
type algebraSourceState struct {
	input     algebraInput
	ranges    *selectedRanges
	rangeFrom addrKey
	rangeTo   addrKey
	hasRange  bool
	active    bool
}

// algebraScan runs the ordered N-way event sweep (Rust scan.rs run):
// one Start/End event per source range, boundary emission before every
// event, the same-at group application, the terminal segment, and the
// per-source range-count agreement.
func algebraScan(a *MembershipAlgebra, ops rangeOps, heap *operationHeap, sink *algebraSink, check checkpoint) (scanReport, error) {
	if check != nil {
		if err := check(); err != nil {
			return scanReport{}, err
		}
	}
	sourceCount := a.state.inputCount()
	work.InputSourcePass(uint64(sourceCount))
	states, events, err := initializeAlgebraSources(a, sourceCount, ops, heap, check)
	if err != nil {
		return scanReport{}, err
	}
	global, err := newAlgebraGlobalState(len(a.state.names), heap)
	if err != nil {
		return scanReport{}, err
	}
	cacheShare := heap.remainingBytes() / (uint64(len(states)) + 1)
	for _, state := range states {
		if err := state.ranges.enableCache(heap, cacheShare); err != nil {
			return scanReport{}, err
		}
	}
	if err := sink.enableCache(heap, cacheShare); err != nil {
		return scanReport{}, err
	}

	report := scanReport{}
	var position *addrKey
	eventWork := 0
	for {
		event, ok := events.pop()
		if !ok {
			break
		}
		eventWork++
		if eventWork == 4096 {
			eventWork = 0
			if check != nil {
				if err := check(); err != nil {
					return scanReport{}, err
				}
			}
		}
		if err := emitAlgebraBefore(position, event.at, ops, global, sink, &report, check); err != nil {
			return scanReport{}, err
		}
		at := event.at
		if err := applyAlgebraBoundary(event, states, events, global, &eventWork, ops, check); err != nil {
			return scanReport{}, err
		}
		position = &at
	}
	if err := emitAlgebraTerminal(position, states, ops, global, sink, &report, check); err != nil {
		return scanReport{}, err
	}
	if err := finishAlgebraSources(states, ops, &report, check); err != nil {
		return scanReport{}, err
	}
	return report, nil
}

// initializeAlgebraSources opens every source's selected-range stream
// and pushes its first Start event (Rust initialize_sources).
func initializeAlgebraSources(a *MembershipAlgebra, sourceCount int, ops rangeOps, heap *operationHeap, check checkpoint) ([]*algebraSourceState, *algebraEvents, error) {
	stateSize := uint64(rustAlgebraSourceState4)
	eventSize := uint64(rustAlgebraEvent4)
	if a.state.family == format.AddressFamilyIPv6 {
		stateSize = rustAlgebraSourceState6
		eventSize = rustAlgebraEvent6
	}
	if err := heap.filled(uint64(sourceCount), stateSize, "membership algebra scan heap"); err != nil {
		return nil, nil, err
	}
	if err := heap.filled(uint64(sourceCount), eventSize, "membership algebra event heap"); err != nil {
		return nil, nil, err
	}
	states := make([]*algebraSourceState, 0, sourceCount)
	events := &algebraEvents{values: make([]algebraEvent, 0, sourceCount), small: sourceCount <= 4}
	for index := 0; index < sourceCount; index++ {
		input, err := a.source(index)
		if err != nil {
			return nil, nil, err
		}
		ranges, err := newAlgebraSelectedRanges(input, ops, heap)
		if err != nil {
			return nil, nil, err
		}
		state := &algebraSourceState{input: input, ranges: ranges}
		if err := algebraLoadNext(state, check); err != nil {
			return nil, nil, err
		}
		if uint64(len(states)) > uint64(^uint32(0)) {
			return nil, nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra sources"}
		}
		source := uint32(len(states))
		if state.hasRange {
			events.push(algebraEvent{at: state.rangeFrom, source: source, kind: algebraEventStart})
		}
		states = append(states, state)
	}
	return states, events, nil
}

// newAlgebraSelectedRanges opens one source's membership range stream
// and selected-runs merger (Rust SelectedRanges::new over the source
// input).
func newAlgebraSelectedRanges(input algebraInput, ops rangeOps, heap *operationHeap) (*selectedRanges, error) {
	stream, err := newMembershipIterator(input.reader, input.reader.Meta().AddressFamily == format.AddressFamilyIPv4)
	if err != nil {
		return nil, err
	}
	return newSelectedRanges(input.reader, input.scope, stream, ops, heap)
}

// newMembershipIterator opens the family membership range cursor over
// one reader.
func newMembershipIterator(r *ImmutableReader, ipv4 bool) (*membershipIterator, error) {
	if ipv4 {
		cursor, err := r.NewMembershipRangeCursor4()
		if err != nil {
			return nil, err
		}
		return &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv4}, nil
	}
	cursor, err := r.NewMembershipRangeCursor6()
	if err != nil {
		return nil, err
	}
	return &membershipIterator{cursor: cursor.state, family: format.AddressFamilyIPv6}, nil
}

// algebraLoadNext loads the next selected run of one source (Rust
// load_next).
func algebraLoadNext(state *algebraSourceState, check checkpoint) error {
	next, err := state.ranges.next(check)
	if err != nil {
		return err
	}
	if next != nil {
		state.rangeFrom, state.rangeTo = next.from, next.to
		state.hasRange = true
	} else {
		state.hasRange = false
	}
	if state.ranges.count() > state.input.expectedRanges {
		return corrupt("membership algebra range count disagrees")
	}
	return nil
}

// emitAlgebraBefore delivers the maximal segment before one boundary
// (Rust emit_before).
func emitAlgebraBefore(from *addrKey, boundary addrKey, ops rangeOps, global *algebraGlobalState, sink *algebraSink, report *scanReport, check checkpoint) error {
	if from == nil || !from.Less(boundary) || len(global.present) == 0 {
		return nil
	}
	to, err := ops.previous(boundary)
	if err != nil {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership algebra boundary"}
	}
	if err := sink.segment(*from, to, global.present, global.counts, ops, check); err != nil {
		return err
	}
	segments, err := increment64(report.segments, "membership algebra segments")
	if err != nil {
		return err
	}
	report.segments = segments
	return nil
}

// applyAlgebraBoundary applies every event at one address (Rust
// apply_boundary: the first event plus each queued event at the same
// address).
func applyAlgebraBoundary(first algebraEvent, states []*algebraSourceState, events *algebraEvents, global *algebraGlobalState, eventWork *int, ops rangeOps, check checkpoint) error {
	at := first.at
	if err := applyAlgebraEvent(first, states, events, global, ops, check); err != nil {
		return err
	}
	for {
		next, ok := events.peek()
		if !ok || !(next.at.Equal(at)) {
			return nil
		}
		*eventWork++
		if *eventWork == 4096 {
			*eventWork = 0
			if check != nil {
				if err := check(); err != nil {
					return err
				}
			}
		}
		same, ok := events.pop()
		if !ok {
			return corrupt("membership algebra event disappeared")
		}
		if err := applyAlgebraEvent(same, states, events, global, ops, check); err != nil {
			return err
		}
	}
}

// applyAlgebraEvent applies one Start or End event (Rust apply_event).
func applyAlgebraEvent(event algebraEvent, states []*algebraSourceState, events *algebraEvents, global *algebraGlobalState, ops rangeOps, check checkpoint) error {
	if uint64(event.source) >= uint64(len(states)) {
		return corrupt("membership algebra event source is invalid")
	}
	state := states[event.source]
	if !state.hasRange {
		return corrupt("membership algebra event has no range")
	}
	switch event.kind {
	case algebraEventStart:
		if state.active || !state.rangeFrom.Equal(event.at) {
			return corrupt("membership algebra start event disagrees")
		}
		if err := global.add(state.ranges.present(), state.input.localToGlobal, check); err != nil {
			return err
		}
		state.active = true
		if next, err := ops.next(state.rangeTo); err == nil {
			events.push(algebraEvent{at: next, source: event.source, kind: algebraEventEnd})
		}
	case algebraEventEnd:
		next, nextErr := ops.next(state.rangeTo)
		if !state.active || nextErr != nil || !next.Equal(event.at) {
			return corrupt("membership algebra end event disagrees")
		}
		if err := global.remove(state.ranges.present(), state.input.localToGlobal, check); err != nil {
			return err
		}
		state.active = false
		state.hasRange = false
		if err := algebraLoadNext(state, check); err != nil {
			return err
		}
		if state.hasRange {
			events.push(algebraEvent{at: state.rangeFrom, source: event.source, kind: algebraEventStart})
		}
	}
	work.JoinAdvance(1)
	return nil
}

// emitAlgebraTerminal delivers the final segment to the maximum
// address (Rust emit_terminal).
func emitAlgebraTerminal(from *addrKey, states []*algebraSourceState, ops rangeOps, global *algebraGlobalState, sink *algebraSink, report *scanReport, check checkpoint) error {
	if from == nil || len(global.present) == 0 {
		return nil
	}
	var to addrKey
	found := false
	for _, state := range states {
		if !state.active {
			continue
		}
		if !state.hasRange {
			return corrupt("membership algebra has no terminal range")
		}
		to = state.rangeTo
		found = true
		break
	}
	if !found {
		return corrupt("membership algebra has no terminal range")
	}
	if _, err := ops.next(to); err == nil {
		return corrupt("membership algebra event queue ended early")
	}
	if err := sink.segment(*from, to, global.present, global.counts, ops, check); err != nil {
		return err
	}
	next, err := increment64(report.segments, "membership algebra segments")
	if err != nil {
		return err
	}
	report.segments = next
	return nil
}

// finishAlgebraSources verifies every source ended exactly at its
// expected range count (Rust finish_sources).
func finishAlgebraSources(states []*algebraSourceState, ops rangeOps, report *scanReport, check checkpoint) error {
	for work, state := range states {
		if work&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if state.active {
			if !state.hasRange {
				return corrupt("active membership algebra source has no range")
			}
			if _, err := ops.next(state.rangeTo); err == nil {
				return corrupt("membership algebra source remained active")
			}
			state.active = false
			state.hasRange = false
			if err := algebraLoadNext(state, check); err != nil {
				return err
			}
		}
		if state.hasRange || state.ranges.count() != state.input.expectedRanges {
			return corrupt("membership algebra range count disagrees")
		}
		var ok bool
		report.sourceRanges, ok = addCheckedU64(report.sourceRanges, state.ranges.count())
		if !ok {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership algebra source range count"}
		}
	}
	return nil
}
