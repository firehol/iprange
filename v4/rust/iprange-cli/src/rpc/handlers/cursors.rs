//! Bounded feed and range cursor JSON-RPC handlers.

use iprange_livedb::{
    AddressFamily, CancellationToken, DatabaseInfo, FeedName, Ipv4Key, Ipv6Key, RangeDirection,
    ValueKind,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::framing::RESPONSE_OBJECT_LIMIT;
use super::super::schema::RequestId;
use super::super::session::SessionState;
use super::super::state::{CursorKind, CursorPoint, CursorValue, CursorView, ReaderValue};
use super::reader::{bounded_result, exact_object, exact_object_opt, parse_address, sdk, validate_handle};

pub const CURSOR_LIMIT: usize = 64;

pub fn validate_cursor(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["cursor"])?;
    validate_handle(object["cursor"].as_str())
}

pub fn validate_feeds_open(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["reader", "batch_size"])?;
    validate_handle(object["reader"].as_str())?;
    validate_batch(object["batch_size"].as_u64())
}

pub fn validate_ranges_open(params: &Value) -> Result<(), String> {
    let object = exact_object_opt(
        params,
        &["reader", "view", "direction", "batch_size"],
        &["start"],
    )?;
    validate_handle(object["reader"].as_str())?;
    validate_view(&object["view"])?;
    match object["direction"].as_str() {
        Some("forward") | Some("reverse") => {}
        _ => return Err("direction must be forward or reverse".into()),
    }
    match object.get("start") {
        None => {}
        Some(Value::String(address)) => {
            parse_address(address)?;
        }
        Some(_) => return Err("start must be a canonical IP address when present".into()),
    }
    validate_batch(object["batch_size"].as_u64())
}

fn validate_view(value: &Value) -> Result<(), String> {
    let view = value.as_object().ok_or("view must be an object")?;
    match view.get("kind").and_then(Value::as_str) {
        Some("direct") | Some("structured") => {
            if view.len() != 1 {
                return Err("direct and structured views accept only kind".into());
            }
        }
        Some("feed") => {
            if view.len() != 2 || !view.contains_key("feed") {
                return Err("feed view requires exactly kind and feed".into());
            }
            FeedName::new(view["feed"].as_str().ok_or("view.feed must be a string")?)
                .map_err(|error| error.to_string())?;
        }
        _ => return Err("view.kind must be direct, structured, or feed".into()),
    }
    Ok(())
}

pub fn feeds_open(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let reader_handle = object["reader"]
        .as_str()
        .expect("validator checked handle")
        .to_owned();
    let batch = object["batch_size"]
        .as_u64()
        .expect("validator checked batch") as usize;
    ensure_cursor_capacity(state)?;
    let reader = super::reader::reader(state, &reader_handle)?;
    if sdk(reader.info())?.value_kind == ValueKind::Direct {
        return Err(wrong_view(
            "feed enumeration requires a membership-capable database",
        ));
    }
    sdk(reader.feed_cursor()).map_err(view_error)?;
    let view = CursorView::Feed {
        name: String::new(),
    };
    let cursor = insert_cursor(
        state,
        reader_handle,
        CursorKind::Feeds,
        view,
        false,
        None,
        batch,
    )?;
    bounded_result(json!({
        "method": "iprange.v1.reader.feeds.open",
        "cursor": cursor,
    }))
}

pub fn feeds_next(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let handle = cursor_handle(&params).map_err(HandlerError::invalid_params)?;
    let Some(cursor) = state.resources.cursors.get(&handle).cloned() else {
        return Err(closed_or_unknown_cursor(state, &handle));
    };
    if cursor.kind != CursorKind::Feeds {
        return Err(wrong_cursor_kind());
    }
    let mut encoded = cursor_base(state, "iprange.v1.reader.feeds.next", "feeds")?;
    let reader = super::reader::reader(state, &cursor.reader)?;
    let mut catalog = sdk(reader.feed_cursor())?;
    let mut rows = Vec::new();
    let mut last = cursor.last_feed_index;
    let mut done = false;
    while rows.len() < cursor.batch_size {
        let Some(entry) = sdk(catalog.next_feed())? else {
            done = true;
            break;
        };
        if last.is_some_and(|index| entry.index <= index) {
            continue;
        }
        let row = json!({"name": entry.name.as_str()});
        if !fits_next_item(encoded, &rows, &row) {
            if rows.is_empty() {
                close_cursor(state, &handle);
                return Err(limit_error());
            }
            break;
        }
        encoded += item_size(&rows, &row);
        rows.push(row);
        last = Some(entry.index);
    }
    let result = json!({
        "method": "iprange.v1.reader.feeds.next",
        "feeds": rows,
        "done": done,
    });
    finish_cursor_result(state, &handle, result, done, |active| {
        active.last_feed_index = last;
    })
}

pub fn feeds_close(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    close(
        state,
        &params,
        "iprange.v1.reader.feeds.close",
        CursorKind::Feeds,
    )
}

pub fn ranges_open(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let reader_handle = object["reader"]
        .as_str()
        .expect("validator checked handle")
        .to_owned();
    let batch = object["batch_size"]
        .as_u64()
        .expect("validator checked batch") as usize;
    let view_object = object["view"].as_object().expect("validator checked view");
    let kind = view_object["kind"]
        .as_str()
        .expect("validator checked kind");
    let feed = view_object
        .get("feed")
        .and_then(Value::as_str)
        .map(str::to_owned);
    let reverse = object["direction"].as_str() == Some("reverse");
    let start = match object.get("start") {
        None => None,
        Some(value) => Some(
            parse_address(value.as_str().expect("validator checked address"))
                .map_err(HandlerError::invalid_params)?,
        ),
    };
    ensure_cursor_capacity(state)?;
    let reader = super::reader::reader(state, &reader_handle)?;
    let view = match (kind, feed.as_deref()) {
        ("direct", None) => CursorView::Direct,
        ("structured", None) => CursorView::Structured,
        ("feed", Some(name)) => CursorView::Feed {
            name: name.to_owned(),
        },
        _ => {
            return Err(HandlerError::invalid_params(
                "view members do not match kind",
            ))
        }
    };
    if start.is_some() && matches!(view, CursorView::Feed { .. }) {
        return Err(HandlerError::invalid_params(
            "start is valid only for direct or structured views",
        ));
    }
    open_and_seek(reader, &view, reverse, start)?;
    let cursor = insert_cursor(
        state,
        reader_handle,
        CursorKind::Ranges,
        view,
        reverse,
        start,
        batch,
    )?;
    bounded_result(json!({
        "method": "iprange.v1.reader.ranges.open",
        "cursor": cursor,
    }))
}

pub fn ranges_next(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let handle = cursor_handle(&params).map_err(HandlerError::invalid_params)?;
    let Some(cursor) = state.resources.cursors.get(&handle).cloned() else {
        return Err(closed_or_unknown_cursor(state, &handle));
    };
    if cursor.kind != CursorKind::Ranges {
        return Err(wrong_cursor_kind());
    }
    if cursor.exhausted {
        close_cursor(state, &handle);
        return bounded_result(json!({
            "method": "iprange.v1.reader.ranges.next",
            "records": [],
            "done": true,
        }));
    }
    let mut encoded = cursor_base(state, "iprange.v1.reader.ranges.next", "records")?;
    // Captured before the reader borrow so the page loop can poll the
    // transport cancellation token while the cursor holds the reader.
    let cancellation = state.token.clone();
    let reader = super::reader::reader(state, &cursor.reader)?;
    let info = sdk(reader.info())?;
    // Feed views keep one cursor open for the whole page: the cursor is
    // seeked to the checkpoint once (O(log n)) and iterated, instead of
    // reopening and skipping from the start for every record (O(n^2)
    // over the whole stream). The page cursor borrows the reader, so
    // each next call re-opens and seeks; the checkpoint below makes the
    // re-open exact and bounded.
    let mut feed = match &cursor.view {
        CursorView::Feed { name } => Some(open_feed_page(
            reader,
            info,
            name,
            range_direction(cursor.reverse),
            cursor.point,
        )?),
        _ => None,
    };
    let mut point = cursor.point;
    let mut records = Vec::new();
    let mut exhausted = false;
    while records.len() < cursor.batch_size {
        check_cancelled(&cancellation)?;
        let before_point = point;
        if !next_record(
            reader,
            info,
            &cursor.view,
            cursor.reverse,
            &mut point,
            &mut records,
            &mut feed,
        )? {
            exhausted = true;
            break;
        }
        let Some(record) = records.last() else {
            break;
        };
        if !fits_next_item(encoded, &records, record) {
            records.pop();
            point = before_point;
            if records.is_empty() {
                close_cursor(state, &handle);
                return Err(limit_error());
            }
            break;
        }
        let size = item_size(&records, record);
        encoded += size;
        if point.is_none() {
            exhausted = true;
            break;
        }
    }
    let result = json!({
        "method": "iprange.v1.reader.ranges.next",
        "records": records,
        "done": exhausted,
    });
    finish_cursor_result(state, &handle, result, exhausted, |active| {
        active.point = point;
    })
}

pub fn ranges_close(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    close(
        state,
        &params,
        "iprange.v1.reader.ranges.close",
        CursorKind::Ranges,
    )
}

fn next_record(
    reader: &ReaderValue,
    info: DatabaseInfo,
    view: &CursorView,
    reverse: bool,
    point: &mut Option<CursorPoint>,
    records: &mut Vec<Value>,
    feed: &mut Option<PageFeedCursor<'_>>,
) -> Result<bool, HandlerError> {
    let direction = range_direction(reverse);
    let family_matches = matches!(
        (*point, info.address_family),
        (None, _)
            | (Some(CursorPoint::V4(_)), AddressFamily::Ipv4)
            | (Some(CursorPoint::V6(_)), AddressFamily::Ipv6)
    );
    if !family_matches {
        return Err(HandlerError::new(
            "wrong_address_family",
            "read_only_failure",
            "cursor checkpoint does not match the reader family",
        ));
    }
    let v4 = info.address_family == AddressFamily::Ipv4;
    match (view, v4) {
        (CursorView::Direct, true) => {
            let mut cursor = sdk(reader.direct_cursor_v4(direction)).map_err(view_error)?;
            seek_direct_v4(&mut cursor, *point)?;
            let Some(range) = sdk(cursor.next_range())? else {
                return Ok(false);
            };
            push_v4(
                records,
                range.from,
                range.to,
                Some(json!(range.value)),
                reverse,
                point,
            );
        }
        (CursorView::Direct, false) => {
            let mut cursor = sdk(reader.direct_cursor_v6(direction)).map_err(view_error)?;
            seek_direct_v6(&mut cursor, *point)?;
            let Some(range) = sdk(cursor.next_range())? else {
                return Ok(false);
            };
            push_v6(
                records,
                range.from,
                range.to,
                Some(json!(range.value)),
                reverse,
                point,
            );
        }
        (CursorView::Structured, true) => {
            let mut cursor =
                sdk(reader.network_enrichment_v1_cursor_v4(direction)).map_err(view_error)?;
            seek_structured_v4(&mut cursor, *point)?;
            let Some(range) = sdk(cursor.next_range())? else {
                return Ok(false);
            };
            let feeds = super::reader::threat_feed_names(reader, &range.value)?;
            let value = super::convert::enrichment_view(&range.value, &feeds);
            push_v4(records, range.from, range.to, Some(value), reverse, point);
        }
        (CursorView::Structured, false) => {
            let mut cursor =
                sdk(reader.network_enrichment_v1_cursor_v6(direction)).map_err(view_error)?;
            seek_structured_v6(&mut cursor, *point)?;
            let Some(range) = sdk(cursor.next_range())? else {
                return Ok(false);
            };
            let feeds = super::reader::threat_feed_names(reader, &range.value)?;
            let value = super::convert::enrichment_view(&range.value, &feeds);
            push_v6(records, range.from, range.to, Some(value), reverse, point);
        }
        (CursorView::Feed { .. }, true) => {
            let Some(range) = next_feed_page_v4(feed)? else {
                return Ok(false);
            };
            push_v4(records, range.from, range.to, None, reverse, point);
        }
        (CursorView::Feed { .. }, false) => {
            let Some(range) = next_feed_page_v6(feed)? else {
                return Ok(false);
            };
            push_v6(records, range.from, range.to, None, reverse, point);
        }
    }
    Ok(true)
}

fn push_v4(
    records: &mut Vec<Value>,
    from: Ipv4Key,
    to: Ipv4Key,
    value: Option<Value>,
    reverse: bool,
    point: &mut Option<CursorPoint>,
) {
    let mut record = json!({
        "from": super::convert::cursor_address(CursorPoint::V4(from.0)),
        "to": super::convert::cursor_address(CursorPoint::V4(to.0)),
    });
    if let Some(value) = value {
        record["value"] = value;
    }
    records.push(record);
    *point = if reverse {
        if from.0 == 0 {
            None
        } else {
            Some(CursorPoint::V4(from.0 - 1))
        }
    } else if to.0 == u32::MAX {
        None
    } else {
        Some(CursorPoint::V4(to.0 + 1))
    };
}

fn push_v6(
    records: &mut Vec<Value>,
    from: Ipv6Key,
    to: Ipv6Key,
    value: Option<Value>,
    reverse: bool,
    point: &mut Option<CursorPoint>,
) {
    let mut record = json!({
        "from": super::convert::cursor_address(CursorPoint::V6(from.to_u128())),
        "to": super::convert::cursor_address(CursorPoint::V6(to.to_u128())),
    });
    if let Some(value) = value {
        record["value"] = value;
    }
    records.push(record);
    let value = if reverse {
        from.to_u128().checked_sub(1)
    } else {
        to.to_u128().checked_add(1)
    };
    *point = value.map(CursorPoint::V6);
}

fn open_and_seek(
    reader: &ReaderValue,
    view: &CursorView,
    reverse: bool,
    start: Option<CursorPoint>,
) -> Result<(), HandlerError> {
    let family = sdk(reader.info())?.address_family;
    let direction = range_direction(reverse);
    let family_matches = |point: Option<CursorPoint>| {
        matches!(
            (point, family),
            (None, _)
                | (Some(CursorPoint::V4(_)), AddressFamily::Ipv4)
                | (Some(CursorPoint::V6(_)), AddressFamily::Ipv6)
        )
    };
    if !family_matches(start) {
        return Err(wrong_family());
    }
    match view {
        CursorView::Direct => match start {
            Some(CursorPoint::V4(value)) => {
                let mut cursor = sdk(reader.direct_cursor_v4(direction)).map_err(view_error)?;
                sdk(cursor.seek(Ipv4Key(value)))
            }
            Some(CursorPoint::V6(value)) => {
                let mut cursor = sdk(reader.direct_cursor_v6(direction)).map_err(view_error)?;
                sdk(cursor.seek(Ipv6Key::from_u128(value)))
            }
            None if family == AddressFamily::Ipv4 => {
                sdk(reader.direct_cursor_v4(direction)).map(|_| ())
            }
            None => sdk(reader.direct_cursor_v6(direction)).map(|_| ()),
        },
        CursorView::Structured => match start {
            Some(CursorPoint::V4(value)) => {
                let mut cursor =
                    sdk(reader.network_enrichment_v1_cursor_v4(direction)).map_err(view_error)?;
                sdk(cursor.seek(Ipv4Key(value)))
            }
            Some(CursorPoint::V6(value)) => {
                let mut cursor =
                    sdk(reader.network_enrichment_v1_cursor_v6(direction)).map_err(view_error)?;
                sdk(cursor.seek(Ipv6Key::from_u128(value)))
            }
            None if family == AddressFamily::Ipv4 => {
                sdk(reader.network_enrichment_v1_cursor_v4(direction))
                    .map(|_| ())
                    .map_err(view_error)
            }
            None => sdk(reader.network_enrichment_v1_cursor_v6(direction))
                .map(|_| ())
                .map_err(view_error),
        },
        CursorView::Feed { name } => match start {
            Some(_) => Err(wrong_view("feed views do not accept start")),
            None if family == AddressFamily::Ipv4 => {
                sdk(reader.feed_range_cursor_v4(name, direction))
                    .map(|_| ())
                    .map_err(view_error)
            }
            None => sdk(reader.feed_range_cursor_v6(name, direction))
                .map(|_| ())
                .map_err(view_error),
        },
    }
}

fn seek_direct_v4(
    cursor: &mut iprange_livedb::DirectCursorV4<'_>,
    point: Option<CursorPoint>,
) -> Result<(), HandlerError> {
    match point {
        Some(CursorPoint::V4(value)) => sdk(cursor.seek(Ipv4Key(value))),
        Some(CursorPoint::V6(_)) => Err(wrong_family()),
        None => Ok(()),
    }
}

fn seek_direct_v6(
    cursor: &mut iprange_livedb::DirectCursorV6<'_>,
    point: Option<CursorPoint>,
) -> Result<(), HandlerError> {
    match point {
        Some(CursorPoint::V4(_)) => Err(wrong_family()),
        Some(CursorPoint::V6(value)) => sdk(cursor.seek(Ipv6Key::from_u128(value))),
        None => Ok(()),
    }
}

fn seek_structured_v4(
    cursor: &mut iprange_livedb::NetworkEnrichmentV1CursorV4<'_>,
    point: Option<CursorPoint>,
) -> Result<(), HandlerError> {
    match point {
        Some(CursorPoint::V4(value)) => sdk(cursor.seek(Ipv4Key(value))),
        Some(CursorPoint::V6(_)) => Err(wrong_family()),
        None => Ok(()),
    }
}

/// One feed cursor held open for one `ranges.next` page. Feed views
/// reopen and seek the underlying SDK cursor once per page; the borrow
/// of the reader ends when the page completes.
enum PageFeedCursor<'a> {
    V4(iprange_livedb::FeedRangeCursorV4<'a>),
    V6(iprange_livedb::FeedRangeCursorV6<'a>),
}

fn open_feed_page<'a>(
    reader: &'a ReaderValue,
    info: DatabaseInfo,
    name: &str,
    direction: RangeDirection,
    point: Option<CursorPoint>,
) -> Result<PageFeedCursor<'a>, HandlerError> {
    if !matches!(
        (point, info.address_family),
        (None, _)
            | (Some(CursorPoint::V4(_)), AddressFamily::Ipv4)
            | (Some(CursorPoint::V6(_)), AddressFamily::Ipv6)
    ) {
        return Err(wrong_family());
    }
    let cursor = match info.address_family {
        AddressFamily::Ipv4 => {
            let mut cursor =
                sdk(reader.feed_range_cursor_v4(name, direction)).map_err(view_error)?;
            if let Some(CursorPoint::V4(value)) = point {
                sdk(cursor.seek(Ipv4Key(value)))?
            }
            PageFeedCursor::V4(cursor)
        }
        AddressFamily::Ipv6 => {
            let mut cursor =
                sdk(reader.feed_range_cursor_v6(name, direction)).map_err(view_error)?;
            if let Some(CursorPoint::V6(value)) = point {
                sdk(cursor.seek(Ipv6Key::from_u128(value)))?
            }
            PageFeedCursor::V6(cursor)
        }
    };
    Ok(cursor)
}

fn next_feed_page_v4(
    feed: &mut Option<PageFeedCursor<'_>>,
) -> Result<Option<iprange_livedb::AddressRange<Ipv4Key>>, HandlerError> {
    match feed.as_mut() {
        Some(PageFeedCursor::V4(cursor)) => sdk(cursor.next_range()),
        _ => Err(HandlerError::new(
            "handle_wrong_kind",
            "not_started",
            "feed page cursor does not match the reader family",
        )),
    }
}

fn next_feed_page_v6(
    feed: &mut Option<PageFeedCursor<'_>>,
) -> Result<Option<iprange_livedb::AddressRange<Ipv6Key>>, HandlerError> {
    match feed.as_mut() {
        Some(PageFeedCursor::V6(cursor)) => sdk(cursor.next_range()),
        _ => Err(HandlerError::new(
            "handle_wrong_kind",
            "not_started",
            "feed page cursor does not match the reader family",
        )),
    }
}

/// Stop a bounded cursor page factually when the transport cancelled
/// the active request between records.
fn check_cancelled(token: &CancellationToken) -> Result<(), HandlerError> {
    if token.is_cancelled() {
        return Err(HandlerError::new(
            "cancelled",
            "not_started",
            "cursor read was cancelled",
        ));
    }
    Ok(())
}

fn seek_structured_v6(
    cursor: &mut iprange_livedb::NetworkEnrichmentV1CursorV6<'_>,
    point: Option<CursorPoint>,
) -> Result<(), HandlerError> {
    match point {
        Some(CursorPoint::V4(_)) => Err(wrong_family()),
        Some(CursorPoint::V6(value)) => sdk(cursor.seek(Ipv6Key::from_u128(value))),
        None => Ok(()),
    }
}

fn wrong_family() -> HandlerError {
    HandlerError::new(
        "wrong_address_family",
        "read_only_failure",
        "cursor checkpoint does not match the reader family",
    )
}

fn finish_cursor_result(
    state: &mut SessionState,
    handle: &str,
    result: Value,
    done: bool,
    update: impl FnOnce(&mut CursorValue),
) -> Result<Value, HandlerError> {
    bounded_result(result.clone()).map_err(|error| {
        close_cursor(state, handle);
        error
    })?;
    if done {
        close_cursor(state, handle);
    } else if let Some(cursor) = state.resources.cursors.get_mut(handle) {
        update(cursor);
    }
    bounded_result(result)
}

fn close(
    state: &mut SessionState,
    params: &Value,
    method: &'static str,
    expected: CursorKind,
) -> Result<Value, HandlerError> {
    let handle = cursor_handle(params).map_err(HandlerError::invalid_params)?;
    if state.resources.closed_cursors.contains_key(&handle) {
        return Err(closed_error());
    }
    let Some(kind) = state
        .resources
        .cursors
        .get(&handle)
        .map(|cursor| cursor.kind)
    else {
        return Err(unknown_error());
    };
    if kind != expected {
        return Err(wrong_cursor_kind());
    }
    if state.resources.cursors.remove(&handle).is_none() {
        return Err(unknown_error());
    }
    state.resources.closed_cursors.insert(handle, ());
    bounded_result(json!({ "method": method, "closed": true }))
}

/// The byte size of the COMPLETE response object (jsonrpc, echoed id,
/// and the result skeleton) for one feeds/ranges.next response. The
/// session exposes the active request id so pages are sized against the
/// real envelope; an unencodable id makes the base exceed the ceiling, in
/// which case the first page is refused with output_limit like any other
/// unencodable row.
/// Envelope base for cursor pages: complete response object with the
/// active request id echoed. The id is captured before any state borrow.
fn cursor_base(state: &SessionState, method: &str, field: &str) -> Result<usize, HandlerError> {
    response_base(state, method, field).map_err(|()| limit_error())
}

fn response_base(
    state: &SessionState,
    method: &str,
    field: &str,
) -> Result<usize, ()> {
    let id = state
        .active_request_id
        .as_ref()
        .map(RequestId::as_json)
        .unwrap_or(Value::Null);
    let mut result = serde_json::Map::new();
    result.insert("method".into(), Value::String(method.into()));
    result.insert(field.into(), Value::Array(Vec::new()));
    result.insert("done".into(), Value::Bool(false));
    let complete = json!({
        "jsonrpc": "2.0",
        "id": id,
        "result": result,
    });
    schema_complete_size(&complete).ok_or(())
}

fn schema_complete_size(complete: &Value) -> Option<usize> {
    super::super::schema::encode_response_object(complete).ok().map(|s| s.len())
}

fn fits_next_item(base: usize, items: &[Value], item: &Value) -> bool {
    // One comma per existing item plus the item text; the closing brace
    // is already included in the base.
    base + usize::from(!items.is_empty()) + item.to_string().len() <= RESPONSE_OBJECT_LIMIT
}

fn item_size(items: &[Value], item: &Value) -> usize {
    usize::from(!items.is_empty()) + item.to_string().len()
}

fn ensure_cursor_capacity(state: &SessionState) -> Result<(), HandlerError> {
    if state.resources.cursors.len() >= CURSOR_LIMIT {
        return Err(HandlerError::new(
            "server_busy",
            "not_started",
            "connection cursor limit 64 is exhausted",
        ));
    }
    Ok(())
}

fn insert_cursor(
    state: &mut SessionState,
    reader: String,
    kind: CursorKind,
    view: CursorView,
    reverse: bool,
    point: Option<CursorPoint>,
    batch_size: usize,
) -> Result<String, HandlerError> {
    if state.resources.cursors.len() >= CURSOR_LIMIT {
        return Err(HandlerError::new(
            "server_busy",
            "not_started",
            "connection cursor limit 64 is exhausted",
        ));
    }
    let mut handle = super::reader::random_handle()?;
    while state.resources.cursors.contains_key(&handle)
        || state.resources.closed_cursors.contains_key(&handle)
        || state.resources.readers.contains_key(&handle)
        || state.resources.closed_readers.contains_key(&handle)
    {
        handle = super::reader::random_handle()?;
    }
    state.resources.cursors.insert(
        handle.clone(),
        CursorValue {
            kind,
            reader,
            view,
            reverse,
            point,
            last_feed_index: None,
            batch_size,
            exhausted: false,
        },
    );
    Ok(handle)
}

fn close_cursor(state: &mut SessionState, handle: &str) {
    if state.resources.cursors.remove(handle).is_some() {
        state.resources.closed_cursors.insert(handle.to_owned(), ());
    }
}

fn cursor_handle(params: &Value) -> Result<String, String> {
    let object = params.as_object().ok_or("params must be an object")?;
    let handle = object
        .get("cursor")
        .and_then(Value::as_str)
        .ok_or("cursor must be a string")?;
    validate_handle(Some(handle))?;
    Ok(handle.to_owned())
}

fn closed_or_unknown_cursor(state: &SessionState, handle: &str) -> HandlerError {
    if state.resources.closed_cursors.contains_key(handle) {
        closed_error()
    } else {
        unknown_error()
    }
}

fn limit_error() -> HandlerError {
    HandlerError::new(
        "output_limit",
        "read_only_failure",
        "cursor response exceeds the 65000-byte object limit",
    )
}

fn closed_error() -> HandlerError {
    HandlerError::new("cursor_closed", "not_started", "cursor is already closed")
}

fn unknown_error() -> HandlerError {
    HandlerError::new("cursor_not_found", "not_started", "cursor is unknown")
}

fn wrong_view(message: &'static str) -> HandlerError {
    HandlerError::new("handle_wrong_kind", "not_started", message)
}

fn wrong_cursor_kind() -> HandlerError {
    wrong_view("cursor handle belongs to the other cursor family")
}

fn view_error(error: HandlerError) -> HandlerError {
    if error.code == "wrong_value_kind" || error.code == "wrong_structure_kind" {
        wrong_view("reader does not support the requested cursor view")
    } else {
        error
    }
}

fn validate_batch(value: Option<u64>) -> Result<(), String> {
    let Some(value) = value else {
        return Err("batch_size must be a JSON integer from 1 through 4096".into());
    };
    if value == 0 || value > 4096 {
        return Err("batch_size must be from 1 through 4096".into());
    }
    Ok(())
}

fn range_direction(reverse: bool) -> RangeDirection {
    if reverse {
        RangeDirection::Backward
    } else {
        RangeDirection::Forward
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::handlers::reader;
    use iprange_livedb::{
        create_immutable_feed_v4, AddressRange, FeedName, ImmutableFeedBudget, PublicationPolicy,
        SliceSource, ValueTag,
    };
    use std::fs;
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn immutable_membership(label: &str) -> PathBuf {
        immutable_membership_ranges(label, &[AddressRange {
            from: Ipv4Key(1),
            to: Ipv4Key(9),
        }])
    }

    fn immutable_membership_ranges(label: &str, ranges: &[AddressRange<Ipv4Key>]) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-cursor-{label}-{}-{unique}",
            std::process::id()
        ));
        create_immutable_feed_v4(
            &path,
            ValueTag::new(b"feeds").unwrap(),
            FeedName::new("feed-a").unwrap(),
            None,
            PublicationPolicy::FailIfExists,
            &mut SliceSource::new(ranges),
            &ImmutableFeedBudget::new(2 * 1024 * 1024, 10_000, 10_000, 3),
            &iprange_livedb::CancellationToken::new(),
        )
        .unwrap();
        path
    }

    fn open_immutable_feed(state: &mut SessionState, path: &PathBuf) -> String {
        let opened = reader::open(
            state,
            serde_json::json!({
                "source":{"path":path.display().to_string(),"mode":"immutable"}
            }),
        )
        .unwrap();
        opened["reader"].as_str().unwrap().to_owned()
    }

    /// Collect every record of one feed view cursor until `done`,
    /// using the given batch size, as the exact JSON wire values.
    fn collect_feed_pages(
        state: &mut SessionState,
        reader_handle: &str,
        direction: &str,
        batch_size: u64,
    ) -> Vec<Value> {
        let opened = ranges_open(
            state,
            serde_json::json!({
                "reader": reader_handle,
                "view": {"kind":"feed", "feed":"feed-a"},
                "direction": direction,
                "batch_size": batch_size,
            }),
        )
        .unwrap();
        let cursor = opened["cursor"].as_str().unwrap().to_owned();
        let mut records = Vec::new();
        loop {
            let next = ranges_next(state, serde_json::json!({"cursor": cursor})).unwrap();
            records.extend(next["records"].as_array().unwrap().iter().cloned());
            if next["done"].as_bool().unwrap() {
                break;
            }
        }
        records
    }

    #[test]
    fn feed_paging_matches_one_unbounded_page() {
        // Thousands of canonical ranges with real gaps. The old paging
        // implementation reopened the feed cursor and skipped from the
        // start for every emitted record; the seek-based paging must
        // return byte-identical records no matter how pages fall.
        let ranges: Vec<AddressRange<Ipv4Key>> = (0..5000u32)
            .map(|i| {
                let from = 2 + i * 4;
                AddressRange {
                    from: Ipv4Key(from),
                    to: Ipv4Key(from + u32::from(i % 3 == 0) + 1),
                }
            })
            .collect();
        let path = immutable_membership_ranges("feed-paging", &ranges);
        for direction in ["forward", "reverse"] {
            let mut state = SessionState::default();
            let reader_handle = open_immutable_feed(&mut state, &path);
            let unbounded = collect_feed_pages(&mut state, &reader_handle, direction, 4096);
            let paged = collect_feed_pages(&mut state, &reader_handle, direction, 7);
            assert!(
                unbounded.len() > 1000,
                "expected a large feed stream, got {} records",
                unbounded.len()
            );
            assert_eq!(paged.len(), unbounded.len());
            assert_eq!(
                serde_json::to_string(&paged).unwrap(),
                serde_json::to_string(&unbounded).unwrap(),
                "{direction} paging diverged from the unbounded stream"
            );
            reader::close(&mut state, serde_json::json!({"reader": reader_handle})).unwrap();
        }
        fs::remove_file(path).unwrap();
    }

    #[test]
    fn cancelled_page_stops_factually_and_keeps_the_cursor() {
        // A transport cancel between records stops the page with the
        // documented `cancelled` product error and leaves the cursor
        // checkpoint untouched, so a later page resumes exactly.
        let path = immutable_membership("cancel");
        let mut state = SessionState::default();
        let reader_handle = open_immutable_feed(&mut state, &path);
        let opened = ranges_open(
            &mut state,
            serde_json::json!({
                "reader": reader_handle,
                "view": {"kind":"feed", "feed":"feed-a"},
                "direction":"forward",
                "batch_size":4096
            }),
        )
        .unwrap();
        let cursor = opened["cursor"].as_str().unwrap().to_owned();
        state.token.cancel();
        let error = ranges_next(&mut state, serde_json::json!({"cursor": cursor})).unwrap_err();
        assert_eq!((error.code, error.outcome), ("cancelled", "not_started"));
        // A fresh active unit replaces the token; the paused cursor
        // resumes from its stored checkpoint unchanged.
        state.token = std::sync::Arc::new(iprange_livedb::CancellationToken::new());
        let next = ranges_next(&mut state, serde_json::json!({"cursor": cursor})).unwrap();
        assert!(next["records"].as_array().unwrap().len() == 1);
        assert_eq!(next["records"][0]["from"], "0.0.0.1");
        reader::close(&mut state, serde_json::json!({"reader": reader_handle})).unwrap();
        fs::remove_file(path).unwrap();
    }

    #[test]
    fn large_cursor_page_sizes_against_the_complete_envelope() {
        // A page must be reduced against the full response object
        // (jsonrpc, echoed id, method, rows), not the bare result
        // skeleton; a valid large cursor must page, not output_limit.
        let fixture = reader::test_support::create_direct_v6("cursor");
        let mut state = SessionState::default();
        state.active_request_id = Some(RequestId::String("cursor-large-page".into()));
        let opened =
            reader::open(&mut state, reader::test_support::live_source(&fixture.path)).unwrap();
        let reader_handle = opened["reader"].as_str().unwrap().to_owned();
        let opened = ranges_open(
            &mut state,
            serde_json::json!({
                "reader": reader_handle,
                "view": {"kind":"direct"},
                "direction":"forward",
                "batch_size":4096
            }),
        )
        .unwrap();
        let cursor = opened["cursor"].as_str().unwrap().to_owned();
        // The envelope-aware base leaves room for rows; the oversized
        // fallback would have returned output_limit instead.
        let next = ranges_next(&mut state, serde_json::json!({"cursor": cursor})).unwrap();
        assert_eq!(next["records"].as_array().unwrap().len(), 1);
        assert_eq!(next["done"], true);
        reader::close(&mut state, serde_json::json!({"reader": reader_handle})).unwrap();
        fixture.remove();
    }

    #[test]
    fn ipv6_direct_cursor_without_start_uses_v6_preflight() {
        let fixture = reader::test_support::create_direct_v6("cursor");
        let mut state = SessionState::default();
        let opened =
            reader::open(&mut state, reader::test_support::live_source(&fixture.path)).unwrap();
        let reader_handle = opened["reader"].as_str().unwrap().to_owned();
        let opened = ranges_open(
            &mut state,
            serde_json::json!({
                "reader": reader_handle,
                "view": {"kind":"direct"},
                "direction":"forward",
                "batch_size":4
            }),
        )
        .unwrap();
        let cursor = opened["cursor"].as_str().unwrap().to_owned();
        let next = ranges_next(&mut state, serde_json::json!({"cursor": cursor})).unwrap();
        assert_eq!(next["records"][0]["from"], "2001:db8::1");
        assert_eq!(next["records"][0]["to"], "2001:db8::a");
        assert_eq!(next["records"][0]["value"], 7);
        reader::close(&mut state, serde_json::json!({"reader": reader_handle})).unwrap();
        fixture.remove();
    }

    #[test]
    fn cursor_family_rejects_the_wrong_next_method() {
        let path = immutable_membership("wrong-kind");
        let mut state = SessionState::default();
        let opened = reader::open(
            &mut state,
            serde_json::json!({
                "source":{"path":path.display().to_string(),"mode":"immutable"}
            }),
        )
        .unwrap();
        let reader_handle = opened["reader"].as_str().unwrap().to_owned();
        let feeds = feeds_open(
            &mut state,
            serde_json::json!({
                "reader":reader_handle,
                "batch_size":4
            }),
        )
        .unwrap();
        let cursor = feeds["cursor"].as_str().unwrap().to_owned();
        let wrong = ranges_next(&mut state, serde_json::json!({"cursor":cursor})).unwrap_err();
        assert_eq!(
            (wrong.code, wrong.outcome),
            ("handle_wrong_kind", "not_started")
        );
        let wrong_close =
            ranges_close(&mut state, serde_json::json!({"cursor":cursor})).unwrap_err();
        assert_eq!(
            (wrong_close.code, wrong_close.outcome),
            ("handle_wrong_kind", "not_started")
        );

        reader::close(&mut state, serde_json::json!({"reader":reader_handle})).unwrap();
        let cascade = feeds_next(&mut state, serde_json::json!({"cursor":cursor})).unwrap_err();
        assert_eq!(
            (cascade.code, cascade.outcome),
            ("cursor_closed", "not_started")
        );
        fs::remove_file(path).unwrap();
    }
}
