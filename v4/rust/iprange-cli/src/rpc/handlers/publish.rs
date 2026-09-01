//! Immutable current-feed publication handler.

use iprange_livedb::publication::{
    AccessPolicy, CleanupArtifacts, DestinationContent, LaterCanonical, PublicationResult,
    PublicationStatus,
};
use iprange_livedb::{
    create_immutable_feed_v4, create_immutable_feed_v6, FeedName, ImmutableFeedBudget,
    ImmutableFeedReport, ImmutableFeedResult, Ipv4Key, Ipv6Key,
};
use serde_json::{json, Value};

use super::super::dispatch::HandlerError;
use super::super::session::SessionState;
use super::lifecycle;
use super::reader;
use crate::io::input::{AddressFamilyInput, TextInputOptions, TextInputSource};

pub fn validate_current_publish(params: &Value) -> Result<(), String> {
    let object = exact_object(
        params,
        &[
            "input",
            "feed",
            "value_tag",
            "metadata",
            "destination",
            "publication_policy",
            "immutable_feed_budget",
        ],
    )?;
    validate_text_input(&object["input"])?;
    validate_feed(object["feed"].as_str())?;
    lifecycle::validate_value_tag(&object["value_tag"])?;
    lifecycle::validate_metadata(&object["metadata"], false)?;
    reader::validate_path(object["destination"].as_str())?;
    reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| "publication_policy is invalid".to_string())?;
    validate_immutable_budget(&object["immutable_feed_budget"])
}

pub fn current_publish(state: &mut SessionState, params: Value) -> Result<Value, HandlerError> {
    let object = params
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("params must be an object"))?;
    let input = text_input_params(&object["input"])?;
    let destination = object["destination"]
        .as_str()
        .ok_or_else(|| HandlerError::invalid_params("destination must be a string"))?;
    require_publication_parent(std::path::Path::new(destination))?;
    let metadata = lifecycle::metadata_value(&object["metadata"])?;
    let metadata = match metadata {
        lifecycle::MetadataValue::Keep => None,
        lifecycle::MetadataValue::Clear => None,
        lifecycle::MetadataValue::Replace(bytes) => Some(bytes),
    };
    let feed = FeedName::new(
        object["feed"]
            .as_str()
            .ok_or_else(|| HandlerError::invalid_params("feed must be a string"))?,
    )
    .map_err(|_| HandlerError::invalid_params("feed is invalid"))?;
    let value_tag =
        lifecycle::value_tag(&object["value_tag"]).map_err(HandlerError::invalid_params)?;
    let policy = reader::publication_policy(object["publication_policy"].as_str())
        .map_err(|_| HandlerError::invalid_params("publication_policy is invalid"))?;
    let budget =
        immutable_budget(&object["immutable_feed_budget"]).map_err(HandlerError::invalid_params)?;

    let (outcome, source_error) = match input.family {
        AddressFamilyInput::Ipv4 => {
            let mut source = TextInputSource::<Ipv4Key>::new(
                input.paths,
                input.options,
                input.expand_at_paths,
                input.max_expanded_paths,
            )
            .map_err(input_error)?;
            let outcome = create_immutable_feed_v4(
                destination,
                value_tag,
                feed,
                metadata.as_deref(),
                policy,
                &mut source,
                &budget,
                &state.token,
            );
            (
                outcome,
                source
                    .last_input_error()
                    .zip(source.last_input_message().map(str::to_owned)),
            )
        }
        AddressFamilyInput::Ipv6 => {
            let mut source = TextInputSource::<Ipv6Key>::new(
                input.paths,
                input.options,
                input.expand_at_paths,
                input.max_expanded_paths,
            )
            .map_err(input_error)?;
            let outcome = create_immutable_feed_v6(
                destination,
                value_tag,
                feed,
                metadata.as_deref(),
                policy,
                &mut source,
                &budget,
                &state.token,
            );
            (
                outcome,
                source
                    .last_input_error()
                    .zip(source.last_input_message().map(str::to_owned)),
            )
        }
    };
    match outcome {
        Ok(result) => publication_success(result),
        Err(failure) => Err(preparation_error(&failure, source_error)),
    }
}

fn require_publication_parent(path: &std::path::Path) -> Result<(), HandlerError> {
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
        .unwrap_or_else(|| std::path::Path::new("."));
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

fn publication_success(result: ImmutableFeedResult) -> Result<Value, HandlerError> {
    let publication = publication_result(&result.publication)?;
    if result.publication.publication != PublicationStatus::Published
        || result.publication.cause.is_some()
    {
        let cause = result.publication.cause.as_ref();
        let code = cause.map_or("io", |error| reader::sdk_code(error.code));
        let message = cause.map_or_else(
            || "immutable feed publication did not complete".to_owned(),
            |error| error.detail.to_string(),
        );
        return Err(HandlerError {
            code,
            outcome: publication_status(result.publication.publication),
            message,
            details: Some(json!({
                "report": immutable_feed_report(&result.report),
                "publication": publication,
            })),
        });
    }
    let value = json!({
        "method": "iprange.v1.current.publish",
        "report": immutable_feed_report(&result.report),
        "publication": publication,
    });
    reader::bounded_result(value)
}

pub(crate) fn immutable_feed_report(report: &ImmutableFeedReport) -> Value {
    json!({
        "input_record_count": report.input_record_count.to_string(),
        "normalized_interval_count": report.normalized_interval_count.to_string(),
        "addresses": report.addresses.to_string(),
    })
}

pub(crate) fn publication_result(result: &PublicationResult) -> Result<Value, HandlerError> {
    let mut value = json!({
        "attempt": "attempted",
        "main_namespace_may_have_been_attempted": result.main_namespace_may_have_been_attempted,
        "publication": publication_status(result.publication),
        "destination_content": destination_content(result.destination_content),
        "later_canonical": later_canonical(result.later_canonical),
        "main_access_policy": access_policy(result.main_access_policy),
        "coordination_access_policy": access_policy(result.coordination_access_policy),
        "cleanup": publication_cleanup(&result.cleanup),
        "coordination_cleanup": lifecycle::coordination_cleanup(result.coordination_cleanup),
        "housekeeping": lifecycle::housekeeping(
            result.housekeeping,
            &result.visible_housekeeping,
        ),
        "visible_housekeeping": Value::Array(
            result
                .visible_housekeeping
                .iter()
                .map(lifecycle::housekeeping_artifact)
                .collect(),
        ),
    });
    if let Some(lineage) = result.live_lineage {
        value["live_lineage"] = json!({"kind": live_lineage(lineage)});
    }
    if let Some(id) = result.later_attempt_or_sidecar_id {
        value["later_attempt_or_sidecar_id"] = json!(super::convert::hex_id(&id));
    }
    if let Some(transaction) = result.later_selected_transaction_id {
        value["later_selected_transaction_id"] = json!(super::convert::decimal_u64(transaction));
    }
    if let Some(nonce) = result.later_selected_commit_nonce {
        value["later_selected_commit_nonce"] = json!(super::convert::hex_id(&nonce));
    }
    Ok(value)
}

fn preparation_error(
    failure: &iprange_livedb::ImmutableFeedPreparationFailure,
    source_error: Option<(&'static str, String)>,
) -> HandlerError {
    let (code, source_message) = source_error
        .map(|(code, message)| (code, Some(message)))
        .unwrap_or_else(|| (reader::sdk_code(failure.cause.code), None));
    let outcome = if source_message.is_some() {
        "not_started"
    } else if failure.output.is_some() || !failure.cleanup.is_empty() {
        "not_published"
    } else {
        "not_started"
    };
    HandlerError {
        code,
        outcome,
        message: source_message.unwrap_or_else(|| failure.cause.detail.to_string()),
        details: Some(json!({
            "output": failure.output.as_ref().map(private_output_attempt),
            "cleanup": publication_cleanup(&failure.cleanup),
            "coordination_cleanup": lifecycle::coordination_cleanup(failure.coordination_cleanup),
            "housekeeping": lifecycle::housekeeping(
                failure.housekeeping,
                &failure.visible_housekeeping,
            ),
            "visible_housekeeping": Value::Array(
                failure
                    .visible_housekeeping
                    .iter()
                    .map(lifecycle::housekeeping_artifact)
                    .collect(),
            ),
        })),
    }
}

fn private_output_attempt(value: &iprange_livedb::publication::PrivateOutputAttempt) -> Value {
    json!({
        "publication_attempt_id": super::convert::hex_id(&value.publication_attempt_id),
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

fn publication_cleanup(value: &CleanupArtifacts) -> Value {
    if value.is_empty() {
        return json!({});
    }
    json!({
        "artifacts": value.iter().map(lifecycle::cleanup_artifact).collect::<Vec<_>>(),
    })
}

fn publication_status(value: PublicationStatus) -> &'static str {
    match value {
        PublicationStatus::NotPublished => "not_published",
        PublicationStatus::Published => "published",
        PublicationStatus::OutcomeUnknown => "outcome_unknown",
    }
}

fn destination_content(value: DestinationContent) -> &'static str {
    match value {
        DestinationContent::Desired => "created",
        DestinationContent::Previous => "previous",
        DestinationContent::Absent => "absent",
        DestinationContent::Other => "other",
        DestinationContent::Unclassified => "unclassified",
    }
}

fn later_canonical(value: LaterCanonical) -> &'static str {
    match value {
        LaterCanonical::None => "absent",
        LaterCanonical::ReservationOrTransition => "reservation_or_transition",
        LaterCanonical::ReadyLiveSidecar => "ready_live_sidecar",
    }
}

fn live_lineage(value: iprange_livedb::publication::LiveLineage) -> &'static str {
    match value {
        iprange_livedb::publication::LiveLineage::SameGenerationExactBytes => {
            "same_generation_exact_bytes"
        }
        iprange_livedb::publication::LiveLineage::SameGenerationPhysicalBytesChanged => {
            "same_generation_physical_bytes_changed"
        }
        iprange_livedb::publication::LiveLineage::AdvancedGeneration => "advanced_generation",
    }
}

fn access_policy(value: AccessPolicy) -> &'static str {
    match value {
        AccessPolicy::Absent => "absent",
        AccessPolicy::CreatorOnly => "creator_only",
        AccessPolicy::ChangedOrUnproven => "changed_or_unproven",
        AccessPolicy::Unclassified => "unclassified",
    }
}

struct ParsedTextInput {
    paths: Vec<String>,
    options: TextInputOptions,
    expand_at_paths: bool,
    max_expanded_paths: usize,
    family: AddressFamilyInput,
}

fn text_input_params(value: &Value) -> Result<ParsedTextInput, HandlerError> {
    let object = value
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("input must be an object"))?;
    let paths = object["paths"]
        .as_array()
        .ok_or_else(|| HandlerError::invalid_params("input.paths must be an array"))?
        .iter()
        .map(|path| {
            path.as_str()
                .map(str::to_owned)
                .ok_or_else(|| HandlerError::invalid_params("each input path must be a string"))
        })
        .collect::<Result<Vec<_>, _>>()?;
    let family = match object["family"].as_str() {
        Some("ipv4") => AddressFamilyInput::Ipv4,
        Some("ipv6") => AddressFamilyInput::Ipv6,
        _ => return Err(HandlerError::invalid_params("input.family is invalid")),
    };
    let dns = object["dns"]
        .as_object()
        .ok_or_else(|| HandlerError::invalid_params("input.dns must be an object"))?;
    let threads = dns["threads"]
        .as_u64()
        .and_then(|value| usize::try_from(value).ok())
        .ok_or_else(|| HandlerError::invalid_params("input.dns.threads must be u32"))?;
    let options = TextInputOptions {
        family,
        fix_network: object["fix_network"]
            .as_bool()
            .ok_or_else(|| HandlerError::invalid_params("input.fix_network must be boolean"))?,
        default_prefix: object["default_prefix"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .ok_or_else(|| HandlerError::invalid_params("input.default_prefix must be u32"))?,
        dns_threads: threads,
        dns_silent: dns["silent"]
            .as_bool()
            .ok_or_else(|| HandlerError::invalid_params("input.dns.silent must be boolean"))?,
        max_line_bytes: object["max_line_bytes"]
            .as_u64()
            .and_then(|value| usize::try_from(value).ok())
            .ok_or_else(|| HandlerError::invalid_params("input.max_line_bytes must be u32"))?,
    };
    let max_expanded_paths = object["max_expanded_paths"]
        .as_u64()
        .and_then(|value| usize::try_from(value).ok())
        .ok_or_else(|| HandlerError::invalid_params("input.max_expanded_paths must be u32"))?;
    Ok(ParsedTextInput {
        paths,
        options,
        expand_at_paths: object["expand_at_paths"]
            .as_bool()
            .ok_or_else(|| HandlerError::invalid_params("input.expand_at_paths must be boolean"))?,
        max_expanded_paths,
        family,
    })
}

fn validate_text_input(value: &Value) -> Result<(), String> {
    let object = exact_object(
        value,
        &[
            "paths",
            "family",
            "fix_network",
            "default_prefix",
            "dns",
            "expand_at_paths",
            "max_line_bytes",
            "max_expanded_paths",
        ],
    )?;
    let paths = object["paths"]
        .as_array()
        .ok_or("input.paths must be an array")?;
    if paths.is_empty() {
        return Err("input.paths must contain at least one path".into());
    }
    for path in paths {
        reader::validate_path(path.as_str())?;
    }
    let max_prefix = match object["family"].as_str() {
        Some("ipv4") => 32,
        Some("ipv6") => 128,
        _ => return Err("input.family must be ipv4 or ipv6".into()),
    };
    if object["fix_network"].as_bool().is_none() {
        return Err("input.fix_network must be boolean".into());
    }
    let prefix = object["default_prefix"]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or("input.default_prefix must be u32")?;
    if prefix > max_prefix {
        return Err("input.default_prefix is outside the selected family range".into());
    }
    let dns = exact_object(&object["dns"], &["threads", "silent"])?;
    let threads = dns["threads"]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or("input.dns.threads must be u32")?;
    if threads == 0 || threads > i32::MAX as u32 {
        return Err("input.dns.threads must be 1 through 2147483647".into());
    }
    if dns["silent"].as_bool().is_none() {
        return Err("input.dns.silent must be boolean".into());
    }
    if object["expand_at_paths"].as_bool().is_none() {
        return Err("input.expand_at_paths must be boolean".into());
    }
    let line = object["max_line_bytes"]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or("input.max_line_bytes must be u32")?;
    if line == 0 || line > 1_048_576 {
        return Err("input.max_line_bytes must be 1 through 1048576".into());
    }
    let expanded = object["max_expanded_paths"]
        .as_u64()
        .and_then(|value| u32::try_from(value).ok())
        .ok_or("input.max_expanded_paths must be u32")?;
    if expanded == 0 || expanded > 1_000_000 {
        return Err("input.max_expanded_paths must be 1 through 1000000".into());
    }
    Ok(())
}

fn validate_feed(value: Option<&str>) -> Result<(), String> {
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

fn validate_immutable_budget(value: &Value) -> Result<(), String> {
    let object = exact_object(
        value,
        &[
            "max_heap_bytes",
            "max_output_pages",
            "max_workspace_pages",
            "max_open_files",
        ],
    )?;
    for field in ["max_heap_bytes", "max_output_pages", "max_workspace_pages"] {
        reader::positive_u64_string(object[field].as_str())
            .map_err(|error| format!("immutable_feed_budget.{field}: {error}"))?;
    }
    reader::positive_u32(&object["max_open_files"])
        .map_err(|error| format!("immutable_feed_budget.max_open_files: {error}"))?;
    Ok(())
}

fn immutable_budget(value: &Value) -> Result<ImmutableFeedBudget, String> {
    let object = value
        .as_object()
        .ok_or("immutable_feed_budget must be an object")?;
    Ok(ImmutableFeedBudget::new(
        reader::u64_string(object["max_heap_bytes"].as_str())?,
        reader::u64_string(object["max_output_pages"].as_str())?,
        reader::u64_string(object["max_workspace_pages"].as_str())?,
        object["max_open_files"]
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .ok_or("max_open_files must be u32")?,
    ))
}

fn exact_object<'a>(
    value: &'a Value,
    fields: &[&str],
) -> Result<&'a serde_json::Map<String, Value>, String> {
    let object = value.as_object().ok_or("value must be an object")?;
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

fn input_error(error: crate::io::input::InputError) -> HandlerError {
    HandlerError::new(error.code(), "not_started", error.message().to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn current_publish_normalizes_text_into_one_cataloged_feed() {
        let unique = format!(
            "iprange-current-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
        );
        let input = std::env::temp_dir().join(format!("{unique}.txt"));
        let destination = std::env::temp_dir().join(format!("{unique}.iprange"));
        std::fs::write(
            &input,
            b"# comment\n10.0.0.1\n10.0.0.0/24\n10.0.0.10 - 10.0.0.8\n",
        )
        .unwrap();

        let params = json!({
            "input": {
                "paths": [input.display().to_string()],
                "family": "ipv4",
                "fix_network": true,
                "default_prefix": 32,
                "dns": {"threads": 1, "silent": true},
                "expand_at_paths": true,
                "max_line_bytes": 1048576,
                "max_expanded_paths": 100000
            },
            "feed": "feed-a",
            "value_tag": {"hex":"aa"},
            "metadata": {"mode":"clear"},
            "destination": destination.display().to_string(),
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "2097152",
                "max_output_pages": "10000",
                "max_workspace_pages": "10000",
                "max_open_files": 3
            }
        });
        validate_current_publish(&params).unwrap();
        let mut state = SessionState::default();
        let result = current_publish(&mut state, params).unwrap();
        assert_eq!(result["method"], "iprange.v1.current.publish");
        assert_eq!(result["report"]["input_record_count"], "3");
        assert_eq!(result["report"]["normalized_interval_count"], "1");
        assert_eq!(result["report"]["addresses"], "256");
        assert_eq!(result["publication"]["publication"], "published");

        let reader = iprange_livedb::ImmutableReader::open(&destination).unwrap();
        assert!(reader.lookup_feed("feed-a").unwrap().is_some());
        assert!(reader.metadata_json().unwrap().is_none());
        std::fs::remove_file(input).unwrap();
        std::fs::remove_file(destination).unwrap();
    }

    #[test]
    fn empty_input_still_publishes_the_named_empty_feed() {
        let unique = format!(
            "iprange-current-empty-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
        );
        let input = std::env::temp_dir().join(format!("{unique}.txt"));
        let destination = std::env::temp_dir().join(format!("{unique}.iprange"));
        std::fs::write(&input, b"# empty\n").unwrap();
        let params = json!({
            "input": {
                "paths": [input.display().to_string()],
                "family": "ipv6",
                "fix_network": true,
                "default_prefix": 128,
                "dns": {"threads": 1, "silent": true},
                "expand_at_paths": true,
                "max_line_bytes": 1048576,
                "max_expanded_paths": 100000
            },
            "feed": "empty-feed",
            "value_tag": {"text":"feeds"},
            "metadata": {"mode":"clear"},
            "destination": destination.display().to_string(),
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "2097152",
                "max_output_pages": "10000",
                "max_workspace_pages": "10000",
                "max_open_files": 3
            }
        });
        let mut state = SessionState::default();
        let result = current_publish(&mut state, params).unwrap();
        assert_eq!(result["report"]["input_record_count"], "0");
        assert_eq!(result["report"]["addresses"], "0");
        let reader = iprange_livedb::ImmutableReader::open(&destination).unwrap();
        assert!(reader.lookup_feed("empty-feed").unwrap().is_some());
        std::fs::remove_file(input).unwrap();
        std::fs::remove_file(destination).unwrap();
    }
}

#[cfg(test)]
mod error_tests {
    use super::*;
    use serde_json::json;

    fn params(input: &str, destination: &str) -> Value {
        json!({
            "input": {
                "paths": [input],
                "family": "ipv4",
                "fix_network": true,
                "default_prefix": 32,
                "dns": {"threads": 1, "silent": true},
                "expand_at_paths": true,
                "max_line_bytes": 1048576,
                "max_expanded_paths": 100000
            },
            "feed": "feed-a",
            "value_tag": {"hex":"aa"},
            "metadata": {"mode":"clear"},
            "destination": destination,
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "2097152",
                "max_output_pages": "10000",
                "max_workspace_pages": "10000",
                "max_open_files": 3
            }
        })
    }

    #[test]
    fn missing_input_and_invalid_input_have_distinct_adapter_codes() {
        let mut state = SessionState::default();
        let missing = current_publish(
            &mut state,
            params("/definitely/missing/iprange-input", "/tmp/unused.iprange"),
        )
        .unwrap_err();
        assert_eq!(
            (missing.code, missing.outcome),
            ("invalid_path", "not_started")
        );

        let directory = std::env::temp_dir().join(format!(
            "iprange-current-invalid-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
        ));
        std::fs::create_dir(&directory).unwrap();
        let input = directory.join("input.txt");
        let destination = directory.join("output.iprange");
        std::fs::write(&input, b"this is not an iprange line\n").unwrap();
        let invalid = current_publish(
            &mut state,
            params(
                &input.display().to_string(),
                &destination.display().to_string(),
            ),
        )
        .unwrap_err();
        assert_eq!(
            (invalid.code, invalid.outcome),
            ("input_format", "not_started")
        );
        assert!(!destination.exists());
        std::fs::remove_file(input).unwrap();
        std::fs::remove_dir(directory).unwrap();
    }
}

#[cfg(test)]
mod positive_budget_tests {
    use super::*;

    #[test]
    fn immutable_budget_rejects_zero_but_accepts_positive_limits() {
        let valid = json!({
            "max_heap_bytes": "1",
            "max_output_pages": "1",
            "max_workspace_pages": "1",
            "max_open_files": 1
        });
        assert_eq!(validate_immutable_budget(&valid), Ok(()));
        for field in ["max_heap_bytes", "max_output_pages", "max_workspace_pages"] {
            let mut zero = valid.clone();
            zero[field] = json!("0");
            assert!(validate_immutable_budget(&zero).is_err());
        }
        let mut zero_files = valid.clone();
        zero_files["max_open_files"] = json!(0);
        assert!(validate_immutable_budget(&zero_files).is_err());
    }
}
