//! `iprange.v1.export`: canonical file export of one pinned v4 view.
//!
//! Source iteration is a bounded stream of canonical constant-value
//! segments over public SDK cursors. Flat set formats merge those
//! segments into maximal coverage; row formats keep their values. The
//! writer enforces the caller's row/byte budgets before each row, so
//! prefix or per-address expansion refuses instead of exploding.

use std::collections::{BTreeSet, HashSet};
use std::path::Path;
use std::sync::Arc;

use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::recovery::{inspect_recovery_candidates, RecoveryInspectionMode};
use iprange_livedb::validation::{LocalFileIdentity, ValidationBudget};
use iprange_livedb::{
    AddressFamily, CancellationToken, Cardinality129, FeedEntry, FeedName, ImmutableReader,
    LiveReader, RangeDirection,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::ReaderValue;
use super::reader::{
    bounded_result, member_object, positive_u32, positive_u64_string, preflight_response,
    publication_policy, read_error, sdk, u64_string, validate_path, widest_close_fact,
    widest_identity, WIDEST_129, WIDEST_U64,
};
use crate::io::export_writer::{
    emit_ipset, emit_netset, inclusive_span as span_of, legacy_binary_header,
    legacy_binary_min_header_bytes, legacy_binary_record_v4, legacy_binary_record_v6, push_address,
    push_json_string, push_ranges_line, write_json_value, ExportBudget, ExportFacts, ExportWriter,
    PrefixFilter, LEGACY_ENDIANNESS_MARKER,
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
///
/// Feed sets are shared with `Arc` so each emitted segment clones only
/// a reference-count bump instead of allocating the feed-name array;
/// `PartialEq` compares contents, keeping equal-value merging identical.
#[derive(Clone, Debug, PartialEq)]
enum ExportValue {
    Direct(u32),
    Structured(Value),
    Feeds(Arc<[String]>),
}

type SegmentSink<'a> = dyn FnMut(u128, u128, ExportValue) -> Result<(), HandlerError> + 'a;
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
    let destination_text = object["destination"]
        .as_str()
        .expect("validator checked path");
    let destination = Path::new(destination_text);
    let policy = publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
    let budget = decode_budget(&object["result_budget"])?;
    // The complete inline result carries the destination string and
    // the source identity; refuse an unrepresentable request before
    // the source reader is opened or any output file is created, so a
    // published export is never relabeled as a read-only failure by
    // the post-hoc response bound (iprange-jsonrpc-v1.md).
    preflight_export(state, format, destination_text, &source_mode)?;
    let mut reader = open_source(&source_path, &source_mode, &state.token())?;
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
    // Close the source even when the export failed, then report the
    // export failure; a completed export below must also survive a
    // close failure with both facts preserved.
    let close_result = close_ephemeral_source(&mut reader);
    let completed = match export_result {
        Ok(completed) => completed,
        Err(mut export_error) => {
            // A product error preserves the close result of the reader
            // it opened whether the close succeeded or failed: keep the
            // export error primary and merge the factual source_close
            // into its details (double-fault pattern, algebra.rs
            // collect_projection_facts, when the close also failed).
            let close_fact = match close_result {
                Ok(Some(close)) => Some(close),
                Ok(None) => None,
                Err(close_error) => close_error.details.and_then(|mut close_details| {
                    close_details
                        .as_object_mut()
                        .and_then(|members| members.remove("source_close"))
                }),
            };
            if let Some(close_fact) = close_fact {
                let mut details = export_error.details.take().unwrap_or_else(|| json!({}));
                if let Some(members) = details.as_object_mut() {
                    members.insert("source_close".into(), close_fact);
                }
                export_error.details = Some(details);
            }
            return Err(export_error);
        }
    };
    let mut result = json!({
        "method": "iprange.v1.export",
        "path": completed.facts.path,
        "format": format,
        "sha256": completed.facts.sha256,
        "rows": completed.facts.rows.to_string(),
        "addresses": completed.facts.addresses.to_string(),
        "bytes": completed.facts.bytes.to_string(),
        "identity": completed.identity,
    });
    match close_result {
        Ok(Some(source_close)) => {
            result["source_close"] = source_close;
        }
        Ok(None) => {}
        Err(error) => {
            return Err(super::reader::preserve_completed_report(error, result));
        }
    }
    bounded_result(result)
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

/// Close the export source through the shared ephemeral-reader owner
/// so every internally opened reader reports the same close facts.
fn close_ephemeral_source(reader: &mut ReaderValue) -> Result<Option<Value>, HandlerError> {
    super::reader::close_ephemeral_reader(reader)
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
    let cancellation = state.token();
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
        "ipset" => {
            let mut line = String::new();
            write_streamed(
                destination,
                policy,
                budget,
                reader,
                view,
                &mut |writer, from, to| {
                    emit_ipset(from, to, host_prefix, &mut line, &mut |text| {
                        check_cancelled(&cancellation)?;
                        writer.write_line(text, Cardinality129::from_u64(1))
                    })
                },
            )?
        }
        "netset" => {
            let mut line = String::new();
            write_streamed(
                destination,
                policy,
                budget,
                reader,
                view,
                &mut |writer, from, to| {
                    emit_netset(from, to, &filter, &mut line, &mut |text, span| {
                        check_cancelled(&cancellation)?;
                        writer.write_line(text, span)
                    })
                },
            )?
        }
        _ => {
            let mut line = String::new();
            write_streamed(
                destination,
                policy,
                budget,
                reader,
                view,
                &mut |writer, from, to| {
                    check_cancelled(&cancellation)?;
                    let span = span_of(from, to);
                    line.clear();
                    push_ranges_line(&mut line, from, to, host_prefix);
                    writer.write_line(&line, span)
                },
            )?
        }
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
            writer.write_chunk(b"from,to,value\n", 0, Cardinality129::ZERO)?;
        }
        // Buffer one row so adjacent equal-value segments become one
        // canonical row without retaining the stream. One line buffer
        // is reused for every row so large exports allocate no per-row
        // strings.
        let mut line = String::new();
        // One reusable scratch for RFC-4180 quoting of structured
        // CSV fields: a quoted row reuses this buffer instead of
        // allocating a fresh string per row.
        let mut quote = String::new();
        // The stream constructs one owned semantic value per segment
        // and moves it here, so merging adjacent equal-value segments
        // does not allocate: equal values drop the incoming segment,
        // different values move it into the pending slot.
        let mut pending: Option<(u128, u128, ExportValue)> = None;
        stream_segments(reader, view, &mut |from, to, value| {
            check_cancelled(cancellation)?;
            match pending.take() {
                None => pending = Some((from, to, value)),
                Some((pending_from, pending_to, pending_value)) => {
                    if from == pending_to.saturating_add(1) && pending_value == value {
                        pending = Some((pending_from, to, pending_value));
                    } else {
                        write_row(
                            &mut writer,
                            host_prefix,
                            jsonl,
                            pending_from,
                            pending_to,
                            &pending_value,
                            &mut line,
                            &mut quote,
                        )?;
                        pending = Some((from, to, value));
                    }
                }
            }
            Ok(())
        })?;
        if let Some((from, to, value)) = pending {
            write_row(
                &mut writer,
                host_prefix,
                jsonl,
                from,
                to,
                &value,
                &mut line,
                &mut quote,
            )?;
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
    value: &ExportValue,
    line: &mut String,
    quote: &mut String,
) -> Result<(), HandlerError> {
    let span = span_of(from, to);
    line.clear();
    if jsonl {
        line.push_str("{\"from\":");
        push_address(line, from, host_prefix);
        line.push_str(",\"to\":");
        push_address(line, to, host_prefix);
        line.push_str(",\"value\":");
        match value {
            ExportValue::Direct(direct) => {
                use std::fmt::Write as _;
                let _ = write!(line, "{direct}");
            }
            ExportValue::Structured(structured) => write_json_value(line, structured)?,
            // Feed names are [a-z0-9_.-] (SDK FeedName grammar), so the
            // array literal is written straight into the line buffer.
            ExportValue::Feeds(feeds) => {
                line.push('[');
                let mut first = true;
                for feed in feeds.iter() {
                    if !first {
                        line.push(',');
                    }
                    first = false;
                    push_json_string(line, feed);
                }
                line.push(']');
            }
        }
        line.push('}');
    } else {
        push_address(line, from, host_prefix);
        line.push(',');
        push_address(line, to, host_prefix);
        line.push(',');
        match value {
            ExportValue::Direct(direct) => {
                use std::fmt::Write as _;
                let _ = write!(line, "{direct}");
            }
            // Feed names are [a-z0-9_.-] (SDK FeedName grammar), so the
            // semicolon-joined field never needs RFC-4180 quoting.
            ExportValue::Feeds(feeds) => {
                let mut first = true;
                for feed in feeds.iter() {
                    if !first {
                        line.push(';');
                    }
                    first = false;
                    line.push_str(feed);
                }
            }
            // Structured values are canonical compact JSON; quote the
            // field when it contains RFC-4180 specials.
            ExportValue::Structured(structured) => {
                let start = line.len();
                write_json_value(line, structured)?;
                let encoded = &line[start..];
                let needs_quotes = encoded
                    .bytes()
                    .any(|byte| byte == b',' || byte == b'"' || byte == b'\r' || byte == b'\n');
                if needs_quotes {
                    quote.clear();
                    quote.push('"');
                    for character in encoded.chars() {
                        if character == '"' {
                            quote.push('"');
                        }
                        quote.push(character);
                    }
                    quote.push('"');
                    line.truncate(start);
                    line.push_str(quote);
                }
            }
        }
    }
    writer.write_line(line, span)
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
    let mut addresses = Cardinality129::ZERO;
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
        addresses = addresses.checked_add(span_of(from, to)).map_err(|_| {
            HandlerError::new(
                "output_limit",
                "not_started",
                "export address cardinality exceeded the exact 129-bit counter",
            )
        })?;
        Ok(())
    })?;
    if records == 0 {
        // The released writer emits nothing for an empty set; the
        // destination is still atomically published as an empty file.
        return ExportWriter::create(destination, policy, budget)?.finish();
    }
    // The released header parses `unique ips` into a uint128 for IPv6
    // (src/ipset6_binary.c), so the 2^128 addresses of a full IPv6
    // space cannot be represented exactly; refuse before writing any
    // output instead of emitting a wrong count. IPv4's uint64 field
    // always fits (an IPv4 export holds at most 2^32 addresses).
    let unique_ips = u128::try_from(addresses).map_err(|_| {
        HandlerError::new(
            "output_limit",
            "not_started",
            "legacy_binary header stores unique ips as uint128; the exported full IPv6 space (340282366920938463463374607431768211456 addresses) cannot be represented exactly",
        )
    })?;
    let header = legacy_binary_header(ipv6, records, unique_ips);
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
        writer.write_chunk(header.as_bytes(), 0, Cardinality129::ZERO)?;
        writer
            .write_chunk(&LEGACY_ENDIANNESS_MARKER, 0, Cardinality129::ZERO)?;
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
                    ExportValue::Direct(range.value),
                )?;
            }
            Ok(())
        }
        ExportView::Structured => {
            let mut cursor =
                view_cursor(reader.network_enrichment_v1_cursor_v4(RangeDirection::Forward))?;
            // One catalog sweep for the whole stream and one reusable
            // membership-word buffer shared by every record.
            let snapshot = super::reader::build_feed_snapshot(reader)?;
            let mut words: Vec<u64> = Vec::new();
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let feeds =
                    super::reader::threat_feed_names(&range.value, &snapshot, &mut words)?;
                let value =
                    ExportValue::Structured(super::convert::enrichment_view(&range.value, &feeds));
                sink(u128::from(range.from.0), u128::from(range.to.0), value)?;
            }
            Ok(())
        }
        ExportView::Feed { name } => {
            require_feed(reader, name)?;
            let mut cursor =
                view_cursor(reader.feed_range_cursor_v4(name, RangeDirection::Forward))?;
            let value = ExportValue::Feeds(Arc::from(vec![name.clone()]));
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                sink(u128::from(range.from.0), u128::from(range.to.0), value.clone())?;
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
                &mut |from, to, names| {
                    sink(from, to, ExportValue::Feeds(Arc::from(names.to_vec())))
                },
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
                    ExportValue::Direct(range.value),
                )?;
            }
            Ok(())
        }
        ExportView::Structured => {
            let mut cursor =
                view_cursor(reader.network_enrichment_v1_cursor_v6(RangeDirection::Forward))?;
            // One catalog sweep for the whole stream and one reusable
            // membership-word buffer shared by every record.
            let snapshot = super::reader::build_feed_snapshot(reader)?;
            let mut words: Vec<u64> = Vec::new();
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                let feeds =
                    super::reader::threat_feed_names(&range.value, &snapshot, &mut words)?;
                let value =
                    ExportValue::Structured(super::convert::enrichment_view(&range.value, &feeds));
                sink(range.from.to_u128(), range.to.to_u128(), value)?;
            }
            Ok(())
        }
        ExportView::Feed { name } => {
            require_feed(reader, name)?;
            let mut cursor =
                view_cursor(reader.feed_range_cursor_v6(name, RangeDirection::Forward))?;
            let value = ExportValue::Feeds(Arc::from(vec![name.clone()]));
            while let Some(range) = sdk(cursor.next_range()).map_err(view_error)? {
                sink(range.from.to_u128(), range.to.to_u128(), value.clone())?;
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
                &mut |from, to, names| {
                    sink(from, to, ExportValue::Feeds(Arc::from(names.to_vec())))
                },
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
                    let mut seen: HashSet<&str> = HashSet::new();
                    for feed in feeds {
                        let name = feed
                            .as_str()
                            .ok_or("each selection feed must be a string")?;
                        FeedName::new(name).map_err(|error| error.to_string())?;
                        if !seen.insert(name) {
                            return Err("selection.feeds must be unique".into());
                        }
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
    // The budget must be exactly the canonical member set; indexing a
    // member that is not present panics on serde_json maps, and a
    // frame-valid request must never crash the worker (the Go product
    // answers -32602; regression pinned by
    // invalid_result_budget_member_set_answers_error).
    const CANONICAL: [&str; 3] = ["max_rows", "max_output_bytes", "max_open_files"];
    if budget.len() != CANONICAL.len()
        || CANONICAL.iter().any(|name| !budget.contains_key(*name))
    {
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

/// Worst-case JSON serialization of the export destination string.
///
/// `schema::encode_response_object` serializes with serde_json's
/// default string escaping: `"` and `\` double to two bytes, control
/// bytes escape to two or six, and every other byte passes through
/// raw. A byte at or above 0x7f is modeled at six bytes, an upper
/// bound of its raw pass-through and of any `\u` escape of the same
/// character, so the modeled length is never below the real
/// serialized length of the destination path.
fn worst_json_path(destination: &str) -> String {
    let mut worst = String::with_capacity(destination.len());
    for byte in destination.bytes() {
        match byte {
            b'"' | b'\\' => worst.push(byte as char),
            0x00..=0x1f => worst.push(byte as char),
            byte if byte < 0x7f => worst.push(byte as char),
            _ => worst.push_str("xxxxxx"),
        }
    }
    worst
}

/// Refuse an export whose worst-case complete inline result cannot fit
/// the 65,000-byte response object, before any file is opened or
/// created. The template uses the real destination string with its
/// worst-case JSON escaping and the real format name, widest count,
/// identity, and sha256 fields, and (for live sources) the widest
/// factual source-close shape; the shared helper echoes the real
/// request id.
fn preflight_export(
    state: &SessionState,
    format: &str,
    destination: &str,
    source_mode: &str,
) -> Result<(), HandlerError> {
    let mut worst = json!({
        "method": "iprange.v1.export",
        "path": worst_json_path(destination),
        "format": format,
        "sha256": "f".repeat(128),
        "rows": WIDEST_U64,
        "addresses": WIDEST_129,
        "bytes": WIDEST_U64,
        "identity": widest_identity(),
    });
    if source_mode == "live" {
        worst["source_close"] = widest_close_fact();
    }
    preflight_response(state, worst)
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
        inspect_recovery_candidates(source, mode, &inspection, &state.token()).map_err(read_error)?;
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
        // A wrong-name third member (frame-valid, wrong names) must be
        // rejected as an error, never panic the worker: the pre-fix
        // validator checked only the member count and indexed the
        // canonical names, panicking with "no entry found for key"
        // (product-level P1).
        let mut wrong_name = base.clone();
        wrong_name["result_budget"]["max_workspace_bytes"] = json!(1);
        wrong_name["result_budget"]
            .as_object_mut()
            .unwrap()
            .remove("max_open_files");
        assert!(validate_export(&wrong_name).is_err());
        let mut extra_member = base.clone();
        extra_member["result_budget"]["max_workspace_bytes"] = json!(1);
        assert!(validate_export(&extra_member).is_err());
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

    #[test]
    fn worst_json_path_is_exact_for_ascii_and_bounds_non_ascii() {
        // Printable ASCII passes through unchanged: the modeled
        // serialization equals the real one byte for byte.
        let ascii = "/example/export.netset";
        assert_eq!(worst_json_path(ascii), ascii);

        // For every tricky input the modeled serialization is never
        // shorter than serde_json's real serialization, so the
        // preflight bound is faithful.
        for destination in [
            "a\"b\\c\n\t\u{1f}",
            "greek-αβγ-emoji-😀",
            "\u{7f}",
            "中文字符",
            "mixed \"quote\" \\ \u{0} tail",
            "крайний путь /tmp/π-файл",
        ] {
            let worst = worst_json_path(destination);
            let real = serde_json::to_string(&serde_json::json!(destination)).unwrap();
            let modeled = serde_json::to_string(&serde_json::json!(worst)).unwrap();
            assert!(
                modeled.len() >= real.len(),
                "worst model ({} bytes) must bound the real serialization ({} bytes) for {destination:?}",
                modeled.len(),
                real.len()
            );
        }

        // A two-byte UTF-8 char is modeled at exactly six bytes per
        // byte (12 chars) plus the surrounding quotes.
        let worst = worst_json_path("π");
        assert_eq!(worst.len(), 12);
        assert_eq!(
            serde_json::to_string(&serde_json::json!(worst)).unwrap().len(),
            14
        );
    }
}

#[cfg(test)]
mod live_source_tests {
    use super::*;
    use iprange_livedb::snapshot::{SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode};
    use iprange_livedb::snapshot_to;
    use iprange_livedb::{
        create_live, AddressFamily, CancellationToken, FeedName, Ipv4Key, Ipv6Key, LiveWriter,
        MembershipOperation, NetworkEnrichmentV1, StructureKind, TransactionBudget, ValueKind,
        ValueTag,
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

    /// One live structured IPv4 database: three records whose threat
    /// memberships are {a,b}, {c}, and none.
    fn structured_live_v4(label: &str) -> PathBuf {
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
            ValueKind::Structured,
            StructureKind::NetworkEnrichmentV1,
            ValueTag::new(b"export-struct").unwrap(),
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
        let mut transaction = writer.begin_structured_transaction(&token).unwrap();
        let feed_a = transaction.ensure_feed(FeedName::new("feed-a").unwrap()).unwrap();
        let feed_b = transaction.ensure_feed(FeedName::new("feed-b").unwrap()).unwrap();
        let feed_c = transaction.ensure_feed(FeedName::new("feed-c").unwrap()).unwrap();
        let empty = transaction.empty_membership().unwrap();
        let membership_ab = transaction.add_feed(empty, feed_a).unwrap();
        let membership_ab = transaction.add_feed(membership_ab, feed_b).unwrap();
        let empty = transaction.empty_membership().unwrap();
        let membership_c = transaction.add_feed(empty, feed_c).unwrap();
        let structure_ab = transaction
            .intern_network_enrichment_v1(
                NetworkEnrichmentV1 {
                    asn: 1,
                    ..Default::default()
                },
                Some(membership_ab),
            )
            .unwrap();
        let structure_c = transaction
            .intern_network_enrichment_v1(
                NetworkEnrichmentV1 {
                    asn: 2,
                    ..Default::default()
                },
                Some(membership_c),
            )
            .unwrap();
        let structure_none = transaction
            .intern_network_enrichment_v1(
                NetworkEnrichmentV1 {
                    asn: 3,
                    ..Default::default()
                },
                None,
            )
            .unwrap();
        transaction
            .assign_v4(Ipv4Key(10), Ipv4Key(19), structure_ab)
            .unwrap();
        transaction
            .assign_v4(Ipv4Key(20), Ipv4Key(29), structure_c)
            .unwrap();
        transaction
            .assign_v4(Ipv4Key(30), Ipv4Key(39), structure_none)
            .unwrap();
        transaction.commit().unwrap();
        writer.close().unwrap();
        main
    }

    /// One live direct IPv6 database covering the complete address
    /// space with a single range record.
    fn live_direct_v6_full(label: &str) -> PathBuf {
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
            AddressFamily::Ipv6,
            ValueKind::Direct,
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
        let mut transaction = writer.begin_direct_transaction(&token).unwrap();
        transaction
            .assign_v6(
                Ipv6Key::from_u128(0),
                Ipv6Key::from_u128(u128::MAX),
                7,
            )
            .unwrap();
        transaction.commit().unwrap();
        writer.close().unwrap();
        main
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
        // A live source internally opened a reader: its factual close
        // result is part of the success (iprange-jsonrpc-v1.md).
        assert_eq!(result["source_close"]["outcome"], "closed");
        assert_eq!(
            fs::read_to_string(&destination).unwrap(),
            "from,to,value\n192.0.2.1,192.0.2.10,feed-a\n"
        );
        fs::remove_file(&destination).unwrap();
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }

    #[test]
    fn immutable_membership_export_has_no_source_close() {
        // One compact immutable snapshot of the live fixture: immutable
        // readers have no close fact, so the result omits source_close.
        let main = live_membership("immutable");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let immutable =
            std::env::temp_dir().join(format!("iprange-export-immutable-{unique}.iprange"));
        snapshot_to(
            &main,
            SnapshotSourceMode::Live,
            &immutable,
            SnapshotPublicationPolicy::FailIfExists,
            &SnapshotBudget::new(2 * 1024 * 1024, 10_000, 3),
            &CancellationToken::new(),
        )
        .map_err(|failure| failure.cause.detail.to_string())
        .unwrap();
        let destination =
            std::env::temp_dir().join(format!("iprange-export-immutable-out-{unique}.csv"));
        let mut state = SessionState::default();
        let result = export(
            &mut state,
            json!({
                "source": {"path": immutable.display().to_string(), "mode": "immutable"},
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
        assert_eq!(result["rows"], "1");
        assert!(result.get("source_close").is_none());
        fs::remove_file(&destination).unwrap();
        fs::remove_file(&immutable).unwrap();
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }

    #[test]
    fn close_failure_preserves_the_completed_export_report() {
        // A completed export must survive a failing source close with
        // both the report facts and the close result in `details`.
        let close_error = HandlerError {
            code: "io",
            outcome: "read_only_failure",
            message: "live reader close is incomplete".to_owned(),
            details: Some(json!({
                "source_close": {
                    "outcome": "close_incomplete",
                    "cleanup": {},
                    "coordination_cleanup": {},
                }
            })),
        };
        let report = json!({
            "method": "iprange.v1.export",
            "path": "/tmp/out.csv",
            "format": "csv",
            "sha256": "aa".repeat(32),
            "rows": "2",
            "addresses": "12",
            "bytes": "40",
            "identity": {"volume": "1", "file": "2"},
        });
        let error = super::super::reader::preserve_completed_report(close_error, report);
        assert_eq!((error.code, error.outcome), ("io", "read_only_failure"));
        let details = error.details.unwrap();
        assert_eq!(details["source_close"]["outcome"], "close_incomplete");
        assert_eq!(details["report"]["path"], "/tmp/out.csv");
        assert_eq!(details["report"]["format"], "csv");
        assert_eq!(details["report"]["rows"], "2");
        assert_eq!(details["report"]["addresses"], "12");
        assert_eq!(details["report"]["bytes"], "40");
        assert_eq!(
            details["report"]["identity"],
            json!({"volume": "1", "file": "2"})
        );
        assert_eq!(details["report"]["sha256"], "aa".repeat(32));
    }

    #[test]
    fn full_ipv6_ranges_export_reports_exact_129_bit_cardinality() {
        // A `::/0` ranges export covers 2^128 addresses, which exceeds
        // u128; the result must carry the exact decimal, never a
        // saturated count (binary-format-v4.md section 17).
        let main = live_direct_v6_full("full-v6");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let destination =
            std::env::temp_dir().join(format!("iprange-export-live-out-{unique}.ranges"));
        let mut state = SessionState::default();
        let result = export(
            &mut state,
            json!({
                "source": {"path": main.display().to_string(), "mode": "live"},
                "view": {"kind": "direct"},
                "format": "ranges",
                "destination": destination.display().to_string(),
                "publication_policy": "fail_if_exists",
                "result_budget": {
                    "max_rows": "10",
                    "max_output_bytes": "2000",
                    "max_open_files": 2
                }
            }),
        )
        .unwrap();
        assert_eq!(result["rows"], "1");
        assert_eq!(
            result["addresses"],
            "340282366920938463463374607431768211456"
        );
        let exported = fs::read_to_string(&destination).unwrap();
        assert_eq!(exported, "::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n");
        fs::remove_file(&destination).unwrap();
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }

    #[test]
    fn export_preflight_refuses_unanswerable_id_before_creating_destination() {
        // A valid request whose complete response cannot fit the
        // 65,000-byte response object (the echoed id alone nearly
        // fills it) must be refused with output_limit/not_started
        // BEFORE the source reader opens or the destination is
        // created. Previously the export published the file and only
        // then was relabeled output_limit/read_only_failure by the
        // post-hoc bound (final-review finding T1).
        let main = live_membership("preflight-id");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let destination = std::env::temp_dir().join(format!(
            "iprange-export-preflight-out-{unique}.csv"
        ));
        let mut state = SessionState::default();
        assert_eq!(state.active_request_id, None);
        state.active_request_id =
            Some(crate::rpc::schema::RequestId::String("R".repeat(64_530)));
        let error = export(
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
        .unwrap_err();
        assert_eq!(
            (error.code, error.outcome),
            ("output_limit", "not_started")
        );
        assert!(
            !destination.exists(),
            "preflight refusal must not create the destination"
        );
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }

    #[test]
    fn export_preflight_refuses_unrepresentable_destination_before_creating_output() {
        // A monster non-ASCII destination is schema-valid, but its
        // worst-case JSON escaping (six bytes per non-ASCII byte)
        // cannot fit the response object; the request must be refused
        // before any output file exists.
        let main = live_membership("preflight-path");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let destination = std::env::temp_dir().join(format!(
            "{}-export-many-π-{unique}.csv",
            "π".repeat(12_000)
        ));
        let mut state = SessionState::default();
        let error = export(
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
        .unwrap_err();
        assert_eq!(
            (error.code, error.outcome),
            ("output_limit", "not_started")
        );
        assert!(
            !destination.exists(),
            "preflight refusal must not create the destination"
        );
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }

    #[test]
    fn structured_export_is_byte_identical_to_one_catalog_sweep_stream() {
        // A structured export sweeps the catalog exactly once for the
        // whole stream (never per record), and every row's
        // `threat_feeds` value matches the catalog-order enumeration.
        let main = structured_live_v4("export-structured");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let destination =
            std::env::temp_dir().join(format!("iprange-export-structured-out-{unique}.jsonl"));
        let mut state = SessionState::default();
        let result = export(
            &mut state,
            json!({
                "source": {"path": main.display().to_string(), "mode": "live"},
                "view": {"kind": "structured"},
                "format": "jsonl",
                "destination": destination.display().to_string(),
                "publication_policy": "fail_if_exists",
                "result_budget": {
                    "max_rows": "10",
                    "max_output_bytes": "4000",
                    "max_open_files": 2
                }
            }),
        )
        .unwrap();
        assert_eq!(result["rows"], "3");
        assert_eq!(result["addresses"], "30");
        let exported = fs::read_to_string(&destination).unwrap();
        let mut lines = exported.lines();
        // The jsonl row format writes addresses as bare IPv4 text
        // (push_address), matching the golden wire shape.
        assert_eq!(
            lines.next().unwrap(),
            r#"{"from":0.0.0.10,"to":0.0.0.19,"value":{"asn":1,"city_id":0,"country_id":0,"location":null,"state_id":0,"threat_feeds":["feed-a","feed-b"]}}"#
        );
        assert_eq!(
            lines.next().unwrap(),
            r#"{"from":0.0.0.20,"to":0.0.0.29,"value":{"asn":2,"city_id":0,"country_id":0,"location":null,"state_id":0,"threat_feeds":["feed-c"]}}"#
        );
        assert_eq!(
            lines.next().unwrap(),
            r#"{"from":0.0.0.30,"to":0.0.0.39,"value":{"asn":3,"city_id":0,"country_id":0,"location":null,"state_id":0,"threat_feeds":[]}}"#
        );
        assert!(lines.next().is_none());
        fs::remove_file(&destination).unwrap();
        fs::remove_file(&main).unwrap();
        fs::remove_file(sidecar(&main)).unwrap();
    }
}
