//! Reader and database read-only JSON-RPC handlers.

use std::net::IpAddr;
use std::path::Path;

use iprange_livedb::error::{Error, ErrorCode};
use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::{CloseOutcome, FeedName, ImmutableReader};
use iprange_livedb::{Ipv4Key, Ipv6Key};
use iprange_livedb::{NetworkEnrichmentV1View, ValueKind};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::schema;
use super::super::session::SessionState;
use super::super::state::{CursorPoint, ReaderValue};
use super::convert;
use super::lifecycle;
use super::output;

pub const READER_LIMIT: usize = 64;

pub fn validate_reader_source(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["source"])?;
    let source = member_object(object, "source")?;
    exact_object_map(source, &["path", "mode"])?;
    validate_path(source["path"].as_str())?;
    match source["mode"].as_str() {
        Some("immutable") | Some("live") => Ok(()),
        _ => Err("source.mode must be immutable or live".into()),
    }
}

pub fn validate_reader_handle(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["reader"])?;
    validate_handle(object["reader"].as_str())
}

pub fn validate_metadata(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["reader", "delivery"])?;
    validate_handle(object["reader"].as_str())?;
    validate_delivery(&object["delivery"])
}

pub fn validate_database_metadata(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["source", "delivery"])?;
    let source = member_object(object, "source")?;
    exact_object_map(source, &["path", "mode"])?;
    validate_path(source["path"].as_str())?;
    match source["mode"].as_str() {
        Some("immutable") | Some("live") => {}
        _ => return Err("source.mode must be immutable or live".into()),
    }
    validate_delivery(&object["delivery"])
}

pub fn validate_lookup(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["reader", "addresses"])?;
    validate_handle(object["reader"].as_str())?;
    let addresses = object["addresses"]
        .as_array()
        .ok_or("addresses must be an array")?;
    if addresses.is_empty() || addresses.len() > 4096 {
        return Err("addresses must contain 1 through 4096 values".into());
    }
    for address in addresses {
        parse_address(address.as_str().ok_or("each address must be a string")?)?;
    }
    Ok(())
}

pub fn validate_matching_feeds(params: &Value) -> Result<(), String> {
    let object = exact_object(params, &["reader", "address"])?;
    validate_handle(object["reader"].as_str())?;
    parse_address(
        object["address"]
            .as_str()
            .ok_or("address must be a string")?,
    )?;
    Ok(())
}

pub fn open(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let (path, mode) = source_params(&params).map_err(HandlerError::invalid_params)?;
    if state.resources.readers.len() >= READER_LIMIT {
        return Err(HandlerError::new(
            "server_busy",
            "not_started",
            "connection reader limit 64 is exhausted",
        ));
    }
    let reader = open_reader(&path, &mode, &state.token)?;
    let info = sdk(reader.info())?;
    let mut handle = random_handle()?;
    while state.resources.readers.contains_key(&handle)
        || state.resources.closed_readers.contains_key(&handle)
    {
        handle = random_handle()?;
    }
    state.resources.readers.insert(handle.clone(), reader);
    let result = json!({
        "method": "iprange.v1.reader.open",
        "reader": handle,
        "info": convert::database_info(&info),
    });
    bounded_result(result)
}

pub fn close(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let handle = handle_param(&params, "reader").map_err(HandlerError::invalid_params)?;
    // The spec treats closing an already-closed handle exactly like an
    // unknown handle: `handle_not_found` (spec reader.close). Other
    // operations on a closed handle still report `handle_closed`.
    if state.resources.closed_readers.contains_key(&handle)
        || !state.resources.readers.contains_key(&handle)
    {
        return Err(HandlerError::new(
            "handle_not_found",
            "not_started",
            "reader handle is unknown or already closed",
        ));
    }
    let mut reader = state.resources.readers.remove(&handle).unwrap();
    let dependent: Vec<String> = state
        .resources
        .cursors
        .iter()
        .filter(|(_, cursor)| cursor.reader == handle)
        .map(|(cursor, _)| cursor.clone())
        .collect();
    for cursor in dependent {
        state.resources.cursors.remove(&cursor);
        state.resources.closed_cursors.insert(cursor, ());
    }
    let source_close = match reader.close_live() {
        Ok(result) => result,
        Err(error) => {
            state.resources.readers.insert(handle.clone(), reader);
            return Err(read_error(error));
        }
    };
    let source_close = match source_close {
        Some(result) => {
            let close = reader_close_result(&result);
            if result.outcome != CloseOutcome::Closed || result.cause.is_some() {
                state.resources.readers.insert(handle.clone(), reader);
                let code = result
                    .cause
                    .as_ref()
                    .map(|error| sdk_code(error.code()))
                    .unwrap_or("io");
                return Err(HandlerError {
                    code,
                    outcome: "read_only_failure",
                    message: result.cause.as_ref().map_or_else(
                        || "live reader close is incomplete".to_owned(),
                        ToString::to_string,
                    ),
                    details: Some(json!({"source_close": close})),
                });
            }
            Some(close)
        }
        None => None,
    };
    state.resources.closed_readers.insert(handle.clone(), ());
    let mut result = json!({
        "method": "iprange.v1.reader.close",
        "closed": true,
    });
    if let Some(source_close) = source_close {
        result["source_close"] = source_close;
    }
    bounded_result(result)
}

pub fn info(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let reader = reader(
        state,
        &handle_param(&params, "reader").map_err(HandlerError::invalid_params)?,
    )?;
    let info = sdk(reader.info())?;
    bounded_result(json!({
        "method": "iprange.v1.reader.info",
        "info": convert::database_info(&info),
    }))
}

pub fn metadata(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let handle = object["reader"]
        .as_str()
        .ok_or_else(|| invalid("reader must be a string"))?;
    let delivery = &object["delivery"];
    let reader = reader(state, handle)?;
    let result = metadata_result("iprange.v1.reader.metadata", reader, delivery)?;
    bounded_result(result)
}

pub fn lookup(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let reader = reader(state, object["reader"].as_str().unwrap_or(""))?;
    let addresses = object["addresses"].as_array().cloned().unwrap_or_default();
    let info = sdk(reader.info())?;
    let mut matches = Vec::with_capacity(addresses.len());
    for address in addresses {
        let text = address
            .as_str()
            .ok_or_else(|| invalid("address must be a string"))?;
        let point = parse_address(text).map_err(HandlerError::invalid_params)?;
        let mut match_value = json!({"address": text, "present": false});
        match info.value_kind {
            ValueKind::Direct => {
                let value = match point {
                    CursorPoint::V4(value) => sdk(reader.lookup_direct_v4(Ipv4Key(value)))?,
                    CursorPoint::V6(value) => {
                        sdk(reader.lookup_direct_v6(Ipv6Key::from_u128(value)))?
                    }
                };
                if let Some(value) = value {
                    match_value["present"] = json!(true);
                    match_value["value"] = json!(value);
                }
            }
            ValueKind::Membership => {
                let feeds = match point {
                    CursorPoint::V4(value) => membership_names(
                        reader,
                        sdk(reader.lookup_membership_v4(Ipv4Key(value)))?.as_ref(),
                    )?,
                    CursorPoint::V6(value) => membership_names(
                        reader,
                        sdk(reader.lookup_membership_v6(Ipv6Key::from_u128(value)))?.as_ref(),
                    )?,
                };
                if !feeds.is_empty() {
                    match_value["present"] = json!(true);
                    match_value["feeds"] = json!(feeds);
                }
            }
            ValueKind::Structured => {
                let view = match point {
                    CursorPoint::V4(value) => {
                        sdk(reader.lookup_network_enrichment_v1_v4(Ipv4Key(value)))?
                    }
                    CursorPoint::V6(value) => {
                        sdk(reader.lookup_network_enrichment_v1_v6(Ipv6Key::from_u128(value)))?
                    }
                };
                if let Some(view) = view {
                    let feeds = threat_feed_names(reader, &view)?;
                    match_value["present"] = json!(true);
                    match_value.merge_enrichment(&convert::enrichment_view(&view, &feeds));
                }
            }
        }
        matches.push(match_value);
    }
    bounded_result(json!({
        "method": "iprange.v1.reader.lookup",
        "matches": matches,
    }))
}

pub fn matching_feeds(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let reader = reader(state, object["reader"].as_str().unwrap_or(""))?;
    let address = object["address"]
        .as_str()
        .ok_or_else(|| invalid("address must be a string"))?;
    let point = parse_address(address).map_err(HandlerError::invalid_params)?;
    let info = sdk(reader.info())?;
    let (feeds, count) = match info.value_kind {
        ValueKind::Direct => {
            return Err(HandlerError::new(
                "handle_wrong_kind",
                "not_started",
                "matching feeds requires a membership-capable database",
            ));
        }
        ValueKind::Membership => {
            let query = sdk(reader.membership_query())?;
            let mut names = Vec::new();
            let report = match point {
                CursorPoint::V4(value) => sdk(query.matching_feeds_v4(
                    Ipv4Key(value),
                    &mut |feed: FeedName| {
                        names.push(feed.as_str().to_owned());
                        Ok(())
                    },
                    &Default::default(),
                ))?,
                CursorPoint::V6(value) => sdk(query.matching_feeds_v6(
                    Ipv6Key::from_u128(value),
                    &mut |feed: FeedName| {
                        names.push(feed.as_str().to_owned());
                        Ok(())
                    },
                    &Default::default(),
                ))?,
            };
            (names, report.matching_feed_count)
        }
        ValueKind::Structured => {
            let view = match point {
                CursorPoint::V4(value) => {
                    sdk(reader.lookup_network_enrichment_v1_v4(Ipv4Key(value)))?
                }
                CursorPoint::V6(value) => {
                    sdk(reader.lookup_network_enrichment_v1_v6(Ipv6Key::from_u128(value)))?
                }
            };
            let names = view
                .map(|view| threat_feed_names(reader, &view))
                .transpose()?
                .unwrap_or_default();
            let count = names.len() as u64;
            (names, count)
        }
    };
    bounded_result(json!({
        "method": "iprange.v1.reader.matching_feeds",
        "address": address,
        "feeds": feeds,
        "matching_feed_count": count.to_string(),
    }))
}

pub fn database_info(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let (path, mode) = source_params(&params).map_err(HandlerError::invalid_params)?;
    let mut reader = open_reader(&path, &mode, &state.token)?;
    let info = sdk(reader.info())?;
    finish_ephemeral_reader(
        &mut reader,
        json!({
            "method": "iprange.v1.database.info",
            "info": convert::database_info(&info),
        }),
    )
}

pub fn database_metadata(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let (path, mode) = source_value(&object["source"]).map_err(HandlerError::invalid_params)?;
    let mut reader = open_reader(&path, &mode, &state.token)?;
    let result = metadata_result(
        "iprange.v1.database.metadata.get",
        &reader,
        &object["delivery"],
    )?;
    finish_ephemeral_reader(&mut reader, result)
}

pub(crate) fn reader<'a>(
    state: &'a mut SessionState,
    handle: &str,
) -> Result<&'a ReaderValue, HandlerError> {
    if !valid_handle(handle) {
        return Err(HandlerError::invalid_params("invalid reader handle"));
    }
    if state.resources.closed_readers.contains_key(handle) {
        return Err(HandlerError::new(
            "handle_closed",
            "not_started",
            "reader handle is already closed",
        ));
    }
    state.resources.readers.get(handle).ok_or_else(|| {
        HandlerError::new(
            "handle_not_found",
            "not_started",
            "reader handle is unknown",
        )
    })
}

fn open_reader(
    path: &str,
    mode: &str,
    cancellation: &iprange_livedb::CancellationToken,
) -> Result<ReaderValue, HandlerError> {
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
            .map_err(read_error),
        _ => iprange_livedb::LiveReader::open(path, cancellation)
            .map(ReaderValue::Live)
            .map_err(read_error),
    }
}

/// Close one internally opened reader and return its factual live
/// close conversion. Immutable readers produce no close fact (`None`).
pub(crate) fn close_ephemeral_reader(
    reader: &mut ReaderValue,
) -> Result<Option<Value>, HandlerError> {
    let Some(result) = reader.close_live().map_err(read_error)? else {
        return Ok(None);
    };
    let close = reader_close_result(&result);
    if result.outcome != CloseOutcome::Closed || result.cause.is_some() {
        let code = result
            .cause
            .as_ref()
            .map(|error| sdk_code(error.code()))
            .unwrap_or("io");
        return Err(HandlerError {
            code,
            outcome: "read_only_failure",
            message: result.cause.as_ref().map_or_else(
                || "live reader close is incomplete".to_owned(),
                ToString::to_string,
            ),
            details: Some(json!({"source_close": close})),
        });
    }
    Ok(Some(close))
}

/// Finish a read-only method that opened one ephemeral reader.
///
/// Success carries the factual live close result as `source_close`
/// when one exists (absent for immutable readers). A close failure is
/// a product error whose details preserve BOTH the completed logical
/// report and the close result (iprange-jsonrpc-v1.md).
pub(crate) fn finish_ephemeral_reader(
    reader: &mut ReaderValue,
    report: Value,
) -> Result<Value, HandlerError> {
    match close_ephemeral_reader(reader) {
        Ok(Some(source_close)) => {
            let mut result = report;
            result
                .as_object_mut()
                .expect("method results are objects")
                .insert("source_close".into(), source_close);
            bounded_result(result)
        }
        Ok(None) => bounded_result(report),
        Err(error) => Err(preserve_completed_report(error, report)),
    }
}

/// Keep the completed logical report of a failed post-report step in
/// the error details so the factual work is never dropped.
pub(crate) fn preserve_completed_report(mut error: HandlerError, report: Value) -> HandlerError {
    let mut details = error.details.take().unwrap_or_else(|| json!({}));
    if let Some(target) = details.as_object_mut() {
        target.insert("report".into(), report);
    }
    error.details = Some(details);
    error
}

fn reader_close_result(result: &iprange_livedb::ReaderCloseResult) -> Value {
    json!({
        "outcome": close_outcome(result.outcome),
        "cleanup": {},
        "coordination_cleanup": lifecycle::coordination_cleanup(result.coordination_cleanup),
    })
}

fn close_outcome(value: CloseOutcome) -> &'static str {
    match value {
        CloseOutcome::Closed => "closed",
        CloseOutcome::CloseIncomplete => "close_incomplete",
    }
}

pub(crate) fn random_handle() -> Result<String, HandlerError> {
    let mut bytes = [0u8; 16];
    getrandom::fill(&mut bytes).map_err(|error| {
        HandlerError::new(
            "io",
            "not_started",
            format!("generate reader handle: {error}"),
        )
    })?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

fn metadata_result(
    method: &str,
    reader: &ReaderValue,
    delivery: &Value,
) -> Result<Value, HandlerError> {
    let delivery = delivery
        .as_object()
        .ok_or_else(|| invalid("delivery must be an object"))?;
    match delivery["mode"].as_str() {
        Some("inline") => match reader.metadata_json().map_err(read_error)? {
            Some(bytes) => bounded_result(json!({
                "method": method,
                "present": true,
                "base64": output::base64_padded(&bytes),
            })),
            None => bounded_result(json!({
                "method": method,
                "present": false,
            })),
        },
        Some("file") => {
            let path = delivery["path"]
                .as_str()
                .ok_or_else(|| invalid("delivery.path must be a string"))?;
            let policy = publication_policy(delivery["publication_policy"].as_str())
                .map_err(|_| invalid("delivery.publication_policy is invalid"))?;
            let max_output_bytes = u64_string(delivery["max_output_bytes"].as_str())
                .map_err(|_| invalid("delivery.max_output_bytes is invalid"))?;
            let max_open_files = delivery["max_open_files"]
                .as_u64()
                .and_then(|value| u32::try_from(value).ok())
                .ok_or_else(|| invalid("delivery.max_open_files must be u32"))?;
            match reader.metadata_json().map_err(read_error)? {
                Some(bytes) => {
                    let facts = output::metadata_output(
                        Path::new(path),
                        &bytes,
                        policy,
                        max_output_bytes,
                        max_open_files,
                    )?;
                    bounded_result(json!({
                        "method": method,
                        "present": true,
                        "output": facts,
                    }))
                }
                None => bounded_result(json!({
                    "method": method,
                    "present": false,
                })),
            }
        }
        _ => Err(HandlerError::invalid_params(
            "delivery.mode must be inline or file",
        )),
    }
}

pub(crate) fn membership_names(
    reader: &ReaderValue,
    membership: Option<&iprange_livedb::MembershipView<'_>>,
) -> Result<Vec<String>, HandlerError> {
    let Some(membership) = membership else {
        return Ok(Vec::new());
    };
    let mut feeds = Vec::new();
    let mut cursor = sdk(reader.feed_cursor())?;
    while let Some(entry) = sdk(cursor.next_feed())? {
        if sdk(membership.contains_index(entry.index))? {
            feeds.push(entry.name.as_str().to_owned());
        }
    }
    Ok(feeds)
}

pub(crate) fn threat_feed_names(
    reader: &ReaderValue,
    view: &NetworkEnrichmentV1View<'_>,
) -> Result<Vec<String>, HandlerError> {
    let Some(membership) = sdk(view.threat_membership())? else {
        return Ok(Vec::new());
    };
    let mut feeds = Vec::new();
    let mut cursor = sdk(reader.feed_cursor())?;
    while let Some(entry) = sdk(cursor.next_feed())? {
        if sdk(membership.contains_index(entry.index))? {
            feeds.push(entry.name.as_str().to_owned());
        }
    }
    Ok(feeds)
}

pub(crate) fn bounded_result(result: Value) -> Result<Value, HandlerError> {
    if schema::encode_response_object(&json!({"result": result})).is_err() {
        return Err(HandlerError::new(
            "output_limit",
            "read_only_failure",
            "response object exceeds the 65000-byte limit",
        ));
    }
    Ok(result)
}

pub(crate) fn read_error(error: Error) -> HandlerError {
    HandlerError::new(
        sdk_code(error.code()),
        "read_only_failure",
        error.to_string(),
    )
}

pub(crate) fn sdk<T>(result: Result<T, Error>) -> Result<T, HandlerError> {
    result.map_err(read_error)
}

pub(crate) fn sdk_code(code: ErrorCode) -> &'static str {
    match code {
        ErrorCode::InvalidArgument => "invalid_argument",
        ErrorCode::NullPointer => "null_pointer",
        ErrorCode::MisalignedPointer => "misaligned_pointer",
        ErrorCode::InvalidLength => "invalid_length",
        ErrorCode::InvalidEnum => "invalid_enum",
        ErrorCode::ReservedNonzero => "reserved_nonzero",
        ErrorCode::BufferTooSmall => "buffer_too_small",
        ErrorCode::WrongHandleKind => "handle_wrong_kind",
        ErrorCode::HandleClosed => "handle_closed",
        ErrorCode::HandleBusy => "handle_busy",
        ErrorCode::WrongState => "wrong_state",
        ErrorCode::WrongAddressFamily => "wrong_address_family",
        ErrorCode::WrongValueKind => "wrong_value_kind",
        ErrorCode::WrongValueTag => "wrong_value_tag",
        ErrorCode::RangeReversed => "range_reversed",
        ErrorCode::NameInvalid => "name_invalid",
        ErrorCode::NameExists => "name_exists",
        ErrorCode::NameNotFound => "name_not_found",
        ErrorCode::StaleReference => "stale_reference",
        ErrorCode::ForeignReference => "foreign_reference",
        ErrorCode::NoPendingTransaction => "no_pending_transaction",
        ErrorCode::TransactionAborted => "transaction_aborted",
        ErrorCode::AbortIncomplete => "abort_incomplete",
        ErrorCode::InsufficientResourceBudget => "insufficient_resource_budget",
        ErrorCode::PageSpaceExhausted => "page_space_exhausted",
        ErrorCode::WorkLimitTooSmall => "work_limit_too_small",
        ErrorCode::Cancelled => "cancelled",
        ErrorCode::SourceFailed => "source_failed",
        ErrorCode::SinkFailed => "sink_failed",
        ErrorCode::StoppedBySink => "stopped_by_sink",
        ErrorCode::Io => "io",
        ErrorCode::FormatInvalid => "format_invalid",
        ErrorCode::NotV4 => "not_v4",
        ErrorCode::DurabilityUnsupported => "durability_unsupported",
        ErrorCode::PublicationUnsupported => "publication_unsupported",
        ErrorCode::AccessPolicyUnsupported => "access_policy_unsupported",
        ErrorCode::Conflict => "conflict",
        ErrorCode::Unresolvable => "unresolvable",
        ErrorCode::WriterBusy => "writer_busy",
        ErrorCode::DirectoryIdentityMismatch => "directory_identity_mismatch",
        ErrorCode::DestinationNameMismatch => "destination_name_mismatch",
        ErrorCode::CleanupConflict => "cleanup_conflict",
        ErrorCode::CoordinationSequenceExhausted => "coordination_sequence_exhausted",
        ErrorCode::LiveCoordinationUnsupported => "live_coordination_unsupported",
        ErrorCode::LiveCoordinationCleanupRequired => "live_coordination_cleanup_required",
        ErrorCode::LiveCoordinationMalformedRequiresReset => {
            "live_coordination_malformed_requires_reset"
        }
        ErrorCode::LiveOpenCleanupRequired => "live_open_cleanup_required",
        ErrorCode::LiveRecoveryCoordinationUnavailable => "live_recovery_coordination_unavailable",
        ErrorCode::LiveRecoveryCurrentGenerationUnprovable => {
            "live_recovery_current_generation_unprovable"
        }
        ErrorCode::LiveRecoveryCurrentGenerationUnreadable => {
            "live_recovery_current_generation_unreadable"
        }
        ErrorCode::RecoveryCandidateChanged => "recovery_candidate_changed",
        ErrorCode::RecoveryPreparationFailed => "recovery_preparation_failed",
        ErrorCode::SnapshotPreparationFailed => "snapshot_preparation_failed",
        ErrorCode::TransitionSuperseded => "transition_superseded",
        ErrorCode::CurrentGenerationUnprovable => "current_generation_unprovable",
        ErrorCode::ForkedHandle => "forked_handle",
        ErrorCode::Panic => "panic",
        ErrorCode::OsUnsupported => "os_unsupported",
        ErrorCode::TransactionIdExhausted => "transaction_id_exhausted",
        ErrorCode::ArithmeticOverflow => "arithmetic_overflow",
        ErrorCode::FeedIndexExhausted => "feed_index_exhausted",
        ErrorCode::MembershipIdExhausted => "membership_id_exhausted",
        ErrorCode::ReaderCapacityExhausted => "reader_capacity_exhausted",
        ErrorCode::CleanupInProgress => "cleanup_in_progress",
        ErrorCode::FaultWorkerUnavailable => "fault_worker_unavailable",
        ErrorCode::FaultWorkerFailed => "fault_worker_failed",
        ErrorCode::UnsupportedStructure => "unsupported_structure",
        ErrorCode::WrongStructureKind => "wrong_structure_kind",
        ErrorCode::StructureIdExhausted => "structure_id_exhausted",
        // ErrorCode is #[non_exhaustive] in iprange-livedb, so Rust requires
        // this arm even after naming every currently published variant.
        _ => "io",
    }
}

pub(crate) fn exact_object<'a>(
    value: &'a Value,
    fields: &[&str],
) -> Result<&'a serde_json::Map<String, Value>, String> {
    let object = value.as_object().ok_or("params must be an object")?;
    for key in object.keys() {
        if !fields.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in fields {
        if !object.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(object)
}

/// Same as `exact_object` but the optional fields may be absent.
pub(crate) fn exact_object_opt<'a>(
    value: &'a Value,
    required: &[&str],
    optional: &[&str],
) -> Result<&'a serde_json::Map<String, Value>, String> {
    let object = value.as_object().ok_or("params must be an object")?;
    for key in object.keys() {
        if !required.contains(&key.as_str()) && !optional.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in required {
        if !object.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(object)
}

pub(crate) fn member_object<'a>(
    object: &'a serde_json::Map<String, Value>,
    name: &str,
) -> Result<&'a serde_json::Map<String, Value>, String> {
    object[name]
        .as_object()
        .ok_or_else(|| format!("{name} must be an object"))
}

pub(crate) fn validate_path(path: Option<&str>) -> Result<(), String> {
    let path = path.ok_or("path must be a string")?;
    // The frozen schema measures path length in Unicode code points
    // (common.PATH.max_len), so count chars, not UTF-8 bytes.
    if path.is_empty() || path.chars().count() > 65_536 || path.contains('\0') || path == "-" {
        return Err("path is empty, '-', over 65536 characters, or contains NUL".into());
    }
    Ok(())
}

pub(crate) fn validate_handle(handle: Option<&str>) -> Result<(), String> {
    let handle = handle.ok_or("handle must be a string")?;
    if !valid_handle(handle) {
        return Err("handle must be 32 lowercase hexadecimal characters".into());
    }
    Ok(())
}

pub(crate) fn valid_handle(handle: &str) -> bool {
    handle.len() == 32
        && handle
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

pub(crate) fn parse_address(address: &str) -> Result<CursorPoint, String> {
    let parsed: IpAddr = address
        .parse()
        .map_err(|_| format!("address is not canonical IP text: {address}"))?;
    if parsed.to_string() != address {
        return Err(format!("address is not canonical IP text: {address}"));
    }
    Ok(match parsed {
        IpAddr::V4(value) => CursorPoint::V4(u32::from(value)),
        IpAddr::V6(value) => CursorPoint::V6(u128::from(value)),
    })
}

pub(crate) fn validate_delivery(value: &Value) -> Result<(), String> {
    let delivery = value.as_object().ok_or("delivery must be an object")?;
    match delivery.get("mode").and_then(Value::as_str) {
        Some("inline") => exact_object(value, &["mode"]).map(|_| ()),
        Some("file") => {
            exact_object(
                value,
                &[
                    "mode",
                    "path",
                    "publication_policy",
                    "max_output_bytes",
                    "max_open_files",
                ],
            )?;
            validate_path(delivery["path"].as_str())?;
            publication_policy(delivery["publication_policy"].as_str())
                .map_err(|_| "delivery.publication_policy is invalid".to_string())?;
            let bytes_limit = u64_string(delivery["max_output_bytes"].as_str())?;
            if bytes_limit == 0 {
                return Err("delivery.max_output_bytes must be positive".into());
            }
            let files = delivery["max_open_files"]
                .as_u64()
                .and_then(|value| u32::try_from(value).ok());
            match files {
                Some(0) => Err("delivery.max_open_files must be positive".into()),
                Some(_) => Ok(()),
                None => Err("delivery.max_open_files must be u32".into()),
            }
        }
        _ => Err("delivery.mode must be inline or file".into()),
    }
}

pub(crate) fn publication_policy(value: Option<&str>) -> Result<PublicationPolicy, ()> {
    match value {
        Some("fail_if_exists") => Ok(PublicationPolicy::FailIfExists),
        Some("replace_existing") => Ok(PublicationPolicy::ReplaceExisting),
        Some("replace_existing_no_rollback") => Ok(PublicationPolicy::ReplaceExistingNoRollback),
        _ => Err(()),
    }
}

pub(crate) fn u64_string(value: Option<&str>) -> Result<u64, String> {
    let value = value.ok_or("value must be a canonical unsigned decimal string")?;
    if value == "0" {
        return Ok(0);
    }
    if value.is_empty()
        || !value.bytes().all(|byte| byte.is_ascii_digit())
        || value.starts_with('0')
    {
        return Err("value must be a canonical unsigned decimal string".into());
    }
    value
        .parse()
        .map_err(|_| "value must be a canonical unsigned decimal string".to_string())
}

pub(crate) fn positive_u64_string(value: Option<&str>) -> Result<u64, String> {
    let parsed = u64_string(value)?;
    if parsed == 0 {
        return Err("value must be a positive canonical unsigned decimal string".into());
    }
    Ok(parsed)
}

pub(crate) fn positive_u32(value: &Value) -> Result<u32, String> {
    match value.as_u64().and_then(|parsed| u32::try_from(parsed).ok()) {
        Some(0) => Err("value must be a positive u32 integer".into()),
        Some(parsed) => Ok(parsed),
        None => Err("value must be a positive u32 integer".into()),
    }
}

fn handle_param(params: &Value, name: &str) -> Result<String, String> {
    let object = params.as_object().ok_or("params must be an object")?;
    let handle = object
        .get(name)
        .and_then(Value::as_str)
        .ok_or_else(|| format!("{name} must be a string"))?;
    validate_handle(Some(handle))?;
    Ok(handle.to_owned())
}

fn source_value(value: &Value) -> Result<(String, String), String> {
    let source = value.as_object().ok_or("source must be an object")?;
    exact_object_map(source, &["path", "mode"])?;
    validate_path(source["path"].as_str())?;
    let mode = source["mode"]
        .as_str()
        .ok_or("source.mode must be a string")?;
    if mode != "immutable" && mode != "live" {
        return Err("source.mode must be immutable or live".into());
    }
    Ok((source["path"].as_str().unwrap().to_owned(), mode.to_owned()))
}

fn source_params(params: &Value) -> Result<(String, String), String> {
    let object = exact_object(params, &["source"])?;
    source_value(&object["source"])
}

fn exact_object_map(
    object: &serde_json::Map<String, Value>,
    fields: &[&str],
) -> Result<(), String> {
    for key in object.keys() {
        if !fields.contains(&key.as_str()) {
            return Err(format!("unknown member {key:?}"));
        }
    }
    for field in fields {
        if !object.contains_key(*field) {
            return Err(format!("missing member {field:?}"));
        }
    }
    Ok(())
}

fn invalid(message: &str) -> HandlerError {
    HandlerError::invalid_params(message)
}

trait MergeEnrichment {
    fn merge_enrichment(&mut self, value: &Value);
}

impl MergeEnrichment for Value {
    fn merge_enrichment(&mut self, value: &Value) {
        if let (Some(target), Some(source)) = (self.as_object_mut(), value.as_object()) {
            for (key, item) in source {
                target.insert(key.clone(), item.clone());
            }
        }
    }
}

#[cfg(test)]
pub(crate) mod test_support {
    use std::fs;
    use std::path::{Path, PathBuf};
    use std::time::{SystemTime, UNIX_EPOCH};

    use iprange_livedb::{
        create_live, AddressFamily, CancellationToken, DirectTransaction, Ipv6Key, LiveWriter,
        StructureKind, TransactionBudget, ValueKind, ValueTag,
    };

    pub(crate) struct DirectV6Live {
        pub path: PathBuf,
    }

    impl DirectV6Live {
        pub fn sidecar(&self) -> PathBuf {
            let mut name = self.path.file_name().unwrap().to_os_string();
            name.push(".readers");
            self.path.with_file_name(name)
        }

        pub fn remove(self) {
            let _ = fs::remove_file(&self.path);
            let _ = fs::remove_file(self.sidecar());
        }
    }

    pub(crate) fn create_direct_v6(label: &str) -> DirectV6Live {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-reader-{label}-{}-{unique}",
            std::process::id()
        ));
        let token = CancellationToken::new();
        create_live(
            &path,
            AddressFamily::Ipv6,
            ValueKind::Direct,
            StructureKind::None,
            ValueTag::new(b"reader-test").unwrap(),
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
        let mut writer = LiveWriter::open(&path, budget, &token).unwrap();
        let mut transaction: DirectTransaction<'_> =
            writer.begin_direct_transaction(&token).unwrap();
        transaction
            .assign_v6(
                Ipv6Key::from_u128(0x20010db8000000000000000000000001),
                Ipv6Key::from_u128(0x20010db800000000000000000000000a),
                7,
            )
            .unwrap();
        transaction.commit().unwrap();
        writer.close().unwrap();
        DirectV6Live { path }
    }

    pub(crate) fn live_source(path: &Path) -> serde_json::Value {
        serde_json::json!({
            "source": {
                "path": path.display().to_string(),
                "mode": "live"
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::handlers::cursors;
    use iprange_livedb::snapshot::{SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode};
    use iprange_livedb::snapshot_to;
    use iprange_livedb::CancellationToken;
    use std::path::PathBuf;

    /// One compact immutable snapshot of a live fixture.
    fn immutable_snapshot(live: &Path) -> PathBuf {
        let immutable = live.with_extension("immutable.iprange");
        snapshot_to(
            live,
            SnapshotSourceMode::Live,
            &immutable,
            SnapshotPublicationPolicy::FailIfExists,
            &SnapshotBudget::new(2 * 1024 * 1024, 10_000, 3),
            &CancellationToken::new(),
        )
        .map_err(|failure| failure.cause.detail.to_string())
        .unwrap();
        immutable
    }

    #[test]
    fn ranges_open_accepts_optional_start() {
        let params = serde_json::json!({
            "reader": "a0000000000000000000000000000000",
            "view": {"kind":"direct"},
            "direction":"forward",
            "start":"192.0.2.1",
            "batch_size":16
        });
        assert!(cursors::validate_ranges_open(&params).is_ok());
    }

    #[test]
    fn live_reader_roundtrip_and_closed_tombstone_are_factual() {
        let fixture = test_support::create_direct_v6("roundtrip");
        let mut state = SessionState::default();
        let opened = open(&mut state, test_support::live_source(&fixture.path)).unwrap();
        let handle = opened["reader"].as_str().unwrap().to_owned();
        assert_eq!(opened["info"]["address_family"], "ipv6");

        let lookup = lookup(
            &mut state,
            serde_json::json!({
                "reader": handle,
                "addresses": ["2001:db8::1"]
            }),
        )
        .unwrap();
        assert_eq!(lookup["matches"][0]["value"], 7);

        let closed = close(&mut state, serde_json::json!({"reader": handle})).unwrap();
        assert_eq!(closed["closed"], true);
        assert_eq!(closed["source_close"]["outcome"], "closed");

        let reused = info(&mut state, serde_json::json!({"reader": handle})).unwrap_err();
        assert_eq!(
            (reused.code, reused.outcome),
            ("handle_closed", "not_started")
        );
        // Closing an already-closed handle reports handle_not_found
        // per the spec (reader.close), unlike use operations.
        let twice = close(&mut state, serde_json::json!({"reader": handle})).unwrap_err();
        assert_eq!(
            (twice.code, twice.outcome),
            ("handle_not_found", "not_started")
        );
        let unknown = info(
            &mut state,
            serde_json::json!({"reader":"b0000000000000000000000000000000"}),
        )
        .unwrap_err();
        assert_eq!(
            (unknown.code, unknown.outcome),
            ("handle_not_found", "not_started")
        );
        fixture.remove();
    }

    #[test]
    fn ephemeral_database_info_reports_live_close_and_omits_it_for_immutable() {
        let fixture = test_support::create_direct_v6("ephemeral-info");
        let mut state = SessionState::default();
        let live = database_info(&mut state, test_support::live_source(&fixture.path)).unwrap();
        assert_eq!(live["method"], "iprange.v1.database.info");
        assert_eq!(live["info"]["address_family"], "ipv6");
        assert_eq!(live["source_close"]["outcome"], "closed");

        let immutable_path = immutable_snapshot(&fixture.path);
        let immutable = database_info(
            &mut state,
            serde_json::json!({
                "source": {
                    "path": immutable_path.display().to_string(),
                    "mode": "immutable"
                }
            }),
        )
        .unwrap();
        assert_eq!(immutable["info"]["address_family"], "ipv6");
        assert!(immutable.get("source_close").is_none());
        std::fs::remove_file(&immutable_path).unwrap();
        fixture.remove();
    }
}

#[cfg(test)]
mod open_failure_tests {
    use super::*;
    use std::fs;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn sdk_open_failure_is_read_only_failure() {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-reader-invalid-{}-{unique}",
            std::process::id()
        ));
        fs::write(&path, b"not an immutable v4 database").unwrap();
        let mut state = SessionState::default();
        let error = open(
            &mut state,
            serde_json::json!({
                "source":{"path":path.display().to_string(),"mode":"immutable"}
            }),
        )
        .unwrap_err();
        assert_eq!(error.outcome, "read_only_failure");
        assert_ne!(error.code, "io");
        fs::remove_file(path).unwrap();
    }
}
