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
	root := s.draft.meta.RangeRoot
	count := s.draft.meta.RangeRecordCount
	ctx := &rangeCtx{family: family, store: s, root: &root, count: &count}
	var changed bool
	if s.draft.rangeTreePrivate {
		changed, err = rangeAssignPrivate(ctx, from, to, value)
	} else {
		changed, err = rangeAssign(ctx, from, to, value)
	}
	if err != nil {
		return false, err
	}
	s.draft.meta.RangeRoot = root
	s.draft.meta.RangeRecordCount = count
	s.draft.changed = s.draft.changed || changed
	return changed, nil
}

func (s *DraftStore) clear(from, to tree.Key) (bool, error) {
	family, err := s.rangeFamily()
	if err != nil {
		return false, err
	}
	root := s.draft.meta.RangeRoot
	count := s.draft.meta.RangeRecordCount
	ctx := &rangeCtx{family: family, store: s, root: &root, count: &count}
	changed, err := rangeClear(ctx, from, to)
	if err != nil {
		return false, err
	}
	s.draft.meta.RangeRoot = root
	s.draft.meta.RangeRecordCount = count
	s.draft.changed = s.draft.changed || changed
	return changed, nil
}

// AssignV4 assigns one IPv4 range (Rust DraftStore::assign_v4).
func (s *DraftStore) AssignV4(from, to uint32, value uint32) (bool, error) {
	return s.assign(tree.Key{Hi: uint64(from)}, tree.Key{Hi: uint64(to)}, value)
}

// AssignV6 assigns one IPv6 range (Rust DraftStore::assign_v6).
func (s *DraftStore) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	return s.assign(tree.Key{Hi: fromHi, Lo: fromLo}, tree.Key{Hi: toHi, Lo: toLo}, value)
}

// ClearV4 clears one IPv4 range (Rust DraftStore::clear_v4).
func (s *DraftStore) ClearV4(from, to uint32) (bool, error) {
	return s.clear(tree.Key{Hi: uint64(from)}, tree.Key{Hi: uint64(to)})
}

// ClearV6 clears one IPv6 range (Rust DraftStore::clear_v6).
func (s *DraftStore) ClearV6(fromHi, fromLo, toHi, toLo uint64) (bool, error) {
	return s.clear(tree.Key{Hi: fromHi, Lo: fromLo}, tree.Key{Hi: toHi, Lo: toLo})
}

// RangeRecordAdded accounts one range record with value (Rust
// draft_store/membership.rs range_record_added). Direct databases carry no
// per-value accounting; membership and structured accounting arrive with
// their edit cores and fail closed here.
func (s *DraftStore) RangeRecordAdded(value uint32) error {
	switch s.draft.meta.ValueKind {
	case format.ValueKindMembership, format.ValueKindStructured:
		return unsupported("membership/structured range accounting is not implemented yet")
	default:
		return nil
	}
}

// RangeRecordRemoved accounts one removed range record (Rust
// draft_store/membership.rs range_record_removed).
func (s *DraftStore) RangeRecordRemoved(value uint32) error {
	switch s.draft.meta.ValueKind {
	case format.ValueKindMembership, format.ValueKindStructured:
		return unsupported("membership/structured range accounting is not implemented yet")
	default:
		return nil
	}
}
