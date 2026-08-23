package reader

// Ordered structured-value range cursors (Rust
// structured_value/cursor.rs NetworkEnrichmentV1CursorV4/V6 parity). The
// cursor walks the same physical range tree as the direct surface in the
// forward direction; each record's value is a structure dictionary ID
// resolved through the same by-id path as the point lookups, yielding a
// NetworkEnrichmentV1View whose threat membership stays lazy until the
// caller resolves it. Kind and family guards run at construction exactly
// like Rust require_kind, and visiting a record counts RangeConsumed like
// the Rust CursorState::next shared by the direct cursor (the membership
// range cursor is the one deliberate exception, documented in
// membership_ranges.go).

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// NetworkEnrichmentV1Range4 is one inclusive enrichment interval.
type NetworkEnrichmentV1Range4 struct {
	From, To uint32
	Value    NetworkEnrichmentV1View
}

// NetworkEnrichmentV1Range6 is one inclusive enrichment interval.
type NetworkEnrichmentV1Range6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Value                      NetworkEnrichmentV1View
}

// requireStructuredFamilyLocked enforces the network-enrichment value
// kind and one address family on the reader's meta (Rust
// require_kind in structured_value/view.rs; the structure-kind check
// covers unknown wire codes exactly like Rust structure_kind() -> None).
func (r *ImmutableReader) requireStructuredFamilyLocked(family uint8) error {
	if r.meta.ValueKind != format.ValueKindStructured || r.meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return &format.Error{Code: format.CodeWrongStructureKind, Detail: "network enrichment lookup requires its matching structured database"}
	}
	if r.meta.AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

// NetworkEnrichmentV1Cursor4 is one ordered IPv4 enrichment cursor.
type NetworkEnrichmentV1Cursor4 struct {
	state *treeCursor
}

// NewNetworkEnrichmentV1Cursor4 opens one ordered IPv4
// network-enrichment cursor over the committed generation in direction
// (Rust NetworkEnrichmentV1CursorV4::new).
func (r *ImmutableReader) NewNetworkEnrichmentV1Cursor4(direction RangeDirection) (*NetworkEnrichmentV1Cursor4, error) {
	if err := r.requireStructuredFamilyLocked(format.AddressFamilyIPv4); err != nil {
		return nil, err
	}
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(direction), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &NetworkEnrichmentV1Cursor4{state: state}, nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *NetworkEnrichmentV1Cursor4) Seek(target uint32) error {
	c.state.seek4 = target
	if err := c.state.seekPosition(); err != nil {
		return err
	}
	if c.state.finished {
		return nil
	}
	if c.state.index >= int(c.state.itemCount) {
		c.state.index = int(c.state.itemCount) - 1
		if _, _, err := c.state.advance(); err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next enrichment range; ok reports whether a range was
// produced. Each range's structure payload is decoded during the visit
// through the same internal by-id path as LookupNetworkEnrichmentV14;
// a value naming an absent structure ID is corruption (Rust by_id's
// "range names an absent structure ID").
func (c *NetworkEnrichmentV1Cursor4) Next() (NetworkEnrichmentV1Range4, bool, error) {
	if c.state.finished {
		return NetworkEnrichmentV1Range4{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return NetworkEnrichmentV1Range4{}, false, err
	}
	rec, err := rangeRecordAt4(sl, c.state.index)
	if err != nil {
		return NetworkEnrichmentV1Range4{}, false, err
	}
	work.RangeConsumed(1)
	if _, _, err := c.state.advance(); err != nil {
		return NetworkEnrichmentV1Range4{}, false, err
	}
	view, found, err := c.state.r.lookupStructureID(rec.Value)
	if err != nil {
		return NetworkEnrichmentV1Range4{}, false, err
	}
	if !found {
		return NetworkEnrichmentV1Range4{}, false, corrupt("range names an absent structure ID")
	}
	return NetworkEnrichmentV1Range4{From: rec.From, To: rec.To, Value: view}, true, nil
}

// NetworkEnrichmentV1Cursor6 is one ordered IPv6 enrichment cursor.
type NetworkEnrichmentV1Cursor6 struct {
	state *treeCursor
}

// NewNetworkEnrichmentV1Cursor6 opens one ordered IPv6
// network-enrichment cursor over the committed generation in direction
// (Rust NetworkEnrichmentV1CursorV6::new).
func (r *ImmutableReader) NewNetworkEnrichmentV1Cursor6(direction RangeDirection) (*NetworkEnrichmentV1Cursor6, error) {
	if err := r.requireStructuredFamilyLocked(format.AddressFamilyIPv6); err != nil {
		return nil, err
	}
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(direction), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &NetworkEnrichmentV1Cursor6{state: state}, nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *NetworkEnrichmentV1Cursor6) Seek(targetHi, targetLo uint64) error {
	c.state.seekHi = targetHi
	c.state.seekLo = targetLo
	if err := c.state.seekPosition(); err != nil {
		return err
	}
	if c.state.finished {
		return nil
	}
	if c.state.index >= int(c.state.itemCount) {
		c.state.index = int(c.state.itemCount) - 1
		if _, _, err := c.state.advance(); err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next enrichment range; ok reports whether a range was
// produced. Structure decode and corruption rules mirror
// NetworkEnrichmentV1Cursor4.Next.
func (c *NetworkEnrichmentV1Cursor6) Next() (NetworkEnrichmentV1Range6, bool, error) {
	if c.state.finished {
		return NetworkEnrichmentV1Range6{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return NetworkEnrichmentV1Range6{}, false, err
	}
	rec, err := rangeRecordAt6(sl, c.state.index)
	if err != nil {
		return NetworkEnrichmentV1Range6{}, false, err
	}
	work.RangeConsumed(1)
	if _, _, err := c.state.advance(); err != nil {
		return NetworkEnrichmentV1Range6{}, false, err
	}
	view, found, err := c.state.r.lookupStructureID(rec.Value)
	if err != nil {
		return NetworkEnrichmentV1Range6{}, false, err
	}
	if !found {
		return NetworkEnrichmentV1Range6{}, false, corrupt("range names an absent structure ID")
	}
	return NetworkEnrichmentV1Range6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo, Value: view}, true, nil
}
