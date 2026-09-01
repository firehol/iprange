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
    if let Some(error) = facts.staging_error {
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
    let result = initialize_live(path, capacity, &state.token)
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
    let result = reset_live_coordination(path, capacity, policy, &state.token)
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
    let result = resolve_create_live(path, &supplied, mode, &state.token)
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
    let result = resolve_live_transition(path, &supplied, mode, &state.token)
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
    let result = resolve_interrupted_live_transition(path, mode, &state.token)
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
    let result = resolve_commit(path, &supplied, mode, &state.token)
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

    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let ipv6 = writer.address_family() == AddressFamily::Ipv6;
    let mut workflow = match writer.begin_direct_replacement(&state.token) {
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
        &state.token,
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
    let mut collector = match object.get("removals_output") {
        Some(output) => Some(RemovalCollector::new(removals_settings(output)?, refresh_value)?),
        None => None,
    };
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let budget = lifecycle::writer_budget(&object["writer_budget"])
        .map_err(HandlerError::invalid_params)?;

    let mut reader = open_source_reader(&source_path, &source_mode, &state.token)?;
    let family = reader
        .info()
        .map_err(|error| lifecycle::sdk_error(&error, "not_started"))?
        .address_family;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let outcome = match family {
        AddressFamily::Ipv4 => run_first_seen_v4(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            collector.as_mut(),
            &metadata,
            &state.token,
        )
        .and_then(|facts| {
            publisher_value(
                facts,
                &mut writer,
                &metadata,
                "iprange.v1.retention.first_seen.refresh",
                collector.as_ref(),
                &state.token,
            )
        }),
        AddressFamily::Ipv6 => run_first_seen_v6(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            collector.as_mut(),
            &metadata,
            &state.token,
        )
        .and_then(|facts| {
            publisher_value(
                facts,
                &mut writer,
                &metadata,
                "iprange.v1.retention.first_seen.refresh",
                collector.as_ref(),
                &state.token,
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
                    let mut details = json!({"result": result});
                    details["removals_publication_failure"] =
                        json!({"code": error.code, "message": error.message});
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

    let mut reader = open_source_reader(&source_path, &source_mode, &state.token)?;
    let family = reader
        .info()
        .map_err(|error| lifecycle::sdk_error(&error, "not_started"))?
        .address_family;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let facts = match family {
        AddressFamily::Ipv4 => run_last_seen_v4(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            cutoff,
            &metadata,
            &state.token,
        )?,
        AddressFamily::Ipv6 => run_last_seen_v6(
            &mut writer,
            &mut reader,
            &feed,
            refresh_value,
            cutoff,
            &metadata,
            &state.token,
        )?,
    };
    let value = publisher_value(
        facts,
        &mut writer,
        &metadata,
        "iprange.v1.retention.last_seen.refresh",
        None,
        &state.token,
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
            return Err(close_writer_facts(
                writer,
                lifecycle::sdk_error(&error, "not_started"),
            ))
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v4(reader, feed)?;
        refresh.add_ranges_v4(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        return Err(close_writer_facts(
            writer,
            lifecycle::sdk_error(&error, "not_started"),
        ));
    }
    if let Err(error) = close_current_reader(reader) {
        drop(refresh);
        return Err(close_writer_facts(writer, error));
    }
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
    outcome.map_err(|error| close_writer_facts(writer, error))
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
            return Err(close_writer_facts(
                writer,
                lifecycle::sdk_error(&error, "not_started"),
            ))
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v6(reader, feed)?;
        refresh.add_ranges_v6(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        return Err(close_writer_facts(
            writer,
            lifecycle::sdk_error(&error, "not_started"),
        ));
    }
    if let Err(error) = close_current_reader(reader) {
        drop(refresh);
        return Err(close_writer_facts(writer, error));
    }
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
    outcome.map_err(|error| close_writer_facts(writer, error))
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
            return Err(close_writer_facts(
                writer,
                lifecycle::sdk_error(&error, "not_started"),
            ))
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v4(reader, feed)?;
        refresh.add_ranges_v4(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        return Err(close_writer_facts(
            writer,
            lifecycle::sdk_error(&error, "not_started"),
        ));
    }
    if let Err(error) = close_current_reader(reader) {
        drop(refresh);
        return Err(close_writer_facts(writer, error));
    }
    let outcome = match refresh.finish_input() {
        Ok(finished) => Ok(consume_finished(finished, metadata)),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    };
    outcome.map_err(|error| close_writer_facts(writer, error))
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
            return Err(close_writer_facts(
                writer,
                lifecycle::sdk_error(&error, "not_started"),
            ))
        }
    };
    let drain = (|| -> Result<(), Error> {
        let mut source = named_feed_source_v6(reader, feed)?;
        refresh.add_ranges_v6(&mut source)
    })();
    if let Err(error) = drain {
        drop(refresh);
        return Err(close_writer_facts(
            writer,
            lifecycle::sdk_error(&error, "not_started"),
        ));
    }
    if let Err(error) = close_current_reader(reader) {
        drop(refresh);
        return Err(close_writer_facts(writer, error));
    }
    let outcome = match refresh.finish_input() {
        Ok(finished) => Ok(consume_finished(finished, metadata)),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    };
    outcome.map_err(|error| close_writer_facts(writer, error))
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

/// Close the ephemeral current-coverage reader. The retention result
/// schemas carry no `source_close` member, so a successful close fact
/// is discarded after the lease is released.
fn close_current_reader(reader: &mut ReaderValue) -> Result<(), HandlerError> {
    reader::close_ephemeral_reader(reader).map(|_| ())
}

/// Close a live writer on an error path, merging the factual close result
/// into the error details. `LiveWriter::close` aborts any unpublished draft.
fn close_writer_facts(writer: &mut LiveWriter, mut error: HandlerError) -> HandlerError {
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
        "main_basename": lifecycle::basename(result.main_basename.as_bytes()),
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
        let file = File::open(path)
            .map_err(|error| CsvFailure::io(format!("open direct CSV input {path}: {error}")))?;
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

    fn read_line(&mut self) -> Result<Option<String>, CsvFailure> {
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
        match String::from_utf8(std::mem::take(&mut self.line)) {
            Ok(text) => Ok(Some(text)),
            Err(_) => Err(CsvFailure::format("direct CSV input is not valid UTF-8")),
        }
    }

    fn parse_record(&self, line: &str) -> Result<DirectRange<K>, CsvFailure> {
        let mut columns = line.split(',').map(str::trim);
        let expected = || CsvFailure::format("direct CSV row must have exactly 3 columns: from,to,value");
        let from_text = columns.next().ok_or_else(expected)?;
        let to_text = columns.next().ok_or_else(expected)?;
        let value_text = columns.next().ok_or_else(expected)?;
        if columns.next().is_some() {
            return Err(expected());
        }
        let from = (self.parse_address)(from_text).map_err(CsvFailure::format)?;
        let to = (self.parse_address)(to_text).map_err(CsvFailure::format)?;
        if from > to {
            return Err(CsvFailure::format("range start exceeds range end"));
        }
        let value = value_text
            .parse::<u32>()
            .map_err(|_| CsvFailure::format("value must be unsigned decimal 0 through 4294967295"))?;
        Ok(DirectRange { from, to, value })
    }

    fn take_failure(&mut self) -> Option<CsvFailure> {
        self.failure.take()
    }
}

impl<K> RangeSource<DirectRange<K>> for DirectCsvSource<K>
where
    K: Copy + Ord,
{
    fn next_batch(&mut self) -> Result<Option<&[DirectRange<K>]>, Error> {
        if self.finished {
            return Ok(None);
        }
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
            match self.parse_record(line) {
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
                fs::remove_file(&self.temporary)
                    .map_err(|error| file_error(error, "remove removal temporary"))?;
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
        sync_directory(parent)?;
        let digest = self.digest.clone().finalize();
        let sha256 = digest.iter().map(|byte| format!("{byte:02x}")).collect::<String>();
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

fn sync_directory(parent: &Path) -> Result<(), HandlerError> {
    #[cfg(unix)]
    {
        File::open(parent)
            .and_then(|directory| directory.sync_all())
            .map_err(|error| file_error(error, "sync removal output directory"))?;
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
