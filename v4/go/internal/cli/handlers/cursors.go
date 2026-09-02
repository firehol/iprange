// Bounded feed and range cursor JSON-RPC handlers (Rust
// handlers/cursors.rs parity, iprange-jsonrpc-v1.md reader feeds/
// ranges methods). Cursors are connection checkpoints: every `next`
// call re-opens a fresh SDK cursor and positions it at the retained
// checkpoint, so each response stays bounded. Pages are sized against
// the complete response object (echoed id included) and stop before
// the 65,000-byte ceiling; exhausting a cursor closes it automatically.

package handlers

import (
	"encoding/json"
	"fmt"
	"math/bits"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// CursorLimit is the per-connection cursor handle bound (spec limits).
const CursorLimit = 64

// feedNameValidator accepts the exact v4 FeedName grammar (spec file
// sources and selections): 1 through 255 lowercase ASCII bytes, first
// and last char a-z or 0-9, interior also _ - .
func validFeedName(name string) bool {
	return feedNameValid(name)
}

// RegisterCursors installs the reader feeds/ranges cursor methods. The
// lead calls it from register.go's RegisterAll.
func RegisterCursors() {
	rpc.Register("iprange.v1.reader.feeds.open", ValidateFeedsOpen, FeedsOpen)
	rpc.Register("iprange.v1.reader.feeds.next", ValidateCursorParam, FeedsNext)
	rpc.Register("iprange.v1.reader.feeds.close", ValidateCursorParam, FeedsClose)
	rpc.Register("iprange.v1.reader.ranges.open", ValidateRangesOpen, RangesOpen)
	rpc.Register("iprange.v1.reader.ranges.next", ValidateCursorParam, RangesNext)
	rpc.Register("iprange.v1.reader.ranges.close", ValidateCursorParam, RangesClose)
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

// ValidateCursorParam enforces the handle-only cursor params of the
// next/close methods.
func ValidateCursorParam(params json.RawMessage) error {
	object, err := exactObject(params, "cursor")
	if err != nil {
		return err
	}
	handle, err := asString(object, "cursor")
	if err != nil {
		return err
	}
	return validateHandle(handle)
}

// ValidateFeedsOpen enforces the reader.feeds.open params: one reader
// handle and a batch size from 1 through 4096.
func ValidateFeedsOpen(params json.RawMessage) error {
	object, err := exactObject(params, "reader", "batch_size")
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	if err := validateHandle(handle); err != nil {
		return err
	}
	batch, err := asUint64(object, "batch_size")
	if err != nil {
		return err
	}
	return validateBatch(batch)
}

// ValidateRangesOpen enforces the reader.ranges.open params: one
// reader handle, a strict view, a direction, an optional start, and a
// batch size from 1 through 4096. Start is invalid for a feed view.
func ValidateRangesOpen(params json.RawMessage) error {
	object, err := exactObjectOpt(params, []string{"reader", "view", "direction", "batch_size"}, []string{"start"})
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	if err := validateHandle(handle); err != nil {
		return err
	}
	view, err := memberObject(object, "view")
	if err != nil {
		return err
	}
	kind, err := validateView(view)
	if err != nil {
		return err
	}
	direction, err := asString(object, "direction")
	if err != nil {
		return err
	}
	if direction != "forward" && direction != "reverse" {
		return fmt.Errorf("direction must be forward or reverse")
	}
	if raw, ok := object["start"]; ok {
		if kind == "feed" {
			return fmt.Errorf("start is not valid for a feed view")
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return fmt.Errorf("start must be a canonical IP address when present")
		}
		if _, herr := ParseAddress(text); herr != nil {
			return fmt.Errorf("start must be a canonical IP address when present")
		}
	}
	batch, err := asUint64(object, "batch_size")
	if err != nil {
		return err
	}
	return validateBatch(batch)
}

// validateView enforces the range cursor view object (methods.py
// reader.ranges.open): direct/structured accept only kind, feed
// requires exactly kind plus a valid feed name. It returns the kind.
func validateView(view rawObject) (string, error) {
	kind, err := asString(view, "kind")
	if err != nil {
		return "", fmt.Errorf("view.kind must be direct, structured, or feed")
	}
	switch kind {
	case "direct", "structured":
		if len(view) != 1 {
			return "", fmt.Errorf("direct and structured views accept only kind")
		}
	case "feed":
		if len(view) != 2 {
			return "", fmt.Errorf("feed view requires exactly kind and feed")
		}
		name, err := asString(view, "feed")
		if err != nil {
			return "", fmt.Errorf("view.feed must be a string")
		}
		if !validFeedName(name) {
			return "", fmt.Errorf("view.feed is not a valid feed name")
		}
	default:
		return "", fmt.Errorf("view.kind must be direct, structured, or feed")
	}
	return kind, nil
}

func validateBatch(value uint64) error {
	if value < 1 || value > 4096 {
		return fmt.Errorf("batch_size must be from 1 through 4096")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// FeedsOpen opens a feed-catalog cursor over one membership-capable
// reader (spec reader.feeds.open). Direct databases refuse with
// handle_wrong_kind.
func FeedsOpen(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader", "batch_size")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	readerHandle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	batchValue, err := asUint64(object, "batch_size")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	if herr := ensureCursorCapacity(st); herr != nil {
		return nil, herr
	}
	reader, herr := ReaderHandle(st, readerHandle)
	if herr != nil {
		return nil, herr
	}
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, herr
	}
	if info.ValueKind == iprangedb.ValueKindDirect {
		return nil, wrongView("feed enumeration requires a membership-capable database")
	}
	// Probe the catalog cursor so a wrong-kind database refuses before
	// any handle is allocated (Rust cursors.rs parity).
	if _, herr := sdk(sdkSurface(reader).FeedCursor()); herr != nil {
		return nil, viewError(herr)
	}
	handle, herr := insertCursor(st, readerHandle, rpc.CursorKindFeeds, rpc.CursorView{}, false, nil, int(batchValue))
	if herr != nil {
		return nil, herr
	}
	return boundedResult(map[string]any{
		"method": "iprange.v1.reader.feeds.open",
		"cursor": handle,
	})
}

// FeedsNext returns one bounded page of catalog rows in feed-catalog
// order (spec reader.feeds.next). Each page re-opens the catalog cursor
// and seeks it once to the retained feed-index checkpoint. The cursor
// closes automatically at done:true.
func FeedsNext(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	cursor := st.Resources.Cursors[handle]
	if cursor == nil {
		return nil, closedOrUnknownCursor(st, handle)
	}
	if cursor.Kind != rpc.CursorKindFeeds {
		return nil, wrongCursorKind()
	}
	encoded, herr := cursorBase(st, "iprange.v1.reader.feeds.next", "feeds")
	if herr != nil {
		return nil, herr
	}
	reader, herr := ReaderHandle(st, cursor.Reader)
	if herr != nil {
		return nil, herr
	}
	catalog, herr := sdk(sdkSurface(reader).FeedCursor())
	if herr != nil {
		return nil, herr
	}
	if cursor.LastFeedIndex != nil {
		if *cursor.LastFeedIndex == ^uint32(0) {
			// Defensive sentinel: the catalog is past its last index.
			result := map[string]any{
				"method": "iprange.v1.reader.feeds.next",
				"feeds":  []any{},
				"done":   true,
			}
			closeCursor(st, handle)
			return boundedResult(result)
		}
		// One O(log n) reposition per page (Rust cursors.rs
		// feeds.next parity): the catalog cursor seeks directly to the
		// checkpoint so the first row of this page is exactly the row
		// that follows the last emitted row of the previous page.
		if err := catalog.SeekByIndex(*cursor.LastFeedIndex + 1); err != nil {
			return nil, readError(err)
		}
	}
	rows := make([]any, 0)
	var last *uint32
	done := false
	for len(rows) < cursor.BatchSize {
		entry, ok, err := catalog.NextFeed()
		if err != nil {
			return nil, readError(err)
		}
		if !ok {
			done = true
			break
		}
		row := map[string]any{"name": entry.Name}
		if !fitsNextItem(encoded, rows, row) {
			if len(rows) == 0 {
				closeCursor(st, handle)
				return nil, cursorLimitError()
			}
			break
		}
		encoded += itemSize(rows, row)
		rows = append(rows, row)
		index := entry.Index
		last = &index
	}
	result := map[string]any{
		"method": "iprange.v1.reader.feeds.next",
		"feeds":  rows,
		"done":   done,
	}
	if _, herr := boundedResult(result); herr != nil {
		closeCursor(st, handle)
		return nil, herr
	}
	if done {
		closeCursor(st, handle)
	} else if active := st.Resources.Cursors[handle]; active != nil {
		active.LastFeedIndex = last
	}
	return boundedResult(result)
}

// FeedsClose closes one feed-catalog cursor (spec reader.feeds.close).
func FeedsClose(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return closeCursorHandler(st, params, "iprange.v1.reader.feeds.close", rpc.CursorKindFeeds)
}

// RangesOpen opens a range cursor over one reader with a strict view,
// direction, optional start, and batch size (spec reader.ranges.open).
// The SDK cursor is opened and seeked once to validate the view before
// the handle is allocated.
func RangesOpen(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObjectOpt(params, []string{"reader", "view", "direction", "batch_size"}, []string{"start"})
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	readerHandle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	batchValue, err := asUint64(object, "batch_size")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	viewObject, err := memberObject(object, "view")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	kind, err := asString(viewObject, "kind")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	feed, err := asOptionalString(viewObject, "feed")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	direction, err := asString(object, "direction")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	var start *rpc.CursorPoint
	if raw, ok := object["start"]; ok {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, rpc.InvalidParamsError("start must be a canonical IP address when present")
		}
		point, herr := ParseAddress(text)
		if herr != nil {
			return nil, herr
		}
		start = point
	}
	view := rpc.CursorView{}
	switch kind {
	case "direct":
		view.Direct = true
	case "structured":
		view.Structured = true
	case "feed":
		view.FeedName = feed
	}
	if start != nil && view.FeedName != "" {
		return nil, rpc.InvalidParamsError("start is valid only for direct or structured views")
	}
	if herr := ensureCursorCapacity(st); herr != nil {
		return nil, herr
	}
	reader, herr := ReaderHandle(st, readerHandle)
	if herr != nil {
		return nil, herr
	}
	if herr := openAndSeek(reader, &view, direction == "reverse", start); herr != nil {
		return nil, herr
	}
	handle, herr := insertCursor(st, readerHandle, rpc.CursorKindRanges, view, direction == "reverse", start, int(batchValue))
	if herr != nil {
		return nil, herr
	}
	return boundedResult(map[string]any{
		"method": "iprange.v1.reader.ranges.open",
		"cursor": handle,
	})
}

// RangesNext returns one bounded page of range records (spec
// reader.ranges.next): direct and structured records carry the semantic
// value, feed records carry only from/to. Each page re-opens fresh SDK
// cursors and seeks each once to the retained address checkpoint. The
// cursor closes automatically at done:true.
func RangesNext(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	cursor := st.Resources.Cursors[handle]
	if cursor == nil {
		return nil, closedOrUnknownCursor(st, handle)
	}
	if cursor.Kind != rpc.CursorKindRanges {
		return nil, wrongCursorKind()
	}
	if cursor.Exhausted {
		closeCursor(st, handle)
		return boundedResult(map[string]any{
			"method":  "iprange.v1.reader.ranges.next",
			"records": []any{},
			"done":    true,
		})
	}
	encoded, herr := cursorBase(st, "iprange.v1.reader.ranges.next", "records")
	if herr != nil {
		return nil, herr
	}
	// Captured before the reader borrow so the page loop can poll the
	// transport cancellation token (Rust cursors.rs parity).
	cancellation := st.Token()
	reader, herr := ReaderHandle(st, cursor.Reader)
	if herr != nil {
		return nil, herr
	}
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, herr
	}
	// Feed views keep one projection cursor open for the whole page,
	// seeked once to the checkpoint.
	var feedPage *pageFeedCursor
	if cursor.View.FeedName != "" {
		feedPage, herr = openFeedPage(reader, info, cursor.View.FeedName, rangeDirection(cursor.Reverse), cursor.Point)
		if herr != nil {
			return nil, herr
		}
	}
	// Structured pages map threat feeds through one catalog snapshot and
	// one reusable membership-word buffer shared by every record.
	var snapshot *FeedSnapshot
	if cursor.View.Structured {
		snapshot, herr = BuildFeedSnapshot(reader)
		if herr != nil {
			return nil, herr
		}
	}
	words := []uint64{}
	point := cursor.Point
	records := make([]any, 0)
	exhausted := false
	for len(records) < cursor.BatchSize {
		if cancellation.IsCancelled() {
			return nil, rpc.NewHandlerError("cancelled", "not_started",
				"cursor read was cancelled")
		}
		record, nextPoint, ok, herr := nextRecord(reader, info, cursor.View, cursor.Reverse, point, feedPage, snapshot, &words)
		if herr != nil {
			return nil, herr
		}
		if !ok {
			exhausted = true
			break
		}
		if !fitsNextItem(encoded, records, record) {
			// The record is not committed; the checkpoint stays at the
			// pre-record position so a later page resumes exactly.
			if len(records) == 0 {
				closeCursor(st, handle)
				return nil, cursorLimitError()
			}
			break
		}
		encoded += itemSize(records, record)
		records = append(records, record)
		point = nextPoint
		if point == nil {
			exhausted = true
			break
		}
	}
	result := map[string]any{
		"method":  "iprange.v1.reader.ranges.next",
		"records": records,
		"done":    exhausted,
	}
	if _, herr := boundedResult(result); herr != nil {
		closeCursor(st, handle)
		return nil, herr
	}
	if exhausted {
		closeCursor(st, handle)
	} else if active := st.Resources.Cursors[handle]; active != nil {
		active.Point = point
	}
	return boundedResult(result)
}

// RangesClose closes one range cursor (spec reader.ranges.close).
func RangesClose(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return closeCursorHandler(st, params, "iprange.v1.reader.ranges.close", rpc.CursorKindRanges)
}

// ---------------------------------------------------------------------------
// Cursor page machinery
// ---------------------------------------------------------------------------

// pageFeedCursor is one feed projection held open for one ranges.next
// page, seeked once to the retained address checkpoint (Rust
// cursors.rs open_feed_page parity: one O(log n) seek per page instead
// of a linear walk from the start of the projection).
type pageFeedCursor struct {
	v4 *iprangedb.FeedRangeCursorV4
	v6 *iprangedb.FeedRangeCursorV6
}

// openFeedPage opens the named-feed projection and positions it at the
// checkpoint (Rust cursors.rs open_feed_page parity: the projection
// seeks once per page to the retained address, so the first record of
// the page is exactly the record that follows the last emitted record
// of the previous page).
func openFeedPage(reader *rpc.ReaderValue, info iprangedb.DatabaseInfo, name string, direction iprangedb.RangeDirection, point *rpc.CursorPoint) (*pageFeedCursor, *rpc.HandlerError) {
	if !familyMatches(point, info.Family) {
		return nil, wrongFamily()
	}
	op := sdkSurface(reader)
	if info.Family == iprangedb.AddressFamilyIPv4 {
		c, herr := sdk(op.FeedRangeCursorV4(name, direction))
		if herr != nil {
			return nil, viewError(herr)
		}
		if point != nil && point.V4 != nil {
			if herr := sdkErr(c.Seek(iprangedb.IPv4(*point.V4))); herr != nil {
				return nil, herr
			}
		}
		return &pageFeedCursor{v4: c}, nil
	}
	c, herr := sdk(op.FeedRangeCursorV6(name, direction))
	if herr != nil {
		return nil, viewError(herr)
	}
	if point != nil && point.V6 != nil {
		if herr := sdkErr(c.Seek(*point.V6)); herr != nil {
			return nil, herr
		}
	}
	return &pageFeedCursor{v6: c}, nil
}

// nextRecord produces the next page record and the checkpoint that
// follows it (nil when the record ends the family range), or ok=false
// when the stream is exhausted.
func nextRecord(reader *rpc.ReaderValue, info iprangedb.DatabaseInfo, view rpc.CursorView, reverse bool, point *rpc.CursorPoint, feed *pageFeedCursor, snapshot *FeedSnapshot, words *[]uint64) (any, *rpc.CursorPoint, bool, *rpc.HandlerError) {
	if !familyMatches(point, info.Family) {
		return nil, nil, false, wrongFamily()
	}
	direction := rangeDirection(reverse)
	op := sdkSurface(reader)
	ipv4 := info.Family == iprangedb.AddressFamilyIPv4
	switch {
	case view.Direct && ipv4:
		c, herr := sdk(op.DirectCursorV4(direction))
		if herr != nil {
			return nil, nil, false, viewError(herr)
		}
		if point != nil && point.V4 != nil {
			if herr := sdkErr(c.Seek(iprangedb.IPv4(*point.V4))); herr != nil {
				return nil, nil, false, herr
			}
		}
		rng, ok, err := c.NextRange()
		if err != nil {
			return nil, nil, false, readError(err)
		}
		if !ok {
			return nil, nil, false, nil
		}
		record := map[string]any{
			"from":  addressV4(uint32(rng.From)),
			"to":    addressV4(uint32(rng.To)),
			"value": rng.Value,
		}
		return record, nextV4Point(uint32(rng.From), uint32(rng.To), reverse), true, nil
	case view.Direct && !ipv4:
		c, herr := sdk(op.DirectCursorV6(direction))
		if herr != nil {
			return nil, nil, false, viewError(herr)
		}
		if point != nil && point.V6 != nil {
			if herr := sdkErr(c.Seek(*point.V6)); herr != nil {
				return nil, nil, false, herr
			}
		}
		rng, ok, err := c.NextRange()
		if err != nil {
			return nil, nil, false, readError(err)
		}
		if !ok {
			return nil, nil, false, nil
		}
		record := map[string]any{
			"from":  addressV6(rng.FromHi, rng.FromLo),
			"to":    addressV6(rng.ToHi, rng.ToLo),
			"value": rng.Value,
		}
		return record, nextV6Point(rng.FromHi, rng.FromLo, rng.ToHi, rng.ToLo, reverse), true, nil
	case view.Structured && ipv4:
		c, herr := sdk(op.NetworkEnrichmentV1CursorV4(direction))
		if herr != nil {
			return nil, nil, false, viewError(herr)
		}
		// The enrichment cursor pins the reader; every exit closes it.
		if point != nil && point.V4 != nil {
			if herr := sdkErr(c.Seek(iprangedb.IPv4(*point.V4))); herr != nil {
				_ = c.Close()
				return nil, nil, false, herr
			}
		}
		rng, ok, err := c.NextRange()
		if err != nil {
			_ = c.Close()
			return nil, nil, false, readError(err)
		}
		if !ok {
			_ = c.Close()
			return nil, nil, false, nil
		}
		feeds, herr := ThreatFeedNames(rng.Value, snapshot, words)
		if herr != nil {
			_ = c.Close()
			return nil, nil, false, herr
		}
		value, err := rng.Value.Value()
		if err != nil {
			_ = c.Close()
			return nil, nil, false, readError(err)
		}
		if err := c.Close(); err != nil {
			return nil, nil, false, readError(err)
		}
		record := map[string]any{
			"from":  addressV4(uint32(rng.From)),
			"to":    addressV4(uint32(rng.To)),
			"value": NetworkEnrichmentJSON(value, feeds),
		}
		return record, nextV4Point(uint32(rng.From), uint32(rng.To), reverse), true, nil
	case view.Structured && !ipv4:
		c, herr := sdk(op.NetworkEnrichmentV1CursorV6(direction))
		if herr != nil {
			return nil, nil, false, viewError(herr)
		}
		if point != nil && point.V6 != nil {
			if herr := sdkErr(c.Seek(*point.V6)); herr != nil {
				_ = c.Close()
				return nil, nil, false, herr
			}
		}
		rng, ok, err := c.NextRange()
		if err != nil {
			_ = c.Close()
			return nil, nil, false, readError(err)
		}
		if !ok {
			_ = c.Close()
			return nil, nil, false, nil
		}
		feeds, herr := ThreatFeedNames(rng.Value, snapshot, words)
		if herr != nil {
			_ = c.Close()
			return nil, nil, false, herr
		}
		value, err := rng.Value.Value()
		if err != nil {
			_ = c.Close()
			return nil, nil, false, readError(err)
		}
		if err := c.Close(); err != nil {
			return nil, nil, false, readError(err)
		}
		record := map[string]any{
			"from":  addressV6(rng.FromHi, rng.FromLo),
			"to":    addressV6(rng.ToHi, rng.ToLo),
			"value": NetworkEnrichmentJSON(value, feeds),
		}
		return record, nextV6Point(rng.FromHi, rng.FromLo, rng.ToHi, rng.ToLo, reverse), true, nil
	case view.FeedName != "" && ipv4:
		rng, ok, herr := nextFeedPage4(feed)
		if herr != nil {
			return nil, nil, false, herr
		}
		if !ok {
			return nil, nil, false, nil
		}
		record := map[string]any{
			"from": addressV4(uint32(rng.From)),
			"to":   addressV4(uint32(rng.To)),
		}
		return record, nextV4Point(uint32(rng.From), uint32(rng.To), reverse), true, nil
	case view.FeedName != "" && !ipv4:
		rng, ok, herr := nextFeedPage6(feed)
		if herr != nil {
			return nil, nil, false, herr
		}
		if !ok {
			return nil, nil, false, nil
		}
		record := map[string]any{
			"from": addressV6(rng.FromHi, rng.FromLo),
			"to":   addressV6(rng.ToHi, rng.ToLo),
		}
		return record, nextV6Point(rng.FromHi, rng.FromLo, rng.ToHi, rng.ToLo, reverse), true, nil
	}
	return nil, nil, false, wrongView("reader does not support the requested cursor view")
}

// nextFeedPage4 returns the next record of an open IPv4 feed page; the
// page cursor was already seeked to the checkpoint at open.
func nextFeedPage4(feed *pageFeedCursor) (*iprangedb.AddressRange4, bool, *rpc.HandlerError) {
	if feed == nil || feed.v4 == nil {
		return nil, false, rpc.NewHandlerError("handle_wrong_kind", "not_started",
			"feed page cursor does not match the reader family")
	}
	rng, ok, err := feed.v4.NextRange()
	if err != nil {
		return nil, false, readError(err)
	}
	if !ok {
		return nil, false, nil
	}
	return &rng, true, nil
}

// nextFeedPage6 returns the next record of an open IPv6 feed page; the
// page cursor was already seeked to the checkpoint at open.
func nextFeedPage6(feed *pageFeedCursor) (*iprangedb.AddressRange6, bool, *rpc.HandlerError) {
	if feed == nil || feed.v6 == nil {
		return nil, false, rpc.NewHandlerError("handle_wrong_kind", "not_started",
			"feed page cursor does not match the reader family")
	}
	rng, ok, err := feed.v6.NextRange()
	if err != nil {
		return nil, false, readError(err)
	}
	if !ok {
		return nil, false, nil
	}
	return &rng, true, nil
}

// openAndSeek validates one requested view against the reader by
// opening and seek-probing the matching SDK cursor before any handle is
// allocated (Rust cursors.rs open_and_seek parity).
func openAndSeek(reader *rpc.ReaderValue, view *rpc.CursorView, reverse bool, start *rpc.CursorPoint) *rpc.HandlerError {
	info, herr := readerInfo(reader)
	if herr != nil {
		return herr
	}
	if !familyMatches(start, info.Family) {
		return wrongFamily()
	}
	direction := rangeDirection(reverse)
	op := sdkSurface(reader)
	switch {
	case view.Direct:
		if info.Family == iprangedb.AddressFamilyIPv4 {
			c, herr := sdk(op.DirectCursorV4(direction))
			if herr != nil {
				return herr
			}
			if start != nil {
				return sdkErr(c.Seek(iprangedb.IPv4(*start.V4)))
			}
			return nil
		}
		c, herr := sdk(op.DirectCursorV6(direction))
		if herr != nil {
			return herr
		}
		if start != nil {
			return sdkErr(c.Seek(*start.V6))
		}
		return nil
	case view.Structured:
		if info.Family == iprangedb.AddressFamilyIPv4 {
			c, herr := sdk(op.NetworkEnrichmentV1CursorV4(direction))
			if herr != nil {
				return viewError(herr)
			}
			if start != nil {
				if herr := sdkErr(c.Seek(iprangedb.IPv4(*start.V4))); herr != nil {
					_ = c.Close()
					return herr
				}
			}
			return sdkErr(c.Close())
		}
		c, herr := sdk(op.NetworkEnrichmentV1CursorV6(direction))
		if herr != nil {
			return viewError(herr)
		}
		if start != nil {
			if herr := sdkErr(c.Seek(*start.V6)); herr != nil {
				_ = c.Close()
				return herr
			}
		}
		return sdkErr(c.Close())
	default:
		if info.Family == iprangedb.AddressFamilyIPv4 {
			_, herr := sdk(op.FeedRangeCursorV4(view.FeedName, direction))
			if herr != nil {
				return viewError(herr)
			}
			return nil
		}
		_, herr := sdk(op.FeedRangeCursorV6(view.FeedName, direction))
		if herr != nil {
			return viewError(herr)
		}
		return nil
	}
}

// ensureCursorCapacity enforces the per-connection cursor bound.
func ensureCursorCapacity(st *rpc.SessionState) *rpc.HandlerError {
	if len(st.Resources.Cursors) >= CursorLimit {
		return rpc.NewHandlerError("server_busy", "not_started",
			"connection cursor limit 64 is exhausted")
	}
	return nil
}

// insertCursor allocates a fresh non-colliding handle and registers one
// cursor checkpoint.
func insertCursor(st *rpc.SessionState, reader string, kind rpc.CursorKind, view rpc.CursorView, reverse bool, point *rpc.CursorPoint, batch int) (string, *rpc.HandlerError) {
	if herr := ensureCursorCapacity(st); herr != nil {
		return "", herr
	}
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return "", herr
	}
	for st.Resources.Cursors[handle] != nil || st.Resources.ClosedCursors[handle] ||
		st.Resources.Readers[handle] != nil || st.Resources.ClosedReaders[handle] {
		handle, herr = rpc.NewHandle()
		if herr != nil {
			return "", herr
		}
	}
	st.Resources.Cursors[handle] = &rpc.CursorValue{
		Kind:      kind,
		Reader:    reader,
		View:      view,
		Reverse:   reverse,
		Point:     point,
		BatchSize: batch,
	}
	return handle, nil
}

// closeCursor removes one cursor and keeps its closed tombstone.
func closeCursor(st *rpc.SessionState, handle string) {
	if _, ok := st.Resources.Cursors[handle]; ok {
		delete(st.Resources.Cursors, handle)
		st.Resources.RecordClosedCursor(handle)
	}
}

// closeCursorHandler implements both cursor-family close methods: a
// handle of the other family is handle_wrong_kind; an already closed
// handle is cursor_closed; an unknown handle is cursor_not_found.
func closeCursorHandler(st *rpc.SessionState, params json.RawMessage, method string, expected rpc.CursorKind) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "cursor")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	if st.Resources.ClosedCursors[handle] {
		return nil, rpc.NewHandlerError("cursor_closed", "not_started", "cursor is already closed")
	}
	cursor := st.Resources.Cursors[handle]
	if cursor == nil {
		return nil, rpc.NewHandlerError("cursor_not_found", "not_started", "cursor is unknown")
	}
	if cursor.Kind != expected {
		return nil, wrongCursorKind()
	}
	delete(st.Resources.Cursors, handle)
	st.Resources.RecordClosedCursor(handle)
	return boundedResult(map[string]any{"method": method, "closed": true})
}

func closedOrUnknownCursor(st *rpc.SessionState, handle string) *rpc.HandlerError {
	if st.Resources.ClosedCursors[handle] {
		return rpc.NewHandlerError("cursor_closed", "not_started", "cursor is already closed")
	}
	return rpc.NewHandlerError("cursor_not_found", "not_started", "cursor is unknown")
}

func wrongCursorKind() *rpc.HandlerError {
	return wrongView("cursor handle belongs to the other cursor family")
}

func wrongView(message string) *rpc.HandlerError {
	return rpc.NewHandlerError("handle_wrong_kind", "not_started", message)
}

// viewError maps wrong-kind SDK view refusals to the stable adapter
// handle_wrong_kind (Rust cursors.rs view_error parity); a nil error
// passes through for the unconditionally mapped call sites.
func viewError(herr *rpc.HandlerError) *rpc.HandlerError {
	if herr == nil {
		return nil
	}
	if herr.Code == "wrong_value_kind" || herr.Code == "wrong_structure_kind" {
		return wrongView("reader does not support the requested cursor view")
	}
	return herr
}

func wrongFamily() *rpc.HandlerError {
	return rpc.NewHandlerError("wrong_address_family", "read_only_failure",
		"cursor checkpoint does not match the reader family")
}

func cursorLimitError() *rpc.HandlerError {
	return rpc.NewHandlerError("output_limit", "read_only_failure",
		"cursor response exceeds the 65000-byte object limit")
}

// familyMatches reports whether one checkpoint belongs to the reader
// family (nil checkpoints match any family).
func familyMatches(point *rpc.CursorPoint, family iprangedb.AddressFamily) bool {
	if point == nil {
		return true
	}
	if point != nil && point.V4 != nil {
		return family == iprangedb.AddressFamilyIPv4
	}
	return family == iprangedb.AddressFamilyIPv6
}

func rangeDirection(reverse bool) iprangedb.RangeDirection {
	if reverse {
		return iprangedb.RangeDirectionBackward
	}
	return iprangedb.RangeDirectionForward
}

// nextV4Point is the checkpoint following one IPv4 record: exclusive
// to+1 forward, from-1 backward, nil at the family edge.
func nextV4Point(from, to uint32, reverse bool) *rpc.CursorPoint {
	if reverse {
		if from == 0 {
			return nil
		}
		value := from - 1
		return &rpc.CursorPoint{V4: &value}
	}
	if to == ^uint32(0) {
		return nil
	}
	value := to + 1
	return &rpc.CursorPoint{V4: &value}
}

// nextV6Point is the checkpoint following one IPv6 record, using the
// 128-bit numeric successor/predecessor (nil at the family edges).
func nextV6Point(fromHi, fromLo, toHi, toLo uint64, reverse bool) *rpc.CursorPoint {
	if reverse {
		if fromHi == 0 && fromLo == 0 {
			return nil
		}
		lo, borrow := bits.Sub64(fromLo, 1, 0)
		value := iprangedb.IPv6FromHalves(fromHi-borrow, lo)
		return &rpc.CursorPoint{V6: &value}
	}
	if toHi == ^uint64(0) && toLo == ^uint64(0) {
		return nil
	}
	lo, carry := bits.Add64(toLo, 1, 0)
	value := iprangedb.IPv6FromHalves(toHi+carry, lo)
	return &rpc.CursorPoint{V6: &value}
}

// cursorBase is the byte size of the complete response object (jsonrpc,
// echoed id, method, empty rows, done) for one cursor page; pages are
// sized against this real envelope (Rust cursors.rs cursor_base parity).
func cursorBase(state *rpc.SessionState, method, field string) (int, *rpc.HandlerError) {
	var id any
	if state.ActiveRequestID != nil {
		id = json.RawMessage(state.ActiveRequestID.AsJSON())
	} else {
		id = nil
	}
	result := map[string]any{"method": method, field: []any{}, "done": false}
	envelope := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	text, serr := rpc.EncodeResponseObjectProbe(envelope)
	if serr != nil {
		return 0, cursorLimitError()
	}
	return len(text), nil
}

// fitsNextItem reports whether one more row fits the response-object
// ceiling (one comma per existing row plus the row text; the closing
// bracket is already part of the base).
func fitsNextItem(base int, items []any, item any) bool {
	comma := 0
	if len(items) > 0 {
		comma = 1
	}
	text, err := json.Marshal(item)
	if err != nil {
		return false
	}
	return base+comma+len(text) <= rpc.ResponseObjectLimit
}

// itemSize is the byte cost of appending one row to the page array.
func itemSize(items []any, item any) int {
	comma := 0
	if len(items) > 0 {
		comma = 1
	}
	text, err := json.Marshal(item)
	if err != nil {
		return 0
	}
	return comma + len(text)
}

func addressV4(value uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", value>>24, (value>>16)&0xff, (value>>8)&0xff, value&0xff)
}

func addressV6(hi, lo uint64) string {
	v6 := iprangedb.IPv6FromHalves(hi, lo)
	return CursorAddressV6(&v6)
}
