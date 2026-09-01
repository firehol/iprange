//! Reader and database read-only JSON-RPC handlers.

use std::net::IpAddr;
use std::path::Path;

use iprange_livedb::error::{Error, ErrorCode};
use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::{FeedName, ImmutableReader};
use iprange_livedb::{Ipv4Key, Ipv6Key};
use iprange_livedb::{NetworkEnrichmentV1View, ValueKind};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::super::state::{CursorPoint, ReaderValue};
use super::super::{new_handle, schema};
use super::convert;
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
    let reader = open_immutable(&path, &mode)?;
    let info = reader.info();
    let mut handle = new_handle();
    while state.resources.readers.contains_key(&handle) {
        handle = new_handle();
    }
    state
        .resources
        .readers
        .insert(handle.clone(), ReaderValue::Immutable(reader));
    let result = json!({
        "method": "iprange.v1.reader.open",
        "reader": handle,
        "info": convert::database_info(&info),
    });
    bounded_result(result)
}

pub fn close(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let handle = handle_param(&params, "reader").map_err(HandlerError::invalid_params)?;
    if state.resources.readers.remove(&handle).is_none() {
        return Err(HandlerError::new(
            "handle_not_found",
            "not_started",
            "reader handle is unknown or already closed",
        ));
    }
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
    bounded_result(json!({
        "method": "iprange.v1.reader.close",
        "closed": true,
    }))
}

pub fn info(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let reader = reader(
        state,
        &handle_param(&params, "reader").map_err(HandlerError::invalid_params)?,
    )?;
    let info = reader.info();
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
    let info = reader.info();
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
    let info = reader.info();
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

pub fn database_info(_state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let (path, mode) = source_params(&params).map_err(HandlerError::invalid_params)?;
    let reader = open_immutable(&path, &mode)?;
    let info = reader.info();
    bounded_result(json!({
        "method": "iprange.v1.database.info",
        "info": convert::database_info(&info),
    }))
}

pub fn database_metadata(_state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| invalid("params must be an object"))?;
    let (path, mode) = source_value(&object["source"]).map_err(HandlerError::invalid_params)?;
    let reader = open_immutable(&path, &mode)?;
    let result = metadata_result(
        "iprange.v1.database.metadata.get",
        &reader,
        &object["delivery"],
    )?;
    drop(reader);
    bounded_result(result)
}

pub(crate) fn reader<'a>(
    state: &'a mut SessionState,
    handle: &str,
) -> Result<&'a ImmutableReader, HandlerError> {
    if !valid_handle(handle) {
        return Err(HandlerError::invalid_params("invalid reader handle"));
    }
    state
        .resources
        .readers
        .get(handle)
        .and_then(ReaderValue::immutable)
        .ok_or_else(|| {
            HandlerError::new(
                "handle_not_found",
                "not_started",
                "reader handle is unknown or already closed",
            )
        })
}

fn open_immutable(path: &str, mode: &str) -> Result<ImmutableReader, HandlerError> {
    if mode == "live" {
        return Err(HandlerError::new(
            "io",
            "not_started",
            "live reader registration is unsupported",
        ));
    }
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
    ImmutableReader::open(path).map_err(|error| {
        if error.code() == ErrorCode::Io {
            HandlerError::new("io", "not_started", error.to_string())
        } else {
            HandlerError::new(sdk_code(error.code()), "not_started", error.to_string())
        }
    })
}

fn metadata_result(
    method: &str,
    reader: &ImmutableReader,
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

fn membership_names(
    reader: &ImmutableReader,
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
    reader: &ImmutableReader,
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
        ErrorCode::NameInvalid => "name_invalid",
        ErrorCode::NameExists => "name_exists",
        ErrorCode::NameNotFound => "name_not_found",
        ErrorCode::WrongState => "wrong_state",
        ErrorCode::WrongAddressFamily => "wrong_address_family",
        ErrorCode::WrongValueKind => "wrong_value_kind",
        ErrorCode::WrongValueTag => "wrong_value_tag",
        ErrorCode::UnsupportedStructure => "unsupported_structure",
        ErrorCode::WrongStructureKind => "wrong_structure_kind",
        ErrorCode::Io => "io",
        ErrorCode::FormatInvalid => "format_invalid",
        ErrorCode::Cancelled => "cancelled",
        ErrorCode::InsufficientResourceBudget => "insufficient_resource_budget",
        ErrorCode::WrongHandleKind => "handle_wrong_kind",
        ErrorCode::HandleClosed => "handle_closed",
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
    if path.is_empty() || path.len() > 65_536 || path.contains('\0') || path == "-" {
        return Err("path is empty, '-', over 65536 bytes, or contains NUL".into());
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
            u64_string(delivery["max_output_bytes"].as_str())?;
            let files = delivery["max_open_files"]
                .as_u64()
                .and_then(|value| u32::try_from(value).ok());
            if files.is_none() {
                return Err("delivery.max_open_files must be u32".into());
            }
            Ok(())
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
