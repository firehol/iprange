// Ordered cursors over one opened immutable or live database (Rust
// database.rs and live_reader.rs cursor surface parity): direct-range
// cursors with seek, the catalog feed cursor, the named-feed range
// projections, and the network-enrichment cursors. Cursors hold one
// opened reader; every call re-validates the reader state.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// cursorHost is the reader surface the ordered cursors and the membership
// query hold open. Both the immutable and the live public facades
// implement it, so one cursor implementation serves both reader kinds
// (Rust parity: the immutable database and LiveReader expose the same
// cursor set). checkOpen reports the facade open state; core exposes the
// shared internal reader; addPin and dropPin guard the enrichment
// cursors' lifetime pins.
type cursorHost interface {
	checkOpen() error
	core() *reader.ImmutableReader
	addPin() bool
	dropPin()
}

// RangeDirection selects the cursor movement direction.
type RangeDirection uint8

const (
	RangeDirectionForward  RangeDirection = 0
	RangeDirectionBackward RangeDirection = 1
)

// DirectCursorV4 walks the committed IPv4 range records in one direction.
type DirectCursorV4 struct {
	r     cursorHost
	inner *reader.DirectCursor4
}

// DirectCursorV4 opens the IPv4 direct-range cursor in direction.
func (r *ImmutableReader) DirectCursorV4(direction RangeDirection) (*DirectCursorV4, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindDirect {
		return nil, &Error{Code: ErrorWrongValueKind, Detail: "direct lookup requires a direct-value database"}
	}
	if r.inner.Meta().AddressFamily != 4 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
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
	r     cursorHost
	inner *reader.DirectCursor6
}

// DirectCursorV6 opens the IPv6 direct-range cursor in direction.
func (r *ImmutableReader) DirectCursorV6(direction RangeDirection) (*DirectCursorV6, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindDirect {
		return nil, &Error{Code: ErrorWrongValueKind, Detail: "direct lookup requires a direct-value database"}
	}
	if r.inner.Meta().AddressFamily != 6 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
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
	r     cursorHost
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

// SeekByIndex repositions to the first catalog entry whose feed index
// is at least target, discarding already-consumed state (Rust
// feed_catalog.rs FeedCursor::seek_by_index parity). Entries before
// the target are never revisited; subsequent NextFeed calls continue
// from the repositioned entry. Seeking to 0 restarts a complete sweep;
// seeking past the last entry finishes the cursor. Seeks are repeatable
// on an exhausted cursor.
func (c *FeedCursor) SeekByIndex(target uint32) error {
	if err := c.r.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.SeekByIndex(target))
}

// FeedRangeCursorV4 returns the coalesced IPv4 intervals of one named
// feed in one direction.
type FeedRangeCursorV4 struct {
	r     cursorHost
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

// Seek repositions to the containing feed interval or the nearest
// interval in the cursor's direction, discarding the projection
// coalescing state (Rust ProjectionState::seek parity). Seeks are
// repeatable on an exhausted cursor.
func (c *FeedRangeCursorV4) Seek(target IPv4) error {
	if err := c.r.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(uint32(target)))
}

// FeedRangeCursorV6 returns the coalesced IPv6 intervals of one named
// feed in one direction.
type FeedRangeCursorV6 struct {
	r     cursorHost
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

// Seek repositions to the containing feed interval or the nearest
// interval in the cursor's direction, discarding the projection
// coalescing state (Rust ProjectionState::seek parity). Seeks are
// repeatable on an exhausted cursor.
func (c *FeedRangeCursorV6) Seek(target IPv6) error {
	if err := c.r.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(target.Hi, target.Lo))
}

// NetworkEnrichmentV1RangeV4 is one typed IPv4 enrichment interval
// (Rust network_enrichment_v1_cursor_v4 next_range). The Value view is
// valid while the cursor is open.
type NetworkEnrichmentV1RangeV4 struct {
	From, To IPv4
	Value    NetworkEnrichmentV1View
}

// NetworkEnrichmentV1CursorV4 walks the committed IPv4 enrichment
// ranges in one direction (Rust NetworkEnrichmentV1CursorV4). Close
// must be called to release the reader pin the cursor holds.
type NetworkEnrichmentV1CursorV4 struct {
	r     cursorHost
	inner *reader.NetworkEnrichmentV1Cursor4
	state *pinState
}

// NetworkEnrichmentV1CursorV4 opens the IPv4 enrichment cursor in the
// requested direction. The cursor holds one reader lifetime pin (Rust
// borrow parity): the reader refuses to close while the cursor is open,
// and Close releases the pin.
func (r *ImmutableReader) NetworkEnrichmentV1CursorV4(direction RangeDirection) (*NetworkEnrichmentV1CursorV4, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindStructured || r.inner.Meta().StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return nil, &Error{Code: ErrorWrongStructureKind, Detail: "network enrichment lookup requires its matching structured database"}
	}
	if r.inner.Meta().AddressFamily != 4 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	inner, err := r.inner.NewNetworkEnrichmentV1Cursor4(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	// A Close that raced this cursor either saw the added pin
	// (HandleBusy) or closed first; addPin's closed re-check makes the
	// loser return WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &NetworkEnrichmentV1CursorV4{r: r, inner: inner, state: &pinState{h: r}}, nil
}

// checkOpen reports the cursor state: every call re-validates the
// reader state and the cursor's own open flag.
func (c *NetworkEnrichmentV1CursorV4) checkOpen() error {
	if c.state == nil || c.state.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment cursor is closed"}
	}
	return c.r.checkOpen()
}

// Close releases the cursor's reader pin. The reader refuses to close
// while any enrichment cursor is open; a second Close reports
// WrongState. Views are readable while the cursor is open and refuse
// after Close; the cursor pin is the Go mapping of the Rust reader
// borrow, scoped to the cursor lifetime.
func (c *NetworkEnrichmentV1CursorV4) Close() error {
	if c.state == nil || c.state.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment cursor already closed"}
	}
	c.state.closed = true
	c.r.dropPin()
	return nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *NetworkEnrichmentV1CursorV4) Seek(target IPv4) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(uint32(target)))
}

// NextRange returns the next enrichment range in the cursor's direction;
// ok reports whether a range was produced. The range value is decoded
// during the visit; a value naming an absent structure ID is corruption.
func (c *NetworkEnrichmentV1CursorV4) NextRange() (NetworkEnrichmentV1RangeV4, bool, error) {
	if err := c.checkOpen(); err != nil {
		return NetworkEnrichmentV1RangeV4{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return NetworkEnrichmentV1RangeV4{}, false, publicError(err)
	}
	if !ok {
		return NetworkEnrichmentV1RangeV4{}, false, nil
	}
	return NetworkEnrichmentV1RangeV4{
		From:  IPv4(rec.From),
		To:    IPv4(rec.To),
		Value: NetworkEnrichmentV1View{st: c.state, inner: rec.Value},
	}, true, nil
}

// NetworkEnrichmentV1RangeV6 is one typed IPv6 enrichment interval
// (Rust network_enrichment_v1_cursor_v6 next_range). The Value view is
// valid while the cursor is open.
type NetworkEnrichmentV1RangeV6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Value                      NetworkEnrichmentV1View
}

// NetworkEnrichmentV1CursorV6 walks the committed IPv6 enrichment
// ranges in one direction (Rust NetworkEnrichmentV1CursorV6). Close
// must be called to release the reader pin the cursor holds.
type NetworkEnrichmentV1CursorV6 struct {
	r     cursorHost
	inner *reader.NetworkEnrichmentV1Cursor6
	state *pinState
}

// NetworkEnrichmentV1CursorV6 opens the IPv6 enrichment cursor in the
// requested direction. The cursor holds one reader lifetime pin (Rust
// borrow parity): the reader refuses to close while the cursor is open,
// and Close releases the pin.
func (r *ImmutableReader) NetworkEnrichmentV1CursorV6(direction RangeDirection) (*NetworkEnrichmentV1CursorV6, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if r.inner.Meta().ValueKind != format.ValueKindStructured || r.inner.Meta().StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return nil, &Error{Code: ErrorWrongStructureKind, Detail: "network enrichment lookup requires its matching structured database"}
	}
	if r.inner.Meta().AddressFamily != 6 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	inner, err := r.inner.NewNetworkEnrichmentV1Cursor6(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	// A Close that raced this cursor either saw the added pin
	// (HandleBusy) or closed first; addPin's closed re-check makes the
	// loser return WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &NetworkEnrichmentV1CursorV6{r: r, inner: inner, state: &pinState{h: r}}, nil
}

// checkOpen reports the cursor state: every call re-validates the
// reader state and the cursor's own open flag.
func (c *NetworkEnrichmentV1CursorV6) checkOpen() error {
	if c.state == nil || c.state.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment cursor is closed"}
	}
	return c.r.checkOpen()
}

// Close releases the cursor's reader pin. The reader refuses to close
// while any enrichment cursor is open; a second Close reports
// WrongState. Views are readable while the cursor is open and refuse
// after Close; the cursor pin is the Go mapping of the Rust reader
// borrow, scoped to the cursor lifetime.
func (c *NetworkEnrichmentV1CursorV6) Close() error {
	if c.state == nil || c.state.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment cursor already closed"}
	}
	c.state.closed = true
	c.r.dropPin()
	return nil
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *NetworkEnrichmentV1CursorV6) Seek(target IPv6) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	return publicError(c.inner.Seek(target.Hi, target.Lo))
}

// NextRange returns the next enrichment range in the cursor's direction;
// ok reports whether a range was produced. The range value is decoded
// during the visit; a value naming an absent structure ID is corruption.
func (c *NetworkEnrichmentV1CursorV6) NextRange() (NetworkEnrichmentV1RangeV6, bool, error) {
	if err := c.checkOpen(); err != nil {
		return NetworkEnrichmentV1RangeV6{}, false, err
	}
	rec, ok, err := c.inner.Next()
	if err != nil {
		return NetworkEnrichmentV1RangeV6{}, false, publicError(err)
	}
	if !ok {
		return NetworkEnrichmentV1RangeV6{}, false, nil
	}
	return NetworkEnrichmentV1RangeV6{
		FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo,
		Value: NetworkEnrichmentV1View{st: c.state, inner: rec.Value},
	}, true, nil
}
