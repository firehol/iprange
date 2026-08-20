package reader

// Ordered membership range cursors (Rust reader_core/cursor.rs
// MembershipRangeCursor parity). The membership range tree is the same
// physical range tree as the direct surface; each record's value is the
// membership dictionary ID covered by the inclusive interval. Cursors
// walk forward only (the Rust membership cursor hardcodes the forward
// direction) and require a membership database of the matching family.
// Unlike the direct cursor, visiting a record does not count
// RangeConsumed: the aggregation and join scans count physical ranges
// themselves (Rust does the same).

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// MembershipRange4 is one inclusive membership interval.
type MembershipRange4 struct {
	From, To   uint32
	Membership uint32
}

// MembershipRange6 is one inclusive membership interval.
type MembershipRange6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Membership                 uint32
}

func (r *ImmutableReader) newMembershipRangeCursor4() (*MembershipRangeCursor4, error) {
	if err := r.requireMembershipFamilyLocked(format.AddressFamilyIPv4); err != nil {
		return nil, err
	}
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(RangeForward), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &MembershipRangeCursor4{state: state}, nil
}

func (r *ImmutableReader) newMembershipRangeCursor6() (*MembershipRangeCursor6, error) {
	if err := r.requireMembershipFamilyLocked(format.AddressFamilyIPv6); err != nil {
		return nil, err
	}
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(RangeForward), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &MembershipRangeCursor6{state: state}, nil
}

// requireMembershipFamilyLocked enforces the membership value kind and
// one address family on the reader's meta (Rust
// GenerationReader::require_membership_family).
func (r *ImmutableReader) requireMembershipFamilyLocked(family uint8) error {
	if r.meta.ValueKind != format.ValueKindMembership {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "membership range cursor requires a membership database"}
	}
	if r.meta.AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "membership range cursor address family does not match"}
	}
	return nil
}

// MembershipRangeCursor4 is one ordered IPv4 membership range cursor.
type MembershipRangeCursor4 struct {
	state *treeCursor
}

// Next returns the next membership range; ok reports whether a range was
// produced.
func (c *MembershipRangeCursor4) Next() (MembershipRange4, bool, error) {
	if c.state.finished {
		return MembershipRange4{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return MembershipRange4{}, false, err
	}
	rec, err := rangeRecordAt4(sl, c.state.index)
	if err != nil {
		return MembershipRange4{}, false, err
	}
	if _, _, err := c.state.advance(); err != nil {
		return MembershipRange4{}, false, err
	}
	return MembershipRange4{From: rec.From, To: rec.To, Membership: rec.Value}, true, nil
}

// MembershipRangeCursor6 is one ordered IPv6 membership range cursor.
type MembershipRangeCursor6 struct {
	state *treeCursor
}

// Next returns the next membership range; ok reports whether a range was
// produced.
func (c *MembershipRangeCursor6) Next() (MembershipRange6, bool, error) {
	if c.state.finished {
		return MembershipRange6{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return MembershipRange6{}, false, err
	}
	rec, err := rangeRecordAt6(sl, c.state.index)
	if err != nil {
		return MembershipRange6{}, false, err
	}
	if _, _, err := c.state.advance(); err != nil {
		return MembershipRange6{}, false, err
	}
	return MembershipRange6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo, Membership: rec.Value}, true, nil
}
