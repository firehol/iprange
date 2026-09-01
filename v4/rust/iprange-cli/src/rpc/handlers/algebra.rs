//! Algebra, query, join, and history-projection JSON-RPC handlers
//! (iprange-jsonrpc-v1.md).
//!
//! Queries and joins stream their rows into one atomically published
//! output file (`result_budget`-bounded) while a pinned SDK scope scans;
//! algebra resolves one global same-name catalog over all sources. Every
//! internally opened live reader is closed before the response; success
//! results carry no `source_close` (the frozen result schemas define
//! exactly the members listed in v4/cli/schema/results.py).

use std::path::Path;

use iprange_livedb::error::ErrorCode;
use iprange_livedb::publication::{
    CleanupArtifacts, CleanupState, PrivateOutputAttempt, PublicationStatus,
};
use iprange_livedb::{
    CancellationToken, CommitResult, Error, FinishedHistoryProjection,
    FeedCardinality, FeedName, FeedOverlap, FeedPair, FeedSelection, HistoryProjectionReport,
    HistoryProjectionSource, HistoryWindow, HistoryWindowReport, ImmutableReader, LiveReader,
    LiveWriter, MembershipAggregateSink, MembershipAggregationMode,
    MembershipAggregationReport, MembershipAlgebra, MembershipAlgebraBudget, MembershipCrossCell,
    MembershipJoinReport, MembershipJoinSink, MembershipQueryBudget, MembershipScope,
    PreparedHistoryProjection, UncoveredFeed, UncoveredSide,
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraPreparationFailure, AlgebraSetOperation, AlgebraSetReport, DirectJoinBudget,
    DirectJoinCell, DirectJoinReport, DirectJoinSink, DirectJoinSource,
};
use std::fmt::Write as _;

use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::{CursorPoint, ReaderValue};
use super::convert;
use super::lifecycle;
use super::live::close_writer_facts;
use super::reader;
use super::snapshot;
use super::workflow::{close_writer, finish_publisher, finish_writer_error, logical_change, publish_changed, publish_no_change, workflow_failure, CommitDraft};
use crate::io::export_writer::{
    push_json_string, write_csv_field, ExportBudget, ExportFacts, ExportWriter,
};
use iprange_livedb::Ipv4Key as V4Key;
use iprange_livedb::Ipv6Key as V6Key;

// ---------------------------------------------------------------------------
// Strict params validators (each maps to the frozen methods.py schema).
// ---------------------------------------------------------------------------

pub fn validate_algebra_count(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["sources", "selection", "algebra_budget"])?;
    validate_sources(&object["sources"])?;
    validate_selection(&object["selection"])?;
    validate_algebra_budget(&object["algebra_budget"], &object["sources"])
}

pub fn validate_algebra_compare(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["sources", "left", "right", "algebra_budget"])?;
    validate_sources(&object["sources"])?;
    validate_selection(&object["left"])?;
    validate_selection(&object["right"])?;
    validate_algebra_budget(&object["algebra_budget"], &object["sources"])
}

pub fn validate_algebra_publish(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &[
            "sources",
            "operation",
            "output_mode",
            "value_tag",
            "metadata",
            "destination",
            "publication_policy",
            "algebra_budget",
            "algebra_output_budget",
        ],
    )?;
    validate_sources(&object["sources"])?;
    validate_algebra_budget(&object["algebra_budget"], &object["sources"])?;
    validate_operation(&object["operation"])?;
    validate_output_mode(&object["output_mode"])?;
    lifecycle::validate_value_tag(&object["value_tag"])?;
    lifecycle::validate_metadata(&object["metadata"], false)?;
    reader::validate_path(object["destination"].as_str())?;
    reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "publication_policy is invalid".to_string())?;
    validate_algebra_output_budget(&object["algebra_output_budget"])?;
    Ok(())
}

pub fn validate_query_cardinalities(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &["source", "selection", "membership_query_budget", "output"],
    )?;
    validate_source(&object["source"])?;
    validate_selection(&object["selection"])?;
    validate_membership_query_budget(&object["membership_query_budget"])?;
    validate_output(&object["output"])
}

pub fn validate_query_overlaps(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &["source", "selection", "membership_query_budget", "mode", "output"],
    )?;
    validate_source(&object["source"])?;
    validate_selection(&object["selection"])?;
    validate_membership_query_budget(&object["membership_query_budget"])?;
    validate_overlaps_mode(&object["mode"])?;
    validate_output(&object["output"])
}

pub fn validate_query_matching_feeds(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["source", "addresses", "output"])?;
    validate_source(&object["source"])?;
    validate_addresses(&object["addresses"])?;
    validate_output(&object["output"])
}

pub fn validate_join_direct(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &["membership", "direct", "output", "max_result_cells"],
    )?;
    validate_join_side(&object["membership"])?;
    validate_source(&object["direct"])?;
    reader::u64_string(object["max_result_cells"].as_str())
        .map_err(|_| "max_result_cells must be a canonical unsigned decimal string".to_string())?;
    validate_output(&object["output"])
}

pub fn validate_join_membership(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["left", "right", "output"])?;
    validate_join_side(&object["left"])?;
    validate_join_side(&object["right"])?;
    validate_output(&object["output"])
}

pub fn validate_history_project(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &["path", "last_seen", "windows", "metadata", "writer_budget"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    validate_source(&object["last_seen"])?;
    validate_windows(&object["windows"])?;
    lifecycle::validate_metadata(&object["metadata"], true)?;
    lifecycle::validate_writer_budget(&object["writer_budget"])
}

fn validate_sources(value: &Value) -> Result<(), String> {
    let sources = value.as_array().ok_or("sources must be an array")?;
    if sources.is_empty() {
        return Err("sources must contain at least one entry".into());
    }
    for (index, source) in sources.iter().enumerate() {
        let object = reader::exact_object(source, &["source", "scope", "membership_query_budget"])
            .map_err(|error| format!("sources[{index}]: {error}"))?;
        validate_source(&object["source"]).map_err(|error| format!("sources[{index}]: {error}"))?;
        let scope = reader::exact_object(&object["scope"], &["mode"])
            .map_err(|error| format!("sources[{index}].scope: {error}"))?;
        if scope["mode"].as_str() != Some("all") {
            return Err(format!("sources[{index}].scope.mode must be all"));
        }
        validate_membership_query_budget(&object["membership_query_budget"])
            .map_err(|error| format!("sources[{index}]: {error}"))?;
    }
    Ok(())
}

fn validate_algebra_budget(value: &Value, sources: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["max_heap_bytes", "max_sources"])?;
    reader::positive_u64_string(object["max_heap_bytes"].as_str())
        .map_err(|error| format!("algebra_budget.max_heap_bytes: {error}"))?;
    let maximum = reader::positive_u32(&object["max_sources"])
        .map_err(|error| format!("algebra_budget.max_sources: {error}"))?;
    let count = sources.as_array().map(|array| array.len()).unwrap_or(0);
    if !(1..=maximum as usize).contains(&count) {
        return Err(format!("source count {count} outside 1..{maximum}"));
    }
    Ok(())
}

fn validate_algebra_output_budget(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["max_output_pages", "max_open_files"])?;
    reader::positive_u64_string(object["max_output_pages"].as_str())
        .map_err(|error| format!("algebra_output_budget.max_output_pages: {error}"))?;
    reader::positive_u32(&object["max_open_files"])
        .map_err(|error| format!("algebra_output_budget.max_open_files: {error}"))?;
    Ok(())
}

fn validate_membership_query_budget(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["max_heap_bytes"])?;
    reader::positive_u64_string(object["max_heap_bytes"].as_str())
        .map_err(|error| format!("membership_query_budget.max_heap_bytes: {error}"))?;
    Ok(())
}

fn validate_source(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["path", "mode"])?;
    reader::validate_path(object["path"].as_str())?;
    match object["mode"].as_str() {
        Some("immutable") | Some("live") => Ok(()),
        _ => Err("source.mode must be immutable or live".into()),
    }
}

fn validate_selection(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("selection must be an object")?;
    match object.get("mode").and_then(Value::as_str) {
        Some("all") => reader::exact_object(value, &["mode"]).map(|_| ()),
        Some("named") => {
            reader::exact_object(value, &["mode", "feeds"])?;
            let feeds = object["feeds"]
                .as_array()
                .ok_or("selection.feeds must be an array")?;
            if feeds.is_empty() {
                return Err("selection.feeds must contain at least one feed".into());
            }
            let mut seen: std::collections::HashSet<&str> = std::collections::HashSet::new();
            for feed in feeds {
                let name = feed
                    .as_str()
                    .ok_or("each selection feed must be a string")?;
                validate_feed_name(Some(name))?;
                if !seen.insert(name) {
                    return Err("selection.feeds must be unique".into());
                }
            }
            Ok(())
        }
        _ => Err("selection.mode must be all or named".into()),
    }
}

fn validate_feed_name(value: Option<&str>) -> Result<(), String> {
    let feed = value.ok_or("feed must be a string")?;
    let bytes = feed.as_bytes();
    let valid = (1..=255).contains(&bytes.len())
        && bytes
            .first()
            .is_some_and(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
        && bytes
            .last()
            .is_some_and(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
        && bytes.iter().all(|byte| {
            byte.is_ascii_lowercase()
                || byte.is_ascii_digit()
                || matches!(*byte, b'_' | b'-' | b'.')
        });
    valid
        .then_some(())
        .ok_or_else(|| "feed does not use the v4 FeedName grammar".into())
}

fn validate_output(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        value,
        &["path", "format", "publication_policy", "result_budget"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    match object["format"].as_str() {
        Some("jsonl") | Some("csv") => {}
        _ => return Err("output.format must be jsonl or csv".into()),
    }
    reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "output.publication_policy is invalid".to_string())?;
    let budget = reader::exact_object(
        &object["result_budget"],
        &["max_rows", "max_output_bytes", "max_open_files"],
    )?;
    reader::positive_u64_string(budget["max_rows"].as_str())
        .map_err(|error| format!("result_budget.max_rows: {error}"))?;
    reader::positive_u64_string(budget["max_output_bytes"].as_str())
        .map_err(|error| format!("result_budget.max_output_bytes: {error}"))?;
    reader::positive_u32(&budget["max_open_files"])
        .map_err(|error| format!("result_budget.max_open_files: {error}"))?;
    Ok(())
}

fn validate_addresses(value: &Value) -> Result<(), String> {
    let addresses = value.as_array().ok_or("addresses must be an array")?;
    if addresses.is_empty() || addresses.len() > 4096 {
        return Err("addresses must contain 1 through 4096 values".into());
    }
    for address in addresses {
        reader::parse_address(address.as_str().ok_or("each address must be a string")?)?;
    }
    Ok(())
}

fn validate_join_side(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        value,
        &["source", "selection", "membership_query_budget"],
    )?;
    validate_source(&object["source"])?;
    validate_selection(&object["selection"])?;
    validate_membership_query_budget(&object["membership_query_budget"])
}

fn validate_overlaps_mode(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("mode must be an object")?;
    match object.get("kind").and_then(Value::as_str) {
        Some("all_pairs") => reader::exact_object(value, &["kind"]).map(|_| ()),
        Some("target") => {
            reader::exact_object(value, &["kind", "target_feed"])?;
            validate_feed_name(object["target_feed"].as_str())
        }
        Some("selected_pairs") => {
            reader::exact_object(value, &["kind", "pairs"])?;
            let pairs = object["pairs"]
                .as_array()
                .ok_or("mode.pairs must be an array")?;
            if pairs.is_empty() {
                return Err("mode.pairs must contain at least one pair".into());
            }
            let mut normalized: std::collections::HashSet<(&str, &str)> =
                std::collections::HashSet::new();
            for pair in pairs {
                let pair_object = reader::exact_object(pair, &["left", "right"])
                    .map_err(|error| format!("mode.pairs: {error}"))?;
                let left = pair_object["left"]
                    .as_str()
                    .ok_or("mode.pairs left must be a string")?;
                let right = pair_object["right"]
                    .as_str()
                    .ok_or("mode.pairs right must be a string")?;
                validate_feed_name(Some(left))?;
                validate_feed_name(Some(right))?;
                if left == right {
                    return Err("pair left and right feeds must differ".into());
                }
                let entry = if left < right { (left, right) } else { (right, left) };
                if !normalized.insert(entry) {
                    return Err("unordered pairs must be unique".into());
                }
            }
            Ok(())
        }
        _ => Err("mode.kind must be all_pairs, target, or selected_pairs".into()),
    }
}

fn validate_operation(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("operation must be an object")?;
    match object.get("kind").and_then(Value::as_str) {
        Some("union") | Some("intersection") => {
            reader::exact_object(value, &["kind", "selection"])?;
            validate_selection(&object["selection"])
        }
        Some("exclusion") => {
            reader::exact_object(value, &["kind", "included", "excluded"])?;
            validate_selection(&object["included"])?;
            validate_selection(&object["excluded"])
        }
        _ => Err("operation.kind must be union, intersection, or exclusion".into()),
    }
}

fn validate_output_mode(value: &Value) -> Result<(), String> {
    let object = value.as_object().ok_or("output_mode must be an object")?;
    match object.get("kind").and_then(Value::as_str) {
        Some("preserve_feeds") => reader::exact_object(value, &["kind"]).map(|_| ()),
        Some("flat") => {
            reader::exact_object(value, &["kind", "feed"])?;
            validate_feed_name(object["feed"].as_str())
        }
        _ => Err("output_mode.kind must be preserve_feeds or flat".into()),
    }
}

fn validate_windows(value: &Value) -> Result<(), String> {
    let windows = value.as_array().ok_or("windows must be an array")?;
    if windows.is_empty() || windows.len() > 4096 {
        return Err("windows must contain 1 through 4096 values".into());
    }
    let mut seen: std::collections::HashSet<&str> = std::collections::HashSet::new();
    for (index, window) in windows.iter().enumerate() {
        let object = reader::exact_object(window, &["feed", "cutoff"])
            .map_err(|error| format!("windows[{index}]: {error}"))?;
        validate_feed_name(object["feed"].as_str())?;
        let feed = object["feed"].as_str().expect("validated feed");
        if !seen.insert(feed) {
            return Err("window feed names must be unique".into());
        }
        if object["cutoff"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .is_none()
        {
            return Err(format!("windows[{index}].cutoff must be u32"));
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Query and join handlers (tabular outputs through one atomic writer).
// ---------------------------------------------------------------------------

pub fn query_cardinalities(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let (path, mode) = source_parts(object, "source")?;
    let selection = decode_selection(&object["selection"])?;
    let budget = decode_membership_budget(&object["membership_query_budget"])?;
    let spec = decode_output(&object["output"])?;
    let mut reader = open_temporary(&path, &mode, state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let mut writer = ExportWriter::create(&spec.path, spec.policy, &spec.budget)?;
        if !spec.jsonl {
            writer.write_chunk(b"feed,addresses\n", 0, 0)?;
        }
        let scope = resolve_scope(&reader, &selection, budget, state)?;
        let mut captured = None;
        let outcome = {
            let mut sink = CardinalitySink {
                writer: &mut writer,
                jsonl: spec.jsonl,
                slot: &mut captured,
                line: String::new(),
            };
            sdk(scope.aggregate(
                MembershipAggregationMode::Cardinalities,
                &mut sink,
                &state.token,
            ))
        };
        if let Some(error) = captured.take() {
            return Err(error);
        }
        let report = outcome?;
        drop(scope);
        let facts = writer.finish()?;
        Ok(json!({
            "method": "iprange.v1.query.cardinalities",
            "output": output_facts(&facts),
            "report": aggregation_report(&report),
        }))
    })();
    match result {
        Ok(report) => close_reader(&mut reader, report),
        Err(error) => Err(reader::close_on_error(
            std::slice::from_mut(&mut reader),
            error,
        )),
    }
}

pub fn query_overlaps(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let (path, mode) = source_parts(object, "source")?;
    let selection = decode_selection(&object["selection"])?;
    let budget = decode_membership_budget(&object["membership_query_budget"])?;
    let spec = decode_output(&object["output"])?;
    let mut reader = open_temporary(&path, &mode, state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let mut writer = ExportWriter::create(&spec.path, spec.policy, &spec.budget)?;
        if !spec.jsonl {
            writer.write_chunk(b"left,right,addresses\n", 0, 0)?;
        }
        let scope = resolve_scope(&reader, &selection, budget, state)?;
        let mode = decode_overlap_mode(&object["mode"])?;
        let mut captured = None;
        let outcome = {
            let mut sink = OverlapSink {
                writer: &mut writer,
                jsonl: spec.jsonl,
                slot: &mut captured,
                line: String::new(),
            };
            let aggregation = match &mode {
                OverlapMode::AllPairs => MembershipAggregationMode::AllPairs,
                OverlapMode::Target(feed) => MembershipAggregationMode::TargetAgainstScope(*feed),
                OverlapMode::SelectedPairs(pairs) => {
                    MembershipAggregationMode::SelectedPairs(pairs)
                }
            };
            sdk(scope.aggregate(aggregation, &mut sink, &state.token))
        };
        if let Some(error) = captured.take() {
            return Err(error);
        }
        let report = outcome?;
        drop(scope);
        let facts = writer.finish()?;
        Ok(json!({
            "method": "iprange.v1.query.overlaps",
            "output": output_facts(&facts),
            "report": aggregation_report(&report),
        }))
    })();
    match result {
        Ok(report) => close_reader(&mut reader, report),
        Err(error) => Err(reader::close_on_error(
            std::slice::from_mut(&mut reader),
            error,
        )),
    }
}

pub fn query_matching_feeds(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let (path, mode) = source_parts(object, "source")?;
    let spec = decode_output(&object["output"])?;
    let mut reader = open_temporary(&path, &mode, state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let mut writer = ExportWriter::create(&spec.path, spec.policy, &spec.budget)?;
        if !spec.jsonl {
            writer.write_chunk(b"address,feeds\n", 0, 0)?;
        }
        let query = sdk(reader.membership_query())?;
        let addresses = object["addresses"]
            .as_array()
            .expect("validator checked addresses");
        let mut captured = None;
        let mut matching_feed_count = 0u64;
        let mut names: Vec<String> = Vec::new();
        let mut line = String::with_capacity(160);
        for address in addresses {
            let text = address
                .as_str()
                .expect("validator checked address canonicality");
            let point = reader::parse_address(text).map_err(HandlerError::invalid_params)?;
            names.clear();
            let report = match point {
                CursorPoint::V4(value) => sdk(query.matching_feeds_v4(
                    V4Key(value),
                    &mut |feed: FeedName| {
                        names.push(feed.as_str().to_owned());
                        Ok(())
                    },
                    &state.token,
                ))?,
                CursorPoint::V6(value) => sdk(query.matching_feeds_v6(
                    V6Key::from_u128(value),
                    &mut |feed: FeedName| {
                        names.push(feed.as_str().to_owned());
                        Ok(())
                    },
                    &state.token,
                ))?,
            };
            line.clear();
            if spec.jsonl {
                line.push_str("{\"address\":");
                push_json_string(&mut line, text);
                line.push_str(",\"feeds\":[");
                for (index, name) in names.iter().enumerate() {
                    if index != 0 {
                        line.push(',');
                    }
                    push_json_string(&mut line, name);
                }
                line.push_str("]}");
            } else {
                write_csv_field(&mut line, text);
                line.push(',');
                for (index, name) in names.iter().enumerate() {
                    if index != 0 {
                        line.push(';');
                    }
                    line.push_str(name);
                }
            }
            if let Err(error) = writer.write_line(&line, 1) {
                captured = Some(error);
                break;
            }
            matching_feed_count = matching_feed_count.saturating_add(report.matching_feed_count);
        }
        let _ = query;
        if let Some(error) = captured.take() {
            return Err(error);
        }
        let facts = writer.finish()?;
        Ok(json!({
            "method": "iprange.v1.query.matching_feeds",
            "output": output_facts(&facts),
            "matching_feed_count": matching_feed_count.to_string(),
        }))
    })();
    match result {
        Ok(report) => close_reader(&mut reader, report),
        Err(error) => Err(reader::close_on_error(
            std::slice::from_mut(&mut reader),
            error,
        )),
    }
}

pub fn join_direct(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let membership = reader::member_object(object, "membership")
        .map_err(HandlerError::invalid_params)?;
    let selection = decode_selection(&membership["selection"])?;
    let budget = decode_membership_budget(&membership["membership_query_budget"])?;
    let (membership_path, membership_mode) = source_parts(membership, "source")?;
    let (direct_path, direct_mode) = source_parts(object, "direct")?;
    let max_result_cells = reader::u64_string(object["max_result_cells"].as_str())
        .map_err(HandlerError::invalid_params)?;
    let spec = decode_output(&object["output"])?;
    let mut membership_reader = open_temporary(&membership_path, &membership_mode, state)?;
    let direct_reader = match open_temporary(&direct_path, &direct_mode, state) {
        Ok(reader) => reader,
        Err(error) => {
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut membership_reader),
                error,
            ))
        }
    };
    let result = (|| -> Result<Value, HandlerError> {
        let mut writer = ExportWriter::create(&spec.path, spec.policy, &spec.budget)?;
        if !spec.jsonl {
            writer.write_chunk(b"feed,direct_value,addresses\n", 0, 0)?;
        }
        let scope = resolve_scope(&membership_reader, &selection, budget, state)?;
        let mut captured = None;
        let outcome = {
            let mut sink = DirectJoinSinkImpl {
                writer: &mut writer,
                jsonl: spec.jsonl,
                slot: &mut captured,
                line: String::new(),
            };
            let source = match &direct_reader {
                ReaderValue::Immutable(reader) => DirectJoinSource::Immutable(reader),
                ReaderValue::Live(reader) => DirectJoinSource::Live(reader),
            };
            sdk(scope.join_direct(
                source,
                DirectJoinBudget { max_result_cells },
                &mut sink,
                &state.token,
            ))
        };
        if let Some(error) = captured.take() {
            return Err(error);
        }
        let report = outcome?;
        drop(scope);
        let facts = writer.finish()?;
        Ok(json!({
            "method": "iprange.v1.join.direct",
            "output": output_facts(&facts),
            "report": direct_join_report(&report),
        }))
    })();
    match result {
        Ok(report) => close_readers(vec![membership_reader, direct_reader], report),
        Err(error) => Err(reader::close_on_error(
            &mut [membership_reader, direct_reader],
            error,
        )),
    }
}

pub fn join_membership(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let left = reader::member_object(object, "left")
        .map_err(HandlerError::invalid_params)?;
    let right = reader::member_object(object, "right")
        .map_err(HandlerError::invalid_params)?;
    let left_selection = decode_selection(&left["selection"])?;
    let right_selection = decode_selection(&right["selection"])?;
    let left_budget = decode_membership_budget(&left["membership_query_budget"])?;
    let right_budget = decode_membership_budget(&right["membership_query_budget"])?;
    let (left_path, left_mode) = source_parts(left, "source")?;
    let (right_path, right_mode) = source_parts(right, "source")?;
    let spec = decode_output(&object["output"])?;
    let mut left_reader = open_temporary(&left_path, &left_mode, state)?;
    let right_reader = match open_temporary(&right_path, &right_mode, state) {
        Ok(reader) => reader,
        Err(error) => {
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut left_reader),
                error,
            ))
        }
    };
    let result = (|| -> Result<Value, HandlerError> {
        let left_scope = resolve_scope(&left_reader, &left_selection, left_budget, state)?;
        let right_scope = resolve_scope(&right_reader, &right_selection, right_budget, state)?;
        let mut writer = ExportWriter::create(&spec.path, spec.policy, &spec.budget)?;
        if !spec.jsonl {
            writer.write_chunk(b"kind,left,right,side,feed,addresses\n", 0, 0)?;
        }
        let mut captured = None;
        let outcome = {
            let mut sink = MembershipJoinSinkImpl {
                writer: &mut writer,
                jsonl: spec.jsonl,
                slot: &mut captured,
                line: String::new(),
            };
            sdk(left_scope.join_membership(&right_scope, &mut sink, &state.token))
        };
        if let Some(error) = captured.take() {
            return Err(error);
        }
        let report = outcome?;
        drop(left_scope);
        drop(right_scope);
        let facts = writer.finish()?;
        Ok(json!({
            "method": "iprange.v1.join.membership",
            "output": output_facts(&facts),
            "report": membership_join_report(&report),
        }))
    })();
    match result {
        Ok(report) => close_readers(vec![left_reader, right_reader], report),
        Err(error) => Err(reader::close_on_error(
            &mut [left_reader, right_reader],
            error,
        )),
    }
}

// ---------------------------------------------------------------------------
// Algebra handlers.
// ---------------------------------------------------------------------------

pub fn algebra_count(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let selection = decode_selection(&object["selection"])?;
    let budget = decode_algebra_budget(&object["algebra_budget"])?;
    let mut readers = open_sources(&object["sources"], state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let scopes = resolve_algebra_scopes(&readers, &object["sources"], state)?;
        let refs: Vec<&MembershipScope<'_>> = scopes.iter().collect();
        let algebra = sdk(MembershipAlgebra::new(&refs, budget, &state.token))?;
        let report = sdk(algebra.count(feed_selection(&selection), &state.token))?;
        Ok(json!({
            "method": "iprange.v1.algebra.count",
            "report": count_report(&report),
            "cardinality": report.addresses.to_string(),
        }))
    })();
    match result {
        Ok(report) => close_readers(readers, report),
        Err(error) => Err(reader::close_on_error(&mut readers, error)),
    }
}

pub fn algebra_compare(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let left = decode_selection(&object["left"])?;
    let right = decode_selection(&object["right"])?;
    let budget = decode_algebra_budget(&object["algebra_budget"])?;
    let mut readers = open_sources(&object["sources"], state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let scopes = resolve_algebra_scopes(&readers, &object["sources"], state)?;
        let refs: Vec<&MembershipScope<'_>> = scopes.iter().collect();
        let algebra = sdk(MembershipAlgebra::new(&refs, budget, &state.token))?;
        let report = sdk(algebra.compare(
            feed_selection(&left),
            feed_selection(&right),
            &state.token,
        ))?;
        Ok(json!({
            "method": "iprange.v1.algebra.compare",
            "report": comparison_report(&report),
        }))
    })();
    match result {
        Ok(report) => close_readers(readers, report),
        Err(error) => Err(reader::close_on_error(&mut readers, error)),
    }
}

pub fn algebra_publish(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let budget = decode_algebra_budget(&object["algebra_budget"])?;
    // The complete result carries one live-close fact per opened live
    // source; refuse an unrepresentable request before any source is
    // opened or the destination is published.
    let source_count = object["sources"]
        .as_array()
        .map(|sources| sources.len())
        .ok_or_else(|| HandlerError::invalid_params("sources must be an array"))?;
    preflight_algebra_publish(state, source_count)?;
    let mut readers = open_sources(&object["sources"], state)?;
    let result = (|| -> Result<Value, HandlerError> {
        let scopes = resolve_algebra_scopes(&readers, &object["sources"], state)?;
        let refs: Vec<&MembershipScope<'_>> = scopes.iter().collect();
        let algebra = sdk(MembershipAlgebra::new(&refs, budget, &state.token))?;
        let destination = object["destination"]
            .as_str()
            .ok_or_else(|| HandlerError::invalid_params("destination must be a string"))?;
        require_publication_parent(Path::new(destination))?;
        let value_tag = lifecycle::value_tag(&object["value_tag"])
            .map_err(HandlerError::invalid_params)?;
        let metadata = match lifecycle::metadata_value(&object["metadata"])? {
            lifecycle::MetadataValue::Keep => None,
            lifecycle::MetadataValue::Clear => None,
            lifecycle::MetadataValue::Replace(bytes) => Some(bytes),
        };
        let operation = decode_operation(&object["operation"])?;
        let output_mode = decode_output_mode(&object["output_mode"])?;
        let policy = reader::publication_policy(object["publication_policy"].as_str())
            .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
        let output_budget = decode_algebra_output_budget(&object["algebra_output_budget"])?;
        let outcome = algebra.publish_set(
            destination,
            value_tag,
            algebra_operation(&operation),
            algebra_output_mode(output_mode),
            metadata.as_deref(),
            policy,
            output_budget,
            &state.token,
        );
        let result = match outcome {
            Ok(result) => result,
            Err(failure) => return Err(algebra_preparation_error(&failure)),
        };
        let publication = snapshot::publication_result(&result.publication);
        if result.publication.publication != PublicationStatus::Published
            || result.publication.cause.is_some()
        {
            let cause = result.publication.cause.as_ref();
            let code = cause.map_or("io", |error| publication_code(error.code));
            let message = cause.map_or_else(
                || "algebra publication did not complete".to_owned(),
                |error| error.detail.to_string(),
            );
            return Err(HandlerError {
                code,
                outcome: publication_outcome(result.publication.publication),
                message,
                details: Some(json!({
                    "report": set_report(&result.report),
                    "publication": publication,
                })),
            });
        }
        Ok(json!({
            "method": "iprange.v1.algebra.publish",
            "report": set_report(&result.report),
            "publication": publication,
        }))
    })();
    match result {
        Ok(report) => close_readers(readers, report),
        Err(error) => Err(reader::close_on_error(&mut readers, error)),
    }
}

// ---------------------------------------------------------------------------
// History projection handler (live writer publisher family).
// ---------------------------------------------------------------------------

pub fn history_project(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let path = object["path"]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params("path must be a string"))?;
    require_existing_database(Path::new(path))?;
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;
    let (source_path, source_mode) = source_parts(object, "last_seen")?;
    let windows = decode_windows(&object["windows"])?;
    // The complete report grows linearly with the window count; refuse
    // a request whose worst-case inline result cannot fit the response
    // object ceiling BEFORE any writer is opened or mutation runs, so
    // a committed workflow is never relabeled as a read-only failure.
    preflight_history_result(state, &windows)?;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let mut reader = match open_temporary(&source_path, &source_mode, state) {
        Ok(reader) => reader,
        Err(error) => return Err(close_writer_facts(&mut writer, error)),
    };
    // SDK projections own the writer borrow, so the result is consumed
    // inside a collector that touches only the reader; the writer is
    // re-borrowed by the finisher after the projection borrow has ended.
    let outcome = match &reader {
        ReaderValue::Immutable(source) => sdk(writer.project_history(
            HistoryProjectionSource::Immutable(source),
            &windows,
            &state.token,
        )),
        ReaderValue::Live(source) => sdk(writer.project_history(
            HistoryProjectionSource::Live(source),
            &windows,
            &state.token,
        )),
    };
    let facts = collect_projection_facts(&mut reader, outcome, &metadata);
    finish_projection_facts(&mut writer, facts, &metadata, &state.token)
}

/// Carry the factual live source close into a publisher outcome: the
/// success result gets `source_closes` (schema CLOSE_RESULT_LIST); a
/// product error preserves it in `details`.
fn with_source_close(
    outcome: Result<Value, HandlerError>,
    source_close: Option<&Value>,
) -> Result<Value, HandlerError> {
    let Some(close) = source_close else {
        return outcome;
    };
    match outcome {
        Ok(mut value) => {
            value
                .as_object_mut()
                .expect("method results are objects")
                .insert("source_closes".into(), Value::Array(vec![close.clone()]));
            Ok(value)
        }
        Err(mut error) => {
            let mut details = error.details.take().unwrap_or_else(|| json!({}));
            if let Some(members) = details.as_object_mut() {
                members.insert(
                    "source_closes".into(),
                    Value::Array(vec![close.clone()]),
                );
            }
            error.details = Some(details);
            Err(error)
        }
    }
}

// ---------------------------------------------------------------------------
// Publisher result machinery (live writer mutations).
// ---------------------------------------------------------------------------

/// Borrow-free outcome of a completed history projection. The SDK's
/// finished projection owns a writer borrow and runs a destructor, so
/// every factual piece is moved out of the match on the SDK result; the
/// writer is re-borrowed only when these facts are finished.
enum ProjectionFacts {
    NoChange {
        report: Value,
        source_close: Option<Value>,
    },
    Changed {
        report: Value,
        metadata_logical_change: &'static str,
        commit: Option<std::result::Result<CommitResult, Error>>,
        source_close: Option<Value>,
    },
    Failed {
        report: Option<Value>,
        error: HandlerError,
        source_close: Option<Value>,
    },
    ReaderCloseFailed {
        report: Value,
        close_error: HandlerError,
    },
}

/// Consume a completed projection: close the ephemeral last-seen reader,
/// apply the requested metadata through the prepared draft, and return
/// borrow-free facts. The writer is only re-borrowed by
/// `finish_projection_facts`, after the projection borrow has ended.
fn collect_projection_facts(
    reader: &mut ReaderValue,
    outcome: std::result::Result<FinishedHistoryProjection<'_>, HandlerError>,
    metadata: &lifecycle::MetadataValue,
) -> ProjectionFacts {
    let source_close = match reader::close_ephemeral_reader(reader) {
        Ok(close) => close,
        Err(close_error) => {
            if let Ok(projection) = outcome {
                let report = history_projection_report(projection.report());
                drop(projection);
                return ProjectionFacts::ReaderCloseFailed { report, close_error };
            }
            // The projection and the reader close both failed: keep the
            // projection error primary and merge the factual close
            // result it carried into the error details, so no close
            // failure evidence is dropped on the double-fault path.
            let mut error = match outcome {
                Err(error) => error,
                Ok(_) => unreachable!("handled above"),
            };
            if let Some(mut close_details) = close_error.details {
                if let Some(close_fact) = close_details
                    .as_object_mut()
                    .and_then(|members| members.remove("source_close"))
                {
                    let mut details = error.details.take().unwrap_or_else(|| json!({}));
                    if let Some(members) = details.as_object_mut() {
                        members.insert("source_close".into(), close_fact);
                    }
                    error.details = Some(details);
                }
            }
            return ProjectionFacts::Failed {
                report: None,
                error,
                source_close: None,
            };
        }
    };
    match outcome {
        Ok(projection) => {
            let report = history_projection_report(projection.report());
            match projection {
                FinishedHistoryProjection::NoChange(_) => {
                    ProjectionFacts::NoChange { report, source_close }
                }
                FinishedHistoryProjection::Changed(prepared) => {
                    match publish_changed(prepared, metadata) {
                        Ok((metadata_logical_change, commit)) => ProjectionFacts::Changed {
                            report,
                            metadata_logical_change,
                            commit,
                            source_close,
                        },
                        Err(error) => ProjectionFacts::Failed {
                            report: Some(report),
                            error,
                            source_close,
                        },
                    }
                }
            }
        }
        Err(error) => ProjectionFacts::Failed {
            report: None,
            error,
            source_close,
        },
    }
}

/// Finish one completed projection: commit no-change metadata, close the
/// writer, and convert the commit/close facts into the wire result.
fn finish_projection_facts(
    writer: &mut LiveWriter,
    facts: ProjectionFacts,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<Value, HandlerError> {
    match facts {
        ProjectionFacts::NoChange {
            report,
            source_close,
        } => {
            let outcome = match publish_no_change(writer, metadata, token) {
                Ok((metadata_logical_change, commit)) => finish_publisher(
                    writer,
                    "iprange.v1.history.project",
                    Some(&report),
                    metadata_logical_change,
                    commit,
                ),
                Err(error) => Err(finish_writer_error(writer, error, &report)),
            };
            with_source_close(outcome, source_close.as_ref())
        }
        ProjectionFacts::Changed {
            report,
            metadata_logical_change,
            commit,
            source_close,
        } => with_source_close(
            finish_publisher(
                writer,
                "iprange.v1.history.project",
                Some(&report),
                metadata_logical_change,
                commit,
            ),
            source_close.as_ref(),
        ),
        ProjectionFacts::Failed {
            report,
            error,
            source_close,
        } => {
            let outcome = match report {
                Some(report) => Err(finish_writer_error(writer, error, &report)),
                None => Err(workflow_failure(writer, error)),
            };
            with_source_close(outcome, source_close.as_ref())
        }
        ProjectionFacts::ReaderCloseFailed { report, close_error } => {
            let close = close_writer(writer).ok();
            let mut details = json!({"report": report});
            if let Some(close) = close {
                details["writer_close"] = close;
            }
            Err(reader::preserve_completed_report(close_error, details))
        }
    }
}



impl CommitDraft for PreparedHistoryProjection<'_> {
    fn set_metadata(&mut self, input: &[u8]) -> iprange_livedb::Result<bool> {
        self.set_metadata_json(input)
    }
    fn clear_metadata(&mut self) -> iprange_livedb::Result<bool> {
        self.clear_metadata_json()
    }
    fn commit(self) -> iprange_livedb::Result<CommitResult> {
        self.commit()
    }
}

// ---------------------------------------------------------------------------
// Mechanical report conversions.
// ---------------------------------------------------------------------------

fn aggregation_report(report: &MembershipAggregationReport) -> Value {
    json!({
        "scanned_range_count": report.scanned_range_count.to_string(),
        "scanned_addresses": report.scanned_addresses.to_string(),
        "feed_result_count": report.feed_result_count.to_string(),
        "pair_result_count": report.pair_result_count.to_string(),
    })
}

fn direct_join_report(report: &DirectJoinReport) -> Value {
    json!({
        "membership_range_count": report.membership_range_count.to_string(),
        "direct_ranges_visited": report.direct_ranges_visited.to_string(),
        "joined_segment_count": report.joined_segment_count.to_string(),
        "selected_addresses": report.selected_addresses.to_string(),
        "mapped_addresses": report.mapped_addresses.to_string(),
        "unmapped_addresses": report.unmapped_addresses.to_string(),
        "result_cell_count": report.result_cell_count.to_string(),
    })
}

fn membership_join_report(report: &MembershipJoinReport) -> Value {
    json!({
        "left_range_count": report.left_range_count.to_string(),
        "right_range_count": report.right_range_count.to_string(),
        "joined_segment_count": report.joined_segment_count.to_string(),
        "left_addresses": report.left_addresses.to_string(),
        "right_addresses": report.right_addresses.to_string(),
        "overlap_addresses": report.overlap_addresses.to_string(),
        "left_uncovered_addresses": report.left_uncovered_addresses.to_string(),
        "right_uncovered_addresses": report.right_uncovered_addresses.to_string(),
        "cross_result_count": report.cross_result_count.to_string(),
        "uncovered_result_count": report.uncovered_result_count.to_string(),
    })
}

fn count_report(report: &AlgebraCountReport) -> Value {
    json!({
        "source_count": report.source_count.to_string(),
        "source_range_count": report.source_range_count.to_string(),
        "joined_segment_count": report.joined_segment_count.to_string(),
        "addresses": report.addresses.to_string(),
    })
}

fn comparison_report(report: &AlgebraComparisonReport) -> Value {
    json!({
        "source_count": report.source_count.to_string(),
        "source_range_count": report.source_range_count.to_string(),
        "joined_segment_count": report.joined_segment_count.to_string(),
        "left_addresses": report.left_addresses.to_string(),
        "right_addresses": report.right_addresses.to_string(),
        "overlap_addresses": report.overlap_addresses.to_string(),
        "left_only_addresses": report.left_only_addresses.to_string(),
        "right_only_addresses": report.right_only_addresses.to_string(),
        "union_addresses": report.union_addresses.to_string(),
        "equal": report.equal,
    })
}

fn set_report(report: &AlgebraSetReport) -> Value {
    json!({
        "source_count": report.source_count.to_string(),
        "source_range_count": report.source_range_count.to_string(),
        "joined_segment_count": report.joined_segment_count.to_string(),
        "output_feed_count": report.output_feed_count.to_string(),
        "output_range_count": report.output_range_count.to_string(),
        "output_addresses": report.output_addresses.to_string(),
    })
}

fn history_projection_report(report: &HistoryProjectionReport) -> Value {
    json!({
        "logical_change": logical_change(report.logical_change),
        "source_range_count": report.source_range_count.to_string(),
        "source_addresses": report.source_addresses.to_string(),
        "created_feed_count": report.created_feed_count.to_string(),
        "before_interval_count": report.before_interval_count.to_string(),
        "after_interval_count": report.after_interval_count.to_string(),
        "before_addresses": report.before_addresses.to_string(),
        "after_addresses": report.after_addresses.to_string(),
        "unchanged_addresses": report.unchanged_addresses.to_string(),
        "added_addresses": report.added_addresses.to_string(),
        "removed_addresses": report.removed_addresses.to_string(),
        "windows": report
            .windows
            .iter()
            .map(history_window_report)
            .collect::<Vec<_>>(),
    })
}

/// Bound the complete response object of a mutating method before any
/// mutation runs. Every report scalar is either a fixed-width hex
/// identity, a u64 decimal, or a Cardinality129 decimal; the template
/// uses the longest encodings, the actual request-derived counts (real
/// feed names, real source counts), full-size identities, complete
/// cleanup/close shapes, and the echoed request id, so a response
/// whose real report passes this template always fits the
/// response-object ceiling. A request that cannot fit is refused with
/// `output_limit` before any writer is opened or file is published: a
/// committed workflow is never relabeled as a read-only failure by the
/// defensive post-hoc bound (iprange-jsonrpc-v1.md, response ceiling).
fn preflight_response(state: &SessionState, worst: Value) -> Result<(), HandlerError> {
    let mut envelope = json!({"jsonrpc": "2.0", "result": worst});
    if let Some(id) = &state.active_request_id {
        envelope["id"] = id.as_json();
    }
    // A conservative byte margin covers any constant the template does
    // not model (longest SDK code names, ledger counts in crash states);
    // refusing slightly early is the honest direction to err.
    const PREFLIGHT_MARGIN: usize = 2048;
    match super::super::schema::encode_response_object(&envelope) {
        Ok(text) if text.len() <= super::super::framing::RESPONSE_OBJECT_LIMIT - PREFLIGHT_MARGIN => {
            Ok(())
        }
        _ => Err(HandlerError::new(
            "output_limit",
            "not_started",
            "request refused: the complete inline result cannot fit the 65000-byte response object",
        )),
    }
}

const WIDEST_U64: &str = "18446744073709551615";
const WIDEST_129: &str = "680564733841876926926749214863536422911";
/// Longest portably representable basename: the SDK clamps basenames to
/// 512 bytes (live_writer LocalBasename), and lossy UTF-8 conversion of
/// a Windows UTF-16 basename can widen to at most twice that in
/// characters; 1024 characters bounds every platform.
const WIDEST_BASENAME: usize = 1024;
/// Longest SDK error-code name used on the wire (e.g.
/// insufficient_resource_budget); 32 characters bounds every code.
const WIDEST_CODE: usize = 32;

fn widest_identity() -> Value {
    json!({"volume": WIDEST_U64, "file": WIDEST_U64})
}

fn widest_close_fact() -> Value {
    json!({
        "outcome": "close_incomplete",
        "cleanup": {},
        "coordination_cleanup": {"kind": "retained_writer_close_required"},
    })
}

/// Largest observable artifact: full-size identities, the platform-max
/// basename, and worst-case housekeeping states; a maximal ledger of
/// four artifacts covers crash-state observations.
fn widest_housekeeping_artifact() -> Value {
    json!({
        "state": "move_ambiguous",
        "directory_role": "scratch_directory",
        "directory_identity": widest_identity(),
        "basename_encoding": 65535,
        "attempt_id": convert::hex_id(&[0xff; 16]),
        "ordinal": u32::MAX,
        "envelope_basename": "e".repeat(WIDEST_BASENAME),
        "envelope_identity": widest_identity(),
        "source_basename": "s".repeat(WIDEST_BASENAME),
        "inert_basename": "i".repeat(WIDEST_BASENAME),
        "source_presence": "unclassified",
        "source_identity": widest_identity(),
        "inert_presence": "unclassified",
        "inert_identity": widest_identity(),
        "kind": "unpublished_main_tail",
        "creation_security": {"kind": 65535, "commitment": "c".repeat(64)},
        "selected_envelope_sequence": WIDEST_U64,
    })
}

fn widest_cleanup_artifact() -> Value {
    json!({
        "kind": "unpublished_main_tail",
        "directory_role": "main_file",
        "directory_identity": widest_identity(),
        "basename_encoding": 65535,
        "basename": "b".repeat(WIDEST_BASENAME),
        "identity": widest_identity(),
        "error": {"code": "io", "detail": "d".repeat(64)},
        "creation_security": {"kind": 65535, "commitment": "c".repeat(64)},
        "unpublished_tail": {
            "expected_database_id": convert::hex_id(&[0xff; 16]),
            "committed_target_transaction_id": WIDEST_U64,
            "committed_target_nonce": convert::hex_id(&[0xff; 16]),
            "committed_target_length": WIDEST_U64,
            "observed_tail_end_exclusive": WIDEST_U64,
        },
    })
}

fn widest_commit_cleanup_artifact() -> Value {
    json!({
        "directory_identity": widest_identity(),
        "main_basename": "m".repeat(WIDEST_BASENAME),
        "main_identity": widest_identity(),
        "expected_database_id": convert::hex_id(&[0xff; 16]),
        "target_transaction_id": WIDEST_U64,
        "target_commit_nonce": convert::hex_id(&[0xff; 16]),
        "committed_target_length": WIDEST_U64,
        "observed_tail_end_exclusive": WIDEST_U64,
        "cleanup_error": "i".repeat(WIDEST_CODE),
    })
}

fn preflight_history_result(state: &SessionState, windows: &[HistoryWindow]) -> Result<(), HandlerError> {
    let widest_windows = windows
        .iter()
        .map(|window| {
            json!({
                "feed_name": window.feed_name.as_str(),
                "cutoff": u32::MAX,
                "created": true,
                "before_interval_count": WIDEST_U64,
                "after_interval_count": WIDEST_U64,
                "before_addresses": WIDEST_129,
                "after_addresses": WIDEST_129,
                "unchanged_addresses": WIDEST_129,
                "added_addresses": WIDEST_129,
                "removed_addresses": WIDEST_129,
            })
        })
        .collect::<Vec<_>>();
    let identity = widest_identity();
    let artifact = widest_commit_cleanup_artifact();
    let worst = json!({
        "method": "iprange.v1.history.project",
        "metadata_logical_change": "unchanged",
        "writer_close": {
            "outcome": "close_incomplete",
            "cleanup": {"artifacts": [artifact]},
            "coordination_cleanup": {"kind": "retained_writer_close_required"},
        },
        "source_closes": [widest_close_fact()],
        "commit": {
            "attempted_database_id": convert::hex_id(&[0xff; 16]),
            "directory_identity": identity,
            "main_identity": identity,
            "attempted_transaction_id": WIDEST_U64,
            "attempted_commit_nonce": convert::hex_id(&[0xff; 16]),
            "durability": "committed",
            "cleanup": {"artifacts": [artifact]},
            "coordination_cleanup": {"kind": "retained_reader_close_required"},
        },
        "report": {
            "logical_change": "changed",
            "source_range_count": WIDEST_U64,
            "source_addresses": WIDEST_129,
            "created_feed_count": WIDEST_U64,
            "before_interval_count": WIDEST_U64,
            "after_interval_count": WIDEST_U64,
            "before_addresses": WIDEST_129,
            "after_addresses": WIDEST_129,
            "unchanged_addresses": WIDEST_129,
            "added_addresses": WIDEST_129,
            "removed_addresses": WIDEST_129,
            "windows": widest_windows,
        },
    });
    preflight_response(state, worst)
}

/// `algebra.publish` commits a destination file and then reports one
/// live-close fact per opened live source; the source count is a legal
/// request parameter, so the complete result scales with it. Refuse an
/// unrepresentable request before the destination is published.
fn preflight_algebra_publish(state: &SessionState, sources: usize) -> Result<(), HandlerError> {
    // The 512-byte LocalBasename bound base64-encodes to 684 characters.
    let widest_basename_b64 = "Z".repeat(684);
    // The SDK cleanup ledger capacity is four artifacts
    // (publication/types.rs CLEANUP_CAPACITY). Housekeeping observations
    // fire once per retired authority (main_file.rs retire_steps ->
    // gc::retire_observed -> one observer call); two maximal artifacts
    // over-model every real ledger.
    let worst = json!({
        "method": "iprange.v1.algebra.publish",
        "report": {
            "source_count": WIDEST_U64,
            "source_range_count": WIDEST_U64,
            "joined_segment_count": WIDEST_U64,
            "output_feed_count": WIDEST_U64,
            "output_range_count": WIDEST_U64,
            "output_addresses": WIDEST_129,
        },
        "publication": {
            "attempt": {
                "database_id": convert::hex_id(&[0xff; 16]),
                "transaction_id": WIDEST_U64,
                "commit_nonce": convert::hex_id(&[0xff; 16]),
                "publication_attempt_id": convert::hex_id(&[0xff; 16]),
                "directory_identity": widest_identity(),
                "destination_basename_encoding": 65535,
                "destination_basename": widest_basename_b64,
                "output_identity": widest_identity(),
                "output_byte_length": WIDEST_U64,
                "output_sha512": "f".repeat(128),
                "publication_policy": "fail_if_exists",
                "previous_destination": {
                    "identity": widest_identity(),
                    "byte_length": WIDEST_U64,
                    "sha512": "e".repeat(128),
                },
                "reservation_identity": widest_identity(),
                "creation_security": {"kind": 65535, "commitment": "d".repeat(64)},
            },
            "main_namespace_may_have_been_attempted": true,
            "publication": "outcome_unknown",
            "destination_content": "unclassified",
            "later_canonical": "ready_live_sidecar",
            "main_access_policy": "changed_or_unproven",
            "coordination_access_policy": "unclassified",
            "cleanup": {"artifacts": (0..4).map(|_| widest_cleanup_artifact()).collect::<Vec<_>>()},
            "coordination_cleanup": {"kind": "retained_reader_close_required"},
            "housekeeping": {
                "state": "visible",
                "artifacts": (0..2).map(|_| widest_housekeeping_artifact()).collect::<Vec<_>>(),
            },
            "visible_housekeeping": (0..2).map(|_| widest_housekeeping_artifact()).collect::<Vec<_>>(),
        },
        "source_closes": vec![widest_close_fact(); sources],
    });
    preflight_response(state, worst)
}

fn history_window_report(report: &HistoryWindowReport) -> Value {
    json!({
        "feed_name": report.feed_name.as_str(),
        "cutoff": report.cutoff,
        "created": report.created,
        "before_interval_count": report.before_interval_count.to_string(),
        "after_interval_count": report.after_interval_count.to_string(),
        "before_addresses": report.before_addresses.to_string(),
        "after_addresses": report.after_addresses.to_string(),
        "unchanged_addresses": report.unchanged_addresses.to_string(),
        "added_addresses": report.added_addresses.to_string(),
        "removed_addresses": report.removed_addresses.to_string(),
    })
}


fn output_facts(facts: &ExportFacts) -> Value {
    json!({
        "path": facts.path,
        "sha256": facts.sha256,
        "bytes": facts.bytes.to_string(),
        "rows": facts.rows.to_string(),
    })
}

fn cardinality_u128(value: iprange_livedb::Cardinality129) -> u128 {
    if value.bit128() == 1 {
        u128::MAX
    } else {
        (u128::from(value.hi()) << 64) | u128::from(value.lo())
    }
}

// ---------------------------------------------------------------------------
// Sinks (bounded stream consumers; the SDK stops on the captured failure).
// ---------------------------------------------------------------------------

fn fail(slot: &mut Option<HandlerError>, error: HandlerError) -> iprange_livedb::Result<()> {
    if slot.is_none() {
        *slot = Some(error);
    }
    Err(iprange_livedb::Error::StoppedBySink)
}

struct CardinalitySink<'a> {
    writer: &'a mut ExportWriter,
    jsonl: bool,
    slot: &'a mut Option<HandlerError>,
    line: String,
}

impl MembershipAggregateSink for CardinalitySink<'_> {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> iprange_livedb::Result<()> {
        self.line.clear();
        for cell in batch {
            if self.jsonl {
                self.line.push_str("{\"feed\":");
                push_json_string(&mut self.line, cell.feed.as_str());
                self.line.push_str(",\"addresses\":\"");
                let _ = write!(self.line, "{}\"}}", cell.addresses);
            } else {
                write_csv_field(&mut self.line, cell.feed.as_str());
                self.line.push(',');
                let _ = write!(self.line, "{}", cell.addresses);
            }
            if let Err(error) = self
                .writer
                .write_line(&self.line, cardinality_u128(cell.addresses))
            {
                return fail(self.slot, error);
            }
            self.line.clear();
        }
        Ok(())
    }
    fn feed_overlaps(&mut self, _batch: &[FeedOverlap]) -> iprange_livedb::Result<()> {
        Ok(())
    }
}

struct OverlapSink<'a> {
    writer: &'a mut ExportWriter,
    jsonl: bool,
    slot: &'a mut Option<HandlerError>,
    line: String,
}

impl MembershipAggregateSink for OverlapSink<'_> {
    fn feed_cardinalities(&mut self, _batch: &[FeedCardinality]) -> iprange_livedb::Result<()> {
        Ok(())
    }
    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> iprange_livedb::Result<()> {
        self.line.clear();
        for cell in batch {
            if self.jsonl {
                self.line.push_str("{\"left\":");
                push_json_string(&mut self.line, cell.left.as_str());
                self.line.push_str(",\"right\":");
                push_json_string(&mut self.line, cell.right.as_str());
                self.line.push_str(",\"addresses\":\"");
                let _ = write!(self.line, "{}\"}}", cell.addresses);
            } else {
                write_csv_field(&mut self.line, cell.left.as_str());
                self.line.push(',');
                write_csv_field(&mut self.line, cell.right.as_str());
                self.line.push(',');
                let _ = write!(self.line, "{}", cell.addresses);
            }
            if let Err(error) = self
                .writer
                .write_line(&self.line, cardinality_u128(cell.addresses))
            {
                return fail(self.slot, error);
            }
            self.line.clear();
        }
        Ok(())
    }
}

struct DirectJoinSinkImpl<'a> {
    writer: &'a mut ExportWriter,
    jsonl: bool,
    slot: &'a mut Option<HandlerError>,
    line: String,
}

impl DirectJoinSink for DirectJoinSinkImpl<'_> {
    fn direct_join_cells(&mut self, batch: &[DirectJoinCell]) -> iprange_livedb::Result<()> {
        self.line.clear();
        for cell in batch {
            if self.jsonl {
                self.line.push_str("{\"feed\":");
                push_json_string(&mut self.line, cell.feed.as_str());
                self.line.push_str(",\"direct_value\":");
                match cell.direct_value {
                    Some(value) => {
                        let _ = write!(self.line, "{value}");
                    }
                    None => self.line.push_str("null"),
                }
                self.line.push_str(",\"addresses\":\"");
                let _ = write!(self.line, "{}\"}}", cell.addresses);
            } else {
                // CSV has no null vocabulary; the semantic null of an
                // uncovered cell serializes as the literal "null".
                write_csv_field(&mut self.line, cell.feed.as_str());
                self.line.push(',');
                match cell.direct_value {
                    Some(value) => {
                        let _ = write!(self.line, "{value}");
                    }
                    None => self.line.push_str("null"),
                }
                self.line.push(',');
                let _ = write!(self.line, "{}", cell.addresses);
            }
            if let Err(error) = self
                .writer
                .write_line(&self.line, cardinality_u128(cell.addresses))
            {
                return fail(self.slot, error);
            }
            self.line.clear();
        }
        Ok(())
    }
}

struct MembershipJoinSinkImpl<'a> {
    writer: &'a mut ExportWriter,
    jsonl: bool,
    slot: &'a mut Option<HandlerError>,
    line: String,
}

impl MembershipJoinSink for MembershipJoinSinkImpl<'_> {
    fn membership_cross_cells(&mut self, batch: &[MembershipCrossCell]) -> iprange_livedb::Result<()> {
        self.line.clear();
        for cell in batch {
            if self.jsonl {
                self.line.push_str("{\"kind\":\"cross\",\"left\":");
                push_json_string(&mut self.line, cell.left.as_str());
                self.line.push_str(",\"right\":");
                push_json_string(&mut self.line, cell.right.as_str());
                self.line.push_str(",\"addresses\":\"");
                let _ = write!(self.line, "{}\"}}", cell.addresses);
            } else {
                let _ = write!(self.line, "cross,");
                write_csv_field(&mut self.line, cell.left.as_str());
                self.line.push_str(",,,");
                write_csv_field(&mut self.line, cell.right.as_str());
                self.line.push(',');
                let _ = write!(self.line, "{}", cell.addresses);
            }
            if let Err(error) = self
                .writer
                .write_line(&self.line, cardinality_u128(cell.addresses))
            {
                return fail(self.slot, error);
            }
            self.line.clear();
        }
        Ok(())
    }
    fn uncovered_feeds(&mut self, batch: &[UncoveredFeed]) -> iprange_livedb::Result<()> {
        self.line.clear();
        for cell in batch {
            let side = match cell.side {
                UncoveredSide::Left => "left",
                UncoveredSide::Right => "right",
            };
            if self.jsonl {
                self.line.push_str("{\"kind\":\"uncovered\",\"side\":\"");
                self.line.push_str(side);
                self.line.push_str("\",\"feed\":");
                push_json_string(&mut self.line, cell.feed.as_str());
                self.line.push_str(",\"addresses\":\"");
                let _ = write!(self.line, "{}\"}}", cell.addresses);
            } else {
                let _ = write!(self.line, "uncovered,,,{side},");
                write_csv_field(&mut self.line, cell.feed.as_str());
                self.line.push(',');
                let _ = write!(self.line, "{}", cell.addresses);
            }
            if let Err(error) = self
                .writer
                .write_line(&self.line, cardinality_u128(cell.addresses))
            {
                return fail(self.slot, error);
            }
            self.line.clear();
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Decoding helpers (wire shapes were strictly validated).
// ---------------------------------------------------------------------------

enum Selection {
    All,
    Named(Vec<FeedName>),
}

fn decode_selection(value: &Value) -> Result<Selection, HandlerError> {
    let object = value.as_object().expect("validator checked selection");
    match object["mode"].as_str() {
        Some("all") => Ok(Selection::All),
        _ => {
            let feeds = object["feeds"].as_array().expect("validator checked feeds");
            let mut names = Vec::with_capacity(feeds.len());
            for feed in feeds {
                names.push(
                    FeedName::new(feed.as_str().expect("validator checked feed names"))
                        .map_err(|_| HandlerError::invalid_params("selection feed is invalid"))?,
                );
            }
            Ok(Selection::Named(names))
        }
    }
}

fn feed_selection(selection: &Selection) -> FeedSelection<'_> {
    match selection {
        Selection::All => FeedSelection::All,
        Selection::Named(names) => FeedSelection::Named(names),
    }
}

enum OverlapMode {
    AllPairs,
    Target(FeedName),
    SelectedPairs(Vec<FeedPair>),
}

fn decode_overlap_mode(value: &Value) -> Result<OverlapMode, HandlerError> {
    let object = value.as_object().expect("validator checked mode");
    match object["kind"].as_str() {
        Some("all_pairs") => Ok(OverlapMode::AllPairs),
        Some("target") => {
            let feed = FeedName::new(
                object["target_feed"]
                    .as_str()
                    .expect("validator checked target_feed"),
            )
            .map_err(|_| HandlerError::invalid_params("target_feed is invalid"))?;
            Ok(OverlapMode::Target(feed))
        }
        Some("selected_pairs") => {
            let pairs = object["pairs"].as_array().expect("validator checked pairs");
            let mut decoded = Vec::with_capacity(pairs.len());
            for pair in pairs {
                let pair_object = pair.as_object().expect("validator checked pair");
                let left = FeedName::new(
                    pair_object["left"]
                        .as_str()
                        .expect("validator checked pair left"),
                )
                .map_err(|_| HandlerError::invalid_params("pair left is invalid"))?;
                let right = FeedName::new(
                    pair_object["right"]
                        .as_str()
                        .expect("validator checked pair right"),
                )
                .map_err(|_| HandlerError::invalid_params("pair right is invalid"))?;
                decoded.push(FeedPair { left, right });
            }
            Ok(OverlapMode::SelectedPairs(decoded))
        }
        _ => Err(HandlerError::invalid_params("mode.kind is invalid")),
    }
}

enum Operation {
    Union(Selection),
    Intersection(Selection),
    Exclusion {
        included: Selection,
        excluded: Selection,
    },
}

fn decode_operation(value: &Value) -> Result<Operation, HandlerError> {
    let object = value.as_object().expect("validator checked operation");
    match object["kind"].as_str() {
        Some("union") => Ok(Operation::Union(decode_selection(&object["selection"])?)),
        Some("intersection") => Ok(Operation::Intersection(decode_selection(
            &object["selection"],
        )?)),
        Some("exclusion") => Ok(Operation::Exclusion {
            included: decode_selection(&object["included"])?,
            excluded: decode_selection(&object["excluded"])?,
        }),
        _ => Err(HandlerError::invalid_params("operation.kind is invalid")),
    }
}

fn algebra_operation(operation: &Operation) -> AlgebraSetOperation<'_> {
    match operation {
        Operation::Union(selection) => AlgebraSetOperation::Union(feed_selection(selection)),
        Operation::Intersection(selection) => {
            AlgebraSetOperation::Intersection(feed_selection(selection))
        }
        Operation::Exclusion { included, excluded } => AlgebraSetOperation::Exclusion {
            included: feed_selection(included),
            excluded: feed_selection(excluded),
        },
    }
}

enum OutputMode {
    PreserveFeeds,
    Flat(FeedName),
}

fn decode_output_mode(value: &Value) -> Result<OutputMode, HandlerError> {
    let object = value.as_object().expect("validator checked output_mode");
    match object["kind"].as_str() {
        Some("preserve_feeds") => Ok(OutputMode::PreserveFeeds),
        Some("flat") => {
            let feed = FeedName::new(
                object["feed"]
                    .as_str()
                    .expect("validator checked output_mode feed"),
            )
            .map_err(|_| HandlerError::invalid_params("output_mode feed is invalid"))?;
            Ok(OutputMode::Flat(feed))
        }
        _ => Err(HandlerError::invalid_params("output_mode.kind is invalid")),
    }
}

fn algebra_output_mode(mode: OutputMode) -> AlgebraOutputMode {
    match mode {
        OutputMode::PreserveFeeds => AlgebraOutputMode::PreserveFeeds,
        OutputMode::Flat(feed) => AlgebraOutputMode::Flat(feed),
    }
}

fn decode_membership_budget(value: &Value) -> Result<MembershipQueryBudget, HandlerError> {
    let object = value.as_object().expect("validator checked budget");
    Ok(MembershipQueryBudget {
        max_heap_bytes: reader::u64_string(object["max_heap_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
    })
}

fn decode_algebra_budget(value: &Value) -> Result<MembershipAlgebraBudget, HandlerError> {
    let object = value.as_object().expect("validator checked budget");
    Ok(MembershipAlgebraBudget {
        max_heap_bytes: reader::u64_string(object["max_heap_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_sources: reader::positive_u32(&object["max_sources"])
            .map_err(HandlerError::invalid_params)?,
    })
}

fn decode_algebra_output_budget(value: &Value) -> Result<AlgebraOutputBudget, HandlerError> {
    let object = value.as_object().expect("validator checked budget");
    Ok(AlgebraOutputBudget {
        max_output_pages: reader::u64_string(object["max_output_pages"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: reader::positive_u32(&object["max_open_files"])
            .map_err(HandlerError::invalid_params)?,
    })
}

struct OutputSpec {
    path: std::path::PathBuf,
    jsonl: bool,
    policy: iprange_livedb::PublicationPolicy,
    budget: ExportBudget,
}

fn decode_output(value: &Value) -> Result<OutputSpec, HandlerError> {
    let object = value.as_object().expect("validator checked output");
    let path = std::path::PathBuf::from(
        object["path"]
            .as_str()
            .expect("validator checked output.path"),
    );
    let jsonl = object["format"].as_str() == Some("jsonl");
    let policy = reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("output.publication_policy is invalid"))?;
    let budget_object = reader::exact_object(
        &object["result_budget"],
        &["max_rows", "max_output_bytes", "max_open_files"],
    )
    .map_err(HandlerError::invalid_params)?;
    let budget = ExportBudget {
        max_rows: reader::u64_string(budget_object["max_rows"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_output_bytes: reader::u64_string(budget_object["max_output_bytes"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_open_files: reader::positive_u32(&budget_object["max_open_files"])
            .map_err(HandlerError::invalid_params)?,
    };
    Ok(OutputSpec {
        path,
        jsonl,
        policy,
        budget,
    })
}

fn decode_windows(value: &Value) -> Result<Vec<HistoryWindow>, HandlerError> {
    let windows = value.as_array().expect("validator checked windows");
    let mut decoded = Vec::with_capacity(windows.len());
    for window in windows {
        let object = window.as_object().expect("validator checked window");
        let feed = FeedName::new(object["feed"].as_str().expect("validator checked window feed"))
            .map_err(|_| HandlerError::invalid_params("window feed is invalid"))?;
        let cutoff = object["cutoff"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .ok_or_else(|| HandlerError::invalid_params("window cutoff must be u32"))?;
        decoded.push(HistoryWindow {
            feed_name: feed,
            cutoff,
        });
    }
    Ok(decoded)
}

// ---------------------------------------------------------------------------
// Readers and sources.
// ---------------------------------------------------------------------------

fn resolve_scope<'a>(
    reader: &'a ReaderValue,
    selection: &Selection,
    budget: MembershipQueryBudget,
    state: &SessionState,
) -> Result<MembershipScope<'a>, HandlerError> {
    let query = sdk(reader.membership_query())?;
    match selection {
        Selection::All => sdk(query.all_feeds(budget, &state.token)),
        Selection::Named(names) => sdk(query.named_feeds(names, budget, &state.token)),
    }
}

fn resolve_algebra_scopes<'a>(
    readers: &'a [ReaderValue],
    sources: &Value,
    state: &SessionState,
) -> Result<Vec<MembershipScope<'a>>, HandlerError> {
    let sources = sources.as_array().expect("validator checked sources");
    let mut scopes = Vec::with_capacity(sources.len());
    for (index, source) in sources.iter().enumerate() {
        let object = source.as_object().expect("validator checked algebra source");
        let budget = decode_membership_budget(&object["membership_query_budget"])?;
        let query = sdk(readers[index].membership_query())?;
        scopes.push(sdk(query.all_feeds(budget, &state.token))?);
    }
    Ok(scopes)
}

fn open_sources(sources: &Value, state: &SessionState) -> Result<Vec<ReaderValue>, HandlerError> {
    let sources = sources.as_array().expect("validator checked sources");
    let mut readers = Vec::with_capacity(sources.len());
    for source in sources {
        let opened = (|| -> Result<ReaderValue, HandlerError> {
            let object = source.as_object().expect("validator checked algebra source");
            let source_member = reader::member_object(object, "source")
                .map_err(HandlerError::invalid_params)?;
            let (path, mode) = source_parts_object(source_member, "source")?;
            open_temporary(&path, &mode, state)
        })();
        match opened {
            Ok(reader) => readers.push(reader),
            Err(error) => {
                return Err(reader::close_on_error(&mut readers, error));
            }
        }
    }
    Ok(readers)
}

fn source_parts(
    object: &serde_json::Map<String, Value>,
    member: &str,
) -> Result<(String, String), HandlerError> {
    let source = reader::member_object(object, member)
        .map_err(HandlerError::invalid_params)?;
    source_parts_object(source, member)
}

fn source_parts_object(
    source: &serde_json::Map<String, Value>,
    member: &str,
) -> Result<(String, String), HandlerError> {
    let path = source["path"]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params(format!("{member}.path must be a string")))?
        .to_owned();
    let mode = source["mode"]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params(format!("{member}.mode must be a string")))?
        .to_owned();
    Ok((path, mode))
}

fn open_temporary(path: &str, mode: &str, state: &SessionState) -> Result<ReaderValue, HandlerError> {
    match Path::new(path).try_exists() {
        Ok(true) => {}
        Ok(false) => {
            return Err(HandlerError::new(
                "invalid_path",
                "not_started",
                format!("database source does not exist: {path}"),
            ));
        }
        Err(error) => {
            return Err(HandlerError::new(
                "io",
                "not_started",
                format!("cannot inspect database source {path}: {error}"),
            ));
        }
    }
    match mode {
        "immutable" => ImmutableReader::open(path)
            .map(ReaderValue::Immutable)
            .map_err(reader::read_error),
        _ => LiveReader::open(path, &state.token)
            .map(ReaderValue::Live)
            .map_err(reader::read_error),
    }
}

/// Close every ephemeral reader; success carries every factual live
/// close result as `source_closes` in reader order (absent for
/// immutable readers), while a close failure preserves the completed
/// report in the error details.
fn close_readers(readers: Vec<ReaderValue>, report: Value) -> Result<Value, HandlerError> {
    let mut closes = Vec::new();
    let mut first_error: Option<HandlerError> = None;
    for mut reader in readers {
        match reader::close_ephemeral_reader(&mut reader) {
            Ok(Some(close)) => closes.push(close),
            Ok(None) => {}
            Err(error) => {
                if first_error.is_none() {
                    first_error = Some(error);
                }
            }
        }
    }
    match first_error {
        Some(error) => Err(reader::preserve_completed_report(error, report)),
        None => {
            let mut result = report;
            if !closes.is_empty() {
                result
                    .as_object_mut()
                    .expect("method results are objects")
                    .insert("source_closes".into(), Value::Array(closes));
            }
            reader::bounded_result(result)
        }
    }
}

/// Close one ephemeral reader; success carries the factual live close
/// result as `source_close` (absent for immutable readers).
fn close_reader(reader: &mut ReaderValue, report: Value) -> Result<Value, HandlerError> {
    match reader::close_ephemeral_reader(reader) {
        Ok(Some(source_close)) => {
            let mut result = report;
            result
                .as_object_mut()
                .expect("method results are objects")
                .insert("source_close".into(), source_close);
            reader::bounded_result(result)
        }
        Ok(None) => reader::bounded_result(report),
        Err(error) => Err(reader::preserve_completed_report(error, report)),
    }
}

fn require_existing_database(path: &Path) -> Result<(), HandlerError> {
    match path.metadata() {
        Ok(value) if value.is_file() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("live database is not a regular file: {}", path.display()),
        )),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("live database does not exist: {}", path.display()),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect live database {}: {error}", path.display()),
        )),
    }
}

fn require_publication_parent(path: &Path) -> Result<(), HandlerError> {
    if path.file_name().is_none() {
        return Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!(
                "publication destination has no file name: {}",
                path.display()
            ),
        ));
    }
    let parent = path
        .parent()
        .filter(|value| !value.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    match parent.metadata() {
        Ok(value) if value.is_dir() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!(
                "publication parent is not a directory: {}",
                parent.display()
            ),
        )),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("publication parent does not exist: {}", parent.display()),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect publication parent {}: {error}", parent.display()),
        )),
    }
}

fn algebra_preparation_error(failure: &AlgebraPreparationFailure) -> HandlerError {
    HandlerError {
        code: publication_code(failure.cause.code),
        outcome: "not_started",
        message: format!("algebra preparation failed: {}", failure.cause.detail),
        details: Some(json!({
            "cleanup_state": cleanup_state(failure.cleanup_state()),
            "cleanup": cleanup_artifacts(&failure.cleanup),
            "coordination_cleanup": lifecycle::coordination_cleanup(failure.coordination_cleanup),
            "housekeeping": lifecycle::housekeeping(failure.housekeeping, &failure.visible_housekeeping),
            "visible_housekeeping": failure
                .visible_housekeeping
                .iter()
                .map(lifecycle::housekeeping_artifact)
                .collect::<Vec<_>>(),
            "output": failure.output.as_ref().map(private_output_attempt),
        })),
    }
}

fn publication_outcome(value: PublicationStatus) -> &'static str {
    match value {
        PublicationStatus::NotPublished => "not_published",
        PublicationStatus::Published => "published",
        PublicationStatus::OutcomeUnknown => "outcome_unknown",
    }
}

/// Canonical SDK error names for the publication failure classes the
/// reader mapping does not list (mirrors snapshot.rs).
fn publication_code(code: ErrorCode) -> &'static str {
    match code {
        ErrorCode::PublicationUnsupported => "publication_unsupported",
        ErrorCode::OsUnsupported => "os_unsupported",
        ErrorCode::DurabilityUnsupported => "durability_unsupported",
        ErrorCode::AccessPolicyUnsupported => "access_policy_unsupported",
        ErrorCode::SnapshotPreparationFailed => "snapshot_preparation_failed",
        ErrorCode::LiveCoordinationUnsupported => "live_coordination_unsupported",
        code => reader::sdk_code(code),
    }
}

fn cleanup_state(value: CleanupState) -> &'static str {
    match value {
        CleanupState::Clean => "clean",
        CleanupState::ResiduePossible => "residue_possible",
    }
}

fn cleanup_artifacts(value: &CleanupArtifacts) -> Value {
    if value.is_empty() {
        json!({})
    } else {
        json!({"artifacts": value.iter().map(lifecycle::cleanup_artifact).collect::<Vec<_>>()})
    }
}

fn private_output_attempt(value: &PrivateOutputAttempt) -> Value {
    json!({
        "publication_attempt_id": convert::hex_id(&value.publication_attempt_id),
        "directory_identity": lifecycle::file_identity(&value.directory_identity)
            .unwrap_or_else(|error| json!({"error": error.message})),
        "basename_encoding": value.basename_encoding,
        "basename": lifecycle::basename(&value.basename),
        "identity": value.identity.as_ref().map(|identity| {
            lifecycle::file_identity(identity)
                .unwrap_or_else(|error| json!({"error": error.message}))
        }),
        "creation_security": lifecycle::creation_security(&value.creation_security),
    })
}

fn sdk<T>(result: iprange_livedb::Result<T>) -> Result<T, HandlerError> {
    result.map_err(reader::read_error)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn window(feed: &str, cutoff: u32) -> HistoryWindow {
        HistoryWindow {
            feed_name: FeedName::new(feed).expect("valid test feed"),
            cutoff,
        }
    }

    fn session() -> SessionState {
        SessionState::default()
    }

    #[test]
    fn history_preflight_accepts_small_reports() {
        let windows = vec![window("alpha", 7), window("beta", 9)];
        assert!(preflight_history_result(&mut session(), &windows).is_ok());
    }

    #[test]
    fn history_preflight_refuses_unrepresentable_reports_before_mutation() {
        // Worst-case window reports are far larger than the real ones;
        // a request whose complete report cannot fit the response
        // object ceiling must be refused before any writer is opened.
        let windows = (0..2000)
            .map(|index| window(&format!("f{index:04}"), 1))
            .collect::<Vec<_>>();
        match preflight_history_result(&mut session(), &windows) {
            Err(error) => {
                assert_eq!(error.code, "output_limit");
                assert_eq!(error.outcome, "not_started");
            }
            Ok(()) => panic!("oversized history report must be refused pre-mutation"),
        }
    }

    #[test]
    fn algebra_publish_preflight_scales_with_source_count() {
        assert!(preflight_algebra_publish(&session(), 1).is_ok());
        match preflight_algebra_publish(&session(), 100_000) {
            Err(error) => {
                assert_eq!(error.code, "output_limit");
                assert_eq!(error.outcome, "not_started");
            }
            Ok(()) => panic!("oversized publish result must be refused pre-mutation"),
        }
    }
}
