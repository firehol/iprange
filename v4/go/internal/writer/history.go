// One-pass projection of a last-seen map into several named feeds (Rust
// draft_store/history.rs): the plan validates the windows, interns every
// destination feed, ranks the cutoffs, and prepares the prefix bitmap
// cache; the merge then walks the source and the committed destination in
// lockstep, computing per-window before/after statistics and replacing
// each destination membership with its cutoff-ranked prefix.

package writer

import (
	"math"
	"sort"

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

// historyWindow is one requested history projection window (Rust
// HistoryWindow): one named feed and the last-seen cutoff.
type historyWindow struct {
	feedName string
	cutoff   uint32
}

// historyWindowReport is the exact before/after statistics of one
// projected feed (Rust HistoryWindowReport).
type historyWindowReport struct {
	feedName            string
	cutoff              uint32
	created             bool
	beforeIntervalCount uint64
	afterIntervalCount  uint64
	beforeAddresses     format.Cardinality129
	afterAddresses      format.Cardinality129
	unchangedAddresses  format.Cardinality129
	addedAddresses      format.Cardinality129
	removedAddresses    format.Cardinality129
}

// historyProjectionReport is the exact aggregate plus per-window outcome
// of one history projection (Rust HistoryProjectionReport).
type historyProjectionReport struct {
	logicalChange       logicalChange
	sourceRangeCount    uint64
	sourceAddresses     format.Cardinality129
	createdFeedCount    uint64
	beforeIntervalCount uint64
	afterIntervalCount  uint64
	beforeAddresses     format.Cardinality129
	afterAddresses      format.Cardinality129
	unchangedAddresses  format.Cardinality129
	addedAddresses      format.Cardinality129
	removedAddresses    format.Cardinality129
	windows             []historyWindowReport
}

// logicalChange is the internal change classification (Rust
// workflow::LogicalChange; the public values are mirrored by the public
// facade).
type logicalChange uint8

const (
	logicalChanged  logicalChange = 0
	logicalNoChange logicalChange = 1
)

// historyRun is the adjacency state of one window's observation sweep
// (Rust Run<K>).
type historyRun struct {
	lastTo *tree.Key
	before bool
	after  bool
}

// historyCached is one cached transform outcome (Rust Cached).
type historyCached struct {
	old    *uint32
	prefix int
	new    *uint32
}

// historyPlan is the prepared projection state before the merge starts
// (Rust HistoryPlan).
type historyPlan struct {
	policy           historyPolicy
	createdFeedCount uint64
}

// historyMerge is one running projection merge (Rust HistoryMerge).
type historyMerge struct {
	inner            *orderedMerge[historyPolicy]
	createdFeedCount uint64
}

// historyPolicy is both the merge policy and the finished merge state
// (Rust HistoryPolicy; the transform observes decode through
// selectedMembershipBits and interns prefix bitmaps through the
// dictionary).
type historyPolicy struct {
	reports       []historyWindowReport
	runs          []historyRun
	cutoffOrder   []uint32
	rank          []uint32
	feedIndexes   []uint32
	feedToWindow  []uint32
	beforeSorted  []uint8
	before        []uint8
	prefixes      []membershipHandle
	currentPrefix int
	family        uint8 // address family of the projection (Rust HistoryPolicy<K> fixes K at plan time)
	aggregate     historyWindowReport
	aggregateRun  historyRun
	decodedOld    *uint32
	cache         *historyCached
	check         func() error
}

// prepareHistoryPlan validates the windows, ensures every destination
// feed, and charges the whole retained plan against the draft heap
// budget (Rust HistoryPlan::prepare_from).
func prepareHistoryPlan(store *DraftStore, windows []historyWindow, check func() error) (*historyPlan, error) {
	windowCount := len(windows)
	if windowCount == 0 {
		return nil, invalid("history windows are empty")
	}
	if windowCount > math.MaxUint32 {
		return nil, invalid("history window count exceeds u32")
	}
	heap := newHeapBudget(store.budget.MaxHeapBytes)
	reports, runs, cutoffOrder, feedOrder, err := collectHistoryWindows(store, windows, heap, check)
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
	prefixes := make([]membershipHandle, windowCount+1)
	policy := historyPolicy{
		reports:       reports,
		runs:          runs,
		cutoffOrder:   cutoffOrder,
		rank:          rank,
		feedIndexes:   feedIndexes,
		feedToWindow:  feedToWindow,
		beforeSorted:  beforeSorted,
		before:        before,
		prefixes:      prefixes,
		currentPrefix: 0,
		family:        store.draft.meta.AddressFamily,
		aggregate:     emptyHistoryReport(historyWindow{feedName: "aggregate", cutoff: 0}, false),
		aggregateRun:  historyRun{},
		decodedOld:    nil,
		cache:         nil,
		check:         check,
	}
	return &historyPlan{policy: policy, createdFeedCount: createdFeedCount}, nil
}

// begin starts the projection merge over the committed destination (Rust
// HistoryPlan::begin).
func (p historyPlan) begin(store *DraftStore, base format.Meta, check func() error) (*historyMerge, error) {
	var codec rangeFamily
	if base.AddressFamily == format.AddressFamilyIPv4 {
		codec = rangeCodec4{}
	} else {
		codec = rangeCodec6{}
	}
	inner, err := newOrderedMerge[historyPolicy](store, base, codec, &p.policy, check)
	if err != nil {
		return nil, err
	}
	return &historyMerge{inner: inner, createdFeedCount: p.createdFeedCount}, nil
}

// collectHistoryWindows copies the window requests into the charged plan
// vectors (Rust collect_windows).
func collectHistoryWindows(store *DraftStore, windows []historyWindow, heap *heapBudget, check func() error) ([]historyWindowReport, []historyRun, []uint32, []uint32, error) {
	windowCount := uint64(len(windows))
	if err := heap.vector(windowCount, historyReportBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, err
	}
	runBytes := uint64(historyRunV4Bytes)
	if store.draft.meta.AddressFamily == format.AddressFamilyIPv6 {
		runBytes = historyRunV6Bytes
	}
	if err := heap.vector(windowCount, runBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := heap.vector(windowCount, historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := heap.vector(windowCount, historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, nil, nil, err
	}
	reports := make([]historyWindowReport, windowCount)
	runs := make([]historyRun, windowCount)
	cutoffOrder := make([]uint32, windowCount)
	feedOrder := make([]uint32, windowCount)
	for index, request := range windows {
		if index&4095 == 4095 {
			if err := check(); err != nil {
				return nil, nil, nil, nil, err
			}
		}
		if !format.FeedNameValidString(request.feedName) {
			return nil, nil, nil, nil, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
		}
		reports[index] = emptyHistoryReport(request, false)
		runs[index] = historyRun{}
		cutoffOrder[index] = uint32(index)
		feedOrder[index] = uint32(index)
	}
	return reports, runs, cutoffOrder, feedOrder, nil
}

// requireUniqueHistoryNames rejects duplicate destination feed names
// (Rust require_unique_names).
func requireUniqueHistoryNames(reports []historyWindowReport, feedOrder []uint32, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	sort.Slice(feedOrder, func(left, right int) bool {
		return reports[feedOrder[left]].feedName < reports[feedOrder[right]].feedName
	})
	if err := check(); err != nil {
		return err
	}
	for work := 0; work+1 < len(feedOrder); work++ {
		if work&4095 == 4095 {
			if err := check(); err != nil {
				return err
			}
		}
		if reports[feedOrder[work]].feedName == reports[feedOrder[work+1]].feedName {
			return invalid("history window feed names are not unique")
		}
	}
	return nil
}

// ensureHistoryFeeds creates the missing destination feeds and records
// their indexes (Rust ensure_feeds).
func ensureHistoryFeeds(store *DraftStore, reports []historyWindowReport, heap *heapBudget, check func() error) (uint64, []uint32, error) {
	var createdFeedCount uint64
	if err := heap.vector(uint64(len(reports)), historyWordBytes, "history projection heap"); err != nil {
		return 0, nil, err
	}
	indexes := make([]uint32, len(reports))
	for work := range reports {
		if work&4095 == 4095 {
			if err := check(); err != nil {
				return 0, nil, err
			}
		}
		entry, created, err := store.ensureFeed(reports[work].feedName)
		if err != nil {
			return 0, nil, err
		}
		reports[work].created = created
		var added uint64
		if created {
			added = 1
		}
		next, err := checkedAdd(createdFeedCount, added, "created history feed count")
		if err != nil {
			return 0, nil, err
		}
		createdFeedCount = next
		indexes[work] = entry.index
	}
	return createdFeedCount, indexes, nil
}

// orderHistoryCutoffs ranks the windows by (cutoff, feed name) (Rust
// order_cutoffs).
func orderHistoryCutoffs(reports []historyWindowReport, cutoffOrder []uint32, rank []uint32, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	sort.Slice(cutoffOrder, func(left, right int) bool {
		leftReport := reports[cutoffOrder[left]]
		rightReport := reports[cutoffOrder[right]]
		if leftReport.cutoff != rightReport.cutoff {
			return leftReport.cutoff < rightReport.cutoff
		}
		return leftReport.feedName < rightReport.feedName
	})
	if err := check(); err != nil {
		return err
	}
	for position, window := range cutoffOrder {
		if position&4095 == 4095 {
			if err := check(); err != nil {
				return err
			}
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
	sort.Slice(feedToWindow, func(left, right int) bool {
		return original[feedToWindow[left]] < original[feedToWindow[right]]
	})
	if err := check(); err != nil {
		return nil, nil, err
	}
	if err := heap.vector(uint64(len(original)), historyWordBytes, "history projection heap"); err != nil {
		return nil, nil, err
	}
	indexes := make([]uint32, len(original))
	for work, window := range feedToWindow {
		if work&4095 == 4095 {
			if err := check(); err != nil {
				return nil, nil, err
			}
		}
		indexes[work] = original[window]
	}
	return indexes, feedToWindow, nil
}

// push feeds one source range into the projection merge (Rust
// HistoryMerge::push).
func (m *historyMerge) push(store *DraftStore, from, to tree.Key, lastSeen uint32, check func() error) error {
	return m.inner.push(store, incomingRange{from: from, to: to, value: lastSeen}, check)
}

// finish ends the merge and produces the projection report (Rust
// HistoryMerge::finish).
func (m *historyMerge) finish(store *DraftStore, check func() error, sourceRangeCount uint64, sourceAddresses format.Cardinality129) (*historyProjectionReport, error) {
	policy, err := m.inner.finish(store, check)
	if err != nil {
		return nil, err
	}
	return policy.finishReport(sourceRangeCount, sourceAddresses, m.createdFeedCount)
}

func (p *historyPolicy) preserveWithoutInput() bool { return false }

// transform computes the new membership of one segment (Rust
// HistoryPolicy::transform): the destination bitmap loses every feed
// whose cutoff is below the incoming last-seen, then gains exactly the
// feeds ranked below the current prefix.
func (p *historyPolicy) transform(store *DraftStore, old *uint32, incoming *uint32) (*uint32, error) {
	p.currentPrefix = 0
	if incoming != nil {
		p.currentPrefix = sort.Search(len(p.cutoffOrder), func(index int) bool {
			return p.reports[p.cutoffOrder[index]].cutoff >= *incoming
		})
	}
	oldID := uint32(0)
	if old != nil {
		oldID = *old
	}
	if p.decodedOld == nil || *p.decodedOld != oldID {
		if err := store.selectedMembershipBits(oldID, p.feedIndexes, p.beforeSorted, p.check); err != nil {
			return nil, err
		}
		for position, window := range p.feedToWindow {
			if position&4095 == 4095 {
				if err := p.check(); err != nil {
					return nil, err
				}
			}
			p.before[window] = p.beforeSorted[position]
		}
		p.decodedOld = &oldID
	}
	if p.cache != nil && sameOptionalUint32(p.cache.old, old) && p.cache.prefix == p.currentPrefix {
		return p.cache.new, nil
	}
	if matches, err := p.matchesPrefix(); err != nil {
		return nil, err
	} else if matches {
		p.cache = &historyCached{old: old, prefix: p.currentPrefix, new: old}
		return old, nil
	}
	var withoutTargets uint32
	var withoutPresent bool
	if old != nil {
		all, err := p.prefix(store, len(p.reports))
		if err != nil {
			return nil, err
		}
		allID, allWords := all.stored()
		combined, present, err := store.combineMemberships(*old, allID, allWords, membershipDifference)
		if err != nil {
			return nil, err
		}
		withoutTargets = combined
		withoutPresent = present
	}
	prefix, err := p.prefix(store, p.currentPrefix)
	if err != nil {
		return nil, err
	}
	prefixID, prefixWords := prefix.stored()
	var new *uint32
	switch {
	case !withoutPresent && prefixID == 0:
		new = nil
	case !withoutPresent:
		new = &prefixID
	case prefixID == 0:
		new = &withoutTargets
	default:
		combined, present, err := store.combineMemberships(withoutTargets, prefixID, prefixWords, membershipUnion)
		if err != nil {
			return nil, err
		}
		if present {
			new = &combined
		} else {
			new = nil
		}
	}
	p.cache = &historyCached{old: old, prefix: p.currentPrefix, new: new}
	return new, nil
}

// observe folds one segment into every window and the aggregate (Rust
// HistoryPolicy::observe).
func (p *historyPolicy) observe(from, to tree.Key, _old, _incoming, _new *uint32) error {
	count, err := familyInclusiveCardinality(p.family, from, to)
	if err != nil {
		return err
	}
	var beforeAny bool
	for index := range p.reports {
		if index&4095 == 4095 {
			if err := p.check(); err != nil {
				return err
			}
		}
		before := p.before[index] != 0
		after := p.rank[index] < uint32(p.currentPrefix)
		beforeAny = beforeAny || before
		if err := observeHistoryWindow(&p.reports[index], &p.runs[index], p.family, from, to, count, before, after); err != nil {
			return err
		}
		work.HistoryWindowTest(1)
	}
	return observeHistoryWindow(&p.aggregate, &p.aggregateRun, p.family, from, to, count, beforeAny, p.currentPrefix != 0)
}

// finish returns the policy (Rust HistoryPolicy::finish: the policy IS
// the merge output).
func (p *historyPolicy) finish() (historyPolicy, error) { return *p, nil }

// prefix returns the interned prefix bitmap of the first length feeds in
// cutoff rank, caching it in the plan (Rust HistoryPolicy::prefix).
func (p *historyPolicy) prefix(store *DraftStore, length int) (membershipHandle, error) {
	if length < 0 || length >= len(p.prefixes) {
		return membershipHandle{}, corrupt("history prefix is outside the window set")
	}
	cached := p.prefixes[length]
	if length == 0 || !cached.isEmpty() {
		return cached, nil
	}
	words := prefixWords{
		feedIndexes:  p.feedIndexes,
		feedToWindow: p.feedToWindow,
		rank:         p.rank,
		prefix:       uint32(length),
		check:        p.check,
	}
	interned, err := store.internMembership(&words)
	if err != nil {
		return membershipHandle{}, err
	}
	prefix := handleFromInterned(interned)
	p.prefixes[length] = prefix
	return prefix, nil
}

// matchesPrefix reports whether the decoded old bitmap already equals
// the current prefix bitmap (Rust HistoryPolicy::matches_prefix).
func (p *historyPolicy) matchesPrefix() (bool, error) {
	for window, before := range p.before {
		if window&4095 == 4095 {
			if err := p.check(); err != nil {
				return false, err
			}
		}
		if (before != 0) != (p.rank[window] < uint32(p.currentPrefix)) {
			return false, nil
		}
	}
	return true, nil
}

// finishReport balances and assembles the projection report (Rust
// HistoryPolicy::finish_report).
func (p *historyPolicy) finishReport(sourceRangeCount uint64, sourceAddresses format.Cardinality129, createdFeedCount uint64) (*historyProjectionReport, error) {
	changed := createdFeedCount != 0
	for work := range p.reports {
		if work&4095 == 4095 {
			if err := p.check(); err != nil {
				return nil, err
			}
		}
		if err := requireBalancedHistoryReport(&p.reports[work]); err != nil {
			return nil, err
		}
		changed = changed ||
			p.reports[work].addedAddresses != format.CardinalityZero() ||
			p.reports[work].removedAddresses != format.CardinalityZero()
	}
	if err := requireBalancedHistoryReport(&p.aggregate); err != nil {
		return nil, err
	}
	aggregate := p.aggregate
	change := logicalNoChange
	if changed {
		change = logicalChanged
	}
	return &historyProjectionReport{
		logicalChange:       change,
		sourceRangeCount:    sourceRangeCount,
		sourceAddresses:     sourceAddresses,
		createdFeedCount:    createdFeedCount,
		beforeIntervalCount: aggregate.beforeIntervalCount,
		afterIntervalCount:  aggregate.afterIntervalCount,
		beforeAddresses:     aggregate.beforeAddresses,
		afterAddresses:      aggregate.afterAddresses,
		unchangedAddresses:  aggregate.unchangedAddresses,
		addedAddresses:      aggregate.addedAddresses,
		removedAddresses:    aggregate.removedAddresses,
		windows:             p.reports,
	}, nil
}

// prefixWords is one caller-owned prefix bitmap source over the ranked
// feed indexes (Rust PrefixWords: reads only caller-owned output words).
type prefixWords struct {
	feedIndexes  []uint32
	feedToWindow []uint32
	rank         []uint32
	prefix       uint32
	check        func() error
}

// WordCount returns the canonical bitmap word count (Rust
// PrefixWords::word_count: the last feed index's word, computed at
// construction in the Rust type; the Go type fills it at plan time).
func (w *prefixWords) WordCount() uint32 {
	wordCount := uint32(0)
	for position := len(w.feedIndexes) - 1; position >= 0; position-- {
		window := w.feedToWindow[position]
		if w.rank[window] < w.prefix {
			wordCount = w.feedIndexes[position]/64 + 1
			break
		}
	}
	if wordCount == 0 {
		panic("nonempty history prefix has no feeds")
	}
	return wordCount
}

// ReadWords writes the selected prefix bits into caller-owned output
// (Rust PrefixWords::read_words).
func (w *prefixWords) ReadWords(start uint32, output []uint64) error {
	for index := range output {
		output[index] = 0
	}
	end := start
	if len(output) > 0 {
		next, err := checkedAdd(uint64(start), uint64(len(output)), "history prefix word range")
		if err != nil {
			return err
		}
		if next > math.MaxUint32 {
			return overflow("history prefix word range")
		}
		end = uint32(next)
	}
	firstIndex := start
	if firstIndex > math.MaxUint32/64 {
		return overflow("history prefix bit range")
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
		if work&4095 == 4095 {
			if err := w.check(); err != nil {
				return err
			}
		}
		window := w.feedToWindow[position]
		if w.rank[window] < w.prefix {
			output[(word - start)] |= uint64(1) << (index % 64)
		}
		position++
		work++
	}
	return nil
}

// observeHistoryWindow folds one segment into one window report (Rust
// observe).
func observeHistoryWindow(report *historyWindowReport, run *historyRun, family uint8, from, to tree.Key, count format.Cardinality129, before, after bool) error {
	adjacent := false
	if run.lastTo != nil {
		next, ok := nextKey(family, *run.lastTo)
		adjacent = ok && next.Equal(from)
	}
	if before {
		next, err := addHistoryCount(report.beforeAddresses, count)
		if err != nil {
			return err
		}
		report.beforeAddresses = next
		if !adjacent || !run.before {
			next, err := incrementHistoryCount(report.beforeIntervalCount)
			if err != nil {
				return err
			}
			report.beforeIntervalCount = next
		}
	}
	if after {
		next, err := addHistoryCount(report.afterAddresses, count)
		if err != nil {
			return err
		}
		report.afterAddresses = next
		if !adjacent || !run.after {
			next, err := incrementHistoryCount(report.afterIntervalCount)
			if err != nil {
				return err
			}
			report.afterIntervalCount = next
		}
	}
	switch {
	case before && after:
		next, err := addHistoryCount(report.unchangedAddresses, count)
		if err != nil {
			return err
		}
		report.unchangedAddresses = next
	case before && !after:
		next, err := addHistoryCount(report.removedAddresses, count)
		if err != nil {
			return err
		}
		report.removedAddresses = next
	case !before && after:
		next, err := addHistoryCount(report.addedAddresses, count)
		if err != nil {
			return err
		}
		report.addedAddresses = next
	}
	toCopy := to
	run.lastTo = &toCopy
	run.before = before
	run.after = after
	return nil
}

// requireBalancedHistoryReport verifies the before/after accounting of
// one window (Rust require_balanced).
func requireBalancedHistoryReport(report *historyWindowReport) error {
	before, err := addHistoryCount(report.unchangedAddresses, report.removedAddresses)
	if err != nil {
		return err
	}
	after, err := addHistoryCount(report.unchangedAddresses, report.addedAddresses)
	if err != nil {
		return err
	}
	zero := format.CardinalityZero()
	if before != report.beforeAddresses || after != report.afterAddresses ||
		(report.beforeIntervalCount == 0) != (report.beforeAddresses == zero) ||
		(report.afterIntervalCount == 0) != (report.afterAddresses == zero) {
		return corrupt("history projection counters do not balance")
	}
	return nil
}

// emptyHistoryReport builds the zero report of one window (Rust
// empty_report).
func emptyHistoryReport(window historyWindow, created bool) historyWindowReport {
	return historyWindowReport{
		feedName: window.feedName,
		cutoff:   window.cutoff,
		created:  created,
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
		size, err := format.IPv4Inclusive(uint32(from.Hi), uint32(to.Hi))
		if err != nil {
			return format.CardinalityZero(), overflow("IPv4 interval cardinality")
		}
		return size, nil
	}
	size, err := format.IPv6Inclusive(from.Hi, from.Lo, to.Hi, to.Lo)
	if err != nil {
		return format.CardinalityZero(), overflow("IPv6 interval cardinality")
	}
	return size, nil
}

// sameOptionalUint32 compares two Rust-style Option<u32> values (both
// present with equal values, or both absent).
func sameOptionalUint32(left, right *uint32) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
