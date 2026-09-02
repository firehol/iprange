package reader

// Ordered projection of one named feed over the range tree (Rust
// feed_range_cursor.rs parity). The projection walks the range cursor,
// keeps only ranges whose value's membership bitmap contains the feed
// index (structured databases resolve the membership ID through the
// structure table), and coalesces adjacent same-feed intervals in the
// cursor's direction.

import "github.com/firehol/iprange/v4/go/internal/format"

// AddressRange4 is one value-free inclusive input interval.
type AddressRange4 struct {
	From, To uint32
}

// AddressRange6 is one value-free inclusive input interval.
type AddressRange6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
}

// FeedRangeProjection4 projects one feed out of the IPv4 range tree.
type FeedRangeProjection4 struct {
	r         *ImmutableReader
	feedIndex uint32
	direction RangeDirection
	inner     *DirectCursor4
	// pending is the coalescing interval under construction (Rust
	// Option<AddressRange>); membership caches the last resolved
	// membership ID (Rust Option<(u32, bool)>). All state is value
	// typed so the projection hot path allocates nothing.
	pending              AddressRange4
	hasPending           bool
	membershipCacheID    uint32
	membershipContains   bool
	membershipCacheValid bool
	rawFinished          bool
	finished             bool
}

func (r *ImmutableReader) NewFeedRangeProjection4(feedIndex uint32, direction RangeDirection) (*FeedRangeProjection4, error) {
	if err := requireFeedCursor(r.meta.ValueKind, feedIndex, r.meta.FeedIndexLimit); err != nil {
		return nil, err
	}
	inner, err := r.NewDirectCursor4(direction)
	if err != nil {
		return nil, err
	}
	return &FeedRangeProjection4{r: r, feedIndex: feedIndex, direction: direction, inner: inner}, nil
}

// requireFeedCursor validates the membership-capable database and feed
// namespace for one named-feed cursor (Rust require_feed).
func requireFeedCursor(valueKind uint8, feedIndex uint32, feedIndexLimit uint64) error {
	kind := valueKind
	if kind != format.ValueKindMembership && kind != format.ValueKindStructured {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "named-feed cursor requires a membership-capable database"}
	}
	if uint64(feedIndex) >= feedIndexLimit {
		return corrupt("feed index exceeds the catalog namespace")
	}
	return nil
}

// Seek repositions the projection to the containing feed interval or
// the nearest interval in the cursor's direction, discarding the
// coalescing state (Rust feed_range_cursor.rs ProjectionState::seek
// parity): the inner range cursor seeks and the pending interval,
// membership cache, and finished flags reset so the next Next call
// starts clean at the repositioned point.
func (c *FeedRangeProjection4) Seek(target uint32) error {
	if err := c.inner.Seek(target); err != nil {
		return err
	}
	c.pending, c.hasPending = AddressRange4{}, false
	c.membershipCacheValid = false
	c.rawFinished = false
	c.finished = false
	return nil
}

// Next returns the next coalesced interval belonging to the feed; ok
// reports whether an interval was produced.
func (c *FeedRangeProjection4) Next() (AddressRange4, bool, error) {
	if c.finished {
		return AddressRange4{}, false, nil
	}
	next, ok, err := c.nextInner()
	if err != nil {
		c.finished = true
		return AddressRange4{}, false, err
	}
	if !ok {
		c.finished = true
		return AddressRange4{}, false, nil
	}
	return next, true, nil
}

func (c *FeedRangeProjection4) nextInner() (AddressRange4, bool, error) {
	for {
		current, ok, err := c.nextMember()
		if err != nil {
			return AddressRange4{}, false, err
		}
		if !ok {
			pending, has := c.pending, c.hasPending
			c.pending, c.hasPending = AddressRange4{}, false
			return pending, has, nil
		}
		if !c.hasPending {
			c.pending, c.hasPending = current, true
			continue
		}
		if merged, adjacent := mergeRange4(c.direction, c.pending, current); adjacent {
			c.pending = merged
			continue
		}
		pending := c.pending
		c.pending, c.hasPending = current, true
		return pending, true, nil
	}
}

func (c *FeedRangeProjection4) nextMember() (AddressRange4, bool, error) {
	for !c.rawFinished {
		range_, ok, err := c.inner.Next()
		if err != nil {
			return AddressRange4{}, false, err
		}
		if !ok {
			c.rawFinished = true
			break
		}
		contains, err := c.contains(range_.Value)
		if err != nil {
			return AddressRange4{}, false, err
		}
		if contains {
			return AddressRange4{From: range_.From, To: range_.To}, true, nil
		}
	}
	return AddressRange4{}, false, nil
}

// contains resolves whether one range value's membership bitmap contains
// the projected feed index, caching the last resolved membership ID
// (Rust cached_membership).
func (c *FeedRangeProjection4) contains(value uint32) (bool, error) {
	if c.membershipCacheValid && c.membershipCacheID == value {
		return c.membershipContains, nil
	}
	id, err := c.membershipID(value)
	if err != nil {
		return false, err
	}
	contains := false
	if id != 0 {
		view, err := c.r.LookupMembershipID(id)
		if err != nil {
			return false, err
		}
		contains, err = view.ContainsIndex(c.feedIndex)
		if err != nil {
			return false, err
		}
	}
	c.membershipCacheID, c.membershipContains, c.membershipCacheValid = value, contains, true
	return contains, nil
}

// membershipID resolves the membership ID behind one range value
// (ValueKind::Membership values ARE feed indexes; structured values are
// resolved through the structure table, mirroring
// structured_value::membership_id: an absent structure ID is corruption).
func (c *FeedRangeProjection4) membershipID(value uint32) (uint32, error) {
	switch c.r.meta.ValueKind {
	case format.ValueKindMembership:
		return value, nil
	case format.ValueKindStructured:
		view, found, err := c.r.lookupStructureID(value)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, corrupt("range names an absent structure ID")
		}
		return view.MembershipID(), nil
	default:
		return 0, &format.Error{Code: format.CodeWrongValueKind, Detail: "named-feed cursor requires a membership-capable database"}
	}
}

// mergeRange4 coalesces two adjacent intervals in the cursor's direction
// (Rust merge: pending.to.checked_next() == current.from forward,
// current.to.checked_next() == pending.from backward; a range ending at
// the maximum address has no successor, so it never merges).
func mergeRange4(direction RangeDirection, pending, current AddressRange4) (AddressRange4, bool) {
	adjacent := false
	if direction == RangeForward {
		adjacent = pending.To != ^uint32(0) && pending.To+1 == current.From
	} else {
		adjacent = current.To != ^uint32(0) && current.To+1 == pending.From
	}
	if !adjacent {
		return AddressRange4{}, false
	}
	merged := pending
	if direction == RangeForward {
		merged.To = current.To
	} else {
		merged.From = current.From
	}
	return merged, true
}

// FeedRangeProjection6 projects one feed out of the IPv6 range tree.
type FeedRangeProjection6 struct {
	r         *ImmutableReader
	feedIndex uint32
	direction RangeDirection
	inner     *DirectCursor6
	// pending and the membership cache are value typed so the projection
	// hot path allocates nothing (Rust Option<AddressRange> and
	// Option<(u32, bool)>).
	pending              AddressRange6
	hasPending           bool
	membershipCacheID    uint32
	membershipContains   bool
	membershipCacheValid bool
	rawFinished          bool
	finished             bool
}

func (r *ImmutableReader) NewFeedRangeProjection6(feedIndex uint32, direction RangeDirection) (*FeedRangeProjection6, error) {
	if err := requireFeedCursor(r.meta.ValueKind, feedIndex, r.meta.FeedIndexLimit); err != nil {
		return nil, err
	}
	inner, err := r.NewDirectCursor6(direction)
	if err != nil {
		return nil, err
	}
	return &FeedRangeProjection6{r: r, feedIndex: feedIndex, direction: direction, inner: inner}, nil
}

// Seek repositions the projection to the containing feed interval or
// the nearest interval in the cursor's direction, discarding the
// coalescing state (Rust feed_range_cursor.rs ProjectionState::seek
// parity; IPv6 twin of the v4 Seek).
func (c *FeedRangeProjection6) Seek(targetHi, targetLo uint64) error {
	if err := c.inner.Seek(targetHi, targetLo); err != nil {
		return err
	}
	c.pending, c.hasPending = AddressRange6{}, false
	c.membershipCacheValid = false
	c.rawFinished = false
	c.finished = false
	return nil
}

// Next returns the next coalesced interval belonging to the feed; ok
// reports whether an interval was produced.
func (c *FeedRangeProjection6) Next() (AddressRange6, bool, error) {
	if c.finished {
		return AddressRange6{}, false, nil
	}
	next, ok, err := c.nextInner()
	if err != nil {
		c.finished = true
		return AddressRange6{}, false, err
	}
	if !ok {
		c.finished = true
		return AddressRange6{}, false, nil
	}
	return next, true, nil
}

func (c *FeedRangeProjection6) nextInner() (AddressRange6, bool, error) {
	for {
		current, ok, err := c.nextMember()
		if err != nil {
			return AddressRange6{}, false, err
		}
		if !ok {
			pending, has := c.pending, c.hasPending
			c.pending, c.hasPending = AddressRange6{}, false
			return pending, has, nil
		}
		if !c.hasPending {
			c.pending, c.hasPending = current, true
			continue
		}
		if merged, adjacent := mergeRange6(c.direction, c.pending, current); adjacent {
			c.pending = merged
			continue
		}
		pending := c.pending
		c.pending, c.hasPending = current, true
		return pending, true, nil
	}
}

func (c *FeedRangeProjection6) nextMember() (AddressRange6, bool, error) {
	for !c.rawFinished {
		range_, ok, err := c.inner.Next()
		if err != nil {
			return AddressRange6{}, false, err
		}
		if !ok {
			c.rawFinished = true
			break
		}
		contains, err := c.contains(range_.Value)
		if err != nil {
			return AddressRange6{}, false, err
		}
		if contains {
			return AddressRange6{FromHi: range_.FromHi, FromLo: range_.FromLo, ToHi: range_.ToHi, ToLo: range_.ToLo}, true, nil
		}
	}
	return AddressRange6{}, false, nil
}

func (c *FeedRangeProjection6) contains(value uint32) (bool, error) {
	if c.membershipCacheValid && c.membershipCacheID == value {
		return c.membershipContains, nil
	}
	id, err := c.membershipID(value)
	if err != nil {
		return false, err
	}
	contains := false
	if id != 0 {
		view, err := c.r.LookupMembershipID(id)
		if err != nil {
			return false, err
		}
		contains, err = view.ContainsIndex(c.feedIndex)
		if err != nil {
			return false, err
		}
	}
	c.membershipCacheID, c.membershipContains, c.membershipCacheValid = value, contains, true
	return contains, nil
}

func (c *FeedRangeProjection6) membershipID(value uint32) (uint32, error) {
	switch c.r.meta.ValueKind {
	case format.ValueKindMembership:
		return value, nil
	case format.ValueKindStructured:
		view, found, err := c.r.lookupStructureID(value)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, corrupt("range names an absent structure ID")
		}
		return view.MembershipID(), nil
	default:
		return 0, &format.Error{Code: format.CodeWrongValueKind, Detail: "named-feed cursor requires a membership-capable database"}
	}
}

func mergeRange6(direction RangeDirection, pending, current AddressRange6) (AddressRange6, bool) {
	// checked_next on the u128 endpoint: MAX has no successor, so an
	// adjac:ent merge is only possible when the endpoint is not MAX
	// (mirrors mergeRange4 and Rust merge).
	adjacent := false
	if direction == RangeForward {
		adjacent = pending.ToHi != ^uint64(0) || pending.ToLo != ^uint64(0)
		if adjacent {
			lo, carry := pending.ToLo+1, uint64(0)
			if pending.ToLo == ^uint64(0) {
				carry = 1
				lo = 0
			}
			adjacent = pending.ToHi+carry == current.FromHi && lo == current.FromLo
		}
	} else {
		adjacent = current.ToHi != ^uint64(0) || current.ToLo != ^uint64(0)
		if adjacent {
			lo, carry := current.ToLo+1, uint64(0)
			if current.ToLo == ^uint64(0) {
				carry = 1
				lo = 0
			}
			adjacent = current.ToHi+carry == pending.FromHi && lo == pending.FromLo
		}
	}
	if !adjacent {
		return AddressRange6{}, false
	}
	merged := pending
	if direction == RangeForward {
		merged.ToHi, merged.ToLo = current.ToHi, current.ToLo
	} else {
		merged.FromHi, merged.FromLo = current.FromHi, current.FromLo
	}
	return merged, true
}
