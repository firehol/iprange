// Ordered cursors over one opened immutable database (Rust database.rs
// cursor surface parity): direct-range cursors with seek, the catalog
// feed cursor, and the named-feed range projections. Cursors hold one
// opened reader; every call re-validates the reader state.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// RangeDirection selects the cursor movement direction.
type RangeDirection uint8

const (
	RangeDirectionForward  RangeDirection = 0
	RangeDirectionBackward RangeDirection = 1
)

// DirectCursorV4 walks the committed IPv4 range records in one direction.
type DirectCursorV4 struct {
	r     *ImmutableReader
	inner *reader.DirectCursor4
}

// DirectCursorV4 opens the IPv4 direct-range cursor in direction.
func (r *ImmutableReader) DirectCursorV4(direction RangeDirection) (*DirectCursorV4, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindDirect {
		return nil, &Error{Code: ErrorWrongValueKind, Detail: "direct cursor requires a direct-value database"}
	}
	if r.inner.Meta().AddressFamily != 4 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "range cursor address family does not match the database"}
	}
	inner, err := r.inner.NewDirectCursor4(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &DirectCursorV4{r: r, inner: inner}, nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor.
func (c *DirectCursorV4) Seek(target IPv4) error {
	if err := c.r.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(uint32(target)))
}

// NextRange returns the next range in the cursor's direction; ok reports
// whether a range was produced.
func (c *DirectCursorV4) NextRange() (DirectRangeV4, bool, error) {
	if err := c.r.checkOpen(); err != nil {
		return DirectRangeV4{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return DirectRangeV4{}, false, publicError(err)
	}
	if !ok {
		return DirectRangeV4{}, false, nil
	}
	return DirectRangeV4{From: rec.From, To: rec.To, Value: rec.Value}, true, nil
}

// DirectCursorV6 walks the committed IPv6 range records in one direction.
type DirectCursorV6 struct {
	r     *ImmutableReader
	inner *reader.DirectCursor6
}

// DirectCursorV6 opens the IPv6 direct-range cursor in direction.
func (r *ImmutableReader) DirectCursorV6(direction RangeDirection) (*DirectCursorV6, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindDirect {
		return nil, &Error{Code: ErrorWrongValueKind, Detail: "direct cursor requires a direct-value database"}
	}
	if r.inner.Meta().AddressFamily != 6 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "range cursor address family does not match the database"}
	}
	inner, err := r.inner.NewDirectCursor6(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &DirectCursorV6{r: r, inner: inner}, nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor.
func (c *DirectCursorV6) Seek(target IPv6) error {
	if err := c.r.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(target.Hi, target.Lo))
}

// NextRange returns the next range in the cursor's direction; ok reports
// whether a range was produced.
func (c *DirectCursorV6) NextRange() (DirectRangeV6, bool, error) {
	if err := c.r.checkOpen(); err != nil {
		return DirectRangeV6{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return DirectRangeV6{}, false, publicError(err)
	}
	if !ok {
		return DirectRangeV6{}, false, nil
	}
	return DirectRangeV6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo, Value: rec.Value}, true, nil
}

// FeedCursor walks the catalog in ascending feed-index order.
type FeedCursor struct {
	r     *ImmutableReader
	inner *reader.FeedCursor
}

// FeedCursor opens the forward catalog cursor.
func (r *ImmutableReader) FeedCursor() (*FeedCursor, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if err := r.requireMembershipCapable(); err != nil {
		return nil, err
	}
	inner, err := r.inner.NewFeedCursor()
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedCursor{r: r, inner: inner}, nil
}

// NextFeed returns the next catalog entry in ascending feed-index order;
// ok reports whether an entry was produced. The entry name is an owned
// string copy (allocated once per feed; the root boundary never hands
// out views that alias the mapping).
func (c *FeedCursor) NextFeed() (FeedEntry, bool, error) {
	if err := c.r.checkOpen(); err != nil {
		return FeedEntry{}, false, err
	}
	entry, ok, err := c.inner.Next()
	if err != nil {
		return FeedEntry{}, false, publicError(err)
	}
	if !ok {
		return FeedEntry{}, false, nil
	}
	return FeedEntry{Index: entry.FeedIndex, Name: string(entry.Name)}, true, nil
}

// FeedRangeCursorV4 returns the coalesced IPv4 intervals of one named
// feed in one direction.
type FeedRangeCursorV4 struct {
	r     *ImmutableReader
	inner *reader.FeedRangeProjection4
}

// FeedRangeCursorV4 opens the IPv4 projection of the named feed in
// direction.
func (r *ImmutableReader) FeedRangeCursorV4(name string, direction RangeDirection) (*FeedRangeCursorV4, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if err := r.requireMembershipCapable(); err != nil {
		return nil, err
	}
	if r.inner.Meta().AddressFamily != 4 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "feed cursor address family does not match the database"}
	}
	entry, found, err := r.LookupFeed(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &Error{Code: ErrorNameNotFound, Detail: "feed name not in the catalog"}
	}
	inner, err := r.inner.NewFeedRangeProjection4(entry.Index, reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedRangeCursorV4{r: r, inner: inner}, nil
}

// NextRange returns the next coalesced interval belonging to the feed; ok
// reports whether an interval was produced.
func (c *FeedRangeCursorV4) NextRange() (AddressRange4, bool, error) {
	if err := c.r.checkOpen(); err != nil {
		return AddressRange4{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return AddressRange4{}, false, publicError(err)
	}
	if !ok {
		return AddressRange4{}, false, nil
	}
	return AddressRange4{From: IPv4(rec.From), To: IPv4(rec.To)}, true, nil
}

// FeedRangeCursorV6 returns the coalesced IPv6 intervals of one named
// feed in one direction.
type FeedRangeCursorV6 struct {
	r     *ImmutableReader
	inner *reader.FeedRangeProjection6
}

// FeedRangeCursorV6 opens the IPv6 projection of the named feed in
// direction.
func (r *ImmutableReader) FeedRangeCursorV6(name string, direction RangeDirection) (*FeedRangeCursorV6, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if err := r.requireMembershipCapable(); err != nil {
		return nil, err
	}
	if r.inner.Meta().AddressFamily != 6 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "feed cursor address family does not match the database"}
	}
	entry, found, err := r.LookupFeed(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &Error{Code: ErrorNameNotFound, Detail: "feed name not in the catalog"}
	}
	inner, err := r.inner.NewFeedRangeProjection6(entry.Index, reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedRangeCursorV6{r: r, inner: inner}, nil
}

// NextRange returns the next coalesced interval belonging to the feed; ok
// reports whether an interval was produced.
func (c *FeedRangeCursorV6) NextRange() (AddressRange6, bool, error) {
	if err := c.r.checkOpen(); err != nil {
		return AddressRange6{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return AddressRange6{}, false, publicError(err)
	}
	if !ok {
		return AddressRange6{}, false, nil
	}
	return AddressRange6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo}, true, nil
}
