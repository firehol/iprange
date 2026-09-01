//! `iprange.v1.export`: canonical file export of one pinned v4 view.
//!
//! Source iteration is a bounded stream of canonical constant-value
//! segments over public SDK cursors. Flat set formats merge those
//! segments into maximal coverage; row formats keep their values. The
//! writer enforces the caller's row/byte budgets before each row, so
//! prefix or per-address expansion refuses instead of exploding.

use std::collections::BTreeSet;
use std::path::Path;

use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::recovery::{inspect_recovery_candidates, RecoveryInspectionMode};
use iprange_livedb::validation::{LocalFileIdentity, ValidationBudget};
use iprange_livedb::{
    AddressFamily, CancellationToken, FeedEntry, FeedName, ImmutableReader, LiveReader,
    RangeDirection,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::ReaderValue;
use super::reader::{
    bounded_result, member_object, positive_u32, positive_u64_string, publication_policy,
    read_error, sdk, u64_string, validate_path,
};
use crate::io::export_writer::{
    csv_field, emit_ipset, emit_netset, format_address, legacy_binary_header,
    legacy_binary_min_header_bytes, legacy_binary_record_v4, legacy_binary_record_v6, ranges_line,
    ExportBudget, ExportFacts, ExportWriter, PrefixFilter, LEGACY_ENDIANNESS_MARKER,
};

/// View selector decoded from the strict wire schema.
#[derive(Clone, Debug, PartialEq, Eq)]
enum ExportView {
    Direct,
    Structured,
    Feed {
        name: String,
    },
    /// `None` selects every feed; `Some` preserves caller names.
    Selection {
        named: Option<Vec<String>>,
    },
}

/// Semantic row value carried through the row format encoders.
#[derive(Clone, Debug, PartialEq)]
enum ExportValue {
    Direct(u32),
    Structured(Value),
    Feeds(Vec<String>),
}

type SegmentSink<'a> = dyn FnMut(u128, u128, &ExportValue) -> Result<(), HandlerError> + 'a;
type CoverageSink<'a> = dyn FnMut(u128, u128) -> Result<(), HandlerError> + 'a;

pub fn validate_export(params: &Value) -> Result<(), String> {
    let object = params.as_object().ok_or("params must be an object")?;
    for key in object.keys() {
        if !matches!(
            key.as_str(),
            "source"
                | "view"
                | "format"
                | "destination"
                | "publication_policy"
                | "min_prefix"
                | "prefixes"
                | "result_budget"
        ) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in [
        "source",
        "view",
        "format",
        "destination",
        "publication_policy",
        "result_budget",
    ] {
        if !object.contains_key(field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    validate_source(&object["source"])?;
    validate_view(&object["view"])?;
    let format = object["format"].as_str().ok_or("format must be a string")?;
    if !matches!(
        format,
        "netset" | "ipset" | "ranges" | "csv" | "jsonl" | "legacy_binary"
    ) {
        return Err("format must be netset, ipset, ranges, csv, jsonl, or legacy_binary".into());
    }
    validate_path(object["destination"].as_str())?;
    publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "publication_policy is invalid".to_string())?;
    validate_result_budget(&object["result_budget"])?;
    let minimum = object.get("min_prefix");
    let prefixes = object.get("prefixes");
    if minimum.is_some() && prefixes.is_some() {
        return Err("min_prefix and prefixes are mutually exclusive".into());
    }
    if format != "netset" && (minimum.is_some() || prefixes.is_some()) {
        return Err("min_prefix and prefixes apply only to netset format".into());
    }
    if let Some(minimum) = minimum {
        let minimum = u32_member(minimum).ok_or("min_prefix must be u32")?;
        if minimum > 128 {
            return Err("min_prefix must not exceed 128".into());
        }
    }
    if let Some(prefixes) = prefixes {
        let prefixes = prefixes.as_array().ok_or("prefixes must be an array")?;
        if prefixes.is_empty() {
            return Err("prefixes must contain at least one value".into());
        }
        let mut seen: Vec<u32> = Vec::new();
        for prefix in prefixes {
            let prefix = u32_member(prefix).ok_or("each prefix must be u32")?;
            if prefix > 128 {
                return Err("each prefix must not exceed 128".into());
            }
            if seen.contains(&prefix) {
                return Err("prefixes must be unique".into());
            }
            seen.push(prefix);
        }
    }
    Ok(())
}

pub fn export(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params.as_object().expect("validator checked object");
    let (source_path, source_mode) = source_value(&object["source"])?;
    match Path::new(&source_path).try_exists() {
        Ok(true) => {}
        Ok(false) => {
            return Err(HandlerError::new(
                "invalid_path",
                "not_started",
                format!("database source does not exist: {source_path}"),
            ));
        }
        Err(error) => {
            return Err(HandlerError::new(
                "io",
                "not_started",
                format!("cannot inspect database source {source_path}: {error}"),
            ));
        }
    }
    let view = decode_view(&object["view"])?;
    let format = object["format"].as_str().expect("validator checked format");
    let destination = Path::new(
        object["destination"]
            .as_str()
            .expect("validator checked path"),
    );
    let policy = publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
    let budget = decode_budget(&object["result_budget"])?;
    let mut reader = open_source(&source_path, &source_mode, &state.token)?;
    let export_result = export_with_reader(
        state,
        object,
        &view,
        format,
        destination,
        policy,
        &budget,
        &reader,
    );
    let close_result = close_ephemeral_source(&mut reader);
    let completed = export_result?;
    close_result?;
    bounded_result(json!({
        "method": "iprange.v1.export",
        "path": completed.facts.path,
        "format": format,
        "sha256": completed.facts.sha256,
        "rows": completed.facts.rows.to_string(),
        "addresses": completed.facts.addresses.to_string(),
        "bytes": completed.facts.bytes.to_string(),
        "identity": completed.identity,
    }))
}

fn open_source(
    path: &str,
    mode: &str,
    cancellation: &CancellationToken,
) -> Result<ReaderValue, HandlerError> {
    match mode {
        "immutable" => ImmutableReader::open(path)
            .map(ReaderValue::Immutable)
            .map_err(read_error),
        _ => LiveReader::open(path, cancellation)
            .map(ReaderValue::Live)
            .map_err(read_error),
    }
}

fn close_ephemeral_source(reader: &mut ReaderValue) -> Result<(), HandlerError> {
    let Some(result) = reader.close_live().map_err(read_error)? else {
        return Ok(());
    };
    if result.outcome != iprange_livedb::CloseOutcome::Closed || result.cause.is_some() {
        let code = result
            .cause
            .as_ref()
            .map(|error| super::reader::sdk_code(error.code()))
            .unwrap_or("io");
        return Err(HandlerError {
            code,
            outcome: "read_only_failure",
            message: result.cause.as_ref().map_or_else(
                || "live export reader close is incomplete".to_owned(),
                ToString::to_string,
            ),
            details: Some(json!({"source_close": {
                "outcome": "close_incomplete",
                "cleanup": {},
                "coordination_cleanup": super::lifecycle::coordination_cleanup(
                    result.coordination_cleanup
                ),
            }})),
        });
    }
    Ok(())
}

/// One completed export plus the retained identity used in its wire result.
struct ExportFactsWithIdentity {
    facts: ExportFacts,
    identity: Value,
}

#[allow(clippy::too_many_arguments)]
fn export_with_reader(
    state: &mut SessionState,
    object: &serde_json::Map<String, Value>,
    view: &ExportView,
    format: &str,
    destination: &Path,
    policy: PublicationPolicy,
    budget: &ExportBudget,
    reader: &ReaderValue,
) -> Result<ExportFactsWithIdentity, HandlerError> {
    let host_prefix = match sdk(reader.info())?.address_family {
        AddressFamily::Ipv4 => 32,
        AddressFamily::Ipv6 => 128,
    };
    let filter = decode_prefixes(object, format, host_prefix)?;
    if format == "legacy_binary"
        && !matches!(view, ExportView::Feed { .. } | ExportView::Selection { .. })
    {
        return Err(HandlerError::new(
            "invalid_argument",
            "not_started",
            "legacy_binary exports a flat address set; direct/structured values cannot be discarded",
        ));
    }
    if policy == PublicationPolicy::FailIfExists && destination_exists(destination)? {
        return Err(HandlerError::new(
            "name_exists",
            "not_started",
            format!(
                "export destination already exists: {}",
                destination.display()
            ),
        ));
    }
    let identity = source_identity(
        state,
        object["source"]["path"]
            .as_str()
            .expect("validator checked source.path"),
        object["source"]["mode"]
            .as_str()
            .expect("validator checked source.mode"),
        budget,
    )?;
    let cancellation = state.token.clone();
    let facts = match format {
        "legacy_binary" => write_legacy_binary(
            destination,
            policy,
            budget,
            reader,
            view,
            host_prefix,
            &cancellation,
        )?,
        "csv" | "jsonl" => write_rows(
            destination,
            policy,
            budget,
            reader,
            view,
            host_prefix,
            &cancellation,
            format == "jsonl",
        )?,
        "ipset" => write_streamed(
            destination,
            policy,
            budget,
            reader,
            view,
            &mut |writer, from, to| {
                emit_ipset(from, to, host_prefix, &mut |line| {
                    check_cancelled(&cancellation)?;
                    writer.write_line(line, 1)
                })
            },
        )?,
        "netset" => write_streamed(
            destination,
            policy,
            budget,
            reader,
            view,
            &mut |writer, from, to| {
                emit_netset(from, to, &filter, &mut |line, span| {
                    check_cancelled(&cancellation)?;
                    writer.write_line(line, span)
                })
            },
        )?,
        _ => write_streamed(
            destination,
            policy,
            budget,
            reader,
            view,
            &mut |writer, from, to| {
                check_cancelled(&cancellation)?;
                let span = span_of(from, to);
                writer.write_line(&ranges_line(from, to, host_prefix), span)
            },
        )?,
    };
    Ok(ExportFactsWithIdentity { facts, identity })
}

/// Create the writer, stream maximal coverage through one format
/// callback, and publish atomically.
#[allow(clippy::too_many_arguments)]
fn write_streamed(
    destination: &Path,
    policy: PublicationPolicy,
    budget: &ExportBudget,
    reader: &ReaderValue,
    view: &ExportView,
    format: &mut dyn FnMut(&mut ExportWriter, u128, u128) -> Result<(), HandlerError>,
) -> Result<ExportFacts, HandlerError> {
    let mut writer = ExportWriter::create(destination, policy, budget)?;
    let work = stream_coverage(reader, view, &mut |from, to| format(&mut writer, from, to));
    work.and_then(|()| writer.finish())
}

/// Write CSV or JSONL rows with their constant semantic values.
#[allow(clippy::too_many_arguments)]
fn write_rows(
    destination: &Path,
    policy: PublicationPolicy,
    budget: &ExportBudget,
    reader: &ReaderValue,
    view: &ExportView,
    host_prefix: u32,
    cancellation: &CancellationToken,
    jsonl: bool,
) -> Result<ExportFacts, HandlerError> {
    let mut writer = ExportWriter::create(destination, policy, budget)?;
    let result = (|| -> Result<(), HandlerError> {
        if !jsonl {
            writer.write_chunk(b"from,to,value\n", 0, 0)?;
        }
        // Buffer one row so adjacent equal-value segments become one
        // canonical row without retaining the stream.
        let mut pending: Option<(u128, u128, Value)> = None;
        stream_segments(reader, view, &mut |from, to, value| {
            check_cancelled(cancellation)?;
            let semantic = value_json(value);
            match pending.take() {
                None => pending = Some((from, to, semantic)),
                Some((pending_from, pending_to, pending_value)) => {
                    if from == pending_to.saturating_add(1) && pending_value == semantic {
                        pending = Some((pending_from, to, pending_value));
                    } else {
                        write_row(
                            &mut writer,
                            host_prefix,
                            jsonl,
                            pending_from,
                            pending_to,
                            &pending_value,
                        )?;
                        pending = Some((from, to, semantic));
                    }
                }
            }
            Ok(())
        })?;
        if let Some((from, to, value)) = pending {
            write_row(&mut writer, host_prefix, jsonl, from, to, &value)?;
        }
        Ok(())
    })();
    result.and_then(|()| writer.finish())
}

fn write_row(
    writer: &mut ExportWriter,
    host_prefix: u32,
    jsonl: bool,
    from: u128,
    to: u128,
    value: &Value,
) -> Result<(), HandlerError> {
    let span = span_of(from, to);
    if jsonl {
        let row = json!({
            "from": format_address(from, host_prefix),
            "to": format_address(to, host_prefix),
            "value": value,
        });
        writer.write_line(&row.to_string(), span)
    } else {
        let field = match value {
            Value::Number(number) => number.to_string(),
            Value::Array(names) => names
                .iter()
                .filter_map(Value::as_str)
                .collect::<Vec<_>>()
                .join(";"),
            other => other.to_string(),
        };
        let line = format!(
            "{},{},{}",
            format_address(from, host_prefix),
            format_address(to, host_prefix),
            csv_field(&field)
        );
        writer.write_line(&line, span)
    }
}

/// Write the released legacy binary format for a flat address set.
///
/// The released header declares record, byte, line, and unique-IP
/// counts before the payload, so the canonical ranges are streamed
/// once to prove the counts and budgets, then streamed again to write.
#[allow(clippy::too_many_arguments)]
fn write_legacy_binary(
    destination: &Path,
    policy: PublicationPolicy,
    budget: &ExportBudget,
    reader: &ReaderValue,
    view: &ExportView,
    host_prefix: u32,
    cancellation: &CancellationToken,
) -> Result<ExportFacts, HandlerError> {
    let ipv6 = host_prefix == 128;
    let record_size: u64 = if ipv6 { 32 } else { 8 };
    let minimum_header = legacy_binary_min_header_bytes(ipv6);
    let mut records = 0u64;
    let mut addresses = 0u128;
    stream_coverage(reader, view, &mut |from, to| {
        check_cancelled(cancellation)?;
        records = records
            .checked_add(1)
            .filter(|count| *count <= budget.max_rows)
            .ok_or_else(|| {
                HandlerError::new(
                    "output_limit",
                    "not_started",
                    format!("export refused before exceeding budget: record {records} exceeds max_rows (limit {})", budget.max_rows),
                )
            })?;
        let payload = record_size
            .saturating_mul(records)
            .saturating_add(4)
            .saturating_add(minimum_header);
        if payload > budget.max_output_bytes {
            return Err(HandlerError::new(
                "output_limit",
                "not_started",
                format!(
                    "export refused before exceeding budget: byte {payload} exceeds max_output_bytes (limit {})",
                    budget.max_output_bytes
                ),
            ));
        }
        addresses = addresses.saturating_add(span_of(from, to));
        Ok(())
    })?;
    if records == 0 {
        // The released writer emits nothing for an empty set; the
        // destination is still atomically published as an empty file.
        return ExportWriter::create(destination, policy, budget)?.finish();
    }
    let header = legacy_binary_header(ipv6, records, addresses);
    let exact_bytes = header.len() as u64 + 4 + record_size * records;
    if exact_bytes > budget.max_output_bytes {
        return Err(HandlerError::new(
            "output_limit",
            "not_started",
            format!(
                "export refused before exceeding budget: byte {exact_bytes} exceeds max_output_bytes (limit {})",
                budget.max_output_bytes
            ),
        ));
    }
    let mut writer = ExportWriter::create(destination, policy, budget)?;
    let result = (|| -> Result<(), HandlerError> {
        writer.write_chunk(header.as_bytes(), 0, 0)?;
        writer.write_chunk(&LEGACY_ENDIANNESS_MARKER, 0, 0)?;
        stream_coverage(reader, view, &mut |from, to| {
            check_cancelled(cancellation)?;
            let span = span_of(from, to);
            if ipv6 {
                let record = legacy_binary_record_v6(from, to);
                writer.write_chunk(&record, 1, span)
            } else {
                let record = legacy_binary_record_v4(
                    u32::try_from(from).expect("IPv4 address fits u32"),
                    u32::try_from(to).expect("IPv4 address fits u32"),
                );
                writer.write_chunk(&record, 1, span)
            }
        })
    })();
    result.and_then(|()| writer.finish())
}

/// Stream the view as maximal coverage ranges, merging adjacent
/// segments regardless of their values (flat set semantics).
fn stream_coverage(
    reader: &ReaderValue,
    view: &ExportView,
    sink: &mut CoverageSink<'_>,
) -> Result<(), HandlerError> {
    let mut pending_from: Option<u128> = None;
    let mut pending_to = 0u128;
    stream_segments(reader, view, &mut |from, to, _value| match pending_from {
        None => {
            pending_from = Some(from);
            pending_to = to;
            Ok(())
        }
        Some(_) if from == pending_to.saturating_add(1) => {
            pending_to = to;
            Ok(())
        }
        Some(_) => {
            let from = pending_from.replace(from).unwrap_or_default();
            sink(from, pending_to)?;
            pending_to = to;
            Ok(())
        }
    })?;
    if let Some(from) = pending_from {
        sink(from, pending_to)?;
    }
    Ok(())
}

/// Stream one ordered canonical segment per constant semantic value.
fn stream_segments(
    reader: &ReaderValue,
    view: &ExportView,
    sink: &mut SegmentSink<'_>,
) -> Result<(), HandlerError> {
    match sdk(reader.info())?.address_family {
        AddressFamily::Ipv4 => stream_segments_v4(reader, view, sink),
        AddressFamily::Ipv6 => stream_segments_v6(reader, view, sink),
    }
}

fn value_json(value: &ExportValue) -> Value {
    match value {
        ExportValue::Direct(direct) => json!(direct),
        ExportValue::Structured(structured) => structured.clone(),
        ExportValue::Feeds(feeds) => json!(feeds),
    }
}

fn span_of(from: u128, to: u128) -> u128 {
    // The complete IPv6 space alone exceeds the u128 counter; the
    // frozen wire result schema models a u64 counter, so saturate.
    to.saturating_sub(from).saturating_add(1)
}

fn check_cancelled(token: &CancellationToken) -> Result<(), HandlerError> {
    if token.is_cancelled() {
        return Err(HandlerError::new(
            "cancelled",
            "not_started",
            "export was cancelled",
        ));
    }
    Ok(())
}

fn view_cursor<T>(result: iprange_livedb::Result<T>) -> Result<T, HandlerError> {
    result.map_err(read_error).map_err(view_error)
}

fn view_error(error: HandlerError) -> HandlerError {
    if error.code == "wrong_value_kind" || error.code == "wrong_structure_kind" {
        HandlerError::new(
            "handle_wrong_kind",
            "not_started",
            "reader does not support the requested export view",
        )
    } else {
        error
    }
}

fn stream_segments_v4(
    reader: &ReaderValue,
    view: &ExportView,
    sink: &mut SegmentSink<'_>,
) -> Result<(), HandlerError> {
    match view {
        ExportView::Direct => {
            let mut cursor = view_cursor(reader.direct_cursor_v4(RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                sink(
                    u128::from(range.from.0),
                    u128::from(range.to.0),
                    &ExportValue::Direct(range.value),
                )?;
            }
            Ok(())
        }
        ExportView::Structured => {
            let mut cursor =
                view_cursor(reader.network_enrichment_v1_cursor_v4(RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let feeds = super::reader::threat_feed_names(reader, &range.value)?;
                let value =
                    ExportValue::Structured(super::convert::enrichment_view(&range.value, &feeds));
                sink(u128::from(range.from.0), u128::from(range.to.0), &value)?;
            }
            Ok(())
        }
        ExportView::Feed { name } => {
            require_feed(reader, name)?;
            let mut cursor =
                view_cursor(reader.feed_range_cursor_v4(name, RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let value = ExportValue::Feeds(vec![name.clone()]);
                sink(u128::from(range.from.0), u128::from(range.to.0), &value)?;
            }
            Ok(())
        }
        ExportView::Selection { named } => {
            let feeds = resolve_selection(reader, named.as_deref())?;
            let mut cursors = Vec::with_capacity(feeds.len());
            for feed in &feeds {
                let mut cursor =
                    view_cursor(reader.feed_range_cursor_v4(&feed.name, RangeDirection::Forward))?;
                let current = sdk(cursor.next_range()).map_err(view_error)?;
                cursors.push((cursor, current));
            }
            let heads: Vec<Option<(u128, u128)>> = cursors
                .iter()
                .map(|(_, current)| {
                    current.map(|range| (u128::from(range.from.0), u128::from(range.to.0)))
                })
                .collect();
            let names: Vec<String> = feeds.iter().map(|feed| feed.name.clone()).collect();
            selection_sweep(
                heads,
                &names,
                &mut |index| {
                    Ok(sdk(cursors[index].0.next_range())
                        .map_err(view_error)?
                        .map(|range| (u128::from(range.from.0), u128::from(range.to.0))))
                },
                &mut |from, to, names| sink(from, to, &ExportValue::Feeds(names.to_vec())),
            )
        }
    }
}

fn stream_segments_v6(
    reader: &ReaderValue,
    view: &ExportView,
    sink: &mut SegmentSink<'_>,
) -> Result<(), HandlerError> {
    match view {
        ExportView::Direct => {
            let mut cursor = view_cursor(reader.direct_cursor_v6(RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                sink(
                    range.from.to_u128(),
                    range.to.to_u128(),
                    &ExportValue::Direct(range.value),
                )?;
            }
            Ok(())
        }
        ExportView::Structured => {
            let mut cursor =
                view_cursor(reader.network_enrichment_v1_cursor_v6(RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let feeds = super::reader::threat_feed_names(reader, &range.value)?;
                let value =
                    ExportValue::Structured(super::convert::enrichment_view(&range.value, &feeds));
                sink(range.from.to_u128(), range.to.to_u128(), &value)?;
            }
            Ok(())
        }
        ExportView::Feed { name } => {
            require_feed(reader, name)?;
            let mut cursor =
                view_cursor(reader.feed_range_cursor_v6(name, RangeDirection::Forward))?;
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let value = ExportValue::Feeds(vec![name.clone()]);
                sink(range.from.to_u128(), range.to.to_u128(), &value)?;
            }
            Ok(())
        }
        ExportView::Selection { named } => {
            let feeds = resolve_selection(reader, named.as_deref())?;
            let mut cursors = Vec::with_capacity(feeds.len());
            for feed in &feeds {
                let mut cursor =
                    view_cursor(reader.feed_range_cursor_v6(&feed.name, RangeDirection::Forward))?;
                let current = sdk(cursor.next_range()).map_err(view_error)?;
                cursors.push((cursor, current));
            }
            let heads: Vec<Option<(u128, u128)>> = cursors
                .iter()
                .map(|(_, current)| current.map(|range| (range.from.to_u128(), range.to.to_u128())))
                .collect();
            let names: Vec<String> = feeds.iter().map(|feed| feed.name.clone()).collect();
            selection_sweep(
                heads,
                &names,
                &mut |index| {
                    Ok(sdk(cursors[index].0.next_range())
                        .map_err(view_error)?
                        .map(|range| (range.from.to_u128(), range.to.to_u128())))
                },
                &mut |from, to, names| sink(from, to, &ExportValue::Feeds(names.to_vec())),
            )
        }
    }
}

/// One selected feed resolved to its catalog identity.
struct SelectedFeed {
    index: u32,
    name: String,
}

fn resolve_selection(
    reader: &ReaderValue,
    named: Option<&[String]>,
) -> Result<Vec<SelectedFeed>, HandlerError> {
    let mut feeds = match named {
        None => {
            let mut catalog = sdk(reader.feed_cursor())?;
            let mut feeds = Vec::new();
            while let Some(entry) = sdk(catalog.next_feed())? {
                feeds.push(SelectedFeed {
                    index: entry.index,
                    name: entry.name.as_str().to_owned(),
                });
            }
            feeds
        }
        Some(names) => {
            let mut feeds = Vec::with_capacity(names.len());
            for name in names {
                let entry = require_feed(reader, name)?;
                feeds.push(SelectedFeed {
                    index: entry.index,
                    name: name.clone(),
                });
            }
            feeds
        }
    };
    feeds.sort_by_key(|feed| feed.index);
    Ok(feeds)
}

fn lookup_feed(reader: &ReaderValue, name: &str) -> iprange_livedb::Result<Option<FeedEntry>> {
    match reader {
        ReaderValue::Immutable(reader) => reader.lookup_feed(name),
        ReaderValue::Live(reader) => reader.lookup_feed(name),
    }
}

fn require_feed(reader: &ReaderValue, name: &str) -> Result<FeedEntry, HandlerError> {
    FeedName::new(name).map_err(|error| {
        HandlerError::new(
            "name_invalid",
            "not_started",
            format!("export feed name is invalid: {error}"),
        )
    })?;
    sdk(lookup_feed(reader, name))?.ok_or_else(|| {
        HandlerError::new(
            "name_not_found",
            "not_started",
            format!("export feed does not exist: {name}"),
        )
    })
}

/// K-way sweep over per-feed forward range cursors.
///
/// Emits one maximal segment whenever the catalog-ordered set of
/// selected feeds covering the address changes. Only each feed's
/// current head range is retained, so the working set stays bounded
/// by the selected-feed count instead of the result size.
fn selection_sweep(
    heads: Vec<Option<(u128, u128)>>,
    names: &[String],
    advance: &mut dyn FnMut(usize) -> Result<Option<(u128, u128)>, HandlerError>,
    emit: &mut dyn FnMut(u128, u128, &[String]) -> Result<(), HandlerError>,
) -> Result<(), HandlerError> {
    let mut current = heads;
    // Pending feed heads by starting address; active feeds in catalog
    // order with the end of their covering range.
    let mut pending: BTreeSet<(u128, usize)> = current
        .iter()
        .enumerate()
        .filter_map(|(index, head)| head.map(|(from, _)| (from, index)))
        .collect();
    let mut active: Vec<(usize, u128)> = Vec::new();
    let mut position = 0u128;
    loop {
        if active.is_empty() {
            let Some(&(from, _)) = pending.iter().next() else {
                return Ok(());
            };
            position = from;
            activate(&mut pending, &mut active, &current, position);
        }
        // The first boundary is the earliest active range end plus one
        // and the earliest pending range start, whichever comes first.
        let mut boundary: Option<u128> = None;
        for (_, to) in &active {
            boundary = min_unbounded(boundary, to.checked_add(1));
        }
        if let Some(&(next_from, _)) = pending.iter().next() {
            boundary = min_unbounded(boundary, Some(next_from));
        }
        let segment_names: Vec<String> = active
            .iter()
            .map(|(index, _)| names[*index].clone())
            .collect();
        match boundary {
            None => {
                emit(position, u128::MAX, &segment_names)?;
                return Ok(());
            }
            Some(boundary) => {
                emit(position, boundary - 1, &segment_names)?;
                position = boundary;
                let mut index = 0;
                while index < active.len() {
                    if active[index].1 < boundary {
                        let (finished, _) = active.remove(index);
                        current[finished] = advance(finished)?;
                        if let Some((from, _)) = current[finished] {
                            pending.insert((from, finished));
                        }
                    } else {
                        index += 1;
                    }
                }
                activate(&mut pending, &mut active, &current, position);
            }
        }
    }
}

/// Move every pending head starting at or before `position` into the
/// catalog-ordered active list.
fn activate(
    pending: &mut BTreeSet<(u128, usize)>,
    active: &mut Vec<(usize, u128)>,
    current: &[Option<(u128, u128)>],
    position: u128,
) {
    while let Some(&(from, index)) = pending.iter().next() {
        if from > position {
            break;
        }
        pending.remove(&(from, index));
        let to = current[index].map(|(_, to)| to).unwrap_or(u128::MAX);
        let at = active
            .binary_search_by(|(existing, _)| existing.cmp(&index))
            .unwrap_or_else(|at| at);
        active.insert(at, (index, to));
    }
}

/// Minimum where `None` means unbounded (the family address maximum).
fn min_unbounded(left: Option<u128>, right: Option<u128>) -> Option<u128> {
    match (left, right) {
        (Some(left), Some(right)) => Some(left.min(right)),
        (None, right) => right,
        (left, None) => left,
    }
}

/// Extract a database source after `validate_export` checked its shape.
fn source_value(value: &Value) -> Result<(String, String), HandlerError> {
    let source = value
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("source must be an object"))?;
    let path = source["path"]
        .as_str()
        .expect("validator checked source.path");
    let mode = source["mode"]
        .as_str()
        .expect("validator checked source.mode");
    Ok((path.to_owned(), mode.to_owned()))
}

fn validate_source(value: &Value) -> Result<(), String> {
    let source = value.as_object().ok_or("source must be an object")?;
    if source.len() != 2 || !source.contains_key("path") || !source.contains_key("mode") {
        return Err("source requires exactly path and mode".into());
    }
    validate_path(source["path"].as_str())?;
    match source["mode"].as_str() {
        Some("immutable") | Some("live") => Ok(()),
        _ => Err("source.mode must be immutable or live".into()),
    }
}

fn validate_view(value: &Value) -> Result<(), String> {
    let view = value.as_object().ok_or("view must be an object")?;
    match view.get("kind").and_then(Value::as_str) {
        Some("direct") | Some("structured") => {
            if view.len() != 1 {
                return Err("direct and structured views accept only kind".into());
            }
            Ok(())
        }
        Some("feed") => {
            if view.len() != 2 || !view.contains_key("feed") {
                return Err("feed view requires exactly kind and feed".into());
            }
            let name = view["feed"].as_str().ok_or("view.feed must be a string")?;
            FeedName::new(name).map_err(|error| error.to_string())?;
            Ok(())
        }
        Some("selection") => {
            if view.len() != 2 || !view.contains_key("selection") {
                return Err("selection view requires exactly kind and selection".into());
            }
            let selection = view["selection"]
                .as_object()
                .ok_or("view.selection must be an object")?;
            match selection.get("mode").and_then(Value::as_str) {
                Some("all") => {
                    if selection.len() != 1 {
                        return Err("all selection accepts only mode".into());
                    }
                    Ok(())
                }
                Some("named") => {
                    if selection.len() != 2 || !selection.contains_key("feeds") {
                        return Err("named selection requires exactly mode and feeds".into());
                    }
                    let feeds = selection["feeds"]
                        .as_array()
                        .ok_or("selection.feeds must be an array")?;
                    if feeds.is_empty() {
                        return Err("selection.feeds must contain at least one name".into());
                    }
                    let mut seen: Vec<&str> = Vec::new();
                    for feed in feeds {
                        let name = feed
                            .as_str()
                            .ok_or("each selection feed must be a string")?;
                        FeedName::new(name).map_err(|error| error.to_string())?;
                        if seen.contains(&name) {
                            return Err("selection.feeds must be unique".into());
                        }
                        seen.push(name);
                    }
                    Ok(())
                }
                _ => Err("selection.mode must be all or named".into()),
            }
        }
        _ => Err("view.kind must be direct, structured, feed, or selection".into()),
    }
}

/// Decode the view selector after `validate_export` checked its shape.
fn decode_view(value: &Value) -> Result<ExportView, HandlerError> {
    let view = value
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("view must be an object"))?;
    match view.get("kind").and_then(Value::as_str) {
        Some("direct") => Ok(ExportView::Direct),
        Some("structured") => Ok(ExportView::Structured),
        Some("feed") => Ok(ExportView::Feed {
            name: view["feed"]
                .as_str()
                .expect("validator checked view.feed")
                .to_owned(),
        }),
        Some("selection") => {
            let selection =
                member_object(view, "selection").map_err(HandlerError::invalid_params)?;
            let named = match selection.get("mode").and_then(Value::as_str) {
                Some("all") => None,
                _ => Some(
                    selection["feeds"]
                        .as_array()
                        .expect("validator checked selection.feeds")
                        .iter()
                        .map(|feed| {
                            feed.as_str()
                                .expect("validator checked each feed name")
                                .to_owned()
                        })
                        .collect(),
                ),
            };
            Ok(ExportView::Selection { named })
        }
        _ => Err(HandlerError::invalid_params(
            "view.kind must be direct, structured, feed, or selection",
        )),
    }
}

fn validate_result_budget(value: &Value) -> Result<(), String> {
    let budget = value.as_object().ok_or("result_budget must be an object")?;
    if budget.len() != 3 {
        return Err(
            "result_budget requires exactly max_rows, max_output_bytes, and max_open_files".into(),
        );
    }
    positive_u64_string(budget["max_rows"].as_str())
        .map_err(|error| format!("result_budget.max_rows: {error}"))?;
    positive_u64_string(budget["max_output_bytes"].as_str())
        .map_err(|error| format!("result_budget.max_output_bytes: {error}"))?;
    positive_u32(&budget["max_open_files"])
        .map_err(|error| format!("result_budget.max_open_files: {error}"))?;
    Ok(())
}

fn u32_member(value: &Value) -> Option<u32> {
    value.as_u64().and_then(|parsed| u32::try_from(parsed).ok())
}

/// Decode the export limits after `validate_export` checked their shape.
fn decode_budget(value: &Value) -> Result<ExportBudget, HandlerError> {
    let budget = value
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("result_budget must be an object"))?;
    Ok(ExportBudget {
        max_rows: u64_string(budget["max_rows"].as_str()).map_err(HandlerError::invalid_params)?,
        max_output_bytes: u64_string(budget["max_output_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: u32_member(&budget["max_open_files"])
            .ok_or_else(|| HandlerError::invalid_params("max_open_files must be u32"))?,
    })
}

fn decode_prefixes(
    object: &serde_json::Map<String, Value>,
    format: &str,
    host_prefix: u32,
) -> Result<PrefixFilter, HandlerError> {
    let invalid = |message: &str| HandlerError::invalid_params(message);
    if format != "netset" {
        return Ok(PrefixFilter::all(host_prefix));
    }
    if let Some(minimum) = object.get("min_prefix") {
        let minimum = u32_member(minimum).ok_or_else(|| invalid("min_prefix must be u32"))?;
        if minimum > host_prefix {
            return Err(invalid("min_prefix exceeds the family host prefix"));
        }
        return Ok(PrefixFilter::min_prefix(host_prefix, minimum));
    }
    if let Some(prefixes) = object.get("prefixes").and_then(Value::as_array) {
        let mut decoded = Vec::with_capacity(prefixes.len());
        for prefix in prefixes {
            let prefix = u32_member(prefix).ok_or_else(|| invalid("each prefix must be u32"))?;
            if prefix > host_prefix {
                return Err(invalid("prefix exceeds the family host prefix"));
            }
            decoded.push(prefix);
        }
        if !decoded.contains(&host_prefix) {
            return Err(invalid("prefixes must include the family host prefix"));
        }
        return Ok(PrefixFilter::listed(host_prefix, &decoded));
    }
    Ok(PrefixFilter::all(host_prefix))
}

fn destination_exists(destination: &Path) -> Result<bool, HandlerError> {
    destination.try_exists().map_err(|error| {
        HandlerError::new(
            "io",
            "not_started",
            format!(
                "cannot inspect export destination {}: {error}",
                destination.display()
            ),
        )
    })
}

/// The retained source-file identity of one export source.
///
/// The public readers expose no file identity, and the wire result requires
/// the same `{volume,file}` pair as recovery inspection. The public SDK
/// inspection provides it without scanning the page graph; immutable mode
/// needs one open file while live mode needs two.
fn source_identity(
    state: &SessionState,
    source: &str,
    source_mode: &str,
    budget: &ExportBudget,
) -> Result<Value, HandlerError> {
    let inspection =
        ValidationBudget::heap_only(2 * std::mem::size_of::<u32>() as u64, budget.max_open_files);
    let mode = if source_mode == "live" {
        RecoveryInspectionMode::Live
    } else {
        RecoveryInspectionMode::Immutable
    };
    let result =
        inspect_recovery_candidates(source, mode, &inspection, &state.token).map_err(read_error)?;
    file_identity(&result.source_identity)
}

/// Convert the public SDK local file identity to its documented
/// volume/file decimal pair (kind 1: POSIX device/inode, kind 2:
/// Windows volume/file). Production counterpart of the test-only
/// helper in convert.rs.
fn file_identity(identity: &LocalFileIdentity) -> Result<Value, HandlerError> {
    let volume = u64::from_le_bytes(
        identity.bytes[0..8]
            .try_into()
            .expect("identity volume is fixed width"),
    );
    let file = u64::from_le_bytes(
        identity.bytes[8..16]
            .try_into()
            .expect("identity file is fixed width"),
    );
    let supported = match identity.kind {
        1 => identity.bytes[16..].iter().all(|byte| *byte == 0),
        2 => identity.bytes[24..].iter().all(|byte| *byte == 0),
        _ => false,
    };
    if !supported {
        return Err(HandlerError::new(
            "io",
            "read_only_failure",
            "source file identity uses an unsupported platform encoding",
        ));
    }
    Ok(json!({
        "volume": volume.to_string(),
        "file": file.to_string(),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sweep(feeds: &[Vec<(u128, u128)>]) -> Vec<(u128, u128, Vec<String>)> {
        let names: Vec<String> = (0..feeds.len()).map(|i| format!("feed-{i}")).collect();
        let heads: Vec<Option<(u128, u128)>> =
            feeds.iter().map(|ranges| ranges.first().copied()).collect();
        let mut positions = vec![0usize; feeds.len()];
        let mut output = Vec::new();
        selection_sweep(
            heads,
            &names,
            &mut |index| {
                positions[index] += 1;
                Ok(feeds[index].get(positions[index]).copied())
            },
            &mut |from, to, names| {
                let mut ordered = names.to_vec();
                ordered.sort();
                output.push((from, to, ordered));
                Ok(())
            },
        )
        .unwrap();
        output
    }

    #[test]
    fn selection_sweep_segments_by_catalog_ordered_feed_set() {
        let output = sweep(&[vec![(0, 4)], vec![(2, 6)]]);
        assert_eq!(
            output,
            vec![
                (0, 1, vec!["feed-0".to_owned()]),
                (2, 4, vec!["feed-0".to_owned(), "feed-1".to_owned()]),
                (5, 6, vec!["feed-1".to_owned()]),
            ]
        );
    }

    #[test]
    fn selection_sweep_handles_disjoint_and_terminal_feeds() {
        let output = sweep(&[vec![(10, 19), (40, 49)], vec![(20, 29)]]);
        assert_eq!(
            output,
            vec![
                (10, 19, vec!["feed-0".to_owned()]),
                (20, 29, vec!["feed-1".to_owned()]),
                (40, 49, vec!["feed-0".to_owned()]),
            ]
        );
        assert!(sweep(&[vec![]]).is_empty());
        let full = sweep(&[vec![(u128::MAX - 1, u128::MAX)]]);
        assert_eq!(
            full,
            vec![(u128::MAX - 1, u128::MAX, vec!["feed-0".to_owned()])]
        );
    }

    #[test]
    fn export_params_validation_rejects_invalid_prefix_controls() {
        let base = json!({
            "source": {"path": "/tmp/db.iprange", "mode": "immutable"},
            "view": {"kind": "selection", "selection": {"mode": "all"}},
            "format": "netset",
            "destination": "/tmp/out.netset",
            "publication_policy": "fail_if_exists",
            "result_budget": {
                "max_rows": "10", "max_output_bytes": "1000", "max_open_files": 1
            }
        });
        assert_eq!(validate_export(&base), Ok(()));
        let mut both = base.clone();
        both["min_prefix"] = json!(24);
        both["prefixes"] = json!([24, 32]);
        assert!(validate_export(&both).is_err());
        let mut wrong_format = base.clone();
        wrong_format["format"] = json!("csv");
        wrong_format["min_prefix"] = json!(24);
        assert!(validate_export(&wrong_format).is_err());
        let mut bad_range = base.clone();
        bad_range["prefixes"] = json!([24, 129]);
        assert!(validate_export(&bad_range).is_err());
        let mut duplicate = base.clone();
        duplicate["prefixes"] = json!([24, 24, 32]);
        assert!(validate_export(&duplicate).is_err());
        let mut empty = base.clone();
        empty["prefixes"] = json!([]);
        assert!(validate_export(&empty).is_err());
        let mut unknown = base.clone();
        unknown["unexpected"] = json!(1);
        assert!(validate_export(&unknown).is_err());
        for field in ["max_rows", "max_output_bytes"] {
            let mut zero = base.clone();
            zero["result_budget"][field] = json!("0");
            assert!(validate_export(&zero).is_err());
        }
        let mut zero_files = base.clone();
        zero_files["result_budget"]["max_open_files"] = json!(0);
        assert!(validate_export(&zero_files).is_err());
    }

    #[test]
    fn file_identity_decodes_public_sdk_layout() {
        let mut bytes = [0u8; 32];
        bytes[..8].copy_from_slice(&0x0102030405060708u64.to_le_bytes());
        bytes[8..16].copy_from_slice(&0x090a0b0c0d0e0f10u64.to_le_bytes());
        let identity = LocalFileIdentity { kind: 1, bytes };
        assert_eq!(
            file_identity(&identity).unwrap(),
            json!({"volume": "72623859790382856", "file": "651345242494996240"})
        );
        bytes[16] = 1;
        assert!(file_identity(&LocalFileIdentity { kind: 1, bytes }).is_err());
        bytes[16] = 0;
        assert!(file_identity(&LocalFileIdentity { kind: 9, bytes }).is_err());
    }
}

#[cfg(test)]
mod live_source_tests {
    use super::*;
    use iprange_livedb::{
        create_live, AddressFamily, CancellationToken, FeedName, Ipv4Key, LiveWriter,
        MembershipOperation, StructureKind, TransactionBudget, ValueKind, ValueTag,
    };
    use std::fs;
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn live_membership(label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let main = std::env::temp_dir().join(format!(
            "iprange-export-live-{label}-{}-{unique}",
            std::process::id()
        ));
        let token = CancellationToken::new();
        create_live(
            &main,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            StructureKind::None,
            ValueTag::new(b"export-live").unwrap(),
            2,
            &token,
        )
        .unwrap();
        let budget = TransactionBudget {
            max_heap_bytes: 2 * 1024 * 1024,
            max_private_pages: 10_000,
            max_file_growth_pages: 10_000,
            max_open_files: 2,
        };
        let mut writer = LiveWriter::open(&main, budget, &token).unwrap();
        let mut transaction = writer.begin_membership_transaction(&token).unwrap();
        let feed = transaction
            .ensure_feed(FeedName::new("feed-a").unwrap())
            .unwrap();
        let empty = transaction.empty_membership().unwrap();
        let membership = transaction.add_feed(empty, feed).unwrap();
        transaction
            .apply_v4(
                Ipv4Key(0xc0000201),
                Ipv4Key(0xc000020a),
                membership,
                MembershipOperation::Replace,
            )
            .unwrap();
        transaction.commit().unwrap();
        writer.close().unwrap();
        main
    }

    fn sidecar(main: &Path) -> PathBuf {
        let mut name = main.file_name().unwrap().to_os_string();
        name.push(".readers");
        main.with_file_name(name)
    }

    #[test]
    fn live_membership_feed_exports_exact_csv() {
        let main = live_membership("csv");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let destination =
            std::env::temp_dir().join(format!("iprange-export-live-out-{unique}.csv"));
        let mut state = SessionState::default();
        let result = export(
            &mut state,
            json!({
                "source": {"path": main.display().to_string(), "mode": "live"},
                "view": {"kind": "feed", "feed": "feed-a"},
                "format": "csv",
                "destination": destination.display().to_string(),
                "publication_policy": "fail_if_exists",
                "result_budget": {
                    "max_rows": "10",
                    "max_output_bytes": "1000",
                    "max_open_files": 2
                }
            }),
        )
        .unwrap();
        assert_eq!(result["method"], "iprange.v1.export");
        assert_eq!(result["format"], "csv");
        assert_eq!(result["rows"], "1");
        assert_eq!(result["addresses"], "10");
        assert_eq!(
            fs::read_to_string(&destination).unwrap(),
            "from,to,value\n192.0.2.1,192.0.2.10,feed-a\n"
        );
        fs::remove_file(&destination).unwrap();
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }
}
