//! Named-feed lifecycle JSON-RPC handlers (iprange-jsonrpc-v1.md).
//!
//! Every mutation opens one clean live writer, performs one high-level
//! SDK workflow, applies the requested metadata inside that draft,
//! commits when changed, and returns the complete workflow and
//! commit/close facts (`v4/cli/schema/results.py` `_PUBLISHER_COMMON`).

use std::path::Path;

use iprange_livedb::{
    AddressFamily, AddressRange, CancellationToken, CommitResult, Error,
    FeedName, FeedRangeSourceV4, FeedRangeSourceV6, FinishedWorkflow, ImmutableReader, Ipv4Key,
    Ipv6Key, LiveReader, LiveWriter, MembershipImportSource, PreparedFeedChange,
    RangeSource, SliceSource, };
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::ReaderValue;
use super::lifecycle;
use super::live::close_writer_facts;
use super::reader;
use super::workflow::{close_writer, finish_publisher, finish_writer_error, publish_changed, publish_no_change, workflow_failure, workflow_report, CommitDraft};

// ---------------------------------------------------------------------------
// Strict params validators (each maps to the frozen methods.py schema).
// ---------------------------------------------------------------------------

pub fn validate_feeds_create(params: &Value) -> Result<(), String> {
    validate_feed_mutation(params, &["path", "feed", "current", "metadata", "writer_budget"])
}

pub fn validate_feeds_replace(params: &Value) -> Result<(), String> {
    validate_feed_mutation(params, &["path", "feed", "current", "metadata", "writer_budget"])
}

pub fn validate_feeds_delete(params: &Value) -> Result<(), String> {
    validate_feed_mutation(params, &["path", "feed", "metadata", "writer_budget"])
}

pub fn validate_feeds_rename(params: &Value) -> Result<(), String> {
    validate_feed_mutation(params, &["path", "old_feed", "new_feed", "metadata", "writer_budget"])
}

pub fn validate_feeds_import(params: &Value) -> Result<(), String> {
    validate_feed_mutation(params, &["path", "source", "metadata", "writer_budget"])
}

fn validate_feed_mutation(params: &Value, fields: &[&str]) -> Result<(), String> {
    let object = reader::exact_object(params, fields)?;
    reader::validate_path(object["path"].as_str())?;
    for member in ["feed", "old_feed", "new_feed"] {
        if let Some(value) = object.get(member) {
            validate_feed_name(value.as_str())?;
        }
    }
    if let Some(current) = object.get("current") {
        validate_current(current)?;
    }
    if let Some(source) = object.get("source") {
        validate_source(source)?;
    }
    lifecycle::validate_metadata(&object["metadata"], true)?;
    lifecycle::validate_writer_budget(&object["writer_budget"])?;
    Ok(())
}

fn validate_current(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["source", "feed"])?;
    validate_source(&object["source"])?;
    validate_feed_name(object["feed"].as_str())
}

fn validate_source(value: &Value) -> Result<(), String> {
    let object = reader::exact_object(value, &["path", "mode"])?;
    reader::validate_path(object["path"].as_str())?;
    match object["mode"].as_str() {
        Some("immutable") | Some("live") => Ok(()),
        _ => Err("source.mode must be immutable or live".into()),
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

// ---------------------------------------------------------------------------
// Handlers.
// ---------------------------------------------------------------------------

pub fn feeds_create(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    publisher_feed_workflow(state, params, "iprange.v1.feeds.create", true)
}

pub fn feeds_replace(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    publisher_feed_workflow(state, params, "iprange.v1.feeds.replace", false)
}

pub fn feeds_delete(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    feed_change_workflow(state, params, "iprange.v1.feeds.delete", "delete_feed")
}

pub fn feeds_rename(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    feed_change_workflow(state, params, "iprange.v1.feeds.rename", "rename_feed")
}

pub fn feeds_import(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
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
    let (source_path, source_mode) = source_parts(object, "source")?;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let mut reader = match open_temporary(&source_path, &source_mode, state) {
        Ok(reader) => reader,
        Err(error) => return Err(close_writer_facts(&mut writer, error)),
    };
    // The SDK workflow owns the writer borrow, so the result is consumed
    // inside a collector that touches only the reader; the writer is
    // re-borrowed after the collector returns.
    let outcome = match &reader {
        ReaderValue::Immutable(source) => run_import(
            &mut writer,
            MembershipImportSource::Immutable(source),
            &state.token,
        ),
        ReaderValue::Live(source) => run_import(
            &mut writer,
            MembershipImportSource::Live(source),
            &state.token,
        ),
    };
    let facts = collect_workflow_facts(&mut reader, outcome, &metadata);
    finish_workflow_facts(&mut writer, facts, &metadata, &state.token, "iprange.v1.feeds.import")
}

/// Create or replace one named feed from the `current` coverage source.
fn publisher_feed_workflow(
    state: &mut SessionState,
    params: Value,
    method: &'static str,
    create: bool,
) -> Result<Value, HandlerError> {
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
    let target = FeedName::new(
        object["feed"]
            .as_str()
            .ok_or_else(|| HandlerError::invalid_params("feed must be a string"))?,
    )
    .map_err(|_| HandlerError::invalid_params("feed is invalid"))?;
    let current = reader::member_object(object, "current")
        .map_err(HandlerError::invalid_params)?;
    let current_feed = current["feed"]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params("current.feed must be a string"))?;
    let (source_path, source_mode) = source_parts(current, "source")?;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let mut reader = match open_temporary(&source_path, &source_mode, state) {
        Ok(reader) => reader,
        Err(error) => return Err(close_writer_facts(&mut writer, error)),
    };
    let outcome = run_feed_workflow(
        &mut writer,
        &reader,
        current_feed,
        target,
        create,
        &state.token,
    );
    let facts = collect_workflow_facts(&mut reader, outcome, &metadata);
    finish_workflow_facts(&mut writer, facts, &metadata, &state.token, method)
}

/// Mutate one existing feed (delete or rename) and publish metadata facts.
fn feed_change_workflow(
    state: &mut SessionState,
    params: Value,
    method: &'static str,
    workflow: &'static str,
) -> Result<Value, HandlerError> {
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
    let old = FeedName::new(
        object
            .get("old_feed")
            .or_else(|| object.get("feed"))
            .and_then(Value::as_str)
            .ok_or_else(|| HandlerError::invalid_params("feed must be a string"))?,
    )
    .map_err(|_| HandlerError::invalid_params("feed is invalid"))?;
    let mut writer = match LiveWriter::open(path, budget, &state.token) {
        Ok(writer) => writer,
        Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
    };
    let facts = match run_feed_change(&mut writer, object, workflow, old, &state.token) {
        Ok(prepared) => match publish_changed(prepared, &metadata) {
            Ok((metadata_logical_change, commit)) => FeedChangeFacts::Changed {
                metadata_logical_change,
                commit,
            },
            Err(error) => FeedChangeFacts::Failed { error },
        },
        Err(error) => FeedChangeFacts::Failed { error },
    };
    match facts {
        FeedChangeFacts::Changed {
            metadata_logical_change,
            commit,
        } => finish_publisher(&mut writer, method, None, metadata_logical_change, commit),
        FeedChangeFacts::Failed { error } => {
            let close = close_writer(&mut writer);
            let details = match close {
                Ok(close) => json!({"writer_close": close}),
                Err(close_error) => json!({"writer_close_error": close_error.message}),
            };
            Err(HandlerError {
                details: Some(details),
                ..error
            })
        }
    }
}

// ---------------------------------------------------------------------------
// Publisher result machinery shared by every named-feed mutation.
// ---------------------------------------------------------------------------

/// Borrow-free outcome of a completed feed workflow. The SDK's finished
/// workflow owns a writer borrow and runs a destructor, so every factual
/// piece is moved out of the match on the SDK result; the writer is
/// re-borrowed only when these facts are finished.
enum FeedWorkflowFacts {
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

/// Borrow-free outcome of one prepared feed change (delete or rename).
/// The SDK exposes no WorkflowReport for these mutations (product
/// decision D2): the fact-carrying outcome is the commit result plus
/// the metadata and writer-close facts.
enum FeedChangeFacts {
    Changed {
        metadata_logical_change: &'static str,
        commit: Option<std::result::Result<CommitResult, Error>>,
    },
    Failed {
        error: HandlerError,
    },
}

/// Merge the already-factual source close into a later publish-stage
/// error; feeds success results keep the frozen `_PUBLISHER_COMMON`
/// shape with no `source_close` member, so only errors carry it.
fn with_source_close_on_error(
    outcome: Result<Value, HandlerError>,
    source_close: Option<Value>,
) -> Result<Value, HandlerError> {
    match outcome {
        Ok(value) => Ok(value),
        Err(mut error) => {
            if let Some(close) = source_close {
                let mut details = error.details.take().unwrap_or_else(|| json!({}));
                if let Some(members) = details.as_object_mut() {
                    members.insert("source_close".into(), close);
                }
                error.details = Some(details);
            }
            Err(error)
        }
    }
}

/// Consume a completed workflow: close the ephemeral source reader, apply
/// the requested metadata through the prepared draft, and return
/// borrow-free facts. The writer is only re-borrowed by
/// `finish_workflow_facts`, after the workflow borrow has ended.
fn collect_workflow_facts(
    reader: &mut ReaderValue,
    outcome: std::result::Result<FinishedWorkflow<'_>, HandlerError>,
    metadata: &lifecycle::MetadataValue,
) -> FeedWorkflowFacts {
    let source_close = match reader::close_ephemeral_reader(reader) {
        Ok(close) => close,
        Err(close_error) => {
            if let Ok(workflow) = outcome {
                let report = workflow_report(workflow.report());
                drop(workflow);
                return FeedWorkflowFacts::ReaderCloseFailed { report, close_error };
            }
            // The workflow and the reader close both failed: keep the
            // workflow error primary and merge the factual close
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
            return FeedWorkflowFacts::Failed {
                report: None,
                error,
                source_close: None,
            };
        }
    };
    match outcome {
        Ok(workflow) => {
            let report = workflow_report(workflow.report());
            match workflow {
                FinishedWorkflow::NoChange(_) => FeedWorkflowFacts::NoChange {
                    report,
                    source_close,
                },
                FinishedWorkflow::Changed(prepared) => {
                    match publish_changed(prepared, metadata) {
                        Ok((metadata_logical_change, commit)) => FeedWorkflowFacts::Changed {
                            report,
                            metadata_logical_change,
                            commit,
                            source_close,
                        },
                        Err(error) => FeedWorkflowFacts::Failed {
                            report: Some(report),
                            error,
                            source_close,
                        },
                    }
                }
            }
        }
        Err(error) => FeedWorkflowFacts::Failed {
            report: None,
            error,
            source_close,
        },
    }
}

/// Finish one completed workflow: commit no-change metadata, close the
/// writer, and convert the commit/close facts into the wire result.
fn finish_workflow_facts(
    writer: &mut LiveWriter,
    facts: FeedWorkflowFacts,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
    method: &'static str,
) -> Result<Value, HandlerError> {
    match facts {
        FeedWorkflowFacts::NoChange {
            report,
            source_close,
        } => {
            let outcome = match publish_no_change(writer, metadata, token) {
                Ok((metadata_logical_change, commit)) => {
                    finish_publisher(writer, method, Some(&report), metadata_logical_change, commit)
                }
                Err(error) => Err(finish_writer_error(writer, error, &report)),
            };
            with_source_close_on_error(outcome, source_close)
        }
        FeedWorkflowFacts::Changed {
            report,
            metadata_logical_change,
            commit,
            source_close,
        } => {
            let outcome =
                finish_publisher(writer, method, Some(&report), metadata_logical_change, commit);
            with_source_close_on_error(outcome, source_close)
        }
        FeedWorkflowFacts::Failed {
            report,
            mut error,
            source_close,
        } => {
            let error = match source_close {
                Some(close) => {
                    let mut details = error.details.take().unwrap_or_else(|| json!({}));
                    if let Some(members) = details.as_object_mut() {
                        members.insert("source_close".into(), close);
                    }
                    error.details = Some(details);
                    error
                }
                None => error,
            };
            match report {
                Some(report) => Err(finish_writer_error(writer, error, &report)),
                None => Err(workflow_failure(writer, error)),
            }
        }
        FeedWorkflowFacts::ReaderCloseFailed { report, close_error } => {
            let close = close_writer(writer).ok();
            let mut details = json!({"report": report});
            if let Some(close) = close {
                details["writer_close"] = close;
            }
            Err(reader::preserve_completed_report(close_error, details))
        }
    }
}



/// Everything between the completed SDK workflow and the wire result:
/// close the ephemeral source reader, apply metadata, commit, close the
/// writer, and convert the commit/close facts.
/// Stage the requested metadata inside one changed prepared draft and commit.
impl CommitDraft for PreparedFeedChange<'_> {
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
// Sources and readers.
// ---------------------------------------------------------------------------

/// Create or replace one named feed from the `current` coverage source
/// ranges, returning the completed workflow for metadata/commit handling.
fn run_feed_workflow<'a>(
    writer: &'a mut LiveWriter,
    reader: &ReaderValue,
    current_feed: &str,
    target: FeedName,
    create: bool,
    token: &CancellationToken,
) -> std::result::Result<FinishedWorkflow<'a>, HandlerError> {
    let family = sdk(reader.info())?.address_family;
    let present = feed_present(reader, current_feed)?;
    match family {
        AddressFamily::Ipv4 => {
            let mut input = feed_input_v4(reader, current_feed, present)?;
            if create {
                let mut draft = sdk(writer.begin_create_feed(target, token))?;
                sdk(draft.add_ranges_v4(&mut input))?;
                sdk(draft.finish_input())
            } else {
                let mut draft = sdk(writer.begin_replace_feed(target, token))?;
                sdk(draft.add_ranges_v4(&mut input))?;
                sdk(draft.finish_input())
            }
        }
        AddressFamily::Ipv6 => {
            let mut input = feed_input_v6(reader, current_feed, present)?;
            if create {
                let mut draft = sdk(writer.begin_create_feed(target, token))?;
                sdk(draft.add_ranges_v6(&mut input))?;
                sdk(draft.finish_input())
            } else {
                let mut draft = sdk(writer.begin_replace_feed(target, token))?;
                sdk(draft.add_ranges_v6(&mut input))?;
                sdk(draft.finish_input())
            }
        }
    }
}

/// Delete or rename one existing feed, returning the prepared change.
fn run_feed_change<'a>(
    writer: &'a mut LiveWriter,
    object: &serde_json::Map<String, Value>,
    workflow: &'static str,
    old: FeedName,
    token: &CancellationToken,
) -> std::result::Result<PreparedFeedChange<'a>, HandlerError> {
    if workflow == "delete_feed" {
        sdk(writer.delete_feed(old, token))
    } else {
        let new = FeedName::new(
            object["new_feed"]
                .as_str()
                .ok_or_else(|| HandlerError::invalid_params("new_feed must be a string"))?,
        )
        .map_err(|_| HandlerError::invalid_params("new_feed is invalid"))?;
        sdk(writer.rename_feed(old, new, token))
    }
}

/// Complete name-based import from one pinned membership reader.
fn run_import<'a>(
    writer: &'a mut LiveWriter,
    source: MembershipImportSource<'_>,
    token: &CancellationToken,
) -> std::result::Result<FinishedWorkflow<'a>, HandlerError> {
    let import = sdk(writer.begin_membership_import(source, token))?;
    sdk(import.finish_input())
}

/// One named feed's ranges stream from the current source; a source that
/// does not contain the feed contributes the empty input so create still
/// preserves an empty feed.
enum FeedInputV4<'a> {
    Feed(FeedRangeSourceV4<'a>),
    Empty(SliceSource<'a, AddressRange<Ipv4Key>>),
}

enum FeedInputV6<'a> {
    Feed(FeedRangeSourceV6<'a>),
    Empty(SliceSource<'a, AddressRange<Ipv6Key>>),
}

impl RangeSource<AddressRange<Ipv4Key>> for FeedInputV4<'_> {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        match self {
            FeedInputV4::Feed(source) => source.next_batch(),
            FeedInputV4::Empty(source) => source.next_batch(),
        }
    }
}

impl RangeSource<AddressRange<Ipv6Key>> for FeedInputV6<'_> {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv6Key>]>> {
        match self {
            FeedInputV6::Feed(source) => source.next_batch(),
            FeedInputV6::Empty(source) => source.next_batch(),
        }
    }
}

fn feed_input_v4<'a>(
    reader: &'a ReaderValue,
    name: &str,
    present: bool,
) -> Result<FeedInputV4<'a>, HandlerError> {
    if !present {
        return Ok(FeedInputV4::Empty(SliceSource::new(&[])));
    }
    match reader {
        ReaderValue::Immutable(reader) => sdk(reader.named_feed_source_v4(name)).map(FeedInputV4::Feed),
        ReaderValue::Live(reader) => sdk(reader.named_feed_source_v4(name)).map(FeedInputV4::Feed),
    }
}

fn feed_input_v6<'a>(
    reader: &'a ReaderValue,
    name: &str,
    present: bool,
) -> Result<FeedInputV6<'a>, HandlerError> {
    if !present {
        return Ok(FeedInputV6::Empty(SliceSource::new(&[])));
    }
    match reader {
        ReaderValue::Immutable(reader) => sdk(reader.named_feed_source_v6(name)).map(FeedInputV6::Feed),
        ReaderValue::Live(reader) => sdk(reader.named_feed_source_v6(name)).map(FeedInputV6::Feed),
    }
}

fn feed_present(reader: &ReaderValue, name: &str) -> Result<bool, HandlerError> {
    match reader {
        ReaderValue::Immutable(reader) => Ok(sdk(reader.lookup_feed(name))?.is_some()),
        ReaderValue::Live(reader) => Ok(sdk(reader.lookup_feed(name))?.is_some()),
    }
}

fn source_parts(object: &serde_json::Map<String, Value>, member: &str) -> Result<(String, String), HandlerError> {
    let source = reader::member_object(object, member)
        .map_err(HandlerError::invalid_params)?;
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

fn sdk<T>(result: iprange_livedb::Result<T>) -> Result<T, HandlerError> {
    result.map_err(reader::read_error)
}
