// Name-translated membership import over the ordered range merge (Rust
// draft_store/import_merge.rs): the import policy unions every incoming
// translated membership into the preserved destination generation with
// one cached combine slot, observes the exact before/after address
// classification, and finishes into the workflow Comparison.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// translatedMembership is one destination dictionary membership plus
// its stored word count (Rust TranslatedMembership).
type translatedMembership struct {
	id    uint32
	words uint32
}

func newTranslatedMembership(id, words uint32) translatedMembership {
	return translatedMembership{id: id, words: words}
}

// importCachedUnion is one cached import transform outcome keyed on the
// old and supplied membership ids (Rust CachedUnion).
type importCachedUnion struct {
	old      uint32
	supplied uint32
	new      optionalValue
}

// importPolicy is the per-import merge policy (Rust ImportPolicy): a
// missing incoming segment preserves the old value, a missing old value
// adopts the incoming membership, and an overlap unions both with one
// cached combine slot. The merge record carries the already-translated
// membership exactly like Rust Incoming<TranslatedMembership>; no
// translation lookup happens inside the merge.
type importPolicy[K any] struct {
	comparison Comparison
	cached     importCachedUnion
	hasCached  bool
	family     uint8
	codec      rangeFamily[K]
}

func newImportPolicy[K any](family uint8, codec rangeFamily[K]) importPolicy[K] {
	return importPolicy[K]{family: family, codec: codec}
}

// preserveWithoutInput preserves the untouched base generation (Rust
// ImportPolicy PRESERVE_WITHOUT_INPUT).
func (p *importPolicy[K]) preserveWithoutInput() bool { return true }

// transform returns the merged membership of one segment (Rust
// ImportPolicy::transform): no incoming value keeps the old bitmap, no
// old bitmap adopts the incoming one, and an overlap unions the two
// through the cached combine.
func (p *importPolicy[K]) transform(store *DraftStore, old optionalValue, incoming incomingValue[translatedMembership]) (optionalValue, error) {
	if !incoming.present {
		return old, nil
	}
	if !old.present {
		return someValue(incoming.value.id), nil
	}
	if p.hasCached && p.cached.old == old.value && p.cached.supplied == incoming.value.id {
		return p.cached.new, nil
	}
	new, present, err := store.combineMemberships(old.value, incoming.value.id, incoming.value.words, MembershipUnion)
	if err != nil {
		return optionalValue{}, err
	}
	p.hasCached = true
	p.cached = importCachedUnion{old: old.value, supplied: incoming.value.id, new: optionalValue{value: new, present: present}}
	return p.cached.new, nil
}

// observe folds one segment into the exact before/after classification
// (Rust ImportPolicy::observe over MapComparison).
func (p *importPolicy[K]) observe(from, to K, old optionalValue, _incoming incomingValue[translatedMembership], new optionalValue) error {
	count, err := familyInclusiveCardinalityOf(p.codec, from, to)
	if err != nil {
		return err
	}
	if old.present {
		p.comparison.Before, err = p.comparison.Before.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
	}
	if new.present {
		p.comparison.After, err = p.comparison.After.Add(count)
		if err != nil {
			return overflow("ordered merge address count")
		}
	}
	switch {
	case old.present && new.present && old.value == new.value:
		p.comparison.Unchanged, err = p.comparison.Unchanged.Add(count)
	case old.present && new.present:
		p.comparison.Changed, err = p.comparison.Changed.Add(count)
	case old.present:
		p.comparison.Removed, err = p.comparison.Removed.Add(count)
	case new.present:
		p.comparison.Added, err = p.comparison.Added.Add(count)
	}
	if err != nil {
		return overflow("ordered merge address count")
	}
	return nil
}

// finish returns the policy (Rust ImportPolicy::finish over
// MapComparison; the classification is exposed by the merge finish).
func (p *importPolicy[K]) finish() (importPolicy[K], error) {
	return *p, nil
}

// importComparisonBalance balances the classification like the Rust
// MapComparison::finish: the before and after totals recomputed from
// the classes must equal the directly counted totals.
func importComparisonBalance(c Comparison) (Comparison, error) {
	before, err := c.Unchanged.Add(c.Changed)
	if err != nil {
		return Comparison{}, overflow("ordered merge address count")
	}
	before, err = before.Add(c.Removed)
	if err != nil {
		return Comparison{}, overflow("ordered merge address count")
	}
	after, err := c.Unchanged.Add(c.Changed)
	if err != nil {
		return Comparison{}, overflow("ordered merge address count")
	}
	after, err = after.Add(c.Added)
	if err != nil {
		return Comparison{}, overflow("ordered merge address count")
	}
	if before.Compare(c.Before) != 0 || after.Compare(c.After) != 0 {
		return Comparison{}, corrupt("ordered merge comparison does not balance")
	}
	return c, nil
}

// ImportMerge is one running import merge over the committed
// destination (Rust ImportMerge). The facade holds exactly the family
// side selected by the destination address family; the edit arms route
// the per-family methods to that side.
type ImportMerge struct {
	inner4 *orderedMerge[translatedMembership, importPolicy[key4], key4]
	inner6 *orderedMerge[translatedMembership, importPolicy[key6], key6]
}

// beginImportMerge opens the import merge over the committed
// destination generation (Rust ImportMerge::new).
func beginImportMerge(store *DraftStore, base format.Meta, check func() error) (*ImportMerge, error) {
	if base.AddressFamily == format.AddressFamilyIPv4 {
		policy := newImportPolicy(base.AddressFamily, rangeCodec4{})
		inner, err := newOrderedMerge[translatedMembership, importPolicy[key4], key4](store, base, rangeCodec4{}, &policy, check)
		if err != nil {
			return nil, err
		}
		return &ImportMerge{inner4: inner}, nil
	}
	policy := newImportPolicy(base.AddressFamily, rangeCodec6{})
	inner, err := newOrderedMerge[translatedMembership, importPolicy[key6], key6](store, base, rangeCodec6{}, &policy, check)
	if err != nil {
		return nil, err
	}
	return &ImportMerge{inner6: inner}, nil
}

// push4 streams one translated IPv4 membership interval into the merge
// (Rust ImportMerge::push over TranslatedMembership; the words arrive
// with the record, so the policy never re-resolves a translation).
func (m *ImportMerge) push4(store *DraftStore, from, to key4, membership translatedMembership, check func() error) error {
	return m.inner4.push(store, incomingRange[translatedMembership, key4]{from: from, to: to, value: membership}, check)
}

// push6 is the IPv6 form of push4.
func (m *ImportMerge) push6(store *DraftStore, from, to key6, membership translatedMembership, check func() error) error {
	return m.inner6.push(store, incomingRange[translatedMembership, key6]{from: from, to: to, value: membership}, check)
}

// finish4 completes the IPv4 merge and returns the classification (Rust
// ImportMerge::finish over ImportPolicy::finish).
func (m *ImportMerge) finish4(store *DraftStore, check func() error) (Comparison, error) {
	policy, err := m.inner4.finish(store, check)
	if err != nil {
		return Comparison{}, err
	}
	return importComparisonBalance(policy.comparison)
}

// finish6 is the IPv6 form of finish4.
func (m *ImportMerge) finish6(store *DraftStore, check func() error) (Comparison, error) {
	policy, err := m.inner6.finish(store, check)
	if err != nil {
		return Comparison{}, err
	}
	return importComparisonBalance(policy.comparison)
}
