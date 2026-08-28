// DraftStore range edit entry points (Rust draft_store.rs assign/clear +
// range_mutation::RangeStore for DraftStore).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// rangeFamily returns the per-family range codec for the draft (Rust
// draft_store.rs RangeCodec selection). The range root and record count
// are snapshotted into locals by assign/clear and committed to the draft
// meta only after the edit succeeds, exactly like Rust draft_store.rs
// assign/clear (locals written back after the state machine returns).
func (s *DraftStore) rangeFamily() (rangeFamily, error) {
	switch s.draft.meta.AddressFamily {
	case format.AddressFamilyIPv4:
		return rangeCodec4{}, nil
	case format.AddressFamilyIPv6:
		return rangeCodec6{}, nil
	default:
		return nil, corrupt("draft has no supported address family")
	}
}

func (s *DraftStore) assign(from, to tree.Key, value uint32) (bool, error) {
	family, err := s.rangeFamily()
	if err != nil {
		return false, err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	var changed bool
	if s.draft.rangeTreePrivate {
		changed, err = rangeAssignPrivate(ctx, from, to, value)
	} else {
		changed, err = rangeAssign(ctx, from, to, value)
	}
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

func (s *DraftStore) clear(from, to tree.Key) (bool, error) {
	family, err := s.rangeFamily()
	if err != nil {
		return false, err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeClear(ctx, from, to)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// AssignV4 assigns one IPv4 range (Rust DraftStore::assign_v4).
func (s *DraftStore) AssignV4(from, to uint32, value uint32) (bool, error) {
	return s.assign(tree.KeyOfU32(from), tree.KeyOfU32(to), value)
}

// AssignV6 assigns one IPv6 range (Rust DraftStore::assign_v6).
func (s *DraftStore) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	return s.assign(tree.KeyOfU128(fromHi, fromLo), tree.KeyOfU128(toHi, toLo), value)
}

// ClearV4 clears one IPv4 range (Rust DraftStore::clear_v4).
func (s *DraftStore) ClearV4(from, to uint32) (bool, error) {
	return s.clear(tree.KeyOfU32(from), tree.KeyOfU32(to))
}

// ClearV6 clears one IPv6 range (Rust DraftStore::clear_v6).
func (s *DraftStore) ClearV6(fromHi, fromLo, toHi, toLo uint64) (bool, error) {
	return s.clear(tree.KeyOfU128(fromHi, fromLo), tree.KeyOfU128(toHi, toLo))
}

// RangeRecordAdded accounts one range record with value (Rust
// draft_store/membership.rs range_record_added). Direct databases carry no
// per-value accounting; membership refcounts feed the draft membership
// delta state and structured refcounts the structure delta state.
func (s *DraftStore) RangeRecordAdded(value uint32) error {
	switch s.draft.meta.ValueKind {
	case format.ValueKindMembership:
		return s.trackMembershipRefcount(value, 1)
	case format.ValueKindStructured:
		return s.trackStructureRefcount(value, 1)
	default:
		return nil
	}
}

// RangeRecordRemoved accounts one removed range record (Rust
// draft_store/membership.rs range_record_removed).
func (s *DraftStore) RangeRecordRemoved(value uint32) error {
	switch s.draft.meta.ValueKind {
	case format.ValueKindMembership:
		return s.trackMembershipRefcount(value, -1)
	case format.ValueKindStructured:
		return s.trackStructureRefcount(value, -1)
	default:
		return nil
	}
}

// addPrivateConstantRange pushes one untracked constant range into the
// draft range tree (Rust DraftStore::add_private_constant_range). The
// range is internal to one exact workflow (empty-map feeds, timestamp
// refreshes); the value accounting is untracked and the changed flag
// still marks the draft.
func (s *DraftStore) addPrivateConstantRange(from, to tree.Key, value uint32, input *UnionInput) error {
	family, err := s.rangeFamily()
	if err != nil {
		return err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := pushPrivateUntracked(ctx, from, to, value, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// finishPrivateConstantRanges seals one untracked constant-range input
// (Rust DraftStore::finish_private_constant_ranges).
func (s *DraftStore) finishPrivateConstantRanges(input *UnionInput) error {
	family, err := s.rangeFamily()
	if err != nil {
		return err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := finishInputUntracked(ctx, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// assignInput assigns one private range through the leaf-locator input
// (Rust DraftStore::assign_input): the range tree must be draft-private,
// because a shared committed tree cannot be assigned through the locator
// cache.
func (s *DraftStore) assignInput(from, to tree.Key, value uint32, input *privateInput) (bool, error) {
	if !s.draft.rangeTreePrivate {
		return false, corrupt("private assignment input has a shared range tree")
	}
	family, err := s.rangeFamily()
	if err != nil {
		return false, err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeAssignPrivateInput(ctx, from, to, value, input)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}
