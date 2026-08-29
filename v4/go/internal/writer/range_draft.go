// DraftStore range edit entry points (Rust draft_store.rs assign/clear +
// range_mutation::RangeStore for DraftStore).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

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
func (s *DraftStore) addPrivateConstantRange4(from, to key4, value uint32, input *unionInput[key4]) error {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := pushPrivateUntracked(ctx, from, to, value, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// addPrivateConstantRange6 is the IPv6 form of addPrivateConstantRange4.
func (s *DraftStore) addPrivateConstantRange6(from, to key6, value uint32, input *unionInput[key6]) error {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := pushPrivateUntracked(ctx, from, to, value, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// finishPrivateConstantRanges4 seals one untracked constant-range input
// (Rust DraftStore::finish_private_constant_ranges).
func (s *DraftStore) finishPrivateConstantRanges4(input *unionInput[key4]) error {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := finishInputUntracked(ctx, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// finishPrivateConstantRanges6 is the IPv6 form of
// finishPrivateConstantRanges4.
func (s *DraftStore) finishPrivateConstantRanges6(input *unionInput[key6]) error {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := finishInputUntracked(ctx, input)
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return nil
}

// AssignV4 assigns one inclusive IPv4 range on the draft (Rust
// DraftStore::assign_v4; the draft-private gap path when the tree is
// private).
func (s *DraftStore) AssignV4(from, to uint32, value uint32) (bool, error) {
	return s.assign4(key4(from), key4(to), value)
}

// AssignV6 assigns one inclusive IPv6 range on the draft (Rust
// DraftStore::assign_v6).
func (s *DraftStore) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	return s.assign6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, value)
}

// ClearV4 clears one inclusive IPv4 range on the draft (Rust
// DraftStore::clear_v4).
func (s *DraftStore) ClearV4(from, to uint32) (bool, error) {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeClear(ctx, key4(from), key4(to))
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// ClearV6 clears one inclusive IPv6 range on the draft (Rust
// DraftStore::clear_v6).
func (s *DraftStore) ClearV6(fromHi, fromLo, toHi, toLo uint64) (bool, error) {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeClear(ctx, key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo})
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// assign4 assigns one inclusive IPv4 range on the draft (Rust
// DraftStore::assign over Ipv4Key; the per-family internal form used by
// the structured arms). A draft-private range tree takes the
// single-descent private gap path exactly like the Rust assign_private;
// a shared committed tree uses the general replace.
func (s *DraftStore) assign4(from, to key4, value uint32) (bool, error) {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	var changed bool
	var err error
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

// assign6 is the IPv6 form of assign4.
func (s *DraftStore) assign6(from, to key6, value uint32) (bool, error) {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	var changed bool
	var err error
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

// clear4 clears one inclusive IPv4 range on the draft (Rust
// DraftStore::clear over Ipv4Key).
func (s *DraftStore) clear4(from, to key4) (bool, error) {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeClear(ctx, from, to)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// clear6 is the IPv6 form of clear4.
func (s *DraftStore) clear6(from, to key6) (bool, error) {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeClear(ctx, from, to)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// assignInput4 assigns one private IPv4 range through the leaf-locator
// input (Rust DraftStore::assign_input): the range tree must be
// draft-private, because a shared committed tree cannot be assigned
// through the locator cache.
func (s *DraftStore) assignInput4(from, to key4, value uint32, input *privateInput[key4]) (bool, error) {
	if !s.draft.rangeTreePrivate {
		return false, corrupt("private assignment input has a shared range tree")
	}
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeAssignPrivateInput(ctx, from, to, value, input)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// assignInput6 is the IPv6 form of assignInput4.
func (s *DraftStore) assignInput6(from, to key6, value uint32, input *privateInput[key6]) (bool, error) {
	if !s.draft.rangeTreePrivate {
		return false, corrupt("private assignment input has a shared range tree")
	}
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	changed, err := rangeAssignPrivateInput(ctx, from, to, value, input)
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}
