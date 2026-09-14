//! Live lifecycle, resolution-attempt, and live-mutation handlers.
//!
//! Owns one spec family from iprange-jsonrpc-v1.md: `database.*` live
//! lifecycle and resolution methods, `commit.resolve`, `direct.replace`,
//! and the two retention refreshes. Mutations open one clean writer,
//! run one public high-level workflow, stage the requested metadata in
//! that draft, commit when changed, and return the complete workflow,
//! commit, and close facts. Input failure aborts the draft and never
//! fabricates a commit.
//!
//! The retention refreshes additionally open one ephemeral reader over
//! the caller-supplied current coverage source and stream one named
//! membership feed into the refresh draft; the reader is closed before
//! the draft finishes. `first_seen.refresh` writes an exact removal
//! log to a same-directory private file and publishes it only after the
//! commit is factually known to have committed (iprange-jsonrpc-v1.md).

use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, BufWriter, Write};
use std::fmt::Write as _;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::path::{Path, PathBuf};

use iprange_livedb::error::Error;
use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::{
    initialize_live, reset_live_coordination, resolve_commit, resolve_create_live,
    resolve_interrupted_live_transition, resolve_live_transition, AddressFamily,
    CancellationToken, CommitResolution, CommitResolutionMode, CommitResolutionResult,
    CommitDurability, CommitResult, DirectRange, FeedName, FeedRangeSourceV4, FeedRangeSourceV6,
    FinishedWorkflow, FirstSeenRemoval, FirstSeenRemovalSink, ImmutableReader, Ipv4Key, Ipv6Key,
    LiveCoordinationLocation, LiveReader, LiveResidueKind, LiveResidueResult, LiveResidueStatus,
    LiveResetPolicy, LiveTransitionOperation, LiveTransitionResolutionMode, LiveTransitionResult,
    LiveTransitionStatus, LiveWriter, LocalFileRelation, RangeSource,
};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::ReaderValue;
use super::convert;
use super::lifecycle;
use super::lifecycle::durability_outcome;
use super::workflow::workflow_report;
use super::lifecycle_live;
use super::reader;

const CSV_BATCH_CAPACITY: usize = 256;

/// Owned publisher-mutation facts after the borrowed workflow has been
/// consumed. `FinishedWorkflow` borrows the writer and its changed variant
/// has drop glue, so the workflow is consumed entirely (metadata staging,
/// commit) before the writer is used again; every value stored here is
/// owned so the caller can close the writer afterwards.
struct PublisherFacts {
    report: Value,
    metadata_changed: bool,
    no_change: bool,
    staging_error: Option<Error>,
    commit: Option<Result<CommitResult, Error>>,
    /// Factual live close of the ephemeral current-coverage reader
    /// (absent for immutable sources). Filled by the retention runs
    /// whose workflow closes the reader mid-flow; direct replacements
    /// never open a source reader and leave it `None`.
    source_close: Option<Value>,
}

/// Stage the requested metadata in a changed draft, commit it, and collect
/// the owned facts. The workflow is consumed exactly once and no writer is
/// touched inside, which keeps its writer borrow linear: the caller can use
/// the writer again only after this returns its owned facts.
fn consume_finished(
    finished: FinishedWorkflow<'_>,
    metadata: &lifecycle::MetadataValue,
) -> PublisherFacts {
    let report = workflow_report(finished.report());
    let mut metadata_changed = false;
    let mut no_change = false;
    let mut staging_error: Option<Error> = None;
    let mut commit: Option<Result<CommitResult, Error>> = None;
    match finished {
        FinishedWorkflow::Changed(mut prepared) => {
            let staged = match metadata {
                lifecycle::MetadataValue::Keep => Ok(false),
                lifecycle::MetadataValue::Replace(bytes) => {
                    prepared.set_metadata_json(bytes).map(|_| true)
                }
                lifecycle::MetadataValue::Clear => prepared.clear_metadata_json(),
            };
            match staged {
                Ok(changed) => {
                    metadata_changed = changed;
                    commit = Some(prepared.commit());
                }
                Err(error) => {
                    drop(prepared);
                    staging_error = Some(error);
                }
            }
        }
        FinishedWorkflow::NoChange(_) => {
            no_change = true;
        }
    }
    PublisherFacts {
        report,
        metadata_changed,
        no_change,
        staging_error,
        commit,
        source_close: None,
    }
}

/// Publish the owned facts: metadata-only mutations on a no-change draft
/// use one fresh direct transaction, then the writer is closed and the
/// complete workflow/commit/close facts are assembled. No borrowed
/// workflow value is alive here.
fn publisher_value(
    facts: PublisherFacts,
    writer: &mut LiveWriter,
    metadata: &lifecycle::MetadataValue,
    method: &str,
    removals: Option<&RemovalCollector>,
    cancellation: &CancellationToken,
) -> Result<Value, HandlerError> {
    let report = facts.report;
    let mut metadata_changed = facts.metadata_changed;
    let no_change = facts.no_change;
    let mut commit = facts.commit;
    let staging_error = facts.staging_error;
    let source_close = facts.source_close;
    // Publish behind one closure so every error path can merge the
    // already-factual source close into `details` next to `writer_close`
    // (spec iprange-jsonrpc-v1.md factual-close rule); the success path
    // keeps the `source_close` member.
    let outcome = (|| -> Result<Value, HandlerError> {
        if let Some(error) = staging_error {
            return Err(close_writer_facts(
                writer,
                lifecycle::sdk_error(&error, "not_started"),
            ));
        }
        if no_change {
            match metadata {
                lifecycle::MetadataValue::Keep => {}
                lifecycle::MetadataValue::Replace(bytes) => {
                    // One fresh direct transaction inside an owned-outcome
                    // closure: the transaction borrow dies with the call, so
                    // the writer is free again when the outcome is handled.
                    let outcome = (|| -> Result<CommitResult, Error> {
                        let mut transaction = writer.begin_direct_transaction(cancellation)?;
                        transaction.set_metadata_json(bytes)?;
                        transaction.commit()
                    })();
                    match outcome {
                        Ok(attempt) => {
                            metadata_changed = true;
                            commit = Some(Ok(attempt));
                        }
                        Err(error) => {
                            return Err(close_writer_facts(
                                writer,
                                lifecycle::sdk_error(&error, "not_started"),
                            ))
                        }
                    }
                }
                lifecycle::MetadataValue::Clear => {
                    let outcome = (|| -> Result<Option<CommitResult>, Error> {
                        let mut transaction = writer.begin_direct_transaction(cancellation)?;
                        let cleared = transaction.clear_metadata_json()?;
                        if cleared {
                            Ok(Some(transaction.commit()?))
                        } else {
                            Ok(None)
                        }
                    })();
                    match outcome {
                        Ok(Some(attempt)) => {
                            metadata_changed = true;
                            commit = Some(Ok(attempt));
                        }
                        Ok(None) => {}
                        Err(error) => {
                            return Err(close_writer_facts(
                                writer,
                                lifecycle::sdk_error(&error, "not_started"),
                            ))
                        }
                    }
                }
            }
        }
        let close = match writer.close() {
            Ok(close) => lifecycle::close_result(&close)?,
            Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
        };
        let metadata_change = if metadata_changed { "changed" } else { "unchanged" };
        let mut details = json!({
            "report": report,
            "metadata_logical_change": metadata_change,
            "writer_close": close,
        });
        if let Some(removals) = removals {
            details["removals"] = removals.unpublished_facts();
        }
        match &commit {
            Some(Ok(attempt)) => {
                details["commit"] = lifecycle::commit_result(attempt)?;
                if attempt.durability != CommitDurability::Committed || attempt.cause.is_some() {
                    let cause = attempt.cause.as_ref();
                    let code = cause.map_or("io", |error| reader::sdk_code(error.code()));
                    let message = cause.map_or_else(
                        || "publisher commit did not complete".to_owned(),
                        ToString::to_string,
                    );
                    return Err(HandlerError {
                        code,
                        outcome: durability_outcome(attempt.durability),
                        message,
                        details: Some(details),
                    });
                }
            }
            Some(Err(error)) => {
                details["failure"] =
                    json!({"code": reader::sdk_code(error.code()), "message": error.to_string()});
                let failure = lifecycle::sdk_error(error, "not_started");
                return Err(HandlerError {
                    details: Some(details),
                    ..failure
                });
            }
            None => {}
        }
        let close_failed = matches!(close["outcome"].as_str(), Some("close_incomplete"));
        if close_failed {
            return Err(HandlerError {
                code: "io",
                outcome: if commit.is_some() { "committed" } else { "not_started" },
                message: "live writer close is incomplete".into(),
                details: Some(details),
            });
        }
        let mut value = json!({
            "method": method,
            "report": report,
            "metadata_logical_change": metadata_change,
            "writer_close": close,
        });
        if let Some(Ok(attempt)) = &commit {
            value["commit"] = lifecycle::commit_result(attempt)?;
        }
        Ok(value)
    })();
    match outcome {
        Ok(mut value) => {
            if let Some(close) = source_close {
                value["source_close"] = close;
            }
            Ok(value)
        }
        Err(error) => Err(merge_source_close(error, source_close)),
    }
}

// ---------------------------------------------------------------------------
// Param validators
// ---------------------------------------------------------------------------

pub fn validate_database_initialize_live(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "reader_capacity"])?;
    reader::validate_path(object["path"].as_str())?;
    u32_member(object, "reader_capacity")
}

pub fn validate_database_reset_live(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "reader_capacity", "policy"])?;
    reader::validate_path(object["path"].as_str())?;
    u32_member(object, "reader_capacity")?;
    match object["policy"].as_str() {
        Some("rollback_safe") | Some("discard_previous") => Ok(()),
        _ => Err("policy must be rollback_safe or discard_previous".into()),
    }
}

pub fn validate_create_resolve(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "create_result", "resolution_mode"])?;
    reader::validate_path(object["path"].as_str())?;
    if !object["create_result"].is_object() {
        return Err("create_result must be an object".into());
    }
    resolution_mode(object["resolution_mode"].as_str())
}

pub fn validate_live_transition_resolve(params: &Value) -> Result<(), String> {
    let object =
        reader::exact_object(params, &["path", "live_transition_result", "resolution_mode"])?;
    reader::validate_path(object["path"].as_str())?;
    if !object["live_transition_result"].is_object() {
        return Err("live_transition_result must be an object".into());
    }
    resolution_mode(object["resolution_mode"].as_str())
}

pub fn validate_live_residue_resolve(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "resolution_mode"])?;
    reader::validate_path(object["path"].as_str())?;
    resolution_mode(object["resolution_mode"].as_str())
}

pub fn validate_commit_resolve(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "commit_result", "mode"])?;
    reader::validate_path(object["path"].as_str())?;
    if !object["commit_result"].is_object() {
        return Err("commit_result must be an object".into());
    }
    match object["mode"].as_str() {
        Some("live") | Some("immutable") => Ok(()),
        _ => Err("mode must be live or immutable".into()),
    }
}

pub fn validate_direct_replace(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(params, &["path", "input", "metadata", "writer_budget"])?;
    reader::validate_path(object["path"].as_str())?;
    let input = reader::exact_object(&object["input"], &["path", "max_line_bytes"])?;
    reader::validate_path(input["path"].as_str())?;
    line_byte_limit(&input["max_line_bytes"])?;
    lifecycle::validate_metadata(&object["metadata"], true)?;
    lifecycle::validate_writer_budget(&object["writer_budget"])
}

pub fn validate_first_seen_refresh(params: &Value) -> Result<(), String> {
    let object = reader::exact_object_opt(
        params,
        &["path", "current", "refresh_value", "metadata", "writer_budget"],
        &["removals_output"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    validate_current_source(&object["current"])?;
    u32_member(object, "refresh_value")?;
    if let Some(output) = object.get("removals_output") {
        validate_removals_output(output)?;
    }
    lifecycle::validate_metadata(&object["metadata"], true)?;
    lifecycle::validate_writer_budget(&object["writer_budget"])
}

pub fn validate_last_seen_refresh(params: &Value) -> Result<(), String> {
    let object = reader::exact_object(
        params,
        &["path", "current", "refresh_value", "cutoff", "metadata", "writer_budget"],
    )?;
    reader::validate_path(object["path"].as_str())?;
    validate_current_source(&object["current"])?;
    u32_member(object, "refresh_value")?;
    u32_member(object, "cutoff")?;
    lifecycle::validate_metadata(&object["metadata"], true)?;
    lifecycle::validate_writer_budget(&object["writer_budget"])
}

fn line_byte_limit(value: &Value) -> Result<(), String> {
    let limit = value
        .as_u64()
        .and_then(|parsed| u32::try_from(parsed).ok())
        .ok_or("max_line_bytes must be u32")?;
    if !(1..=1_048_576).contains(&limit) {
        return Err("max_line_bytes must be 1 through 1048576".into());
    }
    Ok(())
}

fn validate_current_source(value: &Value) -> Result<(), String> {
    let current = reader::exact_object(value, &["source", "feed"])?;
    let source = reader::exact_object(&current["source"], &["path", "mode"])?;
    reader::validate_path(source["path"].as_str())?;
    match source["mode"].as_str() {
        Some("immutable") | Some("live") => {}
        _ => return Err("current.source.mode must be immutable or live".into()),
    }
    let feed = current["feed"]
        .as_str()
        .ok_or("current.feed must be a string")?;
    FeedName::new(feed).map_err(|error| format!("current.feed: {error}"))?;
    Ok(())
}

fn validate_removals_output(value: &Value) -> Result<(), String> {
    let output = reader::exact_object(value, &["path", "publication_policy", "result_budget"])?;
    reader::validate_path(output["path"].as_str())?;
    reader::publication_policy(output["publication_policy"].as_str())
        .map_err(|_| "removals_output.publication_policy is invalid".to_string())?;
    validate_result_budget(&output["result_budget"])
}

fn validate_result_budget(value: &Value) -> Result<(), String> {
    let budget = reader::exact_object(value, &["max_rows", "max_output_bytes", "max_open_files"])?;
    reader::positive_u64_string(budget["max_rows"].as_str())
        .map_err(|error| format!("result_budget.max_rows: {error}"))?;
    reader::positive_u64_string(budget["max_output_bytes"].as_str())
        .map_err(|error| format!("result_budget.max_output_bytes: {error}"))?;
    reader::positive_u32(&budget["max_open_files"])
        .map_err(|error| format!("result_budget.max_open_files: {error}"))?;
    Ok(())
}

fn resolution_mode(value: Option<&str>) -> Result<(), String> {
    match value {
        Some("complete") | Some("rollback") => Ok(()),
        _ => Err("resolution_mode must be complete or rollback".into()),
    }
}

// ---------------------------------------------------------------------------
// Live lifecycle and resolution-attempt handlers
// ---------------------------------------------------------------------------

pub fn database_initialize_live(
    state: &mut SessionState,
    params: Value,
) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let capacity = u32_value(&object["reader_capacity"]).map_err(HandlerError::invalid_params)?;
    let result = initialize_live(path, capacity, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "not_started"))?;
    bounded(completed_result(
        "iprange.v1.database.initialize_live",
        live_transition_result(&result),
    ))
}

pub fn database_reset_live(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let capacity = u32_value(&object["reader_capacity"]).map_err(HandlerError::invalid_params)?;
    let policy = match object["policy"].as_str() {
        Some("rollback_safe") => LiveResetPolicy::RollbackSafe,
        _ => LiveResetPolicy::DiscardPrevious,
    };
    let result = reset_live_coordination(path, capacity, policy, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "not_started"))?;
    bounded(completed_result(
        "iprange.v1.database.reset_live",
        live_transition_result(&result),
    ))
}

pub fn create_resolve(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let supplied = lifecycle_live::create_result_from_wire(&object["create_result"], path)
        .map_err(HandlerError::invalid_params)?;
    let mode = resolve_mode(&object["resolution_mode"])?;
    let result = resolve_create_live(path, &supplied, mode, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "outcome_unknown"))?;
    let mut value = lifecycle::create_result(&result)?;
    value["method"] = json!("iprange.v1.database.create.resolve");
    bounded(value)
}

pub fn live_transition_resolve(
    state: &mut SessionState,
    params: Value,
) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let supplied =
        lifecycle_live::live_transition_result_from_wire(&object["live_transition_result"], path)
            .map_err(HandlerError::invalid_params)?;
    let mode = resolve_mode(&object["resolution_mode"])?;
    let result = resolve_live_transition(path, &supplied, mode, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "outcome_unknown"))?;
    bounded(completed_result(
        "iprange.v1.database.live_transition.resolve",
        live_transition_result(&result),
    ))
}

pub fn live_residue_resolve(
    state: &mut SessionState,
    params: Value,
) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    let mode = resolve_mode(&object["resolution_mode"])?;
    // The residue resolver may recover a transition whose main is already
    // gone, so no existence pre-check applies here.
    let result = resolve_interrupted_live_transition(path, mode, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "outcome_unknown"))?;
    bounded(completed_result(
        "iprange.v1.database.live_residue.resolve",
        live_residue_result(&result),
    ))
}

pub fn commit_resolve(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let supplied = lifecycle_live::commit_result_from_wire(&object["commit_result"])
        .map_err(HandlerError::invalid_params)?;
    let mode = match object["mode"].as_str() {
        Some("live") => CommitResolutionMode::Live,
        _ => CommitResolutionMode::Immutable,
    };
    let result = resolve_commit(path, &supplied, mode, &state.token())
        .map_err(|error| lifecycle::sdk_error(&error, "outcome_unknown"))?;
    bounded(completed_result(
        "iprange.v1.commit.resolve",
        commit_resolution_result(&result),
    ))
}

fn resolve_mode(value: &Value) -> Result<LiveTransitionResolutionMode, HandlerError> {
    match value.as_str() {
        Some("complete") => Ok(LiveTransitionResolutionMode::Complete),
        Some("rollback") => Ok(LiveTransitionResolutionMode::Rollback),
        _ => Err(HandlerError::invalid_params(
            "resolution_mode must be complete or rollback",
        )),
    }
}

fn completed_result(method: &str, mut result: Value) -> Value {
    result["method"] = json!(method);
    result
}

// ---------------------------------------------------------------------------
// Live mutation handlers
// ---------------------------------------------------------------------------

pub fn direct_replace(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let input = object["input"]
        .as_object()
        .ok_or_else(|| invalid("input must be an object"))?;
    let csv_path = input["path"]
        .as_str()
        .ok_or_else(|| invalid("input.path must be a string"))?;
    let max_line_bytes = u32_value(&input["max_line_bytes"])
        .map_err(HandlerError::invalid_params)? as usize;
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;

    let mut writer = match LiveWriter::open(path, budget, &state.token()) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let ipv6 = writer.address_family() == AddressFamily::Ipv6;
    let mut workflow = match writer.begin_direct_replacement(&state.token()) {
        Ok(workflow) => workflow,
        Err(error) => {
            return Err(close_writer_facts(
                &mut writer,
                lifecycle::sdk_error(&error, "not_started"),
            ))
        }
    };
    let drain_error = if ipv6 {
        let mut source =
            match DirectCsvSource::<Ipv6Key>::open(csv_path, max_line_bytes, parse_ipv6) {
                Ok(source) => source,
                Err(failure) => {
                    return Err(close_writer_facts(&mut writer, failure.into_handler_error()))
                }
            };
        match workflow.add_ranges_v6(&mut source) {
            Ok(()) => None,
            Err(error) => Some((source.take_failure(), error)),
        }
    } else {
        let mut source =
            match DirectCsvSource::<Ipv4Key>::open(csv_path, max_line_bytes, parse_ipv4) {
                Ok(source) => source,
                Err(failure) => {
                    return Err(close_writer_facts(&mut writer, failure.into_handler_error()))
                }
            };
        match workflow.add_ranges_v4(&mut source) {
            Ok(()) => None,
            Err(error) => Some((source.take_failure(), error)),
        }
    };
    if let Some((failure, error)) = drain_error {
        drop(workflow);
        let failure = match failure {
            Some(failure) => failure.into_handler_error(),
            None => lifecycle::sdk_error(&error, "not_started"),
        };
        return Err(close_writer_facts(&mut writer, failure));
    }
    let outcome = match workflow.finish_input() {
        Ok(finished) => Ok(consume_finished(finished, &metadata)),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let facts = outcome.map_err(|error| close_writer_facts(&mut writer, error))?;
    let value = publisher_value(
        facts,
        &mut writer,
        &metadata,
        "iprange.v1.direct.replace",
        None,
        &state.token(),
    )?;
    bounded(value)
}

pub fn first_seen_refresh(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let (source_path, source_mode, feed) = decode_current_source(&object["current"])?;
    let refresh_value = u32_value(&object["refresh_value"]).map_err(HandlerError::invalid_params)?;
    // Validate the removal-output settings now, but create the private
    // temporary only after every fallible pre-work (source open/info,
    // writer open) has succeeded, so no early return can leak it.
    let removals = match object.get("removals_output") {
        Some(output) => Some(removals_settings(output)?),
        None => None,
    };
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;

    let mut reader = open_source_reader(&source_path, &source_mode, &state.token())?;
    let family = match reader.info() {
        Ok(info) => info.address_family,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut reader),
                error,
            ));
        }
    };
    let mut writer = match LiveWriter::open(path, budget, &state.token()) {
        Ok(writer) => writer,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut reader),
                error,
            ))
        }
    };
    let mut collector = match removals {
        // Collector creation is fallible and happens after the writer
        // is open; a failure must still close the source reader and
        // the writer and report both factual close results with the
        // error.
        Some(settings) => match RemovalCollector::new(settings, refresh_value) {
            Ok(collector) => Some(collector),
            Err(error) => return Err(close_refresh_facts(&mut reader, &mut writer, error)),
        },
        None => None,
    };
    let outcome = match family {
        AddressFamily::Ipv4 => run_first_seen_v4(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            collector.as_mut(),
            &metadata,
            &state.token(),
        )
        .and_then(|facts| {
            publisher_value(
                facts,
                &mut writer,
                &metadata,
                "iprange.v1.retention.first_seen.refresh",
                collector.as_ref(),
                &state.token(),
            )
        }),
        AddressFamily::Ipv6 => run_first_seen_v6(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            collector.as_mut(),
            &metadata,
            &state.token(),
        )
        .and_then(|facts| {
            publisher_value(
                facts,
                &mut writer,
                &metadata,
                "iprange.v1.retention.first_seen.refresh",
                collector.as_ref(),
                &state.token(),
            )
        }),
    };
    let mut result = match outcome {
        Ok(result) => result,
        Err(mut error) => {
            // The private removal output is discarded explicitly on every
            // failure path; a failed removal is reported with the error.
            if let Some(collector) = collector {
                if let Err(discard) = collector.discard() {
                    let mut details = error.details.take().unwrap_or_else(|| json!({}));
                    if let Some(members) = details.as_object_mut() {
                        members.insert(
                            "cleanup_failure".to_owned(),
                            json!({"code": discard.code, "message": discard.message}),
                        );
                    }
                    error.details = Some(details);
                }
            }
            return Err(error);
        }
    };
    if let Some(collector) = collector {
        match result
            .get("commit")
            .and_then(|commit| commit.get("durability"))
            .and_then(Value::as_str)
        {
            Some("committed") => match collector.publish() {
                Ok(removals) => {
                    result["removals"] = removals;
                }
                Err(error) => {
                    // The database transaction committed and the auxiliary
                    // removal output is unresolved: both facts are reported. The
                    // transaction outcome owns the reply, and the publication
                    // facts (stage, digest, counts, visibility) travel with the
                    // failure that produced them.
                    let mut failure = json!({
                        "code": error.code,
                        "outcome": error.outcome,
                        "message": error.message,
                    });
                    let facts = error
                        .details
                        .as_ref()
                        .and_then(Value::as_object)
                        .cloned()
                        .unwrap_or_default();
                    if let Some(failure_object) = failure.as_object_mut() {
                        for (key, value) in facts {
                            failure_object.insert(key, value);
                        }
                    }
                    let mut details = json!({"result": result});
                    details["removals_publication_failure"] = failure;
                    return Err(HandlerError {
                        code: error.code,
                        outcome: "committed",
                        message: "first-seen removals publication failed".into(),
                        details: Some(details),
                    });
                }
            },
            // No commit or a non-committed commit: discard the private
            // removal file explicitly; a failed removal is reported.
            _ => {
                collector.discard().map_err(|discard| HandlerError {
                    code: discard.code,
                    outcome: "not_started",
                    message: discard.message,
                    details: Some(json!({"result": result})),
                })?;
            }
        }
    }
    bounded(result)
}

pub fn last_seen_refresh(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let path = required_str(object, "path")?;
    require_existing_database(Path::new(path))?;
    let (source_path, source_mode, feed) = decode_current_source(&object["current"])?;
    let refresh_value = u32_value(&object["refresh_value"]).map_err(HandlerError::invalid_params)?;
    let cutoff = u32_value(&object["cutoff"]).map_err(HandlerError::invalid_params)?;
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;

    let mut reader = open_source_reader(&source_path, &source_mode, &state.token())?;
    let family = match reader.info() {
        Ok(info) => info.address_family,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut reader),
                error,
            ));
        }
    };
    let mut writer = match LiveWriter::open(path, budget, &state.token()) {
        Ok(writer) => writer,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(reader::close_on_error(
                std::slice::from_mut(&mut reader),
                error,
            ))
        }
    };
    let facts = match family {
        AddressFamily::Ipv4 => run_last_seen_v4(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            cutoff,
            &metadata,
            &state.token(),
        )?,
        AddressFamily::Ipv6 => run_last_seen_v6(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            cutoff,
            &metadata,
            &state.token(),
        )?,
    };
    let value = publisher_value(
        facts,
        &mut writer,
        &metadata,
        "iprange.v1.retention.last_seen.refresh",
        None,
        &state.token(),
    )?;
    bounded(value)
}

// ---------------------------------------------------------------------------
// Per-family refresh drivers
// ---------------------------------------------------------------------------

#[allow(clippy::too_many_arguments)]
fn run_first_seen_v4(
    writer: &mut LiveWriter,
    reader: &mut ReaderValue,
    feed: &str,
    refresh_value: u32,
    mut collector: Option<&mut RemovalCollector>,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<PublisherFacts, HandlerError> {
    let mut refresh = match writer.begin_first_seen_refresh(refresh_value, token) {
        Ok(refresh) => refresh,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(close_refresh_facts(reader, writer, error));
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v4(reader, feed)?;
        refresh.add_ranges_v4(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        let error = lifecycle::sdk_error(&error, "not_started");
        return Err(close_refresh_facts(reader, writer, error));
    }
    let source_close = match close_current_reader(reader) {
        Ok(close) => close,
        Err(error) => {
            drop(refresh);
            return Err(close_writer_facts(writer, error));
        }
    };
    let outcome = match collector.as_deref_mut() {
        Some(collector) => match refresh.finish_input_with_removals_v4(collector) {
            Ok(finished) => Ok(consume_finished(finished, metadata)),
            Err(error) => {
                let violation = collector.take_violation();
                Err(match violation {
                    Some(message) => {
                        HandlerError::new("output_limit", "not_started", message)
                    }
                    None => lifecycle::sdk_error(&error, "not_started"),
                })
            }
        },
        None => match refresh.finish_input() {
            Ok(finished) => Ok(consume_finished(finished, metadata)),
            Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
        },
    };
    let mut facts = match outcome {
        Ok(facts) => facts,
        // The source reader was already factually closed; preserve its
        // close result next to the writer close fact on this error path.
        Err(error) => {
            let error = close_writer_facts(writer, error);
            return Err(merge_source_close(error, source_close));
        }
    };
    facts.source_close = source_close;
    Ok(facts)
}

#[allow(clippy::too_many_arguments)]
fn run_first_seen_v6(
    writer: &mut LiveWriter,
    reader: &mut ReaderValue,
    feed: &str,
    refresh_value: u32,
    mut collector: Option<&mut RemovalCollector>,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<PublisherFacts, HandlerError> {
    let mut refresh = match writer.begin_first_seen_refresh(refresh_value, token) {
        Ok(refresh) => refresh,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(close_refresh_facts(reader, writer, error));
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v6(reader, feed)?;
        refresh.add_ranges_v6(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        let error = lifecycle::sdk_error(&error, "not_started");
        return Err(close_refresh_facts(reader, writer, error));
    }
    let source_close = match close_current_reader(reader) {
        Ok(close) => close,
        Err(error) => {
            drop(refresh);
            return Err(close_writer_facts(writer, error));
        }
    };
    let outcome = match collector.as_deref_mut() {
        Some(collector) => match refresh.finish_input_with_removals_v6(collector) {
            Ok(finished) => Ok(consume_finished(finished, metadata)),
            Err(error) => {
                let violation = collector.take_violation();
                Err(match violation {
                    Some(message) => {
                        HandlerError::new("output_limit", "not_started", message)
                    }
                    None => lifecycle::sdk_error(&error, "not_started"),
                })
            }
        },
        None => match refresh.finish_input() {
            Ok(finished) => Ok(consume_finished(finished, metadata)),
            Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
        },
    };
    let mut facts = match outcome {
        Ok(facts) => facts,
        // The source reader was already factually closed; preserve its
        // close result next to the writer close fact on this error path.
        Err(error) => {
            let error = close_writer_facts(writer, error);
            return Err(merge_source_close(error, source_close));
        }
    };
    facts.source_close = source_close;
    Ok(facts)
}

#[allow(clippy::too_many_arguments)]
fn run_last_seen_v4(
    writer: &mut LiveWriter,
    reader: &mut ReaderValue,
    feed: &str,
    refresh_value: u32,
    cutoff: u32,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<PublisherFacts, HandlerError> {
    let mut refresh = match writer.begin_last_seen_refresh(refresh_value, cutoff, token) {
        Ok(refresh) => refresh,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(close_refresh_facts(reader, writer, error));
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v4(reader, feed)?;
        refresh.add_ranges_v4(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        let error = lifecycle::sdk_error(&error, "not_started");
        return Err(close_refresh_facts(reader, writer, error));
    }
    let source_close = match close_current_reader(reader) {
        Ok(close) => close,
        Err(error) => {
            drop(refresh);
            return Err(close_writer_facts(writer, error));
        }
    };
    let outcome = match refresh.finish_input() {
        Ok(finished) => Ok(consume_finished(finished, metadata)),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let mut facts = match outcome {
        Ok(facts) => facts,
        // The source reader was already factually closed; preserve its
        // close result next to the writer close fact on this error path.
        Err(error) => {
            let error = close_writer_facts(writer, error);
            return Err(merge_source_close(error, source_close));
        }
    };
    facts.source_close = source_close;
    Ok(facts)
}

#[allow(clippy::too_many_arguments)]
fn run_last_seen_v6(
    writer: &mut LiveWriter,
    reader: &mut ReaderValue,
    feed: &str,
    refresh_value: u32,
    cutoff: u32,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<PublisherFacts, HandlerError> {
    let mut refresh = match writer.begin_last_seen_refresh(refresh_value, cutoff, token) {
        Ok(refresh) => refresh,
        Err(error) => {
            let error = lifecycle::sdk_error(&error, "not_started");
            return Err(close_refresh_facts(reader, writer, error));
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v6(reader, feed)?;
        refresh.add_ranges_v6(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        let error = lifecycle::sdk_error(&error, "not_started");
        return Err(close_refresh_facts(reader, writer, error));
    }
    let source_close = match close_current_reader(reader) {
        Ok(close) => close,
        Err(error) => {
            drop(refresh);
            return Err(close_writer_facts(writer, error));
        }
    };
    let outcome = match refresh.finish_input() {
        Ok(finished) => Ok(consume_finished(finished, metadata)),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let mut facts = match outcome {
        Ok(facts) => facts,
        // The source reader was already factually closed; preserve its
        // close result next to the writer close fact on this error path.
        Err(error) => {
            let error = close_writer_facts(writer, error);
            return Err(merge_source_close(error, source_close));
        }
    };
    facts.source_close = source_close;
    Ok(facts)
}

fn named_feed_source_v4<'a>(
    reader: &'a ReaderValue,
    name: &str,
) -> Result<FeedRangeSourceV4<'a>, Error> {
    match reader {
        ReaderValue::Immutable(reader) => reader.named_feed_source_v4(name),
        ReaderValue::Live(reader) => reader.named_feed_source_v4(name),
    }
}

fn named_feed_source_v6<'a>(
    reader: &'a ReaderValue,
    name: &str,
) -> Result<FeedRangeSourceV6<'a>, Error> {
    match reader {
        ReaderValue::Immutable(reader) => reader.named_feed_source_v6(name),
        ReaderValue::Live(reader) => reader.named_feed_source_v6(name),
    }
}

fn open_source_reader(
    path: &str,
    mode: &str,
    token: &CancellationToken,
) -> Result<ReaderValue, HandlerError> {
    match Path::new(path).try_exists() {
        Ok(true) => {}
        Ok(false) => {
            return Err(HandlerError::new(
                "invalid_path",
                "not_started",
                format!("current coverage source does not exist: {path}"),
            ))
        }
        Err(error) => {
            return Err(HandlerError::new(
                "io",
                "not_started",
                format!("cannot inspect current coverage source {path}: {error}"),
            ))
        }
    }
    match mode {
        "immutable" => ImmutableReader::open(path)
            .map(ReaderValue::Immutable)
            .map_err(|error| lifecycle::sdk_error(&error, "not_started")),
        _ => LiveReader::open(path, token)
            .map(ReaderValue::Live)
            .map_err(|error| lifecycle::sdk_error(&error, "not_started")),
    }
}

/// Close the ephemeral current-coverage reader and return its factual
/// live close result (`None` for immutable sources). The retention
/// success result carries it as `source_close`; error paths merge it
/// through `close_refresh_facts` before the reader is closed and
/// through `merge_source_close` afterwards.
fn close_current_reader(reader: &mut ReaderValue) -> Result<Option<Value>, HandlerError> {
    reader::close_ephemeral_reader(reader)
}

/// Close the refresh source reader and writer on an error path,
/// merging the factual close results into the error details.
fn close_refresh_facts(
    reader: &mut ReaderValue,
    writer: &mut LiveWriter,
    error: HandlerError,
) -> HandlerError {
    let error = reader::close_on_error(std::slice::from_mut(reader), error);
    close_writer_facts(writer, error)
}

/// Close a live writer on an error path, merging the factual close result
/// into the error details. `LiveWriter::close` aborts any unpublished draft.
pub(crate) fn close_writer_facts(writer: &mut LiveWriter, mut error: HandlerError) -> HandlerError {
    if let Ok(close) = writer.close() {
        if let Ok(close) = lifecycle::close_result(&close) {
            let mut details = error.details.take().unwrap_or_else(|| json!({}));
            if let Some(target) = details.as_object_mut() {
                target.insert("writer_close".into(), close);
            }
            error.details = Some(details);
        }
    }
    error
}

/// Merge the already-factual source close into an error's `details`
/// next to `writer_close` (spec iprange-jsonrpc-v1.md factual-close
/// rule). Callers pass the captured `source_close` only after the
/// current-coverage reader was factually closed; immutable sources
/// pass `None` and the error is returned unchanged. Existing detail
/// members are preserved.
fn merge_source_close(mut error: HandlerError, source_close: Option<Value>) -> HandlerError {
    if let Some(close) = source_close {
        let mut details = error.details.take().unwrap_or_else(|| json!({}));
        if let Some(members) = details.as_object_mut() {
            members.insert("source_close".into(), close);
        }
        error.details = Some(details);
    }
    error
}

// ---------------------------------------------------------------------------
// Result conversions
// ---------------------------------------------------------------------------

fn live_transition_result(result: &LiveTransitionResult) -> Value {
    let mut value = json!({
        "operation": transition_operation(result.operation),
        "status": transition_status(result.status),
        "database_id": convert::hex_id(&result.database_id),
        "transaction_id": convert::decimal_u64(result.transaction_id),
        "commit_nonce": convert::hex_id(&result.commit_nonce),
        "directory_identity": file_identity_fact(&result.directory_identity),
        "main_identity": file_identity_fact(&result.main_identity),
        "main_basename": lifecycle::local_basename_text(&result.main_basename),
        "reader_capacity": result.reader_capacity,
        "sidecar_id": convert::hex_id(&result.sidecar_id),
        "new_sidecar_location": coordination_location(result.new_sidecar_location),
        "residue_possible": result.residue_possible,
        "housekeeping": lifecycle::housekeeping(result.housekeeping, &result.visible_housekeeping),
        "visible_housekeeping": Value::Array(
            result
                .visible_housekeeping
                .iter()
                .map(lifecycle::housekeeping_artifact)
                .collect(),
        ),
    });
    if let Some(policy) = result.reset_policy {
        value["reset_policy"] = json!(reset_policy(policy));
    }
    if let Some(identity) = result.previous_sidecar_identity {
        value["previous_sidecar_identity"] = file_identity_fact(&identity);
    }
    if let Some(identity) = result.new_sidecar_identity {
        value["new_sidecar_identity"] = file_identity_fact(&identity);
    }
    value
}

fn live_residue_result(result: &LiveResidueResult) -> Value {
    let mut value = json!({
        "status": residue_status(result.status),
        "residue_possible": result.residue_possible,
        "housekeeping": lifecycle::housekeeping(result.housekeeping, &result.visible_housekeeping),
        "visible_housekeeping": Value::Array(
            result
                .visible_housekeeping
                .iter()
                .map(lifecycle::housekeeping_artifact)
                .collect(),
        ),
    });
    if let Some(kind) = result.kind {
        value["kind"] = json!(residue_kind(kind));
    }
    if let Some(database_id) = result.database_id {
        value["database_id"] = json!(convert::hex_id(&database_id));
    }
    if let Some(sidecar_id) = result.sidecar_id {
        value["sidecar_id"] = json!(convert::hex_id(&sidecar_id));
    }
    if let Some(capacity) = result.reader_capacity {
        value["reader_capacity"] = json!(capacity);
    }
    if let Some(identity) = result.main_identity {
        value["main_identity"] = file_identity_fact(&identity);
    }
    if let Some(identity) = result.sidecar_identity {
        value["sidecar_identity"] = file_identity_fact(&identity);
    }
    value
}

fn commit_resolution_result(result: &CommitResolutionResult) -> Value {
    json!({
        "attempted_database_id": convert::hex_id(&result.attempted_database_id),
        "attempted_transaction_id": convert::decimal_u64(result.attempted_transaction_id),
        "attempted_commit_nonce": convert::hex_id(&result.attempted_commit_nonce),
        "actual_directory_identity": file_identity_fact(&result.actual_directory_identity),
        "actual_main_identity": file_identity_fact(&result.actual_main_identity),
        "local_file_relation": local_file_relation(result.local_file_relation),
        "resolution": commit_resolution(result.resolution),
        "cleanup": lifecycle::commit_cleanup(&result.cleanup),
        "coordination_cleanup": lifecycle::coordination_cleanup(result.coordination_cleanup),
    })
}

fn file_identity_fact(identity: &iprange_livedb::validation::LocalFileIdentity) -> Value {
    lifecycle::file_identity(identity).unwrap_or_else(|error| json!({"error": error.message}))
}

fn transition_operation(value: LiveTransitionOperation) -> &'static str {
    match value {
        LiveTransitionOperation::Initialize => "initialize",
        LiveTransitionOperation::Reset => "reset",
    }
}

fn transition_status(value: LiveTransitionStatus) -> &'static str {
    match value {
        LiveTransitionStatus::Unchanged => "unchanged",
        LiveTransitionStatus::Initialized => "initialized",
        LiveTransitionStatus::OutcomeUnknown => "outcome_unknown",
    }
}

fn reset_policy(value: LiveResetPolicy) -> &'static str {
    match value {
        LiveResetPolicy::RollbackSafe => "rollback_safe",
        LiveResetPolicy::DiscardPrevious => "discard_previous",
    }
}

fn coordination_location(value: LiveCoordinationLocation) -> &'static str {
    match value {
        LiveCoordinationLocation::Absent => "absent",
        LiveCoordinationLocation::Canonical => "canonical",
        LiveCoordinationLocation::Private => "private",
        LiveCoordinationLocation::Unclassified => "unclassified",
    }
}

fn residue_status(value: LiveResidueStatus) -> &'static str {
    match value {
        LiveResidueStatus::Absent => "absent",
        LiveResidueStatus::Ready => "ready",
        LiveResidueStatus::Completed => "completed",
        LiveResidueStatus::Removed => "removed",
        LiveResidueStatus::OutcomeUnknown => "outcome_unknown",
    }
}

fn residue_kind(value: LiveResidueKind) -> &'static str {
    match value {
        LiveResidueKind::Canonical => "canonical",
        LiveResidueKind::PrivateReset => "private_reset",
    }
}

fn local_file_relation(value: LocalFileRelation) -> &'static str {
    match value {
        LocalFileRelation::SameLocalFile => "same_local_file",
        LocalFileRelation::DifferentLocalFile => "different_local_file",
    }
}

fn commit_resolution(value: CommitResolution) -> &'static str {
    match value {
        CommitResolution::Committed => "committed",
        CommitResolution::NotCommitted => "not_committed",
        CommitResolution::SupersededUnknown => "superseded_unknown",
        CommitResolution::Unresolvable => "unresolvable",
    }
}

// ---------------------------------------------------------------------------
// Direct CSV input source (bounded-batch `RangeSource`)
// ---------------------------------------------------------------------------

struct CsvFailure {
    code: &'static str,
    message: String,
}

impl CsvFailure {
    fn invalid_path(message: impl Into<String>) -> Self {
        Self { code: "invalid_path", message: message.into() }
    }
    fn io(message: impl Into<String>) -> Self {
        Self { code: "io", message: message.into() }
    }
    fn format(message: impl Into<String>) -> Self {
        Self { code: "input_format", message: message.into() }
    }
    fn into_handler_error(self) -> HandlerError {
        HandlerError::new(self.code, "not_started", self.message)
    }
}

fn parse_ipv4(text: &str) -> Result<Ipv4Key, String> {
    text.parse::<Ipv4Addr>()
        .map(|address| Ipv4Key(u32::from(address)))
        .map_err(|_| format!("invalid IPv4 address: {text}"))
}

fn parse_ipv6(text: &str) -> Result<Ipv6Key, String> {
    text.parse::<Ipv6Addr>()
        .map(|address| Ipv6Key::from_u128(u128::from(address)))
        .map_err(|_| format!("invalid IPv6 address: {text}"))
}

/// Open one already-inspected direct CSV input.
///
/// The `fs::metadata()` check in `DirectCsvSource::open` cannot close the
/// window before the open, so only this descriptor decides what will be
/// read: a FIFO swapped into the path after that check is refused with the
/// same class the check uses for a non-regular path, never read.
fn open_direct_csv_file(path: &str) -> Result<File, CsvFailure> {
    let opened = crate::io::caller_open::open_regular(Path::new(path))
        .map_err(|error| CsvFailure::io(format!("open direct CSV input {path}: {error}")))?;
    match opened {
        Some(file) => Ok(file),
        None => Err(CsvFailure::invalid_path(format!(
            "direct CSV input is not a regular file: {path}"
        ))),
    }
}

/// Streaming `from,to,value` CSV reader for `direct.replace`. One bounded
/// line and one bounded batch are retained; rows may be unordered,
/// duplicate, or overlapping, exactly as the direct-replacement workflow
/// requires. Parse failures are classified as `input_format`.
struct DirectCsvSource<K> {
    reader: BufReader<File>,
    max_line_bytes: usize,
    parse_address: fn(&str) -> Result<K, String>,
    batch: Vec<DirectRange<K>>,
    line: Vec<u8>,
    failure: Option<CsvFailure>,
    finished: bool,
}

impl<K> DirectCsvSource<K>
where
    K: Copy + Ord,
{
    fn open(
        path: &str,
        max_line_bytes: usize,
        parse_address: fn(&str) -> Result<K, String>,
    ) -> Result<Self, CsvFailure> {
        match fs::metadata(path) {
            Ok(value) if value.is_file() => {}
            Ok(_) => {
                return Err(CsvFailure::invalid_path(format!(
                    "direct CSV input is not a regular file: {path}"
                )))
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Err(CsvFailure::invalid_path(format!(
                    "direct CSV input does not exist: {path}"
                )))
            }
            Err(error) => {
                return Err(CsvFailure::io(format!(
                    "inspect direct CSV input {path}: {error}"
                )))
            }
        }
        let file = open_direct_csv_file(path)?;
        let mut source = Self {
            reader: BufReader::new(file),
            max_line_bytes,
            parse_address,
            batch: Vec::with_capacity(CSV_BATCH_CAPACITY),
            line: Vec::with_capacity(256),
            failure: None,
            finished: false,
        };
        loop {
            match source.read_line()? {
                Some(line) if line.trim().is_empty() => continue,
                Some(line) if line.trim() == "from,to,value" => return Ok(source),
                Some(line) => {
                    return Err(CsvFailure::format(format!(
                        "direct CSV header must be exactly 'from,to,value', found {line:?}"
                    )))
                }
                None => return Err(CsvFailure::format("direct CSV input is empty")),
            }
        }
    }

    /// Fill the one reusable line buffer with the next logical row and
    /// return it as a validated UTF-8 borrow. The row is parsed and
    /// consumed before the next call; no per-row allocation happens.
    fn read_line(&mut self) -> Result<Option<&str>, CsvFailure> {
        self.line.clear();
        loop {
            let available = match self.reader.fill_buf() {
                Ok(available) => available,
                Err(error) => {
                    return Err(CsvFailure::io(format!("read direct CSV input: {error}")))
                }
            };
            if available.is_empty() {
                if self.line.is_empty() {
                    return Ok(None);
                }
                break;
            }
            let newline = available.iter().position(|byte| *byte == b'\n');
            let take = newline.map_or(available.len(), |at| at + 1);
            if self.line.len() + take > self.max_line_bytes + 1 {
                return Err(CsvFailure::format(format!(
                    "direct CSV line exceeds max_line_bytes {}",
                    self.max_line_bytes
                )));
            }
            self.line.extend_from_slice(&available[..take]);
            self.reader.consume(take);
            if newline.is_some() {
                break;
            }
        }
        if self.line.last() == Some(&b'\r') {
            self.line.pop();
        }
        match std::str::from_utf8(&self.line) {
            Ok(text) => Ok(Some(text)),
            Err(_) => Err(CsvFailure::format("direct CSV input is not valid UTF-8")),
        }
    }

    fn take_failure(&mut self) -> Option<CsvFailure> {
        self.failure.take()
    }
}

/// Parse one `from,to,value` CSV row from a borrowed line. Free of
/// `self` so the caller can reuse the source's line buffer in place.
fn parse_record<K>(
    parse_address: fn(&str) -> Result<K, String>,
    line: &str,
) -> Result<DirectRange<K>, CsvFailure>
where
    K: Copy + Ord,
{
    let mut columns = line.split(',').map(str::trim);
    let expected = || CsvFailure::format("direct CSV row must have exactly 3 columns: from,to,value");
    let from_text = columns.next().ok_or_else(expected)?;
    let to_text = columns.next().ok_or_else(expected)?;
    let value_text = columns.next().ok_or_else(expected)?;
    if columns.next().is_some() {
        return Err(expected());
    }
    let from = parse_address(from_text).map_err(CsvFailure::format)?;
    let to = parse_address(to_text).map_err(CsvFailure::format)?;
    if from > to {
        return Err(CsvFailure::format("range start exceeds range end"));
    }
    let value = value_text
        .parse::<u32>()
        .map_err(|_| CsvFailure::format("value must be unsigned decimal 0 through 4294967295"))?;
    Ok(DirectRange { from, to, value })
}

impl<K> RangeSource<DirectRange<K>> for DirectCsvSource<K>
where
    K: Copy + Ord,
{
    fn next_batch(&mut self) -> Result<Option<&[DirectRange<K>]>, Error> {
        if self.finished {
            return Ok(None);
        }
        // Copy the parse callback out so the row borrow from `read_line`
        // never aliases `self`; the row is parsed in place and its record
        // is pushed before the next line is read.
        let parse_address = self.parse_address;
        self.batch.clear();
        while self.batch.len() < CSV_BATCH_CAPACITY {
            let line = match self.read_line() {
                Ok(Some(line)) => line,
                Ok(None) => {
                    self.finished = true;
                    break;
                }
                Err(failure) => {
                    self.finished = true;
                    self.failure = Some(failure);
                    return Err(Error::InvalidArgument("direct CSV input failed"));
                }
            };
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            match parse_record(parse_address, line) {
                Ok(record) => self.batch.push(record),
                Err(failure) => {
                    self.finished = true;
                    self.failure = Some(failure);
                    return Err(Error::InvalidArgument("direct CSV input failed"));
                }
            }
        }
        Ok((!self.batch.is_empty()).then(|| self.batch.as_slice()))
    }
}

// ---------------------------------------------------------------------------
// First-seen removal log collector
// ---------------------------------------------------------------------------

struct RemovalsSettings {
    destination: PathBuf,
    policy: PublicationPolicy,
    max_rows: u64,
    max_output_bytes: u64,
    max_open_files: u32,
}

fn removals_settings(value: &Value) -> Result<RemovalsSettings, HandlerError> {
    let output = value
        .as_object()
        .ok_or_else(|| invalid("removals_output must be an object"))?;
    Ok(RemovalsSettings {
        destination: PathBuf::from(
            output["path"]
                .as_str()
                .ok_or_else(|| invalid("removals_output.path must be a string"))?,
        ),
        policy: reader::publication_policy(output["publication_policy"].as_str())
            .map_err(|_| HandlerError::invalid_params("removals_output.publication_policy is invalid"))?,
        max_rows: reader::u64_string(output["result_budget"]["max_rows"].as_str())
            .map_err(HandlerError::invalid_params)?,
        max_output_bytes: reader::u64_string(
            output["result_budget"]["max_output_bytes"].as_str(),
        )
        .map_err(HandlerError::invalid_params)?,
        max_open_files: output["result_budget"]["max_open_files"]
            .as_u64()
            .and_then(|parsed| u32::try_from(parsed).ok())
            .ok_or_else(|| HandlerError::invalid_params("max_open_files must be u32"))?,
    })
}

/// Bounded, digest-tracking JSONL writer for first-seen removals. The
/// private file is created in the destination directory before the
/// refresh finishes, survives the commit decision, and is published only
/// by [`RemovalCollector::publish`]; any other path drops the temporary.
struct RemovalCollector {
    file: BufWriter<File>,
    temporary: PathBuf,
    destination: PathBuf,
    policy: PublicationPolicy,
    max_rows: u64,
    max_output_bytes: u64,
    refresh_value: u32,
    rows: u64,
    bytes: u64,
    digest: Sha256,
    line: String,
    violation: Option<String>,
}

impl RemovalCollector {
    fn new(settings: RemovalsSettings, refresh_value: u32) -> Result<Self, HandlerError> {
        if settings.max_open_files < 1 {
            return Err(HandlerError::new(
                "invalid_argument",
                "not_started",
                "removal output requires at least one open file",
            ));
        }
        let parent = settings
            .destination
            .parent()
            .filter(|value| !value.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."));
        match parent.metadata() {
            Ok(value) if value.is_dir() => {}
            Ok(_) => {
                return Err(HandlerError::new(
                    "invalid_path",
                    "not_started",
                    format!("removal output parent is not a directory: {}", parent.display()),
                ))
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Err(HandlerError::new(
                    "invalid_path",
                    "not_started",
                    format!("removal output parent does not exist: {}", parent.display()),
                ))
            }
            Err(error) => {
                return Err(HandlerError::new(
                    "io",
                    "not_started",
                    format!("inspect removal output parent {}: {error}", parent.display()),
                ))
            }
        }
        let mut temporary = parent.to_path_buf();
        temporary.push(format!(".{}.removals.tmp", super::super::new_handle()?));
        let file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .map_err(|error| file_error(error, "create removal output"))?;
        Ok(Self {
            file: BufWriter::with_capacity(64 * 1024, file),
            temporary,
            destination: settings.destination,
            policy: settings.policy,
            max_rows: settings.max_rows,
            max_output_bytes: settings.max_output_bytes,
            refresh_value,
            rows: 0,
            bytes: 0,
            digest: Sha256::new(),
        line: String::with_capacity(160),
            violation: None,
        })
    }

    fn write_line(&mut self, line: &str) -> Result<(), Error> {
        let next_rows = self
            .rows
            .checked_add(1)
            .ok_or_else(|| self.budget_violation("row count overflow"))?;
        if next_rows > self.max_rows {
            return Err(self.budget_violation(&format!("row {next_rows} exceeds max_rows")));
        }
        let next_bytes = self
            .bytes
            .checked_add(line.len() as u64 + 1)
            .ok_or_else(|| self.budget_violation("byte count overflow"))?;
        if next_bytes > self.max_output_bytes {
            return Err(self.budget_violation(&format!(
                "byte {next_bytes} exceeds max_output_bytes"
            )));
        }
        self.file
            .write_all(line.as_bytes())
            .and_then(|()| self.file.write_all(b"\n"))
            .map_err(Error::Io)?;
        self.digest.update(line.as_bytes());
        self.digest.update(b"\n");
        self.rows = next_rows;
        self.bytes = next_bytes;
        Ok(())
    }

    fn budget_violation(&mut self, detail: &str) -> Error {
        self.violation = Some(format!(
            "first-seen removals refused before exceeding budget: {detail}"
        ));
        Error::InvalidArgument("first-seen removal output exceeded its result budget")
    }

    fn take_violation(&mut self) -> Option<String> {
        self.violation.take()
    }

    fn unpublished_facts(&self) -> Value {
        json!({"publication": removal_publication_facts("not_published", "absent")})
    }

    /// Explicit best-effort removal of the private temporary. Every
    /// terminal path that does not publish must call this; removal
    /// failures are reported, never absorbed by an automatic destructor.
    fn discard(self) -> Result<(), HandlerError> {
        match std::fs::remove_file(&self.temporary) {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(file_error(error, "remove removal output temporary")),
        }
    }

    /// Flush, sync, atomically publish, and sync the directory. The caller
    /// invokes this only after the commit is factually known to have
    /// committed; the outcome is reported with the commit's outcome.
    fn publish(mut self) -> Result<Value, HandlerError> {
        match self.publish_inner() {
            Ok(value) => Ok(value),
            Err(mut error) => {
                // The private temporary is removed explicitly on publication
                // failure; a failed removal is reported with the error.
                if let Err(remove_error) = std::fs::remove_file(&self.temporary) {
                    if remove_error.kind() != std::io::ErrorKind::NotFound {
                        let mut details = error.details.take().unwrap_or_else(|| json!({}));
                        if let Some(members) = details.as_object_mut() {
                            members.insert(
                                "cleanup_failure".to_owned(),
                                json!({
                                    "error": remove_error.to_string(),
                                    "path": self.temporary.to_string_lossy(),
                                }),
                            );
                        }
                        error.details = Some(details);
                    }
                }
                Err(error)
            }
        }
    }

    fn publish_inner(&mut self) -> Result<Value, HandlerError> {
        self.file
            .flush()
            .map_err(|error| file_error(error, "sync removal output"))?;
        self.file
            .get_ref()
            .sync_all()
            .map_err(|error| file_error(error, "sync removal output"))?;
        let destination_content = match self.destination.try_exists() {
            Ok(true) => "previous",
            Ok(false) => "created",
            Err(error) => return Err(file_error(error, "inspect removal destination")),
        };
        match self.policy {
            PublicationPolicy::FailIfExists => {
                fs::hard_link(&self.temporary, &self.destination)
                    .map_err(|error| file_error(error, "publish removal output"))?;
                // The removal rows are published from here on: removing the
                // private name is cleanup, and its failure can only mean that
                // the durability of the published entry is unproven. Retry once
                // so the reported fact matches what the namespace holds.
                if let Err(error) = fs::remove_file(&self.temporary) {
                    let temporary_removed = fs::remove_file(&self.temporary).is_ok();
                    return Err(self.publication_failure(
                        error,
                        "remove removal temporary",
                        destination_content,
                        temporary_removed,
                    ));
                }
            }
            PublicationPolicy::ReplaceExisting | PublicationPolicy::ReplaceExistingNoRollback => {
                fs::rename(&self.temporary, &self.destination)
                    .map_err(|error| file_error(error, "publish removal output"))?;
            }
        }
        let parent = self
            .destination
            .parent()
            .filter(|value| !value.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."));
        // The destination holds the complete output; a directory-sync failure
        // leaves that namespace entry's durability unproven. That is an unknown
        // outcome about the auxiliary output, never a reason to touch it.
        sync_directory(parent).map_err(|error| {
            self.publication_failure(
                error,
                "sync removal output directory",
                destination_content,
                true,
            )
        })?;
        let digest = self.digest.clone().finalize();
        let sha256 = digest
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>();
        Ok(json!({
            "publication": removal_publication_facts("published", destination_content),
            "output": {
                "path": self.destination.to_string_lossy(),
                "sha256": sha256,
                "bytes": self.bytes.to_string(),
                "rows": self.rows.to_string(),
            },
        }))
    }

    /// Failure of the removal-output publication once the destination name is
    /// visible. The refresh's database commit is unaffected — the caller reports
    /// it — but the durability of this auxiliary output is unproven, so the
    /// outcome is `outcome_unknown` and the error carries the facts that let the
    /// caller judge the file. Same model as the export writer's
    /// `publication_failure`.
    fn publication_failure(
        &self,
        error: std::io::Error,
        stage: &str,
        destination_content: &str,
        temporary_removed: bool,
    ) -> HandlerError {
        let sha256 = self
            .digest
            .clone()
            .finalize()
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>();
        HandlerError {
            code: "io",
            outcome: "outcome_unknown",
            message: format!("{stage}: {error}"),
            details: Some(json!({
                "publication": {
                    "outcome": "outcome_unknown",
                    "publication_policy": crate::io::export_writer::policy_name(self.policy),
                    "path": self.destination.to_string_lossy(),
                    "stage": stage,
                    "destination_visible": true,
                    "destination_content": destination_content,
                    "temporary_removed": temporary_removed,
                    "rows": self.rows.to_string(),
                    "bytes": self.bytes.to_string(),
                    "sha256": sha256,
                }
            })),
        }
    }
}

/// Published removal facts in the complete `PublicationResult` wire shape.
/// The removal artifact is an adapter-owned text file, not a v4 access
/// controlled artifact, so the access-policy members report `absent`.
fn removal_publication_facts(publication: &str, destination_content: &str) -> Value {
    // Adapter-owned artifact publication: no SDK PublicationResult exists
    // for the removals file, so the facts carry only the outcome.
    json!({
        "publication": publication,
        "destination_content": destination_content,
    })
}

impl FirstSeenRemovalSink<Ipv4Key> for RemovalCollector {
    fn removals(&mut self, batch: &[FirstSeenRemoval<Ipv4Key>]) -> Result<(), Error> {
        for removal in batch {
            self.line.clear();
            self.line.push_str("{\"from\":\"");
            let _ = write!(self.line, "{}\"", Ipv4Addr::from(removal.from.0));
            self.line.push_str(",\"to\":\"");
            let _ = write!(self.line, "{}\"", Ipv4Addr::from(removal.to.0));
            self.line.push_str(",\"first_seen\":");
            let _ = write!(self.line, "{}", removal.first_seen);
            self.line.push_str(",\"removed_at\":");
            let _ = write!(self.line, "{}", self.refresh_value);
            self.line.push_str(",\"addresses\":\"");
            let _ = write!(self.line, "{}\"}}", removal.addresses);
            let line = std::mem::take(&mut self.line);
            let outcome = self.write_line(&line);
            self.line = line;
            outcome?;
        }
        Ok(())
    }
}

impl FirstSeenRemovalSink<Ipv6Key> for RemovalCollector {
    fn removals(&mut self, batch: &[FirstSeenRemoval<Ipv6Key>]) -> Result<(), Error> {
        for removal in batch {
            self.line.clear();
            self.line.push_str("{\"from\":\"");
            let _ = write!(self.line, "{}\"", Ipv6Addr::from(removal.from.to_u128().to_be_bytes()));
            self.line.push_str(",\"to\":\"");
            let _ = write!(self.line, "{}\"", Ipv6Addr::from(removal.to.to_u128().to_be_bytes()));
            self.line.push_str(",\"first_seen\":");
            let _ = write!(self.line, "{}", removal.first_seen);
            self.line.push_str(",\"removed_at\":");
            let _ = write!(self.line, "{}", self.refresh_value);
            self.line.push_str(",\"addresses\":\"");
            let _ = write!(self.line, "{}\"}}", removal.addresses);
            let line = std::mem::take(&mut self.line);
            let outcome = self.write_line(&line);
            self.line = line;
            outcome?;
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Local helpers
// ---------------------------------------------------------------------------

fn decode_current_source(value: &Value) -> Result<(String, String, String), HandlerError> {
    let current = value
        .as_object()
        .ok_or_else(|| invalid("current must be an object"))?;
    let source = current["source"]
        .as_object()
        .ok_or_else(|| invalid("current.source must be an object"))?;
    Ok((
        source["path"]
            .as_str()
            .ok_or_else(|| invalid("current.source.path must be a string"))?
            .to_owned(),
        source["mode"]
            .as_str()
            .ok_or_else(|| invalid("current.source.mode must be a string"))?
            .to_owned(),
        current["feed"]
            .as_str()
            .ok_or_else(|| invalid("current.feed must be a string"))?
            .to_owned(),
    ))
}

fn require_existing_database(path: &Path) -> Result<(), HandlerError> {
    match path.metadata() {
        Ok(value) if value.is_file() => Ok(()),
        Ok(_) => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("database is not a regular file: {}", path.display()),
        )),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Err(HandlerError::new(
            "invalid_path",
            "not_started",
            format!("database does not exist: {}", path.display()),
        )),
        Err(error) => Err(HandlerError::new(
            "io",
            "not_started",
            format!("inspect database {}: {error}", path.display()),
        )),
    }
}

fn required_str<'a>(
    object: &'a serde_json::Map<String, Value>,
    name: &str,
) -> Result<&'a str, HandlerError> {
    object[name]
        .as_str()
        .ok_or_else(|| invalid(format!("{name} must be a string")))
}

fn u32_member(object: &serde_json::Map<String, Value>, name: &str) -> Result<(), String> {
    object[name]
        .as_u64()
        .and_then(|parsed| u32::try_from(parsed).ok())
        .map(|_| ())
        .ok_or_else(|| format!("{name} must be a u32 integer"))
}

fn u32_value(value: &Value) -> Result<u32, String> {
    value
        .as_u64()
        .and_then(|parsed| u32::try_from(parsed).ok())
        .ok_or_else(|| "value must be a u32 integer".to_owned())
}

fn sync_directory(parent: &Path) -> io::Result<()> {
    #[cfg(unix)]
    {
        File::open(parent)?.sync_all()?;
    }
    #[cfg(not(unix))]
    let _ = parent;
    Ok(())
}

fn file_error(error: io::Error, operation: &str) -> HandlerError {
    let message = format!("{operation}: {error}");
    if error.kind() == io::ErrorKind::AlreadyExists {
        HandlerError::new("name_exists", "not_started", message)
    } else {
        HandlerError::new("io", "not_started", message)
    }
}

fn bounded(result: Value) -> Result<Value, HandlerError> {
    reader::bounded_result(result)
}

fn invalid(message: impl Into<String>) -> HandlerError {
    HandlerError::invalid_params(message)
}

#[cfg(test)]
mod tests {
    use super::*;
    use iprange_livedb::create_live;
    use iprange_livedb::{AddressRange, StructureKind, ValueKind, ValueTag};
    use std::path::PathBuf;
    use std::time::{SystemTime, UNIX_EPOCH};

    /// One live IPv4 membership database with an exact named feed over
    /// the given coverage ranges.
    fn live_membership_with_ranges(label: &str, ranges: &[AddressRange<Ipv4Key>]) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-retention-{label}-{}-{unique}",
            std::process::id()
        ));
        create_live(
            &path,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            StructureKind::None,
            ValueTag::new(b"membership").unwrap(),
            1,
            &CancellationToken::new(),
        )
        .unwrap();
        let token = CancellationToken::new();
        let budget = iprange_livedb::TransactionBudget {
            max_heap_bytes: 2 * 1024 * 1024,
            max_private_pages: 10_000,
            max_file_growth_pages: 10_000,
            max_open_files: 2,
        };
        let mut writer = LiveWriter::open(&path, budget, &token).unwrap();
        let mut draft = writer
            .begin_create_feed(FeedName::new("coverage").unwrap(), &token)
            .unwrap();
        draft.add_ranges_v4_slice(ranges).unwrap();
        match draft.finish_input().unwrap() {
            FinishedWorkflow::Changed(prepared) => {
                prepared.commit().unwrap();
            }
            FinishedWorkflow::NoChange(_) => panic!("creating a feed changed nothing"),
        }
        writer.close().unwrap();
        path
    }

    /// One live IPv4 membership database with the canonical two-range feed.
    fn live_membership_with_feed(label: &str) -> PathBuf {
        live_membership_with_ranges(
            label,
            &[
                AddressRange {
                    from: Ipv4Key(u32::from(std::net::Ipv4Addr::new(192, 0, 2, 1))),
                    to: Ipv4Key(u32::from(std::net::Ipv4Addr::new(192, 0, 2, 10))),
                },
                AddressRange {
                    from: Ipv4Key(u32::from(std::net::Ipv4Addr::new(198, 51, 100, 5))),
                    to: Ipv4Key(u32::from(std::net::Ipv4Addr::new(198, 51, 100, 5))),
                },
            ],
        )
    }

    /// One live IPv4 membership database whose coverage omits the
    /// 192.0.2.1-10 range, so a refresh over it must emit removals.
    fn live_membership_with_single_range(label: &str) -> PathBuf {
        live_membership_with_ranges(
            label,
            &[AddressRange {
                from: Ipv4Key(u32::from(std::net::Ipv4Addr::new(198, 51, 100, 5))),
                to: Ipv4Key(u32::from(std::net::Ipv4Addr::new(198, 51, 100, 5))),
            }],
        )
    }

    /// One empty live IPv4 direct database tagged `first_seen`.
    fn live_first_seen_target(label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-retention-{label}-{}-{unique}",
            std::process::id()
        ));
        create_live(
            &path,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            StructureKind::None,
            ValueTag::FIRST_SEEN,
            1,
            &CancellationToken::new(),
        )
        .unwrap();
        path
    }

    fn refresh_params(
        target: &std::path::Path,
        source: &std::path::Path,
        mode: &str,
    ) -> serde_json::Value {
        serde_json::json!({
            "path": target.display().to_string(),
            "current": {
                "feed": "coverage",
                "source": {"path": source.display().to_string(), "mode": mode},
            },
            "refresh_value": 42,
            "metadata": {"mode": "keep"},
            "writer_budget": {
                "max_heap_bytes": "2097152",
                "max_private_pages": "10000",
                "max_growth_pages": "10000",
                "max_open_files": 2,
            },
        })
    }

    fn remove_live(path: &std::path::Path) {
        std::fs::remove_file(path).unwrap();
        std::fs::remove_file(path.with_extension("readers")).ok();
    }

    #[test]
    fn first_seen_refresh_with_live_source_carries_source_close_success_fact() {
        let source = live_membership_with_feed("first-seen-src");
        let target = live_first_seen_target("first-seen-dst");
        let mut state = SessionState::default();
        let result = first_seen_refresh(
            &mut state,
            refresh_params(&target, &source, "live"),
        )
        .unwrap();
        assert_eq!(result["method"], "iprange.v1.retention.first_seen.refresh");
        assert_eq!(result["source_close"]["outcome"], "closed");
        remove_live(&target);
        remove_live(&source);
    }

    #[test]
    fn last_seen_refresh_with_live_source_carries_source_close_success_fact() {
        let source = live_membership_with_feed("last-seen-src");
        // The target tag must be exactly `last_seen` for the workflow.
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let target = std::env::temp_dir().join(format!(
            "iprange-retention-last-seen-dst-{}-{unique}",
            std::process::id()
        ));
        create_live(
            &target,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            StructureKind::None,
            ValueTag::LAST_SEEN,
            1,
            &CancellationToken::new(),
        )
        .unwrap();
        let mut state = SessionState::default();
        let params = serde_json::json!({
            "path": target.display().to_string(),
            "current": {
                "feed": "coverage",
                "source": {"path": source.display().to_string(), "mode": "live"},
            },
            "refresh_value": 42,
            "cutoff": 100,
            "metadata": {"mode": "keep"},
            "writer_budget": {
                "max_heap_bytes": "2097152",
                "max_private_pages": "10000",
                "max_growth_pages": "10000",
                "max_open_files": 2,
            },
        });
        let result = last_seen_refresh(&mut state, params).unwrap();
        assert_eq!(result["method"], "iprange.v1.retention.last_seen.refresh");
        assert_eq!(result["source_close"]["outcome"], "closed");
        remove_live(&target);
        remove_live(&source);
    }

    #[test]
    fn first_seen_refresh_removals_budget_error_after_reader_close_carries_source_close() {
        let source = live_membership_with_feed("first-seen-err-src");
        let target = live_first_seen_target("first-seen-err-dst");
        let mut state = SessionState::default();
        // Prime the target with first-seen records from the full coverage.
        first_seen_refresh(&mut state, refresh_params(&target, &source, "live")).unwrap();
        // A second refresh over narrowed coverage must emit one removal;
        // a zero-row budget turns that drain failure into an
        // output_limit error AFTER the live source reader was closed.
        let narrow = live_membership_with_single_range("first-seen-err-narrow");
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let removals_path = std::env::temp_dir().join(format!(
            "iprange-retention-removals-{}-{unique}.jsonl",
            std::process::id()
        ));
        let error = first_seen_refresh(
            &mut state,
            serde_json::json!({
                "path": target.display().to_string(),
                "current": {
                    "feed": "coverage",
                    "source": {"path": narrow.display().to_string(), "mode": "live"},
                },
                "refresh_value": 84,
                "metadata": {"mode": "keep"},
                "writer_budget": {
                    "max_heap_bytes": "2097152",
                    "max_private_pages": "10000",
                    "max_growth_pages": "10000",
                    "max_open_files": 2,
                },
                "removals_output": {
                    "path": removals_path.display().to_string(),
                    "publication_policy": "fail_if_exists",
                    "result_budget": {
                        "max_rows": "0",
                        "max_output_bytes": "0",
                        "max_open_files": 1,
                    },
                },
            }),
        )
        .unwrap_err();
        assert_eq!(error.code, "output_limit");
        assert_eq!(error.outcome, "not_started");
        let details = error
            .details
            .expect("close facts must be merged into details");
        assert_eq!(details["writer_close"]["outcome"], "closed");
        assert_eq!(details["source_close"]["outcome"], "closed");
        let _ = std::fs::remove_file(&removals_path);
        remove_live(&narrow);
        remove_live(&target);
        remove_live(&source);
    }

    #[test]
    fn publisher_value_staging_error_after_source_close_preserves_source_close() {
        let target = live_first_seen_target("publisher-staging-dst");
        let token = CancellationToken::new();
        let budget = iprange_livedb::TransactionBudget {
            max_heap_bytes: 2 * 1024 * 1024,
            max_private_pages: 10_000,
            max_file_growth_pages: 10_000,
            max_open_files: 2,
        };
        let mut writer = LiveWriter::open(&target, budget, &token).unwrap();
        let facts = PublisherFacts {
            report: serde_json::json!({"source_label": "changed"}),
            metadata_changed: false,
            no_change: false,
            staging_error: Some(Error::InvalidArgument("metadata staging refused")),
            commit: None,
            source_close: Some(serde_json::json!({"outcome": "closed"})),
        };
        let outcome = publisher_value(
            facts,
            &mut writer,
            &lifecycle::MetadataValue::Keep,
            "iprange.v1.retention.first_seen.refresh",
            None,
            &token,
        );
        let error = outcome.unwrap_err();
        assert_eq!(error.code, "invalid_argument");
        let details = error
            .details
            .expect("close facts must be merged into details");
        assert_eq!(details["writer_close"]["outcome"], "closed");
        assert_eq!(details["source_close"]["outcome"], "closed");
        remove_live(&target);
    }

    /// Post-visibility durability semantics of the removals output.
    ///
    /// The refresh's database commit and the durability of this auxiliary
    /// output are two separate facts, and neither may be reported as the
    /// other: a transaction that committed stays reported, while the output
    /// whose directory synchronization failed is `outcome_unknown` with its
    /// publication facts. The trigger is a destination parent that is writable
    /// and searchable but not readable (mode 0311), so every publication step
    /// succeeds and only `open(2)` of the directory fails.
    #[cfg(unix)]
    mod removals_publication_tests {
        use super::*;
        use std::os::unix::fs::PermissionsExt as _;

        fn directory(label: &str) -> PathBuf {
            let unique = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .expect("clock after the unix epoch")
                .as_nanos();
            let path = std::env::temp_dir().join(format!(
                "iprange-removals-{label}-{}-{unique}",
                std::process::id()
            ));
            std::fs::create_dir(&path).expect("create the removals directory");
            path
        }

        fn set_mode(path: &Path, mode: u32) {
            std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode))
                .expect("set the directory mode");
        }

        fn refresh_with_removals_output(
            target: &Path,
            narrow: &Path,
            removals: &Path,
            policy: &str,
        ) -> Result<Value, HandlerError> {
            let mut state = SessionState::default();
            first_seen_refresh(
                &mut state,
                serde_json::json!({
                    "path": target.display().to_string(),
                    "current": {
                        "feed": "coverage",
                        "source": {"path": narrow.display().to_string(), "mode": "live"},
                    },
                    "refresh_value": 84,
                    "metadata": {"mode": "keep"},
                    "writer_budget": {
                        "max_heap_bytes": "2097152",
                        "max_private_pages": "10000",
                        "max_growth_pages": "10000",
                        "max_open_files": 2,
                    },
                    "removals_output": {
                        "path": removals.display().to_string(),
                        "publication_policy": policy,
                        "result_budget": {
                            "max_rows": "10",
                            "max_output_bytes": "4096",
                            "max_open_files": 1,
                        },
                    },
                }),
            )
        }

        /// Prime a `first_seen` target, then refresh it over narrowed coverage
        /// so exactly one removal must be written.
        fn primed_target(label: &str) -> (PathBuf, PathBuf, PathBuf) {
            let source = live_membership_with_feed(&format!("{label}-src"));
            let target = live_first_seen_target(&format!("{label}-dst"));
            let mut state = SessionState::default();
            first_seen_refresh(&mut state, refresh_params(&target, &source, "live")).unwrap();
            let narrow = live_membership_with_single_range(&format!("{label}-narrow"));
            (source, target, narrow)
        }

        #[test]
        fn unresolved_directory_sync_keeps_both_the_commit_and_the_unknown_output() {
            let (source, target, narrow) = primed_target("sync");
            let output = directory("sync");
            let removals = output.join("removals.jsonl");
            set_mode(&output, 0o311);
            let error = refresh_with_removals_output(&target, &narrow, &removals, "fail_if_exists")
                .expect_err("synchronizing an unreadable directory must fail");
            set_mode(&output, 0o700);
            assert_eq!(
                (error.code, error.outcome),
                ("io", "committed"),
                "the committed transaction owns the reply"
            );
            let details = error.details.expect("the failure carries the facts");
            assert_eq!(
                details["result"]["commit"]["durability"], "committed",
                "the commit fact must survive the auxiliary failure"
            );
            let failure = &details["removals_publication_failure"];
            assert_eq!(failure["outcome"], "outcome_unknown");
            assert_eq!(failure["code"], "io");
            let facts = &failure["publication"];
            assert_eq!(facts["stage"], "sync removal output directory");
            assert_eq!(facts["outcome"], "outcome_unknown");
            assert_eq!(facts["publication_policy"], "fail_if_exists");
            assert_eq!(facts["destination_visible"], true);
            assert_eq!(facts["destination_content"], "created");
            assert_eq!(facts["temporary_removed"], true);
            assert_eq!(facts["path"], removals.to_string_lossy().as_ref());
            assert_eq!(facts["rows"], "1");
            assert_eq!(facts["sha256"].as_str().map(str::len), Some(64));
            let written = std::fs::read(&removals).expect("the removals file must survive");
            assert!(written.starts_with(b"{") && written.ends_with(b"}\n"));
            let leftovers: Vec<String> = std::fs::read_dir(&output)
                .expect("read the directory")
                .flatten()
                .map(|entry| entry.file_name().to_string_lossy().into_owned())
                .collect();
            assert_eq!(leftovers, vec!["removals.jsonl".to_owned()], "no residue");
            std::fs::remove_dir_all(&output).ok();
            remove_live(&narrow);
            remove_live(&target);
            remove_live(&source);
        }

        #[test]
        fn failure_before_visibility_is_a_definite_refusal_and_keeps_the_commit() {
            let (source, target, narrow) = primed_target("pre");
            let output = directory("pre");
            let removals = output.join("removals.jsonl");
            std::fs::write(&removals, b"previous\n").expect("write the previous output");
            let error = refresh_with_removals_output(&target, &narrow, &removals, "fail_if_exists")
                .expect_err("fail_if_exists must refuse an existing destination");
            assert_eq!(
                (error.code, error.outcome),
                ("name_exists", "committed"),
                "a refusal before visibility is definite, and the commit stands"
            );
            let details = error.details.expect("the failure carries the facts");
            assert_eq!(details["result"]["commit"]["durability"], "committed");
            let failure = &details["removals_publication_failure"];
            assert_eq!(failure["code"], "name_exists");
            assert_eq!(
                failure["outcome"], "not_started",
                "a failure before the destination appeared is not an unknown outcome"
            );
            assert!(
                failure.get("publication").is_none(),
                "publication facts belong to an unknown outcome only: {failure}"
            );
            // The refusal happened before publication, so the previous file is
            // intact, no private temporary remains, and the reply still reports
            // the committed transaction.
            assert_eq!(std::fs::read(&removals).unwrap(), b"previous\n");
            let leftovers: Vec<String> = std::fs::read_dir(&output)
                .expect("read the directory")
                .flatten()
                .map(|entry| entry.file_name().to_string_lossy().into_owned())
                .collect();
            assert_eq!(leftovers, vec!["removals.jsonl".to_owned()], "no residue");
            std::fs::remove_dir_all(&output).ok();
            remove_live(&narrow);
            remove_live(&target);
            remove_live(&source);
        }

        #[test]
        fn cleanup_stage_failure_reports_the_private_name_still_present() {
            let output = directory("cleanup");
            let removals = output.join("removals.jsonl");
            let settings = removals_settings(&serde_json::json!({
                "path": removals.display().to_string(),
                "publication_policy": "replace_existing",
                "result_budget": {
                    "max_rows": "10",
                    "max_output_bytes": "4096",
                    "max_open_files": 1,
                },
            }))
            .expect("valid removals settings");
            let mut collector = RemovalCollector::new(settings, 84).expect("create the collector");
            collector
                .write_line("{\"feed\":\"coverage\"}")
                .expect("write one removal row");
            let error = collector.publication_failure(
                std::io::Error::from_raw_os_error(libc::EACCES),
                "remove removal temporary",
                "created",
                false,
            );
            assert_eq!((error.code, error.outcome), ("io", "outcome_unknown"));
            let facts = error.details.expect("facts")["publication"].clone();
            assert_eq!(facts["stage"], "remove removal temporary");
            assert_eq!(facts["destination_visible"], true);
            assert_eq!(facts["destination_content"], "created");
            assert_eq!(facts["temporary_removed"], false);
            assert_eq!(facts["publication_policy"], "replace_existing");
            assert_eq!(facts["rows"], "1");
            assert_eq!(facts["sha256"].as_str().map(str::len), Some(64));
            assert!(
                !removals.exists(),
                "reporting must not create or touch the destination"
            );
            std::fs::remove_dir_all(&output).ok();
        }
    }
}

/// The direct CSV input is inspected before it is opened, and an inspection
/// cannot close the window before the open. This pin calls the arm's own
/// open helper with a writerless FIFO already at the path, so the open
/// itself is what must refuse, with the arm's non-regular class. The helper
/// runs on a bounded thread: an open that waits for a writer fails the test
/// instead of hanging the suite.
#[cfg(all(test, unix))]
mod open_refusal_tests {
    use super::open_direct_csv_file;
    use crate::io::caller_open::pin_support;

    #[test]
    fn direct_csv_open_refuses_fifo_as_invalid_path() {
        let directory = pin_support::scratch_dir("csv");
        let path = directory.join("in.csv");
        pin_support::mkfifo(&path);
        let refusal = pin_support::prompt("csv", move || {
            open_direct_csv_file(&path.to_string_lossy())
                .err()
                .map(|failure| (failure.code, failure.message))
        });
        pin_support::remove_dir(&directory);
        let (code, message) = refusal.expect("a writerless fifo must not open as a csv input");
        assert_eq!(code, "invalid_path");
        assert!(
            message.contains("direct CSV input is not a regular file"),
            "unexpected refusal: {message}"
        );
    }
}

/// Arm-level pin for the direct CSV input open.
///
/// The helper-level pin above proves that `open_direct_csv_file` refuses a
/// standing FIFO; it cannot prove that `direct.replace` calls it, because
/// replacing that call with a plain `File::open` keeps the pin green while
/// reintroducing the wedge: a FIFO swapped into the path after
/// `DirectCsvSource::open`'s own `fs::metadata()` check would block the request
/// thread inside `open(2)` forever. This pin therefore drives the registered
/// handler behind `iprange.v1.direct.replace` — the function the JSON-RPC
/// dispatcher calls — while the CSV path is flipped between a regular file and a
/// writerless FIFO. The fixture's first line is not the CSV header, so an arm
/// that opened its input normally fails on the header instead of replacing the
/// database it was pointed at.
#[cfg(all(test, unix))]
mod csv_open_caller_tests {
    use crate::io::caller_open::pin_support::{self, RaceRule};
    use crate::rpc::session::SessionState;
    use iprange_livedb::{create_live, StructureKind, ValueKind, ValueTag};
    use serde_json::json;
    use std::path::{Path, PathBuf};

    /// Attempts per pin; see the input-arm pin for how this count is sized
    /// against the measured per-attempt hit rate.
    const ATTEMPTS: usize = 200;
    const CONCURRENCY: usize = 4;

    /// One empty live IPv4 direct database to point `direct.replace` at. The
    /// fixture never commits, so one database serves every attempt.
    fn live_direct_target(label: &str) -> PathBuf {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after the unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-csv-arm-{label}-{}-{unique}",
            std::process::id()
        ));
        create_live(
            &path,
            iprange_livedb::AddressFamily::Ipv4,
            ValueKind::Direct,
            StructureKind::None,
            ValueTag::new(b"direct").unwrap(),
            1,
            &iprange_livedb::CancellationToken::new(),
        )
        .expect("create the live direct database for the CSV arm pin");
        path
    }

    fn remove_live(path: &Path) {
        let _ = std::fs::remove_file(path);
        let mut sidecar = path.as_os_str().to_os_string();
        sidecar.push(".readers");
        let _ = std::fs::remove_file(PathBuf::from(sidecar));
    }

    #[test]
    fn swapped_fifo_is_refused_by_the_direct_csv_arm() {
        let target = live_direct_target("target");
        let rule = RaceRule {
            code: "invalid_path",
            message: "direct CSV input is not a regular file",
            other_accepted: &["direct CSV header must be exactly"],
        };
        let database = target.clone();
        // One database serves every attempt, and a live database admits one
        // writer at a time, so the attempts take turns on it. They never
        // interfere otherwise: no attempt of this fixture gets far enough to
        // change the database, because the raced file is not a CSV the
        // workflow accepts.
        let writer_turn = std::sync::Arc::new(std::sync::Mutex::new(()));
        let content: &'static [u8] = b"bogus,not,a,header\n";
        let arm = std::sync::Arc::new(move |input: PathBuf| {
                let params = json!({
                    "path": database.display().to_string(),
                    "input": {
                        "path": input.display().to_string(),
                        "max_line_bytes": 1048576
                    },
                    "metadata": {"mode": "keep"},
                    "writer_budget": {
                        "max_heap_bytes": "2097152",
                        "max_private_pages": "10000",
                        "max_growth_pages": "10000",
                        "max_open_files": 2
                    }
                });
                super::validate_direct_replace(&params)
                    .expect("the fixture params must satisfy the method schema");
                let _turn = writer_turn.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
                let mut state = SessionState::default();
                let answer = super::direct_replace(&mut state, params)
                    .err()
                    .map(|error| (error.code.to_owned(), error.message));
                drop(_turn);
                answer
            });
        pin_support::assert_control(
            "direct CSV input",
            &pin_support::control("csv-arm-control", content, &arm),
        );
        let report = pin_support::swap_race(
            "csv-arm",
            content,
            &rule,
            ATTEMPTS,
            CONCURRENCY,
            arm,
        );
        remove_live(&target);
        pin_support::assert_race("direct CSV input", &report, ATTEMPTS);
    }
}
