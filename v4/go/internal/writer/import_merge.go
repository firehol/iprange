// Name-translated membership import over the ordered range merge (Rust
// draft_store/import_merge.rs): the import policy unions every incoming
// translated membership into the preserved destination generation with
// one cached combine slot, observes the exact before/after address
// classification, and finishes into the workflow Comparison.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
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
type importPolicy struct {
	comparison Comparison
	cached     importCachedUnion
	hasCached  bool
	family     uint8
}

func newImportPolicy(family uint8) importPolicy {
	return importPolicy{family: family}
}

// preserveWithoutInput preserves the untouched base generation (Rust
// ImportPolicy PRESERVE_WITHOUT_INPUT).
func (p *importPolicy) preserveWithoutInput() bool { return true }

// transform returns the merged membership of one segment (Rust
// ImportPolicy::transform): no incoming value keeps the old bitmap, no
// old bitmap adopts the incoming one, and an overlap unions the two
// through the cached combine.
func (p *importPolicy) transform(store *DraftStore, old optionalValue, incoming incomingValue[translatedMembership]) (optionalValue, error) {
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
func (p *importPolicy) observe(from, to tree.Key, old optionalValue, _incoming incomingValue[translatedMembership], new optionalValue) error {
	count, err := familyInclusiveCardinality(p.family, from, to)
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
func (p *importPolicy) finish() (importPolicy, error) {
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
// destination (Rust ImportMerge). The root facade holds the opaque
// merge handle between WordEdit arms.
type ImportMerge struct {
	inner *orderedMerge[translatedMembership, importPolicy]
}

// beginImportMerge opens the import merge over the committed
// destination generation (Rust ImportMerge::new).
func beginImportMerge(store *DraftStore, base format.Meta, check func() error) (*ImportMerge, error) {
	var codec rangeFamily
	if base.AddressFamily == format.AddressFamilyIPv4 {
		codec = rangeCodec4{}
	} else {
		codec = rangeCodec6{}
	}
	policy := newImportPolicy(base.AddressFamily)
	inner, err := newOrderedMerge[translatedMembership, importPolicy](store, base, codec, &policy, check)
	if err != nil {
		return nil, err
	}
	return &ImportMerge{inner: inner}, nil
}

// push streams one translated membership interval into the merge (Rust
// ImportMerge::push over TranslatedMembership; the words arrive with
// the record, so the policy never re-resolves a translation).
func (m *ImportMerge) push(store *DraftStore, from, to tree.Key, membership translatedMembership, check func() error) error {
	return m.inner.push(store, incomingRange[translatedMembership]{from: from, to: to, value: membership}, check)
}

// finish completes the merge and returns the classification (Rust
// ImportMerge::finish over ImportPolicy::finish).
func (m *ImportMerge) finish(store *DraftStore, check func() error) (Comparison, error) {
	policy, err := m.inner.finish(store, check)
	if err != nil {
		return Comparison{}, err
	}
	return importComparisonBalance(policy.comparison)
}
