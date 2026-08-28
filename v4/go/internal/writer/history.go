// One-pass projection of a last-seen map into several named feeds (Rust
// draft_store/history.rs): the plan validates the windows, interns every
// destination feed, ranks the cutoffs, and prepares the prefix bitmap
// cache; the merge then walks the source and the committed destination in
// lockstep, computing per-window before/after statistics and replacing
// each destination membership with its cutoff-ranked prefix.

package writer

import (
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Exact Rust vector element sizes charged to the history projection heap
// (draft_store/history.rs Heap::vector/filled calls; the Go heap helper
// models the Rust byte counts, not the Go struct layouts).
const (
	historyReportBytes = 400 // size_of::<HistoryWindowReport>()
	historyRunV4Bytes  = 12  // size_of::<Run<Ipv4Key>>()
	historyRunV6Bytes  = 32  // size_of::<Run<Ipv6Key>>()
	historyWordBytes   = 4   // size_of::<u32>()
	historyByteBytes   = 1   // size_of::<u8>()
	historyHandleBytes = 8   // size_of::<MembershipHandle>()
)

// HistoryWindow is one requested history projection window at the
// internal boundary (Rust HistoryWindow): one named feed and the
// last-seen cutoff. The public facade converts its value-free window
// onto this type.
type HistoryWindow struct {
	FeedName string
	Cutoff   uint32
}

// HistoryWindowReport is the exact before/after statistics of one
// projected feed (Rust HistoryWindowReport). The public facade copies
// the values onto its value-free report type.
type HistoryWindowReport struct {
	FeedName            string
	Cutoff              uint32
	Created             bool
	BeforeIntervalCount uint64
	AfterIntervalCount  uint64
	BeforeAddresses     format.Cardinality129
	AfterAddresses      format.Cardinality129
	UnchangedAddresses  format.Cardinality129
	AddedAddresses      format.Cardinality129
	RemovedAddresses    format.Cardinality129
}

// HistoryProjectionReport is the exact aggregate plus per-window outcome
// of one history projection (Rust HistoryProjectionReport). The public
// facade copies the values onto its value-free report type.
type HistoryProjectionReport struct {
	LogicalChange       LogicalChange
	SourceRangeCount    uint64
	SourceAddresses     format.Cardinality129
	CreatedFeedCount    uint64
	BeforeIntervalCount uint64
	AfterIntervalCount  uint64
	BeforeAddresses     format.Cardinality129
	AfterAddresses      format.Cardinality129
	UnchangedAddresses  format.Cardinality129
	AddedAddresses      format.Cardinality129
	RemovedAddresses    format.Cardinality129
	Windows             []HistoryWindowReport
}

// LogicalChange is the internal change classification of one projection
// (Rust workflow::LogicalChange; the same numeric values as the public
// facade constants).
type LogicalChange uint8

const (
	LogicalChanged  LogicalChange = 0
	LogicalNoChange LogicalChange = 1
)

// historyRun is the adjacency state of one window's observation sweep
// (Rust Run<K>). lastTo is embedded by value: the observation hot path
// must not allocate per segment.
type historyRun[K any] struct {
	lastTo    K
	hasLastTo bool
	before    bool
	after     bool
}

// historyCached is one cached transform outcome (Rust Cached; embedded
// by value in the policy so the transform hot path never allocates).
type historyCached struct {
	old    optionalValue
	prefix int
	new    optionalValue
}

// historyPlan is the prepared projection state before the merge starts
// (Rust HistoryPlan).
type historyPlan struct {
	family           uint8
	policy4          historyPolicy[key4]
	policy6          historyPolicy[key6]
	createdFeedCount uint64
}

// historyMerge is one running projection merge (Rust HistoryMerge).
type historyMerge[K any] struct {
	inner            *orderedMerge[uint32, historyPolicy[K], K]
	createdFeedCount uint64
}

// historyPolicy is both the merge policy and the finished merge state
// (Rust HistoryPolicy; the transform observes decode through
// selectedMembershipBits and interns prefix bitmaps through the
// dictionary).
type historyPolicy[K any] struct {
	reports       []HistoryWindowReport
	runs          []historyRun[K]
	cutoffOrder   []uint32
	rank          []uint32
	feedIndexes   []uint32
	feedToWindow  []uint32
	beforeSorted  []uint8
	before        []uint8
	prefixes      []MembershipHandle
	currentPrefix int
	family        uint8 // address family of the projection (Rust HistoryPolicy<K> fixes K at plan time)
	codec         rangeFamily[K]
	aggregate     HistoryWindowReport
	aggregateRun  historyRun[K]
	decodedOld    optionalValue
	decodedOldSet bool
	cache         historyCached
	hasCache      bool
	check         func() error
	// scratchWords is a reusable heap-owned prefix bitmap view (Rust
	// PrefixWords is borrowed for the intern call; Go generic dispatch
	// cannot borrow a stack value, so the view lives on the heap policy
	// and never allocates per prefix).
	scratchWords prefixWords
}

// prepareHistoryPlan validates the windows, ensures every destination
// feed, and charges the whole retained plan against the draft heap
// budget (Rust HistoryPlan::prepare_from).
func prepareHistoryPlan(store *DraftStore, windows []HistoryWindow, check func() error) (*historyPlan, error) {
	windowCount := len(windows)
	if windowCount == 0 {
		return nil, invalid("history windows are empty")
	}
	if uint64(windowCount) > math.MaxUint32 {
		return nil, invalid("history window count exceeds u32")
	}
	heap := newHeapBudget(store.budget.MaxHeapBytes)
	reports, runs4, runs6, cutoffOrder, feedOrder, err := collectHistoryWindows(store, windows, heap, check)
	if err != nil {
		return nil, err
	}
	if err := requireUniqueHistoryNames(reports, feedOrder, check); err != nil {
		return nil, err
	}
	createdFeedCount, originalIndexes, err := ensureHistoryFeeds(store, reports, heap, check)
	if err != nil {
		return nil, err
	}
	if err := heap.filled(uint64(windowCount), historyWordBytes, "history projection heap"); err != nil {
		return nil, err
	}
	rank := make([]uint32, windowCount)
	if err := orderHistoryCutoffs(reports, cutoffOrder, rank, check); err != nil {
		return nil, err
	}
	feedIndexes, feedToWindow, err := orderHistoryFeedIndexes(originalIndexes, feedOrder, heap, check)
	if err != nil {
		return nil, err
	}
	if err := heap.filled(uint64(windowCount), historyByteBytes, "history projection heap"); err != nil {
		return nil, err
	}
	beforeSorted := make([]uint8, windowCount)
	if err := heap.filled(uint64(windowCount), historyByteBytes, "history projection heap"); err != nil {
		return nil, err
	}
	before := make([]uint8, windowCount)
	if err := heap.filled(uint64(windowCount)+1, historyHandleBytes, "history projection heap"); err != nil {
		return nil, err
	}
	prefixes := make([]MembershipHandle, windowCount+1)
	policy4 := historyPolicy[key4]{}
	policy6 := historyPolicy[key6]{}
	if store.draft.meta.AddressFamily == format.AddressFamilyIPv4 {
		policy4 = historyPolicy[key4]{
			reports:       reports,
			runs:          runs4,
			cutoffOrder:   cutoffOrder,
			rank:          rank,
			feedIndexes:   feedIndexes,
			feedToWindow:  feedToWindow,
			beforeSorted:  beforeSorted,
			before:        before,
			prefixes:      prefixes,
			currentPrefix: 0,
			family:        store.draft.meta.AddressFamily,
			codec:         rangeCodec4{},
			aggregate:     emptyHistoryReport(HistoryWindow{FeedName: "aggregate"}, false),
			decodedOld:    optionalValue{},
			decodedOldSet: false,
			cache:         historyCached{},
			hasCache:      false,
			check:         check,
		}
	} else {
		policy6 = historyPolicy[key6]{
			reports:       reports,
			runs:          runs6,
			cutoffOrder:   cutoffOrder,
			rank:          rank,
			feedIndexes:   feedIndexes,
			feedToWindow:  feedToWindow,
			beforeSorted:  beforeSorted,
			before:        before,
			prefixes:      prefixes,
			currentPrefix: 0,
			family:        store.draft.meta.AddressFamily,
			codec:         rangeCodec6{},
			aggregate:     emptyHistoryReport(HistoryWindow{FeedName: "aggregate"}, false),
			decodedOld:    optionalValue{},
			decodedOldSet: false,
			cache:         historyCached{},
			hasCache:      false,
			check:         check,
		}
	}
	return &historyPlan{family: store.draft.meta.AddressFamily, policy4: policy4, policy6: policy6, createdFeedCount: createdFeedCount}, nil
}

// begin starts the projection merge over the committed destination (Rust
// HistoryPlan::begin).
func (p *historyPlan) begin(store *DraftStore, base format.Meta, check func() error) (*historyMerge[key4], *historyMerge[key6], error) {
	if base.AddressFamily == format.AddressFamilyIPv4 {
		inner, err := newOrderedMerge[uint32, historyPolicy[key4], key4](store, base, rangeCodec4{}, &p.policy4, check)
		if err != nil {
			return nil, nil, err
		}
		return &historyMerge[key4]{inner: inner, createdFeedCount: p.createdFeedCount}, nil, nil
	}
	inner, err := newOrderedMerge[uint32, historyPolicy[key6], key6](store, base, rangeCodec6{}, &p.policy6, check)
	if err != nil {
		return nil, nil, err
	}
	return nil, &historyMerge[key6]{inner: inner, createdFeedCount: p.createdFeedCount}, nil
}

// collectHistoryWindows copies the window requests into the charged plan
// vectors (Rust collect_windows).
func collectHistoryWindows(store *DraftStore, windows []HistoryWindow, heap *heapBudget, check func() error) ([]HistoryWindowReport, []historyRun[key4], []historyRun[key6], []uint32, []uint32, error) {
	windowCount := uint64(len(windows))
	if err := heap.vector(windowCount, historyReportBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	runBytes := uint64(historyRunV4Bytes)
	if store.draft.meta.AddressFamily == format.AddressFamilyIPv6 {
		runBytes = historyRunV6Bytes
	}
	if err := heap.vector(windowCount, runBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if err := heap.vector(windowCount, historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if err := heap.vector(windowCount, historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	reports := make([]HistoryWindowReport, windowCount)
	runs4 := make([]historyRun[key4], windowCount)
	runs6 := make([]historyRun[key6], windowCount)
	cutoffOrder := make([]uint32, windowCount)
	feedOrder := make([]uint32, windowCount)
	for index, request := range windows {
		if err := checkEvery(uint32(index), check); err != nil {
			return nil, nil, nil, nil, nil, err
		}
		if !format.FeedNameValidString(request.FeedName) {
			return nil, nil, nil, nil, nil, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
		}
		reports[index] = emptyHistoryReport(request, false)
		runs4[index] = historyRun[key4]{}
		runs6[index] = historyRun[key6]{}
		cutoffOrder[index] = uint32(index)
		feedOrder[index] = uint32(index)
	}
	return reports, runs4, runs6, cutoffOrder, feedOrder, nil
}

// requireUniqueHistoryNames rejects duplicate destination feed names
// (Rust require_unique_names).
func requireUniqueHistoryNames(reports []HistoryWindowReport, feedOrder []uint32, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	slices.SortFunc(feedOrder, func(left, right uint32) int {
		return strings.Compare(reports[left].FeedName, reports[right].FeedName)
	})
	if err := check(); err != nil {
		return err
	}
	for work := 0; work+1 < len(feedOrder); work++ {
		if err := checkEvery(uint32(work), check); err != nil {
			return err
		}
		if reports[feedOrder[work]].FeedName == reports[feedOrder[work+1]].FeedName {
			return invalid("history window feed names are not unique")
		}
	}
	return nil
}

// ensureHistoryFeeds creates the missing destination feeds and records
// their indexes (Rust ensure_feeds).
func ensureHistoryFeeds(store *DraftStore, reports []HistoryWindowReport, heap *heapBudget, check func() error) (uint64, []uint32, error) {
	var createdFeedCount uint64
	if err := heap.vector(uint64(len(reports)), historyWordBytes, "history projection heap"); err != nil {
		return 0, nil, err
	}
	indexes := make([]uint32, len(reports))
	for work := range reports {
		if err := checkEvery(uint32(work), check); err != nil {
			return 0, nil, err
		}
		entry, created, err := store.ensureFeed(reports[work].FeedName)
		if err != nil {
			return 0, nil, err
		}
		reports[work].Created = created
		var added uint64
		if created {
			added = 1
		}
		next, err := checkedAdd(createdFeedCount, added, "created history feed count")
		if err != nil {
			return 0, nil, err
		}
		createdFeedCount = next
		indexes[work] = entry.Index
	}
	return createdFeedCount, indexes, nil
}

// orderHistoryCutoffs ranks the windows by (cutoff, feed name) (Rust
// order_cutoffs).
func orderHistoryCutoffs(reports []HistoryWindowReport, cutoffOrder []uint32, rank []uint32, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	slices.SortFunc(cutoffOrder, func(left, right uint32) int {
		leftReport := reports[left]
		rightReport := reports[right]
		if leftReport.Cutoff != rightReport.Cutoff {
			if leftReport.Cutoff < rightReport.Cutoff {
				return -1
			}
			return 1
		}
		return strings.Compare(leftReport.FeedName, rightReport.FeedName)
	})
	if err := check(); err != nil {
		return err
	}
	for position, window := range cutoffOrder {
		if err := checkEvery(uint32(position), check); err != nil {
			return err
		}
		rank[window] = uint32(position)
	}
	return nil
}

// orderHistoryFeedIndexes sorts the feed-order vector by the original
// feed indexes and builds the feed-index vector of the same order (Rust
// order_feed_indexes).
func orderHistoryFeedIndexes(original []uint32, feedToWindow []uint32, heap *heapBudget, check func() error) ([]uint32, []uint32, error) {
	if err := check(); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(feedToWindow, func(left, right uint32) int {
		if original[left] < original[right] {
			return -1
		}
		if original[left] > original[right] {
			return 1
		}
		return 0
	})
	if err := check(); err != nil {
		return nil, nil, err
	}
	if err := heap.vector(uint64(len(original)), historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, err
	}
	indexes := make([]uint32, len(original))
	for work, window := range feedToWindow {
		if err := checkEvery(uint32(work), check); err != nil {
			return nil, nil, err
		}
		indexes[work] = original[window]
	}
	return indexes, feedToWindow, nil
}

// push feeds one source range into the projection merge (Rust
// HistoryMerge::push).
func (m *historyMerge[K]) push(store *DraftStore, from, to K, lastSeen uint32, check func() error) error {
	return m.inner.push(store, incomingRange[uint32, K]{from: from, to: to, value: lastSeen}, check)
}

// finish ends the merge and produces the projection report (Rust
// HistoryMerge::finish).
func (m *historyMerge[K]) finish(store *DraftStore, check func() error, sourceRangeCount uint64, sourceAddresses format.Cardinality129) (*HistoryProjectionReport, error) {
	policy, err := m.inner.finish(store, check)
	if err != nil {
		return nil, err
	}
	return policy.finishReport(sourceRangeCount, sourceAddresses, m.createdFeedCount)
}

func (p *historyPolicy[K]) preserveWithoutInput() bool { return false }

// transform computes the new membership of one segment (Rust
// HistoryPolicy::transform): the destination bitmap loses every feed
// whose cutoff is below the incoming last-seen, then gains exactly the
// feeds ranked below the current prefix.
func (p *historyPolicy[K]) transform(store *DraftStore, old optionalValue, incoming incomingValue[uint32]) (optionalValue, error) {
	p.currentPrefix = 0
	if incoming.present {
		p.currentPrefix = sort.Search(len(p.cutoffOrder), func(index int) bool {
			return p.reports[p.cutoffOrder[index]].Cutoff >= incoming.value
		})
	}
	oldID := uint32(0)
	if old.present {
		oldID = old.value
	}
	if !p.decodedOldSet || p.decodedOld.value != oldID {
		if err := store.selectedMembershipBits(oldID, p.feedIndexes, p.beforeSorted, p.check); err != nil {
			return noneValue(), err
		}
		for position, window := range p.feedToWindow {
			if err := checkEvery(position, p.check); err != nil {
				return noneValue(), err
			}
			p.before[window] = p.beforeSorted[position]
		}
		p.decodedOld = optionalValue{value: oldID, present: true}
		p.decodedOldSet = true
	}
	if p.hasCache && sameOptional(p.cache.old, old) && p.cache.prefix == p.currentPrefix {
		return p.cache.new, nil
	}
	if matches, err := p.matchesPrefix(); err != nil {
		return noneValue(), err
	} else if matches {
		p.cache = historyCached{old: old, prefix: p.currentPrefix, new: old}
		p.hasCache = true
		return old, nil
	}
	var withoutTargets uint32
	var withoutPresent bool
	if old.present {
		all, err := p.prefix(store, len(p.reports))
		if err != nil {
			return noneValue(), err
		}
		allID, allWords := all.stored()
		combined, present, err := store.combineMemberships(old.value, allID, allWords, MembershipDifference)
		if err != nil {
			return noneValue(), err
		}
		withoutTargets = combined
		withoutPresent = present
	}
	prefix, err := p.prefix(store, p.currentPrefix)
	if err != nil {
		return noneValue(), err
	}
	prefixID, prefixWords := prefix.stored()
	var new optionalValue
	switch {
	case !withoutPresent && prefixID == 0:
		new = noneValue()
	case !withoutPresent:
		new = someValue(prefixID)
	case prefixID == 0:
		new = someValue(withoutTargets)
	default:
		combined, present, err := store.combineMemberships(withoutTargets, prefixID, prefixWords, MembershipUnion)
		if err != nil {
			return noneValue(), err
		}
		if present {
			new = someValue(combined)
		} else {
			new = noneValue()
		}
	}
	p.cache = historyCached{old: old, prefix: p.currentPrefix, new: new}
	p.hasCache = true
	return new, nil
}

// observe folds one segment into every window and the aggregate (Rust
// HistoryPolicy::observe).
func (p *historyPolicy[K]) observe(from, to K, _old optionalValue, _incoming incomingValue[uint32], _new optionalValue) error {
	count, err := familyInclusiveCardinalityOf(p.codec, from, to)
	if err != nil {
		return err
	}
	var beforeAny bool
	for index := range p.reports {
		if err := checkEvery(uint32(index), p.check); err != nil {
			return err
		}
		before := p.before[index] != 0
		after := p.rank[index] < uint32(p.currentPrefix)
		beforeAny = beforeAny || before
		if err := observeHistoryWindow(&p.reports[index], &p.runs[index], p.codec, from, to, count, before, after); err != nil {
			return err
		}
		work.HistoryWindowTest(1)
	}
	return observeHistoryWindow(&p.aggregate, &p.aggregateRun, p.codec, from, to, count, beforeAny, p.currentPrefix != 0)
}

// finish returns the policy (Rust HistoryPolicy::finish: the policy IS
// the merge output).
func (p *historyPolicy[K]) finish() (historyPolicy[K], error) { return *p, nil }

// prefix returns the interned prefix bitmap of the first length feeds in
// cutoff rank, caching it in the plan (Rust HistoryPolicy::prefix).
func (p *historyPolicy[K]) prefix(store *DraftStore, length int) (MembershipHandle, error) {
	if length < 0 || length >= len(p.prefixes) {
		return MembershipHandle{}, corrupt("history prefix is outside the window set")
	}
	cached := p.prefixes[length]
	if length == 0 || !cached.isEmpty() {
		return cached, nil
	}
	view := &p.scratchWords
	wordCount, err := historyPrefixWordCount(p.feedIndexes, p.feedToWindow, p.rank, uint32(length), p.check)
	if err != nil {
		return MembershipHandle{}, err
	}
	*view = prefixWords{
		feedIndexes:  p.feedIndexes,
		feedToWindow: p.feedToWindow,
		rank:         p.rank,
		prefix:       uint32(length),
		wordCount:    wordCount,
		check:        p.check,
	}
	interned, err := draftInternMembership(store, view)
	if err != nil {
		return MembershipHandle{}, err
	}
	prefix := handleFromInterned(interned)
	p.prefixes[length] = prefix
	return prefix, nil
}

// matchesPrefix reports whether the decoded old bitmap already equals
// the current prefix bitmap (Rust HistoryPolicy::matches_prefix).
func (p *historyPolicy[K]) matchesPrefix() (bool, error) {
	for window, before := range p.before {
		if err := checkEvery(uint32(window), p.check); err != nil {
			return false, err
		}
		if (before != 0) != (p.rank[window] < uint32(p.currentPrefix)) {
			return false, nil
		}
	}
	return true, nil
}

// finishReport balances and assembles the projection report (Rust
// HistoryPolicy::finish_report).
func (p *historyPolicy[K]) finishReport(sourceRangeCount uint64, sourceAddresses format.Cardinality129, createdFeedCount uint64) (*HistoryProjectionReport, error) {
	changed := createdFeedCount != 0
	for work := range p.reports {
		if err := checkEvery(uint32(work), p.check); err != nil {
			return nil, err
		}
		if err := requireBalancedHistoryReport(&p.reports[work]); err != nil {
			return nil, err
		}
		changed = changed ||
			p.reports[work].AddedAddresses != format.CardinalityZero() ||
			p.reports[work].RemovedAddresses != format.CardinalityZero()
	}
	if err := requireBalancedHistoryReport(&p.aggregate); err != nil {
		return nil, err
	}
	aggregate := p.aggregate
	change := LogicalNoChange
	if changed {
		change = LogicalChanged
	}
	return &HistoryProjectionReport{
		LogicalChange:       change,
		SourceRangeCount:    sourceRangeCount,
		SourceAddresses:     sourceAddresses,
		CreatedFeedCount:    createdFeedCount,
		BeforeIntervalCount: aggregate.BeforeIntervalCount,
		AfterIntervalCount:  aggregate.AfterIntervalCount,
		BeforeAddresses:     aggregate.BeforeAddresses,
		AfterAddresses:      aggregate.AfterAddresses,
		UnchangedAddresses:  aggregate.UnchangedAddresses,
		AddedAddresses:      aggregate.AddedAddresses,
		RemovedAddresses:    aggregate.RemovedAddresses,
		Windows:             p.reports,
	}, nil
}

// prefixWords is one caller-owned prefix bitmap source over the ranked
// feed indexes (Rust PrefixWords: reads only caller-owned output words).
// historyPrefixWordCount computes the canonical bitmap word count of a
// prefix (Rust PrefixWords::new: the last selected feed index's word,
// with the cancellation cadence and a corrupt error when no feed is
// selected instead of a panic).
func historyPrefixWordCount(feedIndexes, feedToWindow, rank []uint32, prefix uint32, check func() error) (uint32, error) {
	var wordCount uint32
	for position := len(feedIndexes) - 1; position >= 0; position-- {
		if err := checkEvery(uint32(position), check); err != nil {
			return 0, err
		}
		window := feedToWindow[position]
		if rank[window] < prefix {
			wordCount = feedIndexes[position]/64 + 1
			break
		}
	}
	if wordCount == 0 {
		return 0, corrupt("nonempty history prefix has no feeds")
	}
	return wordCount, nil
}

type prefixWords struct {
	feedIndexes  []uint32
	feedToWindow []uint32
	rank         []uint32
	prefix       uint32
	wordCount    uint32
	check        func() error
}

// WordCount returns the canonical bitmap word count computed once when
// the prefix is built (Rust PrefixWords::new stores word_count).
func (w *prefixWords) WordCount() uint32 { return w.wordCount }

// ReadChunk returns the selected prefix bits starting at start by value
// (Rust PrefixWords::read_words with a HASH_WORDS chunk).
func (w *prefixWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	count = membershipChunkWords
	if remaining := w.WordCount() - start; count > remaining {
		count = remaining
	}
	next, err := checkedAdd(uint64(start), uint64(count), "history prefix word range")
	if err != nil {
		return words, 0, err
	}
	if next > math.MaxUint32 {
		return words, 0, overflow("history prefix word range")
	}
	end := uint32(next)
	firstIndex := start
	if firstIndex > math.MaxUint32/64 {
		return words, 0, overflow("history prefix bit range")
	}
	firstIndex *= 64
	position := sort.Search(len(w.feedIndexes), func(index int) bool {
		return w.feedIndexes[index] >= firstIndex
	})
	work := 0
	for position < len(w.feedIndexes) {
		index := w.feedIndexes[position]
		word := index / 64
		if word >= end {
			break
		}
		if err := checkEvery(uint32(work), w.check); err != nil {
			return words, 0, err
		}
		window := w.feedToWindow[position]
		if w.rank[window] < w.prefix {
			words[(word - start)] |= uint64(1) << (index % 64)
		}
		position++
		work++
	}
	return words, count, nil
}

// observeHistoryWindow folds one segment into one window report (Rust
// observe).
func observeHistoryWindow[K any](report *HistoryWindowReport, run *historyRun[K], codec rangeFamily[K], from, to K, count format.Cardinality129, before, after bool) error {
	adjacent := false
	if run.hasLastTo {
		next, ok := codec.Next(run.lastTo)
		adjacent = ok && codec.Equal(next, from)
	}
	if before {
		next, err := addHistoryCount(report.BeforeAddresses, count)
		if err != nil {
			return err
		}
		report.BeforeAddresses = next
		if !adjacent || !run.before {
			next, err := incrementHistoryCount(report.BeforeIntervalCount)
			if err != nil {
				return err
			}
			report.BeforeIntervalCount = next
		}
	}
	if after {
		next, err := addHistoryCount(report.AfterAddresses, count)
		if err != nil {
			return err
		}
		report.AfterAddresses = next
		if !adjacent || !run.after {
			next, err := incrementHistoryCount(report.AfterIntervalCount)
			if err != nil {
				return err
			}
			report.AfterIntervalCount = next
		}
	}
	switch {
	case before && after:
		next, err := addHistoryCount(report.UnchangedAddresses, count)
		if err != nil {
			return err
		}
		report.UnchangedAddresses = next
	case before && !after:
		next, err := addHistoryCount(report.RemovedAddresses, count)
		if err != nil {
			return err
		}
		report.RemovedAddresses = next
	case !before && after:
		next, err := addHistoryCount(report.AddedAddresses, count)
		if err != nil {
			return err
		}
		report.AddedAddresses = next
	}
	run.lastTo = to
	run.hasLastTo = true
	run.before = before
	run.after = after
	return nil
}

// requireBalancedHistoryReport verifies the before/after accounting of
// one window (Rust require_balanced).
func requireBalancedHistoryReport(report *HistoryWindowReport) error {
	before, err := addHistoryCount(report.UnchangedAddresses, report.RemovedAddresses)
	if err != nil {
		return err
	}
	after, err := addHistoryCount(report.UnchangedAddresses, report.AddedAddresses)
	if err != nil {
		return err
	}
	zero := format.CardinalityZero()
	if before != report.BeforeAddresses || after != report.AfterAddresses ||
		(report.BeforeIntervalCount == 0) != (report.BeforeAddresses == zero) ||
		(report.AfterIntervalCount == 0) != (report.AfterAddresses == zero) {
		return corrupt("history projection counters do not balance")
	}
	return nil
}

// emptyHistoryReport builds the zero report of one window (Rust
// empty_report).
func emptyHistoryReport(window HistoryWindow, created bool) HistoryWindowReport {
	return HistoryWindowReport{
		FeedName: window.FeedName,
		Cutoff:   window.Cutoff,
		Created:  created,
	}
}

// addHistoryCount is the checked report count addition (Rust add).
func addHistoryCount(left, right format.Cardinality129) (format.Cardinality129, error) {
	next, err := left.Add(right)
	if err != nil {
		return format.CardinalityZero(), overflow("history projection addresses")
	}
	return next, nil
}

// incrementHistoryCount is the checked interval count increment (Rust
// increment).
func incrementHistoryCount(value uint64) (uint64, error) {
	return checkedAdd(value, 1, "history projection intervals")
}

// familyInclusiveCardinality returns the exact inclusive size of one
// interval in its address family (Rust IpKey::inclusive_cardinality).
func familyInclusiveCardinality(family uint8, from, to tree.Key) (format.Cardinality129, error) {
	if family == format.AddressFamilyIPv4 {
		size, err := format.IPv4Inclusive(from.U32(), to.U32())
		if err != nil {
			return format.CardinalityZero(), overflow("IPv4 interval cardinality")
		}
		return size, nil
	}
	fromHi, fromLo := from.U128()
	toHi, toLo := to.U128()
	size, err := format.IPv6Inclusive(fromHi, fromLo, toHi, toLo)
	if err != nil {
		return format.CardinalityZero(), overflow("IPv6 interval cardinality")
	}
	return size, nil
}

// familyInclusiveCardinalityOf returns the exact inclusive size of one
// typed interval in its address family (Rust IpKey::inclusive_cardinality
// over the family codec).
func familyInclusiveCardinalityOf[K any](codec rangeFamily[K], from, to K) (format.Cardinality129, error) {
	if _, ok := any(codec).(rangeCodec4); ok {
		from4 := any(from).(key4)
		to4 := any(to).(key4)
		size, err := format.IPv4Inclusive(uint32(from4), uint32(to4))
		if err != nil {
			return format.CardinalityZero(), overflow("IPv4 interval cardinality")
		}
		return size, nil
	}
	from6 := any(from).(key6)
	to6 := any(to).(key6)
	size, err := format.IPv6Inclusive(from6.hi, from6.lo, to6.hi, to6.lo)
	if err != nil {
		return format.CardinalityZero(), overflow("IPv6 interval cardinality")
	}
	return size, nil
}
