// Exact named-feed policy over the authoritative ordered range merge
// (Rust draft_store/feed_merge.rs). The empty-map feed trio builds the
// workflow's constant-value ranges, the private coverage tree is the
// untracked input of one merge over the committed base, and the
// feed policy unions or differences the member bitmap with the base
// membership, projecting exact before/after address and interval counts.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// feedMerge is the per-feed outcome of one coverage merge (Rust
// FeedMerge).
type feedMerge struct {
	inputIntervals uint64
	inputAddresses format.Cardinality129
	comparison     scannedComparison
}

// beginEmptyMapFeed starts the empty-map workflow of a value-free base:
// the base and draft range trees must both be empty, and the range tree
// becomes draft-private (Rust DraftStore::begin_empty_map_feed).
func (s *DraftStore) beginEmptyMapFeed() error {
	if s.draft.base.RangeRoot != 0 || s.draft.base.RangeRecordCount != 0 ||
		s.draft.meta.RangeRoot != 0 || s.draft.meta.RangeRecordCount != 0 {
		return corrupt("empty-map feed range tree is not empty")
	}
	s.draft.rangeTreePrivate = true
	return nil
}

// addEmptyMapFeedRange pushes one constant member-valued range into the
// private draft tree (Rust DraftStore::add_empty_map_feed_range).
func (s *DraftStore) addEmptyMapFeedRange(from, to tree.Key, member membershipHandle, input *unionInput) error {
	memberID, _ := member.stored()
	return s.addPrivateConstantRange(from, to, memberID, input)
}

// finishEmptyMapFeedRanges seals the constant ranges, accounts the
// member refcount for every record of the sealed tree, and returns the
// ordered-prefix address count when one was built (Rust
// DraftStore::finish_empty_map_feed_ranges).
func (s *DraftStore) finishEmptyMapFeedRanges(member membershipHandle, input *unionInput) (format.Cardinality129, bool, error) {
	if err := s.finishPrivateConstantRanges(input); err != nil {
		return format.Cardinality129{}, false, err
	}
	ordered, hasOrdered := input.orderedAddresses()
	value, _ := member.stored()
	count := int64(s.draft.meta.RangeRecordCount)
	if count < 0 {
		return format.Cardinality129{}, false, overflow("membership range refcount")
	}
	if count != 0 {
		if err := s.trackMembershipRefcount(value, count); err != nil {
			return format.Cardinality129{}, false, err
		}
	}
	return ordered, hasOrdered, nil
}

// addFeedCoverage pushes one value-1 range into the private workflow
// coverage tree, untracked (Rust DraftStore::add_feed_coverage: the
// coverage tree is the input of the upcoming merge, never part of the
// published range tree).
func (s *DraftStore) addFeedCoverage(from, to tree.Key, input *unionInput) error {
	family, err := s.rangeFamily()
	if err != nil {
		return err
	}
	root := s.draft.workflowRangeRoot
	count := s.draft.workflowRangeCount
	ctx := &rangeCtx{family: family, store: s, root: &root, count: &count}
	if _, err := pushPrivateUntracked(ctx, from, to, 1, input); err != nil {
		return err
	}
	s.draft.workflowRangeRoot = root
	s.draft.workflowRangeCount = count
	return nil
}

// finishFeedCoverage seals the pending coverage input (Rust
// DraftStore::finish_feed_coverage).
func (s *DraftStore) finishFeedCoverage(input *unionInput) error {
	family, err := s.rangeFamily()
	if err != nil {
		return err
	}
	root := s.draft.workflowRangeRoot
	count := s.draft.workflowRangeCount
	ctx := &rangeCtx{family: family, store: s, root: &root, count: &count}
	if _, err := finishInputUntracked(ctx, input); err != nil {
		return err
	}
	s.draft.workflowRangeRoot = root
	s.draft.workflowRangeCount = count
	return nil
}

// mergeFeed merges the workflow coverage tree over the committed base
// generation, applying the member bitmap through the feed policy (Rust
// DraftStore::merge_feed). A created feed with an empty coverage tree
// returns the exact no-change result without scanning anything.
func (s *DraftStore) mergeFeed(base format.Meta, member membershipHandle, create bool, check func() error) (feedMerge, error) {
	if create && s.draft.workflowRangeRoot == 0 {
		return emptyFeedMerge(), nil
	}
	return s.mergeFeedFamily(base, member, check)
}

// mergeFeedFamily runs the coverage merge for the base address family
// (Rust DraftStore::merge_feed_family): the coverage meta is the draft
// meta with the workflow range roots substituted, and the policy output
// is the scanned before/after comparison.
func (s *DraftStore) mergeFeedFamily(base format.Meta, member membershipHandle, check func() error) (feedMerge, error) {
	coverageMeta := s.draft.meta
	coverageMeta.RangeRoot = s.draft.workflowRangeRoot
	coverageMeta.RangeRecordCount = s.draft.workflowRangeCount
	var codec rangeFamily
	if base.AddressFamily == format.AddressFamilyIPv4 {
		codec = rangeCodec4{}
	} else {
		codec = rangeCodec6{}
	}
	policy := newFeedPolicy(member, base.AddressFamily)
	inputIntervals, finished, err := mergeCoverage[scannedComparison](s, coverageMeta, base, codec, &policy, check, "feed input intervals")
	if err != nil {
		return feedMerge{}, err
	}
	s.draft.workflowRangeRoot = 0
	s.draft.workflowRangeCount = 0
	return feedMerge{
		inputIntervals: inputIntervals,
		inputAddresses: finished.comparison.after,
		comparison:     finished,
	}, nil
}

// emptyFeedMerge is the exact no-change outcome of an empty workflow
// (Rust empty_result).
func emptyFeedMerge() feedMerge {
	return feedMerge{comparison: scannedComparison{comparison: comparison{}}}
}

// cachedMembership is one cached transform outcome keyed on the old
// membership and the coverage presence (Rust CachedMembership). The new
// bitmap is the Option<u32> combine outcome, carried as optionalValue.
type cachedMembership struct {
	old     uint32
	covered bool
	new     optionalValue
}

// feedPolicy is the per-feed merge policy (Rust FeedPolicy): the member
// bitmap is unioned into covered segments and differenced out of
// uncovered segments, with one cache slot and the running projection.
type feedPolicy struct {
	member     membershipHandle
	cached     cachedMembership
	hasCached  bool
	projection feedProjection
	family     uint8
}

// newFeedPolicy starts one feed policy over the member handle and the
// merge address family (Rust FeedPolicy::new; the projection fixes its
// family at plan time like HistoryPolicy<K>).
func newFeedPolicy(member membershipHandle, family uint8) feedPolicy {
	return feedPolicy{member: member, family: family, projection: feedProjection{family: family}}
}

// transform returns the merged membership of one segment (Rust
// FeedPolicy::transform): no old bitmap adopts the member when covered
// and stays absent otherwise; an old bitmap combines with the member
// through the cached union/difference.
func (p *feedPolicy) transform(store *DraftStore, old, incoming optionalValue) (optionalValue, error) {
	covered := incoming.present
	memberID, memberWords := p.member.stored()
	if !old.present {
		if covered {
			return someValue(memberID), nil
		}
		return noneValue(), nil
	}
	if p.hasCached && p.cached.old == old.value && p.cached.covered == covered {
		return p.cached.new, nil
	}
	operation := membershipUnion
	if !covered {
		operation = membershipDifference
	}
	new, present, err := store.combineMemberships(old.value, memberID, memberWords, operation)
	if err != nil {
		return optionalValue{}, err
	}
	p.hasCached = true
	p.cached = cachedMembership{old: old.value, covered: covered, new: optionalValue{value: new, present: present}}
	return p.cached.new, nil
}

// observe projects one transformed segment into the before/after counts
// (Rust FeedPolicy::observe: the before classification compares the new
// bitmap with the old one around the coverage).
func (p *feedPolicy) observe(from, to tree.Key, old, incoming, new optionalValue) error {
	after := incoming.present
	var before bool
	if after {
		before = sameOptional(new, old)
	} else {
		before = !sameOptional(new, old)
	}
	return p.projection.observe(from, to, before, after)
}

// finish balances and returns the projection (Rust FeedPolicy::finish
// over Projection::finish).
func (p *feedPolicy) finish() (scannedComparison, error) {
	return p.projection.finish()
}

// preserveWithoutInput is false like every Rust policy except the
// explicit preserving ones; the feed policies always consume their
// coverage input (Rust Policy::PRESERVE_WITHOUT_INPUT default).
func (p *feedPolicy) preserveWithoutInput() bool { return false }

// feedProjection is the scanned before/after projection of one feed
// policy (Rust Projection<K>): the exact comparison counts plus the
// interval counters, with the adjacency state embedded by value so one
// observe never allocates.
type feedProjection struct {
	result          comparison
	beforeIntervals uint64
	afterIntervals  uint64
	lastTo          tree.Key
	hasLastTo       bool
	lastBefore      bool
	lastAfter       bool
	family          uint8
}

// observe folds one transformed segment into the projection (Rust
// Projection::observe): adjacent same-class segments share one interval
// count, and every address count is exact.
func (p *feedProjection) observe(from, to tree.Key, before, after bool) error {
	adjacent := false
	if p.hasLastTo {
		next, ok := nextKey(p.family, p.lastTo)
		adjacent = ok && next.Equal(from)
	}
	count, err := familyInclusiveCardinality(p.family, from, to)
	if err != nil {
		return err
	}
	if before {
		p.result.before, err = p.result.before.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
		if !adjacent || !p.lastBefore {
			p.beforeIntervals, err = incrementFeedIntervals(p.beforeIntervals, "before feed intervals")
			if err != nil {
				return err
			}
		}
	}
	if after {
		p.result.after, err = p.result.after.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
		if !adjacent || !p.lastAfter {
			p.afterIntervals, err = incrementFeedIntervals(p.afterIntervals, "after feed intervals")
			if err != nil {
				return err
			}
		}
	}
	switch {
	case before && after:
		p.result.unchanged, err = p.result.unchanged.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
	case before && !after:
		p.result.removed, err = p.result.removed.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
	case !before && after:
		p.result.added, err = p.result.added.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
	}
	p.lastTo = to
	p.hasLastTo = true
	p.lastBefore = before
	p.lastAfter = after
	return nil
}

// finish balances the six counts and returns the scanned comparison
// (Rust Projection::finish): the before/after totals must equal the
// unchanged/removed and unchanged/added sums, the changed class must be
// empty, and the interval counter must agree with the address count
// emptiness.
func (p *feedProjection) finish() (scannedComparison, error) {
	unchangedRemoved, err := p.result.unchanged.Add(p.result.removed)
	if err != nil {
		return scannedComparison{}, overflow("ordered merge address count")
	}
	unchangedAdded, err := p.result.unchanged.Add(p.result.added)
	if err != nil {
		return scannedComparison{}, overflow("ordered merge address count")
	}
	if unchangedRemoved.Compare(p.result.before) != 0 ||
		unchangedAdded.Compare(p.result.after) != 0 ||
		p.result.changed.Compare(format.CardinalityZero()) != 0 ||
		(p.afterIntervals == 0) != (p.result.after.Compare(format.CardinalityZero()) == 0) {
		return scannedComparison{}, corrupt("feed merge counters do not balance")
	}
	return scannedComparison{
		comparison:      p.result,
		beforeIntervals: p.beforeIntervals,
		afterIntervals:  p.afterIntervals,
	}, nil
}

// incrementFeedIntervals adds one interval count with the Rust overflow
// class (Rust increment).
func incrementFeedIntervals(value uint64, context string) (uint64, error) {
	return checkedAdd(value, 1, context)
}
