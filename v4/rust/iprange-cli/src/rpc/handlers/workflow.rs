//! Shared publisher-facts machinery for the mutation handler families.
//!
//! The algebra, history, and feeds mutation handlers all finish the same
//! way: stage the requested metadata inside one prepared SDK draft (or one
//! fresh membership transaction when no draft exists), commit when changed,
//! close the writer, and convert the commit/close facts. This module is the
//! single authority for that finalization; families add only their own
//! `CommitDraft` implementations.

use iprange_livedb::{
    CancellationToken, CommitDurability, CommitResult, Error, LiveWriter, LogicalChange,
    PreparedWorkflow, WorkflowKind, WorkflowReport,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::lifecycle;
use super::reader;

/// Close the live writer and convert its close result.
pub(crate) fn close_writer(writer: &mut LiveWriter) -> Result<Value, HandlerError> {
    match writer.close() {
        Ok(result) => lifecycle::close_result(&result),
        Err(error) => Err(lifecycle::sdk_error(&error, "not_started")),
    }
}

/// One prepared SDK draft that accepts metadata staging and a final commit.
pub(crate) trait CommitDraft {
    fn set_metadata(&mut self, input: &[u8]) -> iprange_livedb::Result<bool>;
    fn clear_metadata(&mut self) -> iprange_livedb::Result<bool>;
    fn commit(self) -> iprange_livedb::Result<CommitResult>;
}

impl CommitDraft for PreparedWorkflow<'_> {
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

/// Stage the requested metadata inside one changed prepared draft and commit.
pub(crate) fn publish_changed<D: CommitDraft>(
    mut draft: D,
    metadata: &lifecycle::MetadataValue,
) -> Result<
    (
        &'static str,
        Option<std::result::Result<CommitResult, Error>>,
    ),
    HandlerError,
> {
    let metadata_logical_change = match metadata {
        lifecycle::MetadataValue::Keep => "unchanged",
        lifecycle::MetadataValue::Clear => match draft.clear_metadata() {
            Ok(true) => "changed",
            Ok(false) => "unchanged",
            Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
        },
        lifecycle::MetadataValue::Replace(bytes) => {
            match draft.set_metadata(bytes) {
                Ok(_) => "changed",
                Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
            }
        }
    };
    Ok((metadata_logical_change, Some(draft.commit())))
}

/// A no-change workflow leaves a clean draft; commit only the requested
/// metadata through one fresh membership transaction. Replacements always
/// commit; clear commits only when metadata was present.
pub(crate) fn publish_no_change(
    writer: &mut LiveWriter,
    metadata: &lifecycle::MetadataValue,
    token: &CancellationToken,
) -> Result<
    (
        &'static str,
        Option<std::result::Result<CommitResult, Error>>,
    ),
    HandlerError,
> {
    match metadata {
        lifecycle::MetadataValue::Keep => Ok(("unchanged", None)),
        lifecycle::MetadataValue::Clear => {
            let mut transaction = match writer.begin_membership_transaction(token) {
                Ok(transaction) => transaction,
                Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
            };
            match transaction.clear_metadata_json() {
                Ok(true) => Ok(("changed", Some(transaction.commit()))),
                Ok(false) => {
                    drop(transaction);
                    Ok(("unchanged", None))
                }
                Err(error) => {
                    let _ = transaction.abort();
                    Err(lifecycle::sdk_error(&error, "not_started"))
                }
            }
        }
        lifecycle::MetadataValue::Replace(bytes) => {
            let mut transaction = match writer.begin_membership_transaction(token) {
                Ok(transaction) => transaction,
                Err(error) => return Err(lifecycle::sdk_error(&error, "not_started")),
            };
            if let Err(error) = transaction.set_metadata_json(bytes) {
                let _ = transaction.abort();
                return Err(lifecycle::sdk_error(&error, "not_started"));
            }
            Ok(("changed", Some(transaction.commit())))
        }
    }
}

/// Convert the final commit/close facts into the publisher result or a
/// product error that preserves every factual field.
pub(crate) fn finish_publisher(
    writer: &mut LiveWriter,
    method: &str,
    report: Option<&Value>,
    metadata_logical_change: &'static str,
    commit: Option<std::result::Result<CommitResult, Error>>,
) -> Result<Value, HandlerError> {
    if let Some(Err(error)) = &commit {
        let close = close_writer(writer)?;
        let failure = lifecycle::sdk_error(error, "not_started");
        let mut details = json!({
            "metadata_logical_change": metadata_logical_change,
            "writer_close": close,
            "failure": {"code": failure.code, "message": failure.message},
        });
        if let Some(report) = report {
            details["report"] = report.clone();
        }
        return Err(HandlerError {
            details: Some(details),
            ..failure
        });
    }
    if let Some(Ok(result)) = &commit {
        if result.durability != CommitDurability::Committed || result.cause.is_some() {
            let close = close_writer(writer)?;
            let cause = result.cause.as_ref();
            let code = cause.map_or("io", |error| reader::sdk_code(error.code()));
            let message = cause.map_or_else(
                || "publisher commit did not complete".to_owned(),
                |error| error.to_string(),
            );
            let mut details = json!({
                "metadata_logical_change": metadata_logical_change,
                "commit": lifecycle::commit_result(result)?,
                "writer_close": close,
            });
            if let Some(report) = report {
                details["report"] = report.clone();
            }
            return Err(HandlerError {
                code,
                outcome: lifecycle::durability_outcome(result.durability),
                message,
                details: Some(details),
            });
        }
    }
    let mut result = json!({
        "method": method,
        "metadata_logical_change": metadata_logical_change,
        "writer_close": close_writer(writer)?,
    });
    if let Some(report) = report {
        result["report"] = report.clone();
    }
    if let Some(Ok(commit)) = &commit {
        result["commit"] = lifecycle::commit_result(commit)?;
    }
    if result["writer_close"]["outcome"].as_str() == Some("close_incomplete") {
        return Err(HandlerError {
            code: "io",
            outcome: if commit.is_some() {
                "committed"
            } else {
                "not_started"
            },
            message: "live writer close is incomplete".into(),
            details: Some(result),
        });
    }
    reader::bounded_result(result)
}

/// Abort a failed workflow, close the writer, and keep the close facts.
pub(crate) fn workflow_failure(writer: &mut LiveWriter, error: HandlerError) -> HandlerError {
    let _ = writer.abort();
    let close = close_writer(writer).ok();
    let mut details = json!({});
    if let Some(close) = close {
        details["writer_close"] = close;
    }
    HandlerError {
        details: Some(details),
        ..error
    }
}

/// Close the writer after a metadata-stage failure; the draft is already
/// aborted by the SDK, so the completed logical report is preserved.
pub(crate) fn finish_writer_error(
    writer: &mut LiveWriter,
    error: HandlerError,
    report: &Value,
) -> HandlerError {
    let close = close_writer(writer).ok();
    let mut details = json!({"report": report});
    if let Some(close) = close {
        details["writer_close"] = close;
    }
    HandlerError {
        details: Some(details),
        ..error
    }
}


pub(crate) fn workflow_kind(value: WorkflowKind) -> &'static str {
    match value {
        WorkflowKind::CreateFeed => "create_feed",
        WorkflowKind::ReplaceFeed => "replace_feed",
        WorkflowKind::DirectReplacement => "direct_replacement",
        WorkflowKind::FirstSeenRefresh => "first_seen_refresh",
        WorkflowKind::LastSeenRefresh => "last_seen_refresh",
        WorkflowKind::MembershipImport => "membership_import",
    }
}

pub(crate) fn logical_change(value: LogicalChange) -> &'static str {
    match value {
        LogicalChange::Changed => "changed",
        LogicalChange::NoChange => "unchanged",
    }
}

/// Mechanical `WorkflowReport` conversion for the wire result.
pub(crate) fn workflow_report(report: &WorkflowReport) -> Value {
    json!({
        "workflow": workflow_kind(report.workflow),
        "logical_change": logical_change(report.logical_change),
        "input_record_count": report.input_record_count.to_string(),
        "input_normalized_interval_count": report.input_normalized_interval_count.to_string(),
        "before_range_record_count": report.before_range_record_count.to_string(),
        "after_range_record_count": report.after_range_record_count.to_string(),
        "input_addresses": report.input_addresses.to_string(),
        "before_addresses": report.before_addresses.to_string(),
        "after_addresses": report.after_addresses.to_string(),
        "unchanged_value_addresses": report.unchanged_value_addresses.to_string(),
        "changed_value_addresses": report.changed_value_addresses.to_string(),
        "added_addresses": report.added_addresses.to_string(),
        "removed_addresses": report.removed_addresses.to_string(),
        "source_feed_count": report.source_feed_count.to_string(),
        "matched_feed_count": report.matched_feed_count.to_string(),
        "created_feed_count": report.created_feed_count.to_string(),
        "source_distinct_membership_count": report.source_distinct_membership_count.to_string(),
        "translated_membership_count": report.translated_membership_count.to_string(),
    })
}
